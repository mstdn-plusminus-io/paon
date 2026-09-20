package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestNormalizeApplicationScopes(t *testing.T) {
	got := normalizeApplicationScopes([]string{"read write", "read", "follow"}, "")
	if got != "read write follow" {
		t.Fatalf("scopes = %q", got)
	}
	if got := normalizeApplicationScopes(nil, "read push"); got != "read push" {
		t.Fatalf("fallback scopes = %q", got)
	}
}

func TestOAuthConfiguredScopesMatchDeclaredOrder(t *testing.T) {
	if got, want := len(oauthConfiguredScopes), len(oauthConfiguredScopeOrder); got != want {
		t.Fatalf("OAuth configured scope map size = %d, order size = %d", got, want)
	}
	for _, scope := range oauthConfiguredScopeOrder {
		if _, ok := oauthConfiguredScopes[scope]; !ok {
			t.Fatalf("OAuth configured scopes missing declared scope %q", scope)
		}
	}
	if _, ok := oauthConfiguredScopes["read"]; !ok {
		t.Fatal("default OAuth scopes must include read")
	}
}

func TestApplicationFromRequestParsesRailsFields(t *testing.T) {
	form := url.Values{}
	form.Set("doorkeeper_application[name]", "Test App")
	form.Set("doorkeeper_application[website]", "https://example.test")
	form.Set("doorkeeper_application[redirect_uri]", "urn:ietf:wg:oauth:2.0:oob")
	form.Add("doorkeeper_application[scopes][]", "")
	form.Add("doorkeeper_application[scopes][]", "read")
	form.Add("doorkeeper_application[scopes][]", "write")
	form.Add("doorkeeper_application[scopes][]", "follow")
	req := httptest.NewRequest(http.MethodPost, "/settings/applications", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	app, err := applicationFromRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "Test App" || app.Website != "https://example.test" || app.RedirectURI != nativeOAuthRedirectURI || app.Scopes != "read write follow" {
		t.Fatalf("app = %#v", app)
	}
}

func TestApplicationFromRequestStillAcceptsLegacyFlatScopeField(t *testing.T) {
	form := url.Values{}
	form.Set("doorkeeper_application[name]", "Test App")
	form.Set("doorkeeper_application[redirect_uri]", "urn:ietf:wg:oauth:2.0:oob")
	form.Add("doorkeeper_application[scopes]", "read")
	form.Add("doorkeeper_application[scopes]", "write follow")
	req := httptest.NewRequest(http.MethodPost, "/settings/applications", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	app, err := applicationFromRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	if app.Scopes != "read write follow" {
		t.Fatalf("scopes = %q", app.Scopes)
	}
}

func TestApplicationRequestsKeepRawDoorkeeperFieldsLikeRails(t *testing.T) {
	form := url.Values{}
	form.Set("doorkeeper_application[name]", " Test App ")
	form.Set("doorkeeper_application[website]", "https://example.test/app?label=%20raw%20")
	form.Set("doorkeeper_application[redirect_uri]", " urn:ietf:wg:oauth:2.0:oob ")
	req := httptest.NewRequest(http.MethodPost, "/settings/applications", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	app, err := applicationFromRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != " Test App " || app.Website != "https://example.test/app?label=%20raw%20" || app.RedirectURI != " urn:ietf:wg:oauth:2.0:oob " {
		t.Fatalf("settings application must keep raw Rails-permitted fields, got %#v", app)
	}

	apiReq := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(`{
		"client_name": " API App ",
		"redirect_uris": " urn:ietf:wg:oauth:2.0:oob ",
		"scopes": "read write",
		"website": "https://api.example.test/app?label=%20raw%20"
	}`))
	apiReq.Header.Set("Content-Type", "application/json")
	apiCtx := echo.NewContext(apiReq, httptest.NewRecorder(), echo.New())

	apiApp, err := appRegistrationFromRequest(apiCtx)
	if err != nil {
		t.Fatal(err)
	}
	if apiApp.Name != " API App " || apiApp.Website != "https://api.example.test/app?label=%20raw%20" || apiApp.RedirectURI != " urn:ietf:wg:oauth:2.0:oob " {
		t.Fatalf("API application registration must keep raw Rails-permitted fields, got %#v", apiApp)
	}
}

func TestSettingsApplicationRedirectErrorsUseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("applications.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "createSettingsApplication", `settingsDatabaseUnavailableMessage(locale)`) {
		t.Fatal("createSettingsApplication must use localized database-unavailable flash")
	}
	if functionBodyContains(t, src, "createSettingsApplication", `QueryEscape("DATABASE_URL is not set")`) {
		t.Fatal("createSettingsApplication must not redirect with fixed Go-only database flash")
	}
}

func TestAppRegistrationFromRequestParsesJSONRailsFields(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(`{
		"client_name": "JSON App",
		"redirect_uris": "urn:ietf:wg:oauth:2.0:oob",
		"scopes": "read write push",
		"website": "https://app.example.test"
	}`))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	app, err := appRegistrationFromRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "JSON App" || app.RedirectURI != nativeOAuthRedirectURI || app.Scopes != "read write push" || app.Website != "https://app.example.test" {
		t.Fatalf("app = %#v", app)
	}
}

