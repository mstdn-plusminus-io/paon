package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func activityPubFeaturePolicyBitmap(value any, account models.Account) int {
	policy, ok := activityJSONLDSingle(value).(map[string]any)
	if !ok {
		return 0
	}
	canFeature, ok := activityJSONLDSingle(activityJSONLDValue(policy, "canFeature")).(map[string]any)
	if !ok {
		return 0
	}
	automatic := activityPubFeatureSubpolicyBitmap(activityJSONLDValue(canFeature, "automaticApproval"), account)
	manual := activityPubFeatureSubpolicyBitmap(activityJSONLDValue(canFeature, "manualApproval"), account)
	return automatic<<16 | manual
}

func activityPubFeatureSubpolicyBitmap(value any, account models.Account) int {
	flags := 0
	unknown := false
	includesTargetActor := false
	for _, raw := range activityJSONLDListItems(value) {
		uri := activityJSONLDValueOrID(raw)
		switch {
		case activityPubPublicCollection(uri):
			flags |= featurePolicyPublic
		case uri != "" && uri == account.FollowersURL:
			flags |= featurePolicyFollowers
		case uri != "" && uri == account.FollowingURL:
			flags |= featurePolicyFollowing
		case uri != "" && uri == account.URI:
			includesTargetActor = true
		default:
			if uri != "" {
				unknown = true
			}
		}
	}
	if unknown {
		flags |= featurePolicyUnsupported
	}
	if flags == 0 && includesTargetActor {
		flags |= featurePolicyDisabled
	}
	return flags
}

func activityPubAccountFeatureAutomaticApproval(s *Server, account models.Account) string {
	if !account.Discoverable.Valid || !account.Discoverable.Bool {
		return activityPubAccountTagManagerURI(s, account)
	}
	if account.Locked {
		if strings.TrimSpace(account.FollowersURL) != "" {
			return account.FollowersURL
		}
		return activityPubAccountTagManagerURI(s, account) + "/followers"
	}
	return activityPubPublicIRI
}

func activityPubFeaturedCollectionURI(s *Server, collection models.Collection) string {
	if collection.Local {
		return s.cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(collection.AccountID, 10) + "/collections/" + strconv.FormatInt(collection.ID, 10)
	}
	return strings.TrimSpace(collection.URI.String)
}

func activityPubCollectionURL(s *Server, collection models.Collection) string {
	if collection.Local {
		return s.cfg.BaseURL() + "/collections/" + strconv.FormatInt(collection.ID, 10)
	}
	if collection.URL.Valid && strings.TrimSpace(collection.URL.String) != "" {
		return collection.URL.String
	}
	return activityPubFeaturedCollectionURI(s, collection)
}

func activityPubCollectionItemURI(s *Server, item models.CollectionItem) string {
	if item.Collection.Local {
		return s.cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(item.Collection.AccountID, 10) + "/collection_items/" + strconv.FormatInt(item.ID, 10)
	}
	return strings.TrimSpace(item.URI.String)
}

func (s *Server) activityPubFeaturedCollectionObject(resource collectionResource) (map[string]any, error) {
	collection := resource.Collection
	items := make([]any, 0, len(resource.Items))
	for _, item := range resource.Items {
		if item.State != 1 || !item.AccountID.Valid {
			continue
		}
		item.Collection = collection
		object, err := s.activityPubFeaturedItemObject(item)
		if err != nil {
			return nil, err
		}
		items = append(items, object)
	}
	object := map[string]any{
		"id": activityPubFeaturedCollectionURI(s, collection), "type": "FeaturedCollection",
		"totalItems": len(items), "name": collection.Name,
		"attributedTo": activityPubAccountTagManagerURI(s, collection.Account),
		"url":          activityPubCollectionURL(s, collection), "sensitive": collection.Sensitive,
		"discoverable": collection.Discoverable, "published": collection.CreatedAt.UTC().Format(time.RFC3339),
		"updated": collection.UpdatedAt.UTC().Format(time.RFC3339), "orderedItems": items,
	}
	if collection.Language.Valid && strings.TrimSpace(collection.Language.String) != "" {
		object["summaryMap"] = map[string]string{collection.Language.String: collection.Description.String}
	} else if collection.Description.Valid {
		object["summary"] = collection.Description.String
	}
	if resource.Tag != nil {
		object["topic"] = map[string]any{"type": "Hashtag", "href": s.cfg.BaseURL() + "/tags/" + url.PathEscape(resource.Tag.Name), "name": "#" + resource.Tag.DisplayNameValue()}
	}
	return object, nil
}

