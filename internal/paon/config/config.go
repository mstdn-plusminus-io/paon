package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	webpush "github.com/SherClockHolmes/webpush-go"
	"gopkg.in/yaml.v3"
)

type Config struct {
	RailsEnv                                string
	RailsLogLevel                           string
	ProcessRole                             string
	ListenAddr                              string
	ListenNetwork                           string
	ProxyProtocolV1                         bool
	PersistentTimeout                       time.Duration
	DatabaseURL                             string
	ReplicaDatabaseURL                      string
	PgHeroStatsDatabaseURL                  string
	PgHeroOtherDatabaseURL                  string
	DatabaseMaxOpenConns                    int
	DatabaseMaxIdleConns                    int
	DatabasePreparedStatements              bool
	DatabaseLockTimeout                     time.Duration
	PublicDir                               string
	ShakapackerDevServerPublic              string
	ShakapackerDevServerHTTPS               bool
	CDNHost                                 string
	StorageHost                             string
	CSPMediaHost                            string
	PaperclipRootPath                       string
	PaperclipRootPathSet                    bool
	PaperclipRootURL                        string
	PaperclipRootURLSet                     bool
	SendfileHeader                          string
	S3Enabled                               bool
	S3Bucket                                string
	S3Endpoint                              string
	S3Region                                string
	S3RegionSet                             bool
	S3Protocol                              string
	S3ProtocolSet                           bool
	S3Hostname                              string
	S3HostnameSet                           bool
	S3OverridePathStyle                     bool
	S3AccessKeyID                           string
	S3SecretAccessKey                       string
	S3SessionToken                          string
	S3Permission                            string
	S3StorageClass                          string
	S3StorageClassSet                       bool
	S3MultipartThreshold                    int
	S3MultipartThresholdSet                 bool
	S3SignatureVersion                      string
	S3OpenTimeout                           int
	S3ReadTimeout                           int
	S3ForceSingleRequest                    bool
	S3EnableChecksumMode                    bool
	S3KeyPrefix                             string
	S3RetryLimit                            int
	S3RetryLimitSet                         bool
	S3BatchDeleteLimit                      int
	S3BatchDeleteLimitSet                   bool
	S3BatchDeleteRetry                      int
	S3BatchDeleteRetrySet                   bool
	SwiftEnabled                            bool
	SwiftContainer                          string
	SwiftObjectURL                          string
	SwiftTempURLKey                         string
	SwiftUsername                           string
	SwiftProjectID                          string
	SwiftTenant                             string
	SwiftPassword                           string
	SwiftAuthURL                            string
	SwiftDomainName                         string
	SwiftDomainNameSet                      bool
	SwiftRegion                             string
	SwiftCacheTTL                           string
	SwiftCacheTTLSet                        bool
	AzureEnabled                            bool
	AzureStorageAccount                     string
	AzureStorageAccessKey                   string
	AzureContainerName                      string
	AzureAliasHost                          string
	CacheBusterEnabled                      bool
	CacheBusterSecretHeader                 string
	CacheBusterSecret                       string
	CacheBusterHTTPMethod                   string
	LocalDomain                             string
	WebDomain                               string
	AlternateDomains                        []string
	RailsDevelopmentHosts                   []string
	Scheme                                  string
	ForceSSL                                bool
	StreamingAPIBaseURL                     string
	StreamingAPIBaseURLSet                  bool
	Title                                   string
	Version                                 string
	MastodonVersion                         string
	SourceURL                               string
	Repository                              string
	MeiliEnabled                            bool
	MeiliHost                               string
	MeiliMasterKey                          string
	MeiliPrefix                             string
	MeiliLibraryOnly                        bool
	RedisURL                                string
	SidekiqRedisURL                         string
	CacheRedisURL                           string
	RedisHost                               string
	RedisPort                               string
	RedisPassword                           string
	RedisDB                                 string
	RedisNamespace                          string
	RedisSentinel                           RedisSentinelConfig
	SidekiqRedisSentinel                    RedisSentinelConfig
	CacheRedisSentinel                      RedisSentinelConfig
	SidekiqConcurrency                      int
	AsynqQueues                             []string
	WorkerReadyFilename                     string
	StatsDAddr                              string
	StatsDNamespace                         string
	StatsDSidekiq                           bool
	OpenTelemetryEnabled                    bool
	OpenTelemetryTracesEnabled              bool
	OpenTelemetryMetricsEnabled             bool
	OTelServiceNamePrefix                   string
	OTelServiceNameSeparator                string
	OTelExporterOTLPEndpoint                string
	OTelExporterOTLPTracesEndpoint          string
	OTelExporterOTLPMetricsEndpoint         string
	OTelExporterOTLPHeaders                 string
	OTelExporterOTLPTracesHeaders           string
	OTelExporterOTLPMetricsHeaders          string
	OTelExporterOTLPProtocol                string
	OTelExporterOTLPTracesProtocol          string
	OTelExporterOTLPMetricsProtocol         string
	OTelTracesSampler                       string
	OTelTracesSamplerArg                    string
	OTelPropagators                         []string
	VapidPublicKey                          string
	VapidPrivateKey                         string
	VapidSubject                            string
	SMTPServer                              string
	SMTPPort                                string
	SMTPLogin                               string
	SMTPPassword                            string
	SMTPFrom                                string
	SMTPDomain                              string
	SMTPDomainSet                           bool
	SMTPAuthMethod                          string
	SMTPReplyTo                             string
	SMTPReturnPath                          string
	SMTPCAFile                              string
	SMTPOpenSSLVerifyMode                   string
	SMTPDeliveryMethod                      string
	SMTPTLS                                 bool
	SMTPStartTLS                            bool
	SMTPStartTLSRequired                    bool
	FFmpegBinary                            string
	FFmpegBinarySet                         bool
	FFprobeBinary                           string
	FFprobeBinarySet                        bool
	MaxSessionActivations                   int
	MaxRequestPoolSize                      int
	MaxRequestPoolSizeSet                   bool
	CASEnabled                              bool
	CASDisplayName                          string
	CASURL                                  string
	CASHost                                 string
	CASPort                                 string
	CASSL                                   bool
	CASValidateURL                          string
	CASCallbackURL                          string
	CASLogoutURL                            string
	CASLoginURL                             string
	CASUIDField                             string
	CASCAPath                               string
	CASDisableSSLVerification               bool
	CASUIDKey                               string
	CASNameKey                              string
	CASEmailKey                             string
	CASNicknameKey                          string
	CASFirstNameKey                         string
	CASLastNameKey                          string
	CASLocationKey                          string
	CASImageKey                             string
	CASPhoneKey                             string
	CASSecurityAssumeEmailVerified          bool
	SAMLEnabled                             bool
	SAMLDisplayName                         string
	SAMLACSURL                              string
	SAMLIssuer                              string
	SAMLIDPSSOTargetURL                     string
	SAMLIDPSSOTargetParams                  string
	SAMLIDPCert                             string
	SAMLIDPCertFingerprint                  string
	SAMLIDPCertFingerprintValidator         string
	SAMLNameIdentifierFormat                string
	SAMLCertificate                         string
	SAMLPrivateKey                          string
	SAMLSecurityWantAssertionsSigned        bool
	SAMLSecurityWantAssertionsEncrypted     bool
	SAMLSecurityAssumeEmailVerified         bool
	SAMLAttributeUID                        string
	SAMLAttributeEmail                      string
	SAMLAttributeFullName                   string
	SAMLAttributeFirstName                  string
	SAMLAttributeLastName                   string
	SAMLAttributeVerified                   string
	SAMLAttributeVerifiedEmail              string
	SAMLUIDAttribute                        string
	SAMLAllowedClockDrift                   string
	OIDCEnabled                             bool
	OIDCDiscovery                           bool
	OIDCIssuer                              string
	OIDCDisplayName                         string
	OIDCScope                               string
	OIDCUIDField                            string
	OIDCClientID                            string
	OIDCClientSecret                        string
	OIDCRedirectURI                         string
	OIDCHTTPScheme                          string
	OIDCHost                                string
	OIDCPort                                string
	OIDCAuthEndpoint                        string
	OIDCTokenEndpoint                       string
	OIDCUserInfoEndpoint                    string
	OIDCJWKSURI                             string
	OIDCEndSessionEndpoint                  string
	OIDCPostLogoutRedirectURI               string
	OIDCResponseType                        string
	OIDCResponseMode                        string
	OIDCResponseModeSet                     bool
	OIDCDisplay                             string
	OIDCDisplaySet                          bool
	OIDCPrompt                              string
	OIDCPromptSet                           bool
	OIDCSendNonce                           bool
	OIDCSendScopeToTokenEndpoint            bool
	OIDCUsePKCE                             bool
	OIDCClientAuthMethod                    string
	OIDCSecurityAssumeEmailVerified         bool
	PAMEnabled                              bool
	PAMEmailDomain                          string
	PAMDefaultService                       string
	PAMControlledService                    string
	PAMAuthCommand                          string
	LDAPEnabled                             bool
	LDAPHost                                string
	LDAPPort                                int
	LDAPPortSet                             bool
	LDAPMethod                              string
	LDAPBase                                string
	LDAPBindDN                              string
	LDAPPassword                            string
	LDAPUID                                 string
	LDAPMail                                string
	LDAPTLSNoVerify                         bool
	LDAPSearchFilter                        string
	LDAPUIDConversionEnabled                bool
	LDAPUIDConversionSearch                 string
	LDAPUIDConversionReplace                string
	DeepLAPIKey                             string
	DeepLPlan                               string
	DeepLPlanSet                            bool
	LibreTranslateEndpoint                  string
	LibreTranslateAPIKey                    string
	LibreTranslateAPIKeySet                 bool
	CloudflareTurnstileEnabled              bool
	CloudflareTurnstileSiteKey              string
	CloudflareTurnstileSecretKey            string
	HCaptchaSiteKey                         string
	HCaptchaSecretKey                       string
	OTPSecret                               string
	OTPSecretSet                            bool
	ActiveRecordEncryptionDeterministicKey  string
	ActiveRecordEncryptionKeyDerivationSalt string
	ActiveRecordEncryptionPrimaryKey        string
	SecretKeyBase                           string
	SelfDestruct                            string
	DefaultLocale                           string
	DefaultLocaleSet                        bool
	StatusMaxChars                          int
	StatusMaxCharsSet                       bool
	MaxMedia                                int
	MaxMediaSet                             bool
	MaxFollowsThreshold                     int
	MaxFollowsThresholdSet                  bool
	MaxFollowsRatio                         float64
	MaxFollowsRatioSet                      bool
	ImageSizeLimit                          int
	ImageSizeLimitSet                       bool
	VideoSizeLimit                          int
	VideoSizeLimitSet                       bool
	MatrixLimit                             int
	MatrixLimitSet                          bool
	DisableRemoteMediaCache                 bool
	DisableRemoteMediaCacheSet              bool
	SingleUserMode                          bool
	LimitedFederationMode                   bool
	DynamoDBEnabled                         bool
	DynamoDBAccessKey                       string
	DynamoDBSecretKey                       string
	DynamoDBSessionToken                    string
	DynamoDBRegion                          string
	DynamoDBRegionSet                       bool
	DynamoDBNamespace                       string
	DynamoDBEndpoint                        string
	SSORedirect                             string
	SSOFormActionURL                        string
	SSOAccountSignUpURL                     string
	SSOAccountSignUpURLSet                  bool
	SSOAccountSettingsURL                   string
	OmniAuthOnly                            bool
	DisableSignupByAPI                      bool
	DisallowUnauthenticatedAPIAccess        bool
	AuthorizedFetch                         bool
	AuthorizedFetchEnvSet                   bool
	DisableAutoSwitchingRegistrations       bool
	EmailDomainListsApplyAfterConfirm       bool
	SuspiciousSignInDisabled                bool
	UpdateCheckURL                          string
	HTTPProxyURL                            string
	HTTPHiddenProxyURL                      string
	TrustedProxyIP                          string
	AllowAccessToHiddenService              bool
	AllowedPrivateAddresses                 string
}

// RedisSentinelConfig is the connection metadata which cannot be represented
// by a redis:// URL. The data Redis credentials remain in the corresponding
// RedisURL while these credentials are used only to discover the current
// primary through Sentinel.
type RedisSentinelConfig struct {
	MasterName string
	Addresses  []string
	Username   string
	Password   string
}

const defaultRepository = "mstdn-plusminus-io/paon"

func VersionFromEnv() string {
	if version := firstNonEmpty(os.Getenv("PAON_VERSION")); version != "" {
		return version
	}
	return versionWithRailsMetadata(DefaultVersion, DefaultPrerelease, "PAON_VERSION_PRERELEASE", "PAON_VERSION_METADATA")
}

func MastodonVersionFromEnv() string {
	if version := firstNonEmpty(os.Getenv("MASTODON_VERSION")); version != "" {
		return version
	}
	return versionWithRailsMetadata(DefaultMastodonVersion, "", "MASTODON_VERSION_PRERELEASE", "MASTODON_VERSION_METADATA")
}

func versionWithRailsMetadata(base string, defaultPrerelease string, prereleaseEnv string, metadataEnv string) string {
	version := strings.TrimSpace(base)
	prerelease := railsPresenceEnv(prereleaseEnv)
	if prerelease == "" {
		prerelease = strings.TrimSpace(defaultPrerelease)
	}
	if prerelease != "" {
		version += "-" + prerelease
	}
	if metadata := railsPresenceEnv(metadataEnv); metadata != "" {
		version += "+" + metadata
	}
	return version
}

func repositoryFromEnv() string {
	return envOrDefault("GITHUB_REPOSITORY", defaultRepository)
}

func sourceURLFromEnv() string {
	if sourceURL, ok := os.LookupEnv("SOURCE_URL"); ok {
		return sourceURL
	}
	baseURL := strings.TrimRight(envOrDefault("SOURCE_BASE_URL", "https://github.com/"+repositoryFromEnv()), "/")
	if tag, ok := os.LookupEnv("SOURCE_TAG"); ok {
		return baseURL + "/tree/" + tag
	}
	return baseURL
}

