package api

import (
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
)

func (s *Server) letterOpenerPage(c *echo.Context) error {
	c.Response().Header().Set("Content-Security-Policy", railsLetterOpenerCSP())
	locale := s.webLocale(c, nil)
	title := adminT(locale, "admin.devops.letter_opener_title", "Letter Opener")
	body := `<p class="lead">` + html.EscapeString(adminT(locale, "admin.devops.letter_opener_description", "Development mail generated without SMTP delivery is stored in memory and listed here for preview.")) + `</p>` +
		letterOpenerMailboxHTML(locale)
	if preview, ok := letterOpenerRequestedPreview(c); ok {
		body += letterOpenerPreviewHTML(locale, preview)
	}
	return c.HTML(http.StatusOK, authPageHTML(title, "", "", body, locale))
}

func letterOpenerRequestedPreview(c *echo.Context) (developmentMailPreview, bool) {
	path := strings.Trim(strings.TrimPrefix(c.Request().URL.Path, "/letter_opener"), "/")
	if path == "" || path == "inbox" {
		return developmentMailPreview{}, false
	}
	parts := strings.Split(path, "/")
	id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil {
		return developmentMailPreview{}, false
	}
	return developmentMailPreviewByID(id)
}

func letterOpenerMailboxHTML(locale string) string {
	previews := developmentMailPreviews()
	var body strings.Builder
	body.WriteString(`<div class="table-wrapper"><table class="table"><thead><tr><th>`)
	body.WriteString(html.EscapeString(adminT(locale, "admin.devops.mailbox", "Mailbox")))
	body.WriteString(`</th><th>`)
	body.WriteString(html.EscapeString(adminT(locale, "admin.devops.messages", "Messages")))
	body.WriteString(`</th></tr></thead><tbody><tr><td>Paon</td><td>`)
	body.WriteString(strconv.Itoa(len(previews)))
	body.WriteString(`</td></tr></tbody></table></div>`)
	body.WriteString(`<div class="table-wrapper"><table class="table"><thead><tr><th>`)
	body.WriteString(html.EscapeString(adminT(locale, "admin.devops.sent_at", "Sent at")))
	body.WriteString(`</th><th>`)
	body.WriteString(html.EscapeString(adminT(locale, "admin.devops.recipient", "Recipient")))
	body.WriteString(`</th><th>`)
	body.WriteString(html.EscapeString(adminT(locale, "admin.devops.subject", "Subject")))
	body.WriteString(`</th></tr></thead><tbody>`)
	if len(previews) == 0 {
		body.WriteString(`<tr><td colspan="3">`)
		body.WriteString(html.EscapeString(adminT(locale, "admin.devops.no_messages", "No messages")))
		body.WriteString(`</td></tr>`)
	} else {
		for _, preview := range previews {
			body.WriteString(`<tr><td><a href="/letter_opener/`)
			body.WriteString(strconv.FormatInt(preview.ID, 10))
			body.WriteString(`">`)
			body.WriteString(html.EscapeString(preview.CreatedAt.Format("2006-01-02 15:04:05 UTC")))
			body.WriteString(`</a></td><td>`)
			body.WriteString(html.EscapeString(preview.To))
			body.WriteString(`</td><td>`)
			body.WriteString(html.EscapeString(preview.Subject))
			body.WriteString(`</td></tr>`)
		}
	}
	body.WriteString(`</tbody></table></div>`)
	return body.String()
}

func letterOpenerPreviewHTML(locale string, preview developmentMailPreview) string {
	return `<hr class="spacer" />` +
		`<h2>` + html.EscapeString(preview.Subject) + `</h2>` +
		`<p><strong>` + html.EscapeString(adminT(locale, "admin.devops.recipient", "Recipient")) + `</strong>: ` + html.EscapeString(preview.To) + `</p>` +
		`<p><strong>` + html.EscapeString(adminT(locale, "admin.devops.sent_at", "Sent at")) + `</strong>: ` + html.EscapeString(preview.CreatedAt.Format("2006-01-02 15:04:05 UTC")) + `</p>` +
		`<pre class="input-copy" style="white-space: pre-wrap">` + html.EscapeString(preview.Raw) + `</pre>`
}
