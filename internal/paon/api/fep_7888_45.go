package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const activityPubContextDescendantsLimit = 60

func activityPubContextFormatSupported(c *echo.Context) bool {
	format := strings.ToLower(strings.TrimSpace(c.Param("format")))
	return format == "" || format == "json"
}

func (s *Server) assignConversationParentTx(tx *gorm.DB, status models.Status) error {
	if tx == nil || !status.ConversationID.Valid || status.InReplyToID.Valid || status.ReblogOfID.Valid || status.ID == 0 || status.AccountID == 0 {
		return nil
	}
	return tx.Model(&models.Conversation{}).
		Where("id = ? AND parent_status_id IS NULL AND parent_account_id IS NULL", status.ConversationID.Int64).
		Updates(map[string]any{"parent_status_id": status.ID, "parent_account_id": status.AccountID, "updated_at": time.Now().UTC()}).Error
}

func (s *Server) activityPubFEP7888ConversationURI(status models.Status) string {
	if s == nil || s.db == nil || !status.ConversationID.Valid {
		return ""
	}
	var conversation models.Conversation
	if err := s.db.Select("id", "uri", "parent_status_id", "parent_account_id").First(&conversation, status.ConversationID.Int64).Error; err != nil {
		return ""
	}
	if conversation.URI.Valid && strings.TrimSpace(conversation.URI.String) != "" {
		parsed, err := url.Parse(conversation.URI.String)
		if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			return conversation.URI.String
		}
		return ""
	}
	if !conversation.ParentStatusID.Valid || !conversation.ParentAccountID.Valid {
		return ""
	}
	return s.cfg.BaseURL() + "/contexts/" + strconv.FormatInt(conversation.ParentAccountID.Int64, 10) + "-" + strconv.FormatInt(conversation.ParentStatusID.Int64, 10)
}

func parseActivityPubContextID(raw string) (int64, int64, bool) {
	accountRaw, statusRaw, ok := strings.Cut(strings.TrimSpace(raw), "-")
	if !ok || strings.Contains(statusRaw, "-") {
		return 0, 0, false
	}
	accountID, err1 := strconv.ParseInt(accountRaw, 10, 64)
	statusID, err2 := strconv.ParseInt(statusRaw, 10, 64)
	return accountID, statusID, err1 == nil && err2 == nil && accountID > 0 && statusID > 0
}

func (s *Server) activityPubContextConversation(c *echo.Context) (*models.Conversation, error) {
	accountID, statusID, ok := parseActivityPubContextID(activityPubFormatParam(c, "id"))
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	var conversation models.Conversation
	err := s.db.Preload("ParentAccount").
		Where("uri IS NULL AND parent_account_id = ? AND parent_status_id = ?", accountID, statusID).
		First(&conversation).Error
	if err != nil {
		return nil, gorm.ErrRecordNotFound
	}
	return &conversation, nil
}

func (s *Server) activityPubContextStatusURIs(c *echo.Context, conversation models.Conversation) ([]any, string, error) {
	query := s.statusQuery().Where("statuses.conversation_id = ? AND statuses.deleted_at IS NULL AND statuses.visibility IN ?", conversation.ID, []int{0, 1})
	if minID := strings.TrimSpace(c.QueryParam("min_id")); minID != "" {
		query = query.Where("statuses.id > ?", minID)
	}
	var statuses []models.Status
	if err := query.Order("statuses.id ASC").Limit(activityPubContextDescendantsLimit).Find(&statuses).Error; err != nil {
		return nil, "", err
	}
	items := make([]any, 0, len(statuses))
	for _, status := range statuses {
		items = append(items, activityPubStatusURI(s, status))
	}
	next := ""
	if len(statuses) == activityPubContextDescendantsLimit {
		next = s.activityPubContextItemsURL(conversation) + "?page=true&min_id=" + strconv.FormatInt(statuses[len(statuses)-1].ID, 10)
	}
	return items, next, nil
}

func (s *Server) activityPubContextURL(conversation models.Conversation) string {
	return s.cfg.BaseURL() + "/contexts/" + strconv.FormatInt(conversation.ParentAccountID.Int64, 10) + "-" + strconv.FormatInt(conversation.ParentStatusID.Int64, 10)
}

func (s *Server) activityPubContextItemsURL(conversation models.Conversation) string {
	return s.activityPubContextURL(conversation) + "/items"
}

func (s *Server) activityPubContextPage(conversation models.Conversation, items []any, next string, id string) map[string]any {
	page := map[string]any{
		"type": "CollectionPage", "partOf": s.activityPubContextURL(conversation), "items": items,
	}
	if id != "" {
		page["id"] = id
	}
	if next != "" {
		page["next"] = next
	}
	return page
}

func (s *Server) activityPubContextCollection(conversation models.Conversation, items []any, next string) map[string]any {
	out := map[string]any{
		"@context": activityContext(), "id": s.activityPubContextURL(conversation), "type": "Collection",
		"first": s.activityPubContextPage(conversation, items, next, ""),
	}
	// The root account can be deleted while the local conversation remains.
	// Mastodon keeps serving the collection and simply omits attributedTo.
	if conversation.ParentAccount != nil {
		out["attributedTo"] = activityPubAccountTagManagerURI(s, *conversation.ParentAccount)
	}
	return out
}

func (s *Server) activityPubContext(c *echo.Context) error {
	if !activityPubContextFormatSupported(c) {
		return s.activityPubStatusUnsupportedFormat(c)
	}
	s.activityPubAccountVary(c)
	if _, err := s.activityPubSignatureAccountForPublicFetch(c); err != nil {
		return err
	}
	conversation, err := s.activityPubContextConversation(c)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	items, next, err := s.activityPubContextStatusURIs(c, *conversation)
	if err != nil {
		return err
	}
	out := s.activityPubContextCollection(*conversation, items, next)
	return activityJSONWithCachePrivacy(c, out, 180, !s.authorizedFetchMode())
}

func (s *Server) activityPubContextItems(c *echo.Context) error {
	if !activityPubContextFormatSupported(c) {
		return s.activityPubStatusUnsupportedFormat(c)
	}
	s.activityPubAccountVary(c)
	if _, err := s.activityPubSignatureAccountForPublicFetch(c); err != nil {
		return err
	}
	conversation, err := s.activityPubContextConversation(c)
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !truthy(c.QueryParam("page")) {
		firstURL := s.activityPubContextItemsURL(*conversation) + "?page=true"
		return activityJSONWithCachePrivacy(c, map[string]any{
			"@context": activityContext(), "id": s.activityPubContextItemsURL(*conversation), "type": "Collection", "first": firstURL,
		}, 180, !s.authorizedFetchMode())
	}
	items, next, err := s.activityPubContextStatusURIs(c, *conversation)
	if err != nil {
		return err
	}
	id := s.activityPubContextItemsURL(*conversation) + "?page=true"
	if minID := strings.TrimSpace(c.QueryParam("min_id")); minID != "" {
		id += "&min_id=" + url.QueryEscape(minID)
	}
	page := s.activityPubContextPage(*conversation, items, next, id)
	page["@context"] = activityContext()
	return activityJSONWithCachePrivacy(c, page, 180, !s.authorizedFetchMode())
}