func (s *Server) activityPubFeaturedItemObject(item models.CollectionItem) (map[string]any, error) {
	if item.Collection.ID == 0 {
		if err := s.db.Where("id = ?", item.CollectionID).First(&item.Collection).Error; err != nil {
			return nil, err
		}
	}
	if !item.AccountID.Valid {
		return nil, gorm.ErrRecordNotFound
	}
	var account models.Account
	if err := s.db.Where("id = ?", item.AccountID.Int64).First(&account).Error; err != nil {
		return nil, err
	}
	authorization := strings.TrimSpace(item.ApprovalURI.String)
	if account.Local() {
		authorization = s.cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(account.ID, 10) + "/feature_authorizations/" + strconv.FormatInt(item.ID, 10)
	}
	return map[string]any{
		"id": activityPubCollectionItemURI(s, item), "type": "FeaturedItem",
		"featuredObject":       activityPubAccountTagManagerURI(s, account),
		"featureAuthorization": authorization, "published": item.CreatedAt.UTC().Format(time.RFC3339),
	}, nil
}

func (s *Server) activityPubFeatureAuthorizationObject(item models.CollectionItem) (map[string]any, error) {
	if !item.AccountID.Valid {
		return nil, gorm.ErrRecordNotFound
	}
	if item.Collection.ID == 0 {
		if err := s.db.Where("id = ?", item.CollectionID).First(&item.Collection).Error; err != nil {
			return nil, err
		}
	}
	var account models.Account
	if err := s.db.Where("id = ?", item.AccountID.Int64).First(&account).Error; err != nil {
		return nil, err
	}
	return map[string]any{
		"id":   s.cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(account.ID, 10) + "/feature_authorizations/" + strconv.FormatInt(item.ID, 10),
		"type": "FeatureAuthorization", "interactingObject": activityPubFeaturedCollectionURI(s, item.Collection),
		"interactionTarget": activityPubAccountTagManagerURI(s, account),
	}, nil
}