func FromEnv() Config {
	localDomain := envOrDefault("LOCAL_DOMAIN", railsDefaultLocalDomain())
	webDomain := envOrDefault("WEB_DOMAIN", localDomain)
	publicDir := firstNonEmpty(os.Getenv("PAON_PUBLIC_DIR"), "public")
	cacheBusterSecretHeader, cacheBusterSecret, cacheBusterHTTPMethod := cacheBusterConfigFromEnv()
	_, disableRemoteMediaCacheSet := os.LookupEnv("DISABLE_REMOTE_MEDIA_CACHE")
	_, s3MultipartThresholdSet := os.LookupEnv("S3_MULTIPART_THRESHOLD")
	production := railsProductionEnv()
	scheme := schemeFromEnv(production)

	defaultLocale, defaultLocaleSet := defaultLocaleFromRailsEnv()

	redisSentinel := redisSentinelConfigFromEnv("", RedisSentinelConfig{})
	redisURL := railsRedisURLFromEnv("", "", true)
	sidekiqRedisSentinel := redisSentinelConfigFromEnv("SIDEKIQ", redisSentinel)
	cacheRedisSentinel := redisSentinelConfigFromEnv("CACHE", redisSentinel)
	vapidPublicKey, vapidPrivateKey := railsVapidKeysFromEnv(production)
	otelEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otelTracesEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	otelMetricsEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"))
	otelTracesEnabled := otelEndpoint != "" || otelTracesEndpoint != ""
	otelMetricsEnabled := otelEndpoint != "" || otelMetricsEndpoint != ""

	return Config{
		RailsEnv:                                railsEnvName(),
		RailsLogLevel:                           railsLogLevelFromEnv(),
		ProcessRole:                             paonProcessRoleFromEnv(),
		ListenAddr:                              listenAddrFromEnv(),
		ListenNetwork:                           listenNetworkFromEnv(),
		ProxyProtocolV1:                         os.Getenv("PROXY_PROTO_V1") == "true",
		PersistentTimeout:                       time.Duration(railsIntFromEnv("PERSISTENT_TIMEOUT", 20)) * time.Second,
		DatabaseURL:                             firstNonEmpty(os.Getenv("DATABASE_URL"), databaseURLFromRailsEnv()),
		ReplicaDatabaseURL:                      replicaDatabaseURLFromRailsEnv(),
		PgHeroStatsDatabaseURL:                  os.Getenv("PGHERO_STATS_DATABASE_URL"),
		PgHeroOtherDatabaseURL:                  os.Getenv("OTHER_DATABASE_URL"),
		DatabaseMaxOpenConns:                    databasePoolFromEnv(),
		DatabaseMaxIdleConns:                    intFromEnv("PAON_DB_MAX_IDLE_CONNS", 5),
		DatabasePreparedStatements:              envDefaultTrue("PREPARED_STATEMENTS"),
		DatabaseLockTimeout:                     databaseLockTimeoutFromEnv(),
		PublicDir:                               publicDir,
		ShakapackerDevServerPublic:              shakapackerDevServerPublicFromEnv(),
		ShakapackerDevServerHTTPS:               os.Getenv("SHAKAPACKER_DEV_SERVER_HTTPS") == "true" || os.Getenv("WEBPACKER_DEV_SERVER_HTTPS") == "true",
		CDNHost:                                 railsPresenceEnv("CDN_HOST"),
		StorageHost:                             storageHostFromEnv(),
		CSPMediaHost:                            cspMediaHostFromEnv(scheme),
		PaperclipRootPath:                       paperclipRootPathFromEnv(publicDir),
		PaperclipRootPathSet:                    envIsSet("PAPERCLIP_ROOT_PATH"),
		PaperclipRootURL:                        paperclipRootURLFromEnv(),
		PaperclipRootURLSet:                     envIsSet("PAPERCLIP_ROOT_URL"),
		SendfileHeader:                          os.Getenv("SENDFILE_HEADER"),
		S3Enabled:                               os.Getenv("S3_ENABLED") == "true",
		S3Bucket:                                os.Getenv("S3_BUCKET"),
		S3Endpoint:                              os.Getenv("S3_ENDPOINT"),
		S3Region:                                envOrDefault("S3_REGION", "us-east-1"),
		S3RegionSet:                             envIsSet("S3_REGION"),
		S3Protocol:                              envOrDefault("S3_PROTOCOL", "https"),
		S3ProtocolSet:                           envIsSet("S3_PROTOCOL"),
		S3Hostname:                              s3HostnameFromEnv(),
		S3HostnameSet:                           envIsSet("S3_HOSTNAME"),
		S3OverridePathStyle:                     os.Getenv("S3_OVERRIDE_PATH_STYLE") == "true",
		S3AccessKeyID:                           os.Getenv("AWS_ACCESS_KEY_ID"),
		S3SecretAccessKey:                       os.Getenv("AWS_SECRET_ACCESS_KEY"),
		S3SessionToken:                          os.Getenv("AWS_SESSION_TOKEN"),
		S3Permission:                            s3PermissionFromEnv(),
		S3StorageClass:                          os.Getenv("S3_STORAGE_CLASS"),
		S3StorageClassSet:                       envIsSet("S3_STORAGE_CLASS"),
		S3MultipartThreshold:                    railsIntFromEnv("S3_MULTIPART_THRESHOLD", 15*1024*1024),
		S3MultipartThresholdSet:                 s3MultipartThresholdSet,
		S3SignatureVersion:                      envOrDefault("S3_SIGNATURE_VERSION", "v4"),
		S3OpenTimeout:                           railsIntFromEnv("S3_OPEN_TIMEOUT", 5),
		S3ReadTimeout:                           railsIntFromEnv("S3_READ_TIMEOUT", 5),
		S3ForceSingleRequest:                    os.Getenv("S3_FORCE_SINGLE_REQUEST") == "true",
		S3EnableChecksumMode:                    os.Getenv("S3_ENABLE_CHECKSUM_MODE") == "true",
		S3KeyPrefix:                             strings.Trim(strings.TrimSpace(os.Getenv("S3_KEY_PREFIX")), "/"),
		S3RetryLimit:                            strictIntFromEnv("S3_RETRY_LIMIT", 0),
		S3RetryLimitSet:                         envIsSet("S3_RETRY_LIMIT"),
		S3BatchDeleteLimit:                      strictIntFromEnv("S3_BATCH_DELETE_LIMIT", 1000),
		S3BatchDeleteLimitSet:                   envIsSet("S3_BATCH_DELETE_LIMIT"),
		S3BatchDeleteRetry:                      strictIntFromEnv("S3_BATCH_DELETE_RETRY", 3),
		S3BatchDeleteRetrySet:                   envIsSet("S3_BATCH_DELETE_RETRY"),
		SwiftEnabled:                            os.Getenv("SWIFT_ENABLED") == "true",
		SwiftContainer:                          os.Getenv("SWIFT_CONTAINER"),
		SwiftObjectURL:                          os.Getenv("SWIFT_OBJECT_URL"),
		SwiftTempURLKey:                         os.Getenv("SWIFT_TEMP_URL_KEY"),
		SwiftUsername:                           os.Getenv("SWIFT_USERNAME"),
		SwiftProjectID:                          os.Getenv("SWIFT_PROJECT_ID"),
		SwiftTenant:                             os.Getenv("SWIFT_TENANT"),
		SwiftPassword:                           os.Getenv("SWIFT_PASSWORD"),
		SwiftAuthURL:                            os.Getenv("SWIFT_AUTH_URL"),
		SwiftDomainName:                         envOrDefault("SWIFT_DOMAIN_NAME", "default"),
		SwiftDomainNameSet:                      envIsSet("SWIFT_DOMAIN_NAME"),
		SwiftRegion:                             os.Getenv("SWIFT_REGION"),
		SwiftCacheTTL:                           envOrDefault("SWIFT_CACHE_TTL", "60"),
		SwiftCacheTTLSet:                        envIsSet("SWIFT_CACHE_TTL"),
		AzureEnabled:                            os.Getenv("AZURE_ENABLED") == "true",
		AzureStorageAccount:                     os.Getenv("AZURE_STORAGE_ACCOUNT"),
		AzureStorageAccessKey:                   os.Getenv("AZURE_STORAGE_ACCESS_KEY"),
		AzureContainerName:                      os.Getenv("AZURE_CONTAINER_NAME"),
		AzureAliasHost:                          os.Getenv("AZURE_ALIAS_HOST"),
		CacheBusterEnabled:                      os.Getenv("CACHE_BUSTER_ENABLED") == "true",
		CacheBusterSecretHeader:                 cacheBusterSecretHeader,
		CacheBusterSecret:                       cacheBusterSecret,
		CacheBusterHTTPMethod:                   cacheBusterHTTPMethod,
		LocalDomain:                             localDomain,
		WebDomain:                               webDomain,
		AlternateDomains:                        alternateDomainsFromEnv(),
		RailsDevelopmentHosts:                   railsDevelopmentHostsFromEnv(),
		Scheme:                                  firstNonEmpty(os.Getenv("PAON_SCHEME"), scheme),
		ForceSSL:                                production,
		StreamingAPIBaseURL:                     streamingAPIBaseURLFromEnv(localDomain, webDomain, production, scheme),
		StreamingAPIBaseURLSet:                  envIsSet("STREAMING_API_BASE_URL"),
		Title:                                   envOrDefault("LOCAL_DOMAIN", localDomain),
		Version:                                 VersionFromEnv(),
		MastodonVersion:                         MastodonVersionFromEnv(),
		SourceURL:                               sourceURLFromEnv(),
		Repository:                              repositoryFromEnv(),
		MeiliEnabled:                            os.Getenv("MEILI_ENABLED") == "true",
		MeiliHost:                               envOrDefault("MEILI_HOST", "http://localhost:7700"),
		MeiliMasterKey:                          os.Getenv("MEILI_MASTER_KEY"),
		MeiliPrefix:                             meiliPrefixFromEnv(),
		MeiliLibraryOnly:                        os.Getenv("MEILI_LIBRARY_ONLY") == "true",
		RedisURL:                                redisURL,
		SidekiqRedisURL:                         railsRedisURLFromEnv("SIDEKIQ", redisURL, false),
		CacheRedisURL:                           railsRedisURLFromEnv("CACHE", redisURL, false),
		RedisHost:                               envOrDefault("REDIS_HOST", "localhost"),
		RedisPort:                               envOrDefault("REDIS_PORT", "6379"),
		RedisPassword:                           os.Getenv("REDIS_PASSWORD"),
		RedisDB:                                 envOrDefault("REDIS_DB", "0"),
		RedisNamespace:                          redisNamespaceFromEnv(),
		RedisSentinel:                           redisSentinel,
		SidekiqRedisSentinel:                    sidekiqRedisSentinel,
		CacheRedisSentinel:                      cacheRedisSentinel,
		SidekiqConcurrency:                      asynqConcurrencyFromEnv(),
		AsynqQueues:                             asynqQueuesFromEnv(),
		WorkerReadyFilename:                     strings.TrimSpace(os.Getenv("MASTODON_SIDEKIQ_READY_FILENAME")),
		StatsDAddr:                              os.Getenv("STATSD_ADDR"),
		StatsDNamespace:                         envOrDefault("STATSD_NAMESPACE", "Mastodon."+railsEnvName()),
		StatsDSidekiq:                           os.Getenv("STATSD_SIDEKIQ") == "true",
		OpenTelemetryEnabled:                    otelTracesEnabled || otelMetricsEnabled,
		OpenTelemetryTracesEnabled:              otelTracesEnabled,
		OpenTelemetryMetricsEnabled:             otelMetricsEnabled,
		OTelServiceNamePrefix:                   envOrDefault("OTEL_SERVICE_NAME_PREFIX", "mastodon"),
		OTelServiceNameSeparator:                envOrDefault("OTEL_SERVICE_NAME_SEPARATOR", "/"),
		OTelExporterOTLPEndpoint:                otelEndpoint,
		OTelExporterOTLPTracesEndpoint:          otelTracesEndpoint,
		OTelExporterOTLPMetricsEndpoint:         otelMetricsEndpoint,
		OTelExporterOTLPHeaders:                 os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"),
		OTelExporterOTLPTracesHeaders:           os.Getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS"),
		OTelExporterOTLPMetricsHeaders:          os.Getenv("OTEL_EXPORTER_OTLP_METRICS_HEADERS"),
		OTelExporterOTLPProtocol:                envOrDefault("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf"),
		OTelExporterOTLPTracesProtocol:          os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"),
		OTelExporterOTLPMetricsProtocol:         os.Getenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"),
		OTelTracesSampler:                       envOrDefault("OTEL_TRACES_SAMPLER", "parentbased_always_on"),
		OTelTracesSamplerArg:                    os.Getenv("OTEL_TRACES_SAMPLER_ARG"),
		OTelPropagators:                         splitAndTrimCSV(envOrDefault("OTEL_PROPAGATORS", "tracecontext,baggage")),
		VapidPublicKey:                          vapidPublicKey,
		VapidPrivateKey:                         vapidPrivateKey,
		VapidSubject:                            vapidSubjectFromEnv(localDomain),
		SMTPServer:                              os.Getenv("SMTP_SERVER"),
		SMTPPort:                                envOrDefault("SMTP_PORT", ""),
		SMTPLogin:                               railsPresenceEnv("SMTP_LOGIN"),
		SMTPPassword:                            railsPresenceEnv("SMTP_PASSWORD"),
		SMTPFrom:                                envOrDefault("SMTP_FROM_ADDRESS", "notifications@localhost"),
		SMTPDomain:                              envOrDefault("SMTP_DOMAIN", localDomain),
		SMTPDomainSet:                           envIsSet("SMTP_DOMAIN"),
		SMTPAuthMethod:                          envOrDefault("SMTP_AUTH_METHOD", "plain"),
		SMTPReplyTo:                             railsPresenceEnv("SMTP_REPLY_TO"),
		SMTPReturnPath:                          railsPresenceEnv("SMTP_RETURN_PATH"),
		SMTPCAFile:                              smtpCAFileFromEnv(),
		SMTPOpenSSLVerifyMode:                   os.Getenv("SMTP_OPENSSL_VERIFY_MODE"),
		SMTPDeliveryMethod:                      smtpDeliveryMethodFromRailsEnv(),
		SMTPTLS:                                 os.Getenv("SMTP_TLS") == "true" || os.Getenv("SMTP_SSL") == "true",
		SMTPStartTLS:                            smtpStartTLSFromEnv(),
		SMTPStartTLSRequired:                    os.Getenv("SMTP_ENABLE_STARTTLS") == "always",
		FFmpegBinary:                            envOrDefault("FFMPEG_BINARY", "ffmpeg"),
		FFmpegBinarySet:                         envIsSet("FFMPEG_BINARY"),
		FFprobeBinary:                           envOrDefault("FFPROBE_BINARY", "ffprobe"),
		FFprobeBinarySet:                        envIsSet("FFPROBE_BINARY"),
		MaxSessionActivations:                   railsIntFromEnv("MAX_SESSION_ACTIVATIONS", 10),
		MaxRequestPoolSize:                      railsIntFromEnv("MAX_REQUEST_POOL_SIZE", 512),
		MaxRequestPoolSizeSet:                   envIsSet("MAX_REQUEST_POOL_SIZE"),
		CASEnabled:                              os.Getenv("CAS_ENABLED") == "true",
		CASDisplayName:                          os.Getenv("CAS_DISPLAY_NAME"),
		CASURL:                                  os.Getenv("CAS_URL"),
		CASHost:                                 os.Getenv("CAS_HOST"),
		CASPort:                                 os.Getenv("CAS_PORT"),
		CASSL:                                   os.Getenv("CAS_SSL") == "true",
		CASValidateURL:                          os.Getenv("CAS_VALIDATE_URL"),
		CASCallbackURL:                          os.Getenv("CAS_CALLBACK_URL"),
		CASLogoutURL:                            os.Getenv("CAS_LOGOUT_URL"),
		CASLoginURL:                             os.Getenv("CAS_LOGIN_URL"),
		CASUIDField:                             envOrDefault("CAS_UID_FIELD", "user"),
		CASCAPath:                               os.Getenv("CAS_CA_PATH"),
		CASDisableSSLVerification:               os.Getenv("CAS_DISABLE_SSL_VERIFICATION") == "true",
		CASUIDKey:                               envOrDefault("CAS_UID_KEY", "user"),
		CASNameKey:                              envOrDefault("CAS_NAME_KEY", "name"),
		CASEmailKey:                             envOrDefault("CAS_EMAIL_KEY", "email"),
		CASNicknameKey:                          envOrDefault("CAS_NICKNAME_KEY", "nickname"),
		CASFirstNameKey:                         envOrDefault("CAS_FIRST_NAME_KEY", "firstname"),
		CASLastNameKey:                          envOrDefault("CAS_LAST_NAME_KEY", "lastname"),
		CASLocationKey:                          envOrDefault("CAS_LOCATION_KEY", "location"),
		CASImageKey:                             envOrDefault("CAS_IMAGE_KEY", "image"),
		CASPhoneKey:                             envOrDefault("CAS_PHONE_KEY", "phone"),
		CASSecurityAssumeEmailVerified:          os.Getenv("CAS_SECURITY_ASSUME_EMAIL_IS_VERIFIED") == "true",
		SAMLEnabled:                             os.Getenv("SAML_ENABLED") == "true",
		SAMLDisplayName:                         os.Getenv("SAML_DISPLAY_NAME"),
		SAMLACSURL:                              os.Getenv("SAML_ACS_URL"),
		SAMLIssuer:                              os.Getenv("SAML_ISSUER"),
		SAMLIDPSSOTargetURL:                     os.Getenv("SAML_IDP_SSO_TARGET_URL"),
		SAMLIDPSSOTargetParams:                  os.Getenv("SAML_IDP_SSO_TARGET_PARAMS"),
		SAMLIDPCert:                             os.Getenv("SAML_IDP_CERT"),
		SAMLIDPCertFingerprint:                  os.Getenv("SAML_IDP_CERT_FINGERPRINT"),
		SAMLIDPCertFingerprintValidator:         os.Getenv("SAML_IDP_CERT_FINGERPRINT_VALIDATOR"),
		SAMLNameIdentifierFormat:                os.Getenv("SAML_NAME_IDENTIFIER_FORMAT"),
		SAMLCertificate:                         os.Getenv("SAML_CERT"),
		SAMLPrivateKey:                          os.Getenv("SAML_PRIVATE_KEY"),
		SAMLSecurityWantAssertionsSigned:        os.Getenv("SAML_SECURITY_WANT_ASSERTION_SIGNED") == "true",
		SAMLSecurityWantAssertionsEncrypted:     os.Getenv("SAML_SECURITY_WANT_ASSERTION_ENCRYPTED") == "true",
		SAMLSecurityAssumeEmailVerified:         os.Getenv("SAML_SECURITY_ASSUME_EMAIL_IS_VERIFIED") == "true",
		SAMLAttributeUID:                        os.Getenv("SAML_ATTRIBUTES_STATEMENTS_UID"),
		SAMLAttributeEmail:                      os.Getenv("SAML_ATTRIBUTES_STATEMENTS_EMAIL"),
		SAMLAttributeFullName:                   os.Getenv("SAML_ATTRIBUTES_STATEMENTS_FULL_NAME"),
		SAMLAttributeFirstName:                  os.Getenv("SAML_ATTRIBUTES_STATEMENTS_FIRST_NAME"),
		SAMLAttributeLastName:                   os.Getenv("SAML_ATTRIBUTES_STATEMENTS_LAST_NAME"),
		SAMLAttributeVerified:                   os.Getenv("SAML_ATTRIBUTES_STATEMENTS_VERIFIED"),
		SAMLAttributeVerifiedEmail:              os.Getenv("SAML_ATTRIBUTES_STATEMENTS_VERIFIED_EMAIL"),
		SAMLUIDAttribute:                        os.Getenv("SAML_UID_ATTRIBUTE"),
		SAMLAllowedClockDrift:                   os.Getenv("SAML_ALLOWED_CLOCK_DRIFT"),
		OIDCEnabled:                             os.Getenv("OIDC_ENABLED") == "true",
		OIDCDiscovery:                           os.Getenv("OIDC_DISCOVERY") == "true",
		OIDCIssuer:                              os.Getenv("OIDC_ISSUER"),
		OIDCDisplayName:                         os.Getenv("OIDC_DISPLAY_NAME"),
		OIDCScope:                               os.Getenv("OIDC_SCOPE"),
		OIDCUIDField:                            os.Getenv("OIDC_UID_FIELD"),
		OIDCClientID:                            os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret:                        os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCRedirectURI:                         os.Getenv("OIDC_REDIRECT_URI"),
		OIDCHTTPScheme:                          envOrDefault("OIDC_HTTP_SCHEME", "https"),
		OIDCHost:                                os.Getenv("OIDC_HOST"),
		OIDCPort:                                os.Getenv("OIDC_PORT"),
		OIDCAuthEndpoint:                        os.Getenv("OIDC_AUTH_ENDPOINT"),
		OIDCTokenEndpoint:                       os.Getenv("OIDC_TOKEN_ENDPOINT"),
		OIDCUserInfoEndpoint:                    os.Getenv("OIDC_USER_INFO_ENDPOINT"),
		OIDCJWKSURI:                             os.Getenv("OIDC_JWKS_URI"),
		OIDCEndSessionEndpoint:                  os.Getenv("OIDC_END_SESSION_ENDPOINT"),
		OIDCPostLogoutRedirectURI:               os.Getenv("OIDC_IDP_LOGOUT_REDIRECT_URI"),
		OIDCResponseType:                        envOrDefault("OIDC_RESPONSE_TYPE", "code"),
		OIDCResponseMode:                        os.Getenv("OIDC_RESPONSE_MODE"),
		OIDCResponseModeSet:                     envIsSet("OIDC_RESPONSE_MODE"),
		OIDCDisplay:                             os.Getenv("OIDC_DISPLAY"),
		OIDCDisplaySet:                          envIsSet("OIDC_DISPLAY"),
		OIDCPrompt:                              os.Getenv("OIDC_PROMPT"),
		OIDCPromptSet:                           envIsSet("OIDC_PROMPT"),
		OIDCSendNonce:                           envDefaultTrue("OIDC_SEND_NONCE"),
		OIDCSendScopeToTokenEndpoint:            envDefaultTrue("OIDC_SEND_SCOPE_TO_TOKEN_ENDPOINT"),
		OIDCUsePKCE:                             os.Getenv("OIDC_USE_PKCE") == "true",
		OIDCClientAuthMethod:                    envOrDefault("OIDC_CLIENT_AUTH_METHOD", "basic"),
		OIDCSecurityAssumeEmailVerified:         os.Getenv("OIDC_SECURITY_ASSUME_EMAIL_IS_VERIFIED") == "true",
		PAMEnabled:                              os.Getenv("PAM_ENABLED") == "true",
		PAMEmailDomain:                          envOrDefault("PAM_EMAIL_DOMAIN", os.Getenv("LOCAL_DOMAIN")),
		PAMDefaultService:                       envOrDefault("PAM_DEFAULT_SERVICE", "rpam"),
		PAMControlledService:                    os.Getenv("PAM_CONTROLLED_SERVICE"),
		PAMAuthCommand:                          firstNonEmpty(os.Getenv("PAM_AUTH_COMMAND"), "pamtester"),
		LDAPEnabled:                             os.Getenv("LDAP_ENABLED") == "true",
		LDAPHost:                                envOrDefault("LDAP_HOST", "localhost"),
		LDAPPort:                                railsIntFromEnv("LDAP_PORT", 389),
		LDAPPortSet:                             envIsSet("LDAP_PORT"),
		LDAPMethod:                              envOrDefault("LDAP_METHOD", "simple_tls"),
		LDAPBase:                                os.Getenv("LDAP_BASE"),
		LDAPBindDN:                              os.Getenv("LDAP_BIND_DN"),
		LDAPPassword:                            os.Getenv("LDAP_PASSWORD"),
		LDAPUID:                                 envOrDefault("LDAP_UID", "cn"),
		LDAPMail:                                envOrDefault("LDAP_MAIL", "mail"),
		LDAPTLSNoVerify:                         os.Getenv("LDAP_TLS_NO_VERIFY") == "true",
		LDAPSearchFilter:                        envOrDefault("LDAP_SEARCH_FILTER", "(|(%{uid}=%{email})(%{mail}=%{email}))"),
		LDAPUIDConversionEnabled:                os.Getenv("LDAP_UID_CONVERSION_ENABLED") == "true",
		LDAPUIDConversionSearch:                 envOrDefault("LDAP_UID_CONVERSION_SEARCH", ".,- "),
		LDAPUIDConversionReplace:                envOrDefault("LDAP_UID_CONVERSION_REPLACE", "_"),
		DeepLAPIKey:                             railsPresenceEnv("DEEPL_API_KEY"),
		DeepLPlan:                               envOrDefault("DEEPL_PLAN", "free"),
		DeepLPlanSet:                            envIsSet("DEEPL_PLAN"),
		LibreTranslateEndpoint:                  railsPresenceEnv("LIBRE_TRANSLATE_ENDPOINT"),
		LibreTranslateAPIKey:                    os.Getenv("LIBRE_TRANSLATE_API_KEY"),
		LibreTranslateAPIKeySet:                 envIsSet("LIBRE_TRANSLATE_API_KEY"),
		CloudflareTurnstileEnabled:              os.Getenv("CLOUDFLARE_TURNSTILE_ENABLED") == "true",
		CloudflareTurnstileSiteKey:              os.Getenv("CLOUDFLARE_TURNSTILE_SITE_KEY"),
		CloudflareTurnstileSecretKey:            os.Getenv("CLOUDFLARE_TURNSTILE_SECRET_KEY"),
		HCaptchaSiteKey:                         os.Getenv("HCAPTCHA_SITE_KEY"),
		HCaptchaSecretKey:                       os.Getenv("HCAPTCHA_SECRET_KEY"),
		OTPSecret:                               otpSecretFromRailsEnv(),
		OTPSecretSet:                            envIsSet("OTP_SECRET"),
		ActiveRecordEncryptionDeterministicKey:  os.Getenv("ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY"),
		ActiveRecordEncryptionKeyDerivationSalt: os.Getenv("ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT"),
		ActiveRecordEncryptionPrimaryKey:        os.Getenv("ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY"),
		SecretKeyBase:                           secretKeyBaseFromEnv(),
		SelfDestruct:                            railsPresenceEnv("SELF_DESTRUCT"),
		DefaultLocale:                           defaultLocale,
		DefaultLocaleSet:                        defaultLocaleSet,
		StatusMaxChars:                          railsIntFromEnv("STATUS_LENGTH_LIMIT", 5000),
		StatusMaxCharsSet:                       envIsSet("STATUS_LENGTH_LIMIT"),
		MaxMedia:                                railsIntFromEnv("MAX_MEDIA_ATTACHMENTS", 4),
		MaxMediaSet:                             envIsSet("MAX_MEDIA_ATTACHMENTS"),
		MaxFollowsThreshold:                     railsIntFromEnv("MAX_FOLLOWS_THRESHOLD", 7500),
		MaxFollowsThresholdSet:                  envIsSet("MAX_FOLLOWS_THRESHOLD"),
		MaxFollowsRatio:                         railsFloatFromEnv("MAX_FOLLOWS_RATIO", 1.1),
		MaxFollowsRatioSet:                      envIsSet("MAX_FOLLOWS_RATIO"),
		ImageSizeLimit:                          railsIntFromEnv("IMAGE_LIMIT_MEGABYTES", 40) * 1024 * 1024,
		ImageSizeLimitSet:                       envIsSet("IMAGE_LIMIT_MEGABYTES"),
		VideoSizeLimit:                          railsIntFromEnv("VIDEO_LIMIT_MEGABYTES", 90) * 1024 * 1024,
		VideoSizeLimitSet:                       envIsSet("VIDEO_LIMIT_MEGABYTES"),
		MatrixLimit:                             railsIntFromEnv("MAX_ATTACHMENT_MATRIX_LIMIT", 16_777_216),
		MatrixLimitSet:                          envIsSet("MAX_ATTACHMENT_MATRIX_LIMIT"),
		DisableRemoteMediaCache:                 os.Getenv("DISABLE_REMOTE_MEDIA_CACHE") == "true",
		DisableRemoteMediaCacheSet:              disableRemoteMediaCacheSet,
		SingleUserMode:                          os.Getenv("SINGLE_USER_MODE") == "true",
		LimitedFederationMode:                   os.Getenv("LIMITED_FEDERATION_MODE") == "true" || os.Getenv("WHITELIST_MODE") == "true",
		DynamoDBEnabled:                         os.Getenv("DYNAMODB_ENABLED") == "true",
		DynamoDBAccessKey:                       envOrFallback("DYNAMODB_AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"),
		DynamoDBSecretKey:                       envOrFallback("DYNAMODB_AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"),
		DynamoDBSessionToken:                    envOrFallback("DYNAMODB_AWS_SESSION_TOKEN", "AWS_SESSION_TOKEN"),
		DynamoDBRegion:                          envOrDefault("DYNAMODB_REGION", "ap-northeast-1"),
		DynamoDBRegionSet:                       envIsSet("DYNAMODB_REGION"),
		DynamoDBNamespace:                       os.Getenv("DYNAMODB_NAMESPACE"),
		DynamoDBEndpoint:                        os.Getenv("DYNAMODB_ENDPOINT"),
		SSORedirect:                             ssoRedirectFromEnv(),
		SSOFormActionURL:                        ssoFormActionURLFromEnv(),
		SSOAccountSignUpURL:                     os.Getenv("SSO_ACCOUNT_SIGN_UP"),
		SSOAccountSignUpURLSet:                  envIsSet("SSO_ACCOUNT_SIGN_UP"),
		SSOAccountSettingsURL:                   os.Getenv("SSO_ACCOUNT_SETTINGS"),
		OmniAuthOnly:                            railsEnvTrue("OMNIAUTH_ONLY"),
		DisableSignupByAPI:                      railsEnvTrue("DISABLE_SIGNUP_BY_API"),
		DisallowUnauthenticatedAPIAccess:        railsEnvTrue("DISALLOW_UNAUTHENTICATED_API_ACCESS"),
		AuthorizedFetch:                         railsEnvTrue("AUTHORIZED_FETCH"),
		AuthorizedFetchEnvSet:                   envKeySet("AUTHORIZED_FETCH"),
		DisableAutoSwitchingRegistrations:       railsEnvTrue("DISABLE_AUTOMATIC_SWITCHING_TO_APPROVED_REGISTRATIONS"),
		EmailDomainListsApplyAfterConfirm:       railsEnvTrue("EMAIL_DOMAIN_LISTS_APPLY_AFTER_CONFIRMATION"),
		SuspiciousSignInDisabled:                suspiciousSignInDisabledFromEnv(),
		UpdateCheckURL:                          updateCheckURLFromEnv(),
		HTTPProxyURL:                            os.Getenv("http_proxy"),
		HTTPHiddenProxyURL:                      os.Getenv("http_hidden_proxy"),
		TrustedProxyIP:                          os.Getenv("TRUSTED_PROXY_IP"),
		AllowAccessToHiddenService:              os.Getenv("ALLOW_ACCESS_TO_HIDDEN_SERVICE") == "true",
		AllowedPrivateAddresses:                 os.Getenv("ALLOWED_PRIVATE_ADDRESSES"),
	}
}

