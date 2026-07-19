package api

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestMarkerTimelineParamsMatchesRailsArrayParams(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/markers?timeline[]=notifications&timeline=home&timeline=home&timeline=home,notifications", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got := markerTimelineParams(c)
	want := []string{"notifications", "home"}
	if len(got) != len(want) {
		t.Fatalf("len = %d: %#v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timelines = %#v, want %#v", got, want)
		}
	}
}

func TestMarkerTimelineParamsDoesNotSplitCommaValuesLikeRails(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("GET", "/api/v1/markers?timeline=home,notifications", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if got := markerTimelineParams(c); len(got) != 0 {
		t.Fatalf("comma-separated timeline should be treated as one invalid Rails param: %#v", got)
	}
}

func TestParseMarkerPayloadAcceptsJSON(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/markers", strings.NewReader(`{
		"home":{"last_read_id":"10"},
		"notifications":{"last_read_id":"20"},
		"unknown":{"last_read_id":"30"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseMarkerPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if markerLastReadIDValue(payload["home"]) != "10" || markerLastReadIDValue(payload["notifications"]) != "20" {
		t.Fatalf("payload = %#v", payload)
	}
	if _, ok := payload["unknown"]; ok {
		t.Fatalf("unexpected unknown timeline: %#v", payload)
	}
}

func TestParseMarkerPayloadAcceptsBeaconForm(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/markers", strings.NewReader("home%5Blast_read_id%5D=10&notifications%5Blast_read_id%5D=20"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseMarkerPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if markerLastReadIDValue(payload["home"]) != "10" || markerLastReadIDValue(payload["notifications"]) != "20" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestParseMarkerPayloadKeepsExplicitBlankLastReadID(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/markers", strings.NewReader("home%5Blast_read_id%5D="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	payload, err := parseMarkerPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	marker, ok := payload["home"]
	if !ok || marker.LastReadID == nil || *marker.LastReadID != "" {
		t.Fatalf("payload = %#v", payload)
	}
}

func markerLastReadIDValue(payload markerTimelinePayload) string {
	if payload.LastReadID == nil {
		return "<nil>"
	}
	return *payload.LastReadID
}

func TestParseMarkerLastReadIDAllowsNegativeLikeRailsModel(t *testing.T) {
	got, err := parseMarkerLastReadID("-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != -1 {
		t.Fatalf("parseMarkerLastReadID = %d, want -1", got)
	}

	if _, err := parseMarkerLastReadID("foo"); err == nil {
		t.Fatal("parseMarkerLastReadID accepted non-numeric value")
	}
}

func TestUpdateMarkersUsesRailsOptimisticLockingConflict(t *testing.T) {
	src, err := os.ReadFile("markers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`errors.Is(err, gorm.ErrRecordNotFound)`,
		`previousLockVersion := marker.LockVersion`,
		`Where("id = ? AND lock_version = ?", marker.ID, previousLockVersion)`,
		`if result.RowsAffected == 0 {`,
		`return errMarkerStaleObject`,
		`return apiError(c, http.StatusConflict, "Conflict during update, please try again")`,
	} {
		if !functionBodyContains(t, src, "updateMarkers", want) && !strings.Contains(string(src), want) {
			t.Fatalf("updateMarkers missing Rails optimistic locking behavior %q", want)
		}
	}
	if functionBodyContains(t, src, "updateMarkers", `err == gorm.ErrRecordNotFound`) {
		t.Fatal("updateMarkers must tolerate wrapped gorm.ErrRecordNotFound")
	}
}
