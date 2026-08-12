package models

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type NullSafeString string

func (s *NullSafeString) Scan(value any) error {
	if value == nil {
		*s = ""
		return nil
	}
	switch v := value.(type) {
	case string:
		*s = NullSafeString(v)
	case []byte:
		*s = NullSafeString(string(v))
	default:
		return fmt.Errorf("unsupported nullable string value %T", value)
	}
	return nil
}

func (s NullSafeString) Value() (driver.Value, error) {
	return string(s), nil
}

type Int64Array []int64

func (Int64Array) GormDataType() string { return "bigint[]" }

func (a *Int64Array) Scan(value any) error {
	if value == nil {
		*a = nil
		return nil
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("unsupported int64 array value %T", value)
	}
	text = strings.Trim(text, "{}")
	if text == "" {
		*a = Int64Array{}
		return nil
	}
	parts := strings.Split(text, ",")
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(strings.Trim(part, `"`)), 10, 64)
		if err != nil {
			return err
		}
		out = append(out, id)
	}
	*a = out
	return nil
}

func (a Int64Array) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	parts := make([]string, 0, len(a))
	for _, id := range a {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

type StringArray []string

func (StringArray) GormDataType() string { return "text[]" }

func (a *StringArray) Scan(value any) error {
	if value == nil {
		*a = nil
		return nil
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("unsupported string array value %T", value)
	}
	*a = parsePostgresStringArray(text)
	return nil
}

func (a StringArray) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	parts := make([]string, 0, len(a))
	for _, item := range a {
		escaped := strings.ReplaceAll(item, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		parts = append(parts, `"`+escaped+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

func parsePostgresStringArray(text string) []string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(strings.TrimSuffix(text, "}"), "{")
	if text == "" {
		return []string{}
	}
	out := []string{}
	var current strings.Builder
	inQuote := false
	escaped := false
	for _, r := range text {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	out = append(out, current.String())
	return out
}

type JSONValue []byte

func (j *JSONValue) Scan(value any) error {
	if value == nil {
		*j = nil
		return nil
	}
	switch v := value.(type) {
	case string:
		*j = JSONValue(v)
	case []byte:
		*j = JSONValue(v)
	default:
		return fmt.Errorf("unsupported json value %T", value)
	}
	return nil
}

func (j JSONValue) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "{}", nil
	}
	if !json.Valid(j) {
		return nil, fmt.Errorf("invalid json value")
	}
	return string(j), nil
}

type AccountStatusesCleanupPolicy struct {
	ID               int64         `gorm:"primaryKey;column:id"`
	AccountID        int64         `gorm:"column:account_id"`
	Enabled          bool          `gorm:"column:enabled"`
	MinStatusAge     int           `gorm:"column:min_status_age"`
	KeepDirect       bool          `gorm:"column:keep_direct"`
	KeepPinned       bool          `gorm:"column:keep_pinned"`
	KeepPolls        bool          `gorm:"column:keep_polls"`
	KeepMedia        bool          `gorm:"column:keep_media"`
	KeepSelfFav      bool          `gorm:"column:keep_self_fav"`
	KeepSelfBookmark bool          `gorm:"column:keep_self_bookmark"`
	MinFavs          sql.NullInt64 `gorm:"column:min_favs"`
	MinReblogs       sql.NullInt64 `gorm:"column:min_reblogs"`
	CreatedAt        time.Time     `gorm:"column:created_at"`
	UpdatedAt        time.Time     `gorm:"column:updated_at"`
	Account          Account       `gorm:"foreignKey:AccountID"`
}

func (AccountStatusesCleanupPolicy) TableName() string { return "account_statuses_cleanup_policies" }

type Account struct {
	ID                         int64          `gorm:"primaryKey;column:id"`
	Username                   string         `gorm:"column:username"`
	Domain                     sql.NullString `gorm:"column:domain"`
	PrivateKey                 sql.NullString `gorm:"column:private_key"`
	CreatedAt                  time.Time      `gorm:"column:created_at"`
	UpdatedAt                  time.Time      `gorm:"column:updated_at"`
	PublicKey                  string         `gorm:"column:public_key"`
	Note                       string         `gorm:"column:note"`
	DisplayName                string         `gorm:"column:display_name"`
	URI                        string         `gorm:"column:uri"`
	URL                        sql.NullString `gorm:"column:url"`
	AvatarFileName             sql.NullString `gorm:"column:avatar_file_name"`
	AvatarContentType          sql.NullString `gorm:"column:avatar_content_type"`
	AvatarFileSize             sql.NullInt64  `gorm:"column:avatar_file_size"`
	AvatarUpdatedAt            sql.NullTime   `gorm:"column:avatar_updated_at"`
	AvatarRemoteURL            sql.NullString `gorm:"column:avatar_remote_url"`
	AvatarStorageSchemaVersion sql.NullInt64  `gorm:"column:avatar_storage_schema_version"`
	HeaderFileName             sql.NullString `gorm:"column:header_file_name"`
	HeaderContentType          sql.NullString `gorm:"column:header_content_type"`
	HeaderFileSize             sql.NullInt64  `gorm:"column:header_file_size"`
	HeaderUpdatedAt            sql.NullTime   `gorm:"column:header_updated_at"`
	HeaderRemoteURL            string         `gorm:"column:header_remote_url"`
	HeaderStorageSchemaVersion sql.NullInt64  `gorm:"column:header_storage_schema_version"`
	LastWebfingeredAt          sql.NullTime   `gorm:"column:last_webfingered_at"`
	InboxURL                   string         `gorm:"column:inbox_url"`
	OutboxURL                  string         `gorm:"column:outbox_url"`
	SharedInboxURL             string         `gorm:"column:shared_inbox_url"`
	FollowersURL               string         `gorm:"column:followers_url"`
	Protocol                   int            `gorm:"column:protocol"`
	Locked                     bool           `gorm:"column:locked"`
	Memorial                   bool           `gorm:"column:memorial"`
	MovedToAccountID           sql.NullInt64  `gorm:"column:moved_to_account_id"`
	FeaturedCollectionURL      sql.NullString `gorm:"column:featured_collection_url"`
	Fields                     []byte         `gorm:"column:fields"`
	AlsoKnownAs                StringArray    `gorm:"column:also_known_as"`
	AttributionDomains         StringArray    `gorm:"column:attribution_domains"`
	ActorType                  sql.NullString `gorm:"column:actor_type"`
	Discoverable               sql.NullBool   `gorm:"column:discoverable"`
	HideCollections            sql.NullBool   `gorm:"column:hide_collections"`
	Indexable                  bool           `gorm:"column:indexable"`
	SilencedAt                 sql.NullTime   `gorm:"column:silenced_at"`
	SuspendedAt                sql.NullTime   `gorm:"column:suspended_at"`
	SensitizedAt               sql.NullTime   `gorm:"column:sensitized_at"`
	SuspensionOrigin           sql.NullInt64  `gorm:"column:suspension_origin"`
	Trendable                  sql.NullBool   `gorm:"column:trendable"`
	ReviewedAt                 sql.NullTime   `gorm:"column:reviewed_at"`
	RequestedReviewAt          sql.NullTime   `gorm:"column:requested_review_at"`
	AccountStat                AccountStat    `gorm:"foreignKey:AccountID;->"`
	User                       User           `gorm:"foreignKey:AccountID"`
	MovedToAccount             *Account       `gorm:"foreignKey:MovedToAccountID;references:ID"`
	CustomEmojis               []CustomEmoji  `gorm:"-"`
	Tags                       []Tag          `gorm:"-"`
}

func (Account) TableName() string { return "accounts" }

func (a Account) Local() bool { return !a.Domain.Valid || a.Domain.String == "" }

func (a Account) AvatarUsesCachePrefix() bool {
	return !a.Local() && a.AvatarStorageSchemaVersion.Valid && a.AvatarStorageSchemaVersion.Int64 >= 1
}

func (a Account) HeaderUsesCachePrefix() bool {
	return !a.Local() && a.HeaderStorageSchemaVersion.Valid && a.HeaderStorageSchemaVersion.Int64 >= 1
}

func (a Account) Acct() string {
	if a.Local() {
		return a.Username
	}
	return a.Username + "@" + a.Domain.String
}

type AccountTag struct {
	TagID     int64   `gorm:"primaryKey;column:tag_id"`
	AccountID int64   `gorm:"primaryKey;column:account_id"`
	Account   Account `gorm:"foreignKey:AccountID"`
	Tag       Tag     `gorm:"foreignKey:TagID"`
}

func (AccountTag) TableName() string { return "accounts_tags" }

type AccountModerationNote struct {
	ID              int64     `gorm:"primaryKey;column:id"`
	Content         string    `gorm:"column:content"`
	AccountID       int64     `gorm:"column:account_id"`
	TargetAccountID int64     `gorm:"column:target_account_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
	Account         Account   `gorm:"foreignKey:AccountID"`
	TargetAccount   Account   `gorm:"foreignKey:TargetAccountID"`
}

func (AccountModerationNote) TableName() string { return "account_moderation_notes" }

type AccountWarning struct {
	ID              int64         `gorm:"primaryKey;column:id"`
	AccountID       sql.NullInt64 `gorm:"column:account_id"`
	TargetAccountID sql.NullInt64 `gorm:"column:target_account_id"`
	Action          int           `gorm:"column:action"`
	Text            string        `gorm:"column:text"`
	CreatedAt       time.Time     `gorm:"column:created_at"`
	UpdatedAt       time.Time     `gorm:"column:updated_at"`
	ReportID        sql.NullInt64 `gorm:"column:report_id"`
	StatusIDs       StringArray   `gorm:"column:status_ids;type:text[]"`
	OverruledAt     sql.NullTime  `gorm:"column:overruled_at"`
	Account         Account       `gorm:"foreignKey:AccountID"`
	TargetAccount   Account       `gorm:"foreignKey:TargetAccountID"`
	Report          Report        `gorm:"foreignKey:ReportID"`
}

func (AccountWarning) TableName() string { return "account_warnings" }

func AccountWarningAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func AccountWarningTargetAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type Appeal struct {
	ID                  int64          `gorm:"primaryKey;column:id"`
	AccountID           int64          `gorm:"column:account_id"`
	AccountWarningID    int64          `gorm:"column:account_warning_id"`
	Text                string         `gorm:"column:text"`
	ApprovedAt          sql.NullTime   `gorm:"column:approved_at"`
	ApprovedByAccountID sql.NullInt64  `gorm:"column:approved_by_account_id"`
	RejectedAt          sql.NullTime   `gorm:"column:rejected_at"`
	RejectedByAccountID sql.NullInt64  `gorm:"column:rejected_by_account_id"`
	CreatedAt           time.Time      `gorm:"column:created_at"`
	UpdatedAt           time.Time      `gorm:"column:updated_at"`
	Account             Account        `gorm:"foreignKey:AccountID"`
	Strike              AccountWarning `gorm:"foreignKey:AccountWarningID"`
}

func (Appeal) TableName() string { return "appeals" }

type AccountWarningPreset struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	Text      string    `gorm:"column:text"`
	Title     string    `gorm:"column:title"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (AccountWarningPreset) TableName() string { return "account_warning_presets" }

type AccountAlias struct {
	ID        int64         `gorm:"primaryKey;column:id"`
	AccountID sql.NullInt64 `gorm:"column:account_id"`
	Acct      string        `gorm:"column:acct"`
	URI       string        `gorm:"column:uri"`
	CreatedAt time.Time     `gorm:"column:created_at"`
	UpdatedAt time.Time     `gorm:"column:updated_at"`
	Account   Account       `gorm:"foreignKey:AccountID"`
}

func (AccountAlias) TableName() string { return "account_aliases" }

func AccountAliasAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type AccountMigration struct {
	ID              int64         `gorm:"primaryKey;column:id"`
	AccountID       sql.NullInt64 `gorm:"column:account_id"`
	Acct            string        `gorm:"column:acct"`
	FollowersCount  int64         `gorm:"column:followers_count"`
	TargetAccountID sql.NullInt64 `gorm:"column:target_account_id"`
	CreatedAt       time.Time     `gorm:"column:created_at"`
	UpdatedAt       time.Time     `gorm:"column:updated_at"`
	Account         Account       `gorm:"foreignKey:AccountID"`
	TargetAccount   Account       `gorm:"foreignKey:TargetAccountID"`
}

func (AccountMigration) TableName() string { return "account_migrations" }

func AccountMigrationAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type AccountStat struct {
	ID             int64        `gorm:"primaryKey;column:id"`
	AccountID      int64        `gorm:"column:account_id"`
	StatusesCount  int64        `gorm:"column:statuses_count"`
	FollowingCount int64        `gorm:"column:following_count"`
	FollowersCount int64        `gorm:"column:followers_count"`
	CreatedAt      time.Time    `gorm:"column:created_at"`
	UpdatedAt      time.Time    `gorm:"column:updated_at"`
	LastStatusAt   sql.NullTime `gorm:"column:last_status_at"`
}

func (AccountStat) TableName() string { return "account_stats" }

type AccountDomainBlock struct {
	ID        int64          `gorm:"primaryKey;column:id"`
	Domain    NullSafeString `gorm:"column:domain"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	AccountID sql.NullInt64  `gorm:"column:account_id"`
}

func (AccountDomainBlock) TableName() string { return "account_domain_blocks" }

func AccountDomainBlockAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type AccountDeletionRequest struct {
	ID        int64         `gorm:"primaryKey;column:id"`
	AccountID sql.NullInt64 `gorm:"column:account_id"`
	CreatedAt time.Time     `gorm:"column:created_at"`
	UpdatedAt time.Time     `gorm:"column:updated_at"`
	Account   Account       `gorm:"foreignKey:AccountID"`
}

func (AccountDeletionRequest) TableName() string { return "account_deletion_requests" }

func AccountDeletionRequestAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type Announcement struct {
	ID              int64         `gorm:"primaryKey;column:id"`
	Text            string        `gorm:"column:text"`
	Published       bool          `gorm:"column:published"`
	AllDay          bool          `gorm:"column:all_day"`
	ScheduledAt     sql.NullTime  `gorm:"column:scheduled_at"`
	StartsAt        sql.NullTime  `gorm:"column:starts_at"`
	EndsAt          sql.NullTime  `gorm:"column:ends_at"`
	CreatedAt       time.Time     `gorm:"column:created_at"`
	UpdatedAt       time.Time     `gorm:"column:updated_at"`
	PublishedAt     sql.NullTime  `gorm:"column:published_at"`
	StatusIDs       Int64Array    `gorm:"column:status_ids"`
	MentionAccounts []Account     `gorm:"-"`
	CustomEmojis    []CustomEmoji `gorm:"-"`
}

func (Announcement) TableName() string { return "announcements" }

type AnnouncementMute struct {
	ID             int64         `gorm:"primaryKey;column:id"`
	AccountID      sql.NullInt64 `gorm:"column:account_id"`
	AnnouncementID sql.NullInt64 `gorm:"column:announcement_id"`
	CreatedAt      time.Time     `gorm:"column:created_at"`
	UpdatedAt      time.Time     `gorm:"column:updated_at"`
}

func (AnnouncementMute) TableName() string { return "announcement_mutes" }

func AnnouncementMuteAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func AnnouncementMuteAnnouncementID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type AnnouncementReaction struct {
	ID             int64         `gorm:"primaryKey;column:id"`
	AccountID      sql.NullInt64 `gorm:"column:account_id"`
	AnnouncementID sql.NullInt64 `gorm:"column:announcement_id"`
	Name           string        `gorm:"column:name"`
	CustomEmojiID  sql.NullInt64 `gorm:"column:custom_emoji_id"`
	CreatedAt      time.Time     `gorm:"column:created_at"`
	UpdatedAt      time.Time     `gorm:"column:updated_at"`
}

func (AnnouncementReaction) TableName() string { return "announcement_reactions" }

func AnnouncementReactionAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func AnnouncementReactionAnnouncementID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type CustomEmojiCategory struct {
	ID        int64          `gorm:"primaryKey;column:id"`
	Name      sql.NullString `gorm:"column:name"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
}

func (CustomEmojiCategory) TableName() string { return "custom_emoji_categories" }

func CustomEmojiCategoryName(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

type CustomEmoji struct {
	ID                        int64               `gorm:"primaryKey;column:id"`
	Shortcode                 string              `gorm:"column:shortcode"`
	Domain                    sql.NullString      `gorm:"column:domain"`
	ImageFileName             sql.NullString      `gorm:"column:image_file_name"`
	ImageContentType          sql.NullString      `gorm:"column:image_content_type"`
	ImageFileSize             sql.NullInt64       `gorm:"column:image_file_size"`
	ImageUpdatedAt            sql.NullTime        `gorm:"column:image_updated_at"`
	CreatedAt                 time.Time           `gorm:"column:created_at"`
	UpdatedAt                 time.Time           `gorm:"column:updated_at"`
	Disabled                  bool                `gorm:"column:disabled"`
	URI                       sql.NullString      `gorm:"column:uri"`
	ImageRemoteURL            sql.NullString      `gorm:"column:image_remote_url"`
	VisibleInPicker           bool                `gorm:"column:visible_in_picker"`
	CategoryID                sql.NullInt64       `gorm:"column:category_id"`
	ImageStorageSchemaVersion sql.NullInt64       `gorm:"column:image_storage_schema_version"`
	Category                  CustomEmojiCategory `gorm:"foreignKey:CategoryID"`
}

func (CustomEmoji) TableName() string { return "custom_emojis" }

func (e CustomEmoji) Local() bool { return !e.Domain.Valid || e.Domain.String == "" }

type DomainBlock struct {
	ID             int64          `gorm:"primaryKey;column:id"`
	Domain         string         `gorm:"column:domain"`
	CreatedAt      time.Time      `gorm:"column:created_at"`
	UpdatedAt      time.Time      `gorm:"column:updated_at"`
	Severity       sql.NullInt64  `gorm:"column:severity"`
	RejectMedia    bool           `gorm:"column:reject_media"`
	RejectReports  bool           `gorm:"column:reject_reports"`
	PrivateComment sql.NullString `gorm:"column:private_comment"`
	PublicComment  sql.NullString `gorm:"column:public_comment"`
	Obfuscate      bool           `gorm:"column:obfuscate"`
}

func (DomainBlock) TableName() string { return "domain_blocks" }

func DomainBlockSeverity(value int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(value), Valid: true}
}

func (b DomainBlock) SeverityInt() (int, bool) {
	if !b.Severity.Valid {
		return 0, false
	}
	return int(b.Severity.Int64), true
}

type DomainAllow struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	Domain    string    `gorm:"column:domain"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (DomainAllow) TableName() string { return "domain_allows" }

type UnavailableDomain struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	Domain    string    `gorm:"column:domain"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (UnavailableDomain) TableName() string { return "unavailable_domains" }

type EmailDomainBlock struct {
	ID                int64         `gorm:"primaryKey;column:id"`
	Domain            string        `gorm:"column:domain"`
	CreatedAt         time.Time     `gorm:"column:created_at"`
	UpdatedAt         time.Time     `gorm:"column:updated_at"`
	ParentID          sql.NullInt64 `gorm:"column:parent_id"`
	AllowWithApproval bool          `gorm:"column:allow_with_approval"`
}

func (EmailDomainBlock) TableName() string { return "email_domain_blocks" }

type CanonicalEmailBlock struct {
	ID                 int64         `gorm:"primaryKey;column:id"`
	CanonicalEmailHash string        `gorm:"column:canonical_email_hash"`
	ReferenceAccountID sql.NullInt64 `gorm:"column:reference_account_id"`
	CreatedAt          time.Time     `gorm:"column:created_at"`
	UpdatedAt          time.Time     `gorm:"column:updated_at"`
}

func (CanonicalEmailBlock) TableName() string { return "canonical_email_blocks" }

type IPBlock struct {
	ID        int64        `gorm:"primaryKey;column:id"`
	CreatedAt time.Time    `gorm:"column:created_at"`
	UpdatedAt time.Time    `gorm:"column:updated_at"`
	ExpiresAt sql.NullTime `gorm:"column:expires_at"`
	IP        string       `gorm:"column:ip"`
	Severity  int          `gorm:"column:severity"`
	Comment   string       `gorm:"column:comment"`
}

func (IPBlock) TableName() string { return "ip_blocks" }

type Identity struct {
	ID        int64         `gorm:"primaryKey;column:id"`
	Provider  string        `gorm:"column:provider"`
	UID       string        `gorm:"column:uid"`
	CreatedAt time.Time     `gorm:"column:created_at"`
	UpdatedAt time.Time     `gorm:"column:updated_at"`
	UserID    sql.NullInt64 `gorm:"column:user_id"`
	User      User          `gorm:"foreignKey:UserID"`
}

func (Identity) TableName() string { return "identities" }

type Import struct {
	ID              int64          `gorm:"primaryKey;column:id"`
	Type            int            `gorm:"column:type"`
	Approved        bool           `gorm:"column:approved"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
	DataFileName    sql.NullString `gorm:"column:data_file_name"`
	DataContentType sql.NullString `gorm:"column:data_content_type"`
	DataFileSize    sql.NullInt64  `gorm:"column:data_file_size"`
	DataUpdatedAt   sql.NullTime   `gorm:"column:data_updated_at"`
	AccountID       int64          `gorm:"column:account_id"`
	Overwrite       bool           `gorm:"column:overwrite"`
	Account         Account        `gorm:"foreignKey:AccountID"`
}

func (Import) TableName() string { return "imports" }

type Instance struct {
	Domain        string `gorm:"primaryKey;column:domain"`
	AccountsCount int64  `gorm:"column:accounts_count"`
}

func (Instance) TableName() string { return "instances" }

type Setting struct {
	ID        int64          `gorm:"primaryKey;column:id"`
	Var       string         `gorm:"column:var"`
	Value     sql.NullString `gorm:"column:value"`
	ThingType sql.NullString `gorm:"column:thing_type"`
	CreatedAt sql.NullTime   `gorm:"column:created_at"`
	UpdatedAt sql.NullTime   `gorm:"column:updated_at"`
	ThingID   sql.NullInt64  `gorm:"column:thing_id"`
}

func (Setting) TableName() string { return "settings" }

type SiteUpload struct {
	ID              int64          `gorm:"primaryKey;column:id"`
	Var             string         `gorm:"column:var"`
	FileFileName    sql.NullString `gorm:"column:file_file_name"`
	FileContentType sql.NullString `gorm:"column:file_content_type"`
	FileFileSize    sql.NullInt64  `gorm:"column:file_file_size"`
	FileUpdatedAt   sql.NullTime   `gorm:"column:file_updated_at"`
	Meta            JSONValue      `gorm:"column:meta"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
	Blurhash        sql.NullString `gorm:"column:blurhash"`
}

func (SiteUpload) TableName() string { return "site_uploads" }

type Rule struct {
	ID        int64        `gorm:"primaryKey;column:id"`
	Priority  int          `gorm:"column:priority"`
	DeletedAt sql.NullTime `gorm:"column:deleted_at"`
	Text      string       `gorm:"column:text"`
	Hint      string       `gorm:"column:hint"`
	CreatedAt time.Time    `gorm:"column:created_at"`
	UpdatedAt time.Time    `gorm:"column:updated_at"`
}

func (Rule) TableName() string { return "rules" }

type Relay struct {
	ID               int64          `gorm:"primaryKey;column:id"`
	InboxURL         string         `gorm:"column:inbox_url"`
	FollowActivityID sql.NullString `gorm:"column:follow_activity_id"`
	CreatedAt        time.Time      `gorm:"column:created_at"`
	UpdatedAt        time.Time      `gorm:"column:updated_at"`
	State            int            `gorm:"column:state"`
}

func (Relay) TableName() string { return "relays" }

type Webhook struct {
	ID        int64          `gorm:"primaryKey;column:id"`
	URL       string         `gorm:"column:url"`
	Events    StringArray    `gorm:"column:events"`
	Secret    string         `gorm:"column:secret"`
	Enabled   bool           `gorm:"column:enabled"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	Template  sql.NullString `gorm:"column:template"`
}

func (Webhook) TableName() string { return "webhooks" }

type AdminActionLog struct {
	ID              int64          `gorm:"primaryKey;column:id"`
	AccountID       sql.NullInt64  `gorm:"column:account_id"`
	Action          string         `gorm:"column:action"`
	TargetType      sql.NullString `gorm:"column:target_type"`
	TargetID        sql.NullInt64  `gorm:"column:target_id"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
	HumanIdentifier sql.NullString `gorm:"column:human_identifier"`
	RouteParam      sql.NullString `gorm:"column:route_param"`
	Permalink       sql.NullString `gorm:"column:permalink"`
	Account         Account        `gorm:"foreignKey:AccountID"`
}

func (AdminActionLog) TableName() string { return "admin_action_logs" }

type AccountConversation struct {
	ID                    int64         `gorm:"primaryKey;column:id"`
	AccountID             sql.NullInt64 `gorm:"column:account_id"`
	ConversationID        sql.NullInt64 `gorm:"column:conversation_id"`
	ParticipantAccountIDs Int64Array    `gorm:"column:participant_account_ids"`
	StatusIDs             Int64Array    `gorm:"column:status_ids"`
	LastStatusID          sql.NullInt64 `gorm:"column:last_status_id"`
	LockVersion           int           `gorm:"column:lock_version"`
	Unread                bool          `gorm:"column:unread"`
	LastStatus            *Status       `gorm:"foreignKey:LastStatusID"`
	ParticipantAccounts   []Account     `gorm:"-"`
}

func (AccountConversation) TableName() string { return "account_conversations" }

func AccountConversationAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func AccountConversationConversationID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type Conversation struct {
	ID        int64          `gorm:"primaryKey;column:id"`
	URI       sql.NullString `gorm:"column:uri"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
}

func (Conversation) TableName() string { return "conversations" }

type ConversationMute struct {
	ID             int64 `gorm:"primaryKey;column:id"`
	ConversationID int64 `gorm:"column:conversation_id"`
	AccountID      int64 `gorm:"column:account_id"`
}

func (ConversationMute) TableName() string { return "conversation_mutes" }

type AccountNote struct {
	ID              int64         `gorm:"primaryKey;column:id"`
	AccountID       sql.NullInt64 `gorm:"column:account_id"`
	TargetAccountID sql.NullInt64 `gorm:"column:target_account_id"`
	Comment         string        `gorm:"column:comment"`
	CreatedAt       time.Time     `gorm:"column:created_at"`
	UpdatedAt       time.Time     `gorm:"column:updated_at"`
}

func (AccountNote) TableName() string { return "account_notes" }

func AccountNoteAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type AccountPin struct {
	ID              int64         `gorm:"primaryKey;column:id"`
	AccountID       sql.NullInt64 `gorm:"column:account_id"`
	TargetAccountID sql.NullInt64 `gorm:"column:target_account_id"`
	CreatedAt       time.Time     `gorm:"column:created_at"`
	UpdatedAt       time.Time     `gorm:"column:updated_at"`
}

func (AccountPin) TableName() string { return "account_pins" }

func AccountPinAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func AccountPinTargetAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type CustomFilter struct {
	ID        int64                 `gorm:"primaryKey;column:id"`
	AccountID sql.NullInt64         `gorm:"column:account_id"`
	ExpiresAt sql.NullTime          `gorm:"column:expires_at"`
	Phrase    string                `gorm:"column:phrase"`
	Context   StringArray           `gorm:"column:context"`
	CreatedAt time.Time             `gorm:"column:created_at"`
	UpdatedAt time.Time             `gorm:"column:updated_at"`
	Action    int                   `gorm:"column:action"`
	Keywords  []CustomFilterKeyword `gorm:"foreignKey:CustomFilterID"`
	Statuses  []CustomFilterStatus  `gorm:"foreignKey:CustomFilterID"`
}

func (CustomFilter) TableName() string { return "custom_filters" }

func CustomFilterAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type CustomFilterKeyword struct {
	ID             int64        `gorm:"primaryKey;column:id"`
	CustomFilterID int64        `gorm:"column:custom_filter_id"`
	Keyword        string       `gorm:"column:keyword"`
	WholeWord      bool         `gorm:"column:whole_word"`
	CreatedAt      time.Time    `gorm:"column:created_at"`
	UpdatedAt      time.Time    `gorm:"column:updated_at"`
	CustomFilter   CustomFilter `gorm:"foreignKey:CustomFilterID"`
}

func (CustomFilterKeyword) TableName() string { return "custom_filter_keywords" }

type CustomFilterStatus struct {
	ID             int64        `gorm:"primaryKey;column:id"`
	CustomFilterID int64        `gorm:"column:custom_filter_id"`
	StatusID       int64        `gorm:"column:status_id"`
	CreatedAt      time.Time    `gorm:"column:created_at"`
	UpdatedAt      time.Time    `gorm:"column:updated_at"`
	CustomFilter   CustomFilter `gorm:"foreignKey:CustomFilterID"`
	Status         *Status      `gorm:"foreignKey:StatusID"`
}

func (CustomFilterStatus) TableName() string { return "custom_filter_statuses" }

type List struct {
	ID            int64     `gorm:"primaryKey;column:id"`
	AccountID     int64     `gorm:"column:account_id"`
	Title         string    `gorm:"column:title"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
	RepliesPolicy int       `gorm:"column:replies_policy"`
	Exclusive     bool      `gorm:"column:exclusive"`
}

func (List) TableName() string { return "lists" }

type ListAccount struct {
	ID              int64         `gorm:"primaryKey;column:id"`
	ListID          int64         `gorm:"column:list_id"`
	AccountID       int64         `gorm:"column:account_id"`
	FollowID        sql.NullInt64 `gorm:"column:follow_id"`
	FollowRequestID sql.NullInt64 `gorm:"column:follow_request_id"`
	Account         Account       `gorm:"foreignKey:AccountID"`
}

func (ListAccount) TableName() string { return "list_accounts" }

type User struct {
	ID                     int64          `gorm:"primaryKey;column:id"`
	AccountID              int64          `gorm:"column:account_id"`
	Email                  string         `gorm:"column:email"`
	EncryptedPassword      string         `gorm:"column:encrypted_password"`
	Locale                 sql.NullString `gorm:"column:locale"`
	CreatedAt              time.Time      `gorm:"column:created_at"`
	UpdatedAt              time.Time      `gorm:"column:updated_at"`
	ResetPasswordToken     sql.NullString `gorm:"column:reset_password_token"`
	ResetPasswordSentAt    sql.NullTime   `gorm:"column:reset_password_sent_at"`
	Disabled               bool           `gorm:"column:disabled"`
	Approved               bool           `gorm:"column:approved"`
	ConfirmationToken      sql.NullString `gorm:"column:confirmation_token"`
	ConfirmedAt            sql.NullTime   `gorm:"column:confirmed_at"`
	ConfirmationSentAt     sql.NullTime   `gorm:"column:confirmation_sent_at"`
	UnconfirmedEmail       sql.NullString `gorm:"column:unconfirmed_email"`
	LastEmailedAt          sql.NullTime   `gorm:"column:last_emailed_at"`
	EncryptedOTPSecret     sql.NullString `gorm:"column:encrypted_otp_secret"`
	EncryptedOTPSecretIV   sql.NullString `gorm:"column:encrypted_otp_secret_iv"`
	EncryptedOTPSecretSalt sql.NullString `gorm:"column:encrypted_otp_secret_salt"`
	OTPSecret              sql.NullString `gorm:"column:otp_secret"`
	ConsumedTimestep       sql.NullInt64  `gorm:"column:consumed_timestep"`
	OTPRequiredForLogin    bool           `gorm:"column:otp_required_for_login"`
	OTPBackupCodes         StringArray    `gorm:"column:otp_backup_codes;type:text[]"`
	WebauthnID             sql.NullString `gorm:"column:webauthn_id"`
	SignInCount            int            `gorm:"column:sign_in_count"`
	CurrentSignInAt        sql.NullTime   `gorm:"column:current_sign_in_at"`
	LastSignInAt           sql.NullTime   `gorm:"column:last_sign_in_at"`
	SignInToken            sql.NullString `gorm:"column:sign_in_token"`
	SignInTokenSentAt      sql.NullTime   `gorm:"column:sign_in_token_sent_at"`
	SkipSignInToken        sql.NullBool   `gorm:"column:skip_sign_in_token"`
	Settings               sql.NullString `gorm:"column:settings"`
	ChosenLanguages        StringArray    `gorm:"column:chosen_languages"`
	InviteID               sql.NullInt64  `gorm:"column:invite_id"`
	CreatedByApplicationID sql.NullInt64  `gorm:"column:created_by_application_id"`
	RoleID                 sql.NullInt64  `gorm:"column:role_id"`
	SignUpIP               sql.NullString `gorm:"column:sign_up_ip"`
	TimeZone               sql.NullString `gorm:"column:time_zone"`
	Account                *Account       `gorm:"foreignKey:AccountID"`
	Role                   UserRole       `gorm:"foreignKey:RoleID"`
}

func (User) TableName() string { return "users" }

type WebauthnCredential struct {
	ID         int64         `gorm:"primaryKey;column:id"`
	ExternalID string        `gorm:"column:external_id"`
	PublicKey  string        `gorm:"column:public_key"`
	Nickname   string        `gorm:"column:nickname"`
	SignCount  int64         `gorm:"column:sign_count"`
	UserID     sql.NullInt64 `gorm:"column:user_id"`
	CreatedAt  time.Time     `gorm:"column:created_at"`
	UpdatedAt  time.Time     `gorm:"column:updated_at"`
}

func (WebauthnCredential) TableName() string { return "webauthn_credentials" }

func WebauthnCredentialUserID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type SessionActivation struct {
	ID                    int64          `gorm:"primaryKey;column:id"`
	SessionID             string         `gorm:"column:session_id"`
	CreatedAt             time.Time      `gorm:"column:created_at"`
	UpdatedAt             time.Time      `gorm:"column:updated_at"`
	UserAgent             string         `gorm:"column:user_agent"`
	IP                    sql.NullString `gorm:"column:ip"`
	AccessTokenID         sql.NullInt64  `gorm:"column:access_token_id"`
	UserID                int64          `gorm:"column:user_id"`
	WebPushSubscriptionID sql.NullInt64  `gorm:"column:web_push_subscription_id"`
}

func (SessionActivation) TableName() string { return "session_activations" }

type UserInviteRequest struct {
	ID        int64          `gorm:"primaryKey;column:id"`
	UserID    sql.NullInt64  `gorm:"column:user_id"`
	Text      sql.NullString `gorm:"column:text"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
}

func (UserInviteRequest) TableName() string { return "user_invite_requests" }

func UserInviteRequestUserID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type UserRole struct {
	ID          int64     `gorm:"primaryKey;column:id"`
	Name        string    `gorm:"column:name"`
	Color       string    `gorm:"column:color"`
	Position    int       `gorm:"column:position"`
	Permissions int64     `gorm:"column:permissions"`
	Highlighted bool      `gorm:"column:highlighted"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (UserRole) TableName() string { return "user_roles" }

type SoftwareUpdate struct {
	ID           int64     `gorm:"primaryKey;column:id"`
	Version      string    `gorm:"column:version"`
	Urgent       bool      `gorm:"column:urgent"`
	Type         int       `gorm:"column:type"`
	ReleaseNotes string    `gorm:"column:release_notes"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (SoftwareUpdate) TableName() string { return "software_updates" }

type Invite struct {
	ID         int64          `gorm:"primaryKey;column:id"`
	UserID     int64          `gorm:"column:user_id"`
	Code       string         `gorm:"column:code"`
	ExpiresAt  sql.NullTime   `gorm:"column:expires_at"`
	MaxUses    sql.NullInt64  `gorm:"column:max_uses"`
	Uses       int64          `gorm:"column:uses"`
	CreatedAt  time.Time      `gorm:"column:created_at"`
	UpdatedAt  time.Time      `gorm:"column:updated_at"`
	Autofollow bool           `gorm:"column:autofollow"`
	Comment    sql.NullString `gorm:"column:comment"`
	User       User           `gorm:"foreignKey:UserID"`
}

func (Invite) TableName() string { return "invites" }

type Backup struct {
	ID              int64          `gorm:"primaryKey;column:id"`
	UserID          sql.NullInt64  `gorm:"column:user_id"`
	DumpFileName    sql.NullString `gorm:"column:dump_file_name"`
	DumpContentType sql.NullString `gorm:"column:dump_content_type"`
	DumpUpdatedAt   sql.NullTime   `gorm:"column:dump_updated_at"`
	Processed       bool           `gorm:"column:processed"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
	DumpFileSize    sql.NullInt64  `gorm:"column:dump_file_size"`
	User            User           `gorm:"foreignKey:UserID"`
}

func (Backup) TableName() string { return "backups" }

type BulkImport struct {
	ID               int64           `gorm:"primaryKey;column:id"`
	Type             int             `gorm:"column:type"`
	State            int             `gorm:"column:state"`
	TotalItems       int             `gorm:"column:total_items"`
	ImportedItems    int             `gorm:"column:imported_items"`
	ProcessedItems   int             `gorm:"column:processed_items"`
	FinishedAt       sql.NullTime    `gorm:"column:finished_at"`
	Overwrite        bool            `gorm:"column:overwrite"`
	LikelyMismatched bool            `gorm:"column:likely_mismatched"`
	OriginalFilename string          `gorm:"column:original_filename"`
	AccountID        int64           `gorm:"column:account_id"`
	CreatedAt        time.Time       `gorm:"column:created_at"`
	UpdatedAt        time.Time       `gorm:"column:updated_at"`
	Rows             []BulkImportRow `gorm:"foreignKey:BulkImportID"`
}

func (BulkImport) TableName() string { return "bulk_imports" }

type BulkImportRow struct {
	ID           int64     `gorm:"primaryKey;column:id"`
	BulkImportID int64     `gorm:"column:bulk_import_id"`
	Data         JSONValue `gorm:"column:data"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (BulkImportRow) TableName() string { return "bulk_import_rows" }

type LoginActivity struct {
	ID                   int64          `gorm:"primaryKey;column:id"`
	UserID               int64          `gorm:"column:user_id"`
	AuthenticationMethod sql.NullString `gorm:"column:authentication_method"`
	Provider             sql.NullString `gorm:"column:provider"`
	Success              sql.NullBool   `gorm:"column:success"`
	FailureReason        sql.NullString `gorm:"column:failure_reason"`
	IP                   sql.NullString `gorm:"column:ip"`
	UserAgent            sql.NullString `gorm:"column:user_agent"`
	CreatedAt            sql.NullTime   `gorm:"column:created_at"`
	User                 User           `gorm:"foreignKey:UserID"`
}

func (LoginActivity) TableName() string { return "login_activities" }

type Marker struct {
	ID          int64         `gorm:"primaryKey;column:id"`
	UserID      sql.NullInt64 `gorm:"column:user_id"`
	Timeline    string        `gorm:"column:timeline"`
	LastReadID  int64         `gorm:"column:last_read_id"`
	LockVersion int           `gorm:"column:lock_version"`
	CreatedAt   time.Time     `gorm:"column:created_at"`
	UpdatedAt   time.Time     `gorm:"column:updated_at"`
	User        User          `gorm:"foreignKey:UserID"`
}

func (Marker) TableName() string { return "markers" }

func MarkerUserID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type WebSetting struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	Data      JSONValue `gorm:"column:data"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	UserID    int64     `gorm:"column:user_id"`
	User      User      `gorm:"foreignKey:UserID"`
}

func (WebSetting) TableName() string { return "web_settings" }

type WebPushSubscription struct {
	ID            int64         `gorm:"primaryKey;column:id"`
	Endpoint      string        `gorm:"column:endpoint"`
	KeyP256dh     string        `gorm:"column:key_p256dh"`
	KeyAuth       string        `gorm:"column:key_auth"`
	Data          JSONValue     `gorm:"column:data"`
	CreatedAt     time.Time     `gorm:"column:created_at"`
	UpdatedAt     time.Time     `gorm:"column:updated_at"`
	AccessTokenID sql.NullInt64 `gorm:"column:access_token_id"`
	UserID        sql.NullInt64 `gorm:"column:user_id"`
}

func (WebPushSubscription) TableName() string { return "web_push_subscriptions" }

type OAuthAccessGrant struct {
	ID                  int64          `gorm:"primaryKey;column:id"`
	Token               string         `gorm:"column:token"`
	ExpiresIn           int64          `gorm:"column:expires_in"`
	RedirectURI         string         `gorm:"column:redirect_uri"`
	CreatedAt           time.Time      `gorm:"column:created_at"`
	RevokedAt           sql.NullTime   `gorm:"column:revoked_at"`
	Scopes              NullSafeString `gorm:"column:scopes"`
	ApplicationID       int64          `gorm:"column:application_id"`
	ResourceOwnerID     int64          `gorm:"column:resource_owner_id"`
	CodeChallenge       sql.NullString `gorm:"column:code_challenge"`
	CodeChallengeMethod sql.NullString `gorm:"column:code_challenge_method"`
}

func (OAuthAccessGrant) TableName() string { return "oauth_access_grants" }

type OAuthAccessToken struct {
	ID              int64          `gorm:"primaryKey;column:id"`
	Token           string         `gorm:"column:token"`
	RefreshToken    sql.NullString `gorm:"column:refresh_token"`
	ExpiresIn       sql.NullInt64  `gorm:"column:expires_in"`
	RevokedAt       sql.NullTime   `gorm:"column:revoked_at"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	Scopes          NullSafeString `gorm:"column:scopes"`
	ApplicationID   sql.NullInt64  `gorm:"column:application_id"`
	ResourceOwnerID sql.NullInt64  `gorm:"column:resource_owner_id"`
	LastUsedAt      sql.NullTime   `gorm:"column:last_used_at"`
	LastUsedIP      sql.NullString `gorm:"column:last_used_ip"`
}

func (OAuthAccessToken) TableName() string { return "oauth_access_tokens" }

type OAuthApplication struct {
	ID           int64          `gorm:"primaryKey;column:id"`
	Name         string         `gorm:"column:name"`
	UID          string         `gorm:"column:uid"`
	Secret       string         `gorm:"column:secret"`
	RedirectURI  string         `gorm:"column:redirect_uri"`
	Scopes       string         `gorm:"column:scopes"`
	CreatedAt    sql.NullTime   `gorm:"column:created_at"`
	UpdatedAt    sql.NullTime   `gorm:"column:updated_at"`
	Superapp     bool           `gorm:"column:superapp"`
	Website      NullSafeString `gorm:"column:website"`
	OwnerType    sql.NullString `gorm:"column:owner_type"`
	OwnerID      sql.NullInt64  `gorm:"column:owner_id"`
	Confidential bool           `gorm:"column:confidential"`
}

func (OAuthApplication) TableName() string { return "oauth_applications" }

type PgHeroSpaceStat struct {
	ID         int64          `gorm:"primaryKey;column:id"`
	Database   sql.NullString `gorm:"column:database"`
	Schema     sql.NullString `gorm:"column:schema"`
	Relation   sql.NullString `gorm:"column:relation"`
	Size       sql.NullInt64  `gorm:"column:size"`
	CapturedAt sql.NullTime   `gorm:"column:captured_at"`
}

func (PgHeroSpaceStat) TableName() string { return "pghero_space_stats" }

type Status struct {
	ID                        int64               `gorm:"primaryKey;column:id"`
	URI                       sql.NullString      `gorm:"column:uri"`
	Text                      string              `gorm:"column:text"`
	CreatedAt                 time.Time           `gorm:"column:created_at"`
	UpdatedAt                 time.Time           `gorm:"column:updated_at"`
	InReplyToID               sql.NullInt64       `gorm:"column:in_reply_to_id"`
	ReblogOfID                sql.NullInt64       `gorm:"column:reblog_of_id"`
	URL                       sql.NullString      `gorm:"column:url"`
	Sensitive                 bool                `gorm:"column:sensitive"`
	Visibility                int                 `gorm:"column:visibility"`
	SpoilerText               string              `gorm:"column:spoiler_text"`
	Reply                     bool                `gorm:"column:reply"`
	Language                  sql.NullString      `gorm:"column:language"`
	ConversationID            sql.NullInt64       `gorm:"column:conversation_id"`
	Local                     sql.NullBool        `gorm:"column:local"`
	AccountID                 int64               `gorm:"column:account_id"`
	ApplicationID             sql.NullInt64       `gorm:"column:application_id"`
	InReplyToAccountID        sql.NullInt64       `gorm:"column:in_reply_to_account_id"`
	PollID                    sql.NullInt64       `gorm:"column:poll_id"`
	DeletedAt                 sql.NullTime        `gorm:"column:deleted_at"`
	EditedAt                  sql.NullTime        `gorm:"column:edited_at"`
	Trendable                 sql.NullBool        `gorm:"column:trendable"`
	OrderedMediaAttachmentIDs Int64Array          `gorm:"column:ordered_media_attachment_ids"`
	Account                   Account             `gorm:"foreignKey:AccountID"`
	Reblog                    *Status             `gorm:"foreignKey:ReblogOfID"`
	StatusStat                StatusStat          `gorm:"foreignKey:StatusID"`
	Application               *OAuthApplication   `gorm:"foreignKey:ApplicationID"`
	MediaAttachments          []MediaAttachment   `gorm:"foreignKey:StatusID"`
	Mentions                  []Mention           `gorm:"foreignKey:StatusID"`
	Tags                      []Tag               `gorm:"many2many:statuses_tags;joinForeignKey:StatusID;joinReferences:TagID"`
	PreviewCards              []PreviewCard       `gorm:"many2many:preview_cards_statuses;joinForeignKey:StatusID;joinReferences:PreviewCardID"`
	PreviewCardStatuses       []PreviewCardStatus `gorm:"foreignKey:StatusID"`
	Poll                      *Poll               `gorm:"foreignKey:StatusID"`
	FavouritedByCurrent       bool                `gorm:"-"`
	RebloggedByCurrent        bool                `gorm:"-"`
	MutedByCurrent            bool                `gorm:"-"`
	BookmarkedByCurrent       bool                `gorm:"-"`
	PinnedByCurrent           bool                `gorm:"-"`
	CustomEmojis              []CustomEmoji       `gorm:"-"`
	QuoteID                   sql.NullString      `gorm:"-"`
	QuoteOriginalURL          sql.NullString      `gorm:"-"`
}

func (Status) TableName() string { return "statuses" }

func (status Status) FirstPreviewCard() (PreviewCard, bool) {
	if len(status.PreviewCards) == 0 {
		return PreviewCard{}, false
	}
	card := status.PreviewCards[0]
	for _, join := range status.PreviewCardStatuses {
		if join.PreviewCardID == card.ID && join.URL.Valid && strings.TrimSpace(join.URL.String) != "" {
			card.URL = join.URL.String
			break
		}
	}
	return card, true
}

type Tombstone struct {
	ID          int64         `gorm:"primaryKey;column:id"`
	AccountID   sql.NullInt64 `gorm:"column:account_id"`
	URI         string        `gorm:"column:uri"`
	CreatedAt   time.Time     `gorm:"column:created_at"`
	UpdatedAt   time.Time     `gorm:"column:updated_at"`
	ByModerator sql.NullBool  `gorm:"column:by_moderator"`
	Account     Account       `gorm:"foreignKey:AccountID"`
}

func (Tombstone) TableName() string { return "tombstones" }

type StatusEdit struct {
	ID                        int64             `gorm:"primaryKey;column:id"`
	StatusID                  int64             `gorm:"column:status_id"`
	AccountID                 sql.NullInt64     `gorm:"column:account_id"`
	Text                      string            `gorm:"column:text"`
	SpoilerText               string            `gorm:"column:spoiler_text"`
	CreatedAt                 time.Time         `gorm:"column:created_at"`
	UpdatedAt                 time.Time         `gorm:"column:updated_at"`
	OrderedMediaAttachmentIDs Int64Array        `gorm:"column:ordered_media_attachment_ids"`
	MediaDescriptions         StringArray       `gorm:"column:media_descriptions"`
	PollOptions               StringArray       `gorm:"column:poll_options"`
	Sensitive                 sql.NullBool      `gorm:"column:sensitive"`
	Status                    Status            `gorm:"foreignKey:StatusID"`
	Account                   Account           `gorm:"foreignKey:AccountID"`
	OrderedMediaAttachments   []MediaAttachment `gorm:"-"`
	CustomEmojis              []CustomEmoji     `gorm:"-"`
}

func (StatusEdit) TableName() string { return "status_edits" }

type StatusStat struct {
	ID              int64     `gorm:"primaryKey;column:id"`
	StatusID        int64     `gorm:"column:status_id"`
	RepliesCount    int64     `gorm:"column:replies_count"`
	ReblogsCount    int64     `gorm:"column:reblogs_count"`
	FavouritesCount int64     `gorm:"column:favourites_count"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (StatusStat) TableName() string { return "status_stats" }

type StatusTrend struct {
	ID        int64          `gorm:"primaryKey;column:id"`
	StatusID  int64          `gorm:"column:status_id"`
	AccountID int64          `gorm:"column:account_id"`
	Score     float64        `gorm:"column:score"`
	Rank      int            `gorm:"column:rank"`
	Allowed   bool           `gorm:"column:allowed"`
	Language  sql.NullString `gorm:"column:language"`
	Status    Status         `gorm:"foreignKey:StatusID"`
	Account   Account        `gorm:"foreignKey:AccountID"`
}

func (StatusTrend) TableName() string { return "status_trends" }

type MediaAttachment struct {
	ID                       int64          `gorm:"primaryKey;column:id"`
	StatusID                 sql.NullInt64  `gorm:"column:status_id"`
	ScheduledStatusID        sql.NullInt64  `gorm:"column:scheduled_status_id"`
	Shortcode                sql.NullString `gorm:"column:shortcode"`
	FileFileName             sql.NullString `gorm:"column:file_file_name"`
	FileContentType          sql.NullString `gorm:"column:file_content_type"`
	FileFileSize             sql.NullInt64  `gorm:"column:file_file_size"`
	FileUpdatedAt            sql.NullTime   `gorm:"column:file_updated_at"`
	CreatedAt                time.Time      `gorm:"column:created_at"`
	UpdatedAt                time.Time      `gorm:"column:updated_at"`
	RemoteURL                string         `gorm:"column:remote_url"`
	Type                     int            `gorm:"column:type"`
	FileMeta                 []byte         `gorm:"column:file_meta"`
	AccountID                sql.NullInt64  `gorm:"column:account_id"`
	Description              sql.NullString `gorm:"column:description"`
	Blurhash                 sql.NullString `gorm:"column:blurhash"`
	Processing               sql.NullInt64  `gorm:"column:processing"`
	FileStorageSchemaVersion sql.NullInt64  `gorm:"column:file_storage_schema_version"`
	ThumbnailFileName        sql.NullString `gorm:"column:thumbnail_file_name"`
	ThumbnailContentType     sql.NullString `gorm:"column:thumbnail_content_type"`
	ThumbnailFileSize        sql.NullInt64  `gorm:"column:thumbnail_file_size"`
	ThumbnailUpdatedAt       sql.NullTime   `gorm:"column:thumbnail_updated_at"`
	ThumbnailRemoteURL       sql.NullString `gorm:"column:thumbnail_remote_url"`
	Status                   Status         `gorm:"foreignKey:StatusID"`
}

func (MediaAttachment) TableName() string { return "media_attachments" }

type PreviewCard struct {
	ID                        int64           `gorm:"primaryKey;column:id"`
	URL                       string          `gorm:"column:url"`
	Title                     string          `gorm:"column:title"`
	Description               string          `gorm:"column:description"`
	ImageFileName             sql.NullString  `gorm:"column:image_file_name"`
	ImageContentType          sql.NullString  `gorm:"column:image_content_type"`
	ImageFileSize             sql.NullInt64   `gorm:"column:image_file_size"`
	ImageUpdatedAt            sql.NullTime    `gorm:"column:image_updated_at"`
	Type                      int             `gorm:"column:type"`
	HTML                      string          `gorm:"column:html"`
	AuthorName                string          `gorm:"column:author_name"`
	AuthorURL                 string          `gorm:"column:author_url"`
	ProviderName              string          `gorm:"column:provider_name"`
	ProviderURL               string          `gorm:"column:provider_url"`
	Width                     int             `gorm:"column:width"`
	Height                    int             `gorm:"column:height"`
	CreatedAt                 time.Time       `gorm:"column:created_at"`
	UpdatedAt                 time.Time       `gorm:"column:updated_at"`
	EmbedURL                  string          `gorm:"column:embed_url"`
	ImageStorageSchemaVersion sql.NullInt64   `gorm:"column:image_storage_schema_version"`
	Blurhash                  sql.NullString  `gorm:"column:blurhash"`
	Language                  sql.NullString  `gorm:"column:language"`
	MaxScore                  sql.NullFloat64 `gorm:"column:max_score"`
	MaxScoreAt                sql.NullTime    `gorm:"column:max_score_at"`
	Trendable                 sql.NullBool    `gorm:"column:trendable"`
	LinkType                  sql.NullInt64   `gorm:"column:link_type"`
	PublishedAt               sql.NullTime    `gorm:"column:published_at"`
	ImageDescription          string          `gorm:"column:image_description"`
	AuthorAccountID           sql.NullInt64   `gorm:"column:author_account_id"`
	AuthorAccount             *Account        `gorm:"foreignKey:AuthorAccountID"`
}

func (PreviewCard) TableName() string { return "preview_cards" }

type PreviewCardStatus struct {
	StatusID      int64          `gorm:"primaryKey;column:status_id"`
	PreviewCardID int64          `gorm:"primaryKey;column:preview_card_id"`
	URL           sql.NullString `gorm:"column:url"`
	PreviewCard   PreviewCard    `gorm:"foreignKey:PreviewCardID"`
	Status        Status         `gorm:"foreignKey:StatusID"`
}

func (PreviewCardStatus) TableName() string { return "preview_cards_statuses" }

type PreviewCardProvider struct {
	ID                int64          `gorm:"primaryKey;column:id"`
	Domain            string         `gorm:"column:domain"`
	IconFileName      sql.NullString `gorm:"column:icon_file_name"`
	IconContentType   sql.NullString `gorm:"column:icon_content_type"`
	IconFileSize      sql.NullInt64  `gorm:"column:icon_file_size"`
	IconUpdatedAt     sql.NullTime   `gorm:"column:icon_updated_at"`
	Trendable         sql.NullBool   `gorm:"column:trendable"`
	ReviewedAt        sql.NullTime   `gorm:"column:reviewed_at"`
	RequestedReviewAt sql.NullTime   `gorm:"column:requested_review_at"`
	CreatedAt         time.Time      `gorm:"column:created_at"`
	UpdatedAt         time.Time      `gorm:"column:updated_at"`
}

func (PreviewCardProvider) TableName() string { return "preview_card_providers" }

type PreviewCardTrend struct {
	ID            int64          `gorm:"primaryKey;column:id"`
	PreviewCardID int64          `gorm:"column:preview_card_id"`
	Score         float64        `gorm:"column:score"`
	Rank          int            `gorm:"column:rank"`
	Allowed       bool           `gorm:"column:allowed"`
	Language      sql.NullString `gorm:"column:language"`
	PreviewCard   PreviewCard    `gorm:"foreignKey:PreviewCardID"`
}

func (PreviewCardTrend) TableName() string { return "preview_card_trends" }

type ScheduledStatus struct {
	ID               int64             `gorm:"primaryKey;column:id"`
	AccountID        sql.NullInt64     `gorm:"column:account_id"`
	ScheduledAt      sql.NullTime      `gorm:"column:scheduled_at"`
	Params           JSONValue         `gorm:"column:params"`
	MediaAttachments []MediaAttachment `gorm:"foreignKey:ScheduledStatusID"`
}

func (ScheduledStatus) TableName() string { return "scheduled_statuses" }

func ScheduledStatusAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type Mention struct {
	ID        int64         `gorm:"primaryKey;column:id"`
	StatusID  sql.NullInt64 `gorm:"column:status_id"`
	CreatedAt time.Time     `gorm:"column:created_at"`
	UpdatedAt time.Time     `gorm:"column:updated_at"`
	AccountID sql.NullInt64 `gorm:"column:account_id"`
	Silent    bool          `gorm:"column:silent"`
	Account   Account       `gorm:"foreignKey:AccountID"`
}

func (Mention) TableName() string { return "mentions" }

func MentionStatusID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func MentionAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type Tag struct {
	ID                int64           `gorm:"primaryKey;column:id"`
	Name              string          `gorm:"column:name"`
	CreatedAt         time.Time       `gorm:"column:created_at"`
	UpdatedAt         time.Time       `gorm:"column:updated_at"`
	Usable            sql.NullBool    `gorm:"column:usable"`
	Trendable         sql.NullBool    `gorm:"column:trendable"`
	Listable          sql.NullBool    `gorm:"column:listable"`
	ReviewedAt        sql.NullTime    `gorm:"column:reviewed_at"`
	RequestedReviewAt sql.NullTime    `gorm:"column:requested_review_at"`
	LastStatusAt      sql.NullTime    `gorm:"column:last_status_at"`
	MaxScore          sql.NullFloat64 `gorm:"column:max_score"`
	MaxScoreAt        sql.NullTime    `gorm:"column:max_score_at"`
	DisplayName       sql.NullString  `gorm:"column:display_name"`
}

func (Tag) TableName() string { return "tags" }

func (tag Tag) DisplayNameValue() string {
	if tag.DisplayName.Valid {
		return tag.DisplayName.String
	}
	return tag.Name
}

type StatusTag struct {
	TagID    int64  `gorm:"primaryKey;column:tag_id"`
	StatusID int64  `gorm:"primaryKey;column:status_id"`
	Status   Status `gorm:"foreignKey:StatusID"`
	Tag      Tag    `gorm:"foreignKey:TagID"`
}

func (StatusTag) TableName() string { return "statuses_tags" }

type TagFollow struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	TagID     int64     `gorm:"column:tag_id"`
	AccountID int64     `gorm:"column:account_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	Tag       Tag       `gorm:"foreignKey:TagID"`
}

func (TagFollow) TableName() string { return "tag_follows" }

type FeaturedTag struct {
	ID            int64          `gorm:"primaryKey;column:id"`
	AccountID     int64          `gorm:"column:account_id"`
	TagID         int64          `gorm:"column:tag_id"`
	StatusesCount int64          `gorm:"column:statuses_count"`
	LastStatusAt  sql.NullTime   `gorm:"column:last_status_at"`
	CreatedAt     time.Time      `gorm:"column:created_at"`
	UpdatedAt     time.Time      `gorm:"column:updated_at"`
	Name          sql.NullString `gorm:"column:name"`
	Account       Account        `gorm:"foreignKey:AccountID"`
	Tag           Tag            `gorm:"foreignKey:TagID"`
}

func (FeaturedTag) TableName() string { return "featured_tags" }

func (tag FeaturedTag) DisplayNameValue() string {
	if tag.Name.Valid {
		return tag.Name.String
	}
	return tag.Tag.DisplayNameValue()
}

type FollowRecommendationSuppression struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	AccountID int64     `gorm:"column:account_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	Account   Account   `gorm:"foreignKey:AccountID"`
}

func (FollowRecommendationSuppression) TableName() string {
	return "follow_recommendation_suppressions"
}

type FollowRecommendationMute struct {
	ID              int64     `gorm:"primaryKey;column:id"`
	AccountID       int64     `gorm:"column:account_id"`
	TargetAccountID int64     `gorm:"column:target_account_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
	Account         Account   `gorm:"foreignKey:AccountID"`
	TargetAccount   Account   `gorm:"foreignKey:TargetAccountID"`
}

func (FollowRecommendationMute) TableName() string { return "follow_recommendation_mutes" }

type GeneratedAnnualReport struct {
	ID            int64        `gorm:"primaryKey;column:id"`
	AccountID     int64        `gorm:"column:account_id"`
	Year          int          `gorm:"column:year"`
	Data          JSONValue    `gorm:"column:data"`
	SchemaVersion int          `gorm:"column:schema_version"`
	ViewedAt      sql.NullTime `gorm:"column:viewed_at"`
	CreatedAt     time.Time    `gorm:"column:created_at"`
	UpdatedAt     time.Time    `gorm:"column:updated_at"`
	Account       Account      `gorm:"foreignKey:AccountID"`
}

func (GeneratedAnnualReport) TableName() string { return "generated_annual_reports" }

type Favourite struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	AccountID int64     `gorm:"column:account_id"`
	StatusID  int64     `gorm:"column:status_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	Account   Account   `gorm:"foreignKey:AccountID"`
}

func (Favourite) TableName() string { return "favourites" }

type StatusPin struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	AccountID int64     `gorm:"column:account_id"`
	StatusID  int64     `gorm:"column:status_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (StatusPin) TableName() string { return "status_pins" }

type Poll struct {
	ID            int64         `gorm:"primaryKey;column:id"`
	AccountID     sql.NullInt64 `gorm:"column:account_id"`
	StatusID      sql.NullInt64 `gorm:"column:status_id"`
	ExpiresAt     sql.NullTime  `gorm:"column:expires_at"`
	Options       StringArray   `gorm:"column:options"`
	CachedTallies Int64Array    `gorm:"column:cached_tallies"`
	Multiple      bool          `gorm:"column:multiple"`
	HideTotals    bool          `gorm:"column:hide_totals"`
	VotesCount    int64         `gorm:"column:votes_count"`
	LastFetchedAt sql.NullTime  `gorm:"column:last_fetched_at"`
	CreatedAt     time.Time     `gorm:"column:created_at"`
	UpdatedAt     time.Time     `gorm:"column:updated_at"`
	LockVersion   int           `gorm:"column:lock_version"`
	VotersCount   sql.NullInt64 `gorm:"column:voters_count"`
	Votes         []PollVote    `gorm:"foreignKey:PollID"`
	Account       Account       `gorm:"foreignKey:AccountID"`
	CustomEmojis  []CustomEmoji `gorm:"-"`
}

func (Poll) TableName() string { return "polls" }

func PollAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type PollVote struct {
	ID        int64          `gorm:"primaryKey;column:id"`
	AccountID sql.NullInt64  `gorm:"column:account_id"`
	PollID    sql.NullInt64  `gorm:"column:poll_id"`
	Choice    int            `gorm:"column:choice"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	URI       NullSafeString `gorm:"column:uri"`
}

func (PollVote) TableName() string { return "poll_votes" }

func PollVoteAccountID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func PollVotePollID(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

type Bookmark struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	AccountID int64     `gorm:"column:account_id"`
	StatusID  int64     `gorm:"column:status_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	Status    Status    `gorm:"foreignKey:StatusID"`
}

func (Bookmark) TableName() string { return "bookmarks" }

type Notification struct {
	ID             int64                              `gorm:"primaryKey;column:id"`
	ActivityID     int64                              `gorm:"column:activity_id"`
	ActivityType   string                             `gorm:"column:activity_type"`
	CreatedAt      time.Time                          `gorm:"column:created_at"`
	UpdatedAt      time.Time                          `gorm:"column:updated_at"`
	AccountID      int64                              `gorm:"column:account_id"`
	FromAccountID  int64                              `gorm:"column:from_account_id"`
	Type           NullSafeString                     `gorm:"column:type"`
	Filtered       bool                               `gorm:"column:filtered"`
	GroupKey       sql.NullString                     `gorm:"column:group_key"`
	FromAccount    Account                            `gorm:"foreignKey:FromAccountID"`
	TargetStatus   *Status                            `gorm:"-"`
	Report         *Report                            `gorm:"-"`
	AccountWarning *AccountWarning                    `gorm:"-"`
	SeveranceEvent *AccountRelationshipSeveranceEvent `gorm:"-"`
}

func (Notification) TableName() string { return "notifications" }

type NotificationPolicy struct {
	ID                 int64     `gorm:"primaryKey;column:id"`
	AccountID          int64     `gorm:"column:account_id"`
	CreatedAt          time.Time `gorm:"column:created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at"`
	ForNotFollowing    int       `gorm:"column:for_not_following"`
	ForNotFollowers    int       `gorm:"column:for_not_followers"`
	ForNewAccounts     int       `gorm:"column:for_new_accounts"`
	ForPrivateMentions int       `gorm:"column:for_private_mentions"`
	ForLimitedAccounts int       `gorm:"column:for_limited_accounts"`
	Account            Account   `gorm:"foreignKey:AccountID"`
}

func (NotificationPolicy) TableName() string { return "notification_policies" }

type NotificationPermission struct {
	ID            int64     `gorm:"primaryKey;column:id"`
	AccountID     int64     `gorm:"column:account_id"`
	FromAccountID int64     `gorm:"column:from_account_id"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
	Account       Account   `gorm:"foreignKey:AccountID"`
	FromAccount   Account   `gorm:"foreignKey:FromAccountID"`
}

func (NotificationPermission) TableName() string { return "notification_permissions" }

type NotificationRequest struct {
	ID                 int64         `gorm:"primaryKey;column:id"`
	AccountID          int64         `gorm:"column:account_id"`
	FromAccountID      int64         `gorm:"column:from_account_id"`
	LastStatusID       sql.NullInt64 `gorm:"column:last_status_id"`
	NotificationsCount int64         `gorm:"column:notifications_count"`
	CreatedAt          time.Time     `gorm:"column:created_at"`
	UpdatedAt          time.Time     `gorm:"column:updated_at"`
	Account            Account       `gorm:"foreignKey:AccountID"`
	FromAccount        Account       `gorm:"foreignKey:FromAccountID"`
	LastStatus         *Status       `gorm:"foreignKey:LastStatusID"`
}

func (NotificationRequest) TableName() string { return "notification_requests" }

func (n Notification) ResolvedType() string {
	if n.Type != "" {
		return string(n.Type)
	}
	switch n.ActivityType {
	case "Mention":
		return "mention"
	case "Status":
		return "reblog"
	case "Follow":
		return "follow"
	case "FollowRequest":
		return "follow_request"
	case "Favourite":
		return "favourite"
	case "Poll":
		return "poll"
	default:
		return n.ActivityType
	}
}

type Report struct {
	ID                     int64          `gorm:"primaryKey;column:id"`
	StatusIDs              Int64Array     `gorm:"column:status_ids"`
	Comment                string         `gorm:"column:comment"`
	CreatedAt              time.Time      `gorm:"column:created_at"`
	UpdatedAt              time.Time      `gorm:"column:updated_at"`
	AccountID              int64          `gorm:"column:account_id"`
	ActionTakenByAccountID sql.NullInt64  `gorm:"column:action_taken_by_account_id"`
	TargetAccountID        int64          `gorm:"column:target_account_id"`
	AssignedAccountID      sql.NullInt64  `gorm:"column:assigned_account_id"`
	URI                    sql.NullString `gorm:"column:uri"`
	Forwarded              sql.NullBool   `gorm:"column:forwarded"`
	Category               int            `gorm:"column:category"`
	ActionTakenAt          sql.NullTime   `gorm:"column:action_taken_at"`
	RuleIDs                Int64Array     `gorm:"column:rule_ids"`
	ApplicationID          sql.NullInt64  `gorm:"column:application_id"`
	Account                Account        `gorm:"foreignKey:AccountID"`
	TargetAccount          Account        `gorm:"foreignKey:TargetAccountID"`
	AssignedAccount        Account        `gorm:"foreignKey:AssignedAccountID"`
	ActionTakenByAccount   Account        `gorm:"foreignKey:ActionTakenByAccountID"`
}

func (Report) TableName() string { return "reports" }

type RelationshipSeveranceEvent struct {
	ID         int64     `gorm:"primaryKey;column:id"`
	Type       int       `gorm:"column:type"`
	TargetName string    `gorm:"column:target_name"`
	Purged     bool      `gorm:"column:purged"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

func (RelationshipSeveranceEvent) TableName() string { return "relationship_severance_events" }

type AccountRelationshipSeveranceEvent struct {
	ID                           int64                      `gorm:"primaryKey;column:id"`
	AccountID                    int64                      `gorm:"column:account_id"`
	RelationshipSeveranceEventID int64                      `gorm:"column:relationship_severance_event_id"`
	FollowersCount               int                        `gorm:"column:followers_count"`
	FollowingCount               int                        `gorm:"column:following_count"`
	CreatedAt                    time.Time                  `gorm:"column:created_at"`
	UpdatedAt                    time.Time                  `gorm:"column:updated_at"`
	Account                      Account                    `gorm:"foreignKey:AccountID"`
	RelationshipSeveranceEvent   RelationshipSeveranceEvent `gorm:"foreignKey:RelationshipSeveranceEventID"`
}

func (AccountRelationshipSeveranceEvent) TableName() string {
	return "account_relationship_severance_events"
}

type SeveredRelationship struct {
	ID                           int64                      `gorm:"primaryKey;column:id"`
	RelationshipSeveranceEventID int64                      `gorm:"column:relationship_severance_event_id"`
	LocalAccountID               int64                      `gorm:"column:local_account_id"`
	RemoteAccountID              int64                      `gorm:"column:remote_account_id"`
	Direction                    int                        `gorm:"column:direction"`
	ShowReblogs                  sql.NullBool               `gorm:"column:show_reblogs"`
	Notify                       sql.NullBool               `gorm:"column:notify"`
	Languages                    StringArray                `gorm:"column:languages;type:text[]"`
	CreatedAt                    time.Time                  `gorm:"column:created_at"`
	UpdatedAt                    time.Time                  `gorm:"column:updated_at"`
	RelationshipSeveranceEvent   RelationshipSeveranceEvent `gorm:"foreignKey:RelationshipSeveranceEventID"`
	LocalAccount                 Account                    `gorm:"foreignKey:LocalAccountID"`
	RemoteAccount                Account                    `gorm:"foreignKey:RemoteAccountID"`
}

func (SeveredRelationship) TableName() string { return "severed_relationships" }

type ReportNote struct {
	ID        int64     `gorm:"primaryKey;column:id"`
	Content   string    `gorm:"column:content"`
	ReportID  int64     `gorm:"column:report_id"`
	AccountID int64     `gorm:"column:account_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	Account   Account   `gorm:"foreignKey:AccountID"`
	Report    Report    `gorm:"foreignKey:ReportID"`
}

func (ReportNote) TableName() string { return "report_notes" }

type Follow struct {
	ID              int64          `gorm:"primaryKey;column:id"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
	AccountID       int64          `gorm:"column:account_id"`
	TargetAccountID int64          `gorm:"column:target_account_id"`
	ShowReblogs     bool           `gorm:"column:show_reblogs"`
	Notify          bool           `gorm:"column:notify"`
	URI             NullSafeString `gorm:"column:uri"`
	Languages       StringArray    `gorm:"column:languages"`
	Account         Account        `gorm:"foreignKey:AccountID"`
	TargetAccount   Account        `gorm:"foreignKey:TargetAccountID"`
}

func (Follow) TableName() string { return "follows" }

type FollowRequest struct {
	ID              int64          `gorm:"primaryKey;column:id"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
	AccountID       int64          `gorm:"column:account_id"`
	TargetAccountID int64          `gorm:"column:target_account_id"`
	ShowReblogs     bool           `gorm:"column:show_reblogs"`
	Notify          bool           `gorm:"column:notify"`
	URI             NullSafeString `gorm:"column:uri"`
	Languages       StringArray    `gorm:"column:languages"`
	Account         Account        `gorm:"foreignKey:AccountID"`
}

func (FollowRequest) TableName() string { return "follow_requests" }

type Block struct {
	ID              int64          `gorm:"primaryKey;column:id"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
	AccountID       int64          `gorm:"column:account_id"`
	TargetAccountID int64          `gorm:"column:target_account_id"`
	URI             NullSafeString `gorm:"column:uri"`
	TargetAccount   Account        `gorm:"foreignKey:TargetAccountID"`
}

func (Block) TableName() string { return "blocks" }

type Mute struct {
	ID                int64        `gorm:"primaryKey;column:id"`
	CreatedAt         time.Time    `gorm:"column:created_at"`
	UpdatedAt         time.Time    `gorm:"column:updated_at"`
	HideNotifications bool         `gorm:"column:hide_notifications"`
	AccountID         int64        `gorm:"column:account_id"`
	TargetAccountID   int64        `gorm:"column:target_account_id"`
	ExpiresAt         sql.NullTime `gorm:"column:expires_at"`
	TargetAccount     Account      `gorm:"foreignKey:TargetAccountID"`
}

func (Mute) TableName() string { return "mutes" }
