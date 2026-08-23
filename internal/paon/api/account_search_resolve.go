package api

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) resolveAccountSearchExact(query string, current *models.Account, resolve bool, following bool, offset int) (*models.Account, error) {
	if offset != 0 || strings.TrimSpace(query) == "" || !accountSearchCompleteAcct(query) {
		return nil, nil
	}
	account, err := s.accountSearchExactCandidate(query, resolve)
	if err != nil {
		return nil, err
	}
	if account == nil || account.SuspendedAt.Valid {
		return nil, nil
	}
	if following && current != nil {
		ok, err := s.accountSearchCurrentFollows(current, account.ID)
		if err != nil || !ok {
			return nil, err
		}
	}
	return account, nil
}

func (s *Server) accountSearchExactCandidate(query string, resolve bool) (*models.Account, error) {
	acct := normalizeAcctInput(query)
	if resolve {
		account, err := s.resolveInteractionAccount(acct)
		if err != nil {
			return nil, err
		}
		return account, nil
	}
	account, err := s.findAccountByAcct(acct)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return account, err
}

func accountSearchCompleteAcct(query string) bool {
	acct := normalizeAcctInput(query)
	username, domain, ok := strings.Cut(acct, "@")
	return ok && username != "" && domain != "" && !strings.Contains(domain, "@")
}

func (s *Server) accountSearchCurrentFollows(current *models.Account, targetID int64) (bool, error) {
	if current == nil || s.db == nil {
		return false, nil
	}
	if current.ID == targetID {
		return true, nil
	}
	var count int64
	if err := s.db.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", current.ID, targetID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func accountSearchNonExactLimit(query string, current *models.Account, limitValue int, exactAccount *models.Account) int {
	if limitValue < 1 {
		return 0
	}
	if current == nil && utf8.RuneCountInString(strings.TrimPrefix(strings.TrimSpace(query), "@")) < 3 {
		return 0
	}
	if exactAccount != nil {
		return limitValue - 1
	}
	return limitValue
}
