package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const adminMetricsCacheTTL = 5 * time.Minute

type adminMetricsPayload struct {
	Keys      []string                       `json:"keys"`
	StartAt   string                         `json:"start_at"`
	EndAt     string                         `json:"end_at"`
	Frequency string                         `json:"frequency"`
	Limit     int                            `json:"limit"`
	Params    map[string]adminMetricKeyParam `json:"-"`
}

type adminMetricKeyParam struct {
	Domain            string `json:"domain"`
	IncludeSubdomains bool   `json:"include_subdomains"`
	ID                string `json:"id"`
}

func (s *Server) adminMeasures(c *echo.Context) error {
	if _, err := s.requireAdminReadWithPermissions(c, nil, rolePermissionViewDashboard); err != nil {
		return err
	}
	payload, err := parseAdminMetricsPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if len(payload.Keys) == 0 {
		return c.JSON(http.StatusOK, []serializer.AdminMeasure{})
	}
	start, end := adminMetricsRange(payload.StartAt, payload.EndAt)
	out := make([]serializer.AdminMeasure, 0, len(payload.Keys))
	for _, key := range payload.Keys {
		out = append(out, s.cachedAdminMeasure(key, start, end, payload.Params[key]))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) adminDimensions(c *echo.Context) error {
	if _, err := s.requireAdminReadWithPermissions(c, nil, rolePermissionViewDashboard); err != nil {
		return err
	}
	payload, err := parseAdminMetricsPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	start, end := adminMetricsRange(payload.StartAt, payload.EndAt)
	out := make([]serializer.AdminDimension, 0, len(payload.Keys))
	for _, key := range payload.Keys {
		out = append(out, s.cachedAdminDimension(key, payload.Limit, payload.Params[key], start, end))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) adminRetention(c *echo.Context) error {
	if _, err := s.requireAdminReadWithPermissions(c, nil, rolePermissionViewDashboard); err != nil {
		return err
	}
	payload, err := parseAdminMetricsPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	start, _ := adminMetricsRange(payload.StartAt, payload.EndAt)
	frequency := payload.Frequency
	if frequency != "month" {
		frequency = "day"
	}
	return c.JSON(http.StatusOK, s.cachedAdminRetentionCohorts(start, payload.EndAt, frequency))
}

func parseAdminMetricsPayload(c *echo.Context) (adminMetricsPayload, error) {
	payload := adminMetricsPayload{Params: map[string]adminMetricKeyParam{}}
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "json") {
		raw := map[string]json.RawMessage{}
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		_ = json.Unmarshal(raw["keys"], &payload.Keys)
		_ = json.Unmarshal(raw["start_at"], &payload.StartAt)
		_ = json.Unmarshal(raw["end_at"], &payload.EndAt)
		_ = json.Unmarshal(raw["frequency"], &payload.Frequency)
		_ = json.Unmarshal(raw["limit"], &payload.Limit)
		for _, key := range payload.Keys {
			var param adminMetricKeyParam
			if rawParam, ok := raw[key]; ok {
				_ = json.Unmarshal(rawParam, &param)
			}
			payload.Params[key] = param
		}
		return payload, nil
	}
	values, err := c.FormValues()
	if err != nil {
		return payload, nil
	}
	payload.Keys = adminMetricKeysFromValues(values)
	payload.StartAt = values.Get("start_at")
	payload.EndAt = values.Get("end_at")
	payload.Frequency = values.Get("frequency")
	payload.Limit = intParam(values.Get("limit"), 0)
	for _, key := range payload.Keys {
		payload.Params[key] = adminMetricParamFromValues(values, key)
	}
	return payload, nil
}

func adminMetricKeysFromValues(values map[string][]string) []string {
	raw := append([]string{}, values["keys[]"]...)
	raw = append(raw, values["keys"]...)
	out := []string{}
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func adminMetricParamFromValues(values map[string][]string, key string) adminMetricKeyParam {
	return adminMetricKeyParam{
		Domain:            strings.TrimSpace(firstNonEmpty(lastFormValue(values, key+"[domain]"), lastFormValue(values, "params["+key+"][domain]"))),
		IncludeSubdomains: truthy(firstNonEmpty(lastFormValue(values, key+"[include_subdomains]"), lastFormValue(values, "params["+key+"][include_subdomains]"))),
		ID:                strings.TrimSpace(firstNonEmpty(lastFormValue(values, key+"[id]"), lastFormValue(values, "params["+key+"][id]"))),
	}
}

func (s *Server) cachedAdminMeasure(key string, start time.Time, end time.Time, params adminMetricKeyParam) serializer.AdminMeasure {
	cacheKey := adminMeasureCacheKey(key, start, end, params)
	var measure serializer.AdminMeasure
	if s.adminMetricsCacheRead(cacheKey, &measure) {
		return measure
	}
	measure = s.adminMeasure(key, start, end, params)
	s.adminMetricsCacheWrite(cacheKey, measure)
	return measure
}

func (s *Server) adminMeasure(key string, start time.Time, end time.Time, params adminMetricKeyParam) serializer.AdminMeasure {
	total := int64(0)
	if s.db != nil || adminMeasureUsesRedisOnly(key) {
		total = s.adminMeasureTotal(key, start, end, params)
	}
	measure := serializer.AdminMeasure{
		Key:   key,
		Total: intString(total),
		Data:  s.adminMeasureData(key, start, end, params),
	}
	if adminMeasureTotalInTimeRange(key) {
		previous := int64(0)
		if s.db != nil || adminMeasureUsesRedisOnly(key) {
			length := end.Sub(start)
			previous = s.adminMeasureTotal(key, start.Add(-length), end.Add(-length), params)
		}
		previousText := intString(previous)
		measure.PreviousTotal = &previousText
	}
	if key == "instance_media_attachments" {
		unit := "bytes"
		humanValue := humanBytes(total)
		measure.Unit = &unit
		measure.HumanValue = &humanValue
	}
	return measure
}

func (s *Server) cachedAdminDimension(key string, limitValue int, params adminMetricKeyParam, start time.Time, end time.Time) serializer.AdminDimension {
	cacheKey := adminDimensionCacheKey(key, start, end, limitValue, params)
	var dimension serializer.AdminDimension
	if s.adminMetricsCacheRead(cacheKey, &dimension) {
		return dimension
	}
	dimension = s.adminDimension(key, limitValue, params, start, end)
	s.adminMetricsCacheWrite(cacheKey, dimension)
	return dimension
}

func (s *Server) adminMeasureTotal(key string, start time.Time, end time.Time, params adminMetricKeyParam) int64 {
	switch key {
	case "active_users":
		return s.adminActivityTrackerTotal("activity:logins", true, start, end)
	case "interactions":
		return s.adminActivityTrackerTotal("activity:interactions", false, start, end)
	case "new_users":
		return countBetween(s.db.Model(&models.User{}), "created_at", start, end)
	case "opened_reports":
		return countBetween(s.db.Model(&models.Report{}), "created_at", start, end)
	case "resolved_reports":
		return countBetween(s.db.Model(&models.Report{}).Where("action_taken_at IS NOT NULL"), "action_taken_at", start, end)
	case "instance_accounts":
		return countRows(scopeAccountsByDomain(s.db.Model(&models.Account{}), params, "accounts.domain"))
	case "instance_follows":
		return countRows(scopeAccountsByDomain(s.db.Model(&models.Follow{}).Joins("JOIN accounts ON accounts.id = follows.target_account_id"), params, "accounts.domain"))
	case "instance_followers":
		return countRows(scopeAccountsByDomain(s.db.Model(&models.Follow{}).Joins("JOIN accounts ON accounts.id = follows.account_id"), params, "accounts.domain"))
	case "instance_statuses":
		return countRows(scopeAccountsByDomain(s.db.Model(&models.Status{}).Joins("JOIN accounts ON accounts.id = statuses.account_id"), params, "accounts.domain"))
	case "instance_reports":
		return countRows(scopeAccountsByDomain(s.db.Model(&models.Report{}).Joins("JOIN accounts ON accounts.id = reports.target_account_id"), params, "accounts.domain"))
	case "instance_media_attachments":
		var total int64
		_ = scopeAccountsByDomain(s.db.Model(&models.MediaAttachment{}).Select("COALESCE(SUM(COALESCE(file_file_size, 0) + COALESCE(thumbnail_file_size, 0)), 0)").Joins("JOIN accounts ON accounts.id = media_attachments.account_id"), params, "accounts.domain").Scan(&total).Error
		return total
	case "tag_accounts":
		return s.adminTagAccountsTotal(params, start, end)
	case "tag_uses":
		return s.adminTagUsesTotal(params, start, end)
	case "tag_servers":
		return s.adminTagServersTotal(params, start, end)
	default:
		return 0
	}
}

func (s *Server) adminMeasureData(key string, start time.Time, end time.Time, params adminMetricKeyParam) []serializer.AdminMeasureData {
	days := metricDays(start, end)
	data := make([]serializer.AdminMeasureData, 0, len(days))
	for _, day := range days {
		value := int64(0)
		if s.db != nil || adminMeasureUsesRedisOnly(key) {
			value = s.adminMeasureDailyTotal(key, day, params)
		}
		data = append(data, serializer.AdminMeasureData{Date: day.Format(time.RFC3339), Value: intString(value)})
	}
	return data
}

func (s *Server) adminMeasureDailyTotal(key string, day time.Time, params adminMetricKeyParam) int64 {
	next := day.AddDate(0, 0, 1)
	switch key {
	case "active_users":
		return s.adminActivityTrackerDailyTotal("activity:logins", true, day)
	case "interactions":
		return s.adminActivityTrackerDailyTotal("activity:interactions", false, day)
	case "new_users":
		return countBetween(s.db.Model(&models.User{}), "created_at", day, next)
	case "opened_reports":
		return countBetween(s.db.Model(&models.Report{}), "created_at", day, next)
	case "resolved_reports":
		return countBetween(s.db.Model(&models.Report{}).Where("action_taken_at IS NOT NULL"), "action_taken_at", day, next)
	case "instance_accounts":
		return countBetween(scopeAccountsByDomain(s.db.Model(&models.Account{}), params, "accounts.domain"), "accounts.created_at", day, next)
	case "instance_follows":
		return countBetween(scopeAccountsByDomain(s.db.Model(&models.Follow{}).Joins("JOIN accounts ON accounts.id = follows.target_account_id"), params, "accounts.domain"), "follows.created_at", day, next)
	case "instance_followers":
		return countBetween(scopeAccountsByDomain(s.db.Model(&models.Follow{}).Joins("JOIN accounts ON accounts.id = follows.account_id"), params, "accounts.domain"), "follows.created_at", day, next)
	case "instance_statuses":
		return countBetween(scopeAccountsByDomain(s.db.Model(&models.Status{}).Joins("JOIN accounts ON accounts.id = statuses.account_id"), params, "accounts.domain"), "statuses.created_at", day, next)
	case "instance_reports":
		return countBetween(scopeAccountsByDomain(s.db.Model(&models.Report{}).Joins("JOIN accounts ON accounts.id = reports.target_account_id"), params, "accounts.domain"), "reports.created_at", day, next)
	case "instance_media_attachments":
		var total int64
		query := scopeAccountsByDomain(
			s.db.Model(&models.MediaAttachment{}).
				Select("COALESCE(SUM(COALESCE(file_file_size, 0) + COALESCE(thumbnail_file_size, 0)), 0)").
				Joins("JOIN accounts ON accounts.id = media_attachments.account_id"),
			params,
			"accounts.domain",
		).Where("media_attachments.created_at >= ? AND media_attachments.created_at < ?", day, next)
		_ = query.Scan(&total).Error
		return total
	case "tag_accounts":
		return s.adminTagAccountsDailyTotal(params, day)
	case "tag_uses":
		return s.adminTagUsesDailyTotal(params, day)
	case "tag_servers":
		return s.adminTagServersDailyTotal(params, day)
	default:
		return 0
	}
}

func adminMeasureUsesRedisOnly(key string) bool {
	return key == "active_users" || key == "interactions"
}

func adminMeasureTotalInTimeRange(key string) bool {
	switch key {
	case "instance_accounts", "instance_follows", "instance_followers", "instance_statuses", "instance_reports", "instance_media_attachments":
		return false
	default:
		return true
	}
}

func (s *Server) adminDimension(key string, limitValue int, params adminMetricKeyParam, start time.Time, end time.Time) serializer.AdminDimension {
	if limitValue <= 0 {
		limitValue = 10
	}
	dimension := serializer.AdminDimension{Key: key, Data: []serializer.AdminDimensionData{}}
	if s.db == nil {
		return dimension
	}
	switch key {
	case "languages":
		dimension.Data = s.adminLanguagesDimension(start, end, limitValue)
	case "sources":
		dimension.Data = s.adminSourcesDimension(start, end, limitValue)
	case "servers":
		dimension.Data = s.adminServersDimension(start, end, limitValue)
	case "instance_accounts":
		dimension.Data = s.adminInstanceAccountsDimension(params, limitValue)
	case "software_versions":
		dimension.Data = s.adminSoftwareVersionsDimension()
	case "tag_servers":
		dimension.Data = s.adminTagServersDimension(params, start, end, limitValue)
	case "tag_languages":
		dimension.Data = s.adminTagLanguagesDimension(params, start, end, limitValue)
	case "instance_languages":
		dimension.Data = s.adminInstanceLanguagesDimension(params, start, end, limitValue)
	case "space_usage":
		dimension.Data = s.adminSpaceUsageDimension()
	}
	return dimension
}

func (s *Server) adminLanguagesDimension(start time.Time, end time.Time, limitValue int) []serializer.AdminDimensionData {
	return s.adminDimensionRowsWithHumanKey(
		"users.locale AS key, COUNT(*) AS value",
		"users",
		nil,
		func(query *gorm.DB) *gorm.DB {
			return query.
				Where("users.current_sign_in_at >= ? AND users.current_sign_in_at < ?", start, end).
				Where("users.locale IS NOT NULL")
		},
		limitValue,
		adminMetricStandardLocaleName,
	)
}

func (s *Server) adminSourcesDimension(start time.Time, end time.Time, limitValue int) []serializer.AdminDimensionData {
	type row struct {
		Key   string
		Value int64
	}
	rows := []row{}
	err := s.db.Table("users").
		Select("COALESCE(oauth_applications.name, 'web') AS key, COUNT(*) AS value").
		Joins("LEFT JOIN oauth_applications ON oauth_applications.id = users.created_by_application_id").
		Where("users.created_at >= ? AND users.created_at < ?", start, end).
		Group("key").
		Order("value DESC").
		Limit(limitValue).
		Scan(&rows).Error
	if err != nil {
		return []serializer.AdminDimensionData{}
	}
	out := make([]serializer.AdminDimensionData, 0, len(rows))
	for _, item := range rows {
		humanKey := item.Key
		if item.Key == "web" {
			humanKey = adminT("en", "admin.dashboard.website", "Website")
		}
		out = append(out, serializer.AdminDimensionData{Key: item.Key, HumanKey: humanKey, Value: intString(item.Value)})
	}
	return out
}

func (s *Server) adminServersDimension(start time.Time, end time.Time, limitValue int) []serializer.AdminDimensionData {
	earliestStatusID, latestStatusID := adminMetricStatusIDRange(start, end)
	return s.adminDimensionRows(
		"COALESCE(accounts.domain, ?) AS key, COUNT(*) AS value",
		"statuses",
		[]any{s.cfg.LocalDomain},
		func(query *gorm.DB) *gorm.DB {
			return query.
				Joins("JOIN accounts ON accounts.id = statuses.account_id").
				Where("statuses.id BETWEEN ? AND ?", earliestStatusID, latestStatusID)
		},
		limitValue,
	)
}

func (s *Server) adminSoftwareVersionsDimension() []serializer.AdminDimensionData {
	out := []serializer.AdminDimensionData{
		adminVersionDimensionItem("mastodon", "Paon", s.cfg.Version),
	}
	if version := s.adminPostgreSQLVersion(); version != "" {
		out = append(out, adminVersionDimensionItem("postgresql", "PostgreSQL", version))
	}
	if version := s.adminRedisVersion(); version != "" {
		out = append(out, adminVersionDimensionItem("redis", "Redis", version))
	}
	if version := s.adminMeiliVersion(); version != "" {
		out = append(out, adminVersionDimensionItem("meilisearch", "Meilisearch", version))
	}
	return out
}

func adminVersionDimensionItem(key string, humanKey string, value string) serializer.AdminDimensionData {
	return serializer.AdminDimensionData{Key: key, HumanKey: humanKey, Value: value, HumanValue: &value}
}

func (s *Server) adminPostgreSQLVersion() string {
	if s.db == nil {
		return ""
	}
	var raw string
	if err := s.db.Raw("SELECT VERSION()").Scan(&raw).Error; err != nil {
		return ""
	}
	return postgreSQLVersionFromBanner(raw)
}

func postgreSQLVersionFromBanner(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "PostgreSQL ")
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (s *Server) adminRedisVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, "INFO")
	if err != nil {
		return ""
	}
	info, _ := value.(string)
	return redisInfoValue(info, "redis_version")
}

