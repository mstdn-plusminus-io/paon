package api

import (
	"context"
	"regexp"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const accountSearchAutocompleteTextRanksSQL = `(
	setweight(to_tsvector('simple', coalesce(accounts.username, '') || ' ' || coalesce(accounts.domain, '')), 'A') ||
	setweight(to_tsvector('simple', coalesce(accounts.display_name, '')), 'A')
)`

const accountSearchFullTextRanksSQL = `(
	setweight(to_tsvector('simple', coalesce(accounts.username, '') || ' ' || coalesce(accounts.domain, '')), 'A') ||
	setweight(to_tsvector('simple', coalesce(accounts.display_name, '')), 'B') ||
	setweight(to_tsvector('simple', coalesce(accounts.note, '')), 'C')
)`

const accountSearchFollowersScoreSQL = `(
	ln(greatest(0, coalesce(account_search_stats.followers_count, 0)) + 1.0) / ln(10.0)
)`

func (s *Server) accountSearchDatabaseResults(query string, current *models.Account, following bool, fullText bool, limitValue int, offsetValue int, excludeAccountID int64) ([]models.Account, error) {
	if s.db == nil || limitValue < 1 {
		return nil, nil
	}
	terms := s.accountSearchTermsForQuery(query)
	tsquery := accountSearchTSQuery(terms)
	if tsquery == "" {
		return nil, nil
	}

	textRanks := accountSearchAutocompleteTextRanksSQL
	if fullText {
		textRanks = accountSearchFullTextRanksSQL
	}
	rankSQL := `ts_rank_cd(` + textRanks + `, to_tsquery('simple', ?), 32) + ` + accountSearchFollowersScoreSQL
	selectArgs := []any{tsquery}
	if current != nil && !following {
		rankSQL += ` + CASE WHEN account_search_current_follows.id IS NOT NULL OR accounts.id = ? THEN 100.0 ELSE 0.0 END`
		selectArgs = append(selectArgs, current.ID)
	}

	db := accountSerializerPreloads(s.db.Model(&models.Account{})).
		Select("accounts.*, "+rankSQL+" AS rank", selectArgs...).
		Joins("LEFT JOIN account_stats AS account_search_stats ON account_search_stats.account_id = accounts.id").
		Where("accounts.suspended_at IS NULL").
		Where("accounts.moved_to_account_id IS NULL").
		Where("to_tsquery('simple', ?) @@ "+textRanks, tsquery)
	if excludeAccountID > 0 {
		db = db.Where("accounts.id <> ?", excludeAccountID)
	}
	if following && current != nil {
		db = db.Joins("JOIN (SELECT target_account_id FROM follows WHERE account_id = ? UNION SELECT ?) account_search_first_degree ON account_search_first_degree.target_account_id = accounts.id", current.ID, current.ID)
	} else {
		if current != nil {
			db = db.Joins("LEFT JOIN follows AS account_search_current_follows ON account_search_current_follows.target_account_id = accounts.id AND account_search_current_follows.account_id = ?", current.ID)
		}
		db = db.
			Joins("LEFT JOIN users ON accounts.id = users.account_id").
			Where("accounts.domain IS NOT NULL OR (users.approved = TRUE AND users.confirmed_at IS NOT NULL)")
	}
	db = db.Order("rank DESC").Order("accounts.id ASC")

	var accounts []models.Account
	if err := db.Offset(offsetValue).Limit(limitValue).Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

func (s *Server) accountSearchMeiliResults(ctx context.Context, query string, current *models.Account, following bool, fullText bool, limitValue int, offsetValue int, excludeAccountID int64) ([]models.Account, bool, error) {
	if s.db == nil || limitValue < 1 {
		return nil, false, nil
	}
	meiliIDs, err := s.searchMeiliAccountIDs(ctx, s.accountSearchTermsForQuery(query), current, following, fullText, limitValue, offsetValue)
	if err != nil {
		return nil, false, nil
	}
	if len(meiliIDs) == 0 {
		return nil, true, nil
	}
	accounts := []models.Account{}
	accountQuery := accountSerializerPreloads(s.db).Where("accounts.suspended_at IS NULL AND accounts.id IN ?", meiliIDs)
	if excludeAccountID > 0 {
		accountQuery = accountQuery.Where("accounts.id <> ?", excludeAccountID)
	}
	if current != nil && following {
		accountQuery = accountQuery.Where("accounts.id = ? OR EXISTS (SELECT 1 FROM follows search_account_follows WHERE search_account_follows.target_account_id = accounts.id AND search_account_follows.account_id = ?)", current.ID, current.ID)
	}
	if err := accountQuery.Find(&accounts).Error; err != nil {
		return nil, true, err
	}
	return orderAccountsByIDs(accounts, meiliIDs), true, nil
}

func (s *Server) accountSearchTermsForQuery(query string) string {
	acct := strings.TrimSpace(strings.TrimPrefix(query, "@"))
	username, domain, ok := strings.Cut(acct, "@")
	if !ok || domain == "" || strings.EqualFold(domain, s.cfg.LocalDomain) || strings.EqualFold(domain, s.cfg.WebDomain) {
		return username
	}
	return acct
}

var accountSearchLexemePattern = regexp.MustCompile(`[\pL\pN_]+`)

func accountSearchTSQuery(terms string) string {
	lexemes := accountSearchLexemePattern.FindAllString(strings.TrimSpace(terms), -1)
	if len(lexemes) == 0 {
		return ""
	}
	query := make([]string, 0, len(lexemes))
	for _, lexeme := range lexemes {
		query = append(query, "'"+strings.ToLower(lexeme)+"':*")
	}
	return strings.Join(query, " & ")
}
