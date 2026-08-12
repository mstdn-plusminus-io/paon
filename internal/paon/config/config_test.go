package config

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func unsetEnvForTest(t *testing.T, name string) {
	t.Helper()
	oldValue, hadValue := os.LookupEnv(name)
	_ = os.Unsetenv(name)
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(name, oldValue)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func TestRailsLogLevelDefaultsToInfo(t *testing.T) {
	unsetEnvForTest(t, "RAILS_LOG_LEVEL")
	cfg := FromEnv()
	if cfg.RailsLogLevel != "info" {
		t.Fatalf("default RAILS_LOG_LEVEL = %q, want info", cfg.RailsLogLevel)
	}
	if !cfg.ShouldLog("info") {
		t.Fatal("default log level suppressed INFO logs")
	}
}

func TestFromEnvUpdateCheckURLMatchesRailsDefaultAndDisableSemantics(t *testing.T) {
	t.Setenv("UPDATE_CHECK_URL", "")
	cfg := FromEnv()
	if cfg.UpdateCheckURL != "" {
		t.Fatalf("explicit empty UPDATE_CHECK_URL should disable checks, got %q", cfg.UpdateCheckURL)
	}

	os.Unsetenv("UPDATE_CHECK_URL")
	cfg = FromEnv()
	if cfg.UpdateCheckURL != "https://join.plusminus.io/api/update-check" {
		t.Fatalf("default UpdateCheckURL = %q", cfg.UpdateCheckURL)
	}

	t.Setenv("UPDATE_CHECK_URL", "https://updates.example.test/check")
	cfg = FromEnv()
	if cfg.UpdateCheckURL != "https://updates.example.test/check" {
		t.Fatalf("custom UpdateCheckURL = %q", cfg.UpdateCheckURL)
	}

	t.Setenv("UPDATE_CHECK_URL", "   ")
	cfg = FromEnv()
	if cfg.UpdateCheckURL != "   " {
		t.Fatalf("explicit whitespace UPDATE_CHECK_URL should match Rails raw ENV.fetch/check_enabled semantics, got %q", cfg.UpdateCheckURL)
	}
}

func railsVersionModuleDefault(t *testing.T, src string) string {
	t.Helper()
	values := map[string]string{}
	for _, name := range []string{"major", "minor", "patch"} {
		match := regexp.MustCompile(`(?ms)def ` + name + `\s+(\d+)\s+end`).FindStringSubmatch(src)
		if len(match) != 2 {
			t.Fatalf("Rails version module missing %s:\n%s", name, src)
		}
		values[name] = match[1]
	}
	return values["major"] + "." + values["minor"] + "." + values["patch"]
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestFromEnvLoadsRailsSMTPStartTLSModes(t *testing.T) {
	t.Setenv("SMTP_ENABLE_STARTTLS", "always")
	cfg := FromEnv()
	if !cfg.SMTPStartTLS || !cfg.SMTPStartTLSRequired {
		t.Fatalf("always STARTTLS config = %#v", cfg)
	}

	t.Setenv("SMTP_ENABLE_STARTTLS", "never")
	cfg = FromEnv()
	if cfg.SMTPStartTLS || cfg.SMTPStartTLSRequired {
		t.Fatalf("never STARTTLS config = %#v", cfg)
	}

	t.Setenv("SMTP_ENABLE_STARTTLS", "auto")
	cfg = FromEnv()
	if !cfg.SMTPStartTLS || cfg.SMTPStartTLSRequired {
		t.Fatalf("auto STARTTLS config = %#v", cfg)
	}

	t.Setenv("SMTP_ENABLE_STARTTLS", "")
	t.Setenv("SMTP_ENABLE_STARTTLS_AUTO", "false")
	cfg = FromEnv()
	if cfg.SMTPStartTLS || cfg.SMTPStartTLSRequired {
		t.Fatalf("legacy false STARTTLS config = %#v", cfg)
	}
}

func TestFromEnvStatusLengthLimitMatchesRailsToI(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 0},
		{"bad", 0},
		{"12bad", 12},
		{"  +9px", 9},
		{"-1", -1},
	} {
		t.Setenv("STATUS_LENGTH_LIMIT", tc.raw)
		cfg := FromEnv()
		if cfg.StatusMaxChars != tc.want || !cfg.StatusMaxCharsSet {
			t.Fatalf("STATUS_LENGTH_LIMIT=%q parsed as %d set=%v, want %d set=true", tc.raw, cfg.StatusMaxChars, cfg.StatusMaxCharsSet, tc.want)
		}
		if err := cfg.ValidateRuntime(); err != nil && strings.Contains(err.Error(), "STATUS_LENGTH_LIMIT") {
			t.Fatalf("explicit Rails-style STATUS_LENGTH_LIMIT=%q should not be rejected: %v", tc.raw, err)
		}
	}
}

func TestFromEnvPrefersAsynqConcurrencyOverLegacySidekiqConcurrency(t *testing.T) {
	t.Setenv("SIDEKIQ_CONCURRENCY", "7")
	unsetEnvForTest(t, "ASYNQ_CONCURRENCY")

	if got := FromEnv().SidekiqConcurrency; got != 7 {
		t.Fatalf("legacy SIDEKIQ_CONCURRENCY = %d, want 7", got)
	}

	t.Setenv("ASYNQ_CONCURRENCY", "11")
	if got := FromEnv().SidekiqConcurrency; got != 11 {
		t.Fatalf("ASYNQ_CONCURRENCY override = %d, want 11", got)
	}
}

func TestFromEnvParsesAsynqQueueSelection(t *testing.T) {
	unsetEnvForTest(t, "ASYNQ_QUEUES")
	if got := FromEnv().AsynqQueues; got != nil {
		t.Fatalf("unset ASYNQ_QUEUES = %#v, want nil for all queues", got)
	}

	t.Setenv("ASYNQ_QUEUES", " push, pull, PUSH, ,mailers ")
	want := []string{"push", "pull", "mailers"}
	if got := FromEnv().AsynqQueues; !reflect.DeepEqual(got, want) {
		t.Fatalf("ASYNQ_QUEUES parsed as %#v, want %#v", got, want)
	}

	t.Setenv("ASYNQ_QUEUES", " ")
	if got := FromEnv().AsynqQueues; got != nil {
		t.Fatalf("empty ASYNQ_QUEUES = %#v, want nil for all queues", got)
	}
}

func TestValidateRuntimeRejectsUnsupportedAsynqQueue(t *testing.T) {
	cfg := FromEnv()
	cfg.AsynqQueues = []string{"push", "unknown"}
	err := cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), `ASYNQ_QUEUES contains unsupported queue "unknown"`) {
		t.Fatalf("ValidateRuntime error = %v", err)
	}
}

func TestFromEnvFollowLimitsMatchRailsToIAndToF(t *testing.T) {
	cases := []struct {
		name          string
		thresholdRaw  string
		ratioRaw      string
		wantThreshold int
		wantRatio     float64
	}{
		{name: "empty", thresholdRaw: "", ratioRaw: "", wantThreshold: 0, wantRatio: 0},
		{name: "invalid", thresholdRaw: "bad", ratioRaw: "bad", wantThreshold: 0, wantRatio: 0},
		{name: "numeric_prefix", thresholdRaw: "12bad", ratioRaw: "1.25bad", wantThreshold: 12, wantRatio: 1.25},
		{name: "signed_with_space", thresholdRaw: "  -9px", ratioRaw: "  +0.5x", wantThreshold: -9, wantRatio: 0.5},
		{name: "exponent", thresholdRaw: "3e2", ratioRaw: "1e-1x", wantThreshold: 3, wantRatio: 0.1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MAX_FOLLOWS_THRESHOLD", tc.thresholdRaw)
			t.Setenv("MAX_FOLLOWS_RATIO", tc.ratioRaw)
			cfg := FromEnv()
			if cfg.MaxFollowsThreshold != tc.wantThreshold || !cfg.MaxFollowsThresholdSet {
				t.Fatalf("MAX_FOLLOWS_THRESHOLD=%q parsed as %d set=%v, want %d set=true", tc.thresholdRaw, cfg.MaxFollowsThreshold, cfg.MaxFollowsThresholdSet, tc.wantThreshold)
			}
			if cfg.MaxFollowsRatio != tc.wantRatio || !cfg.MaxFollowsRatioSet {
				t.Fatalf("MAX_FOLLOWS_RATIO=%q parsed as %g set=%v, want %g set=true", tc.ratioRaw, cfg.MaxFollowsRatio, cfg.MaxFollowsRatioSet, tc.wantRatio)
			}
		})
	}
}

