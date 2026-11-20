package api

import (
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const linkedDataForgeryVictimURI = "https://origin.example/users/alice"

// signedLinkedDataDeleteFixture returns a Delete of one of the victim's
// statuses with a valid RsaSignature2017 linked-data signature by the victim,
// as Mastodon attaches to every Delete.
func signedLinkedDataDeleteFixture(t *testing.T) (map[string]any, *rsa.PublicKey) {
	t.Helper()
	return signedLinkedDataFixture(t, linkedDataDeletePayload)
}

func linkedDataDeletePayload(string) map[string]any {
	return map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       linkedDataForgeryVictimURI + "/statuses/1#delete",
		"type":     "Delete",
		"actor":    linkedDataForgeryVictimURI,
		"to":       []any{activityPubPublicIRI},
		"object": map[string]any{
			"id":   linkedDataForgeryVictimURI + "/statuses/1",
			"type": "Tombstone",
		},
	}
}

// signedLinkedDataFixture signs the payload built from the victim's new public
// key PEM with the victim's RsaSignature2017 linked-data signature.
func signedLinkedDataFixture(t *testing.T, payload func(publicKeyPEM string) map[string]any) (map[string]any, *rsa.PublicKey) {
	t.Helper()
	privateKeyPEM, publicKeyPEM, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := activityPublicKey(publicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	signingServer := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "origin.example", WebDomain: "origin.example"}}
	signer := models.Account{
		Username:   "alice",
		PrivateKey: sql.NullString{String: privateKeyPEM, Valid: true},
		PublicKey:  publicKeyPEM,
	}
	signed, err := signingServer.signActivityPubLinkedDataSignaturePayload(signer, payload(publicKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	return cloneLinkedDataTestDocument(t, signed), publicKey
}

// linkedDataPersonUpdatePayload is the victim's Update of its own actor, which
// carries the key Paon stores for the victim.
func linkedDataPersonUpdatePayload(publicKeyPEM string) map[string]any {
	return map[string]any{
		"@context": []any{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext},
		"id":       linkedDataForgeryVictimURI + "#updates/1",
		"type":     "Update",
		"actor":    linkedDataForgeryVictimURI,
		"to":       []any{activityPubPublicIRI},
		"object": map[string]any{
			"id":                linkedDataForgeryVictimURI,
			"type":              "Person",
			"preferredUsername": "alice",
			"inbox":             linkedDataForgeryVictimURI + "/inbox",
			"publicKey": map[string]any{
				"id":           linkedDataForgeryVictimURI + "#main-key",
				"owner":        linkedDataForgeryVictimURI,
				"publicKeyPem": publicKeyPEM,
			},
		},
	}
}

// linkedDataNoteCreatePayload is a public post by the victim.
func linkedDataNoteCreatePayload(string) map[string]any {
	return map[string]any{
		"@context": []any{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext},
		"id":       linkedDataForgeryVictimURI + "/statuses/3/activity",
		"type":     "Create",
		"actor":    linkedDataForgeryVictimURI,
		"to":       []any{activityPubPublicIRI},
		"object": map[string]any{
			"id":           linkedDataForgeryVictimURI + "/statuses/3",
			"type":         "Note",
			"attributedTo": linkedDataForgeryVictimURI,
			"content":      "signed",
			"to":           []any{activityPubPublicIRI},
		},
	}
}

func linkedDataForgeryRelay() *models.Account {
	return &models.Account{
		ID:     7,
		URI:    "https://attacker.example/actor",
		Domain: sql.NullString{String: "attacker.example", Valid: true},
	}
}

func cloneLinkedDataTestDocument(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	body := marshalLinkedDataTestDocument(t, document)
	var clone map[string]any
	if err := json.Unmarshal(body, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func marshalLinkedDataTestDocument(t *testing.T, document map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// compactLinkedDataTestDocumentDirectly compacts body with Paon's context even
// when activityPubCompactCollectionBody would fall back to the unsigned
// original, so the linked-data signature checks can be tested on their own.
func compactLinkedDataTestDocumentDirectly(t *testing.T, body []byte) []byte {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	compacted, err := activityPubCompactSignedActivityDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	return marshalLinkedDataTestDocument(t, compacted)
}

// forgeLinkedDataObject keeps the signed RDF unchanged while replacing the
// literal "object" key, which Paon reads, with forged: the signed object moves
// under an alias of as:object and "object" is defined as null, so JSON-LD
// expansion silently drops the forged value.
func forgeLinkedDataObject(t *testing.T, document map[string]any, forged any) {
	t.Helper()
	context, ok := document["@context"].([]any)
	if !ok {
		t.Fatalf("signed @context = %#v, want an array", document["@context"])
	}
	document["@context"] = append(append([]any(nil), context...), map[string]any{
		"object":           nil,
		"paonSignedObject": map[string]any{"@id": "as:object", "@type": "@id"},
	})
	document["paonSignedObject"] = document["object"]
	document["object"] = forged
}

var disguisedLinkedDataSignatures = []struct {
	name     string
	disguise func(document map[string]any)
}{
	{name: "array", disguise: func(document map[string]any) {
		document["signature"] = []any{document["signature"]}
	}},
	{name: "sec CURIE", disguise: func(document map[string]any) {
		document["sec:signature"] = document["signature"]
		delete(document, "signature")
	}},
	{name: "security IRI", disguise: func(document map[string]any) {
		document["https://w3id.org/security#signature"] = document["signature"]
		delete(document, "signature")
	}},
}

func TestLinkedDataNullContextForgeryNeedsCompactionBypass(t *testing.T) {
	document, publicKey := signedLinkedDataDeleteFixture(t)
	forgeLinkedDataObject(t, document, linkedDataForgeryVictimURI)
	body := marshalLinkedDataTestDocument(t, document)

	// The forged document still canonicalizes to the signed RDF and its literal
	// keys name the victim actor, so it must never be verified uncompacted.
	if err := verifyActivityPubLinkedDataSignatureWithError(body, publicKey); err != nil {
		t.Fatalf("forgery fixture no longer preserves the signed RDF: %v", err)
	}
	if uncompacted, err := parseActivityPayload(body); err != nil || uncompacted.Object.ID != linkedDataForgeryVictimURI {
		t.Fatalf("uncompacted forged object = %#v, %v", uncompacted.Object, err)
	}

	// With a literal signature object the document is compacted first, which
	// restores the object that was actually signed.
	compactedBody, compacted := activityPubCompactCollectionBody(body)
	if !compacted {
		t.Fatal("document with a literal signature object was not compacted")
	}
	payload, err := parseActivityPayload(compactedBody)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Signature.Present || payload.Object.ID != linkedDataForgeryVictimURI+"/statuses/1" || payload.Object.TypeExact != "Tombstone" {
		t.Fatalf("compacted payload signature=%t object=%#v", payload.Signature.Present, payload.Object)
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
		t.Fatalf("compacted document lost its signature: %v", err)
	}
}

func TestLinkedDataSignatureRequiresLiteralSignatureObject(t *testing.T) {
	relay := &models.Account{
		ID:     7,
		URI:    "https://attacker.example/actor",
		Domain: sql.NullString{String: "attacker.example", Valid: true},
	}
	// Rejection must happen before signature creator lookup touches the
	// database; reaching it here would panic on the zero-value handle.
	server := &Server{db: &gorm.DB{}}
	distributable := models.Status{Visibility: 0}
	for _, forged := range []bool{false, true} {
		for _, test := range disguisedLinkedDataSignatures {
			name := test.name
			if forged {
				name += " with null-context forgery"
			}
			t.Run(name, func(t *testing.T) {
				document, publicKey := signedLinkedDataDeleteFixture(t)
				if forged {
					forgeLinkedDataObject(t, document, linkedDataForgeryVictimURI)
				}
				test.disguise(document)
				body := marshalLinkedDataTestDocument(t, document)

				if _, ok := activityPubLinkedDataSignatureMap(document); ok {
					t.Fatal("disguised signature was treated as a linked-data signature")
				}
				payload, err := parseActivityPayload(body)
				if err != nil {
					t.Fatal(err)
				}
				if payload.Signature.Present {
					t.Fatalf("disguised signature is present: %#v", payload.Signature)
				}
				if _, compacted := activityPubCompactCollectionBody(body); compacted {
					t.Fatal("document without a literal signature object was compacted")
				}
				if err := verifyActivityPubLinkedDataSignatureWithError(body, publicKey); err == nil {
					t.Fatal("disguised signature verified")
				}
				if activityPubStatusForwardable(distributable, body) {
					t.Fatal("document with a disguised signature is forwardable")
				}
				err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, relay, nil, 0)
				if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "not JSON-LD compacted") {
					t.Fatalf("relayed activity error = %v, want rejection before linked-data verification", err)
				}
			})
		}
	}
}

// A {"@list": [...]} wrapper is itself a JSON object, so like Mastodon's
// `is_a?(Hash)` the document is compacted, but the wrapped signature is never
// unwrapped: its literal type is missing and verification fails.
func TestLinkedDataSignatureListWrapperIsNotHonored(t *testing.T) {
	relay := &models.Account{
		ID:     7,
		URI:    "https://attacker.example/actor",
		Domain: sql.NullString{String: "attacker.example", Valid: true},
	}
	server := &Server{db: &gorm.DB{}}
	for _, forged := range []bool{false, true} {
		name := "plain"
		if forged {
			name = "with null-context forgery"
		}
		t.Run(name, func(t *testing.T) {
			document, publicKey := signedLinkedDataDeleteFixture(t)
			if forged {
				forgeLinkedDataObject(t, document, linkedDataForgeryVictimURI)
			}
			document["signature"] = map[string]any{"@list": []any{document["signature"]}}
			body := marshalLinkedDataTestDocument(t, document)

			compactedBody, compacted := activityPubCompactCollectionBody(body)
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			payload, err := parseActivityPayload(compactedBody)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Signature.Type != "" || payload.Signature.Creator != "" || payload.Signature.SignatureValue != "" {
				t.Fatalf("signature inside @list was unwrapped: %#v", payload.Signature)
			}
			if payload.Object.ID != linkedDataForgeryVictimURI+"/statuses/1" {
				t.Fatalf("compacted object = %#v, want the signed Tombstone", payload.Object)
			}
			for _, candidate := range [][]byte{body, compactedBody} {
				if err := verifyActivityPubLinkedDataSignatureWithError(candidate, publicKey); err == nil {
					t.Fatal("signature inside @list verified")
				}
			}
			err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, relay, nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "unsupported linked-data signature type") {
				t.Fatalf("relayed activity error = %v, want linked-data signature rejection", err)
			}
		})
	}
}

func TestLinkedDataSignatureLiteralObjectRemainsAccepted(t *testing.T) {
	document, publicKey := signedLinkedDataDeleteFixture(t)
	body := marshalLinkedDataTestDocument(t, document)
	if _, ok := activityPubLinkedDataSignatureMap(document); !ok {
		t.Fatal("literal signature object was not detected")
	}
	compactedBody, compacted := activityPubCompactCollectionBody(body)
	if !compacted {
		t.Fatal("literal signature object was not compacted")
	}
	payload, err := parseActivityPayload(compactedBody)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Signature.Present || payload.Signature.Type != "RsaSignature2017" || payload.Signature.Creator != linkedDataForgeryVictimURI+"#main-key" {
		t.Fatalf("literal signature = %#v", payload.Signature)
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
		t.Fatalf("literal signature did not verify: %v", err)
	}
	if !activityPubStatusForwardable(models.Status{Visibility: 1}, compactedBody) {
		t.Fatal("verified unlisted activity is not forwardable")
	}
	if activityPubStatusForwardable(models.Status{Visibility: 2}, compactedBody) {
		t.Fatal("private activity is forwardable")
	}
	empty := cloneLinkedDataTestDocument(t, document)
	empty["signature"] = map[string]any{}
	if activityPubStatusForwardable(models.Status{Visibility: 0}, marshalLinkedDataTestDocument(t, empty)) {
		t.Fatal("empty signature object is forwardable")
	}
}

func TestLinkedDataSignatureCoversSignatureAliasKeys(t *testing.T) {
	for _, key := range []string{"as:signature", "http://w3id.org/security#signature"} {
		t.Run(key, func(t *testing.T) {
			document, publicKey := signedLinkedDataDeleteFixture(t)
			document[key] = "unsigned value"
			compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
			if !compacted {
				t.Fatal("literal signature object was not compacted")
			}
			if !strings.Contains(string(compactedBody), key) {
				t.Fatalf("compaction removed %q: %s", key, compactedBody)
			}
			if verifyActivityPubLinkedDataSignature(compactedBody, publicKey) {
				t.Fatalf("unsigned %q was excluded from the signed document", key)
			}
		})
	}
}

func TestLinkedDataSignatureIsDetectedOnlyAtTopLevel(t *testing.T) {
	document, _ := signedLinkedDataDeleteFixture(t)
	signature := document["signature"]
	activity := cloneLinkedDataTestDocument(t, document)
	delete(activity, "@context")

	nested := map[string]any{"@context": document["@context"], "@graph": []any{activity}}
	body := marshalLinkedDataTestDocument(t, nested)
	payload, err := parseActivityPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Delete" || payload.Signature.Present {
		t.Fatalf("signature inside @graph: type=%q present=%t", payload.Type, payload.Signature.Present)
	}
	if _, compacted := activityPubCompactCollectionBody(body); compacted {
		t.Fatal("signature inside @graph caused compaction")
	}

	// A top-level signature covers the whole graph, not the node Paon picks
	// from @graph, so that node is never authenticated by it.
	delete(activity, "signature")
	topLevel := map[string]any{"@context": document["@context"], "@graph": []any{activity}, "signature": signature}
	payload, err = parseActivityPayload(marshalLinkedDataTestDocument(t, topLevel))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Delete" || payload.Signature.Present {
		t.Fatalf("top-level signature with @graph: type=%q signature=%#v", payload.Type, payload.Signature)
	}
}

