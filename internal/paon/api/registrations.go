package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var localUsernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

var turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
var turnstileHTTPClient = &http.Client{Timeout: 5 * time.Second}
var hcaptchaVerifyURL = "https://hcaptcha.com/siteverify"
var hcaptchaHTTPClient = &http.Client{Timeout: 5 * time.Second}

const registrationRulesAcceptCookieName = "paon_rules_accept"
const registrationFormTimeCookieName = "paon_registration_form_time"
const maxTurnstileResponseBodySize = 1 << 20
const registrationMalformedRequestFallback = "Malformed request"
const registrationInviteInvalidFallback = "Invite is invalid"
const registrationPasswordConfirmationMismatchFallback = "Password confirmation does not match"
const registrationUsernameOrEmailTakenFallback = "Username or e-mail has already been taken"
const registrationTooFastFallback = "This form was submitted too fast"
const registrationValidationUsernameInvalid = "Validation failed: Username is invalid"
const registrationValidationEmailInvalid = "Validation failed: Email is invalid"
const registrationValidationPasswordInvalid = "Validation failed: Password is invalid"
const registrationValidationAgreementRequired = "Validation failed: Agreement must be accepted"
const registrationValidationUsernameReserved = "Validation failed: Username is reserved"
const registrationValidationUsernameOrEmailTaken = "Validation failed: " + registrationUsernameOrEmailTakenFallback
const registrationValidationReasonRequired = "Validation failed: Reason can't be blank"
const registrationValidationReasonTooLong = "Validation failed: Reason is too long"
const registrationValidationTooFast = "Validation failed: " + registrationTooFastFallback
const registrationValidationHoneypot = "Validation failed: Website must be blank"
const registrationTurnstileMalformedRequest = "Cloudflare Turnstile reports malformed request"

var errMalformedRegistrationPayload = errors.New("malformed registration payload")

type accountCreatePayload struct {
	Username          string `json:"username" form:"username"`
	Email             string `json:"email" form:"email"`
	Password          string `json:"password" form:"password"`
	Agreement         string `json:"agreement" form:"agreement"`
	Locale            string `json:"locale" form:"locale"`
	Reason            string `json:"reason" form:"reason"`
	TimeZone          string `json:"time_zone" form:"time_zone"`
	InviteCode        string `json:"invite_code" form:"invite_code"`
	TurnstileResponse string `json:"cf-turnstile-response" form:"cf-turnstile-response"`
	Website           string `json:"website" form:"website"`
	ConfirmPassword   string `json:"confirm_password" form:"confirm_password"`
}

func (s *Server) createAccount(c *echo.Context) error {
	app, scopes, err := s.requireApplicationWriteToken(c, "write:accounts")
	if err != nil {
		return err
	}
	if s.db == nil {
		return apiError(c, http.StatusServiceUnavailable, "DATABASE_URL is not set")
	}
	payload, err := parseAccountCreatePayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err := validateAccountCreatePayload(payload); err != nil {
		return err
	}
	if err := validateInviteRequestText(payload.Reason, false); err != nil {
		return err
	}
	if err := s.validateAccountUsernameAvailable(payload.Username); err != nil {
		return err
	}
	if err := s.ensureEmailDomainAllowed(c.Request().Context(), payload.Email, c.RealIP(), true, false); err != nil {
		return err
	}
	if err := s.checkCloudflareTurnstile(c, payload.TurnstileResponse); err != nil {
		return err
	}
	if !s.registrationsEnabled() {
		return apiError(c, http.StatusForbidden, "This method is not available")
	}
	ipRestriction, err := s.signUpIPRestriction(c.RealIP(), time.Now().UTC())
	if err != nil {
		return err
	}
	if ipRestriction.Blocked {
		return apiError(c, http.StatusForbidden, "This method is not available")
	}
	emailRequiresApproval, err := s.emailSignUpRequiresApproval(c.Request().Context(), payload.Email, c.RealIP())
	if err != nil {
		return err
	}
	ipRestriction.RequiresApproval = ipRestriction.RequiresApproval || emailRequiresApproval

	token := &models.OAuthAccessToken{}
	var createdUser *models.User
	err = s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		user, err := s.createLocalUserAccount(tx, payload, accountCreationOptions{
			ApplicationID: sql.NullInt64{Int64: app.ID, Valid: true},
			Now:           now,
			SignUpIP:      c.RealIP(),
			IPRestriction: ipRestriction,
		})
		if err != nil {
			return err
		}
		createdUser = user
		token = &models.OAuthAccessToken{
			Token:           randomHex(32),
			CreatedAt:       now,
			Scopes:          models.NullSafeString(firstNonEmpty(app.Scopes, scopes, "read write follow push")),
			ApplicationID:   sql.NullInt64{Int64: app.ID, Valid: true},
			ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true},
		}
		return tx.Create(token).Error
	})
	if err != nil {
		if isUniqueConstraintError(err) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Username or e-mail has already been taken")
		}
		return err
	}
	s.triggerAccountWebhook("account.created", createdUser.AccountID)
	if delivery := confirmationDeliveryForUser(createdUser); delivery.Token != "" {
		if err := s.sendConfirmationDelivery(delivery); err != nil {
			return mailDeliveryError("confirmation", err)
		}
	}
	return c.JSON(http.StatusOK, tokenResponse(token))
}

func (s *Server) registrationForm(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	if blocked, err := s.webSignUpIPBlocked(c); err != nil {
		return err
	} else if blocked {
		return c.Redirect(http.StatusFound, "/")
	}
	invite, err := s.findUsableInvite(c.QueryParam("invite_code"), time.Now().UTC())
	if err != nil {
		invite = nil
	}
	if !s.webRegistrationsAllowedForInvite(invite) {
		return c.Redirect(http.StatusFound, "/")
	}
	inviteCode := c.QueryParam("invite_code")
	if handled, err := s.registrationRulesGateForInvite(c, inviteCode, invite); handled || err != nil {
		return err
	}
	s.setRegistrationFormTimeCookie(c, time.Now().UTC())
	return c.HTML(http.StatusOK, s.registrationPageHTMLForInvite("", invite, inviteCode, locale, c.QueryParam("accept")))
}

func (s *Server) cancelUserRegistration(c *echo.Context) error {
	return c.Redirect(http.StatusFound, "/auth/sign_up")
}

func (s *Server) destroyUserRegistration(c *echo.Context) error {
	return s.notFound(c)
}

