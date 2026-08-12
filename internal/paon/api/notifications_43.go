package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const notificationGroupSampleAccountsLimit = 8

var groupableNotificationTypes = map[string]struct{}{
	"favourite":     {},
	"reblog":        {},
	"follow":        {},
	"admin.sign_up": {},
}

type notificationGroupRow struct {
	ID       int64  `gorm:"column:id"`
	GroupKey string `gorm:"column:group_key"`
}

type notificationGroupEntity struct {
	GroupKey                 string                                        `json:"group_key"`
	NotificationsCount       int64                                         `json:"notifications_count"`
	Type                     string                                        `json:"type"`
	MostRecentNotificationID int64                                         `json:"most_recent_notification_id"`
	PageMinID                *string                                       `json:"page_min_id,omitempty"`
	PageMaxID                *string                                       `json:"page_max_id,omitempty"`
	LatestPageNotificationAt *string                                       `json:"latest_page_notification_at,omitempty"`
	SampleAccountIDs         []string                                      `json:"sample_account_ids"`
	StatusID                 *string                                       `json:"status_id,omitempty"`
	Report                   *serializer.Report                            `json:"report,omitempty"`
	Event                    *serializer.AccountRelationshipSeveranceEvent `json:"event,omitempty"`
	ModerationWarning        *serializer.AccountWarning                    `json:"moderation_warning,omitempty"`
	AnnualReport             *annualReportEventEntity                      `json:"annual_report,omitempty"`
}

type annualReportEventEntity struct {
	Year string `json:"year"`
}

type partialNotificationAccount struct {
	ID           string `json:"id"`
	Acct         string `json:"acct"`
	Locked       bool   `json:"locked"`
	Bot          bool   `json:"bot"`
	URL          string `json:"url"`
	Avatar       string `json:"avatar"`
	AvatarStatic string `json:"avatar_static"`
}

func (s *Server) groupedNotifications(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}
	expandAccounts, valid := notificationExpandAccounts(c)
	if !valid {
		return apiError(c, http.StatusBadRequest, "Invalid value for 'expand_accounts': '"+expandAccounts+"', allowed values are 'full' and 'partial_avatars'")
	}
	pageLimit := limit(c, 40, 80)
	rows, err := s.notificationGroupRows(c, account.ID, pageLimit)
	if err != nil {
		return err
	}
	notifications, err := s.notificationsForGroupRows(account, rows)
	if err != nil {
		return err
	}
	groups, accounts, statuses, err := s.notificationGroupEnvelope(account, rows, notifications, notificationGroupPageRange(c, rows, pageLimit))
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		c.Response().Header().Set("Link", notificationV2PaginationLink(c, rows[0].ID, rows[len(rows)-1].ID))
	}
	envelope := map[string]any{
		"statuses":            statuses,
		"notification_groups": groups,
	}
	if expandAccounts == "partial_avatars" {
		full, partial := notificationPartialAvatarAccounts(groups, accounts)
		envelope["partial_accounts"] = partial
		envelope["accounts"] = full
	} else {
		envelope["accounts"] = accounts
	}
	return c.JSON(http.StatusOK, envelope)
}

func notificationExpandAccounts(c *echo.Context) (string, bool) {
	value := c.QueryParam("expand_accounts")
	if _, provided := c.QueryParams()["expand_accounts"]; !provided {
		return "full", true
	}
	return value, value == "full" || value == "partial_avatars"
}

func (s *Server) showGroupedNotification(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}
	query := s.notificationQuery(account.ID)
	groupKey := c.Param("group_key")
	if id, ok := ungroupedNotificationID(groupKey); ok {
		query = query.Where("notifications.id = ?", id)
	} else {
		query = query.Where("notifications.group_key = ?", groupKey)
	}
	var notification models.Notification
	if err := query.Order("notifications.id DESC").First(&notification).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	rows := []notificationGroupRow{{ID: notification.ID, GroupKey: effectiveNotificationGroupKey(notification)}}
	notifications, err := s.notificationsForGroupRows(account, rows)
	if err != nil {
		return err
	}
	groups, accounts, statuses, err := s.notificationGroupEnvelope(account, rows, notifications, nil)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"accounts": accounts, "statuses": statuses, "notification_groups": groups})
}

