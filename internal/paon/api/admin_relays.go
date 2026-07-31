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
	"gorm.io/gorm/clause"
)

const (
	relayStateIdle     = 0
	relayStatePending  = 1
	relayStateAccepted = 2
	relayStateRejected = 3
)

type adminRelayForm struct {
	InboxURL string
}

func (s *Server) adminRelaysPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	relays, err := s.adminRelayModels()
	if err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRelaysIndexHTML(relays, c.QueryParam("notice"), c.QueryParam("error"), s.authorizedFetchMode(), s.webLocale(c, user)))
}

func (s *Server) newAdminRelayPage(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	return c.HTML(http.StatusOK, adminRelayFormHTML(adminRelayForm{}, c.QueryParam("error"), s.authorizedFetchMode(), s.webLocale(c, user)))
}

func (s *Server) createAdminRelay(c *echo.Context) error {
	user, handled, err := s.requireAdminFederationWebUser(c)
	if handled || err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	form, err := parseAdminRelayForm(c)
	if err != nil {
		if errors.Is(err, errAdminRelayParamsMissing) {
			return apiError(c, http.StatusBadRequest, "Malformed request")
		}
		return c.HTML(http.StatusOK, adminRelayFormHTML(form, adminRelayMessage(locale, "errors.invalid", "Relay is invalid"), s.authorizedFetchMode(), locale))
	}
	if err := validateAdminRelayForm(form); err != nil {
		return c.HTML(http.StatusOK, adminRelayFormHTML(form, adminRelayErrorText(locale, err), s.authorizedFetchMode(), locale))
	}
	if s.db == nil {
		return c.HTML(http.StatusOK, adminRelayFormHTML(form, adminRelayMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set"), s.authorizedFetchMode(), locale))
	}
	if err := s.insertAdminRelay(form); err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/relays")
}

func (s *Server) enableAdminRelay(c *echo.Context) error {
	if _, handled, err := s.requireAdminFederationWebUser(c); handled || err != nil {
		return err
	}
	relay, err := s.findAdminRelay(c.Param("id"))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	activityID := s.relayFollowActivityID()
	if err := s.db.Model(&models.Relay{}).Where("id = ?", relay.ID).Updates(map[string]any{
		"state":              relayStatePending,
		"follow_activity_id": sql.NullString{String: activityID, Valid: true},
		"updated_at":         now,
	}).Error; err != nil {
		return err
	}
	relay.FollowActivityID = sql.NullString{String: activityID, Valid: true}
	s.trackActivityPubDeliveryStoplightSuccess(relay.InboxURL)
	_ = s.deliverActivityPubRelayFollow(relay, activityID)
	return c.Redirect(http.StatusFound, "/admin/relays")
}

func (s *Server) disableAdminRelay(c *echo.Context) error {
	if _, handled, err := s.requireAdminFederationWebUser(c); handled || err != nil {
		return err
	}
	relay, err := s.findAdminRelay(c.Param("id"))
	if err != nil {
		return err
	}
	activityID := s.relayFollowActivityID()
	if err := s.db.Model(&models.Relay{}).Where("id = ?", relay.ID).Updates(map[string]any{
		"state":              relayStateIdle,
		"follow_activity_id": sql.NullString{},
		"updated_at":         time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	s.trackActivityPubDeliveryStoplightSuccess(relay.InboxURL)
	_ = s.deliverActivityPubRelayUndoFollow(relay, activityID)
	return c.Redirect(http.StatusFound, "/admin/relays")
}

func (s *Server) destroyAdminRelay(c *echo.Context) error {
	if _, handled, err := s.requireAdminFederationWebUser(c); handled || err != nil {
		return err
	}
	relay, err := s.findAdminRelay(c.Param("id"))
	if err != nil {
		return err
	}
	if relay.State == relayStateAccepted {
		activityID := s.relayFollowActivityID()
		if err := s.db.Model(&models.Relay{}).Where("id = ?", relay.ID).Updates(map[string]any{
			"state":              relayStateIdle,
			"follow_activity_id": sql.NullString{},
			"updated_at":         time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		s.trackActivityPubDeliveryStoplightSuccess(relay.InboxURL)
		_ = s.deliverActivityPubRelayUndoFollow(relay, activityID)
	}
	if err := s.db.Delete(&models.Relay{}, relay.ID).Error; err != nil {
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/relays")
}

func adminRelayMessage(locale string, key string, fallback string) string {
	return adminT(locale, "admin.relays."+key, fallback)
}

func adminRelayErrorText(locale string, err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	keys := map[string]string{
		"Relay inbox URL can't be blank": "inbox_url_blank",
		"Relay inbox URL is invalid":     "inbox_url_invalid",
	}
	if key := keys[text]; key != "" {
		return adminRelayMessage(locale, "errors."+key, text)
	}
	return text
}

func (s *Server) adminRelayMemberAction(c *echo.Context) error {
	if methodOverrideIs(c, "delete") {
		return s.destroyAdminRelay(c)
	}
	return c.Redirect(http.StatusFound, "/admin/relays")
}

func (s *Server) requireAdminFederationWebUser(c *echo.Context) (*models.User, bool, error) {
	user, _, handled, err := s.requireFunctionalWebUser(c)
	if handled || err != nil {
		return nil, handled, err
	}
	if !s.userCan(user, rolePermissionManageFederation) {
		locale := s.webLocale(c, user)
		return nil, true, c.HTML(http.StatusForbidden, authPageHTML(adminT(locale, "admin.relays.title", "Admin federation"), "", adminT(locale, "admin.relays.not_permitted", "You are not allowed to manage federation."), "", locale))
	}
	return user, false, nil
}

func (s *Server) adminRelayModels() ([]models.Relay, error) {
	if s.db == nil {
		return []models.Relay{}, nil
	}
	var relays []models.Relay
	err := s.db.Order("id ASC").Find(&relays).Error
	return relays, err
}

func (s *Server) findAdminRelay(rawID string) (models.Relay, error) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return models.Relay{}, echo.NewHTTPError(http.StatusNotFound, "relay not found")
	}
	var relay models.Relay
	if s.db == nil {
		return relay, echo.NewHTTPError(http.StatusNotFound, "relay not found")
	}
	if err := s.db.Where("id = ?", id).First(&relay).Error; err != nil {
		return relay, echo.NewHTTPError(http.StatusNotFound, "relay not found")
	}
	return relay, nil
}

func parseAdminRelayForm(c *echo.Context) (adminRelayForm, error) {
	req := c.Request()
	if err := req.ParseForm(); err != nil {
		return adminRelayForm{}, err
	}
	prefix := "relay"
	if !formHasNestedPrefix(req.Form, prefix) {
		return adminRelayForm{}, errAdminRelayParamsMissing
	}
	return adminRelayForm{InboxURL: strings.TrimSpace(lastFormValue(req.Form, "relay[inbox_url]"))}, nil
}

var errAdminRelayParamsMissing = errors.New("admin relay root parameter is missing")

func validateAdminRelayForm(form adminRelayForm) error {
	inboxURL := strings.TrimSpace(form.InboxURL)
	if inboxURL == "" {
		return errAdminSetting("Relay inbox URL can't be blank")
	}
	parsed, err := url.Parse(inboxURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errAdminSetting("Relay inbox URL is invalid")
	}
	return nil
}

func (s *Server) insertAdminRelay(form adminRelayForm) error {
	now := time.Now().UTC()
	inboxURL := strings.TrimSpace(form.InboxURL)
	relay := models.Relay{
		InboxURL:  inboxURL,
		State:     relayStatePending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.Create(&relay).Error; err != nil {
		return err
	}
	activityID := s.relayFollowActivityID()
	if err := s.db.Model(&models.Relay{}).Where("id = ?", relay.ID).Updates(map[string]any{
		"follow_activity_id": sql.NullString{String: activityID, Valid: true},
		"updated_at":         now,
	}).Error; err != nil {
		return err
	}
	relay.FollowActivityID = sql.NullString{String: activityID, Valid: true}
	s.trackActivityPubDeliveryStoplightSuccess(relay.InboxURL)
	_ = s.deliverActivityPubRelayFollow(relay, activityID)
	return nil
}

func (s *Server) relayFollowActivityID() string {
	return activityPubGeneratedPayloadURI(s)
}

func (s *Server) representativeActivityPubAccount() (*models.Account, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var account models.Account
	err := s.db.Preload("AccountStat").Where("id = ?", int64(-99)).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		privateKey, publicKey, keyErr := generateAccountKeyPair()
		if keyErr != nil {
			return nil, keyErr
		}
		now := time.Now().UTC()
		if err := s.db.Model(&models.Account{}).Clauses(clause.OnConflict{DoNothing: true}).Create(map[string]any{
			"id":          int64(-99),
			"username":    instanceActorUsername,
			"actor_type":  sql.NullString{String: "Application", Valid: true},
			"locked":      true,
			"private_key": sql.NullString{String: privateKey, Valid: true},
			"public_key":  publicKey,
			"created_at":  now,
			"updated_at":  now,
		}).Error; err != nil {
			return nil, err
		}
		if err := s.db.Preload("AccountStat").Where("id = ?", int64(-99)).First(&account).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := s.ensureRepresentativeAccountStat(account.ID); err != nil {
		return nil, err
	}
	updates := map[string]any{}
	if strings.Contains(account.Username, ":") || account.Username == "" {
		updates["username"] = instanceActorUsername
		account.Username = instanceActorUsername
	}
	if !account.PrivateKey.Valid || strings.TrimSpace(account.PrivateKey.String) == "" || strings.TrimSpace(account.PublicKey) == "" {
		privateKey, publicKey, keyErr := generateAccountKeyPair()
		if keyErr != nil {
			return nil, keyErr
		}
		updates["private_key"] = sql.NullString{String: privateKey, Valid: true}
		updates["public_key"] = publicKey
		account.PrivateKey = sql.NullString{String: privateKey, Valid: true}
		account.PublicKey = publicKey
	}
	if len(updates) > 0 {
		updates["updated_at"] = time.Now().UTC()
		if err := s.db.Model(&models.Account{}).Where("id = ?", account.ID).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	if err := s.db.Preload("AccountStat").Where("id = ?", account.ID).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (s *Server) ensureRepresentativeAccountStat(accountID int64) error {
	now := time.Now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := setActivityPubTransactionLockTimeout(tx); err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO account_stats (account_id, statuses_count, following_count, followers_count, created_at, updated_at)
			VALUES (?, 0, 0, 0, ?, ?)
			ON CONFLICT (account_id) DO NOTHING
		`, accountID, now, now).Error
	})
}

func adminRelaysIndexHTML(relays []models.Relay, notice string, errorText string, authorizedFetch bool, locale ...string) string {
	loc := settingsLocaleArgOrEnglish(locale...)
	var rows strings.Builder
	if len(relays) > 0 {
		rows.WriteString(`<hr class="spacer"><div class="table-wrapper"><table class="table"><thead><tr><th>` + html.EscapeString(adminT(loc, "admin.relays.inbox_url", "Relay URL")) + `</th><th>` + html.EscapeString(adminT(loc, "admin.relays.status", "Status")) + `</th><th></th></tr></thead><tbody>`)
		for _, relay := range relays {
			id := strconv.FormatInt(relay.ID, 10)
			stateClass := "negative-hint"
			stateIcon := "times"
			if relay.State == relayStateAccepted {
				stateClass, stateIcon = "positive-hint", "check"
			} else if relay.State == relayStatePending {
				stateClass, stateIcon = "", "hourglass"
			}
			state := `<span class="` + stateClass + `"><i class="fa fa-` + stateIcon + `"></i> ` + html.EscapeString(adminT(loc, "admin.relays."+adminRelayStateLabel(relay.State), adminRelayStateLabel(relay.State))) + `</span>`
			rows.WriteString(`<tr><td><samp>` + html.EscapeString(relay.InboxURL) + `</samp></td><td>` + state + `</td><td>`)
			confirm := adminT(loc, "admin.accounts.are_you_sure", "Are you sure?")
			if relay.State == relayStateAccepted {
				rows.WriteString(adminTableActionLinkHTML("power-off", adminT(loc, "admin.relays.disable", "Disable"), "/admin/relays/"+id+"/disable", "post", confirm))
			} else if relay.State != relayStatePending {
				rows.WriteString(adminTableActionLinkHTML("power-off", adminT(loc, "admin.relays.enable", "Enable"), "/admin/relays/"+id+"/enable", "post", confirm))
			}
			rows.WriteString(adminTableActionLinkHTML("times", adminT(loc, "admin.relays.delete", "Delete"), "/admin/relays/"+id, "delete", confirm) + `</td></tr>`)
		}
		rows.WriteString(`</tbody></table></div>`)
	}
	warning := adminRelayAuthorizedFetchWarningHTML(authorizedFetch, loc)
	description := adminT(loc, "admin.relays.description_html", "A <strong>federation relay</strong> is an intermediary server that exchanges large volumes of public posts between servers that subscribe and publish to it. <strong>It can help small and medium servers discover content from the fediverse</strong>, which would otherwise require local users manually following other people on remote servers.")
	linkLabelKey := "admin.relays.add_new"
	linkLabelFallback := "Add new relay"
	if len(relays) == 0 {
		linkLabelKey = "admin.relays.setup"
		linkLabelFallback = "Setup a relay connection"
	}
	body := `<div class="simple_form"><p class="hint">` + description + `</p><a class="block-button" href="/admin/relays/new">` + html.EscapeString(adminT(loc, linkLabelKey, linkLabelFallback)) + `</a></div>` + warning + rows.String()
	return authPageHTML(adminT(loc, "admin.relays.title", "Admin relays"), notice, errorText, body, loc)
}

func adminRelayFormHTML(form adminRelayForm, errorText string, authorizedFetch bool, locale ...string) string {
	loc := settingsLocaleArg(locale...)
	warning := adminRelayAuthorizedFetchWarningHTML(authorizedFetch, loc)
	body := warning + simpleFormOpen("/admin/relays", "post") +
		simpleTextInput(adminT(loc, "simple_form.labels.relay.inbox_url", "Inbox URL"), "relay[inbox_url]", form.InboxURL, "url", `required`) +
		simpleSubmit(adminT(loc, "admin.relays.save_and_enable", "Save and enable")) +
		`<p class="hint subtle-hint">` + html.EscapeString(adminT(loc, "admin.relays.enable_hint", "Once enabled, your server will subscribe to all public posts from this relay, and will begin sending this server's public posts to it.")) + `</p>` +
		simpleFormClose()
	return authPageHTML(adminT(loc, "admin.relays.add_new", "Add relay"), "", errorText, body, loc)
}

func adminRelayAuthorizedFetchWarningHTML(enabled bool, locale ...string) string {
	if !enabled {
		return ""
	}
	loc := settingsLocaleArgOrEnglish(locale...)
	return `<p class="flash-message error">` + html.EscapeString(adminT(loc, "admin.relays.signatures_not_enabled", "Relays may not work correctly while secure mode or limited federation mode is enabled")) + `</p>`
}

func adminRelayStateLabel(state int) string {
	switch state {
	case relayStatePending:
		return "pending"
	case relayStateAccepted:
		return "enabled"
	case relayStateRejected:
		return "rejected"
	default:
		return "disabled"
	}
}
