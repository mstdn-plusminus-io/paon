package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

type filterPayload struct {
	Title              string                 `json:"title" form:"title"`
	TitleSet           bool                   `json:"-" form:"-"`
	Context            []string               `json:"context" form:"context"`
	ContextSet         bool                   `json:"-" form:"-"`
	FilterAction       string                 `json:"filter_action" form:"filter_action"`
	ExpiresIn          *int64                 `json:"expires_in" form:"expires_in"`
	ExpiresInSet       bool                   `json:"-" form:"-"`
	KeywordsAttributes []filterKeywordPayload `json:"keywords_attributes" form:"keywords_attributes"`
}

type v1FilterPayload struct {
	Phrase       string   `json:"phrase" form:"phrase"`
	PhraseSet    bool     `json:"-" form:"-"`
	Context      []string `json:"context" form:"context"`
	ContextSet   bool     `json:"-" form:"-"`
	ExpiresIn    *int64   `json:"expires_in" form:"expires_in"`
	ExpiresInSet bool     `json:"-" form:"-"`
	Irreversible *bool    `json:"irreversible" form:"irreversible"`
	WholeWord    *bool    `json:"whole_word" form:"whole_word"`
}

type filterKeywordPayload struct {
	ID         string `json:"id" form:"id"`
	Keyword    string `json:"keyword" form:"keyword"`
	KeywordSet bool   `json:"-" form:"-"`
	WholeWord  *bool  `json:"whole_word" form:"whole_word"`
	Destroy    bool   `json:"_destroy" form:"_destroy"`
}

type filterStatusPayload struct {
	StatusID string `json:"status_id" form:"status_id"`
}

func (s *Server) v1Filters(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:filters")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	var keywords []models.CustomFilterKeyword
	err = s.db.Preload("CustomFilter").
		Joins("JOIN custom_filters ON custom_filters.id = custom_filter_keywords.custom_filter_id").
		Where("custom_filters.account_id = ?", account.ID).
		Order("custom_filter_keywords.id ASC").
		Find(&keywords).Error
	if err != nil {
		return err
	}
	out := make([]serializer.V1Filter, 0, len(keywords))
	for _, keyword := range keywords {
		out = append(out, serializer.V1FilterFromKeyword(keyword))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showV1Filter(c *echo.Context) error {
	keyword, err := s.findFilterKeyword(c, c.Param("id"), "read", "read:filters")
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	c.Response().Header().Set("Vary", "Authorization")
	return c.JSON(http.StatusOK, serializer.V1FilterFromKeyword(*keyword))
}

func (s *Server) createV1Filter(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:filters")
	if err != nil {
		return err
	}
	payload, err := parseV1FilterPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	contexts := normalizeFilterContexts(payload.Context)
	if !validFilterKeyword(payload.Phrase) || len(contexts) == 0 || !validFilterContexts(contexts) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter is invalid")
	}
	now := time.Now().UTC()
	action := 0
	if payload.Irreversible != nil && *payload.Irreversible {
		action = 1
	}
	wholeWord := true
	if payload.WholeWord != nil {
		wholeWord = *payload.WholeWord
	}
	filter := models.CustomFilter{
		AccountID: models.CustomFilterAccountID(account.ID),
		Phrase:    payload.Phrase,
		Context:   models.StringArray(contexts),
		Action:    action,
		ExpiresAt: expiresAtFromSeconds(payload.ExpiresIn, now),
		CreatedAt: now,
		UpdatedAt: now,
	}
	keyword := models.CustomFilterKeyword{Keyword: payload.Phrase, WholeWord: wholeWord, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&filter).Error; err != nil {
			return err
		}
		keyword.CustomFilterID = filter.ID
		return tx.Create(&keyword).Error
	}); err != nil {
		return err
	}
	created, err := s.findFilterKeyword(c, strconv.FormatInt(keyword.ID, 10), "write", "write:filters")
	if err != nil {
		return err
	}
	s.invalidateFilterCacheAndBroadcast(account.ID)
	return c.JSON(http.StatusOK, serializer.V1FilterFromKeyword(*created))
}

