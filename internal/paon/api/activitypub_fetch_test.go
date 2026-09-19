package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestActivityFetchUsesWorkerContext(t *testing.T) {
	previousClient := activityHTTPClient
	t.Cleanup(func() {
		activityHTTPClient = previousClient
	})
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fetchActivityResourceWithMetadataAndUserAgentSignedWithAcceptAndContext(ctx, "https://remote.example/statuses/1", "", nil, nil, activityResourceAcceptHeader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch error = %v, want context cancellation", err)
	}
}

func TestParseActivityResourcePayloadAcceptsDirectNote(t *testing.T) {
	payload, err := parseActivityResourcePayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/statuses/1",
		"type":"Note",
		"attributedTo":"https://remote.example/users/alice",
		"content":"<p>Hello</p>"
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Create" || payload.Actor != "https://remote.example/users/alice" || payload.Object.ID != "https://remote.example/statuses/1" || !payload.ObjectDocument {
		t.Fatalf("payload = %#v", payload)
	}
	bearcapActor := "bear:?u=https%3A%2F%2Fremote.example%2Fusers%2Falice"
	bearcapPayload, err := parseActivityResourcePayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/statuses/bear",
		"type":"Note",
		"attributedTo":"` + bearcapActor + `",
		"content":"<p>Hello</p>"
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if bearcapPayload.Actor != "https://remote.example/users/alice" || bearcapPayload.ActorRaw != bearcapActor || bearcapPayload.Object.AttributedToRaw != bearcapActor {
		t.Fatalf("direct Note should keep Rails value_or_id attributedTo for fetch checks, got %#v", bearcapPayload)
	}
}

func TestParseActivityResourcePayloadAcceptsMstdnJPMediaOnlyNote(t *testing.T) {
	payload, err := parseActivityResourcePayload([]byte(`{
		"@context":["https://www.w3.org/ns/activitystreams",{
			"sensitive":"as:sensitive",
			"blurhash":"http://joinmastodon.org/ns#blurhash"
		}],
		"id":"https://mstdn.jp/users/6v8/statuses/117142074050939161",
		"type":"Note",
		"published":"2026-08-23T00:49:58Z",
		"url":"https://mstdn.jp/@6v8/117142074050939161",
		"attributedTo":"https://mstdn.jp/users/6v8",
		"to":["https://www.w3.org/ns/activitystreams#Public"],
		"cc":["https://mstdn.jp/users/6v8/followers"],
		"content":"",
		"contentMap":{"ja":""},
		"attachment":[{
			"type":"Document",
			"mediaType":"image/png",
			"url":"https://img.mstdn.jp/media_attachments/files/117/142/073/581/577/316/original/79c7beb10c686d78.png",
			"name":null,
			"blurhash":"UgKw:@~p01IUV[WBoej[M{WBofoeRkWVofbH",
			"width":1491,
			"height":1055
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Create" || !payload.ObjectDocument || payload.Actor != "https://mstdn.jp/users/6v8" {
		t.Fatalf("media-only Note payload = %#v", payload)
	}
	if payload.Object.Content != "" || len(payload.Object.Attachments) != 1 {
		t.Fatalf("media-only Note content=%q attachments=%#v", payload.Object.Content, payload.Object.Attachments)
	}
	attachment := payload.Object.Attachments[0]
	if attachment.MediaType != "image/png" ||
		attachment.URL != "https://img.mstdn.jp/media_attachments/files/117/142/073/581/577/316/original/79c7beb10c686d78.png" ||
		attachment.Blurhash != "UgKw:@~p01IUV[WBoej[M{WBofoeRkWVofbH" {
		t.Fatalf("media-only Note attachment = %#v", attachment)
	}
}

func TestParseActivityResourcePayloadAcceptsCreateNote(t *testing.T) {
	payload, err := parseActivityResourcePayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/activities/1",
		"type":"Create",
		"actor":"https://remote.example/users/alice",
		"object":{"id":"https://remote.example/statuses/1","type":"Note","attributedTo":"https://remote.example/users/alice"}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Create" || payload.Actor != "https://remote.example/users/alice" || payload.Object.Type != "Note" || payload.ObjectDocument {
		t.Fatalf("payload = %#v", payload)
	}
	bearcapActor := "bear:?u=https%3A%2F%2Fremote.example%2Fusers%2Falice"
	bearcapPayload, err := parseActivityResourcePayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/activities/bear",
		"type":"Create",
		"actor":"` + bearcapActor + `",
		"object":{"id":"https://remote.example/statuses/bear","type":"Note","attributedTo":"https://remote.example/users/alice"}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if bearcapPayload.Actor != "https://remote.example/users/alice" || bearcapPayload.ActorRaw != bearcapActor || activityPayloadFetchActorURI(bearcapPayload) != bearcapActor {
		t.Fatalf("Create actor should preserve Rails value_or_id for fetch checks, got %#v", bearcapPayload)
	}

	referencePayload, err := parseActivityResourcePayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/activities/ref",
		"type":"Create",
		"actor":"https://remote.example/users/alice",
		"object":"https://remote.example/statuses/ref"
	}`)))
	if err != nil {
		t.Fatalf("Create with object reference should be accepted before Rails dereference_object!: %v", err)
	}
	if !referencePayload.ObjectReference || referencePayload.Object.ID != "https://remote.example/statuses/ref" {
		t.Fatalf("reference payload = %#v", referencePayload)
	}
}

