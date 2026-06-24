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
  "max_players": 6,
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
- `max_players` の初期値は 6 人です。
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
  "max_players": 6,
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
- `max_players`: 2 から 15 人。現在参加人数未満、または `oni_count` 以下にはできません。
- `area_size`: 50 文字以下
- `sync_interval`: 60 / 180 / 300 秒
- `grace_period`: 60 / 120 / 180 秒
- `area_center`: 任意。指定する場合、`lat` は -90 から 90、`lng` は -180 から 180

現在の実装では、設定更新時に `time_limit`, `oni_count`, `area_size`, `sync_interval`, `grace_period` をすべて送る前提です。一部項目だけを送ると、未指定の数値項目が `0` として扱われるため、フロント側ではフォームの全項目をまとめて送信してください。
`max_players` は任意項目です。未指定の場合は既存値、既存値が未設定の古いルームでは 6 人として扱われます。
`area_center` は任意項目です。未指定または `null` の場合、保存済みの中心地点は変更されません。

### Step3 時点の人数・設定 validation

| 項目 | 現行ルール |
| --- | --- |
| 最大参加人数 | 15 人 |
| `max_players` | 2 から 15。join 上限と待機画面枠数に使う |
| 最小開始人数 | 2 人 |
| 鬼人数 | 1 から 3 人 |
| `oni_count` | 1 から 3 |
| 全員鬼 | 禁止 |
| `oni_users` | 参加済み `user_id` のみ。空、重複、未参加者は `error` |

## WebSocket: 送信 action

### Payload matrix

| 種別 | 名前 | 方向 | Step3 状態 | 主な用途 |
| --- | --- | --- | --- | --- |
| action | `join` | frontend -> backend | 実装済み | 入室、再接続、待機中の名前・色更新 |
| event | `waiting` | backend -> clients | 実装済み | 待機中参加者一覧 |
| action | `update_color` | frontend -> backend | 実装済み | 待機中/ゲーム中の自分の色更新 |
| action | `start_roulette` | frontend -> backend | 実装済み | host がルーレット開始を通知 |
| event | `roulette_started` | backend -> clients | 実装済み | roulette 表示順と確定鬼を同期 |
| action | `start` | frontend -> backend | 実装済み | 役割指定とゲーム開始 |
| event | `start` | backend -> clients | 実装済み | 各 viewer への役割通知 |
| event | `game_active` | backend -> clients | 実装済み | 本編開始通知 |
| action | `move` | frontend -> backend | 実装済み | 位置更新 |
| event | `sync` | backend -> clients | 実装済み | viewer 別位置同期 |
| action | `capture_request` | frontend -> backend | 簡易実装済み | Step4 以降で `request_id` と 30 秒 expire を追加予定 |
| event | `capture_checking` | backend -> target runner | 簡易実装済み | 確保確認表示 |
| action | `capture_response` | frontend -> backend | 簡易実装済み | Step4 以降で pending request との紐づけを追加予定 |
| event | `captured` | backend -> clients | 簡易実装済み | 承認済み捕獲通知 |
| event | `capture_denied` | backend -> clients | 簡易実装済み | 拒否通知。Step4 以降は送信先を申請鬼 + 対象逃走者へ絞る提案 |
| action | `reset` | frontend -> backend | 実装済み | result 後の再戦 |
| action | `leave` | frontend -> backend | 実装済み | 明示退出 |
| event | `result` | backend -> clients | 実装済み | 現行は `survivors` / `results`。Step5/Step6 以降で `winner` などを拡張予定 |

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
- 参加人数は `room_settings.max_players` までです。未設定の古いルームは 6 人扱い、絶対上限は 15 人です。
- `name` は前後空白を除いて 1 文字以上 12 文字以下です。日本語も 1 文字として数えます。
- `color` は `#RRGGBB` 形式です。`#000000` は鬼用のため、逃走者/待機中プレイヤーは使えません。
- `join` の `color` が空、`#000000`、または他プレイヤーと重複する場合、backend が未使用 palette から安全な色を自動割当します。不正な形式の色は `error` です。

受信イベント:

```json
{
  "event": "waiting",
  "host_user_id": "user-1",
  "max_players": 6,
  "players": [
    {
      "user_id": "user-1",
      "name": "player name",
      "color": "#FF0000",
      "photo_url": null,
      "is_host": true
    }
  ]
}
```

`players` は Step3 後、文字列配列ではなく参加者 object の配列です。`host_user_id` と `players[].is_host` で現在のhostを判定できます。待機中にhostが退出した場合は、残っている参加者へhostが移譲されます。`photo_url` は未設定時は省略または `null` として扱ってください。

### update_color

自分の色を更新します。

```json
{
  "action": "update_color",
  "color": "#00AAFF"
}
```

要点:

