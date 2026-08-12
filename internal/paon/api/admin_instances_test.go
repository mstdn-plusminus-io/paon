package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminInstancesRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/instances?limited=1"},
		{http.MethodGet, "/admin/instances.html?limited=1"},
		{http.MethodPost, "/admin/instances/remote.example/clear_delivery_errors.html"},
		{http.MethodPost, "/admin/instances/remote.example/restart_delivery.html"},
		{http.MethodPost, "/admin/instances/remote.example/stop_delivery.html"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)

		if rec.Code != http.StatusFound {
			t.Fatalf("%s %s status = %d body = %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		want := "/auth/sign_in?redirect_to=" + url.QueryEscape(tc.path)
		if got := rec.Header().Get("Location"); got != want {
			t.Fatalf("%s %s Location = %q, want %q", tc.method, tc.path, got, want)
		}
	}
}

func TestEscapeLikePattern(t *testing.T) {
	if got := escapeLikePattern(`foo_%\bar`); got != `foo\_\%\\bar` {
		t.Fatalf("pattern = %q", got)
	}
}

func TestExhaustedDeliveriesRedisKey(t *testing.T) {
	if got, want := exhaustedDeliveriesRedisKey("mastodon:", "HTTPS://Remote.Example/inbox"), "mastodon:exhausted_deliveries:remote.example"; got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
	if got := exhaustedDeliveriesRedisKey("mastodon:", " "); got != "" {
		t.Fatalf("blank host key = %q, want empty", got)
	}
}

func TestUnavailableDomainsCacheRedisKeysMatchRailsCacheNamespaceCandidates(t *testing.T) {
	got := unavailableDomainsCacheRedisKeys(config.Config{RedisNamespace: "mastodon:"})
	want := []string{"unavailable_domains", "cache:unavailable_domains", "mastodon:unavailable_domains", "mastodon_cache:unavailable_domains"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unavailable domain cache keys = %#v, want %#v", got, want)
	}
}

func TestAdminInstanceListItemHTMLIncludesPolicyAndLinks(t *testing.T) {
	html := adminInstanceListItemHTML(adminInstanceRow{
		Instance:    models.Instance{Domain: "remote.example", AccountsCount: 1234},
		DomainBlock: &models.DomainBlock{Severity: domainBlockSeverityValue("suspend"), RejectMedia: true},
		Unavailable: &models.UnavailableDomain{Domain: "remote.example"},
	})
	for _, want := range []string{
		`href="/admin/instances/remote.example"`,
		`class="fa fa-warning fa-fw"`,
		"remote.example",
		"suspend / reject media",
		"1,234",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("instance item html missing %q: %s", want, html)
		}
	}
}

func TestAdminInstanceListItemHTMLIncludesRailsFailingWarning(t *testing.T) {
	html := adminInstanceListItemHTML(adminInstanceRow{
		Instance:    models.Instance{Domain: "remote.example", AccountsCount: 12},
		FailureDays: 3,
	})
	if !strings.Contains(html, "remote.example") || !strings.Contains(html, "unsuccessful") {
		t.Fatalf("failing instance item html missing delivery warning: %s", html)
	}
}

func TestQueryAdminInstancesUsesRailsKaminariPageSizeAndOffset(t *testing.T) {
	src, err := os.ReadFile("admin_instances.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Offset(adminRailsPageOffset(c))",
		"Limit(adminRailsDefaultPageSize)",
		`JOIN domain_allows ON lower(domain_allows.domain) = lower(instances.domain)`,
		`JOIN domain_blocks ON lower(domain_blocks.domain) = lower(instances.domain)`,
		`JOIN unavailable_domains ON lower(unavailable_domains.domain) = lower(instances.domain)`,
		`domains, err := s.adminInstanceWarningDomains(redisCtx)`,
		`query = query.Where("instances.domain IN ?", domains)`,
	} {
		if !functionBodyContains(t, src, "queryAdminInstances", want) {
			t.Fatalf("queryAdminInstances missing %q", want)
		}
	}
}

func TestDecorateAdminInstancesUsesCaseInsensitiveDomainLookups(t *testing.T) {
	src, err := os.ReadFile("admin_instances.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`domains = append(domains, strings.ToLower(instance.Domain))`,
		`Where("lower(domain) IN ?", domains)`,
		`blockMap[strings.ToLower(block.Domain)] = block`,
		`allowMap[strings.ToLower(allow.Domain)] = allow`,
		`unavailableMap[strings.ToLower(unavailable.Domain)] = unavailable`,
		`domainKey := strings.ToLower(instance.Domain)`,
	} {
		if !functionBodyContains(t, src, "decorateAdminInstances", want) {
			t.Fatalf("decorateAdminInstances missing %q", want)
		}
	}
}

