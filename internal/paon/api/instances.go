package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

type instanceStats = serializer.InstanceStats
type deliveryHistory = serializer.DeliveryHistory

type instanceActivityWeek struct {
	Week          string `json:"week"`
	Statuses      string `json:"statuses"`
	Logins        string `json:"logins"`
	Registrations string `json:"registrations"`
}

const activityTrackerExpireAfter = 15778476 * time.Second

func (s *Server) instanceStatsV2(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	c.Response().Header().Set("Cache-Control", "no-store")
	now := time.Now().UTC()
	histories := zeroDeliveryHistories(now, 72)
	if redisHistories, err := s.deliveryHistories(c.Request().Context(), c.Param("domain"), now, 72); err == nil {
		histories = redisHistories
	}
	return c.JSON(http.StatusOK, instanceStats{DeliveryHistories: histories})
}

func (s *Server) instancePeers(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	if !s.peersAPIEnabled() {
		return c.NoContent(http.StatusNotFound)
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 86400)
	if s.db == nil {
		return c.JSON(http.StatusOK, []string{})
	}
	var instances []models.Instance
	err := s.db.Model(&models.Instance{}).
		Where("NOT EXISTS (SELECT 1 FROM domain_blocks WHERE lower(domain_blocks.domain) = lower(instances.domain))").
		Order("domain ASC").
		Find(&instances).Error
	if err != nil {
		return err
	}
	out := make([]string, 0, len(instances))
	for _, instance := range instances {
		out = append(out, instance.Domain)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) instanceRules(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	rules, err := s.instanceRuleModels()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.InstanceRulesFromModels(rules))
}

func (s *Server) instanceRuleModels() ([]models.Rule, error) {
	if s.db == nil {
		return []models.Rule{}, nil
	}
	var rules []models.Rule
	err := s.db.Where("deleted_at IS NULL").Order("priority ASC, id ASC").Find(&rules).Error
	return rules, err
}

func (s *Server) instanceLanguages(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	languages := serializer.SupportedLanguages()
	out := make([]serializer.Language, 0, len(languages))
	for _, language := range languages {
		out = append(out, serializer.Language{Code: language.Code, Name: language.Name})
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) instanceActivity(c *echo.Context) error {
	if err := s.requireAuthenticatedAPIInLimitedFederation(c); err != nil {
		return err
	}
	if !s.publicActivityAPIEnabled() {
		return c.NoContent(http.StatusNotFound)
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 86400)
	weeks, err := s.instanceActivityWeeks(c.Request().Context(), time.Now().UTC())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, weeks)
}

func (s *Server) instanceActivityWeeks(ctx context.Context, now time.Time) ([]instanceActivityWeek, error) {
	if weeks, err := s.instanceActivityWeeksFromRedis(ctx, now); err == nil {
		return weeks, nil
	}
	return zeroInstanceActivityWeeks(now, 12), nil
}

func (s *Server) instanceActivityWeeksFromRedis(ctx context.Context, now time.Time) ([]instanceActivityWeek, error) {
	weeks := zeroInstanceActivityWeeks(now, 12)
	for i := range weeks {
		start := now.AddDate(0, 0, -7*i)
		end := start.AddDate(0, 0, 6)
		statuses, err := s.activityTrackerBasicSum(ctx, "activity:statuses:local", start, end)
		if err != nil {
			return nil, err
		}
		logins, err := s.activityTrackerUniqueSum(ctx, "activity:logins", start, end)
		if err != nil {
			return nil, err
		}
		registrations, err := s.activityTrackerBasicSum(ctx, "activity:accounts:local", start, end)
		if err != nil {
			return nil, err
		}
		weeks[i].Statuses = strconv.FormatInt(statuses, 10)
		weeks[i].Logins = strconv.FormatInt(logins, 10)
		weeks[i].Registrations = strconv.FormatInt(registrations, 10)
	}
	return weeks, nil
}

func zeroInstanceActivityWeeks(now time.Time, count int) []instanceActivityWeek {
	weeks := make([]instanceActivityWeek, 0, count)
	for i := 0; i < count; i++ {
		week := now.AddDate(0, 0, -7*i).Unix()
		weeks = append(weeks, instanceActivityWeek{
			Week:          strconv.FormatInt(week, 10),
			Statuses:      "0",
			Logins:        "0",
			Registrations: "0",
		})
	}
	return weeks
}

func (s *Server) activityTrackerBasicSum(ctx context.Context, trackerPrefix string, start time.Time, end time.Time) (int64, error) {
	keys := activityTrackerRedisKeys(s.cfg.RedisNamespace, trackerPrefix, start, end)
	if len(keys) == 0 {
		return 0, nil
	}
	value, err := s.redisCommand(ctx, append([]string{"MGET"}, keys...)...)
	if err != nil {
		return 0, err
	}
	values, ok := value.([]any)
	if !ok {
		return 0, nil
	}
	var total int64
	for _, value := range values {
		total += redisInt(value)
	}
	return total, nil
}

