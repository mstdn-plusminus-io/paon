package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestFindVisibleStatusHydratesSQLQuoteAndVisibility(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.hydrateStatusQuote(&status)`,
		`s.hydrateQuoteVisibility(visibilityStatuses, account)`,
		`status = visibilityStatuses[0]`,
	} {
		if !functionBodyContains(t, src, "findVisibleStatusForAccount", want) {
			t.Fatalf("findVisibleStatusForAccount does not contain %q", want)
		}
	}
}

type statusQuoteRoundTripFunc func(*http.Request) (*http.Response, error)

func (f statusQuoteRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeStatusQuoteStore struct {
	quotes     map[string]statusQuote
	getManyIDs []string
}

func (f *fakeStatusQuoteStore) GetMany(_ context.Context, statusIDs []string) (map[string]statusQuote, error) {
	f.getManyIDs = append(f.getManyIDs, statusIDs...)
	quotes := make(map[string]statusQuote)
	for _, statusID := range statusIDs {
		if quote, ok := f.quotes[statusID]; ok {
			quotes[statusID] = quote
		}
	}
	return quotes, nil
}

func TestNewStatusQuoteStoreUsesDynamoidTableName(t *testing.T) {
	var gotHost string
	var gotPath string
	client := &http.Client{Transport: statusQuoteRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotHost = req.URL.Host
		gotPath = req.URL.Path
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: req}, nil
	})}
	store, err := newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBAccessKey: "access",
		DynamoDBSecretKey: "secret",
		DynamoDBNamespace: "paon-prod",
		DynamoDBRegion:    "us-west-2",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	dynamo, ok := store.(*dynamoDBStatusQuoteStore)
	if !ok {
		t.Fatalf("store = %#v", store)
	}
	if dynamo.tableName != "paon-prod_status_quotes" {
		t.Fatalf("tableName = %q", dynamo.tableName)
	}
	if _, err := dynamo.GetMany(context.Background(), []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if gotHost != "dynamodb.us-west-2.amazonaws.com" || gotPath != "/" {
		t.Fatalf("AWS SDK DynamoDB endpoint host=%q path=%q", gotHost, gotPath)
	}

	gotHost = ""
	gotPath = ""
	store, err = newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBAccessKey: "access",
		DynamoDBSecretKey: "secret",
		DynamoDBNamespace: "paon-prod",
		DynamoDBRegion:    "us-west-2",
		DynamoDBEndpoint:  "http://dynamodb.test/",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	dynamo, ok = store.(*dynamoDBStatusQuoteStore)
	if !ok {
		t.Fatalf("store = %#v", store)
	}
	if _, err := dynamo.GetMany(context.Background(), []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if gotHost != "dynamodb.test" || gotPath != "/" {
		t.Fatalf("AWS SDK custom DynamoDB endpoint host=%q path=%q", gotHost, gotPath)
	}
}

func TestNewStatusQuoteStoreUsesDefaultHTTPTimeout(t *testing.T) {
	store, err := newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBAccessKey: "access",
		DynamoDBSecretKey: "secret",
		DynamoDBNamespace: "paon-prod",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dynamo, ok := store.(*dynamoDBStatusQuoteStore)
	if !ok {
		t.Fatalf("store = %#v", store)
	}
	if dynamo.client == nil {
		t.Fatal("dynamo client is nil")
	}
	httpClient, ok := dynamo.client.Options().HTTPClient.(*http.Client)
	if !ok || httpClient.Timeout != dynamoDBStatusQuoteHTTPTimeout {
		t.Fatalf("DynamoDB SDK HTTP client = %#v", dynamo.client.Options().HTTPClient)
	}
}

func TestDynamoDBStatusQuoteStoreDefaultsBlankRegion(t *testing.T) {
	store, err := newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBAccessKey: "access",
		DynamoDBSecretKey: "secret",
		DynamoDBNamespace: "paon-prod",
		DynamoDBRegion:    "",
		DynamoDBRegionSet: true,
	}, &http.Client{Transport: statusQuoteRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		auth := req.Header.Get("Authorization")
		if !strings.Contains(auth, "Credential=access/") || !strings.Contains(auth, "/ap-northeast-1/dynamodb/aws4_request") {
			t.Fatalf("authorization credential scope = %q", auth)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: req}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	dynamo, ok := store.(*dynamoDBStatusQuoteStore)
	if !ok {
		t.Fatalf("store = %#v", store)
	}
	if _, err := dynamo.GetMany(context.Background(), []string{"1"}); err != nil {
		t.Fatal(err)
	}
}

func TestNewStatusQuoteStoreRequiresNamespaceLikeRailsWhenCredentialsExist(t *testing.T) {
	_, err := newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBAccessKey: "access",
		DynamoDBSecretKey: "secret",
	}, &http.Client{})
	if err == nil {
		t.Fatal("expected namespace error")
	}
}

func TestDynamoDBStatusQuoteStoreUsesRailsCompatibleItems(t *testing.T) {
	var requests []string
	client := &http.Client{Transport: statusQuoteRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if req.Header.Get("Authorization") == "" || req.Header.Get("X-Amz-Date") == "" {
			t.Fatalf("missing signed headers: %#v", req.Header)
		}
		if req.Header.Get("X-Amz-Security-Token") != "session" {
			t.Fatalf("session token = %q", req.Header.Get("X-Amz-Security-Token"))
		}
		requests = append(requests, req.Header.Get("X-Amz-Target")+" "+string(body))
		response := `{}`
		if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".BatchGetItem") {
			response = `{"Responses":{"paon-prod_status_quotes":[{"status_id":{"S":"100"},"quote_id":{"S":"99"},"original_url":{"S":"https://social.example/users/bob/statuses/99"},"local_url":{"S":"https://social.example/users/bob/statuses/99"}},{"status_id":{"S":"101"},"quote_id":{"S":"98"},"original_url":{"S":"https://social.example/users/alice/statuses/98"},"local_url":{"S":"https://social.example/users/alice/statuses/98"}}]}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     http.Header{},
		}, nil
	})}
	quoteStore, err := newStatusQuoteStore(config.Config{
		DynamoDBEnabled:      true,
		DynamoDBAccessKey:    "access",
		DynamoDBSecretKey:    "secret",
		DynamoDBSessionToken: "session",
		DynamoDBRegion:       "us-west-2",
		DynamoDBNamespace:    "paon-prod",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	store := quoteStore.(*dynamoDBStatusQuoteStore)

	quotes, err := store.GetMany(context.Background(), []string{"100", "101", "100", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 2 || quotes["101"].QuoteID != "98" {
		t.Fatalf("quotes = %#v", quotes)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	for _, want := range []string{
		`"status_id":{"S":"100"}`,
		`DynamoDB_20120810.BatchGetItem`,
		`"RequestItems":{"paon-prod_status_quotes":{"Keys":[{"status_id":{"S":"100"}},{"status_id":{"S":"101"}}]}}`,
	} {
		if !strings.Contains(strings.Join(requests, "\n"), want) {
			t.Fatalf("requests missing %q: %#v", want, requests)
		}
	}
}

func TestNewStatusQuoteStoreAllowsAWSDefaultCredentialChain(t *testing.T) {
	store, err := newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBRegion:    "ap-northeast-1",
		DynamoDBNamespace: "paon-prod",
	}, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("DynamoDB SDK store is nil without static credentials")
	}
}

func TestDynamoStatusQuoteStoreIsReadOnlyCutoverSource(t *testing.T) {
	var store any = &dynamoDBStatusQuoteStore{}
	type writer interface {
		Put(context.Context, statusQuote) error
	}
	type deleter interface {
		Delete(context.Context, string) error
	}
	if _, ok := store.(writer); ok {
		t.Fatal("legacy DynamoDB quote store must not expose Put")
	}
	if _, ok := store.(deleter); ok {
		t.Fatal("legacy DynamoDB quote store must not expose Delete")
	}
}

func TestDynamoStatusQuoteStoreIsNotUsedByWebOrWorkerRuntime(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "operations.go" || name == "status_quotes.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "newStatusQuoteStore(") || strings.Contains(string(src), ".quoteStore") {
			t.Fatalf("normal runtime file %s accesses the legacy DynamoDB quote cutover source", name)
		}
	}
	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serverSource), "quoteStore") {
		t.Fatal("normal web/worker Server must not retain a legacy DynamoDB quote store")
	}
}

