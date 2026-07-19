# Paon Go operational commands

The production image ships `paon-admin`, `paon-migrate`, `paon-cutover`, and `paon-meili-deploy`. Destructive account/domain commands require `--confirm`; inspection uses `--dry-run`. Commands validate the exact schema before operating.

## Ported P1 commands

| Rails/tootctl surface | Go command |
|---|---|
| `accounts create` | `paon-admin accounts create USERNAME --email ADDRESS [--password VALUE] [--confirmed] [--approved] [--role NAME]` |
| `accounts modify` | `paon-admin accounts modify USERNAME [--email ADDRESS] [--role NAME\|--remove-role] [--confirm] [--approve] [--enable\|--disable] [--disable-2fa] [--reset-password]` |
| `accounts delete` | `paon-admin accounts delete USERNAME --dry-run`, then `--confirm` |
| `accounts rotate` | `paon-admin accounts rotate USERNAME --confirm`, or `accounts rotate --all --confirm` |
| `accounts cull` | `paon-admin accounts cull [--concurrency N] [--dry-run] [DOMAIN...]` |
| `settings registrations open/approved/close` | `paon-admin settings registrations MODE [--require-reason]` |
| `domains purge` | `paon-admin domains purge DOMAIN --dry-run`, then `--confirm` |
| `domains crawl/list` | `paon-admin domains crawl [--concurrency N] [--exclude-suspended] [--format summary\|domains\|json] [START]` |
| `email_domain_blocks list/add/remove` | `paon-admin email-domain-blocks <list\|add\|remove> ...` |
| `canonical_email_blocks find/remove` | `paon-admin canonical-email-blocks <find\|remove> EMAIL` |
| `ip_blocks add/remove/export` | `paon-admin ip-blocks <add\|remove\|export> ...` |
| `statuses remove` / scheduled status vacuum | `paon-admin vacuum statuses --confirm` |
| `media remove` / media vacuum | `paon-admin vacuum media --confirm` |
| `preview_cards remove` | `paon-admin vacuum preview-cards --confirm` |
| `feeds build/clear/vacuum` | `paon-admin feeds build USERNAME`, `feeds build --all`, `feeds clear --confirm`, `feeds vacuum --confirm` |
| `cache clear/recount` | `paon-admin cache clear --confirm`, `cache recount <accounts\|statuses>` |
| `search deploy` | `paon-meili-deploy` |
| schema and seed lifecycle | `paon-migrate` |
| Sidekiq drain cutover | `paon-cutover --producer-fenced` |

Account deletion, key federation, and domain purge use the same Asynq jobs as the HTTP/admin implementation. They refuse success when durable enqueue fails. Vacuum commands call the same Go worker implementation used by periodic maintenance.

## Remaining command inventory

The following Rails surfaces remain production work and are not silently claimed as ported:

- media storage orphan scan, permission repair, targeted refresh, and remote-only purge variants;
- cache repair operations beyond Rails-compatible account/status recount;
- Meilisearch per-model reset/import/resume convenience flags beyond `paon-meili-deploy`;
- maintenance duplicate fixers, upgrade fixers, federation self-destruct, and emergency recovery;
- bulk file import formats for email-domain and IP blocks beyond the argument/stdout-compatible commands above;
- emoji archive convenience, statistics, branding/assets, and developer-only Rake tasks.

Ruby is not approved as a retained P1 runtime dependency. Until these rows are ported and integration-tested, operators must use the corresponding admin HTTP/UI surface when available or treat the operation as unsupported by the Go-only image.
