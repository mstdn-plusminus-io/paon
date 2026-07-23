package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestAssignMediaAttachmentsToStatusExpandsPostgresINList(t *testing.T) {
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=paon dbname=paon",
		PreferSimpleProtocol: false,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}

	result := assignMediaAttachmentsToStatus(database, 7, 9, []int64{11, 12}, time.Unix(1_700_000_000, 0).UTC())
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	query := result.Statement.SQL.String()
	if !strings.Contains(query, `WHERE account_id = $4 AND id IN ($5,$6)`) {
		t.Fatalf("media attachment update did not expand the PostgreSQL IN list: %s", query)
	}
	if len(result.Statement.Vars) != 6 {
		t.Fatalf("media attachment update vars = %#v, want six bound values", result.Statement.Vars)
	}
}

func TestParseStatusUpdatePayloadDetectsJSONPollNullAndMediaAttributes(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PUT", "/api/v1/statuses/1", strings.NewReader(`{
		"status":"edited",
		"media_ids":["4"],
		"media_attributes":[{"id":"4","description":"new description","focus":"0.25,-0.5"}],
		"sensitive":true,
		"spoiler_text":"cw",
		"language":"ja",
		"poll":null
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseStatusUpdatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.HasStatus || payload.Status != "edited" {
		t.Fatalf("status = %#v", payload)
	}
	if !payload.HasMediaIDs || len(payload.MediaIDs) != 1 || payload.MediaIDs[0] != "4" {
		t.Fatalf("media ids = %#v", payload.MediaIDs)
	}
	if len(payload.MediaAttributes) != 1 || payload.MediaAttributes[0].Description == nil || *payload.MediaAttributes[0].Description != "new description" {
		t.Fatalf("media attributes = %#v", payload.MediaAttributes)
	}
	if payload.MediaAttributes[0].Focus == nil || *payload.MediaAttributes[0].Focus != "0.25,-0.5" {
		t.Fatalf("media focus = %#v", payload.MediaAttributes)
	}
	if !payload.HasSensitive || !payload.Sensitive {
		t.Fatalf("sensitive = %#v", payload)
	}
	if !payload.HasSpoilerText || payload.SpoilerText != "cw" {
		t.Fatalf("spoiler = %#v", payload)
	}
	if !payload.HasLanguage || payload.Language != "ja" {
		t.Fatalf("language = %#v", payload)
	}
	if !payload.HasPoll || payload.Poll != nil {
		t.Fatalf("poll = %#v", payload.Poll)
	}
}

func TestParseStatusUpdatePayloadAcceptsRailsMediaAttributesForm(t *testing.T) {
	e := echo.New()
	body := strings.Join([]string{
		"media_ids%5B%5D=4",
		"media_ids%5B%5D=9",
		"media_attributes%5B1%5D%5Bid%5D=9",
		"media_attributes%5B1%5D%5Bfocus%5D=-0.25%2C0.5",
		"media_attributes%5B0%5D%5Bid%5D=4",
		"media_attributes%5B0%5D%5Bdescription%5D=new+description",
	}, "&")
	req := httptest.NewRequest("PUT", "/api/v1/statuses/1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseStatusUpdatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.HasMediaIDs || len(payload.MediaIDs) != 2 || payload.MediaIDs[0] != "4" || payload.MediaIDs[1] != "9" {
		t.Fatalf("media ids = %#v has=%v", payload.MediaIDs, payload.HasMediaIDs)
	}
	if len(payload.MediaAttributes) != 2 {
		t.Fatalf("media attributes = %#v", payload.MediaAttributes)
	}
	first := payload.MediaAttributes[0]
	if first.ID != "4" || first.Description == nil || *first.Description != "new description" || first.Focus != nil {
		t.Fatalf("first media attribute = %#v", first)
	}
	second := payload.MediaAttributes[1]
	if second.ID != "9" || second.Focus == nil || *second.Focus != "-0.25,0.5" || second.Description != nil {
		t.Fatalf("second media attribute = %#v", second)
	}
}

func TestParseStatusCreatePayloadAcceptsJSONReplyPollAndMediaAttributes(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/statuses", strings.NewReader(`{
		"status":"hello",
		"in_reply_to_id":123,
		"media_ids":["4"],
		"media_attributes":[{"id":"4","description":"new description","focus":"0.25,-0.5"}],
		"sensitive":true,
		"spoiler_text":"cw",
		"visibility":"unlisted",
		"language":"ja",
		"scheduled_at":"2026-06-19T12:00:00Z",
		"quote_id":"456",
		"allowed_mentions":[123,"456"],
		"poll":{"options":["yes","no"],"multiple":true,"hide_totals":true,"expires_in":3600}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseStatusCreatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.HasStatus || payload.Status != "hello" || payload.InReplyToID != "123" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Visibility != "unlisted" || !payload.Sensitive || payload.SpoilerText != "cw" || payload.Language != "ja" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.ScheduledAt != "2026-06-19T12:00:00Z" {
		t.Fatalf("scheduled_at = %q", payload.ScheduledAt)
	}
	if payload.QuoteID != "456" {
		t.Fatalf("quote_id = %q", payload.QuoteID)
	}
	if !payload.HasAllowedMentions || len(payload.AllowedMentions) != 2 || payload.AllowedMentions[0] != "123" || payload.AllowedMentions[1] != "456" {
		t.Fatalf("allowed_mentions = %#v has=%v", payload.AllowedMentions, payload.HasAllowedMentions)
	}
	if !payload.HasMediaIDs || len(payload.MediaIDs) != 1 || payload.MediaIDs[0] != "4" {
		t.Fatalf("media ids = %#v", payload.MediaIDs)
	}
	if len(payload.MediaAttributes) != 1 || payload.MediaAttributes[0].Description == nil || *payload.MediaAttributes[0].Description != "new description" {
		t.Fatalf("media attributes = %#v", payload.MediaAttributes)
	}
	if !payload.HasPoll || payload.Poll == nil || len(payload.Poll.Options) != 2 || !payload.Poll.Multiple || !payload.Poll.HideTotals || payload.Poll.ExpiresIn != 3600 {
		t.Fatalf("poll = %#v", payload.Poll)
	}
}

