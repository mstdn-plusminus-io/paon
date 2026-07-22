package serializer

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

var (
	railsAttributePattern = regexp.MustCompile(`(?m)^\s*attribute\s+:([a-z_]+)(?:.*key:\s*(?::([a-z_]+)|['"]([^'"]+)['"]))?`)
	railsBelongsToPattern = regexp.MustCompile(`(?m)^\s*belongs_to\s+:([a-z_]+)(?:.*key:\s*(?::([a-z_]+)|['"]([^'"]+)['"]))?`)
	railsHasManyPattern   = regexp.MustCompile(`(?m)^\s*has_many\s+:([a-z_]+)(?:.*key:\s*(?::([a-z_]+)|['"]([^'"]+)['"]))?`)
	railsHasOnePattern    = regexp.MustCompile(`(?m)^\s*has_one\s+:([a-z_]+)(?:.*key:\s*(?::([a-z_]+)|['"]([^'"]+)['"]))?`)
	railsSymbolPattern    = regexp.MustCompile(`:([a-z_]+)`)
)

func TestAccountFromModelLocalURLs(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:          42,
		Username:    "alice",
		DisplayName: "Alice",
		Note:        "hello",
		CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountStat: models.AccountStat{StatusesCount: 3, FollowingCount: 2, FollowersCount: 1},
	}

	out := AccountFromModel(cfg, account)
	if out.ID != "42" {
		t.Fatalf("ID = %q", out.ID)
	}
	if out.Acct != "alice" {
		t.Fatalf("Acct = %q", out.Acct)
	}
	if out.URL != "https://example.test/@alice" {
		t.Fatalf("URL = %q", out.URL)
	}
	if out.URI != "https://example.test/users/alice" {
		t.Fatalf("URI = %q", out.URI)
	}
	if out.CreatedAt != "2026-06-18T00:00:00.000Z" {
		t.Fatalf("CreatedAt = %q", out.CreatedAt)
	}
	if out.StatusesCount != 3 || out.FollowingCount != 2 || out.FollowersCount != 1 {
		t.Fatalf("unexpected counters: %#v", out)
	}
}

func TestAccountCreatedAtMatchesRailsDateOnlyTimestamp(t *testing.T) {
	got := accountCreatedAt(time.Date(2026, 6, 18, 23, 59, 58, 123456789, time.FixedZone("JST", 9*60*60)))
	if got != "2026-06-18T00:00:00.000Z" {
		t.Fatalf("accountCreatedAt = %q", got)
	}
}

func TestAccountFromModelFormatsLocalNoteAndFieldsLikeRailsTextFormatter(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		Note:      "hello #GoLang gemini://example.com/docs",
		Fields:    []byte(`[{"name":"Site","value":"ipfs://bafybeigdyrzt"}]`),
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
	}

	out := AccountFromModel(cfg, account)
	for _, want := range []string{
		`<a href="https://example.test/tags/golang" class="mention hashtag" rel="tag">#<span>GoLang</span></a>`,
		`href="gemini://example.com/docs"`,
	} {
		if !strings.Contains(out.Note, want) {
			t.Fatalf("note missing %q: %s", want, out.Note)
		}
	}
	if len(out.Fields) != 1 {
		t.Fatalf("fields = %#v", out.Fields)
	}
	if strings.Contains(out.Fields[0].Value, "<p>") {
		t.Fatalf("field value should be inline like Rails multiline:false: %s", out.Fields[0].Value)
	}
	if !strings.Contains(out.Fields[0].Value, `href="ipfs://bafybeigdyrzt"`) {
		t.Fatalf("field value missing ipfs link: %s", out.Fields[0].Value)
	}
}

func TestAccountFromModelShortensRemoteVerifiedFieldLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		Domain:    sql.NullString{String: "remote.example", Valid: true},
		Fields:    []byte(`[{"name":"Site","value":"<a href=\"https://remote.example/some/really/long/path\">https://remote.example/some/really/long/path</a>","verified_at":"2026-06-18T12:00:00Z"}]`),
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
	}

	out := AccountFromModel(cfg, account)
	if len(out.Fields) != 1 {
		t.Fatalf("fields = %#v", out.Fields)
	}
	for _, want := range []string{
		`href="https://remote.example/some/really/long/path"`,
		`<span class="invisible">https://</span><span class="ellipsis">remote.example/some/really/lon</span><span class="invisible">g/path</span>`,
	} {
		if !strings.Contains(out.Fields[0].Value, want) {
			t.Fatalf("verified field missing %q: %s", want, out.Fields[0].Value)
		}
	}
}

func TestAccountFromModelIncludesHighlightedLocalRole(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:          42,
		Username:    "alice",
		CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountStat: models.AccountStat{},
		User: models.User{
			ID: 9,
			Role: models.UserRole{
				ID:          4,
				Name:        "Moderator",
				Color:       "#ffcc00",
				Highlighted: true,
			},
		},
	}

	out := AccountFromModel(cfg, account)
	if len(out.Roles) != 1 {
		t.Fatalf("Roles length = %d", len(out.Roles))
	}
	role, ok := out.Roles[0].(AccountRole)
	if !ok {
		t.Fatalf("role type = %T", out.Roles[0])
	}
	if role.ID != "4" || role.Name != "Moderator" || role.Color != "#ffcc00" {
		t.Fatalf("role = %#v", role)
	}
}

func TestAccountFromModelIncludesHydratedCustomEmojis(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		CustomEmojis: []models.CustomEmoji{{
			ID:            7,
			Shortcode:     "party",
			ImageFileName: sql.NullString{String: "party.gif", Valid: true},
		}},
	}

	out := AccountFromModel(cfg, account)
	if len(out.Emojis) != 1 || out.Emojis[0].Shortcode != "party" || out.Emojis[0].URL == "" {
		t.Fatalf("Emojis = %#v", out.Emojis)
	}
}

func TestAccountFromModelUsesUserSettingForLocalNoIndex(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:           42,
		Username:     "alice",
		CreatedAt:    time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Discoverable: sql.NullBool{Bool: false, Valid: true},
		User: models.User{
			ID:       9,
			Settings: sql.NullString{String: `{"noindex":false}`, Valid: true},
		},
	}

	out := AccountFromModel(cfg, account)
	if out.NoIndex == nil || *out.NoIndex {
		t.Fatalf("NoIndex = %#v, want explicit false from user settings", out.NoIndex)
	}

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if value, ok := payload["noindex"]; !ok || value != false {
		t.Fatalf("noindex payload = %#v in %s", payload["noindex"], string(body))
	}
}

func TestAccountFromModelOmitsNoIndexForRemoteAccount(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		Domain:    sql.NullString{String: "remote.example", Valid: true},
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		User: models.User{
			ID:       9,
			Settings: sql.NullString{String: `{"noindex":true}`, Valid: true},
		},
	}

	body, err := json.Marshal(AccountFromModel(cfg, account))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["noindex"]; ok {
		t.Fatalf("remote account serialized noindex: %s", string(body))
	}
}

func TestAccountFromModelSerializesEmptyRolesForLocalAccount(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		User:      models.User{ID: 9},
	}

	body, err := json.Marshal(AccountFromModel(cfg, account))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	roles, ok := payload["roles"].([]any)
	if !ok || len(roles) != 0 {
		t.Fatalf("roles payload = %#v in %s", payload["roles"], string(body))
	}
}

func TestAccountFromModelOmitsRolesForRemoteAccount(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		Domain:    sql.NullString{String: "remote.example", Valid: true},
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		User: models.User{
			ID:   9,
			Role: models.UserRole{ID: 4, Name: "Moderator", Highlighted: true},
		},
	}

	body, err := json.Marshal(AccountFromModel(cfg, account))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["roles"]; ok {
		t.Fatalf("remote account serialized roles: %s", string(body))
	}
}

func TestAccountFromModelHidesSuspendedProfileDecoration(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:              42,
		Username:        "alice",
		DisplayName:     "Alice",
		Note:            "hello",
		CreatedAt:       time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AvatarRemoteURL: sql.NullString{String: "https://remote.example/avatar.png", Valid: true},
		HeaderRemoteURL: "https://remote.example/header.png",
		Fields:          []byte(`[{"name":"Website","value":"https://example.test"}]`),
		SuspendedAt:     sql.NullTime{Time: time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC), Valid: true},
		MovedToAccount: &models.Account{
			ID:        84,
			Username:  "alice_new",
			CreatedAt: time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
		},
		CustomEmojis: []models.CustomEmoji{{
			ID:            7,
			Shortcode:     "party",
			ImageFileName: sql.NullString{String: "party.gif", Valid: true},
		}},
	}

	out := AccountFromModel(cfg, account)
	if out.DisplayName != "" || out.Note != "" || out.Moved != nil {
		t.Fatalf("suspended public profile fields leaked: %#v", out)
	}
	if len(out.Fields) != 0 || len(out.Emojis) != 0 {
		t.Fatalf("suspended profile decorations leaked: fields=%#v emojis=%#v", out.Fields, out.Emojis)
	}
	if out.Avatar != "https://example.test/avatars/original/missing.png" || out.AvatarStatic != out.Avatar {
		t.Fatalf("suspended avatar = %q / %q", out.Avatar, out.AvatarStatic)
	}
	if out.Header != "https://example.test/headers/original/missing.png" || out.HeaderStatic != out.Header {
		t.Fatalf("suspended header = %q / %q", out.Header, out.HeaderStatic)
	}
	if out.Suspended == nil || !*out.Suspended {
		t.Fatalf("suspended flag = %#v", out.Suspended)
	}
}

func TestAccountFromModelRemoteMediaCacheDisabledUsesRemoteProfileImagesBeforeSuspendedFallback(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", DisableRemoteMediaCache: true}
	account := models.Account{
		ID:              42,
		Username:        "alice",
		CreatedAt:       time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AvatarRemoteURL: sql.NullString{String: "https://remote.example/avatar.png", Valid: true},
		HeaderRemoteURL: "https://remote.example/header.png",
		SuspendedAt:     sql.NullTime{Time: time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC), Valid: true},
	}

	out := AccountFromModel(cfg, account)
	if out.Avatar != "https://remote.example/avatar.png" || out.AvatarStatic != out.Avatar {
		t.Fatalf("remote-cache-disabled avatar = %q / %q", out.Avatar, out.AvatarStatic)
	}
	if out.Header != "https://remote.example/header.png" || out.HeaderStatic != out.Header {
		t.Fatalf("remote-cache-disabled header = %q / %q", out.Header, out.HeaderStatic)
	}
	if out.Suspended == nil || !*out.Suspended {
		t.Fatalf("suspended flag = %#v", out.Suspended)
	}
}

func TestAccountFromModelKeepsSuspendedGroupFlagLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:          42,
		Username:    "alice",
		CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		ActorType:   sql.NullString{String: "Group", Valid: true},
		SuspendedAt: sql.NullTime{Time: time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC), Valid: true},
	}

	out := AccountFromModel(cfg, account)
	if !out.Group {
		t.Fatalf("Group = false, want true for suspended Group actor")
	}
	if out.Locked || out.Bot || out.Discoverable == nil || *out.Discoverable {
		t.Fatalf("suspended account leaked masked booleans: locked=%v bot=%v discoverable=%v", out.Locked, out.Bot, out.Discoverable)
	}
}