func (s *Server) publicInvite(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	if blocked, err := s.webSignUpIPBlocked(c); err != nil {
		return err
	} else if blocked {
		return c.Redirect(http.StatusFound, "/")
	}
	code := publicShortAccountParam(c, "invite_code")
	invite, err := s.findUsableInvite(code, time.Now().UTC())
	if err != nil || invite == nil {
		if inviteWantsJSON(c) {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": registrationInviteInvalidFallback})
		}
		return c.Redirect(http.StatusFound, "/")
	}
	if !s.webRegistrationsAllowedForInvite(invite) {
		if inviteWantsJSON(c) {
			return apiError(c, http.StatusForbidden, "This action is not allowed")
		}
		return c.Redirect(http.StatusFound, "/")
	}
	if inviteWantsJSON(c) {
		return c.JSON(http.StatusOK, map[string]string{"invite_code": invite.Code, "instance_api_url": s.cfg.BaseURL() + "/api/v2/instance"})
	}
	if handled, err := s.registrationRulesGateForInvite(c, invite.Code, invite); handled || err != nil {
		return err
	}
	s.setRegistrationFormTimeCookie(c, time.Now().UTC())
	return c.HTML(http.StatusOK, s.registrationPageHTMLForInvite("", invite, invite.Code, locale, c.QueryParam("accept")))
}

func (s *Server) webSignUpIPBlocked(c *echo.Context) (bool, error) {
	ipRestriction, err := s.signUpIPRestriction((*c).RealIP(), time.Now().UTC())
	if err != nil {
		return false, err
	}
	return ipRestriction.Blocked, nil
}

func (s *Server) createWebRegistration(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	if s.db == nil {
		return c.HTML(http.StatusServiceUnavailable, s.registrationPageHTML(settingsDatabaseUnavailableMessage(locale), c.FormValue("user[invite_code]"), locale, c.FormValue("accept")))
	}
	payload, err := parseWebAccountCreatePayload(c)
	if err != nil {
		return c.HTML(http.StatusBadRequest, s.registrationPageHTML(registrationErrorMessage(locale, "malformed_request", registrationMalformedRequestFallback), c.FormValue("user[invite_code]"), locale, c.FormValue("accept")))
	}
	invite, err := s.findUsableInvite(payload.InviteCode, time.Now().UTC())
	if err != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTML(registrationErrorMessage(locale, "invite_invalid", registrationInviteInvalidFallback), payload.InviteCode, locale, c.FormValue("accept")))
	}
	if !s.webRegistrationsAllowedForInvite(invite) {
		return c.Redirect(http.StatusFound, "/")
	}
	if err := s.checkCloudflareTurnstile(c, payload.TurnstileResponse); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationWebErrorMessage(locale, err), invite, payload.InviteCode, locale, c.FormValue("accept")))
	}
	if err := s.validateRegistrationFormTime(c, time.Now().UTC()); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationWebErrorMessage(locale, err), invite, payload.InviteCode, locale, c.FormValue("accept")))
	}
	if err := validateWebRegistrationHoneypot(payload); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationWebErrorMessage(locale, err), invite, payload.InviteCode, locale, c.FormValue("accept")))
	}
	ipRestriction, err := s.signUpIPRestriction(c.RealIP(), time.Now().UTC())
	if err != nil {
		return err
	}
	if ipRestriction.Blocked {
		return c.Redirect(http.StatusFound, "/")
	}
	emailRequiresApproval, err := s.emailSignUpRequiresApproval(c.Request().Context(), payload.Email, c.RealIP())
	if err != nil {
		return err
	}
	ipRestriction.RequiresApproval = ipRestriction.RequiresApproval || emailRequiresApproval
	if confirm := strings.TrimSpace(c.FormValue("user[password_confirmation]")); confirm != "" && confirm != payload.Password {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationErrorMessage(locale, "password_confirmation_mismatch", registrationPasswordConfirmationMismatchFallback), invite, payload.InviteCode, locale, c.FormValue("accept")))
	}
	if err := validateAccountCreatePayload(payload); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationWebErrorMessage(locale, err), invite, payload.InviteCode, locale, c.FormValue("accept")))
	}
	if err := validateInviteRequestText(payload.Reason, s.webInviteRequestTextRequired(invite)); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationWebErrorMessage(locale, err), invite, payload.InviteCode, locale, c.FormValue("accept")))
	}
	if err := s.validateAccountUsernameAvailable(payload.Username); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationWebErrorMessage(locale, err), invite, payload.InviteCode, locale, c.FormValue("accept")))
	}
	if err := s.ensureEmailDomainAllowed(c.Request().Context(), payload.Email, c.RealIP(), true, invite != nil); err != nil {
		return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationWebErrorMessage(locale, err), invite, payload.InviteCode, locale, c.FormValue("accept")))
	}

	var user *models.User
	err = s.db.Transaction(func(tx *gorm.DB) error {
		created, err := s.createLocalUserAccount(tx, payload, accountCreationOptions{
			Invite:        invite,
			Now:           time.Now().UTC(),
			SignUpIP:      c.RealIP(),
			IPRestriction: ipRestriction,
		})
		user = created
		return err
	})
	if err != nil {
		if isUniqueConstraintError(err) {
			return c.HTML(http.StatusUnprocessableEntity, s.registrationPageHTMLForInvite(registrationErrorMessage(locale, "taken", registrationUsernameOrEmailTakenFallback), invite, payload.InviteCode, locale, c.FormValue("accept")))
		}
		return err
	}
	s.triggerAccountWebhook("account.created", user.AccountID)
	if delivery := confirmationDeliveryForUser(user); delivery.Token != "" {
		if err := s.sendConfirmationDelivery(delivery); err != nil {
			return mailDeliveryError("confirmation", err)
		}
	}
	token, err := s.issueAccessToken(user, "read write follow push")
	if err != nil {
		return err
	}
	if err := s.recordSessionActivation(user.ID, token.ID, c); err != nil {
		return err
	}
	if err := s.setSessionCookie(c, token.Token); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/auth/setup")
}

func (s *Server) createEmailConfirmation(c *echo.Context) error {
	user, accessToken, err := s.requireUserAccessToken(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	if !emailConfirmationTokenOwnsUserApplication(user, accessToken) {
		return apiError(c, http.StatusForbidden, "This method is only available to the application the user originally signed-up with")
	}
	if user.ConfirmedAt.Valid && strings.TrimSpace(user.UnconfirmedEmail.String) == "" {
		return apiError(c, http.StatusForbidden, "This method is only available while the e-mail is awaiting confirmation")
	}

	confirmationToken := randomHex(16)
	updates := map[string]any{"confirmation_sent_at": time.Now().UTC(), "confirmation_token": deviseTokenForStorage(confirmationToken, deviseConfirmationTokenColumn, s.cfg.SecretKeyBase)}
	recipient := confirmationRecipient(*user)
	email, hasEmail, err := emailConfirmationFromRequest(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if hasEmail {
		if !railsEmailAddressValid(email) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Email is invalid")
		}
		updates["unconfirmed_email"] = email
		recipient = email
	}
	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		return err
	}
	deliveryUser := *user
	if hasEmail {
		deliveryUser.UnconfirmedEmail = sql.NullString{String: email, Valid: true}
	}
	delivery := confirmationDeliveryForUserWithToken(&deliveryUser, confirmationToken)
	delivery.Email = recipient
	if err := s.sendConfirmationDelivery(delivery); err != nil {
		return mailDeliveryError("confirmation", err)
	}
	return renderEmpty(c)
}

