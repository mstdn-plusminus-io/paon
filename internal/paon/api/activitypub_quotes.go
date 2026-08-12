package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const quoteBackgroundRefreshInterval = 7 * 24 * time.Hour
const quoteMaxSynchronousDepth = 2

type quoteFingerprint struct {
	ID              int64
	QuotedStatusID  sql.NullInt64
	QuotedAccountID sql.NullInt64
	State           int
	ApprovalURI     sql.NullString
	Legacy          bool
}

type activityPubQuoteTargetAction uint8

const (
	activityPubQuoteTargetProcess activityPubQuoteTargetAction = iota
	activityPubQuoteTargetIgnore
	activityPubQuoteTargetReplace
)

func quoteEditRelationshipChanged(before quoteFingerprint, after quoteFingerprint) bool {
	return before.ID != after.ID || before.QuotedStatusID != after.QuotedStatusID || before.Legacy != after.Legacy
}

func (s *Server) recordActivityPubQuoteEdit(ctx context.Context, statusID int64, accountID int64, previousStatus *models.Status, editAlreadyRecorded bool, editedAt sql.NullTime, now time.Time) error {
	if s == nil || s.db == nil || statusID == 0 {
		return nil
	}
	currentStatus, err := loadStatusForSnapshot(s.db.WithContext(nonNilContext(ctx)), statusID)
	if err != nil {
		return err
	}
	current := statusSnapshotEdit(*currentStatus)
	current.AccountID = sql.NullInt64{Int64: accountID, Valid: accountID != 0}
	current.CreatedAt = now
	current.UpdatedAt = now
	return s.db.WithContext(nonNilContext(ctx)).Transaction(func(tx *gorm.DB) error {
		if editedAt.Valid {
			if err := tx.Model(&models.Status{}).Where("id = ?", statusID).Update("edited_at", editedAt).Error; err != nil {
				return err
			}
		}
		if editAlreadyRecorded {
			var latest models.StatusEdit
			if err := tx.Select("id").Where("status_id = ?", statusID).Order("id DESC").First(&latest).Error; err != nil {
				return err
			}
			return tx.Model(&models.StatusEdit{}).Where("id = ?", latest.ID).Update("quote_id", current.QuoteID).Error
		}
		var editCount int64
		if err := tx.Model(&models.StatusEdit{}).Where("status_id = ?", statusID).Count(&editCount).Error; err != nil {
			return err
		}
		if editCount == 0 && previousStatus != nil {
			previous := statusSnapshotEdit(*previousStatus)
			if err := tx.Omit("Status", "Account", "OrderedMediaAttachments", "Quote").Create(&previous).Error; err != nil {
				return err
			}
		}
		return tx.Omit("Status", "Account", "OrderedMediaAttachments", "Quote").Create(&current).Error
	})
}

func (s *Server) activityPubQuoteFingerprint(ctx context.Context, statusID int64) quoteFingerprint {
	var value quoteFingerprint
	if s == nil || s.db == nil || statusID == 0 {
		return value
	}
	_ = s.db.WithContext(nonNilContext(ctx)).Table("quotes").
		Select("id, quoted_status_id, quoted_account_id, state, approval_uri, legacy").
		Where("status_id = ?", statusID).
		Take(&value).Error
	return value
}

