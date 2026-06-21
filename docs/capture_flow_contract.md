# Capture Flow Contract

この資料は、Step4 以降で実装する capture flow の契約案です。現行 backend には `capture_request` / `capture_checking` / `capture_response` / `captured` / `capture_denied` の簡易フローがありますが、`request_id`、DB 保存、30 秒 expire、申請単位の status 管理は未実装です。

## 方針

- Step4 以降の提案として、capture request は backend が `request_id` を採番して DB に保存します。
- photo field は、現状 backend に `photo_url` があるため、まずは `photo_url` に統一します。
- 将来 Storage key も保持したい場合は、DB に `photo_key` を追加し、WebSocket 表示用は `photo_url` を使います。
- 既存互換として、移行期のみ `request_id` なしの `capture_request` を許容してもよいですが、Step4 では `request_id` 導入を優先します。
- WebSocket 切断と明示的な `leave` は別扱いです。capture の pending request は `leave` / timeout / result 遷移時の扱いを Step4 実装時に確定します。

## Status

DB 上の `status` は以下に統一する提案です。

| status | 意味 |
| --- | --- |
| `pending` | 対象逃走者の回答待ち |
| `approved` | 対象逃走者が承認し、捕獲成立 |
| `rejected` | 対象逃走者が拒否し、捕獲不成立 |
| `expired` | 30 秒以内に回答がなく期限切れ |

既存 event 名の `capture_denied` は、DB status では `rejected` に対応します。

## Payload 案

### capture_request

frontend -> backend

```json
{
  "action": "capture_request",
  "target_id": "target-user-id",
  "photo_url": "https://..."
}
```

| 項目 | 担当 | 内容 |
| --- | --- | --- |
| `target_id` | frontend | 対象逃走者の `user_id` |
| `photo_url` | frontend | Supabase Storage などに upload 済みの表示用 URL |
| `request_id` | backend | backend が採番する方針を推奨 |

Step4 では `request_id` 導入を優先します。移行期のみ `request_id` なしを許容する場合でも、backend は受信時に必ず `request_id` を生成し、以降の event/action に含めます。

### capture_checking

backend -> target runner

```json
{
  "event": "capture_checking",
  "request_id": "capture-request-id",
  "attacker_id": "attacker-user-id",
  "attacker_name": "attacker name",
  "target_id": "target-user-id",
  "photo_url": "https://...",
  "expires_at": "2026-01-01T00:00:30Z"
}
```

対象逃走者だけに送ります。frontend は `request_id` を保持し、承認または拒否時に `capture_response` へ含めます。

### capture_response

target runner -> backend

```json
{
  "action": "capture_response",
  "request_id": "capture-request-id",
  "approved": true
}
```

backend は `request_id` で `pending` の capture request を取得し、対象逃走者本人からの回答かを検証します。`expired` 後の `capture_response` は拒否します。

### captured

backend -> all clients

```json
{
  "event": "captured",
  "request_id": "capture-request-id",
  "target_id": "target-user-id",
  "attacker_id": "attacker-user-id",
  "approved": true,
  "photo_url": "https://..."
}
```

承認時は `status = approved` に更新し、対象逃走者の `is_caught` を `true` にします。その後、全員へ `captured` を配信します。

### capture_denied

backend -> attacker and target

```json
{
  "event": "capture_denied",
  "request_id": "capture-request-id",
  "target_id": "target-user-id",
  "attacker_id": "attacker-user-id",
  "approved": false
}
```

拒否時は `status = rejected` に更新します。MVP では申請鬼と対象逃走者だけに送ります。

### capture_expired

backend -> attacker and target

```json
{
  "event": "capture_expired",
  "request_id": "capture-request-id",
  "target_id": "target-user-id",
  "attacker_id": "attacker-user-id"
}
```

期限切れ時は `status = expired` に更新します。MVP では申請鬼と対象逃走者だけに送ります。

## 30 秒 expire

- `created_at + 30s` を期限とします。
- `pending` のまま期限到達したら `expired` に更新します。
- `capture_expired` を申請鬼と対象逃走者へ送ります。
- MVP では全員通知は不要です。
- expired 後の `capture_response` は拒否します。
- 複数 request が同時に存在できるか、同一 target に pending を 1 件までにするかは Step4 実装前に確定します。MVP では同一 target の pending は 1 件までを推奨します。

## DB 項目案

`capture_requests` に保存する項目案です。Step4 以降の提案であり、現行 model には未実装です。

| field | 内容 |
| --- | --- |
| `id` | `request_id` |
| `room_id` | room id |
| `attacker_user_id` | 捕獲申請した鬼 |
| `target_user_id` | 対象逃走者 |
| `status` | `pending` / `approved` / `rejected` / `expired` |
| `photo_url` | 表示用写真 URL |
| `created_at` | 作成時刻 |
| `expires_at` | 期限 |
| `responded_at` | 承認/拒否時刻 |

## 配信先

| event | 送信先 |
| --- | --- |
| `capture_checking` | 対象逃走者のみ |
| `captured` | 全員 |
| `capture_denied` | 申請鬼 + 対象逃走者 |
| `capture_expired` | 申請鬼 + 対象逃走者 |

## Validation 案

| 項目 | backend | frontend |
| --- | --- | --- |
| 申請者 role | 鬼のみ許可 | 鬼にだけ capture UI を表示 |
| 対象 role | 未捕獲逃走者のみ許可 | 捕獲済み逃走者を選択不可 |
| `photo_url` | 空許容にするか必須にするか Step4 で確定 | Supabase Storage upload 後の URL を送る |
| `request_id` | 採番、重複防止、pending 検証 | `capture_checking` で受け取った値を返す |
| 期限切れ | `expired` 更新後の回答を拒否 | 30 秒経過後は回答 UI を閉じる |

## 既存互換

- 現行 backend は `capture_response` に `request_id` を要求していません。
- Step4 以降は `request_id` ありを正とし、移行期のみ `request_id` なしを backend 側で明示的に許容するか判断します。
- 現行 event の `capture_denied` は残し、DB status 名だけ `rejected` に寄せます。
