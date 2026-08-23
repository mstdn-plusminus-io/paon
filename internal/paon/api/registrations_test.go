package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestClosedRegistrationRedirectCarriesLocalizedContactAlert(t *testing.T) {
	previous, hadPrevious := railsSettingDefaults["site_contact_email"]
	railsSettingDefaults["site_contact_email"] = "moderation@example.test"
	t.Cleanup(func() {
		if hadPrevious {
			railsSettingDefaults["site_contact_email"] = previous
		} else {
			delete(railsSettingDefaults, "site_contact_email")
		}
	})

	tests := []struct {
		locale string
		want   string
	}{
		{locale: "en", want: "Your registration attempt has been blocked due to a network policy."},
		{locale: "ja", want: "ネットワークポリシーにより登録がブロックされました。"},
	}
	for _, tt := range tests {
		t.Run(tt.locale, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context := echo.NewContext(httptest.NewRequest(http.MethodGet, "/auth/sign_up", nil), recorder, echo.New())
			if err := (&Server{}).redirectClosedWebRegistration(context, tt.locale); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusFound {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
			}
			location, err := url.Parse(recorder.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			if location.Path != "/auth/sign_in" {
				t.Fatalf("redirect path = %q", location.Path)
			}
			alert := location.Query().Get("alert")
			if !strings.Contains(alert, tt.want) || !strings.Contains(alert, "moderation@example.test") {
				t.Fatalf("localized alert = %q", alert)
			}
		})
	}
}

func TestParseAccountCreatePayloadAcceptsFormValues(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/accounts", strings.NewReader("username=alice&email=alice%40example.test&password=correcthorsebattery&agreement=true&locale=ja&reason=hello&time_zone=Asia%2FTokyo"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAccountCreatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Username != "alice" || payload.Email != "alice@example.test" || payload.Locale != "ja" || payload.TimeZone != "Asia/Tokyo" {
		t.Fatalf("payload = %#v", payload)
	}
	if err := validateAccountCreatePayload(payload); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestParseAccountCreatePayloadAcceptsJSONBooleanAgreement(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/accounts", strings.NewReader(`{
		"username": "alice",
		"email": "alice@example.test",
		"password": "correcthorsebattery",
		"agreement": true,
		"locale": "ja",
		"reason": "hello",
		"time_zone": "Asia/Tokyo"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAccountCreatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Username != "alice" || payload.Email != "alice@example.test" || payload.Agreement != "true" || payload.Locale != "ja" || payload.TimeZone != "Asia/Tokyo" {
		t.Fatalf("payload = %#v", payload)
	}
	if err := validateAccountCreatePayload(payload); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestParseAccountCreatePayloadAcceptsDateOfBirth(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/accounts", strings.NewReader(`{
		"username":"alice","email":"alice@example.test","password":"correcthorsebattery",
		"agreement":true,"date_of_birth":"2000-08-12"
	}`))
	req.Header.Set("Content-Type", "application/json")
	payload, err := parseAccountCreatePayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if payload.DateOfBirth != "2000-08-12" {
		t.Fatalf("date_of_birth = %q", payload.DateOfBirth)
	}

	body := "user%5Baccount_attributes%5D%5Busername%5D=alice&user%5Bemail%5D=alice%40example.test&user%5Bpassword%5D=correcthorsebattery&user%5Bagreement%5D=true&user%5Bdate_of_birth%281i%29%5D=2000&user%5Bdate_of_birth%282i%29%5D=8&user%5Bdate_of_birth%283i%29%5D=12"
	req = httptest.NewRequest("POST", "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	payload, err = parseWebAccountCreatePayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if payload.DateOfBirth != "12.8.2000" {
		t.Fatalf("web date_of_birth = %q", payload.DateOfBirth)
	}
}

func TestValidateRegistrationAgeMatchesMastodon44Boundary(t *testing.T) {
	previous, hadPrevious := railsSettingDefaults["min_age"]
	railsSettingDefaults["min_age"] = "16"
	t.Cleanup(func() {
		if hadPrevious {
			railsSettingDefaults["min_age"] = previous
		} else {
			delete(railsSettingDefaults, "min_age")
		}
	})

	server := &Server{}
	now := time.Date(2026, time.August, 12, 15, 0, 0, 0, time.UTC)
	for _, date := range []string{"2010-08-12", "12.08.2010", "2009-08-13"} {
		if err := server.validateRegistrationAge(accountCreatePayload{DateOfBirth: date}, now); err != nil {
			t.Fatalf("eligible date %q rejected: %v", date, err)
		}
	}
	for _, test := range []struct {
		date    string
		message string
		code    string
	}{
		{date: "", message: registrationValidationDateOfBirthRequired, code: "ERR_BLANK"},
		{date: "not-a-date", message: registrationValidationDateOfBirthInvalid, code: "ERR_INVALID"},
		{date: "2010-08-13", message: registrationValidationDateOfBirthBelowLimit, code: "ERR_BELOW_LIMIT"},
	} {
		err := server.validateRegistrationAge(accountCreatePayload{DateOfBirth: test.date}, now)
		if err == nil || err.Error() != test.message {
			t.Fatalf("date %q error = %v, want %q", test.date, err, test.message)
		}
		apiErr, ok := err.(apiHTTPError)
		if !ok || !strings.Contains(string(mustJSON(t, apiErr.body)), test.code) {
			t.Fatalf("date %q response = %#v, want code %q", test.date, apiErr.body, test.code)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestRegistrationUserLocaleAndTimeZoneUseRailsSanitizers(t *testing.T) {
	src, err := os.ReadFile("registrations.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Locale:                 railsUserLocaleValue(payload.Locale),`,
		`TimeZone:               railsUserTimeZoneValue(payload.TimeZone),`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("registrations.go missing %q", want)
		}
	}
	if strings.Contains(string(src), `Locale:                 nullString(payload.Locale),`) || strings.Contains(string(src), `TimeZone:               nullString(payload.TimeZone),`) {
		t.Fatal("registration must not persist unsanitized locale/time_zone values")
	}
	if locale := railsUserLocaleValue(" ja "); locale.Valid {
		t.Fatalf("whitespace-padded locale should be rejected like Rails sanitizer, got %#v", locale)
	}
	if zone := railsUserTimeZoneValue(" Asia/Tokyo "); zone.Valid {
		t.Fatalf("whitespace-padded time_zone should be rejected like Rails sanitizer, got %#v", zone)
	}
}

