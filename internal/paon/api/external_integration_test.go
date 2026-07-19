//go:build external

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func requiredExternalEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required for the external integration suite", name)
	}
	return value
}

func TestExternalMeilisearchCreateUpdateSearchAndDelete(t *testing.T) {
	cfg := config.Config{
		MeiliEnabled:   true,
		MeiliHost:      requiredExternalEnv(t, "PAON_TEST_MEILI_URL"),
		MeiliMasterKey: requiredExternalEnv(t, "PAON_TEST_MEILI_KEY"),
		MeiliPrefix:    fmt.Sprintf("paon_external_%d_", time.Now().UnixNano()),
	}
	server := &Server{cfg: cfg}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := MeiliAvailable(ctx, cfg); err != nil {
		t.Fatalf("MeiliAvailable: %v", err)
	}
	if err := server.meiliCreateIndex(ctx, meiliIndexDefinition{Index: "accounts", PrimaryKey: "id"}); err != nil {
		t.Fatalf("create index: %v", err)
	}
	if err := server.meiliWriteJSON(ctx, http.MethodPost, "accounts", "documents", []map[string]any{{"id": 901, "username": "external-alice"}}); err != nil {
		t.Fatalf("write document: %v", err)
	}
	var ids []int64
	var err error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ids, err = server.searchMeiliIDs(ctx, "accounts", "external-alice", meiliSearchOptions{Limit: 10})
		if err == nil && len(ids) == 1 && ids[0] == 901 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil || len(ids) != 1 || ids[0] != 901 {
		t.Fatalf("search ids=%v err=%v", ids, err)
	}
	if err := server.meiliWriteJSON(ctx, http.MethodDelete, "accounts", "documents/901", nil); err != nil {
		t.Fatalf("delete document: %v", err)
	}
}

func TestExternalS3CompatibleStorageRoundTrip(t *testing.T) {
	cfg := config.Config{
		S3Enabled:  true,
		S3Bucket:   "paon-integration",
		S3Endpoint: requiredExternalEnv(t, "PAON_TEST_S3_URL"),
		S3Region:   "us-east-1",
		// Rails sets force_path_style when S3_OVERRIDE_PATH_STYLE is not true.
		S3OverridePathStyle: false,
		S3AccessKeyID:       "paon-access",
		S3SecretAccessKey:   "paon-secret-key",
		S3SignatureVersion:  "v4",
		S3Permission:        "private",
	}
	configureS3HTTPClient(cfg)
	server := &Server{cfg: cfg}
	key := fmt.Sprintf("external/%d/message.txt", time.Now().UnixNano())
	want := []byte("paon external storage round trip")
	if err := server.putS3Object(t.Context(), key, want, "text/plain; charset=utf-8"); err != nil {
		t.Fatalf("put S3 object: %v", err)
	}
	got, found, err := server.getS3Object(t.Context(), key)
	if err != nil || !found || string(got) != string(want) {
		t.Fatalf("get S3 object found=%v body=%q err=%v", found, got, err)
	}
	if err := server.deleteS3Object(t.Context(), key); err != nil {
		t.Fatalf("delete S3 object: %v", err)
	}
	_, found, err = server.getS3Object(t.Context(), key)
	if err != nil || found {
		t.Fatalf("deleted S3 object found=%v err=%v", found, err)
	}
}

func TestExternalSMTPReceivesUTF8MultipartMessage(t *testing.T) {
	host := requiredExternalEnv(t, "PAON_TEST_SMTP_HOST")
	port := requiredExternalEnv(t, "PAON_TEST_SMTP_PORT")
	mailpitURL := requiredExternalEnv(t, "PAON_TEST_MAILPIT_URL")
	cfg := config.Config{
		LocalDomain:        "example.test",
		SMTPServer:         host,
		SMTPPort:           port,
		SMTPFrom:           `"Paon 通知" <notifications@example.test>`,
		SMTPDeliveryMethod: "smtp",
	}
	message := mailMessage{To: "alice@example.test", Subject: "互換性テスト", Body: "plain body", HTMLBody: "<p>html body</p>"}
	if err := sendMail(cfg, message); err != nil {
		t.Fatalf("sendMail: %v", err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, strings.TrimRight(mailpitURL, "/")+"/api/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("query Mailpit: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Mailpit status=%d body=%s", response.StatusCode, body)
	}
	var payload struct {
		Messages []struct {
			Subject string `json:"Subject"`
			To      []struct {
				Address string `json:"Address"`
			} `json:"To"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Mailpit response: %v body=%s", err, body)
	}
	found := false
	for _, received := range payload.Messages {
		if received.Subject == message.Subject && len(received.To) > 0 && received.To[0].Address == message.To {
			found = true
		}
	}
	if !found {
		t.Fatalf("Mailpit did not receive subject=%q recipient=%q: %s", message.Subject, message.To, body)
	}
}
