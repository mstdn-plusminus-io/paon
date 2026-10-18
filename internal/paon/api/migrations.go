package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/net/idna"
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
	if _, ok := err.(migrationInputError); ok {
		return s.renderSettingsMigrationError(c, *user, *account, migrationErrorText(locale, err), locale)
	}
	if err != nil {
		return err
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
	// MoveService#call: MoveWorker, UpdateDistributionWorker and
	// MoveDistributionWorker run independently, so a failed relationship
	// migration never holds back the Update and Move deliveries.
	if err := s.processLocalAccountMigration(account.ID, target.ID); err != nil {
		log.Printf("level=ERROR event=account_migration_move_failed account_id=%d target_account_id=%d error=%q", account.ID, target.ID, err)
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
	if _, ok := err.(migrationInputError); ok {
		return s.renderSettingsMigrationRedirectError(c, *user, migrationErrorText(locale, err), locale)
	}
	if err != nil {
		return err
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
	if acct == "" {
		return nil, errMigrationNotFound
	}
	if !railsDomainValidatorAcctCompliant(acct) {
		return nil, errMigrationInvalidAcct
	}
	if strings.EqualFold(acct, current.Acct()) || strings.EqualFold(acct, current.Username+"@"+s.cfg.LocalDomain) {
		return nil, errMigrationMoveToSelf
	}
	// AccountMigration and Form::Redirect resolve with skip_cache: true so the
	// target's alsoKnownAs is always checked against fresh origin data.
	account, err := s.resolveSettingsAccount(acct, true)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, errMigrationNotFound
	}
	if current.MovedToAccountID.Valid && current.MovedToAccountID.Int64 == account.ID {
		return nil, errMigrationAlreadyMoved
	}
	if account.ID == current.ID {
		return nil, errMigrationMoveToSelf
	}
	return account, nil
}

// resolveSettingsAccount mirrors ResolveAccountService#call as used by AccountAlias
// (skipCache false) and AccountMigration / Form::Redirect (skipCache true). A nil
// account means Rails would have returned nil (not found, disallowed or gone).
func (s *Server) resolveSettingsAccount(acct string, skipCache bool) (*models.Account, error) {
	if s.db == nil {
		return nil, nil
	}
	// TagManager#local_domain? (and WEB_DOMAIN, which Rails reaches through a
	// WebFinger redirect to this server) resolve to the local account.
	acct = s.resolveInteractionAccountLookupAcct(acct)
	username, domain, remote := strings.Cut(acct, "@")
	if username == "" {
		return nil, nil
	}
	if remote {
		if disallowed, err := s.domainNotAllowed(domain); err != nil || disallowed {
			return nil, err
		}
	}
	now := time.Now().UTC()
	account, err := s.findSettingsResolveAccount(acct)
	if err != nil {
		return nil, err
	}
	if account != nil && (account.Local() || !skipCache && !remoteActivityActorPossiblyStale(*account, now)) {
		return account, nil
	}
	if !remote {
		return nil, nil
	}
	remoteAcct, ok := s.importRemoteAcct(acct)
	if !ok {
		return nil, nil
	}
	actorURL, resolvedAcct, err := fetchActivityActorURLAndAcctFromWebFinger(remoteAcct)
	if err != nil {
		if activityFetchGone(err) && account != nil && !account.Local() {
			_ = s.queueRemoteGoneAccountDeletion(context.Background(), account, now)
		}
		return nil, nil
	}
	resolvedAcct = s.resolveInteractionAccountLookupAcct(resolvedAcct)
	_, resolvedDomain, resolvedRemote := strings.Cut(resolvedAcct, "@")
	if resolvedRemote {
		if disallowed, err := s.domainNotAllowed(resolvedDomain); err != nil || disallowed {
			return nil, err
		}
	}
	if account == nil {
		if account, err = s.findSettingsResolveAccount(resolvedAcct); err != nil {
			return nil, err
		}
	}
	if account != nil && (account.Local() || !skipCache && !remoteActivityActorPossiblyStale(*account, now)) {
		return account, nil
	}
	if !resolvedRemote {
		return nil, nil
	}
	// FetchRemoteActorService#call never fetches a local URI. Rails would return
	// that local account, but it is not the WebFinger-confirmed remote acct (see
	// accountMatchesWebFingerAcct), so it is rejected without a request to this
	// instance.
	if s.localActivityURI(actorURL) || s.remoteActivityActorURIIsLocal(actorURL) {
		return nil, nil
	}
	// FetchRemoteActorService#call returns early for disallowed actor hosts.
	if disallowed, err := s.remoteActivityDomainNotAllowed(actorURL); err != nil || disallowed {
		return nil, err
	}
	fetched, err := s.fetchAndStoreActivityActorForWebFingerDB(s.db, actorURL, resolvedAcct)
	if err != nil || fetched == nil || !accountMatchesWebFingerAcct(fetched, resolvedAcct) {
		return nil, nil
	}
	return fetched, nil
}

func (s *Server) findSettingsResolveAccount(acct string) (*models.Account, error) {
	account, err := s.findAccountByAcct(acct)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return account, nil
}

// accountMatchesWebFingerAcct accepts the fetched actor for the WebFinger-confirmed
// acct. Paon stores remote accounts under the actor host, so split-domain servers
// (LOCAL_DOMAIN != WEB_DOMAIN, GoToSocial account-domain) are accepted only when the
// actor host's WebFinger confirms the same acct and loops back to the actor, like
// FetchRemoteActorService#check_webfinger!.
func accountMatchesWebFingerAcct(account *models.Account, resolvedAcct string) bool {
	if accountMatchesImportAcct(account, resolvedAcct) {
		return true
	}
	if account == nil || account.Local() || account.URI == "" {
		return false
	}
	actorURL, confirmedAcct, err := fetchRemoteActorServiceWebFingerURLAndAcct(account.Username, account.Domain.String)
	return err == nil && actorURL == account.URI && strings.EqualFold(confirmedAcct, resolvedAcct)
}

var railsDomainValidatorLabelPattern = regexp.MustCompile(`^[a-z0-9\-]+$`)

// railsDomainValidatorAcctCompliant mirrors DomainValidator with acct: true:
// `_, domain = value.split('@')` is skipped when blank, otherwise its
// normalized host must be shorter than 256 characters and every label must be
// 1..63 characters of [a-z0-9-].
func railsDomainValidatorAcctCompliant(acct string) bool {
	// Ruby's String#split drops trailing empty fields.
	parts := strings.Split(acct, "@")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 || parts[1] == "" {
		return true
	}
	host := parts[1]
	if strings.IndexFunc(host, func(r rune) bool { return r >= utf8.RuneSelf }) >= 0 {
		ascii, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return false
		}
		host = ascii
	}
	host = strings.ToLower(host)
	if len(host) >= 256 {
		return false
	}
	labels := strings.Split(host, ".")
	for len(labels) > 0 && labels[len(labels)-1] == "" {
		labels = labels[:len(labels)-1]
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || !railsDomainValidatorLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
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

// processLocalAccountMigration mirrors MoveWorker.perform_async: the local
// relationship migration runs in the move task, or inline when it cannot be
// enqueued.
func (s *Server) processLocalAccountMigration(sourceAccountID int64, targetAccountID int64) error {
	if s.db == nil {
		return nil
	}
	if s.enqueueMoveTask(sourceAccountID, targetAccountID) {
		return nil
	}
	return s.performMoveWorker(context.Background(), sourceAccountID, targetAccountID)
}

// performMoveWorker mirrors MoveWorker#perform.
func (s *Server) performMoveWorker(ctx context.Context, sourceAccountID int64, targetAccountID int64) error {
	if s == nil || s.db == nil {
		return nil
	}
	var source models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", sourceAccountID).First(&source).Error; err != nil {
		return workerLookupError("move source account lookup", err)
	}
	var target models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", targetAccountID).First(&target).Error; err != nil {
		return workerLookupError("move target account lookup", err)
	}
	if source.Local() && target.Local() {
		if err := s.rewriteLocalAccountMigrationFollows(ctx, source.ID, target.ID); err != nil {
			return err
		}
	} else if err := s.migrateRemoteAccountMigrationFollowers(source, target); err != nil {
		// MoveWorker#perform clears @deferred_error after queue_follow_unfollows!, so a
		// failed follower migration never retries the move (which would copy the
		// account notes again).
		log.Printf("level=ERROR event=account_migration_followers_failed account_id=%d target_account_id=%d error=%q", source.ID, target.ID, err)
	}
	return s.carryAccountMigrationRelationships(source, target)
}