func (s *Server) adminMeiliVersion() string {
	if !s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.cfg.MeiliHost, "/")+"/version", nil)
	if err != nil {
		return ""
	}
	if s.cfg.MeiliMasterKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.MeiliMasterKey)
	}
	res, err := meiliHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ""
	}
	var payload struct {
		PkgVersion string `json:"pkgVersion"`
	}
	if err := decodeMeiliJSONResponse(res, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.PkgVersion)
}

func (s *Server) adminInstanceAccountsDimension(params adminMetricKeyParam, limitValue int) []serializer.AdminDimensionData {
	type row struct {
		Key   string
		Value int64
	}
	rows := []row{}
	query := scopeAccountsByDomain(s.db.Model(&models.Account{}).Select("accounts.username AS key, COUNT(follows.id) AS value").Joins("LEFT JOIN follows ON follows.target_account_id = accounts.id"), params, "accounts.domain")
	if err := query.Group("accounts.id, accounts.username").Order("value DESC").Limit(limitValue).Scan(&rows).Error; err != nil {
		return []serializer.AdminDimensionData{}
	}
	out := make([]serializer.AdminDimensionData, 0, len(rows))
	for _, item := range rows {
		out = append(out, serializer.AdminDimensionData{Key: item.Key, HumanKey: item.Key, Value: intString(item.Value)})
	}
	return out
}

