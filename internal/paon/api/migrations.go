package api

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const accountMigrationCooldown = 30 * 24 * time.Hour

func (s *Server) settingsMigrationPage(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	migrations, err := s.accountMigrations(account.ID)
	if err != nil {
		return err
	}
	cooldown := migrationCooldownUntil(migrations, time.Now().UTC())
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, migrationHTML(*user, *account, migrations, cooldown, c.QueryParam("notice"), c.QueryParam("error"), renderArgs...))
}

func (s *Server) createSettingsMigration(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	params, err := settingsMigrationParams(c)
	if errors.Is(err, errSettingsMigrationParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	if !deleteChallengePassed(*user, *account, params.CurrentPassword, params.CurrentUsername) {
		return s.renderSettingsMigrationError(c, *user, *account, settingsT(locale, "deletes.challenge_not_passed", "Challenge did not pass"), locale)
	}
	target, err := s.migrationTargetAccount(params.Acct, account)
	if err != nil {
		return s.renderSettingsMigrationError(c, *user, *account, migrationErrorText(locale, err), locale)
	}
	sourceURI := activityPubActorURL(s, *account)
	if !stringArrayContains(target.AlsoKnownAs, sourceURI) {
		return s.renderSettingsMigrationError(c, *user, *account, settingsT(locale, "migrations.errors.missing_also_known_as", "Target account is missing the required alias back-reference"), locale)
	}
	acquired, releaseMigrationLock, err := s.acquireActivityPubRedisLock(c.Request().Context(), "account_migration:"+strconv.FormatInt(account.ID, 10), 15*time.Minute)
	if err != nil {
		return err
	}
	if !acquired {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	defer releaseMigrationLock()
	migrations, err := s.accountMigrations(account.ID)
	if err != nil {
		return err
	}
	if until := migrationCooldownUntil(migrations, time.Now().UTC()); until.Valid {
		return s.renderSettingsMigrationError(c, *user, *account, settingsT(locale, "migrations.errors.on_cooldown", "Account migration is on cooldown")+" "+until.Time.UTC().Format(time.RFC3339), locale)
	}
	migration, err := s.createAccountMigration(account, target, params.Acct)
	if err != nil {
		return err
	}
	if err := s.processLocalAccountMigration(*migration); err != nil {
		return err
	}
	s.triggerAccountWebhook("account.updated", account.ID)
	_ = s.enqueueFASPAccountLifecycleByID(c.Request().Context(), account.ID, "update")
	_ = s.enqueueActivityPubAccountUpdate(*account, 0)
	_ = s.deliverActivityPubMove(*migration)
	return c.Redirect(http.StatusFound, "/settings/migration?notice="+url.QueryEscape(migrationMovedMessage(locale, target.Acct())))
}

func (s *Server) newSettingsMigrationRedirect(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, migrationRedirectHTML(*user, "", renderArgs...))
}

func (s *Server) createSettingsMigrationRedirect(c *echo.Context) error {
	if strings.EqualFold(c.FormValue("_method"), "delete") {
		return s.destroySettingsMigrationRedirect(c)
	}
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	params, err := settingsMigrationRedirectParams(c)
	if errors.Is(err, errSettingsMigrationRedirectParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	if !deleteChallengePassed(*user, *account, params.CurrentPassword, params.CurrentUsername) {
		return s.renderSettingsMigrationRedirectError(c, *user, settingsT(locale, "deletes.challenge_not_passed", "Challenge did not pass"), locale)
	}
	target, err := s.migrationTargetAccount(params.Acct, account)
	if err != nil {
		return s.renderSettingsMigrationRedirectError(c, *user, migrationErrorText(locale, err), locale)
	}
	if err := s.setMovedToAccount(account.ID, sql.NullInt64{Int64: target.ID, Valid: true}); err != nil {
		return err
	}
	s.triggerAccountWebhook("account.updated", account.ID)
	_ = s.enqueueFASPAccountLifecycleByID(c.Request().Context(), account.ID, "update")
	_ = s.enqueueActivityPubAccountUpdate(*account, 0)
	return c.Redirect(http.StatusFound, "/settings/migration?notice="+url.QueryEscape(migrationRedirectedMessage(locale, target.Acct())))
}

func (s *Server) destroySettingsMigrationRedirect(c *echo.Context) error {
	user, _, err := s.currentUserIncludingDisabled(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	account, err := s.currentAccountForSettings(user.AccountID)
	if err != nil {
		return err
	}
	if account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "This account is suspended")
	}
	if account.MovedToAccountID.Valid {
		if err := s.setMovedToAccount(account.ID, sql.NullInt64{}); err != nil {
			return err
		}
		s.triggerAccountWebhook("account.updated", account.ID)
		_ = s.enqueueFASPAccountLifecycleByID(c.Request().Context(), account.ID, "update")
		_ = s.enqueueActivityPubAccountUpdate(*account, 0)
	}
	return c.Redirect(http.StatusFound, "/settings/migration?notice="+url.QueryEscape(settingsT(locale, "migrations.cancelled_msg", "Redirect cancelled")))
}

func (s *Server) renderSettingsMigrationError(c *echo.Context, user models.User, account models.Account, message string, locale string) error {
	migrations, err := s.accountMigrations(account.ID)
	if err != nil {
		return err
	}
	cooldown := migrationCooldownUntil(migrations, time.Now().UTC())
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, &user, &account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, migrationHTML(user, account, migrations, cooldown, "", message, renderArgs...))
}

