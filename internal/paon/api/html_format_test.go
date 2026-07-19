package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestHTMLOnlyOptionalFormatAllowsHTMLAndEmptyFormat(t *testing.T) {
	for _, path := range []string{"/unsubscribe", "/unsubscribe.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		c := echo.NewContext(req, rec, echo.New())
		if err := requireHTMLOnlyOptionalFormat(c); err != nil {
			t.Fatalf("%s requireHTMLOnlyOptionalFormat error = %v", path, err)
		}
	}
}