func (s *Server) rewriteLocalAccountMigrationFollows(ctx context.Context, sourceAccountID int64, targetAccountID int64) error {
	now := time.Now().UTC()
	var authorized []models.Account
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		authorized, err = s.approveTargetFollowRequestsFromSourceFollowers(tx, sourceAccountID, targetAccountID, now)
		if err != nil {
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
	}); err != nil {
		return err
	}
	// FollowRequest#authorize!: follow! (remove_potential_friendship) and
	// MergeWorker for local followers.
	for _, follower := range authorized {
		s.accountMigrationFollowCacheEffects(ctx, follower, targetAccountID)
		if follower.Local() {
			s.mergeAfterDirectFollowBestEffort(ctx, targetAccountID, follower)
		}
	}
	return nil
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

// approveTargetFollowRequestsFromSourceFollowers mirrors the first stage of
// MoveWorker#rewrite_follows!: FollowRequest#authorize! for every follower of
// the source account, local or remote, with a pending request to the target.
// It returns the authorized followers for the post-commit effects.
func (s *Server) approveTargetFollowRequestsFromSourceFollowers(tx *gorm.DB, sourceAccountID int64, targetAccountID int64, now time.Time) ([]models.Account, error) {
	var requests []models.FollowRequest
	if err := tx.Model(&models.FollowRequest{}).
		Preload("Account").
		Joins("JOIN follows source_follows ON source_follows.account_id = follow_requests.account_id AND source_follows.target_account_id = ?", sourceAccountID).
		Where("follow_requests.target_account_id = ?", targetAccountID).
		Find(&requests).Error; err != nil {
		return nil, err
	}
	authorized := make([]models.Account, 0, len(requests))
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
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			if err := tx.Model(&models.ListAccount{}).Where("follow_request_id = ?", request.ID).Updates(map[string]any{
				"account_id":        targetAccountID,
				"follow_id":         follow.ID,
				"follow_request_id": nil,
			}).Error; err != nil {
				return nil, err
			}
			if err := incrementAccountStatCounter(tx, request.AccountID, accountStatCounterFollowing, 1); err != nil {
				return nil, err
			}
			if err := incrementAccountStatCounter(tx, targetAccountID, accountStatCounterFollowers, 1); err != nil {
				return nil, err
			}
		}
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", request.ID).Delete(&models.Notification{}).Error; err != nil {
			return nil, err
		}
		if err := tx.Delete(&models.FollowRequest{}, request.ID).Error; err != nil {
			return nil, err
		}
		authorized = append(authorized, request.Account)
	}
	return authorized, nil
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
	var oldTarget models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", oldTargetAccountID).First(&oldTarget).Error; err != nil {
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
	if err := s.db.WithContext(ctx).Where("account_id = ? AND target_account_id = ?", followerAccountID, oldTargetAccountID).First(&follow).Error; err != nil {
		return nil
	}
	// FollowService#call: request_follow! when the target is locked (unless
	// bypassed), the follower is silenced or the target is remote; otherwise
	// direct_follow! for a local target.
	if !(target.Locked && !bypassLocked) && !follower.SilencedAt.Valid && target.Local() {
		newFollow, created, err := s.createDirectMigrationFollow(follow, target)
		if err != nil {
			return err
		}
		s.accountMigrationFollowCacheEffects(ctx, follower, target.ID)
		if !created {
			return nil
		}
		s.activityTrackerIncrementBasic(ctx, "activity:interactions", time.Now().UTC(), 1)
		// FollowService#direct_follow!: LocalNotificationWorker 'follow' and MergeWorker.
		if err := s.notifyAccountMigrationFollow(ctx, follower, target, newFollow.ID, "Follow", "follow"); err != nil {
			return err
		}
		s.queueMeiliPrivateStatusReindexBestEffort(ctx, target.ID)
		s.mergeAfterDirectFollowBestEffort(ctx, target.ID, follower)
		return s.unfollowAccountMigrationOldTarget(ctx, follower, oldTarget)
	}
	request, created, err := s.createMigrationFollowRequest(follow, target)
	if err != nil {
		return err
	}
	if !created {
		s.accountMigrationFollowCacheEffects(ctx, follower, target.ID)
		return nil
	}
	s.accountMigrationFollowRequestCacheEffects(ctx, follower, target.ID)
	s.activityTrackerIncrementBasic(ctx, "activity:interactions", time.Now().UTC(), 1)
	if target.Local() {
		if err := s.notifyAccountMigrationFollow(ctx, follower, target, request.ID, "FollowRequest", "follow_request"); err != nil {
			return err
		}
		return s.unfollowAccountMigrationOldTarget(ctx, follower, oldTarget)
	}
	// ActivityPub::MigratedFollowDeliveryWorker unfollows the old target only
	// after the Follow has been delivered (afterActivityPubDeliveryRetrySuccess).
	_ = s.deliverActivityPubMigratedFollow(follower, target, request.ID, string(request.URI), oldTargetAccountID)
	return nil
}