- `join` 後に送信します。
- `color` は `#RRGGBB` 形式です。
- 同じ room の他 player が使っている色は拒否されます。
- `#000000` は鬼用として予約されています。
- 待機中は更新後に `waiting` が全員へ届きます。
- ゲーム中はメモリとDBが更新され、次回 `sync` に反映されます。

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
- 直前に `start_roulette` が成功している場合、backend は `roulette_started.selected_oni_user_ids` を優先して実際の鬼にします。この場合、`start` で送った `oni_users` とroulette結果はズレません。
- 開始には 2 人以上の参加者が必要です。
- `oni_users` は 1 から 3 人で、重複はできません。
- 全員を鬼にすることはできません。
- `oni_count` と `oni_users` の人数は一致させてください。
- `oni_users` が空、未指定、重複、未参加の `user_id` を含む場合は開始されず、送信元に `error` が届きます。
- 鬼に指定された接続中プレイヤーの表示色はゲーム中メモリでは `black` になります。DB 上の `player.color` は再戦用に元の選択色を保持します。
- `start` はhostのみ実行できます。

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

### start_roulette

host がルーレット開始ボタンを押したときに送ります。この時点で backend が鬼を確定し、次の `start` では同じ `user_id` を実際の role に使います。ゲーム開始自体はまだ行いません。

```json
{
  "action": "start_roulette"
}
```

成功すると全員へ以下が届きます。

```json
{
  "event": "roulette_started",
  "roulette_order": ["user-001", "user-002"],
  "selected_oni_user_ids": ["user-002"],
  "starts_at": "2026-01-01T00:00:00Z",
  "duration_ms": 3000
}
```

要点:

- `selected_oni_user_ids` は必須です。frontend はhost/guest共通の最終停止位置として使ってください。
- `roulette_order` は待機中プレイヤーの `user_id` を安定順で並べたものです。
- `starts_at` と `duration_ms` はhost/guestのanimation同期用です。
- 二重に `start_roulette` が送られた場合、参加者や設定が変わらない限りpending中の同じ結果を再送します。

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
  "target_id": "user-002",
  "photo_url": "https://..."
}
```

Step3 時点の実装では `request_id` はまだありません。Step4 以降では `request_id` を backend 採番にして、申請と回答を紐づける方針です。

対象の逃走者だけに以下が届きます。

```json
{
  "event": "capture_checking",
  "attacker_name": "はるき",
  "photo_url": "https://..."
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
  "target_id": "user-002",
  "approved": false
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
- 役割、捕獲状態、位置情報、game中の写真URLはリセットされ、全員が待機中の逃走者状態に戻ります。
- `player.color` と `name` は維持されるため、再戦後の `waiting.players[].color` は空になりません。
- 成功すると接続中クライアントへ `waiting` が届きます。
- result 後の再戦用です。

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
- ホームへ戻るなど、ユーザーが明示的に退出したい場合に使います。

## WebSocket: 受信 event

### room_settings

ルーム設定更新後は接続中の全クライアントへ届きます。`join` 成功直後にも、参加したクライアントだけへ現在の設定が届きます。

```json
{
  "event": "room_settings",
  "time_limit": 900,
  "oni_count": 1,
  "max_players": 6,
  "area_size": "500m",
  "sync_interval": 180,
  "grace_period": 120,
  "mission_enabled": false,
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
      "player_id": "room-id:user-id",
      "user_id": "user-id",
      "name": "player name",
      "role": 0,
      "is_caught": false,
      "color": "#FF0000",
      "lat": 34.0,
      "lng": 135.0,
      "photo_url": null
    }
  ]
}
```

`role` は `0` が逃走者、`1` が鬼です。`locations` の中身は受信者ごとに変わります。

- `lat` / `lng` は表示対象にする場合のみ存在します。
- 鬼には、未捕獲逃走者だけが `lat` / `lng` 付きで届きます。
- 逃走者には基本的に他人の座標は届かず、自分の状態だけが届きます。
- 未捕獲逃走者には、自分の状態が `lat` / `lng` 付きで届きます。
- 捕獲済み逃走者には、自分の状態だけが届き、`lat` / `lng` は省略されます。
- 捕獲済み逃走者の座標は、他プレイヤー向けの表示対象にしません。
- `photo_url` は backend 側にはありますが、frontend 型との整合は Step4 以降で整理予定です。未設定時は省略または `null` として扱ってください。

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
      "is_caught": false,
      "photo_url": null
    },
    {
      "user_id": "user-002",
      "name": "みな",
      "role": 0,
      "is_caught": true,
      "photo_url": "https://...",
      "captured_at": "2026-06-24T10:00:00+09:00",
      "survival_seconds": 120
    }
  ]
}
```

`survivors` には最後まで捕まらなかった逃走者の `user_id` が入ります。全員捕獲時は空配列です。`results` には逃走者と鬼の両方が含まれます。逃走者には `survival_seconds` が含まれます。捕獲済み逃走者には `captured_at` と capture 写真の `photo_url` も含まれます。鬼の `survival_seconds` は省略されます。

### error

不正な入力や未対応の action の場合、送信元だけに届きます。

```json
{
  "event": "error",
  "message": "先に入室してください"
}
```

エラー文言はユーザー表示に使われる可能性があるため、フロント側では `event === "error"` を共通ハンドリングしてください。
