package api

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const suggestionSourceGlobal = "global"
const suggestionSourcePastInteractions = "past_interactions"
const suggestionSourceStaff = "featured"
const suggestionSourceFriendsOfFriends = "friends_of_friends"
const suggestionSourceSimilarProfiles = "similar_to_recently_followed"
const suggestionSourceFASP = "fasp"

const suggestionSourceBatchSize = 40
const suggestionCacheTTL = 15 * time.Minute

const potentialFriendshipExpireSeconds = 90 * 24 * 60 * 60
const potentialFriendshipMaxItems = 80

type suggestedAccount struct {
	Account models.Account
	Source  string
	Sources []string
}

type cachedSuggestedAccount struct {
	ID      int64    `json:"id"`
	Sources []string `json:"sources"`
}

type staffSuggestionRef struct {
	Username string
	Domain   string
}

func (s *Server) suggestionsV2(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	s.scheduleFASPFollowRecommendations(c, account.ID)

	limitValue := limit(c, 40, 80)
	offsetValue := suggestionOffset(c.QueryParam("offset"))
	accounts, err := s.suggestedAccounts(account.ID, requestSuggestionLocale(c, s.cfg.DefaultLocale), limitValue+offsetValue)
	if err != nil {
		return err
	}
	if offsetValue >= len(accounts) {
		accounts = []suggestedAccount{}
	} else {
		accounts = accounts[offsetValue:]
		if len(accounts) > limitValue {
			accounts = accounts[:limitValue]
		}
	}

	out := make([]serializer.Suggestion, 0, len(accounts))
	for _, suggested := range accounts {
		out = append(out, serializer.SuggestionFromModelWithSources(s.cfg, suggested.Account, suggestionSources(suggested)))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) suggestionsV1(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:accounts")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")

	accounts, err := s.suggestedAccountsFromPastInteractions(account.ID, map[int64]struct{}{}, limit(c, 40, 80))
	if err != nil {
		return err
	}
	out := make([]models.Account, 0, len(accounts))
	for _, suggested := range accounts {
		out = append(out, suggested.Account)
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, out))
}

func (s *Server) deleteSuggestion(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	targetAccountID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || targetAccountID <= 0 {
		return s.notFound(c)
	}
	if s.db != nil {
		now := time.Now().UTC()
		if err := s.db.Exec(`
			INSERT INTO follow_recommendation_mutes (account_id, target_account_id, created_at, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (account_id, target_account_id) DO NOTHING
		`, account.ID, targetAccountID, now, now).Error; err != nil {
			return err
		}
	}
	s.invalidateSuggestionCache((*c).Request().Context(), account.ID)
	return renderEmpty(c)
}

func (s *Server) suggestedAccounts(accountID int64, locale string, limitValue int) ([]suggestedAccount, error) {
	if limitValue <= 0 {
		return []suggestedAccount{}, nil
	}
	if cached, ok := s.readSuggestionCache(accountID); ok {
		return s.loadCachedSuggestedAccounts(accountID, cached, limitValue)
	}

	sources := []func() ([]suggestedAccount, error){
		func() ([]suggestedAccount, error) {
			return s.suggestedAccountsFromStaffSetting(accountID, map[int64]struct{}{}, suggestionSourceBatchSize)
		},
		func() ([]suggestedAccount, error) {
			return s.suggestedAccountsFromFriendsOfFriends(accountID, suggestionSourceBatchSize)
		},
		func() ([]suggestedAccount, error) {
			return s.suggestedAccountsFromSimilarProfiles(accountID, suggestionSourceBatchSize)
		},
		func() ([]suggestedAccount, error) {
			return s.suggestedAccountsFromGlobalRecommendations(accountID, map[int64]struct{}{}, locale, suggestionSourceBatchSize)
		},
		func() ([]suggestedAccount, error) {
			return s.suggestedAccountsFromFASPRecommendations(accountID, suggestionSourceBatchSize)
		},
	}
	merged := make([]cachedSuggestedAccount, 0, len(sources)*suggestionSourceBatchSize)
	positions := map[int64]int{}
	for _, source := range sources {
		items, err := source()
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if position, exists := positions[item.Account.ID]; exists {
				merged[position].Sources = appendUniqueStrings(merged[position].Sources, suggestionSources(item)...)
				continue
			}
			positions[item.Account.ID] = len(merged)
			merged = append(merged, cachedSuggestedAccount{ID: item.Account.ID, Sources: suggestionSources(item)})
		}
	}
	rand.Shuffle(len(merged), func(i int, j int) { merged[i], merged[j] = merged[j], merged[i] })
	s.writeSuggestionCache(accountID, merged)
	return s.loadCachedSuggestedAccounts(accountID, merged, limitValue)
}

