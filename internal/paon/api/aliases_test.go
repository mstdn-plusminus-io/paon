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

func TestAliasURIRequiresResolvedRemoteAccount(t *testing.T) {
	src, err := os.ReadFile("aliases.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.importRemoteAcct(acct)`,
		`s.fetchAndStoreActivityActorForAcct(remoteAcct)`,
		`!accountMatchesImportAcct(account, remoteAcct)`,
		`return "", errAliasNotFound`,
	} {
		if !functionBodyContains(t, src, "aliasURIForAcct", want) {
			t.Fatalf("aliasURIForAcct missing %q", want)
		}
	}
	if functionBodyContains(t, src, "aliasURIForAcct", `"https://"`) {
		t.Fatal("aliasURIForAcct must not synthesize remote actor URLs without WebFinger/ActivityPub resolution")
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
