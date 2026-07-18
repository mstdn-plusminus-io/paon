package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type meiliSearchOptions struct {
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
	Filter string   `json:"filter,omitempty"`
	Sort   []string `json:"sort,omitempty"`
}

type meiliSearchRequest struct {
	Query string `json:"q"`
	meiliSearchOptions
}

type meiliSearchResponse struct {
	Hits []map[string]any `json:"hits"`
}

type meiliIndexSettings struct {
	SearchableAttributes []string `json:"searchableAttributes,omitempty"`
	FilterableAttributes []string `json:"filterableAttributes,omitempty"`
	SortableAttributes   []string `json:"sortableAttributes,omitempty"`
	RankingRules         []string `json:"rankingRules,omitempty"`
}

type meiliIndexDefinition struct {
	Index      string
	PrimaryKey string
	Settings   meiliIndexSettings
}

const (
	meiliHTTPTimeout         = 10 * time.Second
	maxMeiliResponseBodySize = 1 << 20
)

var meiliHTTPClient = &http.Client{Timeout: meiliHTTPTimeout}

func MeiliAvailable(ctx context.Context, cfg config.Config) error {
	if !cfg.MeiliEnabled || strings.TrimSpace(cfg.MeiliHost) == "" {
		return errMeiliDisabled
	}
	host := strings.TrimRight(cfg.MeiliHost, "/")
	endpoint, err := url.JoinPath(host, "health")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if cfg.MeiliMasterKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.MeiliMasterKey)
	}
	res, err := meiliHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("meilisearch health returned %s", res.Status)
	}
	return nil
}

func WaitForMeiliAvailable(ctx context.Context, cfg config.Config, timeout time.Duration) error {
	if !cfg.MeiliEnabled || strings.TrimSpace(cfg.MeiliHost) == "" {
		return errMeiliDisabled
	}
	if timeout <= 0 {
		return MeiliAvailable(ctx, cfg)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := MeiliAvailable(ctx, cfg); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) searchMeiliIDs(ctx context.Context, index string, query string, options meiliSearchOptions) ([]int64, error) {
	if !s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == "" {
		return nil, errMeiliDisabled
	}

	body, err := json.Marshal(meiliSearchRequest{Query: query, meiliSearchOptions: options})
	if err != nil {
		return nil, err
	}

	host := strings.TrimRight(s.cfg.MeiliHost, "/")
	endpoint, err := url.JoinPath(host, "indexes", s.cfg.MeiliPrefix+index, "search")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.MeiliMasterKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.MeiliMasterKey)
	}

	res, err := meiliHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("meilisearch %s returned %s", s.cfg.MeiliPrefix+index, res.Status)
	}

	var payload meiliSearchResponse
	if err := decodeMeiliJSONResponse(res, &payload); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(payload.Hits))
	for _, hit := range payload.Hits {
		id, ok := meiliHitID(hit["id"])
		if ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *Server) meiliWriteJSON(ctx context.Context, method string, index string, path string, payload any) error {
	if !s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == "" {
		return errMeiliDisabled
	}
	var body *bytes.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	} else {
		body = bytes.NewReader(nil)
	}
	host := strings.TrimRight(s.cfg.MeiliHost, "/")
	parts := append([]string{"indexes", s.cfg.MeiliPrefix + index}, strings.Split(strings.Trim(path, "/"), "/")...)
	endpoint, err := url.JoinPath(host, parts...)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cfg.MeiliMasterKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.MeiliMasterKey)
	}
	res, err := meiliHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("meilisearch %s returned %s", s.cfg.MeiliPrefix+index, res.Status)
	}
	return nil
}

func (s *Server) meiliCreateIndex(ctx context.Context, definition meiliIndexDefinition) error {
	if !s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == "" {
		return errMeiliDisabled
	}
	payload := map[string]string{
		"uid":        s.cfg.MeiliPrefix + definition.Index,
		"primaryKey": definition.PrimaryKey,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	host := strings.TrimRight(s.cfg.MeiliHost, "/")
	endpoint, err := url.JoinPath(host, "indexes")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.MeiliMasterKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.MeiliMasterKey)
	}
	res, err := meiliHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		return nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("meilisearch %s returned %s", s.cfg.MeiliPrefix+definition.Index, res.Status)
	}
	return nil
}

