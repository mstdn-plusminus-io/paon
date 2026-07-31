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

func TestMigrationTargetAccountResolvesRemoteAccountsLikeRails(t *testing.T) {
	src, err := os.ReadFile("migrations.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.importRemoteAcct(acct)`,
		`s.fetchAndStoreActivityActorForAcct(remoteAcct)`,
		`!accountMatchesImportAcct(account, remoteAcct)`,
		`return nil, errMigrationNotFound`,
	} {
		if !functionBodyContains(t, src, "migrationTargetAccount", want) {
			t.Fatalf("migrationTargetAccount missing %q", want)
		}
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
	for _, want := range []string{
		`migration.Account.Local() && !migration.TargetAccount.Local()`,
		`s.migrateRemoteAccountMigrationFollowers(migration.Account, migration.TargetAccount)`,
	} {
		if !functionBodyContains(t, src, "processLocalAccountMigration", want) {
			t.Fatalf("processLocalAccountMigration missing %q", want)
		}
	}
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
	for _, want := range []string{
		`Where("account_id = ? AND target_account_id = ?", followerAccountID, oldTargetAccountID)`,
		`if target.Local() && bypassLocked && !follower.SilencedAt.Valid`,
		`s.createDirectMigrationFollow(follow, target)`,
		`s.createMigrationFollowRequest(follow, target)`,
		`s.deliverActivityPubMigratedFollow(follow.Account, target, request.ID, string(request.URI), oldTargetAccountID)`,
	} {
		if !functionBodyContains(t, src, "performUnfollowFollowMigration", want) {
			t.Fatalf("performUnfollowFollowMigration missing %q", want)
		}
	}
	body := functionBody(t, src, "performUnfollowFollowMigration")
	deliveryIndex := strings.Index(body, `s.deliverActivityPubMigratedFollow(follow.Account, target, request.ID, string(request.URI), oldTargetAccountID)`)
	if deliveryIndex < 0 {
		t.Fatal("performUnfollowFollowMigration missing migrated Follow delivery")
	}
	if strings.Contains(body[deliveryIndex:], `deleteFollow(tx, follow)`) {
		t.Fatal("remote migrated Follow cleanup must wait for ActivityPub::MigratedFollowDeliveryWorker-equivalent delivery success")
	}
	for _, want := range []string{
		`ShowReblogs:     original.ShowReblogs`,
		`Notify:          original.Notify`,
		`Languages:       original.Languages`,
		`URI:             models.NullSafeString(activityPubGeneratedPayloadURI(s))`,
		`migrateListAccountsForAccountMigration(tx, original.ID, target.ID`,
		`errors.Is(err, gorm.ErrRecordNotFound)`,
	} {
		if !functionBodyContains(t, src, "createMigrationFollowRequest", want) {
			t.Fatalf("createMigrationFollowRequest missing %q", want)
		}
	}
	if !functionBodyContains(t, src, "migrateListAccountsForAccountMigration", `clause.OnConflict{DoNothing: true}`) {
		t.Fatal("migrateListAccountsForAccountMigration must tolerate existing target list memberships")
	}
	if functionBodyContains(t, src, "createMigrationFollowRequest", `err != gorm.ErrRecordNotFound`) {
		t.Fatal("createMigrationFollowRequest must tolerate wrapped gorm.ErrRecordNotFound")
	}
}
