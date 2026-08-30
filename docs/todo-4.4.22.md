# Mastodon 4.4.22 互換化 TODO

## 目的

Paon を Mastodon v4.3.23 の実装契約から v4.4.22 の実装契約へ更新する。
対象は PostgreSQL schema、REST/OAuth、ActivityPub、streaming、security、React
Web UI、i18n、Asynq jobs、search、media、storage、mail、runtime configuration、
operations である。

UI は Mastodon 4.4 の実装を直接取り込まない。Mastodon 4.2.19 を起点に Paon
固有機能とともに拡張してきたデザインを維持し、4.4 の操作・状態・アクセシビリティ
契約だけを意味的に移植する。Rails、Ruby、Sidekiq、Vite、standalone Node streaming
server、port 4000、GORM AutoMigrate、Makefile は導入しない。

## 比較基準

- upstream start: `v4.3.23` / `efb25b6aa2014201b04f25c390ad1f516a3ff52d`
- upstream target: `v4.4.22` / `e5e4425d13bfee9572d8d384a19553caad7503d1`
- upstream comparison: `git diff v4.3.23 v4.4.22`（endpoint tree差分
  2,694 paths）。stable-4.3とstable-4.4は直線履歴ではなく、merge-base
  `03210085b7481568cc507f088144aaf1dae73c88`から各々355/1,982 commits分岐している。
  したがって`v4.3.23..v4.4.22`を単一のupgrade commit列とは見なさず、両tagの
  final tree、schema、routes、specと全4.4 release noteを比較する。
- target schema: `20250627132728`
- target migration inventory: 539 markers（4.3.23 の 472 + 4.4 の 67）
- target `db/schema.rb` SHA-1: `b53e3b8de778cd1b53158326b97afa9368f3237e`
- Paon base: branch `feature/4.4.22` の分岐点
  `bc7bba270`（4.3.23 実装コミット済み、作業ツリー clean）

一次資料は upstream tag の `CHANGELOG.md`、`db/schema.rb`、67 migrations、
routes、models/services/workers、request/model specs、frontend tests とする。release note
だけで実装済みとは判断せず、Paon の実行経路、入出力、副作用、失敗契約へ対応付ける。

## 優先度と状態

| 表記     | 意味                                                                 |
| -------- | -------------------------------------------------------------------- |
| P0       | security、data loss、schema、起動、認証・認可の release blocker      |
| P1       | client、federation、主要 user flow の互換性に必須                    |
| P2       | admin、operations、UX、performance、diagnostics を含む完全互換に必須 |
| 対応済み | 実装と対象テストが完了                                               |
| 一部対応 | 既存機能を再利用できるが 4.4 最終契約が不足                          |
| 対象外   | Ruby/Rails/Vite 固有。Go/Rspack 側の代替確認は必要                   |

## P0: security と patch-release fixes

### SEC44-01: v4.4.18〜v4.4.22 security delta

- [ ] IPv4-compatible IPv6、IPv4-mapped IPv6、特殊 prefix、DNS pinning、redirect
      再解決を全 outbound fetch 経路で同一 policy にする。
- [ ] `attribution_domains` の host/scheme/registrable-domain 判定を spoof 不能にし、
      remote actor/profile/card の negative fixture を追加する。
- [ ] sanitizer が malformed input や例外を安全に処理し、message processing DoS に
      ならないことを確認する。
- [ ] LDAP 相当設定が存在しない Go path は対象外と記録し、Paon の TLS client が
      verification を暗黙に無効化しないことをテストする。
- [ ] 4.4.21 permission enforcement を admin/API/browser route ごとに照合し、role、
      owner、scope、CSRF/CORS matrix を通す。
- [ ] Web Push delete/update は owner と bearer token に scope し、browser endpoint の
      delete に CSRF token を要求しない 4.4.21 契約を満たす。

### SEC44-02: quote authorization と lifecycle

- [ ] FEP-044f `QuoteAuthorization` の host/type/interacting object/target/author/stateを
      Mastodon 4.4.22どおり検証する。取得不能な承認はreject/revokeし、内容不一致は状態を
      進めず、`Delete`による承認取消をidempotentにしてstream updateを重複させない。
- [ ] inbound `QuoteRequest` はMastodon 4.4.22どおり常に`Reject`する。quote requestへの
      `Accept`/`Reject`応答、`Undo`、Quote作成APIはこのバージョンの対象外とする。
