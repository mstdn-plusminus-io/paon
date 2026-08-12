# Mastodon 4.3.23 互換化 TODO

## 目的

Paon を Mastodon 4.2.19 互換から Mastodon 4.3.23 互換へ更新するための調査結果と実装計画をまとめる。単に `MASTODON_VERSION` 相当の表示を変更するのではなく、次の契約を Mastodon 4.3.23 の最終状態に合わせる。

- PostgreSQL のスキーマと既存データの更新経路
- REST API、OAuth、ActivityPub、WebFinger、NodeInfo、HTML の外部契約
- SSE/WebSocket、通知、非同期ジョブの順序・再試行・認可
- React/Rspack Web UI、管理画面、メール、locale、静的 asset
- 4.3.0 から 4.3.23 までに公開されたセキュリティ修正
- Paon 固有機能と Go/Asynq/Meilisearch/単一 streaming process という実行モデル

この文書は実装前の調査結果であり、チェック項目が完了するまでは 4.3.23 互換を名乗らない。

## 比較基準と調査方法

| 対象 | tag / commit |
| --- | --- |
| 比較元 | Mastodon `v4.2.19` / `a58a2b5fafb33e6ced71884419f0da5f1b398ae7` |
| メジャー更新点 | Mastodon `v4.3.0` / `ab36c152f9e5f9054798504354365dcaef4e5c43` |
| 比較先 | Mastodon `v4.3.23` / `efb25b6aa2014201b04f25c390ad1f516a3ff52d` |
| Paon 調査対象 | `feature/4.3.23` の `461e40da0` |
| 調査日 | 2026-08-09 |

調査ではローカルの Mastodon checkout に対し、両 endpoint の tree、`CHANGELOG.md`、routes、serializers、models、services/workers、通常 migration と post-deployment migration、frontend、locale、設定例、テストを比較した。安定版 branch 同士の tag は単純な直系関係ではないため、最終ファイル状態は three-dot ではなく `git diff v4.2.19 v4.3.23` で比較している。差分規模は 3,617 files、`+154,741/-77,834` である。

特に大きい領域は次のとおり。

| 領域 | upstream の endpoint 差分 | Paon 移植上の注意 |
| --- | ---: | --- |
| `app/javascript` | 972 files、`+50,897/-22,441` | Paon 独自差分と 201 path で重複するため一括置換しない |
| `config/locales` | 437 files、`+37,305/-12,512` | upstream key を merge し、Paon 固有 key と日本語を保持する |
| serializers | 47 files、`+502/-340` | Go serializer の fixture を先に確定する |
| DB | schema version `20230907150100` から `20241007071624` | fresh schema だけでなく既存 4.2 DB の明示的 upgrade が必要 |

Paon の履歴は `v4.2.19` commit を基点にしつつ、その後の 4.2 stable 修正を一部 backport している。現在の `internal/paon/config/version.go` は `4.2.29` を広告するが、本調査の比較起点は依頼どおり 4.2.19 とし、各項目の状態は version 文字列ではなく Paon HEAD の実装とテストで判定した。

### 一次資料

