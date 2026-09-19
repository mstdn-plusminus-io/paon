package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

const (
	asynqTaskEmailSubscriptionConfirmation = "email_subscription:confirmation"
	asynqTaskEmailSubscriptionDistribution = "email_subscription:distribution"
	asynqTaskEmailSubscriptionDelivery     = "email_subscription:delivery"
	emailSubscriptionDistributionDelay     = 5 * time.Minute
	emailSubscriptionBatchTTL              = time.Hour
	emailSubscriptionDeliveryMarkerTTL     = 7 * 24 * time.Hour
	emailSubscriptionDeliveryClaimTTL      = 5 * time.Minute
	emailSubscriptionUnconfirmedTTL        = 7 * 24 * time.Hour
)

type asynqEmailSubscriptionPayload struct {
	Version        int     `json:"version"`
	SubscriptionID int64   `json:"subscription_id,omitempty"`
	AccountID      int64   `json:"account_id,omitempty"`
	StatusIDs      []int64 `json:"status_ids,omitempty"`
	BatchID        string  `json:"batch_id,omitempty"`
}

func emailSubscriptionConfirmationToken() string {
	return strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
}

func emailSubscriptionBatchKey(prefix string, accountID int64) string {
	return prefix + "email_subscriptions:" + strconv.FormatInt(accountID, 10) + ":next_batch"
}

func normalizedEmailSubscriptionStatusIDs(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	compact := out[:0]
	for _, id := range out {
		if id <= 0 || len(compact) > 0 && compact[len(compact)-1] == id {
			continue
		}
		compact = append(compact, id)
	}
	return compact
}

func emailSubscriptionDeliveryBatchID(accountID int64, statusIDs []int64) string {
	ids := normalizedEmailSubscriptionStatusIDs(statusIDs)
	var value strings.Builder
	value.WriteString(strconv.FormatInt(accountID, 10))
	for _, id := range ids {
		value.WriteByte(':')
		value.WriteString(strconv.FormatInt(id, 10))
	}
	sum := sha256.Sum256([]byte(value.String()))
	return fmt.Sprintf("%x", sum[:16])
}

func emailSubscriptionDeliveryMarkerKey(prefix string, subscriptionID int64, batchID string) string {
	return prefix + "email_subscriptions:delivery:" + strconv.FormatInt(subscriptionID, 10) + ":" + batchID
}

func newEmailSubscriptionDeliveryTask(subscriptionID, accountID int64, statusIDs []int64, queue string) (*asynq.Task, error) {
	statusIDs = normalizedEmailSubscriptionStatusIDs(statusIDs)
	if subscriptionID <= 0 || accountID <= 0 || len(statusIDs) == 0 {
		return nil, fmt.Errorf("email subscription delivery: subscription, account, and statuses are required")
	}
	batchID := emailSubscriptionDeliveryBatchID(accountID, statusIDs)
	payload, err := marshalAsynqTaskPayload(asynqEmailSubscriptionPayload{
		SubscriptionID: subscriptionID,
		AccountID:      accountID,
		StatusIDs:      statusIDs,
		BatchID:        batchID,
	})
	if err != nil {
		return nil, err
	}
	taskID := "email-subscription-" + strconv.FormatInt(subscriptionID, 10) + "-" + batchID
	return asynq.NewTask(asynqTaskEmailSubscriptionDelivery, payload,
		asynq.Queue(queue), asynq.MaxRetry(25), asynq.Unique(emailSubscriptionDeliveryMarkerTTL), asynq.TaskID(taskID)), nil
}

func (s *Server) enqueueEmailSubscriptionDelivery(ctx context.Context, subscriptionID, accountID int64, statusIDs []int64) error {
	if s == nil || s.asynqClient == nil {
		return fmt.Errorf("email subscription delivery: asynq client is not configured")
	}
	task, err := newEmailSubscriptionDeliveryTask(subscriptionID, accountID, statusIDs, s.asynqQueue(asynqQueueMailers))
	if err != nil {
		return err
	}
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	if asynqEnqueueAccepted(err) {
		return nil
	}
	return fmt.Errorf("enqueue email subscription delivery: %w", err)
}

