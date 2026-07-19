package api

import (
	"os"
	"strings"
	"testing"
)

func TestAccountSerializerPreloadsIncludeMovedAccount(t *testing.T) {
	src, err := os.ReadFile("account_preloads.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Preload("AccountStat")`,
		`Preload("User.Role")`,
		`Preload("MovedToAccount.AccountStat")`,
		`Preload("MovedToAccount.User.Role")`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("accountSerializerPreloads missing %s", want)
		}
	}
}

func TestAccountRelationSerializerPreloadsIncludeMovedAccount(t *testing.T) {
	src, err := os.ReadFile("account_preloads.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Preload(relation + ".AccountStat")`,
		`Preload(relation + ".User.Role")`,
		`Preload(relation + ".MovedToAccount.AccountStat")`,
		`Preload(relation + ".MovedToAccount.User.Role")`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("accountRelationSerializerPreloads missing %s", want)
		}
	}
}
