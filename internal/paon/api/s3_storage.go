package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	s3HTTPOpenTimeout              = 5 * time.Second
	s3HTTPReadTimeout              = 5 * time.Second
	s3HTTPExpectContinueTimeout    = 10 * time.Second
	paperclipImmutableCacheControl = "public, max-age=315576000, immutable"
)

var s3HTTPClient = &http.Client{Transport: s3HTTPTransport()}

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
	if s.s3ObjectStorageEnabled() {
		_ = s.deleteS3Object(ctx, key)
		return
	}
	if s.azureObjectStorageEnabled() {
		_ = s.deleteAzureBlobObject(ctx, key)
		return
	}
	if s.swiftObjectStorageEnabled() {
		_ = s.deleteSwiftObject(ctx, key)
	}
}

func accountImageObjectKey(accountID int64, kind string, style string, filename string) string {
	dir := "avatars"
	if kind == "header" {
		dir = "headers"
	}
	return path.Join("accounts", dir, strings.ReplaceAll(mediaPaperclipIDPartition(accountID), string(os.PathSeparator), "/"), style, filename)
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
	rawURL := s.s3ObjectRequestURL(key)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	if s.s3SignatureVersionV2() {
		query := req.URL.Query()
		query.Set("AWSAccessKeyId", s.cfg.S3AccessKeyID)
		query.Set("Expires", strconv.FormatInt(time.Now().UTC().Add(expires).Unix(), 10))
		if token := strings.TrimSpace(s.cfg.S3SessionToken); token != "" {
			query.Set("x-amz-security-token", token)
		}
		req.URL.RawQuery = query.Encode()
		query.Set("Signature", s.signS3V2Query(req))
		req.URL.RawQuery = query.Encode()
		return req.URL.String()
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	region := s.s3RegionForRequest()
	scope := date + "/" + region + "/s3/aws4_request"
	query := req.URL.Query()
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", s.cfg.S3AccessKeyID+"/"+scope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	query.Set("X-Amz-SignedHeaders", "host")
	if token := strings.TrimSpace(s.cfg.S3SessionToken); token != "" {
		query.Set("X-Amz-Security-Token", token)
	}
	req.URL.RawQuery = query.Encode()

	canonical := http.MethodGet + "\n" +
		canonicalURI(req.URL.EscapedPath()) + "\n" +
		req.URL.RawQuery + "\n" +
		"host:" + req.URL.Host + "\n\n" +
		"host\n" +
		"UNSIGNED-PAYLOAD"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + s3SHA256HexString(canonical)
	signature := hmacHex(s3SigningKey(s.cfg.S3SecretAccessKey, date, region, "s3"), stringToSign)
	query.Set("X-Amz-Signature", signature)
	req.URL.RawQuery = query.Encode()
	return req.URL.String()
}

func (s *Server) s3ObjectStorageEnabled() bool {
	return s != nil &&
		s.cfg.S3Enabled &&
		strings.TrimSpace(s.cfg.S3Bucket) != "" &&
		strings.TrimSpace(s.cfg.S3AccessKeyID) != "" &&
		strings.TrimSpace(s.cfg.S3SecretAccessKey) != ""
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
	hash := s3SHA256Hex(body)
	req, err := s.newS3ObjectRequestWithPayloadHash(ctx, http.MethodPut, key, bytes.NewReader(body), hash)
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(body))
	return s.doPutS3ObjectWithACL(req, contentType, acl, hash)
}

func (s *Server) putS3ObjectFileWithACL(ctx context.Context, key string, localPath string, contentType string, acl string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	hash, size, err := s3FileSHA256Hex(file)
	if err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return err
	}
	req, err := s.newS3ObjectRequestWithPayloadHash(ctx, http.MethodPut, key, file, hash)
	if err != nil {
		_ = file.Close()
		return err
	}
	req.ContentLength = size
	req.GetBody = func() (io.ReadCloser, error) {
		return os.Open(localPath)
	}
	defer file.Close()
	return s.doPutS3ObjectWithACL(req, contentType, acl, hash)
}

