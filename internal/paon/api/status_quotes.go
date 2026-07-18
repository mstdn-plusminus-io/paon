package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	cfg       config.Config
	tableName string
	endpoint  string
	client    *http.Client
}

const (
	dynamoDBStatusQuoteHTTPTimeout         = 2 * time.Second
	maxDynamoDBStatusQuoteResponseBodySize = 1 << 20
)

type dynamoDBStringAttribute struct {
	S string `json:"S,omitempty"`
}

func newStatusQuoteStore(cfg config.Config, client *http.Client) (statusQuoteStore, error) {
	if !cfg.DynamoDBEnabled {
		return nil, nil
	}
	if strings.TrimSpace(cfg.DynamoDBAccessKey) == "" || strings.TrimSpace(cfg.DynamoDBSecretKey) == "" {
		return nil, nil
	}
	if strings.TrimSpace(cfg.DynamoDBNamespace) == "" {
		return nil, errors.New("DYNAMODB_NAMESPACE is required when DYNAMODB_ENABLED=true and DynamoDB credentials are configured")
	}
	if client == nil {
		client = &http.Client{Timeout: dynamoDBStatusQuoteHTTPTimeout}
	}
	endpoint := strings.TrimRight(cfg.DynamoDBEndpoint, "/")
	if endpoint == "" {
		region := dynamoDBRegionForRequest(cfg)
		endpoint = "https://dynamodb." + region + ".amazonaws.com"
	}
	return &dynamoDBStatusQuoteStore{
		cfg:       cfg,
		tableName: dynamoDBTableName(cfg.DynamoDBNamespace, "status_quotes"),
		endpoint:  endpoint,
		client:    client,
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
	body := map[string]any{
		"TableName": d.tableName,
		"Key":       map[string]any{"status_id": dynamoDBStringAttribute{S: statusID}},
	}
	raw, err := d.do(ctx, "GetItem", body)
	if err != nil {
		return out, false, err
	}
	var decoded struct {
		Item map[string]dynamoDBStringAttribute `json:"Item"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return out, false, err
	}
	if len(decoded.Item) == 0 {
		return out, false, nil
	}
	out = statusQuote{
		StatusID:    decoded.Item["status_id"].S,
		QuoteID:     decoded.Item["quote_id"].S,
		OriginalURL: decoded.Item["original_url"].S,
		LocalURL:    decoded.Item["local_url"].S,
	}
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
	keys := make([]map[string]dynamoDBStringAttribute, 0, len(statusIDs))
	for _, statusID := range statusIDs {
		keys = append(keys, map[string]dynamoDBStringAttribute{"status_id": {S: statusID}})
	}
	body := map[string]any{
		"RequestItems": map[string]any{
			d.tableName: map[string]any{
				"Keys": keys,
			},
		},
	}
	raw, err := d.do(ctx, "BatchGetItem", body)
	if err != nil {
		return err
	}
	var decoded struct {
		Responses map[string][]map[string]dynamoDBStringAttribute `json:"Responses"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	for _, item := range decoded.Responses[d.tableName] {
		quote := statusQuote{
			StatusID:    item["status_id"].S,
			QuoteID:     item["quote_id"].S,
			OriginalURL: item["original_url"].S,
			LocalURL:    item["local_url"].S,
		}
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
	item := map[string]dynamoDBStringAttribute{
		"status_id":    {S: quote.StatusID},
		"quote_id":     {S: quote.QuoteID},
		"original_url": {S: quote.OriginalURL},
		"local_url":    {S: firstNonEmpty(quote.LocalURL, quote.OriginalURL)},
	}
	_, err := d.do(ctx, "PutItem", map[string]any{"TableName": d.tableName, "Item": item})
	return err
}

func (d *dynamoDBStatusQuoteStore) Delete(ctx context.Context, statusID string) error {
	if strings.TrimSpace(statusID) == "" {
		return nil
	}
	_, err := d.do(ctx, "DeleteItem", map[string]any{
		"TableName": d.tableName,
		"Key":       map[string]any{"status_id": dynamoDBStringAttribute{S: statusID}},
	})
	return err
}

func (d *dynamoDBStatusQuoteStore) do(ctx context.Context, operation string, body any) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, dynamoDBStatusQuoteHTTPTimeout)
	defer cancel()

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	target := "DynamoDB_20120810." + operation
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", target)
	d.sign(req, payload)

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := readDynamoDBStatusQuoteResponseBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("dynamodb %s failed: %s", operation, resp.Status)
	}
	return raw, nil
}

func readDynamoDBStatusQuoteResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("dynamodb response body is empty")
	}
	if resp.ContentLength > maxDynamoDBStatusQuoteResponseBodySize {
		return nil, fmt.Errorf("dynamodb response body is too large")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDynamoDBStatusQuoteResponseBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDynamoDBStatusQuoteResponseBodySize {
		return nil, fmt.Errorf("dynamodb response body is too large")
	}
	return body, nil
}

func (d *dynamoDBStatusQuoteStore) sign(req *http.Request, payload []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	region := dynamoDBRegionForRequest(d.cfg)
	service := "dynamodb"
	credentialScope := shortDate + "/" + region + "/" + service + "/aws4_request"
	payloadHash := sha256Hex(payload)

	req.Header.Set("X-Amz-Date", amzDate)
	if d.cfg.DynamoDBSessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", d.cfg.DynamoDBSessionToken)
	}

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	headers := []string{"content-type", "host", "x-amz-date", "x-amz-target"}
	if d.cfg.DynamoDBSessionToken != "" {
		headers = append(headers, "x-amz-security-token")
	}
	canonicalHeaders := ""
	for _, header := range headers {
		canonicalHeaders += header + ":" + strings.TrimSpace(headerValue(req, header)) + "\n"
	}
	signedHeaders := strings.Join(headers, ";")
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		req.URL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signingKey := awsSigningKey(d.cfg.DynamoDBSecretKey, shortDate, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+d.cfg.DynamoDBAccessKey+"/"+credentialScope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func dynamoDBRegionForRequest(cfg config.Config) string {
	if !cfg.DynamoDBRegionSet && strings.TrimSpace(cfg.DynamoDBRegion) == "" {
		return "ap-northeast-1"
	}
	return cfg.DynamoDBRegion
}

func headerValue(req *http.Request, name string) string {
	if name == "host" {
		return req.URL.Host
	}
	return req.Header.Get(name)
}

func awsSigningKey(secret string, date string, region string, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key []byte, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
