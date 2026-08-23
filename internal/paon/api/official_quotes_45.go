package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

var errQuotedUserNotMentioned = errors.New("quoted user must be explicitly mentioned in a direct status")

const quotePolicyFollowing = 1 << 3

type quotePolicyDecision string

const (
	quotePolicyAutomatic quotePolicyDecision = "automatic"
	quotePolicyManual    quotePolicyDecision = "manual"
	quotePolicyUndecided quotePolicyDecision = "unknown"
	quotePolicyDenied    quotePolicyDecision = "denied"
)

func quoteApprovalPolicyFromName(name string) (int, bool) {
	switch strings.TrimSpace(name) {
	case "public":
		return quotePolicyPublic << 16, true
	case "followers":
		return quotePolicyFollowers << 16, true
	case "nobody":
		return 0, true
	default:
		return 0, false
	}
}

func quoteApprovalPolicyName(policy int) string {
	switch policy >> 16 {
	case quotePolicyPublic:
		return "public"
	case quotePolicyFollowers:
		return "followers"
	default:
		return "nobody"
	}
}

func quoteApprovalPolicyForPayload(payload statusUpdatePayload, account models.Account) (int, bool) {
	name := payload.QuoteApprovalPolicy
	if !payload.HasQuoteApprovalPolicy || strings.TrimSpace(name) == "" {
		name = stringSettingValue(userSettingsForAccount(account), "default_quote_policy")
		if name == "" {
			name = "public"
		}
	}
	return quoteApprovalPolicyFromName(name)
}

func quotePolicyKeys(value int, automatic bool) []string {
	if automatic {
		value >>= 16
	} else {
		value &= 0xffff
	}
	out := make([]string, 0, 4)
	for _, item := range []struct {
		name string
		flag int
	}{
		{"unsupported_policy", quotePolicyUnknown},
		{"public", quotePolicyPublic},
		{"followers", quotePolicyFollowers},
		{"following", quotePolicyFollowing},
	} {
		if value&item.flag != 0 {
			out = append(out, item.name)
		}
	}
	return out
}

func quotePolicyDecisionWithRelations(status models.Status, viewer *models.Account, viewerFollowsAuthor bool, authorFollowsViewer bool) quotePolicyDecision {
	if viewer == nil || viewer.ID == 0 || status.Visibility == 3 || status.ReblogOfID.Valid {
		return quotePolicyDenied
	}
	if status.AccountID == viewer.ID {
		return quotePolicyAutomatic
	}
	automatic := status.QuoteApprovalPolicy >> 16
	manual := status.QuoteApprovalPolicy & 0xffff
	if automatic&quotePolicyPublic != 0 || automatic&quotePolicyFollowers != 0 && viewerFollowsAuthor || automatic&quotePolicyFollowing != 0 && authorFollowsViewer {
		return quotePolicyAutomatic
	}
	if manual&quotePolicyPublic != 0 || manual&quotePolicyFollowers != 0 && viewerFollowsAuthor || manual&quotePolicyFollowing != 0 && authorFollowsViewer {
		return quotePolicyManual
	}
	if (automatic|manual)&quotePolicyUnknown != 0 {
		return quotePolicyUndecided
	}
	return quotePolicyDenied
}

func (s *Server) quotePolicyForAccount(ctx context.Context, status models.Status, viewer *models.Account) (quotePolicyDecision, error) {
	if viewer == nil || viewer.ID == 0 || status.Visibility == 3 || status.ReblogOfID.Valid {
		return quotePolicyDenied, nil
	}
	if status.AccountID == viewer.ID {
		return quotePolicyAutomatic, nil
	}
	db := s.db.WithContext(nonNilContext(ctx))
	var viewerFollowsAuthor int64
	if err := db.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", viewer.ID, status.AccountID).Count(&viewerFollowsAuthor).Error; err != nil {
		return quotePolicyDenied, err
	}
	var authorFollowsViewer int64
	if err := db.Model(&models.Follow{}).Where("account_id = ? AND target_account_id = ?", status.AccountID, viewer.ID).Count(&authorFollowsViewer).Error; err != nil {
		return quotePolicyDenied, err
	}
	return quotePolicyDecisionWithRelations(status, viewer, viewerFollowsAuthor > 0, authorFollowsViewer > 0), nil
}

