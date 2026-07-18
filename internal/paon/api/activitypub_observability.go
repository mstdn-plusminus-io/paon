package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
)

const activityPubResponseLogLimit = 2 * 1024
const activityPubLogValueLimit = 512

type activityPubLogFields struct {
	Type   string
	ID     string
	Actor  string
	Object string
}

func activityPubLogFieldsFromBody(body []byte) activityPubLogFields {
	var raw map[string]any
	if json.Unmarshal(body, &raw) != nil {
		return activityPubLogFields{}
	}
	return activityPubLogFields{
		Type:   activityJSONLDActivityType(raw),
		ID:     activityRawValueOrID(raw["id"]),
		Actor:  activityRawValueOrID(raw["actor"]),
		Object: activityRawValueOrID(raw["object"]),
	}.sanitized()
}

func activityPubLogFieldsFromPayload(payload activityPayload) activityPubLogFields {
	return activityPubLogFields{
		Type:   payload.Type,
		ID:     activityPayloadIDValueOrID(payload),
		Actor:  activityPayloadActorValueOrID(payload),
		Object: payload.Object.ID,
	}.sanitized()
}

func (fields activityPubLogFields) sanitized() activityPubLogFields {
	fields.Type = activityPubSafeLogValue(fields.Type, activityPubLogValueLimit)
	fields.ID = activityPubSafeLogValue(fields.ID, activityPubLogValueLimit)
	fields.Actor = activityPubSafeLogValue(fields.Actor, activityPubLogValueLimit)
	fields.Object = activityPubSafeLogValue(fields.Object, activityPubLogValueLimit)
	return fields
}

func activityPubSafeLogValue(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.ToValidUTF8(value, "?")), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "..."
}

func activityPubResponseSnippet(reader io.Reader) string {
	if reader == nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(reader, activityPubResponseLogLimit+1))
	if err != nil {
		return "<response body read failed: " + activityPubSafeLogValue(err.Error(), 256) + ">"
	}
	truncated := len(raw) > activityPubResponseLogLimit
	if truncated {
		raw = raw[:activityPubResponseLogLimit]
	}
	value := activityPubSafeLogValue(string(raw), activityPubResponseLogLimit)
	if truncated && !strings.HasSuffix(value, "...") {
		value += "..."
	}
	return value
}

func activityPubRequestLogContext(c *echo.Context) (requestID string, path string) {
	if c == nil {
		return "", ""
	}
	requestID = c.Response().Header().Get(echo.HeaderXRequestID)
	if requestID == "" {
		requestID, _ = c.Get("request_id").(string)
	}
	if request := c.Request(); request != nil && request.URL != nil {
		path = request.URL.Path
	}
	return activityPubSafeLogValue(requestID, 256), activityPubSafeLogValue(path, 512)
}

func logActivityPubIngressIssue(c *echo.Context, event string, reason string, body []byte, err error) {
	requestID, path := activityPubRequestLogContext(c)
	fields := activityPubLogFieldsFromBody(body)
	log.Printf("activitypub ingress %s request_id=%q path=%q reason=%q activity_type=%q activity_id=%q actor=%q object=%q error=%q",
		event, requestID, path, reason, fields.Type, fields.ID, fields.Actor, fields.Object, activityPubErrorLogValue(err))
}

func logActivityPubProcessingIssue(event string, reason string, body []byte, actorID int64, deliveredTo int64, err error) {
	fields := activityPubLogFieldsFromBody(body)
	log.Printf("activitypub processing %s reason=%q actor_id=%d delivered_to_account_id=%d activity_type=%q activity_id=%q actor=%q object=%q error=%q",
		event, reason, actorID, deliveredTo, fields.Type, fields.ID, fields.Actor, fields.Object, activityPubErrorLogValue(err))
}

func activityPubErrorLogValue(err error) string {
	if err == nil {
		return ""
	}
	return activityPubSafeLogValue(err.Error(), 4*1024)
}

func activityPubProcessingError(body []byte, actorID int64, deliveredTo int64, err error) error {
	if err == nil {
		return nil
	}
	fields := activityPubLogFieldsFromBody(body)
	return fmt.Errorf("activitypub processing actor_id=%d delivered_to_account_id=%d activity_type=%q activity_id=%q actor=%q object=%q: %w",
		actorID, deliveredTo, fields.Type, fields.ID, fields.Actor, fields.Object, err)
}

func logActivityPubUnsupportedPayload(payload activityPayload, actorID int64, reason string) {
	fields := activityPubLogFieldsFromPayload(payload)
	log.Printf("activitypub processing ignored reason=%q actor_id=%d activity_type=%q activity_id=%q actor=%q object_type=%q object=%q",
		reason, actorID, fields.Type, fields.ID, fields.Actor, activityPubSafeLogValue(payload.Object.TypeExact, activityPubLogValueLimit), fields.Object)
}

func logActivityPubDeliveryRejected(sourceAccountID int64, inboxURL string, status int, response string) {
	log.Printf("activitypub delivery rejected source_account_id=%d inbox=%q status=%d response=%q",
		sourceAccountID, activityPubSafeLogValue(inboxURL, activityPubLogValueLimit), status, activityPubSafeLogValue(response, activityPubResponseLogLimit))
}
