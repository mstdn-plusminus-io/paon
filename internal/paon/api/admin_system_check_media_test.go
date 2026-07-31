package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminMediaPrivacyDirectoryListingCheck(t *testing.T) {
	attachment := models.MediaAttachment{
		ID:           42,
		FileFileName: sql.NullString{String: "photo.jpg", Valid: true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/system/media_attachments/files/000/000/042/original/" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<a href="photo.jpg">photo.jpg</a>`))
	}))
	defer server.Close()

	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.test", PaperclipRootURL: server.URL + "/system"}}
	if !s.adminDashboardMediaDirectoryListingAccessible(server.Client(), attachment) {
		t.Fatal("directory listing should be considered accessible when it includes the uploaded filename")
	}
	if s.adminDashboardMediaDirectoryListingAccessible(server.Client(), models.MediaAttachment{ID: 42, FileFileName: sql.NullString{String: "other.jpg", Valid: true}}) {
		t.Fatal("directory listing without the uploaded filename should not fail")
	}
}

func TestAdminMediaPrivacyS3ListingCheck(t *testing.T) {
	attachment := models.MediaAttachment{
		ID:           42,
		FileFileName: sql.NullString{String: "photo.jpg", Valid: true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("max-keys") != "1" {
			t.Fatalf("missing max-keys query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`<ListBucketResult><Name>bucket</Name></ListBucketResult>`))
	}))
	defer server.Close()

	s := &Server{cfg: config.Config{S3Enabled: true, StorageHost: server.URL + "/bucket"}}
	if !s.adminDashboardS3ListingAccessible(server.Client(), attachment) {
		t.Fatal("S3 bucket listing should be considered accessible when ListBucketResult is returned")
	}
	urls := s.adminDashboardS3BucketListingURLs(attachment)
	if len(urls) != 1 || !strings.Contains(urls[0], "max-keys=1") {
		t.Fatalf("S3 listing URLs = %#v", urls)
	}
}

func TestAdminDashboardSystemChecksWireMediaPrivacy(t *testing.T) {
	src, err := os.ReadFile("admin_dashboard.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "adminDashboardSystemChecks", "s.adminDashboardMediaPrivacyCheck()") {
		t.Fatal("admin dashboard system checks must include Rails media privacy check equivalent")
	}
}