func TestAccountFromModelRemoteAvatarHonorsCacheSettingAndStaticStyle(t *testing.T) {
	account := models.Account{
		ID:                         42,
		Username:                   "alice",
		Domain:                     sql.NullString{String: "remote.example", Valid: true},
		CreatedAt:                  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AvatarRemoteURL:            sql.NullString{String: "https://remote.example/avatar.gif", Valid: true},
		HeaderRemoteURL:            "https://remote.example/header.gif",
		AvatarFileName:             sql.NullString{String: "avatar.gif", Valid: true},
		AvatarContentType:          sql.NullString{String: "image/gif", Valid: true},
		AvatarStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
		HeaderFileName:             sql.NullString{String: "header.gif", Valid: true},
		HeaderContentType:          sql.NullString{String: "image/gif", Valid: true},
		HeaderStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}

	cached := AccountFromModel(config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}, account)
	if cached.Avatar != "https://example.test/system/cache/accounts/avatars/000/000/042/original/avatar.gif" {
		t.Fatalf("cached avatar = %q", cached.Avatar)
	}
	if cached.AvatarStatic != "https://example.test/system/cache/accounts/avatars/000/000/042/static/avatar.png" {
		t.Fatalf("cached avatar_static = %q", cached.AvatarStatic)
	}
	if cached.Header != "https://example.test/system/cache/accounts/headers/000/000/042/original/header.gif" {
		t.Fatalf("cached header = %q", cached.Header)
	}
	if cached.HeaderStatic != "https://example.test/system/cache/accounts/headers/000/000/042/static/header.png" {
		t.Fatalf("cached header_static = %q", cached.HeaderStatic)
	}

	stored := AccountFromModel(config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", StorageHost: "https://media.example.test/"}, account)
	if stored.Avatar != "https://media.example.test/cache/accounts/avatars/000/000/042/original/avatar.gif" {
		t.Fatalf("storage-host avatar = %q", stored.Avatar)
	}
	if stored.AvatarStatic != "https://media.example.test/cache/accounts/avatars/000/000/042/static/avatar.png" {
		t.Fatalf("storage-host avatar_static = %q", stored.AvatarStatic)
	}
	if stored.Header != "https://media.example.test/cache/accounts/headers/000/000/042/original/header.gif" {
		t.Fatalf("storage-host header = %q", stored.Header)
	}
	if stored.HeaderStatic != "https://media.example.test/cache/accounts/headers/000/000/042/static/header.png" {
		t.Fatalf("storage-host header_static = %q", stored.HeaderStatic)
	}

	remote := AccountFromModel(config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", DisableRemoteMediaCache: true}, account)
	if remote.Avatar != "https://remote.example/avatar.gif" || remote.AvatarStatic != remote.Avatar {
		t.Fatalf("remote avatar = %q / %q", remote.Avatar, remote.AvatarStatic)
	}
	if remote.Header != "https://remote.example/header.gif" || remote.HeaderStatic != remote.Header {
		t.Fatalf("remote header = %q / %q", remote.Header, remote.HeaderStatic)
	}
}

func TestAccountFromModelIncludesMovedAccountWithoutRecursiveMoved(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	movedAgain := models.Account{
		ID:          100,
		Username:    "alice_latest",
		DisplayName: "Alice Latest",
		CreatedAt:   time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
	}
	target := models.Account{
		ID:               84,
		Username:         "alice_new",
		DisplayName:      "Alice New",
		CreatedAt:        time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
		MovedToAccountID: sql.NullInt64{Int64: 100, Valid: true},
		MovedToAccount:   &movedAgain,
	}
	account := models.Account{
		ID:               42,
		Username:         "alice",
		DisplayName:      "Alice",
		CreatedAt:        time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		MovedToAccountID: sql.NullInt64{Int64: 84, Valid: true},
		MovedToAccount:   &target,
	}

	out := AccountFromModel(cfg, account)
	if out.Moved == nil {
		t.Fatal("expected moved account")
	}
	if out.Moved.ID != "84" || out.Moved.Username != "alice_new" {
		t.Fatalf("Moved = %#v", out.Moved)
	}
	if out.Moved.Moved != nil {
		t.Fatalf("recursive moved account serialized: %#v", out.Moved.Moved)
	}

	payload, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"moved":{"id":"84"`) {
		t.Fatalf("payload missing moved account: %s", payload)
	}
	if strings.Contains(string(payload), "alice_latest") {
		t.Fatalf("payload serialized recursive moved target: %s", payload)
	}
}

func TestAccountRolesFromModelOmitsNonPublicRoles(t *testing.T) {
	cases := []struct {
		name    string
		account models.Account
	}{
		{
			name: "not highlighted",
			account: models.Account{
				User: models.User{ID: 9, Role: models.UserRole{ID: 4, Highlighted: false}},
			},
		},
		{
			name: "remote account",
			account: models.Account{
				Domain: sql.NullString{String: "remote.example", Valid: true},
				User:   models.User{ID: 9, Role: models.UserRole{ID: 4, Highlighted: true}},
			},
		},
		{
			name: "suspended account",
			account: models.Account{
				SuspendedAt: sql.NullTime{Time: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC), Valid: true},
				User:        models.User{ID: 9, Role: models.UserRole{ID: 4, Highlighted: true}},
			},
		},
		{
			name:    "no local user",
			account: models.Account{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if roles := AccountRolesFromModel(tc.account); len(roles) != 0 {
				t.Fatalf("roles = %#v", roles)
			}
		})
	}
}

func TestSuggestionFromModelUsesMastodonV2Shape(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
	}

	out := SuggestionFromModel(cfg, account, "global")
	if out.Source != "global" {
		t.Fatalf("Source = %q", out.Source)
	}
	if out.Account.ID != "42" || out.Account.Acct != "alice" {
		t.Fatalf("Account = %#v", out.Account)
	}
}

func TestMutedAccountFromModelIncludesOnlyFutureExpiration(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
	}

	future := MutedAccountFromModel(cfg, account, sql.NullTime{Time: time.Now().UTC().Add(time.Hour), Valid: true})
	if future.MuteExpiresAt == nil {
		t.Fatal("expected future mute expiration to be serialized")
	}

	expired := MutedAccountFromModel(cfg, account, sql.NullTime{Time: time.Now().UTC().Add(-time.Hour), Valid: true})
	if expired.MuteExpiresAt != nil {
		t.Fatalf("expired mute expiration serialized: %#v", *expired.MuteExpiresAt)
	}

	body, err := json.Marshal(expired)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["mute_expires_at"]; !ok {
		t.Fatalf("mute_expires_at key omitted: %s", string(body))
	}
	if payload["mute_expires_at"] != nil {
		t.Fatalf("mute_expires_at = %#v, want null in %s", payload["mute_expires_at"], string(body))
	}
}

func TestListFromModelUsesMastodonKeys(t *testing.T) {
	out := ListFromModel(models.List{ID: 9, Title: "Friends", RepliesPolicy: 1, Exclusive: true})
	if out.ID != "9" {
		t.Fatalf("ID = %q", out.ID)
	}
	if out.Title != "Friends" {
		t.Fatalf("Title = %q", out.Title)
	}
	if out.RepliesPolicy != "followed" {
		t.Fatalf("RepliesPolicy = %q", out.RepliesPolicy)
	}
	if !out.Exclusive {
		t.Fatal("Exclusive = false")
	}
}

func TestAdminTagFromModelUsesReviewAndDefaultBooleans(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := AdminTagFromModel(cfg, models.Tag{
		ID:          7,
		Name:        "golang",
		DisplayName: sql.NullString{String: "GoLang", Valid: true},
		Trendable:   sql.NullBool{Bool: false, Valid: true},
	})

	if out.ID != "7" || out.Name != "GoLang" || out.URL != "https://example.test/tags/golang" {
		t.Fatalf("tag identity = %#v", out)
	}
	if out.Trendable || !out.Usable || !out.Listable || !out.RequiresReview {
		t.Fatalf("admin tag booleans = %#v", out)
	}
}

func TestAdminTagFromModelWithHistoryMatchesRailsTagSerializer(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := AdminTagFromModelWithHistory(cfg, models.Tag{
		ID:   7,
		Name: "golang",
	}, []any{map[string]string{
		"day":      "1781827200",
		"uses":     "12",
		"accounts": "3",
	}})
	if len(out.History) != 1 {
		t.Fatalf("History = %#v", out.History)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	history, ok := raw["history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("json history = %#v", raw["history"])
	}
	first, ok := history[0].(map[string]any)
	if !ok || first["day"] != "1781827200" || first["uses"] != "12" || first["accounts"] != "3" {
		t.Fatalf("json history[0] = %#v", history[0])
	}
}

func TestAdminCanonicalEmailBlockFromModel(t *testing.T) {
	out := AdminCanonicalEmailBlockFromModel(models.CanonicalEmailBlock{ID: 9, CanonicalEmailHash: "abc123"})
	if out.ID != "9" || out.CanonicalEmailHash != "abc123" {
		t.Fatalf("canonical email block = %#v", out)
	}
}

func TestFilterFromModelUsesMastodonV2Keys(t *testing.T) {
	expiresAt := time.Date(2026, 6, 18, 13, 0, 0, 0, time.UTC)
	filter := models.CustomFilter{
		ID:        7,
		Phrase:    "Noise",
		Context:   models.StringArray{"home", "thread"},
		ExpiresAt: sql.NullTime{Time: expiresAt, Valid: true},
		Action:    1,
		Keywords: []models.CustomFilterKeyword{
			{ID: 8, Keyword: "spoiler", WholeWord: false},
		},
		Statuses: []models.CustomFilterStatus{
			{ID: 9, StatusID: 100},
		},
	}

	out := FilterFromModel(filter, true)
	if out.ID != "7" || out.Title != "Noise" {
		t.Fatalf("unexpected filter = %#v", out)
	}
	if out.FilterAction != "hide" {
		t.Fatalf("FilterAction = %q", out.FilterAction)
	}
	if out.ExpiresAt == nil || *out.ExpiresAt != "2026-06-18T13:00:00.000Z" {
		t.Fatalf("ExpiresAt = %#v", out.ExpiresAt)
	}
	if len(out.Keywords) != 1 || out.Keywords[0].ID != "8" || out.Keywords[0].WholeWord {
		t.Fatalf("Keywords = %#v", out.Keywords)
	}
	if len(out.Statuses) != 1 || out.Statuses[0].StatusID != "100" {
		t.Fatalf("Statuses = %#v", out.Statuses)
	}
}

func TestFilterFromModelSerializesEmptyRulesWhenRequested(t *testing.T) {
	filter := models.CustomFilter{
		ID:      7,
		Phrase:  "Noise",
		Context: models.StringArray{"home"},
	}

	body, err := json.Marshal(FilterFromModel(filter, true))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	keywords, ok := payload["keywords"].([]any)
	if !ok || len(keywords) != 0 {
		t.Fatalf("keywords payload = %#v in %s", payload["keywords"], string(body))
	}
	statuses, ok := payload["statuses"].([]any)
	if !ok || len(statuses) != 0 {
		t.Fatalf("statuses payload = %#v in %s", payload["statuses"], string(body))
	}
}

func TestFilterFromModelOmitsRulesWhenNotRequested(t *testing.T) {
	filter := models.CustomFilter{
		ID:      7,
		Phrase:  "Noise",
		Context: models.StringArray{"home"},
		Keywords: []models.CustomFilterKeyword{
			{ID: 8, Keyword: "spoiler"},
		},
		Statuses: []models.CustomFilterStatus{
			{ID: 9, StatusID: 100},
		},
	}

	body, err := json.Marshal(FilterFromModel(filter, false))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["keywords"]; ok {
		t.Fatalf("keywords should be omitted when rules are not requested: %s", string(body))
	}
	if _, ok := payload["statuses"]; ok {
		t.Fatalf("statuses should be omitted when rules are not requested: %s", string(body))
	}
}

func TestMarkerFromModelUsesMastodonKeys(t *testing.T) {
	updatedAt := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	out := MarkerFromModel(models.Marker{LastReadID: 123, LockVersion: 4, UpdatedAt: updatedAt})

	if out.LastReadID != "123" {
		t.Fatalf("LastReadID = %q", out.LastReadID)
	}
	if out.Version != 4 {
		t.Fatalf("Version = %d", out.Version)
	}
	if out.UpdatedAt != "2026-06-18T12:00:00.000Z" {
		t.Fatalf("UpdatedAt = %q", out.UpdatedAt)
	}
}

func TestCredentialAccountFromModelIncludesSourceSettings(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	longName := " " + strings.Repeat("名", 260) + " "
	longValue := " " + strings.Repeat("値", 260) + " "
	rawFields, err := json.Marshal([]map[string]string{
		{"name": " Website ", "value": " https://example.test ", "verified_at": "2026-06-18T12:00:00Z"},
		{"name": longName, "value": longValue},
		{"name": " ", "value": "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	account := models.Account{
		ID:              42,
		Username:        "alice",
		DisplayName:     "Alice",
		Note:            "raw note",
		CreatedAt:       time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Discoverable:    sql.NullBool{Bool: true, Valid: true},
		HideCollections: sql.NullBool{Bool: true, Valid: true},
		Indexable:       true,
		Fields:          rawFields,
		AccountStat:     models.AccountStat{},
	}
	user := models.User{Settings: sql.NullString{String: `{"default_privacy":"unlisted","default_sensitive":true,"default_language":"ja"}`, Valid: true}}

	role := models.UserRole{ID: 4, Name: "Moderator", Permissions: 1 << 4, Color: "#ffcc00", Highlighted: true}
	everyone := models.UserRole{ID: -99, Permissions: 1 << 16}
	out := CredentialAccountFromModelWithRole(cfg, account, user, 3, &role, &everyone)
	if out.Source.Privacy != "unlisted" || !out.Source.Sensitive {
		t.Fatalf("source = %#v", out.Source)
	}
	if out.Source.Language == nil || *out.Source.Language != "ja" {
		t.Fatalf("language = %#v", out.Source.Language)
	}
	emptyLanguage := CredentialAccountFromModel(cfg, account, models.User{Settings: sql.NullString{String: `{"default_language":""}`, Valid: true}}, 0)
	if emptyLanguage.Source.Language == nil || *emptyLanguage.Source.Language != "" {
		t.Fatalf("empty language = %#v, want explicit empty string", emptyLanguage.Source.Language)
	}
	if out.Source.Note != "raw note" || out.Source.FollowRequestsCount != 3 {
		t.Fatalf("source = %#v", out.Source)
	}
	if len(out.Source.Fields) != 2 || out.Source.Fields[0].Name != "Website" || out.Source.Fields[0].Value != "https://example.test" {
		t.Fatalf("fields = %#v", out.Source.Fields)
	}
	if out.Source.Fields[0].VerifiedAt == nil || *out.Source.Fields[0].VerifiedAt != "2026-06-18T12:00:00Z" {
		t.Fatalf("verified_at = %#v", out.Source.Fields[0].VerifiedAt)
	}
	if len([]rune(out.Source.Fields[1].Name)) != 255 || len([]rune(out.Source.Fields[1].Value)) != 255 {
		t.Fatalf("source long field not truncated to Rails local field limit: name=%d value=%d", len([]rune(out.Source.Fields[1].Name)), len([]rune(out.Source.Fields[1].Value)))
	}
	if out.Source.HideCollections == nil || !*out.Source.HideCollections ||
		out.Source.Discoverable == nil || !*out.Source.Discoverable ||
		!out.Source.Indexable {
		t.Fatalf("source flags = %#v", out.Source)
	}
	outRole, ok := out.Role.(*Role)
	if !ok || outRole.ID != "4" || outRole.Permissions != "65552" {
		t.Fatalf("role = %#v", out.Role)
	}
}

func TestCredentialAccountFromModelSerializesNullRoleLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
	}

	body, err := json.Marshal(CredentialAccountFromModel(cfg, account, models.User{}, 0))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["source"].(map[string]any); !ok {
		t.Fatalf("source key omitted: %s", string(body))
	}
	if _, ok := payload["role"]; !ok {
		t.Fatalf("role key omitted: %s", string(body))
	}
	if payload["role"] != nil {
		t.Fatalf("role = %#v, want null in %s", payload["role"], string(body))
	}
}

func TestPreferencesFromModelUsesUserSettings(t *testing.T) {
	account := models.Account{Locked: true}
	user := models.User{Settings: sql.NullString{String: `{
		"default_privacy":"unlisted",
		"default_sensitive":true,
		"default_language":"en",
		"web.display_media":"hide_all",
		"web.expand_content_warnings":true,
		"web.auto_play":true
	}`, Valid: true}}

	out := PreferencesFromModel(config.Config{}, user, account)
	if out.PostingDefaultVisibility != "unlisted" ||
		!out.PostingDefaultSensitive ||
		out.PostingDefaultLanguage != "en" ||
		out.ReadingExpandMedia != "hide_all" ||
		!out.ReadingExpandSpoilers ||
		!out.ReadingAutoplayGIFs {
		t.Fatalf("preferences = %#v", out)
	}
}

func TestPreferencesFromModelFallsBackToLocaleLanguage(t *testing.T) {
	account := models.Account{}
	user := models.User{Locale: sql.NullString{String: "fr", Valid: true}}

	out := PreferencesFromModel(config.Config{DefaultLocale: "en"}, user, account)
	if out.PostingDefaultLanguage != "fr" {
		t.Fatalf("language = %#v", out.PostingDefaultLanguage)
	}
	if out.PostingDefaultVisibility != "public" {
		t.Fatalf("visibility = %#v", out.PostingDefaultVisibility)
	}
}

func TestWebPushSubscriptionFromModelUsesMastodonKeys(t *testing.T) {
	cfg := config.Config{VapidPublicKey: "server-key"}
	out := WebPushSubscriptionFromModel(cfg, models.WebPushSubscription{
		ID:       9,
		Endpoint: "https://push.example/1",
		Data:     models.JSONValue(`{"policy":"followed","alerts":{"mention":"true","reblog":0,"status":"no","follow":"f","poll":"","update":null}}`),
	})

	if out.ID != "9" || out.Endpoint != "https://push.example/1" {
		t.Fatalf("subscription = %#v", out)
	}
	if out.ServerKey != "server-key" || out.Policy != "followed" {
		t.Fatalf("subscription = %#v", out)
	}
	if out.Alerts["mention"] != true || out.Alerts["reblog"] != false || out.Alerts["status"] != true || out.Alerts["follow"] != false {
		t.Fatalf("alerts = %#v", out.Alerts)
	}
	if value, ok := out.Alerts["poll"]; !ok || value != nil {
		t.Fatalf("blank alert = %#v ok=%v", value, ok)
	}
	if value, ok := out.Alerts["update"]; !ok || value != nil {
		t.Fatalf("null alert = %#v ok=%v", value, ok)
	}
}

func TestScheduledStatusFromModelOmitsApplicationID(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := ScheduledStatusFromModel(cfg, models.ScheduledStatus{
		ID:          7,
		ScheduledAt: sql.NullTime{Time: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC), Valid: true},
		Params:      models.JSONValue(`{"text":"hello","application_id":42}`),
	})

	if out.ID != "7" || out.ScheduledAt == nil || *out.ScheduledAt != "2026-06-18T12:00:00.000Z" {
		t.Fatalf("scheduled status = %#v", out)
	}
	if out.Params["text"] != "hello" {
		t.Fatalf("params = %#v", out.Params)
	}
	if _, ok := out.Params["application_id"]; ok {
		t.Fatalf("application_id leaked: %#v", out.Params)
	}
}

func TestScheduledStatusFromModelSerializesNilScheduledAtLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := ScheduledStatusFromModel(cfg, models.ScheduledStatus{
		ID:     7,
		Params: models.JSONValue(`{"text":"hello"}`),
	})
	if out.ScheduledAt != nil {
		t.Fatalf("nil scheduled_at should remain JSON null-compatible: %#v", out)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"scheduled_at":null`) {
		t.Fatalf("serialized scheduled status = %s", raw)
	}
}

func TestScheduledStatusFromModelSortsLegacyMediaAttachmentsByID(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := ScheduledStatusFromModel(cfg, models.ScheduledStatus{
		ID: 7,
		MediaAttachments: []models.MediaAttachment{
			{ID: 9, RemoteURL: "https://cdn.example.test/9.png"},
			{ID: 4, RemoteURL: "https://cdn.example.test/4.png"},
		},
	})
	if len(out.MediaAttachments) != 2 || out.MediaAttachments[0].ID != "4" || out.MediaAttachments[1].ID != "9" {
		t.Fatalf("scheduled media attachments = %#v", out.MediaAttachments)
	}
}

func TestNotificationFromModelSerializesStatusOnlyForRailsStatusTypes(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	target := &models.Status{
		ID:        100,
		Text:      "hello",
		CreatedAt: now,
		AccountID: 7,
		Account:   models.Account{ID: 7, Username: "bob", CreatedAt: now},
	}
	base := models.Notification{
		ID:        10,
		CreatedAt: now,
		FromAccount: models.Account{
			ID:        7,
			Username:  "bob",
			CreatedAt: now,
		},
		TargetStatus: target,
	}

	withStatus := base
	withStatus.Type = "mention"
	withStatusBody, err := json.Marshal(NotificationFromModel(cfg, withStatus, &models.Account{ID: 42}))
	if err != nil {
		t.Fatal(err)
	}
	var withStatusPayload map[string]any
	if err := json.Unmarshal(withStatusBody, &withStatusPayload); err != nil {
		t.Fatal(err)
	}
	if _, ok := withStatusPayload["status"].(map[string]any); !ok {
		t.Fatalf("mention notification omitted status: %s", string(withStatusBody))
	}

	withoutStatus := base
	withoutStatus.Type = "follow"
	withoutStatusBody, err := json.Marshal(NotificationFromModel(cfg, withoutStatus, &models.Account{ID: 42}))
	if err != nil {
		t.Fatal(err)
	}
	var withoutStatusPayload map[string]any
	if err := json.Unmarshal(withoutStatusBody, &withoutStatusPayload); err != nil {
		t.Fatal(err)
	}
	if _, ok := withoutStatusPayload["status"]; ok {
		t.Fatalf("follow notification serialized status: %s", string(withoutStatusBody))
	}
}

func TestV1FilterFromKeywordUsesKeywordIDAndParentFilter(t *testing.T) {
	expiresAt := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	out := V1FilterFromKeyword(models.CustomFilterKeyword{
		ID:        9,
		Keyword:   "spoiler",
		WholeWord: true,
		CustomFilter: models.CustomFilter{
			Context:   models.StringArray{"home", "notifications"},
			Action:    1,
			ExpiresAt: sql.NullTime{Time: expiresAt, Valid: true},
		},
	})

	if out.ID != "9" || out.Phrase != "spoiler" || !out.WholeWord || !out.Irreversible {
		t.Fatalf("filter = %#v", out)
	}
	if len(out.Context) != 2 || out.Context[0] != "home" {
		t.Fatalf("context = %#v", out.Context)
	}
	if out.ExpiresAt == nil || *out.ExpiresAt != "2026-06-18T12:00:00.000Z" {
		t.Fatalf("expires_at = %#v", out.ExpiresAt)
	}
}

func TestInstanceFromConfigMarksTranslationDisabled(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InstanceFromConfig(cfg, nil)
	translation, ok := out.Configuration["translation"].(map[string]any)
	if !ok {
		t.Fatalf("translation config missing: %#v", out.Configuration)
	}
	if translation["enabled"] != false {
		t.Fatalf("translation enabled = %#v", translation["enabled"])
	}

	cfg = config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", DeepLAPIKey: " ", LibreTranslateEndpoint: " "}
	out = InstanceFromConfig(cfg, nil)
	translation = out.Configuration["translation"].(map[string]any)
	if translation["enabled"] != false {
		t.Fatalf("whitespace translation enabled = %#v", translation["enabled"])
	}
}

func TestInstanceFromConfigUsesRailsPaonVersionShape(t *testing.T) {
	cfg := config.Config{
		LocalDomain:     "example.test",
		WebDomain:       "example.test",
		Scheme:          "https",
		Version:         "6.0.2+nightly",
		MastodonVersion: "4.2.27",
	}
	out := InstanceFromConfig(cfg, nil)
	if out.Version != "4.2.27 (compatible; Paon/6.0.2+nightly)" {
		t.Fatalf("version = %q", out.Version)
	}
	if out.ActualVersion != "6.0.2+nightly" {
		t.Fatalf("actual_version = %q", out.ActualVersion)
	}

	initial := InitialStateFromConfig(cfg, nil, "")
	if initial.Meta["version"] != out.Version || initial.Meta["actual_version"] != out.ActualVersion {
		t.Fatalf("initial state versions = %#v/%#v", initial.Meta["version"], initial.Meta["actual_version"])
	}
}

func TestInstanceFromConfigMarksTranslationEnabled(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", LibreTranslateEndpoint: "https://translate.example.test"}
	out := InstanceFromConfig(cfg, nil)
	translation, ok := out.Configuration["translation"].(map[string]any)
	if !ok {
		t.Fatalf("translation config missing: %#v", out.Configuration)
	}
	if translation["enabled"] != true {
		t.Fatalf("translation enabled = %#v", translation["enabled"])
	}
}

func TestInstanceFromConfigIncludesMastodonConfigurationLimits(t *testing.T) {
	cfg := config.Config{
		LocalDomain:         "example.test",
		WebDomain:           "example.test",
		Scheme:              "https",
		StatusMaxChars:      7000,
		MaxMedia:            8,
		ImageSizeLimit:      50 * 1024 * 1024,
		VideoSizeLimit:      120 * 1024 * 1024,
		MatrixLimit:         123456,
		DynamoDBEnabled:     true,
		StreamingAPIBaseURL: "wss://streaming.example.test",
	}
	out := InstanceFromConfig(cfg, nil)
	if !out.FeatureQuote {
		t.Fatal("feature_quote was not enabled from config")
	}

	urls, ok := out.Configuration["urls"].(map[string]any)
	if !ok || urls["streaming"] != "wss://streaming.example.test" || urls["status"] != "" {
		t.Fatalf("urls config = %#v", out.Configuration["urls"])
	}
	accounts, ok := out.Configuration["accounts"].(map[string]any)
	if !ok || accounts["max_featured_tags"] != 10 {
		t.Fatalf("accounts config = %#v", out.Configuration["accounts"])
	}
	statuses, ok := out.Configuration["statuses"].(map[string]any)
	if !ok || statuses["max_characters"] != 7000 || statuses["max_media_attachments"] != 8 || statuses["characters_reserved_per_url"] != 23 {
		t.Fatalf("statuses config = %#v", out.Configuration["statuses"])
	}
	cfg.MaxMedia = 0
	cfg.MaxMediaSet = true
	cfg.StatusMaxChars = 0
	cfg.StatusMaxCharsSet = true
	zeroLimit := InstanceFromConfig(cfg, nil).Configuration["statuses"].(map[string]any)
	if zeroLimit["max_media_attachments"] != 0 {
		t.Fatalf("explicit Rails-style zero media limit = %#v", zeroLimit["max_media_attachments"])
	}
	if zeroLimit["max_characters"] != 0 {
		t.Fatalf("explicit Rails-style zero status limit = %#v", zeroLimit["max_characters"])
	}
	cfg.ImageSizeLimit = 0
	cfg.ImageSizeLimitSet = true
	cfg.VideoSizeLimit = 0
	cfg.VideoSizeLimitSet = true
	cfg.MatrixLimit = 0
	cfg.MatrixLimitSet = true
	zeroMedia := InstanceFromConfig(cfg, nil).Configuration["media_attachments"].(map[string]any)
	for _, key := range []string{"image_size_limit", "image_matrix_limit", "video_size_limit", "video_matrix_limit"} {
		if zeroMedia[key] != 0 {
			t.Fatalf("explicit Rails-style zero %s = %#v", key, zeroMedia[key])
		}
	}
	media, ok := out.Configuration["media_attachments"].(map[string]any)
	if !ok {
		t.Fatalf("media config missing: %#v", out.Configuration)
	}
	supported, ok := media["supported_mime_types"].([]string)
	if !ok {
		t.Fatalf("supported mime types = %#v", media["supported_mime_types"])
	}
	for _, want := range []string{"image/heic", "image/avif", "video/webm", "video/quicktime", "audio/x-m4a", "video/x-ms-asf"} {
		if !containsString(supported, want) {
			t.Fatalf("supported mime types missing %s: %#v", want, supported)
		}
	}
	for key, want := range map[string]any{
		"image_size_limit":       50 * 1024 * 1024,
		"image_matrix_limit":     123456,
		"video_size_limit":       120 * 1024 * 1024,
		"video_frame_rate_limit": 120,
		"video_matrix_limit":     123456,
	} {
		if media[key] != want {
			t.Fatalf("media[%s] = %#v, want %#v", key, media[key], want)
		}
	}
}

func TestInitialStateFromConfigIncludesRailsMediaAcceptContentTypes(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InitialStateFromConfig(cfg, nil, "")

	accept, ok := out.MediaAttachments["accept_content_types"].([]string)
	if !ok {
		t.Fatalf("accept content types = %#v", out.MediaAttachments["accept_content_types"])
	}
	for _, want := range []string{".heic", ".heif", ".avif", ".webm", ".m4a", ".wma", "image/heic", "video/quicktime", "audio/x-m4a", "video/x-ms-asf"} {
		if !containsString(accept, want) {
			t.Fatalf("accept content types missing %s: %#v", want, accept)
		}
	}
}

func TestInstanceFromConfigUsesRegistrationOptions(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InstanceFromConfigWithRegistrations(cfg, nil, InstanceRegistrationOptions{
		Mode:          "approved",
		ClosedMessage: "Closed",
		SignUpURL:     "https://sso.example.test/sign-up",
	})
	if out.Registrations["enabled"] != true {
		t.Fatalf("registrations enabled = %#v", out.Registrations["enabled"])
	}
	if out.Registrations["approval_required"] != true {
		t.Fatalf("approval_required = %#v", out.Registrations["approval_required"])
	}
	if out.Registrations["message"] != nil {
		t.Fatalf("message should be nil while registrations are enabled: %#v", out.Registrations["message"])
	}
	if out.Registrations["url"] != "https://sso.example.test/sign-up" {
		t.Fatalf("registrations url = %#v", out.Registrations["url"])
	}
}

func TestInstanceFromConfigLanguagesMatchRailsDefaultLocale(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", DefaultLocale: "en"}
	out := InstanceFromConfigWithRegistrations(cfg, nil, InstanceRegistrationOptions{})
	if len(out.Languages) != 1 || out.Languages[0] != "en" {
		t.Fatalf("instance languages = %#v, want default locale only", out.Languages)
	}
	if len(SupportedLanguageCodes()) <= 1 {
		t.Fatal("supported language list should remain available for /api/v1/instance/languages")
	}
}

func TestInstanceFromConfigDisablesRegistrationsInSingleUserMode(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", SingleUserMode: true}
	out := InstanceFromConfigWithRegistrations(cfg, nil, InstanceRegistrationOptions{Mode: "open"})
	if out.Registrations["enabled"] != false {
		t.Fatalf("registrations enabled = %#v", out.Registrations["enabled"])
	}
}

func TestInstanceFromConfigIncludesClosedRegistrationMessage(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InstanceFromConfigWithRegistrations(cfg, nil, InstanceRegistrationOptions{
		Mode:          "none",
		ClosedMessage: "Closed **today**",
	})
	if out.Registrations["enabled"] != false {
		t.Fatalf("registrations enabled = %#v", out.Registrations["enabled"])
	}
	message, ok := out.Registrations["message"].(string)
	if !ok || !strings.Contains(message, "<p>Closed <strong>today</strong></p>") {
		t.Fatalf("closed registration message = %#v", out.Registrations["message"])
	}
}

func TestInstanceFromConfigCanIncludeActiveMonthUsage(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	activeMonth := int64(42)
	out := InstanceFromConfigWithRegistrationsAndUsage(cfg, nil, InstanceRegistrationOptions{Mode: "none"}, &activeMonth)
	users, ok := out.Usage["users"].(map[string]any)
	if !ok {
		t.Fatalf("usage users = %#v", out.Usage["users"])
	}
	if users["active_month"] != int64(42) {
		t.Fatalf("active_month = %#v", users["active_month"])
	}
}

func TestInstanceFromConfigWithOptionsUsesMetadata(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", Title: "Fallback"}
	contactAccount := models.Account{
		ID:          7,
		Username:    "admin",
		DisplayName: "Admin",
		CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountStat: models.AccountStat{},
	}
	out := InstanceFromConfigWithOptions(cfg, nil, InstanceRegistrationOptions{Mode: "none"}, nil, InstanceMetadata{
		Title:            "Configured title",
		ShortDescription: "Short",
		ContactEmail:     "admin@example.test",
		ContactAccount:   &contactAccount,
		Thumbnail: &models.SiteUpload{
			ID:           12,
			Var:          "thumbnail",
			FileFileName: sql.NullString{String: "hero.png", Valid: true},
			Blurhash:     sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
		},
		PreviewImageURL: "https://assets.example.test/preview.png",
		Rules: []models.Rule{
			{ID: 2, Text: "Be kind"},
		},
		StatusPageURL: "https://status.example.test",
	})
	if out.Title != "Configured title" || out.Description != "Short" {
		t.Fatalf("title/description = %q/%q", out.Title, out.Description)
	}
	if out.Contact["email"] != "admin@example.test" {
		t.Fatalf("contact = %#v", out.Contact)
	}
	account, ok := out.Contact["account"].(Account)
	if !ok || account.ID != "7" || account.Username != "admin" {
		t.Fatalf("contact account = %#v", out.Contact["account"])
	}
	urls, ok := out.Configuration["urls"].(map[string]any)
	if !ok || urls["status"] != "https://status.example.test" {
		t.Fatalf("urls = %#v", out.Configuration["urls"])
	}
	if out.Email != "" || out.ShortDescription != "" {
		t.Fatalf("legacy fields leaked into v2 instance: email=%q short=%q", out.Email, out.ShortDescription)
	}
	if out.Thumbnail["url"] != "https://example.test/system/site_uploads/files/000/000/012/@1x/hero.png" || out.Thumbnail["blurhash"] != "LEHV6nWB2yk8pyo0adR*.7kCMdnj" {
		t.Fatalf("thumbnail = %#v", out.Thumbnail)
	}
	versions, ok := out.Thumbnail["versions"].(map[string]string)
	if !ok || versions["@2x"] != "https://example.test/system/site_uploads/files/000/000/012/@2x/hero.png" {
		t.Fatalf("thumbnail versions = %#v", out.Thumbnail["versions"])
	}
	if v1 := InstanceV1ThumbnailFromSiteUpload(cfg, &models.SiteUpload{ID: 12, Var: "thumbnail", FileFileName: sql.NullString{String: "hero.png", Valid: true}}, ""); v1 != "https://example.test/system/site_uploads/files/000/000/012/@1x/hero.png" {
		t.Fatalf("v1 thumbnail = %q", v1)
	}
	jpgUpload := &models.SiteUpload{ID: 13, Var: "thumbnail", FileFileName: sql.NullString{String: "hero.jpg", Valid: true}}
	if jpgThumbnail := InstanceThumbnailFromSiteUpload(cfg, jpgUpload, ""); jpgThumbnail["url"] != "https://example.test/system/site_uploads/files/000/000/013/@1x/hero.png" {
		t.Fatalf("jpg thumbnail = %#v", jpgThumbnail)
	}
	if v1 := InstanceV1ThumbnailFromSiteUpload(cfg, jpgUpload, ""); v1 != "https://example.test/system/site_uploads/files/000/000/013/@1x/hero.png" {
		t.Fatalf("jpg v1 thumbnail = %q", v1)
	}
	if len(out.Rules) != 1 {
		t.Fatalf("rules = %#v", out.Rules)
	}
	rule, ok := out.Rules[0].(InstanceRule)
	if !ok || rule.ID != "2" || rule.Text != "Be kind" {
		t.Fatalf("rule = %#v", out.Rules[0])
	}
}

func TestInstanceFromConfigPreservesExplicitBlankMetadataTitle(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", Title: "Fallback"}
	out := InstanceFromConfigWithOptions(cfg, nil, InstanceRegistrationOptions{Mode: "none"}, nil, InstanceMetadata{Title: "   ", TitleSet: true})
	if out.Title != "   " {
		t.Fatalf("explicit raw metadata title = %q", out.Title)
	}
	out = InstanceFromConfigWithOptions(cfg, nil, InstanceRegistrationOptions{Mode: "none"}, nil, InstanceMetadata{TitleSet: true})
	if out.Title != "" {
		t.Fatalf("explicit blank metadata title = %q", out.Title)
	}
	out = InstanceFromConfigWithOptions(cfg, nil, InstanceRegistrationOptions{Mode: "none"}, nil, InstanceMetadata{})
	if out.Title != "Fallback" {
		t.Fatalf("missing metadata title fallback = %q", out.Title)
	}
}

func TestInitialStateFromConfigCanMarkRegistrationsOpen(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	closed := InitialStateFromConfig(cfg, nil, "")
	if closed.Meta["registrations_open"] != false {
		t.Fatalf("default registrations_open = %#v", closed.Meta["registrations_open"])
	}
	open := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{RegistrationsOpen: true})
	if open.Meta["registrations_open"] != true {
		t.Fatalf("open registrations_open = %#v", open.Meta["registrations_open"])
	}
}