func TestAppRegistrationFromRequestRequiresRailsApplicationFields(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want error
	}{
		{name: "missing name", body: "redirect_uris=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob", want: errApplicationNameRequired},
		{name: "missing redirect uri", body: "client_name=No+Redirect", want: errApplicationRedirectURIRequired},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

		if _, err := appRegistrationFromRequest(c); err != tt.want {
			t.Fatalf("%s: error = %#v, want %#v", tt.name, err, tt.want)
		}
	}
}

func TestAppRegistrationFromRequestRejectsUnsupportedScopesLikeDoorkeeper(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader("client_name=Scoped&redirect_uris=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob&scopes=read+hoge"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if _, err := appRegistrationFromRequest(c); err != errApplicationScopesInvalid {
		t.Fatalf("error = %#v, want %#v", err, errApplicationScopesInvalid)
	}
}

func TestAppRegistrationFromRequestRejectsTooLongNameAndWebsite(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want error
	}{
		{
			name: "too long name",
			body: "client_name=" + strings.Repeat("hoge", 20) + "&redirect_uris=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob",
			want: errApplicationNameTooLong,
		},
		{
			name: "too long website",
			body: "client_name=Website&redirect_uris=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob&website=" + url.QueryEscape("https://foo.bar/"+strings.Repeat("hoge", 2000)),
			want: errApplicationWebsiteTooLong,
		},
		{
			name: "invalid website",
			body: "client_name=Website&redirect_uris=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob&website=" + url.QueryEscape("ftp://foo.bar/"),
			want: errApplicationWebsiteInvalid,
		},
		{
			name: "raw website with surrounding whitespace",
			body: "client_name=Website&redirect_uris=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob&website=" + url.QueryEscape(" https://foo.bar/ "),
			want: errApplicationWebsiteInvalid,
		},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

		if _, err := appRegistrationFromRequest(c); err != tt.want {
			t.Fatalf("%s: error = %#v, want %#v", tt.name, err, tt.want)
		}
	}
}

func TestAppRegistrationFromRequestRejectsRailsForbiddenRedirectURIs(t *testing.T) {
	for _, redirectURI := range []string{
		"javascript:alert(1)",
		"data:text/html,hello",
		"vbscript:msgbox(1)",
		"https://ok.example/callback javascript:alert(1)",
	} {
		body := "client_name=Bad&redirect_uris=" + url.QueryEscape(redirectURI)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

		if _, err := appRegistrationFromRequest(c); err != errApplicationRedirectURIInvalid {
			t.Fatalf("%s: error = %#v, want %#v", redirectURI, err, errApplicationRedirectURIInvalid)
		}
	}
}

