package api

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var railsI18nAvailableLocales = config.RailsI18nAvailableLocales()

var railsStandardLocaleNames = map[string]string{
	"af":    "Afrikaans",
	"an":    "Aragonese",
	"ar":    "Arabic",
	"ast":   "Asturian",
	"be":    "Belarusian",
	"bg":    "Bulgarian",
	"bn":    "Bengali",
	"br":    "Breton",
	"bs":    "Bosnian",
	"ca":    "Catalan",
	"ckb":   "Sorani (Kurdish)",
	"co":    "Corsican",
	"cs":    "Czech",
	"cy":    "Welsh",
	"da":    "Danish",
	"de":    "German",
	"el":    "Greek",
	"en":    "English",
	"eo":    "Esperanto",
	"es":    "Spanish",
	"et":    "Estonian",
	"eu":    "Basque",
	"fa":    "Persian",
	"fi":    "Finnish",
	"fo":    "Faroese",
	"fr":    "French",
	"fy":    "Western Frisian",
	"ga":    "Irish",
	"gd":    "Scottish Gaelic",
	"gl":    "Galician",
	"he":    "Hebrew",
	"hi":    "Hindi",
	"hr":    "Croatian",
	"hu":    "Hungarian",
	"hy":    "Armenian",
	"id":    "Indonesian",
	"ig":    "Igbo",
	"io":    "Ido",
	"is":    "Icelandic",
	"it":    "Italian",
	"ja":    "Japanese",
	"ka":    "Georgian",
	"kab":   "Kabyle",
	"kk":    "Kazakh",
	"kn":    "Kannada",
	"ko":    "Korean",
	"ku":    "Kurmanji (Kurdish)",
	"kw":    "Cornish",
	"la":    "Latin",
	"lt":    "Lithuanian",
	"lv":    "Latvian",
	"mk":    "Macedonian",
	"ml":    "Malayalam",
	"mr":    "Marathi",
	"ms":    "Malay",
	"my":    "Burmese",
	"nl":    "Dutch",
	"nn":    "Norwegian Nynorsk",
	"no":    "Norwegian",
	"oc":    "Occitan",
	"pa":    "Panjabi",
	"pl":    "Polish",
	"ro":    "Romanian",
	"ru":    "Russian",
	"sa":    "Sanskrit",
	"sc":    "Sardinian",
	"sco":   "Scots",
	"si":    "Sinhala",
	"sk":    "Slovak",
	"sl":    "Slovenian",
	"sq":    "Albanian",
	"sr":    "Serbian",
	"sv":    "Swedish",
	"szl":   "Silesian",
	"ta":    "Tamil",
	"te":    "Telugu",
	"th":    "Thai",
	"tr":    "Turkish",
	"tt":    "Tatar",
	"ug":    "Uyghur",
	"uk":    "Ukrainian",
	"ur":    "Urdu",
	"vi":    "Vietnamese",
	"zgh":   "Standard Moroccan Tamazight",
	"zh-CN": "Chinese (China)",
	"zh-HK": "Chinese (Hong Kong)",
	"zh-TW": "Chinese (Taiwan)",
}

func (s *Server) adminFollowRecommendationsPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFollowRecommendationsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	accounts, err := s.adminFollowRecommendationAccounts(c, locale)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminFollowRecommendationsHTMLWithConfig(s.cfg, accounts, c.QueryParam("notice"), c.QueryParam("error"), adminFollowRecommendationStatus(c), adminFollowRecommendationLanguage(c, locale), locale))
}

func (s *Server) updateAdminFollowRecommendations(c *echo.Context) error {
	_, handled, err := s.requireAdminFollowRecommendationsWebUser(c)
	if handled || err != nil {
		return err
	}
	ids := parseAdminFollowRecommendationAccountIDs(c)
	action := adminFollowRecommendationAction(c)
	if len(ids) > 0 && action != "" {
		if err := s.applyAdminFollowRecommendationAction(ids, action); err != nil {
			return err
		}
	}
	return c.Redirect(http.StatusFound, "/admin/follow_recommendations?"+adminFollowRecommendationFilterQuery(c))
}

