package api

import (
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
)

const appealMaxStrikeAge = 20 * 24 * time.Hour

func (s *Server) disputeStrikesPage(c *echo.Context) error {
	if err := requireHTMLOnlyOptionalFormat(c); err != nil {
		return err
	}
	account, _, user, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	var strikes []models.AccountWarning
	if err := s.db.Preload("Account").Where("target_account_id = ?", account.ID).Order("id DESC").Find(&strikes).Error; err != nil {
		return err
	}
	appeals, err := s.disputeAppealsForWarnings(account.ID, strikes)
	if err != nil {
		return err
	}
	navigation, err := s.settingsNavigationForUser(c.Request().URL.Path, locale, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, disputeStrikesHTML(strikes, appeals, c.QueryParam("notice"), c.QueryParam("error"), locale, theme, localDomainFromBaseURL(s.cfg.BaseURL()), navigation))
}

func (s *Server) disputeStrikePage(c *echo.Context) error {
	if err := requireHTMLOnlyOptionalFormat(c); err != nil {
		return err
	}
	account, _, user, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	allowAdmin := s.userCan(user, rolePermissionManageAppeals)
	strike, appeal, err := s.disputeStrikeForAccount(account.ID, c.Param("id"), true)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	if !allowAdmin && (!strike.TargetAccountID.Valid || strike.TargetAccountID.Int64 != account.ID) {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	navigation, err := s.settingsNavigationForUser(c.Request().URL.Path, locale, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, disputeStrikeHTML(*strike, appeal, c.QueryParam("notice"), c.QueryParam("error"), locale, theme, navigation))
}

func (s *Server) createDisputeAppeal(c *echo.Context) error {
	account, _, user, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	locale := s.webLocale(c, user)
	strike, existing, err := s.disputeStrikeForAccount(account.ID, c.Param("strike_id"), false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		return err
	}
	text, err := disputeAppealText(c)
	if errors.Is(err, errDisputeAppealParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	if existing != nil {
		return s.renderDisputeAppealValidationError(c, *strike, existing, settingsT(locale, "disputes.strikes.your_appeal_pending", "Appeal has already been submitted"), locale, user)
	}
	if !appealStrikeWithinTimeFrame(strike.CreatedAt, time.Now().UTC()) {
		return s.renderDisputeAppealValidationError(c, *strike, nil, settingsT(locale, "strikes.errors.too_late", "Strike is too old to appeal"), locale, user)
	}
	if strings.TrimSpace(text) == "" || len([]rune(text)) > 2000 {
		return s.renderDisputeAppealValidationError(c, *strike, &models.Appeal{AccountID: account.ID, AccountWarningID: strike.ID, Text: text, Account: *account, Strike: *strike}, settingsT(locale, "disputes.strikes.appeals.invalid_text", "Appeal text is invalid"), locale, user)
	}
	now := time.Now().UTC()
	appeal := models.Appeal{AccountID: account.ID, AccountWarningID: strike.ID, Text: text, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&appeal).Error; err != nil {
		return err
	}
	appeal.Account = *account
	appeal.Strike = *strike
	_ = s.sendStaffNewAppealMails(appeal)
	return c.Redirect(http.StatusFound, "/disputes/strikes/"+strconv.FormatInt(strike.ID, 10)+"?notice="+url.QueryEscape(settingsT(locale, "disputes.strikes.appealed_msg", "Appeal submitted")))
}

var errDisputeAppealParamsMissing = errors.New("dispute appeal root parameter is missing")

func disputeAppealText(c *echo.Context) (string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return "", err
	}
	const prefix = "appeal"
	if !formHasNestedPrefix(req.Form, prefix) {
		return "", errDisputeAppealParamsMissing
	}
	return lastFormValue(req.Form, prefix+"[text]"), nil
}

func (s *Server) renderDisputeAppealValidationError(c *echo.Context, strike models.AccountWarning, appeal *models.Appeal, message string, locale string, user *models.User) error {
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	navigation, err := s.settingsNavigationForUser(c.Request().URL.Path, locale, user, nil)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, disputeStrikeHTML(strike, appeal, "", message, locale, theme, navigation))
}