func TestAppRegistrationFromRequestRejectsTooLongRedirectURI(t *testing.T) {
	body := "client_name=Long&redirect_uris=" + url.QueryEscape("https://foo.bar/"+strings.Repeat("hoge", 2000))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if _, err := appRegistrationFromRequest(c); err != errApplicationRedirectURITooLong {
		t.Fatalf("error = %#v, want %#v", err, errApplicationRedirectURITooLong)
	}
}

func TestAppRegistrationFromRequestDeduplicatesConfiguredScopes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader("client_name=Dup&redirect_uris=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob&scopes="+url.QueryEscape(strings.Repeat("read ", 40))))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	app, err := appRegistrationFromRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	if app.Scopes != "read" {
		t.Fatalf("scopes = %q", app.Scopes)
	}
}

func TestApplicationFromRequestRejectsRailsForbiddenRedirectURIs(t *testing.T) {
	form := url.Values{}
	form.Set("doorkeeper_application[name]", "Bad")
	form.Set("doorkeeper_application[redirect_uri]", "javascript:alert(1)")
	req := httptest.NewRequest(http.MethodPost, "/settings/applications", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if _, err := applicationFromRequest(c); err != errApplicationRedirectURIInvalid {
		t.Fatalf("error = %#v, want %#v", err, errApplicationRedirectURIInvalid)
	}
}

func TestApplicationFromRequestRejectsUnsupportedScopesLikeDoorkeeper(t *testing.T) {
	form := url.Values{}
	form.Set("doorkeeper_application[name]", "Bad")
	form.Set("doorkeeper_application[redirect_uri]", "urn:ietf:wg:oauth:2.0:oob")
	form.Add("doorkeeper_application[scopes]", "read")
	form.Add("doorkeeper_application[scopes]", "hoge")
	req := httptest.NewRequest(http.MethodPost, "/settings/applications", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	if _, err := applicationFromRequest(c); err != errApplicationScopesInvalid {
		t.Fatalf("error = %#v, want %#v", err, errApplicationScopesInvalid)
	}
}

func TestAppRegistrationFromRequestDefaultsScopesLikeDoorkeeper(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps", strings.NewReader("client_name=Minimal&redirect_uris=urn%3Aietf%3Awg%3Aoauth%3A2.0%3Aoob"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	app, err := appRegistrationFromRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "Minimal" || app.RedirectURI != nativeOAuthRedirectURI || app.Scopes != "read" {
		t.Fatalf("app = %#v", app)
	}
}

func TestSettingsApplicationsRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/applications", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.settingsApplicationsPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/applications")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestApplicationsIndexHTMLRendersRailsPaginationLinks(t *testing.T) {
	apps := make([]oauthApplication, adminRailsDefaultPageSize)
	for i := range apps {
		apps[i] = oauthApplication{ID: int64(i + 1), Name: "App", Scopes: "read"}
	}
	html := applicationsIndexHTML(apps, "", "", "2")
	for _, want := range []string{
		`href="/settings/applications?page=1"`,
		`href="/settings/applications?page=3"`,
		"Previous",
		"Next",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("applications html missing pagination %q: %s", want, html)
		}
	}
}

func TestApplicationsIndexHTMLRendersRailsTableAndEmptyState(t *testing.T) {
	html := applicationsIndexHTML([]oauthApplication{{ID: 7, Name: "Test App", Scopes: "read write"}}, "", "", "1")
	for _, want := range []string{
		`class="button"`,
		`class="table-wrapper"`,
		`class="table"`,
		`href="/settings/applications/7"`,
		`class="table-action-link"`,
		`data-confirm=`,
		`read write`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("applications index html missing %q: %s", want, html)
		}
	}
	empty := applicationsIndexHTML(nil, "", "", "1")
	if !strings.Contains(empty, `class="muted-hint center-text"`) || strings.Contains(empty, `class="table-wrapper"`) {
		t.Fatalf("applications empty state should match Rails without table: %s", empty)
	}
}

