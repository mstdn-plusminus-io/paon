package api

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

const mastodon45AntispamAccountAgeExemption = 7 * 24 * time.Hour
const mastodon45AntispamReportComment = "Account automatically reported for posting a banned URL"

func mastodon45AntispamNormalizedText(text string) string {
	return strings.ToLower(norm.NFKC.String(text))
}

func mastodon45AntispamTextContainsAny(text string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func mastodon45AntispamConsidered(text string, recentAccount bool, recentPatterns []string, allTimePatterns []string, anyRecipientFollowsAuthor bool) bool {
	normalized := mastodon45AntispamNormalizedText(text)
	suspiciousText := mastodon45AntispamTextContainsAny(normalized, allTimePatterns) || recentAccount && mastodon45AntispamTextContainsAny(normalized, recentPatterns)
	return suspiciousText && !anyRecipientFollowsAuthor
}

func (s *Server) mastodon45AntispamMembers(ctx context.Context, key string) ([]string, error) {
	if s == nil || !redisEndpointConfigured(s.cfg) {
		return nil, nil
	}
	value, err := s.redisCommand(nonNilContext(ctx), "SMEMBERS", redisConfig(s.cfg).prefix+key)
	if err != nil {
		return nil, err
	}
	items, ok := redisStringArray(value)
	if !ok {
		return nil, errors.New("invalid Redis antispam set response")
	}
	return items, nil
}

func (s *Server) mastodon45StatusMentionAccounts(ctx context.Context, actorID int64, text string) ([]models.Account, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	db := s.db.WithContext(nonNilContext(ctx))
	seen := map[int64]struct{}{}
	accounts := make([]models.Account, 0)
	for _, ref := range statusMentionRefs(text) {
		account, err := s.accountFromMentionRef(db, ref)
		if err != nil {
			return nil, err
		}
		if account == nil || account.ID == actorID || !statusMentionAccountMentionable(account) {
			continue
		}
		blocked, err := statusMentionBlockedByActor(db, actorID, *account)
		if err != nil {
			return nil, err
		}
		if blocked {
			continue
		}
		if _, exists := seen[account.ID]; exists {
			continue
		}
		seen[account.ID] = struct{}{}
		accounts = append(accounts, *account)
	}
	return accounts, nil
}

func mastodon45AntispamRecipientIDs(replyTo *models.Status, mentions []models.Account) []int64 {
	ids := make([]int64, 0, len(mentions)+1)
	if replyTo != nil && replyTo.AccountID != 0 {
		ids = append(ids, replyTo.AccountID)
	}
	for _, account := range mentions {
		if account.ID != 0 {
			ids = append(ids, account.ID)
		}
	}
	return uniqueInt64s(ids)
}

func (s *Server) mastodon45LocalStatusConsideredSpam(ctx context.Context, author models.Account, text string, recipientIDs []int64, now time.Time) (bool, error) {
	recentPatterns, err := s.mastodon45AntispamMembers(ctx, "antispam:spammy_texts")
	if err != nil {
		return false, err
	}
	allTimePatterns, err := s.mastodon45AntispamMembers(ctx, "antispam:all_time_spammy_texts")
	if err != nil {
		return false, err
	}
	if len(recentPatterns) == 0 && len(allTimePatterns) == 0 {
		return false, nil
	}
	anyRecipientFollowsAuthor := false
	if len(recipientIDs) > 0 {
		var count int64
		if err := s.db.WithContext(nonNilContext(ctx)).Model(&models.Follow{}).
			Where("account_id IN ? AND target_account_id = ?", recipientIDs, author.ID).
			Count(&count).Error; err != nil {
			return false, err
		}
		anyRecipientFollowsAuthor = count > 0
	}
	recentAccount := author.CreatedAt.IsZero() || !author.CreatedAt.Before(now.Add(-mastodon45AntispamAccountAgeExemption))
	return mastodon45AntispamConsidered(text, recentAccount, recentPatterns, allTimePatterns, anyRecipientFollowsAuthor), nil
}

func (s *Server) mastodon45CreateAntispamReport(ctx context.Context, target models.Account) error {
	representative, err := s.representativeActivityPubAccount()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	report := models.Report{
		StatusIDs:       models.Int64Array{},
		Comment:         mastodon45AntispamReportComment,
		CreatedAt:       now,
		UpdatedAt:       now,
		AccountID:       representative.ID,
		TargetAccountID: target.ID,
		URI:             s.reportURIForAccount(*representative),
		Forwarded:       sql.NullBool{Bool: false, Valid: true},
		Category:        reportCategoryValue("spam"),
	}
	created := false
	var staffNotificationPayloads []asynqLocalNotificationPayload
	if err := s.db.WithContext(nonNilContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.Report{}).
			Where("account_id = ? AND target_account_id = ? AND category = ? AND action_taken_at IS NULL", representative.ID, target.ID, reportCategoryValue("spam")).
			Count(&count).Error; err != nil || count > 0 {
			return err
		}
		if err := tx.Create(&report).Error; err != nil {
			return err
		}
		created = true
		payloads, err := s.createStaffReportNotificationPayloads(tx, report, *representative)
		staffNotificationPayloads = payloads
		return err
	}); err != nil {
		return err
	}
	if !created {
		return nil
	}
	s.triggerReportCreatedWebhook(report)
	if len(staffNotificationPayloads) > 0 {
		if _, err := s.enqueueOrCreateLocalNotifications(ctx, staffNotificationPayloads); err != nil {
			return err
		}
		_ = s.sendStaffNewReportMails(report)
	}
	return nil
}

func mastodon45DummyStatus(account models.Account, text string, spoilerText string, visibility int, language sql.NullString, quoteApprovalPolicy int, replyTo *models.Status, mentions []models.Account, now time.Time) models.Status {
	status := models.Status{
		ID: mastodonSnowflakeIDAt(now, true), Text: text, SpoilerText: spoilerText,
		CreatedAt: now, UpdatedAt: now, AccountID: account.ID, Account: account,
		Local: sql.NullBool{Bool: true, Valid: true}, Visibility: visibility,
		Language: language, QuoteApprovalPolicy: quoteApprovalPolicy, QuotePolicyCurrentUser: string(quotePolicyAutomatic),
	}
	if replyTo != nil {
		status.InReplyToID = sql.NullInt64{Int64: replyTo.ID, Valid: true}
		status.InReplyToAccountID = railsStatusReplyAccountID(account.ID, replyTo)
		status.Reply = true
	}
	status.StatusStat = models.StatusStat{StatusID: status.ID}
	status.Mentions = make([]models.Mention, 0, len(mentions))
	for i, mentioned := range mentions {
		status.Mentions = append(status.Mentions, models.Mention{
			ID: int64(i + 1), StatusID: models.MentionStatusID(status.ID), AccountID: models.MentionAccountID(mentioned.ID), Account: mentioned,
		})
	}
	return status
}

func (s *Server) mastodon45DummyScheduledStatus(status models.ScheduledStatus) serializer.ScheduledStatus {
	result := serializer.ScheduledStatusFromModel(s.cfg, status)
	result.ID = ""
	return result
}
