package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) activityPubFollowersSynchronization(c *echo.Context) error {
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return err
	}
	if s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
	c.Response().Header().Set("Cache-Control", "max-age=0, private")
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	signedAccount, err := s.verifyActivityPubSignature(c, body)
	if err != nil {
		return apiError(c, http.StatusUnauthorized, err.Error())
	}
	origin := accountURIOrigin(signedAccount.URI)
	if origin == "" {
		return apiError(c, http.StatusUnauthorized, "signed account has no ActivityPub origin")
	}
	items, err := s.followersSynchronizationItems(account.ID, origin)
	if err != nil {
		return err
	}
	return activityJSON(c, activityPubFollowersSynchronizationObject(s, *account, items))
}

func (s *Server) activityPubClaim(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	setPrivateNoStoreCacheHeaders(c)
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}
	c.Request().Body = io.NopCloser(bytes.NewReader(body))
	if _, err := s.verifyActivityPubSignature(c, body); err != nil {
		return apiError(c, http.StatusUnauthorized, err.Error())
	}
	key, err := s.claimLocalOneTimeKey(account.ID, activityPubClaimDeviceID(c))
	if err != nil {
		return err
	}
	if key == nil {
		return c.JSON(http.StatusOK, nil)
	}
	return c.JSON(http.StatusOK, activityPubOneTimeKeyObject(*key))
}

func (s *Server) claimLocalOneTimeKey(accountID int64, deviceID string) (*models.OneTimeKey, error) {
	if deviceID == "" {
		return nil, nil
	}
	var key models.OneTimeKey
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var device models.Device
		if err := tx.Where("account_id = ? AND device_id = ?", accountID, deviceID).First(&device).Error; err != nil {
			return err
		}
		if err := tx.Where("device_id = ?", device.ID).Order("random()").First(&key).Error; err != nil {
			return err
		}
		return tx.Delete(&key).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &key, nil
}

func activityPubClaimDeviceID(c *echo.Context) string {
	if raw := c.QueryParam("id"); raw != "" {
		return raw
	}
	return c.FormValue("id")
}

func (s *Server) followersSynchronizationItems(accountID int64, origin string) ([]string, error) {
	var rows []models.Account
	like := escapeSQLLike(strings.TrimRight(origin, "/")) + "/%"
	if err := s.db.
		Table("accounts").
		Select("accounts.uri").
		Joins("JOIN follows ON follows.account_id = accounts.id").
		Where("follows.target_account_id = ?", accountID).
		Where(`accounts.uri = ? OR accounts.uri LIKE ? ESCAPE '\'`, origin, like).
		Order("follows.id DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.URI) != "" {
			items = append(items, row.URI)
		}
	}
	return items, nil
}

func escapeSQLLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func activityPubFollowersSynchronizationObject(s *Server, account models.Account, items []string) map[string]any {
	id := activityPubActorURL(s, account) + "/followers_synchronization"
	return map[string]any{
		"@context":     activityPubActivityStreamsContext(),
		"id":           id,
		"type":         "OrderedCollection",
		"orderedItems": items,
	}
}

func activityPubOneTimeKeyObject(key models.OneTimeKey) map[string]any {
	return map[string]any{
		"@context":        activityPubEncryptedMessageContext(),
		"keyId":           key.KeyID,
		"type":            "Curve25519Key",
		"publicKeyBase64": key.Key,
		"signature": map[string]any{
			"type":           "Ed25519Signature",
			"signatureValue": key.Signature,
		},
	}
}

func (s *Server) processActivityPubCollectionSynchronization(c *echo.Context, actor *models.Account) {
	if s == nil || s.db == nil || actor == nil || os.Getenv("DISABLE_FOLLOWERS_SYNCHRONIZATION") == "true" {
		return
	}
	params, ok := parseActivityPubCollectionSynchronizationHeader(c.Request().Header.Get("Collection-Synchronization"))
	if !ok || !s.shouldSynchronizeActivityPubFollowers(*actor, params) {
		return
	}
	urlValue := params["url"]
	if s.enqueueFollowersSynchronizationTask(actor.ID, urlValue) {
		return
	}
	go func() {
		_ = s.synchronizeActivityPubFollowers(context.Background(), *actor, urlValue)
	}()
}

