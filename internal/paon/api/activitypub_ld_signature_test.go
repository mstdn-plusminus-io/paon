package api

import (
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
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

	// Like ProcessCollectionService in Mastodon 4.3.23, a document holding
	// @graph is processed as the original without its signature.
	processedBody, compacted := activityPubCompactCollectionBody(body)
	if compacted {
		t.Fatal("document with @graph was compacted")
	}
	if payload, err := parseActivityPayload(processedBody); err != nil || payload.Signature.Present {
		t.Fatalf("@graph fallback payload signature=%#v err=%v, want no signature", payload.Signature, err)
	}
	compactedDocument, err := activityPubCompactSignedActivityDocument(cloneLinkedDataTestDocument(t, flattened))
	if err != nil {
		t.Fatal(err)
	}
	compactedBody := marshalLinkedDataTestDocument(t, compactedDocument)
	if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
		t.Fatalf("flattened fixture no longer preserves the signed RDF: %v", err)
	}
	// Even when compacted with its signature, the node picked from @graph is
	// not authenticated by it.
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
			forged := test.forge(document)
			body := marshalLinkedDataTestDocument(t, forged)

			// Like ProcessCollectionService in Mastodon 4.3.23, a document using
			// these keywords is processed as the original without its signature.
			processedBody, compacted := activityPubCompactCollectionBody(body)
			if compacted {
				t.Fatalf("document with %s was compacted", test.keyword)
			}
			var processed map[string]any
			if err := json.Unmarshal(processedBody, &processed); err != nil {
				t.Fatal(err)
			}
			if _, ok := processed["signature"]; ok {
				t.Fatalf("linked-data signature survived the %s fallback", test.keyword)
			}
			// LinkedDataSignature#verify_actor! refuses them as well, so the
			// coverage check rejects them even once compacted.
			compactedDocument, err := activityPubCompactSignedActivityDocument(cloneLinkedDataTestDocument(t, forged))
			if err != nil {
				t.Fatal(err)
			}
			compactedBody := marshalLinkedDataTestDocument(t, compactedDocument)
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("forgery no longer preserves the signed RDF: %v", err)
			}
			err = activityPubLinkedDataSignatureCoversDocument(compactedBody)
			if err == nil || !strings.Contains(err.Error(), test.keyword) {
				t.Fatalf("coverage error = %v, want rejection of %s", err, test.keyword)
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

// Mastodon 4.3 publishes attributionDomains as bare domains under an @id term,
// so RDF conversion drops them and a relay can replace them without breaking
// the signature. Paon stores them as the account's attribution_domains, so
// such a document is not authenticated by its linked-data signature.
func TestLinkedDataSignatureRejectsUncoveredMastodonAttributionDomains(t *testing.T) {
	person := func(publicKeyPEM string) map[string]any {
		payload := linkedDataPersonUpdatePayload(publicKeyPEM)
		payload["@context"] = append(payload["@context"].([]any), map[string]any{
			"toot":               "http://joinmastodon.org/ns#",
			"attributionDomains": map[string]any{"@id": "toot:attributionDomains", "@type": "@id"},
		})
		payload["object"].(map[string]any)["attributionDomains"] = []any{"origin.example", "news.example"}
		return payload
	}
	forged, publicKey := signedLinkedDataFixture(t, person)
	forged["object"].(map[string]any)["attributionDomains"] = []any{"attacker.example"}
	compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, forged))
	if !compacted {
		t.Fatal("document with a literal signature object was not compacted")
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
		t.Fatalf("replaced attributionDomains changed the signed RDF: %v", err)
	}
	payload, err := parseActivityPayload(compactedBody)
	if err != nil {
		t.Fatal(err)
	}
	if got := activityLimitedValueOrIDList(payload.Object.AttributionDomains, 256); !slices.Equal(got, []string{"attacker.example"}) {
		t.Fatalf("compacted attributionDomains = %#v, want the replaced value Paon would store", got)
	}
	if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err == nil || !strings.Contains(err.Error(), "does not cover the document") {
		t.Fatalf("coverage error = %v, want rejection of the uncovered attributionDomains", err)
	}
}

