package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestAnnouncementReactionCustomEmojiIDIsEmptyWithoutDatabase(t *testing.T) {
	got := (&Server{}).announcementReactionCustomEmojiID("party")
	if got.Valid {
		t.Fatalf("custom emoji id = %#v, want invalid", got)
	}
}

func TestAnnouncementReactionCustomEmojisHandlesEmptyRows(t *testing.T) {
	got, err := (&Server{}).announcementReactionCustomEmojis(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("emojis = %#v", got)
	}
}

func TestSupportedUnicodeReactionNameApproximatesRailsEmojiMap(t *testing.T) {
	cases := map[string]bool{
		"😀":       true,
		"👍🏻":      true,
		"👨‍👩‍👧‍👦": true,
		"1️⃣":     true,
		"☑":       true,
		"party":   false,
		":party:": false,
		"1":       false,
		"✓":       false,
		" 😀 ":     false,
		"":        false,
	}
	for name, want := range cases {
		if got := supportedUnicodeReactionName(name); got != want {
			t.Fatalf("supportedUnicodeReactionName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidateAnnouncementReactionAllowsCustomEmojiAndRejectsUnknownNames(t *testing.T) {
	if err := (&Server{}).validateAnnouncementReaction(1, "party", sql.NullInt64{Int64: 10, Valid: true}); err != nil {
		t.Fatalf("custom emoji should be valid: %v", err)
	}
	err := (&Server{}).validateAnnouncementReaction(1, "party", sql.NullInt64{})
	apiErr, ok := err.(apiHTTPError)
	if !ok || apiErr.status != http.StatusUnprocessableEntity || apiErr.message != "Validation failed: Name is not a recognized emoji" {
		t.Fatalf("err = %#v", err)
	}
}

func TestAnnouncementReferenceHydrationPathsAreWired(t *testing.T) {
	src, err := os.ReadFile("announcements.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "announcements", `if err := s.hydrateAnnouncementReferences(&announcement); err != nil`) {
		t.Fatal("announcements should hydrate references before serializing")
	}

	streaming, err := os.ReadFile("announcement_streaming.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, streaming, "broadcastAnnouncement", `_ = s.hydrateAnnouncementReferences(&announcement)`) {
		t.Fatal("broadcastAnnouncement should hydrate references before serializing")
	}
}

func TestAnnouncementsEarlyAuthErrorKeepsRailsAuthorizationVary(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/announcements", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Vary"); got != "Authorization" {
		t.Fatalf("Vary = %q, want Authorization", got)
	}
	if !strings.Contains(rec.Body.String(), "The access token is invalid") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAnnouncementReactionUpdateMatchesRailsCreateSemantics(t *testing.T) {
	src, err := os.ReadFile("announcements.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if err := s.validateAnnouncementReaction(announcement.ID, name, reaction.CustomEmojiID); err != nil`,
		`if result.RowsAffected == 0 {
		return apiError(c, http.StatusUnprocessableEntity, "Duplicate record")
	}`,
		`Distinct("name")`,
		`if count >= announcementReactionLimit`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("announcement reaction Rails semantics missing %q", want)
		}
	}
}

func TestAnnouncementReactionMeMatchesRailsNameScope(t *testing.T) {
	src, err := os.ReadFile("announcements.go")
	if err != nil {
		t.Fatal(err)
	}
	want := "Select(`name, custom_emoji_id, COUNT(*) AS count, EXISTS(SELECT 1 FROM announcement_reactions r WHERE r.account_id = ? AND r.announcement_id = ? AND r.name = announcement_reactions.name) AS me`, accountID, announcementID)"
	if !functionBodyContains(t, src, "announcementReactions", want) {
		t.Fatalf("announcementReactions should calculate me by a bound announcement/name like Rails, even when custom_emoji_id differs")
	}
	if !functionBodyContains(t, src, "announcementReactions", `!errors.Is(err, gorm.ErrRecordNotFound)`) {
		t.Fatal("announcementReactions must tolerate wrapped gorm.ErrRecordNotFound as an empty reaction set")
	}
	if functionBodyContains(t, src, "announcementReactions", `err != gorm.ErrRecordNotFound`) {
		t.Fatal("announcementReactions must not compare gorm.ErrRecordNotFound directly")
	}
}

func TestAnnouncementReactionBroadcastUsesRailsReactionPayload(t *testing.T) {
	src, err := os.ReadFile("announcement_streaming.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"broadcastAnnouncementReaction", `payload, err := s.announcementReactionStreamPayload(announcementID, name)`},
		{"announcementReactionStreamPayload", `Select("name, custom_emoji_id, COUNT(*) AS count")`},
		{"announcementReactionStreamPayload", `"announcement_id": strconv.FormatInt(announcementID, 10)`},
		{"announcementReactionStreamPayload", `emojis, err := s.announcementReactionCustomEmojis([]announcementReactionRow{row})`},
		{"announcementReactionStreamPayload", `serialized := serializer.CustomEmojiFromModel(s.cfg, emoji)`},
		{"announcementReactionStreamPayload", `payload["static_url"] = serialized.StaticURL`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("announcement_streaming.go:%s missing %q", check.fn, check.want)
		}
	}
}

func TestAnnouncementReactionUpdateRoutesMatchRails(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`e.PUT("/api/v1/announcements/:id/reactions/:name", s.addAnnouncementReaction)`,
		`e.PATCH("/api/v1/announcements/:id/reactions/:name", s.addAnnouncementReaction)`,
		`e.DELETE("/api/v1/announcements/:id/reactions/:name", s.removeAnnouncementReaction)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("announcement reaction route missing %q", want)
		}
	}
}
