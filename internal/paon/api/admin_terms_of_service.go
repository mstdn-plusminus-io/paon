package api

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

var (
	errAdminTermsParamsMissing        = errors.New("admin terms of service root parameter is missing")
	errAdminTermsTextBlank            = errors.New("terms of service text can't be blank")
	errAdminTermsChangelogBlank       = errors.New("terms of service changelog can't be blank")
	errAdminTermsEffectiveDateBlank   = errors.New("terms of service effective date can't be blank")
	errAdminTermsEffectiveDateTaken   = errors.New("terms of service effective date has already been taken")
	errAdminTermsEffectiveDateTooSoon = errors.New("terms of service effective date is too soon")
)

type adminTermsOfServiceForm struct {
	Text              string
	Changelog         string
	EffectiveDate     sql.NullTime
	EffectiveDateText string
}

var errAdminTermsGeneratorParamsMissing = errors.New("admin terms of service generator root parameter is missing")

var adminTermsOfServiceGeneratorVariables = []string{
	"admin_email",
	"arbitration_address",
	"arbitration_website",
	"choice_of_law",
	"dmca_address",
	"dmca_email",
	"domain",
	"jurisdiction",
	"min_age",
}

type adminTermsOfServiceGeneratorForm struct {
	AdminEmail         string
	ArbitrationAddress string
	ArbitrationWebsite string
	ChoiceOfLaw        string
	DMCAAddress        string
	DMCAEmail          string
	Domain             string
	Jurisdiction       string
	MinimumAge         string
}

//go:embed terms_of_service_template.md
var mastodon44TermsOfServiceTemplate string

func (s *Server) adminTermsOfServicePage(c *echo.Context) error {
	user, theme, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	terms, err := s.latestPublishedTermsOfService(c.Request().Context())
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminTermsOfServiceIndexHTML(s, terms, c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) adminTermsOfServiceGeneratePage(c *echo.Context) error {
	user, theme, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	form := adminTermsOfServiceGeneratorForm{
		Domain:     firstNonEmpty(strings.TrimSpace(s.cfg.LocalDomain), strings.TrimSpace(s.cfg.WebDomain)),
		AdminEmail: strings.TrimSpace(s.settingStringValue("site_contact_email", "")),
	}
	return c.HTML(http.StatusOK, adminTermsOfServiceGeneratorHTML(form, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user), theme))
}

func (s *Server) generateAdminTermsOfService(c *echo.Context) error {
	user, theme, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminTermsOfServiceGeneratorForm(c)
	if err != nil {
		if errors.Is(err, errAdminTermsGeneratorParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminTermsOfServiceGeneratorHTML(form, "", adminTermsOfServiceGeneratorErrorText(locale), locale, theme))
	}
	if err := validateAdminTermsOfServiceGeneratorForm(form); err != nil {
		return c.HTML(http.StatusOK, adminTermsOfServiceGeneratorHTML(form, "", adminTermsOfServiceGeneratorErrorText(locale), locale, theme))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminTermsOfServiceGeneratorHTML(form, "", adminT(locale, "admin.terms_of_service.errors.database_unavailable", "DATABASE_URL is not set"), locale, theme))
	}
	if _, err := s.createGeneratedTermsOfServiceDraft(c.Request().Context(), form, time.Now().UTC()); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/terms_of_service/draft")
}

func (s *Server) createGeneratedTermsOfServiceDraft(ctx context.Context, form adminTermsOfServiceGeneratorForm, now time.Time) (models.TermsOfService, error) {
	draft := models.TermsOfService{
		Text:      renderAdminTermsOfServiceTemplate(form),
		Changelog: "",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if s == nil || s.db == nil {
		return draft, gorm.ErrInvalidDB
	}
	err := s.db.WithContext(ctx).Create(&draft).Error
	return draft, err
}

func (s *Server) adminTermsOfServiceDraftPage(c *echo.Context) error {
	user, theme, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	draft, err := s.adminTermsOfServiceDraft(c.Request().Context(), time.Now().UTC())
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminTermsOfServiceDraftHTML(adminTermsOfServiceFormFromModel(draft), c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user), theme))
}

