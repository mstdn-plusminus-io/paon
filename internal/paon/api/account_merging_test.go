package api

import "testing"

func TestAccountMergingIncludesMastodon4422AccountReferences(t *testing.T) {
	assertTable := func(name string, tables []string, want string) {
		t.Helper()
		for _, table := range tables {
			if table == want {
				return
			}
		}
		t.Fatalf("%s does not include %q: %#v", name, want, tables)
	}

	assertTable("account_id tables", accountMergingOwnedTables, "appeals")
	assertTable("account_id tables", accountMergingOwnedTables, "quotes")
	assertTable("target_account_id tables", accountMergingTargetTables, "account_warnings")
}