func (s *Server) dismissGroupedNotification(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:notifications")
	if err != nil {
		return err
	}
	query := s.db.Where("account_id = ?", account.ID)
	if id, ok := ungroupedNotificationID(c.Param("group_key")); ok {
		query = query.Where("id = ?", id)
	} else {
		query = query.Where("group_key = ?", c.Param("group_key"))
	}
	if err := query.Delete(&models.Notification{}).Error; err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) groupedNotificationAccounts(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}
	query := s.notificationQuery(account.ID).Where("notifications.group_key = ?", c.Param("group_key"))
	query = applyIDPagination(c, query, "notifications.id").Order("notifications.id DESC")
	var notifications []models.Notification
	if err := query.Limit(limit(c, 40, 80)).Find(&notifications).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(notifications)
	}
	out := make([]serializer.Account, 0, len(notifications))
	for i := range notifications {
		if err := s.hydrateAccountCustomEmojis(&notifications[i].FromAccount); err != nil {
			return err
		}
		out = append(out, serializer.AccountFromModel(s.cfg, notifications[i].FromAccount))
	}
	if len(notifications) > 0 {
		c.Response().Header().Set("Link", paginationLink(c, notifications[0].ID, notifications[len(notifications)-1].ID))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) notificationGroupRows(c *echo.Context, accountID int64, pageLimit int) ([]notificationGroupRow, error) {
	groupedTypes := notificationGroupedTypes(c)
	typeList := make([]string, 0, len(groupedTypes))
	for kind := range groupedTypes {
		typeList = append(typeList, "'"+kind+"'")
	}
	groupExpression := "'ungrouped-' || notifications.id"
	if len(typeList) > 0 {
		groupExpression = "CASE WHEN notifications.group_key IS NOT NULL AND notifications.type IN (" + strings.Join(typeList, ",") + ") THEN notifications.group_key ELSE 'ungrouped-' || notifications.id END"
	}
	query := s.db.Table("notifications").
		Select("MAX(notifications.id) AS id, "+groupExpression+" AS group_key").
		Joins("JOIN accounts notification_group_accounts ON notification_group_accounts.id = notifications.from_account_id").
		Where("notifications.account_id = ? AND notification_group_accounts.suspended_at IS NULL", accountID)
	query = applyNotificationTypeFiltersV2(c, query)
	if !truthy(c.QueryParam("include_filtered")) {
		query = query.Where("notifications.filtered = ?", false)
	}
	if maxID := railsToInt64(c.QueryParam("max_id")); queryParamValuePresent(c, "max_id") {
		query = query.Where("notifications.id < ?", maxID)
	}
	if sinceID := railsToInt64(c.QueryParam("since_id")); queryParamValuePresent(c, "since_id") {
		query = query.Where("notifications.id > ?", sinceID)
	}
	ascending := queryParamValuePresent(c, "min_id")
	if minID := railsToInt64(c.QueryParam("min_id")); ascending {
		query = query.Where("notifications.id > ?", minID)
	}
	query = query.Group(groupExpression)
	if ascending {
		query = query.Order("MAX(notifications.id) ASC")
	} else {
		query = query.Order("MAX(notifications.id) DESC")
	}
	var rows []notificationGroupRow
	if err := query.Limit(pageLimit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if ascending {
		reverseRows(rows)
	}
	return rows, nil
}

func applyNotificationTypeFiltersV2(c *echo.Context, query *gorm.DB) *gorm.DB {
	types, requested := notificationFilterValues(c, "types[]", "types")
	excluded, _ := notificationFilterValues(c, "exclude_types[]", "exclude_types")
	if requested && len(types) == 0 {
		query = query.Where("1 = 0")
	}
	if len(types) > 0 {
		query = query.Where(notificationFilterTypeSQL()+" IN ?", types)
	}
	if len(excluded) > 0 {
		query = query.Where(notificationFilterTypeSQL()+" NOT IN ?", excluded)
	}
	return query
}

func notificationGroupedTypes(c *echo.Context) map[string]struct{} {
	values := c.QueryParams()["grouped_types[]"]
	values = append(values, c.QueryParams()["grouped_types"]...)
	if len(values) == 0 {
		return map[string]struct{}{"favourite": {}, "reblog": {}, "follow": {}, "admin.sign_up": {}}
	}
	out := map[string]struct{}{}
	for _, kind := range values {
		if _, ok := groupableNotificationTypes[kind]; ok {
			out[kind] = struct{}{}
		}
	}
	return out
}

func (s *Server) notificationsForGroupRows(account *models.Account, rows []notificationGroupRow) ([]models.Notification, error) {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return []models.Notification{}, nil
	}
	var notifications []models.Notification
	if err := s.notificationQuery(account.ID).Where("notifications.id IN ?", ids).Find(&notifications).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]models.Notification, len(notifications))
	for _, notification := range notifications {
		byID[notification.ID] = notification
	}
	ordered := make([]models.Notification, 0, len(rows))
	for _, row := range rows {
		if notification, ok := byID[row.ID]; ok {
			ordered = append(ordered, notification)
		}
	}
	if err := s.hydrateNotificationStatuses(ordered); err != nil {
		return nil, err
	}
	if err := s.hydrateNotificationReports(ordered); err != nil {
		return nil, err
	}
	if err := s.hydrateNotificationSpecialEvents(ordered); err != nil {
		return nil, err
	}
	if err := s.hydrateNotificationAccounts(ordered); err != nil {
		return nil, err
	}
	if err := s.hydrateNotificationStatusRelationships(ordered, account); err != nil {
		return nil, err
	}
	return ordered, nil
}