func TestParseActivityResourcePayloadAcceptsAnnounce(t *testing.T) {
	src, err := os.ReadFile("activitypub_fetch.go")
	if err != nil {
		t.Fatal(err)
	}
	if functionBodyContains(t, src, "activityAnnounceTargetURI", `payload.Object.ObjectID`) {
		t.Fatal("activityAnnounceTargetURI must match Rails value_or_id(@object) and not fall back to nested object.object")
	}
	bearcapID := "bear:?u=https%3A%2F%2Fremote.example%2Factivities%2Fboost-bear"
	payload, err := parseActivityResourcePayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/activities/boost-1",
		"type":"Announce",
		"actor":"https://remote.example/users/alice",
		"published":"2026-06-21T00:00:00Z",
		"object":"https://origin.example/statuses/1"
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Announce" || payload.Actor != "https://remote.example/users/alice" || activityAnnounceTargetURI(payload) != "https://origin.example/statuses/1" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Published != "2026-06-21T00:00:00Z" {
		t.Fatalf("published = %q", payload.Published)
	}

	payload, err = parseActivityResourcePayload([]byte(activityTestJSON(`{
		"id":"` + bearcapID + `",
		"type":"Announce",
		"actor":"https://remote.example/users/alice",
		"object":"https://origin.example/statuses/1"
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if payload.ID != "https://remote.example/activities/boost-bear" || payload.IDRaw != bearcapID {
		t.Fatalf("bearcap id = %q raw = %q", payload.ID, payload.IDRaw)
	}
	if got := activityAnnounceURI(activityPayloadIDValueOrID(payload), &models.Account{URI: "https://remote.example/users/alice"}, activityAnnounceTargetURI(payload)); got != bearcapID {
		t.Fatalf("announce uri = %q, want raw activity id %q", got, bearcapID)
	}

	payload, err = parseActivityResourcePayload([]byte(activityTestJSON(`{
		"id":"https://remote.example/activities/boost-wrapper",
		"type":"Announce",
		"actor":"https://remote.example/users/alice",
		"object":{"type":"Announce","object":"https://origin.example/statuses/nested"}
	}`)))
	if err == nil {
		t.Fatalf("Announce with object hash missing id should be rejected like Rails object_uri nil boundary, payload=%#v", payload)
	}
}

func TestFetchActivityResourcePayloadExpandsCreateObjectURI(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	requests := []string{}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		switch r.URL.String() {
		case "https://remote.example/activities/1":
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/activities/1",
				"type":"Create",
				"actor":"https://remote.example/users/alice",
				"object":"https://remote.example/statuses/1"
			}`)), nil
		case "https://remote.example/statuses/1":
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/statuses/1",
				"type":"Note",
				"attributedTo":"https://remote.example/users/alice",
				"content":"<p>Hello</p>"
			}`)), nil
		default:
			t.Fatalf("unexpected fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	payload, err := fetchActivityResourcePayload("https://remote.example/activities/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %#v, want two fetches", requests)
	}
	if payload.Type != "Create" || payload.Actor != "https://remote.example/users/alice" || payload.Object.ID != "https://remote.example/statuses/1" || payload.Object.Content != "<p>Hello</p>" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestFetchActivityResourcePayloadWithUserAgentCarriesAcrossObjectFetches(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	userAgents := []string{}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		userAgents = append(userAgents, r.Header.Get("User-Agent"))
		switch r.URL.String() {
		case "https://remote.example/activities/1":
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/activities/1",
				"type":"Create",
				"actor":"https://remote.example/users/alice",
				"object":"https://remote.example/statuses/1"
			}`)), nil
		case "https://remote.example/statuses/1":
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.example/statuses/1",
				"type":"Note",
				"attributedTo":"https://remote.example/users/alice",
				"content":"<p>Hello</p>"
			}`)), nil
		default:
			t.Fatalf("unexpected fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	payload, err := fetchActivityResourcePayloadWithUserAgent("https://remote.example/activities/1", "Paon/6.0.2; based Mastodon/4.2.27; +https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if payload.Object.ID != "https://remote.example/statuses/1" {
		t.Fatalf("payload = %#v", payload)
	}
	if strings.Join(userAgents, "\n") != "Paon/6.0.2; based Mastodon/4.2.27; +https://example.com\nPaon/6.0.2; based Mastodon/4.2.27; +https://example.com" {
		t.Fatalf("User-Agent sequence = %#v", userAgents)
	}
}

func TestFetchActivityResourcePayloadFollowsLDJSONAlternateLinkHeader(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://remote.example/@alice/1":
			resp := textResponse(http.StatusOK, "text/html", `<html><body>status</body></html>`)
			resp.Header.Set("Link", `<https://remote.example/users/alice/statuses/1>; rel="alternate"; type="application/ld+json; profile=\"https://www.w3.org/ns/activitystreams\""`)
			return resp, nil
		case "https://remote.example/users/alice/statuses/1":
			return textResponse(http.StatusOK, `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`, activityTestJSON(`{
				"id":"https://remote.example/users/alice/statuses/1",
				"type":"Note",
				"attributedTo":"https://remote.example/users/alice",
				"content":"<p>LD linked</p>"
			}`)), nil
		default:
			t.Fatalf("unexpected fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	payload, err := fetchActivityResourcePayload("https://remote.example/@alice/1")
	if err != nil {
		t.Fatal(err)
	}
	if payload.Object.ID != "https://remote.example/users/alice/statuses/1" || payload.Object.Content != "<p>LD linked</p>" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestActivityJSONContentTypeRequiresLDJSONActivityStreamsProfile(t *testing.T) {
	if !activityJSONContentType("application/activity+json") {
		t.Fatal("application/activity+json should be valid")
	}
	if !activityJSONContentType("application/activity+json; charset=utf-8") {
		t.Fatal("parameterized application/activity+json should be valid like http.rb mime_type")
	}
	if !activityJSONContentType(`application/ld+json; profile="https://www.w3.org/ns/activitystreams"`) {
		t.Fatal("profiled application/ld+json should be valid")
	}
	if !activityJSONContentType(`application/ld+json; charset=utf-8; profile="https://example.com/context https://www.w3.org/ns/activitystreams"`) {
		t.Fatal("profiled application/ld+json should allow other parameters and profile lists like Rails")
	}
	if activityJSONContentType("application/ld+json") {
		t.Fatal("unprofiled application/ld+json should be rejected like Rails")
	}
	if activityJSONContentType(`application/ld+json; profile="https://example.com/context"`) {
		t.Fatal("non-ActivityStreams application/ld+json should be rejected")
	}
}

func TestFetchActivityResourcePayloadRejectsUnprofiledLDJSONLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return textResponse(http.StatusOK, "application/ld+json", activityTestJSON(`{
			"id":"https://remote.example/users/alice/statuses/1",
			"type":"Note",
			"attributedTo":"https://remote.example/users/alice",
			"content":"<p>Unprofiled</p>"
		}`)), nil
	})}

	if _, err := fetchActivityResourcePayload("https://remote.example/users/alice/statuses/1"); err == nil {
		t.Fatal("expected unprofiled application/ld+json to be rejected")
	}
}

