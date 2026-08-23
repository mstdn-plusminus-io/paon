package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"golang.org/x/crypto/hkdf"
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
	return deliverWebPushWithClient(ctx, cfg, subscription, payload, http.DefaultClient, time.Now().UTC(), rand.Reader)
}

func deliverWebPushWithClient(ctx context.Context, cfg config.Config, subscription models.WebPushSubscription, payload []byte, client webpush.HTTPClient, now time.Time, entropy io.Reader) (*http.Response, error) {
	if subscription.Standard {
		return deliverStandardWebPush(ctx, cfg, subscription, payload, client, now, entropy)
	}
	return deliverLegacyWebPush(ctx, cfg, subscription, payload, client, now, entropy)
}

func deliverStandardWebPush(ctx context.Context, cfg config.Config, subscription models.WebPushSubscription, payload []byte, client webpush.HTTPClient, now time.Time, entropy io.Reader) (*http.Response, error) {
	authorization, _, err := webPushVAPIDHeaders(cfg, subscription.Endpoint, true, now, entropy)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	client = webPushHeaderClient{
		base: client,
		headers: http.Header{
			"Authorization":   []string{authorization},
			"Unsubscribe-Url": []string{webPushUnsubscribeURL(cfg, subscription, now)},
		},
		setContentLength: true,
	}
	return webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys: webpush.Keys{
			Auth:   subscription.KeyAuth,
			P256dh: subscription.KeyP256dh,
		},
	}, &webpush.Options{
		HTTPClient:      client,
		Subscriber:      cfg.VapidSubject,
		TTL:             webPushTTL,
		Urgency:         webPushUrgency,
		VAPIDPublicKey:  cfg.VapidPublicKey,
		VAPIDPrivateKey: cfg.VapidPrivateKey,
		VapidExpiration: now.UTC().Add(24 * time.Hour),
	})
}

type webPushHeaderClient struct {
	base             webpush.HTTPClient
	headers          http.Header
	setContentLength bool
}