func railsDefaultLocalDomain() string {
	return "localhost:" + envOrDefault("PORT", "3000")
}

func railsProductionEnv() bool {
	return railsEnvName() == "production"
}

func railsEnvName() string {
	if value, ok := os.LookupEnv("RAILS_ENV"); ok {
		return value
	}
	if value, ok := os.LookupEnv("PAON_ENV"); ok {
		return value
	}
	return "development"
}

func secretKeyBaseFromEnv() string {
	if value, ok := os.LookupEnv("SECRET_KEY_BASE"); ok {
		return value
	}
	env := railsEnvName()
	if env == "production" {
		return ""
	}
	raw, err := os.ReadFile(railsConfigFile("secrets.yml"))
	if err != nil {
		return ""
	}
	var parsed map[string]map[string]string
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	return parsed[env]["secret_key_base"]
}

func railsConfigFile(name string) string {
	for _, candidate := range []string{filepath.Join("config", name), filepath.Join("..", "..", "..", "config", name)} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Join("config", name)
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "config", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return filepath.Join("config", name)
}

func railsEnvTrue(name string) bool {
	return os.Getenv(name) == "true"
}

func envKeySet(name string) bool {
	_, ok := os.LookupEnv(name)
	return ok
}

func suspiciousSignInDisabledFromEnv() bool {
	value, ok := os.LookupEnv("DISABLE_LOGIN_TOKEN_CHALLENGE")
	if !ok {
		value = os.Getenv("DISABLE_SUSPICIOUS_SIGN_IN")
	}
	return value == "true"
}

