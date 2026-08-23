# Paon Mastodon 4.5 drain cutover runbook

Paon supports a drain cutover. Sidekiq and Asynq are intentionally not treated as wire-compatible queues. Do not run Rails producers and Go producers at the same time.

## Abort conditions

Abort when any of these conditions is true:

- the database or Redis backup is incomplete;
- Rails web/streaming/scheduler producers cannot be fenced;
- a Sidekiq queue, retry, scheduled, dead, process, work, or unique-lock count is nonzero;
- a Go worker has accepted work before the Sidekiq preflight passes;
- the Go schema/config/asset readiness check fails;
- the Redis namespace dry run reports a collision, or legacy tag-trend source
  keys cannot be inventoried;
- a configured legacy DynamoDB quote source cannot be counted and dry-run
  validated after the PostgreSQL contract;
- rollback would leave pending Asynq or Redis fallback delivery work.

## Cutover

1. Record the deployed image digests, database schema version, Redis database
   numbers and key prefixes, queue counts, legacy tag-trend counts, legacy
   DynamoDB quote counts (when configured), and deployment timestamp. Take
   PostgreSQL, Redis, and applicable DynamoDB backups. Before the maintenance
   window, restore the PostgreSQL and Redis backups into an isolated
   environment, run the complete staged migration followed by
   `paon-migrate --check`, and retain restore time, locks, replica lag, disk
   headroom, checksums, and row counts. Re-running an already completed phase is
   a no-op.
2. Enable the maintenance/write fence at the load balancer. Scale every Rails web process and any external producer to zero. Streaming readers may remain only when they cannot enqueue jobs.
3. Keep Sidekiq running. Resolve all queued, retrying, scheduled, and dead jobs under the normal Rails admin policy. Future scheduled statuses/announcements and retries must be executed, cancelled, or moved to a later maintenance window; they must not be silently deleted.
4. Stop Sidekiq after all queue/set counts reach zero. Wait longer than the configured shutdown timeout and confirm the Sidekiq process and work sets are empty.
5. Reap Sidekiq Unique Jobs locks only after the corresponding jobs are known to be complete or cancelled. Record the before/after lock counts.
6. Run the shipped preflight against `SIDEKIQ_REDIS_URL` and the old production
   `REDIS_NAMESPACE` (when one existed):

   ```sh
   paon-cutover --producer-fenced --json
   ```

   Exit `0` and zero values for every field are required. Exit `2` is an
   operational refusal, not a warning. With every old and new Paon process
   stopped, migrate any former namespace before the 4.4 tag-trend backfill:

   ```sh
   paon-admin redis namespace-cutover --prefix OLD_PREFIX --dry-run
   paon-admin redis namespace-cutover --prefix OLD_PREFIX --confirm
   ```

   Preserve an explicit `MEILI_PREFIX` when search indexes used the former
   prefix. Remove `REDIS_NAMESPACE` after the confirmed cutover; 4.4 runtime
   configuration rejects it.

7. For a supported 4.4.22 (`20250627132728`), 4.3.23 (`20241007071624`), or
   4.2.19 (`20230907150100`) source, apply expand. Before backfill, verify that
   every old web/worker/streaming process is stopped. For a 4.2 source, pin
   `OTP_SECRET` and the three `ACTIVE_RECORD_ENCRYPTION_*` values in the
   backup/restore inventory; the phase sequence completes the reviewed 4.3 and
   4.4 inventories before 4.5. Complete the idempotent backfill/validation
   phases, verify all 554 target markers, schema SHA-1
   `801766beefdd9b1d55fe6f8bf3bed91392aebab1`, and captured row counts, then
   explicitly acknowledge every pending irreversible contract:

   ```sh
   paon-migrate --phase=expand
   paon-migrate --phase=backfill
   paon-migrate --phase=validate
   paon-migrate --phase=contract --acknowledge-contract
   ```

   Each phase holds the advisory lock in a separate transaction and verifies
   all prior phase markers. A 4.3/4.2 source may require the same command for
   each successive version; re-running a completed phase is a no-op and the
   migration runner advances only after the preceding inventory is complete.
   The 4.4 backfill imports unnamespaced
   `trending_tags:all` and `trending_tags:allowed`, commits PostgreSQL, and then
   removes those Redis sources. Use `MIGRATION_SKIP_TAG_TREND_BACKFILL=true`
   only with recorded proof that both sets are empty or intentionally
   disposable. Do not use `MIGRATION_IGNORE_INVALID_OTP_SECRET=true` until every
   reported user ID has been investigated and the data-loss decision recorded.
   No old writer may return after backfill begins.

8. If this installation used Paon's legacy DynamoDB quote extension, keep all
   writers fenced and perform the one-way, read-only-source cutover after the
   PostgreSQL 4.5 schema is final:

   ```sh
   paon-admin quotes cutover --dry-run
   paon-admin quotes cutover --confirm
   ```

   Reconcile candidates/imported/skipped with the source count and investigate
   every skipped row. PostgreSQL `quotes` is the only 4.5 runtime source of
   truth. Retain DynamoDB only for the approved rollback window; never enable a
   dual-write path.

9. Remove migration-only `DYNAMODB_*`, `OTP_SECRET`, and `REDIS_NAMESPACE`
   values from normal web/worker configuration, then run the Go
   configuration/schema checks before accepting traffic:

   ```sh
   paon --check-config
   paon-meili-deploy --check-config
   ```

10. Start Go worker processes first. Confirm Asynq queue connectivity, worker
    heartbeat, mailers, ingress, push/pull/default/FASP queues, and Redis
    fallback processing/visibility sets.
11. Start Go web processes, verify `/health/live`, `/health/ready`, REST,
    ActivityPub inbox/signature handling, WebSocket and SSE streaming, then
    remove the maintenance fence.
12. Reconcile scheduled statuses, announcements, polls, mail, ActivityPub
    deliveries, deletion requests, tag trends, official quotes, ToS state,
    annual reports, FASP state, notification policies, Web Push owners, and
    enabled-2FA counts against the values recorded in step 1.

## Rollback

1. Re-enable the write fence and stop Go web producers.
2. Cancel the Go worker context and wait for its 30-second graceful drain. Do not start Rails while any Asynq active/pending/retry/archive task or Paon Redis fallback ready/processing item remains.
3. If Go queues are not empty, fix or explicitly reconcile each operation by stable record ID. Never copy Asynq payloads into Sidekiq Redis keys.
4. After a committed expand phase, an old binary may continue only for the
   specifically rehearsed additive window; do not remove added objects during
   rollback. Once backfill moves tag-trend state or OTP ciphertext, Redis keys
   are unprefixed, legacy quotes are imported, or contract drops imports,
   settings ownership, legacy OTP, E2EE, superseded quote/follow indexes, or
   other obsolete objects,
   application-only rollback is unsafe. Restore the matching pre-upgrade
   PostgreSQL and Redis backups and secret set; restore the DynamoDB source only
   if it was separately modified outside Paon's read-only importer.
5. Restore the pre-cutover application image only after the required database restore completes. Start Sidekiq, then Rails web, and remove the fence.
6. Reconcile the same operation families as cutover step 12 and retain the
   backup/restore rehearsal, migration, namespace, quote, and queue preflight
   reports with the incident/deployment record.
