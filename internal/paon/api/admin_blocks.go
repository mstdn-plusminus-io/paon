package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"golang.org/x/net/idna"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type adminDomainBlockPayload struct {
	Domain            string `json:"domain" form:"domain"`
	Severity          string `json:"severity" form:"severity"`
	RejectMedia       *bool  `json:"reject_media" form:"reject_media"`
	RejectReports     *bool  `json:"reject_reports" form:"reject_reports"`
	PrivateComment    string `json:"private_comment" form:"private_comment"`
	PublicComment     string `json:"public_comment" form:"public_comment"`
	Obfuscate         *bool  `json:"obfuscate" form:"obfuscate"`
	DomainSet         bool   `json:"-" form:"-"`
	SeveritySet       bool   `json:"-" form:"-"`
	PrivateCommentSet bool   `json:"-" form:"-"`
	PublicCommentSet  bool   `json:"-" form:"-"`
}

type adminIPBlockPayload struct {
	IP           string `json:"ip" form:"ip"`
	Severity     string `json:"severity" form:"severity"`
	Comment      string `json:"comment" form:"comment"`
	ExpiresIn    string `json:"expires_in" form:"expires_in"`
	IPSet        bool   `json:"-" form:"-"`
	SeveritySet  bool   `json:"-" form:"-"`
	CommentSet   bool   `json:"-" form:"-"`
	ExpiresInSet bool   `json:"-" form:"-"`
}

type adminDomainPayload struct {
	Domain string `json:"domain" form:"domain"`
}

type adminCanonicalEmailBlockPayload struct {
	CanonicalEmailHash string `json:"canonical_email_hash" form:"canonical_email_hash"`
	Email              string `json:"email" form:"email"`
}

func canonicalEmailBlockForAccount(account models.Account, now time.Time) (models.CanonicalEmailBlock, bool) {
	if !account.Local() || account.User.ID == 0 || strings.TrimSpace(account.User.Email) == "" {
		return models.CanonicalEmailBlock{}, false
	}
	return models.CanonicalEmailBlock{
		CanonicalEmailHash: canonicalEmailHash(account.User.Email),
		ReferenceAccountID: sql.NullInt64{Int64: account.ID, Valid: true},
		CreatedAt:          now,
		UpdatedAt:          now,
	}, true
}

func createCanonicalEmailBlockForAccountTx(tx *gorm.DB, account models.Account, now time.Time) error {
	row, ok := canonicalEmailBlockForAccount(account, now)
	if !ok {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func destroyCanonicalEmailBlocksForAccountTx(tx *gorm.DB, accountID int64) error {
	return tx.Where("reference_account_id = ?", accountID).Delete(&models.CanonicalEmailBlock{}).Error
}

func (s *Server) adminDomainAllows(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:domain_allows"); err != nil {
		return err
	}
	var rows []models.DomainAllow
	limitValue := limit(c, 100, 200)
	if err := adminPage(c, s.db.Model(&models.DomainAllow{}), "domain_allows.id").Order("domain_allows.id DESC").Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(rows)
	}
	setLinkForRows(c, rows, func(row models.DomainAllow) int64 { return row.ID }, limitValue)
	out := make([]serializer.AdminDomainAllow, 0, len(rows))
	for _, row := range rows {
		out = append(out, serializer.AdminDomainAllowFromModel(row))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showAdminDomainAllow(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:domain_allows"); err != nil {
		return err
	}
	var row models.DomainAllow
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.AdminDomainAllowFromModel(row))
}

func (s *Server) createAdminDomainAllow(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:domain_allows")
	if err != nil {
		return err
	}
	payload, err := parseAdminDomainPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	domain := normalizeDomain(payload.Domain)
	if domain == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain is invalid")
	}
	var row models.DomainAllow
	created := false
	err = s.db.Where("domain = ?", domain).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		now := time.Now().UTC()
		row = models.DomainAllow{Domain: domain, CreatedAt: now, UpdatedAt: now}
		err = s.db.Create(&row).Error
		created = err == nil
	}
	if err != nil {
		return err
	}
	if created {
		if err := logAdminAction(s.db, user.AccountID, "create", domainAllowAuditLogTarget(row), row.CreatedAt); err != nil {
			return err
		}
		if err := s.materializeDomainControlMutation(c.Request().Context(), row.Domain); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, serializer.AdminDomainAllowFromModel(row))
}