func TestFromEnvMediaSizeLimitsMatchRailsToI(t *testing.T) {
	cases := []struct {
		name       string
		imageRaw   string
		videoRaw   string
		matrixRaw  string
		wantImage  int
		wantVideo  int
		wantMatrix int
	}{
		{name: "empty", imageRaw: "", videoRaw: "", matrixRaw: "", wantImage: 0, wantVideo: 0, wantMatrix: 0},
		{name: "invalid", imageRaw: "bad", videoRaw: "bad", matrixRaw: "bad", wantImage: 0, wantVideo: 0, wantMatrix: 0},
		{name: "numeric_prefix", imageRaw: "12bad", videoRaw: "13bad", matrixRaw: "14bad", wantImage: 12 * 1024 * 1024, wantVideo: 13 * 1024 * 1024, wantMatrix: 14},
		{name: "signed_with_space", imageRaw: "  +9px", videoRaw: "  -2px", matrixRaw: "  +7px", wantImage: 9 * 1024 * 1024, wantVideo: -2 * 1024 * 1024, wantMatrix: 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("IMAGE_LIMIT_MEGABYTES", tc.imageRaw)
			t.Setenv("VIDEO_LIMIT_MEGABYTES", tc.videoRaw)
			t.Setenv("MAX_ATTACHMENT_MATRIX_LIMIT", tc.matrixRaw)
			cfg := FromEnv()
			if cfg.ImageSizeLimit != tc.wantImage || !cfg.ImageSizeLimitSet {
				t.Fatalf("IMAGE_LIMIT_MEGABYTES=%q parsed as %d set=%v, want %d set=true", tc.imageRaw, cfg.ImageSizeLimit, cfg.ImageSizeLimitSet, tc.wantImage)
			}
			if cfg.VideoSizeLimit != tc.wantVideo || !cfg.VideoSizeLimitSet {
				t.Fatalf("VIDEO_LIMIT_MEGABYTES=%q parsed as %d set=%v, want %d set=true", tc.videoRaw, cfg.VideoSizeLimit, cfg.VideoSizeLimitSet, tc.wantVideo)
			}
			if cfg.MatrixLimit != tc.wantMatrix || !cfg.MatrixLimitSet {
				t.Fatalf("MAX_ATTACHMENT_MATRIX_LIMIT=%q parsed as %d set=%v, want %d set=true", tc.matrixRaw, cfg.MatrixLimit, cfg.MatrixLimitSet, tc.wantMatrix)
			}
			if err := cfg.ValidateRuntime(); err != nil {
				message := err.Error()
				for _, unexpected := range []string{"IMAGE_LIMIT_MEGABYTES", "VIDEO_LIMIT_MEGABYTES", "MAX_ATTACHMENT_MATRIX_LIMIT"} {
					if strings.Contains(message, unexpected) {
						t.Fatalf("explicit Rails-style media envs should not be rejected: %v", err)
					}
				}
			}
		})
	}
}

func TestFromEnvMaxMediaAttachmentsMatchesRailsToI(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 0},
		{"bad", 0},
		{"12bad", 12},
		{"  +9px", 9},
		{"-1", -1},
	} {
		t.Setenv("MAX_MEDIA_ATTACHMENTS", tc.raw)
		cfg := FromEnv()
		if cfg.MaxMedia != tc.want || !cfg.MaxMediaSet {
			t.Fatalf("MAX_MEDIA_ATTACHMENTS=%q parsed as %d set=%v, want %d set=true", tc.raw, cfg.MaxMedia, cfg.MaxMediaSet, tc.want)
		}
		if err := cfg.ValidateRuntime(); err != nil && strings.Contains(err.Error(), "MAX_MEDIA_ATTACHMENTS") {
			t.Fatalf("explicit Rails-style MAX_MEDIA_ATTACHMENTS=%q should not be rejected: %v", tc.raw, err)
		}
	}
}

func TestFromEnvLoadsStorageHostFallbacks(t *testing.T) {
	t.Setenv("S3_CLOUDFRONT_HOST", "https://cloudfront.example.test/")

	cfg := FromEnv()
	if cfg.StorageHost != "https://cloudfront.example.test" {
		t.Fatalf("StorageHost = %q", cfg.StorageHost)
	}

	_ = os.Unsetenv("S3_CLOUDFRONT_HOST")
	t.Setenv("S3_ENABLED", "true")
	t.Setenv("S3_ENDPOINT", "https://storage.example.test/")
	t.Setenv("S3_BUCKET", "bucket-name")
	t.Setenv("S3_REGION", "ap-northeast-1")
	t.Setenv("S3_HOSTNAME", "s3.ap-northeast-1.wasabisys.com")
	t.Setenv("AWS_ACCESS_KEY_ID", "access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "session")
	t.Setenv("S3_PERMISSION", "")
	t.Setenv("S3_STORAGE_CLASS", "STANDARD_IA")
	t.Setenv("S3_MULTIPART_THRESHOLD", "1048576")
	t.Setenv("S3_SIGNATURE_VERSION", "v4")
	t.Setenv("S3_OVERRIDE_PATH_STYLE", "true")
	cfg = FromEnv()
	if cfg.StorageHost != "https://storage.example.test/bucket-name" {
		t.Fatalf("StorageHost = %q", cfg.StorageHost)
	}
	if !cfg.S3Enabled || cfg.S3Bucket != "bucket-name" || cfg.S3Endpoint != "https://storage.example.test/" || cfg.S3Region != "ap-northeast-1" || cfg.S3Hostname != "s3.ap-northeast-1.wasabisys.com" || !cfg.S3OverridePathStyle {
		t.Fatalf("S3 config = %#v", cfg)
	}
	if !cfg.S3RegionSet || !cfg.S3HostnameSet {
		t.Fatalf("S3 explicit env flags not preserved: region_set=%v hostname_set=%v", cfg.S3RegionSet, cfg.S3HostnameSet)
	}
	if cfg.S3AccessKeyID != "access" || cfg.S3SecretAccessKey != "secret" || cfg.S3SessionToken != "session" || cfg.S3Permission != "" || cfg.S3StorageClass != "STANDARD_IA" || !cfg.S3StorageClassSet || cfg.S3MultipartThreshold != 1048576 || !cfg.S3MultipartThresholdSet || cfg.S3SignatureVersion != "v4" {
		t.Fatalf("S3 credentials/options = %#v", cfg)
	}

	t.Setenv("S3_STORAGE_CLASS", "")
	cfg = FromEnv()
	if cfg.S3StorageClass != "" || !cfg.S3StorageClassSet {
		t.Fatalf("explicit blank S3_STORAGE_CLASS should preserve Rails ENV.has_key? boundary: value=%q set=%v", cfg.S3StorageClass, cfg.S3StorageClassSet)
	}

	t.Setenv("S3_ALIAS_HOST", "media.example.test")
	cfg = FromEnv()
	if cfg.StorageHost != "https://media.example.test" {
		t.Fatalf("StorageHost alias override = %q", cfg.StorageHost)
	}

	_ = os.Unsetenv("S3_ALIAS_HOST")
	t.Setenv("S3_ENABLED", "")
	t.Setenv("S3_ENDPOINT", "")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("AZURE_ALIAS_HOST", "azure.example.test")
	t.Setenv("AZURE_ENABLED", "true")
	t.Setenv("AZURE_STORAGE_ACCOUNT", "account-name")
	t.Setenv("AZURE_STORAGE_ACCESS_KEY", "azure-key")
	t.Setenv("AZURE_CONTAINER_NAME", "media")
	cfg = FromEnv()
	if cfg.StorageHost != "https://azure.example.test" {
		t.Fatalf("StorageHost = %q", cfg.StorageHost)
	}
	if !cfg.AzureEnabled || cfg.AzureStorageAccount != "account-name" || cfg.AzureStorageAccessKey != "azure-key" || cfg.AzureContainerName != "media" || cfg.AzureAliasHost != "azure.example.test" {
		t.Fatalf("Azure config = %#v", cfg)
	}

	_ = os.Unsetenv("AZURE_ALIAS_HOST")
	t.Setenv("AZURE_ENABLED", "")
	t.Setenv("SWIFT_OBJECT_URL", "https://swift.example.test/container")
	t.Setenv("SWIFT_ENABLED", "true")
	t.Setenv("SWIFT_CONTAINER", "container")
	t.Setenv("SWIFT_TEMP_URL_KEY", "swift-temp-key")
	t.Setenv("SWIFT_USERNAME", "swift-user")
	t.Setenv("SWIFT_PROJECT_ID", "project-id")
	t.Setenv("SWIFT_TENANT", "tenant-name")
	t.Setenv("SWIFT_PASSWORD", "swift-password")
	t.Setenv("SWIFT_AUTH_URL", "https://keystone.example.test/v3/")
	t.Setenv("SWIFT_DOMAIN_NAME", "example-domain")
	t.Setenv("SWIFT_REGION", "region-one")
	t.Setenv("SWIFT_CACHE_TTL", "120")
	cfg = FromEnv()
	if cfg.StorageHost != "https://swift.example.test/container" {
		t.Fatalf("StorageHost = %q", cfg.StorageHost)
	}
	if !cfg.SwiftEnabled || cfg.SwiftContainer != "container" || cfg.SwiftObjectURL != "https://swift.example.test/container" || cfg.SwiftTempURLKey != "swift-temp-key" {
		t.Fatalf("Swift config = %#v", cfg)
	}
	if cfg.SwiftUsername != "swift-user" || cfg.SwiftProjectID != "project-id" || cfg.SwiftTenant != "tenant-name" || cfg.SwiftPassword != "swift-password" || cfg.SwiftAuthURL != "https://keystone.example.test/v3/" || cfg.SwiftDomainName != "example-domain" || cfg.SwiftRegion != "region-one" || cfg.SwiftCacheTTL != "120" {
		t.Fatalf("Swift config = %#v", cfg)
	}
	if !cfg.SwiftDomainNameSet || !cfg.SwiftCacheTTLSet {
		t.Fatalf("Swift explicit env flags not preserved: domain_set=%v cache_ttl_set=%v", cfg.SwiftDomainNameSet, cfg.SwiftCacheTTLSet)
	}
}

func TestFromEnvLoadsCacheBusterSettings(t *testing.T) {
	t.Setenv("CACHE_BUSTER_ENABLED", "true")
	t.Setenv("CACHE_BUSTER_SECRET_HEADER", "X-Bust-Secret")
	t.Setenv("CACHE_BUSTER_SECRET", "secret")
	t.Setenv("CACHE_BUSTER_HTTP_METHOD", "PURGE")

	cfg := FromEnv()
	if !cfg.CacheBusterEnabled {
		t.Fatal("CacheBusterEnabled = false")
	}
	if cfg.CacheBusterSecretHeader != "X-Bust-Secret" || cfg.CacheBusterSecret != "secret" || cfg.CacheBusterHTTPMethod != "PURGE" {
		t.Fatalf("cache buster config = %#v", cfg)
	}

	t.Setenv("CACHE_BUSTER_HTTP_METHOD", "")
	cfg = FromEnv()
	if cfg.CacheBusterHTTPMethod != "" {
		t.Fatalf("explicit blank CACHE_BUSTER_HTTP_METHOD should match Rails raw env, got %q", cfg.CacheBusterHTTPMethod)
	}

	t.Setenv("CACHE_BUSTER_HTTP_METHOD", "  ")
	cfg = FromEnv()
	if cfg.CacheBusterHTTPMethod != "  " {
		t.Fatalf("explicit whitespace CACHE_BUSTER_HTTP_METHOD should match Rails raw env, got %q", cfg.CacheBusterHTTPMethod)
	}
}

func TestFromEnvLoadsRailsHTTPProxySettings(t *testing.T) {
	t.Setenv("http_proxy", "http://user:pass@proxy.example.test:8080")
	t.Setenv("http_hidden_proxy", "https://hidden-proxy.example.test:8443")
	t.Setenv("TRUSTED_PROXY_IP", "203.0.113.10, 2001:db8::/64")
	t.Setenv("ALLOW_ACCESS_TO_HIDDEN_SERVICE", "true")
	t.Setenv("ALLOWED_PRIVATE_ADDRESSES", "10.0.0.0/8, 127.0.0.1")

	cfg := FromEnv()
	if cfg.HTTPProxyURL != "http://user:pass@proxy.example.test:8080" {
		t.Fatalf("HTTPProxyURL = %q", cfg.HTTPProxyURL)
	}
	if cfg.HTTPHiddenProxyURL != "https://hidden-proxy.example.test:8443" {
		t.Fatalf("HTTPHiddenProxyURL = %q", cfg.HTTPHiddenProxyURL)
	}
	if cfg.TrustedProxyIP != "203.0.113.10, 2001:db8::/64" {
		t.Fatalf("TrustedProxyIP = %q", cfg.TrustedProxyIP)
	}
	if !cfg.AllowAccessToHiddenService {
		t.Fatal("AllowAccessToHiddenService = false")
	}
	if cfg.AllowedPrivateAddresses != "10.0.0.0/8, 127.0.0.1" {
		t.Fatalf("AllowedPrivateAddresses = %q", cfg.AllowedPrivateAddresses)
	}
}

func TestFromEnvUsesRailsCacheBusterDefaultSecretHeader(t *testing.T) {
	t.Setenv("CACHE_BUSTER_ENABLED", "true")

	cfg := FromEnv()
	if !cfg.CacheBusterEnabled {
		t.Fatal("CacheBusterEnabled = false")
	}
	if cfg.CacheBusterHTTPMethod != "GET" || cfg.CacheBusterSecretHeader != "Secret-Header" || cfg.CacheBusterSecret != "True" {
		t.Fatalf("cache buster defaults = %#v", cfg)
	}
}

func TestFromEnvDoesNotAddRailsCacheBusterDefaultSecretWhenMethodIsExplicit(t *testing.T) {
	t.Setenv("CACHE_BUSTER_ENABLED", "true")
	t.Setenv("CACHE_BUSTER_HTTP_METHOD", "GET")

	cfg := FromEnv()
	if cfg.CacheBusterHTTPMethod != "GET" {
		t.Fatalf("CacheBusterHTTPMethod = %q", cfg.CacheBusterHTTPMethod)
	}
	if cfg.CacheBusterSecretHeader != "" || cfg.CacheBusterSecret != "" {
		t.Fatalf("cache buster explicit method config = %#v", cfg)
	}
}

func TestSystemAssetURLMatchesPaperclipStorageHosts(t *testing.T) {
	cfg := Config{WebDomain: "social.example", Scheme: "https"}
	if got := cfg.SystemAssetURL("media_attachments/files/000/000/123/original/photo.png"); got != "https://social.example/system/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("filesystem asset URL = %q", got)
	}

	cfg.PaperclipRootURL = "/uploads/system"
	if got := cfg.SystemAssetURL("media_attachments/files/000/000/123/original/photo.png"); got != "https://social.example/uploads/system/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("custom filesystem asset URL = %q", got)
	}

	cfg.PaperclipRootURL = "https://assets.example.test/system"
	if got := cfg.SystemAssetURL("media_attachments/files/000/000/123/original/photo.png"); got != "https://assets.example.test/system/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("absolute filesystem asset URL = %q", got)
	}

	cfg.PaperclipRootURL = ""
	cfg.PaperclipRootURLSet = true
	if got := cfg.SystemAssetURL("media_attachments/files/000/000/123/original/photo.png"); got != "https://social.example/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("blank filesystem asset URL = %q", got)
	}

	cfg.StorageHost = "https://media.example/"
	if got := cfg.SystemAssetURL("/media_attachments/files/000/000/123/original/photo.png"); got != "https://media.example/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("object-storage asset URL = %q", got)
	}

	cfg.StorageHost = "https://storage.example.test/bucket-name"
	if got := cfg.SystemAssetURL("media_attachments/files/000/000/123/original/photo.png"); got != "https://storage.example.test/bucket-name/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("s3 endpoint asset URL = %q", got)
	}
}