func (s *Server) adminActivityTrackerTotal(trackerPrefix string, unique bool, start time.Time, end time.Time) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if unique {
		total, err := s.activityTrackerUniqueSum(ctx, trackerPrefix, start, end)
		if err != nil {
			return 0
		}
		return total
	}
	total, err := s.activityTrackerBasicSum(ctx, trackerPrefix, start, end)
	if err != nil {
		return 0
	}
	return total
}

func (s *Server) adminActivityTrackerDailyTotal(trackerPrefix string, unique bool, day time.Time) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := activityTrackerDailyKey(s.cfg.RedisNamespace, trackerPrefix, day)
	command := "GET"
	if unique {
		command = "PFCOUNT"
	}
	value, err := s.redisCommand(ctx, command, key)
	if err != nil {
		return 0
	}
	return redisInt(value)
}

func (s *Server) adminTagUsesTotal(params adminMetricKeyParam, start time.Time, end time.Time) int64 {
	tagID, ok := adminMetricID(params.ID)
	if !ok {
		return 0
	}
	if value, ok := s.adminTagUsesRedisTotal(tagID, start, end); ok {
		return value
	}
	return countRows(adminTagStatusScope(s.db, tagID, start, end))
}

func (s *Server) adminTagAccountsTotal(params adminMetricKeyParam, start time.Time, end time.Time) int64 {
	tagID, ok := adminMetricID(params.ID)
	if !ok {
		return 0
	}
	if value, ok := s.adminTagAccountsRedisTotal(tagID, start, end); ok {
		return value
	}
	return countDistinct(adminTagStatusScope(s.db, tagID, start, end), "statuses.account_id")
}