func (s *Server) activityTrackerIncrementBasic(ctx context.Context, trackerPrefix string, at time.Time, value int64) {
	if s == nil || value == 0 {
		return
	}
	ctx, cancel := activityTrackerWriteContext(ctx)
	defer cancel()
	key := activityTrackerDailyKey(s.cfg.RedisNamespace, trackerPrefix, at)
	ttl := strconv.FormatInt(int64(activityTrackerExpireAfter/time.Second), 10)
	_, _ = s.redisCommand(ctx, "INCRBY", key, strconv.FormatInt(value, 10))
	_, _ = s.redisCommand(ctx, "EXPIRE", key, ttl)
}

func (s *Server) activityTrackerRecordUnique(ctx context.Context, trackerPrefix string, at time.Time, value int64) {
	if s == nil || value == 0 {
		return
	}
	ctx, cancel := activityTrackerWriteContext(ctx)
	defer cancel()
	key := activityTrackerDailyKey(s.cfg.RedisNamespace, trackerPrefix, at)
	ttl := strconv.FormatInt(int64(activityTrackerExpireAfter/time.Second), 10)
	_, _ = s.redisCommand(ctx, "PFADD", key, strconv.FormatInt(value, 10))
	_, _ = s.redisCommand(ctx, "EXPIRE", key, ttl)
}

func activityTrackerWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), 250*time.Millisecond)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 250*time.Millisecond)
}

func (s *Server) activityTrackerUniqueSum(ctx context.Context, trackerPrefix string, start time.Time, end time.Time) (int64, error) {
	keys := activityTrackerRedisKeys(s.cfg.RedisNamespace, trackerPrefix, start, end)
	if len(keys) == 0 {
		return 0, nil
	}
	value, err := s.redisCommand(ctx, append([]string{"PFCOUNT"}, keys...)...)
	if err != nil {
		return 0, err
	}
	return redisInt(value), nil
}