// Flattening the victim's signed Undo(Announce) into @graph keeps the signed
// RDF, but Paon would pick the Announce from the graph and redo the boost.
func TestLinkedDataSignatureDoesNotAuthenticateGraphNode(t *testing.T) {
	const announceID = linkedDataForgeryVictimURI + "/statuses/2/activity"
	document, publicKey := signedLinkedDataFixture(t, func(string) map[string]any {
		return map[string]any{
			"@context": "https://www.w3.org/ns/activitystreams",
			"id":       announceID + "#undo",
			"type":     "Undo",
			"actor":    linkedDataForgeryVictimURI,
			"to":       []any{activityPubPublicIRI},
			"object": map[string]any{
				"id":     announceID,
				"type":   "Announce",
				"actor":  linkedDataForgeryVictimURI,
				"to":     []any{activityPubPublicIRI},
				"object": "https://other.example/statuses/1",
			},
		}
	})
	undo := cloneLinkedDataTestDocument(t, document)
	announce := undo["object"].(map[string]any)
	undo["object"] = announceID
	delete(undo, "@context")
	delete(undo, "signature")
	flattened := map[string]any{
		"@context":  document["@context"],
		"@graph":    []any{announce, undo},
		"signature": document["signature"],
	}
	body := marshalLinkedDataTestDocument(t, flattened)

	// The signature still covers the flattened RDF once compacted; only the
	// @graph checks keep it from authenticating the node picked from the graph.
	signedBody := compactLinkedDataTestDocumentDirectly(t, body)
	if err := verifyActivityPubLinkedDataSignatureWithError(signedBody, publicKey); err != nil {
		t.Fatalf("flattened fixture no longer preserves the signed RDF: %v", err)
	}
	if err := activityPubLinkedDataSignatureCoversDocument(signedBody); err == nil || !strings.Contains(err.Error(), "does not cover the document") {
		t.Fatalf("coverage error = %v, want rejection of @graph", err)
	}
	// Like Mastodon's ProcessCollectionService, the inbox processes such a
	// document as the unsigned original instead of compacting it.
	compactedBody, compacted := activityPubCompactCollectionBody(body)
	if compacted {
		t.Fatal("document using @graph was compacted for linked-data signature verification")
	}
	payload, err := parseActivityPayload(compactedBody)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "Announce" || payload.Signature.Present {
		t.Fatalf("flattened payload type=%q signature=%#v", payload.Type, payload.Signature)
	}
	server := &Server{db: &gorm.DB{}}
	err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, linkedDataForgeryRelay(), nil, 0)
	if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature is missing") {
		t.Fatalf("relayed activity error = %v, want linked-data signature rejection", err)
	}
}

