package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const activityPubPublicIRI = "https://www.w3.org/ns/activitystreams#Public"

var (
	errActivityPubPollVoteAlreadyVoted = errors.New("activitypub poll vote already voted")
	errActivityPubEventNotApplied      = errors.New("activitypub event was not applied")
)

func activityPubEventNotAppliedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errActivityPubEventNotApplied, fmt.Sprintf(format, args...))
}

type activityPubProcessingOptions struct {
	OverrideTimestamps   bool
	RequestID            string
	DeliveredToAccountID int64
}

func (s *Server) processActivityPubInbox(body []byte, actor *models.Account) error {
	return s.processActivityPubInboxForTarget(body, actor, nil)
}

func (s *Server) processActivityPubInboxForTarget(body []byte, actor *models.Account, target *models.Account) error {
	deliveredToAccountID := int64(0)
	if target != nil {
		deliveredToAccountID = target.ID
	}
	return s.processActivityPubInboxForDeliveredTo(body, actor, target, deliveredToAccountID)
}

func (s *Server) processActivityPubInboxForDeliveredTo(body []byte, actor *models.Account, target *models.Account, deliveredToAccountID int64) error {
	return s.processActivityPubInboxForDeliveredToWithContext(context.Background(), body, actor, target, deliveredToAccountID)
}

func (s *Server) processActivityPubInboxForDeliveredToWithContext(ctx context.Context, body []byte, actor *models.Account, target *models.Account, deliveredToAccountID int64) error {
	originalBody := body
	// Keep the compacted signature intact until a relayed activity has been
	// bound to its original actor. Forwarding compatibility is handled only
	// after authentication, on a separate serialized document.
	verificationBody := activityPubCompactCollectionBody(body)
	if !json.Valid(verificationBody) {
		return activityPubEventNotAppliedf("invalid JSON document")
	}
	if !activityPayloadSupportedContext(verificationBody) {
		return activityPubEventNotAppliedf("unsupported JSON-LD context")
	}
	payload, err := parseActivityPayload(verificationBody)
	if err != nil {
		return activityPubEventNotAppliedf("parse payload: %v", err)
	}
	if actor == nil {
		return activityPubEventNotAppliedf("verified actor is missing")
	}
	if s == nil || s.db == nil {
		return activityPubEventNotAppliedf("database is unavailable")
	}
	target, err = s.activityPubDeliveredToAccount(target, deliveredToAccountID)
	if err != nil {
		return err
	}
	if actor.Local() {
		return activityPubEventNotAppliedf("verified actor %d is local", actor.ID)
	}
	var relayedThrough *models.Account
	if activityPayloadDifferentActor(payload, actor) {
		verifiedActor := s.activityPubLinkedDataSignatureActor(verificationBody, payload)
		if verifiedActor == nil {
			return activityPubEventNotAppliedf("activity actor does not match verified HTTP signature actor")
		}
		if activityPayloadDifferentActor(payload, verifiedActor) {
			return activityPubEventNotAppliedf("linked-data signature actor does not match activity actor")
		}
		relayedThrough = actor
		actor = verifiedActor
	}
	if actor.Local() {
		return activityPubEventNotAppliedf("linked-data signature actor %d is local", actor.ID)
	}
	if actor.SuspendedAt.Valid && !activityPubActivityAllowedWhileSuspended(payload.Type) {
		return nil
	}
	forwardingBody := activityPubFinalizeCollectionBodyForForwarding(originalBody, verificationBody)
	payload, err = parseActivityPayload(forwardingBody)
	if err != nil {
		return activityPubEventNotAppliedf("parse forwarding-safe payload: %v", err)
	}
	if activityPayloadDifferentActor(payload, actor) {
		return activityPubEventNotAppliedf("forwarding-safe activity actor does not match processing actor")
	}
	options := activityPubProcessingOptions{
		OverrideTimestamps:   true,
		RequestID:            remoteStatusDiscoveryRequestID("", activityPayloadIDValueOrID(payload)),
		DeliveredToAccountID: deliveredToAccountID,
	}
	if activityPayloadIsCollection(payload) {
		return s.processActivityPubCollectionWithContext(ctx, payload, actor, target, relayedThrough, options)
	}
	return s.processActivityPubPayloadWithContext(ctx, payload, actor, target, relayedThrough, options)
}

func (s *Server) activityPubDeliveredToAccount(target *models.Account, deliveredToAccountID int64) (*models.Account, error) {
	if deliveredToAccountID == 0 {
		return target, nil
	}
	if target != nil {
		if target.ID != deliveredToAccountID || !target.Local() {
			return nil, activityPubEventNotAppliedf(
				"personal inbox account_id=%d conflicts with processing target account_id=%d",
				deliveredToAccountID,
				target.ID,
			)
		}
		return target, nil
	}
	var deliveredTo models.Account
	err := s.db.Where("id = ? AND domain IS NULL", deliveredToAccountID).First(&deliveredTo).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, activityPubEventNotAppliedf("personal inbox account %d is not a local account", deliveredToAccountID)
	}
	if err != nil {
		return nil, fmt.Errorf("load personal inbox account %d: %w", deliveredToAccountID, err)
	}
	return &deliveredTo, nil
}

func activityPubCompactCollectionBody(body []byte) []byte {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	if signature, ok := raw["signature"].(map[string]any); !ok || signature == nil {
		return body
	}
	compacted, err := activityPubCompactSignedActivityDocument(raw)
	if err != nil {
		withoutSignature := make(map[string]any, len(raw))
		for key, value := range raw {
			if key != "signature" {
				withoutSignature[key] = value
			}
		}
		if fallback, marshalErr := json.Marshal(withoutSignature); marshalErr == nil {
			return fallback
		}
		return body
	}
	if compactedBody, err := json.Marshal(compacted); err == nil {
		return compactedBody
	}
	return body
}

func activityPubFinalizeCollectionBodyForForwarding(originalBody []byte, compactedBody []byte) []byte {
	var original map[string]any
	if err := json.Unmarshal(originalBody, &original); err != nil {
		return compactedBody
	}
	if signature, ok := original["signature"].(map[string]any); !ok || signature == nil {
		return compactedBody
	}
	var compacted map[string]any
	if err := json.Unmarshal(compactedBody, &compacted); err != nil {
		return compactedBody
	}
	if signature, ok := compacted["signature"].(map[string]any); !ok || signature == nil {
		return compactedBody
	}
	activityPubPatchForForwarding(original, compacted)
	if !activityPubSafeForForwarding(original, compacted) {
		delete(compacted, "signature")
	}
	if body, err := json.Marshal(compacted); err == nil {
		return body
	}
	return compactedBody
}

func activityPubPatchForForwarding(original map[string]any, compacted map[string]any) {
	for key, value := range original {
		if key == "@context" || key == "signature" || value == nil {
			continue
		}
		compactedValue, ok := compacted[key]
		if !ok {
			continue
		}
		if originalMap, ok := value.(map[string]any); ok {
			if compactedMap, ok := compactedValue.(map[string]any); ok {
				activityPubPatchForForwarding(originalMap, compactedMap)
			}
			continue
		}
		if originalArray, ok := value.([]any); ok {
			compactedArray, ok := compactedValue.([]any)
			if !ok {
				compactedArray = []any{compactedValue}
			}
			if len(originalArray) != len(compactedArray) {
				return
			}
			patched := make([]any, 0, len(originalArray))
			for index, item := range originalArray {
				compactedItem := compactedArray[index]
				if itemMap, ok := item.(map[string]any); ok {
					if compactedItemMap, ok := compactedItem.(map[string]any); ok {
						activityPubPatchForForwarding(itemMap, compactedItemMap)
						patched = append(patched, compactedItemMap)
						continue
					}
				}
				if item == activityPubPublicIRI && compactedItem == "as:Public" {
					patched = append(patched, item)
					continue
				}
				patched = append(patched, compactedItem)
			}
			compacted[key] = patched
			continue
		}
		if value == activityPubPublicIRI && compactedValue == "as:Public" {
			compacted[key] = value
		}
	}
}

func activityPubSafeForForwarding(original map[string]any, compacted map[string]any) bool {
	for key, value := range original {
		if key == "@context" || key == "signature" {
			continue
		}
		compactedValue, ok := compacted[key]
		if !ok {
			if value == nil {
				continue
			}
			return false
		}
		if activityPubForwardingValueClass(value) != activityPubForwardingValueClass(compactedValue) {
			return false
		}
		if originalMap, ok := value.(map[string]any); ok {
			compactedMap, ok := compactedValue.(map[string]any)
			if !ok || !activityPubSafeForForwarding(originalMap, compactedMap) {
				return false
			}
			continue
		}
		if originalArray, ok := value.([]any); ok {
			compactedArray, ok := compactedValue.([]any)
			if !ok {
				return false
			}
			for index, item := range originalArray {
				if index >= len(compactedArray) {
					return false
				}
				compactedItem := compactedArray[index]
				if itemMap, ok := item.(map[string]any); ok {
					compactedItemMap, ok := compactedItem.(map[string]any)
					if !ok || !activityPubSafeForForwarding(itemMap, compactedItemMap) {
						return false
					}
					continue
				}
				if item != compactedItem {
					return false
				}
			}
			continue
		}
		if value != compactedValue {
			return false
		}
	}
	return true
}

func activityPubForwardingValueClass(value any) string {
	switch value.(type) {
	case nil:
		return "nil"
	case map[string]any:
		return "map"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "bool"
	case float64, float32, int, int64, json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func (s *Server) processActivityPubCollection(payload activityPayload, actor *models.Account, target *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	return s.processActivityPubCollectionWithContext(context.Background(), payload, actor, target, relayedThrough, options)
}

func (s *Server) processActivityPubCollectionWithContext(ctx context.Context, payload activityPayload, actor *models.Account, target *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	for i := len(payload.Items) - 1; i >= 0; i-- {
		if err := s.processActivityPubPayloadWithContext(ctx, activityPayloadWithoutActor(payload.Items[i]), actor, target, relayedThrough, options); err != nil {
			return err
		}
	}
	return nil
}

func activityPayloadWithoutActor(payload activityPayload) activityPayload {
	payload.Actor = ""
	payload.ActorRaw = ""
	payload.ActorPresent = false
	return payload
}

func activityPayloadActorValueOrID(payload activityPayload) string {
	return firstNonEmpty(payload.Actor, payload.ActorRaw)
}

func activityPayloadDifferentActor(payload activityPayload, actor *models.Account) bool {
	return payload.ActorPresent && actor != nil && activityPayloadActorValueOrID(payload) != actor.URI
}

func activityPayloadIDValueOrID(payload activityPayload) string {
	return firstNonEmpty(payload.IDRaw, payload.ID)
}

func activityPayloadIsCollection(payload activityPayload) bool {
	switch payload.Type {
	case "Collection", "CollectionPage", "OrderedCollection", "OrderedCollectionPage":
		return true
	default:
		return false
	}
}

func activityPubActivityAllowedWhileSuspended(activityType string) bool {
	switch activityType {
	case "Delete", "Reject", "Undo", "Update":
		return true
	default:
		return false
	}
}

func activityPayloadSupportedContext(body []byte) bool {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	if activityResourceSupportedContext(raw["@context"]) {
		return true
	}
	if _, ok := activityPubLinkedDataSignatureMap(raw); !ok {
		return false
	}
	compacted, err := activityPubCompactSignedActivityDocument(raw)
	if err != nil {
		return false
	}
	return activityResourceSupportedContext(compacted["@context"])
}

func activityPubCompactSignedActivityDocument(raw map[string]any) (map[string]any, error) {
	signature := raw["signature"]
	unsigned := make(map[string]any, len(raw))
	for key, value := range raw {
		if key != "signature" {
			unsigned[key] = value
		}
	}
	compacted, err := activityPubJSONLDCompactToActivityStreams(unsigned)
	if err != nil {
		return nil, err
	}
	compacted["signature"] = signature
	return compacted, nil
}

func (s *Server) processActivityPubPayload(payload activityPayload, actor *models.Account, target *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	return s.processActivityPubPayloadWithContext(context.Background(), payload, actor, target, relayedThrough, options)
}

func (s *Server) processActivityPubPayloadWithContext(ctx context.Context, payload activityPayload, actor *models.Account, target *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	if activityPayloadDifferentActor(payload, actor) {
		return activityPubEventNotAppliedf("activity actor does not match processing actor")
	}
	switch payload.Type {
	case "Create":
		s.scheduleActivityPubActorRefreshIfStale(actor, payload.ID)
		if payload.ObjectReference && payload.Object.ID != "" {
			return s.processActivityPubDereferencedCreate(payload, actor, target, relayedThrough, options)
		}
		if payload.Object.TypeExact == "EncryptedMessage" {
			return s.processActivityPubCreateEncryptedMessage(payload, actor, target)
		}
		if activityObjectIsStatus(payload.Object) {
			return s.processActivityPubCreateNote(payload, actor, target, relayedThrough, options)
		}
	case "Update":
		s.scheduleActivityPubActorRefreshIfStale(actor, payload.ID)
		return s.processActivityPubUpdate(payload, actor, target, relayedThrough, options)
	case "Delete":
		return s.processActivityPubDeleteWithContext(ctx, payload, actor)
	case "Move":
		return s.processActivityPubMove(payload, actor)
	case "Accept":
		return s.processActivityPubAccept(payload, actor, options.DeliveredToAccountID)
	case "Reject":
		return s.processActivityPubReject(payload, actor, options.DeliveredToAccountID)
	case "Block":
		return s.processActivityPubBlock(payload, actor)
	case "Flag":
		return s.processActivityPubFlag(payload, actor)
	case "Add":
		return s.processActivityPubAdd(payload, actor, options)
	case "Remove":
		return s.processActivityPubRemove(payload, actor)
	case "Follow":
		return s.processActivityPubFollow(payload, actor)
	case "Like":
		return s.processActivityPubLike(payload, actor)
	case "Announce":
		return s.processActivityPubAnnounce(payload, actor, relayedThrough, options)
	case "Undo":
		if !payload.Object.TypePresent {
			return s.processActivityPubUndoReference(payload.Object, actor)
		}
		if payload.Object.TypeExact == "Follow" {
			return s.processActivityPubUndoFollow(payload.Object, actor)
		}
		if payload.Object.TypeExact == "Accept" {
			return s.processActivityPubUndoAccept(payload.Object, actor)
		}
		if payload.Object.TypeExact == "Like" {
			return s.processActivityPubUndoLike(payload.Object, actor)
		}
		if payload.Object.TypeExact == "Announce" {
			return s.processActivityPubUndoAnnounce(payload.Object, actor)
		}
		if payload.Object.TypeExact == "Block" {
			return s.processActivityPubUndoBlock(payload.Object, actor)
		}
	case "View":
		// PeerTube federates aggregate video view counters as View
		// activities. Mastodon has no state to apply for them, so accept the
		// authenticated activity without sending it through retry/archive.
		return nil
	}
	logActivityPubUnsupportedPayload(payload, actor.ID, "unsupported_activity_or_object_type")
	return activityPubEventNotAppliedf("unsupported activity type %q or object type %q", payload.Type, payload.Object.TypeExact)
}

func (s *Server) processActivityPubDereferencedCreate(payload activityPayload, actor *models.Account, target *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	objectFetchURI, objectURI := activityPubDereferenceFetchURI(payload.Object.ID)
	if s == nil || actor == nil || objectFetchURI == "" || objectURI == "" {
		return activityPubEventNotAppliedf("dereferenced Create has no fetchable object")
	}
	if activityPubURIHostMismatch(actor.URI, objectURI) {
		return activityPubEventNotAppliedf("dereferenced Create object host does not match actor")
	}
	if disallowed, err := s.remoteActivityDomainNotAllowed(objectURI); err != nil || disallowed {
		return err
	}
	if target == nil && options.DeliveredToAccountID != 0 {
		var deliveredTo models.Account
		if err := s.db.Where("id = ?", options.DeliveredToAccountID).First(&deliveredTo).Error; err != nil {
			return fmt.Errorf("activitypub delivered-to account lookup: %w", err)
		}
		target = &deliveredTo
	}
	signer, err := s.activityPubSignedFetchAccount(actor, target, payload.To, payload.CC)
	if err != nil {
		return err
	}
	dereferenced, err := s.fetchActivityDereferencerPayloadStrictWithExpectedID(objectFetchURI, objectURI, paonUserAgent(s.cfg), signer)
	if err != nil {
		return err
	}
	if dereferenced.Type != "Create" || !activityObjectIsStatus(dereferenced.Object) {
		return activityPubEventNotAppliedf("dereferenced Create did not resolve to a status")
	}
	if dereferenced.Actor != "" && actor.URI != "" && dereferenced.Actor != actor.URI {
		return activityPubEventNotAppliedf("dereferenced Create actor does not match verified actor")
	}
	if dereferenced.Object.AttributedTo == "" {
		dereferenced.Object.AttributedTo = actor.URI
	}
	if !activityNoteBelongsToActor(dereferenced.Object, actor) {
		return activityPubEventNotAppliedf("dereferenced Create object does not belong to verified actor")
	}
	dereferenced.ID = firstNonEmpty(payload.ID, dereferenced.ID)
	dereferenced.Actor = firstNonEmpty(payload.Actor, dereferenced.Actor, actor.URI)
	dereferenced.Published = firstNonEmpty(payload.Published, dereferenced.Published)
	dereferenced.To = firstNonEmptyStringSlice(payload.To, dereferenced.To)
	dereferenced.CC = firstNonEmptyStringSlice(payload.CC, dereferenced.CC)
	return s.processActivityPubCreateNote(dereferenced, actor, target, relayedThrough, options)
}

func firstNonEmptyStringSlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func (s *Server) processActivityPubUndoReference(object activityObject, actor *models.Account) error {
	if object.ID == "" && !object.Reference && !object.IDPresent {
		return activityPubEventNotAppliedf("Undo object reference is missing")
	}
	if handled, err := s.processActivityPubUndoAnnounceWithTombstone(object, actor, false); err != nil || handled {
		return err
	}
	if handled, err := s.processActivityPubUndoFollowWithTombstone(object, actor, false); err != nil || handled {
		return err
	}
	if handled, err := s.processActivityPubUndoBlockWithTombstone(object, actor, false); err != nil || handled {
		return err
	}
	return s.markActivityPubDeleteUponArrival(actor, object.ID)
}

const activityPubBackgroundRefreshInterval = 7 * 24 * time.Hour

func (s *Server) scheduleActivityPubActorRefreshIfStale(actor *models.Account, requestID string) {
	if s == nil || !activityPubActorRefreshStale(actor, time.Now().UTC()) {
		return
	}
	s.enqueueAccountRefreshTask(actor.ID)
}

func activityPubActorRefreshStale(actor *models.Account, now time.Time) bool {
	if actor == nil || actor.ID == 0 || actor.Local() || !actor.LastWebfingeredAt.Valid {
		return false
	}
	return !actor.LastWebfingeredAt.Time.After(now.UTC().Add(-activityPubBackgroundRefreshInterval))
}

func (s *Server) processActivityPubAdd(payload activityPayload, actor *models.Account, options activityPubProcessingOptions) error {
	if actor == nil || actor.ID == 0 || !activityTargetIsFeaturedCollection(payload.Target, s, *actor) {
		return activityPubEventNotAppliedf("Add target is not the verified actor's featured collection")
	}
	if payload.Object.TypeExact == "Hashtag" {
		_, _, err := s.createFeaturedTagForAccount(actor, payload.Object.Name, true)
		return err
	}
	status, err := s.statusFromActivityURI(payload.Object.ID)
	if err == nil && status == nil && activityPubEmbeddedSelfStatus(payload.Object, actor) {
		createPayload := activityPayload{
			ID:        payload.Object.ID,
			Type:      "Create",
			Actor:     actor.URI,
			Published: payload.Published,
			Object:    payload.Object,
			To:        payload.Object.To,
			CC:        payload.Object.CC,
			RawBody:   payload.RawBody,
		}
		if err := s.processActivityPubCreateNote(createPayload, actor, nil, nil, options); err != nil {
			return err
		}
		status, err = s.statusFromActivityURI(payload.Object.ID)
	}
	if err == nil && status == nil {
		signer, signerErr := s.firstActivityPubLocalFollower(actor)
		if signerErr != nil {
			return signerErr
		}
		if activityPubHTTPURIAllowedRaw(payload.Object.ID) {
			status, err = s.fetchRemoteStatusFromActivityURIForRequestWithSigner(payload.Object.ID, actor.URI, payload.ID, signer)
		} else if targetURL := payload.Object.URL; activityPubHTTPURIAllowedRaw(targetURL) && !s.localActivityURI(targetURL) {
			status, err = s.fetchRemoteStatusFromResolvableURL(targetURL)
		}
	}
	if err != nil || status == nil {
		if err != nil {
			return err
		}
		return activityPubEventNotAppliedf("Add object status %q could not be resolved", payload.Object.ID)
	}
	if !activityPubStatusPinValidForRails(*actor, *status) {
		return activityPubEventNotAppliedf("Add object status %q cannot be pinned by actor", payload.Object.ID)
	}
	now := time.Now().UTC()
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.StatusPin{AccountID: actor.ID, StatusID: status.ID, CreatedAt: now, UpdatedAt: now}).Error
}

func activityPubStatusPinValidForRails(actor models.Account, status models.Status) bool {
	return status.AccountID == actor.ID && !status.ReblogOfID.Valid && status.Visibility != 3
}

func (s *Server) processActivityPubRemove(payload activityPayload, actor *models.Account) error {
	if actor == nil || actor.ID == 0 || !activityTargetIsFeaturedCollection(payload.Target, s, *actor) {
		return activityPubEventNotAppliedf("Remove target is not the verified actor's featured collection")
	}
	if payload.Object.TypeExact == "Hashtag" {
		normalized, _, ok := normalizeTagName(payload.Object.Name)
		if !ok {
			return activityPubEventNotAppliedf("Remove Hashtag name is invalid")
		}
		return s.db.Exec(`
			DELETE FROM featured_tags
			USING tags
			WHERE featured_tags.tag_id = tags.id
			  AND featured_tags.account_id = ?
			  AND lower(tags.name) = ?
		`, actor.ID, normalized).Error
	}
	status, err := s.statusFromActivityURI(payload.Object.ID)
	if err != nil {
		return err
	}
	if status == nil {
		// The requested final state is already reached when the status is gone.
		return nil
	}
	if status.AccountID != actor.ID {
		return activityPubEventNotAppliedf("Remove object status %q does not belong to actor", payload.Object.ID)
	}
	var pin models.StatusPin
	deleted := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("account_id = ? AND status_id = ?", actor.ID, status.ID).First(&pin).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Delete(&pin).Error; err != nil {
			return err
		}
		deleted = true
		return nil
	}); err != nil {
		return err
	}
	if deleted {
		s.runStatusPinDestroyedSideEffects(context.Background(), pin)
	}
	return nil
}

func activityTargetIsFeaturedCollection(target string, s *Server, actor models.Account) bool {
	if strings.TrimSpace(target) == "" || !actor.FeaturedCollectionURL.Valid {
		return false
	}
	return target == actor.FeaturedCollectionURL.String
}

func (s *Server) processActivityPubFlag(payload activityPayload, actor *models.Account) error {
	if actor == nil || actor.ID == 0 || actor.Local() {
		return activityPubEventNotAppliedf("Flag actor must be a persisted remote account")
	}
	reject, err := s.activityPubRejectsReportsFromDomain(*actor)
	if err != nil || reject {
		return err
	}
	uris := payload.ObjectIDRaws
	if len(uris) == 0 && payload.Object.ID != "" {
		uris = []string{payload.Object.ID}
	}
	if len(uris) == 0 {
		return activityPubEventNotAppliedf("Flag has no reportable objects")
	}
	statusesByAccount, err := s.activityPubFlagStatusesByAccount(uris)
	if err != nil {
		return err
	}
	targetAccounts, err := s.activityPubFlagTargetAccounts(uris)
	if err != nil {
		return err
	}
	if len(targetAccounts) == 0 {
		return activityPubEventNotAppliedf("Flag did not identify a known target account")
	}
	comment := activityPubFlagComment(payload.Content)
	for _, target := range targetAccounts {
		if target.SuspendedAt.Valid {
			continue
		}
		statuses := statusesByAccount[target.ID]
		repliedToLocal, err := s.activityPubFlagStatusesReplyToLocalAccounts(statuses)
		if err != nil {
			return err
		}
		if !target.Local() && !repliedToLocal {
			continue
		}
		if err := s.createActivityPubFlagReport(*actor, target, activityPubFlagStatusIDs(statuses), comment, activityPubFlagReportURI(firstNonEmpty(payload.IDRaw, payload.ID), actor)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) activityPubRejectsReportsFromDomain(actor models.Account) (bool, error) {
	if !actor.Domain.Valid {
		return false, nil
	}
	block, err := s.existingDomainBlockForDomain(actor.Domain.String)
	if err != nil || block == nil {
		return false, err
	}
	return block.RejectReports, nil
}

func (s *Server) activityPubFlagTargetAccounts(uris []string) ([]models.Account, error) {
	targets := make([]models.Account, 0, len(uris))
	for _, uri := range uris {
		account, err := s.accountFromActivityURI(uri)
		if err != nil {
			return nil, err
		}
		if account == nil {
			continue
		}
		targets = append(targets, *account)
	}
	return targets, nil
}

func (s *Server) activityPubFlagStatusesByAccount(uris []string) (map[int64][]models.Status, error) {
	out := map[int64][]models.Status{}
	for _, uri := range uris {
		status, err := s.statusFromActivityURI(uri)
		if err != nil {
			return nil, err
		}
		if status == nil {
			continue
		}
		out[status.AccountID] = append(out[status.AccountID], *status)
	}
	return out, nil
}

func (s *Server) activityPubFlagStatusesReplyToLocalAccounts(statuses []models.Status) (bool, error) {
	replyAccountIDs := make([]int64, 0, len(statuses))
	for _, status := range statuses {
		if status.InReplyToAccountID.Valid {
			replyAccountIDs = append(replyAccountIDs, status.InReplyToAccountID.Int64)
		}
	}
	if len(replyAccountIDs) == 0 {
		return false, nil
	}
	var count int64
	if err := s.db.Model(&models.Account{}).
		Where("id IN ? AND domain IS NULL", replyAccountIDs).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func activityPubFlagStatusIDs(statuses []models.Status) models.Int64Array {
	out := make(models.Int64Array, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, status.ID)
	}
	return out
}

func activityPubFlagComment(comment string) string {
	runes := []rune(comment)
	if len(runes) > 5000 {
		return reportComment(string(runes[:5000]))
	}
	return reportComment(comment)
}

func activityPubFlagReportURI(id string, actor *models.Account) sql.NullString {
	if actor == nil || id == "" || activityPubURIHostsMatch(actor.URI, id) {
		return sql.NullString{String: id, Valid: id != ""}
	}
	return sql.NullString{}
}

func activityPubURIHostsMatch(left string, right string) bool {
	if !activityPubHTTPURIAllowedRaw(right) {
		return false
	}
	leftURL, err := url.Parse(left)
	if err != nil || leftURL.Host == "" {
		return false
	}
	rightURL, err := url.Parse(right)
	if err != nil || rightURL.Host == "" {
		return false
	}
	return strings.EqualFold(leftURL.Hostname(), rightURL.Hostname())
}

func activityPubNormalizedURIHostsMatch(left string, right string) bool {
	if !activityPubHTTPURIAllowedRaw(right) {
		return false
	}
	leftHost := activityPubNormalizedURIHost(left)
	rightHost := activityPubNormalizedURIHostRaw(right)
	return leftHost != "" && rightHost != "" && leftHost == rightHost
}

func activityPubNormalizedURIHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	return normalizeDeliveryStatsHost(parsed.Hostname())
}

func activityPubNormalizedURIHostRaw(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return normalizeDeliveryStatsHost(parsed.Hostname())
}

func (s *Server) createActivityPubFlagReport(source models.Account, target models.Account, statusIDs models.Int64Array, comment string, uri sql.NullString) error {
	now := time.Now().UTC()
	filteredStatusIDs, err := s.activityPubFlagReportStatusIDs(source, target, statusIDs)
	if err != nil {
		return err
	}
	report := models.Report{
		StatusIDs:       filteredStatusIDs,
		Comment:         reportComment(comment),
		CreatedAt:       now,
		UpdatedAt:       now,
		AccountID:       source.ID,
		TargetAccountID: target.ID,
		URI:             uri,
		Forwarded:       sql.NullBool{Bool: false, Valid: true},
		Category:        reportCategoryValue("other"),
	}
	var staffNotificationPayloads []asynqLocalNotificationPayload
	created := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if uri.Valid && strings.TrimSpace(uri.String) != "" {
			lockKey := activityPubFlagReportLockKey(uri.String, source.ID, target.ID)
			if tx.Dialector.Name() == "postgres" {
				if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", lockKey).Error; err != nil {
					return err
				}
			}
			var duplicateCount int64
			if err := tx.Model(&models.Report{}).
				Where("uri = ? AND account_id = ? AND target_account_id = ?", uri.String, source.ID, target.ID).
				Count(&duplicateCount).Error; err != nil {
				return err
			}
			if duplicateCount > 0 {
				return nil
			}
		}
		if err := tx.Create(&report).Error; err != nil {
			return err
		}
		created = true
		payloads, err := s.createStaffReportNotificationPayloads(tx, report, source)
		staffNotificationPayloads = payloads
		return err
	}); err != nil {
		return err
	}
	if !created {
		return nil
	}
	s.triggerReportCreatedWebhook(report)
	if len(staffNotificationPayloads) > 0 {
		if _, err := s.enqueueOrCreateLocalNotifications(context.Background(), staffNotificationPayloads); err != nil {
			return err
		}
		_ = s.sendStaffNewReportMails(report)
	}
	return nil
}

func activityPubFlagReportLockKey(uri string, sourceAccountID int64, targetAccountID int64) string {
	return strconv.Itoa(len(uri)) + ":" + uri + ":" + strconv.FormatInt(sourceAccountID, 10) + ":" + strconv.FormatInt(targetAccountID, 10)
}

func (s *Server) activityPubFlagReportStatusIDs(source models.Account, target models.Account, statusIDs models.Int64Array) (models.Int64Array, error) {
	if len(statusIDs) == 0 {
		return models.Int64Array{}, nil
	}
	if source.Local() {
		return statusIDs, nil
	}
	domain := strings.ToLower(strings.TrimSpace(source.Domain.String))
	if domain == "" {
		return models.Int64Array{}, nil
	}
	var followers int64
	if err := s.db.Table("follows").
		Joins("JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id = ? AND lower(accounts.domain) = ?", target.ID, domain).
		Count(&followers).Error; err != nil {
		return nil, err
	}
	visibility := []int{0, 1}
	if followers > 0 {
		visibility = append(visibility, 2)
	}
	var allowed []int64
	if err := s.db.Model(&models.Status{}).
		Where("account_id = ? AND id IN ?", target.ID, []int64(statusIDs)).
		Where(`visibility IN ? OR EXISTS (
			SELECT 1
			FROM mentions m
			JOIN accounts a ON a.id = m.account_id
			WHERE m.status_id = statuses.id
			  AND lower(a.domain) = ?
		)`, visibility, domain).
		Pluck("id", &allowed).Error; err != nil {
		return nil, err
	}
	allowedSet := make(map[int64]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	out := make(models.Int64Array, 0, len(statusIDs))
	for _, id := range statusIDs {
		if _, ok := allowedSet[id]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *Server) processActivityPubAccept(payload activityPayload, actor *models.Account, deliveredToAccountID int64) error {
	if actor == nil || actor.ID == 0 || actor.Local() {
		return activityPubEventNotAppliedf("Accept actor must be a persisted remote account")
	}
	if handled, err := s.processActivityPubRelayFollowResponse(payload.Object.ID, payload.Object.IDPresent, relayStateAccepted); handled || err != nil {
		return err
	}
	match, err := s.outgoingFollowResponseFromActivity(payload.Object, actor, deliveredToAccountID)
	if err != nil {
		return err
	}
	if match.Request == nil {
		if match.Follow != nil {
			return nil
		}
		return activityPubEventNotAppliedf(
			"Accept did not match an outgoing Follow request source_account_id=%d target_account_id=%d",
			match.SourceAccountID,
			actor.ID,
		)
	}
	request := match.Request
	hasLocalFollower, err := s.remoteAccountHasLocalFollowers(actor.ID)
	if err != nil {
		return err
	}
	isFirstLocalFollow := !hasLocalFollower
	follow, created, affectedListIDs, err := s.acceptOutgoingFollowRequest(*request)
	if err != nil || follow == nil {
		return err
	}
	for _, listID := range uniqueInt64s(affectedListIDs) {
		_ = s.clearListFeedCacheContext(context.Background(), listID)
	}
	_ = s.clearHomeFeedCacheContext(context.Background(), request.AccountID)
	s.invalidateFollowRelationshipCaches(context.Background(), request.Account, actor.ID)
	if created {
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), actor.ID)
		s.mergeAfterDirectFollowBestEffort(context.Background(), actor.ID, request.Account)
	}
	if isFirstLocalFollow {
		s.enqueueRemoteAccountRefreshTask(actor.ID, payload.ID)
	}
	return nil
}

