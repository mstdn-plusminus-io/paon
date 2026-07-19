package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestCreatePollMatchesRailsRecordNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/polls", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.createPoll(c); err == nil {
		t.Fatal("expected createPoll to return Record not found")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusNotFound {
		t.Fatalf("error = %#v", err)
	}
}

func TestPollVoteChoicesAcceptsJSONNumbersAndStrings(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/polls/1/votes", strings.NewReader(`{"choices":[0,"2"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	got, ok := pollVoteChoices(c)
	if !ok {
		t.Fatal("pollVoteChoices rejected valid JSON choices")
	}
	want := []int{0, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pollVoteChoices = %#v, want %#v", got, want)
	}
}

func TestPollVoteChoicesAcceptsRailsFormAndQueryArrays(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/polls/1/votes", strings.NewReader("choices%5B%5D=0&choices%5B%5D=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := echo.NewContext(req, httptest.NewRecorder(), e)

	got, ok := pollVoteChoices(c)
	if !ok {
		t.Fatal("pollVoteChoices rejected valid form choices")
	}
	if want := []int{0, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("form pollVoteChoices = %#v, want %#v", got, want)
	}

	req = httptest.NewRequest("POST", "/api/v1/polls/1/votes?choices[]=1&choices[]=2&choices[]=3", nil)
	c = echo.NewContext(req, httptest.NewRecorder(), e)

	got, ok = pollVoteChoices(c)
	if !ok {
		t.Fatal("pollVoteChoices rejected valid query choices")
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query pollVoteChoices = %#v, want %#v", got, want)
	}
}

func TestPollVoteChoicesRejectsInvalidValuesLikeRailsInteger(t *testing.T) {
	e := echo.New()
	for _, req := range []*http.Request{
		httptest.NewRequest("POST", "/api/v1/polls/1/votes?choices[]=bad", nil),
		httptest.NewRequest("POST", "/api/v1/polls/1/votes?choices[]=2,3", nil),
		httptest.NewRequest("POST", "/api/v1/polls/1/votes", strings.NewReader("choices%5B%5D=bad")),
	} {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c := echo.NewContext(req, httptest.NewRecorder(), e)
		if got, ok := pollVoteChoices(c); ok {
			t.Fatalf("pollVoteChoices accepted invalid query choices: %#v", got)
		}
	}

	req := httptest.NewRequest("POST", "/api/v1/polls/1/votes", strings.NewReader(`{"choices":[0,"bad"]}`))
	req.Header.Set("Content-Type", "application/json")
	c := echo.NewContext(req, httptest.NewRecorder(), e)
	if got, ok := pollVoteChoices(c); ok {
		t.Fatalf("pollVoteChoices accepted invalid JSON choices: %#v", got)
	}
}

func TestPollVoteValidationMessagesMatchRailsLocales(t *testing.T) {
	if railsPollAlreadyVotedMessage != "Validation failed: You have already voted on this poll" {
		t.Fatalf("already voted message = %q", railsPollAlreadyVotedMessage)
	}
	if railsPollExpiredMessage != "Validation failed: The poll has already ended" {
		t.Fatalf("expired message = %q", railsPollExpiredMessage)
	}
	if railsPollInvalidChoiceMessage != "Validation failed: The chosen vote option does not exist" {
		t.Fatalf("invalid choice message = %q", railsPollInvalidChoiceMessage)
	}
	if railsPollSelfVoteMessage != "Validation failed: You cannot vote in your own polls" {
		t.Fatalf("self vote message = %q", railsPollSelfVoteMessage)
	}
}

func TestPollVoteDuplicateChoicesMatchRailsAlreadyVoted(t *testing.T) {
	src, err := os.ReadFile("polls.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if !poll.Multiple && len(choices) > 1 {`,
		`return apiError(c, http.StatusUnprocessableEntity, railsPollAlreadyVotedMessage)`,
		`if _, ok := seen[choice]; ok {`,
		`return apiError(c, http.StatusUnprocessableEntity, railsPollAlreadyVotedMessage)`,
	} {
		if !functionBodyContains(t, src, "votePoll", want) {
			t.Fatalf("votePoll duplicate choice path missing %q", want)
		}
	}
	if functionBodyContains(t, src, "votePoll", `Poll does not allow multiple choices`) {
		t.Fatal("non-multiple poll should use the Rails already_voted validation message")
	}
}