// json-gold leaves nodes with a relative or malformed @id, malformed IRIs and
// malformed language tags out of the canonicalized RDF. Added to a compacted
// document they keep the victim's signature valid while Paon reads them.
func TestLinkedDataSignatureRejectsContentOutsideCanonicalizedRDF(t *testing.T) {
	_, attackerPublicKeyPEM, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	object := func(document map[string]any) map[string]any {
		return document["object"].(map[string]any)
	}
	injectKey := func(id string) func(map[string]any) {
		return func(document map[string]any) {
			person := object(document)
			person["publicKey"] = []any{
				map[string]any{"id": id, "owner": linkedDataForgeryVictimURI, "publicKeyPem": attackerPublicKeyPEM},
				person["publicKey"],
			}
		}
	}
	readsAttackerKey := func(payload activityPayload) bool {
		return payload.Object.PublicKey == attackerPublicKeyPEM
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		inject  func(document map[string]any)
		reads   func(payload activityPayload) bool
	}{
		{name: "publicKey with relative id", payload: linkedDataPersonUpdatePayload, inject: injectKey("injected-key"), reads: readsAttackerKey},
		{name: "publicKey with empty id", payload: linkedDataPersonUpdatePayload, inject: injectKey(""), reads: readsAttackerKey},
		{name: "publicKey with network-path id", payload: linkedDataPersonUpdatePayload, inject: injectKey("//origin.example/key"), reads: readsAttackerKey},
		{name: "publicKey with malformed host", payload: linkedDataPersonUpdatePayload, inject: injectKey("https://origin .example/key"), reads: readsAttackerKey},
		{name: "publicKey with whitespace in IRI", payload: linkedDataPersonUpdatePayload, inject: injectKey(linkedDataForgeryVictimURI + "#main key"), reads: readsAttackerKey},
		{
			name:    "Mention with relative id",
			payload: linkedDataNoteCreatePayload,
			inject: func(document map[string]any) {
				object(document)["tag"] = []any{map[string]any{"id": "mention", "type": "Mention", "href": "https://paon.example/users/bob", "name": "@bob@paon.example"}}
			},
			reads: func(payload activityPayload) bool {
				return len(payload.Object.Tags) == 1 && payload.Object.Tags[0].Href == "https://paon.example/users/bob"
			},
		},
		{
			name:    "attachment with malformed id",
			payload: linkedDataNoteCreatePayload,
			inject: func(document map[string]any) {
				object(document)["attachment"] = []any{map[string]any{"id": "https://exa mple/x", "type": "Document", "mediaType": "image/png", "url": "https://attacker.example/x.png"}}
			},
			reads: func(payload activityPayload) bool {
				return len(payload.Object.Attachments) == 1 && payload.Object.Attachments[0].URL == "https://attacker.example/x.png"
			},
		},
		{
			name:    "content with malformed language tag",
			payload: linkedDataNoteCreatePayload,
			inject: func(document map[string]any) {
				object(document)["contentMap"] = map[string]any{"x y": "forged"}
			},
			reads: func(payload activityPayload) bool {
				return payload.Object.ContentMap["x y"] == "forged"
			},
		},
		{
			name:    "type with malformed IRI",
			payload: linkedDataNoteCreatePayload,
			inject: func(document map[string]any) {
				object(document)["type"] = []any{"https://exa mple/Type", "Note"}
			},
			reads: func(payload activityPayload) bool {
				return payload.Object.TypeExact != "Note"
			},
		},
		{
			name: "object reference with malformed IRI",
			payload: func(string) map[string]any {
				return map[string]any{
					"@context": "https://www.w3.org/ns/activitystreams",
					"id":       linkedDataForgeryVictimURI + "#delete",
					"type":     "Delete",
					"actor":    linkedDataForgeryVictimURI,
					"to":       []any{activityPubPublicIRI},
					"object":   linkedDataForgeryVictimURI + "/statuses/1",
				}
			},
			inject: func(document map[string]any) {
				document["object"] = []any{linkedDataForgeryVictimURI + "/statuses/2 ", document["object"]}
			},
			reads: func(payload activityPayload) bool {
				return len(payload.ObjectIDRaws) == 2
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			test.inject(document)
			body := marshalLinkedDataTestDocument(t, document)

			// The injected content is outside the canonicalized RDF: the
			// compacted document still verifies with the victim's key, and Paon
			// reads the injected content from it.
			compactedBody, compacted := activityPubCompactCollectionBody(body)
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("injection changed the signed RDF: %v", err)
			}
			payload, err := parseActivityPayload(compactedBody)
			if err != nil {
				t.Fatal(err)
			}
			if !payload.Signature.Present || !test.reads(payload) {
				t.Fatalf("compacted payload does not carry the injected content: %#v", payload)
			}

			if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err == nil {
				t.Fatal("content outside the signed RDF was accepted")
			}
			// Rejection must happen before signature creator lookup touches the
			// database; reaching it here would panic on the zero-value handle.
			server := &Server{db: &gorm.DB{}}
			err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, linkedDataForgeryRelay(), nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature does not cover the document") {
				t.Fatalf("relayed activity error = %v, want rejection of content outside the signed RDF", err)
			}
		})
	}
}