func enqueueEmailSubscriptionDeliveries(subscriptions []models.EmailSubscription, enqueue func(subscriptionID int64) error) error {
	var enqueueErrors []error
	for _, subscription := range subscriptions {
		if err := enqueue(subscription.ID); err != nil {
			enqueueErrors = append(enqueueErrors, err)
		}
	}
	return errors.Join(enqueueErrors...)
}

func dispatchEmailSubscriptionBatch(subscriptions []models.EmailSubscription, enqueue func(subscriptionID int64) error, acknowledge func() error) error {
	if err := enqueueEmailSubscriptionDeliveries(subscriptions, enqueue); err != nil {
		return err
	}
	return acknowledge()
}

func (s *Server) emailSubscriptionsAllowedForAccount(ctx context.Context, accountID int64) bool {
	if s == nil || s.db == nil || !s.cfg.EmailSubscriptionsEnabled || accountID == 0 || !s.settingBoolValue("email_subscriptions", false) {
		return false
	}
	var user models.User
	if err := s.db.WithContext(ctx).Where("account_id = ?", accountID).First(&user).Error; err != nil {
		return false
	}
	return s.userCanAny(&user, rolePermissionAdministrator, rolePermissionManageEmailSubscriptions) && userSettingBool(user, "email_subscriptions", false)
}

func emailSubscriptionStatusEligible(status models.Status) bool {
	return status.ID != 0 && status.AccountID != 0 && status.Visibility == 0 && !status.ReblogOfID.Valid &&
		(!status.InReplyToID.Valid || status.InReplyToAccountID.Valid && status.InReplyToAccountID.Int64 == status.AccountID)
}

func (s *Server) enqueueEmailSubscriptionStatus(ctx context.Context, status models.Status) bool {
	if s == nil || s.asynqClient == nil || !emailSubscriptionStatusEligible(status) || !s.emailSubscriptionsAllowedForAccount(ctx, status.AccountID) {
		return false
	}
	key := emailSubscriptionBatchKey(redisConfig(s.cfg).prefix, status.AccountID)
	if _, err := s.redisCommand(ctx, "SADD", key, strconv.FormatInt(status.ID, 10)); err != nil {
		return false
	}
	_, _ = s.redisCommand(ctx, "EXPIRE", key, strconv.FormatInt(int64(emailSubscriptionBatchTTL/time.Second), 10))
	payload, err := marshalAsynqTaskPayload(asynqEmailSubscriptionPayload{AccountID: status.AccountID})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskEmailSubscriptionDistribution, payload, asynq.Queue(s.asynqQueue(asynqQueueMailers)), asynq.MaxRetry(5))
	_, err = s.asynqClient.EnqueueContext(ctx, task, asynq.ProcessIn(emailSubscriptionDistributionDelay), asynq.Unique(emailSubscriptionBatchTTL))
	return asynqEnqueueAccepted(err)
}

func (s *Server) enqueueEmailSubscriptionConfirmation(subscriptionID int64) error {
	if s == nil || s.asynqClient == nil || subscriptionID == 0 {
		return fmt.Errorf("email subscription confirmation: asynq client is not configured")
	}
	payload, err := marshalAsynqTaskPayload(asynqEmailSubscriptionPayload{SubscriptionID: subscriptionID})
	if err != nil {
		return err
	}
	task := asynq.NewTask(asynqTaskEmailSubscriptionConfirmation, payload, asynq.Queue(s.asynqQueue(asynqQueueMailers)), asynq.MaxRetry(25))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return err
}

