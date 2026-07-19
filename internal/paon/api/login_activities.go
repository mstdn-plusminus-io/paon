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
)

const settingsLoginActivitiesPageSize = adminRailsDefaultPageSize

var loginActivityAuthenticationMethods = map[string]struct{}{
	"password":      {},
	"otp":           {},
	"webauthn":      {},
	"sign_in_token": {},
	"omniauth":      {},
}

func (s *Server) settingsLoginActivitiesPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	activities, err := s.userLoginActivities(user.ID, c)
	if err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, loginActivitiesHTML(activities, adminTrendsPageValue(c), renderArgs...))
}

func (s *Server) userLoginActivities(userID int64, c *echo.Context) ([]models.LoginActivity, error) {
	if s.db == nil {
		return []models.LoginActivity{}, nil
	}
	var activities []models.LoginActivity
	err := s.db.
		Select("id, user_id, authentication_method, provider, success, failure_reason, ip::text AS ip, user_agent, created_at").
		Where("user_id = ?", userID).
		Order("id DESC").
		Offset(adminPageOffset(c, settingsLoginActivitiesPageSize)).
		Limit(settingsLoginActivitiesPageSize).
		Find(&activities).Error
	return activities, err
}

func (s *Server) recordLoginActivity(userID int64, method string, success bool, failureReason string, ip string, userAgent string) error {
	if s.db == nil {
		return nil
	}
	method = strings.TrimSpace(method)
	if !validLoginActivityAuthenticationMethod(method) {
		return errors.New("login activity authentication_method is invalid")
	}
	activity := models.LoginActivity{
		UserID:               userID,
		AuthenticationMethod: sql.NullString{String: method, Valid: true},
		Success:              sql.NullBool{Bool: success, Valid: true},
		FailureReason:        sql.NullString{String: failureReason, Valid: failureReason != ""},
		IP:                   sql.NullString{String: ip, Valid: ip != ""},
		UserAgent:            sql.NullString{String: userAgent, Valid: userAgent != ""},
		CreatedAt:            sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}
	return s.db.Create(&activity).Error
}

func validLoginActivityAuthenticationMethod(method string) bool {
	_, ok := loginActivityAuthenticationMethods[method]
	return ok
}

func loginActivitiesHTML(activities []models.LoginActivity, page string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	var rows strings.Builder
	for _, activity := range activities {
		icon := "times"
		statusClass := "failure"
		messageKey := "login_activities.failed_sign_in_html"
		messageFallback := "Failed sign-in attempt with %{method} from %{ip} (%{browser})"
		if activity.Success.Valid && activity.Success.Bool {
			icon = "check"
			statusClass = "success"
			messageKey = "login_activities.successful_sign_in_html"
			messageFallback = "Successful sign-in with %{method} from %{ip} (%{browser})"
		}
		method := nullStringValue(activity.AuthenticationMethod, settingsT(loc, "sessions.browsers.generic", "unknown"))
		if activity.Provider.Valid && activity.Provider.String != "" {
			method = settingsT(loc, "auth.providers."+activity.Provider.String, activity.Provider.String)
		} else if activity.AuthenticationMethod.Valid && activity.AuthenticationMethod.String != "" {
			method = settingsT(loc, "login_activities.authentication_methods."+activity.AuthenticationMethod.String, activity.AuthenticationMethod.String)
		}
		ip := nullStringValue(activity.IP, "")
		userAgent := nullStringValue(activity.UserAgent, "")
		browser := localizedUserAgentDescription(userAgent, loc)
		timestamp := ""
		timestampText := ""
		if activity.CreatedAt.Valid {
			timestamp = activity.CreatedAt.Time.UTC().Format(time.RFC3339)
			timestampText = formatRailsLocalizedTime(loc, activity.CreatedAt.Time)
		}
		methodHTML := `<span class="target">` + html.EscapeString(method) + `</span>`
		ipHTML := `<span class="target">` + html.EscapeString(ip) + `</span>`
		browserHTML := `<span class="target" title="` + html.EscapeString(userAgent) + `">` + html.EscapeString(browser) + `</span>`
		title := settingsTVars(loc, messageKey, messageFallback, map[string]string{
			"method":  methodHTML,
			"ip":      ipHTML,
			"browser": browserHTML,
		})
		rows.WriteString(`<div class="log-entry"><div class="log-entry__header"><div class="log-entry__avatar"><div class="indicator-icon ` + statusClass + `"><i class="fa fa-` + icon + ` fa-fw"></i></div></div><div class="log-entry__content"><div class="log-entry__title">` + title + `</div><div class="log-entry__timestamp"><time class="formatted" datetime="` + html.EscapeString(timestamp) + `">` + html.EscapeString(timestampText) + `</time></div></div></div></div>`)
	}
	activityHTML := ""
	if rows.Len() == 0 {
		activityHTML = `<div class="muted-hint center-text">` + html.EscapeString(settingsT(loc, "login_activities.empty", "No authentication activity")) + `</div>`
	} else {
		activityHTML = `<div class="announcements-list">` + rows.String() + `</div>`
	}
	title := settingsT(loc, "login_activities.title", "Authentication history")
	body := `<p>` + settingsT(loc, "login_activities.description_html", "Review recent successful and failed sign-in attempts for your account.") + `</p>
    <hr class="spacer">
    ` + activityHTML + loginActivitiesPaginationHTML(page, len(activities) == settingsLoginActivitiesPageSize, loc)
	return accountSecurityPageHTML(title, "login_activities", "", "", body, localeAndTheme...)
}

