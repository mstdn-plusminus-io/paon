package api

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

func (s *Server) statusSource(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if err := s.requireStatusReadScope(c); err != nil {
		return err
	}
	status, _, err := s.findVisibleStatusForRequest(c, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.StatusSourceFromModel(*status))
}

func (s *Server) requireStatusReadScope(c *echo.Context) error {
	return s.requireAccessTokenScope(c, "read", "read:statuses")
}

func (s *Server) requireAccessTokenScope(c *echo.Context, scopes ...string) error {
	c.Response().Header().Set("Vary", "Authorization")
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil {
		return apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !tokenHasAnyScope(accessToken.Scopes, scopes...) {
		return apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}
	return nil
}

func (s *Server) requireTokenScope(c *echo.Context, scopes ...string) error {
	user, _, err := s.requireUser(c)
	if err != nil {
		return err
	}
	return s.requireUserTokenScope(c, *user, scopes...)
}

func (s *Server) requireUserScope(c *echo.Context, scopes ...string) (*models.User, string, error) {
	c.Response().Header().Set("Vary", "Authorization")
	user, token, err := s.requireUser(c)
	if err != nil {
		return nil, "", err
	}
	if err := s.requireUserTokenScope(c, *user, scopes...); err != nil {
		return nil, "", err
	}
	return user, token, nil
}

func (s *Server) requireAccountScope(c *echo.Context, scopes ...string) (*models.Account, string, error) {
	c.Response().Header().Set("Vary", "Authorization")
	user, token, err := s.requireUser(c)
	if err != nil {
		return nil, "", err
	}
	if err := s.requireUserTokenScope(c, *user, scopes...); err != nil {
		return nil, "", err
	}
	var account models.Account
	if err := s.db.Preload("AccountStat").Preload("User.Role").Where("id = ?", user.AccountID).First(&account).Error; err != nil {
		return nil, "", apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if err := s.hydrateAccountCustomEmojis(&account); err != nil {
		return nil, "", err
	}
	return &account, token, nil
}

func (s *Server) requireUserTokenScope(c *echo.Context, user models.User, scopes ...string) error {
	accessToken, err := s.currentAccessToken(c)
	if err != nil || !accessToken.ResourceOwnerID.Valid || accessToken.ResourceOwnerID.Int64 != user.ID {
		return apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !tokenHasAnyScope(accessToken.Scopes, scopes...) {
		return apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}
	return nil
}

func (s *Server) authorizeTokenScopeIfPresent(c *echo.Context, scopes ...string) error {
	if requestToken(c) == "" {
		return nil
	}
	return s.requireAccessTokenScope(c, scopes...)
}

func (s *Server) translateStatus(c *echo.Context) error {
	user, _, err := s.requireUserScope(c, "read", "read:statuses")
	if err != nil {
		return err
	}
	status, _, err := s.findVisibleStatusForRequest(c, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	translation, err := s.translateStatusForUser(c, *status, *user)
	if err != nil {
		return translationAPIError(c, err)
	}
	return c.JSON(http.StatusOK, translation)
}

func (s *Server) statusHistory(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	status, _, err := s.findVisibleStatusForRequest(c, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	var edits []models.StatusEdit
	if err := s.db.Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Where("status_id = ?", status.ID).
		Order("id ASC").
		Find(&edits).Error; err != nil {
		return err
	}
	if len(edits) == 0 {
		edits = []models.StatusEdit{statusSnapshotEdit(*status)}
	}

	out := make([]serializer.StatusEdit, 0, len(edits))
	for _, edit := range edits {
		edit.Status = *status
		edit.OrderedMediaAttachments = orderedEditMediaAttachments(edit, status.MediaAttachments)
		emojis, err := s.statusEditCustomEmojis(edit, *status)
		if err != nil {
			return err
		}
		edit.CustomEmojis = emojis
		out = append(out, serializer.StatusEditFromModel(s.cfg, edit))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) statusCard(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	status, _, err := s.findVisibleStatusForRequest(c, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if len(status.PreviewCards) == 0 {
		_ = s.fetchLinkCardForStatus(c.Request().Context(), status.ID)
		status, _, err = s.findVisibleStatusForRequest(c, c.Param("id"))
		if err != nil || len(status.PreviewCards) == 0 {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
	}
	return c.JSON(http.StatusOK, serializer.PreviewCardFromModel(s.cfg, status.PreviewCards[0]))
}

func statusSnapshotEdit(status models.Status) models.StatusEdit {
	createdAt := status.CreatedAt
	if status.EditedAt.Valid {
		createdAt = status.EditedAt.Time
	}
	sensitive := sql.NullBool{Bool: status.Sensitive, Valid: true}
	pollOptions := models.StringArray{}
	if status.Poll != nil {
		pollOptions = append(pollOptions, status.Poll.Options...)
	}
	media := snapshotMediaAttachments(status)
	mediaIDs := make(models.Int64Array, 0, len(media))
	mediaDescriptions := make(models.StringArray, 0, len(media))
	for _, attachment := range media {
		mediaIDs = append(mediaIDs, attachment.ID)
		mediaDescriptions = append(mediaDescriptions, attachment.Description.String)
	}
	return models.StatusEdit{
		StatusID:                  status.ID,
		AccountID:                 sql.NullInt64{Int64: status.AccountID, Valid: true},
		Text:                      status.Text,
		SpoilerText:               status.SpoilerText,
		CreatedAt:                 createdAt,
		UpdatedAt:                 createdAt,
		OrderedMediaAttachmentIDs: mediaIDs,
		MediaDescriptions:         mediaDescriptions,
		PollOptions:               pollOptions,
		Sensitive:                 sensitive,
		Status:                    status,
		Account:                   status.Account,
	}
}

func snapshotMediaAttachments(status models.Status) []models.MediaAttachment {
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

func orderedEditMediaAttachments(edit models.StatusEdit, media []models.MediaAttachment) []models.MediaAttachment {
	if edit.OrderedMediaAttachmentIDs == nil {
		return mediaAttachmentsSortedByID(media)
	}
	byID := make(map[int64]models.MediaAttachment, len(media))
	for _, attachment := range media {
		byID[attachment.ID] = attachment
	}
	ordered := make([]models.MediaAttachment, 0, len(edit.OrderedMediaAttachmentIDs))
	for index, id := range edit.OrderedMediaAttachmentIDs {
		attachment, ok := byID[id]
		if !ok {
			continue
		}
		if index < len(edit.MediaDescriptions) {
			attachment.Description = sql.NullString{String: edit.MediaDescriptions[index], Valid: true}
		}
		ordered = append(ordered, attachment)
	}
	return ordered
}

func mediaAttachmentsSortedByID(attachments []models.MediaAttachment) []models.MediaAttachment {
	if len(attachments) < 2 {
		return attachments
	}
	out := append([]models.MediaAttachment(nil), attachments...)
	sort.SliceStable(out, func(i int, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Server) statusEditCustomEmojis(edit models.StatusEdit, status models.Status) ([]models.CustomEmoji, error) {
	if s.db == nil {
		return nil, nil
	}
	shortcodes := statusEmbedEmojiShortcodes(statusEditEmojiText(edit))
	if len(shortcodes) == 0 {
		return nil, nil
	}
	var emojis []models.CustomEmoji
	query := customEmojiDomainQuery(s.db.Where("shortcode IN ? AND disabled = false", shortcodes), status.Account.Domain)
	if err := query.Find(&emojis).Error; err != nil {
		return nil, err
	}
	return orderCustomEmojisByShortcode(shortcodes, emojis), nil
}

func statusEditEmojiText(edit models.StatusEdit) string {
	return strings.Join([]string{edit.SpoilerText, edit.Text}, "\n")
}

func orderCustomEmojisByShortcode(shortcodes []string, emojis []models.CustomEmoji) []models.CustomEmoji {
	byShortcode := make(map[string]models.CustomEmoji, len(emojis))
	for _, emoji := range emojis {
		byShortcode[emoji.Shortcode] = emoji
	}
	ordered := make([]models.CustomEmoji, 0, len(emojis))
	for _, shortcode := range shortcodes {
		if emoji, ok := byShortcode[shortcode]; ok {
			ordered = append(ordered, emoji)
		}
	}
	return ordered
}

func (s *Server) pinStatus(c *echo.Context) error {
	return s.toggleStatusPin(c, true)
}

func (s *Server) unpinStatus(c *echo.Context) error {
	return s.toggleStatusPin(c, false)
}

func (s *Server) toggleStatusPin(c *echo.Context, create bool) error {
	account, _, err := s.requireAccountScope(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	status, err := s.findStatus(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if status.AccountID != account.ID {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: You can only pin your own posts")
	}
	if status.ReblogOfID.Valid {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: You cannot pin boosts")
	}
	if status.Visibility == 3 {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: You cannot pin direct messages")
	}

	pinned := false
	changed := false
	if create {
		var count int64
		if err := s.db.Model(&models.StatusPin{}).Where("account_id = ?", account.ID).Count(&count).Error; err != nil {
			return err
		}
		if account.Local() && count > 4 {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: You have reached the pin limit")
		}
		now := time.Now().UTC()
		if err := s.db.Create(&models.StatusPin{AccountID: account.ID, StatusID: status.ID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			if isUniqueConstraintError(err) {
				return apiError(c, http.StatusUnprocessableEntity, "Duplicate record")
			}
			return err
		}
		changed = true
		pinned = true
	} else {
		res := s.db.Where("account_id = ? AND status_id = ?", account.ID, status.ID).Delete(&models.StatusPin{})
		if res.Error != nil {
			return res.Error
		}
		changed = res.RowsAffected > 0
	}
	if changed {
		if create {
			_ = s.deliverActivityPubRawDistribution(status.Account, activityPubAddPinnedStatus(s, *status), nil)
		} else {
			s.invalidateStatusesCleanupLastInspected(c.Request().Context(), account.ID, status.ID, "unpin")
			_ = s.deliverActivityPubRawDistribution(status.Account, activityPubRemovePinnedStatus(s, *status), nil)
		}
	}

	out := statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public")
	out.Pinned = &pinned
	return c.JSON(http.StatusOK, out)
}

func (s *Server) favouritedBy(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil {
		return err
	}
	if _, _, err := s.findVisibleStatusForRequest(c, c.Param("id")); err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	current, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}

	query := s.db.Model(&models.Favourite{}).
		Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Joins("JOIN accounts ON accounts.id = favourites.account_id").
		Where("favourites.status_id = ? AND accounts.suspended_at IS NULL", c.Param("id"))
	query = excludeAccountsFromInteractionList(query, current)
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		query = query.Where("favourites.id < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		query = query.Where("favourites.id > ?", sinceID)
	}
	query = query.Order("favourites.id DESC")

	limitValue := limit(c, 40, 80)
	var rows []models.Favourite
	if err := query.Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	accounts := make([]models.Account, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, row.Account)
	}
	if len(rows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}

func (s *Server) rebloggedBy(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:accounts"); err != nil {
		return err
	}
	if _, _, err := s.findVisibleStatusForRequest(c, c.Param("id")); err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	current, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}

	query := s.db.Model(&models.Status{}).
		Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Joins("JOIN accounts ON accounts.id = statuses.account_id").
		Where("statuses.reblog_of_id = ? AND statuses.deleted_at IS NULL AND statuses.visibility IN ? AND accounts.suspended_at IS NULL", c.Param("id"), []int{0, 1})
	query = excludeAccountsFromInteractionList(query, current)
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		query = query.Where("statuses.id < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		query = query.Where("statuses.id > ?", sinceID)
	}
	query = query.Order("statuses.id DESC")

	limitValue := limit(c, 40, 80)
	var rows []models.Status
	if err := query.Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	accounts := make([]models.Account, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, row.Account)
	}
	if len(rows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}

func (s *Server) currentAccountForOptionalRequestToken(c *echo.Context) (*models.Account, error) {
	if requestToken(c) == "" {
		return nil, nil
	}
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil {
		return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !accessToken.ResourceOwnerID.Valid {
		return nil, nil
	}
	var user models.User
	if err := s.db.Where("id = ?", accessToken.ResourceOwnerID.Int64).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var account models.Account
	if err := accountSerializerPreloads(s.db).Where("id = ?", user.AccountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if err := s.hydrateAccountCustomEmojis(&account); err != nil {
		return nil, err
	}
	return &account, nil
}

func excludeAccountsFromInteractionList(query *gorm.DB, current *models.Account) *gorm.DB {
	if current == nil || current.ID == 0 {
		return query
	}
	return query.
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks interaction_blocks
			WHERE interaction_blocks.account_id = ?
			AND interaction_blocks.target_account_id = accounts.id
		)`, current.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM blocks interaction_blocked_by
			WHERE interaction_blocked_by.target_account_id = ?
			AND interaction_blocked_by.account_id = accounts.id
		)`, current.ID).
		Where(`NOT EXISTS (
			SELECT 1 FROM mutes interaction_mutes
			WHERE interaction_mutes.account_id = ?
			AND interaction_mutes.target_account_id = accounts.id
		)`, current.ID)
}
