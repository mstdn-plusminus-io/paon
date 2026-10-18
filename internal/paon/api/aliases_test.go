package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestNormalizeAliasAcct(t *testing.T) {
	if got := normalizeAliasAcct(" @alice@example.test "); got != "alice@example.test" {
		t.Fatalf("acct = %q", got)
	}
	if got := normalizeAliasAcct("alice@example.test"); got != "alice@example.test" {
		t.Fatalf("acct without leading at = %q", got)
	}
	if got := normalizeAliasAcct("   "); got != "" {
		t.Fatalf("blank acct = %q", got)
	}
}

func TestAliasesHTMLRendersRows(t *testing.T) {
	html := aliasesHTML([]models.AccountAlias{{ID: 7, Acct: "alice@xn--eckwd4c7c.xn--zckzah", URI: "https://example.test/users/alice"}}, "", "")
	for _, want := range []string{
		"alice@ドメイン.テスト",
		`class="simple_form new_account_alias"`,
		`id="new_account_alias"`,
		`class="input with_block_label string required account_alias_acct field_with_hint"`,
		`<abbr title="required">*</abbr>`,
		`class="btn button"`,
		`class="table-action-link" data-method="delete" href="/settings/aliases/7"`,
		`class="table-wrapper"`,
		`class="table inline-table"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "alice@xn--eckwd4c7c.xn--zckzah") {
		t.Fatalf("html should render Rails pretty_acct unicode domain, got: %s", html)
	}
}

func TestSettingsAliasesRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/aliases", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.settingsAliasesPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/aliases")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAliasErrorTextUsesRailsMigrationLocaleKeys(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{errAliasNotFound, "見つかりませんでした"},
		{errAliasMoveToSelf, "同じアカウントに引っ越すことはできません"},
	} {
		if got := aliasErrorText("ja", test.err); got != test.want {
			t.Fatalf("aliasErrorText(ja, %v) = %q, want %q", test.err, got, test.want)
		}
	}
	if got := aliasErrorText("en", errAliasNotFound); got != "could not be found" {
		t.Fatalf("aliasErrorText(en, not_found) = %q", got)
	}
	// DomainValidator adds :invalid (errors.messages.invalid) since Mastodon 4.3.
	if got := aliasErrorText("en", errAliasInvalidAcct); got != "is invalid" {
		t.Fatalf("aliasErrorText(en, invalid_acct) = %q", got)
	}
}

func TestRailsDomainValidatorAcctCompliant(t *testing.T) {
	for _, acct := range []string{"alice@remote.example", "bob", "alice@xn--eckwd4c7c.xn--zckzah", "alice@例え.テスト", "alice@Remote.Example", "alice@"} {
		if !railsDomainValidatorAcctCompliant(acct) {
			t.Fatalf("%q should pass DomainValidator(acct: true)", acct)
		}
	}
	for _, acct := range []string{"alice@remote example", "alice@remote.example/users", "alice@remote.example?x", "alice@<remote>", "alice@under_score.example", "alice@remote..example", "alice@" + strings.Repeat("a", 64) + ".example"} {
		if railsDomainValidatorAcctCompliant(acct) {
			t.Fatalf("%q should fail DomainValidator(acct: true)", acct)
		}
	}
}

func TestAccountMatchesWebFingerAcctFollowsCheckWebFingerForSplitDomain(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://gts.example.com/.well-known/webfinger?resource=acct%3Aalice%40gts.example.com":
			return textResponse(http.StatusOK, "application/jrd+json", `{"subject":"acct:alice@example.com","links":[{"rel":"self","type":"application/activity+json","href":"https://gts.example.com/users/alice"}]}`), nil
		case "https://example.com/.well-known/webfinger?resource=acct%3Aalice%40example.com":
			return textResponse(http.StatusOK, "application/jrd+json", `{"subject":"acct:alice@example.com","links":[{"rel":"self","type":"application/activity+json","href":"https://gts.example.com/users/alice"}]}`), nil
		case "https://victim.example/.well-known/webfinger?resource=acct%3Abob%40victim.example":
			return textResponse(http.StatusOK, "application/jrd+json", `{"subject":"acct:bob@victim.example","links":[{"rel":"self","type":"application/activity+json","href":"https://victim.example/users/bob"}]}`), nil
		default:
			t.Fatalf("unexpected request %q", r.URL.String())
			return nil, nil
		}
	})}

	split := &models.Account{ID: 1, Username: "alice", Domain: sql.NullString{String: "gts.example.com", Valid: true}, URI: "https://gts.example.com/users/alice"}
	if !accountMatchesWebFingerAcct(split, "alice@example.com") {
		t.Fatal("split-domain actor confirmed by its host WebFinger must match the canonical acct")
	}
	if !accountMatchesWebFingerAcct(split, "alice@gts.example.com") {
		t.Fatal("actor host acct must keep matching")
	}
	victim := &models.Account{ID: 2, Username: "bob", Domain: sql.NullString{String: "victim.example", Valid: true}, URI: "https://victim.example/users/bob"}
	if accountMatchesWebFingerAcct(victim, "alice@example.com") {
		t.Fatal("an actor whose host WebFinger does not confirm the acct must not match")
	}
}

func TestSettingsAliasAcctRequiresRailsRootParameter(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/aliases", strings.NewReader("acct=%40alice%40remote.test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := settingsAliasAcct(c); !errors.Is(err, errSettingsAliasParamsMissing) {
		t.Fatalf("flat acct should be rejected like Rails params.require(:account_alias), got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/settings/aliases", strings.NewReader("account_alias%5Bacct%5D=%40alice%40remote.test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	acct, err := settingsAliasAcct(c)
	if err != nil {
		t.Fatal(err)
	}
	if acct != "alice@remote.test" {
		t.Fatalf("acct = %q", acct)
	}
}

func TestAliasURIRejectsResolvedCurrentAccountLikeRailsValidateTarget(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "web.example", LocalDomain: "social.example"}}
	current := &models.Account{ID: 7, Username: "alice"}

	if !aliasURIIsCurrentAccount(s, current, &models.Account{ID: 7, Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}}, "https://remote.example/users/alice") {
		t.Fatal("same resolved account ID must be rejected as move_to_self")
	}
	if !aliasURIIsCurrentAccount(s, current, &models.Account{ID: 8, Username: "alice"}, "https://web.example/users/alice") {
		t.Fatal("same resolved actor URI must be rejected as move_to_self")
	}
	if aliasURIIsCurrentAccount(s, current, &models.Account{ID: 8, Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}}, "https://remote.example/users/bob") {
		t.Fatal("different resolved account must remain valid")
	}
	sameUsername := &models.Account{ID: 9, Username: "alice", Domain: sql.NullString{String: "misskey.example", Valid: true}, URI: "https://misskey.example/users/9abcdef"}
	if aliasURIIsCurrentAccount(s, current, sameUsername, activityPubAccountTagManagerURI(s, *sameUsername)) {
		t.Fatal("remote account sharing the local username must not be treated as move_to_self")
	}
}

func TestCreateSettingsAliasRejectsDuplicateURILikeRails(t *testing.T) {
	src, err := os.ReadFile("aliases.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`tx.Where("account_id = ? AND uri = ?", account.ID, uri).First(&existing)`,
		`return errAliasAlreadyExists`,
		`!errors.Is(err, gorm.ErrRecordNotFound)`,
		`tx.Create(&alias)`,
	} {
		if !functionBodyContains(t, src, "createSettingsAlias", want) {
			t.Fatalf("createSettingsAlias must reject duplicate alias URIs like Rails; missing %q", want)
		}
	}
	if functionBodyContains(t, src, "createSettingsAlias", `FirstOrCreate`) {
		t.Fatal("createSettingsAlias must not treat duplicate aliases as success")
	}
	if functionBodyContains(t, src, "createSettingsAlias", `err != gorm.ErrRecordNotFound`) {
		t.Fatal("createSettingsAlias must tolerate wrapped gorm.ErrRecordNotFound from the uniqueness probe")
	}
}