// quoteDeniedByAccountRelationship mirrors the relationship checks in
// Mastodon's StatusPolicy#quote?: the quoted-status author must not block the
// prospective quoter (including by domain), and the quoter must not directly
// block the author. A viewer-owned domain block is a filtering preference, not
// an authorization denial. The policy bits alone are deliberately insufficient.
func (s *Server) quoteDeniedByAccountRelationship(quotedAuthor *models.Account, quoter *models.Account) (bool, error) {
	if s == nil || s.db == nil || quotedAuthor == nil || quotedAuthor.ID == 0 || quoter == nil || quoter.ID == 0 {
		return true, nil
	}
	blocked, err := s.accountBlocksAccountOrDomain(quotedAuthor.ID, quoter)
	if err != nil || blocked {
		return blocked, err
	}
	var count int64
	if err := s.db.Model(&models.Block{}).
		Where("account_id = ? AND target_account_id = ?", quoter.ID, quotedAuthor.ID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Server) findQuotableStatusForAccount(ctx context.Context, viewer *models.Account, id string) (*models.Status, quotePolicyDecision, error) {
	if viewer == nil || viewer.ID == 0 {
		return nil, quotePolicyDenied, gorm.ErrRecordNotFound
	}
	// Mastodon's controller resolves Status#proper before applying StatusPolicy,
	// so quoting a boost quotes its original status rather than the boost row.
	candidate, err := s.findStatus(id)
	if err != nil || candidate == nil || candidate.DeletedAt.Valid {
		return nil, quotePolicyDenied, gorm.ErrRecordNotFound
	}
	if candidate.ReblogOfID.Valid {
		id = strconv.FormatInt(candidate.ReblogOfID.Int64, 10)
	}
	status, err := s.findVisibleStatusForAccount(viewer, id)
	if err != nil || status == nil || status.DeletedAt.Valid || status.ReblogOfID.Valid {
		return nil, quotePolicyDenied, gorm.ErrRecordNotFound
	}
	if status.AccountID != viewer.ID && status.Visibility != 0 && status.Visibility != 1 {
		return nil, quotePolicyDenied, gorm.ErrRecordNotFound
	}
	if status.AccountID != viewer.ID {
		blocked, err := s.quoteDeniedByAccountRelationship(&status.Account, viewer)
		if err != nil {
			return nil, quotePolicyDenied, err
		}
		if blocked {
			return nil, quotePolicyDenied, gorm.ErrRecordNotFound
		}
	}
	decision, err := s.quotePolicyForAccount(ctx, *status, viewer)
	if err != nil || decision == quotePolicyDenied {
		if err == nil {
			err = gorm.ErrRecordNotFound
		}
		return nil, decision, err
	}
	return status, decision, nil
}

func (s *Server) createOfficialQuoteTx(tx *gorm.DB, status *models.Status, quoted *models.Status, decision quotePolicyDecision, now time.Time) (*models.Quote, error) {
	if tx == nil || status == nil || quoted == nil {
		return nil, errors.New("quote relationship is incomplete")
	}
	state := models.QuoteStatePending
	approvalURI := sql.NullString{}
	activityURI := sql.NullString{}
	if quoted.Account.Local() && decision != quotePolicyDenied {
		state = models.QuoteStateAccepted
	} else if !quoted.Account.Local() {
		activityURI = sql.NullString{String: activityPubAccountTagManagerURI(s, status.Account) + "/quote_requests/" + uuid.NewString(), Valid: true}
	}
	quote := &models.Quote{
		AccountID: status.AccountID, StatusID: status.ID,
		QuotedStatusID:  sql.NullInt64{Int64: quoted.ID, Valid: true},
		QuotedAccountID: sql.NullInt64{Int64: quoted.AccountID, Valid: true},
		State:           state, ApprovalURI: approvalURI, ActivityURI: activityURI,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(quote).Error; err != nil {
		return nil, err
	}
	if state == models.QuoteStateAccepted {
		if err := incrementStatusStatCounter(tx, quoted.ID, statusStatCounterQuotes, 1); err != nil {
			return nil, err
		}
	}
	if quoted.AccountID != status.AccountID {
		var existing int64
		if err := tx.Model(&models.Mention{}).Where("status_id = ? AND account_id = ?", status.ID, quoted.AccountID).Count(&existing).Error; err != nil {
			return nil, err
		}
		if existing == 0 {
			mention := models.Mention{StatusID: models.MentionStatusID(status.ID), AccountID: models.MentionAccountID(quoted.AccountID), Silent: true, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&mention).Error; err != nil {
				return nil, err
			}
		}
	}
	return quote, nil
}

func officialQuoteNotificationPayload(quote *models.Quote, quoted *models.Status) (asynqLocalNotificationPayload, bool) {
	if quote == nil || quoted == nil || quote.ID == 0 || quote.State != models.QuoteStateAccepted || !quoted.Account.Local() || quoted.AccountID == 0 {
		return asynqLocalNotificationPayload{}, false
	}
	return asynqLocalNotificationPayload{
		ReceiverAccountID: quoted.AccountID,
		FromAccountID:     quote.AccountID,
		ActivityID:        quote.ID,
		ActivityType:      "Quote",
		Type:              "quote",
	}, true
}

func (s *Server) localQuoteAuthorizationURI(account models.Account, quoteID int64) string {
	return activityPubAccountTagManagerURI(s, account) + "/quote_authorizations/" + strconv.FormatInt(quoteID, 10)
}

func activityPubQuoteInteractionPolicy(s *Server, status models.Status) map[string]any {
	automatic := status.QuoteApprovalPolicy >> 16
	approved := make([]string, 0, 3)
	if automatic&quotePolicyPublic != 0 {
		approved = append(approved, activityPubPublicIRI)
	}
	if automatic&quotePolicyFollowers != 0 {
		followers := strings.TrimSpace(status.Account.FollowersURL)
		if followers == "" {
			followers = activityPubAccountTagManagerURI(s, status.Account) + "/followers"
		}
		approved = append(approved, followers)
	}
	if automatic&quotePolicyFollowing != 0 {
		following := strings.TrimSpace(status.Account.FollowingURL)
		if following == "" {
			following = activityPubAccountTagManagerURI(s, status.Account) + "/following"
		}
		approved = append(approved, following)
	}
	if len(approved) == 0 {
		approved = append(approved, activityPubAccountTagManagerURI(s, status.Account))
	}
	return map[string]any{"canQuote": map[string]any{"automaticApproval": approved}}
}

func activityPubAddQuoteFields(s *Server, status models.Status, note map[string]any) {
	if s == nil || note == nil || status.Quote == nil || !statusQuoteAcceptable(status.Quote) {
		return
	}
	quote := status.Quote
	if quote.QuotedStatus != nil && quote.QuotedStatus.ID != 0 && !quote.QuotedStatus.DeletedAt.Valid {
		target := s.quoteStatusURI(*quote.QuotedStatus)
		note["quote"] = target
		note["_misskey_quote"] = target
		note["quoteUri"] = target
	} else {
		note["quote"] = map[string]any{"type": "Tombstone"}
	}
	if approvalURI := activityPubQuoteApprovalURI(s, quote); approvalURI != "" {
		note["quoteAuthorization"] = approvalURI
	}
}

func activityPubQuoteApprovalURI(s *Server, quote *models.Quote) string {
	if s == nil || quote == nil {
		return ""
	}
	var quotedAccount *models.Account
	if quote.QuotedAccount != nil && quote.QuotedAccount.ID != 0 {
		quotedAccount = quote.QuotedAccount
	} else if quote.QuotedStatus != nil && quote.QuotedStatus.Account.ID != 0 {
		quotedAccount = &quote.QuotedStatus.Account
	}
	if quotedAccount != nil && quotedAccount.Local() {
		if quote.State != models.QuoteStateAccepted || quote.ID == 0 {
			return ""
		}
		return s.localQuoteAuthorizationURI(*quotedAccount, quote.ID)
	}
	if !quote.ApprovalURI.Valid {
		return ""
	}
	return strings.TrimSpace(quote.ApprovalURI.String)
}

func (s *Server) quoteAuthorizationObject(quote models.Quote, forceID bool) map[string]any {
	id := strings.TrimSpace(quote.ApprovalURI.String)
	if forceID || id == "" {
		if quote.QuotedAccount != nil {
			id = s.localQuoteAuthorizationURI(*quote.QuotedAccount, quote.ID)
		}
	}
	object := map[string]any{
		"id": id, "type": "QuoteAuthorization",
	}
	if quote.QuotedAccount != nil {
		object["attributedTo"] = activityPubAccountTagManagerURI(s, *quote.QuotedAccount)
	}
	if quote.Status.ID != 0 {
		object["interactingObject"] = s.quoteStatusURI(quote.Status)
	}
	if quote.QuotedStatus != nil {
		object["interactionTarget"] = s.quoteStatusURI(*quote.QuotedStatus)
	}
	return object
}

func (s *Server) activityPubQuoteAuthorization(c *echo.Context) error {
	s.activityPubAccountVary(c)
	var account *models.Account
	var err error
	if accountID := strings.TrimSpace(c.Param("account_id")); accountID != "" {
		account, err = s.findAccountByID(accountID)
		if err == nil && (account == nil || !account.Local()) {
			err = gorm.ErrRecordNotFound
		}
	} else {
		account, err = s.localActivityPubAccount(c)
	}
	if err != nil || account == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.activityPubAccountOwnedGuard(c, account, false); err != nil {
		return err
	}
	id, err := strconv.ParseInt(activityPubFormatParam(c, "id"), 10, 64)
	if err != nil || id == 0 {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var quote models.Quote
	err = quoteRelationshipQuery(s.db).
		Preload("Status.Account").Preload("QuotedStatus.Account").
		Where("quotes.id = ? AND quotes.quoted_account_id = ? AND quotes.state = ?", id, account.ID, models.QuoteStateAccepted).
		First(&quote).Error
	if err != nil || quote.QuotedStatus == nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	signed, err := s.activityPubSignatureAccountForPublicFetch(c)
	if err != nil {
		return err
	}
	visible, err := s.activityPubStatusVisible(*quote.QuotedStatus, *account, signed)
	if err != nil {
		return err
	}
	if !visible {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	object := s.quoteAuthorizationObject(quote, true)
	object["@context"] = activityContext()
	return activityJSONWithCachePrivacy(c, object, 30, !s.authorizedFetchMode() && (quote.QuotedStatus.Visibility == 0 || quote.QuotedStatus.Visibility == 1))
}

func (s *Server) deliverLocalQuoteRequest(ctx context.Context, status models.Status) error {
	if s == nil || s.db == nil || !status.Account.Local() {
		return nil
	}
	var quote models.Quote
	err := quoteRelationshipQuery(s.db.WithContext(nonNilContext(ctx))).
		Preload("Status.Account").Preload("QuotedStatus.Account").
		Where("quotes.status_id = ? AND quotes.state = ?", status.ID, models.QuoteStatePending).First(&quote).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil || quote.QuotedStatus == nil || quote.QuotedAccount == nil || quote.QuotedAccount.Local() || !quote.ActivityURI.Valid || strings.TrimSpace(quote.QuotedAccount.InboxURL) == "" {
		return err
	}
	note, err := activityPubNoteWithError(s, quote.Status)
	if err != nil {
		return err
	}
	request := map[string]any{
		"@context": activityContext(),
		"id":       quote.ActivityURI.String, "type": "QuoteRequest",
		"actor":      activityPubAccountTagManagerURI(s, quote.Status.Account),
		"object":     s.quoteStatusURI(*quote.QuotedStatus),
		"instrument": note,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return s.deliverActivityPubConfigured(quote.Status.Account, quote.QuotedAccount.InboxURL, body, nil)
}

func (s *Server) deliverDeleteQuoteAuthorization(ctx context.Context, quote models.Quote, quoted models.Status) error {
	if s == nil || s.db == nil || !quoted.Account.Local() {
		return nil
	}
	quote.QuotedStatus = &quoted
	quote.QuotedAccount = &quoted.Account
	object := s.quoteAuthorizationObject(quote, true)
	activity := map[string]any{
		"@context": activityContext(),
		"id":       object["id"].(string) + "#delete", "type": "Delete",
		"actor": activityPubAccountTagManagerURI(s, quoted.Account),
		"to":    []string{activityPubPublicIRI}, "object": object,
	}
	body, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	inboxes, err := s.activityPubStatusRecipientInboxesConfigured(quote.Status, true)
	if err != nil {
		return err
	}
	targetInboxes, err := s.activityPubStatusRecipientInboxesConfigured(quoted, true)
	if err != nil {
		return err
	}
	var lastErr error
	for _, inbox := range compactUniqueStrings(append(inboxes, targetInboxes...)) {
		if err := s.deliverActivityPubConfigured(quoted.Account, inbox, body, nil); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func directQuoteMentionsQuotedAccount(tx *gorm.DB, statusID int64, quotedAccountID int64) (bool, error) {
	var count int64
	err := tx.Model(&models.Mention{}).Where("status_id = ? AND account_id = ? AND silent = FALSE", statusID, quotedAccountID).Count(&count).Error
	return count > 0, err
}

func (s *Server) statusTextExplicitlyMentionsAccount(text string, account *models.Account) bool {
	if s == nil || account == nil || account.ID == 0 {
		return false
	}
	for _, ref := range statusMentionRefs(text) {
		if account.Local() {
			if strings.EqualFold(account.Username, ref.Username) && s.localMentionRef(ref) {
				return true
			}
			continue
		}
		if accountMatchesMentionRef(account, ref) {
			return true
		}
	}
	return false
}

func (s *Server) hydrateQuotePolicyForViewer(statuses []models.Status, viewer *models.Account) error {
	if s == nil || s.db == nil || len(statuses) == 0 {
		return nil
	}
	var walk func(*models.Status) error
	walk = func(status *models.Status) error {
		if status == nil || status.ID == 0 {
			return nil
		}
		decision, err := s.quotePolicyForAccount(context.Background(), *status, viewer)
		if err != nil {
			return err
		}
		status.QuotePolicyCurrentUser = string(decision)
		if err := walk(status.Reblog); err != nil {
			return err
		}
		if status.Quote != nil {
			return walk(status.Quote.QuotedStatus)
		}
		return nil
	}
	for i := range statuses {
		if err := walk(&statuses[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) updateStatusInteractionPolicy(c *echo.Context) error {
	if err := s.requireAccessTokenScope(c, "write", "write:statuses"); err != nil {
		return err
	}
	account, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}
	if err := requireAvailableQuoteAPIAccount(c, account); err != nil {
		return err
	}
	status, err := s.findVisibleStatusForAccount(account, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if account == nil || status.AccountID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is outside the authorized account")
	}
	payload, err := parseStatusUpdatePayload(c)
	if err != nil {
		return apiError(c, http.StatusBadRequest, "Malformed request")
	}
	policy, ok := quoteApprovalPolicyForPayload(payload, *account)
	if !ok {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Quote approval policy is invalid")
	}
	if status.Visibility > 1 || status.ReblogOfID.Valid {
		policy = 0
	}
	changed := status.QuoteApprovalPolicy != policy
	if changed {
		if err := s.db.Model(&models.Status{}).Where("id = ? AND account_id = ?", status.ID, account.ID).Updates(map[string]any{"quote_approval_policy": policy, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		status.QuoteApprovalPolicy = policy
	}
	if err := s.hydrateStatusRelationship(status, account); err != nil {
		return err
	}
	if changed {
		s.publishStatusUpdateEvent("status.update", *status)
		_ = s.enqueueOrDeliverStatusUpdateDistribution(*status)
	}
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))
}

func (s *Server) statusQuotes(c *echo.Context) error {
	// Api::V1::Statuses::QuotesController authorizes the OAuth token but does
	// not require a resource owner. A client-credentials token may therefore
	// list quotes of a public status, with the same anonymous visibility and
	// filtering rules as Rails. A missing token remains a 401 and a token with
	// the wrong scope remains a 403.
	if err := s.requireAccessTokenScope(c, "read", "read:statuses"); err != nil {
		return err
	}
	account, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}
	if err := requireAvailableQuoteAPIAccount(c, account); err != nil {
		return err
	}
	status, err := s.findVisibleStatusForAccount(account, c.Param("status_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	limitValue := limitParam(c, 20, 40)
	query := s.db.Model(&models.Quote{}).Where("quoted_status_id = ? AND state = ?", status.ID, models.QuoteStateAccepted)
	query = applyIDPagination(c, query, "quotes.id").Order("quotes.id DESC").Limit(limitValue)
	var quotes []models.Quote
	if err := query.Find(&quotes).Error; err != nil {
		return err
	}
	ids := make([]int64, 0, len(quotes))
	for _, quote := range quotes {
		ids = append(ids, quote.StatusID)
	}
	rows := make([]models.Status, 0, len(ids))
	if len(ids) > 0 {
		if err := applyStatusContextFilterQuery(s.visibleStatusQuery(account).Where("statuses.id IN ?", ids), account).Find(&rows).Error; err != nil {
			return err
		}
		sortStatusesByIDs(rows, ids)
		if err := s.hydrateStatusRelationships(rows, account); err != nil {
			return err
		}
		c.Response().Header().Set("Link", paginationLinkWithAllowedParams(c, quotes[0].ID, quotes[len(quotes)-1].ID, "since_id", len(quotes) == limitValue, true, []string{"limit"}))
	}
	return c.JSON(http.StatusOK, serializeStatusesWithFilterContext(s.cfg, rows, account, s.accountFilters(account), "public"))
}

func (s *Server) revokeStatusQuote(c *echo.Context) error {
	if err := s.requireAccessTokenScope(c, "write", "write:statuses"); err != nil {
		return err
	}
	account, err := s.currentAccountForOptionalRequestToken(c)
	if err != nil {
		return err
	}
	if err := requireAvailableQuoteAPIAccount(c, account); err != nil {
		return err
	}
	target, err := s.findVisibleStatusForAccount(account, c.Param("status_id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if account == nil || target.AccountID != account.ID {
		return apiError(c, http.StatusForbidden, "This action is outside the authorized account")
	}
	var quote models.Quote
	err = quoteRelationshipQuery(s.db).Preload("Status.Account").Where("quotes.quoted_status_id = ? AND quotes.status_id = ?", target.ID, c.Param("id")).First(&quote).Error
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	changed, err := s.transitionQuoteState(c.Request().Context(), &quote, quoteRejectedState(quote.State), sql.NullString{})
	if err != nil {
		return err
	}
	if changed {
		s.publishQuoteStateUpdate(c.Request().Context(), quote.StatusID)
		_ = s.deliverDeleteQuoteAuthorization(c.Request().Context(), quote, *target)
	}
	status, err := s.findStatus(strconv.FormatInt(quote.StatusID, 10))
	if err != nil {
		return err
	}
	if err := s.hydrateStatusRelationship(status, account); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))
}

// Api::BaseController runs require_not_suspended! for these endpoints even
// though their controllers intentionally do not run require_user!. Application
// tokens have no account and remain valid; a suspended resource owner receives
// Mastodon's standard 403 before status visibility or ownership is evaluated.
func requireAvailableQuoteAPIAccount(c *echo.Context, account *models.Account) error {
	if account != nil && account.SuspendedAt.Valid {
		return apiError(c, http.StatusForbidden, "Your login is currently disabled")
	}
	return nil
}
