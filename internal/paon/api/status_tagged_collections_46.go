package api

import (
	"errors"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) hydrateStatusesTaggedCollections(statuses []models.Status, current *models.Account) error {
	for i := range statuses {
		if err := s.hydrateStatusTaggedCollections(&statuses[i], current); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) hydrateStatusTaggedCollections(status *models.Status, current *models.Account) error {
	if status == nil || status.ID == 0 {
		return nil
	}
	var rows []models.TaggedObject
	if err := s.db.Where("status_id = ? AND ap_type = ? AND object_type = ? AND object_id IS NOT NULL", status.ID, "FeaturedCollection", "Collection").Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.ObjectID.Valid {
			ids = append(ids, row.ObjectID.Int64)
		}
	}
	status.TaggedCollections = []models.Collection{}
	if len(ids) > 0 {
		var collections []models.Collection
		if err := accountRelationSerializerPreloads(s.db.Model(&models.Collection{}), "Account").Where("collections.id IN ?", ids).Find(&collections).Error; err != nil {
			return err
		}
		byID := make(map[int64]models.Collection, len(collections))
		for _, collection := range collections {
			resource, err := s.collectionResource(collection, current)
			if err != nil {
				return err
			}
			resource.Collection.RESTTag = resource.Tag
			resource.Collection.RESTItems = resource.Items
			byID[collection.ID] = resource.Collection
		}
		for _, id := range ids {
			if collection, ok := byID[id]; ok {
				status.TaggedCollections = append(status.TaggedCollections, collection)
			}
		}
	}
	if status.Reblog != nil {
		if err := s.hydrateStatusTaggedCollections(status.Reblog, current); err != nil {
			return err
		}
	}
	if status.Quote != nil && status.Quote.QuotedStatus != nil {
		if err := s.hydrateStatusTaggedCollections(status.Quote.QuotedStatus, current); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return nil
}