func (s *Server) updateV1Filter(c *echo.Context) error {
	keyword, err := s.findFilterKeyword(c, c.Param("id"), "write", "write:filters")
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	payload, err := parseV1FilterPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	now := time.Now().UTC()
	keywordUpdates := map[string]any{"updated_at": now}
	filterUpdates := map[string]any{"updated_at": now}
	if payload.PhraseSet && strings.TrimSpace(payload.Phrase) == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Keyword can't be blank")
	}
	if payload.PhraseSet && !validFilterKeyword(payload.Phrase) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Keyword is invalid")
	}
	if payload.PhraseSet {
		keywordUpdates["keyword"] = payload.Phrase
		filterUpdates["phrase"] = payload.Phrase
	}
	if payload.WholeWord != nil {
		keywordUpdates["whole_word"] = *payload.WholeWord
	}
	if payload.ContextSet {
		contexts := normalizeFilterContexts(payload.Context)
		if len(contexts) == 0 || !validFilterContexts(contexts) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter context is invalid")
		}
		filterUpdates["context"] = models.StringArray(contexts)
	}
	if payload.Irreversible != nil {
		if *payload.Irreversible {
			filterUpdates["action"] = 1
		} else {
			filterUpdates["action"] = 0
		}
	}
	if payload.ExpiresInSet {
		filterUpdates["expires_at"] = expiresAtFromSeconds(payload.ExpiresIn, now)
	}
	if v1FilterParentParamsChanged(keyword.CustomFilter, payload, now) {
		if err := s.ensureSingleKeywordFilter(keyword.CustomFilterID); err != nil {
			return err
		}
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.CustomFilterKeyword{}).Where("id = ?", keyword.ID).Updates(keywordUpdates).Error; err != nil {
			return err
		}
		return tx.Model(&models.CustomFilter{}).Where("id = ?", keyword.CustomFilterID).Updates(filterUpdates).Error
	}); err != nil {
		return err
	}
	updated, err := s.findFilterKeyword(c, c.Param("id"), "write", "write:filters")
	if err != nil {
		return err
	}
	if keyword.CustomFilter.AccountID.Valid {
		s.invalidateFilterCacheAndBroadcast(keyword.CustomFilter.AccountID.Int64)
	}
	return c.JSON(http.StatusOK, serializer.V1FilterFromKeyword(*updated))
}

func (s *Server) deleteV1Filter(c *echo.Context) error {
	return s.deleteFilterKeyword(c)
}

func (s *Server) filters(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:filters")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	var filters []models.CustomFilter
	if err := s.db.Preload("Keywords").Preload("Statuses").Where("account_id = ?", account.ID).Order("id ASC").Find(&filters).Error; err != nil {
		return err
	}
	out := make([]serializer.Filter, 0, len(filters))
	for _, filter := range filters {
		out = append(out, serializer.FilterFromModel(filter, true))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showFilter(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:filters")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	filter, err := s.findFilter(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.FilterFromModel(*filter, true))
}

func (s *Server) createFilter(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:filters")
	if err != nil {
		return err
	}
	payload, err := parseFilterPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	payload.Context = normalizeFilterContexts(payload.Context)
	if !validFilterTitle(payload.Title) || len(payload.Context) == 0 || !validFilterContexts(payload.Context) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter is invalid")
	}
	action, ok := filterActionValue(firstNonEmpty(payload.FilterAction, "warn"))
	if !ok {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter action is invalid")
	}

	now := time.Now().UTC()
	filter := models.CustomFilter{
		AccountID: models.CustomFilterAccountID(account.ID),
		Phrase:    payload.Title,
		Context:   models.StringArray(payload.Context),
		Action:    action,
		ExpiresAt: expiresAtFromSeconds(payload.ExpiresIn, now),
		CreatedAt: now,
		UpdatedAt: now,
		Keywords:  []models.CustomFilterKeyword{},
		Statuses:  []models.CustomFilterStatus{},
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&filter).Error; err != nil {
			return err
		}
		return applyFilterKeywordAttributes(tx, filter.ID, payload.KeywordsAttributes, now)
	}); err != nil {
		return err
	}

	created, err := s.findFilter(account.ID, strconv.FormatInt(filter.ID, 10))
	if err != nil {
		return err
	}
	s.invalidateFilterCacheAndBroadcast(account.ID)
	return c.JSON(http.StatusOK, serializer.FilterFromModel(*created, true))
}