func (s *Server) reconcileActivityPubQuote(ctx context.Context, statusID int64, note activityObject, requestID string, quotingAccount *models.Account, signer *models.Account, removeWhenAbsent bool) bool {
	before := s.activityPubQuoteFingerprint(ctx, statusID)
	// Implicit updates only refresh approval for a relationship that was
	// already observed on Create or an explicit Update. Mastodon 4.4 does not
	// create a new Quote from an implicit update.
	if !removeWhenAbsent && before.ID == 0 {
		return false
	}
	quoteURI := activityPubQuoteURL(note)
	deletedQuote := note.QuoteSet && note.QuoteDeleted
	if quoteURI == "" && (!deletedQuote || !removeWhenAbsent) {
		if removeWhenAbsent && before.ID != 0 {
			_ = s.db.WithContext(nonNilContext(ctx)).Where("status_id = ?", statusID).Delete(&models.Quote{}).Error
		}
	} else {
		handledDeletedReplacement := false
		if existing, err := s.findQuoteByStatusID(ctx, statusID); err == nil && existing != nil {
			switch activityPubQuoteTargetReconcileAction(s, existing, quoteURI, removeWhenAbsent) {
			case activityPubQuoteTargetIgnore:
				return false
			case activityPubQuoteTargetReplace:
				if result := s.db.WithContext(nonNilContext(ctx)).Where("id = ?", existing.ID).Delete(&models.Quote{}); result.Error == nil && result.RowsAffected > 0 && deletedQuote {
					_ = s.persistActivityPubQuote(ctx, statusID, nil, sql.NullString{}, false, models.QuoteStateDeleted)
					handledDeletedReplacement = true
				}
			}
		}
		if !handledDeletedReplacement {
			s.processActivityPubQuoteBestEffort(ctx, statusID, note, requestID, quotingAccount, signer, removeWhenAbsent, false)
		}
	}
	after := s.activityPubQuoteFingerprint(ctx, statusID)
	return before != after
}

// Explicit updates replace a resolved quote when its target changes. Implicit
// updates are approval refreshes only and ignore a different target, matching
// ActivityPub::ProcessStatusUpdateService#update_quote_approval! in 4.4.
func activityPubQuoteTargetReconcileAction(s *Server, existing *models.Quote, quoteURI string, explicit bool) activityPubQuoteTargetAction {
	if existing == nil || existing.QuotedStatus == nil || activityPubQuoteReferencesStatus(s, quoteURI, *existing.QuotedStatus) {
		return activityPubQuoteTargetProcess
	}
	if explicit {
		return activityPubQuoteTargetReplace
	}
	return activityPubQuoteTargetIgnore
}

func activityPubQuoteReferencesStatus(s *Server, quoteURI string, status models.Status) bool {
	expected := activityPubFetchExpectedID(strings.TrimSpace(quoteURI))
	if expected == "" {
		return false
	}
	for _, candidate := range []string{status.URI.String, status.URL.String, s.quoteStatusURI(status)} {
		if strings.TrimSpace(candidate) != "" && expected == activityPubFetchExpectedID(candidate) {
			return true
		}
	}
	return false
}

const (
	quotePolicyUnknown   = 1 << 0
	quotePolicyPublic    = 1 << 1
	quotePolicyFollowers = 1 << 2
)

func activityPubQuoteApprovalPolicy(note activityObject, actor *models.Account) int {
	policy, ok := activityJSONLDSingle(note.InteractionPolicy).(map[string]any)
	if !ok {
		return 0
	}
	canQuote, ok := activityJSONLDSingle(activityJSONLDValue(policy, "canQuote")).(map[string]any)
	if !ok {
		return 0
	}
	automatic := activityPubQuoteSubpolicy(activityJSONLDValue(canQuote, "automaticApproval"), actor)
	manual := activityPubQuoteSubpolicy(activityJSONLDValue(canQuote, "manualApproval"), actor)
	return automatic<<16 | manual
}

func activityPubQuoteSubpolicy(value any, actor *models.Account) int {
	flags := 0
	actorURI := ""
	followersURI := ""
	if actor != nil {
		actorURI = strings.TrimSpace(actor.URI)
		followersURI = strings.TrimSpace(actor.FollowersURL)
	}
	for _, candidate := range activityPubObjectValueOrIDs(value) {
		switch candidate {
		case "as:Public", "Public", activityPubPublicIRI:
			flags |= quotePolicyPublic
		case actorURI:
			// The author is always implicitly allowed to quote their own post and
			// is not represented in the persisted policy bitmask.
		case followersURI:
			if followersURI != "" {
				flags |= quotePolicyFollowers
			}
		default:
			if strings.TrimSpace(candidate) != "" {
				flags |= quotePolicyUnknown
			}
		}
	}
	return flags
}

