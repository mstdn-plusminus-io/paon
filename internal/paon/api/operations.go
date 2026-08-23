package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type Operations struct {
	server *Server
}

type OperationCustomEmojiPurgeEntry struct {
	ID        int64
	Shortcode string
	Domain    string
}

func (operations *Operations) CustomEmojiPurgeInventory(ctx context.Context, remoteOnly bool, suspendedOnly bool) ([]OperationCustomEmojiPurgeEntry, error) {
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return nil, errors.New("operations database is not configured")
	}
	query := operations.server.db.WithContext(ctx).Model(&models.CustomEmoji{})
	if suspendedOnly {
		query = query.Where(`custom_emojis.domain IS NOT NULL AND custom_emojis.domain <> '' AND EXISTS (
			SELECT 1 FROM domain_blocks
			WHERE domain_blocks.severity = ?
			  AND (lower(custom_emojis.domain) = lower(domain_blocks.domain)
			       OR lower(custom_emojis.domain) LIKE ('%.' || lower(domain_blocks.domain)))
		)`, domainBlockSeverityCode("suspend"))
	} else if remoteOnly {
		query = query.Where("custom_emojis.domain IS NOT NULL AND custom_emojis.domain <> ''")
	}
	var emojis []models.CustomEmoji
	if err := query.Order("custom_emojis.id ASC").Find(&emojis).Error; err != nil {
		return nil, err
	}
	entries := make([]OperationCustomEmojiPurgeEntry, 0, len(emojis))
	for _, emoji := range emojis {
		entries = append(entries, OperationCustomEmojiPurgeEntry{ID: emoji.ID, Shortcode: emoji.Shortcode, Domain: emoji.Domain.String})
	}
	return entries, nil
}

func (operations *Operations) PurgeCustomEmojis(ctx context.Context, remoteOnly bool, suspendedOnly bool) (int, error) {
	entries, err := operations.CustomEmojiPurgeInventory(ctx, remoteOnly, suspendedOnly)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		var emoji models.CustomEmoji
		if err := operations.server.db.WithContext(ctx).Where("id = ?", entry.ID).First(&emoji).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		} else if err != nil {
			return removed, err
		}
		operations.server.removeCustomEmojiLocalFiles(emoji)
		if err := operations.server.db.WithContext(ctx).Delete(&models.CustomEmoji{}, emoji.ID).Error; err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

type OperationMediaRefreshOptions struct {
	StatusID int64
	Account  string
	Domain   string
	Days     int
	Force    bool
}

type OperationMediaRefreshEntry struct {
	ID   int64
	Size int64
}

func (operations *Operations) SearchDeployInventory(ctx context.Context) (MeiliDeployStats, error) {
	var out MeiliDeployStats
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return out, errors.New("operations database is not configured")
	}
	counts := []struct {
		model any
		value *int
	}{
		{&models.Account{}, &out.Accounts},
		{&models.Status{}, &out.Statuses},
		{&models.Tag{}, &out.Tags},
		{&models.Instance{}, &out.Instances},
	}
	for _, item := range counts {
		var count int64
		if err := operations.server.db.WithContext(ctx).Model(item.model).Count(&count).Error; err != nil {
			return out, err
		}
		*item.value = int(count)
	}
	return out, nil
}

func (operations *Operations) DeploySearch(ctx context.Context, options MeiliDeployOptions) (MeiliDeployStats, error) {
	if operations == nil || operations.server == nil {
		return MeiliDeployStats{}, errors.New("operations server is not configured")
	}
	return operations.server.DeployMeiliIndexes(ctx, options)
}

