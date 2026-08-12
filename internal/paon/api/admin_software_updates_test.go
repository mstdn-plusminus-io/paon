package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminSoftwareUpdatesDisabledReturnsNotFound(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/software_updates", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestAdminSoftwareUpdatesRequireSessionWhenEnabled(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com", UpdateCheckURL: "https://updates.example"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/software_updates", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/software_updates")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminSoftwareUpdatesHTMLIncludesRailsColumns(t *testing.T) {
	html := adminSoftwareUpdatesHTML([]models.SoftwareUpdate{{
		Version:      "4.3.2",
		Type:         0,
		Urgent:       true,
		ReleaseNotes: "https://example.com/releases/4.3.2",
	}})
	for _, want := range []string{
		"Available updates",
		`<div class="table-wrapper"><table class="table">`,
		`<th>Version</th><th>Type</th><th></th><th></th>`,
		"Version",
		"Type",
		"4.3.2",
		"Patch release",
		"Critical",
		`href="https://example.com/releases/4.3.2"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("software updates html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "<th>Urgency</th>") || strings.Contains(html, "<th>Release notes</th>") {
		t.Fatalf("software updates table should keep Rails blank action/urgency headers: %s", html)
	}
}

func TestCompareSoftwareVersionsSortsNumerically(t *testing.T) {
	versions := []string{"4.10.0", "4.2.0", "4.2.1", "5.0.0"}
	want := []string{"4.2.0", "4.2.1", "4.10.0", "5.0.0"}
	for i := 0; i < len(versions); i++ {
		for j := i + 1; j < len(versions); j++ {
			if compareSoftwareVersions(versions[j], versions[i]) < 0 {
				versions[i], versions[j] = versions[j], versions[i]
			}
		}
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("versions[%d] = %q, want %q (%#v)", i, versions[i], want[i], versions)
		}
	}
}

func TestCriticalSoftwareUpdatesPendingOnlyCountsUrgentFutureVersions(t *testing.T) {
	updates := []models.SoftwareUpdate{
		{Version: "4.2.9", Urgent: true},
		{Version: "4.3.1", Urgent: false},
		{Version: "4.3.2", Urgent: true},
	}
	if !criticalSoftwareUpdatesPending(updates, "4.3.0") {
		t.Fatal("expected urgent future update to be pending")
	}
	if criticalSoftwareUpdatesPending(updates, "4.3.2") {
		t.Fatal("expected current urgent version not to be pending")
	}
	if criticalSoftwareUpdatesPending([]models.SoftwareUpdate{{Version: "4.3.1", Urgent: false}}, "4.3.0") {
		t.Fatal("expected non-urgent future update not to be critical")
	}
	if criticalSoftwareUpdatesPending([]models.SoftwareUpdate{{Version: "4.3.0.rc1", Urgent: true}}, "4.3.0") {
		t.Fatal("expected pre-release urgent version not to be pending for its final release")
	}
	if !criticalSoftwareUpdatesPending([]models.SoftwareUpdate{{Version: "4.3.0", Urgent: true}}, "4.3.0.rc1+paon") {
		t.Fatal("expected final urgent release to be pending for a pre-release current version")
	}
}

func TestPendingSoftwareUpdatesExcludesCurrentAndOlderVersions(t *testing.T) {
	updates := []models.SoftwareUpdate{
		{Version: "3.5.0"},
		{Version: "4.4.22"},
		{Version: "4.4.23"},
		{Version: "4.5.0"},
	}

	pending := pendingSoftwareUpdates(updates, "4.4.22")
	if len(pending) != 2 || pending[0].Version != "4.4.23" || pending[1].Version != "4.5.0" {
		t.Fatalf("pendingSoftwareUpdates() = %#v, want only future versions", pending)
	}
}

func TestSoftwareUpdateTypeLabel(t *testing.T) {
	for value, want := range map[int]string{0: "Patch release", 1: "Minor release", 2: "Major release", 9: "unknown"} {
		if got := softwareUpdateTypeLabel(value); got != want {
			if !strings.Contains(got, want) {
				t.Fatalf("type label %d = %q, want substring %q", value, got, want)
			}
		}
	}
}
