package api

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

var meiliHTMLTagPattern = regexp.MustCompile(`<[^>]+>`)

type meiliStatusDocument struct {
	ID                 int64    `json:"id"`
	AccountID          int64    `json:"account_id"`
	InReplyToID        *int64   `json:"in_reply_to_id"`
	ReblogOfID         *int64   `json:"reblog_of_id"`
	Language           *string  `json:"language"`
	Sensitive          bool     `json:"sensitive"`
	Text               string   `json:"text"`
	Tags               []string `json:"tags"`
	Visibility         string   `json:"visibility"`
	SearchableBy       []int64  `json:"searchable_by"`
	HasMedia           bool     `json:"has_media"`
	HasImage           bool     `json:"has_image"`
	HasVideo           bool     `json:"has_video"`
	HasPoll            bool     `json:"has_poll"`
	HasLink            bool     `json:"has_link"`
	HasEmbed           bool     `json:"has_embed"`
	IsReply            bool     `json:"is_reply"`
	CreatedAtTimestamp int64    `json:"created_at_timestamp"`
	FavouritesCount    int64    `json:"favourites_count"`
	ReblogsCount       int64    `json:"reblogs_count"`
	RepliesCount       int64    `json:"replies_count"`
}

type meiliTagDocument struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Trendable     bool   `json:"trendable"`
	Reviewed      bool   `json:"reviewed"`
	Usage         int64  `json:"usage"`
	AccountsCount int64  `json:"accounts_count"`
	LastStatusAt  int64  `json:"last_status_at"`
}

type meiliAccountDocument struct {
	ID                 int64   `json:"id"`
	Username           string  `json:"username"`
	DisplayName        string  `json:"display_name"`
	Domain             *string `json:"domain"`
	Bot                bool    `json:"bot"`
	Locked             bool    `json:"locked"`
	Discoverable       bool    `json:"discoverable"`
	Indexable          bool    `json:"indexable"`
	Text               string  `json:"text,omitempty"`
	FollowersCount     int64   `json:"followers_count"`
	FollowingCount     int64   `json:"following_count"`
	StatusesCount      int64   `json:"statuses_count"`
	LastStatusAt       int64   `json:"last_status_at"`
	CreatedAtTimestamp int64   `json:"created_at_timestamp"`
}

type meiliInstanceDocument struct {
	ID            string `json:"id"`
	Domain        string `json:"domain"`
	AccountsCount int64  `json:"accounts_count"`
}

func (s *Server) meiliIndexInstanceBestEffort(ctx context.Context, domain string) {
	if !s.cfg.MeiliEnabled || s.db == nil {
		return
	}
	domain = normalizeDomain(domain)
	if domain == "" {
		return
	}
	var instance models.Instance
	if err := s.db.WithContext(ctx).Where("domain = ?", domain).First(&instance).Error; err != nil {
		_ = s.meiliDeleteDocumentByID(ctx, "instances", domain)
		return
	}
	if !meiliInstanceSearchable(instance) {
		_ = s.meiliDeleteDocumentByID(ctx, "instances", domain)
		return
	}
	document := meiliInstanceDocument{
		ID:            instance.Domain,
		Domain:        instance.Domain,
		AccountsCount: instance.AccountsCount,
	}
	_ = s.meiliUpsertDocuments(ctx, "instances", []meiliInstanceDocument{document})
}

func (s *Server) meiliIndexAccountBestEffort(ctx context.Context, accountID int64) {
	if !s.cfg.MeiliEnabled || s.db == nil {
		return
	}
	account, err := s.findAccountByID(intString(accountID))
	if err != nil {
		_ = s.meiliDeleteDocument(ctx, "accounts", accountID)
		return
	}
	if !meiliAccountSearchable(*account) {
		_ = s.meiliDeleteDocument(ctx, "accounts", accountID)
		return
	}
	document := s.meiliAccountDocument(*account)
	_ = s.meiliUpsertDocuments(ctx, "accounts", []meiliAccountDocument{document})
}

