# ActivityPub の署名検証診断

`activitypub:processing` で `RsaSignature2017` の RSA 検証に失敗すると、エラー末尾に `signature_diagnostics={...}` が自動で付きます。設定の追加は不要です。Asynq の `LastErr`、管理画面の「最終エラー」とタスクの Markdown コピー、ワーカーの `event=asynq_task_failed` ログの `error` フィールドから確認できます。公開鍵の取得失敗や、RSA 検証に到達する前の JSON-LD 正規化エラーは対象外です。

新しく受信したジョブには、ペイロードの `receipt` に受信時の情報も保存します。署名検証失敗時には、この情報を `signature_diagnostics.receipt` に含めます。

導入時は Web とワーカーの両方を更新すると、新規ジョブの受信時情報から検証失敗まで記録できます。ワーカーだけを更新した場合も、既存ジョブが次に該当の検証失敗を起こした際の診断情報は残ります。設定フラグの変更は不要です。

| フィールド | 記録する内容 |
| --- | --- |
| `receipt.body_sha256` | enqueue に渡された受信本文のバイト列の SHA-256。 |
| `receipt.queued_body_sha256` | ジョブを JSON 化した後の `body` のバイト列の SHA-256。`json.RawMessage` の格納時に空白の圧縮や HTML 文字のエスケープが入るため、受信原文とは異なる場合があります。数値リテラルは保持されます。 |
| `original` | ワーカーが受け取った、JSON-LD compaction 前の本文のバイト数・ハッシュと、その本文で再計算できた署名検証情報。HTTP 受信時の原文とは区別します。 |
| `verification` | 実際に失敗した検証に渡した本文のバイト数・ハッシュと、検証に使った正規化後のハッシュ。通常は JSON-LD compaction 後の本文です。 |
| `receipt_matches_worker_body` | `receipt.queued_body_sha256` と `original.body_sha256` の一致。格納時の通常の JSON 変換を除いて、本文がワーカーまで同じバイト列で届いたかを確認できます。 |

`original` と `verification` の `options_sha256` は署名オプション、`document_sha256` は署名を除いた本文を URDNA2015 で正規化した結果の SHA-256 です。`verification_sha256` は、その 2 つのハッシュの 16 進文字列を順に連結して SHA-256 を計算した値で、RSA 検証の比較対象です。本文のバイト列が違っても、正規化後のハッシュは一致する場合があります。

検証に使った公開鍵は、SPKI DER を Base64 化した `public_key_spki_base64` と、その DER の `public_key_sha256` で記録します。署名者の URI、HTTP 署名で確認した actor、公開鍵を持つアカウントの更新日時、署名の作成日時、署名バイト列のハッシュも含まれます。公開鍵スナップショットにはサイズ制限があり、省略時は `key_snapshot_omitted` が付きます。アカウントの更新日時は鍵の変更日時を保証しません。

`recovered_digest_status=sha256_digest_info` の場合だけ、使用した公開鍵による RSA 演算の結果から、厳密な PKCS#1 v1.5 SHA-256 形式に入っていた値を `recovered_sha256` に記録します。この値だけで送信者の署名時本文を復元したり、その本文の正当性を証明したりすることはできません。他の status では比較可能なダイジェストを取り出せていません。

`receipt.runtime` は受信・enqueue 時、`signature_diagnostics.runtime` は失敗したワーカーの実行情報です。Paon の設定上のバージョンとビルド時のバージョン、Go バージョンを含み、ビルド情報から取得できる場合は JSON-LD ライブラリのバージョンと VCS revision / modified も含みます。VCS 情報がないビルドでは該当項目を省略します。`builtin_contexts_sha256` は Paon 内蔵 JSON-LD コンテキスト群の識別用ハッシュです。送信元の署名時本文・実行バージョンは、受信側だけでは記録できません。

調査では次の順に比較します。

1. Asynq のタスク詳細をコピーして、本文と最終エラーを一緒に確保します。`receipt_matches_worker_body` を確認し、受信時とワーカー実行時の runtime を比較します。
2. `original` と `verification` の正規化後のハッシュを比較します。`original.verified=true` なら、受け取った本文では同じ公開鍵による検証が成功しており、compaction 前後の差が調査対象になります。再計算できなかった場合は `original.error` を確認します。
3. `recovered_sha256` があれば、それぞれの `verification_sha256` と比較します。不一致だけでは、本文の変化・正規化の相違・鍵の違いのどれが原因かは確定しません。保存した公開鍵と本文を使い、同じ条件で再検証します。
4. 送信側と比較する場合は、署名生成時の本文・正規化後のハッシュ・実行バージョンを送信側で採取した情報と照合します。現在公開されている本文や鍵だけでは、過去の署名生成時の状態を保証できません。

既存ジョブには `receipt` がありませんが、新しいワーカーで次に該当の検証失敗が起きれば、その時点の `signature_diagnostics` は残ります。過去の受信時ハッシュや runtime は補完しません。Asynq の最終エラーは最新の試行の情報なので、必要な試行結果は再試行前にコピーしてください。

診断のために本文を追加でログ出力したり、秘密鍵を記録したりしません。本文は従来どおり Asynq のペイロードに残ります。追加の HTTP 取得は行わず、原文の再計算には内蔵・キャッシュ済みコンテキストだけを使います。この診断によって認証結果や再試行の扱いは変わりません。