func (s *Server) persistActivityPubQuote(ctx context.Context, statusID int64, quoted *models.Status, approvalURI sql.NullString, legacy bool, state int) error {
	if s == nil || s.db == nil || statusID == 0 {
		return nil
	}
	var source models.Status
	if err := s.db.WithContext(nonNilContext(ctx)).Select("id", "account_id").Where("id = ? AND deleted_at IS NULL", statusID).First(&source).Error; err != nil {
		return err
	}
	if quoted == nil {
		now := time.Now().UTC()
		value := models.Quote{AccountID: source.AccountID, StatusID: source.ID, State: state, ApprovalURI: approvalURI, Legacy: legacy, CreatedAt: now, UpdatedAt: now}
		updates := map[string]any{
			"state": state, "approval_uri": approvalURI, "legacy": legacy, "updated_at": now,
		}
		if state == models.QuoteStateDeleted {
			updates["quoted_status_id"] = nil
			updates["quoted_account_id"] = nil
		}
		return s.db.WithContext(nonNilContext(ctx)).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "status_id"}},
			DoUpdates: clause.Assignments(updates),
		}).Create(&value).Error
	}
	return upsertSQLStatusQuoteTx(s.db.WithContext(nonNilContext(ctx)), &source, quoted, approvalURI, sql.NullString{}, legacy, state)
}

func (s *Server) findQuoteByStatusID(ctx context.Context, statusID int64) (*models.Quote, error) {
	if s == nil || s.db == nil || statusID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var quote models.Quote
	err := quoteRelationshipQuery(s.db.WithContext(nonNilContext(ctx))).Where("quotes.status_id = ?", statusID).First(&quote).Error
	if err == nil {
		s.hydrateQuoteTarget(&quote)
	}
	return &quote, err
}

func (s *Server) verifyActivityPubQuote(ctx context.Context, quoteID int64, quotedURI string, approvalURI string, requestID string, signer *models.Account) (bool, error) {
	return s.verifyActivityPubQuoteDepth(ctx, quoteID, quotedURI, approvalURI, requestID, signer, 0)
}