func TestSystemAssetPathMatchesPaperclipRootPath(t *testing.T) {
	cfg := Config{PublicDir: "/srv/paon/public"}
	if got := cfg.SystemAssetPath("media_attachments", "files", "000", "original", "photo.png"); got != "/srv/paon/public/system/media_attachments/files/000/original/photo.png" {
		t.Fatalf("default system asset path = %q", got)
	}

	cfg.PaperclipRootPath = "/mnt/mastodon/system/"
	if got := cfg.SystemAssetPath("media_attachments", "files", "000", "original", "photo.png"); got != "/mnt/mastodon/system/media_attachments/files/000/original/photo.png" {
		t.Fatalf("custom system asset path = %q", got)
	}

	cfg.PaperclipRootPath = ""
	cfg.PaperclipRootPathSet = true
	if got := cfg.SystemAssetPath("media_attachments", "files", "000", "original", "photo.png"); got != "media_attachments/files/000/original/photo.png" {
		t.Fatalf("blank system asset path = %q", got)
	}
}

func TestFromEnvPrefersExplicitPaonGoListenAddr(t *testing.T) {
	t.Setenv("PAON_GO_ADDR", "127.0.0.1:4567")
	t.Setenv("SOCKET", "/tmp/ignored-paon.sock")
	t.Setenv("BIND", "0.0.0.0")
	t.Setenv("PORT", "3000")

	cfg := FromEnv()
	if cfg.ListenAddr != "127.0.0.1:4567" {
		t.Fatalf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.ListenNetwork != "tcp" {
		t.Fatalf("ListenNetwork = %q", cfg.ListenNetwork)
	}
}

func TestFromEnvBuildsListenAddrFromMastodonBindAndPort(t *testing.T) {
	t.Setenv("PAON_GO_ADDR", "")
	unsetEnvForTest(t, "SOCKET")
	t.Setenv("BIND", "127.0.0.1")
	t.Setenv("PORT", "4000")

	cfg := FromEnv()
	if cfg.ListenAddr != "127.0.0.1:4000" {
		t.Fatalf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.ListenNetwork != "tcp" {
		t.Fatalf("ListenNetwork = %q", cfg.ListenNetwork)
	}
}

func TestFromEnvKeepsBindWithPort(t *testing.T) {
	t.Setenv("PAON_GO_ADDR", "")
	unsetEnvForTest(t, "SOCKET")
	t.Setenv("BIND", "0.0.0.0:5000")
	t.Setenv("PORT", "4000")

	cfg := FromEnv()
	if cfg.ListenAddr != "0.0.0.0:5000" {
		t.Fatalf("ListenAddr = %q", cfg.ListenAddr)
	}
}

func TestFromEnvDefaultsListenAddrToPort3000(t *testing.T) {
	t.Setenv("PAON_GO_ADDR", "")
	unsetEnvForTest(t, "SOCKET")
	unsetEnvForTest(t, "BIND")
	unsetEnvForTest(t, "PORT")

	cfg := FromEnv()
	if cfg.ListenAddr != "127.0.0.1:3000" {
		t.Fatalf("ListenAddr = %q", cfg.ListenAddr)
	}
}

func TestFromEnvPrefersDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://explicit.example/paon")
	t.Setenv("DB_HOST", "ignored.example")
	t.Setenv("DB_NAME", "ignored")

	cfg := FromEnv()
	if cfg.DatabaseURL != "postgres://explicit.example/paon" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

func TestFromEnvLoadsDatabaseLockTimeout(t *testing.T) {
	unsetEnvForTest(t, "PAON_DB_LOCK_TIMEOUT")
	if got := FromEnv().DatabaseLockTimeout; got != 5*time.Second {
		t.Fatalf("default DatabaseLockTimeout = %s, want 5s", got)
	}

	t.Setenv("PAON_DB_LOCK_TIMEOUT", "750ms")
	if got := FromEnv().DatabaseLockTimeout; got != 750*time.Millisecond {
		t.Fatalf("duration DatabaseLockTimeout = %s, want 750ms", got)
	}

	t.Setenv("PAON_DB_LOCK_TIMEOUT", "3")
	if got := FromEnv().DatabaseLockTimeout; got != 3*time.Second {
		t.Fatalf("numeric DatabaseLockTimeout = %s, want 3s", got)
	}
}

func TestFromEnvDoesNotEnableReplicaWithoutRailsReplicaEnvKeys(t *testing.T) {
	unsetEnvForTest(t, "REPLICA_DATABASE_URL")
	unsetEnvForTest(t, "REPLICA_DB_NAME")
	unsetEnvForTest(t, "REPLICA_DB_HOST")
	unsetEnvForTest(t, "REPLICA_DB_USER")
	unsetEnvForTest(t, "REPLICA_DB_PASS")
	unsetEnvForTest(t, "REPLICA_DB_PORT")

	cfg := FromEnv()
	if cfg.ReplicaDatabaseURL != "" {
		t.Fatalf("ReplicaDatabaseURL = %q, want empty unless REPLICA_DATABASE_URL or REPLICA_DB_NAME is set", cfg.ReplicaDatabaseURL)
	}
}

func TestFromEnvUsesRailsProductionDatabaseDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("RAILS_ENV", "production")

	cfg := FromEnv()
	want := "postgres://mastodon@localhost:5432/mastodon_production?connect_timeout=15&sslmode=prefer"
	if cfg.DatabaseURL != want {
		t.Fatalf("production default DatabaseURL = %q, want %q", cfg.DatabaseURL, want)
	}
}

func TestFromEnvHonorsRailsDBSSLMode(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "db.example")
	t.Setenv("DB_NAME", "mastodon_production")
	t.Setenv("DB_SSLMODE", "require")

	cfg := FromEnv()
	want := "postgres://db.example/mastodon_production?connect_timeout=15&sslmode=require"
	if cfg.DatabaseURL != want {
		t.Fatalf("DatabaseURL = %q, want %q", cfg.DatabaseURL, want)
	}
}

