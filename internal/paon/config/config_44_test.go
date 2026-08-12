package config

import (
	"strings"
	"testing"
	"time"
)

func TestMastodon44RuntimeConfigurationDefaults(t *testing.T) {
	cfg := FromEnv()
	if cfg.FetchRepliesEnabled {
		t.Fatal("FETCH_REPLIES_ENABLED must default to false")
	}
	if cfg.FetchRepliesCooldown != 15*time.Minute || cfg.FetchRepliesInitialWait != 5*time.Minute {
		t.Fatalf("reply fetch durations = %s/%s", cfg.FetchRepliesCooldown, cfg.FetchRepliesInitialWait)
	}
	if cfg.FetchRepliesMaxGlobal != 1000 || cfg.FetchRepliesMaxSingle != 500 || cfg.FetchRepliesMaxPages != 500 {
		t.Fatalf("reply fetch limits = %d/%d/%d", cfg.FetchRepliesMaxGlobal, cfg.FetchRepliesMaxSingle, cfg.FetchRepliesMaxPages)
	}
	if !cfg.ReplicaPreparedStatements || !cfg.ReplicaDatabaseTasks {
		t.Fatalf("replica defaults = prepared:%t tasks:%t", cfg.ReplicaPreparedStatements, cfg.ReplicaDatabaseTasks)
	}
	if cfg.ForceDefaultLocale {
		t.Fatal("FORCE_DEFAULT_LOCALE must default to false in Mastodon 4.4")
	}
}

func TestForceDefaultLocaleIsStrictAndExplicit(t *testing.T) {
	t.Setenv("FORCE_DEFAULT_LOCALE", "true")
	if cfg := FromEnv(); !cfg.ForceDefaultLocale {
		t.Fatal("FORCE_DEFAULT_LOCALE=true was ignored")
	}
	t.Setenv("FORCE_DEFAULT_LOCALE", "yes")
	if err := FromEnv().ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "FORCE_DEFAULT_LOCALE") {
		t.Fatalf("invalid FORCE_DEFAULT_LOCALE was accepted: %v", err)
	}
}

func TestMastodon44ValidationKeepsLegacyConfigLiteralsDisabledByDefault(t *testing.T) {
	if err := (Config{}).validateRuntime44(); err != nil {
		t.Fatalf("all-zero 4.4 extension fields should mean disabled defaults for config literals: %v", err)
	}
}

func TestMeiliPrefixNoLongerFallsBackToRedisNamespace(t *testing.T) {
	t.Setenv("REDIS_NAMESPACE", "tenant")
	t.Setenv("MEILI_PREFIX", "")
	if got := FromEnv().MeiliPrefix; got != "" {
		t.Fatalf("MeiliPrefix = %q, want no REDIS_NAMESPACE fallback", got)
	}
	t.Setenv("MEILI_PREFIX", "search")
	if got := FromEnv().MeiliPrefix; got != "search_" {
		t.Fatalf("explicit MeiliPrefix = %q", got)
	}
}

func TestExtraMediaHostsAreNormalizedAndStrictlyValidated(t *testing.T) {
	t.Setenv("EXTRA_MEDIA_HOSTS", "https://MEDIA.example.test, http://static.example.test:8080 https://media.example.test")
	cfg := FromEnv()
	if got := strings.Join(cfg.ExtraMediaHosts, ","); got != "https://media.example.test,http://static.example.test:8080" {
		t.Fatalf("ExtraMediaHosts = %q", got)
	}

	t.Setenv("EXTRA_MEDIA_HOSTS", "https://good.example.test https://bad.example.test/private")
	err := FromEnv().ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "EXTRA_MEDIA_HOSTS") || !strings.Contains(err.Error(), "without paths") {
		t.Fatalf("ValidateRuntime error = %v", err)
	}

	t.Setenv("EXTRA_MEDIA_HOSTS", "https://bad.example.test:")
	if err := FromEnv().ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "EXTRA_MEDIA_HOSTS") {
		t.Fatalf("trailing-port separator was accepted: %v", err)
	}
}

func TestReplyFetchConfigurationRejectsInvalidValues(t *testing.T) {
	t.Setenv("FETCH_REPLIES_ENABLED", "yes")
	t.Setenv("FETCH_REPLIES_MAX_GLOBAL", "0")
	t.Setenv("FETCH_REPLIES_MAX_SINGLE", "many")
	err := FromEnv().ValidateRuntime()
	if err == nil {
		t.Fatal("invalid reply-fetch configuration was accepted")
	}
	for _, name := range []string{"FETCH_REPLIES_ENABLED", "FETCH_REPLIES_MAX_GLOBAL", "FETCH_REPLIES_MAX_SINGLE"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("ValidateRuntime error %q missing %s", err, name)
		}
	}
}

func TestBulkSMTPIsOptionalPairedAndMapsToDedicatedTransport(t *testing.T) {
	t.Setenv("LOCAL_DOMAIN", "social.example.test")
	t.Setenv("BULK_SMTP_SERVER", "bulk.example.test")
	t.Setenv("BULK_SMTP_PORT", "2525")
	t.Setenv("BULK_SMTP_LOGIN", "bulk-user")
	t.Setenv("BULK_SMTP_PASSWORD", "bulk-password")
	cfg := FromEnv()
	bulk := cfg.BulkMailSMTPConfig()
	if bulk.SMTPServer != "bulk.example.test" || bulk.SMTPPort != "2525" || bulk.SMTPLogin != "bulk-user" || bulk.SMTPDomain != "social.example.test" {
		t.Fatalf("bulk transport = %#v", bulk)
	}

	t.Setenv("BULK_SMTP_PASSWORD", "")
	err := FromEnv().ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "BULK_SMTP_LOGIN and BULK_SMTP_PASSWORD") {
		t.Fatalf("unpaired bulk credentials error = %v", err)
	}
}

func TestBulkSMTPVariablesRequireActivationServer(t *testing.T) {
	t.Setenv("BULK_SMTP_SERVER", "")
	t.Setenv("BULK_SMTP_PORT", "2525")
	err := FromEnv().ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "BULK_SMTP_PORT requires BULK_SMTP_SERVER") {
		t.Fatalf("partial bulk SMTP error = %v", err)
	}
}

func TestPrometheusCompatibilityMapsOnlyToOTelMetrics(t *testing.T) {
	t.Setenv("MASTODON_PROMETHEUS_EXPORTER_ENABLED", "true")
	cfg := FromEnv()
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "requires OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Fatalf("missing mapped OTLP metrics error = %v", err)
	}

	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "https://collector.example.test/v1/metrics")
	cfg = FromEnv()
	if !cfg.PrometheusExporterEnabled || !cfg.OpenTelemetryMetricsEnabled {
		t.Fatalf("Prometheus/OTel mapping = %#v", cfg)
	}
}

func TestReplicaPreparedStatementsOverridePrimarySetting(t *testing.T) {
	t.Setenv("PREPARED_STATEMENTS", "false")
	if cfg := FromEnv(); cfg.DatabasePreparedStatements || cfg.ReplicaPreparedStatements {
		t.Fatalf("fallback prepared statements = primary:%t replica:%t", cfg.DatabasePreparedStatements, cfg.ReplicaPreparedStatements)
	}
	t.Setenv("REPLICA_PREPARED_STATEMENTS", "true")
	if cfg := FromEnv(); cfg.DatabasePreparedStatements || !cfg.ReplicaPreparedStatements {
		t.Fatalf("override prepared statements = primary:%t replica:%t", cfg.DatabasePreparedStatements, cfg.ReplicaPreparedStatements)
	}
}
