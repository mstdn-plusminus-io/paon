package api

import "strings"

func normalizeAcctInput(acct string) string {
	acct = strings.TrimSpace(acct)
	acct = strings.TrimPrefix(acct, "acct:")
	acct = strings.TrimPrefix(acct, "Acct:")
	acct = strings.TrimPrefix(acct, "ACCT:")
	return strings.TrimPrefix(acct, "@")
}
