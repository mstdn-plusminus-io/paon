package api

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type statusMentionSQLCapture struct {
	queries []string
}

func (capture *statusMentionSQLCapture) LogMode(logger.LogLevel) logger.Interface { return capture }
func (capture *statusMentionSQLCapture) Info(context.Context, string, ...any)     {}
func (capture *statusMentionSQLCapture) Warn(context.Context, string, ...any)     {}
func (capture *statusMentionSQLCapture) Error(context.Context, string, ...any)    {}

func (capture *statusMentionSQLCapture) Trace(_ context.Context, _ time.Time, query func() (string, int64), _ error) {
	sqlText, _ := query()
	capture.queries = append(capture.queries, sqlText)
}

func statusMentionDryRunDatabase(t *testing.T, capture *statusMentionSQLCapture) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=localhost user=paon dbname=paon",
		PreferSimpleProtocol: false,
	}), &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestStatusMentionBlockedByActorChecksIndividualAndDomainBlocks(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	database := statusMentionDryRunDatabase(t, capture)

	blocked, err := statusMentionBlockedByActor(database, 7, models.Account{
		ID:     9,
		Domain: sql.NullString{String: "blocked.example", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("dry-run block lookup unexpectedly reported a matching row")
	}

	queries := strings.Join(capture.queries, "\n")
	for _, want := range []string{
		`FROM "blocks"`,
		`account_id = 7 AND target_account_id = 9`,
		`FROM "account_domain_blocks"`,
		`lower(domain) = lower('blocked.example')`,
	} {
		if !strings.Contains(queries, want) {
			t.Fatalf("blocked mention lookup SQL missing %q:\n%s", want, queries)
		}
	}
}

func TestStatusMentionChangeIDsDropsBlockedAndSilencesOrdinaryRemovals(t *testing.T) {
	const (
		domainBlockedAccountID       = int64(101)
		individuallyBlockedAccountID = int64(102)
		removedAccountID             = int64(103)
		currentAccountID             = int64(104)
	)
	previous := []models.Mention{
		{ID: 11, AccountID: models.MentionAccountID(domainBlockedAccountID)},
		{ID: 12, AccountID: models.MentionAccountID(individuallyBlockedAccountID)},
		{ID: 13, AccountID: models.MentionAccountID(removedAccountID)},
		{ID: 14, AccountID: models.MentionAccountID(currentAccountID)},
		{ID: 15},
	}
	current := map[int64]struct{}{currentAccountID: {}}
	blocked := map[int64]struct{}{
		domainBlockedAccountID:       {},
		individuallyBlockedAccountID: {},
	}

	droppedIDs, removedIDs := statusMentionChangeIDs(previous, current, blocked)
	if !reflect.DeepEqual(droppedIDs, []int64{11, 12}) {
		t.Fatalf("dropped mention ids = %#v, want [11 12]", droppedIDs)
	}
	if !reflect.DeepEqual(removedIDs, []int64{13}) {
		t.Fatalf("silenced mention ids = %#v, want [13]", removedIDs)
	}
}

func TestApplyStatusMentionChangesDeletesBlockedRowsAndSilencesOrdinaryRemovals(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	database := statusMentionDryRunDatabase(t, capture)

	if err := applyStatusMentionChanges(database, []int64{11, 12}, []int64{13}); err != nil {
		t.Fatal(err)
	}
	queries := strings.Join(capture.queries, "\n")
	for _, want := range []string{
		`SELECT "id" FROM "mentions" WHERE id IN (11,12) FOR UPDATE`,
		`DELETE FROM "notifications"`,
		`activity_type = 'Mention' AND activity_id IN (11,12)`,
		`DELETE FROM "mentions" WHERE id IN (11,12)`,
		`UPDATE "mentions" SET "silent"=true`,
		`WHERE id IN (13)`,
	} {
		if !strings.Contains(queries, want) {
			t.Fatalf("mention change SQL missing %q:\n%s", want, queries)
		}
	}
	lockIndex := strings.Index(queries, `SELECT "id" FROM "mentions" WHERE id IN (11,12) FOR UPDATE`)
	notificationDeleteIndex := strings.Index(queries, `DELETE FROM "notifications"`)
	mentionDeleteIndex := strings.Index(queries, `DELETE FROM "mentions" WHERE id IN (11,12)`)
	if !(lockIndex < notificationDeleteIndex && notificationDeleteIndex < mentionDeleteIndex) {
		t.Fatalf("blocked mention changes must lock Mention rows before deleting notifications and mentions:\n%s", queries)
	}
}

func TestDroppedStatusMentionsAreExcludedFromActivityPubAudienceAndDelivery(t *testing.T) {
	domainBlocked := models.Account{
		ID:       101,
		Username: "domain_blocked",
		Domain:   sql.NullString{String: "blocked.example", Valid: true},
		URI:      "https://blocked.example/users/domain_blocked",
		InboxURL: "https://blocked.example/inbox/domain_blocked",
	}
	individuallyBlocked := models.Account{
		ID:       102,
		Username: "individually_blocked",
		Domain:   sql.NullString{String: "remote.example", Valid: true},
		URI:      "https://remote.example/users/individually_blocked",
		InboxURL: "https://remote.example/inbox/individually_blocked",
	}
	removed := models.Account{
		ID:       103,
		Username: "removed",
		Domain:   sql.NullString{String: "old.example", Valid: true},
		URI:      "https://old.example/users/removed",
		InboxURL: "https://old.example/inbox/removed",
	}
	current := models.Account{
		ID:       104,
		Username: "current",
		Domain:   sql.NullString{String: "current.example", Valid: true},
		URI:      "https://current.example/users/current",
		InboxURL: "https://current.example/inbox/current",
	}
	previous := []models.Mention{
		{ID: 11, AccountID: models.MentionAccountID(domainBlocked.ID), Account: domainBlocked},
		{ID: 12, AccountID: models.MentionAccountID(individuallyBlocked.ID), Account: individuallyBlocked},
		{ID: 13, AccountID: models.MentionAccountID(removed.ID), Account: removed},
		{ID: 14, AccountID: models.MentionAccountID(current.ID), Account: current},
	}
	droppedIDs, removedIDs := statusMentionChangeIDs(
		previous,
		map[int64]struct{}{current.ID: {}},
		map[int64]struct{}{domainBlocked.ID: {}, individuallyBlocked.ID: {}},
	)
	dropped := make(map[int64]struct{}, len(droppedIDs))
	for _, id := range droppedIDs {
		dropped[id] = struct{}{}
	}
	silenced := make(map[int64]struct{}, len(removedIDs))
	for _, id := range removedIDs {
		silenced[id] = struct{}{}
	}
	remaining := make([]models.Mention, 0, len(previous)-len(droppedIDs))
	for _, mention := range previous {
		if _, ok := dropped[mention.ID]; ok {
			continue
		}
		if _, ok := silenced[mention.ID]; ok {
			mention.Silent = true
		}
		remaining = append(remaining, mention)
	}

	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "local.example", LocalDomain: "local.example"}}
	status := models.Status{
		ID:         500,
		AccountID:  1,
		Account:    models.Account{ID: 1, Username: "author"},
		Visibility: 3,
		Mentions:   remaining,
	}
	to, cc := activityPubAudience(server, status)
	audience := strings.Join(append(to, cc...), "\n")
	inboxes, err := server.activityPubStatusRecipientInboxes(status)
	if err != nil {
		t.Fatal(err)
	}
	delivery := strings.Join(inboxes, "\n")
	for _, blockedValue := range []string{domainBlocked.URI, individuallyBlocked.URI, domainBlocked.InboxURL, individuallyBlocked.InboxURL} {
		if strings.Contains(audience+"\n"+delivery, blockedValue) {
			t.Fatalf("blocked mention remained in ActivityPub audience/delivery: %q\naudience:\n%s\ndelivery:\n%s", blockedValue, audience, delivery)
		}
	}
	if !strings.Contains(audience, current.URI) {
		t.Fatalf("current mention missing from ActivityPub audience: %s", audience)
	}
	if !strings.Contains(delivery, current.InboxURL) {
		t.Fatalf("current mention missing from ActivityPub delivery: %s", delivery)
	}
	if !strings.Contains(delivery, removed.InboxURL) {
		t.Fatalf("ordinary removed mention lost retained ActivityPub delivery access: %s", delivery)
	}
}

