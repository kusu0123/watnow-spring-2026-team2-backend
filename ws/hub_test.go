package ws

import (
	"encoding/json"
	"math"
	"net"
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

func newTestServer(t *testing.T, room models.Room) (*gorm.DB, string, func()) {
	t.Helper()

	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("DB接続失敗: %v", err)
	}
	if err := db.AutoMigrate(&models.Room{}, &models.Player{}); err != nil {
		t.Fatalf("マイグレーション失敗: %v", err)
	}

	if room.ID != "" {
		if err := db.Create(&room).Error; err != nil {
			t.Fatalf("テストルーム作成失敗: %v", err)
		}
	}

	GameHub = &Hub{Rooms: make(map[string]*RoomState)}

	gin.SetMode(gin.TestMode)
	router := gin.Default()
	router.GET("/ws/rooms/:id", func(c *gin.Context) {
		ServeWs(c, db)
	})

	server := httptest.NewServer(router)
	baseURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/rooms/"
	cleanup := func() {
		server.Close()
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	}

	return db, baseURL, cleanup
}

func connectToRoom(t *testing.T, baseURL, roomID string) *websocket.Conn {
	t.Helper()

	wsConn, _, err := websocket.DefaultDialer.Dial(baseURL+roomID, nil)
	if err != nil {
		t.Fatalf("接続失敗: %v", err)
	}

	return wsConn
}

func sendJSON(t *testing.T, wsConn *websocket.Conn, body string) {
	t.Helper()

	if err := wsConn.WriteMessage(websocket.TextMessage, []byte(body)); err != nil {
		t.Fatalf("送信失敗: %v", err)
	}
}

func readMessage(t *testing.T, wsConn *websocket.Conn) OutgoingMessage {
	t.Helper()

	wsConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := wsConn.ReadMessage()
	if err != nil {
		t.Fatalf("受信失敗: %v", err)
	}

	var msg OutgoingMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("JSONパース失敗: %v", err)
	}

	return msg
}

func readUntilEvent(t *testing.T, wsConn *websocket.Conn, event string) OutgoingMessage {
	t.Helper()

	for i := 0; i < 8; i++ {
		msg := readMessage(t, wsConn)
		if msg.Event == event {
			return msg
		}
	}

	t.Fatalf("%s イベントを受信できませんでした", event)
	return OutgoingMessage{}
}

func assertErrorMessage(t *testing.T, wsConn *websocket.Conn, message string) {
	t.Helper()

	msg := readMessage(t, wsConn)
	if msg.Event != "error" || msg.Message != message {
		t.Fatalf("エラー通知が不正です: %+v", msg)
	}
}

func assertNoMessage(t *testing.T, wsConn *websocket.Conn) {
	t.Helper()

	wsConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err := wsConn.ReadMessage()
	if err == nil {
		t.Fatal("通知されないはずのクライアントがメッセージを受信しました")
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("想定外の読み取りエラーです: %v", err)
	}
}

func waitFor(t *testing.T, check func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("条件が時間内に満たされませんでした")
}

func findClient(room *RoomState, userID string) (*Client, bool) {
	room.mu.RLock()
	defer room.mu.RUnlock()

	for client := range room.Clients {
		client.mu.Lock()
		sameUser := client.UserID == userID
		client.mu.Unlock()
		if sameUser {
			return client, true
		}
	}

	return nil, false
}

func findResult(results []ResultVal, userID string) (ResultVal, bool) {
	for _, result := range results {
		if result.UserID == userID {
			return result, true
		}
	}
	return ResultVal{}, false
}

func findLocation(locations []LocationVal, userID string) (LocationVal, bool) {
	for _, location := range locations {
		if location.UserID == userID {
			return location, true
		}
	}
	return LocationVal{}, false
}

func TestWebSocketFlow(t *testing.T) {
	roomID := "testRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"blue"}`)
	msg := readMessage(t, wsConn)
	if msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
		t.Fatalf("プレイヤー保存失敗: %v", err)
	}
	if player.ID != makePlayerID(roomID, "player1") || player.Name != "はるき" || player.Color != "blue" {
		t.Fatalf("保存されたプレイヤーが不正です: %+v", player)
	}

	room := GameHub.GetOrCreateRoom(roomID)
	client, ok := findClient(room, "player1")
	if !ok {
		t.Fatal("入室したクライアントがメモリに保存されていません")
	}

	client.mu.Lock()
	color := client.Color
	client.mu.Unlock()
	if color != "blue" {
		t.Fatalf("メモリに保存されたカラーが不正です: %s", color)
	}

	_ = wsConn.Close()
	waitFor(t, func() bool {
		room.mu.RLock()
		count := len(room.Clients)
		room.mu.RUnlock()
		return count == 0
	})

	var count int64
	if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, "player1").Count(&count).Error; err != nil {
		t.Fatalf("プレイヤー件数取得失敗: %v", err)
	}
	if count != 1 {
		t.Fatalf("切断後もプレイヤーは1件残る想定ですが、%d件でした", count)
	}
}

