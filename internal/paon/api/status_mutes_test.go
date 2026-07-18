package api

import (
	"os"
	"strings"
	"testing"
)

func TestToggleStatusMuteHydratesFullStatusResponse(t *testing.T) {
	raw, err := os.ReadFile("status_mutes.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		`if err := s.hydrateStatusRelationship(status, account); err != nil {`,
		`return c.JSON(http.StatusOK, statusWithFilterContext(s.cfg, *status, account, s.accountFilters(account), "public"))`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("toggleStatusMute missing %q", want)
		}
	}
}
