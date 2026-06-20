# フロントエンド連携仕様

この資料は、フロントエンドからバックエンドへ接続するときの HTTP API と WebSocket メッセージ仕様をまとめたものです。

## 接続先

ローカル起動時のデフォルトは以下です。

- HTTP: `http://localhost:8080`
- WebSocket: `ws://localhost:8080`

WebSocket はルームごとに接続します。

```text
ws://localhost:8080/ws/rooms/:room_id
```

存在しない `room_id` に接続した場合は 404 になります。

## HTTP API

### ヘルスチェック

```http
GET /healthz
```

レスポンス例:

```json
{
  "status": "ok"
}
```

補足:

- Render Free のコールドスタート対策として、UptimeRobot から定期的にこの endpoint を叩きます。
- DB にはアクセスしない軽い確認用 endpoint です。

### ルーム作成

```http
POST /rooms
```

レスポンス例:

```json
{
  "room_id": "1234",
  "status": 0,
  "time_limit": 900,
  "oni_count": 0,
  "area_size": "",
  "sync_interval": 0,
  "grace_period": 0,
  "created_at": "2026-01-01T00:00:00Z"
}
```

補足:

- `room_id` は 4 桁の数字です。
- 作成直後の `status` は `0` です。
- `time_limit` の初期値は 900 秒です。
- `oni_count`, `sync_interval`, `grace_period` は必要に応じて設定更新 API で上書きします。

### ルーム設定更新

```http
PUT /rooms/:id
Content-Type: application/json
```

リクエスト例:

```json
{
  "time_limit": 900,
  "oni_count": 1,
  "area_size": "school-yard",
  "sync_interval": 180,
  "grace_period": 120,
  "area_center": {
    "lat": 34.0,
    "lng": 135.0
  }
}
```

バリデーション:

- `time_limit`: 600 / 900 / 1800 秒
- `oni_count`: 1 から 3 人
- `area_size`: 50 文字以下
- `sync_interval`: 60 / 180 / 300 秒
- `grace_period`: 60 / 120 / 180 秒
- `area_center`: 任意。指定する場合、`lat` は -90 から 90、`lng` は -180 から 180

現在の実装では、設定更新時に `time_limit`, `oni_count`, `area_size`, `sync_interval`, `grace_period` をすべて送る前提です。一部項目だけを送ると、未指定の数値項目が `0` として扱われるため、フロント側ではフォームの全項目をまとめて送信してください。
`area_center` は任意項目です。未指定または `null` の場合、保存済みの中心地点は変更されません。

## WebSocket: 送信 action

### join

入室、再接続、プレイヤー名や色の更新に使います。

```json
{
  "action": "join",
  "user_id": "user-001",
  "name": "はるき",
  "color": "#00AAFF"
}
```

要点:

- `user_id` はフロント側で安定して保持します。
- 同じ `room_id` と `user_id` で再接続すると、DB に保存された状態を使って復帰します。
- 新規参加は待機中のみ許可されます。ゲーム中またはリザルト中の新規参加は `error` になります。
- ゲーム中またはリザルト中でも、既存の `room_id + user_id` の復帰は許可されます。
- 参加人数は最大 15 人です。
- `name` は 1 文字以上 20 文字以下です。
- `color` は `#` から始まる 7 文字の形式を送ってください。

受信イベント:

```json
{
  "event": "waiting",
  "players": ["はるき", "みな"]
}
```

### start

ゲーム開始に使います。

```json
{
  "action": "start",
  "oni_users": ["user-001"]
}
```

要点:

- `oni_users` は鬼にする参加済み `user_id` の配列です。
- 開始には 2 人以上の参加者が必要です。
- `oni_users` は 1 から 3 人で、重複はできません。
- 全員を鬼にすることはできません。
- `oni_count` と `oni_users` の人数は一致させてください。
- `oni_users` が空、未指定、重複、未参加の `user_id` を含む場合は開始されず、送信元に `error` が届きます。
- 鬼に指定されたプレイヤーの `color` はサーバー側で `black` に上書きされます。

受信イベント:

```json
{
  "event": "start",
  "role": 1,
  "time_limit": 900,
  "oni_users": ["user-001"]
}
```

`role` は `0` が逃走者、`1` が鬼です。`start` は役割通知であり、猶予時間が設定されている場合はまだ本編開始前です。

猶予時間が終わり、本編が開始されると以下が届きます。