func TestWebSocketRoomNotFound(t *testing.T) {
	_, baseURL, cleanup := newTestServer(t, models.Room{})
	defer cleanup()

	_, resp, err := websocket.DefaultDialer.Dial(baseURL+"nonexistent", nil)
	if err == nil {
		t.Fatal("架空の部屋への接続が成功してしまいました")
	}

	if resp != nil && resp.StatusCode != 404 {
		t.Errorf("期待するステータスコードは404ですが、受け取ったのは %d です", resp.StatusCode)
	}
}

func TestReconnectRestoresPlayerState(t *testing.T) {
	roomID := "reconnectRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	oldPlayer := models.Player{
		ID:       makePlayerID(roomID, "player1"),
		RoomID:   roomID,
		UserID:   "player1",
		Name:     "前の名前",
		Role:     1,
		IsCaught: true,
		Lat:      35.1,
		Lng:      139.2,
		Color:    "red",
	}
	if err := db.Create(&oldPlayer).Error; err != nil {
		t.Fatalf("既存プレイヤー作成失敗: %v", err)
	}

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"新しい名前"}`)
	msg := readMessage(t, wsConn)
	if msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	room := GameHub.GetOrCreateRoom(roomID)
	client, ok := findClient(room, "player1")
	if !ok {
		t.Fatal("再接続したクライアントがメモリに復元されていません")
	}

	client.mu.Lock()
	name := client.Name
	role := client.Role
	isCaught := client.IsCaught
	lat := client.Lat
	lng := client.Lng
	color := client.Color
	client.mu.Unlock()

	if name != "新しい名前" || role != 1 || !isCaught || math.Abs(lat-35.1) > 0.000001 || math.Abs(lng-139.2) > 0.000001 || color != "red" {
		t.Fatalf("復元されたクライアント状態が不正です: name=%s role=%d caught=%v lat=%f lng=%f color=%s", name, role, isCaught, lat, lng, color)
	}

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
		t.Fatalf("プレイヤー取得失敗: %v", err)
	}
	if player.Name != "新しい名前" || player.Color != "red" {
		t.Fatalf("再接続後のDB更新が不正です: %+v", player)
	}
}

func TestWebSocketStartFlowWithSettings(t *testing.T) {
	roomID := "startFlowRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  0,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	GameHub.UpdateRoomSettings(roomID, 120, 1, 1, 0)

	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.RLock()
	if room.SyncInterval != 1 || room.GracePeriod != 0 {
		room.mu.RUnlock()
		t.Fatalf("設定反映失敗: sync_interval=%d grace_period=%d", room.SyncInterval, room.GracePeriod)
	}
	room.mu.RUnlock()

	sendJSON(t, wsConn, `{"action":"start"}`)

	gotStart := false
	gotGameActive := false

	for i := 0; i < 3; i++ {
		msg := readMessage(t, wsConn)
		switch msg.Event {
		case "start":
			gotStart = true
			if msg.TimeLimit != 120 {
				t.Fatalf("startのtime_limitが不正です: %d", msg.TimeLimit)
			}
			if msg.Role == nil || *msg.Role != 1 {
				t.Fatalf("startのroleが不正です: %v", msg.Role)
			}
		case "game_active":
			gotGameActive = true
		}

		if gotStart && gotGameActive {
			break
		}
	}

	if !gotStart || !gotGameActive {
		t.Fatalf("startフロー未達: start=%v game_active=%v", gotStart, gotGameActive)
	}

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
		t.Fatalf("プレイヤー取得失敗: %v", err)
	}
	if player.Role != 1 {
		t.Fatalf("DBに保存されたroleが不正です: %d", player.Role)
	}
}

func TestGracePeriodUsesSecondsAndMoveKeepsWorking(t *testing.T) {
	roomID := "graceSecondsRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    5,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  1,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	startedAt := time.Now()
	sendJSON(t, wsConn, `{"action":"start"}`)
	if msg := readUntilEvent(t, wsConn, "start"); msg.TimeLimit != 5 {
		t.Fatalf("startのtime_limitが不正です: %d", msg.TimeLimit)
	}

	sendJSON(t, wsConn, `{"action":"move","lat":34.7,"lng":135.5}`)
	waitFor(t, func() bool {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
			return false
		}
		return math.Abs(player.Lat-34.7) < 0.000001 && math.Abs(player.Lng-135.5) < 0.000001
	})

	readUntilEvent(t, wsConn, "game_active")
	if time.Since(startedAt) > 2500*time.Millisecond {
		t.Fatalf("grace_periodが秒単位で動いていません: elapsed=%s", time.Since(startedAt))
	}
}

func TestSyncIntervalUsesSeconds(t *testing.T) {
	roomID := "syncSecondsRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    4,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  0,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#00AAFF"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn, `{"action":"move","lat":34.7,"lng":135.5}`)
	sendJSON(t, wsConn, `{"action":"start"}`)
	readUntilEvent(t, wsConn, "start")
	readUntilEvent(t, wsConn, "game_active")

	msg := readUntilEvent(t, wsConn, "sync")
	location, ok := findLocation(msg.Locations, "player1")
	if !ok {
		t.Fatalf("syncにplayer1の位置情報が含まれていません: %+v", msg.Locations)
	}
	if math.Abs(location.Lat-34.7) > 0.000001 || math.Abs(location.Lng-135.5) > 0.000001 {
		t.Fatalf("syncの位置情報が不正です: %+v", location)
	}
	if location.Color != "#00AAFF" {
		t.Fatalf("syncのカラー情報が不正です: %+v", location)
	}
}

func TestInvalidJSONReturnsErrorToSender(t *testing.T) {
	roomID := "invalidJSONRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join"`)
	assertErrorMessage(t, wsConn, "送信データの形式が正しくありません")
}

