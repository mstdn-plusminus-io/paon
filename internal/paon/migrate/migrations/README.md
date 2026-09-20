# Versioned Mastodon migrations

Each release directory contains the upstream migration IDs introduced by that
release. `paon-migrate` embeds these
files into its executable and applies the selected releases in order. The files
do not depend on the process working directory or on an installed SQL bundle.

`<upstream ID>_<phase>.sql` belongs to one of `expand`, `backfill`, `validate`,
or `contract`. Within each phase, filenames determine timestamp order. The
existing phase transactions, advisory locks, prerequisite checks, and
`schema_migrations` markers govern execution and retries. SQL statements end
with semicolons; the loader preserves PostgreSQL quoted strings and function
bodies.

Some migrations need Go code for encrypted OTP secrets, batched updates,
YAML/JSON settings, or Redis data. Their header-only files identify the Go
handler; the release runner invokes it and records the same upstream ID.
The four 4.3 duplicate backfills contain a parameterized deletion statement
followed by index creation; the runner supplies the batch size and repeats the
deletion until complete.

Files ending in `_prepare.sql` contain additive work performed during expand
before the associated migration marker can safely be recorded in contract.
These supporting files never create additional `schema_migrations` entries. Do not run the SQL
files directly: doing so would bypass the phase prerequisites, Go handlers,
and marker bookkeeping.

Release orchestration and data handlers remain in `upgrade_4_*.go` and
`upgrade_4_3_backfill.go`. Catalog reconciliation for historical database
layouts remains in `reconcile_4_3.go`; `schema.sql` is the fresh-install
snapshot, not the upgrade path.
