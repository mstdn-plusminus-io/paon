package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	currentStorageSchemaVersion     = 1
	storageSchemaMoveMaxAttempts    = 3
	storageSchemaMoveBaseRetryDelay = 100 * time.Millisecond
)

type OperationStorageSchemaInventory struct {
	AccountAvatars int64
	AccountHeaders int64
	CustomEmojis   int64
	Media          int64
	PreviewCards   int64
}

func (inventory OperationStorageSchemaInventory) Total() int64 {
	return inventory.AccountAvatars + inventory.AccountHeaders + inventory.CustomEmojis + inventory.Media + inventory.PreviewCards
}

func (operations *Operations) StorageSchemaInventory(ctx context.Context) (OperationStorageSchemaInventory, error) {
	var out OperationStorageSchemaInventory
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return out, errors.New("operations database is not configured")
	}
	queries := []struct {
		table       string
		filename    string
		version     string
		destination *int64
	}{
		{"accounts", "avatar_file_name", "avatar_storage_schema_version", &out.AccountAvatars},
		{"accounts", "header_file_name", "header_storage_schema_version", &out.AccountHeaders},
		{"custom_emojis", "image_file_name", "image_storage_schema_version", &out.CustomEmojis},
		{"media_attachments", "file_file_name", "file_storage_schema_version", &out.Media},
		{"preview_cards", "image_file_name", "image_storage_schema_version", &out.PreviewCards},
	}
	for _, query := range queries {
		if err := operations.server.db.WithContext(ctx).Table(query.table).
			Where(query.filename+" IS NOT NULL AND "+query.filename+" <> ''").
			Where("COALESCE("+query.version+", 0) < ?", currentStorageSchemaVersion).
			Count(query.destination).Error; err != nil {
			return out, err
		}
	}
	return out, nil
}