func (s *Server) suggestedAccountsFromFASPRecommendations(accountID int64, limitValue int) ([]suggestedAccount, error) {
	if !s.faspEnabled() || s.db == nil || limitValue <= 0 {
		return []suggestedAccount{}, nil
	}
	var ids []int64
	if err := s.db.Model(&models.FaspFollowRecommendation{}).
		Where("requesting_account_id = ?", accountID).
		Where("created_at >= ?", time.Now().UTC().Add(-24*time.Hour)).
		Order("created_at DESC").
		Limit(limitValue*3).
		Pluck("recommended_account_id", &ids).Error; err != nil {
		return nil, err
	}
	return s.suggestedAccountsByOrderedIDs(accountID, ids, suggestionSourceFASP, limitValue)
}

func suggestionOffset(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	// Each recommendation source contributes at most one source batch.
	if value > suggestionSourceBatchSize*5 {
		return suggestionSourceBatchSize * 5
	}
	return value
}

func suggestionSources(item suggestedAccount) []string {
	if len(item.Sources) > 0 {
		return append([]string(nil), item.Sources...)
	}
	if strings.TrimSpace(item.Source) == "" {
		return []string{}
	}
	return []string{item.Source}
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func suggestionCacheKey(accountID int64) string {
	return "follow_recommendations/" + strconv.FormatInt(accountID, 10)
}

func (s *Server) readSuggestionCache(accountID int64) ([]cachedSuggestedAccount, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	value, err := s.cacheRedisCommand(ctx, "GET", suggestionCacheKey(accountID))
	if err != nil || value == nil {
		return nil, false
	}
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, false
	}
	var cached []cachedSuggestedAccount
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		return nil, false
	}
	return cached, true
}

func (s *Server) writeSuggestionCache(accountID int64, suggestions []cachedSuggestedAccount) {
	body, err := json.Marshal(suggestions)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, _ = s.cacheRedisCommand(ctx, "SET", suggestionCacheKey(accountID), string(body), "EX", strconv.Itoa(int(suggestionCacheTTL/time.Second)))
}

func (s *Server) invalidateSuggestionCache(ctx context.Context, accountIDs ...int64) {
	if len(accountIDs) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	args := []string{"DEL"}
	for _, accountID := range accountIDs {
		if accountID > 0 {
			args = append(args, suggestionCacheKey(accountID))
		}
	}
	if len(args) > 1 {
		_, _ = s.cacheRedisCommand(ctx, args...)
	}
}

func (s *Server) loadCachedSuggestedAccounts(accountID int64, cached []cachedSuggestedAccount, limitValue int) ([]suggestedAccount, error) {
	if len(cached) == 0 || limitValue <= 0 {
		return []suggestedAccount{}, nil
	}
	ids := make([]int64, 0, len(cached))
	for _, item := range cached {
		if item.ID > 0 {
			ids = append(ids, item.ID)
		}
	}
	var accounts []models.Account
	if err := s.suggestionFollowableAccountQuery(accountID).
		Where("accounts.id IN ?", ids).
		Find(&accounts).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]models.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]suggestedAccount, 0, min(limitValue, len(cached)))
	for _, item := range cached {
		account, ok := byID[item.ID]
		if !ok {
			continue
		}
		out = append(out, suggestedAccount{Account: account, Sources: append([]string(nil), item.Sources...)})
		if len(out) >= limitValue {
			break
		}
	}
	return out, nil
}

