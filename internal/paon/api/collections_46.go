package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const (
	collectionDefaultLimit = 40
	collectionMaximumLimit = 100
	collectionMaximumItems = 25
)

type collectionPayload struct {
	Name              *string
	Description       *string
	Language          *string
	Sensitive         *bool
	Discoverable      *bool
	TagName           *string
	AccountIDs        []int64
	InvalidAccountIDs bool
}

type collectionResource struct {
	Collection models.Collection
	Tag        *models.Tag
	Items      []models.CollectionItem
}

func (s *Server) accountCollections(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:collections"); err != nil {
		return err
	}
	target, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	current, _, _ := s.currentAccount(c)
	if current != nil && current.ID != target.ID {
		blocked, err := s.accountBlocksAccountOrDomain(target.ID, current)
		if err != nil {
			return err
		}
		if blocked {
			return c.JSON(http.StatusOK, map[string]any{"collections": []serializer.Collection{}})
		}
	}
	offset := nonNegativeIntParam(c.QueryParam("offset"))
	pageLimit := limit(c, collectionDefaultLimit, collectionMaximumLimit)
	query := s.db.Where("account_id = ?", target.ID).Order("created_at DESC").Offset(offset).Limit(pageLimit)
	if current == nil || current.ID != target.ID {
		query = query.Where("discoverable = ?", true)
	}
	var collections []models.Collection
	if err := query.Find(&collections).Error; err != nil {
		return err
	}
	resources, err := s.collectionResources(collections, current)
	if err != nil {
		return err
	}
	out := make([]serializer.Collection, 0, len(resources))
	for _, resource := range resources {
		out = append(out, serializer.CollectionFromModel(s.cfg, resource.Collection, resource.Tag, resource.Items))
	}
	var total int64
	totalQuery := s.db.Model(&models.Collection{}).Where("account_id = ?", target.ID)
	if err := totalQuery.Count(&total).Error; err != nil {
		return err
	}
	hasNext := (offset*pageLimit)+len(out) < int(total)
	c.Response().Header().Set("Link", collectionOffsetPaginationLink(c, c.Request().URL.Path, offset, pageLimit, hasNext))
	publicRESTCacheIfUnauthenticated(c, 15)
	return c.JSON(http.StatusOK, map[string]any{"collections": out})
}

func (s *Server) inCollections(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:collections")
	if err != nil {
		return err
	}
	targetID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || targetID <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if targetID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	offset := nonNegativeIntParam(c.QueryParam("offset"))
	pageLimit := limit(c, collectionDefaultLimit, collectionDefaultLimit*2)
	base := s.db.Model(&models.Collection{}).
		Joins("JOIN collection_items ON collection_items.collection_id = collections.id").
		Where("collection_items.account_id = ? AND collection_items.state IN ?", account.ID, []int{0, 1})
	var collections []models.Collection
	if err := base.Session(&gorm.Session{}).Offset(offset).Limit(pageLimit).Find(&collections).Error; err != nil {
		return err
	}
	resources, err := s.collectionResources(collections, account)
	if err != nil {
		return err
	}
	out := make([]serializer.Collection, 0, len(resources))
	for _, resource := range resources {
		out = append(out, serializer.CollectionFromModel(s.cfg, resource.Collection, resource.Tag, resource.Items))
	}
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return err
	}
	hasNext := (offset*pageLimit)+len(out) < int(total)
	c.Response().Header().Set("Link", collectionOffsetPaginationLink(c, c.Request().URL.Path, offset, pageLimit, hasNext))
	return c.JSON(http.StatusOK, map[string]any{"collections": out})
}

func (s *Server) showCollection(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:collections"); err != nil {
		return err
	}
	collection, err := s.findCollection(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	current, _, _ := s.currentAccount(c)
	if current != nil && current.ID != collection.AccountID {
		blocked, err := s.accountBlocksAccountOrDomain(collection.AccountID, current)
		if err != nil {
			return err
		}
		if blocked {
			return apiError(c, http.StatusForbidden, "This action is not allowed")
		}
	}
	resource, err := s.collectionResource(*collection, current)
	if err != nil {
		return err
	}
	accounts, err := s.collectionResourceAccounts(resource, current)
	if err != nil {
		return err
	}
	serializedAccounts := make([]serializer.Account, 0, len(accounts))
	for _, account := range accounts {
		serializedAccounts = append(serializedAccounts, s.serializeAccountForCurrent(account, current))
	}
	publicRESTCacheIfUnauthenticated(c, 15)
	return c.JSON(http.StatusOK, serializer.CollectionWithAccounts{
		Collection: serializer.CollectionFromModel(s.cfg, resource.Collection, resource.Tag, resource.Items),
		Accounts:   serializedAccounts,
	})
}