func (operations *Operations) MediaRefreshInventory(ctx context.Context, options OperationMediaRefreshOptions) ([]OperationMediaRefreshEntry, error) {
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return nil, errors.New("operations database is not configured")
	}
	sources := 0
	if options.StatusID > 0 {
		sources++
	}
	if strings.TrimSpace(options.Account) != "" {
		sources++
	}
	if strings.TrimSpace(options.Domain) != "" {
		sources++
	}
	if options.Days > 0 {
		sources++
	}
	if sources != 1 {
		return nil, errors.New("specify exactly one of status, account, domain, or days")
	}
	query := operations.server.db.WithContext(ctx).Model(&models.MediaAttachment{}).
		Select("media_attachments.id, COALESCE(media_attachments.file_file_size, 0) + COALESCE(media_attachments.thumbnail_file_size, 0) AS size").
		Where("media_attachments.remote_url <> ''")
	if !options.Force {
		query = query.Where("media_attachments.file_file_name IS NULL OR media_attachments.file_file_name = ''")
	}
	switch {
	case options.StatusID > 0:
		query = query.Where("media_attachments.status_id = ?", options.StatusID)
	case strings.TrimSpace(options.Account) != "":
		username, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(options.Account)), "@")
		if !ok || username == "" || domain == "" {
			return nil, errors.New("account must be a remote username@domain handle")
		}
		query = query.Joins("JOIN accounts media_refresh_accounts ON media_refresh_accounts.id = media_attachments.account_id").
			Where("lower(media_refresh_accounts.username) = ? AND lower(media_refresh_accounts.domain) = ?", username, domain)
	case strings.TrimSpace(options.Domain) != "":
		domain := normalizeDomain(options.Domain)
		if domain == "" {
			return nil, errors.New("invalid media domain")
		}
		query = query.Joins("JOIN accounts media_refresh_accounts ON media_refresh_accounts.id = media_attachments.account_id").
			Where("lower(media_refresh_accounts.domain) = ? OR lower(media_refresh_accounts.domain) LIKE ?", domain, "%."+domain)
	case options.Days > 0:
		query = query.Joins("JOIN accounts media_refresh_accounts ON media_refresh_accounts.id = media_attachments.account_id").
			Where("media_refresh_accounts.domain IS NOT NULL AND media_refresh_accounts.domain <> ''").
			Where("media_attachments.created_at >= ?", time.Now().UTC().Add(-time.Duration(options.Days)*24*time.Hour))
	}
	var entries []OperationMediaRefreshEntry
	if err := query.Order("media_attachments.id ASC").Scan(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

func (operations *Operations) RefreshMedia(ctx context.Context, options OperationMediaRefreshOptions) (int, int64, error) {
	entries, err := operations.MediaRefreshInventory(ctx, options)
	if err != nil {
		return 0, 0, err
	}
	queued := 0
	var bytes int64
	for _, entry := range entries {
		if operations.server.enqueueRedownloadMediaTask(entry.ID) {
			queued++
			bytes += entry.Size
			continue
		}
		if err := operations.server.redownloadRemoteMediaAttachment(ctx, entry.ID); err != nil {
			return queued, bytes, err
		}
		queued++
		bytes += entry.Size
	}
	return queued, bytes, nil
}

type EmailDomainBlockEntry struct {
	ID                int64
	Domain            string
	ParentID          sql.NullInt64
	AllowWithApproval bool
}

func (operations *Operations) ListEmailDomainBlockEntries(ctx context.Context) ([]EmailDomainBlockEntry, error) {
	var rows []models.EmailDomainBlock
	if err := operations.server.db.WithContext(ctx).Order("domain ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	parents := make([]models.EmailDomainBlock, 0, len(rows))
	children := map[int64][]models.EmailDomainBlock{}
	for _, row := range rows {
		if row.ParentID.Valid {
			children[row.ParentID.Int64] = append(children[row.ParentID.Int64], row)
			continue
		}
		parents = append(parents, row)
	}
	out := make([]EmailDomainBlockEntry, 0, len(rows))
	for _, parent := range parents {
		out = append(out, EmailDomainBlockEntry{ID: parent.ID, Domain: parent.Domain, ParentID: parent.ParentID, AllowWithApproval: parent.AllowWithApproval})
		for _, child := range children[parent.ID] {
			out = append(out, EmailDomainBlockEntry{ID: child.ID, Domain: child.Domain, ParentID: child.ParentID, AllowWithApproval: child.AllowWithApproval})
		}
	}
	return out, nil
}

func (operations *Operations) ListEmailDomainBlocks(ctx context.Context) ([]string, error) {
	var rows []models.EmailDomainBlock
	if err := operations.server.db.WithContext(ctx).Order("domain ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Domain)
	}
	return out, nil
}

func (operations *Operations) AddEmailDomainBlocks(ctx context.Context, domains []string) (int, error) {
	return operations.AddEmailDomainBlocksWithApproval(ctx, domains, false)
}

func (operations *Operations) AddEmailDomainBlocksWithApproval(ctx context.Context, domains []string, allowWithApproval bool) (int, error) {
	added, _, err := operations.AddEmailDomainBlocksWithDNS(ctx, domains, allowWithApproval, false)
	return added, err
}

func (operations *Operations) AddEmailDomainBlocksWithDNS(ctx context.Context, domains []string, allowWithApproval bool, withDNSRecords bool) (int, int, error) {
	added := 0
	skipped := 0
	for _, raw := range domains {
		domain := normalizeDomain(raw)
		if domain == "" || strings.ContainsAny(domain, " /@") || !strings.Contains(domain, ".") {
			return added, skipped, fmt.Errorf("invalid email domain %q", raw)
		}
		var count int64
		if err := operations.server.db.WithContext(ctx).Model(&models.EmailDomainBlock{}).Where("domain = ?", domain).Count(&count).Error; err != nil {
			return added, skipped, err
		}
		if count > 0 {
			skipped++
			continue
		}
		otherDomains := []string(nil)
		if withDNSRecords {
			otherDomains = resolveEmailDomainBlockMXDomains(domain)
		}
		rows, err := operations.server.insertAdminEmailDomainBlocks(domain, otherDomains, allowWithApproval)
		if err != nil {
			if isUniqueConstraintError(err) {
				skipped++
				continue
			}
			return added, skipped, err
		}
		added += len(rows)
		skipped += 1 + len(otherDomains) - len(rows)
	}
	return added, skipped, nil
}

func (operations *Operations) RemoveEmailDomainBlocks(ctx context.Context, domains []string) (int64, error) {
	entries, err := operations.EmailDomainBlockRemovalInventory(ctx, domains)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := operations.server.db.WithContext(ctx).Where("id IN ?", ids).Delete(&models.EmailDomainBlock{})
	return result.RowsAffected, result.Error
}

func (operations *Operations) EmailDomainBlockRemovalInventory(ctx context.Context, domains []string) ([]EmailDomainBlockEntry, error) {
	normalized := make([]string, 0, len(domains))
	for _, domain := range domains {
		if value := strings.ToLower(strings.TrimSpace(domain)); value != "" {
			normalized = append(normalized, value)
		}
	}
	if len(normalized) == 0 {
		return []EmailDomainBlockEntry{}, nil
	}
	var roots []models.EmailDomainBlock
	if err := operations.server.db.WithContext(ctx).Where("domain IN ?", normalized).Find(&roots).Error; err != nil {
		return nil, err
	}
	rootIDs := make([]int64, 0, len(roots))
	for _, root := range roots {
		rootIDs = append(rootIDs, root.ID)
	}
	var rows []models.EmailDomainBlock
	if len(rootIDs) > 0 {
		if err := operations.server.db.WithContext(ctx).Where("id IN ? OR parent_id IN ?", rootIDs, rootIDs).Order("id ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
	}
	out := make([]EmailDomainBlockEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, EmailDomainBlockEntry{ID: row.ID, Domain: row.Domain, ParentID: row.ParentID, AllowWithApproval: row.AllowWithApproval})
	}
	return out, nil
}

func (operations *Operations) CanonicalEmailBlockExists(ctx context.Context, email string) (bool, error) {
	hash, err := operationCanonicalEmailHash(email)
	if err != nil {
		return false, err
	}
	var count int64
	err = operations.server.db.WithContext(ctx).Model(&models.CanonicalEmailBlock{}).Where("canonical_email_hash = ?", hash).Count(&count).Error
	return count > 0, err
}

func (operations *Operations) RemoveCanonicalEmailBlock(ctx context.Context, email string) (bool, error) {
	hash, err := operationCanonicalEmailHash(email)
	if err != nil {
		return false, err
	}
	result := operations.server.db.WithContext(ctx).Where("canonical_email_hash = ?", hash).Delete(&models.CanonicalEmailBlock{})
	return result.RowsAffected > 0, result.Error
}

func operationCanonicalEmailHash(email string) (string, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(email)), "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid email %q", email)
	}
	return canonicalEmailHash(strings.TrimSpace(email)), nil
}