func parseActivityPubCollectionSynchronizationHeader(raw string) (map[string]string, bool) {
	params, err := parseActivitySignatureParams(raw)
	if err != nil {
		return nil, false
	}
	return params, params["collectionId"] != "" && params["digest"] != "" && params["url"] != ""
}

func (s *Server) shouldSynchronizeActivityPubFollowers(account models.Account, params map[string]string) bool {
	if params["collectionId"] == "" || params["url"] == "" {
		return false
	}
	if params["collectionId"] != account.FollowersURL {
		return false
	}
	if activityPubURIHostMismatch(account.URI, params["url"]) {
		return false
	}
	digest, err := s.activityPubLocalFollowersHash(account)
	return err == nil && digest != params["digest"]
}

func activityPubURIHostMismatch(baseURL string, comparisonURL string) bool {
	if !strings.HasPrefix(comparisonURL, "http://") && !strings.HasPrefix(comparisonURL, "https://") {
		return true
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" {
		return true
	}
	comparison, err := url.Parse(comparisonURL)
	if err != nil || comparison.Host == "" {
		return true
	}
	return !strings.EqualFold(base.Hostname(), comparison.Hostname())
}

func (s *Server) activityPubLocalFollowersHash(account models.Account) (string, error) {
	if s == nil || s.db == nil || account.ID == 0 {
		return "", nil
	}
	var followers []models.Account
	if err := s.db.Table("accounts").
		Select("accounts.id, accounts.username, accounts.domain").
		Joins("JOIN follows ON follows.account_id = accounts.id").
		Where("follows.target_account_id = ?", account.ID).
		Where("accounts.domain IS NULL").
		Find(&followers).Error; err != nil {
		return "", err
	}
	digest := make([]byte, sha256.Size)
	for _, follower := range followers {
		sum := sha256.Sum256([]byte(activityPubActorURL(s, follower)))
		for i := range digest {
			digest[i] ^= sum[i]
		}
	}
	return hexLower(digest), nil
}

func hexLower(value []byte) string {
	const table = "0123456789abcdef"
	out := make([]byte, len(value)*2)
	for i, b := range value {
		out[i*2] = table[b>>4]
		out[i*2+1] = table[b&0x0f]
	}
	return string(out)
}

func (s *Server) synchronizeActivityPubFollowers(ctx context.Context, account models.Account, collectionURL string) error {
	expectedIDs := []int64{}
	complete, err := s.processActivityPubFollowersSynchronizationCollection(collectionURL, account.URI, 10, func(items []string) error {
		ids, err := s.localAccountIDsFromActivityURIs(items)
		if err != nil {
			return err
		}
		expectedIDs = append(expectedIDs, ids...)
		return s.applyExpectedActivityPubFollowersSynchronization(ctx, account, ids)
	})
	if err != nil || !complete {
		return err
	}
	return s.removeUnexpectedActivityPubFollowers(ctx, account, expectedIDs)
}