func (s *Server) processActivityPubReject(payload activityPayload, actor *models.Account, deliveredToAccountID int64) error {
	if actor == nil || actor.ID == 0 || actor.Local() {
		return activityPubEventNotAppliedf("Reject actor must be a persisted remote account")
	}
	if handled, err := s.processActivityPubRelayFollowResponse(payload.Object.ID, payload.Object.IDPresent, relayStateRejected); handled || err != nil {
		return err
	}
	match, err := s.outgoingFollowResponseFromActivity(payload.Object, actor, deliveredToAccountID)
	if err != nil {
		return err
	}
	if match.Request != nil {
		affectedListIDs, err := s.deleteOutgoingFollowRequest(*match.Request)
		if err != nil {
			return err
		}
		for _, listID := range uniqueInt64s(affectedListIDs) {
			_ = s.clearListFeedCacheContext(context.Background(), listID)
		}
		s.invalidateRelationshipCaches(context.Background(), match.Request.AccountID, actor.ID)
		return nil
	}
	if match.Follow == nil {
		// A repeated Reject is already in its requested final state.
		return nil
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return deleteFollow(tx, *match.Follow)
	}); err != nil {
		return err
	}
	s.unmergeAfterUnfollowBestEffort(context.Background(), actor.ID, match.Follow.Account)
	s.invalidateFollowRelationshipCaches(context.Background(), match.Follow.Account, actor.ID)
	s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), actor.ID)
	return nil
}

func (s *Server) processActivityPubRelayFollowResponse(followActivityID string, followActivityIDPresent bool, state int) (bool, error) {
	if !followActivityIDPresent {
		return false, nil
	}
	result := s.db.Model(&models.Relay{}).
		Where("follow_activity_id = ?", followActivityID).
		Updates(map[string]any{"state": state, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (s *Server) remoteAccountHasLocalFollowers(accountID int64) (bool, error) {
	if s == nil || s.db == nil || accountID == 0 {
		return false, nil
	}
	var count int64
	err := s.db.Table("follows").
		Joins("INNER JOIN accounts ON accounts.id = follows.account_id").
		Where("follows.target_account_id = ? AND accounts.domain IS NULL", accountID).
		Limit(1).
		Count(&count).Error
	return count > 0, err
}

func (s *Server) refreshActivityPubRemoteAccountBestEffort(account *models.Account, requestID string) {
	_ = s.refreshActivityPubRemoteAccount(context.Background(), account, requestID)
}

func (s *Server) refreshActivityPubRemoteAccount(ctx context.Context, account *models.Account, requestID string) error {
	if s == nil || s.db == nil || account == nil || account.ID == 0 || account.Local() || strings.TrimSpace(account.URI) == "" {
		return nil
	}
	if disallowed, err := s.remoteActivityDomainNotAllowed(account.URI); err != nil || disallowed {
		return err
	}
	actor, err := s.fetchActivityActor(account.URI)
	if err != nil || actor.ID == "" || actor.PublicKey.PublicKeyPem == "" {
		if status, ok := activityFetchStatus(err); ok && activityPubDeliveryResponseErrorUnsalvageable(status) {
			return nil
		}
		if err != nil {
			return err
		}
		return nil
	}
	_, err = s.upsertRemoteActivityActorForRequest(actor, requestID)
	return err
}

func (s *Server) processActivityPubBlock(payload activityPayload, actor *models.Account) error {
	if actor == nil || actor.ID == 0 || actor.Local() {
		return activityPubEventNotAppliedf("Block actor must be a persisted remote account")
	}
	activityID := activityPayloadIDValueOrID(payload)
	activityIDPresent := strings.TrimSpace(activityID) != ""
	generateBlockURI := !payload.IDPresent
	target, err := s.localAccountFromActivityURI(payload.Object.ID)
	if err != nil {
		return err
	}
	if target == nil || !target.Local() || target.ID == actor.ID {
		return activityPubEventNotAppliedf("Block target %q is not a distinct local account", payload.Object.ID)
	}
	now := time.Now().UTC()
	var changed bool
	var afterBlockCleanup bool
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Block
		err := tx.Where("account_id = ? AND target_account_id = ?", actor.ID, target.ID).First(&existing).Error
		if err == nil {
			if activityIDPresent && string(existing.URI) != activityID {
				changed = true
				return tx.Model(&existing).Updates(map[string]any{"uri": activityID, "updated_at": now}).Error
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if activityID != "" {
			deleteArrivedFirst, err := s.activityPubDeleteArrivedFirst(actor, activityID)
			if err != nil {
				return err
			}
			if deleteArrivedFirst {
				changed = true
				return cleanupActivityPubBlockRelationships(tx, actor.ID, target.ID)
			}
		}
		block, err := s.createActivityPubAccountBlock(tx, actor.ID, target.ID, now, activityID, generateBlockURI)
		if err != nil {
			return err
		}
		if block != nil && activityID != "" && string(block.URI) != activityID {
			if err := tx.Model(block).Updates(map[string]any{"uri": activityID, "updated_at": now}).Error; err != nil {
				return err
			}
			block.URI = models.NullSafeString(activityID)
		}
		afterBlockCleanup = true
		changed = true
		return nil
	}); err != nil {
		return err
	}
	if changed {
		if afterBlockCleanup {
			s.clearAfterBlockFeedCaches(context.Background(), actor.ID, target.ID)
		}
		s.invalidateBlockRelationshipCaches(context.Background(), actor.ID, target.ID)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), actor.ID, target.ID)
	}
	return nil
}

func (s *Server) createActivityPubAccountBlock(tx *gorm.DB, accountID int64, targetID int64, now time.Time, uri string, generateURI bool) (*models.Block, error) {
	block := models.Block{CreatedAt: now, UpdatedAt: now, AccountID: accountID, TargetAccountID: targetID, URI: models.NullSafeString(uri)}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&block).Error; err != nil {
		return nil, err
	}
	if block.ID == 0 {
		if err := tx.Where("account_id = ? AND target_account_id = ?", accountID, targetID).First(&block).Error; err != nil {
			return nil, err
		}
	}
	if block.URI == "" && generateURI {
		block.URI = models.NullSafeString(activityPubGeneratedPayloadURI(s))
		if err := tx.Model(&block).Updates(map[string]any{"uri": block.URI, "updated_at": now}).Error; err != nil {
			return nil, err
		}
	}
	if err := cleanupActivityPubBlockRelationships(tx, accountID, targetID); err != nil {
		return nil, err
	}
	return &block, nil
}

func cleanupActivityPubBlockRelationships(tx *gorm.DB, accountID int64, targetID int64) error {
	if err := deleteFollowEdge(tx, accountID, targetID); err != nil {
		return err
	}
	if err := deleteFollowEdge(tx, targetID, accountID); err != nil {
		return err
	}
	var requestIDs []int64
	if err := tx.Model(&models.FollowRequest{}).
		Where("account_id = ? AND target_account_id = ?", targetID, accountID).
		Pluck("id", &requestIDs).Error; err != nil {
		return err
	}
	if len(requestIDs) > 0 {
		if err := tx.Where("activity_type = ? AND activity_id IN ?", "FollowRequest", requestIDs).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
	}
	return tx.Where("account_id = ? AND target_account_id = ?", targetID, accountID).Delete(&models.FollowRequest{}).Error
}

func (s *Server) processActivityPubUndoBlock(object activityObject, actor *models.Account) error {
	_, err := s.processActivityPubUndoBlockWithTombstone(object, actor, true)
	return err
}

func (s *Server) processActivityPubUndoBlockWithTombstone(object activityObject, actor *models.Account, tombstoneOnMiss bool) (bool, error) {
	if actor == nil || actor.ID == 0 || actor.Local() {
		return false, nil
	}
	targetURI := activityPubUndoTargetURI(object)
	target, err := s.localAccountFromActivityURI(targetURI)
	if (err != nil || target == nil) && !object.TypePresent {
		blockTarget, blockErr := s.blockTargetFromURI(object.ID, actor.ID)
		if blockErr != nil {
			return false, blockErr
		}
		target = blockTarget
	}
	if target == nil || !target.Local() {
		return false, err
	}
	query := s.db.Where("account_id = ? AND target_account_id = ?", actor.ID, target.ID)
	if object.ID != "" {
		query = s.db.Where("account_id = ? AND (target_account_id = ? OR uri = ?)", actor.ID, target.ID, object.ID)
	}
	res := query.Delete(&models.Block{})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		s.invalidateBlockRelationshipCaches(context.Background(), actor.ID, target.ID)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), actor.ID, target.ID)
		return true, nil
	}
	if !tombstoneOnMiss {
		return false, nil
	}
	return false, s.markActivityPubDeleteUponArrival(actor, object.ID)
}

func (s *Server) blockTargetFromURI(uri string, accountID int64) (*models.Account, error) {
	if s == nil || s.db == nil || uri == "" || accountID == 0 {
		return nil, nil
	}
	var block models.Block
	if err := s.db.Where("account_id = ? AND uri = ?", accountID, uri).First(&block).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var target models.Account
	if err := s.db.Where("id = ?", block.TargetAccountID).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &target, nil
}

type outgoingFollowResponseMatch struct {
	Request         *models.FollowRequest
	Follow          *models.Follow
	SourceAccountID int64
}

func (s *Server) outgoingFollowResponseFromActivity(object activityObject, target *models.Account, deliveredToAccountID int64) (outgoingFollowResponseMatch, error) {
	var match outgoingFollowResponseMatch
	if s == nil || s.db == nil || target == nil || target.ID == 0 {
		return match, activityPubEventNotAppliedf("Follow response target is unavailable")
	}
	if object.TypePresent && object.TypeExact != "Follow" {
		return match, activityPubEventNotAppliedf("Follow response object type is %q", object.TypeExact)
	}
	if object.TypeExact == "Follow" && object.ObjectIDPresent && firstNonEmpty(object.ObjectID, activityURIFromBearcapRaw(object.ObjectIDRaw)) != target.URI {
		return match, activityPubEventNotAppliedf(
			"embedded Follow target %q does not match response actor %q",
			firstNonEmpty(object.ObjectID, object.ObjectIDRaw),
			target.URI,
		)
	}

	embeddedSourceAccountID := int64(0)
	if sourceURI := activityPubEmbeddedFollowActorURI(object); sourceURI != "" {
		source, err := s.localAccountFromActivityURI(sourceURI)
		if err != nil {
			return match, fmt.Errorf("resolve embedded Follow actor %q: %w", sourceURI, err)
		}
		if source == nil || !source.Local() {
			return match, activityPubEventNotAppliedf("embedded Follow actor %q is not a local account", sourceURI)
		}
		embeddedSourceAccountID = source.ID
	}

	deliveredSourceAccountID := int64(0)
	if deliveredToAccountID != 0 {
		var deliveredTo models.Account
		err := s.db.Select("id", "domain").
			Where("id = ? AND domain IS NULL", deliveredToAccountID).
			First(&deliveredTo).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return match, activityPubEventNotAppliedf("personal inbox account %d is not a local account", deliveredToAccountID)
		}
		if err != nil {
			return match, fmt.Errorf("load personal inbox account %d: %w", deliveredToAccountID, err)
		}
		deliveredSourceAccountID = deliveredTo.ID
	}
	if embeddedSourceAccountID != 0 && deliveredSourceAccountID != 0 && embeddedSourceAccountID != deliveredSourceAccountID {
		return match, activityPubEventNotAppliedf(
			"embedded Follow actor account_id=%d conflicts with personal inbox account_id=%d",
			embeddedSourceAccountID,
			deliveredSourceAccountID,
		)
	}
	match.SourceAccountID = firstNonZeroInt64(embeddedSourceAccountID, deliveredSourceAccountID)

	if strings.TrimSpace(object.ID) != "" {
		var request models.FollowRequest
		err := s.db.Preload("Account").
			Where("target_account_id = ? AND uri = ?", target.ID, object.ID).
			First(&request).Error
		if err == nil {
			if match.SourceAccountID != 0 && match.SourceAccountID != request.AccountID {
				return match, activityPubEventNotAppliedf(
					"Follow request source account_id=%d conflicts with response source account_id=%d",
					request.AccountID,
					match.SourceAccountID,
				)
			}
			match.Request = &request
			match.SourceAccountID = request.AccountID
			return match, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return match, err
		}

		var follow models.Follow
		err = s.db.Preload("Account").
			Where("target_account_id = ? AND uri = ?", target.ID, object.ID).
			First(&follow).Error
		if err == nil {
			if match.SourceAccountID != 0 && match.SourceAccountID != follow.AccountID {
				return match, activityPubEventNotAppliedf(
					"Follow source account_id=%d conflicts with response source account_id=%d",
					follow.AccountID,
					match.SourceAccountID,
				)
			}
			match.Follow = &follow
			match.SourceAccountID = follow.AccountID
			return match, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return match, err
		}
	}

	if match.SourceAccountID == 0 {
		request, follow, err := s.uniqueOutgoingFollowResponseCandidate(target.ID)
		if err != nil {
			return match, err
		}
		if request != nil {
			match.Request = request
			match.SourceAccountID = request.AccountID
			return match, nil
		}
		if follow != nil {
			match.Follow = follow
			match.SourceAccountID = follow.AccountID
			return match, nil
		}
		return match, activityPubEventNotAppliedf("Follow response has no resolvable local source account")
	}

	var request models.FollowRequest
	err := s.db.Preload("Account").
		Where("target_account_id = ? AND account_id = ?", target.ID, match.SourceAccountID).
		First(&request).Error
	if err == nil {
		match.Request = &request
		return match, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return match, err
	}

	var follow models.Follow
	err = s.db.Preload("Account").
		Where("target_account_id = ? AND account_id = ?", target.ID, match.SourceAccountID).
		First(&follow).Error
	if err == nil {
		match.Follow = &follow
		return match, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return match, err
	}
	return match, nil
}

func (s *Server) uniqueOutgoingFollowResponseCandidate(targetAccountID int64) (*models.FollowRequest, *models.Follow, error) {
	var requests []models.FollowRequest
	if err := s.db.Preload("Account").
		Where("target_account_id = ?", targetAccountID).
		Order("id ASC").
		Limit(2).
		Find(&requests).Error; err != nil {
		return nil, nil, err
	}
	switch len(requests) {
	case 1:
		return &requests[0], nil, nil
	case 2:
		return nil, nil, activityPubEventNotAppliedf(
			"Follow response is ambiguous for target_account_id=%d: multiple pending requests",
			targetAccountID,
		)
	}

	var follows []models.Follow
	if err := s.db.Preload("Account").
		Where("target_account_id = ?", targetAccountID).
		Order("id ASC").
		Limit(2).
		Find(&follows).Error; err != nil {
		return nil, nil, err
	}
	switch len(follows) {
	case 1:
		return nil, &follows[0], nil
	case 2:
		return nil, nil, activityPubEventNotAppliedf(
			"Follow response is ambiguous for target_account_id=%d: multiple existing follows",
			targetAccountID,
		)
	default:
		return nil, nil, nil
	}
}

func activityPubEmbeddedFollowActorURI(object activityObject) string {
	if object.TypeExact != "Follow" {
		return ""
	}
	return firstNonEmpty(object.Actor, activityURIFromBearcapRaw(object.ActorRaw))
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func (s *Server) acceptOutgoingFollowRequest(request models.FollowRequest) (*models.Follow, bool, []int64, error) {
	now := time.Now().UTC()
	var follow models.Follow
	affectedListIDs := []int64{}
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		follow = models.Follow{
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       request.AccountID,
			TargetAccountID: request.TargetAccountID,
			ShowReblogs:     request.ShowReblogs,
			Notify:          request.Notify,
			Languages:       request.Languages,
			URI:             request.URI,
			Account:         request.Account,
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			if err := tx.Where("account_id = ? AND target_account_id = ?", request.AccountID, request.TargetAccountID).First(&follow).Error; err != nil {
				return err
			}
			if request.URI != "" && follow.URI == "" {
				if err := tx.Model(&follow).Updates(map[string]any{"uri": request.URI, "updated_at": now}).Error; err != nil {
					return err
				}
				follow.URI = request.URI
			}
		} else {
			created = true
			if err := incrementAccountStatCounter(tx, request.AccountID, accountStatCounterFollowing, 1); err != nil {
				return err
			}
			if err := incrementAccountStatCounter(tx, request.TargetAccountID, accountStatCounterFollowers, 1); err != nil {
				return err
			}
		}
		listIDs, err := updateListAccountsForAcceptedFollowRequest(tx, request.ID, follow.ID)
		if err != nil {
			return err
		}
		affectedListIDs = append(affectedListIDs, listIDs...)
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", request.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		return tx.Delete(&request).Error
	})
	if err != nil {
		return nil, false, nil, err
	}
	if follow.Account.ID == 0 {
		follow.Account = request.Account
	}
	return &follow, created, uniqueInt64s(affectedListIDs), nil
}

func (s *Server) deleteOutgoingFollowRequest(request models.FollowRequest) ([]int64, error) {
	affectedListIDs := []int64{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		listIDs, err := deleteListAccountsForRejectedFollowRequest(tx, request.ID)
		if err != nil {
			return err
		}
		affectedListIDs = append(affectedListIDs, listIDs...)
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", request.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		return tx.Delete(&request).Error
	})
	if err != nil {
		return nil, err
	}
	return uniqueInt64s(affectedListIDs), nil
}

func (s *Server) processActivityPubUndoAccept(object activityObject, actor *models.Account) error {
	follow, err := s.outgoingFollowFromUndoAccept(object, actor)
	if err != nil {
		return err
	}
	if follow == nil {
		request, requestErr := s.outgoingFollowRequestFromUndoAccept(object, actor)
		if requestErr != nil {
			return requestErr
		}
		if request != nil {
			// A repeated Undo Accept already restored the outgoing request.
			return nil
		}
		return activityPubEventNotAppliedf("Undo Accept did not match an outgoing Follow")
	}
	if err := s.revokeOutgoingFollowToRequest(*follow); err != nil {
		return err
	}
	s.invalidateFollowRelationshipCaches(context.Background(), follow.Account, actor.ID)
	_ = s.clearHomeFeedCacheContext(context.Background(), follow.AccountID)
	s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), actor.ID)
	return nil
}

func (s *Server) outgoingFollowRequestFromUndoAccept(object activityObject, target *models.Account) (*models.FollowRequest, error) {
	if s == nil || s.db == nil || target == nil || target.ID == 0 {
		return nil, activityPubEventNotAppliedf("Undo Accept target is unavailable")
	}
	query := s.db.Where("target_account_id = ?", target.ID)
	if object.ObjectIDPresent {
		query = query.Where("uri = ?", activityPubUndoTargetURI(object))
	} else {
		query = query.Where("uri IS NULL")
	}
	var request models.FollowRequest
	err := query.First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &request, nil
}

func (s *Server) outgoingFollowFromUndoAccept(object activityObject, target *models.Account) (*models.Follow, error) {
	if s == nil || s.db == nil || target == nil || target.ID == 0 {
		return nil, activityPubEventNotAppliedf("Undo Accept target is unavailable")
	}
	query := s.db.Preload("Account").Where("target_account_id = ?", target.ID)
	if object.ObjectIDPresent {
		query = query.Where("uri = ?", activityPubUndoTargetURI(object))
	} else {
		query = query.Where("uri IS NULL")
	}
	var follow models.Follow
	err := query.First(&follow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &follow, nil
}

func (s *Server) revokeOutgoingFollowToRequest(follow models.Follow) error {
	now := time.Now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		request := models.FollowRequest{
			CreatedAt:       now,
			UpdatedAt:       now,
			AccountID:       follow.AccountID,
			TargetAccountID: follow.TargetAccountID,
			ShowReblogs:     follow.ShowReblogs,
			Notify:          follow.Notify,
			Languages:       follow.Languages,
			URI:             follow.URI,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&request).Error; err != nil {
			return err
		}
		return deleteFollow(tx, follow)
	})
}

func (s *Server) processActivityPubMove(payload activityPayload, actor *models.Account) error {
	if actor == nil || actor.ID == 0 || actor.Local() {
		return activityPubEventNotAppliedf("Move actor must be a persisted remote account")
	}
	sourceURI := actor.URI
	if payload.Object.ID != sourceURI || payload.Target == "" {
		return activityPubEventNotAppliedf("Move source or target is invalid")
	}
	processing, err := s.markActivityPubMoveAsProcessing(context.Background(), actor.ID)
	if err != nil {
		return err
	}
	if !processing {
		return activityPubEventNotAppliedf("Move for account_id=%d is already being processed", actor.ID)
	}
	target, err := s.activityActorForMoveTargetURI(payload.Target)
	if err != nil {
		s.unmarkActivityPubMoveAsProcessing(context.Background(), actor.ID)
		return err
	}
	if target == nil || target.ID == 0 || target.SuspendedAt.Valid {
		s.unmarkActivityPubMoveAsProcessing(context.Background(), actor.ID)
		return activityPubEventNotAppliedf("Move target %q is unavailable", payload.Target)
	}
	if !stringArrayContains(target.AlsoKnownAs, sourceURI) {
		s.unmarkActivityPubMoveAsProcessing(context.Background(), actor.ID)
		return activityPubEventNotAppliedf("Move target does not reference source in alsoKnownAs")
	}
	if !(actor.MovedToAccountID.Valid && actor.MovedToAccountID.Int64 == target.ID) {
		if err := s.setMovedToAccount(actor.ID, sql.NullInt64{Int64: target.ID, Valid: true}); err != nil {
			s.unmarkActivityPubMoveAsProcessing(context.Background(), actor.ID)
			return err
		}
		actor.MovedToAccountID = sql.NullInt64{Int64: target.ID, Valid: true}
	}
	if err := s.processLocalAccountMigration(models.AccountMigration{Account: *actor, TargetAccount: *target}); err != nil {
		s.unmarkActivityPubMoveAsProcessing(context.Background(), actor.ID)
		return err
	}
	return nil
}

const activityPubMoveProcessingCooldown = 7 * 24 * time.Hour
const activityPubRedisLockDefaultTTL = 15 * time.Minute

func (s *Server) activityActorForMoveTargetURI(actorURI string) (*models.Account, error) {
	if s == nil || s.db == nil || strings.TrimSpace(actorURI) == "" {
		return nil, activityPubEventNotAppliedf("Move target URI is missing")
	}
	if s.localActivityURI(actorURI) {
		return s.localAccountFromActivityURI(actorURI)
	}
	if disallowed, err := s.remoteActivityDomainNotAllowed(actorURI); err != nil || disallowed {
		return nil, err
	}
	actor, err := s.fetchActivityActor(actorURI)
	if err != nil {
		return nil, err
	}
	if actor.ID == "" || actor.PublicKey.PublicKeyPem == "" {
		return nil, activityPubEventNotAppliedf("Move target actor is missing id or public key")
	}
	if err := verifyRemoteActivityActorWebFinger(actor); err != nil {
		return nil, err
	}
	return s.upsertRemoteActivityActorForRequest(actor, "")
}

func activityPubMoveProcessingKey(prefix string, accountID int64) string {
	return prefix + "move_in_progress:" + strconv.FormatInt(accountID, 10)
}

const activityPubDeleteUponArrivalTTL = 6 * time.Hour

func activityPubDeleteUponArrivalKey(prefix string, accountID int64, uri string) string {
	return prefix + "delete_upon_arrival:" + strconv.FormatInt(accountID, 10) + ":" + uri
}

func activityPubRedisLockKey(prefix string, name string) string {
	return prefix + "lock:" + name
}