func (s *Server) verifyActivityPubQuoteDepth(ctx context.Context, quoteID int64, quotedURI string, approvalURI string, requestID string, signer *models.Account, depth int) (bool, error) {
	if s == nil || s.db == nil || quoteID == 0 {
		return false, nil
	}
	var quote models.Quote
	if err := quoteRelationshipQuery(s.db.WithContext(nonNilContext(ctx))).Preload("Status.Account").Where("quotes.id = ?", quoteID).First(&quote).Error; err != nil {
		return false, err
	}
	s.hydrateQuoteTarget(&quote)
	changed := false
	var quotedFetchErr error
	if quote.QuotedStatus == nil && strings.TrimSpace(quotedURI) != "" {
		quoted, err := s.resolveActivityPubQuoteTargetDepth(ctx, quotedURI, requestID, signer, depth)
		if err != nil {
			quotedFetchErr = err
		} else {
			targetChanged, err := s.attachActivityPubQuoteTarget(ctx, &quote, quoted)
			if err != nil {
				return changed, err
			}
			changed = changed || targetChanged
		}
	}
	if quote.QuotedStatus != nil && quote.AccountID == quote.QuotedAccountID.Int64 {
		stateChanged, err := s.transitionQuoteState(ctx, &quote, models.QuoteStateAccepted, quote.ApprovalURI)
		return changed || stateChanged, err
	}
	approvalURI = strings.TrimSpace(firstNonEmpty(approvalURI, quote.ApprovalURI.String))
	if approvalURI == "" {
		return changed, nil
	}
	resource, err := fetchActivityResourceWithMetadataAndUserAgentSignedWithAccept(approvalURI, paonUserAgent(s.cfg), s, s.activityFetchSigner(signer), activityDereferencerAcceptHeader)
	if err != nil {
		if status, ok := activityFetchStatus(err); !ok || !activityDereferencerHTTPStatusIgnoredLikeRails(status) {
			return changed, err
		}
		stateChanged, transitionErr := s.transitionQuoteState(ctx, &quote, quoteRejectedState(quote.State), sql.NullString{})
		return changed || stateChanged, transitionErr
	}
	var raw map[string]any
	if !activityJSONContentType(resource.contentType) {
		stateChanged, transitionErr := s.transitionQuoteState(ctx, &quote, quoteRejectedState(quote.State), sql.NullString{})
		return changed || stateChanged, transitionErr
	}
	if err := json.Unmarshal(resource.body, &raw); err != nil || raw == nil || activityJSONLDIDRaw(raw) != activityPubFetchExpectedID(approvalURI) {
		stateChanged, transitionErr := s.transitionQuoteState(ctx, &quote, quoteRejectedState(quote.State), sql.NullString{})
		return changed || stateChanged, transitionErr
	}
	attributedTo := activityJSONLDObjectID(raw, "attributedTo")
	if !activityURIHostsMatch(approvalURI, attributedTo) {
		return changed, nil
	}
	if !activityPubQuoteApprovalEnvelopeMatches(s, &quote, raw) {
		return changed, nil
	}
	if quote.QuotedStatus == nil {
		inlineChanged, err := s.importActivityPubQuoteApprovalTarget(ctx, &quote, raw, quotedURI, requestID, depth)
		if err != nil {
			return changed, err
		}
		changed = changed || inlineChanged
		if quote.QuotedStatus != nil && quote.AccountID == quote.QuotedAccountID.Int64 {
			stateChanged, err := s.transitionQuoteState(ctx, &quote, models.QuoteStateAccepted, quote.ApprovalURI)
			return changed || stateChanged, err
		}
	}
	if quote.QuotedStatus == nil && quotedFetchErr != nil {
		return changed, quotedFetchErr
	}
	if !activityPubQuoteApprovalTargetMatches(s, &quote, raw) {
		return changed, nil
	}
	stateChanged, err := s.transitionQuoteState(ctx, &quote, models.QuoteStateAccepted, sqlNullString(approvalURI))
	return changed || stateChanged, err
}

func activityPubQuoteApprovalEnvelopeMatches(s *Server, quote *models.Quote, approval map[string]any) bool {
	return s != nil && quote != nil && quote.StatusID != 0 &&
		activityResourceSupportedContext(approval["@context"]) &&
		activityValueHasCompactType(activityJSONLDValue(approval, "type"), "QuoteAuthorization") &&
		activityJSONLDObjectID(approval, "interactingObject") == s.quoteStatusURI(quote.Status)
}

func activityPubQuoteApprovalTargetMatches(s *Server, quote *models.Quote, approval map[string]any) bool {
	if s == nil || quote == nil || quote.QuotedStatus == nil || quote.QuotedStatus.ReblogOfID.Valid || quote.QuotedAccount == nil {
		return false
	}
	return activityJSONLDObjectID(approval, "interactionTarget") == s.quoteStatusURI(*quote.QuotedStatus) &&
		activityJSONLDObjectID(approval, "attributedTo") == activityPubAccountTagManagerURI(s, *quote.QuotedAccount)
}

func (s *Server) attachActivityPubQuoteTarget(ctx context.Context, quote *models.Quote, quoted *models.Status) (bool, error) {
	if s == nil || s.db == nil || quote == nil || quote.ID == 0 || quote.QuotedStatusID.Valid || !activityPubQuoteTargetValidForAccount(quoted, quote.AccountID) {
		return false, nil
	}
	updates := map[string]any{
		"quoted_status_id":  quoted.ID,
		"quoted_account_id": quoted.AccountID,
		"updated_at":        time.Now().UTC(),
	}
	result := s.db.WithContext(nonNilContext(ctx)).Model(&models.Quote{}).
		Where("id = ? AND quoted_status_id IS NULL", quote.ID).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	quote.QuotedStatusID = sql.NullInt64{Int64: quoted.ID, Valid: true}
	quote.QuotedAccountID = sql.NullInt64{Int64: quoted.AccountID, Valid: true}
	quote.QuotedStatus = quoted
	quote.QuotedAccount = &quoted.Account
	return true, nil
}

