package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestVerifiedVerificationFields(t *testing.T) {
	raw := []byte(`[
		{"name":"Site","value":"https://example.com/about","verified_at":"2026-06-19T00:00:00Z"},
		{"name":"Empty","value":"https://empty.example","verified_at":""},
		{"name":"Pending","value":"https://pending.example","verified_at":null},
		{"name":"Blank","value":"   ","verified_at":"2026-06-19T00:00:00Z"}
	]`)

	fields := verifiedVerificationFields(raw)
	if len(fields) != 1 {
		t.Fatalf("len(fields) = %d", len(fields))
	}
	if fields[0].Name != "Site" || fields[0].Value != "https://example.com/about" {
		t.Fatalf("field = %#v", fields[0])
	}
}

func TestLocalAttributionDomainsNormalizesAndValidatesMastodonInput(t *testing.T) {
	domains, err := localAttributionDomains("https://*.Example.COM\nnews.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(domains, ","); got != "example.com,news.example.org" {
		t.Fatalf("domains = %q", got)
	}

	for _, raw := range []string{"example.com/path", "user@example.com", "exa_mple.com", strings.Repeat("x.example\n", 101)} {
		if _, err := localAttributionDomains(raw); err == nil {
			t.Fatalf("localAttributionDomains(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestVerificationSettingsHTMLIncludesAuthorAttributionControls(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "social.example", LocalDomain: "social.example", DefaultLocale: "en"}}
	body := verificationSettingsHTML(s, models.Account{
		Username:           "alice",
		DisplayName:        "Alice",
		AttributionDomains: models.StringArray{"example.com"},
	}, "en", "default", "Paon")

	for _, want := range []string{
		`id="edit_account"`,
		`name=&#34;fediverse:creator&#34;`,
		`@alice@social.example`,
		`name="account[attribution_domains_as_text]"`,
		`autocapitalize="none"`,
		`autocorrect="off"`,
		`spellcheck="false"`,
		`example.com`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("verification settings missing %q: %s", want, body)
		}
	}
}

func TestSettingsVerificationRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/settings/verification", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.settingsVerificationPage(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/verification")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestVerifyProfileFieldLinksBestEffortMarksRelMeLinks(t *testing.T) {
	oldClient := profileVerificationHTTPClient
	defer func() { profileVerificationHTTPClient = oldClient }()
	profileVerificationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != "https://remote.example/about" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
		body := `<html><head><link rel="me alternate" href="https://social.example/@alice"></head></html>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	fields := verifyProfileFieldLinksBestEffort([]profileField{{Name: "Site", Value: "https://remote.example/about"}}, "https://social.example/@alice", time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC))
	if fields[0].VerifiedAt == nil || *fields[0].VerifiedAt != "2026-06-20T12:00:00Z" {
		t.Fatalf("verified_at = %#v", fields[0].VerifiedAt)
	}
}

func TestVerifyActivityPubActorProfileFieldsBestEffortMarksRemoteRelMeLinks(t *testing.T) {
	oldClient := profileVerificationHTTPClient
	defer func() { profileVerificationHTTPClient = oldClient }()
	profileVerificationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != "https://remote.example/about" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
		body := `<a rel="me" href="https://remote.example/@alice">profile</a>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	s := &Server{}
	fields := s.verifyActivityPubActorProfileFieldsBestEffort(models.Account{
		Username: "alice",
		Domain:   sqlNullString("remote.example"),
		URL:      sqlNullString("https://remote.example/@alice"),
		URI:      "https://remote.example/users/alice",
	}, []profileField{{Name: "Site", Value: `<a href="https://remote.example/about">https://remote.example/about</a>`}}, time.Date(2026, 6, 22, 3, 4, 5, 0, time.UTC))
	if fields[0].VerifiedAt == nil || *fields[0].VerifiedAt != "2026-06-22T03:04:05Z" {
		t.Fatalf("verified_at = %#v", fields[0].VerifiedAt)
	}
}

func TestProfileLinkVerifiedRequiresExactRedirectLikeRails(t *testing.T) {
	for _, location := range []string{
		"https://SOCIAL.example/@alice",
		" https://social.example/@alice ",
	} {
		oldClient := profileVerificationHTTPClient
		profileVerificationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.Method + " " + req.URL.String() {
			case "GET https://remote.example/about":
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<a rel="me" href="https://remote.example/redirect">profile</a>`)), Header: make(http.Header)}, nil
			case "HEAD https://remote.example/redirect":
				header := make(http.Header)
				header.Set("Location", location)
				return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: header}, nil
			default:
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
				return nil, nil
			}
		})}
		if profileLinkVerified("https://remote.example/about", "https://social.example/@alice") {
			t.Fatalf("redirect Location %q should not verify; Rails compares redirect_to_url exactly", location)
		}
		profileVerificationHTTPClient = oldClient
	}
}

func TestProfileLinkVerifiedPreservesRelMeHrefWhitespaceLikeRails(t *testing.T) {
	oldClient := profileVerificationHTTPClient
	defer func() { profileVerificationHTTPClient = oldClient }()
	profileVerificationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != "https://remote.example/about" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
		body := `<a rel="me" href=" https://social.example/@alice ">profile</a>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	if profileLinkVerified("https://remote.example/about", "https://social.example/@alice") {
		t.Fatal("padded rel=me href should not verify; Rails compares raw href.downcase without stripping")
	}
}

func TestProfileLinkVerifiedOnlyRedirectsFirstRelMeLikeRails(t *testing.T) {
	oldClient := profileVerificationHTTPClient
	defer func() { profileVerificationHTTPClient = oldClient }()
	requests := []string{}
	profileVerificationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Method+" "+req.URL.String())
		switch req.Method + " " + req.URL.String() {
		case "GET https://remote.example/about":
			body := `<a rel="me" href="https://remote.example/wrong">wrong</a>` +
				`<a rel="me" href="https://remote.example/right-redirect">right</a>`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		case "HEAD https://remote.example/wrong":
			header := make(http.Header)
			header.Set("Location", "https://elsewhere.example/@alice")
			return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: header}, nil
		case "HEAD https://remote.example/right-redirect":
			t.Fatal("Rails VerifyLinkService only checks redirects for links.first")
			return nil, nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	})}

	if profileLinkVerified("https://remote.example/about", "https://social.example/@alice") {
		t.Fatal("second rel=me redirect should not verify when the first rel=me does not redirect back")
	}
	if got := strings.Join(requests, ","); got != "GET https://remote.example/about,HEAD https://remote.example/wrong" {
		t.Fatalf("requests = %q", got)
	}
}

