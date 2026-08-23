package api

import (
	"bytes"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type streamingFilterResult struct {
	Filter         streamingFilter `json:"filter"`
	KeywordMatches []string        `json:"keyword_matches"`
	StatusMatches  any             `json:"status_matches"`
}

type streamingFilter struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Context      []string `json:"context"`
	ExpiresAt    *string  `json:"expires_at"`
	FilterAction string   `json:"filter_action"`
	Keywords     []any    `json:"keywords"`
	Statuses     []any    `json:"statuses"`
	regexp       *regexp.Regexp
	statusIDs    []int64
}

func (s *Server) filterStreamingMessage(session streamingSession, message redisMessage, filterContext string) (redisMessage, bool) {
	if message.Event != "update" && message.Event != "status.update" {
		return message, true
	}

	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		return message, true
	}
	if local, known := streamingPayloadIsLocal(payload); known && ((local && session.FilterLocal) || (!local && session.FilterRemote)) {
		return message, false
	}
	if !streamingLanguageAllowed(session.ChosenLanguages, payload) {
		return message, false
	}
	if session.Account == nil || s.db == nil {
		return message, true
	}
	if s.streamingStatusBlocked(session.Account.ID, payload) {
		return message, false
	}
	if _, ok := payload["filtered"]; ok {
		return message, true
	}

	results := s.streamingFilterResults(session.Account.ID, payload, filterContext)
	if results == nil {
		return message, true
	}
	payload["filtered"] = results
	encoded := encodeStreamingPayload(payload)
	if len(encoded) == 0 {
		return message, true
	}
	message.Payload = encoded
	return message, true
}

func streamingPayloadIsLocal(payload map[string]any) (bool, bool) {
	account, ok := payload["account"].(map[string]any)
	if !ok {
		return false, false
	}
	username, usernameOK := account["username"].(string)
	acct, acctOK := account["acct"].(string)
	if !usernameOK || !acctOK {
		return false, false
	}
	return username == acct, true
}

func streamingLanguageAllowed(chosen []string, payload map[string]any) bool {
	if len(chosen) == 0 {
		return true
	}
	language, _ := payload["language"].(string)
	for _, allowed := range chosen {
		if allowed == language {
			return true
		}
	}
	return false
}

func (s *Server) streamingStatusBlocked(accountID int64, payload map[string]any) bool {
	targetIDs := streamingTargetAccountIDs(payload)
	if len(targetIDs) == 0 {
		return false
	}
	statusAccountID := targetIDs[0]

	var count int64
	if err := s.db.Table("blocks").
		Where("(account_id = ? AND target_account_id IN ?) OR (account_id = ? AND target_account_id = ?)", accountID, targetIDs, statusAccountID, accountID).
		Count(&count).Error; err == nil && count > 0 {
		return true
	}
	if err := s.db.Table("mutes").Where("account_id = ? AND target_account_id IN ?", accountID, targetIDs).Count(&count).Error; err == nil && count > 0 {
		return true
	}

	if domain := streamingStatusAccountDomain(payload); domain != "" {
		if err := s.db.Table("account_domain_blocks").Where("account_id = ? AND lower(domain) = lower(?)", accountID, domain).Count(&count).Error; err == nil && count > 0 {
			return true
		}
	}
	return false
}

func (s *Server) streamingFilterResults(accountID int64, payload map[string]any, filterContext string) []streamingFilterResult {
	return streamingFilterResultsFromFilters(payload, s.streamingFilters(accountID), filterContext)
}

func streamingFilterResultsFromFilters(payload map[string]any, filters []streamingFilter, filterContext string) []streamingFilterResult {
	text := streamingSearchableText(payload)
	statusIDs := streamingPayloadStatusIDs(payload)
	results := make([]streamingFilterResult, 0)
	for _, filter := range filters {
		if !streamingFilterAppliesToContext(filter, filterContext) {
			continue
		}
		keywordMatches := []string(nil)
		if filter.regexp != nil && text != "" {
			keywordMatches = uniqueStrings(filter.regexp.FindAllString(text, -1))
		}
		statusMatches := matchingStatusIDs(statusIDs, filter.statusIDs)
		if len(keywordMatches) == 0 && len(statusMatches) == 0 {
			continue
		}
		filter.regexp = nil
		filter.statusIDs = nil
		results = append(results, streamingFilterResult{Filter: filter, KeywordMatches: keywordMatches, StatusMatches: statusMatches})
	}
	return results
}

