package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func refetchedMutationClaim(activityType string) activityPayload {
	return activityPayload{
		Type:      activityType,
		ID:        "https://origin.example/users/alice#updates/unverified-activity-id",
		Actor:     "https://origin.example/users/alice",
		Published: "2099-01-01T00:00:00Z",
		To:        []string{"https://unverified.example/recipient"},
		CC:        []string{"https://unverified.example/cc"},
		Object: activityObject{
			ID:           "https://origin.example/notes/1",
			Type:         "Note",
			Content:      "unverified-content",
			AtomURI:      "https://unverified.example/atom-id",
			AttributedTo: "https://origin.example/users/alice",
		},
	}
}

func refetchedMutationDocument() map[string]any {
	return map[string]any{
		"@context":     "https://www.w3.org/ns/activitystreams",
		"id":           "https://origin.example/notes/1",
		"type":         "Note",
		"attributedTo": "https://origin.example/users/alice",
		"content":      "current origin content",
		"published":    "2026-09-20T00:00:00Z",
		"updated":      "2026-09-20T01:00:00Z",
		"to":           []any{activityPubPublicIRI},
		"cc":           []any{"https://origin.example/users/alice/followers"},
	}
}

func encodeRefetchedMutationDocument(t *testing.T, document map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRefetchedMutationUsesOnlyOriginFields(t *testing.T) {
	for _, activityType := range []string{"Create", "Update"} {
		t.Run(activityType, func(t *testing.T) {
			claimed := refetchedMutationClaim(activityType)
			document := refetchedMutationDocument()
			for _, key := range []string{"signature", "proof", "https://w3id.org/security#signature", "https://w3id.org/security#proof"} {
				document[key] = map[string]any{"value": "unverified-origin-proof"}
			}
			body, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, document))
			if err != nil {
				t.Fatal(err)
			}
			fetched, err := parseActivityPayload(body)
			if err != nil {
				t.Fatal(err)
			}
			if fetched.Type != activityType || fetched.Actor != claimed.Actor || fetched.Object.ID != claimed.Object.ID || fetched.Object.Content != "current origin content" || fetched.Object.Updated != "2026-09-20T01:00:00Z" {
				t.Fatalf("wrong refetched payload: %#v", fetched)
			}
			if fetched.Published != "2026-09-20T00:00:00Z" || len(fetched.To) != 1 || fetched.To[0] != activityPubPublicIRI || len(fetched.CC) != 1 || fetched.CC[0] != "https://origin.example/users/alice/followers" {
				t.Fatalf("envelope did not use origin metadata: %#v", fetched)
			}
			for _, untrusted := range []string{"unverified", "2099-01-01T00:00:00Z", `"signature"`, `"proof"`, "https://w3id.org/security#signature", "https://w3id.org/security#proof"} {
				if bytes.Contains(body, []byte(untrusted)) {
					t.Fatalf("refetched body retained %q: %s", untrusted, body)
				}
			}
		})
	}
}

func TestRefetchedMutationRejectsOtherIdentitiesAndDocuments(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"different object", func(doc map[string]any) { doc["id"] = "https://origin.example/notes/2" }},
		{"different user on same origin", func(doc map[string]any) { doc["attributedTo"] = "https://origin.example/users/bob" }},
		{"missing attribution", func(doc map[string]any) { delete(doc, "attributedTo") }},
		{"unsupported type", func(doc map[string]any) { doc["type"] = "Collection" }},
		{"activity instead of object", func(doc map[string]any) { doc["type"] = "Create"; doc["object"] = refetchedMutationDocument() }},
		{"unsupported context", func(doc map[string]any) { doc["@context"] = "https://untrusted.example/context" }},
		{"missing context", func(doc map[string]any) { delete(doc, "@context") }},
		{"graph wrapper", func(doc map[string]any) { doc["@graph"] = []any{refetchedMutationDocument()} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, activityType := range []string{"Create", "Update"} {
				doc := refetchedMutationDocument()
				test.mutate(doc)
				if body, err := activityPubRefetchedMutationBody(refetchedMutationClaim(activityType), encodeRefetchedMutationDocument(t, doc)); err == nil {
					t.Fatalf("%s accepted invalid object: %s", activityType, body)
				}
			}
		})
	}
	for _, body := range []string{"null", "[]", "<html>not JSON</html>"} {
		if _, err := activityPubRefetchedMutationBody(refetchedMutationClaim("Create"), []byte(body)); err == nil {
			t.Fatalf("accepted invalid JSON object %q", body)
		}
	}
	if _, err := activityPubRefetchedMutationBody(refetchedMutationClaim("Announce"), encodeRefetchedMutationDocument(t, refetchedMutationDocument())); err == nil {
		t.Fatal("unsupported mutation type was accepted")
	}
}

