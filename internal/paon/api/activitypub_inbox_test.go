package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestActivityPubProcessingFailuresReturnForAsynqRetry(t *testing.T) {
	server := &Server{db: &gorm.DB{}}
	actor := &models.Account{
		ID:     42,
		URI:    "https://remote.example/users/alice",
		Domain: sql.NullString{String: "remote.example", Valid: true},
	}
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{`},
		{name: "unsupported context", body: `{"type":"Create"}`},
		{name: "verified actor mismatch", body: `{
			"@context":"https://www.w3.org/ns/activitystreams",
			"id":"https://other.example/activities/1",
			"type":"Delete",
			"actor":"https://other.example/users/bob",
			"object":"https://other.example/users/bob"
		}`},
		{name: "unsupported activity", body: `{
			"@context":"https://www.w3.org/ns/activitystreams",
			"id":"https://remote.example/activities/1",
			"type":"Travel",
			"actor":"https://remote.example/users/alice",
			"object":"https://remote.example/objects/1"
		}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), []byte(test.body), actor, nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) {
				t.Fatalf("processing error = %v, want errActivityPubEventNotApplied", err)
			}
		})
	}
}

func TestActivityPubPeerTubeViewIsAcceptedWithoutApplyingState(t *testing.T) {
	server := &Server{db: &gorm.DB{}}
	actor := &models.Account{
		ID:     106836212681004967,
		URI:    "https://video.blender.org/accounts/peertube",
		Domain: sql.NullString{String: "video.blender.org", Valid: true},
	}
	body := []byte(`{
		"@context":[
			"https://www.w3.org/ns/activitystreams",
			"https://w3id.org/security/v1",
			{"RsaSignature2017":"https://w3id.org/security#RsaSignature2017"},
			{
				"pt":"https://joinpeertube.org/ns#",
				"sc":"http://schema.org/",
				"WatchAction":"sc:WatchAction",
				"InteractionCounter":"sc:InteractionCounter",
				"interactionType":"sc:interactionType",
				"userInteractionCount":"sc:userInteractionCount"
			}
		],
		"to":[
			"https://www.w3.org/ns/activitystreams#Public",
			"https://video.blender.org/video-channels/blender_open_movies"
		],
		"cc":["https://video.blender.org/accounts/blender/followers"],
		"id":"https://video.blender.org/accounts/peertube/views/videos/24061/ff8fe61b-026f-4f07-b66b-2a790d6f6ab1",
		"type":"View",
		"actor":"https://video.blender.org/accounts/peertube",
		"object":"https://video.blender.org/videos/watch/ff8fe61b-026f-4f07-b66b-2a790d6f6ab1",
		"expires":"2026-08-23T15:32:23.631Z",
		"result":{
			"interactionType":"WatchAction",
			"type":"InteractionCounter",
			"userInteractionCount":1
		},
		"signature":{
			"type":"RsaSignature2017",
			"creator":"https://video.blender.org/accounts/peertube",
			"created":"2026-08-23T15:30:23.667Z",
			"signatureValue":"test-signature"
		}
	}`)

	verificationBody := activityPubCompactCollectionBody(body)
	payload, err := parseActivityPayload(verificationBody)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "View" {
		t.Fatalf("PeerTube activity type = %q, want View", payload.Type)
	}
	if fields := activityPubLogFieldsFromBody(body); fields.Type != "View" {
		t.Fatalf("PeerTube activity log type = %q, want View", fields.Type)
	}
	graphPayload, err := parseActivityPayload([]byte(`{
		"@context":"https://www.w3.org/ns/activitystreams",
		"@graph":[
			{
				"id":"https://video.blender.org/views/counters/1",
				"type":"InteractionCounter"
			},
			{
				"id":"https://video.blender.org/accounts/peertube/views/videos/24061/ff8fe61b-026f-4f07-b66b-2a790d6f6ab1",
				"type":"View",
				"actor":"https://video.blender.org/accounts/peertube",
				"object":"https://video.blender.org/videos/watch/ff8fe61b-026f-4f07-b66b-2a790d6f6ab1"
			}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if graphPayload.Type != "View" || graphPayload.Actor != actor.URI {
		t.Fatalf("graph-wrapped PeerTube activity = type %q actor %q", graphPayload.Type, graphPayload.Actor)
	}

	if err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, actor, nil, 0); err != nil {
		t.Fatalf("PeerTube View processing error = %v, want nil", err)
	}
}

func TestActivityPubRelayLinkedDataSignatureSurvivesUntilForwardingFinalization(t *testing.T) {
	privateKey, publicKeyPEM, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: config.Config{
		Scheme:      "https",
		WebDomain:   "origin.example",
		LocalDomain: "origin.example",
	}}
	signer := models.Account{
		Username:   "alice",
		PrivateKey: sql.NullString{String: privateKey, Valid: true},
		PublicKey:  publicKeyPEM,
	}
	actorURI := "https://origin.example/users/alice"
	activity := map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			map[string]any{
				"misskey":          "https://misskey-hub.net/ns#",
				"_misskey_summary": "misskey:_misskey_summary",
			},
		},
		"id":    actorURI + "#updates/1",
		"type":  "Update",
		"actor": actorURI,
		"to":    []any{activityPubPublicIRI},
		"object": map[string]any{
			"id":               actorURI,
			"type":             "Person",
			"name":             "Alice",
			"_misskey_summary": "Misskey profile source",
		},
	}
	signed, err := server.signActivityPubLinkedDataSignaturePayload(signer, activity)
	if err != nil {
		t.Fatal(err)
	}
	originalBody, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := activityPublicKey(publicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	verificationBody := activityPubCompactCollectionBody(originalBody)
	verificationPayload, err := parseActivityPayload(verificationBody)
	if err != nil {
		t.Fatal(err)
	}
	if !verificationPayload.Signature.Present {
		t.Fatal("linked-data signature was removed before actor verification")
	}
	if !verifyActivityPubLinkedDataSignature(verificationBody, publicKey) {
		t.Fatal("compacted Misskey activity did not retain a valid linked-data signature")
	}

	forwardingBody := activityPubFinalizeCollectionBodyForForwarding(originalBody, verificationBody)
	forwardingPayload, err := parseActivityPayload(forwardingBody)
	if err != nil {
		t.Fatal(err)
	}
	if forwardingPayload.Signature.Present {
		t.Fatal("forwarding-unsafe Misskey activity retained its linked-data signature")
	}
	if !strings.EqualFold(forwardingPayload.Actor, actorURI) || string(forwardingPayload.RawBody) != string(forwardingBody) {
		t.Fatalf("forwarding payload did not retain its authenticated actor/body: actor=%q", forwardingPayload.Actor)
	}
	if strings.Contains(string(forwardingPayload.RawBody), `"signature"`) {
		t.Fatal("forwarding payload RawBody retained a linked-data signature")
	}
	if !verificationPayload.Signature.Present || !verifyActivityPubLinkedDataSignature(verificationBody, publicKey) {
		t.Fatal("forwarding finalization mutated the document reserved for actor verification")
	}

	var tampered map[string]any
	if err := json.Unmarshal(verificationBody, &tampered); err != nil {
		t.Fatal(err)
	}
	object, _ := tampered["object"].(map[string]any)
	object["name"] = "Mallory"
	tamperedBody, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if verifyActivityPubLinkedDataSignature(tamperedBody, publicKey) {
		t.Fatal("tampered relayed activity retained a valid linked-data signature")
	}
}

func TestAcceptFollowPayloadPreservesEmbeddedFollowIdentity(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"@context":["https://www.w3.org/ns/activitystreams","https://w3id.org/security/v1"],
		"id":"https://remote.example/activities/accept-follow",
		"type":"Accept",
		"actor":"https://remote.example/users/bob",
		"object":{
			"id":"https://paon.example/payloads/original-follow",
			"type":"Follow",
			"actor":"https://paon.example/users/alice",
			"object":"https://remote.example/users/bob"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Accept" || payload.Actor != "https://remote.example/users/bob" {
		t.Fatalf("Accept identity = type %q actor %q", payload.Type, payload.Actor)
	}
	if payload.Object.TypeExact != "Follow" ||
		payload.Object.ID != "https://paon.example/payloads/original-follow" ||
		payload.Object.Actor != "https://paon.example/users/alice" ||
		payload.Object.ObjectID != "https://remote.example/users/bob" {
		t.Fatalf("embedded Follow = %#v", payload.Object)
	}
}

func TestActivityPubNoteParsesRepliesCollection(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"id":"https://remote.test/activities/1",
		"type":"Create",
		"actor":"https://remote.test/users/bob",
		"object":{
			"id":"https://remote.test/users/bob/statuses/1",
			"type":"Note",
			"attributedTo":"https://remote.test/users/bob",
			"content":"hello",
			"replies":{
				"id":"https://remote.test/users/bob/statuses/1/replies",
				"type":"Collection",
				"items":[
					"https://remote.test/users/alice/statuses/2",
					{"id":"https://remote.test/users/carol/statuses/3"}
				]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Object.Replies.ID != "https://remote.test/users/bob/statuses/1/replies" || payload.Object.Replies.Type != "Collection" {
		t.Fatalf("replies collection = %#v", payload.Object.Replies)
	}
	if !reflect.DeepEqual(payload.Object.Replies.Items, []string{"https://remote.test/users/alice/statuses/2", "https://remote.test/users/carol/statuses/3"}) {
		t.Fatalf("replies items = %#v", payload.Object.Replies.Items)
	}
	bearcapReply := "bear:?u=https%3A%2F%2Fremote.test%2Fusers%2Falice%2Fstatuses%2Fbear"
	bearcapPayload, err := parseActivityPayload([]byte(activityTestJSON(`{
		"type":"Create",
		"actor":"https://remote.test/users/bob",
		"object":{
			"id":"https://remote.test/users/bob/statuses/2",
			"type":"Note",
			"attributedTo":"https://remote.test/users/bob",
			"inReplyTo":"` + bearcapReply + `",
			"replies":{"type":"Collection","items":["` + bearcapReply + `",{"type":"Note","id":"` + bearcapReply + `"}]}
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if bearcapPayload.Object.InReplyTo != bearcapReply {
		t.Fatalf("inReplyTo should preserve Rails value_or_id bearcap, got %q", bearcapPayload.Object.InReplyTo)
	}
	if !reflect.DeepEqual(bearcapPayload.Object.Replies.Items, []string{bearcapReply, bearcapReply}) || !reflect.DeepEqual(bearcapPayload.Object.Replies.NoteItems, []string{bearcapReply, bearcapReply}) {
		t.Fatalf("replies collection should preserve Rails value_or_id bearcaps, got items=%#v noteItems=%#v", bearcapPayload.Object.Replies.Items, bearcapPayload.Object.Replies.NoteItems)
	}
	inlineFirstPayload, err := parseActivityPayload([]byte(activityTestJSON(`{
		"type":"Create",
		"actor":"https://remote.test/users/bob",
		"object":{
			"id":"https://remote.test/users/bob/statuses/4",
			"type":"Note",
			"attributedTo":"https://remote.test/users/bob",
			"replies":{
				"id":"https://remote.test/users/bob/statuses/4/replies",
				"type":"Collection",
				"items":["https://remote.test/users/ignored/statuses/root-item"],
				"first":{
					"type":"CollectionPage",
					"partOf":"https://remote.test/users/bob/statuses/4/replies",
					"items":["https://remote.test/users/alice/statuses/5",{"id":"https://remote.test/users/carol/statuses/6"}]
				}
			}
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if inlineFirstPayload.Object.Replies.FirstCollection == nil {
		t.Fatalf("inline first replies page was not preserved: %#v", inlineFirstPayload.Object.Replies)
	}
	if !reflect.DeepEqual(inlineFirstPayload.Object.Replies.FirstCollection.Items, []string{"https://remote.test/users/alice/statuses/5", "https://remote.test/users/carol/statuses/6"}) {
		t.Fatalf("inline first replies items = %#v", inlineFirstPayload.Object.Replies.FirstCollection.Items)
	}
}

func TestActivityPubJSONLDDocumentLoaderCachesRemoteContextsLikeRails(t *testing.T) {
	reset := func() {
		activityPubJSONLDContextCache.Lock()
		activityPubJSONLDContextCache.entries = make(map[string]activityPubJSONLDContextCacheEntry)
		activityPubJSONLDContextCache.Unlock()
	}
	reset()
	t.Cleanup(reset)

	oldClient := activityHTTPClient
	oldProxy := activityHTTPProxyConfigured
	t.Cleanup(func() {
		activityHTTPClient = oldClient
		activityHTTPProxyConfigured = oldProxy
	})
	activityHTTPProxyConfigured = true

	calls := 0
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/ld+json"}},
			Body:       io.NopCloser(strings.NewReader(`{"@context":{"ex":"https://example.com/ns#"},"ex:name":"cached"}`)),
			Request:    r,
		}, nil
	})}

	first, err := activityPubJSONLDDocumentLoader().LoadDocument("https://context-cache.example/context")
	if err != nil {
		t.Fatalf("first LoadDocument error = %v", err)
	}
	second, err := activityPubJSONLDDocumentLoader().LoadDocument("https://context-cache.example/context")
	if err != nil {
		t.Fatalf("second LoadDocument error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("remote JSON-LD context fetch calls = %d, want Rails-style cache hit after first load", calls)
	}
	firstMap, ok := first.Document.(map[string]any)
	if !ok {
		t.Fatalf("first document = %#v", first.Document)
	}
	secondMap, ok := second.Document.(map[string]any)
	if !ok {
		t.Fatalf("second document = %#v", second.Document)
	}
	firstMap["mutated"] = true
	if _, ok := secondMap["mutated"]; ok {
		t.Fatal("cached raw JSON-LD context must be decoded into a fresh RemoteDocument on each load")
	}
	if first.DocumentURL != "https://context-cache.example/context" || second.DocumentURL != first.DocumentURL {
		t.Fatalf("cached document URLs = %q / %q", first.DocumentURL, second.DocumentURL)
	}
}

func TestActivityPubJSONLDDocumentLoaderRejectsOversizedContextsLikeRailsBodyWithLimit(t *testing.T) {
	reset := func() {
		activityPubJSONLDContextCache.Lock()
		activityPubJSONLDContextCache.entries = make(map[string]activityPubJSONLDContextCacheEntry)
		activityPubJSONLDContextCache.Unlock()
	}
	reset()
	t.Cleanup(reset)

	oldClient := activityHTTPClient
	oldProxy := activityHTTPProxyConfigured
	t.Cleanup(func() {
		activityHTTPClient = oldClient
		activityHTTPProxyConfigured = oldProxy
	})
	activityHTTPProxyConfigured = true

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"application/ld+json"}},
			Body:          io.NopCloser(strings.NewReader(`{}`)),
			ContentLength: activityPubJSONLDContextMaxBytes + 1,
			Request:       r,
		}, nil
	})}
	if _, err := activityPubJSONLDDocumentLoader().LoadDocument("https://context-cache.example/too-large"); err == nil {
		t.Fatal("JSON-LD context above Rails body_with_limit size should be rejected")
	}
}

func TestActivityPubJSONLDDocumentLoaderUsesSharedActivityHTTPClient(t *testing.T) {
	oldClient := activityHTTPClient
	oldProxy := activityHTTPProxyConfigured
	oldPrivate := activityPrivateAddressExceptions
	t.Cleanup(func() {
		activityHTTPClient = oldClient
		activityHTTPProxyConfigured = oldProxy
		activityPrivateAddressExceptions = oldPrivate
	})
	activityHTTPProxyConfigured = false
	activityPrivateAddressExceptions = nil

	called := false
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		if got := r.Header.Get("Accept"); got != "application/ld+json" {
			t.Fatalf("Accept = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/ld+json"}},
			Body:       io.NopCloser(strings.NewReader(`{"@context":{"ex":"https://example.com/ns#"}}`)),
			Request:    r,
		}, nil
	})}

	doc, err := activityPubJSONLDDocumentLoader().LoadDocument("https://context.example/context")
	if err != nil {
		t.Fatalf("LoadDocument error = %v", err)
	}
	if !called || doc.Document == nil {
		t.Fatalf("shared ActivityPub client was not used; called=%v doc=%#v", called, doc)
	}

	called = false
	if _, err := activityPubJSONLDDocumentLoader().LoadDocument("https://10.0.0.1/context"); err == nil {
		t.Fatal("private JSON-LD context host should be rejected without proxy or ALLOWED_PRIVATE_ADDRESSES")
	}
	if called {
		t.Fatal("private JSON-LD context host should be rejected before HTTP request")
	}
}

func TestActivityPubParserIgnoresUncompactedJSONLDObjectAliases(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"@context":["https://www.w3.org/ns/activitystreams",{"object":"https://www.w3.org/ns/activitystreams#object"}],
		"id":"https://forwarder.test/activities/1",
		"type":"Create",
		"actor":"https://remote.test/users/bob",
		"object":{
			"id":"https://remote.test/users/bob/fake-status",
			"type":"Note",
			"content":"<p>puck was here</p>",
			"attributedTo":"https://remote.test/users/bob",
			"@id":"https://remote.test/users/bob/statuses/107928807471117876",
			"@type":"https://www.w3.org/ns/activitystreams#Note",
			"https://www.w3.org/ns/activitystreams#content":[
				"<p>hello world</p>",
				{"@value":"<p>hello world</p>","@language":"en"}
			]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Object.ID != "https://remote.test/users/bob/fake-status" {
		t.Fatalf("uncompacted @id must not replace object id: %#v", payload.Object)
	}
	if payload.Object.Type != "Note" {
		t.Fatalf("uncompacted @type must not replace object type: %#v", payload.Object)
	}
	if payload.Object.Content != "<p>puck was here</p>" {
		t.Fatalf("uncompacted content alias must not replace object content: %#v", payload.Object)
	}
	if !activityNoteBelongsToActor(payload.Object, &models.Account{URI: "https://remote.test/users/bob"}) {
		t.Fatal("plain compact ActivityPub object should still belong to the declared actor")
	}
}

func TestActivityPubParserAcceptsExpandedJSONLDFallbackFields(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"@context":"https://www.w3.org/ns/activitystreams",
		"@id":"https://remote.test/users/bob/statuses/1/activity",
		"@type":"https://www.w3.org/ns/activitystreams#Create",
		"https://www.w3.org/ns/activitystreams#actor":[{"@id":"https://remote.test/users/bob"}],
		"https://www.w3.org/ns/activitystreams#to":[{"@list":[{"@id":"https://www.w3.org/ns/activitystreams#Public"}]}],
		"https://www.w3.org/ns/activitystreams#cc":[{"@id":"https://remote.test/users/bob/followers"}],
		"https://www.w3.org/ns/activitystreams#object":[{
			"@id":"https://remote.test/users/bob/statuses/1",
			"@type":"https://www.w3.org/ns/activitystreams#Note",
			"https://www.w3.org/ns/activitystreams#attributedTo":[{"@id":"https://remote.test/users/bob"}],
			"https://www.w3.org/ns/activitystreams#content":[{"@value":"<p>hello world</p>","@language":"en"}],
			"https://www.w3.org/ns/activitystreams#published":[{"@value":"2026-06-22T00:00:00Z"}],
			"https://www.w3.org/ns/activitystreams#to":[{"@id":"https://www.w3.org/ns/activitystreams#Public"}],
			"https://www.w3.org/ns/activitystreams#cc":[{"@list":[{"@id":"https://remote.test/users/bob/followers"}]}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.ID != "https://remote.test/users/bob/statuses/1/activity" || payload.Type != "Create" || payload.Actor != "https://remote.test/users/bob" {
		t.Fatalf("expanded payload identity = %#v", payload)
	}
	if payload.Object.ID != "https://remote.test/users/bob/statuses/1" || payload.Object.Type != "Note" || payload.Object.AttributedTo != "https://remote.test/users/bob" {
		t.Fatalf("expanded object identity = %#v", payload.Object)
	}
	if payload.Object.Content != "<p>hello world</p>" || payload.Object.Published != "2026-06-22T00:00:00Z" {
		t.Fatalf("expanded object content = %#v", payload.Object)
	}
	if !reflect.DeepEqual(payload.To, []string{"https://www.w3.org/ns/activitystreams#Public"}) || !reflect.DeepEqual(payload.Object.CC, []string{"https://remote.test/users/bob/followers"}) {
		t.Fatalf("expanded audience payload=%#v object=%#v", payload.To, payload.Object.CC)
	}
	httpIRIPayload, err := parseActivityPayload([]byte(`{
		"@context":"https://www.w3.org/ns/activitystreams",
		"@id":"https://remote.test/users/bob/statuses/http-iri/activity",
		"@type":"http://www.w3.org/ns/activitystreams#Create",
		"http://www.w3.org/ns/activitystreams#actor":[{"@id":"https://remote.test/users/bob"}],
		"http://www.w3.org/ns/activitystreams#to":[{"@id":"https://www.w3.org/ns/activitystreams#Public"}],
		"http://www.w3.org/ns/activitystreams#object":[{
			"@id":"https://remote.test/users/bob/statuses/http-iri",
			"@type":"http://www.w3.org/ns/activitystreams#Note",
			"http://www.w3.org/ns/activitystreams#attributedTo":[{"@id":"https://remote.test/users/bob"}],
			"http://www.w3.org/ns/activitystreams#content":[{"@value":"<p>http expanded</p>"}],
			"http://www.w3.org/ns/activitystreams#cc":[{"@id":"https://remote.test/users/bob/followers"}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if httpIRIPayload.Type != "Create" || httpIRIPayload.Actor != "https://remote.test/users/bob" || httpIRIPayload.Object.Type != "Note" || httpIRIPayload.Object.Content != "<p>http expanded</p>" {
		t.Fatalf("http expanded ActivityStreams payload = %#v", httpIRIPayload)
	}
	if !reflect.DeepEqual(httpIRIPayload.To, []string{"https://www.w3.org/ns/activitystreams#Public"}) || !reflect.DeepEqual(httpIRIPayload.Object.CC, []string{"https://remote.test/users/bob/followers"}) {
		t.Fatalf("http expanded audience payload=%#v object=%#v", httpIRIPayload.To, httpIRIPayload.Object.CC)
	}
	nestedIDPayload, err := parseActivityPayload([]byte(activityTestJSON(`{
		"type":"Create",
		"actor":{"id":{"@id":"https://remote.test/users/bob"}},
		"object":{
			"id":[{"@list":[{"@id":"https://remote.test/users/bob/statuses/nested-id"}]}],
			"type":"Note",
			"attributedTo":{"id":{"@id":"https://remote.test/users/bob"}}
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if nestedIDPayload.Actor != "https://remote.test/users/bob" || nestedIDPayload.Object.ID != "https://remote.test/users/bob/statuses/nested-id" || nestedIDPayload.Object.AttributedTo != "https://remote.test/users/bob" {
		t.Fatalf("nested id payload = %#v", nestedIDPayload)
	}
	compactPayload, err := parseActivityPayload([]byte(activityTestJSON(`{
		"@type":"as:Create",
		"actor":"https://remote.test/users/bob",
		"object":{
			"@id":"https://remote.test/users/bob/statuses/compact-prefix",
			"@type":"as:Note",
			"attributedTo":"https://remote.test/users/bob"
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if compactPayload.Type != "Create" || compactPayload.Object.Type != "Note" || !activityObjectIsStatus(compactPayload.Object) {
		t.Fatalf("compact prefixed payload = %#v", compactPayload)
	}

	prefixedPayload, err := parseActivityPayload([]byte(activityTestJSON(`{
		"@type":"as:Create",
		"as:actor":"https://remote.test/users/bob",
		"as:object":{
			"@id":"https://remote.test/users/bob/statuses/compact-property-prefix",
			"@type":"as:Note",
			"as:attributedTo":"https://remote.test/users/bob",
			"as:content":"<p>compact property</p>"
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if prefixedPayload.Actor != "https://remote.test/users/bob" || prefixedPayload.Object.AttributedTo != "https://remote.test/users/bob" || prefixedPayload.Object.Content != "<p>compact property</p>" {
		t.Fatalf("compact prefixed properties = %#v", prefixedPayload)
	}

	listWrapped, err := parseActivityPayload([]byte(`{
		"@context":"https://www.w3.org/ns/activitystreams",
		"@id":"https://remote.test/users/bob/statuses/list/activity",
		"@type":"https://www.w3.org/ns/activitystreams#Create",
		"https://www.w3.org/ns/activitystreams#actor":[{"@list":[{"@id":"https://remote.test/users/bob"}]}],
		"https://www.w3.org/ns/activitystreams#object":[{"@list":[{
			"@id":"https://remote.test/users/bob/statuses/list",
			"@type":"https://www.w3.org/ns/activitystreams#Note",
			"https://www.w3.org/ns/activitystreams#attributedTo":[{"@list":[{"@id":"https://remote.test/users/bob"}]}],
			"https://www.w3.org/ns/activitystreams#inReplyTo":[{"@list":[{"@id":"https://remote.test/users/alice/statuses/1"}]}]
		}]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if listWrapped.Actor != "https://remote.test/users/bob" || listWrapped.Object.ID != "https://remote.test/users/bob/statuses/list" ||
		listWrapped.Object.AttributedTo != "https://remote.test/users/bob" || listWrapped.Object.InReplyTo != "https://remote.test/users/alice/statuses/1" {
		t.Fatalf("list-wrapped single IDs = %#v", listWrapped)
	}

	attributedToFirstOnly, err := parseActivityPayload([]byte(activityTestJSON(`{
		"type":"Create",
		"actor":"https://remote.test/users/bob",
		"object":{
			"id":"https://remote.test/users/bob/statuses/first-only",
			"type":"Note",
			"attributedTo":[{}, {"id":"https://remote.test/users/bob"}]
		}
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if attributedToFirstOnly.Object.AttributedTo != "" {
		t.Fatalf("attributedTo arrays must evaluate only the first value like Rails first_of_value, got %q", attributedToFirstOnly.Object.AttributedTo)
	}

	graphWrapped, err := parseActivityPayload([]byte(activityTestJSON(`{
		"@graph":[
			{"id":"https://remote.test/users/bob","type":"Person"},
			{
				"id":"https://remote.test/users/bob/statuses/graph/activity",
				"type":"Create",
				"actor":"https://remote.test/users/bob",
				"object":{
					"@graph":[
						{"id":"https://remote.test/ignored","type":"Person"},
						{
							"id":"https://remote.test/users/bob/statuses/graph",
							"type":"Note",
							"attributedTo":"https://remote.test/users/bob",
							"content":"<p>graph content</p>"
						}
					]
				}
			}
		]
	}`)))
	if err != nil {
		t.Fatal(err)
	}
	if graphWrapped.ID != "https://remote.test/users/bob/statuses/graph/activity" || graphWrapped.Type != "Create" || graphWrapped.Actor != "https://remote.test/users/bob" {
		t.Fatalf("graph-wrapped activity = %#v", graphWrapped)
	}
	if graphWrapped.Object.ID != "https://remote.test/users/bob/statuses/graph" || graphWrapped.Object.Type != "Note" || graphWrapped.Object.Content != "<p>graph content</p>" {
		t.Fatalf("graph-wrapped object = %#v", graphWrapped.Object)
	}
}

func TestFetchActivityPubFeaturedTagNamesIgnoresInitialIDMismatchLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	requests := []string{}
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		switch r.URL.String() {
		case "https://remote.test/users/owner/collections/tags":
			return textResponse(http.StatusOK, "application/activity+json", activityTestJSON(`{
				"id":"https://remote.test/users/other/collections/tags",
				"type":"Collection",
				"first":"https://remote.test/users/owner/collections/tags?page=1"
			}`)), nil
		case "https://remote.test/users/owner/collections/tags?page=1":
			return textResponse(http.StatusOK, "application/activity+json", `{
				"id":"https://remote.test/users/other/collections/tags?page=1",
				"type":"CollectionPage",
				"items":[{"type":"Hashtag","name":"#Go"}]
			}`), nil
		default:
			t.Fatalf("unexpected fetch URL %q", r.URL.String())
			return nil, nil
		}
	})}

	got, err := fetchActivityPubFeaturedTagNames("https://remote.test/users/owner", "https://remote.test/users/owner/collections/tags", "Paon/test")
	if err != nil {
		t.Fatal(err)
	}
	if want := []activityPubFeaturedTagName{{Normalized: "go", Display: "Go"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("featured tag names = %#v", got)
	}
	if strings.Join(requests, ",") != "https://remote.test/users/owner/collections/tags,https://remote.test/users/owner/collections/tags?page=1" {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestActivityPubFlagReportLockKeyIsPostgreSQLTextSafe(t *testing.T) {
	got := activityPubFlagReportLockKey("https://remote.example/reports/1", 42, 84)
	if strings.ContainsRune(got, '\x00') {
		t.Fatalf("lock key contains PostgreSQL-invalid NUL byte: %q", got)
	}
	if got != "32:https://remote.example/reports/1:42:84" {
		t.Fatalf("lock key = %q", got)
	}
}

func TestActivityPubQuestionClosedAnyOfParsesMultipleExpiredPoll(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"type":"Update",
		"object":{
			"id":"https://remote.test/users/bob/statuses/2",
			"type":"Question",
			"closed":true,
			"anyOf":[{"name":"a"},{"name":"b"}]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 11, 0, 0, 0, time.UTC)
	poll, ok := activityPollFromObject(payload.Object, 42, 99, now)
	if !ok || !poll.Multiple {
		t.Fatalf("poll = %#v ok = %v", poll, ok)
	}
	if !poll.ExpiresAt.Valid || !poll.ExpiresAt.Time.Equal(now) {
		t.Fatalf("closed poll expires_at = %#v", poll.ExpiresAt)
	}
}

func TestActivityPubQuestionExpandedClosedDateParsesPollExpiry(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"@context":["https://www.w3.org/ns/activitystreams"],
		"@type":["as:Update"],
		"as:object":[{
			"@id":"https://remote.test/users/bob/statuses/2",
			"@type":["as:Question"],
			"as:closed":[{"@list":[{"@value":"2026-06-19T12:30:00Z"}]}],
			"as:anyOf":[{"@list":[{"as:name":[{"@value":"a"}]},{"as:name":[{"@value":"b"}]}]}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	poll, ok := activityPollFromObject(payload.Object, 42, 99, time.Date(2026, 6, 19, 11, 0, 0, 0, time.UTC))
	if !ok || !poll.Multiple {
		t.Fatalf("poll = %#v ok = %v", poll, ok)
	}
	if !poll.ExpiresAt.Valid || poll.ExpiresAt.Time.Format(time.RFC3339) != "2026-06-19T12:30:00Z" {
		t.Fatalf("expanded closed poll expires_at = %#v", poll.ExpiresAt)
	}
}

func TestActivityPubQuestionClosedFalseFallsBackToEndTime(t *testing.T) {
	payload, err := parseActivityPayload([]byte(`{
		"type":"Update",
		"object":{
			"id":"https://remote.test/users/bob/statuses/2",
			"type":"Question",
			"closed":false,
			"endTime":"2026-06-19T12:30:00Z",
			"oneOf":[{"name":"a"},{"name":"b"}]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	poll, ok := activityPollFromObject(payload.Object, 42, 99, time.Date(2026, 6, 19, 11, 0, 0, 0, time.UTC))
	if !ok || poll.Multiple {
		t.Fatalf("poll = %#v ok = %v", poll, ok)
	}
	if !poll.ExpiresAt.Valid || poll.ExpiresAt.Time.Format(time.RFC3339) != "2026-06-19T12:30:00Z" {
		t.Fatalf("closed false poll expires_at = %#v", poll.ExpiresAt)
	}
}

func TestActivityPubQuestionBlankClosedStringDoesNotFallbackToEndTimeLikeRails(t *testing.T) {
	for _, rawClosed := range []string{"", "   "} {
		payload, err := parseActivityPayload([]byte(`{
			"type":"Update",
			"object":{
				"id":"https://remote.test/users/bob/statuses/blank-closed",
				"type":"Question",
				"closed":` + strconv.Quote(rawClosed) + `,
				"endTime":"2026-06-19T12:30:00Z",
				"oneOf":[{"name":"a"},{"name":"b"}]
			}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		poll, ok := activityPollFromObject(payload.Object, 42, 99, time.Date(2026, 6, 19, 11, 0, 0, 0, time.UTC))
		if !ok {
			t.Fatalf("poll with closed=%q was not parsed", rawClosed)
		}
		if poll.ExpiresAt.Valid {
			t.Fatalf("closed string %q should parse through Rails to_datetime and not fallback to endTime, got %#v", rawClosed, poll.ExpiresAt)
		}
	}
}

func TestActivityPubPollVoteChoiceMatchesOptionExactly(t *testing.T) {
	poll := models.Poll{Options: models.StringArray{"red", "blue"}}
	if got := activityPollVoteChoice(poll, "blue"); got != 1 {
		t.Fatalf("choice = %d, want 1", got)
	}
	if got := activityPollVoteChoice(poll, " blue "); got != -1 {
		t.Fatalf("choice with whitespace = %d, want -1", got)
	}
	if got := activityPollVoteChoice(poll, "green"); got != -1 {
		t.Fatalf("missing choice = %d, want -1", got)
	}
}

func TestActivityPubDereferenceReturnsMismatchedObjectHostForAsynqArchive(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected dereference request to %s", r.URL.String())
		return nil, nil
	})}

	server := &Server{cfg: config.Config{LocalDomain: "example.com", WebDomain: "example.com"}}
	actor := &models.Account{URI: "https://remote.example/users/alice"}
	createPayload := activityPayload{Type: "Create", Actor: actor.URI, Object: activityObject{ID: "https://evil.example/statuses/1"}}
	if err := server.processActivityPubDereferencedCreate(createPayload, actor, nil, nil, activityPubProcessingOptions{}); !errors.Is(err, errActivityPubEventNotApplied) {
		t.Fatalf("processActivityPubDereferencedCreate error = %v", err)
	}
	updatePayload := activityPayload{Type: "Update", Actor: actor.URI, Object: activityObject{ID: "https://evil.example/statuses/1"}}
	if err := server.processActivityPubDereferencedUpdate(updatePayload, actor, nil, nil, activityPubProcessingOptions{}); !errors.Is(err, errActivityPubEventNotApplied) {
		t.Fatalf("processActivityPubDereferencedUpdate error = %v", err)
	}
	bearcap := "bear:?u=https%3A%2F%2Fevil.example%2Fstatuses%2Fbear&t=secret-token"
	bearcapCreate := activityPayload{Type: "Create", Actor: actor.URI, Object: activityObject{ID: bearcap}}
	if err := server.processActivityPubDereferencedCreate(bearcapCreate, actor, nil, nil, activityPubProcessingOptions{}); !errors.Is(err, errActivityPubEventNotApplied) {
		t.Fatalf("processActivityPubDereferencedCreate bearcap error = %v", err)
	}
	bearcapUpdate := activityPayload{Type: "Update", Actor: actor.URI, Object: activityObject{ID: bearcap}}
	if err := server.processActivityPubDereferencedUpdate(bearcapUpdate, actor, nil, nil, activityPubProcessingOptions{}); !errors.Is(err, errActivityPubEventNotApplied) {
		t.Fatalf("processActivityPubDereferencedUpdate bearcap error = %v", err)
	}
}

func TestActivityPubDereferencedUpdatePayloadClearsReferenceAndKeepsPublished(t *testing.T) {
	payload := activityPayload{
		Type:            "Update",
		ObjectReference: true,
		Object:          activityObject{ID: "https://remote.example/statuses/old"},
	}
	object := activityObject{
		ID:           "https://remote.example/statuses/old",
		Type:         "Note",
		Published:    "2026-06-20T11:59:59Z",
		AttributedTo: "https://remote.example/users/alice",
	}

	dereferenced := activityPubDereferencedUpdatePayload(payload, object)
	if dereferenced.ObjectReference {
		t.Fatal("dereferenced Update must not be dereferenced again")
	}
	if dereferenced.Object.ID != object.ID || dereferenced.Object.Published != object.Published {
		t.Fatalf("dereferenced object = %#v", dereferenced.Object)
	}
	if !payload.ObjectReference {
		t.Fatal("building the dereferenced payload must not mutate the original value")
	}
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if !activityPubUpdateShouldIgnoreUnknownObject(true, dereferenced.Object, now) {
		t.Fatal("an old unknown dereferenced Update must use the same age policy as an embedded object")
	}
}

func TestActivityPubStatusSignificantChangeMatchesRemoteEditBoundaries(t *testing.T) {
	editedAt := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	current := models.Status{
		Text:     "old",
		URL:      sql.NullString{String: "https://remote.example/statuses/1", Valid: true},
		EditedAt: sql.NullTime{Time: editedAt, Valid: true},
	}
	next := current
	if activityPubStatusSignificantChange(current, next, activityObject{}) {
		t.Fatal("unchanged status without updated timestamp should not create edit snapshots")
	}
	changedText := current
	changedText.Text = "new"
	if !activityPubStatusSignificantChange(current, changedText, activityObject{}) {
		t.Fatal("text change should create edit snapshots")
	}
	changedSpoiler := current
	changedSpoiler.SpoilerText = "new cw"
	if !activityPubStatusSignificantChange(current, changedSpoiler, activityObject{}) {
		t.Fatal("spoiler text change should create edit snapshots")
	}
	metadataOnly := current
	metadataOnly.Sensitive = !current.Sensitive
	metadataOnly.Visibility = 1
	metadataOnly.Reply = true
	metadataOnly.URL = sql.NullString{String: "https://remote.example/statuses/1-updated", Valid: true}
	metadataOnly.Language = sql.NullString{String: "ja", Valid: true}
	metadataOnly.InReplyToID = sql.NullInt64{Int64: 123, Valid: true}
	if activityPubStatusSignificantChange(current, metadataOnly, activityObject{}) {
		t.Fatal("metadata-only remote update should not create edit snapshots like Rails")
	}
	if activityPubStatusSignificantChange(current, next, activityObject{Updated: "2026-06-20T00:00:00Z"}) {
		t.Fatal("same remote updated timestamp should not create duplicate edit snapshots")
	}
	if activityPubStatusSignificantChange(current, next, activityObject{Updated: "2026-06-21T00:00:00Z"}) {
		t.Fatal("new remote updated timestamp alone should not create edit snapshots")
	}
}

func TestActivityPubStatusUpdateEditedAtModeMatchesRails(t *testing.T) {
	current := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	status := models.Status{EditedAt: sql.NullTime{Time: current, Valid: true}}
	older := activityPubStatusUpdateEditedAt(activityObject{Updated: current.Add(-time.Minute).Format(time.RFC3339)})
	if !activityPubStatusUpdateIsOlder(status, older) || activityPubStatusUpdateIsExplicit(status, older) {
		t.Fatalf("older update mode mismatch: %#v", older)
	}
	equal := activityPubStatusUpdateEditedAt(activityObject{Updated: current.Format(time.RFC3339)})
	if activityPubStatusUpdateIsOlder(status, equal) || activityPubStatusUpdateIsExplicit(status, equal) {
		t.Fatalf("equal update should be implicit: %#v", equal)
	}
	newer := activityPubStatusUpdateEditedAt(activityObject{Updated: current.Add(time.Minute).Format(time.RFC3339)})
	if activityPubStatusUpdateIsOlder(status, newer) || !activityPubStatusUpdateIsExplicit(status, newer) {
		t.Fatalf("newer update should be explicit: %#v", newer)
	}
	dateOnly := activityPubStatusUpdateEditedAt(activityObject{Updated: "2026-06-23"})
	if !dateOnly.Valid || dateOnly.Time.Format(time.RFC3339) != "2026-06-23T00:00:00Z" || !activityPubStatusUpdateIsExplicit(status, dateOnly) {
		t.Fatalf("date-only updated should parse like Rails StatusParser#edited_at: %#v", dateOnly)
	}
	missing := activityPubStatusUpdateEditedAt(activityObject{})
	if !activityPubStatusUpdateIsOlder(status, missing) || activityPubStatusUpdateIsExplicit(status, missing) {
		t.Fatalf("missing updated should be rejected when current status has edited_at: %#v", missing)
	}
	neverEdited := models.Status{CreatedAt: current}
	beforeCreate := activityPubStatusUpdateEditedAt(activityObject{Updated: current.Add(-time.Minute).Format(time.RFC3339)})
	afterCreate := activityPubStatusUpdateEditedAt(activityObject{Updated: current.Add(time.Minute).Format(time.RFC3339)})
	if !activityPubStatusUpdateIsExplicit(neverEdited, beforeCreate) {
		t.Fatal("updated status with no previous edited_at should still be explicit like Rails ProcessStatusUpdateService")
	}
	if activityPubStatusUpdateShouldForward(neverEdited, beforeCreate) {
		t.Fatal("explicit ActivityPub status Update before created_at must not be forwarded; Rails compares edited_at to last_edit_date")
	}
	if !activityPubStatusUpdateShouldForward(neverEdited, afterCreate) {
		t.Fatal("explicit ActivityPub status Update after created_at should be forwarded like Rails")
	}
	alreadyEdited := models.Status{CreatedAt: current.Add(-time.Hour), EditedAt: sql.NullTime{Time: current, Valid: true}}
	if activityPubStatusUpdateShouldForward(alreadyEdited, equal) || !activityPubStatusUpdateShouldForward(alreadyEdited, newer) {
		t.Fatal("ActivityPub status Update forwarding must use edited_at when present")
	}
	if activityPubStatusUpdateShouldForward(alreadyEdited, missing) {
		t.Fatal("ActivityPub status Update forwarding requires an explicit edited_at")
	}
	if !activityPubStatusUpdateIsExplicit(models.Status{}, newer) {
		t.Fatal("updated status with no previous edited_at should be explicit")
	}
}

func TestActivityPubUpdateUnknownObjectAgePolicyMatchesMastodon428(t *testing.T) {
	if activityPubUpdateUnknownObjectAgeThreshold != 24*time.Hour {
		t.Fatalf("unknown Update object age threshold = %s", activityPubUpdateUnknownObjectAgeThreshold)
	}
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		statusMissing bool
		published     string
		want          bool
	}{
		{name: "older than one day unknown", statusMissing: true, published: now.Add(-24*time.Hour - time.Second).Format(time.RFC3339), want: true},
		{name: "newer than one day unknown", statusMissing: true, published: now.Add(-24*time.Hour + time.Second).Format(time.RFC3339), want: false},
		{name: "exactly one day unknown", statusMissing: true, published: now.Add(-24 * time.Hour).Format(time.RFC3339), want: false},
		{name: "missing published unknown", statusMissing: true, published: "", want: false},
		{name: "invalid published unknown", statusMissing: true, published: "not-a-date", want: false},
		{name: "older than one day known", statusMissing: false, published: now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			object := activityObject{Published: tt.published}
			if got := activityPubUpdateShouldIgnoreUnknownObject(tt.statusMissing, object, now); got != tt.want {
				t.Fatalf("ignore = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActivityPubQuoteObjectRequiresAttributedToLikeRails(t *testing.T) {
	oldClient := activityHTTPClient
	defer func() { activityHTTPClient = oldClient }()

	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/statuses/quoted","type":"Note","content":"quoted"}`
		if r.URL.String() == "https://remote.example/statuses/with-actor" {
			body = `{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/statuses/with-actor","type":"Note","attributedTo":" https://remote.example/users/alice ","content":"quoted"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/activity+json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	server := &Server{cfg: config.Config{Title: "Paon", LocalDomain: "example.com"}}
	if actor, ok := server.activityPubQuoteObjectAttributedTo("https://remote.example/statuses/quoted", nil); ok || actor != "" {
		t.Fatalf("quote without attributedTo should be skipped like Rails, actor=%q ok=%v", actor, ok)
	}
	actor, ok := server.activityPubQuoteObjectAttributedTo("https://remote.example/statuses/with-actor", nil)
	if !ok || actor != " https://remote.example/users/alice " {
		t.Fatalf("quote attributedTo should preserve the Rails raw value, actor=%q ok=%v", actor, ok)
	}
}

func TestActivityPubAnnounceTargetFetchGuardsLocalAndUnsupportedURIs(t *testing.T) {
	server := &Server{cfg: config.Config{WebDomain: "local.example", LocalDomain: "local.example"}}
	for _, uri := range []string{
		"https://local.example/users/alice/statuses/1",
		"bear:?u=https://remote.example/users/alice/statuses/1",
		"ipfs://bafy/status",
		" https://remote.example/users/alice/statuses/1 ",
		"",
	} {
		status, err := server.fetchActivityPubAnnounceTarget(uri, "", "activity-1")
		if err != nil {
			t.Fatalf("fetchActivityPubAnnounceTarget(%q) error = %v", uri, err)
		}
		if status != nil {
			t.Fatalf("fetchActivityPubAnnounceTarget(%q) = %#v, want nil", uri, status)
		}
	}
	status, err := server.fetchActivityPubAnnounceTarget("https://remote.example/users/bob/statuses/1", "", "activity-1")
	if err != nil {
		t.Fatalf("remote fetch with nil db should be a no-op error = %v", err)
	}
	if status != nil {
		t.Fatalf("remote fetch with nil db = %#v, want nil", status)
	}
	status, err = server.fetchActivityPubAnnounceTarget("tag:remote.example,2026:status:1", "https://remote.example/@bob/1", "activity-1")
	if err != nil {
		t.Fatalf("remote URL fallback with nil db should be a no-op error = %v", err)
	}
	if status != nil {
		t.Fatalf("remote URL fallback with nil db = %#v, want nil", status)
	}
}

func TestIncomingFollowDomainBlockUsesCaseInsensitiveRailsDomains(t *testing.T) {
	src, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "rejectIncomingFollow", `Where("account_id = ? AND lower(domain) = lower(?)", target.ID, actor.Domain.String)`) {
		t.Fatal("incoming Follow rejection must use case-insensitive account_domain_blocks comparison")
	}
	if functionBodyContains(t, src, "rejectIncomingFollow", `Where("account_id = ? AND domain = ?", target.ID, actor.Domain.String)`) {
		t.Fatal("incoming Follow rejection must not use a case-sensitive domain comparison")
	}
}
