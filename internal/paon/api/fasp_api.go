package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type faspRegistrationInput struct {
	Name      string `json:"name"`
	BaseURL   string `json:"baseUrl"`
	ServerID  string `json:"serverId"`
	PublicKey string `json:"publicKey"`
}

type faspProviderInfo struct {
	PrivacyPolicy    json.RawMessage  `json:"privacyPolicy"`
	Capabilities     []faspCapability `json:"capabilities"`
	SignInURL        string           `json:"signInUrl"`
	ContactEmail     string           `json:"contactEmail"`
	FediverseAccount string           `json:"fediverseAccount"`
}

type faspEventSubscriptionInput struct {
	Category         string `json:"category"`
	SubscriptionType string `json:"subscriptionType"`
	MaxBatchSize     int    `json:"maxBatchSize"`
	Threshold        struct {
		Timeframe *int64 `json:"timeframe"`
		Shares    *int64 `json:"shares"`
		Likes     *int64 `json:"likes"`
		Replies   *int64 `json:"replies"`
	} `json:"threshold"`
}

type faspBackfillInput struct {
	Category string `json:"category"`
	MaxCount int    `json:"maxCount"`
}

func (s *Server) registerFASPRoutes() {
	if s == nil || s.echo == nil {
		return
	}
	e := s.echo
	e.POST("/api/fasp/registration", s.createFASPRegistration)
	e.POST("/api/fasp/data_sharing/v0/event_subscriptions", s.createFASPEventSubscription)
	e.DELETE("/api/fasp/data_sharing/v0/event_subscriptions/:id", s.destroyFASPEventSubscription)
	e.POST("/api/fasp/data_sharing/v0/backfill_requests", s.createFASPBackfillRequest)
	e.POST("/api/fasp/data_sharing/v0/backfill_requests/:backfill_request_id/continuation", s.continueFASPBackfillRequest)
	e.POST("/api/fasp/debug/v0/callback/responses", s.createFASPDebugCallback)

	e.GET("/admin/fasp/providers", s.adminFASPProvidersPage)
	e.GET("/admin/fasp/providers/:id", s.adminFASPProviderPage)
	e.GET("/admin/fasp/providers/:id/edit", s.editAdminFASPProviderPage)
	e.POST("/admin/fasp/providers/:id", s.adminFASPProviderMemberAction)
	e.PATCH("/admin/fasp/providers/:id", s.updateAdminFASPProvider)
	e.PUT("/admin/fasp/providers/:id", s.updateAdminFASPProvider)
	e.DELETE("/admin/fasp/providers/:id", s.destroyAdminFASPProvider)
	e.GET("/admin/fasp/providers/:provider_id/registration/new", s.newAdminFASPRegistrationPage)
	e.POST("/admin/fasp/providers/:provider_id/registration", s.confirmAdminFASPRegistration)
	e.POST("/admin/fasp/providers/:provider_id/debug_calls", s.createAdminFASPDebugCall)
	e.GET("/admin/fasp/debug/callbacks", s.adminFASPDebugCallbacksPage)
	e.POST("/admin/fasp/debug/callbacks/:id", s.adminFASPDebugCallbackMemberAction)
	e.DELETE("/admin/fasp/debug/callbacks/:id", s.destroyAdminFASPDebugCallback)
}

func (s *Server) requireFASPFeature(c *echo.Context) error {
	if !s.faspEnabled() {
		return s.notFound(c)
	}
	return nil
}

func faspReadRequestBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return []byte{}, nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, faspRequestBodyLimit+1))
	if err != nil || int64(len(body)) > faspRequestBodyLimit {
		return nil, fmt.Errorf("invalid FASP request body")
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func (s *Server) authenticateFASPRequest(c *echo.Context) (models.FaspProvider, []byte, error) {
	if err := s.requireFASPFeature(c); err != nil {
		return models.FaspProvider{}, nil, err
	}
	if s.db == nil {
		return models.FaspProvider{}, nil, echo.NewHTTPError(http.StatusServiceUnavailable, "FASP database is unavailable")
	}
	body, err := faspReadRequestBody(c.Request())
	if err != nil {
		return models.FaspProvider{}, nil, echo.NewHTTPError(http.StatusBadRequest, "Invalid FASP request")
	}
	input, err := faspParseSignatureInput(c.Request().Header.Get("Signature-Input"))
	if err != nil || input.KeyID == "" {
		return models.FaspProvider{}, nil, echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}
	providerID, err := strconv.ParseInt(input.KeyID, 10, 64)
	if err != nil || providerID <= 0 {
		return models.FaspProvider{}, nil, echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}
	var provider models.FaspProvider
	if err := s.db.Where("id = ? AND confirmed = ?", providerID, true).First(&provider).Error; err != nil {
		return models.FaspProvider{}, nil, echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}
	publicKey, err := faspParsePublicKey(provider.ProviderPublicKeyPEM)
	if err != nil {
		return models.FaspProvider{}, nil, echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}
	if _, err := faspVerifyHTTPRequest(c.Request(), body, publicKey, time.Now().UTC()); err != nil {
		return models.FaspProvider{}, nil, echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}
	return provider, body, nil
}

func faspWriteSignedResponse(c *echo.Context, provider models.FaspProvider, status int, body []byte) error {
	privateKey, err := faspParsePrivateKey(provider.ServerPrivateKeyPEM)
	if err != nil {
		return err
	}
	header := c.Response().Header()
	header.Set("Content-Type", "application/json")
	if err := faspSignHTTPResponse(header, status, body, privateKey, time.Now().UTC()); err != nil {
		return err
	}
	return c.Blob(status, "application/json", body)
}

func faspWriteSignedJSON(c *echo.Context, provider models.FaspProvider, status int, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return faspWriteSignedResponse(c, provider, status, body)
}

func faspWriteSignedEmpty(c *echo.Context, provider models.FaspProvider, status int) error {
	return faspWriteSignedResponse(c, provider, status, []byte{})
}

func faspDecodeJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func faspDecodeRegistrationInput(req *http.Request, body []byte, target *faspRegistrationInput) error {
	if req == nil || target == nil {
		return fmt.Errorf("missing FASP registration target")
	}
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err == nil && mediaType == "application/x-www-form-urlencoded" {
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return err
		}
		*target = faspRegistrationInput{
			Name:      values.Get("name"),
			BaseURL:   values.Get("baseUrl"),
			ServerID:  values.Get("serverId"),
			PublicKey: values.Get("publicKey"),
		}
		return nil
	}
	return faspDecodeJSON(body, target)
}

func (s *Server) createFASPRegistration(c *echo.Context) error {
	if err := s.requireFASPFeature(c); err != nil {
		return err
	}
	if s.db == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "FASP database is unavailable")
	}
	body, err := faspReadRequestBody(c.Request())
	if err != nil {
		return c.NoContent(http.StatusUnprocessableEntity)
	}
	var input faspRegistrationInput
	if err := faspDecodeRegistrationInput(c.Request(), body, &input); err != nil {
		return c.NoContent(http.StatusUnprocessableEntity)
	}
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.ServerID = strings.TrimSpace(input.ServerID)
	if input.Name == "" || input.BaseURL == "" || input.ServerID == "" || len(input.Name) > 255 || len(input.BaseURL) > 2048 || len(input.ServerID) > 255 {
		return c.NoContent(http.StatusUnprocessableEntity)
	}
	if _, err := faspSignatureString(input.ServerID); err != nil {
		return c.NoContent(http.StatusUnprocessableEntity)
	}
	publicKeyPEM, err := faspPublicKeyPEMFromBase64(input.PublicKey)
	if err != nil {
		return c.NoContent(http.StatusUnprocessableEntity)
	}
	probe := models.FaspProvider{BaseURL: input.BaseURL}
	if _, err := faspProviderURL(probe, "/provider_info"); err != nil {
		return c.NoContent(http.StatusUnprocessableEntity)
	}
	privateKeyPEM, publicKeyBase64, err := faspGenerateServerKey()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	provider := models.FaspProvider{
		Confirmed:            false,
		Name:                 input.Name,
		BaseURL:              strings.TrimRight(input.BaseURL, "/"),
		RemoteIdentifier:     input.ServerID,
		ProviderPublicKeyPEM: publicKeyPEM,
		ServerPrivateKeyPEM:  privateKeyPEM,
		Capabilities:         models.JSONValue("[]"),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := s.db.Create(&provider).Error; err != nil {
		return c.NoContent(http.StatusUnprocessableEntity)
	}
	return faspWriteSignedJSON(c, provider, http.StatusOK, map[string]any{
		"faspId":                    strconv.FormatInt(provider.ID, 10),
		"publicKey":                 publicKeyBase64,
		"registrationCompletionUri": strings.TrimRight(s.cfg.BaseURL(), "/") + "/admin/fasp/providers/" + strconv.FormatInt(provider.ID, 10) + "/registration/new",
	})
}