func emailConfirmationTokenOwnsUserApplication(user *models.User, accessToken *models.OAuthAccessToken) bool {
	if user == nil || accessToken == nil {
		return false
	}
	if user.CreatedByApplicationID.Valid != accessToken.ApplicationID.Valid {
		return false
	}
	if !user.CreatedByApplicationID.Valid {
		return true
	}
	return user.CreatedByApplicationID.Int64 == accessToken.ApplicationID.Int64
}

func emailConfirmationFromRequest(c *echo.Context) (string, bool, error) {
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		raw := map[string]json.RawMessage{}
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return "", false, err
		}
		value, ok := raw["email"]
		if !ok {
			return "", false, nil
		}
		return strings.ToLower(strings.TrimSpace(rawJSONString(value))), true, nil
	}
	if value, ok := formField(c, "email"); ok {
		return strings.ToLower(strings.TrimSpace(value)), true, nil
	}
	return "", false, nil
}

func (s *Server) checkEmailConfirmation(c *echo.Context) error {
	user, _, err := s.currentUser(c)
	if err != nil {
		return apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	return c.JSON(http.StatusOK, user.ConfirmedAt.Valid)
}

func (s *Server) registrationRulesGate(c *echo.Context, inviteCode string, invited bool) (bool, error) {
	var invite *models.Invite
	if invited {
		invite = &models.Invite{Code: inviteCode}
	}
	return s.registrationRulesGateForInvite(c, inviteCode, invite)
}

func (s *Server) registrationRulesGateForInvite(c *echo.Context, inviteCode string, invite *models.Invite) (bool, error) {
	rules, err := s.instanceRuleModels()
	if err != nil {
		return true, err
	}
	if len(rules) == 0 || registrationRulesAccepted(c) {
		return false, nil
	}
	token := randomHex(16)
	setCookie(c, registrationRulesAcceptCookieName, token, 60*30, s.cfg.ForceSSL)
	manualReview := s.registrationMode() == "approved" && invite == nil
	return true, c.HTML(http.StatusOK, registrationRulesHTMLForInvite(rules, inviteCode, token, manualReview, s.cfg.LocalDomain, inviteRegistrationContext(invite), s.webLocale(c, nil)))
}

func registrationRulesAccepted(c *echo.Context) bool {
	accept := strings.TrimSpace(c.QueryParam("accept"))
	if accept == "" {
		return false
	}
	cookie, err := c.Cookie(registrationRulesAcceptCookieName)
	return err == nil && cookie.Value != "" && accept == cookie.Value
}

func parseAccountCreatePayload(c *echo.Context) (accountCreatePayload, error) {
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return accountCreatePayload{}, err
		}
		return accountCreatePayloadFromJSON(raw), nil
	}
	var payload accountCreatePayload
	if err := c.Bind(&payload); err != nil {
		return payload, err
	}
	if values, err := c.FormValues(); err == nil {
		payload.Username = firstNonEmpty(payload.Username, values.Get("username"))
		payload.Email = firstNonEmpty(payload.Email, values.Get("email"))
		payload.Password = firstNonEmpty(payload.Password, values.Get("password"))
		payload.Agreement = firstNonEmpty(payload.Agreement, values.Get("agreement"))
		payload.Locale = firstNonEmpty(payload.Locale, values.Get("locale"))
		payload.Reason = firstNonBlankRaw(payload.Reason, values.Get("reason"))
		payload.TimeZone = firstNonEmpty(payload.TimeZone, values.Get("time_zone"))
		payload.InviteCode = firstNonEmpty(payload.InviteCode, values.Get("invite_code"))
		payload.TurnstileResponse = firstNonEmpty(payload.TurnstileResponse, values.Get("cf-turnstile-response"))
	}
	return payload, nil
}

func parseWebAccountCreatePayload(c *echo.Context) (accountCreatePayload, error) {
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return accountCreatePayload{}, err
		}
		if jsonMapValue(raw, "user") == nil {
			return accountCreatePayload{}, errMalformedRegistrationPayload
		}
		return webAccountCreatePayloadFromJSON(raw), nil
	}
	if !railsNestedFormRootPresent(c, "user") {
		return accountCreatePayload{}, errMalformedRegistrationPayload
	}
	values, err := c.FormValues()
	if err != nil {
		return accountCreatePayload{}, err
	}
	return accountCreatePayload{
		Username:          values.Get("user[account_attributes][username]"),
		Email:             values.Get("user[email]"),
		Password:          values.Get("user[password]"),
		Agreement:         values.Get("user[agreement]"),
		Locale:            values.Get("user[locale]"),
		Reason:            values.Get("user[invite_request_attributes][text]"),
		TimeZone:          values.Get("user[time_zone]"),
		InviteCode:        values.Get("user[invite_code]"),
		TurnstileResponse: values.Get("cf-turnstile-response"),
		Website:           values.Get("user[website]"),
		ConfirmPassword:   values.Get("user[confirm_password]"),
	}, nil
}

func accountCreatePayloadFromJSON(raw map[string]any) accountCreatePayload {
	return accountCreatePayload{
		Username:          stringPayloadValue(raw["username"]),
		Email:             stringPayloadValue(raw["email"]),
		Password:          stringPayloadValue(raw["password"]),
		Agreement:         stringPayloadValue(raw["agreement"]),
		Locale:            stringPayloadValue(raw["locale"]),
		Reason:            firstNonBlankRaw(stringPayloadValue(raw["reason"])),
		TimeZone:          stringPayloadValue(raw["time_zone"]),
		InviteCode:        stringPayloadValue(raw["invite_code"]),
		TurnstileResponse: stringPayloadValue(raw["cf-turnstile-response"]),
	}
}

func webAccountCreatePayloadFromJSON(raw map[string]any) accountCreatePayload {
	user := jsonMapValue(raw, "user")
	accountAttrs := jsonMapValue(user, "account_attributes")
	inviteRequestAttrs := jsonMapValue(user, "invite_request_attributes")
	return accountCreatePayload{
		Username:          firstNonEmpty(stringPayloadValue(raw["username"]), stringPayloadValue(accountAttrs["username"])),
		Email:             firstNonEmpty(stringPayloadValue(raw["email"]), stringPayloadValue(user["email"])),
		Password:          firstNonEmpty(stringPayloadValue(raw["password"]), stringPayloadValue(user["password"])),
		Agreement:         firstNonEmpty(stringPayloadValue(raw["agreement"]), stringPayloadValue(user["agreement"])),
		Locale:            firstNonEmpty(stringPayloadValue(raw["locale"]), stringPayloadValue(user["locale"])),
		Reason:            firstNonBlankRaw(stringPayloadValue(raw["reason"]), stringPayloadValue(inviteRequestAttrs["text"])),
		TimeZone:          firstNonEmpty(stringPayloadValue(raw["time_zone"]), stringPayloadValue(user["time_zone"])),
		InviteCode:        firstNonEmpty(stringPayloadValue(raw["invite_code"]), stringPayloadValue(user["invite_code"])),
		TurnstileResponse: stringPayloadValue(raw["cf-turnstile-response"]),
		Website:           firstNonBlankRaw(stringPayloadValue(user["website"])),
		ConfirmPassword:   firstNonBlankRaw(stringPayloadValue(user["confirm_password"])),
	}
}

