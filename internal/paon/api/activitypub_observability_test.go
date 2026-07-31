package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
)

func TestActivityPubLogFieldsExtractEnvelopeWithoutContent(t *testing.T) {
	body := []byte(`{
  "@context": ["https://www.w3.org/ns/activitystreams", "https://misskey-hub.net/ns#"],
  "id": "https://misskey.example/activities/create-1",
  "type": "Create",
  "actor": {"id": "https://misskey.example/users/alice"},
  "object": {"id": "https://misskey.example/notes/note-1", "type": "Note", "content": "private body"}
}`)
	fields := activityPubLogFieldsFromBody(body)
	if fields.Type != "Create" || fields.ID != "https://misskey.example/activities/create-1" {
		t.Fatalf("activity fields = %#v", fields)
	}
	if fields.Actor != "https://misskey.example/users/alice" || fields.Object != "https://misskey.example/notes/note-1" {
		t.Fatalf("actor/object fields = %#v", fields)
	}
	err := activityPubProcessingError(body, 42, 7, errors.New("processing failed"))
	if err == nil || !strings.Contains(err.Error(), "activity_type=\"Create\"") || !strings.Contains(err.Error(), "processing failed") {
		t.Fatalf("processing error = %v", err)
	}
	if strings.Contains(err.Error(), "private body") {
		t.Fatalf("processing error leaked object content: %v", err)
	}
}

func TestActivityPubResponseSnippetIsBoundedAndSingleLine(t *testing.T) {
	value := " first\nsecond\t" + strings.Repeat("x", activityPubResponseLogLimit*2)
	got := activityPubResponseSnippet(strings.NewReader(value))
	if strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("response snippet contains control whitespace: %q", got)
	}
	if len(got) > activityPubResponseLogLimit+3 {
		t.Fatalf("response snippet length = %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("response snippet was not marked truncated: %q", got)
	}
}

func TestAsynqTaskErrorLogDoesNotIncludePayload(t *testing.T) {
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	task := asynq.NewTask(asynqTaskActivityPubProcessing, []byte(`{"content":"do not log me"}`))
	logAsynqTaskError(context.Background(), task, errors.New("processing failed"))
	got := output.String()
	for _, want := range []string{`task_type="activitypub:processing"`, "payload_bytes=27", "processing failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("task error log %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "do not log me") {
		t.Fatalf("task error log leaked payload: %q", got)
	}
}