type OperationIPBlock struct {
	CIDR      string
	Severity  string
	Comment   string
	ExpiresAt sql.NullTime
}

func (operations *Operations) AddIPBlocks(ctx context.Context, blocks []OperationIPBlock, force bool) (int, error) {
	now := time.Now().UTC()
	added := 0
	for _, block := range blocks {
		cidr, err := normalizeOperationCIDR(block.CIDR)
		if err != nil {
			return added, err
		}
		severity, ok := map[string]int{"sign_up_requires_approval": 5000, "sign_up_block": 5500, "no_access": 9999}[block.Severity]
		if !ok {
			return added, fmt.Errorf("unsupported IP block severity %q", block.Severity)
		}
		query := `INSERT INTO ip_blocks (ip, severity, comment, expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`
		if force {
			query += ` ON CONFLICT (ip) DO UPDATE SET severity = EXCLUDED.severity, comment = EXCLUDED.comment, expires_at = EXCLUDED.expires_at, updated_at = EXCLUDED.updated_at`
		} else {
			query += ` ON CONFLICT (ip) DO NOTHING`
		}
		result := operations.server.db.WithContext(ctx).Exec(query, cidr, severity, block.Comment, block.ExpiresAt, now, now)
		if result.Error != nil {
			return added, result.Error
		}
		added += int(result.RowsAffected)
	}
	if added > 0 {
		operations.server.invalidateIPBlockCache(ctx)
	}
	return added, nil
}

func (operations *Operations) RemoveIPBlocks(ctx context.Context, cidrs []string) (int64, error) {
	normalized := make([]string, 0, len(cidrs))
	for _, raw := range cidrs {
		cidr, err := normalizeOperationCIDR(raw)
		if err != nil {
			return 0, err
		}
		normalized = append(normalized, cidr)
	}
	result := operations.server.db.WithContext(ctx).Where("ip IN ?", normalized).Delete(&models.IPBlock{})
	if result.Error == nil && result.RowsAffected > 0 {
		operations.server.invalidateIPBlockCache(ctx)
	}
	return result.RowsAffected, result.Error
}

func (operations *Operations) ListIPBlocks(ctx context.Context) ([]models.IPBlock, error) {
	var rows []models.IPBlock
	err := operations.server.db.WithContext(ctx).Order("ip ASC").Find(&rows).Error
	sort.Slice(rows, func(i, j int) bool { return rows[i].IP < rows[j].IP })
	return rows, err
}

func normalizeOperationCIDR(value string) (string, error) {
	value = strings.TrimSpace(value)
	if _, network, err := net.ParseCIDR(value); err == nil {
		return network.String(), nil
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return "", fmt.Errorf("invalid IP address or CIDR %q", value)
	}
	if ip.To4() != nil {
		return ip.String() + "/32", nil
	}
	return ip.String() + "/128", nil
}

func NewOperations(cfg config.Config, database *gorm.DB) *Operations {
	return &Operations{server: &Server{
		cfg:            cfg,
		db:             database,
		asynqClient:    asynq.NewClient(asynqRedisOpt(cfg)),
		asynqInspector: asynq.NewInspector(asynqRedisOpt(cfg)),
	}}
}