func TestParseWebAccountCreatePayloadAcceptsDeviseNestedFormValues(t *testing.T) {
	body := "user%5Baccount_attributes%5D%5Busername%5D=alice&user%5Bemail%5D=alice%40example.test&user%5Bpassword%5D=correcthorsebattery&user%5Bagreement%5D=true&user%5Binvite_code%5D=abc123&user%5Binvite_request_attributes%5D%5Btext%5D=+hello+&user%5Bwebsite%5D=https%3A%2F%2Fspam.test&user%5Bconfirm_password%5D=bot&cf-turnstile-response=turnstile-token"
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseWebAccountCreatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Username != "alice" || payload.Email != "alice@example.test" || payload.InviteCode != "abc123" || payload.Reason != " hello " || payload.Website != "https://spam.test" || payload.ConfirmPassword != "bot" || payload.TurnstileResponse != "turnstile-token" {
		t.Fatalf("payload = %#v", payload)
	}
	if err := validateAccountCreatePayload(payload); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}

	req = httptest.NewRequest("POST", "/api/v1/accounts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	apiPayload, err := parseAccountCreatePayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if apiPayload.Username != "" || apiPayload.Email != "" || apiPayload.InviteCode != "" || apiPayload.Reason != "" {
		t.Fatalf("API payload must ignore Devise nested form fields: %#v", apiPayload)
	}
}

