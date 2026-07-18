package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type statusQuoteRoundTripFunc func(*http.Request) (*http.Response, error)

func (f statusQuoteRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeStatusQuoteStore struct {
	quotes     map[string]statusQuote
	getManyIDs []string
	puts       []statusQuote
	deletes    []string
}

func (f *fakeStatusQuoteStore) Get(_ context.Context, statusID string) (statusQuote, bool, error) {
	quote, ok := f.quotes[statusID]
	return quote, ok, nil
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

func (f *fakeStatusQuoteStore) Put(_ context.Context, quote statusQuote) error {
	f.puts = append(f.puts, quote)
	return nil
}

func (f *fakeStatusQuoteStore) Delete(_ context.Context, statusID string) error {
	f.deletes = append(f.deletes, statusID)
	return nil
}

func TestNewStatusQuoteStoreUsesDynamoidTableName(t *testing.T) {
	store, err := newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBAccessKey: "access",
		DynamoDBSecretKey: "secret",
		DynamoDBNamespace: "paon-prod",
		DynamoDBRegion:    "us-west-2",
	}, &http.Client{})
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
	if dynamo.endpoint != "https://dynamodb.us-west-2.amazonaws.com" {
		t.Fatalf("endpoint = %q", dynamo.endpoint)
	}

	store, err = newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBAccessKey: "access",
		DynamoDBSecretKey: "secret",
		DynamoDBNamespace: "paon-prod",
		DynamoDBRegion:    "us-west-2",
		DynamoDBEndpoint:  "http://dynamodb.test/",
	}, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	dynamo, ok = store.(*dynamoDBStatusQuoteStore)
	if !ok {
		t.Fatalf("store = %#v", store)
	}
	if dynamo.endpoint != "http://dynamodb.test" {
		t.Fatalf("raw trailing-slash DynamoDB endpoint = %q", dynamo.endpoint)
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
	if dynamo.client.Timeout != dynamoDBStatusQuoteHTTPTimeout {
		t.Fatalf("dynamo client timeout = %s, want %s", dynamo.client.Timeout, dynamoDBStatusQuoteHTTPTimeout)
	}
}