func (s *Server) renderSettingsMigrationRedirectError(c *echo.Context, user models.User, message string, locale string) error {
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, &user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, migrationRedirectHTML(user, message, renderArgs...))
}

type settingsMigrationFormParams struct {
	Acct            string
	CurrentPassword string
	CurrentUsername string
}

var (
	errSettingsMigrationParamsMissing         = errors.New("settings migration root parameter is missing")
	errSettingsMigrationRedirectParamsMissing = errors.New("settings migration redirect root parameter is missing")
)

func settingsMigrationParams(c *echo.Context) (settingsMigrationFormParams, error) {
	return settingsMigrationRootParams(c, "account_migration", errSettingsMigrationParamsMissing)
}

func settingsMigrationRedirectParams(c *echo.Context) (settingsMigrationFormParams, error) {
	return settingsMigrationRootParams(c, "form_redirect", errSettingsMigrationRedirectParamsMissing)
}

func settingsMigrationRootParams(c *echo.Context, prefix string, missingErr error) (settingsMigrationFormParams, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return settingsMigrationFormParams{}, err
	}
	if !formHasNestedPrefix(req.Form, prefix) {
		return settingsMigrationFormParams{}, missingErr
	}
	return settingsMigrationFormParams{
		Acct:            normalizeAliasAcct(lastFormValue(req.Form, prefix+"[acct]")),
		CurrentPassword: lastFormValue(req.Form, prefix+"[current_password]"),
		CurrentUsername: lastFormValue(req.Form, prefix+"[current_username]"),
	}, nil
}

