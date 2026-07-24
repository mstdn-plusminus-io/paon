package api

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func remoteTaskTargetHost(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return "unknown"
	}
	return strings.ToLower(parsed.Hostname())
}

func localTaskTargetHost(cfg config.Config) string {
	host := strings.TrimSpace(firstNonEmpty(cfg.LocalDomain, cfg.WebDomain))
	if host == "" {
		return "localhost"
	}
	return strings.ToLower(host)
}

func serverLocalTaskTargetHost(server *Server) string {
	if server == nil {
		return "localhost"
	}
	return localTaskTargetHost(server.cfg)
}

func taskTargetError(operation string, target string, host string, err error) error {
	if err == nil {
		return nil
	}
	if strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	return fmt.Errorf("%s target=%s host=%q: %w", operation, target, host, err)
}
