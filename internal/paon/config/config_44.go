package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var experimentalFeatureNamePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

func strictBoolValueFromEnv(name string, fallback bool) bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return strings.TrimSpace(raw) == "true"
}

func runtimeConfigurationErrorsFromEnv() []error {
	var problems []error
	for _, name := range []string{
		"FETCH_REPLIES_ENABLED",
		"FORCE_DEFAULT_LOCALE",
		"REPLICA_PREPARED_STATEMENTS",
		"REPLICA_DB_TASKS",
		"MASTODON_PROMETHEUS_EXPORTER_ENABLED",
		"MASTODON_PROMETHEUS_EXPORTER_LOCAL",
		"MASTODON_PROMETHEUS_EXPORTER_WEB_DETAILED_METRICS",
		"MASTODON_PROMETHEUS_EXPORTER_SIDEKIQ_DETAILED_METRICS",
		"BULK_SMTP_ENABLE_STARTTLS_AUTO",
		"BULK_SMTP_TLS",
		"BULK_SMTP_SSL",
	} {
		if raw, ok := os.LookupEnv(name); ok && strings.TrimSpace(raw) != "true" && strings.TrimSpace(raw) != "false" {
			problems = append(problems, fmt.Errorf("%s must be true or false, got %q", name, raw))
		}
	}
	for _, name := range []string{
		"FETCH_REPLIES_COOLDOWN_MINUTES",
		"FETCH_REPLIES_INITIAL_WAIT_MINUTES",
		"FETCH_REPLIES_MAX_GLOBAL",
		"FETCH_REPLIES_MAX_SINGLE",
		"FETCH_REPLIES_MAX_PAGES",
	} {
		if raw, ok := os.LookupEnv(name); ok {
			value, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || value <= 0 {
				problems = append(problems, fmt.Errorf("%s must be a positive integer, got %q", name, raw))
			}
		}
	}
	if raw, ok := os.LookupEnv("BULK_SMTP_ENABLE_STARTTLS"); ok {
		switch strings.TrimSpace(raw) {
		case "", "always", "never", "auto":
		default:
			problems = append(problems, fmt.Errorf("BULK_SMTP_ENABLE_STARTTLS must be always, never, or auto, got %q", raw))
		}
	}

	bulkServer := railsPresenceEnv("BULK_SMTP_SERVER")
	if bulkServer == "" {
		for _, name := range []string{
			"BULK_SMTP_PORT", "BULK_SMTP_LOGIN", "BULK_SMTP_PASSWORD", "BULK_SMTP_DOMAIN",
			"BULK_SMTP_AUTH_METHOD", "BULK_SMTP_CA_FILE", "BULK_SMTP_OPENSSL_VERIFY_MODE",
			"BULK_SMTP_ENABLE_STARTTLS", "BULK_SMTP_ENABLE_STARTTLS_AUTO", "BULK_SMTP_TLS", "BULK_SMTP_SSL",
		} {
			if _, ok := os.LookupEnv(name); ok {
				problems = append(problems, fmt.Errorf("%s requires BULK_SMTP_SERVER", name))
			}
		}
	}

	if strictBoolValueFromEnv("MASTODON_PROMETHEUS_EXPORTER_LOCAL", false) {
		problems = append(problems, errors.New("MASTODON_PROMETHEUS_EXPORTER_LOCAL is not supported by Paon; export the mapped metrics through OTLP"))
	}
	for _, name := range []string{"MASTODON_PROMETHEUS_EXPORTER_HOST", "MASTODON_PROMETHEUS_EXPORTER_PORT"} {
		if _, ok := os.LookupEnv(name); ok {
			problems = append(problems, fmt.Errorf("%s is not supported by Paon; configure the existing OTLP exporter instead", name))
		}
	}

	for _, raw := range splitExtraMediaHosts(os.Getenv("EXTRA_MEDIA_HOSTS")) {
		if _, err := normalizeExtraMediaHost(raw); err != nil {
			problems = append(problems, err)
		}
	}
	for _, feature := range experimentalFeaturesFromEnv() {
		if !experimentalFeatureNamePattern.MatchString(feature) {
			problems = append(problems, fmt.Errorf("EXPERIMENTAL_FEATURES contains invalid feature name %q", feature))
		}
	}
	return problems
}