func (s *Server) handleAsynqEmailSubscriptionConfirmation(ctx context.Context, task *asynq.Task) error {
	var payload asynqEmailSubscriptionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("email subscription confirmation: %w", err)
	}
	if err := validateAsynqPayloadVersion("email subscription confirmation", payload.Version); err != nil {
		return err
	}
	var subscription models.EmailSubscription
	if err := s.db.WithContext(ctx).Preload("Account").Where("id = ?", payload.SubscriptionID).First(&subscription).Error; err != nil {
		return workerLookupError("email subscription confirmation lookup", err)
	}
	return sendMail(s.cfg, emailSubscriptionConfirmationMessage(s, subscription))
}

func emailSubscriptionConfirmationMessage(s *Server, subscription models.EmailSubscription) mailMessage {
	accountName := accountDisplayName(subscription.Account)
	locale := firstNonEmpty(strings.TrimSpace(subscription.Locale), "en")
	confirmationURL := s.cfg.BaseURL() + "/email_subscriptions/confirmation?" + url.Values{"confirmation_token": []string{subscription.ConfirmationToken.String}}.Encode()
	vars := map[string]string{"name": accountName, "acct": subscription.Account.Acct()}
	subject := settingsT(locale, "email_subscription_mailer.confirmation.subject", "Confirm your email address")
	body := settingsTVars(locale, "email_subscription_mailer.confirmation.instructions_to_confirm", "Confirm you'd like to receive emails from %{name} (@%{acct}) when they publish new posts.", vars) + "\n\n" + confirmationURL + "\n\n" + settingsT(locale, "email_subscription_mailer.confirmation.instructions_to_ignore", "If you're not sure why you received this email, you can delete it. You will not be subscribed if you don't click on the link above.")
	label := settingsT(locale, "email_subscription_mailer.confirmation.action", "Confirm email address")
	return mailMessage{To: subscription.Email, Subject: subject, Body: body, HTMLBody: "<p>" + html.EscapeString(subject) + "</p><p><a href=\"" + html.EscapeString(confirmationURL) + "\">" + html.EscapeString(label) + "</a></p>"}
}

func (s *Server) handleAsynqEmailSubscriptionDistribution(ctx context.Context, task *asynq.Task) error {
	var payload asynqEmailSubscriptionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("email subscription distribution: %w", err)
	}
	if err := validateAsynqPayloadVersion("email subscription distribution", payload.Version); err != nil {
		return err
	}
	if !s.emailSubscriptionsAllowedForAccount(ctx, payload.AccountID) {
		return nil
	}
	key := emailSubscriptionBatchKey(redisConfig(s.cfg).prefix, payload.AccountID)
	value, err := s.redisCommand(ctx, "SMEMBERS", key)
	if err != nil {
		return err
	}
	items, ok := redisStringArray(value)
	if !ok || len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		if id, parseErr := strconv.ParseInt(item, 10, 64); parseErr == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		_, _ = s.redisCommand(ctx, append([]string{"SREM", key}, items...)...)
		return nil
	}
	ids = normalizedEmailSubscriptionStatusIDs(ids)
	var statuses []models.Status
	if err := s.db.WithContext(ctx).
		Preload("Account").
		Where("id IN ? AND account_id = ? AND visibility = 0 AND reblog_of_id IS NULL AND (in_reply_to_id IS NULL OR in_reply_to_account_id = account_id)", ids, payload.AccountID).
		Order("id DESC").Find(&statuses).Error; err != nil {
		return err
	}
	if len(statuses) == 0 {
		_, _ = s.redisCommand(ctx, append([]string{"SREM", key}, items...)...)
		return nil
	}
	statusIDs := make([]int64, 0, len(statuses))
	for _, status := range statuses {
		statusIDs = append(statusIDs, status.ID)
	}
	var subscriptions []models.EmailSubscription
	if err := s.db.WithContext(ctx).Preload("Account").Where("account_id = ? AND confirmed_at IS NOT NULL", payload.AccountID).Find(&subscriptions).Error; err != nil {
		return err
	}
	return dispatchEmailSubscriptionBatch(subscriptions, func(subscriptionID int64) error {
		return s.enqueueEmailSubscriptionDelivery(ctx, subscriptionID, payload.AccountID, statusIDs)
	}, func() error {
		_, err := s.redisCommand(ctx, append([]string{"SREM", key}, items...)...)
		return err
	})
}