func (s *Server) activityPubFollowersSynchronizationCollectionItems(collectionURL string, actorURI string, maxPages int) ([]string, bool, error) {
	items := []string{}
	complete, err := s.processActivityPubFollowersSynchronizationCollection(collectionURL, actorURI, maxPages, func(pageItems []string) error {
		items = append(items, pageItems...)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !complete {
		return nil, false, nil
	}
	return items, true, nil
}

func (s *Server) processActivityPubFollowersSynchronizationCollection(collectionURL string, actorURI string, maxPages int, processPage func([]string) error) (bool, error) {
	if activityPubURIHostMismatch(actorURI, collectionURL) {
		return false, nil
	}
	body, err := s.fetchActivityPubFollowersSynchronizationResource(collectionURL)
	if err != nil {
		return false, err
	}
	collection, err := activityPubCollectionMap(body)
	if err != nil {
		return false, err
	}
	firstValue := activityJSONLDValue(collection, "first")
	if firstCollection := activityPubCollectionInlineMap(firstValue); firstCollection != nil {
		collection = firstCollection
	} else if first := activityPubCollectionURI(firstValue); first != "" {
		if activityPubURIHostMismatch(actorURI, first) {
			return false, nil
		}
		body, err = s.fetchActivityPubFollowersSynchronizationResource(first)
		if err != nil {
			return false, err
		}
		collection, err = activityPubCollectionMap(body)
		if err != nil {
			return false, err
		}
	}
	for maxPages > 0 {
		if processPage != nil {
			if err := processPage(activityPubCollectionItems(collection)); err != nil {
				return false, err
			}
		}
		maxPages--
		nextValue := activityJSONLDValue(collection, "next")
		if nextCollection := activityPubCollectionInlineMap(nextValue); nextCollection != nil {
			collection = nextCollection
			continue
		}
		next := activityPubCollectionURI(nextValue)
		if next == "" {
			return true, nil
		}
		if maxPages <= 0 {
			return false, nil
		}
		if activityPubURIHostMismatch(actorURI, next) {
			return false, nil
		}
		body, err = s.fetchActivityPubFollowersSynchronizationResource(next)
		if err != nil {
			return false, err
		}
		collection, err = activityPubCollectionMap(body)
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

func (s *Server) fetchActivityPubFollowersSynchronizationResource(collectionURL string) ([]byte, error) {
	if s != nil && s.db != nil {
		if signer, err := s.representativeActivityPubAccount(); err == nil && signer != nil {
			resource, err := fetchActivityResourceWithMetadataAndUserAgentSigned(collectionURL, paonUserAgent(s.cfg), s, signer)
			if err != nil {
				return nil, err
			}
			return resource.body, nil
		}
	}
	return fetchActivityResource(collectionURL)
}

func activityPubCollectionInlineMap(value any) map[string]any {
	items := activityJSONLDListItems(value)
	if len(items) == 0 {
		return nil
	}
	typed, ok := items[0].(map[string]any)
	if !ok {
		return nil
	}
	switch activityJSONLDType(typed) {
	case "Collection", "CollectionPage", "OrderedCollection", "OrderedCollectionPage":
		return typed
	default:
		return nil
	}
}

func activityPubCollectionMap(body []byte) (map[string]any, error) {
	var out map[string]any
	err := json.Unmarshal(body, &out)
	return out, err
}

func activityPubCollectionURI(value any) string {
	raw := ""
	switch v := activityJSONLDSingle(value).(type) {
	case string:
		raw = v
	case map[string]any:
		raw = activityJSONLDValueOrID(v)
	default:
		return ""
	}
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return raw
}

func activityPubCollectionItems(collection map[string]any) []string {
	var value any
	switch activityJSONLDType(collection) {
	case "Collection", "CollectionPage":
		value = activityJSONLDValue(collection, "items")
	case "OrderedCollection", "OrderedCollectionPage":
		value = activityJSONLDValue(collection, "orderedItems")
	default:
		return nil
	}
	values := activityJSONLDListItems(value)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, item := range values {
		if uri := activityPubCollectionURI(item); uri != "" {
			out = append(out, uri)
		}
	}
	return out
}

func (s *Server) localAccountIDsFromActivityURIs(uris []string) ([]int64, error) {
	ids := []int64{}
	seen := map[int64]struct{}{}
	for _, uri := range uris {
		username := s.localUsernameFromActivityURI(uri)
		if username == "" {
			continue
		}
		var account models.Account
		err := s.db.Where("lower(username) = ? AND domain IS NULL", strings.ToLower(username)).First(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, ok := seen[account.ID]; !ok {
			seen[account.ID] = struct{}{}
			ids = append(ids, account.ID)
		}
	}
	return ids, nil
}

func (s *Server) applyActivityPubFollowersSynchronization(ctx context.Context, account models.Account, expectedIDs []int64) error {
	if err := s.applyExpectedActivityPubFollowersSynchronization(ctx, account, expectedIDs); err != nil {
		return err
	}
	return s.removeUnexpectedActivityPubFollowers(ctx, account, expectedIDs)
}

func (s *Server) removeUnexpectedActivityPubFollowers(ctx context.Context, account models.Account, expectedIDs []int64) error {
	expected := map[int64]struct{}{}
	for _, id := range expectedIDs {
		expected[id] = struct{}{}
	}
	removedFollowerIDs := []int64{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var follows []models.Follow
		if err := tx.Joins("JOIN accounts ON accounts.id = follows.account_id").
			Where("follows.target_account_id = ?", account.ID).
			Where("accounts.domain IS NULL").
			Find(&follows).Error; err != nil {
			return err
		}
		for _, follow := range follows {
			if _, ok := expected[follow.AccountID]; ok {
				continue
			}
			if err := deleteFollow(tx, follow); err != nil {
				return err
			}
			removedFollowerIDs = append(removedFollowerIDs, follow.AccountID)
			s.invalidateFollowRelationshipCaches(ctx, models.Account{ID: follow.AccountID}, account.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.afterActivityPubFollowersSynchronization(ctx, account, removedFollowerIDs, nil, nil)
	return nil
}

func (s *Server) applyExpectedActivityPubFollowersSynchronization(ctx context.Context, account models.Account, expectedIDs []int64) error {
	expected := map[int64]struct{}{}
	for _, id := range expectedIDs {
		expected[id] = struct{}{}
	}
	acceptedFollowerIDs := []int64{}
	undoFollowerIDs := []int64{}
	affectedListIDs := []int64{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for id := range expected {
			var count int64
			if err := tx.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", id, account.ID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			var request models.FollowRequest
			err := tx.Where("account_id = ? AND target_account_id = ?", id, account.ID).First(&request).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				undoFollowerIDs = append(undoFollowerIDs, id)
				continue
			}
			if err != nil {
				return err
			}
			follow := models.Follow{
				CreatedAt:       request.CreatedAt,
				UpdatedAt:       request.UpdatedAt,
				AccountID:       request.AccountID,
				TargetAccountID: request.TargetAccountID,
				ShowReblogs:     request.ShowReblogs,
				Notify:          request.Notify,
				Languages:       request.Languages,
				URI:             request.URI,
			}
			if err := tx.Create(&follow).Error; err != nil {
				return err
			}
			listIDs, err := updateListAccountsForAcceptedFollowRequest(tx, request.ID, follow.ID)
			if err != nil {
				return err
			}
			affectedListIDs = append(affectedListIDs, listIDs...)
			if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", request.ID).Delete(&models.Notification{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&request).Error; err != nil {
				return err
			}
			if err := incrementAccountStatCounter(tx, request.AccountID, accountStatCounterFollowing, 1); err != nil {
				return err
			}
			if err := incrementAccountStatCounter(tx, request.TargetAccountID, accountStatCounterFollowers, 1); err != nil {
				return err
			}
			acceptedFollowerIDs = append(acceptedFollowerIDs, request.AccountID)
			s.invalidateFollowRelationshipCaches(ctx, models.Account{ID: request.AccountID}, account.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, listID := range uniqueInt64s(affectedListIDs) {
		_ = s.clearListFeedCacheContext(ctx, listID)
	}
	s.afterActivityPubFollowersSynchronization(ctx, account, nil, acceptedFollowerIDs, undoFollowerIDs)
	return nil
}

func (s *Server) afterActivityPubFollowersSynchronization(ctx context.Context, account models.Account, removedFollowerIDs []int64, acceptedFollowerIDs []int64, undoFollowerIDs []int64) {
	for _, id := range removedFollowerIDs {
		// Followers-synchronization removal drops a follower's follow of `account`; mirror
		// Rails' follow-destroy path by unmerging `account` from the follower's home feed
		// (element-level, like UnmergeWorker) instead of dropping the whole cached feed.
		s.unmergeAfterUnfollowBestEffort(ctx, account.ID, models.Account{ID: id})
	}
	if len(removedFollowerIDs) > 0 || len(acceptedFollowerIDs) > 0 {
		s.meiliReindexPrivateStatusesForAccountsBestEffort(ctx, account.ID)
	}
	for _, id := range acceptedFollowerIDs {
		s.mergeAfterDirectFollowBestEffort(ctx, account.ID, models.Account{ID: id})
	}
	for _, id := range undoFollowerIDs {
		var follower models.Account
		if err := s.db.Where("id = ? AND domain IS NULL", id).First(&follower).Error; err != nil {
			continue
		}
		_ = s.deliverActivityPubUndoFollow(follower, account, 0, "")
	}
}

func accountURIOrigin(uri string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(uri, prefix) {
			rest := uri[len(prefix):]
			if slash := strings.IndexByte(rest, '/'); slash >= 0 {
				return uri[:len(prefix)+slash]
			}
			return uri
		}
	}
	return ""
}
