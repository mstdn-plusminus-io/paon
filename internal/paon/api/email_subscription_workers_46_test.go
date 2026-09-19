package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestMastodon46EmailSubscriptionBatchContract(t *testing.T) {
	if emailSubscriptionDistributionDelay != 5*time.Minute || emailSubscriptionBatchTTL != time.Hour {
		t.Fatalf("batch delay/TTL = %s/%s", emailSubscriptionDistributionDelay, emailSubscriptionBatchTTL)
	}
	if emailSubscriptionUnconfirmedTTL != 7*24*time.Hour {
		t.Fatalf("unconfirmed TTL = %s", emailSubscriptionUnconfirmedTTL)
	}
	if got := emailSubscriptionBatchKey("paon:", 42); got != "paon:email_subscriptions:42:next_batch" {
		t.Fatalf("batch key = %q", got)
	}
}

func TestEmailSubscriptionStatusEligibility(t *testing.T) {
	base := models.Status{ID: 1, AccountID: 10, Visibility: 0}
	if !emailSubscriptionStatusEligible(base) {
		t.Fatal("public root post was not eligible")
	}
	selfReply := base
	selfReply.InReplyToID = sql.NullInt64{Int64: 2, Valid: true}
	selfReply.InReplyToAccountID = sql.NullInt64{Int64: 10, Valid: true}
	if !emailSubscriptionStatusEligible(selfReply) {
		t.Fatal("self-thread reply was not eligible")
	}
	for name, status := range map[string]models.Status{
		"unlisted":      {ID: 1, AccountID: 10, Visibility: 1},
		"reblog":        {ID: 1, AccountID: 10, Visibility: 0, ReblogOfID: sql.NullInt64{Int64: 2, Valid: true}},
		"other reply":   {ID: 1, AccountID: 10, Visibility: 0, InReplyToID: sql.NullInt64{Int64: 2, Valid: true}, InReplyToAccountID: sql.NullInt64{Int64: 11, Valid: true}},
		"missing id":    {AccountID: 10, Visibility: 0},
		"missing owner": {ID: 1, Visibility: 0},
	} {
		if emailSubscriptionStatusEligible(status) {
			t.Errorf("%s status was eligible: %#v", name, status)
		}
	}
}

func TestEmailSubscriptionConfirmationMessage(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.test", LocalDomain: "example.test"}}
	subscription := models.EmailSubscription{
		ID: 1, Email: "reader@example.net", ConfirmationToken: sql.NullString{String: "token-value", Valid: true},
		Account: models.Account{ID: 2, Username: "alice", DisplayName: "Alice"},
	}
	message := emailSubscriptionConfirmationMessage(server, subscription)
	if message.To != subscription.Email || message.Subject != "Confirm your email address" || !strings.Contains(message.Body, "@alice") || !strings.Contains(message.Body, "confirmation_token=token-value") {
		t.Fatalf("confirmation message = %#v", message)
	}
	if first, second := emailSubscriptionConfirmationToken(), emailSubscriptionConfirmationToken(); first == second || len(first) < 60 || len(second) < 60 {
		t.Fatalf("confirmation tokens are not unique friendly tokens: %q / %q", first, second)
	}
}

func TestEmailSubscriptionDeliveryTaskIsStableAndPerRecipient(t *testing.T) {
	task, err := newEmailSubscriptionDeliveryTask(7, 42, []int64{9, 3, 9, 0}, "mailers")
	if err != nil {
		t.Fatal(err)
	}
	if task.Type() != asynqTaskEmailSubscriptionDelivery {
		t.Fatalf("task type = %q", task.Type())
	}
	var payload asynqEmailSubscriptionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Version != asynqPayloadVersion43 || payload.SubscriptionID != 7 || payload.AccountID != 42 || len(payload.StatusIDs) != 2 || payload.StatusIDs[0] != 3 || payload.StatusIDs[1] != 9 {
		t.Fatalf("delivery payload = %#v", payload)
	}
	wantBatchID := emailSubscriptionDeliveryBatchID(42, []int64{3, 9})
	if payload.BatchID != wantBatchID || emailSubscriptionDeliveryBatchID(42, []int64{9, 3, 9}) != wantBatchID {
		t.Fatalf("batch id is not stable: %#v", payload)
	}
	if got := emailSubscriptionDeliveryMarkerKey("paon:", 7, wantBatchID); !strings.HasPrefix(got, "paon:email_subscriptions:delivery:7:") {
		t.Fatalf("delivery marker = %q", got)
	}
}

func TestEmailSubscriptionDeliveryEnqueueAttemptsLaterRecipientsAfterFailure(t *testing.T) {
	subscriptions := []models.EmailSubscription{{ID: 1}, {ID: 2}, {ID: 3}}
	called := []int64{}
	acknowledged := false
	err := dispatchEmailSubscriptionBatch(subscriptions, func(subscriptionID int64) error {
		called = append(called, subscriptionID)
		if subscriptionID == 2 {
			return errors.New("queue unavailable")
		}
		return nil
	}, func() error {
		acknowledged = true
		return nil
	})
	if err == nil || acknowledged || len(called) != 3 || called[0] != 1 || called[1] != 2 || called[2] != 3 {
		t.Fatalf("enqueue calls = %#v acknowledged=%v err=%v", called, acknowledged, err)
	}
	if err := dispatchEmailSubscriptionBatch(subscriptions, func(int64) error { return nil }, func() error {
		acknowledged = true
		return nil
	}); err != nil || !acknowledged {
		t.Fatalf("successful batch was not acknowledged: acknowledged=%v err=%v", acknowledged, err)
	}
}

func TestEmailSubscriptionNotificationHTMLRendersPostImages(t *testing.T) {
	server := &Server{cfg: config.Config{Scheme: "https", WebDomain: "example.test", LocalDomain: "example.test"}}
	subscription := models.EmailSubscription{ID: 7, Email: "reader@example.net", Account: models.Account{ID: 42, Username: "alice", DisplayName: "Alice"}}
	status := models.Status{
		ID: 9, AccountID: 42, Text: "A post with an image", Account: subscription.Account,
		MediaAttachments: []models.MediaAttachment{{
			ID: 11, Type: 0,
			FileFileName: sql.NullString{String: "photo.jpg", Valid: true},
			Processing:   sql.NullInt64{Int64: 2, Valid: true},
			Description:  sql.NullString{String: "A descriptive alt", Valid: true},
		}},
	}
	message := emailSubscriptionNotificationMessage(server, subscription, []models.Status{status})
	for _, want := range []string{`<img src="`, `alt="A descriptive alt"`, `A post with an image`, `/unsubscribe?token=`} {
		if !strings.Contains(message.HTMLBody, want) {
			t.Fatalf("HTML notification missing %q: %s", want, message.HTMLBody)
		}
	}
	if !message.Bulk || message.HTMLBody == "" {
		t.Fatalf("notification message = %#v", message)
	}
}