func faspValidCategory(value string) bool {
	return value == "account" || value == "content"
}

func faspValidSubscriptionType(value string) bool {
	return value == "lifecycle" || value == "trends"
}

func faspThresholdValue(value *int64, fallback int64) (sql.NullInt64, bool) {
	if value == nil {
		return sql.NullInt64{Int64: fallback, Valid: true}, true
	}
	if *value < 0 {
		return sql.NullInt64{}, false
	}
	return sql.NullInt64{Int64: *value, Valid: true}, true
}

func (s *Server) createFASPEventSubscription(c *echo.Context) error {
	provider, body, err := s.authenticateFASPRequest(c)
	if err != nil {
		return err
	}
	var input faspEventSubscriptionInput
	if faspDecodeJSON(body, &input) != nil || !faspValidCategory(input.Category) || !faspValidSubscriptionType(input.SubscriptionType) || input.MaxBatchSize <= 0 {
		return faspWriteSignedEmpty(c, provider, http.StatusUnprocessableEntity)
	}
	timeframe, okTimeframe := faspThresholdValue(input.Threshold.Timeframe, 15)
	shares, okShares := faspThresholdValue(input.Threshold.Shares, 3)
	likes, okLikes := faspThresholdValue(input.Threshold.Likes, 3)
	replies, okReplies := faspThresholdValue(input.Threshold.Replies, 3)
	if !okTimeframe || !okShares || !okLikes || !okReplies {
		return faspWriteSignedEmpty(c, provider, http.StatusUnprocessableEntity)
	}
	if input.SubscriptionType != "trends" {
		timeframe, shares, likes, replies = sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
	}
	now := time.Now().UTC()
	subscription := models.FaspSubscription{
		Category:           input.Category,
		SubscriptionType:   input.SubscriptionType,
		MaxBatchSize:       input.MaxBatchSize,
		ThresholdTimeframe: timeframe,
		ThresholdShares:    shares,
		ThresholdLikes:     likes,
		ThresholdReplies:   replies,
		FaspProviderID:     provider.ID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.db.Create(&subscription).Error; err != nil {
		return faspWriteSignedEmpty(c, provider, http.StatusUnprocessableEntity)
	}
	return faspWriteSignedJSON(c, provider, http.StatusCreated, map[string]any{"subscription": map[string]any{"id": subscription.ID}})
}

func (s *Server) destroyFASPEventSubscription(c *echo.Context) error {
	provider, _, err := s.authenticateFASPRequest(c)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		return faspWriteSignedEmpty(c, provider, http.StatusNotFound)
	}
	result := s.db.Where("id = ? AND fasp_provider_id = ?", id, provider.ID).Delete(&models.FaspSubscription{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return faspWriteSignedEmpty(c, provider, http.StatusNotFound)
	}
	return faspWriteSignedEmpty(c, provider, http.StatusNoContent)
}

func (s *Server) createFASPBackfillRequest(c *echo.Context) error {
	provider, body, err := s.authenticateFASPRequest(c)
	if err != nil {
		return err
	}
	var input faspBackfillInput
	if faspDecodeJSON(body, &input) != nil || !faspValidCategory(input.Category) {
		return faspWriteSignedEmpty(c, provider, http.StatusUnprocessableEntity)
	}
	if input.MaxCount == 0 {
		input.MaxCount = 100
	}
	if input.MaxCount < 1 || input.MaxCount > 10_000 {
		return faspWriteSignedEmpty(c, provider, http.StatusUnprocessableEntity)
	}
	now := time.Now().UTC()
	request := models.FaspBackfillRequest{Category: input.Category, MaxCount: input.MaxCount, FaspProviderID: provider.ID, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&request).Error; err != nil {
		return faspWriteSignedEmpty(c, provider, http.StatusUnprocessableEntity)
	}
	if err := s.enqueueFASPBackfill(c.Request().Context(), request.ID); err != nil {
		_ = s.db.Delete(&request).Error
		return err
	}
	return faspWriteSignedJSON(c, provider, http.StatusCreated, map[string]any{"backfillRequest": map[string]any{"id": request.ID}})
}

func (s *Server) continueFASPBackfillRequest(c *echo.Context) error {
	provider, _, err := s.authenticateFASPRequest(c)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("backfill_request_id")), 10, 64)
	if err != nil || id <= 0 {
		return faspWriteSignedEmpty(c, provider, http.StatusNotFound)
	}
	var count int64
	if err := s.db.Model(&models.FaspBackfillRequest{}).Where("id = ? AND fasp_provider_id = ?", id, provider.ID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return faspWriteSignedEmpty(c, provider, http.StatusNotFound)
	}
	if err := s.enqueueFASPBackfill(c.Request().Context(), id); err != nil {
		return err
	}
	return faspWriteSignedEmpty(c, provider, http.StatusNoContent)
}