func TestApplicationFormAndShowHTMLRenderRailsClasses(t *testing.T) {
	app := oauthApplication{ID: 7, Name: "Test App", UID: "uid", Secret: "secret", RedirectURI: nativeOAuthRedirectURI, Scopes: "read write"}
	form := applicationFormBody("/settings/applications", http.MethodPost, app, "en")
	for _, want := range []string{
		`class="simple_form new_doorkeeper_application"`,
		`id="new_doorkeeper_application"`,
		`novalidate="novalidate"`,
		`class="fields-group"`,
		`class="input with_label string required doorkeeper_application_name"`,
		`<abbr title="required">*</abbr>`,
		`class="label_input__wrapper"`,
		`class="input with_block_label text optional doorkeeper_application_redirect_uri field_with_hint"`,
		`<p class="hint">`,
		`class="field-group"`,
		`class="input with_block_label check_boxes optional doorkeeper_application_scopes"`,
		`<li class="checkbox"><label for="doorkeeper_application_scopes_read"><input class="check_boxes optional" type="checkbox" value="read" name="doorkeeper_application[scopes][]" id="doorkeeper_application_scopes_read" selected="selected" checked="checked"><samp class="scope-danger">read</samp>`,
		`value="read:accounts"`,
		`value="write:statuses"`,
		`value="admin:read:canonical_email_blocks"`,
		`value="admin:write:reports"`,
		`<span class="hint">`,
		`class="actions"`,
		`class="btn"`,
		`Submit`,
	} {
		if !strings.Contains(form, want) {
			t.Fatalf("application form html missing %q: %s", want, form)
		}
	}
	if strings.Contains(form, `value="crypto"`) {
		t.Fatalf("Mastodon 4.3 application form must omit removed crypto scope: %s", form)
	}
	show := applicationShowHTML(app, "token", "", "", "en")
	for _, want := range []string{
		`class="hint"`,
		`class="table-wrapper"`,
		`class="table"`,
		`rowspan="2"`,
		`class="table-action-link"`,
		`fa-refresh`,
		`class="simple_form edit_doorkeeper_application"`,
	} {
		if !strings.Contains(show, want) {
			t.Fatalf("application show html missing %q: %s", want, show)
		}
	}
}

func TestApplicationTokenRegenerationDeletesTokensLikeRailsDestroy(t *testing.T) {
	src, err := os.ReadFile("applications.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`tx.Where("access_token_id IN ?", revokedTokenIDs).Delete(&models.WebPushSubscription{})`,
		`tx.Where("access_token_id IN ?", revokedTokenIDs).Delete(&models.SessionActivation{})`,
		`tx.Delete(&models.OAuthAccessToken{}, revokedTokenIDs)`,
		`s.publishAccessTokenKills(revokedTokenIDs)`,
	} {
		if !functionBodyContains(t, src, "revokeApplicationUserTokens", want) {
			t.Fatalf("revokeApplicationUserTokens missing %q", want)
		}
	}
	if functionBodyContains(t, src, "revokeApplicationUserTokens", `Update("revoked_at"`) {
		t.Fatal("application token regeneration should destroy tokens like Rails, not only revoke them")
	}
}

func TestUserOAuthApplicationsUseRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("applications.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"settingsApplicationsPage", "s.userOAuthApplications(user.ID, c)"},
		{"settingsApplicationsPage", "applicationsIndexHTML(apps, c.QueryParam(\"notice\"), c.QueryParam(\"error\"), adminTrendsPageValue(c), renderArgs...)"},
		{"userOAuthApplications", "Offset(adminRailsPageOffset(c))"},
		{"userOAuthApplications", "Limit(adminRailsDefaultPageSize)"},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}
