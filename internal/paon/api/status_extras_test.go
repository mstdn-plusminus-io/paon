package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestStatusSnapshotEditUsesEditedStatusState(t *testing.T) {
	editedAt := time.Date(2026, 6, 18, 13, 0, 0, 0, time.UTC)
	status := models.Status{
		ID:          100,
		AccountID:   42,
		Text:        "current",
		SpoilerText: "cw",
		Sensitive:   true,
		CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		EditedAt:    sql.NullTime{Time: editedAt, Valid: true},
		Account:     models.Account{ID: 42, Username: "alice"},
		MediaAttachments: []models.MediaAttachment{
			{ID: 3, Description: sql.NullString{String: "first", Valid: true}},
			{ID: 4, Description: sql.NullString{String: "second", Valid: true}},
		},
		Poll: &models.Poll{Options: models.StringArray{"yes", "no"}},
	}

	edit := statusSnapshotEdit(status)
	if edit.StatusID != 100 || edit.AccountID.Int64 != 42 {
		t.Fatalf("unexpected ids: %#v", edit)
	}
	if !edit.CreatedAt.Equal(editedAt) {
		t.Fatalf("CreatedAt = %s", edit.CreatedAt)
	}
	if !edit.Sensitive.Valid || !edit.Sensitive.Bool {
		t.Fatalf("Sensitive = %#v", edit.Sensitive)
	}
	if len(edit.OrderedMediaAttachmentIDs) != 2 || edit.OrderedMediaAttachmentIDs[1] != 4 {
		t.Fatalf("OrderedMediaAttachmentIDs = %#v", edit.OrderedMediaAttachmentIDs)
	}
	if len(edit.MediaDescriptions) != 2 || edit.MediaDescriptions[0] != "first" {
		t.Fatalf("MediaDescriptions = %#v", edit.MediaDescriptions)
	}
	if len(edit.PollOptions) != 2 || edit.PollOptions[1] != "no" {
		t.Fatalf("PollOptions = %#v", edit.PollOptions)
	}
}

func TestStatusSnapshotEditUsesOrderedMediaAttachmentIDs(t *testing.T) {
	status := models.Status{
		ID:                        100,
		AccountID:                 42,
		Text:                      "current",
		CreatedAt:                 time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Account:                   models.Account{ID: 42, Username: "alice"},
		OrderedMediaAttachmentIDs: models.Int64Array{4},
		MediaAttachments: []models.MediaAttachment{
			{ID: 3, Description: sql.NullString{String: "old", Valid: true}},
			{ID: 4, Description: sql.NullString{String: "kept", Valid: true}},
		},
	}

	edit := statusSnapshotEdit(status)
	if len(edit.OrderedMediaAttachmentIDs) != 1 || edit.OrderedMediaAttachmentIDs[0] != 4 {
		t.Fatalf("OrderedMediaAttachmentIDs = %#v", edit.OrderedMediaAttachmentIDs)
	}
	if len(edit.MediaDescriptions) != 1 || edit.MediaDescriptions[0] != "kept" {
		t.Fatalf("MediaDescriptions = %#v", edit.MediaDescriptions)
	}
}

func TestStatusSnapshotEditSortsLegacyMediaAttachmentsByID(t *testing.T) {
	status := models.Status{
		ID:        100,
		AccountID: 42,
		Text:      "current",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Account:   models.Account{ID: 42, Username: "alice"},
		MediaAttachments: []models.MediaAttachment{
			{ID: 9, Description: sql.NullString{String: "later", Valid: true}},
			{ID: 4, Description: sql.NullString{String: "earlier", Valid: true}},
		},
	}

	edit := statusSnapshotEdit(status)
	if len(edit.OrderedMediaAttachmentIDs) != 2 || edit.OrderedMediaAttachmentIDs[0] != 4 || edit.OrderedMediaAttachmentIDs[1] != 9 {
		t.Fatalf("OrderedMediaAttachmentIDs = %#v", edit.OrderedMediaAttachmentIDs)
	}
	if len(edit.MediaDescriptions) != 2 || edit.MediaDescriptions[0] != "earlier" || edit.MediaDescriptions[1] != "later" {
		t.Fatalf("MediaDescriptions = %#v", edit.MediaDescriptions)
	}
}

