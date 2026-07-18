package parity

import (
	"strings"
	"testing"
)

func TestAuditRoutesMapsOptionalFormatsAndRecordsAcceptedControllers(t *testing.T) {
	rails := []RailsRoute{
		{Methods: []string{"GET"}, Path: "/api/v1/accounts/:id(.:format)", Controller: "api/v1/accounts", Action: "show"},
		{Methods: []string{"POST"}, Path: "/api/v1/crypto/keys", Controller: "api/v1/crypto/keys", Action: "create"},
		{Methods: []string{"GET"}, Path: "/missing(.:format)", Controller: "missing", Action: "show"},
	}
	goRoutes := []GoRoute{{Method: "GET", Path: "/api/v1/accounts/:id", Handler: "GET:/api/v1/accounts/:id", Contract: "REST-API"}}
	audit := AuditRoutes(rails, goRoutes, []AcceptedRoute{{Controller: "api/v1/crypto/keys", Reason: "disabled upstream crypto API"}})
	if len(audit.Mapped) != 1 || audit.Mapped[0].GoPath != "/api/v1/accounts/:id" {
		t.Fatalf("mapped = %#v", audit.Mapped)
	}
	if len(audit.Accepted) != 1 || audit.Accepted[0].AcceptedWhy == "" {
		t.Fatalf("accepted = %#v", audit.Accepted)
	}
	if len(audit.Unmapped) != 1 || audit.Unmapped[0].Controller != "missing" {
		t.Fatalf("unmapped = %#v", audit.Unmapped)
	}
}

func TestRouteManifestLoadersRejectUnknownFields(t *testing.T) {
	if _, err := LoadRailsRoutes(strings.NewReader(`[{"methods":["GET"],"path":"/","controller":"home","action":"show","defaults":{},"constraints":{},"extra":true}]`)); err == nil {
		t.Fatal("LoadRailsRoutes accepted an unknown field")
	}
	if _, err := LoadGoRoutes(strings.NewReader(`[{"method":"GET","path":"/","handler":"home","contract":"PUBLIC-WEB","extra":true}]`)); err == nil {
		t.Fatal("LoadGoRoutes accepted an unknown field")
	}
}
