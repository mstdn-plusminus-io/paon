package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestS3SDKPresignsGetObjectWithSigV4(t *testing.T) {
	s := &Server{cfg: config.Config{
		S3Enabled:          true,
		S3Bucket:           "bucket-name",
		S3Endpoint:         "https://storage.example.test",
		S3AccessKeyID:      "access",
		S3SecretAccessKey:  "secret",
		S3Region:           "ap-northeast-1",
		S3SignatureVersion: "v4",
		S3SessionToken:     "session-token",
	}}

	raw := s.presignedS3ObjectURL("media/file.png", time.Minute)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || query.Get("X-Amz-Credential") == "" || query.Get("X-Amz-Signature") == "" {
		t.Fatalf("S3 SDK presigned query = %#v", query)
	}
	if query.Get("X-Amz-Expires") != "60" {
		t.Fatalf("S3 SDK presigned expiry = %q", query.Get("X-Amz-Expires"))
	}
	if query.Get("X-Amz-Security-Token") != "session-token" {
		t.Fatalf("S3 SDK presigned session token missing: %#v", query)
	}
}

func TestS3SDKUsesPathStyleForDottedBucketOverHTTPS(t *testing.T) {
	s := &Server{cfg: config.Config{
		S3Enabled:         true,
		S3Bucket:          "media.stg.mstdn.plusminus.io",
		S3Region:          "ap-northeast-1",
		S3Protocol:        "https",
		S3ProtocolSet:     true,
		S3Hostname:        "s3-ap-northeast-1.amazonaws.com",
		S3HostnameSet:     true,
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
	}}

	raw := s.presignedS3ObjectURL("media_attachments/files/000/000/042/original/photo.png", time.Minute)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "s3-ap-northeast-1.amazonaws.com" || parsed.Path != "/media.stg.mstdn.plusminus.io/media_attachments/files/000/000/042/original/photo.png" {
		t.Fatalf("S3 SDK dotted-bucket URL = %q", raw)
	}
}

func TestS3SDKCanOverrideCustomEndpointPathStyle(t *testing.T) {
	s := &Server{cfg: config.Config{
		S3Enabled:           true,
		S3Bucket:            "media-bucket",
		S3Endpoint:          "https://storage.example.test",
		S3Region:            "ap-northeast-1",
		S3OverridePathStyle: true,
		S3AccessKeyID:       "access",
		S3SecretAccessKey:   "secret",
	}}

	raw := s.presignedS3ObjectURL("media/file.png", time.Minute)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "media-bucket.storage.example.test" || parsed.Path != "/media/file.png" {
		t.Fatalf("S3 SDK virtual-hosted URL = %q", raw)
	}
}

func TestS3SDKRejectsSignatureVersionV2(t *testing.T) {
	_, err := newS3SDKStorage(context.Background(), config.Config{
		S3Enabled:          true,
		S3Bucket:           "bucket-name",
		S3SignatureVersion: "v2",
	})
	if err == nil || !strings.Contains(err.Error(), "AWS SDK for Go v2 uses SigV4") {
		t.Fatalf("newS3SDKStorage error = %v", err)
	}
}