func TestParseWebAccountCreatePayloadAcceptsNestedJSONValues(t *testing.T) {
	body := `{
		"user": {
			"account_attributes": {"username": "alice"},
			"email": "alice@example.test",
			"password": "correcthorsebattery",
			"agreement": true,
			"invite_code": "abc123",
			"invite_request_attributes": {"text": " hello "},
			"website": "https://spam.test",
			"confirm_password": "bot",
			"time_zone": "Asia/Tokyo"
		},
		"cf-turnstile-response": "turnstile-token"
	}`
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseWebAccountCreatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Username != "alice" || payload.Email != "alice@example.test" || payload.InviteCode != "abc123" || payload.Reason != " hello " || payload.Website != "https://spam.test" || payload.ConfirmPassword != "bot" || payload.Agreement != "true" || payload.TurnstileResponse != "turnstile-token" {
		t.Fatalf("payload = %#v", payload)
	}
	if err := validateAccountCreatePayload(payload); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
}

func TestValidateAccountCreatePayloadRejectsInvalidValues(t *testing.T) {
	tests := []accountCreatePayload{
		{Username: "bad-name", Email: "alice@example.test", Password: "correcthorsebattery", Agreement: "true"},
		{Username: "alice", Email: "not-email", Password: "correcthorsebattery", Agreement: "true"},
		{Username: "alice", Email: "alice@example.test", Password: "short", Agreement: "true"},
		{Username: "alice", Email: "alice@example.test", Password: "correcthorsebattery", Agreement: "false"},
	}
	for _, test := range tests {
		if err := validateAccountCreatePayload(test); err == nil {
			t.Fatalf("invalid payload accepted: %#v", test)
		}
	}
}

func TestCreateAccountRequiresApplicationToken(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/accounts", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.createAccount(c); err == nil {
		t.Fatal("expected create account to require an application token")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestCreateAccountRequiresClientCredentialsToken(t *testing.T) {
	src, err := os.ReadFile("registrations.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !functionBodyContains(t, []byte(body), "requireApplicationWriteToken", `if accessToken.ResourceOwnerID.Valid {`) ||
		!functionBodyContains(t, []byte(body), "requireApplicationWriteToken", `"This method requires an client credentials authentication"`) {
		t.Fatal("Mastodon 4.4 account creation must reject user-owned access tokens")
	}
}

func TestCreateAccountUsesRailsAPIRegistrationGate(t *testing.T) {
	src, err := os.ReadFile("registrations.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "createAccount", `if !s.registrationsEnabled() {`) {
		t.Fatal("createAccount should use the Rails API registration gate, not invite-aware web registration")
	}
	for _, forbidden := range []string{"findUsableInvite", "registrationsEnabledForInvite", "Invite:        invite"} {
		if functionBodyContains(t, src, "createAccount", forbidden) {
			t.Fatalf("createAccount should not use %q; Rails API registration ignores invite_code", forbidden)
		}
	}
}

func TestSignUpIPRestrictionForBlocks(t *testing.T) {
	requires := signUpIPRestrictionForBlocks("192.0.2.10", []models.IPBlock{
		{IP: "192.0.2.0/24", Severity: 5000},
	})
	if !requires.RequiresApproval || requires.Blocked {
		t.Fatalf("requires approval restriction = %#v", requires)
	}

	blocked := signUpIPRestrictionForBlocks("2001:db8::10", []models.IPBlock{
		{IP: "2001:db8::/64", Severity: 5500},
	})
	if !blocked.Blocked {
		t.Fatalf("blocked restriction = %#v", blocked)
	}

	none := signUpIPRestrictionForBlocks("198.51.100.10", []models.IPBlock{
		{IP: "192.0.2.0/24", Severity: 5500},
	})
	if none.Blocked || none.RequiresApproval {
		t.Fatalf("unexpected restriction = %#v", none)
	}
}

func TestIPMatchesBlockAcceptsHostAndCIDR(t *testing.T) {
	if !ipMatchesBlock("192.0.2.10", "192.0.2.10") {
		t.Fatal("host IP did not match")
	}
	if !ipMatchesBlock("192.0.2.10", "192.0.2.0/24") {
		t.Fatal("CIDR IP did not match")
	}
	if ipMatchesBlock("192.0.2.10", "198.51.100.0/24") {
		t.Fatal("unrelated CIDR matched")
	}
	if ipMatchesBlock("bad", "192.0.2.0/24") || ipMatchesBlock("192.0.2.10", "bad") {
		t.Fatal("invalid IP or block matched")
	}
}

func TestAccountApprovedForRegistrationHonorsIPRequiresApproval(t *testing.T) {
	invite := &models.Invite{ID: 1}
	if !accountApprovedForRegistration("open", nil, false) {
		t.Fatal("open registrations should approve")
	}
	if !accountApprovedForRegistration("approved", invite, false) {
		t.Fatal("valid invite should approve")
	}
	if accountApprovedForRegistration("open", nil, true) {
		t.Fatal("IP requires approval should override open registrations")
	}
	if accountApprovedForRegistration("approved", invite, true) {
		t.Fatal("IP requires approval should override invite approval")
	}
}

func TestInviteAutofollowTargetAccountID(t *testing.T) {
	if got := inviteAutofollowTargetAccountID(nil); got != 0 {
		t.Fatalf("nil invite target = %d", got)
	}
	if got := inviteAutofollowTargetAccountID(&models.Invite{Autofollow: false, User: models.User{AccountID: 10}}); got != 0 {
		t.Fatalf("disabled autofollow target = %d", got)
	}
	if got := inviteAutofollowTargetAccountID(&models.Invite{Autofollow: true, User: models.User{AccountID: 10}}); got != 10 {
		t.Fatalf("autofollow target = %d", got)
	}
}

func TestInviteAutofollowUsesRequestForLockedOrSilencedAccounts(t *testing.T) {
	if inviteAutofollowUsesRequest(models.Account{}, models.Account{}) {
		t.Fatal("unlocked target should use direct follow")
	}
	if !inviteAutofollowUsesRequest(models.Account{}, models.Account{Locked: true}) {
		t.Fatal("locked target should use follow request")
	}
	if !inviteAutofollowUsesRequest(models.Account{SilencedAt: sql.NullTime{Time: time.Now(), Valid: true}}, models.Account{}) {
		t.Fatal("silenced source should use follow request")
	}
}

func TestRoleIDsWithPermissionFromRolesMatchesComputedRolePermissions(t *testing.T) {
	roles := []models.UserRole{
		{ID: -99, Permissions: rolePermissionInviteUsers},
		{ID: 1, Permissions: 0},
		{ID: 2, Permissions: rolePermissionManageUsers},
		{ID: 3, Permissions: rolePermissionAdministrator},
	}
	got := roleIDsWithPermissionFromRoles(roles, rolePermissionManageUsers)
	if strings.Join(int64Strings(got), ",") != "2,3" {
		t.Fatalf("role ids = %#v", got)
	}

	roles[0].Permissions = rolePermissionManageUsers
	got = roleIDsWithPermissionFromRoles(roles, rolePermissionManageUsers)
	if strings.Join(int64Strings(got), ",") != "-99,1,2,3" {
		t.Fatalf("role ids with everyone permission = %#v", got)
	}
}

func int64Strings(values []int64) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strconv.FormatInt(value, 10))
	}
	return out
}

