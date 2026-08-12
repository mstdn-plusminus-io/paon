package api

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) relationshipsPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	filters, err := relationshipPageFilters(c)
	if err != nil {
		return err
	}
	accounts, err := s.relationshipPageAccounts(account.ID, user.ID, filters, limit(c, 40, 80))
	if err != nil {
		return err
	}
	interrelationships, err := s.relationshipPageInterrelationshipsForAccounts(account.ID, accounts)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	settings := decodeUserSettings(user.Settings.String)
	theme := settingsWebTheme(settings)
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	view := relationshipPageView{
		Config:             s.cfg,
		Accounts:           accounts,
		Interrelationships: interrelationships,
		Filters:            filters,
		AutoPlayGIF:        rawBool(settings["web.auto_play"], false),
	}
	return c.HTML(http.StatusOK, relationshipsHTML(view, c.QueryParam("notice"), c.QueryParam("error"), append([]string{relationshipFilterHiddenFields(c)}, renderArgs...)...))
}

func (s *Server) updateRelationshipsPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	if c.Request().Method == http.MethodPost && !methodOverrideIs(c, "patch", "put", "post") {
		return c.Redirect(http.StatusFound, relationshipBatchRedirectPath(c, "", ""))
	}
	targetIDs := formInt64Values(c, "form_account_batch[account_ids][]")
	action := relationshipBatchAction(c)
	if len(targetIDs) > 0 && action != "" {
		if err := s.applyRelationshipBatch(c.Request().Context(), account.ID, targetIDs, action); err != nil {
			return c.Redirect(http.StatusFound, relationshipBatchRedirectPath(c, relationshipBatchErrorKey(action), relationshipBatchErrorMessage(s.webLocale(c, user), action)))
		}
	}
	return c.Redirect(http.StatusFound, relationshipBatchRedirectPath(c, "", ""))
}

func relationshipPageFilter(c *echo.Context) string {
	filters, err := relationshipPageFilters(c)
	if err != nil {
		return ""
	}
	return filters.Relationship
}

type relationshipFilters struct {
	Relationship string
	Status       string
	ByDomain     string
	Activity     string
	Order        string
	Location     string
}

func relationshipPageFilters(c *echo.Context) (relationshipFilters, error) {
	value := relationshipRequestParam(c, "relationship")
	if value == "" {
		value = "following"
	}
	switch value {
	case "following", "followed_by", "mutual", "invited":
	default:
		return relationshipFilters{}, relationshipInvalidFilterError("relationship", value)
	}
	order := relationshipRequestParam(c, "order")
	if order == "" {
		order = "recent"
	}
	switch order {
	case "active", "recent":
	default:
		return relationshipFilters{}, relationshipInvalidFilterError("order", order)
	}
	filters := relationshipFilters{Relationship: value, Order: order}
	if status := relationshipRequestParam(c, "status"); status != "" {
		switch status {
		case "moved", "primary":
			filters.Status = status
		default:
			return relationshipFilters{}, relationshipInvalidFilterError("status", status)
		}
	}
	if location := relationshipRequestParam(c, "location"); location != "" {
		switch location {
		case "local", "remote":
			filters.Location = location
		default:
			return relationshipFilters{}, relationshipInvalidFilterError("location", location)
		}
	}
	if activity := relationshipRequestParam(c, "activity"); activity != "" {
		switch activity {
		case "dormant":
			filters.Activity = activity
		default:
			return relationshipFilters{}, relationshipInvalidFilterError("activity", activity)
		}
	}
	filters.ByDomain = relationshipRequestParam(c, "by_domain")
	return filters, nil
}

func relationshipInvalidFilterError(name string, value string) error {
	return apiHTTPError{status: http.StatusBadRequest, message: "Unknown " + name + ": " + value}
}

func relationshipRequestParam(c *echo.Context, key string) string {
	if value := strings.TrimSpace(c.QueryParam(key)); value != "" {
		return value
	}
	return strings.TrimSpace(c.FormValue(key))
}