func (s *Server) createFASPDebugCallback(c *echo.Context) error {
	provider, body, err := s.authenticateFASPRequest(c)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	callback := models.FaspDebugCallback{FaspProviderID: provider.ID, IP: c.RealIP(), RequestBody: string(body), CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&callback).Error; err != nil {
		return err
	}
	return faspWriteSignedEmpty(c, provider, http.StatusCreated)
}

func (s *Server) findAdminFASPProvider(rawID string) (models.FaspProvider, error) {
	if !s.faspEnabled() || s.db == nil {
		return models.FaspProvider{}, gorm.ErrRecordNotFound
	}
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 {
		return models.FaspProvider{}, gorm.ErrRecordNotFound
	}
	var provider models.FaspProvider
	if err := s.db.Where("id = ?", id).First(&provider).Error; err != nil {
		return models.FaspProvider{}, err
	}
	return provider, nil
}

func faspCapabilities(provider models.FaspProvider) []faspCapability {
	var capabilities []faspCapability
	if len(provider.Capabilities) == 0 || json.Unmarshal(provider.Capabilities, &capabilities) != nil {
		return []faspCapability{}
	}
	return capabilities
}

func faspCapabilityEnabled(provider models.FaspProvider, capabilityID string) bool {
	if !provider.Confirmed {
		return false
	}
	for _, capability := range faspCapabilities(provider) {
		if capability.ID == capabilityID && capability.Enabled {
			return true
		}
	}
	return false
}

func (s *Server) confirmedFASPProvidersWithCapability(capabilityID string) ([]models.FaspProvider, error) {
	if !s.faspEnabled() || s.db == nil {
		return []models.FaspProvider{}, nil
	}
	var providers []models.FaspProvider
	if err := s.db.Where("confirmed = ?", true).Order("id ASC").Find(&providers).Error; err != nil {
		return nil, err
	}
	out := make([]models.FaspProvider, 0, len(providers))
	for _, provider := range providers {
		if faspCapabilityEnabled(provider, capabilityID) {
			out = append(out, provider)
		}
	}
	return out, nil
}

func (s *Server) requireAdminFASPUser(c *echo.Context) (*models.User, bool, error) {
	if !s.faspEnabled() {
		return nil, true, s.notFound(c)
	}
	return s.requireAdminFederationWebUser(c)
}