func TestUploadPaperclipObjectStreamsLocalFilesToObjectStorage(t *testing.T) {
	src, err := os.ReadFile("s3_storage.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, src, "uploadPaperclipObjectWithACL")
	if strings.Contains(body, "os.ReadFile") {
		t.Fatalf("uploadPaperclipObjectWithACL must not read whole media/backup files into memory:\n%s", body)
	}
	for _, want := range []string{
		`s.putS3ObjectFileWithACL(ctx, key, localPath, contentType, acl)`,
		`s.putAzureBlobObjectFile(ctx, key, localPath, contentType)`,
		`s.putSwiftObjectFile(ctx, key, localPath, contentType)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("uploadPaperclipObjectWithACL missing streaming object-storage path %q:\n%s", want, body)
		}
	}
}

func TestS3ACLHeadersPreserveRawRailsPermissionValue(t *testing.T) {
	var gotPutACL string
	var gotObjectACL string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("X-Amz-Multipart-Threshold"); got != "" {
			t.Fatalf("AWS SDK upload sent internal multipart threshold header %q", got)
		}
		if r.URL.RawQuery == "acl=" {
			gotObjectACL = r.Header.Get("X-Amz-Acl")
		} else {
			gotPutACL = r.Header.Get("X-Amz-Acl")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}

	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, []byte("image-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{
		S3Enabled:         true,
		S3Bucket:          "bucket-name",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "ap-northeast-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
	}}

	if err := s.uploadPaperclipObjectWithACL(context.Background(), "media_attachments/files/000/000/042/original/photo.png", path, "image/png", " public-read "); err != nil {
		t.Fatal(err)
	}
	if err := s.putS3ObjectACL(context.Background(), "media_attachments/files/000/000/042/original/photo.png", " public-read "); err != nil {
		t.Fatal(err)
	}
	if gotPutACL != " public-read " || gotObjectACL != " public-read " {
		t.Fatalf("raw S3 ACLs = put %q object %q", gotPutACL, gotObjectACL)
	}
}

func TestAccountImageObjectKeyUsesRailsPaperclipIDPartition(t *testing.T) {
	if got := accountImageObjectKey(42, "avatar", "original", "avatar.png"); got != "accounts/avatars/000/000/042/original/avatar.png" {
		t.Fatalf("avatar key = %q", got)
	}
	if got := accountImageObjectKey(42, "header", "static", "header.png"); got != "accounts/headers/000/000/042/static/header.png" {
		t.Fatalf("header key = %q", got)
	}
	remote := models.Account{
		ID:                         42,
		Domain:                     sql.NullString{String: "remote.example", Valid: true},
		AvatarStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
		HeaderStorageSchemaVersion: sql.NullInt64{Int64: 1, Valid: true},
	}
	if got := accountImageObjectKeyForAccount(remote, "avatar", "original", "avatar.png"); got != "cache/accounts/avatars/000/000/042/original/avatar.png" {
		t.Fatalf("remote avatar key = %q", got)
	}
	if got := accountImageObjectKeyForAccount(remote, "header", "static", "header.png"); got != "cache/accounts/headers/000/000/042/static/header.png" {
		t.Fatalf("remote header key = %q", got)
	}
}

func TestS3SDKDefaultsBlankRegionToUSEast1(t *testing.T) {
	var gotAuthorization string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuthorization = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("object-bytes")), Request: r}, nil
	})}
	s := &Server{cfg: config.Config{
		S3Enabled:         true,
		S3Bucket:          "bucket-name",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "",
		S3RegionSet:       true,
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
	}}

	if _, _, err := s.getS3Object(context.Background(), "media_attachments/files/000/000/042/original/photo.png"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotAuthorization, "/us-east-1/s3/aws4_request") {
		t.Fatalf("AWS SDK Authorization region = %q", gotAuthorization)
	}
}

func TestSwiftAuthPayloadPreservesExplicitBlankRailsDomainName(t *testing.T) {
	s := &Server{cfg: config.Config{SwiftDomainName: "", SwiftDomainNameSet: true}}
	payload := s.swiftAuthPayload()
	auth := payload["auth"].(map[string]any)
	scope := auth["scope"].(map[string]any)
	project := scope["project"].(map[string]any)
	domain := project["domain"].(map[string]any)
	if got, _ := domain["name"].(string); got != "" {
		t.Fatalf("Swift domain name = %q, want explicit blank", got)
	}

	s.cfg.SwiftDomainNameSet = false
	payload = s.swiftAuthPayload()
	auth = payload["auth"].(map[string]any)
	scope = auth["scope"].(map[string]any)
	project = scope["project"].(map[string]any)
	domain = project["domain"].(map[string]any)
	if got, _ := domain["name"].(string); got != "default" {
		t.Fatalf("Swift default domain name = %q, want default", got)
	}
}

func TestRemoveMediaAttachmentDeletesKnownS3Objects(t *testing.T) {
	var mu sync.Mutex
	var deleteBodies []string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Query().Has("delete") == false {
			t.Fatalf("method = %q", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		deleteBodies = append(deleteBodies, string(body))
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)), Request: r}, nil
	})}

	root := t.TempDir()
	s := &Server{cfg: config.Config{
		PublicDir:         root,
		S3Enabled:         true,
		S3Bucket:          "bucket-name",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "us-east-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
	}}

	s.removeMediaAttachmentLocalFiles(models.MediaAttachment{
		ID:                42,
		FileFileName:      sql.NullString{String: "photo.png", Valid: true},
		ThumbnailFileName: sql.NullString{String: "photo-small.png", Valid: true},
	})

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"media_attachments/files/000/000/042/original/photo.png",
		"media_attachments/thumbnails/000/000/042/original/photo-small.png",
	}
	if len(deleteBodies) != 1 {
		t.Fatalf("delete requests = %#v", deleteBodies)
	}
	for _, key := range want {
		if !strings.Contains(deleteBodies[0], "<Key>"+key+"</Key>") {
			t.Fatalf("delete body missing %q: %s", key, deleteBodies[0])
		}
	}
}

func TestDeleteS3ObjectsRetriesOnlyTransientPartialFailuresWithPrefix(t *testing.T) {
	var bodies []string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		responseBody := `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`
		if len(bodies) == 1 {
			responseBody = `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Error><Key>tenant/media/retry.png</Key><Code>SlowDown</Code><Message>retry</Message></Error></DeleteResult>`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: r}, nil
	})}
	s := &Server{cfg: config.Config{
		S3Enabled:          true,
		S3Bucket:           "bucket-name",
		S3Endpoint:         "https://storage.example.test",
		S3Region:           "us-east-1",
		S3AccessKeyID:      "access",
		S3SecretAccessKey:  "secret",
		S3KeyPrefix:        "tenant",
		S3BatchDeleteLimit: 1000,
		S3BatchDeleteRetry: 3,
	}}

	if err := s.deleteS3Objects(context.Background(), []string{"media/ok.png", "media/retry.png", "media/ok.png"}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("delete requests = %d, want 2", len(bodies))
	}
	for _, key := range []string{"tenant/media/ok.png", "tenant/media/retry.png"} {
		if !strings.Contains(bodies[0], "<Key>"+key+"</Key>") {
			t.Fatalf("first delete body missing %q: %s", key, bodies[0])
		}
	}
	if strings.Contains(bodies[1], "tenant/tenant/") || strings.Contains(bodies[1], "tenant/media/ok.png") || !strings.Contains(bodies[1], "<Key>tenant/media/retry.png</Key>") {
		t.Fatalf("partial retry body = %s", bodies[1])
	}
}

func TestDeleteS3ObjectsHonorsBatchLimit(t *testing.T) {
	requests := 0
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(`<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)), Request: r}, nil
	})}
	s := &Server{cfg: config.Config{
		S3Enabled:          true,
		S3Bucket:           "bucket-name",
		S3Endpoint:         "https://storage.example.test",
		S3Region:           "us-east-1",
		S3AccessKeyID:      "access",
		S3SecretAccessKey:  "secret",
		S3BatchDeleteLimit: 2,
		S3BatchDeleteRetry: 3,
	}}
	if err := s.deleteS3Objects(context.Background(), []string{"a", "b", "c"}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("delete requests = %d, want 2", requests)
	}
}