func (s *Server) deleteAdminDomainAllow(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:domain_allows")
	if err != nil {
		return err
	}
	var row models.DomainAllow
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.applyAdminDomainUnallowEffects(s.db, row.Domain); err != nil {
		return err
	}
	if err := s.db.Delete(&models.DomainAllow{}, row.ID).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := logAdminAction(s.db, user.AccountID, "destroy", domainAllowAuditLogTarget(row), time.Now().UTC()); err != nil {
		return err
	}
	if err := s.refreshDomainControlMutation(c.Request().Context(), row.Domain); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) adminDomainBlocks(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:domain_blocks"); err != nil {
		return err
	}
	var rows []models.DomainBlock
	limitValue := limit(c, 100, 200)
	if err := adminPage(c, s.db.Model(&models.DomainBlock{}), "domain_blocks.id").Order("domain_blocks.id DESC").Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(rows)
	}
	setLinkForRows(c, rows, func(row models.DomainBlock) int64 { return row.ID }, limitValue)
	out := make([]serializer.AdminDomainBlock, 0, len(rows))
	for _, row := range rows {
		out = append(out, serializer.AdminDomainBlockFromModel(row))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showAdminDomainBlock(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:domain_blocks"); err != nil {
		return err
	}
	var row models.DomainBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.AdminDomainBlockFromModel(row))
}

func (s *Server) createAdminDomainBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:domain_blocks")
	if err != nil {
		return err
	}
	payload, err := parseAdminDomainBlockPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	domain := normalizeDomain(payload.Domain)
	if domain == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain is invalid")
	}
	now := time.Now().UTC()
	row := models.DomainBlock{
		Domain:         domain,
		Severity:       domainBlockSeverityValue(firstNonEmpty(payload.Severity, "silence")),
		RejectMedia:    boolValue(payload.RejectMedia),
		RejectReports:  boolValue(payload.RejectReports),
		PrivateComment: optionalCommentString(payload.PrivateComment, payload.PrivateCommentSet),
		PublicComment:  optionalCommentString(payload.PublicComment, payload.PublicCommentSet),
		Obfuscate:      boolValue(payload.Obfuscate),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	existing, err := s.existingDomainBlockForDomain(domain)
	if err != nil {
		return err
	}
	if existing != nil && (existing.Domain == domain || !domainBlockStricterThan(row, *existing)) {
		return c.JSON(http.StatusUnprocessableEntity, existingDomainBlockError(*existing, s.webLocale(c, user)))
	}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain has already been taken")
		}
		return err
	}
	if err := logAdminAction(s.db, user.AccountID, "create", domainBlockAuditLogTarget(row), now); err != nil {
		return err
	}
	if err := s.enqueueAdminDomainBlockEffectsOrApply(s.db, row, false); err != nil {
		return err
	}
	if err := s.materializeDomainControlMutation(c.Request().Context(), row.Domain); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.AdminDomainBlockFromModel(row))
}

func (s *Server) updateAdminDomainBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:domain_blocks")
	if err != nil {
		return err
	}
	var row models.DomainBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	payload, err := parseAdminDomainBlockPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if payload.SeveritySet {
		updates["severity"] = domainBlockSeverityValue(payload.Severity)
	}
	if payload.RejectMedia != nil {
		updates["reject_media"] = *payload.RejectMedia
	}
	if payload.RejectReports != nil {
		updates["reject_reports"] = *payload.RejectReports
	}
	if payload.PrivateCommentSet {
		updates["private_comment"] = payload.PrivateComment
	}
	if payload.PublicCommentSet {
		updates["public_comment"] = payload.PublicComment
	}
	if payload.Obfuscate != nil {
		updates["obfuscate"] = *payload.Obfuscate
	}
	if err := s.db.Model(&row).Updates(updates).Error; err != nil {
		return err
	}
	_ = s.db.First(&row, row.ID).Error
	if err := logAdminAction(s.db, user.AccountID, "update", domainBlockAuditLogTarget(row), time.Now().UTC()); err != nil {
		return err
	}
	if err := s.enqueueAdminDomainBlockEffectsOrApply(s.db, row, true); err != nil {
		return err
	}
	if err := s.refreshDomainControlMutation(c.Request().Context(), row.Domain); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.AdminDomainBlockFromModel(row))
}

