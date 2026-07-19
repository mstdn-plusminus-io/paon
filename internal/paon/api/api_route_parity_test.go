package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestDisabledOAuthApplicationRoutesReturnRailsForbidden(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/oauth/applications"},
		{method: http.MethodGet, path: "/oauth/applications.json"},
		{method: http.MethodPost, path: "/oauth/applications"},
		{method: http.MethodPost, path: "/oauth/applications.json"},
		{method: http.MethodGet, path: "/oauth/applications/new"},
		{method: http.MethodGet, path: "/oauth/applications/new.json"},
		{method: http.MethodGet, path: "/oauth/applications/42"},
		{method: http.MethodGet, path: "/oauth/applications/42.json"},
		{method: http.MethodPatch, path: "/oauth/applications/42"},
		{method: http.MethodPatch, path: "/oauth/applications/42.json"},
		{method: http.MethodPut, path: "/oauth/applications/42"},
		{method: http.MethodPut, path: "/oauth/applications/42.json"},
		{method: http.MethodDelete, path: "/oauth/applications/42"},
		{method: http.MethodDelete, path: "/oauth/applications/42.json"},
		{method: http.MethodGet, path: "/oauth/applications/42/edit"},
		{method: http.MethodGet, path: "/oauth/applications/42/edit.json"},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if strings.TrimSpace(rec.Body.String()) != "" {
				t.Fatalf("disabled OAuth route should match Rails head 403 with empty body, got %q", rec.Body.String())
			}
		})
	}
}

func TestFrontendStaticAPIPathsHaveGoRoutes(t *testing.T) {
	serverSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := registeredEchoRoutes(string(serverSrc))
	calls, err := frontendStaticAPICalls("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("frontend static API path scan found no calls")
	}
	for _, call := range calls {
		if !goRoutesContainSpecCall(routes, call) {
			t.Fatalf("frontend API call has no Go route: %s %s (%s)", call.method, call.path, call.file)
		}
	}
}

func TestFrontendStaticAPIScanExpandsComposeRequestMethods(t *testing.T) {
	calls, err := frontendStaticAPICalls("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []railsSpecAPICall{
		{method: "POST", path: "/api/v1/statuses"},
		{method: "PUT", path: "/api/v1/statuses/dynamic"},
	} {
		if !frontendCallsContain(calls, want) {
			t.Fatalf("frontend static API scan missing %s %s", want.method, want.path)
		}
	}
}

func TestFrontendStaticAPIScanCoversExistingClientCallShapes(t *testing.T) {
	calls, err := frontendStaticAPICalls("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []railsSpecAPICall{
		{method: "GET", path: "/api/v1/trends/tags"},
		{method: "GET", path: "/api/v1/accounts/relationships"},
		{method: "POST", path: "/api/v1/markers"},
		{method: "POST", path: "/api/v1/accounts/dynamic/note"},
		{method: "GET", path: "/api/v1/notifications/dynamic"},
		{method: "POST", path: "/api/v1/statuses/dynamic/reblog"},
		{method: "POST", path: "/api/v1/statuses/dynamic/favourite"},
		{method: "PUT", path: "/api/web/settings"},
		{method: "POST", path: "/api/web/push_subscriptions"},
		{method: "PUT", path: "/api/web/push_subscriptions/dynamic"},
		{method: "GET", path: "/api/web/embeds/dynamic"},
		{method: "POST", path: "/api/v1/admin/measures"},
		{method: "POST", path: "/api/v1/admin/dimensions"},
		{method: "POST", path: "/api/v1/admin/retention"},
		{method: "GET", path: "/api/v1/admin/trends/tags"},
		{method: "PUT", path: "/api/v1/admin/reports/dynamic"},
		{method: "GET", path: "/api/v1/peers/search"},
		{method: "GET", path: "/api/v1/instance/privacy_policy"},
		{method: "GET", path: "/api/v1/accounts/lookup"},
		{method: "GET", path: "/api/v1/emails/check_confirmation"},
	} {
		if !frontendCallsContain(calls, want) {
			t.Fatalf("frontend static API scan missing %s %s", want.method, want.path)
		}
	}
}

