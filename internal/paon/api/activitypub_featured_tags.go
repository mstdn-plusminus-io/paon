package api

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type activityPubFeaturedTagName struct {
	Normalized string
	Display    string
}

func (s *Server) syncActivityPubFeaturedTagsBestEffort(account *models.Account, collectionURI string) {
	if s == nil || s.db == nil || account == nil || account.ID == 0 || account.Local() || account.SuspendedAt.Valid || strings.TrimSpace(collectionURI) == "" {
		return
	}
	if s.enqueueFeaturedTagsSyncTask(account.ID, collectionURI) {
		return
	}
	go func() {
		_ = s.syncActivityPubFeaturedTagsNow(account, collectionURI)
	}()
}

func (s *Server) syncActivityPubFeaturedTagsNow(account *models.Account, collectionURI string) error {
	return s.syncActivityPubFeaturedTagsNowWithContext(context.Background(), account, collectionURI)
}

func (s *Server) syncActivityPubFeaturedTagsNowWithContext(ctx context.Context, account *models.Account, collectionURI string) error {
	if s == nil || s.db == nil || account == nil || account.ID == 0 || account.Local() || account.SuspendedAt.Valid || strings.TrimSpace(collectionURI) == "" {
		return nil
	}
	signer, err := s.activityPubFeaturedCollectionFetchSigner(account)
	if err != nil {
		return err
	}
	names, err := s.fetchActivityPubFeaturedTagNamesWithSignerAndContext(ctx, account.URI, collectionURI, paonUserAgent(s.cfg), signer)
	if err != nil {
		return err
	}
	return s.syncRemoteFeaturedTags(account, names)
}

func fetchActivityPubFeaturedTagNames(actorURI string, collectionURI string, userAgent string) ([]activityPubFeaturedTagName, error) {
	return fetchActivityPubFeaturedTagNamesWithFetcher(actorURI, collectionURI, userAgent, fetchActivityResourceWithMetadataAndUserAgent)
}

func (s *Server) fetchActivityPubFeaturedTagNamesWithSigner(actorURI string, collectionURI string, userAgent string, signer *models.Account) ([]activityPubFeaturedTagName, error) {
	return s.fetchActivityPubFeaturedTagNamesWithSignerAndContext(context.Background(), actorURI, collectionURI, userAgent, signer)
}

func (s *Server) fetchActivityPubFeaturedTagNamesWithSignerAndContext(ctx context.Context, actorURI string, collectionURI string, userAgent string, signer *models.Account) ([]activityPubFeaturedTagName, error) {
	fetcher := func(uri string, userAgent string) (fetchedActivityResource, error) {
		return fetchActivityResourceWithMetadataAndUserAgentSignedWithAcceptAndContext(ctx, uri, userAgent, s, signer, activityResourceAcceptHeader)
	}
	return fetchActivityPubFeaturedTagNamesWithFetcher(actorURI, collectionURI, userAgent, fetcher)
}

func fetchActivityPubFeaturedTagNamesWithFetcher(actorURI string, collectionURI string, userAgent string, fetcher activityResourceFetcher) ([]activityPubFeaturedTagName, error) {
	if strings.TrimSpace(collectionURI) == "" {
		return nil, nil
	}
	collection, err := fetchActivityCollectionWithFetcher(collectionURI, userAgent, fetcher)
	if err != nil {
		return nil, err
	}
	if collection.FirstCollection != nil {
		collection = *collection.FirstCollection
	} else if collection.First != "" && activityURIHostsMatch(actorURI, collection.First) {
		first, err := fetchActivityCollectionWithoutContextWithFetcher(collection.First, userAgent, fetcher)
		if err != nil {
			return nil, err
		}
		collection = first
	} else if collection.FirstPresent {
		return nil, nil
	}
	out := make([]activityPubFeaturedTagName, 0, featuredTagLimit)
	indexByName := map[string]int{}
	seenPages := map[string]struct{}{collectionURI: {}}
	for {
		out = appendActivityPubFeaturedTagNames(out, indexByName, collection.Tags)
		if len(out) >= featuredTagLimit {
			return out, nil
		}
		if collection.NextCollection != nil {
			collection = *collection.NextCollection
			continue
		}
		if collection.NextPresent && collection.Next == "" {
			return out, nil
		}
		next := collection.Next
		if strings.TrimSpace(next) == "" || !activityURIHostsMatch(actorURI, next) {
			return out, nil
		}
		if _, ok := seenPages[next]; ok {
			return out, nil
		}
		seenPages[next] = struct{}{}
		collection, err = fetchActivityCollectionWithoutContextWithFetcher(next, userAgent, fetcher)
		if err != nil {
			return out, err
		}
	}
}

