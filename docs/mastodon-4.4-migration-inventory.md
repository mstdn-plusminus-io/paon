# Mastodon 4.4 migration inventory

This inventory maps all 67 upstream migration markers between Mastodon 4.3.23
and 4.4.22 to Paon's explicit PostgreSQL phases. The executable source is
`internal/paon/migrate/upgrade_4_4.go`; `schema.sql` is the authoritative fresh
database snapshot. A marker is recorded only after its Paon-equivalent SQL or
data operation succeeds in the same transaction.

## Expand (35 markers)

Expand adds columns, tables, indexes, and `NOT VALID` constraints while old
4.3 readers can still operate. It does not record the final schema marker.

| Markers | Contract |
| --- | --- |
| `20240918233930`, `20241022214312` | fetched replies and untrusted status counters |
| `20241104082851`, `20241111141355`, `20241123224956` | annual report counts, PostgreSQL tag trends, terms of service |
| `20241205103523`, `20241206131513`, `20241213130230`, `20250103131909` | FASP providers, callbacks, subscriptions, and backfill requests |
| `20241213170027`, `20241213170043`, `20241216223425`, `20241216223446`, `20241216223852`, `20241216224211`, `20241216224229`, `20241216224507`, `20241216224520`, `20241216224813` | staged nullability checks |
| `20250108111200`, `20250129144440`, `20250221143646` | standard Web Push, replacement public-status index, announcement delivery state |
| `20250224144617`, `20250305074104`, `20250313123400`, `20250328153843` | effective ToS dates, uniqueness, age verification, instance moderation notes |
| `20250411094808`, `20250411095859`, `20250425134308`, `20250428095029`, `20250605110215` | official quote table, status-edit reference, timestamp IDs, approval policy, legacy flag |
| `20250422083912`, `20250422085027`, `20250428104538`, `20250520204643` | push ownership checks, ToS interstitial state, translated rules |

The final upstream migration also creates `fasp_follow_recommendations`. Paon
creates that additive table and indexes during expand without recording
`20250627132728`; the marker remains behind the contract fence.

## Backfill (2 markers)

| Marker | Contract |
| --- | --- |
| `20241123160722` | Read `trending_tags:all` and `trending_tags:allowed`, validate positive tag IDs and finite scores, upsert `tag_trends`, recalculate 1-based per-language ranks, commit, then delete the Redis source. Missing Redis configuration fails closed unless the operator explicitly supplies `MIGRATION_SKIP_TAG_TREND_BACKFILL=true`. |
| `20241205135901` | Remove legacy per-user/global settings rows that cannot satisfy the final global-`var` contract. Duplicate `var` values are rejected before contract. |

## Validate (22 markers)

The following markers clean invalid rows, validate staged constraints, and set
upstream `NOT NULL` contracts:

`20241210140838`, `20241212152158`, `20241212152618`,
`20241212152734`, `20241212152910`, `20241212153054`,
`20241212153202`, `20241212153254`, `20241212154231`,
`20241212154346`, `20241213170036`, `20241213170053`,
`20241216223433`, `20241216223452`, `20241216223859`,
`20241216224218`, `20241216224237`, `20241216224514`,
`20241216224530`, `20241216224825`, `20250422084214`, and
`20250422085303`.

Affected relations include account pins/aliases/deletion requests/domain
blocks/conversations/notes, action logs, announcement mutes/reactions, filters,
scheduled statuses, invite requests, markers, polls/votes, tombstones, and Web
Push owners. The phase also rejects self/duplicate quotes, duplicate global
settings, nonempty legacy imports, and enabled-2FA rows whose Active Record
Encryption value cannot be decrypted into a valid TOTP key.

## Contract (8 markers)

| Marker | Contract |
| --- | --- |
| `20241014010506` | Drop duplicate/obsolete indexes. |
| `20241205135925` | Remove settings ownership columns and install the global `var` unique index. |
| `20241205162640`, `20241205163118` | Replace WebAuthn and moderation-note foreign keys with cascade contracts. |
| `20250129144813` | Drop the old public-status index after its replacement exists. |
| `20250410144908` | Drop the drained legacy `imports` table. |
| `20250520192024` | Drop legacy `encrypted_otp_secret*` columns after OTP validation. |
| `20250627132728` | Record final schema SHA-1 `b53e3b8de778cd1b53158326b97afa9368f3237e`. |

Contract requires `--phase=contract --acknowledge-contract` after every old
web/worker process is stopped. Every phase is advisory-locked, transactional,
idempotent, and independently resumable. Fresh, 4.3.23, and 4.2.19 upgrade
paths are covered by PostgreSQL integration tests. Production admission still
requires a restored production-size rehearsal with lock duration, replica lag,
disk headroom, backup/restore time, and row-count checks retained as evidence.