func TestFrontendStaticAPIScanCoversAxiosRequestObjectShapes(t *testing.T) {
	root := t.TempDir()
	frontendRoot := filepath.Join(root, "app", "javascript", "mastodon")
	if err := os.MkdirAll(frontendRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `
api().request({ url: '/api/v1/statuses/123', method: 'DELETE' });
api().request({ method: 'POST', url: '/api/v1/statuses/123/favourite' });
`
	if err := os.WriteFile(filepath.Join(frontendRoot, "request_shapes.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, err := frontendStaticAPICalls(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []railsSpecAPICall{
		{method: "DELETE", path: "/api/v1/statuses/123"},
		{method: "POST", path: "/api/v1/statuses/123/favourite"},
	} {
		if !frontendCallsContain(calls, want) {
			t.Fatalf("frontend static API scan missing request object call %s %s", want.method, want.path)
		}
	}
}

func TestFrontendStaticAPIScanCoversWebpackPackEntrypoints(t *testing.T) {
	root := t.TempDir()
	packsRoot := filepath.Join(root, "app", "javascript", "packs")
	if err := os.MkdirAll(packsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `
axios.get('/api/v1/emails/check_confirmation');
api().post('/api/v1/statuses');
`
	if err := os.WriteFile(filepath.Join(packsRoot, "sign_up.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, err := frontendStaticAPICalls(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []railsSpecAPICall{
		{method: "GET", path: "/api/v1/emails/check_confirmation"},
		{method: "POST", path: "/api/v1/statuses"},
	} {
		if !frontendCallsContain(calls, want) {
			t.Fatalf("frontend static API scan missing pack entrypoint call %s %s", want.method, want.path)
		}
	}
}

func TestFrontendStaticServerLinksHaveGoRoutes(t *testing.T) {
	serverSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := registeredEchoRoutes(string(serverSrc))
	calls, err := frontendStaticServerCalls("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("frontend static server link scan found no calls")
	}
	for _, call := range calls {
		if !goRoutesContainSpecCall(routes, call) {
			t.Fatalf("frontend server link has no Go route: %s %s (%s)", call.method, call.path, call.file)
		}
	}
}

func TestFrontendStaticServerLinkScanCoversWebpackPackEntrypoints(t *testing.T) {
	root := t.TempDir()
	packsRoot := filepath.Join(root, "app", "javascript", "packs")
	if err := os.MkdirAll(packsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `
axios.get('/settings/security_keys/options');
callback('/settings/security_keys', params);
`
	if err := os.WriteFile(filepath.Join(packsRoot, "two_factor_authentication.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, err := frontendStaticServerCalls(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []railsSpecAPICall{
		{method: "GET", path: "/settings/security_keys/options"},
		{method: "POST", path: "/settings/security_keys"},
	} {
		if !frontendCallsContain(calls, want) {
			t.Fatalf("frontend static server scan missing pack entrypoint call %s %s", want.method, want.path)
		}
	}
}

func TestRouteParityTreatsHeadAsRailsCompatibleGet(t *testing.T) {
	routes := registeredEchoRoutes(`func (s *Server) routes() {
		e.GET("/api/v1/instance", s.instanceV1)
	}`)
	call := railsSpecAPICall{method: "HEAD", path: "/api/v1/instance"}
	if !goRoutesContainSpecCall(routes, call) {
		t.Fatal("HEAD requests should match GET routes because headMethodMiddleware rewrites HEAD before routing")
	}
}

func frontendCallsContain(calls []railsSpecAPICall, want railsSpecAPICall) bool {
	for _, call := range calls {
		if call.method == want.method && call.path == want.path {
			return true
		}
	}
	return false
}

type registeredRoute struct {
	method  string
	rawPath string
	match   *regexp.Regexp
}

type railsSpecAPICall struct {
	file    string
	method  string
	rawPath string
	path    string
}

func registeredEchoRoutes(src string) []registeredRoute {
	pattern := regexp.MustCompile(`e\.(GET|POST|PUT|PATCH|DELETE|HEAD)\("([^"]+)"`)
	matches := pattern.FindAllStringSubmatch(src, -1)
	routes := make([]registeredRoute, 0, len(matches))
	for _, match := range matches {
		route := registeredRoute{method: match[1], rawPath: match[2], match: echoRouteRegexp(match[2])}
		routes = append(routes, route)
		if route.method == "GET" {
			routes = append(routes, registeredRoute{method: "HEAD", rawPath: route.rawPath, match: route.match})
		}
	}
	webAppPattern := regexp.MustCompile(`(?ms)s\.registerWebAppRoutes\((.*?)\)`)
	routeLiteralPattern := regexp.MustCompile(`"([^"]+)"`)
	for _, block := range webAppPattern.FindAllStringSubmatch(src, -1) {
		for _, match := range routeLiteralPattern.FindAllStringSubmatch(block[1], -1) {
			route := match[1]
			routes = append(routes, registeredRoute{method: "GET", rawPath: route, match: echoRouteRegexp(route)})
			routes = append(routes, registeredRoute{method: "HEAD", rawPath: route, match: echoRouteRegexp(route)})
			if webAppRouteAcceptsOptionalFormat(route) {
				routes = append(routes, registeredRoute{method: "GET", rawPath: route + ".:format", match: echoRouteRegexp(route + ".:format")})
				routes = append(routes, registeredRoute{method: "HEAD", rawPath: route + ".:format", match: echoRouteRegexp(route + ".:format")})
			}
		}
	}
	return routes
}

func echoRouteRegexp(route string) *regexp.Regexp {
	parts := strings.Split(route, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch {
		case part == "*":
			out = append(out, `.*`)
		case strings.HasPrefix(part, ":"):
			out = append(out, `[^/]+`)
		case strings.Contains(part, ".:"):
			literal, _, _ := strings.Cut(part, ".:")
			out = append(out, regexp.QuoteMeta(literal)+`\.[^/]+`)
		default:
			out = append(out, strings.ReplaceAll(regexp.QuoteMeta(part), `\*`, `.*`))
		}
	}
	return regexp.MustCompile(`^` + strings.Join(out, `/`) + `$`)
}

func railsRequestSpecAPICalls(root string) ([]railsSpecAPICall, error) {
	specRoot := filepath.Join(root, "spec", "requests", "api")
	callPattern := regexp.MustCompile(`\b(get|post|put|patch|delete|head)\s+['"]([^'"]+)['"]`)
	interpolationPattern := regexp.MustCompile(`#\{[^}]+\}`)
	calls := []railsSpecAPICall{}
	seen := map[string]struct{}{}
	err := filepath.WalkDir(specRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_spec.rb") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range callPattern.FindAllStringSubmatch(string(raw), -1) {
			rawPath := match[2]
			if !strings.HasPrefix(rawPath, "/api/") {
				continue
			}
			call := railsSpecAPICall{
				file:    filepath.ToSlash(path),
				method:  strings.ToUpper(match[1]),
				rawPath: rawPath,
				path:    interpolationPattern.ReplaceAllString(rawPath, "dynamic"),
			}
			key := call.method + " " + call.path + " " + call.file
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			calls = append(calls, call)
		}
		return nil
	})
	return calls, err
}

func railsRequestSpecCalls(root string) ([]railsSpecAPICall, error) {
	specRoot := filepath.Join(root, "spec", "requests")
	callPattern := regexp.MustCompile(`\b(get|post|put|patch|delete|head)\s+['"]([^'"]+)['"]`)
	interpolationPattern := regexp.MustCompile(`#\{[^}]+\}`)
	calls := []railsSpecAPICall{}
	seen := map[string]struct{}{}
	err := filepath.WalkDir(specRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_spec.rb") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range callPattern.FindAllStringSubmatch(string(raw), -1) {
			rawPath := match[2]
			if !strings.HasPrefix(rawPath, "/") {
				continue
			}
			call := railsSpecAPICall{
				file:    filepath.ToSlash(path),
				method:  strings.ToUpper(match[1]),
				rawPath: rawPath,
				path:    railsSpecPathPattern(interpolationPattern.ReplaceAllString(rawPath, "dynamic")),
			}
			key := call.method + " " + call.path + " " + call.file
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			calls = append(calls, call)
		}
		return nil
	})
	return calls, err
}

func frontendStaticServerCalls(root string) ([]railsSpecAPICall, error) {
	frontendRoots := []string{
		filepath.Join(root, "app", "javascript", "mastodon"),
		filepath.Join(root, "app", "javascript", "packs"),
	}
	linkPattern := regexp.MustCompile(`(?:href|action)=['"](/[^'"]+)['"]`)
	jsxLinkPattern := regexp.MustCompile(`(?:href|action)=\{['"](/[^'"]+)['"]\}`)
	formActionPattern := regexp.MustCompile(`form\.action\s*=\s*['"](/[^'"]+)['"]`)
	windowOpenPattern := regexp.MustCompile(`window\.open\(\s*['"](/[^'"]+)['"]`)
	axiosServerMethodPattern := regexp.MustCompile("\\.(get|post|put|patch|delete)(?:<[^>]+>)?\\(\\s*['\"](/[^'\"]+)['\"]")
	callbackPostPattern := regexp.MustCompile(`callback\(\s*['"](/[^'"]+)['"]`)
	interpolationPattern := regexp.MustCompile(`\$\{[^}]+\}`)
	calls := []railsSpecAPICall{}
	seen := map[string]struct{}{}
	for _, frontendRoot := range frontendRoots {
		err := filepath.WalkDir(frontendRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !frontendRouteSourceFile(path) {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(raw)
			for _, pattern := range []*regexp.Regexp{linkPattern, jsxLinkPattern, windowOpenPattern} {
				for _, match := range pattern.FindAllStringSubmatch(src, -1) {
					appendFrontendServerCall(&calls, seen, path, "GET", match[1], interpolationPattern)
				}
			}
			for _, match := range formActionPattern.FindAllStringSubmatch(src, -1) {
				method := "POST"
				if match[1] == "/auth/sign_out" {
					method = "DELETE"
				}
				appendFrontendServerCall(&calls, seen, path, method, match[1], interpolationPattern)
			}
			for _, match := range axiosServerMethodPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendServerCall(&calls, seen, path, strings.ToUpper(match[1]), match[2], interpolationPattern)
			}
			for _, match := range callbackPostPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendServerCall(&calls, seen, path, "POST", match[1], interpolationPattern)
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return calls, err
		}
	}
	return calls, nil
}

func appendFrontendServerCall(calls *[]railsSpecAPICall, seen map[string]struct{}, file string, method string, rawPath string, interpolationPattern *regexp.Regexp) {
	path := interpolationPattern.ReplaceAllString(rawPath, "dynamic")
	if beforeQuery, _, ok := strings.Cut(path, "?"); ok {
		path = beforeQuery
	}
	path = strings.TrimRight(path, "/")
	if !frontendServerPath(path) {
		return
	}
	if method == "GET" && path == "/auth/sign_out" {
		return
	}
	call := railsSpecAPICall{
		file:    filepath.ToSlash(file),
		method:  method,
		rawPath: rawPath,
		path:    path,
	}
	key := call.method + " " + call.path + " " + call.file
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*calls = append(*calls, call)
}

func frontendServerPath(path string) bool {
	switch {
	case path == "/terms" || path == "/invites":
		return true
	case strings.HasPrefix(path, "/settings/"):
		return true
	case strings.HasPrefix(path, "/auth/"):
		return true
	case strings.HasPrefix(path, "/admin/"):
		return true
	default:
		return false
	}
}

func frontendStaticAPICalls(root string) ([]railsSpecAPICall, error) {
	frontendRoots := []string{
		filepath.Join(root, "app", "javascript", "mastodon"),
		filepath.Join(root, "app", "javascript", "packs"),
	}
	axiosMethodPattern := regexp.MustCompile("\\.(get|post|put|patch|delete)(?:<[^>]+>)?\\(\\s*['\"](/api/[^'\"]+)['\"]")
	axiosTemplateMethodPattern := regexp.MustCompile("\\.(get|post|put|patch|delete)(?:<[^>]+>)?\\(\\s*`([^`]+)`")
	fetchOptionsPattern := regexp.MustCompile("(?s)fetch\\(\\s*['\"](/api/[^'\"]+)['\"]\\s*,\\s*\\{.*?method:\\s*['\"]([A-Za-z]+)['\"]")
	fetchFromAPIQuotedPattern := regexp.MustCompile("fetchFromApi\\(\\s*['\"](/api/[^'\"]+)['\"]\\s*,\\s*['\"]([A-Za-z]+)['\"]")
	fetchFromAPITemplatePattern := regexp.MustCompile("fetchFromApi\\(\\s*`([^`]+)`\\s*,\\s*['\"]([A-Za-z]+)['\"]")
	sendBeaconPattern := regexp.MustCompile("sendBeacon\\(\\s*['\"](/api/[^'\"]+)['\"]")
	xhrOpenPattern := regexp.MustCompile("\\.open\\(\\s*['\"](GET|POST|PUT|PATCH|DELETE)['\"]\\s*,\\s*['\"](/api/[^'\"]+)['\"]")
	requestQuotedPattern := regexp.MustCompile("\\.request\\(\\s*\\{[^}]*url:\\s*['\"](/api/[^'\"]+)['\"][^}]*method:\\s*['\"]([A-Za-z]+)['\"]")
	requestQuotedMethodFirstPattern := regexp.MustCompile("\\.request\\(\\s*\\{[^}]*method:\\s*['\"]([A-Za-z]+)['\"][^}]*url:\\s*['\"](/api/[^'\"]+)['\"]")
	requestTernaryPattern := regexp.MustCompile("(?s)\\.request\\(\\s*\\{.*?url:\\s*[^?]+\\?\\s*['\"](/api/[^'\"]+)['\"]\\s*:\\s*`([^`]+)`\\s*,\\s*method:\\s*[^?]+\\?\\s*['\"]([A-Za-z]+)['\"]\\s*:\\s*['\"]([A-Za-z]+)['\"]")
	quotedPattern := regexp.MustCompile(`['"](/api/[^'"]+)['"]`)
	templatePattern := regexp.MustCompile("`([^`]+)`")
	interpolationPattern := regexp.MustCompile(`\$\{[^}]+\}`)
	calls := []railsSpecAPICall{}
	seen := map[string]struct{}{}
	for _, frontendRoot := range frontendRoots {
		err := filepath.WalkDir(frontendRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !frontendRouteSourceFile(path) {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(raw)
			for _, match := range axiosMethodPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[1]), match[2], interpolationPattern)
			}
			for _, match := range axiosTemplateMethodPattern.FindAllStringSubmatch(src, -1) {
				if idx := strings.Index(match[2], "/api/"); idx >= 0 {
					appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[1]), match[2][idx:], interpolationPattern)
				}
			}
			for _, match := range fetchOptionsPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[2]), match[1], interpolationPattern)
			}
			for _, match := range fetchFromAPIQuotedPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[2]), match[1], interpolationPattern)
			}
			for _, match := range fetchFromAPITemplatePattern.FindAllStringSubmatch(src, -1) {
				if idx := strings.Index(match[1], "/api/"); idx >= 0 {
					appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[2]), match[1][idx:], interpolationPattern)
				}
			}
			for _, match := range sendBeaconPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, "POST", match[1], interpolationPattern)
			}
			for _, match := range xhrOpenPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[1]), match[2], interpolationPattern)
			}
			for _, match := range requestQuotedPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[2]), match[1], interpolationPattern)
			}
			for _, match := range requestQuotedMethodFirstPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[1]), match[2], interpolationPattern)
			}
			for _, match := range requestTernaryPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[3]), match[1], interpolationPattern)
				if idx := strings.Index(match[2], "/api/"); idx >= 0 {
					appendFrontendAPICall(&calls, seen, path, strings.ToUpper(match[4]), match[2][idx:], interpolationPattern)
				}
			}
			for _, match := range quotedPattern.FindAllStringSubmatch(src, -1) {
				appendFrontendAPICall(&calls, seen, path, "*", match[1], interpolationPattern)
			}
			for _, match := range templatePattern.FindAllStringSubmatch(src, -1) {
				if idx := strings.Index(match[1], "/api/"); idx >= 0 {
					appendFrontendAPICall(&calls, seen, path, "*", match[1][idx:], interpolationPattern)
				}
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return calls, err
		}
	}
	return calls, nil
}

