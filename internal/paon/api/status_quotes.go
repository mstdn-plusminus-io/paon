package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type statusQuote struct {
	StatusID    string
	QuoteID     string
	OriginalURL string
	LocalURL    string
}

type statusQuoteStore interface {
	GetMany(ctx context.Context, statusIDs []string) (map[string]statusQuote, error)
}

type dynamoDBStatusQuoteStore struct {
	tableName string
	client    *dynamodb.Client
}

const dynamoDBStatusQuoteHTTPTimeout = 2 * time.Second

func newStatusQuoteStore(cfg config.Config, client *http.Client) (statusQuoteStore, error) {
	if !cfg.DynamoDBEnabled {
		return nil, nil
	}
	if strings.TrimSpace(cfg.DynamoDBNamespace) == "" {
		return nil, errors.New("DYNAMODB_NAMESPACE is required when DYNAMODB_ENABLED=true")
	}
	if (strings.TrimSpace(cfg.DynamoDBAccessKey) == "") != (strings.TrimSpace(cfg.DynamoDBSecretKey) == "") {
		return nil, errors.New("DYNAMODB_AWS_ACCESS_KEY_ID and DYNAMODB_AWS_SECRET_ACCESS_KEY must be configured together")
	}
	if client == nil {
		client = &http.Client{Timeout: dynamoDBStatusQuoteHTTPTimeout}
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(dynamoDBRegionForRequest(cfg)),
	}
	if strings.TrimSpace(cfg.DynamoDBAccessKey) != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.DynamoDBAccessKey,
			cfg.DynamoDBSecretKey,
			cfg.DynamoDBSessionToken,
		)))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
	if err != nil {
		return nil, err
	}
	clientOptions := []func(*dynamodb.Options){func(options *dynamodb.Options) {
		options.HTTPClient = client
		options.Retryer = aws.NopRetryer{}
		if endpoint := strings.TrimSpace(cfg.DynamoDBEndpoint); endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	}}
	return &dynamoDBStatusQuoteStore{
		tableName: dynamoDBTableName(cfg.DynamoDBNamespace, "status_quotes"),
		client:    dynamodb.NewFromConfig(awsCfg, clientOptions...),
	}, nil
}

func dynamoDBTableName(namespace string, baseName string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return baseName
	}
	return namespace + "_" + baseName
}

func (s *Server) hydrateStatusQuote(status *models.Status) {
	if s == nil || s.db == nil || status == nil || status.ID == 0 {
		return
	}
	var quote models.Quote
	if err := quoteRelationshipQuery(s.db.WithContext(context.Background())).
		Where("quotes.status_id = ?", status.ID).
		First(&quote).Error; err != nil {
		return
	}
	s.hydrateQuoteTarget(&quote)
	applySQLStatusQuote(status, &quote, s)
}

func (s *Server) hydrateStatusesQuote(statuses []models.Status) {
	s.hydrateStatusesQuoteDepth(statuses, 1)
}

// hydrateStatusesQuoteDepth bounds quote expansion to the shape exposed by the
// Mastodon REST API: the top-level quote contains a status, while a quote on
// that status is serialized in its shallow form.
func (s *Server) hydrateStatusesQuoteDepth(statuses []models.Status, remainingDepth int) {
	if s == nil || s.db == nil || len(statuses) == 0 {
		return
	}
	targets := make(map[int64][]*models.Status, len(statuses)*2)
	statusIDs := make([]int64, 0, len(statuses)*2)
	for i := range statuses {
		if statuses[i].ID != 0 {
			id := statuses[i].ID
			if len(targets[id]) == 0 {
				statusIDs = append(statusIDs, id)
			}
			targets[id] = append(targets[id], &statuses[i])
		}
		if statuses[i].Reblog != nil && statuses[i].Reblog.ID != 0 {
			id := statuses[i].Reblog.ID
			if len(targets[id]) == 0 {
				statusIDs = append(statusIDs, id)
			}
			targets[id] = append(targets[id], statuses[i].Reblog)
		}
	}
	if len(statusIDs) == 0 {
		return
	}
	var quotes []models.Quote
	if err := quoteRelationshipQuery(s.db.WithContext(context.Background())).
		Where("quotes.status_id IN ?", statusIDs).
		Find(&quotes).Error; err != nil {
		return
	}
	quotedStatusIDs := make([]int64, 0, len(quotes))
	for i := range quotes {
		if quotes[i].QuotedStatusID.Valid {
			quotedStatusIDs = append(quotedStatusIDs, quotes[i].QuotedStatusID.Int64)
		}
	}
	quotedStatuses := s.loadQuoteTargetsDepth(quotedStatusIDs, remainingDepth)
	for i := range quotes {
		if quotes[i].QuotedStatusID.Valid {
			if quoted, ok := quotedStatuses[quotes[i].QuotedStatusID.Int64]; ok {
				quotedCopy := quoted
				quotes[i].QuotedStatus = &quotedCopy
			}
		}
		for _, status := range targets[quotes[i].StatusID] {
			quoteCopy := quotes[i]
			applySQLStatusQuote(status, &quoteCopy, s)
		}
	}
}

