package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const asynqTaskFetchAllReplies = "fetch:all_replies"

const (
	defaultFetchRepliesCooldown    = 15 * time.Minute
	defaultFetchRepliesInitialWait = 5 * time.Minute
	defaultFetchRepliesMaxGlobal   = 1000
	defaultFetchRepliesMaxSingle   = 500
	defaultFetchRepliesMaxPages    = 500
)

type asynqFetchAllRepliesPayload struct {
	RootStatusID    int64  `json:"root_status_id"`
	RequestID       string `json:"request_id,omitempty"`
	AsyncRefreshKey string `json:"async_refresh_key,omitempty"`
}

type fetchRepliesLimits struct {
	Cooldown    time.Duration
	InitialWait time.Duration
	MaxGlobal   int
	MaxSingle   int
	MaxPages    int
}

func (s *Server) fetchRepliesLimits() fetchRepliesLimits {
	limits := fetchRepliesLimits{
		Cooldown:    s.cfg.FetchRepliesCooldown,
		InitialWait: s.cfg.FetchRepliesInitialWait,
		MaxGlobal:   s.cfg.FetchRepliesMaxGlobal,
		MaxSingle:   s.cfg.FetchRepliesMaxSingle,
		MaxPages:    s.cfg.FetchRepliesMaxPages,
	}
	// NewServer validates values loaded from the environment. These fallbacks
	// preserve the 4.4 defaults for focused tests that construct Config values
	// directly and for jobs queued during a rolling binary upgrade.
	if limits.Cooldown <= 0 {
		limits.Cooldown = defaultFetchRepliesCooldown
	}
	if limits.InitialWait <= 0 {
		limits.InitialWait = defaultFetchRepliesInitialWait
	}
	if limits.MaxGlobal <= 0 {
		limits.MaxGlobal = defaultFetchRepliesMaxGlobal
	}
	if limits.MaxSingle <= 0 {
		limits.MaxSingle = defaultFetchRepliesMaxSingle
	}
	if limits.MaxPages <= 0 {
		limits.MaxPages = defaultFetchRepliesMaxPages
	}
	return limits
}

func (s *Server) shouldFetchAllReplies(status models.Status, now time.Time) bool {
	if s == nil || status.ID == 0 || status.Account.Local() || !status.URI.Valid || strings.TrimSpace(status.URI.String) == "" {
		return false
	}
	limits := s.fetchRepliesLimits()
	if status.CreatedAt.After(now.Add(-limits.InitialWait)) {
		return false
	}
	return !status.FetchedRepliesAt.Valid || !status.FetchedRepliesAt.Time.After(now.Add(-limits.Cooldown))
}

// enqueueFetchAllRepliesTask mirrors ActivityPub::FetchAllRepliesWorker. The
// database cooldown claim in the worker is the idempotency boundary when
// concurrent context requests enqueue the same remote status.
func (s *Server) enqueueFetchAllRepliesTask(rootStatusID int64, requestID string, asyncRefreshKeys ...string) bool {
	if s == nil || s.asynqClient == nil || rootStatusID == 0 {
		return false
	}
	asyncRefreshKey := ""
	if len(asyncRefreshKeys) > 0 {
		asyncRefreshKey = strings.TrimSpace(asyncRefreshKeys[0])
	}
	payload, err := marshalAsynqTaskPayload(asynqFetchAllRepliesPayload{RootStatusID: rootStatusID, RequestID: requestID, AsyncRefreshKey: asyncRefreshKey})
	if err != nil {
		return false
	}
	task := asynq.NewTask(asynqTaskFetchAllReplies, payload, asynq.Queue(s.asynqQueue(asynqQueuePull)), asynq.MaxRetry(3))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = s.asynqClient.EnqueueContext(ctx, task)
	return asynqEnqueueAccepted(err)
}

// maybeEnqueueContextReplyFetch schedules recursive fetching only for an
// authenticated context request and only when the status is already eligible.
// The worker repeats and atomically claims this check before performing I/O.
func (s *Server) maybeEnqueueContextReplyFetch(status *models.Status, account *models.Account, requestID string) {
	if status == nil || account == nil || !s.shouldFetchAllReplies(*status, time.Now().UTC()) {
		return
	}
	s.enqueueFetchAllRepliesTask(status.ID, requestID)
}

