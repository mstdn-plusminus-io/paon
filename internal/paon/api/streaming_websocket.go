package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v5"
)

const websocketKeepAliveInterval = 30 * time.Second

const websocketBinaryMessageCloseReason = "The mastodon streaming server does not support binary messages"

type websocketCommand struct {
	Type   string         `json:"type"`
	Stream any            `json:"stream"`
	Params map[string]any `json:"-"`
}

type websocketSubscription struct {
	cancel context.CancelFunc
}

func (s *Server) streamingWebSocket(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "private, no-store")
	session, err := s.currentStreamingSession(c)
	if err != nil || session.Account == nil {
		return apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	ws, err := upgradeWebSocket(c)
	if err != nil {
		return err
	}
	defer ws.Close()
	releaseClientMetric := s.streamMetrics.trackClient(streamingMetricWebSocket)
	defer releaseClientMetric()

	ctx, cancel := context.WithCancel(c.Request().Context())
	defer cancel()
	if after := streamingWebSocketDisconnectAfter(c); after > 0 {
		timer := time.AfterFunc(after, func() {
			cancel()
			_ = ws.WriteClose()
		})
		defer timer.Stop()
	}
	subscriptions := map[string]websocketSubscription{}
	defer func() {
		for key, subscription := range subscriptions {
			subscription.cancel()
			delete(subscriptions, key)
		}
	}()
	if session.AccessTokenID != 0 {
		s.subscribeWebSocketSystem(ctx, cancel, ws, session, subscriptions)
	}
	keepAlive := startWebSocketKeepAlive(ctx, cancel, ws, websocketKeepAliveInterval)

	if channel := streamingChannel(c); channel != "" {
		if err := s.subscribeWebSocketStream(ctx, ws, session, subscriptions, channel, c.QueryParams()); err != nil {
			_ = ws.WriteText(websocketError(err.Error()))
		}
	}

	for {
		frame, err := ws.ReadFrame()
		if err != nil {
			return nil
		}
		switch frame.Opcode {
		case wsOpcodeClose:
			_ = ws.WriteClose()
			return nil
		case wsOpcodePing:
			_ = ws.WritePong(frame.Payload)
		case wsOpcodePong:
			keepAlive.markAlive()
		case wsOpcodeBinary:
			_ = ws.WriteCloseWithCode(1003, websocketBinaryMessageCloseReason)
			return nil
		case wsOpcodeText:
			command, err := parseWebSocketCommand(frame.Payload)
			if err != nil {
				continue
			}
			channel := firstWebSocketParam(command.Stream)
			params := webSocketCommandParams(command)
			switch command.Type {
			case "subscribe":
				if err := s.subscribeWebSocketStream(ctx, ws, session, subscriptions, channel, params); err != nil {
					_ = ws.WriteText(websocketError(err.Error()))
				}
			case "unsubscribe":
				if err := s.unsubscribeWebSocketStream(session, subscriptions, channel, params); err != nil {
					_ = ws.WriteText(websocketError("Error unsubscribing from channel"))
				}
			}
		}
	}
}

func streamingWebSocketDisconnectAfter(c *echo.Context) time.Duration {
	raw := c.QueryParam("x-disconnect-after")
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

type websocketKeepAlive struct {
	alive atomic.Bool
}

func startWebSocketKeepAlive(ctx context.Context, cancel context.CancelFunc, ws *websocketConn, interval time.Duration) *websocketKeepAlive {
	keepAlive := &websocketKeepAlive{}
	keepAlive.markAlive()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !keepAlive.alive.Swap(false) {
					cancel()
					_ = ws.Close()
					return
				}
				if err := ws.WritePing(nil); err != nil {
					cancel()
					_ = ws.Close()
					return
				}
			}
		}
	}()
	return keepAlive
}

func (k *websocketKeepAlive) markAlive() {
	k.alive.Store(true)
}

