package api

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestRailsTrustedProxyIPExtractorUsesDefaultTrustedProxiesWhenUnset(t *testing.T) {
	extractor := railsTrustedProxyIPExtractor(config.Config{})
	req := httptestRequestWithRemote("10.0.0.5:443")
	req.Header.Set(echo.HeaderXForwardedFor, "198.51.100.44")
	if got := extractor(req); got != "198.51.100.44" {
		t.Fatalf("default private proxy trust real IP = %q", got)
	}

	realIP := httptestRequestWithRemote("127.0.0.1:3000")
	realIP.Header.Set(echo.HeaderXRealIP, "198.51.100.45")
	if got := extractor(realIP); got != "198.51.100.45" {
		t.Fatalf("trusted X-Real-IP fallback = %q", got)
	}
}

func httptestRequestWithRemote(remote string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	return req
}