func (s *Server) disputeStrikeForAccount(accountID int64, id string, allowAdmin bool) (*models.AccountWarning, *models.Appeal, error) {
	var strike models.AccountWarning
	query := s.db.Preload("Account").Preload("TargetAccount").Where("id = ?", id)
	if !allowAdmin {
		query = query.Where("target_account_id = ?", accountID)
	}
	if err := query.First(&strike).Error; err != nil {
		return nil, nil, err
	}
	var appeal models.Appeal
	appealQuery := s.db.Where("account_warning_id = ?", strike.ID)
	if !allowAdmin {
		appealQuery = appealQuery.Where("account_id = ?", accountID)
	}
	err := appealQuery.First(&appeal).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &strike, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &strike, &appeal, nil
}

func (s *Server) disputeAppealsForWarnings(accountID int64, strikes []models.AccountWarning) (map[int64]models.Appeal, error) {
	ids := make([]int64, 0, len(strikes))
	for _, strike := range strikes {
		ids = append(ids, strike.ID)
	}
	if len(ids) == 0 || s.db == nil {
		return map[int64]models.Appeal{}, nil
	}
	var appeals []models.Appeal
	if err := s.db.Preload("Account").Where("account_id = ? AND account_warning_id IN ?", accountID, ids).Find(&appeals).Error; err != nil {
		return nil, err
	}
	out := make(map[int64]models.Appeal, len(appeals))
	for _, appeal := range appeals {
		out[appeal.AccountWarningID] = appeal
	}
	return out, nil
}

func disputeStrikesHTML(strikes []models.AccountWarning, appeals map[int64]models.Appeal, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	instance := settingsInstanceDomainArg(localeAndTheme...)
	var rows strings.Builder
	for _, strike := range strikes {
		rows.WriteString(`<a class="log-entry" href="/disputes/strikes/`)
		rows.WriteString(strconv.FormatInt(strike.ID, 10))
		rows.WriteString(`"><div class="log-entry__header"><div class="log-entry__avatar"><div class="indicator-icon `)
		if strike.OverruledAt.Valid {
			rows.WriteString(`success`)
		} else {
			rows.WriteString(`failure`)
		}
		rows.WriteString(`"><i class="fa fa-warning fa-fw"></i></div></div><div class="log-entry__content"><div class="log-entry__title">`)
		rows.WriteString(html.EscapeString(disputeStrikeTitle(strike, loc)))
		rows.WriteString(`</div><div class="log-entry__timestamp"><time class="formatted" datetime="`)
		rows.WriteString(html.EscapeString(strike.CreatedAt.UTC().Format(time.RFC3339)))
		rows.WriteString(`">`)
		rows.WriteString(html.EscapeString(strike.CreatedAt.UTC().Format(time.RFC3339)))
		rows.WriteString(`</time>`)
		if strike.OverruledAt.Valid {
			rows.WriteString(` · <span class="positive-hint">` + html.EscapeString(settingsT(loc, "disputes.strikes.your_appeal_approved", "Your appeal has been approved")) + `</span>`)
		} else if appeal, ok := appeals[strike.ID]; ok {
			if appeal.RejectedAt.Valid {
				rows.WriteString(` · <span class="negative-hint">` + html.EscapeString(settingsT(loc, "disputes.strikes.your_appeal_rejected", "Your appeal has been rejected")) + `</span>`)
			} else if !appeal.ApprovedAt.Valid {
				rows.WriteString(` · <span class="warning-hint">` + html.EscapeString(settingsT(loc, "disputes.strikes.your_appeal_pending", "You have submitted an appeal")) + `</span>`)
			}
		}
		rows.WriteString(`</div>`)
		if strings.TrimSpace(strike.Text) != "" {
			rows.WriteString(`<div class="log-entry__content__text">`)
			rows.WriteString(html.EscapeString(trimForTable(strike.Text)))
			rows.WriteString(`</div>`)
		}
		rows.WriteString(`</div></div></a>`)
	}
	title := settingsT(loc, "settings.strikes", "Moderation strikes")
	body := settingsFlashHTML(notice, errorText) + `<p>` + adminTVars(loc, "disputes.strikes.description_html", "These are actions taken against your account and warnings that have been sent to you by the staff of %{instance}.", map[string]string{"instance": html.EscapeString(instance)}) + `</p>
    <div class="account-strikes">` + rows.String() + `</div>`
	navigation := settingsNavForLocale(loc)
	if len(localeAndTheme) > 3 && strings.TrimSpace(localeAndTheme[3]) != "" {
		navigation = localeAndTheme[3]
	}
	return settingsPageShell(title, navigation, body, loc, theme)
}