- [ ] inbound quote authorization が mention や implicit update の通常 access check を
      bypass しない。
- [ ] deleted quote の Tombstone、soft-deleted post、author change、nested quote fetch、
      recursion/depth/size limit を attack fixture で検証する。
- [ ] incoming quoteの正本を公式PostgreSQL `quotes` tableへ置き換える。REST/UIは受信した
      quoteのfull/shallow entityとlifecycle stateを表示するが、outgoing quoteをserializeせず、
      status create/update/scheduled APIに`quoted_status_id`を公開しない。独自DynamoDBは
      one-shot read-only cutover以外のruntimeで使用しない。

### SEC44-03: HTTP Signature と RFC 9421

- [ ] legacy HTTP Signature は duplicate parameter、temporary actor fetch、Accept header、
      date/digest/algorithm/key owner の 4.4.22 挙動へ合わせる。
- [ ] feature flag `http_message_signatures` で inbound RFC 9421 verification を実装し、
      covered components、signature-input、created/expires、content-digest、key resolutionを検証する。
      Mastodon 4.4が要求しない`nonce`を必須条件にはしない。
- [ ] legacy/RFC 9421 の parser state を request 間で共有せず、並行処理 race test を通す。
- [ ] signature fetch failure は恒久 unauthorized と一時 unavailable を区別する。

### SEC44-04: federation/API input and disclosure boundaries

- [ ] federated actor/status/tag/media description/interaction policy/collection の全 4.4
      length/count limit を適用する。remote media description は最大 10,000 文字とする。
- [ ] private/missing/deleted status、poll、quote、media、embed の error shape と locale が
      resource existence を漏らさない。
- [ ] suspended/blocked/domain-blocked actor の Create/Update/mention/collection cache/relevancy
      判定を 4.4.22 fixture で確認する。
- [ ] legacy redirect と malformed external object URL を open redirect にしない。
- [ ] account creation API はuser access tokenからの作成を拒否し、OAuth resource-owner
      password grantとDevise user HTTP Basic authenticationを削除する。token endpointの
      confidential client認証用HTTP Basicは維持する。

### SEC44-05: dependency and image security

- [ ] Go modules と frontend lockfile を更新し、`govulncheck` と package audit の reachable
      high/critical を解消する。
- [ ] exact release image を Trivy で fixable/all の両方スキャンし、FFmpeg 7.1.5 以上を
      確認する。
- [ ] unfixed base-image findings を fixable 0 と混同せず、release risk disposition を残す。

## P0: PostgreSQL schema と migration

### DB44-01: final schema `20250627132728`

- [x] authoritative `internal/paon/migrate/schema.sql` を upstream 4.4.22 catalog と一致させる。
- [x] `quotes`、`rule_translations`、`tag_trends`、`terms_of_services`、
      `instance_moderation_notes`、FASP 5 tables、annual-report status counts を追加する。
- [x] `statuses.fetched_replies_at`、`statuses.quote_approval_policy`、
      `status_edits.quote_id`、`status_stats.untrusted_*_count`、`users.age_verified_at`、
      `users.require_tos_interstitial`、`announcements.notification_sent_at`、
      `web_push_subscriptions.standard` を追加する。
- [x] `imports` table、legacy user settings columns/index、legacy OTP columns、old public status
      index、duplicate WebAuthn indexをfinal schemaから削除する。
- [x] polls、scheduled statuses、tombstones、push subscription、account aliases/notes/pins/
      conversations/domain blocks/deletion requests、markers、poll votes、invite requests の
      NOT NULL と cascade FK を exact matching する。
- [x] settings は global `var` unique contract とし polymorphic thing columns を削除する。
- [x] fresh DBの `ar_internal_metadata.schema_sha1` を `b53e3b8de778cd1b53158326b97afa9368f3237e` とし、staged DBでは上流同様にsource schema SHAを保持する。

### DB44-02: staged 4.3→4.4 upgrade

- [x] `20241007071624` を明示的な supported source とし、expand/backfill/validate/contract
      の advisory-locked、idempotent、resumable upgrade を実装する。
- [x] advertised 4.2.29 DB `20230907150100` からは既存 4.3 phases を通して 4.4 phasesへ
      連続到達できるようにする。
- [x] unknown/future/partial marker を fail closed し、4.4 の67 markerを個別に記録する。
- [x] destructive contract は旧 web/worker が停止し明示ackされた後だけ実行する。