func (s *Server) adminTagServersTotal(params adminMetricKeyParam, start time.Time, end time.Time) int64 {
	tagID, ok := adminMetricID(params.ID)
	if !ok {
		return 0
	}
	return countDistinct(adminTagStatusScope(s.db, tagID, start, end).Joins("JOIN accounts ON accounts.id = statuses.account_id"), "accounts.domain")
}

func (s *Server) adminTagUsesDailyTotal(params adminMetricKeyParam, day time.Time) int64 {
	tagID, ok := adminMetricID(params.ID)
	if !ok {
		return 0
	}
	if value, ok := s.adminTagUsesRedisDay(tagID, day); ok {
		return value
	}
	return countRows(adminTagStatusScope(s.db, tagID, day, day.AddDate(0, 0, 1)))
}

func (s *Server) adminTagAccountsDailyTotal(params adminMetricKeyParam, day time.Time) int64 {
	tagID, ok := adminMetricID(params.ID)
	if !ok {
		return 0
	}
	if value, ok := s.adminTagAccountsRedisDay(tagID, day); ok {
		return value
	}
	return countDistinct(adminTagStatusScope(s.db, tagID, day, day.AddDate(0, 0, 1)), "statuses.account_id")
}

func (s *Server) adminTagServersDailyTotal(params adminMetricKeyParam, day time.Time) int64 {
	tagID, ok := adminMetricID(params.ID)
	if !ok {
		return 0
	}
	return countDistinct(adminTagStatusScope(s.db, tagID, day, day.AddDate(0, 0, 1)).Joins("JOIN accounts ON accounts.id = statuses.account_id"), "accounts.domain")
}