func activityPubQuoteTargetValidForAccount(quoted *models.Status, quotingAccountID int64) bool {
	if quoted == nil || quoted.ID == 0 || quoted.AccountID == 0 || quoted.DeletedAt.Valid || quoted.ReblogOfID.Valid {
		return false
	}
	return quoted.AccountID == quotingAccountID || quoted.Visibility == 0 || quoted.Visibility == 1
}

func (s *Server) importActivityPubQuoteApprovalTarget(ctx context.Context, quote *models.Quote, approval map[string]any, quotedURI string, requestID string, depth int) (bool, error) {
	if s == nil || quote == nil || quote.QuotedStatusID.Valid || strings.TrimSpace(quotedURI) == "" {
		return false, nil
	}
	inline, ok := activityJSONLDSingle(activityJSONLDValue(approval, "interactionTarget")).(map[string]any)
	if !ok {
		return false, nil
	}
	targetURI := activityJSONLDIDRaw(inline)
	if targetURI != activityPubFetchExpectedID(strings.TrimSpace(quotedURI)) || !activityURIHostsMatch(activityJSONLDIDRaw(approval), targetURI) {
		return false, nil
	}
	note := parseActivityObject(inline)
	if !activityObjectIsStatus(note) || note.ID != targetURI {
		return false, nil
	}
	if err := s.processFetchedActivityPubStatusObjectForRequestWithQuoteDepth(note, requestID, depth); err != nil {
		return false, err
	}
	quoted, err := s.statusFromActivityURIWithContext(nonNilContext(ctx), targetURI)
	if err != nil {
		return false, err
	}
	return s.attachActivityPubQuoteTarget(ctx, quote, quoted)
}

func (s *Server) resolveActivityPubQuoteTarget(ctx context.Context, quotedURI string, requestID string, signer *models.Account) (*models.Status, error) {
	return s.resolveActivityPubQuoteTargetDepth(ctx, quotedURI, requestID, signer, 0)
}

func (s *Server) resolveActivityPubQuoteTargetDepth(ctx context.Context, quotedURI string, requestID string, signer *models.Account, depth int) (*models.Status, error) {
	lookupURI := activityURIFromBearcap(strings.TrimSpace(quotedURI))
	if lookupURI == "" || !activityPubHTTPURIAllowedRaw(lookupURI) {
		return nil, errors.New("quoted status URI is not fetchable")
	}
	quoted, err := s.statusFromActivityURIWithContext(nonNilContext(ctx), lookupURI)
	if err != nil || quoted != nil {
		return quoted, err
	}
	if depth > quoteMaxSynchronousDepth {
		return nil, errors.New("remote quote fetch depth exceeded")
	}
	quoted, err = s.fetchRemoteStatusFromActivityURIForRequestWithSignerContextAndQuoteDepth(nonNilContext(ctx), quotedURI, "", requestID, signer, depth+1)
	if err != nil {
		return nil, err
	}
	return quoted, nil
}

func quoteRejectedState(current int) int {
	if current == models.QuoteStateAccepted {
		return models.QuoteStateRevoked
	}
	if current == models.QuoteStateRevoked {
		return models.QuoteStateRevoked
	}
	return models.QuoteStateRejected
}

func activityValueHasCompactType(value any, wanted string) bool {
	for _, item := range activityJSONLDListItems(value) {
		if activityCompactType(stringValue(item)) == wanted {
			return true
		}
	}
	return activityCompactType(stringValue(value)) == wanted
}