func (operations *Operations) UpgradeStorageSchema(ctx context.Context, dryRun bool) (int, error) {
	if operations == nil || operations.server == nil || operations.server.db == nil {
		return 0, errors.New("operations database is not configured")
	}
	upgraded := 0
	var accounts []models.Account
	if err := operations.server.db.WithContext(ctx).
		Where("(avatar_file_name IS NOT NULL AND avatar_file_name <> '' AND COALESCE(avatar_storage_schema_version, 0) < ?) OR (header_file_name IS NOT NULL AND header_file_name <> '' AND COALESCE(header_storage_schema_version, 0) < ?)", currentStorageSchemaVersion, currentStorageSchemaVersion).
		Order("id ASC").Find(&accounts).Error; err != nil {
		return upgraded, err
	}
	for _, account := range accounts {
		if account.AvatarFileName.Valid && account.AvatarFileName.String != "" && storageSchemaOutdated(account.AvatarStorageSchemaVersion) {
			oldKeys := []string{
				accountImageObjectKeyWithCachePrefix(account.ID, "avatar", "original", account.AvatarFileName.String, false),
				accountImageObjectKeyWithCachePrefix(account.ID, "avatar", "static", profileImageStaticFilename(account.AvatarFileName.String, account.AvatarContentType.String), false),
			}
			newKeys := []string{
				accountImageObjectKeyWithCachePrefix(account.ID, "avatar", "original", account.AvatarFileName.String, !account.Local()),
				accountImageObjectKeyWithCachePrefix(account.ID, "avatar", "static", profileImageStaticFilename(account.AvatarFileName.String, account.AvatarContentType.String), !account.Local()),
			}
			if err := operations.upgradeStorageAttachment(ctx, oldKeys, newKeys, dryRun); err != nil {
				return upgraded, fmt.Errorf("upgrade account avatar id=%d: %w", account.ID, err)
			}
			if !dryRun {
				if err := operations.server.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", account.ID).Update("avatar_storage_schema_version", currentStorageSchemaVersion).Error; err != nil {
					return upgraded, err
				}
			}
			upgraded++
		}
		if account.HeaderFileName.Valid && account.HeaderFileName.String != "" && storageSchemaOutdated(account.HeaderStorageSchemaVersion) {
			oldKeys := []string{
				accountImageObjectKeyWithCachePrefix(account.ID, "header", "original", account.HeaderFileName.String, false),
				accountImageObjectKeyWithCachePrefix(account.ID, "header", "static", profileImageStaticFilename(account.HeaderFileName.String, account.HeaderContentType.String), false),
			}
			newKeys := []string{
				accountImageObjectKeyWithCachePrefix(account.ID, "header", "original", account.HeaderFileName.String, !account.Local()),
				accountImageObjectKeyWithCachePrefix(account.ID, "header", "static", profileImageStaticFilename(account.HeaderFileName.String, account.HeaderContentType.String), !account.Local()),
			}
			if err := operations.upgradeStorageAttachment(ctx, oldKeys, newKeys, dryRun); err != nil {
				return upgraded, fmt.Errorf("upgrade account header id=%d: %w", account.ID, err)
			}
			if !dryRun {
				if err := operations.server.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", account.ID).Update("header_storage_schema_version", currentStorageSchemaVersion).Error; err != nil {
					return upgraded, err
				}
			}
			upgraded++
		}
	}

	var emojis []models.CustomEmoji
	if err := operations.server.db.WithContext(ctx).Where("image_file_name IS NOT NULL AND image_file_name <> '' AND COALESCE(image_storage_schema_version, 0) < ?", currentStorageSchemaVersion).Order("id ASC").Find(&emojis).Error; err != nil {
		return upgraded, err
	}
	for _, emoji := range emojis {
		oldKeys := []string{
			customEmojiStorageObjectKey(emoji.ID, "original", emoji.ImageFileName.String, false),
			customEmojiStorageObjectKey(emoji.ID, "static", customEmojiStaticFilename(emoji.ImageFileName.String), false),
		}
		newKeys := []string{
			customEmojiStorageObjectKey(emoji.ID, "original", emoji.ImageFileName.String, !emoji.Local()),
			customEmojiStorageObjectKey(emoji.ID, "static", customEmojiStaticFilename(emoji.ImageFileName.String), !emoji.Local()),
		}
		if err := operations.upgradeStorageAttachment(ctx, oldKeys, newKeys, dryRun); err != nil {
			return upgraded, fmt.Errorf("upgrade custom emoji id=%d: %w", emoji.ID, err)
		}
		if !dryRun {
			if err := operations.server.db.WithContext(ctx).Model(&models.CustomEmoji{}).Where("id = ?", emoji.ID).Update("image_storage_schema_version", currentStorageSchemaVersion).Error; err != nil {
				return upgraded, err
			}
		}
		upgraded++
	}

	var media []models.MediaAttachment
	if err := operations.server.db.WithContext(ctx).Where("file_file_name IS NOT NULL AND file_file_name <> '' AND COALESCE(file_storage_schema_version, 0) < ?", currentStorageSchemaVersion).Order("id ASC").Find(&media).Error; err != nil {
		return upgraded, err
	}
	for _, attachment := range media {
		cachePrefix := strings.TrimSpace(attachment.RemoteURL) != ""
		oldKeys := []string{mediaAttachmentObjectKeyWithCachePrefix(attachment.ID, "files", "original", attachment.FileFileName.String, false)}
		newKeys := []string{mediaAttachmentObjectKeyWithCachePrefix(attachment.ID, "files", "original", attachment.FileFileName.String, cachePrefix)}
		if attachment.ThumbnailFileName.Valid && attachment.ThumbnailFileName.String != "" {
			oldKeys = append(oldKeys,
				mediaAttachmentObjectKeyWithCachePrefix(attachment.ID, "files", "small", attachment.ThumbnailFileName.String, false),
				mediaAttachmentObjectKeyWithCachePrefix(attachment.ID, "thumbnails", "original", attachment.ThumbnailFileName.String, false),
			)
			newKeys = append(newKeys,
				mediaAttachmentObjectKeyWithCachePrefix(attachment.ID, "files", "small", attachment.ThumbnailFileName.String, cachePrefix),
				mediaAttachmentObjectKeyWithCachePrefix(attachment.ID, "thumbnails", "original", attachment.ThumbnailFileName.String, cachePrefix),
			)
		}
		if err := operations.upgradeStorageAttachment(ctx, oldKeys, newKeys, dryRun); err != nil {
			return upgraded, fmt.Errorf("upgrade media attachment id=%d: %w", attachment.ID, err)
		}
		if !dryRun {
			if err := operations.server.db.WithContext(ctx).Model(&models.MediaAttachment{}).Where("id = ?", attachment.ID).Update("file_storage_schema_version", currentStorageSchemaVersion).Error; err != nil {
				return upgraded, err
			}
		}
		upgraded++
	}

	var cards []models.PreviewCard
	if err := operations.server.db.WithContext(ctx).Where("image_file_name IS NOT NULL AND image_file_name <> '' AND COALESCE(image_storage_schema_version, 0) < ?", currentStorageSchemaVersion).Order("id ASC").Find(&cards).Error; err != nil {
		return upgraded, err
	}
	for _, card := range cards {
		oldKey := previewCardStorageObjectKey(card.ID, card.ImageFileName.String, false)
		newKey := previewCardStorageObjectKey(card.ID, card.ImageFileName.String, true)
		if err := operations.upgradeStorageAttachment(ctx, []string{oldKey}, []string{newKey}, dryRun); err != nil {
			return upgraded, fmt.Errorf("upgrade preview card id=%d: %w", card.ID, err)
		}
		if !dryRun {
			if err := operations.server.db.WithContext(ctx).Model(&models.PreviewCard{}).Where("id = ?", card.ID).Update("image_storage_schema_version", currentStorageSchemaVersion).Error; err != nil {
				return upgraded, err
			}
		}
		upgraded++
	}
	return upgraded, nil
}