func TestParseStatusCreatePayloadAcceptsFormReplyAndPoll(t *testing.T) {
	e := echo.New()
	body := "status=hello&in_reply_to_id=123&quote_id=456&visibility=private&scheduled_at=2026-06-19T12%3A00%3A00Z&allowed_mentions%5B%5D=123&allowed_mentions%5B%5D=456&poll%5Boptions%5D%5B%5D=yes&poll%5Boptions%5D%5B%5D=no&poll%5Bmultiple%5D=0&poll%5Bexpires_in%5D=600"
	req := httptest.NewRequest("POST", "/api/v1/statuses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseStatusCreatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Status != "hello" || payload.InReplyToID != "123" || payload.QuoteID != "456" || payload.Visibility != "private" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.ScheduledAt != "2026-06-19T12:00:00Z" {
		t.Fatalf("scheduled_at = %q", payload.ScheduledAt)
	}
	if !payload.HasAllowedMentions || len(payload.AllowedMentions) != 2 || payload.AllowedMentions[0] != "123" || payload.AllowedMentions[1] != "456" {
		t.Fatalf("allowed_mentions = %#v has=%v", payload.AllowedMentions, payload.HasAllowedMentions)
	}
	if !payload.HasPoll || payload.Poll == nil || len(payload.Poll.Options) != 2 || payload.Poll.ExpiresIn != 600 {
		t.Fatalf("poll = %#v", payload.Poll)
	}
}

func TestStatusAllowedMentionsFromFormMatchesRailsArrayParams(t *testing.T) {
	values := map[string][]string{"allowed_mentions[]": {"123,456", "789"}}
	got, ok := statusAllowedMentionsFromForm(values)
	if !ok {
		t.Fatal("expected allowed_mentions presence")
	}
	want := []string{"123,456", "789"}
	if len(got) != len(want) {
		t.Fatalf("allowed_mentions = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allowed_mentions = %#v, want %#v", got, want)
		}
	}
}

func TestUnexpectedMentionAccountsMatchesRailsAllowedMentionsGuard(t *testing.T) {
	accounts := []models.Account{{ID: 123}, {ID: 456}}
	if got := unexpectedMentionAccounts(accounts, nil, false); len(got) != 0 {
		t.Fatalf("omitted allowed_mentions should not enforce: %#v", got)
	}
	got := unexpectedMentionAccounts(accounts, []string{"123"}, true)
	if len(got) != 1 || got[0].ID != 456 {
		t.Fatalf("unexpected accounts = %#v", got)
	}
	got = unexpectedMentionAccounts(accounts, []string{}, true)
	if len(got) != 2 {
		t.Fatalf("empty allowed_mentions should reject all mentions: %#v", got)
	}
}

func TestParseStatusUpdatePayloadUsesRailsTruthySensitiveFormValue(t *testing.T) {
	e := echo.New()
	for _, value := range []string{"true", "1", "on", "yes", "t", "bad", "no"} {
		req := httptest.NewRequest("PUT", "/api/v1/statuses/1", strings.NewReader("sensitive="+value))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		payload, err := parseStatusUpdatePayload(c)
		if err != nil {
			t.Fatal(err)
		}
		if !payload.HasSensitive || !payload.Sensitive {
			t.Fatalf("sensitive=%q parsed as %#v", value, payload)
		}
	}
	for _, value := range []string{"false", "0", "off", "f"} {
		req := httptest.NewRequest("PUT", "/api/v1/statuses/1", strings.NewReader("sensitive="+value))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, e)
		payload, err := parseStatusUpdatePayload(c)
		if err != nil {
			t.Fatal(err)
		}
		if !payload.HasSensitive || payload.Sensitive {
			t.Fatalf("sensitive=%q parsed as %#v", value, payload)
		}
	}
}

func TestParseReblogPayloadAcceptsJSONAndFormVisibility(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/statuses/1/reblog", strings.NewReader(`{"visibility":"unlisted"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseReblogPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Visibility != "unlisted" {
		t.Fatalf("json visibility = %q", payload.Visibility)
	}

	req = httptest.NewRequest("POST", "/api/v1/statuses/1/reblog", strings.NewReader("visibility=private"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)

	payload, err = parseReblogPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Visibility != "private" {
		t.Fatalf("form visibility = %q", payload.Visibility)
	}
}

func TestReblogVisibilityMatchesRailsServiceBoundaries(t *testing.T) {
	server := &Server{}
	account := models.Account{
		Locked: false,
		User:   models.User{Settings: sql.NullString{String: `{"default_privacy":"unlisted"}`, Valid: true}},
	}
	if got := server.statusVisibility(account, ""); got != 1 {
		t.Fatalf("status default privacy visibility = %d", got)
	}
	if got := server.statusVisibility(models.Account{Locked: true}, ""); got != 2 {
		t.Fatalf("locked account default visibility = %d", got)
	}
	if got := server.reblogVisibility(account, models.Status{Visibility: 0}, "private"); got != 2 {
		t.Fatalf("requested visibility = %d", got)
	}
	if got := server.reblogVisibility(account, models.Status{Visibility: 0}, ""); got != 1 {
		t.Fatalf("default privacy visibility = %d", got)
	}
	if got := server.reblogVisibility(account, models.Status{Visibility: 2}, "public"); got != 2 {
		t.Fatalf("hidden target visibility = %d", got)
	}
}