func activityPubFeaturedTagNamesFromTags(tags []activityTag) []activityPubFeaturedTagName {
	return appendActivityPubFeaturedCollectionTagNames(nil, map[string]int{}, tags)
}

func appendActivityPubFeaturedTagNames(out []activityPubFeaturedTagName, indexByName map[string]int, tags []activityTag) []activityPubFeaturedTagName {
	for _, tag := range tags {
		if len(out) >= featuredTagLimit {
			return out
		}
		if tag.Type != "Hashtag" {
			continue
		}
		if !tag.NameSet {
			continue
		}
		name, ok := activityPubFeaturedTagNameFromActivityName(tag.Name)
		if !ok {
			continue
		}
		normalized := name.Normalized
		display := name.Display
		if index, ok := indexByName[normalized]; ok {
			out[index].Display = display
			continue
		}
		indexByName[normalized] = len(out)
		out = append(out, name)
	}
	return out
}

func appendActivityPubFeaturedCollectionTagNames(out []activityPubFeaturedTagName, indexByName map[string]int, tags []activityTag) []activityPubFeaturedTagName {
	for _, tag := range tags {
		if len(out) >= featuredTagLimit {
			return out
		}
		if tag.Type != "Hashtag" || !tag.NameSet {
			continue
		}
		name := activityPubFeaturedCollectionTagNameFromActivityName(tag.Name)
		if index, ok := indexByName[name.Normalized]; ok {
			out[index].Display = name.Display
			continue
		}
		indexByName[name.Normalized] = len(out)
		out = append(out, name)
	}
	return out
}

func activityPubFeaturedTagNameFromActivityName(raw string) (activityPubFeaturedTagName, bool) {
	name := strings.TrimPrefix(raw, "#")
	return activityPubFeaturedTagName{
		Normalized: activityPubFeaturedTagNormalize(name),
		Display:    strings.TrimPrefix(strings.TrimSpace(name), "#"),
	}, true
}

func activityPubFeaturedCollectionTagNameFromActivityName(raw string) activityPubFeaturedTagName {
	normalized := activityPubFeaturedTagNormalize(strings.TrimPrefix(raw, "#"))
	return activityPubFeaturedTagName{Normalized: normalized, Display: normalized}
}

func activityPubFeaturedTagNormalize(raw string) string {
	normalized := railsNormalizeHashtagName(raw)
	var out strings.Builder
	for _, r := range normalized {
		if unicodeIsHashtagNameRune(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func unicodeIsHashtagNameRune(r rune) bool {
	return validTagName.MatchString(string(r))
}

func (s *Server) syncRemoteFeaturedTags(account *models.Account, names []activityPubFeaturedTagName) error {
	if s == nil || s.db == nil || account == nil || account.ID == 0 {
		return nil
	}
	desired := make(map[int64]activityPubFeaturedTagName, len(names))
	tagIDs := make([]int64, 0, len(names))
	for _, name := range names {
		tagName := firstNonEmpty(name.Display, name.Normalized)
		tag, err := s.findOrCreateTag(tagName)
		if err != nil {
			return err
		}
		if _, ok := desired[tag.ID]; ok {
			continue
		}
		desired[tag.ID] = name
		tagIDs = append(tagIDs, tag.ID)
	}
	now := time.Now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		deleteQuery := tx.Where("account_id = ?", account.ID)
		if len(tagIDs) > 0 {
			deleteQuery = deleteQuery.Where("tag_id NOT IN ?", tagIDs)
		}
		if err := deleteQuery.Delete(&models.FeaturedTag{}).Error; err != nil {
			return err
		}
		for _, tagID := range tagIDs {
			name := desired[tagID]
			stats, err := featuredStats(tx, account.ID, tagID)
			if err != nil {
				return err
			}
			featured := models.FeaturedTag{
				AccountID:     account.ID,
				TagID:         tagID,
				StatusesCount: stats.StatusesCount,
				LastStatusAt:  stats.LastStatusAt,
				Name:          sql.NullString{String: name.Display, Valid: name.Display != ""},
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&featured).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.FeaturedTag{}).
				Where("account_id = ? AND tag_id = ?", account.ID, tagID).
				Updates(map[string]any{
					"name":           sql.NullString{String: name.Display, Valid: name.Display != ""},
					"statuses_count": stats.StatusesCount,
					"last_status_at": stats.LastStatusAt,
					"updated_at":     now,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
