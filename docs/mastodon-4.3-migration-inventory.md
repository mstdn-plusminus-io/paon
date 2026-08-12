# Mastodon 4.3 migration inventory

The authoritative empty-database result is `internal/paon/migrate/schema.sql`. The forward path is implemented by `internal/paon/migrate/upgrade_4_3*.go`; every row below is inserted independently into `schema_migrations` only after its Paon step succeeds. Modified historical Rails migrations are not copied.

| Upstream version | Paon phase and implementation                                                              |
| ---------------- | ------------------------------------------------------------------------------------------ |
| `20231006183200` | expand: `preview_cards_statuses.url`                                                       |
| `20231018192110` | backfill: lowest-ID WebAuthn duplicate cleanup, then unique user/nickname index            |
| `20231018193209` | backfill: lowest-ID account-alias duplicate cleanup, then unique account/URI index         |
| `20231018193355` | backfill: lowest-ID custom-filter-status duplicate cleanup, then unique tuple index        |
| `20231018193659` | backfill: lowest-ID identity duplicate cleanup, then unique UID/provider index             |
| `20231210154528` | expand: `users.otp_secret`                                                                 |
| `20231211234923` | expand: `follow_recommendation_mutes`, indexes, cascade FKs                                |
| `20231212073317` | expand: account-summary language/sensitive query index                                     |
| `20231222100226` | expand: email-domain approval flag                                                         |
| `20240109103012` | backfill: `fr-QC` to `fr-CA`                                                               |
| `20240111033014` | expand: generated annual reports and account/year uniqueness                               |
| `20240217171534` | validate: remove database defaults from status-pin timestamps after old writers are fenced |
| `20240221195424` | expand: notification `filtered` flag                                                       |
| `20240221195828` | expand: notification requests in the intermediate dismissed/non-null-status shape          |
| `20240221211359` | expand: notification-request timestamp IDs                                                 |
| `20240222193403` | expand: notification permissions and cascade account FKs                                   |
| `20240222203722` | expand: legacy boolean notification-policy shape                                           |
| `20240227191620` | expand: ordered partial filtered-notification index                                        |
| `20240304090449` | backfill: interaction settings to policy, updating conflicts                               |
| `20240307180905` | backfill: strict legacy Rails/Paon OTP dispatch and Active Record re-encryption            |
| `20240310123453` | expand: rule hint                                                                          |
| `20240312100644` | expand: relationship-severance events                                                      |
| `20240312105620` | expand: severed relationships, uniqueness, query indexes, cascade FKs                      |
| `20240320140159` | expand: account relationship-severance events in intermediate count shape                  |
| `20240320163441` | expand: nullable notification-request last status                                          |
| `20240321160706` | backfill: interaction-settings second pass inserts missing policies only                   |
| `20240322125607` | expand: follower/following severance counts                                                |
| `20240322130318` | contract: remove obsolete aggregate relationship count                                     |
| `20240322161611` | contract: remove obsolete user admin/moderator flags                                       |
| `20240510192043` | expand: notification-policy account FK is created with final cascade behavior              |
| `20240513095755` | expand: notification group key                                                             |
| `20240513123807` | expand: partial account/group-key index                                                    |
| `20240522041528` | expand: preview-card author, partial index, nullifying FK                                  |
| `20240603195202` | backfill: exact `read:me` scope token to `profile`                                         |
| `20240607093446` | validate: not-valid mention status check                                                   |
| `20240607093954` | validate: status check, `NOT NULL`, temporary-check removal                                |
| `20240607094603` | validate: not-valid mention account check                                                  |
| `20240607094856` | validate: account check, `NOT NULL`, temporary-check removal                               |
| `20240712064044` | contract: delete dismissed requests and remove dismissed column/index                      |
| `20240713171841` | expand: nullable report application and not-valid nullifying FK                            |
| `20240713171909` | validate: report application FK                                                            |
| `20240720140205` | contract: remove E2EE tables and `accounts.devices_url`                                    |
| `20240724181224` | expand: OAuth PKCE columns                                                                 |
| `20240808114841` | expand: notification-policy v2 integer columns/defaults                                    |
| `20240808124338` | backfill: boolean policy to v2 enum                                                        |
| `20240808124339` | backfill: post-deployment boolean-to-enum overwrite                                        |
| `20240808125420` | contract: remove legacy policy boolean columns                                             |
| `20240909014637` | expand: nullable attribution-domain array with empty-array default                         |
| `20240916190140` | contract: remove exact `crypto` scope tokens                                               |
| `20241007071624` | contract: permission FKs are already in final cascade shape; record target version         |

The reverted `20240217175251` attachment-size bigint experiment is intentionally absent. Expand, backfill, validate, and contract each run in a separate advisory-locked PostgreSQL transaction. A failure rolls back the active phase and its newly recorded versions while earlier committed phases remain resumable. The contract transaction executes the complete final schema guard before commit, so destructive DDL and the target marker roll back together if the final shape is incomplete.