func jsonMapValue(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return nil
	}
	if value, ok := raw[key].(map[string]any); ok {
		return value
	}
	return nil
}

func validateAccountCreatePayload(payload accountCreatePayload) error {
	username := railsAccountUsernameValue(payload.Username)
	email := strings.TrimSpace(payload.Email)
	password := payload.Password
	switch {
	case username == "" || len([]rune(username)) > 30 || !localUsernamePattern.MatchString(username):
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationUsernameInvalid}
	case !railsEmailAddressValid(email):
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationEmailInvalid}
	case len(password) < 8 || len(password) > 72:
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationPasswordInvalid}
	case !registrationAgreementAccepted(payload.Agreement):
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationAgreementRequired}
	default:
		return nil
	}
}

func registrationAgreementAccepted(value string) bool {
	switch strings.TrimSpace(value) {
	case "true", "1":
		return true
	default:
		return false
	}
}

func railsEmailAddressValid(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" || len([]rune(email)) > 320 || strings.ContainsAny(email, "%,\"") {
		return false
	}
	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email
}

func validateWebRegistrationHoneypot(payload accountCreatePayload) error {
	if strings.TrimSpace(payload.Website) != "" || strings.TrimSpace(payload.ConfirmPassword) != "" {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationHoneypot}
	}
	return nil
}

func validateInviteRequestText(reason string, required bool) error {
	if strings.TrimSpace(reason) == "" {
		if required {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationReasonRequired}
		}
		return nil
	}
	if len([]rune(reason)) > 420 {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationReasonTooLong}
	}
	return nil
}

var defaultReservedUsernames = []string{
	"admin",
	"support",
	"help",
	"root",
	"webmaster",
	"administrator",
	"mod",
	"moderator",
}

func (s *Server) validateAccountUsernameAvailable(username string) error {
	if s.accountUsernameReserved(username) {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationUsernameReserved}
	}
	if s != nil && s.db != nil {
		normalized := railsAccountUsernameValue(username)
		if strings.TrimSpace(normalized) != "" {
			var count int64
			if err := s.db.Model(&models.Account{}).Where("lower(username) = lower(?) AND domain IS NULL", normalized).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationUsernameOrEmailTaken}
			}
		}
	}
	return nil
}

func (s *Server) accountUsernameReserved(username string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return false
	}
	if pamControlledUsername(s.cfg, username) {
		return true
	}
	for _, reserved := range s.reservedUsernames() {
		if username == strings.ToLower(strings.TrimSpace(reserved)) {
			return true
		}
	}
	return false
}

func (s *Server) reservedUsernames() []string {
	if s == nil {
		return defaultReservedUsernames
	}
	if parsed := parseReservedUsernamesSetting(s.settingValue("reserved_usernames", "")); len(parsed) > 0 {
		return parsed
	}
	return defaultReservedUsernames
}

func parseReservedUsernamesSetting(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	raw = strings.Trim(raw, `"`)
	if !strings.Contains(raw, "\n") && strings.Contains(raw, ",") {
		var out []string
		for _, part := range strings.Split(raw, ",") {
			part = strings.Trim(strings.TrimSpace(part), `"`)
			if part != "" {
				out = append(out, part)
			}
		}
		return out
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" {
			continue
		}
		line = strings.TrimPrefix(line, "-")
		line = strings.Trim(strings.TrimSpace(line), `"`)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

type accountCreationOptions struct {
	ApplicationID sql.NullInt64
	Invite        *models.Invite
	Now           time.Time
	SignUpIP      string
	IPRestriction signUpIPRestriction
}

func (s *Server) createLocalUserAccount(tx *gorm.DB, payload accountCreatePayload, options accountCreationOptions) (*models.User, error) {
	privateKey, publicKey, err := generateAccountKeyPair()
	if err != nil {
		return nil, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	account := models.Account{
		Username:   railsAccountUsernameValue(payload.Username),
		PrivateKey: sql.NullString{String: privateKey, Valid: true},
		PublicKey:  publicKey,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := tx.Create(&account).Error; err != nil {
		return nil, err
	}
	stat := models.AccountStat{AccountID: account.ID, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&stat).Error; err != nil {
		return nil, err
	}
	password, err := bcrypt.GenerateFromPassword([]byte(payload.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	approved := accountApprovedForRegistration(s.registrationMode(), options.Invite, options.IPRestriction.RequiresApproval)
	confirmationToken := randomHex(16)
	user := models.User{
		AccountID:              account.ID,
		Email:                  strings.ToLower(strings.TrimSpace(payload.Email)),
		EncryptedPassword:      string(password),
		Locale:                 railsUserLocaleValue(payload.Locale),
		CreatedAt:              now,
		UpdatedAt:              now,
		Approved:               approved,
		ConfirmationToken:      sql.NullString{String: deviseTokenForStorage(confirmationToken, deviseConfirmationTokenColumn, s.cfg.SecretKeyBase), Valid: true},
		ConfirmationSentAt:     sql.NullTime{Time: now, Valid: true},
		CreatedByApplicationID: options.ApplicationID,
		SignUpIP:               nullString(options.SignUpIP),
		TimeZone:               railsUserTimeZoneValue(payload.TimeZone),
	}
	if options.Invite != nil {
		user.InviteID = sql.NullInt64{Int64: options.Invite.ID, Valid: true}
	}
	if err := tx.Create(&user).Error; err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Reason) != "" {
		request := models.UserInviteRequest{UserID: models.UserInviteRequestUserID(user.ID), Text: sql.NullString{String: payload.Reason, Valid: true}, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&request).Error; err != nil {
			return nil, err
		}
	}
	if options.Invite != nil {
		if err := tx.Model(&models.Invite{}).Where("id = ?", options.Invite.ID).UpdateColumn("uses", gorm.Expr("uses + 1")).Error; err != nil {
			return nil, err
		}
	}
	user.Account = &account
	user.ConfirmationToken = sql.NullString{String: confirmationToken, Valid: true}
	return &user, nil
}

func (s *Server) runApprovedAccountBootstrap(ctx context.Context, accountID int64, now time.Time) error {
	if s == nil || accountID == 0 {
		return nil
	}
	if s.enqueueBootstrapTimelineTask(accountID) {
		return nil
	}
	return s.bootstrapApprovedAccount(ctx, accountID, now)
}

func (s *Server) createInviteAutofollow(tx *gorm.DB, source models.Account, invite *models.Invite, now time.Time) (*asynqLocalNotificationPayload, error) {
	targetID := inviteAutofollowTargetAccountID(invite)
	if targetID == 0 && invite != nil && invite.Autofollow && invite.UserID != 0 {
		var inviter models.User
		if err := tx.Where("id = ?", invite.UserID).First(&inviter).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		targetID = inviter.AccountID
	}
	if targetID == 0 || targetID == source.ID {
		return nil, nil
	}

	var target models.Account
	if err := tx.Where("id = ? AND suspended_at IS NULL", targetID).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	if inviteAutofollowUsesRequest(source, target) {
		req := models.FollowRequest{CreatedAt: now, UpdatedAt: now, AccountID: source.ID, TargetAccountID: target.ID, ShowReblogs: true, URI: models.NullSafeString(activityPubGeneratedPayloadURI(s))}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&req)
		if res.Error != nil || res.RowsAffected == 0 {
			return nil, res.Error
		}
		return &asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: source.ID, ActivityID: req.ID, ActivityType: "FollowRequest", Type: "follow_request"}, nil
	}

	follow := models.Follow{CreatedAt: now, UpdatedAt: now, AccountID: source.ID, TargetAccountID: target.ID, ShowReblogs: true, URI: models.NullSafeString(activityPubGeneratedPayloadURI(s))}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	if err := incrementAccountStatCounter(tx, source.ID, accountStatCounterFollowing, 1); err != nil {
		return nil, err
	}
	if err := incrementAccountStatCounter(tx, target.ID, accountStatCounterFollowers, 1); err != nil {
		return nil, err
	}
	return &asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: source.ID, ActivityID: follow.ID, ActivityType: "Follow", Type: "follow"}, nil
}

func (s *Server) bootstrapApprovedAccount(ctx context.Context, accountID int64, now time.Time) error {
	if s.db == nil || accountID == 0 {
		return nil
	}
	var approvedUser *models.User
	var notificationPayloads []asynqLocalNotificationPayload
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var source models.Account
		if err := tx.Preload("User").Where("id = ?", accountID).First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if source.User.ID != 0 && source.User.InviteID.Valid {
			var invite models.Invite
			if err := tx.Preload("User").Where("id = ?", source.User.InviteID.Int64).First(&invite).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			} else {
				notificationPayload, err := s.createInviteAutofollow(tx, source, &invite, now)
				if err != nil {
					return err
				}
				notificationPayloads = appendRelationshipBatchNotificationPayload(notificationPayloads, notificationPayload)
			}
		}
		staffNotificationPayloads, err := s.createStaffSignUpNotificationPayloads(tx, source)
		if err != nil {
			return err
		}
		notificationPayloads = append(notificationPayloads, staffNotificationPayloads...)
		if source.User.ID != 0 {
			source.User.Account = &source
			approvedUser = &source.User
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads); err != nil {
		return err
	}
	if approvedUser != nil {
		s.activityTrackerIncrementBasic(ctx, "activity:accounts:local", now, 1)
		s.activityTrackerRecordUnique(ctx, "activity:logins", now, approvedUser.ID)
		if err := s.sendWelcomeMail(*approvedUser); err != nil {
			return mailDeliveryError("welcome", err)
		}
	}
	return nil
}