func splitExtraMediaHosts(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

func extraMediaHostsFromEnv() []string {
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, raw := range splitExtraMediaHosts(os.Getenv("EXTRA_MEDIA_HOSTS")) {
		host, err := normalizeExtraMediaHost(raw)
		if err != nil {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	return out
}

func normalizeExtraMediaHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("EXTRA_MEDIA_HOSTS contains an invalid URL %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("EXTRA_MEDIA_HOSTS must contain absolute http or https origins without credentials, query, or fragment, got %q", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("EXTRA_MEDIA_HOSTS must contain origins without paths, got %q", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (c Config) validateRuntime44() error {
	problems := append([]error(nil), c.runtimeConfigurationErrors...)
	if strings.TrimSpace(c.RedisNamespace) != "" {
		problems = append(problems, errors.New("REDIS_NAMESPACE is no longer supported in Mastodon 4.4; stop all Paon processes, run `paon-admin redis namespace-cutover --prefix NAME --dry-run` and then `--confirm`, and remove REDIS_NAMESPACE"))
	}
	// Config literals used by focused tests and embedders pre-date these 4.4
	// fields. Treat the all-zero shape as the disabled default; FromEnv always
	// populates every value, so a configured zero or negative value is still a
	// startup error through runtimeConfigurationErrors.
	if c.FetchRepliesEnabled || c.FetchRepliesCooldown != 0 || c.FetchRepliesInitialWait != 0 || c.FetchRepliesMaxGlobal != 0 || c.FetchRepliesMaxSingle != 0 || c.FetchRepliesMaxPages != 0 {
		if c.FetchRepliesCooldown <= 0 {
			problems = append(problems, errors.New("FETCH_REPLIES_COOLDOWN_MINUTES must be greater than 0"))
		}
		if c.FetchRepliesInitialWait <= 0 {
			problems = append(problems, errors.New("FETCH_REPLIES_INITIAL_WAIT_MINUTES must be greater than 0"))
		}
		if c.FetchRepliesMaxGlobal <= 0 {
			problems = append(problems, errors.New("FETCH_REPLIES_MAX_GLOBAL must be greater than 0"))
		}
		if c.FetchRepliesMaxSingle <= 0 {
			problems = append(problems, errors.New("FETCH_REPLIES_MAX_SINGLE must be greater than 0"))
		}
		if c.FetchRepliesMaxPages <= 0 {
			problems = append(problems, errors.New("FETCH_REPLIES_MAX_PAGES must be greater than 0"))
		}
	}
	for _, host := range c.ExtraMediaHosts {
		if _, err := normalizeExtraMediaHost(host); err != nil {
			problems = append(problems, err)
		}
	}
	for _, feature := range c.ExperimentalFeatures {
		feature = strings.ToLower(strings.TrimSpace(feature))
		if feature == "" || !experimentalFeatureNamePattern.MatchString(feature) {
			problems = append(problems, fmt.Errorf("EXPERIMENTAL_FEATURES contains invalid feature name %q", feature))
		}
	}
	if strings.TrimSpace(c.BulkSMTPServer) != "" {
		if strings.TrimSpace(c.BulkSMTPPort) != "" {
			if err := validatePort("BULK_SMTP_PORT", c.BulkSMTPPort); err != nil {
				problems = append(problems, err)
			}
		}
		if (strings.TrimSpace(c.BulkSMTPLogin) == "") != (strings.TrimSpace(c.BulkSMTPPassword) == "") {
			problems = append(problems, errors.New("BULK_SMTP_LOGIN and BULK_SMTP_PASSWORD must be configured together"))
		}
		switch strings.ToLower(strings.TrimSpace(c.BulkSMTPAuthMethod)) {
		case "", "plain", "login", "cram_md5", "none":
		default:
			problems = append(problems, fmt.Errorf("BULK_SMTP_AUTH_METHOD must be one of plain, login, cram_md5, or none, got %q", c.BulkSMTPAuthMethod))
		}
		if c.BulkSMTPTLS && c.BulkSMTPStartTLS {
			problems = append(problems, errors.New("BULK_SMTP_TLS/BULK_SMTP_SSL cannot be combined with BULK_SMTP_ENABLE_STARTTLS"))
		}
		if c.BulkSMTPStartTLSRequired && !c.BulkSMTPStartTLS {
			problems = append(problems, errors.New("BULK_SMTP_ENABLE_STARTTLS=always must enable STARTTLS"))
		}
	}
	if c.PrometheusExporterEnabled && !c.OpenTelemetryMetricsEnabled {
		problems = append(problems, errors.New("MASTODON_PROMETHEUS_EXPORTER_ENABLED=true maps to OpenTelemetry and requires OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"))
	}
	if (c.PrometheusWebDetailedMetrics || c.PrometheusSidekiqDetailedMetrics) && !c.PrometheusExporterEnabled {
		problems = append(problems, errors.New("MASTODON_PROMETHEUS_EXPORTER_*_DETAILED_METRICS requires MASTODON_PROMETHEUS_EXPORTER_ENABLED=true"))
	}
	if len(c.SourceCommit) > 200 || strings.IndexFunc(c.SourceCommit, unicode.IsControl) >= 0 {
		problems = append(problems, errors.New("SOURCE_COMMIT must be at most 200 characters without control characters"))
	}
	return errors.Join(problems...)
}

// BulkMailSMTPConfig selects the optional Mastodon 4.4 bulk SMTP transport.
// The caller remains responsible for limiting this to announcement and terms
// of service distribution mail.
func (c Config) BulkMailSMTPConfig() Config {
	if strings.TrimSpace(c.BulkSMTPServer) == "" {
		return c
	}
	out := c
	out.SMTPServer = c.BulkSMTPServer
	out.SMTPPort = c.BulkSMTPPort
	out.SMTPLogin = c.BulkSMTPLogin
	out.SMTPPassword = c.BulkSMTPPassword
	out.SMTPDomain = c.BulkSMTPDomain
	out.SMTPDomainSet = c.BulkSMTPDomainSet
	out.SMTPAuthMethod = c.BulkSMTPAuthMethod
	out.SMTPCAFile = c.BulkSMTPCAFile
	out.SMTPOpenSSLVerifyMode = c.BulkSMTPOpenSSLVerifyMode
	out.SMTPTLS = c.BulkSMTPTLS
	out.SMTPStartTLS = c.BulkSMTPStartTLS
	out.SMTPStartTLSRequired = c.BulkSMTPStartTLSRequired
	return out
}