func TestLegacyDynamoCutoverHasOneValidatedAcceptedSQLImportPath(t *testing.T) {
	source, err := os.ReadFile("status_quotes.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "cutoverLegacyDynamoStatusQuotes")
	if !strings.Contains(body, "statusQuoteTargetAllowedForAccount(") || !strings.Contains(body, "legacyQuoteMarkerMatchesRow(") || !strings.Contains(body, "models.QuoteStateAccepted") {
		t.Fatalf("legacy cutover must revalidate source, marker and target before importing an accepted SQL Quote: %s", body)
	}
	migrationSource, err := os.ReadFile("../migrate/upgrade_4_4.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(migrationSource), "ImportLegacyQuoteRows") {
		t.Fatal("legacy cutover must not have an unvalidated second SQL import path in the schema migrator")
	}
}

func TestMastodon44DoesNotExposeLaterQuotesCollectionRoute(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "/api/v1/statuses/:id/quotes") || strings.Contains(string(src), "statusQuotes") {
		t.Fatal("Mastodon 4.4 must not expose the later quotes collection endpoint")
	}
}

func TestMastodon44StatusReachIncludesBothQuoteRelationships(t *testing.T) {
	source, err := os.ReadFile("activitypub_delivery.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "activityPubStatusReachedAccountInboxes")
	for _, want := range []string{
		`Where("status_id = ? AND quoted_account_id IS NOT NULL", status.ID)`,
		`Pluck("quoted_account_id", &quotedAccountIDs)`,
		`Where("quoted_status_id = ?", status.ID)`,
		`Pluck("account_id", &interacted)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Mastodon 4.4 StatusReachFinder quote relationship is missing %q: %s", want, body)
		}
	}
	quoteOf := strings.Index(body, `Where("status_id = ? AND quoted_account_id IS NOT NULL", status.ID)`)
	unsafeReach := strings.Index(body, `if distributable || unsafe`)
	quotesOf := strings.Index(body, `Where("quoted_status_id = ?", status.ID)`)
	if quoteOf < 0 || unsafeReach < 0 || quotesOf < unsafeReach || quoteOf > unsafeReach {
		t.Fatalf("quoted author must always be reached, while quoting accounts require distributable/unsafe reach: %s", body)
	}
}

func TestMastodon44QuoteStatusDoesNotFetchLinkCard(t *testing.T) {
	source, err := os.ReadFile("link_cards.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "fetchLinkCardForStatus")
	preload := strings.Index(body, `Preload("Quote")`)
	guard := strings.Index(body, `if status.Quote != nil`)
	fetch := strings.Index(body, `s.findOrFetchPreviewCard(`)
	if preload < 0 || guard < preload || fetch < guard {
		t.Fatalf("Mastodon 4.4 quote status must stop before fetching a link card: %s", body)
	}
}

func TestQuoteWorkerDelaysStayWithinMastodon44Bounds(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if delay := quoteRefetchDelay(); delay < 30*time.Second || delay > 600*time.Second {
			t.Fatalf("refetch delay = %s", delay)
		}
		if delay := quoteRefreshDelay(); delay < 0 || delay >= 6*time.Hour {
			t.Fatalf("refresh delay = %s", delay)
		}
	}
}

func TestQuoteRelationshipChangeDetectionIgnoresApprovalOnlyUpdates(t *testing.T) {
	base := quoteFingerprint{ID: 1, QuotedStatusID: sql.NullInt64{Int64: 2, Valid: true}, State: models.QuoteStatePending}
	approval := base
	approval.State = models.QuoteStateAccepted
	approval.ApprovalURI = sqlNullString("https://remote.example/approval/1")
	if quoteEditRelationshipChanged(base, approval) {
		t.Fatal("approval-only state transition must not create a status edit")
	}
	target := approval
	target.ID = 3
	target.QuotedStatusID = sql.NullInt64{Int64: 4, Valid: true}
	if !quoteEditRelationshipChanged(approval, target) {
		t.Fatal("quote target replacement must create a status edit")
	}
}

func TestMastodon44QuoteUpdateTargetReconciliationModes(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "local.example", WebDomain: "local.example"}}
	target := &models.Status{
		ID:  2,
		URI: sqlNullString("https://quoted.example/users/bob/statuses/2"),
	}
	existing := &models.Quote{ID: 1, QuotedStatus: target}

	if action := activityPubQuoteTargetReconcileAction(server, existing, target.URI.String, false); action != activityPubQuoteTargetProcess {
		t.Fatalf("matching implicit target action = %v", action)
	}
	if action := activityPubQuoteTargetReconcileAction(server, existing, "https://quoted.example/users/bob/statuses/3", false); action != activityPubQuoteTargetIgnore {
		t.Fatalf("mismatching implicit target action = %v", action)
	}
	if action := activityPubQuoteTargetReconcileAction(server, existing, "https://quoted.example/users/bob/statuses/3", true); action != activityPubQuoteTargetReplace {
		t.Fatalf("mismatching explicit target action = %v", action)
	}
	if action := activityPubQuoteTargetReconcileAction(server, existing, "", true); action != activityPubQuoteTargetReplace {
		t.Fatalf("ID-less Tombstone replacement action = %v", action)
	}
	if action := activityPubQuoteTargetReconcileAction(server, &models.Quote{ID: 2}, "https://quoted.example/users/bob/statuses/3", true); action != activityPubQuoteTargetProcess {
		t.Fatalf("unresolved explicit target action = %v", action)
	}
}

func TestMastodon44ImplicitQuoteUpdateRequiresExistingQuote(t *testing.T) {
	source, err := os.ReadFile("activitypub_quotes.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "reconcileActivityPubQuote")
	if !strings.Contains(body, "if !removeWhenAbsent && before.ID == 0") {
		t.Fatalf("implicit quote update must not create a new Quote relationship: %s", body)
	}
}

func TestStatusQuoteTargetStructuralAuthorization(t *testing.T) {
	base := models.Status{
		ID:         99,
		AccountID:  9,
		Visibility: 0,
		Account:    models.Account{ID: 9},
	}
	tests := []struct {
		name   string
		mutate func(*models.Status)
		want   bool
	}{
		{name: "public", want: true},
		{name: "quiet public", mutate: func(status *models.Status) { status.Visibility = 1 }, want: true},
		{name: "edited public", mutate: func(status *models.Status) { status.EditedAt = sql.NullTime{Time: time.Now(), Valid: true} }, want: true},
		{name: "followers only", mutate: func(status *models.Status) { status.Visibility = 2 }},
		{name: "direct", mutate: func(status *models.Status) { status.Visibility = 3 }},
		{name: "limited", mutate: func(status *models.Status) { status.Visibility = 4 }},
		{name: "reblog", mutate: func(status *models.Status) { status.ReblogOfID = sql.NullInt64{Int64: 1, Valid: true} }},
		{name: "deleted", mutate: func(status *models.Status) { status.DeletedAt = sql.NullTime{Time: time.Now(), Valid: true} }},
		{name: "suspended author", mutate: func(status *models.Status) { status.Account.SuspendedAt = sql.NullTime{Time: time.Now(), Valid: true} }},
		{name: "moved author", mutate: func(status *models.Status) { status.Account.MovedToAccountID = sql.NullInt64{Int64: 10, Valid: true} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := base
			if tt.mutate != nil {
				tt.mutate(&status)
			}
			if got := statusQuoteTargetStructurallyAllowed(&status); got != tt.want {
				t.Fatalf("allowed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMastodon44QuoteAuthorizationBindings(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "local.example", WebDomain: "local.example"}}
	sourceURI := "https://quoting.example/users/alice/statuses/1"
	targetURI := "https://quoted.example/users/bob/statuses/2"
	quotedAccountURI := "https://quoted.example/users/bob"
	quote := models.Quote{
		StatusID: 1,
		Status: models.Status{
			ID:        1,
			URI:       sqlNullString(sourceURI),
			AccountID: 10,
			Account:   models.Account{ID: 10, Username: "alice", Domain: sqlNullString("quoting.example"), URI: "https://quoting.example/users/alice"},
		},
		QuotedStatus: &models.Status{
			ID:        2,
			URI:       sqlNullString(targetURI),
			AccountID: 20,
			Account:   models.Account{ID: 20, Username: "bob", Domain: sqlNullString("quoted.example"), URI: quotedAccountURI},
		},
		QuotedAccount: &models.Account{ID: 20, Username: "bob", Domain: sqlNullString("quoted.example"), URI: quotedAccountURI},
	}
	approval := map[string]any{
		"@context":          []any{activityPubActivityStreamsContext(), map[string]any{"QuoteAuthorization": "https://w3id.org/fep/044f#QuoteAuthorization"}},
		"type":              "QuoteAuthorization",
		"attributedTo":      quotedAccountURI,
		"interactingObject": sourceURI,
		"interactionTarget": targetURI,
	}
	if !activityPubQuoteApprovalEnvelopeMatches(server, &quote, approval) || !activityPubQuoteApprovalTargetMatches(server, &quote, approval) {
		t.Fatalf("valid quote authorization was rejected: %#v", approval)
	}

	wrongType := cloneAnyMap(approval)
	wrongType["type"] = "Like"
	if activityPubQuoteApprovalEnvelopeMatches(server, &quote, wrongType) {
		t.Fatal("wrong authorization type matched")
	}
	wrongQuote := cloneAnyMap(approval)
	wrongQuote["interactingObject"] = "https://quoting.example/users/alice/statuses/other"
	if activityPubQuoteApprovalEnvelopeMatches(server, &quote, wrongQuote) {
		t.Fatal("authorization for another quote matched")
	}
	wrongTarget := cloneAnyMap(approval)
	wrongTarget["interactionTarget"] = "https://quoted.example/users/bob/statuses/other"
	if activityPubQuoteApprovalTargetMatches(server, &quote, wrongTarget) {
		t.Fatal("authorization for another target matched")
	}
	wrongAuthor := cloneAnyMap(approval)
	wrongAuthor["attributedTo"] = "https://quoted.example/users/mallory"
	if activityPubQuoteApprovalTargetMatches(server, &quote, wrongAuthor) {
		t.Fatal("authorization from another author matched")
	}
}

func TestMastodon44RejectQuoteRequestPayload(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "local.example", WebDomain: "local.example"}}
	actor := &models.Account{ID: 10, Username: "alice", Domain: sqlNullString("remote.example"), URI: "https://remote.example/users/alice"}
	quoted := &models.Status{ID: 2, AccountID: 20, Account: models.Account{ID: 20, Username: "bob"}}
	payload := activityPayload{
		ID:         "https://remote.example/quote-requests/1",
		Instrument: "https://remote.example/users/alice/statuses/1",
	}
	activity := server.rejectQuoteRequestActivity(payload, actor, quoted)
	if activity["type"] != "Reject" || activity["id"] != "https://local.example/users/bob#rejects/quote_requests/" || activity["actor"] != "https://local.example/users/bob" {
		t.Fatalf("reject envelope = %#v", activity)
	}
	request, ok := activity["object"].(map[string]any)
	if !ok || request["id"] != payload.ID || request["type"] != "QuoteRequest" || request["actor"] != actor.URI || request["object"] != "https://local.example/users/bob/statuses/2" || request["instrument"] != payload.Instrument {
		t.Fatalf("embedded request = %#v", activity["object"])
	}

	payload.Instrument = ""
	activity = server.rejectQuoteRequestActivity(payload, actor, quoted)
	request = activity["object"].(map[string]any)
	if request["instrument"] != nil {
		t.Fatalf("missing instrument = %#v", request["instrument"])
	}
}

func cloneAnyMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
