package api

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strconv"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const remoteMediaRedownloadRetryKey = "paon:media:redownload:retry"
const remoteMediaRedownloadRetryLimit = 3

type remoteMediaRedownloadRetryJob struct {
	MediaAttachmentID int64 `json:"media_attachment_id"`
	Attempts          int   `json:"attempts"`
	CreatedAt         int64 `json:"created_at"`
}

func (s *Server) enqueueRemoteMediaRedownload(mediaAttachmentID int64) {
	if s == nil || s.db == nil || mediaAttachmentID == 0 {
		return
	}
	if s.enqueueRedownloadMediaTask(mediaAttachmentID) {
		return
	}
	job := remoteMediaRedownloadRetryJob{
		MediaAttachmentID: mediaAttachmentID,
		Attempts:          0,
		CreatedAt:         time.Now().UTC().Unix(),
	}
	_ = s.enqueueRemoteMediaRedownloadRetryJob(context.Background(), job)
}

func (s *Server) enqueueRemoteMediaRedownloadRetryJob(ctx context.Context, job remoteMediaRedownloadRetryJob) error {
	if s == nil || job.MediaAttachmentID == 0 {
		return nil
	}
	encoded, runAt, err := nextRemoteMediaRedownloadRetry(job, time.Now().UTC())
	if err != nil {
		return err
	}
	_, err = s.redisCommand(ctx, "ZADD", redisConfig(s.cfg).prefix+remoteMediaRedownloadRetryKey, strconv.FormatInt(runAt.Unix(), 10), encoded)
	return err
}

func nextRemoteMediaRedownloadRetry(job remoteMediaRedownloadRetryJob, now time.Time) (string, time.Time, error) {
	job.Attempts++
	runAt := now.UTC().Add(remoteMediaRedownloadRetryDelay(job.Attempts))
	encoded, err := json.Marshal(job)
	return string(encoded), runAt, err
}

func remoteMediaRedownloadRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > remoteMediaRedownloadRetryLimit {
		attempts = remoteMediaRedownloadRetryLimit
	}
	return time.Duration(attempts*attempts*30) * time.Second
}

func (s *Server) runRemoteMediaRedownloadWorker(ctx context.Context) {
	s.processDueRemoteMediaRedownloadRetries(ctx, 25)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processDueRemoteMediaRedownloadRetries(ctx, 25)
		}
	}
}

func (s *Server) processDueRemoteMediaRedownloadRetries(ctx context.Context, limit int) {
	if s == nil || s.db == nil || limit <= 0 {
		return
	}
	key := redisConfig(s.cfg).prefix + remoteMediaRedownloadRetryKey
	now := time.Now().UTC()
	claims, err := s.claimRedisRetryJobs(ctx, key, limit, now)
	if err != nil {
		return
	}
	for _, claim := range claims {
		var job remoteMediaRedownloadRetryJob
		if err := json.Unmarshal([]byte(claim.Member), &job); err != nil {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		if err := s.redownloadRemoteMediaAttachment(ctx, job.MediaAttachmentID); err == nil || job.Attempts >= remoteMediaRedownloadRetryLimit {
			_ = s.acknowledgeRedisRetryJob(ctx, key, claim)
			continue
		}
		successor, runAt, err := nextRemoteMediaRedownloadRetry(job, now)
		if err != nil {
			continue
		}
		_ = s.replaceRedisRetryJob(ctx, key, claim, successor, runAt)
	}
}

func (s *Server) performRemoteMediaRedownloadRetry(ctx context.Context, job remoteMediaRedownloadRetryJob) {
	if err := s.redownloadRemoteMediaAttachment(ctx, job.MediaAttachmentID); err == nil {
		return
	}
	if job.Attempts >= remoteMediaRedownloadRetryLimit {
		return
	}
	_ = s.enqueueRemoteMediaRedownloadRetryJob(ctx, job)
}

func (s *Server) redownloadRemoteMediaAttachment(ctx context.Context, mediaAttachmentID int64) error {
	if s == nil || s.db == nil || mediaAttachmentID == 0 {
		return nil
	}
	var media models.MediaAttachment
	if err := s.db.WithContext(ctx).Where("id = ?", mediaAttachmentID).First(&media).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if media.RemoteURL == "" || s.cfg.DisableRemoteMediaCache {
		return nil
	}
	_, err := s.cacheRemoteMediaAttachmentConfiguredResult(s.db.WithContext(ctx), &media, time.Now().UTC())
	if remoteMediaHTTPErrorUnsalvageable(err) {
		return nil
	}
	return err
}

func remoteMediaHTTPErrorUnsalvageable(err error) bool {
	status, ok := activityFetchStatus(err)
	return ok && activityPubDeliveryResponseErrorUnsalvageable(status)
}

func remoteMediaRedownloadDelay() time.Duration {
	return time.Duration(30+rand.Int63n(571)) * time.Second
}

func remoteAccountMediaRedownloadDelay() time.Duration {
	return time.Duration(30+rand.Int63n(571)) * time.Second
}