func (s *Server) markActivityPubDeleteUponArrival(actor *models.Account, uri string) error {
	if s == nil || actor == nil || actor.ID == 0 {
		return activityPubEventNotAppliedf("Delete arrival marker is missing a persisted actor")
	}
	if strings.TrimSpace(uri) == "" {
		return activityPubEventNotAppliedf("Delete arrival marker is missing an activity URI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	key := activityPubDeleteUponArrivalKey(redisConfig(s.cfg).prefix, actor.ID, uri)
	_, err := s.redisCommand(ctx, "SETEX", key, strconv.FormatInt(int64(activityPubDeleteUponArrivalTTL/time.Second), 10), "true")
	return err
}

func (s *Server) activityPubDeleteArrivedFirst(actor *models.Account, uri string) (bool, error) {
	if s == nil || actor == nil || actor.ID == 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	key := activityPubDeleteUponArrivalKey(redisConfig(s.cfg).prefix, actor.ID, uri)
	value, err := s.redisCommand(ctx, "EXISTS", key)
	if err != nil {
		return false, err
	}
	switch typed := value.(type) {
	case int64:
		return typed > 0, nil
	case int:
		return typed > 0, nil
	case string:
		return typed != "" && typed != "0", nil
	case []byte:
		return len(typed) > 0 && string(typed) != "0", nil
	default:
		return false, nil
	}
}

func (s *Server) acquireActivityPubRedisLock(ctx context.Context, name string, ttl time.Duration) (bool, func(), error) {
	release := func() {}
	name = strings.TrimSpace(name)
	if s == nil || name == "" {
		return true, release, nil
	}
	if !redisEndpointConfigured(s.cfg) {
		return true, release, nil
	}
	if ttl <= 0 {
		ttl = activityPubRedisLockDefaultTTL
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	key := activityPubRedisLockKey(redisConfig(s.cfg).prefix, name)
	value, err := s.redisCommand(ctx, "SET", key, "true", "NX", "EX", strconv.FormatInt(int64(ttl/time.Second), 10))
	if err != nil {
		return false, release, err
	}
	if !strings.EqualFold(activityPubRedisString(value), "OK") {
		return false, release, nil
	}
	release = func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer releaseCancel()
		_, _ = s.redisCommand(releaseCtx, "DEL", key)
	}
	return true, release, nil
}

func (s *Server) markActivityPubMoveAsProcessing(ctx context.Context, accountID int64) (bool, error) {
	if s == nil || accountID == 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	value, err := s.redisCommand(ctx, "SET", activityPubMoveProcessingKey(redisConfig(s.cfg).prefix, accountID), "true", "NX", "EX", strconv.FormatInt(int64(activityPubMoveProcessingCooldown/time.Second), 10))
	if err != nil {
		return false, err
	}
	return strings.EqualFold(activityPubRedisString(value), "OK"), nil
}

func activityPubRedisString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func (s *Server) unmarkActivityPubMoveAsProcessing(ctx context.Context, accountID int64) {
	if s == nil || accountID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, _ = s.redisCommand(ctx, "DEL", activityPubMoveProcessingKey(redisConfig(s.cfg).prefix, accountID))
}

func (s *Server) processActivityPubCreateEncryptedMessage(payload activityPayload, actor *models.Account, target *models.Account) error {
	object := payload.Object
	if target == nil || target.ID == 0 || object.TargetDeviceID == "" {
		return activityPubEventNotAppliedf("EncryptedMessage target account or device is missing")
	}
	if activityPubURIHostMismatch(actor.URI, object.ID) {
		return activityPubEventNotAppliedf("EncryptedMessage object host does not match actor")
	}
	now := time.Now().UTC()
	var targetDevice models.Device
	if err := s.db.Where("account_id = ? AND device_id = ?", target.ID, object.TargetDeviceID).First(&targetDevice).Error; err != nil {
		return fmt.Errorf("load EncryptedMessage target device: %w", err)
	}
	messageFranking, err := s.cryptoMessageFrankingWithOriginal(actor.ID, target.ID, object.DigestValue, object.MessageFranking, now)
	if err != nil {
		return err
	}
	message := models.EncryptedMessage{
		DeviceID:        models.EncryptedMessageDeviceID(targetDevice.ID),
		FromAccountID:   models.EncryptedMessageFromAccountID(actor.ID),
		FromDeviceID:    object.SourceDeviceID,
		MessageType:     object.MessageType,
		Body:            object.CipherText,
		Digest:          object.DigestValue,
		MessageFranking: messageFranking,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.db.Create(&message).Error; err != nil {
		return err
	}
	s.publishEncryptedMessage(message, targetDevice)
	return nil
}

func (s *Server) processActivityPubCreateNote(payload activityPayload, actor *models.Account, deliveredTo *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	note := payload.Object
	if !activityNoteBelongsToActor(note, actor) {
		return activityPubEventNotAppliedf("Create object does not belong to verified actor")
	}
	if !activityObjectIsStatus(note) {
		return activityPubEventNotAppliedf("Create object is not a supported status")
	}
	if activityPubURIHostMismatch(actor.URI, note.ID) {
		return activityPubEventNotAppliedf("Create object host does not match actor")
	}
	tombstoneExists, err := activityPubTombstoneExists(s.db, note.ID)
	if err != nil || tombstoneExists {
		return err
	}
	if !payload.Fetch {
		related, err := s.activityPubCreateRelatedToLocalActivity(note, actor, deliveredTo, relayedThrough)
		if err != nil || !related {
			return err
		}
	}
	acquired, releaseCreateLock, err := s.acquireActivityPubRedisLock(context.Background(), "create:"+note.ID, activityPubRedisLockDefaultTTL)
	if err != nil {
		return err
	}
	if !acquired {
		return activityPubEventNotAppliedf("Create object %q is already being processed", note.ID)
	}
	defer releaseCreateLock()
	deleteArrivedFirst, err := s.activityPubDeleteArrivedFirst(actor, note.ID)
	if err != nil || deleteArrivedFirst {
		return err
	}
	now := time.Now().UTC()
	handled, err := s.processActivityPubPollVote(note, actor, now)
	if errors.Is(err, errActivityPubPollVoteAlreadyVoted) {
		return nil
	}
	if err != nil || handled {
		return err
	}
	status := s.activityStatusFromNote(note, actor, now, true)
	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	var conversationIDs []int64
	var affectedTagIDs []int64
	var customEmojiChanges []models.CustomEmoji
	var createdStatusID int64
	var deliveredStatus *models.Status
	postprocessedDeliveredStatus := false
	addDeliveredStatusToHomeFeed := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := setActivityPubTransactionLockTimeout(tx); err != nil {
			return err
		}
		tombstoneExists, err := activityPubTombstoneExists(tx, note.ID)
		if err != nil || tombstoneExists {
			return err
		}
		var existing models.Status
		existingQuery := tx.Where("account_id = ? AND uri = ?", actor.ID, note.ID)
		if activityPubRailsPresentString(note.AtomURI) {
			existingQuery = tx.Where("account_id = ? AND (uri = ? OR uri = ?)", actor.ID, note.ID, note.AtomURI)
		}
		err = existingQuery.First(&existing).Error
		if err == nil {
			postprocessed, addToHome, err := s.postprocessActivityPubExistingStatusAudience(tx, &existing, actor, deliveredTo, now)
			if err != nil {
				return err
			}
			deliveredStatus = &existing
			postprocessedDeliveredStatus = postprocessed
			addDeliveredStatusToHomeFeed = addToHome
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := s.ensureActivityPubStatusConversation(tx, &status, note, now); err != nil {
			return err
		}
		if err := s.ensureStatusConversation(tx, &status, now); err != nil {
			return err
		}
		prepareActivityPubFetchedStatusID(&status, payload.Fetch, now)
		if err := tx.Omit(clause.Associations).Create(&status).Error; err != nil {
			return err
		}
		createdStatusID = status.ID
		if err := tx.Create(&models.StatusStat{StatusID: status.ID}).Error; err != nil {
			return err
		}
		notifications, err := s.saveActivityPubStatusMetadata(tx, &status, note, actor, deliveredTo, now, false)
		if err != nil {
			return err
		}
		notificationIDs = append(notificationIDs, notifications.NotificationIDs...)
		notificationPayloads = append(notificationPayloads, notifications.NotificationPayloads...)
		customEmojiChanges = append(customEmojiChanges, notifications.CustomEmojiChanges...)
		updatedConversationIDs, err := s.addDirectStatusToConversations(tx, status, nil)
		if err != nil {
			return err
		}
		conversationIDs = append(conversationIDs, updatedConversationIDs...)
		tagIDs, err := activityPubStatusTagIDs(tx, status.ID)
		if err != nil {
			return err
		}
		affectedTagIDs = append(affectedTagIDs, tagIDs...)
		if err := refreshFeaturedTagStatsForStatusTags(tx, actor.ID, status.Visibility, tagIDs, now); err != nil {
			return err
		}
		if status.InReplyToID.Valid && statusCountsTowardReplyStats(status.Visibility) {
			if err := incrementStatusStatCounter(tx, status.InReplyToID.Int64, statusStatCounterReplies, 1); err != nil {
				return err
			}
		}
		return upsertAccountStatForStatus(tx, actor.ID, status.Visibility, now)
	}); err != nil {
		return err
	}
	s.invalidateCustomEmojiEntityCaches(context.Background(), customEmojiChanges)
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(context.Background(), notificationPayloads)
	if err != nil {
		return err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	s.publishConversationIDs(context.Background(), conversationIDs)
	s.publishNotificationIDs(notificationIDs)
	if postprocessedDeliveredStatus && deliveredStatus != nil {
		s.meiliIndexStatusBestEffort(context.Background(), deliveredStatus.ID)
	}
	if addDeliveredStatusToHomeFeed && deliveredStatus != nil && deliveredTo != nil {
		s.addActivityPubDeliveredStatusToHomeFeedBestEffort(context.Background(), *deliveredStatus, *deliveredTo)
	}
	if createdStatusID != 0 {
		quoteSigner, _ := s.activityPubSignedFetchAccount(actor, deliveredTo, payload.To, payload.CC)
		s.processActivityPubQuoteBestEffort(context.Background(), createdStatusID, note, payload.ID, quoteSigner)
		s.resolveActivityPubThreadBestEffort(status, note, payload.ID)
		s.fetchActivityPubRepliesBestEffort(status, note, actor)
		s.meiliIndexStatusBestEffort(context.Background(), createdStatusID)
		s.meiliIndexTagsBestEffort(context.Background(), affectedTagIDs)
		s.fetchLinkCardForStatusDelayed(createdStatusID)
		if options.OverrideTimestamps || activityPubStatusWithinRealtimeWindow(status, now) {
			if created, err := s.findStatus(strconv.FormatInt(createdStatusID, 10)); err == nil {
				_ = s.fanOutStatusToLocalRecipientsSkipNotifications(context.Background(), s.db, *created)
				s.publishStatusUpdateEvent("update", *created)
			}
		}
		_ = s.forwardActivityPubCreateReply(*actor, status, payload.RawBody)
		if !actor.SilencedAt.Valid {
			s.recordTagTrendUse(context.Background(), actor.ID, status.Visibility, affectedTagIDs, status.CreatedAt)
		}
	}
	return nil
}

func (s *Server) postprocessActivityPubExistingStatusAudience(tx *gorm.DB, status *models.Status, actor *models.Account, deliveredTo *models.Account, now time.Time) (bool, bool, error) {
	if status == nil || actor == nil || deliveredTo == nil || status.ID == 0 || actor.ID == 0 || deliveredTo.ID == 0 {
		return false, false, nil
	}
	var mentionCount int64
	if err := tx.Model(&models.Mention{}).Where("status_id = ? AND account_id = ?", status.ID, deliveredTo.ID).Count(&mentionCount).Error; err != nil {
		return false, false, err
	}
	if mentionCount > 0 {
		return false, false, nil
	}
	mention := models.Mention{StatusID: models.MentionStatusID(status.ID), AccountID: models.MentionAccountID(deliveredTo.ID), Silent: true, CreatedAt: now, UpdatedAt: now}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mention)
	if res.Error != nil {
		return false, false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, false, nil
	}
	if status.Visibility == 3 {
		if err := tx.Model(&models.Status{}).Where("id = ?", status.ID).Updates(map[string]any{"visibility": 4, "updated_at": now}).Error; err != nil {
			return false, false, err
		}
		status.Visibility = 4
	}
	var followCount int64
	if err := tx.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", deliveredTo.ID, actor.ID).Count(&followCount).Error; err != nil {
		return false, false, err
	}
	return true, followCount > 0, nil
}

func prepareActivityPubFetchedStatusID(status *models.Status, fetched bool, now time.Time) {
	if status == nil || !fetched || status.ID != 0 || status.CreatedAt.IsZero() || !status.CreatedAt.Before(now) {
		return
	}
	status.ID = mastodonSnowflakeIDAt(status.CreatedAt, true)
}

func (s *Server) addActivityPubDeliveredStatusToHomeFeedBestEffort(ctx context.Context, status models.Status, deliveredTo models.Account) {
	if s == nil || s.db == nil || deliveredTo.ID == 0 || status.ID == 0 {
		return
	}
	var user models.User
	aggregateReblogs := true
	if err := s.db.WithContext(ctx).Select("settings").Where("account_id = ?", deliveredTo.ID).First(&user).Error; err == nil {
		aggregateReblogs = aggregateReblogsFromSettings(user.Settings)
	}
	if s.enqueueFeedInsertTask(status.ID, "home", deliveredTo.ID, aggregateReblogs) {
		return
	}
	_, _ = s.addStatusToFeedContext(ctx, "home", deliveredTo.ID, status, aggregateReblogs)
	_ = s.trimFeedContext(ctx, "home", deliveredTo.ID)
}

func (s *Server) processActivityPubQuoteBestEffort(ctx context.Context, statusID int64, note activityObject, requestID string, signer *models.Account) {
	if s == nil || s.db == nil || s.quoteStore == nil || statusID == 0 {
		return
	}
	quoteURL := activityPubQuoteURL(note)
	if quoteURL == "" {
		return
	}
	quoteActorURI, ok := s.activityPubQuoteObjectAttributedTo(quoteURL, signer)
	if !ok {
		return
	}
	if !s.fetchActivityPubQuoteActorForRequest(quoteActorURI, requestID) {
		return
	}
	quoteLookupURL := activityURIFromBearcap(quoteURL)
	quote, err := s.statusFromActivityURI(quoteLookupURL)
	if err != nil || quote == nil {
		quote, err = s.fetchRemoteStatusFromActivityURIForRequestWithSigner(quoteURL, "", requestID, signer)
	}
	if err != nil || quote == nil || quote.ID == 0 || quote.ID == statusID {
		return
	}
	s.putStatusQuoteMetadataBestEffort(ctx, statusID, quote.ID, quoteURL, s.quoteStatusURL(*quote))
}

func (s *Server) activityPubQuoteObjectAttributedTo(quoteURL string, signer *models.Account) (string, bool) {
	if s == nil || strings.TrimSpace(quoteURL) == "" {
		return "", false
	}
	signer = s.activityFetchSigner(signer)
	resource, err := fetchActivityResourceWithMetadataAndUserAgentSignedWithAccept(quoteURL, paonUserAgent(s.cfg), s, signer, activityDereferencerAcceptHeader)
	if err != nil {
		if status, ok := activityFetchStatus(err); ok && activityDereferencerHTTPStatusIgnoredLikeRails(status) {
			return "", false
		}
		return "", false
	}
	var raw map[string]any
	if err := json.Unmarshal(resource.body, &raw); err != nil || raw == nil {
		return "", false
	}
	if activityJSONLDIDRaw(raw) != activityPubFetchExpectedID(quoteURL) {
		return "", false
	}
	actorURI := activityJSONLDValueOrID(activityJSONLDValue(raw, "attributedTo"))
	return actorURI, strings.TrimSpace(actorURI) != ""
}

func (s *Server) fetchActivityPubQuoteActorForRequest(actorURI string, requestID string) bool {
	if s == nil || s.db == nil || strings.TrimSpace(actorURI) == "" {
		return false
	}
	actor, err := s.fetchActivityActor(actorURI)
	if err != nil || actor.ID == "" {
		return false
	}
	account, err := s.upsertRemoteActivityActorForRequest(actor, requestID)
	return err == nil && account != nil && account.ID != 0
}

func activityPubQuoteURL(note activityObject) string {
	for _, value := range []struct {
		raw        string
		normalized string
		present    bool
	}{
		{raw: note.QuoteURIRaw, normalized: note.QuoteURI, present: note.QuoteURISet},
		{raw: note.QuoteURLRaw, normalized: note.QuoteURL, present: note.QuoteURLSet},
		{raw: note.MisskeyQuoteRaw, normalized: note.MisskeyQuote, present: note.MisskeyQuoteSet},
	} {
		if !value.present {
			continue
		}
		quoteURL := value.raw
		if quoteURL == "" {
			quoteURL = value.normalized
		}
		if strings.TrimSpace(quoteURL) == "" {
			return ""
		}
		return quoteURL
	}
	return ""
}

const activityPubStatusRealtimeWindow = 6 * time.Hour

func activityPubStatusWithinRealtimeWindow(status models.Status, now time.Time) bool {
	return !status.CreatedAt.Before(now.Add(-activityPubStatusRealtimeWindow))
}

func (s *Server) resolveActivityPubThreadBestEffort(child models.Status, note activityObject, requestID string) {
	parentURI := activityPubHTTPURI(note.InReplyTo)
	if s == nil || child.ID == 0 || child.InReplyToID.Valid || parentURI == "" {
		return
	}
	if s.enqueueThreadResolveTask(child.ID, parentURI, requestID) {
		return
	}
	go func() {
		_ = s.resolveActivityPubThread(child.ID, parentURI, requestID)
	}()
}

func (s *Server) resolveActivityPubThread(childID int64, parentURI string, requestID string) error {
	parentURI = activityPubHTTPURI(parentURI)
	if s == nil || s.db == nil || childID == 0 || parentURI == "" {
		return nil
	}
	parent, err := s.statusFromActivityURI(parentURI)
	if err != nil {
		return err
	}
	if parent == nil {
		parent, err = s.fetchRemoteStatusFromResolvableURLForRequest(parentURI, requestID)
		if err != nil {
			return err
		}
	}
	if parent == nil || parent.ID == 0 {
		return nil
	}
	now := time.Now().UTC()
	var child models.Status
	updated := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", childID).First(&child).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if child.InReplyToID.Valid {
			return nil
		}
		parent, err = s.railsStatusReplyTarget(parent)
		if err != nil {
			return err
		}
		parentAccountID := railsStatusReplyAccountID(child.AccountID, parent)
		if err := tx.Model(&models.Status{}).Where("id = ?", child.ID).Updates(map[string]any{
			"in_reply_to_id":         parent.ID,
			"in_reply_to_account_id": parentAccountID,
			"reply":                  true,
			"updated_at":             now,
		}).Error; err != nil {
			return err
		}
		if statusCountsTowardReplyStats(child.Visibility) {
			if err := incrementStatusStatCounter(tx, parent.ID, statusStatCounterReplies, 1); err != nil {
				return err
			}
		}
		child.InReplyToID = sql.NullInt64{Int64: parent.ID, Valid: true}
		child.InReplyToAccountID = parentAccountID
		child.Reply = true
		child.UpdatedAt = now
		updated = true
		return nil
	}); err != nil {
		return err
	}
	if !updated {
		return nil
	}
	s.meiliIndexStatusBestEffort(context.Background(), child.ID)
	if activityPubStatusWithinRealtimeWindow(child, now) {
		if refreshed, err := s.findStatus(strconv.FormatInt(child.ID, 10)); err == nil {
			_ = s.fanOutStatusToLocalRecipientsSkipNotifications(context.Background(), s.db, *refreshed)
			s.publishStatusUpdateEvent("update", *refreshed)
		}
	}
	return nil
}

func activityPubHTTPURI(raw string) string {
	return activityPubHTTPURIRaw(raw)
}

func activityPubDereferenceFetchURI(raw string) (string, string) {
	if strings.TrimSpace(raw) == "" {
		return "", ""
	}
	if strings.HasPrefix(raw, "bear:") {
		objectURI := activityPubHTTPURIRaw(activityURIFromBearcap(raw))
		if objectURI == "" {
			return "", ""
		}
		return raw, objectURI
	}
	objectURI := activityPubHTTPURIRaw(raw)
	return objectURI, objectURI
}

func activityPubHTTPURIRaw(raw string) string {
	if !activityPubHTTPURIAllowedRaw(raw) {
		return ""
	}
	return raw
}

func (s *Server) processActivityPubPollVote(note activityObject, actor *models.Account, now time.Time) (bool, error) {
	if activityPubReplyURI(note) == "" || strings.TrimSpace(note.Name) == "" {
		return false, nil
	}
	repliedTo, err := s.statusFromActivityURI(activityPubReplyURI(note))
	if err == nil && repliedTo == nil && activityPubRailsPresentString(note.InReplyToAtomURI) {
		repliedTo, err = s.statusFromActivityURI(note.InReplyToAtomURI)
	}
	if err != nil || repliedTo == nil {
		return false, err
	}
	if !repliedTo.Account.Local() {
		return false, nil
	}
	var poll models.Poll
	err = s.db.Where("status_id = ?", repliedTo.ID).First(&poll).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	choice := activityPollVoteChoice(poll, note.Name)
	if choice < 0 {
		return false, nil
	}
	if poll.ExpiresAt.Valid && poll.ExpiresAt.Time.Before(now) {
		return true, nil
	}

	voteLockName := "vote:" + strconv.FormatInt(poll.ID, 10) + ":" + strconv.FormatInt(actor.ID, 10)
	acquired, releaseVoteLock, err := s.acquireActivityPubRedisLock(context.Background(), voteLockName, activityPubRedisLockDefaultTTL)
	if err != nil {
		return true, err
	}
	if !acquired {
		return true, activityPubEventNotAppliedf("poll vote for poll_id=%d account_id=%d is already being processed", poll.ID, actor.ID)
	}
	defer releaseVoteLock()

	created := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing []models.PollVote
		if err := tx.Where("poll_id = ? AND account_id = ?", poll.ID, actor.ID).Find(&existing).Error; err != nil {
			return err
		}
		if !poll.Multiple && len(existing) > 0 {
			return errActivityPubPollVoteAlreadyVoted
		}
		for _, vote := range existing {
			if vote.Choice == choice {
				return errActivityPubPollVoteAlreadyVoted
			}
		}

		vote := models.PollVote{AccountID: models.PollVoteAccountID(actor.ID), PollID: models.PollVotePollID(poll.ID), Choice: choice, URI: models.NullSafeString(note.ID), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&vote).Error; err != nil {
			return err
		}
		for len(poll.CachedTallies) <= choice {
			poll.CachedTallies = append(poll.CachedTallies, 0)
		}
		poll.CachedTallies[choice]++
		poll.VotesCount++
		updates := map[string]any{
			"cached_tallies": poll.CachedTallies,
			"votes_count":    poll.VotesCount,
			"updated_at":     now,
		}
		if len(existing) == 0 && poll.VotersCount.Valid {
			poll.VotersCount.Int64++
			updates["voters_count"] = poll.VotersCount.Int64
		}
		if err := tx.Model(&models.Poll{}).Where("id = ?", poll.ID).Updates(updates).Error; err != nil {
			return err
		}
		created = true
		return nil
	}); err != nil {
		return true, err
	}
	if created {
		if poll.StatusID.Valid {
			s.invalidateStatusCache(context.Background(), poll.StatusID.Int64)
		}
		if !poll.HideTotals {
			s.enqueueActivityPubPollUpdateForPoll(poll, railsPollUpdateDelay)
		}
	}
	return true, nil
}