func (s *Server) updateAdminTermsOfServiceDraft(c *echo.Context) error {
	user, theme, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminTermsOfServiceForm(c)
	if err != nil {
		if errors.Is(err, errAdminTermsParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminTermsOfServiceDraftHTML(form, "", adminTermsOfServiceErrorText(locale, err), locale, theme))
	}
	publish := strings.EqualFold(strings.TrimSpace(c.FormValue("action_type")), "publish")
	if err := validateAdminTermsOfServiceForm(form, publish); err != nil {
		return c.HTML(http.StatusOK, adminTermsOfServiceDraftHTML(form, "", adminTermsOfServiceErrorText(locale, err), locale, theme))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminTermsOfServiceDraftHTML(form, "", adminT(locale, "admin.terms_of_service.errors.database_unavailable", "DATABASE_URL is not set"), locale, theme))
	}
	now := time.Now().UTC()
	draft, err := s.adminTermsOfServiceDraft(c.Request().Context(), now)
	if err != nil {
		return err
	}
	if _, err := s.saveAdminTermsOfServiceDraft(c.Request().Context(), user.AccountID, draft.ID, form, publish, now); err != nil {
		if isAdminTermsOfServiceValidationError(err) {
			return c.HTML(http.StatusOK, adminTermsOfServiceDraftHTML(form, "", adminTermsOfServiceErrorText(locale, err), locale, theme))
		}
		return err
	}
	if publish {
		return c.Redirect(http.StatusFound, "/admin/terms_of_service?notice="+url.QueryEscape(adminT(locale, "admin.terms_of_service.published", "Terms of service published")))
	}
	return c.Redirect(http.StatusFound, "/admin/terms_of_service/draft?notice="+url.QueryEscape(adminT(locale, "admin.terms_of_service.draft_saved", "Draft saved")))
}

func (s *Server) adminTermsOfServiceHistoryPage(c *echo.Context) error {
	user, theme, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	terms, err := s.publishedTermsOfService(c.Request().Context())
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminTermsOfServiceHistoryHTML(terms, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user), theme))
}

func (s *Server) adminTermsOfServicePreviewPage(c *echo.Context) error {
	user, theme, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	terms, err := s.findDistributableTermsOfService(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	count, err := s.termsOfServiceNotificationUserCount(c.Request().Context(), terms)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminTermsOfServicePreviewHTML(terms, count, user.Email, c.QueryParam("notice"), c.QueryParam("error"), s.webLocale(c, user), theme))
}