func TestPurgeAdminInstanceDomainDeletesPaperclipFilesBeforeRows(t *testing.T) {
	src, err := os.ReadFile("admin_instances.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "runPurgeAdminInstanceDomain", `s.purgeAdminInstanceDomainFiles(tx, domain)`) {
		t.Fatal("runPurgeAdminInstanceDomain does not remove Paperclip files before deleting rows")
	}
	for _, want := range []string{
		`s.removeAccountImageObjects(account)`,
		`s.removeAccountLocalImageFiles(account.ID)`,
		`s.removeMediaAttachmentLocalFiles(attachment)`,
		`s.removeCustomEmojiLocalFiles(emoji)`,
		`scheduled_status_id IN (SELECT id FROM scheduled_statuses`,
	} {
		if !functionBodyContains(t, src, "purgeAdminInstanceDomainFiles", want) {
			t.Fatalf("purgeAdminInstanceDomainFiles missing %q", want)
		}
	}
}

func TestAdminInstanceRedirectErrorsUseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("admin_instances.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"destroyAdminInstance", "restartAdminInstanceDelivery", "stopAdminInstanceDelivery"} {
		if !functionBodyContains(t, src, fn, `adminInstanceMessage(s.webLocale(c, user), "errors.database_unavailable", "DATABASE_URL is not set")`) {
			t.Fatalf("%s must use localized database-unavailable flash", fn)
		}
		if functionBodyContains(t, src, fn, `QueryEscape("DATABASE_URL is not set")`) {
			t.Fatalf("%s must not redirect with fixed Go-only database flash", fn)
		}
	}
	if got := adminInstanceMessage("ja", "errors.database_unavailable", "DATABASE_URL is not set"); got == "DATABASE_URL is not set" || !strings.Contains(got, "DATABASE_URL") {
		t.Fatalf("Japanese admin instance database flash did not resolve locale key: %q", got)
	}
}

func TestAdminInstanceFilterHiddenFieldsPreserveRailsKeys(t *testing.T) {
	html := adminInstanceFilterHiddenFields(adminInstanceFilters{Page: "3", Limited: "1", Availability: "unavailable"})
	for _, want := range []string{
		`name="page" value="3"`,
		`name="limited" value="1"`,
		`name="availability" value="unavailable"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("hidden fields missing %q: %s", want, html)
		}
	}
}

func TestAdminInstancesHTMLIncludesRailsCompatibleActions(t *testing.T) {
	html := adminInstancesHTML([]adminInstanceRow{{Instance: models.Instance{Domain: "remote.example"}}}, false, "saved", "", adminInstanceFilters{Page: "2", ByDomain: "remote", Limited: "1", Availability: "unavailable"})
	for _, want := range []string{
		"Federation",
		`id="add-instance-button" href="/admin/domain_blocks/new"`,
		`href="/admin/export_domain_blocks/export.csv"`,
		`name="page" value="2"`,
		`name="by_domain" id="by_domain" value="remote"`,
		`name="limited" value="1"`,
		`name="availability" value="unavailable"`,
		`limited=1`,
		"saved",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("instances html missing %q: %s", want, html)
		}
	}
}

func TestAdminInstanceHTMLIncludesModerationAndDeliveryForms(t *testing.T) {
	html := adminInstanceHTML(adminInstanceRow{
		Instance: models.Instance{Domain: "remote.example"},
		DomainBlock: &models.DomainBlock{
			ID:             4,
			Severity:       domainBlockSeverityValue("silence"),
			PrivateComment: sql.NullString{String: "private", Valid: true},
			PublicComment:  sql.NullString{String: "public", Valid: true},
		},
		Unavailable: &models.UnavailableDomain{Domain: "remote.example", CreatedAt: time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)},
	}, false, "", "")
	for _, want := range []string{
		"remote.example",
		`href="/admin/domain_blocks/4/edit"`,
		`href="/admin/domain_blocks/4" data-method="delete"`,
		`class="table horizontal-table"`,
		`class="availability-indicator"`,
		`href="/admin/instances/remote.example/restart_delivery" data-method="post"`,
		`href="/instance-stats/remote.example"`,
		">Purge</a>",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("instance html missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "Rails Redis worker path") {
		t.Fatalf("instance html still contains stale delivery tracker caveat: %s", html)
	}
}

func TestAdminInstanceHTMLIncludesRailsMetricsDashboardForAuthorizedViewer(t *testing.T) {
	html := adminInstanceHTMLWithOptions(adminInstanceRow{Instance: models.Instance{Domain: "remote.example"}}, false, "", "", adminInstanceHTMLOptions{Locale: "en", ShowDashboard: true})
	for _, want := range []string{
		`class="dashboard"`,
		`data-admin-component="Counter"`,
		`data-admin-component="Dimension"`,
		`instance_accounts`,
		`instance_statuses`,
		`instance_media_attachments`,
		`instance_follows`,
		`instance_followers`,
		`instance_reports`,
		`instance_languages`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("instance dashboard html missing %q: %s", want, html)
		}
	}
}

func TestAdminInstanceDashboardLinksRespectExtraPermissions(t *testing.T) {
	permissions := adminDashboardPermissions{}
	html := adminInstanceDashboardHTMLWithPermissions("remote.example", "en", &permissions)
	for _, forbidden := range []string{"origin=remote", "by_target_domain"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("permission-restricted dashboard contains %q: %s", forbidden, html)
		}
	}
	for _, measure := range []string{"instance_accounts", "instance_reports"} {
		if !strings.Contains(html, measure) {
			t.Fatalf("dashboard counter %q must remain visible without a link", measure)
		}
	}
}