// The expectations below were checked against Rails Account::MENTION_RE
// (?<![=/[:word:]])@((USERNAME_RE)(?:@[[:word:]]+([.-]+[[:word:]]+)*)?) on
// Ruby 3.4, the runtime of the Rails version (.ruby-version 3.4.7).
func TestStatusMentionRefsIgnoreURLsLikeRailsMentionRE(t *testing.T) {
	for _, text := range []string{
		"https://h.example/@eve/1",
		"https://h.example/@alice@mastodon.social/1",
		"https://x.example/?u=@bob",
		"RE: https://h.example/@eve/123",
		"look\n\nRE: https://h.example/@alice@mastodon.social/109",
		"foo@bar",
		"a@b@c",
		"あ@eve",
		"０@eve",
		"Ⅻ@eve",
		"Ⓐ@eve",
		"🄰@eve",
		"‿@eve",
		"́@eve",
		"‍@eve",
	} {
		if refs := statusMentionRefs(text); len(refs) != 0 {
			t.Fatalf("statusMentionRefs(%q) = %#v, want no mentions", text, refs)
		}
	}

	for text, want := range map[string][]statusMentionRef{
		"@eve":                                  {{Username: "eve"}},
		"(@eve)":                                {{Username: "eve"}},
		"@eve@remote.example hello":             {{Username: "eve", Domain: "remote.example"}},
		"hello\n@eve@remote.example":            {{Username: "eve", Domain: "remote.example"}},
		"hello.@eve":                            {{Username: "eve"}},
		"hello,@eve":                            {{Username: "eve"}},
		"hello、@eve":                            {{Username: "eve"}},
		"x@@eve":                                {{Username: "eve"}},
		"-@eve":                                 {{Username: "eve"}},
		"²@eve":                                 {{Username: "eve"}},
		"①@eve":                                 {{Username: "eve"}},
		"see https://h.example/@eve/1 and @bob": {{Username: "bob"}},
	} {
		if got := statusMentionRefs(text); !reflect.DeepEqual(got, want) {
			t.Fatalf("statusMentionRefs(%q) = %#v, want %#v", text, got, want)
		}
	}
}