func inviteAutofollowTargetAccountID(invite *models.Invite) int64 {
	if invite == nil || !invite.Autofollow {
		return 0
	}
	return invite.User.AccountID
}

func inviteAutofollowUsesRequest(source models.Account, target models.Account) bool {
	return target.Locked || source.SilencedAt.Valid
}

func (s *Server) createStaffSignUpNotificationPayloads(tx *gorm.DB, source models.Account) ([]asynqLocalNotificationPayload, error) {
	users, err := s.staffUsersWithPermission(tx, rolePermissionManageUsers)
	if err != nil || len(users) == 0 {
		return nil, err
	}
	payloads := make([]asynqLocalNotificationPayload, 0, len(users))
	for _, user := range users {
		if user.AccountID == 0 || user.AccountID == source.ID {
			continue
		}
		payloads = append(payloads, asynqLocalNotificationPayload{
			ReceiverAccountID: user.AccountID,
			FromAccountID:     source.ID,
			ActivityID:        source.ID,
			ActivityType:      "Account",
			Type:              "admin.sign_up",
		})
	}
	return payloads, nil
}

func (s *Server) roleIDsWithPermission(tx *gorm.DB, permission int64) ([]int64, error) {
	var roles []models.UserRole
	if err := tx.Find(&roles).Error; err != nil {
		return nil, err
	}
	return roleIDsWithPermissionFromRoles(roles, permission), nil
}

func roleIDsWithPermissionFromRoles(roles []models.UserRole, permission int64) []int64 {
	everyone := models.UserRole{ID: -99, Permissions: rolePermissionInviteUsers, Position: -1}
	for i := range roles {
		if roles[i].ID == -99 {
			everyone = roles[i]
			break
		}
	}
	ids := make([]int64, 0, len(roles))
	for i := range roles {
		if computedRolePermissionsForUser(&roles[i], &everyone)&permission == permission {
			ids = append(ids, roles[i].ID)
		}
	}
	return ids
}

type signUpIPRestriction struct {
	Blocked          bool
	RequiresApproval bool
}

func (s *Server) signUpIPRestriction(remoteIP string, now time.Time) (signUpIPRestriction, error) {
	if s.db == nil || strings.TrimSpace(remoteIP) == "" {
		return signUpIPRestriction{}, nil
	}
	var blocks []models.IPBlock
	if err := s.db.
		Where("severity IN ?", []int{5000, 5500}).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Find(&blocks).Error; err != nil {
		return signUpIPRestriction{}, err
	}
	return signUpIPRestrictionForBlocks(remoteIP, blocks), nil
}

func signUpIPRestrictionForBlocks(remoteIP string, blocks []models.IPBlock) signUpIPRestriction {
	restriction := signUpIPRestriction{}
	for _, block := range blocks {
		if !ipMatchesBlock(remoteIP, block.IP) {
			continue
		}
		switch block.Severity {
		case 5500:
			restriction.Blocked = true
		case 5000:
			restriction.RequiresApproval = true
		}
	}
	return restriction
}

func ipMatchesBlock(remoteIP string, blockValue string) bool {
	ip := net.ParseIP(strings.TrimSpace(remoteIP))
	if ip == nil {
		return false
	}
	blockValue = strings.TrimSpace(blockValue)
	if blockValue == "" {
		return false
	}
	if strings.Contains(blockValue, "/") {
		_, network, err := net.ParseCIDR(blockValue)
		return err == nil && network.Contains(ip)
	}
	blockIP := net.ParseIP(blockValue)
	return blockIP != nil && blockIP.Equal(ip)
}

func accountApprovedForRegistration(mode string, invite *models.Invite, requiresApproval bool) bool {
	if requiresApproval {
		return false
	}
	return mode == "open" || invite != nil
}

func (s *Server) findUsableInvite(code string, now time.Time) (*models.Invite, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, nil
	}
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var invite models.Invite
	if err := s.db.Preload("User.Account").Where("code = ?", code).First(&invite).Error; err != nil {
		return nil, err
	}
	if !inviteUsable(invite, now) {
		return nil, gorm.ErrRecordNotFound
	}
	return &invite, nil
}

