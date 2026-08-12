package api

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type adminAuditLogTarget struct {
	Type            string
	ID              int64
	HumanIdentifier string
	RouteParam      string
	Permalink       string
}

func adminActionLogForTarget(actorAccountID int64, action string, target adminAuditLogTarget, at time.Time) models.AdminActionLog {
	log := models.AdminActionLog{
		Action:    action,
		CreatedAt: at,
		UpdatedAt: at,
	}
	if actorAccountID > 0 {
		log.AccountID = sql.NullInt64{Int64: actorAccountID, Valid: true}
	}
	if target.Type != "" {
		log.TargetType = sql.NullString{String: target.Type, Valid: true}
	}
	if target.ID > 0 {
		log.TargetID = sql.NullInt64{Int64: target.ID, Valid: true}
	}
	if target.HumanIdentifier != "" {
		log.HumanIdentifier = sql.NullString{String: target.HumanIdentifier, Valid: true}
	}
	if target.RouteParam != "" {
		log.RouteParam = sql.NullString{String: target.RouteParam, Valid: true}
	}
	if target.Permalink != "" {
		log.Permalink = sql.NullString{String: target.Permalink, Valid: true}
	}
	return log
}

func logAdminAction(tx *gorm.DB, actorAccountID int64, action string, target adminAuditLogTarget, at time.Time) error {
	if tx == nil {
		return nil
	}
	log := adminActionLogForTarget(actorAccountID, action, target, at)
	return tx.Create(&log).Error
}

func customEmojiAuditLogTarget(emoji models.CustomEmoji) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "CustomEmoji",
		ID:              emoji.ID,
		HumanIdentifier: emoji.Shortcode,
	}
}

func domainBlockAuditLogTarget(block models.DomainBlock) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "DomainBlock",
		ID:              block.ID,
		HumanIdentifier: block.Domain,
	}
}

func domainAllowAuditLogTarget(allow models.DomainAllow) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "DomainAllow",
		ID:              allow.ID,
		HumanIdentifier: allow.Domain,
	}
}

func instanceAuditLogTarget(domain string) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "Instance",
		HumanIdentifier: domain,
	}
}

func emailDomainBlockAuditLogTarget(block models.EmailDomainBlock) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "EmailDomainBlock",
		ID:              block.ID,
		HumanIdentifier: block.Domain,
	}
}

func canonicalEmailBlockAuditLogTarget(block models.CanonicalEmailBlock) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "CanonicalEmailBlock",
		ID:              block.ID,
		HumanIdentifier: block.CanonicalEmailHash,
	}
}

func ipBlockAuditLogTarget(block models.IPBlock) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "IpBlock",
		ID:              block.ID,
		HumanIdentifier: block.IP,
	}
}

func appealAuditLogTarget(appeal models.Appeal) adminAuditLogTarget {
	target := adminAuditLogTarget{
		Type: "Appeal",
		ID:   appeal.ID,
	}
	if appeal.AccountWarningID > 0 {
		target.RouteParam = strconv.FormatInt(appeal.AccountWarningID, 10)
	}
	if appeal.Account.ID != 0 {
		target.HumanIdentifier = appeal.Account.Acct()
	}
	return target
}

func accountAuditLogTarget(account models.Account) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "Account",
		ID:              account.ID,
		HumanIdentifier: account.Acct(),
	}
}

func userAuditLogTarget(user models.User) adminAuditLogTarget {
	target := adminAuditLogTarget{
		Type: "User",
		ID:   user.ID,
	}
	if user.AccountID > 0 {
		target.RouteParam = strconv.FormatInt(user.AccountID, 10)
	}
	if user.Account != nil {
		target.HumanIdentifier = user.Account.Acct()
	}
	return target
}

func accountWarningAuditLogTarget(warning models.AccountWarning) adminAuditLogTarget {
	target := adminAuditLogTarget{
		Type: "AccountWarning",
		ID:   warning.ID,
	}
	if warning.TargetAccount.ID != 0 {
		target.HumanIdentifier = warning.TargetAccount.Acct()
	}
	return target
}

func accountModerationNoteAuditLogTarget(note models.AccountModerationNote) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type: "AccountModerationNote",
		ID:   note.ID,
	}
}

func statusAuditLogTarget(status models.Status) adminAuditLogTarget {
	target := adminAuditLogTarget{
		Type: "Status",
		ID:   status.ID,
	}
	if status.Account.ID != 0 {
		target.HumanIdentifier = status.Account.Acct()
	}
	if status.URI.Valid && status.URI.String != "" {
		target.Permalink = strings.TrimSpace(status.URI.String)
	}
	return target
}

func tagAuditLogTarget(tag models.Tag) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "Tag",
		ID:              tag.ID,
		HumanIdentifier: tag.DisplayNameValue(),
	}
}

func previewCardAuditLogTarget(card models.PreviewCard) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "PreviewCard",
		ID:              card.ID,
		HumanIdentifier: firstNonEmpty(card.Title, card.URL),
	}
}

func previewCardProviderAuditLogTarget(provider models.PreviewCardProvider) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "PreviewCardProvider",
		ID:              provider.ID,
		HumanIdentifier: provider.Domain,
	}
}

func reportAuditLogTarget(report models.Report) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type: "Report",
		ID:   report.ID,
	}
}

func userRoleAuditLogTarget(role models.UserRole) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "UserRole",
		ID:              role.ID,
		HumanIdentifier: role.Name,
	}
}

func relayAuditLogTarget(relay models.Relay) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "Relay",
		ID:              relay.ID,
		HumanIdentifier: relay.InboxURL,
	}
}

func announcementAuditLogTarget(announcement models.Announcement) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "Announcement",
		ID:              announcement.ID,
		HumanIdentifier: announcement.Text,
	}
}

func unavailableDomainAuditLogTarget(domain models.UnavailableDomain) adminAuditLogTarget {
	return adminAuditLogTarget{
		Type:            "UnavailableDomain",
		ID:              domain.ID,
		HumanIdentifier: domain.Domain,
	}
}
