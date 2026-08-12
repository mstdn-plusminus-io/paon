package api

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"gopkg.in/yaml.v3"
)

func TestNormalizeSettingScalarTrimsWhitespaceAndQuotes(t *testing.T) {
	if got := normalizeSettingScalar(`  "false"  `); got != "false" {
		t.Fatalf("normalizeSettingScalar = %q", got)
	}
}

func TestNormalizeSettingScalarDecodesRailsSerializedYAML(t *testing.T) {
	tests := map[string]string{
		"--- \"@mohemohe\"\n": "@mohemohe",
		"--- true\n":          "true",
		"--- 7\n":             "7",
		"--- ''\n":            "",
	}
	for input, want := range tests {
		if got := normalizeSettingScalar(input); got != want {
			t.Errorf("normalizeSettingScalar(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRailsSettingStoredValuePreservesNonScalarYAML(t *testing.T) {
	raw := "---\n- alice\n- bob\n"
	if got := railsSettingStoredValue(raw); got != raw {
		t.Fatalf("railsSettingStoredValue = %q, want original YAML %q", got, raw)
	}
	if got := railsSettingStoredValue("  plain text  "); got != "  plain text  " {
		t.Fatalf("plain stored value = %q", got)
	}
}

func TestNormalizeRegistrationsModeAcceptsQuotedValues(t *testing.T) {
	if got := normalizeRegistrationsMode(` "approved" `); got != "approved" {
		t.Fatalf("normalizeRegistrationsMode = %q", got)
	}
}

func TestRailsSettingsDefaultsDrivePublicMetadataWithoutDB(t *testing.T) {
	server := &Server{cfg: config.Config{Title: "Configured"}}
	metadata := server.instanceMetadata()
	if metadata.Title != "Mastodon" {
		t.Fatalf("Title = %q, want Rails settings.yml site_title default", metadata.Title)
	}
	options := server.instanceRegistrationOptions()
	if options.Mode != "none" || options.ClosedMessage != "" {
		t.Fatalf("registration options = %#v", options)
	}
	if got := server.reservedUsernames(); !reflect.DeepEqual(got, defaultReservedUsernames) {
		t.Fatalf("reserved usernames = %#v, want Rails settings.yml defaults %#v", got, defaultReservedUsernames)
	}
	wantReserved := []string{"abuse", "account", "accounts", "admin", "administration", "administrator", "admins", "help", "helpdesk", "instance", "mod", "moderator", "moderators", "mods", "owner", "root", "security", "server", "staff", "support", "webmaster"}
	if !reflect.DeepEqual(defaultReservedUsernames, wantReserved) {
		t.Fatalf("default reserved usernames = %#v, want Mastodon 4.4 defaults %#v", defaultReservedUsernames, wantReserved)
	}
}

func TestInitialStateServerSettingsUseRailsDefaultsWithoutDB(t *testing.T) {
	settings := (&Server{}).initialStateServerSettings()
	if settings == nil {
		t.Fatal("settings is nil")
	}
	if !settings.ProfileDirectory || !settings.TrendsEnabled || !settings.TimelinePreview || !settings.ActivityAPIEnabled || !settings.TrendsAsLandingPage {
		t.Fatalf("server setting defaults = %#v", settings)
	}
	if settings.AutoPlayGIF != nil || settings.DisplayMedia != nil || settings.ReduceMotion != nil || settings.UseBlurhash != nil || settings.CropImages != nil {
		t.Fatalf("appearance defaults = %#v", settings)
	}
}

func TestSettingBoolValueUsesFallbackWithoutDB(t *testing.T) {
	s := &Server{}
	if !s.settingBoolValue("trends", true) {
		t.Fatal("expected Rails settings.yml default true")
	}
	if s.settingBoolValue("not_in_settings_yml", false) {
		t.Fatal("expected fallback false")
	}
}

func TestTrendsEnabledDefaultsToRailsDefault(t *testing.T) {
	if !(&Server{}).trendsEnabled() {
		t.Fatal("trendsEnabled = false")
	}
}

func TestActivityAPIEnabledDefaultsToRailsDefault(t *testing.T) {
	if !(&Server{}).activityAPIEnabled() {
		t.Fatal("activityAPIEnabled = false")
	}
}

func TestPeersAPIEnabledDefaultsToRailsDefault(t *testing.T) {
	if !(&Server{}).peersAPIEnabled() {
		t.Fatal("peersAPIEnabled = false")
	}
}

func TestProfileDirectoryEnabledDefaultsToRailsDefault(t *testing.T) {
	if !(&Server{}).profileDirectoryEnabled() {
		t.Fatal("profileDirectoryEnabled = false")
	}
}

func TestTimelinePreviewSettingDefaultsToRailsDefault(t *testing.T) {
	if !(&Server{}).timelinePreviewEnabled() {
		t.Fatal("timelinePreviewEnabled = false")
	}
}

func TestInstanceMetadataFallsBackToRailsSettingsDefaultTitle(t *testing.T) {
	metadata := (&Server{cfg: config.Config{Title: "Configured"}}).instanceMetadata()
	if metadata.Title != "Mastodon" {
		t.Fatalf("Title = %q", metadata.Title)
	}
	if !metadata.TitleSet {
		t.Fatal("metadata title from Rails settings should be marked set")
	}
	if metadata.ShortDescription != "" || metadata.ContactEmail != "" || metadata.ContactAccount != nil || metadata.StatusPageURL != "" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestInstanceRegistrationOptionsIncludeRailsSSOAccountSignUpURL(t *testing.T) {
	server := &Server{cfg: config.Config{SSOAccountSignUpURL: "https://sso.example.test/sign-up", SSOAccountSignUpURLSet: true}}
	options := server.instanceRegistrationOptions()
	if options.SignUpURL != "https://sso.example.test/sign-up" {
		t.Fatalf("SignUpURL = %q", options.SignUpURL)
	}
	if !options.SignUpURLSet {
		t.Fatal("SignUpURLSet = false")
	}
}

func railsSettingsYAMLDefaults(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("../../../config/settings.yml")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	defaults, ok := parsed["defaults"]
	if !ok {
		t.Fatal("config/settings.yml missing defaults section")
	}
	out := make(map[string]string, len(defaults))
	for key, value := range defaults {
		out[key] = railsSettingYAMLDefaultString(t, value)
	}
	return out
}

func railsSettingYAMLDefaultString(t *testing.T, value any) string {
	t.Helper()
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				t.Fatalf("unexpected settings.yml array item %#v", item)
			}
			out = append(out, text)
		}
		return strings.Join(out, "\n")
	default:
		t.Fatalf("unexpected settings.yml default type %T: %#v", value, value)
		return ""
	}
}