func inviteUsable(invite models.Invite, now time.Time) bool {
	if invite.ID == 0 || inviteExpired(invite, now) {
		return false
	}
	if invite.MaxUses.Valid && invite.Uses >= invite.MaxUses.Int64 {
		return false
	}
	if invite.User.ID == 0 || !inviteUserFunctionalForUse(invite.User) {
		return false
	}
	return true
}

func inviteUserFunctionalForUse(user models.User) bool {
	if user.Disabled || !user.Approved || !user.ConfirmedAt.Valid {
		return false
	}
	if user.Account != nil {
		if user.Account.MovedToAccountID.Valid || user.Account.SuspendedAt.Valid || user.Account.Memorial {
			return false
		}
	}
	return true
}

func (s *Server) registrationsEnabledForInvite(invite *models.Invite) bool {
	if s.cfg.SingleUserMode {
		return false
	}
	if s.cfg.OmniAuthOnly || s.cfg.DisableSignupByAPI {
		return false
	}
	return s.registrationMode() != "none" || invite != nil
}

func (s *Server) webRegistrationsAllowedForInvite(invite *models.Invite) bool {
	if s.webSingleUserMode() {
		return false
	}
	if s.cfg.OmniAuthOnly {
		return false
	}
	return s.registrationMode() != "none" || invite != nil
}

func (s *Server) webSingleUserMode() bool {
	if s == nil || !s.cfg.SingleUserMode {
		return false
	}
	if s.db == nil {
		return true
	}
	var count int64
	if err := s.db.Model(&models.Account{}).Where("id > 0").Limit(1).Count(&count).Error; err != nil {
		return true
	}
	return count > 0
}

func (s *Server) webInviteRequestTextRequired(invite *models.Invite) bool {
	return s.settingBoolValue("require_invite_text", false) && s.registrationMode() != "open" && invite == nil
}

func apiErrorMessage(err error) string {
	if apiErr, ok := err.(apiHTTPError); ok {
		return apiErr.message
	}
	return err.Error()
}

func registrationWebErrorMessage(locale string, err error) string {
	message := apiErrorMessage(err)
	switch message {
	case registrationValidationUsernameInvalid:
		return registrationErrorMessage(locale, "username_invalid", message)
	case registrationValidationEmailInvalid:
		return registrationErrorMessage(locale, "email_invalid", message)
	case registrationValidationPasswordInvalid:
		return registrationErrorMessage(locale, "password_invalid", message)
	case registrationValidationAgreementRequired:
		return registrationErrorMessage(locale, "agreement_required", message)
	case registrationValidationUsernameReserved:
		return registrationErrorMessage(locale, "username_reserved", message)
	case registrationValidationUsernameOrEmailTaken:
		return registrationErrorMessage(locale, "taken", message)
	case registrationValidationReasonRequired:
		return registrationErrorMessage(locale, "reason_required", message)
	case registrationValidationReasonTooLong:
		return registrationErrorMessage(locale, "reason_too_long", message)
	case registrationValidationTooFast:
		return settingsT(locale, "auth.too_fast", message)
	case registrationValidationHoneypot:
		return registrationErrorMessage(locale, "honeypot", message)
	case registrationTurnstileMalformedRequest:
		return registrationErrorMessage(locale, "turnstile_malformed", message)
	default:
		return message
	}
}

func registrationErrorMessage(locale string, key string, fallback string) string {
	return settingsT(locale, "auth.sign_up.errors."+key, fallback)
}

func (s *Server) setRegistrationFormTimeCookie(c *echo.Context, now time.Time) {
	setCookie(c, registrationFormTimeCookieName, s.registrationFormTimeCookieValue(now), 60*30, s.cfg.ForceSSL)
}

func (s *Server) validateRegistrationFormTime(c *echo.Context, now time.Time) error {
	cookie, err := c.Cookie(registrationFormTimeCookieName)
	if err != nil {
		return nil
	}
	startedAt, ok := s.registrationFormTimeFromCookie(cookie.Value, now)
	if !ok {
		return nil
	}
	if now.Sub(startedAt) < 3*time.Second {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationValidationTooFast}
	}
	return nil
}

func (s *Server) registrationFormTimeCookieValue(at time.Time) string {
	seconds := strconv.FormatInt(at.UTC().Unix(), 10)
	sig := registrationFormTimeSignature(seconds, s.registrationFormTimeCookieSecret())
	return seconds + "." + sig
}

func (s *Server) registrationFormTimeFromCookie(value string, now time.Time) (time.Time, bool) {
	seconds, sig, ok := strings.Cut(strings.TrimSpace(value), ".")
	if !ok || seconds == "" || sig == "" {
		return time.Time{}, false
	}
	if !hmac.Equal([]byte(sig), []byte(registrationFormTimeSignature(seconds, s.registrationFormTimeCookieSecret()))) {
		return time.Time{}, false
	}
	unix, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	at := time.Unix(unix, 0).UTC()
	if at.After(now.Add(5 * time.Minute)) {
		return time.Time{}, false
	}
	return at, true
}

func (s *Server) registrationFormTimeCookieSecret() string {
	if s != nil {
		if strings.TrimSpace(s.cfg.SecretKeyBase) != "" {
			return s.cfg.SecretKeyBase
		}
		if strings.TrimSpace(s.cfg.LocalDomain) != "" {
			return s.cfg.LocalDomain
		}
	}
	return "paon-registration-form-time"
}

func registrationFormTimeSignature(value string, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) checkCloudflareTurnstile(c *echo.Context, response string) error {
	if !s.cfg.CloudflareTurnstileEnabled {
		return nil
	}
	if strings.TrimSpace(response) == "" || strings.TrimSpace(s.cfg.CloudflareTurnstileSecretKey) == "" {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationTurnstileMalformedRequest}
	}
	values := url.Values{}
	values.Set("secret", s.cfg.CloudflareTurnstileSecretKey)
	values.Set("response", response)
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, turnstileVerifyURL, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := turnstileHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationTurnstileMalformedRequest}
	}
	var payload struct {
		Success bool `json:"success"`
	}
	if err := decodeTurnstileResponse(resp, &payload); err != nil {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationTurnstileMalformedRequest}
	}
	if !payload.Success {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: registrationTurnstileMalformedRequest}
	}
	return nil
}

func (s *Server) hcaptchaAvailable() bool {
	return strings.TrimSpace(s.cfg.HCaptchaSecretKey) != "" && strings.TrimSpace(s.cfg.HCaptchaSiteKey) != ""
}

func (s *Server) captchaRequired() bool {
	return s.hcaptchaAvailable() && s.settingBoolValue("captcha_enabled", false)
}

func (s *Server) checkHCaptcha(c *echo.Context, response string) error {
	if !s.captchaRequired() {
		return nil
	}
	return s.verifyHCaptcha(c, response)
}