func (s *Server) deleteAdminDomainBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:domain_blocks")
	if err != nil {
		return err
	}
	var row models.DomainBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.applyAdminDomainUnblockEffects(s.db, row); err != nil {
		return err
	}
	if err := s.db.Delete(&models.DomainBlock{}, row.ID).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := logAdminAction(s.db, user.AccountID, "destroy", domainBlockAuditLogTarget(row), time.Now().UTC()); err != nil {
		return err
	}
	if err := s.refreshDomainControlMutation(c.Request().Context(), row.Domain); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) adminEmailDomainBlocks(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:email_domain_blocks"); err != nil {
		return err
	}
	var rows []models.EmailDomainBlock
	limitValue := limit(c, 100, 200)
	if err := adminPage(c, s.db.Model(&models.EmailDomainBlock{}), "email_domain_blocks.id").Order("email_domain_blocks.id DESC").Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(rows)
	}
	setLinkForRows(c, rows, func(row models.EmailDomainBlock) int64 { return row.ID }, limitValue)
	out := make([]serializer.AdminEmailDomainBlock, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.adminEmailDomainBlockFromModel(c, row))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showAdminEmailDomainBlock(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:email_domain_blocks"); err != nil {
		return err
	}
	var row models.EmailDomainBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, s.adminEmailDomainBlockFromModel(c, row))
}

func (s *Server) createAdminEmailDomainBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:email_domain_blocks")
	if err != nil {
		return err
	}
	payload, err := parseAdminDomainPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	domain := normalizeDomain(payload.Domain)
	if domain == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain is invalid")
	}
	now := time.Now().UTC()
	row := models.EmailDomainBlock{Domain: domain, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain has already been taken")
		}
		return err
	}
	if err := logAdminAction(s.db, user.AccountID, "create", emailDomainBlockAuditLogTarget(row), now); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s.adminEmailDomainBlockFromModel(c, row))
}

func (s *Server) adminEmailDomainBlockFromModel(c *echo.Context, block models.EmailDomainBlock) serializer.AdminEmailDomainBlock {
	return serializer.AdminEmailDomainBlockFromModelWithHistory(block, s.emailDomainBlockHistory((*c).Request().Context(), block.ID, time.Now().UTC()))
}

func (s *Server) emailDomainBlockHistory(ctx context.Context, blockID int64, now time.Time) []serializer.AdminEmailDomainBlockHistory {
	out := make([]serializer.AdminEmailDomainBlockHistory, 0, 7)
	for i := 0; i < 7; i++ {
		day := dayStart(now.AddDate(0, 0, -i))
		uses, accounts := s.emailDomainBlockHistoryDay(ctx, blockID, day)
		out = append(out, serializer.AdminEmailDomainBlockHistory{
			Day:      strconv.FormatInt(day.Unix(), 10),
			Accounts: strconv.FormatInt(accounts, 10),
			Uses:     strconv.FormatInt(uses, 10),
		})
	}
	return out
}

func (s *Server) emailDomainBlockHistoryDay(ctx context.Context, blockID int64, day time.Time) (int64, int64) {
	usesKey, accountsKey := emailDomainBlockHistoryRedisKeys(s.cfg, blockID, day)
	usesCtx, cancelUses := context.WithTimeout(ctx, 150*time.Millisecond)
	usesValue, usesErr := s.redisCommand(usesCtx, "GET", usesKey)
	cancelUses()
	accountsCtx, cancelAccounts := context.WithTimeout(ctx, 150*time.Millisecond)
	accountsValue, accountsErr := s.redisCommand(accountsCtx, "PFCOUNT", accountsKey)
	cancelAccounts()
	var uses int64
	if usesErr == nil {
		uses = redisInt(usesValue)
	}
	var accounts int64
	if accountsErr == nil {
		accounts = redisInt(accountsValue)
	}
	return uses, accounts
}

func (s *Server) deleteAdminEmailDomainBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:email_domain_blocks")
	if err != nil {
		return err
	}
	var row models.EmailDomainBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Where("id = ? OR parent_id = ?", row.ID, row.ID).Delete(&models.EmailDomainBlock{}).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := logAdminAction(s.db, user.AccountID, "destroy", emailDomainBlockAuditLogTarget(row), time.Now().UTC()); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) adminCanonicalEmailBlocks(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:canonical_email_blocks"); err != nil {
		return err
	}
	var rows []models.CanonicalEmailBlock
	limitValue := limit(c, 100, 200)
	if err := adminPage(c, s.db.Model(&models.CanonicalEmailBlock{}), "canonical_email_blocks.id").Order("canonical_email_blocks.id DESC").Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(rows)
	}
	setLinkForRows(c, rows, func(row models.CanonicalEmailBlock) int64 { return row.ID }, limitValue)
	return c.JSON(http.StatusOK, adminCanonicalEmailBlockResponses(rows))
}

func (s *Server) showAdminCanonicalEmailBlock(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:canonical_email_blocks"); err != nil {
		return err
	}
	var row models.CanonicalEmailBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.AdminCanonicalEmailBlockFromModel(row))
}

func (s *Server) testAdminCanonicalEmailBlocks(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:canonical_email_blocks"); err != nil {
		return err
	}
	payload, err := parseAdminCanonicalEmailBlockPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if strings.TrimSpace(payload.Email) == "" {
		return apiError(c, http.StatusBadRequest, "param is missing or the value is empty: email")
	}
	var rows []models.CanonicalEmailBlock
	if err := s.db.Where("canonical_email_hash = ?", canonicalEmailHash(payload.Email)).Order("canonical_email_blocks.id DESC").Find(&rows).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, adminCanonicalEmailBlockResponses(rows))
}

func (s *Server) createAdminCanonicalEmailBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:canonical_email_blocks")
	if err != nil {
		return err
	}
	payload, err := parseAdminCanonicalEmailBlockPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	hash := canonicalEmailBlockHashFromPayload(payload)
	if strings.TrimSpace(hash) == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Canonical email hash can't be blank")
	}
	now := time.Now().UTC()
	row := models.CanonicalEmailBlock{CanonicalEmailHash: hash, CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Canonical email hash has already been taken")
		}
		return err
	}
	if err := logAdminAction(s.db, user.AccountID, "create", canonicalEmailBlockAuditLogTarget(row), now); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.AdminCanonicalEmailBlockFromModel(row))
}

func (s *Server) deleteAdminCanonicalEmailBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:canonical_email_blocks")
	if err != nil {
		return err
	}
	var row models.CanonicalEmailBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Delete(&models.CanonicalEmailBlock{}, row.ID).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := logAdminAction(s.db, user.AccountID, "destroy", canonicalEmailBlockAuditLogTarget(row), time.Now().UTC()); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) adminIPBlocks(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:ip_blocks"); err != nil {
		return err
	}
	var rows []models.IPBlock
	limitValue := limit(c, 100, 200)
	if err := adminPage(c, s.db.Model(&models.IPBlock{}), "ip_blocks.id").Order("ip_blocks.id DESC").Limit(limitValue).Find(&rows).Error; err != nil {
		return err
	}
	if queryParamValuePresent(c, "min_id") {
		reverseRows(rows)
	}
	setLinkForRows(c, rows, func(row models.IPBlock) int64 { return row.ID }, limitValue)
	out := make([]serializer.AdminIPBlock, 0, len(rows))
	for _, row := range rows {
		out = append(out, serializer.AdminIPBlockFromModel(row))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) showAdminIPBlock(c *echo.Context) error {
	if _, err := s.requireAdminRead(c, "admin:read:ip_blocks"); err != nil {
		return err
	}
	var row models.IPBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.AdminIPBlockFromModel(row))
}

func (s *Server) createAdminIPBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:ip_blocks")
	if err != nil {
		return err
	}
	payload, err := parseAdminIPBlockPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	ip := normalizeIPBlock(payload.IP)
	if ip == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: IP is invalid")
	}
	severity, ok := ipBlockSeverityValue(payload.Severity)
	if !ok {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Severity can't be blank")
	}
	now := time.Now().UTC()
	row := models.IPBlock{IP: ip, Severity: severity, Comment: payload.Comment, ExpiresAt: expiresAt(payload.ExpiresIn), CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: IP has already been taken")
		}
		return err
	}
	s.invalidateIPBlockCache(c.Request().Context())
	if err := logAdminAction(s.db, user.AccountID, "create", ipBlockAuditLogTarget(row), now); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.AdminIPBlockFromModel(row))
}