func TestUploadPaperclipObjectCanOverrideACLForPrivateBackups(t *testing.T) {
	var gotACL string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotACL = r.Header.Get("X-Amz-Acl")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}

	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{
		S3Enabled:         true,
		S3Bucket:          "bucket-name",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "us-east-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
		S3Permission:      "public-read",
	}}

	if err := s.uploadPaperclipObjectWithACL(context.Background(), "backups/dumps/000/000/042/original/archive.zip", path, "application/zip", "private"); err != nil {
		t.Fatal(err)
	}
	if gotACL != "private" {
		t.Fatalf("X-Amz-Acl = %q", gotACL)
	}
}

func TestUploadPaperclipObjectMirrorsToAzureBlobStorage(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody string
	var gotContentType string
	var gotBlobType string
	var gotVersion string
	var gotDate string
	var gotAuthorization string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotBody = string(body)
		gotContentType = r.Header.Get("Content-Type")
		gotBlobType = r.Header.Get("x-ms-blob-type")
		gotVersion = r.Header.Get("x-ms-version")
		gotDate = r.Header.Get("x-ms-date")
		gotAuthorization = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}

	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, []byte("image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{
		AzureEnabled:          true,
		AzureStorageAccount:   "acct",
		AzureStorageAccessKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		AzureContainerName:    "media",
	}}

	if err := s.uploadPaperclipObject(context.Background(), "media_attachments/files/000/000/042/original/photo #1.png", path, "image/png"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotPath != "/media/media_attachments/files/000/000/042/original/photo%20%231.png" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody != "image-bytes" {
		t.Fatalf("body = %q", gotBody)
	}
	if gotContentType != "image/png" || gotBlobType != "BlockBlob" || gotVersion != azureBlobSASVersion || gotDate == "" {
		t.Fatalf("azure headers content=%q blob=%q version=%q date=%q", gotContentType, gotBlobType, gotVersion, gotDate)
	}
	if !strings.HasPrefix(gotAuthorization, "SharedKey acct:") {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
}

func TestDeletePaperclipObjectRemovesAzureBlobStorageObject(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotAuthorization string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotAuthorization = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	s := &Server{cfg: config.Config{
		AzureEnabled:          true,
		AzureStorageAccount:   "acct",
		AzureStorageAccessKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		AzureContainerName:    "media",
	}}

	s.deletePaperclipObject(context.Background(), "media_attachments/files/000/000/042/original/photo #1.png")
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotPath != "/media/media_attachments/files/000/000/042/original/photo%20%231.png" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.HasPrefix(gotAuthorization, "SharedKey acct:") {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
}

func TestDeletePaperclipObjectRetriesOnlyTransientAzureFailures(t *testing.T) {
	oldClient := s3HTTPClient
	oldWait := storageDeleteRetryWait
	t.Cleanup(func() {
		s3HTTPClient = oldClient
		storageDeleteRetryWait = oldWait
	})
	waits := 0
	storageDeleteRetryWait = func(context.Context, int) error {
		waits++
		return nil
	}
	s := &Server{cfg: config.Config{
		AzureEnabled:          true,
		AzureStorageAccount:   "acct",
		AzureStorageAccessKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		AzureContainerName:    "media",
		S3BatchDeleteRetry:    3,
	}}

	t.Run("transient", func(t *testing.T) {
		calls := 0
		s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			status := http.StatusAccepted
			if calls == 1 {
				status = http.StatusServiceUnavailable
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		s.deletePaperclipObject(context.Background(), "media/file.png")
		if calls != 2 || waits != 1 {
			t.Fatalf("transient Azure delete calls=%d waits=%d, want 2/1", calls, waits)
		}
	})

	t.Run("permanent", func(t *testing.T) {
		calls := 0
		waits = 0
		s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		s.deletePaperclipObject(context.Background(), "media/file.png")
		if calls != 1 || waits != 0 {
			t.Fatalf("permanent Azure delete calls=%d waits=%d, want 1/0", calls, waits)
		}
	})
}

func TestUploadPaperclipObjectMirrorsToSwiftObjectStorage(t *testing.T) {
	var authPath string
	var authProjectID string
	var authUser string
	var gotMethod string
	var gotPath string
	var gotBody string
	var gotToken string
	var gotContentType string
	var gotCacheControl string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "keystone.example.test":
			authPath = r.URL.Path
			var payload map[string]any
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			auth := payload["auth"].(map[string]any)
			scope := auth["scope"].(map[string]any)
			project := scope["project"].(map[string]any)
			authProjectID, _ = project["id"].(string)
			identity := auth["identity"].(map[string]any)
			password := identity["password"].(map[string]any)
			user := password["user"].(map[string]any)
			authUser, _ = user["name"].(string)
			return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"X-Subject-Token": []string{"swift-token"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
		case "swift.example.test":
			body, _ := io.ReadAll(r.Body)
			gotMethod = r.Method
			gotPath = r.URL.EscapedPath()
			gotBody = string(body)
			gotToken = r.Header.Get("X-Auth-Token")
			gotContentType = r.Header.Get("Content-Type")
			gotCacheControl = r.Header.Get("Cache-Control")
			return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		default:
			t.Fatalf("unexpected host %s", r.URL.Host)
		}
		return nil, nil
	})}

	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, []byte("image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Config{
		SwiftEnabled:    true,
		SwiftObjectURL:  "https://swift.example.test/v1/AUTH_project/container/",
		SwiftContainer:  "container",
		SwiftUsername:   "swift-user",
		SwiftProjectID:  "project-id",
		SwiftPassword:   "swift-password",
		SwiftAuthURL:    "https://keystone.example.test/v3/",
		SwiftDomainName: "example-domain",
	}}

	if err := s.uploadPaperclipObject(context.Background(), "media_attachments/files/000/000/042/original/photo #1.png", path, "image/png"); err != nil {
		t.Fatal(err)
	}
	if authPath != "/v3/auth/tokens" || authProjectID != "project-id" || authUser != "swift-user" {
		t.Fatalf("swift auth path=%q project=%q user=%q", authPath, authProjectID, authUser)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotPath != "/v1/AUTH_project/container/media_attachments/files/000/000/042/original/photo%20%231.png" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotBody != "image-bytes" || gotToken != "swift-token" || gotContentType != "image/png" {
		t.Fatalf("swift request body=%q token=%q content=%q", gotBody, gotToken, gotContentType)
	}
	if gotCacheControl != "public, max-age=315576000, immutable" {
		t.Fatalf("cache control = %q", gotCacheControl)
	}
}

