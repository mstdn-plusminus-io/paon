package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

var (
	errTranslationNotConfigured      = errors.New("translation service is not configured")
	errTranslationNotPermitted       = errors.New("translation is not permitted")
	errUnexpectedTranslationResponse = errors.New("unexpected translation service response")
	errTranslationQuotaExceeded      = errors.New("translation quota exceeded")
	errTranslationTooManyRequests    = errors.New("translation service rate limited")
)

const (
	translationLanguageCacheKey = "paon:translation_service:languages"
	translationLanguageCacheTTL = 7 * 24 * time.Hour
	statusTranslationCacheTTL   = 24 * time.Hour
)

var translationNoTranslateSpanPattern = regexp.MustCompile(`(?is)<span\s+translate\s*=\s*(?:"no"|'no'|no)\s*>(.*?)</span>`)
var translationHTTPClient *http.Client

type translationResult struct {
	Text                   string
	DetectedSourceLanguage string
	Provider               string
}

type statusTranslationSource struct {
	kind         string
	text         string
	mediaID      int64
	pollOptionID int
}

func translationHTTPDo(req *http.Request, allowLocal bool) (*http.Response, error) {
	client := translationHTTPClient
	if client == nil {
		client = activityHTTPClient
	}
	if client == nil {
		return nil, errUnexpectedTranslationResponse
	}
	if allowLocal {
		return client.Do(req)
	}
	if req.URL == nil || req.URL.Host == "" || !activityFetchHostAllowed(req.URL.Hostname()) {
		return nil, errUnexpectedTranslationResponse
	}
	checkRedirect := client.CheckRedirect
	limited := *client
	limited.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if checkRedirect != nil {
			if err := checkRedirect(req, via); err != nil {
				return err
			}
		}
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		if !activityRedirectAllowed(req, via) {
			return errUnexpectedTranslationResponse
		}
		return nil
	}
	return limited.Do(req)
}

func translationConfigured(cfg config.Config) bool {
	return translationProviderConfigured(cfg.DeepLAPIKey) || translationProviderConfigured(cfg.LibreTranslateEndpoint)
}

func translationProviderConfigured(value string) bool {
	return strings.TrimSpace(value) != ""
}

func (s *Server) translateStatusForUser(c *echo.Context, status models.Status, user models.User) (serializer.Translation, error) {
	if !translationConfigured(s.cfg) {
		return serializer.Translation{}, errTranslationNotConfigured
	}
	targetLanguage := translationTargetLanguage(c, user, s.cfg)
	sourceLanguage := statusTranslationSourceLanguage(status.Language)
	ctx, cancel := context.WithTimeout((*c).Request().Context(), 10*time.Second)
	defer cancel()

	languages, err := s.translationLanguageMapStrict(ctx)
	if err != nil {
		return serializer.Translation{}, err
	}
	if !translationTargetAllowed(languages, sourceLanguage, targetLanguage) {
		return serializer.Translation{}, errTranslationNotPermitted
	}

	sources := statusTranslationSources(s.cfg, status)
	cacheKey := statusTranslationCacheKey(sourceLanguage, targetLanguage, sources)
	if translation, ok := s.cachedStatusTranslation(ctx, cacheKey); ok {
		return translation, nil
	}
	texts := make([]string, 0, len(sources))
	for _, source := range sources {
		texts = append(texts, source.text)
	}
	results, err := translateTexts(ctx, s.cfg, texts, sourceLanguage, targetLanguage)
	if err != nil {
		return serializer.Translation{}, err
	}
	translation := buildStatusTranslation(status, targetLanguage, sources, results)
	s.cacheStatusTranslation(ctx, cacheKey, translation)
	return translation, nil
}

func translationAPIError(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, errTranslationNotConfigured), errors.Is(err, errTranslationNotPermitted):
		return apiError(c, http.StatusNotFound, "Record not found")
	case errors.Is(err, errTranslationQuotaExceeded):
		return apiError(c, http.StatusServiceUnavailable, "Translation quota exceeded")
	case errors.Is(err, errTranslationTooManyRequests):
		return apiError(c, http.StatusServiceUnavailable, "Translation service is rate limited")
	default:
		return apiError(c, http.StatusServiceUnavailable, "Translation service returned an unexpected response")
	}
}