func (s *Server) meiliIndexStatusBestEffort(ctx context.Context, statusID int64) {
	if !s.cfg.MeiliEnabled || s.db == nil {
		return
	}
	status, err := s.findStatusByID(statusID)
	if err != nil || status == nil {
		_ = s.meiliDeleteDocument(ctx, "statuses", statusID)
		return
	}
	if !s.meiliStatusIndexable(ctx, *status) {
		_ = s.meiliDeleteDocument(ctx, "statuses", statusID)
		return
	}
	document := s.meiliStatusDocument(*status)
	_ = s.meiliUpsertDocuments(ctx, "statuses", []meiliStatusDocument{document})
}

func (s *Server) meiliDeleteStatusBestEffort(ctx context.Context, statusID int64) {
	if !s.cfg.MeiliEnabled {
		return
	}
	_ = s.meiliDeleteDocument(ctx, "statuses", statusID)
}

func (s *Server) meiliReindexPrivateStatusesForAccountsBestEffort(ctx context.Context, accountIDs ...int64) {
	if !s.cfg.MeiliEnabled || s.db == nil || len(accountIDs) == 0 {
		return
	}
	for _, accountID := range meiliUniqueInt64s(accountIDs) {
		s.meiliReindexPrivateStatusesForAccountBestEffort(ctx, accountID)
	}
}

func (s *Server) meiliReindexPrivateStatusesForAccountBestEffort(ctx context.Context, accountID int64) {
	lastID := int64(0)
	for {
		var ids []int64
		query := s.db.WithContext(ctx).
			Model(&models.Status{}).
			Where("account_id = ? AND visibility = ? AND deleted_at IS NULL AND id > ?", accountID, 2, lastID).
			Order("id ASC").
			Limit(100)
		if s.cfg.MeiliLibraryOnly {
			query = query.Where(meiliLibraryOnlyStatusSQL())
		}
		if err := query.Pluck("id", &ids).Error; err != nil {
			return
		}
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			lastID = id
			s.meiliIndexStatusBestEffort(ctx, id)
		}
	}
}

func (s *Server) meiliIndexTagsBestEffort(ctx context.Context, tagIDs []int64) {
	if !s.cfg.MeiliEnabled || s.db == nil || len(tagIDs) == 0 {
		return
	}
	seen := map[int64]struct{}{}
	for _, id := range tagIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		var tag models.Tag
		if err := s.db.Where("id = ?", id).First(&tag).Error; err != nil {
			_ = s.meiliDeleteDocument(ctx, "tags", id)
			continue
		}
		if !meiliTagListable(tag) {
			_ = s.meiliDeleteDocument(ctx, "tags", id)
			continue
		}
		document := s.meiliTagDocument(tag)
		_ = s.meiliUpsertDocuments(ctx, "tags", []meiliTagDocument{document})
	}
}

func (s *Server) meiliAccountDocument(account models.Account) meiliAccountDocument {
	lastStatusAt := int64(0)
	if account.AccountStat.LastStatusAt.Valid {
		lastStatusAt = account.AccountStat.LastStatusAt.Time.Unix()
	}
	text := ""
	if account.Discoverable.Valid && account.Discoverable.Bool {
		text = plainMeiliText(account.Note)
	}
	return meiliAccountDocument{
		ID:                 account.ID,
		Username:           accountSearchableUsername(account),
		DisplayName:        account.DisplayName,
		Domain:             stringPtrFromNull(account.Domain),
		Bot:                account.ActorType.Valid && strings.EqualFold(account.ActorType.String, "Service"),
		Locked:             account.Locked,
		Discoverable:       account.Discoverable.Valid && account.Discoverable.Bool,
		Indexable:          account.Indexable,
		Text:               text,
		FollowersCount:     account.AccountStat.FollowersCount,
		FollowingCount:     account.AccountStat.FollowingCount,
		StatusesCount:      account.AccountStat.StatusesCount,
		LastStatusAt:       lastStatusAt,
		CreatedAtTimestamp: account.CreatedAt.Unix(),
	}
}