func (s *Server) verifyHCaptcha(c *echo.Context, response string) error {
	if strings.TrimSpace(response) == "" {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "hCaptcha verification failed"}
	}
	values := url.Values{}
	values.Set("secret", s.cfg.HCaptchaSecretKey)
	values.Set("response", response)
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, hcaptchaVerifyURL, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hcaptchaHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "hCaptcha verification failed"}
	}
	var payload struct {
		Success bool `json:"success"`
	}
	if err := decodeCaptchaResponse(resp, &payload); err != nil {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "hCaptcha verification failed"}
	}
	if !payload.Success {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "hCaptcha verification failed"}
	}
	return nil
}

func decodeTurnstileResponse(resp *http.Response, out any) error {
	return decodeCaptchaResponse(resp, out)
}

func decodeCaptchaResponse(resp *http.Response, out any) error {
	if resp == nil || resp.Body == nil {
		return errors.New("captcha response body is empty")
	}
	if resp.ContentLength > maxTurnstileResponseBodySize {
		return errors.New("captcha response body is too large")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTurnstileResponseBodySize+1))
	if err != nil {
		return err
	}
	if len(body) > maxTurnstileResponseBodySize {
		return errors.New("captcha response body is too large")
	}
	return json.Unmarshal(body, out)
}

func (s *Server) registrationPageHTML(errorText string, inviteCode string, locale string, acceptTokens ...string) string {
	return s.registrationPageHTMLForInvite(errorText, nil, inviteCode, locale, acceptTokens...)
}

func (s *Server) registrationPageHTMLForInvite(errorText string, invite *models.Invite, inviteCode string, locale string, acceptTokens ...string) string {
	manualReview := s.registrationMode() == "approved" && strings.TrimSpace(inviteCode) == ""
	acceptToken := ""
	if len(acceptTokens) > 0 {
		acceptToken = acceptTokens[0]
	}
	return registrationHTMLWithTurnstile(s.cfg.LocalDomain, errorText, inviteCode, s.cfg.CloudflareTurnstileEnabled, s.cfg.CloudflareTurnstileSiteKey, locale, manualReview, acceptToken, inviteRegistrationContext(invite))
}

func registrationHTML(domain string, errorText string, inviteCode string) string {
	return registrationHTMLWithTurnstile(domain, errorText, inviteCode, false, "", "")
}

func registrationRulesHTML(rules []models.Rule, inviteCode string, acceptToken string, invited bool, manualReview bool, domain string, locales ...string) string {
	inviteContext := registrationInviteContext{}
	if invited {
		inviteContext = registrationInviteContext{Invited: true}
	}
	return registrationRulesHTMLForInvite(rules, inviteCode, acceptToken, manualReview, domain, inviteContext, locales...)
}

func registrationRulesHTMLForInvite(rules []models.Rule, inviteCode string, acceptToken string, manualReview bool, domain string, inviteContext registrationInviteContext, locales ...string) string {
	locale := settingsLocaleArg(locales...)
	domain = firstNonEmpty(strings.TrimSpace(domain), "this server")
	var rows strings.Builder
	rows.WriteString(`<ol class="rules-list">`)
	for _, rule := range rules {
		rows.WriteString(`<li><div class="rules-list__text">` + html.EscapeString(rule.Text) + `</div></li>`)
	}
	rows.WriteString(`</ol>`)

	acceptPath := "/auth/sign_up?accept=" + url.QueryEscape(acceptToken)
	if strings.TrimSpace(inviteCode) != "" {
		acceptPath = "/invite/" + url.PathEscape(inviteCode) + "?accept=" + url.QueryEscape(acceptToken)
	}
	title := webT(locale, "auth.rules.title")
	lead := webT(locale, "auth.rules.preamble", map[string]string{"domain": domain})
	if inviteContext.Invited {
		title = webT(locale, "auth.rules.title_invited")
		lead = webT(locale, "auth.rules.preamble_invited", map[string]string{"domain": domain})
	}
	var body strings.Builder
	body.WriteString(registrationProgressHTML("rules", manualReview, locale))
	body.WriteString(`<h1 class="title">` + html.EscapeString(title) + `</h1>`)
	if inviteContext.Autofollow && inviteContext.Account != nil {
		body.WriteString(`<p class="lead invited-by">` + html.EscapeString(webT(locale, "auth.rules.invited_by", map[string]string{"domain": domain})) + `</p>`)
		body.WriteString(registrationInviterCardHTML(*inviteContext.Account, true))
	}
	body.WriteString(`<p class="lead">` + html.EscapeString(lead) + `</p>`)
	body.WriteString(rows.String())
	body.WriteString(`<div class="stacked-actions"><a class="button" href="` + html.EscapeString(acceptPath) + `">` + html.EscapeString(webT(locale, "auth.rules.accept")) + `</a> <a class="button button-tertiary" href="/">` + html.EscapeString(webT(locale, "auth.rules.back")) + `</a></div>`)
	body.WriteString(registrationAuthLinksHTML(locale))
	return authShellHTML(title, "", "", body.String(), locale)
}