func (s *Server) updateAdminIPBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:ip_blocks")
	if err != nil {
		return err
	}
	var row models.IPBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	payload, err := parseAdminIPBlockPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if payload.IPSet {
		ip := normalizeIPBlock(payload.IP)
		if ip == "" {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: IP is invalid")
		}
		updates["ip"] = ip
	}
	if payload.SeveritySet {
		severity, ok := ipBlockSeverityValue(payload.Severity)
		if !ok {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Severity can't be blank")
		}
		updates["severity"] = severity
	}
	if payload.CommentSet {
		updates["comment"] = payload.Comment
	}
	if payload.ExpiresInSet {
		updates["expires_at"] = expiresAt(payload.ExpiresIn)
	}
	if err := s.db.Model(&row).Updates(updates).Error; err != nil {
		return err
	}
	s.invalidateIPBlockCache(c.Request().Context())
	_ = s.db.First(&row, row.ID).Error
	if err := logAdminAction(s.db, user.AccountID, "update", ipBlockAuditLogTarget(row), time.Now().UTC()); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.AdminIPBlockFromModel(row))
}

func (s *Server) deleteAdminIPBlock(c *echo.Context) error {
	user, err := s.requireAdminWrite(c, "admin:write:ip_blocks")
	if err != nil {
		return err
	}
	var row models.IPBlock
	if err := s.db.First(&row, c.Param("id")).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Delete(&models.IPBlock{}, row.ID).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	s.invalidateIPBlockCache(c.Request().Context())
	if err := logAdminAction(s.db, user.AccountID, "destroy", ipBlockAuditLogTarget(row), time.Now().UTC()); err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) requireAdminWrite(c *echo.Context, scopes ...string) (*models.User, error) {
	return s.requireAdminWriteWithPermissions(c, scopes, adminRolePermissionsForScopes(scopes, true)...)
}