func (s *Server) meiliUpdateIndexSettings(ctx context.Context, definition meiliIndexDefinition) error {
	return s.meiliWriteJSON(ctx, http.MethodPatch, definition.Index, "settings", definition.Settings)
}

func (s *Server) syncMeiliIndexes(ctx context.Context) error {
	if !s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == "" {
		return errMeiliDisabled
	}
	for _, definition := range meiliIndexDefinitions() {
		if err := s.meiliCreateIndex(ctx, definition); err != nil {
			return err
		}
		if err := s.meiliUpdateIndexSettings(ctx, definition); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) syncMeiliIndexesBestEffort(ctx context.Context) {
	_ = s.syncMeiliIndexes(ctx)
}

func meiliIndexDefinitions() []meiliIndexDefinition {
	return []meiliIndexDefinition{
		{
			Index:      "accounts",
			PrimaryKey: "id",
			Settings: meiliIndexSettings{
				SearchableAttributes: []string{"username", "display_name", "text"},
				FilterableAttributes: []string{"domain", "bot", "locked", "discoverable", "indexable"},
				SortableAttributes:   []string{"followers_count", "following_count", "statuses_count", "last_status_at", "created_at_timestamp"},
				RankingRules:         []string{"words", "typo", "proximity", "attribute", "sort", "followers_count:desc", "statuses_count:desc", "last_status_at:desc"},
			},
		},
		{
			Index:      "statuses",
			PrimaryKey: "id",
			Settings: meiliIndexSettings{
				SearchableAttributes: []string{"text", "tags"},
				FilterableAttributes: []string{"id", "account_id", "in_reply_to_id", "language", "visibility", "sensitive", "has_media", "has_image", "has_video", "has_poll", "has_link", "has_embed", "is_reply", "searchable_by", "created_at_timestamp"},
				SortableAttributes:   []string{"created_at_timestamp", "favourites_count", "reblogs_count", "replies_count"},
				RankingRules:         []string{"words", "typo", "proximity", "attribute", "sort", "created_at_timestamp:desc", "favourites_count:desc", "reblogs_count:desc"},
			},
		},
		{
			Index:      "tags",
			PrimaryKey: "id",
			Settings: meiliIndexSettings{
				SearchableAttributes: []string{"name"},
				FilterableAttributes: []string{"reviewed", "trendable"},
				SortableAttributes:   []string{"usage", "accounts_count", "last_status_at"},
				RankingRules:         []string{"words", "typo", "proximity", "sort", "usage:desc", "accounts_count:desc", "last_status_at:desc"},
			},
		},
		{
			Index:      "instances",
			PrimaryKey: "id",
			Settings: meiliIndexSettings{
				SearchableAttributes: []string{"domain"},
				SortableAttributes:   []string{"accounts_count"},
				RankingRules:         []string{"words", "typo", "proximity", "sort", "accounts_count:desc"},
			},
		},
	}
}

func (s *Server) meiliUpsertDocuments(ctx context.Context, index string, documents any) error {
	return s.meiliWriteJSON(ctx, http.MethodPost, index, "documents", documents)
}

func (s *Server) meiliDeleteDocument(ctx context.Context, index string, id int64) error {
	return s.meiliWriteJSON(ctx, http.MethodDelete, index, "documents/"+strconv.FormatInt(id, 10), nil)
}

func (s *Server) meiliDeleteDocumentByID(ctx context.Context, index string, id string) error {
	return s.meiliWriteJSON(ctx, http.MethodDelete, index, "documents/"+id, nil)
}

func (s *Server) meiliDeleteAllDocuments(ctx context.Context, index string) error {
	return s.meiliWriteJSON(ctx, http.MethodDelete, index, "documents", nil)
}

var errMeiliDisabled = fmt.Errorf("meilisearch disabled")

func (s *Server) searchMeiliAccountIDs(ctx context.Context, query string, current *models.Account, following bool, limitValue int, offsetValue int) ([]int64, error) {
	options := meiliSearchOptions{
		Limit:  limitValue,
		Offset: offsetValue,
		Sort:   []string{"followers_count:desc", "statuses_count:desc"},
	}
	if following && current != nil {
		ids, err := s.followingAccountIDs(current.ID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, current.ID)
		if len(ids) == 0 {
			return []int64{}, nil
		}
		options.Filter = "id IN [" + joinInt64s(ids) + "]"
	}
	return s.searchMeiliIDs(ctx, "accounts", query, options)
}

func (s *Server) searchMeiliStatusIDs(ctx context.Context, query string, current *models.Account, accountID string, minID string, maxID string, limitValue int, offsetValue int) ([]int64, error) {
	filters := []string{meiliStatusVisibilityFilter(current)}
	transformedQuery, queryFilters, err := s.meiliStatusQueryFilters(ctx, query, current)
	if err != nil {
		return nil, err
	}
	filters = append(filters, queryFilters...)
	if accountID != "" {
		id, err := strconv.ParseInt(accountID, 10, 64)
		if err != nil {
			return nil, err
		}
		filters = append(filters, fmt.Sprintf("account_id = %d", id))
	}
	if minID != "" {
		id := railsToInt64(minID)
		filters = append(filters, fmt.Sprintf("created_at_timestamp >= %d", mastodonSnowflakeUnixSeconds(id)))
	}
	if maxID != "" {
		id := railsToInt64(maxID)
		filters = append(filters, fmt.Sprintf("created_at_timestamp <= %d", mastodonSnowflakeUnixSeconds(id)))
	}
	return s.searchMeiliIDs(ctx, "statuses", transformedQuery, meiliSearchOptions{
		Limit:  limitValue,
		Offset: offsetValue,
		Filter: strings.Join(filters, " AND "),
		Sort:   []string{"created_at_timestamp:desc"},
	})
}

func (s *Server) meiliStatusQueryFilters(ctx context.Context, query string, current *models.Account) (string, []string, error) {
	terms := strings.Fields(strings.TrimSpace(query))
	out := make([]string, 0, len(terms))
	filters := []string{}
	for _, term := range terms {
		operator := ""
		raw := term
		if strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, "+") {
			operator = raw[:1]
			raw = raw[1:]
		}
		prefix, value, ok := strings.Cut(raw, ":")
		if !ok || value == "" {
			out = append(out, term)
			continue
		}
		prefix = strings.ToLower(prefix)
		negated := operator == "-"
		filter, handled, err := s.meiliStatusPrefixFilter(ctx, prefix, value, negated, current)
		if err != nil {
			return "", nil, err
		}
		if handled {
			if filter != "" {
				filters = append(filters, filter)
			}
			continue
		}
		out = append(out, strings.TrimSpace(prefix+" "+value))
	}
	return strings.Join(out, " "), filters, nil
}

