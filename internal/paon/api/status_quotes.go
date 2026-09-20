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
)

type statusQuote struct {
	StatusID    string
	QuoteID     string
	OriginalURL string
	LocalURL    string
}

type statusQuoteStore interface {
	Get(ctx context.Context, statusID string) (statusQuote, bool, error)
	GetMany(ctx context.Context, statusIDs []string) (map[string]statusQuote, error)
	Put(ctx context.Context, quote statusQuote) error
	Delete(ctx context.Context, statusID string) error
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
	if s == nil || s.quoteStore == nil || status == nil || status.ID == 0 || !statusTextContainsQuoteMarker(status.Text) {
		return
	}
	quote, ok, err := s.quoteStore.Get(context.Background(), strconv.FormatInt(status.ID, 10))
	if err != nil || !ok || quote.QuoteID == "" || quote.QuoteID == quote.StatusID {
		return
	}
	applyStatusQuoteMetadata(status, quote)
}

func (s *Server) hydrateStatusesQuote(statuses []models.Status) {
	if s == nil || s.quoteStore == nil || len(statuses) == 0 {
		return
	}
	type target struct {
		status   *models.Status
		statusID string
	}
	targets := make([]target, 0, len(statuses)*2)
	seen := make(map[string]struct{}, len(statuses)*2)
	statusIDs := make([]string, 0, len(statuses)*2)
	for i := range statuses {
		if statuses[i].ID != 0 && statusTextContainsQuoteMarker(statuses[i].Text) {
			id := strconv.FormatInt(statuses[i].ID, 10)
			targets = append(targets, target{status: &statuses[i], statusID: id})
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				statusIDs = append(statusIDs, id)
			}
		}
		if statuses[i].Reblog != nil && statuses[i].Reblog.ID != 0 && statusTextContainsQuoteMarker(statuses[i].Reblog.Text) {
			id := strconv.FormatInt(statuses[i].Reblog.ID, 10)
			targets = append(targets, target{status: statuses[i].Reblog, statusID: id})
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				statusIDs = append(statusIDs, id)
			}
		}
	}
	if len(statusIDs) == 0 {
		return
	}
	quotes, err := s.quoteStore.GetMany(context.Background(), statusIDs)
	if err != nil {
		return
	}
	for _, target := range targets {
		quote, ok := quotes[target.statusID]
		if !ok || quote.QuoteID == "" || quote.QuoteID == quote.StatusID {
			continue
		}
		applyStatusQuoteMetadata(target.status, quote)
	}
}

func (s *Server) putStatusQuoteBestEffort(ctx context.Context, statusID int64, quote *models.Status) {
	if s == nil || s.quoteStore == nil || statusID == 0 || quote == nil || quote.ID == 0 || quote.ID == statusID {
		return
	}
	s.putStatusQuoteMetadataBestEffort(ctx, statusID, quote.ID, s.quoteStatusURI(*quote), s.quoteStatusURI(*quote))
}

func (s *Server) putStatusQuoteMetadataBestEffort(ctx context.Context, statusID int64, quoteID int64, originalURL string, localURL string) {
	if s == nil || s.quoteStore == nil || statusID == 0 || quoteID == 0 || statusID == quoteID {
		return
	}
	_ = s.quoteStore.Put(ctx, statusQuote{
		StatusID:    strconv.FormatInt(statusID, 10),
		QuoteID:     strconv.FormatInt(quoteID, 10),
		OriginalURL: originalURL,
		LocalURL:    firstNonEmpty(localURL, originalURL),
	})
}

func (s *Server) applyStatusQuote(status *models.Status, quote *models.Status) {
	if s == nil || status == nil || quote == nil || quote.ID == 0 || quote.ID == status.ID {
		return
	}
	status.QuoteID = sqlNullString(strconv.FormatInt(quote.ID, 10))
	status.QuoteOriginalURL = sqlNullString(s.quoteStatusURI(*quote))
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
	if s == nil || s.quoteStore == nil || statusID == 0 {
		return
	}
	_ = s.quoteStore.Delete(ctx, strconv.FormatInt(statusID, 10))
}

func applyStatusQuoteMetadata(status *models.Status, quote statusQuote) {
	if status == nil {
		return
	}
	status.QuoteID = sqlNullString(quote.QuoteID)
	status.QuoteOriginalURL = sqlNullString(quote.OriginalURL)
}

func statusTextContainsQuoteMarker(text string) bool {
	return strings.Contains(text, "RE:") || strings.Contains(text, "QT:")
}

func (d *dynamoDBStatusQuoteStore) Get(ctx context.Context, statusID string) (statusQuote, bool, error) {
	var out statusQuote
	if strings.TrimSpace(statusID) == "" {
		return out, false, nil
	}
	ctx, cancel := dynamoDBStatusQuoteContext(ctx)
	defer cancel()
	output, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.tableName),
		Key:       map[string]dynamodbtypes.AttributeValue{"status_id": dynamoDBStringAttribute(statusID)},
	})
	if err != nil {
		return out, false, err
	}
	if len(output.Item) == 0 {
		return out, false, nil
	}
	out = statusQuoteFromDynamoDBItem(output.Item)
	if out.StatusID == "" {
		out.StatusID = statusID
	}
	return out, true, nil
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

func (d *dynamoDBStatusQuoteStore) Put(ctx context.Context, quote statusQuote) error {
	if strings.TrimSpace(quote.StatusID) == "" || strings.TrimSpace(quote.QuoteID) == "" {
		return nil
	}
	item := map[string]dynamodbtypes.AttributeValue{
		"status_id":    dynamoDBStringAttribute(quote.StatusID),
		"quote_id":     dynamoDBStringAttribute(quote.QuoteID),
		"original_url": dynamoDBStringAttribute(quote.OriginalURL),
		"local_url":    dynamoDBStringAttribute(firstNonEmpty(quote.LocalURL, quote.OriginalURL)),
	}
	ctx, cancel := dynamoDBStatusQuoteContext(ctx)
	defer cancel()
	_, err := d.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(d.tableName), Item: item})
	return err
}

func (d *dynamoDBStatusQuoteStore) Delete(ctx context.Context, statusID string) error {
	if strings.TrimSpace(statusID) == "" {
		return nil
	}
	ctx, cancel := dynamoDBStatusQuoteContext(ctx)
	defer cancel()
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.tableName),
		Key:       map[string]dynamodbtypes.AttributeValue{"status_id": dynamoDBStringAttribute(statusID)},
	})
	return err
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
