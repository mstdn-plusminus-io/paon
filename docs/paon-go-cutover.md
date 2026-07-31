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

1. Record the deployed Rails and Go image digests, database schema version, Redis database numbers, queue counts, and deployment timestamp. Take PostgreSQL and Redis backups.
2. Enable the maintenance/write fence at the load balancer. Scale every Rails web process and any external producer to zero. Streaming readers may remain only when they cannot enqueue jobs.
3. Keep Sidekiq running. Resolve all queued, retrying, scheduled, and dead jobs under the normal Rails admin policy. Future scheduled statuses/announcements and retries must be executed, cancelled, or moved to a later maintenance window; they must not be silently deleted.
4. Stop Sidekiq after all queue/set counts reach zero. Wait longer than the configured shutdown timeout and confirm the Sidekiq process and work sets are empty.
5. Reap Sidekiq Unique Jobs locks only after the corresponding jobs are known to be complete or cancelled. Record the before/after lock counts.
6. Run the shipped preflight against `SIDEKIQ_REDIS_URL` and the production `REDIS_NAMESPACE`:

   ```sh
   paon-cutover --producer-fenced --json
   ```

   Exit `0` and zero values for every field are required. Exit `2` is an operational refusal, not a warning.
7. Run the Go configuration/schema check and release operations before accepting traffic:

   ```sh
   paon --check-config
   paon-meili-deploy --check-config
   ```

8. Start Go worker processes first. Confirm Asynq queue connectivity, worker heartbeat, mailers, ingress, push/pull/default queues, and Redis fallback processing/visibility sets.
9. Start Go web processes, verify `/health/live`, `/health/ready`, REST, ActivityPub inbox/signature handling, WebSocket and SSE streaming, then remove the maintenance fence.
10. Reconcile scheduled statuses, announcements, poll notifications, mail, ActivityPub deliveries, imports, backups, and deletion requests against database state and the counts recorded in step 1.

## Rollback

1. Re-enable the write fence and stop Go web producers.
2. Cancel the Go worker context and wait for its 30-second graceful drain. Do not start Rails while any Asynq active/pending/retry/archive task or Paon Redis fallback ready/processing item remains.
3. If Go queues are not empty, fix or explicitly reconcile each operation by stable record ID. Never copy Asynq payloads into Sidekiq Redis keys.
4. Restore the pre-cutover application image and, only when required by the schema compatibility table, the database backup. Start Sidekiq, then Rails web, and remove the fence.
5. Reconcile the same operation families as step 10 and retain both preflight reports with the incident/deployment record.