func (s *Server) requireAdminFollowRecommendationsWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageTaxonomies) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.follow_recommendations.title", "Follow recommendations"), "", adminT(locale, "admin.follow_recommendations.not_permitted", "You are not allowed to manage follow recommendations."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminFollowRecommendationAccounts(c *echo.Context, defaultLocale string) ([]models.Account, error) {
	if s.db == nil {
		return []models.Account{}, nil
	}
	query := s.db.Model(&models.Account{}).Preload("AccountStat").Where("accounts.suspended_at IS NULL")
	if adminFollowRecommendationStatus(c) == "suppressed" {
		query = query.Joins("JOIN follow_recommendation_suppressions ON follow_recommendation_suppressions.account_id = accounts.id").
			Order("follow_recommendation_suppressions.id DESC")
	} else {
		redisCtx, cancel := context.WithTimeout(c.Request().Context(), 500*time.Millisecond)
		defer cancel()
		if accounts, ok, err := s.adminFollowRecommendationAccountsFromRedis(redisCtx, adminFollowRecommendationLanguage(c, defaultLocale)); err != nil {
			return nil, err
		} else if ok {
			return accounts, nil
		}
		query = query.Joins("LEFT JOIN follow_recommendation_suppressions ON follow_recommendation_suppressions.account_id = accounts.id").
			Where("follow_recommendation_suppressions.id IS NULL").
			Joins("JOIN global_follow_recommendations ON global_follow_recommendations.account_id = accounts.id").
			Order("global_follow_recommendations.rank DESC")
	}
	var accounts []models.Account
	err := query.Find(&accounts).Error
	if err != nil && adminFollowRecommendationStatus(c) != "suppressed" && strings.Contains(strings.ToLower(err.Error()), "global_follow_recommendations") {
		return s.adminFollowRecommendationFallbackAccounts()
	}
	return accounts, err
}

func (s *Server) adminFollowRecommendationAccountsFromRedis(ctx context.Context, locale string) ([]models.Account, bool, error) {
	value, err := s.redisCommand(ctx, "ZREVRANGE", followRecommendationsRedisKey(s.cfg.RedisNamespace, locale), "0", "-1")
	if err != nil {
		return nil, false, nil
	}
	members, ok := redisStringArray(value)
	if !ok {
		return nil, false, nil
	}
	accountIDs := adminFollowRecommendationIDsFromRedisMembers(members)
	if len(accountIDs) == 0 {
		return []models.Account{}, true, nil
	}
	var accounts []models.Account
	if err := s.db.Model(&models.Account{}).
		Preload("AccountStat").
		Where("accounts.id IN ?", accountIDs).
		Find(&accounts).Error; err != nil {
		return nil, true, err
	}
	return adminFollowRecommendationAccountsInRedisOrder(accounts, accountIDs), true, nil
}

func (s *Server) adminFollowRecommendationFallbackAccounts() ([]models.Account, error) {
	var accounts []models.Account
	err := s.db.Model(&models.Account{}).
		Preload("AccountStat").
		Joins("LEFT JOIN account_stats ON account_stats.account_id = accounts.id").
		Joins("LEFT JOIN follow_recommendation_suppressions ON follow_recommendation_suppressions.account_id = accounts.id").
		Where("accounts.suspended_at IS NULL").
		Where("follow_recommendation_suppressions.id IS NULL").
		Where("accounts.discoverable = TRUE").
		Order("account_stats.followers_count DESC NULLS LAST").
		Order("account_stats.statuses_count DESC NULLS LAST").
		Order("accounts.id DESC").
		Find(&accounts).Error
	return accounts, err
}

func adminFollowRecommendationIDsFromRedisMembers(members []string) []int64 {
	out := make([]int64, 0, len(members))
	seen := map[int64]struct{}{}
	for _, member := range members {
		id, err := strconv.ParseInt(strings.TrimSpace(member), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		out = append(out, id)
		seen[id] = struct{}{}
	}
	return out
}

func adminFollowRecommendationAccountsInRedisOrder(accounts []models.Account, accountIDs []int64) []models.Account {
	byID := make(map[int64]models.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]models.Account, 0, len(accountIDs))
	for _, id := range accountIDs {
		if account, ok := byID[id]; ok {
			out = append(out, account)
		}
	}
	return out
}

func (s *Server) applyAdminFollowRecommendationAction(ids []int64, action string) error {
	if s.db == nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "DATABASE_URL is not set")
	}
	now := time.Now().UTC()
	switch action {
	case "suppress_follow_recommendation":
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			for _, id := range ids {
				suppression := models.FollowRecommendationSuppression{AccountID: id, CreatedAt: now, UpdatedAt: now}
				if err := tx.Where("account_id = ?", id).FirstOrCreate(&suppression).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		redisCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.removeSuppressedFollowRecommendations(redisCtx, ids)
		return nil
	case "unsuppress_follow_recommendation":
		return s.db.Where("account_id IN ?", ids).Delete(&models.FollowRecommendationSuppression{}).Error
	default:
		return nil
	}
}