func (s *Server) updateFilter(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:filters")
	if err != nil {
		return err
	}
	filter, err := s.findFilter(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	payload, err := parseFilterPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if payload.TitleSet {
		if strings.TrimSpace(payload.Title) == "" {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Title can't be blank")
		}
		if !validFilterTitle(payload.Title) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Title is invalid")
		}
		updates["phrase"] = payload.Title
	}
	if payload.ContextSet {
		contexts := normalizeFilterContexts(payload.Context)
		if len(contexts) == 0 || !validFilterContexts(contexts) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter context is invalid")
		}
		updates["context"] = models.StringArray(contexts)
	}
	if payload.FilterAction != "" {
		action, ok := filterActionValue(payload.FilterAction)
		if !ok {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Filter action is invalid")
		}
		updates["action"] = action
	}
	if payload.ExpiresInSet {
		updates["expires_at"] = expiresAtFromSeconds(payload.ExpiresIn, time.Now().UTC())
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.CustomFilter{}).Where("id = ? AND account_id = ?", filter.ID, account.ID).Updates(updates).Error; err != nil {
			return err
		}
		return applyFilterKeywordAttributes(tx, filter.ID, payload.KeywordsAttributes, time.Now().UTC())
	}); err != nil {
		return err
	}
	updated, err := s.findFilter(account.ID, c.Param("id"))
	if err != nil {
		return err
	}
	s.invalidateFilterCacheAndBroadcast(account.ID)
	return c.JSON(http.StatusOK, serializer.FilterFromModel(*updated, true))
}

func (s *Server) deleteFilter(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:filters")
	if err != nil {
		return err
	}
	filter, err := s.findFilter(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("custom_filter_id = ?", filter.ID).Delete(&models.CustomFilterKeyword{}).Error; err != nil {
			return err
		}
		if err := tx.Where("custom_filter_id = ?", filter.ID).Delete(&models.CustomFilterStatus{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.CustomFilter{}, "id = ? AND account_id = ?", filter.ID, account.ID).Error
	}); err != nil {
		return err
	}
	s.invalidateFilterCacheAndBroadcast(account.ID)
	return renderEmpty(c)
}

func (s *Server) filterKeywords(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:filters")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	filter, err := s.findFilter(account.ID, c.Param("filter_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.FilterKeywordsFromModel(filter.Keywords))
}

func (s *Server) showFilterKeyword(c *echo.Context) error {
	keyword, err := s.findFilterKeyword(c, c.Param("id"), "read", "read:filters")
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	c.Response().Header().Set("Vary", "Authorization")
	return c.JSON(http.StatusOK, serializer.FilterKeywordFromModel(*keyword))
}

func (s *Server) createFilterKeyword(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:filters")
	if err != nil {
		return err
	}
	filter, err := s.findFilter(account.ID, c.Param("filter_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	payload, err := parseFilterKeywordPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if !validFilterKeyword(payload.Keyword) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Keyword is invalid")
	}
	wholeWord := true
	if payload.WholeWord != nil {
		wholeWord = *payload.WholeWord
	}
	now := time.Now().UTC()
	keyword := models.CustomFilterKeyword{CustomFilterID: filter.ID, Keyword: payload.Keyword, WholeWord: wholeWord, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&keyword).Error; err != nil {
		return err
	}
	if filter.AccountID.Valid {
		s.invalidateFilterCacheAndBroadcast(filter.AccountID.Int64)
	}
	return c.JSON(http.StatusOK, serializer.FilterKeywordFromModel(keyword))
}

func (s *Server) updateFilterKeyword(c *echo.Context) error {
	keyword, err := s.findFilterKeyword(c, c.Param("id"), "write", "write:filters")
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	accountID := keyword.CustomFilter.AccountID
	payload, err := parseFilterKeywordPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if payload.KeywordSet && strings.TrimSpace(payload.Keyword) == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Keyword can't be blank")
	}
	if payload.KeywordSet && !validFilterKeyword(payload.Keyword) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Keyword is invalid")
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if payload.KeywordSet {
		updates["keyword"] = payload.Keyword
	}
	if payload.WholeWord != nil {
		updates["whole_word"] = *payload.WholeWord
	}
	if err := s.db.Model(&models.CustomFilterKeyword{}).Where("id = ?", keyword.ID).Updates(updates).Error; err != nil {
		return err
	}
	if err := s.db.First(keyword, keyword.ID).Error; err != nil {
		return err
	}
	if accountID.Valid {
		s.invalidateFilterCacheAndBroadcast(accountID.Int64)
	}
	return c.JSON(http.StatusOK, serializer.FilterKeywordFromModel(*keyword))
}