// CutoverLegacyStatusQuotes is the only supported DynamoDB quote access after
// the official Quote cutover. It reads and revalidates legacy metadata, writes accepted
// PostgreSQL Quote rows only when dryRun is false, and never mutates DynamoDB.
func (operations *Operations) CutoverLegacyStatusQuotes(ctx context.Context, dryRun bool) (QuoteCutoverResult, error) {
	var result QuoteCutoverResult
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return result, errors.New("operations database is not configured")
	}
	store, err := newStatusQuoteStore(operations.server.cfg, nil)
	if err != nil {
		return result, err
	}
	if store == nil {
		return result, errors.New("DYNAMODB_ENABLED=true is required for the one-way quote cutover")
	}
	return operations.server.cutoverLegacyDynamoStatusQuotes(ctx, !dryRun, store)
}

func (operations *Operations) Close() error {
	if operations == nil || operations.server == nil {
		return nil
	}
	var closeErrors []error
	if operations.server.asynqClient != nil {
		closeErrors = append(closeErrors, operations.server.asynqClient.Close())
	}
	if operations.server.asynqInspector != nil {
		closeErrors = append(closeErrors, operations.server.asynqInspector.Close())
	}
	return errors.Join(closeErrors...)
}

type OperationAccountCreate struct {
	Username  string
	Email     string
	Password  string
	Role      string
	Confirmed bool
	Approved  bool
}

func (operations *Operations) CreateAccount(ctx context.Context, options OperationAccountCreate) (models.User, error) {
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return models.User{}, errors.New("operations database is not configured")
	}
	options.Username = railsAccountUsernameValue(options.Username)
	options.Email = strings.ToLower(strings.TrimSpace(options.Email))
	if !localUsernamePattern.MatchString(options.Username) || options.Email == "" || len(options.Password) < 8 || len(options.Password) > 72 {
		return models.User{}, errors.New("username, email, and an 8-72 character password are required")
	}
	var user *models.User
	err := operations.server.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created, err := operations.server.createLocalUserAccount(tx, accountCreatePayload{
			Username: options.Username,
			Email:    options.Email,
			Password: options.Password,
			Locale:   operations.server.cfg.Locale(),
		}, accountCreationOptions{Now: time.Now().UTC()})
		if err != nil {
			return err
		}
		updates := map[string]any{"approved": options.Approved, "updated_at": time.Now().UTC()}
		if options.Confirmed {
			updates["confirmed_at"] = time.Now().UTC()
			updates["confirmation_token"] = nil
		}
		if role := strings.TrimSpace(options.Role); role != "" {
			var row models.UserRole
			if err := tx.Where("LOWER(name) = LOWER(?)", role).First(&row).Error; err != nil {
				return fmt.Errorf("find role %q: %w", role, err)
			}
			updates["role_id"] = row.ID
			created.RoleID = sql.NullInt64{Int64: row.ID, Valid: true}
		}
		if err := tx.Model(&models.User{}).Where("id = ?", created.ID).Updates(updates).Error; err != nil {
			return err
		}
		created.Approved = options.Approved
		if options.Confirmed {
			created.ConfirmedAt = sql.NullTime{Time: updates["confirmed_at"].(time.Time), Valid: true}
		}
		user = created
		return nil
	})
	if err != nil {
		return models.User{}, err
	}
	if options.Approved {
		_ = operations.server.runApprovedAccountBootstrap(ctx, user.AccountID, time.Now().UTC())
	}
	return *user, nil
}

type OperationAccountModify struct {
	Email         string
	Role          string
	RemoveRole    bool
	Confirm       bool
	Approve       bool
	Enable        bool
	Disable       bool
	Disable2FA    bool
	ResetPassword bool
}

type OperationAccountModifyResult struct {
	User              models.User
	GeneratedPassword string
}

