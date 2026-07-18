package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const webhookDeliveryTimeout = 10 * time.Second
const webhookDeliveryHistoryMaxItems = 50
const webhookDeliveryHistoryTTL = 14 * 24 * time.Hour

var webhookTemplateExpression = regexp.MustCompile(`\{\{[a-z_]+(\.([a-z_]+|[0-9]+))*\}\}`)
var webhookTemplatePath = regexp.MustCompile(`^[a-z_]+(\.([a-z_]+|[0-9]+))*$`)
var webhookHTTPClient = &http.Client{Timeout: webhookDeliveryTimeout}

type webhookDeliveryHistoryItem struct {
	DeliveredAt int64           `json:"delivered_at"`
	Status      string          `json:"status"`
	Event       string          `json:"event"`
	HTTPStatus  int             `json:"http_status,omitempty"`
	Error       string          `json:"error,omitempty"`
	Body        json.RawMessage `json:"body"`
}

func webhookEventBody(event string, object any, createdAt time.Time) ([]byte, error) {
	return json.Marshal(serializer.AdminWebhookEvent{
		Event:     event,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
		Object:    object,
	})
}

func webhookSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func webhookHandlesEvent(webhook models.Webhook, event string) bool {
	if !webhook.Enabled {
		return false
	}
	for _, candidate := range webhook.Events {
		if candidate == event {
			return true
		}
	}
	return false
}

func filterEnabledWebhooksForEvent(webhooks []models.Webhook, event string) []models.Webhook {
	out := make([]models.Webhook, 0, len(webhooks))
	for _, webhook := range webhooks {
		if webhookHandlesEvent(webhook, event) {
			out = append(out, webhook)
		}
	}
	return out
}

func enabledWebhooksForEvent(tx *gorm.DB, event string) ([]models.Webhook, error) {
	var webhooks []models.Webhook
	if tx == nil {
		return webhooks, nil
	}
	if err := tx.Where("enabled = ? AND ? = ANY(events)", true, event).Find(&webhooks).Error; err != nil {
		return nil, err
	}
	return webhooks, nil
}

func renderWebhookTemplate(body []byte, template string) ([]byte, error) {
	if strings.TrimSpace(template) == "" {
		return body, nil
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	rendered := webhookTemplateExpression.ReplaceAllStringFunc(template, func(match string) string {
		path := strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}")
		value := webhookTemplateValue(document, strings.Split(path, "."))
		return webhookTemplateString(value)
	})
	return []byte(rendered), nil
}

func webhookTemplateValid(template string) bool {
	if strings.TrimSpace(template) == "" {
		return true
	}
	for i := 0; i < len(template); {
		start := strings.Index(template[i:], "{{")
		if start < 0 {
			return true
		}
		start += i
		end := strings.Index(template[start+2:], "}}")
		if end < 0 {
			return false
		}
		end += start + 2
		path := template[start+2 : end]
		if path != "" && !webhookTemplatePath.MatchString(path) {
			return false
		}
		i = end + 2
	}
	return true
}

func webhookTemplateValue(document any, path []string) any {
	current := document
	for _, segment := range path {
		switch typed := current.(type) {
		case map[string]any:
			current = typed[segment]
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil
			}
			current = typed[index]
		default:
			return nil
		}
	}
	return current
}

func webhookTemplateString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func (s *Server) triggerWebhookEvent(event string, object any) {
	if s == nil || s.db == nil {
		return
	}
	createdAt := time.Now().UTC()
	body, err := webhookEventBody(event, object, createdAt)
	if err != nil {
		return
	}
	webhooks, err := enabledWebhooksForEvent(s.db, event)
	if err != nil || len(webhooks) == 0 {
		return
	}
	for _, webhook := range webhooks {
		webhook := webhook
		if s.enqueueWebhookDeliveryTask(webhook.ID, body) {
			continue
		}
		go func() {
			if err := s.deliverWebhook(webhook, body); err != nil {
				s.enqueueWebhookDeliveryRetry(webhook.ID, body)
			}
		}()
	}
}

func (s *Server) triggerAccountWebhook(event string, accountID int64) {
	if s == nil || s.db == nil {
		return
	}
	account, err := s.findAccountByID(strconv.FormatInt(accountID, 10))
	if err != nil {
		return
	}
	s.meiliIndexAccountBestEffort(context.Background(), account.ID)
	if !account.Local() {
		return
	}
	if s.enqueueTriggerWebhookTask(event, "Account", accountID) {
		return
	}
	s.triggerWebhookEvent(event, serializer.AdminAccountFromModel(s.cfg, *account))
}