// A property that expands to the blank node identifier "_:attributionDomains"
// (an undefined term under the ActivityStreams "@vocab": "_:", or that key
// itself) is dropped by RDF conversion, so the signature does not cover it.
// Mastodon v4.3.23 compacts with a context that defines attributionDomains
// (ContextHelper#full_context), so such a property stays "_:attributionDomains"
// and ProcessAccountService never reads it. Paon must not read it either, while
// signed literal and absolute IRI values are still read.
func TestLinkedDataSignatureIgnoresBlankNodeAttributionDomains(t *testing.T) {
	mastodonPerson := func(definition map[string]any, domains ...any) func(publicKeyPEM string) map[string]any {
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
	// Mastodon 4.4.18 and later publish literal domains in a set.
	literalDomains := mastodonPerson(map[string]any{"@id": "toot:attributionDomains", "@container": "@set"}, "origin.example")
	// Mastodon 4.3 publishes bare domains as node references, whose relative
	// identifiers RDF conversion drops (see
	// TestLinkedDataSignatureRejectsUncoveredMastodonAttributionDomains).
	referencedDomains := mastodonPerson(map[string]any{"@id": "toot:attributionDomains", "@type": "@id"}, "origin.example")
	// Absolute IRIs under the same term are signed node references.
	iriDomains := mastodonPerson(map[string]any{"@id": "toot:attributionDomains", "@type": "@id"}, "https://origin.example")
	undefinedTerm := func(object map[string]any) {
		object["attributionDomains"] = []any{"attacker.example"}
	}
	blankNodeProperty := func(object map[string]any) {
		object["_:attributionDomains"] = []any{"attacker.example"}
	}
	blankNodeReference := func(object map[string]any) {
		object["_:attributionDomains"] = []any{map[string]any{"@id": "https://attacker.example"}}
	}
	for _, test := range []struct {
		name     string
		payload  func(publicKeyPEM string) map[string]any
		inject   func(object map[string]any)
		rejected bool
		want     []string
	}{
		{name: "undefined term", payload: linkedDataPersonUpdatePayload, inject: undefinedTerm},
		{name: "blank node property", payload: linkedDataPersonUpdatePayload, inject: blankNodeProperty},
		{name: "blank node property with a node reference", payload: linkedDataPersonUpdatePayload, inject: blankNodeReference},
		{name: "signed literals", payload: literalDomains, want: []string{"origin.example"}},
		{name: "blank node property next to signed literals", payload: literalDomains, inject: blankNodeProperty, want: []string{"origin.example"}},
		{name: "signed absolute IRIs", payload: iriDomains, want: []string{"https://origin.example"}},
		{name: "blank node property next to signed absolute IRIs", payload: iriDomains, inject: blankNodeProperty, want: []string{"https://origin.example"}},
		{name: "blank node property next to signed references", payload: referencedDomains, inject: blankNodeProperty, rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			if test.inject != nil {
				test.inject(document["object"].(map[string]any))
			}
			compactedBody, compacted := activityPubCompactCollectionBody(marshalLinkedDataTestDocument(t, document))
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
			}
			// The injected property is outside the signed RDF.
			if err := verifyActivityPubLinkedDataSignatureWithError(compactedBody, publicKey); err != nil {
				t.Fatalf("injection changed the signed RDF: %v", err)
			}
			err := activityPubLinkedDataSignatureCoversDocument(compactedBody)
			if test.rejected {
				// Rejecting the document also keeps the injected value out.
				if err == nil || !strings.Contains(err.Error(), "does not cover the document") {
					t.Fatalf("coverage error = %v, want rejection of the uncovered references", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("signed Update(Person) was rejected: %v", err)
			}
			payload, err := parseActivityPayload(compactedBody)
			if err != nil {
				t.Fatal(err)
			}
			if domains := activityLimitedValueOrIDList(payload.Object.AttributionDomains, 256); !slices.Equal(domains, test.want) {
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

	const (
		mallory                  = "https://attacker.example/users/mallory"
		attributionDomainsIRI    = "http://joinmastodon.org/ns#attributionDomains"
		attributionDomainsTarget = "attacker.example"
	)
	// Paon stores attributionDomains as normalized domains, so a scheme does
	// not matter (previewCardAccountAllowsAttribution).
	readsAttributionDomains := func(payload activityPayload) bool {
		for _, domain := range activityLimitedValueOrIDList(payload.Object.AttributionDomains, 256) {
			if normalizeActivityAttributionDomain(domain) == attributionDomainsTarget {
				return true
			}
		}
		return false
	}
	// Retyped values become relative node references, which the RDF leaves
	// out; the content check after re-expansion would also catch those.
	retypedDomains := func(key string) func(document map[string]any) {
		return func(document map[string]any) {
			object := document["object"].(map[string]any)
			object["_:@context"] = map[string]any{key: map[string]any{"_:@type": "@id"}}
			object[key] = []any{attributionDomainsTarget}
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
			name:    "retyped attributionDomains IRI",
			payload: linkedDataPersonUpdatePayload,
			inject:  retypedDomains("_:" + attributionDomainsIRI),
			reads:   readsAttributionDomains,
		},
		{
			// attributionDomains is an @id term of Paon's context, so only a
			// node reference compacts to that term instead of to a CURIE.
			name:    "fetched context nulls attributionDomains",
			payload: linkedDataPersonUpdatePayload,
			inject:  fetchedContext(attributionDomainsIRI, []any{map[string]any{"@id": "https://" + attributionDomainsTarget}}),
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
			if err := activityPubLinkedDataSignatureCoversDocument(compactedBody); err == nil || !strings.Contains(err.Error(), "does not cover the document: embedded @context") {
				t.Fatalf("coverage error = %v, want rejection of the embedded @context", err)
			}
			// Rejection must happen before signature creator lookup touches the
			// database; reaching it here would panic on the zero-value handle.
			server := &Server{db: &gorm.DB{}}
			err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, linkedDataForgeryRelay(), nil, 0)
			if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature does not cover the document: embedded @context") {
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

// A relay forges the account migration of a victim's linked-data signed
// Update(Person): it adds a "_:@context" property referencing a context that
// nulls movedTo, the same context to the top-level @context, and a movedTo
// under its absolute IRI. Compacting the relayed document fetches and caches
// that context, turns the blank node property into an embedded "@context",
// and the forged value into the movedTo term Paon reads, while re-expanding
// the compacted document for canonicalization nulls it again. The victim's
// signature therefore still verifies, but the relayed document must not
// authenticate the victim.
func TestLinkedDataSignatureEmbeddedContextCannotForgeMovedTo(t *testing.T) {
	const (
		attackerContextURI = "https://attacker.example/moved-to-context"
		mallory            = "https://attacker.example/users/mallory"
	)
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
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != attackerContextURI {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/ld+json"}},
			Body:       io.NopCloser(strings.NewReader(`{"@context":{"movedTo":null}}`)),
			Request:    r,
		}, nil
	})}

	var victimPublicKeyPEM string
	document, _ := signedLinkedDataFixture(t, func(publicKeyPEM string) map[string]any {
		victimPublicKeyPEM = publicKeyPEM
		return linkedDataPersonUpdatePayload(publicKeyPEM)
	})
	victim := models.Account{
		ID:        21,
		Username:  "alice",
		Domain:    sql.NullString{String: "origin.example", Valid: true},
		URI:       linkedDataForgeryVictimURI,
		PublicKey: victimPublicKeyPEM,
	}
	database, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=paon dbname=paon",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	// The signature creator resolves to the known victim and its stored key.
	if err := database.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		if destination, ok := tx.Statement.Dest.(*models.Account); ok {
			*destination = victim
		}
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		cfg: config.Config{Scheme: "https", WebDomain: "paon.example", LocalDomain: "paon.example"},
		db:  database,
	}
	// authenticate follows a relayed activity through
	// processActivityPubInboxForDeliveredToWithContext: compaction, the
	// linked-data signature authentication, then the forwarding-safe document
	// Paon processes.
	authenticate := func(t *testing.T, body []byte) (*models.Account, activityPayload, error) {
		t.Helper()
		compactedBody, compacted := activityPubCompactCollectionBody(body)
		if !compacted {
			t.Fatal("document with a literal signature object was not compacted")
		}
		payload, err := parseActivityPayload(compactedBody)
		if err != nil {
			t.Fatal(err)
		}
		actor, err := server.activityPubLinkedDataSignatureActor(compactedBody, payload)
		if err != nil {
			return nil, activityPayload{}, err
		}
		payload, err = parseActivityPayload(activityPubFinalizeCollectionBodyForForwarding(body, compactedBody))
		if err != nil {
			t.Fatal(err)
		}
		return actor, payload, nil
	}

	actor, payload, err := authenticate(t, marshalLinkedDataTestDocument(t, document))
	if err != nil || actor == nil || actor.URI != linkedDataForgeryVictimURI {
		t.Fatalf("signed Update(Person) authenticated actor %#v, error %v; want the victim", actor, err)
	}
	if movedTo := activityPubObjectID(payload.Object.MovedTo); movedTo != "" {
		t.Fatalf("signed Update(Person) movedTo = %q, want none", movedTo)
	}

	document["@context"] = append(document["@context"].([]any), attackerContextURI)
	object := document["object"].(map[string]any)
	object["_:@context"] = attackerContextURI
	object["https://www.w3.org/ns/activitystreams#movedTo"] = map[string]any{"@id": mallory}
	actor, payload, err = authenticate(t, marshalLinkedDataTestDocument(t, document))
	if err == nil {
		t.Fatalf("forged Update(Person) authenticated %q with movedTo %q", actor.URI, activityPubObjectID(payload.Object.MovedTo))
	}
	if !strings.Contains(err.Error(), "linked-data signature does not cover the document: embedded @context") {
		t.Fatalf("forged Update(Person) error = %v, want rejection of the embedded @context", err)
	}
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

// linkedDataKeywordQuestionPayload is a signed poll open until 2030, which a
// relay must not be able to close.
func linkedDataKeywordQuestionPayload(string) map[string]any {
	payload := linkedDataNoteCreatePayload("")
	object := payload["object"].(map[string]any)
	object["type"] = "Question"
	object["endTime"] = "2030-01-01T00:00:00Z"
	object["oneOf"] = []any{
		map[string]any{"type": "Note", "name": "yes", "replies": map[string]any{"type": "Collection", "totalItems": 0}},
		map[string]any{"type": "Note", "name": "no", "replies": map[string]any{"type": "Collection", "totalItems": 0}},
	}
	return payload
}

// json-gold also compacts a blank node property "_:@language" to the bare
// "@language". Re-expanding a map that has "@language" but no "@value" turns
// it into null (step 11 of the expansion algorithm), so it leaves neither the
// canonicalized RDF nor the expanded document the coverage check walks, while
// Paon still reads the map. Placed in front of a signed value it hides that
// value from readers that take the first entry, and as a whole value it reads
// as true for properties such as suspended or closed. Compacting a document
// json-gold can canonicalize with Paon's context writes "@language" and
// "@direction" only inside value objects, so such a document is never
// authenticated.
func TestLinkedDataSignatureRejectsCompactedLanguageOnlyMaps(t *testing.T) {
	const (
		as   = "https://www.w3.org/ns/activitystreams#"
		toot = "http://joinmastodon.org/ns#"
	)
	language := func() map[string]any { return map[string]any{"_:@language": "en"} }
	object := func(document map[string]any) map[string]any { return document["object"].(map[string]any) }
	hide := func(keys ...string) func(document map[string]any) {
		return func(document map[string]any) {
			target := object(document)
			for _, key := range keys {
				target[key] = []any{language(), target[key]}
			}
		}
	}
	pollCheckedAt := time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		forge   func(document map[string]any)
		forged  func(payload activityPayload) bool
	}{
		{
			name:    "suspends the actor",
			payload: linkedDataKeywordPersonPayload,
			forge: func(document map[string]any) {
				object(document)[toot+"suspended"] = language()
			},
			forged: func(p activityPayload) bool { return p.Object.Suspended },
		},
		{
			name:    "hides the actor key",
			payload: linkedDataKeywordPersonPayload,
			forge:   hide("publicKey"),
			forged:  func(p activityPayload) bool { return p.Object.PublicKey == "" },
		},
		{
			name:    "hides sensitive and the content warning",
			payload: linkedDataKeywordNotePayload,
			forge:   hide("sensitive", "summary"),
			forged:  func(p activityPayload) bool { return !p.Object.Sensitive && p.Object.Summary == "" },
		},
		{
			name:    "closes an open poll",
			payload: linkedDataKeywordQuestionPayload,
			forge: func(document map[string]any) {
				object(document)[as+"closed"] = language()
			},
			forged: func(p activityPayload) bool {
				return p.Object.Type == "Question" && activityPollExpiresAt(p.Object, pollCheckedAt).Time.Equal(pollCheckedAt)
			},
		},
		{
			name:    "hides the shared inbox with a dropped keyword-like key",
			payload: linkedDataKeywordPersonPayload,
			forge: func(document map[string]any) {
				endpoints := object(document)["endpoints"].(map[string]any)
				endpoints["sharedInbox"] = []any{map[string]any{"_:@language": "en", "_:@unknown": "x"}, endpoints["sharedInbox"]}
			},
			forged: func(p activityPayload) bool { return p.Object.SharedInbox == "" },
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
			if err == nil || !strings.Contains(err.Error(), "linked-data signature does not cover the document: embedded @language") {
				t.Fatalf("coverage error = %v, want rejection of the embedded @language", err)
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
	// A map with "@direction" but no "@value" is not nulled, so it changes the
	// signed RDF, but compaction never writes one either.
	t.Run("@direction outside a value object", func(t *testing.T) {
		body := marshalLinkedDataTestDocument(t, map[string]any{
			"@context": activityPubFullJSONLDContext(),
			"id":       linkedDataForgeryVictimURI + "#updates/1",
			"type":     "Update",
			"actor":    linkedDataForgeryVictimURI,
			"object": map[string]any{
				"id":        linkedDataForgeryVictimURI,
				"type":      "Person",
				"suspended": map[string]any{"@direction": "rtl"},
			},
		})
		err := activityPubLinkedDataSignatureCoversDocument(body)
		if err == nil || !strings.Contains(err.Error(), "linked-data signature does not cover the document: embedded @direction") {
			t.Fatalf("coverage error = %v, want rejection of the embedded @direction", err)
		}
	})
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
			object[ext+"label"] = map[string]any{"@value": "שלום", "@direction": "rtl"}
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
			keywords: []string{"@value", "@language", "@direction", "@list", "@index"},
			reads:    readsNote,
		},
		{
			name:     "Update(Person) with value, list and index objects",
			payload:  withExtensions(linkedDataKeywordPersonPayload),
			keywords: []string{"@value", "@language", "@direction", "@list", "@index"},
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

// linkedDataPlacementContext is the context of a Mastodon full_context sender
// that also defines Paon's extension terms, so every property of the
// placement fixtures below is signed.
func linkedDataPlacementContext() []any {
	return []any{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext, activityPubFullJSONLDContextExtensions()}
}

// linkedDataPlacementPersonPayload is the victim's signed Update(Person) of a
// locked, suspended account that moved, which a relay must not be able to
// unlock, unsuspend, unmove or strip of its key and followers collection.
func linkedDataPlacementPersonPayload(publicKeyPEM string) map[string]any {
	return map[string]any{
		"@context": linkedDataPlacementContext(),
		"id":       linkedDataForgeryVictimURI + "#updates/1",
		"type":     "Update",
		"actor":    linkedDataForgeryVictimURI,
		"to":       []any{activityPubPublicIRI},
		"object": map[string]any{
			"id":                        linkedDataForgeryVictimURI,
			"type":                      "Person",
			"preferredUsername":         "alice",
			"inbox":                     linkedDataForgeryVictimURI + "/inbox",
			"followers":                 linkedDataForgeryVictimURI + "/followers",
			"manuallyApprovesFollowers": true,
			"suspended":                 true,
			"movedTo":                   "https://new.example/users/alice",
			"alsoKnownAs":               []any{"https://new.example/users/alice"},
			"icon":                      map[string]any{"type": "Image", "mediaType": "image/png", "url": "https://files.origin.example/avatar.png"},
			"publicKey": map[string]any{
				"id":           linkedDataForgeryVictimURI + "#main-key",
				"owner":        linkedDataForgeryVictimURI,
				"publicKeyPem": publicKeyPEM,
			},
		},
	}
}

// linkedDataPlacementNotePayload is the victim's signed public reply with a
// content warning, a mention, a hashtag and a described attachment.
func linkedDataPlacementNotePayload(string) map[string]any {
	followers := linkedDataForgeryVictimURI + "/followers"
	return map[string]any{
		"@context": linkedDataPlacementContext(),
		"id":       linkedDataForgeryVictimURI + "/statuses/3/activity",
		"type":     "Create",
		"actor":    linkedDataForgeryVictimURI,
		"to":       []any{activityPubPublicIRI},
		"cc":       []any{followers},
		"object": map[string]any{
			"id":           linkedDataForgeryVictimURI + "/statuses/3",
			"type":         "Note",
			"attributedTo": linkedDataForgeryVictimURI,
			"summary":      "CW",
			"sensitive":    true,
			"content":      "<p>signed</p>",
			"inReplyTo":    "https://other.example/statuses/1",
			"to":           []any{activityPubPublicIRI},
			"cc":           []any{followers},
			"tag": []any{
				map[string]any{"type": "Mention", "href": "https://paon.example/users/bob", "name": "@bob@paon.example"},
				map[string]any{"type": "Hashtag", "href": "https://origin.example/tags/paon", "name": "#paon"},
			},
			"attachment": []any{map[string]any{"type": "Document", "mediaType": "image/png", "url": "https://files.origin.example/media/1.png", "name": "alt"}},
		},
	}
}

// linkedDataPlacementQuestionPayload is the victim's signed poll, open until
// 2030, with two votes for "yes" and one for "no".
func linkedDataPlacementQuestionPayload(string) map[string]any {
	return map[string]any{
		"@context": linkedDataPlacementContext(),
		"id":       linkedDataForgeryVictimURI + "/statuses/5/activity",
		"type":     "Create",
		"actor":    linkedDataForgeryVictimURI,
		"to":       []any{activityPubPublicIRI},
		"object": map[string]any{
			"id":           linkedDataForgeryVictimURI + "/statuses/5",
			"type":         "Question",
			"attributedTo": linkedDataForgeryVictimURI,
			"content":      "<p>poll</p>",
			"to":           []any{activityPubPublicIRI},
			"endTime":      "2030-01-01T00:00:00Z",
			"votersCount":  3,
			"oneOf": []any{
				map[string]any{"type": "Note", "name": "yes", "replies": map[string]any{"type": "Collection", "totalItems": 2}},
				map[string]any{"type": "Note", "name": "no", "replies": map[string]any{"type": "Collection", "totalItems": 1}},
			},
		},
	}
}

// assertLinkedDataForgeryRejected checks that forging the signed document kept
// its signed RDF, that Paon reads the forgery from the compacted document
// (forged may be nil when it does not change what Paon reads), and that the
// coverage check rejects the document with want before a relayed copy reaches
// the signature creator lookup.
func assertLinkedDataForgeryRejected(t *testing.T, document map[string]any, publicKey *rsa.PublicKey, forged func(payload activityPayload) bool, want string) {
	t.Helper()
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
	if !payload.Signature.Present || payload.Actor != linkedDataForgeryVictimURI || (forged != nil && !forged(payload)) {
		t.Fatalf("compacted payload does not show the forgery: %s", compactedBody)
	}
	err = activityPubLinkedDataSignatureCoversDocument(compactedBody)
	if err == nil || !strings.Contains(err.Error(), "linked-data signature does not cover the document: "+want) {
		t.Fatalf("coverage error = %v, want %q", err, want)
	}
	// Rejection must happen before signature creator lookup touches the
	// database; reaching it here would panic on the zero-value handle.
	server := &Server{db: &gorm.DB{}}
	err = server.processActivityPubInboxForDeliveredToWithContext(t.Context(), body, linkedDataForgeryRelay(), nil, 0)
	if !errors.Is(err, errActivityPubEventNotApplied) || !strings.Contains(err.Error(), "linked-data signature does not cover the document") {
		t.Fatalf("relayed activity error = %v, want rejection before creator lookup", err)
	}
}

// linkedDataPlacementHasMention reports whether Paon reads the signed mention
// of bob from the payload.
func linkedDataPlacementHasMention(payload activityPayload) bool {
	for _, tag := range payload.Object.Tags {
		if tag.Type == "Mention" && tag.Href == "https://paon.example/users/bob" {
			return true
		}
	}
	return false
}

// RDF conversion merges node objects with the same @id and keeps the
// statements of a node object below a property it drops (an undefined term
// expands to a blank node property). A relay can therefore move or split the
// victim's signed node descriptions, or put a reference in front of one,
// without changing the signed RDF, while Paon reads the compacted JSON by
// position and loses the moved values. Such documents are never
// authenticated.
func TestLinkedDataSignatureRejectsRelocatedNodeDescriptions(t *testing.T) {
	victim := linkedDataForgeryVictimURI
	note := victim + "/statuses/3"
	activity := note + "/activity"
	question := victim + "/statuses/5"
	key := victim + "#main-key"
	object := func(document map[string]any) map[string]any { return document["object"].(map[string]any) }
	park := func(document map[string]any, node any) {
		parked, _ := document["zzpark"].([]any)
		document["zzpark"] = append(parked, node)
	}
	pollCheckedAt := time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC)
	poll := func(payload activityPayload) models.Poll {
		parsed, _ := activityPollFromObject(payload.Object, 1, 1, pollCheckedAt)
		return parsed
	}
	noteWithoutObjectAudience := func(publicKeyPEM string) map[string]any {
		document := linkedDataPlacementNotePayload(publicKeyPEM)
		delete(object(document), "to")
		delete(object(document), "cc")
		return document
	}
	differentStatements := func(id string) string {
		return `node "` + id + `" is described more than once with different statements`
	}
	blankDescribedTwice := func(id string) string {
		return `blank node "` + id + `" is described more than once`
	}
	describedBelowDropped := func(id string) string {
		return `node "` + id + `" is described below a property the signature does not cover`
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		forge   func(document map[string]any)
		forged  func(payload activityPayload) bool
		want    string
	}{
		{
			name:    "suspension parked below an undefined term",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				park(document, map[string]any{"id": victim, "suspended": object(document)["suspended"]})
				delete(object(document), "suspended")
			},
			forged: func(p activityPayload) bool { return !p.Object.Suspended },
			want:   differentStatements(victim),
		},
		{
			name:    "locked flag moved into the actor",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				document["actor"] = map[string]any{"id": victim, "manuallyApprovesFollowers": object(document)["manuallyApprovesFollowers"]}
				delete(object(document), "manuallyApprovesFollowers")
			},
			forged: func(p activityPayload) bool { return !p.Object.Locked },
			want:   differentStatements(victim),
		},
		{
			name:    "followers collection moved into the actor",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				document["actor"] = map[string]any{"id": victim, "followers": object(document)["followers"]}
				delete(object(document), "followers")
			},
			forged: func(p activityPayload) bool { return p.Object.Followers == "" },
			want:   differentStatements(victim),
		},
		{
			name:    "account migration parked below an undefined term",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				person := object(document)
				park(document, map[string]any{"id": victim, "movedTo": person["movedTo"], "alsoKnownAs": person["alsoKnownAs"]})
				delete(person, "movedTo")
				delete(person, "alsoKnownAs")
			},
			forged: func(p activityPayload) bool { return p.Object.MovedTo == nil && p.Object.AlsoKnownAs == nil },
			want:   differentStatements(victim),
		},
		{
			name:    "key split off the actor into a second object",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				person := object(document)
				split := map[string]any{"id": victim, "publicKey": person["publicKey"]}
				delete(person, "publicKey")
				document["object"] = []any{person, split}
			},
			forged: func(p activityPayload) bool { return p.Object.PublicKey == "" },
			want:   differentStatements(victim),
		},
		{
			name:    "audience of the activity and the object parked below an undefined term",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				status := object(document)
				park(document, map[string]any{"id": activity, "to": document["to"], "cc": document["cc"]})
				park(document, map[string]any{"id": note, "to": status["to"], "cc": status["cc"]})
				for _, node := range []map[string]any{document, status} {
					delete(node, "to")
					delete(node, "cc")
				}
			},
			forged: func(p activityPayload) bool {
				return len(p.To) == 0 && len(p.CC) == 0 && len(p.Object.To) == 0 && len(p.Object.CC) == 0
			},
			want: differentStatements(activity),
		},
		{
			name:    "audience of an activity whose object has none parked below an undefined term",
			payload: noteWithoutObjectAudience,
			forge: func(document map[string]any) {
				park(document, map[string]any{"id": activity, "to": document["to"], "cc": document["cc"]})
				delete(document, "to")
				delete(document, "cc")
			},
			forged: func(p activityPayload) bool { return len(p.To) == 0 && len(p.CC) == 0 && !p.Object.ToSet },
			want:   differentStatements(activity),
		},
		{
			name:    "content warning and tags parked below nested undefined terms",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				status := object(document)
				status["zzinner"] = map[string]any{"zzdeeper": []any{map[string]any{"id": note, "summary": status["summary"], "sensitive": status["sensitive"], "tag": status["tag"]}}}
				delete(status, "summary")
				delete(status, "sensitive")
				delete(status, "tag")
			},
			forged: func(p activityPayload) bool {
				return !p.Object.Sensitive && p.Object.Summary == "" && len(p.Object.Tags) == 0
			},
			want: differentStatements(note),
		},
		{
			name:    "reply target split off the object",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				status := object(document)
				split := map[string]any{"id": note, "inReplyTo": status["inReplyTo"]}
				delete(status, "inReplyTo")
				document["object"] = []any{status, split}
			},
			forged: func(p activityPayload) bool { return p.Object.InReplyTo == "" },
			want:   differentStatements(note),
		},
		{
			name:    "mention split over a labelled blank node",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				tags := object(document)["tag"].([]any)
				mention := tags[0].(map[string]any)
				tags[0] = map[string]any{"id": "_:m", "type": mention["type"], "name": mention["name"]}
				object(document)["tag"] = append(tags, map[string]any{"id": "_:m", "href": mention["href"]})
			},
			forged: func(p activityPayload) bool { return !linkedDataPlacementHasMention(p) },
			want:   blankDescribedTwice("_:m"),
		},
		{
			name:    "mention moved into a list below an undefined term",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				tags := object(document)["tag"].([]any)
				mention := tags[0].(map[string]any)
				mention["id"] = "_:m"
				tags[0] = "_:m"
				document["zzpark"] = map[string]any{"@list": []any{mention}}
			},
			forged: func(p activityPayload) bool { return !linkedDataPlacementHasMention(p) },
			want:   describedBelowDropped("_:m"),
		},
		{
			name:    "attachment description split over a labelled blank node",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				attachment := object(document)["attachment"].([]any)[0].(map[string]any)
				attachment["id"] = "_:a"
				name := attachment["name"]
				delete(attachment, "name")
				object(document)["attachment"] = []any{attachment, map[string]any{"id": "_:a", "name": name}}
			},
			forged: func(p activityPayload) bool {
				return len(p.Object.Attachments) == 1 && p.Object.Attachments[0].Name == ""
			},
			want: blankDescribedTwice("_:a"),
		},
		{
			name:    "poll option split so that the tallies shift",
			payload: linkedDataPlacementQuestionPayload,
			forge: func(document map[string]any) {
				options := object(document)["oneOf"].([]any)
				yes, no := options[0].(map[string]any), options[1].(map[string]any)
				object(document)["oneOf"] = []any{
					map[string]any{"id": "_:no", "replies": no["replies"]},
					map[string]any{"id": "_:yes", "type": "Note", "name": "yes", "replies": yes["replies"]},
					map[string]any{"id": "_:no", "type": "Note", "name": "no"},
				}
			},
			forged: func(p activityPayload) bool { return !slices.Equal(poll(p).CachedTallies, models.Int64Array{2, 1}) },
			want:   blankDescribedTwice("_:no"),
		},
		{
			name:    "poll option repeated with the same label",
			payload: linkedDataPlacementQuestionPayload,
			forge: func(document map[string]any) {
				options := object(document)["oneOf"].([]any)
				yes := func() map[string]any {
					return map[string]any{"id": "_:yes", "type": "Note", "name": "yes", "replies": map[string]any{"id": "_:yes-replies", "type": "Collection", "totalItems": 2}}
				}
				object(document)["oneOf"] = []any{yes(), yes(), options[1]}
			},
			forged: func(p activityPayload) bool { return !slices.Equal(poll(p).Options, models.StringArray{"yes", "no"}) },
			want:   blankDescribedTwice("_:yes"),
		},
		{
			name:    "poll option replies split over a labelled blank node",
			payload: linkedDataPlacementQuestionPayload,
			forge: func(document map[string]any) {
				yes := object(document)["oneOf"].([]any)[0].(map[string]any)
				replies := yes["replies"].(map[string]any)
				yes["replies"] = []any{map[string]any{"id": "_:r", "type": replies["type"]}, map[string]any{"id": "_:r", "totalItems": replies["totalItems"]}}
			},
			forged: func(p activityPayload) bool { return !slices.Equal(poll(p).CachedTallies, models.Int64Array{2, 1}) },
			want:   blankDescribedTwice("_:r"),
		},
		{
			name:    "poll end time and voters parked below an undefined term",
			payload: linkedDataPlacementQuestionPayload,
			forge: func(document map[string]any) {
				status := object(document)
				park(document, map[string]any{"id": question, "endTime": status["endTime"], "votersCount": status["votersCount"]})
				delete(status, "endTime")
				delete(status, "votersCount")
			},
			forged: func(p activityPayload) bool { return !poll(p).ExpiresAt.Valid && !poll(p).VotersCount.Valid },
			want:   differentStatements(question),
		},
		{
			name:    "key reference in front of its description",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				person := object(document)
				person["publicKey"] = []any{key, person["publicKey"]}
			},
			forged: func(p activityPayload) bool { return p.Object.PublicKey == "" },
			want:   `node "` + key + `" is both referenced and described in one property`,
		},
		{
			name:    "key reference with its description parked below an undefined term",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				person := object(document)
				park(document, person["publicKey"])
				person["publicKey"] = key
			},
			forged: func(p activityPayload) bool { return p.Object.PublicKey == "" },
			want:   describedBelowDropped(key),
		},
		{
			name:    "key reference with its description below a blank node of an undefined term",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				person := object(document)
				document["zzpark"] = map[string]any{"zzinner": person["publicKey"]}
				person["publicKey"] = key
			},
			forged: func(p activityPayload) bool { return p.Object.PublicKey == "" },
			want:   describedBelowDropped(key),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			test.forge(document)
			assertLinkedDataForgeryRejected(t, document, publicKey, test.forged, test.want)
		})
	}
}