func relationshipFilterHiddenFields(c *echo.Context) string {
	var out strings.Builder
	page := relationshipRequestParam(c, "page")
	if page == "" {
		page = "1"
	}
	out.WriteString(`<input type="hidden" name="page" value="`)
	out.WriteString(html.EscapeString(page))
	out.WriteString(`">`)
	for _, key := range []string{"relationship", "status", "by_domain", "activity", "order", "location"} {
		if value := relationshipRequestParam(c, key); value != "" {
			out.WriteString(`<input type="hidden" name="`)
			out.WriteString(html.EscapeString(key))
			out.WriteString(`" value="`)
			out.WriteString(html.EscapeString(value))
			out.WriteString(`">`)
		}
	}
	return out.String()
}

func applyRelationshipFilters(query *gorm.DB, filters relationshipFilters, now time.Time) *gorm.DB {
	switch filters.Status {
	case "moved":
		query = query.Where("accounts.moved_to_account_id IS NOT NULL")
	case "primary":
		query = query.Where("accounts.moved_to_account_id IS NULL")
	}
	switch filters.Location {
	case "local":
		query = query.Where("(accounts.domain IS NULL OR accounts.domain = '')")
	case "remote":
		query = query.Where("accounts.domain IS NOT NULL AND accounts.domain <> ''")
	}
	if filters.ByDomain != "" {
		query = query.Where("accounts.domain = ?", filters.ByDomain)
	}
	if filters.Activity == "dormant" {
		query = query.Where("(account_stats.last_status_at IS NULL OR account_stats.last_status_at < ?)", now.AddDate(0, -1, 0))
	}
	switch filters.Order {
	case "active":
		query = query.Order("account_stats.last_status_at DESC NULLS LAST").Order("accounts.id DESC")
	case "recent":
		if filters.Relationship == "invited" {
			query = query.Order("accounts.id DESC")
		} else {
			query = query.Order("relationship_sort.id DESC")
		}
	default:
		query = query.Order("relationship_sort.id DESC")
	}
	return query
}

func relationshipBatchAction(c *echo.Context) string {
	switch {
	case relationshipFormParamExists(c, "follow"):
		return "follow"
	case relationshipFormParamExists(c, "unfollow"):
		return "unfollow"
	case relationshipFormParamExists(c, "remove_from_followers"):
		return "remove_from_followers"
	case relationshipFormParamExists(c, "block_domains"), relationshipFormParamExists(c, "remove_domains_from_followers"):
		return "remove_domains_from_followers"
	default:
		return ""
	}
}

func relationshipFormParamExists(c *echo.Context, key string) bool {
	_ = c.Request().ParseForm()
	_, ok := c.Request().Form[key]
	return ok
}

func relationshipBatchErrorKey(action string) string {
	if action == "follow" {
		return "error"
	}
	return ""
}

func relationshipBatchErrorMessage(locale string, action string) string {
	if action == "follow" {
		return settingsT(locale, "relationships.follow_failure", "Could not follow selected accounts")
	}
	return ""
}

func relationshipBatchRedirectPath(c *echo.Context, messageKey string, message string) string {
	values := url.Values{}
	for _, key := range []string{"page", "relationship", "status", "by_domain", "activity", "order", "location"} {
		value := strings.TrimSpace(firstNonEmpty(c.FormValue(key), c.QueryParam(key)))
		if value != "" {
			values.Set(key, value)
		}
	}
	if messageKey != "" && message != "" {
		values.Set(messageKey, message)
	}
	if query := values.Encode(); query != "" {
		return "/relationships?" + query
	}
	return "/relationships"
}