func TestInitialStateFromConfigIncludesBlankComposeTextLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InitialStateFromConfig(cfg, nil, "")
	if out.Compose["text"] != "" {
		t.Fatalf("blank compose text = %#v, compose = %#v", out.Compose["text"], out.Compose)
	}

	withText := InitialStateFromConfigWithComposeText(cfg, nil, "", "Hello")
	if withText.Compose["text"] != "Hello" {
		t.Fatalf("compose text = %#v", withText.Compose["text"])
	}
}

func TestInitialStateFromConfigIncludesMascotURL(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	closed := InitialStateFromConfig(cfg, nil, "")
	if closed.Meta["mascot"] != nil {
		t.Fatalf("default mascot = %#v", closed.Meta["mascot"])
	}
	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{MascotURL: "https://example.test/system/site_uploads/files/000/000/003/original/mascot.png"})
	if out.Meta["mascot"] != "https://example.test/system/site_uploads/files/000/000/003/original/mascot.png" {
		t.Fatalf("mascot = %#v", out.Meta["mascot"])
	}
	upload := models.SiteUpload{ID: 3, FileFileName: sql.NullString{String: "mascot.png", Valid: true}}
	if url := SiteUploadFileURL(cfg, upload, "original"); url != "https://example.test/system/site_uploads/files/000/000/003/original/mascot.png" {
		t.Fatalf("site upload url = %q", url)
	}
}

func TestInitialStateFromConfigIncludesSingleUserMode(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", SingleUserMode: true}
	out := InitialStateFromConfig(cfg, nil, "")
	if out.Meta["single_user_mode"] != true {
		t.Fatalf("single_user_mode = %#v", out.Meta["single_user_mode"])
	}
}

func TestInitialStateFromConfigIncludesAdminAndOwnerAccounts(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", SingleUserMode: true}
	admin := models.Account{ID: 7, Username: "admin", CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)}
	owner := models.Account{ID: 8, Username: "owner", CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)}

	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{
		AdminAccount: &admin,
		OwnerAccount: &owner,
	})

	if out.Meta["admin"] != "7" || out.Meta["owner"] != "8" {
		t.Fatalf("meta admin/owner = %#v", out.Meta)
	}
	if out.Accounts["7"].Username != "admin" || out.Accounts["8"].Username != "owner" {
		t.Fatalf("accounts = %#v", out.Accounts)
	}
}

func TestInitialStateFromConfigIncludesRole(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	role := models.UserRole{ID: 4, Name: "Moderator", Permissions: 1 << 4, Color: "#ffcc00", Highlighted: true}
	everyone := models.UserRole{ID: -99, Permissions: 1 << 16}

	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{Role: &role, EveryoneRole: &everyone})
	if out.Role == nil {
		t.Fatal("role missing")
	}
	if out.Role.ID != "4" || out.Role.Name != "Moderator" || out.Role.Permissions != "65552" || out.Role.Color != "#ffcc00" || !out.Role.Highlighted {
		t.Fatalf("role = %#v", out.Role)
	}
}

