package api

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const (
	asynqTaskFASPBackfill         = "fasp:backfill"
	asynqTaskFASPAccountSearch    = "fasp:account_search"
	asynqTaskFASPFollowRecommend  = "fasp:follow_recommendation"
	asynqTaskFASPAccountLifecycle = "fasp:account_lifecycle"
	asynqTaskFASPContentLifecycle = "fasp:content_lifecycle"
	asynqTaskFASPTrend            = "fasp:trend"
)

type asynqFASPBackfillPayload struct {
	BackfillRequestID int64 `json:"backfill_request_id"`
}

type asynqFASPAccountSearchPayload struct {
	Query string `json:"query"`
}

type asynqFASPEventPayload struct {
	URI       string `json:"uri"`
	EventType string `json:"event_type"`
}

type asynqFASPTrendPayload struct {
	StatusID int64  `json:"status_id"`
	Source   string `json:"source"`
}

func (s *Server) enqueueFASPTask(ctx context.Context, taskType string, payload any, retries int, unique time.Duration) error {
	if !s.faspEnabled() {
		return errFASPDisabled
	}
	if s.asynqClient == nil {
		return fmt.Errorf("FASP queue is unavailable")
	}
	encoded, err := marshalAsynqTaskPayload(payload)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	enqueueCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	options := []asynq.Option{asynq.Queue(s.asynqQueue(asynqQueueFASP)), asynq.MaxRetry(retries)}
	if unique > 0 {
		options = append(options, asynq.Unique(unique))
	}
	_, err = s.asynqClient.EnqueueContext(enqueueCtx, asynq.NewTask(taskType, encoded), options...)
	if asynqEnqueueAccepted(err) {
		return nil
	}
	return err
}

func (s *Server) enqueueFASPBackfill(ctx context.Context, requestID int64) error {
	if requestID <= 0 {
		return fmt.Errorf("invalid FASP backfill request")
	}
	return s.enqueueFASPTask(ctx, asynqTaskFASPBackfill, asynqFASPBackfillPayload{BackfillRequestID: requestID}, 5, 0)
}

func (s *Server) enqueueFASPAccountSearch(ctx context.Context, query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	return s.enqueueFASPTask(ctx, asynqTaskFASPAccountSearch, asynqFASPAccountSearchPayload{Query: query}, 0, time.Minute)
}

func (s *Server) enqueueFASPFollowRecommendation(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return nil
	}
	return s.enqueueFASPTask(ctx, asynqTaskFASPFollowRecommend, asynqAccountPayload{AccountID: accountID}, 0, time.Minute)
}

func (s *Server) enqueueFASPAccountLifecycle(ctx context.Context, account models.Account, eventType string) error {
	return s.enqueueFASPAccountLifecycleEligible(ctx, account, eventType, account.Discoverable.Valid && account.Discoverable.Bool)
}

func (s *Server) enqueueFASPAccountLifecycleByID(ctx context.Context, accountID int64, eventType string) error {
	if !s.faspEnabled() || s.db == nil || accountID <= 0 {
		return nil
	}
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", accountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	return s.enqueueFASPAccountLifecycle(ctx, account, eventType)
}

func (s *Server) enqueueFASPAccountLifecycleUpdate(ctx context.Context, previous models.Account, current models.Account) error {
	return s.enqueueFASPAccountLifecycleEligible(ctx, current, "update", faspAccountLifecycleUpdateEligible(previous, current))
}

func faspAccountLifecycleUpdateEligible(previous models.Account, current models.Account) bool {
	isDiscoverable := current.Discoverable.Valid && current.Discoverable.Bool
	changed := previous.Discoverable.Valid != current.Discoverable.Valid || previous.Discoverable.Bool != current.Discoverable.Bool
	return isDiscoverable || changed
}

func (s *Server) enqueueFASPAccountLifecycleEligible(ctx context.Context, account models.Account, eventType string, eligible bool) error {
	if !s.faspEnabled() || !eligible {
		return nil
	}
	if eventType != "new" && eventType != "update" && eventType != "delete" {
		return nil
	}
	uri := activityPubAccountTagManagerURI(s, account)
	if uri == "" {
		return nil
	}
	return s.enqueueFASPTask(ctx, asynqTaskFASPAccountLifecycle, asynqFASPEventPayload{URI: uri, EventType: eventType}, 5, 0)
}