func TestDynamoDBStatusQuoteStorePreservesExplicitBlankRailsRegion(t *testing.T) {
	store, err := newStatusQuoteStore(config.Config{
		DynamoDBEnabled:   true,
		DynamoDBAccessKey: "access",
		DynamoDBSecretKey: "secret",
		DynamoDBNamespace: "paon-prod",
		DynamoDBRegion:    "",
		DynamoDBRegionSet: true,
	}, &http.Client{Transport: statusQuoteRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		auth := req.Header.Get("Authorization")
		if !strings.Contains(auth, "Credential=access/") || !strings.Contains(auth, "//dynamodb/aws4_request") {
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
	if dynamo.endpoint != "https://dynamodb..amazonaws.com" {
		t.Fatalf("endpoint = %q", dynamo.endpoint)
	}
	if err := dynamo.Put(context.Background(), statusQuote{StatusID: "1", QuoteID: "2"}); err != nil {
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
		if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem") {
			response = `{"Item":{"status_id":{"S":"100"},"quote_id":{"S":"99"},"original_url":{"S":"https://social.example/users/bob/statuses/99"},"local_url":{"S":"https://social.example/users/bob/statuses/99"}}}`
		}
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
	store := &dynamoDBStatusQuoteStore{
		cfg: config.Config{
			DynamoDBAccessKey:    "access",
			DynamoDBSecretKey:    "secret",
			DynamoDBSessionToken: "session",
			DynamoDBRegion:       "us-west-2",
		},
		tableName: "paon-prod_status_quotes",
		endpoint:  "https://dynamodb.us-west-2.amazonaws.com",
		client:    client,
	}

	quote, ok, err := store.Get(context.Background(), "100")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || quote.QuoteID != "99" || quote.OriginalURL != "https://social.example/users/bob/statuses/99" {
		t.Fatalf("quote = %#v ok=%v", quote, ok)
	}
	quotes, err := store.GetMany(context.Background(), []string{"100", "101", "100", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 2 || quotes["101"].QuoteID != "98" {
		t.Fatalf("quotes = %#v", quotes)
	}
	if err := store.Put(context.Background(), statusQuote{StatusID: "100", QuoteID: "99", OriginalURL: "https://social.example/users/bob/statuses/99"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "100"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 4 {
		t.Fatalf("requests = %#v", requests)
	}
	for _, want := range []string{
		`DynamoDB_20120810.GetItem`,
		`"TableName":"paon-prod_status_quotes"`,
		`"status_id":{"S":"100"}`,
		`DynamoDB_20120810.BatchGetItem`,
		`"RequestItems":{"paon-prod_status_quotes":{"Keys":[{"status_id":{"S":"100"}},{"status_id":{"S":"101"}}]}}`,
		`DynamoDB_20120810.PutItem`,
		`"quote_id":{"S":"99"}`,
		`DynamoDB_20120810.DeleteItem`,
	} {
		if !strings.Contains(strings.Join(requests, "\n"), want) {
			t.Fatalf("requests missing %q: %#v", want, requests)
		}
	}
}

func TestDynamoDBStatusQuoteStoreRejectsOversizedResponses(t *testing.T) {
	store := &dynamoDBStatusQuoteStore{
		cfg: config.Config{
			DynamoDBAccessKey: "access",
			DynamoDBSecretKey: "secret",
			DynamoDBRegion:    "us-west-2",
		},
		tableName: "paon-prod_status_quotes",
		endpoint:  "https://dynamodb.us-west-2.amazonaws.com",
		client: &http.Client{Transport: statusQuoteRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Body:          io.NopCloser(strings.NewReader(`{}`)),
				Header:        http.Header{},
				ContentLength: maxDynamoDBStatusQuoteResponseBodySize + 1,
				Request:       req,
			}, nil
		})},
	}
	if _, _, err := store.Get(context.Background(), "100"); err == nil {
		t.Fatal("expected advertised oversized DynamoDB response to fail")
	}

	store.client = &http.Client{Transport: statusQuoteRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", maxDynamoDBStatusQuoteResponseBodySize+1))),
			Header:        http.Header{},
			ContentLength: -1,
			Request:       req,
		}, nil
	})}
	if _, _, err := store.Get(context.Background(), "100"); err == nil {
		t.Fatal("expected streamed oversized DynamoDB response to fail")
	}
}

func TestHydrateStatusQuoteSetsVirtualFields(t *testing.T) {
	store := &fakeStatusQuoteStore{quotes: map[string]statusQuote{
		"100": {StatusID: "100", QuoteID: "99", OriginalURL: "https://social.example/users/bob/statuses/99"},
	}}
	server := &Server{quoteStore: store}
	status := models.Status{ID: 100, Text: "hello\n\nRE: https://social.example/@bob/99"}

	server.hydrateStatusQuote(&status)
	if !status.QuoteID.Valid || status.QuoteID.String != "99" {
		t.Fatalf("QuoteID = %#v", status.QuoteID)
	}
	if !status.QuoteOriginalURL.Valid || status.QuoteOriginalURL.String != "https://social.example/users/bob/statuses/99" {
		t.Fatalf("QuoteOriginalURL = %#v", status.QuoteOriginalURL)
	}
}

func TestHydrateStatusesQuoteUsesBatchLookup(t *testing.T) {
	store := &fakeStatusQuoteStore{quotes: map[string]statusQuote{
		"101": {StatusID: "101", QuoteID: "99", OriginalURL: "https://social.example/users/bob/statuses/99"},
		"201": {StatusID: "201", QuoteID: "98", OriginalURL: "https://social.example/users/alice/statuses/98"},
	}}
	server := &Server{quoteStore: store}
	statuses := []models.Status{
		{ID: 100, Text: "plain"},
		{ID: 101, Text: "hello\n\nRE: https://social.example/@bob/99"},
		{ID: 200, Text: "plain reblog wrapper", Reblog: &models.Status{ID: 201, Text: "QT: https://social.example/@alice/98"}},
		{ID: 101, Text: "duplicate\n\nRE: https://social.example/@bob/99"},
	}

	server.hydrateStatusesQuote(statuses)
	if got := strings.Join(store.getManyIDs, ","); got != "101,201" {
		t.Fatalf("GetMany IDs = %q", got)
	}
	if statuses[0].QuoteID.Valid || statuses[0].QuoteOriginalURL.Valid {
		t.Fatalf("statuses[0].QuoteID = %#v", statuses[0].QuoteID)
	}
	if !statuses[2].Reblog.QuoteID.Valid || statuses[2].Reblog.QuoteID.String != "98" {
		t.Fatalf("reblog QuoteID = %#v", statuses[2].Reblog.QuoteID)
	}
	if !statuses[1].QuoteID.Valid || statuses[1].QuoteID.String != "99" {
		t.Fatalf("statuses[1].QuoteID = %#v", statuses[1].QuoteID)
	}
	if !statuses[3].QuoteID.Valid || statuses[3].QuoteID.String != "99" {
		t.Fatalf("statuses[3].QuoteID = %#v", statuses[3].QuoteID)
	}
}

func TestPutAndDeleteStatusQuoteBestEffortUseStore(t *testing.T) {
	store := &fakeStatusQuoteStore{}
	server := &Server{
		cfg:        config.Config{LocalDomain: "social.example", WebDomain: "social.example", Scheme: "https"},
		quoteStore: store,
	}
	quote := &models.Status{ID: 99, URI: sqlNullString("https://social.example/users/bob/statuses/99"), Account: models.Account{ID: 9, Username: "bob"}}

	server.putStatusQuoteBestEffort(context.Background(), 100, quote)
	status := &models.Status{ID: 100}
	server.applyStatusQuote(status, quote)
	server.deleteStatusQuoteBestEffort(context.Background(), 100)
	if len(store.puts) != 1 || store.puts[0].StatusID != "100" || store.puts[0].QuoteID != "99" || store.puts[0].OriginalURL != "https://social.example/users/bob/statuses/99" {
		t.Fatalf("puts = %#v", store.puts)
	}
	if !status.QuoteID.Valid || status.QuoteID.String != "99" || !status.QuoteOriginalURL.Valid || status.QuoteOriginalURL.String != "https://social.example/users/bob/statuses/99" {
		t.Fatalf("status quote = %#v/%#v", status.QuoteID, status.QuoteOriginalURL)
	}
	if len(store.deletes) != 1 || store.deletes[0] != "100" {
		t.Fatalf("deletes = %#v", store.deletes)
	}
}