func (s *Server) deleteFilterKeyword(c *echo.Context) error {
	keyword, err := s.findFilterKeyword(c, c.Param("id"), "write", "write:filters")
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	accountID := keyword.CustomFilter.AccountID
	if err := s.db.Delete(&models.CustomFilterKeyword{}, "id = ?", keyword.ID).Error; err != nil {
		return err
	}
	if accountID.Valid {
		s.invalidateFilterCacheAndBroadcast(accountID.Int64)
	}
	return renderEmpty(c)
}

func (s *Server) filterStatuses(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:filters")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	filter, err := s.findFilter(account.ID, c.Param("filter_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.FilterStatusesFromModel(filter.Statuses))
}

func (s *Server) showFilterStatus(c *echo.Context) error {
	status, err := s.findFilterStatus(c, c.Param("id"), "read", "read:filters")
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	c.Response().Header().Set("Vary", "Authorization")
	return c.JSON(http.StatusOK, serializer.FilterStatusFromModel(*status))
}

func (s *Server) createFilterStatus(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:filters")
	if err != nil {
		return err
	}
	filter, err := s.findFilter(account.ID, c.Param("filter_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var payload filterStatusPayload
	_ = c.Bind(&payload)
	if payload.StatusID == "" {
		payload.StatusID = c.FormValue("status_id")
	}
	statusID, err := strconv.ParseInt(strings.TrimSpace(payload.StatusID), 10, 64)
	if err != nil || statusID <= 0 {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Status is invalid")
	}
	if _, err := s.findVisibleStatusForAccount(account, strconv.FormatInt(statusID, 10)); err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Status is invalid")
	}
	var existing int64
	if err := s.db.Model(&models.CustomFilterStatus{}).Where("custom_filter_id = ? AND status_id = ?", filter.ID, statusID).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Status has already been taken")
	}
	now := time.Now().UTC()
	filterStatus := models.CustomFilterStatus{CustomFilterID: filter.ID, StatusID: statusID, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&filterStatus).Error; err != nil {
		return err
	}
	if filter.AccountID.Valid {
		s.invalidateFilterCacheAndBroadcast(filter.AccountID.Int64)
	}
	return c.JSON(http.StatusOK, serializer.FilterStatusFromModel(filterStatus))
}

func (s *Server) deleteFilterStatus(c *echo.Context) error {
	status, err := s.findFilterStatus(c, c.Param("id"), "write", "write:filters")
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Delete(&models.CustomFilterStatus{}, "id = ?", status.ID).Error; err != nil {
		return err
	}
	if status.CustomFilter.AccountID.Valid {
		s.invalidateFilterCacheAndBroadcast(status.CustomFilter.AccountID.Int64)
	}
	return renderEmpty(c)
}

func (s *Server) findFilter(accountID int64, id string) (*models.CustomFilter, error) {
	var filter models.CustomFilter
	err := s.db.Preload("Keywords").Preload("Statuses").Where("id = ? AND account_id = ?", id, accountID).First(&filter).Error
	return &filter, err
}

func (s *Server) findFilterKeyword(c *echo.Context, id string, scopes ...string) (*models.CustomFilterKeyword, error) {
	account, _, err := s.requireAccountScope(c, scopes...)
	if err != nil {
		return nil, err
	}
	var keyword models.CustomFilterKeyword
	err = s.db.Preload("CustomFilter").
		Joins("JOIN custom_filters ON custom_filters.id = custom_filter_keywords.custom_filter_id").
		Where("custom_filter_keywords.id = ? AND custom_filters.account_id = ?", id, account.ID).
		First(&keyword).Error
	return &keyword, err
}