func settingsInstanceDomainArg(localeThemeAndDomain ...string) string {
	if len(localeThemeAndDomain) > 2 && strings.TrimSpace(localeThemeAndDomain[2]) != "" {
		return strings.TrimSpace(localeThemeAndDomain[2])
	}
	return ""
}

func disputeStrikeTitle(strike models.AccountWarning, locale string) string {
	return adminTVars(locale, "disputes.strikes.title", "%{action} from %{date}", map[string]string{
		"action": accountWarningActionLabel(strike.Action, locale),
		"date":   strike.CreatedAt.UTC().Format("2006-01-02"),
	})
}

func disputeStrikeTextBlock(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var out strings.Builder
	for _, part := range strings.Split(value, "\n\n") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out.WriteString(`<p>`)
		out.WriteString(strings.ReplaceAll(html.EscapeString(part), "\n", "<br>"))
		out.WriteString(`</p>`)
	}
	return out.String()
}

func disputeStrikeStatusListHTML(strike models.AccountWarning, locale string) string {
	if len(strike.StatusIDs) == 0 {
		return ""
	}
	var rows strings.Builder
	rows.WriteString(`<p><strong>` + html.EscapeString(settingsT(locale, "user_mailer.warning.statuses", "Posts cited")) + `</strong></p><div class="strike-card__statuses-list">`)
	for _, statusID := range strike.StatusIDs {
		statusID = strings.TrimSpace(statusID)
		if statusID == "" {
			continue
		}
		rows.WriteString(`<div class="strike-card__statuses-list__item"><div class="one-liner">`)
		rows.WriteString(html.EscapeString(adminTVars(locale, "disputes.strikes.status", "Post #%{id}", map[string]string{"id": statusID})))
		rows.WriteString(`</div><div class="strike-card__statuses-list__item__meta">`)
		rows.WriteString(html.EscapeString(settingsT(locale, "disputes.strikes.status_removed", "Post already removed from system")))
		rows.WriteString(`</div></div>`)
	}
	rows.WriteString(`</div>`)
	return rows.String()
}

func disputeStrikeDetailItemHTML(title string, content string) string {
	return `<div class="report-header__details__item"><div class="report-header__details__item__header"><strong>` + html.EscapeString(title) + `</strong></div><div class="report-header__details__item__content">` + content + `</div></div>`
}

func disputeStrikeTimeHTML(t time.Time) string {
	stamp := t.UTC().Format(time.RFC3339)
	return `<time class="formatted" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(stamp) + `">` + html.EscapeString(stamp) + `</time>`
}