func (operations *Operations) ModifyAccount(ctx context.Context, username string, options OperationAccountModify) (OperationAccountModifyResult, error) {
	user, err := operations.localUser(ctx, username)
	if err != nil {
		return OperationAccountModifyResult{}, err
	}
	now := time.Now().UTC()
	updates := map[string]any{"updated_at": now}
	if email := strings.ToLower(strings.TrimSpace(options.Email)); email != "" {
		updates["email"] = email
	}
	if options.Confirm {
		updates["confirmed_at"] = now
		updates["confirmation_token"] = nil
	}
	if options.Approve {
		updates["approved"] = true
	}
	if options.Enable {
		updates["disabled"] = false
	}
	if options.Disable {
		updates["disabled"] = true
	}
	if options.Disable2FA {
		updates["otp_required_for_login"] = false
		updates["otp_secret"] = nil
		updates["otp_backup_codes"] = models.StringArray{}
	}
	if options.RemoveRole {
		updates["role_id"] = nil
	} else if role := strings.TrimSpace(options.Role); role != "" {
		var row models.UserRole
		if err := operations.server.db.WithContext(ctx).Where("LOWER(name) = LOWER(?)", role).First(&row).Error; err != nil {
			return OperationAccountModifyResult{}, fmt.Errorf("find role %q: %w", role, err)
		}
		updates["role_id"] = row.ID
	}
	generatedPassword := ""
	if options.ResetPassword {
		generatedPassword = randomHex(16)
		hash, err := bcrypt.GenerateFromPassword([]byte(generatedPassword), bcrypt.DefaultCost)
		if err != nil {
			return OperationAccountModifyResult{}, err
		}
		updates["encrypted_password"] = string(hash)
	}
	if len(updates) == 1 {
		return OperationAccountModifyResult{}, errors.New("no account modifications requested")
	}
	if err := operations.server.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if options.Disable2FA {
			if err := tx.Where("user_id = ?", user.ID).Delete(&models.WebauthnCredential{}).Error; err != nil {
				return err
			}
		}
		if options.ResetPassword {
			if err := tx.Where("id IN (?)", tx.Model(&models.SessionActivation{}).Select("web_push_subscription_id").Where("user_id = ? AND web_push_subscription_id IS NOT NULL", user.ID)).Delete(&models.WebPushSubscription{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_id = ?", user.ID).Delete(&models.SessionActivation{}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&models.User{}).Where("id = ?", user.ID).Updates(updates).Error
	}); err != nil {
		return OperationAccountModifyResult{}, err
	}
	if options.ResetPassword {
		revokedTokenIDs, err := operations.revokeCredentialsAfterPasswordReset(ctx, user.ID, now)
		if err != nil {
			return OperationAccountModifyResult{User: user, GeneratedPassword: generatedPassword}, fmt.Errorf("password changed but credential revocation failed: %w", err)
		}
		operations.server.publishAccessTokenKills(revokedTokenIDs)
	}
	updated, err := operations.localUser(ctx, username)
	return OperationAccountModifyResult{User: updated, GeneratedPassword: generatedPassword}, err
}

// revokeCredentialsAfterPasswordReset deliberately runs after the password and
// browser-session transaction, mirroring User#change_password! callbacks whose
// token revocation and streaming disconnect are separate durable effects.
func (operations *Operations) revokeCredentialsAfterPasswordReset(ctx context.Context, userID int64, now time.Time) ([]int64, error) {
	var revokedTokenIDs []int64
	err := operations.server.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.OAuthAccessToken{}).
			Where("resource_owner_id = ? AND revoked_at IS NULL", userID).
			Pluck("id", &revokedTokenIDs).Error; err != nil {
			return err
		}
		pushQuery := tx.Where("user_id = ?", userID)
		if len(revokedTokenIDs) > 0 {
			pushQuery = pushQuery.Or("access_token_id IN ?", revokedTokenIDs)
		}
		if err := pushQuery.Delete(&models.WebPushSubscription{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.OAuthAccessGrant{}).Where("resource_owner_id = ? AND revoked_at IS NULL", userID).Update("revoked_at", now).Error; err != nil {
			return err
		}
		return tx.Model(&models.OAuthAccessToken{}).Where("resource_owner_id = ? AND revoked_at IS NULL", userID).Update("revoked_at", now).Error
	})
	return revokedTokenIDs, err
}

func (operations *Operations) RotateAccountKey(ctx context.Context, username string) error {
	user, err := operations.localUser(ctx, username)
	if err != nil {
		return err
	}
	privateKey, publicKey, err := generateAccountKeyPair()
	if err != nil {
		return err
	}
	oldPrivateKey := user.Account.PrivateKey.String
	if err := operations.server.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", user.AccountID).Updates(map[string]any{
		"private_key": privateKey,
		"public_key":  publicKey,
		"updated_at":  time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	if !operations.server.enqueueAccountUpdateTaskWithSigningKey(user.AccountID, 0, oldPrivateKey) {
		return errors.New("account key rotated but federation update could not be queued")
	}
	return nil
}

func (operations *Operations) RotateAllAccountKeys(ctx context.Context) (int, error) {
	var accounts []models.Account
	if err := operations.server.db.WithContext(ctx).
		Where("domain IS NULL AND suspended_at IS NULL").
		Order("id ASC").
		Find(&accounts).Error; err != nil {
		return 0, err
	}
	processed := 0
	for index, account := range accounts {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		privateKey, publicKey, err := generateAccountKeyPair()
		if err != nil {
			return processed, err
		}
		oldPrivateKey := account.PrivateKey.String
		if err := operations.server.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{
			"private_key": privateKey,
			"public_key":  publicKey,
			"updated_at":  time.Now().UTC(),
		}).Error; err != nil {
			return processed, err
		}
		delay := time.Duration(index/1000) * 5 * time.Minute
		if !operations.server.enqueueAccountUpdateTaskWithSigningKey(account.ID, delay, oldPrivateKey) {
			return processed, fmt.Errorf("account %d key rotated but federation update could not be queued", account.ID)
		}
		processed++
	}
	return processed, nil
}

type OperationAccountCullResult struct {
	Visited            int
	Removed            int
	UnavailableDomains []string
}

