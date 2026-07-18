package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminAccountModerationNoteCreateRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/account_moderation_notes", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/account_moderation_notes")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminAccountModerationNoteDestroyRequiresSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/account_moderation_notes/4", strings.NewReader("_method=delete"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/account_moderation_notes/4")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminAccountModerationNotesWriteAuditLogs(t *testing.T) {
	src, err := os.ReadFile("admin_account_moderation_notes.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"createAdminAccountModerationNoteWeb", `logAdminAction(tx, user.AccountID, "create", accountModerationNoteAuditLogTarget(note), now)`},
		{"destroyAdminAccountModerationNoteWeb", `logAdminAction(tx, user.AccountID, "destroy", accountModerationNoteAuditLogTarget(note), now)`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s does not write audit log", check.fn)
		}
	}
}

func TestAdminAccountModerationNoteRedirectErrorsUseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("admin_account_moderation_notes.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"createAdminAccountModerationNoteWeb", `locale := s.webLocale(c, user)`},
		{"createAdminAccountModerationNoteWeb", `adminAccountModerationNoteMessage(locale, "errors.invalid", "Account moderation note is invalid")`},
		{"createAdminAccountModerationNoteWeb", `adminAccountModerationNoteMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing localized redirect fragment %q", check.fn, check.want)
		}
	}
	for _, forbidden := range []string{
		`QueryEscape("Account moderation note is invalid")`,
		`QueryEscape("DATABASE_URL is not set")`,
	} {
		if functionBodyContains(t, src, "createAdminAccountModerationNoteWeb", forbidden) {
			t.Fatalf("createAdminAccountModerationNoteWeb still contains non-localized redirect literal %q", forbidden)
		}
	}
}

func TestAdminAccountModerationNoteMessagesResolveJapaneseLocale(t *testing.T) {
	tests := []struct {
		key      string
		fallback string
		english  string
		japanese string
	}{
		{"errors.invalid", "Account moderation note is invalid", "Account moderation note", "不正"},
		{"errors.database_unavailable", "DATABASE_URL is not set", "DATABASE_URL", "DATABASE_URL"},
	}
	for _, tt := range tests {
		en := adminAccountModerationNoteMessage("en", tt.key, tt.fallback)
		ja := adminAccountModerationNoteMessage("ja", tt.key, tt.fallback)
		if !strings.Contains(en, tt.english) {
			t.Fatalf("%s en = %q, want containing %q", tt.key, en, tt.english)
		}
		if ja == tt.fallback || !strings.Contains(ja, tt.japanese) {
			t.Fatalf("%s ja = %q, want localized text containing %q", tt.key, ja, tt.japanese)
		}
	}
}

func TestAdminAccountHTMLIncludesModerationNotes(t *testing.T) {
	html := adminAccountHTML(models.Account{ID: 7, Username: "alice"}, "", "", models.AccountModerationNote{
		ID:        4,
		Content:   "watch this account",
		Account:   models.Account{ID: 9, Username: "mod"},
		CreatedAt: time.Date(2026, 6, 19, 1, 2, 3, 0, time.UTC),
	})
	for _, want := range []string{
		"View and leave notes to other moderators and your future self",
		"watch this account",
		`class="report-notes__item__avatar"`,
		`class="report-notes__item__header"`,
		`class="report-notes__item__content"`,
		`class="report-notes__item__actions"`,
		`href="/admin/accounts/9"`,
		`>@mod</a>`,
		`href="/admin/account_moderation_notes/4"`,
		`data-method="delete"`,
		`action="/admin/account_moderation_notes"`,
		`name="account_moderation_note[target_account_id]" value="7"`,
		`name="account_moderation_note[content]"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("account html missing %q: %s", want, html)
		}
	}
}

func TestAdminAccountModerationNoteHTMLEscapesContent(t *testing.T) {
	html := adminAccountModerationNoteHTML(models.AccountModerationNote{
		ID:        4,
		Content:   `<script>alert(1)</script>`,
		Account:   models.Account{ID: 9, Username: "mod"},
		CreatedAt: time.Now().UTC(),
	}, "en", config.Config{})
	if strings.Contains(html, "<script>") {
		t.Fatalf("note html did not escape content: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("note html missing escaped content: %s", html)
	}
}

func TestAdminAccountModerationNoteAvatarURLUsesPaperclipStorageConfig(t *testing.T) {
	account := models.Account{
		ID:             9,
		Username:       "mod",
		AvatarFileName: sql.NullString{String: "avatar image.png", Valid: true},
	}
	if got := adminAccountModerationNoteAvatarURL(config.Config{Scheme: "https", WebDomain: "example.test", PaperclipRootURL: "/uploads"}, account); got != "https://example.test/uploads/accounts/avatars/000/000/009/original/avatar%20image.png" {
		t.Fatalf("custom Paperclip root avatar URL = %q", got)
	}
	if got := adminAccountModerationNoteAvatarURL(config.Config{StorageHost: "https://media.example.test/root"}, account); got != "https://media.example.test/root/accounts/avatars/000/000/009/original/avatar%20image.png" {
		t.Fatalf("storage host avatar URL = %q", got)
	}
}