func (s *Server) testAdminTermsOfServiceDistribution(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	terms, err := s.findDistributableTermsOfService(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if err := s.enqueueOrDeliverBulkMail(*user, "terms_of_service", termsOfServiceChangedMailMessage(s.cfg, *user, terms)); err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.Redirect(http.StatusFound, "/admin/terms_of_service/"+strconv.FormatInt(terms.ID, 10)+"/preview?notice="+url.QueryEscape(adminT(locale, "admin.terms_of_service.preview.sent", "Preview email sent")))
}

func (s *Server) distributeAdminTermsOfService(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	terms, err := s.findDistributableTermsOfService(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result := s.db.WithContext(c.Request().Context()).Model(&models.TermsOfService{}).
		Where("id = ? AND published_at IS NOT NULL AND notification_sent_at IS NULL", terms.ID).
		Updates(map[string]any{"notification_sent_at": sql.NullTime{Time: now, Valid: true}, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return echo.NewHTTPError(http.StatusForbidden, "terms of service notification has already been sent")
	}
	if err := s.enqueueTermsOfServiceDistributionTask(terms.ID); err != nil {
		// Enqueueing is the commit boundary for distribution. Re-open the action only
		// when this exact attempt still owns the timestamp, so a concurrent request
		// can never turn a successfully queued notification back into a draft action.
		_ = s.db.WithContext(c.Request().Context()).Model(&models.TermsOfService{}).
			Where("id = ? AND notification_sent_at = ?", terms.ID, now).
			Updates(map[string]any{"notification_sent_at": nil, "updated_at": time.Now().UTC()}).Error
		return err
	}
	locale := s.webLocale(c, user)
	return c.Redirect(http.StatusFound, "/admin/terms_of_service?notice="+url.QueryEscape(adminT(locale, "admin.terms_of_service.distribution_started", "User notification distribution started")))
}

func (s *Server) latestPublishedTermsOfService(ctx context.Context) (*models.TermsOfService, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var terms models.TermsOfService
	err := s.db.WithContext(ctx).Where("published_at IS NOT NULL").
		Order("COALESCE(effective_date::timestamp, published_at) DESC").First(&terms).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &terms, err
}

func (s *Server) publishedTermsOfService(ctx context.Context) ([]models.TermsOfService, error) {
	if s == nil || s.db == nil {
		return []models.TermsOfService{}, nil
	}
	var terms []models.TermsOfService
	err := s.db.WithContext(ctx).Where("published_at IS NOT NULL").
		Order("COALESCE(effective_date::timestamp, published_at) DESC").Find(&terms).Error
	return terms, err
}

func (s *Server) adminTermsOfServiceDraft(ctx context.Context, now time.Time) (models.TermsOfService, error) {
	if s == nil || s.db == nil {
		return models.TermsOfService{EffectiveDate: sql.NullTime{Time: termsOfServiceUTCDay(now).AddDate(0, 0, 10), Valid: true}}, nil
	}
	var draft models.TermsOfService
	err := s.db.WithContext(ctx).Where("published_at IS NULL").Order("id DESC").First(&draft).Error
	if err == nil {
		return draft, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return draft, err
	}
	var current models.TermsOfService
	currentErr := s.db.WithContext(ctx).
		Where("published_at IS NOT NULL AND (effective_date IS NULL OR effective_date < ?)", now).
		Order("COALESCE(effective_date::timestamp, published_at) DESC").
		First(&current).Error
	if currentErr == nil {
		draft.Text = current.Text
	} else if !errors.Is(currentErr, gorm.ErrRecordNotFound) {
		return draft, currentErr
	}
	draft.EffectiveDate = sql.NullTime{Time: termsOfServiceUTCDay(now).AddDate(0, 0, 10), Valid: true}
	return draft, nil
}

func (s *Server) saveAdminTermsOfServiceDraft(ctx context.Context, actorAccountID int64, draftID int64, form adminTermsOfServiceForm, publish bool, now time.Time) (models.TermsOfService, error) {
	var saved models.TermsOfService
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateAdminTermsOfServiceForm(form, publish); err != nil {
			return err
		}
		if form.EffectiveDate.Valid {
			var count int64
			query := tx.Model(&models.TermsOfService{}).Where("effective_date = ?", form.EffectiveDate.Time)
			if draftID > 0 {
				query = query.Where("id <> ?", draftID)
			}
			if err := query.Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return errAdminTermsEffectiveDateTaken
			}
			minimum, err := minimumTermsOfServiceEffectiveDate(tx, now)
			if err != nil {
				return err
			}
			if form.EffectiveDate.Time.Before(minimum) {
				return errAdminTermsEffectiveDateTooSoon
			}
		}

		if draftID > 0 {
			if err := tx.Where("id = ? AND published_at IS NULL", draftID).First(&saved).Error; err != nil {
				return err
			}
		} else {
			saved.CreatedAt = now
		}
		saved.Text = form.Text
		saved.Changelog = form.Changelog
		saved.EffectiveDate = form.EffectiveDate
		saved.UpdatedAt = now
		if publish {
			saved.PublishedAt = sql.NullTime{Time: now, Valid: true}
		}
		if saved.ID == 0 {
			if err := tx.Create(&saved).Error; err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "effective_date") && strings.Contains(strings.ToLower(err.Error()), "unique") {
					return errAdminTermsEffectiveDateTaken
				}
				return err
			}
		} else {
			result := tx.Model(&models.TermsOfService{}).Where("id = ? AND published_at IS NULL", saved.ID).Updates(map[string]any{
				"text":           saved.Text,
				"changelog":      saved.Changelog,
				"effective_date": nullableTimeValue(saved.EffectiveDate),
				"published_at":   nullableTimeValue(saved.PublishedAt),
				"updated_at":     now,
			})
			if result.Error != nil {
				if strings.Contains(strings.ToLower(result.Error.Error()), "effective_date") && strings.Contains(strings.ToLower(result.Error.Error()), "unique") {
					return errAdminTermsEffectiveDateTaken
				}
				return result.Error
			}
			if result.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}
		if publish {
			return logAdminAction(tx, actorAccountID, "publish", termsOfServiceAuditLogTarget(saved), now)
		}
		return nil
	})
	return saved, err
}

func nullableTimeValue(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time
}

func minimumTermsOfServiceEffectiveDate(tx *gorm.DB, now time.Time) (time.Time, error) {
	minimum := termsOfServiceUTCDay(now)
	var current models.TermsOfService
	err := tx.Where("published_at IS NOT NULL AND (effective_date IS NULL OR effective_date < ?)", now).
		Order("COALESCE(effective_date::timestamp, published_at) DESC").First(&current).Error
	if err == nil && current.EffectiveDate.Valid {
		minimum = termsOfServiceUTCDay(current.EffectiveDate.Time)
		return minimum, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, err
	}
	return minimum, nil
}

