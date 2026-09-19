package api

import (
	"os"
	"strings"
	"testing"
)

func TestMastodon46EmailSubscriptionRouteAndPrivacyBoundary(t *testing.T) {
	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serverSource), `e.POST("/api/v1/accounts/:id/email_subscriptions", s.createAccountEmailSubscription)`) {
		t.Fatal("email subscription API route is not registered")
	}
	source, err := os.ReadFile("email_subscriptions_46.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`s.emailSubscriptionsEnabled()`,
		`!account.Local()`,
		`!s.emailSubscriptionsAllowedForAccount(c.Request().Context(), account.ID)`,
		`return c.NoContent(http.StatusNotFound)`,
	} {
		if !strings.Contains(string(source), want) {
			t.Fatalf("email subscription implementation missing %q", want)
		}
	}
}
