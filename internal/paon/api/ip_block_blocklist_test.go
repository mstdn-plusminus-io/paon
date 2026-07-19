package api

import (
	"context"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestNoAccessIPBlockedForBlocksUsesRailsSeverityAndCIDR(t *testing.T) {
	blocks := []models.IPBlock{
		{IP: "192.0.2.0/24", Severity: 9999},
		{IP: "2001:db8::/64", Severity: 9999},
		{IP: "198.51.100.0/24", Severity: 5500},
	}
	if !noAccessIPBlockedForBlocks("192.0.2.12", blocks) {
		t.Fatal("IPv4 no_access block was not applied")
	}
	if !noAccessIPBlockedForBlocks("2001:db8::1", blocks) {
		t.Fatal("IPv6 no_access block was not applied")
	}
	if noAccessIPBlockedForBlocks("198.51.100.12", blocks) {
		t.Fatal("sign_up_block must not trigger Rack::Attack no_access blocklist")
	}
	if noAccessIPBlockedForBlocks("203.0.113.12", blocks) {
		t.Fatal("unmatched IP was blocked")
	}
}

func TestInvalidateIPBlockCacheClearsInProcessNoAccessCache(t *testing.T) {
	s := &Server{
		cfg:              config.Config{RedisNamespace: "mastodon:"},
		noAccessIPBlocks: []models.IPBlock{{IP: "192.0.2.0/24", Severity: 9999}},
		noAccessIPCached: time.Now().UTC(),
	}
	s.invalidateIPBlockCache(context.Background())
	if len(s.noAccessIPBlocks) != 0 || !s.noAccessIPCached.IsZero() {
		t.Fatalf("in-process IP block cache was not cleared: %#v %s", s.noAccessIPBlocks, s.noAccessIPCached)
	}
}