func (s *Server) adminFASPProvidersPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	var providers []models.FaspProvider
	if s.db != nil {
		if err := s.db.Order("confirmed ASC, created_at DESC").Find(&providers).Error; err != nil {
			return err
		}
	}
	return c.HTML(http.StatusOK, adminFASPProvidersHTML(providers, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) adminFASPProviderPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	provider, err := s.findAdminFASPProvider(c.Param("id"))
	if err != nil {
		return s.notFound(c)
	}
	return c.HTML(http.StatusOK, adminFASPProviderHTML(provider, false, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) editAdminFASPProviderPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	provider, err := s.findAdminFASPProvider(c.Param("id"))
	if err != nil {
		return s.notFound(c)
	}
	return c.HTML(http.StatusOK, adminFASPProviderHTML(provider, true, c.QueryParam("error"), s.webLocale(c, user)))
}

func (s *Server) newAdminFASPRegistrationPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	provider, err := s.findAdminFASPProvider(c.Param("provider_id"))
	if err != nil {
		return s.notFound(c)
	}
	fingerprint, err := faspProviderPublicKeyFingerprint(provider)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminFASPRegistrationHTML(provider, fingerprint, c.QueryParam("error"), s.webLocale(c, user)))
}

func validateFASPProviderInfo(info faspProviderInfo) error {
	seen := make(map[string]struct{}, len(info.Capabilities))
	for _, capability := range info.Capabilities {
		capability.ID = strings.TrimSpace(capability.ID)
		capability.Version = strings.TrimSpace(capability.Version)
		if capability.ID == "" || capability.Version == "" || len(capability.ID) > 255 || len(capability.Version) > 64 {
			return fmt.Errorf("invalid provider capability")
		}
		if _, ok := seen[capability.ID]; ok {
			return fmt.Errorf("duplicate provider capability")
		}
		seen[capability.ID] = struct{}{}
	}
	for _, raw := range []string{info.SignInURL} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
			return fmt.Errorf("invalid provider URL")
		}
	}
	return nil
}

func (s *Server) confirmAdminFASPRegistration(c *echo.Context) error {
	user, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	provider, err := s.findAdminFASPProvider(c.Param("provider_id"))
	if err != nil {
		return s.notFound(c)
	}
	fingerprint, fingerprintErr := faspProviderPublicKeyFingerprint(provider)
	if fingerprintErr != nil {
		return fingerprintErr
	}
	requestProvider := provider
	requestProvider.Confirmed = true
	body, err := s.faspRequest(c.Request().Context(), requestProvider, http.MethodGet, "/provider_info", nil)
	if err != nil {
		return c.HTML(http.StatusBadGateway, adminFASPRegistrationHTML(provider, fingerprint, adminT(s.webLocale(c, user), "admin.fasp.errors.confirmation_failed", "Provider confirmation failed."), s.webLocale(c, user)))
	}
	var info faspProviderInfo
	if err := faspDecodeJSON(body, &info); err != nil || validateFASPProviderInfo(info) != nil {
		return c.HTML(http.StatusBadGateway, adminFASPRegistrationHTML(provider, fingerprint, adminT(s.webLocale(c, user), "admin.fasp.errors.invalid_provider_info", "Provider returned invalid information."), s.webLocale(c, user)))
	}
	capabilities, err := json.Marshal(info.Capabilities)
	if err != nil {
		return err
	}
	privacyPolicy := models.JSONValue(nil)
	if len(info.PrivacyPolicy) > 0 && string(info.PrivacyPolicy) != "null" {
		privacyPolicy = models.JSONValue(append([]byte(nil), info.PrivacyPolicy...))
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"confirmed":         true,
		"capabilities":      models.JSONValue(capabilities),
		"privacy_policy":    privacyPolicy,
		"sign_in_url":       nullableFASPString(info.SignInURL),
		"contact_email":     nullableFASPString(info.ContactEmail),
		"fediverse_account": nullableFASPString(info.FediverseAccount),
		"updated_at":        now,
	}
	if err := s.db.Model(&models.FaspProvider{}).Where("id = ? AND confirmed = ?", provider.ID, false).Updates(updates).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/fasp/providers/"+strconv.FormatInt(provider.ID, 10)+"/edit")
}

func nullableFASPString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