### DB44-03: data backfills

- [ ] Redis tag trend state を `tag_trends` へ language/allowed/rank/score とともに移す。
- [ ] legacy user settings rows/columns を final global settings へ安全に集約し、重複 var を
      事前検出する。
- [ ] public timeline index を `(id, language, account_id)` partial indexへ入れ替える。
- [ ] quote ID sequence/timestamp ID migration と `legacy` flag を upstream順序で適用する。
- [ ] annual report account/status count、ToS、rule translation、FASP seed/backfillを再実行可能にする。

### DB44-04: OTP/import contract

- [ ] `users.otp_secret` が有効2FA全rowで復号・TOTP検証できることを contract前に確認する。
- [ ] contract phase で `encrypted_otp_secret`、`encrypted_otp_secret_iv`、
      `encrypted_otp_secret_salt` を削除し、runtime の `OTP_SECRET` 依存を除去する。
- [ ] legacy import recordsの処理終了を確認して `imports` tableをdropする。

### DB44-05: catalog and data gates

- [x] empty DB、4.3.23 populated DB、4.2.19 populated DB の3経路を実 PostgreSQL 14/15で試験する。
- [x] relations/columns/types/defaults/nullability/index expression/predicate/order/FK/delete action/
      sequences/views/functions/extensions/seeds/539 markers を `pg_catalog` で比較する。
- [ ] production-volume lock、table rewrite、replica lag、disk headroom、backup/restoreをrunbook化する。

## P0/P1: quote posts and federation

### QUOTE44-01: SQL model and REST entities

- [ ] official `quotes` state modelをGORMへ追加し、posting/quoted account/status、approval URI、
      activity URI、legacy/stateを保存する。
- [ ] `Status` と `StatusEdit` に 4.4 `quote` objectを追加し、shallow/deleted/pending/rejected/
      accepted serializer shapeをexactにする。
- [ ] Mastodon 4.4のPostgreSQL `quotes` modelを唯一のruntime source of truthとする。
      Paon固有DynamoDB quoteへの新規read/writeはcutover後に禁止し、恒久dual-writeは行わない。
- [ ] 既存DynamoDB `status_quotes`を公式SQL modelへ一方向に移す、停止時間を伴う再開可能な
      cutover commandを用意する。件数、status/quoted-status/account参照、legacy/state、URLを
      検証してからSQLへ切り替え、DynamoDBはrollback保持期間後に明示的に廃止する。
- [ ] quote visibility、filter、CW、poll、media、limited account、block/mute/domain blockを
      nested statusと同じ規則で適用する。

### QUOTE44-02: ActivityPub FEP-044f

- [ ] actor/note JSON-LD context、`quote`、`quoteUri`、`quoteUrl`、Misskey fallback、
      `QuoteAuthorization`を4.4.22定義へ合わせる。
- [ ] inbound Create/Update/Delete/Tombstone、常時RejectするQuoteRequest、out-of-order implicit
      updateをtransaction-safe/idempotentに処理する。QuoteRequestへのAccept/Reject応答とUndoは
      4.4.22では未実装のため追加しない。
- [ ] quote authorization fetch/delivery/retry/cache、recursive reply/quote fetch limitsを実装する。
- [ ] account merge/suspend/delete/move、status delete/redraft/editでquote参照を壊さない。

### QUOTE44-03: search, trends, notifications and streaming

- [ ] `has:quote` search operator、quoteを含むreach計算、trend eligibilityを実装する。
- [ ] notification、email、featured carousel、annual/export、admin moderationにquoteを表示する。
- [ ] quote state/revoke/deleteをSSEとWebSocketへ同じevent/orderでpublishする。
- [ ] account mergeは`quotes.account_id`/`quoted_account_id`を移し、Appealの
      `account_warning_id`をaccount IDとして誤更新しない。

## P1: REST API、OAuth、HTML

### API44-01: accounts, tags and media routes

- [ ] `GET /api/v1/accounts/:id/endorsements`、`POST .../:id/endorse`、
      `POST .../:id/unendorse`を追加し、legacy pin/unpin aliasとrelationship entityを一致させる。
- [ ] `POST /api/v1/tags/:id/feature` と `unfeature` を追加する。
- [ ] `DELETE /api/v1/media/:id` をowner-onlyかつ使用中attachment拒否で実装する。
- [ ] `DELETE /api/v1/statuses/:id?delete_media=true` のmedia cleanup契約を実装する。
- [ ] `GET /api/v1/annual_reports/:id` と既存read/indexの4.4 serializer/paginationを合わせる。
- [ ] obsolete `POST /api/v1/polls` routeを除去する。