func registrationHTMLWithTurnstile(domain string, errorText string, inviteCode string, turnstileEnabled bool, turnstileSiteKey string, locale string, args ...any) string {
	requireManualReview := false
	if len(args) > 0 {
		requireManualReview, _ = args[0].(bool)
	}
	acceptToken := ""
	if len(args) > 1 {
		acceptToken, _ = args[1].(string)
	}
	inviteContext := registrationInviteContext{}
	if len(args) > 2 {
		inviteContext, _ = args[2].(registrationInviteContext)
	}
	inviteInput := ""
	if strings.TrimSpace(inviteCode) != "" {
		inviteInput = `<input type="hidden" name="user[invite_code]" value="` + html.EscapeString(inviteCode) + `">`
	}
	turnstileHTML := ""
	if turnstileEnabled {
		turnstileHTML = `<div class="turnstile"><script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script><div class="cf-turnstile" data-sitekey="` + html.EscapeString(turnstileSiteKey) + `"></div></div>`
	}
	title := webT(locale, "auth.register")
	usernameLabel := webT(locale, "simple_form.labels.defaults.username")
	passwordLabel := webT(locale, "simple_form.labels.defaults.password")
	confirmPasswordLabel := webT(locale, "simple_form.labels.defaults.confirm_password")
	var body strings.Builder
	body.WriteString(simpleFormOpen("/auth", "post"))
	body.WriteString(registrationProgressHTML("details", requireManualReview, locale))
	body.WriteString(`<h1 class="title">` + html.EscapeString(webT(locale, "auth.sign_up.title", map[string]string{"domain": domain})) + `</h1>`)
	body.WriteString(`<p class="lead">` + html.EscapeString(webT(locale, "auth.sign_up.preamble")) + `</p>`)
	body.WriteString(inviteInput)
	body.WriteString(`<input type="hidden" name="accept" value="` + html.EscapeString(acceptToken) + `">`)
	if inviteContext.Autofollow && inviteContext.Account != nil {
		body.WriteString(`<div class="fields-group invited-by"><p class="hint">` + html.EscapeString(webT(locale, "invites.invited_by")) + `</p>`)
		body.WriteString(registrationInviterCardHTML(*inviteContext.Account, false))
		body.WriteString(`</div>`)
	}
	body.WriteString(`<div class="fields-group"><div class="input with_label"><div class="label_input"><label>` + html.EscapeString(usernameLabel) + `</label><input type="text" aria-label="` + html.EscapeString(usernameLabel) + `" name="user[account_attributes][username]" autocomplete="off" pattern="[a-zA-Z0-9_]+" maxlength="30" required> <span class="suffix">@` + html.EscapeString(domain) + `</span></div></div></div>`)
	body.WriteString(simpleTextInput(webT(locale, "simple_form.labels.defaults.email"), "user[email]", "", "email", `autocomplete="username" required`))
	body.WriteString(simpleTextInput(passwordLabel, "user[password]", "", "password", `autocomplete="new-password" minlength="8" maxlength="72" required`))
	body.WriteString(simpleTextInput(confirmPasswordLabel, "user[password_confirmation]", "", "password", `autocomplete="new-password" maxlength="72" required`))
	body.WriteString(`<div class="fields-group"><div class="input string optional user_confirm_password with_label"><div class="label_input"><label>` + html.EscapeString(webT(locale, "simple_form.labels.defaults.honeypot", map[string]string{"label": passwordLabel})) + `</label><input type="text" aria-label="` + html.EscapeString(webT(locale, "simple_form.labels.defaults.honeypot", map[string]string{"label": passwordLabel})) + `" name="user[confirm_password]" autocomplete="off"></div></div></div>`)
	body.WriteString(`<div class="fields-group"><div class="input url optional user_website with_label"><div class="label_input"><label>` + html.EscapeString(webT(locale, "simple_form.labels.defaults.honeypot", map[string]string{"label": "Website"})) + `</label><input type="url" aria-label="` + html.EscapeString(webT(locale, "simple_form.labels.defaults.honeypot", map[string]string{"label": "Website"})) + `" name="user[website]" autocomplete="off"></div></div></div>`)
	if requireManualReview {
		body.WriteString(`<p class="lead">` + html.EscapeString(webT(locale, "auth.sign_up.manual_review", map[string]string{"domain": domain})) + `</p>`)
		body.WriteString(`<div class="fields-group"><div class="input text required user_invite_request_text with_block_label"><div class="label_input"><label>` + html.EscapeString(webT(locale, "auth.sign_up.manual_review", map[string]string{"domain": domain})) + `</label><textarea name="user[invite_request_attributes][text]" required></textarea></div></div></div>`)
	}
	body.WriteString(`<div class="fields-group"><div class="input boolean required"><label class="boolean"><input type="checkbox" name="user[agreement]" value="true" required> ` + webT(locale, "auth.privacy_policy_agreement_html", map[string]string{"rules_path": "/about/more", "privacy_policy_path": "/privacy-policy"}) + `</label></div></div>`)
	body.WriteString(turnstileHTML)
	body.WriteString(simpleSubmit(title))
	body.WriteString(simpleFormClose())
	body.WriteString(registrationAuthLinksHTML(locale))
	return authShellHTML(title, "", errorText, body.String(), locale)
}

type registrationInviteContext struct {
	Invited    bool
	Autofollow bool
	Account    *models.Account
}

func inviteRegistrationContext(invite *models.Invite) registrationInviteContext {
	if invite == nil {
		return registrationInviteContext{}
	}
	context := registrationInviteContext{Invited: true}
	if invite.Autofollow && invite.User.Account != nil && invite.User.Account.ID != 0 {
		context.Autofollow = true
		context.Account = invite.User.Account
	}
	return context
}

func registrationInviterCardHTML(account models.Account, compact bool) string {
	classes := "account-card"
	if compact {
		classes += " compact"
	}
	display := strings.TrimSpace(account.DisplayName)
	if display == "" {
		display = accountDisplayName(account)
	}
	acct := account.Acct()
	href := accountWebPath(account)
	return `<div class="` + classes + `"><a class="name-tag" href="` + html.EscapeString(href) + `"><span class="username">` + html.EscapeString(display) + `</span><small>@` + html.EscapeString(acct) + `</small></a></div>`
}

func registrationProgressHTML(stage string, review bool, locale string) string {
	index := map[string]int{"rules": 0, "details": 1, "confirm": 2}[stage]
	itemClass := func(item int) string {
		switch {
		case index > item:
			return ` class="completed"`
		case index == item:
			return ` class="active"`
		default:
			return ""
		}
	}
	separator := func(done bool) string {
		if done {
			return `<li class="separator completed"></li>`
		}
		return `<li class="separator"></li>`
	}
	circle := func(done bool) string {
		if done {
			return `<div class="circle">&#10003;</div>`
		}
		return `<div class="circle"></div>`
	}
	var b strings.Builder
	b.WriteString(`<ol class="progress-tracker">`)
	b.WriteString(`<li` + itemClass(0) + `>` + circle(index > 0) + `<div class="label">` + html.EscapeString(webT(locale, "auth.progress.rules")) + `</div></li>`)
	b.WriteString(separator(index > 0))
	b.WriteString(`<li` + itemClass(1) + `>` + circle(index > 1) + `<div class="label">` + html.EscapeString(webT(locale, "auth.progress.details")) + `</div></li>`)
	b.WriteString(separator(index > 1))
	b.WriteString(`<li` + itemClass(2) + `>` + circle(index > 2) + `<div class="label">` + html.EscapeString(webT(locale, "auth.progress.confirm")) + `</div></li>`)
	if review {
		b.WriteString(separator(index > 2) + `<li><div class="circle"></div><div class="label">` + html.EscapeString(webT(locale, "auth.progress.review")) + `</div></li>`)
	}
	b.WriteString(`</ol>`)
	return b.String()
}

func registrationAuthLinksHTML(locale string) string {
	return authSharedFooterHTML("registrations", "/auth/sign_up", locale)
}

func (s *Server) requireApplicationWriteToken(c *echo.Context, scopes ...string) (*oauthApplication, string, error) {
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil || !accessToken.ApplicationID.Valid {
		return nil, "", apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !tokenHasAnyScope(accessToken.Scopes, append(scopes, "write")...) {
		return nil, "", apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}
	var app oauthApplication
	if err := s.db.Where("id = ?", accessToken.ApplicationID.Int64).First(&app).Error; err != nil {
		return nil, "", apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	return &app, string(accessToken.Scopes), nil
}

func (s *Server) registrationsEnabled() bool {
	if s.cfg.SingleUserMode {
		return false
	}
	return s.registrationMode() != "none" && !s.cfg.OmniAuthOnly && !s.cfg.DisableSignupByAPI
}

func (s *Server) registrationMode() string {
	value := strings.Trim(strings.TrimSpace(s.settingValue("registrations_mode", "none")), "\"")
	switch value {
	case "open", "approved":
		return value
	default:
		return "none"
	}
}

func generateAccountKeyPair() (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	return string(privatePEM), string(publicPEM), nil
}

func isUniqueConstraintError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint")
}

func nullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}