func (s *Server) meiliStatusPrefixFilter(ctx context.Context, prefix string, value string, negated bool, current *models.Account) (string, bool, error) {
	switch prefix {
	case "has":
		field, ok := meiliStatusHasFilter(value)
		if !ok {
			return "", false, fmt.Errorf("Unknown has: filter: %s", value)
		}
		return meiliBoolFilter(field, !negated), true, nil
	case "is":
		switch value {
		case "reply":
			return meiliBoolFilter("is_reply", !negated), true, nil
		case "sensitive":
			return meiliBoolFilter("sensitive", !negated), true, nil
		default:
			field, ok := meiliStatusHasFilter(value)
			if !ok {
				return "", false, fmt.Errorf("Unknown has: filter: %s", value)
			}
			return meiliBoolFilter(field, !negated), true, nil
		}
	case "language":
		code := meiliStatusLanguageCode(value)
		if code == "" {
			return "", false, nil
		}
		if negated {
			return fmt.Sprintf("language != '%s'", code), true, nil
		}
		return fmt.Sprintf("language = '%s'", code), true, nil
	case "from":
		id := int64(-1)
		if strings.EqualFold(value, "me") {
			if current != nil && current.ID != 0 {
				id = current.ID
			}
		} else {
			account, err := s.findAccountByAcct(value)
			if err == nil && account != nil && account.ID != 0 {
				id = account.ID
			} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return "", false, err
			}
		}
		if negated {
			return fmt.Sprintf("account_id != %d", id), true, nil
		}
		return fmt.Sprintf("account_id = %d", id), true, nil
	case "before", "after", "during":
		timestamp, ok := meiliStatusDateTimestamp(value, meiliStatusSearchTimeZone(current))
		if !ok {
			return "", false, nil
		}
		switch prefix {
		case "before":
			return fmt.Sprintf("created_at_timestamp < %d", timestamp), true, nil
		case "after":
			return fmt.Sprintf("created_at_timestamp >= %d", timestamp), true, nil
		default:
			return fmt.Sprintf("created_at_timestamp >= %d AND created_at_timestamp < %d", timestamp, timestamp+86400), true, nil
		}
	case "in":
		switch value {
		case "library":
			id := int64(-1)
			if current != nil && current.ID != 0 {
				id = current.ID
			}
			return fmt.Sprintf("account_id = %d", id), true, nil
		case "public":
			return `visibility = "public"`, true, nil
		case "bookmark":
			if current == nil || current.ID == 0 {
				return "id = -1", true, nil
			}
			ids, err := s.bookmarkStatusIDsForSearch(ctx, current.ID)
			if err != nil {
				return "", false, err
			}
			if len(ids) == 0 {
				return "id = -1", true, nil
			}
			return "id IN [" + joinInt64s(ids) + "]", true, nil
		default:
			return "", false, nil
		}
	default:
		return "", false, nil
	}
}