func adminTagStatusScope(db *gorm.DB, tagID int64, start time.Time, end time.Time) *gorm.DB {
	earliestStatusID, latestStatusID := adminMetricStatusIDRange(start, end)
	return db.Table("statuses").
		Joins("JOIN statuses_tags ON statuses_tags.status_id = statuses.id").
		Where("statuses_tags.tag_id = ?", tagID).
		Where("statuses.id BETWEEN ? AND ?", earliestStatusID, latestStatusID)
}

func countDistinct(query *gorm.DB, expression string) int64 {
	var count int64
	if err := query.Select("COUNT(DISTINCT " + expression + ")").Scan(&count).Error; err != nil {
		return 0
	}
	return count
}

func (s *Server) adminTagUsesRedisTotal(tagID int64, start time.Time, end time.Time) (int64, bool) {
	keys := tagHistoryRedisKeys(s.cfg.RedisNamespace, tagID, start, end, false)
	if len(keys) == 0 {
		return 0, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, append([]string{"MGET"}, keys...)...)
	if err != nil {
		return 0, false
	}
	items, ok := value.([]any)
	if !ok {
		return 0, false
	}
	total := int64(0)
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
			total += parsed
		case int64:
			total += typed
		}
	}
	return total, true
}

func (s *Server) adminTagAccountsRedisTotal(tagID int64, start time.Time, end time.Time) (int64, bool) {
	keys := tagHistoryRedisKeys(s.cfg.RedisNamespace, tagID, start, end, true)
	if len(keys) == 0 {
		return 0, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, append([]string{"PFCOUNT"}, keys...)...)
	if err != nil {
		return 0, false
	}
	total, ok := value.(int64)
	return total, ok
}

func (s *Server) adminTagUsesRedisDay(tagID int64, day time.Time) (int64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, "GET", tagHistoryRedisKey(s.cfg.RedisNamespace, tagID, day, false))
	if err != nil {
		return 0, false
	}
	if text, ok := value.(string); ok {
		parsed, _ := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		return parsed, true
	}
	if number, ok := value.(int64); ok {
		return number, true
	}
	return 0, true
}

func (s *Server) adminTagAccountsRedisDay(tagID int64, day time.Time) (int64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, "PFCOUNT", tagHistoryRedisKey(s.cfg.RedisNamespace, tagID, day, true))
	if err != nil {
		return 0, false
	}
	total, ok := value.(int64)
	return total, ok
}

func tagHistoryRedisKeys(redisPrefix string, tagID int64, start time.Time, end time.Time, accounts bool) []string {
	keys := []string{}
	for day := truncateMetricTime(start, "day"); day.Before(end); day = day.AddDate(0, 0, 1) {
		keys = append(keys, tagHistoryRedisKey(redisPrefix, tagID, day, accounts))
		if len(keys) >= 370 {
			break
		}
	}
	return keys
}

func tagHistoryRedisKey(redisPrefix string, tagID int64, day time.Time, accounts bool) string {
	key := redisPrefix + "activity:tags:" + strconv.FormatInt(tagID, 10) + ":" + strconv.FormatInt(truncateMetricTime(day, "day").Unix(), 10)
	if accounts {
		key += ":accounts"
	}
	return key
}

func (s *Server) adminTagServersDimension(params adminMetricKeyParam, start time.Time, end time.Time, limitValue int) []serializer.AdminDimensionData {
	tagID, ok := adminMetricID(params.ID)
	if !ok {
		return []serializer.AdminDimensionData{}
	}
	earliestStatusID, latestStatusID := adminMetricStatusIDRange(start, end)
	return s.adminDimensionRows(
		"COALESCE(accounts.domain, ?) AS key, COUNT(*) AS value",
		"statuses",
		[]any{s.cfg.LocalDomain},
		func(query *gorm.DB) *gorm.DB {
			return query.
				Joins("JOIN accounts ON accounts.id = statuses.account_id").
				Joins("JOIN statuses_tags ON statuses_tags.status_id = statuses.id").
				Where("statuses_tags.tag_id = ?", tagID).
				Where("statuses.id BETWEEN ? AND ?", earliestStatusID, latestStatusID)
		},
		limitValue,
	)
}

func (s *Server) adminTagLanguagesDimension(params adminMetricKeyParam, start time.Time, end time.Time, limitValue int) []serializer.AdminDimensionData {
	tagID, ok := adminMetricID(params.ID)
	if !ok {
		return []serializer.AdminDimensionData{}
	}
	earliestStatusID, latestStatusID := adminMetricStatusIDRange(start, end)
	return s.adminDimensionRowsWithHumanKey(
		"COALESCE(statuses.language, 'und') AS key, COUNT(*) AS value",
		"statuses",
		nil,
		func(query *gorm.DB) *gorm.DB {
			return query.
				Joins("JOIN statuses_tags ON statuses_tags.status_id = statuses.id").
				Where("statuses_tags.tag_id = ?", tagID).
				Where("statuses.id BETWEEN ? AND ?", earliestStatusID, latestStatusID)
		},
		limitValue,
		adminMetricStandardLocaleName,
	)
}