func (s *Server) relationshipPageAccounts(accountID int64, userID int64, filters relationshipFilters, limit int) ([]models.Account, error) {
	query := s.relationshipPageBaseQuery(accountID, userID, filters).Limit(limit)
	var accounts []models.Account
	if err := query.Preload("AccountStat").Find(&accounts).Error; err != nil {
		return nil, err
	}
	for i := range accounts {
		if err := s.hydrateAccountCustomEmojis(&accounts[i]); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}

type relationshipPageInterrelationships struct {
	Following  map[int64]bool
	FollowedBy map[int64]bool
}

func (s *Server) relationshipPageInterrelationshipsForAccounts(accountID int64, accounts []models.Account) (relationshipPageInterrelationships, error) {
	accountIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account.ID != 0 {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	if s == nil || s.db == nil || accountID == 0 || len(accountIDs) == 0 {
		return relationshipInterrelationshipsFromFollows(accountID, nil), nil
	}

	var follows []models.Follow
	err := s.db.
		Select("account_id", "target_account_id").
		Where("(account_id = ? AND target_account_id IN ?) OR (target_account_id = ? AND account_id IN ?)", accountID, accountIDs, accountID, accountIDs).
		Find(&follows).Error
	return relationshipInterrelationshipsFromFollows(accountID, follows), err
}

func relationshipInterrelationshipsFromFollows(accountID int64, follows []models.Follow) relationshipPageInterrelationships {
	result := relationshipPageInterrelationships{
		Following:  map[int64]bool{},
		FollowedBy: map[int64]bool{},
	}
	for _, follow := range follows {
		if follow.AccountID == accountID {
			result.Following[follow.TargetAccountID] = true
		}
		if follow.TargetAccountID == accountID {
			result.FollowedBy[follow.AccountID] = true
		}
	}
	return result
}

func (s *Server) relationshipPageBaseQuery(accountID int64, userID int64, filters relationshipFilters) *gorm.DB {
	query := s.db.Model(&models.Account{}).
		Select("accounts.*").
		Joins("LEFT JOIN account_stats ON account_stats.account_id = accounts.id").
		Where("accounts.suspended_at IS NULL")
	switch filters.Relationship {
	case "followed_by":
		query = query.Joins("JOIN follows relationship_sort ON relationship_sort.account_id = accounts.id").Where("relationship_sort.target_account_id = ?", accountID)
	case "mutual":
		query = query.
			Joins("JOIN follows relationship_sort ON relationship_sort.account_id = accounts.id AND relationship_sort.target_account_id = ?", accountID).
			Joins("JOIN follows following_edge ON following_edge.account_id = ? AND following_edge.target_account_id = accounts.id", accountID)
	case "invited":
		query = query.
			Joins("JOIN users relationship_users ON relationship_users.account_id = accounts.id").
			Joins("JOIN invites relationship_sort ON relationship_sort.id = relationship_users.invite_id").
			Where("relationship_sort.user_id = ?", userID)
	default:
		query = query.Joins("JOIN follows relationship_sort ON relationship_sort.target_account_id = accounts.id").Where("relationship_sort.account_id = ?", accountID)
	}
	return applyRelationshipFilters(query, filters, time.Now().UTC())
}

func (s *Server) applyRelationshipBatch(ctx context.Context, accountID int64, targetIDs []int64, action string) error {
	var current models.Account
	if err := s.db.Where("id = ?", accountID).First(&current).Error; err != nil {
		return err
	}
	var targets []models.Account
	if err := s.db.Where("id IN ? AND suspended_at IS NULL", targetIDs).Find(&targets).Error; err != nil {
		return err
	}
	deliveries := []relationshipBatchDelivery{}
	notificationPayloads := []asynqLocalNotificationPayload{}
	followCacheEffects := []followRelationshipCacheEffect{}
	// unfollow unmerges each unfollowed target from the source account's home feed
	// (Rails UnfollowService -> UnmergeWorker). remove_from_followers and
	// remove_domains_from_followers mirror Rails RemoveFromFollowersService, which does
	// NOT enqueue an UnmergeWorker, so they perform no feed cleanup here.
	unmergeTargets := []int64{}
	listUnmerges := []accountBlockUnmerge{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, target := range targets {
			if target.ID == accountID {
				continue
			}
			switch action {
			case "follow":
				delivery, notificationPayload, err := s.createRelationshipFollow(ctx, tx, current, target, time.Now().UTC())
				if err != nil {
					return err
				}
				deliveries = appendRelationshipBatchDelivery(deliveries, delivery)
				notificationPayloads = appendRelationshipBatchNotificationPayload(notificationPayloads, notificationPayload)
			case "unfollow":
				delivery, affectedListIDs, deleted, err := s.deleteRelationshipFollowForBatch(tx, current, target)
				if err != nil {
					return err
				}
				deliveries = appendRelationshipBatchDelivery(deliveries, delivery)
				if deleted {
					unmergeTargets = append(unmergeTargets, target.ID)
					listUnmerges = append(listUnmerges, accountBlockUnmerge{FromAccountID: target.ID, ListIDs: affectedListIDs})
					followCacheEffects = append(followCacheEffects, followRelationshipCacheEffect{Source: current, TargetID: target.ID})
				}
			case "remove_from_followers":
				delivery, effect, err := removeFollowerForBatch(tx, current, target)
				if err != nil {
					return err
				}
				deliveries = appendRelationshipBatchDelivery(deliveries, delivery)
				followCacheEffects = appendFollowRelationshipCacheEffect(followCacheEffects, effect)
			case "remove_domains_from_followers":
				if target.Domain.Valid && target.Domain.String != "" {
					domainDeliveries, domainEffects, err := deleteFollowersByDomain(tx, current, target.Domain.String)
					if err != nil {
						return err
					}
					deliveries = append(deliveries, domainDeliveries...)
					followCacheEffects = append(followCacheEffects, domainEffects...)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, targetID := range uniqueInt64s(unmergeTargets) {
		s.unmergeAfterUnfollowBestEffort(ctx, targetID, current)
	}
	for _, effect := range listUnmerges {
		s.unmergeListFeedsAfterUnfollowBestEffort(ctx, effect.FromAccountID, effect.ListIDs)
	}
	for _, effect := range followCacheEffects {
		s.invalidateFollowRelationshipCaches(ctx, effect.Source, effect.TargetID)
	}
	notificationIDs, err := s.enqueueOrCreateLocalNotifications(ctx, notificationPayloads)
	if err != nil {
		return err
	}
	s.publishNotificationIDs(notificationIDs)
	switch action {
	case "follow", "unfollow":
		s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, targetIDs...)
	case "remove_from_followers", "remove_domains_from_followers":
		s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, accountID)
	}
	s.deliverRelationshipBatchDeliveries(deliveries)
	return nil
}

type relationshipBatchDelivery struct {
	Kind   string
	Local  models.Account
	Remote models.Account
	ID     int64
	URI    string
}

func appendRelationshipBatchDelivery(deliveries []relationshipBatchDelivery, delivery *relationshipBatchDelivery) []relationshipBatchDelivery {
	if delivery == nil {
		return deliveries
	}
	return append(deliveries, *delivery)
}

func appendRelationshipBatchNotificationPayload(payloads []asynqLocalNotificationPayload, payload *asynqLocalNotificationPayload) []asynqLocalNotificationPayload {
	if payload == nil {
		return payloads
	}
	return append(payloads, *payload)
}

func appendFollowRelationshipCacheEffect(effects []followRelationshipCacheEffect, effect *followRelationshipCacheEffect) []followRelationshipCacheEffect {
	if effect == nil {
		return effects
	}
	return append(effects, *effect)
}

func (s *Server) createRelationshipFollow(ctx context.Context, tx *gorm.DB, current models.Account, target models.Account, now time.Time) (*relationshipBatchDelivery, *asynqLocalNotificationPayload, error) {
	if exists, err := relationshipFollowOrRequestExists(tx, current.ID, target.ID); err != nil || exists {
		return nil, nil, err
	}
	if reached, limit, err := s.followLimitReachedInDB(ctx, tx, current); err != nil {
		return nil, nil, err
	} else if reached {
		return nil, nil, errors.New(followLimitReachedMessage(limit))
	}
	if target.Locked || current.SilencedAt.Valid {
		req := models.FollowRequest{CreatedAt: now, UpdatedAt: now, AccountID: current.ID, TargetAccountID: target.ID, ShowReblogs: true, URI: models.NullSafeString(activityPubGeneratedPayloadURI(s))}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&req)
		if res.Error != nil || res.RowsAffected == 0 {
			return nil, nil, res.Error
		}
		notificationPayload := &asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: current.ID, ActivityID: req.ID, ActivityType: "FollowRequest", Type: "follow_request"}
		if !target.Local() {
			return &relationshipBatchDelivery{Kind: "follow", Local: current, Remote: target, ID: req.ID, URI: string(req.URI)}, notificationPayload, nil
		}
		return nil, notificationPayload, nil
	}
	follow := models.Follow{CreatedAt: now, UpdatedAt: now, AccountID: current.ID, TargetAccountID: target.ID, ShowReblogs: true, URI: models.NullSafeString(activityPubGeneratedPayloadURI(s))}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, nil, res.Error
	}
	if err := incrementAccountStatCounter(tx, current.ID, accountStatCounterFollowing, 1); err != nil {
		return nil, nil, err
	}
	if err := incrementAccountStatCounter(tx, target.ID, accountStatCounterFollowers, 1); err != nil {
		return nil, nil, err
	}
	notificationPayload := &asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: current.ID, ActivityID: follow.ID, ActivityType: "Follow", Type: "follow"}
	if !target.Local() {
		return &relationshipBatchDelivery{Kind: "follow", Local: current, Remote: target, ID: follow.ID, URI: string(follow.URI)}, notificationPayload, nil
	}
	return nil, notificationPayload, nil
}

