package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var errAccountDomainBlockDuplicate = errors.New("account domain block already exists")

func (s *Server) domainBlocks(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "read", "read:blocks")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")

	query := s.db.Model(&models.AccountDomainBlock{}).Where("account_id = ?", account.ID)
	if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
		query = query.Where("id < ?", maxID)
	}
	if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
		query = query.Where("id > ?", sinceID)
	}
	query = query.Order("id DESC")

	limitValue := limit(c, 100, 200)
	var rows []models.AccountDomainBlock
	if err := query.Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}

	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, accountDomainBlockDisplayDomain(row.Domain))
	}
	if len(rows) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, rows[0].ID, rows[len(rows)-1].ID, "since_id", len(rows) == limitValue))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) createDomainBlock(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "write", "write:blocks")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	rawDomain := domainBlockRequestValue(c)
	if strings.TrimSpace(rawDomain) == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain can't be blank")
	}
	domain, err := normalizeDomainBlockParam(rawDomain)
	if err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain is invalid")
	}

	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var existing models.AccountDomainBlock
		err := tx.Where("account_id = ? AND domain = ?", account.ID, domain).First(&existing).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var count int64
		if err := tx.Model(&models.AccountDomainBlock{}).Where("account_id = ? AND lower(domain) = ?", account.ID, domain).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errAccountDomainBlockDuplicate
		}

		block := models.AccountDomainBlock{AccountID: models.AccountDomainBlockAccountID(account.ID), Domain: models.NullSafeString(domain), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&block).Error; err != nil {
			if isUniqueConstraintError(err) {
				return nil
			}
			return err
		}
		return nil
	})
	if errors.Is(err, errAccountDomainBlockDuplicate) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain has already been taken")
	}
	if err != nil {
		return err
	}
	s.invalidateAccountDomainBlockCaches(c.Request().Context(), account.ID, []string{domain})
	if err := s.enqueueAfterAccountDomainBlockOrRun(c.Request().Context(), account.ID, domain); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) deleteDomainBlock(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "follow", "write", "write:blocks")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	domain, err := normalizeDomainBlockParam(domainBlockRequestValue(c))
	if err != nil {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain is invalid")
	}
	if err := s.db.Where("account_id = ? AND lower(domain) = ?", account.ID, domain).Delete(&models.AccountDomainBlock{}).Error; err != nil {
		return err
	}
	s.invalidateAccountDomainBlockCaches(c.Request().Context(), account.ID, []string{domain})
	return renderEmpty(c)
}

type domainBlockRejectDelivery struct {
	Remote    models.Account
	FollowID  int64
	FollowURI string
}

type domainBlockCleanupResult struct {
	Deliveries              []domainBlockRejectDelivery
	NotificationIDs         []int64
	PrivateStatusAccountIDs []int64
	FollowCacheEffects      []followRelationshipCacheEffect
	RelationshipCaches      []relationshipCacheEffect
	ListUnmerges            []accountBlockUnmerge
}

type followRelationshipCacheEffect struct {
	Source   models.Account
	TargetID int64
}

type relationshipCacheEffect struct {
	AccountID int64
	TargetID  int64
}

func cleanupDomainBlock(tx *gorm.DB, accountID int64, domain string) error {
	_, err := cleanupDomainBlockRecords(tx, accountID, domain)
	return err
}

func (s *Server) enqueueAfterAccountDomainBlockOrRun(ctx context.Context, accountID int64, domain string) error {
	if s != nil && s.enqueueAfterAccountDomainBlockTask(accountID, domain) {
		return nil
	}
	return s.runAfterAccountDomainBlockEffects(ctx, accountID, domain)
}

