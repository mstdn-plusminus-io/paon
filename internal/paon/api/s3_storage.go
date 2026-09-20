package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsretry "github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	tmtypes "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	awshttp "github.com/aws/smithy-go/transport/http"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	s3HTTPOpenTimeout              = 5 * time.Second
	s3HTTPReadTimeout              = 5 * time.Second
	s3HTTPExpectContinueTimeout    = 10 * time.Second
	paperclipImmutableCacheControl = "public, max-age=315576000, immutable"
	azureBlobCopyPollInterval      = 100 * time.Millisecond
	azureBlobCopyTimeout           = 5 * time.Minute
)

var s3HTTPClient = &http.Client{Transport: s3HTTPTransport()}
var storageDeleteRetryWait = waitForStorageDeleteRetry

type s3SDKStorage struct {
	client    *s3.Client
	presigner *s3.PresignClient
	uploader  *transfermanager.Client
}

func s3HTTPTransport() *http.Transport {
	return s3HTTPTransportForConfig(config.Config{
		S3OpenTimeout: 5,
		S3ReadTimeout: 5,
	})
}

func configureS3HTTPClient(cfg config.Config) {
	if !cfg.S3Enabled && cfg.S3OpenTimeout == 0 && cfg.S3ReadTimeout == 0 {
		cfg.S3OpenTimeout = 5
		cfg.S3ReadTimeout = 5
	}
	s3HTTPClient = &http.Client{Transport: s3HTTPTransportForConfig(cfg)}
}

func s3HTTPTransportForConfig(cfg config.Config) *http.Transport {
	openTimeout := railsS3TimeoutDuration(cfg.S3OpenTimeout)
	readTimeout := railsS3TimeoutDuration(cfg.S3ReadTimeout)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: openTimeout}).DialContext
	transport.TLSHandshakeTimeout = openTimeout
	transport.ResponseHeaderTimeout = readTimeout
	transport.ExpectContinueTimeout = s3HTTPExpectContinueTimeout
	return transport
}

