package api

import (
	"database/sql"
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

func (s *Server) settingsFeaturedTagsPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	tags, err := s.findFeaturedTags(account.ID)
	if err != nil {
		return err
	}
	suggestions, err := s.featuredTagSettingSuggestions(account.ID, tags)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	navigation, err := s.settingsNavigationForUser(c.Request().URL.Path, locale, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, featuredTagsSettingsHTML(tags, suggestions, c.QueryParam("error"), locale, theme, navigation))
}

func (s *Server) createSettingsFeaturedTag(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	name, err := settingsFeaturedTagName(c)
	if errors.Is(err, errSettingsFeaturedTagParamsMissing) {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err != nil {
		return err
	}
	featured, _, err := s.createFeaturedTagForAccount(account, name, false)
	if err != nil {
		locale := s.webLocale(c, user)
		theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
		tags, tagsErr := s.findFeaturedTags(account.ID)
		if tagsErr != nil {
			return tagsErr
		}
		suggestions, suggestionsErr := s.featuredTagSettingSuggestions(account.ID, tags)
		if suggestionsErr != nil {
			return suggestionsErr
		}
		navigation, navigationErr := s.settingsNavigationForUser(c.Request().URL.Path, locale, user, account)
		if navigationErr != nil {
			return navigationErr
		}
		return c.HTML(http.StatusOK, featuredTagsSettingsHTML(tags, suggestions, featuredTagErrorText(locale, err), locale, theme, navigation))
	}
	_ = s.deliverActivityPubAccountRawDistribution(featured.Account, activityPubAddFeaturedTag(s, *featured))
	return c.Redirect(http.StatusFound, "/settings/featured_tags")
}

var errSettingsFeaturedTagParamsMissing = errors.New("settings featured tag root parameter is missing")

func settingsFeaturedTagName(c *echo.Context) (string, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return "", err
	}
	const prefix = "featured_tag"
	if !formHasNestedPrefix(req.Form, prefix) {
		return "", errSettingsFeaturedTagParamsMissing
	}
	return strings.TrimSpace(lastFormValue(req.Form, prefix+"[name]")), nil
}

func (s *Server) destroySettingsFeaturedTag(c *echo.Context) error {
	account, _, _, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	if strings.EqualFold(c.FormValue("_method"), "delete") || c.Request().Method == http.MethodDelete {
		var featured models.FeaturedTag
		err := s.db.Preload("Account.AccountStat").Preload("Account.User.Role").Preload("Tag").Where("id = ? AND account_id = ?", c.Param("id"), account.ID).First(&featured).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apiError(c, http.StatusNotFound, "Record not found")
			}
			return err
		}
		if err := s.db.Delete(&featured).Error; err != nil {
			return err
		}
		_ = s.deliverActivityPubAccountRawDistribution(featured.Account, activityPubRemoveFeaturedTag(s, featured))
	}
	return c.Redirect(http.StatusFound, "/settings/featured_tags")
}

