package migrate

import (
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	paonotp "github.com/mstdn-plusminus-io/paon/internal/paon/otp"
	"gorm.io/gorm"
)

const migrationBatchSize = 500

func preflightMastodon43Validate(tx *gorm.DB) error {
	var nullMentions struct {
		Status  int64
		Account int64
	}
	if err := tx.Raw(`SELECT COUNT(*) FILTER (WHERE status_id IS NULL) AS status, COUNT(*) FILTER (WHERE account_id IS NULL) AS account FROM mentions`).Scan(&nullMentions).Error; err != nil {
		return fmt.Errorf("Mastodon 4.3 preflight mentions: %w", err)
	}
	if nullMentions.Status != 0 || nullMentions.Account != 0 {
		return fmt.Errorf("Mastodon 4.3 preflight failed: mentions.status_id has %d NULL rows and mentions.account_id has %d NULL rows; repair explicitly before retrying", nullMentions.Status, nullMentions.Account)
	}
	return nil
}

func preflightMastodon43Backfill(tx *gorm.DB, options Options) error {
	var enabledOTPUsers int64
	if err := tx.Raw(`SELECT COUNT(*) FROM users WHERE otp_required_for_login = true`).Scan(&enabledOTPUsers).Error; err != nil {
		return fmt.Errorf("Mastodon 4.3 preflight OTP users: %w", err)
	}
	if enabledOTPUsers == 0 {
		return nil
	}
	if err := options.ActiveRecordEncryption.Validate(); err != nil {
		return fmt.Errorf("Mastodon 4.3 preflight OTP encryption: %w", err)
	}
	var mastodonLegacyUsers int64
	if err := tx.Raw(`SELECT COUNT(*) FROM users WHERE otp_required_for_login = true AND otp_secret IS NULL AND (encrypted_otp_secret IS NULL OR encrypted_otp_secret NOT LIKE ?)`, paonotp.LegacyPaonPrefix+"%").Scan(&mastodonLegacyUsers).Error; err != nil {
		return fmt.Errorf("Mastodon 4.3 preflight legacy OTP formats: %w", err)
	}
	if mastodonLegacyUsers != 0 && strings.TrimSpace(options.OTPSecret) == "" {
		return errors.New("Mastodon 4.3 preflight OTP encryption: OTP_SECRET is required for legacy Mastodon secrets")
	}
	return nil
}

type notificationSettingsRow struct {
	ID        int64
	AccountID int64
	Settings  sql.NullString
}

func applyNotificationPolicyBackfill(tx *gorm.DB, version string, preserveExisting bool) error {
	applied, err := upgradeVersionApplied(tx, version)
	if err != nil || applied {
		return err
	}
	lastID := int64(0)
	for {
		var rows []notificationSettingsRow
		if err := tx.Raw(`SELECT id, account_id, settings FROM users WHERE id > ? ORDER BY id ASC LIMIT ?`, lastID, migrationBatchSize).Scan(&rows).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 notification policy backfill %s: %w", version, err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			lastID = row.ID
			policy, required, err := notificationPolicyFromSettings(row.Settings)
			if err != nil {
				return fmt.Errorf("Mastodon 4.3 notification settings for user id=%d: %w", row.ID, err)
			}
			if !required {
				continue
			}
			conflict := `DO UPDATE SET filter_not_following = EXCLUDED.filter_not_following, filter_not_followers = EXCLUDED.filter_not_followers, filter_private_mentions = EXCLUDED.filter_private_mentions, updated_at = EXCLUDED.updated_at`
			if preserveExisting {
				conflict = `DO NOTHING`
			}
			statement := `INSERT INTO notification_policies (account_id, filter_not_following, filter_not_followers, filter_new_accounts, filter_private_mentions, created_at, updated_at) VALUES (?, ?, ?, false, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP) ON CONFLICT (account_id) ` + conflict
			if err := tx.Exec(statement, row.AccountID, policy.FilterNotFollowing, policy.FilterNotFollowers, policy.FilterPrivateMentions).Error; err != nil {
				return fmt.Errorf("Mastodon 4.3 notification policy for user id=%d: %w", row.ID, err)
			}
		}
		if len(rows) < migrationBatchSize {
			break
		}
	}
	return recordUpgradeVersion(tx, version)
}

type legacyNotificationPolicy struct {
	FilterNotFollowing    bool
	FilterNotFollowers    bool
	FilterPrivateMentions bool
}

func notificationPolicyFromSettings(settings sql.NullString) (legacyNotificationPolicy, bool, error) {
	policy := legacyNotificationPolicy{FilterPrivateMentions: true}
	if !settings.Valid || strings.TrimSpace(settings.String) == "" || strings.TrimSpace(settings.String) == "null" {
		return policy, false, nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(settings.String), &values); err != nil {
		return policy, false, errors.New("settings are not valid JSON")
	}
	required := false
	if settingTruthy(values, "interactions.must_be_follower") {
		policy.FilterNotFollowers = true
		required = true
	}
	if settingTruthy(values, "interactions.must_be_following") {
		policy.FilterNotFollowing = true
		required = true
	}
	if !settingTruthy(values, "interactions.must_be_following_dm") {
		policy.FilterPrivateMentions = false
		required = true
	}
	return policy, required, nil
}