func TestReblogRejectsDirectAndLimitedTargetsBeforeCreate(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := `target = properReblogTarget(target)`; !strings.Contains(string(src), want) {
		t.Fatalf("reblog target normalization missing %q", want)
	}
	if want := `if target.Visibility == 3 || target.Visibility == 4 || (target.Visibility == 2 && target.AccountID != account.ID) {`; !strings.Contains(string(src), want) {
		t.Fatalf("reblog visibility guard missing %q", want)
	}
}

func TestStatusVisibilityValidatesRailsEnumAndSilencedPublicFallback(t *testing.T) {
	for _, value := range []string{"", "public", "unlisted", "private", "direct", "limited"} {
		if !validStatusVisibility(value) {
			t.Fatalf("%q should be a valid Rails status visibility", value)
		}
	}
	for _, value := range []string{"local", "publicish", "friends"} {
		if validStatusVisibility(value) {
			t.Fatalf("%q should not be a valid Rails status visibility", value)
		}
	}

	server := &Server{}
	if got := visibilityValue("limited"); got != 4 {
		t.Fatalf("limited visibility = %d", got)
	}
	silenced := models.Account{SilencedAt: sql.NullTime{Valid: true}}
	if got := server.statusVisibility(silenced, "public"); got != 1 {
		t.Fatalf("silenced public visibility = %d", got)
	}
	if got := server.statusVisibility(silenced, ""); got != 1 {
		t.Fatalf("silenced default public visibility = %d", got)
	}
}

func TestStatusLanguageCascadeMatchesRailsLocaleFallback(t *testing.T) {
	if got := validStatusLocaleOrNil("en-US"); got != "en" {
		t.Fatalf("regional locale fallback = %q", got)
	}
	if got := validStatusLocaleCascade("", "bad", "pt_BR", "ja"); got != "pt" {
		t.Fatalf("locale cascade = %q", got)
	}
	account := models.Account{User: models.User{
		Settings: sql.NullString{String: `{"default_language":"bad"}`, Valid: true},
		Locale:   sql.NullString{String: "fr-CA", Valid: true},
	}}
	server := &Server{cfg: config.Config{DefaultLocale: "ja"}}
	if got := server.statusLanguageForAccount("", sql.NullString{}, account); !got.Valid || got.String != "fr" {
		t.Fatalf("account language = %#v", got)
	}
	if got := server.statusLanguageForAccount("xx", sql.NullString{String: "en", Valid: true}, account); !got.Valid || got.String != "en" {
		t.Fatalf("current language fallback = %#v", got)
	}
	if got := server.statusLanguageForAccount("de-DE", sql.NullString{String: "en", Valid: true}, account); !got.Valid || got.String != "de" {
		t.Fatalf("requested language fallback = %#v", got)
	}
}

func TestStatusMutationsUseRailsLanguageCascade(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`language := s.statusLanguageForAccount(payload.Language, sql.NullString{}, *account)`,
		`nextLanguage := s.statusLanguageForAccount(payload.Language, status.Language, *account)`,
		`updates["language"] = nextLanguage`,
		`statusUpdateHasSignificantChanges(*status, payload, nextText, nextSpoilerText, nextLanguage)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("status language cascade wiring missing %q", want)
		}
	}
	scheduled, err := os.ReadFile("scheduled_status_publish.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := `language := s.statusLanguageForAccount(payload.Language, sql.NullString{}, account)`; !strings.Contains(string(scheduled), want) {
		t.Fatalf("scheduled language cascade wiring missing %q", want)
	}
}

func TestStatusMutationHandlersRejectInvalidVisibilityBeforePersisting(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if !validStatusVisibility(payload.Visibility) {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Visibility is invalid")`,
		`if !validReblogVisibility(payload.Visibility) {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Visibility is reserved")`,
		`Visibility: s.reblogVisibility(*account, *target, payload.Visibility),`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("status visibility validation missing %q", want)
		}
	}
}

func TestStatusRelationshipFlagsMarkNestedReblogTarget(t *testing.T) {
	original := models.Status{ID: 42}
	wrapper := models.Status{ID: 84, Reblog: &original}
	applyStatusRelationshipFlags(&wrapper, nil, nil, map[int64]struct{}{42: {}}, nil, nil)
	if wrapper.RebloggedByCurrent {
		t.Fatal("reblog wrapper must not be marked as reblogged")
	}
	if wrapper.Reblog == nil || !wrapper.Reblog.RebloggedByCurrent {
		t.Fatal("nested reblog target must be marked as reblogged")
	}
}

func TestStatusCreationUsesRailsDefaultPrivacyInsteadOfHardcodedPublic(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`visibility := s.statusVisibility(*account, payload.Visibility)`,
		`payload.Visibility = serializer.UserDefaultPrivacy(userSettingsForAccount(*account), *account)`,
		`"visibility":   payload.Visibility`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("status creation missing %q", want)
		}
	}
	if strings.Contains(string(src), `firstNonEmpty(payload.Visibility, "public")`) {
		t.Fatal("status creation still hardcodes public visibility")
	}
}