func storageSchemaOutdated(version sql.NullInt64) bool {
	return !version.Valid || version.Int64 < currentStorageSchemaVersion
}

func customEmojiStorageObjectKey(id int64, style string, filename string, cachePrefix bool) string {
	prefix := "custom_emojis"
	if cachePrefix {
		prefix = "cache/custom_emojis"
	}
	return prefix + "/images/" + strings.ReplaceAll(adminPaperclipIDPartition(id), string(os.PathSeparator), "/") + "/" + style + "/" + filename
}

func previewCardStorageObjectKey(id int64, filename string, cachePrefix bool) string {
	prefix := "preview_cards"
	if cachePrefix {
		prefix = "cache/preview_cards"
	}
	return prefix + "/images/" + strings.ReplaceAll(mediaPaperclipIDPartition(id), string(os.PathSeparator), "/") + "/original/" + filename
}

func (operations *Operations) upgradeStorageAttachment(ctx context.Context, oldKeys []string, newKeys []string, dryRun bool) error {
	if len(oldKeys) != len(newKeys) {
		return errors.New("storage schema key inventory mismatch")
	}
	for index := range oldKeys {
		if oldKeys[index] == newKeys[index] || dryRun {
			continue
		}
		if err := operations.moveStorageObject(ctx, oldKeys[index], newKeys[index]); err != nil {
			return err
		}
	}
	return nil
}

func (operations *Operations) moveStorageObject(ctx context.Context, oldKey string, newKey string) error {
	log.Printf("level=INFO event=storage_schema_object_move source_key=%q target_key=%q", oldKey, newKey)
	var lastErr error
	for attempt := 1; attempt <= storageSchemaMoveMaxAttempts; attempt++ {
		lastErr = operations.moveStorageObjectOnce(ctx, oldKey, newKey)
		if lastErr == nil {
			return nil
		}
		if !storageSchemaMoveRetryable(lastErr) || attempt == storageSchemaMoveMaxAttempts {
			break
		}
		delay := time.Duration(attempt) * storageSchemaMoveBaseRetryDelay
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return operations.safeStorageSchemaError(lastErr)
}

func (operations *Operations) moveStorageObjectOnce(ctx context.Context, oldKey string, newKey string) error {
	s := operations.server
	if s.s3ObjectStorageEnabled() {
		storage, err := s.s3SDK(ctx)
		if err != nil {
			return err
		}
		bucket := strings.Trim(s.cfg.S3Bucket, "/")
		copySource := url.PathEscape(bucket + "/" + s.cfg.S3ObjectKey(oldKey))
		input := &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String(s.cfg.S3ObjectKey(newKey)), CopySource: aws.String(copySource)}
		if permission := strings.TrimSpace(s.cfg.S3Permission); permission != "" {
			input.ACL = s3types.ObjectCannedACL(permission)
		}
		if _, err := storage.client.CopyObject(ctx, input); err != nil {
			if s3SDKHTTPStatusCode(err) == http.StatusNotFound {
				return nil
			}
			return err
		}
		return s.deleteS3Object(ctx, oldKey)
	}
	if s.azureObjectStorageEnabled() {
		if err := s.copyAzureBlobObject(ctx, oldKey, newKey); err != nil {
			return err
		}
		return s.deleteAzureBlobObject(ctx, oldKey)
	}
	if s.swiftObjectStorageEnabled() {
		if err := s.copySwiftObject(ctx, oldKey, newKey); err != nil {
			return err
		}
		return s.deleteSwiftObject(ctx, oldKey)
	}
	oldPath := s.cfg.SystemAssetPath(strings.Split(oldKey, "/")...)
	newPath := s.cfg.SystemAssetPath(strings.Split(newKey, "/")...)
	if _, err := os.Stat(oldPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return err
	}
	return os.Rename(oldPath, newPath)
}

func storageSchemaMoveRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}
	var httpError s3HTTPError
	if errors.As(err, &httpError) {
		return httpError.status == http.StatusRequestTimeout || httpError.status == http.StatusTooManyRequests || httpError.status >= http.StatusInternalServerError
	}
	status := s3SDKHTTPStatusCode(err)
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// safeStorageSchemaError removes configured credentials from errors before the
// administrative CLI prints them. Object keys remain visible through the
// structured move log, but passwords, access keys, and session tokens do not.
func (operations *Operations) safeStorageSchemaError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if operations != nil && operations.server != nil {
		cfg := operations.server.cfg
		for _, secret := range []string{
			cfg.S3AccessKeyID,
			cfg.S3SecretAccessKey,
			cfg.S3SessionToken,
			cfg.AzureStorageAccessKey,
			cfg.SwiftPassword,
			cfg.SwiftTempURLKey,
		} {
			if secret = strings.TrimSpace(secret); len(secret) >= 4 {
				message = strings.ReplaceAll(message, secret, "[REDACTED]")
			}
		}
	}
	return errors.New(message)
}