type notificationPageRange struct {
	MinID        int64
	HasMinID     bool
	MaxID        int64
	HasMaxID     bool
	MaxExclusive bool
}

func notificationGroupPageRange(c *echo.Context, rows []notificationGroupRow, pageLimit int) *notificationPageRange {
	if len(rows) == 0 {
		return nil
	}
	page := &notificationPageRange{
		MinID:    rows[len(rows)-1].ID,
		HasMinID: true,
		MaxID:    rows[0].ID,
		HasMaxID: true,
	}
	if len(rows) >= pageLimit {
		return page
	}
	if queryParamValuePresent(c, "min_id") {
		if maxID := railsToInt64(c.QueryParam("max_id")); maxID > 0 {
			page.MaxID = maxID
			page.HasMaxID = true
			page.MaxExclusive = true
		} else {
			page.MaxID = 0
			page.HasMaxID = false
		}
		return page
	}
	if sinceID := railsToInt64(c.QueryParam("since_id")); queryParamValuePresent(c, "since_id") && sinceID >= 0 {
		page.MinID = sinceID + 1
		page.HasMinID = true
	} else {
		page.MinID = 0
		page.HasMinID = false
	}
	return page
}

func (s *Server) notificationGroupEnvelope(account *models.Account, rows []notificationGroupRow, notifications []models.Notification, pageRange *notificationPageRange) ([]notificationGroupEntity, []serializer.Account, []serializer.Status, error) {
	notificationsByID := make(map[int64]models.Notification, len(notifications))
	for _, notification := range notifications {
		notificationsByID[notification.ID] = notification
	}
	groups := make([]notificationGroupEntity, 0, len(rows))
	accountsByID := map[int64]serializer.Account{}
	statusesByID := map[int64]serializer.Status{}
	accountOrder := []int64{}
	statusOrder := []int64{}
	for _, row := range rows {
		notification, ok := notificationsByID[row.ID]
		if !ok {
			continue
		}
		samples, count, minID, mostRecentID, latestAt, err := s.notificationGroupSamples(account.ID, row.GroupKey, notification, pageRange)
		if err != nil {
			return nil, nil, nil, err
		}
		entity := notificationGroupEntity{
			GroupKey:                 row.GroupKey,
			NotificationsCount:       count,
			Type:                     notification.ResolvedType(),
			MostRecentNotificationID: mostRecentID,
			SampleAccountIDs:         make([]string, 0, len(samples)),
		}
		if pageRange != nil {
			pageMin := strconv.FormatInt(minID, 10)
			pageMax := strconv.FormatInt(mostRecentID, 10)
			latest := latestAt.UTC().Format("2006-01-02T15:04:05.000Z")
			entity.PageMinID = &pageMin
			entity.PageMaxID = &pageMax
			entity.LatestPageNotificationAt = &latest
		}
		for i := range samples {
			if err := s.hydrateAccountCustomEmojis(&samples[i]); err != nil {
				return nil, nil, nil, err
			}
			entity.SampleAccountIDs = append(entity.SampleAccountIDs, strconv.FormatInt(samples[i].ID, 10))
			if _, exists := accountsByID[samples[i].ID]; !exists {
				accountsByID[samples[i].ID] = serializer.AccountFromModel(s.cfg, samples[i])
				accountOrder = append(accountOrder, samples[i].ID)
			}
		}
		if notification.TargetStatus != nil && notification.TargetStatus.ID != 0 {
			statusID := strconv.FormatInt(notification.TargetStatus.ID, 10)
			entity.StatusID = &statusID
			if _, exists := statusesByID[notification.TargetStatus.ID]; !exists {
				statusesByID[notification.TargetStatus.ID] = notificationStatusValue(notificationWithStatusFilters(s.cfg, notification, account, s.accountFilters(account)))
				statusOrder = append(statusOrder, notification.TargetStatus.ID)
			}
		}
		if notification.Report != nil {
			report := serializer.ReportFromModel(s.cfg, *notification.Report)
			entity.Report = &report
		}
		if notification.SeveranceEvent != nil {
			event := serializer.AccountRelationshipSeveranceEventFromModel(*notification.SeveranceEvent)
			entity.Event = &event
		}
		if notification.AccountWarning != nil {
			warning := serializer.AccountWarningFromModel(s.cfg, *notification.AccountWarning)
			entity.ModerationWarning = &warning
		}
		if notification.AnnualReport != nil {
			entity.AnnualReport = &annualReportEventEntity{Year: strconv.Itoa(notification.AnnualReport.Year)}
		}
		groups = append(groups, entity)
	}
	accounts := make([]serializer.Account, 0, len(accountOrder))
	for _, id := range accountOrder {
		accounts = append(accounts, accountsByID[id])
	}
	statuses := make([]serializer.Status, 0, len(statusOrder))
	for _, id := range statusOrder {
		statuses = append(statuses, statusesByID[id])
	}
	return groups, accounts, statuses, nil
}