func TestScheduledStatusParamsFromPayloadUsesRESTKeys(t *testing.T) {
	params, err := scheduledStatusParamsFromPayload(statusCreatePayload{
		statusUpdatePayload: statusUpdatePayload{
			Status:      "hello",
			SpoilerText: "cw",
			Language:    "ja",
			Poll:        &pollUpdatePayload{Options: []string{"yes", "no"}, Multiple: true, HideTotals: true, ExpiresIn: 3600},
			HasPoll:     true,
		},
		Visibility:  "unlisted",
		InReplyToID: "123",
		QuoteID:     "456",
	}, []string{"4"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(params, &out); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"status":         "hello",
		"sensitive":      true,
		"spoiler_text":   "cw",
		"visibility":     "unlisted",
		"language":       "ja",
		"in_reply_to_id": "123",
		"quote_id":       "456",
	} {
		if out[key] != want {
			t.Fatalf("params[%s] = %#v, want %#v: %#v", key, out[key], want, out)
		}
	}
	if out["scheduled_at"] != nil {
		t.Fatalf("scheduled_at = %#v", out["scheduled_at"])
	}
	mediaIDs, ok := out["media_ids"].([]any)
	if !ok || len(mediaIDs) != 1 || mediaIDs[0] != "4" {
		t.Fatalf("media_ids = %#v", out["media_ids"])
	}
	poll, ok := out["poll"].(map[string]any)
	if !ok || poll["multiple"] != true || poll["hide_totals"] != true || poll["expires_in"].(float64) != 3600 {
		t.Fatalf("poll = %#v", out["poll"])
	}
}

func TestQuoteStatusURLPrefersRailsPublicURL(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "social.example", WebDomain: "social.example", Scheme: "https"}}
	local := models.Status{
		ID: 123,
		Account: models.Account{
			ID:       42,
			Username: "alice",
		},
	}
	if got := server.quoteStatusURL(local); got != "https://social.example/@alice/123" {
		t.Fatalf("local URL = %q", got)
	}
	remoteWithURL := models.Status{
		ID:  456,
		URL: sql.NullString{String: "https://remote.example/@bob/456", Valid: true},
		Account: models.Account{
			ID:       43,
			Username: "bob",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
		},
	}
	if got := server.quoteStatusURL(remoteWithURL); got != "https://remote.example/@bob/456" {
		t.Fatalf("remote URL = %q", got)
	}
	remoteWithURI := models.Status{
		ID:  789,
		URI: sql.NullString{String: "https://remote.example/users/carol/statuses/789", Valid: true},
		Account: models.Account{
			ID:       44,
			Username: "carol",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
		},
	}
	if got := server.quoteStatusURL(remoteWithURI); got != "https://remote.example/users/carol/statuses/789" {
		t.Fatalf("remote URI = %q", got)
	}
}

func TestStatusTextWithQuoteURLAppendsRailsQuoteMarker(t *testing.T) {
	if got := statusTextWithQuoteURL("hello", "https://social.example/@alice/123"); got != "hello\n\nRE: https://social.example/@alice/123" {
		t.Fatalf("quoted text = %q", got)
	}
	if got := statusTextWithQuoteURL("hello\n", "https://social.example/@alice/123"); got != "hello\n\nRE: https://social.example/@alice/123" {
		t.Fatalf("quoted text trims trailing newline = %q", got)
	}
	if got := statusTextWithQuoteURL("", "https://social.example/@alice/123"); got != "\n\nRE: https://social.example/@alice/123" {
		t.Fatalf("blank quoted text = %q", got)
	}
}

func TestStatusTextWithExistingQuoteURLPreservesRailsQuoteOnEdit(t *testing.T) {
	quoteURL := "https://social.example/@alice/123"
	if got := statusTextWithExistingQuoteURL("edited", quoteURL); got != "edited\n\nRE: "+quoteURL {
		t.Fatalf("quoted edit = %q", got)
	}
	alreadyQuoted := "edited\n\nRE: " + quoteURL
	if got := statusTextWithExistingQuoteURL(alreadyQuoted, quoteURL); got != alreadyQuoted {
		t.Fatalf("quoted edit duplicated = %q", got)
	}
	if got := statusTextWithExistingQuoteURL("edited", ""); got != "edited" {
		t.Fatalf("quote-free edit = %q", got)
	}
}

func TestUpdateStatusUsesQuotePreservingTextForMetadataRefresh(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		`nextText = statusTextWithExistingQuoteURL(payload.Status, status.QuoteOriginalURL.String)`,
		`updates["text"] = nextText`,
		`deleteStatusPreviewCardLinks(tx, status.ID)`,
		`s.updateStatusMentionsFromTextAndCollectAccounts(tx, status.ID, account.ID, nextText, now)`,
		`replaceStatusTagsFromText(tx, status.ID, nextText, now)`,
	}
	for _, want := range checks {
		if !strings.Contains(string(src), want) {
			t.Fatalf("updateStatus missing %q", want)
		}
	}
}

func TestStatusPreviewCardLinksAreClearedForRailsRecrawl(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`func deleteStatusPreviewCardLinks(tx *gorm.DB, statusID int64) error`,
		`DELETE FROM preview_cards_statuses WHERE status_id = ?`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("preview card reset missing %q", want)
		}
	}
}

func TestSpoilerTextFallbackMatchesRailsStatusServices(t *testing.T) {
	createPayload := statusUpdatePayload{SpoilerText: "content warning", HasSpoilerText: true}
	sensitive := statusSensitiveValue(createPayload)
	applyCreateSpoilerTextFallback(&createPayload)
	if createPayload.Status != "content warning" || createPayload.SpoilerText != "" || !createPayload.HasStatus || createPayload.HasSpoilerText {
		t.Fatalf("create fallback = %#v", createPayload)
	}
	if !sensitive {
		t.Fatal("create fallback should preserve preprocessed sensitive value")
	}

	updatePayload := statusUpdatePayload{Status: "", HasStatus: true, SpoilerText: "edited text", HasSpoilerText: true}
	applyUpdateSpoilerTextFallback(&updatePayload)
	if updatePayload.Status != "edited text" || updatePayload.SpoilerText != "" || updatePayload.HasSpoilerText {
		t.Fatalf("update fallback = %#v", updatePayload)
	}

	noStatusKey := statusUpdatePayload{SpoilerText: "keep cw", HasSpoilerText: true}
	applyUpdateSpoilerTextFallback(&noStatusKey)
	if noStatusKey.Status != "" || noStatusKey.SpoilerText != "keep cw" || !noStatusKey.HasSpoilerText {
		t.Fatalf("update without status key should not fallback: %#v", noStatusKey)
	}
}