func relationshipFollowOrRequestExists(tx *gorm.DB, accountID int64, targetID int64) (bool, error) {
	var count int64
	if err := tx.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", accountID, targetID).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if err := tx.Model(&models.FollowRequest{}).Where("account_id = ? AND target_account_id = ?", accountID, targetID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Server) deliverRelationshipBatchDeliveries(deliveries []relationshipBatchDelivery) {
	for _, delivery := range deliveries {
		switch delivery.Kind {
		case "follow":
			_ = s.deliverActivityPubFollow(delivery.Local, delivery.Remote, delivery.ID, delivery.URI)
		case "undo_follow":
			_ = s.deliverActivityPubUndoFollow(delivery.Local, delivery.Remote, delivery.ID, delivery.URI)
		case "reject_follow":
			_ = s.deliverActivityPubFollowResponse("Reject", delivery.Local, delivery.Remote, delivery.ID, delivery.URI)
		}
	}
}

func (s *Server) deleteRelationshipFollowForBatch(tx *gorm.DB, current models.Account, target models.Account) (*relationshipBatchDelivery, []int64, bool, error) {
	var follow models.Follow
	err := tx.Where("account_id = ? AND target_account_id = ?", current.ID, target.ID).First(&follow).Error
	if err == nil {
		uri := string(follow.URI)
		if uri == "" && !target.Local() {
			uri = activityPubFollowURI(s, current, follow.ID)
		}
		listIDs, err := deleteFollowWithAffectedListIDs(tx, follow)
		if err != nil {
			return nil, nil, false, err
		}
		if !target.Local() {
			return &relationshipBatchDelivery{Kind: "undo_follow", Local: current, Remote: target, ID: follow.ID, URI: uri}, listIDs, true, nil
		}
		return nil, listIDs, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, false, err
	}
	var req models.FollowRequest
	err = tx.Where("account_id = ? AND target_account_id = ?", current.ID, target.ID).First(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	uri := string(req.URI)
	if uri == "" && !target.Local() {
		uri = activityPubFollowURI(s, current, req.ID)
	}
	if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", req.ID).Delete(&models.Notification{}).Error; err != nil {
		return nil, nil, false, err
	}
	if _, err := deleteListAccountsForRejectedFollowRequest(tx, req.ID); err != nil {
		return nil, nil, false, err
	}
	if err := tx.Delete(&req).Error; err != nil {
		return nil, nil, false, err
	}
	if !target.Local() {
		return &relationshipBatchDelivery{Kind: "undo_follow", Local: current, Remote: target, ID: req.ID, URI: uri}, nil, false, nil
	}
	return nil, nil, false, nil
}