func frontendRouteSourceFile(path string) bool {
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

func appendFrontendAPICall(calls *[]railsSpecAPICall, seen map[string]struct{}, file string, method string, rawPath string, interpolationPattern *regexp.Regexp) {
	path := interpolationPattern.ReplaceAllString(rawPath, "dynamic")
	if beforeQuery, _, ok := strings.Cut(path, "?"); ok {
		path = beforeQuery
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return
	}
	call := railsSpecAPICall{
		file:    filepath.ToSlash(file),
		method:  method,
		rawPath: rawPath,
		path:    path,
	}
	key := call.method + " " + call.path + " " + call.file
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*calls = append(*calls, call)
}

func railsSpecPathPattern(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[i] = "dynamic"
		}
	}
	return strings.Join(parts, "/")
}

func railsRequestSpecCallIsTestOnly(call railsSpecAPICall) bool {
	switch {
	case strings.Contains(call.file, "catch_all_route_request_spec.rb"):
		return true
	case strings.Contains(call.file, "signature_verification_spec.rb"):
		return true
	default:
		return false
	}
}

func goRoutesContainSpecCall(routes []registeredRoute, call railsSpecAPICall) bool {
	candidates := []string{call.path}
	if strings.HasPrefix(call.path, "/api/") {
		trimmed := strings.TrimRight(call.path, "/")
		if trimmed != call.path {
			candidates = append(candidates, trimmed)
		}
	}
	for _, route := range routes {
		if call.method != "*" && route.method != call.method {
			continue
		}
		for _, candidate := range candidates {
			if route.match.MatchString(candidate) {
				return true
			}
		}
	}
	return false
}

func TestRailsCORSMiddlewareStaysBeforeAPIGate(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	requestID := strings.Index(body, `e.Use(requestIDMiddleware)`)
	hostAuth := strings.Index(body, `e.Use(hostAuthorizationMiddleware(cfg))`)
	forceSSL := strings.Index(body, `e.Use(forceSSLMiddleware(cfg))`)
	rateLimit := strings.Index(body, `e.Use(apiRateLimitHeadersMiddleware)`)
	securityHeaders := strings.Index(body, `e.Use(securityHeadersMiddleware)`)
	cors := strings.Index(body, `e.Use(corsMiddleware)`)
	gate := strings.Index(body, `e.Use(server.apiAuthenticationGateMiddleware)`)
	if cors < 0 || gate < 0 {
		t.Fatal("server.go must register Rails-compatible CORS middleware and API auth gate")
	}
	if requestID < 0 || hostAuth < 0 || forceSSL < 0 || rateLimit < 0 || securityHeaders < 0 {
		t.Fatal("server.go must register Rails-compatible request-id, host auth, force SSL, API rate-limit, and security headers middleware")
	}
	if requestID > cors {
		t.Fatal("request-id middleware must run before CORS responses are written")
	}
	if cors > hostAuth || cors > forceSSL {
		t.Fatal("CORS middleware must run before host authorization and force SSL like Rack::Cors inserted at index 0")
	}
	if cors > gate {
		t.Fatal("CORS middleware must run before the API auth gate so OPTIONS preflight is anonymous")
	}
	for _, want := range []string{
		`Access-Control-Expose-Headers`,
		`X-Request-Id`,
		`X-RateLimit-Limit`,
		`X-RateLimit-Remaining`,
		`railsAPICORSPath`,
		`railsOAuthTokenCORSPath`,
		`railsPublicCORSPath`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("server.go missing CORS compatibility wiring %q", want)
		}
	}
}

func TestRailsMethodOverrideRunsBeforeRouting(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	trailingSlash := strings.Index(body, `e.Pre(apiTrailingSlashMiddleware)`)
	pre := strings.Index(body, `e.Pre(methodOverrideMiddleware)`)
	routes := strings.Index(body, `server.routes()`)
	if trailingSlash < 0 {
		t.Fatal("server.go must register Rails-compatible API trailing slash normalization as a pre-router middleware")
	}
	if pre < 0 {
		t.Fatal("server.go must register Rails-compatible method override as a pre-router middleware")
	}
	if trailingSlash > pre {
		t.Fatal("API trailing slash normalization must run before method override and routing")
	}
	if routes >= 0 && pre > routes {
		t.Fatal("method override must be registered before routes are used")
	}
	for _, want := range []string{
		`apiTrailingSlashMiddleware`,
		`strings.HasPrefix(path, "/api/")`,
		`strings.TrimRight(path, "/")`,
		`X-HTTP-Method-Override`,
		`application/x-www-form-urlencoded`,
		`_method`,
		`http.MethodPatch`,
		`http.MethodDelete`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("server.go missing method override compatibility wiring %q", want)
		}
	}
}

func TestRootNonGetRoutesMatchRailsRaiseNotFound(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/", nil)
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s / status = %d body = %s", method, rec.Code, rec.Body.String())
		}
	}
}

func railsWebAppPaths(root string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "config", "routes.rb"))
	if err != nil {
		return nil, err
	}
	blockPattern := regexp.MustCompile(`(?ms)web_app_paths\s*=\s*%w\((.*?)\)\.freeze`)
	match := blockPattern.FindStringSubmatch(string(raw))
	if match == nil {
		return nil, nil
	}
	paths := []string{}
	for _, token := range strings.Fields(match[1]) {
		if strings.HasSuffix(token, "/(*any)") {
			base := strings.TrimSuffix(token, "/(*any)")
			paths = append(paths, base, base+"/*")
			continue
		}
		paths = append(paths, token)
	}
	return paths, nil
}

