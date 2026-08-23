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

func TestAccountMergingKeepsMastodon4515AppealsAndWarningsSeparate(t *testing.T) {
	contains := func(tables []string, wanted string) bool {
		for _, table := range tables {
			if table == wanted {
				return true
			}
		}
		return false
	}
	if !contains(accountMergingOwnedTables, "appeals") || contains(accountMergingTargetTables, "appeals") {
		t.Fatalf("appeals must move only through account_id: owned=%#v target=%#v", accountMergingOwnedTables, accountMergingTargetTables)
	}
	if !contains(accountMergingTargetTables, "account_warnings") || contains(accountMergingOwnedTables, "account_warnings") {
		t.Fatalf("account warnings must move only through target_account_id: owned=%#v target=%#v", accountMergingOwnedTables, accountMergingTargetTables)
	}
}