### API44-02: entity changes

- [ ] instance v2 `registrations.min_age`、`registrations.reason_required`、about/privacy/ToS URLs、
      media `description_limit`を追加する。
- [ ] instance v2 `api_versions.mastodon` をMastodon 4.4.22の値`6`へ更新する。
- [ ] Rule entityにordered `translations`を追加しrequest localeでtext/hintを選択する。
- [ ] Filter `filter_action=blur`、Status/StatusEdit quote、WebPush standard、tag relationship、
      untrusted remote countsをserializerへ追加する。
- [ ] invalid enum/date/lang/parameter は500ではなくexact 4xx/422 shapeを返す。
- [ ] deprecated endpointへ `Deprecation` と関連headerを付ける。

### AUTH44-01: OAuth and sessions

- [ ] `GET`/`POST /oauth/userinfo` をOIDC claim/scope contractで実装する。
- [ ] resource-owner password grantとDevise user HTTP Basic authを削除し、token endpointの
      client HTTP Basic、PKCE/browser session/client credentialsを回帰する。
- [ ] admin CLI password changeでbrowser sessionsとaccess tokensを失効させる。
- [ ] OAuth metadata/profile scope/last-used application orderingを4.4.22と差分試験する。

### WEB44-01: HTML and content negotiation

- [ ] `/@:username/featured`、`/terms-of-service`、dated ToS、`/terms` redirect、content negotiation
      とcanonical/cache/CSPを実装する。
- [ ] status pageにlanguage-aware OpenGraph `og:locale`、RSS/JSON alternate、CW textを出す。
- [ ] custom CSSをcontent hashでinvalidateし、long cacheとcompatibility assetを保つ。
- [ ] `EXTRA_MEDIA_HOSTS`をCSPへ安全に追加し、URL/schemeを検証する。

## P0/P1: Terms of Service、age verification、rules

### TOS44-01: Terms of Service lifecycle

- [ ] admin draft/preview/publish/history/test/distributionを実装し、effective date uniquenessを守る。
- [ ] current/date指定のpublic HTMLとREST entityをlocale、cache、404契約込みで提供する。
- [ ] existing usersのinterstitial状態を保存し、予定変更通知を一度だけ送る。
- [ ] bulk notification mailと通常transaction mailを別transportにできるようにする。

### AGE44-01: registration age gate

- [ ] admin minimum-age settingとinstance entityを実装する。
- [ ] browser/API registrationの`date_of_birth`をserver-sideで検証し、生年月日自体は保存せず
      `age_verified_at`のみ保存する。
- [ ] timezone/date boundary、leap day、missing/invalid/underage、invite/API flowを試験する。

### RULE44-01: translated/reorderable rules

- [ ] rule translation CRUD、unique locale、fallback、order変更、admin previewを実装する。
- [ ] public/API/registration/mailで同じlocale選択とplaceholder/hint契約を使う。

## P1/P2: FASP and experimental APIs

### FASP44-01: provider registration and security

- [ ] `EXPERIMENTAL_FEATURES=fasp` のときだけprovider registration/admin managementを有効化する。
- [ ] provider state/secret/base URL/capabilitiesをDBに保存し、未確認providerを拒否する。
- [ ] outbound FASP HTTPは共通SSRF/TLS/timeout/redirect policyを使い、secretをlog/traceしない。

### FASP44-02: data sharing and recommendations

- [ ] event subscription create/delete、backfill request/continuation、signed callback、retry/idempotency
      を実装する。
- [ ] discovery providerからのfollow recommendationをrequesting/recommended accountごとに保存し、
      existing recommendation sourcesとblock/follow/request suppressionを適用する。
- [ ] FASP account searchを通常search permission/visibilityと同じ境界で統合する。
- [ ] debug callback/call UIはadmin権限とfeature flagで保護する。

### ASYNC44-01: async refresh and reply fetching

- [ ] `/api/v1_alpha/async_refreshes/:id` entity/stateを実装する。
- [ ] `FETCH_REPLIES_ENABLED` と cooldown/initial wait/global/single/pages limitsを設定可能にする。
- [ ] remote repliesをAsynqでbreadth/depth/duplicate/cycle/SSRF limits付き取得し、context refresh IDを返す。