func (s *Server) requireAdminWriteWithPermissions(c *echo.Context, scopes []string, permissions ...int64) (*models.User, error) {
	c.Response().Header().Set("Vary", "Authorization")
	accessToken, err := s.accessTokenFromRequest(c)
	if err != nil || !accessToken.ResourceOwnerID.Valid {
		return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !tokenHasAnyScope(accessToken.Scopes, append(scopes, "admin:write")...) {
		return nil, apiError(c, http.StatusForbidden, "This action is outside the authorized scopes")
	}
	var user models.User
	if err := s.db.Where("id = ? AND disabled = false", accessToken.ResourceOwnerID.Int64).First(&user).Error; err != nil {
		return nil, apiError(c, http.StatusUnauthorized, "The access token is invalid")
	}
	if !s.userCanAny(&user, permissions...) {
		return nil, apiError(c, http.StatusForbidden, "This action is outside the authorized role")
	}
	return &user, nil
}

func parseAdminDomainBlockPayload(c *echo.Context) (adminDomainBlockPayload, error) {
	var payload adminDomainBlockPayload
	if strings.Contains(strings.ToLower((*c).Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		if err := json.NewDecoder((*c).Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["domain"]; ok {
			payload.Domain = stringPayloadValue(value)
			payload.DomainSet = true
		}
		if value, ok := raw["severity"]; ok {
			payload.Severity = stringPayloadValue(value)
			payload.SeveritySet = true
		}
		if value, ok := raw["private_comment"]; ok {
			payload.PrivateComment = stringPayloadValue(value)
			payload.PrivateCommentSet = true
		}
		if value, ok := raw["public_comment"]; ok {
			payload.PublicComment = stringPayloadValue(value)
			payload.PublicCommentSet = true
		}
		if value, ok := raw["reject_media"]; ok {
			v := truthy(stringPayloadValue(value))
			payload.RejectMedia = &v
		}
		if value, ok := raw["reject_reports"]; ok {
			v := truthy(stringPayloadValue(value))
			payload.RejectReports = &v
		}
		if value, ok := raw["obfuscate"]; ok {
			v := truthy(stringPayloadValue(value))
			payload.Obfuscate = &v
		}
		return payload, nil
	}
	if err := c.Bind(&payload); err != nil {
		return payload, err
	}
	if values, err := c.FormValues(); err == nil {
		if values.Has("domain") {
			payload.Domain = values.Get("domain")
			payload.DomainSet = true
		}
		if values.Has("severity") {
			payload.Severity = values.Get("severity")
			payload.SeveritySet = true
		}
		if values.Has("private_comment") {
			payload.PrivateComment = values.Get("private_comment")
			payload.PrivateCommentSet = true
		}
		if values.Has("public_comment") {
			payload.PublicComment = values.Get("public_comment")
			payload.PublicCommentSet = true
		}
		if payload.RejectMedia == nil && values.Has("reject_media") {
			v := truthy(values.Get("reject_media"))
			payload.RejectMedia = &v
		}
		if payload.RejectReports == nil && values.Has("reject_reports") {
			v := truthy(values.Get("reject_reports"))
			payload.RejectReports = &v
		}
		if payload.Obfuscate == nil && values.Has("obfuscate") {
			v := truthy(values.Get("obfuscate"))
			payload.Obfuscate = &v
		}
	}
	return payload, nil
}

func parseAdminIPBlockPayload(c *echo.Context) (adminIPBlockPayload, error) {
	var payload adminIPBlockPayload
	if strings.Contains(strings.ToLower((*c).Request().Header.Get("Content-Type")), "json") {
		var raw map[string]any
		if err := json.NewDecoder((*c).Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		if value, ok := raw["ip"]; ok {
			payload.IP = stringPayloadValue(value)
			payload.IPSet = true
		}
		if value, ok := raw["severity"]; ok {
			payload.Severity = stringPayloadValue(value)
			payload.SeveritySet = true
		}
		if value, ok := raw["comment"]; ok {
			payload.Comment = stringPayloadValue(value)
			payload.CommentSet = true
		}
		if value, ok := raw["expires_in"]; ok {
			payload.ExpiresIn = stringPayloadValue(value)
			payload.ExpiresInSet = true
		}
		return payload, nil
	}
	if err := c.Bind(&payload); err != nil {
		return payload, err
	}
	if values, err := c.FormValues(); err == nil {
		if values.Has("ip") {
			payload.IP = values.Get("ip")
			payload.IPSet = true
		}
		if values.Has("severity") {
			payload.Severity = values.Get("severity")
			payload.SeveritySet = true
		}
		if values.Has("comment") {
			payload.Comment = values.Get("comment")
			payload.CommentSet = true
		}
		if values.Has("expires_in") {
			payload.ExpiresIn = values.Get("expires_in")
			payload.ExpiresInSet = true
		}
	}
	return payload, nil
}

func stringPayloadValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func optionalCommentString(value string, present bool) sql.NullString {
	if !present {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func parseAdminDomainPayload(c *echo.Context) (adminDomainPayload, error) {
	var payload adminDomainPayload
	if err := c.Bind(&payload); err != nil {
		return payload, err
	}
	if values, err := c.FormValues(); err == nil {
		payload.Domain = firstNonEmpty(payload.Domain, values.Get("domain"))
	}
	return payload, nil
}

func parseAdminCanonicalEmailBlockPayload(c *echo.Context) (adminCanonicalEmailBlockPayload, error) {
	var payload adminCanonicalEmailBlockPayload
	if err := c.Bind(&payload); err != nil {
		return payload, err
	}
	if values, err := c.FormValues(); err == nil {
		payload.CanonicalEmailHash = firstNonEmpty(payload.CanonicalEmailHash, values.Get("canonical_email_hash"))
		payload.Email = firstNonEmpty(payload.Email, values.Get("email"))
	}
	return payload, nil
}

func adminCanonicalEmailBlockResponses(rows []models.CanonicalEmailBlock) []serializer.AdminCanonicalEmailBlock {
	out := make([]serializer.AdminCanonicalEmailBlock, 0, len(rows))
	for _, row := range rows {
		out = append(out, serializer.AdminCanonicalEmailBlockFromModel(row))
	}
	return out
}

func canonicalEmailHash(email string) string {
	sum := sha256.Sum256([]byte(canonicalEmail(email)))
	return hex.EncodeToString(sum[:])
}

func canonicalEmailBlockHashFromPayload(payload adminCanonicalEmailBlockPayload) string {
	if strings.TrimSpace(payload.Email) != "" {
		return canonicalEmailHash(payload.Email)
	}
	return payload.CanonicalEmailHash
}

func canonicalEmail(email string) string {
	parts := strings.SplitN(strings.ToLower(email), "@", 2)
	username := strings.ReplaceAll(parts[0], ".", "")
	username = strings.SplitN(username, "+", 2)[0]
	if len(parts) != 2 {
		return username + "@"
	}
	return username + "@" + parts[1]
}

func adminPage(c *echo.Context, query *gorm.DB, column string) *gorm.DB {
	return applyIDPagination(c, query, column)
}

func setLinkForRows[T any](c *echo.Context, rows []T, id func(T) int64, limitValue int) {
	if len(rows) == 0 {
		return
	}
	c.Response().Header().Set("Link", paginationLinkWithAllowedParams(c, id(rows[0]), id(rows[len(rows)-1]), "min_id", len(rows) == limitValue, true, adminLimitPaginationParams))
}

func deleteByID[T any](db *gorm.DB, id string) error {
	result := db.Delete(new(T), id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.Trim(strings.TrimSpace(value), "/"))
	if value == "" || strings.Contains(value, "://") || strings.ContainsAny(value, " /") {
		return ""
	}
	domain, err := railsNormalizedHost(value)
	if err != nil {
		return ""
	}
	return domain
}

func railsNormalizedHost(value string) (string, error) {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if domain == "" {
		return "", errInvalidDomain
	}
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return "", err
	}
	ascii = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ascii)), ".")
	if ascii == "" {
		return "", errInvalidDomain
	}
	return ascii, nil
}

func normalizeIPBlock(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "/") {
		if _, _, err := net.ParseCIDR(value); err != nil {
			return ""
		}
		return value
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		return value + "/32"
	}
	return value + "/128"
}

func domainBlockSeverityValue(value string) sql.NullInt64 {
	return models.DomainBlockSeverity(domainBlockSeverityCode(value))
}

func domainBlockSeverityCode(value string) int {
	switch strings.TrimSpace(value) {
	case "suspend":
		return 1
	case "noop":
		return 2
	default:
		return 0
	}
}

func (s *Server) existingDomainBlockForDomain(domain string) (*models.DomainBlock, error) {
	var block models.DomainBlock
	err := s.db.Where("domain IN ?", domainRuleVariants(domain)).
		Order("char_length(domain) DESC").
		Limit(1).
		First(&block).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &block, nil
}

func domainRuleVariants(domain string) []string {
	domain = normalizeDomain(domain)
	if domain == "" {
		return nil
	}
	parts := strings.Split(domain, ".")
	variants := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		variants = append(variants, strings.Join(parts[i:], "."))
	}
	return variants
}

