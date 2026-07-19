package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestLetterOpenerPageReturnsDevelopmentStub(t *testing.T) {
	resetDevelopmentMailPreviewsForTest()
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/letter_opener/inbox", nil)
	req.Header.Set("Accept-Language", "en")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'self'") || strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("CSP = %q", got)
	}
	for _, want := range []string{"Letter Opener", "Paon", "Mailbox"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestLetterOpenerCapturesDevelopmentMailWhenSMTPIsUnset(t *testing.T) {
	t.Setenv("RAILS_ENV", "development")
	resetDevelopmentMailPreviewsForTest()
	t.Cleanup(resetDevelopmentMailPreviewsForTest)

	cfg := config.Config{Title: "Paon", LocalDomain: "example.com", SMTPFrom: "notifications@example.com"}
	if err := sendMail(cfg, mailMessage{To: "alice@example.test", Subject: "Preview subject", Body: "Preview body"}); err != nil {
		t.Fatal(err)
	}
	previews := developmentMailPreviews()
	if len(previews) != 1 {
		t.Fatalf("preview count = %d", len(previews))
	}
	if previews[0].To != "alice@example.test" || previews[0].Subject != "Preview subject" || !strings.Contains(previews[0].Raw, "Preview body") {
		t.Fatalf("preview = %#v", previews[0])
	}

	s, err := NewServer(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/letter_opener/1", nil)
	req.Header.Set("Accept-Language", "en")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"Preview subject", "alice@example.test", "Preview body"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestLetterOpenerDeliveryMethodSkipsSMTPEvenWhenConfigured(t *testing.T) {
	t.Setenv("RAILS_ENV", "development")
	resetDevelopmentMailPreviewsForTest()
	t.Cleanup(resetDevelopmentMailPreviewsForTest)

	cfg := config.Config{
		Title:              "Paon",
		LocalDomain:        "example.com",
		SMTPFrom:           "notifications@example.com",
		SMTPServer:         "192.0.2.1",
		SMTPPort:           "2525",
		SMTPDeliveryMethod: "letter_opener_web",
	}
	if err := sendMail(cfg, mailMessage{To: "alice@example.test", Subject: "Preview subject", Body: "Preview body"}); err != nil {
		t.Fatal(err)
	}
	previews := developmentMailPreviews()
	if len(previews) != 1 {
		t.Fatalf("preview count = %d", len(previews))
	}
	if previews[0].Subject != "Preview subject" || !strings.Contains(previews[0].Raw, "Preview body") {
		t.Fatalf("preview = %#v", previews[0])
	}
}

func TestLetterOpenerDoesNotCaptureProductionSMTPUnsetMail(t *testing.T) {
	t.Setenv("RAILS_ENV", "production")
	resetDevelopmentMailPreviewsForTest()
	t.Cleanup(resetDevelopmentMailPreviewsForTest)

	if err := sendMail(config.Config{SMTPFrom: "notifications@example.com"}, mailMessage{To: "alice@example.test", Subject: "Production", Body: "body"}); err != nil {
		t.Fatal(err)
	}
	if previews := developmentMailPreviews(); len(previews) != 0 {
		t.Fatalf("production previews = %#v", previews)
	}
}

func TestLetterOpenerPageUsesLocaleCopy(t *testing.T) {
	resetDevelopmentMailPreviewsForTest()
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/letter_opener", nil)
	req.Header.Set("Accept-Language", "ja")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"メールボックス", "メッセージ", "プレビューできます"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("localized body missing %q: %s", want, rec.Body.String())
		}
	}
}
