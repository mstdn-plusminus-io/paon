package api

import (
	"html"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func (s *Server) adminSoftwareUpdatesPage(c *echo.Context) error {
	locale := s.webLocale(c, nil)
	if !s.softwareUpdateCheckEnabled() {
		return c.HTML(http.StatusNotFound, authPageHTML(adminT(locale, "admin.software_updates.title", "Available updates"), "", adminT(locale, "admin.software_updates.disabled", "Software update checks are disabled."), "", locale))
	}
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return err
	}
	locale = s.webLocale(c, user)
	if !s.userCan(user, rolePermissionViewDevops) {
		return c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.software_updates.title", "Available updates"), "", adminT(locale, "admin.software_updates.not_permitted", "You are not allowed to view software updates."), "", locale))
	}
	updates, err := s.softwareUpdates()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminSoftwareUpdatesHTML(updates, locale))
}

func (s *Server) softwareUpdateCheckEnabled() bool {
	return s.cfg.UpdateCheckURL != ""
}

func (s *Server) softwareUpdates() ([]models.SoftwareUpdate, error) {
	if s.db == nil {
		return []models.SoftwareUpdate{}, nil
	}
	var updates []models.SoftwareUpdate
	if err := s.db.Find(&updates).Error; err != nil {
		return nil, err
	}
	updates = pendingSoftwareUpdates(updates, s.currentSoftwareUpdateVersion())
	sort.SliceStable(updates, func(i, j int) bool {
		return compareSoftwareVersions(updates[i].Version, updates[j].Version) < 0
	})
	return updates, nil
}

func pendingSoftwareUpdates(updates []models.SoftwareUpdate, currentVersion string) []models.SoftwareUpdate {
	pending := make([]models.SoftwareUpdate, 0, len(updates))
	for _, update := range updates {
		if compareSoftwareVersions(update.Version, currentVersion) > 0 {
			pending = append(pending, update)
		}
	}
	return pending
}

func (s *Server) criticalSoftwareUpdatesPending() (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var updates []models.SoftwareUpdate
	if err := s.db.Find(&updates).Error; err != nil {
		return false, err
	}
	return criticalSoftwareUpdatesPending(updates, s.currentSoftwareUpdateVersion()), nil
}

func criticalSoftwareUpdatesPending(updates []models.SoftwareUpdate, currentVersion string) bool {
	for _, update := range updates {
		if update.Urgent && compareSoftwareVersions(update.Version, currentVersion) > 0 {
			return true
		}
	}
	return false
}

func adminSoftwareUpdatesHTML(updates []models.SoftwareUpdate, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var body strings.Builder
	body.WriteString(`<p class="lead">` + html.EscapeString(adminT(loc, "admin.software_updates.description", "It is recommended to keep your Mastodon installation up to date to benefit from the latest fixes and features.")) + ` <a href="https://docs.joinmastodon.org/admin/upgrading/#automated_checks" target="_new">` + html.EscapeString(adminT(loc, "admin.software_updates.documentation_link", "Learn more")) + `</a></p>`)
	if len(updates) > 0 {
		body.WriteString(`<div class="table-wrapper"><table class="table"><thead><tr><th>` + html.EscapeString(adminT(loc, "admin.software_updates.version", "Version")) + `</th><th>` + html.EscapeString(adminT(loc, "admin.software_updates.type", "Type")) + `</th><th></th><th></th></tr></thead><tbody>`)
		for _, update := range updates {
			body.WriteString(adminSoftwareUpdateRowHTML(update, loc))
		}
		body.WriteString(`</tbody></table></div>`)
	}
	return authPageHTML(adminT(loc, "admin.software_updates.title", "Available updates"), "", "", body.String(), loc)
}

func adminSoftwareUpdateRowHTML(update models.SoftwareUpdate, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	urgency := ""
	if update.Urgent {
		urgency = adminT(loc, "admin.software_updates.critical_update", "Critical update")
	}
	notes := "-"
	if strings.TrimSpace(update.ReleaseNotes) != "" {
		notes = `<a href="` + html.EscapeString(update.ReleaseNotes) + `">` + html.EscapeString(adminT(loc, "admin.software_updates.release_notes", "Release notes")) + `</a>`
	}
	return `<tr><td>` + html.EscapeString(update.Version) + `</td><td>` + html.EscapeString(softwareUpdateTypeLabel(update.Type, loc)) + `</td><td>` + html.EscapeString(urgency) + `</td><td>` + notes + `</td></tr>`
}

func softwareUpdateTypeLabel(value int, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	switch value {
	case 0:
		return adminT(loc, "admin.software_updates.types.patch", "patch")
	case 1:
		return adminT(loc, "admin.software_updates.types.minor", "minor")
	case 2:
		return adminT(loc, "admin.software_updates.types.major", "major")
	default:
		return "unknown"
	}
}

func compareSoftwareVersions(left string, right string) int {
	leftParts := softwareVersionGemSegments(left)
	rightParts := softwareVersionGemSegments(right)
	maxLen := len(leftParts)
	if len(rightParts) > maxLen {
		maxLen = len(rightParts)
	}
	for i := 0; i < maxLen; i++ {
		lv := softwareVersionSegment{}
		rv := softwareVersionSegment{}
		if i < len(leftParts) {
			lv = leftParts[i]
		}
		if i < len(rightParts) {
			rv = rightParts[i]
		}
		if lv.equal(rv) {
			continue
		}
		if lv.less(rv) {
			return -1
		}
		return 1
	}
	return 0
}

type softwareVersionSegment struct {
	number int
	text   string
}

func (s softwareVersionSegment) isString() bool {
	return s.text != ""
}

func (s softwareVersionSegment) equal(other softwareVersionSegment) bool {
	return s.number == other.number && s.text == other.text
}

func (s softwareVersionSegment) less(other softwareVersionSegment) bool {
	if s.isString() && !other.isString() {
		return true
	}
	if !s.isString() && other.isString() {
		return false
	}
	if s.isString() {
		return s.text < other.text
	}
	return s.number < other.number
}

var softwareVersionSegmentPattern = regexp.MustCompile(`[0-9]+|[A-Za-z]+`)

func softwareVersionGemSegments(value string) []softwareVersionSegment {
	value = strings.ToLower(strings.TrimSpace(value))
	if idx := strings.Index(value, "+"); idx >= 0 {
		value = value[:idx]
	}
	matches := softwareVersionSegmentPattern.FindAllString(value, -1)
	out := make([]softwareVersionSegment, 0, len(matches))
	for _, match := range matches {
		if match[0] >= '0' && match[0] <= '9' {
			number := 0
			for _, r := range match {
				number = number*10 + int(r-'0')
			}
			out = append(out, softwareVersionSegment{number: number})
		} else {
			out = append(out, softwareVersionSegment{text: match})
		}
	}
	return canonicalSoftwareVersionSegments(out)
}

func canonicalSoftwareVersionSegments(segments []softwareVersionSegment) []softwareVersionSegment {
	out := append([]softwareVersionSegment(nil), segments...)
	for len(out) > 0 && !out[len(out)-1].isString() && out[len(out)-1].number == 0 {
		out = out[:len(out)-1]
	}
	for i := 1; i < len(out); i++ {
		if out[i].isString() {
			j := i - 1
			for j > 0 && !out[j].isString() && out[j].number == 0 {
				out = append(out[:j], out[j+1:]...)
				i--
				j--
			}
		}
	}
	return out
}