func (s *Server) transitionQuoteState(ctx context.Context, quote *models.Quote, state int, approvalURI sql.NullString) (bool, error) {
	if s == nil || s.db == nil || quote == nil || quote.ID == 0 {
		return false, nil
	}
	if quote.State == state && quote.ApprovalURI == approvalURI {
		return false, nil
	}
	updates := map[string]any{"state": state, "approval_uri": approvalURI, "updated_at": time.Now().UTC()}
	query := s.db.WithContext(nonNilContext(ctx)).Model(&models.Quote{}).Where("id = ? AND state = ?", quote.ID, quote.State)
	if quote.ApprovalURI.Valid {
		query = query.Where("approval_uri = ?", quote.ApprovalURI.String)
	} else {
		query = query.Where("approval_uri IS NULL")
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	quote.State = state
	quote.ApprovalURI = approvalURI
	return true, nil
}

func (s *Server) revokeQuoteAuthorization(ctx context.Context, approvalURI string, actor *models.Account, rawActivity []byte) (bool, error) {
	if s == nil || s.db == nil || actor == nil || actor.ID == 0 || strings.TrimSpace(approvalURI) == "" {
		return false, nil
	}
	var quote models.Quote
	if err := s.db.WithContext(nonNilContext(ctx)).Preload("Status.Account").Where("approval_uri = ? AND quoted_account_id = ? AND state IN ?", approvalURI, actor.ID, []int{models.QuoteStatePending, models.QuoteStateAccepted}).First(&quote).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	state := models.QuoteStateRejected
	if quote.State == models.QuoteStateAccepted {
		state = models.QuoteStateRevoked
	}
	forwardPlan, err := s.prepareForwardActivityPubStatusActivity(*actor, quote.Status, rawActivity)
	if err != nil {
		return true, err
	}
	changed, err := s.transitionQuoteState(ctx, &quote, state, sql.NullString{})
	if changed {
		if forwardPlan != nil {
			_ = s.deliverForwardedActivityPubStatusActivity(*forwardPlan, rawActivity)
		}
		s.publishQuoteStateUpdate(ctx, quote.StatusID)
	}
	return true, err
}

func (s *Server) publishQuoteStateUpdate(ctx context.Context, statusID int64) {
	if s == nil || statusID == 0 {
		return
	}
	if status, err := s.findStatusWithContext(nonNilContext(ctx), statusID); err == nil && status != nil {
		s.publishStatusUpdateEvent("status.update", *status)
		_ = s.enqueueOrDeliverStatusUpdateDistribution(*status)
	}
}

func (s *Server) rejectQuoteRequestActivity(payload activityPayload, actor *models.Account, quoted *models.Status) map[string]any {
	var instrument any
	if strings.TrimSpace(payload.Instrument) != "" {
		instrument = payload.Instrument
	}
	request := map[string]any{"id": payload.ID, "type": "QuoteRequest", "actor": activityPubAccountTagManagerURI(s, *actor), "object": s.quoteStatusURI(*quoted), "instrument": instrument}
	return map[string]any{
		"@context": []any{activityPubActivityStreamsContext(), map[string]any{"QuoteRequest": "https://w3id.org/fep/044f#QuoteRequest"}},
		"id":       activityPubAccountTagManagerURI(s, quoted.Account) + "#rejects/quote_requests/",
		"type":     "Reject",
		"actor":    activityPubAccountTagManagerURI(s, quoted.Account),
		"object":   request,
	}
}

func (s *Server) processActivityPubQuoteRequest(ctx context.Context, payload activityPayload, actor *models.Account) error {
	if s == nil || s.db == nil || actor == nil || actor.ID == 0 || actor.Local() || payload.ID == "" || !activityURIHostsMatch(actor.URI, payload.ID) {
		return nil
	}
	quotedURI := payload.Object.ID
	if quotedURI == "" {
		return nil
	}
	quoted, err := s.statusFromActivityURI(quotedURI)
	if err != nil || quoted == nil || quoted.ID == 0 || !quoted.Account.Local() || quoted.DeletedAt.Valid || quoted.Visibility != 0 && quoted.Visibility != 1 {
		return err
	}
	if strings.TrimSpace(actor.InboxURL) == "" {
		return nil
	}
	body, err := json.Marshal(s.rejectQuoteRequestActivity(payload, actor, quoted))
	if err != nil {
		return err
	}
	return s.deliverActivityPubConfigured(quoted.Account, actor.InboxURL, body, nil)
}

func quoteVerificationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
