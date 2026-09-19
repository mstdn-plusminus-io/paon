package api

import (
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	featurePolicyUnsupported = 1 << 0
	featurePolicyPublic      = 1 << 1
	featurePolicyFollowers   = 1 << 2
	featurePolicyFollowing   = 1 << 3
	featurePolicyDisabled    = 1 << 4
)

func (s *Server) hydrateAccountFeaturePolicies(accounts []*models.Account, current *models.Account) error {
	if len(accounts) == 0 {
		return nil
	}
	s.hydrateAccountEmailSubscriptions(accounts)
	for _, account := range accounts {
		if account != nil {
			account.FeaturePolicyCurrentUser = "denied"
		}
	}
	if s == nil || s.db == nil || current == nil || current.ID == 0 {
		return nil
	}
	ids := make([]int64, 0, len(accounts))
	seen := map[int64]struct{}{}
	for _, account := range accounts {
		if account != nil && account.ID != 0 {
			if _, ok := seen[account.ID]; !ok {
				seen[account.ID] = struct{}{}
				ids = append(ids, account.ID)
			}
		}
	}
	currentFollows := map[int64]bool{}
	targetFollowsCurrent := map[int64]bool{}
	if len(ids) > 0 {
		var rows []models.Follow
		if err := s.db.Select("target_account_id").Where("account_id = ? AND target_account_id IN ?", current.ID, ids).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			currentFollows[row.TargetAccountID] = true
		}
		rows = nil
		if err := s.db.Select("account_id").Where("target_account_id = ? AND account_id IN ?", current.ID, ids).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			targetFollowsCurrent[row.AccountID] = true
		}
	}
	for _, account := range accounts {
		if account != nil {
			account.FeaturePolicyCurrentUser = featurePolicyDecision(*account, current, currentFollows[account.ID], targetFollowsCurrent[account.ID])
		}
	}
	return nil
}

func featurePolicyDecision(account models.Account, current *models.Account, currentFollows bool, targetFollowsCurrent bool) string {
	if current == nil || current.ID == 0 {
		return "denied"
	}
	if account.Local() {
		if !account.Discoverable.Valid || !account.Discoverable.Bool {
			return "denied"
		}
		if current.ID == account.ID {
			return "automatic"
		}
		if !account.Locked || currentFollows {
			return "automatic"
		}
		return "denied"
	}
	if current.ID == account.ID {
		return "automatic"
	}
	if account.FeatureApprovalPolicy == 0 {
		return "missing"
	}
	automatic := account.FeatureApprovalPolicy >> 16
	manual := account.FeatureApprovalPolicy & 0xffff
	if featureSubpolicyAllows(automatic, currentFollows, targetFollowsCurrent) {
		return "automatic"
	}
	if featureSubpolicyAllows(manual, currentFollows, targetFollowsCurrent) {
		return "manual"
	}
	if automatic&featurePolicyUnsupported != 0 || manual&featurePolicyUnsupported != 0 {
		return "unknown"
	}
	return "denied"
}

func featureSubpolicyAllows(policy int, currentFollows bool, targetFollowsCurrent bool) bool {
	return policy&featurePolicyPublic != 0 ||
		policy&featurePolicyFollowers != 0 && currentFollows ||
		policy&featurePolicyFollowing != 0 && targetFollowsCurrent
}

func statusAuthorAccounts(statuses []models.Status) []*models.Account {
	out := []*models.Account{}
	var appendStatus func(*models.Status)
	appendStatus = func(status *models.Status) {
		if status == nil {
			return
		}
		out = append(out, &status.Account)
		for i := range status.PreviewCards {
			if status.PreviewCards[i].AuthorAccount != nil {
				out = append(out, status.PreviewCards[i].AuthorAccount)
			}
		}
		appendStatus(status.Reblog)
		if status.Quote != nil && status.Quote.QuotedStatus != nil {
			appendStatus(status.Quote.QuotedStatus)
		}
	}
	for i := range statuses {
		appendStatus(&statuses[i])
	}
	return out
}