func (s *Server) updateAdminFASPProvider(c *echo.Context) error {
	_, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	provider, err := s.findAdminFASPProvider(c.Param("id"))
	if err != nil {
		return s.notFound(c)
	}
	if !provider.Confirmed {
		return c.Redirect(http.StatusFound, "/admin/fasp/providers/"+strconv.FormatInt(provider.ID, 10)+"?error=Provider+is+not+confirmed")
	}
	if err := c.Request().ParseForm(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Malformed request")
	}
	capabilities := faspCapabilities(provider)
	for i := range capabilities {
		wantEnabled := truthy(c.FormValue("capability_" + capabilities[i].ID))
		if capabilities[i].Enabled == wantEnabled {
			continue
		}
		major := strings.SplitN(capabilities[i].Version, ".", 2)[0]
		if major == "" {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "Invalid capability version")
		}
		endpoint := "/capabilities/" + url.PathEscape(capabilities[i].ID) + "/" + url.PathEscape(major) + "/activation"
		method := http.MethodDelete
		if wantEnabled {
			method = http.MethodPost
		}
		if _, err := s.faspRequest(c.Request().Context(), provider, method, endpoint, nil); err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, "provider capability update failed")
		}
		capabilities[i].Enabled = wantEnabled
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		return err
	}
	if err := s.db.Model(&models.FaspProvider{}).Where("id = ? AND confirmed = ?", provider.ID, true).Updates(map[string]any{"capabilities": models.JSONValue(encoded), "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/fasp/providers")
}

func (s *Server) destroyAdminFASPProvider(c *echo.Context) error {
	_, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	provider, err := s.findAdminFASPProvider(c.Param("id"))
	if err != nil {
		return s.notFound(c)
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, target := range []any{&models.FaspSubscription{}, &models.FaspBackfillRequest{}, &models.FaspDebugCallback{}} {
			if err := tx.Where("fasp_provider_id = ?", provider.ID).Delete(target).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&models.FaspProvider{}, provider.ID).Error
	}); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/fasp/providers")
}

func (s *Server) adminFASPProviderMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminFASPProvider(c)
	}
	if methodOverrideIs(c, "put", "patch") {
		return s.updateAdminFASPProvider(c)
	}
	return c.Redirect(http.StatusFound, "/admin/fasp/providers")
}

func (s *Server) createAdminFASPDebugCall(c *echo.Context) error {
	_, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	provider, err := s.findAdminFASPProvider(c.Param("provider_id"))
	if err != nil || !faspCapabilityEnabled(provider, "callback") {
		return s.notFound(c)
	}
	if _, err := s.faspRequest(c.Request().Context(), provider, http.MethodPost, "/debug/v0/callback/logs", map[string]string{"hello": "world"}); err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "provider debug call failed")
	}
	return c.Redirect(http.StatusFound, "/admin/fasp/providers")
}

func (s *Server) adminFASPDebugCallbacksPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	var callbacks []models.FaspDebugCallback
	if s.db != nil {
		if err := s.db.Preload("FaspProvider").Order("created_at DESC").Find(&callbacks).Error; err != nil {
			return err
		}
	}
	return c.HTML(http.StatusOK, adminFASPDebugCallbacksHTML(callbacks, s.webLocale(c, user)))
}

func (s *Server) destroyAdminFASPDebugCallback(c *echo.Context) error {
	_, handled, err := s.requireAdminFASPUser(c)
	if handled || err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		return s.notFound(c)
	}
	result := s.db.Delete(&models.FaspDebugCallback{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return s.notFound(c)
	}
	return c.Redirect(http.StatusFound, "/admin/fasp/debug/callbacks")
}

func (s *Server) adminFASPDebugCallbackMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminFASPDebugCallback(c)
	}
	return c.Redirect(http.StatusFound, "/admin/fasp/debug/callbacks")
}