func removeFollowerForBatch(tx *gorm.DB, current models.Account, follower models.Account) (*relationshipBatchDelivery, *followRelationshipCacheEffect, error) {
	var follow models.Follow
	err := tx.Where("account_id = ? AND target_account_id = ?", follower.ID, current.ID).First(&follow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	uri := string(follow.URI)
	if err := deleteFollow(tx, follow); err != nil {
		return nil, nil, err
	}
	effect := &followRelationshipCacheEffect{Source: follower, TargetID: current.ID}
	if current.Local() && !follower.Local() {
		return &relationshipBatchDelivery{Kind: "reject_follow", Local: current, Remote: follower, ID: follow.ID, URI: uri}, effect, nil
	}
	return nil, effect, nil
}

func deleteFollowersByDomain(tx *gorm.DB, current models.Account, domain string) ([]relationshipBatchDelivery, []followRelationshipCacheEffect, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, nil, nil
	}
	var follows []models.Follow
	if err := tx.Preload("Account").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id = ? AND lower(accounts.domain) = lower(?)", current.ID, domain).
		Find(&follows).Error; err != nil {
		return nil, nil, err
	}
	deliveries := make([]relationshipBatchDelivery, 0, len(follows))
	effects := make([]followRelationshipCacheEffect, 0, len(follows))
	for _, follow := range follows {
		uri := string(follow.URI)
		if err := deleteFollow(tx, follow); err != nil {
			return nil, nil, err
		}
		effects = append(effects, followRelationshipCacheEffect{Source: follow.Account, TargetID: current.ID})
		if current.Local() && !follow.Account.Local() {
			deliveries = append(deliveries, relationshipBatchDelivery{Kind: "reject_follow", Local: current, Remote: follow.Account, ID: follow.ID, URI: uri})
		}
	}
	return deliveries, effects, nil
}