func (s *Server) runAfterAccountDomainBlockEffects(ctx context.Context, accountID int64, domain string) error {
	if s == nil || s.db == nil || accountID == 0 {
		return nil
	}
	domain, err := normalizeDomainBlockParam(domain)
	if err != nil {
		return nil
	}
	var cleanup domainBlockCleanupResult
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		cleanup, err = cleanupDomainBlockRecords(tx, accountID, domain)
		return err
	}); err != nil {
		return err
	}
	s.publishNotificationIDs(cleanup.NotificationIDs)
	s.clearDomainBlockFeedCaches(ctx, accountID, []string{domain})
	for _, effect := range cleanup.ListUnmerges {
		s.unmergeListFeedsAfterUnfollowBestEffort(ctx, effect.FromAccountID, effect.ListIDs)
	}
	s.invalidateAccountDomainBlockCaches(ctx, accountID, []string{domain})
	for _, effect := range cleanup.FollowCacheEffects {
		s.invalidateFollowRelationshipCaches(ctx, effect.Source, effect.TargetID)
	}
	for _, effect := range cleanup.RelationshipCaches {
		s.invalidateRelationshipCaches(ctx, effect.AccountID, effect.TargetID)
	}
	s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, append(cleanup.PrivateStatusAccountIDs, accountID)...)
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", accountID).First(&account).Error; err == nil {
		s.deliverDomainBlockRejects(account, cleanup.Deliveries)
	}
	return nil
}

func cleanupDomainBlockRecords(tx *gorm.DB, accountID int64, domain string) (domainBlockCleanupResult, error) {
	result := domainBlockCleanupResult{}
	if err := tx.Exec(`
		DELETE FROM notifications
		USING accounts
		WHERE notifications.account_id = ?
		  AND notifications.from_account_id = accounts.id
		  AND lower(accounts.domain) = ?
	`, accountID, domain).Error; err != nil {
		return result, err
	}

	var outgoing []models.Follow
	if err := tx.Model(&models.Follow{}).
		Select("follows.*").
		Joins("JOIN accounts ON accounts.id = follows.target_account_id").
		Where("follows.account_id = ? AND lower(accounts.domain) = ?", accountID, domain).
		Find(&outgoing).Error; err != nil {
		return result, err
	}

	var incoming []models.Follow
	if err := tx.Model(&models.Follow{}).
		Select("follows.*").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id = ? AND lower(accounts.domain) = ?", accountID, domain).
		Find(&incoming).Error; err != nil {
		return result, err
	}
	// Persist the exact severed-edge snapshot before deleting either direction.
	// This is intentionally synchronous and inside the same transaction so a
	// retry cannot observe a partially removed relationship set.
	severanceNotificationID, err := recordUserDomainSeverance(tx, accountID, domain, outgoing, incoming, time.Now().UTC())
	if err != nil {
		return result, err
	}
	if severanceNotificationID != 0 {
		result.NotificationIDs = append(result.NotificationIDs, severanceNotificationID)
	}

	for _, row := range outgoing {
		result.PrivateStatusAccountIDs = append(result.PrivateStatusAccountIDs, row.TargetAccountID)
		result.FollowCacheEffects = append(result.FollowCacheEffects, followRelationshipCacheEffect{Source: models.Account{ID: accountID}, TargetID: row.TargetAccountID})
		listIDs, err := deleteFollowWithAffectedListIDs(tx, row)
		if err != nil {
			return result, err
		}
		result.ListUnmerges = append(result.ListUnmerges, accountBlockUnmerge{FromAccountID: row.TargetAccountID, ListIDs: listIDs})
	}
	for _, row := range incoming {
		remote, err := domainBlockRemoteAccount(tx, row.AccountID)
		if err != nil {
			return result, err
		}
		result.Deliveries = append(result.Deliveries, domainBlockRejectDelivery{Remote: remote, FollowID: row.ID, FollowURI: string(row.URI)})
		result.FollowCacheEffects = append(result.FollowCacheEffects, followRelationshipCacheEffect{Source: remote, TargetID: accountID})
		if err := deleteFollowEdge(tx, row.AccountID, accountID); err != nil {
			return result, err
		}
	}
	if len(incoming) > 0 {
		result.PrivateStatusAccountIDs = append(result.PrivateStatusAccountIDs, accountID)
	}

	var requests []struct {
		AccountID int64  `gorm:"column:account_id"`
		ID        int64  `gorm:"column:id"`
		URI       string `gorm:"column:uri"`
	}
	if err := tx.Model(&models.FollowRequest{}).
		Select("follow_requests.account_id, follow_requests.id, follow_requests.uri").
		Joins("JOIN accounts ON accounts.id = follow_requests.account_id").
		Where("follow_requests.target_account_id = ? AND lower(accounts.domain) = ?", accountID, domain).
		Find(&requests).Error; err != nil {
		return result, err
	}
	for _, row := range requests {
		remote, err := domainBlockRemoteAccount(tx, row.AccountID)
		if err != nil {
			return result, err
		}
		result.Deliveries = append(result.Deliveries, domainBlockRejectDelivery{Remote: remote, FollowID: row.ID, FollowURI: row.URI})
		result.RelationshipCaches = append(result.RelationshipCaches, relationshipCacheEffect{AccountID: row.AccountID, TargetID: accountID})
	}

	if err := tx.Exec(`
		DELETE FROM follow_requests
		USING accounts
		WHERE follow_requests.target_account_id = ?
		  AND follow_requests.account_id = accounts.id
		  AND lower(accounts.domain) = ?
	`, accountID, domain).Error; err != nil {
		return result, err
	}
	return result, nil
}

