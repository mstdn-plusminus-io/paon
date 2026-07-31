package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestTaskErrorContextIdentifiesTargetWithoutLeakingURLPath(t *testing.T) {
	if got := remoteTaskTargetHost("https://user:secret@Push.Example:8443/send/private-token?key=secret"); got != "push.example" {
		t.Fatalf("remote target host = %q", got)
	}
	if got := localTaskTargetHost(config.Config{LocalDomain: "LOCAL.EXAMPLE", WebDomain: "web.example"}); got != "local.example" {
		t.Fatalf("local target host = %q", got)
	}

	cause := errors.New("connection reset")
	err := taskTargetError("web push delivery", "remote", "push.example", cause)
	if !errors.Is(err, cause) {
		t.Fatalf("target error does not preserve cause: %v", err)
	}
	if got := err.Error(); got != `web push delivery target=remote host="push.example": connection reset` {
		t.Fatalf("target error = %q", got)
	}
}

func TestActivityFetchHTTPErrorIdentifiesRemoteServer(t *testing.T) {
	err := activityFetchHTTPError{
		StatusCode: 429,
		URL:        "https://remote.example/media/avatar.png?token=private",
	}
	got := err.Error()
	for _, want := range []string{`target=remote`, `host="remote.example"`, `status=429`} {
		if !strings.Contains(got, want) {
			t.Fatalf("activity fetch error %q missing %q", got, want)
		}
	}
	for _, secret := range []string{"avatar.png", "token", "private"} {
		if strings.Contains(got, secret) {
			t.Fatalf("activity fetch error leaks URL detail %q: %s", secret, got)
		}
	}
}