func disputeAppealStateHTML(appeal *models.Appeal, locale string) string {
	if appeal == nil {
		return ""
	}
	if appeal.ApprovedAt.Valid {
		return `<p class="hint"><span class="positive-hint"><i class="fa fa-check fa-fw"></i> ` + html.EscapeString(settingsT(locale, "disputes.strikes.appeal_approved", "This strike has been successfully appealed and is no longer valid")) + `</span></p>`
	}
	if appeal.RejectedAt.Valid {
		return `<p class="hint"><span class="negative-hint"><i class="fa fa-times fa-fw"></i> ` + html.EscapeString(settingsT(locale, "disputes.strikes.appeal_rejected", "The appeal has been rejected")) + `</span></p>`
	}
	return ""
}

func disputeAppealHTML(strike models.AccountWarning, appeal *models.Appeal, locale string) string {
	if appeal != nil && appeal.ID > 0 {
		stateClass := "warning-hint"
		state := settingsT(locale, "disputes.strikes.your_appeal_pending", "You have submitted an appeal")
		if appeal.ApprovedAt.Valid {
			stateClass = "positive-hint"
			state = settingsT(locale, "disputes.strikes.your_appeal_approved", "Your appeal has been approved")
		} else if appeal.RejectedAt.Valid {
			stateClass = "negative-hint"
			state = settingsT(locale, "disputes.strikes.your_appeal_rejected", "Your appeal has been rejected")
		}
		accountName := accountDisplayName(appeal.Account)
		accountHref := "/@" + url.PathEscape(accountName)
		if strings.TrimSpace(accountName) == "" {
			accountName = strconv.FormatInt(appeal.AccountID, 10)
			accountHref = "/admin/accounts/" + accountName
		}
		stamp := appeal.CreatedAt.UTC().Format(time.RFC3339)
		return `<h3>` + html.EscapeString(settingsT(locale, "disputes.strikes.appeal", "Appeal")) + `</h3><div class="report-notes"><div class="report-notes__item"><img src="/avatars/original/missing.png" class="report-notes__item__avatar" alt=""><div class="report-notes__item__header"><span class="username"><a href="` + html.EscapeString(accountHref) + `" class="table-action-link">` + html.EscapeString(accountName) + `</a></span> <time class="relative-formatted" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(stamp) + `">` + html.EscapeString(appeal.CreatedAt.UTC().Format("2006-01-02")) + `</time> · <span class="` + stateClass + `">` + html.EscapeString(state) + `</span></div><div class="report-notes__item__content">` + disputeStrikeTextBlock(appeal.Text) + `</div></div></div>`
	}
	if appealStrikeWithinTimeFrame(strike.CreatedAt, time.Now().UTC()) {
		value := ""
		if appeal != nil {
			value = appeal.Text
		}
		return `<h3>` + html.EscapeString(settingsT(locale, "disputes.strikes.appeals.submit", "Submit appeal")) + `</h3><form class="simple_form" method="post" action="/disputes/strikes/` + strconv.FormatInt(strike.ID, 10) + `/appeal">
      <div class="fields-group"><div class="input with_label text optional appeal_text"><label class="text optional" for="appeal_text">` + html.EscapeString(settingsT(locale, "admin.reports.notes.label", "Text")) + `</label><textarea id="appeal_text" name="appeal[text]" maxlength="500" required>` + html.EscapeString(value) + `</textarea></div></div>
      <div class="actions"><button class="button" type="submit">` + html.EscapeString(settingsT(locale, "disputes.strikes.appeals.submit", "Submit appeal")) + `</button></div>
    </form>`
	}
	return `<p class="hint">` + html.EscapeString(settingsT(locale, "strikes.errors.too_late", "Appeal period has expired.")) + `</p>`
}

func appealStrikeWithinTimeFrame(strikeCreatedAt time.Time, now time.Time) bool {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !strikeCreatedAt.Before(now.Add(-appealMaxStrikeAge))
}

func accountDisplayName(account models.Account) string {
	if strings.TrimSpace(account.Username) != "" {
		return account.Username
	}
	if strings.TrimSpace(account.Acct()) != "" {
		return account.Acct()
	}
	if account.ID > 0 {
		return strconv.FormatInt(account.ID, 10)
	}
	return ""
}