func (s *Server) adminInstanceLanguagesDimension(params adminMetricKeyParam, start time.Time, end time.Time, limitValue int) []serializer.AdminDimensionData {
	domain := strings.TrimSpace(params.Domain)
	if domain == "" {
		return []serializer.AdminDimensionData{}
	}
	earliestStatusID, latestStatusID := adminMetricStatusIDRange(start, end)
	return s.adminDimensionRowsWithHumanKey(
		"COALESCE(statuses.language, 'und') AS key, COUNT(*) AS value",
		"statuses",
		nil,
		func(query *gorm.DB) *gorm.DB {
			return query.
				Joins("JOIN accounts ON accounts.id = statuses.account_id").
				Where("accounts.domain = ?", domain).
				Where("statuses.reblog_of_id IS NULL").
				Where("statuses.id BETWEEN ? AND ?", earliestStatusID, latestStatusID)
		},
		limitValue,
		adminMetricStandardLocaleName,
	)
}

func (s *Server) adminDimensionRows(selectSQL string, table string, selectArgs []any, scope func(*gorm.DB) *gorm.DB, limitValue int) []serializer.AdminDimensionData {
	return s.adminDimensionRowsWithHumanKey(selectSQL, table, selectArgs, scope, limitValue, func(key string) string { return key })
}

func (s *Server) adminDimensionRowsWithHumanKey(selectSQL string, table string, selectArgs []any, scope func(*gorm.DB) *gorm.DB, limitValue int, humanKey func(string) string) []serializer.AdminDimensionData {
	type row struct {
		Key   string
		Value int64
	}
	rows := []row{}
	query := s.db.Table(table).Select(selectSQL, selectArgs...)
	if scope != nil {
		query = scope(query)
	}
	if err := query.Group("key").Order("value DESC").Limit(limitValue).Scan(&rows).Error; err != nil {
		return []serializer.AdminDimensionData{}
	}
	out := make([]serializer.AdminDimensionData, 0, len(rows))
	for _, item := range rows {
		out = append(out, serializer.AdminDimensionData{Key: item.Key, HumanKey: humanKey(item.Key), Value: intString(item.Value)})
	}
	return out
}

func (s *Server) adminSpaceUsageDimension() []serializer.AdminDimensionData {
	unit := "bytes"
	return []serializer.AdminDimensionData{
		adminSpaceUsageItem("postgresql", s.adminPostgreSQLDatabaseBytes(), unit),
		adminSpaceUsageItem("redis", s.adminRedisMemoryBytes(), unit),
		adminSpaceUsageItem("media", s.adminMediaStorageBytes(), unit),
	}
}

func adminSpaceUsageItem(key string, value int64, unit string) serializer.AdminDimensionData {
	humanValue := humanBytes(value)
	return serializer.AdminDimensionData{Key: key, HumanKey: adminMetricSpaceUsageHumanKey(key), Value: intString(value), Unit: &unit, HumanValue: &humanValue}
}

func adminMetricStandardLocaleName(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return adminT("en", "generic.none", "None")
	}
	for _, language := range serializer.SupportedLanguages() {
		if language.Code == locale {
			return language.Name
		}
	}
	return locale
}

func adminMetricSpaceUsageHumanKey(key string) string {
	switch key {
	case "postgresql":
		return "PostgreSQL"
	case "redis":
		return "Redis"
	case "media":
		return adminT("en", "admin.dashboard.media_storage", "Media storage")
	default:
		return key
	}
}

func (s *Server) adminPostgreSQLDatabaseBytes() int64 {
	var total int64
	if s.db == nil {
		return 0
	}
	if err := s.db.Raw("SELECT pg_database_size(current_database())").Scan(&total).Error; err != nil {
		return 0
	}
	return total
}

func (s *Server) adminRedisMemoryBytes() int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, "INFO", "memory")
	if err != nil {
		return 0
	}
	info, _ := value.(string)
	return redisUsedMemoryFromInfo(info)
}

func (s *Server) adminMediaStorageBytes() int64 {
	if s.db == nil {
		return 0
	}
	total := int64(0)
	total += s.adminStorageSum("media_attachments", "COALESCE(file_file_size, 0) + COALESCE(thumbnail_file_size, 0)")
	total += s.adminStorageSum("custom_emojis", "COALESCE(image_file_size, 0)")
	total += s.adminStorageSum("preview_cards", "COALESCE(image_file_size, 0)")
	total += s.adminStorageSum("accounts", "COALESCE(avatar_file_size, 0) + COALESCE(header_file_size, 0)")
	total += s.adminStorageSum("backups", "COALESCE(dump_file_size, 0)")
	total += s.adminStorageSum("imports", "COALESCE(data_file_size, 0)")
	total += s.adminStorageSum("site_uploads", "COALESCE(file_file_size, 0)")
	return total
}

func (s *Server) adminStorageSum(table string, expression string) int64 {
	var total int64
	if err := s.db.Table(table).Select("COALESCE(SUM(" + expression + "), 0)").Scan(&total).Error; err != nil {
		return 0
	}
	return total
}

func adminMetricID(value string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return id, err == nil && id > 0
}

func redisUsedMemoryFromInfo(info string) int64 {
	value := redisInfoValue(info, "used_memory")
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err == nil && parsed >= 0 {
		return parsed
	}
	return 0
}