// Signed documents that describe a node more than once with the same
// statements, reference a node they describe elsewhere, or carry empty arrays
// keep their linked-data authority, and Paon reads their signed values.
func TestLinkedDataSignatureAcceptsRepeatedAndReferencedNodes(t *testing.T) {
	victim := linkedDataForgeryVictimURI
	bob := "https://paon.example/users/bob"
	misskeyContext := []any{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext, map[string]any{
		"Key": "sec:Key", "manuallyApprovesFollowers": "as:manuallyApprovesFollowers", "sensitive": "as:sensitive", "Hashtag": "as:Hashtag", "quoteUrl": "as:quoteUrl",
		"toot": "http://joinmastodon.org/ns#", "Emoji": "toot:Emoji", "featured": "toot:featured", "discoverable": "toot:discoverable", "suspended": "toot:suspended",
		"schema": "http://schema.org#", "PropertyValue": "schema:PropertyValue", "value": "schema:value", "misskey": "https://misskey-hub.net/ns#",
		"_misskey_content": "misskey:_misskey_content", "_misskey_quote": "misskey:_misskey_quote", "_misskey_reaction": "misskey:_misskey_reaction",
		"_misskey_summary": "misskey:_misskey_summary", "isCat": "misskey:isCat", "vcard": "http://www.w3.org/2006/vcard/ns#",
	}}
	actorObject := func() map[string]any {
		return map[string]any{
			"type": "Person", "id": victim, "preferredUsername": "alice", "name": "Alice", "inbox": victim + "/inbox",
			"url": "https://origin.example/@alice", "icon": map[string]any{"type": "Image", "url": "https://files.origin.example/a.png"},
		}
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		reads   func(payload activityPayload) bool
	}{
		{
			name:    "Update(Person) with every signed actor property",
			payload: linkedDataPlacementPersonPayload,
			reads: func(p activityPayload) bool {
				return p.Object.Suspended && p.Object.Locked && p.Object.PublicKey != "" && p.Object.Followers == victim+"/followers" &&
					activityPubObjectID(p.Object.MovedTo) == "https://new.example/users/alice"
			},
		},
		{
			name:    "Create(Note) with a content warning, tags and an attachment",
			payload: linkedDataPlacementNotePayload,
			reads: func(p activityPayload) bool {
				return p.Object.Sensitive && p.Object.Summary == "CW" && linkedDataPlacementHasMention(p) &&
					len(p.Object.Attachments) == 1 && p.Object.Attachments[0].Name == "alt" && p.Object.InReplyTo == "https://other.example/statuses/1"
			},
		},
		{
			name:    "Create(Question) with tallies",
			payload: linkedDataPlacementQuestionPayload,
			reads: func(p activityPayload) bool {
				poll, ok := activityPollFromObject(p.Object, 1, 1, time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC))
				return ok && slices.Equal(poll.CachedTallies, models.Int64Array{2, 1}) && poll.ExpiresAt.Valid
			},
		},
		{
			name: "embedded actor repeated as attributedTo",
			payload: func(string) map[string]any {
				return map[string]any{
					"@context": linkedDataPlacementContext(), "id": victim + "/item/1/activity", "type": "Create", "actor": actorObject(),
					"to": []any{activityPubPublicIRI}, "cc": []any{victim + "/followers"},
					"object": map[string]any{"id": victim + "/item/1", "type": "Note", "attributedTo": actorObject(), "content": "x", "to": []any{activityPubPublicIRI}, "cc": []any{victim + "/followers"}},
				}
			},
			reads: func(p activityPayload) bool { return p.Object.AttributedTo == victim },
		},
		{
			name: "identical emoji tags",
			payload: func(string) map[string]any {
				emoji := func() map[string]any {
					return map[string]any{"id": "https://origin.example/emojis/1", "type": "Emoji", "name": ":paon:", "updated": "2020-01-01T00:00:00Z"}
				}
				return map[string]any{
					"@context": linkedDataPlacementContext(), "id": victim + "/statuses/21/activity", "type": "Create", "actor": victim, "to": []any{activityPubPublicIRI},
					"object": map[string]any{"id": victim + "/statuses/21", "type": "Note", "attributedTo": victim, "content": ":paon: :paon:", "to": []any{activityPubPublicIRI}, "tag": []any{emoji(), emoji()}},
				}
			},
			reads: func(p activityPayload) bool { return len(p.Object.Tags) == 2 && p.Object.Tags[1].Name == ":paon:" },
		},
		{
			name: "Mastodon direct message with an empty cc",
			payload: func(string) map[string]any {
				return map[string]any{
					"@context": linkedDataPlacementContext(), "id": victim + "/statuses/20/activity", "type": "Create", "actor": victim,
					"to": []any{bob}, "cc": []any{},
					"object": map[string]any{
						"id": victim + "/statuses/20", "type": "Note", "attributedTo": victim, "content": "dm", "to": []any{bob}, "cc": []any{},
						"tag": []any{map[string]any{"type": "Mention", "href": bob, "name": "@bob@paon.example"}},
					},
				}
			},
			reads: func(p activityPayload) bool {
				return slices.Equal(p.Object.To, []string{bob}) && len(p.Object.CC) == 0 && linkedDataPlacementHasMention(p)
			},
		},
		{
			name: "Lemmy page",
			payload: func(string) map[string]any {
				return map[string]any{
					"@context": []any{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext, map[string]any{
						"lemmy": "https://join-lemmy.org/ns#", "litepub": "http://litepub.social/ns#", "pt": "https://joinpeertube.org/ns#", "sc": "http://schema.org/",
						"ChatMessage": "litepub:ChatMessage", "commentsEnabled": "pt:commentsEnabled", "sensitive": "as:sensitive", "matrixUserId": "lemmy:matrixUserId",
						"postingRestrictedToMods": "lemmy:postingRestrictedToMods", "removeData": "lemmy:removeData", "stickied": "lemmy:stickied",
						"moderators": map[string]any{"@type": "@id", "@id": "lemmy:moderators"}, "expires": "as:endTime", "distinguished": "lemmy:distinguished",
						"language": "sc:inLanguage", "identifier": "sc:identifier",
					}},
					"id": victim + "/activities/create/1", "type": "Create", "actor": victim, "to": []any{"https://origin.example/c/x", activityPubPublicIRI}, "cc": []any{}, "audience": "https://origin.example/c/x",
					"object": map[string]any{
						"type": "Page", "id": victim + "/post/1", "attributedTo": victim, "to": []any{"https://origin.example/c/x", activityPubPublicIRI}, "name": "title",
						"cc": []any{}, "mediaType": "text/html", "attachment": []any{map[string]any{"href": "https://link.example/", "type": "Link"}},
						"commentsEnabled": true, "sensitive": false, "published": "2026-01-01T00:00:00Z",
						"language": map[string]any{"identifier": "en", "name": "English"}, "audience": "https://origin.example/c/x",
					},
				}
			},
			reads: func(p activityPayload) bool { return p.Object.Type == "Page" && p.Object.Name == "title" },
		},
		{
			name: "Misskey note with a quote and a mention",
			payload: func(string) map[string]any {
				return map[string]any{
					"@context": misskeyContext, "id": victim + "/notes/a1/activity", "type": "Create", "actor": victim,
					"published": "2026-01-01T00:00:00.000Z", "to": []any{activityPubPublicIRI}, "cc": []any{victim + "/followers"},
					"object": map[string]any{
						"id": victim + "/notes/a1", "type": "Note", "attributedTo": victim, "content": "<p>hello @bob</p>",
						"_misskey_content": "hello @bob@paon.example", "source": map[string]any{"content": "hello @bob@paon.example", "mediaType": "text/x.misskeymarkdown"},
						"_misskey_quote": "https://other.example/notes/q1", "quoteUrl": "https://other.example/notes/q1", "quoteUri": "https://other.example/notes/q1",
						"published": "2026-01-01T00:00:00.000Z", "to": []any{activityPubPublicIRI}, "cc": []any{victim + "/followers"}, "inReplyTo": nil,
						"attachment": []any{map[string]any{"type": "Document", "mediaType": "image/webp", "url": "https://files.origin.example/a.webp", "name": nil, "sensitive": true}},
						"sensitive":  false,
						"tag": []any{
							map[string]any{"id": "https://origin.example/emojis/blobcat", "type": "Emoji", "name": ":blobcat:", "updated": "2026-01-01T00:00:00.000Z", "icon": map[string]any{"type": "Image", "mediaType": "image/png", "url": "https://files.origin.example/blobcat.png"}},
							map[string]any{"type": "Mention", "href": bob, "name": "@bob@paon.example"},
						},
						"_misskey_reaction": nil,
					},
				}
			},
			reads: func(p activityPayload) bool {
				return activityPubQuoteURL(p.Object) == "https://other.example/notes/q1" && linkedDataPlacementHasMention(p) && len(p.Object.Attachments) == 1
			},
		},
		{
			name: "Misskey Update(Person)",
			payload: func(publicKeyPEM string) map[string]any {
				return map[string]any{
					"@context": misskeyContext, "id": victim + "#update/1", "type": "Update", "actor": victim, "to": []any{activityPubPublicIRI},
					"object": map[string]any{
						"type": "Person", "id": victim, "inbox": victim + "/inbox", "outbox": victim + "/outbox", "followers": victim + "/followers", "following": victim + "/following",
						"featured": victim + "/collections/featured", "sharedInbox": "https://origin.example/inbox", "endpoints": map[string]any{"sharedInbox": "https://origin.example/inbox"},
						"url": "https://origin.example/@alice", "preferredUsername": "alice", "name": "Alice", "summary": "<p>bio</p>", "_misskey_summary": "bio",
						"icon":  map[string]any{"type": "Image", "url": "https://files.origin.example/avatar.webp", "sensitive": false, "name": nil},
						"image": map[string]any{"type": "Image", "url": "https://files.origin.example/banner.webp", "sensitive": false, "name": nil},
						"tag":   []any{}, "manuallyApprovesFollowers": false, "discoverable": true,
						"publicKey":  map[string]any{"id": victim + "#main-key", "type": "Key", "owner": victim, "publicKeyPem": publicKeyPEM},
						"isCat":      true,
						"attachment": []any{map[string]any{"type": "PropertyValue", "name": "Web", "value": "<a href=\"https://alice.example\">https://alice.example</a>"}},
						"vcard:bday": "2000-01-01", "alsoKnownAs": []any{"https://old.example/users/alice"},
					},
				}
			},
			reads: func(p activityPayload) bool {
				return p.Object.PublicKey != "" && p.Object.AvatarRemoteURL == "https://files.origin.example/avatar.webp" && p.Object.SharedInbox == "https://origin.example/inbox"
			},
		},
		{
			name: "PeerTube video attributed to its account and channel",
			payload: func(string) map[string]any {
				return map[string]any{
					"@context": []any{"https://www.w3.org/ns/activitystreams", activityPubSecurityContext, map[string]any{"RsaSignature2017": "https://w3id.org/security#RsaSignature2017"}, map[string]any{
						"pt": "https://joinpeertube.org/ns#", "sc": "http://schema.org/", "Hashtag": "as:Hashtag", "uuid": "sc:identifier", "category": "sc:category", "licence": "sc:license",
						"sensitive": "as:sensitive", "language": "sc:inLanguage", "identifier": "sc:identifier",
						"views": map[string]any{"@type": "sc:Number", "@id": "pt:views"}, "state": map[string]any{"@type": "sc:Number", "@id": "pt:state"},
						"commentsEnabled": map[string]any{"@type": "sc:Boolean", "@id": "pt:commentsEnabled"},
						"likes":           map[string]any{"@id": "as:likes", "@type": "@id"}, "shares": map[string]any{"@id": "as:shares", "@type": "@id"},
						"comments": map[string]any{"@id": "as:comments", "@type": "@id"}, "subtitleLanguage": "sc:subtitleLanguage",
					}},
					"id": victim + "/videos/watch/1/activity", "type": "Create", "actor": victim, "to": []any{activityPubPublicIRI}, "cc": []any{victim + "/followers"},
					"object": map[string]any{
						"type": "Video", "id": victim + "/videos/watch/1", "name": "A video", "duration": "PT60S", "uuid": "11111111-2222-3333-4444-555555555555",
						"tag":      []any{map[string]any{"type": "Hashtag", "name": "#music"}},
						"category": map[string]any{"identifier": "1", "name": "Music"}, "licence": map[string]any{"identifier": "1", "name": "Attribution"},
						"language": map[string]any{"identifier": "en", "name": "English"}, "views": 10, "sensitive": false, "state": 1, "commentsEnabled": true,
						"published": "2026-01-01T00:00:00.000Z", "mediaType": "text/markdown", "content": "description", "subtitleLanguage": []any{},
						"icon": []any{map[string]any{"type": "Image", "url": "https://files.origin.example/thumb.jpg", "mediaType": "image/jpeg", "width": 280, "height": 157}},
						"url": []any{
							map[string]any{"type": "Link", "mediaType": "text/html", "href": victim + "/videos/watch/1"},
							map[string]any{"type": "Link", "mediaType": "video/mp4", "href": "https://files.origin.example/1-720.mp4", "height": 720},
						},
						"likes": victim + "/videos/watch/1/likes", "shares": victim + "/videos/watch/1/announces", "comments": victim + "/videos/watch/1/comments",
						"attributedTo": []any{map[string]any{"type": "Person", "id": victim}, map[string]any{"type": "Group", "id": "https://origin.example/video-channels/c"}},
						"to":           []any{activityPubPublicIRI}, "cc": []any{victim + "/followers"},
					},
				}
			},
			reads: func(p activityPayload) bool { return p.Object.Type == "Video" && p.Object.AttributedTo == victim },
		},
		{
			name: "Create(Note) with empty tag, attachment and replies arrays",
			payload: func(publicKeyPEM string) map[string]any {
				document := linkedDataPlacementNotePayload(publicKeyPEM)
				status := document["object"].(map[string]any)
				status["tag"] = []any{}
				status["attachment"] = []any{}
				status["replies"] = map[string]any{
					"id": victim + "/statuses/3/replies", "type": "Collection",
					"first": map[string]any{"type": "CollectionPage", "partOf": victim + "/statuses/3/replies", "items": []any{}},
				}
				return document
			},
			reads: func(p activityPayload) bool {
				return len(p.Object.Tags) == 0 && len(p.Object.Attachments) == 0 && p.Object.Replies.ID == victim+"/statuses/3/replies"
			},
		},
		{
			name:    "Announce of a status reference",
			payload: linkedDataPlacementAnnouncePayload,
			reads: func(p activityPayload) bool {
				return p.ObjectReference && p.Object.ID == "https://other.example/statuses/1"
			},
		},
		{
			name: "Announce of an embedded status",
			payload: func(publicKeyPEM string) map[string]any {
				document := linkedDataPlacementAnnouncePayload(publicKeyPEM)
				document["object"] = map[string]any{
					"id": "https://other.example/statuses/1", "type": "Note", "attributedTo": "https://other.example/users/carol",
					"content": "boosted", "to": []any{activityPubPublicIRI},
				}
				return document
			},
			reads: func(p activityPayload) bool {
				return p.Object.ID == "https://other.example/statuses/1" && p.Object.AttributedTo == "https://other.example/users/carol"
			},
		},
		{
			name: "Undo of an embedded Announce",
			payload: func(publicKeyPEM string) map[string]any {
				announce := linkedDataPlacementAnnouncePayload(publicKeyPEM)
				delete(announce, "@context")
				return map[string]any{
					"@context": linkedDataPlacementContext(), "id": victim + "/statuses/6/activity#undo", "type": "Undo", "actor": victim,
					"to": []any{activityPubPublicIRI}, "object": announce,
				}
			},
			reads: func(p activityPayload) bool {
				return p.Object.ID == victim+"/statuses/6/activity" && p.Object.ObjectID == "https://other.example/statuses/1"
			},
		},
		{
			name:    "Move to another account",
			payload: linkedDataPlacementMovePayload,
			reads:   func(p activityPayload) bool { return p.Target == "https://new.example/users/alice" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			body := marshalLinkedDataTestDocument(t, document)
			compactedBody, compacted := activityPubCompactCollectionBody(body)
			if !compacted {
				t.Fatal("document with a literal signature object was not compacted")
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
			if !test.reads(payload) {
				t.Fatalf("payload does not carry the signed values: %s", compactedBody)
			}
		})
	}
}

