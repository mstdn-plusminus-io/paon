# Paon self-destruct operations

Paon's Mastodon 4.3-compatible self-destruct workflow broadcasts a signed
ActivityPub `Delete` for every local actor and then marks that actor as locally
suspended. It does **not** delete account, user, status, media, or backup rows.
That retained data is the temporary archive/export window; the instance is not
usable as a social server after the workflow begins.

There is no supported abort, rollback, or return to service after enabling
self-destruct. Restoring service means building a new instance from a known-good
pre-self-destruct backup, with a separately reviewed federation recovery plan.
Removing `SELF_DESTRUCT` is not a rollback.

## Before production

1. Take and restore-test database and object-storage backups.
2. Confirm users can sign in and reach account edit, backup/export, login
   activity, password reset, confirmation, and two-factor authentication paths.
3. Rehearse against a staging restore with production-sized local-account and
   known-inbox cardinalities. Use the same Redis memory limit and worker
   concurrency as production.
4. Run `paon-admin self-destruct dry-run`. Record the unsuspended and
   deletion-requested counts, known-inbox count, batches per account, queue
   backlog, Redis memory reference, and pause state. One run may select at most
   50 unsuspended accounts and, separately, 50 suspended accounts with a pending
   deletion request.
5. In the rehearsal, verify that every delivery batch has at most 1,000 inboxes,
   retries do not repeat an inbox after its delivery marker is recorded, worker
   restarts resume remaining accounts, and account/user rows remain present.
6. Have an explicit, separately approved plan for deleting the retained
   infrastructure only after completion and the archive/export window ends.

## Enable

Run:

```bash
paon-admin self-destruct start
```

The command is always interactive. It requires the exact `LOCAL_DOMAIN`, prints
the irreversible-state warning, and then requires a second `yes`. It only emits
a purpose-signed token; it does not change the database or enqueue deliveries.

Set the emitted value in the environment of **every** web and worker process:

```dotenv
SELF_DESTRUCT=<signed token>
```

Restart all Paon processes. A token signed for another purpose, secret, or
domain is rejected and leaves Paon in normal mode. After restart, run:

```bash
paon-admin self-destruct check
```

The web process returns HTTP 410 for normal UI, REST, and ActivityPub traffic.
Login/session, confirmation, password reset, two-factor authentication,
OmniAuth callbacks, account edit, backups/exports, and login activity remain
available for the archive/export window. Health and metrics endpoints remain
available to operators.

## Scheduler safety and restart behavior

While the signed mode is active, Paon starts only the self-destruct recurring
scheduler; queue consumers remain online to drain deliveries and retries.
Before selecting accounts, and again before the deletion-requested group, it
pauses when either condition is true:

- total Asynq pending backlog across all queues is greater than 10,000;
- Redis `used_memory` is greater than 50% of the smaller positive value among
  configured `maxmemory` and `total_system_memory`.

Each account is marked locally suspended only after every known-inbox task has
been accepted. An enqueue or database failure leaves it eligible for the next
minute. Delivery tasks have deterministic account/inbox IDs, and successful
deliveries record compact Redis markers. These make scheduler and worker
restarts resumable while suppressing duplicate external effects. ActivityPub
`Delete` is also idempotent at a conforming receiver; like Mastodon, delivery is
still an at-least-once distributed operation if a process dies between a remote
success and recording its local marker.

## Status and completion

Run periodically and after every worker restart:

```bash
paon-admin self-destruct status
```

`paused=true` is an automatic safety pause, not a failure or an abort. Reduce
the pending queue below 10,001 or resolve Redis memory pressure, then leave the
workers running; the next scheduler minute resumes automatically.

Completion is reported only when all of the following are zero:

- unsuspended local accounts;
- suspended local accounts that still have a deletion request;
- pending, active, scheduled, retry, and archived Asynq work.

The command then prints `complete=true`. Before decommissioning, retain the
final status output, verify the promised archive/export window has elapsed, and
take a final backup. Infrastructure and retained data removal are separate,
explicitly approved operational actions; Paon never performs them as part of
self-destruct.