func (s *Server) claimFetchAllReplies(ctx context.Context, status models.Status, now time.Time) (bool, error) {
	if !s.shouldFetchAllReplies(status, now) {
		return false, nil
	}
	cutoff := now.Add(-s.fetchRepliesLimits().Cooldown)
	result := s.db.WithContext(ctx).
		Model(&models.Status{}).
		Where("id = ? AND (fetched_replies_at IS NULL OR fetched_replies_at <= ?)", status.ID, cutoff).
		Update("fetched_replies_at", now)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (s *Server) handleAsynqFetchAllReplies(ctx context.Context, task *asynq.Task) (resultErr error) {
	var payload asynqFetchAllRepliesPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("fetch all replies: %w", err)
	}
	if s == nil || s.db == nil || payload.RootStatusID == 0 {
		return nil
	}
	if payload.AsyncRefreshKey != "" {
		defer func() {
			if resultErr == nil || asynqRetryExhausted(ctx) {
				_ = s.completeAsyncRefreshJob(context.Background(), payload.AsyncRefreshKey)
			}
		}()
	}

	var root models.Status
	if err := s.db.WithContext(ctx).Preload("Account").Where("id = ?", payload.RootStatusID).First(&root).Error; err != nil {
		return workerLookupError("fetch all replies root lookup", err)
	}
	now := time.Now().UTC()
	claimed, err := s.claimFetchAllReplies(ctx, root, now)
	if err != nil {
		return fmt.Errorf("fetch all replies claim: %w", err)
	}
	if !claimed {
		return nil
	}

	rootURI := strings.TrimSpace(root.URI.String)
	signer := s.activityFetchSigner(nil)
	userAgent := paonUserAgent(s.cfg)
	fetcher := func(uri string, userAgent string) (fetchedActivityResource, error) {
		return fetchActivityResourceWithMetadataAndUserAgentSignedWithAcceptAndContext(ctx, uri, userAgent, s, signer, activityResourceAcceptHeader)
	}
	rootPayload, err := fetchActivityResourcePayloadStrictDepthWithExpectedIDAndFetcher(rootURI, rootURI, 0, userAgent, fetcher)
	if err != nil {
		if err = activityFetchWorkerError(err); err != nil {
			return fmt.Errorf("fetch all replies root: %w", err)
		}
		return nil
	}
	if !activityObjectIsStatus(rootPayload.Object) {
		return nil
	}

	// Mastodon updates the root status from the prefetched representation. Paon
	// keeps FetchReplyWorker as the single persistence path, at the cost of one
	// extra conditional request, so all existing status validation stays shared.
	s.enqueueFetchReplyTask(rootURI, payload.RequestID, payload.AsyncRefreshKey)
	count, pages, err := s.walkActivityPubRepliesForRefresh(ctx, rootURI, rootPayload.Object, payload.RequestID, payload.AsyncRefreshKey, userAgent, fetcher)
	if err != nil {
		return fmt.Errorf("fetch all replies traversal: %w", err)
	}
	log.Printf("level=INFO event=activitypub_fetch_all_replies request_id=%q root_status_id=%d replies=%d pages=%d", payload.RequestID, root.ID, count, pages)
	return nil
}

type activityReplyCandidateFilter func(context.Context, string, []string, int) ([]string, error)
type activityReplyEnqueuer func(string)

func (s *Server) walkActivityPubReplies(ctx context.Context, rootURI string, rootObject activityObject, requestID string, userAgent string, fetcher activityResourceFetcher) (int, int, error) {
	return s.walkActivityPubRepliesForRefresh(ctx, rootURI, rootObject, requestID, "", userAgent, fetcher)
}

func (s *Server) walkActivityPubRepliesForRefresh(ctx context.Context, rootURI string, rootObject activityObject, requestID string, asyncRefreshKey string, userAgent string, fetcher activityResourceFetcher) (int, int, error) {
	limits := s.fetchRepliesLimits()
	filter := func(ctx context.Context, parentURI string, candidates []string, limit int) ([]string, error) {
		return s.filterActivityPubReplyCandidates(ctx, parentURI, candidates, limit, time.Now().UTC())
	}
	enqueue := func(uri string) { s.enqueueFetchReplyTask(uri, requestID, asyncRefreshKey) }
	return walkActivityPubRepliesWithFetcher(ctx, rootURI, rootObject, limits, userAgent, fetcher, filter, enqueue)
}

