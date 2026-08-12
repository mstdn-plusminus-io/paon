# Paon Go drain cutover runbook

Paon supports a drain cutover. Sidekiq and Asynq are intentionally not treated as wire-compatible queues. Do not run Rails producers and Go producers at the same time.

## Abort conditions

Abort when any of these conditions is true:

- the database or Redis backup is incomplete;
- Rails web/streaming/scheduler producers cannot be fenced;
- a Sidekiq queue, retry, scheduled, dead, process, work, or unique-lock count is nonzero;
- a Go worker has accepted work before the Sidekiq preflight passes;
- the Go schema/config/asset readiness check fails;
- rollback would leave pending Asynq or Redis fallback delivery work.

## Cutover

1. Record the deployed Rails and Go image digests, database schema version, Redis database numbers, queue counts, and deployment timestamp. Take PostgreSQL and Redis backups. Before the maintenance window, restore the PostgreSQL backup into an isolated database, run the complete staged migration there followed by `paon-migrate --check`, and retain the measured restore time and checksum/count report. On a live `20230907150100` database, the additive phase may be applied before the write fence with `paon-migrate --phase=expand`; retain its lock duration, replica lag, disk headroom, and table rewrite measurements. Re-running expand is a no-op.
2. Enable the maintenance/write fence at the load balancer. Scale every Rails web process and any external producer to zero. Streaming readers may remain only when they cannot enqueue jobs.
3. Keep Sidekiq running. Resolve all queued, retrying, scheduled, and dead jobs under the normal Rails admin policy. Future scheduled statuses/announcements and retries must be executed, cancelled, or moved to a later maintenance window; they must not be silently deleted.
4. Stop Sidekiq after all queue/set counts reach zero. Wait longer than the configured shutdown timeout and confirm the Sidekiq process and work sets are empty.
5. Reap Sidekiq Unique Jobs locks only after the corresponding jobs are known to be complete or cancelled. Record the before/after lock counts.
6. Run the shipped preflight against `SIDEKIQ_REDIS_URL` and the production `REDIS_NAMESPACE`:

   ```sh
   paon-cutover --producer-fenced --json
   ```

   Exit `0` and zero values for every field are required. Exit `2` is an operational refusal, not a warning.

7. For a `20230907150100` database, verify that all 4.2 web/worker/streaming processes are stopped and pin `OTP_SECRET` and the three `ACTIVE_RECORD_ENCRYPTION_*` values in the backup/restore secret inventory. Complete the idempotent backfill/validation phases, verify their migration markers against the separately captured checksum/count report, then explicitly acknowledge the irreversible contract phase:

   ```sh
   paon-migrate --phase=backfill
   paon-migrate --phase=validate
   paon-migrate --phase=contract --acknowledge-contract
   ```

   Each phase holds the advisory lock in a separate transaction and verifies all prior phase markers. Validate finalizes mention constraints, report FKs, and timestamp defaults only after old writers are fenced. Do not use `MIGRATION_IGNORE_INVALID_OTP_SECRET=true` until every reported user ID has been investigated and the decision to leave that account's new OTP value null is recorded. Paon reads legacy Rails and `paon-go-totp:` values but writes only `users.otp_secret`; therefore no 4.2 writer may return after backfill begins.

8. Run the Go configuration/schema check and release operations before accepting traffic:

   ```sh
   paon --check-config
   paon-meili-deploy --check-config
   ```

9. Start Go worker processes first. Confirm Asynq queue connectivity, worker heartbeat, mailers, ingress, push/pull/default queues, and Redis fallback processing/visibility sets.
10. Start Go web processes, verify `/health/live`, `/health/ready`, REST, ActivityPub inbox/signature handling, WebSocket and SSE streaming, then remove the maintenance fence.
11. Reconcile scheduled statuses, announcements, poll notifications, mail, ActivityPub deliveries, imports, backups, deletion requests, migrated notification policies, and enabled-2FA counts against database state and the counts recorded in step 1.

## Rollback

1. Re-enable the write fence and stop Go web producers.
2. Cancel the Go worker context and wait for its 30-second graceful drain. Do not start Rails while any Asynq active/pending/retry/archive task or Paon Redis fallback ready/processing item remains.
3. If Go queues are not empty, fix or explicitly reconcile each operation by stable record ID. Never copy Asynq payloads into Sidekiq Redis keys.
4. After a committed expand phase, the old binary may continue against the additive schema; do not try to remove the added objects during rollback. Once backfill writes OTP ciphertext or the contract deletes E2EE/obsolete objects and removes scopes, application-only rollback is unsafe: restore the pre-upgrade PostgreSQL backup and the matching secret set. Never recreate empty E2EE tables as a substitute for restore.
5. Restore the pre-cutover application image only after the required database restore completes. Start Sidekiq, then Rails web, and remove the fence.
6. Reconcile the same operation families as cutover step 11 and retain the backup/restore rehearsal, migration, and both preflight reports with the incident/deployment record.