type relationshipPageView struct {
	Config             config.Config
	Accounts           []models.Account
	Interrelationships relationshipPageInterrelationships
	Filters            relationshipFilters
	AutoPlayGIF        bool
}

func relationshipsHTML(view relationshipPageView, notice string, errorText string, hiddenFieldsAndLocale ...string) string {
	view.Filters = normalizedRelationshipFilters(view.Filters)
	loc := "en"
	themeArgs := []string{}
	if len(hiddenFieldsAndLocale) > 1 {
		loc = settingsLocaleArgOrEnglish(hiddenFieldsAndLocale[1])
		themeArgs = hiddenFieldsAndLocale[1:]
	}
	var rows strings.Builder
	for _, account := range view.Accounts {
		rows.WriteString(relationshipAccountRowHTML(view.Config, account, view.Interrelationships, view.AutoPlayGIF, loc))
	}
	if rows.Len() == 0 {
		rows.WriteString(`<div class="nothing-here nothing-here--under-tabs">` + html.EscapeString(settingsT(loc, "relationships.empty", "No accounts")) + `</div>`)
	}
	hidden := ""
	if len(hiddenFieldsAndLocale) > 0 {
		hidden = hiddenFieldsAndLocale[0]
	}
	body := relationshipFiltersHTML(view.Filters, loc) + `
    <form method="post" action="/relationships" class="edit_form_account_batch">
      <input type="hidden" name="_method" value="patch">
      ` + hidden + `
      <div class="batch-table">
        <div class="batch-table__toolbar">
          <label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label>
          <div class="batch-table__toolbar__actions">` + relationshipBatchActionsHTML(view.Filters.Relationship, loc) + `</div>
        </div>
        <div class="batch-table__body">` + rows.String() + `</div>
      </div>
    </form>`
	return authPageHTML(settingsT(loc, "settings.relationships", "Follows and followers"), notice, errorText, body, themeArgs...)
}

func relationshipFiltersHTML(filters relationshipFilters, locale string) string {
	filters = normalizedRelationshipFilters(filters)
	return `<div class="filters">` +
		relationshipFilterSubsetHTML(settingsT(locale, "relationships.relationship", "Relationship"), []relationshipFilterLink{
			{Label: settingsT(locale, "relationships.following", "Following"), Href: relationshipFilterHref(filters, "relationship", ""), Active: filters.Relationship == "following"},
			{Label: settingsT(locale, "relationships.followers", "Followers"), Href: relationshipFilterHref(filters, "relationship", "followed_by"), Active: filters.Relationship == "followed_by"},
			{Label: settingsT(locale, "relationships.mutual", "Mutual"), Href: relationshipFilterHref(filters, "relationship", "mutual"), Active: filters.Relationship == "mutual"},
		}) +
		relationshipFilterSubsetHTML(settingsT(locale, "relationships.status", "Status"), []relationshipFilterLink{
			{Label: settingsT(locale, "generic.all", "All"), Href: relationshipFilterHref(filters, "status", ""), Active: filters.Status == ""},
			{Label: settingsT(locale, "relationships.primary", "Primary"), Href: relationshipFilterHref(filters, "status", "primary"), Active: filters.Status == "primary"},
			{Label: settingsT(locale, "relationships.moved", "Moved"), Href: relationshipFilterHref(filters, "status", "moved"), Active: filters.Status == "moved"},
		}) +
		relationshipFilterSubsetHTML(settingsT(locale, "relationships.activity", "Activity"), []relationshipFilterLink{
			{Label: settingsT(locale, "generic.all", "All"), Href: relationshipFilterHref(filters, "activity", ""), Active: filters.Activity == ""},
			{Label: settingsT(locale, "relationships.dormant", "Dormant"), Href: relationshipFilterHref(filters, "activity", "dormant"), Active: filters.Activity == "dormant"},
		}) +
		relationshipFilterSubsetHTML(settingsT(locale, "generic.order_by", "Order by"), []relationshipFilterLink{
			{Label: settingsT(locale, "relationships.most_recent", "Most recent"), Href: relationshipFilterHref(filters, "order", ""), Active: filters.Order == "recent"},
			{Label: settingsT(locale, "relationships.last_active", "Last active"), Href: relationshipFilterHref(filters, "order", "active"), Active: filters.Order == "active"},
		}) +
		`</div>`
}

