package api

import (
	"bytes"
	"database/sql"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestParseAccountUpdatePayloadAcceptsJSONFields(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PATCH", "/api/v1/accounts/update_credentials", strings.NewReader(`{
		"display_name":"Alice",
		"bot":true,
		"source":{"privacy":"unlisted"},
		"fields_attributes":[{"name":"Website","value":"https://example.test"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseAccountUpdatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.DisplayName == nil || *payload.DisplayName != "Alice" {
		t.Fatalf("display_name = %#v", payload.DisplayName)
	}
	if payload.Bot == nil || !*payload.Bot {
		t.Fatalf("bot = %#v", payload.Bot)
	}
	if len(payload.FieldsAttributes) != 1 || payload.FieldsAttributes[0].Name != "Website" {
		t.Fatalf("fields = %#v", payload.FieldsAttributes)
	}
	if payload.Source == nil {
		t.Fatal("source missing")
	}
}

func TestParseAccountUpdatePayloadKeepsSparseRailsFieldIndexes(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PATCH", "/api/v1/accounts/update_credentials", strings.NewReader(`{
		"fields_attributes":{
			"2":{"name":"Second","value":"https://second.example"},
			"10":{"name":"Tenth","value":"https://tenth.example"}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseAccountUpdatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.FieldsAttributes) != 2 || payload.FieldsAttributes[0].Name != "Second" || payload.FieldsAttributes[1].Name != "Tenth" {
		t.Fatalf("fields = %#v", payload.FieldsAttributes)
	}

	req = httptest.NewRequest("PATCH", "/api/v1/accounts/update_credentials", strings.NewReader("fields_attributes%5B2%5D%5Bname%5D=Second&fields_attributes%5B2%5D%5Bvalue%5D=https%3A%2F%2Fsecond.example&fields_attributes%5B10%5D%5Bname%5D=Tenth&fields_attributes%5B10%5D%5Bvalue%5D=https%3A%2F%2Ftenth.example"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)

	payload, err = parseAccountUpdatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.FieldsAttributes) != 2 || payload.FieldsAttributes[0].Value != "https://second.example" || payload.FieldsAttributes[1].Value != "https://tenth.example" {
		t.Fatalf("form fields = %#v", payload.FieldsAttributes)
	}
}

func TestProfileNoteLengthOnlyShortensHTTPURLsLikeRailsNoteValidator(t *testing.T) {
	geminiNote := strings.Repeat("x", 490) + " gemini://example.test/" + strings.Repeat("a", 40)
	if !profileNoteTooLong(geminiNote, 500) {
		t.Fatal("profile note should not apply status-only URL concessions to gemini URLs")
	}
}

func TestParseAccountUpdatePayloadAcceptsSourceSettings(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PATCH", "/api/v1/accounts/update_credentials", strings.NewReader(`{
		"source":{"privacy":"unlisted","sensitive":true,"language":"ja"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseAccountUpdatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Source == nil || payload.Source.Privacy == nil || *payload.Source.Privacy != "unlisted" {
		t.Fatalf("source = %#v", payload.Source)
	}
	if payload.Source.Sensitive == nil || !*payload.Source.Sensitive {
		t.Fatalf("source sensitive = %#v", payload.Source)
	}
	if payload.Source.Language == nil || *payload.Source.Language != "ja" {
		t.Fatalf("source language = %#v", payload.Source)
	}
}

func TestApplyUserPostingPrivacySettingRejectsBlankLikeRails(t *testing.T) {
	for _, raw := range []string{"", "   ", "direct"} {
		settings := map[string]any{"default_privacy": "public"}
		err := applyUserPostingPrivacySetting(settings, raw)
		if err == nil {
			t.Fatalf("privacy %q should be invalid", raw)
		}
		apiErr, ok := err.(apiHTTPError)
		if !ok {
			t.Fatalf("error type = %T", err)
		}
		if apiErr.status != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d", apiErr.status)
		}
		if settings["default_privacy"] != "public" {
			t.Fatalf("invalid privacy should not mutate settings: %#v", settings)
		}
	}
}

func TestApplyUserPostingPrivacySettingAcceptsRailsValues(t *testing.T) {
	for _, raw := range []string{"public", "unlisted", "private", " private "} {
		settings := map[string]any{}
		if err := applyUserPostingPrivacySetting(settings, raw); err != nil {
			t.Fatalf("privacy %q should be valid: %v", raw, err)
		}
		if settings["default_privacy"] != strings.TrimSpace(raw) {
			t.Fatalf("settings = %#v", settings)
		}
	}
}

func TestParseAccountUpdatePayloadTreatsEmptyJSONSourceAsBlankLikeRails(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PATCH", "/api/v1/accounts/update_credentials", strings.NewReader(`{
		"display_name":"I'm a cat",
		"source":{}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseAccountUpdatePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Source != nil {
		t.Fatalf("empty JSON source should be blank like Rails: %#v", payload.Source)
	}
	if payload.DisplayName == nil || *payload.DisplayName != "I'm a cat" {
		t.Fatalf("display_name = %#v", payload.DisplayName)
	}
}

func TestUpdateCredentialsRequiresAuth(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PATCH", "/api/v1/accounts/update_credentials", strings.NewReader(`{"display_name":"Alice"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	s := &Server{}
	err := s.updateCredentials(c)
	if err == nil {
		t.Fatal("expected auth error")
	}
	handleAPIError(c, err)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestProfileCredentialMutationsReturnRoleLikeRailsSerializer(t *testing.T) {
	src, err := os.ReadFile("profile_credentials.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"updateCredentials", "deleteProfileImage"} {
		if !functionBodyContains(t, src, fn, `role, everyone := s.initialStateUserRole(user)`) {
			t.Fatalf("%s must load the credential account role", fn)
		}
		if !functionBodyContains(t, src, fn, `serializer.CredentialAccountFromModelWithRole(s.cfg, *reloaded, *user, count, role, everyone)`) {
			t.Fatalf("%s must return the Rails CredentialAccountSerializer role payload", fn)
		}
		if functionBodyContains(t, src, fn, `serializer.CredentialAccountFromModel(s.cfg, *reloaded, *user, count)`) {
			t.Fatalf("%s must not drop role from credential account responses", fn)
		}
	}
}

func TestPreserveProfileFieldVerificationsMatchesRailsFieldsAttributes(t *testing.T) {
	current := []byte(`[
		{"name":"Site","value":"https://example.test","verified_at":"2026-06-18T12:00:00Z"},
		{"name":"Other","value":"https://other.test","verified_at":null}
	]`)
	manualVerified := "2026-06-19T00:00:00Z"
	fields := preserveProfileFieldVerifications([]profileField{
		{Name: "Homepage", Value: "https://example.test"},
		{Name: "Other", Value: "https://other.test"},
		{Name: "Manual", Value: "https://manual.test", VerifiedAt: &manualVerified},
	}, current)
	if fields[0].VerifiedAt == nil || *fields[0].VerifiedAt != "2026-06-18T12:00:00Z" {
		t.Fatalf("verified field was not preserved: %#v", fields[0])
	}
	if fields[1].VerifiedAt != nil {
		t.Fatalf("unverified field should remain unverified: %#v", fields[1])
	}
	if fields[2].VerifiedAt == nil || *fields[2].VerifiedAt != "2026-06-19T00:00:00Z" {
		t.Fatalf("explicit verified_at should be preserved: %#v", fields[2])
	}
}

func TestProfileImageUpdatesUsePaperclipColumns(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	avatar := profileImageUpdates("avatar", "avatar.png", "image/png", 123, now)
	if avatar["avatar_file_name"] != (sql.NullString{String: "avatar.png", Valid: true}) {
		t.Fatalf("avatar updates = %#v", avatar)
	}
	if avatar["avatar_remote_url"] != nil {
		t.Fatalf("avatar remote url = %#v", avatar["avatar_remote_url"])
	}

	header := profileImageUpdates("header", "header.jpg", "image/jpeg", 456, now)
	if header["header_file_name"] != (sql.NullString{String: "header.jpg", Valid: true}) {
		t.Fatalf("header updates = %#v", header)
	}
	if header["header_remote_url"] != "" {
		t.Fatalf("header remote url = %#v", header["header_remote_url"])
	}
}

func TestProfileImageValidationMatchesRailsAvatarHeaderConcerns(t *testing.T) {
	for _, contentType := range []string{"image/jpeg", "image/png", "image/gif", "image/webp"} {
		if !profileImageContentTypeSupported(contentType) {
			t.Fatalf("profile image content type %q should be allowed", contentType)
		}
	}
	for _, contentType := range []string{"image/heic", "image/heif", "image/avif", "image/svg+xml", "video/mp4"} {
		if profileImageContentTypeSupported(contentType) {
			t.Fatalf("profile image content type %q should be rejected", contentType)
		}
	}
	if profileImageSizeLimit != 2*1024*1024 {
		t.Fatalf("profileImageSizeLimit = %d", profileImageSizeLimit)
	}
}

func TestAccountImagePathMatchesSerializerURLShape(t *testing.T) {
	s := &Server{cfg: config.Config{PublicDir: "/tmp/public"}}
	got := s.accountImagePath(42, "avatar", "avatar.png")
	want := filepath.Join("/tmp/public", "system", "accounts", "avatars", "000", "000", "042", "original", "avatar.png")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	got = s.accountImagePath(42, "header", "header.png")
	want = filepath.Join("/tmp/public", "system", "accounts", "headers", "000", "000", "042", "original", "header.png")
	if got != want {
		t.Fatalf("header path = %q, want %q", got, want)
	}
}

func profileTestGIF(t *testing.T, width int, height int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, width, height), []color.Color{color.Black, color.White})
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if (x+y)%2 == 0 {
				img.SetColorIndex(x, y, 1)
			}
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
