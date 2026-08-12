package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/api"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

func runSelfDestruct(ctx context.Context, operations *api.Operations, cfg config.Config, args []string, input io.Reader, output io.Writer) error {
	action := "start"
	if len(args) > 0 {
		action = args[0]
	}
	if len(args) > 1 || action != "start" && action != "dry-run" && action != "status" && action != "check" {
		return errors.New("usage: paon-admin self-destruct [start|dry-run|status|check]")
	}
	if action == "dry-run" {
		inventory, err := operations.SelfDestructInventory(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "dry_run=true unsuspended=%d deletion_requested=%d known_inboxes=%d delivery_batches_per_account=%d\n", inventory.Unsuspended, inventory.DeletionRequested, inventory.KnownInboxes, inventory.DeliveryBatchesPerAccount)
		fmt.Fprintf(output, "would_process_unsuspended=%d would_process_deletion_requested=%d would_process_total=%d\n", inventory.WouldProcessUnsuspended, inventory.WouldProcessRequested, inventory.WouldProcessUnsuspended+inventory.WouldProcessRequested)
		fmt.Fprintf(output, "queue_pending=%d redis_used_memory=%d redis_memory_reference_limit=%d paused=%t", inventory.QueuePending, inventory.RedisUsedMemory, inventory.RedisMemoryReferenceLimit, inventory.Paused)
		if inventory.PauseReason != "" {
			fmt.Fprintf(output, " pause_reason=%q", inventory.PauseReason)
		}
		fmt.Fprintln(output)
		return nil
	}
	configured := strings.TrimSpace(cfg.SelfDestruct) != ""
	valid := api.VerifySelfDestructToken(cfg.SelfDestruct, cfg.SecretKeyBase, cfg.LocalDomain)
	if action == "status" || action == "check" || valid {
		fmt.Fprintf(output, "configured=%t enabled=%t local_domain=%s\n", configured, valid, strings.TrimSpace(cfg.LocalDomain))
		if !valid {
			if configured {
				return errors.New("SELF_DESTRUCT is configured but its signature, purpose, or local domain is invalid")
			}
			if action == "check" {
				return errors.New("SELF_DESTRUCT is not configured")
			}
			return nil
		}
		status, err := operations.SelfDestructStatus(ctx)
		if err != nil {
			return err
		}
		writeSelfDestructStatus(output, status)
		return nil
	}
	if err := confirmSelfDestruct(input, output, strings.TrimSpace(cfg.LocalDomain)); err != nil {
		return err
	}
	token, err := api.GenerateSelfDestructToken(cfg.SecretKeyBase, cfg.LocalDomain)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "Add the following environment variable and restart every Paon web/worker process:")
	fmt.Fprintf(output, "SELF_DESTRUCT=%s\n", token)
	fmt.Fprintln(output, "After restart, run `paon-admin self-destruct check` and then `paon-admin self-destruct status` until complete=true.")
	return nil
}

func confirmSelfDestruct(input io.Reader, output io.Writer, localDomain string) error {
	if localDomain == "" {
		return errors.New("LOCAL_DOMAIN is required")
	}
	reader := bufio.NewReader(input)
	fmt.Fprint(output, "Type the local domain to confirm: ")
	domain, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(domain) != localDomain {
		return errors.New("domains do not match; self-destruct initiation stopped")
	}
	fmt.Fprintln(output, "WARNING: This operation is irreversible. Local data is retained only for archive/export, but federation state will be permanently removed and the running server will be intentionally unusable.")
	fmt.Fprint(output, "Are you sure you want to proceed? Type yes: ")
	confirmation, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.ToLower(strings.TrimSpace(confirmation)) != "yes" {
		return errors.New("operation cancelled; self-destruct will not begin")
	}
	return nil
}

func writeSelfDestructStatus(output io.Writer, status api.SelfDestructStatus) {
	fmt.Fprintf(output, "pending_unsuspended=%d pending_deletion_requested=%d\n", status.PendingUnsuspended, status.PendingDeletionRequested)
	fmt.Fprintf(output, "queue_pending=%d queue_active=%d queue_retry=%d queue_scheduled=%d queue_archived=%d\n", status.QueuePending, status.QueueActive, status.QueueRetry, status.QueueScheduled, status.QueueArchived)
	fmt.Fprintf(output, "redis_used_memory=%d redis_memory_reference_limit=%d paused=%t", status.RedisUsedMemory, status.RedisMemoryReferenceLimit, status.Paused)
	if status.PauseReason != "" {
		fmt.Fprintf(output, " pause_reason=%q", status.PauseReason)
	}
	fmt.Fprintln(output)
	fmt.Fprintf(output, "complete=%t\n", status.Complete)
}
