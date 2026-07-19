package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/pbkdf2"
	"gorm.io/gorm"
)

func (s *Server) disableSettingsTwoFactor(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "post") {
		return c.Redirect(http.StatusFound, "/settings/two_factor_authentication_methods")
	}
	if handled, err := s.settingsRequireChallenge(c, user, "/settings/two_factor_authentication_methods/disable", locale); handled || err != nil {
		return err
	}
	if !user.OTPRequiredForLogin {
		return c.Redirect(http.StatusFound, "/settings/otp_authentication")
	}
	if err := s.disableTwoFactorForUser(user.ID); err != nil {
		return err
	}
	if err := s.sendTwoFactorDisabledMail(*user); err != nil {
		return mailDeliveryError("two-factor disabled", err)
	}
	return c.Redirect(http.StatusFound, "/settings/otp_authentication?notice="+url.QueryEscape(settingsT(locale, "two_factor_authentication.disabled_success", "Two-factor authentication successfully disabled")))
}

func (s *Server) settingsOTPAuthenticationPage(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	if user.OTPRequiredForLogin {
		return c.Redirect(http.StatusFound, "/settings/two_factor_authentication_methods")
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, otpAuthenticationHTML(c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) createSettingsOTPAuthentication(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	if user.OTPRequiredForLogin {
		return c.Redirect(http.StatusFound, "/settings/two_factor_authentication_methods")
	}
	if handled, err := s.settingsRequireChallenge(c, user, "/settings/otp_authentication", s.webLocale(c, user)); handled || err != nil {
		return err
	}
	secret, err := generateOTPSecret(32)
	if err != nil {
		return err
	}
	if err := s.setBrowserNewOTPSecret(c, user.ID, secret); err != nil {
		return err
	}
	expireCookie(c, "paon_new_otp_secret", s.cfg.ForceSSL)
	return c.Redirect(http.StatusFound, "/settings/two_factor_authentication/confirmation/new")
}

func (s *Server) newSettingsTwoFactorConfirmation(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	secret, ok := s.browserNewOTPSecret(c, user.ID)
	if !ok {
		return c.Redirect(http.StatusFound, "/settings/otp_authentication")
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, twoFactorConfirmationHTMLWithProvisioning(secret, "", firstNonEmpty(s.cfg.Title, s.cfg.LocalDomain, "Mastodon"), user.Email, renderArgs...))
}

func (s *Server) createSettingsTwoFactorConfirmation(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	secret, ok := s.browserNewOTPSecret(c, user.ID)
	if !ok {
		return c.Redirect(http.StatusFound, "/settings/otp_authentication")
	}
	attempt, err := twoFactorConfirmationAttempt(c)
	if errors.Is(err, errTwoFactorConfirmationParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	if !validTOTP(secret, attempt, time.Now().UTC()) {
		return c.HTML(http.StatusOK, twoFactorConfirmationHTMLWithProvisioning(secret, settingsT(locale, "otp_authentication.wrong_code", "The entered code was invalid! Are server time and device time correct?"), firstNonEmpty(s.cfg.Title, s.cfg.LocalDomain, "Mastodon"), user.Email, renderArgs...))
	}
	if err := s.enableGoOTPForUser(user.ID, secret); err != nil {
		return err
	}
	recoveryCodes, err := s.regenerateRecoveryCodesForUser(user.ID)
	if err != nil {
		return err
	}
	if err := s.sendTwoFactorEnabledMail(*user); err != nil {
		return mailDeliveryError("two-factor enabled", err)
	}
	if err := s.clearBrowserNewOTPSecret(c); err != nil {
		return err
	}
	expireCookie(c, "paon_new_otp_secret", s.cfg.ForceSSL)
	return c.HTML(http.StatusOK, recoveryCodesHTML(settingsT(locale, "two_factor_authentication.enabled_success", "Two-factor authentication successfully enabled"), recoveryCodes, renderArgs...))
}

var errTwoFactorConfirmationParamsMissing = errors.New("two-factor confirmation root parameter is missing")

func twoFactorConfirmationAttempt(c *echo.Context) (string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return "", err
	}
	const prefix = "form_two_factor_confirmation"
	if !formHasNestedPrefix(req.Form, prefix) {
		return "", errTwoFactorConfirmationParamsMissing
	}
	return lastFormValue(req.Form, prefix+"[otp_attempt]"), nil
}

func (s *Server) createSettingsRecoveryCodes(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	if !user.OTPRequiredForLogin {
		return c.Redirect(http.StatusFound, "/settings/otp_authentication")
	}
	if handled, err := s.settingsRequireChallenge(c, user, "/settings/two_factor_authentication_methods", locale); handled || err != nil {
		return err
	}
	codes, err := s.regenerateRecoveryCodesForUser(user.ID)
	if err != nil {
		return err
	}
	if err := s.sendTwoFactorRecoveryCodesChangedMail(*user); err != nil {
		return mailDeliveryError("two-factor recovery codes", err)
	}
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, recoveryCodesHTML(settingsT(locale, "two_factor_authentication.recovery_codes_regenerated", "Recovery codes successfully regenerated"), codes, renderArgs...))
}

func (s *Server) disableTwoFactorForUser(userID int64) error {
	if s.db == nil {
		return gorm.ErrRecordNotFound
	}
	now := time.Now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		return disableTwoFactorForUserTx(tx, userID, now)
	})
}

