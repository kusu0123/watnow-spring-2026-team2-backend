package ws

import (
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/gorilla/websocket"
)

func TestWebSocketFlow(t *testing.T) {
    // Ginをテストモードとして立ち上げ、ルーターを作成
    gin.SetMode(gin.TestMode)
    router := gin.Default()
    router.GET("/ws/rooms/:id", ServeWs)

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