## P0/P1: ActivityPub and streaming

### AP44-01: processing correctness

- [ ] known statusのauthor変更Createを再作成せず、inbound relevancyをactor/object/audienceで厳密化する。
- [ ] remote status updateはmedia最大4件のoff-by-oneを修正し、mixed media type、no-text edit、
      duplicate/invalid tagを安全に処理する。
- [ ] old unknown statusのUpdateをnew statusとして通知/trendへ流さず、Create/Update out-of-orderを
      implicit updateとして整列する。
- [ ] inlined featured collection、partial follower collection、suspended actor follow behaviorを回帰する。

### STREAM44-01: Go SSE/WebSocket parity

- [ ] quote hydrated status/poll、quote state/revocation、annual/signup grouping eventをserializeする。
- [ ] public timelineは`read`または`read:statuses` scopeを要求し、token/account suspension時に既存
      SSE/WS connectionを閉じる。
- [ ] standalone Node streaming serverを追加せずGo web processのport 3000で提供する。

## P0〜P2: existing-design React UI and i18n

### FE44-01: profile featured experience

- [ ] current profile/header/hover-card designへFollowers you know、relationship tags、featured tab、
      pinned carousel、endorsed accounts、featured tags操作を追加する。
- [ ] remove follower、account quick actions、null URL、limited account visibilityを正しく扱う。
- [ ] desktop/mobile/touch/keyboardでtab/order/focus/empty/loading/error stateを確認する。

### FE44-02: quote display

- [ ] Paon status/card/detail/modal/notification/email/admin designへaccepted/pending/rejected/deleted/
      silenced/CW/poll/media quoteを追加する。
- [ ] filter blur/hide/warnをquoted statusにも再帰適用し、無限fetchやnested interactionを防ぐ。
- [ ] 既存Paon designを拡張して受信したofficial quote state/policy/authorizationを表示する。
      4.4.22では公式のQuote作成flowが未実装のため、独自composer/APIへは接続しない。
- [ ] featured carousel、PiP、media modal、standalone status、embedで同じquote semanticsを使う。

### FE44-03: compose safeguards

- [ ] alt-text欠落確認、detected/selected language warning、posting spinner、poll beforeUnload、
      edit/delete-redraft empty option、sensitive toggleを既存compose designへ追加する。
- [ ] language名をUI localeで表示しalt-text modalに`lang`を付ける。
- [ ] existing emotional/custom spoiler/media reorder controlsを回帰する。4.4.22に存在しない
      独自quote compose controlは残さない。

### FE44-04: navigation, lists and search semantics

- [ ] 4.4 main-menu/narrow-screen情報構造をPaonのnavigationへ適用するが、upstream layout/CSSを
      直接コピーしない。
- [ ] list member add/removeをpast feedへ反映し、members/show/new/edit error/loadingを改善する。
- [ ] search query params、operators、spaces、pagination、`has:quote`、clear buttonを実装する。
- [ ] middle-click new tab、hashtag quick menu/moderation link、translation `t` shortcutを追加する。

### FE44-05: media and accessibility

- [ ] current lightboxへdouble-tap zoom、swipe dismiss、touch dropdown scroll、RTL arrows、safe-area、
      Safari blur/zoom、GIFV click targetを追加する。
- [ ] focus indicator/order、nested button除去、custom select/dropdown semantics、screen-reader labels、
      `<time>`、reduced motionを確認する。
- [ ] audio/video visual refreshは操作 semanticsだけ移植し、Paon themeを保つ。
- [ ] system scrollbar preference、Japanese kerning、bidi、high contrast/light themeを回帰する。

### FE44-06: onboarding, settings and admin

- [ ] existing onboardingへage/ToS/profile/featured recommendation stepsを追加する。
- [ ] 次版向け準備設定`default_quote_policy`（public/followers/nobody、4.4では投稿/AP送信に未適用）、
      server rule translation/reorder、ToS distribution、announcement email、instance notes、
      FASP、minimum ageを既存settings/admin componentへ追加する。
- [ ] annual report notification/view、announcement timestamp、moderation poll/quoteを表示する。
- [ ] Asynq admin、single-column-chat、emotional、custom spoiler、lightboxのPaon固有UIを保持する。

### FE44-07: locales and assets