func disableTwoFactorForUserTx(tx *gorm.DB, userID int64, now time.Time) error {
	if err := tx.Where("user_id = ?", userID).Delete(&models.WebauthnCredential{}).Error; err != nil {
		return err
	}
	return tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		"otp_required_for_login":    false,
		"encrypted_otp_secret":      nil,
		"encrypted_otp_secret_iv":   nil,
		"encrypted_otp_secret_salt": nil,
		"consumed_timestep":         nil,
		"otp_backup_codes":          models.StringArray{},
		"updated_at":                now,
	}).Error
}

const goOTPSecretPrefix = "paon-go-totp:"

func (s *Server) enableGoOTPForUser(userID int64, secret string) error {
	if s.db == nil {
		return gorm.ErrRecordNotFound
	}
	secret = normalizeOTPSecret(secret)
	if secret == "" {
		return gorm.ErrRecordNotFound
	}
	now := time.Now().UTC()
	return s.db.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		"otp_required_for_login":    true,
		"encrypted_otp_secret":      goOTPSecretPrefix + secret,
		"encrypted_otp_secret_iv":   nil,
		"encrypted_otp_secret_salt": nil,
		"consumed_timestep":         nil,
		"updated_at":                now,
	}).Error
}

func (s *Server) validateAndConsumeUserOTP(user *models.User, attempt string, now time.Time) error {
	secret, ok := goOTPSecretFromUser(user)
	if s.db == nil {
		return gorm.ErrRecordNotFound
	}
	if ok {
		step, valid := validTOTPStep(secret, attempt, now)
		if valid {
			if user.ConsumedTimestep.Valid && step <= user.ConsumedTimestep.Int64 {
				return errors.New("otp already consumed")
			}
			return s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
				"consumed_timestep": step,
				"updated_at":        now,
			}).Error
		}
	} else if legacySecret, legacyOK := s.legacyOTPSecretFromUser(user); legacyOK {
		step, valid := validTOTPStep(legacySecret, attempt, now)
		if valid {
			if user.ConsumedTimestep.Valid && step <= user.ConsumedTimestep.Int64 {
				return errors.New("otp already consumed")
			}
			return s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
				"consumed_timestep": step,
				"updated_at":        now,
			}).Error
		}
	}
	if err := s.consumeUserRecoveryCode(user, attempt, now); err == nil {
		return nil
	}
	return errors.New("invalid otp")
}

