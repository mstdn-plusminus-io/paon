package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminAnnouncementsRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/announcements?published=1", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/announcements?published=1")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminAnnouncementMessagesResolveJapaneseLocale(t *testing.T) {
	tests := []struct {
		key      string
		fallback string
		english  string
		japanese string
	}{
		{"errors.invalid", "Announcement is invalid", "Announcement is invalid", "不正"},
		{"errors.text_blank", "Announcement text can't be blank", "Announcement text", "本文"},
		{"errors.start_and_end_required", "Announcement start and end must both be present", "start and end", "開始日時"},
		{"errors.database_unavailable", "DATABASE_URL is not set", "DATABASE_URL", "DATABASE_URL"},
	}
	for _, tt := range tests {
		en := adminAnnouncementMessage("en", tt.key, tt.fallback)
		ja := adminAnnouncementMessage("ja", tt.key, tt.fallback)
		if !strings.Contains(en, tt.english) {
			t.Fatalf("%s en = %q, want containing %q", tt.key, en, tt.english)
		}
		if !strings.Contains(ja, tt.japanese) || ja == tt.fallback {
			t.Fatalf("%s ja = %q, want localized text containing %q", tt.key, ja, tt.japanese)
		}
	}
	if got := adminAnnouncementErrorText("ja", errAdminSetting("Announcement text can't be blank")); strings.Contains(got, "Announcement text") || !strings.Contains(got, "本文") {
		t.Fatalf("localized validation error = %q", got)
	}
	if got := adminAnnouncementErrorText("ja", errAdminSetting("datetime is invalid")); strings.Contains(got, "datetime") || !strings.Contains(got, "日時") {
		t.Fatalf("localized datetime error = %q", got)
	}
}

func TestAdminAnnouncementsHTMLIncludesRailsFields(t *testing.T) {
	html := adminAnnouncementsIndexHTML([]models.Announcement{{ID: 2, Text: "Maintenance", Published: true}}, "saved", "", adminAnnouncementFilters{Page: "1"})
	for _, want := range []string{
		"Announcements",
		`href="/admin/announcements/new"`,
		`href="/admin/announcements/2/edit"`,
		"Maintenance",
		`data-method="post"`,
		`href="/admin/announcements/2/unpublish"`,
		`data-method="delete"`,
		`href="/admin/announcements/2"`,
		`class="announcements-list"`,
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("announcements html missing %q: %s", want, html)
		}
	}
}

func TestAdminAnnouncementsHTMLRendersRailsPaginationLinks(t *testing.T) {
	announcements := make([]models.Announcement, adminRailsDefaultPageSize)
	for i := range announcements {
		announcements[i] = models.Announcement{ID: int64(i + 1), Text: "Maintenance"}
	}
	html := adminAnnouncementsIndexHTML(announcements, "", "", adminAnnouncementFilters{Page: "2", Published: "1", Unpublished: "later"})
	for _, want := range []string{
		`href="/admin/announcements?page=1&amp;published=1&amp;unpublished=later"`,
		`href="/admin/announcements?page=3&amp;published=1&amp;unpublished=later"`,
		"Prev",
		"Next",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("announcements html missing pagination %q: %s", want, html)
		}
	}
}

func TestAdminAnnouncementModelsUseRailsPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_announcements.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		functionName string
		want         string
	}{
		{"adminAnnouncementsPage", "s.adminAnnouncementModels(c)"},
		{"adminAnnouncementsPage", "adminAnnouncementsIndexHTML(announcements, c.QueryParam(\"notice\"), c.QueryParam(\"error\"), filters, s.webLocale(c, user))"},
		{"adminAnnouncementModels", "Offset(adminRailsPageOffset(c))"},
		{"adminAnnouncementModels", "Limit(adminRailsDefaultPageSize)"},
	} {
		if !functionBodyContains(t, src, check.functionName, check.want) {
			t.Fatalf("%s missing %q", check.functionName, check.want)
		}
	}
}

func TestAdminAnnouncementFormHTMLIncludesRailsFields(t *testing.T) {
	html := adminAnnouncementFormHTML("New announcement", "/admin/announcements", "", adminAnnouncementForm{
		Text:   "Maintenance",
		AllDay: true,
	}, false, "Create announcement", "bad")
	for _, want := range []string{
		"New announcement",
		`action="/admin/announcements"`,
		`name="announcement[starts_at]"`,
		`name="announcement[ends_at]"`,
		`name="announcement[all_day]" value="1" checked`,
		`name="announcement[text]"`,
		`name="announcement[scheduled_at]"`,
		"Create announcement",
		"Maintenance",
		"bad",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("announcement form html missing %q: %s", want, html)
		}
	}
	editHTML := adminAnnouncementEditFormHTML(models.Announcement{ID: 3}, adminAnnouncementForm{Text: "Maintenance"}, "", "en")
	if !strings.Contains(editHTML, "Save changes") || strings.Contains(editHTML, "Create announcement") {
		t.Fatalf("announcement edit submit label mismatch: %s", editHTML)
	}
}