func (s *Server) currentAccountForSettings(accountID int64) (*models.Account, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var account models.Account
	if err := s.db.Preload("AccountStat").Preload("MovedToAccount").Where("id = ?", accountID).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (s *Server) accountMigrations(accountID int64) ([]models.AccountMigration, error) {
	if s.db == nil {
		return []models.AccountMigration{}, nil
	}
	var migrations []models.AccountMigration
	err := s.db.Preload("TargetAccount").Where("account_id = ?", accountID).Order("id DESC").Find(&migrations).Error
	return migrations, err
}

func (s *Server) migrationTargetAccount(acct string, current *models.Account) (*models.Account, error) {
	if acct == "" || !strings.Contains(acct, "@") {
		return nil, errMigrationInvalidAcct
	}
	if strings.EqualFold(acct, current.Acct()) || strings.EqualFold(acct, current.Username+"@"+s.cfg.LocalDomain) {
		return nil, errMigrationMoveToSelf
	}
	account, err := s.findAccountByAcct(acct)
	if err != nil {
		remoteAcct, ok := s.importRemoteAcct(acct)
		if !ok {
			return nil, errMigrationNotFound
		}
		account, err = s.fetchAndStoreActivityActorForAcct(remoteAcct)
		if err != nil || !accountMatchesImportAcct(account, remoteAcct) {
			return nil, errMigrationNotFound
		}
	}
	if current.MovedToAccountID.Valid && current.MovedToAccountID.Int64 == account.ID {
		return nil, errMigrationAlreadyMoved
	}
	return account, nil
}

func (s *Server) createAccountMigration(account *models.Account, target *models.Account, acct string) (*models.AccountMigration, error) {
	now := time.Now().UTC()
	migration := models.AccountMigration{
		AccountID:       models.AccountMigrationAccountID(account.ID),
		Acct:            acct,
		FollowersCount:  account.AccountStat.FollowersCount,
		TargetAccountID: sql.NullInt64{Int64: target.ID, Valid: true},
		CreatedAt:       now,
		UpdatedAt:       now,
		Account:         *account,
		TargetAccount:   *target,
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&migration).Error; err != nil {
			return err
		}
		return tx.Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{
			"moved_to_account_id": target.ID,
			"updated_at":          now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &migration, nil
}

func (s *Server) processLocalAccountMigration(migration models.AccountMigration) error {
	if s.db == nil {
		return nil
	}
	if migration.Account.ID == 0 || migration.TargetAccount.ID == 0 {
		if err := s.db.Preload("Account").Preload("TargetAccount").Where("id = ?", migration.ID).First(&migration).Error; err != nil {
			return err
		}
	}
	var deferredErr error
	if migration.Account.Local() && migration.TargetAccount.Local() {
		if err := s.rewriteLocalAccountMigrationFollows(migration.Account.ID, migration.TargetAccount.ID); err != nil {
			return err
		}
	} else if migration.Account.Local() && !migration.TargetAccount.Local() {
		if err := s.migrateRemoteAccountMigrationFollowers(migration.Account, migration.TargetAccount); err != nil {
			deferredErr = err
		}
	} else if !migration.Account.Local() {
		if err := s.migrateRemoteAccountMigrationFollowers(migration.Account, migration.TargetAccount); err != nil {
			deferredErr = err
		}
	}
	if err := s.carryAccountMigrationRelationships(migration.Account, migration.TargetAccount); err != nil {
		return errors.Join(deferredErr, err)
	}
	return deferredErr
}

func (s *Server) rewriteLocalAccountMigrationFollows(sourceAccountID int64, targetAccountID int64) error {
	now := time.Now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.approveTargetFollowRequestsFromSourceFollowers(tx, sourceAccountID, targetAccountID, now); err != nil {
			return err
		}
		if err := migrateLocalAccountMigrationExistingTargetFollowLists(tx, sourceAccountID, targetAccountID); err != nil {
			return err
		}
		followIDs, err := localMigrationFollowIDs(tx, sourceAccountID, targetAccountID)
		if err != nil {
			return err
		}
		if len(followIDs) == 0 {
			return nil
		}
		if err := tx.Model(&models.ListAccount{}).Where("follow_id IN ?", followIDs).Updates(map[string]any{
			"account_id": targetAccountID,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Follow{}).Where("id IN ?", followIDs).Updates(map[string]any{
			"target_account_id": targetAccountID,
			"updated_at":        now,
		}).Error; err != nil {
			return err
		}
		moved := int64(len(followIDs))
		if err := decrementAccountStatCounter(tx, sourceAccountID, accountStatCounterFollowers, moved); err != nil {
			return err
		}
		return incrementAccountStatCounter(tx, targetAccountID, accountStatCounterFollowers, moved)
	})
}

func migrateLocalAccountMigrationExistingTargetFollowLists(tx *gorm.DB, sourceAccountID int64, targetAccountID int64) error {
	var rows []struct {
		SourceFollowID int64
		TargetFollowID int64
	}
	if err := tx.Table("follows AS source_follows").
		Select("source_follows.id AS source_follow_id, target_follows.id AS target_follow_id").
		Joins("JOIN accounts ON accounts.id = source_follows.account_id").
		Joins("JOIN follows AS target_follows ON target_follows.account_id = source_follows.account_id AND target_follows.target_account_id = ?", targetAccountID).
		Where("source_follows.target_account_id = ? AND (accounts.domain IS NULL OR accounts.domain = '')", sourceAccountID).
		Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := migrateListAccountsForAccountMigration(tx, row.SourceFollowID, targetAccountID, sql.NullInt64{Int64: row.TargetFollowID, Valid: true}, sql.NullInt64{}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) approveTargetFollowRequestsFromSourceFollowers(tx *gorm.DB, sourceAccountID int64, targetAccountID int64, now time.Time) error {
	var requests []models.FollowRequest
	if err := tx.Model(&models.FollowRequest{}).
		Joins("JOIN follows source_follows ON source_follows.account_id = follow_requests.account_id AND source_follows.target_account_id = ?", sourceAccountID).
		Joins("JOIN accounts ON accounts.id = follow_requests.account_id").
		Where("follow_requests.target_account_id = ? AND (accounts.domain IS NULL OR accounts.domain = '')", targetAccountID).
		Find(&requests).Error; err != nil {
		return err
	}
	for _, request := range requests {
		follow := models.Follow{
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       request.AccountID,
			TargetAccountID: targetAccountID,
			ShowReblogs:     request.ShowReblogs,
			Notify:          request.Notify,
			URI:             request.URI,
			Languages:       request.Languages,
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if err := tx.Model(&models.ListAccount{}).Where("follow_request_id = ?", request.ID).Updates(map[string]any{
				"account_id":        targetAccountID,
				"follow_id":         follow.ID,
				"follow_request_id": nil,
			}).Error; err != nil {
				return err
			}
			if err := incrementAccountStatCounter(tx, request.AccountID, accountStatCounterFollowing, 1); err != nil {
				return err
			}
			if err := incrementAccountStatCounter(tx, targetAccountID, accountStatCounterFollowers, 1); err != nil {
				return err
			}
		}
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", request.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&request).Error; err != nil {
			return err
		}
	}
	return nil
}

func localMigrationFollowIDs(tx *gorm.DB, sourceAccountID int64, targetAccountID int64) ([]int64, error) {
	var ids []int64
	err := tx.Model(&models.Follow{}).
		Select("follows.id").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id = ? AND follows.account_id <> ? AND (accounts.domain IS NULL OR accounts.domain = '')", sourceAccountID, targetAccountID).
		Where("NOT EXISTS (SELECT 1 FROM follows existing_follows WHERE existing_follows.account_id = follows.account_id AND existing_follows.target_account_id = ?)", targetAccountID).
		Pluck("follows.id", &ids).Error
	return ids, err
}

func (s *Server) migrateRemoteAccountMigrationFollowers(source models.Account, target models.Account) error {
	var follows []models.Follow
	if err := s.db.Preload("Account").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id = ? AND (accounts.domain IS NULL OR accounts.domain = '')", source.ID).
		Find(&follows).Error; err != nil {
		return err
	}
	ctx := context.Background()
	var deferredErr error
	for _, follow := range follows {
		bypassLocked := target.Local()
		if s.enqueueUnfollowFollowTask(follow.AccountID, source.ID, target.ID, bypassLocked) {
			continue
		}
		if err := s.performUnfollowFollowMigration(ctx, follow.AccountID, source.ID, target.ID, bypassLocked); err != nil {
			deferredErr = errors.Join(deferredErr, err)
		}
	}
	return deferredErr
}

func (s *Server) performUnfollowFollowMigration(ctx context.Context, followerAccountID int64, oldTargetAccountID int64, newTargetAccountID int64, bypassLocked bool) error {
	if s == nil || s.db == nil || followerAccountID == 0 || oldTargetAccountID == 0 || newTargetAccountID == 0 {
		return nil
	}
	var follower models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", followerAccountID).First(&follower).Error; err != nil {
		return nil
	}
	var target models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", newTargetAccountID).First(&target).Error; err != nil {
		return nil
	}
	if target.SuspendedAt.Valid || target.ID == follower.ID {
		return nil
	}
	disallowed, err := s.followNotAllowed(&follower, &target)
	if err != nil || disallowed {
		return err
	}
	var follow models.Follow
	if err := s.db.WithContext(ctx).Preload("Account").Where("account_id = ? AND target_account_id = ?", followerAccountID, oldTargetAccountID).First(&follow).Error; err != nil {
		return nil
	}
	if target.Local() && bypassLocked && !follower.SilencedAt.Valid {
		created, err := s.createDirectMigrationFollow(follow, target)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return deleteFollow(tx, follow)
		}); err != nil {
			return err
		}
		s.accountMigrationDirectFollowCacheEffects(ctx, follow, oldTargetAccountID, target.ID)
		return nil
	}
	request, created, err := s.createMigrationFollowRequest(follow, target)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	s.accountMigrationFollowRequestCacheEffects(ctx, follow, target.ID)
	if target.Local() {
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return deleteFollow(tx, follow)
		}); err != nil {
			return err
		}
		s.accountMigrationOldFollowCacheEffects(ctx, follow, oldTargetAccountID)
		return nil
	}
	if err := s.deliverActivityPubMigratedFollow(follow.Account, target, request.ID, string(request.URI), oldTargetAccountID); err != nil {
		return nil
	}
	return nil
}