func (s *Server) ensureSingleKeywordFilter(filterID int64) error {
	var count int64
	if err := s.db.Model(&models.CustomFilterKeyword{}).Where("custom_filter_id = ?", filterID).Count(&count).Error; err != nil {
		return err
	}
	if count > 1 {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: These parameters cannot be changed from this application because they apply to more than one filter keyword. Use a more recent application or the web interface."}
	}
	return nil
}

func v1FilterParentParamsChanged(filter models.CustomFilter, payload v1FilterPayload, now time.Time) bool {
	if payload.PhraseSet && payload.Phrase != filter.Phrase {
		return true
	}
	if payload.ContextSet && !stringSlicesEqual(normalizeFilterContexts(payload.Context), filter.Context) {
		return true
	}
	if payload.Irreversible != nil {
		action := 0
		if *payload.Irreversible {
			action = 1
		}
		if action != filter.Action {
			return true
		}
	}
	if payload.ExpiresInSet {
		next := expiresAtFromSeconds(payload.ExpiresIn, now)
		if next.Valid != filter.ExpiresAt.Valid {
			return true
		}
		if next.Valid && !next.Time.Equal(filter.ExpiresAt.Time) {
			return true
		}
	}
	return false
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (s *Server) findFilterStatus(c *echo.Context, id string, scopes ...string) (*models.CustomFilterStatus, error) {
	account, _, err := s.requireAccountScope(c, scopes...)
	if err != nil {
		return nil, err
	}
	var status models.CustomFilterStatus
	err = s.db.Preload("CustomFilter").
		Joins("JOIN custom_filters ON custom_filters.id = custom_filter_statuses.custom_filter_id").
		Where("custom_filter_statuses.id = ? AND custom_filters.account_id = ?", id, account.ID).
		First(&status).Error
	return &status, err
}

func parseFilterPayload(c *echo.Context) (filterPayload, error) {
	var payload filterPayload
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		decoder := json.NewDecoder(c.Request().Body)
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["title"]; ok {
			payload.TitleSet = true
			payload.Title = rawString(value)
		}
		payload.FilterAction = rawString(raw["filter_action"])
		if value, ok := raw["context"]; ok {
			if contexts, ok := rawStringArray(value); ok {
				payload.ContextSet = true
				payload.Context = contexts
			}
		}
		if value, ok := raw["expires_in"]; ok {
			payload.ExpiresInSet = true
			if seconds, present := railsExpiresInSeconds(value); present {
				payload.ExpiresIn = &seconds
			}
		}
		payload.KeywordsAttributes = filterKeywordAttributesFromRaw(raw["keywords_attributes"])
		return payload, nil
	}
	if values, err := c.FormValues(); err == nil {
		if values.Has("title") {
			payload.TitleSet = true
			payload.Title = values.Get("title")
		}
		payload.FilterAction = values.Get("filter_action")
		if values.Has("context[]") {
			payload.ContextSet = true
			payload.Context = append(payload.Context, values["context[]"]...)
		}
		payload.KeywordsAttributes = filterKeywordAttributesFromForm(values)
		if expires, ok := filterExpiresInFromForm(values, "expires_in"); ok && !payload.ExpiresInSet {
			payload.ExpiresInSet = true
			payload.ExpiresIn = expires
		}
	}
	return payload, nil
}

func filterExpiresInFromForm(values map[string][]string, key string) (*int64, bool) {
	if _, ok := values[key]; !ok {
		return nil, false
	}
	expires := strings.TrimSpace(lastFormValue(values, key))
	if expires == "" {
		return nil, true
	}
	seconds := railsToInt64(expires)
	return &seconds, true
}

func railsExpiresInSeconds(value any) (int64, bool) {
	switch v := value.(type) {
	case nil:
		return 0, false
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false
		}
		return railsToInt64(v), true
	case json.Number:
		return railsToInt64(v.String()), true
	case float64:
		return int64(v), true
	default:
		text := rawString(value)
		if strings.TrimSpace(text) == "" {
			return 0, false
		}
		return railsToInt64(text), true
	}
}

