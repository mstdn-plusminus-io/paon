package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestParseImportCSVWithRailsHeaders(t *testing.T) {
	input := "Account address,Show boosts,Notify on new posts,Languages\n@alice@example.test,true,false,\"en, ja\"\n"
	rows, err := parseImportCSVReader(strings.NewReader(input), "following")
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{
		"acct":         "alice@example.test",
		"show_reblogs": true,
		"notify":       false,
		"languages":    []string{"en", "ja"},
	}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestParseImportCSVFallsBackToDefaultHeaders(t *testing.T) {
	rows, err := parseImportCSVReader(strings.NewReader("cats.example\n"), "domain_blocking")
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"domain": "cats.example"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestParseImportCSVWrongKnownHeaderMatchesCurrentAndLegacyRails(t *testing.T) {
	if _, err := parseImportCSVReader(strings.NewReader("#domain\nexample.test\n"), "following"); err == nil {
		t.Fatal("known first header for the wrong type should be incompatible like Form::Import")
	}
	rows, err := parseLegacyImportCSVReader(strings.NewReader("#domain\nexample.test\n"), "following")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["acct"] != "#domain" || rows[1]["acct"] != "example.test" {
		t.Fatalf("legacy rows = %#v", rows)
	}
}

func TestImportFailureRowMatchesRailsCSVShape(t *testing.T) {
	row := importFailureRow("following", map[string]any{
		"acct":         "alice@example.test",
		"show_reblogs": false,
		"notify":       true,
		"languages":    []any{"en", "ja"},
	})
	want := []string{"alice@example.test", "false", "true", "en, ja"}
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("row = %#v", row)
	}
}

func TestSettingsImportValidationErrorsRenderIndexWithRailsDefaultStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/settings/imports", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	user := &models.User{}
	if err := (&Server{}).renderSettingsImportsError(c, 123, user, "CSV file is required"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("settings import validation status = %d, want Rails render :index default 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CSV file is required") {
		t.Fatalf("settings import validation body missing error text: %s", rec.Body.String())
	}
}

func TestDomainBlockImportDomainsNormalizesDeduplicatesAndKeepsFailureRows(t *testing.T) {
	rowData := func(values map[string]any) models.JSONValue {
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		return models.JSONValue(encoded)
	}
	rows := []models.BulkImportRow{
		{ID: 1, Data: rowData(map[string]any{"domain": "Example.COM"})},
		{ID: 2, Data: rowData(map[string]any{"domain": "example.com"})},
		{ID: 3, Data: rowData(map[string]any{"domain": "https://bad.example"})},
		{ID: 4, Data: models.JSONValue(`{bad json`)},
		{ID: 5, Data: rowData(map[string]any{"domain": "Remote.Example"})},
	}

	domains, successfulRows := domainBlockImportDomains(rows)
	if want := []string{"example.com", "remote.example"}; !reflect.DeepEqual(domains, want) {
		t.Fatalf("domains = %#v, want %#v", domains, want)
	}
	if want := []int64{1, 2, 5}; !reflect.DeepEqual(successfulRows, want) {
		t.Fatalf("successful rows = %#v, want %#v", successfulRows, want)
	}
}

func TestBookmarkImportURIsKeepsFirstRowForOverwriteMatches(t *testing.T) {
	rowData := func(values map[string]any) models.JSONValue {
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		return models.JSONValue(encoded)
	}
	rows := []models.BulkImportRow{
		{ID: 1, Data: rowData(map[string]any{"uri": " https://example.test/users/alice/statuses/1 "})},
		{ID: 2, Data: rowData(map[string]any{"uri": "https://example.test/users/alice/statuses/1"})},
		{ID: 3, Data: rowData(map[string]any{"uri": ""})},
		{ID: 4, Data: models.JSONValue(`{bad json`)},
		{ID: 5, Data: rowData(map[string]any{"uri": "https://remote.test/notes/2"})},
	}

	got := bookmarkImportURIs(rows)
	want := map[string]int64{
		"https://example.test/users/alice/statuses/1": 1,
		"https://remote.test/notes/2":                 5,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uris = %#v, want %#v", got, want)
	}
}

func TestBookmarkImportMaintainsRailsBookmarkFeedCache(t *testing.T) {
	src, err := os.ReadFile("imports.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`var removedBookmarks []models.Bookmark`,
		`return s.enqueueOrProcessImportRows(context.Background(), rows)`,
	} {
		if !functionBodyContains(t, src, "processBookmarkImport", want) {
			t.Fatalf("bookmark import parent wiring missing %q", want)
		}
	}
	for _, want := range []string{
		`added = res.RowsAffected > 0`,
		`s.addBookmarkToFeedCache(context.Background(), bookmark)`,
	} {
		if !functionBodyContains(t, src, "processBookmarkImportRow", want) {
			t.Fatalf("bookmark import row feed cache wiring missing %q", want)
		}
	}
	for _, want := range []string{
		`s.runBookmarkDestroyedSideEffects(context.Background(), bookmark)`,
	} {
		if !functionBodyContains(t, src, "processBookmarkImport", want) {
			t.Fatalf("bookmark import feed cache wiring missing %q", want)
		}
	}
	for _, want := range []string{
		`removedBookmarks *[]models.Bookmark`,
		`*removedBookmarks = append(*removedBookmarks, bookmark)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("bookmark overwrite feed cache wiring missing %q", want)
		}
	}
	feedSrc, err := os.ReadFile("bookmark_feed_cache.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.removeBookmarkFromFeedCache(ctx, bookmark.AccountID, bookmark.StatusID)`,
		`s.invalidateDestroyedBookmarkCleanupInfo(ctx, bookmark)`,
		`s.invalidateStatusesCleanupLastInspected(ctx, bookmark.AccountID, bookmark.StatusID, "unbookmark")`,
	} {
		if !strings.Contains(string(feedSrc), want) {
			t.Fatalf("bookmark destroyed side effects missing %q", want)
		}
	}
}

func TestListImportTitlesTrimsDeduplicatesAndSkipsBadRows(t *testing.T) {
	rowData := func(values map[string]any) models.JSONValue {
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		return models.JSONValue(encoded)
	}
	rows := []models.BulkImportRow{
		{ID: 1, Data: rowData(map[string]any{"list_name": " Friends "})},
		{ID: 2, Data: rowData(map[string]any{"list_name": "Friends"})},
		{ID: 3, Data: rowData(map[string]any{"list_name": ""})},
		{ID: 4, Data: models.JSONValue(`{bad json`)},
		{ID: 5, Data: rowData(map[string]any{"list_name": "Work"})},
	}

	got := listImportTitles(rows)
	want := []string{"Friends", "Work"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("titles = %#v, want %#v", got, want)
	}
}

func TestListImportClearsRailsListAndFollowFeedCaches(t *testing.T) {
	src, err := os.ReadFile("imports.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"processListImport", `affectedListIDs := []int64{}`},
		{"processListImport", `listIDs, err := applyListImportOverwrite(tx, bulkImport.AccountID, listImportTitles(rows))`},
		{"processListImport", `for _, listID := range uniqueInt64s(affectedListIDs) {`},
		{"processListImport", `_ = s.clearListFeedCacheContext(context.Background(), listID)`},
		{"processListImport", `return s.enqueueOrProcessImportRows(context.Background(), rows)`},
		{"processListImportRow", `affectedListID = list.ID`},
		{"processListImportRow", `_ = s.clearListFeedCacheContext(context.Background(), affectedListID)`},
		{"processListImportRow", `_ = s.clearHomeFeedCacheContext(context.Background(), bulkImport.AccountID)`},
		{"processListImportRow", `s.invalidateFollowRelationshipCaches(context.Background(), models.Account{ID: bulkImport.AccountID}, affectedFollowTargetID)`},
		{"applyListImportOverwrite", `return append(removedIDs, clearedIDs...), nil`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("imports.go:%s does not contain %q", check.fn, check.want)
		}
	}
}

func TestImportRemoteAcctSkipsLocalDomainsAndNormalizesRemoteDomain(t *testing.T) {
	s := &Server{cfg: config.Config{LocalDomain: "example.com", WebDomain: "web.example.com"}}

	if acct, ok := s.importRemoteAcct("@Alice@Remote.EXAMPLE"); !ok || acct != "Alice@remote.example" {
		t.Fatalf("remote acct = %q, %v", acct, ok)
	}
	if acct, ok := s.importRemoteAcct("alice@example.com"); ok || acct != "" {
		t.Fatalf("local acct resolved remotely: %q, %v", acct, ok)
	}
	if acct, ok := s.importRemoteAcct("alice@web.example.com"); ok || acct != "" {
		t.Fatalf("web-domain acct resolved remotely: %q, %v", acct, ok)
	}
	if acct, ok := s.importRemoteAcct("alice"); ok || acct != "" {
		t.Fatalf("bare acct resolved remotely: %q, %v", acct, ok)
	}
}

func TestAccountMatchesImportAcctRequiresSameRemoteAcct(t *testing.T) {
	account := &models.Account{Username: "Alice", Domain: sql.NullString{String: "Remote.Example", Valid: true}}
	if !accountMatchesImportAcct(account, "alice@remote.example") {
		t.Fatal("expected account to match import acct case-insensitively")
	}
	if accountMatchesImportAcct(account, "bob@remote.example") {
		t.Fatal("unexpected username match")
	}
	if accountMatchesImportAcct(&models.Account{Username: "Alice"}, "alice@remote.example") {
		t.Fatal("local account matched remote acct")
	}
}

func TestImportValueHelpersMatchCSVJSONShapes(t *testing.T) {
	values := map[string]any{
		"show_reblogs": "false",
		"notify":       true,
	}
	if importBool(values, "show_reblogs", true) {
		t.Fatal("show_reblogs string false parsed as true")
	}
	if !importBool(values, "notify", false) {
		t.Fatal("notify bool true parsed as false")
	}
	if !importBool(values, "missing", true) {
		t.Fatal("missing bool did not use fallback")
	}

	for _, tt := range []struct {
		value any
		want  []string
	}{
		{value: []any{"en", " ja ", ""}, want: []string{"en", "ja"}},
		{value: []string{"fr", " de "}, want: []string{"fr", "de"}},
		{value: "en, ja, ", want: []string{"en", "ja"}},
	} {
		if got := importStringArray(tt.value); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("importStringArray(%#v) = %#v, want %#v", tt.value, got, tt.want)
		}
	}
}

func TestSettingsImportsRequireWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/imports/1", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.showSettingsImport(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/imports/1")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestImportVacuumConstantsMatchRailsImportsVacuum(t *testing.T) {
	if importVacuumWorkerInterval != 24*time.Hour {
		t.Fatalf("importVacuumWorkerInterval = %s", importVacuumWorkerInterval)
	}
	if importVacuumBatchSize != 1000 {
		t.Fatalf("importVacuumBatchSize = %d", importVacuumBatchSize)
	}
	if unconfirmedImportTTL != 10*time.Minute {
		t.Fatalf("unconfirmedImportTTL = %s", unconfirmedImportTTL)
	}
	if oldImportTTL != 7*24*time.Hour {
		t.Fatalf("oldImportTTL = %s", oldImportTTL)
	}
}

func TestImportVacuumWorkerUsesRailsDeleteShape(t *testing.T) {
	src, err := os.ReadFile("import_vacuum_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		functionName string
		want         string
	}{
		{"runImportVacuumWorker", `s.vacuumExpiredImports(ctx, now.UTC())`},
		{"vacuumExpiredImports", `s.vacuumBulkImports(ctx, "state = ? AND created_at <= ?", []any{bulkImportStateUnconfirmed, now.Add(-unconfirmedImportTTL)})`},
		{"vacuumExpiredImports", `s.vacuumBulkImports(ctx, "created_at <= ?", []any{now.Add(-oldImportTTL)})`},
		{"vacuumBulkImports", `Model(&models.BulkImport{})`},
		{"vacuumBulkImports", `Limit(importVacuumBatchSize)`},
		{"vacuumBulkImports", `Delete(&models.BulkImport{}, ids)`},
	}
	for _, check := range checks {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runImportVacuumWorker)") {
		t.Fatal("StartBackgroundWorkers does not start import vacuum worker")
	}
}