func normalizedRelationshipFilters(filters relationshipFilters) relationshipFilters {
	if filters.Relationship == "" {
		filters.Relationship = "following"
	}
	if filters.Order == "" {
		filters.Order = "recent"
	}
	return filters
}

func relationshipFilterHref(filters relationshipFilters, key string, value string) string {
	filters = normalizedRelationshipFilters(filters)
	switch key {
	case "relationship":
		filters.Relationship = value
		if filters.Relationship == "" {
			filters.Relationship = "following"
		}
	case "status":
		filters.Status = value
	case "activity":
		filters.Activity = value
	case "order":
		filters.Order = value
		if filters.Order == "" {
			filters.Order = "recent"
		}
	}

	query := url.Values{}
	if filters.Relationship != "following" {
		query.Set("relationship", filters.Relationship)
	}
	if filters.Status != "" {
		query.Set("status", filters.Status)
	}
	if filters.ByDomain != "" {
		query.Set("by_domain", filters.ByDomain)
	}
	if filters.Activity != "" {
		query.Set("activity", filters.Activity)
	}
	if filters.Order != "recent" {
		query.Set("order", filters.Order)
	}
	if filters.Location != "" {
		query.Set("location", filters.Location)
	}
	if encoded := query.Encode(); encoded != "" {
		return "/relationships?" + encoded
	}
	return "/relationships"
}

type relationshipFilterLink struct {
	Label  string
	Href   string
	Active bool
}

func relationshipFilterSubsetHTML(title string, links []relationshipFilterLink) string {
	var out strings.Builder
	out.WriteString(`<div class="filter-subset"><strong>`)
	out.WriteString(html.EscapeString(title))
	out.WriteString(`</strong><ul>`)
	for _, link := range links {
		class := ` class=""`
		if link.Active {
			class = ` class="selected"`
		}
		out.WriteString(`<li><a` + class + ` href="` + html.EscapeString(link.Href) + `">` + html.EscapeString(link.Label) + `</a></li>`)
	}
	out.WriteString(`</ul></div>`)
	return out.String()
}

