package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const remoteAccountWebFingerRefreshTTL = 24 * time.Hour

func (s *Server) resolveInteractionPath(raw string, current *models.Account) (string, error) {
	if searchURLQuery(raw) {
		account, err := s.resolveSearchAccountURL(raw)
		if err != nil {
			return "", err
		}
		if account != nil && account.ID != 0 && !account.SuspendedAt.Valid {
			return accountWebPath(*account), nil
		}
		status, err := s.resolveSearchStatusURL(raw, current)
		if err != nil {
			return "", err
		}
		if status != nil && status.ID != 0 {
			return statusWebPath(*status), nil
		}
		return "", nil
	}
	account, err := s.resolveInteractionAccount(raw)
	if err != nil {
		return "", err
	}
	if account == nil || account.ID == 0 || account.SuspendedAt.Valid {
		return "", nil
	}
	return accountWebPath(*account), nil
}

func (s *Server) interactionStatusVisible(status models.Status, current *models.Account) bool {
	if status.ID == 0 || status.DeletedAt.Valid || status.Account.SuspendedAt.Valid {
		return false
	}
	if status.Visibility <= 1 {
		return current == nil || (!s.interactionStatusAuthorBlocksCurrent(status.AccountID, current.ID) && !s.interactionStatusAuthorDomainBlocksCurrent(status.AccountID, current))
	}
	if current == nil {
		return false
	}
	if status.AccountID == current.ID {
		return true
	}
	if s.interactionStatusMentions(status.ID, current.ID) {
		return true
	}
	if status.Visibility == 2 {
		following, err := s.accountSearchCurrentFollows(current, status.AccountID)
		return err == nil && following
	}
	return false
}

func (s *Server) interactionStatusMentions(statusID int64, accountID int64) bool {
	if s.db == nil || statusID == 0 || accountID == 0 {
		return false
	}
	var count int64
	if err := s.db.Model(&models.Mention{}).Where("status_id = ? AND account_id = ?", statusID, accountID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func (s *Server) interactionStatusAuthorBlocksCurrent(authorID int64, currentID int64) bool {
	if s.db == nil || authorID == 0 || currentID == 0 {
		return false
	}
	var count int64
	if err := s.db.Model(&models.Block{}).Where("account_id = ? AND target_account_id = ?", authorID, currentID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func (s *Server) interactionStatusAuthorDomainBlocksCurrent(authorID int64, current *models.Account) bool {
	if s.db == nil || authorID == 0 || current == nil || !current.Domain.Valid || current.Domain.String == "" {
		return false
	}
	var count int64
	if err := s.db.Model(&models.AccountDomainBlock{}).Where("account_id = ? AND lower(domain) = lower(?)", authorID, current.Domain.String).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func (s *Server) resolveInteractionAccount(acct string) (*models.Account, error) {
	if s.db == nil {
		return nil, nil
	}
	account, err := s.findAccountByAcct(s.resolveInteractionAccountLookupAcct(acct))
	if err == nil {
		if remoteAccountWebFingerUpdateDue(account, time.Now().UTC()) {
			refreshed, refreshErr := s.fetchAndStoreActivityActorForAcct(account.Acct())
			if activityFetchGone(refreshErr) {
				_ = s.deleteRemoteGoneAccountNow(context.Background(), account, time.Now().UTC())
				return nil, nil
			}
			if refreshErr == nil && refreshed != nil && refreshed.ID != 0 {
				return refreshed, nil
			}
		}
		return account, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	remoteAcct, ok := s.importRemoteAcct(acct)
	if !ok {
		return nil, nil
	}
	account, err = s.fetchAndStoreActivityActorForAcct(remoteAcct)
	if err != nil {
		return nil, nil
	}
	return account, nil
}

func (s *Server) resolveInteractionAccountLookupAcct(acct string) string {
	acct = normalizeAcctInput(acct)
	username, domain, ok := strings.Cut(acct, "@")
	username = strings.TrimSpace(username)
	domain = strings.TrimSpace(domain)
	if !ok || username == "" || domain == "" {
		return acct
	}
	if strings.EqualFold(domain, s.cfg.LocalDomain) || strings.EqualFold(domain, s.cfg.WebDomain) {
		return username
	}
	return acct
}

func remoteAccountWebFingerUpdateDue(account *models.Account, now time.Time) bool {
	if account == nil || account.Local() || account.SuspendedAt.Valid {
		return false
	}
	return !account.LastWebfingeredAt.Valid || !account.LastWebfingeredAt.Time.After(now.Add(-remoteAccountWebFingerRefreshTTL))
}