func TestPollAPIsApplyStatusVisibilityGuard(t *testing.T) {
	src, err := os.ReadFile("polls.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`poll, err := s.findVisiblePollForAccount(account, c.Param("id"))`,
		`refreshed, err := s.findVisiblePollForAccount(account, c.Param("id"))`,
		`visibleStatusIDs := s.visibleStatusQuery(account).Select("statuses.id")`,
		`Where("polls.status_id IN (?)", visibleStatusIDs)`,
		`Preload("Account")`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("poll visibility guard missing %q", want)
		}
	}
}

func TestEmptyPollVoteChoicesReturnPollLikeRails(t *testing.T) {
	src, err := os.ReadFile("polls.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`choices, validChoices := pollVoteChoices(c)`,
		`if !validChoices {`,
		`return apiError(c, http.StatusUnprocessableEntity, railsPollInvalidChoiceMessage)`,
		`if len(choices) == 0 {`,
		`if err := s.hydratePollCustomEmojis(poll); err != nil`,
		`return c.JSON(http.StatusOK, serializer.PollFromModel(s.cfg, poll, account))`,
	} {
		if !functionBodyContains(t, src, "votePoll", want) {
			t.Fatalf("votePoll empty choices path missing %q", want)
		}
	}
	if functionBodyContains(t, src, "votePoll", `Choices can't be blank`) {
		t.Fatal("votePoll should not reject empty choices; Rails VoteService no-ops and returns the poll")
	}
}

func TestPollAPIsHydrateCustomEmojis(t *testing.T) {
	src, err := os.ReadFile("polls.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"getPoll": {
			`if err := s.hydratePollCustomEmojis(poll); err != nil`,
			`return c.JSON(http.StatusOK, serializer.PollFromModel(s.cfg, poll, account))`,
		},
		"votePoll": {
			`if err := s.hydratePollCustomEmojis(refreshed); err != nil`,
			`return c.JSON(http.StatusOK, serializer.PollFromModel(s.cfg, refreshed, account))`,
		},
		"hydratePollCustomEmojis": {
			`shortcodes := statusEmbedEmojiShortcodes(strings.Join(poll.Options, "\n"))`,
			`query := customEmojiDomainQuery(s.db.Where("shortcode IN ? AND disabled = false", shortcodes), poll.Account.Domain)`,
			`poll.CustomEmojis = orderCustomEmojisByShortcode(shortcodes, emojis)`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s missing %q", fn, want)
			}
		}
	}
}

func TestRemotePollPossiblyStaleMatchesRailsModel(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	remote := models.Poll{
		Account:       models.Account{ID: 10, Domain: sql.NullString{String: "remote.example", Valid: true}},
		ExpiresAt:     sql.NullTime{Time: now.Add(time.Hour), Valid: true},
		LastFetchedAt: sql.NullTime{Time: now.Add(-2 * time.Minute), Valid: true},
	}
	if !remotePollPossiblyStale(&remote, now) {
		t.Fatal("remote poll fetched more than a minute ago before expiration should be stale")
	}

	local := remote
	local.Account = models.Account{ID: 10}
	if remotePollPossiblyStale(&local, now) {
		t.Fatal("local poll should not be stale for remote refresh")
	}

	recent := remote
	recent.LastFetchedAt = sql.NullTime{Time: now.Add(-30 * time.Second), Valid: true}
	if remotePollPossiblyStale(&recent, now) {
		t.Fatal("recently fetched remote poll should not be stale")
	}

	fetchedAfterExpiration := remote
	fetchedAfterExpiration.ExpiresAt = sql.NullTime{Time: now.Add(-time.Hour), Valid: true}
	fetchedAfterExpiration.LastFetchedAt = sql.NullTime{Time: now.Add(-30 * time.Minute), Valid: true}
	if remotePollPossiblyStale(&fetchedAfterExpiration, now) {
		t.Fatal("remote poll fetched after expiration should not be stale")
	}

	neverFetched := remote
	neverFetched.LastFetchedAt = sql.NullTime{}
	if !remotePollPossiblyStale(&neverFetched, now) {
		t.Fatal("never-fetched remote poll should be stale")
	}
}