func (s *Server) createCollection(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:collections")
	if err != nil {
		return err
	}
	payload, err := parseCollectionPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err := validateCollectionPayload(c, payload, true); err != nil {
		return err
	}
	var count int64
	if err := s.db.Model(&models.Collection{}).Where("account_id = ?", account.ID).Count(&count).Error; err != nil {
		return err
	}
	collectionLimit, err := s.collectionLimitForUser(account.User)
	if err != nil {
		return err
	}
	if count >= int64(collectionLimit) {
		return collectionValidationError(c, "base", "ERR_TOO_MANY", fmt.Sprintf("is limited to %d collections", collectionLimit))
	}
	targets, err := s.collectionTargetAccounts(payload.AccountIDs, account)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	collection := models.Collection{
		AccountID: account.ID, Name: strings.TrimSpace(*payload.Name), Local: true,
		Sensitive: *payload.Sensitive, Discoverable: *payload.Discoverable,
		CreatedAt: now, UpdatedAt: now,
		Account: *account,
	}
	if payload.Description != nil {
		collection.Description = sql.NullString{String: strings.TrimSpace(*payload.Description), Valid: true}
	}
	if payload.Language != nil && strings.TrimSpace(*payload.Language) != "" {
		collection.Language = sql.NullString{String: strings.TrimSpace(*payload.Language), Valid: true}
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if payload.TagName != nil && strings.TrimSpace(*payload.TagName) != "" {
			tag, err := findOrCreateStatusTag(tx, normalizedSearchTagName(*payload.TagName), strings.TrimPrefix(strings.TrimSpace(*payload.TagName), "#"), now)
			if err != nil {
				return err
			}
			collection.TagID = sql.NullInt64{Int64: tag.ID, Valid: true}
		}
		if err := tx.Create(&collection).Error; err != nil {
			return err
		}
		for index, target := range targets {
			item := collectionItemForTarget(s, collection, target, index+1, now)
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
		}
		return tx.Model(&models.Collection{}).Where("id = ?", collection.ID).Update("item_count", len(targets)).Error
	})
	if err != nil {
		return err
	}
	resource, err := s.collectionResource(collection, account)
	if err != nil {
		return err
	}
	s.distributeLocalCollectionAddedBestEffort(resource)
	s.notifyLocalCollectionItemsBestEffort(resource, targets)
	return c.JSON(http.StatusOK, map[string]any{"collection": serializer.CollectionFromModel(s.cfg, resource.Collection, resource.Tag, resource.Items)})
}