func TestOrderedEditMediaAttachmentsUsesStoredOrderAndDescriptions(t *testing.T) {
	edit := models.StatusEdit{
		OrderedMediaAttachmentIDs: models.Int64Array{4, 3},
		MediaDescriptions:         models.StringArray{"new second", "new first"},
	}
	media := []models.MediaAttachment{
		{ID: 3, Description: sql.NullString{String: "old first", Valid: true}},
		{ID: 4, Description: sql.NullString{String: "old second", Valid: true}},
	}

	ordered := orderedEditMediaAttachments(edit, media)
	if len(ordered) != 2 {
		t.Fatalf("len = %d", len(ordered))
	}
	if ordered[0].ID != 4 || ordered[0].Description.String != "new second" {
		t.Fatalf("first = %#v", ordered[0])
	}
	if ordered[1].ID != 3 || ordered[1].Description.String != "new first" {
		t.Fatalf("second = %#v", ordered[1])
	}
}

func TestOrderedEditMediaAttachmentsSortsLegacyNilOrderByID(t *testing.T) {
	ordered := orderedEditMediaAttachments(models.StatusEdit{}, []models.MediaAttachment{{ID: 9}, {ID: 4}})
	if len(ordered) != 2 || ordered[0].ID != 4 || ordered[1].ID != 9 {
		t.Fatalf("ordered = %#v", ordered)
	}
}

func TestActivityPubAndTranslationMediaAttachmentsSortLegacyNilOrderByID(t *testing.T) {
	status := models.Status{MediaAttachments: []models.MediaAttachment{{ID: 9}, {ID: 4}}}
	ap := activityPubOrderedMediaAttachments(status)
	if len(ap) != 2 || ap[0].ID != 4 || ap[1].ID != 9 {
		t.Fatalf("ActivityPub ordered media = %#v", ap)
	}
	tr := orderedTranslationMediaAttachments(status)
	if len(tr) != 2 || tr[0].ID != 4 || tr[1].ID != 9 {
		t.Fatalf("translation ordered media = %#v", tr)
	}
}

func TestStatusEditEmojiTextAndOrderingMatchRailsBoundaries(t *testing.T) {
	edit := models.StatusEdit{
		Text:        "hello :party: mid:skip: :wave:",
		SpoilerText: "cw :party:",
	}
	shortcodes := statusEmbedEmojiShortcodes(statusEditEmojiText(edit))
	if len(shortcodes) != 2 || shortcodes[0] != "party" || shortcodes[1] != "wave" {
		t.Fatalf("shortcodes = %#v", shortcodes)
	}
	ordered := orderCustomEmojisByShortcode(shortcodes, []models.CustomEmoji{
		{ID: 2, Shortcode: "wave"},
		{ID: 1, Shortcode: "party"},
		{ID: 3, Shortcode: "unused"},
	})
	if len(ordered) != 2 || ordered[0].Shortcode != "party" || ordered[1].Shortcode != "wave" {
		t.Fatalf("ordered = %#v", ordered)
	}
}

func TestStatusEditCustomEmojisUseSharedCaseInsensitiveDomainLookup(t *testing.T) {
	src, err := os.ReadFile("status_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "statusEditCustomEmojis", `query := customEmojiDomainQuery(s.db.Where("shortcode IN ? AND disabled = false", shortcodes), status.Account.Domain)`) {
		t.Fatal("status edit emoji lookup must use shared custom emoji domain query")
	}
	if functionBodyContains(t, src, "statusEditCustomEmojis", `query = query.Where("domain = ?", strings.ToLower(strings.TrimSpace(status.Account.Domain.String)))`) {
		t.Fatal("status edit emoji lookup must not use exact domain comparison")
	}
}

func TestStatusAndAccountEmojiTextMatchRailsFields(t *testing.T) {
	status := models.Status{
		Text:        "hello :party:",
		SpoilerText: "cw :wave:",
		Account:     models.Account{DisplayName: "Display :ignore:"},
		Poll:        &models.Poll{Options: models.StringArray{"yes :vote:", "no"}},
	}
	shortcodes := statusEmbedEmojiShortcodes(statusEmojiText(status))
	if len(shortcodes) != 3 || shortcodes[0] != "wave" || shortcodes[1] != "party" || shortcodes[2] != "vote" {
		t.Fatalf("status shortcodes = %#v", shortcodes)
	}

	account := models.Account{
		Note:        "note :note:",
		DisplayName: "Alice :name:",
		Fields:      []byte(`[{"name":"site :field_name:","value":"https://example.test :field_value:"}]`),
	}
	shortcodes = statusEmbedEmojiShortcodes(accountEmojiText(account))
	if len(shortcodes) != 4 || shortcodes[0] != "note" || shortcodes[1] != "name" || shortcodes[2] != "field_name" || shortcodes[3] != "field_value" {
		t.Fatalf("account shortcodes = %#v", shortcodes)
	}
}

