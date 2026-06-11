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
  "sync_interval": 3,
  "grace_period": 30,
  "area_center": {
    "lat": 34.0,
    "lng": 135.0
  }
}
```

バリデーション:

- `time_limit`: 1 から 3600 秒
- `oni_count`: 1 以上
- `area_size`: 50 文字以下
- `sync_interval`: 1 から 300 秒
- `grace_period`: 0 から 300 秒
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
- `oni_users` が空、未指定、または未参加の `user_id` を含む場合は開始されず、送信元に `error` が届きます。
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
- 他プレイヤーの位置は `sync` イベントで受け取ります。

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

拒否時:

```json
{
  "event": "capture_denied",
  "target_id": "user-002"
}
```

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

ゲーム本編中、`sync_interval` ごとに届く位置同期イベントです。

```json
{
  "event": "sync",
  "locations": [
    {
      "user_id": "user-001",
      "lat": 34.7,
      "lng": 135.5,
      "is_caught": false,
      "color": "#00AAFF"
    }
  ]
}
```

### result

制限時間終了、または逃走者が全員捕まったときに届きます。

```json
{
  "event": "result",
  "survivors": ["みな"],
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

### error

不正な入力や未対応の action の場合、送信元だけに届きます。

```json
{
  "event": "error",
  "message": "先に入室してください"
}
```

エラー文言はユーザー表示に使われる可能性があるため、フロント側では `event === "error"` を共通ハンドリングしてください。