func (s *Server) bookmarkStatusIDsForSearch(ctx context.Context, accountID int64) ([]int64, error) {
	if accountID == 0 {
		return []int64{}, nil
	}
	key := bookmarkFeedRedisKey(s.cfg.RedisNamespace, accountID)
	cacheCtx, cancel := context.WithTimeout(ctx, bookmarkFeedRedisTimeout)
	value, err := s.redisCommand(cacheCtx, "ZREVRANGE", key, "0", "-1")
	cancel()
	if err == nil {
		if members, ok := redisStringArray(value); ok && len(members) > 0 {
			return int64sFromRedisMembers(members), nil
		}
	}
	if s.db == nil {
		return []int64{}, nil
	}
	var rows []struct {
		StatusID   int64 `gorm:"column:status_id"`
		BookmarkID int64 `gorm:"column:bookmark_id"`
	}
	err = s.db.Table("bookmarks").
		Select("bookmarks.status_id, bookmarks.id AS bookmark_id").
		Joins("JOIN statuses ON statuses.id = bookmarks.status_id").
		Where("bookmarks.account_id = ? AND statuses.deleted_at IS NULL", accountID).
		Order("bookmarks.id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	if len(rows) == 0 {
		return ids, nil
	}
	cacheCtx, cancel = context.WithTimeout(ctx, bookmarkFeedRedisTimeout)
	defer cancel()
	args := []string{"ZADD", key}
	for _, row := range rows {
		if row.StatusID == 0 {
			continue
		}
		ids = append(ids, row.StatusID)
		args = append(args, strconv.FormatInt(row.BookmarkID, 10), strconv.FormatInt(row.StatusID, 10))
	}
	if len(args) > 2 {
		_, _ = s.redisCommand(cacheCtx, args...)
		_, _ = s.redisCommand(cacheCtx, "EXPIRE", key, strconv.FormatInt(int64(bookmarkFeedTTL(int64(len(ids)))/time.Second), 10))
	}
	return ids, nil
}

func int64sFromRedisMembers(members []string) []int64 {
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		id, err := strconv.ParseInt(member, 10, 64)
		if err == nil && id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func meiliStatusHasFilter(value string) (string, bool) {
	switch value {
	case "media":
		return "has_media", true
	case "image":
		return "has_image", true
	case "video":
		return "has_video", true
	case "poll":
		return "has_poll", true
	case "link":
		return "has_link", true
	case "embed":
		return "has_embed", true
	default:
		return "", false
	}
}

func meiliBoolFilter(field string, positive bool) string {
	if positive {
		return field + " = true"
	}
	return field + " = false"
}

func meiliStatusLanguageCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ToLower(value)
	if part, _, ok := strings.Cut(value, "-"); ok && part != "" {
		return part
	}
	if part, _, ok := strings.Cut(value, "_"); ok && part != "" {
		return part
	}
	return value
}

func meiliStatusDateTimestamp(value string, timeZone string) (int64, bool) {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Now().UTC().Unix(), true
	}
	loc, err := time.LoadLocation(strings.TrimSpace(timeZone))
	if err != nil || loc == nil {
		loc = time.UTC
	}
	local := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, loc)
	return local.Unix(), true
}