func TestSpoilerTextFallbackIsWiredForCreateUpdateAndScheduledPublish(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`applyCreateSpoilerTextFallback(&payload.statusUpdatePayload)`,
		`applyUpdateSpoilerTextFallback(&payload)`,
		`sensitive := statusSensitiveForCreate(payload.statusUpdatePayload, *account)`,
		`payload.Sensitive = sensitive`,
		`payload.HasSensitive = true`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("spoiler fallback wiring missing %q", want)
		}
	}
	scheduledSrc, err := os.ReadFile("scheduled_status_publish.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`sensitive := statusSensitiveValue(payload.statusUpdatePayload)`,
		`applyCreateSpoilerTextFallback(&payload.statusUpdatePayload)`,
	} {
		if !strings.Contains(string(scheduledSrc), want) {
			t.Fatalf("scheduled spoiler fallback wiring missing %q", want)
		}
	}
}

func TestStatusUpdatePayloadSubmittedTracksRailsEditableFields(t *testing.T) {
	if statusUpdatePayloadSubmitted(statusUpdatePayload{}) {
		t.Fatal("empty update payload should not be submitted")
	}
	description := "description"
	for name, payload := range map[string]statusUpdatePayload{
		"status":           {HasStatus: true},
		"media_ids":        {HasMediaIDs: true},
		"media_attributes": {MediaAttributes: []mediaAttributePayload{{ID: "1", Description: &description}}},
		"sensitive":        {HasSensitive: true},
		"spoiler_text":     {HasSpoilerText: true},
		"language":         {HasLanguage: true},
		"poll":             {HasPoll: true},
	} {
		if !statusUpdatePayloadSubmitted(payload) {
			t.Fatalf("%s update payload should be submitted", name)
		}
	}
}

func TestUpdateStatusSkipsEditMutationForEmptyPayloadLikeRails(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, `func (s *Server) updateStatus(c *echo.Context) error {`)
	end := strings.Index(body, `func (s *Server) deleteStatus(c *echo.Context) error {`)
	if start < 0 || end < 0 || start >= end {
		t.Fatal("could not isolate updateStatus handler")
	}
	updateStatus := body[start:end]
	for _, want := range []string{
		`if !statusUpdateHasSignificantChanges(*status, payload, nextText, nextSpoilerText, nextLanguage) {`,
		`return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))`,
	} {
		if !strings.Contains(updateStatus, want) {
			t.Fatalf("empty update guard missing %q", want)
		}
	}
	guardIndex := strings.Index(updateStatus, `if !statusUpdateHasSignificantChanges(*status, payload, nextText, nextSpoilerText, nextLanguage) {`)
	transactionIndex := strings.Index(updateStatus, `if err := s.db.Transaction(func(tx *gorm.DB) error {`)
	if guardIndex < 0 || transactionIndex < 0 || guardIndex > transactionIndex {
		t.Fatal("empty update guard must run before edit transaction creates snapshots")
	}
}

func TestDeleteStatusSourceResponseDecrementsInMemoryAccountCount(t *testing.T) {
	status := &models.Status{
		Visibility: 0,
		Account: models.Account{
			AccountStat: models.AccountStat{StatusesCount: 2},
		},
	}
	decrementDeletedStatusAccountCountForResponse(status)
	if got := status.Account.AccountStat.StatusesCount; got != 1 {
		t.Fatalf("statuses_count after delete response adjustment = %d, want 1", got)
	}

	status.Visibility = 3
	decrementDeletedStatusAccountCountForResponse(status)
	if got := status.Account.AccountStat.StatusesCount; got != 1 {
		t.Fatalf("direct status should not change Go account_stats response count, got %d", got)
	}

	status.Visibility = 0
	status.Account.AccountStat.StatusesCount = 0
	decrementDeletedStatusAccountCountForResponse(status)
	if got := status.Account.AccountStat.StatusesCount; got != 0 {
		t.Fatalf("statuses_count should not underflow, got %d", got)
	}
}