// linkedDataPlacementAnnouncePayload is the victim's signed boost of a remote
// status.
func linkedDataPlacementAnnouncePayload(string) map[string]any {
	return map[string]any{
		"@context":  linkedDataPlacementContext(),
		"id":        linkedDataForgeryVictimURI + "/statuses/6/activity",
		"type":      "Announce",
		"actor":     linkedDataForgeryVictimURI,
		"published": "2020-01-03T00:00:00Z",
		"to":        []any{activityPubPublicIRI},
		"cc":        []any{"https://other.example/users/carol", linkedDataForgeryVictimURI + "/followers"},
		"object":    "https://other.example/statuses/1",
	}
}

// linkedDataPlacementMovePayload is the victim's signed migration to another
// account.
func linkedDataPlacementMovePayload(string) map[string]any {
	return map[string]any{
		"@context": linkedDataPlacementContext(),
		"id":       linkedDataForgeryVictimURI + "#moves/1",
		"type":     "Move",
		"actor":    linkedDataForgeryVictimURI,
		"object":   linkedDataForgeryVictimURI,
		"target":   "https://new.example/users/alice",
	}
}

// A reference whose node object also holds entries that produce no
// statement (an undefined term, @index or an empty array) keeps the signed
// RDF, but compacts to a map instead of the IRI string Paon reads, so the
// mention, media or avatar it names would be dropped. Such documents are
// never authenticated, also where the decoration does not change what Paon
// reads today.
func TestLinkedDataSignatureRejectsDecoratedReferences(t *testing.T) {
	const bob = "https://paon.example/users/bob"
	object := func(document map[string]any) map[string]any { return document["object"].(map[string]any) }
	decorated := func(id string) string {
		return `reference "` + id + `" carries entries the signature does not cover`
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		forge   func(document map[string]any)
		forged  func(payload activityPayload) bool
		want    string
	}{
		{
			name:    "mention href with an undefined term",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				object(document)["tag"].([]any)[0].(map[string]any)["href"] = map[string]any{"id": bob, "zz": 1}
			},
			forged: func(p activityPayload) bool { return !linkedDataPlacementHasMention(p) },
			want:   decorated(bob),
		},
		{
			name:    "mention href with an index",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				object(document)["tag"].([]any)[0].(map[string]any)["href"] = map[string]any{"@id": bob, "@index": "i"}
			},
			forged: func(p activityPayload) bool { return !linkedDataPlacementHasMention(p) },
			want:   decorated(bob),
		},
		{
			name:    "mention href with an empty array",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				object(document)["tag"].([]any)[0].(map[string]any)["href"] = map[string]any{"id": bob, "name": []any{}}
			},
			forged: func(p activityPayload) bool { return !linkedDataPlacementHasMention(p) },
			want:   decorated(bob),
		},
		{
			name:    "attachment url with an undefined term",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				attachment := object(document)["attachment"].([]any)[0].(map[string]any)
				attachment["url"] = map[string]any{"id": attachment["url"], "zz": 1}
			},
			forged: func(p activityPayload) bool { return len(p.Object.Attachments) == 0 },
			want:   decorated("https://files.origin.example/media/1.png"),
		},
		{
			name:    "avatar url with an undefined term",
			payload: linkedDataPlacementPersonPayload,
			forge: func(document map[string]any) {
				icon := object(document)["icon"].(map[string]any)
				icon["url"] = map[string]any{"id": icon["url"], "zz": 1}
			},
			forged: func(p activityPayload) bool { return p.Object.AvatarRemoteURL == "" },
			want:   decorated("https://files.origin.example/avatar.png"),
		},
		{
			name:    "boosted status with an undefined term",
			payload: linkedDataPlacementAnnouncePayload,
			forge: func(document map[string]any) {
				document["object"] = map[string]any{"id": document["object"], "zz": 1}
			},
			forged: func(p activityPayload) bool { return !p.ObjectReference },
			want:   decorated("https://other.example/statuses/1"),
		},
		{
			name:    "reply target with an undefined term",
			payload: linkedDataPlacementNotePayload,
			forge: func(document map[string]any) {
				object(document)["inReplyTo"] = map[string]any{"id": object(document)["inReplyTo"], "zz": 1}
			},
			want: decorated("https://other.example/statuses/1"),
		},
		{
			name:    "migration target with an undefined term",
			payload: linkedDataPlacementMovePayload,
			forge: func(document map[string]any) {
				document["target"] = map[string]any{"id": document["target"], "zz": 1}
			},
			want: decorated("https://new.example/users/alice"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			test.forge(document)
			assertLinkedDataForgeryRejected(t, document, publicKey, test.forged, test.want)
		})
	}
}

