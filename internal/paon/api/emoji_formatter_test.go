package api

import (
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestActivityPubStatusMetadataKeepsRESTEmojiShortcodes(t *testing.T) {
	src, err := os.ReadFile("activitypub_inbox.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, function := range []string{"saveActivityPubStatusMetadata", "replaceActivityPubStatusMetadata"} {
		if !functionBodyContains(t, src, function, `s.hydrateActivityPubStatusCustomEmojis(tx, status, actor)`) {
			t.Fatalf("%s must hydrate REST emoji metadata without rewriting status text", function)
		}
		if functionBodyContains(t, src, function, `applyActivityPubCustomEmojisToContent`) {
			t.Fatalf("%s must not persist custom emoji img tags into status text", function)
		}
	}
}

func emojiSrcStub(models.CustomEmoji) string { return "https://cdn.example/emoji.png" }

func TestApplyCustomEmojisToContentReplacesKnownShortcode(t *testing.T) {
	emojis := map[string]models.CustomEmoji{"blob": {Shortcode: "blob"}}
	out := applyCustomEmojisToContent(`<p>hello :blob: world</p>`, emojis, emojiSrcStub)
	if !strings.Contains(out, `<img draggable="false" class="emojione"`) || !strings.Contains(out, `alt=":blob:"`) || !strings.Contains(out, `src="https://cdn.example/emoji.png"`) {
		t.Fatalf("shortcode must be replaced with img tag, got %q", out)
	}
	// The shortcode must be consumed as visible text (replaced by the img). The img tag's
	// alt/title intentionally still contain :blob: like Mastodon, so only assert the token
	// is gone from the surrounding text.
	if strings.Contains(out, "hello :blob:") || strings.Contains(out, ":blob: world") {
		t.Fatalf("shortcode text token must be consumed, got %q", out)
	}
}

func TestApplyCustomEmojisToContentRespectsBounding(t *testing.T) {
	emojis := map[string]models.CustomEmoji{"blob": {Shortcode: "blob"}}
	for _, in := range []string{`<p>a:blob: b</p>`, `<p>:blob:b</p>`, `<p>x:blob:y</p>`} {
		out := applyCustomEmojisToContent(in, emojis, emojiSrcStub)
		if !strings.Contains(out, ":blob:") {
			t.Fatalf("bounded shortcode must be preserved for %q, got %q", in, out)
		}
	}
}

func TestApplyCustomEmojisToContentKeepsUnknownShortcode(t *testing.T) {
	out := applyCustomEmojisToContent(`<p>:nope:</p>`, map[string]models.CustomEmoji{"blob": {Shortcode: "blob"}}, emojiSrcStub)
	if !strings.Contains(out, ":nope:") {
		t.Fatalf("unknown shortcode must be preserved, got %q", out)
	}
}

func TestApplyCustomEmojisToContentSkipsAttributeValues(t *testing.T) {
	emojis := map[string]models.CustomEmoji{"blob": {Shortcode: "blob"}}
	out := applyCustomEmojisToContent(`<a href=":blob:">:blob:</a>`, emojis, emojiSrcStub)
	if !strings.Contains(out, `href=":blob:"`) {
		t.Fatalf("href shortcode must not be replaced, got %q", out)
	}
	if strings.Contains(out, ">:blob:<") {
		t.Fatalf("text-node shortcode must be replaced, got %q", out)
	}
}
