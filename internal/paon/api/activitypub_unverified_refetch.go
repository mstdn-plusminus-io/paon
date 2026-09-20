package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/hibiken/asynq"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

type activityPubMutationRefetchResult struct {
	Body    []byte
	Actor   *models.Account
	Skipped bool
}

func activityPubMutationRefetchSupported(typ string) bool {
	switch typ {
	case "Create", "Update", "Delete":
		return true
	default:
		return false
	}
}

// The failed activity is only a hint to synchronize an object from its own
// origin. It is never used as authenticated content, recipients or a deletion
// instruction. This path requires both explicit opt-in and an accepted relay.
func (s *Server) refetchUnverifiedActivityPubMutation(ctx context.Context, claimed activityPayload, relay *models.Account) (activityPubMutationRefetchResult, error) {
	var result activityPubMutationRefetchResult
	if s == nil || s.db == nil || !s.cfg.AllowUnverifiedActivityRefetch || !activityPubMutationRefetchSupported(claimed.Type) {
		return result, fmt.Errorf("unverified activity refetch is disabled or unsupported")
	}
	if relay == nil || relay.Local() || !activityPubSameHTTPSOrigin(relay.URI, relay.InboxURL) {
		return result, fmt.Errorf("unverified activity sender is not an accepted relay")
	}
	accepted, err := s.activityPubRequestedThroughRelay(*relay)
	if err != nil {
		return result, err
	}
	if !accepted {
		return result, fmt.Errorf("unverified activity sender is not an accepted relay")
	}
	actorURI := activityPayloadActorValueOrID(claimed)
	objectURI := claimed.Object.ID
	if !activityPubSameHTTPSOrigin(objectURI, actorURI) || s.localActivityURI(actorURI) {
		return result, fmt.Errorf("refetched object and actor must belong to the same remote HTTPS origin")
	}
	// Fetch the object itself: Update/Delete activity IDs often contain a
	// fragment or identify an event that the origin does not serve over GET.
	body, fetchErr := s.fetchActivityPubOriginBody(ctx, objectURI, s.activityFetchSigner(nil))
	if claimed.Type == "Delete" {
		if status, ok := activityFetchStatus(fetchErr); ok && status == http.StatusGone {
			body, err = json.Marshal(map[string]any{"@context": "https://www.w3.org/ns/activitystreams", "id": objectURI, "type": "Tombstone"})
			if err != nil {
				return result, err
			}
		} else if fetchErr != nil {
			return result, fetchErr
		}
	} else if fetchErr != nil {
		return result, fetchErr
	}
	result.Body, err = activityPubRefetchedMutationBody(claimed, body)
	if err != nil {
		return activityPubMutationRefetchResult{}, err
	}
	if claimed.Type == "Delete" {
		result.Actor, result.Skipped, err = s.activityPubRefetchedDeleteOwner(ctx, actorURI, objectURI)
		return result, err
	}
	result.Actor, err = s.activityActorForURI(actorURI)
	if err != nil {
		return activityPubMutationRefetchResult{}, err
	}
	if result.Actor == nil || result.Actor.Local() || result.Actor.URI != actorURI {
		return activityPubMutationRefetchResult{}, fmt.Errorf("refetched object actor could not be resolved")
	}
	return result, nil
}

// Require existing ownership before calling the normal Delete handler, which
// would otherwise create a delete-before-arrival Tombstone from the relay hint.
// An untracked object confirmed gone needs no local mutation or actor fetch.
func (s *Server) activityPubRefetchedDeleteOwner(ctx context.Context, actorURI, objectURI string) (*models.Account, bool, error) {
	var actor models.Account
	err := s.db.WithContext(ctx).Where("uri = ?", actorURI).First(&actor).Error
	actorMissing := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !actorMissing {
		return nil, false, err
	}
	if !actorMissing && actor.Local() {
		return nil, false, fmt.Errorf("refetched Delete cannot affect a local actor")
	}
	if objectURI == actorURI {
		if actorMissing {
			return nil, true, nil
		}
		return &actor, false, nil
	}
	var status models.Status
	err = s.db.WithContext(ctx).Where("uri = ?", objectURI).First(&status).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if actorMissing || status.AccountID != actor.ID || status.Local.Bool {
		return nil, false, fmt.Errorf("refetched Delete target does not belong to the claimed remote actor")
	}
	return &actor, false, nil
}

func activityPubRefetchedMutationBody(claimed activityPayload, body []byte) ([]byte, error) {
	actorURI := activityPayloadActorValueOrID(claimed)
	if !activityPubMutationRefetchSupported(claimed.Type) || !activityPubSameHTTPSOrigin(claimed.Object.ID, actorURI) {
		return nil, fmt.Errorf("invalid refetched mutation identity")
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	if !activityResourceSupportedContext(document["@context"]) {
		return nil, fmt.Errorf("unsupported refetched object context")
	}
	if _, graph := document["@graph"]; graph {
		return nil, fmt.Errorf("refetched object must be a standalone document")
	}
	object := parseActivityObject(document)
	if object.ID == "" || object.ID != claimed.Object.ID {
		return nil, fmt.Errorf("refetched object ID does not match requested object")
	}
	if claimed.Type == "Delete" {
		if object.Type != "Tombstone" || (object.AttributedTo != "" && object.AttributedTo != actorURI) {
			return nil, fmt.Errorf("refetched object does not confirm deletion by the claimed actor")
		}
		// Do not inherit an atomUri alias or other properties that could select
		// an additional row in the ordinary Delete handler.
		return json.Marshal(map[string]any{
			"@context": "https://www.w3.org/ns/activitystreams", "type": "Delete", "actor": actorURI,
			"object": map[string]any{"id": object.ID, "type": "Tombstone"},
		})
	}
	if claimed.Type == "Update" && activityActorTypeValue(object.Types) != "" {
		if object.ID != actorURI {
			return nil, fmt.Errorf("refetched profile does not match activity actor")
		}
	} else if !activityObjectIsStatus(object) || object.AttributedTo != actorURI {
		return nil, fmt.Errorf("refetched status does not belong to activity actor")
	}
	for key := range document {
		if activityPubJSONLDKeyMatchesTerm(key, "signature") || key == "proof" || key == "https://w3id.org/security#proof" {
			delete(document, key)
		}
	}
	// There is no authenticated copy of the original event ID. Build a fresh
	// processing envelope using only the current origin document and its owner.
	envelope := map[string]any{
		"@context": document["@context"], "type": claimed.Type, "actor": actorURI, "object": document,
	}
	for _, key := range []string{"to", "cc", "published"} {
		if value := activityJSONLDValue(document, key); value != nil {
			envelope[key] = value
		}
	}
	return json.Marshal(envelope)
}

func logActivityPubMutationRefetched(ctx context.Context, claimed activityPayload, relay *models.Account, result activityPubMutationRefetchResult, signatureErr error) {
	taskID, _ := asynq.GetTaskID(ctx)
	queue, _ := asynq.GetQueueName(ctx)
	fields := activityPubLogFieldsFromPayload(claimed)
	outcome := "refetched"
	if result.Skipped {
		outcome = "untracked_delete"
	}
	log.Printf("level=WARN event=activitypub_unverified_activity_refetched task_id=%q queue=%q http_actor_id=%d activity_type=%q activity_id=%q actor=%q object=%q outcome=%q error=%q",
		taskID, queue, relay.ID, fields.Type, fields.ID, fields.Actor, fields.Object, outcome, activityPubErrorLogValue(signatureErr))
}
