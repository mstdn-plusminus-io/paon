package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
)

func TestAccountMigrationNoteTextUsesMoveHandlerLocale(t *testing.T) {
	for _, tc := range []struct {
		locale string
		key    string
		want   string
	}{
		{"ja", "copy_account_note_text", "このユーザーはalice@old.exampleから引っ越しました。これは以前のメモです。"},
		{"ja", "carry_blocks_over_text", "このユーザーは、あなたがブロックしていたalice@old.exampleから引っ越しました。"},
		{"ja", "carry_mutes_over_text", "このユーザーは、あなたがミュートしていたalice@old.exampleから引っ越しました。"},
		{"en", "copy_account_note_text", "This user moved from alice@old.example, here were your previous notes about them:"},
		{"en", "carry_blocks_over_text", "This user moved from alice@old.example, which you had blocked."},
		{"en", "carry_mutes_over_text", "This user moved from alice@old.example, which you had muted."},
	} {
		if got := accountMigrationNoteText(tc.locale, tc.key, "alice@old.example"); got != tc.want {
			t.Fatalf("%s %s = %q, want %q", tc.locale, tc.key, got, tc.want)
		}
	}
}

func TestAccountNoteCommentValidCountsCharacters(t *testing.T) {
	if !accountNoteCommentValid(strings.Repeat("あ", accountNoteCommentMaxLength)) {
		t.Fatal("2000 multibyte characters must be valid")
	}
	if accountNoteCommentValid(strings.Repeat("a", accountNoteCommentMaxLength+1)) {
		t.Fatal("2001 characters must be invalid")
	}
}

func TestActivityPubMoveTargetFetchErrorKeepsOnlyNetworkErrorsRetryable(t *testing.T) {
	network := taskTargetError("failed to fetch remote activity", "remote", "new.example", &url.Error{Op: "Get", URL: "https://new.example/users/alice", Err: errors.New("connection refused")})
	if err := activityPubMoveTargetFetchError(network); err != network {
		t.Fatalf("network error = %v, want it returned unchanged", err)
	}
	for _, err := range []error{
		activityFetchHTTPError{StatusCode: http.StatusNotFound, URL: "https://new.example/users/alice"},
		activityFetchHTTPError{StatusCode: http.StatusServiceUnavailable, URL: "https://new.example/users/alice"},
		fmt.Errorf("unsupported actor type"),
		fmt.Errorf("webfinger response does not loop back to actor"),
	} {
		if got := activityPubMoveTargetFetchError(err); !errors.Is(got, errActivityPubEventNotApplied) {
			t.Fatalf("%v = %v, want errActivityPubEventNotApplied", err, got)
		}
	}
	if activityPubMoveTargetFetchError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestAsynqServeMuxRegistersMoveTask(t *testing.T) {
	if asynqTaskMove != "move" {
		t.Fatalf("move task type = %q", asynqTaskMove)
	}
	s := &Server{}
	if _, pattern := s.newAsynqServeMux().Handler(asynq.NewTask(asynqTaskMove, nil)); pattern != asynqTaskMove {
		t.Fatalf("move task pattern = %q, want %q", pattern, asynqTaskMove)
	}
	if err := s.handleAsynqMove(context.Background(), asynq.NewTask(asynqTaskMove, []byte(`{"source_account_id":0,"target_account_id":2}`))); err != nil {
		t.Fatalf("incomplete move payload = %v, want nil", err)
	}
	if err := s.handleAsynqMove(context.Background(), asynq.NewTask(asynqTaskMove, []byte(`{`))); err == nil {
		t.Fatal("malformed move payload must fail")
	}
}
