# Integration Checklist

この資料は、Backend Step3 / Frontend Step3 後の結合確認と、Step4 以降で追加確認する項目を分けて整理します。Step4 以降の項目は未実装を含むため、実装済み確認として扱わないでください。

## 基本仕様の確認

| 項目 | 内容 |
| --- | --- |
| `PUT /rooms/:id` | 現行は全項目送信が前提 |
| `time_limit` / `sync_interval` / `grace_period` | 秒単位 |
| `role` | `0` が逃走者、`1` が鬼 |
| `status` | `0` が待機中、`1` が進行中、`2` が終了 |
| `user_id` | frontend 側で安定して保持 |
| 最大参加人数 | `max_players` まで。初期値 6 人、絶対上限 15 人 |
| 最小開始人数 | 2 人 |
| 鬼人数 | 1 から 3 人。全員鬼は禁止 |
| 名前 | 前後空白を除いて 1 から 12 文字 |
| player color | `#RRGGBB`。`#000000` は鬼用。join時の空・黒・重複色はbackendが自動割当 |

## Step3 確認

| 確認項目 | 期待結果 | 担当 |
| --- | --- | --- |
| 2 台で join | 両端末に `waiting.players[]` が object 配列で届く | frontend / backend |
| start | 2 人以上、鬼 1 から 3 人、全員鬼ではない条件で開始できる | frontend / backend |
| game_active | 猶予時間後に全員へ `game_active` が届く | backend |
| 初回 sync | `game_active` 直後に初回 `sync` が届く | backend |
| 鬼側に未捕獲逃走者の `lat` / `lng` が届く | 鬼の `sync.locations[]` に未捕獲逃走者の座標が含まれる | frontend / backend |
| 逃走者側に他人座標が届かない | 逃走者の `sync.locations[]` は基本的に自分のみ | frontend / backend |
| result 後 reset | `result` 後に `{ "action": "reset" }` で `waiting` に戻り、`players[].color` が維持される | frontend / backend |
| result 後 leave | `{ "action": "leave" }` で明示退出でき、通常切断と区別される | frontend / backend |
| 再 start | reset 後、同じ room で再度 `start` できる | frontend / backend |

## Step4 以降確認

| 確認項目 | 期待結果 | 状態 |
| --- | --- | --- |
| capture_request 送信 | 鬼が `target_id` と `photo_url` を送れる | Step4 以降 |
| capture_checking 受信 | 対象逃走者だけが `request_id`、申請者情報、`photo_url`、`expires_at` を受け取る | Step4 以降 |
| 写真表示 | 対象逃走者の確認 UI に capture 写真が表示される | Step4 以降 |
| capture_response approved | 対象逃走者が `request_id` 付きで承認できる | Step4 以降 |
| captured broadcast | 承認時に全員へ `captured` が届く | Step4 以降 |
| capture_response rejected | 対象逃走者が `request_id` 付きで拒否できる | Step4 以降 |
| capture_denied | 拒否時に申請鬼 + 対象逃走者へ `capture_denied` が届く | Step4 以降 |
| 30 秒放置 | `pending` request が期限到達する | Step4 以降 |
| capture_expired | 申請鬼 + 対象逃走者へ `capture_expired` が届く | Step4 以降 |
| expired 後 response 拒否 | 期限切れ request への `capture_response` が拒否される | Step4 以降 |
| update_color | 待機中は `waiting`、ゲーム中は次回 `sync` に反映される | 実装済み |
| color 重複対応 | `update_color` は重複・黒を拒否し、`join` は空・黒・重複を自動割当する | 実装済み |
| room_settings broadcast | `PUT /rooms/:id` 後に全員へ `mission_enabled` と `max_players` を含む最新設定が届く | 実装済み |
| roulette_started | host の `start_roulette` で全員へ `selected_oni_user_ids` / `roulette_order` / `starts_at` / `duration_ms` 付きで届く | 実装済み |
| roulette / start 整合 | `start_roulette` で確定した鬼が、その後の `start` の実roleにも使われる | 実装済み |
| host移譲 | waiting中にhostが退出すると残りplayerへhostが移る | 実装済み |
| 捕獲済み runner の位置非表示 | 捕獲済み逃走者の座標が他プレイヤーの表示対象にならない | Step4 以降も継続確認 |
| result payload 拡張 | `survival_seconds` / `captured_at` / capture `photo_url` が追加される | 実装済み |
| result winner / end_reason | `winner` / `end_reason` が追加される | Step5/Step6 以降 |
| result 保存 | `game_results` / `player_results` に保存される | Step5/Step6 以降 |
| MVP 外 UI 確認 | mission / chat / area shrink など未実装 UI が誤操作を誘導しない | Step4 以降 |
| 鬼人数 UI max3 | frontend UI でも鬼人数が最大 3 人に制限される | Step4 以降 |

## 画面状態の確認

- 待機画面では `waiting.players[]` を使って参加者を表示します。
- 待機画面の参加枠数は `waiting.max_players` または `room_settings.max_players` を使います。
- `start` 受信時は役割を表示し、必要に応じて猶予時間の UI を出します。
- `game_active` 受信後に本編の操作を有効にします。
- 地図上の他プレイヤー表示は `sync.locations[]` を基準に更新します。
- `capture_checking` は対象逃走者だけが確認 UI を出します。
- `result` 受信後は結果画面へ遷移します。

## チームで判断したい項目

### WebSocket Origin

現在の WebSocket 接続は Origin を常に許可しています。ローカル開発では扱いやすい一方、本番環境では frontend のデプロイ先ドメインだけを許可する設定が必要です。

### ルーム ID

ルーム ID は 4 桁数字です。利用者が増えると衝突する可能性があります。衝突時の再生成、桁数の変更、または別形式の ID を使うかを確認してください。

### 設定変更のタイミング

現状では `PUT /rooms/:id` により DB と WebSocket 側メモリの設定が更新されます。ゲーム開始後の設定変更を許可するか、待機中だけに制限するかを決めておくと画面実装が安定します。

### 部分更新

現在の設定更新 API は PATCH ではなく、全項目送信を前提とした PUT です。frontend 側で全項目を必ず送る運用にするか、backend 側で部分更新を受け付けるかを確認してください。

### 位置情報の頻度

`move` は高頻度送信、`sync` は一定間隔の配信です。バッテリー消費、通信量、地図表示の滑らかさを見ながら `sync_interval` と frontend 側の送信頻度を調整してください。

## 連携時の最小シナリオ

1. ルームを作成する。
2. ルーム設定を更新する。
3. 2 人以上が同じルームへ WebSocket 接続する。
4. 各プレイヤーが `join` を送る。
5. 参加者一覧が `waiting` で更新される。
6. 1 人が `start` を送る。
7. 全員が `start` と `game_active` を受け取る。
8. 各プレイヤーが `move` を送る。
9. viewer 別の `sync` で位置情報を受け取る。
10. Step4 以降、鬼が `capture_request` を送り、逃走者が `capture_response` を返す。
11. Step4 以降、`captured` / `capture_denied` / `capture_expired` を確認する。
12. 終了条件を満たしたら `result` が届く。