func (s *Server) triggerStatusWebhook(event string, statusID int64) {
	if s == nil || s.db == nil {
		return
	}
	status, err := s.findStatus(strconv.FormatInt(statusID, 10))
	if err != nil || !statusLocalLikeRails(*status) {
		return
	}
	if s.enqueueTriggerWebhookTask(event, "Status", statusID) {
		return
	}
	s.triggerWebhookEvent(event, serializer.StatusFromModel(s.cfg, *status, nil))
}

func (s *Server) triggerWebhookForRecord(ctx context.Context, event string, className string, id int64) error {
	switch strings.TrimSpace(className) {
	case "Account":
		account, err := s.findAccountByID(strconv.FormatInt(id, 10))
		if err != nil {
			return gorm.ErrRecordNotFound
		}
		s.meiliIndexAccountBestEffort(ctx, account.ID)
		if !account.Local() {
			return nil
		}
		s.triggerWebhookEvent(event, serializer.AdminAccountFromModel(s.cfg, *account))
	case "Status":
		status, err := s.findStatus(strconv.FormatInt(id, 10))
		if err != nil {
			return gorm.ErrRecordNotFound
		}
		if !statusLocalLikeRails(*status) {
			return nil
		}
		s.triggerWebhookEvent(event, serializer.StatusFromModel(s.cfg, *status, nil))
	case "Report":
		return s.triggerReportWebhookNow(event, id)
	default:
		return nil
	}
	return nil
}

func (s *Server) deliverWebhook(webhook models.Webhook, body []byte) error {
	finalBody, err := renderWebhookTemplate(body, webhook.Template.String)
	if err != nil {
		s.recordWebhookDeliveryHistory(webhook, body, "failure", 0, err)
		return err
	}
	req, err := http.NewRequest(http.MethodPost, webhook.URL, bytes.NewReader(finalBody))
	if err != nil {
		s.recordWebhookDeliveryHistory(webhook, body, "failure", 0, err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", paonUserAgent(s.cfg))
	req.Header.Set("X-Hub-Signature", webhookSignature(webhook.Secret, finalBody))

	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		s.recordWebhookDeliveryHistory(webhook, body, "failure", 0, err)
		return err
	}
	defer resp.Body.Close()
	if webhookDeliveryResponseSuccessfulOrUnsalvageable(resp.StatusCode) {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			s.recordWebhookDeliveryHistory(webhook, body, "discarded", resp.StatusCode, nil)
			return nil
		}
		s.recordWebhookDeliveryHistory(webhook, body, "success", resp.StatusCode, nil)
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("webhook delivery failed with status %d", resp.StatusCode)
		s.recordWebhookDeliveryHistory(webhook, body, "failure", resp.StatusCode, err)
		return err
	}
	s.recordWebhookDeliveryHistory(webhook, body, "success", resp.StatusCode, nil)
	return nil
}

func webhookDeliveryResponseSuccessfulOrUnsalvageable(status int) bool {
	if status >= 200 && status < 300 {
		return true
	}
	return status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}

func (s *Server) recordWebhookDeliveryHistory(webhook models.Webhook, body []byte, status string, httpStatus int, deliveryErr error) {
	if s == nil || webhook.ID == 0 || len(body) == 0 {
		return
	}
	item := webhookDeliveryHistoryItem{
		DeliveredAt: time.Now().UTC().Unix(),
		Status:      status,
		Event:       webhookRetryEvent(body),
		HTTPStatus:  httpStatus,
		Body:        append(json.RawMessage(nil), body...),
	}
	if deliveryErr != nil {
		item.Error = deliveryErr.Error()
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := s.webhookDeliveryHistoryRedisKey(webhook.ID)
	_, _ = s.redisCommand(ctx, "LPUSH", key, string(encoded))
	_, _ = s.redisCommand(ctx, "LTRIM", key, "0", strconv.Itoa(webhookDeliveryHistoryMaxItems-1))
	_, _ = s.redisCommand(ctx, "EXPIRE", key, strconv.FormatInt(int64(webhookDeliveryHistoryTTL/time.Second), 10))
}

func (s *Server) webhookDeliveryHistoryRedisKey(webhookID int64) string {
	return redisConfig(s.cfg).prefix + "paon:webhooks:delivery:history:" + strconv.FormatInt(webhookID, 10)
}