func statusQuoteTargetStructurallyAllowed(quote *models.Status) bool {
	if quote == nil || quote.ID == 0 || quote.DeletedAt.Valid || quote.ReblogOfID.Valid {
		return false
	}
	if quote.Visibility != 0 && quote.Visibility != 1 {
		return false
	}
	return quote.Account.ID != 0 && !quote.Account.SuspendedAt.Valid && !quote.Account.MovedToAccountID.Valid
}

func (s *Server) statusQuoteTargetAllowedForAccount(ctx context.Context, account *models.Account, quote *models.Status) (bool, error) {
	if s == nil || s.db == nil || account == nil || account.ID == 0 || account.SuspendedAt.Valid || account.MovedToAccountID.Valid || !statusQuoteTargetStructurallyAllowed(quote) {
		return false, nil
	}
	if quote.AccountID == account.ID {
		return true, nil
	}
	database := s.db
	if ctx != nil {
		database = database.WithContext(ctx)
	}
	var blockCount int64
	if err := database.Model(&models.Block{}).
		Where("(account_id = ? AND target_account_id = ?) OR (account_id = ? AND target_account_id = ?)", account.ID, quote.AccountID, quote.AccountID, account.ID).
		Count(&blockCount).Error; err != nil {
		return false, err
	}
	if blockCount > 0 {
		return false, nil
	}
	var muteCount int64
	now := time.Now().UTC()
	if err := database.Model(&models.Mute{}).
		Where("((account_id = ? AND target_account_id = ?) OR (account_id = ? AND target_account_id = ?)) AND (expires_at IS NULL OR expires_at > ?)", account.ID, quote.AccountID, quote.AccountID, account.ID, now).
		Count(&muteCount).Error; err != nil {
		return false, err
	}
	if muteCount > 0 {
		return false, nil
	}
	if quote.Account.Domain.Valid {
		blocked, err := s.accountDomainBlocking(account.ID, quote.Account.Domain.String)
		if err != nil || blocked {
			return false, err
		}
	}
	return true, nil
}

func (s *Server) deleteStatusQuoteBestEffort(ctx context.Context, statusID int64) {
	if s == nil || s.db == nil || statusID == 0 {
		return
	}
	database := s.db.WithContext(nonNilContext(ctx))
	var references []models.Quote
	_ = database.Select("id", "status_id").Where("quoted_status_id = ?", statusID).Find(&references).Error
	_ = database.Where("status_id = ?", statusID).Delete(&models.Quote{}).Error
	for _, quote := range references {
		result := database.Model(&models.Quote{}).
			Where("id = ? AND quoted_status_id = ?", quote.ID, statusID).
			Updates(map[string]any{
				"quoted_status_id": nil,
				"updated_at":       time.Now().UTC(),
			})
		if result.Error == nil && result.RowsAffected > 0 {
			s.publishQuoteStateUpdate(ctx, quote.StatusID)
		}
	}
}

func quoteRelationshipQuery(db *gorm.DB) *gorm.DB {
	return db.Model(&models.Quote{}).
		Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Preload("QuotedAccount.AccountStat").
		Preload("QuotedAccount.User.Role")
}

func (s *Server) hydrateQuoteTarget(quote *models.Quote) {
	if s == nil || quote == nil || !quote.QuotedStatusID.Valid {
		return
	}
	if target, ok := s.loadQuoteTargets([]int64{quote.QuotedStatusID.Int64})[quote.QuotedStatusID.Int64]; ok {
		quote.QuotedStatus = &target
	}
}

func (s *Server) loadQuoteTargets(ids []int64) map[int64]models.Status {
	return s.loadQuoteTargetsDepth(ids, 1)
}