func TestLinkedDataSignatureCoversSignedDocuments(t *testing.T) {
	note := func(string) map[string]any {
		payload := linkedDataNoteCreatePayload("")
		payload["@context"] = append(payload["@context"].([]any), map[string]any{
			"sensitive": "as:sensitive",
			"Hashtag":   "as:Hashtag",
			"toot":      "http://joinmastodon.org/ns#",
			"Emoji":     "toot:Emoji",
		})
		object := payload["object"].(map[string]any)
		object["contentMap"] = map[string]any{"ja": "signed", "zh-Hant-TW": "signed"}
		object["sensitive"] = false
		object["inReplyTo"] = nil
		object["tag"] = []any{
			map[string]any{"type": "Mention", "href": "https://paon.example/users/bob", "name": "@bob@paon.example"},
			map[string]any{"type": "Hashtag", "href": "https://origin.example/tags/日本語", "name": "#日本語"},
			map[string]any{
				"id":   "https://origin.example/emojis/1",
				"type": "Emoji",
				"name": ":paon:",
				"icon": map[string]any{"type": "Image", "mediaType": "image/png", "url": "https://files.origin.example/emojis/paon.png"},
			},
		}
		object["attachment"] = []any{map[string]any{"type": "Document", "mediaType": "image/png", "url": "https://files.origin.example/media/1.png?v=1", "name": "alt text"}}
		// Terms no context defines are never part of the signed RDF; like
		// Mastodon, they are not inspected.
		object["_misskey_content"] = "unsigned extension"
		object["extension"] = map[string]any{"id": "relative-extension", "name": "unsigned"}
		return payload
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
	}{
		{name: "Delete", payload: linkedDataDeletePayload},
		{name: "Update Person", payload: linkedDataPersonUpdatePayload},
		{name: "Create Note", payload: note},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("compacted signature: %v", err)
			}
			if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err != nil {
				t.Fatalf("signed document was rejected: %v", err)
			}
		})
	}
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "Misskey Delete", body: misskeyDeleteBody},
		{name: "Misskey Square Delete", body: misskeySquareDeleteBody},
	} {
		t.Run(test.name, func(t *testing.T) {
			compactedBody, compacted := activityPubCompactCollectionBody([]byte(test.body))
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err != nil {
				t.Fatalf("signed document was rejected: %v", err)
			}
		})
	}
}

// A literal signature whose document fails JSON-LD compaction falls back to
// the document without its signature, which never authenticates another actor.
func TestLinkedDataSignatureCompactionFailureIsRejected(t *testing.T) {
	document, _ := signedLinkedDataDeleteFixture(t)
	context, ok := document["@context"].([]any)
	if !ok {
		t.Fatalf("signed @context = %#v, want an array", document["@context"])
	}
	document["@context"] = append(append([]any(nil), context...), map[string]any{"broken": map[string]any{"@id": true}})
	body := marshalLinkedDataTestDocument(t, document)
	if _, compacted := activityPubCompactCollectionBody(body); compacted {
		t.Fatal("document with an invalid context was compacted")
	}
	server := &Server{db: &gorm.DB{}}
	err := server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, linkedDataForgeryRelay(), nil, 0)
	if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "not JSON-LD compacted") {
		t.Fatalf("relayed activity error = %v, want rejection before linked-data verification", err)
	}
}

// Like Mastodon 0786c1e57a, a compacted document holding @graph, @included or
// @reverse anywhere is never authenticated by its linked-data signature. Each
// case keeps the victim's signed RDF unchanged while the JSON tree Paon reads
// differs from it.
func TestLinkedDataSignatureRejectsUnsupportedJSONLDKeywords(t *testing.T) {
	const announceID = linkedDataForgeryVictimURI + "/statuses/2/activity"
	undoAnnounce := func(string) map[string]any {
		return map[string]any{
			"@context": "https://www.w3.org/ns/activitystreams",
			"id":       announceID + "#undo",
			"type":     "Undo",
			"actor":    linkedDataForgeryVictimURI,
			"to":       []any{activityPubPublicIRI},
			"object": map[string]any{
				"id":     announceID,
				"type":   "Announce",
				"actor":  linkedDataForgeryVictimURI,
				"to":     []any{activityPubPublicIRI},
				"object": "https://other.example/statuses/1",
			},
		}
	}
	announce := func(string) map[string]any {
		return map[string]any{
			"@context": "https://www.w3.org/ns/activitystreams",
			"id":       linkedDataForgeryVictimURI + "/statuses/5/activity",
			"type":     "Announce",
			"actor":    linkedDataForgeryVictimURI,
			"to":       []any{activityPubPublicIRI},
			"object":   "https://other.example/statuses/1",
		}
	}
	nestedGraph := func(id string) func(map[string]any) map[string]any {
		return func(document map[string]any) map[string]any {
			object, ok := document["object"].(map[string]any)
			if !ok {
				object = map[string]any{"id": document["object"]}
				document["object"] = object
			}
			object["@graph"] = []any{map[string]any{"id": id, "@index": "x"}}
			return document
		}
	}
	for _, test := range []struct {
		name    string
		keyword string
		payload func(publicKeyPEM string) map[string]any
		forge   func(document map[string]any) map[string]any
	}{
		{name: "nested @graph turns a status Delete into Delete(actor)", keyword: "@graph", payload: linkedDataDeletePayload, forge: nestedGraph(linkedDataForgeryVictimURI)},
		{name: "nested @graph with an undefined term", keyword: "@graph", payload: linkedDataDeletePayload, forge: func(document map[string]any) map[string]any {
			document["object"].(map[string]any)["@graph"] = []any{map[string]any{"id": linkedDataForgeryVictimURI, "paonUndefinedTerm": "x"}}
			return document
		}},
		{name: "nested @graph retargets an Announce", keyword: "@graph", payload: announce, forge: nestedGraph("https://attacker.example/statuses/spam")},
		{name: "nested @graph retargets an Undo", keyword: "@graph", payload: undoAnnounce, forge: nestedGraph(linkedDataForgeryVictimURI + "#likes/42")},
		{name: "@reverse reroots an Undo into its Announce", keyword: "@reverse", payload: undoAnnounce, forge: func(document map[string]any) map[string]any {
			undo := cloneLinkedDataTestDocument(t, document)
			root := undo["object"].(map[string]any)
			undo["object"] = announceID
			delete(undo, "@context")
			delete(undo, "signature")
			root["@context"] = document["@context"]
			root["@reverse"] = map[string]any{"as:object": undo}
			root["signature"] = document["signature"]
			return root
		}},
		{name: "@included hides signed content", keyword: "@included", payload: linkedDataNoteCreatePayload, forge: func(document map[string]any) map[string]any {
			object := document["object"].(map[string]any)
			content := object["content"]
			delete(object, "content")
			object["@included"] = []any{map[string]any{"id": object["id"], "content": content}}
			return document
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			body := marshalLinkedDataTestDocument(t, test.forge(document))

			signedBody := compactLinkedDataTestDocumentDirectly(t, body)
			if err := verifyActivityPubLinkedDataSignatureWithError(signedBody, publicKey); err != nil {
				t.Fatalf("forgery no longer preserves the signed RDF: %v", err)
			}
			err := activityPubLinkedDataSignatureCoversDocument(signedBody)
			if err == nil || !strings.Contains(err.Error(), "does not cover the document") {
				t.Fatalf("coverage error = %v, want rejection of %s", err, test.keyword)
			}
			// The inbox never compacts such a document for verification; like
			// Mastodon's ProcessCollectionService it drops the signature first.
			compactedBody, compacted := activityPubCompactCollectionBody(body)
			if compacted {
				t.Fatalf("document using %s was compacted for linked-data signature verification", test.keyword)
			}
			var processed map[string]any
			if err := json.Unmarshal(compactedBody, &processed); err != nil {
				t.Fatal(err)
			}
			if _, ok := processed["signature"]; ok {
				t.Fatalf("linked-data signature survived the %s fallback", test.keyword)
			}
			// Rejection must happen before signature creator lookup touches the
			// database; reaching it here would panic on the zero-value handle.
			server := &Server{db: &gorm.DB{}}
			err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, linkedDataForgeryRelay(), nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) {
				t.Fatalf("relayed activity error = %v, want rejection before creator lookup", err)
			}
		})
	}
}