func goOTPSecretFromUser(user *models.User) (string, bool) {
	if user == nil || !user.EncryptedOTPSecret.Valid {
		return "", false
	}
	value := strings.TrimSpace(user.EncryptedOTPSecret.String)
	if !strings.HasPrefix(value, goOTPSecretPrefix) {
		return "", false
	}
	secret := normalizeOTPSecret(strings.TrimPrefix(value, goOTPSecretPrefix))
	return secret, secret != ""
}

func (s *Server) legacyOTPSecretFromUser(user *models.User) (string, bool) {
	if user == nil || strings.TrimSpace(s.cfg.OTPSecret) == "" {
		return "", false
	}
	secret, err := decryptLegacyOTPSecret(
		user.EncryptedOTPSecret.String,
		user.EncryptedOTPSecretIV.String,
		user.EncryptedOTPSecretSalt.String,
		s.cfg.OTPSecret,
	)
	if err != nil {
		return "", false
	}
	secret = normalizeOTPSecret(secret)
	return secret, secret != ""
}

func decryptLegacyOTPSecret(encryptedValue string, encodedIV string, encodedSalt string, otpSecretKey string) (string, error) {
	if strings.TrimSpace(encryptedValue) == "" || strings.TrimSpace(encodedIV) == "" || strings.TrimSpace(encodedSalt) == "" || otpSecretKey == "" {
		return "", errors.New("missing legacy otp secret material")
	}
	ciphertextWithTag, err := decodeAttrEncryptedBase64(encryptedValue)
	if err != nil {
		return "", err
	}
	iv, err := decodeAttrEncryptedBase64(encodedIV)
	if err != nil {
		return "", err
	}
	salt, err := decodeAttrEncryptedSalt(encodedSalt)
	if err != nil {
		return "", err
	}
	if len(ciphertextWithTag) <= 16 {
		return "", errors.New("legacy otp ciphertext is too short")
	}
	key := pbkdf2.Key([]byte(otpSecretKey), salt, 2000, 32, sha1.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := gcm.Open(nil, iv, ciphertextWithTag, []byte(""))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func decodeAttrEncryptedSalt(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "$") {
		return decodeAttrEncryptedBase64(strings.TrimPrefix(value, "$"))
	}
	return []byte(value), nil
}

func decodeAttrEncryptedBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty attr_encrypted value")
	}
	return base64.StdEncoding.DecodeString(value)
}

func normalizeOTPSecret(secret string) string {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	secret = strings.ReplaceAll(secret, " ", "")
	secret = strings.TrimRight(secret, "=")
	return secret
}

func validTOTP(secret string, attempt string, now time.Time) bool {
	_, ok := validTOTPStep(secret, attempt, now)
	return ok
}

func validTOTPStep(secret string, attempt string, now time.Time) (int64, bool) {
	code := normalizeOTPAttempt(attempt)
	if code == "" {
		return 0, false
	}
	secret = normalizeOTPSecret(secret)
	if secret == "" {
		return 0, false
	}
	step := now.Unix() / 30
	for _, candidate := range []int64{step - 1, step, step + 1} {
		expected, err := hotp(secret, candidate)
		if err != nil {
			return 0, false
		}
		if hmac.Equal([]byte(expected), []byte(code)) {
			return candidate, true
		}
	}
	return 0, false
}

func normalizeOTPAttempt(attempt string) string {
	attempt = strings.TrimSpace(attempt)
	attempt = strings.ReplaceAll(attempt, " ", "")
	if len(attempt) < 6 {
		return ""
	}
	if _, err := strconv.Atoi(attempt); err != nil {
		return ""
	}
	if len(attempt) > 6 {
		attempt = attempt[len(attempt)-6:]
	}
	return attempt
}