func TestInitialStateJSONIncludesNullRoleLikeRailsHasOne(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InitialStateFromConfig(cfg, nil, "")

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"role":null`) {
		t.Fatalf("initial state omitted null role: %s", raw)
	}
}

func TestRoleFromModelExpandsAdministratorPermissions(t *testing.T) {
	role := RoleFromModel(models.UserRole{ID: 1, Permissions: 1}, &models.UserRole{ID: -99, Permissions: 1 << 16})
	if role.Permissions != "1048575" {
		t.Fatalf("admin permissions = %q", role.Permissions)
	}
}

func TestInitialStateFromConfigIncludesLimitedFederationMode(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", LimitedFederationMode: true}
	out := InitialStateFromConfig(cfg, nil, "")
	if out.Meta["limited_federation_mode"] != true {
		t.Fatalf("limited_federation_mode = %#v", out.Meta["limited_federation_mode"])
	}
}

func TestInitialStateFromConfigUsesConfiguredLocale(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", DefaultLocale: "en"}
	out := InitialStateFromConfig(cfg, nil, "")
	if out.Meta["locale"] != "en" {
		t.Fatalf("locale = %#v", out.Meta["locale"])
	}
}

func TestInitialStateFromConfigUsesUnicodeDomainLikeRails(t *testing.T) {
	ascii := InitialStateFromConfig(config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}, nil, "")
	if ascii.Meta["domain"] != "example.test" {
		t.Fatalf("ascii domain = %#v", ascii.Meta["domain"])
	}

	unicode := InitialStateFromConfig(config.Config{LocalDomain: "xn--r8jz45g.xn--zckzah", WebDomain: "xn--r8jz45g.xn--zckzah", Scheme: "https"}, nil, "")
	if unicode.Meta["domain"] != "例え.テスト" {
		t.Fatalf("unicode domain = %#v", unicode.Meta["domain"])
	}
}

func TestInitialStateFromConfigUsesStreamingAPIBaseURL(t *testing.T) {
	cfg := config.Config{
		LocalDomain:         "example.test",
		WebDomain:           "example.test",
		Scheme:              "https",
		StreamingAPIBaseURL: "wss://streaming.example.test/",
	}
	out := InitialStateFromConfig(cfg, nil, "")
	if out.Meta["streaming_api_base_url"] != "wss://streaming.example.test/" {
		t.Fatalf("streaming_api_base_url = %#v", out.Meta["streaming_api_base_url"])
	}
}

func TestSupportedLanguageRowsMatchRailsShape(t *testing.T) {
	rows := SupportedLanguageRows()
	if len(rows) < 180 {
		t.Fatalf("supported language count = %d", len(rows))
	}
	if rows[0][0] != "aa" || rows[0][1] != "Afar" || rows[0][2] != "Afaraf" {
		t.Fatalf("first language row = %#v", rows[0])
	}
	var ja []string
	for _, row := range rows {
		if row[0] == "ja" {
			ja = row
			break
		}
	}
	if ja == nil || ja[1] != "Japanese" || ja[2] != "日本語" {
		t.Fatalf("Japanese language row = %#v", ja)
	}
}

func TestLanguageRESTPayloadOmitsNativeNameLikeRailsSerializer(t *testing.T) {
	body, err := json.Marshal(Language{Code: "ja", Name: "Japanese"})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"code":"ja","name":"Japanese"}` {
		t.Fatalf("language payload = %s", string(body))
	}
}

func TestInitialStateFromConfigIncludesQuoteAndSSOMeta(t *testing.T) {
	cfg := config.Config{
		LocalDomain:     "example.test",
		WebDomain:       "example.test",
		Scheme:          "https",
		DynamoDBEnabled: true,
		SSORedirect:     "/auth/auth/openid_connect",
	}
	out := InitialStateFromConfig(cfg, nil, "")
	if out.Meta["feature_quote"] != true {
		t.Fatalf("feature_quote = %#v", out.Meta["feature_quote"])
	}
	if out.Meta["sso_redirect"] != "/auth/auth/openid_connect" {
		t.Fatalf("sso_redirect = %#v", out.Meta["sso_redirect"])
	}
}

func TestInitialStateMetaCoversFrontendGetMetaKeys(t *testing.T) {
	src, err := os.ReadFile("../../../app/javascript/mastodon/initial_state.js")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`getMeta\('([^']+)'\)`)
	matches := pattern.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("frontend initial_state.js getMeta keys not found")
	}

	cfg := config.Config{
		LocalDomain:           "example.test",
		WebDomain:             "example.test",
		Scheme:                "https",
		SingleUserMode:        true,
		DynamoDBEnabled:       true,
		LimitedFederationMode: true,
	}
	current := models.Account{ID: 42, Username: "alice", MovedToAccountID: sql.NullInt64{Int64: 84, Valid: true}}
	owner := models.Account{ID: 99, Username: "owner"}
	moved := models.Account{ID: 84, Username: "alice_new"}
	disabled := models.Account{ID: 77, Username: "disabled"}
	user := models.User{Settings: sql.NullString{String: `{"web.advanced_layout":true,"web.trends":true}`, Valid: true}}
	out := InitialStateFromConfigWithOptions(cfg, &current, "token", InitialStateOptions{
		User:            &user,
		OwnerAccount:    &owner,
		DisabledAccount: &disabled,
		MovedToAccount:  &moved,
	})

	for _, match := range matches {
		key := match[1]
		if _, ok := out.Meta[key]; !ok {
			t.Fatalf("Go initial state meta missing frontend getMeta key %q", key)
		}
	}
}

func railsInitialStateMetaKeys(src string) []string {
	start := strings.Index(src, "  def meta\n")
	end := strings.Index(src, "\n  def compose\n")
	if start < 0 || end < 0 || end <= start {
		return nil
	}
	body := src[start:end]
	seen := map[string]bool{}
	keys := []string{}
	for _, pattern := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s+([a-z_]+):`),
		regexp.MustCompile(`store\[:([a-z_]+)\]`),
	} {
		for _, match := range pattern.FindAllStringSubmatch(body, -1) {
			key := match[1]
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

func railsRESTSerializerKeys(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := topLevelRailsSerializerBody(string(raw))
	keys := map[string]bool{}
	for _, block := range railsAttributeBlocks(body) {
		for _, key := range railsSymbolPattern.FindAllStringSubmatch(block, -1) {
			keys[key[1]] = true
		}
	}
	for _, pattern := range []*regexp.Regexp{
		railsAttributePattern,
		railsBelongsToPattern,
		railsHasManyPattern,
		railsHasOnePattern,
	} {
		for _, match := range pattern.FindAllStringSubmatch(body, -1) {
			key := firstNonEmptyString(match[2], match[3], match[1])
			if key != "" {
				keys[key] = true
			}
		}
	}
	return keys
}

func railsAttributeBlocks(src string) []string {
	blocks := []string{}
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "attributes ") {
			continue
		}
		block := strings.TrimPrefix(line, "attributes ")
		for i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if next == "" || railsTopLevelSerializerDirective(next) {
				break
			}
			i++
			block += " " + next
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func railsTopLevelSerializerDirective(line string) bool {
	for _, prefix := range []string{"attribute ", "belongs_to ", "has_many ", "has_one ", "def ", "class ", "private", "end"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func topLevelRailsSerializerBody(src string) string {
	if idx := strings.Index(src, "\n  class "); idx >= 0 {
		src = src[:idx]
	}
	return src
}

func goJSONKeys(value any, extras ...string) map[string]bool {
	keys := map[string]bool{}
	typ := reflect.TypeOf(value)
	collectGoJSONKeys(keys, typ)
	for _, key := range extras {
		keys[key] = true
	}
	return keys
}

func collectGoJSONKeys(keys map[string]bool, typ reflect.Type) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" && field.Anonymous {
			collectGoJSONKeys(keys, field.Type)
			continue
		}
		if tag == "" {
			continue
		}
		key := strings.Split(tag, ",")[0]
		if key == "" || key == "-" {
			continue
		}
		keys[key] = true
	}
}

func TestInitialStateTopLevelCoversFrontendHydrateKeys(t *testing.T) {
	root := "../../../app/javascript/mastodon/reducers"
	topLevelKeys := map[string]struct{}{}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`action\.state\.get\('([^']+)'\)`),
		regexp.MustCompile(`action\.state\.getIn\(\[['"]([^'"]+)['"]`),
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		if !strings.Contains(src, "STORE_HYDRATE") {
			return nil
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(src, -1) {
				topLevelKeys[match[1]] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(topLevelKeys) == 0 {
		t.Fatal("frontend STORE_HYDRATE keys not found")
	}

	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{ID: 42, Username: "alice"}
	user := models.User{Settings: sql.NullString{String: `{"web.advanced_layout":true}`, Valid: true}}
	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{
		User: &user,
		PushSubscription: &models.WebPushSubscription{
			ID:       12,
			Endpoint: "https://push.example/1",
			Data:     models.JSONValue(`{"policy":"all","alerts":{"mention":true}}`),
		},
	})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	encoded := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	for key := range topLevelKeys {
		if _, ok := encoded[key]; !ok {
			t.Fatalf("Go initial state missing frontend STORE_HYDRATE top-level key %q", key)
		}
	}
}

func TestInitialStateTopLevelCoversFrontendDirectInitialStateKeys(t *testing.T) {
	root := "../../../app/javascript/mastodon"
	topLevelKeys := map[string]struct{}{}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`initialState(?:\?\.)?\.([A-Za-z0-9_]+)`),
		regexp.MustCompile(`initialState\[['"]([^'"]+)['"]\]`),
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !frontendInitialStateSourceFile(path) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		if !strings.Contains(src, "initialState") {
			return nil
		}
		if !frontendUsesGlobalInitialState(path, src) {
			return nil
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(src, -1) {
				topLevelKeys[match[1]] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(topLevelKeys) == 0 {
		t.Fatal("frontend direct initialState top-level keys not found")
	}

	pending := true
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", SingleUserMode: true}
	account := models.Account{ID: 42, Username: "alice"}
	owner := models.Account{ID: 99, Username: "owner"}
	role := models.UserRole{ID: 4, Name: "Moderator", Permissions: 8}
	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{
		OwnerAccount:           &owner,
		Role:                   &role,
		CriticalUpdatesPending: &pending,
	})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	encoded := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	for key := range topLevelKeys {
		if _, ok := encoded[key]; !ok {
			t.Fatalf("Go initial state missing frontend direct initialState top-level key %q", key)
		}
	}
}

func frontendUsesGlobalInitialState(path string, src string) bool {
	if strings.HasSuffix(filepath.ToSlash(path), "/initial_state.js") {
		return true
	}
	return strings.Contains(src, "from 'mastodon/initial_state'") ||
		strings.Contains(src, `from "mastodon/initial_state"`) ||
		strings.Contains(src, "from '../initial_state'") ||
		strings.Contains(src, "from '../../initial_state'") ||
		strings.Contains(src, "from '../../../initial_state'")
}

func frontendInitialStateSourceFile(path string) bool {
	switch filepath.Ext(path) {
	case ".js", ".jsx", ".ts", ".tsx":
		return true
	default:
		return false
	}
}

func TestInstanceFromConfigUsesRailsDefaultLocaleLanguage(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", DefaultLocale: "en"}
	out := InstanceFromConfig(cfg, nil)
	if len(out.Languages) != 1 || out.Languages[0] != "en" {
		t.Fatalf("instance languages = %#v", out.Languages)
	}
}

func TestInitialStateFromConfigUsesServerSettingDefaults(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InitialStateFromConfig(cfg, nil, "")

	for key, want := range map[string]any{
		"profile_directory":      true,
		"trends_enabled":         true,
		"timeline_preview":       true,
		"activity_api_enabled":   true,
		"trends_as_landing_page": true,
		"status_page_url":        "",
		"auto_play_gif":          nil,
		"display_media":          nil,
		"reduce_motion":          nil,
		"use_blurhash":           nil,
		"crop_images":            nil,
	} {
		if out.Meta[key] != want {
			t.Fatalf("meta[%s] = %#v, want %#v", key, out.Meta[key], want)
		}
	}
}

func TestInitialStateFromConfigOmitsAuthenticatedOnlyMetaForAnonymousUsers(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InitialStateFromConfig(cfg, nil, "")

	for _, key := range []string{
		"unfollow_modal",
		"boost_modal",
		"delete_modal",
		"expand_spoilers",
		"disable_swiping",
		"advanced_layout",
		"use_pending_items",
		"show_trends",
	} {
		if _, ok := out.Meta[key]; ok {
			t.Fatalf("anonymous meta unexpectedly includes %s: %#v", key, out.Meta[key])
		}
	}
}

func TestInitialStateFromConfigUsesServerSettings(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	settings := DefaultInitialStateServerSettings()
	settings.ProfileDirectory = false
	settings.TrendsEnabled = false
	settings.TimelinePreview = false
	settings.ActivityAPIEnabled = false
	settings.TrendsAsLandingPage = false
	settings.StatusPageURL = "https://status.example.test"
	settings.AutoPlayGIF = true
	settings.DisplayMedia = "hide_all"
	settings.ReduceMotion = true
	settings.UseBlurhash = false
	settings.CropImages = false

	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{ServerSettings: &settings})
	for key, want := range map[string]any{
		"profile_directory":      false,
		"trends_enabled":         false,
		"timeline_preview":       false,
		"activity_api_enabled":   false,
		"trends_as_landing_page": false,
		"status_page_url":        "https://status.example.test",
		"auto_play_gif":          true,
		"display_media":          "hide_all",
		"reduce_motion":          true,
		"use_blurhash":           false,
		"crop_images":            false,
	} {
		if out.Meta[key] != want {
			t.Fatalf("meta[%s] = %#v, want %#v", key, out.Meta[key], want)
		}
	}
}

func TestInitialStateFromConfigIncludesWebSettings(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{
		Settings: map[string]any{"boost_modal": true, "skin": "default"},
	})
	if out.Settings["boost_modal"] != true || out.Settings["skin"] != "default" {
		t.Fatalf("settings = %#v", out.Settings)
	}
}

func TestInitialStateFromConfigIncludesPushSubscription(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", VapidPublicKey: "server-key"}
	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{
		PushSubscription: &models.WebPushSubscription{
			ID:       12,
			Endpoint: "https://push.example/1",
			Data:     models.JSONValue(`{"policy":"all","alerts":{"mention":true}}`),
		},
	})
	if out.PushSubscription == nil {
		t.Fatal("push subscription missing")
	}
	if out.PushSubscription.ID != "12" || out.PushSubscription.Endpoint != "https://push.example/1" || out.PushSubscription.ServerKey != "server-key" {
		t.Fatalf("push subscription = %#v", out.PushSubscription)
	}
	if out.PushSubscription.Alerts["mention"] != true {
		t.Fatalf("alerts = %#v", out.PushSubscription.Alerts)
	}
}

func TestExtendedDescriptionMarkdownRendersRailsCommonBlocks(t *testing.T) {
	setting := &models.Setting{
		Value: sql.NullString{String: "# About\n\nWelcome to **Paon** and [docs](https://example.test/docs).\n\n1. One\n2. Two\n\n> Federation first\n\n```\n<safe>\n```", Valid: true},
	}
	out := ExtendedDescriptionFromSetting(setting)
	for _, want := range []string{
		"<h1>About</h1>",
		"<strong>Paon</strong>",
		`<a href="https://example.test/docs" rel="nofollow noopener noreferrer" target="_blank">docs</a>`,
		"<ol>",
		"<li>One</li>",
		"<blockquote>",
		"<pre><code>&lt;safe&gt;</code></pre>",
	} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("extended description markdown missing %q: %s", want, out.Content)
		}
	}
}

func TestPrivacyPolicyMarkdownEscapesHTMLAndImages(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test"}
	setting := &models.Setting{
		Value: sql.NullString{String: "Hello <script>alert(1)</script> **%{domain}** ![bad](https://example.test/image.png)", Valid: true},
	}
	out := PrivacyPolicyFromSetting(cfg, setting)
	for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "<strong>example.test</strong>"} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("privacy policy markdown missing %q: %s", want, out.Content)
		}
	}
	if strings.Contains(out.Content, "<script>") || strings.Contains(out.Content, "<img") {
		t.Fatalf("privacy policy rendered unsafe html/image: %s", out.Content)
	}
}

func TestInitialStateFromConfigIncludesComposeDefaults(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	user := models.User{
		Settings: sql.NullString{String: `{"default_privacy":"unlisted","default_sensitive":true,"default_language":"en"}`, Valid: true},
	}
	account := models.Account{ID: 42, Username: "alice"}
	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{User: &user})

	if out.Compose["me"] != "42" || out.Compose["default_privacy"] != "unlisted" || out.Compose["default_sensitive"] != true || out.Compose["default_language"] != "en" {
		t.Fatalf("compose = %#v", out.Compose)
	}
}

func TestInitialStateFromConfigComposePrivacyFallsBackToPrivateForLockedAccount(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	user := models.User{}
	account := models.Account{ID: 42, Username: "alice", Locked: true}
	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{User: &user})

	if out.Compose["default_privacy"] != "private" {
		t.Fatalf("default_privacy = %#v, want private", out.Compose["default_privacy"])
	}
}

func TestInitialStateFromConfigIncludesAuthenticatedMetaSettings(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	user := models.User{
		Settings: sql.NullString{String: `{
			"web.unfollow_modal":false,
			"web.reblog_modal":true,
			"web.delete_modal":false,
			"web.auto_play":true,
			"web.display_media":"show_all",
			"web.expand_content_warnings":true,
			"web.reduce_motion":true,
			"web.disable_swiping":true,
			"web.advanced_layout":true,
			"web.use_blurhash":false,
			"web.use_pending_items":true,
			"web.trends":false,
			"web.crop_images":false
		}`, Valid: true},
	}
	account := models.Account{ID: 42, Username: "alice"}
	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{User: &user})

	for key, want := range map[string]any{
		"unfollow_modal":    false,
		"boost_modal":       true,
		"delete_modal":      false,
		"auto_play_gif":     true,
		"display_media":     "show_all",
		"expand_spoilers":   true,
		"reduce_motion":     true,
		"disable_swiping":   true,
		"advanced_layout":   true,
		"use_blurhash":      false,
		"use_pending_items": true,
		"show_trends":       false,
		"crop_images":       false,
	} {
		if out.Meta[key] != want {
			t.Fatalf("meta[%s] = %#v, want %#v", key, out.Meta[key], want)
		}
	}
}

func TestInitialStateFromConfigUsesAuthenticatedMetaDefaults(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	user := models.User{}
	account := models.Account{ID: 42, Username: "alice"}
	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{User: &user})

	for key, want := range map[string]any{
		"unfollow_modal":    true,
		"boost_modal":       false,
		"delete_modal":      true,
		"auto_play_gif":     false,
		"display_media":     "default",
		"expand_spoilers":   false,
		"reduce_motion":     false,
		"disable_swiping":   false,
		"advanced_layout":   false,
		"use_blurhash":      true,
		"use_pending_items": false,
		"show_trends":       true,
		"crop_images":       true,
	} {
		if out.Meta[key] != want {
			t.Fatalf("meta[%s] = %#v, want %#v", key, out.Meta[key], want)
		}
	}
}