func settingTruthy(values map[string]any, key string) bool {
	value, exists := values[key]
	if !exists || value == nil {
		return false
	}
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	// Oj deserializes JSON values into Ruby objects. Ruby treats every value
	// except false and nil as truthy, including 0, empty strings, and "false".
	return true
}

type legacyOTPRow struct {
	ID                     int64
	EncryptedOTPSecret     sql.NullString
	EncryptedOTPSecretIV   sql.NullString
	EncryptedOTPSecretSalt sql.NullString
}

func applyOTPSecretBackfill(tx *gorm.DB, options Options) error {
	const version = "20240307180905"
	applied, err := upgradeVersionApplied(tx, version)
	if err != nil || applied {
		return err
	}
	lastID := int64(0)
	for {
		var rows []legacyOTPRow
		if err := tx.Raw(`SELECT id, encrypted_otp_secret, encrypted_otp_secret_iv, encrypted_otp_secret_salt FROM users WHERE otp_required_for_login = true AND otp_secret IS NULL AND id > ? ORDER BY id ASC LIMIT ?`, lastID, migrationBatchSize).Scan(&rows).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 OTP backfill query: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			lastID = row.ID
			clearText, err := decryptMigrationOTPSecret(row, options)
			if err != nil {
				if options.IgnoreInvalidOTPSecret {
					logMigration(options, "Mastodon 4.3 OTP migration skipped invalid secret for user id=%d", row.ID)
					continue
				}
				return fmt.Errorf("Mastodon 4.3 OTP migration cannot decrypt secret for user id=%d; verify OTP_SECRET or set MIGRATION_IGNORE_INVALID_OTP_SECRET=true to skip this user", row.ID)
			}
			encrypted, err := paonotp.EncryptActiveRecord(clearText, options.ActiveRecordEncryption)
			if err != nil {
				return fmt.Errorf("Mastodon 4.3 OTP migration encrypt user id=%d: %w", row.ID, err)
			}
			verified, err := paonotp.DecryptActiveRecord(encrypted, options.ActiveRecordEncryption)
			if err != nil || verified != clearText || !sameMigrationTOTPCode(clearText, verified) {
				return fmt.Errorf("Mastodon 4.3 OTP migration verification failed for user id=%d", row.ID)
			}
			result := tx.Exec(`UPDATE users SET otp_secret = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND otp_secret IS NULL`, encrypted, row.ID)
			if result.Error != nil {
				return fmt.Errorf("Mastodon 4.3 OTP migration write user id=%d: %w", row.ID, result.Error)
			}
		}
		if len(rows) < migrationBatchSize {
			break
		}
	}
	return recordUpgradeVersion(tx, version)
}

func decryptMigrationOTPSecret(row legacyOTPRow, options Options) (string, error) {
	if row.EncryptedOTPSecret.Valid && strings.HasPrefix(strings.TrimSpace(row.EncryptedOTPSecret.String), paonotp.LegacyPaonPrefix) {
		secret, ok := paonotp.ParseLegacyPaon(row.EncryptedOTPSecret.String)
		if !ok {
			return "", errors.New("invalid legacy Paon OTP secret")
		}
		return normalizeMigrationOTPSecret(secret), nil
	}
	secret, err := paonotp.DecryptLegacyMastodon(row.EncryptedOTPSecret.String, row.EncryptedOTPSecretIV.String, row.EncryptedOTPSecretSalt.String, options.OTPSecret)
	if err != nil {
		return "", err
	}
	secret = normalizeMigrationOTPSecret(secret)
	if secret == "" {
		return "", errors.New("empty legacy Mastodon OTP secret")
	}
	return secret, nil
}

func normalizeMigrationOTPSecret(secret string) string {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	secret = strings.ReplaceAll(secret, " ", "")
	return strings.TrimRight(secret, "=")
}

func sameMigrationTOTPCode(left string, right string) bool {
	const verificationUnixTime = int64(1_700_000_000)
	leftCode, leftErr := migrationTOTPCode(left, verificationUnixTime)
	rightCode, rightErr := migrationTOTPCode(right, verificationUnixTime)
	return leftErr == nil && rightErr == nil && hmac.Equal([]byte(leftCode), []byte(rightCode))
}