// walkActivityPubRepliesWithFetcher is the bounded traversal core. Collection
// pages stay on the status' origin while reply objects may live on other
// origins, matching Mastodon's amplification boundary and federation model.
func walkActivityPubRepliesWithFetcher(ctx context.Context, rootURI string, rootObject activityObject, limits fetchRepliesLimits, userAgent string, fetcher activityResourceFetcher, filter activityReplyCandidateFilter, enqueue activityReplyEnqueuer) (int, int, error) {
	seen := map[string]struct{}{rootURI: {}}
	stack := make([]string, 0)
	pagesUsed := 0

	addReplies := func(parentURI string, collection activityCollection) error {
		remainingPages := limits.MaxPages - pagesUsed
		if remainingPages <= 0 || len(seen)-1 >= limits.MaxGlobal {
			return nil
		}
		candidates, pages, err := collectActivityPubReplyCollection(parentURI, collection, remainingPages, limits.MaxSingle, userAgent, fetcher)
		pagesUsed += pages
		if err != nil {
			return err
		}
		if filter != nil {
			candidates, err = filter(ctx, parentURI, candidates, limits.MaxSingle)
			if err != nil {
				return err
			}
		}
		for _, candidate := range candidates {
			candidate = activityPubHTTPURI(candidate)
			if candidate == "" {
				continue
			}
			if _, duplicate := seen[candidate]; duplicate {
				continue
			}
			if len(seen)-1 >= limits.MaxGlobal {
				break
			}
			seen[candidate] = struct{}{}
			stack = append(stack, candidate)
			if enqueue != nil {
				enqueue(candidate)
			}
		}
		return nil
	}

	if err := addReplies(rootURI, rootObject.Replies); err != nil {
		return 0, pagesUsed, err
	}
	for len(stack) > 0 && len(seen)-1 < limits.MaxGlobal && pagesUsed < limits.MaxPages {
		last := len(stack) - 1
		uri := stack[last]
		stack = stack[:last]
		select {
		case <-ctx.Done():
			return len(seen) - 1, pagesUsed, ctx.Err()
		default:
		}
		payload, err := fetchActivityResourcePayloadStrictDepthWithExpectedIDAndFetcher(uri, uri, 0, userAgent, fetcher)
		if err != nil || !activityObjectIsStatus(payload.Object) {
			// A child disappearing or rejecting a request must not discard replies
			// already discovered from sibling branches. Its FetchReplyWorker retains
			// independent retry semantics.
			continue
		}
		if err := addReplies(uri, payload.Object.Replies); err != nil {
			continue
		}
	}
	return len(seen) - 1, pagesUsed, nil
}

func collectActivityPubReplyCollection(referenceURI string, collection activityCollection, maxPages int, maxItems int, userAgent string, fetcher activityResourceFetcher) ([]string, int, error) {
	if maxPages <= 0 || maxItems <= 0 {
		return nil, 0, nil
	}
	current, present, err := firstActivityPubReplyCollectionPage(referenceURI, collection, userAgent, fetcher)
	if err != nil || !present {
		return nil, 0, err
	}
	out := make([]string, 0, min(maxItems, len(current.ItemURIs())))
	seenPages := make(map[string]struct{})
	pages := 0
	for present && pages < maxPages && len(out) < maxItems {
		pages++
		if current.ID != "" {
			seenPages[current.ID] = struct{}{}
		}
		for _, uri := range current.ItemURIs() {
			if uri = activityPubHTTPURI(uri); uri == "" {
				continue
			}
			out = append(out, uri)
			if len(out) >= maxItems {
				break
			}
		}
		if len(out) >= maxItems {
			break
		}
		current, present, err = nextActivityPubReplyCollectionPage(referenceURI, current, userAgent, fetcher, seenPages)
		if err != nil {
			return nil, pages, err
		}
	}
	return out, pages, nil
}

func firstActivityPubReplyCollectionPage(referenceURI string, collection activityCollection, userAgent string, fetcher activityResourceFetcher) (activityCollection, bool, error) {
	if collection.Type == "" && collection.ID != "" && len(collection.ItemURIs()) == 0 && collection.FirstCollection == nil && !collection.FirstPresent {
		fetched, ok, err := fetchActivityPubReplyCollectionPage(referenceURI, collection.ID, userAgent, fetcher)
		if err != nil || !ok {
			return activityCollection{}, ok, err
		}
		collection = fetched
	}
	if collection.FirstCollection != nil {
		return *collection.FirstCollection, true, nil
	}
	if collection.FirstPresent {
		if collection.First == "" {
			return activityCollection{}, false, nil
		}
		return fetchActivityPubReplyCollectionPage(referenceURI, collection.First, userAgent, fetcher)
	}
	if collection.Type == "" && collection.ID == "" && len(collection.ItemURIs()) == 0 {
		return activityCollection{}, false, nil
	}
	return collection, true, nil
}