func hotp(secret string, counter int64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalizeOTPSecret(secret))
	if err != nil {
		return "", err
	}
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], uint64(counter))
	hash := hmac.New(sha1.New, key)
	if _, err := hash.Write(counterBytes[:]); err != nil {
		return "", err
	}
	sum := hash.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (int(sum[offset])&0x7f)<<24 |
		(int(sum[offset+1])&0xff)<<16 |
		(int(sum[offset+2])&0xff)<<8 |
		(int(sum[offset+3]) & 0xff)
	return fmt.Sprintf("%06d", value%1000000), nil
}

func (s *Server) regenerateRecoveryCodesForUser(userID int64) ([]string, error) {
	codes, err := generateRecoveryCodes(10)
	if err != nil {
		return nil, err
	}
	hashes := make(models.StringArray, 0, len(codes))
	for _, code := range codes {
		hash, err := recoveryCodeHash(code)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, hash)
	}
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	if err := s.db.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]any{
		"otp_backup_codes": hashes,
		"updated_at":       time.Now().UTC(),
	}).Error; err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Server) consumeUserRecoveryCode(user *models.User, attempt string, now time.Time) error {
	code := normalizeRecoveryCode(attempt)
	if user == nil || code == "" || len(user.OTPBackupCodes) == 0 || s.db == nil {
		return errors.New("invalid recovery code")
	}
	remaining := make(models.StringArray, 0, len(user.OTPBackupCodes))
	matched := false
	for _, hash := range user.OTPBackupCodes {
		if !matched && recoveryCodeMatches(hash, code) {
			matched = true
			continue
		}
		remaining = append(remaining, hash)
	}
	if !matched {
		return errors.New("invalid recovery code")
	}
	return s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"otp_backup_codes": remaining,
		"updated_at":       now,
	}).Error
}

func generateRecoveryCodes(count int) ([]string, error) {
	if count <= 0 {
		count = 10
	}
	codes := make([]string, 0, count)
	seen := map[string]struct{}{}
	for len(codes) < count {
		code, err := randomRecoveryCode(10)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	return codes, nil
}

func randomRecoveryCode(length int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyz23456789"
	var out strings.Builder
	for out.Len() < length {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out.WriteByte(alphabet[index.Int64()])
	}
	return out.String(), nil
}

func recoveryCodeHash(code string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(normalizeRecoveryCode(code)), bcrypt.DefaultCost)
	return string(hash), err
}

func recoveryCodeMatches(hash string, code string) bool {
	return bcrypt.CompareHashAndPassword([]byte(normalizeBCryptPrefix(hash)), []byte(normalizeRecoveryCode(code))) == nil
}

func normalizeRecoveryCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, " ", "")
	code = strings.ReplaceAll(code, "-", "")
	return code
}

func generateOTPSecret(length int) (string, error) {
	if length <= 0 {
		length = 32
	}
	bytesNeeded := (length*5 + 7) / 8
	buf := make([]byte, bytesNeeded)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	if len(secret) > length {
		secret = secret[:length]
	}
	return secret, nil
}

func (s *Server) settingsRequireChallenge(c *echo.Context, user *models.User, returnTo string, locale string) (bool, error) {
	if user == nil || strings.TrimSpace(user.EncryptedPassword) == "" || s.browserChallengePassedRecently(c, user.ID) {
		return false, nil
	}
	action := "/auth/challenge"
	if c.Request().Method != http.MethodGet {
		action = c.Request().URL.RequestURI()
	}
	if err := c.Request().ParseForm(); err != nil {
		return true, err
	}
	if formHasNestedPrefix(c.Request().Form, "form_challenge") {
		challengeReturnTo, password, err := authChallengeParams(c)
		if err != nil {
			return true, err
		}
		if validBCryptPassword(user.EncryptedPassword, password) {
			if err := s.setBrowserChallengePassed(c, user.ID); err != nil {
				return true, err
			}
			expireCookie(c, "paon_challenge_passed_at", s.cfg.ForceSSL)
			return false, nil
		}
		return true, c.HTML(http.StatusOK, authChallengeHTML(challengeReturnTo, action, settingsT(locale, "challenge.invalid_password", "Invalid password"), locale))
	}
	return true, c.HTML(http.StatusOK, authChallengeHTML(returnTo, action, "", locale))
}

