package api

import (
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestStatusCreatePayloadFromScheduledParamsRoundTrip(t *testing.T) {
	params, err := scheduledStatusParamsFromPayload(statusCreatePayload{
		statusUpdatePayload: statusUpdatePayload{
			Status:       "hello #go",
			Sensitive:    true,
			HasSensitive: true,
			SpoilerText:  "cw",
			Language:     "ja",
			Poll:         &pollUpdatePayload{Options: []string{"yes", "no"}, Multiple: true, HideTotals: true, ExpiresIn: 600},
			HasPoll:      true,
		},
		Visibility:         "private",
		InReplyToID:        "123",
		QuoteID:            "456",
		AllowedMentions:    []string{"123", "456"},
		HasAllowedMentions: true,
		ApplicationID:      sql.NullInt64{Int64: 99, Valid: true},
	}, []string{"7", "8"})
	if err != nil {
		t.Fatal(err)
	}
	payload, mediaIDs, err := statusCreatePayloadFromScheduledParams(params)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Status != "hello #go" || !payload.Sensitive || !payload.HasSensitive || payload.SpoilerText != "cw" || payload.Language != "ja" || payload.Visibility != "private" || payload.InReplyToID != "123" || payload.QuoteID != "456" {
		t.Fatalf("payload = %#v", payload)
	}
	if !reflect.DeepEqual(mediaIDs, []string{"7", "8"}) || !reflect.DeepEqual(payload.MediaIDs, []string{"7", "8"}) || !payload.HasMediaIDs {
		t.Fatalf("media IDs = %#v payload=%#v", mediaIDs, payload.MediaIDs)
	}
	if !payload.HasAllowedMentions || !reflect.DeepEqual(payload.AllowedMentions, []string{"123", "456"}) {
		t.Fatalf("allowed_mentions = %#v has=%v", payload.AllowedMentions, payload.HasAllowedMentions)
	}
	if !payload.ApplicationID.Valid || payload.ApplicationID.Int64 != 99 {
		t.Fatalf("application_id = %#v", payload.ApplicationID)
	}
	if payload.Poll == nil || !payload.HasPoll || !reflect.DeepEqual(payload.Poll.Options, []string{"yes", "no"}) || !payload.Poll.Multiple || !payload.Poll.HideTotals || payload.Poll.ExpiresIn != 600 {
		t.Fatalf("poll = %#v has=%v", payload.Poll, payload.HasPoll)
	}
}

func TestStatusCreatePayloadFromScheduledParamsAcceptsRailsLikeJSON(t *testing.T) {
	raw := models.JSONValue(`{
		"status":"queued",
		"visibility":"unlisted",
		"sensitive":false,
		"spoiler_text":"",
		"in_reply_to_id":123,
		"quote_id":456,
		"application_id":"99",
		"allowed_mentions":[123,"456"],
		"media_ids":[7,"8"],
		"poll":{"options":["a","b"],"multiple":false,"hide_totals":false,"expires_in":300}
	}`)
	payload, mediaIDs, err := statusCreatePayloadFromScheduledParams(raw)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Status != "queued" || payload.Visibility != "unlisted" || payload.InReplyToID != "123" || payload.QuoteID != "456" || payload.Sensitive || !payload.HasSensitive {
		t.Fatalf("payload = %#v", payload)
	}
	if !reflect.DeepEqual(mediaIDs, []string{"7", "8"}) {
		t.Fatalf("media IDs = %#v", mediaIDs)
	}
	if !payload.HasAllowedMentions || !reflect.DeepEqual(payload.AllowedMentions, []string{"123", "456"}) {
		t.Fatalf("allowed_mentions = %#v has=%v", payload.AllowedMentions, payload.HasAllowedMentions)
	}
	if !payload.ApplicationID.Valid || payload.ApplicationID.Int64 != 99 {
		t.Fatalf("application_id = %#v", payload.ApplicationID)
	}
	if payload.Poll == nil || payload.Poll.ExpiresIn != 300 {
		t.Fatalf("poll = %#v", payload.Poll)
	}
}

func TestScheduledStatusPublishUsesRailsDefaultPrivacyFallback(t *testing.T) {
	src, err := os.ReadFile("scheduled_status_publish.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if !scheduled.AccountID.Valid || scheduled.AccountID.Int64 == 0`,
		`accountID := scheduled.AccountID.Int64`,
		`Preload("User").Where("id = ? AND suspended_at IS NULL", accountID)`,
		`Visibility:    s.statusVisibility(account, payload.Visibility)`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("scheduled publish missing %q", want)
		}
	}
}

func TestScheduledStatusPublishRestoresRailsApplicationID(t *testing.T) {
	src, err := os.ReadFile("scheduled_status_publish.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`payload.ApplicationID = rawJSONInt64(params["application_id"])`,
		`ApplicationID: payload.ApplicationID`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("scheduled publish missing %q", want)
		}
	}
}

func TestStatusCreatePersistsApplicationIDLikePostStatusService(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`payload.ApplicationID = s.requestApplicationID(c)`,
		`ApplicationID: payload.ApplicationID`,
		`params["application_id"] = payload.ApplicationID.Int64`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("status create missing %q", want)
		}
	}
}

func TestRawJSONHelpersRejectEmptyValues(t *testing.T) {
	if got := rawJSONString(nil); got != "" {
		t.Fatalf("rawJSONString(nil) = %q", got)
	}
	if got := rawJSONStringSlice(nil); got != nil {
		t.Fatalf("rawJSONStringSlice(nil) = %#v", got)
	}
	if got := rawJSONIntDefault(nil); got != 0 {
		t.Fatalf("rawJSONIntDefault(nil) = %d", got)
	}
}

func TestRawJSONBoolUsesRailsBooleanSemanticsForStrings(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want bool
	}{
		{`true`, true},
		{`false`, false},
		{`"0"`, false},
		{`"f"`, false},
		{`"off"`, false},
		{`"no"`, true},
		{`"bad"`, true},
	} {
		got, ok := rawJSONBool([]byte(tt.raw))
		if !ok || got != tt.want {
			t.Fatalf("rawJSONBool(%s) = %v, %v; want %v, true", tt.raw, got, ok, tt.want)
		}
	}
}
