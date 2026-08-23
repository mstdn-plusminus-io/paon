package api

import (
	"database/sql"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestQuoteApprovalPolicyNamesMatchMastodon45(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{name: "public", want: quotePolicyPublic << 16},
		{name: "followers", want: quotePolicyFollowers << 16},
		{name: "nobody", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := quoteApprovalPolicyFromName(test.name)
			if !ok || got != test.want {
				t.Fatalf("quote policy %q = %#x, %v; want %#x, true", test.name, got, ok, test.want)
			}
			if gotName := quoteApprovalPolicyName(got); gotName != test.name {
				t.Fatalf("quote policy %#x name = %q, want %q", got, gotName, test.name)
			}
		})
	}
	if _, ok := quoteApprovalPolicyFromName("friends"); ok {
		t.Fatal("unsupported quote policy was accepted")
	}
}

func TestMastodon45QuoteMutationsAuthorizeApplicationTokensBeforeOwnership(t *testing.T) {
	source, err := os.ReadFile("official_quotes_45.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, functionName := range []string{"updateStatusInteractionPolicy", "revokeStatusQuote"} {
		body := functionBody(t, source, functionName)
		requireScopeAt := strings.Index(body, `s.requireAccessTokenScope(c, "write", "write:statuses")`)
		optionalAccountAt := strings.Index(body, "s.currentAccountForOptionalRequestToken(c)")
		ownershipAt := strings.Index(body, "account == nil ||")
		if requireScopeAt < 0 || optionalAccountAt < requireScopeAt || ownershipAt < optionalAccountAt {
			t.Fatalf("%s must let Doorkeeper authorize a client-credentials token before returning the ownership-policy 403: %s", functionName, body)
		}
		if strings.Contains(body, "requireAccountScope") {
			t.Fatalf("%s incorrectly maps a valid application token to require_user 422: %s", functionName, body)
		}
	}
}

func TestMastodon45QuoteAPIsRejectSuspendedResourceOwners(t *testing.T) {
	account := &models.Account{SuspendedAt: sql.NullTime{Valid: true}}
	if err := requireAvailableQuoteAPIAccount(nil, account); !apiErrorStatus(err, http.StatusForbidden) {
		t.Fatalf("suspended resource owner error = %v, want HTTP %d", err, http.StatusForbidden)
	}
	if err := requireAvailableQuoteAPIAccount(nil, nil); err != nil {
		t.Fatalf("application token without resource owner was rejected: %v", err)
	}
}

func TestQuotePolicyDecisionMatchesMastodon45Relationships(t *testing.T) {
	author := models.Account{ID: 10}
	viewer := &models.Account{ID: 20}
	status := models.Status{ID: 1, AccountID: author.ID, Account: author, Visibility: 0}

	status.QuoteApprovalPolicy = quotePolicyPublic << 16
	if got := quotePolicyDecisionWithRelations(status, viewer, false, false); got != quotePolicyAutomatic {
		t.Fatalf("public automatic policy = %q", got)
	}
	status.QuoteApprovalPolicy = quotePolicyFollowers << 16
	if got := quotePolicyDecisionWithRelations(status, viewer, true, false); got != quotePolicyAutomatic {
		t.Fatalf("followers automatic policy = %q", got)
	}
	status.QuoteApprovalPolicy = quotePolicyFollowing
	if got := quotePolicyDecisionWithRelations(status, viewer, false, true); got != quotePolicyManual {
		t.Fatalf("following manual policy = %q", got)
	}
	status.QuoteApprovalPolicy = quotePolicyUnknown
	if got := quotePolicyDecisionWithRelations(status, viewer, false, false); got != quotePolicyUndecided {
		t.Fatalf("unsupported policy = %q", got)
	}
	status.AccountID = viewer.ID
	status.Visibility = 2
	if got := quotePolicyDecisionWithRelations(status, viewer, false, false); got != quotePolicyAutomatic {
		t.Fatalf("private self-quote policy = %q", got)
	}
	status.Visibility = 3
	if got := quotePolicyDecisionWithRelations(status, viewer, false, false); got != quotePolicyDenied {
		t.Fatalf("direct self-quote policy = %q", got)
	}
}

