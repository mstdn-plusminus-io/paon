package api

import (
	"os"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestMastodon45AntispamNormalizationAndAgeSets(t *testing.T) {
	unicodeVariant := "I use HTTPS://𝐁𝐀𝐍𝐍𝐄𝐃.𝐄𝐗𝐀𝐌𝐏𝐋𝐄 in my text"
	if got := mastodon45AntispamNormalizedText(unicodeVariant); !strings.Contains(got, "https://banned.example") {
		t.Fatalf("normalized antispam text = %q", got)
	}
	if !mastodon45AntispamConsidered(unicodeVariant, true, []string{"https://banned.example"}, nil, false) {
		t.Fatal("recent account with a Unicode-confusable spam URL was not blocked")
	}
	if mastodon45AntispamConsidered(unicodeVariant, false, []string{"https://banned.example"}, nil, false) {
		t.Fatal("old account was blocked by the recent-account set")
	}
	if !mastodon45AntispamConsidered(unicodeVariant, false, nil, []string{"https://banned.example"}, false) {
		t.Fatal("old account was not blocked by the all-time set")
	}
	if mastodon45AntispamConsidered(unicodeVariant, true, []string{"https://banned.example"}, nil, true) {
		t.Fatal("a post to a recipient who follows the author was treated as suspicious")
	}
}

func TestMastodon45AntispamRecipientsIncludeOnlyReplyAndMentions(t *testing.T) {
	reply := &models.Status{AccountID: 10}
	mentions := []models.Account{{ID: 20}, {ID: 10}, {ID: 30}}
	got := mastodon45AntispamRecipientIDs(reply, mentions)
	want := []int64{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("recipient IDs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recipient IDs = %#v, want %#v", got, want)
		}
	}
}

func TestMastodon45AntispamIsWiredBeforePersistence(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, source, "createStatus")
	checkAt := strings.Index(body, "s.mastodon45LocalStatusConsideredSpam")
	transactionAt := strings.Index(body, "s.db.Transaction")
	if checkAt < 0 || transactionAt < 0 || checkAt > transactionAt {
		t.Fatalf("antispam preflight must run before status persistence: %s", body)
	}
	for _, fragment := range []string{
		"s.mastodon45StatusMentionAccounts",
		"mastodon45AntispamRecipientIDs(replyTo, preflightMentions)",
		"s.mastodon45CreateAntispamReport",
		"mastodon45DummyStatus",
		"s.mastodon45DummyScheduledStatus",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("createStatus missing antispam behavior %q", fragment)
		}
	}
	if mastodon45AntispamReportComment != "Account automatically reported for posting a banned URL" {
		t.Fatalf("antispam report comment = %q", mastodon45AntispamReportComment)
	}
	reportSource, err := os.ReadFile("antispam_45.go")
	if err != nil {
		t.Fatal(err)
	}
	reportBody := functionBody(t, reportSource, "mastodon45CreateAntispamReport")
	for _, fragment := range []string{
		`account_id = ? AND target_account_id = ? AND category = ? AND action_taken_at IS NULL`,
		`representative.ID, target.ID, reportCategoryValue("spam")`,
	} {
		if !strings.Contains(reportBody, fragment) {
			t.Fatalf("antispam report duplicate scope missing %q", fragment)
		}
	}
}
