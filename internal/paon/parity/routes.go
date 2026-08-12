package parity

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

type RailsRoute struct {
	Name        string            `json:"name"`
	Methods     []string          `json:"methods"`
	Path        string            `json:"path"`
	Controller  string            `json:"controller"`
	Action      string            `json:"action"`
	Defaults    map[string]string `json:"defaults"`
	Constraints map[string]string `json:"constraints"`
}

type GoRoute struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Handler  string `json:"handler"`
	Contract string `json:"contract"`
}

type AcceptedRoute struct {
	Controller string `json:"controller"`
	Reason     string `json:"reason"`
}

type RouteAudit struct {
	Mapped   []RouteMapping `json:"mapped"`
	Accepted []RouteMapping `json:"accepted"`
	Unmapped []RouteMapping `json:"unmapped"`
	GoOnly   []GoRoute      `json:"go_only"`
}

type RouteMapping struct {
	Method      string `json:"method"`
	RailsPath   string `json:"rails_path"`
	GoPath      string `json:"go_path,omitempty"`
	Controller  string `json:"controller,omitempty"`
	Action      string `json:"action,omitempty"`
	Contract    string `json:"contract,omitempty"`
	AcceptedWhy string `json:"accepted_reason,omitempty"`
}

func LoadRailsRoutes(reader io.Reader) ([]RailsRoute, error) {
	var routes []RailsRoute
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&routes); err != nil {
		return nil, err
	}
	return routes, nil
}

func LoadGoRoutes(reader io.Reader) ([]GoRoute, error) {
	var routes []GoRoute
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&routes); err != nil {
		return nil, err
	}
	return routes, nil
}

func AuditRoutes(rails []RailsRoute, goRoutes []GoRoute, accepted []AcceptedRoute) RouteAudit {
	goByKey := make(map[string]GoRoute, len(goRoutes))
	for _, route := range goRoutes {
		if !isRealHTTPMethod(route.Method) {
			continue
		}
		goByKey[routeKey(route.Method, route.Path)] = route
	}
	acceptedControllers := make(map[string]string, len(accepted))
	for _, item := range accepted {
		if item.Controller != "" && item.Reason != "" {
			acceptedControllers[item.Controller] = item.Reason
		}
	}
	usedGo := make(map[string]bool, len(goRoutes))
	result := RouteAudit{}
	for _, railsRoute := range rails {
		paths := railsPathCandidates(railsRoute)
		for _, method := range railsRoute.Methods {
			mapping := RouteMapping{Method: method, RailsPath: railsRoute.Path, Controller: railsRoute.Controller, Action: railsRoute.Action}
			for _, path := range paths {
				if route, exists := goByKey[routeKey(method, path)]; exists {
					mapping.GoPath = route.Path
					mapping.Contract = route.Contract
					usedGo[routeKey(method, route.Path)] = true
					break
				}
			}
			if mapping.GoPath != "" {
				result.Mapped = append(result.Mapped, mapping)
			} else if reason := acceptedControllers[railsRoute.Controller]; reason != "" {
				mapping.AcceptedWhy = reason
				result.Accepted = append(result.Accepted, mapping)
			} else {
				result.Unmapped = append(result.Unmapped, mapping)
			}
		}
	}
	for _, route := range goRoutes {
		key := routeKey(route.Method, route.Path)
		if isRealHTTPMethod(route.Method) && !usedGo[key] {
			result.GoOnly = append(result.GoOnly, route)
		}
	}
	sortMappings(result.Mapped)
	sortMappings(result.Accepted)
	sortMappings(result.Unmapped)
	sort.Slice(result.GoOnly, func(i, j int) bool {
		if result.GoOnly[i].Path == result.GoOnly[j].Path {
			return result.GoOnly[i].Method < result.GoOnly[j].Method
		}
		return result.GoOnly[i].Path < result.GoOnly[j].Path
	})
	return result
}

var railsOptionalFormat = regexp.MustCompile(`\(\.:format\)$`)
var railsRequiredFormat = regexp.MustCompile(`\.:format$`)
var railsOptionalWildcard = regexp.MustCompile(`\(/\*[A-Za-z_][A-Za-z0-9_]*\)$`)
var routeWildcardName = regexp.MustCompile(`\*[A-Za-z_][A-Za-z0-9_]*`)
var routeParameterName = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)
var literalRouteFormat = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func railsPathCandidates(route RailsRoute) []string {
	path := route.Path
	if format := route.Constraints["format"]; literalRouteFormat.MatchString(format) {
		path = railsOptionalFormat.ReplaceAllString(path, "")
		path = railsRequiredFormat.ReplaceAllString(path, "")
		return expandOptionalRailsWildcard(path + "." + format)
	}
	path = strings.TrimSuffix(path, "(.:format)")
	path = railsOptionalFormat.ReplaceAllString(path, "")
	candidates := expandOptionalRailsWildcard(path)
	if railsRequiredFormat.MatchString(path) {
		candidates = append(candidates, railsRequiredFormat.ReplaceAllString(path, ".:format"))
	}
	return candidates
}

func expandOptionalRailsWildcard(path string) []string {
	if !railsOptionalWildcard.MatchString(path) {
		return []string{path}
	}
	return []string{
		railsOptionalWildcard.ReplaceAllString(path, ""),
		railsOptionalWildcard.ReplaceAllString(path, "/*"),
	}
}

func routeKey(method, path string) string {
	path = routeWildcardName.ReplaceAllString(path, "*")
	path = routeParameterName.ReplaceAllStringFunc(path, func(parameter string) string {
		if parameter == ":format" {
			return parameter
		}
		return ":param"
	})
	return strings.ToUpper(method) + " " + path
}

func isRealHTTPMethod(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return true
	default:
		return false
	}
}

func sortMappings(values []RouteMapping) {
	sort.Slice(values, func(i, j int) bool {
		left := fmt.Sprintf("%s %s %s#%s", values[i].Method, values[i].RailsPath, values[i].Controller, values[i].Action)
		right := fmt.Sprintf("%s %s %s#%s", values[j].Method, values[j].RailsPath, values[j].Controller, values[j].Action)
		return left < right
	})
}