func (s *Server) doPutS3ObjectWithACL(req *http.Request, contentType string, acl string, payloadHash string) error {
	if contentType = strings.TrimSpace(contentType); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Amz-Multipart-Threshold", strconv.Itoa(s.s3MultipartThreshold()))
	req.Header.Set("Cache-Control", paperclipImmutableCacheControl)
	if acl != "" {
		req.Header.Set("X-Amz-Acl", acl)
	}
	if s.cfg.S3StorageClassSet {
		req.Header.Set("X-Amz-Storage-Class", s.cfg.S3StorageClass)
	}
	s.signS3RequestWithPayloadHash(req, payloadHash)
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

func (s *Server) deleteS3Object(ctx context.Context, key string) error {
	req, err := s.newS3ObjectRequest(ctx, http.MethodDelete, key, http.NoBody, nil)
	if err != nil {
		return err
	}
	s.signS3Request(req, nil)
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

func (s *Server) s3MultipartThreshold() int {
	if s == nil || !s.cfg.S3MultipartThresholdSet {
		return 15 * 1024 * 1024
	}
	return s.cfg.S3MultipartThreshold
}

func (s *Server) putS3ObjectACL(ctx context.Context, key string, acl string) error {
	if !s.s3ObjectStorageEnabled() || strings.TrimSpace(key) == "" || acl == "" {
		return nil
	}
	req, err := s.newS3ObjectRequest(ctx, http.MethodPut, key, http.NoBody, nil)
	if err != nil {
		return err
	}
	query := req.URL.Query()
	query.Set("acl", "")
	req.URL.RawQuery = query.Encode()
	req.Header.Set("X-Amz-Acl", acl)
	s.signS3Request(req, nil)
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return s3HTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return nil
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
	req, err := s.newS3ObjectRequest(ctx, http.MethodGet, key, http.NoBody, nil)
	if err != nil {
		return nil, false, err
	}
	if s.cfg.S3EnableChecksumMode {
		req.Header.Set("X-Amz-Checksum-Mode", "ENABLED")
	}
	s.signS3Request(req, nil)
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, false, s3HTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	return resp.Body, true, nil
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

func (s *Server) newS3ObjectRequest(ctx context.Context, method string, key string, body io.Reader, payload []byte) (*http.Request, error) {
	return s.newS3ObjectRequestWithPayloadHash(ctx, method, key, body, s3SHA256Hex(payload))
}

func (s *Server) newS3ObjectRequestWithPayloadHash(ctx context.Context, method string, key string, body io.Reader, payloadHash string) (*http.Request, error) {
	rawURL := s.s3ObjectRequestURL(key)
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if !s.s3SignatureVersionV2() {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}
	return req, nil
}

func (s *Server) s3ObjectRequestURL(key string) string {
	key = strings.TrimLeft(key, "/")
	bucket := strings.Trim(s.cfg.S3Bucket, "/")
	if endpoint := strings.TrimRight(strings.TrimSpace(s.cfg.S3Endpoint), "/"); endpoint != "" {
		if parsed, err := url.Parse(endpoint); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			if s.cfg.S3OverridePathStyle && bucket != "" {
				parsed.Host = bucket + "." + parsed.Host
				parsed.Path = "/" + path.Join(strings.Trim(parsed.Path, "/"), key)
			} else {
				parsed.Path = "/" + path.Join(strings.Trim(parsed.Path, "/"), bucket, key)
			}
			parsed.RawPath = ""
			return parsed.String()
		}
		parsed := url.URL{Path: "/" + path.Join(strings.Trim(s.cfg.S3Bucket, "/"), key)}
		return endpoint + parsed.String()
	}
	protocol := strings.TrimRight(strings.TrimSpace(s.cfg.S3Protocol), ":/")
	if protocol == "" && !s.cfg.S3ProtocolSet {
		protocol = "https"
	}
	host := strings.Trim(strings.TrimSpace(s.cfg.S3Hostname), "/")
	if host == "" && !s.cfg.S3HostnameSet {
		host = "s3-" + s.s3RegionForRequest() + ".amazonaws.com"
	}
	if strings.EqualFold(protocol, "https") && strings.Contains(bucket, ".") && !s.cfg.S3OverridePathStyle {
		return (&url.URL{
			Scheme: protocol,
			Host:   host,
			Path:   "/" + path.Join(bucket, key),
		}).String()
	}
	return (&url.URL{
		Scheme: protocol,
		Host:   bucket + "." + host,
		Path:   "/" + key,
	}).String()
}

func (s *Server) signS3Request(req *http.Request, payload []byte) {
	s.signS3RequestWithPayloadHash(req, s3SHA256Hex(payload))
}

func (s *Server) signS3RequestWithPayloadHash(req *http.Request, payloadHash string) {
	if s.s3SignatureVersionV2() {
		s.signS3V2Request(req)
		return
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	region := s.s3RegionForRequest()
	service := "s3"
	scope := date + "/" + region + "/" + service + "/aws4_request"

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if token := strings.TrimSpace(s.cfg.S3SessionToken); token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}

	canonicalRequest, signedHeaders := canonicalS3Request(req)
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + s3SHA256HexString(canonicalRequest)
	signingKey := s3SigningKey(s.cfg.S3SecretAccessKey, date, region, service)
	signature := hmacHex(signingKey, stringToSign)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.cfg.S3AccessKeyID+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func (s *Server) s3SignatureVersionV2() bool {
	return strings.EqualFold(strings.TrimSpace(s.cfg.S3SignatureVersion), "v2")
}

func (s *Server) s3RegionForRequest() string {
	if s.cfg.S3RegionSet {
		return s.cfg.S3Region
	}
	return firstNonEmptyString(s.cfg.S3Region, "us-east-1")
}

func (s *Server) swiftDomainNameForRequest() string {
	if s.cfg.SwiftDomainNameSet {
		return s.cfg.SwiftDomainName
	}
	return firstNonEmptyString(s.cfg.SwiftDomainName, "default")
}

func canonicalS3Request(req *http.Request) (string, string) {
	headers := map[string]string{
		"host":                 req.URL.Host,
		"x-amz-content-sha256": req.Header.Get("X-Amz-Content-Sha256"),
		"x-amz-date":           req.Header.Get("X-Amz-Date"),
	}
	for _, name := range []string{"X-Amz-Acl", "X-Amz-Checksum-Mode", "X-Amz-Security-Token", "X-Amz-Storage-Class"} {
		if values, ok := req.Header[name]; ok && len(values) > 0 {
			value := values[0]
			headers[strings.ToLower(name)] = strings.Join(strings.Fields(value), " ")
		}
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(headers[name])
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")
	return req.Method + "\n" +
		canonicalURI(req.URL.EscapedPath()) + "\n" +
		req.URL.RawQuery + "\n" +
		canonicalHeaders.String() + "\n" +
		signedHeaders + "\n" +
		req.Header.Get("X-Amz-Content-Sha256"), signedHeaders
}

func (s *Server) signS3V2Request(req *http.Request) {
	req.Header.Del("X-Amz-Date")
	req.Header.Del("X-Amz-Content-Sha256")
	req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	if token := strings.TrimSpace(s.cfg.S3SessionToken); token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}
	signature := s.signS3V2String(s3V2StringToSign(req, s.cfg.S3Bucket, false))
	req.Header.Set("Authorization", "AWS "+s.cfg.S3AccessKeyID+":"+signature)
}

func (s *Server) signS3V2Query(req *http.Request) string {
	return s.signS3V2String(s3V2StringToSign(req, s.cfg.S3Bucket, true))
}

func (s *Server) signS3V2String(value string) string {
	mac := hmac.New(sha1.New, []byte(s.cfg.S3SecretAccessKey))
	_, _ = mac.Write([]byte(value))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func s3V2StringToSign(req *http.Request, bucket string, presigned bool) string {
	date := req.Header.Get("Date")
	if presigned {
		date = req.URL.Query().Get("Expires")
	}
	if req.Header.Get("X-Amz-Date") != "" {
		date = ""
	}
	return strings.Join([]string{
		req.Method,
		req.Header.Get("Content-Md5"),
		req.Header.Get("Content-Type"),
		date,
		s3V2CanonicalizedAmzHeaders(req) + s3V2CanonicalizedResource(req, bucket),
	}, "\n")
}

func s3V2CanonicalizedAmzHeaders(req *http.Request) string {
	values := map[string][]string{}
	for name, headerValues := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") {
			values[lower] = append(values[lower], headerValues...)
		}
	}
	for name, queryValues := range req.URL.Query() {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") {
			values[lower] = append(values[lower], queryValues...)
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	for _, name := range names {
		headerValues := values[name]
		for i, value := range headerValues {
			headerValues[i] = strings.Join(strings.Fields(value), " ")
		}
		sort.Strings(headerValues)
		out.WriteString(name)
		out.WriteByte(':')
		out.WriteString(strings.Join(headerValues, ","))
		out.WriteByte('\n')
	}
	return out.String()
}

func s3V2CanonicalizedResource(req *http.Request, bucket string) string {
	resource := req.URL.EscapedPath()
	if resource == "" {
		resource = "/"
	}
	trimmedBucket := strings.Trim(bucket, "/")
	if trimmedBucket != "" && strings.HasPrefix(strings.ToLower(req.URL.Host), strings.ToLower(trimmedBucket)+".") {
		resource = "/" + trimmedBucket + resource
	}
	if subresource := s3V2CanonicalSubresources(req.URL.Query()); subresource != "" {
		resource += "?" + subresource
	}
	return resource
}

func s3V2CanonicalSubresources(query url.Values) string {
	allowed := map[string]struct{}{
		"acl":                          {},
		"cors":                         {},
		"delete":                       {},
		"lifecycle":                    {},
		"location":                     {},
		"logging":                      {},
		"notification":                 {},
		"partNumber":                   {},
		"policy":                       {},
		"requestPayment":               {},
		"response-cache-control":       {},
		"response-content-disposition": {},
		"response-content-encoding":    {},
		"response-content-language":    {},
		"response-content-type":        {},
		"response-expires":             {},
		"torrent":                      {},
		"uploadId":                     {},
		"uploads":                      {},
		"versionId":                    {},
		"versioning":                   {},
		"versions":                     {},
		"website":                      {},
	}
	keys := make([]string, 0)
	for key := range query {
		if _, ok := allowed[key]; ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		values := append([]string(nil), query[key]...)
		sort.Strings(values)
		if len(values) == 0 || (len(values) == 1 && values[0] == "") {
			parts = append(parts, key)
			continue
		}
		for _, value := range values {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, "&")
}

func canonicalURI(escapedPath string) string {
	if escapedPath == "" {
		return "/"
	}
	parsed, err := url.PathUnescape(escapedPath)
	if err != nil {
		return escapedPath
	}
	segments := strings.Split(parsed, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func s3SigningKey(secret string, date string, region string, service string) []byte {
	kDate := hmacBytes([]byte("AWS4"+secret), date)
	kRegion := hmacBytes(kDate, region)
	kService := hmacBytes(kRegion, service)
	return hmacBytes(kService, "aws4_request")
}

func hmacBytes(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}

func hmacHex(key []byte, value string) string {
	return hex.EncodeToString(hmacBytes(key, value))
}

func s3SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func s3FileSHA256Hex(file *os.File) (string, int64, error) {
	if file == nil {
		return "", 0, fmt.Errorf("file is nil")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func s3SHA256HexString(value string) string {
	return s3SHA256Hex([]byte(value))
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