func TestFromEnvPreservesExplicitBlankRailsDBSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("RAILS_ENV", "production")
	t.Setenv("DB_NAME", "")
	t.Setenv("DB_USER", "")
	t.Setenv("DB_HOST", "")
	t.Setenv("DB_PORT", "")
	t.Setenv("DB_SSLMODE", "")

	cfg := FromEnv()
	want := "postgres:///?connect_timeout=15&sslmode="
	if cfg.DatabaseURL != want {
		t.Fatalf("blank DB_* DatabaseURL = %q, want %q", cfg.DatabaseURL, want)
	}
}

func TestFromEnvPreservesExplicitBlankRailsReplicaDBOverrides(t *testing.T) {
	t.Setenv("REPLICA_DATABASE_URL", "")
	t.Setenv("RAILS_ENV", "production")
	t.Setenv("REPLICA_DB_NAME", "replica")
	t.Setenv("REPLICA_DB_USER", "")
	t.Setenv("REPLICA_DB_PASS", "")
	t.Setenv("REPLICA_DB_HOST", "")
	t.Setenv("REPLICA_DB_PORT", "")
	t.Setenv("DB_USER", "primary")
	t.Setenv("DB_PASS", "primary-pass")
	t.Setenv("DB_HOST", "primary.example")
	t.Setenv("DB_PORT", "6543")
	t.Setenv("DB_SSLMODE", "")

	cfg := FromEnv()
	want := "postgres:///replica?connect_timeout=15&sslmode="
	if cfg.ReplicaDatabaseURL != want {
		t.Fatalf("blank REPLICA_DB_* ReplicaDatabaseURL = %q, want %q", cfg.ReplicaDatabaseURL, want)
	}
}

func TestFromEnvLoadsRemoteMediaCacheSwitch(t *testing.T) {
	t.Setenv("DISABLE_REMOTE_MEDIA_CACHE", "true")

	cfg := FromEnv()
	if !cfg.DisableRemoteMediaCache {
		t.Fatal("DisableRemoteMediaCache = false")
	}
}

func TestFromEnvLoadsSingleUserMode(t *testing.T) {
	t.Setenv("SINGLE_USER_MODE", "true")

	cfg := FromEnv()
	if !cfg.SingleUserMode {
		t.Fatal("SingleUserMode = false")
	}
}

func TestFromEnvLoadsLimitedFederationModeAliases(t *testing.T) {
	t.Setenv("LIMITED_FEDERATION_MODE", "true")

	cfg := FromEnv()
	if !cfg.LimitedFederationMode {
		t.Fatal("LimitedFederationMode = false")
	}

	t.Setenv("LIMITED_FEDERATION_MODE", "")
	t.Setenv("WHITELIST_MODE", "true")

	cfg = FromEnv()
	if !cfg.LimitedFederationMode {
		t.Fatal("LimitedFederationMode from WHITELIST_MODE = false")
	}
}

func TestFromEnvLoadsRailsDevelopmentHosts(t *testing.T) {
	t.Setenv("RAILS_DEVELOPMENT_HOSTS", " devbox.local, preview.local ,, ")
	cfg := FromEnv()
	want := []string{"devbox.local", "preview.local"}
	if len(cfg.RailsDevelopmentHosts) != len(want) {
		t.Fatalf("RailsDevelopmentHosts = %#v, want %#v", cfg.RailsDevelopmentHosts, want)
	}
	for index := range want {
		if cfg.RailsDevelopmentHosts[index] != want[index] {
			t.Fatalf("RailsDevelopmentHosts = %#v, want %#v", cfg.RailsDevelopmentHosts, want)
		}
	}
}

