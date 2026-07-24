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
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type adminInstanceRow struct {
	Instance    models.Instance
	DomainBlock *models.DomainBlock
	DomainAllow *models.DomainAllow
	Unavailable *models.UnavailableDomain
	FailureDays int64
}

func (s *Server) adminInstancesPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	rows, err := s.queryAdminInstances(c)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminInstancesHTML(rows, s.cfg.LimitedFederationMode, c.QueryParam("notice"), c.QueryParam("error"), adminInstanceFilters{
		Page:         adminTrendsPageValue(c),
		ByDomain:     c.QueryParam("by_domain"),
		Limited:      c.QueryParam("limited"),
		Availability: c.QueryParam("availability"),
	}, s.webLocale(c, user)))
}

func (s *Server) showAdminInstancePage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	row, err := s.findAdminInstance(c.Param("id"))
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminInstanceHTMLWithOptions(row, s.cfg.LimitedFederationMode, c.QueryParam("notice"), c.QueryParam("error"), adminInstanceHTMLOptions{
		Locale:        s.webLocale(c, user),
		ShowDashboard: s.userCan(user, rolePermissionViewDashboard),
	}))
}

func (s *Server) destroyAdminInstance(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	row, err := s.findAdminInstance(c.Param("id"))
	if err != nil {
		return err
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(row.Instance.Domain)+"?error="+url.QueryEscape(adminInstanceMessage(s.webLocale(c, user), "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	now := time.Now().UTC()
	if err := logAdminAction(s.db.WithContext(c.Request().Context()), user.AccountID, "destroy", instanceAuditLogTarget(row.Instance.Domain), now); err != nil {
		return err
	}
	if err := s.purgeAdminInstanceDomain(c.Request().Context(), row.Instance.Domain, now); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/instances?notice="+url.QueryEscape(adminTVars(s.webLocale(c, user), "admin.instances.destroyed_msg", "Domain purged: %{domain}", map[string]string{"domain": row.Instance.Domain})))
}

func (s *Server) clearAdminInstanceDeliveryErrors(c *echo.Context) error {
	_, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	domain, err := s.ensureAdminInstanceDomain(c.Param("id"))
	if err != nil {
		return err
	}
	if err := s.clearAdminInstanceDeliveryFailures(c.Request().Context(), domain); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(domain))
}

func (s *Server) restartAdminInstanceDelivery(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	domain, err := s.ensureAdminInstanceDomain(c.Param("id"))
	if err != nil {
		return err
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(domain)+"?error="+url.QueryEscape(adminInstanceMessage(s.webLocale(c, user), "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	now := time.Now().UTC()
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var unavailable models.UnavailableDomain
		if err := tx.Where("domain = ?", domain).First(&unavailable).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Delete(&models.UnavailableDomain{}, unavailable.ID).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "destroy", unavailableDomainAuditLogTarget(unavailable), now)
	}); err != nil {
		return err
	}
	s.invalidateUnavailableDomainsCache(c.Request().Context())
	return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(domain))
}

func (s *Server) stopAdminInstanceDelivery(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	domain, err := s.ensureAdminInstanceDomain(c.Param("id"))
	if err != nil {
		return err
	}
	if s.db == nil {
		return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(domain)+"?error="+url.QueryEscape(adminInstanceMessage(s.webLocale(c, user), "errors.database_unavailable", "DATABASE_URL is not set")))
	}
	now := time.Now().UTC()
	unavailable := models.UnavailableDomain{Domain: domain, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing models.UnavailableDomain
		if err := tx.Where("domain = ?", domain).First(&existing).Error; err == nil {
			return tx.Model(&models.UnavailableDomain{}).Where("id = ?", existing.ID).Update("updated_at", now).Error
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "domain"}},
			DoUpdates: clause.Assignments(map[string]any{"updated_at": now}),
		}).Create(&unavailable).Error; err != nil {
			return err
		}
		return logAdminAction(tx, user.AccountID, "create", unavailableDomainAuditLogTarget(unavailable), now)
	}); err != nil {
		return err
	}
	s.invalidateUnavailableDomainsCache(c.Request().Context())
	return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(domain))
}

func (s *Server) adminInstanceMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminInstance(c)
	}
	return c.Redirect(http.StatusFound, "/admin/instances/"+url.PathEscape(normalizeDomain(c.Param("id"))))
}

func (s *Server) ensureAdminInstanceDomain(rawDomain string) (string, error) {
	row, err := s.findAdminInstance(rawDomain)
	if err != nil {
		return "", err
	}
	return row.Instance.Domain, nil
}

