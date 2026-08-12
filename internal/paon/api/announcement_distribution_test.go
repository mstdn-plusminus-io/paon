package api

import (
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAnnouncementPublishedMailUsesBulkCompatibleLocalizedShape(t *testing.T) {
	message := announcementPublishedMailMessage(config.Config{LocalDomain: "social.example"}, models.User{Email: "alice@example.com"}, models.Announcement{Text: "Maintenance tonight"})
	if message.To != "alice@example.com" || message.Subject == "" {
		t.Fatalf("message = %#v", message)
	}
	for _, want := range []string{"social.example", "Maintenance tonight"} {
		if !strings.Contains(message.Body, want) {
			t.Fatalf("body missing %q: %q", want, message.Body)
		}
	}
}

func TestBulkDistributionMailTaskIDsAreRecipientAndReleaseSpecific(t *testing.T) {
	announcement := announcementDistributionMailTaskID(42, 7)
	if announcement != "announcement-42-user-7" || announcement == announcementDistributionMailTaskID(42, 8) || announcement == announcementDistributionMailTaskID(43, 7) {
		t.Fatalf("announcement distribution task ID = %q", announcement)
	}
	terms := termsOfServiceDistributionMailTaskID(42, 7)
	if terms != "terms-of-service-42-user-7" || terms == termsOfServiceDistributionMailTaskID(42, 8) || terms == announcement {
		t.Fatalf("terms distribution task ID = %q", terms)
	}
	for _, path := range []string{"announcement_distribution.go", "terms_of_service_distribution.go"} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(source), "asynq.Retention(7*24*time.Hour)") {
			t.Fatalf("%s does not retain completed per-user IDs across distribution retries", path)
		}
	}
}

func TestAnnouncementDistributionRoutesAndWorkerAreRegistered(t *testing.T) {
	server, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	workers, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`e.GET("/admin/announcements/:id/preview", s.adminAnnouncementPreviewPage)`,
		`e.POST("/admin/announcements/:id/test", s.testAdminAnnouncementDistribution)`,
		`e.POST("/admin/announcements/:id/distribution", s.distributeAdminAnnouncement)`,
	} {
		if !strings.Contains(string(server), want) {
			t.Fatalf("server.go missing %s", want)
		}
	}
	if !strings.Contains(string(workers), `mux.HandleFunc(asynqTaskDistributeAnnouncement, s.handleAsynqDistributeAnnouncement)`) {
		t.Fatal("announcement distribution worker is not registered")
	}
}

func TestAnnouncementPreviewExtendsExistingAdminDesign(t *testing.T) {
	html := adminAnnouncementPreviewHTML(models.Announcement{ID: 42, Text: "Maintenance"}, 12, "admin@example.com", "", "", "en", "system")
	for _, want := range []string{
		`class="prose"`,
		`/admin/announcements/42/test`,
		`/admin/announcements/42/distribution`,
		`admin@example.com`,
		`12`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("preview HTML missing %q: %s", want, html)
		}
	}
}