func (s *Server) loadQuoteTargetsDepth(ids []int64, remainingDepth int) map[int64]models.Status {
	out := make(map[int64]models.Status)
	ids = uniqueInt64s(ids)
	if s == nil || s.db == nil || len(ids) == 0 {
		return out
	}
	var statuses []models.Status
	if err := s.statusQuery().Where("statuses.id IN ? AND statuses.deleted_at IS NULL", ids).Find(&statuses).Error; err != nil {
		return out
	}
	_ = s.hydrateStatusesCustomEmojis(statuses)
	if remainingDepth > 0 {
		s.hydrateStatusesQuoteDepth(statuses, remainingDepth-1)
	}
	for _, status := range statuses {
		out[status.ID] = status
	}
	return out
}

func (s *Server) hydrateQuoteVisibility(statuses []models.Status, current *models.Account) error {
	if s == nil || s.db == nil {
		return nil
	}
	quotes := make([]*models.Quote, 0, len(statuses))
	quotedStatusIDs := make([]int64, 0, len(statuses))
	visitedStatuses := make(map[*models.Status]struct{})
	var collect func(*models.Status)
	collect = func(status *models.Status) {
		if status == nil {
			return
		}
		if _, ok := visitedStatuses[status]; ok {
			return
		}
		visitedStatuses[status] = struct{}{}
		collect(status.Reblog)
		if status.Quote == nil || status.Quote.QuotedStatus == nil {
			return
		}
		quotes = append(quotes, status.Quote)
		quotedStatusIDs = append(quotedStatusIDs, status.Quote.QuotedStatus.ID)
		collect(status.Quote.QuotedStatus)
	}
	for i := range statuses {
		collect(&statuses[i])
	}
	quotedStatusIDs = uniqueInt64s(quotedStatusIDs)
	if len(quotedStatusIDs) == 0 {
		return nil
	}
	visibleIDs := make([]int64, 0, len(quotedStatusIDs))
	query := s.visibleStatusQuery(current)
	if current != nil && current.ID != 0 {
		// REST::BaseQuoteSerializer uses StatusFilter#filtered_for_quote?, which
		// applies the viewer's blocks, domain blocks, and mutes while deliberately
		// not hiding accounts merely because the server has silenced them.
		query = query.
			Where(`(
				statuses.account_id = ?
				OR NOT EXISTS (
					SELECT 1 FROM blocks quote_status_viewer_blocks
					WHERE quote_status_viewer_blocks.account_id = ?
					  AND quote_status_viewer_blocks.target_account_id = statuses.account_id
				)
			)`, current.ID, current.ID).
			Where(`(
				statuses.account_id = ?
				OR NOT EXISTS (
					SELECT 1 FROM mutes quote_status_viewer_mutes
					WHERE quote_status_viewer_mutes.account_id = ?
					  AND quote_status_viewer_mutes.target_account_id = statuses.account_id
				)
			)`, current.ID, current.ID).
			Where(`(
				statuses.account_id = ?
				OR NOT EXISTS (
					SELECT 1
					FROM account_domain_blocks quote_status_viewer_domain_blocks
					JOIN accounts quote_status_target_accounts
					  ON quote_status_target_accounts.id = statuses.account_id
					WHERE quote_status_viewer_domain_blocks.account_id = ?
					  AND quote_status_target_accounts.domain IS NOT NULL
					  AND lower(quote_status_viewer_domain_blocks.domain) = lower(quote_status_target_accounts.domain)
				)
			)`, current.ID, current.ID)
	}
	if err := query.
		Where("statuses.id IN ?", quotedStatusIDs).
		Pluck("statuses.id", &visibleIDs).Error; err != nil {
		return err
	}
	visible := make(map[int64]struct{}, len(visibleIDs))
	for _, id := range visibleIDs {
		visible[id] = struct{}{}
	}
	for _, quote := range quotes {
		quote.QuotedStatusVisibilityChecked = true
		_, quote.QuotedStatusVisible = visible[quote.QuotedStatus.ID]
	}
	return nil
}

func applySQLStatusQuote(status *models.Status, quote *models.Quote, server *Server) {
	if status == nil || quote == nil || quote.StatusID != status.ID || !statusQuoteAcceptable(quote) {
		return
	}
	status.Quote = quote
	if server != nil && quote.ID != 0 && quote.QuotedStatusID.Valid && quote.ApprovalURI.Valid && !quote.UpdatedAt.IsZero() && !quote.UpdatedAt.After(time.Now().UTC().Add(-quoteBackgroundRefreshInterval)) {
		server.enqueueQuoteRefreshTask(quote.ID)
	}
}

