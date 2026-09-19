package api

import (
	"os"
	"strings"
	"testing"
)

func TestMastodon46PublicWrapstodonRouteAndShareKeyGuard(t *testing.T) {
	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serverSource), `e.GET("/@:username/wrapstodon/:year/:share_key", s.publicWrapstodon)`) {
		t.Fatal("public Wrapstodon route is missing")
	}
	source, err := os.ReadFile("public_wrapstodon_46.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "publicWrapstodon")
	for _, want := range []string{
		`Where("account_id = ? AND year = ? AND share_key = ?"`,
		`"X-Robots-Tag", "noindex, noarchive"`,
		`id="wrapstodon-data"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("publicWrapstodon missing %q: %s", want, body)
		}
	}
}
