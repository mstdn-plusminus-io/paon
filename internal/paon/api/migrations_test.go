package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestMigrationCooldownUntil(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	until := migrationCooldownUntil([]models.AccountMigration{{CreatedAt: now.Add(-24 * time.Hour)}}, now)
	if !until.Valid || !until.Time.Equal(now.Add(-24*time.Hour).Add(accountMigrationCooldown)) {
		t.Fatalf("until = %#v", until)
	}
	if got := migrationCooldownUntil([]models.AccountMigration{{CreatedAt: now.Add(-accountMigrationCooldown - time.Second)}}, now); got.Valid {
		t.Fatalf("expired cooldown = %#v", got)
	}
}

func TestSettingsMigrationRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/migration", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.settingsMigrationPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/migration")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMigrationTargetAccountValidatesAcctLikeRails(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "web.example", LocalDomain: "social.example"}}
	current := &models.Account{ID: 7, Username: "alice"}
	for _, test := range []struct {
		acct string
		want error
	}{
		// presence fails and ResolveAccountService returns nil for a blank acct.
		{"", errMigrationNotFound},
		// DomainValidator(acct: true) accepts a bare local username.
		{"bob", errMigrationNotFound},
		{"bob@remote example", errMigrationInvalidAcct},
		{"alice", errMigrationMoveToSelf},
	} {
		if _, err := s.migrationTargetAccount(test.acct, current); err != test.want {
			t.Fatalf("migrationTargetAccount(%q) error = %v, want %v", test.acct, err, test.want)
		}
	}
	if got := migrationErrorText("ja", errMigrationInvalidAcct); got != "は無効なドメイン名です" {
		t.Fatalf("invalid acct text = %q", got)
	}
}

func TestSettingsMigrationParamsRequireRailsRootParameter(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/migration", strings.NewReader("acct=%40alice%40remote.test&current_password=pass"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := settingsMigrationParams(c); !errors.Is(err, errSettingsMigrationParamsMissing) {
		t.Fatalf("flat acct should be rejected like Rails params.require(:account_migration), got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/settings/migration", strings.NewReader("account_migration%5Bacct%5D=%40alice%40remote.test&account_migration%5Bcurrent_password%5D=pass&account_migration%5Bcurrent_username%5D=bob"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	params, err := settingsMigrationParams(c)
	if err != nil {
		t.Fatal(err)
	}
	if params.Acct != "alice@remote.test" || params.CurrentPassword != "pass" || params.CurrentUsername != "bob" {
		t.Fatalf("params = %#v", params)
	}
}

func TestSettingsMigrationRedirectParamsRequireRailsRootParameter(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/migration/redirect", strings.NewReader("acct=%40alice%40remote.test&current_password=pass"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := settingsMigrationRedirectParams(c); !errors.Is(err, errSettingsMigrationRedirectParamsMissing) {
		t.Fatalf("flat acct should be rejected like Rails params.require(:form_redirect), got %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/settings/migration/redirect", strings.NewReader("form_redirect%5Bacct%5D=%40alice%40remote.test&form_redirect%5Bcurrent_password%5D=pass&form_redirect%5Bcurrent_username%5D=bob"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	params, err := settingsMigrationRedirectParams(c)
	if err != nil {
		t.Fatal(err)
	}
	if params.Acct != "alice@remote.test" || params.CurrentPassword != "pass" || params.CurrentUsername != "bob" {
		t.Fatalf("params = %#v", params)
	}
}

func TestActivityPubMoveDeliveryUsesRailsRecipientSet(t *testing.T) {
	src, err := os.ReadFile("activitypub_delivery.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.activityPubRemoteFollowerInboxes(accountID)`,
		`s.activityPubRemoteBlockedByInboxes(accountID)`,
		`s.activityPubEnabledRelayInboxes()`,
		`compactActivityPubInboxes(inboxes)`,
	} {
		if !functionBodyContains(t, src, "activityPubMoveRecipientInboxes", want) {
			t.Fatalf("activityPubMoveRecipientInboxes missing %q", want)
		}
	}
	if !functionBodyContains(t, src, "activityPubRemoteBlockedByInboxes", `blocks.target_account_id = ?`) {
		t.Fatal("activityPubRemoteBlockedByInboxes must read accounts that blocked the moving account")
	}
}

func TestRemoteAccountMigrationCreatesMigratedFollowRequests(t *testing.T) {
	src, err := os.ReadFile("migrations.go")
	if err != nil {
		t.Fatal(err)
	}
	// performMoveWorker's local/remote branching is covered by
	// TestAccountMoveWorkerIntegration.
	for _, want := range []string{
		`Where("follows.target_account_id = ? AND (accounts.domain IS NULL OR accounts.domain = '')", source.ID)`,
		`bypassLocked := target.Local()`,
		`s.enqueueUnfollowFollowTask(follow.AccountID, source.ID, target.ID, bypassLocked)`,
		`s.performUnfollowFollowMigration(ctx, follow.AccountID, source.ID, target.ID, bypassLocked)`,
	} {
		if !functionBodyContains(t, src, "migrateRemoteAccountMigrationFollowers", want) {
			t.Fatalf("migrateRemoteAccountMigrationFollowers missing %q", want)
		}
	}
	// performUnfollowFollowMigration's FollowMigrationService behavior is covered
	// by TestAccountMigrationUnfollowFollowIntegration.
	if !functionBodyContains(t, src, "migrateListAccountsForAccountMigration", `clause.OnConflict{DoNothing: true}`) {
		t.Fatal("migrateListAccountsForAccountMigration must tolerate existing target list memberships")
	}
}