func TestInitialStateFromConfigDisablesUserTrendsWhenServerTrendsDisabled(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	settings := DefaultInitialStateServerSettings()
	settings.TrendsEnabled = false
	user := models.User{
		Settings: sql.NullString{String: `{"web.trends":true}`, Valid: true},
	}
	account := models.Account{ID: 42, Username: "alice"}

	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{
		ServerSettings: &settings,
		User:           &user,
	})

	if out.Meta["trends_enabled"] != false {
		t.Fatalf("trends_enabled = %#v", out.Meta["trends_enabled"])
	}
	if out.Meta["show_trends"] != false {
		t.Fatalf("show_trends = %#v, want false when server trends are disabled", out.Meta["show_trends"])
	}
}

func TestInitialStateFromConfigIncludesDisabledAndMovedAccountMeta(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{ID: 42, Username: "alice", MovedToAccountID: sql.NullInt64{Int64: 84, Valid: true}}
	moved := models.Account{ID: 84, Username: "alice_new"}

	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{
		DisabledAccount: &account,
		MovedToAccount:  &moved,
	})

	if _, ok := out.Meta["me"]; ok {
		t.Fatalf("disabled account payload should not include me: %#v", out.Meta)
	}
	if out.Meta["disabled_account_id"] != "42" {
		t.Fatalf("disabled_account_id = %#v", out.Meta["disabled_account_id"])
	}
	if out.Meta["moved_to_account_id"] != "84" {
		t.Fatalf("moved_to_account_id = %#v", out.Meta["moved_to_account_id"])
	}
	if out.Accounts["42"].Acct != "alice" {
		t.Fatalf("disabled account = %#v", out.Accounts["42"])
	}
	if out.Accounts["84"].Acct != "alice_new" {
		t.Fatalf("moved account = %#v", out.Accounts["84"])
	}
}

func TestInitialStateFromConfigOmitsDisabledAndMovedMetaByDefault(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	user := models.User{}
	account := models.Account{ID: 42, Username: "alice"}

	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{User: &user})

	if _, ok := out.Meta["disabled_account_id"]; ok {
		t.Fatalf("disabled_account_id unexpectedly set: %#v", out.Meta["disabled_account_id"])
	}
	if _, ok := out.Meta["moved_to_account_id"]; ok {
		t.Fatalf("moved_to_account_id unexpectedly set: %#v", out.Meta["moved_to_account_id"])
	}
}

func TestInitialStateFromConfigIncludesCriticalUpdatesPending(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	pending := true

	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{CriticalUpdatesPending: &pending})

	if out.CriticalUpdatesPending == nil || !*out.CriticalUpdatesPending {
		t.Fatalf("critical updates pending = %#v", out.CriticalUpdatesPending)
	}
}

func TestInitialStateFromConfigOmitsCriticalUpdatesPendingByDefault(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}

	out := InitialStateFromConfigWithOptions(cfg, nil, "", InitialStateOptions{})

	if out.CriticalUpdatesPending != nil {
		t.Fatalf("critical updates pending unexpectedly set: %#v", out.CriticalUpdatesPending)
	}
}

func TestInitialStateFromConfigComposeLanguageFallsBackToUserLocale(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	user := models.User{Locale: sql.NullString{String: "fr", Valid: true}}
	account := models.Account{ID: 42, Username: "alice"}
	out := InitialStateFromConfigWithOptions(cfg, &account, "token", InitialStateOptions{User: &user})

	if out.Compose["default_language"] != "fr" {
		t.Fatalf("compose language = %#v", out.Compose["default_language"])
	}
}

func TestCustomEmojiFromModelUsesPaperclipURLsAndCategory(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := CustomEmojiFromModel(cfg, models.CustomEmoji{
		ID:              42,
		Shortcode:       "party",
		ImageFileName:   sql.NullString{String: "party.gif", Valid: true},
		VisibleInPicker: true,
		Category:        models.CustomEmojiCategory{ID: 7, Name: models.CustomEmojiCategoryName("Reactions")},
	})

	category, ok := out.Category.(*string)
	if out.Shortcode != "party" || !out.VisibleInPicker || !ok || category == nil || *category != "Reactions" {
		t.Fatalf("CustomEmoji = %#v", out)
	}
	if out.URL != "https://example.test/system/custom_emojis/images/000/000/042/original/party.gif" {
		t.Fatalf("URL = %q", out.URL)
	}
	if out.StaticURL != "https://example.test/system/custom_emojis/images/000/000/042/static/party.png" {
		t.Fatalf("StaticURL = %q", out.StaticURL)
	}
}

func TestCustomEmojiFromModelIncludesEmptyLoadedCategory(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := CustomEmojiFromModel(cfg, models.CustomEmoji{
		ID:        43,
		Shortcode: "blank",
		Category:  models.CustomEmojiCategory{ID: 7, Name: models.CustomEmojiCategoryName("")},
	})
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	category, ok := payload["category"]
	if !ok {
		t.Fatalf("custom emoji JSON omitted loaded empty category: %s", body)
	}
	if category != "" {
		t.Fatalf("custom emoji category = %#v, want empty string", category)
	}
}

func TestCustomEmojiFromModelIncludesNullLoadedCategoryName(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := CustomEmojiFromModel(cfg, models.CustomEmoji{
		ID:        44,
		Shortcode: "nullcat",
		Category:  models.CustomEmojiCategory{ID: 7},
	})
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	category, ok := payload["category"]
	if !ok {
		t.Fatalf("custom emoji JSON omitted loaded null category: %s", body)
	}
	if category != nil {
		t.Fatalf("custom emoji category = %#v, want nil", category)
	}
}

func TestCustomEmojiFromModelUsesRemoteURLFallback(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := CustomEmojiFromModel(cfg, models.CustomEmoji{
		ID:             1,
		Shortcode:      "remote",
		ImageRemoteURL: sql.NullString{String: "https://remote.example/emoji.png", Valid: true},
	})
	if out.URL != "https://remote.example/emoji.png" || out.StaticURL != "https://remote.example/emoji.png" {
		t.Fatalf("CustomEmoji = %#v", out)
	}
}

func TestPreviewCardFromModelUsesMastodonKeys(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := PreviewCardFromModel(cfg, models.PreviewCard{
		ID:                        123,
		URL:                       "https://remote.example/articles/1",
		Title:                     "Article",
		Description:               "Description",
		Type:                      0,
		AuthorName:                "Alice",
		AuthorURL:                 "https://remote.example/@alice",
		ProviderName:              "Remote",
		ProviderURL:               "https://remote.example",
		Width:                     640,
		Height:                    360,
		ImageFileName:             sql.NullString{String: "cover.jpg", Valid: true},
		ImageStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
		Blurhash:                  sql.NullString{String: "hash", Valid: true},
		Language:                  sql.NullString{String: "en", Valid: true},
		PublishedAt:               sql.NullTime{Time: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC), Valid: true},
		ImageDescription:          "Cover",
	})

	if out.URL != "https://remote.example/articles/1" || out.Type != "link" || out.Title != "Article" {
		t.Fatalf("PreviewCard = %#v", out)
	}
	if out.Image == nil || *out.Image != "https://example.test/system/cache/preview_cards/images/000/000/123/original/cover.jpg" {
		t.Fatalf("Image = %#v", out.Image)
	}
	if out.Language == nil || *out.Language != "en" || out.PublishedAt == nil || *out.PublishedAt != "2026-06-18T12:00:00.000Z" {
		t.Fatalf("time/lang = %#v %#v", out.Language, out.PublishedAt)
	}
}

func TestPreviewCardFromModelSanitizesOEmbedHTML(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := PreviewCardFromModel(cfg, models.PreviewCard{
		HTML: `<div><iframe src="https://remote.example/embed" onload="alert(2)" allowfullscreen width="640"></iframe></div><script>alert(3)</script><b onclick=x>ok</b>`,
	})

	for _, unwanted := range []string{"<script", "</script", "alert(3)", "onload", "<div", "<b", "onclick"} {
		if strings.Contains(out.HTML, unwanted) {
			t.Fatalf("PreviewCard HTML kept %q: %s", unwanted, out.HTML)
		}
	}
	for _, want := range []string{`<iframe`, `src="https://remote.example/embed"`, `allowfullscreen=""`, `width="640"`, `sandbox="allow-scripts allow-same-origin allow-popups allow-popups-to-escape-sandbox allow-forms"`} {
		if !strings.Contains(out.HTML, want) {
			t.Fatalf("PreviewCard HTML missing %q: %s", want, out.HTML)
		}
	}
	if !strings.Contains(out.HTML, "ok") {
		t.Fatalf("sanitized text missing: %s", out.HTML)
	}
}

func TestPreviewCardFromModelPreservesOEmbedOuterWhitespace(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := PreviewCardFromModel(cfg, models.PreviewCard{
		HTML: "\n\t<iframe src=\"https://remote.example/embed\" onload=\"alert(2)\"></iframe><script>alert(3)</script>  ",
	})

	if !strings.HasPrefix(out.HTML, "\n\t") {
		t.Fatalf("PreviewCard HTML trimmed leading whitespace: %q", out.HTML)
	}
	if !strings.HasSuffix(out.HTML, "  ") {
		t.Fatalf("PreviewCard HTML trimmed trailing whitespace: %q", out.HTML)
	}
	for _, unwanted := range []string{"<script", "</script", "alert(3)", "onload"} {
		if strings.Contains(out.HTML, unwanted) {
			t.Fatalf("PreviewCard HTML kept %q: %s", unwanted, out.HTML)
		}
	}
	if !strings.Contains(out.HTML, `src="https://remote.example/embed"`) {
		t.Fatalf("PreviewCard HTML missing iframe src: %s", out.HTML)
	}
}

func TestPreviewCardOEmbedHTMLRejectsUnsafeMediaSources(t *testing.T) {
	got := sanitizePreviewCardOEmbedHTML(`<video controls height="360" width="640" poster="bad"><source src="https://cdn.example/video.mp4" type="video/mp4"><source src="javascript:alert(1)" type="video/mp4"></video><audio controls autoplay><source src="http://cdn.example/audio.mp3" type="audio/mpeg"></audio>`)
	for _, want := range []string{`<video`, `controls=""`, `height="360"`, `width="640"`, `src="https://cdn.example/video.mp4"`, `type="video/mp4"`, `</video>`, `<audio controls="">`, `src="http://cdn.example/audio.mp3"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized oembed HTML missing %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"poster", "javascript:", "autoplay"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("sanitized oembed HTML kept %q: %s", unwanted, got)
		}
	}
}

func TestPreviewCardTrendLinkFromModelIncludesHistory(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := PreviewCardTrendLinkFromModel(
		cfg,
		models.PreviewCard{ID: 1, URL: "https://remote.example", Title: "Remote"},
		5,
		2,
		time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC),
	)
	if out.URL != "https://remote.example" || out.Title != "Remote" {
		t.Fatalf("PreviewCardTrendLink = %#v", out)
	}
	if len(out.History) != 1 {
		t.Fatalf("History = %#v", out.History)
	}
	history := out.History[0].(map[string]string)
	if history["day"] != "1781827200" || history["uses"] != "5" || history["accounts"] != "2" {
		t.Fatalf("History = %#v", history)
	}
}

func TestAdminTrendLinkFromModelWithHistoryMatchesRailsTrendLinkSerializer(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := AdminTrendLinkFromModelWithHistory(
		cfg,
		models.PreviewCard{ID: 1, URL: "https://remote.example", Title: "Remote"},
		[]any{map[string]string{
			"day":      "1781827200",
			"uses":     "5",
			"accounts": "2",
		}},
		true,
	)
	if out.ID != "1" || !out.RequiresReview {
		t.Fatalf("AdminTrendLink = %#v", out)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	history, ok := raw["history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("json history = %#v", raw["history"])
	}
	first, ok := history[0].(map[string]any)
	if !ok || first["day"] != "1781827200" || first["uses"] != "5" || first["accounts"] != "2" {
		t.Fatalf("json history[0] = %#v", history[0])
	}
	if raw["requires_review"] != true {
		t.Fatalf("requires_review = %#v", raw["requires_review"])
	}
}

func TestAnnouncementStatusSerializesRemoteURLLikeRailsTagManager(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := AnnouncementFromModel(cfg, models.Announcement{
		ID:        12,
		UpdatedAt: time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC),
	}, nil, []models.Status{
		{
			ID:    100,
			Local: sql.NullBool{Bool: false, Valid: true},
			Account: models.Account{
				ID:        5,
				Username:  "alice",
				Domain:    sql.NullString{String: "remote.example", Valid: true},
				CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
			},
		},
	}, nil)

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Statuses []map[string]any `json:"statuses"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Statuses) != 1 || payload.Statuses[0]["url"] != nil {
		t.Fatalf("announcement statuses JSON = %#v in %s", payload.Statuses, string(body))
	}
}

func TestAnnouncementContentHTMLLinkifiesURLAndEscapesText(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := announcementContentHTML(cfg, models.Announcement{
		Text: `See https://www.remote.example/some/really/long/path?q=1, <script> #タグ @alice@remote.example @ghost.`,
		MentionAccounts: []models.Account{{
			Username: "alice",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
			URL:      sql.NullString{String: "https://remote.example/@alice", Valid: true},
		}},
	})

	for _, want := range []string{
		`href="https://www.remote.example/some/really/long/path?q=1" target="_blank" rel="nofollow noopener noreferrer" translate="no"`,
		`<span class="invisible">https://www.</span><span class="ellipsis">remote.example/some/really/lon</span><span class="invisible">g/path?q=1</span>`,
		`&lt;script&gt;`,
		`<a href="https://example.test/tags/%E3%82%BF%E3%82%B0" class="mention hashtag" rel="tag">#<span>タグ</span></a>`,
		`<span class="h-card" translate="no"><a href="https://remote.example/@alice" class="u-url mention">@<span>alice</span></a></span>`,
		`@ghost`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("announcement content missing %s: %s", want, out)
		}
	}
	if strings.Contains(out, `@ghost</span>`) || strings.Contains(out, `/@ghost`) {
		t.Fatalf("unresolved announcement mention should remain plain like Rails: %s", out)
	}
}

func TestFeaturedTagFromModelKeepsRemoteAcctSeparator(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := FeaturedTagFromModel(cfg, models.FeaturedTag{
		ID: 1,
		Account: models.Account{
			Username: "bob",
			Domain:   sql.NullString{String: "remote.example", Valid: true},
		},
		Tag: models.Tag{Name: "golang"},
	})

	if out.URL != "https://example.test/@bob@remote.example/tagged/golang" {
		t.Fatalf("URL = %q", out.URL)
	}
}

func TestPrivacyPolicyFromSettingFallsBackToDefault(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := PrivacyPolicyFromSetting(cfg, nil)

	if out.UpdatedAt == nil || *out.UpdatedAt != "2022-10-07T00:00:00.000Z" {
		t.Fatalf("UpdatedAt = %#v", out.UpdatedAt)
	}
	if !strings.Contains(out.Content, "example.test") {
		t.Fatalf("Content missing domain: %q", out.Content)
	}
}

func TestInstanceDomainBlockFromModelUsesDigestSeverityAndComment(t *testing.T) {
	out := InstanceDomainBlockFromModel(models.DomainBlock{
		Domain:        "blocked.example",
		Severity:      models.DomainBlockSeverity(1),
		PublicComment: sql.NullString{String: "rationale", Valid: true},
	}, true)

	if out.Domain != "blocked.example" || out.Severity != "suspend" {
		t.Fatalf("DomainBlock = %#v", out)
	}
	if out.Digest != "1f30273176bf43428242811a3bd5e04653804cff849e2ac73a14aa6e00c66a48" {
		t.Fatalf("Digest = %q", out.Digest)
	}
	if out.Comment == nil || *out.Comment != "rationale" {
		t.Fatalf("Comment = %#v", out.Comment)
	}
}

func TestAdminEmailDomainBlockFromModelWithHistoryMatchesRailsHistoryShape(t *testing.T) {
	block := models.EmailDomainBlock{
		ID:        42,
		Domain:    "blocked.example",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
	}

	out := AdminEmailDomainBlockFromModelWithHistory(block, []AdminEmailDomainBlockHistory{{
		Day:      "1781827200",
		Accounts: "3",
		Uses:     "12",
	}})
	if out.ID != "42" || out.Domain != "blocked.example" || out.CreatedAt != "2026-06-18T12:00:00.000Z" {
		t.Fatalf("AdminEmailDomainBlock = %#v", out)
	}
	if len(out.History) != 1 || out.History[0].Day != "1781827200" || out.History[0].Accounts != "3" || out.History[0].Uses != "12" {
		t.Fatalf("History = %#v", out.History)
	}

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	history, ok := raw["history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("json history = %#v", raw["history"])
	}
	first, ok := history[0].(map[string]any)
	if !ok || first["day"] != "1781827200" || first["accounts"] != "3" || first["uses"] != "12" {
		t.Fatalf("json history[0] = %#v", history[0])
	}
}

func TestReportFromModelUsesMastodonKeys(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	actionTakenAt := time.Date(2026, 6, 18, 14, 0, 0, 0, time.UTC)
	report := models.Report{
		ID:            11,
		StatusIDs:     models.Int64Array{100, 101},
		RuleIDs:       models.Int64Array{3},
		Comment:       "bad post",
		CreatedAt:     time.Date(2026, 6, 18, 13, 0, 0, 0, time.UTC),
		Forwarded:     sql.NullBool{Bool: true, Valid: true},
		Category:      2000,
		ActionTakenAt: sql.NullTime{Time: actionTakenAt, Valid: true},
		TargetAccount: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
	}

	out := ReportFromModel(cfg, report)
	if out.ID != "11" || out.Category != "violation" {
		t.Fatalf("Report = %#v", out)
	}
	if !out.ActionTaken || out.ActionTakenAt == nil || *out.ActionTakenAt != "2026-06-18T14:00:00.000Z" {
		t.Fatalf("ActionTaken = %#v %#v", out.ActionTaken, out.ActionTakenAt)
	}
	if out.Forwarded == nil || !*out.Forwarded {
		t.Fatal("Forwarded = false")
	}
	if len(out.StatusIDs) != 2 || out.StatusIDs[1] != "101" {
		t.Fatalf("StatusIDs = %#v", out.StatusIDs)
	}
	if len(out.RuleIDs) != 1 || out.RuleIDs[0] != "3" {
		t.Fatalf("RuleIDs = %#v", out.RuleIDs)
	}
	if out.TargetAccount.ID != "42" {
		t.Fatalf("TargetAccount = %#v", out.TargetAccount)
	}
}

