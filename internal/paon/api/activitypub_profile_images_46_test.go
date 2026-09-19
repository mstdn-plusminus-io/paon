package api

import (
	"strings"
	"testing"
)

func TestMastodon46ActivityPubProfileImageDescription(t *testing.T) {
	if got := activityActorImageDescription(map[string]any{"type": "Image", "summary": " Avatar description ", "name": "fallback"}); got != "Avatar description" {
		t.Fatalf("summary description = %q", got)
	}
	if got := activityActorImageDescription([]any{map[string]any{"type": "Image", "name": "Header"}}); got != "Header" {
		t.Fatalf("name description = %q", got)
	}
	long := strings.Repeat("界", activityPubMediaAttachmentMaxDescriptionLength+1)
	if got := activityActorImageDescription(map[string]any{"type": "Image", "summary": long}); len([]rune(got)) != activityPubMediaAttachmentMaxDescriptionLength {
		t.Fatalf("description length = %d", len([]rune(got)))
	}
	if got := activityActorImageDescription(map[string]any{"type": "Document", "summary": "ignored"}); got != "" {
		t.Fatalf("non-image description = %q", got)
	}
}
