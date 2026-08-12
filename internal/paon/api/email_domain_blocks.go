package api

import (
	"context"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const emailDomainBlockHistoryPrefix = "activity:email_domain_blocks"
const emailDomainBlockHistoryTTL = 14 * 24 * time.Hour

var (
	lookupEmailDomainMXRecords = net.LookupMX
	lookupEmailDomainAddresses = net.LookupIP
)

func (s *Server) ensureEmailDomainAllowed(ctx context.Context, email string, attemptIP string, runProviderBlock bool, skipProviderBlock bool) error {
	variants := emailDomainBlockVariants(email)
	if len(variants) == 0 {
		if runProviderBlock && !skipProviderBlock {
			return apiHTTPError{status: 422, message: "Validation failed: Email domain is blocked"}
		}
		return apiHTTPError{status: 422, message: "Validation failed: Email is invalid"}
	}
	if runProviderBlock && !skipProviderBlock {
		if emailDomainDisallowedThroughConfiguration(email) || emailDomainNotAllowedThroughConfiguration(email) {
			return apiHTTPError{status: 422, message: "Validation failed: Email domain is blocked"}
		}
		if s != nil && s.db != nil {
			blocks, err := s.emailDomainBlocksForVariants(ctx, variants)
			if err != nil {
				return err
			}
			hardBlocks := hardEmailDomainBlocks(blocks)
			if len(hardBlocks) > 0 {
				s.recordEmailDomainBlockHistory(ctx, hardBlocks, attemptIP, time.Now().UTC())
				return apiHTTPError{status: 422, message: "Validation failed: Email domain is blocked"}
			}
		}
		if s != nil && s.db != nil {
			blocked, err := s.canonicalEmailBlocked(ctx, email)
			if err != nil {
				return err
			}
			if blocked {
				return apiHTTPError{status: 422, message: "Validation failed: Username or e-mail has already been taken"}
			}
		}
	}
	if !emailDomainDNSValidationEnabled() {
		return nil
	}
	return s.ensureEmailDomainMXAllowed(ctx, variants[0], attemptIP)
}

func (s *Server) canonicalEmailBlocked(ctx context.Context, email string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.CanonicalEmailBlock{}).Where("canonical_email_hash = ?", canonicalEmailHash(email)).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Server) shouldRunEmailDomainProviderBlockForUser(user models.User) bool {
	return s != nil && s.cfg.EmailDomainListsApplyAfterConfirm || !user.ConfirmedAt.Valid
}

func shouldRunEmailDomainProviderBlockForUser(user models.User) bool {
	return (&Server{cfg: config.FromEnv()}).shouldRunEmailDomainProviderBlockForUser(user)
}

func (s *Server) ensureEmailDomainMXAllowed(ctx context.Context, domain string, attemptIP string) error {
	domain = normalizeDomain(strings.TrimSuffix(domain, "."))
	if domain == "" || strings.Contains(domain, "..") {
		return apiHTTPError{status: 422, message: "Validation failed: Email is invalid"}
	}
	if emailDomainOnMXAllowlist(domain) {
		return nil
	}
	resolvedIPs, resolvedDomains := resolveEmailDomainMX(domain)
	if len(resolvedIPs) == 0 {
		return apiHTTPError{status: 422, message: "Validation failed: Email is unreachable"}
	}
	if s == nil || s.db == nil || len(resolvedDomains) == 0 {
		return nil
	}
	variants := emailDomainBlockVariantsForDomains(resolvedDomains)
	if len(variants) == 0 {
		return nil
	}
	blocks, err := s.emailDomainBlocksForVariants(ctx, variants)
	if err != nil {
		return err
	}
	hardBlocks := hardEmailDomainBlocks(blocks)
	if len(hardBlocks) == 0 {
		return nil
	}
	s.recordEmailDomainBlockHistory(ctx, hardBlocks, attemptIP, time.Now().UTC())
	return apiHTTPError{status: 422, message: "Validation failed: Email domain is blocked"}
}

func hardEmailDomainBlocks(blocks []models.EmailDomainBlock) []models.EmailDomainBlock {
	out := make([]models.EmailDomainBlock, 0, len(blocks))
	for _, block := range blocks {
		if !block.AllowWithApproval {
			out = append(out, block)
		}
	}
	return out
}

func (s *Server) emailSignUpRequiresApproval(ctx context.Context, email string, attemptIP string) (bool, error) {
	variants := emailDomainBlockVariants(email)
	if len(variants) == 0 || s == nil || s.db == nil {
		return false, nil
	}
	allVariants := append([]string{}, variants...)
	if emailDomainDNSValidationEnabled() {
		_, resolvedDomains := resolveEmailDomainMX(variants[0])
		allVariants = append(allVariants, emailDomainBlockVariantsForDomains(resolvedDomains)...)
	}
	blocks, err := s.emailDomainBlocksForVariants(ctx, uniqueNonEmptyStrings(allVariants))
	if err != nil {
		return false, err
	}
	approvalBlocks := make([]models.EmailDomainBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.AllowWithApproval {
			approvalBlocks = append(approvalBlocks, block)
		}
	}
	if len(approvalBlocks) == 0 {
		return false, nil
	}
	s.recordEmailDomainBlockHistory(ctx, approvalBlocks, attemptIP, time.Now().UTC())
	return true, nil
}

