package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"gorm.io/gorm"
)

func TestUnsubscribeEmailTypeFromParamAcceptsRailsTypes(t *testing.T) {
	for _, raw := range []string{"follow", "reblog", "favourite", "mention", "follow_request"} {
		key, ok := unsubscribeEmailTypeFromParam(raw)
		if !ok || key != "notification_emails."+raw {
			t.Fatalf("type %q returned %q ok=%v", raw, key, ok)
		}
	}
}

func TestUnsubscribeEmailTypeFromParamRejectsUnknownTypes(t *testing.T) {
	if _, ok := unsubscribeEmailTypeFromParam("report"); ok {
		t.Fatal("expected report to be rejected for public mail unsubscribe")
	}
}

func TestUnsubscribeHTMLPreservesTokenAndType(t *testing.T) {
	html := unsubscribeHTML("signed-token", "mention", "notification_emails.mention", "user@example.test", "", false)
	for _, want := range []string{`class="container-alt"`, `class="form-container"`, `class="simple_form"`, `class="title"`, `action="/unsubscribe"`, `name="token" value="signed-token"`, `name="type" value="mention"`, "user@example.test"} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
}

func TestMailSubscriptionBlankSecretKeyBaseDoesNotBypassSignedTokenLikeRails(t *testing.T) {
	s := &Server{}
	if _, _, err := s.unsubscribeTokenUser("anything"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("blank SECRET_KEY_BASE unsubscribe token error = %v, want not found", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/unsubscribe?type=mention&token=anything", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := s.unsubscribePage(c); !apiErrorStatus(err, http.StatusNotFound) {
		t.Fatalf("GET blank secret error = %#v status = %d body = %s", err, rec.Code, rec.Body.String())
	}
}

func TestRailsSignedGlobalIDUserIDAcceptsRails72JSONMetadata(t *testing.T) {
	const token = "eyJfcmFpbHMiOnsiZGF0YSI6ImdpZDovL21hc3RvZG9uL1VzZXIvMTIzIiwiZXhwIjoiMjA5OS0wMS0wMlQwMzowNDowNS4wMDBaIiwicHVyIjoidW5zdWJzY3JpYmUifX0=--bae967007fcc5c317b3a0ea3d8e1e4c431517781"
	userID, ok := railsSignedGlobalIDUserID(token, "secret-key-base-for-test", func() time.Time {
		return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	})
	if !ok || userID != 123 {
		t.Fatalf("userID = %d ok=%v", userID, ok)
	}
}

func TestMailSubscriptionInvalidTokenReturnsNotFoundLikeRails(t *testing.T) {
	s := &Server{cfg: config.Config{SecretKeyBase: "secret-key-base-for-test"}}
	if _, _, err := s.unsubscribeTokenUser("invalid-token"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("invalid unsubscribe token error = %v, want not found", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/unsubscribe", strings.NewReader(url.Values{
		"type":  {"mention"},
		"token": {"invalid-token"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := s.createUnsubscribe(c); !apiErrorStatus(err, http.StatusNotFound) {
		t.Fatalf("POST invalid token error = %#v status = %d body = %s", err, rec.Code, rec.Body.String())
	}
}

func TestRailsSignedGlobalIDUserIDRejectsPurposeExpiryAndSignatureMismatch(t *testing.T) {
	const token = "eyJfcmFpbHMiOnsiZGF0YSI6ImdpZDovL21hc3RvZG9uL1VzZXIvMTIzIiwiZXhwIjoiMjA5OS0wMS0wMlQwMzowNDowNS4wMDBaIiwicHVyIjoidW5zdWJzY3JpYmUifX0=--bae967007fcc5c317b3a0ea3d8e1e4c431517781"
	if _, ok := railsSignedGlobalIDUserID(token, "wrong-secret", func() time.Time {
		return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	}); ok {
		t.Fatal("expected signature mismatch to be rejected")
	}
	if _, ok := railsSignedGlobalIDUserID(token, "secret-key-base-for-test", func() time.Time {
		return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	}); ok {
		t.Fatal("expected expired token to be rejected")
	}
	wrongPurpose := []byte(`{"_rails":{"data":"gid://mastodon/User/123","exp":"2099-01-02T03:04:05.000Z","pur":"account"}}`)
	if _, ok := railsMessageData(wrongPurpose, railsSignedGlobalIDPurposeUnsubscribe, func() time.Time {
		return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	}); ok {
		t.Fatal("expected wrong purpose to be rejected")
	}
}

func TestRailsSignedGlobalIDUserIDAcceptsLegacyJSONMetadata(t *testing.T) {
	const token = "eyJnaWQiOiJnaWQ6Ly9tYXN0b2Rvbi9Vc2VyLzMyMSIsInB1cnBvc2UiOiJ1bnN1YnNjcmliZSIsImV4cGlyZXNfYXQiOiIyMDk5LTAxLTAyVDAzOjA0OjA1LjAwMFoifQ==--88a7fdc4c4c71706649fcb331cfdd86910b623aa"
	userID, ok := railsSignedGlobalIDUserID(token, "secret-key-base-for-test", func() time.Time {
		return time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	})
	if !ok || userID != 321 {
		t.Fatalf("userID = %d ok=%v", userID, ok)
	}
}
