# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## プロジェクト概要

これは、Mastodonのフォークリポジトリです。以下の追加・変更機能があります：

- 投稿文字数制限を5000文字に増加
- SlackライクなUI
- GitHub Flavored Markdownのサポート（実験的）
- Cloudflare Turnstileによるサインアップ保護
- リモートメディアキャッシュの有効/無効設定
- **検索エンジン**: ElasticsearchからMeilisearchに置き換え（軽量・高速）

## 開発環境のセットアップ

### 前提条件
- Ruby 3.0.x
- Node.js 16.x
- Yarn 1.22.x
- PostgreSQL
- Redis
- Meilisearch (Docker経由で起動)

### 初期セットアップ
```bash
# 依存関係のインストール
yarn bootstrap

# Docker環境の起動（データベース、Redis、Meilisearch等）
yarn docker:dev up -d

# 環境設定ファイルの準備
cp .env.sample .env

# .envファイルでMeilisearchを有効化
# MEILI_ENABLED=true
# MEILI_HOST=http://localhost:7700
# MEILI_MASTER_KEY=aSampleMasterKey
# MEILI_PREFIX=myinstance  # 複数インスタンスで共有する場合に設定（オプション）

# データベースのマイグレーション
rails db:migrate

# DynamoDBテーブルの作成（必要な場合）
yarn dynamo:create

# Meilisearchインデックスの作成（検索機能を使用する場合）
# Railsコンソールで以下を実行
# Account.reindex
# Status.reindex
# Tag.reindex
# Instance.reindex
```

## トラブルシューティング

### charlock_holmes gemのビルドエラー

macOSでcharlock_holmes gemのインストールに失敗する場合：

1. **症状**: C++17互換性エラーでビルドが失敗
   ```
   error: no template named 'is_same_v' in namespace 'std'
   error: 'auto' not allowed in template parameter until C++17
   ```

2. **原因**: icu4c@77がC++17を要求するが、charlock_holmes 0.7.7はC++14までしかサポートしない

3. **解決方法**:
   ```bash
   # icu4c@77をアンインストール
   brew uninstall --ignore-dependencies icu4c@77
   
   # icu4c@75をインストールしてリンク
   brew install icu4c@75
   brew link --force icu4c@75
   
   # Gemfileを編集してcharlock_holmes 0.7.9+を使用
   # gem 'charlock_holmes', '~> 0.7.9'
   
   # 環境変数を設定してインストール
   export PKG_CONFIG_PATH="/opt/homebrew/opt/icu4c@75/lib/pkgconfig:${PKG_CONFIG_PATH}"
   export LDFLAGS="-L/opt/homebrew/opt/icu4c@75/lib ${LDFLAGS}"
   export CPPFLAGS="-I/opt/homebrew/opt/icu4c@75/include ${CPPFLAGS}"
   bundle install
   ```

## よく使うコマンド

### 開発サーバーの起動
```bash
# Foremanを使用してすべてのプロセスを起動
yarn watch

# または個別に起動
bundle exec rails server              # Railsサーバー
bundle exec sidekiq -c 10             # バックグラウンドジョブ
yarn start                            # ストリーミングサーバー
bin/webpack-dev-server                # Webpack開発サーバー
```

### ビルド
```bash
# 開発環境用ビルド
yarn build:development

# 本番環境用ビルド  
yarn build:production
```

### テスト実行
```bash
# すべてのテスト（リント、型チェック、Jest）
yarn test

# RSpecテスト
bundle exec rspec

# 特定のRSpecテストファイルを実行
bundle exec rspec spec/path/to/test_spec.rb

# システムスペック（ブラウザテスト）を実行
RUN_SYSTEM_SPECS=true bundle exec rspec spec/system

# 検索関連のスペックを実行
RUN_SEARCH_SPECS=true bundle exec rspec spec/search
```

### リント・フォーマット
```bash
# JavaScriptのリント
yarn lint:js

# JavaScriptの自動修正
yarn fix:js

# Rubyのリント
bundle exec rubocop

# Rubyの自動修正
bundle exec rubocop -a

# すべてのファイルをリント
yarn lint

# すべてのファイルを自動修正
yarn fix
```

### 型チェック
```bash
yarn typecheck
```

