package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

const unsignedRelayAnnounceBody = `{"@context":"https://www.w3.org/ns/activitystreams","id":"https://mstdn.kemono-friends.info/users/likewolfpenguin/statuses/117295428021982352/activity","type":"Announce","actor":"https://mstdn.kemono-friends.info/users/likewolfpenguin","published":"2026-09-19T02:49:54Z","to":["https://www.w3.org/ns/activitystreams#Public"],"cc":["https://mstdn.kemono-friends.info/users/oaug","https://mstdn.kemono-friends.info/users/likewolfpenguin/followers"],"object":"https://mstdn.kemono-friends.info/users/oaug/statuses/117292814805647407"}`

func TestCanonicalRelayAnnounceUsesOriginBody(t *testing.T) {
	claimed, err := parseActivityPayload([]byte(unsignedRelayAnnounceBody))
	if err != nil {
		t.Fatal(err)
	}
	// The relay can tamper with recipients, time, or an embedded target. None
	// of those fields may replace the document authenticated by origin fetch.
	claimed.To = []string{"https://attacker.example/actor"}
	claimed.Published = "2099-01-01T00:00:00Z"
	claimed.Object.Type = "Note"
	claimed.Object.Content = "forged content"
	claimed.Object.AttributedTo = claimed.Actor
	oldClient := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = oldClient })
	requests := 0
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != claimed.ID || request.Header.Get("Accept") != activityDereferencerAcceptHeader {
			t.Fatalf("canonical request = %s, Accept = %s", request.URL, request.Header.Get("Accept"))
		}
		return textResponse(http.StatusOK, "application/activity+json", unsignedRelayAnnounceBody), nil
	})}
	server := &Server{cfg: config.Config{LocalDomain: "paon.example"}}
	body, err := server.fetchActivityPubCanonicalAnnounce(t.Context(), claimed, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := parseActivityPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !fetched.ObjectReference || fetched.Object.Content != "" || fetched.Published != "2026-09-19T02:49:54Z" || len(fetched.To) != 1 || fetched.To[0] != activityPubPublicIRI {
		t.Fatalf("fetched payload retained relay fields: %#v, requests=%d", fetched, requests)
	}
}

func TestCanonicalRelayAnnounceRejectsDifferentIdentity(t *testing.T) {
	claimed, err := parseActivityPayload([]byte(unsignedRelayAnnounceBody))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, field string
		value       any
	}{
		{name: "activity ID", field: "id", value: claimed.ID + "/other"},
		{name: "activity type", field: "type", value: "Delete"},
		{name: "actor", field: "actor", value: "https://mstdn.kemono-friends.info/users/other"},
		{name: "target", field: "object", value: claimed.Object.ID + "/other"},
		{name: "target note alone is not proof of Announce", field: "type", value: "Note"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(unsignedRelayAnnounceBody), &document); err != nil {
				t.Fatal(err)
			}
			document[test.field] = test.value
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return textResponse(http.StatusOK, "application/activity+json", string(body)), nil
			})}
			if _, err := (&Server{}).fetchActivityPubCanonicalAnnounce(t.Context(), claimed, nil); err == nil {
				t.Fatal("different canonical identity was accepted")
			}
		})
	}
}

func TestCanonicalRelayAnnounceRestrictsRedirects(t *testing.T) {
	claimed, err := parseActivityPayload([]byte(unsignedRelayAnnounceBody))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, destination string
		accepted          bool
	}{
		{name: "same origin", destination: "https://mstdn.kemono-friends.info/redirected/activity", accepted: true},
		{name: "foreign origin", destination: "https://attacker.example/activity"},
		{name: "HTTPS downgrade", destination: "http://mstdn.kemono-friends.info/activity"},
		{name: "different port", destination: "https://mstdn.kemono-friends.info:444/activity"},
		{name: "URL credentials", destination: "https://attacker@mstdn.kemono-friends.info/activity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			requests := 0
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.URL.String() == claimed.ID {
					response := textResponse(http.StatusFound, "text/html", "")
					response.Header.Set("Location", test.destination)
					return response, nil
				}
				return textResponse(http.StatusOK, "application/activity+json", unsignedRelayAnnounceBody), nil
			})}
			_, err := (&Server{}).fetchActivityPubCanonicalAnnounce(t.Context(), claimed, nil)
			if (err == nil) != test.accepted {
				t.Fatalf("redirect error = %v, accepted = %v", err, test.accepted)
			}
			if !test.accepted && requests != 1 {
				t.Fatalf("disallowed redirect issued %d requests", requests)
			}
		})
	}
}

