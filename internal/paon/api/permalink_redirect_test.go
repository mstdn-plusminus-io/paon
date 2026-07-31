package api

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestPermalinkRedirectPathSegmentsMatchRailsDeckStripping(t *testing.T) {
	for _, tc := range []struct {
		path string
		want []string
	}{
		{path: "/@alice@remote.example/123", want: []string{"@alice@remote.example", "123"}},
		{path: "/statuses/123", want: []string{"statuses", "123"}},
		{path: "/deck/statuses/123", want: []string{"statuses", "123"}},
		{path: "/deck/@alice@remote.example", want: []string{"@alice@remote.example"}},
		{path: "/deck", want: nil},
	} {
		if got := permalinkPathSegments(tc.path); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("permalinkPathSegments(%q) = %#v, want %#v", tc.path, got, tc.want)
		}
	}

	s := &Server{}
	if got := s.permalinkRedirectPath("/deck/getting-started"); got != "/getting-started" {
		t.Fatalf("deck redirect = %q, want /getting-started", got)
	}
	if got := s.permalinkRedirectPath("/deck"); got != "" {
		t.Fatalf("empty deck redirect = %q, want empty", got)
	}
}

func TestPermalinkRedirectGuardsMatchRailsSignedAndRemoteURLBoundaries(t *testing.T) {
	s := &Server{}
	user := &models.User{ID: 10}
	account := &models.Account{ID: 20}
	if got := s.webAppPermalinkRedirectPath("/deck/getting-started", account, user); got != "" {
		t.Fatalf("signed non-moved user redirect = %q, want empty", got)
	}
	account.MovedToAccountID = sql.NullInt64{Int64: 99, Valid: true}
	if got := s.webAppPermalinkRedirectPath("/deck/getting-started", account, user); got != "" {
		t.Fatalf("nil DB moved user redirect = %q, want empty", got)
	}

	if got := permalinkRemoteURL(sql.NullString{String: "https://remote.example/@alice", Valid: true}); got != "https://remote.example/@alice" {
		t.Fatalf("remote URL = %q", got)
	}
	for _, value := range []sql.NullString{
		{},
		{String: " https://remote.example/@alice", Valid: true},
		{String: "ftp://remote.example/@alice", Valid: true},
		{String: "", Valid: true},
	} {
		if got := permalinkRemoteURL(value); got != "" {
			t.Fatalf("permalinkRemoteURL(%#v) = %q, want empty", value, got)
		}
	}
}