func TestDeletePaperclipObjectRemovesSwiftObjectStorageObject(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotToken string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "keystone.example.test":
			return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"X-Subject-Token": []string{"swift-token"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
		case "swift.example.test":
			gotMethod = r.Method
			gotPath = r.URL.EscapedPath()
			gotToken = r.Header.Get("X-Auth-Token")
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		default:
			t.Fatalf("unexpected host %s", r.URL.Host)
		}
		return nil, nil
	})}
	s := &Server{cfg: config.Config{
		SwiftEnabled:    true,
		SwiftObjectURL:  "https://swift.example.test/v1/AUTH_project",
		SwiftContainer:  "container",
		SwiftUsername:   "swift-user",
		SwiftTenant:     "tenant-name",
		SwiftPassword:   "swift-password",
		SwiftAuthURL:    "https://keystone.example.test/v3/auth/tokens",
		SwiftDomainName: "default",
	}}

	s.deletePaperclipObject(context.Background(), "media_attachments/files/000/000/042/original/photo #1.png")
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotPath != "/v1/AUTH_project/container/media_attachments/files/000/000/042/original/photo%20%231.png" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotToken != "swift-token" {
		t.Fatalf("token = %q", gotToken)
	}
}

func TestDeletePaperclipObjectRetriesTransientSwiftFailure(t *testing.T) {
	oldClient := s3HTTPClient
	oldWait := storageDeleteRetryWait
	t.Cleanup(func() {
		s3HTTPClient = oldClient
		storageDeleteRetryWait = oldWait
	})
	storageDeleteRetryWait = func(context.Context, int) error { return nil }
	authCalls := 0
	deleteCalls := 0
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "keystone.example.test":
			authCalls++
			return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"X-Subject-Token": []string{"swift-token"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
		case "swift.example.test":
			deleteCalls++
			status := http.StatusNoContent
			if deleteCalls == 1 {
				status = http.StatusTooManyRequests
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		default:
			t.Fatalf("unexpected host %s", r.URL.Host)
		}
		return nil, nil
	})}
	s := &Server{cfg: config.Config{
		SwiftEnabled:       true,
		SwiftObjectURL:     "https://swift.example.test/v1/AUTH_project",
		SwiftContainer:     "container",
		SwiftUsername:      "swift-user",
		SwiftTenant:        "tenant-name",
		SwiftPassword:      "swift-password",
		SwiftAuthURL:       "https://keystone.example.test/v3/auth/tokens",
		SwiftDomainName:    "default",
		S3BatchDeleteRetry: 3,
	}}

	s.deletePaperclipObject(context.Background(), "media/file.png")
	if authCalls != 2 || deleteCalls != 2 {
		t.Fatalf("Swift retry auth=%d delete=%d, want 2/2", authCalls, deleteCalls)
	}
}

