package api

import (
	"database/sql"
	"errors"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func recordUserDomainSeverance(tx *gorm.DB, accountID int64, domain string, outgoing []models.Follow, incoming []models.Follow, now time.Time) (int64, error) {
	if tx == nil || accountID == 0 || len(outgoing)+len(incoming) == 0 {
		return 0, nil
	}
	event := models.RelationshipSeveranceEvent{Type: 1, TargetName: domain, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&event).Error; err != nil {
		return 0, err
	}
	for _, follow := range outgoing {
		row := severedRelationshipFromFollow(event.ID, accountID, follow.TargetAccountID, 1, follow, now)
		if err := tx.Create(&row).Error; err != nil {
			return 0, err
		}
	}
	for _, follow := range incoming {
		row := severedRelationshipFromFollow(event.ID, accountID, follow.AccountID, 0, follow, now)
		if err := tx.Create(&row).Error; err != nil {
			return 0, err
		}
	}
	accountEvent := models.AccountRelationshipSeveranceEvent{
		AccountID: accountID, RelationshipSeveranceEventID: event.ID, FollowersCount: len(incoming), FollowingCount: len(outgoing), CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&accountEvent).Error; err != nil {
		return 0, err
	}
	return createSeveranceNotification(tx, accountEvent, now)
}

func severedRelationshipFromFollow(eventID int64, localAccountID int64, remoteAccountID int64, direction int, follow models.Follow, now time.Time) models.SeveredRelationship {
	return models.SeveredRelationship{
		RelationshipSeveranceEventID: eventID, LocalAccountID: localAccountID, RemoteAccountID: remoteAccountID, Direction: direction,
		ShowReblogs: sql.NullBool{Bool: follow.ShowReblogs, Valid: direction == 1}, Notify: sql.NullBool{Bool: follow.Notify, Valid: direction == 1},
		Languages: follow.Languages, CreatedAt: now, UpdatedAt: now,
	}
}

func createSeveranceNotification(tx *gorm.DB, event models.AccountRelationshipSeveranceEvent, now time.Time) (int64, error) {
	var existing models.Notification
	err := tx.Where("account_id = ? AND activity_type = ? AND activity_id = ? AND type = ?", event.AccountID, "AccountRelationshipSeveranceEvent", event.ID, "severed_relationships").First(&existing).Error
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	notification := models.Notification{
		ActivityID: event.ID, ActivityType: "AccountRelationshipSeveranceEvent", AccountID: event.AccountID, FromAccountID: event.AccountID,
		Type: "severed_relationships", CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&notification).Error; err != nil {
		return 0, err
	}
	return notification.ID, nil
}

func (s *Server) recordAdminDomainSeverance(database *gorm.DB, domain string, occurredAt time.Time) ([]int64, error) {
	if s == nil || database == nil || domain == "" {
		return nil, nil
	}
	var outgoing []models.Follow
	if err := database.Model(&models.Follow{}).
		Joins("JOIN accounts severance_local_accounts ON severance_local_accounts.id = follows.account_id AND severance_local_accounts.domain IS NULL").
		Joins("JOIN accounts severance_remote_accounts ON severance_remote_accounts.id = follows.target_account_id").
		Where(domainAndSubdomainsSQL("severance_remote_accounts.domain"), domain, "%."+domain).
		Find(&outgoing).Error; err != nil {
		return nil, err
	}
	var incoming []models.Follow
	if err := database.Model(&models.Follow{}).
		Joins("JOIN accounts severance_remote_accounts ON severance_remote_accounts.id = follows.account_id").
		Joins("JOIN accounts severance_local_accounts ON severance_local_accounts.id = follows.target_account_id AND severance_local_accounts.domain IS NULL").
		Where(domainAndSubdomainsSQL("severance_remote_accounts.domain"), domain, "%."+domain).
		Find(&incoming).Error; err != nil {
		return nil, err
	}
	if len(outgoing)+len(incoming) == 0 {
		return nil, nil
	}
	var notificationIDs []int64
	err := database.Transaction(func(tx *gorm.DB) error {
		var event models.RelationshipSeveranceEvent
		err := tx.Where("type = ? AND target_name = ? AND created_at = ?", 0, domain, occurredAt).First(&event).Error
		created := false
		if errors.Is(err, gorm.ErrRecordNotFound) {
			event = models.RelationshipSeveranceEvent{Type: 0, TargetName: domain, CreatedAt: occurredAt, UpdatedAt: occurredAt}
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
			created = true
		} else if err != nil {
			return err
		}
		if created {
			for _, follow := range outgoing {
				row := severedRelationshipFromFollow(event.ID, follow.AccountID, follow.TargetAccountID, 1, follow, occurredAt)
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
			for _, follow := range incoming {
				row := severedRelationshipFromFollow(event.ID, follow.TargetAccountID, follow.AccountID, 0, follow, occurredAt)
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
		}
		counts := map[int64]*models.AccountRelationshipSeveranceEvent{}
		for _, follow := range outgoing {
			if counts[follow.AccountID] == nil {
				counts[follow.AccountID] = &models.AccountRelationshipSeveranceEvent{AccountID: follow.AccountID, RelationshipSeveranceEventID: event.ID, CreatedAt: occurredAt, UpdatedAt: occurredAt}
			}
			counts[follow.AccountID].FollowingCount++
		}
		for _, follow := range incoming {
			if counts[follow.TargetAccountID] == nil {
				counts[follow.TargetAccountID] = &models.AccountRelationshipSeveranceEvent{AccountID: follow.TargetAccountID, RelationshipSeveranceEventID: event.ID, CreatedAt: occurredAt, UpdatedAt: occurredAt}
			}
			counts[follow.TargetAccountID].FollowersCount++
		}
		for _, accountEvent := range counts {
			var existing models.AccountRelationshipSeveranceEvent
			err := tx.Where("account_id = ? AND relationship_severance_event_id = ?", accountEvent.AccountID, event.ID).First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(accountEvent).Error; err != nil {
					return err
				}
				existing = *accountEvent
			} else if err != nil {
				return err
			}
			id, err := createSeveranceNotification(tx, existing, occurredAt)
			if err != nil {
				return err
			}
			if id != 0 {
				notificationIDs = append(notificationIDs, id)
			}
		}
		return nil
	})
	return uniqueInt64s(notificationIDs), err
}
