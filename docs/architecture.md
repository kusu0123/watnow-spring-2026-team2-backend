# Architecture

このバックエンドは、少人数のリアルタイム鬼ごっこ MVP を前提にしたシンプルな構成です。アーキテクチャを崩さないため、責務を増やす場所を明確にします。

## 全体像

```text
React Native / Expo frontend
        |
        | REST API
        | WebSocket
        v
Gin server (main.go)
        |
        +-- models: SQLite に保存する構造
        |
        +-- ws: WebSocket 接続、部屋状態、ゲームループ
        |
        v
SQLite (onigokko.db)
```

## パッケージ責務

### `main.go`

HTTP サーバーの入口です。

- SQLite を開く
- `Room` / `Player` を AutoMigrate する
- REST endpoint を定義する
- WebSocket endpoint を `ws.ServeWs` に渡す

ここにゲーム進行ロジックや WebSocket event の詳細を増やさないでください。

### `models`

DB に保存するデータ構造だけを持ちます。

- `Room`
  - 部屋 ID
  - ゲーム状態
  - 制限時間、鬼の人数、同期間隔、猶予時間
- `Player`
  - room ごとの user
  - 名前、役割、捕獲状態、位置、色

WebSocket 接続やゲームループの状態は `models` に入れません。

### `ws`

リアルタイム通信とゲーム中のメモリ状態を扱います。

- WebSocket 接続
- `join`, `start`, `move`, `capture_request`, `capture_response`
- 部屋ごとの `RoomState`
- 接続中 client の一覧
- `sync` や `result` の配信
- ゲームループ

REST API の routing は `main.go` に残し、`ws` には WebSocket とゲーム進行の責務を寄せます。

## DB 状態とメモリ状態

このプロジェクトでは、DB とメモリが別の役割を持ちます。

### SQLite に保存するもの

- 部屋の存在
- 部屋の status
- 部屋設定
  - `time_limit`
  - `oni_count`
  - `area_size`
  - `sync_interval`
  - `grace_period`
- プレイヤー情報
  - `user_id`
  - `name`
  - `role`
  - `is_caught`
  - `lat`
  - `lng`
  - `color`

再接続で復元したい状態は DB に保存します。切断時に `players` は削除しません。

### `GameHub` / `RoomState` に保存するもの

- 現在接続中の WebSocket client
- 部屋ごとのゲーム進行中フラグ
- 猶予終了時刻
- 本編開始時刻
- ゲームループが動いているかどうか
- 接続中 client の最新状態

`GameHub` はプロセス内メモリです。サーバー再起動や複数インスタンス構成には強くありません。MVP では許容し、6月末運用前に必要なら見直します。

## ゲーム状態

`Room.Status` は現在 int で管理されています。

| 値 | 意味 |
|---|---|
| `0` | 待機中 |
| `1` | 進行中 |
| `2` | 終了 |

開始時:

- `start` action を受け取る
- `RoomState.Status` を `1` にする
- DB の `rooms.status` も `1` にする
- 接続中 player に role を割り当てる
- 各 client に `start` event を送る
- 猶予時間後に `game_active` event を送る

終了時:

- 制限時間が終わる
- または逃走者が全員捕まる
- `RoomState.Status` を `2` にする
- DB の `rooms.status` も `2` にする
- `result` event を配信する

## 現時点で対象外のもの

以下は今回の MVP docs では設計対象外です。

- 認証
- 他人が room に入ってくることへの対策
- room ID 推測対策
- 複数サーバーでの WebSocket 共有
- 本格的な監視やログ基盤

対象外のものを場当たり的に実装しないでください。必要になったら、まず設計を docs に追加してから実装します。
