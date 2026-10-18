package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func settingsImportMultipartRequest(t *testing.T, target string, fields [][2]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("form_import[data]", "following_accounts.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("Account address,Show boosts\nalice@example.com,true\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestSettingsImportModeUsesLastSubmittedValueLikeRack(t *testing.T) {
	tests := []struct {
		name   string
		target string
		fields [][2]string
		want   bool
	}{
		{
			name:   "overwrite radio after simple_form hidden field",
			target: "/settings/imports",
			fields: [][2]string{{"form_import[type]", "following"}, {"form_import[mode]", ""}, {"form_import[mode]", "overwrite"}},
			want:   true,
		},
		{
			name:   "merge radio after simple_form hidden field",
			target: "/settings/imports",
			fields: [][2]string{{"form_import[type]", "following"}, {"form_import[mode]", ""}, {"form_import[mode]", "merge"}},
			want:   false,
		},
		{
			name:   "hidden field only",
			target: "/settings/imports",
			fields: [][2]string{{"form_import[type]", "following"}, {"form_import[mode]", ""}},
			want:   false,
		},
		{
			name:   "body value wins over query string",
			target: "/settings/imports?form_import[mode]=overwrite",
			fields: [][2]string{{"form_import[type]", "following"}, {"form_import[mode]", ""}, {"form_import[mode]", "merge"}},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := settingsImportMultipartRequest(t, tt.target, tt.fields)
			present, err := requestHasNestedFormOrFilePrefix(req, "form_import", importFileSizeLimit)
			if err != nil {
				t.Fatal(err)
			}
			if !present {
				t.Fatal("form_import root should be present")
			}
			if got := settingsImportOverwriteRequested(req); got != tt.want {
				t.Fatalf("overwrite = %v, want %v", got, tt.want)
			}
			if got := rackFormParamValue(req, "form_import[type]"); got != "following" {
				t.Fatalf("type = %q", got)
			}
		})
	}
}

func TestInviteFromRequestAutofollowUsesCheckboxValueAfterHiddenField(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "checked", body: "invite[max_uses]=&invite[expires_in]=&invite[autofollow]=0&invite[autofollow]=1&invite[comment]=", want: true},
		{name: "unchecked", body: "invite[max_uses]=&invite[expires_in]=&invite[autofollow]=0&invite[comment]=", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/invites", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
			invite, err := inviteFromRequest(c, 42, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if invite.Autofollow != tt.want {
				t.Fatalf("autofollow = %v, want %v", invite.Autofollow, tt.want)
			}
			if !invite.Comment.Valid || invite.Comment.String != "" {
				t.Fatalf("comment = %#v", invite.Comment)
			}
			if invite.MaxUses.Valid || invite.ExpiresAt.Valid {
				t.Fatalf("max_uses/expires_at should stay unset: %#v %#v", invite.MaxUses, invite.ExpiresAt)
			}
		})
	}
}

func TestInviteFromRequestScalarParamsUseLastBodyValueLikeRack(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	body := "invite[max_uses]=1&invite[max_uses]=5&invite[expires_in]=60&invite[expires_in]=1800&invite[comment]=first&invite[comment]=second"
	req := httptest.NewRequest(http.MethodPost, "/invites?invite[max_uses]=99", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	invite, err := inviteFromRequest(c, 42, now)
	if err != nil {
		t.Fatal(err)
	}
	if !invite.MaxUses.Valid || invite.MaxUses.Int64 != 5 {
		t.Fatalf("max_uses = %#v", invite.MaxUses)
	}
	if !invite.ExpiresAt.Valid || !invite.ExpiresAt.Time.Equal(now.Add(1800*time.Second)) {
		t.Fatalf("expires_at = %#v", invite.ExpiresAt)
	}
	if invite.Comment.String != "second" {
		t.Fatalf("comment = %#v", invite.Comment)
	}
}
