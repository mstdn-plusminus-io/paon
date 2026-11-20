package api

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

// dryRunSQLTopLevelIndex returns the index of the first occurrence of token in
// query outside parentheses and single-quoted literals, or -1.
func dryRunSQLTopLevelIndex(query string, token string) int {
	depth := 0
	inQuote := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case inQuote:
			if c == '\'' {
				inQuote = false
			}
		case c == '\'':
			inQuote = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(query[i:], token):
			return i
		}
	}
	return -1
}

// dryRunSQLOuterWhere returns the outermost WHERE condition of query with its
// whitespace collapsed, without the trailing GROUP BY / ORDER BY / LIMIT.
func dryRunSQLOuterWhere(t *testing.T, query string) string {
	t.Helper()
	query = strings.Join(strings.Fields(query), " ")
	start := dryRunSQLTopLevelIndex(query, " WHERE ")
	if start < 0 {
		t.Fatalf("query has no top-level WHERE: %s", query)
	}
	where := query[start+len(" WHERE "):]
	end := len(where)
	for _, terminator := range []string{" GROUP BY ", " ORDER BY ", " LIMIT "} {
		if idx := dryRunSQLTopLevelIndex(where, terminator); idx >= 0 && idx < end {
			end = idx
		}
	}
	return where[:end]
}

func dryRunCapturedQuery(t *testing.T, queries []string, markers ...string) string {
	t.Helper()
	for _, query := range queries {
		matched := true
		for _, marker := range markers {
			if !strings.Contains(query, marker) {
				matched = false
				break
			}
		}
		if matched {
			return query
		}
	}
	t.Fatalf("no captured query contains %q:\n%s", markers, strings.Join(queries, "\n"))
	return ""
}

// A signed outbox page fetched by someone other than the owner adds the boost
// exclusion OR group. GORM v1.30.0 only parenthesizes a Where expression that
// contains " AND " or " OR " with spaces (clause/where.go), so that group must
// carry its own parentheses; otherwise its OR terms escape the owner,
// visibility, deleted_at and pagination conditions and every status matches.
func TestActivityPubOutboxSignedPageKeepsOwnerScope(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	server := &Server{db: statusMentionDryRunDatabase(t, capture)}
	owner := models.Account{ID: 11, Username: "owner"}
	signer := models.Account{ID: 22, Username: "signer", Domain: sql.NullString{String: "remote.example", Valid: true}}

	if _, err := server.activityPubStatuses(owner, &signer, "100", "", ""); err != nil {
		t.Fatal(err)
	}
	where := dryRunSQLOuterWhere(t, dryRunCapturedQuery(t, capture.queries, `FROM "statuses"`, "activitypub_outbox_reblog_accounts"))
	if idx := dryRunSQLTopLevelIndex(where, " OR "); idx >= 0 {
		t.Fatalf("signed outbox WHERE has a top-level OR that bypasses the owner scope: %s", where)
	}
	for _, want := range []string{
		"(statuses.account_id = 11 AND statuses.deleted_at IS NULL)",
		"AND ((statuses.visibility IN (0,1) OR activitypub_outbox_mentions.id IS NOT NULL))",
		"AND (statuses.reblog_of_id IS NULL OR activitypub_outbox_reblog_accounts.domain IS NULL OR NOT EXISTS ( SELECT 1 FROM account_domain_blocks WHERE account_domain_blocks.account_id = 22 AND account_domain_blocks.domain = activitypub_outbox_reblog_accounts.domain ))",
		"AND statuses.id < '100'",
	} {
		if !strings.Contains(where, want) {
			t.Fatalf("signed outbox WHERE missing %q: %s", want, where)
		}
	}
	if !strings.HasPrefix(where, "(statuses.account_id = 11 AND statuses.deleted_at IS NULL)") {
		t.Fatalf("signed outbox WHERE must start with the owner scope: %s", where)
	}
}

// reportableStatusIDs shares the boost exclusion helper with the outbox, so
// the attached status IDs must stay limited to the reported account.
func TestReportableStatusIDsKeepsTargetAccountScope(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	server := &Server{db: statusMentionDryRunDatabase(t, capture)}
	source := models.Account{ID: 7, Username: "reporter"}
	target := models.Account{ID: 9, Username: "reported"}

	// The dry run returns no rows, so the lookup reports the IDs as not found.
	if _, err := server.reportableStatusIDs(source, target, models.Int64Array{1, 2}); err == nil {
		t.Fatal("dry-run report status lookup unexpectedly matched the requested statuses")
	}
	where := dryRunSQLOuterWhere(t, dryRunCapturedQuery(t, capture.queries, `FROM "statuses"`, "activitypub_outbox_reblog_accounts"))
	if idx := dryRunSQLTopLevelIndex(where, " OR "); idx >= 0 {
		t.Fatalf("report status WHERE has a top-level OR that bypasses the target scope: %s", where)
	}
	if !strings.HasPrefix(where, "(statuses.account_id = 9 AND statuses.id IN ") {
		t.Fatalf("report status WHERE must start with the target account scope: %s", where)
	}
	if !strings.Contains(where, "statuses.id IN (1,2)") {
		t.Fatalf("report status IDs must expand into an IN list: %s", where)
	}
	if !strings.Contains(where, "AND (statuses.reblog_of_id IS NULL OR activitypub_outbox_reblog_accounts.domain IS NULL OR NOT EXISTS (") {
		t.Fatalf("report status WHERE must keep the parenthesized boost exclusion: %s", where)
	}
}

// Reported collection IDs (Mastodon 4.6) arrive as models.Int64Array like the
// status IDs, so they must be expanded into an IN list as well.
func TestReportTargetCollectionIDsExpandIntoINList(t *testing.T) {
	capture := &statusMentionSQLCapture{}
	server := &Server{db: statusMentionDryRunDatabase(t, capture)}

	if _, err := server.reportTargetCollectionIDs(models.Account{ID: 9}, models.Int64Array{3, 4}); err != nil {
		t.Fatal(err)
	}
	where := dryRunSQLOuterWhere(t, dryRunCapturedQuery(t, capture.queries, `FROM "collections"`))
	if where != "account_id = 9 AND id IN (3,4)" {
		t.Fatalf("report collection lookup must keep the target scope and expand the IDs: %s", where)
	}
}