func (s *Server) notificationGroupSamples(accountID int64, groupKey string, representative models.Notification, pageRange *notificationPageRange) ([]models.Account, int64, int64, int64, time.Time, error) {
	if _, ungrouped := ungroupedNotificationID(groupKey); ungrouped {
		return []models.Account{representative.FromAccount}, 1, representative.ID, representative.ID, representative.CreatedAt, nil
	}
	// Mastodon intentionally loads the complete group here, including notifications
	// from accounts that were suspended after the page itself was selected.
	query := accountRelationSerializerPreloads(s.db.Model(&models.Notification{}), "FromAccount").
		Where("notifications.account_id = ? AND notifications.group_key = ?", accountID, groupKey)
	if pageRange != nil && pageRange.HasMaxID {
		operator := "<="
		if pageRange.MaxExclusive {
			operator = "<"
		}
		query = query.Where("notifications.id "+operator+" ?", pageRange.MaxID)
	}
	var count int64
	if err := query.Session(&gorm.Session{}).Count(&count).Error; err != nil {
		return nil, 0, 0, 0, time.Time{}, err
	}
	var samples []models.Notification
	if err := query.Order("notifications.id DESC").Limit(notificationGroupSampleAccountsLimit).Find(&samples).Error; err != nil {
		return nil, 0, 0, 0, time.Time{}, err
	}
	accounts := make([]models.Account, 0, len(samples))
	minID := representative.ID
	mostRecentID := representative.ID
	latestAt := representative.CreatedAt
	for index, sample := range samples {
		accounts = append(accounts, sample.FromAccount)
		if index == 0 {
			mostRecentID = sample.ID
			latestAt = sample.CreatedAt
		}
		if sample.ID < minID {
			minID = sample.ID
		}
	}
	minQuery := query.Session(&gorm.Session{}).Select("notifications.id")
	if pageRange != nil && pageRange.HasMinID {
		minQuery = minQuery.Where("notifications.id >= ?", pageRange.MinID)
	}
	var pageMinimum models.Notification
	if err := minQuery.Order("notifications.id ASC").First(&pageMinimum).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, 0, 0, time.Time{}, err
		}
	} else {
		minID = pageMinimum.ID
	}
	return accounts, count, minID, mostRecentID, latestAt, nil
}

func effectiveNotificationGroupKey(notification models.Notification) string {
	if notification.GroupKey.Valid && notification.GroupKey.String != "" {
		return notification.GroupKey.String
	}
	return "ungrouped-" + strconv.FormatInt(notification.ID, 10)
}