func TestFromEnvLoadsDynamoDBRailsBlankCredentialEdges(t *testing.T) {
	t.Setenv("DYNAMODB_ENABLED", "true")
	t.Setenv("DYNAMODB_AWS_ACCESS_KEY_ID", "")
	t.Setenv("DYNAMODB_AWS_SECRET_ACCESS_KEY", " ")
	t.Setenv("DYNAMODB_AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "fallback-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fallback-secret")
	t.Setenv("AWS_SESSION_TOKEN", "fallback-session")
	t.Setenv("DYNAMODB_REGION", "")

	cfg := FromEnv()
	if cfg.DynamoDBAccessKey != "" || cfg.DynamoDBSecretKey != " " || cfg.DynamoDBSessionToken != "" {
		t.Fatalf("DynamoDB explicit blank credentials should not fall back to AWS_*: %#v", cfg)
	}
	if cfg.DynamoDBRegion != "" || !cfg.DynamoDBRegionSet {
		t.Fatalf("explicit blank DYNAMODB_REGION should be preserved and marked set: %#v", cfg)
	}
}

func TestFromEnvLoadsDynamoDBRailsFallbackCredentials(t *testing.T) {
	for _, name := range []string{"DYNAMODB_AWS_ACCESS_KEY_ID", "DYNAMODB_AWS_SECRET_ACCESS_KEY", "DYNAMODB_AWS_SESSION_TOKEN", "DYNAMODB_REGION"} {
		oldValue, hadValue := os.LookupEnv(name)
		_ = os.Unsetenv(name)
		t.Cleanup(func() {
			if hadValue {
				_ = os.Setenv(name, oldValue)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "fallback-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fallback-secret")
	t.Setenv("AWS_SESSION_TOKEN", "fallback-session")

	cfg := FromEnv()
	if cfg.DynamoDBAccessKey != "fallback-access" || cfg.DynamoDBSecretKey != "fallback-secret" || cfg.DynamoDBSessionToken != "fallback-session" {
		t.Fatalf("DynamoDB AWS_* fallback credentials = %#v", cfg)
	}
	if cfg.DynamoDBRegion != "ap-northeast-1" || cfg.DynamoDBRegionSet {
		t.Fatalf("unset DYNAMODB_REGION should use Rails default without set flag: %#v", cfg)
	}
}

func TestFromEnvLoadsSSOAccountURLs(t *testing.T) {
	t.Setenv("SSO_ACCOUNT_SIGN_UP", "https://sso.example.test/sign-up")
	t.Setenv("SSO_ACCOUNT_SETTINGS", "https://sso.example.test/account")

	cfg := FromEnv()
	if cfg.SSOAccountSignUpURL != "https://sso.example.test/sign-up" {
		t.Fatalf("SSOAccountSignUpURL = %q", cfg.SSOAccountSignUpURL)
	}
	if cfg.SSOAccountSettingsURL != "https://sso.example.test/account" {
		t.Fatalf("SSOAccountSettingsURL = %q", cfg.SSOAccountSettingsURL)
	}
	if !cfg.SSOAccountSignUpURLSet {
		t.Fatal("SSOAccountSignUpURLSet = false")
	}

	t.Setenv("SSO_ACCOUNT_SIGN_UP", "")
	cfg = FromEnv()
	if cfg.SSOAccountSignUpURL != "" || !cfg.SSOAccountSignUpURLSet {
		t.Fatalf("blank SSO_ACCOUNT_SIGN_UP was not preserved: url=%q set=%v", cfg.SSOAccountSignUpURL, cfg.SSOAccountSignUpURLSet)
	}
}

func TestFromEnvLoadsRailsExactTrueRuntimeGuards(t *testing.T) {
	t.Setenv("OMNIAUTH_ONLY", "true")
	t.Setenv("DISABLE_SIGNUP_BY_API", "true")
	t.Setenv("DISALLOW_UNAUTHENTICATED_API_ACCESS", "true")
	t.Setenv("AUTHORIZED_FETCH", "true")
	t.Setenv("DISABLE_AUTOMATIC_SWITCHING_TO_APPROVED_REGISTRATIONS", "true")
	t.Setenv("EMAIL_DOMAIN_LISTS_APPLY_AFTER_CONFIRMATION", "true")
	t.Setenv("DISABLE_LOGIN_TOKEN_CHALLENGE", "true")

	cfg := FromEnv()
	if !cfg.OmniAuthOnly || !cfg.DisableSignupByAPI || !cfg.DisallowUnauthenticatedAPIAccess || !cfg.AuthorizedFetch || !cfg.DisableAutoSwitchingRegistrations || !cfg.EmailDomainListsApplyAfterConfirm || !cfg.SuspiciousSignInDisabled {
		t.Fatalf("Rails exact true guards were not loaded: %#v", cfg)
	}
	if !cfg.AuthorizedFetchEnvSet {
		t.Fatal("AUTHORIZED_FETCH key presence was not preserved")
	}

	t.Setenv("OMNIAUTH_ONLY", "TRUE")
	t.Setenv("DISABLE_SIGNUP_BY_API", " true ")
	t.Setenv("DISALLOW_UNAUTHENTICATED_API_ACCESS", "1")
	t.Setenv("AUTHORIZED_FETCH", "yes")
	t.Setenv("DISABLE_AUTOMATIC_SWITCHING_TO_APPROVED_REGISTRATIONS", "TRUE")
	t.Setenv("EMAIL_DOMAIN_LISTS_APPLY_AFTER_CONFIRMATION", "TRUE")
	t.Setenv("DISABLE_LOGIN_TOKEN_CHALLENGE", "false")
	t.Setenv("DISABLE_SUSPICIOUS_SIGN_IN", "true")
	cfg = FromEnv()
	if cfg.OmniAuthOnly || cfg.DisableSignupByAPI || cfg.DisallowUnauthenticatedAPIAccess || cfg.AuthorizedFetch || cfg.DisableAutoSwitchingRegistrations || cfg.EmailDomainListsApplyAfterConfirm || cfg.SuspiciousSignInDisabled {
		t.Fatalf("Rails exact true guards accepted non-literal values: %#v", cfg)
	}
	if !cfg.AuthorizedFetchEnvSet {
		t.Fatal("AUTHORIZED_FETCH=false-ish override key presence was not preserved")
	}

	os.Unsetenv("DISABLE_LOGIN_TOKEN_CHALLENGE")
	cfg = FromEnv()
	if !cfg.SuspiciousSignInDisabled {
		t.Fatalf("legacy suspicious sign-in disable fallback was not loaded: %#v", cfg)
	}
}

func TestFromEnvOmitsSSORedirectWhenMultipleProvidersAreEnabled(t *testing.T) {
	t.Setenv("ONE_CLICK_SSO_LOGIN", "true")
	t.Setenv("OMNIAUTH_ONLY", "true")
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("SAML_ENABLED", "true")

	cfg := FromEnv()
	if cfg.SSORedirect != "" {
		t.Fatalf("SSORedirect = %q", cfg.SSORedirect)
	}
}

func TestFromEnvLoadsTranslationRailsPresenceEdges(t *testing.T) {
	t.Setenv("DEEPL_API_KEY", " ")
	t.Setenv("DEEPL_PLAN", "")
	t.Setenv("LIBRE_TRANSLATE_ENDPOINT", " ")
	t.Setenv("LIBRE_TRANSLATE_API_KEY", "")

	cfg := FromEnv()
	if cfg.DeepLAPIKey != "" {
		t.Fatalf("blank DeepL API key should follow Rails present? semantics, got %q", cfg.DeepLAPIKey)
	}
	if cfg.LibreTranslateEndpoint != "" {
		t.Fatalf("blank LibreTranslate endpoint should follow Rails present? semantics, got %q", cfg.LibreTranslateEndpoint)
	}
	if cfg.LibreTranslateAPIKey != "" || !cfg.LibreTranslateAPIKeySet {
		t.Fatalf("explicit blank LibreTranslate API key should be preserved as an env-set value: %#v", cfg)
	}
	if cfg.DeepLPlan != "" || !cfg.DeepLPlanSet {
		t.Fatalf("explicit blank DEEPL_PLAN should be preserved and marked set: %#v", cfg)
	}
}

func TestFromEnvLoadsTranslationUnsetDeepLPlanDefault(t *testing.T) {
	oldPlan, hadPlan := os.LookupEnv("DEEPL_PLAN")
	if err := os.Unsetenv("DEEPL_PLAN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadPlan {
			_ = os.Setenv("DEEPL_PLAN", oldPlan)
		} else {
			_ = os.Unsetenv("DEEPL_PLAN")
		}
	})

	cfg := FromEnv()
	if cfg.DeepLPlan != "free" || cfg.DeepLPlanSet {
		t.Fatalf("unset DEEPL_PLAN should default to Rails free without set flag: %#v", cfg)
	}
}

func TestConfigLocaleFallsBackToRailsEnglish(t *testing.T) {
	if got := (Config{}).Locale(); got != "en" {
		t.Fatalf("Locale fallback = %q", got)
	}
	if got := (Config{DefaultLocale: " en "}).Locale(); got != "en" {
		t.Fatalf("Locale configured = %q", got)
	}
}

func TestFromEnvLoadsStreamingAPIBaseURL(t *testing.T) {
	cfg := FromEnv()
	if cfg.StreamingAPIBaseURL != "ws://localhost:3000" {
		t.Fatalf("development default StreamingAPIBaseURL = %q", cfg.StreamingAPIBaseURL)
	}

	t.Setenv("RAILS_ENV", "production")
	t.Setenv("WEB_DOMAIN", "web.example.test")
	cfg = FromEnv()
	if cfg.StreamingAPIBaseURL != "wss://web.example.test" {
		t.Fatalf("production default StreamingAPIBaseURL = %q", cfg.StreamingAPIBaseURL)
	}

	t.Setenv("RAILS_ENV", "development")
	t.Setenv("LOCAL_DOMAIN", "dev.example.test:3000")
	t.Setenv("LOCAL_HTTPS", "true")
	t.Setenv("REMOTE_DEV", "true")
	cfg = FromEnv()
	if cfg.StreamingAPIBaseURL != "wss://dev.example.test:3000" {
		t.Fatalf("remote-dev default StreamingAPIBaseURL = %q", cfg.StreamingAPIBaseURL)
	}

	t.Setenv("STREAMING_API_BASE_URL", "wss://streaming.example.test/")

	cfg = FromEnv()
	if cfg.StreamingAPIBaseURL != "wss://streaming.example.test/" {
		t.Fatalf("StreamingAPIBaseURL = %q", cfg.StreamingAPIBaseURL)
	}
	if !cfg.StreamingAPIBaseURLSet {
		t.Fatal("StreamingAPIBaseURLSet = false")
	}
	if got := cfg.StreamingBaseURL(); got != "wss://streaming.example.test/" {
		t.Fatalf("StreamingBaseURL = %q", got)
	}

	t.Setenv("STREAMING_API_BASE_URL", "")
	cfg = FromEnv()
	if cfg.StreamingAPIBaseURL != "" || !cfg.StreamingAPIBaseURLSet {
		t.Fatalf("explicit blank STREAMING_API_BASE_URL not preserved: value=%q set=%v", cfg.StreamingAPIBaseURL, cfg.StreamingAPIBaseURLSet)
	}
	if got := cfg.StreamingBaseURL(); got != "" {
		t.Fatalf("explicit blank StreamingBaseURL = %q", got)
	}
}

func TestStreamingBaseURLFallsBackToWebSocketBase(t *testing.T) {
	cfg := Config{WebDomain: "example.test", Scheme: "https"}
	if got := cfg.StreamingBaseURL(); got != "wss://example.test" {
		t.Fatalf("https StreamingBaseURL = %q", got)
	}

	cfg = Config{WebDomain: "localhost:3000", Scheme: "http"}
	if got := cfg.StreamingBaseURL(); got != "ws://localhost:3000" {
		t.Fatalf("http StreamingBaseURL = %q", got)
	}
}

func TestFromEnvLoadsCloudflareTurnstileSettings(t *testing.T) {
	t.Setenv("CLOUDFLARE_TURNSTILE_ENABLED", "true")
	t.Setenv("CLOUDFLARE_TURNSTILE_SITE_KEY", "site")
	t.Setenv("CLOUDFLARE_TURNSTILE_SECRET_KEY", "secret")
	t.Setenv("HCAPTCHA_SITE_KEY", "h-site")
	t.Setenv("HCAPTCHA_SECRET_KEY", "h-secret")

	cfg := FromEnv()
	if !cfg.CloudflareTurnstileEnabled || cfg.CloudflareTurnstileSiteKey != "site" || cfg.CloudflareTurnstileSecretKey != "secret" {
		t.Fatalf("Turnstile config = %#v", cfg)
	}
	if cfg.HCaptchaSiteKey != "h-site" || cfg.HCaptchaSecretKey != "h-secret" {
		t.Fatalf("hCaptcha config = %#v", cfg)
	}
}

func TestValidateRuntimeAcceptsDefaultDropInConfig(t *testing.T) {
	cfg := FromEnv()
	if err := cfg.ValidateRuntime(); err != nil {
		t.Fatalf("ValidateRuntime: %v", err)
	}
}

func TestValidateRuntimeRequiresSecretSafeActiveRecordEncryptionKeysInProduction(t *testing.T) {
	t.Setenv("RAILS_ENV", "production")
	t.Setenv("SECRET_KEY_BASE", strings.Repeat("s", 64))
	t.Setenv("OTP_SECRET", strings.Repeat("o", 64))
	t.Setenv("ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY", "short-deterministic")
	t.Setenv("ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT", "short-salt")
	t.Setenv("ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY", "short-primary")
	cfg := FromEnv()
	err := cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("production accepted short Active Record encryption credentials")
	}
	for _, name := range []string{
		"ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY",
		"ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT",
		"ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("validation error %q missing %s", err, name)
		}
		if strings.Contains(err.Error(), "short-") {
			t.Fatalf("validation error leaked a credential: %v", err)
		}
	}
}