func redisInfoValue(info string, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func humanBytes(value int64) string {
	if value < 0 {
		value = 0
	}
	if value == 1 {
		return "1 Byte"
	}
	if value < 1024 {
		return fmt.Sprintf("%d Bytes", value)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	size := float64(value)
	unit := units[0]
	for _, candidate := range units {
		size = size / 1024
		unit = candidate
		if size < 1024 {
			break
		}
	}
	rendered := fmt.Sprintf("%.1f", size)
	rendered = strings.TrimSuffix(strings.TrimSuffix(rendered, "0"), ".")
	return rendered + " " + unit
}

func (s *Server) adminRetentionCohorts(start time.Time, endValue string, frequency string) []serializer.AdminCohort {
	end := parseMetricTime(endValue, time.Now().UTC())
	start = truncateMetricTime(start, frequency)
	end = truncateMetricTime(end, frequency)
	if end.Before(start) {
		end = start
	}
	if cohorts, ok := s.adminRetentionCohortsPostgreSQL(start, end, frequency); ok {
		return cohorts
	}
	periods := metricPeriods(start, end, frequency)
	cohorts := make([]serializer.AdminCohort, 0, len(periods))
	for _, cohortPeriod := range periods {
		cohortSize := s.adminRetentionCohortSize(cohortPeriod, frequency)
		data := make([]serializer.AdminCohortData, 0, len(periods))
		for _, retentionPeriod := range periods {
			if retentionPeriod.Before(cohortPeriod) {
				continue
			}
			value := s.adminRetentionRetainedUsers(cohortPeriod, retentionPeriod, frequency)
			rate := 0.0
			if cohortSize > 0 {
				rate = float64(value) / float64(cohortSize)
			}
			data = append(data, serializer.AdminCohortData{
				Date:  retentionPeriod.Format(time.RFC3339),
				Rate:  rate,
				Value: intString(value),
			})
		}
		cohorts = append(cohorts, serializer.AdminCohort{
			Period:    cohortPeriod.Format(time.RFC3339),
			Frequency: frequency,
			Data:      data,
		})
	}
	return cohorts
}

type adminRetentionRow struct {
	CohortPeriod    time.Time `gorm:"column:cohort_period"`
	RetentionPeriod time.Time `gorm:"column:retention_period"`
	Value           int64     `gorm:"column:value"`
	Rate            float64   `gorm:"column:rate"`
}

func (s *Server) adminRetentionCohortsPostgreSQL(start time.Time, end time.Time, frequency string) ([]serializer.AdminCohort, bool) {
	if s == nil || s.db == nil || s.db.Dialector == nil || s.db.Dialector.Name() != "postgres" {
		return nil, false
	}
	var rows []adminRetentionRow
	query := `
SELECT axis.cohort_period, axis.retention_period, retention.value, retention.rate
FROM (
  WITH cohort_periods AS (
    SELECT generate_series(date_trunc(?, ?::timestamp)::date, date_trunc(?, ?::timestamp)::date, ('1 ' || ?)::interval) AS cohort_period
  ),
  retention_periods AS (
    SELECT cohort_period AS retention_period FROM cohort_periods
  )
  SELECT *
  FROM cohort_periods, retention_periods
  WHERE retention_period >= cohort_period
) AS axis
CROSS JOIN LATERAL (
  WITH new_users AS (
    SELECT users.id
    FROM users
    WHERE date_trunc(?, users.created_at)::date = axis.cohort_period
  ),
  retained_users AS (
    SELECT users.id
    FROM users
    INNER JOIN new_users ON new_users.id = users.id
    WHERE date_trunc(?, users.current_sign_in_at) >= axis.retention_period
  )
  SELECT count(*)::bigint AS value, (count(*))::float / (SELECT GREATEST(count(*), 1) FROM new_users) AS rate
  FROM retained_users
) AS retention
ORDER BY axis.cohort_period ASC, axis.retention_period ASC`
	if err := s.db.Raw(query, frequency, start, frequency, end, frequency, frequency, frequency).Scan(&rows).Error; err != nil {
		return nil, false
	}
	return adminRetentionRowsToCohorts(rows, frequency), true
}

func adminRetentionRowsToCohorts(rows []adminRetentionRow, frequency string) []serializer.AdminCohort {
	cohorts := make([]serializer.AdminCohort, 0)
	for _, row := range rows {
		period := row.CohortPeriod.UTC().Format(time.RFC3339)
		if len(cohorts) == 0 || cohorts[len(cohorts)-1].Period != period {
			cohorts = append(cohorts, serializer.AdminCohort{
				Period:    period,
				Frequency: frequency,
				Data:      []serializer.AdminCohortData{},
			})
		}
		cohorts[len(cohorts)-1].Data = append(cohorts[len(cohorts)-1].Data, serializer.AdminCohortData{
			Date:  row.RetentionPeriod.UTC().Format(time.RFC3339),
			Rate:  row.Rate,
			Value: intString(row.Value),
		})
	}
	return cohorts
}

func (s *Server) cachedAdminRetentionCohorts(start time.Time, endValue string, frequency string) []serializer.AdminCohort {
	cacheKey := adminRetentionCacheKey(start, endValue, frequency)
	var cohorts []serializer.AdminCohort
	if s.adminMetricsCacheRead(cacheKey, &cohorts) {
		return cohorts
	}
	cohorts = s.adminRetentionCohorts(start, endValue, frequency)
	s.adminMetricsCacheWrite(cacheKey, cohorts)
	return cohorts
}

func (s *Server) adminMetricsCacheRead(cacheKey string, out any) bool {
	if s == nil || cacheKey == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, "GET", redisConfig(s.cfg).prefix+cacheKey)
	if err != nil {
		return false
	}
	raw, ok := value.(string)
	return ok && raw != "" && json.Unmarshal([]byte(raw), out) == nil
}

func (s *Server) adminMetricsCacheWrite(cacheKey string, value any) {
	if s == nil || cacheKey == "" {
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.redisCommand(ctx, "SETEX", redisConfig(s.cfg).prefix+cacheKey, strconv.FormatInt(int64(adminMetricsCacheTTL/time.Second), 10), string(raw))
}

func adminMeasureCacheKey(key string, start time.Time, end time.Time, params adminMetricKeyParam) string {
	return strings.Join([]string{"metrics/measure/" + key, adminMetricTimeCacheValue(start), adminMetricTimeCacheValue(end), adminMetricParamsCacheValue(params)}, ";")
}

func adminDimensionCacheKey(key string, start time.Time, end time.Time, limitValue int, params adminMetricKeyParam) string {
	return strings.Join([]string{"metrics/dimension/" + key, adminMetricTimeCacheValue(start), adminMetricTimeCacheValue(end), strconv.Itoa(limitValue), adminMetricParamsCacheValue(params)}, ";")
}

func adminRetentionCacheKey(start time.Time, endValue string, frequency string) string {
	end := truncateMetricTime(parseMetricTime(endValue, time.Now().UTC()), frequency)
	return strings.Join([]string{"metrics/retention", truncateMetricTime(start, frequency).Format("2006-01-02"), end.Format("2006-01-02"), frequency}, ";")
}

func adminMetricTimeCacheValue(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func adminMetricParamsCacheValue(params adminMetricKeyParam) string {
	return strings.Join([]string{
		"domain=" + params.Domain,
		"id=" + params.ID,
		"include_subdomains=" + strconv.FormatBool(params.IncludeSubdomains),
	}, ";")
}

func (s *Server) adminRetentionCohortSize(period time.Time, frequency string) int64 {
	if s.db == nil {
		return 0
	}
	return countBetween(s.db.Model(&models.User{}), "created_at", period, addMetricPeriod(period, frequency))
}

func (s *Server) adminRetentionRetainedUsers(cohortPeriod time.Time, retentionPeriod time.Time, frequency string) int64 {
	if s.db == nil {
		return 0
	}
	query := s.db.Model(&models.User{}).
		Where("created_at >= ? AND created_at < ?", cohortPeriod, addMetricPeriod(cohortPeriod, frequency)).
		Where("current_sign_in_at >= ?", retentionPeriod)
	return countRows(query)
}

func countBetween(query *gorm.DB, column string, start time.Time, end time.Time) int64 {
	return countRows(query.Where(column+" >= ? AND "+column+" < ?", start, end))
}

func countRows(query *gorm.DB) int64 {
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0
	}
	return count
}

func adminMetricStatusIDRange(start time.Time, end time.Time) (int64, int64) {
	return mastodonSnowflakeIDAt(start, false), mastodonSnowflakeIDAt(end, false)
}

func scopeAccountsByDomain(query *gorm.DB, params adminMetricKeyParam, column string) *gorm.DB {
	domain := strings.TrimSpace(params.Domain)
	if domain == "" {
		return query
	}
	if params.IncludeSubdomains {
		return query.Where(column+" = ? OR "+column+" LIKE ?", domain, "%."+domain)
	}
	return query.Where(column+" = ?", domain)
}

func adminMetricsRange(startValue string, endValue string) (time.Time, time.Time) {
	end := parseMetricTime(endValue, time.Now().UTC())
	start := parseMetricTime(startValue, end.AddDate(0, 0, -6))
	start = truncateMetricTime(start, "day")
	end = truncateMetricTime(end, "day").AddDate(0, 0, 1)
	if !start.Before(end) {
		start = end.AddDate(0, 0, -1)
	}
	return start, end
}

func parseMetricTime(value string, fallback time.Time) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return fallback
}

func truncateMetricTime(value time.Time, frequency string) time.Time {
	value = value.UTC()
	if frequency == "month" {
		return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func metricDays(start time.Time, end time.Time) []time.Time {
	days := []time.Time{}
	for day := truncateMetricTime(start, "day"); day.Before(end); day = day.AddDate(0, 0, 1) {
		days = append(days, day)
		if len(days) >= 370 {
			break
		}
	}
	if len(days) == 0 {
		days = append(days, truncateMetricTime(time.Now().UTC(), "day"))
	}
	return days
}

func metricPeriods(start time.Time, end time.Time, frequency string) []time.Time {
	periods := []time.Time{}
	for period := truncateMetricTime(start, frequency); !period.After(end); period = addMetricPeriod(period, frequency) {
		periods = append(periods, period)
		if len(periods) >= 370 {
			break
		}
	}
	if len(periods) == 0 {
		periods = append(periods, truncateMetricTime(time.Now().UTC(), frequency))
	}
	return periods
}

func addMetricPeriod(value time.Time, frequency string) time.Time {
	if frequency == "month" {
		return value.AddDate(0, 1, 0)
	}
	return value.AddDate(0, 0, 1)
}

func intString(value int64) string {
	return strconv.FormatInt(value, 10)
}