func (operations *Operations) CullRemoteAccounts(ctx context.Context, domains []string, concurrency int, dryRun bool) (OperationAccountCullResult, error) {
	if concurrency <= 0 {
		return OperationAccountCullResult{}, errors.New("cull concurrency must be positive")
	}
	normalizedDomains := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		normalizedDomains = append(normalizedDomains, domain)
	}
	query := operations.server.db.WithContext(ctx).Where("domain IS NOT NULL AND protocol = ?", 1)
	if len(normalizedDomains) > 0 {
		query = query.Where("LOWER(domain) IN ?", normalizedDomains)
	}
	threshold := time.Now().UTC().Add(-7 * 24 * time.Hour)
	query = query.Where("updated_at < ? AND (last_webfingered_at IS NULL OR last_webfingered_at < ?)", threshold, threshold)
	var accounts []models.Account
	if err := query.Order("id ASC").Find(&accounts).Error; err != nil {
		return OperationAccountCullResult{}, err
	}
	client := operationRemoteHTTPClient()
	jobs := make(chan models.Account)
	var wait sync.WaitGroup
	var mutex sync.Mutex
	result := OperationAccountCullResult{}
	unavailable := map[string]bool{}
	var firstErr error
	worker := func() {
		defer wait.Done()
		for account := range jobs {
			domain := strings.ToLower(account.Domain.String)
			mutex.Lock()
			skipped := unavailable[domain] || firstErr != nil
			mutex.Unlock()
			if skipped {
				continue
			}
			actorURL, err := url.Parse(strings.TrimSpace(account.URI))
			if err != nil || actorURL.Host == "" || (actorURL.Scheme != "http" && actorURL.Scheme != "https") || !activityFetchHostAllowed(actorURL.Hostname()) {
				mutex.Lock()
				unavailable[domain] = true
				mutex.Unlock()
				continue
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodHead, actorURL.String(), nil)
			if err != nil {
				mutex.Lock()
				unavailable[domain] = true
				mutex.Unlock()
				continue
			}
			request.Header.Set("User-Agent", paonUserAgent(operations.server.cfg))
			response, err := client.Do(request)
			if err != nil {
				mutex.Lock()
				unavailable[domain] = true
				mutex.Unlock()
				continue
			}
			_ = response.Body.Close()
			removed := response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone
			if removed && !dryRun {
				err = operations.server.deleteRemoteGoneAccountNow(ctx, &account, time.Now().UTC())
			} else if !removed || dryRun {
				err = operations.server.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", account.ID).Update("updated_at", time.Now().UTC()).Error
			}
			mutex.Lock()
			result.Visited++
			if removed {
				result.Removed++
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
			mutex.Unlock()
		}
	}
	for index := 0; index < concurrency; index++ {
		wait.Add(1)
		go worker()
	}
	for _, account := range accounts {
		jobs <- account
	}
	close(jobs)
	wait.Wait()
	for domain := range unavailable {
		result.UnavailableDomains = append(result.UnavailableDomains, domain)
	}
	sort.Strings(result.UnavailableDomains)
	return result, firstErr
}

func operationRemoteHTTPClient() http.Client {
	client := *activityHTTPClient
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !activityRedirectAllowed(request, via) {
			return errors.New("unsafe redirect")
		}
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return client
}

type OperationDomainCrawlResult struct {
	Stats     map[string]map[string]any
	Visited   int
	Failed    int
	StartedAt time.Time
}

func (operations *Operations) CrawlDomains(ctx context.Context, start string, concurrency int, excludeSuspended bool) (OperationDomainCrawlResult, error) {
	if concurrency <= 0 {
		return OperationDomainCrawlResult{}, errors.New("crawl concurrency must be positive")
	}
	result := OperationDomainCrawlResult{Stats: map[string]map[string]any{}, StartedAt: time.Now().UTC()}
	frontier := []string{}
	if start = strings.ToLower(strings.TrimSpace(start)); start != "" {
		frontier = append(frontier, start)
	} else {
		if err := operations.server.db.WithContext(ctx).Model(&models.Instance{}).Order("domain ASC").Pluck("domain", &frontier).Error; err != nil {
			return result, err
		}
	}
	blocked := []string{}
	if excludeSuspended {
		if err := operations.server.db.WithContext(ctx).Model(&models.DomainBlock{}).Where("severity = ?", 1).Pluck("domain", &blocked).Error; err != nil {
			return result, err
		}
	}
	seen := map[string]bool{}
	for len(frontier) > 0 {
		batch := make([]string, 0, len(frontier))
		for _, domain := range frontier {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if domain == "" || seen[domain] || operationDomainBlocked(domain, blocked) {
				continue
			}
			seen[domain] = true
			batch = append(batch, domain)
		}
		frontier = nil
		type crawlItem struct {
			domain string
			stats  map[string]any
			peers  []string
			err    error
		}
		results := make(chan crawlItem, len(batch))
		semaphore := make(chan struct{}, concurrency)
		var wait sync.WaitGroup
		for _, domain := range batch {
			wait.Add(1)
			go func(domain string) {
				defer wait.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				stats, peers, err := operations.crawlDomain(ctx, domain)
				results <- crawlItem{domain: domain, stats: stats, peers: peers, err: err}
			}(domain)
		}
		wait.Wait()
		close(results)
		for item := range results {
			result.Visited++
			if item.err != nil {
				result.Failed++
				continue
			}
			result.Stats[item.domain] = item.stats
			frontier = append(frontier, item.peers...)
		}
	}
	return result, ctx.Err()
}

func (operations *Operations) crawlDomain(ctx context.Context, domain string) (map[string]any, []string, error) {
	base := "https://" + domain
	instance := map[string]any{}
	if err := operationGetRemoteJSON(ctx, operations.server.cfg, base+"/api/v1/instance", &instance); err != nil {
		return nil, nil, err
	}
	var peers []string
	if err := operationGetRemoteJSON(ctx, operations.server.cfg, base+"/api/v1/instance/peers", &peers); err != nil {
		return nil, nil, err
	}
	var activity any
	if err := operationGetRemoteJSON(ctx, operations.server.cfg, base+"/api/v1/instance/activity", &activity); err == nil {
		instance["activity"] = activity
	}
	return instance, peers, nil
}