func filterKeywordAttributesFromForm(values map[string][]string) []filterKeywordPayload {
	byIndex := map[string]*filterKeywordPayload{}
	for key := range values {
		index, field, ok := filterKeywordAttributeFormPath(key)
		if !ok {
			continue
		}
		attr := byIndex[index]
		if attr == nil {
			attr = &filterKeywordPayload{}
			byIndex[index] = attr
		}
		value := lastFormValue(values, key)
		switch field {
		case "id":
			attr.ID = value
		case "keyword":
			attr.Keyword = value
			attr.KeywordSet = true
		case "whole_word":
			wholeWord := truthy(value)
			attr.WholeWord = &wholeWord
		case "_destroy":
			attr.Destroy = truthy(value)
		}
	}
	indexes := make([]string, 0, len(byIndex))
	for index := range byIndex {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool {
		left, leftErr := strconv.Atoi(indexes[i])
		right, rightErr := strconv.Atoi(indexes[j])
		if leftErr == nil && rightErr == nil {
			return left < right
		}
		return indexes[i] < indexes[j]
	})
	out := make([]filterKeywordPayload, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, *byIndex[index])
	}
	return out
}

func filterKeywordAttributeFormPath(key string) (string, string, bool) {
	const prefix = "keywords_attributes["
	if !strings.HasPrefix(key, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(key, prefix)
	end := strings.Index(rest, "]")
	if end < 0 {
		return "", "", false
	}
	index := rest[:end]
	rest = rest[end+1:]
	if index == "" || !strings.HasPrefix(rest, "[") || !strings.HasSuffix(rest, "]") {
		return "", "", false
	}
	field := strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]")
	switch field {
	case "id", "keyword", "whole_word", "_destroy":
		return index, field, true
	default:
		return "", "", false
	}
}

func filterKeywordAttributesFromRaw(value any) []filterKeywordPayload {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]filterKeywordPayload, 0, len(items))
	for _, item := range items {
		values, ok := item.(map[string]any)
		if !ok {
			continue
		}
		attr := filterKeywordPayload{
			ID:      rawString(values["id"]),
			Keyword: rawString(values["keyword"]),
			Destroy: railsBool(values["_destroy"], false),
		}
		if _, ok := values["keyword"]; ok {
			attr.KeywordSet = true
		}
		if value, ok := values["whole_word"]; ok {
			wholeWord := railsBool(value, false)
			attr.WholeWord = &wholeWord
		}
		out = append(out, attr)
	}
	return out
}

func rawStringArray(value any) ([]string, bool) {
	switch v := value.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text := rawString(item); text != "" {
				out = append(out, text)
			}
		}
		return out, true
	case []string:
		return v, true
	default:
		return nil, false
	}
}

func parseV1FilterPayload(c *echo.Context) (v1FilterPayload, error) {
	var payload v1FilterPayload
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		decoder := json.NewDecoder(c.Request().Body)
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["phrase"]; ok {
			payload.PhraseSet = true
			payload.Phrase = rawString(value)
		}
		if value, ok := raw["context"]; ok {
			if contexts, ok := rawStringArray(value); ok {
				payload.ContextSet = true
				payload.Context = contexts
			}
		}
		if value, ok := raw["expires_in"]; ok {
			payload.ExpiresInSet = true
			if seconds, present := railsExpiresInSeconds(value); present {
				payload.ExpiresIn = &seconds
			}
		}
		if value, ok := raw["irreversible"]; ok {
			irreversible := railsBool(value, false)
			payload.Irreversible = &irreversible
		}
		if value, ok := raw["whole_word"]; ok {
			wholeWord := railsBool(value, false)
			payload.WholeWord = &wholeWord
		}
		return payload, nil
	}
	if values, err := c.FormValues(); err == nil {
		if values.Has("phrase") {
			payload.PhraseSet = true
			payload.Phrase = values.Get("phrase")
		}
		if values.Has("context[]") {
			payload.ContextSet = true
			payload.Context = append(payload.Context, values["context[]"]...)
		}
		if expires, ok := filterExpiresInFromForm(values, "expires_in"); ok && !payload.ExpiresInSet {
			payload.ExpiresInSet = true
			payload.ExpiresIn = expires
		}
		if values.Has("irreversible") && payload.Irreversible == nil {
			irreversible := truthy(values.Get("irreversible"))
			payload.Irreversible = &irreversible
		}
		if values.Has("whole_word") && payload.WholeWord == nil {
			wholeWord := truthy(values.Get("whole_word"))
			payload.WholeWord = &wholeWord
		}
	}
	return payload, nil
}