func (s *Server) processActivityPubUpdate(payload activityPayload, actor *models.Account, deliveredTo *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	object := payload.Object
	if payload.ObjectReference && object.ID != "" {
		return s.processActivityPubDereferencedUpdate(payload, actor, deliveredTo, relayedThrough, options)
	}
	if actorType := activityActorTypeValue(object.Types); actorType != "" {
		object.Type = actorType
		if object.ID == "" || actor.URI == "" || object.ID != actor.URI {
			return activityPubEventNotAppliedf("Update actor object does not match verified actor")
		}
		if object.Inbox == "" || !activityPubHTTPURIAllowedRaw(object.ID) {
			return activityPubEventNotAppliedf("Update actor object is missing a valid id or inbox")
		}
		if disallowed, err := s.remoteActivityDomainNotAllowed(object.ID); err != nil || disallowed {
			return err
		}
		return s.updateActivityPubActor(actor, object, options.RequestID)
	}
	if activityObjectIsConvertedStatus(object) {
		return nil
	}
	if activityObjectIsStatus(object) {
		if !activityNoteBelongsToActor(object, actor) {
			return activityPubEventNotAppliedf("Update status does not belong to verified actor")
		}
		if activityPubURIHostMismatch(actor.URI, object.ID) {
			return activityPubEventNotAppliedf("Update status host does not match actor")
		}
		var status models.Status
		statusQuery := s.db.Where("uri = ? AND account_id = ?", object.ID, actor.ID)
		if activityPubRailsPresentString(object.AtomURI) {
			statusQuery = s.db.Where("account_id = ? AND (uri = ? OR uri = ?)", actor.ID, object.ID, object.AtomURI)
		}
		err := statusQuery.First(&status).Error
		statusMissing := errors.Is(err, gorm.ErrRecordNotFound)
		if activityPubUpdateShouldIgnoreUnknownObject(statusMissing, object, time.Now().UTC()) {
			return nil
		}
		if statusMissing {
			return s.processActivityPubCreateNote(payload, actor, deliveredTo, relayedThrough, options)
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		editedAt := activityPubStatusUpdateEditedAt(object)
		if activityPubStatusUpdateIsOlder(status, editedAt) {
			return nil
		}
		locked, releaseLock, err := s.acquireActivityPubRedisLock(context.Background(), "create:"+object.ID, activityPubRedisLockDefaultTTL)
		if err != nil {
			return err
		}
		if !locked {
			return activityPubEventNotAppliedf("Update object %q is already being processed", object.ID)
		}
		defer releaseLock()
		if !activityPubStatusUpdateIsExplicit(status, editedAt) {
			return s.processActivityPubImplicitStatusUpdate(&status, object, actor, now)
		}
		next := s.activityStatusFromNote(object, actor, now, false)
		var notificationIDs []int64
		var notificationPayloads []asynqLocalNotificationPayload
		var affectedTagIDs []int64
		var customEmojiChanges []models.CustomEmoji
		significantChanged := activityPubStatusSignificantChange(status, next, object)
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			metadataChanged, err := s.activityPubStatusMetadataSignificantChange(tx, &status, object, actor, now)
			if err != nil {
				return err
			}
			significantChanged = significantChanged || metadataChanged
			if significantChanged {
				var editCount int64
				if err := tx.Model(&models.StatusEdit{}).Where("status_id = ?", status.ID).Count(&editCount).Error; err != nil {
					return err
				}
				if editCount == 0 {
					previousStatus, err := loadStatusForSnapshot(tx, status.ID)
					if err != nil {
						return err
					}
					previous := statusSnapshotEdit(*previousStatus)
					if err := tx.Omit("Status", "Account", "OrderedMediaAttachments").Create(&previous).Error; err != nil {
						return err
					}
				}
			}
			updates := map[string]any{
				"text":         next.Text,
				"updated_at":   now,
				"sensitive":    next.Sensitive,
				"spoiler_text": next.SpoilerText,
				"language":     next.Language,
			}
			if significantChanged {
				updates["edited_at"] = editedAt
			}
			if err := tx.Model(&models.Status{}).Where("id = ?", status.ID).Updates(updates).Error; err != nil {
				return err
			}
			if significantChanged {
				if err := deleteStatusPreviewCardLinks(tx, status.ID); err != nil {
					return err
				}
			}
			status.Text = next.Text
			status.Sensitive = next.Sensitive
			status.SpoilerText = next.SpoilerText
			status.Language = next.Language
			oldTagIDs, err := activityPubStatusTagIDs(tx, status.ID)
			if err != nil {
				return err
			}
			notifications, err := s.replaceActivityPubStatusMetadata(tx, &status, object, actor, now)
			if err != nil {
				return err
			}
			notificationIDs = append(notificationIDs, notifications.NotificationIDs...)
			notificationPayloads = append(notificationPayloads, notifications.NotificationPayloads...)
			customEmojiChanges = append(customEmojiChanges, notifications.CustomEmojiChanges...)
			newTagIDs, err := activityPubStatusTagIDs(tx, status.ID)
			if err != nil {
				return err
			}
			affectedTagIDs = append(append(affectedTagIDs, oldTagIDs...), newTagIDs...)
			if err := refreshFeaturedTagStatsForStatusTags(tx, actor.ID, status.Visibility, affectedTagIDs, now); err != nil {
				return err
			}
			if significantChanged {
				updated, err := loadStatusForSnapshot(tx, status.ID)
				if err != nil {
					return err
				}
				current := statusSnapshotEdit(*updated)
				current.AccountID = sql.NullInt64{Int64: actor.ID, Valid: true}
				current.CreatedAt = now
				current.UpdatedAt = now
				if err := tx.Omit("Status", "Account", "OrderedMediaAttachments").Create(&current).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		s.invalidateCustomEmojiEntityCaches(context.Background(), customEmojiChanges)
		createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(context.Background(), notificationPayloads)
		if err != nil {
			return err
		}
		notificationIDs = append(notificationIDs, createdNotificationIDs...)
		s.publishNotificationIDs(notificationIDs)
		if significantChanged {
			s.invalidateStatusCache(context.Background(), status.ID)
			if updated, err := s.findStatus(strconv.FormatInt(status.ID, 10)); err == nil {
				_ = s.fanOutStatusUpdateToLocalRecipients(context.Background(), s.db, *updated)
				s.publishStatusUpdateEvent("status.update", *updated)
			}
			if activityPubStatusUpdateShouldForward(status, editedAt) {
				_ = s.forwardActivityPubStatusActivity(*actor, status, payload.RawBody)
			}
		}
		s.meiliIndexStatusBestEffort(context.Background(), status.ID)
		s.meiliIndexTagsBestEffort(context.Background(), affectedTagIDs)
		if significantChanged {
			s.fetchLinkCardForStatusDelayed(status.ID)
		}
		return nil
	}
	return activityPubEventNotAppliedf("Update object type %q is unsupported", object.TypeExact)
}

// Mastodon 4.2.28 ignores old Update activities only when their object is not
// already known, preventing old remote posts from being delivered as new.
const activityPubUpdateUnknownObjectAgeThreshold = 24 * time.Hour

func activityPubUpdateShouldIgnoreUnknownObject(statusMissing bool, object activityObject, now time.Time) bool {
	if !statusMissing {
		return false
	}
	publishedAt, ok := parseActivityPubTime(object.Published)
	return ok && publishedAt.Before(now.UTC().Add(-activityPubUpdateUnknownObjectAgeThreshold))
}

func (s *Server) processActivityPubDereferencedUpdate(payload activityPayload, actor *models.Account, deliveredTo *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	objectFetchURI, objectURI := activityPubDereferenceFetchURI(payload.Object.ID)
	if s == nil || actor == nil || objectFetchURI == "" || objectURI == "" {
		return activityPubEventNotAppliedf("dereferenced Update has no fetchable object")
	}
	if activityPubURIHostMismatch(actor.URI, objectURI) {
		return activityPubEventNotAppliedf("dereferenced Update object host does not match actor")
	}
	if disallowed, err := s.remoteActivityDomainNotAllowed(objectURI); err != nil || disallowed {
		return err
	}
	signer, err := s.activityPubSignedFetchAccount(actor, deliveredTo, payload.To, payload.CC)
	if err != nil {
		return err
	}
	object, err := s.fetchActivityObjectForUpdateWithFetchURI(objectFetchURI, objectURI, paonUserAgent(s.cfg), signer)
	if err != nil {
		return err
	}
	if object.ID == "" {
		return activityPubEventNotAppliedf("dereferenced Update object is missing id")
	}
	dereferenced := activityPubDereferencedUpdatePayload(payload, object)
	return s.processActivityPubUpdate(dereferenced, actor, deliveredTo, relayedThrough, options)
}

func activityPubDereferencedUpdatePayload(payload activityPayload, object activityObject) activityPayload {
	payload.Object = object
	payload.ObjectReference = false
	return payload
}

func (s *Server) fetchActivityObjectForUpdate(uri string, userAgent string, signer *models.Account) (activityObject, error) {
	return s.fetchActivityObjectForUpdateWithFetchURI(uri, uri, userAgent, signer)
}

func (s *Server) fetchActivityObjectForUpdateWithFetchURI(fetchURI string, expectedURI string, userAgent string, signer *models.Account) (activityObject, error) {
	payload, err := s.fetchActivityDereferencerPayloadStrictWithExpectedID(fetchURI, expectedURI, userAgent, signer)
	if err == nil && activityObjectIsStatus(payload.Object) {
		return payload.Object, nil
	}
	if err == nil && payload.Type == "" && payload.Object.ID == "" {
		return activityObject{}, nil
	}
	resource, err := fetchActivityResourceWithMetadataAndUserAgentSignedWithAccept(fetchURI, userAgent, s, signer, activityDereferencerAcceptHeader)
	if err != nil {
		return activityObject{}, err
	}
	if !activityJSONContentType(resource.contentType) {
		return activityObject{}, fmt.Errorf("unsupported activity content type")
	}
	var raw map[string]any
	if err := json.Unmarshal(resource.body, &raw); err != nil {
		return activityObject{}, err
	}
	if !activityResourceSupportedContext(raw["@context"]) {
		return activityObject{}, fmt.Errorf("unsupported activity context")
	}
	object := parseActivityObject(raw)
	if object.ID == "" {
		return activityObject{}, fmt.Errorf("activity object id is missing")
	}
	if object.ID != expectedURI {
		return activityObject{}, fmt.Errorf("activity object id mismatch")
	}
	return object, nil
}

func (s *Server) activityPubSignedFetchAccount(actor *models.Account, deliveredTo *models.Account, to []string, cc []string) (*models.Account, error) {
	if deliveredTo != nil && deliveredTo.Local() && deliveredTo.PrivateKey.Valid && strings.TrimSpace(deliveredTo.PrivateKey.String) != "" {
		return deliveredTo, nil
	}
	account, err := s.firstActivityPubLocalAudienceAccount(append(append([]string{}, to...), cc...))
	if err != nil || account != nil {
		return account, err
	}
	return s.firstActivityPubLocalFollower(actor)
}

func (s *Server) firstActivityPubLocalAudienceAccount(audience []string) (*models.Account, error) {
	if s == nil || s.db == nil || len(audience) == 0 {
		return nil, nil
	}
	usernames := make([]string, 0, len(audience))
	seen := map[string]struct{}{}
	for _, uri := range audience {
		username := s.localUsernameFromActivityURI(uri)
		if username == "" {
			continue
		}
		if _, ok := seen[username]; ok {
			continue
		}
		seen[username] = struct{}{}
		usernames = append(usernames, username)
	}
	if len(usernames) == 0 {
		return nil, nil
	}
	var account models.Account
	err := s.db.Where("username IN ? AND domain IS NULL AND private_key IS NOT NULL AND private_key <> ''", usernames).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &account, err
}

func (s *Server) firstActivityPubLocalFollower(actor *models.Account) (*models.Account, error) {
	if s == nil || s.db == nil || actor == nil || actor.ID == 0 {
		return nil, nil
	}
	var account models.Account
	err := s.db.Model(&models.Account{}).
		Select("accounts.*").
		Joins("JOIN follows ON follows.account_id = accounts.id").
		Where("follows.target_account_id = ?", actor.ID).
		Where("accounts.domain IS NULL AND accounts.private_key IS NOT NULL AND accounts.private_key <> ''").
		Order("follows.id ASC").
		First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &account, err
}

func (s *Server) ensureActivityPubStatusConversation(tx *gorm.DB, status *models.Status, note activityObject, now time.Time) error {
	if s == nil || tx == nil || status == nil || status.ConversationID.Valid {
		return nil
	}
	if !note.ConversationSet {
		return nil
	}
	uri := note.Conversation
	if id, ok := s.activityPubLocalConversationID(uri); ok {
		var conversation models.Conversation
		err := tx.First(&conversation, id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		status.ConversationID = sql.NullInt64{Int64: conversation.ID, Valid: true}
		return nil
	}
	conversation := models.Conversation{
		URI:       sql.NullString{String: uri, Valid: true},
		CreatedAt: now,
		UpdatedAt: now,
	}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&conversation)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 || conversation.ID == 0 {
		if err := tx.Where("uri = ?", uri).First(&conversation).Error; err != nil {
			return err
		}
	}
	status.ConversationID = sql.NullInt64{Int64: conversation.ID, Valid: true}
	return nil
}

var activityPubConversationTagRe = regexp.MustCompile(`^tag:([^,]+),\d{4}-\d{2}-\d{2}:objectId=([0-9]+):objectType=Conversation$`)

func (s *Server) activityPubLocalConversationID(uri string) (int64, bool) {
	matches := activityPubConversationTagRe.FindStringSubmatch(uri)
	if len(matches) != 3 || s == nil {
		return 0, false
	}
	domain := matches[1]
	if !strings.EqualFold(domain, s.cfg.LocalDomain) && !strings.EqualFold(domain, s.cfg.WebDomain) {
		return 0, false
	}
	id, err := strconv.ParseInt(matches[2], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func (s *Server) activityPubCreateRelatedToLocalActivity(note activityObject, actor *models.Account, deliveredTo *models.Account, relayedThrough *models.Account) (bool, error) {
	if actor == nil || actor.ID == 0 || s == nil {
		return false, nil
	}
	if deliveredTo != nil && deliveredTo.ID != 0 && deliveredTo.Local() {
		return true, nil
	}
	if s.db == nil {
		return false, nil
	}
	followed, err := s.activityPubActorOrRelayFollowedByLocalAccount(actor, relayedThrough)
	if err != nil || followed {
		return followed, err
	}
	relay, err := s.activityPubActorOrRelayRequestedThroughRelay(actor, relayedThrough)
	if err != nil || relay {
		return relay, err
	}
	replyRelated, err := s.activityPubRespondsToFollowedAccount(note)
	if err != nil || replyRelated {
		return replyRelated, err
	}
	return s.activityPubAddressesLocalAccounts(note)
}

func (s *Server) activityPubActorFollowedByLocalAccount(accountID int64) (bool, error) {
	var count int64
	err := s.db.Model(&models.Follow{}).
		Where("follows.target_account_id = ?", accountID).
		Count(&count).Error
	return count > 0, err
}

func (s *Server) activityPubActorOrRelayFollowedByLocalAccount(actor *models.Account, relayedThrough *models.Account) (bool, error) {
	for _, account := range []*models.Account{actor, relayedThrough} {
		if account == nil || account.ID == 0 {
			continue
		}
		followed, err := s.activityPubActorFollowedByLocalAccount(account.ID)
		if err != nil || followed {
			return followed, err
		}
	}
	return false, nil
}

func (s *Server) activityPubRequestedThroughRelay(actor models.Account) (bool, error) {
	inboxURL := actor.InboxURL
	var count int64
	err := s.db.Model(&models.Relay{}).Where("state = ? AND inbox_url = ?", relayStateAccepted, inboxURL).Count(&count).Error
	return count > 0, err
}

func (s *Server) activityPubActorOrRelayRequestedThroughRelay(actor *models.Account, relayedThrough *models.Account) (bool, error) {
	if relayedThrough == nil || relayedThrough.ID == 0 {
		return false, nil
	}
	return s.activityPubRequestedThroughRelay(*relayedThrough)
}

func (s *Server) activityPubAnnounceRequestedThroughRelay(actor models.Account) (bool, error) {
	if relay, err := s.activityPubRequestedThroughRelay(actor); err != nil || relay {
		return relay, err
	}
	inboxURL := actor.InboxURL
	var count int64
	err := s.db.Model(&models.Relay{}).Where("state = ? AND inbox_url = ?", relayStateAccepted, inboxURL).Count(&count).Error
	return count > 0, err
}

func (s *Server) activityPubAnnounceActorOrRelayRequestedThroughRelay(actor *models.Account, relayedThrough *models.Account) (bool, error) {
	for _, account := range []*models.Account{relayedThrough, actor} {
		if account == nil || account.ID == 0 {
			continue
		}
		relay, err := s.activityPubAnnounceRequestedThroughRelay(*account)
		if err != nil || relay {
			return relay, err
		}
	}
	return false, nil
}

func (s *Server) activityPubRespondsToFollowedAccount(note activityObject) (bool, error) {
	if strings.TrimSpace(activityPubReplyURI(note)) == "" {
		return false, nil
	}
	reply, err := s.statusFromActivityURI(activityPubReplyURI(note))
	if err == nil && reply == nil && activityPubRailsPresentString(note.InReplyToAtomURI) {
		reply, err = s.statusFromActivityURI(note.InReplyToAtomURI)
	}
	if err != nil || reply == nil {
		return false, err
	}
	if reply.Account.Local() {
		return true, nil
	}
	return s.activityPubActorFollowedByLocalAccount(reply.AccountID)
}

func (s *Server) activityPubAddressesLocalAccounts(note activityObject) (bool, error) {
	audience := append([]string{}, note.To...)
	audience = append(audience, note.CC...)
	seen := map[string]struct{}{}
	usernames := []string{}
	for _, uri := range audience {
		username := s.localUsernameFromActivityURI(uri)
		if username == "" {
			continue
		}
		if _, ok := seen[username]; ok {
			continue
		}
		seen[username] = struct{}{}
		usernames = append(usernames, username)
	}
	if len(usernames) == 0 {
		return false, nil
	}
	var count int64
	err := s.db.Model(&models.Account{}).
		Where("accounts.username IN ? AND accounts.domain IS NULL", usernames).
		Count(&count).Error
	return count > 0, err
}

func activityPubStatusSignificantChange(current models.Status, next models.Status, _ activityObject) bool {
	return activityPubStatusTextSignificantlyChanged(current.Text, next.Text) || current.SpoilerText != next.SpoilerText
}

func activityPubStatusTextSignificantlyChanged(current string, next string) bool {
	return activityPubComparableStatusText(current) != activityPubComparableStatusText(next)
}

func activityPubComparableStatusText(value string) string {
	return activityPubPlainText(value)
}

func (s *Server) activityPubStatusMetadataSignificantChange(tx *gorm.DB, status *models.Status, object activityObject, actor *models.Account, now time.Time) (bool, error) {
	mediaChanged, err := s.activityPubMediaAttachmentsSignificantChange(tx, status, object.Attachments)
	if err != nil || mediaChanged {
		return mediaChanged, err
	}
	return s.activityPubPollSignificantChange(tx, status, object, actor, now)
}

func (s *Server) activityPubMediaAttachmentsSignificantChange(tx *gorm.DB, status *models.Status, attachments []activityAttachment) (bool, error) {
	if tx == nil || status == nil || status.ID == 0 {
		return false, nil
	}
	var previous []models.MediaAttachment
	if err := tx.Where("status_id = ?", status.ID).Order("id ASC").Find(&previous).Error; err != nil {
		return false, err
	}
	previousByURL := make(map[string]models.MediaAttachment, len(previous))
	previousIDs := status.OrderedMediaAttachmentIDs
	if previousIDs == nil {
		previousIDs = make(models.Int64Array, 0, len(previous))
		for _, media := range previous {
			previousIDs = append(previousIDs, media.ID)
		}
	}
	for _, media := range previous {
		previousByURL[media.RemoteURL] = media
	}
	nextIDs := make(models.Int64Array, 0, len(attachments))
	changed := false
	accepted := 0
	maxAttachments := s.maxMediaAttachments()
	for _, attachment := range attachments {
		if attachment.URL == "" {
			continue
		}
		if accepted > maxAttachments {
			break
		}
		accepted++
		media, ok := previousByURL[attachment.URL]
		if !ok {
			changed = true
			continue
		}
		nextIDs = append(nextIDs, media.ID)
		if activityPubMediaAttachmentSignificantlyChanges(media, attachment) {
			changed = true
		}
	}
	return changed || !sameInt64Array(previousIDs, nextIDs), nil
}

func activityPubMediaAttachmentSignificantlyChanges(previous models.MediaAttachment, attachment activityAttachment) bool {
	if previous.RemoteURL != attachment.URL {
		return true
	}
	if activityPubNullStringValue(previous.ThumbnailRemoteURL) != attachment.IconURL {
		return true
	}
	return activityPubNullStringValue(previous.Description) != attachment.Description
}

func activityPubNullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func (s *Server) activityPubPollSignificantChange(tx *gorm.DB, status *models.Status, object activityObject, actor *models.Account, now time.Time) (bool, error) {
	if tx == nil || status == nil || status.ID == 0 || actor == nil {
		return false, nil
	}
	parsed, ok := activityPollFromObject(object, actor.ID, status.ID, now)
	var previous models.Poll
	err := tx.Where("status_id = ?", status.ID).First(&previous).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ok, nil
	}
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	return !sameStringArray(previous.Options, parsed.Options) || previous.Multiple != parsed.Multiple, nil
}

func activityPubStatusUpdateEditedAt(object activityObject) sql.NullTime {
	if object.Updated == "" {
		return sql.NullTime{}
	}
	parsed, ok := parseActivityPubTime(object.Updated)
	if !ok {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: parsed, Valid: true}
}

func activityPubStatusUpdateIsExplicit(status models.Status, editedAt sql.NullTime) bool {
	return editedAt.Valid && (!status.EditedAt.Valid || editedAt.Time.After(status.EditedAt.Time))
}

func activityPubStatusUpdateIsOlder(status models.Status, editedAt sql.NullTime) bool {
	return status.EditedAt.Valid && (!editedAt.Valid || editedAt.Time.Before(status.EditedAt.Time))
}

func activityPubStatusUpdateShouldForward(status models.Status, editedAt sql.NullTime) bool {
	if !editedAt.Valid {
		return false
	}
	lastEditDate := status.CreatedAt
	if status.EditedAt.Valid {
		lastEditDate = status.EditedAt.Time
	}
	return editedAt.Time.After(lastEditDate)
}

func (s *Server) processActivityPubImplicitStatusUpdate(status *models.Status, object activityObject, actor *models.Account, now time.Time) error {
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return s.syncActivityPubPollAllowSignificantChange(tx, status, object, actor, now, false)
	}); err != nil {
		return err
	}
	s.invalidateStatusCache(context.Background(), status.ID)
	return nil
}

func (s *Server) processActivityPubDelete(payload activityPayload, actor *models.Account) error {
	return s.processActivityPubDeleteWithContext(context.Background(), payload, actor)
}

func (s *Server) processActivityPubDeleteWithContext(ctx context.Context, payload activityPayload, actor *models.Account) error {
	target := payload.Object.ID
	if target == "" && !payload.Object.Reference {
		return activityPubEventNotAppliedf("Delete target is missing")
	}
	now := time.Now().UTC()
	if actor.URI != "" && target == actor.URI {
		acquired, releaseDeleteLock, err := s.acquireActivityPubRedisLock(ctx, "delete_in_progress:"+strconv.FormatInt(actor.ID, 10), 2*time.Hour)
		if err != nil {
			return err
		}
		if !acquired {
			return activityPubEventNotAppliedf("actor Delete for account_id=%d is already being processed", actor.ID)
		}
		defer releaseDeleteLock()
		if actor.Local() {
			if err := s.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", actor.ID).Updates(map[string]any{"suspended_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
		} else {
			if err := s.suspendRemoteActivityPubActor(ctx, actor, now); err != nil {
				return err
			}
			if s.asynqClient != nil {
				if err := s.enqueueAccountDeletionTaskContext(ctx, actor.ID); err != nil {
					return fmt.Errorf("enqueue remote actor deletion account_id=%d domain=%q: %w", actor.ID, actor.Domain.String, err)
				}
			} else if err := s.purgeAccountDeletionRequest(ctx, actor.ID, now); err != nil {
				return err
			}
		}
		s.meiliIndexAccountBestEffort(ctx, actor.ID)
		return nil
	}
	acquired, releaseDeleteLock, err := s.acquireActivityPubRedisLock(ctx, "delete_status_in_progress:"+target, activityPubRedisLockDefaultTTL)
	if err != nil {
		return err
	}
	if !acquired {
		return activityPubEventNotAppliedf("status Delete for %q is already being processed", target)
	}
	defer releaseDeleteLock()
	var status models.Status
	var deletedReblogs []models.Status
	var removedFavourites []models.Favourite
	var removedBookmarks []models.Bookmark
	var removedStatusPins []models.StatusPin
	var affectedTagIDs []int64
	var forwardPlan *activityPubForwardingPlan
	deleted := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if activityPubURIHostsMatch(actor.URI, target) {
			acquiredCreate, releaseCreateLock, err := s.acquireActivityPubRedisLock(ctx, "create:"+target, activityPubRedisLockDefaultTTL)
			if err != nil {
				return err
			}
			if !acquiredCreate {
				return activityPubEventNotAppliedf("status %q is already being created", target)
			}
			defer releaseCreateLock()
			if err := createActivityPubTombstone(tx, actor.ID, target, now); err != nil {
				return err
			}
		}
		statusQuery := tx.Where("account_id = ? AND uri = ?", actor.ID, target)
		if activityPubRailsPresentString(payload.Object.AtomURI) {
			statusQuery = tx.Where("account_id = ? AND (uri = ? OR uri = ?)", actor.ID, target, payload.Object.AtomURI)
		}
		err := statusQuery.First(&status).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if status.DeletedAt.Valid {
			return nil
		}
		plan, err := s.prepareForwardActivityPubStatusActivity(*actor, status, payload.RawBody)
		if err != nil {
			return err
		}
		forwardPlan = plan
		tagIDs, err := activityPubStatusTagIDs(tx, status.ID)
		if err != nil {
			return err
		}
		affectedTagIDs = append(affectedTagIDs, tagIDs...)
		if err := unlinkDirectStatusFromConversations(ctx, tx, status, now); err != nil {
			return err
		}
		if err := tx.Model(&models.Status{}).Where("id = ?", status.ID).Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Select("account_id, status_id").Where("status_id = ?", status.ID).Find(&removedStatusPins).Error; err != nil {
			return err
		}
		if err := tx.Where("status_id = ?", status.ID).Delete(&models.StatusPin{}).Error; err != nil {
			return err
		}
		if err := tx.Where("activity_type = ? AND activity_id = ?", "Status", status.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		favourites, bookmarks, err := deleteActivityPubStatusJoinRows(tx, []int64{status.ID})
		if err != nil {
			return err
		}
		removedFavourites = append(removedFavourites, favourites...)
		removedBookmarks = append(removedBookmarks, bookmarks...)
		if status.InReplyToID.Valid && statusCountsTowardReplyStats(status.Visibility) {
			if err := decrementStatusStatCounter(tx, status.InReplyToID.Int64, statusStatCounterReplies, 1); err != nil {
				return err
			}
		}
		if status.ReblogOfID.Valid {
			if err := decrementStatusStatCounter(tx, status.ReblogOfID.Int64, statusStatCounterReblogs, 1); err != nil {
				return err
			}
		}
		if err := decrementAccountStatForStatus(tx, actor.ID, status.Visibility); err != nil {
			return err
		}
		if err := refreshFeaturedTagStatsForStatusTags(tx, actor.ID, status.Visibility, tagIDs, now); err != nil {
			return err
		}
		reblogs, favourites, bookmarks, pins, err := deleteActivityPubReblogsForRemovedStatus(tx, status.ID, now)
		if err != nil {
			return err
		}
		deletedReblogs = append(deletedReblogs, reblogs...)
		removedFavourites = append(removedFavourites, favourites...)
		removedBookmarks = append(removedBookmarks, bookmarks...)
		removedStatusPins = append(removedStatusPins, pins...)
		if err := s.deleteActivityPubStatusMetadata(tx, status.ID); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	if err != nil {
		return err
	}
	if deleted {
		if forwardPlan != nil {
			_ = s.deliverForwardedActivityPubStatusActivity(*forwardPlan, payload.RawBody)
		}
		_ = s.removeStatusFromRailsFeeds(context.Background(), s.db, status)
		s.publishStatusDelete(status)
		s.deleteStatusQuoteBestEffort(context.Background(), status.ID)
		s.meiliDeleteStatusBestEffort(context.Background(), status.ID)
		for _, reblog := range deletedReblogs {
			_ = s.removeStatusFromRailsFeeds(context.Background(), s.db, reblog)
			s.publishStatusDelete(reblog)
			s.deleteStatusQuoteBestEffort(context.Background(), reblog.ID)
			s.meiliDeleteStatusBestEffort(context.Background(), reblog.ID)
		}
		for _, favourite := range removedFavourites {
			s.runFavouriteDestroyedSideEffects(context.Background(), favourite)
		}
		for _, bookmark := range removedBookmarks {
			s.runBookmarkDestroyedSideEffects(context.Background(), bookmark)
		}
		for _, pin := range removedStatusPins {
			s.runStatusPinDestroyedSideEffects(context.Background(), pin)
		}
		s.meiliIndexTagsBestEffort(context.Background(), affectedTagIDs)
	}
	return nil
}

func deleteActivityPubReblogsForRemovedStatus(tx *gorm.DB, statusID int64, now time.Time) ([]models.Status, []models.Favourite, []models.Bookmark, []models.StatusPin, error) {
	if tx == nil || statusID == 0 {
		return nil, nil, nil, nil, nil
	}
	var reblogs []models.Status
	if err := tx.Select("id, account_id, visibility").
		Where("reblog_of_id = ? AND deleted_at IS NULL", statusID).
		Find(&reblogs).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	if len(reblogs) == 0 {
		return nil, nil, nil, nil, nil
	}
	reblogIDs := make([]int64, 0, len(reblogs))
	for _, reblog := range reblogs {
		reblogIDs = append(reblogIDs, reblog.ID)
	}
	if err := tx.Where("activity_type = ? AND activity_id IN ?", "Status", reblogIDs).Delete(&models.Notification{}).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	var pins []models.StatusPin
	if err := tx.Select("account_id, status_id").Where("status_id IN ?", reblogIDs).Find(&pins).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	if err := tx.Where("status_id IN ?", reblogIDs).Delete(&models.StatusPin{}).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	favourites, bookmarks, err := deleteActivityPubStatusJoinRows(tx, reblogIDs)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := tx.Where("status_id IN ?", reblogIDs).Delete(&models.MediaAttachment{}).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	if err := tx.Model(&models.Status{}).Where("id IN ?", reblogIDs).Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	if err := decrementStatusStatCounter(tx, statusID, statusStatCounterReblogs, int64(len(reblogs))); err != nil {
		return nil, nil, nil, nil, err
	}
	for _, reblog := range reblogs {
		if err := decrementAccountStatForStatus(tx, reblog.AccountID, reblog.Visibility); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return reblogs, favourites, bookmarks, pins, nil
}

func deleteActivityPubStatusJoinRows(tx *gorm.DB, statusIDs []int64) ([]models.Favourite, []models.Bookmark, error) {
	if tx == nil || len(statusIDs) == 0 {
		return nil, nil, nil
	}
	var favourites []models.Favourite
	if err := tx.Select("id, account_id, status_id").Where("status_id IN ?", statusIDs).Find(&favourites).Error; err != nil {
		return nil, nil, err
	}
	if len(favourites) > 0 {
		favouriteIDs := make([]int64, 0, len(favourites))
		for _, favourite := range favourites {
			favouriteIDs = append(favouriteIDs, favourite.ID)
		}
		if err := tx.Where("activity_type = ? AND activity_id IN ?", "Favourite", favouriteIDs).Delete(&models.Notification{}).Error; err != nil {
			return nil, nil, err
		}
		if err := tx.Where("id IN ?", favouriteIDs).Delete(&models.Favourite{}).Error; err != nil {
			return nil, nil, err
		}
	}
	var bookmarks []models.Bookmark
	if err := tx.Select("account_id, status_id").Where("status_id IN ?", statusIDs).Find(&bookmarks).Error; err != nil {
		return nil, nil, err
	}
	if err := tx.Where("status_id IN ?", statusIDs).Delete(&models.Bookmark{}).Error; err != nil {
		return nil, nil, err
	}
	return favourites, bookmarks, nil
}

func createActivityPubTombstone(tx *gorm.DB, accountID int64, uri string, now time.Time) error {
	if tx == nil || accountID == 0 {
		return nil
	}
	tombstone := models.Tombstone{
		AccountID: sql.NullInt64{Int64: accountID, Valid: true},
		URI:       uri,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return tx.Where("account_id = ? AND uri = ?", accountID, uri).FirstOrCreate(&tombstone).Error
}

func (s *Server) createActivityPubTombstoneForActor(actor *models.Account, uri string) error {
	if s == nil || s.db == nil || actor == nil || actor.ID == 0 {
		return nil
	}
	return createActivityPubTombstone(s.db, actor.ID, uri, time.Now().UTC())
}

func activityPubTombstoneExists(tx *gorm.DB, uri string) (bool, error) {
	if tx == nil {
		return false, nil
	}
	var count int64
	if err := tx.Model(&models.Tombstone{}).Where("uri = ?", uri).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Server) activityStatusFromNote(note activityObject, actor *models.Account, now time.Time, usePublished bool) models.Status {
	createdAt := now
	if usePublished && note.Published != "" {
		if parsed, ok := parseActivityPubTime(note.Published); ok {
			createdAt = parsed.UTC()
		}
	}
	editedAt := activityPubCreateEditedAt(note, createdAt)
	statusText := activityPubStatusParserText(note.Content, note.ContentMapFirstKey, note.ContentMapFirst, note.ContentMap)
	spoilerText := activityPubStatusParserText(note.Summary, note.SummaryMapFirstKey, note.SummaryMapFirst, note.SummaryMap)
	language := activityPubNormalizeLanguage(activityPubStatusParserLanguage(note))
	if activityObjectIsConvertedStatus(note) {
		if strings.TrimSpace(note.Name) == "" {
			note.Name = activityPubLanguageMapFirstValue(note.NameMapFirstKey, note.NameMapFirst, note.NameMap)
		}
		if strings.TrimSpace(note.Summary) == "" {
			note.Summary = activityPubLanguageMapFirstValue(note.SummaryMapFirstKey, note.SummaryMapFirst, note.SummaryMap)
		}
		statusText = activityPubConvertedStatusText(note)
		spoilerText = ""
	}
	status := models.Status{
		URI:                sql.NullString{String: note.ID, Valid: note.ID != ""},
		URL:                sql.NullString{String: firstNonEmpty(note.URL, note.ID), Valid: firstNonEmpty(note.URL, note.ID) != ""},
		Text:               statusText,
		CreatedAt:          createdAt,
		UpdatedAt:          now,
		EditedAt:           editedAt,
		Sensitive:          note.Sensitive || (actor != nil && actor.SensitizedAt.Valid),
		Visibility:         activityPubVisibility(note.To, note.CC, actor),
		SpoilerText:        spoilerText,
		Reply:              note.InReplyToPresent,
		Language:           sql.NullString{String: language, Valid: language != ""},
		Local:              sql.NullBool{Bool: false, Valid: true},
		AccountID:          actor.ID,
		InReplyToID:        sql.NullInt64{},
		InReplyToAccountID: sql.NullInt64{},
	}
	if activityPubReplyURI(note) != "" && s != nil && s.db != nil {
		reply, err := s.statusFromActivityURI(activityPubReplyURI(note))
		if err == nil && reply == nil && activityPubRailsPresentString(note.InReplyToAtomURI) {
			reply, err = s.statusFromActivityURI(note.InReplyToAtomURI)
		}
		if err == nil && reply != nil {
			reply, err = s.railsStatusReplyTarget(reply)
		}
		if err == nil && reply != nil {
			status.InReplyToID = sql.NullInt64{Int64: reply.ID, Valid: true}
			status.InReplyToAccountID = railsStatusReplyAccountID(actor.ID, reply)
		}
	}
	return status
}

func activityPubReplyURI(note activityObject) string {
	return note.InReplyTo
}

func activityPubCreateEditedAt(note activityObject, createdAt time.Time) sql.NullTime {
	if note.Updated == "" {
		return sql.NullTime{}
	}
	updatedAt, ok := parseActivityPubTime(note.Updated)
	if !ok {
		return sql.NullTime{}
	}
	if !createdAt.IsZero() && updatedAt.Equal(createdAt.UTC()) {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: updatedAt, Valid: true}
}

type activityPubNotificationSaveResult struct {
	NotificationIDs      []int64
	NotificationPayloads []asynqLocalNotificationPayload
	CustomEmojiChanges   []models.CustomEmoji
}

func (s *Server) saveActivityPubStatusMetadata(tx *gorm.DB, status *models.Status, note activityObject, actor *models.Account, deliveredTo *models.Account, now time.Time, updateEmptyMediaOrder bool) (activityPubNotificationSaveResult, error) {
	if err := s.saveActivityPubMediaAttachments(tx, status, note.Attachments, actor, now, updateEmptyMediaOrder, false); err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	customEmojiChanges, err := s.saveActivityPubCustomEmojis(tx, actor, note.Tags, now)
	if err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	if err := s.saveActivityPubTags(tx, status.ID, note.Tags, now); err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	audienceAccounts, err := s.activityPubAudienceAccounts(tx, note, deliveredTo)
	if err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	notifications, err := s.saveActivityPubMentionsAndCollect(tx, status.ID, actor.ID, note.Tags, activityPubAudienceAccountIDSet(audienceAccounts), now)
	if err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	if err := s.saveActivityPubAudienceMentions(tx, status, audienceAccounts, now); err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	if err := s.syncActivityPubPoll(tx, status, note, actor, now); err != nil {
		return notifications, err
	}
	notifications.CustomEmojiChanges = append(notifications.CustomEmojiChanges, customEmojiChanges...)
	return notifications, s.hydrateActivityPubStatusCustomEmojis(tx, status, actor)
}

func (s *Server) replaceActivityPubStatusMetadata(tx *gorm.DB, status *models.Status, note activityObject, actor *models.Account, now time.Time) (activityPubNotificationSaveResult, error) {
	if err := s.deleteActivityPubStatusMetadataExceptMediaAndMentions(tx, status.ID); err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	if err := s.saveActivityPubMediaAttachments(tx, status, note.Attachments, actor, now, true, true); err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	customEmojiChanges, err := s.saveActivityPubCustomEmojis(tx, actor, note.Tags, now)
	if err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	if err := s.saveActivityPubTags(tx, status.ID, note.Tags, now); err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	notifications := activityPubNotificationSaveResult{}
	if err := s.updateActivityPubMentions(tx, status.ID, note.Tags, now); err != nil {
		return activityPubNotificationSaveResult{}, err
	}
	if err := s.syncActivityPubPoll(tx, status, note, actor, now); err != nil {
		return notifications, err
	}
	notifications.CustomEmojiChanges = append(notifications.CustomEmojiChanges, customEmojiChanges...)
	return notifications, s.hydrateActivityPubStatusCustomEmojis(tx, status, actor)
}

func (s *Server) saveActivityPubCustomEmojis(tx *gorm.DB, actor *models.Account, tags []activityTag, now time.Time) ([]models.CustomEmoji, error) {
	return s.saveActivityPubCustomEmojisWithMode(tx, actor, tags, now, false)
}

func (s *Server) saveActivityPubActorCustomEmojis(tx *gorm.DB, actor *models.Account, tags []activityTag, now time.Time) ([]models.CustomEmoji, error) {
	return s.saveActivityPubCustomEmojisWithMode(tx, actor, tags, now, true)
}

func (s *Server) saveActivityPubCustomEmojisWithMode(tx *gorm.DB, actor *models.Account, tags []activityTag, now time.Time, includeMixedEmoji bool) ([]models.CustomEmoji, error) {
	if s == nil || tx == nil || actor == nil || !actor.Domain.Valid || strings.TrimSpace(actor.Domain.String) == "" {
		return nil, nil
	}
	skipMedia, err := activityPubActorSkipsMedia(tx, actor)
	if err != nil || skipMedia {
		return nil, err
	}
	domainValue := customEmojiDomainValue(actor.Domain)
	if !domainValue.Valid {
		return nil, nil
	}
	domain := domainValue.String
	changed := make([]models.CustomEmoji, 0)
	for _, item := range tags {
		if includeMixedEmoji {
			if !activityTagHasType(item, "Emoji") {
				continue
			}
		} else if activityTagPrimaryType(item) != "Emoji" {
			continue
		}
		shortcode := activityTagEmojiShortcode(item.Name)
		if shortcode == "" || item.IconURL == "" {
			continue
		}
		var emoji models.CustomEmoji
		err := tx.Where("shortcode = ? AND lower(domain) = ?", shortcode, domain).First(&emoji).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			emoji = models.CustomEmoji{
				Shortcode:       shortcode,
				Domain:          sql.NullString{String: domain, Valid: true},
				URI:             sql.NullString{String: item.ID, Valid: item.ID != ""},
				ImageRemoteURL:  sql.NullString{String: item.IconURL, Valid: true},
				VisibleInPicker: true,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := tx.Create(&emoji).Error; err != nil {
				return nil, err
			}
			changed = append(changed, emoji)
			continue
		}
		if err != nil {
			return nil, err
		}
		updatedAt := activityTagUpdatedAt(item.Updated)
		remoteURLChanged := !emoji.ImageRemoteURL.Valid || emoji.ImageRemoteURL.String != item.IconURL
		updatedRecently := updatedAt.Valid && !updatedAt.Time.Before(emoji.UpdatedAt)
		if !remoteURLChanged && !updatedRecently {
			continue
		}
		if err := tx.Model(&models.CustomEmoji{}).Where("id = ?", emoji.ID).Updates(map[string]any{
			"image_remote_url": item.IconURL,
			"updated_at":       now,
		}).Error; err != nil {
			return nil, err
		}
		emoji.ImageRemoteURL = sql.NullString{String: item.IconURL, Valid: true}
		emoji.UpdatedAt = now
		changed = append(changed, emoji)
	}
	return changed, nil
}

func activityPubActorSkipsMedia(tx *gorm.DB, actor *models.Account) (bool, error) {
	if actor == nil {
		return false, nil
	}
	if actor.SuspendedAt.Valid {
		return true, nil
	}
	return activityPubDomainRejectsMedia(tx, actor.Domain)
}

func activityPubDomainRejectsMedia(tx *gorm.DB, domain sql.NullString) (bool, error) {
	block, err := activityPubDomainBlockForDomain(tx, domain)
	if err != nil || block == nil {
		return false, err
	}
	return block.RejectMedia, nil
}

func activityPubDomainBlockForDomain(tx *gorm.DB, domain sql.NullString) (*models.DomainBlock, error) {
	if tx == nil || !domain.Valid || strings.TrimSpace(domain.String) == "" {
		return nil, nil
	}
	var block models.DomainBlock
	err := tx.Where("lower(domain) IN ?", activityPubDomainRuleVariants(domain.String)).
		Order("char_length(domain) DESC").
		First(&block).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &block, nil
}

func activityPubDomainRuleVariants(raw string) []string {
	parts := strings.Split(strings.ToLower(strings.Trim(strings.TrimSpace(raw), "/")), ".")
	variants := make([]string, 0, len(parts))
	for i := range parts {
		variant := strings.Join(parts[i:], ".")
		if variant != "" {
			variants = append(variants, variant)
		}
	}
	return variants
}

func activityTagEmojiShortcode(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	shortcode := strings.ReplaceAll(name, ":", "")
	if !customEmojiShortcodePattern.MatchString(shortcode) {
		return ""
	}
	return shortcode
}

func activityTagUpdatedAt(raw string) sql.NullTime {
	if raw == "" {
		return sql.NullTime{}
	}
	updatedAt, ok := parseActivityPubTime(raw)
	if !ok {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: updatedAt, Valid: true}
}

func (s *Server) saveActivityPubAudienceMentions(tx *gorm.DB, status *models.Status, accounts []models.Account, now time.Time) error {
	if s == nil || tx == nil || status == nil || status.ID == 0 {
		return nil
	}
	if len(accounts) == 0 {
		return nil
	}
	createdSilentMention := false
	for _, account := range accounts {
		var count int64
		if err := tx.Model(&models.Mention{}).Where("status_id = ? AND account_id = ?", status.ID, account.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		mention := models.Mention{StatusID: models.MentionStatusID(status.ID), AccountID: models.MentionAccountID(account.ID), Silent: true, CreatedAt: now, UpdatedAt: now}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mention)
		if res.Error != nil {
			return res.Error
		}
		createdSilentMention = createdSilentMention || res.RowsAffected > 0
	}
	if createdSilentMention && status.Visibility == 3 {
		if err := tx.Model(&models.Status{}).Where("id = ?", status.ID).Updates(map[string]any{"visibility": 4, "updated_at": now}).Error; err != nil {
			return err
		}
		status.Visibility = 4
	}
	return nil
}

func (s *Server) activityPubAudienceAccounts(tx *gorm.DB, note activityObject, deliveredTo *models.Account) ([]models.Account, error) {
	audience := append([]string{}, note.To...)
	audience = append(audience, note.CC...)
	seen := map[int64]struct{}{}
	accounts := make([]models.Account, 0, len(audience)+1)
	for _, uri := range audience {
		if activityPubPublicCollection(uri) {
			continue
		}
		account, err := s.accountFromActivityURIWithDB(tx, uri)
		if err != nil {
			return nil, err
		}
		if account == nil {
			continue
		}
		if _, ok := seen[account.ID]; ok {
			continue
		}
		seen[account.ID] = struct{}{}
		accounts = append(accounts, *account)
	}
	if deliveredTo != nil && deliveredTo.ID != 0 {
		if _, ok := seen[deliveredTo.ID]; !ok {
			accounts = append(accounts, *deliveredTo)
		}
	}
	return accounts, nil
}

func activityPubAudienceAccountIDSet(accounts []models.Account) map[int64]struct{} {
	out := make(map[int64]struct{}, len(accounts))
	for _, account := range accounts {
		if account.ID != 0 {
			out[account.ID] = struct{}{}
		}
	}
	return out
}

func (s *Server) deleteActivityPubStatusMetadata(tx *gorm.DB, statusID int64) error {
	if err := s.deleteActivityPubStatusMetadataExceptMedia(tx, statusID); err != nil {
		return err
	}
	if err := tx.Where("status_id = ?", statusID).Delete(&models.MediaAttachment{}).Error; err != nil {
		return err
	}
	return tx.Model(&models.Status{}).Where("id = ?", statusID).Updates(map[string]any{"ordered_media_attachment_ids": nil, "poll_id": nil}).Error
}

func (s *Server) deleteActivityPubStatusMetadataExceptMedia(tx *gorm.DB, statusID int64) error {
	var mentions []models.Mention
	if err := tx.Where("status_id = ?", statusID).Find(&mentions).Error; err != nil {
		return err
	}
	for _, mention := range mentions {
		if err := tx.Where("activity_type = ? AND activity_id = ?", "Mention", mention.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("status_id = ?", statusID).Delete(&models.Mention{}).Error; err != nil {
		return err
	}
	return s.deleteActivityPubStatusMetadataExceptMediaAndMentions(tx, statusID)
}

func (s *Server) deleteActivityPubStatusMetadataExceptMediaAndMentions(tx *gorm.DB, statusID int64) error {
	if err := tx.Exec("DELETE FROM statuses_tags WHERE status_id = ?", statusID).Error; err != nil {
		return err
	}
	if err := deleteStatusPollMetadata(tx, statusID); err != nil {
		return err
	}
	return tx.Model(&models.Status{}).Where("id = ?", statusID).Update("poll_id", nil).Error
}

func activityPubStatusTagIDs(tx *gorm.DB, statusID int64) ([]int64, error) {
	var rows []struct {
		ID int64 `gorm:"column:id"`
	}
	err := tx.Table("statuses_tags").Select("tag_id AS id").Where("status_id = ?", statusID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

func deleteStatusPollMetadata(tx *gorm.DB, statusID int64) error {
	var polls []models.Poll
	if err := tx.Where("status_id = ?", statusID).Find(&polls).Error; err != nil {
		return err
	}
	for _, poll := range polls {
		if err := tx.Where("activity_type = ? AND activity_id = ?", "Poll", poll.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Where("poll_id = ?", poll.ID).Delete(&models.PollVote{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.Poll{}, poll.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) syncActivityPubPoll(tx *gorm.DB, status *models.Status, note activityObject, actor *models.Account, now time.Time) error {
	return s.syncActivityPubPollAllowSignificantChange(tx, status, note, actor, now, true)
}

func (s *Server) syncActivityPubPollAllowSignificantChange(tx *gorm.DB, status *models.Status, note activityObject, actor *models.Account, now time.Time, allowSignificantChange bool) error {
	parsed, ok := activityPollFromObject(note, actor.ID, status.ID, now)
	var previous models.Poll
	err := tx.Where("status_id = ?", status.ID).First(&previous).Error
	if !ok {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !allowSignificantChange {
			return nil
		}
		if err := tx.Where("poll_id = ?", previous.ID).Delete(&models.PollVote{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.Poll{}, previous.ID).Error; err != nil {
			return err
		}
		status.PollID = sql.NullInt64{}
		return tx.Model(&models.Status{}).Where("id = ?", status.ID).Update("poll_id", nil).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if !allowSignificantChange {
			return nil
		}
		parsed.LastFetchedAt = sql.NullTime{Time: now, Valid: true}
		if err := tx.Create(&parsed).Error; err != nil {
			return err
		}
		status.PollID = sql.NullInt64{Int64: parsed.ID, Valid: true}
		return tx.Model(&models.Status{}).Where("id = ?", status.ID).Update("poll_id", parsed.ID).Error
	}
	if err != nil {
		return err
	}
	parsed.LastFetchedAt = sql.NullTime{Time: now, Valid: true}
	significantChange := !sameStringArray(previous.Options, parsed.Options) || previous.Multiple != parsed.Multiple
	if significantChange {
		if !allowSignificantChange {
			return nil
		}
		if err := tx.Where("poll_id = ?", previous.ID).Delete(&models.PollVote{}).Error; err != nil {
			return err
		}
		parsed.CachedTallies = make(models.Int64Array, len(parsed.Options))
		parsed.VotesCount = 0
		parsed.VotersCount = sql.NullInt64{Int64: 0, Valid: true}
	}
	updates := map[string]any{
		"options":         parsed.Options,
		"multiple":        parsed.Multiple,
		"expires_at":      parsed.ExpiresAt,
		"voters_count":    parsed.VotersCount,
		"cached_tallies":  parsed.CachedTallies,
		"votes_count":     parsed.VotesCount,
		"last_fetched_at": parsed.LastFetchedAt,
		"updated_at":      now,
	}
	if err := tx.Model(&models.Poll{}).Where("id = ?", previous.ID).Updates(updates).Error; err != nil {
		return err
	}
	if parsed.ExpiresAt.Valid {
		var voteCount int64
		if err := tx.Model(&models.PollVote{}).Where("poll_id = ?", previous.ID).Limit(1).Count(&voteCount).Error; err != nil {
			return err
		}
		if voteCount > 0 {
			if previous.ExpiresAt.Valid && previous.ExpiresAt.Time.After(parsed.ExpiresAt.Time) {
				s.removeScheduledPollExpirationTasks(previous.ID)
			}
			finalCheckPoll := previous
			finalCheckPoll.ExpiresAt = parsed.ExpiresAt
			s.schedulePollExpirationFinalCheck(&finalCheckPoll)
		}
	}
	status.PollID = sql.NullInt64{Int64: previous.ID, Valid: true}
	return tx.Model(&models.Status{}).Where("id = ?", status.ID).Update("poll_id", previous.ID).Error
}

func (s *Server) saveActivityPubMediaAttachments(tx *gorm.DB, status *models.Status, attachments []activityAttachment, actor *models.Account, now time.Time, updateEmptyOrder bool, railsUpdateLimit bool) error {
	orderedIDs := make(models.Int64Array, 0, len(attachments))
	accepted := 0
	maxAttachments := s.maxMediaAttachments()
	skipRemoteMedia, err := activityPubActorSkipsMedia(tx, actor)
	if err != nil {
		return err
	}
	for _, attachment := range attachments {
		if attachment.URL == "" {
			continue
		}
		if railsUpdateLimit {
			if accepted > maxAttachments {
				break
			}
		} else if accepted >= maxAttachments {
			break
		}
		accepted++
		media, created, err := s.findOrCreateActivityPubMediaAttachment(tx, status, attachment, now)
		if err != nil {
			return err
		}
		if !created {
			updates := activityPubApplyMediaAttachmentUpdates(media, status, attachment, now)
			if err := tx.Model(media).Updates(updates).Error; err != nil {
				return err
			}
		}
		if !skipRemoteMedia {
			s.cacheRemoteMediaAttachment(tx, media, now)
		}
		orderedIDs = append(orderedIDs, media.ID)
	}
	if len(orderedIDs) == 0 && !updateEmptyOrder {
		return nil
	}
	status.OrderedMediaAttachmentIDs = orderedIDs
	return tx.Model(&models.Status{}).Where("id = ?", status.ID).Update("ordered_media_attachment_ids", orderedIDs).Error
}

func activityPubApplyMediaAttachmentUpdates(media *models.MediaAttachment, status *models.Status, attachment activityAttachment, now time.Time) map[string]any {
	statusID := sql.NullInt64{Int64: status.ID, Valid: true}
	accountID := sql.NullInt64{Int64: status.AccountID, Valid: true}
	description := sql.NullString{String: attachment.Description, Valid: attachment.Description != ""}
	blurhash := sql.NullString{String: attachment.Blurhash, Valid: attachment.Blurhash != ""}
	thumbnailRemoteURL := sql.NullString{String: attachment.IconURL, Valid: attachment.IconURL != ""}
	fileContentType := sql.NullString{String: attachment.MediaType, Valid: attachment.MediaType != ""}
	mediaType := activityMediaType(attachment.MediaType)

	media.StatusID = statusID
	media.UpdatedAt = now
	media.Type = mediaType
	media.AccountID = accountID
	media.Description = description
	media.Blurhash = blurhash
	media.ThumbnailRemoteURL = thumbnailRemoteURL
	media.FileContentType = fileContentType
	media.RemoteURL = attachment.URL

	updates := map[string]any{
		"status_id":            statusID,
		"updated_at":           now,
		"type":                 mediaType,
		"account_id":           accountID,
		"description":          description,
		"blurhash":             blurhash,
		"thumbnail_remote_url": thumbnailRemoteURL,
		"file_content_type":    fileContentType,
		"remote_url":           attachment.URL,
	}
	if meta, ok := activityPubMediaMetaWithFocus(media.FileMeta, attachment.Focus); ok {
		media.FileMeta = meta
		updates["file_meta"] = meta
	}
	return updates
}

func (s *Server) findOrCreateActivityPubMediaAttachment(tx *gorm.DB, status *models.Status, attachment activityAttachment, now time.Time) (*models.MediaAttachment, bool, error) {
	var media models.MediaAttachment
	err := tx.Where("status_id = ? AND remote_url = ?", status.ID, attachment.URL).First(&media).Error
	if err == nil {
		return &media, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	mediaType := activityMediaType(attachment.MediaType)
	if s != nil && s.cfg.DisableRemoteMediaCache {
		if detected, ok := remoteMediaAttachmentTypeFromHead(context.Background(), attachment.URL); ok {
			mediaType = detected
		}
	}
	media = models.MediaAttachment{
		StatusID:           sql.NullInt64{Int64: status.ID, Valid: true},
		CreatedAt:          now,
		UpdatedAt:          now,
		RemoteURL:          attachment.URL,
		Type:               mediaType,
		AccountID:          sql.NullInt64{Int64: status.AccountID, Valid: true},
		Description:        sql.NullString{String: attachment.Description, Valid: attachment.Description != ""},
		Blurhash:           sql.NullString{String: attachment.Blurhash, Valid: attachment.Blurhash != ""},
		ThumbnailRemoteURL: sql.NullString{String: attachment.IconURL, Valid: attachment.IconURL != ""},
		FileContentType:    sql.NullString{String: attachment.MediaType, Valid: attachment.MediaType != ""},
		Processing:         activityPubRemoteMediaInitialProcessing(),
	}
	if meta, ok := activityPubMediaMetaWithFocus(nil, attachment.Focus); ok {
		media.FileMeta = meta
	}
	if err := tx.Create(&media).Error; err != nil {
		return nil, false, err
	}
	return &media, true, nil
}

func activityPubRemoteMediaInitialProcessing() sql.NullInt64 {
	return sql.NullInt64{Int64: 2, Valid: true}
}

func (s *Server) saveActivityPubTags(tx *gorm.DB, statusID int64, tags []activityTag, now time.Time) error {
	for _, item := range tags {
		if activityTagPrimaryType(item) != "Hashtag" {
			continue
		}
		normalized, display, ok := normalizeActivityHashtag(item.Name)
		if !ok {
			continue
		}
		tag, err := findOrCreateStatusTag(tx, normalized, display, now)
		if err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO statuses_tags (status_id, tag_id) VALUES (?, ?) ON CONFLICT DO NOTHING", statusID, tag.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) saveActivityPubMentions(tx *gorm.DB, statusID int64, actorID int64, tags []activityTag, now time.Time) error {
	_, err := s.saveActivityPubMentionsAndCollect(tx, statusID, actorID, tags, nil, now)
	return err
}

func (s *Server) saveActivityPubMentionsAndCollect(tx *gorm.DB, statusID int64, actorID int64, tags []activityTag, audienceAccountIDs map[int64]struct{}, now time.Time) (activityPubNotificationSaveResult, error) {
	notifications := activityPubNotificationSaveResult{
		NotificationIDs:      make([]int64, 0),
		NotificationPayloads: make([]asynqLocalNotificationPayload, 0),
	}
	for _, item := range tags {
		if activityTagPrimaryType(item) != "Mention" {
			continue
		}
		account, err := s.accountFromActivityPubMention(tx, item)
		if err != nil {
			return notifications, err
		}
		if account == nil {
			continue
		}
		mention := models.Mention{StatusID: models.MentionStatusID(statusID), CreatedAt: now, UpdatedAt: now, AccountID: models.MentionAccountID(account.ID)}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mention)
		if res.Error != nil {
			return notifications, res.Error
		}
		if res.RowsAffected > 0 && activityPubMentionShouldNotify(audienceAccountIDs, account.ID) {
			notifications.NotificationPayloads = append(notifications.NotificationPayloads, asynqLocalNotificationPayload{
				ReceiverAccountID: account.ID,
				FromAccountID:     actorID,
				ActivityID:        mention.ID,
				ActivityType:      "Mention",
				Type:              "mention",
			})
		}
	}
	return notifications, nil
}

func activityPubMentionShouldNotify(audienceAccountIDs map[int64]struct{}, accountID int64) bool {
	if audienceAccountIDs == nil {
		return true
	}
	_, ok := audienceAccountIDs[accountID]
	return ok
}

func (s *Server) updateActivityPubMentions(tx *gorm.DB, statusID int64, tags []activityTag, now time.Time) error {
	var previous []models.Mention
	if err := tx.Where("status_id = ? AND silent = false", statusID).Find(&previous).Error; err != nil {
		return err
	}
	previousByAccount := make(map[int64]models.Mention, len(previous))
	for _, mention := range previous {
		if mention.AccountID.Valid {
			previousByAccount[mention.AccountID.Int64] = mention
		}
	}
	currentIDs := map[int64]struct{}{}
	for _, item := range tags {
		if activityTagPrimaryType(item) != "Mention" {
			continue
		}
		account, err := s.accountFromActivityPubMention(tx, item)
		if err != nil {
			return err
		}
		if account == nil {
			continue
		}
		if mention, ok := previousByAccount[account.ID]; ok {
			currentIDs[mention.ID] = struct{}{}
			continue
		}
		mention := models.Mention{StatusID: models.MentionStatusID(statusID), CreatedAt: now, UpdatedAt: now, AccountID: models.MentionAccountID(account.ID)}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mention)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			currentIDs[mention.ID] = struct{}{}
		}
	}
	removedIDs := make([]int64, 0)
	for _, mention := range previous {
		if _, ok := currentIDs[mention.ID]; !ok {
			removedIDs = append(removedIDs, mention.ID)
		}
	}
	if len(removedIDs) == 0 {
		return nil
	}
	return tx.Model(&models.Mention{}).Where("id IN ?", removedIDs).Updates(map[string]any{"silent": true, "updated_at": now}).Error
}

func (s *Server) accountFromActivityPubMention(tx *gorm.DB, item activityTag) (*models.Account, error) {
	if strings.TrimSpace(item.Href) == "" {
		return nil, nil
	}
	account, err := s.accountFromActivityURIWithDB(tx, item.Href)
	if err != nil {
		return nil, err
	}
	if account != nil {
		s.scheduleActivityPubMentionRefreshIfStale(account)
		return account, err
	}
	return s.fetchActivityPubMentionAccountByHref(tx, item.Href)
}

func (s *Server) scheduleActivityPubMentionRefreshIfStale(account *models.Account) {
	s.scheduleActivityPubActorRefreshIfStale(account, "")
}

func (s *Server) fetchActivityPubMentionAccountByHref(tx *gorm.DB, href string) (*models.Account, error) {
	actorURI := activityPubHTTPURI(href)
	if s == nil || tx == nil || actorURI == "" {
		return nil, activityPubEventNotAppliedf("Mention href %q is not a fetchable actor URI", href)
	}
	if disallowed, err := s.remoteActivityDomainNotAllowed(actorURI); err != nil || disallowed {
		return nil, err
	}
	actor, err := s.fetchActivityActor(actorURI)
	if err != nil {
		return nil, err
	}
	if actor.ID == "" {
		return nil, activityPubEventNotAppliedf("Mention actor %q is missing id", actorURI)
	}
	return s.upsertRemoteActivityActorDB(tx, actor)
}

func (s *Server) activityPubMentionRef(item activityTag) statusMentionRef {
	username := s.localUsernameFromActivityURI(item.Href)
	if username == "" {
		username, domain := activityPubMentionNameParts(item.Name)
		return statusMentionRef{Username: username, Domain: domain}
	}
	return statusMentionRef{Username: username}
}

func activityPubMentionNameParts(name string) (string, string) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if name == "" {
		return "", ""
	}
	username, domain, ok := strings.Cut(name, "@")
	if !ok {
		return username, ""
	}
	return username, domain
}

func (s *Server) localUsernameFromMentionName(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if name == "" {
		return ""
	}
	username, domain, ok := strings.Cut(name, "@")
	if !ok {
		return username
	}
	if domain == s.cfg.LocalDomain || domain == s.cfg.WebDomain {
		return username
	}
	return ""
}

func normalizeActivityHashtag(raw string) (string, string, bool) {
	display := activityHashtagDisplayName(raw)
	normalized := railsNormalizeHashtagName(display)
	if !railsValidTagName(normalized) {
		return "", "", false
	}
	return normalized, display, true
}

func activityHashtagDisplayName(raw string) string {
	raw = norm.NFC.String(strings.TrimSpace(raw))
	var out strings.Builder
	for _, r := range raw {
		if activityHashtagDisplayRuneAllowed(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func activityHashtagDisplayRuneAllowed(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '·' || r == '・' || r == '\u200c'
}

func activityMediaType(mediaType string) int {
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return 0
	case strings.HasPrefix(mediaType, "video/"):
		return 2
	case strings.HasPrefix(mediaType, "audio/"):
		return 4
	default:
		return 3
	}
}

func activityNoteBelongsToActor(note activityObject, actor *models.Account) bool {
	if note.ID == "" || actor == nil {
		return false
	}
	if actor.URI != "" && !activityPubURIHostsMatch(actor.URI, note.ID) {
		return false
	}
	return true
}

func activityObjectIsStatus(object activityObject) bool {
	return activityObjectHasType(object, "Note") || activityObjectHasType(object, "Question") || activityObjectIsConvertedStatus(object)
}

func activityObjectIsConvertedStatus(object activityObject) bool {
	for _, typ := range []string{"Image", "Audio", "Video", "Article", "Page", "Event"} {
		if activityObjectHasType(object, typ) {
			return true
		}
	}
	return false
}

func activityObjectHasType(object activityObject, typ string) bool {
	return activityTypesInclude(object.Type, object.Types, typ)
}

func activityTagHasType(tag activityTag, typ string) bool {
	return activityTypesInclude(tag.Type, tag.Types, typ)
}

func activityTagPrimaryType(tag activityTag) string {
	for _, typ := range []string{"Hashtag", "Mention", "Emoji"} {
		if activityTagHasType(tag, typ) {
			return typ
		}
	}
	return ""
}

func activityTypesInclude(primary string, values []string, typ string) bool {
	if primary == typ {
		return true
	}
	for _, value := range values {
		if value == typ {
			return true
		}
	}
	return false
}

func activityPubConvertedStatusText(object activityObject) string {
	parts := make([]string, 0, 3)
	if name := strings.TrimSpace(activityPubPlainText(object.Name)); name != "" {
		parts = append(parts, html.EscapeString(name))
	}
	if summary := strings.TrimSpace(activityPubPlainText(object.Summary)); summary != "" {
		parts = append(parts, html.EscapeString(summary))
	}
	if uri := strings.TrimSpace(firstNonEmpty(object.URL, object.ID)); uri != "" {
		parts = append(parts, activityPubConvertedStatusLink(uri))
	}
	return activityPubConvertedSimpleFormat(parts)
}

func activityPubConvertedSimpleFormat(parts []string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return "<p>" + strings.Join(filtered, "</p><p>") + "</p>"
}

func activityPubConvertedStatusLink(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.String() == "" {
		return html.EscapeString(raw)
	}
	link := parsed.String()
	if strings.Contains(link, "komiflo.com") {
		escaped := html.EscapeString(link)
		return `<a href="` + escaped + `" target="_blank" rel="nofollow noopener noreferrer" translate="no">` + escaped + `</a>`
	}
	prefix := activityPubConvertedURLPrefix(link)
	rest := link[len(prefix):]
	display, suffix, cutoff := activityPubConvertedURLDisplay(rest)
	return `<a href="` + html.EscapeString(link) + `" target="_blank" rel="nofollow noopener noreferrer" translate="no"><span class="invisible">` + html.EscapeString(prefix) + `</span><span class="` + activityPubConvertedEllipsisClass(cutoff) + `">` + html.EscapeString(display) + `</span><span class="invisible">` + html.EscapeString(suffix) + `</span></a>`
}

func activityPubConvertedURLPrefix(link string) string {
	lower := strings.ToLower(link)
	switch {
	case strings.HasPrefix(lower, "https://www."):
		return link[:12]
	case strings.HasPrefix(lower, "http://www."):
		return link[:11]
	case strings.HasPrefix(lower, "https://"):
		return link[:8]
	case strings.HasPrefix(lower, "http://"):
		return link[:7]
	case strings.HasPrefix(lower, "xmpp:"):
		return link[:5]
	default:
		return ""
	}
}

func activityPubConvertedURLDisplay(rest string) (string, string, bool) {
	runes := []rune(rest)
	if len(runes) <= 30 {
		return rest, "", false
	}
	return string(runes[:30]), string(runes[30:]), true
}

func activityPubConvertedEllipsisClass(cutoff bool) string {
	if cutoff {
		return "ellipsis"
	}
	return ""
}

func activityPubStatusParserText(scalar string, firstMapKey string, firstMapValue string, values map[string]string) string {
	if strings.TrimSpace(scalar) != "" {
		return scalar
	}
	if firstMapKey != "" || len(values) > 0 {
		return activityPubLanguageMapFirstValue(firstMapKey, firstMapValue, values)
	}
	return ""
}

func activityPubStatusParserLanguage(note activityObject) string {
	if key, ok := activityPubLanguageMapFirstKey(note.ContentMapFirstKey, note.ContentMap); ok {
		return key
	}
	if key, ok := activityPubLanguageMapFirstKey(note.NameMapFirstKey, note.NameMap); ok {
		return key
	}
	if key, ok := activityPubLanguageMapFirstKey(note.SummaryMapFirstKey, note.SummaryMap); ok {
		return key
	}
	return ""
}

func activityPubLanguageMapFirstValue(firstKey string, firstValue string, values map[string]string) string {
	if firstKey != "" {
		return firstValue
	}
	return firstActivityLanguageMapValue(values)
}

func activityPubLanguageMapFirstKey(firstKey string, values map[string]string) (string, bool) {
	if firstKey != "" {
		return firstKey, true
	}
	if len(values) == 0 {
		return "", false
	}
	return firstActivityLanguageMapKey(values), true
}

func firstActivityLanguageMapValue(values map[string]string) string {
	for _, value := range values {
		return value
	}
	return ""
}

func activityPubPresentFirst(values ...string) string {
	for _, value := range values {
		if activityPubRailsPresentString(value) {
			return value
		}
	}
	return ""
}

func activityPubFirstPresentRaw(values ...string) string {
	for _, value := range values {
		if activityPubRailsPresentString(value) {
			return value
		}
	}
	return ""
}

func activityPubRailsPresentString(value string) bool {
	return strings.TrimSpace(value) != ""
}

func activityPubJSONLDPresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case bool:
		return typed
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func activityRubyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	default:
		return true
	}
}

func firstActivityLanguageMapKey(values map[string]string) string {
	for key := range values {
		return key
	}
	return ""
}

var activityPubSupportedRegionalLocales = map[string]string{
	"zh-cn":  "zh-CN",
	"zh-hk":  "zh-HK",
	"zh-tw":  "zh-TW",
	"zh-yue": "zh-YUE",
}

var activityPubSupportedISO6393Locales = map[string]struct{}{
	"ast": {}, "chr": {}, "ckb": {}, "cnr": {}, "jbo": {}, "kab": {}, "ldn": {}, "lfn": {},
	"sco": {}, "sma": {}, "smj": {}, "szl": {}, "tok": {}, "xal": {}, "zba": {}, "zgh": {},
}

const activityPubSupportedISO6391Locales = " aa ab ae af ak am an ar as av ay az ba be bg bh bi bm bn bo br bs ca ce ch co cr cs cu cv cy da de dv dz ee el en eo es et eu fa ff fi fj fo fr fy ga gd gl gu gv ha he hi ho hr ht hu hy hz ia id ie ig ii ik io is it iu ja jv ka kg ki kj kk kl km kn ko kr ks ku kv kw ky la lb lg li ln lo lt lu lv mg mh mi mk ml mn mr ms mt my na nb nd ne ng nl nn no nr nv ny oc oj om or os pa pi pl ps pt qu rm rn ro ru rw sa sc sd se sg si sk sl sn so sq sr ss st su sv sw ta te tg th ti tk tl tn to tr ts tt tw ty ug uk ur uz ve vi vo wa wo xh yi yo za zh zu "

func activityPubNormalizeLanguage(lang string) string {
	if strings.TrimSpace(lang) == "" {
		return ""
	}
	lower := strings.ToLower(lang)
	if normalized, ok := activityPubSupportedRegionalLocales[lower]; ok {
		return normalized
	}
	if _, ok := activityPubSupportedISO6393Locales[lower]; ok {
		return lower
	}
	if len(lower) == 2 && strings.Contains(activityPubSupportedISO6391Locales, " "+lower+" ") {
		return lower
	}
	return lang
}

func activityPollFromObject(object activityObject, accountID int64, statusID int64, now time.Time) (models.Poll, bool) {
	if !activityObjectHasType(object, "Question") {
		return models.Poll{}, false
	}
	items := object.OneOf
	multiple := false
	if object.AnyOfSet {
		if !object.AnyOfArray {
			return models.Poll{}, false
		}
		items = object.AnyOf
		multiple = true
	} else if !object.OneOfSet || !object.OneOfArray {
		return models.Poll{}, false
	}
	if len(items) == 0 {
		return models.Poll{}, false
	}
	options := make(models.StringArray, 0, len(items))
	tallies := make(models.Int64Array, 0, len(items))
	for _, item := range items {
		tallies = append(tallies, item.TotalItems)
		title, ok := activityPollOptionTitle(item)
		if !ok {
			continue
		}
		options = append(options, title)
	}
	if len(options) == 0 {
		return models.Poll{}, false
	}
	return models.Poll{
		AccountID:     models.PollAccountID(accountID),
		StatusID:      sql.NullInt64{Int64: statusID, Valid: true},
		ExpiresAt:     activityPollExpiresAt(object, now),
		Options:       options,
		CachedTallies: tallies,
		Multiple:      multiple,
		HideTotals:    false,
		VotesCount:    sumInt64Array(tallies),
		CreatedAt:     now,
		UpdatedAt:     now,
		LockVersion:   0,
		VotersCount:   object.VotersCount,
	}, true
}

func activityPollOptionTitle(item activityPollOption) (string, bool) {
	if strings.TrimSpace(item.Name) != "" {
		return item.Name, true
	}
	return item.Content, item.ContentSet
}

func activityPollVoteChoice(poll models.Poll, name string) int {
	for index, option := range poll.Options {
		if option == name {
			return index
		}
	}
	return -1
}

func activityPollExpiresAt(object activityObject, now time.Time) sql.NullTime {
	closedValue := activityJSONLDScalar(object.Closed)
	if closed, ok := closedValue.(string); ok {
		if parsed, ok := parseActivityPubTime(closed); ok {
			return sql.NullTime{Time: parsed, Valid: true}
		}
		return sql.NullTime{}
	}
	if object.Closed != nil {
		if closedBool, ok := closedValue.(bool); !ok || closedBool {
			return sql.NullTime{Time: now, Valid: true}
		}
	}
	if object.EndTime != "" {
		if parsed, ok := parseActivityPubTime(object.EndTime); ok {
			return sql.NullTime{Time: parsed, Valid: true}
		}
	}
	return sql.NullTime{}
}

func activityJSONLDScalar(value any) any {
	switch typed := activityJSONLDSingle(value).(type) {
	case map[string]any:
		if raw, ok := typed["@value"]; ok {
			return activityJSONLDScalar(raw)
		}
		return typed
	default:
		return typed
	}
}

func sumInt64Array(values models.Int64Array) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total
}

func activityEditedAt(note activityObject, fallback time.Time) sql.NullTime {
	if note.Updated == "" {
		return sql.NullTime{Time: fallback, Valid: true}
	}
	parsed, err := time.Parse(time.RFC3339, note.Updated)
	if err != nil {
		return sql.NullTime{Time: fallback, Valid: true}
	}
	return sql.NullTime{Time: parsed.UTC(), Valid: true}
}

func updateReplyCountersAfterChange(tx *gorm.DB, oldReply sql.NullInt64, nextReply sql.NullInt64) error {
	if oldReply.Valid && (!nextReply.Valid || oldReply.Int64 != nextReply.Int64) {
		if err := decrementStatusStatCounter(tx, oldReply.Int64, statusStatCounterReplies, 1); err != nil {
			return err
		}
	}
	if nextReply.Valid && (!oldReply.Valid || oldReply.Int64 != nextReply.Int64) {
		return incrementStatusStatCounter(tx, nextReply.Int64, statusStatCounterReplies, 1)
	}
	return nil
}

func (s *Server) updateActivityPubActor(actor *models.Account, object activityObject, requestID string) error {
	acquired, releaseAccountLock, err := s.acquireActivityPubRedisLock(context.Background(), "process_account:"+object.ID, activityPubRedisLockDefaultTTL)
	if err != nil {
		return err
	}
	if !acquired {
		return activityPubEventNotAppliedf("actor Update for %q is already being processed", object.ID)
	}
	defer releaseAccountLock()
	now := time.Now().UTC()
	suspensionTransition := activityActorSuspensionTransitionFor(*actor, object.Suspended)
	suspendedAfterUpdate := activityActorSuspendedAfterRemoteUpdate(*actor, object.Suspended)
	protocolChanged := actor.Protocol != 1
	updates := map[string]any{"updated_at": now}
	updates["protocol"] = 1
	updates["last_webfingered_at"] = sql.NullTime{Time: now, Valid: true}
	updates["inbox_url"] = object.Inbox
	updates["outbox_url"] = object.Outbox
	updates["shared_inbox_url"] = object.SharedInbox
	updates["followers_url"] = object.Followers
	updates["url"] = sql.NullString{String: firstNonEmpty(object.URL, object.ID), Valid: firstNonEmpty(object.URL, object.ID) != ""}
	updates["uri"] = object.ID
	updates["actor_type"] = sql.NullString{String: activityActorTypeValueOrDefault(object.Types), Valid: true}
	if object.Published != "" {
		updates["created_at"] = activityActorPublishedAt(object.Published, actor.CreatedAt)
	}
	if !activityActorLocallySuspended(*actor) {
		updates["public_key"] = object.PublicKey
	}
	for key, value := range activityActorSuspensionUpdatesForTransition(suspensionTransition, now) {
		updates[key] = value
	}
	var outboxInfo activityActorCollectionInfo
	var followingInfo activityActorCollectionInfo
	var followersInfo activityActorCollectionInfo
	if !suspendedAfterUpdate {
		outboxInfo = s.fetchActivityActorCollectionInfo(object.Outbox)
		followingInfo = s.fetchActivityActorCollectionInfo(object.Following)
		followersInfo = s.fetchActivityActorCollectionInfo(object.Followers)
		updates["hide_collections"] = sql.NullBool{Bool: !followingInfo.HasFirst || !followersInfo.HasFirst, Valid: true}
		updates["display_name"] = object.Name
		updates["note"] = object.Summary
		updates["featured_collection_url"] = sql.NullString{String: object.Featured, Valid: object.Featured != ""}
		updates["devices_url"] = object.Devices
		updates["locked"] = object.Locked
		updates["discoverable"] = sql.NullBool{Bool: object.Discoverable, Valid: true}
		updates["indexable"] = object.Indexable
		updates["memorial"] = object.Memorial
		updates["also_known_as"] = models.StringArray(activityRailsValueOrIDList(object.AlsoKnownAs))
		movedToID, movedToSet := s.remoteActorMovedToAccountID(object.MovedTo, requestID)
		if movedToSet {
			updates["moved_to_account_id"] = movedToID
		}
		fields := object.ProfileFields
		if fields == nil {
			fields = []profileField{}
		}
		encoded, _ := json.Marshal(fields)
		updates["fields"] = models.JSONValue(encoded)
	}
	var customEmojiChanges []models.CustomEmoji
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := setActivityPubTransactionLockTimeout(tx); err != nil {
			return err
		}
		skipMedia, err := activityPubActorSkipsMedia(tx, actor)
		if err != nil {
			return err
		}
		if suspendedAfterUpdate {
			skipMedia = true
		}
		if !skipMedia {
			updates["avatar_remote_url"] = sql.NullString{String: object.AvatarRemoteURL, Valid: true}
			updates["header_remote_url"] = object.HeaderRemoteURL
			if object.AvatarRemoteURL == "" || s.cfg.DisableRemoteMediaCache {
				clearActivityPubActorAvatarMediaUpdates(updates)
			}
			if object.HeaderRemoteURL == "" || s.cfg.DisableRemoteMediaCache {
				clearActivityPubActorHeaderMediaUpdates(updates)
			}
			changes, err := s.saveActivityPubActorCustomEmojis(tx, actor, object.Tags, now)
			if err != nil {
				return err
			}
			customEmojiChanges = append(customEmojiChanges, changes...)
		}
		if err := tx.Model(&models.Account{}).Where("id = ?", actor.ID).Updates(updates).Error; err != nil {
			return err
		}
		if !suspendedAfterUpdate {
			statUpdates := activityActorCollectionStatUpdates(outboxInfo, followingInfo, followersInfo)
			if len(statUpdates) > 0 {
				stat := models.AccountStat{AccountID: actor.ID, CreatedAt: now, UpdatedAt: now}
				if err := createActivityPubAccountStatIfMissing(tx, stat); err != nil {
					return err
				}
				statUpdates["updated_at"] = now
				if err := tx.Model(&models.AccountStat{}).Where("account_id = ?", actor.ID).Updates(statUpdates).Error; err != nil {
					return err
				}
			}
		}
		if activityActorLocallySuspended(*actor) {
			return nil
		}
		return clearActivityPubActorTombstonesOnKeyChange(tx, actor.ID, actor.PublicKey, object.PublicKey)
	}); err != nil {
		return err
	}
	s.invalidateCustomEmojiEntityCaches(context.Background(), customEmojiChanges)
	if err := s.applyActivityActorSuspensionTransitionEffects(context.Background(), s.db, actor.ID, suspensionTransition); err != nil {
		return err
	}
	if protocolChanged {
		if err := s.applyActivityPubPostUpgrade(context.Background(), s.db, actor.Domain.String); err != nil {
			return err
		}
	}
	s.meiliIndexAccountBestEffort(context.Background(), actor.ID)
	if !suspendedAfterUpdate {
		verificationAccount := *actor
		verificationAccount.URI = object.ID
		verificationAccount.URL = sql.NullString{String: firstNonEmpty(object.URL, object.ID), Valid: firstNonEmpty(object.URL, object.ID) != ""}
		if fields, ok := updates["fields"].(models.JSONValue); ok {
			verificationAccount.Fields = fields
		}
		s.enqueueVerifyAccountLinksIfNeeded(context.Background(), verificationAccount, now)
		s.syncActivityPubFeaturedCollectionBestEffort(actor, object.Featured, "", object.FeaturedTags == "")
		if object.FeaturedCollection != nil {
			if object.FeaturedTags == "" {
				_ = s.syncRemoteFeaturedTags(actor, activityPubFeaturedTagNamesFromTags(object.FeaturedCollection.Tags))
			}
			_ = s.syncRemoteStatusPinsFromActivityCollection(actor, *object.FeaturedCollection, "")
		}
		s.syncActivityPubFeaturedTagsBestEffort(actor, object.FeaturedTags)
	}
	return nil
}

func clearActivityPubActorAvatarMediaUpdates(updates map[string]any) {
	updates["avatar_file_name"] = nil
	updates["avatar_content_type"] = nil
	updates["avatar_file_size"] = nil
	updates["avatar_updated_at"] = nil
}

func clearActivityPubActorHeaderMediaUpdates(updates map[string]any) {
	updates["header_file_name"] = nil
	updates["header_content_type"] = nil
	updates["header_file_size"] = nil
	updates["header_updated_at"] = nil
}

func (s *Server) processActivityPubFollow(payload activityPayload, actor *models.Account) error {
	activityID := activityPayloadIDValueOrID(payload)
	target, err := s.localAccountFromActivityURI(payload.Object.ID)
	if err != nil {
		return err
	}
	if target == nil {
		return activityPubEventNotAppliedf("Follow target %q is not a known local account", payload.Object.ID)
	}
	if target.ID == actor.ID || !target.Local() {
		return activityPubEventNotAppliedf("Follow target %q is not a distinct local account", payload.Object.ID)
	}
	if activityID != "" {
		deleteArrivedFirst, err := s.activityPubDeleteArrivedFirst(actor, activityID)
		if err != nil || deleteArrivedFirst {
			return err
		}
	}
	now := time.Now().UTC()
	updatedExistingRequest, err := s.updateExistingActivityPubFollowRequestURI(actor.ID, target.ID, activityID, now)
	if err != nil {
		return err
	}
	if updatedExistingRequest {
		return nil
	}
	rejected, err := s.rejectIncomingFollow(actor, target)
	if err != nil {
		return err
	}
	if rejected {
		return s.deliverActivityPubFollowResponse("Reject", *target, *actor, 0, activityID)
	}
	followURI := activityID
	if !payload.IDPresent {
		followURI = activityPubGeneratedPayloadURI(s)
	}
	var accepted *models.Follow
	acceptResponseFollowID := int64(0)
	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	followChanged := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var existingFollow models.Follow
		err := tx.Where("account_id = ? AND target_account_id = ?", actor.ID, target.ID).First(&existingFollow).Error
		if err == nil {
			if err := tx.Model(&existingFollow).Updates(map[string]any{"uri": activityID, "updated_at": now}).Error; err != nil {
				return err
			}
			existingFollow.URI = models.NullSafeString(activityID)
			accepted = &existingFollow
			acceptResponseFollowID = 0
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if target.Locked || actor.SilencedAt.Valid {
			req := models.FollowRequest{CreatedAt: now, UpdatedAt: now, AccountID: actor.ID, TargetAccountID: target.ID, ShowReblogs: true, Notify: false, URI: models.NullSafeString(followURI)}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&req)
			if res.Error != nil || res.RowsAffected == 0 {
				return res.Error
			}
			notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: actor.ID, ActivityID: req.ID, ActivityType: "FollowRequest", Type: "follow_request"})
			return nil
		}

		req := models.FollowRequest{CreatedAt: now, UpdatedAt: now, AccountID: actor.ID, TargetAccountID: target.ID, ShowReblogs: true, Notify: false, URI: models.NullSafeString(followURI)}
		reqRes := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&req)
		if reqRes.Error != nil || reqRes.RowsAffected == 0 {
			return reqRes.Error
		}
		acceptResponseFollowID = req.ID
		follow := models.Follow{CreatedAt: now, UpdatedAt: now, AccountID: actor.ID, TargetAccountID: target.ID, ShowReblogs: true, Notify: false, URI: models.NullSafeString(followURI)}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&follow)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		if err := tx.Delete(&req).Error; err != nil {
			return err
		}
		followChanged = true
		accepted = &follow
		if err := incrementAccountStatCounter(tx, actor.ID, accountStatCounterFollowing, 1); err != nil {
			return err
		}
		if err := incrementAccountStatCounter(tx, target.ID, accountStatCounterFollowers, 1); err != nil {
			return err
		}
		notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: target.ID, FromAccountID: actor.ID, ActivityID: follow.ID, ActivityType: "Follow", Type: "follow"})
		return nil
	})
	if err != nil {
		return err
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(context.Background(), notificationPayloads)
	if err != nil {
		return err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	s.publishNotificationIDs(notificationIDs)
	if accepted != nil {
		if followChanged {
			s.invalidateFollowRelationshipCaches(context.Background(), *actor, target.ID)
			s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), target.ID)
		}
		if err := s.deliverActivityPubFollowResponse("Accept", *target, *actor, acceptResponseFollowID, string(accepted.URI)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) updateExistingActivityPubFollowRequestURI(accountID int64, targetAccountID int64, uri string, now time.Time) (bool, error) {
	var request models.FollowRequest
	err := s.db.Where("account_id = ? AND target_account_id = ?", accountID, targetAccountID).First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := s.db.Model(&request).Updates(map[string]any{"uri": uri, "updated_at": now}).Error; err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) processActivityPubUndoFollow(object activityObject, actor *models.Account) error {
	_, err := s.processActivityPubUndoFollowWithTombstone(object, actor, true)
	return err
}

func (s *Server) processActivityPubUndoFollowWithTombstone(object activityObject, actor *models.Account, tombstoneOnMiss bool) (bool, error) {
	targetURI := activityPubUndoTargetURI(object)
	target, err := s.localAccountFromActivityURI(targetURI)
	if (err != nil || target == nil) && !object.TypePresent {
		followTarget, followErr := s.followTargetFromURI(object.ID, actor.ID)
		if followErr != nil {
			return false, followErr
		}
		target = followTarget
	}
	if target == nil {
		return false, nil
	}
	followDeleted := false
	handled := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var follow models.Follow
		query := tx.Where("account_id = ? AND target_account_id = ?", actor.ID, target.ID)
		if object.ID != "" {
			query = tx.Where("(account_id = ? AND target_account_id = ?) OR uri = ?", actor.ID, target.ID, object.ID)
		}
		err := query.First(&follow).Error
		if err == nil {
			if err := deleteFollow(tx, follow); err != nil {
				return err
			}
			followDeleted = true
			handled = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var request models.FollowRequest
		reqQuery := tx.Where("account_id = ? AND target_account_id = ?", actor.ID, target.ID)
		if object.ID != "" {
			reqQuery = tx.Where("(account_id = ? AND target_account_id = ?) OR uri = ?", actor.ID, target.ID, object.ID)
		}
		if err := reqQuery.First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if !tombstoneOnMiss {
					return nil
				}
				return s.markActivityPubDeleteUponArrival(actor, object.ID)
			}
			return err
		}
		if err := tx.Where("activity_type = ? AND activity_id = ?", "FollowRequest", request.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&request).Error; err != nil {
			return err
		}
		handled = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if handled {
		s.invalidateFollowRelationshipCaches(context.Background(), *actor, target.ID)
	}
	if followDeleted {
		s.unmergeAfterUnfollowBestEffort(context.Background(), target.ID, *actor)
		s.meiliReindexPrivateStatusesForAccountsBestEffort(context.Background(), target.ID)
	}
	return handled, nil
}

func (s *Server) followTargetFromURI(uri string, accountID int64) (*models.Account, error) {
	if s == nil || s.db == nil || uri == "" || accountID == 0 {
		return nil, nil
	}
	var follow models.Follow
	if err := s.db.Where("account_id = ? AND uri = ?", accountID, uri).First(&follow).Error; err == nil {
		var target models.Account
		if loadErr := s.db.Where("id = ?", follow.TargetAccountID).First(&target).Error; loadErr != nil {
			if errors.Is(loadErr, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, loadErr
		}
		return &target, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var request models.FollowRequest
	if err := s.db.Where("account_id = ? AND uri = ?", accountID, uri).First(&request).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var target models.Account
	if err := s.db.Where("id = ?", request.TargetAccountID).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &target, nil
}

func (s *Server) processActivityPubLike(payload activityPayload, actor *models.Account) error {
	activityID := activityPayloadIDValueOrID(payload)
	status, err := s.localStatusFromActivityURI(activityPubLikeTargetURI(payload.Object))
	if err != nil {
		return err
	}
	if status == nil {
		return nil
	}
	if status.AccountID == actor.ID || status.DeletedAt.Valid {
		return nil
	}
	joinStatus := statusJoinTarget(status)
	if joinStatus == nil {
		return nil
	}
	now := time.Now().UTC()
	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	favouriteCreated := false
	if activityID != "" {
		deleteArrivedFirst, err := s.activityPubDeleteArrivedFirst(actor, activityID)
		if err != nil || deleteArrivedFirst {
			return err
		}
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		favourite := models.Favourite{AccountID: actor.ID, StatusID: joinStatus.ID, CreatedAt: now, UpdatedAt: now}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&favourite)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		favouriteCreated = true
		if err := incrementStatusStatCounter(tx, joinStatus.ID, statusStatCounterFavourites, 1); err != nil {
			return err
		}
		notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: status.AccountID, FromAccountID: actor.ID, ActivityID: favourite.ID, ActivityType: "Favourite", Type: "favourite"})
		return nil
	}); err != nil {
		return err
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(context.Background(), notificationPayloads)
	if err != nil {
		return err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	if favouriteCreated {
		s.recordStatusTrendUse(context.Background(), status.ID, now)
	}
	s.publishNotificationIDs(notificationIDs)
	return nil
}

func (s *Server) processActivityPubUndoLike(object activityObject, actor *models.Account) error {
	status, err := s.localStatusFromActivityURI(activityPubUndoLikeTargetURI(object))
	if err != nil {
		return err
	}
	if status == nil {
		return nil
	}
	joinStatus := statusJoinTarget(status)
	if joinStatus == nil {
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var properFavourite models.Favourite
		err := tx.Where("account_id = ? AND status_id = ?", actor.ID, joinStatus.ID).First(&properFavourite).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return s.markActivityPubDeleteUponArrival(actor, object.ID)
			}
			return err
		}
		var favourite models.Favourite
		err = tx.Where("account_id = ? AND status_id = ?", actor.ID, status.ID).First(&favourite).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Where("activity_type = ? AND activity_id = ?", "Favourite", favourite.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&favourite).Error; err != nil {
			return err
		}
		return decrementStatusStatCounter(tx, status.ID, statusStatCounterFavourites, 1)
	})
}

