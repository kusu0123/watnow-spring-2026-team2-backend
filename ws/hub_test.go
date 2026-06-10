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

func roomExists(roomID string) bool {
	GameHub.mu.RLock()
	defer GameHub.mu.RUnlock()

	_, ok := GameHub.Rooms[roomID]
	return ok
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

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#0000FF"}`)
	msg := readMessage(t, wsConn)
	if msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
		t.Fatalf("プレイヤー保存失敗: %v", err)
	}
	if player.ID != makePlayerID(roomID, "player1") || player.Name != "はるき" || player.Color != "#0000FF" {
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
	if color != "#0000FF" {
		t.Fatalf("メモリに保存されたカラーが不正です: %s", color)
	}

	_ = wsConn.Close()
	waitFor(t, func() bool {
		return !roomExists(roomID)
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

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#00AAFF"}`)
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

	sendJSON(t, wsConn, `{"action":"start","oni_users":["player1"]}`)

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
	if player.Role != 1 || player.Color != "black" {
		t.Fatalf("DBに保存されたプレイヤー状態が不正です: %+v", player)
	}
	client, ok := findClient(room, "player1")
	if !ok {
		t.Fatal("開始後のクライアントがメモリに存在しません")
	}
	client.mu.Lock()
	color := client.Color
	client.mu.Unlock()
	if color != "black" {
		t.Fatalf("メモリ上の鬼カラーが不正です: %s", color)
	}

	var startedRoom models.Room
	if err := db.First(&startedRoom, "id = ?", roomID).Error; err != nil {
		t.Fatalf("ルーム取得失敗: %v", err)
	}
	if startedRoom.Status != 1 {
		t.Fatalf("DBに保存されたルームstatusが不正です: %d", startedRoom.Status)
	}
}

func TestStartUsesDesignatedOniAndOverwritesOnlyOniColor(t *testing.T) {
	roomID := "designatedOniRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  0,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	defer wsConn1.Close()
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"はるき","color":"#00AAFF"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな","color":"#FF00AA"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn1, `{"action":"start","oni_users":["player2"]}`)
	start1 := readUntilEvent(t, wsConn1, "start")
	start2 := readUntilEvent(t, wsConn2, "start")
	if start1.Role == nil || *start1.Role != 0 {
		t.Fatalf("player1は逃走者になる想定です: %+v", start1)
	}
	if start2.Role == nil || *start2.Role != 1 {
		t.Fatalf("player2は鬼になる想定です: %+v", start2)
	}

	var runner models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&runner).Error; err != nil {
		t.Fatalf("逃走者取得失敗: %v", err)
	}
	if runner.Role != 0 || runner.Color != "#00AAFF" {
		t.Fatalf("逃走者のDB状態が不正です: %+v", runner)
	}

	var oni models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player2").First(&oni).Error; err != nil {
		t.Fatalf("鬼取得失敗: %v", err)
	}
	if oni.Role != 1 || oni.Color != "black" {
		t.Fatalf("鬼のDB状態が不正です: %+v", oni)
	}

	room := GameHub.GetOrCreateRoom(roomID)
	runnerClient, ok := findClient(room, "player1")
	if !ok {
		t.Fatal("逃走者クライアントがメモリに存在しません")
	}
	oniClient, ok := findClient(room, "player2")
	if !ok {
		t.Fatal("鬼クライアントがメモリに存在しません")
	}

	runnerClient.mu.Lock()
	runnerRole := runnerClient.Role
	runnerColor := runnerClient.Color
	runnerClient.mu.Unlock()
	if runnerRole != 0 || runnerColor != "#00AAFF" {
		t.Fatalf("逃走者のメモリ状態が不正です: role=%d color=%s", runnerRole, runnerColor)
	}

	oniClient.mu.Lock()
	oniRole := oniClient.Role
	oniColor := oniClient.Color
	oniClient.mu.Unlock()
	if oniRole != 1 || oniColor != "black" {
		t.Fatalf("鬼のメモリ状態が不正です: role=%d color=%s", oniRole, oniColor)
	}
}