func (s *Server) accountMigrationDirectFollowCacheEffects(ctx context.Context, follow models.Follow, oldTargetID int64, newTargetID int64) {
	s.removePotentialFriendship(ctx, follow.AccountID, newTargetID)
	s.invalidateFollowRelationshipCaches(ctx, follow.Account, newTargetID)
	s.accountMigrationOldFollowCacheEffects(ctx, follow, oldTargetID)
}

func (s *Server) accountMigrationFollowRequestCacheEffects(ctx context.Context, follow models.Follow, targetID int64) {
	s.removePotentialFriendship(ctx, follow.AccountID, targetID)
	s.invalidateRelationshipCaches(ctx, follow.AccountID, targetID)
}

func (s *Server) accountMigrationOldFollowCacheEffects(ctx context.Context, follow models.Follow, oldTargetID int64) {
	s.invalidateFollowRelationshipCaches(ctx, follow.Account, oldTargetID)
}

func (s *Server) createDirectMigrationFollow(original models.Follow, target models.Account) (bool, error) {
	now := time.Now().UTC()
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existingFollow models.Follow
		err := tx.Where("account_id = ? AND target_account_id = ?", original.AccountID, target.ID).First(&existingFollow).Error
		if err == nil {
			return migrateListAccountsForAccountMigration(tx, original.ID, target.ID, sql.NullInt64{Int64: existingFollow.ID, Valid: true}, sql.NullInt64{})
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		follow := models.Follow{
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       original.AccountID,
			TargetAccountID: target.ID,
			ShowReblogs:     original.ShowReblogs,
			Notify:          original.Notify,
			Languages:       original.Languages,
			URI:             models.NullSafeString(activityPubGeneratedPayloadURI(s)),
		}
		if err := tx.Create(&follow).Error; err != nil {
			return err
		}
		created = true
		if err := migrateListAccountsForAccountMigration(tx, original.ID, target.ID, sql.NullInt64{Int64: follow.ID, Valid: true}, sql.NullInt64{}); err != nil {
			return err
		}
		if err := incrementAccountStatCounter(tx, original.AccountID, accountStatCounterFollowing, 1); err != nil {
			return err
		}
		return incrementAccountStatCounter(tx, target.ID, accountStatCounterFollowers, 1)
	})
	return created, err
}

func (s *Server) createMigrationFollowRequest(original models.Follow, target models.Account) (*models.FollowRequest, bool, error) {
	now := time.Now().UTC()
	var request models.FollowRequest
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existingFollow models.Follow
		err := tx.Where("account_id = ? AND target_account_id = ?", original.AccountID, target.ID).First(&existingFollow).Error
		if err == nil {
			return migrateListAccountsForAccountMigration(tx, original.ID, target.ID, sql.NullInt64{Int64: existingFollow.ID, Valid: true}, sql.NullInt64{})
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		err = tx.Where("account_id = ? AND target_account_id = ?", original.AccountID, target.ID).First(&request).Error
		if err == nil {
			if err := tx.Model(&request).Updates(map[string]any{
				"show_reblogs": original.ShowReblogs,
				"notify":       original.Notify,
				"languages":    original.Languages,
				"updated_at":   now,
			}).Error; err != nil {
				return err
			}
			return migrateListAccountsForAccountMigration(tx, original.ID, target.ID, sql.NullInt64{}, sql.NullInt64{Int64: request.ID, Valid: true})
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		request = models.FollowRequest{
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       original.AccountID,
			TargetAccountID: target.ID,
			ShowReblogs:     original.ShowReblogs,
			Notify:          original.Notify,
			Languages:       original.Languages,
			URI:             models.NullSafeString(activityPubGeneratedPayloadURI(s)),
		}
		if err := tx.Create(&request).Error; err != nil {
			return err
		}
		created = true
		return migrateListAccountsForAccountMigration(tx, original.ID, target.ID, sql.NullInt64{}, sql.NullInt64{Int64: request.ID, Valid: true})
	})
	if err != nil {
		return nil, false, err
	}
	return &request, created, nil
}

