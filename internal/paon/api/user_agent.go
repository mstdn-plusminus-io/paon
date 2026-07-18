package api

import (
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

const railsHTTPRequestUserAgent = "http.rb/5.1.1"

func paonUserAgent(cfg config.Config) string {
	paonVersion := strings.TrimSpace(cfg.Version)
	if paonVersion == "" {
		paonVersion = "6.0.2"
	}
	mastodonVersion := strings.TrimSpace(cfg.MastodonVersion)
	if mastodonVersion == "" {
		mastodonVersion = "4.2.27"
	}
	return railsHTTPRequestUserAgent + " (Paon/" + paonVersion + "; based Mastodon/" + mastodonVersion + "; +" + strings.TrimRight(cfg.BaseURL(), "/") + "/)"
}