func TestValidateRuntimeRejectsInvalidBaseURL(t *testing.T) {
	cfg := FromEnv()
	cfg.Scheme = "ftp"
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "WEB_DOMAIN/PAON_SCHEME") {
		t.Fatalf("ValidateRuntime error = %v", err)
	}

	cfg = FromEnv()
	cfg.WebDomain = "://bad"
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "WEB_DOMAIN/PAON_SCHEME") {
		t.Fatalf("ValidateRuntime web domain error = %v", err)
	}

	cfg = FromEnv()
	cfg.StreamingAPIBaseURL = "ftp://streaming.example.test"
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "STREAMING_API_BASE_URL") {
		t.Fatalf("ValidateRuntime streaming URL error = %v", err)
	}
}

func TestValidateRuntimeRejectsInvalidListenAddr(t *testing.T) {
	cfg := FromEnv()
	cfg.ListenAddr = "127.0.0.1"
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("ValidateRuntime error = %v", err)
	}

	cfg = FromEnv()
	cfg.PersistentTimeout = -1 * time.Second
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "PERSISTENT_TIMEOUT") {
		t.Fatalf("ValidateRuntime persistent timeout error = %v", err)
	}
}

func TestValidateRuntimeRejectsInvalidDatabasePoolSettings(t *testing.T) {
	cfg := FromEnv()
	cfg.DatabaseMaxOpenConns = 0
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "DB_POOL") {
		t.Fatalf("ValidateRuntime DB_POOL error = %v", err)
	}

	cfg = FromEnv()
	cfg.DatabaseMaxIdleConns = -1
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "PAON_DB_MAX_IDLE_CONNS") {
		t.Fatalf("ValidateRuntime idle pool error = %v", err)
	}

	cfg = FromEnv()
	cfg.DatabaseMaxOpenConns = 2
	cfg.DatabaseMaxIdleConns = 3
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "less than or equal to DB_POOL") {
		t.Fatalf("ValidateRuntime pool ordering error = %v", err)
	}

	cfg = FromEnv()
	cfg.DatabaseLockTimeout = -1
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "PAON_DB_LOCK_TIMEOUT") {
		t.Fatalf("ValidateRuntime lock timeout error = %v", err)
	}

	cfg = FromEnv()
	cfg.MaxSessionActivations = -2
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "MAX_SESSION_ACTIVATIONS") {
		t.Fatalf("ValidateRuntime max session activations error = %v", err)
	}

	cfg = FromEnv()
	cfg.MaxRequestPoolSize = -1
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "MAX_REQUEST_POOL_SIZE") {
		t.Fatalf("ValidateRuntime max request pool size error = %v", err)
	}
}

func TestValidateRuntimeAcceptsUnlimitedSessionActivations(t *testing.T) {
	t.Setenv("MAX_SESSION_ACTIVATIONS", "-1")
	cfg := FromEnv()
	if cfg.MaxSessionActivations != -1 {
		t.Fatalf("MaxSessionActivations = %d, want -1", cfg.MaxSessionActivations)
	}
	if err := cfg.ValidateRuntime(); err != nil {
		t.Fatalf("ValidateRuntime rejected unlimited session activations: %v", err)
	}
}

func TestValidateRuntimeRejectsInvalidDatabaseURLs(t *testing.T) {
	for _, raw := range []string{
		"http://db.example.test/mastodon",
		"postgres://db.example.test/",
		"postgres://db.example.test:70000/mastodon",
	} {
		cfg := FromEnv()
		cfg.DatabaseURL = raw
		err := cfg.ValidateRuntime()
		if err == nil || !strings.Contains(err.Error(), "DATABASE_URL/DB_*") {
			t.Fatalf("ValidateRuntime(%q) error = %v", raw, err)
		}
	}
	cfg := FromEnv()
	cfg.PgHeroStatsDatabaseURL = "http://stats.example.test/pghero"
	err := cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "PGHERO_STATS_DATABASE_URL") {
		t.Fatalf("ValidateRuntime invalid PgHero stats URL error = %v", err)
	}
	cfg = FromEnv()
	cfg.PgHeroOtherDatabaseURL = "http://other.example.test/pghero"
	err = cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "OTHER_DATABASE_URL") {
		t.Fatalf("ValidateRuntime invalid PgHero other URL error = %v", err)
	}
}

func TestValidateRuntimeRejectsInvalidRailsDBPort(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "db.example.test")
	t.Setenv("DB_PORT", "70000")
	t.Setenv("DB_NAME", "mastodon")

	cfg := FromEnv()
	err := cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL/DB_*") {
		t.Fatalf("ValidateRuntime DB_PORT-built URL error = %v", err)
	}
}

func TestValidateRuntimeRejectsInvalidStatusAndMediaLimits(t *testing.T) {
	cfg := FromEnv()
	cfg.StatusMaxChars = 0
	cfg.MaxMedia = 0
	cfg.ImageSizeLimit = 0
	cfg.VideoSizeLimit = 0
	cfg.MatrixLimit = 0

	err := cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("ValidateRuntime returned nil")
	}
	message := err.Error()
	for _, want := range []string{
		"STATUS_LENGTH_LIMIT",
		"MAX_MEDIA_ATTACHMENTS",
		"IMAGE_LIMIT_MEGABYTES",
		"VIDEO_LIMIT_MEGABYTES",
		"MAX_ATTACHMENT_MATRIX_LIMIT",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("ValidateRuntime error %q missing %q", message, want)
		}
	}
}

func TestValidateRuntimeRejectsInvalidRedisAndSMTPSettings(t *testing.T) {
	cfg := FromEnv()
	cfg.RedisPort = "70000"
	cfg.RedisDB = "-1"
	cfg.RedisURL = ""
	cfg.SMTPServer = "smtp.example.test"
	cfg.SMTPPort = "bad"
	cfg.SMTPAuthMethod = "unsupported"

	err := cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("ValidateRuntime returned nil")
	}
	message := err.Error()
	for _, want := range []string{"REDIS_PORT", "REDIS_DB", "SMTP_PORT", "SMTP_AUTH_METHOD"} {
		if !strings.Contains(message, want) {
			t.Fatalf("ValidateRuntime error %q missing %q", message, want)
		}
	}

	cfg = FromEnv()
	cfg.RedisPort = "70000"
	cfg.RedisDB = "-1"
	cfg.RedisURL = "redis://redis.example:notaport/nope"
	cfg.SidekiqRedisURL = "redis://sidekiq.example:notaport/nope"
	cfg.CacheRedisURL = "redis://cache.example:notaport/nope"

	err = cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("ValidateRuntime returned nil")
	}
	message = err.Error()
	for _, want := range []string{"REDIS_URL", "SIDEKIQ_REDIS_URL", "CACHE_REDIS_URL"} {
		if !strings.Contains(message, want) {
			t.Fatalf("ValidateRuntime error %q missing %q", message, want)
		}
	}
	for _, notWant := range []string{"REDIS_PORT", "REDIS_DB"} {
		if strings.Contains(message, notWant) {
			t.Fatalf("ValidateRuntime error %q should ignore %q when REDIS_URL is present", message, notWant)
		}
	}
}

