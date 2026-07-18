package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

func (s *Server) conversations(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:statuses")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")

	query := s.conversationQuery().
		Where("account_conversations.account_id = ?", account.ID)
	if minID := c.QueryParam("min_id"); queryParamValuePresent(c, "min_id") {
		query = query.Where("account_conversations.last_status_id > ?", minID).Order("account_conversations.last_status_id ASC")
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("account_conversations.last_status_id < ?", maxID)
		}
	} else {
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("account_conversations.last_status_id < ?", maxID)
		}
		if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
			query = query.Where("account_conversations.last_status_id > ?", sinceID)
		}
		query = query.Order("account_conversations.last_status_id DESC")
	}

	limitValue := limit(c, 20, 40)
	var rows []models.AccountConversation
	if err := query.Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	if err := s.hydrateConversationParticipants(rows); err != nil {
		return err
	}
	if err := s.hydrateConversationStatusRelationships(rows, account); err != nil {
		return err
	}

	if len(rows) > 0 {
		first := conversationPaginationID(rows[0])
		last := conversationPaginationID(rows[len(rows)-1])
		if first != 0 && last != 0 {
			c.Response().Header().Set("Link", limitOnlyPaginationLink(c, first, last, "min_id", len(rows) == limitValue))
		}
	}
	return c.JSON(http.StatusOK, serializeConversations(s.cfg, rows, account))
}

func (s *Server) readConversation(c *echo.Context) error {
	return s.setConversationUnread(c, false)
}

func (s *Server) unreadConversation(c *echo.Context) error {
	return s.setConversationUnread(c, true)
}