func TestStatusLimitHelpersUseConfigWithMastodonDefaults(t *testing.T) {
	defaults := &Server{}
	if defaults.maxStatusChars() != 5000 || defaults.maxMediaAttachments() != 4 {
		t.Fatalf("default limits = %d/%d", defaults.maxStatusChars(), defaults.maxMediaAttachments())
	}
	custom := &Server{cfg: config.Config{StatusMaxChars: 7000, MaxMedia: 8}}
	if custom.maxStatusChars() != 7000 || custom.maxMediaAttachments() != 8 {
		t.Fatalf("custom limits = %d/%d", custom.maxStatusChars(), custom.maxMediaAttachments())
	}
	explicitZeroStatus := &Server{cfg: config.Config{StatusMaxChars: 0, StatusMaxCharsSet: true}}
	if explicitZeroStatus.maxStatusChars() != 0 {
		t.Fatalf("explicit zero status limit = %d, want Rails-style 0", explicitZeroStatus.maxStatusChars())
	}
	explicitNegativeStatus := &Server{cfg: config.Config{StatusMaxChars: -1, StatusMaxCharsSet: true}}
	if explicitNegativeStatus.maxStatusChars() != -1 {
		t.Fatalf("explicit negative status limit = %d, want Rails-style -1", explicitNegativeStatus.maxStatusChars())
	}
	explicitZero := &Server{cfg: config.Config{MaxMedia: 0, MaxMediaSet: true}}
	if explicitZero.maxMediaAttachments() != 0 {
		t.Fatalf("explicit zero media limit = %d, want Rails-style 0", explicitZero.maxMediaAttachments())
	}
	explicitNegative := &Server{cfg: config.Config{MaxMedia: -1, MaxMediaSet: true}}
	if explicitNegative.maxMediaAttachments() != -1 {
		t.Fatalf("explicit negative media limit = %d, want Rails-style -1", explicitNegative.maxMediaAttachments())
	}
	if statusLengthTooLong("", "", 0) {
		t.Fatal("empty status should fit explicit zero STATUS_LENGTH_LIMIT like Rails")
	}
	if !statusLengthTooLong("x", "", 0) {
		t.Fatal("non-empty status should exceed explicit zero STATUS_LENGTH_LIMIT like Rails")
	}
	if !statusLengthTooLong("", "", -1) {
		t.Fatal("empty status should exceed explicit negative STATUS_LENGTH_LIMIT like Rails")
	}
}

func TestStatusLengthValidationUsesRailsCountableText(t *testing.T) {
	serverSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"createStatus", `statusLengthTooLong(text, payload.SpoilerText, s.maxStatusChars())`},
		{"updateStatus", `nextText = statusTextWithExistingQuoteURL(payload.Status, status.QuoteOriginalURL.String)`},
		{"updateStatus", `statusLengthTooLong(nextText, nextSpoilerText, s.maxStatusChars())`},
	} {
		if !functionBodyContains(t, serverSrc, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
	scheduledSrc, err := os.ReadFile("scheduled_status_publish.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, scheduledSrc, "publishScheduledStatus", `statusLengthTooLong(text, payload.SpoilerText, s.maxStatusChars())`) {
		t.Fatal("scheduled status publish should use Rails-compatible status length")
	}
}

func TestStatusIdempotencyRedisKeyMatchesRailsShape(t *testing.T) {
	cfg := config.Config{RedisNamespace: "mastodon:"}
	got := statusIdempotencyRedisKey(cfg, 42, "abc")
	if got != "mastodon:idempotency:status:42:abc" {
		t.Fatalf("key = %q", got)
	}
	if cleanIdempotencyKey("  abc  ") != "abc" {
		t.Fatal("idempotency key was not trimmed")
	}
}

func TestStatusIdempotencyDuplicateIgnoresEmptyKey(t *testing.T) {
	id, ok := (&Server{}).statusIdempotencyDuplicate(nil, 42, "")
	if ok || id != "" {
		t.Fatalf("duplicate = %q/%v", id, ok)
	}
}

func TestStatusCreateAndScheduledPublishUseVisibleReplyLookup(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := `replyTo, err = s.findVisibleStatusForAccount(account, payload.InReplyToID)`; !strings.Contains(string(src), want) {
		t.Fatalf("status create reply visibility guard missing %q", want)
	}
	if want := `railsStatusReplyNotFoundMessage  = "The post you are trying to reply to does not appear to exist."`; !strings.Contains(string(src), want) {
		t.Fatalf("status create reply not-found message missing %q", want)
	}
	if !functionBodyContains(t, src, "createStatus", `return apiError(c, http.StatusNotFound, railsStatusReplyNotFoundMessage)`) {
		t.Fatal("status create reply visibility failure should use Rails-compatible message")
	}
	for _, want := range []string{
		`replyTo, err = s.railsStatusReplyTarget(replyTo)`,
		`status.InReplyToAccountID = railsStatusReplyAccountID(account.ID, replyTo)`,
	} {
		if !functionBodyContains(t, src, "createStatus", want) {
			t.Fatalf("createStatus reply normalization missing %q", want)
		}
	}

	scheduled, err := os.ReadFile("scheduled_status_publish.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := `replyTo, err = s.findVisibleStatusForAccount(&account, payload.InReplyToID)`; !strings.Contains(string(scheduled), want) {
		t.Fatalf("scheduled status reply visibility guard missing %q", want)
	}
	for _, want := range []string{
		`replyTo, err = s.railsStatusReplyTarget(replyTo)`,
		`status.InReplyToAccountID = railsStatusReplyAccountID(account.ID, replyTo)`,
	} {
		if !functionBodyContains(t, scheduled, "publishScheduledStatus", want) {
			t.Fatalf("publishScheduledStatus reply normalization missing %q", want)
		}
	}
}

func TestCreateAndReblogStatusRunCallbacksAfterPersistenceTransaction(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name        string
		transaction string
		effects     string
		find        string
		forbidden   []string
	}{
		{
			name:        "reblogStatus",
			transaction: `s.db.Transaction(func(tx *gorm.DB) error {`,
			effects:     `s.runLocalStatusAfterCreateCommitEffects(s.db, createdStatus, *account, target, nil`,
			find:        `s.findStatus(strconv.FormatInt(reblog.ID, 10))`,
			forbidden: []string{
				`s.storeLocalStatusURI(tx, &reblog, *account, now)`,
				`incrementStatusStatCounter(tx, target.ID, statusStatCounterReblogs, 1)`,
				`upsertAccountStatForStatus(tx, account.ID, reblog.Visibility, now)`,
			},
		},
	} {
		body := functionBody(t, src, check.name)
		transactionIndex := strings.Index(body, check.transaction)
		effectsIndex := strings.Index(body, check.effects)
		findIndex := strings.Index(body, check.find)
		if transactionIndex < 0 || effectsIndex < 0 || findIndex < 0 || !(transactionIndex < effectsIndex && effectsIndex < findIndex) {
			t.Fatalf("%s must run Rails Status callbacks after its persistence transaction and before serialization lookup", check.name)
		}
		for _, forbidden := range check.forbidden {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s still runs after-create-commit effect inside its transaction: %q", check.name, forbidden)
			}
		}
	}

	createBody := functionBody(t, src, "createStatus")
	transactionIndex := strings.Index(createBody, `s.db.Transaction(func(tx *gorm.DB) error {`)
	findIndex := strings.Index(createBody, `s.findStatus(strconv.FormatInt(status.ID, 10))`)
	responseIndex := strings.Index(createBody, `response := statusWithFilterContext`)
	jsonIndex := strings.Index(createBody, `responseErr := c.JSON(http.StatusOK, response)`)
	postCommitIndex := strings.Index(createBody, `s.startLocalStatusCreatePostCommit(`)
	returnIndex := strings.Index(createBody, `return responseErr`)
	if transactionIndex < 0 || findIndex < 0 || responseIndex < 0 || jsonIndex < 0 || postCommitIndex < 0 || returnIndex < 0 ||
		!(transactionIndex < findIndex && findIndex < responseIndex && responseIndex < jsonIndex && jsonIndex < postCommitIndex && postCommitIndex < returnIndex) {
		t.Fatal("createStatus must commit, write its JSON response, and only then start detached post-commit work")
	}
	for _, forbidden := range []string{
		`s.runLocalStatusAfterCreateCommitEffects(`,
		`s.enqueueOrCreateLocalNotifications(`,
		`s.fanOutStatusToLocalRecipientsSkipNotifications(`,
		`s.enqueueOrDeliverActivityPubDistribution(`,
	} {
		if strings.Contains(createBody, forbidden) {
			t.Fatalf("createStatus still blocks its HTTP response on post-commit work: %q", forbidden)
		}
	}
	rollbackIndex := strings.Index(createBody, `s.rollbackRailsFamilyRateLimit(c.Request().Context(), *account, railsRateLimitFamilyStatuses, now)`)
	if rollbackIndex < 0 || rollbackIndex > findIndex {
		t.Fatal("createStatus must finish its transaction-failure rate-limit rollback branch before response serialization")
	}
}

