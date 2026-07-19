package main

import (
	"reflect"
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
