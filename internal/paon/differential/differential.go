package differential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Manifest struct {
	Cases []Case `json:"cases"`
}

type Case struct {
	ID               string            `json:"id"`
	Contract         string            `json:"contract"`
	Method           string            `json:"method"`
	Path             string            `json:"path"`
	Headers          map[string]string `json:"headers,omitempty"`
	Body             string            `json:"body,omitempty"`
	CompareHeaders   []string          `json:"compare_headers,omitempty"`
	VolatileHeaders  []string          `json:"volatile_headers,omitempty"`
	JSONReplacements map[string]any    `json:"json_replacements,omitempty"`
}

type Result struct {
	CaseID string `json:"case_id"`
	Error  string `json:"error,omitempty"`
}

type Runner struct {
	RailsBaseURL string
	GoBaseURL    string
	Client       *http.Client
}

var defaultComparedHeaders = []string{
	"Cache-Control", "Content-Disposition", "Content-Language", "Content-Type",
	"ETag", "Link", "Location", "Set-Cookie", "Vary", "WWW-Authenticate",
	"X-Frame-Options", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset",
}

func Load(r io.Reader) (Manifest, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	seen := make(map[string]struct{}, len(manifest.Cases))
	for index := range manifest.Cases {
		item := &manifest.Cases[index]
		item.ID = strings.TrimSpace(item.ID)
		item.Method = strings.ToUpper(strings.TrimSpace(item.Method))
		if item.ID == "" || item.Contract == "" || item.Method == "" || !strings.HasPrefix(item.Path, "/") {
			return Manifest{}, fmt.Errorf("case %d must define id, contract, method, and absolute path", index)
		}
		if _, exists := seen[item.ID]; exists {
			return Manifest{}, fmt.Errorf("duplicate case id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	return manifest, nil
}

func (runner Runner) Run(ctx context.Context, manifest Manifest) []Result {
	results := make([]Result, 0, len(manifest.Cases))
	for _, item := range manifest.Cases {
		result := Result{CaseID: item.ID}
		if err := runner.runCase(ctx, item); err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	return results
}

func (runner Runner) runCase(ctx context.Context, item Case) error {
	client := runner.Client
	if client == nil {
		client = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	rails, err := execute(ctx, client, runner.RailsBaseURL, item)
	if err != nil {
		return fmt.Errorf("Rails request: %w", err)
	}
	goResponse, err := execute(ctx, client, runner.GoBaseURL, item)
	if err != nil {
		return fmt.Errorf("Go request: %w", err)
	}
	if rails.StatusCode != goResponse.StatusCode {
		return fmt.Errorf("status: Rails=%d Go=%d", rails.StatusCode, goResponse.StatusCode)
	}
	headers := item.CompareHeaders
	if len(headers) == 0 {
		headers = defaultComparedHeaders
	}
	volatile := normalizedSet(item.VolatileHeaders)
	for _, name := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if _, ignored := volatile[canonical]; ignored {
			continue
		}
		railsValues := normalizeHeaderValues(canonical, rails.Header.Values(canonical), runner.RailsBaseURL, runner.GoBaseURL)
		goValues := normalizeHeaderValues(canonical, goResponse.Header.Values(canonical), runner.RailsBaseURL, runner.GoBaseURL)
		if !equalStrings(railsValues, goValues) {
			return fmt.Errorf("header %s: Rails=%q Go=%q", canonical, railsValues, goValues)
		}
	}
	return compareBody(rails, goResponse, item.JSONReplacements, runner.RailsBaseURL, runner.GoBaseURL)
}

type capturedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func execute(ctx context.Context, client *http.Client, base string, item Case) (capturedResponse, error) {
	baseURL, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return capturedResponse{}, fmt.Errorf("invalid base URL %q", base)
	}
	relative, err := url.Parse(item.Path)
	if err != nil {
		return capturedResponse{}, err
	}
	requestURL := baseURL.ResolveReference(relative)
	request, err := http.NewRequestWithContext(ctx, item.Method, requestURL.String(), strings.NewReader(item.Body))
	if err != nil {
		return capturedResponse{}, err
	}
	for name, value := range item.Headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return capturedResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return capturedResponse{}, err
	}
	return capturedResponse{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: body}, nil
}

func compareBody(rails, goResponse capturedResponse, replacements map[string]any, railsBase, goBase string) error {
	contentType := rails.Header.Get("Content-Type")
	if strings.Contains(contentType, "json") {
		var railsJSON any
		var goJSON any
		if err := json.Unmarshal(rails.Body, &railsJSON); err != nil {
			return fmt.Errorf("Rails JSON body: %w", err)
		}
		if err := json.Unmarshal(goResponse.Body, &goJSON); err != nil {
			return fmt.Errorf("Go JSON body: %w", err)
		}
		for pointer, replacement := range replacements {
			if err := replaceJSONPointer(&railsJSON, pointer, replacement); err != nil {
				return fmt.Errorf("Rails JSON replacement %q: %w", pointer, err)
			}
			if err := replaceJSONPointer(&goJSON, pointer, replacement); err != nil {
				return fmt.Errorf("Go JSON replacement %q: %w", pointer, err)
			}
		}
		railsBody, _ := json.Marshal(railsJSON)
		goBody, _ := json.Marshal(goJSON)
		if !bytes.Equal(railsBody, goBody) {
			return fmt.Errorf("JSON body differs: Rails=%s Go=%s", railsBody, goBody)
		}
		return nil
	}
	railsBody := normalizeBaseURLs(string(rails.Body), railsBase, goBase)
	goBody := normalizeBaseURLs(string(goResponse.Body), railsBase, goBase)
	if railsBody != goBody {
		return errors.New("response body differs")
	}
	return nil
}

func replaceJSONPointer(root *any, pointer string, replacement any) error {
	if pointer == "" {
		*root = replacement
		return nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return errors.New("pointer must start with /")
	}
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := *root
	for index, encoded := range parts {
		part := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		last := index == len(parts)-1
		switch value := current.(type) {
		case map[string]any:
			next, exists := value[part]
			if !exists {
				return fmt.Errorf("object key %q is absent", part)
			}
			if last {
				value[part] = replacement
				return nil
			}
			current = next
		case []any:
			var arrayIndex int
			if _, err := fmt.Sscanf(part, "%d", &arrayIndex); err != nil || arrayIndex < 0 || arrayIndex >= len(value) {
				return fmt.Errorf("invalid array index %q", part)
			}
			if last {
				value[arrayIndex] = replacement
				return nil
			}
			current = value[arrayIndex]
		default:
			return fmt.Errorf("cannot traverse %q", part)
		}
	}
	return nil
}

func normalizeHeaderValues(name string, values []string, railsBase, goBase string) []string {
	out := append([]string(nil), values...)
	for index := range out {
		out[index] = normalizeBaseURLs(out[index], railsBase, goBase)
		if name == "Set-Cookie" {
			out[index] = strings.ReplaceAll(out[index], "; Domain=rails", "; Domain=go")
		}
	}
	sort.Strings(out)
	return out
}

func normalizeBaseURLs(value, railsBase, goBase string) string {
	value = strings.ReplaceAll(value, strings.TrimRight(railsBase, "/"), "{{BASE_URL}}")
	return strings.ReplaceAll(value, strings.TrimRight(goBase, "/"), "{{BASE_URL}}")
}

func normalizedSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[http.CanonicalHeaderKey(value)] = struct{}{}
	}
	return out
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
