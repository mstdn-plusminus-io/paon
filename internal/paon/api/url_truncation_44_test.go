package api

import (
	"strings"
	"testing"
)

func TestMastodon44URLTruncationAccountsForEllipsis(t *testing.T) {
	for _, test := range []struct {
		name        string
		rest        string
		wantDisplay string
		wantSuffix  string
		wantCutoff  bool
	}{
		{name: "thirty", rest: strings.Repeat("a", 30), wantDisplay: strings.Repeat("a", 30)},
		{name: "one character suffix", rest: strings.Repeat("a", 30) + "b", wantDisplay: strings.Repeat("a", 30) + "b"},
		{name: "two character suffix", rest: strings.Repeat("a", 30) + "bc", wantDisplay: strings.Repeat("a", 30), wantSuffix: "bc", wantCutoff: true},
		{name: "unicode characters", rest: strings.Repeat("界", 31), wantDisplay: strings.Repeat("界", 31)},
	} {
		t.Run(test.name, func(t *testing.T) {
			display, suffix, cutoff := activityPubConvertedURLDisplay(test.rest)
			if display != test.wantDisplay || suffix != test.wantSuffix || cutoff != test.wantCutoff {
				t.Fatalf("display=%q suffix=%q cutoff=%v", display, suffix, cutoff)
			}
		})
	}
}

func TestStatusEmbedURLUsesMastodon44URLTruncation(t *testing.T) {
	short := statusEmbedURLLinkHTML("https://" + strings.Repeat("a", 31))
	if strings.Contains(short, `class="ellipsis"`) || !strings.Contains(short, strings.Repeat("a", 31)) {
		t.Fatalf("31-character display URL was truncated: %s", short)
	}
	long := statusEmbedURLLinkHTML("https://" + strings.Repeat("a", 32))
	if !strings.Contains(long, `class="ellipsis"`) || !strings.Contains(long, `<span class="invisible">aa</span>`) {
		t.Fatalf("32-character display URL was not truncated: %s", long)
	}
}