func (s *Server) emailDomainBlocksForVariants(ctx context.Context, variants []string) ([]models.EmailDomainBlock, error) {
	if s == nil || s.db == nil || len(variants) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var blocks []models.EmailDomainBlock
	if err := s.db.WithContext(ctx).Where("domain IN ?", variants).Order("char_length(domain) DESC").Find(&blocks).Error; err != nil {
		return nil, err
	}
	return blocks, nil
}

func emailDomainBlockVariants(email string) []string {
	_, domain, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || strings.Contains(domain, "@") {
		return nil
	}
	domain = normalizeDomain(domain)
	if domain == "" {
		return nil
	}
	parts := strings.Split(domain, ".")
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, strings.Join(parts[i:], "."))
	}
	return out
}

func emailDomainBlockVariantsForDomains(domains []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = normalizeDomain(strings.TrimSuffix(domain, "."))
		if domain == "" {
			continue
		}
		parts := strings.Split(domain, ".")
		for i := range parts {
			variant := strings.Join(parts[i:], ".")
			if variant == "" {
				continue
			}
			if _, ok := seen[variant]; ok {
				continue
			}
			seen[variant] = struct{}{}
			out = append(out, variant)
		}
	}
	return out
}

func emailDomainDisallowedThroughConfiguration(email string) bool {
	raw := railsEnvOrLegacyEnv("EMAIL_DOMAIN_DENYLIST", "EMAIL_DOMAIN_BLACKLIST")
	if strings.TrimSpace(raw) == "" {
		return false
	}
	return emailDomainConfigurationRegexpMatch(email, raw, false)
}

func emailDomainNotAllowedThroughConfiguration(email string) bool {
	raw := railsEnvOrLegacyEnv("EMAIL_DOMAIN_ALLOWLIST", "EMAIL_DOMAIN_WHITELIST")
	if strings.TrimSpace(raw) == "" {
		return false
	}
	return !emailDomainConfigurationRegexpMatch(email, raw, true)
}

func railsEnvOrLegacyEnv(name string, legacyName string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return os.Getenv(legacyName)
}

func emailDomainConfigurationRegexpMatch(email string, domains string, anchored bool) bool {
	escapedDots := strings.ReplaceAll(strings.TrimSpace(domains), ".", `\.`)
	suffix := ""
	if anchored {
		suffix = "$"
	}
	re, err := regexp.Compile(`(?i)@(.+\.)?(` + escapedDots + `)` + suffix)
	if err != nil {
		return false
	}
	return re.MatchString(strings.TrimSpace(email))
}

func emailDomainOnMXAllowlist(domain string) bool {
	raw := railsEnvOrLegacyEnv("EMAIL_DOMAIN_ALLOWLIST", "EMAIL_DOMAIN_WHITELIST")
	return strings.TrimSpace(raw) != "" && strings.Contains(raw, domain)
}

func emailDomainDNSValidationEnabled() bool {
	env := railsEnvNameFromProcess()
	return env != "test" && env != "development"
}

func resolveEmailDomainMX(domain string) ([]string, []string) {
	records, err := lookupEmailDomainMXRecords(domain)
	if err != nil {
		records = nil
	}
	hostnames := []string{domain}
	resolvedDomains := make([]string, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		host := normalizeDomain(strings.TrimSuffix(record.Host, "."))
		if host == "" {
			continue
		}
		hostnames = append(hostnames, host)
		resolvedDomains = append(resolvedDomains, host)
	}
	seen := map[string]struct{}{}
	ips := make([]string, 0, len(hostnames))
	for _, hostname := range hostnames {
		hostname = normalizeDomain(strings.TrimSuffix(hostname, "."))
		if hostname == "" {
			continue
		}
		addresses, err := lookupEmailDomainAddresses(hostname)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip := address.String()
			if ip == "" {
				continue
			}
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			ips = append(ips, ip)
		}
	}
	return ips, resolvedDomains
}

func emailDomainBlockHistoryRedisKeys(cfg config.Config, blockID int64, at time.Time) (string, string) {
	key := cfg.RedisNamespace + emailDomainBlockHistoryPrefix + ":" + strconv.FormatInt(blockID, 10) + ":" + strconv.FormatInt(dayStart(at).Unix(), 10)
	return key, key + ":accounts"
}

func (s *Server) recordEmailDomainBlockHistory(ctx context.Context, blocks []models.EmailDomainBlock, attemptIP string, at time.Time) {
	attemptIP = strings.TrimSpace(attemptIP)
	if s == nil || attemptIP == "" || len(blocks) == 0 {
		return
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	ttl := strconv.FormatInt(int64(emailDomainBlockHistoryTTL/time.Second), 10)
	for _, block := range blocks {
		usesKey, accountsKey := emailDomainBlockHistoryRedisKeys(s.cfg, block.ID, at)
		_, _ = s.redisCommand(cacheCtx, "INCRBY", usesKey, "1")
		_, _ = s.redisCommand(cacheCtx, "PFADD", accountsKey, attemptIP)
		_, _ = s.redisCommand(cacheCtx, "EXPIRE", usesKey, ttl)
		_, _ = s.redisCommand(cacheCtx, "EXPIRE", accountsKey, ttl)
	}
}