func (s *Server) suggestedAccountsFromFriendsOfFriends(accountID int64, limitValue int) ([]suggestedAccount, error) {
	if s.db == nil || limitValue <= 0 {
		return []suggestedAccount{}, nil
	}
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	err := s.db.Raw(`
		WITH first_degree AS (
			SELECT follows.target_account_id
			FROM follows
			JOIN accounts first_degree_accounts ON first_degree_accounts.id = follows.target_account_id
			WHERE follows.account_id = ? AND COALESCE(first_degree_accounts.hide_collections, FALSE) = FALSE
		)
		SELECT accounts.id
		FROM accounts
		JOIN follows ON follows.target_account_id = accounts.id
		LEFT JOIN account_stats ON account_stats.account_id = accounts.id
		WHERE follows.account_id IN (SELECT target_account_id FROM first_degree)
		GROUP BY accounts.id, account_stats.id
		ORDER BY COUNT(*) DESC, COALESCE(account_stats.followers_count, 0) ASC
		LIMIT ?
	`, accountID, limitValue*3).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return s.suggestedAccountsByOrderedIDs(accountID, ids, suggestionSourceFriendsOfFriends, limitValue)
}

func (s *Server) suggestedAccountsFromSimilarProfiles(accountID int64, limitValue int) ([]suggestedAccount, error) {
	if s.db == nil || !s.cfg.MeiliEnabled || limitValue <= 0 {
		return []suggestedAccount{}, nil
	}
	var profiles []models.Account
	if err := s.db.Model(&models.Account{}).
		Select("accounts.*").
		Joins("JOIN follows recent_suggestion_follows ON recent_suggestion_follows.target_account_id = accounts.id").
		Where("recent_suggestion_follows.account_id = ?", accountID).
		Order("recent_suggestion_follows.id DESC").
		Limit(5).
		Find(&profiles).Error; err != nil {
		return nil, err
	}
	queryParts := make([]string, 0, len(profiles)*3)
	for _, profile := range profiles {
		queryParts = append(queryParts, profile.Username, profile.DisplayName, plainMeiliText(profile.Note))
	}
	query := strings.TrimSpace(strings.Join(queryParts, " "))
	if query == "" {
		return []suggestedAccount{}, nil
	}
	ids, err := s.searchMeiliIDs(context.Background(), "accounts", query, meiliSearchOptions{
		Limit:  limitValue * 3,
		Filter: "discoverable = true AND bot = false",
		Sort:   []string{"followers_count:desc"},
	})
	if err != nil {
		// SimilarProfiles is an optional source. Search downtime must not make
		// the other three recommendation sources unavailable.
		return []suggestedAccount{}, nil
	}
	return s.suggestedAccountsByOrderedIDs(accountID, ids, suggestionSourceSimilarProfiles, limitValue)
}

func (s *Server) suggestedAccountsByOrderedIDs(accountID int64, ids []int64, source string, limitValue int) ([]suggestedAccount, error) {
	if len(ids) == 0 || limitValue <= 0 {
		return []suggestedAccount{}, nil
	}
	var accounts []models.Account
	if err := s.suggestionFollowableAccountQuery(accountID).
		Where("accounts.id IN ?", ids).
		Find(&accounts).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]models.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]suggestedAccount, 0, min(limitValue, len(accounts)))
	for _, id := range ids {
		if account, ok := byID[id]; ok {
			out = append(out, suggestedAccount{Account: account, Source: source})
			if len(out) >= limitValue {
				break
			}
		}
	}
	return out, nil
}

func appendSuggestedAccounts(selected []suggestedAccount, candidates []suggestedAccount, skip map[int64]struct{}, limitValue int) []suggestedAccount {
	for _, existing := range selected {
		skip[existing.Account.ID] = struct{}{}
	}
	blockedBeforeSource := make(map[int64]struct{}, len(skip))
	for id := range skip {
		blockedBeforeSource[id] = struct{}{}
	}
	for _, candidate := range candidates {
		if len(selected) >= limitValue {
			break
		}
		if _, ok := blockedBeforeSource[candidate.Account.ID]; ok {
			continue
		}
		selected = append(selected, candidate)
	}
	for _, item := range selected {
		skip[item.Account.ID] = struct{}{}
	}
	return selected
}