func migrationTOTPCode(secret string, unixTime int64) (string, error) {
	secret = normalizeMigrationOTPSecret(secret)
	padding := (8 - len(secret)%8) % 8
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil && padding != 0 {
		key, err = base32.StdEncoding.DecodeString(secret + strings.Repeat("=", padding))
	}
	if err != nil {
		return "", err
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(unixTime/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000), nil
}

func validateMastodon43UpgradeData(tx *gorm.DB, options Options) error {
	checks := []struct {
		name  string
		query string
	}{
		{name: "NULL mentions", query: `SELECT COUNT(*) FROM mentions WHERE status_id IS NULL OR account_id IS NULL`},
		{name: "duplicate account aliases", query: `SELECT COUNT(*) FROM (SELECT 1 FROM account_aliases WHERE account_id IS NOT NULL AND uri IS NOT NULL GROUP BY account_id, uri HAVING COUNT(*) > 1) duplicates`},
		{name: "duplicate custom filter statuses", query: `SELECT COUNT(*) FROM (SELECT 1 FROM custom_filter_statuses WHERE status_id IS NOT NULL AND custom_filter_id IS NOT NULL GROUP BY status_id, custom_filter_id HAVING COUNT(*) > 1) duplicates`},
		{name: "duplicate identities", query: `SELECT COUNT(*) FROM (SELECT 1 FROM identities WHERE uid IS NOT NULL AND provider IS NOT NULL GROUP BY uid, provider HAVING COUNT(*) > 1) duplicates`},
		{name: "duplicate WebAuthn nicknames", query: `SELECT COUNT(*) FROM (SELECT 1 FROM webauthn_credentials WHERE user_id IS NOT NULL AND nickname IS NOT NULL GROUP BY user_id, nickname HAVING COUNT(*) > 1) duplicates`},
		{name: "Canadian French legacy locales", query: `SELECT COUNT(*) FROM users WHERE locale = 'fr-QC'`},
		{name: "legacy read:me scopes", query: `SELECT (SELECT COUNT(*) FROM oauth_applications WHERE scopes LIKE '%read:me%') + (SELECT COUNT(*) FROM oauth_access_tokens WHERE scopes LIKE '%read:me%')`},
		{name: "invalid notification policy enum", query: `SELECT COUNT(*) FROM notification_policies WHERE for_not_following NOT IN (0,1,2) OR for_not_followers NOT IN (0,1,2) OR for_new_accounts NOT IN (0,1,2) OR for_private_mentions NOT IN (0,1,2) OR for_limited_accounts NOT IN (0,1,2)`},
		{name: "notification policy v2 mismatch", query: `SELECT COUNT(*) FROM notification_policies WHERE for_not_following <> CASE WHEN filter_not_following THEN 1 ELSE 0 END OR for_not_followers <> CASE WHEN filter_not_followers THEN 1 ELSE 0 END OR for_new_accounts <> CASE WHEN filter_new_accounts THEN 1 ELSE 0 END OR for_private_mentions <> CASE WHEN filter_private_mentions THEN 1 ELSE 0 END`},
	}
	for _, check := range checks {
		var count int64
		if err := tx.Raw(check.query).Scan(&count).Error; err != nil {
			return fmt.Errorf("Mastodon 4.3 validate %s: %w", check.name, err)
		}
		if count != 0 {
			return fmt.Errorf("Mastodon 4.3 validate failed: %s count=%d", check.name, count)
		}
	}

	type encryptedOTPRow struct {
		ID        int64
		OTPSecret sql.NullString
	}
	var rows []encryptedOTPRow
	if err := tx.Raw(`SELECT id, otp_secret FROM users WHERE otp_required_for_login = true ORDER BY id`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("Mastodon 4.3 validate OTP rows: %w", err)
	}
	for _, row := range rows {
		if !row.OTPSecret.Valid {
			if options.IgnoreInvalidOTPSecret {
				logMigration(options, "Mastodon 4.3 OTP validation retained NULL for skipped user id=%d", row.ID)
				continue
			}
			return fmt.Errorf("Mastodon 4.3 validate failed: enabled user id=%d has no migrated OTP secret", row.ID)
		}
		secret, err := paonotp.DecryptActiveRecord(row.OTPSecret.String, options.ActiveRecordEncryption)
		if err != nil || normalizeMigrationOTPSecret(secret) == "" {
			return fmt.Errorf("Mastodon 4.3 validate failed: OTP secret is not decryptable for user id=%d", row.ID)
		}
	}
	return nil
}

func validateMastodon43ContractData(tx *gorm.DB) error {
	var cryptoScopes int64
	if err := tx.Raw(`SELECT (SELECT COUNT(*) FROM oauth_applications WHERE scopes LIKE '%crypto%') + (SELECT COUNT(*) FROM oauth_access_tokens WHERE scopes LIKE '%crypto%')`).Scan(&cryptoScopes).Error; err != nil {
		return fmt.Errorf("Mastodon 4.3 validate crypto scopes: %w", err)
	}
	if cryptoScopes != 0 {
		return fmt.Errorf("Mastodon 4.3 validate failed: legacy crypto scope count=%d", cryptoScopes)
	}
	return nil
}