// linkedDataAttributionDomainsPersonPayload returns the victim's Update(Person)
// whose attributionDomains term has the given definition, as Mastodon
// publishes it.
func linkedDataAttributionDomainsPersonPayload(definition map[string]any, domains ...any) func(publicKeyPEM string) map[string]any {
	return func(publicKeyPEM string) map[string]any {
		payload := linkedDataPersonUpdatePayload(publicKeyPEM)
		payload["@context"] = append(payload["@context"].([]any), map[string]any{
			"toot":               "http://joinmastodon.org/ns#",
			"attributionDomains": definition,
		})
		payload["object"].(map[string]any)["attributionDomains"] = domains
		return payload
	}
}

// Mastodon 4.3.0 to 4.4.17 and 4.5.0 to 4.5.10 relay Update(Person) with
// attributionDomains as bare domains under an @id term; RDF conversion drops
// them, but Paon never reads such references, so the signed document is still
// accepted. Content Paon does read keeps being checked next to it, including
// literal attributionDomains values.
func TestLinkedDataSignatureAcceptsMastodonAttributionDomains(t *testing.T) {
	_, attackerPublicKeyPEM, err := generateAccountKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	person := linkedDataAttributionDomainsPersonPayload(map[string]any{"@id": "toot:attributionDomains", "@type": "@id"}, "origin.example", "news.example")
	document, publicKey := signedLinkedDataFixture(t, person)
	compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
	if !compacted {
		t.Fatal("document with a literal signature object was not compacted")
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
		t.Fatalf("compacted signature: %v", err)
	}
	if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err != nil {
		t.Fatalf("signed Update(Person) with attributionDomains was rejected: %v", err)
	}
	payload, err := parseActivityPayload(compactedBody)
	if err != nil {
		t.Fatal(err)
	}
	if domains := activityLimitedStringList(payload.Object.AttributionDomains, 256); len(domains) != 0 {
		t.Fatalf("attributionDomains node references were read as %#v", domains)
	}

	forged, publicKey := signedLinkedDataFixture(t, person)
	object := forged["object"].(map[string]any)
	object["publicKey"] = []any{
		map[string]any{"id": "injected-key", "owner": linkedDataForgeryVictimURI, "publicKeyPem": attackerPublicKeyPEM},
		object["publicKey"],
	}
	compactedBody, compacted = activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, forged))
	if !compacted {
		t.Fatal("forged document was not compacted")
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
		t.Fatalf("injection changed the signed RDF: %v", err)
	}
	if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err == nil || !strings.Contains(err.Error(), "does not cover the document") {
		t.Fatalf("coverage error = %v, want rejection of the injected relative-id key", err)
	}
}

// Mastodon 4.4.18 and later and 4.5.11 and later publish attributionDomains as
// literal domains in a set. Paon stores them (activityLimitedStringList) and
// trusts them for link card author attribution, so signed literals are read,
// and a literal that RDF conversion drops (here an invalid language tag) must
// not ride along unsigned, whichever form the signed document uses.
func TestLinkedDataSignatureChecksLiteralAttributionDomains(t *testing.T) {
	literalDomains := linkedDataAttributionDomainsPersonPayload(map[string]any{"@id": "toot:attributionDomains", "@container": "@set"}, "origin.example", map[string]any{"@value": "news.example"})
	referencedDomains := linkedDataAttributionDomainsPersonPayload(map[string]any{"@id": "toot:attributionDomains", "@type": "@id"}, "origin.example")

	document, publicKey := signedLinkedDataFixture(t, literalDomains)
	compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
	if !compacted {
		t.Fatal("document with a literal signature object was not compacted")
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
		t.Fatalf("compacted signature: %v", err)
	}
	if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err != nil {
		t.Fatalf("signed Update(Person) with literal attributionDomains was rejected: %v", err)
	}
	payload, err := parseActivityPayload(compactedBody)
	if err != nil {
		t.Fatal(err)
	}
	if domains := activityLimitedStringList(payload.Object.AttributionDomains, 256); !slices.Equal(domains, []string{"origin.example", "news.example"}) {
		t.Fatalf("signed attributionDomains = %#v", domains)
	}

	uncanonicalized := map[string]any{"@value": "attacker.example", "@language": "x_y"}
	appendUncanonicalized := func(object map[string]any) {
		object["attributionDomains"] = append(object["attributionDomains"].([]any), uncanonicalized)
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		inject  func(object map[string]any)
	}{
		{name: "next to signed literals", payload: literalDomains, inject: appendUncanonicalized},
		{name: "next to signed references", payload: referencedDomains, inject: appendUncanonicalized},
		{name: "under the expanded IRI", payload: linkedDataPersonUpdatePayload, inject: func(object map[string]any) {
			object[activityPubMastodonAttributionDomainsIRI] = []any{uncanonicalized}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			test.inject(document["object"].(map[string]any))
			compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
			if !compacted {
				t.Fatal("forged attributionDomains document was not compacted")
			}
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("injected literal changed the signed RDF: %v", err)
			}
			payload, err := parseActivityPayload(compactedBody)
			if err != nil {
				t.Fatal(err)
			}
			if domains := activityLimitedStringList(payload.Object.AttributionDomains, 256); !slices.Contains(domains, "attacker.example") {
				t.Fatalf("parsed attributionDomains = %#v, want the injected literal to be read", domains)
			}
			if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err == nil || !strings.Contains(err.Error(), "does not cover the document") {
				t.Fatalf("coverage error = %v, want rejection of the uncanonicalized attributionDomains literal", err)
			}
			// Rejection must happen before signature creator lookup touches the
			// database; reaching it here would panic on the zero-value handle.
			server := &Server{db: &gorm.DB{}}
			err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), marshalLinkedDataTestDocument(t, document), linkedDataForgeryRelay(), nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature does not cover the document") {
				t.Fatalf("relayed activity error = %v, want rejection of the uncanonicalized attributionDomains literal", err)
			}
		})
	}
}