func TestRESTStatusAndAccountCustomEmojiHydrationPathsAreWired(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"findStatus": {
			`err = s.hydrateStatusCustomEmojis(&status)`,
		},
		"findVisibleStatusForAccount": {
			`err = s.hydrateStatusCustomEmojis(&status)`,
		},
		"hydrateStatusRelationships": {
			`if err := s.hydrateStatusesCustomEmojis(statuses); err != nil`,
		},
		"findAccountByID": {
			`err = s.hydrateAccountCustomEmojis(&account)`,
		},
		"findAccountByUsernameDomainTx": {
			`err = s.hydrateAccountCustomEmojis(&account)`,
		},
	}
	for fn, wants := range checks {
		for _, want := range wants {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s missing %q", fn, want)
			}
		}
	}
}

func TestStatusCardAttemptsLazyPreviewCardFetch(t *testing.T) {
	src, err := os.ReadFile("status_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`c.Response().Header().Set("Vary", "Authorization")`,
		`publicRESTCacheIfUnauthenticated(c, 15)`,
		`_ = s.fetchLinkCardForStatus(c.Request().Context(), status.ID)`,
		`status, _, err = s.findVisibleStatusForRequest(c, c.Param("id"))`,
	} {
		if !functionBodyContains(t, src, "statusCard", want) {
			t.Fatalf("statusCard does not contain %q", want)
		}
	}
}

func TestRequireStatusReadScopeRejectsMissingDatabaseToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/statuses/123/source", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	err := s.requireStatusReadScope(c)
	if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestAuthorizeTokenScopeIfPresentAllowsAnonymousRequests(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/statuses/123", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		t.Fatalf("error = %#v", err)
	}
}

func TestAuthorizeTokenScopeIfPresentRejectsBearerWithoutDatabaseToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/statuses/123", nil)
	req.Header.Set("Authorization", "Bearer missing")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses")
	if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestStatusReadScopeNames(t *testing.T) {
	if !tokenHasAnyScope("read", "read", "read:statuses") {
		t.Fatal("read should cover status source")
	}
	if !tokenHasAnyScope("read:statuses", "read", "read:statuses") {
		t.Fatal("read:statuses should cover status source")
	}
	if tokenHasAnyScope("read:notifications", "read", "read:statuses") {
		t.Fatal("read:notifications should not cover status source")
	}
}

func TestStatusContextDescendantDepthMatchesRailsAuthenticatedUnlimited(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`descendantsDepthLimit := -1`,
		`descendantsDepthLimit = anonymousDescendantsDepthLimit`,
		`descendants, err := s.statusDescendants(*status, descendantsLimit, descendantsDepthLimit, account)`,
	} {
		if !functionBodyContains(t, src, "statusContext", want) {
			t.Fatalf("statusContext must match Rails authenticated unlimited descendant depth; missing %q", want)
		}
	}
	for _, want := range []string{
		`if depthLimit == 0 {`,
	} {
		if !functionBodyContains(t, src, "statusDescendants", want) {
			t.Fatalf("statusDescendants must treat negative depth as Rails nil depth limit; missing %q", want)
		}
	}
	if !strings.Contains(string(src), `WHERE (? < 0 OR depth < ?)`) {
		t.Fatal("statusDescendants must treat negative depth as Rails nil depth limit")
	}
}

func TestStatusPinUsesRailsFindBeforeOwnershipValidation(t *testing.T) {
	src, err := os.ReadFile("status_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`status, err := s.findStatus(c.Param("id"))`,
		`if status.AccountID != account.ID {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: You can only pin your own posts")`,
	} {
		if !functionBodyContains(t, src, "toggleStatusPin", want) {
			t.Fatalf("toggleStatusPin must match Rails StatusPin validation flow; missing %q", want)
		}
	}
	if functionBodyContains(t, src, "toggleStatusPin", `findVisibleStatusForAccount(account, c.Param("id"))`) {
		t.Fatal("toggleStatusPin must use Rails Status.find semantics before StatusPin ownership validation")
	}
}

func TestStatusPinDuplicateMatchesRailsRecordNotUnique(t *testing.T) {
	src, err := os.ReadFile("status_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.db.Create(&models.StatusPin{AccountID: account.ID, StatusID: status.ID`,
		`if isUniqueConstraintError(err) {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Duplicate record")`,
		`changed = true`,
	} {
		if !functionBodyContains(t, src, "toggleStatusPin", want) {
			t.Fatalf("toggleStatusPin must match Rails duplicate StatusPin handling; missing %q", want)
		}
	}
	if functionBodyContains(t, src, "toggleStatusPin", "OnConflict") || functionBodyContains(t, src, "toggleStatusPin", "DoNothing") {
		t.Fatal("toggleStatusPin must not silently ignore duplicate StatusPin rows")
	}
}