func termsOfServiceAuditLogTarget(terms models.TermsOfService) adminAuditLogTarget {
	identifier := ""
	if terms.EffectiveDate.Valid {
		identifier = terms.EffectiveDate.Time.Format("2006-01-02")
	}
	return adminAuditLogTarget{Type: "TermsOfService", ID: terms.ID, HumanIdentifier: identifier}
}

func (s *Server) findDistributableTermsOfService(ctx context.Context, rawID string) (models.TermsOfService, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
	if err != nil || id <= 0 || s == nil || s.db == nil {
		return models.TermsOfService{}, echo.NewHTTPError(http.StatusNotFound, "terms of service not found")
	}
	var terms models.TermsOfService
	if err := s.db.WithContext(ctx).Where("id = ? AND published_at IS NOT NULL", id).First(&terms).Error; err != nil {
		return models.TermsOfService{}, echo.NewHTTPError(http.StatusNotFound, "terms of service not found")
	}
	if terms.NotificationSentAt.Valid {
		return models.TermsOfService{}, echo.NewHTTPError(http.StatusForbidden, "terms of service notification has already been sent")
	}
	return terms, nil
}

func (s *Server) termsOfServiceNotificationUserCount(ctx context.Context, terms models.TermsOfService) (int64, error) {
	if s == nil || s.db == nil || !terms.PublishedAt.Valid {
		return 0, nil
	}
	var count int64
	err := termsOfServiceNotificationUsersQuery(s.db.WithContext(ctx), terms).Count(&count).Error
	return count, err
}

func termsOfServiceBaseUsersQuery(db *gorm.DB, terms models.TermsOfService) *gorm.DB {
	return db.Model(&models.User{}).Joins("JOIN accounts ON accounts.id = users.account_id").
		Where("users.confirmed_at IS NOT NULL").Where("users.created_at <= ?", terms.PublishedAt.Time)
}

func termsOfServiceNotificationUsersQuery(db *gorm.DB, terms models.TermsOfService) *gorm.DB {
	cutoff := termsOfServiceNotificationCutoff(terms)
	return termsOfServiceBaseUsersQuery(db, terms).
		Where("accounts.suspended_at IS NULL").Where("users.current_sign_in_at >= ?", cutoff)
}

func termsOfServiceNotificationCutoff(terms models.TermsOfService) time.Time {
	published := terms.PublishedAt.Time
	year := published.Year() - 1
	day := published.Day()
	lastDay := time.Date(year, published.Month()+1, 0, 0, 0, 0, 0, published.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, published.Month(), day, published.Hour(), published.Minute(), published.Second(), published.Nanosecond(), published.Location())
}

func parseAdminTermsOfServiceForm(c *echo.Context) (adminTermsOfServiceForm, error) {
	if err := c.Request().ParseForm(); err != nil {
		return adminTermsOfServiceForm{}, err
	}
	if !formHasNestedPrefix(c.Request().Form, "terms_of_service") {
		return adminTermsOfServiceForm{}, errAdminTermsParamsMissing
	}
	form := adminTermsOfServiceForm{
		Text:              lastFormValue(c.Request().Form, "terms_of_service[text]"),
		Changelog:         lastFormValue(c.Request().Form, "terms_of_service[changelog]"),
		EffectiveDateText: strings.TrimSpace(lastFormValue(c.Request().Form, "terms_of_service[effective_date]")),
	}
	if form.EffectiveDateText != "" {
		parsed, err := time.Parse("2006-01-02", form.EffectiveDateText)
		if err != nil {
			return form, err
		}
		form.EffectiveDate = sql.NullTime{Time: parsed.UTC(), Valid: true}
	}
	return form, nil
}

func validateAdminTermsOfServiceForm(form adminTermsOfServiceForm, publish bool) error {
	if strings.TrimSpace(form.Text) == "" {
		return errAdminTermsTextBlank
	}
	if !publish {
		return nil
	}
	if strings.TrimSpace(form.Changelog) == "" {
		return errAdminTermsChangelogBlank
	}
	if !form.EffectiveDate.Valid {
		return errAdminTermsEffectiveDateBlank
	}
	return nil
}

