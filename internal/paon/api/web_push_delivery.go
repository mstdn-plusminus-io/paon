package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const (
	webPushTTL                   = 48 * 60 * 60
	webPushUrgency               = webpush.UrgencyNormal
	webPushDeliveryRetryKey      = "paon:webpush:delivery:retry"
	webPushDeliveryRetryAttempts = 5
)

type webPushDeliverFunc func(context.Context, config.Config, models.WebPushSubscription, []byte) (*http.Response, error)

type webPushData struct {
	Policy        string          `json:"policy"`
	PolicyDefault bool            `json:"-"`
	Alerts        map[string]bool `json:"alerts"`
}

type webPushNotificationPayload struct {
	AccessToken      string `json:"access_token"`
	PreferredLocale  string `json:"preferred_locale"`
	NotificationID   string `json:"notification_id"`
	NotificationType string `json:"notification_type"`
	Icon             string `json:"icon"`
	Title            string `json:"title"`
	Body             string `json:"body"`
}

type webPushDeliveryTarget struct {
	Subscription models.WebPushSubscription
	AccessToken  string
	Locale       string
}

type webPushDeliveryRetryJob struct {
	SubscriptionID int64 `json:"subscription_id"`
	NotificationID int64 `json:"notification_id"`
	Attempts       int   `json:"attempts"`
	CreatedAt      int64 `json:"created_at"`
}

func defaultWebPushDeliverer(ctx context.Context, cfg config.Config, subscription models.WebPushSubscription, payload []byte) (*http.Response, error) {
	if cfg.VapidPublicKey == "" || cfg.VapidPrivateKey == "" {
		return nil, errors.New("web push VAPID keys are not configured")
	}
	return webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys: webpush.Keys{
			Auth:   subscription.KeyAuth,
			P256dh: subscription.KeyP256dh,
		},
	}, &webpush.Options{
		Subscriber:      cfg.VapidSubject,
		TTL:             webPushTTL,
		Urgency:         webPushUrgency,
		VAPIDPublicKey:  cfg.VapidPublicKey,
		VAPIDPrivateKey: cfg.VapidPrivateKey,
	})
}

func (s *Server) deliverWebPushNotification(ctx context.Context, notification models.Notification, recipient models.Account) {
	if s == nil || s.db == nil || !webPushConfigured(s.cfg) {
		return
	}
	if !s.webPushNotificationActivityPresent(ctx, notification) {
		return
	}
	targets, err := s.webPushDeliveryTargets(ctx, notification, recipient)
	if err != nil {
		return
	}
	for _, target := range targets {
		if !webPushSubscriptionPushable(s.db.WithContext(ctx), target.Subscription, notification) {
			continue
		}
		if s.enqueueWebPushNotificationTask(target.Subscription.ID, notification.ID) {
			continue
		}
		payload, err := webPushNotificationJSON(s.cfg, target, notification)
		if err != nil {
			continue
		}
		if retry := s.deliverWebPushTargetOnce(ctx, target.Subscription, payload); retry {
			s.enqueueWebPushDeliveryRetry(target.Subscription.ID, notification.ID)
		}
	}
}

