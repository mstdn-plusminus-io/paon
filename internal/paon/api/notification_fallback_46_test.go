package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestNotificationFallbackHonorsSupportedTypesContract(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "social.example", LocalDomain: "example"}}
	notification := models.Notification{
		ID: 1, Type: "admin.sign_up", ActivityID: 10, CreatedAt: time.Now().UTC(),
		FromAccount: models.Account{ID: 10, Username: "alice", CreatedAt: time.Now().UTC()},
	}

	contextFor := func(rawQuery string) *echo.Context {
		req := httptest.NewRequest("GET", "/api/v1/notifications"+rawQuery, nil)
		return echo.NewContext(req, httptest.NewRecorder(), echo.New())
	}
	if got := server.notificationFallback(contextFor(""), notification, nil); got != nil {
		t.Fatalf("fallback without supported_types = %#v", got)
	}
	got := server.notificationFallback(contextFor("?supported_types%5B%5D=mention"), notification, nil)
	if got == nil || got.Title == "" || got.Summary == "" || got.Description != nil {
		t.Fatalf("fallback = %#v", got)
	}
	if got := server.notificationFallback(contextFor("?supported_types%5B%5D=admin.sign_up"), notification, nil); got != nil {
		t.Fatalf("client-supported notification got fallback %#v", got)
	}

	notification.Type = "mention"
	if got := server.notificationFallback(contextFor("?supported_types%5B%5D="), notification, nil); got != nil {
		t.Fatalf("baseline notification got fallback %#v", got)
	}
}

func TestNotificationPaginationPreservesSupportedTypes(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/notifications?limit=2&supported_types%5B%5D=mention", nil)
	req.Host = "social.example"
	c := echo.NewContext(req, httptest.NewRecorder(), echo.New())
	if got := notificationPaginationLink(c, 20, 10); !strings.Contains(got, "supported_types%5B%5D=mention") {
		t.Fatalf("v1 pagination link = %q", got)
	}
	if got := notificationV2PaginationLink(c, 20, 10); !strings.Contains(got, "supported_types%5B%5D=mention") {
		t.Fatalf("v2 pagination link = %q", got)
	}
}