func (s *Server) handleAsynqEmailSubscriptionDelivery(ctx context.Context, task *asynq.Task) error {
	var payload asynqEmailSubscriptionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("email subscription delivery: %w", err)
	}
	if err := validateAsynqPayloadVersion("email subscription delivery", payload.Version); err != nil {
		return err
	}
	payload.StatusIDs = normalizedEmailSubscriptionStatusIDs(payload.StatusIDs)
	if payload.SubscriptionID <= 0 || payload.AccountID <= 0 || len(payload.StatusIDs) == 0 || payload.BatchID != emailSubscriptionDeliveryBatchID(payload.AccountID, payload.StatusIDs) {
		return fmt.Errorf("email subscription delivery payload is invalid: %w", asynq.SkipRetry)
	}
	markerKey := emailSubscriptionDeliveryMarkerKey(redisConfig(s.cfg).prefix, payload.SubscriptionID, payload.BatchID)
	if value, err := s.redisCommand(ctx, "GET", markerKey); err != nil {
		return err
	} else if state, ok := redisStringValue(value); ok && state == "sent" {
		return nil
	}
	claim, err := s.redisCommand(ctx, "SET", markerKey, "sending", "NX", "EX", strconv.FormatInt(int64(emailSubscriptionDeliveryClaimTTL/time.Second), 10))
	if err != nil {
		return err
	}
	if !redisOK(claim) {
		return fmt.Errorf("email subscription delivery is already in progress")
	}
	releaseClaim := true
	defer func() {
		if releaseClaim {
			_, _ = s.redisCommand(context.Background(), "DEL", markerKey)
		}
	}()

	var subscription models.EmailSubscription
	if err := s.db.WithContext(ctx).Preload("Account").Where("id = ? AND account_id = ? AND confirmed_at IS NOT NULL", payload.SubscriptionID, payload.AccountID).First(&subscription).Error; err != nil {
		return workerLookupError("email subscription delivery lookup", err)
	}
	if !s.emailSubscriptionsAllowedForAccount(ctx, payload.AccountID) {
		return nil
	}
	var statuses []models.Status
	if err := s.db.WithContext(ctx).Preload("Account").Preload("MediaAttachments").
		Where("id IN ? AND account_id = ? AND visibility = 0 AND reblog_of_id IS NULL AND (in_reply_to_id IS NULL OR in_reply_to_account_id = account_id)", payload.StatusIDs, payload.AccountID).
		Order("id ASC").Find(&statuses).Error; err != nil {
		return err
	}
	if len(statuses) == 0 {
		return nil
	}
	if err := sendMail(s.cfg, emailSubscriptionNotificationMessage(s, subscription, statuses)); err != nil {
		return err
	}
	_, _ = s.redisCommand(ctx, "SETEX", markerKey, strconv.FormatInt(int64(emailSubscriptionDeliveryMarkerTTL/time.Second), 10), "sent")
	releaseClaim = false
	return nil
}

