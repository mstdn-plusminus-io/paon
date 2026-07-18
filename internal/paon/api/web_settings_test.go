package api

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestParseWebSettingsPayloadKeepsDataObject(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PUT", "/api/web/settings", strings.NewReader(`{"data":{"boost_modal":true,"skin":"default"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseWebSettingsPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Data) != `{"boost_modal":true,"skin":"default"}` {
		t.Fatalf("Data = %s", payload.Data)
	}
}

func TestParseWebSettingsPayloadKeepsJSONBooleanTypesLikeRailsController(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PUT", "/api/web/settings", strings.NewReader(`{"data":{"onboarded":true,"notifications":{"follow":false}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseWebSettingsPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Data) != `{"notifications":{"follow":false},"onboarded":true}` {
		t.Fatalf("Data = %s", payload.Data)
	}
}

func TestParseWebSettingsPayloadDefaultsMissingDataToObject(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("PUT", "/api/web/settings", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseWebSettingsPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Data) != `{}` {
		t.Fatalf("Data = %s", payload.Data)
	}
}

func TestParseWebSettingsPayloadAcceptsFormJSONData(t *testing.T) {
	e := echo.New()
	form := url.Values{}
	form.Set("data", `{"boost_modal":false,"skin":"contrast"}`)
	req := httptest.NewRequest("PUT", "/api/web/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseWebSettingsPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Data) != `{"boost_modal":false,"skin":"contrast"}` {
		t.Fatalf("Data = %s", payload.Data)
	}
}

func TestParseWebSettingsPayloadAcceptsRailsNestedFormData(t *testing.T) {
	e := echo.New()
	form := url.Values{}
	form.Set("data[notifications][alerts][follow]", "true")
	form.Set("data[notifications][shows][reblog]", "false")
	form.Set("data[skin]", "default")
	req := httptest.NewRequest("PUT", "/api/web/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseWebSettingsPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"notifications":{"alerts":{"follow":"true"},"shows":{"reblog":"false"}},"skin":"default"}`
	if string(payload.Data) != want {
		t.Fatalf("Data = %s", payload.Data)
	}
}

func TestDecodeWebSettings(t *testing.T) {
	settings := decodeWebSettings([]byte(`{"boost_modal":true,"skin":"default"}`))
	if settings["boost_modal"] != true || settings["skin"] != "default" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestDecodeWebSettingsDefaultsInvalidDataToObject(t *testing.T) {
	settings := decodeWebSettings([]byte(`[`))
	if len(settings) != 0 {
		t.Fatalf("settings = %#v", settings)
	}
}