func adminFASPProvidersHTML(providers []models.FaspProvider, notice string, errorText string, locale string) string {
	title := adminT(locale, "admin.fasp.title", "Fediverse Auxiliary Service Providers")
	var body strings.Builder
	body.WriteString(`<p class="hint">` + html.EscapeString(adminT(locale, "admin.fasp.description", "Manage experimental FASP integrations registered with this server.")) + `</p>`)
	body.WriteString(`<p><a class="button" href="/admin/fasp/debug/callbacks">` + html.EscapeString(adminT(locale, "admin.fasp.debug_callbacks", "Debug callbacks")) + `</a></p>`)
	body.WriteString(`<div class="table-wrapper"><table class="table"><thead><tr><th>` + html.EscapeString(adminT(locale, "admin.fasp.provider", "Provider")) + `</th><th>` + html.EscapeString(adminT(locale, "admin.fasp.status", "Status")) + `</th><th></th></tr></thead><tbody>`)
	for _, provider := range providers {
		id := strconv.FormatInt(provider.ID, 10)
		status := adminT(locale, "admin.fasp.pending", "Pending confirmation")
		if provider.Confirmed {
			status = adminT(locale, "admin.fasp.confirmed", "Confirmed")
		}
		body.WriteString(`<tr><td><a href="/admin/fasp/providers/` + id + `">` + html.EscapeString(provider.Name) + `</a><br><samp>` + html.EscapeString(provider.BaseURL) + `</samp></td><td>` + html.EscapeString(status) + `</td><td>`)
		if provider.Confirmed {
			body.WriteString(`<a class="table-action-link" href="/admin/fasp/providers/` + id + `/edit">` + html.EscapeString(adminT(locale, "admin.fasp.edit", "Edit")) + `</a> `)
		} else {
			body.WriteString(`<a class="table-action-link" href="/admin/fasp/providers/` + id + `/registration/new">` + html.EscapeString(adminT(locale, "admin.fasp.confirm", "Confirm")) + `</a> `)
		}
		if provider.SignInURL.Valid && strings.TrimSpace(provider.SignInURL.String) != "" {
			body.WriteString(`<a class="table-action-link" href="` + html.EscapeString(provider.SignInURL.String) + `" target="_blank" rel="noopener noreferrer">` + html.EscapeString(adminT(locale, "admin.fasp.sign_in", "Sign in")) + `</a> `)
		}
		if faspCapabilityEnabled(provider, "callback") {
			body.WriteString(`<form method="post" action="/admin/fasp/providers/` + id + `/debug_calls" class="inline-form"><button class="table-action-link" type="submit">` + html.EscapeString(adminT(locale, "admin.fasp.debug_call", "Send debug call")) + `</button></form> `)
		}
		body.WriteString(`<form method="post" action="/admin/fasp/providers/` + id + `" class="inline-form"><input type="hidden" name="_method" value="delete"><button class="table-action-link" type="submit">` + html.EscapeString(adminT(locale, "admin.fasp.delete", "Delete")) + `</button></form></td></tr>`)
	}
	body.WriteString(`</tbody></table></div>`)
	return authPageHTML(title, notice, errorText, body.String(), locale)
}

func adminFASPProviderHTML(provider models.FaspProvider, edit bool, errorText string, locale string) string {
	title := adminT(locale, "admin.fasp.provider_details", "FASP provider details")
	id := strconv.FormatInt(provider.ID, 10)
	var body strings.Builder
	body.WriteString(`<dl><dt>` + html.EscapeString(adminT(locale, "admin.fasp.name", "Name")) + `</dt><dd>` + html.EscapeString(provider.Name) + `</dd><dt>` + html.EscapeString(adminT(locale, "admin.fasp.base_url", "Base URL")) + `</dt><dd><samp>` + html.EscapeString(provider.BaseURL) + `</samp></dd><dt>` + html.EscapeString(adminT(locale, "admin.fasp.remote_identifier", "Remote identifier")) + `</dt><dd><samp>` + html.EscapeString(provider.RemoteIdentifier) + `</samp></dd></dl>`)
	if edit && provider.Confirmed {
		body.WriteString(`<form method="post" action="/admin/fasp/providers/` + id + `"><input type="hidden" name="_method" value="patch"><fieldset><legend>` + html.EscapeString(adminT(locale, "admin.fasp.capabilities", "Capabilities")) + `</legend>`)
		for _, capability := range faspCapabilities(provider) {
			checked := ""
			if capability.Enabled {
				checked = " checked"
			}
			body.WriteString(`<label><input type="checkbox" name="capability_` + html.EscapeString(capability.ID) + `" value="1"` + checked + `> ` + html.EscapeString(capability.ID+" "+capability.Version) + `</label><br>`)
		}
		body.WriteString(`</fieldset><button class="button" type="submit">` + html.EscapeString(adminT(locale, "admin.fasp.save", "Save changes")) + `</button></form>`)
		if faspCapabilityEnabled(provider, "callback") {
			body.WriteString(`<hr><form method="post" action="/admin/fasp/providers/` + id + `/debug_calls"><button class="button" type="submit">` + html.EscapeString(adminT(locale, "admin.fasp.debug_call", "Send debug call")) + `</button></form>`)
		}
	}
	body.WriteString(`<p><a href="/admin/fasp/providers">` + html.EscapeString(adminT(locale, "admin.fasp.back", "Back to providers")) + `</a></p>`)
	return authPageHTML(title, "", errorText, body.String(), locale)
}