func (s *Server) activityPubFeaturedCollectionForAccount(c *echo.Context, account models.Account, collectionID int64, signed *models.Account) error {
	if hidden, err := s.activityPubCollectionHiddenForSignedAccount(account, signed); err != nil {
		return err
	} else if hidden {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var collection models.Collection
	if err := accountRelationSerializerPreloads(s.db.Model(&models.Collection{}), "Account").Where("collections.id = ? AND collections.account_id = ?", collectionID, account.ID).First(&collection).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	resource, err := s.collectionResource(collection, signed)
	if err != nil {
		return err
	}
	object, err := s.activityPubFeaturedCollectionObject(resource)
	if err != nil {
		return err
	}
	object["@context"] = activityContext()
	return activityJSONWithCachePrivacy(c, object, collectionCacheSeconds(collection), activityPubPublicFetchCache(s))
}

func collectionCacheSeconds(collection models.Collection) int {
	if collection.UpdatedAt.After(time.Now().UTC().Add(-15 * time.Minute)) {
		return 30
	}
	return 300
}

func (s *Server) publicCollection(c *echo.Context) error {
	if shouldRedirectActivityPubHTML(c) {
		return s.webApp(c)
	}
	if s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
	signed, err := s.activityPubSignatureAccountIfAuthorized(c)
	if err != nil {
		return err
	}
	collection, err := s.findCollection(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return s.activityPubFeaturedCollectionForAccount(c, collection.Account, collection.ID, signed)
}

func (s *Server) activityPubCollectionItem(c *echo.Context) error {
	if !activityPubRequestWantsJSON(c) {
		return noContentError(http.StatusNotAcceptable)
	}
	if s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
	signed, err := s.activityPubSignatureAccountIfAuthorized(c)
	if err != nil {
		return err
	}
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return err
	}
	if hidden, err := s.activityPubCollectionHiddenForSignedAccount(*account, signed); err != nil {
		return err
	} else if hidden {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var item models.CollectionItem
	if err := s.db.Preload("Collection").Where("collection_items.id = ? AND collection_items.state = ?", c.Param("id"), 1).First(&item).Error; err != nil || item.Collection.AccountID != account.ID {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	object, err := s.activityPubFeaturedItemObject(item)
	if err != nil {
		return err
	}
	object["@context"] = activityContext()
	return activityJSONWithCachePrivacy(c, object, 180, activityPubPublicFetchCache(s))
}

func (s *Server) activityPubFeatureAuthorization(c *echo.Context) error {
	if !activityPubRequestWantsJSON(c) {
		return noContentError(http.StatusNotAcceptable)
	}
	if s.authorizedFetchMode() {
		appendVaryHeader(c, "Signature")
	}
	signed, err := s.activityPubSignatureAccountIfAuthorized(c)
	if err != nil {
		return err
	}
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return err
	}
	var item models.CollectionItem
	if err := s.db.Preload("Collection.Account").Where("collection_items.id = ? AND collection_items.account_id = ? AND collection_items.state = ?", c.Param("id"), account.ID, 1).First(&item).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if hidden, err := s.activityPubCollectionHiddenForSignedAccount(item.Collection.Account, signed); err != nil {
		return err
	} else if hidden {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	object, err := s.activityPubFeatureAuthorizationObject(item)
	if err != nil {
		return err
	}
	object["@context"] = activityContext()
	return activityJSONWithCachePrivacy(c, object, 30, activityPubPublicFetchCache(s))
}

func (s *Server) activityPubFeaturedCollections(c *echo.Context) error {
	if !activityPubRequestWantsJSON(c) {
		return noContentError(http.StatusNotAcceptable)
	}
	s.activityPubAccountVary(c)
	signed, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	account, err := s.localActivityPubAccount(c)
	if err != nil {
		return err
	}
	if hidden, err := s.activityPubCollectionHiddenForSignedAccount(*account, signed); err != nil {
		return err
	} else if hidden {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	base := s.cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(account.ID, 10) + "/featured_collections"
	var total int64
	if err := s.db.Model(&models.Collection{}).Where("account_id = ?", account.ID).Count(&total).Error; err != nil {
		return err
	}
	if !queryParamPresent(c, "page") {
		return activityJSONWithCachePrivacy(c, map[string]any{
			"@context": activityContext(), "id": base, "type": "Collection", "totalItems": total, "first": base + "?page=1",
		}, 180, activityPubPublicFetchCache(s))
	}
	page := intParam(c.QueryParam("page"), 1)
	if page < 1 {
		page = 1
	}
	var collections []models.Collection
	if err := accountRelationSerializerPreloads(s.db.Model(&models.Collection{}), "Account").Where("collections.account_id = ?", account.ID).Offset((page - 1) * 5).Limit(5).Find(&collections).Error; err != nil {
		return err
	}
	items := []any{}
	for _, collection := range collections {
		resource, err := s.collectionResource(collection, signed)
		if err != nil {
			return err
		}
		object, err := s.activityPubFeaturedCollectionObject(resource)
		if err != nil {
			return err
		}
		items = append(items, object)
	}
	pageURL := base + "?page=" + strconv.Itoa(page)
	response := map[string]any{
		"@context": activityContext(), "id": pageURL, "type": "CollectionPage", "totalItems": total,
		"partOf": base, "items": items,
	}
	if page > 1 {
		response["prev"] = base + "?page=" + strconv.Itoa(page-1)
	}
	if int64(page*5) < total {
		response["next"] = base + "?page=" + strconv.Itoa(page+1)
	}
	return activityJSONWithCachePrivacy(c, response, 0, activityPubPublicFetchCache(s))
}

func (s *Server) distributeLocalCollectionAddedBestEffort(resource collectionResource) {
	object, err := s.activityPubFeaturedCollectionObject(resource)
	if err != nil {
		return
	}
	_ = s.deliverActivityPubCollectionRawDistribution(resource.Collection, map[string]any{
		"@context": activityContext(), "type": "Add", "actor": activityPubAccountTagManagerURI(s, resource.Collection.Account),
		"target": s.cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(resource.Collection.AccountID, 10) + "/featured_collections", "object": object,
	})
	for _, item := range resource.Items {
		if item.State != 0 || !item.AccountID.Valid {
			continue
		}
		var target models.Account
		if err := s.db.Where("id = ?", item.AccountID.Int64).First(&target).Error; err == nil {
			s.deliverFeatureRequestBestEffort(resource.Collection, item, target)
		}
	}
}

func (s *Server) distributeLocalCollectionUpdatedBestEffort(resource collectionResource) {
	object, err := s.activityPubFeaturedCollectionObject(resource)
	if err != nil {
		return
	}
	_ = s.deliverActivityPubCollectionRawDistribution(resource.Collection, map[string]any{
		"@context": activityContext(), "id": activityPubFeaturedCollectionURI(s, resource.Collection) + "#updates/" + strconv.FormatInt(resource.Collection.UpdatedAt.Unix(), 10),
		"type": "Update", "actor": activityPubAccountTagManagerURI(s, resource.Collection.Account), "to": []string{activityPubPublicIRI}, "object": object,
	})
}

func (s *Server) distributeLocalCollectionRemovedBestEffort(resource collectionResource) {
	payload := map[string]any{
		"@context": activityContext(), "type": "Remove", "actor": activityPubAccountTagManagerURI(s, resource.Collection.Account),
		"target": s.cfg.BaseURL() + "/ap/users/" + strconv.FormatInt(resource.Collection.AccountID, 10) + "/featured_collections",
		"object": activityPubFeaturedCollectionURI(s, resource.Collection),
	}
	if body, err := json.Marshal(payload); err == nil && strings.TrimSpace(resource.Collection.Account.InboxURL) != "" {
		for _, item := range resource.Items {
			if item.State != 1 || !item.AccountID.Valid {
				continue
			}
			var member models.Account
			if err := s.db.Where("id = ? AND domain IS NULL", item.AccountID.Int64).First(&member).Error; err == nil && member.PrivateKey.Valid {
				_ = s.deliverActivityPubConfigured(member, resource.Collection.Account.InboxURL, body, nil)
			}
		}
	}
	_ = s.deliverActivityPubCollectionRawDistribution(resource.Collection, payload)
}

func (s *Server) distributeLocalCollectionItemAddedBestEffort(collection models.Collection, item models.CollectionItem, target models.Account) {
	item.Collection = collection
	if item.State == 0 && !target.Local() {
		s.deliverFeatureRequestBestEffort(collection, item, target)
		return
	}
	object, err := s.activityPubFeaturedItemObject(item)
	if err != nil {
		return
	}
	_ = s.deliverActivityPubCollectionRawDistribution(collection, map[string]any{
		"@context": activityContext(), "type": "Add", "actor": activityPubAccountTagManagerURI(s, collection.Account),
		"target": activityPubFeaturedCollectionURI(s, collection), "object": object,
	})
}

func (s *Server) distributeLocalCollectionItemRemovedBestEffort(collection models.Collection, item models.CollectionItem) {
	item.Collection = collection
	_ = s.deliverActivityPubCollectionRawDistribution(collection, map[string]any{
		"@context": activityContext(), "type": "Remove", "actor": activityPubAccountTagManagerURI(s, collection.Account),
		"target": activityPubFeaturedCollectionURI(s, collection), "object": activityPubCollectionItemURI(s, item),
	})
}

func (s *Server) deliverActivityPubCollectionRawDistribution(collection models.Collection, activity map[string]any) error {
	if s == nil || s.db == nil || !collection.Local || !collection.Account.Local() {
		return nil
	}
	signed, err := s.signActivityPubLinkedDataSignaturePayloadWhenEnabled(collection.Account, activity)
	if err != nil {
		return err
	}
	body, err := json.Marshal(signed)
	if err != nil {
		return err
	}
	inboxes, err := s.activityPubAccountReachInboxes(collection.Account)
	if err != nil {
		return err
	}
	var members []struct {
		InboxURL       string `gorm:"column:inbox_url"`
		SharedInboxURL string `gorm:"column:shared_inbox_url"`
	}
	if err := s.db.Model(&models.Account{}).
		Select("accounts.inbox_url, accounts.shared_inbox_url").
		Joins("JOIN collection_items ON collection_items.account_id = accounts.id").
		Where("collection_items.collection_id = ? AND collection_items.state = ? AND accounts.domain IS NOT NULL", collection.ID, 1).
		Find(&members).Error; err != nil {
		return err
	}
	for _, member := range members {
		inbox := firstNonEmpty(member.SharedInboxURL, member.InboxURL)
		if inbox != "" {
			inboxes = append(inboxes, inbox)
		}
	}
	inboxes = s.compactAvailableActivityPubInboxes(compactActivityPubInboxes(inboxes))
	var lastErr error
	for _, inbox := range inboxes {
		if err := s.deliverActivityPub(collection.Account, inbox, body); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (s *Server) deliverFeatureRequestBestEffort(collection models.Collection, item models.CollectionItem, target models.Account) {
	if target.Local() || strings.TrimSpace(target.InboxURL) == "" {
		return
	}
	payload := map[string]any{
		"@context": activityContext(), "id": item.ActivityURI.String, "type": "FeatureRequest",
		"actor": activityPubAccountTagManagerURI(s, collection.Account), "object": activityPubAccountTagManagerURI(s, target),
		"instrument": activityPubFeaturedCollectionURI(s, collection),
	}
	body, err := json.Marshal(payload)
	if err == nil {
		_ = s.deliverActivityPubConfigured(collection.Account, target.InboxURL, body, nil)
	}
}

func (s *Server) distributeFeatureAuthorizationDeletedBestEffort(item models.CollectionItem) {
	if !item.AccountID.Valid || item.Collection.ID == 0 || item.Collection.Local {
		return
	}
	var account models.Account
	if err := accountSerializerPreloads(s.db).Where("accounts.id = ?", item.AccountID.Int64).First(&account).Error; err != nil || !account.Local() {
		return
	}
	authorization, err := s.activityPubFeatureAuthorizationObject(item)
	if err != nil {
		return
	}
	payload := map[string]any{
		"@context": activityContext(), "id": authorization["id"].(string) + "#delete", "type": "Delete",
		"actor": activityPubAccountTagManagerURI(s, account), "to": []string{activityPubPublicIRI}, "object": authorization,
	}
	body, err := json.Marshal(payload)
	if err == nil && item.Collection.Account.InboxURL != "" {
		_ = s.deliverActivityPubConfigured(account, item.Collection.Account.InboxURL, body, nil)
	}
}

func (s *Server) processActivityPubFeatureResponse(payload activityPayload, actor *models.Account, accepted bool) (bool, error) {
	if actor == nil || actor.ID == 0 || payload.Object.ID == "" {
		return false, nil
	}
	var item models.CollectionItem
	err := s.db.Preload("Collection.Account").Where("activity_uri = ? AND account_id = ?", payload.Object.ID, actor.ID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !accepted {
		return true, s.db.Model(&models.CollectionItem{}).Where("id = ?", item.ID).Updates(map[string]any{"state": 2, "updated_at": time.Now().UTC()}).Error
	}
	approvalURI := strings.TrimSpace(payload.Result)
	if approvalURI == "" || !activityPubHTTPURIAllowedRaw(approvalURI) || !activityPubURIHostsMatch(actor.URI, approvalURI) {
		return true, nil
	}
	if err := s.db.Model(&models.CollectionItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"state": 1, "approval_uri": approvalURI, "updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return true, err
	}
	item.State = 1
	item.ApprovalURI = sql.NullString{String: approvalURI, Valid: true}
	s.distributeLocalCollectionItemAddedBestEffort(item.Collection, item, *actor)
	return true, nil
}

func (s *Server) processActivityPubFeatureRequest(ctx context.Context, payload activityPayload, actor *models.Account) error {
	if actor == nil || actor.ID == 0 || actor.Local() || payload.ID == "" || !activityPubURIHostsMatch(actor.URI, payload.ID) {
		return activityPubEventNotAppliedf("FeatureRequest actor or id is invalid")
	}
	target, err := s.localAccountFromActivityURI(payload.Object.ID)
	if err != nil || target == nil {
		return err
	}
	collection, err := s.fetchFeatureRequestCollection(actor, target, payload.Instrument)
	if err != nil || collection == nil {
		return err
	}
	if err := s.hydrateAccountFeaturePolicies([]*models.Account{target}, actor); err != nil {
		return err
	}
	blocked, err := s.collectionAccountsBlockEachOther(actor.ID, target.ID)
	if err != nil {
		return err
	}
	accepted := !blocked && (target.FeaturePolicyCurrentUser == "automatic" || target.FeaturePolicyCurrentUser == "manual")
	if !accepted {
		s.deliverFeatureRequestResponseBestEffort(*target, *actor, payload.ID, 0, nil, false)
		return nil
	}
	var position int
	if err := s.db.Model(&models.CollectionItem{}).Where("collection_id = ?", collection.ID).Select("COALESCE(MAX(position), 0)").Scan(&position).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	item := models.CollectionItem{
		CollectionID: collection.ID, AccountID: sql.NullInt64{Int64: target.ID, Valid: true}, Position: position + 1,
		ActivityURI: sql.NullString{String: payload.ID, Valid: true}, State: 1, CreatedAt: now, UpdatedAt: now, Collection: *collection,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&item).Error; err != nil {
			if isUniqueConstraintError(err) {
				return nil
			}
			return err
		}
		return refreshCollectionItemCount(tx, collection.ID)
	}); err != nil {
		return err
	}
	authorization, err := s.activityPubFeatureAuthorizationObject(item)
	if err != nil {
		return err
	}
	s.deliverFeatureRequestResponseBestEffort(*target, *actor, payload.ID, item.ID, authorization, true)
	s.createCollectionNotificationBestEffort(target.ID, actor.ID, item.ID, "CollectionItem", "added_to_collection", now)
	_ = ctx
	return nil
}

func (s *Server) fetchFeatureRequestCollection(actor *models.Account, signer *models.Account, uri string) (*models.Collection, error) {
	if actor == nil || strings.TrimSpace(uri) == "" {
		return nil, nil
	}
	var known models.Collection
	if err := accountRelationSerializerPreloads(s.db.Model(&models.Collection{}), "Account").Where("collections.account_id = ? AND collections.uri = ?", actor.ID, uri).First(&known).Error; err == nil {
		return &known, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	fetched, err := s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentAndSigner(uri, uri, paonUserAgent(s.cfg), signer)
	if err != nil {
		return nil, err
	}
	object := fetched.Object
	if object.TypeExact != "FeaturedCollection" || object.ID != uri || object.AttributedTo != actor.URI {
		return nil, activityPubEventNotAppliedf("FeatureRequest collection is invalid")
	}
	collection, err := s.findOrCreateRemoteFeaturedCollection(actor, uri)
	if err != nil || collection == nil {
		return collection, err
	}
	if err := s.processActivityPubFeaturedCollectionUpdate(activityPayload{Object: object}, actor); err != nil {
		return nil, err
	}
	return s.findCollection(strconv.FormatInt(collection.ID, 10))
}

func (s *Server) findOrCreateRemoteFeaturedCollection(actor *models.Account, uri string) (*models.Collection, error) {
	if actor == nil || actor.ID == 0 || strings.TrimSpace(uri) == "" || !activityPubHTTPURIAllowedRaw(uri) {
		return nil, nil
	}
	var collection models.Collection
	err := accountRelationSerializerPreloads(s.db.Model(&models.Collection{}), "Account").Where("collections.account_id = ? AND collections.uri = ?", actor.ID, uri).First(&collection).Error
	if err == nil {
		return &collection, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	collection = models.Collection{
		AccountID: actor.ID, Account: *actor, Name: "Featured collection", URI: sql.NullString{String: uri, Valid: true},
		Local: false, Sensitive: false, Discoverable: false, OriginalNumberOfItems: sql.NullInt64{Int64: 0, Valid: true},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.Create(&collection).Error; err != nil {
		if isUniqueConstraintError(err) {
			return s.findOrCreateRemoteFeaturedCollection(actor, uri)
		}
		return nil, err
	}
	return &collection, nil
}

func (s *Server) deliverFeatureRequestResponseBestEffort(local, remote models.Account, requestURI string, itemID int64, authorization map[string]any, accepted bool) {
	if strings.TrimSpace(remote.InboxURL) == "" {
		return
	}
	kind := "Reject"
	idPart := "#rejects/feature_requests/"
	if accepted {
		kind = "Accept"
		idPart = "#accepts/feature_requests/"
	}
	idSuffix := ""
	if itemID > 0 {
		idSuffix = strconv.FormatInt(itemID, 10)
	}
	payload := map[string]any{
		"@context": activityContext(), "id": activityPubAccountTagManagerURI(s, local) + idPart + idSuffix,
		"type": kind, "actor": activityPubAccountTagManagerURI(s, local), "to": activityPubAccountTagManagerURI(s, remote), "object": requestURI,
	}
	if accepted && authorization != nil {
		payload["result"] = authorization["id"]
	}
	body, err := json.Marshal(payload)
	if err == nil {
		_ = s.deliverActivityPubConfigured(local, remote.InboxURL, body, nil)
	}
}

func (s *Server) processActivityPubFeatureAuthorizationDelete(ctx context.Context, payload activityPayload, actor *models.Account) (bool, error) {
	if actor == nil || actor.ID == 0 || payload.Object.ID == "" {
		return false, nil
	}
	var item models.CollectionItem
	err := s.db.WithContext(ctx).Preload("Collection.Account").Where("approval_uri = ? AND account_id = ? AND state IN ?", payload.Object.ID, actor.ID, []int{0, 1}).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if err := s.db.WithContext(ctx).Model(&models.CollectionItem{}).Where("id = ?", item.ID).Updates(map[string]any{"state": 3, "updated_at": time.Now().UTC()}).Error; err != nil {
		return true, err
	}
	s.distributeLocalCollectionItemRemovedBestEffort(item.Collection, item)
	return true, nil
}

func (s *Server) processActivityPubFeaturedCollectionUpdate(payload activityPayload, actor *models.Account, requestIDs ...string) error {
	if actor == nil || actor.ID == 0 || payload.Object.ID == "" || !activityPubURIHostsMatch(actor.URI, payload.Object.ID) {
		return activityPubEventNotAppliedf("FeaturedCollection Update does not belong to actor")
	}
	var collection models.Collection
	if err := s.db.Where("account_id = ? AND uri = ?", actor.ID, payload.Object.ID).First(&collection).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	name := truncateRunes(payload.Object.Name, 256)
	if strings.TrimSpace(name) == "" {
		return activityPubEventNotAppliedf("FeaturedCollection name is missing")
	}
	if activityJSONLDValue(payload.Object.Raw, "sensitive") == nil || activityJSONLDValue(payload.Object.Raw, "discoverable") == nil {
		return activityPubEventNotAppliedf("FeaturedCollection boolean attributes are missing")
	}
	description := payload.Object.Summary
	var language any
	if payload.Object.SummaryMapFirstKey != "" {
		description = payload.Object.SummaryMapFirst
		language = payload.Object.SummaryMapFirstKey
	}
	originalItems := activityInt64Value(activityJSONLDValue(payload.Object.Raw, "totalItems"))
	if originalItems < 0 {
		return activityPubEventNotAppliedf("FeaturedCollection totalItems is invalid")
	}
	updates := map[string]any{
		"updated_at": time.Now().UTC(), "name": name,
		"description_html": truncateRunes(description, 2048), "language": language,
		"original_number_of_items": originalItems,
	}
	updates["sensitive"] = payload.Object.Sensitive
	updates["discoverable"] = payload.Object.Discoverable
	updates["url"] = firstNonEmpty(payload.Object.URL, payload.Object.ID)
	if topic, ok := activityJSONLDSingle(activityJSONLDValue(payload.Object.Raw, "topic")).(map[string]any); ok {
		if topicName := strings.TrimPrefix(strings.TrimSpace(activityJSONLDString(topic, "name")), "#"); topicName != "" {
			tag, err := findOrCreateStatusTag(s.db, normalizedSearchTagName(topicName), topicName, time.Now().UTC())
			if err != nil {
				return err
			}
			updates["tag_id"] = tag.ID
		}
	}
	if err := s.db.Model(&models.Collection{}).Where("id = ?", collection.ID).Updates(updates).Error; err != nil {
		return err
	}
	return s.syncRemoteFeaturedCollectionItems(&collection, payload.Object, firstNonEmpty(requestIDs...))
}

func (s *Server) syncRemoteFeaturedCollectionsBestEffort(account *models.Account, collectionsURL string, requestID string) {
	if account == nil || account.ID == 0 || account.Local() || account.SuspendedAt.Valid || strings.TrimSpace(collectionsURL) == "" {
		return
	}
	if s.enqueueFeaturedCollectionsSyncTask(account.ID, collectionsURL, requestID) {
		return
	}
	_ = s.syncRemoteFeaturedCollectionsNow(context.Background(), account, collectionsURL, requestID)
}

func (s *Server) syncRemoteFeaturedCollectionsNow(ctx context.Context, account *models.Account, collectionsURL string, requestID string) error {
	if account == nil || account.Local() || account.SuspendedAt.Valid || strings.TrimSpace(collectionsURL) == "" {
		return nil
	}
	next := collectionsURL
	processed := 0
	signer := s.activityFetchSigner(nil)
	for page := 0; page < 10 && next != "" && processed < 50; page++ {
		resource, err := fetchActivityResourceWithMetadataAndUserAgentSignedWithAcceptAndContext(ctx, next, paonUserAgent(s.cfg), s, signer, activityResourceAcceptHeader)
		if err != nil {
			return err
		}
		var document map[string]any
		if err := json.Unmarshal(resource.body, &document); err != nil || !activityResourceSupportedContext(document["@context"]) {
			return fmt.Errorf("featured collections collection is invalid")
		}
		items := activityJSONLDListItems(firstNonNil(activityJSONLDValue(document, "orderedItems"), activityJSONLDValue(document, "items")))
		if len(items) == 0 && page == 0 {
			if first := activityJSONLDValueOrID(activityJSONLDValue(document, "first")); first != "" && first != next {
				next = first
				continue
			}
		}
		for _, raw := range items {
			if processed >= 50 {
				break
			}
			processed++
			if err := s.fetchOrProcessRemoteFeaturedCollection(ctx, account, raw, requestID); err != nil {
				return err
			}
		}
		next = activityJSONLDValueOrID(activityJSONLDValue(document, "next"))
	}
	return nil
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func (s *Server) fetchOrProcessRemoteFeaturedCollection(ctx context.Context, account *models.Account, raw any, requestID string) error {
	var object activityObject
	switch value := raw.(type) {
	case activityObject:
		object = value
	case string:
		fetched, err := s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentSignerAcceptAndContext(ctx, value, value, paonUserAgent(s.cfg), nil, activityResourceAcceptHeader)
		if err != nil {
			return err
		}
		object = fetched.Object
	case map[string]any:
		encoded, _ := json.Marshal(value)
		object = activityObjectWithOrderedLanguageMaps(encoded, parseActivityObject(value))
	default:
		return nil
	}
	if object.TypeExact != "FeaturedCollection" || object.ID == "" || object.AttributedTo != account.URI || !activityPubURIHostsMatch(account.URI, object.ID) {
		return nil
	}
	collection, err := s.findOrCreateRemoteFeaturedCollection(account, object.ID)
	if err != nil || collection == nil {
		return err
	}
	return s.processActivityPubFeaturedCollectionUpdate(activityPayload{Object: object}, account, requestID)
}

func (s *Server) syncRemoteFeaturedCollectionItems(collection *models.Collection, object activityObject, requestID string) error {
	if collection == nil || !collection.URI.Valid {
		return nil
	}
	items := remoteFeaturedCollectionItems(object.Raw)
	itemURIs := make([]string, 0, len(items))
	for _, raw := range items {
		if uri := activityJSONLDValueOrID(raw); uri != "" {
			itemURIs = append(itemURIs, uri)
		}
	}
	deleteQuery := s.db.Where("collection_id = ?", collection.ID)
	if len(itemURIs) > 0 {
		deleteQuery = deleteQuery.Where("uri NOT IN ? OR uri IS NULL", itemURIs)
	}
	if err := deleteQuery.Delete(&models.CollectionItem{}).Error; err != nil {
		return err
	}
	for index, raw := range items {
		if err := s.processRemoteFeaturedItem(collection, raw, index+1, requestID); err != nil {
			return err
		}
	}
	return refreshCollectionItemCount(s.db, collection.ID)
}

func remoteFeaturedCollectionItems(raw map[string]any) []any {
	items := activityJSONLDListItems(activityJSONLDValue(raw, "orderedItems"))
	if len(items) > 150 {
		return items[:150]
	}
	return items
}

func (s *Server) processRemoteFeaturedItem(collection *models.Collection, raw any, position int, requestID string) error {
	var object activityObject
	switch value := raw.(type) {
	case activityObject:
		object = value
	case string:
		fetched, err := s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentAndSigner(value, value, paonUserAgent(s.cfg), nil)
		if err != nil {
			return err
		}
		object = fetched.Object
	case map[string]any:
		object = parseActivityObject(value)
	default:
		return nil
	}
	if object.TypeExact != "FeaturedItem" || object.ID == "" || object.FeaturedObject == "" || !activityPubURIHostsMatch(collection.URI.String, object.ID) {
		return nil
	}
	actorURI := object.FeaturedObject
	approvalURI := object.FeatureAuthorization
	var account *models.Account
	state := 0
	if s.localActivityURI(actorURI) {
		local, err := s.localAccountFromActivityURI(actorURI)
		if err != nil || local == nil {
			return err
		}
		var partial models.CollectionItem
		if err := s.db.Where("collection_id = ? AND account_id = ? AND state = ? AND uri IS NULL", collection.ID, local.ID, 1).First(&partial).Error; err != nil {
			return nil
		}
		account = local
		state = 1
	} else {
		if approvalURI == "" || !activityPubURIHostsMatch(actorURI, approvalURI) {
			return nil
		}
		authorization, err := s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentAndSigner(approvalURI, approvalURI, paonUserAgent(s.cfg), nil)
		if err != nil {
			return err
		}
		auth := authorization.Object
		if auth.TypeExact != "FeatureAuthorization" || auth.InteractingObject != collection.URI.String || auth.InteractionTarget != actorURI || !activityPubURIHostsMatch(approvalURI, actorURI) {
			return nil
		}
		account, err = s.activityActorForURIForRequest(actorURI, requestID)
		if err != nil || account == nil {
			return err
		}
		state = 1
	}
	now := time.Now().UTC()
	createdAt := activityActorPublishedAt(object.Published, now)
	item := models.CollectionItem{}
	err := s.db.Where("collection_id = ? AND (uri = ? OR account_id = ?)", collection.ID, object.ID, account.ID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		item = models.CollectionItem{CollectionID: collection.ID, CreatedAt: createdAt}
	} else if err != nil {
		return err
	}
	item.AccountID = sql.NullInt64{Int64: account.ID, Valid: true}
	item.Position = position
	item.URI = sql.NullString{String: object.ID, Valid: true}
	item.ObjectURI = sql.NullString{String: actorURI, Valid: true}
	item.ApprovalURI = sql.NullString{String: approvalURI, Valid: approvalURI != ""}
	if approvalURI != "" {
		item.ApprovalLastVerifiedAt = sql.NullTime{Time: now, Valid: true}
	}
	item.State = state
	item.UpdatedAt = now
	return s.db.Save(&item).Error
}

func (s *Server) processActivityPubFeaturedCollectionAdd(payload activityPayload, actor *models.Account) (bool, error) {
	if actor == nil || actor.ID == 0 {
		return false, nil
	}
	collectionsURL := strings.TrimSpace(actor.CollectionsURL.String)
	if collectionsURL != "" && payload.Target == collectionsURL && payload.Object.TypeExact == "FeaturedCollection" {
		collection, err := s.findOrCreateRemoteFeaturedCollection(actor, payload.Object.ID)
		if err != nil || collection == nil {
			return true, err
		}
		return true, s.processActivityPubFeaturedCollectionUpdate(payload, actor)
	}
	var collection models.Collection
	if err := s.db.Where("account_id = ? AND uri = ?", actor.ID, payload.Target).First(&collection).Error; err != nil {
		return false, nil
	}
	if payload.Object.TypeExact != "FeaturedItem" || payload.Object.FeaturedObject == "" {
		return true, nil
	}
	var maxPosition int
	if err := s.db.Model(&models.CollectionItem{}).Where("collection_id = ?", collection.ID).Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
		return true, err
	}
	if err := s.processRemoteFeaturedItem(&collection, payload.Object, maxPosition+1, ""); err != nil {
		return true, err
	}
	return true, refreshCollectionItemCount(s.db, collection.ID)
}

func (s *Server) processActivityPubFeaturedCollectionRemove(payload activityPayload, actor *models.Account) (bool, error) {
	if actor == nil || actor.ID == 0 {
		return false, nil
	}
	collectionsURL := strings.TrimSpace(actor.CollectionsURL.String)
	if collectionsURL != "" && payload.Target == collectionsURL {
		result := s.db.Where("account_id = ? AND uri = ?", actor.ID, payload.Object.ID).Delete(&models.Collection{})
		return true, result.Error
	}
	var collection models.Collection
	if err := s.db.Where("account_id = ? AND uri = ?", actor.ID, payload.Target).First(&collection).Error; err != nil {
		return false, nil
	}
	result := s.db.Where("collection_id = ? AND uri = ?", collection.ID, payload.Object.ID).Delete(&models.CollectionItem{})
	if result.Error == nil {
		_ = refreshCollectionItemCount(s.db, collection.ID)
	}
	return true, result.Error
}