func TestRailsAdminWebRoutesStayRegistered(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := []string{
		`e.GET("/admin", redirectTo("/admin/dashboard", http.StatusFound))`,
		`e.GET("/admin.json", redirectTo("/admin/dashboard", http.StatusFound))`,
		`e.GET("/admin.:format", redirectTo("/admin/dashboard", http.StatusFound))`,
		`e.GET("/admin/dashboard.:format", s.adminDashboardPage)`,
		`e.GET("/admin/dashboard", s.adminDashboardPage)`,
		`e.GET("/admin/settings", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))`,
		`e.GET("/admin/settings.json", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))`,
		`e.GET("/admin/settings.:format", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))`,
		`e.GET("/admin/settings/edit", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))`,
		`e.GET("/admin/settings/edit.json", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))`,
		`e.GET("/admin/settings/edit.:format", redirectTo("/admin/settings/branding", http.StatusMovedPermanently))`,
		`e.GET("/admin/settings/branding.:format", s.adminSettingsBrandingPage)`,
		`e.GET("/admin/settings/branding", s.adminSettingsBrandingPage)`,
		`e.PUT("/admin/settings/branding.:format", s.updateAdminSettingsBranding)`,
		`e.PUT("/admin/settings/branding", s.updateAdminSettingsBranding)`,
		`e.PATCH("/admin/settings/branding.:format", s.updateAdminSettingsBranding)`,
		`e.PATCH("/admin/settings/branding", s.updateAdminSettingsBranding)`,
		`e.POST("/admin/settings/branding.:format", s.notFound)`,
		`e.POST("/admin/settings/branding", s.notFound)`,
		`e.GET("/admin/settings/registrations.:format", s.adminSettingsRegistrationsPage)`,
		`e.GET("/admin/settings/registrations", s.adminSettingsRegistrationsPage)`,
		`e.PUT("/admin/settings/registrations.:format", s.updateAdminSettingsRegistrations)`,
		`e.PUT("/admin/settings/registrations", s.updateAdminSettingsRegistrations)`,
		`e.PATCH("/admin/settings/registrations.:format", s.updateAdminSettingsRegistrations)`,
		`e.PATCH("/admin/settings/registrations", s.updateAdminSettingsRegistrations)`,
		`e.POST("/admin/settings/registrations.:format", s.notFound)`,
		`e.POST("/admin/settings/registrations", s.notFound)`,
		`e.GET("/admin/settings/content_retention.:format", s.adminSettingsContentRetentionPage)`,
		`e.GET("/admin/settings/content_retention", s.adminSettingsContentRetentionPage)`,
		`e.PUT("/admin/settings/content_retention.:format", s.updateAdminSettingsContentRetention)`,
		`e.PUT("/admin/settings/content_retention", s.updateAdminSettingsContentRetention)`,
		`e.PATCH("/admin/settings/content_retention.:format", s.updateAdminSettingsContentRetention)`,
		`e.PATCH("/admin/settings/content_retention", s.updateAdminSettingsContentRetention)`,
		`e.POST("/admin/settings/content_retention.:format", s.notFound)`,
		`e.POST("/admin/settings/content_retention", s.notFound)`,
		`e.GET("/admin/settings/about.:format", s.adminSettingsAboutPage)`,
		`e.GET("/admin/settings/about", s.adminSettingsAboutPage)`,
		`e.PUT("/admin/settings/about.:format", s.updateAdminSettingsAbout)`,
		`e.PUT("/admin/settings/about", s.updateAdminSettingsAbout)`,
		`e.PATCH("/admin/settings/about.:format", s.updateAdminSettingsAbout)`,
		`e.PATCH("/admin/settings/about", s.updateAdminSettingsAbout)`,
		`e.POST("/admin/settings/about.:format", s.notFound)`,
		`e.POST("/admin/settings/about", s.notFound)`,
		`e.GET("/admin/settings/appearance.:format", s.adminSettingsAppearancePage)`,
		`e.GET("/admin/settings/appearance", s.adminSettingsAppearancePage)`,
		`e.PUT("/admin/settings/appearance.:format", s.updateAdminSettingsAppearance)`,
		`e.PUT("/admin/settings/appearance", s.updateAdminSettingsAppearance)`,
		`e.PATCH("/admin/settings/appearance.:format", s.updateAdminSettingsAppearance)`,
		`e.PATCH("/admin/settings/appearance", s.updateAdminSettingsAppearance)`,
		`e.POST("/admin/settings/appearance.:format", s.notFound)`,
		`e.POST("/admin/settings/appearance", s.notFound)`,
		`e.GET("/admin/settings/discovery.:format", s.adminSettingsDiscoveryPage)`,
		`e.GET("/admin/settings/discovery", s.adminSettingsDiscoveryPage)`,
		`e.PUT("/admin/settings/discovery.:format", s.updateAdminSettingsDiscovery)`,
		`e.PUT("/admin/settings/discovery", s.updateAdminSettingsDiscovery)`,
		`e.PATCH("/admin/settings/discovery.:format", s.updateAdminSettingsDiscovery)`,
		`e.PATCH("/admin/settings/discovery", s.updateAdminSettingsDiscovery)`,
		`e.POST("/admin/settings/discovery.:format", s.notFound)`,
		`e.POST("/admin/settings/discovery", s.notFound)`,
		`e.GET("/admin/action_logs.:format", s.adminActionLogsPage)`,
		`e.GET("/admin/action_logs", s.adminActionLogsPage)`,
		`e.DELETE("/admin/site_uploads/:id", s.destroyAdminSiteUpload)`,
		`e.POST("/admin/site_uploads/:id", s.notFound)`,
		`e.GET("/admin/invites.:format", s.adminInvitesPage)`,
		`e.GET("/admin/invites", s.adminInvitesPage)`,
		`e.POST("/admin/invites", s.createAdminInvite)`,
		`e.POST("/admin/invites/deactivate_all", s.deactivateAllAdminInvites)`,
		`e.DELETE("/admin/invites/:id", s.destroyAdminInvite)`,
		`e.POST("/admin/invites/:id", s.notFound)`,
		`e.GET("/admin/roles.:format", s.adminRolesPage)`,
		`e.GET("/admin/roles", s.adminRolesPage)`,
		`e.GET("/admin/roles/new.:format", s.newAdminRolePage)`,
		`e.GET("/admin/roles/new", s.newAdminRolePage)`,
		`e.POST("/admin/roles", s.createAdminRole)`,
		`e.GET("/admin/roles/:id/edit.:format", optionalFormatPathParam("id", s.editAdminRolePage))`,
		`e.GET("/admin/roles/:id/edit", s.editAdminRolePage)`,
		`e.PUT("/admin/roles/:id", s.updateAdminRole)`,
		`e.PATCH("/admin/roles/:id", s.updateAdminRole)`,
		`e.DELETE("/admin/roles/:id", s.destroyAdminRole)`,
		`e.POST("/admin/roles/:id", s.notFound)`,
		`e.GET("/admin/users/:user_id/role.:format", s.adminUserRolePage)`,
		`e.GET("/admin/users/:user_id/role", s.adminUserRolePage)`,
		`e.PUT("/admin/users/:user_id/role", s.updateAdminUserRole)`,
		`e.PATCH("/admin/users/:user_id/role", s.updateAdminUserRole)`,
		`e.POST("/admin/users/:user_id/role", s.notFound)`,
		`e.DELETE("/admin/users/:user_id/two_factor_authentication", s.destroyAdminUserTwoFactor)`,
		`e.POST("/admin/users/:user_id/two_factor_authentication", s.notFound)`,
		`e.GET("/admin/accounts.:format", s.adminAccountsPage)`,
		`e.GET("/admin/accounts", s.adminAccountsPage)`,
		`e.POST("/admin/accounts/batch", s.batchAdminAccountsWeb)`,
		`e.GET("/admin/accounts/:id.:format", optionalFormatPathParam("id", s.adminAccountPage))`,
		`e.GET("/admin/accounts/:id", s.adminAccountPage)`,
		`e.DELETE("/admin/accounts/:id", s.destroyAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id", s.notFound)`,
		`e.POST("/admin/accounts/:id/enable.:format", s.enableAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/enable", s.enableAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/unsensitive.:format", s.unsensitiveAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/unsensitive", s.unsensitiveAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/unsilence.:format", s.unsilenceAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/unsilence", s.unsilenceAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/unsuspend.:format", s.unsuspendAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/unsuspend", s.unsuspendAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/redownload.:format", s.redownloadAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/redownload", s.redownloadAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/remove_avatar.:format", s.removeAvatarAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/remove_avatar", s.removeAvatarAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/remove_header.:format", s.removeHeaderAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/remove_header", s.removeHeaderAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/memorialize.:format", s.memorializeAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/memorialize", s.memorializeAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/approve.:format", s.approveAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/approve", s.approveAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/reject.:format", s.rejectAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/reject", s.rejectAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/unblock_email.:format", s.unblockEmailAdminAccountWeb)`,
		`e.POST("/admin/accounts/:id/unblock_email", s.unblockEmailAdminAccountWeb)`,
		`e.GET("/admin/accounts/:account_id/action/new.:format", s.newAdminAccountActionPage)`,
		`e.GET("/admin/accounts/:account_id/action/new", s.newAdminAccountActionPage)`,
		`e.POST("/admin/accounts/:account_id/action", s.createAdminAccountActionWeb)`,
		`e.GET("/admin/accounts/:account_id/statuses.:format", s.adminAccountStatusesPage)`,
		`e.GET("/admin/accounts/:account_id/statuses", s.adminAccountStatusesPage)`,
		`e.GET("/admin/accounts/:account_id/statuses/:id.:format", optionalFormatPathParam("id", s.adminAccountStatusPage))`,
		`e.GET("/admin/accounts/:account_id/statuses/:id", s.adminAccountStatusPage)`,
		`e.POST("/admin/accounts/:account_id/statuses/batch", s.batchAdminAccountStatusesWeb)`,
		`e.GET("/admin/accounts/:account_id/relationships.:format", s.adminAccountRelationshipsPage)`,
		`e.GET("/admin/accounts/:account_id/relationships", s.adminAccountRelationshipsPage)`,
		`e.GET("/admin/accounts/:account_id/change_email.:format", s.adminAccountChangeEmailPage)`,
		`e.GET("/admin/accounts/:account_id/change_email", s.adminAccountChangeEmailPage)`,
		`e.PUT("/admin/accounts/:account_id/change_email", s.updateAdminAccountChangeEmail)`,
		`e.PATCH("/admin/accounts/:account_id/change_email", s.updateAdminAccountChangeEmail)`,
		`e.POST("/admin/accounts/:account_id/change_email", s.notFound)`,
		`e.POST("/admin/accounts/:account_id/reset", s.resetAdminAccountPasswordWeb)`,
		`e.POST("/admin/accounts/:account_id/confirmation", s.confirmAdminAccountWeb)`,
		`e.POST("/admin/accounts/:account_id/confirmation/resend", s.resendAdminAccountConfirmationWeb)`,
		`e.GET("/admin/reports.:format", s.adminReportsPage)`,
		`e.GET("/admin/reports", s.adminReportsPage)`,
		`e.GET("/admin/reports/:id.:format", optionalFormatPathParam("id", s.adminReportPage))`,
		`e.GET("/admin/reports/:id", s.adminReportPage)`,
		`e.POST("/admin/reports/:id", s.notFound)`,
		`e.PUT("/admin/reports/:id", s.notFound)`,
		`e.PATCH("/admin/reports/:id", s.notFound)`,
		`e.POST("/admin/reports/:id/assign_to_self", s.assignAdminReportToSelfWeb)`,
		`e.POST("/admin/reports/:id/unassign", s.unassignAdminReportWeb)`,
		`e.POST("/admin/reports/:id/reopen", s.reopenAdminReportWeb)`,
		`e.POST("/admin/reports/:id/resolve", s.resolveAdminReportWeb)`,
		`e.POST("/admin/reports/:report_id/actions/preview", s.previewAdminReportActionWeb)`,
		`e.POST("/admin/reports/:report_id/actions", s.createAdminReportActionWeb)`,
		`e.POST("/admin/account_moderation_notes", s.createAdminAccountModerationNoteWeb)`,
		`e.DELETE("/admin/account_moderation_notes/:id", s.destroyAdminAccountModerationNoteWeb)`,
		`e.POST("/admin/account_moderation_notes/:id", s.notFound)`,
		`e.POST("/admin/report_notes", s.createAdminReportNoteWeb)`,
		`e.DELETE("/admin/report_notes/:id", s.destroyAdminReportNoteWeb)`,
		`e.POST("/admin/report_notes/:id", s.notFound)`,
		`e.GET("/admin/domain_allows/new.:format", s.newAdminDomainAllowPage)`,
		`e.GET("/admin/domain_allows/new", s.newAdminDomainAllowPage)`,
		`e.POST("/admin/domain_allows", s.createAdminDomainAllowWeb)`,
		`e.DELETE("/admin/domain_allows/:id", s.destroyAdminDomainAllowWeb)`,
		`e.POST("/admin/domain_allows/:id", s.notFound)`,
		`e.GET("/admin/domain_blocks/new.:format", s.newAdminDomainBlockPage)`,
		`e.GET("/admin/domain_blocks/new", s.newAdminDomainBlockPage)`,
		`e.POST("/admin/domain_blocks", s.createAdminDomainBlockWeb)`,
		`e.POST("/admin/domain_blocks/batch", s.batchAdminDomainBlocks)`,
		`e.GET("/admin/domain_blocks/:id/edit.:format", optionalFormatPathParam("id", s.editAdminDomainBlockPage))`,
		`e.GET("/admin/domain_blocks/:id/edit", s.editAdminDomainBlockPage)`,
		`e.PUT("/admin/domain_blocks/:id", s.updateAdminDomainBlockWeb)`,
		`e.PATCH("/admin/domain_blocks/:id", s.updateAdminDomainBlockWeb)`,
		`e.DELETE("/admin/domain_blocks/:id", s.destroyAdminDomainBlockWeb)`,
		`e.POST("/admin/domain_blocks/:id", s.notFound)`,
		`e.GET("/admin/export_domain_allows/new.:format", s.newAdminExportDomainAllowsPage)`,
		`e.GET("/admin/export_domain_allows/new", s.newAdminExportDomainAllowsPage)`,
		`e.GET("/admin/export_domain_blocks/export.csv", s.exportAdminDomainBlocksCSV)`,
		`e.POST("/admin/export_domain_blocks/import", s.importAdminDomainBlocksCSV)`,
		`e.GET("/admin/export_domain_blocks/new.:format", s.newAdminExportDomainBlocksPage)`,
		`e.GET("/admin/export_domain_blocks/new", s.newAdminExportDomainBlocksPage)`,
		`e.GET("/admin/export_domain_allows/export.csv", s.exportAdminDomainAllowsCSV)`,
		`e.POST("/admin/export_domain_allows/import", s.importAdminDomainAllowsCSV)`,
		`e.GET("/admin/email_domain_blocks.:format", s.adminEmailDomainBlocksPage)`,
		`e.GET("/admin/email_domain_blocks", s.adminEmailDomainBlocksPage)`,
		`e.GET("/admin/email_domain_blocks/new.:format", s.newAdminEmailDomainBlockPage)`,
		`e.GET("/admin/email_domain_blocks/new", s.newAdminEmailDomainBlockPage)`,
		`e.POST("/admin/email_domain_blocks", s.createAdminEmailDomainBlockWeb)`,
		`e.POST("/admin/email_domain_blocks/batch", s.batchAdminEmailDomainBlocks)`,
		`e.GET("/admin/ip_blocks.:format", s.adminIPBlocksPage)`,
		`e.GET("/admin/ip_blocks", s.adminIPBlocksPage)`,
		`e.GET("/admin/ip_blocks/new.:format", s.newAdminIPBlockPage)`,
		`e.GET("/admin/ip_blocks/new", s.newAdminIPBlockPage)`,
		`e.POST("/admin/ip_blocks", s.createAdminIPBlockWeb)`,
		`e.POST("/admin/ip_blocks/batch", s.batchAdminIPBlocks)`,
		`e.GET("/admin/instances.html", s.adminInstancesPage)`,
		`e.GET("/admin/instances", s.adminInstancesPage)`,
		`e.GET("/admin/instances/:id", s.showAdminInstancePage)`,
		`e.DELETE("/admin/instances/:id", s.destroyAdminInstance)`,
		`e.POST("/admin/instances/:id", s.notFound)`,
		`e.POST("/admin/instances/:id/clear_delivery_errors.html", s.clearAdminInstanceDeliveryErrors)`,
		`e.POST("/admin/instances/:id/clear_delivery_errors", s.clearAdminInstanceDeliveryErrors)`,
		`e.POST("/admin/instances/:id/restart_delivery.html", s.restartAdminInstanceDelivery)`,
		`e.POST("/admin/instances/:id/restart_delivery", s.restartAdminInstanceDelivery)`,
		`e.POST("/admin/instances/:id/stop_delivery.html", s.stopAdminInstanceDelivery)`,
		`e.POST("/admin/instances/:id/stop_delivery", s.stopAdminInstanceDelivery)`,
		`e.GET("/admin/relays.:format", s.adminRelaysPage)`,
		`e.GET("/admin/relays", s.adminRelaysPage)`,
		`e.GET("/admin/relays/new.:format", s.newAdminRelayPage)`,
		`e.GET("/admin/relays/new", s.newAdminRelayPage)`,
		`e.POST("/admin/relays", s.createAdminRelay)`,
		`e.POST("/admin/relays/:id/enable", s.enableAdminRelay)`,
		`e.POST("/admin/relays/:id/disable", s.disableAdminRelay)`,
		`e.DELETE("/admin/relays/:id", s.destroyAdminRelay)`,
		`e.POST("/admin/relays/:id", s.notFound)`,
		`e.GET("/admin/rules.:format", s.adminRulesPage)`,
		`e.GET("/admin/rules", s.adminRulesPage)`,
		`e.POST("/admin/rules", s.createAdminRule)`,
		`e.GET("/admin/rules/:id/edit.:format", optionalFormatPathParam("id", s.editAdminRulePage))`,
		`e.GET("/admin/rules/:id/edit", s.editAdminRulePage)`,
		`e.PUT("/admin/rules/:id", s.updateAdminRule)`,
		`e.PATCH("/admin/rules/:id", s.updateAdminRule)`,
		`e.DELETE("/admin/rules/:id", s.destroyAdminRule)`,
		`e.POST("/admin/rules/:id", s.notFound)`,
		`e.GET("/admin/webhooks.:format", s.adminWebhooksPage)`,
		`e.GET("/admin/webhooks", s.adminWebhooksPage)`,
		`e.GET("/admin/webhooks/new.:format", s.newAdminWebhookPage)`,
		`e.GET("/admin/webhooks/new", s.newAdminWebhookPage)`,
		`e.POST("/admin/webhooks", s.createAdminWebhook)`,
		`e.GET("/admin/webhooks/:id.:format", optionalFormatPathParam("id", s.showAdminWebhookPage))`,
		`e.GET("/admin/webhooks/:id", s.showAdminWebhookPage)`,
		`e.GET("/admin/webhooks/:id/edit.:format", optionalFormatPathParam("id", s.editAdminWebhookPage))`,
		`e.GET("/admin/webhooks/:id/edit", s.editAdminWebhookPage)`,
		`e.PUT("/admin/webhooks/:id", s.updateAdminWebhook)`,
		`e.PATCH("/admin/webhooks/:id", s.updateAdminWebhook)`,
		`e.POST("/admin/webhooks/:id/enable", s.enableAdminWebhook)`,
		`e.POST("/admin/webhooks/:id/disable", s.disableAdminWebhook)`,
		`e.POST("/admin/webhooks/:webhook_id/secret/rotate", s.rotateAdminWebhookSecret)`,
		`e.DELETE("/admin/webhooks/:id", s.destroyAdminWebhook)`,
		`e.POST("/admin/webhooks/:id", s.notFound)`,
		`e.GET("/admin/custom_emojis.:format", s.adminCustomEmojisPage)`,
		`e.GET("/admin/custom_emojis", s.adminCustomEmojisPage)`,
		`e.GET("/admin/custom_emojis/new.:format", s.newAdminCustomEmojiPage)`,
		`e.GET("/admin/custom_emojis/new", s.newAdminCustomEmojiPage)`,
		`e.POST("/admin/custom_emojis", s.createAdminCustomEmojiWeb)`,
		`e.POST("/admin/custom_emojis/batch", s.batchAdminCustomEmojisWeb)`,
		`e.GET("/admin/follow_recommendations.:format", s.adminFollowRecommendationsPage)`,
		`e.GET("/admin/follow_recommendations", s.adminFollowRecommendationsPage)`,
		`e.PATCH("/admin/follow_recommendations", s.updateAdminFollowRecommendations)`,
		`e.PUT("/admin/follow_recommendations", s.updateAdminFollowRecommendations)`,
		`e.POST("/admin/follow_recommendations", s.notFound)`,
		`e.GET("/admin/warning_presets.:format", s.adminWarningPresetsPage)`,
		`e.GET("/admin/warning_presets", s.adminWarningPresetsPage)`,
		`e.POST("/admin/warning_presets", s.createAdminWarningPreset)`,
		`e.GET("/admin/warning_presets/:id/edit.:format", optionalFormatPathParam("id", s.editAdminWarningPresetPage))`,
		`e.GET("/admin/warning_presets/:id/edit", s.editAdminWarningPresetPage)`,
		`e.PUT("/admin/warning_presets/:id", s.updateAdminWarningPreset)`,
		`e.PATCH("/admin/warning_presets/:id", s.updateAdminWarningPreset)`,
		`e.DELETE("/admin/warning_presets/:id", s.destroyAdminWarningPreset)`,
		`e.POST("/admin/warning_presets/:id", s.notFound)`,
		`e.GET("/admin/announcements.:format", s.adminAnnouncementsPage)`,
		`e.GET("/admin/announcements", s.adminAnnouncementsPage)`,
		`e.GET("/admin/announcements/new.:format", s.newAdminAnnouncementPage)`,
		`e.GET("/admin/announcements/new", s.newAdminAnnouncementPage)`,
		`e.POST("/admin/announcements", s.createAdminAnnouncement)`,
		`e.GET("/admin/announcements/:id/edit.:format", optionalFormatPathParam("id", s.editAdminAnnouncementPage))`,
		`e.GET("/admin/announcements/:id/edit", s.editAdminAnnouncementPage)`,
		`e.PUT("/admin/announcements/:id", s.updateAdminAnnouncement)`,
		`e.PATCH("/admin/announcements/:id", s.updateAdminAnnouncement)`,
		`e.POST("/admin/announcements/:id/publish", s.publishAdminAnnouncement)`,
		`e.POST("/admin/announcements/:id/unpublish", s.unpublishAdminAnnouncement)`,
		`e.DELETE("/admin/announcements/:id", s.destroyAdminAnnouncement)`,
		`e.POST("/admin/announcements/:id", s.notFound)`,
		`e.GET("/admin/tags/:id.:format", optionalFormatPathParam("id", s.adminTagPage))`,
		`e.GET("/admin/tags/:id", s.adminTagPage)`,
		`e.PATCH("/admin/tags/:id", s.updateAdminTagWeb)`,
		`e.PUT("/admin/tags/:id", s.updateAdminTagWeb)`,
		`e.POST("/admin/tags/:id", s.notFound)`,
		`e.GET("/admin/trends/tags.:format", s.adminTrendsTagsPage)`,
		`e.GET("/admin/trends/tags", s.adminTrendsTagsPage)`,
		`e.POST("/admin/trends/tags/batch", s.batchAdminTrendsTags)`,
		`e.GET("/admin/trends/statuses.:format", s.adminTrendsStatusesPage)`,
		`e.GET("/admin/trends/statuses", s.adminTrendsStatusesPage)`,
		`e.POST("/admin/trends/statuses/batch", s.batchAdminTrendsStatuses)`,
		`e.GET("/admin/trends/links.:format", s.adminTrendsLinksPage)`,
		`e.GET("/admin/trends/links", s.adminTrendsLinksPage)`,
		`e.POST("/admin/trends/links/batch", s.batchAdminTrendsLinks)`,
		`e.GET("/admin/trends/links/publishers.:format", s.adminTrendsLinkPublishersPage)`,
		`e.GET("/admin/trends/links/publishers", s.adminTrendsLinkPublishersPage)`,
		`e.POST("/admin/trends/links/publishers/batch", s.batchAdminTrendsLinkPublishers)`,
		`e.GET("/admin/disputes/appeals.:format", s.adminAppealsPage)`,
		`e.GET("/admin/disputes/appeals", s.adminAppealsPage)`,
		`e.POST("/admin/disputes/appeals/:id/approve", s.approveAdminAppealWeb)`,
		`e.POST("/admin/disputes/appeals/:id/reject", s.rejectAdminAppealWeb)`,
		`e.GET("/admin/software_updates.:format", s.adminSoftwareUpdatesPage)`,
		`e.GET("/admin/software_updates", s.adminSoftwareUpdatesPage)`,
	}
	for _, route := range routes {
		if !strings.Contains(string(src), route) {
			t.Fatalf("server.go missing Rails admin web route %q", route)
		}
	}
	adminOptionalFormatMutationRoutes := []string{
		`e.DELETE("/admin/site_uploads/:id.:format", optionalFormatPathParam("id", s.destroyAdminSiteUpload))`,
		`e.POST("/admin/invites.:format", s.createAdminInvite)`,
		`e.POST("/admin/invites/deactivate_all.:format", s.deactivateAllAdminInvites)`,
		`e.DELETE("/admin/invites/:id.:format", optionalFormatPathParam("id", s.destroyAdminInvite))`,
		`e.POST("/admin/rules.:format", s.createAdminRule)`,
		`e.PUT("/admin/rules/:id.:format", optionalFormatPathParam("id", s.updateAdminRule))`,
		`e.PATCH("/admin/rules/:id.:format", optionalFormatPathParam("id", s.updateAdminRule))`,
		`e.DELETE("/admin/rules/:id.:format", optionalFormatPathParam("id", s.destroyAdminRule))`,
		`e.POST("/admin/roles.:format", s.createAdminRole)`,
		`e.PUT("/admin/roles/:id.:format", optionalFormatPathParam("id", s.updateAdminRole))`,
		`e.PATCH("/admin/roles/:id.:format", optionalFormatPathParam("id", s.updateAdminRole))`,
		`e.DELETE("/admin/roles/:id.:format", optionalFormatPathParam("id", s.destroyAdminRole))`,
		`e.PUT("/admin/users/:user_id/role.:format", s.updateAdminUserRole)`,
		`e.PATCH("/admin/users/:user_id/role.:format", s.updateAdminUserRole)`,
		`e.DELETE("/admin/users/:user_id/two_factor_authentication.:format", s.destroyAdminUserTwoFactor)`,
		`e.PATCH("/admin/tags/:id.:format", optionalFormatPathParam("id", s.updateAdminTagWeb))`,
		`e.PUT("/admin/tags/:id.:format", optionalFormatPathParam("id", s.updateAdminTagWeb))`,
		`e.POST("/admin/trends/tags/batch.:format", s.batchAdminTrendsTags)`,
		`e.POST("/admin/trends/statuses/batch.:format", s.batchAdminTrendsStatuses)`,
		`e.POST("/admin/trends/links/batch.:format", s.batchAdminTrendsLinks)`,
		`e.POST("/admin/trends/links/publishers/batch.:format", s.batchAdminTrendsLinkPublishers)`,
		`e.PATCH("/admin/follow_recommendations.:format", s.updateAdminFollowRecommendations)`,
		`e.PUT("/admin/follow_recommendations.:format", s.updateAdminFollowRecommendations)`,
		`e.POST("/admin/warning_presets.:format", s.createAdminWarningPreset)`,
		`e.PUT("/admin/warning_presets/:id.:format", optionalFormatPathParam("id", s.updateAdminWarningPreset))`,
		`e.PATCH("/admin/warning_presets/:id.:format", optionalFormatPathParam("id", s.updateAdminWarningPreset))`,
		`e.DELETE("/admin/warning_presets/:id.:format", optionalFormatPathParam("id", s.destroyAdminWarningPreset))`,
		`e.POST("/admin/announcements.:format", s.createAdminAnnouncement)`,
		`e.PUT("/admin/announcements/:id.:format", optionalFormatPathParam("id", s.updateAdminAnnouncement))`,
		`e.PATCH("/admin/announcements/:id.:format", optionalFormatPathParam("id", s.updateAdminAnnouncement))`,
		`e.POST("/admin/announcements/:id/publish.:format", s.publishAdminAnnouncement)`,
		`e.POST("/admin/announcements/:id/unpublish.:format", s.unpublishAdminAnnouncement)`,
		`e.DELETE("/admin/announcements/:id.:format", optionalFormatPathParam("id", s.destroyAdminAnnouncement))`,
		`e.POST("/admin/relays.:format", s.createAdminRelay)`,
		`e.POST("/admin/relays/:id/enable.:format", s.enableAdminRelay)`,
		`e.POST("/admin/relays/:id/disable.:format", s.disableAdminRelay)`,
		`e.DELETE("/admin/relays/:id.:format", optionalFormatPathParam("id", s.destroyAdminRelay))`,
		`e.POST("/admin/webhooks.:format", s.createAdminWebhook)`,
		`e.PUT("/admin/webhooks/:id.:format", optionalFormatPathParam("id", s.updateAdminWebhook))`,
		`e.PATCH("/admin/webhooks/:id.:format", optionalFormatPathParam("id", s.updateAdminWebhook))`,
		`e.POST("/admin/webhooks/:id/enable.:format", s.enableAdminWebhook)`,
		`e.POST("/admin/webhooks/:id/disable.:format", s.disableAdminWebhook)`,
		`e.POST("/admin/webhooks/:webhook_id/secret/rotate.:format", s.rotateAdminWebhookSecret)`,
		`e.DELETE("/admin/webhooks/:id.:format", optionalFormatPathParam("id", s.destroyAdminWebhook))`,
		`e.POST("/admin/accounts/batch.:format", s.batchAdminAccountsWeb)`,
		`e.DELETE("/admin/accounts/:id.:format", optionalFormatPathParam("id", s.destroyAdminAccountWeb))`,
		`e.POST("/admin/accounts/:account_id/action.:format", s.createAdminAccountActionWeb)`,
		`e.POST("/admin/accounts/:account_id/statuses/batch.:format", s.batchAdminAccountStatusesWeb)`,
		`e.PUT("/admin/accounts/:account_id/change_email.:format", s.updateAdminAccountChangeEmail)`,
		`e.PATCH("/admin/accounts/:account_id/change_email.:format", s.updateAdminAccountChangeEmail)`,
		`e.POST("/admin/accounts/:account_id/reset.:format", s.resetAdminAccountPasswordWeb)`,
		`e.POST("/admin/accounts/:account_id/confirmation.:format", s.confirmAdminAccountWeb)`,
		`e.POST("/admin/accounts/:account_id/confirmation/resend.:format", s.resendAdminAccountConfirmationWeb)`,
		`e.POST("/admin/disputes/appeals/:id/approve.:format", s.approveAdminAppealWeb)`,
		`e.POST("/admin/disputes/appeals/:id/reject.:format", s.rejectAdminAppealWeb)`,
		`e.POST("/admin/custom_emojis.:format", s.createAdminCustomEmojiWeb)`,
		`e.POST("/admin/custom_emojis/batch.:format", s.batchAdminCustomEmojisWeb)`,
		`e.POST("/admin/ip_blocks.:format", s.createAdminIPBlockWeb)`,
		`e.POST("/admin/ip_blocks/batch.:format", s.batchAdminIPBlocks)`,
		`e.POST("/admin/email_domain_blocks.:format", s.createAdminEmailDomainBlockWeb)`,
		`e.POST("/admin/email_domain_blocks/batch.:format", s.batchAdminEmailDomainBlocks)`,
		`e.POST("/admin/domain_allows.:format", s.createAdminDomainAllowWeb)`,
		`e.DELETE("/admin/domain_allows/:id.:format", optionalFormatPathParam("id", s.destroyAdminDomainAllowWeb))`,
		`e.POST("/admin/domain_blocks.:format", s.createAdminDomainBlockWeb)`,
		`e.POST("/admin/domain_blocks/batch.:format", s.batchAdminDomainBlocks)`,
		`e.PUT("/admin/domain_blocks/:id.:format", optionalFormatPathParam("id", s.updateAdminDomainBlockWeb))`,
		`e.PATCH("/admin/domain_blocks/:id.:format", optionalFormatPathParam("id", s.updateAdminDomainBlockWeb))`,
		`e.DELETE("/admin/domain_blocks/:id.:format", optionalFormatPathParam("id", s.destroyAdminDomainBlockWeb))`,
		`e.POST("/admin/export_domain_allows/import.:format", s.importAdminDomainAllowsCSV)`,
		`e.POST("/admin/export_domain_blocks/import.:format", s.importAdminDomainBlocksCSV)`,
		`e.POST("/admin/reports/:id.:format", optionalFormatPathParam("id", s.notFound))`,
		`e.PUT("/admin/reports/:id.:format", optionalFormatPathParam("id", s.notFound))`,
		`e.PATCH("/admin/reports/:id.:format", optionalFormatPathParam("id", s.notFound))`,
		`e.POST("/admin/reports/:id/assign_to_self.:format", s.assignAdminReportToSelfWeb)`,
		`e.POST("/admin/reports/:id/unassign.:format", s.unassignAdminReportWeb)`,
		`e.POST("/admin/reports/:id/reopen.:format", s.reopenAdminReportWeb)`,
		`e.POST("/admin/reports/:id/resolve.:format", s.resolveAdminReportWeb)`,
		`e.POST("/admin/reports/:report_id/actions/preview.:format", s.previewAdminReportActionWeb)`,
		`e.POST("/admin/reports/:report_id/actions.:format", s.createAdminReportActionWeb)`,
		`e.POST("/admin/account_moderation_notes.:format", s.createAdminAccountModerationNoteWeb)`,
		`e.DELETE("/admin/account_moderation_notes/:id.:format", optionalFormatPathParam("id", s.destroyAdminAccountModerationNoteWeb))`,
		`e.POST("/admin/report_notes.:format", s.createAdminReportNoteWeb)`,
		`e.DELETE("/admin/report_notes/:id.:format", optionalFormatPathParam("id", s.destroyAdminReportNoteWeb))`,
	}
	for _, route := range adminOptionalFormatMutationRoutes {
		if !strings.Contains(string(src), route) {
			t.Fatalf("server.go missing Rails admin optional-format mutation route %q", route)
		}
	}
	for _, forbidden := range []string{
		`e.GET("/admin/instances/:id.:format"`,
		`e.POST("/admin/instances/:id.:format"`,
		`optionalFormatPathParam("id", s.showAdminInstancePage)`,
	} {
		if strings.Contains(string(src), forbidden) {
			t.Fatalf("server.go must preserve Rails greedy dotted admin instance ids; forbidden route fragment %q", forbidden)
		}
	}
}