func railsLogLevelFromEnv() string {
	return strings.ToLower(envOrDefault("RAILS_LOG_LEVEL", "info"))
}

func paonProcessRoleFromEnv() string {
	role := strings.ToLower(strings.TrimSpace(os.Getenv("PAON_PROCESS_ROLE")))
	if role == "" {
		return "all"
	}
	return role
}

func (c Config) ShouldStartHTTPServer() bool {
	return c.ProcessRole != "worker"
}

func (c Config) ShouldStartBackgroundWorkers() bool {
	return c.ProcessRole != "web"
}

func shakapackerDevServerPublicFromEnv() string {
	if value, ok := os.LookupEnv("SHAKAPACKER_DEV_SERVER_PUBLIC"); ok {
		return value
	}
	if value, ok := os.LookupEnv("WEBPACKER_DEV_SERVER_PUBLIC"); ok {
		return value
	}
	return "localhost:3035"
}

func schemeFromEnv(production bool) string {
	if production || os.Getenv("LOCAL_HTTPS") == "true" {
		return "https"
	}
	return "http"
}

func streamingAPIBaseURLFromEnv(localDomain string, webDomain string, production bool, scheme string) string {
	if value, ok := os.LookupEnv("STREAMING_API_BASE_URL"); ok {
		return value
	}
	wsScheme := "ws"
	if scheme == "https" {
		wsScheme = "wss"
	}
	host := localDomain
	if production {
		host = webDomain
	}
	return wsScheme + "://" + host
}

func updateCheckURLFromEnv() string {
	if value, ok := os.LookupEnv("UPDATE_CHECK_URL"); ok {
		return value
	}
	return "https://join.plusminus.io/api/update-check"
}

func smtpStartTLSFromEnv() bool {
	switch os.Getenv("SMTP_ENABLE_STARTTLS") {
	case "always", "auto":
		return true
	case "never":
		return false
	default:
		return os.Getenv("SMTP_ENABLE_STARTTLS_AUTO") != "false"
	}
}

func smtpDeliveryMethodFromRailsEnv() string {
	switch railsEnvName() {
	case "development":
		if envIsSet("HEROKU") || envIsSet("VAGRANT") || envIsSet("REMOTE_DEV") {
			return "letter_opener_web"
		}
		return "letter_opener"
	case "test":
		return "test"
	default:
		return envOrDefault("SMTP_DELIVERY_METHOD", "smtp")
	}
}

func cacheBusterConfigFromEnv() (string, string, string) {
	method, methodSet := os.LookupEnv("CACHE_BUSTER_HTTP_METHOD")
	if !methodSet {
		method = "GET"
	}
	header, headerSet := os.LookupEnv("CACHE_BUSTER_SECRET_HEADER")
	secret, secretSet := os.LookupEnv("CACHE_BUSTER_SECRET")
	if !methodSet {
		if !headerSet {
			header = "Secret-Header"
		}
		if !secretSet {
			secret = "True"
		}
	}
	return header, secret, method
}

func s3HostnameFromEnv() string {
	if hostname, ok := os.LookupEnv("S3_HOSTNAME"); ok {
		return hostname
	}
	region := envOrDefault("S3_REGION", "us-east-1")
	return "s3-" + region + ".amazonaws.com"
}

func s3PermissionFromEnv() string {
	if permission, ok := os.LookupEnv("S3_PERMISSION"); ok {
		return permission
	}
	return "public-read"
}

func storageHostFromEnv() string {
	if alias, ok := os.LookupEnv("S3_ALIAS_HOST"); ok {
		return normalizeStorageHostOrBlank(alias)
	}
	if cloudfront, ok := os.LookupEnv("S3_CLOUDFRONT_HOST"); ok {
		return normalizeStorageHostOrBlank(cloudfront)
	}
	if os.Getenv("S3_ENABLED") == "true" {
		if host := s3PathStyleStorageHostFromEnv(); host != "" {
			return host
		}
	}
	host := firstNonEmpty(os.Getenv("AZURE_ALIAS_HOST"), os.Getenv("SWIFT_OBJECT_URL"))
	if host == "" {
		return ""
	}
	return normalizeStorageHost(host)
}

func cspMediaHostFromEnv(scheme string) string {
	for _, key := range []string{"S3_ALIAS_HOST", "S3_CLOUDFRONT_HOST", "AZURE_ALIAS_HOST"} {
		if host := railsCSPHostToURL(os.Getenv(key), scheme); host != "" {
			return host
		}
	}
	if os.Getenv("S3_ENABLED") == "true" {
		return railsCSPHostToURL(os.Getenv("S3_HOSTNAME"), scheme)
	}
	return ""
}

func railsCSPHostToURL(host string, scheme string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	scheme = strings.Trim(strings.TrimSpace(scheme), ":/")
	if scheme == "" {
		scheme = "http"
	}
	u, err := url.Parse(scheme + "://" + host)
	if err != nil {
		return scheme + "://" + host
	}
	if u.Path != "" && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String()
}

func normalizeStorageHostOrBlank(host string) string {
	if strings.TrimSpace(host) == "" {
		return ""
	}
	return normalizeStorageHost(host)
}

func s3PathStyleStorageHostFromEnv() string {
	endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	bucket := strings.Trim(strings.TrimSpace(os.Getenv("S3_BUCKET")), "/")
	if endpoint == "" || bucket == "" {
		return ""
	}
	return normalizeStorageHost(endpoint) + "/" + bucket
}

func normalizeStorageHost(host string) string {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return host
	}
	return "https://" + host
}

func paperclipRootURLFromEnv() string {
	root := envOrDefault("PAPERCLIP_ROOT_URL", "/system")
	return strings.TrimRight(root, "/")
}

func paperclipRootPathFromEnv(publicDir string) string {
	root, ok := os.LookupEnv("PAPERCLIP_ROOT_PATH")
	if !ok {
		root = filepath.Join(publicDir, "system")
	}
	return filepath.Clean(root)
}

func listenAddrFromEnv() string {
	if addr := firstNonEmpty(os.Getenv("PAON_GO_ADDR")); addr != "" {
		return addr
	}
	if socket, ok := os.LookupEnv("SOCKET"); ok {
		return socket
	}
	port := envOrDefault("PORT", "3000")
	bind := envOrDefault("BIND", "127.0.0.1")
	if strings.HasPrefix(bind, ":") {
		return bind
	}
	if _, _, err := net.SplitHostPort(bind); err == nil {
		return bind
	}
	return net.JoinHostPort(bind, port)
}

func listenNetworkFromEnv() string {
	if strings.TrimSpace(os.Getenv("PAON_GO_ADDR")) != "" {
		return "tcp"
	}
	if _, ok := os.LookupEnv("SOCKET"); ok {
		return "unix"
	}
	return "tcp"
}

func databaseURLFromRailsEnv() string {
	defaultDBName, defaultDBUser, defaultDBHost, defaultDBPort := railsDatabaseDefaultsFromEnv()
	databaseName := envOrDefault("DB_NAME", defaultDBName)
	host := envOrDefault("DB_HOST", defaultDBHost)
	if port := envOrDefault("DB_PORT", defaultDBPort); port != "" {
		host = net.JoinHostPort(host, port)
	}
	databaseURL := url.URL{
		Scheme: "postgres",
		Host:   host,
		Path:   "/" + databaseName,
	}
	user := envOrDefault("DB_USER", defaultDBUser)
	password := os.Getenv("DB_PASS")
	if user != "" {
		if password != "" {
			databaseURL.User = url.UserPassword(user, password)
		} else {
			databaseURL.User = url.User(user)
		}
	}
	query := databaseURL.Query()
	query.Set("connect_timeout", "15")
	query.Set("sslmode", envOrDefault("DB_SSLMODE", "prefer"))
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String()
}

func replicaDatabaseURLFromRailsEnv() string {
	if replicaURL, ok := os.LookupEnv("REPLICA_DATABASE_URL"); ok && replicaURL != "" {
		return replicaURL
	}
	if !envIsSet("REPLICA_DB_NAME") && !envIsSet("REPLICA_DATABASE_URL") {
		return ""
	}

	defaultDBName, defaultDBUser, defaultDBHost, defaultDBPort := railsDatabaseDefaultsFromEnv()
	databaseName := envOrFallbackDefault("REPLICA_DB_NAME", "DB_NAME", defaultDBName)
	host := envOrFallbackDefault("REPLICA_DB_HOST", "DB_HOST", defaultDBHost)
	if port := envOrFallbackDefault("REPLICA_DB_PORT", "DB_PORT", defaultDBPort); port != "" {
		host = net.JoinHostPort(host, port)
	}
	databaseURL := url.URL{
		Scheme: "postgres",
		Host:   host,
		Path:   "/" + databaseName,
	}
	user := envOrFallbackDefault("REPLICA_DB_USER", "DB_USER", defaultDBUser)
	password := envOrFallbackDefault("REPLICA_DB_PASS", "DB_PASS", "")
	if user != "" {
		if password != "" {
			databaseURL.User = url.UserPassword(user, password)
		} else {
			databaseURL.User = url.User(user)
		}
	}
	query := databaseURL.Query()
	query.Set("connect_timeout", "15")
	query.Set("sslmode", envOrDefault("DB_SSLMODE", "prefer"))
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String()
}

func railsDatabaseDefaultsFromEnv() (databaseName string, user string, host string, port string) {
	if railsProductionEnv() {
		return "mastodon_production", "mastodon", "localhost", "5432"
	}
	return "mastodon_development", "", "127.0.0.1", ""
}

func (c Config) BaseURL() string {
	host := c.WebDomain
	if strings.TrimSpace(host) == "" {
		host = c.LocalDomain
	}
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err == nil && u.Host != "" {
			return strings.TrimRight(host, "/")
		}
	}
	return c.Scheme + "://" + host
}

func (c Config) SystemAssetURL(path string) string {
	path = strings.TrimLeft(strings.TrimSpace(path), "/")
	if c.S3Enabled {
		path = c.S3ObjectKey(path)
	}
	if strings.TrimSpace(c.StorageHost) != "" {
		return strings.TrimRight(c.StorageHost, "/") + "/" + path
	}
	root := strings.TrimRight(strings.TrimSpace(c.PaperclipRootURL), "/")
	if root == "" && !c.PaperclipRootURLSet {
		root = "/system"
	}
	if strings.HasPrefix(root, "http://") || strings.HasPrefix(root, "https://") {
		return root + "/" + path
	}
	root = strings.Trim(root, "/")
	if root == "" {
		return c.BaseURL() + "/" + path
	}
	return c.BaseURL() + "/" + root + "/" + path
}

func (c Config) S3ObjectKey(objectKey string) string {
	objectKey = strings.TrimLeft(strings.TrimSpace(objectKey), "/")
	prefix := strings.Trim(strings.TrimSpace(c.S3KeyPrefix), "/")
	if prefix == "" || objectKey == "" {
		if objectKey == "" {
			return prefix
		}
		return objectKey
	}
	return prefix + "/" + objectKey
}

