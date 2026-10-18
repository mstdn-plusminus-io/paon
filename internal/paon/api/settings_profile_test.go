package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
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

type settingsProfileTestUpload struct {
	key         string
	filename    string
	contentType string
	size        int
}

func settingsProfileMultipartContext(t *testing.T, uploads ...settingsProfileTestUpload) *echo.Context {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("account[display_name]", "Alice"); err != nil {
		t.Fatal(err)
	}
	for _, upload := range uploads {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+upload.key+`"; filename="`+upload.filename+`"`)
		header.Set("Content-Type", upload.contentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(bytes.Repeat([]byte{0x89}, upload.size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/settings/profile", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if _, err := parseSettingsProfilePayload(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestValidateSettingsProfileUploadsRejectsInvalidImagesBeforeSaving(t *testing.T) {
	for _, tc := range []struct {
		name   string
		upload settingsProfileTestUpload
		want   string
	}{
		{"empty avatar", settingsProfileTestUpload{"account[avatar]", "avatar.png", "image/png", 0}, "Validation failed: File is empty"},
		{"oversized avatar", settingsProfileTestUpload{"account[avatar]", "avatar.png", "image/png", profileImageSizeLimit}, "Validation failed: File size is too large"},
		{"unsupported header", settingsProfileTestUpload{"account[header]", "header.txt", "text/plain", 16}, "Validation failed: File type is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := settingsProfileMultipartContext(t, tc.upload)
			err := validateSettingsProfileUploads(c, "account[avatar]", "account[header]")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("validate error = %v, want %q", err, tc.want)
			}
			header, ferr := c.FormFile(tc.upload.key)
			if ferr != nil {
				t.Fatal(ferr)
			}
			// The pre-validation must reject exactly what storeAccountImage rejects
			// so no text change is committed for an upload that would fail anyway.
			if storeErr := (&Server{}).storeAccountImage(1, "avatar", header); storeErr == nil || storeErr.Error() != err.Error() {
				t.Fatalf("storeAccountImage error = %v, want %v", storeErr, err)
			}
		})
	}
}

func TestValidateSettingsProfileUploadsAcceptsSupportedImagesAndMissingFiles(t *testing.T) {
	c := settingsProfileMultipartContext(t,
		settingsProfileTestUpload{"account[avatar]", "avatar.png", "image/png", 128},
		settingsProfileTestUpload{"account[header]", "header.jpg", "image/jpeg", profileImageSizeLimit - 1},
	)
	if err := validateSettingsProfileUploads(c, "account[avatar]", "account[header]"); err != nil {
		t.Fatalf("supported uploads rejected: %v", err)
	}
	if err := validateSettingsProfileUploads(settingsProfileMultipartContext(t), "account[avatar]", "account[header]"); err != nil {
		t.Fatalf("request without uploads rejected: %v", err)
	}
}

func TestSettingsProfileAccountWithUpdatesKeepsSubmittedValuesLikeRailsRenderShow(t *testing.T) {
	fields, _ := json.Marshal([]profileField{{Name: "Site", Value: "https://new.example"}})
	original := models.Account{
		ID:          42,
		Username:    "alice",
		DisplayName: "Old",
		Note:        "Old bio",
		ActorType:   sql.NullString{String: "Person", Valid: true},
		Fields:      []byte(`[{"name":"Old","value":"old"}]`),
	}
	got := settingsProfileAccountWithUpdates(original, map[string]any{
		"display_name": "New",
		"note":         "New bio",
		"actor_type":   "Service",
		"fields":       models.JSONValue(fields),
	})
	if got.DisplayName != "New" || got.Note != "New bio" || got.ActorType.String != "Service" || string(got.Fields) != string(fields) {
		t.Fatalf("overlaid account = %#v", got)
	}
	if original.DisplayName != "Old" {
		t.Fatal("overlay must not mutate the stored account")
	}
	html := settingsProfileHTMLWithConfigMessages(config.Config{WebDomain: "https://example.test"}, got, "", "", "Validation failed: File size is too large")
	for _, want := range []string{`value="New"`, `New bio</textarea>`, `value="https://new.example"`, `name="account[bot]" value="1" checked`, `File size is too large`} {
		if !strings.Contains(html, want) {
			t.Fatalf("error page missing submitted value %q", want)
		}
	}
}

func TestUpdateSettingsProfileValidatesUploadsBeforePersistingText(t *testing.T) {
	src, err := os.ReadFile("settings_profile.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "updateSettingsProfile")
	validate := strings.Index(body, `validateSettingsProfileUploads(c, "account[avatar]", "account[header]")`)
	persist := strings.Index(body, `s.updateAccountRowsAndTags(*account, updates, payload.Note, now)`)
	if validate < 0 || persist < 0 || validate > persist {
		t.Fatalf("uploads must be validated before account rows are written (validate=%d persist=%d)", validate, persist)
	}
	if !strings.Contains(body, `s.renderSettingsProfileError(c, settingsProfileAccountWithUpdates(*account, updates), user, err.Error())`) {
		t.Fatal("rejected uploads must re-render the form with the submitted values")
	}
	publish := strings.Index(body, `_ = s.enqueueActivityPubAccountUpdate(*reloaded, activityPubAccountUpdateDebounceDelay)`)
	lateError := strings.Index(body, `return s.renderSettingsProfileError(c, *reloaded, user, uploadErr.Error())`)
	if publish < 0 || lateError < 0 || lateError < publish {
		t.Fatal("a processing failure after persisting changes must still distribute them and render the reloaded account")
	}
}
