package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestMastodon45GatedTimelineRequiresAuthenticatedUserWith422(t *testing.T) {
	keys := []string{"local_live_feed_access", "remote_live_feed_access"}
	previous := make(map[string]string, len(keys))
	for _, key := range keys {
		previous[key] = railsSettingDefaults[key]
		railsSettingDefaults[key] = timelineAccessAuthenticated
	}
	t.Cleanup(func() {
		for _, key := range keys {
			railsSettingDefaults[key] = previous[key]
		}
	})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/timelines/public", nil)
	context := echo.NewContext(request, httptest.NewRecorder(), echo.New())
	_, err := (&Server{}).timelineAccessForRequest(context, false, false, false)
	apiErr, ok := err.(apiHTTPError)
	if !ok || apiErr.status != http.StatusUnprocessableEntity || apiErr.message != "This method requires an authenticated user" {
		t.Fatalf("gated anonymous timeline error = %#v", err)
	}
}
