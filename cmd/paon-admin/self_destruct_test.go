package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/api"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func TestConfirmSelfDestructRequiresDomainAndSecondConfirmation(t *testing.T) {
	for name, input := range map[string]string{
		"wrong domain": "other.example\nyes\n",
		"cancelled":    "example.com\nno\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := confirmSelfDestruct(strings.NewReader(input), &bytes.Buffer{}, "example.com"); err == nil {
				t.Fatal("confirmation unexpectedly succeeded")
			}
		})
	}
	if err := confirmSelfDestruct(strings.NewReader("example.com\nyes\n"), &bytes.Buffer{}, "example.com"); err != nil {
		t.Fatalf("confirmation failed: %v", err)
	}
}

func TestRunSelfDestructPrintsSignedTokenOnlyAfterBothConfirmations(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.com", SecretKeyBase: "secret-key-base"}
	var output bytes.Buffer
	if err := runSelfDestruct(context.Background(), nil, cfg, nil, strings.NewReader("example.com\nyes\n"), &output); err != nil {
		t.Fatal(err)
	}
	line := ""
	for _, candidate := range strings.Split(output.String(), "\n") {
		if strings.HasPrefix(candidate, "SELF_DESTRUCT=") {
			line = candidate
			break
		}
	}
	if line == "" {
		t.Fatalf("SELF_DESTRUCT token missing from %q", output.String())
	}
	token := strings.TrimPrefix(line, "SELF_DESTRUCT=")
	if !api.VerifySelfDestructToken(token, cfg.SecretKeyBase, cfg.LocalDomain) {
		t.Fatal("CLI emitted an invalid SELF_DESTRUCT token")
	}
}

func TestRunSelfDestructRejectsInvalidConfiguredToken(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.com", SecretKeyBase: "secret-key-base", SelfDestruct: "example.com"}
	if err := runSelfDestruct(context.Background(), nil, cfg, []string{"check"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("invalid configured token passed check")
	}
}