// A property that expands to the blank node identifier "_:attributionDomains"
// (an undefined term under the ActivityStreams "@vocab": "_:", or that key
// itself) is dropped by RDF conversion, so the signature does not cover it.
// Mastodon v4.5.15 compacts with a context that defines attributionDomains
// (ContextHelper#full_context), so such a property stays "_:attributionDomains"
// and ProcessAccountService never reads it. Paon must not read it either.
// Node references, which RDF conversion also drops when their @id is relative,
// are never read as attribution domains.
func TestLinkedDataSignatureIgnoresBlankNodeAttributionDomains(t *testing.T) {
	// Mastodon 4.4.18 and later and 4.5.11 and later publish literal domains
	// in a set.
	literalDomains := linkedDataAttributionDomainsPersonPayload(map[string]any{"@id": "toot:attributionDomains", "@container": "@set"}, "origin.example")
	// Earlier Mastodon 4.3, 4.4 and 4.5 releases publish them as node
	// references, which nobody reads.
	referencedDomains := linkedDataAttributionDomainsPersonPayload(map[string]any{"@id": "toot:attributionDomains", "@type": "@id"}, "origin.example")
	undefinedTerm := func(object map[string]any) {
		object["attributionDomains"] = []any{"attacker.example"}
	}
	blankNodeProperty := func(object map[string]any) {
		object["_:attributionDomains"] = []any{"attacker.example"}
	}
	nodeReference := func(object map[string]any) {
		object["attributionDomains"] = append(object["attributionDomains"].([]any), map[string]any{"id": "attacker.example"})
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		inject  func(object map[string]any)
		want    []string
	}{
		{name: "undefined term", payload: linkedDataPersonUpdatePayload, inject: undefinedTerm},
		{name: "blank node property", payload: linkedDataPersonUpdatePayload, inject: blankNodeProperty},
		{name: "blank node property next to signed literals", payload: literalDomains, inject: blankNodeProperty, want: []string{"origin.example"}},
		{name: "blank node property next to signed references", payload: referencedDomains, inject: blankNodeProperty},
		{name: "node reference next to signed literals", payload: literalDomains, inject: nodeReference, want: []string{"origin.example"}},
		{name: "references replacing signed references", payload: referencedDomains, inject: undefinedTerm},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			test.inject(document["object"].(map[string]any))
			compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			// The injected property is outside the signed RDF.
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("injection changed the signed RDF: %v", err)
			}
			if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err != nil {
				// Rejecting the document also keeps the injected value out.
				return
			}
			payload, err := parseActivityPayload(compactedBody)
			if err != nil {
				t.Fatal(err)
			}
			if domains := activityLimitedStringList(payload.Object.AttributionDomains, 256); !slices.Equal(domains, test.want) {
				t.Fatalf("attributionDomains = %#v, want only the signed %#v", domains, test.want)
			}
		})
	}
}

// json-gold compacts a blank node property such as "_:@context" against the
// ActivityStreams "@vocab": "_:" to the keyword "@context", so a relay can put
// an embedded context into the compacted document. Re-expanded for
// canonicalization, that context retypes or nulls terms Paon reads: the values
// under them leave the signed RDF, and the victim's signature stays valid. Such
// a document is never authenticated. An embedded context of the original
// document is still fine: compaction only keeps Paon's top-level context.
func TestLinkedDataSignatureRejectsEmbeddedContext(t *testing.T) {
	const attackerContextURI = "https://attacker.example/context"
	forgetAttackerContext := func() {
		activityPubJSONLDContextCache.Lock()
		delete(activityPubJSONLDContextCache.entries, attackerContextURI)
		activityPubJSONLDContextCache.Unlock()
	}
	forgetAttackerContext()
	t.Cleanup(forgetAttackerContext)
	oldClient := activityHTTPClient
	oldProxy := activityHTTPProxyConfigured
	t.Cleanup(func() {
		activityHTTPClient = oldClient
		activityHTTPProxyConfigured = oldProxy
	})
	activityHTTPProxyConfigured = true
	// Nothing is preloaded: the compaction of the relayed document itself
	// fetches and caches the attacker's context, which the verification and
	// coverage loaders then resolve from that cache.
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != attackerContextURI {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/ld+json"}},
			Body:       io.NopCloser(strings.NewReader(`{"@context":{"attributionDomains":null,"movedTo":null}}`)),
			Request:    r,
		}, nil
	})}

	const mallory = "https://attacker.example/users/mallory"
	referencedDomains := linkedDataAttributionDomainsPersonPayload(map[string]any{"@id": "toot:attributionDomains", "@type": "@id"}, "origin.example")
	readsAttributionDomains := func(payload activityPayload) bool {
		return slices.Contains(activityLimitedStringList(payload.Object.AttributionDomains, 256), "attacker.example")
	}
	retypedDomains := func(key string) func(document map[string]any) {
		return func(document map[string]any) {
			object := document["object"].(map[string]any)
			delete(object, "attributionDomains")
			object["_:@context"] = map[string]any{key: map[string]any{"_:@type": "@id"}}
			object[key] = []any{"attacker.example"}
		}
	}
	fetchedContext := func(key string, value any) func(document map[string]any) {
		return func(document map[string]any) {
			document["@context"] = append(document["@context"].([]any), attackerContextURI)
			object := document["object"].(map[string]any)
			object["_:@context"] = attackerContextURI
			object[key] = value
		}
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		inject  func(document map[string]any)
		reads   func(payload activityPayload) bool
	}{
		{
			name:    "retyped attributionDomains CURIE",
			payload: linkedDataPersonUpdatePayload,
			inject:  retypedDomains("_:toot:attributionDomains"),
			reads:   readsAttributionDomains,
		},
		{
			name:    "retyped attributionDomains IRI replacing signed references",
			payload: referencedDomains,
			inject:  retypedDomains("_:" + activityPubMastodonAttributionDomainsIRI),
			reads:   readsAttributionDomains,
		},
		{
			name:    "fetched context nulls attributionDomains",
			payload: linkedDataPersonUpdatePayload,
			inject:  fetchedContext(activityPubMastodonAttributionDomainsIRI, []any{"attacker.example"}),
			reads:   readsAttributionDomains,
		},
		{
			name:    "fetched context nulls movedTo",
			payload: linkedDataPersonUpdatePayload,
			inject:  fetchedContext("https://www.w3.org/ns/activitystreams#movedTo", map[string]any{"@id": mallory}),
			reads: func(payload activityPayload) bool {
				return activityPubObjectID(payload.Object.MovedTo) == mallory
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			forgetAttackerContext()
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			test.inject(document)
			body := marshalLinkedDataTestDocument(t, document)

			compactedBody, compacted := activityPubCompactCollectionBody(body)
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("injection changed the signed RDF: %v", err)
			}
			payload, err := parseActivityPayload(compactedBody)
			if err != nil {
				t.Fatal(err)
			}
			if !payload.Signature.Present || !test.reads(payload) {
				t.Fatalf("compacted payload does not carry the injected content: %s", compactedBody)
			}
			if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err == nil || !strings.Contains(err.Error(), "does not cover the document") {
				t.Fatalf("coverage error = %v, want rejection of the embedded @context", err)
			}
			// Rejection must happen before signature creator lookup touches the
			// database; reaching it here would panic on the zero-value handle.
			server := &Server{db: &gorm.DB{}}
			err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, linkedDataForgeryRelay(), nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature does not cover the document") {
				t.Fatalf("relayed activity error = %v, want rejection of the embedded @context", err)
			}
		})
	}

	t.Run("embedded context of the original document", func(t *testing.T) {
		document, publicKey := signedLinkedDataFixture(t, func(string) map[string]any {
			payload := linkedDataNoteCreatePayload("")
			object := payload["object"].(map[string]any)
			object["@context"] = map[string]any{"sensitive": "as:sensitive"}
			object["sensitive"] = true
			return payload
		})
		compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
		if !compacted {
			t.Fatal("document with a literal signature object was not compacted")
		}
		if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
			t.Fatalf("compacted signature: %v", err)
		}
		if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err != nil {
			t.Fatalf("signed document was rejected: %v", err)
		}
	})
}