func parseFilterKeywordPayload(c *echo.Context) (filterKeywordPayload, error) {
	var payload filterKeywordPayload
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		decoder := json.NewDecoder(c.Request().Body)
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["keyword"]; ok {
			payload.KeywordSet = true
			payload.Keyword = rawString(value)
		}
		if value, ok := raw["whole_word"]; ok {
			wholeWord := railsBool(value, false)
			payload.WholeWord = &wholeWord
		}
		return payload, nil
	}
	if values, err := c.FormValues(); err == nil {
		if values.Has("keyword") {
			payload.KeywordSet = true
			payload.Keyword = values.Get("keyword")
		}
		if values.Has("whole_word") {
			wholeWord := truthy(values.Get("whole_word"))
			payload.WholeWord = &wholeWord
		}
	}
	return payload, nil
}

func applyFilterKeywordAttributes(tx *gorm.DB, filterID int64, attributes []filterKeywordPayload, now time.Time) error {
	for _, attr := range attributes {
		if attr.ID != "" {
			var keyword models.CustomFilterKeyword
			err := tx.Where("id = ? AND custom_filter_id = ?", attr.ID, filterID).First(&keyword).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apiHTTPError{status: http.StatusNotFound, message: "Record not found"}
			}
			if err != nil {
				return err
			}
			if attr.Destroy {
				if err := tx.Delete(&models.CustomFilterKeyword{}, "id = ?", keyword.ID).Error; err != nil {
					return err
				}
				continue
			}
			if attr.KeywordSet && strings.TrimSpace(attr.Keyword) == "" {
				return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Keyword can't be blank"}
			}
			if attr.KeywordSet && !validFilterKeyword(attr.Keyword) {
				return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Keyword is invalid"}
			}
			updates := map[string]any{"updated_at": now}
			if attr.KeywordSet {
				updates["keyword"] = attr.Keyword
			}
			if attr.WholeWord != nil {
				updates["whole_word"] = *attr.WholeWord
			}
			if err := tx.Model(&models.CustomFilterKeyword{}).Where("id = ?", keyword.ID).Updates(updates).Error; err != nil {
				return err
			}
			continue
		}
		if strings.TrimSpace(attr.Keyword) == "" || attr.Destroy {
			continue
		}
		if !validFilterKeyword(attr.Keyword) {
			return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Keyword is invalid"}
		}
		wholeWord := true
		if attr.WholeWord != nil {
			wholeWord = *attr.WholeWord
		}
		keyword := models.CustomFilterKeyword{CustomFilterID: filterID, Keyword: attr.Keyword, WholeWord: wholeWord, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&keyword).Error; err != nil {
			return err
		}
	}
	return nil
}

func validFilterTitle(title string) bool {
	return strings.TrimSpace(title) != "" && utf8.RuneCountInString(title) <= 256
}

func validFilterKeyword(keyword string) bool {
	return strings.TrimSpace(keyword) != "" && utf8.RuneCountInString(keyword) <= 512
}

func normalizeFilterContexts(contexts []string) []string {
	out := make([]string, 0, len(contexts))
	for _, context := range contexts {
		context = strings.TrimSpace(context)
		if context == "" {
			continue
		}
		out = append(out, context)
	}
	return out
}

func validFilterContexts(contexts []string) bool {
	valid := map[string]struct{}{"home": {}, "notifications": {}, "public": {}, "thread": {}, "account": {}}
	for _, context := range contexts {
		if _, ok := valid[context]; !ok {
			return false
		}
	}
	return true
}

func filterActionValue(value string) (int, bool) {
	switch strings.TrimSpace(value) {
	case "hide", "1":
		return 1, true
	case "blur", "2":
		return 2, true
	case "warn", "0", "":
		return 0, true
	default:
		return 0, false
	}
}

func expiresAtFromSeconds(seconds *int64, now time.Time) sql.NullTime {
	if seconds == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: now.Add(time.Duration(*seconds) * time.Second), Valid: true}
}