func (s *Server) clearAdminInstanceDeliveryFailures(ctx context.Context, domain string) error {
	key := exhaustedDeliveriesRedisKey(s.cfg.RedisNamespace, domain)
	if key == "" {
		return nil
	}
	_, err := s.redisCommand(ctx, "DEL", key)
	return err
}

func exhaustedDeliveriesRedisKey(prefix string, host string) string {
	host = normalizeDeliveryStatsHost(host)
	if host == "" {
		return ""
	}
	return prefix + "exhausted_deliveries:" + host
}

func (s *Server) purgeAdminInstanceDomain(ctx context.Context, domain string, now time.Time) error {
	if s != nil && s.enqueueAdminDomainPurgeTask(domain) {
		return nil
	}
	return errors.New("admin domain purge enqueue failed")
}

func (s *Server) runPurgeAdminInstanceDomain(ctx context.Context, domain string, now time.Time) error {
	domain = normalizeDomain(domain)
	if domain == "" || s.db == nil {
		return echo.NewHTTPError(http.StatusNotFound, "instance not found")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.purgeAdminInstanceDomainFiles(tx, domain); err != nil {
			return err
		}
		if err := tx.Exec(`
DELETE FROM media_attachments
WHERE account_id IN (SELECT id FROM accounts WHERE domain = ?)
   OR status_id IN (SELECT id FROM statuses WHERE account_id IN (SELECT id FROM accounts WHERE domain = ?))
   OR scheduled_status_id IN (SELECT id FROM scheduled_statuses WHERE account_id IN (SELECT id FROM accounts WHERE domain = ?))
`, domain, domain, domain).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
DELETE FROM account_moderation_notes
WHERE account_id IN (SELECT id FROM accounts WHERE domain = ?)
   OR target_account_id IN (SELECT id FROM accounts WHERE domain = ?)
`, domain, domain).Error; err != nil {
			return err
		}
		if err := tx.Where("domain = ?", domain).Delete(&models.CustomEmoji{}).Error; err != nil {
			return err
		}
		if err := tx.Where("domain = ?", domain).Delete(&models.Account{}).Error; err != nil {
			return err
		}
		if err := recalculateRelationshipCounters(tx, now); err != nil {
			return err
		}
		if err := recalculateStatusCounters(tx, now); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.refreshInstancesMaterializedView(); err != nil {
		return err
	}
	_ = s.meiliDeleteDocumentByID(ctx, "instances", domain)
	return nil
}

func (s *Server) purgeAdminInstanceDomainFiles(database *gorm.DB, domain string) error {
	if s == nil || database == nil || domain == "" {
		return nil
	}
	var accounts []models.Account
	if err := database.
		Where("domain = ?", domain).
		Where("avatar_file_name IS NOT NULL OR header_file_name IS NOT NULL").
		Find(&accounts).Error; err != nil {
		return err
	}
	for _, account := range accounts {
		s.removeAccountImageObjects(account)
		s.removeAccountLocalImageFiles(account.ID)
	}
	var attachments []models.MediaAttachment
	if err := database.
		Where(`account_id IN (SELECT id FROM accounts WHERE domain = ?)
   OR status_id IN (SELECT id FROM statuses WHERE account_id IN (SELECT id FROM accounts WHERE domain = ?))
   OR scheduled_status_id IN (SELECT id FROM scheduled_statuses WHERE account_id IN (SELECT id FROM accounts WHERE domain = ?))`, domain, domain, domain).
		Where("file_file_name IS NOT NULL OR thumbnail_file_name IS NOT NULL").
		Find(&attachments).Error; err != nil {
		return err
	}
	for _, attachment := range attachments {
		s.removeMediaAttachmentLocalFiles(attachment)
	}
	var emojis []models.CustomEmoji
	if err := database.
		Where("domain = ?", domain).
		Where("image_file_name IS NOT NULL").
		Find(&emojis).Error; err != nil {
		return err
	}
	for _, emoji := range emojis {
		s.removeCustomEmojiLocalFiles(emoji)
	}
	return nil
}

func recalculateRelationshipCounters(tx *gorm.DB, now time.Time) error {
	return tx.Exec(`
UPDATE account_stats
SET following_count = (SELECT COUNT(*) FROM follows WHERE follows.account_id = account_stats.account_id),
    followers_count = (SELECT COUNT(*) FROM follows WHERE follows.target_account_id = account_stats.account_id),
    updated_at = ?
`, now).Error
}

func recalculateRelationshipCountersForAccountIDs(tx *gorm.DB, accountIDs []int64, now time.Time) error {
	for _, batch := range int64Batches(uniqueInt64s(accountIDs), 1_000) {
		if err := tx.Exec(`
UPDATE account_stats
SET following_count = (SELECT COUNT(*) FROM follows WHERE follows.account_id = account_stats.account_id),
    followers_count = (SELECT COUNT(*) FROM follows WHERE follows.target_account_id = account_stats.account_id),
    updated_at = ?
WHERE account_id IN ?
`, now, batch).Error; err != nil {
			return err
		}
	}
	return nil
}

func recalculateStatusCounters(tx *gorm.DB, now time.Time) error {
	return tx.Exec(`
UPDATE status_stats
SET replies_count = (SELECT COUNT(*) FROM statuses WHERE statuses.in_reply_to_id = status_stats.status_id AND statuses.deleted_at IS NULL),
    reblogs_count = (SELECT COUNT(*) FROM statuses WHERE statuses.reblog_of_id = status_stats.status_id AND statuses.deleted_at IS NULL),
    favourites_count = (SELECT COUNT(*) FROM favourites WHERE favourites.status_id = status_stats.status_id),
    updated_at = ?
`, now).Error
}

func recalculateStatusCountersForStatusIDs(tx *gorm.DB, statusIDs []int64, now time.Time) error {
	for _, batch := range int64Batches(uniqueInt64s(statusIDs), 1_000) {
		if err := tx.Exec(`
UPDATE status_stats
SET replies_count = (SELECT COUNT(*) FROM statuses WHERE statuses.in_reply_to_id = status_stats.status_id AND statuses.deleted_at IS NULL),
    reblogs_count = (SELECT COUNT(*) FROM statuses WHERE statuses.reblog_of_id = status_stats.status_id AND statuses.deleted_at IS NULL),
    favourites_count = (SELECT COUNT(*) FROM favourites WHERE favourites.status_id = status_stats.status_id),
    updated_at = ?
WHERE status_id IN ?
`, now, batch).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) refreshInstancesMaterializedView() error {
	if s.db == nil || s.db.Dialector.Name() != "postgres" {
		return nil
	}
	return s.db.Exec("REFRESH MATERIALIZED VIEW CONCURRENTLY instances").Error
}

func (s *Server) findAdminInstance(rawDomain string) (adminInstanceRow, error) {
	domain := normalizeDomain(rawDomain)
	if domain == "" || s.db == nil {
		return adminInstanceRow{}, echo.NewHTTPError(http.StatusNotFound, "instance not found")
	}
	var instance models.Instance
	if err := s.db.Where("domain = ?", domain).First(&instance).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return adminInstanceRow{}, echo.NewHTTPError(http.StatusNotFound, "instance not found")
		}
		return adminInstanceRow{}, err
	}
	rows, err := s.decorateAdminInstances([]models.Instance{instance})
	if err != nil {
		return adminInstanceRow{}, err
	}
	return rows[0], nil
}

func (s *Server) queryAdminInstances(c *echo.Context) ([]adminInstanceRow, error) {
	if s.db == nil {
		return nil, nil
	}
	query := s.db.Model(&models.Instance{})
	if s.cfg.LimitedFederationMode {
		query = query.Joins("JOIN domain_allows ON lower(domain_allows.domain) = lower(instances.domain)").Order("domain_allows.id DESC")
	} else if strings.TrimSpace(c.QueryParam("limited")) != "" {
		query = query.Joins("JOIN domain_blocks ON lower(domain_blocks.domain) = lower(instances.domain)").Order("domain_blocks.id DESC")
	} else {
		query = query.Order("instances.accounts_count DESC")
	}
	if byDomain := strings.TrimSpace(c.QueryParam("by_domain")); byDomain != "" && !s.cfg.LimitedFederationMode {
		query = query.Where("lower(instances.domain) LIKE ?", "%"+strings.ToLower(byDomain)+"%")
	}
	switch strings.TrimSpace(c.QueryParam("availability")) {
	case "":
	case "failing":
		redisCtx, cancel := context.WithTimeout(c.Request().Context(), 500*time.Millisecond)
		defer cancel()
		domains, err := s.adminInstanceWarningDomains(redisCtx)
		if err != nil {
			return nil, err
		}
		if len(domains) == 0 {
			query = query.Where("1 = 0")
		} else {
			query = query.Where("instances.domain IN ?", domains)
		}
	case "unavailable":
		query = query.Joins("JOIN unavailable_domains ON lower(unavailable_domains.domain) = lower(instances.domain)")
	default:
		return nil, echo.NewHTTPError(http.StatusBadRequest, "unknown availability filter")
	}
	var instances []models.Instance
	if err := query.Offset(adminRailsPageOffset(c)).Limit(adminRailsDefaultPageSize).Find(&instances).Error; err != nil {
		return nil, err
	}
	return s.decorateAdminInstances(instances)
}

func (s *Server) decorateAdminInstances(instances []models.Instance) ([]adminInstanceRow, error) {
	rows := make([]adminInstanceRow, 0, len(instances))
	if len(instances) == 0 || s.db == nil {
		return rows, nil
	}
	domains := make([]string, 0, len(instances))
	for _, instance := range instances {
		domains = append(domains, strings.ToLower(instance.Domain))
	}
	blockMap := map[string]models.DomainBlock{}
	var blocks []models.DomainBlock
	if err := s.db.Where("lower(domain) IN ?", domains).Find(&blocks).Error; err != nil {
		return nil, err
	}
	for _, block := range blocks {
		blockMap[strings.ToLower(block.Domain)] = block
	}
	allowMap := map[string]models.DomainAllow{}
	var allows []models.DomainAllow
	if err := s.db.Where("lower(domain) IN ?", domains).Find(&allows).Error; err != nil {
		return nil, err
	}
	for _, allow := range allows {
		allowMap[strings.ToLower(allow.Domain)] = allow
	}
	unavailableMap := map[string]models.UnavailableDomain{}
	var unavailableDomains []models.UnavailableDomain
	if err := s.db.Where("lower(domain) IN ?", domains).Find(&unavailableDomains).Error; err != nil {
		return nil, err
	}
	for _, unavailable := range unavailableDomains {
		unavailableMap[strings.ToLower(unavailable.Domain)] = unavailable
	}
	warningDays := map[string]int64{}
	redisCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	if days, err := s.adminInstanceWarningDaysMap(redisCtx, domains); err == nil {
		warningDays = days
	}
	cancel()
	for _, instance := range instances {
		row := adminInstanceRow{Instance: instance}
		domainKey := strings.ToLower(instance.Domain)
		if block, ok := blockMap[domainKey]; ok {
			row.DomainBlock = &block
		}
		if allow, ok := allowMap[domainKey]; ok {
			row.DomainAllow = &allow
		}
		if unavailable, ok := unavailableMap[domainKey]; ok {
			row.Unavailable = &unavailable
		} else if days := warningDays[domainKey]; days > 0 {
			row.FailureDays = days
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Server) adminInstanceWarningDomains(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	prefix := redisConfig(s.cfg).prefix + "exhausted_deliveries:"
	value, err := s.redisCommand(ctx, "KEYS", prefix+"*")
	if err != nil {
		return nil, err
	}
	keys, ok := redisStringArray(value)
	if !ok {
		return nil, nil
	}
	domainSet := make(map[string]struct{}, len(keys))
	domains := make([]string, 0, len(keys))
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		domain := normalizeDeliveryStatsHost(strings.TrimPrefix(key, prefix))
		if domain == "" {
			continue
		}
		if _, exists := domainSet[domain]; exists {
			continue
		}
		domainSet[domain] = struct{}{}
		domains = append(domains, domain)
	}
	if len(domains) == 0 {
		return nil, nil
	}
	var unavailable []string
	if err := s.db.WithContext(ctx).Model(&models.UnavailableDomain{}).Where("domain IN ?", domains).Pluck("domain", &unavailable).Error; err != nil {
		return nil, err
	}
	for _, domain := range unavailable {
		delete(domainSet, normalizeDeliveryStatsHost(domain))
	}
	filtered := domains[:0]
	for _, domain := range domains {
		if _, ok := domainSet[domain]; ok {
			filtered = append(filtered, domain)
		}
	}
	return filtered, nil
}

func (s *Server) adminInstanceWarningDaysMap(ctx context.Context, domains []string) (map[string]int64, error) {
	out := make(map[string]int64, len(domains))
	if s == nil || len(domains) == 0 {
		return out, nil
	}
	prefix := redisConfig(s.cfg).prefix
	for _, domain := range domains {
		domain = normalizeDeliveryStatsHost(domain)
		if domain == "" {
			continue
		}
		value, err := s.redisCommand(ctx, "SCARD", exhaustedDeliveriesRedisKey(prefix, domain))
		if err != nil {
			return out, err
		}
		if days := redisInt(value); days > 0 {
			out[strings.ToLower(domain)] = days
		}
	}
	return out, nil
}

func escapeLikePattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

type adminInstanceFilters struct {
	Page         string
	ByDomain     string
	Limited      string
	Availability string
}

func adminInstanceFilterHiddenFields(filters adminInstanceFilters) string {
	values := map[string]string{
		"page":         firstNonEmpty(filters.Page, "1"),
		"limited":      filters.Limited,
		"availability": filters.Availability,
	}
	var body strings.Builder
	for _, key := range []string{"page", "limited", "availability"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			body.WriteString(`<input type="hidden" name="` + key + `" value="` + html.EscapeString(value) + `">`)
		}
	}
	return body.String()
}

func adminInstancesHTML(rows []adminInstanceRow, limitedFederation bool, notice string, errorText string, filters adminInstanceFilters, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions">`)
	if limitedFederation {
		body.WriteString(`<a class="button" id="add-instance-button" href="/admin/domain_allows/new">` + html.EscapeString(adminT(loc, "admin.domain_allows.add_new", "Add domain allow")) + `</a> `)
		body.WriteString(`<a class="button" href="/admin/export_domain_allows/export.csv">` + html.EscapeString(settingsT(loc, "exports.csv", "Export")) + `</a> <a class="button" href="/admin/export_domain_allows/new">` + html.EscapeString(settingsT(loc, "settings.import", "Import")) + `</a>`)
	} else {
		body.WriteString(`<a class="button" id="add-instance-button" href="/admin/domain_blocks/new">` + html.EscapeString(adminT(loc, "admin.domain_blocks.add_new", "Add domain block")) + `</a> `)
		body.WriteString(`<a class="button" href="/admin/export_domain_blocks/export.csv">` + html.EscapeString(settingsT(loc, "exports.csv", "Export")) + `</a> <a class="button" href="/admin/export_domain_blocks/new">` + html.EscapeString(settingsT(loc, "settings.import", "Import")) + `</a>`)
	}
	body.WriteString(`</div><div class="filters">`)
	moderationLinks := []relationshipFilterLink{{Label: adminT(loc, "admin.instances.moderation.all", "All"), Href: adminInstanceFilterHref(filters, "limited", ""), Active: filters.Limited == ""}}
	if !limitedFederation {
		moderationLinks = append(moderationLinks, relationshipFilterLink{Label: adminT(loc, "admin.instances.moderation.limited", "Limited"), Href: adminInstanceFilterHref(filters, "limited", "1"), Active: filters.Limited == "1"})
	}
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.instances.moderation.title", "Moderation"), moderationLinks))
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.instances.availability.title", "Availability"), []relationshipFilterLink{
		{Label: adminT(loc, "admin.instances.delivery.all", "All"), Href: adminInstanceFilterHref(filters, "availability", ""), Active: filters.Availability == ""},
		{Label: adminT(loc, "admin.instances.delivery.failing", "Failing"), Href: adminInstanceFilterHref(filters, "availability", "failing"), Active: filters.Availability == "failing"},
		{Label: adminT(loc, "admin.instances.delivery.unavailable", "Unavailable"), Href: adminInstanceFilterHref(filters, "availability", "unavailable"), Active: filters.Availability == "unavailable"},
	}))
	body.WriteString(`</div>`)
	if !limitedFederation {
		body.WriteString(`<form method="get" action="/admin/instances" class="simple_form">` + adminInstanceFilterHiddenFields(filters) + `<div class="fields-group"><div class="input string optional"><input class="string optional" type="text" name="by_domain" id="by_domain" value="` + html.EscapeString(filters.ByDomain) + `" placeholder="` + html.EscapeString(adminT(loc, "admin.instances.by_domain", "Domain")) + `"></div><div class="actions"><button class="button" type="submit">` + html.EscapeString(adminT(loc, "admin.accounts.search", "Search")) + `</button> <a class="button negative" href="/admin/instances">` + html.EscapeString(adminT(loc, "admin.accounts.reset", "Reset")) + `</a></div></div></form>`)
	}
	body.WriteString(`<hr class="spacer">`)
	if len(rows) == 0 {
		body.WriteString(`<div class="muted-hint center-text">` + html.EscapeString(adminT(loc, "admin.instances.empty", "No instances found.")) + `</div>`)
	} else {
		body.WriteString(`<div class="directory">`)
		for _, row := range rows {
			body.WriteString(adminInstanceListItemHTML(row, loc))
		}
		body.WriteString(`</div>`)
	}
	body.WriteString(adminRailsPaginationHTML(loc, "/admin/instances", filters.Page, adminInstanceFiltersQuery(filters), len(rows)))
	return authPageHTML(adminT(loc, "admin.instances.title", "Instances"), notice, errorText, body.String(), loc)
}

