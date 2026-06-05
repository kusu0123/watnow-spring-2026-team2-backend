# watnow-spring-2026-team2-backend

鬼ごっこアプリ用の Go バックエンドです。REST API で部屋を作成・設定し、WebSocket で入室、ゲーム開始、位置同期、捕獲確認、結果通知を扱います。

## 現在の構成

- `main.go`: Gin のルーティング、SQLite 接続、GORM AutoMigrate、REST endpoint
- `models/`: DB に保存する `Room` と `Player`
- `ws/`: WebSocket 接続、部屋ごとのメモリ状態、ゲームループ、イベント処理
- `docs/`: 開発者とAI向けの仕様・設計メモ

## 起動

このプロジェクトは SQLite driver として `go-sqlite3` を使うため、ローカル実行とテストには CGO が必要です。

```powershell
$env:CGO_ENABLED='1'
go run .
```


## テスト

```powershell
$env:CGO_ENABLED='1'
go test ./...
```

`CGO_ENABLED=0` のままだと `go-sqlite3` が動かず、WebSocket テストが DB 接続失敗で落ちます。詳しくは [docs/development_guide.md](docs/development_guide.md) を確認してください。


## 主要 docs

- [docs/development_guide.md](docs/development_guide.md): ローカル開発、CGO、SQLite、テスト実行
- [docs/architecture.md](docs/architecture.md): アーキテクチャ、責務分担、DB とメモリ状態
- [docs/frontend_integration.md](docs/frontend_integration.md): REST API と WebSocket action/event の仕様
- [docs/integration_checklist.md](docs/integration_checklist.md): フロント連携前の確認項目
- [docs/product_rules.md](docs/product_rules.md): ゲームルール、状態遷移、再接続の考え方
- [docs/ai-development-guide.md](docs/ai-development-guide.md): AI が開発を手伝うときのルール
