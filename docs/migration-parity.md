# バージョン別マイグレーションとdump互換性テスト

アップグレードSQLは `internal/paon/migrate/migrations/<version>/` に分割され、
`paon-migrate` バイナリへ埋め込まれます。ファイル名は上流のマイグレーションIDと
フェーズです。OTP、JSON/YAML、Redisなどの処理はGo側に残し、対応するSQLファイルに
ハンドラー名を記載しています。SQLファイルの直接実行はサポートしません。

## 一括適用

旧バージョンのweb/workerを停止したうえで実行します。

```sh
task db:migrate -- --all --acknowledge-contract
```

各 `feature/4.3.23`、`feature/4.4.22`、`feature/4.5.15`、`feature/4.6.6` は
それぞれのバージョンまで適用します。最新ブランチでは到達バージョンを選べます。

```sh
task db:migrate -- --all --target-version 4.4.22 --acknowledge-contract
```

`--all` はexpand、backfill、validate、contractを順次適用します。
既存のフェーズごとのトランザクション、advisory lock、マイグレーション履歴と
再実行制御を維持しています。全リリースを一つのトランザクションにはしません。
従来の `--phase` も利用でき、オプション省略時は引き続きexpandです。
後のバージョンが適用済みのDBを古いバージョンへ戻す操作は拒否します。
最新ブランチの古い到達点では履歴を検証し、`--check` によるアプリケーション用の
完全なスキーマ検証は各ブランチ自身の対象バージョンに対して実行します。

4.4のタグトレンド移行には従来どおりRedisが必要です。Redisの旧トレンドが存在しない、
または破棄を明示的に許容する場合のみ `MIGRATION_SKIP_TAG_TREND_BACKFILL=true` を
指定します。OTPを持つユーザーの移行には既存の暗号鍵が必要です。

## 通常のCIで固定する内容

`TestStagingDumpLineageAgainstPostgreSQL` は提供されたdumpから抽出したスキーマと
Active Recordの履歴だけを復元します。アプリケーションの実データは含めません。
各上流タグが**元の実データdump**を直接更新した結果から採取したcatalogと比較し、
さらに再実行がno-opであることを検証します。スキーマだけのfixtureを上流で更新した
結果も、実データdumpの結果と一致することを独立に確認しています。

既存の `schema-compatibility` CIジョブ（PostgreSQL 14/15）がこのテストを実行します。
カラムの物理順序、削除済みカラム位置、デフォルト、制約名、インデックス、関数本文、
シーケンス所有権、ビュー、拡張、マイグレーション履歴、Active Record metadataを比較します。
実データのないCIでデータ移行そのものの一致を証明することはできません。

## 元dump全体で再検証する

必要なものはDocker、Go、Python 3.11以降、PostgreSQL 18以降の`pg_restore`、`psql`、
ローカルMastodonリポジトリと `testdata/db-dump/mastodon_stg.dump` です。
dumpはGitに含めず、SHA-256を `testdata/migration-parity/versions.json` に固定しています。

```sh
task test:migration-parity -- --mastodon /path/to/mastodon
```

最新ブランチでは4バージョンを検証します。別worktreeにある各ブランチのバイナリを
検証する場合は、対応する `--worktree` を指定します。

```sh
task test:migration-parity -- --mastodon /path/to/mastodon \
  --worktree 4.3.23=/path/to/paon-4.3.23 \
  --worktree 4.4.22=/path/to/paon-4.4.22 \
  --worktree 4.5.15=/path/to/paon-4.5.15
```

このコマンドは専用PostgreSQL/Redisコンテナを作成し、終了時に削除します。
既存DBを接続先として受け取りません。各バージョンについて同じdumpの独立コピーを
Mastodon本体とPaonで更新します。上流imageはdigestで固定し、実行前にマイグレーション、
`db/schema.rb`、`lib/mastodon/snowflake.rb` をローカルタグのファイルと照合します。

- 全物理catalogに加え、全テーブル・materialized viewの行ハッシュとシーケンス値を比較。
- 元dumpに含まれる`timestamp_id`のsaltを含む関数定義もそのまま比較。
- 独立実行で新規生成される監査時刻だけは、指定された実行時間内の値を限定的に正規化。
  対象カラム・正規化した件数も比較し、既存の時刻は保持。
- Paon再実行、Paon→Mastodon、Mastodon→Paonを個別に実行し、各操作後のDBが
  時刻を含めて変化していないことを検証。

実行結果は `tmp/migration-parity/` に保存します。行の内容は出力せず、ハッシュと件数を
保存します。失敗時のSQL/Railsログは機密データを含み得るため、この出力先もGit対象外です。
dumpやタグが存在しない場合はスキップせず失敗します。

このfixtureには移行対象OTPがなく、Redisのdumpもありません。専用の空Redisを使用する
ため、この検証結果はステージングRedisのトレンド移行やOTP復号の証明には含めません。
稼働中のFediverse相手との通信はこのDB検証では実行しません。
