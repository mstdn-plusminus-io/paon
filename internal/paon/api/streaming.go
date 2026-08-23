package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type streamingSession struct {
	Account         *models.Account
	AccessTokenID   int64
	Scopes          string
	ChosenLanguages []string
	CanViewFeeds    bool
	FilterLocal     bool
	FilterRemote    bool
}

func (s *Server) streamingHealth(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "private, no-store")
	return c.String(http.StatusOK, "OK")
}

func (s *Server) streaming(c *echo.Context) error {
	if isWebSocketRequest(c) {
		return s.streamingWebSocket(c)
	}
	setStreamingCORSHeaders(c)
	channel := streamingSSEChannel(c)
	if !streamingKnownChannel(channel) {
		return apiError(c, http.StatusBadRequest, "Error: Unknown channel requested")
	}
	session, accountErr := s.currentStreamingSession(c)
	if err := streamingSSEAccessError(session, accountErr, channel); err != nil {
		return apiError(c, http.StatusUnauthorized, err.Error())
	}
	channelIDs, err := s.streamingChannelIDs(c, channel, session.Account)
	if err != nil || len(channelIDs) == 0 {
		return apiError(c, http.StatusNotFound, "Not found")
	}
	channelIDs = streamingChannelIDsForSession(channel, channelIDs, session)
	session.FilterLocal, session.FilterRemote = s.streamingFeedFilters(channel, session)
	systemChannelIDs := streamingSystemChannelIDs(session)
	channelIDs = append(channelIDs, systemChannelIDs...)

	releaseClientMetric := s.streamMetrics.trackClient(streamingMetricEventSource)
	defer releaseClientMetric()
	releaseChannelMetric := s.streamMetrics.trackChannel(streamingMetricEventSource, channel, 1)
	defer releaseChannelMetric()
	releaseSystemMetric := s.streamMetrics.trackChannel(streamingMetricEventSource, "system", int64(len(systemChannelIDs)))
	defer releaseSystemMetric()

	ctx := c.Request().Context()
	events := make(chan redisMessage, 32)
	needsFiltering := streamingChannelNeedsFiltering(channel)
	filterContext := streamingChannelFilterContext(channel)
	if len(channelIDs) > 0 {
		go func() {
			_ = s.subscribeRedis(ctx, channelIDs, events)
		}()
		go s.keepRedisSubscribed(ctx, channelIDs)
	}

	response := c.Response()
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "private, no-store")
	response.Header().Set("Connection", "keep-alive")
	response.WriteHeader(http.StatusOK)
	flusher, _ := response.(http.Flusher)
	if _, err := response.Write([]byte(":)\n\n")); err != nil {
		return nil
	}
	if flusher != nil {
		flusher.Flush()
	}

	var disconnect <-chan time.Time
	if after := streamingDisconnectAfter(c); after > 0 {
		timer := time.NewTimer(after)
		defer timer.Stop()
		disconnect = timer.C
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-disconnect:
			return nil
		case event := <-events:
			if event.Event == "kill" {
				return nil
			}
			if event.Event == "filters_changed" && streamingSystemChannel(event.Channel) {
				continue
			}
			if needsFiltering {
				var ok bool
				event, ok = s.filterStreamingMessage(session, event, filterContext)
				if !ok {
					continue
				}
			}
			s.streamMetrics.incrementMessagesSent(streamingMetricEventSource)
			if _, err := response.Write(sseEvent(event)); err != nil {
				return nil
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-ticker.C:
			if _, err := response.Write([]byte(":thump\n\n")); err != nil {
				return nil
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func setStreamingCORSHeaders(c *echo.Context) {
	header := c.Response().Header()
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Allow-Headers", "Authorization, Accept, Cache-Control, X-Disconnect-After")
	header.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
}

func streamingSSEChannel(c *echo.Context) string {
	onlyMedia := truthy(c.QueryParam("only_media"))
	switch c.Request().URL.Path {
	case "/api/v1/streaming/user":
		return "user"
	case "/api/v1/streaming/user/notification":
		return "user:notification"
	case "/api/v1/streaming/public":
		if onlyMedia {
			return "public:media"
		}
		return "public"
	case "/api/v1/streaming/public/local":
		if onlyMedia {
			return "public:local:media"
		}
		return "public:local"
	case "/api/v1/streaming/public/remote":
		if onlyMedia {
			return "public:remote:media"
		}
		return "public:remote"
	case "/api/v1/streaming/hashtag":
		return "hashtag"
	case "/api/v1/streaming/hashtag/local":
		return "hashtag:local"
	case "/api/v1/streaming/direct":
		return "direct"
	case "/api/v1/streaming/list":
		return "list"
	default:
		return ""
	}
}

func streamingDisconnectAfter(c *echo.Context) time.Duration {
	raw := firstNonEmpty(c.Request().Header.Get("X-Disconnect-After"), c.QueryParam("x-disconnect-after"), c.QueryParam("X-Disconnect-After"))
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func streamingSystemChannelIDs(session streamingSession) []string {
	if session.Account == nil || session.AccessTokenID == 0 {
		return nil
	}
	return []string{
		"timeline:access_token:" + strconv.FormatInt(session.AccessTokenID, 10),
		"timeline:system:" + strconv.FormatInt(session.Account.ID, 10),
	}
}

func streamingSystemChannel(channel string) bool {
	return strings.Contains(channel, "timeline:system:")
}

func (s *Server) currentStreamingSession(c *echo.Context) (streamingSession, error) {
	user, token, err := s.currentUser(c)
	if err != nil {
		return streamingSession{}, err
	}
	var account models.Account
	if err := s.db.Preload("AccountStat").Where("id = ? AND suspended_at IS NULL", user.AccountID).First(&account).Error; err != nil {
		return streamingSession{}, err
	}
	var accessToken models.OAuthAccessToken
	_ = s.db.Select("id, scopes").Where("token = ? AND revoked_at IS NULL", token).First(&accessToken).Error
	session := streamingSession{Account: &account, AccessTokenID: accessToken.ID, Scopes: string(accessToken.Scopes), ChosenLanguages: []string(user.ChosenLanguages), CanViewFeeds: s.userCan(user, rolePermissionViewFeeds)}
	return session, nil
}

func (s *Server) streamingFeedFilters(channel string, session streamingSession) (bool, bool) {
	if session.CanViewFeeds {
		return false, false
	}
	kind := ""
	switch {
	case strings.HasPrefix(channel, "public"):
		kind = "live"
	case strings.HasPrefix(channel, "hashtag"):
		kind = "topic"
	default:
		return false, false
	}
	localDisabled := normalizeTimelineAccess(s.settingStringValue("local_"+kind+"_feed_access", timelineAccessPublic)) == timelineAccessDisabled
	remoteDisabled := normalizeTimelineAccess(s.settingStringValue("remote_"+kind+"_feed_access", timelineAccessPublic)) == timelineAccessDisabled
	if strings.Contains(channel, ":local") {
		remoteDisabled = false
	}
	if strings.Contains(channel, ":remote") {
		localDisabled = false
	}
	return localDisabled, remoteDisabled
}

func streamingChannel(c *echo.Context) string {
	path := strings.TrimPrefix(c.Request().URL.Path, "/api/v1/streaming")
	path = strings.Trim(path, "/")
	if path == "" {
		path = strings.Trim(c.QueryParam("stream"), "/")
	}
	path = strings.ReplaceAll(path, "/", ":")
	switch path {
	case "public":
		if truthy(c.QueryParam("only_media")) {
			return "public:media"
		}
	case "public:local":
		if truthy(c.QueryParam("only_media")) {
			return "public:local:media"
		}
	case "public:remote":
		if truthy(c.QueryParam("only_media")) {
			return "public:remote:media"
		}
	}
	return path
}

func streamingKnownChannel(channel string) bool {
	switch channel {
	case "user", "user:notification", "public", "public:local", "public:remote",
		"public:media", "public:local:media", "public:remote:media", "direct",
		"hashtag", "hashtag:local", "list":
		return true
	default:
		return false
	}
}

func (s *Server) streamingChannelIDs(c *echo.Context, channel string, account *models.Account) ([]string, error) {
	return s.streamingChannelIDsFromParams(channel, account, c.QueryParams())
}

func (s *Server) streamingChannelIDsFromParams(channel string, account *models.Account, params url.Values) ([]string, error) {
	switch channel {
	case "user":
		if account == nil {
			return nil, nil
		}
		accountID := strconv.FormatInt(account.ID, 10)
		return []string{"timeline:" + accountID}, nil
	case "user:notification":
		if account == nil {
			return nil, nil
		}
		return []string{"timeline:" + strconv.FormatInt(account.ID, 10) + ":notifications"}, nil
	case "public":
		return []string{"timeline:public"}, nil
	case "public:local":
		return []string{"timeline:public:local"}, nil
	case "public:remote":
		return []string{"timeline:public:remote"}, nil
	case "public:media":
		return []string{"timeline:public:media"}, nil
	case "public:local:media":
		return []string{"timeline:public:local:media"}, nil
	case "public:remote:media":
		return []string{"timeline:public:remote:media"}, nil
	case "direct":
		if account == nil {
			return nil, nil
		}
		return []string{"timeline:direct:" + strconv.FormatInt(account.ID, 10)}, nil
	case "hashtag":
		tag := normalizeStreamingHashtag(params.Get("tag"))
		if tag == "" {
			return nil, errStreamingBadRequest("No tag for stream provided")
		}
		return []string{"timeline:hashtag:" + tag}, nil
	case "hashtag:local":
		tag := normalizeStreamingHashtag(params.Get("tag"))
		if tag == "" {
			return nil, errStreamingBadRequest("No tag for stream provided")
		}
		return []string{"timeline:hashtag:" + tag + ":local"}, nil
	case "list":
		if account == nil {
			return nil, nil
		}
		listID := firstNonEmpty(params.Get("list"), params.Get("list_id"))
		if listID == "" {
			return nil, errStreamingBadRequest("Not authorized to stream this list")
		}
		if s.db != nil {
			var count int64
			if err := s.db.Table("lists").Where("id = ? AND account_id = ?", listID, account.ID).Count(&count).Error; err != nil {
				return nil, err
			}
			if count == 0 {
				return nil, nil
			}
		}
		return []string{"timeline:list:" + listID}, nil
	default:
		return nil, nil
	}
}

func streamingChannelNeedsFiltering(channel string) bool {
	return channel == "user" ||
		channel == "user:notification" ||
		channel == "direct" ||
		channel == "public" ||
		channel == "public:local" ||
		channel == "public:remote" ||
		channel == "public:media" ||
		channel == "public:local:media" ||
		channel == "public:remote:media" ||
		channel == "hashtag" ||
		channel == "hashtag:local"
}

func streamingChannelFilterContext(channel string) string {
	if channel == "user" {
		return "home"
	}
	if channel == "user:notification" {
		return "notifications"
	}
	if channel == "direct" {
		return "public"
	}
	if channel == "public" ||
		channel == "public:local" ||
		channel == "public:remote" ||
		channel == "public:media" ||
		channel == "public:local:media" ||
		channel == "public:remote:media" ||
		channel == "hashtag" ||
		channel == "hashtag:local" {
		return "public"
	}
	return ""
}

func streamingSessionCanRead(session streamingSession, channel string) bool {
	if tokenHasAnyScope(session.Scopes, "read") {
		return true
	}
	if channel == "user:notification" {
		return tokenHasAnyScope(session.Scopes, "read:notifications")
	}
	return tokenHasAnyScope(session.Scopes, "read:statuses")
}

func streamingSSEAccessError(session streamingSession, accountErr error, channel string) error {
	if accountErr != nil {
		if accountErr.Error() == "missing token" {
			return streamingClientError("Error: Missing access token")
		}
		return streamingClientError("Error: Invalid access token")
	}
	if session.Account == nil {
		return streamingClientError("Error: Invalid access token")
	}
	if !streamingSessionCanRead(session, channel) {
		return streamingClientError("Error: Access token does not cover required scopes")
	}
	return nil
}

type streamingClientError string

func (err streamingClientError) Error() string {
	return string(err)
}

func errStreamingBadRequest(message string) error {
	return streamingClientError(message)
}

func sseEvent(message redisMessage) []byte {
	return []byte("event: " + message.Event + "\n" + "data: " + ssePayload(message.Payload) + "\n\n")
}

func ssePayload(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func normalizeStreamingHashtag(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '\u00b7' || r == '\u200c' {
			return r
		}
		return -1
	}, value)
}