func (s *Server) removeSuppressedFollowRecommendations(ctx context.Context, accountIDs []int64) {
	if len(accountIDs) == 0 {
		return
	}
	for _, locale := range railsI18nAvailableLocales {
		args := []string{"ZREM", followRecommendationsRedisKey(s.cfg.RedisNamespace, locale)}
		for _, accountID := range accountIDs {
			args = append(args, strconv.FormatInt(accountID, 10))
		}
		_, _ = s.redisCommand(ctx, args...)
	}
}

func followRecommendationsRedisKey(prefix string, locale string) string {
	return prefix + "follow_recommendations:" + locale
}

func parseAdminFollowRecommendationAccountIDs(c *echo.Context) []int64 {
	req := c.Request()
	_ = req.ParseForm()
	values := req.Form["form_account_batch[account_ids][]"]
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, raw := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && id > 0 {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out
}

func adminFollowRecommendationAction(c *echo.Context) string {
	if adminBatchFormParamExists(c, "suppress") {
		return "suppress_follow_recommendation"
	}
	if adminBatchFormParamExists(c, "unsuppress") {
		return "unsuppress_follow_recommendation"
	}
	return ""
}

func adminFollowRecommendationStatus(c *echo.Context) string {
	if status, ok := adminFollowRecommendationFilterParam(c, "status"); ok && status == "suppressed" {
		return "suppressed"
	}
	return ""
}

func adminFollowRecommendationRawStatus(c *echo.Context) string {
	status, _ := adminFollowRecommendationFilterParam(c, "status")
	return status
}

func adminFollowRecommendationLanguage(c *echo.Context, defaultLocale string) string {
	if language, ok := adminFollowRecommendationFilterParam(c, "language"); ok {
		return language
	}
	return defaultLocale
}

func adminFollowRecommendationFilterParam(c *echo.Context, key string) (string, bool) {
	if values, ok := c.QueryParams()[key]; ok && len(values) > 0 {
		return values[len(values)-1], true
	}
	req := c.Request()
	_ = req.ParseForm()
	if values, ok := req.PostForm[key]; ok && len(values) > 0 {
		return values[len(values)-1], true
	}
	return "", false
}

func adminFollowRecommendationRedirectQuery(c *echo.Context, key string, value string) string {
	params := url.Values{}
	if language, ok := adminFollowRecommendationFilterParam(c, "language"); ok {
		params.Set("language", language)
	}
	if status, ok := adminFollowRecommendationFilterParam(c, "status"); ok {
		params.Set("status", status)
	}
	params.Set(key, value)
	return params.Encode()
}

func adminFollowRecommendationFilterQuery(c *echo.Context) string {
	params := url.Values{}
	if language, ok := adminFollowRecommendationFilterParam(c, "language"); ok {
		params.Set("language", language)
	}
	if status, ok := adminFollowRecommendationFilterParam(c, "status"); ok {
		params.Set("status", status)
	}
	return params.Encode()
}

func railsStandardLocaleName(locale string) string {
	if strings.TrimSpace(locale) == "" {
		return ""
	}
	if name, ok := railsStandardLocaleNames[locale]; ok {
		return name
	}
	return locale
}

func adminFollowRecommendationLanguageSelectHTML(language string) string {
	var b strings.Builder
	b.WriteString(`<select name="language">`)
	for _, locale := range railsI18nAvailableLocales {
		b.WriteString(`<option value="` + html.EscapeString(locale) + `"`)
		if locale == language {
			b.WriteString(` selected`)
		}
		b.WriteString(`>` + html.EscapeString(railsStandardLocaleName(locale)) + `</option>`)
	}
	b.WriteString(`</select>`)
	return b.String()
}

func adminFollowRecommendationsHTML(accounts []models.Account, notice string, errorText string, status string, language string, locale ...string) string {
	return adminFollowRecommendationsHTMLWithConfig(config.Config{}, accounts, notice, errorText, status, language, locale...)
}

func adminFollowRecommendationsHTMLWithConfig(cfg config.Config, accounts []models.Account, notice string, errorText string, status string, language string, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<p>` + adminT(loc, "admin.follow_recommendations.description_html", "Review accounts shown as follow recommendations.") + `</p><hr class="spacer">`)
	statusHidden := ""
	if status != "" {
		statusHidden = `<input type="hidden" name="status" value="` + html.EscapeString(status) + `">`
	}
	filters := url.Values{"language": []string{language}}
	body.WriteString(`<form method="get" action="/admin/follow_recommendations" class="simple_form">` + statusHidden + `<div class="filters"><div class="filter-subset filter-subset--with-select"><strong>` + html.EscapeString(adminT(loc, "admin.follow_recommendations.language", "Language")) + `</strong><div class="input select optional">` + adminFollowRecommendationLanguageSelectHTML(language) + `</div></div>`)
	body.WriteString(relationshipFilterSubsetHTML(adminT(loc, "admin.follow_recommendations.status", "Status"), []relationshipFilterLink{
		{Label: adminT(loc, "admin.accounts.moderation.active", "Active"), Href: adminTrendsWebFilterHref("/admin/follow_recommendations", filters, "status", ""), Active: status == ""},
		{Label: adminT(loc, "admin.follow_recommendations.suppressed", "Suppressed"), Href: adminTrendsWebFilterHref("/admin/follow_recommendations", filters, "status", "suppressed"), Active: status == "suppressed"},
	}))
	body.WriteString(`</div></form><form method="post" action="/admin/follow_recommendations" class="edit_form_account_batch"><input type="hidden" name="_method" value="patch"><div class="batch-table"><div class="batch-table__toolbar"><label class="batch-table__toolbar__select batch-checkbox-all"><input type="checkbox" name="batch_checkbox_all"></label><div class="batch-table__toolbar__actions">`)
	if status == "suppressed" {
		body.WriteString(`<input type="hidden" name="status" value="suppressed"><button class="table-action-link" name="unsuppress" value="1" type="submit"><i class="fa fa-plus"></i> ` + html.EscapeString(adminT(loc, "admin.follow_recommendations.unsuppress", "Unsuppress")) + `</button>`)
	} else if status == "" {
		body.WriteString(`<button class="table-action-link" name="suppress" value="1" type="submit" data-confirm="` + html.EscapeString(adminT(loc, "admin.reports.are_you_sure", "Are you sure?")) + `"><i class="fa fa-times"></i> ` + html.EscapeString(adminT(loc, "admin.follow_recommendations.suppress", "Suppress")) + `</button>`)
	}
	body.WriteString(`</div></div><div class="batch-table__body">`)
	if len(accounts) == 0 {
		body.WriteString(adminNothingHereHTML(loc, "nothing-here--under-tabs"))
	} else {
		for _, account := range accounts {
			body.WriteString(adminFollowRecommendationRowHTMLWithConfig(cfg, account, loc))
		}
	}
	body.WriteString(`</div></div></form>`)
	return authPageHTML(adminT(loc, "admin.follow_recommendations.title", "Follow recommendations"), notice, errorText, body.String(), loc)
}

func adminFollowRecommendationRowHTML(account models.Account) string {
	return adminFollowRecommendationRowHTMLWithConfig(config.Config{}, account, "en")
}

func adminFollowRecommendationRowHTMLWithConfig(cfg config.Config, account models.Account, locale string) string {
	lastStatus := "-"
	if account.AccountStat.LastStatusAt.Valid {
		date := account.AccountStat.LastStatusAt.Time.UTC().Format("2006-01-02")
		lastStatus = `<time class="time-ago" datetime="` + html.EscapeString(date) + `" title="` + html.EscapeString(date) + `">` + html.EscapeString(date) + `</time>`
	}
	return `<div class="batch-table__row"><label class="batch-table__row__select batch-table__row__select--aligned batch-checkbox"><input type="checkbox" name="form_account_batch[account_ids][]" value="` + strconv.FormatInt(account.ID, 10) + `"></label><div class="batch-table__row__content batch-table__row__content--unpadded"><table class="accounts-table"><tbody><tr><td>` + relationshipAccountLinkHTML(cfg, account, false) + `</td><td class="accounts-table__count optional">` + strconv.FormatInt(account.AccountStat.StatusesCount, 10) + `<small>` + html.EscapeString(strings.ToLower(adminT(locale, "accounts.posts.other", "posts"))) + `</small></td><td class="accounts-table__count optional">` + strconv.FormatInt(account.AccountStat.FollowersCount, 10) + `<small>` + html.EscapeString(strings.ToLower(adminT(locale, "accounts.followers.other", "followers"))) + `</small></td><td class="accounts-table__count">` + lastStatus + `<small>` + html.EscapeString(adminT(locale, "accounts.last_active", "last active")) + `</small></td></tr></tbody></table></div></div>`
}
