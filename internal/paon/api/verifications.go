package api

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

var profileVerificationHTTPClient *http.Client

var (
	verificationHTMLTagPattern        = regexp.MustCompile(`(?is)<\s*(a|link)\b[^>]*>`)
	verificationHTMLAttrPattern       = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*("[^"]*"|'[^']*'|[^\s"'>]+)`)
	remoteProfileFieldAnchorPattern   = regexp.MustCompile(`(?is)^\s*<\s*a\b([^>]*)>(.*?)</\s*a\s*>\s*$`)
	remoteProfileFieldNestedTagPatten = regexp.MustCompile(`(?is)<[^>]+>`)
)

type verificationField struct {
	Name       string  `json:"name"`
	Value      string  `json:"value"`
	VerifiedAt *string `json:"verified_at"`
}

func (s *Server) settingsVerificationPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	locale := s.webLocale(c, user)
	theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
	renderArgs, err := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, verificationSettingsHTML(s, *account, renderArgs...))
}

func (s *Server) updateSettingsVerification(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	domains, err := localAttributionDomains(c.FormValue("account[attribution_domains_as_text]"))
	if err != nil {
		locale := s.webLocale(c, user)
		theme := settingsWebTheme(decodeUserSettings(user.Settings.String))
		renderArgs, renderErr := s.settingsRenderArgs(c.Request().URL.Path, locale, theme, user, account)
		if renderErr != nil {
			return renderErr
		}
		return c.HTML(http.StatusOK, verificationSettingsHTMLWithMessages(s, *account, "", err.Error(), renderArgs...))
	}
	now := time.Now().UTC()
	if err := s.db.Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{
		"attribution_domains": models.StringArray(domains),
		"updated_at":          now,
	}).Error; err != nil {
		return err
	}
	account.AttributionDomains = models.StringArray(domains)
	_ = s.enqueueActivityPubAccountUpdate(*account, activityPubAccountUpdateDebounceDelay)
	locale := s.webLocale(c, user)
	return c.Redirect(http.StatusFound, "/settings/verification?notice="+url.QueryEscape(settingsChangeSavedMessage(locale)))
}

func localAttributionDomains(raw string) ([]string, error) {
	items := strings.Fields(raw)
	if len(items) > 100 {
		return nil, fmt.Errorf("Validation failed: Attribution domains is too long")
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		domain := normalizeActivityAttributionDomain(item)
		if domain == "" {
			return nil, fmt.Errorf("Validation failed: Attribution domain is invalid")
		}
		values = append(values, domain)
	}
	return values, nil
}