func (s *Server) setConversationUnread(c *echo.Context, unread bool) error {
	account, _, err := s.requireAccountScope(c, "write", "write:conversations")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	conversation, err := s.findOwnedConversation(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	previousLockVersion := conversation.LockVersion
	conversation.LockVersion++
	result := s.db.Model(&models.AccountConversation{}).
		Where("id = ? AND account_id = ? AND lock_version = ?", conversation.ID, account.ID, previousLockVersion).
		Updates(map[string]any{
			"unread":       unread,
			"lock_version": conversation.LockVersion,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apiError(c, http.StatusInternalServerError, "Attempted to update a stale object: AccountConversation.")
	}
	conversation.Unread = unread
	s.publishConversationIDs(c.Request().Context(), []int64{conversation.ID})
	rows := []models.AccountConversation{*conversation}
	if err := s.hydrateConversationParticipants(rows); err != nil {
		return err
	}
	if err := s.hydrateConversationStatusRelationships(rows, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.ConversationFromModel(s.cfg, rows[0], account))
}

func (s *Server) deleteConversation(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:conversations")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	conversation, err := s.findOwnedConversation(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Delete(conversation).Error; err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) conversationQuery() *gorm.DB {
	return s.db.Model(&models.AccountConversation{}).
		Preload("LastStatus.Account.AccountStat").
		Preload("LastStatus.StatusStat").
		Preload("LastStatus.MediaAttachments").
		Preload("LastStatus.Mentions.Account.AccountStat").
		Preload("LastStatus.Tags").
		Preload("LastStatus.PreviewCards").
		Preload("LastStatus.Reblog.Account.AccountStat").
		Preload("LastStatus.Reblog.StatusStat").
		Preload("LastStatus.Reblog.MediaAttachments").
		Preload("LastStatus.Reblog.Mentions.Account.AccountStat").
		Preload("LastStatus.Reblog.Tags").
		Preload("LastStatus.Reblog.PreviewCards")
}

func (s *Server) findOwnedConversation(accountID int64, id string) (*models.AccountConversation, error) {
	var conversation models.AccountConversation
	err := s.conversationQuery().
		Where("account_conversations.id = ? AND account_conversations.account_id = ?", id, accountID).
		First(&conversation).Error
	return &conversation, err
}

func (s *Server) hydrateConversationParticipants(conversations []models.AccountConversation) error {
	ids := make([]int64, 0)
	seen := map[int64]struct{}{}
	for _, conversation := range conversations {
		for _, id := range conversation.ParticipantAccountIDs {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	var accounts []models.Account
	if err := s.db.Preload("AccountStat").Preload("User.Role").Where("id IN ?", ids).Find(&accounts).Error; err != nil {
		return err
	}
	byID := map[int64]models.Account{}
	for _, account := range accounts {
		byID[account.ID] = account
	}
	for i := range conversations {
		participants := make([]models.Account, 0, len(conversations[i].ParticipantAccountIDs))
		for _, id := range conversations[i].ParticipantAccountIDs {
			if account, ok := byID[id]; ok {
				participants = append(participants, account)
			}
		}
		if len(participants) == 0 {
			var owner models.Account
			if err := s.db.Preload("AccountStat").Preload("User.Role").Where("id = ?", conversations[i].AccountID).First(&owner).Error; err == nil {
				participants = append(participants, owner)
			}
		}
		sort.SliceStable(participants, func(a int, b int) bool { return participants[a].ID < participants[b].ID })
		conversations[i].ParticipantAccounts = participants
	}
	return nil
}

func (s *Server) hydrateConversationStatusRelationships(conversations []models.AccountConversation, account *models.Account) error {
	statuses := make([]models.Status, 0, len(conversations))
	indexes := make([]int, 0, len(conversations))
	for i := range conversations {
		if conversations[i].LastStatus == nil || conversations[i].LastStatus.ID == 0 {
			continue
		}
		statuses = append(statuses, *conversations[i].LastStatus)
		indexes = append(indexes, i)
	}
	if len(statuses) == 0 {
		return nil
	}
	if err := s.hydrateStatusRelationships(statuses, account); err != nil {
		return err
	}
	for i, conversationIndex := range indexes {
		conversations[conversationIndex].LastStatus = &statuses[i]
	}
	return nil
}

func serializeConversations(cfg config.Config, conversations []models.AccountConversation, current *models.Account) []serializer.Conversation {
	out := make([]serializer.Conversation, 0, len(conversations))
	for _, conversation := range conversations {
		out = append(out, serializer.ConversationFromModel(cfg, conversation, current))
	}
	return out
}

func conversationPaginationID(conversation models.AccountConversation) int64 {
	if conversation.LastStatusID.Valid {
		return conversation.LastStatusID.Int64
	}
	return 0
}

func (s *Server) ensureStatusConversation(tx *gorm.DB, status *models.Status, now time.Time) error {
	if status == nil || status.ConversationID.Valid {
		return nil
	}
	if status.InReplyToID.Valid {
		var reply models.Status
		if err := tx.Select("id", "conversation_id").Where("id = ?", status.InReplyToID.Int64).First(&reply).Error; err == nil && reply.ConversationID.Valid {
			status.ConversationID = reply.ConversationID
			return nil
		}
	}
	conversation := models.Conversation{CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&conversation).Error; err != nil {
		return err
	}
	status.ConversationID = sql.NullInt64{Int64: conversation.ID, Valid: true}
	if status.InReplyToID.Valid {
		return tx.Model(&models.Status{}).Where("id = ? AND conversation_id IS NULL", status.InReplyToID.Int64).Update("conversation_id", conversation.ID).Error
	}
	return nil
}

func (s *Server) railsStatusReplyTarget(replyTo *models.Status) (*models.Status, error) {
	if replyTo == nil {
		return nil, nil
	}
	if !replyTo.ReblogOfID.Valid {
		return replyTo, nil
	}
	if replyTo.Reblog != nil && replyTo.Reblog.ID != 0 {
		return replyTo.Reblog, nil
	}
	if s == nil || s.db == nil {
		return replyTo, nil
	}
	reblog, err := s.findStatus(strconv.FormatInt(replyTo.ReblogOfID.Int64, 10))
	if err != nil {
		return nil, err
	}
	return reblog, nil
}

func railsStatusReplyAccountID(statusAccountID int64, thread *models.Status) sql.NullInt64 {
	if thread == nil || thread.ID == 0 {
		return sql.NullInt64{}
	}
	if thread.AccountID == statusAccountID && thread.Reply {
		return thread.InReplyToAccountID
	}
	return sql.NullInt64{Int64: thread.AccountID, Valid: true}
}

func (s *Server) addDirectStatusToConversations(tx *gorm.DB, status models.Status, mentioned []models.Account) ([]int64, error) {
	if status.Visibility != 3 || !status.ConversationID.Valid || status.ID == 0 {
		return nil, nil
	}
	author := status.Account
	if author.ID == 0 {
		if err := tx.Select("id", "domain").Where("id = ?", status.AccountID).First(&author).Error; err != nil {
			return nil, err
		}
	}
	if mentioned == nil {
		if err := tx.Model(&models.Mention{}).
			Select("accounts.id, accounts.domain").
			Joins("JOIN accounts ON accounts.id = mentions.account_id").
			Where("mentions.status_id = ? AND mentions.silent = false", status.ID).
			Find(&mentioned).Error; err != nil {
			return nil, err
		}
	}
	participantSet := map[int64]struct{}{status.AccountID: {}}
	recipientSet := map[int64]struct{}{}
	if author.Local() {
		recipientSet[author.ID] = struct{}{}
	}
	for _, account := range mentioned {
		participantSet[account.ID] = struct{}{}
		if account.Local() {
			recipientSet[account.ID] = struct{}{}
		}
	}
	if len(recipientSet) == 0 {
		return nil, nil
	}

	updated := make([]int64, 0, len(recipientSet))
	recipients := mapKeys(recipientSet)
	sort.Slice(recipients, func(i, j int) bool { return recipients[i] < recipients[j] })
	for _, recipientID := range recipients {
		participants := conversationParticipantIDs(participantSet, recipientID)
		changed, conversationID, err := upsertAccountConversationForStatus(tx, recipientID, status, participants)
		if err != nil {
			return updated, err
		}
		if changed {
			updated = append(updated, conversationID)
		}
	}
	return updated, nil
}

func conversationParticipantIDs(participantSet map[int64]struct{}, recipientID int64) models.Int64Array {
	participants := make(models.Int64Array, 0, len(participantSet))
	for id := range participantSet {
		if id != recipientID {
			participants = append(participants, id)
		}
	}
	sort.Slice(participants, func(i, j int) bool { return participants[i] < participants[j] })
	return participants
}

func upsertAccountConversationForStatus(tx *gorm.DB, accountID int64, status models.Status, participants models.Int64Array) (bool, int64, error) {
	var conversation models.AccountConversation
	err := tx.Where("account_id = ? AND conversation_id = ? AND participant_account_ids = ?", accountID, status.ConversationID.Int64, participants).First(&conversation).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, 0, err
	}
	unread := status.AccountID != accountID
	if errors.Is(err, gorm.ErrRecordNotFound) {
		conversation = models.AccountConversation{
			AccountID:             models.AccountConversationAccountID(accountID),
			ConversationID:        models.AccountConversationConversationID(status.ConversationID.Int64),
			ParticipantAccountIDs: participants,
			StatusIDs:             models.Int64Array{status.ID},
			LastStatusID:          sql.NullInt64{Int64: status.ID, Valid: true},
			Unread:                unread,
		}
		if err := tx.Create(&conversation).Error; err != nil {
			return false, 0, err
		}
		return true, conversation.ID, nil
	}
	for _, id := range conversation.StatusIDs {
		if id == status.ID {
			return false, conversation.ID, nil
		}
	}
	conversation.StatusIDs = append(conversation.StatusIDs, status.ID)
	sort.Slice(conversation.StatusIDs, func(i, j int) bool { return conversation.StatusIDs[i] < conversation.StatusIDs[j] })
	lastStatusID := conversation.StatusIDs[len(conversation.StatusIDs)-1]
	if err := tx.Model(&models.AccountConversation{}).Where("id = ?", conversation.ID).Updates(map[string]any{
		"status_ids":     conversation.StatusIDs,
		"last_status_id": lastStatusID,
		"unread":         unread,
		"lock_version":   gorm.Expr("lock_version + 1"),
	}).Error; err != nil {
		return false, 0, err
	}
	return true, conversation.ID, nil
}

func (s *Server) publishConversationIDs(ctx context.Context, ids []int64) {
	for _, id := range ids {
		if !s.enqueuePushConversationTask(id) {
			s.publishConversation(ctx, id)
		}
	}
}

func (s *Server) publishConversation(ctx context.Context, id int64) {
	if id == 0 || s == nil || s.db == nil {
		return
	}
	var conversation models.AccountConversation
	if err := s.conversationQuery().Where("account_conversations.id = ?", id).First(&conversation).Error; err != nil {
		return
	}
	if !conversation.AccountID.Valid {
		return
	}
	accountID := strconv.FormatInt(conversation.AccountID.Int64, 10)
	if value, err := s.redisCommand(ctx, "EXISTS", redisConfig(s.cfg).prefix+"subscribed:timeline:direct:"+accountID); err != nil || value == int64(0) {
		return
	}
	rows := []models.AccountConversation{conversation}
	if err := s.hydrateConversationParticipants(rows); err != nil {
		return
	}
	var current models.Account
	currentPtr := (*models.Account)(nil)
	if err := accountSerializerPreloads(s.db).Where("id = ?", conversation.AccountID.Int64).First(&current).Error; err == nil {
		currentPtr = &current
		_ = s.hydrateConversationStatusRelationships(rows, currentPtr)
	}
	payload, err := json.Marshal(map[string]any{
		"event":   "conversation",
		"payload": serializer.ConversationFromModel(s.cfg, rows[0], currentPtr),
	})
	if err != nil {
		return
	}
	_, _ = s.redisCommand(ctx, "PUBLISH", redisConfig(s.cfg).prefix+"timeline:direct:"+accountID, string(payload))
}
