package api

import (
	"regexp"
	"strings"
	"unicode"
)

var railsRemoteUsernamePattern = regexp.MustCompile(`(?i)^[a-z0-9_]+([.-]+[a-z0-9_]+)*$`)

func railsAccountUsernameValue(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}