func verifiedVerificationFields(raw []byte) []verificationField {
	if len(raw) == 0 {
		return []verificationField{}
	}
	var decoded []verificationField
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return []verificationField{}
	}
	fields := make([]verificationField, 0, len(decoded))
	for _, field := range decoded {
		if field.VerifiedAt == nil || strings.TrimSpace(*field.VerifiedAt) == "" {
			continue
		}
		field.Value = strings.TrimSpace(field.Value)
		if field.Value == "" {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

func verificationSettingsHTML(s *Server, account models.Account, localeAndTheme ...string) string {
	return verificationSettingsHTMLWithMessages(s, account, "", "", localeAndTheme...)
}

func verificationSettingsHTMLWithMessages(s *Server, account models.Account, notice string, errorText string, localeAndTheme ...string) string {
	loc := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	applicationName := settingsApplicationNameArg(localeAndTheme)
	profileURL := s.cfg.BaseURL() + accountWebPath(account)
	relMe := `<a href="` + html.EscapeString(profileURL) + `" rel="me">` + html.EscapeString(applicationName) + `</a>`
	var verified strings.Builder
	fields := verifiedVerificationFields(account.Fields)
	if len(fields) > 0 {
		verified.WriteString(`<h4>` + html.EscapeString(settingsT(loc, "verification.verified_links", "Verified links")) + `</h4><ul class="lead">`)
		for _, field := range fields {
			verified.WriteString(`<li><span class="verified-badge"><i class="fa fa-check verified-badge__mark"></i><span>`)
			verified.WriteString(html.EscapeString(field.Value))
			verified.WriteString(`</span></span></li>`)
		}
		verified.WriteString(`</ul>`)
	}

	hint := strings.ReplaceAll(settingsT(loc, "verification.hint_html", "You can verify ownership of links in your profile metadata by adding a rel=\"me\" link back to your profile."), "Mastodon", applicationName)
	extraInstructions := strings.ReplaceAll(settingsT(loc, "verification.extra_instructions_html", "Verified links are read from existing Mastodon profile fields that already have a verification timestamp."), "Mastodon", applicationName)
	displayName := strings.TrimSpace(account.DisplayName)
	if displayName == "" {
		displayName = account.Username
	}
	creatorHandle := "@" + account.Username + "@" + firstNonEmpty(s.cfg.LocalDomain, s.cfg.WebDomain)
	creatorMeta := `<meta name="fediverse:creator" content="` + html.EscapeString(creatorHandle) + `">`
	body := settingsInlineFlashHTML(notice, errorText) + `<section class="simple_form form-section">
    <h3>` + html.EscapeString(settingsT(loc, "verification.website_verification", "Website verification")) + `</h3>
    <p class="lead">` + hint + `</p>
    <h4>` + html.EscapeString(settingsT(loc, "verification.here_is_how", "Here is how")) + `</h4>
    <p class="lead">` + settingsT(loc, "verification.instructions_html", "Copy this link and place it on the page you want to verify. Then add that page to one of your profile fields.") + `</p>
    <div class="input-copy lead">
      <div class="input-copy__wrapper"><input type="text" maxlength="999" spellcheck="false" readonly value="` + html.EscapeString(relMe) + `"></div><button type="button">` + html.EscapeString(settingsT(loc, "generic.copy", "Copy")) + `</button>
    </div>
	<p class="lead">` + extraInstructions + `</p>
	` + verified.String() + `</section>`
	body += `<form class="simple_form edit_account form-section" id="edit_account" method="post" action="/settings/verification"><input type="hidden" name="_method" value="put">
    <h3>` + html.EscapeString(settingsT(loc, "author_attribution.title", "Author attribution")) + `</h3>
    <p class="lead">` + settingsT(loc, "author_attribution.hint_html", "If you write articles outside this server, you can control how you are credited when they are shared here.") + `</p>
    <div class="fields-group fade-out-top"><div>
      <div class="status-card expanded bottomless"><div class="status-card__content">
        <span class="status-card__host">` + html.EscapeString(settingsTVars(loc, "author_attribution.s_blog", "%{name}'s Blog", map[string]string{"name": displayName})) + `</span>
        <strong class="status-card__title">` + html.EscapeString(settingsT(loc, "author_attribution.example_title", "Sample text")) + `</strong>
      </div></div>
      <div class="more-from-author">` + settingsTVars(loc, "author_attribution.more_from_html", "More from %{name}", map[string]string{"name": `<a href="` + html.EscapeString(profileURL) + `">` + html.EscapeString(displayName) + `</a>`}) + `</div>
    </div></div>
    <h4>` + html.EscapeString(settingsT(loc, "verification.here_is_how", "Here is how")) + `</h4>
    <p class="lead">` + html.EscapeString(settingsT(loc, "author_attribution.instructions", "Make sure this code is in your article's HTML:")) + `</p>
    <div class="input-copy lead"><div class="input-copy__wrapper"><input type="text" maxlength="999" spellcheck="false" readonly value="` + html.EscapeString(creatorMeta) + `"></div><button type="button">` + html.EscapeString(settingsT(loc, "generic.copy", "Copy")) + `</button></div>
    <p class="lead">` + html.EscapeString(settingsT(loc, "author_attribution.then_instructions", "Then add the publication's domain name below.")) + `</p>`
	body += settingsBlockTextarea(
		settingsT(loc, "simple_form.labels.defaults.attribution_domains_as_text", "Websites allowed to credit you"),
		settingsT(loc, "simple_form.hints.defaults.attribution_domains_as_text", "One per line. Protects from false attributions."),
		"account[attribution_domains_as_text]",
		strings.Join(account.AttributionDomains, "\n"),
		`rows="4" placeholder="example1.com&#10;example2.com&#10;example3.com" autocapitalize="none" autocorrect="off" spellcheck="false"`,
	)
	body += settingsSubmitButton(settingsT(loc, "generic.save_changes", "Save changes")) + `</form>`
	return settingsPageShellWithHeadingTitle(settingsT(loc, "verification.verification", "Profile verification"), settingsT(loc, "settings.profile", "Profile"), settingsNavigationArg(localeAndTheme, loc), body, loc, theme, settingsProfileTabsHTML("verification", loc), "")
}

func verifyProfileFieldLinksBestEffort(fields []profileField, profileURL string, now time.Time) []profileField {
	for i := range fields {
		if fields[i].VerifiedAt != nil {
			continue
		}
		fieldURL, ok := profileFieldVerificationURL(fields[i].Value)
		if !ok {
			continue
		}
		if profileLinkVerified(fieldURL, profileURL) {
			verifiedAt := now.UTC().Format(time.RFC3339)
			fields[i].VerifiedAt = &verifiedAt
		}
	}
	return fields
}

func verifyRemoteProfileFieldLinksBestEffort(fields []profileField, profileURL string, now time.Time) []profileField {
	for i := range fields {
		if fields[i].VerifiedAt != nil {
			continue
		}
		fieldURL, ok := remoteProfileFieldVerificationURL(fields[i].Value)
		if !ok {
			continue
		}
		if profileLinkVerified(fieldURL, profileURL) {
			verifiedAt := now.UTC().Format(time.RFC3339)
			fields[i].VerifiedAt = &verifiedAt
		}
	}
	return fields
}

func (s *Server) verifyActivityPubActorProfileFieldsBestEffort(account models.Account, fields []profileField, now time.Time) []profileField {
	if account.Local() || account.SuspendedAt.Valid || len(fields) == 0 {
		return fields
	}
	profileURL := strings.TrimSpace(account.URL.String)
	if profileURL == "" {
		profileURL = strings.TrimSpace(account.URI)
	}
	if profileURL == "" {
		return fields
	}
	return verifyRemoteProfileFieldLinksBestEffort(fields, profileURL, now.UTC())
}

func profileFieldVerificationURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	if parsed.Host != strings.ToLower(parsed.Host) || !activityFetchHostAllowed(parsed.Hostname()) {
		return "", false
	}
	if !profileFieldPathMatchesRailsNormalizedPath(parsed) {
		return "", false
	}
	return parsed.String(), true
}

func profileFieldPathMatchesRailsNormalizedPath(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	rawPath := parsed.EscapedPath()
	if rawPath == "" {
		return true
	}
	if rawPath != railsNormalizedProfileFieldEscapedPath(rawPath) {
		return false
	}
	for _, segment := range strings.Split(rawPath, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func railsNormalizedProfileFieldEscapedPath(rawPath string) string {
	var b strings.Builder
	for i := 0; i < len(rawPath); i++ {
		if rawPath[i] != '%' || i+2 >= len(rawPath) || !isHexDigit(rawPath[i+1]) || !isHexDigit(rawPath[i+2]) {
			b.WriteByte(rawPath[i])
			continue
		}
		decoded := fromHex(rawPath[i+1])<<4 | fromHex(rawPath[i+2])
		if isUnreservedURIByte(decoded) {
			b.WriteByte(decoded)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperHex(rawPath[i+1]))
			b.WriteByte(upperHex(rawPath[i+2]))
		}
		i += 2
	}
	return b.String()
}

func isHexDigit(ch byte) bool {
	return ('0' <= ch && ch <= '9') || ('a' <= ch && ch <= 'f') || ('A' <= ch && ch <= 'F')
}

func fromHex(ch byte) byte {
	switch {
	case '0' <= ch && ch <= '9':
		return ch - '0'
	case 'a' <= ch && ch <= 'f':
		return ch - 'a' + 10
	case 'A' <= ch && ch <= 'F':
		return ch - 'A' + 10
	default:
		return 0
	}
}

func upperHex(ch byte) byte {
	if 'a' <= ch && ch <= 'f' {
		return ch - 'a' + 'A'
	}
	return ch
}

func isUnreservedURIByte(ch byte) bool {
	return ('a' <= ch && ch <= 'z') ||
		('A' <= ch && ch <= 'Z') ||
		('0' <= ch && ch <= '9') ||
		ch == '-' || ch == '.' || ch == '_' || ch == '~'
}

func remoteProfileFieldVerificationURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	match := remoteProfileFieldAnchorPattern.FindStringSubmatch(value)
	if len(match) != 3 || remoteProfileFieldNestedTagPatten.MatchString(match[2]) {
		return "", false
	}
	attrs := verificationTagAttrs("<a " + match[1] + ">")
	href := strings.TrimSpace(attrs["href"])
	label := strings.TrimSpace(html.UnescapeString(match[2]))
	if href == "" || href != label {
		return "", false
	}
	return profileFieldVerificationURL(href)
}

func profileLinkVerified(fieldURL string, profileURL string) bool {
	body, ok := fetchProfileVerificationHTML(fieldURL)
	if !ok {
		return false
	}
	hrefs := verificationRelMeHrefs(body)
	for _, href := range hrefs {
		if strings.EqualFold(href, profileURL) {
			return true
		}
	}
	return len(hrefs) > 0 && verificationHeadRedirectsTo(hrefs[0], profileURL)
}

func fetchProfileVerificationHTML(rawURL string) (string, bool) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "text/html")
	client := profileVerificationClient(true)
	if client == nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := readRailsTruncatedResponseBody(resp, maxActivityResourceBodySize)
	if err != nil {
		return "", false
	}
	return string(body), true
}

func readRailsTruncatedResponseBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("response body is empty")
	}
	if limit <= 0 {
		return []byte{}, nil
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit+1))
}

func verificationRelMeHrefs(body string) []string {
	matches := verificationHTMLTagPattern.FindAllString(body, -1)
	hrefs := make([]string, 0, len(matches))
	for _, tag := range matches {
		attrs := verificationTagAttrs(tag)
		if !relContainsMe(attrs["rel"]) {
			continue
		}
		hrefs = append(hrefs, attrs["href"])
	}
	return hrefs
}

func verificationTagAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	for _, match := range verificationHTMLAttrPattern.FindAllStringSubmatch(tag, -1) {
		name := strings.ToLower(match[1])
		value := strings.Trim(match[2], `"'`)
		attrs[name] = html.UnescapeString(value)
	}
	return attrs
}

func relContainsMe(rel string) bool {
	for _, value := range strings.Fields(strings.ToLower(rel)) {
		if value == "me" {
			return true
		}
	}
	return false
}

func verificationHeadRedirectsTo(rawURL string, profileURL string) bool {
	if strings.TrimSpace(rawURL) == "" {
		return false
	}
	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
	if err != nil {
		return false
	}
	client := profileVerificationClient(false)
	if client == nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	location := resp.Header.Get("Location")
	return location != "" && location == profileURL
}

func profileVerificationClient(followRedirects bool) *http.Client {
	base := profileVerificationHTTPClient
	if base == nil {
		base = activityHTTPClient
	}
	if base == nil {
		return nil
	}
	client := *base
	if !followRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		return &client
	}
	if client.CheckRedirect == nil && base == activityHTTPClient {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			if !activityRedirectAllowed(req, via) {
				return fmt.Errorf("remote host is not allowed")
			}
			return nil
		}
	}
	return &client
}