func statusQuoteAcceptable(quote *models.Quote) bool {
	return quote != nil && (quote.State == models.QuoteStateAccepted || !quote.Legacy)
}

func (s *Server) upsertSQLStatusQuote(ctx context.Context, statusID int64, quoted *models.Status, approvalURI sql.NullString, activityURI sql.NullString, legacy bool, state int) error {
	if s == nil || s.db == nil || statusID == 0 || quoted == nil || quoted.ID == 0 || statusID == quoted.ID {
		return nil
	}
	var source models.Status
	if err := s.db.WithContext(nonNilContext(ctx)).Select("id", "account_id").Where("id = ? AND deleted_at IS NULL", statusID).First(&source).Error; err != nil {
		return err
	}
	return upsertSQLStatusQuoteTx(s.db.WithContext(nonNilContext(ctx)), &source, quoted, approvalURI, activityURI, legacy, state)
}

func upsertSQLStatusQuoteTx(tx *gorm.DB, source *models.Status, quoted *models.Status, approvalURI sql.NullString, activityURI sql.NullString, legacy bool, state int) error {
	if tx == nil || source == nil || source.ID == 0 || source.AccountID == 0 || quoted == nil || quoted.ID == 0 || quoted.AccountID == 0 || source.ID == quoted.ID {
		return nil
	}
	now := time.Now().UTC()
	value := models.Quote{
		AccountID:       source.AccountID,
		StatusID:        source.ID,
		QuotedStatusID:  sql.NullInt64{Int64: quoted.ID, Valid: true},
		QuotedAccountID: sql.NullInt64{Int64: quoted.AccountID, Valid: true},
		State:           state,
		ApprovalURI:     approvalURI,
		ActivityURI:     activityURI,
		Legacy:          legacy,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "status_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"quoted_status_id":  value.QuotedStatusID,
			"quoted_account_id": value.QuotedAccountID,
			"state":             value.State,
			"approval_uri":      value.ApprovalURI,
			"activity_uri":      value.ActivityURI,
			"legacy":            value.Legacy,
			"updated_at":        value.UpdatedAt,
		}),
	}).Create(&value).Error
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// QuoteCutoverResult reports the one-way DynamoDB-to-PostgreSQL quote cut-over.
// The importer never mutates DynamoDB and only accepts rows whose source and target
// can still be resolved and revalidated under the current visibility/block rules.
type QuoteCutoverResult struct {
	Candidates int `json:"candidates"`
	Imported   int `json:"imported"`
	Skipped    int `json:"skipped"`
}

func (s *Server) cutoverLegacyDynamoStatusQuotes(ctx context.Context, apply bool, store statusQuoteStore) (QuoteCutoverResult, error) {
	var result QuoteCutoverResult
	if s == nil || s.db == nil || store == nil {
		return result, nil
	}
	var ids []int64
	if err := s.db.WithContext(nonNilContext(ctx)).Model(&models.Status{}).
		Where("deleted_at IS NULL AND (text LIKE ? OR text LIKE ?)", "%RE:%", "%QT:%").
		Pluck("id", &ids).Error; err != nil {
		return result, err
	}
	result.Candidates = len(ids)
	for offset := 0; offset < len(ids); offset += 100 {
		end := min(offset+100, len(ids))
		keys := make([]string, 0, end-offset)
		for _, id := range ids[offset:end] {
			keys = append(keys, strconv.FormatInt(id, 10))
		}
		rows, err := store.GetMany(nonNilContext(ctx), keys)
		if err != nil {
			return result, err
		}
		for _, sourceID := range ids[offset:end] {
			row, ok := rows[strconv.FormatInt(sourceID, 10)]
			quotedID, parseErr := strconv.ParseInt(strings.TrimSpace(row.QuoteID), 10, 64)
			if !ok || strings.TrimSpace(row.StatusID) != strconv.FormatInt(sourceID, 10) || parseErr != nil || quotedID == 0 || quotedID == sourceID {
				result.Skipped++
				continue
			}
			var source models.Status
			if err := s.statusQuery().Where("statuses.id = ? AND statuses.deleted_at IS NULL", sourceID).First(&source).Error; err != nil {
				result.Skipped++
				continue
			}
			var target models.Status
			if err := s.statusQuery().Where("statuses.id = ? AND statuses.deleted_at IS NULL", quotedID).First(&target).Error; err != nil {
				result.Skipped++
				continue
			}
			if !legacyQuoteURLMatchesStatus(s, row.OriginalURL, target) || strings.TrimSpace(row.LocalURL) != "" && !legacyQuoteURLMatchesStatus(s, row.LocalURL, target) {
				result.Skipped++
				continue
			}
			if !legacyQuoteMarkerMatchesRow(source.Text, row) {
				result.Skipped++
				continue
			}
			allowed, err := s.statusQuoteTargetAllowedForAccount(nonNilContext(ctx), &source.Account, &target)
			if err != nil {
				return result, err
			}
			if !allowed {
				result.Skipped++
				continue
			}
			if apply {
				if err := s.upsertSQLStatusQuote(ctx, sourceID, &target, sql.NullString{}, sql.NullString{}, true, models.QuoteStateAccepted); err != nil {
					return result, err
				}
			}
			result.Imported++
		}
	}
	return result, nil
}

