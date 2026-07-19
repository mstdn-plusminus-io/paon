package api

import (
	"sort"
	"strings"
)

type RouteManifestEntry struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Handler  string `json:"handler"`
	Contract string `json:"contract"`
}

func (s *Server) RouteManifest() []RouteManifestEntry {
	if s == nil || s.echo == nil || s.echo.Router() == nil {
		return nil
	}
	routes := s.echo.Router().Routes()
	manifest := make([]RouteManifestEntry, 0, len(routes))
	for _, route := range routes {
		manifest = append(manifest, RouteManifestEntry{
			Method:   route.Method,
			Path:     route.Path,
			Handler:  route.Name,
			Contract: routeContractOwner(route.Path),
		})
	}
	sort.Slice(manifest, func(i, j int) bool {
		if manifest[i].Path == manifest[j].Path {
			return manifest[i].Method < manifest[j].Method
		}
		return manifest[i].Path < manifest[j].Path
	})
	return manifest
}

func routeContractOwner(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/v1/streaming"):
		return "STREAMING-HTTP"
	case strings.HasPrefix(path, "/api/") || path == "/api":
		return "REST-API"
	case strings.HasPrefix(path, "/oauth/"):
		return "OAUTH"
	case strings.HasPrefix(path, "/admin") || strings.HasPrefix(path, "/asynq") || strings.HasPrefix(path, "/sidekiq") || strings.HasPrefix(path, "/pghero"):
		return "ADMIN-WEB"
	case strings.HasPrefix(path, "/settings") || strings.HasPrefix(path, "/filters") || strings.HasPrefix(path, "/lists") || strings.HasPrefix(path, "/invites") || strings.HasPrefix(path, "/backups") || strings.HasPrefix(path, "/relationships") || strings.HasPrefix(path, "/disputes"):
		return "SETTINGS-WEB"
	case strings.HasPrefix(path, "/auth") || strings.HasPrefix(path, "/users/auth") || strings.HasPrefix(path, "/unsubscribe"):
		return "AUTH-WEB"
	case strings.HasPrefix(path, "/.well-known") || strings.HasPrefix(path, "/users/") || strings.Contains(path, "/inbox") || strings.Contains(path, "/outbox") || strings.Contains(path, "/followers") || strings.Contains(path, "/following") || strings.Contains(path, "/collections/"):
		return "ACTIVITYPUB"
	case strings.HasPrefix(path, "/health"):
		return "RUNTIME-HEALTH"
	default:
		return "PUBLIC-WEB"
	}
}