func (s *Server) accountMigrationFollowCacheEffects(ctx context.Context, follower models.Account, targetID int64) {
	s.removePotentialFriendship(ctx, follower.ID, targetID)
	s.invalidateFollowRelationshipCaches(ctx, follower, targetID)
}

func (s *Server) accountMigrationFollowRequestCacheEffects(ctx context.Context, follower models.Account, targetID int64) {
	s.removePotentialFriendship(ctx, follower.ID, targetID)
	s.invalidateRelationshipCaches(ctx, follower.ID, targetID)
}

func (s *Server) notifyAccountMigrationFollow(ctx context.Context, follower models.Account, target models.Account, activityID int64, activityType string, notificationType string) error {
	notificationIDs, err := s.enqueueOrCreateLocalNotifications(ctx, []asynqLocalNotificationPayload{{ReceiverAccountID: target.ID, FromAccountID: follower.ID, ActivityID: activityID, ActivityType: activityType, Type: notificationType}})
	if err != nil {
		return err
	}
	s.publishNotificationIDs(notificationIDs)
	return nil
}

// unfollowAccountMigrationOldTarget mirrors UnfollowService.new.call(source,
// old_target, skip_unmerge: true) as called by FollowMigrationService and
// ActivityPub::MigratedFollowDeliveryWorker: the Follow (or else the
// FollowRequest) is destroyed and an Undo Follow is delivered to a remote old
// target. The home feed is not unmerged.
func (s *Server) unfollowAccountMigrationOldTarget(ctx context.Context, source models.Account, oldTarget models.Account) error {
	acquired, releaseRelationshipLock, err := s.acquireActivityPubRedisLock(ctx, relationshipRedisLockName(source.ID, oldTarget.ID), 15*time.Minute)
	if err != nil {
		return err
	}
	if !acquired {
		// UnfollowService#call raises when the relationship lock is held, so the
		// worker retries instead of racing a concurrent unfollow.
		return fmt.Errorf("relationship %d -> %d is already being changed", source.ID, oldTarget.ID)
	}
	defer releaseRelationshipLock()
	var undoID int64
	var undoURI string
	deliverUndo := false
	followDeleted := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		follow, _, deleted, err := deleteFollowEdgeReturningFollow(tx, source.ID, oldTarget.ID)
		if err != nil {
			return err
		}
		if deleted {
			followDeleted = true
			undoID, undoURI = follow.ID, string(follow.URI)
			deliverUndo = !oldTarget.Local() && oldTarget.Protocol == 1
			return nil
		}
		var request models.FollowRequest
		err = tx.Where("account_id = ? AND target_account_id = ?", source.ID, oldTarget.ID).First(&request).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", request.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if _, err := deleteListAccountsForRejectedFollowRequest(tx, request.ID); err != nil {
			return err
		}
		if err := tx.Delete(&request).Error; err != nil {
			return err
		}
		undoID, undoURI = request.ID, string(request.URI)
		deliverUndo = !oldTarget.Local()
		return nil
	})
	if err != nil {
		return err
	}
	s.invalidateFollowRelationshipCaches(ctx, source, oldTarget.ID)
	if followDeleted {
		s.queueMeiliPrivateStatusReindexBestEffort(ctx, oldTarget.ID)
	}
	if deliverUndo {
		if undoURI == "" {
			undoURI = activityPubFollowURI(s, source, undoID)
		}
		_ = s.deliverActivityPubUndoFollow(source, oldTarget, undoID, undoURI)
	}
	return nil
}

