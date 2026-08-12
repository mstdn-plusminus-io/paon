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
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Manifest struct {
	Cases []Case `json:"cases"`
}

type Case struct {
	ID                 string            `json:"id"`
	Contract           string            `json:"contract"`
	Method             string            `json:"method"`
	Path               string            `json:"path"`
	Headers            map[string]string `json:"headers,omitempty"`
	HeadersFromEnv     map[string]string `json:"headers_from_env,omitempty"`
	Body               string            `json:"body,omitempty"`
	CompareHeaders     []string          `json:"compare_headers,omitempty"`
	VolatileHeaders    []string          `json:"volatile_headers,omitempty"`
	JSONReplacements   map[string]any    `json:"json_replacements,omitempty"`
	GoOnlyJSONPointers []string          `json:"go_only_json_pointers,omitempty"`
	CompareBody        *bool             `json:"compare_body,omitempty"`
}

type Result struct {
	CaseID string `json:"case_id"`
	Error  string `json:"error,omitempty"`
}

type Runner struct {
	RailsBaseURL string
	GoBaseURL    string
	Client       *http.Client
	Env          func(string) (string, bool)
}

var defaultComparedHeaders = []string{
	"Access-Control-Allow-Credentials", "Access-Control-Allow-Headers",
	"Access-Control-Allow-Methods", "Access-Control-Allow-Origin",
	"Access-Control-Expose-Headers", "Access-Control-Max-Age",
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
	materialized, err := materializeCaseEnvironment(item, runner.Env)
	if err != nil {
		return err
	}
	item = materialized
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
	if item.CompareBody != nil && !*item.CompareBody {
		return nil
	}
	return compareBody(rails, goResponse, item.JSONReplacements, item.GoOnlyJSONPointers, runner.RailsBaseURL, runner.GoBaseURL)
}

var differentialEnvironmentReference = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

func materializeCaseEnvironment(item Case, lookup func(string) (string, bool)) (Case, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	resolved := item
	resolved.Headers = make(map[string]string, len(item.Headers)+len(item.HeadersFromEnv))
	for name, raw := range item.Headers {
		value, err := expandRequiredEnvironment(raw, lookup)
		if err != nil {
			return Case{}, fmt.Errorf("case %s header %s: %w", item.ID, name, err)
		}
		resolved.Headers[name] = value
	}
	for name, variable := range item.HeadersFromEnv {
		variable = strings.TrimSpace(variable)
		if !differentialEnvironmentName(variable) {
			return Case{}, fmt.Errorf("case %s header %s has invalid environment variable name", item.ID, name)
		}
		value, exists := lookup(variable)
		if !exists || strings.TrimSpace(value) == "" {
			return Case{}, fmt.Errorf("case %s requires environment variable %s", item.ID, variable)
		}
		resolved.Headers[name] = value
	}
	var err error
	resolved.Path, err = expandRequiredEnvironment(item.Path, lookup)
	if err != nil {
		return Case{}, fmt.Errorf("case %s path: %w", item.ID, err)
	}
	resolved.Body, err = expandRequiredEnvironment(item.Body, lookup)
	if err != nil {
		return Case{}, fmt.Errorf("case %s body: %w", item.ID, err)
	}
	return resolved, nil
}

func expandRequiredEnvironment(raw string, lookup func(string) (string, bool)) (string, error) {
	var expansionErr error
	resolved := differentialEnvironmentReference.ReplaceAllStringFunc(raw, func(reference string) string {
		name := differentialEnvironmentReference.FindStringSubmatch(reference)[1]
		value, exists := lookup(name)
		if !exists || value == "" {
			expansionErr = fmt.Errorf("environment variable %s is required", name)
			return reference
		}
		return value
	})
	return resolved, expansionErr
}

