package api

import (
	"encoding/csv"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func (s *Server) severedRelationshipsPage(c *echo.Context) error {
	account, _, user, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	var events []models.AccountRelationshipSeveranceEvent
	if err := s.db.Preload("RelationshipSeveranceEvent").Where("account_id = ?", account.ID).Order("id DESC").Find(&events).Error; err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	var body strings.Builder
	body.WriteString(`<div class="table-wrapper"><table><thead><tr><th>` + html.EscapeString(settingsT(locale, "severed_relationships.type", "Type")) + `</th><th>` + html.EscapeString(settingsT(locale, "severed_relationships.target", "Domain")) + `</th><th>` + html.EscapeString(settingsT(locale, "severed_relationships.following", "Following")) + `</th><th>` + html.EscapeString(settingsT(locale, "severed_relationships.followers", "Followers")) + `</th></tr></thead><tbody>`)
	for _, event := range events {
		id := strconv.FormatInt(event.ID, 10)
		following := strconv.Itoa(event.FollowingCount)
		followers := strconv.Itoa(event.FollowersCount)
		if !event.RelationshipSeveranceEvent.Purged {
			following = `<a href="/severed_relationships/` + id + `/following.csv">` + following + `</a>`
			followers = `<a href="/severed_relationships/` + id + `/followers.csv">` + followers + `</a>`
		}
		body.WriteString(`<tr><td>` + html.EscapeString(severanceTypeLabel(event.RelationshipSeveranceEvent.Type, locale)) + `</td><td>` + html.EscapeString(event.RelationshipSeveranceEvent.TargetName) + `</td><td>` + following + `</td><td>` + followers + `</td></tr>`)
	}
	body.WriteString(`</tbody></table></div>`)
	if len(events) == 0 {
		body.WriteString(adminNothingHereHTML(locale, "nothing-here"))
	}
	return c.HTML(http.StatusOK, authPageHTML(settingsT(locale, "severed_relationships.title", "Severed relationships"), "", "", body.String(), locale))
}

func severanceTypeLabel(kind int, locale string) string {
	switch kind {
	case 1:
		return settingsT(locale, "severed_relationships.type.user_domain_block", "Domain blocked by you")
	default:
		return settingsT(locale, "severed_relationships.type.domain_block", "Server domain block")
	}
}

func (s *Server) severedRelationshipsFollowing(c *echo.Context) error {
	return s.severedRelationshipsCSV(c, 1)
}
func (s *Server) severedRelationshipsFollowers(c *echo.Context) error {
	return s.severedRelationshipsCSV(c, 0)
}

func (s *Server) severedRelationshipsCSV(c *echo.Context, direction int) error {
	account, _, _, err := s.requireFunctionalAccountForWeb(c)
	if err != nil {
		return webAuthResponseError(err)
	}
	var event models.AccountRelationshipSeveranceEvent
	if err := s.db.Preload("RelationshipSeveranceEvent").Where("id = ? AND account_id = ?", c.Param("id"), account.ID).First(&event).Error; err != nil || event.RelationshipSeveranceEvent.Purged {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var rows []models.SeveredRelationship
	if err := s.db.Preload("RemoteAccount").Where("relationship_severance_event_id = ? AND local_account_id = ? AND direction = ?", event.RelationshipSeveranceEventID, account.ID, direction).Order("id DESC").Find(&rows).Error; err != nil {
		return err
	}
	var output strings.Builder
	writer := csv.NewWriter(&output)
	if direction == 1 {
		_ = writer.Write([]string{"Account address", "Show boosts", "Notify on new posts", "Languages"})
	} else {
		_ = writer.Write([]string{"Account address"})
	}
	for _, row := range rows {
		if direction == 1 {
			_ = writer.Write([]string{row.RemoteAccount.Acct(), strconv.FormatBool(row.ShowReblogs.Bool), strconv.FormatBool(row.Notify.Bool), strings.Join(row.Languages, ", ")})
		} else {
			_ = writer.Write([]string{row.RemoteAccount.Acct()})
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "private, no-store")
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+map[int]string{0: "followers", 1: "following"}[direction]+`-`+event.RelationshipSeveranceEvent.TargetName+`.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", []byte(output.String()))
}