func ungroupedNotificationID(groupKey string) (int64, bool) {
	value, ok := strings.CutPrefix(groupKey, "ungrouped-")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func notificationV2PaginationLink(c *echo.Context, first int64, last int64) string {
	return paginationLinkWithAllowedParams(c, first, last, "min_id", true, true, []string{"limit", "include_filtered", "types[]", "exclude_types[]", "grouped_types[]"})
}

func notificationPartialAvatarAccounts(groups []notificationGroupEntity, accounts []serializer.Account) ([]serializer.Account, []partialNotificationAccount) {
	accountsByID := make(map[string]serializer.Account, len(accounts))
	for _, account := range accounts {
		accountsByID[account.ID] = account
	}
	fullIDs := make(map[string]struct{}, len(groups))
	full := make([]serializer.Account, 0, len(groups))
	for _, group := range groups {
		if len(group.SampleAccountIDs) == 0 {
			continue
		}
		id := group.SampleAccountIDs[0]
		account, exists := accountsByID[id]
		if !exists {
			continue
		}
		if _, exists := fullIDs[id]; exists {
			continue
		}
		fullIDs[id] = struct{}{}
		full = append(full, account)
	}
	partialIDs := map[string]struct{}{}
	partial := make([]partialNotificationAccount, 0, len(accounts)-len(full))
	for _, group := range groups {
		for _, id := range group.SampleAccountIDs[1:] {
			if _, isFull := fullIDs[id]; isFull {
				continue
			}
			if _, exists := partialIDs[id]; exists {
				continue
			}
			account, exists := accountsByID[id]
			if !exists {
				continue
			}
			partialIDs[id] = struct{}{}
			partial = append(partial, partialNotificationAccount{
				ID: account.ID, Acct: account.Acct, Locked: account.Locked, Bot: account.Bot,
				URL: account.URL, Avatar: account.Avatar, AvatarStatic: account.AvatarStatic,
			})
		}
	}
	return full, partial
}

func (s *Server) unreadNotificationCount(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}
	marker, err := s.notificationMarkerID(account.ID)
	if err != nil {
		return err
	}
	countLimit := int64(limit(c, 100, 1000))
	query := applyNotificationFilteredVisibility(c, applyNotificationFilters(c, s.notificationQuery(account.ID))).Where("notifications.id > ?", marker)
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > countLimit {
		count = countLimit
	}
	return c.JSON(http.StatusOK, map[string]int64{"count": count})
}

func (s *Server) unreadGroupedNotificationCount(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}
	marker, err := s.notificationMarkerID(account.ID)
	if err != nil {
		return err
	}
	queryValues := c.QueryParams()
	previousMinID, hadMinID := queryValues["min_id"]
	queryValues.Set("min_id", strconv.FormatInt(marker, 10))
	c.Request().URL.RawQuery = queryValues.Encode()
	defer func() {
		queryValues := c.QueryParams()
		if hadMinID {
			queryValues["min_id"] = previousMinID
		} else {
			queryValues.Del("min_id")
		}
		c.Request().URL.RawQuery = queryValues.Encode()
	}()
	rows, err := s.notificationGroupRows(c, account.ID, limit(c, 100, 1000))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]int{"count": len(rows)})
}

func (s *Server) notificationMarkerID(accountID int64) (int64, error) {
	var user models.User
	if err := s.db.Select("id").Where("account_id = ?", accountID).First(&user).Error; err != nil {
		return 0, err
	}
	var marker models.Marker
	if err := s.db.Select("last_read_id").Where("user_id = ? AND timeline = ?", user.ID, "notifications").First(&marker).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return marker.LastReadID, nil
}

type notificationPolicyV2 struct {
	ForNotFollowing    string                    `json:"for_not_following"`
	ForNotFollowers    string                    `json:"for_not_followers"`
	ForNewAccounts     string                    `json:"for_new_accounts"`
	ForPrivateMentions string                    `json:"for_private_mentions"`
	ForLimitedAccounts string                    `json:"for_limited_accounts"`
	Summary            notificationPolicySummary `json:"summary"`
}

type notificationPolicyV1 struct {
	FilterNotFollowing    bool                      `json:"filter_not_following"`
	FilterNotFollowers    bool                      `json:"filter_not_followers"`
	FilterNewAccounts     bool                      `json:"filter_new_accounts"`
	FilterPrivateMentions bool                      `json:"filter_private_mentions"`
	Summary               notificationPolicySummary `json:"summary"`
}

type notificationPolicySummary struct {
	PendingRequestsCount      int64 `json:"pending_requests_count"`
	PendingNotificationsCount int64 `json:"pending_notifications_count"`
}

func (s *Server) showNotificationPolicyV2(c *echo.Context) error {
	return s.notificationPolicy(c, false, false)
}
func (s *Server) updateNotificationPolicyV2(c *echo.Context) error {
	return s.notificationPolicy(c, true, false)
}
func (s *Server) showNotificationPolicyV1(c *echo.Context) error {
	return s.notificationPolicy(c, false, true)
}
func (s *Server) updateNotificationPolicyV1(c *echo.Context) error {
	return s.notificationPolicy(c, true, true)
}