func TestCreateStatusDetachedPostCommitRetainsRequiredEffects(t *testing.T) {
	src, err := os.ReadFile("local_status_postcommit.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`s.runLocalStatusAfterCreateCommitEffects(`,
		`s.enqueueOrCreateLocalNotifications(`,
		`s.schedulePollExpirationNotifyWorker(`,
		`s.meiliIndexStatusBestEffort(`,
		`s.recordStatusTrendUse(`,
		`s.rememberStatusIdempotency(`,
		`s.publishStatusUpdateEvent(`,
		`s.fanOutStatusToLocalRecipientsSkipNotifications(`,
		`s.fetchLinkCardForStatusAsync(`,
		`s.enqueueOrDeliverActivityPubDistribution(`,
	} {
		if !functionBodyContains(t, src, "runLocalStatusCreatePostCommit", fragment) {
			t.Fatalf("detached create-status post-commit worker missing %q", fragment)
		}
	}
}

func TestValidateStatusMediaAttachmentsMatchesRailsPostAndUpdateValidation(t *testing.T) {
	if err := validateStatusMediaAttachments([]models.MediaAttachment{
		{Type: 0, Processing: sql.NullInt64{Int64: 2, Valid: true}},
		{Type: 1, Processing: sql.NullInt64{Int64: 2, Valid: true}},
	}); err != nil {
		t.Fatalf("image and gifv attachments should be valid: %v", err)
	}
	if err := validateStatusMediaAttachments([]models.MediaAttachment{
		{Type: 0, Processing: sql.NullInt64{Int64: 2, Valid: true}},
		{Type: 2, Processing: sql.NullInt64{Int64: 2, Valid: true}},
	}); !errors.Is(err, errMediaAttachmentsMixed) {
		t.Fatalf("mixed media error = %#v", err)
	}
	if err := validateStatusMediaAttachments([]models.MediaAttachment{
		{Type: 2, Processing: sql.NullInt64{Int64: 0, Valid: true}},
	}); !errors.Is(err, errMediaAttachmentNotReady) {
		t.Fatalf("not ready media error = %#v", err)
	}
}

func TestStatusMediaValidationErrorsMapToRailsMessages(t *testing.T) {
	if !mediaAttachmentValidationError(errInvalidMediaAttachment) || !mediaAttachmentValidationError(errMediaAttachmentsMixed) || !mediaAttachmentValidationError(errMediaAttachmentNotReady) {
		t.Fatal("media validation sentinel errors should be recognized")
	}
	for _, tt := range []struct {
		err  error
		want string
	}{
		{err: errMediaAttachmentsMixed, want: "Cannot attach a video to a post that already contains images"},
		{err: errMediaAttachmentNotReady, want: "Cannot attach files that have not finished processing. Try again in a moment!"},
		{err: errInvalidMediaAttachment, want: "Validation failed: Media attachment is invalid"},
	} {
		apiErr, ok := mediaAttachmentValidationAPIError(nil, tt.err).(apiHTTPError)
		if !ok || apiErr.message != tt.want {
			t.Fatalf("api error for %v = %#v, want %q", tt.err, apiErr, tt.want)
		}
	}
}