func (s *Server) createFeaturedTagForAccount(account *models.Account, name string, force bool) (*models.FeaturedTag, bool, error) {
	normalized, display, ok := normalizeTagName(name)
	if !ok {
		return nil, false, errFeaturedTagInvalidName
	}
	tag, err := s.findOrCreateTag(firstNonEmpty(display, normalized))
	if err != nil {
		return nil, false, err
	}
	var featured models.FeaturedTag
	created := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if account.Local() {
			var count int64
			if err := tx.Model(&models.FeaturedTag{}).Where("account_id = ?", account.ID).Count(&count).Error; err != nil {
				return err
			}
			if count >= featuredTagLimit {
				return errFeaturedTagLimit
			}
		}
		err := tx.Preload("Account.AccountStat").Preload("Account.User.Role").Preload("Tag").Where("account_id = ? AND tag_id = ?", account.ID, tag.ID).First(&featured).Error
		if err == nil {
			return errFeaturedTagDuplicate
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		stats, err := featuredStats(tx, account.ID, tag.ID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		featured = models.FeaturedTag{
			AccountID:     account.ID,
			TagID:         tag.ID,
			StatusesCount: stats.StatusesCount,
			LastStatusAt:  stats.LastStatusAt,
			Name:          sql.NullString{String: display, Valid: display != ""},
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := tx.Create(&featured).Error; err != nil {
			if force && isUniqueConstraintError(err) {
				return tx.Preload("Account.AccountStat").Preload("Account.User.Role").Preload("Tag").Where("account_id = ? AND tag_id = ?", account.ID, tag.ID).First(&featured).Error
			}
			return err
		}
		created = true
		return tx.Preload("Account.AccountStat").Preload("Account.User.Role").Preload("Tag").First(&featured, "id = ?", featured.ID).Error
	}); err != nil {
		return nil, false, err
	}
	return &featured, created, nil
}

func (s *Server) featuredTagSettingSuggestions(accountID int64, featured []models.FeaturedTag) ([]models.Tag, error) {
	if s.db == nil {
		return []models.Tag{}, nil
	}
	excluded := make([]int64, 0, len(featured))
	for _, item := range featured {
		excluded = append(excluded, item.TagID)
	}
	query := s.featuredTagSuggestionQuery(accountID)
	if len(excluded) > 0 {
		query = query.Where("tags.id NOT IN ?", excluded)
	}
	var tags []models.Tag
	err := query.Find(&tags).Error
	return tags, err
}

func featuredTagsSettingsHTML(tags []models.FeaturedTag, suggestions []models.Tag, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArg(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	var suggestionLinks strings.Builder
	for i, tag := range suggestions {
		if i > 0 {
			suggestionLinks.WriteString(`, `)
		}
		displayName := tagDisplayName(tag)
		query := url.Values{"featured_tag[name]": []string{tag.Name}}
		suggestionLinks.WriteString(`<a rel="nofollow" data-method="post" href="/settings/featured_tags?` + html.EscapeString(query.Encode()) + `">#` + html.EscapeString(displayName) + `</a>`)
	}
	suggestionHint := ""
	if suggestionLinks.Len() > 0 {
		suggestionHint = " " + suggestionLinks.String()
	}
	var rows strings.Builder
	for _, tag := range tags {
		name := featuredTagDisplayName(tag)
		lastStatus := html.EscapeString(settingsT(loc, "accounts.nothing_here", "Nothing here"))
		if tag.LastStatusAt.Valid {
			stamp := tag.LastStatusAt.Time.UTC().Format(time.RFC3339)
			title := tag.LastStatusAt.Time.UTC().Format("2006-01-02 15:04 UTC")
			lastStatus = `<time class="formatted" datetime="` + html.EscapeString(stamp) + `" title="` + html.EscapeString(title) + `">` + html.EscapeString(title) + `</time>`
		}
		rows.WriteString(`<div class="directory__tag"><div><h4><i class="fa fa-hashtag fa-fw"></i> ` + html.EscapeString(name) + `<small>` + lastStatus + ` <a class="table-action-link" data-method="delete" data-confirm="` + html.EscapeString(settingsT(loc, "admin.accounts.are_you_sure", "Are you sure?")) + `" href="/settings/featured_tags/` + strconv.FormatInt(tag.ID, 10) + `"><i class="fa fa-trash fa-fw"></i> ` + html.EscapeString(settingsT(loc, "filters.index.delete", "Delete")) + `</a></small></h4><div class="trends__item__current">` + html.EscapeString(strconv.FormatInt(tag.StatusesCount, 10)) + `</div></div></div>`)
	}
	flash := ""
	if strings.TrimSpace(errorText) != "" {
		flash = settingsFlashHTML("", errorText)
	}
	body := flash + `
	    <form class="simple_form new_featured_tag" id="new_featured_tag" novalidate="novalidate" method="post" action="/settings/featured_tags">
      <p class="lead">` + webT(loc, "featured_tags.hint_html") + `</p>
      <div class="fields-group">
	        <div class="input with_block_label string required featured_tag_name field_with_hint">
	          <label class="string required" for="featured_tag_name">` + html.EscapeString(settingsT(loc, "simple_form.labels.featured_tag.name", settingsT(loc, "featured_tags.name", "Name"))) + filterRequiredMarker(loc) + `</label>
	          <span class="hint">` + html.EscapeString(settingsT(loc, "simple_form.hints.featured_tag.name", "You can feature hashtags that you have used recently.")) + suggestionHint + `</span>
	          <div class="label_input"><input class="string required" type="text" name="featured_tag[name]" id="featured_tag_name"></div>
	        </div>
	      </div>
	      <div class="actions"><button name="button" type="submit" class="btn">` + html.EscapeString(settingsT(loc, "featured_tags.add_new", "Add featured tag")) + `</button></div>
    </form>
    <hr class="spacer">
    ` + rows.String()
	return settingsPageShellWithHeadingTitle(settingsT(loc, "settings.featured_tags", "Featured hashtags"), settingsT(loc, "settings.profile", "Profile"), settingsNavigationArg(localeAndTheme, loc), body, loc, theme, settingsProfileTabsHTML("featured_tags", loc), "")
}

func featuredTagDisplayName(tag models.FeaturedTag) string {
	return tag.DisplayNameValue()
}

func tagDisplayName(tag models.Tag) string {
	return tag.DisplayNameValue()
}

type featuredTagInputError string

func (e featuredTagInputError) Error() string { return string(e) }

const (
	errFeaturedTagInvalidName featuredTagInputError = "Featured tag name is invalid"
	errFeaturedTagLimit       featuredTagInputError = "Featured tag limit reached"
	errFeaturedTagDuplicate   featuredTagInputError = "Featured tag has already been taken"
)

func featuredTagErrorText(locale string, err error) string {
	switch err {
	case errFeaturedTagInvalidName:
		return settingsT(locale, "featured_tags.errors.invalid_name", "Featured tag name is invalid")
	case errFeaturedTagLimit:
		return settingsT(locale, "featured_tags.errors.limit", "You have already featured the maximum number of hashtags")
	case errFeaturedTagDuplicate:
		return settingsT(locale, "featured_tags.errors.taken", "Featured tag has already been taken")
	default:
		return err.Error()
	}
}