func TestStatusMentionRefsIgnoreAppendedQuoteURL(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "h.example", WebDomain: "h.example"}}
	local := models.Status{ID: 123, Account: models.Account{ID: 9, Username: "eve"}}
	remote := models.Status{
		ID:      456,
		URL:     sql.NullString{String: "https://mastodon.social/@alice/109", Valid: true},
		Account: models.Account{ID: 10, Username: "alice", Domain: sql.NullString{String: "mastodon.social", Valid: true}},
	}
	for _, quote := range []models.Status{local, remote} {
		// Mastodon 4.5+ stores quotes as Quote records and only renders the
		// "RE: <url>" fallback (TextFormatter#add_quote_fallback), but a quoted
		// status URL can still be typed or pasted into the text.
		text := "private reply @bob\n\nRE: " + server.quoteStatusURL(quote)
		want := []statusMentionRef{{Username: "bob"}}
		if got := statusMentionRefs(text); !reflect.DeepEqual(got, want) {
			t.Fatalf("statusMentionRefs(%q) = %#v, want only the typed mention %#v", text, got, want)
		}
	}
}

// Onigmo's [[:word:]] for UTF-8 strings is Alphabetic (L, Nl, Other_Alphabetic)
// plus Mark, Decimal_Number, Connector_Punctuation and Join_Control. This was
// verified to equal Ruby 3.4's /[[:word:]]/ for every code point at Unicode
// 15.0.0; the test keeps the hand-written RE2 class in statusMentionPattern in
// sync with Go's Unicode tables.
func TestStatusMentionPatternBoundaryMatchesRubyWordClass(t *testing.T) {
	rubyWord := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.Is(unicode.Nl, r) || unicode.Is(unicode.Other_Alphabetic, r) ||
			unicode.IsMark(r) || unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Pc, r) || unicode.Is(unicode.Join_Control, r)
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		want := !(r == '=' || r == '/' || rubyWord(r))
		if got := statusMentionPattern.MatchString(string(r) + "@eve"); got != want {
			t.Fatalf("mention after U+%04X matched = %v, want %v (Rails MENTION_RE boundary)", r, got, want)
		}
	}
}
