//go:build integration

package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestTermsOfServiceAndRuleTranslationsAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 5,
		DatabaseMaxIdleConns: 2,
		LocalDomain:          "example.test",
		SecretKeyBase:        "integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}

	ctx := context.Background()
	server := &Server{cfg: cfg, db: database}
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	actorAccountID := createTermsTestAccount(t, database, "admin", sql.NullTime{}, now)
	pastEffective := now.AddDate(0, 0, -1)
	if _, err := server.saveAdminTermsOfServiceDraft(ctx, actorAccountID, 0, adminTermsOfServiceForm{
		Text:          "Past draft",
		EffectiveDate: sql.NullTime{Time: pastEffective, Valid: true},
	}, false, now); !errors.Is(err, errAdminTermsEffectiveDateTooSoon) {
		t.Fatalf("past draft effective date error = %v", err)
	}
	effective := now.AddDate(0, 0, 10)
	draftForm := adminTermsOfServiceForm{
		Text:              "# Terms for %{domain}",
		EffectiveDate:     sql.NullTime{Time: effective, Valid: true},
		EffectiveDateText: effective.Format("2006-01-02"),
	}
	draft, err := server.saveAdminTermsOfServiceDraft(ctx, actorAccountID, 0, draftForm, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if draft.ID == 0 || draft.PublishedAt.Valid {
		t.Fatalf("saved draft = %#v", draft)
	}
	publishForm := draftForm
	publishForm.Changelog = "New terms apply"
	published, err := server.saveAdminTermsOfServiceDraft(ctx, actorAccountID, draft.ID, publishForm, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if published.ID != draft.ID || !published.PublishedAt.Valid || !published.EffectiveDate.Valid {
		t.Fatalf("published terms = %#v", published)
	}
	var auditCount int64
	if err := database.Model(&models.AdminActionLog{}).
		Where("action = ? AND target_type = ? AND target_id = ?", "publish", "TermsOfService", published.ID).
		Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("publish audit logs = %d", auditCount)
	}
	if _, err := server.saveAdminTermsOfServiceDraft(ctx, actorAccountID, 0, publishForm, true, now); !errors.Is(err, errAdminTermsEffectiveDateTaken) {
		t.Fatalf("duplicate effective date error = %v", err)
	}
	newDraft, err := server.adminTermsOfServiceDraft(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if newDraft.Text != "" {
		t.Fatalf("future terms were copied into a new draft: %#v", newDraft)
	}
	generated, err := server.createGeneratedTermsOfServiceDraft(ctx, adminTermsOfServiceGeneratorForm{
		AdminEmail:         "legal@example.test",
		ArbitrationAddress: "1 Example Street",
		ArbitrationWebsite: "N/A",
		ChoiceOfLaw:        "Example State",
		DMCAAddress:        "2 Example Street",
		DMCAEmail:          "dmca@example.test",
		Domain:             "example.test",
		Jurisdiction:       "Example Country",
		MinimumAge:         "16",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if generated.ID == 0 || generated.PublishedAt.Valid || generated.EffectiveDate.Valid || !strings.Contains(generated.Text, "located at example.test") || strings.Contains(generated.Text, "%{") {
		t.Fatalf("generated terms draft = %#v", generated)
	}
	var persistedGenerated models.TermsOfService
	if err := database.First(&persistedGenerated, generated.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persistedGenerated.Text != generated.Text || persistedGenerated.Changelog != "" {
		t.Fatalf("persisted generated terms draft = %#v", persistedGenerated)
	}

	cutoff := termsOfServiceNotificationCutoff(published)
	activeAccount := createTermsTestAccount(t, database, "active", sql.NullTime{}, now)
	inactiveAccount := createTermsTestAccount(t, database, "inactive", sql.NullTime{}, now)
	suspendedAccount := createTermsTestAccount(t, database, "suspended", sql.NullTime{Time: now, Valid: true}, now)
	newAccount := createTermsTestAccount(t, database, "new", sql.NullTime{}, now)
	unconfirmedAccount := createTermsTestAccount(t, database, "unconfirmed", sql.NullTime{}, now)
	activeUser := createTermsTestUser(t, database, activeAccount, "active@example.test", now.AddDate(-2, 0, 0), sql.NullTime{Time: now.AddDate(-2, 0, 0), Valid: true}, sql.NullTime{Time: cutoff, Valid: true}, now)
	inactiveUser := createTermsTestUser(t, database, inactiveAccount, "inactive@example.test", now.AddDate(-2, 0, 0), sql.NullTime{Time: now.AddDate(-2, 0, 0), Valid: true}, sql.NullTime{Time: cutoff.Add(-time.Second), Valid: true}, now)
	suspendedUser := createTermsTestUser(t, database, suspendedAccount, "suspended@example.test", now.AddDate(-2, 0, 0), sql.NullTime{Time: now.AddDate(-2, 0, 0), Valid: true}, sql.NullTime{Time: now, Valid: true}, now)
	newUser := createTermsTestUser(t, database, newAccount, "new@example.test", now.Add(time.Second), sql.NullTime{Time: now, Valid: true}, sql.NullTime{Time: now, Valid: true}, now)
	unconfirmedUser := createTermsTestUser(t, database, unconfirmedAccount, "unconfirmed@example.test", now.AddDate(-2, 0, 0), sql.NullTime{}, sql.NullTime{}, now)

	var notificationIDs []int64
	if err := termsOfServiceNotificationUsersQuery(database, published).Order("users.id ASC").Pluck("users.id", &notificationIDs).Error; err != nil {
		t.Fatal(err)
	}
	if len(notificationIDs) != 1 || notificationIDs[0] != activeUser {
		t.Fatalf("notification user ids = %v, want [%d]", notificationIDs, activeUser)
	}
	updated, err := markTermsOfServiceInterstitialUsers(ctx, database, published)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("interstitial users updated = %d, want 2", updated)
	}
	var users []models.User
	if err := database.Where("id IN ?", []int64{activeUser, inactiveUser, suspendedUser, newUser, unconfirmedUser}).Find(&users).Error; err != nil {
		t.Fatal(err)
	}
	interstitial := make(map[int64]bool, len(users))
	for _, user := range users {
		interstitial[user.ID] = user.RequireTOSInterstitial
	}
	if interstitial[activeUser] || !interstitial[inactiveUser] || !interstitial[suspendedUser] || interstitial[newUser] || interstitial[unconfirmedUser] {
		t.Fatalf("interstitial flags = %#v", interstitial)
	}

	ruleForm := adminRuleForm{
		Text: "Be kind",
		Hint: "Respect others",
		Translations: []adminRuleTranslationForm{
			{Language: "ja", Text: "親切にしてください", Hint: "互いを尊重してください"},
		},
	}
	if err := server.insertAdminRule(ruleForm); err != nil {
		t.Fatal(err)
	}
	rules, err := server.adminRuleModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || len(rules[0].Translations) != 1 {
		t.Fatalf("inserted rules = %#v", rules)
	}
	text, hint := localizedRuleContent(rules[0], "ja-JP")
	if text != "親切にしてください" || hint != "互いを尊重してください" {
		t.Fatalf("localized rule = %q / %q", text, hint)
	}
	translationID := rules[0].Translations[0].ID
	updateForm := adminRuleForm{
		Text: "Be excellent",
		Hint: "Respect everyone",
		Translations: []adminRuleTranslationForm{
			{ID: translationID, Language: "ja", Text: "すばらしくあれ", Hint: "全員を尊重してください"},
			{Language: "zh-CN", Text: "友善待人", Hint: "尊重所有人"},
		},
	}
	if err := server.updateAdminRuleModel(rules[0].ID, updateForm); err != nil {
		t.Fatal(err)
	}
	updatedRule, err := server.findAdminRule(strconv.FormatInt(rules[0].ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(updatedRule.Translations) != 2 {
		t.Fatalf("updated translations = %#v", updatedRule.Translations)
	}
	updateForm.Translations = []adminRuleTranslationForm{{ID: translationID, Destroy: true}}
	if err := server.updateAdminRuleModel(rules[0].ID, updateForm); err != nil {
		t.Fatal(err)
	}
	updatedRule, err = server.findAdminRule(strconv.FormatInt(rules[0].ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(updatedRule.Translations) != 1 || updatedRule.Translations[0].Language != "zh-CN" {
		t.Fatalf("translations after delete = %#v", updatedRule.Translations)
	}
	if err := server.insertAdminRule(adminRuleForm{Text: "Second rule", Priority: 1, PriorityPresent: true}); err != nil {
		t.Fatal(err)
	}
	rules, err = server.adminRuleModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("rules before reorder = %#v", rules)
	}
	secondRuleID := rules[1].ID
	if err := server.moveAdminRuleModel(ctx, secondRuleID, -1); err != nil {
		t.Fatal(err)
	}
	rules, err = server.adminRuleModels()
	if err != nil {
		t.Fatal(err)
	}
	if rules[0].ID != secondRuleID || rules[0].Priority != 0 || rules[1].Priority != 1 {
		t.Fatalf("rules after reorder = %#v", rules)
	}
}

func createTermsTestAccount(t *testing.T, database *gorm.DB, username string, suspendedAt sql.NullTime, now time.Time) int64 {
	t.Helper()
	var id int64
	if err := database.Raw(`INSERT INTO accounts (username, created_at, updated_at, suspended_at) VALUES (?, ?, ?, ?) RETURNING id`, username, now, now, nullableTimeValue(suspendedAt)).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func createTermsTestUser(t *testing.T, database *gorm.DB, accountID int64, email string, createdAt time.Time, confirmedAt, currentSignInAt sql.NullTime, now time.Time) int64 {
	t.Helper()
	var id int64
	if err := database.Raw(`INSERT INTO users (email, account_id, created_at, updated_at, confirmed_at, current_sign_in_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, email, accountID, createdAt, now, nullableTimeValue(confirmedAt), nullableTimeValue(currentSignInAt)).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