func (s *Server) subscribeWebSocketStream(ctx context.Context, ws *websocketConn, session streamingSession, subscriptions map[string]websocketSubscription, channel string, params url.Values) error {
	if !streamingKnownChannel(channel) {
		return errStreamingBadRequest("Unknown stream type")
	}
	channel = normalizeWebSocketChannel(channel, params)
	if !streamingSessionCanRead(session, channel) {
		return errStreamingBadRequest("Access token does not cover required scopes")
	}
	channelIDs, err := s.streamingChannelIDsFromParams(channel, session.Account, params)
	if err != nil {
		return err
	}
	if len(channelIDs) == 0 {
		if channel == "list" {
			return errStreamingBadRequest("Not authorized to stream this list")
		}
		return errStreamingBadRequest("Not authorized to stream this channel")
	}
	channelIDs = streamingChannelIDsForSession(channel, channelIDs, session)
	session.FilterLocal, session.FilterRemote = s.streamingFeedFilters(channel, session)
	key := strings.Join(channelIDs, ";")
	if _, ok := subscriptions[key]; ok {
		return nil
	}

	subCtx, cancel := context.WithCancel(ctx)
	releaseChannelMetric := s.streamMetrics.trackChannel(streamingMetricWebSocket, channel, 1)
	subscriptions[key] = websocketSubscription{cancel: func() {
		cancel()
		releaseChannelMetric()
	}}
	events := make(chan redisMessage, 32)
	go func() {
		_ = s.subscribeRedis(subCtx, channelIDs, events)
	}()
	go s.keepRedisSubscribed(subCtx, channelIDs)

	streamName := websocketStreamName(channel, params)
	needsFiltering := streamingChannelNeedsFiltering(channel)
	filterContext := streamingChannelFilterContext(channel)
	go func() {
		for {
			select {
			case <-subCtx.Done():
				return
			case event := <-events:
				if needsFiltering {
					var ok bool
					event, ok = s.filterStreamingMessage(session, event, filterContext)
					if !ok {
						continue
					}
				}
				s.streamMetrics.incrementMessagesSent(streamingMetricWebSocket)
				_ = ws.WriteText(websocketEvent(streamName, event))
			}
		}
	}()
	return nil
}

func (s *Server) unsubscribeWebSocketStream(session streamingSession, subscriptions map[string]websocketSubscription, channel string, params url.Values) error {
	if channel == "" {
		return errStreamingBadRequest("Unknown stream type")
	}
	channel = normalizeWebSocketChannel(channel, params)
	if !streamingSessionCanRead(session, channel) {
		return errStreamingBadRequest("Access token does not cover required scopes")
	}
	channelIDs, err := s.streamingChannelIDsFromParams(channel, session.Account, params)
	if err != nil {
		return err
	}
	if len(channelIDs) == 0 {
		return errStreamingBadRequest("Not authorized to stream this channel")
	}
	channelIDs = streamingChannelIDsForSession(channel, channelIDs, session)
	key := strings.Join(channelIDs, ";")
	if subscription, ok := subscriptions[key]; ok {
		subscription.cancel()
		delete(subscriptions, key)
	}
	return nil
}

func (s *Server) subscribeWebSocketSystem(ctx context.Context, rootCancel context.CancelFunc, ws *websocketConn, session streamingSession, subscriptions map[string]websocketSubscription) {
	channels := []string{
		"timeline:access_token:" + strconv.FormatInt(session.AccessTokenID, 10),
		"timeline:system:" + strconv.FormatInt(session.Account.ID, 10),
	}
	key := strings.Join(channels, ";")
	subCtx, cancel := context.WithCancel(ctx)
	releaseChannelMetric := s.streamMetrics.trackChannel(streamingMetricWebSocket, "system", int64(len(channels)))
	subscriptions[key] = websocketSubscription{cancel: func() {
		cancel()
		releaseChannelMetric()
	}}
	events := make(chan redisMessage, 4)
	go func() {
		_ = s.subscribeRedis(subCtx, channels, events)
	}()
	go func() {
		for {
			select {
			case <-subCtx.Done():
				return
			case event := <-events:
				if event.Event == "kill" {
					rootCancel()
					_ = ws.WriteClose()
					return
				}
			}
		}
	}()
}

func parseWebSocketCommand(payload []byte) (websocketCommand, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return websocketCommand{}, err
	}
	return websocketCommand{
		Type:   firstWebSocketParam(raw["type"]),
		Stream: raw["stream"],
		Params: raw,
	}, nil
}

func webSocketCommandParams(command websocketCommand) url.Values {
	params := url.Values{}
	for key, value := range command.Params {
		if key == "type" || key == "stream" {
			continue
		}
		if text := firstWebSocketParam(value); text != "" {
			params.Set(key, text)
		}
	}
	return params
}

func firstWebSocketParam(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		if len(v) == 0 {
			return ""
		}
		return firstWebSocketParam(v[0])
	default:
		return ""
	}
}

func normalizeWebSocketChannel(channel string, params url.Values) string {
	channel = strings.ReplaceAll(channel, "/", ":")
	if channel == "public" && truthy(params.Get("only_media")) {
		return "public:media"
	}
	if channel == "public:local" && truthy(params.Get("only_media")) {
		return "public:local:media"
	}
	if channel == "public:remote" && truthy(params.Get("only_media")) {
		return "public:remote:media"
	}
	return channel
}

func websocketStreamName(channel string, params url.Values) []string {
	if channel == "list" {
		return []string{channel, params.Get("list")}
	}
	if channel == "hashtag" || channel == "hashtag:local" {
		return []string{channel, params.Get("tag")}
	}
	return []string{channel}
}

func websocketEvent(stream []string, message redisMessage) []byte {
	payload, _ := json.Marshal(map[string]any{
		"stream":  stream,
		"event":   message.Event,
		"payload": ssePayload(message.Payload),
	})
	return payload
}

func websocketError(message string) []byte {
	payload, _ := json.Marshal(map[string]string{"error": message})
	return payload
}
