package api

import (
	"context"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const accountSearchTextRanksSQL = `(
	setweight(to_tsvector('simple', coalesce(accounts.display_name, '')), 'A') ||
	setweight(to_tsvector('simple', coalesce(accounts.username, '')), 'B') ||
	setweight(to_tsvector('simple', coalesce(accounts.domain, '')), 'C')
)`

const accountSearchBoostSQL = `((
	greatest(0, coalesce(account_search_stats.followers_count, 0)) /
	(greatest(0, coalesce(account_search_stats.following_count, 0)) + 1.0)
) + (
	log(greatest(0, coalesce(account_search_stats.followers_count, 0)) + 2)
) + (
	case
		when account_search_stats.last_status_at is null then 0
		else exp(
			-1.0 * (
				(greatest(0, abs(extract(DAY FROM age(account_search_stats.last_status_at))) - 30.0)^2) /
				(2.0 * ((-1.0 * 30^2) / (2.0 * ln(0.3))))
			)
		)
	end
)) / 3.0`

const accountSearchRankSQL = accountSearchBoostSQL + ` * ts_rank_cd(` + accountSearchTextRanksSQL + `, to_tsquery('simple', ?), 32)`

func (s *Server) accountSearchDatabaseResults(query string, current *models.Account, following bool, limitValue int, offsetValue int, excludeAccountID int64) ([]models.Account, error) {
	if s.db == nil || limitValue < 1 {
		return nil, nil
	}
	terms := s.accountSearchTermsForQuery(query)
	tsquery := accountSearchTSQuery(terms)
	if tsquery == "" {
		return nil, nil
	}

	db := accountSerializerPreloads(s.db.Model(&models.Account{})).
		Select("accounts.*, "+accountSearchRankSQL+" AS rank", tsquery).
		Joins("LEFT JOIN account_stats AS account_search_stats ON account_search_stats.account_id = accounts.id").
		Where("accounts.suspended_at IS NULL").
		Where("accounts.moved_to_account_id IS NULL").
		Where("to_tsquery('simple', ?) @@ "+accountSearchTextRanksSQL, tsquery)
	if excludeAccountID > 0 {
		db = db.Where("accounts.id <> ?", excludeAccountID)
	}
	if following && current != nil {
		db = db.
			Joins("JOIN (SELECT target_account_id FROM follows WHERE account_id = ? UNION ALL SELECT ?) account_search_first_degree ON account_search_first_degree.target_account_id = accounts.id", current.ID, current.ID).
			Joins("LEFT OUTER JOIN follows AS account_search_followers ON accounts.id = account_search_followers.account_id AND account_search_followers.target_account_id = ?", current.ID).
			Group("accounts.id, account_search_stats.id").
			Order("count(account_search_followers.id) DESC").
			Order("rank DESC")
	} else {
		if current != nil {
			db = db.
				Joins("LEFT OUTER JOIN follows AS account_search_followers ON (accounts.id = account_search_followers.account_id AND account_search_followers.target_account_id = ?) OR (accounts.id = account_search_followers.target_account_id AND account_search_followers.account_id = ?)", current.ID, current.ID).
				Group("accounts.id, account_search_stats.id")
		}
		db = db.
			Joins("LEFT JOIN users ON accounts.id = users.account_id").
			Where("accounts.domain IS NOT NULL OR (users.approved = TRUE AND users.confirmed_at IS NOT NULL)")
		if current != nil {
			db = db.Order("count(account_search_followers.id) DESC")
		}
		db = db.Order("rank DESC")
	}

	var accounts []models.Account
	if err := db.Offset(offsetValue).Limit(limitValue).Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

func (s *Server) accountSearchMeiliResults(ctx context.Context, query string, current *models.Account, following bool, limitValue int, offsetValue int, excludeAccountID int64) ([]models.Account, bool, error) {
	if s.db == nil || limitValue < 1 {
		return nil, false, nil
	}
	meiliIDs, err := s.searchMeiliAccountIDs(ctx, s.accountSearchTermsForQuery(query), current, following, limitValue, offsetValue)
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
		accountQuery = accountQuery.Joins("JOIN follows search_account_follows ON search_account_follows.target_account_id = accounts.id AND search_account_follows.account_id = ?", current.ID)
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

func accountSearchTSQuery(terms string) string {
	terms = strings.NewReplacer(
		"'", " ",
		"?", " ",
		"\\", " ",
		":", " ",
		"‘", " ",
		"’", " ",
	).Replace(strings.TrimSpace(terms))
	if strings.TrimSpace(terms) == "" {
		return ""
	}
	return "' " + terms + " ':*"
}
