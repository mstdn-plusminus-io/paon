package api

import "testing"

func TestMastodon46ActivityPubMediaDurationIsISO8601(t *testing.T) {
	for _, test := range []struct {
		value any
		want  string
	}{
		{12.5, "PT12.5S"}, {3, "PT3S"}, {"2.250", "PT2.25S"}, {0, ""}, {-1, ""},
	} {
		if got := activityPubMediaDuration(test.value); got != test.want {
			t.Fatalf("activityPubMediaDuration(%#v) = %q, want %q", test.value, got, test.want)
		}
	}
}