## Meilisearch設定

### 基本設定

Meilisearchは軽量で高速な検索エンジンで、Elasticsearchの代替として使用しています。

環境変数：
- `MEILI_ENABLED`: Meilisearchを有効化（true/false）
- `MEILI_HOST`: MeilisearchサーバーのURL（デフォルト: http://localhost:7700）
- `MEILI_MASTER_KEY`: Meilisearchのマスターキー
- `MEILI_PREFIX`: インデックス名のプレフィックス（複数インスタンス共有時に使用）

### 複数インスタンスでの共有

`MEILI_PREFIX`を使用することで、複数のMastodonインスタンスで単一のMeilisearchサーバーを共有できます。

例：
```bash
# インスタンス1
MEILI_PREFIX=instance1

# インスタンス2
MEILI_PREFIX=instance2
```

この設定により、各インスタンスは独立したインデックス名を持ちます：
- `instance1_accounts`, `instance1_statuses`, `instance1_tags`, `instance1_instances`
- `instance2_accounts`, `instance2_statuses`, `instance2_tags`, `instance2_instances`

`MEILI_PREFIX`が設定されていない場合、`REDIS_NAMESPACE`の値がフォールバックとして使用されます。

### インデックスの再作成

検索機能が正常に動作しない場合、インデックスを再作成してください：

```bash
# Railsコンソールで実行
rails console

# 全モデルのインデックスを再作成
Account.reindex
Status.reindex
Tag.reindex
Instance.reindex
```

## アーキテクチャの概要

### バックエンド（Ruby on Rails）

主要なディレクトリ構造：
- `app/controllers/` - Webリクエストを処理するコントローラー
- `app/models/` - データモデル（ActiveRecord）
- `app/services/` - ビジネスロジックを含むサービスクラス
- `app/workers/` - Sidekiqによる非同期ジョブ
- `app/lib/` - 共通ライブラリ・ユーティリティ
- `app/policies/` - 認可ロジック（Pundit）
- `app/serializers/` - API用のシリアライザー
- `app/validators/` - カスタムバリデーター

主要なモデル：
- `Account` - ユーザーアカウント（ローカル・リモート両方）
- `Status` - 投稿（トゥート）
- `User` - ローカルユーザーの認証情報
- `Follow` - フォロー関係
- `Notification` - 通知

### フロントエンド（React/Redux）

- `app/javascript/mastodon/` - Reactアプリケーションのルート
- `app/javascript/mastodon/components/` - 再利用可能なコンポーネント
- `app/javascript/mastodon/features/` - 機能ごとのコンポーネント
- `app/javascript/mastodon/actions/` - Reduxアクション
- `app/javascript/mastodon/reducers/` - Reduxレデューサー
- `app/javascript/mastodon/locales/` - 国際化ファイル

### ストリーミングサーバー（Node.js）

- `streaming/index.js` - WebSocketによるリアルタイム更新を処理

### API

MastodonはRESTful APIとActivityPub（分散型ソーシャルネットワークのプロトコル）を実装しています。

- `/api/v1/` - クライアントAPI
- `/api/v2/` - 新しいバージョンのAPI
- ActivityPubエンドポイント - 他のインスタンスとの通信用

### バックグラウンドジョブ

Sidekiqを使用して以下のような処理を非同期で実行：
- メディアファイルの処理
- 通知の送信
- ActivityPubの配信
- メールの送信

### データベース

PostgreSQLを使用。主要なテーブル：
- `accounts` - アカウント情報
- `statuses` - 投稿
- `users` - ユーザー認証情報
- `follows` - フォロー関係
- `notifications` - 通知

### キャッシュとセッション

Redisを使用：
- セッション管理
- キャッシュ
- Sidekiqのジョブキュー
- ストリーミングサーバーのPub/Sub

## 開発時の注意点

1. マイグレーションを追加する際は`strong_migrations`を使用してパフォーマンスへの影響を確認
2. 新しいAPIエンドポイントは適切なシリアライザーを使用
3. フロントエンドの変更時は国際化（i18n）を考慮
4. ActivityPubの実装を変更する際は他のインスタンスとの互換性を確認
5. このフォークの独自機能（5000文字制限等）を考慮した実装