func accountSearchableUsername(account models.Account) string {
	if account.Domain.Valid && strings.TrimSpace(account.Domain.String) != "" {
		return account.Username + "@" + strings.TrimSpace(account.Domain.String)
	}
	return account.Username
}

func (s *Server) meiliStatusDocument(status models.Status) meiliStatusDocument {
	tags := make([]string, 0, len(status.Tags))
	for _, tag := range status.Tags {
		tags = append(tags, tag.Name)
	}
	textParts := []string{plainMeiliText(status.Text), status.SpoilerText}
	if status.Poll != nil {
		textParts = append(textParts, status.Poll.Options...)
	}
	searchableBy := []int64{}
	if status.Visibility == 2 {
		searchableBy = s.meiliFollowerAccountIDs(status.AccountID)
		searchableBy = append(searchableBy, status.AccountID)
	}
	if status.Visibility == 3 {
		for _, mention := range status.Mentions {
			if mention.AccountID.Valid {
				searchableBy = append(searchableBy, mention.AccountID.Int64)
			}
		}
		searchableBy = append(searchableBy, status.AccountID)
	}
	hasImage := false
	hasVideo := false
	for _, attachment := range status.MediaAttachments {
		if attachment.Type == 0 {
			hasImage = true
		}
		if attachment.Type == 1 || attachment.Type == 2 {
			hasVideo = true
		}
	}
	hasEmbed := false
	for _, card := range status.PreviewCards {
		if card.Type == 2 || strings.TrimSpace(card.HTML) != "" {
			hasEmbed = true
			break
		}
	}
	return meiliStatusDocument{
		ID:                 status.ID,
		AccountID:          status.AccountID,
		InReplyToID:        int64PtrFromNull(status.InReplyToID),
		ReblogOfID:         int64PtrFromNull(status.ReblogOfID),
		Language:           stringPtrFromNull(status.Language),
		Sensitive:          status.Sensitive,
		Text:               strings.TrimSpace(strings.Join(nonEmptyStrings(textParts), "\n\n")),
		Tags:               tags,
		Visibility:         meiliVisibilityName(status.Visibility),
		SearchableBy:       meiliUniqueInt64s(searchableBy),
		HasMedia:           len(status.MediaAttachments) > 0,
		HasImage:           hasImage,
		HasVideo:           hasVideo,
		HasPoll:            status.Poll != nil || status.PollID.Valid,
		HasLink:            len(status.PreviewCards) > 0,
		HasEmbed:           hasEmbed,
		IsReply:            status.InReplyToID.Valid,
		CreatedAtTimestamp: status.CreatedAt.Unix(),
		FavouritesCount:    status.StatusStat.FavouritesCount,
		ReblogsCount:       status.StatusStat.ReblogsCount,
		RepliesCount:       status.StatusStat.RepliesCount,
	}
}

func (s *Server) meiliTagDocument(tag models.Tag) meiliTagDocument {
	now := time.Now().UTC()
	start := dayStart(now.AddDate(0, 0, -7))
	end := dayStart(now).AddDate(0, 0, 1)
	usage := s.adminTagUsesTotal(adminMetricKeyParam{ID: intString(tag.ID)}, start, end)
	accounts := s.adminTagAccountsTotal(adminMetricKeyParam{ID: intString(tag.ID)}, start, end)
	lastStatusAt := int64(0)
	if tag.LastStatusAt.Valid {
		lastStatusAt = tag.LastStatusAt.Time.Unix()
	}
	return meiliTagDocument{
		ID:            tag.ID,
		Name:          tag.Name,
		Trendable:     tag.Trendable.Valid && tag.Trendable.Bool,
		Reviewed:      tag.ReviewedAt.Valid,
		Usage:         usage,
		AccountsCount: accounts,
		LastStatusAt:  lastStatusAt,
	}
}

