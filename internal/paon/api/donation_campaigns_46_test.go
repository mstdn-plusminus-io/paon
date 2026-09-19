package api

import (
	"net/url"
	"testing"
)

func TestRubyDonationCampaignSeedMatchesMastodon(t *testing.T) {
	for _, test := range []struct {
		seed int64
		want int
	}{
		{1, 37}, {2, 40}, {7, 47}, {42, 51}, {123, 66}, {999999999, 71},
		{109000000000000000, 27},
	} {
		if got := rubyRandomPercent(test.seed); got != test.want {
			t.Fatalf("Random.new(%d).rand(100) = %d, want %d", test.seed, got, test.want)
		}
	}
}

func TestDonationCampaignRequestURLMatchesUpstreamQuery(t *testing.T) {
	raw, err := donationCampaignRequestURL("https://campaign.example/api?keep=1", 47, "ja", "production")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"keep": "1", "platform": "web", "seed": "47", "locale": "ja", "environment": "production"}
	for key, value := range want {
		if parsed.Query().Get(key) != value {
			t.Fatalf("query %s = %q, want %q", key, parsed.Query().Get(key), value)
		}
	}
}