func (s *Server) clearDomainBlockFeedCaches(ctx context.Context, accountID int64, domains []string) {
	if s == nil || s.db == nil || accountID == 0 || len(domains) == 0 {
		return
	}
	normalized := make([]string, 0, len(domains))
	seen := map[string]struct{}{}
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		normalized = append(normalized, domain)
	}
	if len(normalized) == 0 {
		return
	}

	_ = s.clearHomeFeedCacheContext(ctx, accountID)

	var listIDs []int64
	if err := s.db.WithContext(ctx).Table("lists").
		Select("DISTINCT lists.id").
		Joins("JOIN list_accounts ON list_accounts.list_id = lists.id").
		Joins("JOIN accounts ON accounts.id = list_accounts.account_id").
		Where("lists.account_id = ? AND lower(accounts.domain) IN ?", accountID, normalized).
		Pluck("lists.id", &listIDs).Error; err != nil {
		return
	}
	for _, listID := range listIDs {
		_ = s.clearListFeedCacheContext(ctx, listID)
	}
}

func domainBlockRemoteAccount(tx *gorm.DB, accountID int64) (models.Account, error) {
	var account models.Account
	err := tx.Where("id = ?", accountID).First(&account).Error
	return account, err
}

func (s *Server) deliverDomainBlockRejects(local models.Account, deliveries []domainBlockRejectDelivery) {
	if !local.Local() || !local.PrivateKey.Valid || strings.TrimSpace(local.PrivateKey.String) == "" {
		return
	}
	for _, delivery := range deliveries {
		if delivery.Remote.Local() {
			continue
		}
		followURI := delivery.FollowURI
		if followURI == "" && delivery.FollowID != 0 {
			followURI = activityPubFollowURI(s, delivery.Remote, delivery.FollowID)
		}
		_ = s.deliverActivityPubFollowResponse("Reject", local, delivery.Remote, delivery.FollowID, followURI)
	}
}

func normalizeDomainBlockParam(value string) (string, error) {
	domain := strings.TrimPrefix(strings.TrimSpace(value), "@")
	if strings.ContainsAny(domain, "/?#[]@: \t\r\n") {
		return "", errInvalidDomain
	}
	normalized, err := railsNormalizedHost(domain)
	if err != nil {
		return "", errInvalidDomain
	}
	domain = normalized
	if domain == "" || len(domain) > 253 {
		return "", errInvalidDomain
	}
	return domain, nil
}

func accountDomainBlockDisplayDomain(value any) string {
	raw := ""
	switch v := value.(type) {
	case models.NullSafeString:
		raw = string(v)
	case string:
		raw = v
	default:
		raw = fmt.Sprint(v)
	}
	domain, err := normalizeDomainBlockParam(raw)
	if err == nil {
		return domain
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

func domainBlockRequestValue(c *echo.Context) string {
	if value := c.FormValue("domain"); value != "" {
		return value
	}
	var body struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
		return ""
	}
	return body.Domain
}

type domainBlockParamError struct{}

func (domainBlockParamError) Error() string { return "invalid domain" }

var errInvalidDomain error = domainBlockParamError{}