func (c Config) SystemAssetPath(parts ...string) string {
	root := strings.TrimSpace(c.PaperclipRootPath)
	if root == "" && !c.PaperclipRootPathSet {
		root = filepath.Join(c.PublicDir, "system")
	}
	clean := []string{filepath.Clean(root)}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		clean = append(clean, part)
	}
	return filepath.Join(clean...)
}

func (c Config) StreamingBaseURL() string {
	if c.StreamingAPIBaseURLSet || c.StreamingAPIBaseURL != "" {
		return c.StreamingAPIBaseURL
	}
	base := c.BaseURL()
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	return strings.TrimRight(u.String(), "/")
}

func (c Config) Locale() string {
	if locale := strings.TrimSpace(c.DefaultLocale); locale != "" {
		return locale
	}
	return "en"
}

func (c Config) ShouldLog(level string) bool {
	return railsLogLevelRank(level) >= railsLogLevelRank(c.RailsLogLevel)
}

func FilterLogParameter(name string, value string) string {
	if railsFilteredParameterName(name) {
		return "[FILTERED]"
	}
	return value
}

func railsFilteredParameterName(name string) bool {
	key := strings.ToLower(name)
	for _, pattern := range railsFilteredParameterPatterns {
		if strings.Contains(key, pattern) {
			return true
		}
	}
	return false
}

var railsFilteredParameterPatterns = []string{
	"passw",
	"secret",
	"token",
	"_key",
	"crypt",
	"salt",
	"certificate",
	"otp",
	"ssn",
}

func railsLogLevelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return 0
	case "info":
		return 1
	case "warn":
		return 2
	case "error":
		return 3
	case "fatal":
		return 4
	case "unknown":
		return 5
	default:
		return 1
	}
}

func railsLogLevelValid(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info", "warn", "error", "fatal", "unknown":
		return true
	default:
		return false
	}
}

func defaultLocaleFromRailsEnv() (string, bool) {
	raw, ok := os.LookupEnv("DEFAULT_LOCALE")
	if !ok {
		return "en", false
	}
	if railsI18nLocaleAvailable(raw) {
		return raw, true
	}
	return "en", true
}

func railsI18nLocaleAvailable(locale string) bool {
	if locale == "" {
		return false
	}
	_, ok := railsI18nAvailableLocaleSet[locale]
	return ok
}

func RailsI18nAvailableLocales() []string {
	return append([]string(nil), railsI18nAvailableLocales...)
}

var railsI18nAvailableLocales = []string{
	"af", "an", "ar", "ast", "az", "be", "bg", "bn", "br", "bs", "ca", "ckb", "co", "cs", "cy", "da", "de", "el", "en", "en-GB", "eo", "es", "es-AR", "es-MX", "et", "eu", "fa", "fi", "fil", "fo", "fr", "fr-CA", "fy", "ga", "gd", "gl", "he", "hi", "hr", "hu", "hy", "ia", "id", "ie", "ig", "io", "is", "it", "ja", "ka", "kab", "kk", "kn", "ko", "ku", "kw", "la", "lad", "lt", "lv", "mk", "ml", "mr", "ms", "my", "nan", "nan-TW", "ne", "nl", "nn", "no", "oc", "pa", "pl", "pt-BR", "pt-PT", "ro", "ru", "ry", "sa", "sc", "sco", "si", "sk", "sl", "sq", "sr", "sr-Latn", "sv", "szl", "ta", "te", "th", "tlh", "tok", "tr", "tt", "ug", "uk", "ur", "vi", "zgh", "zh-CN", "zh-HK", "zh-TW",
}

var railsI18nAvailableLocaleSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(railsI18nAvailableLocales))
	for _, locale := range railsI18nAvailableLocales {
		set[locale] = struct{}{}
	}
	return set
}()

func (c Config) ValidateRuntime() error {
	var problems []error
	switch c.ProcessRole {
	case "all", "web", "worker":
	default:
		problems = append(problems, fmt.Errorf("PAON_PROCESS_ROLE must be all, web, or worker, got %q", c.ProcessRole))
	}
	validAsynqQueues := map[string]struct{}{
		"default":        {},
		"push":           {},
		"ingress":        {},
		"mailers":        {},
		"pull":           {},
		"removal":        {},
		"remote_removal": {},
	}
	for _, queue := range c.AsynqQueues {
		if _, ok := validAsynqQueues[queue]; !ok {
			problems = append(problems, fmt.Errorf("ASYNQ_QUEUES contains unsupported queue %q; supported queues are default, push, ingress, mailers, pull, removal, remote_removal", queue))
		}
	}
	if c.ProcessRole != "worker" {
		if strings.TrimSpace(c.ListenAddr) == "" {
			problems = append(problems, errors.New("PAON_GO_ADDR/BIND/PORT must produce a listen address"))
		} else {
			switch firstNonEmpty(strings.TrimSpace(c.ListenNetwork), "tcp") {
			case "tcp", "tcp4", "tcp6":
				if _, port, err := net.SplitHostPort(c.ListenAddr); err != nil {
					problems = append(problems, fmt.Errorf("PAON_GO_ADDR/BIND/PORT must produce host:port, got %q", c.ListenAddr))
				} else if err := validatePort("PORT", port); err != nil {
					problems = append(problems, err)
				}
			case "unix", "unixpacket":
				if strings.ContainsRune(c.ListenAddr, 0) {
					problems = append(problems, errors.New("SOCKET must not contain NUL bytes"))
				}
			default:
				problems = append(problems, fmt.Errorf("PAON_GO_ADDR/SOCKET produced unsupported listen network %q", c.ListenNetwork))
			}
		}
	}
	if strings.TrimSpace(c.LocalDomain) == "" {
		problems = append(problems, errors.New("LOCAL_DOMAIN is required"))
	}
	if c.RailsEnv == "production" {
		if strings.TrimSpace(c.SecretKeyBase) == "" {
			problems = append(problems, errors.New("SECRET_KEY_BASE is required in production"))
		}
		if !c.OTPSecretSet && strings.TrimSpace(c.OTPSecret) == "" {
			problems = append(problems, errors.New("OTP_SECRET is required in production"))
		}
		for _, credential := range []struct {
			name  string
			value string
		}{
			{name: "ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY", value: c.ActiveRecordEncryptionDeterministicKey},
			{name: "ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT", value: c.ActiveRecordEncryptionKeyDerivationSalt},
			{name: "ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY", value: c.ActiveRecordEncryptionPrimaryKey},
		} {
			if len(credential.value) < 32 {
				problems = append(problems, fmt.Errorf("%s must contain at least 32 bytes in production", credential.name))
			}
		}
	}
	if c.PersistentTimeout < 0 {
		problems = append(problems, errors.New("PERSISTENT_TIMEOUT must be greater than or equal to 0"))
	}
	if c.DatabaseMaxOpenConns <= 0 {
		problems = append(problems, errors.New("DB_POOL must be greater than 0"))
	}
	if c.DatabaseMaxIdleConns < 0 {
		problems = append(problems, errors.New("PAON_DB_MAX_IDLE_CONNS must be greater than or equal to 0"))
	}
	if c.DatabaseMaxOpenConns > 0 && c.DatabaseMaxIdleConns > c.DatabaseMaxOpenConns {
		problems = append(problems, errors.New("PAON_DB_MAX_IDLE_CONNS must be less than or equal to DB_POOL"))
	}
	if c.DatabaseLockTimeout < 0 {
		problems = append(problems, errors.New("PAON_DB_LOCK_TIMEOUT must be a non-negative number of seconds or duration"))
	}
	if !railsLogLevelValid(c.RailsLogLevel) {
		problems = append(problems, fmt.Errorf("RAILS_LOG_LEVEL must be one of debug, info, warn, error, fatal, or unknown, got %q", c.RailsLogLevel))
	}
	if err := c.validateOpenTelemetry(); err != nil {
		problems = append(problems, err)
	}
	if c.MaxSessionActivations < -1 {
		problems = append(problems, errors.New("MAX_SESSION_ACTIVATIONS must be -1 (unlimited) or greater than or equal to 0"))
	}
	if !c.MaxRequestPoolSizeSet && c.MaxRequestPoolSize <= 0 {
		problems = append(problems, errors.New("MAX_REQUEST_POOL_SIZE must be greater than 0"))
	}
	if err := validatePostgresURL("DATABASE_URL/DB_*", c.DatabaseURL); err != nil {
		problems = append(problems, err)
	}
	if err := validatePostgresURL("REPLICA_DATABASE_URL/REPLICA_DB_*", c.ReplicaDatabaseURL); err != nil {
		problems = append(problems, err)
	}
	if err := validatePostgresURL("PGHERO_STATS_DATABASE_URL", c.PgHeroStatsDatabaseURL); err != nil {
		problems = append(problems, err)
	}
	if err := validatePostgresURL("OTHER_DATABASE_URL", c.PgHeroOtherDatabaseURL); err != nil {
		problems = append(problems, err)
	}
	if !c.StatusMaxCharsSet && c.StatusMaxChars <= 0 {
		problems = append(problems, errors.New("STATUS_LENGTH_LIMIT must be greater than 0"))
	}
	if !c.MaxMediaSet && c.MaxMedia <= 0 {
		problems = append(problems, errors.New("MAX_MEDIA_ATTACHMENTS must be greater than 0"))
	}
	if !c.ImageSizeLimitSet && c.ImageSizeLimit <= 0 {
		problems = append(problems, errors.New("IMAGE_LIMIT_MEGABYTES must be greater than 0"))
	}
	if !c.VideoSizeLimitSet && c.VideoSizeLimit <= 0 {
		problems = append(problems, errors.New("VIDEO_LIMIT_MEGABYTES must be greater than 0"))
	}
	if !c.MatrixLimitSet && c.MatrixLimit <= 0 {
		problems = append(problems, errors.New("MAX_ATTACHMENT_MATRIX_LIMIT must be greater than 0"))
	}
	if strings.TrimSpace(c.RedisURL) == "" {
		if err := validatePort("REDIS_PORT", c.RedisPort); err != nil {
			problems = append(problems, err)
		}
		if err := validateNonNegativeInt("REDIS_DB", c.RedisDB); err != nil {
			problems = append(problems, err)
		}
	}
	if err := validateRedisURL("REDIS_URL", c.RedisURL); err != nil {
		problems = append(problems, err)
	}
	if err := validateRedisURL("SIDEKIQ_REDIS_URL", c.SidekiqRedisURL); err != nil {
		problems = append(problems, err)
	}
	if err := validateRedisURL("CACHE_REDIS_URL", c.CacheRedisURL); err != nil {
		problems = append(problems, err)
	}
	for _, sentinel := range []struct {
		name   string
		config RedisSentinelConfig
	}{
		{name: "REDIS", config: c.RedisSentinel},
		{name: "SIDEKIQ_REDIS", config: c.SidekiqRedisSentinel},
		{name: "CACHE_REDIS", config: c.CacheRedisSentinel},
	} {
		if err := validateRedisSentinelConfig(sentinel.name, sentinel.config); err != nil {
			problems = append(problems, err)
		}
	}
	if c.S3RetryLimit < 0 {
		problems = append(problems, errors.New("S3_RETRY_LIMIT must be greater than or equal to 0"))
	}
	if c.S3BatchDeleteLimitSet && (c.S3BatchDeleteLimit < 1 || c.S3BatchDeleteLimit > 1000) {
		problems = append(problems, errors.New("S3_BATCH_DELETE_LIMIT must be between 1 and 1000"))
	}
	if c.S3BatchDeleteRetrySet && c.S3BatchDeleteRetry < 1 {
		problems = append(problems, errors.New("S3_BATCH_DELETE_RETRY must be at least 1 total attempt"))
	}
	if prefix := strings.TrimSpace(c.S3KeyPrefix); prefix != "" {
		for _, segment := range strings.Split(strings.ReplaceAll(prefix, "\\", "/"), "/") {
			if segment == "" || segment == "." || segment == ".." {
				problems = append(problems, errors.New("S3_KEY_PREFIX must be a relative object-key prefix without empty, dot, or parent segments"))
				break
			}
		}
	}
	if filename := strings.TrimSpace(c.WorkerReadyFilename); filename != "" {
		if filename == "." || filename == ".." || filepath.Base(filename) != filename || strings.ContainsAny(filename, `/\\`) {
			problems = append(problems, errors.New("MASTODON_SIDEKIQ_READY_FILENAME must be a basename without path separators"))
		}
	}
	if strings.TrimSpace(c.SMTPServer) != "" {
		if err := validatePort("SMTP_PORT", c.SMTPPort); err != nil {
			problems = append(problems, err)
		}
		switch strings.TrimSpace(c.SMTPAuthMethod) {
		case "", "plain", "login", "cram_md5", "none":
		default:
			problems = append(problems, fmt.Errorf("SMTP_AUTH_METHOD must be one of plain, login, cram_md5, or none, got %q", c.SMTPAuthMethod))
		}
		switch strings.ToLower(strings.TrimSpace(c.SMTPDeliveryMethod)) {
		case "", "smtp", "letter_opener", "letter_opener_web", "test":
		default:
			problems = append(problems, fmt.Errorf("SMTP_DELIVERY_METHOD must be smtp, letter_opener, letter_opener_web, or test for Paon mail delivery, got %q", c.SMTPDeliveryMethod))
		}
		if c.SMTPStartTLSRequired && !c.SMTPStartTLS {
			problems = append(problems, errors.New("SMTP_ENABLE_STARTTLS=always must enable STARTTLS"))
		}
	}
	if err := validateHTTPBase("WEB_DOMAIN/PAON_SCHEME", c.WebDomain, c.Scheme); err != nil {
		problems = append(problems, err)
	}
	if strings.TrimSpace(c.StreamingAPIBaseURL) != "" {
		if err := validateStreamingURL("STREAMING_API_BASE_URL", c.StreamingAPIBaseURL); err != nil {
			problems = append(problems, err)
		}
	}
	if c.PAMEnabled {
		if strings.TrimSpace(c.PAMDefaultService) == "" {
			problems = append(problems, errors.New("PAM_DEFAULT_SERVICE must be set when PAM_ENABLED=true"))
		}
		command := strings.TrimSpace(c.PAMAuthCommand)
		if command == "" {
			problems = append(problems, errors.New("PAM_AUTH_COMMAND must be set when PAM_ENABLED=true"))
		} else if _, err := exec.LookPath(command); err != nil {
			problems = append(problems, fmt.Errorf("PAM_AUTH_COMMAND %q was not found for PAM_ENABLED=true", command))
		}
	}
	if c.LDAPEnabled {
		if strings.TrimSpace(c.LDAPHost) == "" {
			problems = append(problems, errors.New("LDAP_HOST must be set when LDAP_ENABLED=true"))
		}
		if !c.LDAPPortSet && (c.LDAPPort <= 0 || c.LDAPPort > 65535) {
			problems = append(problems, errors.New("LDAP_PORT must be a valid TCP port when LDAP_ENABLED=true"))
		}
		if strings.TrimSpace(c.LDAPBase) == "" {
			problems = append(problems, errors.New("LDAP_BASE must be set when LDAP_ENABLED=true"))
		}
		if strings.TrimSpace(c.LDAPBindDN) == "" {
			problems = append(problems, errors.New("LDAP_BIND_DN must be set when LDAP_ENABLED=true"))
		}
		if strings.TrimSpace(c.LDAPPassword) == "" {
			problems = append(problems, errors.New("LDAP_PASSWORD must be set when LDAP_ENABLED=true"))
		}
		if strings.TrimSpace(c.LDAPUID) == "" || strings.TrimSpace(c.LDAPMail) == "" || strings.TrimSpace(c.LDAPSearchFilter) == "" {
			problems = append(problems, errors.New("LDAP_UID, LDAP_MAIL, and LDAP_SEARCH_FILTER must be set when LDAP_ENABLED=true"))
		}
	}
	if err := validateOmniAuthRuntime(); err != nil {
		problems = append(problems, err)
	}
	if err := validateHTTPProxyURL("http_proxy", c.HTTPProxyURL); err != nil {
		problems = append(problems, err)
	}
	if err := validateHTTPProxyURL("http_hidden_proxy", c.HTTPHiddenProxyURL); err != nil {
		problems = append(problems, err)
	}
	if err := validatePrivateAddressRanges("TRUSTED_PROXY_IP", c.TrustedProxyIP); err != nil {
		problems = append(problems, err)
	}
	if err := validatePrivateAddressRanges("ALLOWED_PRIVATE_ADDRESSES", c.AllowedPrivateAddresses); err != nil {
		problems = append(problems, err)
	}
	if c.MeiliEnabled {
		if err := validateHTTPURL("MEILI_HOST", c.MeiliHost); err != nil {
			problems = append(problems, err)
		}
	}
	if c.S3Enabled && strings.TrimSpace(c.S3Endpoint) != "" {
		if err := validateHTTPURL("S3_ENDPOINT", c.S3Endpoint); err != nil {
			problems = append(problems, err)
		}
	}
	if c.S3Enabled {
		if signatureVersion := strings.TrimSpace(c.S3SignatureVersion); signatureVersion != "" && !strings.EqualFold(signatureVersion, "v4") {
			problems = append(problems, fmt.Errorf("S3_SIGNATURE_VERSION=%q is unsupported: AWS SDK for Go v2 uses SigV4", signatureVersion))
		}
		if (strings.TrimSpace(c.S3AccessKeyID) == "") != (strings.TrimSpace(c.S3SecretAccessKey) == "") {
			problems = append(problems, errors.New("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be configured together when either is set"))
		}
		if strings.TrimSpace(c.S3Region) == "" {
			problems = append(problems, errors.New("S3_REGION must not be blank when S3_ENABLED=true"))
		}
	}
	if c.CloudflareTurnstileEnabled {
		if strings.TrimSpace(c.CloudflareTurnstileSiteKey) == "" {
			problems = append(problems, errors.New("CLOUDFLARE_TURNSTILE_SITE_KEY is required when CLOUDFLARE_TURNSTILE_ENABLED=true"))
		}
		if strings.TrimSpace(c.CloudflareTurnstileSecretKey) == "" {
			problems = append(problems, errors.New("CLOUDFLARE_TURNSTILE_SECRET_KEY is required when CLOUDFLARE_TURNSTILE_ENABLED=true"))
		}
	}
	if c.DynamoDBEnabled {
		if strings.TrimSpace(c.DynamoDBNamespace) == "" {
			problems = append(problems, errors.New("DYNAMODB_NAMESPACE is required when DYNAMODB_ENABLED=true"))
		}
		if strings.TrimSpace(c.DynamoDBEndpoint) != "" {
			if err := validateHTTPURL("DYNAMODB_ENDPOINT", c.DynamoDBEndpoint); err != nil {
				problems = append(problems, err)
			}
		}
		if (strings.TrimSpace(c.DynamoDBAccessKey) == "") != (strings.TrimSpace(c.DynamoDBSecretKey) == "") {
			problems = append(problems, errors.New("DYNAMODB_AWS_ACCESS_KEY_ID and DYNAMODB_AWS_SECRET_ACCESS_KEY must be configured together when either is set"))
		}
		if strings.TrimSpace(c.DynamoDBRegion) == "" {
			problems = append(problems, errors.New("DYNAMODB_REGION must not be blank when DYNAMODB_ENABLED=true"))
		}
	}
	if (strings.TrimSpace(c.VapidPublicKey) == "") != (strings.TrimSpace(c.VapidPrivateKey) == "") {
		problems = append(problems, errors.New("VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY must be configured together"))
	}
	return errors.Join(problems...)
}