func legacyQuoteMarkerMatchesRow(text string, row statusQuote) bool {
	if !statusTextContainsQuoteMarker(text) {
		return false
	}
	for _, candidate := range []string{row.OriginalURL, row.LocalURL} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}

func legacyQuoteURLMatchesStatus(s *Server, value string, status models.Status) bool {
	value = activityPubFetchExpectedID(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, candidate := range []string{status.URI.String, status.URL.String, s.quoteStatusURI(status), s.quoteStatusURL(status)} {
		if strings.TrimSpace(candidate) != "" && value == activityPubFetchExpectedID(candidate) {
			return true
		}
	}
	return false
}

func statusTextContainsQuoteMarker(text string) bool {
	return strings.Contains(text, "RE:") || strings.Contains(text, "QT:")
}

func (d *dynamoDBStatusQuoteStore) GetMany(ctx context.Context, statusIDs []string) (map[string]statusQuote, error) {
	out := make(map[string]statusQuote)
	statusIDs = compactUniqueStrings(statusIDs)
	for len(statusIDs) > 0 {
		chunk := statusIDs
		if len(chunk) > 100 {
			chunk = statusIDs[:100]
		}
		if err := d.getManyChunk(ctx, chunk, out); err != nil {
			return nil, err
		}
		statusIDs = statusIDs[len(chunk):]
	}
	return out, nil
}

func (d *dynamoDBStatusQuoteStore) getManyChunk(ctx context.Context, statusIDs []string, out map[string]statusQuote) error {
	if len(statusIDs) == 0 {
		return nil
	}
	keys := make([]map[string]dynamodbtypes.AttributeValue, 0, len(statusIDs))
	for _, statusID := range statusIDs {
		keys = append(keys, map[string]dynamodbtypes.AttributeValue{"status_id": dynamoDBStringAttribute(statusID)})
	}
	ctx, cancel := dynamoDBStatusQuoteContext(ctx)
	defer cancel()
	output, err := d.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
		RequestItems: map[string]dynamodbtypes.KeysAndAttributes{
			d.tableName: {Keys: keys},
		},
	})
	if err != nil {
		return err
	}
	for _, item := range output.Responses[d.tableName] {
		quote := statusQuoteFromDynamoDBItem(item)
		if quote.StatusID != "" {
			out[quote.StatusID] = quote
		}
	}
	return nil
}

func dynamoDBStatusQuoteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, dynamoDBStatusQuoteHTTPTimeout)
}

func dynamoDBStringAttribute(value string) dynamodbtypes.AttributeValue {
	return &dynamodbtypes.AttributeValueMemberS{Value: value}
}

func statusQuoteFromDynamoDBItem(item map[string]dynamodbtypes.AttributeValue) statusQuote {
	return statusQuote{
		StatusID:    dynamoDBStringAttributeValue(item["status_id"]),
		QuoteID:     dynamoDBStringAttributeValue(item["quote_id"]),
		OriginalURL: dynamoDBStringAttributeValue(item["original_url"]),
		LocalURL:    dynamoDBStringAttributeValue(item["local_url"]),
	}
}

func dynamoDBStringAttributeValue(attribute dynamodbtypes.AttributeValue) string {
	if value, ok := attribute.(*dynamodbtypes.AttributeValueMemberS); ok {
		return value.Value
	}
	return ""
}

func dynamoDBRegionForRequest(cfg config.Config) string {
	return firstNonEmptyString(cfg.DynamoDBRegion, "ap-northeast-1")
}

func sqlNullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

func compactUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
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