func railsS3TimeoutDuration(seconds int) time.Duration {
	if seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func newS3SDKStorage(ctx context.Context, cfg config.Config) (*s3SDKStorage, error) {
	if !cfg.S3Enabled || strings.TrimSpace(cfg.S3Bucket) == "" {
		return nil, nil
	}
	if signatureVersion := strings.TrimSpace(cfg.S3SignatureVersion); signatureVersion != "" && !strings.EqualFold(signatureVersion, "v4") {
		return nil, fmt.Errorf("S3_SIGNATURE_VERSION=%q is unsupported: AWS SDK for Go v2 uses SigV4", signatureVersion)
	}
	if (strings.TrimSpace(cfg.S3AccessKeyID) == "") != (strings.TrimSpace(cfg.S3SecretAccessKey) == "") {
		return nil, errors.New("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be configured together")
	}

	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(firstNonEmptyString(cfg.S3Region, "us-east-1")),
	}
	if strings.TrimSpace(cfg.S3AccessKeyID) != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKeyID,
			cfg.S3SecretAccessKey,
			cfg.S3SessionToken,
		)))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load AWS SDK config: %w", err)
	}
	awsCfg.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	if cfg.S3EnableChecksumMode {
		awsCfg.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenSupported
	} else {
		awsCfg.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	}

	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.HTTPClient = s3HTTPClient
		if cfg.S3RetryLimit > 0 {
			options.Retryer = awsretry.NewStandard(func(retryOptions *awsretry.StandardOptions) {
				retryOptions.MaxAttempts = cfg.S3RetryLimit + 1
			})
		} else {
			options.Retryer = aws.NopRetryer{}
		}
		if endpoint := s3SDKBaseEndpoint(cfg); endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
		if strings.TrimSpace(cfg.S3Endpoint) != "" {
			options.UsePathStyle = !cfg.S3OverridePathStyle
		}
	})
	uploader := transfermanager.New(client, func(options *transfermanager.Options) {
		options.MultipartUploadThreshold = int64(s3MultipartThresholdForConfig(cfg))
		options.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	return &s3SDKStorage{
		client:    client,
		presigner: s3.NewPresignClient(client),
		uploader:  uploader,
	}, nil
}

func s3SDKBaseEndpoint(cfg config.Config) string {
	if endpoint := strings.TrimSpace(cfg.S3Endpoint); endpoint != "" {
		return endpoint
	}
	if !cfg.S3HostnameSet && !cfg.S3ProtocolSet {
		return ""
	}
	protocol := strings.Trim(strings.TrimSpace(cfg.S3Protocol), ":/")
	if protocol == "" {
		protocol = "https"
	}
	host := strings.Trim(strings.TrimSpace(cfg.S3Hostname), "/")
	if host == "" {
		return ""
	}
	return protocol + "://" + host
}

func (s *Server) s3SDK(ctx context.Context) (*s3SDKStorage, error) {
	if s == nil || !s.s3ObjectStorageEnabled() {
		return nil, nil
	}
	if s.s3Storage != nil {
		return s.s3Storage, nil
	}
	return newS3SDKStorage(ctx, s.cfg)
}

func (s *Server) uploadPaperclipObject(ctx context.Context, key string, localPath string, contentType string) error {
	return s.uploadPaperclipObjectWithACL(ctx, key, localPath, contentType, s.cfg.S3Permission)
}

func (s *Server) uploadPaperclipObjectWithACL(ctx context.Context, key string, localPath string, contentType string, acl string) error {
	if !s.s3ObjectStorageEnabled() && !s.azureObjectStorageEnabled() && !s.swiftObjectStorageEnabled() {
		return nil
	}
	if s.s3ObjectStorageEnabled() {
		return s.putS3ObjectFileWithACL(ctx, key, localPath, contentType, acl)
	}
	if s.azureObjectStorageEnabled() {
		return s.putAzureBlobObjectFile(ctx, key, localPath, contentType)
	}
	if s.swiftObjectStorageEnabled() {
		return s.putSwiftObjectFile(ctx, key, localPath, contentType)
	}
	return nil
}

func (s *Server) deletePaperclipObject(ctx context.Context, key string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	_ = s.deletePaperclipObjectWithRetry(ctx, key)
}

func (s *Server) deletePaperclipObjectWithRetry(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	if s.s3ObjectStorageEnabled() {
		// S3 single-object operations use the SDK retryer configured solely by
		// S3_RETRY_LIMIT. Do not multiply it by the attachment-batch setting.
		return s.deleteS3Object(ctx, key)
	}
	var deleteObject func(context.Context, string) error
	switch {
	case s.azureObjectStorageEnabled():
		deleteObject = s.deleteAzureBlobObject
	case s.swiftObjectStorageEnabled():
		deleteObject = s.deleteSwiftObject
	default:
		return nil
	}
	return retryStorageObjectDelete(ctx, key, storageBatchDeleteTotalAttempts(s.cfg), deleteObject, storageDeleteRetryWait)
}

func (s *Server) deletePaperclipObjects(ctx context.Context, keys []string) error {
	keys = uniqueStorageObjectKeys(keys)
	if len(keys) == 0 {
		return nil
	}
	if s.s3ObjectStorageEnabled() {
		if len(keys) == 1 {
			return s.deleteS3Object(ctx, keys[0])
		}
		return s.deleteS3Objects(ctx, keys)
	}
	var deleteObject func(context.Context, string) error
	switch {
	case s.azureObjectStorageEnabled():
		deleteObject = s.deleteAzureBlobObject
	case s.swiftObjectStorageEnabled():
		deleteObject = s.deleteSwiftObject
	default:
		return nil
	}
	var errs []error
	for _, key := range keys {
		if err := retryStorageObjectDelete(ctx, key, storageBatchDeleteTotalAttempts(s.cfg), deleteObject, storageDeleteRetryWait); err != nil {
			errs = append(errs, fmt.Errorf("delete object %q: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

func storageBatchDeleteTotalAttempts(cfg config.Config) int {
	if cfg.S3BatchDeleteRetry > 0 {
		return cfg.S3BatchDeleteRetry
	}
	return 3
}

func retryStorageObjectDelete(ctx context.Context, key string, totalAttempts int, deleteObject func(context.Context, string) error, wait func(context.Context, int) error) error {
	if deleteObject == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	if totalAttempts < 1 {
		totalAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= totalAttempts; attempt++ {
		lastErr = deleteObject(ctx, key)
		if lastErr == nil {
			return nil
		}
		if attempt == totalAttempts || !s3DeleteErrorRetryable(lastErr) {
			return lastErr
		}
		if wait != nil {
			if err := wait(ctx, attempt); err != nil {
				return errors.Join(lastErr, err)
			}
		}
	}
	return lastErr
}

func waitForStorageDeleteRetry(ctx context.Context, attempt int) error {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	delay := time.Duration(1<<attempt) * time.Second
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func uniqueStorageObjectKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func accountImageObjectKey(accountID int64, kind string, style string, filename string) string {
	return accountImageObjectKeyWithCachePrefix(accountID, kind, style, filename, false)
}

func accountImageObjectKeyForAccount(account models.Account, kind string, style string, filename string) string {
	return accountImageObjectKeyWithCachePrefix(account.ID, kind, style, filename, accountImageUsesCachePrefix(account, kind))
}

func accountImageObjectKeyWithCachePrefix(accountID int64, kind string, style string, filename string, cachePrefix bool) string {
	dir := "avatars"
	if kind == "header" {
		dir = "headers"
	}
	parts := []string{"accounts", dir, strings.ReplaceAll(mediaPaperclipIDPartition(accountID), string(os.PathSeparator), "/"), style, filename}
	if cachePrefix {
		parts = append([]string{"cache"}, parts...)
	}
	return path.Join(parts...)
}

func accountImageUsesCachePrefix(account models.Account, kind string) bool {
	if kind == "header" {
		return account.HeaderUsesCachePrefix()
	}
	return account.AvatarUsesCachePrefix()
}

func accountImageAssetPath(account models.Account, kind string, style string, filename string) string {
	dir := "avatars"
	if kind == "header" {
		dir = "headers"
	}
	prefix := ""
	if accountImageUsesCachePrefix(account, kind) {
		prefix = "cache/"
	}
	return prefix + "accounts/" + dir + "/" + strings.ReplaceAll(mediaPaperclipIDPartition(account.ID), string(os.PathSeparator), "/") + "/" + style + "/" + url.PathEscape(filename)
}

func customEmojiObjectKey(emoji models.CustomEmoji, style string, filename string) string {
	prefix := "custom_emojis"
	if !emoji.Local() && emoji.ImageStorageSchemaVersion.Valid && emoji.ImageStorageSchemaVersion.Int64 >= 1 {
		prefix = "cache/custom_emojis"
	}
	return path.Join(prefix, "images", strings.ReplaceAll(adminPaperclipIDPartition(emoji.ID), string(os.PathSeparator), "/"), style, filename)
}

func siteUploadObjectKey(id int64, style string, filename string) string {
	return path.Join("site_uploads", "files", strings.ReplaceAll(mediaPaperclipIDPartition(id), string(os.PathSeparator), "/"), style, filename)
}

func backupDumpObjectKey(id int64, filename string) string {
	return path.Join("backups", "dumps", strings.ReplaceAll(mediaPaperclipIDPartition(id), string(os.PathSeparator), "/"), "original", filename)
}

func (s *Server) presignedS3ObjectURL(key string, expires time.Duration) string {
	if !s.s3ObjectStorageEnabled() {
		return ""
	}
	if expires <= 0 {
		expires = time.Hour
	}
	storage, err := s.s3SDK(context.Background())
	if err != nil {
		return ""
	}
	output, err := storage.presigner.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(strings.Trim(s.cfg.S3Bucket, "/")),
		Key:    aws.String(s.cfg.S3ObjectKey(key)),
	}, func(options *s3.PresignOptions) {
		options.Expires = expires
	})
	if err != nil {
		return ""
	}
	return output.URL
}

func (s *Server) s3ObjectStorageEnabled() bool {
	return s != nil &&
		s.cfg.S3Enabled &&
		strings.TrimSpace(s.cfg.S3Bucket) != ""
}

func (s *Server) azureObjectStorageEnabled() bool {
	return s != nil &&
		s.cfg.AzureEnabled &&
		strings.TrimSpace(s.cfg.AzureStorageAccount) != "" &&
		strings.TrimSpace(s.cfg.AzureStorageAccessKey) != "" &&
		strings.TrimSpace(s.cfg.AzureContainerName) != ""
}

func (s *Server) swiftObjectStorageEnabled() bool {
	return s != nil &&
		s.cfg.SwiftEnabled &&
		strings.TrimSpace(s.cfg.SwiftObjectURL) != "" &&
		strings.TrimSpace(s.cfg.SwiftContainer) != "" &&
		strings.TrimSpace(s.cfg.SwiftUsername) != "" &&
		strings.TrimSpace(s.cfg.SwiftPassword) != "" &&
		strings.TrimSpace(s.cfg.SwiftAuthURL) != "" &&
		(strings.TrimSpace(s.cfg.SwiftProjectID) != "" || strings.TrimSpace(s.cfg.SwiftTenant) != "")
}

func (s *Server) putS3Object(ctx context.Context, key string, body []byte, contentType string) error {
	return s.putS3ObjectWithACL(ctx, key, body, contentType, s.cfg.S3Permission)
}

func (s *Server) putS3ObjectWithACL(ctx context.Context, key string, body []byte, contentType string, acl string) error {
	return s.uploadS3Object(ctx, key, bytes.NewReader(body), int64(len(body)), contentType, acl)
}

func (s *Server) putS3ObjectFileWithACL(ctx context.Context, key string, localPath string, contentType string, acl string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return s.uploadS3Object(ctx, key, file, info.Size(), contentType, acl)
}

func (s *Server) uploadS3Object(ctx context.Context, key string, body io.Reader, size int64, contentType string, acl string) error {
	storage, err := s.s3SDK(ctx)
	if err != nil {
		return err
	}
	if storage == nil {
		return nil
	}
	input := &transfermanager.UploadObjectInput{
		Bucket:        aws.String(strings.Trim(s.cfg.S3Bucket, "/")),
		Key:           aws.String(s.cfg.S3ObjectKey(key)),
		Body:          body,
		ContentLength: aws.Int64(size),
		CacheControl:  aws.String(paperclipImmutableCacheControl),
	}
	if contentType = strings.TrimSpace(contentType); contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if acl != "" {
		input.ACL = tmtypes.ObjectCannedACL(acl)
	}
	if s.cfg.S3StorageClassSet {
		input.StorageClass = tmtypes.StorageClass(s.cfg.S3StorageClass)
	}
	_, err = storage.uploader.UploadObject(ctx, input)
	return err
}

func (s *Server) deleteS3Object(ctx context.Context, key string) error {
	storage, err := s.s3SDK(ctx)
	if err != nil {
		return err
	}
	if storage == nil {
		return nil
	}
	_, err = storage.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(strings.Trim(s.cfg.S3Bucket, "/")),
		Key:    aws.String(s.cfg.S3ObjectKey(key)),
	})
	return err
}

func (s *Server) deleteS3Objects(ctx context.Context, keys []string) error {
	keys = uniqueStorageObjectKeys(keys)
	if len(keys) == 0 {
		return nil
	}
	storage, err := s.s3SDK(ctx)
	if err != nil {
		return err
	}
	if storage == nil {
		return nil
	}
	limit := s.cfg.S3BatchDeleteLimit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	for start := 0; start < len(keys); start += limit {
		end := min(start+limit, len(keys))
		if err := s.deleteS3ObjectBatch(ctx, storage, keys[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deleteS3ObjectBatch(ctx context.Context, storage *s3SDKStorage, keys []string) error {
	totalAttempts := storageBatchDeleteTotalAttempts(s.cfg)
	pending := append([]string(nil), keys...)
	var lastErr error
	for attempt := 1; attempt <= totalAttempts && len(pending) > 0; attempt++ {
		objects := make([]s3types.ObjectIdentifier, 0, len(pending))
		for _, key := range pending {
			objects = append(objects, s3types.ObjectIdentifier{Key: aws.String(s.cfg.S3ObjectKey(key))})
		}
		output, err := storage.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(strings.Trim(s.cfg.S3Bucket, "/")),
			Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		if err != nil {
			lastErr = err
			if attempt == totalAttempts || !s3DeleteErrorRetryable(err) {
				return fmt.Errorf("delete S3 object batch: %w", err)
			}
			continue
		}
		pending = pending[:0]
		var permanent []string
		for _, objectErr := range output.Errors {
			key := strings.TrimSpace(aws.ToString(objectErr.Key))
			code := strings.TrimSpace(aws.ToString(objectErr.Code))
			if code == "NoSuchKey" || key == "" {
				continue
			}
			if s3DeleteCodeRetryable(code) {
				pending = append(pending, removeS3KeyPrefix(s.cfg.S3KeyPrefix, key))
			} else {
				permanent = append(permanent, code+":"+key)
			}
		}
		if len(permanent) > 0 {
			return fmt.Errorf("delete S3 object batch failed: %s", strings.Join(permanent, ", "))
		}
		lastErr = nil
	}
	if len(pending) > 0 {
		return fmt.Errorf("delete S3 object batch exhausted %d attempts for %d objects: %w", totalAttempts, len(pending), lastErr)
	}
	return nil
}

func removeS3KeyPrefix(prefix string, key string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return key
	}
	return strings.TrimPrefix(key, prefix+"/")
}

func s3DeleteCodeRetryable(code string) bool {
	switch code {
	case "InternalError", "OperationAborted", "RequestTimeout", "RequestTimeoutException", "ServiceUnavailable", "SlowDown", "Throttling", "ThrottlingException":
		return true
	default:
		return false
	}
}

func s3DeleteErrorRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}
	var storageHTTPError s3HTTPError
	if errors.As(err, &storageHTTPError) {
		return storageHTTPError.status == http.StatusRequestTimeout ||
			storageHTTPError.status == http.StatusTooEarly ||
			storageHTTPError.status == http.StatusTooManyRequests ||
			storageHTTPError.status >= http.StatusInternalServerError
	}
	status := s3SDKHTTPStatusCode(err)
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func (s *Server) s3MultipartThreshold() int {
	if s == nil {
		return 15 * 1024 * 1024
	}
	return s3MultipartThresholdForConfig(s.cfg)
}

func s3MultipartThresholdForConfig(cfg config.Config) int {
	if !cfg.S3MultipartThresholdSet {
		return 15 * 1024 * 1024
	}
	return cfg.S3MultipartThreshold
}

func (s *Server) putS3ObjectACL(ctx context.Context, key string, acl string) error {
	if !s.s3ObjectStorageEnabled() || strings.TrimSpace(key) == "" || acl == "" {
		return nil
	}
	storage, err := s.s3SDK(ctx)
	if err != nil {
		return err
	}
	_, err = storage.client.PutObjectAcl(ctx, &s3.PutObjectAclInput{
		ACL:    s3types.ObjectCannedACL(acl),
		Bucket: aws.String(strings.Trim(s.cfg.S3Bucket, "/")),
		Key:    aws.String(s.cfg.S3ObjectKey(key)),
	})
	if status := s3SDKHTTPStatusCode(err); status == http.StatusNotFound || status == http.StatusNotImplemented {
		return nil
	}
	return err
}

func (s *Server) getS3Object(ctx context.Context, key string) ([]byte, bool, error) {
	body, ok, err := s.getS3ObjectReader(ctx, key)
	if err != nil || !ok {
		return nil, ok, err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *Server) getS3ObjectReader(ctx context.Context, key string) (io.ReadCloser, bool, error) {
	if !s.s3ObjectStorageEnabled() || strings.TrimSpace(key) == "" {
		return nil, false, nil
	}
	storage, err := s.s3SDK(ctx)
	if err != nil {
		return nil, false, err
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(strings.Trim(s.cfg.S3Bucket, "/")),
		Key:    aws.String(s.cfg.S3ObjectKey(key)),
	}
	if s.cfg.S3EnableChecksumMode {
		input.ChecksumMode = s3types.ChecksumModeEnabled
	}
	output, err := storage.client.GetObject(ctx, input)
	if err != nil {
		if s3SDKHTTPStatusCode(err) == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	return output.Body, true, nil
}

func s3SDKHTTPStatusCode(err error) int {
	var responseError *awshttp.ResponseError
	if errors.As(err, &responseError) {
		return responseError.HTTPStatusCode()
	}
	return 0
}

func (s *Server) putAzureBlobObject(ctx context.Context, key string, body []byte, contentType string) error {
	req, err := s.newAzureBlobObjectRequest(ctx, http.MethodPut, key, bytes.NewReader(body), int64(len(body)), contentType)
	if err != nil {
		return err
	}
	return s.doPutAzureBlobObject(req)
}

func (s *Server) putAzureBlobObjectFile(ctx context.Context, key string, localPath string, contentType string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	req, err := s.newAzureBlobObjectRequest(ctx, http.MethodPut, key, file, info.Size(), contentType)
	if err != nil {
		_ = file.Close()
		return err
	}
	req.GetBody = func() (io.ReadCloser, error) {
		return os.Open(localPath)
	}
	defer file.Close()
	return s.doPutAzureBlobObject(req)
}

func (s *Server) doPutAzureBlobObject(req *http.Request) error {
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	s.signAzureBlobRequest(req)
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return s3HTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return nil
}

// copyAzureBlobObject uses Azure's server-side Copy Blob operation. Copy Blob
// preserves the source content properties and metadata, unlike downloading and
// re-uploading through the paon-admin process. The signed source URL is kept in
// the request header and is never included in logs or returned errors.
func (s *Server) copyAzureBlobObject(ctx context.Context, sourceKey string, destinationKey string) error {
	if !s.azureObjectStorageEnabled() || strings.TrimSpace(sourceKey) == "" || strings.TrimSpace(destinationKey) == "" || sourceKey == destinationKey {
		return nil
	}
	sourceURL := s.presignedAzureBlobURL(sourceKey, azureBlobCopyTimeout+time.Minute)
	if sourceURL == "" {
		return errors.New("build Azure blob copy source URL")
	}
	req, err := s.newAzureBlobObjectRequest(ctx, http.MethodPut, destinationKey, http.NoBody, 0, "")
	if err != nil {
		return err
	}
	req.Header.Set("x-ms-copy-source", sourceURL)
	s.signAzureBlobRequest(req)
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	statusCode := resp.StatusCode
	status := strings.ToLower(strings.TrimSpace(resp.Header.Get("x-ms-copy-status")))
	_ = resp.Body.Close()
	if statusCode == http.StatusNotFound {
		// Missing Paperclip objects are valid legacy state. Advancing the schema
		// version prevents every subsequent check from rediscovering the row.
		return nil
	}
	if statusCode < 200 || statusCode >= 300 {
		return s3HTTPError{status: statusCode}
	}
	switch status {
	case "", "success":
		if status == "success" || statusCode != http.StatusAccepted {
			return nil
		}
	case "failed", "aborted":
		return fmt.Errorf("Azure blob copy finished with status %s", status)
	case "pending":
		// Poll below. Copy Blob is asynchronous even when the source and
		// destination are in the same account.
	default:
		return errors.New("Azure blob copy returned an unknown status")
	}
	return s.waitForAzureBlobCopy(ctx, destinationKey)
}

func (s *Server) waitForAzureBlobCopy(ctx context.Context, destinationKey string) error {
	pollCtx, cancel := context.WithTimeout(ctx, azureBlobCopyTimeout)
	defer cancel()
	for {
		timer := time.NewTimer(azureBlobCopyPollInterval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return pollCtx.Err()
		case <-timer.C:
		}
		req, err := s.newAzureBlobObjectRequest(pollCtx, http.MethodHead, destinationKey, http.NoBody, 0, "")
		if err != nil {
			return err
		}
		s.signAzureBlobRequest(req)
		resp, err := s3HTTPClient.Do(req)
		if err != nil {
			return err
		}
		statusCode := resp.StatusCode
		copyStatus := strings.ToLower(strings.TrimSpace(resp.Header.Get("x-ms-copy-status")))
		_ = resp.Body.Close()
		if statusCode < 200 || statusCode >= 300 {
			return s3HTTPError{status: statusCode}
		}
		switch copyStatus {
		case "success":
			return nil
		case "pending":
			continue
		case "":
			return errors.New("Azure blob copy status is missing")
		case "failed", "aborted":
			return fmt.Errorf("Azure blob copy finished with status %s", copyStatus)
		default:
			return errors.New("Azure blob copy returned an unknown status")
		}
	}
}

func (s *Server) deleteAzureBlobObject(ctx context.Context, key string) error {
	req, err := s.newAzureBlobObjectRequest(ctx, http.MethodDelete, key, http.NoBody, 0, "")
	if err != nil {
		return err
	}
	s.signAzureBlobRequest(req)
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return s3HTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return nil
}

func (s *Server) putSwiftObject(ctx context.Context, key string, body []byte, contentType string) error {
	return s.putSwiftObjectReader(ctx, key, bytes.NewReader(body), int64(len(body)), contentType)
}

func (s *Server) putSwiftObjectFile(ctx context.Context, key string, localPath string, contentType string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	defer file.Close()
	return s.putSwiftObjectReader(ctx, key, file, info.Size(), contentType)
}

func (s *Server) putSwiftObjectReader(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	token, err := s.swiftAuthToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.swiftObjectURL(key), body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("X-Auth-Token", token)
	req.Header.Set("Cache-Control", paperclipImmutableCacheControl)
	if contentType = strings.TrimSpace(contentType); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return s3HTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return nil
}

// copySwiftObject uses Swift's synchronous server-side COPY operation. The
// destination header is URL-escaped per Swift's object-copy contract while the
// authentication token remains confined to the request header.
func (s *Server) copySwiftObject(ctx context.Context, sourceKey string, destinationKey string) error {
	if !s.swiftObjectStorageEnabled() || strings.TrimSpace(sourceKey) == "" || strings.TrimSpace(destinationKey) == "" || sourceKey == destinationKey {
		return nil
	}
	token, err := s.swiftAuthToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "COPY", s.swiftObjectURL(sourceKey), http.NoBody)
	if err != nil {
		return err
	}
	destination := &url.URL{Path: "/" + path.Join(strings.Trim(s.cfg.SwiftContainer, "/"), strings.Trim(destinationKey, "/"))}
	req.Header.Set("Destination", destination.EscapedPath())
	req.Header.Set("X-Auth-Token", token)
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return s3HTTPError{status: resp.StatusCode}
	}
	return nil
}

func (s *Server) deleteSwiftObject(ctx context.Context, key string) error {
	token, err := s.swiftAuthToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.swiftObjectURL(key), http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Token", token)
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return s3HTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return nil
}

func (s *Server) swiftAuthToken(ctx context.Context) (string, error) {
	body, err := json.Marshal(s.swiftAuthPayload())
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, swiftAuthTokensURL(s.cfg.SwiftAuthURL), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", s3HTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	token := strings.TrimSpace(resp.Header.Get("X-Subject-Token"))
	if token == "" {
		return "", s3HTTPError{status: resp.StatusCode, body: "missing X-Subject-Token"}
	}
	return token, nil
}

func (s *Server) swiftAuthPayload() map[string]any {
	domain := s.swiftDomainNameForRequest()
	project := map[string]any{"domain": map[string]any{"name": domain}}
	if projectID := strings.TrimSpace(s.cfg.SwiftProjectID); projectID != "" {
		project["id"] = projectID
	} else {
		project["name"] = strings.TrimSpace(s.cfg.SwiftTenant)
	}
	return map[string]any{
		"auth": map[string]any{
			"identity": map[string]any{
				"methods": []string{"password"},
				"password": map[string]any{
					"user": map[string]any{
						"name":     s.cfg.SwiftUsername,
						"password": s.cfg.SwiftPassword,
						"domain":   map[string]any{"name": domain},
					},
				},
			},
			"scope": map[string]any{"project": project},
		},
	}
}

func swiftAuthTokensURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasSuffix(raw, "/auth/tokens") {
		return raw
	}
	return raw + "/auth/tokens"
}

func (s *Server) swiftObjectURL(key string) string {
	parsed, err := url.Parse(s.cfg.SwiftObjectURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/" + path.Join(swiftObjectURLPathPrefix(parsed.EscapedPath(), s.cfg.SwiftContainer), strings.Trim(key, "/"))
	parsed.RawPath = ""
	return parsed.String()
}

func (s *Server) newAzureBlobObjectRequest(ctx context.Context, method string, key string, body io.Reader, size int64, contentType string) (*http.Request, error) {
	rawURL := s.azureBlobObjectURL(key)
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("x-ms-version", azureBlobSASVersion)
	if size > 0 {
		req.ContentLength = size
	}
	if contentType = strings.TrimSpace(contentType); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func (s *Server) azureBlobObjectURL(key string) string {
	baseURL := s.azureBlobBaseURL()
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/" + path.Join(strings.Trim(parsed.EscapedPath(), "/"), strings.Trim(s.cfg.AzureContainerName, "/"), strings.Trim(key, "/"))
	parsed.RawPath = ""
	return parsed.String()
}

func (s *Server) signAzureBlobRequest(req *http.Request) {
	account := strings.TrimSpace(s.cfg.AzureStorageAccount)
	keyBytes, err := base64.StdEncoding.DecodeString(s.cfg.AzureStorageAccessKey)
	if err != nil || account == "" {
		return
	}
	stringToSign := azureBlobStringToSign(account, req)
	mac := hmac.New(sha256.New, keyBytes)
	_, _ = mac.Write([]byte(stringToSign))
	req.Header.Set("Authorization", "SharedKey "+account+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

func azureBlobStringToSign(account string, req *http.Request) string {
	contentLength := ""
	if req.ContentLength > 0 {
		contentLength = strconv.FormatInt(req.ContentLength, 10)
	}
	contentType := req.Header.Get("Content-Type")
	headers := azureBlobCanonicalizedHeaders(req)
	resource := "/blob/" + account + req.URL.EscapedPath()
	return strings.Join([]string{
		req.Method,
		"",
		"",
		contentLength,
		"",
		contentType,
		"",
		"",
		"",
		"",
		"",
		"",
		headers + resource,
	}, "\n")
}

func azureBlobCanonicalizedHeaders(req *http.Request) string {
	names := make([]string, 0, len(req.Header))
	for name := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-ms-") {
			names = append(names, lower)
		}
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		values := req.Header.Values(name)
		for i, value := range values {
			values[i] = strings.Join(strings.Fields(value), " ")
		}
		out.WriteString(name)
		out.WriteByte(':')
		out.WriteString(strings.Join(values, ","))
		out.WriteByte('\n')
	}
	return out.String()
}

func (s *Server) swiftDomainNameForRequest() string {
	if s.cfg.SwiftDomainNameSet {
		return s.cfg.SwiftDomainName
	}
	return firstNonEmptyString(s.cfg.SwiftDomainName, "default")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type s3HTTPError struct {
	status int
	body   string
}

func (e s3HTTPError) Error() string {
	if e.body == "" {
		return "s3 request failed with status " + http.StatusText(e.status)
	}
	return "s3 request failed with status " + http.StatusText(e.status) + ": " + e.body
}