func adminInstanceFilterHref(filters adminInstanceFilters, key string, value string) string {
	values := adminInstanceFiltersQuery(filters)
	values.Del("page")
	if value == "" {
		values.Del(key)
	} else {
		values.Set(key, value)
	}
	if query := values.Encode(); query != "" {
		return "/admin/instances?" + query
	}
	return "/admin/instances"
}

func adminInstanceFiltersQuery(filters adminInstanceFilters) url.Values {
	values := url.Values{}
	for key, value := range map[string]string{"by_domain": filters.ByDomain, "limited": filters.Limited, "availability": filters.Availability} {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return values
}

func adminInstanceListItemHTML(row adminInstanceRow, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	domain := row.Instance.Domain
	label := adminT(loc, "admin.accounts.no_limits_imposed", "No limits imposed")
	if row.DomainBlock != nil {
		label = adminDomainBlockPoliciesLabel(*row.DomainBlock)
	} else if row.DomainAllow != nil {
		label = adminT(loc, "admin.accounts.whitelisted", "Allowed")
	}
	warning := ""
	if row.Unavailable != nil {
		warning = `<i class="fa fa-warning fa-fw" title="` + html.EscapeString(adminT(loc, "admin.instances.availability.warning", "Delivery warning")) + `"></i> `
	} else if row.FailureDays > 0 {
		warning = `<i class="fa fa-warning fa-fw" title="` + html.EscapeString(adminT(loc, "admin.instances.availability.warning", "Delivery warning")) + `"></i> `
	}
	return `<div class="directory__tag"><a href="/admin/instances/` + url.PathEscape(domain) + `"><h4>` + warning + html.EscapeString(domain) + ` <small>` + html.EscapeString(label) + `</small></h4><div class="trends__item__current" title="` + html.EscapeString(adminTVars(loc, "admin.instances.known_accounts.other", "%{count} known accounts", map[string]string{"count": strconv.FormatInt(row.Instance.AccountsCount, 10)})) + `">` + html.EscapeString(adminInstanceCountString(row.Instance.AccountsCount)) + `</div></a></div>`
}

func adminInstanceHTML(row adminInstanceRow, limitedFederation bool, notice string, errorText string, locale ...string) string {
	return adminInstanceHTMLWithOptions(row, limitedFederation, notice, errorText, adminInstanceHTMLOptions{Locale: settingsLocaleArgOrEnglish(locale...)})
}

type adminInstanceHTMLOptions struct {
	Locale        string
	ShowDashboard bool
}

func adminInstanceHTMLWithOptions(row adminInstanceRow, limitedFederation bool, notice string, errorText string, options adminInstanceHTMLOptions) string {
	loc := settingsLocaleArgOrEnglish(options.Locale)
	domain := row.Instance.Domain
	var body strings.Builder
	if options.ShowDashboard {
		body.WriteString(adminInstanceDashboardHTML(domain, loc))
	}
	body.WriteString(`<hr class="spacer"><h3>` + html.EscapeString(adminT(loc, "admin.instances.content_policies.title", "Content policies")) + `</h3>`)
	confirm := html.EscapeString(adminT(loc, "admin.accounts.are_you_sure", "Are you sure?"))
	if limitedFederation {
		body.WriteString(`<p>` + adminT(loc, "admin.instances.content_policies.limited_federation_mode_description_html", "Only explicitly allowed domains can federate with this server.") + `</p>`)
		if row.DomainAllow != nil {
			body.WriteString(`<a class="button button--destructive" href="/admin/domain_allows/` + strconv.FormatInt(row.DomainAllow.ID, 10) + `" data-method="delete" data-confirm="` + confirm + `">` + html.EscapeString(adminT(loc, "admin.domain_allows.undo", "Undo domain allow")) + `</a>`)
		} else {
			body.WriteString(`<a class="button" href="/admin/domain_allows?domain_allow%5Bdomain%5D=` + url.QueryEscape(domain) + `" data-method="post">` + html.EscapeString(adminT(loc, "admin.domain_allows.add_new", "Add domain allow")) + `</a>`)
		}
	} else {
		body.WriteString(`<p>` + adminT(loc, "admin.instances.content_policies.description_html", "Control how this server communicates with the selected domain.") + `</p>`)
		if row.DomainBlock != nil {
			body.WriteString(`<div class="table-wrapper"><table class="table horizontal-table"><tbody><tr><th>` + html.EscapeString(adminT(loc, "admin.instances.content_policies.comment", "Comment")) + `</th><td>` + html.EscapeString(row.DomainBlock.PrivateComment.String) + `</td></tr><tr><th>` + html.EscapeString(adminT(loc, "admin.instances.content_policies.reason", "Reason")) + `</th><td>` + html.EscapeString(row.DomainBlock.PublicComment.String) + `</td></tr><tr><th>` + html.EscapeString(adminT(loc, "admin.instances.content_policies.policy", "Policy")) + `</th><td>` + html.EscapeString(adminDomainBlockPoliciesLabel(*row.DomainBlock)) + `</td></tr></tbody></table></div>`)
			body.WriteString(`<a class="button" href="/admin/domain_blocks/` + strconv.FormatInt(row.DomainBlock.ID, 10) + `/edit">` + html.EscapeString(adminT(loc, "admin.domain_blocks.edit", "Edit domain block")) + `</a> `)
			body.WriteString(`<a class="button" href="/admin/domain_blocks/` + strconv.FormatInt(row.DomainBlock.ID, 10) + `" data-method="delete" data-confirm="` + confirm + `">` + html.EscapeString(adminT(loc, "admin.domain_blocks.undo", "Undo domain block")) + `</a>`)
		} else {
			body.WriteString(`<a class="button" href="/admin/domain_blocks/new?_domain=` + url.QueryEscape(domain) + `">` + html.EscapeString(adminT(loc, "admin.domain_blocks.add_new", "Add domain block")) + `</a>`)
		}
	}
	body.WriteString(`<hr class="spacer"><h3>` + html.EscapeString(adminT(loc, "admin.instances.availability.title", "Availability")) + `</h3><p>` + adminTVars(loc, "admin.instances.availability.description_html", "Delivery is automatically stopped after repeated failures.", map[string]string{"count": "7"}) + `</p>`)
	body.WriteString(adminInstanceAvailabilityIndicatorHTML(row, loc, confirm))
	body.WriteString(`<span><a class="button" href="/instance-stats/` + url.PathEscape(domain) + `">` + html.EscapeString(adminT(loc, "admin.instances.delivery.instance_stats", "Instance stats")) + `</a></span>`)
	if row.Unavailable != nil || (row.DomainBlock != nil && domainBlockSeverityIs(*row.DomainBlock, "suspend")) {
		body.WriteString(`<p>` + adminT(loc, "admin.instances.purge_description_html", "Permanently remove cached data from this domain.") + `</p><a class="button button--destructive" href="/admin/instances/` + url.PathEscape(domain) + `" data-method="delete" data-confirm="` + html.EscapeString(adminT(loc, "admin.instances.confirm_purge", "Are you sure you want to purge this domain?")) + `">` + html.EscapeString(adminT(loc, "admin.instances.purge", "Purge domain")) + `</a>`)
	}
	return authPageHTML(domain, notice, errorText, body.String(), loc)
}

func adminInstanceDashboardHTML(domain string, locale string) string {
	now := time.Now().UTC()
	startAt := now.AddDate(0, 0, -6).Format("2006-01-02")
	endAt := now.AddDate(0, 0, -1).Format("2006-01-02")
	params := map[string]any{"domain": domain}
	var body strings.Builder
	body.WriteString(`<div class="content__heading__actions"><span>` + html.EscapeString(startAt) + ` - ` + html.EscapeString(endAt) + `</span></div><p><i class="fa fa-info fa-fw"></i> ` + adminT(locale, "admin.instances.totals_time_period_hint_html", "Totals cover the displayed time period.") + `</p><div class="dashboard">`)
	for _, item := range []struct {
		measure string
		label   string
		href    string
	}{
		{"instance_accounts", adminT(locale, "admin.instances.dashboard.instance_accounts_measure", "Accounts"), "/admin/accounts?origin=remote&by_domain=" + url.QueryEscape(domain)},
		{"instance_statuses", adminT(locale, "admin.instances.dashboard.instance_statuses_measure", "Posts"), ""},
		{"instance_media_attachments", adminT(locale, "admin.instances.dashboard.instance_media_attachments_measure", "Media attachments"), ""},
		{"instance_follows", adminT(locale, "admin.instances.dashboard.instance_follows_measure", "Follows"), ""},
		{"instance_followers", adminT(locale, "admin.instances.dashboard.instance_followers_measure", "Followers"), ""},
		{"instance_reports", adminT(locale, "admin.instances.dashboard.instance_reports_measure", "Reports"), "/admin/reports?by_target_domain=" + url.QueryEscape(domain)},
	} {
		props := map[string]any{"measure": item.measure, "start_at": startAt, "end_at": endAt, "params": params, "label": item.label}
		if item.href != "" {
			props["href"] = item.href
		}
		body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("counter", props) + `</div>`)
	}
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "instance_accounts", "start_at": startAt, "end_at": endAt, "params": params, "limit": 8, "label": adminT(locale, "admin.instances.dashboard.instance_accounts_dimension", "Most active accounts")}) + `</div>`)
	body.WriteString(`<div class="dashboard__item">` + adminDashboardReactComponent("dimension", map[string]any{"dimension": "instance_languages", "start_at": startAt, "end_at": endAt, "params": params, "limit": 8, "label": adminT(locale, "admin.instances.dashboard.instance_languages_dimension", "Languages")}) + `</div></div>`)
	return body.String()
}