func operationGetRemoteJSON(ctx context.Context, cfg config.Config, rawURL string, output any) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || !activityFetchHostAllowed(parsed.Hostname()) {
		return errors.New("remote URL is not safe")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", paonUserAgent(cfg))
	client := operationRemoteHTTPClient()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("remote returned %s", response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	return nil
}

func operationDomainBlocked(domain string, blocked []string) bool {
	for _, candidate := range blocked {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if domain == candidate || strings.HasSuffix(domain, "."+candidate) {
			return true
		}
	}
	return false
}

func (operations *Operations) QueueAccountDeletion(ctx context.Context, username string) error {
	user, err := operations.localUser(ctx, username)
	if err != nil {
		return err
	}
	if !operations.server.enqueueAdminAccountDeletionTask(user.AccountID) {
		return errors.New("account deletion could not be queued")
	}
	return nil
}

func (operations *Operations) LocalAccountSummary(ctx context.Context, username string) (models.User, error) {
	return operations.localUser(ctx, username)
}

func (operations *Operations) localUser(ctx context.Context, username string) (models.User, error) {
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return models.User{}, errors.New("operations database is not configured")
	}
	var user models.User
	err := operations.server.db.WithContext(ctx).Preload("Account").Preload("Role").
		Joins("JOIN accounts ON accounts.id = users.account_id").
		Where("accounts.domain IS NULL AND LOWER(accounts.username) = LOWER(?)", strings.TrimSpace(username)).
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.User{}, fmt.Errorf("local account %q not found", username)
	}
	return user, err
}

func (operations *Operations) SetRegistrationsMode(ctx context.Context, mode string, requireReason *bool) error {
	switch mode {
	case "open", "approved", "none":
	default:
		return fmt.Errorf("unsupported registrations mode %q", mode)
	}
	return operations.server.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := upsertGlobalSetting(tx, "registrations_mode", mode); err != nil {
			return err
		}
		if requireReason != nil {
			return upsertGlobalSetting(tx, "require_invite_text", boolSettingValue(*requireReason))
		}
		return nil
	})
}

func (operations *Operations) DomainAccountCount(ctx context.Context, domain string) (int64, error) {
	var count int64
	err := operations.server.db.WithContext(ctx).Model(&models.Account{}).Where("LOWER(domain) = LOWER(?)", strings.TrimSpace(domain)).Count(&count).Error
	return count, err
}

func (operations *Operations) QueueDomainPurge(ctx context.Context, domain string) error {
	return operations.server.purgeAdminInstanceDomain(ctx, strings.TrimSpace(domain), time.Now().UTC())
}

type OperationVacuumResult struct {
	Statuses     int
	Media        int
	PreviewCards int
	Feeds        int
}

func (operations *Operations) Vacuum(ctx context.Context, family string, now time.Time) (OperationVacuumResult, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result := OperationVacuumResult{}
	switch family {
	case "statuses":
		result.Statuses = operations.server.vacuumRemoteStatuses(ctx, now)
	case "media":
		result.Media = operations.server.vacuumMediaAttachments(ctx, now)
	case "preview-cards":
		result.PreviewCards = operations.server.vacuumCachedPreviewCardImages(ctx, now)
	case "feeds":
		result.Feeds = operations.server.vacuumOrphanedFeeds(ctx, now)
	default:
		return result, fmt.Errorf("unsupported vacuum family %q", family)
	}
	return result, nil
}

type OperationBuildHomeFeedsOptions struct {
	SkipFilledTimelines bool
}

func (operations *Operations) BuildHomeFeeds(ctx context.Context, username string, all bool) (int, error) {
	return operations.BuildHomeFeedsWithOptions(ctx, username, all, OperationBuildHomeFeedsOptions{})
}

func (operations *Operations) BuildHomeFeedsWithOptions(ctx context.Context, username string, all bool, options OperationBuildHomeFeedsOptions) (int, error) {
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return 0, errors.New("operations database is not configured")
	}
	query := operations.server.db.WithContext(ctx).Model(&models.User{}).
		Select("users.account_id", "users.settings").
		Joins("JOIN accounts ON accounts.id = users.account_id").
		Where("accounts.domain IS NULL")
	if all {
		query = query.Where("users.confirmed_at IS NOT NULL AND users.current_sign_in_at >= ? AND accounts.suspended_at IS NULL", time.Now().UTC().Add(-userActiveDuration()))
	} else {
		if strings.TrimSpace(username) == "" {
			return 0, errors.New("username or all=true is required")
		}
		query = query.Where("LOWER(accounts.username) = LOWER(?)", username)
	}
	var users []models.User
	if err := query.Order("users.id ASC").Find(&users).Error; err != nil {
		return 0, err
	}
	if len(users) == 0 {
		if all {
			return 0, nil
		}
		return 0, errors.New("no matching local users")
	}
	built := 0
	for _, user := range users {
		if options.SkipFilledTimelines {
			if err := operations.buildHomeFeedsSkippingFilledTimelines(ctx, user); err != nil {
				return built, err
			}
			built++
			continue
		}
		if err := operations.server.clearHomeFeedCacheContext(ctx, user.AccountID); err != nil {
			return built, err
		}
		var listIDs []int64
		if err := operations.server.db.WithContext(ctx).Model(&models.List{}).Where("account_id = ?", user.AccountID).Pluck("id", &listIDs).Error; err != nil {
			return built, err
		}
		for _, listID := range listIDs {
			if err := operations.server.clearListFeedCacheContext(ctx, listID); err != nil {
				return built, err
			}
		}
		if err := operations.server.populateAccountFeeds(ctx, operations.server.db, user.AccountID, user.Settings); err != nil {
			return built, err
		}
		built++
	}
	return built, nil
}