func meiliStatusSearchTimeZone(current *models.Account) string {
	if current != nil && current.User.TimeZone.Valid && strings.TrimSpace(current.User.TimeZone.String) != "" {
		return current.User.TimeZone.String
	}
	return "UTC"
}

func meiliStatusVisibilityFilter(current *models.Account) string {
	if current == nil || current.ID == 0 {
		return `(visibility = "public" OR visibility = "unlisted")`
	}
	return fmt.Sprintf(`((visibility = "public" OR visibility = "unlisted") OR searchable_by = %d)`, current.ID)
}

func mastodonSnowflakeUnixSeconds(id int64) int64 {
	return (id >> 16) / 1000
}

func (s *Server) searchMeiliTagIDs(ctx context.Context, query string, excludeUnreviewed bool, limitValue int, offsetValue int) ([]int64, error) {
	options := meiliSearchOptions{
		Limit:  limitValue,
		Offset: offsetValue,
		Sort:   []string{"usage:desc", "accounts_count:desc"},
	}
	if excludeUnreviewed {
		options.Filter = "reviewed = true"
	}
	return s.searchMeiliIDs(ctx, "tags", searchTagQuery(query), options)
}

func (s *Server) searchMeiliInstanceDomains(ctx context.Context, query string, limitValue int) ([]string, error) {
	if !s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == "" {
		return nil, errMeiliDisabled
	}
	body, err := json.Marshal(meiliSearchRequest{
		Query: searchTagQuery(query),
		meiliSearchOptions: meiliSearchOptions{
			Limit: limitValue,
			Sort:  []string{"accounts_count:desc"},
		},
	})
	if err != nil {
		return nil, err
	}
	host := strings.TrimRight(s.cfg.MeiliHost, "/")
	endpoint, err := url.JoinPath(host, "indexes", s.cfg.MeiliPrefix+"instances", "search")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.MeiliMasterKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.MeiliMasterKey)
	}
	res, err := meiliHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("meilisearch %s returned %s", s.cfg.MeiliPrefix+"instances", res.Status)
	}
	var payload meiliSearchResponse
	if err := decodeMeiliJSONResponse(res, &payload); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(payload.Hits))
	for _, hit := range payload.Hits {
		if domain, ok := hit["domain"].(string); ok && strings.TrimSpace(domain) != "" {
			out = append(out, domain)
		}
	}
	return out, nil
}

func decodeMeiliJSONResponse(res *http.Response, out any) error {
	if res == nil || res.Body == nil {
		return fmt.Errorf("meilisearch response body is empty")
	}
	if res.ContentLength > maxMeiliResponseBodySize {
		return fmt.Errorf("meilisearch response body is too large")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxMeiliResponseBodySize+1))
	if err != nil {
		return err
	}
	if len(body) > maxMeiliResponseBodySize {
		return fmt.Errorf("meilisearch response body is too large")
	}
	return json.Unmarshal(body, out)
}

func (s *Server) followingAccountIDs(accountID int64) ([]int64, error) {
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	if err := s.db.Table("follows").Select("target_account_id AS id").Where("account_id = ?", accountID).Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

func joinInt64s(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

func meiliHitID(value any) (int64, bool) {
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

func orderAccountsByIDs(accounts []models.Account, ids []int64) []models.Account {
	byID := make(map[int64]models.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]models.Account, 0, len(accounts))
	for _, id := range ids {
		if account, ok := byID[id]; ok {
			out = append(out, account)
		}
	}
	return out
}

func orderStatusesByIDs(statuses []models.Status, ids []int64) []models.Status {
	byID := make(map[int64]models.Status, len(statuses))
	for _, status := range statuses {
		byID[status.ID] = status
	}
	out := make([]models.Status, 0, len(statuses))
	for _, id := range ids {
		if status, ok := byID[id]; ok {
			out = append(out, status)
		}
	}
	return out
}

func orderTagsByIDs(tags []models.Tag, ids []int64) []models.Tag {
	byID := make(map[int64]models.Tag, len(tags))
	for _, tag := range tags {
		byID[tag.ID] = tag
	}
	out := make([]models.Tag, 0, len(tags))
	for _, id := range ids {
		if tag, ok := byID[id]; ok {
			out = append(out, tag)
		}
	}
	return out
}