func TestStatusPinLimitOnlyAppliesToLocalAccountsLikeRails(t *testing.T) {
	src, err := os.ReadFile("status_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if account.Local() && count > 4 {`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: You have reached the pin limit")`,
	} {
		if !functionBodyContains(t, src, "toggleStatusPin", want) {
			t.Fatalf("toggleStatusPin must match Rails StatusPinValidator local-only limit; missing %q", want)
		}
	}
	if functionBodyContains(t, src, "toggleStatusPin", `if count > 4 {`) {
		t.Fatal("toggleStatusPin must not apply the pin limit to remote accounts")
	}
}

func TestStatusInteractionListsUseRailsMaxIDPaginationOnly(t *testing.T) {
	src, err := os.ReadFile("status_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"favouritedBy": {
			`accounts.suspended_at IS NULL`,
			`if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id")`,
			`if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id")`,
			`query = query.Order("favourites.id DESC")`,
			`limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue)`,
		},
		"rebloggedBy": {
			`accounts.suspended_at IS NULL`,
			`if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id")`,
			`if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id")`,
			`query = query.Order("statuses.id DESC")`,
			`limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue)`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("status_extras.go:%s does not contain %q", fn, want)
			}
		}
		for _, forbidden := range []string{
			`QueryParam("min_id")`,
			`Order("favourites.id ASC")`,
			`Order("statuses.id ASC")`,
		} {
			if functionBodyContains(t, src, fn, forbidden) {
				t.Fatalf("status_extras.go:%s should not contain unsupported Rails fragment %q", fn, forbidden)
			}
		}
	}
}

func TestOptionalTokenReadEndpointsCheckScopes(t *testing.T) {
	type check struct {
		fn   string
		want string
	}
	checks := map[string][]check{
		"server.go": {
			{"getStatus", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil`},
			{"publicTimeline", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil`},
			{"tagTimeline", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil`},
			{"accountStatuses", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil`},
			{"statusContext", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil`},
		},
		"status_extras.go": {
			{"statusHistory", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil`},
			{"statusCard", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil`},
			{"favouritedBy", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil`},
			{"rebloggedBy", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil`},
		},
		"polls.go": {
			{"getPoll", `if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil`},
		},
	}
	for file, fileChecks := range checks {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range fileChecks {
			if !functionBodyContains(t, src, check.fn, check.want) {
				t.Fatalf("%s:%s does not contain %q", file, check.fn, check.want)
			}
		}
	}
}

func TestStatusInteractionAccountListsApplyCurrentAccountExclusions(t *testing.T) {
	src, err := os.ReadFile("status_extras.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"favouritedBy", "rebloggedBy"} {
		if !functionBodyContains(t, src, fn, `current, err := s.currentAccountForOptionalRequestToken(c)`) {
			t.Fatalf("%s does not resolve current account for optional bearer tokens", fn)
		}
		if !functionBodyContains(t, src, fn, `query = excludeAccountsFromInteractionList(query, current)`) {
			t.Fatalf("%s does not apply Rails relationship exclusions", fn)
		}
	}
	for label, want := range map[string]string{
		"blocking":   `interaction_blocks.account_id = ?`,
		"blocked_by": `interaction_blocked_by.target_account_id = ?`,
		"mutes":      `interaction_mutes.account_id = ?`,
	} {
		if !functionBodyContains(t, src, "excludeAccountsFromInteractionList", want) {
			t.Fatalf("missing %s exclusion %q", label, want)
		}
	}
	if functionBodyContains(t, src, "excludeAccountsFromInteractionList", `account_domain_blocks`) {
		t.Fatal("status interaction lists must match Rails excluded_from_timeline_account_ids and not add domain-block filtering")
	}
}

func TestStatusContextAppliesRailsStatusFilterRelations(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"visibleStatusQuery", `visible_status_accounts.suspended_at IS NULL`},
		{"visibleStatusQuery", `visible_status_author_domain_blocks.account_id = statuses.account_id`},
		{"applyStatusContextFilterQuery", `context_status_mutes.account_id = ?`},
		{"applyStatusContextFilterQuery", `context_status_domain_blocks.account_id = ?`},
		{"applyStatusContextFilterQuery", `context_status_silenced_accounts.silenced_at IS NOT NULL`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("server.go:%s does not contain %q", check.fn, check.want)
		}
	}
}