func (s *Server) updateCollection(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:collections")
	if err != nil {
		return err
	}
	collection, err := s.findCollection(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if collection.AccountID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	payload, err := parseCollectionPayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	if err := validateCollectionPayload(c, payload, false); err != nil {
		return err
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if payload.Name != nil {
		updates["name"] = strings.TrimSpace(*payload.Name)
	}
	if payload.Description != nil {
		updates["description"] = strings.TrimSpace(*payload.Description)
	}
	if payload.Language != nil {
		value := strings.TrimSpace(*payload.Language)
		if value == "" {
			updates["language"] = nil
		} else {
			updates["language"] = value
		}
	}
	if payload.Sensitive != nil {
		updates["sensitive"] = *payload.Sensitive
	}
	if payload.Discoverable != nil {
		updates["discoverable"] = *payload.Discoverable
	}
	if payload.TagName != nil {
		value := strings.TrimSpace(*payload.TagName)
		if value == "" {
			updates["tag_id"] = nil
		} else {
			tag, err := findOrCreateStatusTag(s.db, normalizedSearchTagName(value), strings.TrimPrefix(value, "#"), time.Now().UTC())
			if err != nil {
				return err
			}
			updates["tag_id"] = tag.ID
		}
	}
	if err := s.db.Model(&models.Collection{}).Where("id = ?", collection.ID).Updates(updates).Error; err != nil {
		return err
	}
	updated, err := s.findCollection(strconv.FormatInt(collection.ID, 10))
	if err != nil {
		return err
	}
	resource, err := s.collectionResource(*updated, account)
	if err != nil {
		return err
	}
	s.distributeLocalCollectionUpdatedBestEffort(resource)
	if payload.Name != nil || payload.Description != nil || payload.Sensitive != nil || payload.TagName != nil {
		s.notifyLocalCollectionUpdateBestEffort(resource)
	}
	return c.JSON(http.StatusOK, map[string]any{"collection": serializer.CollectionFromModel(s.cfg, resource.Collection, resource.Tag, resource.Items)})
}

func (s *Server) deleteCollection(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:collections")
	if err != nil {
		return err
	}
	collection, err := s.findCollection(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if collection.AccountID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	resource, _ := s.collectionResource(*collection, account)
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var itemIDs []int64
		if err := tx.Model(&models.CollectionItem{}).Where("collection_id = ?", collection.ID).Pluck("id", &itemIDs).Error; err != nil {
			return err
		}
		if len(itemIDs) > 0 {
			if err := tx.Where("activity_type = ? AND activity_id IN ?", "CollectionItem", itemIDs).Delete(&models.Notification{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("activity_type = ? AND activity_id = ?", "Collection", collection.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Where("collection_id = ?", collection.ID).Delete(&models.CollectionItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("collection_id = ?", collection.ID).Delete(&models.CollectionReport{}).Error; err != nil {
			return err
		}
		return tx.Delete(collection).Error
	}); err != nil {
		return err
	}
	s.distributeLocalCollectionRemovedBestEffort(resource)
	return c.NoContent(http.StatusOK)
}

func (s *Server) createCollectionItem(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:collections")
	if err != nil {
		return err
	}
	collection, err := s.findCollection(c.Param("collection_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if collection.AccountID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	rawAccountID := strings.TrimSpace(requestRawParamValue(c, "account_id"))
	if rawAccountID == "" {
		return apiError(c, http.StatusUnprocessableEntity, "`account_id` parameter is missing")
	}
	targetID, err := strconv.ParseInt(rawAccountID, 10, 64)
	if err != nil || targetID <= 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	targets, err := s.collectionTargetAccounts([]int64{targetID}, account)
	if err != nil {
		return err
	}
	if len(targets) != 1 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var count int64
	if err := s.db.Model(&models.CollectionItem{}).Where("collection_id = ? AND state IN ?", collection.ID, []int{0, 1}).Count(&count).Error; err != nil {
		return err
	}
	if count >= collectionMaximumItems {
		return collectionValidationError(c, "collection_items", "ERR_TOO_MANY", "has too many items")
	}
	var maxPosition int
	if err := s.db.Model(&models.CollectionItem{}).Where("collection_id = ?", collection.ID).Select("COALESCE(MAX(position), 0)").Scan(&maxPosition).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	item := collectionItemForTarget(s, *collection, targets[0], maxPosition+1, now)
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		return tx.Model(&models.Collection{}).Where("id = ?", collection.ID).Update("item_count", gorm.Expr("item_count + 1")).Error
	}); err != nil {
		if isUniqueConstraintError(err) {
			return collectionValidationError(c, "account_id", "ERR_TAKEN", "has already been taken")
		}
		return err
	}
	s.distributeLocalCollectionItemAddedBestEffort(*collection, item, targets[0])
	if targets[0].Local() {
		s.createCollectionNotificationBestEffort(targets[0].ID, collection.AccountID, item.ID, "CollectionItem", "added_to_collection", now)
	}
	return c.JSON(http.StatusOK, map[string]any{"collection_item": serializer.CollectionItemFromModel(item)})
}

func (s *Server) deleteCollectionItem(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:collections")
	if err != nil {
		return err
	}
	collection, err := s.findCollection(c.Param("collection_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if collection.AccountID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	var item models.CollectionItem
	if err := s.db.Where("id = ? AND collection_id = ?", c.Param("id"), collection.ID).First(&item).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("activity_type = ? AND activity_id = ?", "CollectionItem", item.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&item).Error; err != nil {
			return err
		}
		return refreshCollectionItemCount(tx, collection.ID)
	}); err != nil {
		return err
	}
	s.distributeLocalCollectionItemRemovedBestEffort(*collection, item)
	return c.NoContent(http.StatusOK)
}

func (s *Server) revokeCollectionItem(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:collections")
	if err != nil {
		return err
	}
	var item models.CollectionItem
	if err := s.db.Preload("Collection").Where("id = ? AND collection_id = ?", c.Param("id"), c.Param("collection_id")).First(&item).Error; err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if !item.AccountID.Valid || item.AccountID.Int64 != account.ID {
		return apiError(c, http.StatusForbidden, "This action is not allowed")
	}
	if err := s.db.Model(&models.CollectionItem{}).Where("id = ?", item.ID).Updates(map[string]any{"state": 3, "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	item.State = 3
	s.distributeFeatureAuthorizationDeletedBestEffort(item)
	return c.NoContent(http.StatusOK)
}

func (s *Server) findCollection(id string) (*models.Collection, error) {
	var collection models.Collection
	err := accountRelationSerializerPreloads(s.db.Model(&models.Collection{}), "Account").Where("collections.id = ?", id).First(&collection).Error
	return &collection, err
}

func (s *Server) collectionResources(collections []models.Collection, current *models.Account) ([]collectionResource, error) {
	out := make([]collectionResource, 0, len(collections))
	for _, collection := range collections {
		resource, err := s.collectionResource(collection, current)
		if err != nil {
			return nil, err
		}
		out = append(out, resource)
	}
	return out, nil
}

func (s *Server) collectionResource(collection models.Collection, current *models.Account) (collectionResource, error) {
	resource := collectionResource{Collection: collection}
	if collection.TagID.Valid {
		var tag models.Tag
		if err := s.db.Where("id = ?", collection.TagID.Int64).First(&tag).Error; err == nil {
			resource.Tag = &tag
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return resource, err
		}
	}
	query := s.db.Where("collection_id = ?", collection.ID).Order("position ASC")
	if current != nil && current.ID == collection.AccountID {
		query = query.Where("state IN ?", []int{0, 1})
	} else {
		query = query.Where("state = ?", 1)
	}
	if current != nil {
		query = query.Where(`account_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM blocks collection_item_blocks
			WHERE collection_item_blocks.account_id = ?
			  AND collection_item_blocks.target_account_id = collection_items.account_id
		)`, current.ID)
	}
	if err := query.Find(&resource.Items).Error; err != nil {
		return resource, err
	}
	return resource, nil
}

func (s *Server) collectionResourceAccounts(resource collectionResource, current *models.Account) ([]models.Account, error) {
	ids := []int64{resource.Collection.AccountID}
	for _, item := range resource.Items {
		if item.AccountID.Valid {
			ids = append(ids, item.AccountID.Int64)
		}
	}
	ids = uniqueInt64s(ids)
	var accounts []models.Account
	if err := accountSerializerPreloads(s.db).Where("accounts.id IN ?", ids).Find(&accounts).Error; err != nil {
		return nil, err
	}
	byID := map[int64]models.Account{}
	ptrs := make([]*models.Account, 0, len(accounts))
	for i := range accounts {
		ptrs = append(ptrs, &accounts[i])
	}
	if err := s.hydrateAccountFeaturePolicies(ptrs, current); err != nil {
		return nil, err
	}
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]models.Account, 0, len(ids))
	for _, id := range ids {
		if account, ok := byID[id]; ok {
			out = append(out, account)
		}
	}
	return out, nil
}

func (s *Server) collectionTargetAccounts(ids []int64, owner *models.Account) ([]models.Account, error) {
	ids = uniqueInt64s(ids)
	if len(ids) > collectionMaximumItems {
		return nil, apiHTTPError{status: http.StatusUnprocessableEntity, message: "Validation failed: Collection items has too many items"}
	}
	if len(ids) == 0 {
		return []models.Account{}, nil
	}
	var accounts []models.Account
	if err := accountSerializerPreloads(s.db).Where("accounts.id IN ? AND accounts.suspended_at IS NULL", ids).Find(&accounts).Error; err != nil {
		return nil, err
	}
	ptrs := make([]*models.Account, 0, len(accounts))
	for i := range accounts {
		ptrs = append(ptrs, &accounts[i])
	}
	if err := s.hydrateAccountFeaturePolicies(ptrs, owner); err != nil {
		return nil, err
	}
	byID := map[int64]models.Account{}
	for _, account := range accounts {
		byID[account.ID] = account
	}
	out := make([]models.Account, 0, len(ids))
	for _, id := range ids {
		account, ok := byID[id]
		if !ok || account.FeaturePolicyCurrentUser != "automatic" && account.FeaturePolicyCurrentUser != "manual" {
			return nil, apiHTTPError{status: http.StatusForbidden, message: "This account cannot be added to collections"}
		}
		blocked, err := s.collectionAccountsBlockEachOther(owner.ID, account.ID)
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, apiHTTPError{status: http.StatusForbidden, message: "This account cannot be added to collections"}
		}
		out = append(out, account)
	}
	return out, nil
}

func (s *Server) collectionLimitForUser(user models.User) (int, error) {
	roleID := int64(-99)
	if user.RoleID.Valid {
		roleID = user.RoleID.Int64
	}
	role, err := s.userRoleByID(roleID)
	if err != nil {
		return 0, err
	}
	return role.CollectionLimit, nil
}

func (s *Server) collectionAccountsBlockEachOther(leftID, rightID int64) (bool, error) {
	var count int64
	err := s.db.Model(&models.Block{}).
		Where("(account_id = ? AND target_account_id = ?) OR (account_id = ? AND target_account_id = ?)", leftID, rightID, rightID, leftID).
		Count(&count).Error
	return count > 0, err
}

func collectionItemForTarget(s *Server, collection models.Collection, target models.Account, position int, now time.Time) models.CollectionItem {
	state := 0
	activityURI := sql.NullString{}
	if target.Local() {
		state = 1
	} else {
		activityURI = sql.NullString{String: activityPubAccountTagManagerURI(s, collection.Account) + "/feature_requests/" + uuid.NewString(), Valid: true}
	}
	return models.CollectionItem{
		CollectionID: collection.ID, AccountID: sql.NullInt64{Int64: target.ID, Valid: true},
		Position: position, State: state, ActivityURI: activityURI, CreatedAt: now, UpdatedAt: now,
	}
}

func refreshCollectionItemCount(tx *gorm.DB, collectionID int64) error {
	var count int64
	if err := tx.Model(&models.CollectionItem{}).Where("collection_id = ?", collectionID).Count(&count).Error; err != nil {
		return err
	}
	return tx.Model(&models.Collection{}).Where("id = ?", collectionID).Update("item_count", count).Error
}

func (s *Server) notifyLocalCollectionItemsBestEffort(resource collectionResource, targets []models.Account) {
	itemsByAccount := map[int64]models.CollectionItem{}
	for _, item := range resource.Items {
		if item.AccountID.Valid {
			itemsByAccount[item.AccountID.Int64] = item
		}
	}
	for _, target := range targets {
		if !target.Local() {
			continue
		}
		if item, ok := itemsByAccount[target.ID]; ok {
			s.createCollectionNotificationBestEffort(target.ID, resource.Collection.AccountID, item.ID, "CollectionItem", "added_to_collection", item.CreatedAt)
		}
	}
}

func (s *Server) notifyLocalCollectionUpdateBestEffort(resource collectionResource) {
	for _, item := range resource.Items {
		if !item.AccountID.Valid || item.State != 1 {
			continue
		}
		var account models.Account
		if err := s.db.Select("id", "domain").Where("id = ?", item.AccountID.Int64).First(&account).Error; err == nil && account.Local() {
			s.createCollectionNotificationBestEffort(account.ID, resource.Collection.AccountID, resource.Collection.ID, "Collection", "collection_update", resource.Collection.UpdatedAt)
		}
	}
}

func (s *Server) createCollectionNotificationBestEffort(accountID, fromAccountID, activityID int64, activityType, kind string, at time.Time) {
	if accountID == 0 || fromAccountID == 0 || activityID == 0 {
		return
	}
	notification := models.Notification{
		AccountID: accountID, FromAccountID: fromAccountID, ActivityID: activityID,
		ActivityType: activityType, Type: models.NullSafeString(kind), CreatedAt: at, UpdatedAt: at,
	}
	if err := s.db.Create(&notification).Error; err == nil {
		s.publishNotificationIDWithContext(context.Background(), notification.ID)
	}
}

func parseCollectionPayload(c *echo.Context) (collectionPayload, error) {
	payload := collectionPayload{}
	if requestContentTypeIsJSON(c.Request().Header.Get(echo.HeaderContentType)) {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err != nil {
			return payload, err
		}
		decodeRaw(raw, "name", &payload.Name)
		decodeRaw(raw, "description", &payload.Description)
		decodeRaw(raw, "language", &payload.Language)
		decodeRaw(raw, "sensitive", &payload.Sensitive)
		decodeRaw(raw, "discoverable", &payload.Discoverable)
		decodeRaw(raw, "tag_name", &payload.TagName)
		if value, ok := raw["account_ids"]; ok {
			payload.AccountIDs, payload.InvalidAccountIDs = rawCollectionAccountIDs(value)
		}
		return payload, nil
	}
	if err := c.Request().ParseForm(); err != nil {
		return payload, err
	}
	payload.Name = stringPtrFromForm(c, "name")
	payload.Description = stringPtrFromForm(c, "description")
	payload.Language = stringPtrFromForm(c, "language")
	payload.Sensitive = boolPtrFromForm(c, "sensitive")
	payload.Discoverable = boolPtrFromForm(c, "discoverable")
	payload.TagName = stringPtrFromForm(c, "tag_name")
	values := append([]string{}, c.Request().Form["account_ids[]"]...)
	values = append(values, c.Request().Form["account_ids"]...)
	for _, value := range values {
		if id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && id > 0 {
			payload.AccountIDs = append(payload.AccountIDs, id)
		} else {
			payload.InvalidAccountIDs = true
		}
	}
	return payload, nil
}

func rawCollectionAccountIDs(raw json.RawMessage) ([]int64, bool) {
	var values []any
	if json.Unmarshal(raw, &values) != nil {
		return nil, true
	}
	out := []int64{}
	invalid := false
	for _, value := range values {
		if id := anyPositiveInt64(value); id > 0 {
			out = append(out, id)
		} else {
			invalid = true
		}
	}
	return out, invalid
}

func validateCollectionPayload(c *echo.Context, payload collectionPayload, create bool) error {
	if payload.InvalidAccountIDs {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if (create && (payload.Name == nil || strings.TrimSpace(*payload.Name) == "")) || (payload.Name != nil && strings.TrimSpace(*payload.Name) == "") {
		return collectionValidationError(c, "name", "ERR_BLANK", "can't be blank")
	}
	if payload.Name != nil && len([]rune(strings.TrimSpace(*payload.Name))) > 40 {
		return collectionValidationError(c, "name", "ERR_TOO_LONG", "is too long (maximum is 40 characters)")
	}
	if payload.Description != nil && len([]rune(strings.TrimSpace(*payload.Description))) > 100 {
		return collectionValidationError(c, "description", "ERR_TOO_LONG", "is too long (maximum is 100 characters)")
	}
	if create && payload.Sensitive == nil {
		return collectionValidationError(c, "sensitive", "ERR_INCLUSION", "is not included in the list")
	}
	if create && payload.Discoverable == nil {
		return collectionValidationError(c, "discoverable", "ERR_INCLUSION", "is not included in the list")
	}
	if payload.Language != nil && strings.TrimSpace(*payload.Language) != "" && !validCollectionLanguage(*payload.Language) {
		return collectionValidationError(c, "language", "ERR_INVALID", "is invalid")
	}
	if len(uniqueInt64s(payload.AccountIDs)) > collectionMaximumItems {
		return collectionValidationError(c, "collection_items", "ERR_TOO_MANY", "has too many items")
	}
	return nil
}

func validCollectionLanguage(value string) bool {
	value = strings.TrimSpace(value)
	for _, supported := range serializer.SupportedLanguageCodes() {
		if strings.EqualFold(value, supported) {
			return true
		}
	}
	return false
}

func collectionValidationError(c *echo.Context, field, token, description string) error {
	return c.JSON(http.StatusUnprocessableEntity, map[string]any{
		"error": map[string]any{
			"error":   "Validation failed: " + field + " " + description,
			"details": map[string]any{field: []map[string]string{{"error": token, "description": description}}},
		},
	})
}

func nonNegativeIntParam(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func collectionOffsetPaginationLink(c *echo.Context, path string, offset, pageLimit int, hasNext bool) string {
	req := c.Request()
	base := "http://" + req.Host + path
	if req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https" {
		base = "https://" + req.Host + path
	}
	links := []string{}
	if hasNext {
		query := clonePaginationQueryWithAllowedParams(req.URL.Query(), []string{"limit"})
		query.Set("offset", strconv.Itoa(offset+pageLimit))
		links = append(links, "<"+base+"?"+query.Encode()+`>; rel="next"`)
	}
	if offset > 0 {
		query := clonePaginationQueryWithAllowedParams(req.URL.Query(), []string{"limit"})
		query.Set("offset", strconv.Itoa(offset-pageLimit))
		links = append(links, "<"+base+"?"+query.Encode()+`>; rel="prev"`)
	}
	return strings.Join(links, ", ")
}