// linkedDataKeywordNotePayload is a signed post carrying a content warning and
// a mention, which a relay must not be able to hide.
func linkedDataKeywordNotePayload(string) map[string]any {
	payload := linkedDataNoteCreatePayload("")
	payload["@context"] = append(payload["@context"].([]any), map[string]any{"sensitive": "as:sensitive"})
	object := payload["object"].(map[string]any)
	object["sensitive"] = true
	object["summary"] = "spoilers"
	object["tag"] = []any{map[string]any{"type": "Mention", "href": "https://paon.example/users/bob", "name": "@bob@paon.example"}}
	return payload
}

// linkedDataKeywordPersonPayload is a signed Update(Person) carrying a shared
// inbox and an account migration, which a relay must not be able to hide.
func linkedDataKeywordPersonPayload(publicKeyPEM string) map[string]any {
	payload := linkedDataPersonUpdatePayload(publicKeyPEM)
	payload["@context"] = append(payload["@context"].([]any), map[string]any{
		"movedTo":     map[string]any{"@id": "as:movedTo", "@type": "@id"},
		"alsoKnownAs": map[string]any{"@id": "as:alsoKnownAs", "@type": "@id"},
	})
	object := payload["object"].(map[string]any)
	object["endpoints"] = map[string]any{"sharedInbox": "https://origin.example/inbox"}
	object["movedTo"] = "https://new.example/users/alice"
	object["alsoKnownAs"] = []any{"https://new.example/users/alice"}
	return payload
}

// json-gold compacts every blank node property "_:<keyword>" against the
// ActivityStreams "@vocab": "_:" to the bare keyword, not only "@context".
// Re-expanding the compacted document for canonicalization gives the keyword
// its meaning: "@nest" and "@set" move the signed values under them back into
// the enclosing node, so the victim's signature still verifies while Paon no
// longer sees those values under the keys it reads, and "@id" gives a node
// Paon reads a blank node identifier. Compacting with Paon's context never
// writes these keywords, so such a document is never authenticated.
func TestLinkedDataSignatureRejectsCompactedBlankNodeKeywords(t *testing.T) {
	wrap := func(keyword string, node func(document map[string]any) map[string]any, keys ...string) func(document map[string]any) {
		return func(document map[string]any) {
			target := node(document)
			wrapped := map[string]any{}
			for _, key := range keys {
				wrapped[key] = target[key]
				delete(target, key)
			}
			target["_:"+keyword] = wrapped
		}
	}
	root := func(document map[string]any) map[string]any { return document }
	object := func(document map[string]any) map[string]any { return document["object"].(map[string]any) }
	objectKey := func(key string) func(document map[string]any) map[string]any {
		return func(document map[string]any) map[string]any { return object(document)[key].(map[string]any) }
	}
	for _, test := range []struct {
		name    string
		keyword string
		payload func(publicKeyPEM string) map[string]any
		forge   func(document map[string]any)
		forged  func(payload activityPayload) bool
	}{
		{
			name:    "@nest hides sensitive and the content warning",
			keyword: "embedded @nest",
			payload: linkedDataKeywordNotePayload,
			forge:   wrap("@nest", object, "sensitive", "summary"),
			forged:  func(p activityPayload) bool { return !p.Object.Sensitive && p.Object.Summary == "" },
		},
		{
			name:    "@nest hides the actor key",
			keyword: "embedded @nest",
			payload: linkedDataKeywordPersonPayload,
			forge:   wrap("@nest", object, "publicKey"),
			forged:  func(p activityPayload) bool { return p.Object.PublicKey == "" },
		},
		{
			name:    "@nest hides the account migration",
			keyword: "embedded @nest",
			payload: linkedDataKeywordPersonPayload,
			forge:   wrap("@nest", object, "movedTo", "alsoKnownAs"),
			forged:  func(p activityPayload) bool { return p.Object.MovedTo == nil && p.Object.AlsoKnownAs == nil },
		},
		{
			name:    "@set hides the shared inbox",
			keyword: "embedded @set",
			payload: linkedDataKeywordPersonPayload,
			forge:   wrap("@set", objectKey("endpoints"), "sharedInbox"),
			forged:  func(p activityPayload) bool { return p.Object.SharedInbox == "" },
		},
		{
			name:    "@set hides the actor key node",
			keyword: "embedded @set",
			payload: linkedDataKeywordPersonPayload,
			forge:   wrap("@set", objectKey("publicKey"), "id", "owner", "publicKeyPem"),
			forged:  func(p activityPayload) bool { return p.Object.PublicKey == "" },
		},
		{
			name:    "@id adds a blank node identifier to a mention",
			keyword: "embedded @id",
			payload: linkedDataKeywordNotePayload,
			forge: func(document map[string]any) {
				object(document)["tag"].([]any)[0].(map[string]any)["_:@id"] = "_:b0"
			},
			forged: func(p activityPayload) bool { return len(p.Object.Tags) == 1 && p.Object.Tags[0].ID == "_:b0" },
		},
		{
			name:    "top-level @nest hides the object",
			keyword: "top-level @nest",
			payload: linkedDataKeywordNotePayload,
			forge:   wrap("@nest", root, "object"),
			forged:  func(p activityPayload) bool { return p.Object.ID == "" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			test.forge(document)
			body := marshalLinkedDataTestDocument(t, document)
			compactedBody, compacted := activityPubCompactCollectionBody(body)
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("forgery changed the signed RDF: %v", err)
			}
			payload, err := parseActivityPayload(compactedBody)
			if err != nil {
				t.Fatal(err)
			}
			if !payload.Signature.Present || payload.Actor != linkedDataForgeryVictimURI || !test.forged(payload) {
				t.Fatalf("compacted payload does not show the forgery: %s", compactedBody)
			}
			err = activityPubLinkedDataSignatureCoversDocument(compactedBody)
			if err == nil || !strings.Contains(err.Error(), "linked-data signature does not cover the document: "+test.keyword) {
				t.Fatalf("coverage error = %v, want rejection of the %s", err, test.keyword)
			}
			// Rejection must happen before signature creator lookup touches the
			// database; reaching it here would panic on the zero-value handle.
			server := &Server{db: &gorm.DB{}}
			err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, linkedDataForgeryRelay(), nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature does not cover the document") {
				t.Fatalf("relayed activity error = %v, want rejection before creator lookup", err)
			}
		})
	}
}