func activityPubLikeTargetURI(object activityObject) string {
	return object.ID
}

func activityPubUndoLikeTargetURI(object activityObject) string {
	return activityPubUndoTargetURI(object)
}

func activityPubUndoTargetURI(object activityObject) string {
	if object.ObjectIDPresent {
		return object.ObjectIDRaw
	}
	return firstNonEmpty(object.ObjectIDRaw, object.ObjectID)
}

func (s *Server) processActivityPubAnnounce(payload activityPayload, actor *models.Account, relayedThrough *models.Account, options activityPubProcessingOptions) error {
	targetURI := payload.Object.ID
	lockTargetURI := activityPubAnnounceLockTargetURI(payload, targetURI)
	if lockTargetURI == "" {
		return activityPubEventNotAppliedf("Announce target is missing")
	}
	announceURI := activityAnnounceURI(activityPayloadIDValueOrID(payload), actor, targetURI)
	acquired, releaseAnnounceLock, err := s.acquireActivityPubRedisLock(context.Background(), "announce:"+lockTargetURI, activityPubRedisLockDefaultTTL)
	if err != nil {
		return err
	}
	if !acquired {
		return activityPubEventNotAppliedf("Announce target %q is already being processed", lockTargetURI)
	}
	defer releaseAnnounceLock()
	if announceURI != "" {
		deleteArrivedFirst, err := s.activityPubDeleteArrivedFirst(actor, announceURI)
		if err != nil || deleteArrivedFirst {
			return err
		}
	}
	target, err := s.statusFromActivityURI(targetURI)
	if err == nil && target == nil && activityPubEmbeddedSelfStatus(payload.Object, actor) {
		createPayload := activityPayload{
			ID:        payload.Object.ID,
			Type:      "Create",
			Actor:     actor.URI,
			Published: payload.Published,
			Object:    payload.Object,
			To:        payload.Object.To,
			CC:        payload.Object.CC,
			RawBody:   payload.RawBody,
		}
		if err := s.processActivityPubCreateNote(createPayload, actor, nil, relayedThrough, options); err != nil {
			return err
		}
		target, err = s.statusFromActivityURI(targetURI)
	}
	if err == nil && target == nil {
		shouldFetch, fetchErr := s.activityPubAnnounceShouldFetchUnknownTarget(actor, relayedThrough)
		if fetchErr != nil || !shouldFetch {
			return fetchErr
		}
		target, err = s.fetchActivityPubAnnounceTarget(targetURI, payload.Object.URL, payload.ID)
	}
	if err != nil {
		return err
	}
	if target == nil {
		return activityPubEventNotAppliedf("Announce target %q could not be resolved", targetURI)
	}
	if target.DeletedAt.Valid {
		return nil
	}
	related, err := s.activityPubAnnounceRelatedToLocalActivity(actor, relayedThrough, *target)
	if err != nil || !related {
		return err
	}
	if !activityPubAnnounceable(actor, *target) {
		return nil
	}
	throughRelay, err := s.activityPubAnnounceActorOrRelayRequestedThroughRelay(actor, relayedThrough)
	if err != nil || throughRelay {
		return err
	}
	now := time.Now().UTC()
	visibility := activityPubAnnounceVisibility(payload, actor)
	reblog := models.Status{
		URI:        activityPubAnnounceStatusURI(announceURI),
		Text:       "",
		CreatedAt:  activityPayloadPublishedAt(payload, now),
		UpdatedAt:  now,
		AccountID:  actor.ID,
		Local:      sql.NullBool{Bool: false, Valid: true},
		Visibility: visibility,
		ReblogOfID: sql.NullInt64{Int64: target.ID, Valid: true},
	}
	var notificationIDs []int64
	var notificationPayloads []asynqLocalNotificationPayload
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Status
		err := tx.Where("account_id = ? AND reblog_of_id = ? AND deleted_at IS NULL", actor.ID, target.ID).First(&existing).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := safeInsertReblogStatus(tx, &reblog); err != nil {
			if errors.Is(err, errSafeReblogTargetUnavailable) {
				return nil
			}
			return err
		}
		if err := tx.Create(&models.StatusStat{StatusID: reblog.ID}).Error; err != nil {
			return err
		}
		if err := incrementStatusStatCounter(tx, target.ID, statusStatCounterReblogs, 1); err != nil {
			return err
		}
		if err := upsertAccountStatForStatus(tx, actor.ID, reblog.Visibility, now); err != nil {
			return err
		}
		if activityPubAnnounceShouldNotifyReblog(tx, *target, *actor) {
			notificationPayloads = append(notificationPayloads, asynqLocalNotificationPayload{ReceiverAccountID: target.AccountID, FromAccountID: actor.ID, ActivityID: reblog.ID, ActivityType: "Status", Type: "reblog"})
		}
		return nil
	}); err != nil {
		return err
	}
	createdNotificationIDs, err := s.enqueueOrCreateLocalNotifications(context.Background(), notificationPayloads)
	if err != nil {
		return err
	}
	notificationIDs = append(notificationIDs, createdNotificationIDs...)
	if reblog.ID != 0 {
		s.recordStatusTrendUse(context.Background(), target.ID, reblog.CreatedAt)
		s.recordPreviewCardTrendUseForStatus(context.Background(), actor.ID, target.ID, reblog.Visibility, reblog.CreatedAt)
	}
	s.publishNotificationIDs(notificationIDs)
	if reblog.ID != 0 && (options.OverrideTimestamps || activityPubStatusWithinRealtimeWindow(reblog, now)) {
		if created, err := s.findStatus(strconv.FormatInt(reblog.ID, 10)); err == nil {
			_ = s.fanOutStatusToLocalRecipientsSkipNotifications(context.Background(), s.db, *created)
			s.publishStatusUpdateEvent("update", *created)
		}
	}
	return nil
}