func TestStartRequiresValidOniUsers(t *testing.T) {
	tests := []struct {
		name        string
		startBody   string
		wantMessage string
	}{
		{
			name:        "missingOniUsers",
			startBody:   `{"action":"start"}`,
			wantMessage: "鬼に指定するユーザーを1人以上選択してください",
		},
		{
			name:        "emptyOniUsers",
			startBody:   `{"action":"start","oni_users":[]}`,
			wantMessage: "鬼に指定するユーザーを1人以上選択してください",
		},
		{
			name:        "unknownOniUser",
			startBody:   `{"action":"start","oni_users":["missing"]}`,
			wantMessage: "鬼に指定されたユーザーが参加していません",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roomID := "invalidStart" + tt.name
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

			sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#00AAFF"}`)
			if msg := readMessage(t, wsConn); msg.Event != "waiting" {
				t.Fatalf("想定外のイベント: %s", msg.Event)
			}

			sendJSON(t, wsConn, tt.startBody)
			assertErrorMessage(t, wsConn, tt.wantMessage)

			var room models.Room
			if err := db.First(&room, "id = ?", roomID).Error; err != nil {
				t.Fatalf("ルーム取得失敗: %v", err)
			}
			if room.Status != 0 {
				t.Fatalf("不正なstartでDBのルームstatusが更新されています: %d", room.Status)
			}

			var player models.Player
			if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
				t.Fatalf("プレイヤー取得失敗: %v", err)
			}
			if player.Role != 0 || player.Color != "#00AAFF" {
				t.Fatalf("不正なstartでDBのプレイヤー状態が更新されています: %+v", player)
			}

			roomState := GameHub.GetOrCreateRoom(roomID)
			roomState.mu.RLock()
			status := roomState.Status
			isGMLoopActive := roomState.IsGMLoopActive
			roomState.mu.RUnlock()
			if status != 0 || isGMLoopActive {
				t.Fatalf("不正なstartでメモリのルーム状態が更新されています: status=%d gm=%v", status, isGMLoopActive)
			}
		})
	}
}

func TestActiveRoomIsFinishedAndRemovedWhenEmpty(t *testing.T) {
	roomID := "activeCleanupRoom"
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

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn, `{"action":"start","oni_users":["player1"]}`)
	if msg := readUntilEvent(t, wsConn, "start"); msg.TimeLimit != 900 {
		t.Fatalf("startのtime_limitが不正です: %d", msg.TimeLimit)
	}

	waitFor(t, func() bool {
		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			return false
		}
		return room.Status == 1
	})

	_ = wsConn.Close()
	waitFor(t, func() bool {
		return !roomExists(roomID)
	})

	waitFor(t, func() bool {
		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			return false
		}
		return room.Status == 2
	})
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
	sendJSON(t, wsConn, `{"action":"start","oni_users":["player1"]}`)
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
	sendJSON(t, wsConn, `{"action":"start","oni_users":["player1"]}`)
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
	if location.Color != "black" {
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
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    1, // ゲーム中にする
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

	// player1を「鬼」に強制設定する（DBとメモリ両方）
	db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, "player1").Update("role", 1)
	room := GameHub.GetOrCreateRoom(roomID)
	if client, ok := findClient(room, "player1"); ok {
		client.mu.Lock()
		client.Role = 1
		client.mu.Unlock()
	}

	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn1, `{"action":"capture_request","target_id":"missing"}`)
	assertErrorMessage(t, wsConn1, "対象の逃走者が見つからないか、すでに捕まっています")
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

	sendJSON(t, wsConn, `{"action":"start","oni_users":["player1"]}`)
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

	sendJSON(t, wsConn1, `{"action":"start","oni_users":["player1"]}`)
	start1 := readUntilEvent(t, wsConn1, "start")
	start2 := readUntilEvent(t, wsConn2, "start")
	if start1.Role == nil || *start1.Role != 1 {
		t.Fatalf("player1は鬼になる想定です: %+v", start1)
	}
	if start2.Role == nil || *start2.Role != 0 {
		t.Fatalf("player2は逃走者になる想定です: %+v", start2)
	}

	runnerConn := wsConn2
	runnerID := "player2"
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
