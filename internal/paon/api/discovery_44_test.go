package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestOAuthMetadataAdvertisesMastodon44GrantTypes(t *testing.T) {
	recorder := httptest.NewRecorder()
	context := echo.NewContext(
		httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil),
		recorder,
		echo.New(),
	)
	server := &Server{cfg: config.Config{Scheme: "https", LocalDomain: "example.test", WebDomain: "example.test"}}

	if err := server.oauthAuthorizationServerMetadata(context); err != nil {
		t.Fatal(err)
	}
	var body struct {
		GrantTypes       []string `json:"grant_types_supported"`
		UserInfoEndpoint string   `json:"userinfo_endpoint"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []string{"authorization_code", "client_credentials"}
	if !reflect.DeepEqual(body.GrantTypes, want) {
		t.Fatalf("grant_types_supported = %#v, want %#v", body.GrantTypes, want)
	}
	if body.UserInfoEndpoint != "https://example.test/oauth/userinfo" {
		t.Fatalf("userinfo_endpoint = %q", body.UserInfoEndpoint)
	}
}