func activityPubAnnounceLockTargetURI(payload activityPayload, targetURI string) string {
	if len(payload.ObjectIDRaws) > 0 && strings.TrimSpace(payload.ObjectIDRaws[0]) != "" {
		return payload.ObjectIDRaws[0]
	}
	return targetURI
}

func (s *Server) fetchActivityPubAnnounceTarget(targetURI string, targetURL string, requestID string) (*models.Status, error) {
	if activityPubHTTPURIAllowedRaw(targetURI) {
		if s.localActivityURI(targetURI) {
			return nil, nil
		}
		return s.fetchRemoteStatusFromActivityURIForRequest(targetURI, "", requestID)
	}
	if !activityPubHTTPURIAllowedRaw(targetURL) || s.localActivityURI(targetURL) {
		return nil, nil
	}
	return s.fetchRemoteStatusFromResolvableURL(targetURL)
}

func (s *Server) activityPubAnnounceShouldFetchUnknownTarget(actor *models.Account, relayedThrough *models.Account) (bool, error) {
	if actor == nil || actor.ID == 0 || s == nil || s.db == nil {
		return false, nil
	}
	followed, err := s.activityPubActorOrRelayFollowedByLocalAccount(actor, relayedThrough)
	if err != nil || followed {
		return followed, err
	}
	return s.activityPubAnnounceActorOrRelayRequestedThroughRelay(actor, relayedThrough)
}

func (s *Server) activityPubAnnounceRelatedToLocalActivity(actor *models.Account, relayedThrough *models.Account, target models.Status) (bool, error) {
	if actor == nil || actor.ID == 0 || s == nil || s.db == nil {
		return false, nil
	}
	followed, err := s.activityPubActorOrRelayFollowedByLocalAccount(actor, relayedThrough)
	if err != nil || followed {
		return followed, err
	}
	relay, err := s.activityPubAnnounceActorOrRelayRequestedThroughRelay(actor, relayedThrough)
	if err != nil || relay {
		return relay, err
	}
	return target.Account.Local(), nil
}

func activityPubEmbeddedSelfStatus(object activityObject, actor *models.Account) bool {
	return actor != nil &&
		activityObjectIsStatus(object) &&
		object.ID != "" &&
		firstNonEmpty(object.AttributedToRaw, object.AttributedTo) == actor.URI
}

func activityPubAnnounceable(actor *models.Account, target models.Status) bool {
	if actor != nil && target.AccountID == actor.ID {
		return true
	}
	return target.Visibility == 0 || target.Visibility == 1
}

func activityPubAnnounceShouldNotifyReblog(db *gorm.DB, target models.Status, actor models.Account) bool {
	if !target.Account.Local() || target.AccountID == actor.ID {
		return false
	}
	if activityPubAccountIsGroup(actor) && db != nil {
		var count int64
		if err := db.Model(&models.Follow{}).
			Where("account_id = ? AND target_account_id = ?", target.AccountID, actor.ID).
			Count(&count).Error; err == nil && count > 0 {
			return false
		}
	}
	return true
}

func activityPubAnnounceVisibility(payload activityPayload, actor *models.Account) int {
	if activityPubContainsPublicCollection(payload.To) {
		return 0
	}
	if activityPubContainsPublicCollection(payload.CC) {
		return 1
	}
	followers := ""
	if actor != nil && strings.TrimSpace(actor.FollowersURL) != "" {
		followers = actor.FollowersURL
	}
	if followers != "" && stringSliceContains(payload.To, followers) {
		return 2
	}
	return 3
}

func (s *Server) processActivityPubUndoAnnounce(object activityObject, actor *models.Account) error {
	_, err := s.processActivityPubUndoAnnounceWithTombstone(object, actor, true)
	return err
}

func (s *Server) processActivityPubUndoAnnounceWithTombstone(object activityObject, actor *models.Account, tombstoneOnMiss bool) (bool, error) {
	target, err := s.statusFromActivityURI(object.ObjectID)
	if err != nil {
		return false, err
	}
	announceURI := object.ID
	var reblog models.Status
	var removedFavourites []models.Favourite
	var removedBookmarks []models.Bookmark
	var removedStatusPins []models.StatusPin
	handled := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("account_id = ? AND deleted_at IS NULL", actor.ID)
		if target != nil {
			query = query.Where("reblog_of_id = ?", target.ID)
			if announceURI != "" {
				query = tx.Where("account_id = ? AND deleted_at IS NULL AND (reblog_of_id = ? OR uri = ?)", actor.ID, target.ID, announceURI)
				if activityPubRailsPresentString(object.AtomURI) {
					query = tx.Where("account_id = ? AND deleted_at IS NULL AND (reblog_of_id = ? OR uri = ? OR uri = ?)", actor.ID, target.ID, announceURI, object.AtomURI)
				}
			}
		} else if announceURI != "" {
			query = query.Where("uri = ?", announceURI)
			if activityPubRailsPresentString(object.AtomURI) {
				query = tx.Where("account_id = ? AND deleted_at IS NULL AND (uri = ? OR uri = ?)", actor.ID, announceURI, object.AtomURI)
			}
		} else {
			if tombstoneOnMiss && object.IDPresent {
				return s.markActivityPubDeleteUponArrival(actor, announceURI)
			}
			return nil
		}
		err := query.First(&reblog).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if !tombstoneOnMiss {
					return nil
				}
				return s.markActivityPubDeleteUponArrival(actor, announceURI)
			}
			return err
		}
		if target == nil {
			if !reblog.ReblogOfID.Valid {
				return nil
			}
			var loaded models.Status
			if err := tx.Preload("Account.AccountStat").Where("id = ?", reblog.ReblogOfID.Int64).First(&loaded).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			target = &loaded
		}
		now := time.Now().UTC()
		if err := tx.Where("activity_type = ? AND activity_id = ?", "Status", reblog.ID).Delete(&models.Notification{}).Error; err != nil {
			return err
		}
		if err := tx.Select("account_id, status_id").Where("status_id = ?", reblog.ID).Find(&removedStatusPins).Error; err != nil {
			return err
		}
		if err := tx.Where("status_id = ?", reblog.ID).Delete(&models.StatusPin{}).Error; err != nil {
			return err
		}
		favourites, bookmarks, err := deleteActivityPubStatusJoinRows(tx, []int64{reblog.ID})
		if err != nil {
			return err
		}
		removedFavourites = append(removedFavourites, favourites...)
		removedBookmarks = append(removedBookmarks, bookmarks...)
		if err := tx.Model(&models.Status{}).Where("id = ?", reblog.ID).Updates(map[string]any{"deleted_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := decrementStatusStatCounter(tx, target.ID, statusStatCounterReblogs, 1); err != nil {
			return err
		}
		if err := decrementAccountStatForStatus(tx, actor.ID, reblog.Visibility); err != nil {
			return err
		}
		handled = true
		return nil
	}); err != nil {
		return false, err
	}
	if handled {
		_ = s.removeStatusFromRailsFeeds(context.Background(), s.db, reblog)
		s.publishStatusDelete(reblog)
		for _, favourite := range removedFavourites {
			s.runFavouriteDestroyedSideEffects(context.Background(), favourite)
		}
		for _, bookmark := range removedBookmarks {
			s.runBookmarkDestroyedSideEffects(context.Background(), bookmark)
		}
		for _, pin := range removedStatusPins {
			s.runStatusPinDestroyedSideEffects(context.Background(), pin)
		}
		s.meiliDeleteStatusBestEffort(context.Background(), reblog.ID)
	}
	return handled, nil
}

