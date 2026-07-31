package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) hydrateAccountCustomEmojis(account *models.Account) error {
	return s.hydrateAccountCustomEmojisDepth(account, 0)
}

func (s *Server) hydrateAccountCustomEmojisDepth(account *models.Account, depth int) error {
	if s.db == nil || account == nil || account.ID == 0 {
		return nil
	}
	shortcodes := statusEmbedEmojiShortcodes(accountEmojiText(*account))
	emojis, err := s.customEmojisFromShortcodes(shortcodes, account.Domain)
	if err != nil {
		return err
	}
	account.CustomEmojis = emojis
	if depth == 0 && account.MovedToAccount != nil && account.MovedToAccount.ID != 0 {
		return s.hydrateAccountCustomEmojisDepth(account.MovedToAccount, depth+1)
	}
	return nil
}

func (s *Server) hydrateStatusCustomEmojis(status *models.Status) error {
	if s.db == nil || status == nil || status.ID == 0 {
		return nil
	}
	if err := s.hydrateAccountCustomEmojis(&status.Account); err != nil {
		return err
	}
	if status.Poll != nil {
		if status.Poll.Account.ID == 0 {
			status.Poll.Account = status.Account
		}
		if err := s.hydratePollCustomEmojis(status.Poll); err != nil {
			return err
		}
	}
	shortcodes := statusEmbedEmojiShortcodes(statusEmojiText(*status))
	emojis, err := s.customEmojisFromShortcodes(shortcodes, status.Account.Domain)
	if err != nil {
		return err
	}
	status.CustomEmojis = emojis

	if status.Reblog != nil && status.Reblog.ID != 0 {
		if err := s.hydrateStatusCustomEmojis(status.Reblog); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) hydrateAnnouncementCustomEmojis(announcement *models.Announcement) error {
	if s.db == nil || announcement == nil || announcement.ID == 0 {
		return nil
	}
	shortcodes := statusEmbedEmojiShortcodes(announcement.Text)
	emojis, err := s.customEmojisFromShortcodes(shortcodes, sql.NullString{})
	if err != nil {
		return err
	}
	announcement.CustomEmojis = emojis
	return nil
}

func (s *Server) hydrateAnnouncementMentions(announcement *models.Announcement) error {
	if s.db == nil || announcement == nil || announcement.ID == 0 {
		return nil
	}
	refs := statusMentionRefs(announcement.Text)
	if len(refs) == 0 {
		announcement.MentionAccounts = nil
		return nil
	}
	accounts := make([]models.Account, 0, len(refs))
	for _, ref := range refs {
		account, err := s.announcementAccountFromMentionRef(ref)
		if err != nil {
			return err
		}
		if account != nil {
			accounts = append(accounts, *account)
		}
	}
	announcement.MentionAccounts = accounts
	return nil
}

func (s *Server) hydrateAnnouncementReferences(announcement *models.Announcement) error {
	if err := s.hydrateAnnouncementMentions(announcement); err != nil {
		return err
	}
	return s.hydrateAnnouncementCustomEmojis(announcement)
}

func (s *Server) announcementAccountFromMentionRef(ref statusMentionRef) (*models.Account, error) {
	var account models.Account
	query := s.db.Where("lower(username) = ?", ref.Username)
	if s.localMentionRef(ref) {
		query = query.Where("domain IS NULL OR domain = ''")
	} else {
		query = query.Where("lower(domain) = ?", ref.Domain)
	}
	if err := query.First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &account, nil
}

func (s *Server) hydrateStatusesCustomEmojis(statuses []models.Status) error {
	for i := range statuses {
		if err := s.hydrateStatusCustomEmojis(&statuses[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) customEmojisFromShortcodes(shortcodes []string, domain sql.NullString) ([]models.CustomEmoji, error) {
	return customEmojisFromShortcodesDB(s.db, shortcodes, domain)
}

func customEmojisFromShortcodesDB(db *gorm.DB, shortcodes []string, domain sql.NullString) ([]models.CustomEmoji, error) {
	if db == nil || len(shortcodes) == 0 {
		return nil, nil
	}
	var emojis []models.CustomEmoji
	query := customEmojiDomainQuery(db.Where("shortcode IN ? AND disabled = false", shortcodes), domain)
	if err := query.Find(&emojis).Error; err != nil {
		return nil, err
	}
	return orderCustomEmojisByShortcode(shortcodes, emojis), nil
}

// hydrateActivityPubStatusCustomEmojis keeps the inbound ActivityPub body unchanged and
// supplies the same derived emoji associations Rails exposes through its model methods.
// Keeping :shortcode: in status.Text is required by the Mastodon REST client contract.
func (s *Server) hydrateActivityPubStatusCustomEmojis(tx *gorm.DB, status *models.Status, actor *models.Account) error {
	if tx == nil || status == nil || actor == nil {
		return nil
	}
	accountEmojis, err := customEmojisFromShortcodesDB(tx, statusEmbedEmojiShortcodes(accountEmojiText(*actor)), actor.Domain)
	if err != nil {
		return err
	}
	actor.CustomEmojis = accountEmojis
	status.Account.CustomEmojis = accountEmojis

	statusEmojis, err := customEmojisFromShortcodesDB(tx, statusEmbedEmojiShortcodes(statusEmojiText(*status)), actor.Domain)
	if err != nil {
		return err
	}
	status.CustomEmojis = statusEmojis
	if status.Poll != nil {
		pollEmojis, err := customEmojisFromShortcodesDB(tx, statusEmbedEmojiShortcodes(strings.Join(status.Poll.Options, "\n")), actor.Domain)
		if err != nil {
			return err
		}
		status.Poll.CustomEmojis = pollEmojis
	}
	return nil
}

func customEmojiDomainQuery(query *gorm.DB, domain sql.NullString) *gorm.DB {
	if normalized := customEmojiDomainValue(domain); normalized.Valid {
		return query.Where("lower(domain) = ?", normalized.String)
	}
	return query.Where("domain IS NULL")
}

func customEmojiDomainValue(domain sql.NullString) sql.NullString {
	if !domain.Valid {
		return sql.NullString{}
	}
	value := strings.TrimSpace(domain.String)
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: strings.ToLower(value), Valid: true}
}

func statusEmojiText(status models.Status) string {
	parts := []string{status.SpoilerText, status.Text}
	if status.Poll != nil {
		parts = append(parts, status.Poll.Options...)
	}
	return strings.Join(parts, "\n")
}

func accountEmojiText(account models.Account) string {
	parts := []string{account.Note, account.DisplayName}
	for _, field := range accountEmojiFields(account.Fields) {
		parts = append(parts, field.Name, field.Value)
	}
	return strings.Join(parts, "\n")
}

func accountEmojiFields(raw []byte) []struct {
	Name  string `json:"name"`
	Value string `json:"value"`
} {
	if len(raw) == 0 {
		return nil
	}
	var fields []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return fields
}
