package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestParseSettingsPrivacyPayloadAppliesRailsInversions(t *testing.T) {
	body := "account%5Bdiscoverable%5D=1&account%5Bunlocked%5D=0&account%5Bindexable%5D=1&account%5Bshow_collections%5D=1&account%5Bsettings%5D%5Bindexable%5D=0&account%5Bsettings%5D%5Bshow_application%5D=1"
	req := httptest.NewRequest(http.MethodPut, "/settings/privacy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())

	payload, settings, err := parseSettingsPrivacyPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Discoverable == nil || !*payload.Discoverable {
		t.Fatalf("discoverable = %#v", payload.Discoverable)
	}
	if payload.Locked == nil || !*payload.Locked {
		t.Fatalf("locked = %#v", payload.Locked)
	}
	if payload.Indexable == nil || !*payload.Indexable {
		t.Fatalf("indexable = %#v", payload.Indexable)
	}
	if payload.HideCollections == nil || *payload.HideCollections {
		t.Fatalf("hide_collections = %#v", payload.HideCollections)
	}
	if settings["noindex"] != true || settings["show_application"] != true {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestSettingsPrivacyUpdateRequiresWebAuthentication(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPut, "/settings/privacy", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	if err := s.updateSettingsPrivacy(c); err != nil {
		t.Fatal(err)
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/settings/privacy")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status = %d location = %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSettingsPrivacyAccountChangesPublishActorUpdateLikeRails(t *testing.T) {
	src, err := os.ReadFile("settings_privacy.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`reloaded, err = s.accountForUser(user)`,
		`s.triggerAccountWebhook("account.updated", reloaded.ID)`,
		`s.authorizePendingFollowRequestsForUnlockedAccount(c.Request().Context(), *reloaded)`,
		`_ = s.enqueueActivityPubAccountUpdate(*reloaded, activityPubAccountUpdateDebounceDelay)`,
		`if accountChanged || settingsChanged`,
	} {
		if !functionBodyContains(t, src, "updateSettingsPrivacy", want) {
			t.Fatalf("updateSettingsPrivacy must use the reloaded account after privacy changes; missing %q", want)
		}
	}
	for _, stale := range []string{
		`s.triggerAccountWebhook("account.updated", account.ID)`,
		`s.authorizePendingFollowRequestsForUnlockedAccount(c.Request().Context(), *account)`,
		`_ = s.deliverActivityPubAccountUpdate(*account)`,
		`_ = s.enqueueActivityPubAccountUpdate(*account, activityPubAccountUpdateDebounceDelay)`,
	} {
		if functionBodyContains(t, src, "updateSettingsPrivacy", stale) {
			t.Fatalf("updateSettingsPrivacy must not use stale pre-update account data: %q", stale)
		}
	}
}

func TestSettingsPrivacySavedFlashMatchesRailsGenericLocale(t *testing.T) {
	src, err := os.ReadFile("settings_privacy.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "updateSettingsPrivacy", `settingsChangeSavedMessage(locale)`) {
		t.Fatal("updateSettingsPrivacy must use Rails generic.changes_saved_msg flash")
	}
	if functionBodyContains(t, src, "updateSettingsPrivacy", `QueryEscape("Privacy settings saved")`) {
		t.Fatal("updateSettingsPrivacy must not use fixed Go-only success flash")
	}
	if got := settingsChangeSavedMessage("en"); got != "Changes successfully saved!" {
		t.Fatalf("English saved flash = %q", got)
	}
	if got := settingsChangeSavedMessage("ja"); got == "Changes successfully saved!" || !strings.Contains(got, "変更") {
		t.Fatalf("Japanese saved flash did not resolve locale key: %q", got)
	}
	if got := settingsDatabaseUnavailableMessage("ja"); got == "DATABASE_URL is not set" || !strings.Contains(got, "DATABASE_URL") {
		t.Fatalf("Japanese database unavailable flash did not resolve locale key: %q", got)
	}
}
