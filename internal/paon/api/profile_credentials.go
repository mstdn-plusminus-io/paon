package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type accountUpdatePayload struct {
	DisplayName        *string         `json:"display_name"`
	Note               *string         `json:"note"`
	Locked             *bool           `json:"locked"`
	Bot                *bool           `json:"bot"`
	Discoverable       *bool           `json:"discoverable"`
	HideCollections    *bool           `json:"hide_collections"`
	Indexable          *bool           `json:"indexable"`
	AttributionDomains *[]string       `json:"attribution_domains"`
	FieldsAttributes   []profileField  `json:"fields_attributes"`
	RawFields          json.RawMessage `json:"-"`
	Source             *sourcePayload  `json:"source"`
}

type sourcePayload struct {
	Privacy   *string `json:"privacy"`
	Sensitive *bool   `json:"sensitive"`
	Language  *string `json:"language"`
}

type profileField struct {
	Name       string  `json:"name"`
	Value      string  `json:"value"`
	VerifiedAt *string `json:"verified_at,omitempty"`
}

var profileNoteURLPattern = regexp.MustCompile(`(^|[\s(])https?://[^\s<]+`)

func (s *Server) updateCredentials(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	user, _, err := s.requireUserScope(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	account, err := s.accountForUser(user)
	if err != nil {
		return err
	}
	payload, err := parseAccountUpdatePayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	updates, err := accountUpdateMap(payload)
	if err != nil {
		return err
	}
	accountChanged := len(updates) > 0
	if len(updates) > 0 {
		now := time.Now().UTC()
		updates["updated_at"] = now
		if err := s.updateAccountRowsAndTags(*account, updates, payload.Note, now); err != nil {
			return err
		}
	}
	uploadsChanged, err := s.applyProfileUploads(c, account.ID)
	if err != nil {
		return err
	}
	accountChanged = accountChanged || uploadsChanged
	if payload.Source != nil {
		if err := s.updateUserPostingSettings(user.ID, payload.Source); err != nil {
			return err
		}
		user, _, err = s.requireUser(c)
		if err != nil {
			return err
		}
	}
	reloaded, err := s.findAccountByID(strconv.FormatInt(account.ID, 10))
	if err != nil {
		return err
	}
	count, err := s.followRequestsCount(reloaded.ID)
	if err != nil {
		return err
	}
	if accountChanged {
		s.triggerAccountWebhook("account.updated", reloaded.ID)
		_ = s.enqueueFASPAccountLifecycleUpdate(c.Request().Context(), *account, *reloaded)
		if payload.RawFields != nil || len(payload.FieldsAttributes) > 0 {
			s.enqueueVerifyAccountLinksIfNeeded(c.Request().Context(), *reloaded, time.Now().UTC())
		}
		if payload.Locked != nil && account.Locked && !*payload.Locked {
			if err := s.authorizePendingFollowRequestsForUnlockedAccount(c.Request().Context(), *reloaded); err != nil {
				return err
			}
		}
		_ = s.enqueueActivityPubAccountUpdate(*reloaded, activityPubAccountUpdateDebounceDelay)
	}
	role, everyone := s.initialStateUserRole(user)
	return c.JSON(http.StatusOK, serializer.CredentialAccountFromModelWithRole(s.cfg, *reloaded, *user, count, role, everyone))
}

func (s *Server) updateAccountRowsAndTags(account models.Account, updates map[string]any, note *string, now time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.preserveProfileFieldVerificationsInUpdates(tx, account, updates); err != nil {
			return err
		}
		if err := tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
			return err
		}
		syncNote := account.Note
		if note != nil {
			syncNote = *note
		}
		return syncAccountTagsFromNote(tx, account.ID, syncNote, now)
	})
}

func (s *Server) syncAccountTagsForAccount(account models.Account, note *string, now time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		syncNote := account.Note
		if note != nil {
			syncNote = *note
		}
		return syncAccountTagsFromNote(tx, account.ID, syncNote, now)
	})
}

func (s *Server) preserveProfileFieldVerificationsInUpdates(tx *gorm.DB, account models.Account, updates map[string]any) error {
	raw, ok := updates["fields"].(models.JSONValue)
	if !ok {
		return nil
	}
	var current models.Account
	if err := tx.Select("fields").Where("id = ?", account.ID).First(&current).Error; err != nil {
		return err
	}
	var fields []profileField
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	fields = preserveProfileFieldVerifications(fields, current.Fields)
	encoded, _ := json.Marshal(fields)
	updates["fields"] = models.JSONValue(encoded)
	return nil
}

