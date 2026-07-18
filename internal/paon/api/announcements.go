package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type announcementReactionRow struct {
	Name          string
	CustomEmojiID sql.NullInt64
	Count         int64
	Me            bool
}

const announcementReactionLimit = 8

var (
	supportedUnicodeReactionsOnce sync.Once
	supportedUnicodeReactions     map[string]struct{}
	supportedUnicodeReactionsErr  error
)

func (s *Server) announcements(c *echo.Context) error {
	account, _, err := s.requireAccount(c)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	var announcements []models.Announcement
	if err := s.db.Where("published = ?", true).
		Order("COALESCE(starts_at, scheduled_at, published_at, created_at) ASC").
		Find(&announcements).Error; err != nil {
		return err
	}
	out := make([]serializer.Announcement, 0, len(announcements))
	for _, announcement := range announcements {
		if err := s.hydrateAnnouncementReferences(&announcement); err != nil {
			return err
		}
		read, err := s.announcementRead(account.ID, announcement.ID)
		if err != nil {
			return err
		}
		statuses, err := s.announcementStatuses(announcement)
		if err != nil {
			return err
		}
		reactions, err := s.announcementReactions(account.ID, announcement.ID)
		if err != nil {
			return err
		}
		out = append(out, serializer.AnnouncementFromModel(s.cfg, announcement, &read, statuses, reactions))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) dismissAnnouncement(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:accounts")
	if err != nil {
		return err
	}
	announcement, err := s.findPublishedAnnouncement(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	now := time.Now().UTC()
	mute := models.AnnouncementMute{AccountID: models.AnnouncementMuteAccountID(account.ID), AnnouncementID: models.AnnouncementMuteAnnouncementID(announcement.ID), CreatedAt: now, UpdatedAt: now}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&mute).Error; err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) addAnnouncementReaction(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:favourites")
	if err != nil {
		return err
	}
	announcement, err := s.findPublishedAnnouncement(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	name, err := url.PathUnescape(c.Param("name"))
	if err != nil || name == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	now := time.Now().UTC()
	reaction := models.AnnouncementReaction{
		AccountID:      models.AnnouncementReactionAccountID(account.ID),
		AnnouncementID: models.AnnouncementReactionAnnouncementID(announcement.ID),
		Name:           name,
		CustomEmojiID:  s.announcementReactionCustomEmojiID(name),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.validateAnnouncementReaction(announcement.ID, name, reaction.CustomEmojiID); err != nil {
		return err
	}
	result := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&reaction)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apiError(c, http.StatusUnprocessableEntity, "Duplicate record")
	}
	if !s.enqueueAnnouncementReactionTask(announcement.ID, name) {
		s.broadcastAnnouncementReaction(announcement.ID, name)
	}
	return renderEmpty(c)
}

func (s *Server) removeAnnouncementReaction(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:favourites")
	if err != nil {
		return err
	}
	announcement, err := s.findPublishedAnnouncement(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	name, err := url.PathUnescape(c.Param("name"))
	if err != nil || name == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	result := s.db.Where("account_id = ? AND announcement_id = ? AND name = ?", account.ID, announcement.ID, name).
		Delete(&models.AnnouncementReaction{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !s.enqueueAnnouncementReactionTask(announcement.ID, name) {
		s.broadcastAnnouncementReaction(announcement.ID, name)
	}
	return renderEmpty(c)
}

func (s *Server) findPublishedAnnouncement(id string) (*models.Announcement, error) {
	var announcement models.Announcement
	err := s.db.Where("id = ? AND published = ?", id, true).First(&announcement).Error
	return &announcement, err
}

func (s *Server) announcementRead(accountID int64, announcementID int64) (bool, error) {
	var count int64
	err := s.db.Model(&models.AnnouncementMute{}).Where("account_id = ? AND announcement_id = ?", accountID, announcementID).Count(&count).Error
	return count > 0, err
}

func (s *Server) announcementStatuses(announcement models.Announcement) ([]models.Status, error) {
	if len(announcement.StatusIDs) == 0 {
		return []models.Status{}, nil
	}
	var statuses []models.Status
	err := s.statusQuery().
		Where("statuses.id IN ? AND statuses.visibility IN ? AND statuses.deleted_at IS NULL", []int64(announcement.StatusIDs), []int{0, 1}).
		Find(&statuses).Error
	return statuses, err
}

func (s *Server) announcementReactions(accountID int64, announcementID int64) ([]serializer.ReactionSource, error) {
	var rows []announcementReactionRow
	err := s.db.Model(&models.AnnouncementReaction{}).
		Select(`name, custom_emoji_id, COUNT(*) AS count, EXISTS(SELECT 1 FROM announcement_reactions r WHERE r.account_id = ? AND r.announcement_id = announcement_reactions.announcement_id AND r.name = announcement_reactions.name) AS me`, accountID).
		Where("announcement_id = ?", announcementID).
		Group("name, custom_emoji_id").
		Order("MIN(created_at) ASC").
		Scan(&rows).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	emojiByID, err := s.announcementReactionCustomEmojis(rows)
	if err != nil {
		return nil, err
	}
	out := make([]serializer.ReactionSource, 0, len(rows))
	for _, row := range rows {
		source := serializer.ReactionSource{Name: row.Name, Count: row.Count, Me: row.Me}
		if row.CustomEmojiID.Valid {
			if emoji, ok := emojiByID[row.CustomEmojiID.Int64]; ok {
				serialized := serializer.CustomEmojiFromModel(s.cfg, emoji)
				source.URL = serialized.URL
				source.StaticURL = serialized.StaticURL
			}
		}
		out = append(out, source)
	}
	return out, nil
}

func (s *Server) announcementReactionCustomEmojiID(name string) sql.NullInt64 {
	if s == nil || s.db == nil || name == "" {
		return sql.NullInt64{}
	}
	var emoji models.CustomEmoji
	err := s.db.Where("domain IS NULL AND disabled = ? AND shortcode = ?", false, name).First(&emoji).Error
	if err != nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: emoji.ID, Valid: true}
}

func (s *Server) validateAnnouncementReaction(announcementID int64, name string, customEmojiID sql.NullInt64) error {
	if !customEmojiID.Valid && !supportedUnicodeReactionName(name) {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Name is not a recognized emoji"}
	}
	if s == nil || s.db == nil || announcementID == 0 {
		return nil
	}
	var count int64
	if err := s.db.Model(&models.AnnouncementReaction{}).
		Where("announcement_id = ? AND name <> ?", announcementID, name).
		Distinct("name").
		Count(&count).Error; err != nil {
		return err
	}
	if count >= announcementReactionLimit {
		return apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Limit of different reactions reached"}
	}
	return nil
}

func supportedUnicodeReactionName(name string) bool {
	if name == "" {
		return false
	}
	if reactions, err := loadSupportedUnicodeReactions(); err == nil && len(reactions) > 0 {
		_, ok := reactions[name]
		return ok
	}
	hasEmoji := false
	for _, r := range name {
		switch {
		case r == 0x20e3:
			hasEmoji = true
		case r == 0x200d || r == 0xfe0e || r == 0xfe0f:
		case r == '#' || r == '*' || ('0' <= r && r <= '9'):
		case emojiReactionRune(r):
			hasEmoji = true
		default:
			return false
		}
	}
	return hasEmoji
}

func loadSupportedUnicodeReactions() (map[string]struct{}, error) {
	supportedUnicodeReactionsOnce.Do(func() {
		for _, path := range emojiMapCandidatePaths() {
			data, err := os.ReadFile(path)
			if err != nil {
				supportedUnicodeReactionsErr = err
				continue
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				supportedUnicodeReactionsErr = err
				continue
			}
			supportedUnicodeReactions = make(map[string]struct{}, len(raw))
			for name := range raw {
				supportedUnicodeReactions[name] = struct{}{}
			}
			supportedUnicodeReactionsErr = nil
			return
		}
	})
	return supportedUnicodeReactions, supportedUnicodeReactionsErr
}

func emojiMapCandidatePaths() []string {
	const rel = "app/javascript/mastodon/features/emoji/emoji_map.json"
	paths := []string{rel}
	cwd, err := os.Getwd()
	if err != nil {
		return paths
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		paths = append(paths, filepath.Join(dir, rel))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return paths
}

func emojiReactionRune(r rune) bool {
	return (0x1f000 <= r && r <= 0x1faff) ||
		(0x2600 <= r && r <= 0x27bf) ||
		(0x2300 <= r && r <= 0x23ff) ||
		(0x2b00 <= r && r <= 0x2bff) ||
		(0x2190 <= r && r <= 0x21ff) ||
		(0x1f1e6 <= r && r <= 0x1f1ff)
}

func (s *Server) announcementReactionCustomEmojis(rows []announcementReactionRow) (map[int64]models.CustomEmoji, error) {
	ids := make([]int64, 0, len(rows))
	seen := map[int64]struct{}{}
	for _, row := range rows {
		if !row.CustomEmojiID.Valid {
			continue
		}
		if _, ok := seen[row.CustomEmojiID.Int64]; ok {
			continue
		}
		seen[row.CustomEmojiID.Int64] = struct{}{}
		ids = append(ids, row.CustomEmojiID.Int64)
	}
	if len(ids) == 0 {
		return map[int64]models.CustomEmoji{}, nil
	}
	var emojis []models.CustomEmoji
	if err := s.db.Where("id IN ?", ids).Find(&emojis).Error; err != nil {
		return nil, err
	}
	out := make(map[int64]models.CustomEmoji, len(emojis))
	for _, emoji := range emojis {
		out[emoji.ID] = emoji
	}
	return out, nil
}
