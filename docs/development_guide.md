# 開発手順

この資料は、バックエンドをローカルで動かすための基本手順と、開発環境ごとの差分をまとめたものです。

## 前提

- Go がインストールされていること
- リポジトリのルートでコマンドを実行すること
- デフォルトの DB は SQLite ファイル `onigokko.db`
- デフォルトのポートは `8080`

## 起動

```sh
go run .
```

起動後、HTTP API は以下で待ち受けます。

```text
http://localhost:8080
```

WebSocket は以下です。

```text
ws://localhost:8080/ws/rooms/:room_id
```

## 基本的な動作確認

ルーム作成:

```sh
curl -X POST http://localhost:8080/rooms
```

設定更新:

```sh
curl -X PUT http://localhost:8080/rooms/1234 \
  -H "Content-Type: application/json" \
  -d '{"time_limit":900,"oni_count":1,"area_size":"school-yard","sync_interval":3,"grace_period":30}'
```

WebSocket の動作確認は、フロントエンドまたは WebSocket クライアントから `/ws/rooms/:room_id` に接続して行います。

## テスト

```sh
go test ./...
```

Mac の開発環境では通常このコマンドで確認します。

Windows で SQLite 関連のテストが以下のように失敗する場合があります。

```text
Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work.
```

この場合は、`github.com/mattn/go-sqlite3` が CGO を必要とすることが原因です。Windows で同じテストを実行する場合は、CGO を有効にし、`gcc` などの C コンパイラが使える状態にしてください。

```powershell
$env:CGO_ENABLED = "1"
go test ./...
```

`gcc` が見つからない場合は、C コンパイラの導入が必要です。

## DB

アプリ起動時に `onigokko.db` が作成され、GORM の `AutoMigrate` により `rooms` と `players` が用意されます。

SQLite は同時書き込みに強くないため、現在の実装では最大接続数を 1 にしています。ローカル確認では問題ありませんが、同時接続が増える運用を想定する場合は DB の選定や接続設計を見直してください。

## ブランチ運用

作業前に `main` が最新であることを確認します。

```sh
git fetch origin
git status --short --branch
```

`main` で直接作業せず、作業内容に応じたブランチを作成してから変更します。

```sh
git switch -c <branch-name>
```

docs だけを編集する場合も、実装コードと同じようにブランチを分けて扱います。
