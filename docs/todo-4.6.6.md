# Mastodon 4.6.6 互換化 TODO

## 目的と固定契約

PaonをMastodon v4.5.15互換からv4.6.6互換へ更新する。REST、ActivityPub、
streaming、worker、管理・設定画面、React UI、PostgreSQL catalogを対象とし、
MastodonからPaon、PaonからMastodonの双方へ同じDBを持ち替えられることを必須とする。

ただし引用許可だけはPaon固有契約とする。Mastodon互換のendpointとpayload fieldは
残すが、`public`、`followers`、`nobody`の入力値はすべて`public`へ正規化し、
otherwise-visibleな投稿は常に`automatic`で引用可能とする。ローカル投稿へのremote
`QuoteRequest`も保存済みpolicyに関係なく自動承認する。非公開投稿の可視性、block、
domain block、suspensionは引用許可とは別の安全境界として維持する。remote投稿をPaonが
引用するときはremote側がauthorityなので、FEP-044fのrequest/accept/reject交換を維持する。

Rails、Sidekiq、standalone Node streaming、GORM AutoMigrateは導入しない。上流の外部契約を
Go 1.25、Echo v5、Asynq、既存React/Rspack UIへ移植する。

## 上流identity

- tag: `v4.6.6`
- commit: `d74bbd9a3c64740433e016dfc936e83d710858f6`
- final schema marker: `20260611150940`
- `db/schema.rb` SHA-1: `c7b4869d1d9d5614d86e2723464402c4976f6075`
- `db/schema.rb` SHA-256: `e7915a2fadcb1a5f4cb1c5d5fe4aeccb97ecdfaed37759d6b66873efffbde54a`
- v4.5.15からのmarker: 34、total: 588
- Mastodon API compatibility level: 11
- fresh strict catalog: 119 relations、1,011 visible columns、333 indexes、
  269 constraints、154 foreign keys、4 views、1 function、110 sequences

## 必須機能要件

### DB46: PostgreSQL完全互換

1. **DB46-01 fresh catalog**
   - Rails 8.1の`schema.rb` loadが生成する物理列順、type/default/nullability、
     fast-default metadata、index/constraint/FK名、function、view、sequence ownership、
     588 markers、Active Record metadataを一致させる。
   - v4.6 freshは既存tableもalphabeticalな物理列順になるため、v4.5 staged結果と分ける。
2. **DB46-02 staged upgrade**
   - v4.5.15、v4.4.22、v4.3.23、v4.2.19の各lineageを4.6.6へ更新する。
   - 旧4.3/4.4/4.5 boundary goldenを残し、4.6 final goldenを5経路追加する。
3. **DB46-03 migration semantics**
   - 34 migrationをexpand/backfill/validate/contractへ割り当て、各markerは対応処理成功後だけ記録する。
   - collections、collection_items、collection_reports、email_subscriptions、tagged_objects、
     keypairsと全追加列/index/FK/data migrationを上流どおり実行する。
4. **DB46-04 strict proof**
   - 5経路をPostgreSQL 14/15で比較し、fresh/staged固有差を経路別goldenへ固定する。
   - official Mastodon→Paon、Paon→official Mastodonの双方でmigration 0件と実行後catalog不変を確認する。

### COLL46: Collections / FEP-7aa9

5. **COLL46-01 REST API**
   - account collections/in-collections、collection CRUD、item add/delete/revokeと
     `v1_alpha` aliasesを実装する。
   - `read:collections` / `write:collections`、offset pagination、Link header、
     wrapped JSON、limit 25 items、role collection limit、422/403/404を一致させる。
6. **COLL46-02 ActivityPub**
   - FeaturedCollection、FeaturedItem、FeatureRequest、FeatureAuthorization、
     actor `featuredCollections` / `canFeature`、Add/Remove/Update/Accept/Reject/Delete、
     discovery/sync/cleanupを実装する。
7. **COLL46-03 surfaces**
   - REST Status `tagged_collections`、search `collections`、collection notifications、
     streaming/push、report/admin、既存デザイン内の作成・編集・閲覧UIを実装する。

### PROFILE46 / EMAIL46 / AUTH46

8. **PROFILE46-01 profile API**
   - `GET/PATCH /api/v1/profile`とavatar/header deleteを実装する。
   - display name、note、fields、avatar/headerとalt text、discoverability、indexing、
     collection/media表示、attribution domainsを同じshapeで返す。
   - `update_credentials`も`avatar_description` / `header_description`を受理する。