func (s *Server) rejectIncomingFollow(actor *models.Account, target *models.Account) (bool, error) {
	if target.MovedToAccountID.Valid || activityPubAccountIsInstanceActor(target) {
		return true, nil
	}
	var count int64
	if err := s.db.Model(&models.Block{}).Where("account_id = ? AND target_account_id = ?", target.ID, actor.ID).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if actor.Domain.Valid && actor.Domain.String != "" {
		if err := s.db.Model(&models.AccountDomainBlock{}).Where("account_id = ? AND lower(domain) = lower(?)", target.ID, actor.Domain.String).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func activityPubAccountIsInstanceActor(account *models.Account) bool {
	return account != nil && account.ID == -99
}

func decrementFollowCounters(tx *gorm.DB, sourceID int64, targetID int64) error {
	if err := decrementAccountStatCounter(tx, sourceID, accountStatCounterFollowing, 1); err != nil {
		return err
	}
	return decrementAccountStatCounter(tx, targetID, accountStatCounterFollowers, 1)
}

func (s *Server) localAccountFromActivityURI(uri string) (*models.Account, error) {
	if s.localInstanceActorActivityURI(uri) {
		account := s.instanceActorAccount()
		return &account, nil
	}
	username := s.localUsernameFromActivityURI(uri)
	if username == "" {
		return nil, nil
	}
	var account models.Account
	err := s.db.Preload("AccountStat").Where("lower(username) = lower(?) AND domain IS NULL", username).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &account, err
}

func (s *Server) accountFromActivityURI(uri string) (*models.Account, error) {
	return s.accountFromActivityURIWithDB(s.db, uri)
}

func (s *Server) accountFromActivityURIWithDB(db *gorm.DB, uri string) (*models.Account, error) {
	if s == nil || db == nil || uri == "" {
		return nil, nil
	}
	if s.localInstanceActorActivityURI(uri) {
		account := s.instanceActorAccount()
		return &account, nil
	}
	if username := s.localUsernameFromActivityURI(uri); username != "" {
		var account models.Account
		err := db.Preload("AccountStat").Where("lower(username) = lower(?) AND domain IS NULL", username).First(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return &account, err
	}
	var account models.Account
	lookupURI := activityPubLookupURI(uri)
	err := findActivityPubAccountByURIOrURL(db, uri, lookupURI, &account)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &account, err
}

func findActivityPubAccountByURIOrURL(db *gorm.DB, uri string, lookupURI string, account *models.Account) error {
	if db == nil || account == nil {
		return gorm.ErrInvalidDB
	}
	candidates := uniqueStrings([]string{strings.TrimSpace(uri), strings.TrimSpace(lookupURI)})
	if len(candidates) > 0 {
		err := db.Preload("AccountStat").Where("uri IN ?", candidates).First(account).Error
		if err == nil || !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if strings.TrimSpace(lookupURI) == "" {
		return gorm.ErrRecordNotFound
	}
	return db.Preload("AccountStat").Where("url = ?", lookupURI).First(account).Error
}

func activityPubLookupURI(uri string) string {
	lookupURI := strings.TrimSpace(uri)
	if before, _, ok := strings.Cut(lookupURI, "#"); ok {
		return before
	}
	return lookupURI
}

func (s *Server) localInstanceActorActivityURI(uri string) bool {
	parsed, err := url.Parse(uri)
	if err != nil || !s.localActivityHost(parsed.Host) {
		return false
	}
	path := strings.Trim(parsed.EscapedPath(), "/")
	return path == "actor" || path == "actor.json"
}

func (s *Server) localStatusFromActivityURI(uri string) (*models.Status, error) {
	return s.localStatusFromActivityURIWithContext(context.Background(), uri)
}

func (s *Server) localStatusFromActivityURIWithContext(ctx context.Context, uri string) (*models.Status, error) {
	parsed, err := url.Parse(uri)
	if err != nil || !s.localActivityHost(parsed.Host) {
		return nil, nil
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] != "users" || parts[2] != "statuses" {
		return nil, nil
	}
	var status models.Status
	err = s.db.WithContext(ctx).
		Preload("Account.AccountStat").
		Preload("Reblog.Account.AccountStat").
		Where("statuses.id = ? AND statuses.deleted_at IS NULL", parts[3]).
		First(&status).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &status, nil
}

func (s *Server) statusFromActivityURI(uri string) (*models.Status, error) {
	return s.statusFromActivityURIWithContext(context.Background(), uri)
}

func (s *Server) statusFromActivityURIWithContext(ctx context.Context, uri string) (*models.Status, error) {
	if uri == "" {
		return nil, nil
	}
	if status, err := s.localStatusFromActivityURIWithContext(ctx, uri); err != nil || status != nil {
		return status, err
	}
	var status models.Status
	lookupURI := activityPubLookupURI(uri)
	err := s.db.WithContext(ctx).
		Preload("Account.AccountStat").
		Where("statuses.uri = ? AND statuses.deleted_at IS NULL", lookupURI).
		First(&status).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &status, err
}

func activityAnnounceURI(id string, actor *models.Account, targetURI string) string {
	if id != "" {
		return id
	}
	return ""
}

func activityPubAnnounceStatusURI(id string) sql.NullString {
	return sql.NullString{String: id, Valid: id != ""}
}

func activityPublishedAt(object activityObject, fallback time.Time) time.Time {
	if object.Published == "" {
		return fallback
	}
	parsed, ok := parseActivityPubTime(object.Published)
	if !ok {
		return fallback
	}
	return parsed
}

func activityPayloadPublishedAt(payload activityPayload, fallback time.Time) time.Time {
	if payload.Published == "" {
		return activityPublishedAt(payload.Object, fallback)
	}
	parsed, ok := parseActivityPubTime(payload.Published)
	if !ok {
		return activityPublishedAt(payload.Object, fallback)
	}
	return parsed
}

func parseActivityPubTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		parsed, err := time.Parse(layout, raw)
		if err == nil && parsed.Year() >= 0 && parsed.Year() <= 9999 {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func (s *Server) localUsernameFromActivityURI(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || !s.localActivityHost(parsed.Host) {
		return ""
	}
	path := strings.Trim(parsed.EscapedPath(), "/")
	if strings.HasPrefix(path, "users/") {
		parts := strings.Split(strings.TrimPrefix(path, "users/"), "/")
		if len(parts) > 0 {
			return parts[0]
		}
		return ""
	}
	if strings.HasPrefix(path, "@") {
		parts := strings.Split(strings.TrimPrefix(path, "@"), "/")
		if len(parts) > 0 {
			return parts[0]
		}
		return ""
	}
	return ""
}

func (s *Server) localActivityHost(host string) bool {
	normalized := activityHostWithNormalizedName(host)
	return normalized != "" && (normalized == activityHostWithNormalizedName(s.cfg.WebDomain) || normalized == activityHostWithNormalizedName(s.cfg.LocalDomain))
}

func activityHostWithNormalizedName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse("//" + raw)
	if err != nil || parsed.Host == "" {
		return normalizeDeliveryStatsHost(raw)
	}
	host := normalizeDeliveryStatsHost(parsed.Hostname())
	if host == "" {
		return ""
	}
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	return host
}

type activityPayload struct {
	ID              string
	IDRaw           string
	IDPresent       bool
	Type            string
	Actor           string
	ActorRaw        string
	ActorPresent    bool
	Content         string
	Published       string
	Signature       activityLinkedDataSignature
	Object          activityObject
	ObjectReference bool
	ObjectIDs       []string
	ObjectIDRaws    []string
	Target          string
	To              []string
	CC              []string
	Items           []activityPayload
	RawBody         []byte
	Fetch           bool
	ObjectDocument  bool
}

type activityLinkedDataSignature struct {
	Type           string
	Creator        string
	Created        string
	SignatureValue string
	Present        bool
}

type activityObject struct {
	ID                 string
	IDPresent          bool
	Reference          bool
	Type               string
	TypeExact          string
	TypePresent        bool
	Types              []string
	AtomURI            string
	Featured           string
	FeaturedCollection *activityCollection
	FeaturedTags       string
	Devices            string
	Inbox              string
	Outbox             string
	Following          string
	Followers          string
	SharedInbox        string
	Actor              string
	ActorRaw           string
	ObjectID           string
	ObjectIDRaw        string
	ObjectIDPresent    bool
	AttributedTo       string
	AttributedToRaw    string
	URL                string
	Name               string
	NameMap            map[string]string
	NameMapFirstKey    string
	NameMapFirst       string
	Content            string
	ContentMap         map[string]string
	ContentMapFirstKey string
	ContentMapFirst    string
	Summary            string
	SummaryMap         map[string]string
	SummaryMapFirstKey string
	SummaryMapFirst    string
	InReplyTo          string
	InReplyToPresent   bool
	InReplyToAtomURI   string
	Conversation       string
	ConversationSet    bool
	Published          string
	Updated            string
	Language           string
	Sensitive          bool
	Locked             bool
	Discoverable       bool
	Indexable          bool
	Memorial           bool
	Suspended          bool
	Closed             any
	EndTime            string
	OneOf              []activityPollOption
	OneOfSet           bool
	OneOfArray         bool
	AnyOf              []activityPollOption
	AnyOfSet           bool
	AnyOfArray         bool
	VotersCount        sql.NullInt64
	To                 []string
	ToSet              bool
	CC                 []string
	CCSet              bool
	Tags               []activityTag
	Attachments        []activityAttachment
	Replies            activityCollection
	ProfileFields      []profileField
	ProfileFieldsSet   bool
	PublicKey          string
	AvatarRemoteURL    string
	HeaderRemoteURL    string
	SourceDeviceID     string
	TargetDeviceID     string
	MessageType        int
	CipherText         string
	MessageFranking    string
	DigestValue        string
	QuoteURI           string
	QuoteURL           string
	MisskeyQuote       string
	QuoteURIRaw        string
	QuoteURLRaw        string
	MisskeyQuoteRaw    string
	QuoteURISet        bool
	QuoteURLSet        bool
	MisskeyQuoteSet    bool
	AlsoKnownAs        any
	MovedTo            any
}

type activityPollOption struct {
	Name       string
	Content    string
	ContentSet bool
	TotalItems int64
}

type activityPollOptionSource struct {
	Options []activityPollOption
	Set     bool
	Array   bool
}

type activityTag struct {
	ID      string
	Type    string
	Types   []string
	Name    string
	NameSet bool
	Href    string
	IconURL string
	Updated string
}

type activityAttachment struct {
	Type        string
	MediaType   string
	URL         string
	Name        string
	Summary     string
	Description string
	Blurhash    string
	IconURL     string
	Focus       string
}

type activityCollection struct {
	ID               string
	Type             string
	First            string
	FirstPresent     bool
	FirstCollection  *activityCollection
	Next             string
	NextPresent      bool
	NextCollection   *activityCollection
	Items            []string
	OrderedItems     []string
	NoteItems        []string
	OrderedNoteItems []string
	Tags             []activityTag
}

const activityPubFetchedRepliesLimit = 5

func (s *Server) fetchActivityPubRepliesBestEffort(parent models.Status, note activityObject, actor *models.Account) {
	if s == nil || actor == nil {
		return
	}
	uris := activityPubReplyURIsForFetch(actor.URI, note.Replies)
	collectionURI := note.Replies.ID
	collectionURIPresent := strings.TrimSpace(collectionURI) != ""
	if len(uris) == 0 && !collectionURIPresent {
		return
	}
	parentURI := ""
	if parent.URI.Valid {
		parentURI = parent.URI.String
	}
	requestID := remoteStatusDiscoveryRequestID("", firstNonEmpty(note.ID, parentURI))
	if len(uris) == 0 && collectionURIPresent && activityURIHostsMatch(actor.URI, collectionURI) && s.enqueueFetchRepliesTask(parent.ID, collectionURI, requestID) {
		return
	}
	go func() {
		if len(uris) == 0 && collectionURIPresent {
			uris = s.fetchActivityPubReplyCollectionURIs(actor.URI, collectionURI, paonUserAgent(s.cfg))
		}
		// Enqueue a retry-backed fetch task per reply URI, mirroring Rails
		// FetchReplyWorker.perform_async (pull queue, retry 3), instead of fetching inline.
		for _, uri := range uris {
			s.enqueueFetchReplyTask(uri, requestID)
		}
	}()
}

func activityPubReplyURIsForFetch(parentActorURI string, collection activityCollection) []string {
	if collection.FirstCollection != nil {
		return activityPubReplyURIsForFetch(parentActorURI, *collection.FirstCollection)
	}
	if collection.First != "" || collection.FirstPresent {
		return nil
	}
	candidates := collection.ItemURIs()
	out := make([]string, 0, min(activityPubFetchedRepliesLimit, len(candidates)))
	for _, uri := range candidates {
		if uri == "" || !activityURIHostsMatch(parentActorURI, uri) {
			continue
		}
		out = append(out, uri)
		if len(out) >= activityPubFetchedRepliesLimit {
			break
		}
	}
	return out
}

func (s *Server) fetchActivityPubReplyCollectionURIs(parentActorURI string, collectionURI string, userAgent string) []string {
	uris, _ := s.fetchActivityPubReplyCollectionURIsResult(parentActorURI, collectionURI, userAgent)
	return uris
}

func (s *Server) fetchActivityPubReplyCollectionURIsResult(parentActorURI string, collectionURI string, userAgent string) ([]string, error) {
	fetcher := fetchActivityResourceWithMetadataAndUserAgent
	if s != nil && s.db != nil {
		if signer, err := s.representativeActivityPubAccount(); err == nil && signer != nil {
			fetcher = func(uri string, userAgent string) (fetchedActivityResource, error) {
				return fetchActivityResourceWithMetadataAndUserAgentSigned(uri, userAgent, s, signer)
			}
		}
	}
	return fetchActivityPubReplyCollectionURIsWithFetcherResult(parentActorURI, collectionURI, userAgent, fetcher)
}

func fetchActivityPubReplyCollectionURIs(parentActorURI string, collectionURI string, userAgent string) []string {
	return fetchActivityPubReplyCollectionURIsWithFetcher(parentActorURI, collectionURI, userAgent, fetchActivityResourceWithMetadataAndUserAgent)
}

func fetchActivityPubReplyCollectionURIsWithFetcher(parentActorURI string, collectionURI string, userAgent string, fetcher activityResourceFetcher) []string {
	uris, _ := fetchActivityPubReplyCollectionURIsWithFetcherResult(parentActorURI, collectionURI, userAgent, fetcher)
	return uris
}

func fetchActivityPubReplyCollectionURIsWithFetcherResult(parentActorURI string, collectionURI string, userAgent string, fetcher activityResourceFetcher) ([]string, error) {
	if strings.TrimSpace(collectionURI) == "" || !activityURIHostsMatch(parentActorURI, collectionURI) {
		return nil, nil
	}
	collection, err := fetchActivityCollectionWithoutContextWithFetcher(collectionURI, userAgent, fetcher)
	if err != nil {
		return nil, err
	}
	if collection.FirstCollection != nil {
		collection = *collection.FirstCollection
	} else if collection.First != "" && activityURIHostsMatch(parentActorURI, collection.First) {
		first, err := fetchActivityCollectionWithoutContextWithFetcher(collection.First, userAgent, fetcher)
		if err != nil {
			return nil, err
		}
		collection = first
	} else if collection.FirstPresent {
		return nil, nil
	}
	return activityPubReplyURIsForFetch(parentActorURI, collection), nil
}

func fetchActivityCollection(collectionURI string, userAgent string) (activityCollection, error) {
	return fetchActivityCollectionWithFetcher(collectionURI, userAgent, fetchActivityResourceWithMetadataAndUserAgent)
}

func fetchActivityCollectionWithFetcher(collectionURI string, userAgent string, fetcher activityResourceFetcher) (activityCollection, error) {
	return fetchActivityCollectionWithContextRequirement(collectionURI, userAgent, fetcher, true)
}

func fetchActivityCollectionWithoutContextWithFetcher(collectionURI string, userAgent string, fetcher activityResourceFetcher) (activityCollection, error) {
	return fetchActivityCollectionWithContextRequirement(collectionURI, userAgent, fetcher, false)
}

func fetchActivityCollectionWithContextRequirement(collectionURI string, userAgent string, fetcher activityResourceFetcher, requireContext bool) (activityCollection, error) {
	resource, err := fetcher(collectionURI, userAgent)
	if err != nil {
		return activityCollection{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(resource.body, &raw); err != nil {
		return activityCollection{}, err
	}
	if requireContext && !activityResourceSupportedContext(raw["@context"]) {
		return activityCollection{}, fmt.Errorf("unsupported activity context")
	}
	collection := activityCollectionValue(raw)
	switch collection.Type {
	case "Collection", "CollectionPage", "OrderedCollection", "OrderedCollectionPage":
		return collection, nil
	default:
		return activityCollection{}, fmt.Errorf("unsupported activity collection type")
	}
}

func activityURIHostsMatch(left string, right string) bool {
	if !activityPubHTTPURIAllowedRaw(right) {
		return false
	}
	leftURL, err := url.Parse(left)
	if err != nil || leftURL.Host == "" {
		return false
	}
	rightURL, err := url.Parse(right)
	if err != nil || rightURL.Host == "" {
		return false
	}
	return strings.EqualFold(leftURL.Hostname(), rightURL.Hostname())
}

func parseActivityPayload(body []byte) (activityPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return activityPayload{}, err
	}
	raw = activityJSONLDGraphActivity(raw)
	rawObject := activityJSONLDValue(raw, "object")
	object := parseActivityObject(rawObject)
	object = activityObjectWithOrderedLanguageMaps(body, object)
	activityTo := activityJSONLDStringList(raw, "to")
	if !object.ToSet {
		object.To = activityTo
	}
	activityCC := activityJSONLDStringList(raw, "cc")
	if !object.CCSet {
		object.CC = activityCC
	}
	to := activityTo
	if len(to) == 0 {
		to = object.To
	}
	cc := activityCC
	if len(cc) == 0 {
		cc = object.CC
	}
	rawActor := activityJSONLDValue(raw, "actor")
	rawID := activityJSONLDValue(raw, "id")
	payloadType := activityJSONLDActivityType(raw)
	objectIDRaws := activityPubObjectValueOrIDs(rawObject)
	if payloadType == "Flag" {
		objectIDRaws = activityPubFlagObjectValueOrIDs(rawObject)
	}
	payload := activityPayload{
		ID:              activityJSONLDID(raw),
		IDRaw:           activityJSONLDValueOrID(rawID),
		IDPresent:       rawID != nil,
		Type:            payloadType,
		Actor:           activityJSONLDObjectID(raw, "actor"),
		ActorRaw:        activityRailsValueOrID(rawActor),
		ActorPresent:    activityPubJSONLDPresent(rawActor),
		Content:         activityJSONLDString(raw, "content"),
		Published:       activityJSONLDString(raw, "published"),
		Signature:       activityLinkedDataSignatureValue(activityJSONLDValue(raw, "signature")),
		Object:          object,
		ObjectReference: activityPayloadObjectReference(rawObject),
		ObjectIDs:       activityPubObjectIDs(rawObject),
		ObjectIDRaws:    objectIDRaws,
		Target:          activityRailsValueOrID(activityJSONLDValue(raw, "target")),
		To:              to,
		CC:              cc,
		RawBody:         append([]byte(nil), body...),
	}
	payload.Items = parseActivityCollectionPayloads(body, payload.Type)
	return payload, nil
}

func activityJSONLDValueOrID(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case string:
		return typed
	case map[string]any:
		if value, ok := typed["id"]; ok {
			return stringValue(value)
		}
		if value := stringValue(typed["@id"]); value != "" {
			return value
		}
		return stringValue(typed["@value"])
	default:
		return ""
	}
}

func activityRailsValueOrID(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case string:
		return typed
	case map[string]any:
		return stringValue(typed["id"])
	default:
		return ""
	}
}

func activityRailsActorDevicesURL(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if !typed {
			return ""
		}
		return "t"
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case map[string]any:
		return activityRubyInspectMap(typed)
	case []any:
		return activityRubyInspectArray(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func activityRubyInspectMap(value map[string]any) string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strconv.Quote(key)+"=>"+activityRubyInspectValue(value[key]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func activityRubyInspectArray(value []any) string {
	parts := make([]string, 0, len(value))
	for _, item := range value {
		parts = append(parts, activityRubyInspectValue(item))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func activityRubyInspectValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "nil"
	case string:
		return strconv.Quote(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case map[string]any:
		return activityRubyInspectMap(typed)
	case []any:
		return activityRubyInspectArray(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func activityLinkedDataSignatureValue(value any) activityLinkedDataSignature {
	switch typed := activityJSONLDSingle(value).(type) {
	case map[string]any:
		return activityLinkedDataSignature{
			Type:           activityJSONLDType(typed),
			Creator:        activityJSONLDValueOrID(activityJSONLDValue(typed, "creator")),
			Created:        activityJSONLDString(typed, "created"),
			SignatureValue: activityJSONLDString(typed, "signatureValue"),
			Present:        true,
		}
	default:
		return activityLinkedDataSignature{}
	}
}

func activityPayloadObjectReference(value any) bool {
	_, ok := activityJSONLDSingle(value).(string)
	return ok
}

func activityObjectWithOrderedLanguageMaps(body []byte, object activityObject) activityObject {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return object
	}
	objectRaw := activityJSONLDRawValue(raw, "object")
	if len(objectRaw) == 0 || string(objectRaw) == "null" {
		objectRaw = body
	}
	return activityObjectApplyOrderedLanguageMaps(objectRaw, object)
}

func activityObjectApplyOrderedLanguageMaps(raw json.RawMessage, object activityObject) activityObject {
	raw = firstActivityObjectRaw(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return object
	}
	if key, value := firstActivityLanguageMapEntryRaw(activityObjectRawValue(raw, "nameMap")); key != "" {
		object.NameMapFirstKey = key
		object.NameMapFirst = value
	}
	if key, value := firstActivityLanguageMapEntryRaw(activityObjectRawValue(raw, "contentMap")); key != "" {
		object.ContentMapFirstKey = key
		object.ContentMapFirst = value
	}
	if key, value := firstActivityLanguageMapEntryRaw(activityObjectRawValue(raw, "summaryMap")); key != "" {
		object.SummaryMapFirstKey = key
		object.SummaryMapFirst = value
	}
	return object
}

func firstActivityObjectRaw(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(bytes.TrimSpace(raw))
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '[' {
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil || len(values) == 0 {
			return nil
		}
		return firstActivityObjectRaw(values[0])
	}
	if raw[0] != '{' {
		return nil
	}
	if list := activityObjectRawValue(raw, "@list"); len(list) > 0 {
		return firstActivityObjectRaw(list)
	}
	return raw
}

func activityObjectRawValue(raw json.RawMessage, key string) json.RawMessage {
	properties, err := orderedJSONProperties(raw)
	if err != nil {
		return nil
	}
	for _, property := range properties {
		if property.key == key {
			return property.value
		}
	}
	for _, iri := range activityJSONLDTermIRIs(key) {
		for _, property := range properties {
			if property.key == iri {
				return property.value
			}
		}
	}
	return nil
}

type orderedJSONProperty struct {
	key   string
	value json.RawMessage
}

func orderedJSONProperties(raw json.RawMessage) ([]orderedJSONProperty, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("expected object")
	}
	properties := make([]orderedJSONProperty, 0)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("expected object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		properties = append(properties, orderedJSONProperty{key: key, value: value})
	}
	return properties, nil
}

func firstActivityLanguageMapEntryRaw(raw json.RawMessage) (string, string) {
	raw = json.RawMessage(bytes.TrimSpace(raw))
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	if raw[0] == '[' {
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return "", ""
		}
		for _, value := range values {
			lang, text := firstActivityLanguageMapExpandedEntry(value)
			if lang != "" {
				return lang, text
			}
		}
		return "", ""
	}
	if raw[0] != '{' {
		return "", ""
	}
	if list := activityObjectRawValue(raw, "@list"); len(list) > 0 {
		return firstActivityLanguageMapEntryRaw(list)
	}
	properties, err := orderedJSONProperties(raw)
	if err != nil {
		return "", ""
	}
	for _, property := range properties {
		if strings.TrimSpace(property.key) == "" {
			continue
		}
		var value any
		if err := json.Unmarshal(property.value, &value); err != nil {
			continue
		}
		return property.key, activityJSONLDStringValue(value)
	}
	return "", ""
}

func firstActivityLanguageMapExpandedEntry(raw json.RawMessage) (string, string) {
	raw = json.RawMessage(bytes.TrimSpace(raw))
	if len(raw) == 0 || raw[0] != '{' {
		return "", ""
	}
	properties, err := orderedJSONProperties(raw)
	if err != nil {
		return "", ""
	}
	lang := ""
	var value any
	for _, property := range properties {
		switch property.key {
		case "@language":
			var text string
			if err := json.Unmarshal(property.value, &text); err == nil {
				lang = strings.TrimSpace(text)
			}
		case "@value":
			_ = json.Unmarshal(property.value, &value)
		}
	}
	if lang == "" {
		return "", ""
	}
	return lang, activityJSONLDStringValue(value)
}

func parseActivityCollectionPayloads(body []byte, typ string) []activityPayload {
	switch typ {
	case "Collection", "CollectionPage", "OrderedCollection", "OrderedCollectionPage":
	default:
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	key := "items"
	if typ == "OrderedCollection" || typ == "OrderedCollectionPage" {
		key = "orderedItems"
	}
	itemsRaw := activityJSONLDRawValue(raw, key)
	if len(itemsRaw) == 0 || string(itemsRaw) == "null" {
		return nil
	}
	itemBodies := activityCollectionRawItems(itemsRaw)
	out := make([]activityPayload, 0, len(itemBodies))
	for _, itemBody := range itemBodies {
		var item map[string]any
		if err := json.Unmarshal(itemBody, &item); err != nil || item == nil {
			continue
		}
		payload, err := parseActivityPayload(itemBody)
		if err == nil && payload.Type != "" {
			out = append(out, payload)
		}
	}
	return out
}

func activityCollectionRawItems(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err == nil {
		out := make([]json.RawMessage, 0, len(items))
		for _, item := range items {
			if listItems := activityCollectionRawListItems(item); len(listItems) > 0 {
				out = append(out, listItems...)
				continue
			}
			out = append(out, item)
		}
		return out
	}
	if listItems := activityCollectionRawListItems(raw); len(listItems) > 0 {
		return listItems
	}
	return []json.RawMessage{raw}
}

func activityCollectionRawListItems(raw json.RawMessage) []json.RawMessage {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil
	}
	listRaw := object["@list"]
	if len(listRaw) == 0 || string(listRaw) == "null" {
		return nil
	}
	return activityCollectionRawItems(listRaw)
}

func activityJSONLDRawValue(object map[string]json.RawMessage, key string) json.RawMessage {
	if value := object[key]; len(value) > 0 {
		return value
	}
	for _, iri := range activityJSONLDTermIRIs(key) {
		if value := object[iri]; len(value) > 0 {
			return value
		}
	}
	return nil
}

func activityPubObjectIDs(value any) []string {
	values := activityJSONLDListItems(value)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, item := range values {
		id := activityPubObjectID(item)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func activityPubObjectValueOrIDs(value any) []string {
	values := activityJSONLDListItems(value)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, item := range values {
		id := activityJSONLDValueOrID(item)
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

func activityPubFlagObjectValueOrIDs(value any) []string {
	values := activityJSONLDListItems(value)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, item := range values {
		id := activityPubFlagValueOrID(item)
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

func activityPubFlagValueOrID(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case string:
		return typed
	case map[string]any:
		return stringValue(typed["id"])
	default:
		return ""
	}
}

func activityCollectionURI(value any) string {
	uri := activityJSONLDValueOrID(activityFirstCollectionURIValue(value))
	if uri == "" {
		return ""
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return uri
}

func activityFirstCollectionURIValue(value any) any {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			return nil
		}
		return activityFirstCollectionURIValue(typed[0])
	case map[string]any:
		if raw, ok := typed["@list"]; ok {
			return activityFirstCollectionURIValue(raw)
		}
		return typed
	default:
		return value
	}
}

func parseActivityObject(value any) activityObject {
	switch object := value.(type) {
	case string:
		return activityObject{ID: activityURIFromBearcapRaw(object), IDPresent: true, Reference: true}
	case []any:
		if len(object) == 0 {
			return activityObject{}
		}
		return parseActivityObject(object[0])
	case map[string]any:
		if graphObject := activityJSONLDGraphObject(object); graphObject != nil {
			return parseActivityObject(graphObject)
		}
		if _, ok := object["@list"]; ok {
			items := activityJSONLDListItems(object)
			if len(items) == 0 {
				return activityObject{}
			}
			return parseActivityObject(items[0])
		}
		id := activityURIFromBearcapRaw(activityJSONLDIDRaw(object))
		objectIDValue := activityJSONLDValue(object, "object")
		featuredValue := activityJSONLDValue(object, "featured")
		oneOfPollOptions := activityPollOptionListWithShape(activityJSONLDValue(object, "oneOf"))
		anyOfPollOptions := activityPollOptionListWithShape(activityJSONLDValue(object, "anyOf"))
		toValue := activityJSONLDValue(object, "to")
		ccValue := activityJSONLDValue(object, "cc")
		out := activityObject{
			ID:               id,
			IDPresent:        activityJSONLDValue(object, "id") != nil || activityJSONLDValue(object, "@id") != nil,
			Type:             activityJSONLDType(object),
			TypeExact:        activityJSONLDActivityType(object),
			TypePresent:      activityJSONLDValue(object, "type") != nil || activityJSONLDValue(object, "@type") != nil,
			Types:            activityJSONLDTypes(object),
			AtomURI:          activityJSONLDString(object, "atomUri"),
			Featured:         activityCollectionURI(featuredValue),
			FeaturedTags:     activityCollectionURI(activityJSONLDValue(object, "featuredTags")),
			Devices:          activityRailsActorDevicesURL(activityJSONLDValue(object, "devices")),
			Inbox:            activityActorCollectionURI(activityJSONLDValue(object, "inbox")),
			Outbox:           activityActorCollectionURI(activityJSONLDValue(object, "outbox")),
			Following:        activityActorCollectionURI(activityJSONLDValue(object, "following")),
			Followers:        activityActorCollectionURI(activityJSONLDValue(object, "followers")),
			SharedInbox:      activityActorSharedInboxURLFromObject(object),
			Actor:            activityJSONLDObjectID(object, "actor"),
			ActorRaw:         activityJSONLDValueOrID(activityJSONLDValue(object, "actor")),
			ObjectID:         activityJSONLDObjectID(object, "object"),
			ObjectIDRaw:      activityJSONLDValueOrID(objectIDValue),
			ObjectIDPresent:  objectIDValue != nil,
			AttributedTo:     activityJSONLDObjectIDFirst(object, "attributedTo"),
			AttributedToRaw:  activityJSONLDValueOrID(activityJSONLDValue(object, "attributedTo")),
			URL:              activityActorOrStatusURL(activityJSONLDValue(object, "url"), id, activityJSONLDType(object), activityJSONLDTypes(object)),
			Name:             activityJSONLDString(object, "name"),
			NameMap:          activityJSONLDStringMap(object, "nameMap"),
			Content:          sanitizeRemoteNoteContent(activityJSONLDString(object, "content")),
			ContentMap:       activityJSONLDStringMap(object, "contentMap"),
			Summary:          activityJSONLDString(object, "summary"),
			SummaryMap:       activityJSONLDStringMap(object, "summaryMap"),
			InReplyTo:        activityJSONLDValueOrID(activityJSONLDValue(object, "inReplyTo")),
			InReplyToPresent: activityPubJSONLDPresent(activityJSONLDValue(object, "inReplyTo")),
			InReplyToAtomURI: activityJSONLDString(object, "inReplyToAtomUri"),
			Conversation:     activityJSONLDValueOrID(activityJSONLDValue(object, "conversation")),
			ConversationSet:  activityJSONLDValue(object, "conversation") != nil,
			Published:        activityJSONLDString(object, "published"),
			Updated:          activityJSONLDString(object, "updated"),
			Language:         activityJSONLDString(object, "language"),
			Sensitive:        activityBoolValue(activityJSONLDValue(object, "sensitive")),
			Locked:           activityBoolValue(activityJSONLDValue(object, "manuallyApprovesFollowers")),
			Discoverable:     activityBoolValue(activityJSONLDValue(object, "discoverable")),
			Indexable:        activityBoolValue(activityJSONLDValue(object, "indexable")),
			Memorial:         activityBoolValue(activityJSONLDValue(object, "memorial")),
			Suspended:        activityRailsSuspendedTruthy(activityJSONLDValue(object, "suspended")),
			Closed:           activityJSONLDValue(object, "closed"),
			EndTime:          activityJSONLDString(object, "endTime"),
			OneOf:            oneOfPollOptions.Options,
			OneOfSet:         oneOfPollOptions.Set,
			OneOfArray:       oneOfPollOptions.Array,
			AnyOf:            anyOfPollOptions.Options,
			AnyOfSet:         anyOfPollOptions.Set,
			AnyOfArray:       anyOfPollOptions.Array,
			VotersCount:      activityOptionalInt64(activityJSONLDValue(object, "votersCount")),
			To:               activityStringList(toValue),
			ToSet:            activityRubyTruthy(toValue),
			CC:               activityStringList(ccValue),
			CCSet:            activityRubyTruthy(ccValue),
			Tags:             activityRailsTagList(activityJSONLDValue(object, "tag")),
			Attachments:      activityAttachmentList(activityJSONLDValue(object, "attachment")),
			Replies:          activityCollectionValue(activityJSONLDValue(object, "replies")),
			ProfileFields:    activityProfileFields(activityJSONLDValue(object, "attachment")),
			ProfileFieldsSet: activityAttachmentPresent(activityJSONLDValue(object, "attachment")),
			PublicKey:        activityPublicKeyPEM(activityJSONLDValue(object, "publicKey")),
			AvatarRemoteURL:  activityActorImageURL(activityJSONLDValue(object, "icon")),
			HeaderRemoteURL:  activityActorImageURL(activityJSONLDValue(object, "image")),
			SourceDeviceID:   activityDeviceID(activityJSONLDValue(object, "attributedTo")),
			TargetDeviceID:   activityDeviceID(activityJSONLDValue(object, "to")),
			MessageType:      int(activityInt64Value(activityJSONLDValue(object, "messageType"))),
			CipherText:       activityJSONLDString(object, "cipherText"),
			MessageFranking:  activityJSONLDString(object, "messageFranking"),
			DigestValue:      activityDigestValue(activityJSONLDValue(object, "digest")),
			QuoteURI:         activityJSONLDObjectID(object, "quoteUri"),
			QuoteURL:         activityJSONLDObjectID(object, "quoteUrl"),
			MisskeyQuote:     activityJSONLDObjectID(object, "_misskey_quote"),
			QuoteURIRaw:      activityJSONLDObjectIDPreserveBearcap(object, "quoteUri"),
			QuoteURLRaw:      activityJSONLDObjectIDPreserveBearcap(object, "quoteUrl"),
			MisskeyQuoteRaw:  activityJSONLDObjectIDPreserveBearcap(object, "_misskey_quote"),
			QuoteURISet:      activityJSONLDValue(object, "quoteUri") != nil,
			QuoteURLSet:      activityJSONLDValue(object, "quoteUrl") != nil,
			MisskeyQuoteSet:  activityJSONLDValue(object, "_misskey_quote") != nil,
			AlsoKnownAs:      activityJSONLDValue(object, "alsoKnownAs"),
			MovedTo:          activityJSONLDValue(object, "movedTo"),
		}
		out.FeaturedCollection = activityCollectionInlinePage(featuredValue)
		return out
	default:
		return activityObject{}
	}
}

func activityCollectionValue(value any) activityCollection {
	items := activityJSONLDListItems(value)
	if len(items) == 0 {
		return activityCollection{}
	}
	value = items[0]
	switch typed := value.(type) {
	case string:
		return activityCollection{ID: typed}
	case map[string]any:
		firstValue := activityJSONLDValue(typed, "first")
		nextValue := activityJSONLDValue(typed, "next")
		first, firstPresent := activityCollectionPagePointer(firstValue)
		next, nextPresent := activityCollectionPagePointer(nextValue)
		return activityCollection{
			ID:               activityJSONLDID(typed),
			Type:             activityJSONLDType(typed),
			First:            first,
			FirstPresent:     firstPresent,
			FirstCollection:  activityCollectionInlinePage(firstValue),
			Next:             next,
			NextPresent:      nextPresent,
			NextCollection:   activityCollectionInlinePage(nextValue),
			Items:            activityCollectionItemURIs(activityJSONLDValue(typed, "items")),
			OrderedItems:     activityCollectionItemURIs(activityJSONLDValue(typed, "orderedItems")),
			NoteItems:        activityCollectionNoteItemURIs(activityJSONLDValue(typed, "items")),
			OrderedNoteItems: activityCollectionNoteItemURIs(activityJSONLDValue(typed, "orderedItems")),
			Tags:             activityCollectionTags(typed),
		}
	default:
		return activityCollection{}
	}
}

func activityCollectionPagePointer(value any) (string, bool) {
	items := activityJSONLDListItems(value)
	if len(items) == 0 {
		return "", false
	}
	switch typed := items[0].(type) {
	case nil:
		return "", false
	case string:
		if strings.TrimSpace(typed) == "" {
			return "", false
		}
		return typed, true
	case bool:
		return "", typed
	case map[string]any:
		return "", true
	default:
		return "", true
	}
}

func activityCollectionInlinePage(value any) *activityCollection {
	items := activityJSONLDListItems(value)
	if len(items) == 0 {
		return nil
	}
	typed, ok := items[0].(map[string]any)
	if !ok {
		return nil
	}
	collection := activityCollectionValue(typed)
	switch collection.Type {
	case "Collection", "CollectionPage", "OrderedCollection", "OrderedCollectionPage":
		return &collection
	default:
		return nil
	}
}

func (c activityCollection) ItemURIs() []string {
	if len(c.Items) > 0 {
		return c.Items
	}
	return c.OrderedItems
}

func (c activityCollection) NoteItemURIs() []string {
	if len(c.NoteItems) > 0 {
		return c.NoteItems
	}
	return c.OrderedNoteItems
}

func activityCollectionItemURIs(value any) []string {
	return activityPubObjectValueOrIDs(value)
}

func activityCollectionNoteItemURIs(value any) []string {
	items := activityJSONLDListItems(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if id := activityCollectionNoteItemURI(item); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func activityCollectionNoteItemURI(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if activityJSONLDType(typed) == "Note" {
			return activityJSONLDValueOrID(typed)
		}
	}
	return ""
}

func activityCollectionTags(object map[string]any) []activityTag {
	tags := activityTagList(activityJSONLDValue(object, "items"))
	if len(tags) == 0 {
		tags = activityTagList(activityJSONLDValue(object, "orderedItems"))
	}
	return tags
}

func activityPollOptionListWithShape(value any) activityPollOptionSource {
	if value == nil {
		return activityPollOptionSource{}
	}
	values, ok := activityPollOptionArrayItems(value)
	if !ok {
		return activityPollOptionSource{Set: true}
	}
	return activityPollOptionSource{Options: activityPollOptionList(values), Set: true, Array: true}
}

func activityPollOptionArrayItems(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 1 {
			if object, ok := typed[0].(map[string]any); ok {
				if raw, ok := object["@list"]; ok {
					return activityPollOptionArrayItems(raw)
				}
			}
		}
		return typed, true
	case map[string]any:
		if raw, ok := typed["@list"]; ok {
			return activityPollOptionArrayItems(raw)
		}
		return nil, false
	default:
		return nil, false
	}
}

func activityPollOptionList(values []any) []activityPollOption {
	out := make([]activityPollOption, 0, len(values))
	for _, item := range values {
		if object, ok := activityJSONLDSingle(item).(map[string]any); ok {
			out = append(out, activityPollOption{
				Name:       activityJSONLDString(object, "name"),
				Content:    activityJSONLDString(object, "content"),
				ContentSet: activityJSONLDValue(object, "content") != nil,
				TotalItems: activityInt64Value(nestedActivityJSONLDValue(object, "replies", "totalItems")),
			})
		}
	}
	return out
}

func activityPublicKeyPEM(value any) string {
	object, ok := activityJSONLDSingle(value).(map[string]any)
	if !ok {
		return ""
	}
	return activityJSONLDString(object, "publicKeyPem")
}

func activityTagList(value any) []activityTag {
	values := activityObjectList(value)
	out := make([]activityTag, 0, len(values))
	for _, item := range values {
		out = append(out, activityTagFromObject(item))
	}
	return out
}

func activityRailsTagList(value any) []activityTag {
	items := activityRailsArrayItems(value)
	out := make([]activityTag, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, activityTagFromObject(item))
	}
	return out
}

func activityTagFromObject(item map[string]any) activityTag {
	return activityTag{
		ID:      activityPubObjectID(item),
		Type:    activityJSONLDType(item),
		Types:   activityJSONLDTypes(item),
		Name:    activityJSONLDString(item, "name"),
		NameSet: activityJSONLDValue(item, "name") != nil,
		Href:    activityTagHref(activityJSONLDValue(item, "href")),
		IconURL: activityTagIconURL(activityJSONLDValue(item, "icon")),
		Updated: activityJSONLDString(item, "updated"),
	}
}

func activityTagHref(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case string:
		return typed
	case map[string]any:
		if value := stringValue(typed["@value"]); value != "" {
			return value
		}
		return activityPubObjectID(typed["@id"])
	default:
		return ""
	}
}

func activityTagIconURL(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case map[string]any:
		rawURL, expanded := activityJSONLDValueWithExpandedIRI(typed, "url")
		return activityAttachmentURLWithExpandedID(rawURL, expanded)
	default:
		return ""
	}
}

func activityAttachmentList(value any) []activityAttachment {
	items := activityRailsArrayItems(value)
	out := make([]activityAttachment, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := activityJSONLDString(item, "name")
		summary := activityJSONLDString(item, "summary")
		rawURL, rawURLExpanded := activityJSONLDValueWithExpandedIRI(item, "url")
		mediaType := activityAttachmentMediaType(item, rawURL)
		url := activityMediaAttachmentURLWithExpandedID(rawURL, rawURLExpanded)
		if strings.TrimSpace(url) == "" {
			continue
		}
		out = append(out, activityAttachment{
			Type:        activityJSONLDType(item),
			MediaType:   mediaType,
			URL:         url,
			Name:        name,
			Summary:     summary,
			Description: activityAttachmentDescription(summary, name),
			Blurhash:    activityAttachmentBlurhash(activityJSONLDString(item, "blurhash")),
			IconURL:     activityAttachmentIconURL(activityJSONLDValue(item, "icon")),
			Focus:       activityAttachmentFocus(activityJSONLDValue(item, "focalPoint")),
		})
	}
	return out
}

func activityAttachmentMediaType(item map[string]any, rawURL any) string {
	if value := activityJSONLDValue(item, "mediaType"); value != nil {
		return activityJSONLDStringValue(value)
	}
	return activityAttachmentURLMediaType(rawURL)
}

func activityAttachmentIconURL(value any) string {
	switch typed := value.(type) {
	case string:
		return activityNormalizedHTTPURI(typed)
	case map[string]any:
		rawURL, expanded := activityJSONLDValueWithExpandedIRI(typed, "url")
		return activityMediaAttachmentURLWithExpandedID(rawURL, expanded)
	default:
		return ""
	}
}

func activityStatusURL(value any) string {
	raw := activityStatusURLHref(value, "text/html")
	if !activityPubHTTPURIAllowedRaw(raw) {
		return ""
	}
	return raw
}

func activityActorOrStatusURL(value any, actorURI string, typ string, types []string) string {
	raw := activityStatusURL(value)
	if raw == "" {
		return ""
	}
	if !activityTypesAreActor(typ, types) {
		return raw
	}
	if activityURIHostsMatch(actorURI, raw) {
		return raw
	}
	return ""
}

func activityTypesAreActor(typ string, types []string) bool {
	for _, actorType := range []string{"Application", "Group", "Organization", "Person", "Service"} {
		if activityTypesInclude(typ, types, actorType) {
			return true
		}
	}
	return false
}

func activityActorTypeValue(types []string) string {
	for _, typ := range types {
		if activityActorTypeSupported(typ) {
			return typ
		}
	}
	return ""
}

func activityActorTypeValueOrDefault(types []string) string {
	if typ := activityActorTypeValue(types); typ != "" {
		return typ
	}
	return "Person"
}

func activityStatusURLHref(value any, preferredType string) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if _, ok := typed["@list"]; ok {
			return activityStatusURLHref(activityJSONLDListItems(typed), preferredType)
		}
		if preferredType == "" || activityURLLinkMimeType(typed) == preferredType {
			return activityJSONLDString(typed, "href")
		}
	case []any:
		items := activityJSONLDListItems(typed)
		if len(items) == 0 {
			return ""
		}
		if _, ok := items[0].(string); ok {
			return stringValue(items[0])
		}
		for _, item := range items {
			object, ok := activityJSONLDSingle(item).(map[string]any)
			if !ok {
				continue
			}
			if preferredType == "" || activityURLLinkMimeType(object) == preferredType {
				return activityJSONLDString(object, "href")
			}
		}
	}
	return ""
}

func activityURLLinkMimeType(link map[string]any) string {
	mimeType := activityJSONLDString(link, "mimeType")
	if strings.TrimSpace(mimeType) == "" {
		return "text/html"
	}
	return mimeType
}

func activityAttachmentURL(value any) string {
	return activityAttachmentURLWithExpandedID(value, false)
}

func activityAttachmentURLWithExpandedID(value any, allowExpandedID bool) string {
	url := activityAttachmentURLHref(value)
	if allowExpandedID && url == "" {
		url = activityJSONLDIDOnlyRaw(value)
	}
	return activityNormalizedHTTPURIRaw(url)
}

func activityMediaAttachmentURL(value any) string {
	return activityMediaAttachmentURLWithExpandedID(value, false)
}

func activityMediaAttachmentURLWithExpandedID(value any, allowExpandedID bool) string {
	url := activityMediaAttachmentURLHref(value)
	if allowExpandedID && url == "" {
		url = activityJSONLDIDOnlyRaw(value)
	}
	return activityNormalizedHTTPURIRaw(url)
}

func activityMediaAttachmentURLHref(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if _, ok := typed["@list"]; ok {
			return activityMediaAttachmentURLHref(activityJSONLDListItems(typed))
		}
		return activityJSONLDString(typed, "href")
	case []any:
		items := activityJSONLDListItems(typed)
		if len(items) == 0 {
			return ""
		}
		if _, ok := items[0].(string); ok {
			return stringValue(items[0])
		}
		object, ok := activityJSONLDSingle(items[0]).(map[string]any)
		if !ok {
			return ""
		}
		return activityMediaAttachmentURLHref(object)
	}
	return ""
}

func activityAttachmentURLHref(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if _, ok := typed["@list"]; ok {
			return activityAttachmentURLHref(activityJSONLDListItems(typed))
		}
		return activityJSONLDString(typed, "href")
	case []any:
		items := activityJSONLDListItems(typed)
		if len(items) == 0 {
			return ""
		}
		if _, ok := items[0].(string); ok {
			return stringValue(items[0])
		}
		object, ok := activityJSONLDSingle(items[0]).(map[string]any)
		if !ok {
			return ""
		}
		return activityAttachmentURLHref(object)
	}
	return ""
}

func activityAttachmentURLMediaType(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if _, ok := typed["@list"]; ok {
			return activityAttachmentURLMediaType(activityJSONLDListItems(typed))
		}
		return activityJSONLDString(typed, "mediaType")
	case []any:
		items := activityJSONLDListItems(typed)
		if len(items) == 0 {
			return ""
		}
		return activityAttachmentURLMediaType(items[0])
	}
	return ""
}

func activityPubHTTPURIAllowed(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func activityPubHTTPURIAllowedRaw(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func activityNormalizedHTTPURI(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := normalizeDeliveryStatsHost(parsed.Hostname())
	if host != "" {
		if port := parsed.Port(); port != "" {
			host += ":" + port
		}
		parsed.Host = host
	} else {
		parsed.Host = strings.ToLower(parsed.Host)
	}
	return parsed.String()
}

func activityNormalizedHTTPURIRaw(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := normalizeDeliveryStatsHost(parsed.Hostname())
	if host != "" {
		if port := parsed.Port(); port != "" {
			host += ":" + port
		}
		parsed.Host = host
	} else {
		parsed.Host = strings.ToLower(parsed.Host)
	}
	return parsed.String()
}

const activityPubMediaAttachmentMaxDescriptionLength = 1500

func activityAttachmentDescription(summary string, name string) string {
	description := strings.TrimSpace(summary)
	if description == "" {
		description = strings.TrimSpace(name)
	}
	if description == "" {
		return ""
	}
	runes := []rune(description)
	if len(runes) > activityPubMediaAttachmentMaxDescriptionLength {
		return string(runes[:activityPubMediaAttachmentMaxDescriptionLength])
	}
	return description
}

func activityAttachmentBlurhash(raw string) string {
	if strings.TrimSpace(raw) == "" || !activityBlurhashSupported(raw) {
		return ""
	}
	return raw
}

func activityBlurhashSupported(raw string) bool {
	if raw == "" {
		return false
	}
	sizeFlag := strings.IndexRune(blurhashAlphabet, rune(raw[0]))
	if sizeFlag < 0 {
		return false
	}
	componentsX := (sizeFlag % 9) + 1
	componentsY := (sizeFlag / 9) + 1
	if componentsX > 5 || componentsY > 5 {
		return false
	}
	if len(raw) != 4+2*componentsX*componentsY {
		return false
	}
	for _, r := range raw {
		if !strings.ContainsRune(blurhashAlphabet, r) {
			return false
		}
	}
	return true
}

func activityAttachmentFocus(value any) string {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return ""
		}
		return activityAttachmentFocusFromString(typed)
	case []any:
		if len(typed) == 1 {
			if value := activityAttachmentFocus(typed[0]); value != "" {
				return value
			}
		}
		return activityAttachmentFocusFromList(typed)
	case map[string]any:
		if raw, ok := typed["@list"]; ok {
			return activityAttachmentFocus(raw)
		}
	}
	return ""
}

func activityAttachmentFocusFromString(value string) string {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return ""
	}
	return strconv.FormatFloat(railsFloat64FromString(parts[0]), 'f', -1, 64) + "," + strconv.FormatFloat(railsFloat64FromString(parts[1]), 'f', -1, 64)
}

func activityAttachmentFocusFromList(values []any) string {
	if len(values) < 2 {
		return ""
	}
	x, ok := activityFloatValue(values[0])
	if !ok {
		return ""
	}
	y, ok := activityFloatValue(values[1])
	if !ok {
		return ""
	}
	return strconv.FormatFloat(x, 'f', -1, 64) + "," + strconv.FormatFloat(y, 'f', -1, 64)
}

func activityFloatValue(value any) (float64, bool) {
	switch typed := activityJSONLDSingle(value).(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	case map[string]any:
		return activityFloatValue(typed["@value"])
	case string:
		if strings.TrimSpace(typed) != "" {
			return railsFloat64FromString(typed), true
		}
		return 0, true
	default:
		return 0, false
	}
}

func railsFloat64FromString(raw string) float64 {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	end := 0
	seenDigit := false
	if value[end] == '+' || value[end] == '-' {
		end++
	}
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
		seenDigit = true
	}
	if end < len(value) && value[end] == '.' {
		end++
		for end < len(value) && value[end] >= '0' && value[end] <= '9' {
			end++
			seenDigit = true
		}
	}
	if !seenDigit {
		return 0
	}
	if end < len(value) && (value[end] == 'e' || value[end] == 'E') {
		expEnd := end + 1
		if expEnd < len(value) && (value[expEnd] == '+' || value[expEnd] == '-') {
			expEnd++
		}
		expDigitsStart := expEnd
		for expEnd < len(value) && value[expEnd] >= '0' && value[expEnd] <= '9' {
			expEnd++
		}
		if expEnd > expDigitsStart {
			end = expEnd
		}
	}
	parsed, err := strconv.ParseFloat(value[:end], 64)
	if err != nil {
		return 0
	}
	return parsed
}

func activityPubMediaMetaWithFocus(raw []byte, focus string) ([]byte, bool) {
	if strings.TrimSpace(focus) == "" {
		return nil, false
	}
	return mediaMetaWithFocus(raw, focus)
}

func activityObjectList(value any) []map[string]any {
	items := activityJSONLDListItems(value)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := activityJSONLDSingle(item).(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func nestedActivityValue(item map[string]any, keys ...string) any {
	var current any = item
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}

func nestedActivityJSONLDValue(item map[string]any, keys ...string) any {
	var current any = item
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = activityJSONLDValue(object, key)
		if array, ok := current.([]any); ok && len(array) == 1 {
			current = array[0]
		}
	}
	return current
}

func stringValue(value any) string {
	out, _ := value.(string)
	return out
}

func activityJSONLDValue(object map[string]any, key string) any {
	if value, ok := object[key]; ok && value != nil {
		return value
	}
	for _, iri := range activityJSONLDTermIRIs(key) {
		if value, ok := object[iri]; ok && value != nil {
			return value
		}
	}
	return nil
}

func activityJSONLDValueWithExpandedIRI(object map[string]any, key string) (any, bool) {
	if value, ok := object[key]; ok && value != nil {
		return value, false
	}
	for _, iri := range activityJSONLDTermIRIs(key) {
		if value, ok := object[iri]; ok && value != nil {
			return value, strings.HasPrefix(iri, "http://") || strings.HasPrefix(iri, "https://")
		}
	}
	return nil, false
}

func activityJSONLDGraphActivity(object map[string]any) map[string]any {
	items := activityJSONLDGraphMaps(object)
	if len(items) == 0 {
		return object
	}
	for _, item := range items {
		if activityJSONLDTypeIsActivity(activityJSONLDType(item)) {
			return item
		}
	}
	return items[0]
}

func activityJSONLDGraphObject(object map[string]any) map[string]any {
	items := activityJSONLDGraphMaps(object)
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if activityJSONLDTypeIsStatusObject(activityJSONLDType(item)) {
			return item
		}
	}
	for _, item := range items {
		if activityJSONLDTypeIsActorObject(activityJSONLDType(item)) {
			return item
		}
	}
	return items[0]
}

func activityJSONLDGraphActor(object map[string]any) map[string]any {
	items := activityJSONLDGraphMaps(object)
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if activityJSONLDTypeIsActorObject(activityJSONLDType(item)) {
			return item
		}
	}
	return nil
}

