# Mastodon 4.5.15 互換化 TODO

## 目的と互換性境界

Paon を Mastodon v4.4.22 の実装契約から v4.5.15 の実装契約へ更新する。
対象は PostgreSQL schema、REST/OAuth、ActivityPub、SSE/WebSocket、security、
React/Rspack UI、i18n、Asynq jobs、search、media、mail、runtime configuration、
operations である。

公式 Quote は Mastodon 4.5 で送信まで含む正式機能になった。Paon 独自の
DynamoDB Quote を並行運用せず、公式 PostgreSQL `quotes` table、REST entity、
FEP-044f ActivityPub、notification、streaming、composer を唯一の実行契約にする。
DynamoDB は 4.4 で用意した停止時間付き一方向 cutover command から読み取る場合に
限り、通常の web/worker/API/AP 実行経路からは読まない・書かない・dual-write しない。

UI は Mastodon 4.5 の markup、layout、CSS を直接取り込まない。Mastodon 4.2.19 を
起点に Paon 固有機能とともに拡張してきた status、compose、timeline、settings、
admin、theme のデザインを維持し、4.5 の操作、状態、権限、アクセシビリティ契約を
意味的に追加する。Rails、Ruby、Sidekiq、Vite、standalone Node streaming server、
port 4000、GORM AutoMigrate、Makefile は導入しない。

## 比較基準

- upstream start: `v4.4.22` / `e5e4425d13bfee9572d8d384a19553caad7503d1`
- upstream target: `v4.5.15` / `bf046ffab90464faff67f98a0fbd9fca3058e588`
- merge-base: `a203a05eb10db82e1db2d75398e0261cfe4d33e4`
- target-only commits: 1,016
- final-tree diff: 1,211 paths、34,792 insertions、11,437 deletions
- target schema marker: `20251023210145`
- target migration inventory: 554 markers（4.4.22 の 539 + 4.5 の 15）
- target fresh `db/schema.rb` SHA-1:
  `801766beefdd9b1d55fe6f8bf3bed91392aebab1`
- target upstream tables: 107
- Paon base: branch `feature/4.5.15` の分岐点 `ce8586c293a0e2474e98521e70cd9e58576dfec5`
  （Mastodon 4.4.22 互換実装済み、Paon version `7.2.0`）