func nextActivityPubReplyCollectionPage(referenceURI string, collection activityCollection, userAgent string, fetcher activityResourceFetcher, seen map[string]struct{}) (activityCollection, bool, error) {
	if collection.NextCollection != nil {
		return *collection.NextCollection, true, nil
	}
	if !collection.NextPresent || collection.Next == "" {
		return activityCollection{}, false, nil
	}
	if _, duplicate := seen[collection.Next]; duplicate {
		return activityCollection{}, false, nil
	}
	seen[collection.Next] = struct{}{}
	return fetchActivityPubReplyCollectionPage(referenceURI, collection.Next, userAgent, fetcher)
}

func fetchActivityPubReplyCollectionPage(referenceURI string, uri string, userAgent string, fetcher activityResourceFetcher) (activityCollection, bool, error) {
	if !activityURIHostsMatch(referenceURI, uri) {
		return activityCollection{}, false, nil
	}
	collection, err := fetchActivityCollectionWithoutContextWithFetcher(uri, userAgent, fetcher)
	if err != nil {
		return activityCollection{}, false, err
	}
	return collection, true, nil
}

func (s *Server) filterActivityPubReplyCandidates(ctx context.Context, parentURI string, candidates []string, limit int, now time.Time) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	unique := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, uri := range candidates {
		uri = activityPubHTTPURI(uri)
		if uri == "" {
			continue
		}
		if _, duplicate := seen[uri]; duplicate {
			continue
		}
		seen[uri] = struct{}{}
		unique = append(unique, uri)
	}

	// Like FetchAllRepliesService, include known direct replies omitted by the
	// remote collection when Paon cannot rely on delivery from a local follower.
	var parentID int64
	if err := s.db.WithContext(ctx).Model(&models.Status{}).Where("uri = ?", parentURI).Select("id").Scan(&parentID).Error; err != nil {
		return nil, err
	}
	if parentID != 0 {
		var known []models.Status
		query := s.db.WithContext(ctx).
			Model(&models.Status{}).
			Preload("Account").
			Joins("LEFT JOIN follows ON follows.target_account_id = statuses.account_id").
			Joins("LEFT JOIN accounts AS follower_accounts ON follower_accounts.id = follows.account_id").
			Where("statuses.in_reply_to_id = ? AND statuses.uri IS NOT NULL", parentID).
			Where("follower_accounts.domain IS NOT NULL OR follows.created_at >= statuses.updated_at OR follows.id IS NULL").
			Limit(limit)
		if len(unique) > 0 {
			query = query.Where("statuses.uri NOT IN ?", unique)
		}
		if err := query.Find(&known).Error; err != nil {
			return nil, err
		}
		for _, status := range known {
			if status.URI.Valid {
				uri := activityPubHTTPURI(status.URI.String)
				if _, duplicate := seen[uri]; uri != "" && !duplicate {
					seen[uri] = struct{}{}
					unique = append(unique, uri)
				}
			}
		}
	}

	var existing []models.Status
	if len(unique) > 0 {
		if err := s.db.WithContext(ctx).Preload("Account").Where("uri IN ?", unique).Find(&existing).Error; err != nil {
			return nil, err
		}
	}
	existingByURI := make(map[string]models.Status, len(existing))
	for _, status := range existing {
		if status.URI.Valid {
			existingByURI[status.URI.String] = status
		}
	}

	limits := s.fetchRepliesLimits()
	out := make([]string, 0, min(limit, len(unique)))
	touchIDs := make([]int64, 0)
	for _, uri := range unique {
		if status, exists := existingByURI[uri]; exists {
			if status.Account.Local() || status.CreatedAt.After(now.Add(-limits.InitialWait)) || (status.FetchedRepliesAt.Valid && status.FetchedRepliesAt.Time.After(now.Add(-limits.Cooldown))) {
				continue
			}
			touchIDs = append(touchIDs, status.ID)
		}
		out = append(out, uri)
		if len(out) >= limit {
			break
		}
	}
	if len(touchIDs) > 0 {
		if err := s.db.WithContext(ctx).Model(&models.Status{}).Where("id IN ?", touchIDs).Update("fetched_replies_at", now).Error; err != nil {
			return nil, err
		}
	}
	return out, nil
}