func (s *Server) notificationPolicy(c *echo.Context, update bool, legacy bool) error {
	scope := []string{"read", "read:notifications"}
	if update {
		scope = []string{"write", "write:notifications"}
	}
	account, _, err := s.requireAccountScope(c, scope...)
	if err != nil {
		return err
	}
	policy, err := s.findOrInitializeNotificationPolicy(account.ID)
	if err != nil {
		return err
	}
	if update {
		if err := updateNotificationPolicyFromRequest(c, &policy, legacy); err != nil {
			return err
		}
		now := time.Now().UTC()
		policy.UpdatedAt = now
		if policy.ID == 0 {
			policy.CreatedAt = now
			if err := s.db.Create(&policy).Error; err != nil {
				return err
			}
		} else if err := s.db.Save(&policy).Error; err != nil {
			return err
		}
	}
	summary, err := s.notificationPolicySummary(account.ID)
	if err != nil {
		return err
	}
	if legacy {
		return c.JSON(http.StatusOK, notificationPolicyV1{
			FilterNotFollowing:    policy.ForNotFollowing != 0,
			FilterNotFollowers:    policy.ForNotFollowers != 0,
			FilterNewAccounts:     policy.ForNewAccounts != 0,
			FilterPrivateMentions: policy.ForPrivateMentions != 0,
			Summary:               summary,
		})
	}
	return c.JSON(http.StatusOK, notificationPolicyV2{
		ForNotFollowing: notificationPolicyActionName(policy.ForNotFollowing), ForNotFollowers: notificationPolicyActionName(policy.ForNotFollowers),
		ForNewAccounts: notificationPolicyActionName(policy.ForNewAccounts), ForPrivateMentions: notificationPolicyActionName(policy.ForPrivateMentions),
		ForLimitedAccounts: notificationPolicyActionName(policy.ForLimitedAccounts), Summary: summary,
	})
}

func (s *Server) findOrInitializeNotificationPolicy(accountID int64) (models.NotificationPolicy, error) {
	var policy models.NotificationPolicy
	err := s.db.Where("account_id = ?", accountID).First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.NotificationPolicy{AccountID: accountID, ForPrivateMentions: 1, ForLimitedAccounts: 1}, nil
	}
	return policy, err
}

func updateNotificationPolicyFromRequest(c *echo.Context, policy *models.NotificationPolicy, legacy bool) error {
	if _, err := oauthRequestJSONPayload(c); err != nil {
		return apiError(c, http.StatusBadRequest, "Invalid request body")
	}
	if legacy {
		fields := []struct {
			name   string
			target *int
		}{
			{"filter_not_following", &policy.ForNotFollowing}, {"filter_not_followers", &policy.ForNotFollowers},
			{"filter_new_accounts", &policy.ForNewAccounts}, {"filter_private_mentions", &policy.ForPrivateMentions},
		}
		for _, field := range fields {
			if value := oauthRawParamValue(c, field.name); value != "" {
				if truthy(value) {
					*field.target = 1
				} else {
					*field.target = 0
				}
			}
		}
		return nil
	}
	fields := []struct {
		name   string
		target *int
	}{
		{"for_not_following", &policy.ForNotFollowing}, {"for_not_followers", &policy.ForNotFollowers},
		{"for_new_accounts", &policy.ForNewAccounts}, {"for_private_mentions", &policy.ForPrivateMentions},
		{"for_limited_accounts", &policy.ForLimitedAccounts},
	}
	for _, field := range fields {
		value := oauthRawParamValue(c, field.name)
		if value == "" {
			continue
		}
		action, ok := notificationPolicyAction(value)
		if !ok {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: "+field.name+" is invalid")
		}
		*field.target = action
	}
	return nil
}

func notificationPolicyAction(value string) (int, bool) {
	switch value {
	case "accept":
		return 0, true
	case "filter":
		return 1, true
	case "drop":
		return 2, true
	default:
		return 0, false
	}
}

func notificationPolicyActionName(value int) string {
	switch value {
	case 1:
		return "filter"
	case 2:
		return "drop"
	default:
		return "accept"
	}
}

