package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestStorageSchemaMoveAzureCopiesBeforeDelete(t *testing.T) {
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	sequence := []string{}
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: request}
		switch request.Method {
		case http.MethodPut:
			sequence = append(sequence, "copy")
			if request.URL.EscapedPath() != "/media/cache/media_attachments/files/000/000/042/original/photo%20%231.png" {
				t.Fatalf("Azure destination path = %q", request.URL.EscapedPath())
			}
			source, err := url.Parse(request.Header.Get("x-ms-copy-source"))
			if err != nil {
				t.Fatal(err)
			}
			hasSignature := source.Query().Get("sig") != ""
			if source.EscapedPath() != "/media/media_attachments/files/000/000/042/original/photo%20%231.png" || !hasSignature {
				t.Fatalf("Azure copy source path=%q has_signature=%t", source.EscapedPath(), hasSignature)
			}
			if !strings.HasPrefix(request.Header.Get("Authorization"), "SharedKey acct:") {
				t.Fatalf("Azure copy Authorization = %q", request.Header.Get("Authorization"))
			}
			response.StatusCode = http.StatusAccepted
			response.Header.Set("x-ms-copy-status", "pending")
		case http.MethodHead:
			sequence = append(sequence, "poll")
			response.Header.Set("x-ms-copy-status", "success")
		case http.MethodDelete:
			sequence = append(sequence, "delete")
			if request.URL.EscapedPath() != "/media/media_attachments/files/000/000/042/original/photo%20%231.png" {
				t.Fatalf("Azure source delete path = %q", request.URL.EscapedPath())
			}
			response.StatusCode = http.StatusAccepted
		default:
			t.Fatalf("unexpected Azure method %q", request.Method)
		}
		return response, nil
	})}

	operations := &Operations{server: &Server{cfg: config.Config{
		AzureEnabled:          true,
		AzureStorageAccount:   "acct",
		AzureStorageAccessKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		AzureContainerName:    "media",
	}}}
	if err := operations.moveStorageObject(context.Background(),
		"media_attachments/files/000/000/042/original/photo #1.png",
		"cache/media_attachments/files/000/000/042/original/photo #1.png",
	); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sequence, ","); got != "copy,poll,delete" {
		t.Fatalf("Azure move sequence = %q", got)
	}
}

func TestStorageSchemaMoveAzureMissingSourceIsIdempotent(t *testing.T) {
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	sequence := []string{}
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.Method {
		case http.MethodPut:
			sequence = append(sequence, "copy-missing")
			return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: request}, nil
		case http.MethodDelete:
			sequence = append(sequence, "delete-missing")
			return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Request: request}, nil
		default:
			t.Fatalf("unexpected Azure method %q", request.Method)
		}
		return nil, nil
	})}
	operations := &Operations{server: &Server{cfg: config.Config{
		AzureEnabled:          true,
		AzureStorageAccount:   "acct",
		AzureStorageAccessKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		AzureContainerName:    "media",
	}}}
	if err := operations.moveStorageObject(context.Background(), "old.png", "cache/old.png"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sequence, ","); got != "copy-missing,delete-missing" {
		t.Fatalf("Azure idempotent move sequence = %q", got)
	}
}

func TestStorageSchemaMoveSwiftRetriesCopyBeforeDelete(t *testing.T) {
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	sequence := []string{}
	copyAttempts := 0
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "keystone.example.test":
			return &http.Response{
				StatusCode: http.StatusCreated,
				Header:     http.Header{"X-Subject-Token": []string{"swift-token"}},
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Request:    request,
			}, nil
		case "swift.example.test":
			if request.Header.Get("X-Auth-Token") != "swift-token" {
				t.Fatalf("Swift auth token = %q", request.Header.Get("X-Auth-Token"))
			}
			switch request.Method {
			case "COPY":
				sequence = append(sequence, "copy")
				copyAttempts++
				if request.URL.EscapedPath() != "/v1/AUTH_project/container/media_attachments/files/000/000/042/original/photo%20%231.png" {
					t.Fatalf("Swift source path = %q", request.URL.EscapedPath())
				}
				if got := request.Header.Get("Destination"); got != "/container/cache/media_attachments/files/000/000/042/original/photo%20%231.png" {
					t.Fatalf("Swift Destination = %q", got)
				}
				if copyAttempts == 1 {
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Request: request}, nil
				}
				return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Request: request}, nil
			case http.MethodDelete:
				sequence = append(sequence, "delete")
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: request}, nil
			default:
				t.Fatalf("unexpected Swift method %q", request.Method)
			}
		default:
			t.Fatalf("unexpected host %q", request.URL.Host)
		}
		return nil, nil
	})}

	operations := &Operations{server: &Server{cfg: config.Config{
		SwiftEnabled:   true,
		SwiftObjectURL: "https://swift.example.test/v1/AUTH_project/container",
		SwiftContainer: "container",
		SwiftUsername:  "swift-user",
		SwiftProjectID: "project-id",
		SwiftPassword:  "swift-password",
		SwiftAuthURL:   "https://keystone.example.test/v3",
	}}}
	if err := operations.moveStorageObject(context.Background(),
		"media_attachments/files/000/000/042/original/photo #1.png",
		"cache/media_attachments/files/000/000/042/original/photo #1.png",
	); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sequence, ","); got != "copy,copy,delete" {
		t.Fatalf("Swift retry/move sequence = %q", got)
	}
}

func TestStorageSchemaDryRunDoesNotTouchObjectStorage(t *testing.T) {
	oldClient := s3HTTPClient
	defer func() { s3HTTPClient = oldClient }()
	s3HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("dry-run issued %s %s", request.Method, request.URL)
		return nil, errors.New("unexpected request")
	})}
	operations := &Operations{server: &Server{cfg: config.Config{
		SwiftEnabled:   true,
		SwiftObjectURL: "https://swift.example.test/v1/AUTH_project/container",
		SwiftContainer: "container",
		SwiftUsername:  "swift-user",
		SwiftProjectID: "project-id",
		SwiftPassword:  "swift-password",
		SwiftAuthURL:   "https://keystone.example.test/v3",
	}}}
	if err := operations.upgradeStorageAttachment(context.Background(), []string{"old.png"}, []string{"cache/old.png"}, true); err != nil {
		t.Fatal(err)
	}
}

func TestStorageSchemaErrorRedactsConfiguredCredentials(t *testing.T) {
	operations := &Operations{server: &Server{cfg: config.Config{
		S3AccessKeyID:         "s3-access-id",
		S3SecretAccessKey:     "s3-secret-key",
		S3SessionToken:        "s3-session-token",
		AzureStorageAccessKey: "azure-access-key",
		SwiftPassword:         "swift-password",
		SwiftTempURLKey:       "swift-temp-key",
	}}}
	err := operations.safeStorageSchemaError(errors.New("s3-secret-key azure-access-key swift-password s3-session-token swift-temp-key s3-access-id"))
	for _, secret := range []string{"s3-secret-key", "azure-access-key", "swift-password", "s3-session-token", "swift-temp-key", "s3-access-id"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("storage schema error leaked %q: %s", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("storage schema error was not redacted: %s", err)
	}
}