func activityTrackerRedisKeys(redisPrefix string, trackerPrefix string, start time.Time, end time.Time) []string {
	keys := make([]string, 0, 14)
	seen := map[string]struct{}{}
	for date := dayStart(start); date.Before(dayStart(end)); date = date.AddDate(0, 0, 1) {
		for _, key := range []string{
			activityTrackerDailyKey(redisPrefix, trackerPrefix, date),
			activityTrackerLegacyWeekKey(redisPrefix, trackerPrefix, date),
		} {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

func activityTrackerDailyKey(redisPrefix string, trackerPrefix string, at time.Time) string {
	return redisPrefix + trackerPrefix + ":" + strconv.FormatInt(dayStart(at).Unix(), 10)
}

func activityTrackerLegacyWeekKey(redisPrefix string, trackerPrefix string, at time.Time) string {
	_, week := at.UTC().ISOWeek()
	return redisPrefix + trackerPrefix + ":" + strconv.Itoa(week)
}

func dayStart(at time.Time) time.Time {
	utc := at.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Server) activityAPIEnabled() bool {
	return s.settingBoolValue("activity_api_enabled", true)
}

func (s *Server) publicActivityAPIEnabled() bool {
	return s.activityAPIEnabled() && !s.cfg.LimitedFederationMode
}

func (s *Server) peersAPIEnabled() bool {
	return s.settingBoolValue("peers_api_enabled", true) && !s.cfg.LimitedFederationMode
}

func (s *Server) peerSearch(c *echo.Context) error {
	if !s.peersAPIEnabled() {
		return c.NoContent(http.StatusNotFound)
	}
	if err := s.requireAuthenticatedAPIIfDisallowed(c); err != nil {
		return err
	}
	s.publicRESTCacheEvenIfAuthenticated(c, 300)
	domain := normalizePeerSearch(c.QueryParam("q"))
	if domain == "" {
		return c.JSON(http.StatusOK, nil)
	}

	if domains, err := s.searchMeiliInstanceDomains(c.Request().Context(), domain, 10); err == nil {
		return c.JSON(http.StatusOK, domains)
	}
	if s.db == nil {
		return c.JSON(http.StatusOK, []string{})
	}

	var instances []models.Instance
	err := s.db.Model(&models.Instance{}).
		Where("lower(domain) LIKE ? ESCAPE '\\'", strings.ToLower(escapeLikePattern(domain))+"%").
		Where("NOT EXISTS (SELECT 1 FROM domain_blocks WHERE lower(domain_blocks.domain) = lower(instances.domain))").
		Order("accounts_count DESC NULLS LAST").
		Limit(10).
		Find(&instances).Error
	if err != nil {
		return c.JSON(http.StatusOK, []string{})
	}
	out := make([]string, 0, len(instances))
	for _, instance := range instances {
		out = append(out, instance.Domain)
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) deliveryHistories(ctx context.Context, domain string, now time.Time, hours int) ([]deliveryHistory, error) {
	histories := zeroDeliveryHistories(now, hours)
	host := normalizeDeliveryStatsHost(domain)
	if host == "" {
		return histories, nil
	}
	keys := deliveryStatsRedisKeys(s.cfg.RedisNamespace, host, histories)
	args := append([]string{"MGET"}, keys...)
	value, err := s.redisCommand(ctx, args...)
	if err != nil {
		return nil, err
	}
	values, ok := value.([]any)
	if !ok {
		return histories, nil
	}
	applyDeliveryStatsValues(histories, values)
	return histories, nil
}

func zeroDeliveryHistories(now time.Time, hours int) []deliveryHistory {
	start := now.UTC().Add(-time.Duration(hours) * time.Hour).Truncate(time.Hour)
	end := now.UTC().Truncate(time.Hour).Add(time.Hour)
	out := make([]deliveryHistory, 0, hours+1)
	for at := start; at.Before(end); at = at.Add(time.Hour) {
		out = append(out, deliveryHistory{
			Time:         formatRailsJSONTime(at),
			SuccessCount: 0,
			FailureCount: 0,
		})
	}
	return out
}

func deliveryStatsRedisKeys(prefix string, host string, histories []deliveryHistory) []string {
	keys := make([]string, 0, len(histories)*2)
	for _, history := range histories {
		at, err := parseDeliveryHistoryTime(history.Time)
		if err != nil {
			continue
		}
		keys = append(keys, deliveryStatsRedisKey(prefix, host, "success", at))
		keys = append(keys, deliveryStatsRedisKey(prefix, host, "failure", at))
	}
	return keys
}

func deliveryStatsRedisKey(prefix string, host string, result string, at time.Time) string {
	return prefix + "delivery_stats:" + host + ":" + result + ":" + at.UTC().Format("20060102T15")
}

func formatRailsJSONTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func parseDeliveryHistoryTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func applyDeliveryStatsValues(histories []deliveryHistory, values []any) {
	for i := range histories {
		valueIndex := i * 2
		if valueIndex >= len(values) {
			return
		}
		histories[i].SuccessCount = redisInt(values[valueIndex])
		if valueIndex+1 < len(values) {
			histories[i].FailureCount = redisInt(values[valueIndex+1])
		}
	}
}

func redisInt(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		return 0
	}
}

func normalizeDeliveryStatsHost(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return ""
		}
		value = parsed.Hostname()
	}
	value = strings.TrimPrefix(value, "@")
	if index := strings.IndexAny(value, "/?#"); index >= 0 {
		value = value[:index]
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	value = strings.Trim(value, ".")
	if value == "" {
		return ""
	}
	return punycodeHost(value)
}

func punycodeHost(host string) string {
	labels := strings.Split(host, ".")
	for i, label := range labels {
		if label == "" {
			return ""
		}
		ascii, ok := punycodeLabel(label)
		if !ok {
			return ""
		}
		labels[i] = ascii
	}
	return strings.Join(labels, ".")
}

func punycodeLabel(label string) (string, bool) {
	const initialN = 128
	const initialBias = 72
	runes := []rune(label)
	basic := 0
	var out strings.Builder
	for _, r := range runes {
		if r < 0x80 {
			if r <= 0x20 || r >= 0x7f {
				return "", false
			}
			out.WriteRune(r)
			basic++
		}
	}
	if basic == len(runes) {
		return strings.ToLower(label), true
	}
	if basic > 0 {
		out.WriteByte('-')
	}
	n := initialN
	delta := 0
	bias := initialBias
	for handled := basic; handled < len(runes); {
		m := int(^uint(0) >> 1)
		for _, r := range runes {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		delta += (m - n) * (handled + 1)
		n = m
		for _, r := range runes {
			c := int(r)
			if c < n {
				delta++
				continue
			}
			if c != n {
				continue
			}
			q := delta
			for k := 36; ; k += 36 {
				t := punycodeThreshold(k, bias)
				if q < t {
					break
				}
				out.WriteByte(punycodeDigit(t + (q-t)%(36-t)))
				q = (q - t) / (36 - t)
			}
			out.WriteByte(punycodeDigit(q))
			bias = punycodeAdapt(delta, handled+1, handled == basic)
			delta = 0
			handled++
		}
		delta++
		n++
	}
	return "xn--" + strings.ToLower(out.String()), true
}

func punycodeThreshold(k int, bias int) int {
	if k <= bias+1 {
		return 1
	}
	if k >= bias+26 {
		return 26
	}
	return k - bias
}

func punycodeAdapt(delta int, numPoints int, firstTime bool) int {
	if firstTime {
		delta /= 700
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((36-1)*26)/2 {
		delta /= 36 - 1
		k += 36
	}
	return k + ((36-1+1)*delta)/(delta+38)
}

func punycodeDigit(digit int) byte {
	if digit < 26 {
		return byte('a' + digit)
	}
	return byte('0' + digit - 26)
}

func normalizePeerSearch(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "@")
	if index := strings.IndexAny(value, "/?#"); index >= 0 {
		value = value[:index]
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	value = strings.Trim(value, ".")
	return value
}
