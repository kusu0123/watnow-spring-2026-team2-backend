# AI Development Guide

このドキュメントは、AI がこのバックエンドの開発を手伝うときに読む前提ルールです。目的は、変なコード、重複コード、アーキテクチャ崩れを防ぐことです。

## 最初に読む docs

作業前に、最低限この順番で確認してください。

1. `README.md`
2. `docs/architecture.md`
3. `docs/frontend_integration.md`
4. `docs/integration_checklist.md`
5. `docs/development_guide.md`

WebSocket や payload を触る場合は、`docs/frontend_integration.md` を必ず確認してください。

## 基本ルール

- 同じような処理を別の場所に増やさない。
- 既存の責務分担に従う。
- `main.go` にゲームロジックを増やさない。
- `models` に WebSocket 接続状態を入れない。
- `ws` に REST routing を増やさない。
- URL をコードに直書きしない。
- Render の実URLを repo docs に書かない。
- 認証や他人の入室対策を勝手に追加しない。
- 仕様変更が必要な場合は、先に docs の変更点を明確にする。

## 変更場所の判断

### REST API を変えるとき

主に `main.go` を確認します。

必ず更新する docs:

- `docs/frontend_integration.md`
- 必要なら `README.md`

### WebSocket payload を変えるとき

主に `ws/message.go`, `ws/hub.go`, `ws/room.go` を確認します。

必ず更新する docs:

- `docs/frontend_integration.md`
- 必要なら `docs/integration_checklist.md`
- 必要なら `docs/architecture.md`

### DB モデルを変えるとき

主に `models/room.go`, `models/player.go` を確認します。

必ず更新する docs:

- `docs/architecture.md`
- `docs/frontend_integration.md`

## 重複実装を避ける

新しい関数や構造体を作る前に、次を確認してください。

- 既に `RoomState` に近い状態がないか
- 既に `Client` に近い状態がないか
- 既に `OutgoingMessage` / `IncomingMessage` に field がないか
- 既に `Broadcast`, `locations`, `resultMessage`, `finish` で扱える処理ではないか
- REST でやるべき処理を WebSocket に重複して作ろうとしていないか
- WebSocket の進行処理を REST に重複して作ろうとしていないか

似たコードが必要に見える場合は、まず既存の責務に寄せて小さく拡張してください。

## フロント連携時の注意

現状のフロントは React Native / Expo の UI モック寄りで、REST/WebSocket/GPS/全体状態管理はまだ本格的に接続されていません。

AI がフロント連携を手伝う場合:

- `apiClient.ts` に REST 接続を寄せる。
- `webSocketClient.ts` に WebSocket 接続を寄せる。
- 接続先 URL は設定値から読む。
- まずは `POST /rooms` -> WebSocket 接続 -> `join` -> `waiting` の最小導線を通す。
- いきなり map/GPS/capture/result を全部つなごうとしない。

## テスト方針

コードを変更した場合は、原則としてテストを確認します。

PowerShell:

```powershell
$env:CGO_ENABLED='1'
go test ./...
```

`CGO_ENABLED=0` による SQLite エラーは環境問題です。コード変更の評価と混同しないでください。

## 禁止事項

- docs と違う payload を勝手に実装する。
- payload を変えたのに docs を更新しない。
- 似たような WebSocket client 管理を別に作る。
- DB に保存すべき再接続用状態をメモリだけに置く。
- メモリだけでよい接続状態を DB に無理に保存する。
- Render URL をハードコードする。
- 認証やセキュリティ機能を小さなついで修正として混ぜる。

必要な場合は、別作業として設計してから実装してください。