func (operations *Operations) buildHomeFeedsSkippingFilledTimelines(ctx context.Context, user models.User) error {
	homeFilled, err := operations.server.feedTimelineMoreThanHalfFull(ctx, "home", user.AccountID)
	if err != nil {
		return err
	}
	if !homeFilled {
		if err := operations.server.clearHomeFeedCacheContext(ctx, user.AccountID); err != nil {
			return err
		}
		if err := operations.server.populateHomeFeed(ctx, operations.server.db, user.AccountID, user.Settings); err != nil {
			return err
		}
	}

	var lists []models.List
	if err := operations.server.db.WithContext(ctx).Where("account_id = ?", user.AccountID).Order("id ASC").Find(&lists).Error; err != nil {
		return err
	}
	for _, list := range lists {
		filled, err := operations.server.feedTimelineMoreThanHalfFull(ctx, "list", list.ID)
		if err != nil {
			return err
		}
		if filled {
			continue
		}
		if err := operations.server.clearListFeedCacheContext(ctx, list.ID); err != nil {
			return err
		}
		if err := operations.server.populateListFeed(ctx, list, user.Settings); err != nil {
			return err
		}
	}
	return nil
}

func (operations *Operations) ClearFeeds(ctx context.Context) (int, error) {
	var accountIDs []int64
	if err := operations.server.db.WithContext(ctx).Model(&models.User{}).Pluck("account_id", &accountIDs).Error; err != nil {
		return 0, err
	}
	var listIDs []int64
	if err := operations.server.db.WithContext(ctx).Model(&models.List{}).Pluck("id", &listIDs).Error; err != nil {
		return 0, err
	}
	cleared := 0
	for _, accountID := range accountIDs {
		if err := operations.server.clearHomeFeedCacheContext(ctx, accountID); err != nil {
			return cleared, err
		}
		cleared++
	}
	for _, listID := range listIDs {
		if err := operations.server.clearListFeedCacheContext(ctx, listID); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}

func (operations *Operations) ClearCache(ctx context.Context) (int, error) {
	if operations == nil || operations.server == nil {
		return 0, errors.New("operations server is not configured")
	}
	pattern := cacheRedisConfig(operations.server.cfg).prefix + "*"
	cursor := "0"
	deleted := 0
	for {
		value, err := operations.server.cacheRedisCommand(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", "1000")
		if err != nil {
			return deleted, err
		}
		next, keys, ok := redisScanKeys(value)
		if !ok {
			return deleted, errors.New("cache Redis SCAN returned an invalid response")
		}
		if len(keys) > 0 {
			result, err := operations.server.cacheRedisCommand(ctx, append([]string{"DEL"}, keys...)...)
			if err != nil {
				return deleted, err
			}
			deleted += int(redisInteger(result))
		}
		cursor = next
		if cursor == "0" {
			return deleted, nil
		}
	}
}

func (operations *Operations) RecountCache(ctx context.Context, family string) (int64, error) {
	now := time.Now().UTC()
	switch family {
	case "accounts":
		result := operations.server.db.WithContext(ctx).Exec(`
INSERT INTO account_stats (account_id, statuses_count, following_count, followers_count, created_at, updated_at)
SELECT accounts.id,
       (SELECT COUNT(*) FROM statuses WHERE statuses.account_id = accounts.id AND statuses.visibility <> 3),
       (SELECT COUNT(*) FROM follows WHERE follows.account_id = accounts.id),
       (SELECT COUNT(*) FROM follows WHERE follows.target_account_id = accounts.id),
       ?, ?
FROM accounts
WHERE accounts.domain IS NULL
ON CONFLICT (account_id) DO UPDATE SET
  statuses_count = EXCLUDED.statuses_count,
  following_count = EXCLUDED.following_count,
  followers_count = EXCLUDED.followers_count,
  updated_at = EXCLUDED.updated_at`, now, now)
		return result.RowsAffected, result.Error
	case "statuses":
		result := operations.server.db.WithContext(ctx).Exec(`
INSERT INTO status_stats (status_id, replies_count, reblogs_count, favourites_count, created_at, updated_at)
SELECT statuses.id,
       (SELECT COUNT(*) FROM statuses replies WHERE replies.in_reply_to_id = statuses.id AND replies.visibility <> 3),
       (SELECT COUNT(*) FROM statuses reblogs WHERE reblogs.reblog_of_id = statuses.id),
       (SELECT COUNT(*) FROM favourites WHERE favourites.status_id = statuses.id),
       ?, ?
FROM statuses
ON CONFLICT (status_id) DO UPDATE SET
  replies_count = EXCLUDED.replies_count,
  reblogs_count = EXCLUDED.reblogs_count,
  favourites_count = EXCLUDED.favourites_count,
  updated_at = EXCLUDED.updated_at`, now, now)
		return result.RowsAffected, result.Error
	default:
		return 0, fmt.Errorf("unsupported cache recount family %q", family)
	}
}