func TestReportFromModelKeepsNilRuleIDsAsRailsNull(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	report := models.Report{
		ID:        11,
		StatusIDs: models.Int64Array{},
		RuleIDs:   nil,
		CreatedAt: time.Date(2026, 6, 18, 13, 0, 0, 0, time.UTC),
		TargetAccount: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
	}

	body, err := json.Marshal(ReportFromModel(cfg, report))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["rule_ids"] != nil {
		t.Fatalf("rule_ids = %#v, want null", raw["rule_ids"])
	}
	if raw["forwarded"] != nil {
		t.Fatalf("forwarded = %#v, want null", raw["forwarded"])
	}
	if statusIDs, ok := raw["status_ids"].([]any); !ok || len(statusIDs) != 0 {
		t.Fatalf("status_ids = %#v, want []", raw["status_ids"])
	}
}

func TestAdminReportFromModelWithAdminAccountsKeepsRailsAdminAccountDetails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	inviteRequest := "Please approve me"
	usedAt := "2026-06-20T04:05:06Z"
	reporter := AdminAccount{
		ID:            "10",
		Username:      "reporter",
		InviteRequest: &inviteRequest,
		IPs:           []AdminAccountIP{{IP: "192.0.2.10", UsedAt: &usedAt}},
		Role:          &Role{ID: "4", Name: "Moderator", Permissions: "65552", Color: "#ffcc00", Highlighted: true},
	}
	target := AdminAccount{ID: "42", Username: "target"}
	assigned := AdminAccount{ID: "99", Username: "assigned"}
	report := models.Report{
		ID:        11,
		RuleIDs:   models.Int64Array{2},
		Comment:   "bad post",
		CreatedAt: time.Date(2026, 6, 18, 13, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 18, 14, 0, 0, 0, time.UTC),
	}

	out := AdminReportFromModelWithAdminAccounts(cfg, report, nil, reporter, target, &assigned, nil, []models.Rule{{ID: 2, Text: "No spam"}})
	if out.Account.InviteRequest == nil || *out.Account.InviteRequest != "Please approve me" {
		t.Fatalf("Account invite_request = %#v", out.Account.InviteRequest)
	}
	if len(out.Account.IPs) != 1 || out.Account.IPs[0].IP != "192.0.2.10" {
		t.Fatalf("Account IPs = %#v", out.Account.IPs)
	}
	role, ok := out.Account.Role.(*Role)
	if !ok || role.ID != "4" {
		t.Fatalf("Account role = %#v", out.Account.Role)
	}
	if out.TargetAccount.ID != "42" || out.AssignedAccount == nil || out.AssignedAccount.ID != "99" {
		t.Fatalf("AdminReport accounts = %#v %#v", out.TargetAccount, out.AssignedAccount)
	}
	if out.ActionTakenByAccount != nil {
		t.Fatalf("ActionTakenByAccount = %#v", out.ActionTakenByAccount)
	}
	if len(out.Rules) != 1 {
		t.Fatalf("Rules = %#v", out.Rules)
	}
	rule, ok := out.Rules[0].(InstanceRule)
	if !ok || rule.ID != "2" || rule.Text != "No spam" {
		t.Fatalf("Rules[0] = %#v", out.Rules[0])
	}
}

func TestAdminReportFromModelWithCurrentAccountKeepsNestedStatusConditionalFields(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	report := models.Report{
		ID:        12,
		CreatedAt: now,
		UpdatedAt: now,
	}
	status := models.Status{
		ID:                  99,
		AccountID:           10,
		Text:                "reported",
		CreatedAt:           now,
		Visibility:          0,
		Account:             models.Account{ID: 10, Username: "target", CreatedAt: now},
		FavouritedByCurrent: true,
	}
	out := AdminReportFromModelWithAdminAccountsAndCurrent(
		cfg,
		report,
		[]models.Status{status},
		AdminAccount{ID: "2", Username: "reporter"},
		AdminAccount{ID: "10", Username: "target"},
		nil,
		nil,
		nil,
		&models.Account{ID: 1},
	)
	if len(out.Statuses) != 1 || out.Statuses[0].Favourited == nil || !*out.Statuses[0].Favourited {
		t.Fatalf("nested status did not preserve current-account favourite: %#v", out.Statuses)
	}
	if !out.Statuses[0].FilteredPresent || out.Statuses[0].Filtered == nil {
		t.Fatalf("nested status did not preserve current-account filtered field: %#v", out.Statuses[0])
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	if !strings.Contains(payload, `"favourited":true`) || !strings.Contains(payload, `"filtered":[]`) {
		t.Fatalf("admin report nested status json = %s", payload)
	}
}

func TestAdminAccountFromModelUsesMastodonAdminKeys(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:           42,
		Username:     "alice",
		CreatedAt:    time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		SilencedAt:   sql.NullTime{Time: time.Date(2026, 6, 18, 1, 0, 0, 0, time.UTC), Valid: true},
		SensitizedAt: sql.NullTime{Time: time.Date(2026, 6, 18, 2, 0, 0, 0, time.UTC), Valid: true},
		User: models.User{
			ID:                     7,
			Email:                  "alice@example.test",
			Approved:               true,
			Locale:                 sql.NullString{String: "ja", Valid: true},
			ConfirmedAt:            sql.NullTime{Time: time.Date(2026, 6, 18, 3, 0, 0, 0, time.UTC), Valid: true},
			CreatedByApplicationID: sql.NullInt64{Int64: 9, Valid: true},
		},
	}

	out := AdminAccountFromModel(cfg, account)
	if out.ID != "42" || out.Email == nil || *out.Email != "alice@example.test" {
		t.Fatalf("AdminAccount = %#v", out)
	}
	if out.Confirmed == nil || !*out.Confirmed || out.Approved == nil || !*out.Approved || !out.Silenced || !out.Sensitized {
		t.Fatalf("AdminAccount flags = %#v", out)
	}
	if out.Locale == nil || *out.Locale != "ja" {
		t.Fatalf("Locale = %#v", out.Locale)
	}
	if out.CreatedByApplicationID == nil || *out.CreatedByApplicationID != "9" {
		t.Fatalf("CreatedByApplicationID = %#v", out.CreatedByApplicationID)
	}
	if out.Role != nil || len(out.IPs) != 0 {
		t.Fatalf("Role/IPs = %#v %#v", out.Role, out.IPs)
	}
}

func TestAdminAccountFromModelWithIPsMatchesRailsAdminIPShape(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	usedAt := time.Date(2026, 6, 20, 4, 5, 6, 700000000, time.UTC)
	usedAtString := usedAt.Format(time.RFC3339Nano)
	account := models.Account{
		ID:        42,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		User: models.User{
			ID:       7,
			Email:    "alice@example.test",
			Approved: true,
		},
	}

	out := AdminAccountFromModelWithIPs(cfg, account, []AdminAccountIP{{
		IP:     "192.0.2.10",
		UsedAt: &usedAtString,
	}, {
		IP: "192.0.2.11",
	}})
	if out.IP == nil || *out.IP != "192.0.2.10" {
		t.Fatalf("IP = %#v", out.IP)
	}
	if len(out.IPs) != 2 || out.IPs[0].IP != "192.0.2.10" || out.IPs[0].UsedAt == nil || *out.IPs[0].UsedAt != "2026-06-20T04:05:06.7Z" || out.IPs[1].UsedAt != nil {
		t.Fatalf("IPs = %#v", out.IPs)
	}

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["ip"] != "192.0.2.10" {
		t.Fatalf("json ip = %#v", raw["ip"])
	}
	ips, ok := raw["ips"].([]any)
	if !ok || len(ips) != 2 {
		t.Fatalf("json ips = %#v", raw["ips"])
	}
	first, ok := ips[0].(map[string]any)
	if !ok || first["ip"] != "192.0.2.10" || first["used_at"] != "2026-06-20T04:05:06.7Z" {
		t.Fatalf("json ips[0] = %#v", ips[0])
	}
	second, ok := ips[1].(map[string]any)
	if !ok || second["ip"] != "192.0.2.11" || second["used_at"] != nil {
		t.Fatalf("json ips[1] = %#v", ips[1])
	}
}

func TestAdminAccountFromModelWithRoleMatchesRailsRoleShape(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	account := models.Account{
		ID:        42,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		User: models.User{
			ID:       7,
			Email:    "alice@example.test",
			Approved: true,
		},
	}
	role := models.UserRole{ID: 4, Name: "Moderator", Permissions: 1 << 4, Color: "#ffcc00", Highlighted: true}
	everyone := models.UserRole{ID: -99, Permissions: 1 << 16}

	out := AdminAccountFromModelWithIPsAndRole(cfg, account, nil, &role, &everyone)
	outRole, ok := out.Role.(*Role)
	if !ok || outRole.ID != "4" || outRole.Name != "Moderator" || outRole.Permissions != "65552" || outRole.Color != "#ffcc00" || !outRole.Highlighted {
		t.Fatalf("role = %#v", out.Role)
	}

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	roleJSON, ok := raw["role"].(map[string]any)
	if !ok {
		t.Fatalf("json role = %#v", raw["role"])
	}
	if roleJSON["id"] != "4" || roleJSON["name"] != "Moderator" || roleJSON["permissions"] != "65552" || roleJSON["color"] != "#ffcc00" || roleJSON["highlighted"] != true {
		t.Fatalf("json role = %#v", roleJSON)
	}
}

func TestAdminAccountFromModelWithOptionsIncludesInviteFields(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	inviteRequest := "Please approve me"
	invitedByAccountID := "101"
	account := models.Account{
		ID:        42,
		Username:  "alice",
		CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		User: models.User{
			ID:       7,
			Email:    "alice@example.test",
			Approved: true,
		},
	}

	out := AdminAccountFromModelWithOptions(cfg, account, AdminAccountOptions{
		InviteRequest:      &inviteRequest,
		InvitedByAccountID: &invitedByAccountID,
	})
	if out.InviteRequest == nil || *out.InviteRequest != "Please approve me" {
		t.Fatalf("InviteRequest = %#v", out.InviteRequest)
	}
	if out.InvitedByAccountID == nil || *out.InvitedByAccountID != "101" {
		t.Fatalf("InvitedByAccountID = %#v", out.InvitedByAccountID)
	}

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["invite_request"] != "Please approve me" || raw["invited_by_account_id"] != "101" {
		t.Fatalf("json invite fields = %#v %#v", raw["invite_request"], raw["invited_by_account_id"])
	}
}

func TestAdminPreviewCardProviderFromModelMarksUnreviewed(t *testing.T) {
	out := AdminPreviewCardProviderFromModel(models.PreviewCardProvider{
		ID:     42,
		Domain: "example.com",
	})

	if out.ID != "42" || out.Domain != "example.com" {
		t.Fatalf("AdminPreviewCardProvider = %#v", out)
	}
	if !out.RequiresReview {
		t.Fatal("expected unreviewed provider to require review")
	}
	if out.Trendable != nil || out.ReviewedAt != nil || out.RequestedReviewAt != nil {
		t.Fatalf("unexpected nullable fields = %#v", out)
	}
}

func TestAdminTrendStatusSerializesRequiresReviewWithStatusPayload(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := AdminTrendStatusFromModel(cfg, models.Status{
		ID:        100,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
	}, nil)

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["id"] != "100" || payload["content"] != "<p>hello</p>" {
		t.Fatalf("status payload missing in %s", string(body))
	}
	if value, ok := payload["requires_review"].(bool); !ok || !value {
		t.Fatalf("requires_review payload = %#v in %s", payload["requires_review"], string(body))
	}
}

func TestAdminBlockSerializersUseMastodonKeys(t *testing.T) {
	created := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	domain := AdminDomainBlockFromModel(models.DomainBlock{
		ID:             10,
		Domain:         "remote.example",
		CreatedAt:      created,
		Severity:       models.DomainBlockSeverity(1),
		RejectMedia:    true,
		PrivateComment: sql.NullString{String: "private", Valid: true},
	})
	if domain.ID != "10" || domain.Severity != "suspend" || !domain.RejectMedia {
		t.Fatalf("AdminDomainBlock = %#v", domain)
	}
	if domain.Digest != "bbda63643fa3d9826f2c03a2ecfaa9b9f244646c3691e3ef4ede011b564ad686" {
		t.Fatalf("Digest = %q", domain.Digest)
	}
	if domain.PrivateComment == nil || *domain.PrivateComment != "private" {
		t.Fatalf("PrivateComment = %#v", domain.PrivateComment)
	}

	ip := AdminIPBlockFromModel(models.IPBlock{
		ID:        20,
		IP:        "192.0.2.0/24",
		Severity:  9999,
		Comment:   "blocked",
		CreatedAt: created,
	})
	if ip.ID != "20" || ip.Severity != "no_access" || ip.IP != "192.0.2.0/24" {
		t.Fatalf("AdminIPBlock = %#v", ip)
	}
}

func TestConversationFromModelUsesMastodonKeys(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	conversation := models.AccountConversation{
		ID:     77,
		Unread: true,
		ParticipantAccounts: []models.Account{
			{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
		},
		LastStatus: &models.Status{
			ID:         100,
			Text:       "secret",
			CreatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
			UpdatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
			AccountID:  42,
			Visibility: 3,
			Account: models.Account{
				ID:        42,
				Username:  "alice",
				CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	out := ConversationFromModel(cfg, conversation, nil)
	if out.ID != "77" {
		t.Fatalf("ID = %q", out.ID)
	}
	if !out.Unread {
		t.Fatal("Unread = false")
	}
	if len(out.Accounts) != 1 || out.Accounts[0].ID != "42" {
		t.Fatalf("Accounts = %#v", out.Accounts)
	}
	if out.LastStatus == nil || out.LastStatus.Visibility != "direct" {
		t.Fatalf("LastStatus = %#v", out.LastStatus)
	}
}

func TestConversationFromModelUsesHydratedLastStatusFlags(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	current := models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)}
	conversation := models.AccountConversation{
		ID:     77,
		Unread: true,
		LastStatus: &models.Status{
			ID:                  100,
			Text:                "hello",
			CreatedAt:           time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
			UpdatedAt:           time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
			AccountID:           42,
			Account:             current,
			Visibility:          0,
			FavouritedByCurrent: true,
			RebloggedByCurrent:  true,
			MutedByCurrent:      true,
			BookmarkedByCurrent: true,
			PinnedByCurrent:     true,
		},
	}

	out := ConversationFromModel(cfg, conversation, &current)
	if out.LastStatus == nil {
		t.Fatal("LastStatus = nil")
	}
	if out.LastStatus.Favourited == nil || !*out.LastStatus.Favourited {
		t.Fatalf("Favourited = %#v", out.LastStatus.Favourited)
	}
	if out.LastStatus.Reblogged == nil || !*out.LastStatus.Reblogged {
		t.Fatalf("Reblogged = %#v", out.LastStatus.Reblogged)
	}
	if out.LastStatus.Muted == nil || !*out.LastStatus.Muted {
		t.Fatalf("Muted = %#v", out.LastStatus.Muted)
	}
	if out.LastStatus.Bookmarked == nil || !*out.LastStatus.Bookmarked {
		t.Fatalf("Bookmarked = %#v", out.LastStatus.Bookmarked)
	}
	if out.LastStatus.Pinned == nil || !*out.LastStatus.Pinned {
		t.Fatalf("Pinned = %#v", out.LastStatus.Pinned)
	}
}

func TestStatusFromModelUsesMastodonKeys(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:         100,
		Text:       "hello\nworld",
		CreatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID:  42,
		Visibility: 0,
		Language:   sql.NullString{String: "ja", Valid: true},
		Account: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
		StatusStat: models.StatusStat{RepliesCount: 1, ReblogsCount: 2, FavouritesCount: 3},
	}

	out := StatusFromModel(cfg, status, nil)
	if out.ID != "100" || out.Account.ID != "42" {
		t.Fatalf("unexpected IDs: %#v", out)
	}
	if out.Visibility != "public" {
		t.Fatalf("Visibility = %q", out.Visibility)
	}
	if out.Language == nil || *out.Language != "ja" {
		t.Fatalf("Language = %#v", out.Language)
	}
	if out.Content != "<p>hello<br />world</p>" {
		t.Fatalf("Content = %q", out.Content)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["content"] != "<p>hello<br />world</p>" {
		t.Fatalf("content JSON = %#v", payload["content"])
	}
	if payload["quote_id"] != nil || payload["quote_original_url"] != nil {
		t.Fatalf("quote JSON = %#v/%#v", payload["quote_id"], payload["quote_original_url"])
	}
	if out.URL != "https://example.test/@alice/100" {
		t.Fatalf("URL = %q", out.URL)
	}
}

func TestStatusFromModelClampsNegativeStatusStats(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:         100,
		Text:       "hello",
		CreatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		Visibility: 0,
		Account: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
		StatusStat: models.StatusStat{
			RepliesCount:    -1,
			ReblogsCount:    -2,
			FavouritesCount: -3,
		},
	}

	out := StatusFromModel(cfg, status, nil)
	if out.RepliesCount != 0 || out.ReblogsCount != 0 || out.FavouritesCount != 0 {
		t.Fatalf("counts = replies:%d reblogs:%d favourites:%d", out.RepliesCount, out.ReblogsCount, out.FavouritesCount)
	}
}

func TestStatusFromModelSerializesRemoteURLLikeRailsTagManager(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}

	cases := []struct {
		name    string
		rawURL  sql.NullString
		wantURL any
	}{
		{name: "stored https url", rawURL: sql.NullString{String: "https://remote.example/@bob/100", Valid: true}, wantURL: "https://remote.example/@bob/100"},
		{name: "blank stored url", rawURL: sql.NullString{String: "", Valid: true}, wantURL: nil},
		{name: "unsupported url scheme", rawURL: sql.NullString{String: "ftp://remote.example/status/100", Valid: true}, wantURL: nil},
		{name: "missing url", rawURL: sql.NullString{}, wantURL: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := models.Status{
				ID:        100,
				Text:      "hello",
				CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
				Local:     sql.NullBool{Bool: false, Valid: true},
				URL:       tc.rawURL,
				Account: models.Account{
					ID:        42,
					Username:  "bob",
					Domain:    sql.NullString{String: "remote.example", Valid: true},
					CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
				},
			}

			out := StatusFromModel(cfg, status, nil)
			body, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["url"] != tc.wantURL {
				t.Fatalf("url JSON = %#v, want %#v in %s", payload["url"], tc.wantURL, string(body))
			}
		})
	}
}

