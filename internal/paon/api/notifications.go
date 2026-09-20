package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

var notificationTypes = map[string]struct{}{
	"mention":               {},
	"status":                {},
	"reblog":                {},
	"follow":                {},
	"follow_request":        {},
	"favourite":             {},
	"poll":                  {},
	"update":                {},
	"admin.sign_up":         {},
	"admin.report":          {},
	"severed_relationships": {},
	"moderation_warning":    {},
}

func (s *Server) notifications(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}

	query := s.notificationQuery(account.ID)
	query = applyNotificationFilters(c, query)
	query = applyNotificationFilteredVisibility(c, query)
	if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id") {
		query = query.Where("notifications.id > ?", minID).Order("notifications.id ASC")
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("notifications.id < ?", maxID)
		}
	} else {
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("notifications.id < ?", maxID)
		}
		if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
			query = query.Where("notifications.id > ?", sinceID)
		}
		query = query.Order("notifications.id DESC")
	}

	limitValue := limit(c, 40, 80)
	var notifications []models.Notification
	if err := query.Limit(limitValue).Find(&notifications).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(notifications)
	}
	if err := s.hydrateNotificationStatuses(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationReports(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationSpecialEvents(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationAccounts(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationStatusRelationships(notifications, account); err != nil {
		return err
	}

	if len(notifications) > 0 {
		c.Response().Header().Set("Link", notificationPaginationLink(c, notifications[0].ID, notifications[len(notifications)-1].ID))
	}

	return c.JSON(http.StatusOK, serializeNotificationsWithFilters(s.cfg, notifications, account, s.accountFilters(account)))
}

func notificationPaginationLink(c *echo.Context, first int64, last int64) string {
	return paginationLinkWithAllowedParams(c, first, last, "min_id", true, true, []string{"limit", "account_id", "types[]", "exclude_types[]", "include_filtered"})
}

func (s *Server) showNotification(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "read", "read:notifications")
	if err != nil {
		return err
	}

	var notification models.Notification
	err = s.notificationQuery(account.ID).Where("notifications.id = ?", c.Param("id")).First(&notification).Error
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	notifications := []models.Notification{notification}
	if err := s.hydrateNotificationStatuses(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationReports(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationSpecialEvents(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationAccounts(notifications); err != nil {
		return err
	}
	if err := s.hydrateNotificationStatusRelationships(notifications, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, notificationWithStatusFilters(s.cfg, notifications[0], account, s.accountFilters(account)))
}

func (s *Server) clearNotifications(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:notifications")
	if err != nil {
		return err
	}
	if err := s.db.Where("account_id = ?", account.ID).Delete(&models.Notification{}).Error; err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) dismissNotification(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:notifications")
	if err != nil {
		return err
	}
	res := s.db.Where("account_id = ? AND id = ?", account.ID, c.Param("id")).Delete(&models.Notification{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return renderEmpty(c)
}

func (s *Server) notificationQuery(accountID int64) *gorm.DB {
	return accountRelationSerializerPreloads(s.db.Model(&models.Notification{}), "FromAccount").
		Joins("JOIN accounts from_accounts ON from_accounts.id = notifications.from_account_id").
		Where("notifications.account_id = ? AND from_accounts.suspended_at IS NULL", accountID)
}

func applyNotificationFilters(c *echo.Context, query *gorm.DB) *gorm.DB {
	types, typesRequested := notificationFilterValues(c, "types[]")
	excludeTypes, _ := notificationFilterValues(c, "exclude_types[]")
	if typesRequested && len(types) == 0 {
		query = query.Where("1 = 0")
	}
	if len(types) > 0 {
		query = query.Where(notificationFilterTypeSQL()+" IN ?", types)
	}
	if len(excludeTypes) > 0 {
		query = query.Where(notificationFilterTypeSQL()+" NOT IN ?", excludeTypes)
	}
	if rawAccountID := c.QueryParam("account_id"); rawAccountID != "" {
		accountID, ok := notificationAccountIDFilter(rawAccountID)
		if !ok {
			return query.Where("1 = 0")
		}
		query = query.Where("notifications.from_account_id = ?", accountID)
	}
	return query
}

func applyNotificationFilteredVisibility(c *echo.Context, query *gorm.DB) *gorm.DB {
	if truthy(c.QueryParam("include_filtered")) || strings.TrimSpace(c.QueryParam("account_id")) != "" {
		return query
	}
	return query.Where("notifications.filtered = ?", false)
}

func notificationAccountIDFilter(raw string) (int64, bool) {
	accountID, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return accountID, err == nil && accountID > 0
}

func notificationFilterTypeSQL() string {
	return notificationTypeSQL()
}

func notificationTypeSQL() string {
	return `COALESCE(notifications.type, CASE notifications.activity_type
		WHEN 'Mention' THEN 'mention'
		WHEN 'Status' THEN 'reblog'
		WHEN 'Follow' THEN 'follow'
		WHEN 'FollowRequest' THEN 'follow_request'
		WHEN 'Favourite' THEN 'favourite'
		WHEN 'Poll' THEN 'poll'
		ELSE notifications.activity_type
	END)`
}

func notificationFilterValues(c *echo.Context, keys ...string) ([]string, bool) {
	values := c.QueryParams()
	out := []string{}
	seen := map[string]struct{}{}
	requested := false
	for _, key := range keys {
		for _, value := range values[key] {
			requested = true
			kind := value
			if _, ok := notificationTypes[kind]; !ok {
				continue
			}
			if _, ok := seen[kind]; ok {
				continue
			}
			seen[kind] = struct{}{}
			out = append(out, kind)
		}
	}
	return out, requested
}

func (s *Server) hydrateNotificationStatuses(notifications []models.Notification) error {
	for i := range notifications {
		status, err := s.notificationTargetStatus(notifications[i])
		if err != nil {
			return err
		}
		notifications[i].TargetStatus = status
	}
	return nil
}

func (s *Server) hydrateNotificationReports(notifications []models.Notification) error {
	ids := make([]int64, 0, len(notifications))
	indexesByID := map[int64][]int{}
	for i := range notifications {
		if notifications[i].ResolvedType() != "admin.report" || notifications[i].ActivityType != "Report" || notifications[i].ActivityID == 0 {
			continue
		}
		if _, ok := indexesByID[notifications[i].ActivityID]; !ok {
			ids = append(ids, notifications[i].ActivityID)
		}
		indexesByID[notifications[i].ActivityID] = append(indexesByID[notifications[i].ActivityID], i)
	}
	if len(ids) == 0 {
		return nil
	}
	var reports []models.Report
	if err := accountRelationSerializerPreloads(s.db, "TargetAccount").Where("id IN ?", ids).Find(&reports).Error; err != nil {
		return err
	}
	for i := range reports {
		for _, notificationIndex := range indexesByID[reports[i].ID] {
			notifications[notificationIndex].Report = &reports[i]
		}
	}
	return nil
}

func (s *Server) hydrateNotificationSpecialEvents(notifications []models.Notification) error {
	warningIndexes := map[int64][]int{}
	severanceIndexes := map[int64][]int{}
	for i := range notifications {
		switch notifications[i].ResolvedType() {
		case "moderation_warning":
			warningIndexes[notifications[i].ActivityID] = append(warningIndexes[notifications[i].ActivityID], i)
		case "severed_relationships":
			severanceIndexes[notifications[i].ActivityID] = append(severanceIndexes[notifications[i].ActivityID], i)
		}
	}
	if len(warningIndexes) > 0 {
		ids := make([]int64, 0, len(warningIndexes))
		for id := range warningIndexes {
			ids = append(ids, id)
		}
		var warnings []models.AccountWarning
		query := accountRelationSerializerPreloads(s.db.Model(&models.AccountWarning{}), "TargetAccount")
		if err := query.Where("account_warnings.id IN ?", ids).Find(&warnings).Error; err != nil {
			return err
		}
		for i := range warnings {
			if err := s.hydrateAccountCustomEmojis(&warnings[i].TargetAccount); err != nil {
				return err
			}
			for _, index := range warningIndexes[warnings[i].ID] {
				notifications[index].AccountWarning = &warnings[i]
			}
		}
	}
	if len(severanceIndexes) > 0 {
		ids := make([]int64, 0, len(severanceIndexes))
		for id := range severanceIndexes {
			ids = append(ids, id)
		}
		var events []models.AccountRelationshipSeveranceEvent
		if err := s.db.Preload("RelationshipSeveranceEvent").Where("id IN ?", ids).Find(&events).Error; err != nil {
			return err
		}
		for i := range events {
			for _, index := range severanceIndexes[events[i].ID] {
				if events[i].AccountID == notifications[index].AccountID {
					notifications[index].SeveranceEvent = &events[i]
				}
			}
		}
	}
	return nil
}

func (s *Server) hydrateNotificationAccounts(notifications []models.Notification) error {
	for i := range notifications {
		if err := s.hydrateAccountCustomEmojis(&notifications[i].FromAccount); err != nil {
			return err
		}
		if notifications[i].Report != nil && notifications[i].Report.TargetAccount.ID != 0 {
			if err := s.hydrateAccountCustomEmojis(&notifications[i].Report.TargetAccount); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) hydrateNotificationStatusRelationships(notifications []models.Notification, account *models.Account) error {
	statuses := make([]models.Status, 0, len(notifications))
	indexes := make([]int, 0, len(notifications))
	for i := range notifications {
		if notifications[i].TargetStatus == nil {
			continue
		}
		statuses = append(statuses, *notifications[i].TargetStatus)
		indexes = append(indexes, i)
	}
	if err := s.hydrateStatusRelationships(statuses, account); err != nil {
		return err
	}
	for i, notificationIndex := range indexes {
		notifications[notificationIndex].TargetStatus = &statuses[i]
	}
	return nil
}

func (s *Server) notificationTargetStatus(notification models.Notification) (*models.Status, error) {
	switch notification.ResolvedType() {
	case "status", "update":
		return s.findStatusByID(notification.ActivityID)
	case "reblog":
		var reblog models.Status
		if err := s.db.Where("id = ? AND deleted_at IS NULL", notification.ActivityID).First(&reblog).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		if !reblog.ReblogOfID.Valid {
			return nil, nil
		}
		return s.findStatusByID(reblog.ReblogOfID.Int64)
	case "favourite":
		var favourite models.Favourite
		if err := s.db.Where("id = ?", notification.ActivityID).First(&favourite).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return s.findStatusByID(favourite.StatusID)
	case "mention":
		var mention models.Mention
		if err := s.db.Where("id = ?", notification.ActivityID).First(&mention).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		if !mention.StatusID.Valid {
			return nil, nil
		}
		return s.findStatusByID(mention.StatusID.Int64)
	case "poll":
		statusID, err := s.pollStatusID(notification.ActivityID)
		if err != nil || !statusID.Valid {
			return nil, err
		}
		return s.findStatusByID(statusID.Int64)
	default:
		return nil, nil
	}
}

func (s *Server) findStatusByID(id int64) (*models.Status, error) {
	status, err := s.findStatus(strconv.FormatInt(id, 10))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return status, err
}

func (s *Server) pollStatusID(id int64) (sql.NullInt64, error) {
	var row struct {
		StatusID sql.NullInt64 `gorm:"column:status_id"`
	}
	err := s.db.Table("polls").Select("status_id").Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.NullInt64{}, nil
	}
	return row.StatusID, err
}

func serializeNotifications(cfg config.Config, notifications []models.Notification, current *models.Account) []serializer.Notification {
	return serializeNotificationsWithFilters(cfg, notifications, current, nil)
}

func serializeNotificationsWithFilters(cfg config.Config, notifications []models.Notification, current *models.Account, filters []streamingFilter) []serializer.Notification {
	out := make([]serializer.Notification, 0, len(notifications))
	for _, notification := range notifications {
		out = append(out, notificationWithStatusFilters(cfg, notification, current, filters))
	}
	return out
}
