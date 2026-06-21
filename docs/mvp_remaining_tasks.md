# MVP Remaining Tasks

この資料は、Backend Step3 / Frontend Step3 後から MVP 完成までの残タスク整理です。P0 は MVP 動作に必須、P1 は MVP 品質・保存性のために優先、P2 は MVP 後または余力対応の候補です。

## P0

| タスク | 優先度 | frontend/backend | 依存 | 担当候補 | 完了条件 |
| --- | --- | --- | --- | --- | --- |
| capture contract 確定 | P0 | frontend / backend | Step3 payload 確認 | frontend + backend | `capture_flow_contract.md` の payload、送信先、status、expire 方針に合意済み |
| capture_requests DB | P0 | backend | capture contract 確定 | backend | `capture_requests` model / migration 相当が追加され、pending request を保存できる |
| request_id 導入 | P0 | backend / frontend | capture_requests DB | backend + frontend | backend が `request_id` を採番し、`capture_checking` / `capture_response` / 結果 event で同じ ID を扱える |
| capture_response の pending 紐づけ | P0 | backend | request_id 導入 | backend | `capture_response` が `pending` request と対象逃走者本人に紐づく場合だけ処理される |
| 30 秒 expire | P0 | backend / frontend | capture_requests DB | backend + frontend | `created_at + 30s` で `expired` になり、期限後 response が拒否される |
| Supabase Storage upload | P0 | frontend | photo upload 方針 | frontend | capture 申請前に写真 upload が完了し、表示用 URL を取得できる |
| photo_url 送信 | P0 | frontend / backend | Supabase Storage upload | frontend + backend | `capture_request` で `photo_url` が送られ、`capture_checking` / result など必要箇所で参照できる |
| capture 確認 UI | P0 | frontend | request_id 導入、photo_url 送信 | frontend | 対象逃走者が写真、申請者、残り時間を見て承認/拒否できる |
| 鬼人数 UI max3 修正 | P0 | frontend | backend validation 確認 | frontend | UI 上で鬼人数が 1 から 3 人に制限され、全員鬼を選べない |
| 2 端末 capture E2E | P0 | frontend / backend | capture 確認 UI、30 秒 expire | frontend + backend | 2 台で申請、承認、拒否、expire、result 到達を確認済み |

## P1

| タスク | 優先度 | frontend/backend | 依存 | 担当候補 | 完了条件 |
| --- | --- | --- | --- | --- | --- |
| result contract 確定 | P1 | frontend / backend | capture contract 確定 | frontend + backend | `result_contract.md` の `winner` / `end_reason` / 保存方針に合意済み |
| game_results / player_results 保存 | P1 | backend | result contract 確定 | backend | ゲーム終了時に game result と player result を保存できる |
| winner / end_reason / survival_seconds 追加 | P1 | backend / frontend | result 保存方針 | backend + frontend | result payload に追加 field が入り、frontend が優先表示できる |
| result 画面拡張 | P1 | frontend | winner / end_reason / survival_seconds 追加 | frontend | 勝者、終了理由、生存秒数、写真を表示できる |
| MVP 外 UI 整理 | P1 | frontend | MVP scope 確定 | frontend | mission / chat / area shrink など未実装 UI が誤操作を誘導しない |
| 位置未取得時の 0,0 対策 | P1 | frontend / backend | sync payload 確認 | frontend + backend | 初期値 `0,0` を実位置として表示しない。未取得状態を UI / payload で区別できる |

## P2

| タスク | 優先度 | frontend/backend | 依存 | 担当候補 | 完了条件 |
| --- | --- | --- | --- | --- | --- |
| result 履歴 | P2 | frontend / backend | game_results / player_results 保存 | frontend + backend | 過去 game result を取得・表示できる |
| 切断 30 秒判定 | P2 | backend / frontend | WebSocket 切断方針 | backend + frontend | 通常切断後 30 秒以内は復帰扱い、超過後の扱いが明確になる |
| host 権限強化 | P2 | backend / frontend | user / host 方針 | backend + frontend | start / settings / reset などを host のみ実行できる |
| mission | P2 | frontend / backend | MVP 後 scope | frontend + backend | mission payload と UI が定義・実装される |
| chat | P2 | frontend / backend | MVP 後 scope | frontend + backend | chat payload と UI が定義・実装される |
| area shrink | P2 | frontend / backend | area rule 方針 | frontend + backend | エリア縮小ルール、payload、地図表示が定義・実装される |

## 次 PR 候補

- `docs/product_rules.md` に capture / result のルールを反映する。
- `docs/architecture.md` に result 保存 model と capture request model を追記する。
- 今回の PR では上記 2 ファイルは変更しません。