func loginActivitiesPaginationHTML(page string, hasNext bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	pageNum, err := strconv.Atoi(strings.TrimSpace(page))
	if err != nil || pageNum < 1 {
		pageNum = 1
	}
	var links []string
	if pageNum > 1 {
		params := url.Values{"page": []string{strconv.Itoa(pageNum - 1)}}
		links = append(links, `<a href="/settings/login_activities?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.prev", "Previous"))+`</a>`)
	}
	if hasNext {
		params := url.Values{"page": []string{strconv.Itoa(pageNum + 1)}}
		links = append(links, `<a href="/settings/login_activities?`+html.EscapeString(params.Encode())+`">`+html.EscapeString(settingsT(loc, "pagination.next", "Next"))+`</a>`)
	}
	if len(links) == 0 {
		return ""
	}
	return `<nav class="pagination">` + strings.Join(links, " ") + `</nav>`
}

func nullStringValue(value sql.NullString, fallback string) string {
	if value.Valid {
		return value.String
	}
	return fallback
}

type userAgentDetection struct {
	BrowserID  string
	Browser    string
	PlatformID string
	Platform   string
}

func userAgentDescription(value string) string {
	return localizedUserAgentDescription(value, "en")
}

func localizedUserAgentDescription(value string, locale string) string {
	detection := detectUserAgent(value)
	browser := settingsT(locale, "sessions.browsers."+detection.BrowserID, detection.Browser)
	platform := settingsT(locale, "sessions.platforms."+detection.PlatformID, detection.Platform)
	return settingsTVars(locale, "sessions.description", "%{browser} on %{platform}", map[string]string{
		"browser":  browser,
		"platform": platform,
	})
}

func detectUserAgent(value string) userAgentDetection {
	ua := strings.TrimSpace(value)
	lower := strings.ToLower(ua)
	browserID := detectBrowserID(lower)
	platformID := detectPlatformID(lower)
	return userAgentDetection{
		BrowserID:  browserID,
		Browser:    browserLabel(browserID),
		PlatformID: platformID,
		Platform:   platformLabel(platformID),
	}
}

func detectBrowserID(lower string) string {
	switch {
	case lower == "":
		return "unknown_browser"
	case strings.Contains(lower, "micromessenger"):
		return "micro_messenger"
	case strings.Contains(lower, "huawei"):
		return "huawei_browser"
	case strings.Contains(lower, "ucbrowser") || strings.Contains(lower, "uc browser"):
		return "uc_browser"
	case strings.Contains(lower, "qqbrowser"):
		return "qq"
	case strings.Contains(lower, "weibo"):
		return "weibo"
	case strings.Contains(lower, "alipay"):
		return "alipay"
	case strings.Contains(lower, "blackberry"):
		return "blackberry"
	case strings.Contains(lower, "nokia"):
		return "nokia"
	case strings.Contains(lower, "phantomjs"):
		return "phantom_js"
	case strings.Contains(lower, "otter"):
		return "otter"
	case strings.Contains(lower, " electron/"):
		return "electron"
	case strings.Contains(lower, "edg/") || strings.Contains(lower, "edge/") || strings.Contains(lower, "edgios/") || strings.Contains(lower, "edga/"):
		return "edge"
	case strings.Contains(lower, "opr/") || strings.Contains(lower, "opera"):
		return "opera"
	case strings.Contains(lower, "firefox/") || strings.Contains(lower, "fxios/"):
		return "firefox"
	case strings.Contains(lower, "chrome/") || strings.Contains(lower, "crios/") || strings.Contains(lower, "chromium/"):
		return "chrome"
	case (strings.Contains(lower, "safari/") || strings.Contains(lower, "applewebkit/")) && !strings.Contains(lower, "chrome/") && !strings.Contains(lower, "crios/"):
		return "safari"
	case strings.Contains(lower, "msie ") || strings.Contains(lower, "trident/"):
		return "ie"
	default:
		return "generic"
	}
}

func detectPlatformID(lower string) string {
	switch {
	case lower == "":
		return "unknown_platform"
	case strings.Contains(lower, "windows phone"):
		return "windows_phone"
	case strings.Contains(lower, "windows mobile"):
		return "windows_mobile"
	case strings.Contains(lower, "windows"):
		return "windows"
	case strings.Contains(lower, "cros"):
		return "chrome_os"
	case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad") || strings.Contains(lower, "ipod") || strings.Contains(lower, "cpu os"):
		return "ios"
	case strings.Contains(lower, "android"):
		return "android"
	case strings.Contains(lower, "mac os x") || strings.Contains(lower, "macintosh"):
		return "mac"
	case strings.Contains(lower, "kaios"):
		return "kai_os"
	case strings.Contains(lower, "firefox os"):
		return "firefox_os"
	case strings.Contains(lower, "blackberry"):
		return "blackberry"
	case strings.Contains(lower, "linux") || strings.Contains(lower, "x11"):
		return "linux"
	case strings.Contains(lower, "adobeair") || strings.Contains(lower, "adobe air"):
		return "adobe_air"
	default:
		return "unknown_platform"
	}
}

func browserLabel(id string) string {
	switch id {
	case "alipay":
		return "Alipay"
	case "blackberry":
		return "BlackBerry"
	case "chrome":
		return "Chrome"
	case "edge":
		return "Microsoft Edge"
	case "electron":
		return "Electron"
	case "firefox":
		return "Firefox"
	case "huawei_browser":
		return "Huawei Browser"
	case "ie":
		return "Internet Explorer"
	case "micro_messenger":
		return "MicroMessenger"
	case "nokia":
		return "Nokia S40 Ovi Browser"
	case "opera":
		return "Opera"
	case "otter":
		return "Otter"
	case "phantom_js":
		return "PhantomJS"
	case "qq":
		return "QQ Browser"
	case "safari":
		return "Safari"
	case "uc_browser":
		return "UC Browser"
	case "weibo":
		return "Weibo"
	case "generic":
		return "Unknown browser"
	default:
		return "Unknown Browser"
	}
}

func platformLabel(id string) string {
	switch id {
	case "adobe_air":
		return "Adobe Air"
	case "android":
		return "Android"
	case "blackberry":
		return "BlackBerry"
	case "chrome_os":
		return "ChromeOS"
	case "firefox_os":
		return "Firefox OS"
	case "ios":
		return "iOS"
	case "kai_os":
		return "KaiOS"
	case "linux":
		return "Linux"
	case "mac":
		return "macOS"
	case "windows":
		return "Windows"
	case "windows_mobile":
		return "Windows Mobile"
	case "windows_phone":
		return "Windows Phone"
	default:
		return "Unknown Platform"
	}
}
