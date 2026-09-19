package migrate

import (
	"strings"
	"testing"
)

func TestSplitSQLStatementsPreservesPostgreSQLQuotedBodies(t *testing.T) {
	source := `
-- header ; comment
SET client_encoding = 'UTF8';
CREATE FUNCTION timestamp_id(table_name text) RETURNS bigint
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM ';' || table_name;
  RETURN 1;
END
$$;
INSERT INTO settings (var, value) VALUES ('theme', 'one;two');
CREATE TABLE "semi;colon" (id bigint /* nested ; /* still ; */ done */);
`
	statements := splitSQLStatements(source)
	if len(statements) != 4 {
		t.Fatalf("splitSQLStatements() returned %d statements: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[1], "PERFORM ';'") || !strings.Contains(statements[1], "RETURN 1;") {
		t.Fatalf("function body was split: %q", statements[1])
	}
	if !strings.Contains(statements[2], "one;two") {
		t.Fatalf("quoted value was split: %q", statements[2])
	}
}