func TestActivityPubQuoteInteractionPolicyUsesEachCollectionOnce(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	status := models.Status{
		QuoteApprovalPolicy: quotePolicyFollowers << 16,
		Account: models.Account{
			ID:           7,
			Username:     "alice",
			FollowersURL: "https://example.com/users/alice/followers",
		},
	}
	policy := activityPubQuoteInteractionPolicy(server, status)
	canQuote, ok := policy["canQuote"].(map[string]any)
	if !ok {
		t.Fatalf("canQuote = %#v", policy["canQuote"])
	}
	approved, ok := canQuote["automaticApproval"].([]string)
	if !ok || !reflect.DeepEqual(approved, []string{"https://example.com/users/alice/followers"}) {
		t.Fatalf("automaticApproval = %#v", canQuote["automaticApproval"])
	}
}

func TestLocalQuoteAuthorizationURIIsDerivedInsteadOfPersisted(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	quotedAccount := &models.Account{ID: 7, Username: "alice", IDScheme: sql.NullInt64{Int64: 1, Valid: true}}
	quote := &models.Quote{ID: 19, State: models.QuoteStateAccepted, QuotedAccount: quotedAccount}
	want := "https://example.com/ap/users/7/quote_authorizations/19"
	if got := activityPubQuoteApprovalURI(server, quote); got != want {
		t.Fatalf("local quote authorization URI = %q, want %q", got, want)
	}
	if quote.ApprovalURI.Valid {
		t.Fatal("local quote authorization URI was persisted")
	}
	quote.State = models.QuoteStatePending
	if got := activityPubQuoteApprovalURI(server, quote); got != "" {
		t.Fatalf("pending local quote authorization URI = %q", got)
	}
	quote.QuotedAccount = &models.Account{ID: 8, Username: "bob", Domain: sql.NullString{String: "remote.example", Valid: true}}
	quote.ApprovalURI = sql.NullString{String: "https://remote.example/invalid-but-nonempty", Valid: false}
	if got := activityPubQuoteApprovalURI(server, quote); got != "" {
		t.Fatalf("invalid remote quote authorization URI = %q", got)
	}
	quote.ApprovalURI = sqlNullString("https://remote.example/quote-authorizations/19")
	if got := activityPubQuoteApprovalURI(server, quote); got != quote.ApprovalURI.String {
		t.Fatalf("remote quote authorization URI = %q", got)
	}
}

func TestQuoteAuthorizationChecksMutualAndDomainBlocks(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	database := statusMentionDryRunDatabase(t, capture)
	server := &Server{db: database}
	quotedAuthor := &models.Account{ID: 10, Username: "bob"}
	quoter := &models.Account{
		ID:       20,
		Username: "alice",
		Domain:   sql.NullString{String: "remote.example", Valid: true},
	}

	blocked, err := server.quoteDeniedByAccountRelationship(quotedAuthor, quoter)
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("dry-run relationship lookup unexpectedly reported a block")
	}

	queries := strings.Join(capture.queries, "\n")
	for _, want := range []string{
		`account_id = 10 AND target_account_id = 20`,
		`account_id = 10 AND lower(domain) = 'remote.example'`,
		`account_id = 20 AND target_account_id = 10`,
	} {
		if !strings.Contains(queries, want) {
			t.Fatalf("quote authorization SQL missing %q:\n%s", want, queries)
		}
	}

	quoteSource, err := os.ReadFile("activitypub_quotes.go")
	if err != nil {
		t.Fatal(err)
	}
	requestBody := functionBody(t, quoteSource, "processActivityPubQuoteRequest")
	relationAt := strings.Index(requestBody, "quoteDeniedByAccountRelationship")
	policyAt := strings.Index(requestBody, "quotePolicyForAccount")
	if relationAt < 0 || policyAt < 0 || relationAt > policyAt {
		t.Fatal("incoming QuoteRequest does not enforce relationship blocks before quote policy")
	}
}