func adminFASPRegistrationHTML(provider models.FaspProvider, fingerprint string, errorText string, locale string) string {
	title := adminT(locale, "admin.fasp.confirm_registration", "Confirm FASP registration")
	id := strconv.FormatInt(provider.ID, 10)
	var body strings.Builder
	body.WriteString(`<p>` + html.EscapeString(adminT(locale, "admin.fasp.verify_fingerprint", "Verify this provider key fingerprint through a trusted channel before confirming.")) + `</p><p><strong>` + html.EscapeString(provider.Name) + `</strong><br><samp>` + html.EscapeString(fingerprint) + `</samp></p>`)
	if !provider.Confirmed {
		body.WriteString(`<form method="post" action="/admin/fasp/providers/` + id + `" class="inline-form"><input type="hidden" name="_method" value="delete"><button class="button negative" type="submit">` + html.EscapeString(adminT(locale, "admin.fasp.reject", "Reject")) + `</button></form> `)
		body.WriteString(`<form method="post" action="/admin/fasp/providers/` + id + `/registration" class="inline-form"><button class="button" type="submit">` + html.EscapeString(adminT(locale, "admin.fasp.confirm", "Confirm")) + `</button></form>`)
	}
	body.WriteString(`<p><a href="/admin/fasp/providers">` + html.EscapeString(adminT(locale, "admin.fasp.back", "Back to providers")) + `</a></p>`)
	return authPageHTML(title, "", errorText, body.String(), locale)
}

func adminFASPDebugCallbacksHTML(callbacks []models.FaspDebugCallback, locale string) string {
	title := adminT(locale, "admin.fasp.debug_callbacks", "Debug callbacks")
	var body strings.Builder
	body.WriteString(`<div class="table-wrapper"><table class="table"><thead><tr><th>` + html.EscapeString(adminT(locale, "admin.fasp.provider", "Provider")) + `</th><th>` + html.EscapeString(adminT(locale, "admin.fasp.received", "Received")) + `</th><th>` + html.EscapeString(adminT(locale, "admin.fasp.request_body", "Request body")) + `</th><th></th></tr></thead><tbody>`)
	for _, callback := range callbacks {
		id := strconv.FormatInt(callback.ID, 10)
		body.WriteString(`<tr><td>` + html.EscapeString(callback.FaspProvider.Name) + `<br><samp>` + html.EscapeString(callback.IP) + `</samp></td><td><time datetime="` + html.EscapeString(callback.CreatedAt.UTC().Format(time.RFC3339)) + `">` + html.EscapeString(callback.CreatedAt.UTC().Format(time.RFC3339)) + `</time></td><td><pre>` + html.EscapeString(callback.RequestBody) + `</pre></td><td><form method="post" action="/admin/fasp/debug/callbacks/` + id + `"><input type="hidden" name="_method" value="delete"><button class="table-action-link" type="submit">` + html.EscapeString(adminT(locale, "admin.fasp.delete", "Delete")) + `</button></form></td></tr>`)
	}
	body.WriteString(`</tbody></table></div><p><a href="/admin/fasp/providers">` + html.EscapeString(adminT(locale, "admin.fasp.back", "Back to providers")) + `</a></p>`)
	return authPageHTML(title, "", "", body.String(), locale)
}
