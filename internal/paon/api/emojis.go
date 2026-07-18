package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

func (s *Server) publicEmoji(c *echo.Context) error {
	if s != nil && s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
	id := publicPathWithoutAnyFormat(c.Param("id"))
	emoji, err := s.findPublicEmoji(id)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return activityJSONWithCache(c, activityPubEmoji(s, *emoji), 180)
}

func (s *Server) findPublicEmoji(rawID string) (*models.CustomEmoji, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return nil, gorm.ErrRecordNotFound
	}
	var emoji models.CustomEmoji
	err = s.db.
		Preload("Category").
		Where("id = ?", id).
		Where("domain IS NULL").
		First(&emoji).Error
	return &emoji, err
}

func activityPubEmoji(s *Server, emoji models.CustomEmoji) map[string]any {
	rest := serializer.CustomEmojiFromModel(s.cfg, emoji)
	icon := map[string]any{
		"type":      "Image",
		"mediaType": emoji.ImageContentType.String,
		"url":       rest.URL,
	}
	if !emoji.ImageContentType.Valid || emoji.ImageContentType.String == "" {
		delete(icon, "mediaType")
	}
	out := map[string]any{
		"@context": activityPubEmojiContext(),
		"id":       s.cfg.BaseURL() + "/emojis/" + strconv.FormatInt(emoji.ID, 10),
		"type":     "Emoji",
		"name":     ":" + emoji.Shortcode + ":",
		"icon":     icon,
		"updated":  emoji.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if rest.URL == "" {
		delete(icon, "url")
	}
	return out
}
