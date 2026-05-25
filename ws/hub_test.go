package ws

import (
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/gorilla/websocket"
    "github.com/watnow/watnow-spring-2026-team2-backend/models"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

func TestWebSocketFlow(t *testing.T) {
    // Create in-memory database for testing
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    if err != nil {
        t.Fatalf("DB接続失敗: %v", err)
    }
    db.AutoMigrate(&models.Room{})

    // Create a test room
    testRoom := models.Room{
        ID:        "testRoom",
        Status:    0,
        TimeLimit: 900,
    }
    if err := db.Create(&testRoom).Error; err != nil {
        t.Fatalf("テストルーム作成失敗: %v", err)
    }

    // Ginをテストモードとして立ち上げ、ルーターを作成
    gin.SetMode(gin.TestMode)
    router := gin.Default()
    router.GET("/ws/rooms/:id", func(c *gin.Context) {
        ServeWs(c, db)
    })

    // httptestサーバーにGinのルーターを渡す
    s := httptest.NewServer(router)
    defer s.Close()

    // エンドポイント（/ws/rooms/testRoom）を含むURLを構築
    wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws/rooms/testRoom"

    ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
    if err != nil {
        t.Fatalf("接続失敗: %v", err)
    }
    defer ws.Close()

    joinJSON := []byte(`{"action": "join", "user_id": "player1", "name": "はるき"}`)
    err = ws.WriteMessage(websocket.TextMessage, joinJSON)
    if err != nil {
        t.Fatalf("送信失敗: %v", err)
    }

    ws.SetReadDeadline(time.Now().Add(2 * time.Second))
    _, p, err := ws.ReadMessage()
    if err != nil {
        t.Fatalf("受信失敗: %v", err)
    }

    if !strings.Contains(string(p), `"event":"waiting"`) && !strings.Contains(string(p), `"event": "waiting"`) {
        t.Errorf("想定外のレスポンス: %s", string(p))
    }
}

func TestWebSocketRoomNotFound(t *testing.T) {
    // Create in-memory database for testing
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    if err != nil {
        t.Fatalf("DB接続失敗: %v", err)
    }
    db.AutoMigrate(&models.Room{})

    // Ginをテストモードとして立ち上げ、ルーターを作成
    gin.SetMode(gin.TestMode)
    router := gin.Default()
    router.GET("/ws/rooms/:id", func(c *gin.Context) {
        ServeWs(c, db)
    })

    // httptestサーバーにGinのルーターを渡す
    s := httptest.NewServer(router)
    defer s.Close()

    // Try to connect to non-existent room
    wsURL := "ws" + strings.TrimPrefix(s.URL, "http") + "/ws/rooms/nonexistent"

    _, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
    if err == nil {
        t.Fatal("架空の部屋への接続が成功してしまいました")
    }

    if resp != nil && resp.StatusCode != 404 {
        t.Errorf("期待するステータスコードは404ですが、受け取ったのは %d です", resp.StatusCode)
    }
}