func relationshipBatchActionsHTML(relationship string, locale string) string {
	var out strings.Builder
	if relationship == "followed_by" {
		out.WriteString(`<button name="follow" value="1" class="table-action-link" type="submit" data-confirm="` + html.EscapeString(settingsT(locale, "relationships.confirm_follow_selected_followers", "Are you sure you want to follow selected followers?")) + `"><i class="fa fa-user-plus"></i> ` + html.EscapeString(settingsT(locale, "relationships.follow_selected_followers", "Follow selected followers")) + `</button>`)
	}
	if relationship != "followed_by" {
		out.WriteString(`<button name="unfollow" value="1" class="table-action-link" type="submit" data-confirm="` + html.EscapeString(settingsT(locale, "relationships.confirm_remove_selected_follows", "Are you sure you want to remove selected follows?")) + `"><i class="fa fa-user-times"></i> ` + html.EscapeString(settingsT(locale, "relationships.remove_selected_follows", "Unfollow selected users")) + `</button>`)
	}
	if relationship != "following" {
		out.WriteString(`<button name="remove_from_followers" value="1" class="table-action-link" type="submit" data-confirm="` + html.EscapeString(settingsT(locale, "relationships.confirm_remove_selected_followers", "Are you sure you want to remove selected followers?")) + `"><i class="fa fa-trash"></i> ` + html.EscapeString(settingsT(locale, "relationships.remove_selected_followers", "Remove selected followers")) + `</button>`)
	}
	if relationship == "followed_by" {
		out.WriteString(`<button name="remove_domains_from_followers" value="1" class="table-action-link" type="submit" data-confirm="` + html.EscapeString(settingsT(locale, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-trash"></i> ` + html.EscapeString(settingsT(locale, "relationships.remove_selected_domains", "Remove selected domains")) + `</button>`)
	}
	return out.String()
}

func relationshipAccountRowHTML(cfg config.Config, account models.Account, interrelationships relationshipPageInterrelationships, autoPlayGIF bool, locale string) string {
	lastActive := "-"
	if account.AccountStat.LastStatusAt.Valid {
		stamp := account.AccountStat.LastStatusAt.Time.UTC()
		lastActive = `<time class="time-ago" datetime="` + html.EscapeString(stamp.Format("2006-01-02")) + `" title="` + html.EscapeString(stamp.Format("2006-01-02")) + `">` + html.EscapeString(stamp.Format("2006-01-02")) + `</time>`
	}
	return `<div class="batch-table__row"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="form_account_batch[account_ids][]" value="` + strconv.FormatInt(account.ID, 10) + `"></label><div class="batch-table__row__content batch-table__row__content--unpadded"><table class="accounts-table"><tbody><tr><td class="accounts-table__interrelationships">` + relationshipInterrelationshipsIconHTML(interrelationships, account.ID, locale) + `</td><td>` + relationshipAccountLinkHTML(cfg, account, autoPlayGIF) + `</td><td class="accounts-table__count optional">` + strconv.FormatInt(account.AccountStat.StatusesCount, 10) + `<small>` + html.EscapeString(strings.ToLower(settingsT(locale, "accounts.posts.other", "posts"))) + `</small></td><td class="accounts-table__count optional">` + strconv.FormatInt(account.AccountStat.FollowersCount, 10) + `<small>` + html.EscapeString(strings.ToLower(settingsT(locale, "accounts.followers.other", "followers"))) + `</small></td><td class="accounts-table__count">` + lastActive + `<small>` + html.EscapeString(settingsT(locale, "accounts.last_active", "last active")) + `</small></td></tr></tbody></table></div></div>`
}

func relationshipInterrelationshipsIconHTML(interrelationships relationshipPageInterrelationships, accountID int64, locale string) string {
	following := interrelationships.Following[accountID]
	followedBy := interrelationships.FollowedBy[accountID]
	if following && followedBy {
		return `<i title="` + html.EscapeString(settingsT(locale, "relationships.mutual", "Mutual")) + `" class="fa-fw active passive fa fa-exchange"></i>`
	}
	if following {
		icon := "arrow-right"
		if relationshipLocaleRTL(locale) {
			icon = "arrow-left"
		}
		return `<i title="` + html.EscapeString(settingsT(locale, "relationships.following", "Following")) + `" class="fa-fw active fa fa-` + icon + `"></i>`
	}
	if followedBy {
		icon := "arrow-left"
		if relationshipLocaleRTL(locale) {
			icon = "arrow-right"
		}
		return `<i title="` + html.EscapeString(settingsT(locale, "relationships.followers", "Followers")) + `" class="fa-fw passive fa fa-` + icon + `"></i>`
	}
	return ""
}

func relationshipLocaleRTL(locale string) bool {
	base := strings.ToLower(strings.SplitN(strings.ReplaceAll(locale, "_", "-"), "-", 2)[0])
	return base == "ar" || base == "ckb" || base == "fa" || base == "he"
}

func relationshipAccountLinkHTML(cfg config.Config, account models.Account, autoPlayGIF bool) string {
	view := serializer.AccountFromModel(cfg, account)
	avatar := view.AvatarStatic
	if autoPlayGIF {
		avatar = view.Avatar
	}
	displayName := statusEmbedAccountNameHTMLWithConfig(cfg, account, account.CustomEmojis)
	return `<div class="account account--minimal"><div class="account__wrapper"><a class="account__display-name" href="` + html.EscapeString(view.URL) + `"><div class="account__avatar-wrapper"><img class="account__avatar" width="46" height="46" src="` + html.EscapeString(avatar) + `"></div><span class="display-name"><bdi><strong class="display-name__html emojify">` + displayName + `</strong></bdi><span class="display-name__account">@` + html.EscapeString(view.Acct) + `</span></span></a></div></div>`
}
