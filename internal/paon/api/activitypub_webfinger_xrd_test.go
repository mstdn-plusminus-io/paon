package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestFetchActivityActorURLFromWebFingerAcceptsMisskeyXRD(t *testing.T) {
	for _, tt := range []struct {
		username string
		actorID  string
	}{
		{username: "nuotyn", actorID: "ar4i6h27q1"},
		{username: "Sizukutyan", actorID: "apxlv1ifol"},
	} {
		t.Run(tt.username, func(t *testing.T) {
			actorURI := "https://misskey.day/users/" + tt.actorID
			body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0"><Subject>acct:%s@misskey.day</Subject><Link rel="self" type="application/activity+json" href="%s"/><Link rel="http://webfinger.net/rel/profile-page" type="text/html" href="https://misskey.day/@%s"/><Link rel="http://ostatus.org/schema/1.0/subscribe" template="https://misskey.day/authorize-follow?acct={uri}"/></XRD>`, tt.username, actorURI, tt.username)
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if got := request.URL.Query().Get("resource"); got != "acct:"+tt.username+"@misskey.day" {
					t.Fatalf("WebFinger resource = %q", got)
				}
				if got := request.Header.Get("Accept"); got != "application/jrd+json, application/json" {
					t.Fatalf("WebFinger Accept = %q", got)
				}
				return textResponse(http.StatusOK, "application/xrd+xml", body), nil
			})}

			if got, err := fetchActivityActorURLFromWebFinger(tt.username + "@misskey.day"); err != nil || got != actorURI {
				t.Fatalf("resolve XRD actor = %q, %v", got, err)
			}
			if err := verifyRemoteActivityActorWebFinger(remoteActivityActor{ID: actorURI, PreferredUsername: tt.username}); err != nil {
				t.Fatalf("verify XRD actor loopback: %v", err)
			}
		})
	}
}

func TestVerifyRemoteActivityActorWebFingerRejectsInvalidXRD(t *testing.T) {
	const validBody = `<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0"><Subject>acct:alice@remote.example</Subject><Link rel="self" type="application/activity+json" href="https://remote.example/users/alice"/></XRD>`
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "wrong actor", body: strings.ReplaceAll(validBody, "/users/alice", "/users/bob"), want: "webfinger response does not loop back to actor"},
		{name: "missing subject", body: strings.ReplaceAll(validBody, "<Subject>acct:alice@remote.example</Subject>", ""), want: "webfinger subject does not match"},
		{name: "foreign subject namespace", body: strings.ReplaceAll(validBody, "<Subject>", `<Subject xmlns="https://untrusted.example/">`), want: "webfinger subject does not match"},
		{name: "foreign link namespace", body: strings.ReplaceAll(validBody, "<Link ", `<Link xmlns="https://untrusted.example/" `), want: "public key not found"},
		{name: "foreign root namespace", body: strings.ReplaceAll(validBody, "http://docs.oasis-open.org/ns/xri/xrd-1.0", "https://untrusted.example/"), want: "invalid webfinger XRD"},
		{name: "missing root namespace", body: strings.ReplaceAll(validBody, ` xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0"`, ""), want: "invalid webfinger XRD"},
		{name: "HTML", body: "<!doctype html><html><body>error</body></html>", want: "invalid webfinger XRD"},
		{name: "malformed XML", body: strings.TrimSuffix(validBody, "</XRD>"), want: "invalid webfinger XRD"},
		{name: "second root", body: validBody + validBody, want: "invalid webfinger XRD"},
		{name: "trailing text", body: validBody + "unexpected", want: "invalid webfinger XRD"},
		{name: "external entity", body: `<!DOCTYPE XRD [<!ENTITY actor SYSTEM "https://untrusted.example/actor">]>` + strings.ReplaceAll(validBody, "acct:alice@remote.example", "&actor;"), want: "invalid webfinger XRD"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			oldClient := activityHTTPClient
			t.Cleanup(func() { activityHTTPClient = oldClient })
			activityHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.URL.String() != "https://remote.example/.well-known/webfinger?resource=acct%3Aalice%40remote.example" {
					t.Fatalf("unexpected WebFinger request: %s", request.URL)
				}
				return textResponse(http.StatusOK, "application/xrd+xml", tt.body), nil
			})}

			err := verifyRemoteActivityActorWebFinger(remoteActivityActor{ID: "https://remote.example/users/alice", PreferredUsername: "alice"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("XRD verification error = %v, want %q", err, tt.want)
			}
			if requests != 1 {
				t.Fatalf("WebFinger requests = %d, want 1", requests)
			}
		})
	}
}
