package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRelationshipNotificationOpenAISpamClassifyMatchesRailsChatShape(t *testing.T) {
	oldClient := openAISpamHTTPClient
	defer func() { openAISpamHTTPClient = oldClient }()
	t.Setenv("OPENAI_API_BASE", "https://openai.example/v1")
	t.Setenv("OPENAI_BASE_URL", "https://openai-base.example/v1")
	t.Setenv("OPENAI_SPAM_FILTER_MODEL", "gpt-test")
	t.Setenv("SPAM_FILTER_OPENAI_SYSTEM_MESSAGE", "judge spam")

	var gotURL string
	var gotAuth string
	var payload map[string]any
	openAISpamHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		gotAuth = req.Header.Get("Authorization")
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"TRUE"}}]}`)),
			Request:    req,
		}, nil
	})}

	result, err := relationshipNotificationOpenAISpamClassify("token-value", "<p>spam text</p>")
	if err != nil {
		t.Fatal(err)
	}
	if result != "TRUE" {
		t.Fatalf("result = %q", result)
	}
	if gotURL != "https://api.openai.com/v1/chat/completions" || gotAuth != "Bearer token-value" {
		t.Fatalf("request url/auth = %q / %q", gotURL, gotAuth)
	}
	if payload["model"] != "gpt-test" || payload["temperature"] != 0.7 {
		t.Fatalf("payload = %#v", payload)
	}
	messages := payload["messages"].([]any)
	if messages[0].(map[string]any)["content"] != "judge spam" || messages[1].(map[string]any)["content"] != "<p>spam text</p>" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestRelationshipNotificationSenderFollowersCountTreatsMissingStatAsZero(t *testing.T) {
	src, err := os.ReadFile("relationship_spam_gpt.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "relationshipNotificationSenderFollowersCount", `errors.Is(err, gorm.ErrRecordNotFound)`) {
		t.Fatal("relationshipNotificationSenderFollowersCount must tolerate wrapped gorm.ErrRecordNotFound as zero followers")
	}
	if functionBodyContains(t, src, "relationshipNotificationSenderFollowersCount", `err == gorm.ErrRecordNotFound`) {
		t.Fatal("relationshipNotificationSenderFollowersCount must not compare gorm.ErrRecordNotFound directly")
	}
}

func TestRelationshipNotificationOpenAISpamClientHasTimeoutAndBodyLimit(t *testing.T) {
	if openAISpamHTTPClient == nil {
		t.Fatal("openAISpamHTTPClient is nil")
	}
	if openAISpamHTTPClient.Timeout != openAISpamHTTPTimeout {
		t.Fatalf("openAISpamHTTPClient.Timeout = %s, want %s", openAISpamHTTPClient.Timeout, openAISpamHTTPTimeout)
	}

	oldClient := openAISpamHTTPClient
	defer func() { openAISpamHTTPClient = oldClient }()
	openAISpamHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"TRUE"}}]}`)),
			ContentLength: maxOpenAISpamResponseBodySize + 1,
			Request:       req,
		}, nil
	})}
	if _, err := relationshipNotificationOpenAISpamClassify("token-value", "spam text"); err == nil {
		t.Fatal("expected advertised oversized OpenAI spam response to fail")
	}

	openAISpamHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          io.NopCloser(strings.NewReader(strings.Repeat("x", maxOpenAISpamResponseBodySize+1))),
			ContentLength: -1,
			Request:       req,
		}, nil
	})}
	if _, err := relationshipNotificationOpenAISpamClassify("token-value", "spam text"); err == nil {
		t.Fatal("expected streamed oversized OpenAI spam response to fail")
	}
}

func TestRelationshipNotificationGPTSpamCacheExpiresLikeRails(t *testing.T) {
	gptSpamCacheMu.Lock()
	gptSpamCache = map[int64]gptSpamCacheEntry{}
	gptSpamCacheMu.Unlock()

	now := time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC)
	relationshipNotificationGPTSpamCacheStore(42, "TRUE", now.Add(time.Minute))
	if got, ok := relationshipNotificationGPTSpamCached(42, now.Add(30*time.Second)); !ok || got != "TRUE" {
		t.Fatalf("cached = %q ok=%v", got, ok)
	}
	if got, ok := relationshipNotificationGPTSpamCached(42, now.Add(2*time.Minute)); ok || got != "" {
		t.Fatalf("expired cached = %q ok=%v", got, ok)
	}
}
