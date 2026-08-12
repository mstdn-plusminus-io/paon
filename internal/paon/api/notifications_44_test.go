package api

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestNotificationGroupedTypesIncludeMastodon44AdminSignUps(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v2/notifications", nil)
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	defaults := notificationGroupedTypes(c)
	for _, kind := range []string{"favourite", "reblog", "follow", "admin.sign_up"} {
		if _, ok := defaults[kind]; !ok {
			t.Fatalf("default grouped notification types omit %q: %#v", kind, defaults)
		}
	}

	req = httptest.NewRequest("GET", "/api/v2/notifications?grouped_types%5B%5D=admin.sign_up&grouped_types%5B%5D=mention", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), echo.New())
	explicit := notificationGroupedTypes(c)
	if len(explicit) != 1 {
		t.Fatalf("explicit grouped notification types = %#v, want admin.sign_up only", explicit)
	}
	if _, ok := explicit["admin.sign_up"]; !ok {
		t.Fatalf("explicit grouped notification types omit admin.sign_up: %#v", explicit)
	}
}