func (c Config) RuntimeWarnings() []string {
	var warnings []string
	if strings.TrimSpace(c.SecretKeyBase) == "" {
		warnings = append(warnings, "SECRET_KEY_BASE is not configured; Rails session cookies and Devise-compatible token digests will not be verified")
	}
	if strings.TrimSpace(c.OTPSecret) == "" {
		warnings = append(warnings, "OTP_SECRET is not configured; existing Rails attr_encrypted TOTP secrets cannot be decrypted")
	}
	for _, credential := range []struct {
		name  string
		value string
	}{
		{name: "ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY", value: c.ActiveRecordEncryptionDeterministicKey},
		{name: "ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT", value: c.ActiveRecordEncryptionKeyDerivationSalt},
		{name: "ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY", value: c.ActiveRecordEncryptionPrimaryKey},
	} {
		if len(credential.value) < 32 {
			warnings = append(warnings, credential.name+" is not configured with at least 32 bytes; Active Record encrypted TOTP secrets cannot be read or written")
		}
	}
	if strings.TrimSpace(c.VapidPublicKey) == "" && strings.TrimSpace(c.VapidPrivateKey) == "" {
		warnings = append(warnings, "VAPID_PUBLIC_KEY/VAPID_PRIVATE_KEY are not configured; Web Push delivery is disabled")
	}
	if c.DynamoDBEnabled && strings.TrimSpace(c.DynamoDBNamespace) == "" {
		warnings = append(warnings, "DYNAMODB_ENABLED=true but DYNAMODB_NAMESPACE is not configured; quote metadata storage is disabled")
	}
	if c.S3Enabled && strings.TrimSpace(c.S3Bucket) == "" {
		warnings = append(warnings, "S3_ENABLED=true but S3_BUCKET is not configured; Paperclip files are kept locally but object-storage writes and deletes are disabled")
	}
	if strings.TrimSpace(c.RedisNamespace) != "" {
		warnings = append(warnings, "REDIS_NAMESPACE is deprecated in Mastodon 4.3; Paon still applies it for existing Redis/Asynq key compatibility")
	}
	if c.OpenTelemetryEnabled && strings.TrimSpace(c.StatsDAddr) != "" {
		warnings = append(warnings, "STATSD_ADDR is configured with OpenTelemetry; Paon disables the legacy StatsD extension while OTLP export is enabled to prevent double counting")
	}
	if c.AzureEnabled && (strings.TrimSpace(c.AzureStorageAccount) == "" || strings.TrimSpace(c.AzureStorageAccessKey) == "" || strings.TrimSpace(c.AzureContainerName) == "") {
		warnings = append(warnings, "AZURE_ENABLED=true but AZURE_STORAGE_ACCOUNT/AZURE_STORAGE_ACCESS_KEY/AZURE_CONTAINER_NAME are not fully configured; Paperclip files are kept locally but Azure object-storage writes and deletes are disabled")
	}
	if c.SwiftEnabled && (strings.TrimSpace(c.SwiftObjectURL) == "" || strings.TrimSpace(c.SwiftContainer) == "" || strings.TrimSpace(c.SwiftUsername) == "" || strings.TrimSpace(c.SwiftPassword) == "" || strings.TrimSpace(c.SwiftAuthURL) == "" || (strings.TrimSpace(c.SwiftProjectID) == "" && strings.TrimSpace(c.SwiftTenant) == "")) {
		warnings = append(warnings, "SWIFT_ENABLED=true but SWIFT_OBJECT_URL/SWIFT_CONTAINER/SWIFT_USERNAME/SWIFT_PASSWORD/SWIFT_AUTH_URL and SWIFT_PROJECT_ID or SWIFT_TENANT are not fully configured; Paperclip files are kept locally but Swift object-storage writes and deletes are disabled")
	}
	if c.CASEnabled && c.CASDisableSSLVerification {
		warnings = append(warnings, "CAS_DISABLE_SSL_VERIFICATION=true disables TLS certificate verification for CAS serviceValidate requests")
	}
	if c.SAMLEnabled && c.SAMLSecurityWantAssertionsEncrypted && strings.TrimSpace(c.SAMLPrivateKey) == "" {
		warnings = append(warnings, "SAML_SECURITY_WANT_ASSERTION_ENCRYPTED=true requires SAML_PRIVATE_KEY; encrypted SAML assertions will be rejected without the service provider private key")
	}
	if c.SAMLEnabled && strings.TrimSpace(c.SAMLIDPCertFingerprintValidator) != "" {
		warnings = append(warnings, "SAML_IDP_CERT_FINGERPRINT_VALIDATOR is treated as a Rails String#[] fingerprint allowlist in Paon; Ruby lambda/proc code is not executed")
	}
	if c.SAMLEnabled && strings.TrimSpace(c.SAMLCertificate) != "" && strings.TrimSpace(c.SAMLPrivateKey) == "" {
		warnings = append(warnings, "SAML_CERT is configured without SAML_PRIVATE_KEY; SAML AuthnRequest redirects will be sent unsigned")
	}
	if _, configured := os.LookupEnv("MASTODON_USE_LIBVIPS"); configured {
		warnings = append(warnings, "MASTODON_USE_LIBVIPS is not a runtime selector in Paon; image processing is fixed when the Go binary is built (use the Docker PAON_IMAGE_PROCESSOR build argument)")
	}
	ffprobeBinary := mediaToolBinaryFromConfig(c.FFprobeBinary, c.FFprobeBinarySet, "ffprobe")
	if _, err := exec.LookPath(ffprobeBinary); err != nil {
		warnings = append(warnings, ffprobeBinary+" is not installed; queued video/audio uploads can complete but Rails-compatible media duration, bitrate, dimension, and frame-rate metadata will be incomplete")
	}
	ffmpegBinary := mediaToolBinaryFromConfig(c.FFmpegBinary, c.FFmpegBinarySet, "ffmpeg")
	if _, err := exec.LookPath(ffmpegBinary); err != nil {
		warnings = append(warnings, ffmpegBinary+" is not installed; Rails-compatible video/audio transcoding, video/GIFV preview thumbnails, audio cover-art extraction, and HEIC/HEIF/AVIF JPEG conversion will be unavailable")
	}
	return warnings
}

func mediaToolBinaryFromConfig(value string, set bool, fallback string) string {
	if set {
		return value
	}
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func validateHTTPBase(name string, host string, scheme string) error {
	host = strings.TrimSpace(host)
	scheme = strings.TrimSpace(scheme)
	if host == "" {
		return fmt.Errorf("%s host is required", name)
	}
	if strings.Contains(host, "://") {
		return validateHTTPURL(name, host)
	}
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%s must use http or https scheme, got %q", name, scheme)
	}
	return nil
}

func validateHTTPURL(name string, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an http or https URL, got %q", name, raw)
	}
	return nil
}

// ValidateOpenTelemetry validates the optional OTLP integration without
// requiring HTTP/database settings used only by the long-running server.
func (c Config) ValidateOpenTelemetry() error {
	return c.validateOpenTelemetry()
}