func activityJSONLDGraphMaps(object map[string]any) []map[string]any {
	raw, ok := object["@graph"]
	if !ok || raw == nil {
		return nil
	}
	items := activityJSONLDListItems(raw)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := activityJSONLDSingle(item).(map[string]any); ok && mapped != nil {
			out = append(out, mapped)
		}
	}
	return out
}

func activityJSONLDTypeIsActivity(value string) bool {
	switch value {
	case "Accept", "Add", "Announce", "Block", "Create", "Delete", "Flag", "Follow", "Like", "Move", "Reject", "Remove", "Undo", "Update", "View":
		return true
	default:
		return false
	}
}

func activityJSONLDTypeIsStatusObject(value string) bool {
	switch value {
	case "Note", "Question", "Article", "Page", "Image", "Video", "Audio", "Document", "Tombstone":
		return true
	default:
		return false
	}
}

func activityJSONLDTypeIsActorObject(value string) bool {
	switch value {
	case "Application", "Group", "Organization", "Person", "Service":
		return true
	default:
		return false
	}
}

func activityJSONLDSingle(value any) any {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			return nil
		}
		return activityJSONLDSingle(typed[0])
	case map[string]any:
		if raw, ok := typed["@list"]; ok {
			return activityJSONLDSingle(raw)
		}
		return typed
	default:
		return value
	}
}

func activityJSONLDListItems(value any) []any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		if len(typed) == 1 {
			if object, ok := typed[0].(map[string]any); ok {
				if raw, ok := object["@list"]; ok {
					return activityJSONLDListItems(raw)
				}
			}
		}
		return typed
	case map[string]any:
		if raw, ok := typed["@list"]; ok {
			return activityJSONLDListItems(raw)
		}
		return []any{typed}
	default:
		return []any{typed}
	}
}

func activityJSONLDTermIRI(key string) string {
	iris := activityJSONLDTermIRIs(key)
	if len(iris) == 0 {
		return ""
	}
	return iris[0]
}

func activityJSONLDTermIRIs(key string) []string {
	switch key {
	case "atomUri":
		return []string{"http://ostatus.org#atomUri", "ostatus:atomUri"}
	case "conversation":
		return []string{"http://ostatus.org#conversation", "https://www.w3.org/ns/activitystreams#conversation", "http://www.w3.org/ns/activitystreams#conversation", "ostatus:conversation", "as:conversation"}
	case "inReplyToAtomUri":
		return []string{"http://ostatus.org#inReplyToAtomUri", "ostatus:inReplyToAtomUri"}
	case "featured", "featuredTags", "blurhash", "claim", "discoverable", "devices", "fingerprintKey", "identityKey", "indexable", "manuallyApprovesFollowers", "memorial", "publicKeyBase64", "sensitive", "votersCount", "messageFranking", "messageType", "cipherText", "deviceId":
		return []string{"http://joinmastodon.org/ns#" + key, "https://joinmastodon.org/ns#" + key, "https://www.w3.org/ns/activitystreams#" + key, "http://www.w3.org/ns/activitystreams#" + key, "toot:" + key, "as:" + key}
	case "focalPoint":
		return []string{"http://joinmastodon.org/ns#focalPoint", "https://joinmastodon.org/ns#focalPoint", "https://www.w3.org/ns/activitystreams#focalPoint", "http://www.w3.org/ns/activitystreams#focalPoint", "toot:focalPoint", "as:focalPoint"}
	case "suspended":
		return []string{"http://joinmastodon.org/ns#suspended", "https://joinmastodon.org/ns#suspended", "https://www.w3.org/ns/activitystreams#suspended", "http://www.w3.org/ns/activitystreams#suspended", "toot:suspended", "as:suspended"}
	case "value":
		return []string{"http://schema.org#value", "https://schema.org#value", "https://schema.org/value", "https://www.w3.org/ns/activitystreams#value", "http://www.w3.org/ns/activitystreams#value", "schema:value", "as:value"}
	case "publicKey", "owner", "publicKeyPem", "signature", "signatureValue":
		return []string{"https://w3id.org/security#" + key, "http://w3id.org/security#" + key, "https://w3id.org/security/v1#" + key, "http://w3id.org/security/v1#" + key, "https://www.w3.org/ns/activitystreams#" + key, "http://www.w3.org/ns/activitystreams#" + key, "sec:" + key, "security:" + key, "as:" + key}
	case "creator":
		return []string{"http://purl.org/dc/terms/creator", "dc:creator", "https://w3id.org/security#creator", "http://w3id.org/security#creator", "https://w3id.org/security/v1#creator", "http://w3id.org/security/v1#creator", "https://www.w3.org/ns/activitystreams#creator", "http://www.w3.org/ns/activitystreams#creator", "sec:creator", "security:creator", "as:creator"}
	case "created":
		return []string{"http://purl.org/dc/terms/created", "dc:created", "https://w3id.org/security#created", "http://w3id.org/security#created", "https://w3id.org/security/v1#created", "http://w3id.org/security/v1#created", "https://www.w3.org/ns/activitystreams#created", "http://www.w3.org/ns/activitystreams#created", "sec:created", "security:created", "as:created"}
	case "digestValue", "digestAlgorithm":
		return []string{"https://www.w3.org/ns/activitystreams#" + key, "http://www.w3.org/ns/activitystreams#" + key, "as:" + key}
	case "href":
		return []string{"https://www.w3.org/ns/activitystreams#href", "http://www.w3.org/ns/activitystreams#href", "as:href"}
	case "quoteUri":
		return []string{"https://www.w3.org/ns/activitystreams#quoteUri", "http://www.w3.org/ns/activitystreams#quoteUri", "as:quoteUri", "http://joinmastodon.org/ns#quoteUri", "https://joinmastodon.org/ns#quoteUri", "toot:quoteUri", "https://w3id.org/fep/044f#quote", "fep:quote", "quote"}
	case "quoteUrl":
		return []string{"https://www.w3.org/ns/activitystreams#quoteUrl", "http://www.w3.org/ns/activitystreams#quoteUrl", "as:quoteUrl", "http://joinmastodon.org/ns#quoteUrl", "https://joinmastodon.org/ns#quoteUrl", "toot:quoteUrl", "https://w3id.org/fep/044f#quote", "fep:quote", "quote"}
	case "_misskey_quote":
		return []string{"https://misskey-hub.net/ns#_misskey_quote", "misskey:_misskey_quote"}
	case "interactionPolicy", "canQuote", "automaticApproval", "manualApproval":
		return []string{"https://gotosocial.org/ns#" + key, "gts:" + key}
	case "id", "type":
		return nil
	default:
		return []string{"https://www.w3.org/ns/activitystreams#" + key, "http://www.w3.org/ns/activitystreams#" + key, "as:" + key}
	}
}

func activityJSONLDID(object map[string]any) string {
	if value, ok := object["id"]; ok {
		return activityPubObjectID(value)
	}
	return activityPubObjectID(object["@id"])
}

func activityJSONLDIDRaw(object map[string]any) string {
	if value, ok := object["id"]; ok {
		return activityPubObjectIDRaw(value)
	}
	return activityPubObjectIDRaw(object["@id"])
}

func activityJSONLDIDOnlyRaw(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case map[string]any:
		if id, ok := typed["@id"]; ok && len(typed) == 1 {
			return activityPubObjectIDRaw(id)
		}
	case []any:
		items := activityJSONLDListItems(typed)
		if len(items) == 0 {
			return ""
		}
		return activityJSONLDIDOnlyRaw(items[0])
	}
	return ""
}

func activityURIFromBearcap(raw string) string {
	if !strings.HasPrefix(raw, "bear:") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.RawQuery == "" && !strings.Contains(raw, "?") {
		return raw
	}
	if values, ok := parsed.Query()["u"]; ok && len(values) > 0 {
		return values[0]
	}
	return ""
}

func activityURIFromBearcapRaw(raw string) string {
	if !strings.HasPrefix(raw, "bear:") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if uri := parsed.Query().Get("u"); uri != "" {
		return uri
	}
	return raw
}

func activityJSONLDType(object map[string]any) string {
	if typ := firstActivityKnownType(activityJSONLDTypes(object)); typ != "" {
		return typ
	}
	return ""
}

func activityJSONLDActivityType(object map[string]any) string {
	if object == nil {
		return ""
	}
	if typ := activityJSONLDActivityTypeScalar(object["type"]); typ != "" {
		return typ
	}
	if _, compactArray := object["type"].([]any); compactArray {
		return ""
	}
	types := activityTypeValues(object["@type"])
	if len(types) == 1 && activityKnownType(types[0]) {
		return types[0]
	}
	return ""
}

func activityJSONLDActivityTypeScalar(value any) string {
	switch typed := value.(type) {
	case string:
		typ := activityTypeValue(typed)
		if activityKnownType(typ) {
			return typ
		}
	case map[string]any:
		if raw := stringValue(typed["@id"]); raw != "" {
			typ := activityTypeValue(raw)
			if activityKnownType(typ) {
				return typ
			}
		}
		if raw := stringValue(typed["@value"]); raw != "" {
			typ := activityTypeValue(raw)
			if activityKnownType(typ) {
				return typ
			}
		}
	}
	return ""
}

func activityJSONLDTypes(object map[string]any) []string {
	values := append(activityTypeValues(object["type"]), activityTypeValues(object["@type"])...)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstActivityKnownType(values []string) string {
	first := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		if first == "" {
			first = value
		}
		if activityKnownType(value) {
			return value
		}
	}
	return first
}

func activityJSONLDString(object map[string]any, key string) string {
	return activityJSONLDStringValue(activityJSONLDValue(object, key))
}

func activityJSONLDStringValue(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case string:
		return typed
	case map[string]any:
		if value := stringValue(typed["@value"]); value != "" {
			return value
		}
		return activityPubObjectID(typed)
	case []any:
		for _, item := range typed {
			if value := activityJSONLDStringValue(item); value != "" {
				return value
			}
		}
	}
	return ""
}

func activityJSONLDObjectID(object map[string]any, key string) string {
	value := activityJSONLDValue(object, key)
	if id := activityPubObjectID(value); id != "" {
		return id
	}
	for _, item := range activityJSONLDListItems(value) {
		if id := activityPubObjectID(item); id != "" {
			return id
		}
	}
	return ""
}

func activityJSONLDObjectIDFirst(object map[string]any, key string) string {
	return activityPubObjectID(activityJSONLDSingle(activityJSONLDValue(object, key)))
}

func activityJSONLDObjectIDPreserveBearcap(object map[string]any, key string) string {
	value := activityJSONLDValue(object, key)
	if id := activityPubObjectIDPreserveBearcap(value); id != "" {
		return id
	}
	for _, item := range activityJSONLDListItems(value) {
		if id := activityPubObjectIDPreserveBearcap(item); id != "" {
			return id
		}
	}
	return ""
}

func activityPubObjectIDRaw(value any) string {
	switch object := value.(type) {
	case string:
		return activityURIFromBearcapRaw(object)
	case []any:
		for _, item := range object {
			if id := activityPubObjectIDRaw(item); id != "" {
				return id
			}
		}
		return ""
	case map[string]any:
		if raw, ok := object["@list"]; ok {
			return activityPubObjectIDRaw(raw)
		}
		if value, ok := object["id"]; ok {
			return activityPubObjectIDRaw(value)
		}
		if id := activityPubObjectIDRaw(object["@id"]); id != "" {
			return id
		}
		return activityPubObjectIDRaw(object["@value"])
	default:
		return ""
	}
}

func activityPubObjectIDPreserveBearcap(value any) string {
	switch object := value.(type) {
	case string:
		return strings.TrimSpace(object)
	case []any:
		for _, item := range object {
			if id := activityPubObjectIDPreserveBearcap(item); id != "" {
				return id
			}
		}
		return ""
	case map[string]any:
		if raw, ok := object["@list"]; ok {
			return activityPubObjectIDPreserveBearcap(raw)
		}
		if id := activityPubObjectIDPreserveBearcap(object["id"]); id != "" {
			return id
		}
		if id := activityPubObjectIDPreserveBearcap(object["@id"]); id != "" {
			return id
		}
		return activityPubObjectIDPreserveBearcap(object["@value"])
	default:
		return ""
	}
}

func activityJSONLDStringList(object map[string]any, key string) []string {
	return activityStringList(activityJSONLDValue(object, key))
}

func activityJSONLDStringMap(object map[string]any, key string) map[string]string {
	value := activityJSONLDValue(object, key)
	out := make(map[string]string)
	for _, item := range activityJSONLDListItems(value) {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		lang := strings.TrimSpace(stringValue(raw["@language"]))
		if lang == "" {
			continue
		}
		if text := activityJSONLDStringValue(raw); text != "" {
			out[lang] = text
		}
	}
	if len(out) > 0 {
		return out
	}
	raw, ok := activityJSONLDSingle(value).(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out = make(map[string]string, len(raw))
	for lang, item := range raw {
		if strings.TrimSpace(lang) == "" {
			continue
		}
		if text := activityJSONLDStringValue(item); text != "" {
			out[lang] = text
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func activityTypeValue(value any) string {
	return firstActivityKnownType(activityTypeValues(value))
}

func activityTypeValues(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{activityCompactType(typed)}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range activityJSONLDListItems(typed) {
			out = append(out, activityTypeValues(item)...)
		}
		return out
	case map[string]any:
		if raw, ok := typed["@list"]; ok {
			return activityTypeValues(raw)
		}
		if raw, ok := typed["@id"]; ok {
			return activityTypeValues(raw)
		}
		return activityTypeValues(typed["@value"])
	default:
		return nil
	}
}

func activityCompactType(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "https://www.w3.org/ns/activitystreams#") {
		return strings.TrimPrefix(value, "https://www.w3.org/ns/activitystreams#")
	}
	if strings.HasPrefix(value, "http://www.w3.org/ns/activitystreams#") {
		return strings.TrimPrefix(value, "http://www.w3.org/ns/activitystreams#")
	}
	if strings.HasPrefix(value, "http://joinmastodon.org/ns#") {
		return strings.TrimPrefix(value, "http://joinmastodon.org/ns#")
	}
	if strings.HasPrefix(value, "https://joinmastodon.org/ns#") {
		return strings.TrimPrefix(value, "https://joinmastodon.org/ns#")
	}
	if strings.HasPrefix(value, "http://schema.org#") {
		return strings.TrimPrefix(value, "http://schema.org#")
	}
	if strings.HasPrefix(value, "https://schema.org#") {
		return strings.TrimPrefix(value, "https://schema.org#")
	}
	if strings.HasPrefix(value, "https://schema.org/") {
		return strings.TrimPrefix(value, "https://schema.org/")
	}
	if strings.HasPrefix(value, "https://w3id.org/security#") {
		return strings.TrimPrefix(value, "https://w3id.org/security#")
	}
	if strings.HasPrefix(value, "http://w3id.org/security#") {
		return strings.TrimPrefix(value, "http://w3id.org/security#")
	}
	if strings.HasPrefix(value, "https://w3id.org/security/v1#") {
		return strings.TrimPrefix(value, "https://w3id.org/security/v1#")
	}
	if strings.HasPrefix(value, "http://w3id.org/security/v1#") {
		return strings.TrimPrefix(value, "http://w3id.org/security/v1#")
	}
	for _, prefix := range []string{"as:", "toot:", "schema:", "misskey:", "gts:", "sec:", "security:"} {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return value
}

func activityKnownType(value string) bool {
	switch value {
	case "Accept", "Add", "Announce", "Application", "Article", "Audio", "Block", "Collection", "CollectionPage", "Create", "Delete", "EncryptedMessage", "Event", "Flag", "Follow", "Group", "Hashtag", "Image", "Like", "Move", "Note", "OrderedCollection", "OrderedCollectionPage", "Organization", "Page", "Person", "Question", "Reject", "Remove", "Service", "Tombstone", "Undo", "Update", "Video", "View":
		return true
	default:
		return false
	}
}

func activityOptionalInt64(value any) sql.NullInt64 {
	switch typed := activityJSONLDSingle(value).(type) {
	case float64:
		return sql.NullInt64{Int64: int64(typed), Valid: true}
	case int64:
		return sql.NullInt64{Int64: typed, Valid: true}
	case int:
		return sql.NullInt64{Int64: int64(typed), Valid: true}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return sql.NullInt64{Int64: parsed, Valid: true}
		}
	case map[string]any:
		return activityOptionalInt64(typed["@value"])
	case string:
		if strings.TrimSpace(typed) != "" {
			return sql.NullInt64{Int64: railsInt64FromString(typed), Valid: true}
		}
	}
	return sql.NullInt64{}
}

func activityInt64Value(value any) int64 {
	parsed := activityOptionalInt64(value)
	if parsed.Valid {
		return parsed.Int64
	}
	return 0
}

func activityBoolValue(value any) bool {
	switch typed := activityJSONLDSingle(value).(type) {
	case bool:
		return typed
	case map[string]any:
		return activityBoolValue(typed["@value"])
	case string:
		return truthy(typed)
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	default:
		return false
	}
}

func activityRailsSuspendedTruthy(value any) bool {
	switch typed := activityJSONLDSingle(value).(type) {
	case nil:
		return false
	case bool:
		return typed
	case map[string]any:
		if raw, ok := typed["@value"]; ok {
			return activityRailsSuspendedTruthy(raw)
		}
		return true
	default:
		return true
	}
}

func activityDeviceID(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case map[string]any:
		return activityJSONLDString(typed, "deviceId")
	default:
		return ""
	}
}

func activityDigestValue(value any) string {
	switch typed := activityJSONLDSingle(value).(type) {
	case map[string]any:
		return activityJSONLDString(typed, "digestValue")
	default:
		return ""
	}
}

func activityStringList(value any) []string {
	items := activityJSONLDListItems(value)
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		value := activityJSONLDValueOrID(item)
		if value == "" {
			value = activityPubObjectID(item)
		}
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func activityRailsValueOrIDList(value any) []string {
	items := activityRailsArrayItems(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		value := activityRailsValueOrIDScalar(item)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func activityRailsArrayItems(value any) []any {
	if value == nil {
		return nil
	}
	if items, ok := value.([]any); ok {
		return items
	}
	return []any{value}
}

func activityRailsValueOrIDScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		return stringValue(typed["id"])
	default:
		return ""
	}
}

func activityAttachmentPresent(value any) bool {
	switch value.(type) {
	case []any, map[string]any:
		return true
	default:
		return false
	}
}

func activityProfileFields(value any) []profileField {
	if _, ok := value.([]any); !ok {
		return nil
	}
	items := activityObjectList(value)
	fields := make([]profileField, 0, len(items))
	for _, item := range items {
		if activityProfileFieldType(item) != "PropertyValue" {
			continue
		}
		fields = append(fields, profileField{
			Name:  activityJSONLDString(item, "name"),
			Value: activityJSONLDString(item, "value"),
		})
	}
	return fields
}

func activityProfileFieldType(item map[string]any) string {
	raw, ok := item["type"]
	if !ok {
		raw = item["@type"]
	}
	switch typed := raw.(type) {
	case string:
		return activityCompactType(typed)
	case map[string]any:
		return activityCompactType(activityPubObjectID(typed))
	default:
		return ""
	}
}

func firstActivityString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		items := activityJSONLDListItems(typed)
		for _, item := range items {
			object, ok := activityJSONLDSingle(item).(map[string]any)
			if !ok {
				continue
			}
			mimeType := strings.TrimSpace(firstNonEmpty(activityJSONLDString(object, "mimeType"), activityJSONLDString(object, "mediaType")))
			if mimeType == "" || strings.EqualFold(mimeType, "text/html") {
				if value := firstActivityString(object); value != "" {
					return value
				}
			}
		}
		for _, item := range items {
			if value := firstActivityString(item); value != "" {
				return value
			}
		}
		return ""
	case map[string]any:
		if _, ok := typed["@list"]; ok {
			return firstActivityString(activityJSONLDListItems(typed))
		}
		if href := activityJSONLDString(typed, "href"); href != "" {
			return href
		}
		if url := activityAttachmentURLHref(activityJSONLDValue(typed, "url")); url != "" {
			return url
		}
		if url := activityJSONLDString(typed, "url"); url != "" {
			return url
		}
		return activityPubObjectID(typed)
	default:
		return ""
	}
}

func activityPubVisibility(to []string, cc []string, actor *models.Account) int {
	if activityPubContainsPublicCollection(to) {
		return 0
	}
	if activityPubContainsPublicCollection(cc) {
		return 1
	}
	if followers := activityPubActorFollowersCollection(actor); followers != "" && stringSliceContains(to, followers) {
		return 2
	}
	return 3
}

func activityPubActorFollowersCollection(actor *models.Account) string {
	if actor == nil {
		return ""
	}
	if strings.TrimSpace(actor.FollowersURL) != "" {
		return actor.FollowersURL
	}
	return ""
}

func activityPubContainsPublicCollection(values []string) bool {
	for _, value := range values {
		if activityPubPublicCollection(value) {
			return true
		}
	}
	return false
}

func activityPubPublicCollection(value string) bool {
	return value == "https://www.w3.org/ns/activitystreams#Public" || value == "as:Public" || value == "Public"
}

var activityHTMLTagPattern = regexp.MustCompile(`<[^>]+>`)

func activityPubPlainText(content string) string {
	if content == "" {
		return ""
	}
	replacer := strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p><p>", "\n\n", "</p>", "\n", "<p>", "")
	content = replacer.Replace(content)
	content = activityHTMLTagPattern.ReplaceAllString(content, "")
	return strings.TrimSpace(html.UnescapeString(content))
}