func TestSingleUserModeDisablesRegistrationEvenWithInvite(t *testing.T) {
	s := &Server{cfg: config.Config{SingleUserMode: true}}
	invite := &models.Invite{ID: 1}
	if s.registrationsEnabledForInvite(invite) {
		t.Fatal("API registrations should be disabled in single user mode")
	}
	if s.webRegistrationsAllowedForInvite(invite) {
		t.Fatal("web registrations should be disabled in single user mode")
	}
	if s.registrationsEnabled() {
		t.Fatal("registrationsEnabled should be false in single user mode")
	}
}

func TestPublicInviteInvalidCodeRedirectsHome(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/invite/bad", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestDeviseRegistrationCancelAndDestroyResponses(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", WebDomain: "example.com", Scheme: "https"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/cancel", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/auth/sign_up" {
		t.Fatalf("GET /auth/cancel status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}

	req = httptest.NewRequest(http.MethodDelete, "/auth", nil)
	rec = httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE /auth status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestRegistrationHTMLIncludesInviteCode(t *testing.T) {
	html := registrationHTML("example.com", "bad", "abc123")
	if !strings.Contains(html, `name="user[invite_code]" value="abc123"`) || !strings.Contains(html, "bad") || !strings.Contains(html, "@example.com") {
		t.Fatalf("unexpected registration html: %s", html)
	}
}

func TestRegistrationHTMLMatchesRailsSignupStructure(t *testing.T) {
	html := registrationHTMLWithTurnstile("example.com", "bad", "", true, "site-key", "en", true, "accept-token")
	for _, want := range []string{
		`class="simple_form"`,
		`class="progress-tracker"`,
		`Your details`,
		`Let&#39;s get you set up on example.com.`,
		`name="user[account_attributes][username]"`,
		`name="user[confirm_password]"`,
		`name="user[website]"`,
		`name="user[invite_request_attributes][text]"`,
		`maxlength="420" required`,
		`name="accept" value="accept-token"`,
		`href="/privacy-policy"`,
		`class="turnstile"`,
		`data-sitekey="site-key"`,
		`class="form-footer"`,
		`href="/auth/sign_in"`,
		`href="/auth/confirmation/new"`,
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("registration html missing %q: %s", want, html)
		}
	}
}

func TestRegistrationInviteRequestTextCanBeOptional(t *testing.T) {
	html := registrationHTMLWithTurnstile("example.com", "", "", false, "", "en", true, "", registrationInviteContext{}, 0, false, false, false)
	if !strings.Contains(html, `name="user[invite_request_attributes][text]" maxlength="420"`) {
		t.Fatalf("registration HTML missing invite request length limit: %s", html)
	}
	if strings.Contains(html, `name="user[invite_request_attributes][text]" maxlength="420" required`) {
		t.Fatalf("registration HTML unexpectedly requires invite request text: %s", html)
	}
}

func TestRegistrationHTMLIncludesAgeVerificationWithoutPersistingBirthDate(t *testing.T) {
	html := registrationHTMLWithTurnstile("example.com", "", "", false, "", "en", false, "", registrationInviteContext{}, 16, true)
	for _, want := range []string{`name="user[date_of_birth]"`, `autocomplete="bday"`, `at least 16`, `We won&#39;t store this`} {
		if !strings.Contains(html, want) {
			t.Fatalf("registration HTML missing %q: %s", want, html)
		}
	}
}

func TestRegistrationHTMLLinksCurrentTermsOfService(t *testing.T) {
	html := registrationHTMLWithTurnstile("example.com", "", "", false, "", "en", false, "", registrationInviteContext{}, 0, false, true)
	for _, want := range []string{`href="/terms-of-service"`, `href="/privacy-policy"`, `terms of service`} {
		if !strings.Contains(html, want) {
			t.Fatalf("registration HTML missing %q: %s", want, html)
		}
	}
}

func TestRegistrationHTMLIncludesAutofollowInviteCard(t *testing.T) {
	inviter := &models.Account{ID: 10, Username: "alice", DisplayName: "Alice Example"}
	html := registrationHTMLWithTurnstile("example.com", "", "abc123", false, "", "en", false, "accept-token", registrationInviteContext{
		Invited:    true,
		Autofollow: true,
		Account:    inviter,
	})
	for _, want := range []string{
		`class="fields-group invited-by"`,
		`You were invited by:`,
		`class="account-card"`,
		`href="/@alice"`,
		`Alice Example`,
		`@alice`,
		`name="user[invite_code]" value="abc123"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("registration invited html missing %q: %s", want, html)
		}
	}
}

func TestRegistrationRulesAcceptedRequiresMatchingCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/auth/sign_up?accept=token", nil)
	req.AddCookie(&http.Cookie{Name: registrationRulesAcceptCookieName, Value: "token"})
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if !registrationRulesAccepted(c) {
		t.Fatal("matching rules accept token was not accepted")
	}

	req = httptest.NewRequest(http.MethodGet, "/auth/sign_up?accept=bad", nil)
	req.AddCookie(&http.Cookie{Name: registrationRulesAcceptCookieName, Value: "token"})
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, echo.New())
	if registrationRulesAccepted(c) {
		t.Fatal("mismatched rules accept token was accepted")
	}
}

func TestRegistrationRulesHTMLIncludesRailsLikeInviteAcceptPath(t *testing.T) {
	html := registrationRulesHTML([]models.Rule{{ID: 1, Text: "Be kind"}, {ID: 2, Text: "No spam"}}, "abc/123", "accept-token", true, false, "example.com", "en")
	for _, want := range []string{
		"You&#39;ve been invited.",
		"example.com",
		`class="rules-list"`,
		"Be kind",
		"No spam",
		`href="/invite/abc%2F123?accept=accept-token"`,
		`>Accept<`,
		`>Back<`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rules html missing %q: %s", want, html)
		}
	}
}

func TestRegistrationRulesHTMLIncludesAutofollowInviteCard(t *testing.T) {
	inviter := &models.Account{ID: 10, Username: "alice", DisplayName: "Alice Example"}
	html := registrationRulesHTMLForInvite([]models.Rule{{ID: 1, Text: "Be kind"}}, "abc123", "accept-token", false, "example.com", registrationInviteContext{
		Invited:    true,
		Autofollow: true,
		Account:    inviter,
	}, "en")
	for _, want := range []string{
		`You&#39;ve been invited.`,
		`class="lead invited-by"`,
		`You can join example.com thanks to the invitation you have received from:`,
		`class="account-card compact"`,
		`href="/@alice"`,
		`Alice Example`,
		`@alice`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("registration rules invited html missing %q: %s", want, html)
		}
	}
}

func TestRegistrationRulesHTMLUsesLocale(t *testing.T) {
	html := registrationRulesHTML([]models.Rule{{ID: 1, Text: "Be kind"}}, "", "accept-token", false, false, "example.com", "ja")
	if !strings.Contains(html, `<html lang="ja">`) || !strings.Contains(html, `いくつかのルール`) {
		t.Fatalf("rules html did not use locale: %s", html)
	}
}

func TestRegistrationRulesHTMLIncludesReviewStepForApprovalMode(t *testing.T) {
	html := registrationRulesHTML([]models.Rule{{ID: 1, Text: "Be kind"}}, "", "accept-token", false, true, "example.com", "en")
	if !strings.Contains(html, `Our review`) {
		t.Fatalf("rules html did not include review progress step: %s", html)
	}
}

func TestRegistrationHTMLIncludesTurnstileWidgetWhenEnabled(t *testing.T) {
	html := registrationHTMLWithTurnstile("example.com", "", "", true, "site-key", "en")
	if !strings.Contains(html, "https://challenges.cloudflare.com/turnstile/v0/api.js") || !strings.Contains(html, `data-sitekey="site-key"`) {
		t.Fatalf("turnstile widget missing: %s", html)
	}
}

func TestCheckCloudflareTurnstileVerifiesToken(t *testing.T) {
	oldURL := turnstileVerifyURL
	oldClient := turnstileHTTPClient
	turnstileVerifyURL = "https://turnstile.example.test/siteverify"
	turnstileHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s", req.Method)
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if req.Form.Get("secret") != "secret" || req.Form.Get("response") != "good-token" {
			t.Fatalf("form = %#v", req.Form)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"success":true}`)),
			Header:     make(http.Header),
		}, nil
	})}
	defer func() { turnstileVerifyURL = oldURL }()
	defer func() { turnstileHTTPClient = oldClient }()

	req := httptest.NewRequest(http.MethodPost, "/auth", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{cfg: config.Config{CloudflareTurnstileEnabled: true, CloudflareTurnstileSecretKey: "secret"}}
	if err := s.checkCloudflareTurnstile(c, "good-token"); err != nil {
		t.Fatalf("checkCloudflareTurnstile: %v", err)
	}
}

func TestCheckCloudflareTurnstileRejectsOversizedResponse(t *testing.T) {
	oldURL := turnstileVerifyURL
	oldClient := turnstileHTTPClient
	turnstileVerifyURL = "https://turnstile.example.test/siteverify"
	defer func() { turnstileVerifyURL = oldURL }()
	defer func() { turnstileHTTPClient = oldClient }()

	req := httptest.NewRequest(http.MethodPost, "/auth", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{cfg: config.Config{CloudflareTurnstileEnabled: true, CloudflareTurnstileSecretKey: "secret"}}

	turnstileHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(`{"success":true}`)),
			Header:        make(http.Header),
			ContentLength: maxTurnstileResponseBodySize + 1,
			Request:       req,
		}, nil
	})}
	if err := s.checkCloudflareTurnstile(c, "good-token"); err == nil {
		t.Fatal("expected advertised oversized Turnstile response to fail")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnprocessableEntity {
		t.Fatalf("advertised oversized error = %#v", err)
	}

	turnstileHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", maxTurnstileResponseBodySize+1))),
			Header:        make(http.Header),
			ContentLength: -1,
			Request:       req,
		}, nil
	})}
	if err := s.checkCloudflareTurnstile(c, "good-token"); err == nil {
		t.Fatal("expected streamed oversized Turnstile response to fail")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnprocessableEntity {
		t.Fatalf("streamed oversized error = %#v", err)
	}
}

func TestCheckCloudflareTurnstileRejectsMissingToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{cfg: config.Config{CloudflareTurnstileEnabled: true, CloudflareTurnstileSecretKey: "secret"}}

	err := s.checkCloudflareTurnstile(c, "")
	apiErr, ok := err.(apiHTTPError)
	if !ok || apiErr.status != http.StatusUnprocessableEntity {
		t.Fatalf("error = %#v", err)
	}
}

func TestCheckEmailConfirmationRequiresAuth(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/emails/check_confirmation", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.checkEmailConfirmation(c); err == nil {
		t.Fatal("expected confirmation check to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestEmailConfirmationFromRequestAcceptsJSONAndForm(t *testing.T) {
	jsonReq := httptest.NewRequest(http.MethodPost, "/api/v1/emails/confirmations", strings.NewReader(`{"email":" New@Example.TEST "}`))
	jsonReq.Header.Set("Content-Type", "application/json")
	jsonEmail, hasJSONEmail, err := emailConfirmationFromRequest(echo.NewContext(jsonReq, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if !hasJSONEmail || jsonEmail != "new@example.test" {
		t.Fatalf("json email = %q has=%v", jsonEmail, hasJSONEmail)
	}

	formReq := httptest.NewRequest(http.MethodPost, "/api/v1/emails/confirmations", strings.NewReader("email=Other%40Example.TEST"))
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	formEmail, hasFormEmail, err := emailConfirmationFromRequest(echo.NewContext(formReq, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if !hasFormEmail || formEmail != "other@example.test" {
		t.Fatalf("form email = %q has=%v", formEmail, hasFormEmail)
	}
}

func TestEmailConfirmationFromRequestTracksRailsEmailKeyPresence(t *testing.T) {
	jsonReq := httptest.NewRequest(http.MethodPost, "/api/v1/emails/confirmations", strings.NewReader(`{"email":null}`))
	jsonReq.Header.Set("Content-Type", "application/json")
	jsonEmail, hasJSONEmail, err := emailConfirmationFromRequest(echo.NewContext(jsonReq, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if !hasJSONEmail || jsonEmail != "" {
		t.Fatalf("json email = %q has=%v", jsonEmail, hasJSONEmail)
	}

	formReq := httptest.NewRequest(http.MethodPost, "/api/v1/emails/confirmations", strings.NewReader("email="))
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	formEmail, hasFormEmail, err := emailConfirmationFromRequest(echo.NewContext(formReq, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if !hasFormEmail || formEmail != "" {
		t.Fatalf("form email = %q has=%v", formEmail, hasFormEmail)
	}

	emptyJSONReq := httptest.NewRequest(http.MethodPost, "/api/v1/emails/confirmations", strings.NewReader(`{}`))
	emptyJSONReq.Header.Set("Content-Type", "application/json")
	_, hasEmail, err := emailConfirmationFromRequest(echo.NewContext(emptyJSONReq, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if hasEmail {
		t.Fatal("missing JSON email key should not be treated as present")
	}
}

func TestGenerateAccountKeyPairReturnsPEMKeys(t *testing.T) {
	privateKey, publicKey, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(privateKey, "BEGIN RSA PRIVATE KEY") || !strings.Contains(publicKey, "BEGIN PUBLIC KEY") {
		t.Fatalf("unexpected key pair:\n%s\n%s", privateKey, publicKey)
	}
}