func adminInstanceAvailabilityIndicatorHTML(row adminInstanceRow, locale string, confirm string) string {
	var body strings.Builder
	body.WriteString(`<div class="availability-indicator"><ul class="availability-indicator__graphic">`)
	now := time.Now().UTC()
	negativeDays := row.FailureDays
	if negativeDays > 14 {
		negativeDays = 14
	}
	for day := int64(13); day >= 0; day-- {
		className := "neutral"
		if day < negativeDays {
			className = "negative"
		}
		date := now.AddDate(0, 0, -int(day)).Format("2006-01-02")
		body.WriteString(`<li class="availability-indicator__graphic__item ` + className + `" title="` + date + `"></li>`)
	}
	body.WriteString(`</ul><div class="availability-indicator__hint">`)
	domainPath := "/admin/instances/" + url.PathEscape(row.Instance.Domain)
	if row.Unavailable != nil {
		body.WriteString(`<span class="negative-hint">` + html.EscapeString(adminTVars(locale, "admin.instances.availability.failure_threshold_reached", "Delivery stopped since %{date}.", map[string]string{"date": row.Unavailable.CreatedAt.Format("2006-01-02")})) + ` <a href="` + domainPath + `/restart_delivery" data-method="post" data-confirm="` + confirm + `">` + html.EscapeString(adminT(locale, "admin.instances.delivery.restart", "Restart delivery")) + `</a></span>`)
	} else if row.FailureDays == 0 {
		body.WriteString(`<span class="positive-hint">` + html.EscapeString(adminT(locale, "admin.instances.availability.no_failures_recorded", "No delivery failures recorded.")) + ` <a href="` + domainPath + `/stop_delivery" data-method="post" data-confirm="` + confirm + `">` + html.EscapeString(adminT(locale, "admin.instances.delivery.stop", "Stop delivery")) + `</a></span>`)
	} else {
		body.WriteString(`<span class="negative-hint">` + html.EscapeString(adminTVars(locale, "admin.instances.availability.failures_recorded.other", "%{count} delivery failures recorded.", map[string]string{"count": strconv.FormatInt(row.FailureDays, 10)})) + ` <span><a href="` + domainPath + `/clear_delivery_errors" data-method="post" data-confirm="` + confirm + `">` + html.EscapeString(adminT(locale, "admin.instances.delivery.clear", "Clear errors")) + `</a></span> <span><a href="` + domainPath + `/stop_delivery" data-method="post" data-confirm="` + confirm + `">` + html.EscapeString(adminT(locale, "admin.instances.delivery.stop", "Stop delivery")) + `</a></span></span>`)
	}
	body.WriteString(`</div></div>`)
	return body.String()
}

func adminDomainBlockPoliciesLabel(block models.DomainBlock) string {
	labels := []string{}
	if severity := adminDomainBlockSeverityLabel(block.Severity); severity != "" {
		labels = append(labels, severity)
	}
	if block.RejectMedia {
		labels = append(labels, "reject media")
	}
	if block.RejectReports {
		labels = append(labels, "reject reports")
	}
	if block.Obfuscate {
		labels = append(labels, "obfuscate")
	}
	return strings.Join(labels, " / ")
}

func adminInstanceCountString(value int64) string {
	text := strconv.FormatInt(value, 10)
	if len(text) <= 3 {
		return text
	}
	var out strings.Builder
	prefix := len(text) % 3
	if prefix == 0 {
		prefix = 3
	}
	out.WriteString(text[:prefix])
	for i := prefix; i < len(text); i += 3 {
		out.WriteByte(',')
		out.WriteString(text[i : i+3])
	}
	return out.String()
}

func adminInstanceMessage(locale, key, fallback string) string {
	return adminT(locale, "admin.instances."+key, fallback)
}