func TestFetchActivityResourceRejectsOversizedContentLengthLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := textResponse(http.StatusOK, "application/activity+json", `{}`)
		resp.ContentLength = maxActivityResourceBodySize + 1
		return resp, nil
	})}

	if _, err := fetchActivityResourcePayload("https://remote.example/users/alice/statuses/1"); err == nil {
		t.Fatal("expected oversized Content-Length to be rejected")
	}
}

func TestFetchActivityResourceRejectsOversizedBodyLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := strings.Repeat("x", maxActivityResourceBodySize+1)
		resp := textResponse(http.StatusOK, "application/activity+json", body)
		resp.ContentLength = -1
		return resp, nil
	})}

	if _, err := fetchActivityResourcePayload("https://remote.example/users/alice/statuses/1"); err == nil {
		t.Fatal("expected body larger than Rails Request#body_with_limit to be rejected")
	}
}

func TestFetchRemoteStatusReturnsExpandedNoteURI(t *testing.T) {
	src, err := os.ReadFile("activitypub_fetch.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "processFetchedRemoteStatusPayloadWithContext", `return s.statusFromActivityURIWithContext(ctx, firstNonEmpty(note.ID, expectedURI))`) {
		t.Fatal("fetched remote status processing must return the fetched Note URI, not the wrapping Create activity URI")
	}
}

