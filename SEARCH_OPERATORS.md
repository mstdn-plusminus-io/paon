# Meilisearch Search Operators

この文書では、Meilisearch版Mastodonで使用できる検索オプションについて説明します。

## 基本的な検索

単純なテキスト検索:
```
hello world
```

フレーズ検索（完全一致）:
```
"hello world"
```

除外検索:
```
hello -world
```

## 高度な検索オプション

### has: オプション

投稿に特定のメディアや要素が含まれているかを検索:

- `has:media` - メディア（画像・動画）付き投稿
- `has:image` - 画像付き投稿
- `has:video` - 動画付き投稿
- `has:poll` - アンケート付き投稿
- `has:link` - リンク付き投稿
- `has:embed` - 埋め込みコンテンツ付き投稿

例:
```
猫 has:image
```

### is: オプション

投稿の種類を指定:

- `is:reply` - 返信投稿
- `is:sensitive` - センシティブな投稿

例:
```
質問 -is:reply
```

### language: オプション

特定の言語の投稿を検索:

- `language:ja` - 日本語
- `language:en` - 英語
- `language:fr` - フランス語

例:
```
hello language:en
```

### from: オプション

特定のユーザーからの投稿を検索:

- `from:username` - 特定のローカルユーザー
- `from:username@domain.com` - 特定のリモートユーザー
- `from:me` - 自分の投稿

例:
```
from:me Mastodon
```

### 日付オプション

投稿日時で絞り込み:

- `before:2024-01-01` - 指定日より前
- `after:2024-01-01` - 指定日以降
- `during:2024-01-01` - 指定日の投稿のみ

例:
```
年末 after:2023-12-20 before:2024-01-10
```

### in: オプション

検索範囲を指定:

- `in:library` - 自分の投稿、メンション、お気に入り
- `in:public` - 公開投稿のみ
- `in:all` - すべて（デフォルト）

例:
```
in:library 備忘録
```

## 組み合わせ例

複数のオプションを組み合わせて使用できます:

```
from:me has:image language:ja after:2024-01-01
```

```
"重要なお知らせ" -is:reply has:link in:public
```

## プライバシーに関する注意

- デフォルトでは、公開投稿と未収載投稿のみが検索対象です
- 他のユーザーの非公開投稿は検索できません
- 自分の投稿はすべて検索可能です
- フォロワー限定投稿は、その投稿者をフォローしている場合のみ検索可能です

## インデックスの再構築

新しい検索属性を有効にするには、インデックスの再構築が必要です:

```bash
bundle exec rake meilisearch:reindex_all
```

または、投稿のみを再インデックス:

```bash
bundle exec rake meilisearch:reindex_statuses
```