func migrateListAccountsForAccountMigration(tx *gorm.DB, originalFollowID int64, targetAccountID int64, followID sql.NullInt64, followRequestID sql.NullInt64) error {
	var rows []models.ListAccount
	if err := tx.Where("follow_id = ?", originalFollowID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		migrated := models.ListAccount{
			ListID:          row.ListID,
			AccountID:       targetAccountID,
			FollowID:        followID,
			FollowRequestID: followRequestID,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&migrated).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) carryAccountMigrationRelationships(source models.Account, target models.Account) error {
	now := time.Now().UTC()
	sourceAcct := source.Acct()
	var notePairs []accountRelationshipPair
	var blockPairs []accountRelationshipPair
	var mutePairs []accountMigrationMuteEffect
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		notePairs, err = copyAccountMigrationNotes(tx, source.ID, target.ID, sourceAcct, now)
		if err != nil {
			return err
		}
		blockPairs, err = s.carryAccountMigrationBlocks(tx, source.ID, target.ID, sourceAcct, now)
		if err != nil {
			return err
		}
		mutePairs, err = carryAccountMigrationMutes(tx, source.ID, target.ID, sourceAcct, now)
		return err
	}); err != nil {
		return err
	}
	ctx := context.Background()
	for _, pair := range uniqueAccountRelationshipPairs(notePairs) {
		s.invalidateRelationshipCaches(ctx, pair.accountID, pair.targetID)
	}
	for _, pair := range uniqueAccountRelationshipPairs(blockPairs) {
		s.clearAfterBlockFeedCaches(ctx, pair.accountID, pair.targetID)
		s.removePotentialFriendship(ctx, pair.accountID, pair.targetID)
		s.invalidateBlockRelationshipCaches(ctx, pair.accountID, pair.targetID)
	}
	for _, pair := range uniqueAccountMigrationMuteEffects(mutePairs) {
		if pair.hideNotifications {
			s.clearAfterBlockFeedCaches(ctx, pair.accountID, pair.targetID)
		} else {
			s.clearAfterMuteFeedCache(ctx, pair.accountID, pair.targetID)
		}
		s.removePotentialFriendship(ctx, pair.accountID, pair.targetID)
		s.invalidateMuteRelationshipCaches(ctx, pair.accountID, pair.targetID)
	}
	return nil
}

type accountRelationshipPair struct {
	accountID int64
	targetID  int64
}

type accountMigrationMuteEffect struct {
	accountID         int64
	targetID          int64
	hideNotifications bool
}