func TestRailsSettingsWebRoutesStayRegistered(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := []string{
		`e.GET("/settings", redirectTo("/settings/profile", http.StatusMovedPermanently))`,
		`e.GET("/settings.json", redirectTo("/settings/profile", http.StatusMovedPermanently))`,
		`e.GET("/settings.:format", redirectTo("/settings/profile", http.StatusMovedPermanently))`,
		`e.GET("/settings/profile.:format", s.settingsPage)`,
		`e.GET("/settings/profile", s.settingsPage)`,
		`e.PUT("/settings/profile.:format", s.updateSettingsProfile)`,
		`e.PUT("/settings/profile", s.updateSettingsProfile)`,
		`e.PATCH("/settings/profile.:format", s.updateSettingsProfile)`,
		`e.PATCH("/settings/profile", s.updateSettingsProfile)`,
		`e.POST("/settings/profile.:format", s.notFound)`,
		`e.POST("/settings/profile", s.notFound)`,
		`e.DELETE("/settings/profile/pictures/:id.:format", optionalFormatPathParam("id", s.destroySettingsProfilePicture))`,
		`e.DELETE("/settings/profile/pictures/:id", s.destroySettingsProfilePicture)`,
		`e.POST("/settings/profile/pictures/:id.:format", s.notFound)`,
		`e.POST("/settings/profile/pictures/:id", s.notFound)`,
		`e.GET("/settings/preferences", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))`,
		`e.GET("/settings/preferences.json", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))`,
		`e.GET("/settings/preferences.:format", redirectTo("/settings/preferences/appearance", http.StatusMovedPermanently))`,
		`e.GET("/settings/preferences/appearance.:format", s.settingsPage)`,
		`e.GET("/settings/preferences/appearance", s.settingsPage)`,
		`e.PUT("/settings/preferences/appearance.:format", s.updateSettingsPreferences)`,
		`e.PUT("/settings/preferences/appearance", s.updateSettingsPreferences)`,
		`e.PATCH("/settings/preferences/appearance.:format", s.updateSettingsPreferences)`,
		`e.PATCH("/settings/preferences/appearance", s.updateSettingsPreferences)`,
		`e.POST("/settings/preferences/appearance.:format", s.notFound)`,
		`e.POST("/settings/preferences/appearance", s.notFound)`,
		`e.GET("/settings/preferences/notifications.:format", s.settingsPage)`,
		`e.GET("/settings/preferences/notifications", s.settingsPage)`,
		`e.PUT("/settings/preferences/notifications.:format", s.updateSettingsPreferences)`,
		`e.PUT("/settings/preferences/notifications", s.updateSettingsPreferences)`,
		`e.PATCH("/settings/preferences/notifications.:format", s.updateSettingsPreferences)`,
		`e.PATCH("/settings/preferences/notifications", s.updateSettingsPreferences)`,
		`e.POST("/settings/preferences/notifications.:format", s.notFound)`,
		`e.POST("/settings/preferences/notifications", s.notFound)`,
		`e.GET("/settings/preferences/other.:format", s.settingsPage)`,
		`e.GET("/settings/preferences/other", s.settingsPage)`,
		`e.PUT("/settings/preferences/other.:format", s.updateSettingsPreferences)`,
		`e.PUT("/settings/preferences/other", s.updateSettingsPreferences)`,
		`e.PATCH("/settings/preferences/other.:format", s.updateSettingsPreferences)`,
		`e.PATCH("/settings/preferences/other", s.updateSettingsPreferences)`,
		`e.POST("/settings/preferences/other.:format", s.notFound)`,
		`e.POST("/settings/preferences/other", s.notFound)`,
		`e.GET("/settings/privacy.:format", s.settingsPage)`,
		`e.GET("/settings/privacy", s.settingsPage)`,
		`e.PUT("/settings/privacy.:format", s.updateSettingsPrivacy)`,
		`e.PUT("/settings/privacy", s.updateSettingsPrivacy)`,
		`e.PATCH("/settings/privacy.:format", s.updateSettingsPrivacy)`,
		`e.PATCH("/settings/privacy", s.updateSettingsPrivacy)`,
		`e.POST("/settings/privacy.:format", s.notFound)`,
		`e.POST("/settings/privacy", s.notFound)`,
		`e.GET("/settings/export.:format", s.settingsPage)`,
		`e.GET("/settings/export", s.settingsPage)`,
		`e.POST("/settings/export.:format", s.createBackup)`,
		`e.POST("/settings/export", s.createBackup)`,
		`e.GET("/settings/exports/follows.csv", s.exportFollowsCSV)`,
		`e.GET("/settings/exports/blocks.csv", s.exportBlocksCSV)`,
		`e.GET("/settings/exports/mutes.csv", s.exportMutesCSV)`,
		`e.GET("/settings/exports/lists.csv", s.exportListsCSV)`,
		`e.GET("/settings/exports/domain_blocks.csv", s.exportDomainBlocksCSV)`,
		`e.GET("/settings/exports/bookmarks.csv", s.exportBookmarksCSV)`,
		`e.GET("/settings/imports.:format", s.settingsImportsPage)`,
		`e.GET("/settings/imports", s.settingsImportsPage)`,
		`e.POST("/settings/imports.:format", s.createSettingsImport)`,
		`e.POST("/settings/imports", s.createSettingsImport)`,
		`e.GET("/settings/imports/:id.:format", optionalFormatPathParam("id", s.showSettingsImport))`,
		`e.GET("/settings/imports/:id", s.showSettingsImport)`,
		`e.DELETE("/settings/imports/:id.:format", optionalFormatPathParam("id", s.destroySettingsImport))`,
		`e.DELETE("/settings/imports/:id", s.destroySettingsImport)`,
		`e.POST("/settings/imports/:id.:format", s.notFound)`,
		`e.POST("/settings/imports/:id", s.notFound)`,
		`e.POST("/settings/imports/:id/confirm.:format", optionalFormatPathParam("id", s.confirmSettingsImport))`,
		`e.POST("/settings/imports/:id/confirm", s.confirmSettingsImport)`,
		`e.GET("/settings/imports/:id/failures.:format", optionalFormatPathParam("id", s.settingsImportFailuresCSV))`,
		`e.GET("/settings/imports/:id/failures", s.settingsImportFailuresCSV)`,
		`e.GET("/settings/imports/:id/failures.csv", s.settingsImportFailuresCSV)`,
		`e.GET("/settings/applications.:format", s.settingsApplicationsPage)`,
		`e.GET("/settings/applications", s.settingsApplicationsPage)`,
		`e.POST("/settings/applications.:format", s.createSettingsApplication)`,
		`e.POST("/settings/applications", s.createSettingsApplication)`,
		`e.GET("/settings/applications/new.:format", s.newSettingsApplication)`,
		`e.GET("/settings/applications/new", s.newSettingsApplication)`,
		`e.GET("/settings/applications/:id.:format", optionalFormatPathParam("id", s.showSettingsApplication))`,
		`e.GET("/settings/applications/:id", s.showSettingsApplication)`,
		`e.PUT("/settings/applications/:id.:format", optionalFormatPathParam("id", s.updateSettingsApplication))`,
		`e.PUT("/settings/applications/:id", s.updateSettingsApplication)`,
		`e.PATCH("/settings/applications/:id.:format", optionalFormatPathParam("id", s.updateSettingsApplication))`,
		`e.PATCH("/settings/applications/:id", s.updateSettingsApplication)`,
		`e.DELETE("/settings/applications/:id.:format", optionalFormatPathParam("id", s.destroySettingsApplication))`,
		`e.DELETE("/settings/applications/:id", s.destroySettingsApplication)`,
		`e.POST("/settings/applications/:id.:format", s.notFound)`,
		`e.POST("/settings/applications/:id", s.notFound)`,
		`e.POST("/settings/applications/:id/regenerate.:format", optionalFormatPathParam("id", s.regenerateSettingsApplicationToken))`,
		`e.POST("/settings/applications/:id/regenerate", s.regenerateSettingsApplicationToken)`,
		`e.GET("/settings/delete.:format", s.settingsDeletePage)`,
		`e.GET("/settings/delete", s.settingsDeletePage)`,
		`e.DELETE("/settings/delete.:format", s.destroyOwnAccount)`,
		`e.DELETE("/settings/delete", s.destroyOwnAccount)`,
		`e.POST("/settings/delete.:format", s.notFound)`,
		`e.POST("/settings/delete", s.notFound)`,
		`e.GET("/settings/migration.:format", s.settingsMigrationPage)`,
		`e.GET("/settings/migration", s.settingsMigrationPage)`,
		`e.POST("/settings/migration.:format", s.createSettingsMigration)`,
		`e.POST("/settings/migration", s.createSettingsMigration)`,
		`e.GET("/settings/migration/redirect/new.:format", s.newSettingsMigrationRedirect)`,
		`e.GET("/settings/migration/redirect/new", s.newSettingsMigrationRedirect)`,
		`e.POST("/settings/migration/redirect.:format", s.createSettingsMigrationRedirect)`,
		`e.POST("/settings/migration/redirect", s.createSettingsMigrationRedirect)`,
		`e.DELETE("/settings/migration/redirect.:format", s.destroySettingsMigrationRedirect)`,
		`e.DELETE("/settings/migration/redirect", s.destroySettingsMigrationRedirect)`,
		`e.GET("/settings/verification.:format", s.settingsVerificationPage)`,
		`e.GET("/settings/verification", s.settingsVerificationPage)`,
		`e.GET("/settings/aliases.:format", s.settingsAliasesPage)`,
		`e.GET("/settings/aliases", s.settingsAliasesPage)`,
		`e.POST("/settings/aliases.:format", s.createSettingsAlias)`,
		`e.POST("/settings/aliases", s.createSettingsAlias)`,
		`e.DELETE("/settings/aliases/:id.:format", optionalFormatPathParam("id", s.destroySettingsAlias))`,
		`e.DELETE("/settings/aliases/:id", s.destroySettingsAlias)`,
		`e.POST("/settings/aliases/:id.:format", s.notFound)`,
		`e.POST("/settings/aliases/:id", s.notFound)`,
		`e.GET("/settings/featured_tags.:format", s.settingsFeaturedTagsPage)`,
		`e.GET("/settings/featured_tags", s.settingsFeaturedTagsPage)`,
		`e.POST("/settings/featured_tags.:format", s.createSettingsFeaturedTag)`,
		`e.POST("/settings/featured_tags", s.createSettingsFeaturedTag)`,
		`e.DELETE("/settings/featured_tags/:id.:format", optionalFormatPathParam("id", s.destroySettingsFeaturedTag))`,
		`e.DELETE("/settings/featured_tags/:id", s.destroySettingsFeaturedTag)`,
		`e.POST("/settings/featured_tags/:id.:format", s.notFound)`,
		`e.POST("/settings/featured_tags/:id", s.notFound)`,
		`e.GET("/settings/login_activities.:format", s.settingsLoginActivitiesPage)`,
		`e.GET("/settings/login_activities", s.settingsLoginActivitiesPage)`,
		`e.DELETE("/settings/sessions/:id.:format", optionalFormatPathParam("id", s.destroySettingsSession))`,
		`e.DELETE("/settings/sessions/:id", s.destroySettingsSession)`,
		`e.POST("/settings/sessions/:id.:format", s.notFound)`,
		`e.POST("/settings/sessions/:id", s.notFound)`,
		`e.GET("/settings/two_factor_authentication_methods.:format", s.settingsPage)`,
		`e.GET("/settings/two_factor_authentication_methods", s.settingsPage)`,
		`e.POST("/settings/two_factor_authentication_methods/disable.:format", s.disableSettingsTwoFactor)`,
		`e.POST("/settings/two_factor_authentication_methods/disable", s.disableSettingsTwoFactor)`,
		`e.GET("/settings/otp_authentication.:format", s.settingsOTPAuthenticationPage)`,
		`e.GET("/settings/otp_authentication", s.settingsOTPAuthenticationPage)`,
		`e.POST("/settings/otp_authentication.:format", s.createSettingsOTPAuthentication)`,
		`e.POST("/settings/otp_authentication", s.createSettingsOTPAuthentication)`,
		`e.GET("/settings/two_factor_authentication/confirmation/new.:format", s.newSettingsTwoFactorConfirmation)`,
		`e.GET("/settings/two_factor_authentication/confirmation/new", s.newSettingsTwoFactorConfirmation)`,
		`e.POST("/settings/two_factor_authentication/confirmation.:format", s.createSettingsTwoFactorConfirmation)`,
		`e.POST("/settings/two_factor_authentication/confirmation", s.createSettingsTwoFactorConfirmation)`,
		`e.POST("/settings/two_factor_authentication/recovery_codes.:format", s.createSettingsRecoveryCodes)`,
		`e.POST("/settings/two_factor_authentication/recovery_codes", s.createSettingsRecoveryCodes)`,
		`e.GET("/settings/security_keys.:format", s.settingsSecurityKeysPage)`,
		`e.GET("/settings/security_keys", s.settingsSecurityKeysPage)`,
		`e.GET("/settings/security_keys/new.:format", s.newSettingsSecurityKey)`,
		`e.GET("/settings/security_keys/new", s.newSettingsSecurityKey)`,
		`e.GET("/settings/security_keys/options.:format", s.settingsSecurityKeyOptions)`,
		`e.GET("/settings/security_keys/options", s.settingsSecurityKeyOptions)`,
		`e.POST("/settings/security_keys.:format", s.createSettingsSecurityKey)`,
		`e.POST("/settings/security_keys", s.createSettingsSecurityKey)`,
		`e.DELETE("/settings/security_keys/:id.:format", optionalFormatPathParam("id", s.destroySettingsSecurityKey))`,
		`e.DELETE("/settings/security_keys/:id", s.destroySettingsSecurityKey)`,
		`e.POST("/settings/security_keys/:id.:format", s.notFound)`,
		`e.POST("/settings/security_keys/:id", s.notFound)`,
	}
	for _, route := range routes {
		if !strings.Contains(string(src), route) {
			t.Fatalf("server.go missing Rails settings web route %q", route)
		}
	}
	for _, forbidden := range []string{
		`e.GET("/admin/export_domain_blocks/export", s.exportAdminDomainBlocksCSV)`,
		`e.GET("/admin/export_domain_allows/export", s.exportAdminDomainAllowsCSV)`,
	} {
		if strings.Contains(string(src), forbidden) {
			t.Fatalf("server.go must not expose Rails CSV-constrained route without .csv: %q", forbidden)
		}
	}
}