func (s *Server) suggestedAccountsFromStaffSetting(accountID int64, skip map[int64]struct{}, limitValue int) ([]suggestedAccount, error) {
	refs := staffSuggestionRefs(s.cfg.LocalDomain, s.settingValue("bootstrap_timeline_accounts", ""))
	if len(refs) == 0 || limitValue <= 0 {
		return []suggestedAccount{}, nil
	}
	condition, args := staffSuggestionCondition(refs)
	if condition == "" {
		return []suggestedAccount{}, nil
	}
	var accounts []models.Account
	query := applySuggestionSkipIDs(s.suggestionFollowableAccountQuery(accountID), skip).
		Where("accounts.locked = FALSE").
		Where(condition, args...)
	if err := query.Find(&accounts).Error; err != nil {
		return nil, err
	}
	byKey := make(map[string]models.Account, len(accounts))
	for _, account := range accounts {
		byKey[staffSuggestionKey(account.Username, account.Domain.String)] = account
	}
	out := make([]suggestedAccount, 0, len(accounts))
	for _, ref := range refs {
		if account, ok := byKey[staffSuggestionKey(ref.Username, ref.Domain)]; ok {
			out = append(out, suggestedAccount{Account: account, Source: suggestionSourceStaff})
		}
		if len(out) >= limitValue {
			break
		}
	}
	return out, nil
}

func (s *Server) suggestedAccountsFromPastInteractions(accountID int64, skip map[int64]struct{}, limitValue int) ([]suggestedAccount, error) {
	if limitValue <= 0 {
		return []suggestedAccount{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	value, err := s.redisCommand(ctx, "ZREVRANGE", suggestionInteractionsRedisKey(s.cfg, accountID), "0", strconv.Itoa(limitValue+len(skip)))
	if err != nil {
		return []suggestedAccount{}, nil
	}
	members, ok := redisStringArray(value)
	if !ok {
		return []suggestedAccount{}, nil
	}
	accountIDs := suggestionIDsFromRedisMembers(members, skip, limitValue)
	if len(accountIDs) == 0 {
		return []suggestedAccount{}, nil
	}

	var accounts []models.Account
	if err := applySuggestionSkipIDs(s.suggestionSearchableAccountQuery(), skip).
		Where("accounts.id IN ?", accountIDs).
		Find(&accounts).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]models.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]suggestedAccount, 0, len(accounts))
	for _, id := range accountIDs {
		if account, ok := byID[id]; ok {
			out = append(out, suggestedAccount{Account: account, Source: suggestionSourcePastInteractions})
		}
	}
	return out, nil
}

func (s *Server) recordPotentialFriendship(ctx context.Context, accountID int64, targetAccountID int64, action string) {
	if s == nil || s.db == nil || accountID == 0 || targetAccountID == 0 || accountID == targetAccountID {
		return
	}
	weight, ok := potentialFriendshipWeight(action)
	if !ok {
		return
	}
	var followCount int64
	if err := s.db.Model(&models.Follow{}).
		Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).
		Count(&followCount).Error; err != nil || followCount > 0 {
		return
	}
	ctx, cancel := potentialFriendshipWriteContext(ctx)
	defer cancel()
	key := suggestionInteractionsRedisKey(s.cfg, accountID)
	_, _ = s.redisCommand(ctx, "ZINCRBY", key, strconv.Itoa(weight), strconv.FormatInt(targetAccountID, 10))
	_, _ = s.redisCommand(ctx, "ZREMRANGEBYRANK", key, "0", "-"+strconv.Itoa(potentialFriendshipMaxItems))
	_, _ = s.redisCommand(ctx, "EXPIRE", key, strconv.Itoa(potentialFriendshipExpireSeconds))
}