func (s *Server) performWebPushNotificationDelivery(ctx context.Context, subscriptionID int64, notificationID int64) error {
	var subscription models.WebPushSubscription
	if err := s.db.WithContext(ctx).Where("id = ?", subscriptionID).First(&subscription).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var notification models.Notification
	if err := s.db.WithContext(ctx).
		Preload("FromAccount.AccountStat").
		Where("id = ?", notificationID).
		First(&notification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	notifications := []models.Notification{notification}
	if err := s.hydrateNotificationStatuses(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationReports(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationAccounts(notifications); err != nil {
		return err
	}
	notification = notifications[0]
	if !s.webPushNotificationActivityPresent(ctx, notification) || !webPushSubscriptionPushable(s.db.WithContext(ctx), subscription, notification) {
		return nil
	}
	token, locale, ok := s.webPushDeliveryRetryTokenAndLocale(ctx, subscription)
	if !ok {
		return nil
	}
	payload, err := webPushNotificationJSON(s.cfg, webPushDeliveryTarget{Subscription: subscription, AccessToken: token, Locale: locale}, notification)
	if err != nil {
		return err
	}
	return s.deliverWebPushTargetForWorker(ctx, subscription, payload)
}

func (s *Server) deliverWebPushTargetForWorker(ctx context.Context, subscription models.WebPushSubscription, payload []byte) error {
	deliver := s.webPushDeliverer
	if deliver == nil {
		deliver = defaultWebPushDeliverer
	}
	response, err := deliver(ctx, s.webPushDeliveryConfig(), subscription, payload)
	if err != nil {
		return err
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if response == nil || (response.StatusCode >= 200 && response.StatusCode < 300) {
		return nil
	}
	if webPushSubscriptionExpired(response.StatusCode) {
		_ = s.db.WithContext(ctx).Delete(&models.WebPushSubscription{}, subscription.ID).Error
		return nil
	}
	return fmt.Errorf("web push endpoint returned status %d", response.StatusCode)
}

func (s *Server) deliverWebPushTargetOnce(ctx context.Context, subscription models.WebPushSubscription, payload []byte) bool {
	deliver := s.webPushDeliverer
	if deliver == nil {
		deliver = defaultWebPushDeliverer
	}
	response, err := deliver(ctx, s.webPushDeliveryConfig(), subscription, payload)
	if err != nil {
		return true
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if response == nil {
		return false
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return false
	}
	if webPushSubscriptionExpired(response.StatusCode) {
		_ = s.db.WithContext(ctx).Delete(&models.WebPushSubscription{}, subscription.ID).Error
		return false
	}
	return true
}

func (s *Server) webPushDeliveryConfig() config.Config {
	if s == nil {
		return config.Config{}
	}
	cfg := s.cfg
	if contactEmail := s.settingStringValue("site_contact_email", ""); strings.TrimSpace(contactEmail) != "" {
		cfg.VapidSubject = "mailto:" + strings.TrimSpace(contactEmail)
	}
	return cfg
}

func (s *Server) enqueueWebPushDeliveryRetry(subscriptionID int64, notificationID int64) {
	if s == nil || s.db == nil || subscriptionID == 0 || notificationID == 0 {
		return
	}
	job := webPushDeliveryRetryJob{
		SubscriptionID: subscriptionID,
		NotificationID: notificationID,
		Attempts:       0,
		CreatedAt:      time.Now().UTC().Unix(),
	}
	_ = s.enqueueWebPushDeliveryRetryJob(context.Background(), job)
}

func (s *Server) enqueueWebPushDeliveryRetryJob(ctx context.Context, job webPushDeliveryRetryJob) error {
	if job.SubscriptionID == 0 || job.NotificationID == 0 {
		return nil
	}
	encoded, runAt, err := nextWebPushDeliveryRetry(job, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = s.redisCommand(ctx, "ZADD", redisConfig(s.cfg).prefix+webPushDeliveryRetryKey, strconv.FormatInt(runAt.Unix(), 10), encoded)
	return err
}

func nextWebPushDeliveryRetry(job webPushDeliveryRetryJob, now time.Time) (string, time.Time, error) {
	job.Attempts++
	runAt := now.UTC().Add(webhookDeliveryRetryDelay(job.Attempts))
	encoded, err := json.Marshal(job)
	return string(encoded), runAt, err
}

func (s *Server) runWebPushDeliveryRetryWorker(ctx context.Context) {
	s.processDueWebPushDeliveryRetries(ctx, 25)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDueWebPushDeliveryRetries(ctx, 25)
		}
	}
}

func (s *Server) processDueWebPushDeliveryRetries(ctx context.Context, limit int) {
	if s == nil || s.db == nil || limit <= 0 {
		return
	}
	key := redisConfig(s.cfg).prefix + webPushDeliveryRetryKey
	now := time.Now().UTC()
	claims, err := s.claimRedisRetryJobs(ctx, key, limit, now)
	if err != nil {
		return
	}
	for _, claim := range claims {
		var job webPushDeliveryRetryJob
		if err := json.Unmarshal([]byte(claim.Member), &job); err != nil {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		if err := s.performWebPushNotificationDelivery(ctx, job.SubscriptionID, job.NotificationID); err == nil || job.Attempts >= webPushDeliveryRetryAttempts {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		successor, runAt, err := nextWebPushDeliveryRetry(job, now)
		if err != nil {
			continue
		}
		_ = s.replaceRedisRetryJob(ctx, key, claim, successor, runAt)
	}
}

func (s *Server) performWebPushDeliveryRetry(ctx context.Context, job webPushDeliveryRetryJob) {
	if err := s.performWebPushNotificationDelivery(ctx, job.SubscriptionID, job.NotificationID); err != nil && job.Attempts < webPushDeliveryRetryAttempts {
		_ = s.enqueueWebPushDeliveryRetryJob(ctx, job)
	}
}

func (s *Server) webPushDeliveryRetryTokenAndLocale(ctx context.Context, subscription models.WebPushSubscription) (string, string, bool) {
	var user models.User
	err := s.db.WithContext(ctx).Where("id = ? AND disabled = false", subscription.UserID.Int64).First(&user).Error
	if !subscription.UserID.Valid || err != nil {
		var activation models.SessionActivation
		if activationErr := s.db.WithContext(ctx).Where("web_push_subscription_id = ?", subscription.ID).First(&activation).Error; activationErr != nil {
			return "", "", false
		}
		if err := s.db.WithContext(ctx).Where("id = ? AND disabled = false", activation.UserID).First(&user).Error; err != nil {
			return "", "", false
		}
	}
	token, err := s.webPushAccessToken(ctx, subscription, user.ID)
	if err != nil || token == "" {
		return "", "", false
	}
	return token, webPushNullStringValue(user.Locale, s.cfg.DefaultLocale), true
}

func webPushConfigured(cfg config.Config) bool {
	return strings.TrimSpace(cfg.VapidPublicKey) != "" && strings.TrimSpace(cfg.VapidPrivateKey) != ""
}

func (s *Server) webPushNotificationActivityPresent(ctx context.Context, notification models.Notification) bool {
	if s == nil || s.db == nil || notification.ActivityID == 0 {
		return false
	}
	table := notificationActivityTable(notification.ActivityType, notification.ResolvedType())
	if table == "" {
		return false
	}
	var count int64
	if err := s.db.WithContext(ctx).Table(table).Where("id = ?", notification.ActivityID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func notificationActivityTable(activityType string, kind string) string {
	switch activityType {
	case "Mention":
		return "mentions"
	case "Status":
		return "statuses"
	case "Follow":
		return "follows"
	case "FollowRequest":
		return "follow_requests"
	case "Favourite":
		return "favourites"
	case "Poll":
		return "polls"
	case "Report":
		return "reports"
	case "Account":
		return "accounts"
	}
	switch kind {
	case "mention":
		return "mentions"
	case "status", "reblog", "update":
		return "statuses"
	case "follow":
		return "follows"
	case "follow_request":
		return "follow_requests"
	case "favourite":
		return "favourites"
	case "poll":
		return "polls"
	case "admin.report":
		return "reports"
	case "admin.sign_up":
		return "accounts"
	default:
		return ""
	}
}

func (s *Server) webPushDeliveryTargets(ctx context.Context, notification models.Notification, recipient models.Account) ([]webPushDeliveryTarget, error) {
	var user models.User
	err := s.db.WithContext(ctx).Where("account_id = ? AND disabled = false", recipient.ID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var subscriptions []models.WebPushSubscription
	if err := s.db.WithContext(ctx).Where("user_id = ?", user.ID).Find(&subscriptions).Error; err != nil {
		return nil, err
	}
	targets := make([]webPushDeliveryTarget, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		token, err := s.webPushAccessToken(ctx, subscription, user.ID)
		if err != nil || token == "" {
			continue
		}
		targets = append(targets, webPushDeliveryTarget{
			Subscription: subscription,
			AccessToken:  token,
			Locale:       webPushNullStringValue(user.Locale, s.cfg.DefaultLocale),
		})
	}
	return targets, nil
}

func (s *Server) webPushAccessToken(ctx context.Context, subscription models.WebPushSubscription, userID int64) (string, error) {
	if subscription.AccessTokenID.Valid {
		var token models.OAuthAccessToken
		err := s.db.WithContext(ctx).
			Where("id = ?", subscription.AccessTokenID.Int64).
			First(&token).Error
		if err != nil {
			return "", err
		}
		return token.Token, nil
	}
	return s.findOrCreateWebPushAccessToken(ctx, userID)
}

func (s *Server) findOrCreateWebPushAccessToken(ctx context.Context, userID int64) (string, error) {
	if s == nil || s.db == nil || userID == 0 {
		return "", gorm.ErrInvalidDB
	}
	var token models.OAuthAccessToken
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var superapp oauthApplication
		applicationID := sql.NullInt64{}
		if err := tx.Select("id").Where("superapp = ?", true).First(&superapp).Error; err == nil {
			applicationID = sql.NullInt64{Int64: superapp.ID, Valid: true}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		query := tx.Where("resource_owner_id = ? AND revoked_at IS NULL", userID)
		if applicationID.Valid {
			query = query.Where("application_id = ?", applicationID.Int64)
		} else {
			query = query.Where("application_id IS NULL")
		}
		var existing []models.OAuthAccessToken
		if err := query.Order("created_at DESC, id DESC").Find(&existing).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for i := range existing {
			if oauthAccessTokenReusable(existing[i], "read write follow push", now) {
				token = existing[i]
				return nil
			}
		}
		token = models.OAuthAccessToken{
			Token:           randomHex(32),
			CreatedAt:       now,
			Scopes:          "read write follow push",
			ApplicationID:   applicationID,
			ResourceOwnerID: sql.NullInt64{Int64: userID, Valid: true},
		}
		return tx.Create(&token).Error
	})
	if err != nil {
		return "", err
	}
	return token.Token, nil
}

func webPushSubscriptionPushable(tx *gorm.DB, subscription models.WebPushSubscription, notification models.Notification) bool {
	data := parseWebPushData(subscription.Data)
	if !data.Alerts[notification.ResolvedType()] {
		return false
	}
	if data.PolicyDefault {
		return true
	}
	switch data.Policy {
	case "all":
		return true
	case "none":
		return false
	case "followed":
		return webPushFollowExists(tx, notification.AccountID, notification.FromAccountID)
	case "follower":
		return webPushFollowExists(tx, notification.FromAccountID, notification.AccountID)
	default:
		return false
	}
}

func webPushFollowExists(tx *gorm.DB, accountID int64, targetAccountID int64) bool {
	if tx == nil || accountID == 0 || targetAccountID == 0 {
		return false
	}
	var count int64
	if err := tx.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func webPushNotificationJSON(cfg config.Config, target webPushDeliveryTarget, notification models.Notification) ([]byte, error) {
	account := serializer.AccountFromModel(cfg, notification.FromAccount)
	payload := webPushNotificationPayload{
		AccessToken:      target.AccessToken,
		PreferredLocale:  target.Locale,
		NotificationID:   strconv.FormatInt(notification.ID, 10),
		NotificationType: notification.ResolvedType(),
		Icon:             account.AvatarStatic,
		Title:            webPushNotificationTitle(notification, target.Locale),
		Body:             webPushNotificationBody(notification),
	}
	return json.Marshal(payload)
}

func parseWebPushData(raw models.JSONValue) webPushData {
	out := webPushData{PolicyDefault: true, Alerts: map[string]bool{}}
	if len(raw) == 0 {
		return out
	}
	var decoded struct {
		Policy json.RawMessage `json:"policy"`
		Alerts map[string]any  `json:"alerts"`
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return out
	}
	if decoded.Policy != nil && string(decoded.Policy) != "null" {
		out.PolicyDefault = false
		var policy string
		if json.Unmarshal(decoded.Policy, &policy) == nil {
			out.Policy = policy
		}
	}
	for key, value := range decoded.Alerts {
		out.Alerts[key] = railsBooleanCastForWebPushDelivery(value)
	}
	return out
}

func railsBooleanCastForWebPushDelivery(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != "" && !railsFalseStringForWebPushDelivery(typed)
	case float64:
		return typed != 0
	default:
		return true
	}
}

func railsFalseStringForWebPushDelivery(value string) bool {
	switch value {
	case "false", "FALSE", "0", "f", "F", "off", "OFF":
		return true
	default:
		return false
	}
}

func webPushNotificationTitle(notification models.Notification, locale string) string {
	name := notification.FromAccount.DisplayName
	if strings.TrimSpace(name) == "" {
		name = notification.FromAccount.Username
	}
	key := "notification_mailer." + notification.ResolvedType() + ".subject"
	if strings.HasPrefix(notification.ResolvedType(), "admin.") {
		key = "notification_mailer.admin." + strings.TrimPrefix(notification.ResolvedType(), "admin.") + ".subject"
	}
	if value := webT(locale, key, map[string]string{"name": name}); value != key && strings.TrimSpace(value) != "" {
		return value
	}
	switch notification.ResolvedType() {
	case "mention":
		return "You were mentioned by " + name
	case "status":
		return name + " just posted"
	case "reblog":
		return name + " boosted your post"
	case "follow":
		return name + " is now following you"
	case "follow_request":
		return "Pending follower: " + name
	case "favourite":
		return name + " favorited your post"
	case "poll":
		return "A poll by " + name + " has ended"
	case "admin.sign_up":
		return name + " signed up"
	case "admin.report":
		return name + " submitted a report"
	case "update":
		return name + " edited a post"
	default:
		return name
	}
}

func webPushNotificationBody(notification models.Notification) string {
	value := notification.FromAccount.Note
	if notification.TargetStatus != nil {
		value = firstNonEmpty(notification.TargetStatus.SpoilerText, notification.TargetStatus.Text, value)
	}
	return truncateRunes(strings.TrimSpace(stripHTML(value)), 140)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func webPushNullStringValue(value sql.NullString, fallback string) string {
	if value.Valid && strings.TrimSpace(value.String) != "" {
		return strings.TrimSpace(value.String)
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	return "en"
}

func webPushSubscriptionExpired(status int) bool {
	return status >= 400 && status <= 499 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}