func (s *Server) notificationPolicySummary(accountID int64) (notificationPolicySummary, error) {
	var rows []models.NotificationRequest
	if err := s.db.Joins("JOIN accounts notification_request_accounts ON notification_request_accounts.id = notification_requests.from_account_id").
		Where("notification_requests.account_id = ? AND notification_request_accounts.suspended_at IS NULL", accountID).Limit(100).Find(&rows).Error; err != nil {
		return notificationPolicySummary{}, err
	}
	summary := notificationPolicySummary{PendingRequestsCount: int64(len(rows))}
	for _, row := range rows {
		summary.PendingNotificationsCount += row.NotificationsCount
	}
	return summary, nil
}

type notificationRequestEntity struct {
	ID                 string             `json:"id"`
	CreatedAt          string             `json:"created_at"`
	UpdatedAt          string             `json:"updated_at"`
	NotificationsCount string             `json:"notifications_count"`
	Account            serializer.Account `json:"account"`
	LastStatus         *serializer.Status `json:"last_status"`
}

func (s *Server) notificationRequests(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}
	query := s.notificationRequestQuery(account.ID)
	query = applyIDPagination(c, query, "notification_requests.id").Order("notification_requests.id DESC")
	var requests []models.NotificationRequest
	if err := query.Limit(limit(c, 40, 80)).Find(&requests).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(requests)
	}
	out, err := s.serializeNotificationRequests(requests, account)
	if err != nil {
		return err
	}
	if len(requests) > 0 {
		c.Response().Header().Set("Link", paginationLink(c, requests[0].ID, requests[len(requests)-1].ID))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showNotificationRequest(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}
	var request models.NotificationRequest
	if err := s.notificationRequestQuery(account.ID).Where("notification_requests.id = ?", c.Param("id")).First(&request).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	out, err := s.serializeNotificationRequests([]models.NotificationRequest{request}, account)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out[0])
}

func (s *Server) notificationRequestQuery(accountID int64) *gorm.DB {
	return accountRelationSerializerPreloads(s.db.Model(&models.NotificationRequest{}), "FromAccount").
		Joins("JOIN accounts notification_request_accounts ON notification_request_accounts.id = notification_requests.from_account_id").
		Where("notification_requests.account_id = ? AND notification_request_accounts.suspended_at IS NULL", accountID)
}

func (s *Server) serializeNotificationRequests(requests []models.NotificationRequest, account *models.Account) ([]notificationRequestEntity, error) {
	statusIDs := []int64{}
	for _, request := range requests {
		if request.LastStatusID.Valid {
			statusIDs = append(statusIDs, request.LastStatusID.Int64)
		}
	}
	statuses := map[int64]models.Status{}
	if len(statusIDs) > 0 {
		var rows []models.Status
		if err := s.statusQuery().Where("statuses.id IN ? AND statuses.deleted_at IS NULL", uniqueInt64s(statusIDs)).Find(&rows).Error; err != nil {
			return nil, err
		}
		if err := s.hydrateStatusRelationships(rows, account); err != nil {
			return nil, err
		}
		for _, row := range rows {
			statuses[row.ID] = row
		}
	}
	out := make([]notificationRequestEntity, 0, len(requests))
	for i := range requests {
		if err := s.hydrateAccountCustomEmojis(&requests[i].FromAccount); err != nil {
			return nil, err
		}
		var lastStatus *serializer.Status
		if status, ok := statuses[requests[i].LastStatusID.Int64]; ok {
			serialized := serializer.StatusFromModel(s.cfg, status, account)
			lastStatus = &serialized
		}
		out = append(out, notificationRequestEntity{
			ID: strconv.FormatInt(requests[i].ID, 10), CreatedAt: requests[i].CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: requests[i].UpdatedAt.UTC().Format(time.RFC3339Nano),
			NotificationsCount: strconv.FormatInt(requests[i].NotificationsCount, 10), Account: serializer.AccountFromModel(s.cfg, requests[i].FromAccount), LastStatus: lastStatus,
		})
	}
	return out, nil
}

func (s *Server) acceptNotificationRequest(c *echo.Context) error {
	return s.mutateNotificationRequests(c, true, false)
}
func (s *Server) dismissNotificationRequest(c *echo.Context) error {
	return s.mutateNotificationRequests(c, false, false)
}
func (s *Server) acceptNotificationRequests(c *echo.Context) error {
	return s.mutateNotificationRequests(c, true, true)
}
func (s *Server) dismissNotificationRequests(c *echo.Context) error {
	return s.mutateNotificationRequests(c, false, true)
}