func otpAuthenticationHTML(notice string, errorText string, localeAndTheme ...string) string {
	locale := settingsLocaleArgOrEnglish(localeAndTheme...)
	title := webT(locale, "settings.two_factor_authentication")
	if len(localeAndTheme) > 2 && strings.TrimSpace(localeAndTheme[2]) != "" {
		body := `<div class="simple_form"><p class="hint">` + webT(locale, "otp_authentication.description_html") + `</p><hr class="spacer"><a class="block-button" data-method="post" href="/settings/otp_authentication">` + html.EscapeString(webT(locale, "otp_authentication.setup")) + `</a></div>`
		return accountSecurityPageHTML(title, "two_factor", notice, errorText, body, localeAndTheme...)
	}
	var body strings.Builder
	body.WriteString(`<p class="lead">` + webT(locale, "otp_authentication.description_html") + `</p>`)
	body.WriteString(simpleFormOpen("/settings/otp_authentication", "post"))
	body.WriteString(simpleSubmit(webT(locale, "otp_authentication.setup")))
	body.WriteString(simpleFormClose())
	bodyWithTitle := `<h1 class="title">` + html.EscapeString(title) + `</h1>` + body.String() + authFormFooter(`<ul class="no-list"><li><a href="/settings/two_factor_authentication_methods">`+html.EscapeString(title)+`</a></li></ul>`)
	return authShellHTML(title, notice, errorText, bodyWithTitle, locale)
}

func twoFactorConfirmationHTML(secret string, errorText string, localeAndTheme ...string) string {
	return twoFactorConfirmationHTMLWithProvisioning(secret, errorText, "Mastodon", "account", localeAndTheme...)
}

func twoFactorConfirmationHTMLWithProvisioning(secret string, errorText string, issuer string, accountName string, localeAndTheme ...string) string {
	locale := settingsLocaleArgOrEnglish(localeAndTheme...)
	title := webT(locale, "settings.two_factor_authentication")
	if len(localeAndTheme) > 2 && strings.TrimSpace(localeAndTheme[2]) != "" {
		qrImage := ""
		if dataURI := otpQRCodeDataURI(secret, issuer, accountName); dataURI != "" {
			qrImage = `<div class="qr-code"><img src="` + dataURI + `" width="192" height="192" alt="` + html.EscapeString(webT(locale, "otp_authentication.qr_code")) + `"></div>`
		}
		body := `<form class="simple_form new_form_two_factor_confirmation" id="new_form_two_factor_confirmation" novalidate="novalidate" method="post" action="/settings/two_factor_authentication/confirmation"><p class="hint">` + webT(locale, "otp_authentication.instructions_html") + `</p><div class="qr-wrapper">` + qrImage + `<div class="qr-alternative"><p class="hint">` + html.EscapeString(webT(locale, "otp_authentication.manual_instructions")) + `</p><samp class="qr-alternative__code">` + html.EscapeString(groupOTPSecret(secret)) + `</samp></div></div><div class="fields-group"><div class="input with_label string required form_two_factor_confirmation_otp_attempt field_with_hint"><div class="label_input"><label class="string required" for="form_two_factor_confirmation_otp_attempt">` + html.EscapeString(webT(locale, "simple_form.labels.defaults.otp_attempt")) + filterRequiredMarker(locale) + `</label><div class="label_input__wrapper"><input class="string required" autocomplete="off" required="required" aria-required="true" type="text" id="form_two_factor_confirmation_otp_attempt" name="form_two_factor_confirmation[otp_attempt]" inputmode="numeric"></div></div><span class="hint">` + html.EscapeString(webT(locale, "otp_authentication.code_hint")) + `</span></div></div><div class="actions"><button name="button" type="submit" class="btn">` + html.EscapeString(webT(locale, "otp_authentication.enable")) + `</button></div></form>`
		return accountSecurityPageHTML(title, "two_factor", "", errorText, body, localeAndTheme...)
	}
	var body strings.Builder
	body.WriteString(`<p class="lead">` + html.EscapeString(webT(locale, "otp_authentication.code_hint")) + `</p>`)
	body.WriteString(`<p><code>` + html.EscapeString(secret) + `</code></p>`)
	body.WriteString(simpleFormOpen("/settings/two_factor_authentication/confirmation", "post"))
	body.WriteString(simpleTextInput(webT(locale, "simple_form.labels.defaults.otp_attempt"), "form_two_factor_confirmation[otp_attempt]", "", "text", `inputmode="numeric" autocomplete="one-time-code" required`))
	body.WriteString(simpleSubmit(webT(locale, "generic.confirm")))
	body.WriteString(simpleFormClose())
	bodyWithTitle := `<h1 class="title">` + html.EscapeString(title) + `</h1>` + body.String() + authFormFooter(`<ul class="no-list"><li><a href="/settings/otp_authentication">`+html.EscapeString(webT(locale, "settings.back"))+`</a></li></ul>`)
	return authShellHTML(title, "", errorText, bodyWithTitle, locale)
}