func (c Config) validateOpenTelemetry() error {
	if !c.OpenTelemetryEnabled {
		if strings.TrimSpace(c.OTelExporterOTLPHeaders) != "" || strings.TrimSpace(c.OTelExporterOTLPTracesHeaders) != "" || strings.TrimSpace(c.OTelExporterOTLPMetricsHeaders) != "" {
			return errors.New("OTEL_EXPORTER_OTLP*_HEADERS require OTEL_EXPORTER_OTLP_ENDPOINT or a matching signal endpoint")
		}
		if strings.TrimSpace(c.OTelExporterOTLPTracesProtocol) != "" || strings.TrimSpace(c.OTelExporterOTLPMetricsProtocol) != "" {
			return errors.New("signal-specific OTEL_EXPORTER_OTLP*_PROTOCOL requires a matching signal endpoint")
		}
		return nil
	}

	var problems []error
	if !c.OpenTelemetryTracesEnabled && !c.OpenTelemetryMetricsEnabled {
		problems = append(problems, errors.New("OpenTelemetry requires a trace or metric OTLP endpoint"))
	}
	if strings.TrimSpace(c.OTelServiceNamePrefix) == "" {
		problems = append(problems, errors.New("OTEL_SERVICE_NAME_PREFIX must not be blank when OpenTelemetry is enabled"))
	}
	separator := c.OTelServiceNameSeparator
	if separator == "" || len(separator) > 8 || strings.IndexFunc(separator, unicode.IsControl) >= 0 {
		problems = append(problems, errors.New("OTEL_SERVICE_NAME_SEPARATOR must be 1 to 8 non-control characters"))
	}
	for _, endpoint := range []struct {
		name  string
		value string
	}{
		{name: "OTEL_EXPORTER_OTLP_ENDPOINT", value: c.OTelExporterOTLPEndpoint},
		{name: "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", value: c.OTelExporterOTLPTracesEndpoint},
		{name: "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", value: c.OTelExporterOTLPMetricsEndpoint},
	} {
		if strings.TrimSpace(endpoint.value) != "" {
			if err := validateOTLPEndpoint(endpoint.name, endpoint.value); err != nil {
				problems = append(problems, err)
			}
		}
	}
	if strings.TrimSpace(c.OTelExporterOTLPTracesHeaders) != "" && !c.OpenTelemetryTracesEnabled {
		problems = append(problems, errors.New("OTEL_EXPORTER_OTLP_TRACES_HEADERS requires a trace endpoint"))
	}
	if strings.TrimSpace(c.OTelExporterOTLPMetricsHeaders) != "" && !c.OpenTelemetryMetricsEnabled {
		problems = append(problems, errors.New("OTEL_EXPORTER_OTLP_METRICS_HEADERS requires a metric endpoint"))
	}
	for _, headers := range []struct {
		name  string
		value string
	}{
		{name: "OTEL_EXPORTER_OTLP_HEADERS", value: c.OTelExporterOTLPHeaders},
		{name: "OTEL_EXPORTER_OTLP_TRACES_HEADERS", value: c.OTelExporterOTLPTracesHeaders},
		{name: "OTEL_EXPORTER_OTLP_METRICS_HEADERS", value: c.OTelExporterOTLPMetricsHeaders},
	} {
		if strings.ContainsAny(headers.value, "\r\n\x00") {
			problems = append(problems, fmt.Errorf("%s must not contain control-line characters", headers.name))
		}
	}
	if strings.TrimSpace(c.OTelExporterOTLPTracesProtocol) != "" && !c.OpenTelemetryTracesEnabled {
		problems = append(problems, errors.New("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL requires a trace endpoint"))
	}
	if strings.TrimSpace(c.OTelExporterOTLPMetricsProtocol) != "" && !c.OpenTelemetryMetricsEnabled {
		problems = append(problems, errors.New("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL requires a metric endpoint"))
	}
	if c.OpenTelemetryTracesEnabled {
		protocol := firstNonEmpty(strings.TrimSpace(c.OTelExporterOTLPTracesProtocol), strings.TrimSpace(c.OTelExporterOTLPProtocol), "http/protobuf")
		if protocol != "http/protobuf" {
			problems = append(problems, fmt.Errorf("OTEL exporter trace protocol must be http/protobuf, got %q", protocol))
		}
	}
	if c.OpenTelemetryMetricsEnabled {
		protocol := firstNonEmpty(strings.TrimSpace(c.OTelExporterOTLPMetricsProtocol), strings.TrimSpace(c.OTelExporterOTLPProtocol), "http/protobuf")
		if protocol != "http/protobuf" {
			problems = append(problems, fmt.Errorf("OTEL exporter metric protocol must be http/protobuf, got %q", protocol))
		}
	}

	sampler := strings.ToLower(strings.TrimSpace(c.OTelTracesSampler))
	supportedSamplers := map[string]struct{}{
		"always_on": {}, "always_off": {}, "traceidratio": {},
		"parentbased_always_on": {}, "parentbased_always_off": {}, "parentbased_traceidratio": {},
	}
	if _, ok := supportedSamplers[sampler]; !ok {
		problems = append(problems, fmt.Errorf("OTEL_TRACES_SAMPLER has unsupported value %q", c.OTelTracesSampler))
	}
	ratioSampler := sampler == "traceidratio" || sampler == "parentbased_traceidratio"
	if rawArg := strings.TrimSpace(c.OTelTracesSamplerArg); rawArg != "" {
		if !ratioSampler {
			problems = append(problems, errors.New("OTEL_TRACES_SAMPLER_ARG is only valid with traceidratio samplers"))
		} else if ratio, err := strconv.ParseFloat(rawArg, 64); err != nil || ratio < 0 || ratio > 1 {
			problems = append(problems, errors.New("OTEL_TRACES_SAMPLER_ARG must be a number between 0 and 1"))
		}
	}

	seenPropagators := map[string]struct{}{}
	for _, raw := range c.OTelPropagators {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch name {
		case "tracecontext", "baggage", "none":
		default:
			problems = append(problems, fmt.Errorf("OTEL_PROPAGATORS has unsupported value %q", raw))
			continue
		}
		if _, exists := seenPropagators[name]; exists {
			problems = append(problems, fmt.Errorf("OTEL_PROPAGATORS contains duplicate value %q", raw))
		}
		seenPropagators[name] = struct{}{}
	}
	if len(seenPropagators) == 0 {
		problems = append(problems, errors.New("OTEL_PROPAGATORS must contain tracecontext, baggage, or none"))
	}
	if _, none := seenPropagators["none"]; none && len(seenPropagators) != 1 {
		problems = append(problems, errors.New("OTEL_PROPAGATORS=none cannot be combined with other propagators"))
	}
	return errors.Join(problems...)
}

func validateOTLPEndpoint(name string, raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%s must be a valid URL", name)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute http or https URL", name)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain userinfo, query parameters, or a fragment; use OTEL_EXPORTER_OTLP*_HEADERS for credentials", name)
	}
	return nil
}

func validateHTTPProxyURL(name string, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("%s must be an absolute HTTP proxy URL, got %q", name, raw)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("%s must use http or https, got %q", name, raw)
	}
	if u.Port() != "" {
		if err := validatePort(name+" port", u.Port()); err != nil {
			return err
		}
	}
	return nil
}

func validatePrivateAddressRanges(name string, raw string) error {
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(item); err == nil {
			continue
		}
		if ip := net.ParseIP(item); ip != nil {
			continue
		}
		return fmt.Errorf("%s contains invalid IP address or CIDR range %q", name, item)
	}
	return nil
}

func validateStreamingURL(name string, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s must be an http, https, ws, or wss URL, got %q", name, raw)
	}
	switch u.Scheme {
	case "http", "https", "ws", "wss":
		return nil
	default:
		return fmt.Errorf("%s must be an http, https, ws, or wss URL, got %q", name, raw)
	}
}

func validateOmniAuthRuntime() error {
	var problems []error
	if os.Getenv("CAS_ENABLED") == "true" {
		if strings.TrimSpace(os.Getenv("CAS_URL")) == "" && strings.TrimSpace(os.Getenv("CAS_HOST")) == "" {
			if strings.TrimSpace(os.Getenv("CAS_LOGIN_URL")) == "" || strings.TrimSpace(os.Getenv("CAS_VALIDATE_URL")) == "" {
				problems = append(problems, fmt.Errorf("CAS_URL or CAS_HOST is required when CAS_ENABLED=true unless both CAS_LOGIN_URL and CAS_VALIDATE_URL are configured"))
			}
		}
		for _, name := range []string{"CAS_URL", "CAS_VALIDATE_URL", "CAS_CALLBACK_URL", "CAS_LOGOUT_URL", "CAS_LOGIN_URL"} {
			if strings.TrimSpace(os.Getenv(name)) != "" {
				if err := validateHTTPURL(name, os.Getenv(name)); err != nil {
					problems = append(problems, err)
				}
			}
		}
		if strings.TrimSpace(os.Getenv("CAS_PORT")) != "" {
			if port, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CAS_PORT"))); err != nil || port <= 0 || port > 65535 {
				problems = append(problems, fmt.Errorf("CAS_PORT must be a valid TCP port"))
			}
		}
		if path := strings.TrimSpace(os.Getenv("CAS_CA_PATH")); path != "" {
			if _, err := os.Stat(path); err != nil {
				problems = append(problems, fmt.Errorf("CAS_CA_PATH must reference an existing CA file or directory: %w", err))
			}
		}
	}
	if os.Getenv("SAML_ENABLED") == "true" {
		for _, name := range []string{"SAML_ACS_URL", "SAML_ISSUER", "SAML_IDP_SSO_TARGET_URL"} {
			if strings.TrimSpace(os.Getenv(name)) == "" {
				problems = append(problems, fmt.Errorf("%s is required when SAML_ENABLED=true", name))
				continue
			}
			if err := validateHTTPURL(name, os.Getenv(name)); err != nil {
				problems = append(problems, err)
			}
		}
		if strings.TrimSpace(os.Getenv("SAML_IDP_SSO_TARGET_PARAMS")) != "" {
			if err := validateQueryParams("SAML_IDP_SSO_TARGET_PARAMS", os.Getenv("SAML_IDP_SSO_TARGET_PARAMS")); err != nil {
				problems = append(problems, err)
			}
		}
		if strings.TrimSpace(os.Getenv("SAML_ALLOWED_CLOCK_DRIFT")) != "" {
			if _, err := parseDurationSeconds("SAML_ALLOWED_CLOCK_DRIFT", os.Getenv("SAML_ALLOWED_CLOCK_DRIFT")); err != nil {
				problems = append(problems, err)
			}
		}
	}
	if os.Getenv("OIDC_ENABLED") == "true" {
		for _, name := range []string{"OIDC_SCOPE", "OIDC_UID_FIELD", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URI"} {
			if strings.TrimSpace(os.Getenv(name)) == "" {
				problems = append(problems, fmt.Errorf("%s is required when OIDC_ENABLED=true", name))
			}
		}
		if strings.TrimSpace(os.Getenv("OIDC_REDIRECT_URI")) != "" {
			if err := validateHTTPURL("OIDC_REDIRECT_URI", os.Getenv("OIDC_REDIRECT_URI")); err != nil {
				problems = append(problems, err)
			}
		}
		if os.Getenv("OIDC_DISCOVERY") != "true" {
			for _, name := range []string{"OIDC_AUTH_ENDPOINT", "OIDC_TOKEN_ENDPOINT", "OIDC_USER_INFO_ENDPOINT", "OIDC_JWKS_URI"} {
				if strings.TrimSpace(os.Getenv(name)) == "" {
					problems = append(problems, fmt.Errorf("%s is required when OIDC_ENABLED=true and OIDC_DISCOVERY is not true", name))
					continue
				}
				if err := validateOIDCClientEndpoint(name, os.Getenv(name)); err != nil {
					problems = append(problems, err)
				}
			}
		}
		for _, name := range []string{"OIDC_END_SESSION_ENDPOINT", "OIDC_IDP_LOGOUT_REDIRECT_URI"} {
			if strings.TrimSpace(os.Getenv(name)) != "" {
				if err := validateOIDCClientEndpoint(name, os.Getenv(name)); err != nil {
					problems = append(problems, err)
				}
			}
		}
		if strings.TrimSpace(os.Getenv("OIDC_PORT")) != "" {
			if port, err := strconv.Atoi(strings.TrimSpace(os.Getenv("OIDC_PORT"))); err != nil || port <= 0 || port > 65535 {
				problems = append(problems, fmt.Errorf("OIDC_PORT must be a valid TCP port"))
			}
		}
		switch scheme := strings.TrimSpace(os.Getenv("OIDC_HTTP_SCHEME")); scheme {
		case "", "http", "https":
		default:
			problems = append(problems, fmt.Errorf("OIDC_HTTP_SCHEME must be http or https, got %q", scheme))
		}
	}
	return errors.Join(problems...)
}

func validateOIDCClientEndpoint(name string, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s must be a valid URL or endpoint path, got %q", name, raw)
	}
	if u.IsAbs() {
		if err := validateHTTPURL(name, raw); err != nil {
			return err
		}
		return nil
	}
	if strings.TrimSpace(os.Getenv("OIDC_HOST")) == "" {
		return fmt.Errorf("%s must be an absolute URL or OIDC_HOST must be configured", name)
	}
	if !strings.HasPrefix(raw, "/") {
		return fmt.Errorf("%s must be an absolute URL or a path beginning with /, got %q", name, raw)
	}
	return nil
}

func validatePostgresURL(name string, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s must be a postgres or postgresql URL, got %q", name, raw)
	}
	switch u.Scheme {
	case "postgres", "postgresql":
	default:
		return fmt.Errorf("%s must be a postgres or postgresql URL, got %q", name, raw)
	}
	if strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("%s database name is required", name)
	}
	if u.Port() != "" {
		if err := validatePort(name+" port", u.Port()); err != nil {
			return err
		}
	}
	return nil
}

func validateQueryParams(name string, raw string) error {
	for _, pair := range queryParamPairs(raw) {
		key, _, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s must be query-string style key=value pairs, got %q", name, raw)
		}
	}
	values, err := url.ParseQuery(strings.Join(queryParamPairs(raw), "&"))
	if err != nil {
		return fmt.Errorf("%s must be query-string style key=value pairs: %w", name, err)
	}
	for key := range values {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s must not contain blank parameter names", name)
		}
	}
	return nil
}

func queryParamPairs(raw string) []string {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "?"))
	if raw == "" {
		return nil
	}
	sep := "&"
	if !strings.Contains(raw, "&") && strings.Contains(raw, ",") {
		sep = ","
	}
	parts := strings.Split(raw, sep)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func validateRedisURL(name string, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "unix://") {
		if strings.TrimSpace(strings.TrimPrefix(raw, "unix://")) == "" {
			return fmt.Errorf("%s unix socket path is required", name)
		}
		return nil
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s must be a redis, rediss, or unix URL, got %q", name, raw)
	}
	switch u.Scheme {
	case "redis", "rediss":
	default:
		return fmt.Errorf("%s must be a redis, rediss, or unix URL, got %q", name, raw)
	}
	if u.Port() != "" {
		if err := validatePort(name+" port", u.Port()); err != nil {
			return err
		}
	}
	if u.Path != "" && u.Path != "/" {
		if err := validateNonNegativeInt(name+" database", strings.TrimPrefix(u.Path, "/")); err != nil {
			return err
		}
	}
	return nil
}

func validateRedisSentinelConfig(name string, cfg RedisSentinelConfig) error {
	masterConfigured := strings.TrimSpace(cfg.MasterName) != ""
	addressesConfigured := len(cfg.Addresses) > 0
	if masterConfigured != addressesConfigured {
		return fmt.Errorf("%s_SENTINEL_MASTER and non-empty %s_SENTINELS must be configured together", name, name)
	}
	if !masterConfigured {
		return nil
	}
	for _, address := range cfg.Addresses {
		host, port, err := net.SplitHostPort(strings.TrimSpace(address))
		if err != nil || strings.TrimSpace(host) == "" {
			return fmt.Errorf("%s_SENTINELS contains invalid host:port %q", name, address)
		}
		if err := validatePort(name+"_SENTINEL_PORT", port); err != nil {
			return err
		}
	}
	return nil
}

func validatePort(name string, raw string) error {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 || value > 65535 {
		return fmt.Errorf("%s must be a TCP port between 1 and 65535, got %q", name, raw)
	}
	return nil
}

func validateNonNegativeInt(name string, raw string) error {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return fmt.Errorf("%s must be greater than or equal to 0, got %q", name, raw)
	}
	return nil
}

func parseDurationSeconds(name string, raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, fmt.Errorf("%s must be greater than or equal to 0, got %q", name, raw)
		}
		return seconds, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return 0, fmt.Errorf("%s must be a non-negative number of seconds or duration, got %q", name, raw)
	}
	return int(duration / time.Second), nil
}