func emailSubscriptionNotificationMessage(s *Server, subscription models.EmailSubscription, statuses []models.Status) mailMessage {
	name := accountDisplayName(subscription.Account)
	locale := firstNonEmpty(strings.TrimSpace(subscription.Locale), "en")
	subject := settingsTVars(locale, "email_subscription_mailer.notification.subject.plural", "New posts from %{name}", map[string]string{"name": name})
	if len(statuses) == 1 {
		excerpt := []rune(strings.TrimSpace(stripHTML(statuses[0].Text)))
		if len(excerpt) > 17 {
			excerpt = append(excerpt[:14], '.', '.', '.')
		}
		subject = settingsTVars(locale, "email_subscription_mailer.notification.subject.singular", `New post: "%{excerpt}"`, map[string]string{"name": name, "excerpt": string(excerpt)})
	}
	var body strings.Builder
	for _, status := range statuses {
		text := strings.TrimSpace(stripHTML(status.Text))
		if text != "" {
			body.WriteString(text)
			body.WriteString("\n")
		}
		body.WriteString(s.quoteStatusURL(status))
		body.WriteString("\n\n")
	}
	unsubscribeToken := railsSignedGlobalIDForModel("EmailSubscription", subscription.ID, railsSignedGlobalIDPurposeUnsubscribe, s.cfg.SecretKeyBase)
	unsubscribeURL := s.cfg.BaseURL() + "/unsubscribe?" + url.Values{"token": []string{unsubscribeToken}}.Encode()
	body.WriteString(settingsT(locale, "email_subscriptions.unsubscribe.label", "Unsubscribe") + ": " + unsubscribeURL + "\n")
	return mailMessage{
		To: subscription.Email, Subject: subject, Body: body.String(), HTMLBody: emailSubscriptionNotificationHTML(s, subscription, statuses, unsubscribeURL, locale), Bulk: true,
		Headers: []mailHeader{
			{Key: "List-ID", Value: "<" + subscription.Account.Username + "." + s.cfg.LocalDomain + ">"},
			{Key: "List-Unsubscribe", Value: "<" + unsubscribeURL + ">"},
			{Key: "List-Unsubscribe-Post", Value: "List-Unsubscribe=One-Click"},
		},
	}
}

func emailSubscriptionNotificationHTML(s *Server, subscription models.EmailSubscription, statuses []models.Status, unsubscribeURL string, locale string) string {
	var body strings.Builder
	body.WriteString(`<div class="email-subscription-posts">`)
	for _, status := range statuses {
		statusURL := s.quoteStatusURL(status)
		body.WriteString(`<article class="email-subscription-post">`)
		if spoiler := strings.TrimSpace(status.SpoilerText); spoiler != "" {
			body.WriteString(`<p><strong>` + html.EscapeString(spoiler) + `</strong></p>`)
		}
		if text := strings.TrimSpace(stripHTML(status.Text)); text != "" {
			body.WriteString(`<p>` + strings.ReplaceAll(html.EscapeString(text), "\n", `<br>`) + `</p>`)
		}
		for _, attachment := range activityPubOrderedMediaAttachments(status) {
			media := serializer.MediaAttachmentFromModel(s.cfg, attachment)
			if media.URL == "" {
				continue
			}
			if media.Type == "image" {
				body.WriteString(`<p><img src="` + html.EscapeString(media.URL) + `" alt="` + html.EscapeString(media.Description) + `" style="max-width:100%;height:auto"></p>`)
			} else {
				body.WriteString(`<p><a href="` + html.EscapeString(media.URL) + `">` + html.EscapeString(media.URL) + `</a></p>`)
			}
		}
		body.WriteString(`<p><a href="` + html.EscapeString(statusURL) + `">` + html.EscapeString(statusURL) + `</a></p></article>`)
	}
	body.WriteString(`</div><p><a href="` + html.EscapeString(unsubscribeURL) + `">` + html.EscapeString(settingsT(locale, "email_subscriptions.unsubscribe.label", "Unsubscribe")) + `</a></p>`)
	return body.String()
}

func (s *Server) cleanupUnconfirmedEmailSubscriptions(ctx context.Context, cutoff time.Time) int {
	if s == nil || s.db == nil {
		return 0
	}
	result := s.db.WithContext(ctx).Where("confirmed_at IS NULL AND created_at <= ?", cutoff).Delete(&models.EmailSubscription{})
	if result.Error != nil {
		return 0
	}
	return int(result.RowsAffected)
}