func TestRemoteActivityDomainNotAllowedIgnoresBlankAndInvalidURI(t *testing.T) {
	for _, raw := range []string{"", "not a uri", "https://"} {
		got, err := (*Server)(nil).remoteActivityDomainNotAllowed(raw)
		if err != nil {
			t.Fatalf("%q returned error: %v", raw, err)
		}
		if got {
			t.Fatalf("%q should not be marked disallowed without a parseable remote host", raw)
		}
	}
}

func TestVerifyRemoteActivityActorWebFingerLoopback(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://remote.example/.well-known/webfinger?resource=acct%3Aalice%40remote.example" {
			t.Fatalf("unexpected webfinger request: %s", r.URL.String())
		}
		if got := r.Header.Get("User-Agent"); got != railsHTTPRequestUserAgent {
			t.Fatalf("WebFinger User-Agent = %q, want %q", got, railsHTTPRequestUserAgent)
		}
		body := `{"subject":"acct:alice@remote.example","links":[{"rel":"self","type":"application/activity+json","href":"https://remote.example/users/alice"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	err := verifyRemoteActivityActorWebFinger(remoteActivityActor{
		ID:                "https://remote.example/users/alice",
		PreferredUsername: "alice",
	})
	if err != nil {
		t.Fatalf("verifyRemoteActivityActorWebFinger error = %v", err)
	}
}

func TestVerifyRemoteActivityActorWebFingerRejectsSecondSubjectRedirectLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://remote.example/.well-known/webfinger?resource=acct%3Aalias%40remote.example":
			body := `{"subject":"acct:alice@remote.example","links":[{"rel":"self","type":"application/activity+json","href":"https://remote.example/users/wrong"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		case "https://remote.example/.well-known/webfinger?resource=acct%3Aalice%40remote.example":
			body := `{"subject":"acct:bob@remote.example","links":[{"rel":"self","type":"application/activity+json","href":"https://remote.example/users/alias"}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		default:
			t.Fatalf("unexpected webfinger request: %s", r.URL.String())
			return nil, nil
		}
	})}

	err := verifyRemoteActivityActorWebFinger(remoteActivityActor{
		ID:                "https://remote.example/users/alias",
		PreferredUsername: "alias",
	})
	if err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("expected second redirect subject error, got %v", err)
	}
}

func TestVerifyRemoteActivityActorWebFingerRejectsLoopbackMismatch(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"subject":"acct:alice@remote.example","links":[{"rel":"self","type":"application/activity+json","href":"https://remote.example/users/other"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	err := verifyRemoteActivityActorWebFinger(remoteActivityActor{
		ID:                "https://remote.example/users/alice",
		PreferredUsername: "alice",
	})
	if err == nil || !strings.Contains(err.Error(), "loop back") {
		t.Fatalf("expected loopback error, got %v", err)
	}
}

func TestActivityPayloadPublishedAtPrefersTopLevelAnnounceTimestamp(t *testing.T) {
	fallback := time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC)
	payload := activityPayload{
		Published: "2026-06-20T00:00:00Z",
		Object:    activityObject{Published: "2026-06-19T00:00:00Z"},
	}
	got := activityPayloadPublishedAt(payload, fallback)
	want := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("published = %s, want %s", got, want)
	}
}

func TestParseActivityResourcePayloadRejectsNonNote(t *testing.T) {
	if _, err := parseActivityResourcePayload([]byte(activityTestJSON(`{"id":"https://remote.example/users/alice","type":"Person"}`))); err == nil {
		t.Fatal("expected non-note activity to be rejected")
	}
}

func TestActivityHTTPClientPoolSizeEnvMatchesRailsToI(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 0},
		{"bad", 0},
		{"12bad", 12},
		{"  +9px", 9},
		{"-1", -1},
	} {
		t.Setenv("MAX_REQUEST_POOL_SIZE", tc.raw)
		if got := intFromEnvForActivityHTTP("MAX_REQUEST_POOL_SIZE", 512); got != tc.want {
			t.Fatalf("MAX_REQUEST_POOL_SIZE=%q parsed as %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestActivityFetchHiddenServiceGateMatchesRailsRequest(t *testing.T) {
	old := activityAllowAccessHiddenService
	t.Cleanup(func() { activityAllowAccessHiddenService = old })

	activityAllowAccessHiddenService = false
	if activityFetchHostAllowed("abcdefghijklmnop.onion") {
		t.Fatal("hidden service host should be blocked without ALLOW_ACCESS_TO_HIDDEN_SERVICE=true")
	}
	if activityFetchHostAllowed("remote.i2p") {
		t.Fatal("i2p host should be blocked without ALLOW_ACCESS_TO_HIDDEN_SERVICE=true")
	}

	activityAllowAccessHiddenService = true
	if !activityFetchHostAllowed("abcdefghijklmnop.onion") {
		t.Fatal("hidden service host should be allowed when ALLOW_ACCESS_TO_HIDDEN_SERVICE=true")
	}
	if !activityFetchHostAllowed("remote.i2p") {
		t.Fatal("i2p host should be allowed when ALLOW_ACCESS_TO_HIDDEN_SERVICE=true")
	}
}

func activityTestJSON(body string) string {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "{") {
		return body
	}
	return `{"@context":"https://www.w3.org/ns/activitystreams",` + strings.TrimPrefix(body, "{")
}