func uniqueAccountRelationshipPairs(values []accountRelationshipPair) []accountRelationshipPair {
	seen := map[accountRelationshipPair]struct{}{}
	out := make([]accountRelationshipPair, 0, len(values))
	for _, value := range values {
		if value.accountID == 0 || value.targetID == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func uniqueAccountMigrationMuteEffects(values []accountMigrationMuteEffect) []accountMigrationMuteEffect {
	seen := map[accountMigrationMuteEffect]struct{}{}
	out := make([]accountMigrationMuteEffect, 0, len(values))
	for _, value := range values {
		if value.accountID == 0 || value.targetID == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func copyAccountMigrationNotes(tx *gorm.DB, sourceAccountID int64, targetAccountID int64, sourceAcct string, now time.Time) ([]accountRelationshipPair, error) {
	var notes []models.AccountNote
	if err := tx.Where("target_account_id = ?", sourceAccountID).Find(&notes).Error; err != nil {
		return nil, err
	}
	affected := []accountRelationshipPair{}
	prefix := accountMigrationNoteText("copy", sourceAcct)
	for _, note := range notes {
		if !note.AccountID.Valid || note.AccountID.Int64 == 0 {
			continue
		}
		accountID := note.AccountID.Int64
		comment := prefix + "\n" + note.Comment
		var existing models.AccountNote
		err := tx.Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row := models.AccountNote{AccountID: models.AccountNoteAccountID(accountID), TargetAccountID: models.AccountNoteAccountID(targetAccountID), Comment: comment, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return nil, err
			}
			affected = append(affected, accountRelationshipPair{accountID: accountID, targetID: targetAccountID})
			continue
		}
		if err != nil {
			return nil, err
		}
		merged := prefix + "\n" + note.Comment + "\n\n" + existing.Comment
		if err := tx.Model(&existing).Updates(map[string]any{"comment": merged, "updated_at": now}).Error; err != nil {
			return nil, err
		}
		affected = append(affected, accountRelationshipPair{accountID: accountID, targetID: targetAccountID})
	}
	return affected, nil
}

func (s *Server) carryAccountMigrationBlocks(tx *gorm.DB, sourceAccountID int64, targetAccountID int64, sourceAcct string, now time.Time) ([]accountRelationshipPair, error) {
	var blocks []models.Block
	if err := tx.Model(&models.Block{}).
		Joins("JOIN accounts ON accounts.id = blocks.account_id").
		Where("blocks.target_account_id = ? AND (accounts.domain IS NULL OR accounts.domain = '')", sourceAccountID).
		Find(&blocks).Error; err != nil {
		return nil, err
	}
	affected := []accountRelationshipPair{}
	for _, block := range blocks {
		exists, err := accountMigrationRelationshipExists(tx, &models.Block{}, block.AccountID, targetAccountID)
		if err != nil {
			return nil, err
		}
		following, err := accountMigrationRelationshipExists(tx, &models.Follow{}, block.AccountID, targetAccountID)
		if err != nil {
			return nil, err
		}
		if exists || following {
			continue
		}
		if _, err := s.createAccountBlock(tx, block.AccountID, targetAccountID, now); err != nil {
			return nil, err
		}
		if err := afterBlockServiceCleanup(tx, block.AccountID, targetAccountID); err != nil {
			return nil, err
		}
		if err := createAccountMigrationNoteIfMissing(tx, block.AccountID, targetAccountID, accountMigrationNoteText("block", sourceAcct), now); err != nil {
			return nil, err
		}
		affected = append(affected, accountRelationshipPair{accountID: block.AccountID, targetID: targetAccountID})
	}
	return affected, nil
}

func carryAccountMigrationMutes(tx *gorm.DB, sourceAccountID int64, targetAccountID int64, sourceAcct string, now time.Time) ([]accountMigrationMuteEffect, error) {
	var mutes []models.Mute
	if err := tx.Model(&models.Mute{}).
		Joins("JOIN accounts ON accounts.id = mutes.account_id").
		Where("mutes.target_account_id = ? AND (accounts.domain IS NULL OR accounts.domain = '')", sourceAccountID).
		Find(&mutes).Error; err != nil {
		return nil, err
	}
	affected := []accountMigrationMuteEffect{}
	for _, mute := range mutes {
		exists, err := accountMigrationRelationshipExists(tx, &models.Mute{}, mute.AccountID, targetAccountID)
		if err != nil {
			return nil, err
		}
		following, err := accountMigrationRelationshipExists(tx, &models.Follow{}, mute.AccountID, targetAccountID)
		if err != nil {
			return nil, err
		}
		if exists || following {
			continue
		}
		if err := upsertAccountMute(tx, mute.AccountID, targetAccountID, mute.HideNotifications, now, mute.ExpiresAt); err != nil {
			return nil, err
		}
		if mute.HideNotifications {
			if err := afterBlockServiceCleanup(tx, mute.AccountID, targetAccountID); err != nil {
				return nil, err
			}
		}
		if err := createAccountMigrationNoteIfMissing(tx, mute.AccountID, targetAccountID, accountMigrationNoteText("mute", sourceAcct), now); err != nil {
			return nil, err
		}
		affected = append(affected, accountMigrationMuteEffect{accountID: mute.AccountID, targetID: targetAccountID, hideNotifications: mute.HideNotifications})
	}
	return affected, nil
}

func accountMigrationRelationshipExists(tx *gorm.DB, model any, accountID int64, targetAccountID int64) (bool, error) {
	var count int64
	if err := tx.Model(model).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func createAccountMigrationNoteIfMissing(tx *gorm.DB, accountID int64, targetAccountID int64, comment string, now time.Time) error {
	var count int64
	if err := tx.Model(&models.AccountNote{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	note := models.AccountNote{AccountID: models.AccountNoteAccountID(accountID), TargetAccountID: models.AccountNoteAccountID(targetAccountID), Comment: comment, CreatedAt: now, UpdatedAt: now}
	return tx.Create(&note).Error
}

func accountMigrationNoteText(kind string, sourceAcct string) string {
	switch kind {
	case "copy":
		return "This user moved from " + sourceAcct + ", here were your previous notes about them:"
	case "block":
		return "This user moved from " + sourceAcct + ", which you had blocked."
	case "mute":
		return "This user moved from " + sourceAcct + ", which you had muted."
	default:
		return "This user moved from " + sourceAcct + "."
	}
}

func (s *Server) setMovedToAccount(accountID int64, target sql.NullInt64) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if target.Valid {
		updates["moved_to_account_id"] = target.Int64
	} else {
		updates["moved_to_account_id"] = nil
	}
	return s.db.Model(&models.Account{}).Where("id = ?", accountID).Updates(updates).Error
}

func migrationCooldownUntil(migrations []models.AccountMigration, now time.Time) sql.NullTime {
	for _, migration := range migrations {
		until := migration.CreatedAt.Add(accountMigrationCooldown)
		if until.After(now) {
			return sql.NullTime{Time: until, Valid: true}
		}
	}
	return sql.NullTime{}
}

func migrationHTML(user models.User, account models.Account, migrations []models.AccountMigration, cooldown sql.NullTime, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	redirect := `<p class="hint"><span class="positive-hint">` + html.EscapeString(settingsT(loc, "migrations.not_redirecting", "Not redirecting")) + `</span></p>`
	if account.MovedToAccountID.Valid {
		acct := "#" + strconv.FormatInt(account.MovedToAccountID.Int64, 10)
		card := `<div class="account-card"><strong>` + html.EscapeString(acct) + `</strong></div>`
		if account.MovedToAccount != nil && account.MovedToAccount.ID != 0 {
			acct = accountAliasPrettyAcct(account.MovedToAccount.Acct())
			card = registrationInviterCardHTML(*account.MovedToAccount, false)
		}
		redirect = `<div class="fields-row"><div class="fields-row__column fields-group fields-row__column-6">` + card + `</div><div class="fields-row__column fields-group fields-row__column-6"><p class="hint"><span class="positive-hint">` + html.EscapeString(settingsTVars(loc, "migrations.redirecting_to", "Redirecting to account %{acct}", map[string]string{"acct": acct})) + `</span></p><p class="hint">` + html.EscapeString(settingsT(loc, "migrations.cancel_explanation", "You can cancel the redirect while the account is still available.")) + `</p><p class="hint"><a data-method="delete" href="/settings/migration/redirect">` + html.EscapeString(settingsT(loc, "migrations.cancel", "Cancel redirect")) + `</a></p></div></div>`
	}
	cooldownHTML := ""
	disabled := ""
	if cooldown.Valid {
		remaining := time.Until(cooldown.Time)
		days := int((remaining + 24*time.Hour - 1) / (24 * time.Hour))
		if days < 1 {
			days = 1
		}
		cooldownHTML = `<p class="hint"><span class="warning-hint">` + html.EscapeString(settingsTVars(loc, "migrations.on_cooldown", "You have recently migrated your account. This function will become available again in %{count} days.", map[string]string{"count": strconv.Itoa(days)})) + `</span></p>`
		disabled = " disabled"
	} else {
		cooldownHTML = `<p class="hint">` + html.EscapeString(settingsT(loc, "migrations.warning.before", "Before proceeding, please read these notes carefully:")) + `</p><ul class="hint"><li class="warning-hint">` + html.EscapeString(settingsT(loc, "migrations.warning.followers", "Your followers will be moved to the new account")) + `</li><li class="warning-hint">` + html.EscapeString(settingsT(loc, "migrations.warning.redirect", "Your profile will redirect to the new account")) + `</li><li class="warning-hint">` + html.EscapeString(settingsT(loc, "migrations.warning.other_data", "No other data will be moved automatically")) + `</li><li class="warning-hint">` + html.EscapeString(settingsT(loc, "migrations.warning.backreference_required", "The new account must list this account as an alias")) + `</li><li class="warning-hint">` + html.EscapeString(settingsT(loc, "migrations.warning.cooldown", "You must wait before moving again")) + `</li><li class="warning-hint">` + html.EscapeString(settingsT(loc, "migrations.warning.disabled_account", "This account will become inactive")) + `</li></ul>`
	}
	historyHTML := ""
	if len(migrations) > 0 {
		var rows strings.Builder
		for _, migration := range migrations {
			acct := migration.Acct
			accountHTML := html.EscapeString(acct)
			if migration.TargetAccount.ID != 0 {
				accountHTML = migrationCompactAccountLinkHTML(migration.TargetAccount)
			}
			rows.WriteString(`<tr><td>`)
			rows.WriteString(accountHTML)
			rows.WriteString(`</td><td>`)
			rows.WriteString(strconv.FormatInt(migration.FollowersCount, 10))
			stamp := migration.CreatedAt.UTC().Format(time.RFC3339)
			rows.WriteString(`</td><td><time class="time-ago" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(stamp) + `">`)
			rows.WriteString(html.EscapeString(stamp))
			rows.WriteString(`</time>`)
			rows.WriteString(`</td></tr>`)
		}
		historyHTML = `
	<hr class="spacer">
    <h3>` + html.EscapeString(settingsT(loc, "migrations.past_migrations", "Past migrations")) + `</h3>
	<hr class="spacer">
	<div class="table-wrapper"><table class="table inline-table">
      <thead><tr><th>` + html.EscapeString(settingsT(loc, "migrations.acct", "Account")) + `</th><th>` + html.EscapeString(settingsT(loc, "migrations.followers_count", "Followers")) + `</th><th></th></tr></thead>
      <tbody>` + rows.String() + `</tbody>
	</table></div>`
	}
	return authPageHTML(settingsT(loc, "settings.migrate", "Account migration"), notice, errorText, `
	<div class="simple_form">`+redirect+`</div>
	<hr class="spacer">
	<h3>`+html.EscapeString(settingsT(loc, "auth.migrate_account", "Move to a different account"))+`</h3>
	<form class="simple_form new_account_migration" id="new_account_migration" novalidate="novalidate" method="post" action="/settings/migration">
    `+cooldownHTML+`
	  <p class="hint">`+settingsTVars(loc, "migrations.warning.only_redirect_html", "You can also <a href=\"%{path}\">set up a redirect without moving followers</a>.", map[string]string{"path": "/settings/migration/redirect/new"})+`</p>
	  <hr class="spacer">
	  <div class="fields-row"><div class="fields-row__column fields-group fields-row__column-6">`+migrationAddressField("account_migration", disabled, loc)+`</div><div class="fields-row__column fields-group fields-row__column-6">
      `+migrationChallengeField(user, "account_migration", disabled, loc)+`
	  </div></div><div class="actions"><button name="button" type="submit" class="btn button button--destructive"`+disabled+`>`+html.EscapeString(settingsT(loc, "migrations.proceed_with_move", "Move followers"))+`</button></div>
    </form>
	`+historyHTML+`
	<hr class="spacer"><h3>`+html.EscapeString(settingsT(loc, "migrations.incoming_migrations", "Moving from a different account"))+`</h3><p class="muted-hint">`+settingsTVars(loc, "migrations.incoming_migrations_html", "You can <a href=\"%{path}\">create an account alias</a>.", map[string]string{"path": "/settings/aliases"})+`</p>`, localeAndTheme...)
}

func migrationRedirectHTML(user models.User, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	return authPageHTML(settingsT(loc, "settings.migrate", "Account migration"), "", errorText, `
	<form class="simple_form new_form_redirect" id="new_form_redirect" novalidate="novalidate" method="post" action="/settings/migration/redirect">
	  <p class="hint">`+html.EscapeString(settingsT(loc, "migrations.warning.before", "Before proceeding, please read these notes carefully:"))+`</p><ul class="hint"><li class="warning-hint">`+html.EscapeString(settingsT(loc, "migrations.warning.redirect", "Your profile will redirect to the new account"))+`</li><li class="warning-hint">`+html.EscapeString(settingsT(loc, "migrations.warning.other_data", "No other data will be moved automatically"))+`</li><li class="warning-hint">`+html.EscapeString(settingsT(loc, "migrations.warning.disabled_account", "This account will become inactive"))+`</li></ul><hr class="spacer">
	  <div class="fields-row"><div class="fields-row__column fields-group fields-row__column-6">`+migrationAddressField("form_redirect", "", loc)+`</div><div class="fields-row__column fields-group fields-row__column-6">`+migrationChallengeField(user, "form_redirect", "", loc)+`</div></div>
	  <div class="actions"><button name="button" type="submit" class="btn button button--destructive">`+html.EscapeString(settingsT(loc, "migrations.set_redirect", "Set redirect"))+`</button></div>
    </form>`, localeAndTheme...)
}

func migrationAddressField(prefix string, disabled string, locale string) string {
	id := prefix + "_acct"
	return `<div class="input with_block_label string required ` + id + ` field_with_hint"><label class="string required" for="` + id + `">` + html.EscapeString(settingsT(locale, "simple_form.labels.account_migration.acct", "New account")) + filterRequiredMarker(locale) + `</label><span class="hint">` + html.EscapeString(settingsT(locale, "simple_form.hints.account_migration.acct", "Specify the username@domain of the account you are moving to")) + `</span><div class="label_input"><input autocapitalize="none" autocorrect="off" class="string required" type="text" value="" name="` + prefix + `[acct]" id="` + id + `"` + disabled + `></div></div>`
}

func migrationChallengeField(user models.User, prefix string, disabled string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	if strings.TrimSpace(user.EncryptedPassword) == "" {
		id := prefix + "_current_username"
		return `<div class="input with_block_label string required ` + id + ` field_with_hint"><label class="string required" for="` + id + `">` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.current_username", "Current username")) + filterRequiredMarker(loc) + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "simple_form.hints.defaults.current_username", "For security purposes please enter the username of the current account")) + `</span><div class="label_input"><input autocomplete="off" class="string required" required="required" aria-required="true" type="text" name="` + prefix + `[current_username]" id="` + id + `"` + disabled + `></div></div>`
	}
	id := prefix + "_current_password"
	return `<div class="input with_block_label password required ` + id + ` field_with_hint"><label class="password required" for="` + id + `">` + html.EscapeString(settingsT(loc, "simple_form.labels.defaults.current_password", "Current password")) + filterRequiredMarker(loc) + `</label><span class="hint">` + html.EscapeString(settingsT(loc, "simple_form.hints.defaults.current_password", "For security purposes please enter the password of the current account")) + `</span><div class="label_input"><input autocomplete="current-password" class="password required" required="required" aria-required="true" type="password" name="` + prefix + `[current_password]" id="` + id + `"` + disabled + `></div></div>`
}

func migrationCompactAccountLinkHTML(account models.Account) string {
	display := strings.TrimSpace(account.DisplayName)
	if display == "" {
		display = accountDisplayName(account)
	}
	return `<a class="name-tag" href="` + html.EscapeString(accountWebPath(account)) + `"><span class="username">` + html.EscapeString(display) + `</span><small>@` + html.EscapeString(accountAliasPrettyAcct(account.Acct())) + `</small></a>`
}

func stringArrayContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type migrationInputError string

func (e migrationInputError) Error() string { return string(e) }

const (
	errMigrationInvalidAcct  migrationInputError = "Migration account address is invalid"
	errMigrationNotFound     migrationInputError = "Migration target account was not found"
	errMigrationAlreadyMoved migrationInputError = "This account already redirects to that target"
	errMigrationMoveToSelf   migrationInputError = "You cannot migrate to the current account"
)

func migrationErrorText(locale string, err error) string {
	switch err {
	case errMigrationInvalidAcct:
		return settingsT(locale, "migrations.errors.invalid_acct", err.Error())
	case errMigrationNotFound:
		return settingsT(locale, "migrations.errors.not_found", err.Error())
	case errMigrationAlreadyMoved:
		return settingsT(locale, "migrations.errors.already_moved", err.Error())
	case errMigrationMoveToSelf:
		return settingsT(locale, "migrations.errors.move_to_self", err.Error())
	default:
		return err.Error()
	}
}

func migrationMovedMessage(locale string, acct string) string {
	return settingsTVars(locale, "migrations.moved_msg", "Your account is now redirecting to %{acct} and your followers are being moved over.", map[string]string{"acct": acct})
}

func migrationRedirectedMessage(locale string, acct string) string {
	return settingsTVars(locale, "migrations.redirected_msg", "Your account is now redirecting to %{acct}.", map[string]string{"acct": acct})
}
