package api

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func (s *Server) runRemoteCollectionRepairWorker(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		s.runSchedulerWithRedisLock(ctx, "repair_remote_collections_scheduler", 24*time.Hour, func() {
			s.repairMisattributedRemoteCollections(ctx)
		})
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "repair_remote_collections_scheduler", 24*time.Hour, func() {
				s.repairMisattributedRemoteCollections(ctx)
			})
		}
	}
}

func (s *Server) repairMisattributedRemoteCollections(ctx context.Context) int {
	if s == nil || s.db == nil {
		return 0
	}
	var maxID int64
	if err := s.db.WithContext(ctx).Model(&models.Collection{}).Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
		return 0
	}
	markerKey := redisConfig(s.cfg).prefix + "remote_collection_repair:last_known_good"
	lastKnown := int64(0)
	if value, err := s.redisCommand(ctx, "GET", markerKey); err == nil {
		lastKnown = railsToInt64(strings.TrimSpace(stringValue(value)))
	}
	query := s.db.WithContext(ctx).Model(&models.Collection{}).
		Joins("JOIN accounts ON accounts.id = collections.account_id").
		Where("collections.local = FALSE").
		Where("substring(collections.uri from '/ap/users/\\d+/') != substring(accounts.collections_url from '/ap/users/\\d+/')")
	if lastKnown > 0 {
		query = query.Where("collections.id >= ?", lastKnown)
	}
	var collections []models.Collection
	if err := query.Order("collections.id ASC").Find(&collections).Error; err != nil {
		return 0
	}
	repaired := 0
	successful := true
	for _, collection := range collections {
		fetched, err := s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentSignerAcceptAndContext(ctx, collection.URI.String, collection.URI.String, paonUserAgent(s.cfg), nil, activityResourceAcceptHeader)
		if err != nil || fetched.Object.AttributedTo == "" {
			successful = false
			continue
		}
		account, err := s.activityActorForURIForRequest(fetched.Object.AttributedTo, "")
		if err != nil || account == nil {
			successful = false
			continue
		}
		if err := s.db.WithContext(ctx).Model(&models.Collection{}).Where("id = ?", collection.ID).Update("account_id", account.ID).Error; err != nil {
			successful = false
			continue
		}
		repaired++
	}
	if successful {
		_, _ = s.redisCommand(ctx, "SET", markerKey, strconv.FormatInt(maxID, 10))
	}
	return repaired
}
