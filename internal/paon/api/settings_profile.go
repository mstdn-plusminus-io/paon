package api

import (
	"database/sql"
	"errors"
	"html"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func (s *Server) updateSettingsProfile(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "put", "patch") {
		return c.Redirect(http.StatusFound, "/settings/profile")
	}
	payload, err := parseSettingsProfilePayload(c)
	if err != nil {
		if errors.Is(err, errSettingsProfileMissingAccountRoot) {
			return railsWebBadRequest(c)
		}
		return s.renderSettingsProfileError(c, *account, user, settingsProfileInvalidMessage(locale))
	}
	updates, err := accountUpdateMap(payload)
	if err != nil {
		return s.renderSettingsProfileError(c, *account, user, err.Error())
	}
	// Rails validates avatar/header together with the other attributes in
	// account.update, so a rejected upload must not persist the text changes.
	if err := validateSettingsProfileUploads(c, "account[avatar]", "account[header]"); err != nil {
		return s.renderSettingsProfileError(c, settingsProfileAccountWithUpdates(*account, updates), user, err.Error())
	}
	accountChanged := len(updates) > 0
	if len(updates) > 0 {
		now := time.Now().UTC()
		updates["updated_at"] = now
		if err := s.updateAccountRowsAndTags(*account, updates, payload.Note, now); err != nil {
			return err
		}
	}
	uploadsChanged, uploadErr := s.applySettingsProfileUploads(c, account.ID)
	if uploadErr != nil && !accountChanged && !uploadsChanged {
		return s.renderSettingsProfileError(c, *account, user, uploadErr.Error())
	}
	if accountChanged || uploadsChanged {
		reloaded, err := s.findAccountByID(strconv.FormatInt(account.ID, 10))
		if err != nil {
			return err
		}
		s.triggerAccountWebhook("account.updated", reloaded.ID)
		_ = s.enqueueFASPAccountLifecycleUpdate(c.Request().Context(), *account, *reloaded)
		if len(payload.FieldsAttributes) > 0 {
			s.enqueueVerifyAccountLinksIfNeeded(c.Request().Context(), *reloaded, time.Now().UTC())
		}
		_ = s.enqueueActivityPubAccountUpdate(*reloaded, activityPubAccountUpdateDebounceDelay)
		if uploadErr != nil {
			// An image that passed pre-validation but failed while processing
			// leaves earlier changes persisted; they are distributed above and
			// the form is re-rendered from the stored account.
			return s.renderSettingsProfileError(c, *reloaded, user, uploadErr.Error())
		}
	}
	return c.Redirect(http.StatusFound, "/settings/profile?notice="+url.QueryEscape(settingsChangeSavedMessage(locale)))
}

// validateSettingsProfileUploads applies the same pre-storage checks as
// storeAccountImage to every submitted profile image before anything is saved.
func validateSettingsProfileUploads(c *echo.Context, keys ...string) error {
	for _, key := range keys {
		header, err := c.FormFile(key)
		if err != nil {
			continue
		}
		if err := validateSettingsProfileUpload(header); err != nil {
			return err
		}
	}
	return nil
}

func validateSettingsProfileUpload(header *multipart.FileHeader) error {
	if header.Size <= 0 {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: File is empty"}
	}
	if header.Size >= profileImageSizeLimit {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: File size is too large"}
	}
	contentType := mediaContentType(paperclipObfuscatedFilename(header.Filename), header.Header.Get("Content-Type"))
	if !profileImageContentTypeSupported(contentType) {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: File type is invalid"}
	}
	return nil
}