func (client webPushHeaderClient) Do(request *http.Request) (*http.Response, error) {
	for key, values := range client.headers {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if client.setContentLength && request.ContentLength >= 0 {
		request.Header.Set("Content-Length", strconv.FormatInt(request.ContentLength, 10))
	}
	return client.base.Do(request)
}

type webPushLegacyCiphertext struct {
	Ciphertext      []byte
	Salt            []byte
	ServerPublicKey []byte
}

func deliverLegacyWebPush(ctx context.Context, cfg config.Config, subscription models.WebPushSubscription, payload []byte, client webpush.HTTPClient, now time.Time, entropy io.Reader) (*http.Response, error) {
	encrypted, err := encryptLegacyWebPush(payload, subscription.KeyP256dh, subscription.KeyAuth, entropy)
	if err != nil {
		return nil, err
	}
	authorization, vapidCryptoKey, err := webPushVAPIDHeaders(cfg, subscription.Endpoint, false, now, entropy)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, subscription.Endpoint, bytes.NewReader(encrypted.Ciphertext))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("TTL", strconv.Itoa(webPushTTL))
	request.Header.Set("Urgency", string(webPushUrgency))
	request.Header.Set("Content-Encoding", "aesgcm")
	request.Header.Set("Encryption", "salt="+base64.RawURLEncoding.EncodeToString(encrypted.Salt))
	request.Header.Set("Crypto-Key", "dh="+base64.RawURLEncoding.EncodeToString(encrypted.ServerPublicKey)+";"+vapidCryptoKey)
	request.Header.Set("Authorization", authorization)
	if unsubscribeURL := webPushUnsubscribeURL(cfg, subscription, now); unsubscribeURL != "" {
		request.Header.Set("Unsubscribe-URL", unsubscribeURL)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(request)
}

func encryptLegacyWebPush(payload []byte, encodedClientPublicKey string, encodedAuthSecret string, entropy io.Reader) (webPushLegacyCiphertext, error) {
	var out webPushLegacyCiphertext
	if len(payload) == 0 || encodedClientPublicKey == "" || encodedAuthSecret == "" {
		return out, errors.New("web push payload and subscription keys are required")
	}
	clientPublicKey, err := decodeWebPushKey(encodedClientPublicKey)
	if err != nil {
		return out, fmt.Errorf("decode web push p256dh key: %w", err)
	}
	authSecret, err := decodeWebPushKey(encodedAuthSecret)
	if err != nil {
		return out, fmt.Errorf("decode web push auth key: %w", err)
	}
	curve := elliptic.P256()
	clientX, clientY := elliptic.Unmarshal(curve, clientPublicKey)
	if clientX == nil || clientY == nil {
		return out, errors.New("web push p256dh key is not a P-256 point")
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	serverPrivateKey, serverX, serverY, err := elliptic.GenerateKey(curve, entropy)
	if err != nil {
		return out, err
	}
	serverPublicKey := elliptic.Marshal(curve, serverX, serverY)
	sharedX, _ := curve.ScalarMult(clientX, clientY, serverPrivateKey)
	if sharedX == nil {
		return out, errors.New("web push ECDH shared secret is invalid")
	}
	sharedSecret := make([]byte, (curve.Params().BitSize+7)/8)
	sharedX.FillBytes(sharedSecret)

	prk, err := webPushHKDF(sharedSecret, authSecret, []byte("Content-Encoding: auth\x00"), 32)
	if err != nil {
		return out, err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(entropy, salt); err != nil {
		return out, err
	}
	context := legacyWebPushContext(clientPublicKey, serverPublicKey)
	contentEncryptionKey, err := webPushHKDF(prk, salt, append([]byte("Content-Encoding: aesgcm\x00P-256"), context...), 16)
	if err != nil {
		return out, err
	}
	nonce, err := webPushHKDF(prk, salt, append([]byte("Content-Encoding: nonce\x00P-256"), context...), 12)
	if err != nil {
		return out, err
	}
	block, err := aes.NewCipher(contentEncryptionKey)
	if err != nil {
		return out, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return out, err
	}
	paddedPayload := make([]byte, 2, len(payload)+2)
	paddedPayload = append(paddedPayload, payload...)
	out.Ciphertext = gcm.Seal(nil, nonce, paddedPayload, nil)
	out.Salt = salt
	out.ServerPublicKey = serverPublicKey
	return out, nil
}

func legacyWebPushContext(clientPublicKey []byte, serverPublicKey []byte) []byte {
	context := bytes.NewBuffer(make([]byte, 0, 1+2+len(clientPublicKey)+2+len(serverPublicKey)))
	context.WriteByte(0)
	_ = binary.Write(context, binary.BigEndian, uint16(len(clientPublicKey)))
	context.Write(clientPublicKey)
	_ = binary.Write(context, binary.BigEndian, uint16(len(serverPublicKey)))
	context.Write(serverPublicKey)
	return context.Bytes()
}

func webPushHKDF(secret []byte, salt []byte, info []byte, size int) ([]byte, error) {
	key := make([]byte, size)
	read, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, info), key)
	if err != nil {
		return nil, err
	}
	if read != size {
		return nil, io.ErrUnexpectedEOF
	}
	return key, nil
}

func webPushVAPIDHeaders(cfg config.Config, endpoint string, standard bool, now time.Time, entropy io.Reader) (string, string, error) {
	endpointURL, err := url.Parse(endpoint)
	if err != nil || endpointURL.Scheme == "" || endpointURL.Host == "" {
		return "", "", errors.New("web push endpoint is not an absolute URL")
	}
	privateKey, err := webPushVAPIDPrivateKey(cfg.VapidPrivateKey)
	if err != nil {
		return "", "", err
	}
	publicKey, err := decodeWebPushKey(cfg.VapidPublicKey)
	if err != nil {
		return "", "", fmt.Errorf("decode VAPID public key: %w", err)
	}
	publicX, publicY := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if publicX == nil || publicY == nil {
		return "", "", errors.New("VAPID public key is not a P-256 point")
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`))
	claims, err := json.Marshal(struct {
		Audience  string `json:"aud"`
		ExpiresAt int64  `json:"exp"`
		Subject   string `json:"sub"`
	}{
		Audience:  endpointURL.Scheme + "://" + endpointURL.Host,
		ExpiresAt: now.UTC().Add(24 * time.Hour).Unix(),
		Subject:   normalizedVAPIDSubject(cfg.VapidSubject),
	})
	if err != nil {
		return "", "", err
	}
	encodedClaims := base64.RawURLEncoding.EncodeToString(claims)
	signingInput := header + "." + encodedClaims
	digest := sha256.Sum256([]byte(signingInput))
	r, signatureS, err := ecdsa.Sign(entropy, privateKey, digest[:])
	if err != nil {
		return "", "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	signatureS.FillBytes(signature[32:])
	token := signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
	encodedPublicKey := base64.RawURLEncoding.EncodeToString(publicKey)
	if standard {
		return "vapid t=" + token + ",k=" + encodedPublicKey, "", nil
	}
	return "WebPush " + token, "p256ecdsa=" + encodedPublicKey, nil
}

func webPushVAPIDPrivateKey(encoded string) (*ecdsa.PrivateKey, error) {
	raw, err := decodeWebPushKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode VAPID private key: %w", err)
	}
	curve := elliptic.P256()
	d := new(big.Int).SetBytes(raw)
	if d.Sign() <= 0 || d.Cmp(curve.Params().N) >= 0 {
		return nil, errors.New("VAPID private key is outside the P-256 scalar range")
	}
	x, y := curve.ScalarBaseMult(raw)
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}, nil
}

func decodeWebPushKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if decoded, err := encoding.DecodeString(encoded); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 key")
}

func normalizedVAPIDSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if strings.HasPrefix(subject, "mailto:") || strings.HasPrefix(subject, "https:") {
		return subject
	}
	return "mailto:" + subject
}

func webPushUnsubscribeURL(cfg config.Config, subscription models.WebPushSubscription, now time.Time) string {
	token, err := webPushUnsubscribeToken(subscription.ID, cfg.SecretKeyBase, now)
	if err != nil {
		return ""
	}
	return strings.TrimRight(cfg.BaseURL(), "/") + "/api/web/push_subscriptions/" + url.PathEscape(token)
}

func (s *Server) deliverWebPushNotification(ctx context.Context, notification models.Notification, recipient models.Account) {
	if s == nil || s.db == nil || !webPushConfigured(s.cfg) {
		return
	}
	if webPushNotificationOutsideTTL(notification, time.Now().UTC()) {
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
		valid, err := s.prepareWebPushSubscription(ctx, target.Subscription)
		if err != nil {
			s.enqueueWebPushDeliveryRetry(target.Subscription.ID, notification.ID)
			continue
		}
		if !valid {
			continue
		}
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
		return taskTargetError("web push subscription lookup", "local", serverLocalTaskTargetHost(s), err)
	}
	valid, err := s.prepareWebPushSubscription(ctx, subscription)
	if err != nil {
		return taskTargetError("delete invalid web push subscription", "local", serverLocalTaskTargetHost(s), err)
	}
	if !valid {
		return nil
	}
	var notification models.Notification
	if err := s.db.WithContext(ctx).
		Preload("FromAccount.AccountStat").
		Where("id = ?", notificationID).
		First(&notification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return taskTargetError("web push notification lookup", "local", serverLocalTaskTargetHost(s), err)
	}
	notifications := []models.Notification{notification}
	if err := s.hydrateNotificationStatuses(notifications); err != nil {
		return taskTargetError("web push status hydration", "local", serverLocalTaskTargetHost(s), err)
	}
	if err := s.hydrateNotificationReports(notifications); err != nil {
		return taskTargetError("web push report hydration", "local", serverLocalTaskTargetHost(s), err)
	}
	if err := s.hydrateNotificationAccounts(notifications); err != nil {
		return taskTargetError("web push account hydration", "local", serverLocalTaskTargetHost(s), err)
	}
	notification = notifications[0]
	if webPushNotificationOutsideTTL(notification, time.Now().UTC()) {
		return nil
	}
	if !s.webPushNotificationActivityPresent(ctx, notification) || !webPushSubscriptionPushable(s.db.WithContext(ctx), subscription, notification) {
		return nil
	}
	token, locale, ok := s.webPushDeliveryRetryTokenAndLocale(ctx, subscription)
	if !ok {
		return nil
	}
	payload, err := webPushNotificationJSON(s.cfg, webPushDeliveryTarget{Subscription: subscription, AccessToken: token, Locale: locale}, notification)
	if err != nil {
		return taskTargetError("web push payload generation", "local", serverLocalTaskTargetHost(s), err)
	}
	return s.deliverWebPushTargetForWorker(ctx, subscription, payload)
}

func webPushNotificationOutsideTTL(notification models.Notification, now time.Time) bool {
	if notification.UpdatedAt.IsZero() {
		return false
	}
	return notification.UpdatedAt.Before(now.UTC().Add(-time.Duration(webPushTTL) * time.Second))
}

func (s *Server) deliverWebPushTargetForWorker(ctx context.Context, subscription models.WebPushSubscription, payload []byte) error {
	valid, err := s.prepareWebPushSubscription(ctx, subscription)
	if err != nil {
		return taskTargetError("delete invalid web push subscription", "local", serverLocalTaskTargetHost(s), err)
	}
	if !valid {
		return nil
	}
	deliver := s.webPushDeliverer
	if deliver == nil {
		deliver = defaultWebPushDeliverer
	}
	response, err := deliver(ctx, s.webPushDeliveryConfig(), subscription, payload)
	if err != nil {
		return taskTargetError("web push delivery", "remote", remoteTaskTargetHost(subscription.Endpoint), err)
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if response == nil || (response.StatusCode >= 200 && response.StatusCode < 300) {
		return nil
	}
	if webPushSubscriptionExpired(response.StatusCode) {
		if err := s.deleteWebPushSubscription(ctx, subscription.ID); err != nil {
			return taskTargetError("delete expired web push subscription", "local", serverLocalTaskTargetHost(s), err)
		}
		return nil
	}
	return fmt.Errorf("web push delivery target=remote host=%q status=%d", remoteTaskTargetHost(subscription.Endpoint), response.StatusCode)
}

func (s *Server) prepareWebPushSubscription(ctx context.Context, subscription models.WebPushSubscription) (bool, error) {
	if webPushSubscriptionValid(subscription) {
		return true, nil
	}
	if s != nil && s.db != nil && subscription.ID != 0 {
		if err := s.db.WithContext(ctx).Delete(&models.WebPushSubscription{}, subscription.ID).Error; err != nil {
			return false, err
		}
	}
	return false, nil
}

func webPushSubscriptionValid(subscription models.WebPushSubscription) bool {
	endpoint, err := url.Parse(strings.TrimSpace(subscription.Endpoint))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil {
		return false
	}
	publicKey, err := decodeWebPushKey(subscription.KeyP256dh)
	if err != nil {
		return false
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if x == nil || y == nil {
		return false
	}
	auth, err := decodeWebPushKey(subscription.KeyAuth)
	return err == nil && len(auth) == 16
}

func (s *Server) deliverWebPushTargetOnce(ctx context.Context, subscription models.WebPushSubscription, payload []byte) bool {
	valid, err := s.prepareWebPushSubscription(ctx, subscription)
	if err != nil {
		return true
	}
	if !valid {
		return false
	}
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
		return s.deleteWebPushSubscription(ctx, subscription.ID) != nil
	}
	return true
}

func (s *Server) deleteWebPushSubscription(ctx context.Context, subscriptionID int64) error {
	if s == nil || s.db == nil || subscriptionID == 0 {
		return nil
	}
	return s.db.WithContext(nonNilContext(ctx)).Delete(&models.WebPushSubscription{}, subscriptionID).Error
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
	case "Quote":
		return "quotes"
	case "Report":
		return "reports"
	case "Account":
		return "accounts"
	case "AccountRelationshipSeveranceEvent":
		return "account_relationship_severance_events"
	case "AccountWarning":
		return "account_warnings"
	case "GeneratedAnnualReport":
		return "generated_annual_reports"
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
	case "quote":
		return "quotes"
	case "admin.report":
		return "reports"
	case "admin.sign_up":
		return "accounts"
	case "severed_relationships":
		return "account_relationship_severance_events"
	case "moderation_warning":
		return "account_warnings"
	case "annual_report":
		return "generated_annual_reports"
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
	case "quote":
		return name + " quoted your post"
	case "quoted_update":
		return name + " edited a post you have quoted"
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