func TestProfileLinkVerifiedKeepsBlankFirstRelMeLikeRails(t *testing.T) {
	oldClient := profileVerificationHTTPClient
	defer func() { profileVerificationHTTPClient = oldClient }()
	requests := []string{}
	profileVerificationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Method+" "+req.URL.String())
		switch req.Method + " " + req.URL.String() {
		case "GET https://remote.example/about":
			body := `<a rel="me">missing href</a>` +
				`<a rel="me" href="https://remote.example/right-redirect">right</a>`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		case "HEAD https://remote.example/right-redirect":
			t.Fatal("Rails VerifyLinkService only redirects links.first, even when its href is blank")
			return nil, nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	})}

	if profileLinkVerified("https://remote.example/about", "https://social.example/@alice") {
		t.Fatal("later rel=me redirect should not verify when first rel=me href is blank")
	}
	if got := strings.Join(requests, ","); got != "GET https://remote.example/about" {
		t.Fatalf("requests = %q", got)
	}
}

func TestVerifiedProfileFieldsForAccountUpdatesPendingLinks(t *testing.T) {
	oldClient := profileVerificationHTTPClient
	defer func() { profileVerificationHTTPClient = oldClient }()
	profileVerificationHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != "https://remote.example/about" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
		}
		body := `<a rel="me" href="https://social.example/@alice">profile</a>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "social.example"}}
	account := models.Account{
		ID:       7,
		Username: "alice",
		Fields: []byte(`[
			{"name":"Site","value":"https://remote.example/about","verified_at":null},
			{"name":"Other","value":"not a url","verified_at":null}
		]`),
	}
	encoded, changed, err := s.verifiedProfileFieldsForAccount(account, time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected profile fields to change")
	}
	body := string(encoded)
	if !strings.Contains(body, `"verified_at":"2026-06-21T01:02:03Z"`) {
		t.Fatalf("verified_at was not saved: %s", body)
	}
}

func TestProfileVerificationWorkerUsesExistingAccountsFields(t *testing.T) {
	src, err := os.ReadFile("profile_verification_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Where("fields::text LIKE ?", "%\"verified_at\":null%")`,
		`s.verifiedProfileFieldsForAccount(account, now)`,
		`"fields": models.JSONValue(encoded)`,
		`"updated_at": now.UTC()`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("profile_verification_worker.go missing %q", want)
		}
	}
	startup, err := os.ReadFile("activitypub_retry.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, startup, "StartBackgroundWorkers", "workers.Go(ctx, s.runProfileVerificationWorker)") {
		t.Fatal("StartBackgroundWorkers does not start profile verification worker")
	}
}