func TestFindQuotableStatusNormalizesBoostToProperStatus(t *testing.T) {
	source, err := os.ReadFile("official_quotes_45.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "findQuotableStatusForAccount")
	reblogAt := strings.Index(body, "candidate.ReblogOfID.Valid")
	visibleAt := strings.Index(body, "s.findVisibleStatusForAccount(viewer, id)")
	if reblogAt < 0 || visibleAt < 0 || reblogAt > visibleAt {
		t.Fatal("quoted boost is not normalized to its proper status before visibility/policy checks")
	}
	if !strings.Contains(body, "id = strconv.FormatInt(candidate.ReblogOfID.Int64, 10)") {
		t.Fatal("quoted boost does not resolve the original status ID")
	}
}

func TestQuotedAccountCanSeePrivateQuoteThroughSilentMention(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "visibleStatusQuery")
	if !strings.Contains(body, `[]int{2, 3, 4}, account.ID`) {
		t.Fatal("private statuses do not honor mentions, hiding accepted private quotes from the quoted account")
	}
}

func TestOfficialQuoteNotificationRequiresAcceptedLocalTarget(t *testing.T) {
	quoted := &models.Status{ID: 7, AccountID: 70, Account: models.Account{ID: 70, Username: "local"}}
	quote := &models.Quote{ID: 9, AccountID: 90, State: models.QuoteStateAccepted}
	payload, ok := officialQuoteNotificationPayload(quote, quoted)
	if !ok {
		t.Fatal("accepted local quote did not create a notification payload")
	}
	if payload.ReceiverAccountID != 70 || payload.FromAccountID != 90 || payload.ActivityID != 9 || payload.ActivityType != "Quote" || payload.Type != "quote" {
		t.Fatalf("quote notification payload = %#v", payload)
	}
	quote.State = models.QuoteStatePending
	if _, ok := officialQuoteNotificationPayload(quote, quoted); ok {
		t.Fatal("pending quote created a notification payload")
	}
	quote.State = models.QuoteStateAccepted
	quoted.Account.Domain = sql.NullString{String: "remote.example", Valid: true}
	if _, ok := officialQuoteNotificationPayload(quote, quoted); ok {
		t.Fatal("remote target quote created a local notification payload")
	}
}