```json
{
  "event": "game_active"
}
```

フロント側では、役割表示やカウントダウンは `start`、位置同期や確保操作の本格開始は `game_active` を基準にしてください。

### move

位置情報更新に使います。

```json
{
  "action": "move",
  "lat": 34.7,
  "lng": 135.5
}
```

要点:

- `join` 後に送信します。
- 緯度は -90 から 90、経度は -180 から 180 の範囲です。
- `move` は高頻度送信を想定しており、送信のたびにはブロードキャストされません。
- 鬼は未捕獲逃走者の位置を `sync` イベントで受け取ります。

### capture_request

鬼が逃走者へ確保確認を送る action です。

```json
{
  "action": "capture_request",
  "target_id": "user-002"
}
```

対象の逃走者だけに以下が届きます。

```json
{
  "event": "capture_checking",
  "attacker_name": "はるき"
}
```

### capture_response

逃走者が確保を承認または拒否する action です。

```json
{
  "action": "capture_response",
  "approved": true
}
```

承認時:

```json
{
  "event": "captured",
  "target_id": "user-002",
  "approved": true
}
```

承認によって逃走者が全員捕まった場合は、`captured` の直後に `result` が届きます。

拒否時:

```json
{
  "event": "capture_denied",
  "target_id": "user-002"
}
```

### reset

リザルト後に同じ room でもう一度遊ぶための action です。

```json
{
  "action": "reset"
}
```

要点:

- `result` 後だけ実行できます。ゲーム中や待機中に送ると `error` になります。
- 接続中の参加者だけを次ゲームの参加者として残します。
- 役割、捕獲状態、位置情報はリセットされ、全員が待機中の逃走者状態に戻ります。
- 成功すると接続中クライアントへ `waiting` が届きます。

### leave

ホームへ戻る、または明示的に退出するときの action です。

```json
{
  "action": "leave"
}
```

要点:

- 待機中とリザルト中の `leave` では、DB の player も削除されます。
- ゲーム中の `leave` では、復帰できるよう DB の player は残し、接続中メモリからだけ外します。
- WebSocket の通常切断とは別の明示退出として扱います。

## WebSocket: 受信 event

### room_settings

ルーム設定更新後は接続中の全クライアントへ届きます。`join` 成功直後にも、参加したクライアントだけへ現在の設定が届きます。

```json
{
  "event": "room_settings",
  "time_limit": 900,
  "oni_count": 1,
  "area_size": "500m",
  "sync_interval": 180,
  "grace_period": 120,
  "area_center": {
    "lat": 34.0,
    "lng": 135.0
  }
}
```

中心地点が未設定の場合、`area_center` は `null` です。

### sync

ゲーム本編中に届く位置同期イベントです。`game_active` の直後に初回 `sync` が届き、その後は `sync_interval` ごとに届きます。

```json
{
  "event": "sync",
  "locations": [
    {
      "player_id": "room-001:user-001",
      "user_id": "user-001",
      "name": "はるき",
      "role": 0,
      "is_caught": false,
      "lat": 34.7,
      "lng": 135.5,
      "color": "#00AAFF"
    }
  ]
}
```

`role` は `0` が逃走者、`1` が鬼です。`locations` の中身は受信者ごとに変わります。

- 鬼には、未捕獲逃走者だけが `lat` / `lng` 付きで届きます。
- 未捕獲逃走者には、自分の状態だけが `lat` / `lng` 付きで届きます。
- 捕獲済み逃走者には、自分の状態だけが届き、`lat` / `lng` は省略されます。
- 捕獲済み逃走者の位置は、他プレイヤー向けには送られません。

### result

制限時間終了、または逃走者が全員捕まったときに届きます。

```json
{
  "event": "result",
  "survivors": ["user-002"],
  "results": [
    {
      "user_id": "user-001",
      "name": "はるき",
      "role": 1,
      "is_caught": false
    }
  ]
}
```

`survivors` には最後まで捕まらなかった逃走者の `user_id` が入ります。全員捕獲時は空配列です。`results` には逃走者と鬼の両方が含まれます。

### error

不正な入力や未対応の action の場合、送信元だけに届きます。

```json
{
  "event": "error",
  "message": "先に入室してください"
}
```

エラー文言はユーザー表示に使われる可能性があるため、フロント側では `event === "error"` を共通ハンドリングしてください。