func databaseLockTimeoutFromEnv() time.Duration {
	raw, ok := os.LookupEnv("PAON_DB_LOCK_TIMEOUT")
	if !ok {
		return 5 * time.Second
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return -1
	}
	return duration
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func envOrDefault(name string, fallback string) string {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return value
}

func envOrFallback(name string, fallbackName string) string {
	value, ok := os.LookupEnv(name)
	if ok {
		return value
	}
	return os.Getenv(fallbackName)
}

func envOrFallbackDefault(name string, fallbackName string, fallback string) string {
	value, ok := os.LookupEnv(name)
	if ok {
		return value
	}
	fallbackValue, ok := os.LookupEnv(fallbackName)
	if ok {
		return fallbackValue
	}
	return fallback
}

func railsPresenceEnv(name string) string {
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}

func smtpCAFileFromEnv() string {
	if value := railsPresenceEnv("SMTP_CA_FILE"); value != "" {
		return value
	}
	return "/etc/ssl/certs/ca-certificates.crt"
}

func intFromEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func railsIntFromEnv(name string, fallback int) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	sign := 1
	if value[0] == '+' || value[0] == '-' {
		if value[0] == '-' {
			sign = -1
		}
		value = value[1:]
	}
	i := 0
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	parsed, err := strconv.Atoi(value[:i])
	if err != nil {
		return 0
	}
	return sign * parsed
}

func asynqConcurrencyFromEnv() int {
	if envIsSet("ASYNQ_CONCURRENCY") {
		return railsIntFromEnv("ASYNQ_CONCURRENCY", 5)
	}
	return railsIntFromEnv("SIDEKIQ_CONCURRENCY", 5)
}

func asynqQueuesFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("ASYNQ_QUEUES"))
	if raw == "" {
		return nil
	}
	queues := make([]string, 0)
	seen := make(map[string]struct{})
	for _, queue := range strings.Split(raw, ",") {
		queue = strings.ToLower(strings.TrimSpace(queue))
		if queue == "" {
			continue
		}
		if _, ok := seen[queue]; ok {
			continue
		}
		seen[queue] = struct{}{}
		queues = append(queues, queue)
	}
	return queues
}

func railsFloatFromEnv(name string, fallback float64) float64 {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	i := 0
	if value[i] == '+' || value[i] == '-' {
		i++
	}
	digits := 0
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
		digits++
	}
	if i < len(value) && value[i] == '.' {
		i++
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return 0
	}
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		expStart := i
		i++
		if i < len(value) && (value[i] == '+' || value[i] == '-') {
			i++
		}
		expDigitsStart := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if expDigitsStart == i {
			i = expStart
		}
	}
	parsed, err := strconv.ParseFloat(value[:i], 64)
	if err != nil {
		return 0
	}
	return parsed
}

func envIsSet(name string) bool {
	_, ok := os.LookupEnv(name)
	return ok
}

func databasePoolFromEnv() int {
	if envIsSet("DB_POOL") {
		return railsIntFromEnv("DB_POOL", 5)
	}
	return railsIntFromEnv("MAX_THREADS", 5)
}

func floatFromEnv(name string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func strictIntFromEnv(name string, fallback int) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return -1
	}
	return value
}

func envDefaultTrue(name string) bool {
	value, ok := os.LookupEnv(name)
	if !ok {
		return true
	}
	return value == "true"
}

func meiliPrefixFromEnv() string {
	prefix, ok := os.LookupEnv("MEILI_PREFIX")
	if !ok {
		prefix = os.Getenv("REDIS_NAMESPACE")
	}
	if strings.TrimSpace(prefix) == "" {
		return ""
	}
	return prefix + "_"
}

func redisNamespaceFromEnv() string {
	namespace, ok := os.LookupEnv("REDIS_NAMESPACE")
	if !ok || strings.TrimSpace(namespace) == "" {
		return ""
	}
	return namespace + ":"
}

func railsRedisURLFromEnv(prefix string, fallbackURL string, defaults bool) string {
	envPrefix := ""
	if prefix != "" {
		envPrefix = strings.ToUpper(prefix) + "_"
	}
	if value := railsPresenceEnv(envPrefix + "REDIS_URL"); value != "" {
		return value
	}

	user, userSet := os.LookupEnv(envPrefix + "REDIS_USER")
	password, passwordSet := os.LookupEnv(envPrefix + "REDIS_PASSWORD")
	host, hostSet := os.LookupEnv(envPrefix + "REDIS_HOST")
	port, portSet := os.LookupEnv(envPrefix + "REDIS_PORT")
	db, dbSet := os.LookupEnv(envPrefix + "REDIS_DB")
	master, masterSet := os.LookupEnv(envPrefix + "REDIS_SENTINEL_MASTER")
	sentinels, sentinelsSet := os.LookupEnv(envPrefix + "REDIS_SENTINELS")
	if !defaults && !userSet && !passwordSet && !hostSet && !portSet && !dbSet && !masterSet && !sentinelsSet {
		return fallbackURL
	}
	if defaults {
		if !userSet {
			user = ""
		}
		if !passwordSet {
			password = ""
		}
		if !hostSet {
			host = "localhost"
		}
		if !portSet {
			port = "6379"
		}
		if !dbSet {
			db = "0"
		}
	} else {
		// A partially configured alternate Redis must fall back as a whole. This
		// avoids accidentally combining CACHE/SIDEKIQ credentials with the base
		// host, which differs from Mastodon's RedisConfiguration contract.
		if !hostSet || strings.TrimSpace(host) == "" {
			if strings.TrimSpace(master) != "" && strings.TrimSpace(sentinels) != "" {
				host = strings.TrimSpace(master)
			} else {
				return fallbackURL
			}
		}
		if !portSet {
			port = "6379"
		}
		if !dbSet {
			db = "0"
		}
	}
	u := url.URL{Scheme: "redis", Host: net.JoinHostPort(host, port), Path: "/" + db}
	if strings.TrimSpace(user) != "" {
		u.User = url.UserPassword(user, password)
	} else if strings.TrimSpace(password) != "" {
		u.User = url.UserPassword("", password)
	}
	return u.String()
}

func redisSentinelConfigFromEnv(prefix string, fallback RedisSentinelConfig) RedisSentinelConfig {
	envPrefix := ""
	if prefix != "" {
		envPrefix = strings.ToUpper(prefix) + "_"
	}
	redisPrefix := envPrefix + "REDIS_"
	if railsPresenceEnv(redisPrefix+"URL") != "" {
		return RedisSentinelConfig{}
	}

	_, masterSet := os.LookupEnv(redisPrefix + "SENTINEL_MASTER")
	_, listSet := os.LookupEnv(redisPrefix + "SENTINELS")
	_, portSet := os.LookupEnv(redisPrefix + "SENTINEL_PORT")
	_, sentinelUserSet := os.LookupEnv(redisPrefix + "SENTINEL_USERNAME")
	_, sentinelPasswordSet := os.LookupEnv(redisPrefix + "SENTINEL_PASSWORD")
	sentinelSet := masterSet || listSet || portSet || sentinelUserSet || sentinelPasswordSet

	_, userSet := os.LookupEnv(redisPrefix + "USER")
	_, passwordSet := os.LookupEnv(redisPrefix + "PASSWORD")
	_, hostSet := os.LookupEnv(redisPrefix + "HOST")
	_, redisPortSet := os.LookupEnv(redisPrefix + "PORT")
	_, dbSet := os.LookupEnv(redisPrefix + "DB")
	dataConfigSet := userSet || passwordSet || hostSet || redisPortSet || dbSet
	if !sentinelSet {
		if prefix != "" {
			if !dataConfigSet || !hostSet || strings.TrimSpace(os.Getenv(redisPrefix+"HOST")) == "" {
				return fallback
			}
		}
		return RedisSentinelConfig{}
	}

	defaultPort := strings.TrimSpace(envOrDefault(redisPrefix+"SENTINEL_PORT", "26379"))
	dataUser := os.Getenv(redisPrefix + "USER")
	dataPassword := os.Getenv(redisPrefix + "PASSWORD")
	sentinelUser := dataUser
	if value, ok := os.LookupEnv(redisPrefix + "SENTINEL_USERNAME"); ok {
		sentinelUser = value
	}
	sentinelPassword := dataPassword
	if value, ok := os.LookupEnv(redisPrefix + "SENTINEL_PASSWORD"); ok {
		sentinelPassword = value
	}
	return RedisSentinelConfig{
		MasterName: strings.TrimSpace(os.Getenv(redisPrefix + "SENTINEL_MASTER")),
		Addresses:  parseRedisSentinels(os.Getenv(redisPrefix+"SENTINELS"), defaultPort),
		Username:   strings.TrimSpace(sentinelUser),
		Password:   sentinelPassword,
	}
}

func parseRedisSentinels(raw string, defaultPort string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if host, port, err := net.SplitHostPort(part); err == nil {
			out = append(out, net.JoinHostPort(host, port))
			continue
		}
		// An unbracketed IPv6 literal has several colons and no unambiguous
		// optional port. Treat it as a host and apply SENTINEL_PORT.
		if strings.Count(part, ":") > 1 {
			out = append(out, net.JoinHostPort(strings.Trim(part, "[]"), defaultPort))
			continue
		}
		host, port, found := strings.Cut(part, ":")
		if !found {
			port = defaultPort
		}
		out = append(out, net.JoinHostPort(strings.TrimSpace(host), strings.TrimSpace(port)))
	}
	return out
}

func splitAndTrimCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func alternateDomainsFromEnv() []string {
	return alternateDomainsEnvValue("ALTERNATE_DOMAINS")
}

func alternateDomainsEnvValue(name string) []string {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return nil
	}
	parts := regexp.MustCompile(`\s*,\s*`).Split(raw, -1)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func railsDevelopmentHostsFromEnv() []string {
	raw := os.Getenv("RAILS_DEVELOPMENT_HOSTS")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if host := strings.TrimSpace(part); host != "" {
			out = append(out, host)
		}
	}
	return out
}

func vapidSubjectFromEnv(localDomain string) string {
	subject := firstNonEmpty(os.Getenv("VAPID_SUBJECT"))
	if subject != "" {
		return subject
	}
	email := firstNonEmpty(os.Getenv("SMTP_FROM_ADDRESS"), os.Getenv("SMTP_LOGIN"), "notifications@"+localDomain)
	if strings.Contains(email, ":") {
		return email
	}
	return "mailto:" + email
}

func railsVapidKeysFromEnv(production bool) (string, string) {
	if production {
		return os.Getenv("VAPID_PUBLIC_KEY"), os.Getenv("VAPID_PRIVATE_KEY")
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", ""
	}
	return publicKey, privateKey
}

const (
	railsDevelopmentOTPSecret = "1fc2b87989afa6351912abeebe31ffc5c476ead9bf8b3d74cbc4a302c7b69a45b40b1bbef3506ddad73e942e15ed5ca4b402bf9a66423626051104f4b5f05109"
	railsTestOTPSecret        = "100c7faeef00caa29242f6b04156742bf76065771fd4117990c4282b8748ff3d99f8fdae97c982ab5bd2e6756a159121377cce4421f4a8ecd2d67bd7749a3fb4"
)

func otpSecretFromRailsEnv() string {
	if value, ok := os.LookupEnv("OTP_SECRET"); ok {
		return value
	}
	switch railsEnvName() {
	case "development":
		return railsDevelopmentOTPSecret
	case "test":
		return railsTestOTPSecret
	default:
		return ""
	}
}

func ssoRedirectFromEnv() string {
	if os.Getenv("ONE_CLICK_SSO_LOGIN") != "true" || os.Getenv("OMNIAUTH_ONLY") != "true" {
		return ""
	}
	provider, ok := singleSSOProviderFromEnv()
	if !ok {
		return ""
	}
	return "/auth/auth/" + provider
}

func ssoFormActionURLFromEnv() string {
	if os.Getenv("ONE_CLICK_SSO_LOGIN") != "true" || os.Getenv("OMNIAUTH_ONLY") != "true" {
		return ""
	}
	provider, ok := singleSSOProviderFromEnv()
	if !ok {
		return ""
	}
	switch provider {
	case "cas":
		return casSSOFormActionURLFromEnv()
	case "saml":
		return strings.TrimSpace(os.Getenv("SAML_IDP_SSO_TARGET_URL"))
	case "openid_connect":
		return oidcClientEndpointURLFromEnv(os.Getenv("OIDC_AUTH_ENDPOINT"))
	default:
		return ""
	}
}

func singleSSOProviderFromEnv() (string, bool) {
	providers := make([]string, 0, 3)
	if os.Getenv("CAS_ENABLED") == "true" {
		providers = append(providers, "cas")
	}
	if os.Getenv("SAML_ENABLED") == "true" {
		providers = append(providers, "saml")
	}
	if os.Getenv("OIDC_ENABLED") == "true" {
		providers = append(providers, "openid_connect")
	}
	if len(providers) != 1 {
		return "", false
	}
	return providers[0], true
}

func casSSOFormActionURLFromEnv() string {
	if explicit, ok := os.LookupEnv("CAS_LOGIN_URL"); ok {
		return strings.TrimSpace(explicit)
	}
	if rawBase, ok := os.LookupEnv("CAS_URL"); ok {
		base := strings.TrimSpace(rawBase)
		if base == "" {
			return ""
		}
		return strings.TrimRight(base, "/") + "/login"
	}
	host := strings.TrimSpace(os.Getenv("CAS_HOST"))
	if host == "" {
		return ""
	}
	scheme := "http"
	if os.Getenv("CAS_SSL") == "true" {
		scheme = "https"
	}
	if port := strings.TrimSpace(os.Getenv("CAS_PORT")); port != "" && !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, port)
	}
	return scheme + "://" + host + "/login"
}

func oidcClientEndpointURLFromEnv(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		if u.Scheme == "" || u.Host == "" {
			return ""
		}
		return u.String()
	}
	host := strings.TrimSpace(os.Getenv("OIDC_HOST"))
	if host == "" {
		return ""
	}
	scheme := strings.TrimSpace(envOrDefault("OIDC_HTTP_SCHEME", "https"))
	if port := strings.TrimSpace(os.Getenv("OIDC_PORT")); port != "" && !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, port)
	}
	path := u.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if scheme == "" {
		return "//" + host + (&url.URL{Path: path, RawQuery: u.RawQuery}).String()
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: path, RawQuery: u.RawQuery}).String()
}