func streamingFilterAppliesToContext(filter streamingFilter, filterContext string) bool {
	if filterContext == "" {
		return true
	}
	for _, context := range filter.Context {
		if context == filterContext {
			return true
		}
	}
	return false
}

func (s *Server) streamingFilters(accountID int64) []streamingFilter {
	grouped := map[int64]*streamingFilter{}
	patterns := map[int64][]string{}

	var keywordRows []struct {
		ID           int64              `gorm:"column:id"`
		Title        string             `gorm:"column:title"`
		Context      models.StringArray `gorm:"column:context"`
		ExpiresAt    *time.Time         `gorm:"column:expires_at"`
		FilterAction int                `gorm:"column:filter_action"`
		Keyword      string             `gorm:"column:keyword"`
		WholeWord    bool               `gorm:"column:whole_word"`
	}
	if err := s.db.Table("custom_filter_keywords keyword").
		Select("filter.id AS id, filter.phrase AS title, filter.context AS context, filter.expires_at AS expires_at, filter.action AS filter_action, keyword.keyword AS keyword, keyword.whole_word AS whole_word").
		Joins("JOIN custom_filters filter ON filter.id = keyword.custom_filter_id").
		Where("filter.account_id = ? AND (filter.expires_at IS NULL OR filter.expires_at > NOW())", accountID).
		Find(&keywordRows).Error; err != nil {
		return nil
	}
	for _, row := range keywordRows {
		streamingFilterFromRow(grouped, row.ID, row.Title, row.Context, row.ExpiresAt, row.FilterAction)
		patterns[row.ID] = append(patterns[row.ID], keywordPattern(row.Keyword, row.WholeWord))
	}

	var statusRows []struct {
		ID           int64              `gorm:"column:id"`
		Title        string             `gorm:"column:title"`
		Context      models.StringArray `gorm:"column:context"`
		ExpiresAt    *time.Time         `gorm:"column:expires_at"`
		FilterAction int                `gorm:"column:filter_action"`
		FilterStatus int64              `gorm:"column:filter_status_id"`
		StatusID     int64              `gorm:"column:status_id"`
	}
	if err := s.db.Table("custom_filter_statuses status").
		Select("filter.id AS id, filter.phrase AS title, filter.context AS context, filter.expires_at AS expires_at, filter.action AS filter_action, status.id AS filter_status_id, status.status_id AS status_id").
		Joins("JOIN custom_filters filter ON filter.id = status.custom_filter_id").
		Where("filter.account_id = ? AND (filter.expires_at IS NULL OR filter.expires_at > NOW())", accountID).
		Find(&statusRows).Error; err != nil {
		return nil
	}
	for _, row := range statusRows {
		filter := streamingFilterFromRow(grouped, row.ID, row.Title, row.Context, row.ExpiresAt, row.FilterAction)
		filter.statusIDs = append(filter.statusIDs, row.StatusID)
		filter.Statuses = append(filter.Statuses, map[string]string{
			"id":        strconv.FormatInt(row.FilterStatus, 10),
			"status_id": strconv.FormatInt(row.StatusID, 10),
		})
	}

	out := make([]streamingFilter, 0, len(grouped))
	for id, filter := range grouped {
		if len(patterns[id]) > 0 {
			regex, err := regexp.Compile("(?i)" + strings.Join(patterns[id], "|"))
			if err == nil {
				filter.regexp = regex
			}
		}
		filter.statusIDs = uniqueInt64s(filter.statusIDs)
		out = append(out, *filter)
	}
	return out
}

func streamingFilterFromRow(grouped map[int64]*streamingFilter, id int64, title string, context models.StringArray, expiresAt *time.Time, action int) *streamingFilter {
	filter := grouped[id]
	if filter != nil {
		return filter
	}
	filter = &streamingFilter{
		ID:           strconv.FormatInt(id, 10),
		Title:        title,
		Context:      []string(context),
		ExpiresAt:    timeStringPtr(expiresAt),
		FilterAction: streamingFilterActionName(action),
		Keywords:     []any{},
		Statuses:     []any{},
	}
	grouped[id] = filter
	return filter
}