func isAdminTermsOfServiceValidationError(err error) bool {
	return errors.Is(err, errAdminTermsTextBlank) || errors.Is(err, errAdminTermsChangelogBlank) ||
		errors.Is(err, errAdminTermsEffectiveDateBlank) || errors.Is(err, errAdminTermsEffectiveDateTaken) ||
		errors.Is(err, errAdminTermsEffectiveDateTooSoon)
}

func adminTermsOfServiceErrorText(locale string, err error) string {
	keys := map[error]struct {
		key      string
		fallback string
	}{
		errAdminTermsTextBlank:            {"text_blank", "Terms of service text can't be blank"},
		errAdminTermsChangelogBlank:       {"changelog_blank", "Changelog can't be blank when publishing"},
		errAdminTermsEffectiveDateBlank:   {"effective_date_blank", "Effective date can't be blank when publishing"},
		errAdminTermsEffectiveDateTaken:   {"effective_date_taken", "Effective date has already been taken"},
		errAdminTermsEffectiveDateTooSoon: {"effective_date_too_soon", "Effective date can't be in the past"},
	}
	for target, message := range keys {
		if errors.Is(err, target) {
			return adminT(locale, "admin.terms_of_service.errors."+message.key, message.fallback)
		}
	}
	return adminT(locale, "admin.terms_of_service.errors.invalid", "Terms of service are invalid")
}

func adminTermsOfServiceFormFromModel(terms models.TermsOfService) adminTermsOfServiceForm {
	form := adminTermsOfServiceForm{Text: terms.Text, Changelog: terms.Changelog, EffectiveDate: terms.EffectiveDate}
	if terms.EffectiveDate.Valid {
		form.EffectiveDateText = terms.EffectiveDate.Time.Format("2006-01-02")
	}
	return form
}

func parseAdminTermsOfServiceGeneratorForm(c *echo.Context) (adminTermsOfServiceGeneratorForm, error) {
	if err := c.Request().ParseForm(); err != nil {
		return adminTermsOfServiceGeneratorForm{}, err
	}
	if !formHasNestedPrefix(c.Request().Form, "terms_of_service_generator") {
		return adminTermsOfServiceGeneratorForm{}, errAdminTermsGeneratorParamsMissing
	}
	value := func(name string) string {
		return strings.TrimSpace(lastFormValue(c.Request().Form, "terms_of_service_generator["+name+"]"))
	}
	return adminTermsOfServiceGeneratorForm{
		AdminEmail:         value("admin_email"),
		ArbitrationAddress: value("arbitration_address"),
		ArbitrationWebsite: value("arbitration_website"),
		ChoiceOfLaw:        value("choice_of_law"),
		DMCAAddress:        value("dmca_address"),
		DMCAEmail:          value("dmca_email"),
		Domain:             value("domain"),
		Jurisdiction:       value("jurisdiction"),
		MinimumAge:         value("min_age"),
	}, nil
}

func validateAdminTermsOfServiceGeneratorForm(form adminTermsOfServiceGeneratorForm) error {
	values := map[string]string{
		"admin_email":         form.AdminEmail,
		"arbitration_address": form.ArbitrationAddress,
		"arbitration_website": form.ArbitrationWebsite,
		"choice_of_law":       form.ChoiceOfLaw,
		"dmca_address":        form.DMCAAddress,
		"dmca_email":          form.DMCAEmail,
		"domain":              form.Domain,
		"jurisdiction":        form.Jurisdiction,
		"min_age":             form.MinimumAge,
	}
	for _, name := range adminTermsOfServiceGeneratorVariables {
		if strings.TrimSpace(values[name]) == "" {
			return errors.New(name + " can't be blank")
		}
	}
	return nil
}

func renderAdminTermsOfServiceTemplate(form adminTermsOfServiceGeneratorForm) string {
	replacer := strings.NewReplacer(
		"%{admin_email}", form.AdminEmail,
		"%{arbitration_address}", form.ArbitrationAddress,
		"%{arbitration_website}", form.ArbitrationWebsite,
		"%{choice_of_law}", form.ChoiceOfLaw,
		"%{dmca_address}", form.DMCAAddress,
		"%{dmca_email}", form.DMCAEmail,
		"%{domain}", form.Domain,
		"%{jurisdiction}", form.Jurisdiction,
		"%{min_age}", form.MinimumAge,
	)
	return replacer.Replace(mastodon44TermsOfServiceTemplate)
}

func adminTermsOfServiceGeneratorErrorText(locale string) string {
	return adminT(locale, "admin.terms_of_service.generates.errors.blank", "All fields are required")
}