func TestMuteImportAppliesRailsMuteServiceSideEffects(t *testing.T) {
	src, err := os.ReadFile("imports.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"processRelationshipImport", `s.clearAfterMuteFeedCache(context.Background(), bulkImport.AccountID, targetID)`},
		{"processRelationshipImportRow", `hideNotifications = importType == "muting" && importBool(values, "hide_notifications", true)`},
		{"processRelationshipImportRow", `s.clearAfterBlockFeedCaches(context.Background(), bulkImport.AccountID, targetID)`},
		{"processRelationshipImportRow", `s.clearAfterMuteFeedCache(context.Background(), bulkImport.AccountID, targetID)`},
		{"applyRelationshipImportRow", `hideNotifications := importBool(values, "hide_notifications", true)`},
		{"applyRelationshipImportRow", `afterBlockServiceCleanup(tx, accountID, target.ID)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("imports.go:%s does not contain %q", check.fn, check.want)
		}
	}
}

func TestBlockImportAppliesRailsBlockServiceSideEffects(t *testing.T) {
	src, err := os.ReadFile("imports.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"processRelationshipImport", `s.clearAfterBlockFeedCaches(context.Background(), bulkImport.AccountID, targetID)`},
		{"processRelationshipImport", `s.invalidateBlockRelationshipCaches(context.Background(), bulkImport.AccountID, targetID)`},
		{"applyRelationshipImportRow", `s.createAccountBlock(tx, accountID, target.ID, now)`},
		{"applyRelationshipImportRow", `afterBlockServiceCleanup(tx, accountID, target.ID)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("imports.go:%s does not contain %q", check.fn, check.want)
		}
	}
}

func TestFollowImportClearsRailsHomeFeedCache(t *testing.T) {
	src, err := os.ReadFile("imports.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"processRelationshipImport", `affectedTargets := uniqueInt64s(affectedRelationshipTargets)`},
		{"processRelationshipImport", `if importType == "following" && len(affectedTargets) > 0 {`},
		{"processRelationshipImport", `_ = s.clearHomeFeedCacheContext(context.Background(), bulkImport.AccountID)`},
		{"processRelationshipImport", `for _, targetID := range affectedTargets {`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("imports.go:%s does not contain %q", check.fn, check.want)
		}
	}
}