func (s *Server) removePotentialFriendship(ctx context.Context, accountID int64, targetAccountID int64) {
	if s == nil || accountID == 0 || targetAccountID == 0 {
		return
	}
	ctx, cancel := potentialFriendshipWriteContext(ctx)
	defer cancel()
	_, _ = s.redisCommand(ctx, "ZREM", suggestionInteractionsRedisKey(s.cfg, accountID), strconv.FormatInt(targetAccountID, 10))
}

func potentialFriendshipWeight(action string) (int, bool) {
	switch action {
	case "reply":
		return 1, true
	case "favourite":
		return 10, true
	case "reblog":
		return 20, true
	default:
		return 0, false
	}
}

func potentialFriendshipWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), 250*time.Millisecond)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 250*time.Millisecond)
}

func (s *Server) suggestedAccountsFromGlobalRecommendations(accountID int64, skip map[int64]struct{}, locale string, limitValue int) ([]suggestedAccount, error) {
	if redisAccounts, ok, err := s.suggestedAccountsFromRedisFollowRecommendations(accountID, skip, locale, limitValue); err != nil {
		return nil, err
	} else if ok {
		return redisAccounts, nil
	}
	return []suggestedAccount{}, nil
}

func (s *Server) suggestedAccountsFromRedisFollowRecommendations(accountID int64, skip map[int64]struct{}, locale string, limitValue int) ([]suggestedAccount, bool, error) {
	if limitValue <= 0 {
		return []suggestedAccount{}, true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	value, err := s.redisCommand(ctx, "ZREVRANGE", followRecommendationsRedisKey(s.cfg.RedisNamespace, followRecommendationLocale(locale)), "0", "-1")
	if err != nil {
		return nil, false, nil
	}
	members, ok := redisStringArray(value)
	if !ok || len(members) == 0 {
		return nil, false, nil
	}
	accountIDs := suggestionIDsFromRedisMembers(members, skip, limitValue)
	if len(accountIDs) == 0 {
		return []suggestedAccount{}, true, nil
	}
	var accounts []models.Account
	if err := applySuggestionSkipIDs(s.suggestionFollowableAccountQuery(accountID), skip).
		Where("accounts.id IN ?", accountIDs).
		Find(&accounts).Error; err != nil {
		return nil, true, err
	}
	byID := make(map[int64]models.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]suggestedAccount, 0, len(accounts))
	for _, id := range accountIDs {
		if account, ok := byID[id]; ok {
			out = append(out, suggestedAccount{Account: account, Source: suggestionSourceGlobal})
		}
	}
	return out, true, nil
}

func (s *Server) suggestionSearchableAccountQuery() *gorm.DB {
	return s.db.Model(&models.Account{}).
		Preload("AccountStat").
		Preload("User.Role").
		Joins("LEFT JOIN users suggestion_users ON suggestion_users.account_id = accounts.id").
		Where("accounts.suspended_at IS NULL").
		Where("accounts.silenced_at IS NULL").
		Where("accounts.moved_to_account_id IS NULL").
		Where("accounts.memorial = FALSE").
		Where("COALESCE(accounts.discoverable, FALSE) = TRUE").
		Where("COALESCE(accounts.hide_collections, FALSE) = FALSE").
		Where("(accounts.domain IS NOT NULL OR (suggestion_users.approved = ? AND suggestion_users.confirmed_at IS NOT NULL))", true)
}