func adminTermsOfServiceNavigationHTML(active string, locale string) string {
	items := []struct{ id, href, icon, key, fallback string }{
		{"current", "/admin/terms_of_service", "file-text", "current", "Current"},
		{"draft", "/admin/terms_of_service/draft", "pencil", "draft", "Draft"},
		{"history", "/admin/terms_of_service/history", "history", "history", "History"},
	}
	var body strings.Builder
	body.WriteString(`<nav class="content__heading__tabs"><div>`)
	for _, item := range items {
		className := ""
		if item.id == active {
			className = ` class="selected simple-navigation-active-leaf"`
		}
		body.WriteString(`<a id="terms-of-service-` + item.id + `"` + className + ` href="` + item.href + `"><i class="fa fa-` + item.icon + ` fa-fw"></i>` + html.EscapeString(adminT(locale, "admin.terms_of_service."+item.key, item.fallback)) + `</a>`)
	}
	body.WriteString(`</div></nav>`)
	return body.String()
}

func adminTermsOfServiceIndexHTML(s *Server, terms *models.TermsOfService, notice, errorText, locale, theme string) string {
	body := adminTermsOfServiceNavigationHTML("current", locale)
	if terms == nil {
		body += `<p class="lead">` + adminT(locale, "admin.terms_of_service.no_terms_of_service_html", "No terms of service are configured yet.") + `</p><div class="content__heading__actions"><a class="button button-secondary" href="/admin/terms_of_service/generate">` + html.EscapeString(adminT(locale, "admin.terms_of_service.generate", "Use template")) + `</a> <a class="button" href="/admin/terms_of_service/draft">` + html.EscapeString(adminT(locale, "admin.terms_of_service.create", "Use your own")) + `</a></div>`
		return authPageHTML(adminT(locale, "admin.terms_of_service.title", "Terms of Service"), notice, errorText, body, locale, theme)
	}
	status := adminT(locale, "admin.terms_of_service.live", "Live")
	if terms.EffectiveDate.Valid && !terms.EffectiveDate.Time.Before(termsOfServiceUTCDay(time.Now().UTC())) {
		status = adminTVars(locale, "admin.terms_of_service.going_live_on_html", "Live, effective %{date}", map[string]string{"date": formatOptionalDate(locale, terms.EffectiveDate.Time)})
	}
	body += `<div class="admin__terms-of-service__container"><div class="admin__terms-of-service__container__header"><span class="dot-indicator success"><span class="dot-indicator__indicator"></span><span>` + status + `</span></span> &middot; <span>` + html.EscapeString(adminTVars(locale, "admin.terms_of_service.published_on_html", "Published on %{date}", map[string]string{"date": formatOptionalDate(locale, terms.PublishedAt.Time)})) + `</span> &middot; `
	if terms.NotificationSentAt.Valid {
		body += `<span>` + html.EscapeString(adminTVars(locale, "admin.terms_of_service.notified_on_html", "Users notified on %{date}", map[string]string{"date": formatOptionalDate(locale, terms.NotificationSentAt.Time)})) + `</span>`
	} else {
		body += `<a class="link-button" href="/admin/terms_of_service/` + strconv.FormatInt(terms.ID, 10) + `/preview">` + html.EscapeString(adminT(locale, "admin.terms_of_service.notify_users", "Notify users")) + `</a>`
	}
	content := serializer.TermsOfServiceFromModel(s.cfg, *terms, nil, time.Now().UTC()).Content
	body += `</div><div class="admin__terms-of-service__container__body"><div class="prose">` + content + `</div></div></div><hr class="spacer"><h3>` + html.EscapeString(adminT(locale, "admin.terms_of_service.changelog", "What's changed")) + `</h3><div class="prose">` + serializer.MarkdownHTML(terms.Changelog) + `</div>`
	return authPageHTML(adminT(locale, "admin.terms_of_service.title", "Terms of Service"), notice, errorText, body, locale, theme)
}

