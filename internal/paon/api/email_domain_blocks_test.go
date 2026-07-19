package api

import (
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestEmailDomainBlockVariantsMatchRailsParentDomainLookup(t *testing.T) {
	got := emailDomainBlockVariants("User@Mail.Sub.Example.COM")
	want := []string{"mail.sub.example.com", "sub.example.com", "example.com", "com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("variants = %#v, want %#v", got, want)
	}
}

func TestEmailDomainBlockVariantsRejectInvalidEmailDomains(t *testing.T) {
	for _, email := range []string{"bad", "user@", "user@example.com@evil.test", "user@bad domain"} {
		if got := emailDomainBlockVariants(email); got != nil {
			t.Fatalf("variants for %q = %#v, want nil", email, got)
		}
	}
}

func TestEmailDomainBlockVariantsForMXDomains(t *testing.T) {
	got := emailDomainBlockVariantsForDomains([]string{"mx.mail.example.com.", "bad domain", "mx.mail.example.com"})
	want := []string{"mx.mail.example.com", "mail.example.com", "example.com", "com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MX variants = %#v, want %#v", got, want)
	}
}

func TestEnsureEmailDomainAllowedAppliesRailsConfigWithoutDatabase(t *testing.T) {
	t.Setenv("RAILS_ENV", "development")
	t.Setenv("EMAIL_DOMAIN_DENYLIST", "blocked.example")
	err := (&Server{}).ensureEmailDomainAllowed(nil, "user@blocked.example", "203.0.113.10", true, false)
	if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != 422 || !strings.Contains(apiErr.message, "Email domain is blocked") {
		t.Fatalf("denylist error = %#v", err)
	}

	t.Setenv("EMAIL_DOMAIN_DENYLIST", "")
	t.Setenv("EMAIL_DOMAIN_ALLOWLIST", "allowed.example")
	err = (&Server{}).ensureEmailDomainAllowed(nil, "user@other.example", "203.0.113.10", true, false)
	if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != 422 || !strings.Contains(apiErr.message, "Email domain is blocked") {
		t.Fatalf("allowlist error = %#v", err)
	}

	err = (&Server{}).ensureEmailDomainAllowed(nil, "user@allowed.example", "203.0.113.10", true, false)
	if err != nil {
		t.Fatalf("allowlisted domain error = %v", err)
	}
}

func withEmailDomainResolvers(t *testing.T, mx func(string) ([]*net.MX, error), addresses func(string) ([]net.IP, error)) {
	t.Helper()
	oldMX := lookupEmailDomainMXRecords
	oldAddresses := lookupEmailDomainAddresses
	lookupEmailDomainMXRecords = mx
	lookupEmailDomainAddresses = addresses
	t.Cleanup(func() {
		lookupEmailDomainMXRecords = oldMX
		lookupEmailDomainAddresses = oldAddresses
	})
}

func TestEmailDomainBlockHistoryRedisKeysMatchRailsHistory(t *testing.T) {
	at := time.Date(2026, 6, 19, 20, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	uses, accounts := emailDomainBlockHistoryRedisKeys(config.Config{RedisNamespace: "mastodon:"}, 42, at)
	if uses != "mastodon:activity:email_domain_blocks:42:1781827200" {
		t.Fatalf("uses key = %q", uses)
	}
	if accounts != "mastodon:activity:email_domain_blocks:42:1781827200:accounts" {
		t.Fatalf("accounts key = %q", accounts)
	}
}