func streamingPayloadStatusIDs(payload map[string]any) []int64 {
	ids := make([]int64, 0, 2)
	if id, ok := anyID(payload["id"]); ok {
		ids = append(ids, id)
	}
	if reblog, ok := payload["reblog"].(map[string]any); ok {
		if id, ok := anyID(reblog["id"]); ok {
			ids = append(ids, id)
		}
	}
	return uniqueInt64s(ids)
}

func matchingStatusIDs(statusIDs []int64, filterStatusIDs []int64) []string {
	if len(statusIDs) == 0 || len(filterStatusIDs) == 0 {
		return nil
	}
	filterIDs := make(map[int64]bool, len(filterStatusIDs))
	for _, id := range filterStatusIDs {
		filterIDs[id] = true
	}
	out := make([]string, 0)
	for _, id := range statusIDs {
		if filterIDs[id] {
			out = append(out, strconv.FormatInt(id, 10))
		}
	}
	return out
}

func streamingTargetAccountIDs(payload map[string]any) []int64 {
	ids := make([]int64, 0)
	if account, ok := payload["account"].(map[string]any); ok {
		if id, ok := anyID(account["id"]); ok {
			ids = append(ids, id)
		}
	}
	if mentions, ok := payload["mentions"].([]any); ok {
		for _, mention := range mentions {
			if item, ok := mention.(map[string]any); ok {
				if id, ok := anyID(item["id"]); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	return uniqueInt64s(ids)
}

func streamingStatusAccountDomain(payload map[string]any) string {
	account, ok := payload["account"].(map[string]any)
	if !ok {
		return ""
	}
	acct, _ := account["acct"].(string)
	_, domain, ok := strings.Cut(acct, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(domain)
}

func streamingSearchableText(payload map[string]any) string {
	if reblog, ok := payload["reblog"].(map[string]any); ok {
		payload = reblog
	}
	parts := []string{stringField(payload, "spoiler_text"), stringField(payload, "content"), stringField(payload, "text")}
	if poll, ok := payload["poll"].(map[string]any); ok {
		if options, ok := poll["options"].([]any); ok {
			for _, option := range options {
				if item, ok := option.(map[string]any); ok {
					parts = append(parts, stringField(item, "title"))
				}
			}
		}
	}
	if attachments, ok := payload["media_attachments"].([]any); ok {
		for _, attachment := range attachments {
			if item, ok := attachment.(map[string]any); ok {
				parts = append(parts, stringField(item, "description"))
			}
		}
	}
	return strings.TrimSpace(stripHTML(strings.Join(parts, "\n\n")))
}

func stripHTML(value string) string {
	value = strings.ReplaceAll(value, "<br />", "\n")
	value = strings.ReplaceAll(value, "<br/>", "\n")
	value = strings.ReplaceAll(value, "<br>", "\n")
	value = strings.ReplaceAll(value, "</p><p>", "\n\n")
	re := regexp.MustCompile(`<[^>]+>`)
	return html.UnescapeString(re.ReplaceAllString(value, ""))
}

func keywordPattern(keyword string, wholeWord bool) string {
	escaped := regexp.QuoteMeta(keyword)
	if !wholeWord {
		return escaped
	}
	return `\b` + escaped + `\b`
}

func streamingFilterActionName(value int) string {
	switch value {
	case 1:
		return "hide"
	case 2:
		return "blur"
	default:
		return "warn"
	}
}

func anyID(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case string:
		id, err := strconv.ParseInt(v, 10, 64)
		return id, err == nil
	case json.Number:
		id, err := v.Int64()
		return id, err == nil
	default:
		return 0, false
	}
}

func stringField(value map[string]any, field string) string {
	if raw, ok := value[field].(string); ok {
		return raw
	}
	return ""
}

func timeStringPtr(value *time.Time) *string {
	if value == nil {
		return nil
	}
	text := value.UTC().Format(time.RFC3339Nano)
	return &text
}

func uniqueInt64s(values []int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func encodeStreamingPayload(payload map[string]any) json.RawMessage {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
	return bytes.TrimSpace(buf.Bytes())
}
