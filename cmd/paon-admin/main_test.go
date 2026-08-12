package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCommandFlagArgsSupportsTootctlStylePositionalFirst(t *testing.T) {
	got := commandFlagArgs([]string{"alice", "--email", "alice@example.test", "--confirmed"})
	want := []string{"--email", "alice@example.test", "--confirmed", "alice"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commandFlagArgs = %#v, want %#v", got, want)
	}
	alreadyFlagsFirst := []string{"--confirm", "alice"}
	if got := commandFlagArgs(alreadyFlagsFirst); !reflect.DeepEqual(got, alreadyFlagsFirst) {
		t.Fatalf("flags-first args = %#v", got)
	}
}

func TestFeedsBuildSupportsMastodon44SkipFilledTimelineFlag(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		`"skip-filled-timelines"`,
		`"skip-filled-timeline"`,
		`BuildHomeFeedsWithOptions`,
		`SkipFilledTimelines: skipFilledTimelines`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("paon-admin feeds build missing %q", want)
		}
	}
}

func TestDatabaseCommandsRequirePostgreSQL13BeforeSchemaChecks(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	availabilityCheck := strings.Index(body, `paondb.Available(database)`)
	versionCheck := strings.Index(body, `paondb.RequireSupportedVersion(database)`)
	schemaCheck := strings.Index(body, `paondb.SchemaAvailable(database)`)
	if availabilityCheck < 0 || versionCheck < availabilityCheck || schemaCheck < versionCheck {
		t.Fatal("paon-admin must check database availability, PostgreSQL version, and schema in that order")
	}
}