func localDomainFromBaseURL(base string) string {
	if base == "" {
		return ""
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func disputeStrikeHTML(strike models.AccountWarning, appeal *models.Appeal, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	card := `<div class="strike-card">`
	if strings.TrimSpace(strike.Text) != "" {
		card += disputeStrikeTextBlock(strike.Text)
	}
	card += disputeStrikeStatusListHTML(strike, loc) + `</div>`
	target := accountDisplayName(strike.TargetAccount)
	if target == "" && strike.TargetAccountID.Valid {
		target = strconv.FormatInt(strike.TargetAccountID.Int64, 10)
	}
	actionContent := html.EscapeString(accountWarningActionLabel(strike.Action, loc))
	if strike.OverruledAt.Valid {
		actionContent = `<del>` + actionContent + `</del>`
	}
	details := disputeStrikeDetailItemHTML(settingsT(loc, "disputes.strikes.created_at", "Dated"), disputeStrikeTimeHTML(strike.CreatedAt))
	details += disputeStrikeDetailItemHTML(settingsT(loc, "disputes.strikes.recipient", "Addressed to"), html.EscapeString(target))
	details += disputeStrikeDetailItemHTML(settingsT(loc, "disputes.strikes.action_taken", "Action taken"), actionContent)
	if strike.ReportID.Valid {
		reportLabel := adminTVars(loc, "admin.reports.report", "Report #%{id}", map[string]string{"id": strconv.FormatInt(strike.ReportID.Int64, 10)})
		details += disputeStrikeDetailItemHTML(settingsT(loc, "disputes.strikes.associated_report", "Associated report"), `<a class="table-action-link" href="/admin/reports/`+strconv.FormatInt(strike.ReportID.Int64, 10)+`">`+html.EscapeString(reportLabel)+`</a>`)
	}
	if appeal != nil {
		details += disputeStrikeDetailItemHTML(settingsT(loc, "disputes.strikes.appeal_submitted_at", "Appeal submitted"), disputeStrikeTimeHTML(appeal.CreatedAt))
	}
	body := settingsFlashHTML(notice, errorText) + `<p><a class="table-action-link" href="/disputes/strikes">` + html.EscapeString(settingsT(loc, "settings.strikes", "Back to strikes")) + `</a></p>` +
		disputeAppealStateHTML(appeal, loc) +
		`<div class="report-header"><div class="report-header__card">` + card + `</div><div class="report-header__details">` + details + `</div></div><hr class="spacer">` +
		disputeAppealHTML(strike, appeal, loc)
	title := disputeStrikeTitle(strike, loc)
	return settingsPageShell(title, settingsNavigationArg(localeAndTheme, loc), body, loc, theme)
}

func settingsFlashHTML(notice string, errorText string) string {
	var flashes strings.Builder
	if strings.TrimSpace(notice) != "" {
		flashes.WriteString(`<div class="flash-message notice"><strong>` + html.EscapeString(notice) + `</strong></div>`)
	}
	if strings.TrimSpace(errorText) != "" {
		flashes.WriteString(`<div class="flash-message alert"><strong>` + html.EscapeString(errorText) + `</strong></div>`)
	}
	return flashes.String()
}

func accountWarningAction(action int) string {
	switch action {
	case 1000:
		return "disable"
	case 1250:
		return "mark_statuses_as_sensitive"
	case 1500:
		return "delete_statuses"
	case 2000:
		return "sensitive"
	case 3000:
		return "silence"
	case 4000:
		return "suspend"
	default:
		return "none"
	}
}

func accountWarningActionLabel(action int, locale string) string {
	key := accountWarningAction(action)
	return settingsT(locale, "disputes.strikes.title_actions."+key, key)
}

func trimForTable(value string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= 120 {
		return value
	}
	return string([]rune(value)[:120]) + "..."
}