func differentialEnvironmentName(value string) bool {
	return differentialEnvironmentReference.MatchString("${"+value+"}") && differentialEnvironmentReference.FindString("${"+value+"}") == "${"+value+"}"
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

func compareBody(rails, goResponse capturedResponse, replacements map[string]any, goOnlyPointers []string, railsBase, goBase string) error {
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
		for _, pointer := range goOnlyPointers {
			railsMatches, err := countJSONPointerPatternMatches(railsJSON, pointer)
			if err != nil {
				return fmt.Errorf("Rails Go-only JSON pointer %q: %w", pointer, err)
			}
			if railsMatches != 0 {
				return fmt.Errorf("Go-only JSON pointer %q unexpectedly exists in Rails response", pointer)
			}
			removed, err := deleteJSONPointerPattern(&goJSON, pointer)
			if err != nil {
				return fmt.Errorf("Go-only JSON pointer %q: %w", pointer, err)
			}
			if removed == 0 {
				return fmt.Errorf("Go-only JSON pointer %q is absent from Go response", pointer)
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

func countJSONPointerPatternMatches(root any, pointer string) (int, error) {
	parts, err := jsonPointerPatternParts(pointer)
	if err != nil {
		return 0, err
	}
	return countJSONPointerPatternParts(root, parts)
}

func countJSONPointerPatternParts(current any, parts []string) (int, error) {
	if len(parts) == 0 {
		return 1, nil
	}
	part := parts[0]
	rest := parts[1:]
	switch value := current.(type) {
	case map[string]any:
		if part == "*" {
			return 0, errors.New("wildcard object keys are not supported")
		}
		next, exists := value[part]
		if !exists {
			return 0, nil
		}
		return countJSONPointerPatternParts(next, rest)
	case []any:
		if part == "*" {
			matches := 0
			for _, next := range value {
				count, err := countJSONPointerPatternParts(next, rest)
				if err != nil {
					return 0, err
				}
				matches += count
			}
			return matches, nil
		}
		arrayIndex, err := strconv.Atoi(part)
		if err != nil || arrayIndex < 0 || arrayIndex >= len(value) {
			return 0, nil
		}
		return countJSONPointerPatternParts(value[arrayIndex], rest)
	default:
		return 0, nil
	}
}

func deleteJSONPointerPattern(root *any, pointer string) (int, error) {
	parts, err := jsonPointerPatternParts(pointer)
	if err != nil {
		return 0, err
	}
	if len(parts) == 0 {
		return 0, errors.New("root omission is not supported")
	}
	return deleteJSONPointerPatternParts(*root, parts)
}

func deleteJSONPointerPatternParts(current any, parts []string) (int, error) {
	part := parts[0]
	if len(parts) == 1 {
		object, ok := current.(map[string]any)
		if !ok {
			return 0, errors.New("terminal omission must name an object field")
		}
		if part == "*" {
			return 0, errors.New("wildcard object keys are not supported")
		}
		if _, exists := object[part]; !exists {
			return 0, nil
		}
		delete(object, part)
		return 1, nil
	}

	rest := parts[1:]
	switch value := current.(type) {
	case map[string]any:
		if part == "*" {
			return 0, errors.New("wildcard object keys are not supported")
		}
		next, exists := value[part]
		if !exists {
			return 0, nil
		}
		return deleteJSONPointerPatternParts(next, rest)
	case []any:
		if part == "*" {
			removed := 0
			for _, next := range value {
				count, err := deleteJSONPointerPatternParts(next, rest)
				if err != nil {
					return 0, err
				}
				removed += count
			}
			return removed, nil
		}
		arrayIndex, err := strconv.Atoi(part)
		if err != nil || arrayIndex < 0 || arrayIndex >= len(value) {
			return 0, nil
		}
		return deleteJSONPointerPatternParts(value[arrayIndex], rest)
	default:
		return 0, nil
	}
}

func jsonPointerPatternParts(pointer string) ([]string, error) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("pointer must start with /")
	}
	encodedParts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	parts := make([]string, 0, len(encodedParts))
	for _, encoded := range encodedParts {
		parts = append(parts, strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~"))
	}
	return parts, nil
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