func (s *Server) meiliFollowerAccountIDs(accountID int64) []int64 {
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	if s.db == nil {
		return []int64{}
	}
	if err := s.db.Table("follows").Select("account_id AS id").Where("target_account_id = ?", accountID).Find(&rows).Error; err != nil {
		return []int64{}
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func meiliStatusSearchable(status models.Status) bool {
	return !status.DeletedAt.Valid && (status.Visibility == 0 || status.Visibility == 1)
}

func (s *Server) meiliStatusIndexable(ctx context.Context, status models.Status) bool {
	if !meiliStatusSearchable(status) {
		return false
	}
	if !s.cfg.MeiliLibraryOnly {
		return true
	}
	if meiliStatusLocal(status) {
		return true
	}
	return s.meiliRemoteStatusHasLocalInteraction(ctx, status.ID)
}

func meiliStatusLocal(status models.Status) bool {
	if status.Local.Valid {
		return status.Local.Bool
	}
	if status.URI.Valid && strings.TrimSpace(status.URI.String) != "" {
		return false
	}
	if status.Account.ID != 0 {
		return status.Account.Local()
	}
	return true
}

func (s *Server) meiliRemoteStatusHasLocalInteraction(ctx context.Context, statusID int64) bool {
	if s.db == nil || statusID == 0 {
		return false
	}
	var count int64
	err := s.db.WithContext(ctx).
		Table("statuses").
		Where("statuses.id = ?", statusID).
		Where(meiliLibraryOnlyStatusSQL()).
		Count(&count).Error
	return err == nil && count > 0
}

func meiliLibraryOnlyStatusSQL() string {
	return `(
		COALESCE(statuses.local, statuses.uri IS NULL) = TRUE
		OR EXISTS (
			SELECT 1 FROM favourites
			INNER JOIN accounts favourite_accounts ON favourite_accounts.id = favourites.account_id
			WHERE favourites.status_id = statuses.id
			  AND (favourite_accounts.domain IS NULL OR favourite_accounts.domain = '')
		)
		OR EXISTS (
			SELECT 1 FROM bookmarks
			INNER JOIN accounts bookmark_accounts ON bookmark_accounts.id = bookmarks.account_id
			WHERE bookmarks.status_id = statuses.id
			  AND (bookmark_accounts.domain IS NULL OR bookmark_accounts.domain = '')
		)
		OR EXISTS (
			SELECT 1 FROM statuses reblogs
			INNER JOIN accounts reblog_accounts ON reblog_accounts.id = reblogs.account_id
			WHERE reblogs.reblog_of_id = statuses.id
			  AND reblogs.deleted_at IS NULL
			  AND (reblog_accounts.domain IS NULL OR reblog_accounts.domain = '')
		)
		OR EXISTS (
			SELECT 1 FROM mentions
			INNER JOIN accounts mention_accounts ON mention_accounts.id = mentions.account_id
			WHERE mentions.status_id = statuses.id
			  AND (mention_accounts.domain IS NULL OR mention_accounts.domain = '')
		)
	)`
}

func meiliTagListable(tag models.Tag) bool {
	return tag.Listable.Valid && tag.Listable.Bool
}

func meiliAccountSearchable(account models.Account) bool {
	return !account.SuspendedAt.Valid && !account.MovedToAccountID.Valid && account.Discoverable.Valid && account.Discoverable.Bool
}

func meiliInstanceSearchable(instance models.Instance) bool {
	return strings.TrimSpace(instance.Domain) != ""
}

func meiliVisibilityName(value int) string {
	switch value {
	case 0:
		return "public"
	case 1:
		return "unlisted"
	case 2:
		return "private"
	case 3:
		return "direct"
	default:
		return "public"
	}
}

func plainMeiliText(value string) string {
	return strings.TrimSpace(meiliHTMLTagPattern.ReplaceAllString(value, " "))
}

func int64PtrFromNull(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func stringPtrFromNull(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func meiliUniqueInt64s(values []int64) []int64 {
	out := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