func TestValidateRuntimeRejectsInvalidCASConfig(t *testing.T) {
	t.Setenv("CAS_ENABLED", "true")
	t.Setenv("CAS_URL", "")
	t.Setenv("CAS_HOST", "")
	t.Setenv("CAS_LOGIN_URL", "ftp://cas.example.test/login")
	t.Setenv("CAS_VALIDATE_URL", "")
	t.Setenv("CAS_PORT", "bad")
	t.Setenv("CAS_CA_PATH", "/definitely/missing/paon-cas-ca.pem")

	cfg := FromEnv()
	err := cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("ValidateRuntime returned nil")
	}
	for _, want := range []string{"CAS_URL or CAS_HOST", "CAS_LOGIN_URL", "CAS_PORT", "CAS_CA_PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateRuntime error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateRuntimeRejectsInvalidSAMLConfig(t *testing.T) {
	t.Setenv("SAML_ENABLED", "true")
	t.Setenv("SAML_ACS_URL", "not-a-url")
	t.Setenv("SAML_ISSUER", "")
	t.Setenv("SAML_IDP_SSO_TARGET_URL", "ftp://idp.example.test/sso")
	t.Setenv("SAML_IDP_SSO_TARGET_PARAMS", "broken")
	t.Setenv("SAML_ALLOWED_CLOCK_DRIFT", "-1")

	cfg := FromEnv()
	err := cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("ValidateRuntime returned nil")
	}
	for _, want := range []string{"SAML_ACS_URL", "SAML_ISSUER", "SAML_IDP_SSO_TARGET_URL", "SAML_IDP_SSO_TARGET_PARAMS", "SAML_ALLOWED_CLOCK_DRIFT"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateRuntime error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateRuntimeAcceptsOIDCDiscoveryConfig(t *testing.T) {
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_DISCOVERY", "true")
	t.Setenv("OIDC_SCOPE", "openid,email")
	t.Setenv("OIDC_UID_FIELD", "sub")
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_REDIRECT_URI", "https://example.test/auth/auth/openid_connect/callback")

	cfg := FromEnv()
	if err := cfg.ValidateRuntime(); err != nil {
		t.Fatalf("ValidateRuntime: %v", err)
	}
}

func TestFromEnvLDAPPortMatchesRailsToI(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 0},
		{"bad", 0},
		{"12bad", 12},
		{"  +9px", 9},
		{"-1", -1},
	} {
		t.Setenv("LDAP_ENABLED", "true")
		t.Setenv("LDAP_BASE", "dc=example,dc=test")
		t.Setenv("LDAP_BIND_DN", "cn=admin,dc=example,dc=test")
		t.Setenv("LDAP_PASSWORD", "secret")
		t.Setenv("LDAP_PORT", tc.raw)
		cfg := FromEnv()
		if cfg.LDAPPort != tc.want || !cfg.LDAPPortSet {
			t.Fatalf("LDAP_PORT=%q parsed as %d set=%v, want %d set=true", tc.raw, cfg.LDAPPort, cfg.LDAPPortSet, tc.want)
		}
		if err := cfg.ValidateRuntime(); err != nil && strings.Contains(err.Error(), "LDAP_PORT") {
			t.Fatalf("explicit Rails-style LDAP_PORT=%q should not be rejected: %v", tc.raw, err)
		}
	}
}

func TestValidateRuntimeRejectsInvalidOIDCURLs(t *testing.T) {
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_SCOPE", "openid,email")
	t.Setenv("OIDC_UID_FIELD", "sub")
	t.Setenv("OIDC_CLIENT_ID", "client")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("OIDC_REDIRECT_URI", "not-a-url")
	t.Setenv("OIDC_AUTH_ENDPOINT", "https://idp.example.test/auth")
	t.Setenv("OIDC_TOKEN_ENDPOINT", "https://idp.example.test/token")
	t.Setenv("OIDC_USER_INFO_ENDPOINT", "ftp://idp.example.test/userinfo")
	t.Setenv("OIDC_JWKS_URI", "https://idp.example.test/jwks")

	cfg := FromEnv()
	err := cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("ValidateRuntime returned nil")
	}
	for _, want := range []string{"OIDC_REDIRECT_URI", "OIDC_USER_INFO_ENDPOINT"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateRuntime error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateRuntimeRejectsInvalidHTTPProxySettings(t *testing.T) {
	for _, tt := range []struct {
		name   string
		update func(*Config)
		want   string
	}{
		{name: "unsupported normal proxy scheme", update: func(c *Config) { c.HTTPProxyURL = "socks5://proxy.example.test:1080" }, want: "http_proxy"},
		{name: "missing normal proxy host", update: func(c *Config) { c.HTTPProxyURL = "http:///missing" }, want: "http_proxy"},
		{name: "padded normal proxy URL", update: func(c *Config) { c.HTTPProxyURL = " http://proxy.example.test:8080 " }, want: "http_proxy"},
		{name: "unsupported hidden proxy scheme", update: func(c *Config) { c.HTTPHiddenProxyURL = "ftp://proxy.example.test" }, want: "http_hidden_proxy"},
		{name: "padded hidden proxy URL", update: func(c *Config) { c.HTTPHiddenProxyURL = " https://proxy.example.test:8443 " }, want: "http_hidden_proxy"},
		{name: "invalid hidden proxy port", update: func(c *Config) { c.HTTPHiddenProxyURL = "https://proxy.example.test:70000" }, want: "http_hidden_proxy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := FromEnv()
			tt.update(&cfg)
			err := cfg.ValidateRuntime()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateRuntime error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateRuntimeAllowsBlankHTTPProxyLikeRailsPresent(t *testing.T) {
	cfg := FromEnv()
	cfg.HTTPProxyURL = "   "
	cfg.HTTPHiddenProxyURL = "\t"
	if err := cfg.ValidateRuntime(); err != nil && (strings.Contains(err.Error(), "http_proxy") || strings.Contains(err.Error(), "http_hidden_proxy")) {
		t.Fatalf("blank proxy env should be disabled by Rails present? semantics, got %v", err)
	}
}

func TestValidateRuntimeRejectsInvalidAllowedPrivateAddresses(t *testing.T) {
	cfg := FromEnv()
	cfg.AllowedPrivateAddresses = "10.0.0.0/8, not-an-ip"
	err := cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "ALLOWED_PRIVATE_ADDRESSES") {
		t.Fatalf("ValidateRuntime error = %v", err)
	}
}

func TestValidateRuntimeRejectsInvalidTrustedProxyIP(t *testing.T) {
	cfg := FromEnv()
	cfg.TrustedProxyIP = "203.0.113.10, not-an-ip"
	err := cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXY_IP") {
		t.Fatalf("ValidateRuntime error = %v", err)
	}
}

func TestValidateRuntimeAcceptsRedisURLForms(t *testing.T) {
	for _, raw := range []string{
		"redis://redis.example.test:6379/2",
		"rediss://:secret@redis.example.test/0",
		"unix:///tmp/redis.sock",
	} {
		cfg := FromEnv()
		cfg.RedisURL = raw
		if err := cfg.ValidateRuntime(); err != nil {
			t.Fatalf("ValidateRuntime(%q): %v", raw, err)
		}
	}
}

func TestValidateRuntimeRejectsInvalidRedisURLs(t *testing.T) {
	for _, raw := range []string{
		"http://redis.example.test:6379/0",
		"redis://redis.example.test/not-a-db",
		"redis://redis.example.test:70000/0",
		"unix://",
	} {
		cfg := FromEnv()
		cfg.RedisURL = raw
		err := cfg.ValidateRuntime()
		if err == nil || !strings.Contains(err.Error(), "REDIS_URL") {
			t.Fatalf("ValidateRuntime(%q) error = %v", raw, err)
		}
	}
}

func TestValidateRuntimeAllowsBlankSMTPPortWhenSMTPDisabled(t *testing.T) {
	cfg := FromEnv()
	cfg.SMTPServer = ""
	cfg.SMTPPort = ""
	if err := cfg.ValidateRuntime(); err != nil {
		t.Fatalf("ValidateRuntime: %v", err)
	}
}

func TestValidateRuntimeRejectsEnabledFeatureMisconfigurations(t *testing.T) {
	cfg := FromEnv()
	cfg.DatabaseMaxOpenConns = 0
	cfg.StatusMaxChars = 0
	cfg.RedisURL = ""
	cfg.RedisPort = "bad"
	cfg.MeiliEnabled = true
	cfg.MeiliHost = "localhost:7700"
	cfg.MeiliMasterKey = ""
	cfg.CloudflareTurnstileEnabled = true
	cfg.CloudflareTurnstileSiteKey = ""
	cfg.CloudflareTurnstileSecretKey = ""
	cfg.DynamoDBEnabled = true
	cfg.DynamoDBAccessKey = "access"
	cfg.DynamoDBSecretKey = "secret"
	cfg.DynamoDBNamespace = ""
	cfg.S3Enabled = true
	cfg.S3Endpoint = "localhost:9000"
	cfg.VapidPublicKey = "public"
	cfg.VapidPrivateKey = ""

	err := cfg.ValidateRuntime()
	if err == nil {
		t.Fatal("ValidateRuntime returned nil")
	}
	message := err.Error()
	for _, want := range []string{
		"DB_POOL",
		"STATUS_LENGTH_LIMIT",
		"REDIS_PORT",
		"MEILI_HOST",
		"CLOUDFLARE_TURNSTILE_SITE_KEY",
		"CLOUDFLARE_TURNSTILE_SECRET_KEY",
		"DYNAMODB_NAMESPACE",
		"S3_ENDPOINT",
		"VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("ValidateRuntime error %q missing %q", message, want)
		}
	}
}

func TestValidateRuntimeUsesAWSSDKConfigurationContracts(t *testing.T) {
	cfg := FromEnv()
	cfg.S3Enabled = true
	cfg.S3Bucket = "media-bucket"
	cfg.S3Region = "ap-northeast-1"
	cfg.S3SignatureVersion = "v4"
	cfg.S3AccessKeyID = ""
	cfg.S3SecretAccessKey = ""
	cfg.DynamoDBEnabled = true
	cfg.DynamoDBNamespace = "paon-test"
	cfg.DynamoDBRegion = "ap-northeast-1"
	cfg.DynamoDBAccessKey = ""
	cfg.DynamoDBSecretKey = ""
	if err := cfg.ValidateRuntime(); err != nil {
		t.Fatalf("AWS SDK default credential chain should be valid: %v", err)
	}

	cfg.S3SignatureVersion = "v2"
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "AWS SDK for Go v2 uses SigV4") {
		t.Fatalf("S3_SIGNATURE_VERSION=v2 error = %v", err)
	}
	cfg.S3SignatureVersion = "v4"
	cfg.S3AccessKeyID = "access-only"
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("partial S3 credentials error = %v", err)
	}
	cfg.S3AccessKeyID = ""
	cfg.DynamoDBSecretKey = "secret-only"
	if err := cfg.ValidateRuntime(); err == nil || !strings.Contains(err.Error(), "DYNAMODB_AWS_ACCESS_KEY_ID") {
		t.Fatalf("partial DynamoDB credentials error = %v", err)
	}
}

