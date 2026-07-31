package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAnnouncementStreamPayloadParsesForStreaming(t *testing.T) {
	payload := announcementStreamPayload("announcement.delete", "42")
	message, ok := redisPubSubMessage([]any{"message", "timeline:1", payload})
	if !ok {
		t.Fatal("announcement payload did not parse")
	}
	if message.Event != "announcement.delete" || string(message.Payload) != `"42"` {
		t.Fatalf("message = %#v payload=%s", message, string(message.Payload))
	}
}

func TestAnnouncementReactionStreamPayloadShape(t *testing.T) {
	payload := announcementStreamPayload("announcement.reaction", map[string]any{
		"announcement_id": "9",
		"name":            "party",
		"count":           int64(3),
	})
	var decoded struct {
		Event   string `json:"event"`
		Payload struct {
			AnnouncementID string `json:"announcement_id"`
			Name           string `json:"name"`
			Count          int64  `json:"count"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Event != "announcement.reaction" || decoded.Payload.AnnouncementID != "9" || decoded.Payload.Name != "party" || decoded.Payload.Count != 3 {
		t.Fatalf("payload = %#v", decoded)
	}
}

func TestTimelineChannelFromSubscribedKey(t *testing.T) {
	cfg := config.Config{RedisNamespace: "mastodon:"}
	got, ok := timelineChannelFromSubscribedKey(cfg, "mastodon:subscribed:timeline:123")
	if !ok || got != "mastodon:timeline:123" {
		t.Fatalf("channel = %q ok=%v", got, ok)
	}
	for _, key := range []string{
		"mastodon:subscribed:timeline:123:notifications",
		"mastodon:subscribed:timeline:public",
		"subscribed:timeline:123",
	} {
		if channel, ok := timelineChannelFromSubscribedKey(cfg, key); ok {
			t.Fatalf("unexpected channel %q for key %q", channel, key)
		}
	}
}

func TestRedisScanKeys(t *testing.T) {
	cursor, keys, ok := redisScanKeys([]any{"0", []any{"a", "b", int64(1)}})
	if !ok || cursor != "0" || !reflect.DeepEqual(keys, []string{"a", "b"}) {
		t.Fatalf("cursor=%q keys=%#v ok=%v", cursor, keys, ok)
	}
	if _, _, ok := redisScanKeys([]any{"0", "bad"}); ok {
		t.Fatal("expected malformed scan response to be rejected")
	}
}

func TestStatusIDsFromAnnouncementText(t *testing.T) {
	s := &Server{cfg: config.Config{Scheme: "https", WebDomain: "social.example", LocalDomain: "social.example"}}
	got := s.statusIDsFromAnnouncementText(`See https://social.example/@alice/100 and https://social.example/@bob/101, plus https://remote.example/@carol/102 and https://social.example/@alice/100.`)
	want := models.Int64Array{100, 101}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status IDs = %#v, want %#v", got, want)
	}
}

func TestInt64ArraysEqual(t *testing.T) {
	if !int64ArraysEqual(models.Int64Array{1, 2}, models.Int64Array{1, 2}) {
		t.Fatal("expected arrays to match")
	}
	if int64ArraysEqual(models.Int64Array{1, 2}, models.Int64Array{2, 1}) {
		t.Fatal("expected ordered arrays to differ")
	}
}