func (s *Server) suggestionFollowableAccountQuery(accountID int64) *gorm.DB {
	return s.suggestionSearchableAccountQuery().
		Joins("LEFT JOIN follows suggestion_existing_follows ON suggestion_existing_follows.account_id = ? AND suggestion_existing_follows.target_account_id = accounts.id", accountID).
		Joins("LEFT JOIN follow_requests suggestion_existing_follow_requests ON suggestion_existing_follow_requests.account_id = ? AND suggestion_existing_follow_requests.target_account_id = accounts.id", accountID).
		Joins("LEFT JOIN blocks suggestion_account_blocks ON suggestion_account_blocks.account_id = ? AND suggestion_account_blocks.target_account_id = accounts.id", accountID).
		Joins("LEFT JOIN blocks suggestion_target_blocks ON suggestion_target_blocks.account_id = accounts.id AND suggestion_target_blocks.target_account_id = ?", accountID).
		Joins("LEFT JOIN mutes suggestion_account_mutes ON suggestion_account_mutes.account_id = ? AND suggestion_account_mutes.target_account_id = accounts.id", accountID).
		Joins("LEFT JOIN follow_recommendation_mutes suggestion_mutes ON suggestion_mutes.account_id = ? AND suggestion_mutes.target_account_id = accounts.id", accountID).
		Where("accounts.id <> ?", accountID).
		Where("suggestion_existing_follows.id IS NULL").
		Where("suggestion_existing_follow_requests.id IS NULL").
		Where("suggestion_account_blocks.id IS NULL").
		Where("suggestion_target_blocks.id IS NULL").
		Where("suggestion_account_mutes.id IS NULL").
		Where("suggestion_mutes.id IS NULL").
		Where("NOT EXISTS (SELECT 1 FROM account_domain_blocks suggestion_domain_blocks WHERE suggestion_domain_blocks.account_id = ? AND lower(suggestion_domain_blocks.domain) = lower(accounts.domain))", accountID)
}

func suggestedAccountsWithSource(accounts []models.Account, source string) []suggestedAccount {
	out := make([]suggestedAccount, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, suggestedAccount{Account: account, Source: source})
	}
	return out
}

func suggestionInteractionsRedisKey(cfg config.Config, accountID int64) string {
	return redisConfig(cfg).prefix + "interactions:" + strconv.FormatInt(accountID, 10)
}

func suggestionIDsFromRedisMembers(members []string, skip map[int64]struct{}, limitValue int) []int64 {
	out := make([]int64, 0, limitValue)
	seen := map[int64]struct{}{}
	for _, member := range members {
		if len(out) >= limitValue {
			break
		}
		id, err := strconv.ParseInt(strings.TrimSpace(member), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := skip[id]; ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		out = append(out, id)
		seen[id] = struct{}{}
	}
	return out
}

func applySuggestionSkipIDs(query *gorm.DB, skip map[int64]struct{}) *gorm.DB {
	if len(skip) == 0 {
		return query
	}
	ids := make([]int64, 0, len(skip))
	for id := range skip {
		ids = append(ids, id)
	}
	return query.Where("accounts.id NOT IN ?", ids)
}

func staffSuggestionRefs(localDomain string, value string) []staffSuggestionRef {
	localDomain = strings.ToLower(strings.TrimSpace(localDomain))
	refs := []staffSuggestionRef{}
	for _, raw := range strings.Split(value, ",") {
		username, domain, ok := parseStaffSuggestionRef(raw, localDomain)
		if !ok {
			continue
		}
		refs = append(refs, staffSuggestionRef{Username: username, Domain: domain})
	}
	return refs
}

func parseStaffSuggestionRef(value string, localDomain string) (string, string, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "@"))
	if value == "" {
		return "", "", false
	}
	username, domain, _ := strings.Cut(value, "@")
	username = strings.ToLower(strings.TrimSpace(username))
	domain = strings.ToLower(strings.TrimSpace(domain))
	if username == "" {
		return "", "", false
	}
	if domain == localDomain {
		domain = ""
	}
	return username, domain, true
}

func staffSuggestionCondition(refs []staffSuggestionRef) (string, []any) {
	parts := make([]string, 0, len(refs))
	args := make([]any, 0, len(refs)*2)
	for _, ref := range refs {
		if ref.Domain == "" {
			parts = append(parts, "(lower(accounts.username) = ? AND accounts.domain IS NULL)")
			args = append(args, ref.Username)
		} else {
			parts = append(parts, "(lower(accounts.username) = ? AND lower(accounts.domain) = ?)")
			args = append(args, ref.Username, ref.Domain)
		}
	}
	return strings.Join(parts, " OR "), args
}

func staffSuggestionKey(username string, domain string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "@" + strings.ToLower(strings.TrimSpace(domain))
}