// applySettingsProfileUploads stores avatar and header in order and reports
// whether any image was stored even when a later one fails.
func (s *Server) applySettingsProfileUploads(c *echo.Context, accountID int64) (bool, error) {
	changed := false
	for _, upload := range []struct{ key, kind string }{{"account[avatar]", "avatar"}, {"account[header]", "header"}} {
		header, err := c.FormFile(upload.key)
		if err != nil {
			continue
		}
		if err := s.storeAccountImage(accountID, upload.kind, header); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// settingsProfileAccountWithUpdates mirrors Rails re-rendering the form from
// @account with the submitted attributes assigned but not saved.
func settingsProfileAccountWithUpdates(account models.Account, updates map[string]any) models.Account {
	if value, ok := updates["display_name"].(string); ok {
		account.DisplayName = value
	}
	if value, ok := updates["note"].(string); ok {
		account.Note = value
	}
	if value, ok := updates["actor_type"].(string); ok {
		account.ActorType = sql.NullString{String: value, Valid: true}
	}
	if value, ok := updates["fields"].(models.JSONValue); ok {
		account.Fields = value
	}
	return account
}

func (s *Server) renderSettingsProfileError(c *echo.Context, account models.Account, user *models.User, errorText string) error {
	settings := decodeUserSettings(user.Settings.String)
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, settingsProfileHTMLWithConfigMessages(s.cfg, account, s.packAssetPath("public.js"), "", errorText, locale, settingsWebTheme(settings)))
}

func (s *Server) destroySettingsProfilePicture(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "delete") {
		return c.Redirect(http.StatusFound, "/settings/profile")
	}
	kind := c.Param("id")
	if kind != "avatar" && kind != "header" {
		return railsWebBadRequest(c)
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if kind == "avatar" {
		s.removeAccountImageObjects(models.Account{ID: account.ID, AvatarFileName: account.AvatarFileName})
		s.removeAccountLocalImageFilesForKind(account.ID, "avatar")
		updates["avatar_file_name"] = nil
		updates["avatar_content_type"] = nil
		updates["avatar_file_size"] = nil
		updates["avatar_updated_at"] = nil
		updates["avatar_remote_url"] = ""
	} else {
		s.removeAccountImageObjects(models.Account{ID: account.ID, HeaderFileName: account.HeaderFileName})
		s.removeAccountLocalImageFilesForKind(account.ID, "header")
		updates["header_file_name"] = nil
		updates["header_content_type"] = nil
		updates["header_file_size"] = nil
		updates["header_updated_at"] = nil
		updates["header_remote_url"] = ""
	}
	if err := s.db.Model(&models.Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
		return err
	}
	reloaded, err := s.findAccountByID(strconv.FormatInt(account.ID, 10))
	if err != nil {
		return err
	}
	s.triggerAccountWebhook("account.updated", reloaded.ID)
	_ = s.enqueueFASPAccountLifecycleUpdate(c.Request().Context(), *account, *reloaded)
	_ = s.enqueueActivityPubAccountUpdate(*reloaded, activityPubAccountUpdateDebounceDelay)
	return c.Redirect(http.StatusSeeOther, "/settings/profile?notice="+url.QueryEscape(settingsChangeSavedMessage(locale)))
}

func railsWebBadRequest(c *echo.Context) error {
	if railsWebErrorWantsJSON(c) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": http.StatusText(http.StatusBadRequest)})
	}
	return c.HTML(http.StatusBadRequest, railsWebErrorHTML(http.StatusBadRequest))
}

func railsWebErrorWantsJSON(c *echo.Context) bool {
	if publicRequestHasFormat(c, "json") {
		return true
	}
	bestJSONQ := -1.0
	bestJSONOrder := len(c.Request().Header.Get("Accept"))
	bestHTMLQ := -1.0
	bestHTMLOrder := len(c.Request().Header.Get("Accept"))
	for order, part := range strings.Split(c.Request().Header.Get("Accept"), ",") {
		mediaType, q, _ := parseAcceptEntry(part)
		if q <= 0 {
			continue
		}
		switch mediaType {
		case "application/json", "text/x-json", "application/jsonrequest", "application/jrd+json", "application/activity+json", "application/ld+json":
			if q > bestJSONQ || (q == bestJSONQ && order < bestJSONOrder) {
				bestJSONQ = q
				bestJSONOrder = order
			}
		case "text/html", "application/xhtml+xml":
			if q > bestHTMLQ || (q == bestHTMLQ && order < bestHTMLOrder) {
				bestHTMLQ = q
				bestHTMLOrder = order
			}
		}
	}
	return bestJSONQ > bestHTMLQ || (bestJSONQ == bestHTMLQ && bestJSONOrder < bestHTMLOrder)
}

func railsWebErrorHTML(status int) string {
	title := html.EscapeString(http.StatusText(status))
	return `<!DOCTYPE html>
<html>
  <head>
    <meta content="text/html; charset=UTF-8" http-equiv="Content-Type">
    <meta charset="utf-8">
    <title>` + title + `</title>
  </head>
  <body class="error">
    <div class="dialog">
      <div class="dialog__message">
        <h1>` + title + `</h1>
      </div>
    </div>
  </body>
</html>`
}

var errSettingsProfileMissingAccountRoot = errors.New("settings profile account root parameter is missing")

func parseSettingsProfilePayload(c *echo.Context) (accountUpdatePayload, error) {
	var payload accountUpdatePayload
	req := c.Request()
	if err := req.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		if err := req.ParseForm(); err != nil {
			return payload, err
		}
	}
	if !settingsProfileAccountRootPresent(req) {
		return payload, errSettingsProfileMissingAccountRoot
	}
	payload.DisplayName = stringPtrFromForm(c, "account[display_name]")
	payload.Note = stringPtrFromForm(c, "account[note]")
	payload.Bot = boolPtrFromForm(c, "account[bot]")
	payload.FieldsAttributes = profileFieldsFromForm(req.Form, "account[fields_attributes][")
	return payload, nil
}

func settingsProfileAccountRootPresent(req *http.Request) bool {
	for key := range req.Form {
		if strings.HasPrefix(key, "account[") {
			return true
		}
	}
	if req.MultipartForm != nil {
		for key := range req.MultipartForm.Value {
			if strings.HasPrefix(key, "account[") {
				return true
			}
		}
		for key := range req.MultipartForm.File {
			if strings.HasPrefix(key, "account[") {
				return true
			}
		}
	}
	return false
}

func methodOverrideIs(c *echo.Context, allowed ...string) bool {
	method := strings.ToLower(strings.TrimSpace(c.FormValue("_method")))
	if method == "" {
		method = strings.ToLower(c.Request().Method)
	}
	for _, value := range allowed {
		if method == strings.ToLower(value) {
			return true
		}
	}
	return false
}
