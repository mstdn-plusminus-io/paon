package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestAdminRelaysRequiresWebSession(t *testing.T) {
	s, err := NewServer(config.Config{Title: "Paon", LocalDomain: "example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/relays?state=accepted", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := "/auth/sign_in?redirect_to=" + url.QueryEscape("/admin/relays?state=accepted")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestParseAdminRelayFormAcceptsRailsNestedFields(t *testing.T) {
	form := url.Values{}
	form.Set("relay[inbox_url]", " https://relay.example/inbox ")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/relays", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got, err := parseAdminRelayForm(c)
	if err != nil {
		t.Fatal(err)
	}
	if got != (adminRelayForm{InboxURL: "https://relay.example/inbox"}) {
		t.Fatalf("form = %#v", got)
	}
}

func TestValidateAdminRelayForm(t *testing.T) {
	if err := validateAdminRelayForm(adminRelayForm{InboxURL: "https://relay.example/inbox"}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminRelayForm(adminRelayForm{InboxURL: " https://relay.example/inbox "}); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminRelayForm(adminRelayForm{}); err == nil {
		t.Fatal("expected blank URL to be rejected")
	}
	if err := validateAdminRelayForm(adminRelayForm{InboxURL: "ftp://relay.example/inbox"}); err == nil {
		t.Fatal("expected non-http URL to be rejected")
	}
}

func TestAdminRelaysHTMLUsesRailsLocaleKeys(t *testing.T) {
	html := adminRelaysIndexHTML([]models.Relay{
		{ID: 2, InboxURL: "https://relay.example/inbox", State: relayStateAccepted},
		{ID: 3, InboxURL: "https://pending.example/inbox", State: relayStatePending},
	}, "", "", true, "ja")

	for _, want := range []string{
		"<strong>連合リレー</strong>",
		"リレーURL",
		"ステータス",
		"有効",
		"リレーサーバーの承認待ちです",
		"無効化",
		"削除",
		"セキュアモードまたは連合制限モードが有効の場合、リレーは正常に動作しません",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("localized relays html missing %q: %s", want, html)
		}
	}
}

func TestActivityPubRelayFollowPayloads(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.com", LocalDomain: "example.com"}}
	account := &models.Account{ID: -99, Username: "mastodon.internal"}

	follow := activityPubRelayFollowPayload(server, account, "https://example.com/actor#relay-follow-1")
	if follow["id"] != "https://example.com/actor#relay-follow-1" || follow["type"] != "Follow" {
		t.Fatalf("follow = %#v", follow)
	}
	if follow["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("follow context should match Rails FollowSerializer: %#v", follow["@context"])
	}
	if follow["actor"] != "https://example.com/actor" || follow["object"] != "https://www.w3.org/ns/activitystreams#Public" {
		t.Fatalf("follow actor/object = %#v", follow)
	}

	undo := activityPubRelayUndoFollowPayload(server, account, "https://example.com/actor#relay-unfollow-1", follow["id"].(string))
	if undo["type"] != "Undo" || undo["actor"] != "https://example.com/actor" {
		t.Fatalf("undo = %#v", undo)
	}
	if undo["@context"] != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("undo context should match Rails UndoFollowSerializer: %#v", undo["@context"])
	}
	object := undo["object"].(map[string]any)
	if object["id"] != follow["id"] || object["type"] != "Follow" || object["actor"] != "https://example.com/actor" || object["object"] != "https://www.w3.org/ns/activitystreams#Public" {
		t.Fatalf("undo object = %#v", object)
	}
}

func TestAdminRelayRedirectErrorsUseLocaleKeys(t *testing.T) {
	src, err := os.ReadFile("admin_relays.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"createAdminRelay", `user, handled, err := s.requireAdminFederationWebUser(c)`},
		{"createAdminRelay", `locale := s.webLocale(c, user)`},
		{"createAdminRelay", `adminRelayMessage(locale, "errors.invalid", "Relay is invalid")`},
		{"createAdminRelay", `adminRelayErrorText(locale, err)`},
		{"createAdminRelay", `adminRelayMessage(locale, "errors.database_unavailable", "DATABASE_URL is not set")`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing localized redirect helper %q", check.fn, check.want)
		}
	}
	for _, forbidden := range []string{
		`QueryEscape("Relay is invalid")`,
		`QueryEscape(err.Error())`,
		`QueryEscape("DATABASE_URL is not set")`,
	} {
		if functionBodyContains(t, src, "createAdminRelay", forbidden) {
			t.Fatalf("createAdminRelay still contains display literal %q", forbidden)
		}
	}
}

func TestAdminRelayMessagesResolveJapaneseLocale(t *testing.T) {
	for _, check := range []struct {
		key       string
		fallback  string
		forbidden string
		want      string
	}{
		{"errors.invalid", "Relay is invalid", "Relay is invalid", "不正"},
		{"errors.inbox_url_blank", "Relay inbox URL can't be blank", "Relay inbox", "入力"},
		{"errors.inbox_url_invalid", "Relay inbox URL is invalid", "Relay inbox", "不正"},
	} {
		got := adminRelayMessage("ja", check.key, check.fallback)
		if strings.Contains(got, check.forbidden) || !strings.Contains(got, check.want) {
			t.Fatalf("adminRelayMessage(%q) = %q", check.key, got)
		}
	}
	if got := adminRelayErrorText("ja", errAdminSetting("Relay inbox URL is invalid")); strings.Contains(got, "Relay inbox") || !strings.Contains(got, "不正") {
		t.Fatalf("adminRelayErrorText invalid = %q", got)
	}
}

func TestAdminRelayHTMLWarnsWhenAuthorizedFetchEnabled(t *testing.T) {
	warning := "Relays may not work correctly while secure mode or limited federation mode is enabled"
	for name, html := range map[string]string{
		"index": adminRelaysIndexHTML(nil, "", "", true, "en"),
		"form":  adminRelayFormHTML(adminRelayForm{}, "", true, "en"),
	} {
		if !strings.Contains(html, warning) {
			t.Fatalf("%s html missing authorized-fetch warning: %s", name, html)
		}
	}
	if strings.Contains(adminRelaysIndexHTML(nil, "", "", false, "en"), warning) {
		t.Fatal("index html rendered authorized-fetch warning when disabled")
	}
}

func TestAdminRelayStateLabel(t *testing.T) {
	cases := map[int]string{
		relayStateIdle:     "disabled",
		relayStatePending:  "pending",
		relayStateAccepted: "enabled",
		relayStateRejected: "rejected",
	}
	for state, want := range cases {
		if got := adminRelayStateLabel(state); got != want {
			t.Fatalf("state %d = %q, want %q", state, got, want)
		}
	}
}

func mustFunctionBody(t *testing.T, src string, name string) string {
	t.Helper()
	start := -1
	searchFrom := 0
	for {
		funcIndex := strings.Index(src[searchFrom:], "func ")
		if funcIndex < 0 {
			break
		}
		funcIndex += searchFrom
		open := strings.Index(src[funcIndex:], "{")
		if open < 0 {
			break
		}
		signature := src[funcIndex : funcIndex+open]
		if strings.Contains(signature, name+"(") {
			start = funcIndex
			break
		}
		searchFrom = funcIndex + len("func ")
	}
	if start < 0 {
		t.Fatalf("function %s not found", name)
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("function %s body not found", name)
	}
	i := start + open
	depth := 0
	for ; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("function %s body not closed", name)
	return ""
}
