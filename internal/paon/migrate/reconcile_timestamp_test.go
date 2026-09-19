package migrate

import (
	"strings"
	"testing"
)

func TestTimestampIDPreservesBothAuthoritativeLayouts(t *testing.T) {
	salt := strings.Repeat("a", 32)
	fresh := "before md5(table_name || '" + salt + "' || time_part::text) after"
	installed := "before md5(table_name ||\n          '" + salt + "' ||\n          time_part::text\n        ) after"
	for _, reference := range []string{fresh, installed} {
		for _, current := range []string{fresh, installed} {
			if !supportedTimestampIDBody(current, reference, salt) {
				t.Fatal("authoritative function layout would be rewritten")
			}
		}
		for _, current := range []string{
			strings.Replace(fresh, salt, strings.Repeat("b", 32), 1),
			strings.Replace(fresh, "time_part::text", "'constant'", 1),
			fresh + " changed",
		} {
			if supportedTimestampIDBody(current, reference, salt) {
				t.Fatal("changed function body was accepted as authoritative")
			}
		}
	}
}