func (s *Server) enqueueFASPContentLifecycle(ctx context.Context, status models.Status, eventType string) error {
	if !s.faspEnabled() || status.Visibility != 0 {
		return nil
	}
	if status.Account.ID == 0 && s.db != nil && status.AccountID > 0 {
		_ = s.db.WithContext(ctx).Where("id = ?", status.AccountID).First(&status.Account).Error
	}
	if !status.Account.Indexable {
		return nil
	}
	if eventType != "new" && eventType != "update" && eventType != "delete" {
		return nil
	}
	uri := activityPubStatusURI(s, status)
	if uri == "" {
		return nil
	}
	return s.enqueueFASPTask(ctx, asynqTaskFASPContentLifecycle, asynqFASPEventPayload{URI: uri, EventType: eventType}, 5, 0)
}

func (s *Server) faspStatusesForDeletion(ctx context.Context, database *gorm.DB, statusIDQueries ...*gorm.DB) ([]models.Status, error) {
	if !s.faspEnabled() || database == nil {
		return nil, nil
	}
	statuses := make([]models.Status, 0)
	seen := make(map[int64]struct{})
	for _, statusIDs := range statusIDQueries {
		if statusIDs == nil {
			continue
		}
		var batch []models.Status
		if err := database.WithContext(ctx).Preload("Account").Where("id IN (?)", statusIDs).Find(&batch).Error; err != nil {
			return nil, err
		}
		for _, status := range batch {
			if _, ok := seen[status.ID]; ok {
				continue
			}
			seen[status.ID] = struct{}{}
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

func (s *Server) enqueueFASPContentDeletionForIDs(ctx context.Context, database *gorm.DB, ids []int64) {
	if !s.faspEnabled() || database == nil || len(ids) == 0 {
		return
	}
	statusIDs := database.Model(&models.Status{}).Select("id").Where("id IN ?", ids)
	var statuses []models.Status
	if err := database.WithContext(ctx).
		Preload("Account").
		Where("id IN (?) OR reblog_of_id IN (?)", statusIDs, statusIDs).
		Find(&statuses).Error; err != nil {
		return
	}
	for _, status := range statuses {
		_ = s.enqueueFASPContentLifecycle(ctx, status, "delete")
	}
}

func (s *Server) enqueueFASPTrend(ctx context.Context, statusID int64, source string) error {
	if !s.faspEnabled() || statusID <= 0 || (source != "favourite" && source != "reblog" && source != "reply") {
		return nil
	}
	return s.enqueueFASPTask(ctx, asynqTaskFASPTrend, asynqFASPTrendPayload{StatusID: statusID, Source: source}, 5, 0)
}

func (s *Server) enqueueFASPTrendForStatus(ctx context.Context, status models.Status, source string) error {
	if !s.faspEnabled() || (source != "reblog" && source != "reply") {
		return nil
	}
	if status.Account.ID == 0 && s.db != nil && status.AccountID > 0 {
		_ = s.db.WithContext(ctx).Where("id = ?", status.AccountID).First(&status.Account).Error
	}
	if !status.Account.Indexable {
		return nil
	}
	candidateID := int64(0)
	if source == "reblog" && status.ReblogOfID.Valid {
		candidateID = status.ReblogOfID.Int64
	} else if source == "reply" && status.InReplyToID.Valid {
		candidateID = status.InReplyToID.Int64
	}
	return s.enqueueFASPTrend(ctx, candidateID, source)
}

func (s *Server) registerFASPAsynqHandlers(mux *asynq.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc(asynqTaskFASPBackfill, s.handleAsynqFASPBackfill)
	mux.HandleFunc(asynqTaskFASPAccountSearch, s.handleAsynqFASPAccountSearch)
	mux.HandleFunc(asynqTaskFASPFollowRecommend, s.handleAsynqFASPFollowRecommendation)
	mux.HandleFunc(asynqTaskFASPAccountLifecycle, s.handleAsynqFASPAccountLifecycle)
	mux.HandleFunc(asynqTaskFASPContentLifecycle, s.handleAsynqFASPContentLifecycle)
	mux.HandleFunc(asynqTaskFASPTrend, s.handleAsynqFASPTrend)
}

func (s *Server) handleAsynqFASPBackfill(ctx context.Context, task *asynq.Task) error {
	if !s.faspEnabled() || s.db == nil {
		return nil
	}
	var payload asynqFASPBackfillPayload
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.BackfillRequestID <= 0 {
		return fmt.Errorf("invalid FASP backfill payload: %w", asynq.SkipRetry)
	}
	var request models.FaspBackfillRequest
	err := s.db.WithContext(ctx).Preload("FaspProvider").Where("id = ?", payload.BackfillRequestID).First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if request.Fulfilled || !request.FaspProvider.Confirmed {
		return nil
	}
	uris, more, cursor, err := s.faspBackfillBatch(ctx, request)
	if err != nil {
		return err
	}
	body := map[string]any{
		"source":               map[string]any{"backfillRequest": map[string]string{"id": strconv.FormatInt(request.ID, 10)}},
		"category":             request.Category,
		"objectUris":           uris,
		"moreObjectsAvailable": more,
	}
	if _, err := s.faspRequest(ctx, request.FaspProvider, http.MethodPost, "/data_sharing/v0/announcements", body); err != nil {
		return err
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if more {
		updates["cursor"] = sql.NullString{String: strconv.FormatInt(cursor, 10), Valid: true}
	} else {
		updates["fulfilled"] = true
	}
	return s.db.WithContext(ctx).Model(&models.FaspBackfillRequest{}).Where("id = ?", request.ID).Updates(updates).Error
}

func (s *Server) faspBackfillBatch(ctx context.Context, request models.FaspBackfillRequest) ([]string, bool, int64, error) {
	limitValue := request.MaxCount
	if limitValue <= 0 {
		limitValue = 100
	}
	if limitValue > 10_000 {
		limitValue = 10_000
	}
	cursor, _ := strconv.ParseInt(strings.TrimSpace(request.Cursor.String), 10, 64)
	switch request.Category {
	case "account":
		query := s.db.WithContext(ctx).Model(&models.Account{}).
			Select("accounts.*").
			Joins("JOIN account_stats ON account_stats.account_id = accounts.id").
			Joins("LEFT JOIN users fasp_backfill_users ON fasp_backfill_users.account_id = accounts.id").
			Where("accounts.discoverable = ?", true).
			Where("accounts.suspended_at IS NULL").
			Where("accounts.silenced_at IS NULL").
			Where("accounts.moved_to_account_id IS NULL").
			Where("(accounts.domain IS NOT NULL OR (fasp_backfill_users.approved = ? AND fasp_backfill_users.confirmed_at IS NOT NULL))", true).
			Where("accounts.id <> ?", int64(-99))
		if cursor > 0 {
			query = query.Where("accounts.id < ?", cursor)
		}
		var accounts []models.Account
		if err := query.Order("accounts.id DESC").Limit(limitValue + 1).Find(&accounts).Error; err != nil {
			return nil, false, 0, err
		}
		more := len(accounts) > limitValue
		if more {
			accounts = accounts[:limitValue]
		}
		uris := make([]string, 0, len(accounts))
		var lastID int64
		for _, account := range accounts {
			if uri := activityPubAccountTagManagerURI(s, account); uri != "" {
				uris = append(uris, uri)
			}
			lastID = account.ID
		}
		return uris, more, lastID, nil
	case "content":
		query := s.db.WithContext(ctx).Model(&models.Status{}).
			Select("statuses.*").
			Joins("JOIN accounts ON accounts.id = statuses.account_id").
			Where("statuses.deleted_at IS NULL").
			Where("statuses.visibility = ?", 0).
			Where("statuses.reblog_of_id IS NULL").
			Where("accounts.indexable = ?", true)
		if cursor > 0 {
			query = query.Where("statuses.id < ?", cursor)
		}
		var statuses []models.Status
		if err := query.Order("statuses.id DESC").Limit(limitValue + 1).Find(&statuses).Error; err != nil {
			return nil, false, 0, err
		}
		more := len(statuses) > limitValue
		if more {
			statuses = statuses[:limitValue]
		}
		uris := make([]string, 0, len(statuses))
		var lastID int64
		for _, status := range statuses {
			if uri := activityPubStatusURI(s, status); uri != "" {
				uris = append(uris, uri)
			}
			lastID = status.ID
		}
		return uris, more, lastID, nil
	default:
		return nil, false, 0, fmt.Errorf("invalid FASP backfill category: %w", asynq.SkipRetry)
	}
}

func (s *Server) handleAsynqFASPAccountSearch(ctx context.Context, task *asynq.Task) error {
	if !s.faspEnabled() || s.db == nil {
		return nil
	}
	var payload asynqFASPAccountSearchPayload
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || strings.TrimSpace(payload.Query) == "" {
		return fmt.Errorf("invalid FASP account search payload: %w", asynq.SkipRetry)
	}
	query := strings.TrimSpace(payload.Query)
	refreshKey := faspAccountSearchRefreshKey(query)
	defer func() { _ = s.finishAsyncRefresh(context.Background(), refreshKey) }()
	providers, err := s.confirmedFASPProvidersWithCapability("account_search")
	if err != nil {
		return err
	}
	for _, provider := range providers {
		endpoint := "/account_search/v0/search?" + url.Values{"term": []string{query}, "limit": []string{"10"}}.Encode()
		body, err := s.faspRequest(ctx, provider, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		uris, err := faspURIList(body, 100)
		if err != nil {
			return err
		}
		for _, uri := range uris {
			created, err := s.faspFetchUnknownAccountURI(uri)
			if err != nil {
				return err
			}
			if created {
				_ = s.incrementAsyncRefreshResult(ctx, refreshKey, 1)
			}
		}
	}
	return nil
}

func (s *Server) handleAsynqFASPFollowRecommendation(ctx context.Context, task *asynq.Task) error {
	if !s.faspEnabled() || s.db == nil {
		return nil
	}
	var payload asynqAccountPayload
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.AccountID <= 0 {
		return fmt.Errorf("invalid FASP follow recommendation payload: %w", asynq.SkipRetry)
	}
	refreshKey := faspFollowRecommendationRefreshKey(payload.AccountID)
	defer func() { _ = s.finishAsyncRefresh(context.Background(), refreshKey) }()
	var account models.Account
	if err := s.db.WithContext(ctx).Where("id = ?", payload.AccountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	providers, err := s.confirmedFASPProvidersWithCapability("follow_recommendation")
	if err != nil {
		return err
	}
	accountURI := activityPubAccountTagManagerURI(s, account)
	for _, provider := range providers {
		endpoint := "/follow_recommendation/v0/accounts?" + url.Values{"accountUri": []string{accountURI}}.Encode()
		body, err := s.faspRequest(ctx, provider, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		uris, err := faspURIList(body, 100)
		if err != nil {
			return err
		}
		for _, uri := range uris {
			recommended, created, err := s.faspResolveAccountURI(uri)
			if err != nil {
				return err
			}
			if !created || recommended == nil || recommended.ID == 0 || recommended.ID == account.ID {
				continue
			}
			now := time.Now().UTC()
			result := s.db.WithContext(ctx).Where("requesting_account_id = ? AND recommended_account_id = ?", account.ID, recommended.ID).
				FirstOrCreate(&models.FaspFollowRecommendation{}, models.FaspFollowRecommendation{RequestingAccountID: account.ID, RecommendedAccountID: recommended.ID, CreatedAt: now, UpdatedAt: now})
			if result.Error == nil && result.RowsAffected > 0 {
				_ = s.incrementAsyncRefreshResult(ctx, refreshKey, 1)
			}
		}
	}
	s.invalidateSuggestionCache(ctx, account.ID)
	return nil
}

func faspURIList(body []byte, max int) ([]string, error) {
	var raw []string
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("invalid FASP URI response: %w", err)
	}
	if max <= 0 {
		max = 100
	}
	out := make([]string, 0, min(len(raw), max))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || !activityFetchHostAllowed(parsed.Hostname()) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

func (s *Server) faspFetchUnknownAccountURI(uri string) (bool, error) {
	_, created, err := s.faspResolveAccountURI(uri)
	return created, err
}

func (s *Server) faspResolveAccountURI(uri string) (*models.Account, bool, error) {
	if s.db == nil {
		return nil, false, nil
	}
	var known models.Account
	err := s.db.Where("uri = ?", uri).First(&known).Error
	if err == nil {
		return &known, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	account, err := s.resolveSearchRemoteAccountURL(uri)
	if err != nil || account == nil {
		return nil, false, err
	}
	return account, true, nil
}

func (s *Server) handleAsynqFASPAccountLifecycle(ctx context.Context, task *asynq.Task) error {
	return s.handleAsynqFASPLifecycle(ctx, task, "account")
}

func (s *Server) handleAsynqFASPContentLifecycle(ctx context.Context, task *asynq.Task) error {
	return s.handleAsynqFASPLifecycle(ctx, task, "content")
}

func (s *Server) handleAsynqFASPLifecycle(ctx context.Context, task *asynq.Task, category string) error {
	if !s.faspEnabled() || s.db == nil {
		return nil
	}
	var payload asynqFASPEventPayload
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || strings.TrimSpace(payload.URI) == "" || (payload.EventType != "new" && payload.EventType != "update" && payload.EventType != "delete") {
		return fmt.Errorf("invalid FASP lifecycle payload: %w", asynq.SkipRetry)
	}
	var subscriptions []models.FaspSubscription
	if err := s.db.WithContext(ctx).Preload("FaspProvider").Where("category = ? AND subscription_type = ?", category, "lifecycle").Order("id ASC").Find(&subscriptions).Error; err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		if !subscription.FaspProvider.Confirmed {
			continue
		}
		body := map[string]any{
			"source":     map[string]any{"subscription": map[string]string{"id": strconv.FormatInt(subscription.ID, 10)}},
			"category":   category,
			"eventType":  payload.EventType,
			"objectUris": []string{payload.URI},
		}
		if _, err := s.faspRequest(ctx, subscription.FaspProvider, http.MethodPost, "/data_sharing/v0/announcements", body); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleAsynqFASPTrend(ctx context.Context, task *asynq.Task) error {
	if !s.faspEnabled() || s.db == nil {
		return nil
	}
	var payload asynqFASPTrendPayload
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.StatusID <= 0 {
		return fmt.Errorf("invalid FASP trend payload: %w", asynq.SkipRetry)
	}
	var status models.Status
	if err := s.db.WithContext(ctx).Preload("Account").Where("id = ?", payload.StatusID).First(&status).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if !status.Account.Indexable {
		return nil
	}
	var subscriptions []models.FaspSubscription
	if err := s.db.WithContext(ctx).Preload("FaspProvider").Where("category = ? AND subscription_type = ?", "content", "trends").Order("id ASC").Find(&subscriptions).Error; err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		if !subscription.FaspProvider.Confirmed {
			continue
		}
		threshold, timeframe := faspTrendThreshold(subscription, payload.Source)
		if threshold < 0 || timeframe <= 0 {
			continue
		}
		count, err := s.faspTrendCount(ctx, status.ID, payload.Source, time.Now().UTC().Add(-time.Duration(timeframe)*time.Minute))
		if err != nil {
			return err
		}
		if count < threshold {
			continue
		}
		body := map[string]any{
			"source":     map[string]any{"subscription": map[string]string{"id": strconv.FormatInt(subscription.ID, 10)}},
			"category":   "content",
			"eventType":  "trending",
			"objectUris": []string{activityPubStatusURI(s, status)},
		}
		if _, err := s.faspRequest(ctx, subscription.FaspProvider, http.MethodPost, "/data_sharing/v0/announcements", body); err != nil {
			return err
		}
	}
	return nil
}

func faspTrendThreshold(subscription models.FaspSubscription, source string) (int64, int64) {
	timeframe := int64(15)
	if subscription.ThresholdTimeframe.Valid {
		timeframe = subscription.ThresholdTimeframe.Int64
	}
	threshold := int64(3)
	switch source {
	case "favourite":
		if subscription.ThresholdLikes.Valid {
			threshold = subscription.ThresholdLikes.Int64
		}
	case "reblog":
		if subscription.ThresholdShares.Valid {
			threshold = subscription.ThresholdShares.Int64
		}
	case "reply":
		if subscription.ThresholdReplies.Valid {
			threshold = subscription.ThresholdReplies.Int64
		}
	default:
		return -1, timeframe
	}
	return threshold, timeframe
}

func (s *Server) faspTrendCount(ctx context.Context, statusID int64, source string, since time.Time) (int64, error) {
	var count int64
	var query *gorm.DB
	switch source {
	case "favourite":
		query = s.db.WithContext(ctx).Model(&models.Favourite{}).Where("status_id = ? AND created_at >= ?", statusID, since)
	case "reblog":
		query = s.db.WithContext(ctx).Model(&models.Status{}).Where("reblog_of_id = ? AND created_at >= ? AND deleted_at IS NULL", statusID, since)
	case "reply":
		query = s.db.WithContext(ctx).Model(&models.Status{}).Where("in_reply_to_id = ? AND created_at >= ? AND deleted_at IS NULL", statusID, since)
	default:
		return 0, nil
	}
	return count, query.Count(&count).Error
}

func faspAccountSearchRefreshKey(query string) string {
	return "fasp:account_search:" + faspStableRefreshDigest(query)
}

func faspStableRefreshDigest(value string) string {
	// #nosec G401 -- MD5 is part of Mastodon's non-security AsyncRefresh cache key contract.
	digest := md5.Sum([]byte(value))
	return base64.StdEncoding.EncodeToString(digest[:])
}

func faspFollowRecommendationRefreshKey(accountID int64) string {
	return "fasp:follow_recommendation:" + strconv.FormatInt(accountID, 10)
}

func (s *Server) runFASPFollowRecommendationCleanupWorker(ctx context.Context) {
	if !s.faspEnabled() {
		return
	}
	run := func() {
		s.runSchedulerWithRedisLock(ctx, "fasp_follow_recommendation_cleanup_scheduler", 24*time.Hour, func() {
			if s.db != nil {
				_ = s.db.WithContext(ctx).Where("created_at < ?", time.Now().UTC().Add(-24*time.Hour)).Delete(&models.FaspFollowRecommendation{}).Error
			}
		})
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