func TestStatusFromModelKeepsRemoteMentionDisplayShortWithoutCollisionLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:         100,
		Text:       "hi @alice@remote.example",
		CreatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID:  42,
		Visibility: 0,
		Account: models.Account{
			ID:        42,
			Username:  "author",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
		Mentions: []models.Mention{
			{AccountID: models.MentionAccountID(8), Account: models.Account{ID: 8, Username: "alice", Domain: sql.NullString{String: "remote.example", Valid: true}, URL: sql.NullString{String: "https://remote.example/@alice", Valid: true}, CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)}},
		},
	}

	out := StatusFromModel(cfg, status, nil)
	want := `<a href="https://remote.example/@alice" class="u-url mention">@<span>alice</span></a>`
	if !strings.Contains(out.Content, want) {
		t.Fatalf("remote mention display should stay short without same-username collision: %s", out.Content)
	}
	if strings.Contains(out.Content, `@<span>alice@remote.example</span>`) {
		t.Fatalf("remote mention display included domain without Rails same-username collision: %s", out.Content)
	}
}

func TestStatusFromModelSanitizesRemoteHTMLLikeMastodonStrict(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      `<p class="h-entry bad"><a href="javascript:alert(1)" onclick="x" class="mention bad">bad</a><a href="https://remote.example/@bob/1" rel="tag" class="u-url bad" translate="no">ok</a><script>alert(1)</script><span class="ellipsis nope" translate="no">cut</span><h1>Head</h1><img src="https://remote.example/x.png"><img draggable="false" class="emojione bad" alt=":party:" title=":party:" src="https://remote.example/emoji/party.png" onload="x"><img class="emojione" alt=":bad:" src="javascript:alert(1)"></p>`,
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Local:     sql.NullBool{Bool: false, Valid: true},
		Account: models.Account{
			ID:        42,
			Username:  "bob",
			Domain:    sql.NullString{String: "remote.example", Valid: true},
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
	}

	out := StatusFromModel(cfg, status, nil)
	for _, want := range []string{
		`<p>`,
		`bad`,
		`<a href="https://remote.example/@bob/1" class="u-url" translate="no" rel="nofollow noopener noreferrer" target="_blank">ok</a>`,
		`<span class="ellipsis" translate="no">cut</span>`,
		`<p><strong>Head</strong></p>`,
		`<img draggable="false" class="emojione" alt=":party:" title=":party:" src="https://remote.example/emoji/party.png">`,
	} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("remote content missing %q: %s", want, out.Content)
		}
	}
	for _, unwanted := range []string{"javascript:", "onclick", "<script", `src="https://remote.example/x.png"`, " bad", " nope", "<a rel="} {
		if strings.Contains(out.Content, unwanted) {
			t.Fatalf("remote content kept %q: %s", unwanted, out.Content)
		}
	}
	if strings.Count(out.Content, `rel=`) != 1 || strings.Contains(out.Content, `rel="tag"`) {
		t.Fatalf("remote anchor rel was not normalized like Rails MASTODON_STRICT: %s", out.Content)
	}
}

func TestStatusFromModelRestoresLegacyEmbeddedCustomEmojiForMastodonClients(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", PaperclipRootURL: "/system"}
	status := models.Status{
		ID:        100,
		Text:      `<p>before <img draggable="false" class="emojione" alt=":party:" title=":party:" src="https://media.example/party.gif" /> after</p>`,
		CreatedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		AccountID: 42,
		Local:     sql.NullBool{Bool: false, Valid: true},
		Account: models.Account{
			ID:        42,
			Username:  "bob",
			Domain:    sql.NullString{String: "remote.example", Valid: true},
			CreatedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		},
		CustomEmojis: []models.CustomEmoji{{
			ID:               7,
			Shortcode:        "party",
			Domain:           sql.NullString{String: "remote.example", Valid: true},
			ImageFileName:    sql.NullString{String: "party.gif", Valid: true},
			ImageContentType: sql.NullString{String: "image/gif", Valid: true},
		}},
	}

	out := StatusFromModel(cfg, status, nil)
	if out.Content != `<p>before :party: after</p>` {
		t.Fatalf("content = %q", out.Content)
	}
	if len(out.Emojis) != 1 || out.Emojis[0].Shortcode != "party" || out.Emojis[0].URL == "" || out.Emojis[0].StaticURL == "" {
		t.Fatalf("emojis = %#v", out.Emojis)
	}
}

func TestStatusFromModelDoesNotRestoreUnrelatedEmbeddedImage(t *testing.T) {
	status := models.Status{
		ID:        100,
		Text:      `<p><img draggable="false" class="emojione" alt=":other:" src="https://remote.example/other.png"></p>`,
		CreatedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		AccountID: 42,
		Local:     sql.NullBool{Bool: false, Valid: true},
		Account: models.Account{
			ID:        42,
			Username:  "bob",
			Domain:    sql.NullString{String: "remote.example", Valid: true},
			CreatedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		},
		CustomEmojis: []models.CustomEmoji{{Shortcode: "party"}},
	}

	out := StatusFromModel(config.Config{}, status, nil)
	if !strings.Contains(out.Content, `alt=":other:"`) || strings.Contains(out.Content, ":party:") {
		t.Fatalf("unrelated image was rewritten: %s", out.Content)
	}
}

func TestStatusFromModelBlankContentMatchesRailsFormatter(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      " \n ",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
	}

	if out := StatusFromModel(cfg, status, nil); out.Content != "" {
		t.Fatalf("blank local content = %q, want empty", out.Content)
	}
	status.Local = sql.NullBool{Bool: false, Valid: true}
	status.Account.Domain = sql.NullString{String: "remote.example", Valid: true}
	if out := StatusFromModel(cfg, status, nil); out.Content != "" {
		t.Fatalf("blank remote content = %q, want empty", out.Content)
	}
}

func TestStatusFromModelUsesFullURLForKomifloLikeRailsFork(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:         100,
		Text:       "https://komiflo.com/comics/long/path",
		CreatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID:  42,
		Visibility: 0,
		Account: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
	}

	out := StatusFromModel(cfg, status, nil)
	want := `<a href="https://komiflo.com/comics/long/path" target="_blank" rel="nofollow noopener noreferrer" translate="no">https://komiflo.com/comics/long/path</a>`
	if !strings.Contains(out.Content, want) {
		t.Fatalf("content missing full komiflo link: %s", out.Content)
	}
}

func TestStatusFromModelIncludesHydratedQuoteFields(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      "hello\n\nRE: https://example.test/@bob/99",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
		QuoteID:   sql.NullString{String: "99", Valid: true},
		QuoteOriginalURL: sql.NullString{
			String: "https://example.test/users/bob/statuses/99",
			Valid:  true,
		},
	}

	out := StatusFromModel(cfg, status, nil)
	if out.QuoteID != "99" || out.QuoteOriginalURL != "https://example.test/users/bob/statuses/99" {
		t.Fatalf("quote fields = %#v/%#v", out.QuoteID, out.QuoteOriginalURL)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["quote_id"] != "99" || payload["quote_original_url"] != "https://example.test/users/bob/statuses/99" {
		t.Fatalf("quote JSON = %s", string(body))
	}
}

func TestStatusFromModelUsesOrderedMediaAttachmentIDs(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
		MediaAttachments: []models.MediaAttachment{
			{ID: 3, RemoteURL: "https://cdn.example.test/3.png"},
			{ID: 4, RemoteURL: "https://cdn.example.test/4.png"},
		},
		OrderedMediaAttachmentIDs: models.Int64Array{4},
	}

	out := StatusFromModel(cfg, status, nil)
	if len(out.MediaAttachments) != 1 || out.MediaAttachments[0].ID != "4" {
		t.Fatalf("MediaAttachments = %#v", out.MediaAttachments)
	}
}

func TestMediaAttachmentFromModelUsesPaperclipPartitionedThumbnailURL(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:                       123,
		Type:                     0,
		FileFileName:             sql.NullString{String: "photo.png", Valid: true},
		ThumbnailFileName:        sql.NullString{String: "thumb.png", Valid: true},
		ThumbnailRemoteURL:       sql.NullString{String: "https://remote.example/thumb.png", Valid: true},
		ThumbnailContentType:     sql.NullString{String: "image/png", Valid: true},
		ThumbnailFileSize:        sql.NullInt64{Int64: 1234, Valid: true},
		ThumbnailUpdatedAt:       sql.NullTime{Time: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC), Valid: true},
		FileContentType:          sql.NullString{String: "image/png", Valid: true},
		FileStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	})

	if out.URL != "https://example.test/system/media_attachments/files/000/000/123/original/photo.png" {
		t.Fatalf("URL = %q", out.URL)
	}
	if out.PreviewURL != "https://example.test/system/media_attachments/thumbnails/000/000/123/original/thumb.png" {
		t.Fatalf("PreviewURL = %q", out.PreviewURL)
	}
	if out.PreviewRemoteURL != "https://remote.example/thumb.png" {
		t.Fatalf("PreviewRemoteURL = %q", out.PreviewRemoteURL)
	}
}

func TestMediaAttachmentFromModelSuppressesURLWhileProcessingLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:           124,
		Type:         2,
		FileFileName: sql.NullString{String: "video.mp4", Valid: true},
		Processing:   sql.NullInt64{Int64: 0, Valid: true},
	})

	if out.URL != "" {
		t.Fatalf("URL = %q, want empty while media is not processed", out.URL)
	}
	if out.PreviewURL != "" {
		t.Fatalf("PreviewURL = %q, want empty without processed preview", out.PreviewURL)
	}
}

func TestMediaAttachmentFromModelSerializesAbsentOptionalURLsAsNull(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:           124,
		Type:         2,
		FileFileName: sql.NullString{String: "video.mp4", Valid: true},
		Processing:   sql.NullInt64{Int64: 0, Valid: true},
	})

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"url", "preview_url", "remote_url", "preview_remote_url", "text_url", "description", "blurhash"} {
		value, ok := payload[key]
		if !ok {
			t.Fatalf("%s key omitted from media attachment JSON: %s", key, string(body))
		}
		if value != nil {
			t.Fatalf("%s = %#v, want null in %s", key, value, string(body))
		}
	}
}

func TestMediaAttachmentFromModelUsesRemoteURLWhenRemoteCacheDisabled(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", DisableRemoteMediaCache: true}
	out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:                 125,
		Type:               0,
		RemoteURL:          "https://remote.example/photo.png",
		FileFileName:       sql.NullString{String: "cached.png", Valid: true},
		ThumbnailRemoteURL: sql.NullString{String: "https://remote.example/thumb.png", Valid: true},
		ThumbnailFileName:  sql.NullString{String: "cached-thumb.png", Valid: true},
	})

	if out.URL != "https://remote.example/photo.png" {
		t.Fatalf("URL = %q", out.URL)
	}
	if out.PreviewURL != "https://remote.example/photo.png" {
		t.Fatalf("PreviewURL = %q", out.PreviewURL)
	}
	if out.RemoteURL != "https://remote.example/photo.png" || out.PreviewRemoteURL != "https://remote.example/thumb.png" {
		t.Fatalf("remote URLs = %#v", out)
	}
}

func TestMediaAttachmentFromModelUsesStorageHostForLocalPaperclipAssets(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", StorageHost: "https://media.example.test/"}
	out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:                125,
		Type:              0,
		FileFileName:      sql.NullString{String: "photo.png", Valid: true},
		ThumbnailFileName: sql.NullString{String: "thumb.png", Valid: true},
	})

	if out.URL != "https://media.example.test/media_attachments/files/000/000/125/original/photo.png" {
		t.Fatalf("URL = %q", out.URL)
	}
	if out.PreviewURL != "https://media.example.test/media_attachments/thumbnails/000/000/125/original/thumb.png" {
		t.Fatalf("PreviewURL = %q", out.PreviewURL)
	}
}

func TestMediaAttachmentFromModelUsesLocalSmallStylePreviewLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}

	for _, tc := range []struct {
		name string
		kind int
		file string
		want string
	}{
		{name: "image", kind: 0, file: "photo.png", want: "https://example.test/system/media_attachments/files/000/000/125/small/photo.png"},
		{name: "gifv", kind: 1, file: "clip.mp4", want: "https://example.test/system/media_attachments/files/000/000/125/small/clip.png"},
		{name: "video", kind: 2, file: "video.mp4", want: "https://example.test/system/media_attachments/files/000/000/125/small/video.png"},
		{name: "audio", kind: 4, file: "audio.mp3", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
				ID:           125,
				Type:         tc.kind,
				FileFileName: sql.NullString{String: tc.file, Valid: true},
			})
			if out.PreviewURL != tc.want {
				t.Fatalf("PreviewURL = %q, want %q", out.PreviewURL, tc.want)
			}
		})
	}
}

func TestMediaAttachmentFromModelKeepsRemoteThumbnailOutOfPreviewURLLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:                 125,
		Type:               4,
		FileFileName:       sql.NullString{String: "audio.mp3", Valid: true},
		ThumbnailRemoteURL: sql.NullString{String: "https://remote.example/audio-cover.jpg", Valid: true},
	})

	if out.PreviewURL != "" {
		t.Fatalf("PreviewURL = %q, want empty because Rails exposes thumbnail_remote_url only as preview_remote_url", out.PreviewURL)
	}
	if out.PreviewRemoteURL != "https://remote.example/audio-cover.jpg" {
		t.Fatalf("PreviewRemoteURL = %q", out.PreviewRemoteURL)
	}
}

func TestMediaAttachmentFromModelUsesProxyForUncachedRemoteMedia(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:                 126,
		Type:               2,
		RemoteURL:          "https://remote.example/video.mp4",
		ThumbnailRemoteURL: sql.NullString{String: "https://remote.example/thumb.jpg", Valid: true},
	})

	if out.URL != "https://example.test/media_proxy/126/original" {
		t.Fatalf("URL = %q", out.URL)
	}
	if out.PreviewURL != "https://example.test/media_proxy/126/small" {
		t.Fatalf("PreviewURL = %q", out.PreviewURL)
	}
	if out.PreviewRemoteURL != "https://remote.example/thumb.jpg" {
		t.Fatalf("PreviewRemoteURL = %q", out.PreviewRemoteURL)
	}
}

func TestMediaAttachmentFromModelIncludesTextURLForLocalShortcode(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := MediaAttachmentFromModel(cfg, models.MediaAttachment{
		ID:        127,
		Type:      0,
		Shortcode: sql.NullString{String: "109915428643912138", Valid: true},
	})

	if out.TextURL != "https://example.test/media/109915428643912138" {
		t.Fatalf("TextURL = %q", out.TextURL)
	}
}

func TestStatusFromModelIncludesPreviewCard(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
		PreviewCards: []models.PreviewCard{
			{ID: 9, URL: "https://remote.example", Title: "Remote", Type: 3},
		},
	}

	out := StatusFromModel(cfg, status, nil)
	card, ok := out.Card.(PreviewCard)
	if !ok {
		t.Fatalf("Card = %#v", out.Card)
	}
	if card.URL != "https://remote.example" || card.Type != "rich" || card.Title != "Remote" {
		t.Fatalf("Card = %#v", card)
	}
}

func TestStatusFromModelWithSourceIncludesRawText(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      "raw **text**",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
	}

	out := StatusFromModelWithSource(cfg, status, nil)
	if out.Text == nil || *out.Text != "raw **text**" {
		t.Fatalf("Text = %#v", out.Text)
	}
	if out.Content == "" {
		t.Fatal("expected formatted content to remain available internally")
	}

	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["content"]; ok {
		t.Fatalf("content should be omitted for source payload: %s", string(body))
	}
	if payload["text"] != "raw **text**" {
		t.Fatalf("text = %#v", payload["text"])
	}
}

func TestStatusFromModelUsesHydratedRelationshipFlags(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	current := models.Account{ID: 42, Username: "alice"}
	status := models.Status{
		ID:                  100,
		Text:                "hello",
		CreatedAt:           time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID:           42,
		Account:             current,
		Visibility:          0,
		FavouritedByCurrent: true,
		RebloggedByCurrent:  true,
		MutedByCurrent:      true,
		BookmarkedByCurrent: true,
		PinnedByCurrent:     true,
	}

	out := StatusFromModel(cfg, status, &current)
	if out.Favourited == nil || !*out.Favourited {
		t.Fatalf("Favourited = %#v", out.Favourited)
	}
	if out.Reblogged == nil || !*out.Reblogged {
		t.Fatalf("Reblogged = %#v", out.Reblogged)
	}
	if out.Muted == nil || !*out.Muted {
		t.Fatalf("Muted = %#v", out.Muted)
	}
	if out.Bookmarked == nil || !*out.Bookmarked {
		t.Fatalf("Bookmarked = %#v", out.Bookmarked)
	}
	if out.Pinned == nil || !*out.Pinned {
		t.Fatalf("Pinned = %#v", out.Pinned)
	}
}

