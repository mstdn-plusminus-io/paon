package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestParseSettingsProfilePayloadAcceptsRailsNestedFields(t *testing.T) {
	body := "account%5Bdisplay_name%5D=Alice&account%5Bnote%5D=Hello&account%5Bbot%5D=0&account%5Bbot%5D=1&account%5Bfields_attributes%5D%5B0%5D%5Bname%5D=Site&account%5Bfields_attributes%5D%5B0%5D%5Bvalue%5D=https%3A%2F%2Fexample.com"
	req := httptest.NewRequest(http.MethodPut, "/settings/profile", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, err := parseSettingsProfilePayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.DisplayName == nil || *payload.DisplayName != "Alice" {
		t.Fatalf("display_name = %#v", payload.DisplayName)
	}
	if payload.Bot == nil || !*payload.Bot {
		t.Fatalf("bot = %#v", payload.Bot)
	}
	if len(payload.FieldsAttributes) != 1 || payload.FieldsAttributes[0].Name != "Site" || payload.FieldsAttributes[0].Value != "https://example.com" {
		t.Fatalf("fields = %#v", payload.FieldsAttributes)
	}
}

func TestSettingsProfileHTMLRendersRailsFormFieldsAndPreviewContract(t *testing.T) {
	html := settingsProfileHTMLWithConfig(config.Config{WebDomain: "https://example.test"}, models.Account{
		ID:             42,
		Username:       "alice",
		DisplayName:    "Alice",
		Note:           "Hello",
		ActorType:      sql.NullString{String: "Service", Valid: true},
		AvatarFileName: sql.NullString{String: "avatar.png", Valid: true},
		HeaderFileName: sql.NullString{String: "header.png", Valid: true},
		Fields:         []byte(`[{"name":"Website","value":"https://example.test"}]`),
	}, "/packs/js/public-hash.js")
	for _, want := range []string{
		`class="simple_form edit_account"`,
		`id="edit_profile"`,
		`class="content__heading__tabs"`,
		`id="profile" class="selected simple-navigation-active-leaf"`,
		`class="fields-row"`,
		`with_block_label`,
		`name="account[display_name]"`,
		`name="account[note]"`,
		`name="account[fields_attributes][0][name]"`,
		`name="account[fields_attributes][0][value]"`,
		`name="account[avatar]"`,
		`id="account_avatar-preview"`,
		`data-original-src=`,
		`name="account[header]"`,
		`id="account_header-preview"`,
		`name="account[bot]" value="1" checked`,
		`href="/settings/profile/pictures/avatar"`,
		`href="/settings/profile/pictures/header"`,
		`At most 2 MB. Will be downscaled to 400x400px`,
		`At most 2 MB. Will be downscaled to 1500x500px`,
		`src="/packs/js/public-hash.js"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("profile settings html missing %q: %s", want, html)
		}
	}
}

func TestSettingsProfileUpdateRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPut, "/settings/profile", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.updateSettingsProfile(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/profile")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsProfileMutationsPublishActorUpdateLikeRails(t *testing.T) {
	src, err := os.ReadFile("settings_profile.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"updateSettingsProfile", "destroySettingsProfilePicture"} {
		for _, want := range []string{
			`reloaded, err := s.findAccountByID(strconv.FormatInt(account.ID, 10))`,
			`s.triggerAccountWebhook("account.updated", reloaded.ID)`,
			`_ = s.enqueueActivityPubAccountUpdate(*reloaded, activityPubAccountUpdateDebounceDelay)`,
		} {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s must publish latest profile data after changes; missing %q", fn, want)
			}
		}
		for _, stale := range []string{
			`s.triggerAccountWebhook("account.updated", account.ID)`,
			`_ = s.deliverActivityPubAccountUpdate(*account)`,
			`_ = s.enqueueActivityPubAccountUpdate(*account, activityPubAccountUpdateDebounceDelay)`,
		} {
			if functionBodyContains(t, src, fn, stale) {
				t.Fatalf("%s must not publish stale pre-update account data: %q", fn, stale)
			}
		}
	}
	if !functionBodyContains(t, src, "updateSettingsProfile", `s.updateAccountRowsAndTags(*account, updates, payload.Note, now)`) {
		t.Fatal("updateSettingsProfile must synchronize account hashtags when profile note changes")
	}
}

func TestSettingsProfileMutationsUseRailsGenericSavedFlash(t *testing.T) {
	src, err := os.ReadFile("settings_profile.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"updateSettingsProfile", "destroySettingsProfilePicture"} {
		if !functionBodyContains(t, src, fn, `settingsChangeSavedMessage(locale)`) {
			t.Fatalf("%s must use Rails generic.changes_saved_msg flash", fn)
		}
		for _, stale := range []string{
			`QueryEscape("Profile saved")`,
			`QueryEscape("Privacy settings saved")`,
		} {
			if functionBodyContains(t, src, fn, stale) {
				t.Fatalf("%s must not use fixed Go-only success flash: %q", fn, stale)
			}
		}
	}
}

func TestSettingsProfileInvalidFlashUsesLocaleKey(t *testing.T) {
	src, err := os.ReadFile("settings_profile.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "updateSettingsProfile", `settingsProfileInvalidMessage(locale)`) {
		t.Fatal("updateSettingsProfile must use localized invalid flash")
	}
	if functionBodyContains(t, src, "updateSettingsProfile", `QueryEscape("Profile update is invalid")`) {
		t.Fatal("updateSettingsProfile must not use fixed Go-only invalid flash")
	}
	if got := settingsProfileInvalidMessage("ja"); got == "Profile update is invalid" || !strings.Contains(got, "プロフィール") {
		t.Fatalf("Japanese profile invalid flash did not resolve locale key: %q", got)
	}
}

func TestDestroySettingsProfilePictureClearsPaperclipColumnsLikeRails(t *testing.T) {
	src, err := os.ReadFile("settings_profile.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.removeAccountImageObjects(models.Account{ID: account.ID, AvatarFileName: account.AvatarFileName})`,
		`s.removeAccountLocalImageFilesForKind(account.ID, "avatar")`,
		`updates["avatar_file_name"] = nil`,
		`updates["avatar_content_type"] = nil`,
		`updates["avatar_file_size"] = nil`,
		`updates["avatar_updated_at"] = nil`,
		`updates["avatar_remote_url"] = ""`,
		`s.removeAccountImageObjects(models.Account{ID: account.ID, HeaderFileName: account.HeaderFileName})`,
		`s.removeAccountLocalImageFilesForKind(account.ID, "header")`,
		`updates["header_file_name"] = nil`,
		`updates["header_content_type"] = nil`,
		`updates["header_file_size"] = nil`,
		`updates["header_updated_at"] = nil`,
		`updates["header_remote_url"] = ""`,
	} {
		if !functionBodyContains(t, src, "destroySettingsProfilePicture", want) {
			t.Fatalf("destroySettingsProfilePicture missing Rails-compatible update %q", want)
		}
	}
}