func TestStatusMediaValidationIsWiredForImmediateScheduledAndPublishedStatuses(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`media, err := loadStatusMediaAttachments(tx, accountID, statusID, mediaIDs)`,
		`if err := validateStatusMediaAttachments(media); err != nil`,
		`acceptedMediaIDs = orderedExistingMediaIDs(mediaIntIDs, media)`,
		`media, err := loadScheduledStatusMediaAttachments(tx, accountID, mediaIDs)`,
		`if mediaErr := mediaAttachmentValidationAPIError(c, err); mediaErr != nil`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("status media validation wiring missing %q", want)
		}
	}
	scheduledSrc, err := os.ReadFile("scheduled_status_publish.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scheduledSrc), `if mediaAttachmentValidationError(err)`) {
		t.Fatal("scheduled publish worker should treat Rails media validation errors as invalid scheduled statuses")
	}
}

func TestScheduledStatusMediaSelectionMatchesRailsStatusIDOnlyScope(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"attachMediaToScheduledStatus", "loadScheduledStatusMediaAttachments"} {
		if !functionBodyContains(t, src, fn, `Where("account_id = ? AND status_id IS NULL AND id IN ?", accountID, mediaIDs)`) {
			t.Fatalf("%s should match Rails PostStatusService media lookup by status_id only", fn)
		}
		if functionBodyContains(t, src, fn, `scheduled_status_id IS NULL`) {
			t.Fatalf("%s must not reject media already attached to a scheduled status; Rails only filters status_id", fn)
		}
	}
}

func TestStatusEditPollAndMediaChangesInvalidateRailsStatusCache(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if payload.HasMediaIDs || payload.HasPoll {`,
		`s.invalidateStatusCache(c.Request().Context(), status.ID)`,
	} {
		if !functionBodyContains(t, src, "updateStatus", want) {
			t.Fatalf("status edit cache invalidation missing %q", want)
		}
	}
}

func TestUniqueInt64sDropsDuplicates(t *testing.T) {
	got := uniqueInt64s([]int64{2, 0, 2, 3, 0, 3, 4})
	want := []int64{2, 0, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("unique values = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unique values = %#v, want %#v", got, want)
		}
	}
}

func TestParseStatusUpdatePayloadAcceptsFormPoll(t *testing.T) {
	e := echo.New()
	body := "status=hello&poll%5Boptions%5D%5B%5D=yes&poll%5Boptions%5D%5B%5D=%20no%20&poll%5Boptions%5D%5B%5D=&poll%5Bmultiple%5D=1&poll%5Bhide_totals%5D=true&poll%5Bexpires_in%5D=3600"
	req := httptest.NewRequest("POST", "/api/v1/statuses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseStatusUpdatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.HasStatus || payload.Status != "hello" {
		t.Fatalf("status = %#v", payload)
	}
	if !payload.HasPoll || payload.Poll == nil {
		t.Fatalf("poll missing: %#v", payload)
	}
	if len(payload.Poll.Options) != 2 || payload.Poll.Options[0] != "yes" || payload.Poll.Options[1] != "no" {
		t.Fatalf("options = %#v", payload.Poll.Options)
	}
	if !payload.Poll.Multiple || !payload.Poll.HideTotals || payload.Poll.ExpiresIn != 3600 {
		t.Fatalf("poll = %#v", payload.Poll)
	}
}

func TestPollPayloadFromFormValuesDetectsClearPollRequest(t *testing.T) {
	poll, ok := pollPayloadFromFormValues(map[string][]string{"poll[options][]": []string{""}})
	if !ok || poll == nil {
		t.Fatalf("poll = %#v ok = %v", poll, ok)
	}
	if len(poll.Options) != 0 {
		t.Fatalf("options = %#v", poll.Options)
	}
}

func TestValidatePollPayloadMatchesRailsPollValidators(t *testing.T) {
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	valid := &pollUpdatePayload{Options: []string{" yes ", "no"}, ExpiresIn: 5 * 60}
	if err := validatePollPayload(valid, now); err != nil {
		t.Fatalf("valid poll rejected: %v", err)
	}
	if len(valid.Options) != 2 || valid.Options[0] != "yes" || valid.Options[1] != "no" {
		t.Fatalf("options were not normalized: %#v", valid.Options)
	}
	cases := []struct {
		name    string
		payload *pollUpdatePayload
		want    string
	}{
		{name: "too few", payload: &pollUpdatePayload{Options: []string{"yes"}, ExpiresIn: 600}, want: "Validation failed: Options must have more than one item"},
		{name: "too many", payload: &pollUpdatePayload{Options: []string{"a", "b", "c", "d", "e"}, ExpiresIn: 600}, want: "Validation failed: Options can't contain more than 4 items"},
		{name: "too long", payload: &pollUpdatePayload{Options: []string{strings.Repeat("x", 51), "b"}, ExpiresIn: 600}, want: "Validation failed: Options are over the character limit"},
		{name: "duplicate", payload: &pollUpdatePayload{Options: []string{"a", "a"}, ExpiresIn: 600}, want: "Validation failed: Options contain duplicates"},
		{name: "too short", payload: &pollUpdatePayload{Options: []string{"a", "b"}, ExpiresIn: 299}, want: "Validation failed: Expires at is too soon"},
		{name: "too long duration", payload: &pollUpdatePayload{Options: []string{"a", "b"}, ExpiresIn: pollMaxExpirationSeconds + 1}, want: "Validation failed: Expires at is too far into the future"},
		{name: "missing duration", payload: &pollUpdatePayload{Options: []string{"a", "b"}}, want: "Validation failed: Expires at is too far into the future"},
	}
	for _, tt := range cases {
		err := validatePollPayload(tt.payload, now)
		apiErr, ok := err.(apiHTTPError)
		if !ok || apiErr.message != tt.want {
			t.Fatalf("%s error = %#v, want %q", tt.name, err, tt.want)
		}
	}
}