func (s *Server) mutateNotificationRequests(c *echo.Context, accept bool, bulk bool) error {
	account, _, err := s.requireAccountScope(c, "write", "write:notifications")
	if err != nil {
		return err
	}
	ids := []int64{}
	if bulk {
		ids = uniquePositiveRequestIDs(c)
	} else if id := railsToInt64(c.Param("id")); id > 0 {
		ids = []int64{id}
	}
	if len(ids) == 0 {
		if bulk {
			return renderEmpty(c)
		}
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	pairs := make([]asynqNotificationPairPayload, 0, len(ids))
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var requests []models.NotificationRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("account_id = ? AND id IN ?", account.ID, ids).
			Find(&requests).Error; err != nil {
			return err
		}
		if !bulk && len(requests) == 0 {
			return gorm.ErrRecordNotFound
		}
		permissionKnown := map[int64]bool{}
		for _, request := range requests {
			if permissionKnown[request.FromAccountID] {
				if err := tx.Delete(&models.NotificationRequest{}, request.ID).Error; err != nil {
					return err
				}
				continue
			}
			permissionKnown[request.FromAccountID] = true
			pairs = append(pairs, asynqNotificationPairPayload{Version: asynqPayloadVersion43, AccountID: account.ID, FromAccountID: request.FromAccountID})
			if accept {
				now := time.Now().UTC()
				permission := models.NotificationPermission{AccountID: account.ID, FromAccountID: request.FromAccountID, CreatedAt: now, UpdatedAt: now}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&permission).Error; err != nil {
					return err
				}
			}
			if err := tx.Delete(&models.NotificationRequest{}, request.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	ctx := c.Request().Context()
	queuedUnfilter := false
	for _, pair := range pairs {
		if accept {
			if s.enqueueUnfilterNotificationsTask(ctx, pair.AccountID, pair.FromAccountID) {
				queuedUnfilter = true
				continue
			}
			if err := s.processUnfilterNotifications(ctx, pair, false); err != nil {
				return err
			}
			continue
		}
		if bulk {
			continue
		}
		if s.enqueueFilteredNotificationCleanupTask(pair.AccountID, pair.FromAccountID) {
			continue
		}
		if err := s.processFilteredNotificationCleanup(ctx, pair); err != nil {
			return err
		}
	}
	if accept && len(pairs) > 0 && !queuedUnfilter {
		s.publishNotificationsMerged(account.ID)
	}
	return renderEmpty(c)
}

func uniquePositiveRequestIDs(c *echo.Context) []int64 {
	values := append([]string{}, c.QueryParams()["id[]"]...)
	values = append(values, c.QueryParams()["id"]...)
	_ = c.Request().ParseForm()
	values = append(values, c.Request().Form["id[]"]...)
	values = append(values, c.Request().Form["id"]...)
	if payload, err := oauthRequestJSONPayload(c); err == nil {
		values = appendRequestIDValues(values, payload["id"])
		values = appendRequestIDValues(values, payload["id[]"])
	}
	seen := map[int64]struct{}{}
	out := []int64{}
	for _, raw := range values {
		id := railsToInt64(raw)
		if id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func appendRequestIDValues(values []string, value any) []string {
	switch typed := value.(type) {
	case string:
		return append(values, typed)
	case float64:
		return append(values, strconv.FormatInt(int64(typed), 10))
	case json.Number:
		return append(values, typed.String())
	case []any:
		for _, item := range typed {
			values = appendRequestIDValues(values, item)
		}
	case []string:
		values = append(values, typed...)
	}
	return values
}

func (s *Server) notificationRequestsMerged(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}
	value, _ := s.redisCommand(c.Request().Context(), "GET", redisConfig(s.cfg).prefix+"notification_unfilter_jobs:"+strconv.FormatInt(account.ID, 10))
	pending := int64(0)
	if raw, ok := value.(string); ok {
		pending, _ = strconv.ParseInt(raw, 10, 64)
	}
	return c.JSON(http.StatusOK, map[string]bool{"merged": pending <= 0})
}

func (s *Server) publishNotificationsMerged(accountID int64) {
	if accountID == 0 {
		return
	}
	ctx, cancel := contextWithShortTimeout()
	defer cancel()
	payload := statusStreamPayload("notifications_merged", "1")
	_, _ = s.redisCommand(ctx, "PUBLISH", notificationStreamingChannel(redisConfig(s.cfg).prefix, accountID), payload)
}

func contextWithShortTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 250*time.Millisecond)
}

func notificationStatusValue(notification serializer.Notification) serializer.Status {
	if notification.Status == nil {
		return serializer.Status{}
	}
	return *notification.Status
}