- [ ] upstream 4.4 locale追加・rename・message IDをsemantic portし、Paon独自messagesを保持する。
- [ ] en/ja、regional locale、fallback、plural、placeholder、RTL、posting languageを検証する。
- [ ] Twemoji 15.1 emoji data/picker/completionを同期する。
- [ ] current Rspack entrypoint/lazy chunk/manifest/CSPを維持し、Vite設定は取り込まない。

## P1/P2: WebPush、mail、jobs、search、media、operations

### PUSH44-01: standard WebPush

- [ ] previous draftとstandard WebPush subscriptionを識別・serialize・deliverする。
- [ ] deliveryに`Unsubscribe-URL`を付け、owner-protected delete endpointを提供する。
- [ ] 2日超のnotificationをpushせず、memoization/locale/quote payloadを回帰する。

### MAIL44-01: bulk mail

- [ ] `BULK_SMTP_*` paired configurationをoptionalにし、未設定時は既存SMTPを使う。
- [ ] announcement/ToS distributionだけbulk transportを使い、retry/idempotency/rate/observabilityを
      Asynqで担保する。

### JOB44-01: Asynq parity

- [ ] account merge inventoryをquote/warning/severed/tag-followへ更新する。
- [ ] scheduled statusはfrozen authorなら破棄し、poll expirationをimplicit updateで再通知しない。
- [ ] list feed retroactive updates、follow recommendation、tag trend persistence、remote reply fetch、
      annual report、ToS mailをversioned payloadで実装する。
- [ ] self-destruct scheduler、worker restart、retry exhaustionをRedis topology別に回帰する。

### SEARCH44-01: search and trends

- [ ] account searchのspace normalization、known private GtS status、`has:quote`、FASP provider、
      analyzer mismatch warningを実装する。
- [ ] tag trendsはDB source of truthにし、language別rankとadmin allow/rejectを維持する。
- [ ] Meilisearch unavailable時のfallback/errorを既存Paon contract内で維持する。

### MEDIA44-01: processing and privacy

- [ ] moderated post mediaはpublic accessを拒否しmoderatorだけ許可する。
- [ ] libvips pathでavatar/header 8 MiB、HEIF/APNG/rotation、mixed remote media、VFR、
      yuvj420p、faststartを検証する。
- [ ] preview card acceptance、YouTube referer/start、S3 expensive delete timeout、Swift token
      throttling/storage-schemaを回帰する。

### CONF44-01: runtime configuration

- [ ] Mastodon 4.4で削除された`REDIS_NAMESPACE`をruntimeで拒否し、既存prefixed keysを
      non-prefixedへ移す明示preflight/migration commandを用意する。
- [ ] `MEILI_PREFIX`を`REDIS_NAMESPACE`から暗黙導出しない。
- [ ] `EXPERIMENTAL_FEATURES`、reply-fetch limits、`EXTRA_MEDIA_HOSTS`、bulk SMTP、min age、
      allow referrer、replica optionsをstrict/paired validationする。
- [ ] web/worker roleを独立起動し、Go web processがHTTP/REST/AP/SSE/WSをport 3000で提供する。

### OBS44-01: metrics and logging

- [ ] upstream optional Prometheus semanticsは既存OpenTelemetry metricsへ翻訳し、別Ruby exporterを
      導入しない。
- [ ] VCS revision、trace/span IDs、dependency spansを保持し、Redis spanだけでroot traceを作らない。
- [ ] secrets、authorization、request body、remote private URL、FASP tokensをlog/traceしない。

## release tracking: 4.4.0〜4.4.22