func TestStatusFromModelSerializesEmptyFilteredForAuthenticatedViewer(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	current := models.Account{ID: 42, Username: "alice"}
	status := models.Status{
		ID:         100,
		Text:       "hello",
		CreatedAt:  time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID:  42,
		Account:    current,
		Visibility: 0,
	}

	body, err := json.Marshal(StatusFromModel(cfg, status, &current))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	filtered, ok := payload["filtered"].([]any)
	if !ok || len(filtered) != 0 {
		t.Fatalf("filtered payload = %#v in %s", payload["filtered"], string(body))
	}
}

func TestStatusFromModelOmitsFilteredForAnonymousViewer(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
	}

	body, err := json.Marshal(StatusFromModel(cfg, status, nil))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["filtered"]; ok {
		t.Fatalf("anonymous status serialized filtered: %s", string(body))
	}
}

func TestStatusFromModelOmitsPinnedWhenRailsWouldNotExposeIt(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	current := models.Account{ID: 42, Username: "alice"}

	tests := []struct {
		name   string
		status models.Status
	}{
		{
			name: "direct",
			status: models.Status{
				ID:              100,
				Text:            "hello",
				CreatedAt:       time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
				AccountID:       42,
				Account:         current,
				Visibility:      3,
				PinnedByCurrent: true,
			},
		},
		{
			name: "limited",
			status: models.Status{
				ID:              101,
				Text:            "hello",
				CreatedAt:       time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
				AccountID:       42,
				Account:         current,
				Visibility:      4,
				PinnedByCurrent: true,
			},
		},
		{
			name: "reblog",
			status: models.Status{
				ID:              102,
				Text:            "hello",
				CreatedAt:       time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
				AccountID:       42,
				Account:         current,
				Visibility:      0,
				ReblogOfID:      sql.NullInt64{Int64: 10, Valid: true},
				PinnedByCurrent: true,
			},
		},
		{
			name: "other-account",
			status: models.Status{
				ID:              103,
				Text:            "hello",
				CreatedAt:       time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
				AccountID:       7,
				Account:         models.Account{ID: 7, Username: "bob"},
				Visibility:      0,
				PinnedByCurrent: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := StatusFromModel(cfg, tt.status, &current)
			if out.Pinned != nil {
				t.Fatalf("Pinned = %#v", out.Pinned)
			}
		})
	}
}

func TestStatusFromModelIncludesApplicationWhenRailsWouldShowIt(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:            100,
		Text:          "hello",
		CreatedAt:     time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID:     42,
		Account:       models.Account{ID: 42, Username: "alice", User: models.User{ID: 7}},
		ApplicationID: sql.NullInt64{Int64: 9, Valid: true},
		Application:   &models.OAuthApplication{ID: 9, Name: "Paon Mobile", Website: "https://app.example.test"},
	}

	out := StatusFromModel(cfg, status, nil)
	if out.Application == nil || out.Application.Name != "Paon Mobile" {
		t.Fatalf("Application = %#v", out.Application)
	}
	if out.Application.Website == nil || *out.Application.Website != "https://app.example.test" {
		t.Fatalf("Application website = %#v", out.Application.Website)
	}
}

func TestStatusFromModelSerializesApplicationWebsiteNullLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:            100,
		Text:          "hello",
		CreatedAt:     time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID:     42,
		Account:       models.Account{ID: 42, Username: "alice", User: models.User{ID: 7}},
		ApplicationID: sql.NullInt64{Int64: 9, Valid: true},
		Application:   &models.OAuthApplication{ID: 9, Name: "Paon Mobile"},
	}

	body, err := json.Marshal(StatusFromModel(cfg, status, nil))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	application, ok := payload["application"].(map[string]any)
	if !ok {
		t.Fatalf("application payload = %#v in %s", payload["application"], string(body))
	}
	if _, ok := application["website"]; !ok {
		t.Fatalf("application website key omitted: %s", string(body))
	}
	if application["website"] != nil {
		t.Fatalf("application website = %#v, want null in %s", application["website"], string(body))
	}
}

func TestStatusFromModelSerializesMissingApplicationNullWhenRailsWouldShowIt(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", User: models.User{ID: 7}},
	}

	body, err := json.Marshal(StatusFromModel(cfg, status, nil))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["application"]; !ok {
		t.Fatalf("application key omitted: %s", string(body))
	}
	if payload["application"] != nil {
		t.Fatalf("application = %#v, want null in %s", payload["application"], string(body))
	}
}

func TestStatusFromModelHidesApplicationWhenAccountSettingDisablesIt(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account: models.Account{ID: 42, Username: "alice", User: models.User{
			ID:       7,
			Settings: sql.NullString{String: `{"show_application":false}`, Valid: true},
		}},
		ApplicationID: sql.NullInt64{Int64: 9, Valid: true},
		Application:   &models.OAuthApplication{ID: 9, Name: "Paon Mobile"},
	}

	other := models.Account{ID: 99, Username: "viewer"}
	if out := StatusFromModel(cfg, status, &other); out.Application != nil {
		t.Fatalf("Application should be hidden for other viewers: %#v", out.Application)
	}
	body, err := json.Marshal(StatusFromModel(cfg, status, &other))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["application"]; ok {
		t.Fatalf("application should be omitted when Rails show_application? is false: %s", string(body))
	}
	owner := models.Account{ID: 42, Username: "alice"}
	if out := StatusFromModel(cfg, status, &owner); out.Application == nil || out.Application.Name != "Paon Mobile" {
		t.Fatalf("Application should be shown to owner: %#v", out.Application)
	}
}

func TestStatusFromModelSensitiveMatchesRailsOwnerRule(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	sensitizedAt := time.Date(2026, 6, 18, 2, 0, 0, 0, time.UTC)
	status := models.Status{
		ID:        100,
		Text:      "hello",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account: models.Account{
			ID:           42,
			Username:     "alice",
			SensitizedAt: sql.NullTime{Time: sensitizedAt, Valid: true},
		},
		Sensitive: false,
	}

	owner := models.Account{ID: 42, Username: "alice"}
	if out := StatusFromModel(cfg, status, &owner); out.Sensitive {
		t.Fatalf("owner Sensitive = %v, want status sensitive only", out.Sensitive)
	}
	other := models.Account{ID: 99, Username: "viewer"}
	if out := StatusFromModel(cfg, status, &other); !out.Sensitive {
		t.Fatalf("other Sensitive = %v, want account sensitized applied", out.Sensitive)
	}
	if out := StatusFromModel(cfg, status, nil); !out.Sensitive {
		t.Fatalf("anonymous Sensitive = %v, want account sensitized applied", out.Sensitive)
	}
}

func TestStatusFromModelIncludesHydratedCustomEmojis(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	status := models.Status{
		ID:        100,
		Text:      "hello :party:",
		CreatedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		AccountID: 42,
		Account:   models.Account{ID: 42, Username: "alice", CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)},
		CustomEmojis: []models.CustomEmoji{{
			ID:            7,
			Shortcode:     "party",
			ImageFileName: sql.NullString{String: "party.gif", Valid: true},
		}},
	}

	out := StatusFromModel(cfg, status, nil)
	if len(out.Emojis) != 1 || out.Emojis[0].Shortcode != "party" || out.Emojis[0].URL == "" {
		t.Fatalf("Emojis = %#v", out.Emojis)
	}
}

func TestStatusSourceFromModel(t *testing.T) {
	out := StatusSourceFromModel(models.Status{ID: 100, Text: "raw **text**", SpoilerText: "cw"})
	if out.ID != "100" {
		t.Fatalf("ID = %q", out.ID)
	}
	if out.Text != "raw **text**" {
		t.Fatalf("Text = %q", out.Text)
	}
	if out.SpoilerText != "cw" {
		t.Fatalf("SpoilerText = %q", out.SpoilerText)
	}
}

func TestStatusEditFromModelUsesMastodonKeys(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	sensitive := sql.NullBool{Bool: true, Valid: true}
	edit := models.StatusEdit{
		Text:        "old\ntext",
		SpoilerText: "cw",
		Sensitive:   sensitive,
		CreatedAt:   time.Date(2026, 6, 18, 12, 30, 0, 0, time.UTC),
		AccountID:   sql.NullInt64{Int64: 42, Valid: true},
		Account: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
		OrderedMediaAttachments: []models.MediaAttachment{
			{ID: 9, RemoteURL: "https://cdn.example.test/9.png", Description: sql.NullString{String: "before", Valid: true}},
		},
		PollOptions: models.StringArray{"yes", "no"},
		CustomEmojis: []models.CustomEmoji{{
			ID:            7,
			Shortcode:     "party",
			ImageFileName: sql.NullString{String: "party.gif", Valid: true},
		}},
	}

	out := StatusEditFromModel(cfg, edit)
	if out.Content != "<p>old<br />text</p>" {
		t.Fatalf("Content = %q", out.Content)
	}
	if out.SpoilerText != "cw" {
		t.Fatalf("SpoilerText = %q", out.SpoilerText)
	}
	if out.Sensitive == nil || !*out.Sensitive {
		t.Fatalf("Sensitive = %#v", out.Sensitive)
	}
	if out.CreatedAt != "2026-06-18T12:30:00.000Z" {
		t.Fatalf("CreatedAt = %q", out.CreatedAt)
	}
	if out.Account == nil || out.Account.ID != "42" {
		t.Fatalf("Account = %#v", out.Account)
	}
	if len(out.MediaAttachments) != 1 || out.MediaAttachments[0].Description != "before" {
		t.Fatalf("MediaAttachments = %#v", out.MediaAttachments)
	}
	if out.Poll == nil || len(out.Poll.Options) != 2 || out.Poll.Options[1].Title != "no" {
		t.Fatalf("Poll = %#v", out.Poll)
	}
	if len(out.Emojis) != 1 || out.Emojis[0].Shortcode != "party" || out.Emojis[0].URL == "" {
		t.Fatalf("Emojis = %#v", out.Emojis)
	}
}

func TestStatusEditFromModelAllowsNullAccountLikeRailsOptionalAssociation(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	edit := models.StatusEdit{
		Text:      "old text",
		CreatedAt: time.Date(2026, 6, 18, 12, 30, 0, 0, time.UTC),
		AccountID: sql.NullInt64{},
		Status: models.Status{
			Local: sql.NullBool{Bool: true, Valid: true},
			Account: models.Account{
				ID:        99,
				Username:  "owner",
				CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	out := StatusEditFromModel(cfg, edit)
	if out.Account != nil {
		t.Fatalf("Account = %#v, want nil for Rails status_edits.account_id NULL", out.Account)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"account":null`) {
		t.Fatalf("serialized status edit = %s, want account null", body)
	}
}

func TestStatusEditFromModelUsesRailsStatusFormatter(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	edit := models.StatusEdit{
		Text:      "old @alice #GoLang https://www.example.com/some/really/long/path?with=query.",
		CreatedAt: time.Date(2026, 6, 18, 12, 30, 0, 0, time.UTC),
		AccountID: sql.NullInt64{Int64: 42, Valid: true},
		Account: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
		Status: models.Status{
			Local: sql.NullBool{Bool: true, Valid: true},
		},
	}

	out := StatusEditFromModel(cfg, edit)
	for _, want := range []string{
		`<span class="h-card" translate="no"><a href="https://example.test/@alice" class="u-url mention">@<span>alice</span></a></span>`,
		`<a href="https://example.test/tags/golang" class="mention hashtag" rel="tag">#<span>GoLang</span></a>`,
		`target="_blank" rel="nofollow noopener noreferrer" translate="no"`,
		`<span class="invisible">https://www.</span><span class="ellipsis">example.com/some/really/long/p</span><span class="invisible">ath?with=query</span>`,
	} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("content missing %q: %s", want, out.Content)
		}
	}
	if strings.Contains(out.Content, `path?with=query.</span>`) {
		t.Fatalf("trailing punctuation was included in URL link: %s", out.Content)
	}
}

func TestStatusEditFromModelSanitizesRemoteHTMLLikeStatusContent(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	edit := models.StatusEdit{
		Text:      `<p><a href="javascript:alert(1)" onclick="x">bad</a><a href="https://remote.example/@bob/1" class="u-url bad" translate="no">ok</a><script>alert(1)</script><h2>Head</h2><img draggable="false" class="emojione bad" alt=":party:" title=":party:" src="/system/custom_emojis/images/000/000/007/original/party.gif"></p>`,
		CreatedAt: time.Date(2026, 6, 18, 12, 30, 0, 0, time.UTC),
		AccountID: sql.NullInt64{Int64: 42, Valid: true},
		Account: models.Account{
			ID:        42,
			Username:  "bob",
			Domain:    sql.NullString{String: "remote.example", Valid: true},
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
		Status: models.Status{
			Local: sql.NullBool{Bool: false, Valid: true},
		},
	}

	out := StatusEditFromModel(cfg, edit)
	for _, want := range []string{
		`bad`,
		`<a href="https://remote.example/@bob/1" class="u-url" translate="no" rel="nofollow noopener noreferrer" target="_blank">ok</a>`,
		`<p><strong>Head</strong></p>`,
		`<img draggable="false" class="emojione" alt=":party:" title=":party:" src="/system/custom_emojis/images/000/000/007/original/party.gif">`,
	} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("remote content missing %q: %s", want, out.Content)
		}
	}
	for _, unwanted := range []string{"javascript:", "onclick", "<script", " bad"} {
		if strings.Contains(out.Content, unwanted) {
			t.Fatalf("remote content kept %q: %s", unwanted, out.Content)
		}
	}
}

func TestPollFromModelIncludesOwnVotes(t *testing.T) {
	current := &models.Account{ID: 42}
	voters := int64(3)
	poll := &models.Poll{
		ID:            10,
		AccountID:     models.PollAccountID(99),
		Options:       models.StringArray{"yes :party:", "no"},
		CachedTallies: models.Int64Array{2, 1},
		VotesCount:    3,
		VotersCount:   sql.NullInt64{Int64: voters, Valid: true},
		Votes:         []models.PollVote{{AccountID: models.PollVoteAccountID(42), Choice: 1}},
		CustomEmojis: []models.CustomEmoji{{
			ID:            7,
			Shortcode:     "party",
			ImageFileName: sql.NullString{String: "party.gif", Valid: true},
		}},
	}

	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := PollFromModel(cfg, poll, current)
	if out == nil || out.ID != "10" {
		t.Fatalf("PollFromModel = %#v", out)
	}
	if out.Voted == nil || !*out.Voted {
		t.Fatalf("Voted = %#v", out.Voted)
	}
	if out.OwnVotes == nil || len(*out.OwnVotes) != 1 || (*out.OwnVotes)[0] != 1 {
		t.Fatalf("OwnVotes = %#v", out.OwnVotes)
	}
	if out.Options[0].VotesCount == nil || *out.Options[0].VotesCount != 2 {
		t.Fatalf("Options = %#v", out.Options)
	}
	if len(out.Emojis) != 1 || out.Emojis[0].Shortcode != "party" || out.Emojis[0].URL == "" {
		t.Fatalf("Emojis = %#v", out.Emojis)
	}
}

func TestPollFromModelIgnoresLegacyNullAccountVotes(t *testing.T) {
	current := &models.Account{ID: 42}
	poll := &models.Poll{
		ID:            10,
		AccountID:     models.PollAccountID(99),
		Options:       models.StringArray{"yes", "no"},
		CachedTallies: models.Int64Array{2, 1},
		VotesCount:    3,
		VotersCount:   sql.NullInt64{Int64: 3, Valid: true},
		Votes: []models.PollVote{
			{AccountID: sql.NullInt64{}, Choice: 0},
			{AccountID: models.PollVoteAccountID(7), Choice: 1},
		},
	}

	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	out := PollFromModel(cfg, poll, current)
	if out == nil || out.Voted == nil || *out.Voted {
		t.Fatalf("voted = %#v, want false for NULL/non-current poll votes", out)
	}
	if out.OwnVotes == nil || len(*out.OwnVotes) != 0 {
		t.Fatalf("own_votes = %#v, want empty for NULL/non-current poll votes", out.OwnVotes)
	}
}

func TestPollFromModelHidesOptionTalliesUntilExpiredLikeRails(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	voters := int64(3)
	active := &models.Poll{
		ID:            10,
		Options:       models.StringArray{"yes", "no"},
		CachedTallies: models.Int64Array{2, 1},
		HideTotals:    true,
		VotesCount:    3,
		VotersCount:   sql.NullInt64{Int64: voters, Valid: true},
		ExpiresAt:     sql.NullTime{Time: time.Now().UTC().Add(time.Hour), Valid: true},
	}

	out := PollFromModel(cfg, active, nil)
	if out == nil || out.VotesCount != 3 || out.VotersCount == nil || *out.VotersCount != 3 {
		t.Fatalf("top-level totals = %#v", out)
	}
	for _, option := range out.Options {
		if option.VotesCount != nil {
			t.Fatalf("active hidden option votes leaked: %#v", out.Options)
		}
	}

	expired := *active
	expired.ExpiresAt = sql.NullTime{Time: time.Now().UTC().Add(-time.Hour), Valid: true}
	out = PollFromModel(cfg, &expired, nil)
	if out.Options[0].VotesCount == nil || *out.Options[0].VotesCount != 2 {
		t.Fatalf("expired option votes = %#v", out.Options)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
