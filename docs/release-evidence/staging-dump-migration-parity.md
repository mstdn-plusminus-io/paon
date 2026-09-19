# Staging dump migration parity (2026-09-19)

All four native feature-branch migration binaries passed the restored-data gate against their pinned Mastodon tags on PostgreSQL 14.23. PostgreSQL 14 and 15 schema-lineage regressions also passed.

- Source: `testdata/db-dump/mastodon_stg.dump` (PostgreSQL 14.7 staging backup).
- Dump SHA-256: `4dd84ea5c29a677aeb5d27d8d74b32884eec562d36b3ada3059c934aa86f4c46`.
- Baseline: 423 migration markers, latest `20230907150100`; 1,064 accounts, 2 users and 307,992 statuses.
- Each upstream image's migration files, schema and snowflake source were byte-checked against the corresponding local Git tag.
- Each target was restored independently from the same baseline, without first passing through earlier target releases.

| Target | Upstream commit | Markers | Tables / materialized views | Compared rows | Result |
| --- | --- | ---: | ---: | ---: | --- |
| 4.3.23 | `efb25b6aa201` | 473 | 102 | 1,447,757 | PASS |
| 4.4.22 | `e5e4425d13bf` | 540 | 112 | 1,447,803 | PASS |
| 4.5.15 | `bf046ffab904` | 555 | 113 | 1,447,845 | PASS |
| 4.6.6 | `d74bbd9a3c64` | 589 | 119 | 1,447,879 | PASS |

## Comparison boundaries

Compared the public migration catalog, every stored table/materialized-view row, current sequence values and the complete `timestamp_id` function definition including its original salt. Row multisets use sorted SHA-256 row digests, with length-framed table hashing; duplicates remain significant. Both normalized snapshots were byte-identical for every release.

The only data normalization covers newly written `created_at` / `updated_at` values inside each recorded execution window: 2 notification-policy rows in every target; additionally 4 settings and 23 username-block rows in 4.5/4.6. Normalized counts are compared as well. Original timestamps and every other field remain exact. Each retry and each implementation switch was then compared separately with **raw**, unnormalized timestamps.

Instance OIDs, owners/ACLs stripped during restore, storage statistics and volatile ordinary-view results are outside the catalog/data comparison. Ordinary view definitions and their underlying stored data are checked. The source fixture has no OTP migration candidates; external Redis data was not supplied, so each run used an isolated empty Redis. This is database evidence, not a live federation or deployed-environment test.

## Defects exposed by the populated dump

- Preserve the migration-installed `timestamp_id` function layout and salt instead of rewriting it to the fresh-schema layout.
- Match the upstream notification-policy batch traversal and avoid consuming sequence values when an existing policy is skipped.
- Preserve unchanged policy timestamps and stored settings JSON key order, nested values and large integers.

## Retained regressions

- All four branches have versioned embedded SQL, bulk migration, an independently generated staging-lineage schema fixture, and PostgreSQL 14/15 goldens.
- All native staging-lineage regressions passed on PostgreSQL 14 and 15, including rerun no-op checks.
- Full migration suites passed: native 4.3/4.4/4.5 on PostgreSQL 14 (17/22/33 top-level tests), latest on PostgreSQL 14 and 15 (45 top-level tests each).
- Focused data-migration tests pin policy assignment/sequence behavior and JSON preservation.
- Comparator SQL tests cover normalization windows, unchanged timestamps, NULLs, duplicates and non-timestamp data; harness tests prevent stale success reports and incomplete cleanup.
- Package unit tests, `go vet` for affected migration/comparison packages, and `git diff --check` passed.

Rerun instructions and required private inputs: [migration parity guide](../migration-parity.md). Pinned upstream identities and non-row evidence: [`testdata/migration-parity`](../../testdata/migration-parity/). The original full snapshots and run logs remain local under `tmp/migration-parity/`.
