package api

import (
	"context"
	"testing"
	"time"
)

func TestWebhookDeliveryRetryDelayIsBounded(t *testing.T) {
	if got := webhookDeliveryRetryDelay(1); got != 15*time.Second {
		t.Fatalf("delay(1) = %s", got)
	}
	if got := webhookDeliveryRetryDelay(20); got != 960*time.Second {
		t.Fatalf("delay(20) = %s", got)
	}
}

func TestEnqueueWebhookDeliveryRetryJobSkipsInvalidJobs(t *testing.T) {
	server := &Server{}
	if err := server.enqueueWebhookDeliveryRetryJob(context.Background(), webhookDeliveryRetryJob{}); err != nil {
		t.Fatalf("empty retry job returned error: %v", err)
	}
	if err := server.enqueueWebhookDeliveryRetryJob(context.Background(), webhookDeliveryRetryJob{WebhookID: 1}); err != nil {
		t.Fatalf("bodyless retry job returned error: %v", err)
	}
}
