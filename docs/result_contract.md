# Result Contract

この資料は、Step5/Step6 以降で扱う result payload 拡張と result 保存の契約案です。現行 backend は `result` event の `survivors` / `results` を返しますが、`winner`、`end_reason`、`survival_seconds`、result DB 保存は未実装です。

## 方針

- 既存の `result` event の `survivors` / `results` は維持します。
- 追加 field で `winner`、`end_reason`、`survival_seconds` などを拡張します。
- frontend は追加 field があれば優先し、なければ現行の `survivors` / `results` から推定表示する fallback を持ちます。
- result DB 保存は Step5/Step6 以降の提案です。現行 model には `game_results` / `player_results` は未実装です。
- `photo_url` は backend 側の player field として存在するため、result 表示用にも `photo_url` を使います。

## Payload 案

backend -> clients

```json
{
  "event": "result",
  "winner": "runner",
  "end_reason": "time_limit",
  "survivors": ["user-1"],
  "results": [
    {
      "user_id": "user-1",
      "name": "player name",
      "role": 0,
      "is_caught": false,
      "survival_seconds": 600,
      "photo_url": null
    }
  ]
}
```

## Field

| field | 状態 | 内容 |
| --- | --- | --- |
| `event` | 実装済み | `result` |
| `winner` | Step5/Step6 以降の提案 | `oni` / `runner` |
| `end_reason` | Step5/Step6 以降の提案 | 終了理由 |
| `survivors` | 実装済み | 最後まで捕まらなかった逃走者の `user_id` |
| `results[]` | 実装済み | 逃走者と鬼を含むプレイヤー結果 |
| `results[].survival_seconds` | Step5/Step6 以降の提案 | 逃走者の生存秒数。鬼は `0` または省略を Step5/Step6 で確定 |
| `results[].photo_url` | 実装済み field 由来 | 未設定時は省略または `null` として扱う |

## winner

| value | 条件 |
| --- | --- |
| `oni` | `all_runners_caught` で終了 |
| `runner` | `time_limit` で終了 |

## end_reason

| value | 内容 |
| --- | --- |
| `all_runners_caught` | 逃走者が全員捕獲された |
| `time_limit` | 制限時間に到達した |

将来必要なら以下も追加可能です。

- `room_empty`
- `manual_end`

## DB model 案

Step5/Step6 以降の提案であり、現行 model には未実装です。

### game_results

| field | 内容 |
| --- | --- |
| `id` | game result id |
| `room_id` | room id |
| `winner` | `oni` / `runner` |
| `end_reason` | `all_runners_caught` / `time_limit` |
| `started_at` | game active 開始時刻 |
| `ended_at` | 終了時刻 |
| `duration_seconds` | 実ゲーム時間 |
| `created_at` | 作成時刻 |

### player_results

| field | 内容 |
| --- | --- |
| `id` | player result id |
| `game_result_id` | `game_results.id` |
| `user_id` | user id |
| `name` | player name |
| `role` | runner / oni |
| `is_caught` | 捕獲済みか |
| `survival_seconds` | 生存秒数 |
| `photo_url` | player photo url |

## 既存互換

- 現行 frontend は `survivors` と `results[].is_caught` / `results[].role` から勝敗を推定できます。
- Step5/Step6 以降で `winner` と `end_reason` が追加されたら、frontend は追加 field を優先します。
- 追加 field は optional として扱い、古い backend payload でも result 画面が壊れないようにします。
- `results[]` の基本 shape は維持し、既存画面の参照先を壊さない方針です。

## 担当整理

| 項目 | frontend | backend |
| --- | --- | --- |
| `winner` / `end_reason` 表示 | 追加 field があれば優先表示 | 終了条件から算出して payload に含める |
| `survival_seconds` 表示 | 値があれば表示、なければ現行表示 | 捕獲時刻または終了時刻から算出 |
| result 保存 | 履歴 UI は Step5/Step6 以降 | `game_results` / `player_results` 作成 |
| 既存 fallback | 現行 payload から推定 | `survivors` / `results` を維持 |