func TestCanonicalRelayAnnounceRejectsUntrustedResponse(t *testing.T) {
	claimed, err := parseActivityPayload([]byte(unsignedRelayAnnounceBody))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		make func() *http.Response
	}{
		{name: "HTML", make: func() *http.Response { return textResponse(http.StatusOK, "text/html", unsignedRelayAnnounceBody) }},
		{name: "not found", make: func() *http.Response {
			return textResponse(http.StatusNotFound, "application/activity+json", unsignedRelayAnnounceBody)
		}},
		{name: "graph wrapper", make: func() *http.Response {
			return textResponse(http.StatusOK, "application/activity+json", `{"@context":"https://www.w3.org/ns/activitystreams","type":"Announce","@graph":[`+unsignedRelayAnnounceBody+`]}`)
		}},
		{name: "too large", make: func() *http.Response {
			return textResponse(http.StatusOK, "application/activity+json", strings.Repeat(" ", maxActivityResourceBodySize+1))
		}},
		{name: "gzip too large", make: func() *http.Response {
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			_, _ = writer.Write([]byte(strings.Repeat(" ", maxActivityResourceBodySize+1)))
			_ = writer.Close()
			response := textResponse(http.StatusOK, "application/activity+json", compressed.String())
			response.Header.Set("Content-Encoding", "gzip")
			return response
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) { return test.make(), nil })}
			if _, err := (&Server{}).fetchActivityPubCanonicalAnnounce(t.Context(), claimed, nil); err == nil {
				t.Fatal("untrusted canonical response was accepted")
			}
		})
	}
}

func TestUnsignedAnnounceRecoveryDoesNotBypassSuppliedSignature(t *testing.T) {
	for _, value := range []string{`null`, `"invalid"`, `{}`, `{"type":"RsaSignature2017","signatureValue":"invalid"}`} {
		for _, key := range []string{"signature", "https://w3id.org/security#signature"} {
			t.Run(key+value, func(t *testing.T) {
				body := []byte(strings.TrimSuffix(unsignedRelayAnnounceBody, "}") + "," + strconv.Quote(key) + ":" + value + "}")
				// Deliberately parse the unsigned copy to simulate failed compaction
				// discarding the supplied signature before actor authentication.
				payload, err := parseActivityPayload([]byte(unsignedRelayAnnounceBody))
				if err != nil {
					t.Fatal(err)
				}
				if activityPubUnsignedAnnounce(body, payload) {
					t.Fatal("supplied signature can enter unsigned recovery")
				}
				graph := []byte(`{"@context":"https://www.w3.org/ns/activitystreams","@graph":[` + string(body) + `]}`)
				if activityPubUnsignedAnnounce(graph, payload) {
					t.Fatal("signature inside graph can enter unsigned recovery")
				}
			})
		}
	}
}

func TestCanonicalRelayAnnounceUsesWorkerContext(t *testing.T) {
	claimed, err := parseActivityPayload([]byte(unsignedRelayAnnounceBody))
	if err != nil {
		t.Fatal(err)
	}
	oldClient := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = oldClient })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (&Server{}).fetchActivityPubCanonicalAnnounce(ctx, claimed, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch error = %v, want context canceled", err)
	}
}

func TestCanonicalRelayAnnounceStripsUnverifiedOriginSignature(t *testing.T) {
	claimed, err := parseActivityPayload([]byte(unsignedRelayAnnounceBody))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSuffix(unsignedRelayAnnounceBody, "}") + `,"signature":{"type":"RsaSignature2017","signatureValue":"unverified"}}`
	oldClient := activityHTTPClient
	t.Cleanup(func() { activityHTTPClient = oldClient })
	activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := textResponse(http.StatusOK, "application/activity+json", "")
		response.Body = io.NopCloser(strings.NewReader(body))
		return response, nil
	})}
	fetched, err := (&Server{}).fetchActivityPubCanonicalAnnounce(t.Context(), claimed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(fetched, []byte(`"signature"`)) {
		t.Fatal("canonical body retained unverified linked-data signature")
	}
}

func TestCanonicalRelayAnnounceRejectsUnsafeOrigin(t *testing.T) {
	for _, test := range []struct{ name, activity, actor string }{
		{"foreign actor", "https://relay.example/1", "https://origin.example/actor"},
		{"plain HTTP", "http://origin.example/1", "http://origin.example/actor"},
		{"different port", "https://origin.example:444/1", "https://origin.example/actor"},
		{"activity fragment", "https://origin.example/1#activity", "https://origin.example/actor"},
		{"credentials", "https://user@origin.example/1", "https://origin.example/actor"},
		{"localhost", "https://127.0.0.1/1", "https://127.0.0.1/actor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				t.Fatal("unsafe origin triggered an HTTP request")
				return nil, nil
			})}
			claimed := activityPayload{Type: "Announce", ID: test.activity, Actor: test.actor, Object: activityObject{ID: "https://target.example/1"}}
			if _, err := (&Server{}).fetchActivityPubCanonicalAnnounce(t.Context(), claimed, nil); err == nil {
				t.Fatal("unsafe origin was accepted")
			}
		})
	}
}