func domainBlockStricterThan(block models.DomainBlock, other models.DomainBlock) bool {
	blockSuspend := domainBlockSeverityIs(block, "suspend")
	blockSilence := domainBlockSeverityIs(block, "silence")
	blockNoop := domainBlockSeverityIs(block, "noop")
	otherSuspend := domainBlockSeverityIs(other, "suspend")
	otherSilence := domainBlockSeverityIs(other, "silence")
	if blockSuspend {
		return true
	}
	if otherSuspend && (blockSilence || blockNoop) {
		return false
	}
	if otherSilence && blockNoop {
		return false
	}
	return (block.RejectMedia || !other.RejectMedia) && (block.RejectReports || !other.RejectReports)
}

func domainBlockSeverityIs(block models.DomainBlock, value string) bool {
	severity, ok := block.SeverityInt()
	return ok && severity == domainBlockSeverityCode(value)
}

func existingDomainBlockError(block models.DomainBlock, locale string) serializer.AdminExistingDomainBlockError {
	message := settingsTVars(locale, "admin.domain_blocks.existing_domain_block", "You have already imposed stricter limits on %{name}.", map[string]string{"name": block.Domain})
	return serializer.AdminExistingDomainBlockErrorFromModelWithMessage(block, message)
}

func ipBlockSeverityValue(value string) (int, bool) {
	switch strings.TrimSpace(value) {
	case "sign_up_requires_approval":
		return 5000, true
	case "sign_up_block":
		return 5500, true
	case "no_access":
		return 9999, true
	default:
		return 0, false
	}
}

func expiresAt(value string) sql.NullTime {
	if strings.TrimSpace(value) == "" {
		return sql.NullTime{}
	}
	seconds := railsInt64FromString(value)
	return sql.NullTime{Time: time.Now().UTC().Add(time.Duration(seconds) * time.Second), Valid: true}
}

func boolValue(value *bool) bool {
	return value != nil && *value
}