func (s *Server) createDirectMigrationFollow(original models.Follow, target models.Account) (*models.Follow, bool, error) {
	now := time.Now().UTC()
	var follow models.Follow
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		existing, err := changeAccountMigrationFollowOptions(tx, original, target.ID, now)
		if err != nil || existing {
			return err
		}
		follow = models.Follow{
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
	if err != nil {
		return nil, false, err
	}
	return &follow, created, nil
}

func (s *Server) createMigrationFollowRequest(original models.Follow, target models.Account) (*models.FollowRequest, bool, error) {
	now := time.Now().UTC()
	var request models.FollowRequest
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		existing, err := changeAccountMigrationFollowOptions(tx, original, target.ID, now)
		if err != nil || existing {
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

// changeAccountMigrationFollowOptions mirrors FollowMigrationService's
// change_follow_options! / change_follow_request_options!: when the follower
// already follows or has requested the target, the original follow's list
// memberships are copied and follow! / request_follow! overwrite the existing
// options (languages only when the original follow has them). The old follow is
// kept.
func changeAccountMigrationFollowOptions(tx *gorm.DB, original models.Follow, targetAccountID int64, now time.Time) (bool, error) {
	updates := followPayloadUpdates(followPayload{
		ShowReblogs:    original.ShowReblogs,
		HasShowReblogs: true,
		Notify:         original.Notify,
		HasNotify:      true,
		Languages:      original.Languages,
		HasLanguages:   original.Languages != nil,
	}, now)
	var follow models.Follow
	err := tx.Where("account_id = ? AND target_account_id = ?", original.AccountID, targetAccountID).First(&follow).Error
	if err == nil {
		if err := migrateListAccountsForAccountMigration(tx, original.ID, targetAccountID, sql.NullInt64{Int64: follow.ID, Valid: true}, sql.NullInt64{}); err != nil {
			return false, err
		}
		return true, tx.Model(&follow).Updates(updates).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	var request models.FollowRequest
	err = tx.Where("account_id = ? AND target_account_id = ?", original.AccountID, targetAccountID).First(&request).Error
	if err == nil {
		if err := migrateListAccountsForAccountMigration(tx, original.ID, targetAccountID, sql.NullInt64{}, sql.NullInt64{Int64: request.ID, Valid: true}); err != nil {
			return false, err
		}
		return true, tx.Model(&request).Updates(updates).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	return false, nil
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
	defaultLocale := s.cfg.Locale()
	var notePairs []accountRelationshipPair
	var blockEffects []accountMigrationBlockEffect
	var mutePairs []accountMigrationMuteEffect
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		notePairs, err = copyAccountMigrationNotes(tx, defaultLocale, source.ID, target.ID, sourceAcct, now)
		if err != nil {
			return err
		}
		blockEffects, err = s.carryAccountMigrationBlocks(tx, defaultLocale, source.ID, target, sourceAcct, now)
		if err != nil {
			return err
		}
		var muteNotePairs []accountRelationshipPair
		mutePairs, muteNotePairs, err = carryAccountMigrationMutes(tx, defaultLocale, source.ID, target.ID, sourceAcct, now)
		notePairs = append(notePairs, muteNotePairs...)
		return err
	}); err != nil {
		return err
	}
	ctx := context.Background()
	for _, pair := range uniqueAccountRelationshipPairs(notePairs) {
		s.invalidateRelationshipCaches(ctx, pair.accountID, pair.targetID)
	}
	// BlockService#call side effects, in the same order as blockAccount. The
	// private statuses are reindexed once per account after the loop instead of
	// reindexing the target for every blocker.
	reindexAccountIDs := make([]int64, 0, len(blockEffects)+1)
	for _, effect := range blockEffects {
		s.clearAfterBlockFeedCaches(ctx, effect.blocker.ID, target.ID)
		s.removePotentialFriendship(ctx, effect.blocker.ID, target.ID)
		s.applyAccountBlockRelationshipCleanupEffects(ctx, effect.blocker, target, effect.cleanup)
		s.invalidateBlockRelationshipCaches(ctx, effect.blocker.ID, target.ID)
		reindexAccountIDs = append(reindexAccountIDs, effect.blocker.ID, target.ID)
		_ = s.deliverActivityPubBlock(effect.blocker, target, effect.block.ID, string(effect.block.URI))
	}
	s.queueMeiliPrivateStatusReindexBestEffort(ctx, reindexAccountIDs...)
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

type accountMigrationBlockEffect struct {
	blocker models.Account
	block   models.Block
	cleanup accountBlockRelationshipCleanup
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

// accountNoteCommentMaxLength mirrors AccountNote's
// `validates :comment, length: { maximum: 2_000 }`.
const accountNoteCommentMaxLength = 2000

func accountNoteCommentValid(comment string) bool {
	return utf8.RuneCountInString(comment) <= accountNoteCommentMaxLength
}

// copyAccountMigrationNotes mirrors MoveWorker#copy_account_notes!.
func copyAccountMigrationNotes(tx *gorm.DB, defaultLocale string, sourceAccountID int64, targetAccountID int64, sourceAcct string, now time.Time) ([]accountRelationshipPair, error) {
	var notes []models.AccountNote
	if err := tx.Where("target_account_id = ?", sourceAccountID).Find(&notes).Error; err != nil {
		return nil, err
	}
	affected := []accountRelationshipPair{}
	for _, note := range notes {
		if !note.AccountID.Valid || note.AccountID.Int64 == 0 {
			continue
		}
		accountID := note.AccountID.Int64
		locale, err := accountMigrationNoteLocale(tx, accountID, defaultLocale)
		if err != nil {
			return nil, err
		}
		text := accountMigrationNoteText(locale, "copy_account_note_text", sourceAcct)
		var existing models.AccountNote
		err = tx.Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// [text, note.comment].join("\n"), falling back to the bare comment
			// when the prefixed one fails validation.
			comment := text + "\n" + note.Comment
			if !accountNoteCommentValid(comment) {
				comment = note.Comment
			}
			if !accountNoteCommentValid(comment) {
				continue
			}
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
		// [text, note.comment, "\n", new_note.comment].join("\n"); an invalid
		// merged note is left unchanged.
		merged := strings.Join([]string{text, note.Comment, "\n", existing.Comment}, "\n")
		if !accountNoteCommentValid(merged) {
			continue
		}
		if err := tx.Model(&existing).Updates(map[string]any{"comment": merged, "updated_at": now}).Error; err != nil {
			return nil, err
		}
		affected = append(affected, accountRelationshipPair{accountID: accountID, targetID: targetAccountID})
	}
	return affected, nil
}

// carryAccountMigrationBlocks mirrors MoveWorker#carry_blocks_over!, which
// blocks the target through BlockService. The returned effects run after the
// transaction commits.
func (s *Server) carryAccountMigrationBlocks(tx *gorm.DB, defaultLocale string, sourceAccountID int64, target models.Account, sourceAcct string, now time.Time) ([]accountMigrationBlockEffect, error) {
	var blocks []models.Block
	if err := tx.Model(&models.Block{}).
		Joins("JOIN accounts ON accounts.id = blocks.account_id").
		Where("blocks.target_account_id = ? AND (accounts.domain IS NULL OR accounts.domain = '')", sourceAccountID).
		Find(&blocks).Error; err != nil {
		return nil, err
	}
	affected := []accountMigrationBlockEffect{}
	for _, block := range blocks {
		exists, err := accountMigrationRelationshipExists(tx, &models.Block{}, block.AccountID, target.ID)
		if err != nil {
			return nil, err
		}
		following, err := accountMigrationRelationshipExists(tx, &models.Follow{}, block.AccountID, target.ID)
		if err != nil {
			return nil, err
		}
		if exists || following {
			continue
		}
		// BlockService#call returns early for a self-block.
		if block.AccountID != target.ID {
			var blocker models.Account
			if err := tx.Where("id = ?", block.AccountID).First(&blocker).Error; err != nil {
				return nil, err
			}
			cleanup, err := cleanupAccountBlockRelationships(tx, blocker, target)
			if err != nil {
				return nil, err
			}
			carried, err := s.createAccountBlock(tx, blocker.ID, target.ID, now)
			if err != nil {
				return nil, err
			}
			affected = append(affected, accountMigrationBlockEffect{blocker: blocker, block: *carried, cleanup: cleanup})
		}
		if _, err := createAccountMigrationNoteIfMissing(tx, defaultLocale, block.AccountID, target.ID, "carry_blocks_over_text", sourceAcct, now); err != nil {
			return nil, err
		}
	}
	return affected, nil
}

// carryAccountMigrationMutes mirrors MoveWorker#carry_mutes_over!: MuteService
// creates a mute without expiry unless the account already mutes or follows
// the target, and the note is added either way. It returns the created mutes
// and the accounts that got a new note.
func carryAccountMigrationMutes(tx *gorm.DB, defaultLocale string, sourceAccountID int64, targetAccountID int64, sourceAcct string, now time.Time) ([]accountMigrationMuteEffect, []accountRelationshipPair, error) {
	var mutes []models.Mute
	if err := tx.Model(&models.Mute{}).
		Joins("JOIN accounts ON accounts.id = mutes.account_id").
		Where("mutes.target_account_id = ? AND (accounts.domain IS NULL OR accounts.domain = '')", sourceAccountID).
		Find(&mutes).Error; err != nil {
		return nil, nil, err
	}
	affected := []accountMigrationMuteEffect{}
	notes := []accountRelationshipPair{}
	for _, mute := range mutes {
		exists, err := accountMigrationRelationshipExists(tx, &models.Mute{}, mute.AccountID, targetAccountID)
		if err != nil {
			return nil, nil, err
		}
		following, err := accountMigrationRelationshipExists(tx, &models.Follow{}, mute.AccountID, targetAccountID)
		if err != nil {
			return nil, nil, err
		}
		// MuteService#call returns early for a self-mute.
		if !exists && !following && mute.AccountID != targetAccountID {
			if err := upsertAccountMute(tx, mute.AccountID, targetAccountID, mute.HideNotifications, now); err != nil {
				return nil, nil, err
			}
			if mute.HideNotifications {
				if err := afterBlockServiceCleanup(tx, mute.AccountID, targetAccountID); err != nil {
					return nil, nil, err
				}
			}
			affected = append(affected, accountMigrationMuteEffect{accountID: mute.AccountID, targetID: targetAccountID, hideNotifications: mute.HideNotifications})
		}
		created, err := createAccountMigrationNoteIfMissing(tx, defaultLocale, mute.AccountID, targetAccountID, "carry_mutes_over_text", sourceAcct, now)
		if err != nil {
			return nil, nil, err
		}
		if created {
			notes = append(notes, accountRelationshipPair{accountID: mute.AccountID, targetID: targetAccountID})
		}
	}
	return affected, notes, nil
}

func accountMigrationRelationshipExists(tx *gorm.DB, model any, accountID int64, targetAccountID int64) (bool, error) {
	var count int64
	if err := tx.Model(model).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// createAccountMigrationNoteIfMissing mirrors MoveWorker#add_account_note_if_needed!.
func createAccountMigrationNoteIfMissing(tx *gorm.DB, defaultLocale string, accountID int64, targetAccountID int64, key string, sourceAcct string, now time.Time) (bool, error) {
	var count int64
	if err := tx.Model(&models.AccountNote{}).Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	locale, err := accountMigrationNoteLocale(tx, accountID, defaultLocale)
	if err != nil {
		return false, err
	}
	note := models.AccountNote{AccountID: models.AccountNoteAccountID(accountID), TargetAccountID: models.AccountNoteAccountID(targetAccountID), Comment: accountMigrationNoteText(locale, key, sourceAcct), CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&note).Error; err != nil {
		return false, err
	}
	return true, nil
}

// accountMigrationNoteLocale mirrors MoveWorker's
// `account.user&.locale.presence || I18n.default_locale`.
func accountMigrationNoteLocale(tx *gorm.DB, accountID int64, defaultLocale string) (string, error) {
	var locales []sql.NullString
	if err := tx.Model(&models.User{}).Where("account_id = ?", accountID).Limit(1).Pluck("locale", &locales).Error; err != nil {
		return "", err
	}
	if len(locales) > 0 && locales[0].Valid && strings.TrimSpace(locales[0].String) != "" {
		return locales[0].String, nil
	}
	return defaultLocale, nil
}

// accountMigrationNoteText translates a move_handler.* note text.
func accountMigrationNoteText(locale string, key string, sourceAcct string) string {
	fallback := "This user moved from %{acct}."
	switch key {
	case "copy_account_note_text":
		fallback = "This user moved from %{acct}, here were your previous notes about them:"
	case "carry_blocks_over_text":
		fallback = "This user moved from %{acct}, which you had blocked."
	case "carry_mutes_over_text":
		fallback = "This user moved from %{acct}, which you had muted."
	}
	return settingsTVars(locale, "move_handler."+key, fallback, map[string]string{"acct": sourceAcct})
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
		// DomainValidator adds the built-in :invalid error since Mastodon 4.3.
		return settingsT(locale, "errors.messages.invalid", "is invalid")
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