func (s *Server) translationLanguageMapStrict(ctx context.Context) (map[string][]string, error) {
	if !translationConfigured(s.cfg) {
		return nil, errTranslationNotConfigured
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if languages, ok := s.cachedTranslationLanguageMap(ctx); ok {
		return languages, nil
	}
	languages, err := s.fetchTranslationLanguageMap(ctx)
	if err != nil {
		return nil, err
	}
	s.cacheTranslationLanguageMap(ctx, languages)
	return languages, nil
}

func (s *Server) fetchTranslationLanguageMap(ctx context.Context) (map[string][]string, error) {
	if translationProviderConfigured(s.cfg.DeepLAPIKey) {
		return deepLTranslationLanguages(ctx, s.cfg)
	}
	return libreTranslateLanguages(ctx, s.cfg)
}

func (s *Server) translationLanguageMap(ctx context.Context) map[string][]string {
	if !translationConfigured(s.cfg) {
		return map[string][]string{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	languages, err := s.translationLanguageMapStrict(ctx)
	if err != nil {
		return map[string][]string{}
	}
	return languages
}

func (s *Server) cachedTranslationLanguageMap(ctx context.Context) (map[string][]string, bool) {
	if s == nil {
		return nil, false
	}
	value, err := s.redisCommand(ctx, "GET", redisConfig(s.cfg).prefix+translationLanguageCacheKey)
	if err != nil {
		return nil, false
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return nil, false
	}
	languages, err := unmarshalTranslationLanguageMap(raw)
	return languages, err == nil
}

func (s *Server) cacheTranslationLanguageMap(ctx context.Context, languages map[string][]string) {
	if s == nil || len(languages) == 0 {
		return
	}
	raw, err := marshalTranslationLanguageMap(languages)
	if err != nil {
		return
	}
	_, _ = s.redisCommand(ctx, "SETEX", redisConfig(s.cfg).prefix+translationLanguageCacheKey, strconv.FormatInt(int64(translationLanguageCacheTTL/time.Second), 10), raw)
}

func marshalTranslationLanguageMap(languages map[string][]string) (string, error) {
	raw, err := json.Marshal(languages)
	return string(raw), err
}

func unmarshalTranslationLanguageMap(raw string) (map[string][]string, error) {
	languages := map[string][]string{}
	err := json.Unmarshal([]byte(raw), &languages)
	if err != nil {
		return nil, err
	}
	return languages, nil
}

func (s *Server) cachedStatusTranslation(ctx context.Context, key string) (serializer.Translation, bool) {
	if s == nil || key == "" {
		return serializer.Translation{}, false
	}
	value, err := s.redisCommand(ctx, "GET", redisConfig(s.cfg).prefix+key)
	if err != nil {
		return serializer.Translation{}, false
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return serializer.Translation{}, false
	}
	var translation serializer.Translation
	if err := json.Unmarshal([]byte(raw), &translation); err != nil {
		return serializer.Translation{}, false
	}
	return translation, true
}

func (s *Server) cacheStatusTranslation(ctx context.Context, key string, translation serializer.Translation) {
	if s == nil || key == "" {
		return
	}
	raw, err := json.Marshal(translation)
	if err != nil {
		return
	}
	_, _ = s.redisCommand(ctx, "SETEX", redisConfig(s.cfg).prefix+key, strconv.FormatInt(int64(statusTranslationCacheTTL/time.Second), 10), string(raw))
}

func statusTranslationCacheKey(sourceLanguage string, targetLanguage string, sources []statusTranslationSource) string {
	return "paon:v2:translations/" + sourceLanguage + "/" + targetLanguage + "/" + statusTranslationContentHash(sources)
}

func statusTranslationContentHash(sources []statusTranslationSource) string {
	raw := statusTranslationContentHashJSON(sources)
	sum := sha256.Sum256(raw)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func statusTranslationContentHashJSON(sources []statusTranslationSource) []byte {
	var out bytes.Buffer
	out.WriteByte('{')
	for index, source := range sources {
		if index > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(statusTranslationSourceCacheKey(index, source))
		value, _ := json.Marshal(source.text)
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes()
}

func statusTranslationSourceCacheKey(index int, source statusTranslationSource) string {
	switch source.kind {
	case "media_attachment":
		return "MediaAttachment-" + strconv.FormatInt(source.mediaID, 10)
	case "poll_option":
		return "Poll::Option-" + strconv.Itoa(source.pollOptionID)
	default:
		return source.kind
	}
}

func libreTranslateLanguages(ctx context.Context, cfg config.Config) (map[string][]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.LibreTranslateEndpoint+"/languages", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := translationHTTPDo(req, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errUnexpectedTranslationResponse
	}
	body, err := readActivityResponseBodyWithRailsLimit(resp, "translation-languages")
	if err != nil {
		return nil, errUnexpectedTranslationResponse
	}
	var payload []struct {
		Code    string   `json:"code"`
		Targets []string `json:"targets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return libreTranslateLanguageMap(payload), nil
}

func translateTexts(ctx context.Context, cfg config.Config, texts []string, sourceLanguage string, targetLanguage string) ([]translationResult, error) {
	if len(texts) == 0 {
		return []translationResult{}, nil
	}
	if translationProviderConfigured(cfg.DeepLAPIKey) {
		return translateTextsWithDeepL(ctx, cfg, texts, sourceLanguage, targetLanguage)
	}
	if translationProviderConfigured(cfg.LibreTranslateEndpoint) {
		return translateTextsWithLibreTranslate(ctx, cfg, texts, sourceLanguage, targetLanguage)
	}
	return nil, errTranslationNotConfigured
}

func translateTextsWithLibreTranslate(ctx context.Context, cfg config.Config, texts []string, sourceLanguage string, targetLanguage string) ([]translationResult, error) {
	source := sourceLanguage
	if source == "" || source == "und" {
		source = "auto"
	}
	payload := map[string]any{
		"q":       texts,
		"source":  source,
		"target":  targetLanguage,
		"format":  "html",
		"api_key": libreTranslateAPIKeyPayloadValue(cfg),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.LibreTranslateEndpoint+"/translate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := translationHTTPDo(req, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := translationResponseError(resp.StatusCode, true); err != nil {
		return nil, err
	}
	responseBody, err := readActivityResponseBodyWithRailsLimit(resp, "translation")
	if err != nil {
		return nil, errUnexpectedTranslationResponse
	}
	var response libreTranslateResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, errUnexpectedTranslationResponse
	}
	results, err := libreTranslateResults(response, sourceLanguage)
	if err != nil {
		return nil, err
	}
	if len(results) != len(texts) {
		return nil, errUnexpectedTranslationResponse
	}
	return results, nil
}

func libreTranslateAPIKeyPayloadValue(cfg config.Config) any {
	if cfg.LibreTranslateAPIKeySet || cfg.LibreTranslateAPIKey != "" {
		return cfg.LibreTranslateAPIKey
	}
	return nil
}

type libreTranslateResponse struct {
	TranslatedText   []string `json:"translatedText"`
	DetectedLanguage []struct {
		Language string `json:"language"`
	} `json:"detectedLanguage"`
}

func libreTranslateResults(response libreTranslateResponse, sourceLanguage string) ([]translationResult, error) {
	results := make([]translationResult, 0, len(response.TranslatedText))
	for index, text := range response.TranslatedText {
		detected := sourceLanguage
		if index < len(response.DetectedLanguage) && response.DetectedLanguage[index].Language != "" {
			detected = normalizeTranslationLanguage(response.DetectedLanguage[index].Language)
		}
		results = append(results, translationResult{Text: text, DetectedSourceLanguage: detected, Provider: "LibreTranslate"})
	}
	return results, nil
}

func translateTextsWithDeepL(ctx context.Context, cfg config.Config, texts []string, sourceLanguage string, targetLanguage string) ([]translationResult, error) {
	baseURL := "https://api-free.deepl.com"
	if deepLPlanUsesPaidEndpoint(cfg) {
		baseURL = "https://api.deepl.com"
	}
	form := deepLTranslateForm(texts, sourceLanguage, targetLanguage)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v2/translate", strings.NewReader(form))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+cfg.DeepLAPIKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := translationHTTPDo(req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := translationResponseError(resp.StatusCode, false); err != nil {
		return nil, err
	}
	body, err := readActivityResponseBodyWithRailsLimit(resp, "translation")
	if err != nil {
		return nil, errUnexpectedTranslationResponse
	}
	var response deepLTranslateResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errUnexpectedTranslationResponse
	}
	results, err := deepLResults(response)
	if err != nil {
		return nil, err
	}
	if len(results) != len(texts) {
		return nil, errUnexpectedTranslationResponse
	}
	return results, nil
}

func deepLTranslateForm(texts []string, sourceLanguage string, targetLanguage string) string {
	parts := make([]string, 0, len(texts)+3)
	for _, text := range texts {
		parts = append(parts, url.QueryEscape("text")+"="+url.QueryEscape(text))
	}
	if sourceLanguage == "" || sourceLanguage == "und" {
		parts = append(parts, url.QueryEscape("source_lang"))
	} else {
		parts = append(parts, url.QueryEscape("source_lang")+"="+url.QueryEscape(strings.ToUpper(sourceLanguage)))
	}
	parts = append(parts, url.QueryEscape("target_lang")+"="+url.QueryEscape(targetLanguage))
	parts = append(parts, url.QueryEscape("tag_handling")+"="+url.QueryEscape("html"))
	return strings.Join(parts, "&")
}

type deepLTranslateResponse struct {
	Translations []struct {
		Text                   string `json:"text"`
		DetectedSourceLanguage string `json:"detected_source_language"`
	} `json:"translations"`
}

func deepLResults(response deepLTranslateResponse) ([]translationResult, error) {
	results := make([]translationResult, 0, len(response.Translations))
	for _, translation := range response.Translations {
		results = append(results, translationResult{
			Text:                   translation.Text,
			DetectedSourceLanguage: normalizeTranslationLanguage(translation.DetectedSourceLanguage),
			Provider:               "DeepL.com",
		})
	}
	return results, nil
}

func translationResponseError(statusCode int, libreTranslate bool) error {
	switch statusCode {
	case http.StatusTooManyRequests:
		return errTranslationTooManyRequests
	case http.StatusForbidden:
		return errTranslationQuotaExceeded
	case 456:
		if !libreTranslate {
			return errTranslationQuotaExceeded
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return errUnexpectedTranslationResponse
	}
	return nil
}

func translationTargetLanguage(c *echo.Context, user models.User, cfg config.Config) string {
	if language := railsContentLocaleCandidate((*c).QueryParam("lang")); language != "" {
		return language
	}
	if user.Locale.Valid {
		if language := railsContentLocaleCandidate(user.Locale.String); language != "" {
			return language
		}
	}
	if language := acceptLanguageContentLocale((*c).Request().Header.Get("Accept-Language")); language != "" {
		return language
	}
	if language := railsContentLocaleCandidate(cfg.DefaultLocale); language != "" {
		return language
	}
	return "ja"
}

func railsContentLocaleCandidate(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" || !railsI18nLocaleAvailable(locale) {
		return ""
	}
	language, _, _ := strings.Cut(locale, "-")
	return language
}

func acceptLanguageCandidate(header string) string {
	part, _, _ := strings.Cut(header, ",")
	language, _, _ := strings.Cut(part, ";")
	return strings.TrimSpace(language)
}

func acceptLanguageContentLocale(header string) string {
	type candidate struct {
		value string
		q     float64
		order int
	}
	candidates := []candidate{}
	for index, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		language, params, _ := strings.Cut(part, ";")
		language = strings.TrimSpace(language)
		q := 1.0
		for _, param := range strings.Split(params, ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || strings.ToLower(key) != "q" {
				continue
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err == nil {
				q = parsed
			}
		}
		if language != "" && q > 0 {
			candidates = append(candidates, candidate{value: language, q: q, order: index})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].q == candidates[j].q {
			return candidates[i].order < candidates[j].order
		}
		return candidates[i].q > candidates[j].q
	})
	for _, candidate := range candidates {
		if locale := railsCompatibleLocale(candidate.value); locale != "" {
			language, _, _ := strings.Cut(locale, "-")
			return language
		}
	}
	return ""
}

func railsCompatibleLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" || locale == "*" {
		return ""
	}
	if railsI18nLocaleAvailable(locale) {
		return locale
	}
	primary, _, _ := strings.Cut(locale, "-")
	if railsI18nLocaleAvailable(primary) {
		return primary
	}
	return ""
}

func railsI18nLocaleAvailable(locale string) bool {
	for _, available := range railsI18nAvailableLocales {
		if locale == available {
			return true
		}
	}
	return false
}

func normalizeContentLocale(locale string) string {
	language := normalizeTranslationLanguage(locale)
	if language == "" {
		return ""
	}
	primary, _, _ := strings.Cut(language, "-")
	return primary
}

func statusTranslationSourceLanguage(language sql.NullString) string {
	if language.Valid && strings.TrimSpace(language.String) != "" {
		return normalizeTranslationLanguage(language.String)
	}
	return "und"
}

func translationTargetAllowed(languages map[string][]string, sourceLanguage string, targetLanguage string) bool {
	for _, target := range languages[sourceLanguage] {
		if target == targetLanguage {
			return true
		}
	}
	return false
}

func statusTranslationSources(cfg config.Config, status models.Status) []statusTranslationSource {
	sources := []statusTranslationSource{}
	if strings.TrimSpace(status.Text) != "" {
		content := serializer.StatusFromModel(cfg, status, nil).Content
		sources = append(sources, statusTranslationSource{kind: "content", text: wrapTranslationEmojiShortcodes(content, status.CustomEmojis)})
	}
	if strings.TrimSpace(status.SpoilerText) != "" {
		spoiler := html.EscapeString(status.SpoilerText)
		sources = append(sources, statusTranslationSource{kind: "spoiler_text", text: wrapTranslationEmojiShortcodes(spoiler, status.CustomEmojis)})
	}
	if status.Poll != nil {
		for index, option := range status.Poll.Options {
			title := html.EscapeString(option)
			sources = append(sources, statusTranslationSource{kind: "poll_option", text: wrapTranslationEmojiShortcodes(title, status.CustomEmojis), pollOptionID: index})
		}
	}
	for _, attachment := range orderedTranslationMediaAttachments(status) {
		description := ""
		if attachment.Description.Valid {
			description = attachment.Description.String
		}
		sources = append(sources, statusTranslationSource{
			kind:    "media_attachment",
			text:    html.EscapeString(description),
			mediaID: attachment.ID,
		})
	}
	return sources
}

func buildStatusTranslation(status models.Status, targetLanguage string, sources []statusTranslationSource, results []translationResult) serializer.Translation {
	detected, provider := translationMetadata(results)
	out := serializer.Translation{
		DetectedSourceLanguage: detected,
		Language:               targetLanguage,
		Provider:               provider,
		MediaAttachments:       []serializer.TranslationMediaAttachment{},
	}
	var pollOptions []serializer.TranslationOption
	for index, source := range sources {
		if index >= len(results) {
			break
		}
		text := results[index].Text
		switch source.kind {
		case "content":
			out.Content = sanitizeTranslationContentHTML(text)
		case "spoiler_text":
			out.SpoilerText = translationPlainText(text)
		case "poll_option":
			pollOptions = append(pollOptions, serializer.TranslationOption{Title: translationPlainText(text)})
		case "media_attachment":
			out.MediaAttachments = append(out.MediaAttachments, serializer.TranslationMediaAttachment{
				ID:          strconv.FormatInt(source.mediaID, 10),
				Description: html.UnescapeString(text),
			})
		}
	}
	if status.Poll != nil {
		out.Poll = &serializer.TranslationPoll{
			ID:      strconv.FormatInt(status.Poll.ID, 10),
			Options: pollOptions,
		}
	}
	return out
}

func wrapTranslationEmojiShortcodes(value string, emojis []models.CustomEmoji) string {
	emojiMap := make(map[string]models.CustomEmoji, len(emojis))
	for _, emoji := range emojis {
		shortcode := strings.TrimSpace(emoji.Shortcode)
		if shortcode == "" {
			continue
		}
		emoji.Shortcode = shortcode
		emojiMap[shortcode] = emoji
	}
	return applyCustomEmojisToContentWithTag(value, emojiMap, func(models.CustomEmoji) string { return "raw-shortcode" }, translationRawShortcodeTag)
}

func translationRawShortcodeTag(_ models.CustomEmoji, shortcode string, _ func(models.CustomEmoji) string) string {
	return `<span translate="no">:` + html.EscapeString(shortcode) + `:</span>`
}

func unwrapTranslationEmojiShortcodes(value string) string {
	for {
		next := translationNoTranslateSpanPattern.ReplaceAllString(value, "$1")
		if next == value {
			return next
		}
		value = next
	}
}

func sanitizeTranslationContentHTML(value string) string {
	value = unwrapTranslationEmojiShortcodes(value)
	value = oEmbedScriptBlockPattern.ReplaceAllString(value, "")
	var out strings.Builder
	last := 0
	for _, match := range oEmbedHTMLTagPattern.FindAllStringSubmatchIndex(value, -1) {
		if match[0] > last {
			out.WriteString(escapeTranslationText(value[last:match[0]]))
		}
		closing := value[match[2]:match[3]] != ""
		name := strings.ToLower(value[match[4]:match[5]])
		attrText := ""
		if match[6] >= 0 {
			attrText = value[match[6]:match[7]]
		}
		if translationAllowedContentElement(name) {
			if closing {
				if name != "br" {
					out.WriteString("</" + name + ">")
				}
			} else {
				out.WriteString("<" + name + sanitizeTranslationContentAttrs(name, attrText) + ">")
			}
		}
		last = match[1]
	}
	if last < len(value) {
		out.WriteString(escapeTranslationText(value[last:]))
	}
	return out.String()
}

func translationPlainText(value string) string {
	value = unwrapTranslationEmojiShortcodes(value)
	value = oEmbedScriptBlockPattern.ReplaceAllString(value, "")
	value = oEmbedHTMLTagPattern.ReplaceAllString(value, "")
	return html.UnescapeString(value)
}

func escapeTranslationText(value string) string {
	return html.EscapeString(html.UnescapeString(value))
}

func translationAllowedContentElement(name string) bool {
	switch name {
	case "a", "b", "blockquote", "br", "code", "del", "em", "i", "li", "ol", "p", "pre", "s", "span", "strong", "u", "ul":
		return true
	default:
		return false
	}
}

func sanitizeTranslationContentAttrs(name string, attrText string) string {
	attrs := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, match := range oEmbedAttrPattern.FindAllStringSubmatch(attrText, -1) {
		if len(match) < 5 {
			continue
		}
		key := strings.ToLower(match[1])
		if !translationAllowedContentAttr(name, key) {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		value := strings.TrimSpace(firstNonEmpty(match[2], match[3], match[4]))
		if key == "href" && !translationSafeURL(value) {
			continue
		}
		if key == "target" && value != "_blank" {
			continue
		}
		attrs = append(attrs, key+`="`+html.EscapeString(value)+`"`)
		seen[key] = struct{}{}
	}
	if name == "a" {
		if _, ok := seen["href"]; !ok {
			if len(attrs) == 0 {
				return ""
			}
			return " " + strings.Join(attrs, " ")
		}
		if _, ok := seen["rel"]; !ok {
			attrs = append(attrs, `rel="nofollow noopener noreferrer"`)
		}
	}
	if len(attrs) == 0 {
		return ""
	}
	return " " + strings.Join(attrs, " ")
}

func translationAllowedContentAttr(name string, key string) bool {
	switch key {
	case "lang":
		return true
	case "class":
		return name == "a" || name == "span" || name == "p" || name == "code"
	case "href", "rel", "target", "title":
		return name == "a"
	default:
		return false
	}
}

func translationSafeURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if parsed.IsAbs() {
		scheme := strings.ToLower(parsed.Scheme)
		return scheme == "http" || scheme == "https" || scheme == "mailto"
	}
	return strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "#")
}

func translationMetadata(results []translationResult) (*string, *string) {
	for _, result := range results {
		var detected *string
		if result.DetectedSourceLanguage != "" {
			value := result.DetectedSourceLanguage
			detected = &value
		}
		var provider *string
		if result.Provider != "" {
			value := result.Provider
			provider = &value
		}
		if detected != nil || provider != nil {
			return detected, provider
		}
	}
	return nil, nil
}

func orderedTranslationMediaAttachments(status models.Status) []models.MediaAttachment {
	if status.OrderedMediaAttachmentIDs == nil {
		return mediaAttachmentsSortedByID(status.MediaAttachments)
	}
	byID := make(map[int64]models.MediaAttachment, len(status.MediaAttachments))
	for _, attachment := range status.MediaAttachments {
		byID[attachment.ID] = attachment
	}
	ordered := make([]models.MediaAttachment, 0, len(status.OrderedMediaAttachmentIDs))
	for _, id := range status.OrderedMediaAttachmentIDs {
		if attachment, ok := byID[id]; ok {
			ordered = append(ordered, attachment)
		}
	}
	return ordered
}

func libreTranslateLanguageMap(payload []struct {
	Code    string   `json:"code"`
	Targets []string `json:"targets"`
}) map[string][]string {
	out := map[string][]string{}
	allTargets := map[string]struct{}{}
	for _, language := range payload {
		code := normalizeTranslationLanguage(language.Code)
		if code == "" {
			continue
		}
		targets := normalizeTranslationTargets(language.Targets, code)
		out[code] = targets
		for _, target := range targets {
			allTargets[target] = struct{}{}
		}
	}
	out["und"] = sortedStringSet(allTargets)
	return out
}

func deepLTranslationLanguages(ctx context.Context, cfg config.Config) (map[string][]string, error) {
	sources, err := fetchDeepLLanguages(ctx, cfg, "source")
	if err != nil {
		return nil, err
	}
	targets, err := fetchDeepLLanguages(ctx, cfg, "target")
	if err != nil {
		return nil, err
	}
	targetSet := map[string]struct{}{"en": {}, "pt": {}}
	for _, target := range targets {
		targetSet[target] = struct{}{}
	}
	allTargets := sortedStringSet(targetSet)
	out := map[string][]string{"und": allTargets}
	for _, source := range sources {
		out[source] = targetsWithout(allTargets, source)
	}
	return out, nil
}

func fetchDeepLLanguages(ctx context.Context, cfg config.Config, languageType string) ([]string, error) {
	baseURL := "https://api-free.deepl.com"
	if deepLPlanUsesPaidEndpoint(cfg) {
		baseURL = "https://api.deepl.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v2/languages?type="+url.QueryEscape(languageType), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+cfg.DeepLAPIKey)
	resp, err := translationHTTPDo(req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := translationResponseError(resp.StatusCode, false); err != nil {
		return nil, err
	}
	body, err := readActivityResponseBodyWithRailsLimit(resp, "translation-languages")
	if err != nil {
		return nil, errUnexpectedTranslationResponse
	}
	var payload []struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(payload))
	for _, language := range payload {
		code := normalizeTranslationLanguage(language.Language)
		if code != "" {
			out = append(out, code)
		}
	}
	sort.Strings(out)
	return out, nil
}

func deepLPlanUsesPaidEndpoint(cfg config.Config) bool {
	if !cfg.DeepLPlanSet && strings.TrimSpace(cfg.DeepLPlan) == "" {
		return false
	}
	return cfg.DeepLPlan != "free"
}

func normalizeTranslationLanguage(language string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(language), func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	parts[0] = strings.ToLower(parts[0])
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i])
	}
	return strings.Join(parts, "-")
}

func normalizeTranslationTargets(targets []string, source string) []string {
	seen := map[string]struct{}{}
	for _, target := range targets {
		code := normalizeTranslationLanguage(target)
		if code == "" || code == source {
			continue
		}
		seen[code] = struct{}{}
	}
	return sortedStringSet(seen)
}

func sortedStringSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func targetsWithout(targets []string, source string) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		if target != source {
			out = append(out, target)
		}
	}
	return out
}