// An empty array produces no RDF statement, so a relay can add one to a
// signed document without invalidating its linked-data signature. The terms
// Paon reads by their presence treat it as absent, so the relayed copy reads
// like the signed document: an open poll stays open, a single-choice poll
// keeps its options, a quote keeps its target and a status without a
// conversation does not join an empty one.
func TestLinkedDataSignatureEmptyPresenceArraysKeepSignedReads(t *testing.T) {
	const (
		as      = "https://www.w3.org/ns/activitystreams#"
		toot    = "http://joinmastodon.org/ns#"
		ostatus = "http://ostatus.org#"
	)
	pollCheckedAt := time.Date(2026, time.November, 20, 0, 0, 0, 0, time.UTC)
	readsPoll := func(p activityPayload) string {
		poll, ok := activityPollFromObject(p.Object, 1, 1, pollCheckedAt)
		return fmt.Sprintf("poll=%v options=%v multiple=%v tallies=%v expires=%v", ok, poll.Options, poll.Multiple, poll.CachedTallies, poll.ExpiresAt)
	}
	readsQuote := func(p activityPayload) string { return activityPubQuoteURL(p.Object) }
	readsConversation := func(p activityPayload) string {
		return fmt.Sprintf("conversation=%q set=%v", p.Object.Conversation, p.Object.ConversationSet)
	}
	noteWith := func(key, value string) func(publicKeyPEM string) map[string]any {
		return func(publicKeyPEM string) map[string]any {
			document := linkedDataPlacementNotePayload(publicKeyPEM)
			document["object"].(map[string]any)[key] = value
			return document
		}
	}
	for _, test := range []struct {
		name    string
		payload func(publicKeyPEM string) map[string]any
		key     string
		reads   func(payload activityPayload) string
	}{
		{name: "closed on an open poll", payload: linkedDataPlacementQuestionPayload, key: as + "closed", reads: readsPoll},
		{name: "anyOf on a single-choice poll", payload: linkedDataPlacementQuestionPayload, key: as + "anyOf", reads: readsPoll},
		{name: "quoteUri in front of _misskey_quote", payload: noteWith("_misskey_quote", "https://other.example/notes/q"), key: toot + "quoteUri", reads: readsQuote},
		{name: "quoteUri in front of quoteUrl", payload: noteWith("quoteUrl", "https://other.example/statuses/q"), key: toot + "quoteUri", reads: readsQuote},
		{name: "quoteUrl in front of _misskey_quote", payload: noteWith("_misskey_quote", "https://other.example/notes/q"), key: as + "quoteUrl", reads: readsQuote},
		{name: "conversation on a status without one", payload: linkedDataPlacementNotePayload, key: ostatus + "conversation", reads: readsConversation},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, publicKey := signedLinkedDataFixture(t, test.payload)
			parse := func(body []byte) (activityPayload, []byte) {
				t.Helper()
				compactedBody, compacted := activityPubCompactCollectionBody(body)
				if !compacted {
					t.Fatal("document with a literal signature object was not compacted")
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
				return payload, compactedBody
			}
			signed, _ := parse(marshalLinkedDataTestDocument(t, document))
			document["object"].(map[string]any)[test.key] = []any{}
			relayed, compactedBody := parse(marshalLinkedDataTestDocument(t, document))
			// Compaction keeps the empty array under the term or its compact IRI.
			_, term, _ := strings.Cut(test.key, "#")
			if !strings.Contains(string(compactedBody), term+`":[]`) {
				t.Fatalf("compacted document does not keep the empty %s: %s", term, compactedBody)
			}
			if got, want := test.reads(relayed), test.reads(signed); got != want {
				t.Fatalf("relayed copy reads %s, want the signed %s", got, want)
			}
		})
	}
}