| Release | Paon gate                                                                                                                |
| ------- | ------------------------------------------------------------------------------------------------------------------------ |
| 4.4.0   | quote/FEP、profile featured、ToS/age、FASP、RFC 9421、reply fetch、API/entity、DB 67 migrations、UI/a11y/runtime changes |
| 4.4.1   | build without Redis、env special chars、migration index tolerance、reply account handling                                |
| 4.4.2   | quote context/implicit update、dropdown/custom-select a11y、Firefox/touch、age wording                                   |
| 4.4.3   | rate limit、quote race/recursion/trend/reach、recommendation filtering、null URL                                         |
| 4.4.4   | quote revoke/delete UI、poll/edit/media/admin/self-destruct/archive fixes                                                |
| 4.4.5   | `has:quote`、limited quote click-through、out-of-order Create/Update、CW                                                 |
| 4.4.6   | stream scope/suspension、CLI token invalidation、Tombstone quote、quote events/mail/carousel                             |
| 4.4.7   | soft-deleted quote、moderation mail、referrer setting                                                                    |
| 4.4.8   | quote-control security fixture                                                                                           |
| 4.4.9   | quote filtering、old Update semantics、storage-schema                                                                    |
| 4.4.10  | private-status disclosure、quoted poll hydration、YouTube/S3                                                             |
| 4.4.11  | SSRF、severed-relationship ownership、temporary signature status、blocked mentions                                       |
| 4.4.12  | AP/property limits、remote suspension、push ownership、duplicate signature、quote idempotency                            |
| 4.4.13  | blocked collection cache、move cache、invalid/duplicate tags、connection recycling                                       |
| 4.4.14  | FASP confirmation/SSRF transport、emoji purge/domain suspension                                                          |
| 4.4.15  | quote authorization、legacy redirect、10k remote alt text、signature Accept、list/poll/Swift fixes                       |
| 4.4.16  | email verification、quote JSON-LD definition                                                                             |
| 4.4.17  | SSRF prefix and JSON-LD graph security                                                                                   |
| 4.4.18  | attribution-domain spoofing、sanitizer DoS、large media descriptions                                                     |
| 4.4.19  | TLS verification、sensitive toggle                                                                                       |
| 4.4.20  | FFmpeg 7.1.5 / CVE-2026-8461                                                                                             |
| 4.4.21  | permission enforcement、IPv4-compatible IPv6、push delete、Create relevancy/author、merge/tag cleanup                    |
| 4.4.22  | embedded quote typo、Appeal/AccountWarning merge、remote media max-4 off-by-one                                          |

## implementation order

1. security hotfixとupstream route/schema/catalog fixtureを固定する。
2. 4.4 expand/backfill/validate/contractとmodelsを実装する。
3. quote/FEP、REST/OAuth/ToS/age/FASP/experimental APIsを実装する。
4. jobs/search/media/push/config/operationsを実装する。
5. existing-design UI/i18nを意味的に拡張する。
6. official Rails 4.4.22とのroute/catalog/live differentialと全repository gateを実行する。
7. 本番受入条件を満たした場合だけ advertised versionを4.4.22へ変更する。

## release gates

### Schema/data

- [ ] official 4.4.22 fresh DB、Paon fresh DB、4.3 upgrade、4.2 direct upgradeのcatalog差分が
      許容差分だけになる。
- [ ] 539 migration markers、schema SHA、seeds、data invariantsが一致する。
- [ ] backup/restore、production-volume locks、replica lag、rolling/contract fenceを実証する。

### API/federation/security

- [ ] official 4.4.22 route manifestにmissing routeがない。
- [ ] public/authenticated/admin/locale/visibility/scope/pagination matrixをreal Rails/Goで比較する。
- [ ] FEP-044f/RFC 9421/legacy signature/remote repliesをreal interopまたはdeterministic fixtureで比較する。
- [ ] 4.4.0〜4.4.22 security itemsをattack fixture、Go非該当、dependency evidenceへ分類する。

### UI/i18n

- [ ] desktop/mobile/touch/keyboard/screen reader/reduced motionで主要4.4 flowをE2Eする。
- [ ] Paon固有UIとcurrent designのvisual/interaction regressionがない。
- [ ] locale extract後にenがclean、全bundle/fallback/plural/RTL/manifest/CSP assetが成立する。

### Runtime/operations

```bash
task build
task test:rtk
task test:integration
task test:external
task routes:audit
yarn i18n:extract
yarn lint:js
yarn lint:json
yarn typecheck
yarn jest --runInBand
yarn build:production
docker compose config --quiet
docker build .
```

- [ ] Redis namespace migration/failover、worker restart、storage partial failure、Meilisearch
      unavailable、self-destruct、bulk mail、FASP callbackをrehearseする。
- [ ] exact image dependency scanに未承認のfixable high/criticalがない。

### Version

- [ ] `internal/paon/config/version.go`、RSS/User-Agent、instance v1/v2、software update、testsを
      一括で4.4.22へ更新する。
- [ ] implementationまたはproduction acceptanceが未完了の状態でversion文字列だけを変更しない。

## explicit non-goals

- Mastodon 4.4 のUI markup/CSS/layoutをそのままコピーすること。
- Rails/Ruby/Sidekiq/Vite/standalone streaming serverを復活させること。
- GORM AutoMigrateや暗黙的・破壊的schema変更。
- experimental featureをfeature flagなしで公開すること。