func TestOfficialQuoteCounterTransitionsStayTransactional(t *testing.T) {
	accepted := func(target int64) quoteCounterSnapshot {
		return quoteCounterSnapshot{Exists: true, QuotedStatusID: sql.NullInt64{Int64: target, Valid: true}, State: models.QuoteStateAccepted}
	}
	legacyAccepted := func(target int64) quoteCounterSnapshot {
		return quoteCounterSnapshot{Exists: true, QuotedStatusID: sql.NullInt64{Int64: target, Valid: true}, State: models.QuoteStateAccepted, Legacy: true}
	}
	pending := func(target int64) quoteCounterSnapshot {
		return quoteCounterSnapshot{Exists: true, QuotedStatusID: sql.NullInt64{Int64: target, Valid: true}, State: models.QuoteStatePending}
	}
	tests := []struct {
		name         string
		before       quoteCounterSnapshot
		after        quoteCounterSnapshot
		wantDecrease int64
		wantIncrease int64
	}{
		{name: "accept", before: pending(10), after: accepted(10), wantIncrease: 10},
		{name: "attach accepted target", before: quoteCounterSnapshot{Exists: true, State: models.QuoteStateAccepted}, after: accepted(10), wantIncrease: 10},
		{name: "revoke", before: accepted(10), after: pending(10), wantDecrease: 10},
		{name: "replace target", before: accepted(10), after: accepted(20), wantDecrease: 10, wantIncrease: 20},
		{name: "delete", before: accepted(10), after: quoteCounterSnapshot{}, wantDecrease: 10},
		{name: "legacy create still counts", before: quoteCounterSnapshot{}, after: legacyAccepted(10), wantIncrease: 10},
		{name: "legacy delete still counts", before: legacyAccepted(10), after: quoteCounterSnapshot{}, wantDecrease: 10},
		{name: "idempotent", before: accepted(10), after: accepted(10)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decrease, increase := quoteCounterTransitionTargets(test.before, test.after)
			if decrease != test.wantDecrease || increase != test.wantIncrease {
				t.Fatalf("counter transition = (%d, %d), want (%d, %d)", decrease, increase, test.wantDecrease, test.wantIncrease)
			}
		})
	}
	if decrease, increase := quoteUpdateCounterTransitionTargets(legacyAccepted(10), quoteCounterSnapshot{Exists: true, QuotedStatusID: sql.NullInt64{Int64: 10, Valid: true}, State: models.QuoteStatePending, Legacy: true}); decrease != 0 || increase != 0 {
		t.Fatalf("legacy after-update counter transition = (%d, %d), want (0, 0)", decrease, increase)
	}
	if decrease, increase := quoteUpdateCounterTransitionTargets(pending(10), accepted(10)); decrease != 0 || increase != 10 {
		t.Fatalf("official after-update counter transition = (%d, %d), want (0, 10)", decrease, increase)
	}

	quoteSource, err := os.ReadFile("activitypub_quotes.go")
	if err != nil {
		t.Fatal(err)
	}
	reconcile := functionBody(t, quoteSource, "reconcileActivityPubQuote")
	if strings.Contains(reconcile, `Delete(&models.Quote{})`) || !strings.Contains(reconcile, "deleteSQLStatusQuoteWithCounter") {
		t.Fatal("ActivityPub quote replacement/removal bypasses the transactional quotes_count path")
	}
	transition := functionBody(t, quoteSource, "transitionQuoteState")
	if !strings.Contains(transition, "if quote.Legacy") {
		t.Fatal("legacy quote state transitions must not mutate quotes_count")
	}
}

func TestActivityContextUsesCanonicalMastodon45QuoteTerms(t *testing.T) {
	contexts := activityContext()
	if len(contexts) != 2 {
		t.Fatalf("activity context = %#v", contexts)
	}
	extension, ok := contexts[1].(map[string]any)
	if !ok {
		t.Fatalf("activity context extension = %#v", contexts[1])
	}
	if extension["quoteUri"] != "http://fedibird.com/ns#quoteUri" {
		t.Fatalf("quoteUri term = %#v", extension["quoteUri"])
	}
	if extension["_misskey_quote"] != "https://misskey-hub.net/ns#_misskey_quote" {
		t.Fatalf("_misskey_quote term = %#v", extension["_misskey_quote"])
	}
	for _, term := range []string{"quote", "quoteAuthorization", "interactingObject", "interactionTarget"} {
		definition, ok := extension[term].(map[string]any)
		if !ok || definition["@type"] != "@id" {
			t.Fatalf("%s term = %#v, want an @id-typed definition", term, extension[term])
		}
	}
	if extension["QuoteAuthorization"] != "fep:QuoteAuthorization" {
		t.Fatalf("QuoteAuthorization term = %#v", extension["QuoteAuthorization"])
	}
}

