package api

import (
	"context"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/net/publicsuffix"
)

func (s *Server) materializeDomainControl(ctx context.Context, domain string) error {
	domain = normalizeDomain(domain)
	if domain == "" {
		return nil
	}
	if s != nil && s.db != nil {
		var count int64
		if err := s.db.Model(&models.Instance{}).Where("domain = ?", domain).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := s.refreshInstancesMaterializedView(); err != nil {
				return err
			}
		}
		s.meiliIndexInstanceBestEffort(ctx, domain)
	}
	if s != nil {
		s.countUniqueSubdomain(ctx, domain)
	}
	return nil
}

func (s *Server) materializeDomainControlMutation(ctx context.Context, domain string) error {
	if s != nil {
		s.invalidateUnavailableDomainsCache(ctx)
	}
	return s.materializeDomainControl(ctx, domain)
}

func (s *Server) refreshDomainControlMutation(ctx context.Context, domain string) error {
	if s == nil {
		return nil
	}
	s.invalidateUnavailableDomainsCache(ctx)
	domain = normalizeDomain(domain)
	if s.db != nil {
		if err := s.refreshInstancesMaterializedView(); err != nil {
			return err
		}
		if domain != "" {
			s.meiliIndexInstanceBestEffort(ctx, domain)
		}
	}
	return nil
}

func (s *Server) countUniqueSubdomain(ctx context.Context, domain string) {
	base := registrableDomain(domain)
	if s == nil || base == "" {
		return
	}
	cfg := redisConfig(s.cfg)
	key := cfg.prefix + "unique_subdomains_for:" + base
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, _ = s.redisCommand(cacheCtx, "PFADD", key, domain)
	_, _ = s.redisCommand(cacheCtx, "EXPIRE", key, "60")
}

func registrableDomain(domain string) string {
	domain = normalizeDomain(domain)
	if domain == "" {
		return ""
	}
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return ""
	}
	suffix := icannPublicSuffix(domain, parts)
	if suffix == "" {
		return ""
	}
	suffixLabels := len(strings.Split(suffix, "."))
	if len(parts) <= suffixLabels {
		return ""
	}
	return strings.Join(parts[len(parts)-suffixLabels-1:], ".")
}

func icannPublicSuffix(domain string, parts []string) string {
	if suffix, icann := publicsuffix.PublicSuffix(domain); icann {
		return suffix
	}
	for i := 1; i < len(parts); i++ {
		candidate := strings.Join(parts[i:], ".")
		if suffix, icann := publicsuffix.PublicSuffix(candidate); icann && suffix == candidate {
			return suffix
		}
	}
	return ""
}