func preserveProfileFieldVerifications(fields []profileField, currentRaw []byte) []profileField {
	current := profileFieldsFromRaw(currentRaw)
	verifiedByValue := map[string]*string{}
	for _, field := range current {
		value := strings.TrimSpace(field.Value)
		if value == "" || field.VerifiedAt == nil || strings.TrimSpace(*field.VerifiedAt) == "" {
			continue
		}
		verified := *field.VerifiedAt
		verifiedByValue[value] = &verified
	}
	for i := range fields {
		if fields[i].VerifiedAt != nil {
			continue
		}
		if verified, ok := verifiedByValue[strings.TrimSpace(fields[i].Value)]; ok {
			fields[i].VerifiedAt = verified
		}
	}
	return fields
}

func syncAccountTagsFromNote(tx *gorm.DB, accountID int64, note string, now time.Time) error {
	refs := statusHashtagRefs(note)
	tagIDs := make([]int64, 0, len(refs))
	for _, ref := range refs {
		tag, err := findOrCreateStatusTag(tx, ref.Normalized, ref.Display, now)
		if err != nil {
			return err
		}
		tagIDs = append(tagIDs, tag.ID)
	}
	tagIDs = uniqueInt64s(tagIDs)
	if len(tagIDs) == 0 {
		return tx.Exec("DELETE FROM accounts_tags WHERE account_id = ?", accountID).Error
	}
	if err := tx.Exec("DELETE FROM accounts_tags WHERE account_id = ? AND tag_id NOT IN ?", accountID, tagIDs).Error; err != nil {
		return err
	}
	for _, tagID := range tagIDs {
		if err := tx.Exec("INSERT INTO accounts_tags (account_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", accountID, tagID).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) applyProfileUploads(c *echo.Context, accountID int64) (bool, error) {
	return s.applyProfileUploadsForKeys(c, accountID, "avatar", "header")
}

func (s *Server) applyProfileUploadsForKeys(c *echo.Context, accountID int64, avatarKey string, headerKey string) (bool, error) {
	changed := false
	if header, err := c.FormFile(avatarKey); err == nil {
		if err := s.storeAccountImage(accountID, "avatar", header); err != nil {
			return false, err
		}
		changed = true
	}
	if header, err := c.FormFile(headerKey); err == nil {
		if err := s.storeAccountImage(accountID, "header", header); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func (s *Server) storeAccountImage(accountID int64, kind string, header *multipart.FileHeader) error {
	if header.Size <= 0 {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: File is empty"}
	}
	if header.Size >= profileImageSizeLimit {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: File size is too large"}
	}
	filename := paperclipObfuscatedFilename(header.Filename)
	contentType := mediaContentType(filename, header.Header.Get("Content-Type"))
	if !profileImageContentTypeSupported(contentType) {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: File type is invalid"}
	}
	storedFilename, storedContentType, storedSize, err := s.storeAccountImageFile(header, accountID, kind, filename, contentType)
	if err != nil {
		return err
	}
	updates := profileImageUpdates(kind, storedFilename, storedContentType, storedSize, time.Now().UTC())
	return s.db.Model(&models.Account{}).Where("id = ?", accountID).Updates(updates).Error
}

func (s *Server) storeAccountImageFile(header *multipart.FileHeader, accountID int64, kind string, filename string, contentType string) (string, string, int64, error) {
	src, err := header.Open()
	if err != nil {
		return "", "", 0, err
	}
	defer src.Close()

	data, err := io.ReadAll(src)
	if err != nil {
		return "", "", 0, err
	}
	if strings.EqualFold(strings.TrimSpace(contentType), "image/webp") {
		contentType = "image/png"
		if ext := filepath.Ext(filename); ext != "" {
			filename = strings.TrimSuffix(filename, ext) + ".png"
		} else {
			filename += ".png"
		}
	}
	data, err = resizeAccountImageBuffer(kind, data, contentType)
	if err != nil {
		return "", "", 0, apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: File type is invalid"}
	}

	target := s.accountImagePath(accountID, kind, filename)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", "", 0, err
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return "", "", 0, err
	}
	if err := s.uploadPaperclipObject(context.Background(), accountImageObjectKey(accountID, kind, "original", filename), target, contentType); err != nil {
		return "", "", 0, err
	}
	if err := s.storeAccountImageStaticStyle(accountID, kind, filename, target, contentType); err != nil {
		return "", "", 0, err
	}
	return filename, contentType, int64(len(data)), nil
}

func (s *Server) accountImagePath(accountID int64, kind string, filename string) string {
	return s.accountImagePathStyleWithCachePrefix(accountID, kind, "original", filename, false)
}

func (s *Server) accountImagePathStyle(accountID int64, kind string, style string, filename string) string {
	return s.accountImagePathStyleWithCachePrefix(accountID, kind, style, filename, false)
}

func (s *Server) accountImagePathStyleWithCachePrefix(accountID int64, kind string, style string, filename string, cachePrefix bool) string {
	dir := "avatars"
	if kind == "header" {
		dir = "headers"
	}
	parts := []string{"accounts", dir, mediaPaperclipIDPartition(accountID), style, filename}
	if cachePrefix {
		parts = append([]string{"cache"}, parts...)
	}
	return s.cfg.SystemAssetPath(parts...)
}

func (s *Server) storeAccountImageStaticStyle(accountID int64, kind string, filename string, originalPath string, contentType string) error {
	return s.storeAccountImageStaticStyleWithCachePrefix(accountID, kind, filename, originalPath, contentType, false)
}

func (s *Server) storeAccountImageStaticStyleWithCachePrefix(accountID int64, kind string, filename string, originalPath string, contentType string, cachePrefix bool) error {
	staticFilename := profileImageStaticFilename(filename, contentType)
	staticTarget := s.accountImagePathStyleWithCachePrefix(accountID, kind, "static", staticFilename, cachePrefix)
	staticContentType := contentType
	if strings.EqualFold(strings.TrimSpace(strings.Split(contentType, ";")[0]), "image/gif") {
		staticContentType = "image/png"
		if err := writeStaticPNGFromImageFile(originalPath, staticTarget); err != nil {
			return err
		}
	} else if err := copyFile(originalPath, staticTarget); err != nil {
		return err
	}
	return s.uploadPaperclipObject(context.Background(), accountImageObjectKeyWithCachePrefix(accountID, kind, "static", staticFilename, cachePrefix), staticTarget, staticContentType)
}

func profileImageStaticFilename(filename string, contentType string) string {
	if !strings.EqualFold(strings.TrimSpace(strings.Split(contentType, ";")[0]), "image/gif") {
		return filename
	}
	base := filename
	if index := strings.LastIndex(base, "."); index > 0 {
		base = base[:index]
	}
	return base + ".png"
}

func writeStaticPNGFromImageFile(source string, target string) error {
	return writeVIPSStaticPNG(source, target)
}

func profileImageContentTypeSupported(contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func profileImageUpdates(kind string, filename string, contentType string, size int64, now time.Time) map[string]any {
	updates := map[string]any{"updated_at": now}
	if kind == "avatar" {
		updates["avatar_file_name"] = sql.NullString{String: filename, Valid: true}
		updates["avatar_content_type"] = sql.NullString{String: contentType, Valid: true}
		updates["avatar_file_size"] = sql.NullInt64{Int64: size, Valid: true}
		updates["avatar_updated_at"] = sql.NullTime{Time: now, Valid: true}
		updates["avatar_remote_url"] = nil
		return updates
	}
	updates["header_file_name"] = sql.NullString{String: filename, Valid: true}
	updates["header_content_type"] = sql.NullString{String: contentType, Valid: true}
	updates["header_file_size"] = sql.NullInt64{Int64: size, Valid: true}
	updates["header_updated_at"] = sql.NullTime{Time: now, Valid: true}
	updates["header_remote_url"] = ""
	return updates
}

func (s *Server) deleteProfileAvatar(c *echo.Context) error {
	return s.deleteProfileImage(c, "avatar")
}

func (s *Server) deleteProfileHeader(c *echo.Context) error {
	return s.deleteProfileImage(c, "header")
}

func (s *Server) deleteProfileImage(c *echo.Context, kind string) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if kind == "avatar" {
		if account.AvatarFileName.Valid && strings.TrimSpace(account.AvatarFileName.String) != "" {
			s.bustAccountImageKindCache(account.ID, "avatar", account.AvatarFileName.String, account.AvatarContentType.String)
			s.deletePaperclipObject(c.Request().Context(), accountImageObjectKey(account.ID, "avatar", "original", account.AvatarFileName.String))
			s.deletePaperclipObject(c.Request().Context(), accountImageObjectKey(account.ID, "avatar", "static", profileImageStaticFilename(account.AvatarFileName.String, account.AvatarContentType.String)))
		}
		s.removeAccountLocalImageFilesForKind(account.ID, "avatar")
		updates["avatar_file_name"] = nil
		updates["avatar_content_type"] = nil
		updates["avatar_file_size"] = nil
		updates["avatar_updated_at"] = nil
		updates["avatar_remote_url"] = nil
	} else {
		if account.HeaderFileName.Valid && strings.TrimSpace(account.HeaderFileName.String) != "" {
			s.bustAccountImageKindCache(account.ID, "header", account.HeaderFileName.String, account.HeaderContentType.String)
			s.deletePaperclipObject(c.Request().Context(), accountImageObjectKey(account.ID, "header", "original", account.HeaderFileName.String))
			s.deletePaperclipObject(c.Request().Context(), accountImageObjectKey(account.ID, "header", "static", profileImageStaticFilename(account.HeaderFileName.String, account.HeaderContentType.String)))
		}
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
	count, err := s.followRequestsCount(reloaded.ID)
	if err != nil {
		return err
	}
	user, _, err := s.requireUser(c)
	if err != nil {
		return err
	}
	s.triggerAccountWebhook("account.updated", reloaded.ID)
	_ = s.enqueueFASPAccountLifecycleUpdate(c.Request().Context(), *account, *reloaded)
	_ = s.enqueueActivityPubAccountUpdate(*reloaded, activityPubAccountUpdateDebounceDelay)
	role, everyone := s.initialStateUserRole(user)
	return c.JSON(http.StatusOK, serializer.CredentialAccountFromModelWithRole(s.cfg, *reloaded, *user, count, role, everyone))
}

func parseAccountUpdatePayload(c *echo.Context) (accountUpdatePayload, error) {
	var payload accountUpdatePayload
	if strings.Contains(c.Request().Header.Get("Content-Type"), "application/json") {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		decodeRaw(raw, "display_name", &payload.DisplayName)
		decodeRaw(raw, "note", &payload.Note)
		decodeRaw(raw, "locked", &payload.Locked)
		decodeRaw(raw, "bot", &payload.Bot)
		decodeRaw(raw, "discoverable", &payload.Discoverable)
		decodeRaw(raw, "hide_collections", &payload.HideCollections)
		decodeRaw(raw, "indexable", &payload.Indexable)
		decodeRaw(raw, "attribution_domains", &payload.AttributionDomains)
		if rawFields, ok := raw["fields_attributes"]; ok {
			payload.RawFields = rawFields
			payload.FieldsAttributes = profileFieldsFromRaw(rawFields)
		}
		if rawSource, ok := raw["source"]; ok && string(rawSource) != "null" && !jsonObjectEmpty(rawSource) {
			var source sourcePayload
			if json.Unmarshal(rawSource, &source) == nil {
				payload.Source = &source
			}
		}
		return payload, nil
	}

	req := c.Request()
	if err := req.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		if err := req.ParseForm(); err != nil {
			return payload, err
		}
	}
	payload.DisplayName = stringPtrFromForm(c, "display_name")
	payload.Note = stringPtrFromForm(c, "note")
	payload.Locked = boolPtrFromForm(c, "locked")
	payload.Bot = boolPtrFromForm(c, "bot")
	payload.Discoverable = boolPtrFromForm(c, "discoverable")
	payload.HideCollections = boolPtrFromForm(c, "hide_collections")
	payload.Indexable = boolPtrFromForm(c, "indexable")
	if values, ok := req.Form["attribution_domains[]"]; ok {
		copyValues := append([]string(nil), values...)
		payload.AttributionDomains = &copyValues
	} else if values, ok := req.Form["attribution_domains"]; ok {
		copyValues := append([]string(nil), values...)
		payload.AttributionDomains = &copyValues
	}
	payload.FieldsAttributes = profileFieldsFromForm(req.Form, "fields_attributes[")
	payload.Source = sourcePayloadFromForm(c)
	return payload, nil
}

func accountUpdateMap(payload accountUpdatePayload) (map[string]any, error) {
	updates := map[string]any{}
	if payload.DisplayName != nil {
		displayName := strings.TrimSpace(*payload.DisplayName)
		if len([]rune(displayName)) > 30 {
			return nil, apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Display name is too long"}
		}
		updates["display_name"] = displayName
	}
	if payload.Note != nil {
		note := strings.TrimSpace(*payload.Note)
		if profileNoteTooLong(note, 500) {
			return nil, apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Bio is too long"}
		}
		updates["note"] = note
	}
	if payload.Locked != nil {
		updates["locked"] = *payload.Locked
	}
	if payload.Bot != nil {
		if *payload.Bot {
			updates["actor_type"] = "Service"
		} else {
			updates["actor_type"] = "Person"
		}
	}
	if payload.Discoverable != nil {
		updates["discoverable"] = sql.NullBool{Bool: *payload.Discoverable, Valid: true}
	}
	if payload.HideCollections != nil {
		updates["hide_collections"] = sql.NullBool{Bool: *payload.HideCollections, Valid: true}
	}
	if payload.Indexable != nil {
		updates["indexable"] = *payload.Indexable
	}
	if payload.AttributionDomains != nil {
		domains, err := localAttributionDomains(strings.Join(*payload.AttributionDomains, "\n"))
		if err != nil {
			return nil, apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Attribution domains is invalid"}
		}
		updates["attribution_domains"] = models.StringArray(domains)
	}
	if payload.RawFields != nil || len(payload.FieldsAttributes) > 0 {
		fields := cleanProfileFields(payload.FieldsAttributes)
		if len(fields) > 4 {
			return nil, apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Fields is too long"}
		}
		encoded, _ := json.Marshal(fields)
		updates["fields"] = models.JSONValue(encoded)
	}
	return updates, nil
}

func profileNoteTooLong(note string, max int) bool {
	if max <= 0 {
		return false
	}
	return graphemeLength(profileNoteCountableText(note)) > max
}

func profileNoteCountableText(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	replaced := profileNoteURLPattern.ReplaceAllStringFunc(note, func(match string) string {
		prefix := ""
		urlText := match
		if !strings.HasPrefix(match, "http://") && !strings.HasPrefix(match, "https://") {
			prefix = match[:1]
			urlText = match[1:]
		}
		if len([]rune(urlText)) > 4096 {
			return match
		}
		return prefix + strings.Repeat("x", 23)
	})
	return statusLengthRemoteMention.ReplaceAllStringFunc(replaced, func(match string) string {
		indices := statusLengthRemoteMention.FindStringSubmatchIndex(match)
		if len(indices) < 8 {
			return match
		}
		prefix := match[indices[2]:indices[3]]
		username := match[indices[4]:indices[5]]
		domain := match[indices[6]:indices[7]]
		if !statusLengthMentionDomainCountable(domain) {
			return match
		}
		return prefix + "@" + username
	})
}

func decodeRaw[T any](raw map[string]json.RawMessage, key string, out **T) {
	if value, ok := raw[key]; ok && string(value) != "null" {
		var decoded T
		if json.Unmarshal(value, &decoded) == nil {
			*out = &decoded
		}
	}
}

func jsonObjectEmpty(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	return len(object) == 0
}

func stringPtrFromForm(c *echo.Context, key string) *string {
	if _, ok := c.Request().Form[key]; !ok {
		return nil
	}
	value := lastFormValue(c.Request().Form, key)
	return &value
}

func boolPtrFromForm(c *echo.Context, key string) *bool {
	if _, ok := c.Request().Form[key]; !ok {
		return nil
	}
	value := truthy(lastFormValue(c.Request().Form, key))
	return &value
}

func lastFormValue(values map[string][]string, key string) string {
	rawValues := values[key]
	if len(rawValues) == 0 {
		return ""
	}
	return rawValues[len(rawValues)-1]
}

func profileFieldsFromRaw(raw json.RawMessage) []profileField {
	var asList []profileField
	if json.Unmarshal(raw, &asList) == nil {
		return asList
	}
	var asMap map[string]profileField
	if json.Unmarshal(raw, &asMap) == nil {
		return orderedProfileFields(asMap)
	}
	return nil
}

func profileFieldsFromForm(values map[string][]string, prefixes ...string) []profileField {
	fields := map[string]*profileField{}
	for key, rawValues := range values {
		var rest string
		var ok bool
		for _, prefix := range prefixes {
			rest, ok = strings.CutPrefix(key, prefix)
			if ok {
				break
			}
		}
		if !ok || len(rawValues) == 0 {
			continue
		}
		parts := strings.SplitN(rest, "][", 2)
		if len(parts) != 2 {
			continue
		}
		index := strings.TrimSuffix(parts[0], "]")
		name := strings.TrimSuffix(parts[1], "]")
		field := fields[index]
		if field == nil {
			field = &profileField{}
			fields[index] = field
		}
		switch name {
		case "name":
			field.Name = rawValues[0]
		case "value":
			field.Value = rawValues[0]
		}
	}
	return orderedProfileFieldsPtr(fields)
}

func orderedProfileFieldsPtr(fields map[string]*profileField) []profileField {
	copied := make(map[string]profileField, len(fields))
	for key, field := range fields {
		if field != nil {
			copied[key] = *field
		}
	}
	return orderedProfileFields(copied)
}

func orderedProfileFields(fields map[string]profileField) []profileField {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, leftErr := strconv.Atoi(keys[i])
		right, rightErr := strconv.Atoi(keys[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return keys[i] < keys[j]
	})
	out := make([]profileField, 0, len(keys))
	for _, key := range keys {
		out = append(out, fields[key])
	}
	return out
}

func cleanProfileFields(fields []profileField) []profileField {
	out := make([]profileField, 0, len(fields))
	for _, field := range fields {
		field.Name = sanitizeLocalProfileFieldText(field.Name)
		if field.Name == "" {
			continue
		}
		field.Value = sanitizeLocalProfileFieldText(field.Value)
		field.VerifiedAt = nil
		out = append(out, field)
	}
	return out
}

func sanitizeLocalProfileFieldText(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 255 {
		return string(runes[:255])
	}
	return value
}

func sourcePayloadFromForm(c *echo.Context) *sourcePayload {
	source := sourcePayload{}
	seen := false
	if value := stringPtrFromForm(c, "source[privacy]"); value != nil {
		source.Privacy = value
		seen = true
	}
	if value := boolPtrFromForm(c, "source[sensitive]"); value != nil {
		source.Sensitive = value
		seen = true
	}
	if value := stringPtrFromForm(c, "source[language]"); value != nil {
		source.Language = value
		seen = true
	}
	if !seen {
		return nil
	}
	return &source
}

func (s *Server) accountForUser(user *models.User) (*models.Account, error) {
	var account models.Account
	err := accountSerializerPreloads(s.db).Where("id = ?", user.AccountID).First(&account).Error
	return &account, err
}

func (s *Server) followRequestsCount(accountID int64) (int64, error) {
	var count int64
	err := s.db.Model(&models.Account{}).
		Joins("JOIN follow_requests ON follow_requests.account_id = accounts.id").
		Where("follow_requests.target_account_id = ? AND accounts.suspended_at IS NULL", accountID).
		Limit(40).
		Count(&count).Error
	return count, err
}

func (s *Server) updateUserPostingSettings(userID int64, source *sourcePayload) error {
	var user models.User
	if err := s.db.Select("id, settings").Where("id = ?", userID).First(&user).Error; err != nil {
		return err
	}
	settings := map[string]any{}
	if user.Settings.Valid && strings.TrimSpace(user.Settings.String) != "" {
		_ = json.Unmarshal([]byte(user.Settings.String), &settings)
	}
	if err := applyUserPostingSettings(settings, source); err != nil {
		return err
	}
	encoded, _ := json.Marshal(settings)
	return s.db.Model(&models.User{}).Where("id = ?", userID).Update("settings", string(encoded)).Error
}

func applyUserPostingSettings(settings map[string]any, source *sourcePayload) error {
	if source == nil {
		return nil
	}
	if source.Privacy != nil {
		if err := applyUserPostingPrivacySetting(settings, *source.Privacy); err != nil {
			return err
		}
	}
	if source.Sensitive != nil {
		settings["default_sensitive"] = *source.Sensitive
	}
	if source.Language != nil {
		settings["default_language"] = *source.Language
	}
	return nil
}

func applyUserPostingPrivacySetting(settings map[string]any, raw string) error {
	value := strings.TrimSpace(raw)
	if value != "public" && value != "unlisted" && value != "private" {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Default privacy is invalid"}
	}
	settings["default_privacy"] = value
	return nil
}