- [Mastodon v4.3.23 CHANGELOG](https://github.com/mastodon/mastodon/blob/v4.3.23/CHANGELOG.md)
- [Mastodon v4.2.19 tag](https://github.com/mastodon/mastodon/tree/v4.2.19)
- [Mastodon v4.3.23 tag](https://github.com/mastodon/mastodon/tree/v4.3.23)
- [Mastodon 4.3 upgrade notes](https://docs.joinmastodon.org/admin/upgrading/#upgrade-to-version-43)
- [Mastodon REST API documentation](https://docs.joinmastodon.org/api/)
- [Mastodon ActivityPub extensions](https://docs.joinmastodon.org/spec/activitypub/)

## 優先度・状態

| 表記 | 意味 |
| --- | --- |
| P0 | セキュリティ、データ保全、起動、または公式 4.3 Web UI の動作を阻害する release blocker |
| P1 | 外部クライアント・連合・ユーザー向けの 4.3.23 互換性に必須 |
| P2 | 管理運用、UX、性能、診断まで含む完全な parity に必須 |
| 未対応 | Paon HEAD に対応する route/model/behavior がない |
| 一部対応 | 基礎実装または過去の backport はあるが 4.3.23 の最終契約を満たさない |
| 要監査 | 類似実装はある。差分 fixture または攻撃 fixture で同値性を証明する必要がある |
| 対応済み | HEAD の実装とテストで最終挙動を確認済み |
| 対象外 | Ruby/Rails 固有など、Paon の実行経路に存在しない。代替確認は必要 |

「対象外」以外の P0/P1/P2 を完了し、後述の release gate を通ることを 4.3.23 互換の完了条件とする。`対応済み` も Mastodon 4.3.23 reference との differential test を最終 gate で再実行する。

## 現状サマリー

| ID | 優先度 | 状態 | 領域 | 結論 |
| --- | --- | --- | --- | --- |
| SEC43-01 | P0 | 一部対応 | SSRF / URL | IPv4-mapped IPv6 は正規化するが 4.3.23 の全拒否 prefix を満たさない |
| SEC43-02 | P0 | 未対応 | JSON-LD signature | `@graph` を許可しており、4.3.23 の graph-restructuring 対策が必要 |
| DB43-01 | P0 | 未対応 | schema upgrade | Paon は `20230907150100` の fresh/validate のみで既存 DB を更新できない |
| DB43-02 | P0 | 未対応 | OTP | legacy column と Paon 独自形式を使用中。4.3 の Active Record Encryption 互換がない |
| NOT43-01〜04 | P0 | 未対応 | notifications | grouped API、policy/request、新 notification type がない |
| API43/AUTH43/DISC43 | P1 | 一部対応 | REST/OAuth/discovery | PKCE など一部はあるが route・entity の追加が不足 |
| AP43/STREAM43 | P0/P1 | 一部対応 | federation/streaming | 基本 AP/streaming はあるが 4.3 property、limits、順序・cache 修正が不足 |
| FE43-01〜13 | P0〜P2 | 未対応/一部対応 | Web UI | 旧 UI と独自拡張が共存。semantic three-way port が必要 |
| JOB43/OPS43/MEDIA43/CONF43 | P1/P2 | 一部対応 | worker/storage/telemetry | Go/Asynq/Meilisearch に意味を翻訳して実装する |
| VER43-01 | P0 | 未対応 | advertised version | 全 gate 後にのみ `4.3.23` へ変更する |

### Paonに先行実装があるため再利用・監査するもの

| behavior | Paonの現状 | 4.3.23で行うこと |
| --- | --- | --- |
| IPv4-mapped SSRF | `activitypub_signature.go`で`Unmap()`済み | 新prefixと全fetch経路/DNS/redirect fixtureを追加 |
| PKCE | `auth.go`にplain/S256と独自code保持がある | formal grant columns、S256-only external contractへ移行 |
| Account fields | `indexable`/`hide_collections`等を一部実装済み | unavailable masking、URL fallback、serializer shapeを差分試験 |
| AP URL scheme/cache privacy | http(s)制限、authorized-fetch cache privacyの基礎あり | blocked signer、likes/shares、全collectionで回帰 |
| AP processing | partial follower sync、inline featured、old/out-of-order Update等のbackportあり | 4.3.23 malicious/ordering fixtureで同値性を証明 |
| Private status masking | core statusでmissing/inaccessibleを404にする基礎あり | poll/embed/media/likes/shares/interactionをlocale別matrixで回帰 |
| only-key actor refresh | 既知actor URIへ限定する基礎あり | 未知URIからhandle fallback/createしない4.3.22 fixture |
| domain-block users visibility | confirmed/approved/functional userへ限定する基礎あり | moved/disabled/unconfirmedを含む4.3.4 regression |
| Streaming auth | scope、suspended user、system killの基礎あり | cache header、新event、token/account revokeのSSE/WS共通試験 |
| HTML sanitizer | remote `<embed>`除去等を一部backport済み | HTML5/ruby tagと全attributeの最終allowlistを比較 |
| Web Push ownership | `updateWebPushSubscription`が現在`id`だけで取得・更新しており不足 | 両PUT routeをactive sessionの`user_id`へscopeし、別user IDのnegative test |
| Media | libvips 8.16/native fallback、HEIF/AVIF基盤あり | 4.3 limits、HEIF/APNG/rotation/faststartを挙動で確認 |
| Feed maintenance | vacuum等のPaon commandが一部ある | list feed、inactive-user、retry semanticsをexact比較 |

先行実装はupstream commit hashの一致ではなく、4.3.23 referenceとの入力/出力・副作用比較でのみ「対応済み」へ昇格する。

## P0: セキュリティ修正

4.3.23 の patch release 群は単なる依存更新ではない。Paon は Ruby 依存を持たなくても、同じ入力・HTTP・ActivityPub・認可経路を持つ修正を個別に評価する。

### SEC43-01: SSRF と外部 URL 検証

状態: **一部対応**。

上流 4.3.17 と 4.3.23 は、URL 解決後の private/special address 拒否を拡張し、IPv4-mapped IPv6 を native address に正規化した。4.3.23 では少なくとも `::/128`、`64:ff9b:1::/48`、`3fff::/20` と mapped private/link-local address を拒否する。

Paon の `internal/paon/api/activitypub_signature.go` は `netip.Addr.Unmap()`、`::/128`、mapped IPv4 の検査を既に持つ一方、調査時点では `64:ff9b:1::/48` と `3fff::/20` が不足している。

- [ ] 上流 `app/lib/private_address_check.rb` の v4.3.23 最終 prefix を Go の全 outbound fetch 共通 policy に反映する。
- [ ] ActivityPub actor/object/key、WebFinger/host-meta、preview card/oEmbed、remote media、redirect、custom emoji、OIDC discovery の全経路が同じ resolver/redirect policy を使うことを確認する。
- [ ] DNS rebinding を防ぐため、検証した address と実接続先を結び付け、各 redirect hop を再検査する。
- [ ] IPv4、IPv6、IPv4-mapped IPv6、NAT64/local-use、unspecified、loopback、private、link-local、multicast、documentation prefix の table-driven test を追加する。
- [ ] `64:ff9b:1::1.1.1.1`、`3fff::1`、`::ffff:127.0.0.1`、redirect 後 private address、複数 A/AAAA の一部だけ private という fixture を拒否する。

関連 advisory: [GHSA-xfrj-c749-jxxq](https://github.com/mastodon/mastodon/security/advisories/GHSA-xfrj-c749-jxxq)、[GHSA-crr4-7rm4-8gpw](https://github.com/mastodon/mastodon/security/advisories/GHSA-crr4-7rm4-8gpw)、[GHSA-xx55-4rrg-8xg6](https://github.com/mastodon/mastodon/security/advisories/GHSA-xx55-4rrg-8xg6)。

### SEC43-02: Linked Data Signature と JSON-LD graph restructuring

状態: **未対応、最優先**。

Paon の `internal/paon/api/activitypub_ld_signature.go` と `activitypub_inbox.go` は `@graph` を明示的に扱い、署名対象の正規化後に graph から actor/object を選択する。Mastodon 4.3.23 は LD Signature が付いた payload に `@graph`、`@included`、`@reverse` が再帰的に含まれる場合、署名済みとして扱わない。これは署名対象の entry を落とす、または named graph の並べ替えで撤回済み `Announce` を再発行する攻撃を防ぐためである。

- [ ] JSON object/array を再帰走査し、LD Signature 付き payload の任意の深さにある `@graph`、`@included`、`@reverse` を検出する。
- [ ] 検出時は actor/object を「LD 署名検証済み」として処理しない。上流と同じく許される場合だけ unsigned original activity として通常の HTTP Signature 認可へ戻す。
- [ ] graph 内の一部 entry だけを再構築・選択して署名済み権限を付与しない。
- [ ] Delete/Undo/Announce/Update と actor key refresh を対象に、entry removal、named graph reorder、nested `@included`/`@reverse`、複数 graph node の悪性 fixture を追加する。
- [ ] 正常な非署名 `@graph` を受ける既存互換性と、悪性の LD-signed graph を拒否する境界を別々に固定する。

関連 advisory: [GHSA-53m7-2wrh-q839](https://github.com/mastodon/mastodon/security/advisories/GHSA-53m7-2wrh-q839)、[GHSA-chgx-jx3p-rf73](https://github.com/mastodon/mastodon/security/advisories/GHSA-chgx-jx3p-rf73)。

### SEC43-03: HTTP Signature、ActivityPub input limit、remote suspension

状態: **要監査/一部対応**。

- [ ] `Signature` header の同名 parameter 重複を拒否し、先勝ち/後勝ちで解釈しない。
- [ ] ActivityPub inbox は宣言された `Content-Length` が 1 MiB を超える時点で `413`、chunked/偽装 length も実読込上限で拒否する。
- [ ] remote pollはoption数最大500件、remote accountはusername/display name最大2,048、note最大20,480 bytes、`alsoKnownAs`/`attributionDomains`最大256件、profile fields最大50件に制限する。
- [ ] local custom emoji shortcode 最大 128、remote 最大 2,048、list/filter title 最大 256、filter keyword 最大 512 を DB/API/連合の全入口で一致させる。
- [ ] suspended remote account から unknown status を作らず、boost、deleted target、fan-out、Update の各経路で suspension を再確認する。
- [ ] `Vary` header は comma-separated token として解析し、空白分割しない。

関連 advisory: [GHSA-gg8q-rcg7-p79g](https://github.com/mastodon/mastodon/security/advisories/GHSA-gg8q-rcg7-p79g)、[GHSA-5h2f-wg8j-xqwp](https://github.com/mastodon/mastodon/security/advisories/GHSA-5h2f-wg8j-xqwp)、[GHSA-6x3w-9g92-gvf3](https://github.com/mastodon/mastodon/security/advisories/GHSA-6x3w-9g92-gvf3)。

### SEC43-04: actor identity と URL scheme

状態: **要監査**。

- [ ] only-key actor refreshを既知actor URIだけへ限定するPaon既存挙動を回帰確認し、未知URIからusername/domain fallbackでaccountを作成・置換しない。
- [ ] actor ID、object ID、key owner、canonical URL の origin/URI が不正でも削除・配送 queue 全体を停止させず、対象だけを fail closed で捨てる。
- [ ] account/profile/status/media/preview URL は backend と frontend の双方で `http`/`https` のみ許可し、`javascript:`、`data:`、scheme-relative、制御文字、parse ambiguity を拒否する。
- [ ] HTTP redirect 後は署名を新しい request target/host で作り直す。
- [ ] built-in interaction policy context を network fetch せず解決する。

関連 advisory: [GHSA-5wxh-3p65-r4g6](https://github.com/mastodon/mastodon/security/advisories/GHSA-5wxh-3p65-r4g6)、[GHSA-x2rc-v5wx-g3m5](https://github.com/mastodon/mastodon/security/advisories/GHSA-x2rc-v5wx-g3m5)。

### SEC43-05: Web/API access control と情報漏えい

状態: **要監査/一部対応**。

- [ ] Paonの`PUT /api/web/push_subscriptions/:id`と`/:id/update`は現在IDだけでrowを取得・更新する。active sessionの`user_id`へscopeし、別user IDは404相当のnon-disclosing responseになることをnegative integration testする。
- [ ] severed relationship CSV/detail は current account が所有する event だけを返す。新機能の初回実装から ownership test を入れる。
- [ ] Paon実装済みのpublic streaming `read`/`read:statuses`、notification `read:notifications`、suspended/system killを、token失効・disabled userを含むSSE/WS共通testで回帰確認する。
- [ ] `paon-admin` の password reset/change は browser session、access token、refresh可能な grant をすべて revoke する。
- [ ] private status の存在有無を locale/`Accept-Language`/認証状態ごとの status・body 差で識別できないようにする。
- [ ] Paonの`activityJSONWithCachePrivacy`とauthorized-fetchがsigner依存応答を`private, no-store`にする既存挙動を、blocked signerと`AUTHORIZED_FETCH` on/offのfixtureで回帰確認する。
- [ ] domain block一覧の`users`可視性をconfirmedかつapprovedのfunctional userへ限定するPaon既存挙動を、unconfirmed/disabled/moved userで回帰確認する。

関連 advisory: [GHSA-f3q8-7vw3-69v4](https://github.com/mastodon/mastodon/security/advisories/GHSA-f3q8-7vw3-69v4)、[GHSA-ww85-x9cp-5v24](https://github.com/mastodon/mastodon/security/advisories/GHSA-ww85-x9cp-5v24)、[GHSA-r2fh-jr9c-9pxh](https://github.com/mastodon/mastodon/security/advisories/GHSA-r2fh-jr9c-9pxh)、[GHSA-7gwh-mw97-qjgp](https://github.com/mastodon/mastodon/security/advisories/GHSA-7gwh-mw97-qjgp)、[GHSA-f3q3-rmf7-9655](https://github.com/mastodon/mastodon/security/advisories/GHSA-f3q3-rmf7-9655)、[GHSA-gwhw-gcjx-72v8](https://github.com/mastodon/mastodon/security/advisories/GHSA-gwhw-gcjx-72v8)、[GHSA-ccpr-m53r-mfwr](https://github.com/mastodon/mastodon/security/advisories/GHSA-ccpr-m53r-mfwr)、[GHSA-94h4-fj37-c825](https://github.com/mastodon/mastodon/security/advisories/GHSA-94h4-fj37-c825)。

### SEC43-06: email、rate limit、redirect、HTML/CSP

状態: **一部対応/要監査**。

- [ ] user email は最大 320 文字とし、Mastodon 4.3.22 と同じく少なくとも `%`、`,`、`"` を local/domain 部に含む曖昧な address を拒否する。`mail.ParseAddress` の成功だけを根拠にしない。
- [ ] signup、email change/resend、`/auth/confirmation`、`/api/v1/emails/confirmations`の再送rate limit（5回/30分相当）をactor/IPだけでなく、Webはnormalized対象email、APIはauthenticated userへ正しくbindする。
- [ ] legacy `/web/...` redirect は `%2F`/`%5C`、scheme-relative、二重 encode を decode した後も external host へ遷移しない。
- [ ] logged-out user が remote resource へ進む場合は confirmation interstitial を表示し、silent open redirect を行わない。
- [ ] Paon sanitizerが既に`embed`を除外することを回帰確認し、`ruby`/`rt`/`rp`は4.3の許可属性だけを安全に通す。
- [ ] CSP の `form-action`、`img-src`、`media-src` を 4.3 の意図へ合わせ、object storage/CDN と remote form submit の regression test を持つ。
- [ ] `/oauth/revoke` CORS は必要な method/header/origin だけを返す。

関連 advisory: [GHSA-5r37-qpwq-2jhh](https://github.com/mastodon/mastodon/security/advisories/GHSA-5r37-qpwq-2jhh)、[GHSA-84ch-6436-c7mg](https://github.com/mastodon/mastodon/security/advisories/GHSA-84ch-6436-c7mg)、[GHSA-v39f-c9jj-8w7h](https://github.com/mastodon/mastodon/security/advisories/GHSA-v39f-c9jj-8w7h)、[GHSA-xqw8-4j56-5hj6](https://github.com/mastodon/mastodon/security/advisories/GHSA-xqw8-4j56-5hj6)、[GHSA-mq2m-hr29-8gqf](https://github.com/mastodon/mastodon/security/advisories/GHSA-mq2m-hr29-8gqf)。

### SEC43-07: Paon 固有 quote の追加監査

状態: **Paon 固有 P0**。

Mastodon の quote authorization advisory は 4.3 以下を quote 非対応として対象外にしている。一方 Paon は `internal/paon/api/status_quotes.go`、ActivityPub inbox、React status/compose に独自 quote を持つため、上流 4.3.21 の「対象外」をそのまま適用できない。

- [ ] reblog/boost を quote target にできないことを API、ActivityPub、Web UI の全入口で保証する。
- [ ] private/direct/followers-only、blocked/muted、deleted、edited、moved account の quote authorization を再検証する。
- [ ] quote の original URL、quote ID、fetched remote object を URL/SSRF/署名 policy の例外にしない。
- [ ] quote を含む status serializer/normalizer を 4.3 型へ port しても Paon 固有 field を落とさない。

関連 advisory: [GHSA-q4g8-82c5-9h33](https://github.com/mastodon/mastodon/security/advisories/GHSA-q4g8-82c5-9h33)。

### SEC43-08: Go では直接対象外の修正

- Ruby regexp engine に依存する 4.3.0 の ReDoS (`GHSA-jpxp-r43f-rhvx`) は Go の RE2 には直接該当しない。ただし username/hashtag/URL の最大入力長と bounded execution test は残す。
- Ruby/Rack/ActiveRecord/OmniAuth の依存更新は直接対象外。対応する Echo middleware、Go OAuth/OIDC、GORM query、Go module、npm package を脆弱性 scan し、同じ外部挙動の修正が必要か個別判断する。
- 依存更新を「対象外」と一括処理せず、`govulncheck`、npm audit 相当、container scan の結果を release evidence に残す。

## P0: PostgreSQL schema と migration

### DB43-01: target schema `20241007071624`

状態: **対応済み**。

Paon の `internal/paon/migrate/schema.sql`、`migrate/runner.go`、`db/db.go` は `20241007071624` を authoritative version とし、empty DBへのsnapshot適用と`20230907150100`からの明示的upgradeを行う。GORM AutoMigrateは引き続き禁止する。

#### 最終 schema へ追加するもの

| group | table / column / index |
| --- | --- |
| 通知 | `notifications.filtered boolean not null default false`、`group_key varchar`、未 filter index、`(account_id, group_key)` partial index |
| 通知 | `notification_policies`。`for_not_following`、`for_not_followers`、`for_new_accounts`、`for_private_mentions`、`for_limited_accounts` と account unique FK |
| 通知 | `notification_requests`。timestamp ID、owner/sender、nullable `last_status_id`、`notifications_count`、owner/sender unique |
| 通知 | `notification_permissions`。owner/sender と cascade FK |
| relationship severance | `relationship_severance_events`、`severed_relationships`、`account_relationship_severance_events` と unique/index/FK |
| recommendation | `follow_recommendation_mutes` と unique owner/target、`account_summaries(account_id,language,sensitive)` concurrent index |
| annual report | `generated_annual_reports` と `(account_id, year)` unique |
| OAuth / 2FA | `oauth_access_grants.code_challenge`、`code_challenge_method`、`users.otp_secret` |
| profile / link | `accounts.attribution_domains varchar[] default {}`（upstream finalどおりnullable）、`preview_cards.author_account_id` と partial index、`preview_cards_statuses.url` |
| moderation / admin | `email_domain_blocks.allow_with_approval`、`rules.hint`、`reports.application_id` と application削除時nullifyのFK |
| integrity | `mentions.status_id` と `mentions.account_id` を validated `NOT NULL`、notification FK 修正、`status_pins.created_at/updated_at` の DB default 除去 |
| duplicate cleanup | `(account_aliases.account_id,uri)`、`(custom_filter_statuses.status_id,custom_filter_id)`、`(identities.uid,provider)`、`(webauthn_credentials.user_id,nickname)` の重複を最小IDへ収束後unique化 |
| query index | `account_summaries(account_id,language,sensitive)`、notification filtered/group indexes |

誤った独自constraintを足さないため、次のfinal shapeも固定する。

- notification filtered partial indexは`(account_id,id DESC,type) WHERE filtered=false`で、既存の無条件`(account_id,id DESC,type)`も残す。
- `notification_policies.account_id`はunique/cascade FK、integer defaultsは`0,0,0,1,1`。
- `notification_requests`は`timestamp_id('notification_requests')` ID、`notifications_count bigint NOT NULL DEFAULT 0`、`(account_id,from_account_id)` unique。owner/sender FKはcascade、nullable `last_status_id` FKはnullify。
- `notification_permissions.account_id/from_account_id`はNOT NULL、各単独index/cascade FKを持つがcomposite uniqueは持たない。
- `preview_cards.author_account_id`、`preview_cards_statuses.url`、`reports.application_id`はnullable。preview-card/report application FKはnullify。
- `rules.hint`は`text NOT NULL DEFAULT ''`、`email_domain_blocks.allow_with_approval`は`boolean NOT NULL DEFAULT false`。
- `status_pins.created_at/updated_at`はNOT NULLのまま、DBの`now()` defaultだけを外す。
- severance eventはNOT NULLの`type/target_name`と`purged=false`、severed relationshipの`show_reblogs/notify/languages`はnullable、unique tupleは`(event_id,local_account_id,direction,remote_account_id)`、account event countsはNOT NULL DEFAULT 0。
- annual reportは`data jsonb`/`schema_version`がNOT NULL、`viewed_at`がnullable。

新 table の final shape は上流 `db/schema.rb` をそのまま写経するのではなく、型、default、nullability、index predicate/order、FK の `ON DELETE` まで `internal/paon/migrate/schema.sql` と schema guard に明記する。`20240217175251` で一度導入後に revert された attachment file-size の bigint 化は target schema に存在しないため実装しない。

#### 最終 schema から削除するもの

- end-to-end encryption 用の `devices`、`encrypted_messages`、`one_time_keys`、`system_keys` と関連 FK/index。
- `accounts.devices_url`。
- obsolete な `users.admin`、`users.moderator`。権限は `role_id`/role permissions へ統一する。
- notification policy の旧 column、`notification_requests.dismissed`。

`crypto`はschema要素ではない。destructive post-data migrationで`oauth_applications.scopes`と`oauth_access_tokens.scopes`から不可逆に除去する。

削除は expand/backfill が完了し、旧 web/worker process が停止した後の contract phase でのみ行う。Paon の `internal/paon/api/crypto*.go` と route manifest も同時に除去し、削除した table を参照する binary が混在しないことを deploy gate にする。

### DB43-02: 4.2 existing DB の明示的 upgrade path

状態: **対応済み（production volumeでの運用受入はcutover gate）**。

- [x] `paon-migrate` に `20230907150100 -> 20241007071624` の reviewed SQL を追加し、advisory lock、transaction、各 phase の記録を保持する。
- [x] **expand**: nullable column/table/FK/index を先に追加し、4.2 binary と 4.3 migration binary の同時アクセスを可能にする。大 table の index/constraint はtransaction内のwrite-blocking lockとして明示し、production相当volumeの計測をcutover gateにする。
- [x] **backfill**: duplicate row cleanup、notification settings/policy、Canadian French locale、OTP secret、profile scopeをidempotent batchで移す。mention NULLは修復せずread-only preflight/constraint validationでfailさせ、role backfillは行わない。crypto scope除去はdestructive post-data phaseへ分離する。
- [x] **validate**: null/duplicate/count/enum/secret decryptabilityをread-only queryとFK validationで確認し、失敗時はtransactionとversionを進めない。
- [x] **contract**: NOT NULL/validated FK を確定し、obsolete columns/tables を落とす。全 4.2 process 停止後だけ許可する acknowledgment を設ける。
- [x] fresh schema snapshot、`CurrentSchemaVersion`、startup guard、GORM models、serializer query、fixtures を同じ commit で更新する。
- [x] `schema_migrations` は migration ごとの履歴を残し、最終行だけを挿入して途中失敗を隠さない。

backfillの固定契約:

- duplicate index対象は最小IDのrowを残し、concurrent writeと競合したら安全にretryする。
- locale `fr-QC`を`fr-CA`へ変換する。
- legacy notification settingsの既定は`filter_not_followers=false`、`filter_not_following=false`、`filter_private_mentions=true`とし、既定と異なるuserからpolicyを生成する。
- policy v2 enumは`accept=0`、`filter=1`、`drop=2`、最終defaultはnot-following/not-followers/new-accountsがaccept、private-mentions/limited-accountsがfilterとする。
- `read:me` scopeを`profile`へ移す。legacy interaction-settingsのsecond passは既存policyを保持してmissing policyだけを補う一方、policy-v2のbool→integer post passは旧boolean列から全rowを再変換して上書きする。post passから旧列dropまで新enumへの書込みを止めるmaintenance windowまたは旧/新columnのdual-writeを設ける。

受入試験:

1. empty PostgreSQL に target snapshot を適用する。
2. pristine Mastodon 4.2.19/Paon 4.2 fixture を full data 付きで upgrade する。
3. duplicate、nullable mention、legacy notification settings、2FA enabled user、E2EE rowを含むworst-case fixtureを使う。nullable mention入りの初回upgradeは件数をsecret-freeに報告してfailし、承認済みdata修復後の再実行だけが成功する二段階試験にする。
4. 各 backfill を途中で kill し、再実行で同じ最終 data/hash/count になることを確認する。
5. 4.3.23 Rails reference が upgrade 後 DB を読み書きでき、Paon が upstream 4.3.23 schema を検証できることを双方向に確認する。
6. lock duration、table rewrite、disk headroom、replica lag を production 相当 volume で計測する。

rollback は expand 中なら旧 binary へ戻せるが、OTP 書換えと E2EE/obsolete column の contract 後は DB backup/restore を境界とする。upgrade 実行前 backup、restore rehearsal、旧 process 全停止の手順を `docs/paon-go-cutover.md` に追記する。

### DB43-03: OTP secret の暗号互換と移行

状態: **対応済み**。

Mastodon 4.3 は `users.encrypted_otp_secret*` から `users.otp_secret` の Active Record Encryption へ移す。既存 server は `OTP_SECRET` に加え `ACTIVE_RECORD_ENCRYPTION_DETERMINISTIC_KEY`、`ACTIVE_RECORD_ENCRYPTION_KEY_DERIVATION_SALT`、`ACTIVE_RECORD_ENCRYPTION_PRIMARY_KEY` を生成・固定し、post migration で復号・再暗号化する。invalid legacy secret を意図して無視する escape hatch もある。

Paon は現在 `models.User.EncryptedOTPSecret*` と `paon-go-totp:` prefix を legacy column に保存する独自経路を持ち、`otp_secret` model/AR ciphertext 互換がない。

- [x] Rails 4.3.23 が生成した `otp_secret` ciphertext を Go が復号でき、Go が生成した値を Rails 4.3.23 が復号できる、version/header/AAD/key derivation まで含む互換実装を用意する。
- [x] legacy Mastodon AES-256-GCM（PBKDF2-HMAC-SHA1、2,000 iterations、末尾 16 byte GCM tag）、Paon `paon-go-totp:`、既に AR-encrypted、2FA disabled/null の全形式を strict dispatch する。
- [x] migration対象はupstream同様`otp_required_for_login=true AND otp_secret IS NULL`のuserだけに限定し、既に`otp_secret`があるrowを再暗号化・上書きしない。
- [x] 旧 secret の復号に成功してから新 column を書き、TOTP code を旧/新で照合する。4.3.23 の final schema は `encrypted_otp_secret*` を残し、model も移行中 fallback を維持するため、Paon も独断で legacy columns を削除しない。失敗 user ID は secret を出さずに報告し、既定では migration 全体を fail closed にする。`MIGRATION_IGNORE_INVALID_OTP_SECRET=true` のときだけ対象 user を skip する。
- [x] encryption key は起動時 presence/length/consistency だけを secret-safe に検証し、ログ、CLI output、test snapshot に値を出さない。
- [x] key rotation/backup/restore/runbook と mixed-version deployment の read-old/write-new 方針を定義する。

受入条件は、legacy Rails、legacy Paon、new Rails、new Paon の 4 方向 fixture で同一 TOTP code が通ること、wrong key/tampered ciphertext が認証失敗になること、migration 再実行が ciphertext/2FA recovery code を破壊しないことである。

### DB43-04: migration inventory の追跡

状態: **対応済み**。`docs/mastodon-4.3-migration-inventory.md` に全versionの対応を記録した。

実装 PR は次の上流 migration 群を traceability table に一つずつ対応付ける。

- `20231006183200`〜`20240111033014`: preview-card original URL、duplicate unique indexes、OTP、recommendation mute/language index、email approval、locale、annual report。
- `20240217171534`〜`20240513123807`: status-pin default、notification filtered/request/permission/policy/settings/index/FK/group key。
- `20240310123453`〜`20240322125607`: rule hint と relationship severance tables/counts。
- `20240522041528`〜`20240724181224`: preview author、mention NOT NULL validate、report application FK、PKCE。
- `20240808114841`〜`20241007071624`: notification policy v2、attribution domains、permission FK。
- post migrations: OTP conversion、interaction settings second pass、severance obsolete count、obsolete role、`read:me -> profile`、request dismissed、E2EE tables、notification policy v2 cleanup、crypto scopes。

modified old Rails migration filesは lint/modernization の影響を含むため Paon へ再導入しない。Paon は target snapshot と上記 forward upgrade の最終意味だけを実装する。

## P0: notification subsystem

4.3 の最大の API/schema/UI 変更は通知である。backend、streaming、push、mailer、React state を別々に出荷せず、一つの end-to-end slice として実装する。

### NOT43-01: notification grouping と `/api/v2/notifications`

状態: **未対応**。Paon は `internal/paon/api/notifications.go` の v1 individual notification と legacy UI のみ。

追加する route:

| method | path | 契約 |
| --- | --- | --- |
| GET | `/api/v2/notifications` | group 一覧。filter、pagination、`include_filtered`、`types[]`/`exclude_types[]` を最終 4.3.23 と同じにする |
| GET | `/api/v2/notifications/:group_key` | group 1 件。ungrouped notification の 4.3.4 修正も含む |
| GET | `/api/v2/notifications/:group_key/accounts` | group sender accounts、stable pagination |
| POST | `/api/v2/notifications/:group_key/dismiss` | owner scope で group dismiss |
| POST | `/api/v2/notifications/clear` | grouped notification を owner scope で clear |
| GET | `/api/v2/notifications/unread_count` | grouped unread count |
| GET | `/api/v1/notifications/unread_count` | legacy notification unread count |

- [ ] notification 作成時の `group_key` を type/target/activity に応じて Rails と同じ規則で生成する。
- [ ] groupable typeは`favourite`、`reblog`、`follow`に限定する。最初のhour bucketをRedisへ保存し、そのbucketから12時間未満のnew activityは同じbucketを再利用、境界後に新groupを作る。favourite/reblogは`{type}-{target_status_id}-{hour_bucket}`、followは`follow-{hour_bucket}`、Redis keyはrecipientとtype prefixで分離し、filtered notificationにはgroup keyを付けない。
- [ ] group entityは`group_key`、`notifications_count`、`type`、`most_recent_notification_id`、最大8件の`sample_account_ids`、条件付き`status_id`/`report`/`event`/`moderation_warning`、pagination時の`page_min_id`/`page_max_id`/`latest_page_notification_at`を同じnull/omission規則で返す。
- [ ] list response envelope の `accounts` または `partial_accounts`、`statuses`、`notification_groups` と `expand_accounts=full|partial_avatars`、`grouped_types[]`、`include_filtered` を実装する。
- [ ] list default limit 40と、`types[]`/`exclude_types[]`/`grouped_types[]`をLink headerへ保持する。
- [ ] unread countはmarkerを考慮し、既定100件・最大1,000件を数え、`{count: integer}`だけを返す。上限到達を示す独自flagは追加しない。
- [ ] cursor、`min_id`、`max_id`、`since_id`、limit、Link header、最終 page、同時 insert、slow mode の重複を Rails fixture と比較する。
- [ ] follow grouping preference、clear/dismiss、marker/unread、push/mail の重複抑制を確認する。
- [ ] v1 `Notification` に `group_key` と conditional `filtered` を追加し、既存 v1 client を壊さない。

### NOT43-02: policies、filtered notifications、requests

状態: **未対応**。

追加する route:

| method | path |
| --- | --- |
| GET/PATCH | `/api/v2/notifications/policy` |
| GET/PATCH | `/api/v1/notifications/policy`（4.3 中の互換 route は上流最終 routes/behavior を fixture 化） |
| GET | `/api/v1/notifications/requests` |
| GET | `/api/v1/notifications/requests/:id` |
| POST | `/api/v1/notifications/requests/:id/accept` |
| POST | `/api/v1/notifications/requests/:id/dismiss` |
| POST | `/api/v1/notifications/requests/accept` |
| POST | `/api/v1/notifications/requests/dismiss` |
| GET | `/api/v1/notifications/requests/merged` |

- [ ] `not_following`、`not_followers`、`new_accounts`、`private_mentions`、`limited_accounts` をそれぞれ `accept`/`filter`/`drop` へ map する。
- [ ] filterable type は mention/reblog/follow/follow_request/favourite、non-filterable は status/poll/update/severance/moderation/admin 系として固定する。`drop` は保存せず、`filter` は保存しても stream/push/mail しない。
- [ ] new accountは30日、follow relationshipは作成後3日の境界を時刻固定testで再現する。new-account/private-mention/limited policyはいずれもrecipientがsenderをfollowしていない場合だけ適用する。
- [ ] moderator/staff mention と、最大 100 ancestor の既存 direct conversation に属する private mention の exemption を再現する。
- [ ] senderごとの`notification_permissions`はpolicyのfilter/dropだけをoverrideし、unavailable account、block、mute、conversation mute等のhard dropを迂回しない。
- [ ] v2 policy serializerは5 categoryを`accept, accept, accept, filter, filter`の既定値で返し、`summary: {pending_requests_count,pending_notifications_count}`を持つ。v1は先頭4 categoryだけをlegacy `filter_*: boolean`として返し、v2の`filter`と`drop`をどちらも`true`へ縮退、limited accountは含めない。
- [ ] request entity は string ID/count、timestamps、account、nullable last status を返し、owner scoped pagination を行う。
- [ ] request は filtered mention に対してだけ作成/更新し、`notifications_count` の実効上限 100、`last_status_id`、suspended sender の非表示を上流と合わせる。
- [ ] acceptはpermissionを保存して対象notificationsをmain inboxへmergeする。single dismissはcleanup workerをenqueueしてrequestを削除し、bulk dismissはrequestだけを削除してcleanup workerを起動しない。policyの`drop`は将来のnotificationを保存しないだけで既存filtered dataを遡及削除しない。permission tableにcomposite uniqueを独自追加せず、request owner lock等で並行acceptを直列化してidempotencyを保つ。
- [ ] bulk request は partial failure、重複 ID、別 user ID、既削除 request を安全に扱う。
- [ ] merge完了後、Go web processのSSE/WSからpayload文字列`"1"`の`notifications_merged`を一度だけpublishする。
- [ ] moderator/staffからの`mention`だけをpolicy/filter対象外にする4.3.4最終挙動を含め、同senderからのfollow/favourite/reblogまで無条件にbypassさせない。

### NOT43-03: relationship severance notification

状態: **未対応**。

- [ ] server-wide domain blockのうちseverityが`suspend`の場合とuser domain blockで、削除前のfollow/follower relationshipと`show_reblogs`/`notify`/languagesを`severed_relationships`へ記録する。server-wide `silence`と、途中で廃止された単一remote account suspension通知は4.3.23対象に含めない。
- [ ] local affected account ごとの followers/following count と event を作り、`severed_relationships` notification を一度だけ生成する。
- [ ] v1/v2 serializer の `event`、streaming、push、Web UI、CSV export/detail page を実装する。
- [ ] domain purge時は既存eventを`purged=true`にする。event owner以外へは返さず、purged eventはowner一覧に残して`purged: true`を表示するがrelationship CSVは提供しない。独自のexpiry契約は追加しない。
- [ ] worker retry で二重 event/notification/count を作らない。

### NOT43-04: moderation warning notification

状態: **未対応**。

- [ ] local moderation action/warning 作成時に `moderation_warning` notification を生成する。
- [ ] warning/action type、report、status deletion後/null target を v1/v2 entity へ安全に serialize する。
- [ ] policy/filterで隠さず、DB、streaming、web push、legacy UI、grouped UIに同じ内容を出す。4.3.23では`moderation_warning`と`severed_relationships`はnon-email typeでありmailを送らない。

### NOT43-05: notification acceptance matrix

- sender relation: following / follower / neither / new / limited / blocked / muted。
- activity: mention、private mention、follow、follow request、favourite、reblog、poll、status、update、admin sign-up、report、severance、moderation warning。
- API: v1/v2、filtered on/off、grouped/ungrouped、pagination boundary、deleted status/account。
- delivery: DB、SSE、WebSocket、web push、mail、marker/unread count。
- timing: concurrent insert、out-of-order job、worker retry、accept/dismiss race、slow mode。

上記 matrix を PostgreSQL/Redis integration test と Rails 4.3.23 differential fixture の双方で通す。

## P1: REST API、OAuth、discovery、HTML routes

### API43-01: 新規/変更 REST routes

notification routes を除き、route manifest に次を追加する。`internal/paon/api/server.go` と `api_route_parity_test.go` を同時に更新し、method、path parameter、format、scope、CORS、pagination を Rails 4.3.23 から生成した manifest と比較する。

| method | path | 実装要点 |
| --- | --- | --- |
| GET | `/api/v1/accounts?id[]=...` | 最大40。optional auth、`read`/`read:accounts`、ID重複を除去し欠損/unavailableをfilterする。出力順はinput順と仮定せずreference比較 |
| GET | `/api/v1/statuses?id[]=...` | 最大20。optional auth、`read`/`read:statuses`、ID重複を除去し各statusのvisibility/filter/current-account stateを個別適用。出力順はreference比較 |
| GET | `/api/v1/timelines/link?url=...` | discovery opt-in の public posts、block/filter、Link pagination |
| GET | `/api/v1/domain_blocks/preview?domain=...` | `follow`/`write`/`write:blocks`。`{following_count,followers_count}`を返し実mutationはしない |
| GET | `/api/v1/annual_reports` | `read`/`read:accounts`。ownerの未読/pending reportsを`annual_reports`/`accounts`/`statuses` envelopeで返す |
| POST | `/api/v1/annual_reports/:year/read` | `write`/`write:accounts`。owner scopeで`viewed_at`を更新し、empty responseを返す |
| GET | `/invite/:invite_code` with JSON | `{invite_code,instance_api_url}`。無効inviteは401、HTML signup routeとcontent negotiationを分離 |
| GET | `/api/v1/accounts/relationships?with_suspended=true` | 既存endpointの挙動変更。suspended accountを明示指定時に含める |

既存 endpoint で変更するもの:

- [ ] `/api/v1/announcements` の `statuses` は通常の REST `Status` entities とし、filters/current-account state/null を通常 timeline と揃える。
- [ ] pinned account statuses と `reblogged_by`/`favourited_by` accounts の pagination `Link` header、string IDs、重複除去を最終挙動へ合わせる。
- [ ] search v2 の `min_id`/`max_id`、operator preservation、invalid/private URL resolve、logged-out/limited-federation behavior を 4.3.23 に合わせる。
- [ ] `/api/v1/apps/verify_credentials` は token introspection に `read` scope を要求せず、secret を含まない public application entity を返す。
- [ ] registration API で invite code を受け、expired/exhausted/disabled/approval-required を HTML signup と同じ rule で判定する。
- [ ] user/account confirmation、following/followers、search の auth boundary を 4.3.23 reference で再固定する。

### API43-02: entity/serializer 契約

`internal/paon/serializer/rest.go` に snapshot test を先に追加し、次の field と omission/null/type を合わせる。

| entity | 4.3.23 の変更 |
| --- | --- |
| `Account` | `indexable`、`hide_collections`、unavailable/suspended account の field/URL fallback。Paon の既存 field は differential test で再確認 |
| `Notification` | `group_key`、conditional `filtered`、`event`、`moderation_warning` |
| `NotificationGroup` | group/count/type/latest/page/sample IDs、status/report/event/warning |
| `NotificationPolicy` | 5 policy categories と pending counts。v1/v2 serializer の違いを保持 |
| `NotificationRequest` | ID/timestamps/count/account/last status |
| `PreviewCard` | `authors[] {name,url,account}`、association に保存した original URL 優先、empty authors |
| `Instance` v2 | `icon[]`、`api_versions: {mastodon: 2}`、VAPID public key、account/status/media/poll limits |
| `Instance` v1 | 対応する limits/URLs を server constants から算出し、v1 shape は維持 |
| `Rule` | `hint` |
| `Suggestion` | deprecated `source` を維持しつつ `sources[]` を追加 |
| `Application` | publicはid/name/website/scopes/`redirect_uris`とdeprecated `redirect_uri`/`vapid_key`、credentialだけclient_id/secret/`client_secret_expires_at: 0`を追加 |
| `Announcement` | referenced statuses を regular `Status` として返す |
| `AnnualReport` | `year`、`data`、`schema_version`だけを返す。`viewed_at`はentity fieldに含めない |
| admin email-domain block | `allow_with_approval` |

Paon 固有の `quote_id`、`quote_original_url`、emotional/custom spoiler field は 4.3 type/normalizer へ明示的に再追加し、upstream に存在しないという理由で削除しない。

### AUTH43-01: OAuth PKCE、metadata、scope

状態: **一部対応**。Paon の `internal/paon/api/auth.go` は `code_challenge`/`code_challenge_method` の検証を持つが、DB は grant columns を持たず、4.3 の外部 contract 全体は未完了。

- [ ] PKCE challenge/method を authorization request から `oauth_access_grants` に保存し、token exchange で verifier を一度だけ検証する。metadata で公開するのは `S256` のみとし、S256 success、plain、method missing、mismatch、replay、別 client/redirect URI を reference test にする。
- [ ] application 作成時の newline/space 区切りを含む複数 `redirect_uris`、authorization/token 時の exact redirect URI match を実装する。
- [ ] RFC 8414 `GET /.well-known/oauth-authorization-server`を追加し、issuer、authorization/token/revocation endpoints、`scopes_supported`、`response_types_supported`、`response_modes_supported`、`grant_types_supported`、`token_endpoint_auth_methods_supported`（`client_secret_basic`/`client_secret_post`）、`code_challenge_methods_supported: ["S256"]`、`service_documentation`、Mastodon拡張`app_registration_endpoint`をbase URLごとに正しく返す。
- [ ] `profile` scope を追加し、post migration で `read:me` を変換する。scope implication と account credential field の露出範囲を route ごとに test する。
- [ ] `/oauth/revoke` の CORS/preflight と idempotent invalid token response を合わせる。
- [ ] OAuth application responseはcreate/credential/publicでsecret exposureを分離し、4.3.2の`client_secret_expires_at: 0`（never expires）をnumberとして固定する。
- [ ] OIDC を有効にする場合は `OIDC_USE_PKCE` で S256 を使い、Mastodon OAuth と外部 IdP の PKCE state を混同しない。

### AUTH43-02: browser session/cookie rolling compatibility

- [ ] Mastodon 4.3 が新cookieで使うdigest/rotator behaviorと、4.2 cookieの読み取り互換をPaonのbrowser session fixtureで比較する。
- [ ] rolling windowでは4.2/4.3双方が必要なcookieを読めることを確認し、切替後にpre-4.2 serverを混在させない。
- [ ] password/email/2FA/session security event時のcookie rotation、CSRF binding、session activation revocationをAPI/HTML/streamingで一貫させる。

### DISC43-01: WebFinger、host-meta、NodeInfo、redirect

- [ ] `/.well-known/host-meta.json` を JRD で返し、既存 XRD `host-meta` の Accept/format/cache header を壊さない。
- [ ] `/.well-known/oauth-authorization-server` を上記 OAuth metadata として返す。
- [ ] `/nodeinfo/2.0` に CORS を付け、`metadata.nodeName`、`metadata.nodeDescription` を scalar/object の正しい shape で追加する。
- [ ] `/redirect/accounts/:id`、`/redirect/statuses/:id` は local known object から安全に目的 URL を解決し、missing/malformed URL は external redirect しない。
- [ ] legacy `/users/...`の`redirect_with_vary`は`Vary: Origin, Accept`を保つ。`/web/*`とencoded `%40`はそれぞれの上流header契約を比較し、全redirectでformat/queryとSEC43-06のopen-redirect fixtureを通す。
- [ ] PWA route は `/links/:url`、`/notifications`、`/notifications/requests`、`/notifications/requests/:id`、nested `/start/*` を refresh/deep-link しても app shell を返す。`notifications_v2` はmodule/chunk名でありURL routeとして追加しない。

### REG43-01: registration と email-domain approval

- [ ] `email_domain_blocks.allow_with_approval=false` は拒否、`true` は登録を許し `approved=false` とする。exact domain と MX match、登録時と confirmation 時を同じ判定にする。
- [ ] admin REST/HTML/CLI で値の作成・表示・削除・audit log を実装する。
- [ ] unconfirmed user cleanup TTL を 48 時間から 1 週間へ変更し、WebAuthn/invite/moderation-note 関連 row があっても安全に削除する。
- [ ] email validation/rate limit は SEC43-06 の中央実装を API、HTML、admin、CLI で共有する。

### WEB43-01: remote permalink confirmation と content negotiation

状態: **未対応/一部対応**。Paon は remote account/status の HTML permalink を直接 external URL へ redirect する経路がある。

- [ ] logged-out HTML request は local `/redirect/accounts/:id` または `/redirect/statuses/:id` の確認画面へ送り、JSON/ActivityPub content negotiation と分離する。
- [ ] 確認画面をlocalized、robots `noindex, noarchive`、canonical、`Vary: Accept-Language`、external link `rel="noreferrer noopener"`で実装する。
- [ ] missing/malformed/null remote URL は同一 origin から外へ redirect せず、安全な 404/説明へ落とす。
- [ ] local/remote、logged-in/out、HTML/JSON/AP Accept の matrix と SEC43-06 の encoded redirect fixture を通す。

### WEB43-02: OEmbed 4.3 format

- [ ] `/api/oembed` は旧 iframe URL ではなく blockquote placeholder と `embed.js` を返し、root `data-allowed-prefixes`、既定 width 400、optional height を合わせる。
- [ ] localで`distributable?`なpublic/unlisted statusだけをembed可能にし、private/direct/remote/deleted statusのexistenceを漏らさない。
- [ ] `embed.js` が複数回 load されても一度だけ初期化し、複数 embed、CSP、responsive height を試験する。
- [ ] FE43-07 の standalone React status と Go initial state が安定してから旧 iframe/DOM implementation を除去する。

## P0/P1: ActivityPub と federation

### AP43-01: actor/note の 4.3 extension

- [ ] actorにMastodon namespaceの`attributionDomains`を配信・受信し、remote actorは最大256件、local settings入力は最大100行、domain normalization、profile credential owner、許可domain updateを検証する。
- [ ] Note に `likes` と `shares` collection URI を追加し、`GET /users/:username/statuses/:id/likes` と `/shares` を `id`/`type`/`totalItems` の Collection として配信する。actor 一覧自体を漏らさない。
- [ ] collection は status visibility、authorized fetch、block/domain block、deleted/unavailable actor を request signer ごとに再認可する。
- [ ] 4.3.23の`unavailable?`（suspendedのalias）に基づくserializer maskingをfieldごとに合わせ、limitedと混同しない。
- [ ] E2EE の `devices`、claim、encrypted-message property/routes/serializers を除去し、`crypto` scope も返さない。

### AP43-02: Create/Update/Delete/Undo/Announce の順序と idempotency

- [ ] Paon実装済みのCreate/Update逆転、published/updated判定、後着Createの一件収束を上流4.3.23 fixtureで回帰確認する。
- [ ] Paon実装済みの「古い未発見postのUpdateを新規post/notificationとして扱わない」挙動を回帰確認する。
- [ ] remote edit で text なし・new media、poll、mentions/silent mention、duplicate mentions、duplicate/invalid hashtags を transactionally更新する。
- [ ] deleted account/status、reblog of deleted target、unknown suspended actor は permanent failure と retryable network failure を区別し、無限 retry/fan-out しない。
- [ ] implicit update、poll expiration update、edit notification を重複発火しない。
- [ ] Paon の quote extension を含む Create/Update/Announce/Undo の idempotency key と authorization を SEC43-07 で固定する。

### AP43-03: collection、followers sync、account lifecycle

- [ ] Paon実装済みのinlined `featured`とpaginated partial follower synchronizationへ、missing `items`、malformed/partial collectionの4.3.23 fixtureを追加して回帰確認する。
- [ ] account suspension は suspension 時点から到達可能な account と recently-followed account にも配信する。
- [ ] account move 後は relationship cache/counters を invalidate し、follow migration と request/block/mute を整合させる。
- [ ] blocked/domain-blocked user の mention/tag/trend/collection item を処理しない。
- [ ] featured tags/posts の update/cache-busting URL と remote deletion を最終 patch behavior へ合わせる。

### AP43-04: HTML、media、poll の federated input

- [ ] remote HTML は malformed markup を HTML5 相当で安定して sanitize し、East Asian `ruby`/`rt`/`rp` を安全に保持する。
- [ ] remote attachment が複数 media type を示す場合の選択、unknown type、HEIF/APNG、rotation、description 最大 10,000 を実装する。local description 1,500 と混同しない。
- [ ] remote pollはoption数500/501件の境界、duplicate option、edit/expiry、future timestampをvalidateする。
- [ ] preview card canonical URL の文字列 `undefined` を canonical とみなさず、status ごとの original URL を `preview_cards_statuses.url` に保持する。

### AP43-05: delivery、fetch、cache の受入条件

- [ ] HTTP Signature の `(request-target)`/host/date/digest、redirect 後の再署名、correct signature default を Mastodon 4.3.23 receiver と相互試験する。
- [ ] 取得 response の `Content-Length` と実 body 上限、content type、`Vary`/cache-control、redirect hop を統一 adapter で検査する。
- [ ] `AUTHORIZED_FETCH` on/off、limited federation、anonymous/signed、follower/non-follower、blocked/domain-blocked の matrix で actor/status/replies/likes/shares/featured/followers を比較する。
- [ ] inbox processing は Go `ingress` Asynq queue、delivery は `push`/`pull` の既存 process boundary を維持し、web/worker を独立起動できる。

### STREAM43-01: Go streaming protocol parity

standalone Node streaming server/image と port 4000 は導入しない。Go web process の SSE/WebSocket を次の最終契約へ合わせる。

- [ ] SSE と streaming health は `Cache-Control: private, no-store` を返す。
- [ ] `severed_relationships`、`moderation_warning`、payload文字列`"1"`の`notifications_merged`をSSE/WSの双方で同じJSON/event名にする。
- [ ] E2EE `encrypted_message`/device channels を route、subscription、Redis fan-out から除去する。
- [ ] 全 stream は `read` または `read:statuses`、notification stream は `read` または `read:notifications` を要求する。
- [ ] disabled/suspended user、revoked token は新規接続を拒否し、接続中の session/token/account kill event で即切断する。

Paon は scope check、suspension check、system kill の基礎を既に持つため、未実装と決め付けず SSE/WS 共通 table test で監査する。

## P0〜P2: React Web UI、server-rendered UI、i18n

upstream 4.3 の Web UI は icon の全置換、compose/notification/modal/CW/media の再設計、TypeScript/Redux/router 基盤の更新を含む。Paon は v4.2.19 から frontend/locales/build だけでも 271 files の独自差分があり、4.3 差分と 201 path で重なる。`app/javascript` や locale の一括コピーは禁止し、次の順に semantic port する。

### FE43-01: entrypoint、TypeScript、Redux/router 基盤

優先度: **P0**。

- [ ] upstream の `packs` から `entrypoints` への整理、public/signup/admin の TypeScript 化、identity context、access tokenをRedux/contextから除去して共通API client/initial stateへ移す変更、React Redux 9、Redux Toolkit 2、Immutable 4.3.8、router 5.3.4 の意味を移植する。
- [ ] Node requirement を 18 以上へ上げ、`package.json`/lockfile/`tsconfig` の `noImplicitReturns`、`noUncheckedIndexedAccess`、incremental、`@/*` alias、entrypoint include を整合させる。
- [ ] upstream v4.3.23 はYarn 4.5.0（`yarn install --immutable`）だが、Paon HEADはYarn 1.22.22である。4.3互換にpackage manager自体は不要なため本upgradeではYarn 1を維持し、`--frozen-lockfile`とsecurity auditを使う。Yarn 4移行はlock/cache/CI/containerを一体で扱う別decisionとし、両lock方式を混在させない。
- [ ] feature dependencyの基準を少なくとも`@reduxjs/toolkit ^2.0.1`、`react-redux ^9.0.4`、`react-router(-dom) ^5.3.4`、`immutable ^4.3.8`、`@dnd-kit/core ^6.1.0`、`@dnd-kit/sortable ^8.0.0`、`@dnd-kit/utilities ^3.2.2`、`use-debounce ^10.0.0`、`hoist-non-react-statics ^3.3.2`、`@rails/ujs 7.1.401`へ合わせる。`@rails/ujs`は現行Go-rendered pageで使うbehaviorを確認してからupgrade/除去を判断し、Go backendだから不要とは決め付けない。
- [ ] upstream の Webpack config は戻さず、Rspack の entry mapping、SVG/assets、dynamic chunk、manifest を更新する。repo内`app/javascript/material-icons/**`の269 SVGを`?react` importできるSVGR rule、TypeScript SVG module declaration、title handlingを追加し、現在`node_modules/@material-design-icons`だけを対象とするruleでは不足することをfixture化する。
- [ ] Go HTML が参照する pack/entry name を server-rendered public/admin/share/signup/2FA/mail preview と同時に更新する。
- [ ] `packs/admin.jsx` にある Paon 固有 Asynq 管理機能を失わず、4.3 admin entrypoint と型へ移す。

受入条件: clean `yarn install --frozen-lockfile`、typecheck、lint、Jest、production build が通り、全 entry/lazy chunk が manifest に存在し、CSP nonce/CSRF を維持して各 Go-rendered page が load する。

### FE43-02: grouped notifications UI

優先度: **P0**。NOT43-01 と同一 slice で出荷する。

- [ ] `features/notifications_v2`、notification group API types/actions/reducer/selectors/model/wrapper/routes を 4.3.23 最終状態で移植する。
- [ ] first/next/last page、page内/跨ぎ merge、sample accounts、follow grouping on/off、slow mode、polling、clear/dismiss、unread marker を実装する。
- [ ] `notifications_merged` を SSE/WS で受け、request merge 完了後だけ group/request state を refresh する。
- [ ] deleted/null account/status、ungrouped legacy notification、unknown notification type でも crash しない。
- [ ] 単一の `/notifications` routeでgrouped UIを提供し、`/notifications/requests`と`/notifications/requests/:id`の2 route、既存column/history stateを両立する。module名`notifications_v2`をURLへ露出しない。

### FE43-03: notification policy/request/new types UI

優先度: **P0**。NOT43-02〜04 と同一 slice で出荷する。

- [ ] 5 category の accept/filter/drop 設定、filtered banner、pending summary、request 一覧/詳細/accept/dismiss/bulk UI を追加する。
- [ ] notification componentにはrelationship severance eventの説明と`/severed_relationships`へのLearn more linkを追加する。server-rendered detail pageではownerだけに`GET /severed_relationships/:id/followers.csv`と`/following.csv`を提供し、purged eventではdownloadを出さない。`moderation_warning`にはaction/report/status linkとiconを追加する。
- [ ] old `interactions.must_be_follower` 等の表示を policy へ migration し、legacy setting と新 policy が矛盾しない。
- [ ] moderator notification、unknown type、null target、purged event を安全に表示する。

### FE43-04: compose redesign と media reorder

優先度: **P1**。

- [ ] privacy と language を常時ラベル表示し、UI 表記の Unlisted を “Quiet public” へ変更する。API value `unlisted` は変えない。
- [ ] server-provided character limit と poll option/count limits を `/api/v2/instance` から使い、`/share`、reply/edit/redraft に同じ validation を適用する。
- [ ] `@dnd-kit` を用いて最大 4 media を pointer/keyboard/touch で並べ替え、送信される attachment ID 順と preview 順を一致させる。
- [ ] poll add/edit/delete/redraft、空 option、3枚以上 media height、processing progress の 4.3 patch fixes を含める。
- [ ] Paon の quote、emotional、custom spoiler compose state/action/reducer を 4.3 compose model へ統合する。

### FE43-05: Material Symbols と status/CW/filter/media/modal

優先度: **P1**。

- [ ] React UI の Font Awesome icon を Material Symbols と active/inactive variants へ段階移行する。
- [ ] Go-rendered admin/settings は現在 `fa` class を使うため、React 移行完了前に package/font を削除しない。Go側を置換した後にのみ旧 asset を除去する。
- [ ] compose、mute/block/domain-block modal、colors、status metadata、thread label、media gallery/profile media、navigation breakpoints を port する。
- [ ] CW と filter warning/banner を新 design にし、detail、favourites、bookmarks、boosted status、keyboard shortcut でも filter が適用されるようにする。
- [ ] CW 展開後の preview card、ALT badge popover、unknown media、hide-media、mobile sizing、RTL、reduced-motion、focus/ARIA を patch 最終状態で確認する。
- [ ] Paon 固有 lightbox/download と single-column-chat theme の class/interaction を維持する。

### FE43-06: link timeline と fediverse creator

優先度: **P1**。

- [ ] frontendの`/links/:url` routeはURLを`encodeURIComponent`で扱い、`GET /api/v1/timelines/link?url=...&max_id=...`をdata sourceにしてpagination/loading/empty/error UIを実装する。
- [ ] PreviewCard `authors` の resolved/unresolved author、account hover/link、original URL を表示する。
- [ ] `/settings/verification` で attribution domain を追加・削除・validate し、autocorrect/autocapitalize を無効にする。
- [ ] trending/discovery opt-in、block/filter、許可 domain、non-trendable card、empty authors の E2E を行う。

### FE43-07: embedded posts

優先度: **P1**。

- [ ] 旧 Go-rendered embed + `public.jsx` DOM 操作から、4.3 の standalone React status embed へ段階移行する。
- [ ] local status だけ embed code menu を表示し、remote status では 4.3.1 どおり非表示にする。
- [ ] 複数 embed、responsive height/postMessage、CW、media、poll、Paon quote、mobile、YouTube start parameter/referer を試験する。
- [ ] 新 React embed の initial state と route が安定してから旧 DOM implementation を除去する。

### FE43-08: system theme と preferences

優先度: **P1**。

- [ ] `system` theme を追加し logged-out default とする。OS light/dark change を追従し、user の明示 theme を優先する。
- [ ] hover card disable setting、follow notification grouping、volume persistence 等の 4.3 preference/meta/initial state を追加する。
- [ ] unfollow confirmation off settingを廃止し、常に confirmation を出す。既存 user setting の残値は無害に扱う。
- [ ] Web UI は home marker の自動更新を送らない。notification marker は維持し、他 client の home marker を上書きしない。

### FE43-09: follow suggestions、hover/profile、onboarding

優先度: **P1/P2**。

- [ ] suggestion carousel、dismiss、`sources[]` hint を REC43-01 の backend と実装する。
- [ ] account hover card を delay/leave/touch/keyboard/focus/disabled setting とともに実装し、bio 2 行、profile fields 2 件、account note/private field を安全に扱う。
- [ ] profile domain tooltip、share/copy、follow-back/mutual、responsive counters を追加する。
- [ ] onboarding profile setup と narrow-screen behavior を追加し、途中離脱/再開/完了 state を保存する。

### FE43-10: settings/admin UI

優先度: **P1/P2**。

- [ ] instance favicon/app icon upload/preview/delete、rule text+hint、hashtag moderation search/pagination を実装する。
- [ ] notification policy、attribution domains、system theme、hover setting を `internal/paon/api/settings_pages.go` 等の Go page と React initial state の双方へ反映する。
- [ ] admin quick links、reports、roles、audit entries、appeals、warning presets、forwarded-report banner、instance comments、content retention、Recommendations & Trends の navigation/permissions を更新する。
- [ ] admin/moderation noteの最大長を500から2,000へ、Admin Domain Management APIのpage上限を200から500へ更新し、DB/UI/APIのvalidationを一致させる。
- [ ] admin React API は Paon の session+CSRF contract を維持し、権限不足を HTML/JSON で正しく拒否する。

### FE43-11: locale と posting language

一括置換せず key 単位で merge する。

| locale source | upstream 4.3.23 | Paon HEAD | 不足の目安 |
| --- | ---: | ---: | ---: |
| Web UI `en.json` | 887 keys | 728 keys | upstream key 202 |
| Web UI `ja.json` | 887 keys | 730 keys | upstream key 203 |
| server `en.yml` scalar | 1,654 | 1,790 | upstream key 120 |
| server `ja.yml` scalar | 1,622 | 1,752 | upstream key 124 |

- [ ] extracted `en.json`を完全化し、notification policy/request/severance/warning、onboarding、profile hint、suggestion source、upload DnD、CW/domain/ALT/system theme/redirect/admin/mailについてupstream v4.3.23に翻訳が存在するlocaleだけをkey単位でmergeする。欠落keyは英語fallbackで解決し、未翻訳英語を全localeへ複製しない。`ja`はupstream最終key/valueをmergeする。
- [ ] simple-form en/ja の attribution domain、app icon、favicon、rule hint、hover-card setting の label/hint を追加する。
- [ ] locale inventory に `az`、`fil`、`fr-CA`、`ia`、`ie`、`lad`、`nan-TW`、`nan`、`ne`、`ry`、`tlh`、`tok` を加え、旧 `fr-QC` を data migration と locale negotiation の双方で `fr-CA` へ map する。
- [ ] Interlingue/Interlingua を interface language に、Kashubian/Pennsylvania Dutch/Vai/Jawi Malay/Mohawk/Low German と追加言語を posting language list へ同期する。
- [ ] Paon 固有 quote/lightbox/emotional/custom-spoiler/theme/Asynq admin key と日本語訳を保持する。
- [ ] extracted key、fallback、placeholder/plural、bundle load、RTL を CI で検査する。

### FE43-12: assets と mail design

- [ ] repo内`app/javascript/material-icons/**`の269 SVG、Inter font、filter/warning stripes、check image、4.3 image/style assets、Twemoji 15をRspack manifestとCSPへ追加する。`?react` import、SVGR title、tree-shaking、missing assetをproduction buildで検証し、既存icon packageでの見た目だけの代用はしない。
- [ ] user mail templates を 4.3 design/contentへ port し、Go `mailer.go`、server locale、inline CSS、plain text multipart、unsubscribe linkを同期する。
- [ ] mailer assets を Rails view のまま持ち込まず、Paon の Go mail deliveryで同じ visible content と安全な URL を生成する。
- [ ] major desktop/mobile mail clients向け snapshot/render test、locale fallback、filtered notification suppressionを確認する。

### FE43-13: Paon 固有 frontend 回帰契約

次は upstream に存在しなくても削除禁止とする。

- quote の compose/render/ActivityPub/REST fields
- emotional reaction と custom spoiler
- lightbox/download behavior
- single-column-chat theme と PlusMinus branding
- Asynq administration UI

受入 matrix は single/advanced column、narrow/wide、desktop/touch、logged-in/out、default/light/dark/system/contrast/Paon custom theme、RTL、keyboard-only、screen reader、reduced motion を最低限含む。

## P1/P2: recommendation と search

### REC43-01: 4 source と永続 dismissal

状態: **未対応**。Paon の `suggestions.go` と follow-recommendation worker は旧 past-interactions/global/staff と Redis dismissal を中心にしている。

- [ ] source を `Setting`、`FriendsOfFriends`、`SimilarProfiles`、`Global` の順で各最大 40 件取得し、15 分 cache 後に統合/shuffle する。
- [ ] current follows/request/block/mute/domain block、双方向 block、suspended/silenced/moved/memorial/non-discoverable、`hide_collections` を全 source で除外する。
- [ ] FriendsOfFriends は共通 follow 出現頻度降順、followers 数昇順を基本にする。
- [ ] dismissal を Redis の一時値ではなく `follow_recommendation_mutes` に永続化し、関連 cache を即 invalidate する。
- [ ] API の deprecated `source` を旧 client 向けに変換しつつ `sources[]` を返す。
- [ ] already requested account を勧めない 4.3.11 fix を含める。

### REC43-02: SimilarProfiles on Meilisearch

上流 Elasticsearch は直近 5 follows の profile を `more_like_this` に使う。Meilisearch に同一 primitive はないため、次を設計判断として記録する。

- [ ] searchable text、language、discoverable filter と ranking を使う互換近似を設計し、同じ除外・最大件数・dismissal 契約を守る。
- [ ] Meilisearch disabled/unavailable 時は他 source を壊さず SimilarProfiles だけ空にする。
- [ ] 固定 account corpus で上流 4.3.23 と top-N/ranking の許容差を定義し、結果品質を測定する。

### SRCH43-01: account/status search

- [ ] account ranking の reputation/time-decay を廃止し、followers の `log10(followers+1)` と following boost 100 を意味的に再現する。
- [ ] autocomplete は username/display name、full search は usernameを強くした AND semantics とし、Meili/SQL fallback の結果を一致させる。
- [ ] operators を pagination/retry 後も失わず、`min_id`/`max_id` を正しく扱う。
- [ ] GoToSocial の既知 private URL `/@username/(statuses/)?[0-9A-Za-z]+` を、request user に閲覧権限がある場合だけ解決する。
- [ ] invalid/null URL、deleted account/status、limited federation、anonymous search の error/redirect を reference と比較する。

## P1/P2: jobs、admin CLI、lifecycle

### JOB43-01: Asynq worker parity

Rails/Sidekiq class を再導入せず、次の意味を Asynq task として追加する。

- [ ] failed remote mentionを`rand(30...600)`秒delay、初回失敗後最大7 retryで解決し、`(status_id,account_id)`をidempotent upsertするMentionResolve task。
- [ ] filtered notification cleanup/unfilter、permission merge、完了 event を順序付きで行う tasks。
- [ ] relationship severanceはfollow削除前に同期的にsnapshotを保存する。復元不能になるためsnapshot自体を遅延taskへ送らず、通知/purgeだけをAsynq化する。
- [ ] annual report generation task。未利用 groundwork でも schema/API と整合する。
- [ ] daily recommendation refreshは`AccountSummary.refresh`の後に`FollowRecommendation.refresh`を実行し、retryなし・1日lock・cache invalidationを持つ。
- [ ] delayed profile update、partial follower sync、recent follower suspension delivery。
- [ ] deleted account/permanent 4xx は retry せず、transient fetch/object-storage error だけ retry する。

全 task に payload version、unique/idempotency key、retry/backoff、queue、timeout、dead-letter/retry UI、web/worker 分離 test を定義する。

### JOB43-02: feed maintenance と timeline fixes

- [ ] feed build は home に加えて所有 list timelines も構築する。
- [ ] `feeds vacuum` の既存 Paon 実装を 4.3.2 と exact behavior 比較する。
- [ ] inactive user への follow/unsuspend/direct inbox/hashtag-follow fan-out で不要な backfill/push をしない。
- [ ] reblog of deleted status、exclusive list と notification、status batch removal、tag follow account operation を retry-safe にする。
- [ ] materialized views は可能なものを concurrent refresh し、lock/empty-view failure を試験する。

### CLI43-01: `paon-admin` security/parity

- [ ] password reset/changeは、password更新と`session_activations`削除をtransactionで行い、その後access grants/tokensの`revoked_at`を更新し、tokenに紐づくWeb Push subscriptionsを削除、tokenごとにstreaming killをpublishする。全処理を一つのDB transactionと誤認しない。
- [ ] emoji purge に `--suspended-only` を追加する。
- [ ] storage schema upgrade/check、media refresh、search deploy、email-domain block DNS mode の 4.3 fix を対応する Paon command へ反映する。
- [ ] destructive command は dry-run/inventory/explicit acknowledgment を持ち、対象 account/object/key をログで追跡可能にする。ただし secret は出さない。

### OPS43-01: self-destruct workflow

優先度: **P1、高リスク**。

- [ ] admin CLI で local domain の再入力と二重確認を行い、`SELF_DESTRUCT` 用の署名 token を生成・検証する。
- [ ] mode中はschedulerをself-destruct専用にする。全queueのenqueued backlogが10,000超なら新規処理を休止し、Redis memoryはconfigured maxmemoryとtotal system memoryの正の小さい方の50%を基準とする。1 scheduler runで未suspend local accountsを最大50、deletion request付きsuspended accountsを別枠で最大50、合計最大100を処理する。
- [ ] known inbox への actor Delete を最大 1,000 件 batch で送り、retry/restart しても重複副作用を抑える。
- [ ] local origin は suspended 相当とするが即時 DB deletion は行わず、user が archive/exportへアクセスできる期間を残す。
- [ ] 通常UI/APIは原則410、login/session/confirmation/password reset/2FA/OmniAuth callback/account edit/backup/export/login activityは許可する。
- [ ] status/check、worker restart後のresume、完了判定runbookとproduction-sized rehearsalを用意する。開始後のsupported abort/rollbackはないことを明記する。

## P1/P2: media、preview card、object storage

### MEDIA43-01: media validation/processing

- [ ] local description 最大 1,500、remote description 最大 10,000 を入口別に分ける。
- [ ] `audio/opus`、`.jfif`、iOS 18 HEIF、animated PNG emoji、unknown/multiple remote media types を 4.3.23 最終 behavior で処理する。
- [ ] passthrough video でも `moov` atom を先頭へ移す faststart を行い、ffprobe rotation を width/height 判定へ反映する。
- [ ] preview image 上限は libvips 使用時 8 MiB、native/ImageMagick互換経路 2 MiB という上流差を、Paon の実際の processor policyとして明示する。
- [ ] libvips/FFmpeg の container version と codec matrix を更新し、Go-native fallback でも accepted formats と output contract を守る。
- [ ] upstream `MASTODON_USE_LIBVIPS`をPaonのcompile/runtime選択へ互換aliasとして受けるか、libvips buildでは常時利用する差分として文書化するかを明示し、silent no-op envにしない。
- [ ] 4枚超 media、portrait GIF profile、video rotation、HEIF/APNG、malformed image、processing failure fallbackを `docs/paon-go-media-validation.md` の matrixへ追加する。

### CARD43-01: preview card fetch/attribution/cache

- [ ] URL は http(s) のみ、最大 2,692、response body 最大 1 MiB、redirect/SSRF policy 共通、server default locale の `Accept-Language` を送る。
- [ ] canonical URL が `undefined`/invalid の場合は無視し、status に現れた original URL を association へ保存する。
- [ ] `fediverse:creator` を抽出し、authorが許可した attribution domain、trendable/discovery条件を満たす場合だけ `author_account_id` を保存する。
- [ ] Content Warning 展開後の表示、long title、language、oEmbed sanitizer、YouTube referer/startを backend/frontendで合わせる。
- [ ] deletion 前に CDN cache-bust target URL を収集し、cache-bust failure で DB/object deletion自体を止めない。

### STORE43-01: S3-compatible/Swift/Azure behavior

- [ ] `S3_KEY_PREFIX` を upload/read/delete/presign/cache-bust/list の全 keyへ一度だけ適用する。
- [ ] `S3_RETRY_LIMIT` default 0、`S3_BATCH_DELETE_LIMIT` default 1,000、`S3_BATCH_DELETE_RETRY` default 3 total attempts（初回+2 retry）をvalidation付きで追加し、両retry設定を混同しない。
- [ ] transient storage error では media redownload/delete を retryし、permanent auth/not-found と区別する。
- [ ] Swift batch attachment deletion の partial failure、S3 timeout、prefix traversal、duplicate delete を external integration testで確認する。

## P1/P2: runtime configuration と observability

### CONF43-01: Redis Sentinel と namespace

- [ ] `REDIS_*`、`CACHE_REDIS_*`、`SIDEKIQ_REDIS_*`それぞれのURL/USER/PASSWORD/HOST/PORT/DBとSENTINEL_MASTER/SENTINEL_PORT/SENTINELS/SENTINEL_USERNAME/PASSWORDを解釈する。Sentinel port defaultは26379、MASTERと非空SENTINELSを組で要求し、sentinel credentials未指定時は同系統Redis USER/PASSWORDを継承する。
- [ ] cache/worker系configが一式として成立しない場合、個別値をbaseへ混ぜずbase config全体へfallbackする。`SIDEKIQ_REDIS_*`はenv互換名として受けてもGo/Asynqへ意味を翻訳する。
- [ ] Sentinel failover中の web session、stream subscription、Asynq enqueue/dequeue、rate limit/cacheを integration testする。
- [ ] `REDIS_NAMESPACE` は4.3でdeprecated warning対象だが、Paon/Asynqの既存key互換を壊さず段階廃止方針を記録する。
- [ ] `REDIS_DRIVER=ruby` はRuby固有なので対象外。Go Redis clientのdriverを増やさない。

### CONF43-02: worker readiness

上流の `MASTODON_SIDEKIQ_READY_FILENAME` は Sidekiq process 用だが、drop-in deployment compatibility のため同じ env を Paon worker readinessへ翻訳する。

- [ ] basenameだけを許可し、path traversal/absolute pathを拒否する。
- [ ] workerがRedis接続・queue登録・scheduler初期化を終えた後に `tmp/<name>` を作り、graceful shutdownで削除する。
- [ ] `PAON_PROCESS_ROLE=web` は作らず、worker/all の lifecycleとcontainer probeを試験する。

### OBS43-01: OpenTelemetry と dependency security

- [ ] upstreamでStatsDを置換したOpenTelemetryのHTTP、worker、DB、Redis、federation、search spans/metricsをGoへ対応付ける。
- [ ] service name prefix（上流 default `mastodon`、separator `/`）、trace propagation、PII/secret redaction、samplingを設定可能にする。
- [ ] search spanにbackend、offset/limit/result countを持たせる。query本文やtokenを無条件にattributeへ出さない。
- [ ] PaonのStatsD互換を残す場合は独自extensionと明記し、OTelと二重計上しない。
- [ ] Go modules、npm lock、libvips/FFmpeg/container baseを更新し、`govulncheck`、frontend audit、container scan結果をrelease evidenceへ保存する。

### CONF43-03: config/documentation contract

- [ ] 新規 env とdefault/validationをconfig model、`.env.sample`、Docker/Compose、`--check-config`、operations docsへ同時追加する。
- [ ] optional integrationは必要なpaired variablesを全て検証し、未設定時は既存挙動を維持する。
- [ ] `task dev`/Composeの親環境だけでなく、実際のGo web/worker/migrate/admin子processへ値が届くことをsecret-safe presence/length testで証明する。
- [ ] Rails、Ruby、Sidekiq、standalone Node streaming image、port 4000、Makefileは追加しない。
- [ ] Elasticsearch固有 `ES_CA_FILE` は対象外。Meilisearchにcustom CA要件がある場合はPaon固有の明示設計とする。

## リリース別追跡表

この表は 4.3.0 の大規模変更に加え、4.3.1〜4.3.23 の patch で最終挙動が変わった点を取りこぼさないための追跡表である。依存更新だけの release も security scan evidence を要求する。

### 4.3.0（2024-10-08）

| 分類 | upstream の主要変更 | 対応 ID |
| --- | --- | --- |
| Security | remote redirect confirmation、Ruby ReDoS、CSP `form-action` 強化 | SEC43-06/08、WEB43-01 |
| Notifications | server-side grouping、policy/filter/request、severance、moderation warning、unread API | DB43、NOT43、FE43-02/03 |
| Federation/link | status likes/shares collection、fediverse creator/`attributionDomains`、link timeline、preview original URL | AP43、CARD43、FE43-06 |
| REST/OAuth | batch accounts/statuses、PKCE、multiple redirect URIs、profile scope、RFC 8414、revoke CORS、invite JSON、instance API version/icon/VAPID/rule hint | API43、AUTH43、DISC43 |
| Web UI | Material Symbols、compose/modal/color/CW/filter/media redesign、media reorder、ALT、hover cards、system theme、onboarding、suggestions、React embed | FE43-01〜12 |
| Admin/registration | email-domain approval、hashtag search、report application、admin navigation/audit、one-week unconfirmed cleanup | REG43、FE43-10 |
| Search/recommendation | friends-of-friends/similar profiles/global sources、persistent dismiss、new account search ranking | REC43、SRCH43 |
| Runtime/storage | Redis Sentinel、OpenTelemetry、S3 prefix/retry/batch delete、libvips、worker readiness、self-destruct | CONF43、OBS43、STORE43、MEDIA43、OPS43 |
| Removed/changed | E2EE/crypto surface、StatsD、home marker updates、unfollow setting。Announcements now full Status entities | DB43-01、AP43-01、FE43-08、API43-02 |

### 4.3.1〜4.3.23

| release | 最終状態へ反映すべき更新 | 対応/確認 |
| --- | --- | --- |
| 4.3.1 | follow notification grouping、6時間 mute、author attribution説明、regional translation、remote embed menu削除。stream cache header、RTL、push locale、notification marker、recommendation suppression等を修正 | NOT43-01、FE43-02/06/07/11、STREAM43-01 |
| 4.3.2 | feeds vacuum、自分自身follow error、`client_secret_expires_at`、CW/filter redesign。mention解決、inactive feed、notification重複、future trend、GIF、search min/max、list limit、Swift delete、link timeline block等を修正 | AUTH43-01、JOB43-02、NOT43、FE43-04/05、STORE43 |
| 4.3.3 | account URI検証 security。notification policy migration、group pagination、silent mention、WebAuthn付きunconfirmed cleanup、empty card authorsを修正 | SEC43-04、DB43-04、NOT43、REG43、CARD43 |
| 4.3.4 | `<embed>` sanitizer、signup confirmation rate limit、domain-block disclosure security。CW展開card、moderator通知非filter、v2 ungrouped notification、redirect再署名、poll edit、featured tags等を修正 | SEC43-05/06、NOT43、AP43、FE43-03/05 |
| 4.3.5 | hashtag suggestion casing、iOS 18 HEIF、unknown-language public stream filter、CW下preview card、narrow admin profileを修正 | REC43/SRCH43、MEDIA43、STREAM43、FE43-05/10 |
| 4.3.6 | security dependency更新、`REDIS_NAMESPACE` 使用時の Stoplight error修正 | CONF43-01、OBS43-01 |
| 4.3.7 | profile update debounce、partial follower collection pagination、suspension reach、archive URL TTL 1時間。APNG、filters in detail/favourites/bookmarks、malformed HTML、cache-buster URLを修正 | JOB43-01、AP43-03/04、FE43-05、CARD43 |
| 4.3.8 | account/profile/media URL scheme security。Redis namespace warning、interaction-policy context、deleted account delivery retry停止、limited federation unauthenticated API redirect修正 | SEC43-04、AP43-02/05、CONF43-01 |
| 4.3.9 | passthrough video faststart。emoji cache、deleted reply moderation、search operators、ALT button、remote mixed media、trend/filter、inline featured、rotation、share limit、modal autofocus等を修正 | MEDIA43、AP43、SRCH43、FE43-04/05/10 |
| 4.3.10 | security dependency更新 | OBS43-01 |
| 4.3.11 | confirmation email rate-limit security。Create critical path query-cache race、null account URL、already-requested recommendationを修正 | SEC43-06、AP43-02、API43-02、REC43-01 |
| 4.3.12 | remote editのtextなし+new media、poll edit/redraft、Redis構成別self-destruct schedulerを修正 | AP43-02/04、FE43-04、OPS43-01 |
| 4.3.13 | out-of-order Create/Update と implicit updateを修正 | AP43-02 |
| 4.3.14 | streaming scope/suspension、admin CLI reset-password token失効 security。malformed external object redirectを修正 | SEC43-05、STREAM43-01、CLI43-01、WEB43-01 |
| 4.3.15 | storage-schema CLI、過去の未知status Updateを新着扱いする問題を修正 | CLI43-01、AP43-02 |
| 4.3.16 | private status存在漏えい security。YouTube referer/start、大規模S3 batch timeoutを修正 | SEC43-05、WEB43-02、CARD43、STORE43 |
| 4.3.17 | SSRF bypass、severed relationship ownership security。domain-blocked user mentionを修正 | SEC43-01、NOT43-03、AP43-03 |
| 4.3.18 | remote property/poll DoS、remote suspension bypass、list/filter length、push settings IDOR security。inbox 1 MiB、duplicate signature、`Vary` parse、deleted reblog fan-out等の最終修正 | SEC43-03〜05、AP43、NOT43 |
| 4.3.19 | signed featured/pinned collection cache security。move relationship cache、invalid updated tag、recycled connectionを修正 | SEC43-05、AP43-03/05 |
| 4.3.20 | emoji purge `--suspended-only`、updated objectのduplicate hashtagsを修正 | CLI43-01、AP43-02 |
| 4.3.21 | quote authorization security（公式4.3はquoteなしだがPaonは対象）、legacy path open redirect。known private GoToSocial URL search、remote media description 10,000、poll notification、hover cardを修正 | SEC43-06/07、SRCH43-01、MEDIA43、AP43-02、FE43-09 |
| 4.3.22 | email validation security。only-key actor refreshで未知actorを作らない修正、dependency更新 | SEC43-04/06、OBS43-01 |
| 4.3.23 | IPv6/NAT64/mapped address SSRF、JSON-LD `@graph`/`@included`/`@reverse` signature bypass、dependency更新 | SEC43-01/02、OBS43-01 |

## 実装順と依存関係

### Phase 0: fixture と security hotfix

1. Mastodon 4.3.23 Rails reference、固定clock、共通DB fixture、route/serializer/AP golden filesを用意する。
2. SEC43-01〜07の攻撃fixtureを先に追加し、SSRF、JSON-LD、body limit、duplicate signature、email、redirect、IDOR、streamingを修正する。
3. advertised versionは変更しない。

### Phase 1: DB expand/backfill と backend entity

1. DB43-01〜04のmigration runner、expand、OTP、backfillを実装する。
2. new models/enumsとGo serializer fixtureを追加する。
3. 旧binaryとのmixed-version test後、target snapshot/schema guardを準備する。contract/dropはまだ行わない。

### Phase 2: Notifications end-to-end

1. policy判定とrequest/permission workers。
2. v1/v2 routes、grouping、unread/marker、serializer。
3. SSE/WS/push/mailとFE43-02/03。
4. severance/moderation warningとowner-only export。

### Phase 3: REST/OAuth/federation

1. API43、AUTH43、DISC43、WEB43 routes/entities。
2. AP actor/note collections、attribution、fetch/delivery/order fixes。
3. batch/link/annual/recommendation/search、media/card/storage。
4. Rails 4.3.23 differential suiteを領域ごとにgreenにする。

### Phase 4: Web UI semantic port

1. FE43-01 build/type/state基盤。
2. compose/status/modal/icon/theme/embed。
3. link/creator/suggestion/profile/onboarding/admin。
4. locale/assets/mailをmergeし、Paon固有frontend回帰matrixを通す。

### Phase 5: operational parity と DB contract

1. Redis Sentinel/readiness、OTel、Asynq maintenance/self-destruct、admin CLIを実装・演習する。
2. fleetが新binaryのみであること、backup/restore rehearsal、backfill validationを確認する。
3. destructive post migrationsとschema contractを実行し、fresh/upgrade schemaを比較する。
4. 全release gate通過後にだけMastodon compatibility versionを `4.3.23` へ変更する。

## リリースゲート

### Schema/data gate

- [ ] empty DB、upstream 4.2.19 reference populated DB、current Paon HEAD/実運用 populated DB（現在advertised 4.2.29）が同じ`20241007071624` final schemaへ到達する。
- [ ] column type/default/nullability、index expression/predicate/order、FK/on-delete、function/view/seedを`pg_catalog`で上流targetと比較する。
- [ ] OTPのRails/Paon双方向ciphertextとTOTP code、backfill中断再開、contract前後backup/restoreを証明する。
- [ ] final schemaに削除対象E2EE tables/columns、obsolete role/policy/request fieldsがなく、legacy OTP fallback columnsは存在する。

### API/protocol gate

- [ ] 既存`task routes:go`の出力を4.3.23 reference manifestと比較する監査command/taskを実装し、明示した対象外以外の欠落を報告しない。存在しない`task routes:audit`をrelease手順だけに書かない。
- [ ] `task test:differential` でstatus、JSON/HTML body、content type、Link/Cache-Control/Vary/CORS/locationを比較する。
- [ ] local/remote、anonymous/user/admin、scope、visibility、suspension、block、locale、paginationのmatrixを通す。
- [ ] Mastodon 4.3.23とPaonのActivityPub actor/note/collection/signature相互試験を通す。

### Security gate

- [ ] 4.3.0〜4.3.23の全advisoryを「攻撃fixture成功」「Go実行経路に非該当」「Paon独自quote対応」のいずれかへevidence付きで分類する。
- [ ] SSRF DNS/redirect/address、JSON-LD graph、oversized inbox/property、duplicate Signature、email、redirect、private status、collection cache、push ownership、stream revokeをnegative testで確認する。
- [ ] `govulncheck`、frontend dependency audit、container scanに未承認のhigh/criticalがない。

### UI/i18n gate

- [ ] grouped/filter/request/new-type notificationと全主要4.3 user flowをdesktop/mobile/touch/keyboardでE2Eする。
- [ ] Paon quote/emotional/custom spoiler/lightbox/theme/Asynq adminの回帰がない。
- [ ] 全locale bundle、fallback、placeholder/plural、RTL、system theme、CSP asset、server-rendered pageが成立する。
- [ ] visual snapshotだけでなくscreen reader label、focus order、reduced motionを確認する。

### Runtime/operations gate

```bash
task build
task test:rtk
task test:integration
task test:external
yarn build:production
yarn test
yarn lint:md
docker compose config
docker build .
```

- [ ] `PAON_PROCESS_ROLE=web` と `worker` を独立起動し、webがHTTP/REST/AP/SSE/WSをport 3000で提供する。
- [ ] `yarn i18n:extract`後に`en.json`がclean diffであること、全entrypoint/lazy chunkがmanifestに存在すること、Go-rendered各entrypointがproduction assetをloadできることを確認する。
- [ ] Redis failover、worker restart、notification merge、storage partial failure、Meilisearch unavailable、self-destruct rehearsalを実施する。
- [ ] deploy前check、schema migration、rolling window、contract、rollback/restore、post-deploy smokeをrunbook化する。

### Version gate

- [ ] `internal/paon/config/version.go` のdefault、RSS generator、User-Agent、instance v1/v2、software update check、tests/fixturesを一括で `4.3.23` へ更新する。
- [ ] version string変更だけの状態を4.3.23互換としてreleaseしない。

## upstream トレーサビリティ

実装時は少なくとも次の上流 path/commit を各PR説明へ紐付ける。

| area | upstream evidence |
| --- | --- |
| OAuth metadata / PKCE / profile / redirect URIs | `116f01ec7d`、`693d9b03ed`、`e02d23b549`、`2da2a1dae9` |
| E2EE removal | `5405bdd344` と `db/post_migrate/20240720140205_*`、`20240916190140_*` |
| link/domain preview/annual/invite | `a2505e8611`、`3426ea2912`、`5b1eb09d54`、`07a4059901` |
| AP likes/shares | `aaab6b7adc`、`app/controllers/activitypub/{likes,shares}_controller.rb` |
| remote redirect interstitial | `b19ae521b7`、`app/controllers/redirect/*` |
| grouped notifications | `974335e414` 以降、`app/controllers/api/v2/notifications*`、`app/models/notification*.rb` |
| notification policy/request | `50b17f7e10` 以降、`app/controllers/api/v1/notifications/*` |
| NodeInfo / Account fallback | `10b879bd5e`、`6202cf6b65` |
| frontend entrypoints/notifications/compose | `3f6887557b`、`f587ff643f`、`6936e5aa69`、`11a12e56b3` |
| Material/CW/embed/theme | `134de736dc`、`c634da32cf`、`3d46f47817`、`02ea161506` |
| final security | v4.3.17〜v4.3.23 tagged `CHANGELOG.md` と各GHSA、特に `private_address_check.rb`、`jsonld_helper.rb`、`linked_data_signature.rb` |

## 明示的な非目標

- Ruby、Rails、Sidekiq、Bundlerを再導入しない。
- standalone Node streaming process/imageとport 4000依存を導入しない。
- GORM AutoMigrateを有効にしない。
- Elasticsearchを再導入せず、search semanticsをMeilisearch/SQLで実装する。
- upstreamのWebpack構成を戻さず、既存Rspack buildを更新する。
- Makefileを追加しない。
- revert済みmigrationや、4.3.23 final treeに存在しない中間実装をtargetへ入れない。