func otpQRCodeDataURI(secret string, issuer string, accountName string) string {
	issuer = firstNonEmpty(strings.TrimSpace(issuer), "Mastodon")
	accountName = firstNonEmpty(strings.TrimSpace(accountName), "account")
	u := url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + issuer + ":" + accountName}
	query := u.Query()
	query.Set("secret", normalizeOTPSecret(secret))
	query.Set("issuer", issuer)
	u.RawQuery = query.Encode()
	png, err := qrcode.Encode(u.String(), qrcode.Medium, 192)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func groupOTPSecret(secret string) string {
	secret = normalizeOTPSecret(secret)
	groups := make([]string, 0, (len(secret)+3)/4)
	for len(secret) > 4 {
		groups = append(groups, secret[:4])
		secret = secret[4:]
	}
	if secret != "" {
		groups = append(groups, secret)
	}
	return strings.Join(groups, " ")
}

func recoveryCodesHTML(notice string, codes []string, localeAndTheme ...string) string {
	locale := settingsLocaleArgOrEnglish(localeAndTheme...)
	title := webT(locale, "settings.two_factor_authentication")
	if len(localeAndTheme) > 2 && strings.TrimSpace(localeAndTheme[2]) != "" {
		body := `<p class="hint">` + webT(locale, "two_factor_authentication.recovery_instructions_html") + `</p><ol class="recovery-codes">`
		for _, code := range codes {
			body += `<li><samp>` + html.EscapeString(code) + `</samp></li>`
		}
		body += `</ol>`
		return accountSecurityPageHTML(title, "two_factor", notice, "", body, localeAndTheme...)
	}
	var rows strings.Builder
	for _, code := range codes {
		rows.WriteString(`<li><code>` + html.EscapeString(code) + `</code></li>`)
	}
	var body strings.Builder
	body.WriteString(`<p class="lead">` + html.EscapeString(webT(locale, "two_factor_authentication.lost_recovery_codes")) + `</p>`)
	body.WriteString(`<ul>` + rows.String() + `</ul>`)
	bodyWithTitle := `<h1 class="title">` + html.EscapeString(title) + `</h1>` + body.String() + authFormFooter(`<ul class="no-list"><li><a href="/settings/two_factor_authentication_methods">`+html.EscapeString(webT(locale, "settings.two_factor_authentication"))+`</a></li></ul>`)
	return authShellHTML(title, notice, "", bodyWithTitle, locale)
}