func TestRefetchedActorUpdateRequiresExactActorIdentity(t *testing.T) {
	claimed := refetchedMutationClaim("Update")
	claimed.Object.ID = claimed.Actor
	document := map[string]any{
		"@context":          "https://www.w3.org/ns/activitystreams",
		"id":                claimed.Actor,
		"type":              "Person",
		"name":              "current profile",
		"preferredUsername": "alice",
		"inbox":             claimed.Actor + "/inbox",
	}
	body, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, document))
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := parseActivityPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Type != "Update" || fetched.Object.ID != claimed.Actor || fetched.Object.Name != "current profile" {
		t.Fatalf("wrong actor update: %#v", fetched)
	}
	claimed.Type = "Create"
	if _, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, document)); err == nil {
		t.Fatal("Create accepted an actor profile")
	}
	claimed.Type = "Update"
	claimed.Object.ID = "https://origin.example/users/bob"
	document["id"] = claimed.Object.ID
	if _, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, document)); err == nil {
		t.Fatal("actor update accepted another user on the same origin")
	}
}

func TestRefetchedMutationRejectsUnsafeObjectOrigin(t *testing.T) {
	for _, objectURI := range []string{
		"http://origin.example/notes/1",
		"https://origin.example:444/notes/1",
		"https://foreign.example/notes/1",
		"https://user@origin.example/notes/1",
		"https://origin.example/notes/1#fragment",
	} {
		t.Run(objectURI, func(t *testing.T) {
			claimed := refetchedMutationClaim("Create")
			claimed.Object.ID = objectURI
			document := refetchedMutationDocument()
			document["id"] = objectURI
			if _, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, document)); err == nil {
				t.Fatal("unsafe object origin accepted")
			}
		})
	}
}

func TestRefetchedDeleteUsesMinimalTombstone(t *testing.T) {
	claimed := refetchedMutationClaim("Delete")
	document := map[string]any{
		"@context":   "https://www.w3.org/ns/activitystreams",
		"id":         claimed.Object.ID,
		"type":       "Tombstone",
		"atomUri":    "https://origin.example/notes/another-object",
		"formerType": "Note",
		"content":    "unverified tombstone content",
		"signature":  map[string]any{"signatureValue": "unverified"},
	}
	body, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, document))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	object, ok := envelope["object"].(map[string]any)
	if !ok || len(object) != 2 || object["id"] != claimed.Object.ID || object["type"] != "Tombstone" {
		t.Fatalf("Delete retained unnecessary Tombstone fields: %s", body)
	}
	if bytes.Contains(body, []byte("unverified")) || bytes.Contains(body, []byte("atomUri")) {
		t.Fatalf("Delete retained unverified fields: %s", body)
	}
	document["id"] = claimed.Object.ID + "/other"
	if _, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, document)); err == nil {
		t.Fatal("Delete accepted a different Tombstone ID")
	}
	if _, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, refetchedMutationDocument())); err == nil {
		t.Fatal("Delete accepted a live object")
	}
	document["id"] = claimed.Object.ID
	document["attributedTo"] = "https://origin.example/users/bob"
	if _, err := activityPubRefetchedMutationBody(claimed, encodeRefetchedMutationDocument(t, document)); err == nil {
		t.Fatal("Delete accepted a Tombstone attributed to another user")
	}
}

func TestActivityOriginFetchRequiresDirectGoneForDeletion(t *testing.T) {
	const uri = "https://origin.example/notes/1"
	for _, redirected := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct Gone", true: "redirected Gone"}[redirected], func(t *testing.T) {
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			requests := 0
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if redirected && requests == 1 {
					response := textResponse(http.StatusFound, "text/html", "")
					response.Header.Set("Location", "https://origin.example/gone")
					return response, nil
				}
				return textResponse(http.StatusGone, "text/html", "gone"), nil
			})}
			_, err := (&Server{}).fetchActivityPubOriginBody(t.Context(), uri, nil)
			if err == nil {
				t.Fatal("Gone returned no status error")
			}
			var status activityFetchHTTPError
			if redirected {
				if errors.As(err, &status) || requests != 2 {
					t.Fatalf("redirected Gone could authorize deletion: requests=%d error=%v", requests, err)
				}
			} else if !errors.As(err, &status) || status.StatusCode != http.StatusGone || status.URL != uri || requests != 1 {
				t.Fatalf("direct Gone lost target/status: requests=%d error=%v", requests, err)
			}
		})
	}
}

func TestActivityOriginFetchRejectsRedirectOutsideHTTPSOrigin(t *testing.T) {
	const uri = "https://origin.example/notes/1"
	for _, destination := range []string{
		"http://origin.example/notes/1",
		"https://origin.example:444/notes/1",
		"https://foreign.example/notes/1",
		"https://user@origin.example/notes/1",
	} {
		t.Run(destination, func(t *testing.T) {
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			requests := 0
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if requests != 1 {
					t.Fatal("untrusted redirect triggered a request")
				}
				response := textResponse(http.StatusFound, "text/html", "")
				response.Header.Set("Location", destination)
				return response, nil
			})}
			if _, err := (&Server{}).fetchActivityPubOriginBody(t.Context(), uri, nil); err == nil {
				t.Fatal("unsafe redirect accepted")
			}
		})
	}
}

func TestActivityOriginFetchUsesWorkerCancellation(t *testing.T) {
	oldClient := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = oldClient })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (&Server{}).fetchActivityPubOriginBody(ctx, "https://origin.example/notes/1", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch error = %v, want context cancellation", err)
	}
}
