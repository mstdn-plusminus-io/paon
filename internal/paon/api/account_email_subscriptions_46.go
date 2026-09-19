package api

import (
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func (s *Server) hydrateAccountEmailSubscriptions(accounts []*models.Account) {
	visible := s != nil && s.cfg.EmailSubscriptionsEnabled && s.settingBoolValue("email_subscriptions", false)
	everyonePermissions := int64(0)
	if visible {
		if everyone, err := s.userRoleByID(-99); err == nil && everyone != nil {
			everyonePermissions = everyone.Permissions
		}
	}
	seen := map[int64]struct{}{}
	var hydrate func(*models.Account)
	hydrate = func(account *models.Account) {
		if account == nil || account.ID == 0 {
			return
		}
		if _, ok := seen[account.ID]; ok {
			return
		}
		seen[account.ID] = struct{}{}
		account.EmailSubscriptionsVisible = visible
		account.EmailSubscriptionsValue = false
		if visible && account.User.ID != 0 {
			permissions := account.User.Role.Permissions | everyonePermissions
			account.EmailSubscriptionsValue = (permissions&rolePermissionAdministrator != 0 || permissions&rolePermissionManageEmailSubscriptions != 0) && userSettingBool(account.User, "email_subscriptions", false)
		}
		hydrate(account.MovedToAccount)
	}
	for _, account := range accounts {
		hydrate(account)
	}
}

func (s *Server) serializeAccount(account models.Account) serializer.Account {
	s.hydrateAccountEmailSubscriptions([]*models.Account{&account})
	return serializer.AccountFromModel(s.cfg, account)
}

func (s *Server) serializeAccountForCurrent(account models.Account, current *models.Account) serializer.Account {
	_ = s.hydrateAccountFeaturePolicies([]*models.Account{&account}, current)
	return serializer.AccountFromModelForCurrent(s.cfg, account, current)
}

func (s *Server) serializeAccounts(accounts []models.Account, currentAccounts ...*models.Account) []serializer.Account {
	ptrs := make([]*models.Account, 0, len(accounts))
	for i := range accounts {
		ptrs = append(ptrs, &accounts[i])
	}
	var current *models.Account
	if len(currentAccounts) > 0 {
		current = currentAccounts[0]
	}
	_ = s.hydrateAccountFeaturePolicies(ptrs, current)
	out := make([]serializer.Account, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, serializer.AccountFromModelForCurrent(s.cfg, account, current))
	}
	return out
}