func adminTermsOfServiceGeneratorHTML(form adminTermsOfServiceGeneratorForm, notice, errorText, locale, theme string) string {
	fields := []struct {
		name, value, label, hint, inputType string
	}{
		{"domain", form.Domain, "Server domain", "", "text"},
		{"min_age", form.MinimumAge, "Minimum age", "", "number"},
		{"jurisdiction", form.Jurisdiction, "Jurisdiction", "", "text"},
		{"choice_of_law", form.ChoiceOfLaw, "Choice of law", "City, region, territory or state whose laws will govern claims.", "text"},
		{"admin_email", form.AdminEmail, "Email address for legal notices", "Legal notices include court orders, takedown requests, and law enforcement requests.", "email"},
		{"dmca_email", form.DMCAEmail, "Email address for DMCA notices", "", "email"},
		{"dmca_address", form.DMCAAddress, "Physical address for DMCA notices", "", "text"},
		{"arbitration_address", form.ArbitrationAddress, "Physical address for arbitration notices", "Can be the same as the physical address above, or N/A if using email.", "text"},
		{"arbitration_website", form.ArbitrationWebsite, "Website for submitting arbitration notices", "Can be a web form, or N/A if using email.", "text"},
	}
	var inputs strings.Builder
	for _, field := range fields {
		label := adminT(locale, "simple_form.labels.terms_of_service_generator."+field.name, field.label)
		hint := adminT(locale, "simple_form.hints.terms_of_service_generator."+field.name, field.hint)
		id := "terms_of_service_generator_" + field.name
		inputs.WriteString(`<div class="fields-group"><div class="input with_label string required terms_of_service_generator_` + field.name + ` field_with_hint"><label for="` + id + `">` + html.EscapeString(label) + filterRequiredMarker(locale) + `</label>`)
		if strings.TrimSpace(hint) != "" {
			inputs.WriteString(`<span class="hint">` + html.EscapeString(hint) + `</span>`)
		}
		inputs.WriteString(`<div class="label_input"><input type="` + field.inputType + `" id="` + id + `" name="terms_of_service_generator[` + field.name + `]" value="` + html.EscapeString(field.value) + `" required="required" aria-required="true"></div></div></div>`)
	}
	body := `<div class="back-link"><a href="/admin/terms_of_service"><i class="fa fa-chevron-left fa-fw"></i>` + html.EscapeString(adminT(locale, "admin.terms_of_service.back", "Back to terms of service")) + `</a></div>` +
		`<p class="lead">` + adminT(locale, "admin.terms_of_service.generates.explanation_html", "The provided template is for informational purposes only and is not legal advice. Consult your own legal counsel about your situation.") + `</p>` +
		`<p class="lead">` + adminT(locale, "admin.terms_of_service.generates.chance_to_review_html", "<strong>The generated terms of service will not be published automatically.</strong> You can review them before publishing.") + `</p><hr class="spacer">` +
		simpleFormOpen("/admin/terms_of_service/generate", "post") + inputs.String() + simpleSubmit(adminT(locale, "admin.terms_of_service.generates.action", "Generate")) + simpleFormClose()
	return authPageHTML(adminT(locale, "admin.terms_of_service.generates.title", "Terms of Service Setup"), notice, errorText, body, locale, theme)
}

func adminTermsOfServiceDraftHTML(form adminTermsOfServiceForm, notice, errorText, locale, theme string) string {
	body := adminTermsOfServiceNavigationHTML("draft", locale) + simpleFormOpen("/admin/terms_of_service/draft", "put") +
		adminTermsOfServiceTextArea("text", adminT(locale, "admin.terms_of_service.fields.text", "Terms of Service"), adminT(locale, "admin.terms_of_service.hints.text", "Can be structured with Markdown syntax."), form.Text, true) +
		adminTermsOfServiceTextArea("changelog", adminT(locale, "admin.terms_of_service.fields.changelog", "What's changed?"), adminT(locale, "admin.terms_of_service.hints.changelog", "Can be structured with Markdown syntax."), form.Changelog, false) +
		`<div class="fields-group"><div class="input with_block_label date optional terms_of_service_effective_date field_with_hint"><label for="terms_of_service_effective_date">` + html.EscapeString(adminT(locale, "admin.terms_of_service.fields.effective_date", "Effective date")) + `</label><span class="hint">` + html.EscapeString(adminT(locale, "admin.terms_of_service.hints.effective_date", "A reasonable timeframe is 10 to 30 days after notifying users.")) + `</span><div class="label_input"><input type="date" id="terms_of_service_effective_date" name="terms_of_service[effective_date]" value="` + html.EscapeString(form.EffectiveDateText) + `"></div></div></div>` +
		`<div class="actions"><button class="button button-secondary" type="submit" name="action_type" value="save_draft">` + html.EscapeString(adminT(locale, "admin.terms_of_service.save_draft", "Save draft")) + `</button> <button class="button" type="submit" name="action_type" value="publish">` + html.EscapeString(adminT(locale, "admin.terms_of_service.publish", "Publish")) + `</button></div>` + simpleFormClose()
	return authPageHTML(adminT(locale, "admin.terms_of_service.title", "Terms of Service"), notice, errorText, body, locale, theme)
}