9. **EMAIL46-01 email subscriptions**
   - role/account gate、anonymous subscribe、confirmation/unsubscribe、MX/重複validation、
     distribution worker、purge、mail、admin/profile UIを実装する。
10. **AUTH46-01 role security**
    - role別（Everybodyを含む）2FA要求とlogin/acceptance flow、invite bypass permission分離、
      role `collection_limit`を実装する。

### API46: REST/entity compatibility

11. **API46-01 entity fields**
    - Instance API version 11とaccount/media/profile limits、`wrapstodon`、
      PreviewCard `missing_attribution`、Relationship `muting_expires_at`、
      CustomEmoji `featured`、Report `collection_ids`、Role `collection_limit`、
      AnnualReport `account_id` / `share_url`を実装する。
12. **API46-02 request changes**
    - accounts statuses `exclude_direct`、admin reports `unresolved`、report `collection_ids`、
      media+poll同時投稿、notification `supported_types`、bot filterを実装する。
13. **API46-03 new endpoints**
    - annual report `state` / `generate`、donation campaigns、email subscription、
      profile、collectionsの全route/method/auth/pagination/error contractを実装する。
14. **API46-04 quote divergence**
    - Mastodon互換field/route/FEP-044f objectは保持する。
    - local statusは常時`automatic:["public"]`, `manual:[]`, `current_user:"automatic"`。
    - policy update/revokeは互換responseを返すが引用を不許可化しない。

### AP46 / RUNTIME46

15. **AP46-01 remote identity**
    - remote accountの複数keypair、unknown secondary key fetch、rotation/error handlingを実装する。
16. **AP46-02 federation additions**
    - FEP-2c59 WebFinger backlink、FEP-3b86 Activity Intents、profile image alt text、
      attachment duration、FeaturedCollection tags、JSON-LD collection変更を実装する。
17. **RUNTIME46-01 observable fixes**
    - media description 10,000文字、display name 40文字、poll+media、FFmpeg `fps_mode`、
      Meilisearch circuit breaker、streaming collection events、4.6.0〜4.6.6の
      applicable security/federation/media/search fixesを実装する。
18. **RUNTIME46-02 identity**
    - Paon `7.4.0`、Mastodon compatibility `4.6.6`、API level 11をadvertiseする。

### UI46 / OPS46

19. **UI46-01 retained design**
    - 上流4.6のprofile/collection/email/annual-report機能を既存Paonデザインへ追加する。
    - quote policy selector/revoke/cannot-quote表示を除去し、quote actionは可視性条件内で常時有効にする。
20. **OPS46-01 operations**
    - custom-filter import/export、overview landing、theme setting migration、role/admin controls、
      collection/email cleanup workerと必要なCLI behaviorを実装する。

## 非機能要件

21. **互換性**: 既存4.2〜4.5のupgrade、API、ActivityPub、UI契約を退行させない。
22. **安全性**: unknown/future/partial markerをfail closedし、contractは明示acknowledgement後だけ実行する。
23. **局所化**: 新しいfrontend/server-rendered文言はlocale dictionary経由にする。
24. **process role**: web/worker/allを独立実行でき、streamingはGo port 3000内に維持する。

## 実装順序

1. 上流migration inventoryと5経路goldenを固定する。
2. model/schema guardとstaged migrationを実装する。
3. Collections/Profile/notification/entity REST contractを実装する。
4. FEP-7aa9、multiple keys、WebFinger/Activity Intentsを実装する。
5. email/2FA/annual report/search/media/worker behaviorを実装する。
6. 既存UIへ機能を接続し、quote permission UIを除去する。
7. route/payload differential、PG14/15 catalog、双方向migration、全build/test gateを実行する。

## 完了gate

- [x] official v4.6.6 route manifestにmissing routeがない。
- [x] source/payload監査とfocused REST/ActivityPub testsでquote divergence以外のundeclared差分がない。seeded live differentialはrelease-environment gateとしてevidenceに分離する。
- [x] PostgreSQL 14/15の5 final routeと旧boundary goldensがPASSする。
- [x] Mastodon→Paon / Paon→Mastodonの双方向no-opが全10経路でPASSする。
- [x] `task test:rtk`、`task build`、`go vet ./...`、module checks、frontend tests/build、
      integration/external tests、Compose validation、`git diff --check`がPASSする。
- [x] production-volume migration、live remote federation、human browser/accessibility、
      deployed topologyはlocal automated proofと区別してrelease evidenceへ記録する。