func TestParseActivityPayloadKeepsInlineQuoteRequestInstrument(t *testing.T) {
	payload, err := parseActivityPayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/quote_requests/1",
		"type":"QuoteRequest",
		"actor":"https://remote.example/users/alice",
		"object":"https://example.com/users/bob/statuses/2",
		"instrument":{
			"id":"https://remote.example/users/alice/statuses/3",
			"type":"Note",
			"attributedTo":"https://remote.example/users/alice",
			"content":"quoted"
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Instrument != "https://remote.example/users/alice/statuses/3" || payload.InstrumentObject == nil {
		t.Fatalf("inline quote instrument = %q, %#v", payload.Instrument, payload.InstrumentObject)
	}
	if payload.InstrumentObject.ID != payload.Instrument || payload.InstrumentObject.TypeExact != "Note" {
		t.Fatalf("parsed inline instrument = %#v", payload.InstrumentObject)
	}
}

func TestParseActivityPayloadKeepsEmbeddedQuoteRequestReferences(t *testing.T) {
	payload, err := parseActivityPayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/accepts/1",
		"type":"Accept",
		"actor":"https://remote.example/users/alice",
		"object":{
			"id":"https://local.example/users/bob/quote_requests/2",
			"type":"QuoteRequest",
			"actor":"https://local.example/users/bob",
			"object":"https://remote.example/users/alice/statuses/3",
			"instrument":"https://local.example/users/bob/statuses/4"
		},
		"result":"https://remote.example/users/alice/quote_authorizations/5"
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Object.TypeExact != "QuoteRequest" || payload.Object.ObjectID != "https://remote.example/users/alice/statuses/3" || payload.Object.Instrument != "https://local.example/users/bob/statuses/4" {
		t.Fatalf("embedded QuoteRequest = %#v", payload.Object)
	}
}

func TestStatusTextExplicitlyMentionsQuotedAccount(t *testing.T) {
	server := &Server{cfg: config.Config{LocalDomain: "example.com", WebDomain: "social.example.com"}}
	local := &models.Account{ID: 1, Username: "bob"}
	remote := &models.Account{ID: 2, Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}}

	for _, text := range []string{"hi @bob", "hi @bob@example.com", "hi @bob@social.example.com"} {
		if !server.statusTextExplicitlyMentionsAccount(text, local) {
			t.Fatalf("local account was not recognized in %q", text)
		}
	}
	if !server.statusTextExplicitlyMentionsAccount("hi @alice@remote.example", remote) {
		t.Fatal("remote account was not recognized")
	}
	for _, text := range []string{"hi @alice", "hi @alice@other.example", "hi @bob@remote.example"} {
		if server.statusTextExplicitlyMentionsAccount(text, remote) {
			t.Fatalf("remote account unexpectedly matched %q", text)
		}
	}
}

func TestFEP7888ContextIdentifiersAndPageShape(t *testing.T) {
	accountID, statusID, ok := parseActivityPubContextID("12-34")
	if !ok || accountID != 12 || statusID != 34 {
		t.Fatalf("context ID = %d, %d, %v", accountID, statusID, ok)
	}
	for _, invalid := range []string{"", "12", "12-34-56", "a-34", "12-0"} {
		if _, _, ok := parseActivityPubContextID(invalid); ok {
			t.Fatalf("invalid context ID %q was accepted", invalid)
		}
	}
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	conversation := models.Conversation{
		ParentAccountID: sql.NullInt64{Int64: 12, Valid: true},
		ParentStatusID:  sql.NullInt64{Int64: 34, Valid: true},
	}
	items := []any{"https://example.com/users/alice/statuses/34"}
	page := server.activityPubContextPage(conversation, items, "https://example.com/contexts/12-34/items?page=true&min_id=34", "")
	if page["type"] != "CollectionPage" || page["partOf"] != "https://example.com/contexts/12-34" || !reflect.DeepEqual(page["items"], items) {
		t.Fatalf("FEP-7888 page = %#v", page)
	}
	if _, exists := page["id"]; exists {
		t.Fatalf("embedded first page unexpectedly has id: %#v", page)
	}
	collection := server.activityPubContextCollection(conversation, items, "")
	if _, exists := collection["attributedTo"]; exists {
		t.Fatalf("deleted root account leaked attributedTo: %#v", collection)
	}
	conversation.ParentAccount = &models.Account{ID: 12, Username: "alice"}
	collection = server.activityPubContextCollection(conversation, items, "")
	if collection["attributedTo"] != "https://example.com/users/alice" {
		t.Fatalf("context attributedTo = %#v", collection["attributedTo"])
	}
}

