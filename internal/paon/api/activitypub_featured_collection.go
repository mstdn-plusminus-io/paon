package api

import (
	"errors"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) syncActivityPubFeaturedCollectionBestEffort(account *models.Account, collectionURI string, requestID string, syncTags bool) {
	if s == nil || s.db == nil || account == nil || account.ID == 0 || account.Local() || account.SuspendedAt.Valid || strings.TrimSpace(collectionURI) == "" || !activityURIHostsMatch(account.URI, collectionURI) {
		return
	}
	if s.enqueueFeaturedCollectionSyncTask(account.ID, collectionURI, requestID, syncTags) {
		return
	}
	go func() {
		_ = s.syncActivityPubFeaturedCollectionNow(account, collectionURI, requestID, syncTags)
	}()
}

func (s *Server) syncActivityPubFeaturedCollectionNow(account *models.Account, collectionURI string, requestID string, syncTags bool) error {
	if s == nil || s.db == nil || account == nil || account.ID == 0 || account.Local() || account.SuspendedAt.Valid || strings.TrimSpace(collectionURI) == "" || !activityURIHostsMatch(account.URI, collectionURI) {
		return nil
	}
	signer, err := s.activityPubFeaturedCollectionFetchSigner(account)
	if err != nil {
		return err
	}
	collection, err := s.fetchActivityPubFeaturedCollectionWithSigner(account.URI, collectionURI, paonUserAgent(s.cfg), signer)
	if err != nil {
		return err
	}
	if syncTags {
		if err := s.syncRemoteFeaturedTags(account, activityPubFeaturedTagNamesFromTags(collection.Tags)); err != nil {
			return err
		}
	}
	return s.syncRemoteStatusPinsFromActivityCollection(account, collection, requestID)
}

func fetchActivityPubFeaturedCollection(actorURI string, collectionURI string, userAgent string) (activityCollection, error) {
	return fetchActivityPubFeaturedCollectionWithFetcher(actorURI, collectionURI, userAgent, fetchActivityResourceWithMetadataAndUserAgent)
}

func (s *Server) fetchActivityPubFeaturedCollectionWithSigner(actorURI string, collectionURI string, userAgent string, signer *models.Account) (activityCollection, error) {
	fetcher := func(uri string, userAgent string) (fetchedActivityResource, error) {
		return fetchActivityResourceWithMetadataAndUserAgentSigned(uri, userAgent, s, signer)
	}
	return fetchActivityPubFeaturedCollectionWithFetcher(actorURI, collectionURI, userAgent, fetcher)
}

func fetchActivityPubFeaturedCollectionWithFetcher(actorURI string, collectionURI string, userAgent string, fetcher activityResourceFetcher) (activityCollection, error) {
	if strings.TrimSpace(collectionURI) == "" || !activityURIHostsMatch(actorURI, collectionURI) {
		return activityCollection{}, nil
	}
	collection, err := fetchActivityCollectionWithoutContextWithFetcher(collectionURI, userAgent, fetcher)
	if err != nil {
		return activityCollection{}, err
	}
	if collection.FirstCollection != nil {
		return *collection.FirstCollection, nil
	}
	if collection.First != "" && activityURIHostsMatch(actorURI, collection.First) {
		return fetchActivityCollectionWithoutContextWithFetcher(collection.First, userAgent, fetcher)
	}
	if collection.FirstPresent {
		return activityCollection{}, nil
	}
	return collection, nil
}

func (s *Server) syncRemoteStatusPinsFromActivityCollection(account *models.Account, collection activityCollection, requestID string) error {
	uris := activityPubFeaturedStatusURIs(account.URI, collection)
	statusIDs := make([]int64, 0, len(uris))
	signer, err := s.activityPubFeaturedCollectionFetchSigner(account)
	if err != nil {
		return err
	}
	for _, uri := range uris {
		if s.localActivityURI(uri) {
			continue
		}
		status, err := s.statusFromActivityURI(uri)
		if err != nil {
			return err
		}
		if status == nil {
			status, err = s.fetchRemoteStatusFromActivityURIForRequestWithSigner(uri, account.URI, requestID, signer)
			if err != nil {
				return err
			}
		}
		if status == nil || status.AccountID != account.ID {
			continue
		}
		statusIDs = append(statusIDs, status.ID)
	}
	now := time.Now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		deleteQuery := tx.Where("account_id = ?", account.ID)
		if len(statusIDs) > 0 {
			deleteQuery = deleteQuery.Where("status_id NOT IN ?", statusIDs)
		}
		if err := deleteQuery.Delete(&models.StatusPin{}).Error; err != nil {
			return err
		}
		for _, statusID := range statusIDs {
			pin := models.StatusPin{AccountID: account.ID, StatusID: statusID, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&pin).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Server) activityPubFeaturedCollectionFetchSigner(actor *models.Account) (*models.Account, error) {
	signer, err := s.firstActivityPubUnsuspendedLocalFollower(actor)
	if err != nil || signer != nil {
		return signer, err
	}
	return s.representativeActivityPubAccount()
}

func (s *Server) firstActivityPubUnsuspendedLocalFollower(actor *models.Account) (*models.Account, error) {
	if s == nil || s.db == nil || actor == nil || actor.ID == 0 {
		return nil, nil
	}
	var account models.Account
	err := s.db.Model(&models.Account{}).
		Select("accounts.*").
		Joins("JOIN follows ON follows.account_id = accounts.id").
		Where("follows.target_account_id = ?", actor.ID).
		Where("accounts.domain IS NULL AND accounts.suspended_at IS NULL AND accounts.private_key IS NOT NULL AND accounts.private_key <> ''").
		Order("follows.id ASC").
		First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &account, err
}

func activityPubFeaturedStatusURIs(actorURI string, collection activityCollection) []string {
	candidates := collection.NoteItemURIs()
	out := make([]string, 0, len(candidates))
	for _, uri := range candidates {
		if strings.TrimSpace(uri) == "" || !activityURIHostsMatch(actorURI, uri) {
			continue
		}
		out = append(out, uri)
	}
	return out
}