func TestPutS3ObjectACLUsesAWSACLSubresource(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotQuery string
	var gotACL string
	var gotAuthorization string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotACL = r.Header.Get("X-Amz-Acl")
		gotAuthorization = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	s := &Server{cfg: config.Config{
		S3Enabled:         true,
		S3Bucket:          "bucket-name",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "ap-northeast-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
	}}

	if err := s.putS3ObjectACL(context.Background(), "media_attachments/files/000/000/042/original/photo.png", "private"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotPath != "/bucket-name/media_attachments/files/000/000/042/original/photo.png" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "acl=" {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotACL != "private" {
		t.Fatalf("X-Amz-Acl = %q", gotACL)
	}
	if !strings.Contains(gotAuthorization, "AWS4-HMAC-SHA256 Credential=access/") || !strings.Contains(gotAuthorization, "x-amz-acl") {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
}

func TestGetS3ObjectSignsAndReadsPaperclipObject(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotAuthorization string
	var gotChecksumMode string
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		gotChecksumMode = r.Header.Get("X-Amz-Checksum-Mode")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("object-bytes")), Request: r}, nil
	})}
	s := &Server{cfg: config.Config{
		S3Enabled:         true,
		S3Bucket:          "bucket-name",
		S3Endpoint:        "https://storage.example.test",
		S3Region:          "us-east-1",
		S3AccessKeyID:     "access",
		S3SecretAccessKey: "secret",
	}}

	body, ok, err := s.getS3Object(context.Background(), "media_attachments/files/000/000/042/original/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(body) != "object-bytes" {
		t.Fatalf("getS3Object body=%q ok=%v", body, ok)
	}
	if gotMethod != http.MethodGet || gotPath != "/bucket-name/media_attachments/files/000/000/042/original/photo.png" {
		t.Fatalf("request method=%q path=%q", gotMethod, gotPath)
	}
	if !strings.Contains(gotAuthorization, "AWS4-HMAC-SHA256 Credential=access/") {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if gotChecksumMode != "" {
		t.Fatalf("X-Amz-Checksum-Mode = %q, want absent by default", gotChecksumMode)
	}
}