// Compacting with Paon's context keeps the keywords of value objects, list
// objects and indexes below the top level, and removes a sender's own @nest
// aliases and embedded contexts. Such signed documents are still
// authenticated, and Paon reads the signed values the forgeries above hide.
func TestLinkedDataSignatureAcceptsCompactedValueKeywords(t *testing.T) {
	const ext = "https://ext.example/ns#"
	withExtensions := func(payload func(publicKeyPEM string) map[string]any) func(publicKeyPEM string) map[string]any {
		return func(publicKeyPEM string) map[string]any {
			document := payload(publicKeyPEM)
			object := document["object"].(map[string]any)
			object[ext+"rating"] = map[string]any{"@value": "5", "@type": "http://www.w3.org/2001/XMLSchema#integer"}
			object[ext+"note"] = map[string]any{"@value": "メモ", "@language": "ja"}
			object[ext+"steps"] = map[string]any{"@list": []any{"one", "two"}}
			object[ext+"indexed"] = map[string]any{"@value": "first", "@index": "a"}
			return document
		}
	}
	senderNest := func(string) map[string]any {
		return map[string]any{
			"@context": []any{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext, map[string]any{
				"@version":  1.1,
				"props":     "@nest",
				"sensitive": map[string]any{"@id": "as:sensitive", "@nest": "props"},
			}},
			"id":    linkedDataForgeryVictimURI + "/statuses/9/activity",
			"type":  "Create",
			"actor": linkedDataForgeryVictimURI,
			"to":    []any{activityPubPublicIRI},
			"object": map[string]any{
				"id":           linkedDataForgeryVictimURI + "/statuses/9",
				"type":         "Note",
				"attributedTo": linkedDataForgeryVictimURI,
				"content":      "signed",
				"summary":      "spoilers",
				"to":           []any{activityPubPublicIRI},
				"props":        map[string]any{"sensitive": true},
				"tag":          []any{map[string]any{"type": "Mention", "href": "https://paon.example/users/bob", "name": "@bob@paon.example"}},
			},
		}
	}
	readsNote := func(t *testing.T, payload activityPayload) {
		t.Helper()
		if !payload.Object.Sensitive || payload.Object.Summary != "spoilers" {
			t.Fatalf("sensitive = %v, summary = %q; want the signed content warning", payload.Object.Sensitive, payload.Object.Summary)
		}
		if len(payload.Object.Tags) != 1 || payload.Object.Tags[0].Href != "https://paon.example/users/bob" || payload.Object.Tags[0].ID != "" {
			t.Fatalf("tags = %#v, want the signed mention", payload.Object.Tags)
		}
	}
	for _, test := range []struct {
		name     string
		payload  func(publicKeyPEM string) map[string]any
		keywords []string
		reads    func(t *testing.T, payload activityPayload)
	}{
		{
			name:     "Create(Note) with value, list and index objects",
			payload:  withExtensions(linkedDataKeywordNotePayload),
			keywords: []string{"@value", "@language", "@list", "@index"},
			reads:    readsNote,
		},
		{
			name:     "Update(Person) with value, list and index objects",
			payload:  withExtensions(linkedDataKeywordPersonPayload),
			keywords: []string{"@value", "@language", "@list", "@index"},
			reads: func(t *testing.T, payload activityPayload) {
				t.Helper()
				if payload.Object.PublicKey == "" || payload.Object.SharedInbox != "https://origin.example/inbox" {
					t.Fatalf("publicKey = %q, sharedInbox = %q; want the signed values", payload.Object.PublicKey, payload.Object.SharedInbox)
				}
				if movedTo := activityPubObjectID(payload.Object.MovedTo); movedTo != "https://new.example/users/alice" {
					t.Fatalf("movedTo = %q, want the signed account migration", movedTo)
				}
				if aliases := activityLimitedValueOrIDList(payload.Object.AlsoKnownAs, 256); !slices.Equal(aliases, []string{"https://new.example/users/alice"}) {
					t.Fatalf("alsoKnownAs = %#v, want the signed aliases", aliases)
				}
			},
		},
		{
			name:    "Create(Note) with a sender @nest alias",
			payload: senderNest,
			reads:   readsNote,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			body := marshalLinkedDataTestDocument(t, document)
			compactedBody, compacted := activityPubCompactCollectionBody(body)
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			for _, keyword := range test.keywords {
				if !strings.Contains(string(compactedBody), `"`+keyword+`":`) {
					t.Fatalf("compacted document does not hold %s: %s", keyword, compactedBody)
				}
			}
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("compacted signature: %v", err)
			}
			if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err != nil {
				t.Fatalf("signed document was rejected: %v", err)
			}
			payload, err := parseActivityPayload(activityPubFinalizeCollectionBodyForForwarding(body, compactedBody))
			if err != nil {
				t.Fatal(err)
			}
			test.reads(t, payload)
		})
	}
}

// Mastodon 4.6 documents reference the WebFinger context, which Mastodon
// preloads; Paon preloads it too so relayed documents compact offline.
func TestLinkedDataSignatureCompactsMastodonWebFingerContext(t *testing.T) {
	// The offline loader only serves preloaded documents, so this fails if the
	// context would have to be fetched from purl.archive.org.
	offline := newActivityPubJSONLDDocumentLoader(true)
	for _, uri := range []string{"https://purl.archive.org/socialweb/webfinger", "http://purl.archive.org/socialweb/webfinger"} {
		if _, err := offline.LoadDocument(uri); err != nil {
			t.Fatalf("offline load of %s: %v", uri, err)
		}
	}
	document, publicKey := signedLinkedDataFixture(t, func(publicKeyPEM string) map[string]any {
		payload := linkedDataPersonUpdatePayload(publicKeyPEM)
		payload["@context"] = append(payload["@context"].([]any), "https://purl.archive.org/socialweb/webfinger")
		payload["object"].(map[string]any)["webfinger"] = "alice@origin.example"
		return payload
	})
	compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
	if !compacted {
		t.Fatal("document referencing the WebFinger context was not compacted")
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
		t.Fatalf("compacted signature: %v", err)
	}
	if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err != nil {
		t.Fatalf("signed document was rejected: %v", err)
	}
}