func TestRuntimeWarningsReportDropInCompatibilityGaps(t *testing.T) {
	cfg := FromEnv()
	cfg.SecretKeyBase = ""
	cfg.OTPSecret = ""
	cfg.VapidPublicKey = ""
	cfg.VapidPrivateKey = ""
	cfg.DynamoDBEnabled = true
	cfg.DynamoDBAccessKey = ""
	cfg.DynamoDBSecretKey = ""
	cfg.S3Enabled = true
	cfg.S3Bucket = ""
	cfg.S3AccessKeyID = ""
	cfg.S3SecretAccessKey = ""
	cfg.AzureEnabled = true
	cfg.AzureStorageAccount = ""
	cfg.AzureStorageAccessKey = ""
	cfg.AzureContainerName = ""
	cfg.SwiftEnabled = true
	cfg.SwiftObjectURL = ""
	cfg.SwiftContainer = ""
	cfg.SwiftUsername = ""
	cfg.SwiftPassword = ""
	cfg.SwiftAuthURL = ""
	cfg.SwiftProjectID = ""
	cfg.SwiftTenant = ""
	cfg.CASEnabled = true
	cfg.CASDisableSSLVerification = true
	cfg.SAMLEnabled = true
	cfg.SAMLSecurityWantAssertionsEncrypted = true
	cfg.SAMLIDPCertFingerprintValidator = "validator"
	cfg.SAMLCertificate = "-----BEGIN CERTIFICATE-----"
	cfg.SAMLPrivateKey = ""

	warnings := strings.Join(cfg.RuntimeWarnings(), "\n")
	for _, want := range []string{
		"SECRET_KEY_BASE",
		"OTP_SECRET",
		"ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY",
		"ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT",
		"ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY",
		"VAPID_PUBLIC_KEY/VAPID_PRIVATE_KEY",
		"DYNAMODB_ENABLED=true",
		"S3_ENABLED=true",
		"AZURE_ENABLED=true",
		"SWIFT_ENABLED=true",
		"CAS_DISABLE_SSL_VERIFICATION=true",
		"SAML_SECURITY_WANT_ASSERTION_ENCRYPTED=true requires SAML_PRIVATE_KEY",
		"SAML_IDP_CERT_FINGERPRINT_VALIDATOR is treated as a Rails String#[] fingerprint allowlist",
		"SAML_CERT is configured without SAML_PRIVATE_KEY",
	} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("RuntimeWarnings %q missing %q", warnings, want)
		}
	}
}

func TestRuntimeWarningsReportMissingMediaTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	warnings := strings.Join((Config{}).RuntimeWarnings(), "\n")
	for _, want := range []string{"ffprobe is not installed", "ffmpeg is not installed", "HEIC/HEIF/AVIF JPEG conversion", "audio cover-art extraction", "video/audio transcoding"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("RuntimeWarnings %q missing %q warning", warnings, want)
		}
	}
}

func TestRuntimeWarningsPreserveRailsRawFFmpegEnvBoundary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	cfg := Config{FFmpegBinarySet: true, FFprobeBinarySet: true}
	warnings := strings.Join(cfg.RuntimeWarnings(), "\n")
	if strings.Contains(warnings, "ffmpeg is not installed") || strings.Contains(warnings, "ffprobe is not installed") {
		t.Fatalf("explicit blank ffmpeg env should not fall back to default tool names: %q", warnings)
	}
	if !strings.Contains(warnings, " is not installed") {
		t.Fatalf("explicit blank ffmpeg env should still warn about unusable raw command: %q", warnings)
	}

	cfg = Config{
		FFmpegBinary:     " /opt/bin/ffmpeg with spaces ",
		FFmpegBinarySet:  true,
		FFprobeBinary:    " /opt/bin/ffprobe with spaces ",
		FFprobeBinarySet: true,
	}
	warnings = strings.Join(cfg.RuntimeWarnings(), "\n")
	for _, want := range []string{" /opt/bin/ffmpeg with spaces  is not installed", " /opt/bin/ffprobe with spaces  is not installed"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("RuntimeWarnings should preserve raw ffmpeg env value; missing %q in %q", want, warnings)
		}
	}
}

func TestRedisSentinelConfigInheritsDataCredentialsAndHonorsExplicitBlank(t *testing.T) {
	t.Setenv("REDIS_USER", "data-user")
	t.Setenv("REDIS_PASSWORD", "data-password")
	t.Setenv("REDIS_SENTINEL_MASTER", "mymaster")
	t.Setenv("REDIS_SENTINELS", "sentinel-one,sentinel-two:26380")

	got := redisSentinelConfigFromEnv("", RedisSentinelConfig{})
	if got.MasterName != "mymaster" || strings.Join(got.Addresses, ",") != "sentinel-one:26379,sentinel-two:26380" || got.Username != "data-user" || got.Password != "data-password" {
		t.Fatalf("sentinel config = %#v", got)
	}

	t.Setenv("REDIS_SENTINEL_USERNAME", "")
	t.Setenv("REDIS_SENTINEL_PASSWORD", "")
	got = redisSentinelConfigFromEnv("", RedisSentinelConfig{})
	if got.Username != "" || got.Password != "" {
		t.Fatalf("explicit blank sentinel credentials = %#v", got)
	}
}

func TestRoleRedisPartialConfigurationFallsBackAsAWhole(t *testing.T) {
	fallback := RedisSentinelConfig{MasterName: "base", Addresses: []string{"base-sentinel:26379"}, Username: "base-user"}
	t.Setenv("CACHE_REDIS_PASSWORD", "partial-password")
	if got := redisSentinelConfigFromEnv("CACHE", fallback); got.MasterName != fallback.MasterName || got.Username != fallback.Username {
		t.Fatalf("partial cache sentinel config = %#v, want fallback %#v", got, fallback)
	}

	t.Setenv("CACHE_REDIS_HOST", "cache.example")
	if got := redisSentinelConfigFromEnv("CACHE", fallback); got.MasterName != "" || len(got.Addresses) != 0 {
		t.Fatalf("complete direct cache Redis should not inherit sentinel config: %#v", got)
	}
}

func TestS3BatchSettingsRejectExplicitInvalidValues(t *testing.T) {
	t.Setenv("S3_BATCH_DELETE_LIMIT", "not-a-number")
	t.Setenv("S3_BATCH_DELETE_RETRY", "0")
	cfg := FromEnv()
	err := cfg.ValidateRuntime()
	if err == nil || !strings.Contains(err.Error(), "S3_BATCH_DELETE_LIMIT") || !strings.Contains(err.Error(), "S3_BATCH_DELETE_RETRY") {
		t.Fatalf("ValidateRuntime error = %v", err)
	}
}

func TestS3ObjectKeyAppliesPrefixExactlyOncePerBoundary(t *testing.T) {
	cfg := Config{S3KeyPrefix: "tenant/production"}
	if got := cfg.S3ObjectKey("/media/file.png"); got != "tenant/production/media/file.png" {
		t.Fatalf("S3ObjectKey = %q", got)
	}
	if got := (Config{Scheme: "https", LocalDomain: "example.test", S3Enabled: true, S3KeyPrefix: "tenant"}).SystemAssetURL("media/file.png"); got != "https://example.test/system/tenant/media/file.png" {
		t.Fatalf("SystemAssetURL = %q", got)
	}
}

func TestRuntimeWarningsReportsDeprecatedRedisNamespace(t *testing.T) {
	warnings := strings.Join((Config{RedisNamespace: "mastodon:"}).RuntimeWarnings(), "\n")
	if !strings.Contains(warnings, "REDIS_NAMESPACE is deprecated") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestRuntimeWarningsReportsLibvipsBuildTimeSelection(t *testing.T) {
	t.Setenv("MASTODON_USE_LIBVIPS", "true")
	warnings := strings.Join((Config{}).RuntimeWarnings(), "\n")
	if !strings.Contains(warnings, "MASTODON_USE_LIBVIPS is not a runtime selector") || !strings.Contains(warnings, "PAON_IMAGE_PROCESSOR") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestFromEnvReadsSelfDestructWithoutTreatingWhitespaceAsEnabled(t *testing.T) {
	t.Setenv("SELF_DESTRUCT", " signed-token ")
	if got := FromEnv().SelfDestruct; got != " signed-token " {
		t.Fatalf("SelfDestruct = %q", got)
	}
	t.Setenv("SELF_DESTRUCT", "   ")
	if got := FromEnv().SelfDestruct; got != "" {
		t.Fatalf("blank SelfDestruct = %q", got)
	}
}