stable-4.4 と stable-4.5 は `v4.4.22` を merge-base にする直線履歴ではない。
したがって `v4.4.22..v4.5.15` の commit subject だけを upgrade inventory とせず、
両 tag の final tree、`CHANGELOG.md`、15 migrations、schema、routes、models/services/
workers、request/model/frontend specs、および
[v4.5.15 release](https://github.com/mastodon/mastodon/releases/tag/v4.5.15)を
一次資料として比較する。

## 状態の読み方

| 状態                     | 意味                                                                                                            |
| ------------------------ | --------------------------------------------------------------------------------------------------------------- |
| 実装済み・最終 gate 待ち | 現在の作業ツリーに Paon の実行経路と focused test がある。全 repository gate や live interop の完了は意味しない |
| 一部対応                 | 既存経路を利用できるが、Mastodon 4.5.15 の最終契約または test が不足する                                        |
| 未実行                   | source implementation ではなく、実サービス、実 image、production topology、human/browser evidence が必要        |
| 対象外                   | Rails/Ruby/Sidekiq/Vite/LDAP など Paon に存在しない経路。Go/Rspack の代替境界は確認する                         |

## P0: PostgreSQL schema と migration

### DB45-01: final schema `20251023210145`

状態: **実装済み・最終 gate 待ち**

- `internal/paon/migrate/schema.sql` を 4.5.15 catalog に更新する。
- `username_blocks` table と seed、`fasp_providers.delivery_last_failed_at`、
  `status_stats.quotes_count`、`conversations.parent_status_id` / `parent_account_id`、
  `accounts.following_url` / `id_scheme` を追加する。
- quote、conversation、status、follow の 4.5 indexesを追加し、置換前 index は
  acknowledged contract まで残す。
- fresh schemaの `ar_internal_metadata.schema_sha1` を
  `801766beefdd9b1d55fe6f8bf3bed91392aebab1` にする。staged migrationでは値を
  上書きせず、4.4 / 4.3 / 4.2 lineageの
  `b53e3b8de778cd1b53158326b97afa9368f3237e` /
  `d03e3ba56d365d37ac099782d9d80efbce3abb8b` /
  `7d5086228b379c66ff21a4396f443ba4daac5752` を保持する。
- GORM model は `Account.FollowingURL` / `IDScheme`、
  `FaspProvider.DeliveryLastFailedAt`、`Conversation.ParentStatusID` /
  `ParentAccountID`、`StatusStat.QuotesCount`、`UsernameBlock` を final catalog と合わせる。

実装対応: `internal/paon/migrate/schema.sql`、`internal/paon/models/models.go`、
`internal/paon/schema/versions.go`、`internal/paon/migrate/upgrade_4_5_test.go`。

### DB45-02: reviewed 4.4.22 → 4.5.15 upgrade

状態: **実装済み・最終 gate 待ち**

15 marker を次の staged phase へ固定する。

| Phase               | Marker           | PostgreSQL/data contract                                                                                                     |
| ------------------- | ---------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| expand              | `20250717003848` | `username_blocks`、case-insensitive unique / normalized indexes、reserved-name seed                                          |
| expand              | `20250805075010` | FASP delivery failure timestamp                                                                                              |
| contract            | `20250819100545` | quote indexesを `(account_id, quoted_account_id, id)` と `(quoted_status_id, id)` へ置換。replacement は expand 中に先行作成 |
| expand              | `20250820084312` | non-null default-zero `status_stats.quotes_count`                                                                            |
| expand              | `20250828222741` | conversation root status/account references                                                                                  |
| expand              | `20250902221600` | `statuses(conversation_id)` index                                                                                            |
| expand              | `20250909100506` | non-null parent status partial unique index                                                                                  |
| backfill            | `20250911163952` | quote mail default、private → nobody、unset unlisted → followers                                                             |
| expand              | `20250912082651` | non-null default-empty `accounts.following_url`                                                                              |
| expand              | `20250924170259` | existing accounts用 `id_scheme=0` column                                                                                     |
| backfill            | `20251002140103` | `timeline_preview` を local/remote live/topic 4 settingsへ展開                                                               |
| expand              | `20251007100627` | reverse follow lookup composite index                                                                                        |
| contract            | `20251007100813` | 旧 target-account-only follow indexを削除                                                                                    |
| expand              | `20251007142305` | new accountの `id_scheme` defaultをnumeric (`1`)へ変更                                                                       |
| backfill + contract | `20251023210145` | `trends_as_landing_page` を `landing_page`へ変換し、最終 markerを記録。source routeのSHAは保持                             |

`internal/paon/migrate/upgrade_4_5.go` は advisory-lock、transaction、idempotent、
resumable な expand/backfill/validate/contract を提供する。contract は全 4.4 web/worker
停止後の `--acknowledge-contract` を要求し、unknown/future/partial marker は fail closed
にする。4.2.19 と 4.3.23 からも既存 phase を順に通して 4.5.15 へ到達させる。

### DB45-03: migration data invariants

状態: **実装済み・最終 gate 待ち**

- username block seed は normalize 後の値と unique constraint を満たし、再実行可能にする。
- quote defaults backfill は malformed JSON を黙って破棄せず fail closed にする。
- timeline/landing legacy YAML はRuby同様に `nil` と `false` のみfalseとして扱い、
  string、number、array、hashを含むそれ以外の値はtrueとして移行する。
- conversation parent account/status、numeric account scheme、new quote indexes、follow index、
  FASP timestamp の null/default/index invariantsをvalidateする。
- contract markerは全15 markerと final schema availabilityが成立した後だけ記録する。

### DB45-04: catalog and production migration gates

状態: **自動化gate実行済み / production-volume gate未実行**

- [x] empty DB、4.4.22 populated DB、4.3.23 populated DB、4.2.19 populated DB の
      4経路を実 PostgreSQL 14 / 15 の両方で実行する。
- [x] official 4.5.15 とPaonを別々に構築し、fresh、4.4.22-to-4.5.15、
      4.3.23-to-4.5.15、4.2.19-to-4.5.15のroute別goldenで relation、physical column
      position/dropped slot/type/default/nullability、index expression/predicate/order、
      canonical FK名/delete action、sequence ownership、function、view、554 markers、
      Active Record metadataを `pg_catalog` 比較する。全4経路をPostgreSQL 14 / 15で固定し、
      freshのSHA `801766beefdd9b1d55fe6f8bf3bed91392aebab1` とstaged lineageの
      `b53e3b8de778cd1b53158326b97afa9368f3237e` /
      `d03e3ba56d365d37ac099782d9d80efbce3abb8b` /
      `7d5086228b379c66ff21a4396f443ba4daac5752` をそれぞれ検証する。
      username block seed 23件も別途固定する。
- [x] 上記4経路をPostgreSQL 14 / 15で双方向に検証する。official Mastodon DBへ
      Paon migratorを実行してmigration 0件、Paon DBへofficial v4.5.15 migratorを実行して
      554 up / 0 downを確認し、双方とも実行後catalogがroute別goldenから変化しないことを確認する。
- [ ] production-volume restore で lock/table rewrite、replica lag、disk headroom、
      backup/restore、old/new writer fenceを計測する。

## P0/P1: official Quote authoring and lifecycle

### QUOTE45-01: official PostgreSQL REST source of truth

状態: **実装済み・最終 gate 待ち**

- `POST /api/v1/statuses` と scheduled status に `quoted_status_id` と
  `quote_approval_policy` を追加し、edit/delete-and-redraftでも同じ contractを使う。
- quote-only postをemptyとして拒否せず、poll/mediaとの排他、direct/private visibility、
  self quote、block/mute/domain block、quoted target visibilityを検証する。
- `PUT /api/v1/statuses/:id/interaction_policy`、
  `GET /api/v1/statuses/:status_id/quotes`、
  `POST /api/v1/statuses/:status_id/quotes/:id/revoke` を追加する。
- Status/StatusEdit/ScheduledStatusにfull/shallow quote、`quotes_count`、
  `quote_approval_policy`、`quoted_status_id` を4.5 entity shapeで出す。
- `accepted`、`pending`、`rejected`、`revoked`、`deleted`、`unauthorized`、
  `blocked_account`、`blocked_domain`、`muted_account`をviewerごとにserializeする。
- accepted quoteのcreate/remove/revokeで `status_stats.quotes_count` をtransactionalに更新し、
  duplicate/out-of-order activityをidempotentにする。

互換性注記: v4.5.15 upstreamでpoll/mediaとの排他はReact composerの操作制約であり、
`PostStatusService` / `Status` / `Quote` はQuoteとの組み合わせ自体を拒否しない。Paonも
composerのQuote開始時alertとpoll/upload buttonのunavailable状態で排他にし、REST APIへ
upstreamにない拒否条件は追加しない。

実装対応: `internal/paon/api/official_quotes_45.go`、
`internal/paon/api/status_quotes.go`、`internal/paon/api/status_counters.go`、
`internal/paon/serializer/rest.go`、`internal/paon/api/server.go`、
`internal/paon/api/scheduled_status_publish.go`。

### QUOTE45-02: FEP-044f outgoing federation

状態: **実装済み・最終 gate 待ち**

- local quote postのNoteへ `quote`、`quoteUri`、`quoteUrl`、interaction policy、
  `QuoteAuthorization` contextをserializeする。
- automatic/manual/followers/nobody policyで `QuoteRequest`を作り、signed deliveryする。
- inbound `QuoteRequest` を4.4のalways-Rejectから、policy/relationshipに応じた
  `Accept` / `Reject`へ更新する。
- `Accept` / `Reject` / `Delete QuoteAuthorization`、explicit revoke、status/account
  delete/suspend/merge/moveをtransaction-safe/idempotentに処理する。
- embedded `QuoteRequest`、object reference、activity ID fallback、misskey fallback、
  typo修正、scheme/host/actor/object/instrument/authorization一致を検証する。
- temporary fetch/delivery failureはretryし、恒久拒否と区別する。

実装対応: `internal/paon/api/activitypub_quotes.go`、
`internal/paon/api/official_quotes_45.go`、`internal/paon/api/activitypub.go`、
`internal/paon/api/activitypub_inbox.go`、`internal/paon/api/activitypub_delivery.go`。

### QUOTE45-03: notifications、mail、push、streaming、search

状態: **実装済み・最終 gate 待ち**

- local/scheduled accepted quoteは `quote` notificationを一度だけ作る。
- quoted source editはquoterへ `quoted_update` notificationを送り、sound/email/pushの
  quote/update preferencesを尊重する。
- quote create/revoke/delete/authorization/updateをGo SSEとWebSocketのport 3000経路へ
  publishし、hydrated nested poll/filter stateを保つ。
- quote list、`has:quote`、trend/reach、moderator view、mail entityへ同じviewer policyを使う。
- account mergeはquoteのposting/quoted accountを移し、AppealとAccountWarningを混同しない。

実装対応: `internal/paon/api/notifications.go`、
`internal/paon/api/list_feed_cache.go`、`internal/paon/api/mailer.go`、
`internal/paon/api/web_push_delivery.go`、`internal/paon/api/streaming*.go`。

### QUOTE45-04: Paon DynamoDB extension removal boundary

状態: **実装済み・最終 gate 待ち**

- runtime serializer、REST、ActivityPub、composer、workerはofficial SQL modelだけを使う。
- DynamoDB adapterはoffline `paon-admin quotes cutover`のread-only inputとしてだけ残す。
- cutover後にfallback read、new write、dual-writeを行わない。
- rollback保持期間終了後のDynamoDB廃止はoperatorの明示操作とする。

## P0/P1: replies、conversation federation、numeric ActivityPub

### REPLY45-01: fetch/refresh replies

状態: **実装済み・最終 gate 待ち**

- context APIの`Mastodon-Async-Refresh` headerへ refresh ID と `result_count`を一貫して返す。
- Web UIは初回待機、polling、完了/失敗、new reply count、scroll position、retryを扱う。
- Asynq reply traversalはglobal/single/page/depth/count/cycle/SSRF limitsとcooldownを守る。
- old undiscovered Updateをnew postとして表示せず、prepared quoteとreply draftを正しく破棄する。

実装対応: `internal/paon/api/async_refreshes.go`、
`internal/paon/api/reply_fetch_44.go`、`app/javascript/mastodon/api.ts`、
`app/javascript/mastodon/features/status/components/refresh_controller.jsx`。

### AP45-01: FEP-7888 public conversation context

状態: **実装済み・最終 gate 待ち**

- new public conversationのrootへparent status/accountを固定し、
  `/contexts/:account_id-:status_id` と `/items` collection/pageを、拡張子なしと`.json`
  aliasの両方で公開する。
- Noteにcontext URIをserializeし、public/unlistedだけをbounded pageで返す。
- remote context URI、pagination、authorized fetch、blocked/suspended/private status、
  cross-server conversation trackingを検証する。

実装対応: `internal/paon/api/fep_7888_45.go`、
`internal/paon/api/server.go`、`internal/paon/models/models.go`。

### AP45-02: numeric local account identifiers

状態: **実装済み・最終 gate 待ち**

- 既存local accountはusername URI、migration後のnew accountは `/ap/users/:id` を正本にする。
- actor、inbox/outbox、followers/following/synchronization、collections、status、activity、
  replies/likes/shares、quote authorizationをnumeric routeで提供する。
- HTML requestはshort account/status URLへredirectし、ActivityPub JSONはnumeric URIを維持する。
- WebFinger、mentions、audience、reblogs、delivery key ID、remote `following_url`を両schemeで扱う。

実装対応: `internal/paon/api/activitypub_numeric_accounts_45.go`、
`internal/paon/api/server.go`、`internal/paon/api/activitypub*.go`。

### AP45-03: converted object Update

状態: **実装済み・最終 gate 待ち**

`Note` / `Question`に変換して保存済みの`Article`、`Page`、`Image`、`Audio`、`Video`、
`Event`に対する`Update`は既知statusだけを通常update pathへ流し、unknown
converted Updateをnew postとして保存しない。`processActivityPubUpdate`の変換済みtypeに
対する早期returnは除去済みで、explicit/implicit Update fixtureを最終gateで回帰する。

## P0/P1: feeds、registration、settings、admin

### FEED45-01: granular live/topic feed access

状態: **実装済み・最終 gate 待ち**

- `local_live_feed_access`、`remote_live_feed_access`、`local_topic_feed_access`、
  `remote_topic_feed_access`を`public` / `authenticated` / `disabled`で管理する。
- local topicはupstreamの許容値、public/tag/link/SSE/WebSocketは同じfilterを使う。
- `view_feeds` role permissionはdisabled feedをbypassでき、通常ユーザーはできない。
- instance v2 configurationとinitial stateへ設定を公開し、admin discovery UIで編集する。

実装対応: `internal/paon/api/timeline_access_45.go`、
`internal/paon/api/role_permissions.go`、`internal/paon/api/admin_discovery.go`、
`internal/paon/api/streaming*.go`、`internal/paon/serializer/rest.go`。

### WEB45-01: configurable landing page

状態: **実装済み・最終 gate 待ち**

`about` / `trends` / `local_feed`をbranding setting、initial state、logged-out root routing、
admin selectorで共有する。Paonの既存landing/timeline layoutを使い、4.5 UIをコピーしない。

### REG45-01: username blocks

状態: **実装済み・最終 gate 待ち**

- `username_blocks` CRUD/batch admin UI、audit log、seedを追加する。
- digit homoglyphとcaseをnormalizeし、exact/containsを区別する。
- hard blockはavailability/registration/confirmationを拒否し、
  `allow_with_approval`は登録を残してautomatic approvalだけ止める。
- race時のavailabilityとadmin-created accounts、invite/manual reviewを回帰する。

実装対応: `internal/paon/api/username_blocks.go`、
`internal/paon/api/admin_username_blocks.go`、`internal/paon/api/registrations.go`、
`internal/paon/api/confirmations.go`。

### REG45-02: built-in antispam text sets

状態: **実装済み・最終 gate 待ち**

Mastodon 4.5はRedis `antispam:spammy_texts`をrecent accountへ、
`antispam:all_time_spammy_texts`をaccount ageに関係なく適用し、status textをUnicode NFKC +
lowercaseで比較する。spammy statusはsilent dropし、reply先またはmention recipientのいずれかが
authorをfollowしている場合は除外する。未解決reportの重複抑止は、representative
accountが作成したsystem spam reportだけを対象にする。

実装対応: `internal/paon/api/antispam_45.go`、
`internal/paon/api/antispam_45_test.go`、`internal/paon/api/server.go`。

### REG45-03: denied registration redirect

状態: **実装済み・最終 gate 待ち**

registrationがclosed/single-user/IP policyで許可されないとき、4.5はweb appのrootではなく
`/auth/sign_in`へredirectし、contact e-mail入りの`closed_registrations` alertを表示する。
`registrationForm`、invite、`createWebRegistration`のsign-in redirect helper、en/jaの
`devise.failure.closed_registrations` locale key、localized contact e-mail alertを含むexact redirect testを実装済み。

### PREF45-01: Posting defaults page

状態: **実装済み・最終 gate 待ち**

`GET/PUT /settings/preferences/posting_defaults`、navigation、form action、4.5用en/ja hintは
実装済み。`default_privacy=private`とした場合は、画面からの値に依存せず
`default_quote_policy=nobody`をserver-sideで強制する。

実装対応: `internal/paon/api/settings_preferences.go`、
`internal/paon/api/settings_pages.go`、`internal/paon/api/server.go`。

## P0/P1: security and patch contracts

### SEC45-01: federation and fetch security

状態: **実装済み・最終 gate 待ち**

- common outbound fetch policyでIPv4-compatible/mapped IPv6、special prefix、DNS pinning、
  redirects、scheme/credential/portを再検証する。
- allowed attribution domain、malformed sanitizer、JSON-LD graph restructuring、linked-data
  signature、QuoteAuthorization host/actor/target/stateのnegative fixtureを維持する。
- AP property/count/length limits、remote suspension、private-resource disclosure、legacy redirect、
  collection cache relevanceを4.5.15のattack fixtureへ更新する。
- remote media descriptionは10,000文字まで許可し、remote status updateは最大4 mediaで止める。
- LDAP認証はPaonにも存在する。4.5.12と同様に接続ごとにfreshなTLS configを作り、
  `LDAP_TLS_NO_VERIFY=true`の明示指定だけを当該接続へ適用して、別接続の証明書検証状態へ
  leakさせない。

  4.4 security基盤を再利用し、IPv4-compatible/mapped IPv6、known-status actor変更、visibility別
  relevancy、embedded Quote context、remote media最大4件など4.5.14/4.5.15のnegative/focused testを
  追加した。Goのfull attack suiteは通過済みで、live federation interopはrelease gateに残す。

### SEC45-02: HTTP signatures

状態: **実装済み・最終 gate 待ち**

- RFC 9421 inbound verificationはfeature flagに依存せず常時有効にする。
- legacy outbound fetch signatureは4.5.8どおり`Accept`をcovered headersから外す。
- duplicate parameter、temporary actor fetch=503、date/digest/key owner、parser request isolationを
  legacy/RFC 9421両方で回帰する。

feature gateの無条件化とdefault/redirect fetch signatureの`Accept`除外は実装済み。

### SEC45-03: Web Push ownership and invalid subscriptions

状態: **実装済み・最終 gate 待ち**

settings update/deleteはowner/scopeに限定し、browser unsubscribe/deleteは4.5.14どおり
anti-CSRF tokenを誤要求しない。404/410 delivery先の削除、不正endpoint/P-256/auth keyの
preflight、worker/direct fallbackの送信防止は実装済み。不正rowのDB削除失敗は
upstream `destroy!`と同様にworker error/retryへ流す。

### SEC45-04: e-mail and dependency security

状態: **実装済み・exact-image gate実行済み / production risk review待ち**

- registration emailは最大320文字、`%` / `,` / `"`を拒否し、parse後addressが原文と一致する。
- only-key actor refreshはURIで既知accountだけを更新し、handle lookup/new account creationへ
  fallbackしない。
- Go/frontend dependencyを更新しreachable high/criticalを分類する。
- promoted imageのFFmpegがCVE-2026-8461修正版であることをversionとTrivy両方で確認する。

## P1/P2: jobs、FASP、search、media、runtime

### JOB45-01: FASP delivery failure handling

状態: **実装済み・最終 gate 待ち**

connection failureを`delivery_last_failed_at`へ記録し、成功でclearし、cooldown中のprovider jobを
抑止する。backfill/search/follow recommendation/lifecycle/trendで共通wrapperを使い、
unconfirmed FASPを拒否し、既存SSRF/TLS/timeout/socket policyを共有する。

実装対応: `internal/paon/api/fasp_http.go`、`internal/paon/api/fasp_workers.go`。

### JOB45-02: follower synchronization digest

状態: **実装済み・最終 gate 待ち**

followers synchronization deliveryにdigestを付け、取得collectionのURI digestと一致する場合
だけmissing followerを削除する。invalid/missing/mismatch digestは追加処理を許可しても破壊的
削除を行わない。

実装対応: `internal/paon/api/activitypub_followers_synchronization.go`。

### SEARCH45-01: account/status search

状態: **実装済み・最終 gate 待ち**

- logged-out account search minimumをUnicode 3 charactersにする。
- already-known private GoToSocial status URLはviewer visibilityを通して解決する。
- quote、blocked/muted relationship、Meilisearch unavailable、stale UI resultsを回帰する。

### PUSH45-01: notification lifecycle

状態: **実装済み・最終 gate 待ち**

standard/draft payload、quote/quoted_update alerts、TTL、Unsubscribe-URL、404/410 cleanup、
invalid subscription preflightと削除失敗retryは既存経路を利用する。

### MEDIA45-01: media and storage

状態: **実装済み・最終 gate 待ち**

- remote updateはtextなし/new media/mixed typeでも最大4件を保つ。
- audio posterがなくてもexisting-design visualizerを表示する。
- YouTube referer/start time、S3 expensive batch delete timeout、Swift Keystone throttling、
  storage-schema、custom emoji domain suspension cleanupを回帰する。
- media attachment / preview-card vacuumはID cursorをbatch選択時に進め、1 batchのfile/DB
  cleanup失敗でrun全体を中断せず次batchへ進む。
- image/modal/admin/filter sensitive controlsは4.5.15のkeyboard/touch/browser matrixを通す。

### RUNTIME45-01: dependency floors and identity

状態: **実装済み・最終 gate 待ち**

- PostgreSQL minimumは14、Redis/Valkey minimumは7.0、frontend effective Node minimumは
  20.19、release imageのasset buildはNode 24を使う。
- Valkey/Dragonfly/Redis identityをadmin dashboardへ表示する。
- Paon `7.3.0` / Mastodon compatibility `4.5.15`、instance API version `7`、User-Agent、
  RSS/software update metadataを同時に更新する。
- Go web processがHTTP/REST/AP/SSE/WSをport 3000で提供し、web/worker roleを独立起動する。

### SEO45-01: schema.org status metadata

状態: **実装済み・最終 gate 待ち**

SEO-enabled public status HTMLへescaped `application/ld+json`のschema.org
`SocialMediaPosting` metadataを追加し、`noindex`アカウンでは出力しない。author、
published/modified date、interaction counts、media、preview cardをstatus HTMLのheadへ埋め込む。

実装対応: `internal/paon/api/public_status.go`、
`internal/paon/api/public_status_test.go`、`internal/paon/web/assets.go`。

## P1/P2: retained-design React UI and i18n

### UI45-01: quote composer and status actions

状態: **実装済み・最終 gate 待ち**

- current Paon composerへquote preparation、quoted preview/remove、policy selector、private/quiet
  education、empty quote、poll/media exclusion、replyとの切替、delete-and-redraftを追加する。
- status/action bar/detail/modalへquote action、counter、quote list、revoke、blocked/muted/deleted/
  pending/fallback/CW/filter stateを追加する。
- notification、mail-linked view、stream update、featured/standalone/PiPでnested quote semanticsを共有する。
- Mastodon 4.5 composer layout/CSSはコピーせず、Paonのemotional/custom spoiler/
  single-column-chat/themeを維持する。

実装対応: `app/javascript/mastodon/actions/compose.js`、
`app/javascript/mastodon/reducers/compose.js`、
`app/javascript/mastodon/features/compose/components/quoted_post.jsx`、
`quote_policy_selector.jsx`、`features/quotes/`、`components/status_action_bar.jsx`。

### UI45-02: reply refresh、feeds、landing

状態: **実装済み・最終 gate 待ち**

- detail threadへasync refresh alerts/count/loading/failureを追加し、scroll/focusを維持する。
- local/remote live/topic feedのdisabled/authenticated状態をAPI、navigation、column empty state、
  streamingで一致させる。
- logged-out rootはserver `landing_page`に従い、access不能なlocal feedを選ばない。

### UI45-03: emoji、viewport、audio

状態: **実装済み・最終 gate 待ち**

- Twemoji 16 data/sheet/new SVGを追加し、picker/completion/renderingを更新する。
- `emoji_style=auto|native|twemoji`をinitial stateとrendererで扱い、custom emojiを壊さない。
- status/CW/profile/notificationのHTML/emoji pathでvariant selector、cache、worker pathを回帰する。
- existing themeを`dvh`へ拡張し、audio posterなしのvisualizer、reduced motion、safe area、
  mobile/keyboard semanticsを確認する。

### UI45-04: settings、admin、a11y、locales

状態: **実装済み・最終 gate 待ち**

- landing/feed/username-blockは既存admin designへ追加済み。moderationのstatus row/detailにも
  accepted/pending/rejected/revoked/deleted Quote、quoted post link、`quotes_count`、poll/media/CW、
  link previewをread-onlyで表示し、report画面でも同じrendererを共有する。
- Posting defaultsの独立route/navigation、4.5用hint、private → nobodyのserver-side強制は
  実装済み。
- follow/unfollow/unblock/withdraw confirmation、disabled dropdown focus、feed keyboard navigation、
  empty-post error、always-sensitiveのmanual unmark、quote notification soundをfocused testで回帰する。
- en/jaの新規message、plural、placeholder、Web Push localeを揃え、locale extractionで全bundleの
  fallbackを検証する。upstreamのlayout/theme messageを無差別にcopyしない。

実装対応: `internal/paon/api/admin_account_subroutes.go`、
`internal/paon/api/admin_reports_web.go`、`internal/paon/api/settings_pages.go`、
`app/javascript/mastodon/components/dropdown_menu.jsx`、
`app/javascript/mastodon/utils/feed_keyboard_navigation.js`、
`app/javascript/mastodon/reducers/compose.js`。

### UI changelog item audit

判定は `implemented`（今回の4.5作業ツリーで対応）、`pre-existing`（4.4.22互換の
現行designに同じ挙動が既にあり、focused testまたは実行経路を確認）、
`non-applicable`（Rails/Vite/Sidekiq等だけの実装名でPaonに同一経路がない）の3値に固定する。
`implemented`はhuman/browser gate完了を意味しない。視覚だけの変更もupstream markup/CSSを
コピーせず、Paonの既存class/componentへ必要な状態、寸法、focus、contrastだけを追加した。

#### v4.5.0 Changed / Fixed 全項目

| CHANGELOG item                                               | 判定                 | Paonの実行経路または境界                                                         |
| ------------------------------------------------------------ | -------------------- | -------------------------------------------------------------------------------- |
| Changed: follow操作（unfollow/unblock/withdraw）の確認dialog | implemented          | `account_container.jsx`、`header_container.jsx`、`account_card.jsx`              |
| Changed: Follow / Follow back / Request to follow label      | implemented          | `account.jsx`、account header/directory、`account_follow_labels_45-test.jsx`     |
| Changed: appearanceのAdvanced section                        | implemented          | `settingsAppearanceHTMLWithMessages`のsection headingとsettings test             |
| Changed: blocked/muted quoted user表示                       | implemented          | official `Quote.state` serializer、`quote.tsx`、status selector                  |
| Changed: empty postをsilent failureでなくerror表示           | implemented          | `submitCompose`の`compose.error.blank_post` alert                                |
| Changed: Privacy and reachをtop-level settingsへ移動         | implemented          | `settingsNavigationHTML`、`/settings/privacy`、navigation focused test           |
| Changed: quote verification retry回数                        | implemented (non-UI) | `QUOTE45-02`のAsynq retry contract                                               |
| Changed: Admin UIのcontent warning表示                       | implemented          | `adminAccountStatusRowContentHTML`の`details/summary`、report/detail共有renderer |
| Changed: column banner styling                               | implemented          | retained `.switch-to-advanced` / `.follow_requests-unlocked_explanation` style   |
| Changed: recommended Node 24                                 | implemented (non-UI) | `Dockerfile` asset stage、`RUNTIME45-01`                                         |
| Changed: logged-out account search minimum 3                 | implemented          | account search API/`SEARCH45-01`                                                 |
| Changed: Vite legacy browser target                          | non-applicable       | Viteは導入せず、既存Rspack/Browserslist production buildをgateする               |
| Changed: follows index                                       | implemented (non-UI) | `DB45-02` staged migration                                                       |
| Changed: settings/moderation account linksをlocal viewへ     | pre-existing         | Go server-rendered pagesは`/admin/accounts/:id`等のexplicit local pathを使用     |
| Changed: denied registrationをsign-in alertへ                | implemented          | `registrationForm`、`createWebRegistration`、en/ja Devise locale                 |
| Changed: RFC 9421 verificationを常時有効化                   | implemented (non-UI) | `SEC45-02`                                                                       |
| Changed: interaction dialogの簡略化                          | implemented          | retained `interaction_modal` markupにgeneric title/action wordingを適用          |
| Changed: disabled dropdown itemもfocus可能                   | implemented          | `dropdown_menu.jsx`とkeyboard focused test                                       |
| Changed: light theme modal surface                           | implemented          | `mastodon-light/diff.scss`でopaque modal backgroundを設定                        |
| Changed: private posting defaultはquote policy nobody        | implemented          | server-side settings coercion、`PREF45-01`                                       |
| Changed: Quiet public description                            | implemented          | `privacy_dropdown.jsx`とen/ja locale                                             |
| Changed: Boost with original visibility wording              | implemented          | status/detail/PiP action barとen/ja locale                                       |
| Changed: invalid push subscriptionの自動削除                 | implemented (non-UI) | `SEC45-03` / push delivery worker                                                |
| Changed: quote post design                                   | implemented          | upstream layoutを移植せずretained `status__quote` / composer previewを拡張       |
| Changed: audit-log account sort                              | implemented          | `admin_action_logs.go` username order                                            |
| Changed: translation restoreをservice creditより先に表示     | implemented          | `status_content.jsx`のbutton/meta order                                          |
| Changed: reportのAdd postsをtable toolbar内へ                | implemented          | `adminReportHTMLWithConfig`とtoolbar-order test                                  |
| Changed: docker-compose Sidekiq healthcheck                  | non-applicable       | Sidekiqなし。Go worker roleのhealth/gateは`RUNTIME45-01`                         |
| Fixed: quote表示前のrelationship取得                         | implemented          | quote importer/relationship fetchとviewer-state selector                         |
| Fixed: status history poll option markup                     | implemented          | `compare_history_modal.jsx`のeditable poll option label                          |
| Fixed: logged-out hashtag menuのMute                         | implemented          | identity-aware `hashtag_menu_controller.jsx`とsigned-out test                    |
| Fixed: Rules language初期値と不要selector                    | implemented          | `rule_locales.js`、rules panel、focused test                                     |
| Fixed: empty-path mention URL比較                            | implemented          | `compare_urls.js`をstatus mention link pathへ適用                                |
| Fixed: full-width hash sign                                  | implemented          | composer tokens、status link handling、Go tag/AP/announcement extraction tests   |
| Fixed: purged severed-relationship layout                    | implemented          | 2 download列を`colspan=2`のpurged messageへ置換、Go focused test                 |
| Fixed: reduce animations時のSkeleton                         | implemented          | `.reduce-motion .skeleton { animation: none }`                                   |
| Fixed: vacuum single-batch failure                           | implemented (non-UI) | `MEDIA45-01` cursor/continue worker contract                                     |
| Fixed: unreachable search service                            | implemented (non-UI) | `SEARCH45-01` Meilisearch error path                                             |
| Fixed: soft-deleted bookmark export                          | implemented (non-UI) | export query contract                                                            |
| Fixed: Newsの長いauthor名overflow/alignment                  | implemented          | retained `.story__details__shared` baseline/min-width/shrink rules               |
| Fixed: discovery preambleのmissing word                      | implemented          | `config/locales/en.yml`                                                          |
| Fixed: `.more-from-author` overflow                          | implemented          | retained component styleに`overflow: hidden`                                     |
| Fixed: admin action button wrapping                          | implemented          | announcements/filter action barのgap/wrap/end alignment                          |
| Fixed: Safari translate button width                         | implemented          | `.status__content__translate-button { width: min-content }`                      |
| Fixed: OAuth認可中login画面から別ページへ遷移                | implemented          | `redirect_to=/oauth/authorize`時のnon-linking logo/footer suppression test       |
| Fixed: stale search results                                  | pre-existing         | search request時にresultsをclear、`search_45-test.js`で固定                      |
| Fixed: YouTube start time                                    | pre-existing         | `handleIframeUrl`の`start` parameterとfocused test                               |
| Fixed: unicodeによるbanned-text回避                          | implemented (non-UI) | `REG45-02` NFKC/lowercase matcher                                                |
| Fixed: batch toolbarがstatus mediaの下へ潜る                 | implemented          | retained `.batch-table__toolbar` z-index                                         |
| Fixed: RSS gzip MIME                                         | implemented          | RSS responseと`dist/nginx.conf`を`application/rss+xml`へ統一                     |
| Fixed: detailから削除後の404                                 | implemented          | delete thunk成功後だけ`history.push('/')`、resolve/reject test                   |
| Fixed: feed keyboard navigation                              | implemented          | `feed_keyboard_navigation.js`、status list/UI hotkeys test                       |
| Fixed: Who to follow loading layout shift                    | implemented          | loading indicatorとretained card領域のminimum height                             |
| Fixed: Vagrantfile                                           | non-applicable       | Paon release/development contractにVagrantなし                                   |
| Fixed: reply indicatorのstale avatar                         | implemented          | Avatar keyへaccount IDを含める                                                   |
| Fixed: Chewy sample-data task                                | non-applicable       | Rails/Chewy/dev sample taskなし                                                  |
| Fixed: moved-to userの重複account note                       | implemented (non-UI) | account mute/update service contract                                             |
| Fixed: seeded admin creation                                 | implemented (non-UI) | `paon-admin` / registration path                                                 |
| Fixed: media modal imageの重複title                          | implemented          | `GIFV`と`ZoomableImage`からtitleを除去、a11y test                                |
| Fixed: unset default privacy                                 | pre-existing         | settings default resolutionとblank-option regression test                        |
| Fixed: status keyboard navigationのglitch                    | implemented          | DOM item based adjacent-focus helper                                             |
| Fixed: CW field Enterでpost submit                           | implemented          | spoiler/body別keydown helper、Ctrl/Cmd+Enterだけ明示submit                       |

#### v4.5.1〜v4.5.15 frontend / server-rendered UI項目

| Release / CHANGELOG item                             | 判定           | Paonの実行経路または境界                                                                            |
| ---------------------------------------------------- | -------------- | --------------------------------------------------------------------------------------------------- |
| 4.5.1 Alt text modalのCmd/Ctrl+Enter                 | pre-existing   | retained focal-point/alt-text textareaは`onKeyDown`でsubmit                                         |
| 4.5.1 public/hashtag stream由来postのquoteability    | implemented    | streaming importerがinteraction/quote policyをnormalize                                             |
| 4.5.1 `Intl.DisplayNames`非対応browser               | implemented    | guarded initial-state language name fallback test                                                   |
| 4.5.1 detail内quoteへのfilter適用                    | implemented    | detailed status selectorのnested quote filter state                                                 |
| 4.5.1 reply-refresh alertのscroll shift              | implemented    | `refresh_controller.jsx`のstable alert slot/scroll handling                                         |
| 4.5.1 keyboard-open dropdownのfirst focus            | implemented    | dropdown open focus regression test                                                                 |
| 4.5.1 prepared quoteをreply時にdiscard               | implemented    | `discard_draft.js` / compose reply reset                                                            |
| 4.5.2 self-quoteではprivate educationを省略          | implemented    | `quiet_quote_modal.jsx` self-account branch                                                         |
| 4.5.2 CW-only quote fallback link                    | implemented    | quote/status fallback rendering                                                                     |
| 4.5.2 textless status loading表示                    | implemented    | status/quote loading-state renderer                                                                 |
| 4.5.2 focused post上の`g h`                          | pre-existing   | retained `react-hotkeys`はchild `h`とroot sequenceを別Mousetrap scopeで処理                         |
| 4.5.2 quote開始時に既存CWを保持                      | implemented    | compose reducerは空の時だけquoted CWを採用                                                          |
| 4.5.2 threaded view scroll-to-status                 | implemented    | detail `RefreshController` / scroll restoration                                                     |
| 4.5.2 emoji worker path                              | implemented    | Rspack worker resolution/build contract                                                             |
| 4.5.2 CSS modules cross-origin                       | non-applicable | Vite CSS modulesなし。Rspack asset public pathをproduction buildで検証                              |
| 4.5.2 percent入りremote tag                          | implemented    | status hashtag handlerはinvalid percent linkをexternalとして保持                                    |
| 4.5.2 bogus quote policy normalization               | implemented    | importer/reducer/API policy fallback                                                                |
| 4.5.2 hashtag completion insertion                   | implemented    | compose token/suggestion insertionとfull-width focused test                                         |
| 4.5.2 composer Cmd/Ctrl+Enterのmodal誤作動           | implemented    | compose keydownで`preventDefault`後にsubmit                                                         |
| 4.5.3 non-quote Delete & Redraft                     | implemented    | redraftのnullable `quoted_status_id` normalization                                                  |
| 4.5.3 YouTube embed referrer                         | implemented    | iframeへ`strict-origin-when-cross-origin`属性、focused test                                         |
| 4.5.3 streamed quoted poll hydration                 | implemented    | importer nested poll hydration                                                                      |
| 4.5.3 external linkの余分な`noreferrer`              | implemented    | status external linkを`nofollow noopener`へ正規化、focused test                                     |
| 4.5.3 advanced UI有効時single-column post navigation | implemented    | layout-aware column number/focus helper                                                             |
| 4.5.3 compose autosuggestのcase保持                  | implemented    | original tokenを保持するsuggestion insertion path                                                   |
| 4.5.4 profile field custom emoji                     | implemented    | shared custom-emoji hydration/renderer                                                              |
| 4.5.4 CW-only quote fallback                         | implemented    | quote fallback link renderer                                                                        |
| 4.5.4 locked warning link                            | implemented    | `/settings/privacy#account_unlocked`へ変更（実在field ID）                                          |
| 4.5.4 remote post内local custom emoji                | implemented    | account/status scoped emoji normalization                                                           |
| 4.5.4 configured CDN assets                          | implemented    | Rspack manifest/public-path asset serving                                                           |
| 4.5.4 Tor browser notifications page                 | implemented    | notification state/rendererのcapability-safe path                                                   |
| 4.5.4 hashtag autocomplete prefix                    | implemented    | suggestion token insertion regression                                                               |
| 4.5.5 deleted reblogのfeed表示                       | implemented    | status list reducer deleted-target guard                                                            |
| 4.5.5 omitted quote policyをeditで保持               | implemented    | compose/API updateは未指定policyを既存値として扱う                                                  |
| 4.5.5 emoji variant selector                         | implemented    | Twemoji/native rendererのvariant normalization                                                      |
| 4.5.5 mobile admin sidebar stacking                  | implemented    | admin content isolationとbatch toolbar z-index                                                      |
| 4.5.6 edit→Delete & Redraft後のquote cancel          | implemented    | redraft quote ID/state restore                                                                      |
| 4.5.6 subscribed followerへのedit notification       | implemented    | notification update grouping/preferences                                                            |
| 4.5.7 emoji cache                                    | implemented    | emoji mode/data cache path                                                                          |
| 4.5.7 pending post Delete & Redraft                  | implemented    | pending quote/redraft state handling                                                                |
| 4.5.7 disabled timeline streaming                    | implemented    | feed access stateとSSE/WS authorizationをUI navigationと共有                                        |
| 4.5.8 account migration current username check       | implemented    | server-rendered migration validation                                                                |
| 4.5.8 Unblock / Unmuteがdisabled                     | implemented    | account/list/headerはrequested/muting/blocking branchをdisabled条件より優先                         |
| 4.5.8 hover cardの意図しない表示/touch表示           | implemented    | recent mouse movement threshold、touch suppression、focus path                                      |
| 4.5.8 unfollow後もlistにstatusが残る                 | implemented    | status-list membership reducer                                                                      |
| 4.5.9 quote update notification sound preference     | implemented    | notification settings/group sound preference                                                        |
| 4.5.12 always-sensitive時のmanual unmark             | pre-existing   | first mediaでwarningを開き、その後のspoiler toggleはsensitiveを解除可能。redraft/editも復元testあり |
| 4.5.14 expirationなしpoll vote                       | pre-existing   | null `expires_at`をexpired扱いせずtimer/labelを省略                                                 |
| 4.5.14 Hide media with a warning filter              | implemented    | `matched_media_filters`をplain valueでstatusへ渡すselector test                                     |
| 4.5.14 quote list error表示                          | implemented    | quote list request failure state/UI                                                                 |
| 4.5.14 empty-text quote editでCWをbodyへcopyしない   | implemented    | `COMPOSE_SET_STATUS` text/spoiler separation                                                        |
| 4.5.14 invite moderationのautofollow                 | implemented    | server-rendered invite form/value persistence                                                       |
| 4.5.15 UI固有CHANGELOG項目なし                       | non-applicable | typo/merge/media上限は`QUOTE45-*` / `MEDIA45-01`のbackend focused test対象                          |

### TEST45-01: 4.5 differential contract matrix

状態: **実装済み**

`routes:audit`は4.5.15 Rails manifestをdefaultにしている。`authenticated.json`へofficial Quote
create/list/revoke/interaction policyとQuoteAuthorization、FEP-7888 context/items/page、numeric
ActivityPub actor/outbox/followers/status、4 feed gateと`view_feeds`、username blocks、Posting
defaultsのcaseを追加済み。Rails/Go固有のsession Cookie/CSRFはtarget別env headerからstrictに
解決し、secretをmanifest/resultへ出さない。REST/APはstructural equalityを必須にし、Paonが
既存designを拡張するadmin/settings HTMLだけstatus/header比較へ限定する。isolatedな同一fixture、
fixed clock、reset済みRedisを使うlive Rails/Go実行をrelease gateに固定した。

## release tracking: 4.5.0〜4.5.15

この表は各releaseの`CHANGELOG.md`項目をPaon gateへ対応付けたもの。Rails/Vite/tootctl固有の
実装名であっても、同じdata/security/operation contractがPaonにあればGo/Rspack/Asynq側で
検証する。

| Release | UI監査         | Cumulative compatibility gate                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | 主な対応ID                                                                                                                        |
| ------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------- |
| 4.5.0   | concrete gap 0 | official outgoing Quote/composer、reply fetch、username blocks、4 feed gates/`view_feeds`、moderator quote/admin link preview、landing page、converted-object Update、`dvh`、numeric AP URI、poster-less audio visualizer、Mongolian、followers quote policy、schema.org、quote-default backfill、Posting defaults、Twemoji 16/emoji style、FEP-7888、follower digest、Valkey、FASP failure、all-time antispam；follow confirmations/labels、advanced appearance、blocked/muted quote states、empty-post error、privacy/reach navigation、quote retry、CW/admin/UI wording、Node 24、logged-out search=3、follow index、denied-registration redirect、unconditional RFC 9421、invalid push deletion；poll/history/search/mentions/hashtags/a11y/storage fixes；PostgreSQL 13 removal | `DB45-*`, `QUOTE45-*`, `REPLY45-*`, `AP45-*`, `FEED45-*`, `REG45-*`, `PREF45-01`, `SEC45-*`, `UI45-*`, `RUNTIME45-01`, `SEO45-01` |
| 4.5.1   | concrete gap 0 | Alt-text Cmd/Ctrl+Enter、stream-origin quoteability、old Update、`Intl.DisplayNames` fallback、detail quote filter、reply-alert scroll、dropdown focus、arch64 build、async-refresh `result_count`、prepared quote discard                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `QUOTE45-01`, `REPLY45-01`, `UI45-*`                                                                                              |
| 4.5.2   | concrete gap 0 | self-quote private education、CW-only fallback、textless loading、`g h` shortcut、CW preservation、thread scroll、emoji worker path、storage-schema、percent remote tags、bogus policy normalization、hashtag completion、composer shortcut isolation                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `QUOTE45-*`, `UI45-*`, `MEDIA45-01`                                                                                               |
| 4.5.3   | concrete gap 0 | private-post existence nondisclosure、non-quote redraft、YouTube referer、quoted poll hydration、duplicate conversation prevention、external link rel、single-column navigation、status cleanup quote safety、S3 timeout、autosuggest case                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `SEC45-01`, `QUOTE45-*`, `AP45-01`, `MEDIA45-01`, `UI45-*`                                                                        |
| 4.5.4   | concrete gap 0 | SSRF/severed-owner security、temporary signature fetch=503、profile/CW/notification custom emoji、context page serialization、CW quote fallback、locked link、CDN assets、Tor notification、`view_feeds` default、hashtag completion、domain-blocked mention                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | `SEC45-*`, `QUOTE45-*`, `AP45-01`, `FEED45-01`, `UI45-*`                                                                          |
| 4.5.5   | concrete gap 0 | federated/user field limits、remote suspension、push-settings ownership、404 Tombstone skip、duplicate quote lifecycle、deleted reblog feed、omitted quote policy preservation、`Vary` parsing、QuoteRequest scheme、thread-safe dispatch、numeric reblog URI、signature duplicate params、emoji selector、mobile admin                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `SEC45-*`, `QUOTE45-*`, `AP45-02`, `PUSH45-01`, `UI45-*`                                                                          |
| 4.5.6   | concrete gap 0 | blocked-account collection cache、pending quote cache、migration relationship cache、quote-cancel redraft、edit subscription notifications、invalid updated tag、cross-server conversation、connection close                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | `SEC45-01`, `QUOTE45-*`, `AP45-01`, `UI45-01`                                                                                     |
| 4.5.7   | concrete gap 0 | unconfirmed FASP/socket security、suspended-domain emoji purge、emoji cache、pending redraft、separate key document、disabled timeline streaming、duplicate updated hashtags                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | `JOB45-01`, `FEED45-01`, `SEC45-01`, `UI45-03`                                                                                    |
| 4.5.8   | concrete gap 0 | QuoteAuthorization security、legacy open redirect、known private GtS search、remote description 10k、legacy signatureから`Accept`除外、numeric HTML redirect、duplicate repair/migration/Swift/poll/list/hover/unblock/username fixes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `QUOTE45-02`, `SEC45-01`, `SEC45-02`, `SEARCH45-01`, `AP45-02`, `MEDIA45-01`                                                      |
| 4.5.9   | concrete gap 0 | email verification security、quote JSON-LD definition、quote sound preference、blocked target quote refusal、setup trademark warning                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | `SEC45-04`, `QUOTE45-*`, `UI45-04`                                                                                                |
| 4.5.10  | UI固有項目なし | SSRF、JSON-LD graph/linked-data signature security、quote context types/authorization、unused Devise strategy removal                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `SEC45-01`, `QUOTE45-02`; Deviseは対象外                                                                                          |
| 4.5.11  | UI固有項目なし | attribution-domain spoofing、sanitizer DoS、large remote media descriptions                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | `SEC45-01`                                                                                                                        |
| 4.5.12  | concrete gap 0 | LDAP TLS verification、dependency update、always-sensitive時のmanual unmark                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | LDAP TLS state isolationを回帰、`SEC45-01`, `UI45-04`                                                                             |
| 4.5.13  | UI固有項目なし | FFmpeg CVE-2026-8461                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | `SEC45-04`, `MEDIA45-01`                                                                                                          |
| 4.5.14  | concrete gap 0 | permission/IPv4-compatible IPv6 security、no-expiration poll vote、hide-media filter、admin query、push delete CSRF、known-status author change、quote list error、AP relevancy、quote merge、suspended follow-request count、followed-tag cleanup、empty-text quote CW、QuoteRequest rejection lookup、invite autofollow                                                                                                                                                                                                                                                                                                                                                                                                                                                            | `SEC45-*`, `QUOTE45-*`, `FEED45-01`, `PUSH45-01`, `UI45-*`                                                                        |
| 4.5.15  | UI固有項目なし | embedded quote typo、Appeal/AccountWarning merge separation、remote media max-4 off-by-one                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `QUOTE45-02`, `QUOTE45-03`, `SEC45-01`, `MEDIA45-01`                                                                              |

## static implementation gaps before final verification

現在の作業ツリーをupstream final tree/routes/specと照合し、source上のconcrete
unimplemented candidateを0にした。以下の静的項目は解消済みで、full gateとlive/human
admissionは引き続き別に扱う。

- [x] `REG45-03`: en/ja closed-registration alertとcontact e-mail付きredirectをlocale/focused testで固定した。
- [x] `UI45-04`: admin moderation status/reportへofficial Quote state/quoted link/count、poll/media/CW、link previewをread-onlyで追加した。
- [x] `TEST45-01`: differential manifestsへ4.5のQuote/FEP/numeric/feed/admin/settings契約を追加し、isolated fixtureを使うlive Rails/Go release gateを定義した。
- [x] v4.5.0の全Changed/Fixedとv4.5.1〜v4.5.15のfrontend/server-rendered UI項目を
      1件ずつ`implemented` / `pre-existing` / `non-applicable`へ分類し、実行経路へ対応付けた。
      privacy top-level nav、unauthenticated hashtag mute、Rules language、full-width hashtag、
      reduced motion、mention URL、purged table、RSS MIME、OAuth flow、media title、feed/CW keyboard等の
      concrete gapはfocused test付きで閉じた。
- [x] 4.5.0〜4.5.15の全Added/Changed/Fixed/Removedとfinal routes/specを再読し、
      backendのconcrete unimplemented candidateを0にした。4.5.14のadmin user queryは
      `users.account_id` Snowflake rangeへ更新し、4.5.15のQuote merge分離とremote media最大4件も
      focused testで固定した。
- [x] final-candidateのfresh-schema role smokeで検出した旧`imports.data_file_size`参照を除去し、
      upstream 4.5と同じ6種類のattachment-backed media storageだけを集計する。`bulk_imports`は
      PostgreSQL database sizeへ含め、fresh PostgreSQL 14で旧tableを照会しないことを固定した。

## release gates

### Source and static gates

```bash
task build
task test:rtk
go vet ./...
go mod verify
go mod tidy -diff
task routes:audit MASTODON_VERSION=4.5.15
yarn i18n:extract
yarn lint:js
yarn lint:json
yarn typecheck
yarn jest --runInBand
yarn build:production
git diff --check
```

- [x] Go full test 3,555件/23 package、5 binaryの`task build`、`go vet ./...`、
      `go mod verify`、`go mod tidy -diff`、全Go fileの`gofmt -d`、`git diff --check`を完走した。
      sandbox listener failureとapplication failureはlistener-capable環境での再実行により区別した。
- [x] official 4.5.15 route manifest 739件にaccepted/missing routeがない。
- [x] en/ja/fallback/plural/RTL、Rspack lazy chunks/manifest/CSP、Twemoji 16 assetsを検証した。
      frontendは85 suite / 265 test / 26 snapshot、typecheck、lint、production buildが通過し、
      React messageはen 960 / ja 1,095 / en-only 0である。
- [x] TODO全項目をworking treeの実行経路とtestへ再対応付けし、sourceの部分対応を0にした。

### Real dependency gates

```bash
task test:integration
task test:external
docker compose config --quiet
docker build .
```

- [x] PostgreSQL 14、Redis 7、Meilisearch、object storage、SMTP、FFmpeg/ffprobe、libvipsの
      real integrationを通した。Valkey/Dragonfly identityはautomated contract test済みで、
      target topologyでのfailoverはproduction admissionに残す。
- [x] exact promoted imageでFFmpeg version、`govulncheck`、frontend production audit、
      Trivy fixable/all findingsを記録する。findingのproduction risk acceptanceは別gateとする。
- [x] isolated PostgreSQL 14/Redis 7上で`--check-config`、web-only、worker-only、all roles、
      Go SSE/WS port 3000、workerのgraceful stop/restart/readiness再生成をrehearseする。さらに実workerへ
      `max_retry=0`のunknown Asynq taskを投入し、retryせずarchiveされるretry-exhaustionを確認する。
- [ ] production topologyでのrolling restart/failoverを行う。isolated role/retry smokeを
      productionのqueue recovery、負荷、冗長化の証拠にはしない。

### Live and human production admission

- [ ] official Mastodon 4.5.15とのpublic/authenticated/admin/locale/visibility/scope/pagination
      response differentialを実行する。
- [ ] official Quoteをremote Mastodon/GoToSocial等とのAccept/Reject/revoke/delete/edit、
      FEP-7888、numeric URI、legacy/RFC 9421でlive interopする。
- [ ] desktop/mobile/touch/keyboard/screen reader/reduced motionでquote compose、reply refresh、
      feed access、emoji mode、settings/adminをhuman E2Eし、Paon themeのvisual regressionを確認する。
- [ ] production-volume migration、backup/restore、replica lag、Redis failover、storage failure、
      Meilisearch outage、FASP callback、Web Push、mailをtarget topologyでrehearseする。

## version release gate

- [x] source defaultをPaon `7.3.0` / Mastodon `4.5.15` / instance API `7`へ一括更新する。
- [x] source compatibility、real dependency gates、live/human production admissionを区別して報告する。
- [x] version文字列やlocal greenだけをproduction acceptanceの証拠にしない。

## explicit non-goals

- Mastodon 4.5 UI markup/CSS/layoutをそのままコピーすること。
- Paon独自DynamoDB Quoteをofficial Quoteと並行してruntime運用すること。
- Rails/Ruby/Sidekiq/Vite/standalone streaming server/port 4000を復活させること。
- GORM AutoMigrate、暗黙的・破壊的schema変更、ackなしcontract。
- dependency scanのfixable 0をunfixed finding 0またはproduction admissionと表現すること。