func adminTermsOfServiceTextArea(name, label, hint, value string, required bool) string {
	requiredAttrs := ""
	requiredClass := "optional"
	if required {
		requiredAttrs = ` required="required" aria-required="true"`
		requiredClass = "required"
	}
	return `<div class="fields-group"><div class="input with_block_label text ` + requiredClass + ` terms_of_service_` + name + ` field_with_hint"><label for="terms_of_service_` + name + `">` + html.EscapeString(label) + `</label><span class="hint">` + html.EscapeString(hint) + `</span><div class="label_input"><textarea rows="8" id="terms_of_service_` + name + `" name="terms_of_service[` + name + `]"` + requiredAttrs + `>` + html.EscapeString(value) + `</textarea></div></div></div>`
}

func adminTermsOfServiceHistoryHTML(terms []models.TermsOfService, notice, errorText, locale, theme string) string {
	body := adminTermsOfServiceNavigationHTML("history", locale)
	if len(terms) == 0 {
		body += `<p>` + html.EscapeString(adminT(locale, "admin.terms_of_service.no_history", "There are no recorded changes yet.")) + `</p>`
	} else {
		body += `<ol class="admin__terms-of-service__history">`
		for _, terms := range terms {
			published := formatOptionalDate(locale, terms.PublishedAt.Time)
			title := html.EscapeString(published)
			if terms.EffectiveDate.Valid {
				title = `<a href="/terms-of-service/` + terms.EffectiveDate.Time.Format("2006-01-02") + `">` + title + `</a>`
			}
			body += `<li><div class="admin__terms-of-service__history__item"><h5>` + title + `</h5><div class="prose">` + serializer.MarkdownHTML(terms.Changelog) + `</div></div></li>`
		}
		body += `</ol>`
	}
	return authPageHTML(adminT(locale, "admin.terms_of_service.title", "Terms of Service"), notice, errorText, body, locale, theme)
}

func adminTermsOfServicePreviewHTML(terms models.TermsOfService, count int64, email, notice, errorText, locale, theme string) string {
	published := formatOptionalDate(locale, terms.PublishedAt.Time)
	countText := strconv.FormatInt(count, 10)
	body := `<div class="back-link"><a href="/admin/terms_of_service"><i class="fa fa-chevron-left fa-fw"></i>` + html.EscapeString(adminT(locale, "admin.terms_of_service.back", "Back to terms of service")) + `</a></div>` +
		`<p class="lead">` + adminTVars(locale, "admin.terms_of_service.preview.explanation_html", "The email will be sent to <strong>%{display_count} users</strong> who signed up before %{date}.", map[string]string{"display_count": countText, "date": published}) + `</p><div class="prose">` + serializer.MarkdownHTML(terms.Changelog) + `</div><hr class="spacer"><div class="content__heading__actions">` +
		simpleFormOpen("/admin/terms_of_service/"+strconv.FormatInt(terms.ID, 10)+"/test", "post") + `<button class="button button-secondary" type="submit">` + html.EscapeString(adminTVars(locale, "admin.terms_of_service.preview.send_preview", "Send preview to %{email}", map[string]string{"email": email})) + `</button>` + simpleFormClose() +
		simpleFormOpen("/admin/terms_of_service/"+strconv.FormatInt(terms.ID, 10)+"/distribution", "post") + `<button class="button" type="submit" data-confirm="` + html.EscapeString(adminT(locale, "admin.reports.are_you_sure", "Are you sure?")) + `">` + html.EscapeString(adminTermsSendToAllText(locale, count)) + `</button>` + simpleFormClose() + `</div>`
	return authPageHTML(adminT(locale, "admin.terms_of_service.preview.title", "Preview terms of service notification"), notice, errorText, body, locale, theme)
}

func adminTermsSendToAllText(locale string, count int64) string {
	key := "other"
	if count == 1 {
		key = "one"
	}
	return adminTVars(locale, "admin.terms_of_service.preview.send_to_all."+key, "Send %{display_count} emails", map[string]string{"display_count": strconv.FormatInt(count, 10)})
}

func termsOfServiceUTCDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}
