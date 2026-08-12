package migrate

import (
	"fmt"
	"sort"
	"strings"

	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"gorm.io/gorm"
)

// mastodon4219UpgradePrerequisites is the reviewed base shape directly read or
// altered by the phased migration. It deliberately describes only 4.2 objects;
// additive 4.3 objects may be absent on a pristine database or present on a
// resumed one. This prevents a marker-only, partially restored 4.2 database
// from committing data backfills before its malformed base is discovered.
func mastodon4219UpgradePrerequisites() map[string][]string {
	prerequisites := map[string][]string{
		"account_aliases":        {"id", "account_id", "uri"},
		"account_summaries":      {"account_id", "language", "sensitive"},
		"accounts":               {"id", "devices_url"},
		"custom_filter_statuses": {"id", "custom_filter_id", "status_id"},
		"devices":                {"id"},
		"email_domain_blocks":    {"id"},
		"encrypted_messages":     {"id"},
		"identities":             {"id", "uid", "provider"},
		"mentions":               {"id", "status_id", "account_id"},
		"notifications":          {"id", "account_id", "type"},
		"oauth_access_grants":    {"id"},
		"oauth_access_tokens":    {"id", "scopes"},
		"oauth_applications":     {"id", "scopes"},
		"one_time_keys":          {"id"},
		"preview_cards":          {"id"},
		"preview_cards_statuses": {"preview_card_id", "status_id"},
		"reports":                {"id"},
		"rules":                  {"id"},
		"status_pins":            {"id", "created_at", "updated_at"},
		"system_keys":            {"id"},
		"users":                  {"id", "account_id", "admin", "moderator", "locale", "settings", "otp_required_for_login", "encrypted_otp_secret", "encrypted_otp_secret_iv", "encrypted_otp_secret_salt"},
		"webauthn_credentials":   {"id", "user_id", "nickname"},
	}
	createdIn43 := map[string]struct{}{
		"account_relationship_severance_events": {},
		"follow_recommendation_mutes":           {},
		"generated_annual_reports":              {},
		"notification_permissions":              {},
		"notification_policies":                 {},
		"notification_requests":                 {},
		"relationship_severance_events":         {},
		"severed_relationships":                 {},
	}
	addedColumns := map[string]map[string]struct{}{
		"accounts":               {"attribution_domains": {}},
		"email_domain_blocks":    {"allow_with_approval": {}},
		"notifications":          {"filtered": {}, "group_key": {}},
		"oauth_access_grants":    {"code_challenge": {}, "code_challenge_method": {}},
		"preview_cards":          {"author_account_id": {}},
		"preview_cards_statuses": {"url": {}},
		"reports":                {"application_id": {}},
		"rules":                  {"hint": {}},
		"users":                  {"otp_secret": {}},
	}
	for _, table := range paondb.RequiredMastodonTables() {
		if _, added := createdIn43[table]; added {
			continue
		}
		if _, exists := prerequisites[table]; !exists {
			prerequisites[table] = nil
		}
	}
	for table, columns := range paondb.RequiredMastodonColumns() {
		if _, added := createdIn43[table]; added {
			continue
		}
		for _, column := range columns {
			if _, added := addedColumns[table][column]; added {
				continue
			}
			if !containsString(prerequisites[table], column) {
				prerequisites[table] = append(prerequisites[table], column)
			}
		}
	}
	return prerequisites
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateMastodon4219UpgradePrerequisites(tx *gorm.DB) error {
	missing := make([]string, 0)
	for table, columns := range mastodon4219UpgradePrerequisites() {
		var relationAvailable bool
		if err := tx.Raw(`SELECT to_regclass(?) IS NOT NULL`, table).Scan(&relationAvailable).Error; err != nil {
			return fmt.Errorf("inspect Mastodon 4.2 prerequisite relation %s: %w", table, err)
		}
		if !relationAvailable {
			missing = append(missing, table)
			continue
		}
		var availableColumns []string
		if err := tx.Raw(`SELECT a.attname FROM pg_attribute a WHERE a.attrelid = to_regclass(?) AND a.attnum > 0 AND NOT a.attisdropped`, table).Scan(&availableColumns).Error; err != nil {
			return fmt.Errorf("inspect Mastodon 4.2 prerequisite columns for %s: %w", table, err)
		}
		available := make(map[string]struct{}, len(availableColumns))
		for _, column := range availableColumns {
			available[strings.ToLower(column)] = struct{}{}
		}
		for _, column := range columns {
			if _, ok := available[strings.ToLower(column)]; !ok {
				missing = append(missing, table+"."+column)
			}
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("Mastodon 4.2 schema is missing migration prerequisites: %s; restore or repair the reviewed 4.2 schema before retrying", strings.Join(missing, ", "))
	}
	return nil
}