func TestUnknownActionReturnsError(t *testing.T) {
	roomID := "unknownActionRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"dance"}`)
	assertErrorMessage(t, wsConn, "対応していない操作です")
}

func TestCaptureRequestMissingTargetReturnsErrorOnlyToSender(t *testing.T) {
	roomID := "captureErrorRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	defer wsConn1.Close()
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn1, `{"action":"capture_request","target_id":"missing"}`)
	assertErrorMessage(t, wsConn1, "捕まえる相手が見つかりません")
	assertNoMessage(t, wsConn2)
}

func TestTimeLimitEndsGameWithResult(t *testing.T) {
	roomID := "timeLimitRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    1,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  0,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn, `{"action":"start"}`)
	readUntilEvent(t, wsConn, "start")
	readUntilEvent(t, wsConn, "game_active")

	msg := readUntilEvent(t, wsConn, "result")
	if len(msg.Survivors) != 0 {
		t.Fatalf("鬼のみの部屋に生存逃走者が含まれています: %+v", msg.Survivors)
	}

	result, ok := findResult(msg.Results, "player1")
	if !ok {
		t.Fatalf("resultにplayer1が含まれていません: %+v", msg.Results)
	}
	if result.Name != "はるき" || result.Role != 1 || result.IsCaught {
		t.Fatalf("player1の結果が不正です: %+v", result)
	}

	waitFor(t, func() bool {
		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			return false
		}
		return room.Status == 2
	})
}

func TestAllRunnersCaughtEndsGameWithResult(t *testing.T) {
	roomID := "allCaughtRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    20,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  0,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	defer wsConn1.Close()
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn1, `{"action":"start"}`)
	start1 := readUntilEvent(t, wsConn1, "start")
	start2 := readUntilEvent(t, wsConn2, "start")

	var runnerConn *websocket.Conn
	runnerID := ""
	if start1.Role != nil && *start1.Role == 0 {
		runnerConn = wsConn1
		runnerID = "player1"
	}
	if start2.Role != nil && *start2.Role == 0 {
		runnerConn = wsConn2
		runnerID = "player2"
	}
	if runnerConn == nil {
		t.Fatalf("逃走者が割り当てられていません: role1=%v role2=%v", start1.Role, start2.Role)
	}

	readUntilEvent(t, runnerConn, "game_active")
	sendJSON(t, runnerConn, `{"action":"capture_response","approved":true}`)
	readUntilEvent(t, runnerConn, "captured")

	msg := readUntilEvent(t, runnerConn, "result")
	if len(msg.Survivors) != 0 {
		t.Fatalf("全員確保後に生存逃走者が含まれています: %+v", msg.Survivors)
	}

	result, ok := findResult(msg.Results, runnerID)
	if !ok {
		t.Fatalf("resultに逃走者が含まれていません: %+v", msg.Results)
	}
	if result.Role != 0 || !result.IsCaught {
		t.Fatalf("逃走者の結果が不正です: %+v", result)
	}

	waitFor(t, func() bool {
		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			return false
		}
		return room.Status == 2
	})
}

func TestMoveAndCaptureSavePlayerState(t *testing.T) {
	roomID := "stateRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn, `{"action":"move","lat":34.7,"lng":135.5}`)
	waitFor(t, func() bool {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
			return false
		}
		return math.Abs(player.Lat-34.7) < 0.000001 && math.Abs(player.Lng-135.5) < 0.000001
	})

	sendJSON(t, wsConn, `{"action":"capture_response","approved":true}`)
	if msg := readMessage(t, wsConn); msg.Event != "captured" || msg.TargetID != "player1" || !msg.Approved {
		t.Fatalf("捕獲通知が不正です: %+v", msg)
	}

	waitFor(t, func() bool {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
			return false
		}
		return player.IsCaught
	})
}