func TestFEP7888ConversationOwnershipAndRemoteRootAssignment(t *testing.T) {
	contextSource, err := os.ReadFile("fep_7888_45.go")
	if err != nil {
		t.Fatal(err)
	}
	lookup := functionBody(t, contextSource, "activityPubContextConversation")
	if !strings.Contains(lookup, `uri IS NULL AND parent_account_id = ? AND parent_status_id = ?`) {
		t.Fatal("FEP-7888 endpoint does not use Mastodon's Conversation.local ownership boundary")
	}
	if strings.Contains(lookup, "!conversation.ParentAccount.Local()") {
		t.Fatal("FEP-7888 endpoint incorrectly rejects locally-owned conversations rooted by remote accounts")
	}
	if strings.Contains(lookup, "conversation.ParentAccount == nil") {
		t.Fatal("FEP-7888 endpoint rejects a conversation after its root account is deleted")
	}

	inboxSource, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	create := functionBody(t, inboxSource, "processActivityPubCreateNote")
	createdAt := strings.Index(create, "tx.Omit(clause.Associations).Create(&status)")
	parentAt := strings.Index(create, "s.assignConversationParentTx(tx, status)")
	if createdAt < 0 || parentAt < 0 || parentAt < createdAt {
		t.Fatal("remote root status creation does not assign the conversation parent after obtaining its status ID")
	}
}

func TestMastodon45QuoteAndFEP7888RouteAliases(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		`e.PATCH("/api/v1/statuses/:id/interaction_policy", s.updateStatusInteractionPolicy)`,
		`e.GET("/contexts/:id.:format", s.activityPubContext)`,
		`e.GET("/contexts/:id/items.:format", s.activityPubContextItems)`,
	} {
		if indexBytes(source, []byte(route)) < 0 {
			t.Fatalf("server routes missing %s", route)
		}
	}
}

func TestMastodon45ConvertedArticleTextAndUpdatePath(t *testing.T) {
	article := activityObject{
		ID:      "https://remote.example/articles/1",
		URL:     "https://remote.example/articles/1",
		Name:    "Future & the Fediverse",
		Summary: "<p>Guest article by Jane Mastodon</p>",
	}
	got := activityPubConvertedStatusText(article)
	wantPrefix := "<h2>Future &amp; the Fediverse</h2>\n\n<p>Guest article by Jane Mastodon</p>\n\n"
	if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("converted Article text = %q, want prefix %q", got, wantPrefix)
	}

	source, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "processActivityPubUpdate")
	if indexBytes([]byte(body), []byte("if activityObjectIsConvertedStatus(object)")) >= 0 {
		t.Fatal("converted objects are still discarded before the status Update path")
	}
	if indexBytes([]byte(body), []byte("if activityObjectIsStatus(object)")) < 0 {
		t.Fatal("converted objects do not reach the shared status Update path")
	}
}

func TestAsyncReplyRefreshRegistersJobsBeforeEnqueue(t *testing.T) {
	source, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "enqueueFetchReplyTask")
	add := []byte("s.addAsyncRefreshPendingJob(ctx, asyncRefreshKey)")
	enqueue := []byte("s.asynqClient.EnqueueContext(ctx, task)")
	if addAt, enqueueAt := indexBytes([]byte(body), add), indexBytes([]byte(body), enqueue); addAt < 0 || enqueueAt < 0 || addAt > enqueueAt {
		t.Fatalf("async refresh job registration must precede enqueue: %s", body)
	}
}

func indexBytes(haystack []byte, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if reflect.DeepEqual(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}
