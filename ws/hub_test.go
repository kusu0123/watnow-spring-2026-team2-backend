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

func readRawMessage(t *testing.T, wsConn *websocket.Conn) OutgoingMessage {
	t.Helper()

	return readRawMessageWithin(t, wsConn, 2*time.Second)
}

func readRawMessageWithin(t *testing.T, wsConn *websocket.Conn, timeout time.Duration) OutgoingMessage {
	t.Helper()

	wsConn.SetReadDeadline(time.Now().Add(timeout))
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

func readMessageWithin(t *testing.T, wsConn *websocket.Conn, timeout time.Duration) OutgoingMessage {
	t.Helper()

	for i := 0; i < 8; i++ {
		msg := readRawMessageWithin(t, wsConn, timeout)
		if msg.Event != "room_settings" {
			return msg
		}
	}

	t.Fatal("room_settings以外のイベントを受信できませんでした")
	return OutgoingMessage{}
}

func readMessage(t *testing.T, wsConn *websocket.Conn) OutgoingMessage {
	t.Helper()

	for i := 0; i < 8; i++ {
		msg := readRawMessage(t, wsConn)
		if msg.Event != "room_settings" {
			return msg
		}
	}

	t.Fatal("room_settings以外のイベントを受信できませんでした")
	return OutgoingMessage{}
}

func readRawUntilEvent(t *testing.T, wsConn *websocket.Conn, event string) OutgoingMessage {
	t.Helper()

	for i := 0; i < 8; i++ {
		msg := readRawMessage(t, wsConn)
		if msg.Event == event {
			return msg
		}
	}

	t.Fatalf("%s イベントを受信できませんでした", event)
	return OutgoingMessage{}
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

	assertNoMessageFor(t, wsConn, 200*time.Millisecond)
}

func assertNoMessageFor(t *testing.T, wsConn *websocket.Conn, duration time.Duration) {
	t.Helper()

	deadline := time.Now().Add(duration)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		if remaining > 200*time.Millisecond {
			remaining = 200 * time.Millisecond
		}

		wsConn.SetReadDeadline(time.Now().Add(remaining))
		_, payload, err := wsConn.ReadMessage()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			t.Fatalf("想定外の読み取りエラーです: %v", err)
		}

		var msg OutgoingMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatalf("JSONパース失敗: %v", err)
		}
		if msg.Event != "room_settings" {
			t.Fatalf("通知されないはずのクライアントがメッセージを受信しました: %+v", msg)
		}
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

func assertLocationMeta(t *testing.T, location LocationVal, roomID, userID, name string, role int, isCaught bool, color string) {
	t.Helper()

	if location.PlayerID != makePlayerID(roomID, userID) || location.UserID != userID || location.Name != name || location.Role != role || location.IsCaught != isCaught || location.Color != color {
		t.Fatalf("syncのプレイヤー情報が不正です: %+v", location)
	}
}

func assertLocationCoords(t *testing.T, location LocationVal, lat, lng float64) {
	t.Helper()

	if location.Lat == nil || location.Lng == nil {
		t.Fatalf("syncの座標が省略されています: %+v", location)
	}
	if math.Abs(*location.Lat-lat) > 0.000001 || math.Abs(*location.Lng-lng) > 0.000001 {
		t.Fatalf("syncの座標が不正です: %+v", location)
	}
}

func assertLocationNoCoords(t *testing.T, location LocationVal) {
	t.Helper()

	if location.Lat != nil || location.Lng != nil {
		t.Fatalf("syncの座標は省略される想定です: %+v", location)
	}
}

func findWaitingPlayer(players []WaitingPlayerVal, userID string) (WaitingPlayerVal, bool) {
	for _, player := range players {
		if player.UserID == userID {
			return player, true
		}
	}
	return WaitingPlayerVal{}, false
}

func assertWaitingPlayer(t *testing.T, msg OutgoingMessage, userID, name, color string) {
	t.Helper()

	player, ok := findWaitingPlayer(msg.Players, userID)
	if !ok {
		t.Fatalf("waitingに%sが含まれていません: %+v", userID, msg.Players)
	}
	if player.Name != name || player.Color != color {
		t.Fatalf("waitingのプレイヤー情報が不正です: %+v", player)
	}
}

func assertRoomSettings(t *testing.T, msg OutgoingMessage, timeLimit, oniCount int, areaSize string, syncInterval, gracePeriod int, areaCenter *AreaCenterVal) {
	t.Helper()

	if msg.Event != "room_settings" {
		t.Fatalf("room_settingsではないイベントを受信しました: %+v", msg)
	}
	if msg.TimeLimit != timeLimit || msg.OniCount != oniCount || msg.AreaSize != areaSize || msg.SyncInterval != syncInterval || msg.GracePeriod != gracePeriod {
		t.Fatalf("room_settingsの設定値が不正です: %+v", msg)
	}
	if areaCenter == nil {
		if msg.AreaCenter != nil {
			t.Fatalf("area_centerはnull想定です: %+v", msg.AreaCenter)
		}
		return
	}
	if msg.AreaCenter == nil || math.Abs(msg.AreaCenter.Lat-areaCenter.Lat) > 0.000001 || math.Abs(msg.AreaCenter.Lng-areaCenter.Lng) > 0.000001 {
		t.Fatalf("area_centerが不正です: %+v", msg.AreaCenter)
	}
}

func roomExists(roomID string) bool {
	GameHub.mu.RLock()
	defer GameHub.mu.RUnlock()

	_, ok := GameHub.Rooms[roomID]
	return ok
}

func assertDBPlayerExists(t *testing.T, db *gorm.DB, roomID, userID string, want bool) {
	t.Helper()

	var count int64
	if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, userID).Count(&count).Error; err != nil {
		t.Fatalf("プレイヤー件数取得失敗: %v", err)
	}
	if got := count > 0; got != want {
		t.Fatalf("DB player存在有無が不正です: user_id=%s got=%v want=%v", userID, got, want)
	}
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
	assertWaitingPlayer(t, msg, "player1", "はるき", "#0000FF")

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

func TestJoinSendsRoomSettingsToJoiningClient(t *testing.T) {
	roomID := "joinSettingsRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:            roomID,
		Status:        0,
		TimeLimit:     900,
		OniCount:      1,
		AreaSize:      "500m",
		SyncInterval:  180,
		GracePeriod:   120,
		AreaCenterLat: 34.0,
		AreaCenterLng: 135.0,
		HasAreaCenter: true,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#0000FF"}`)
	if msg := readRawMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	settings := readRawMessage(t, wsConn)
	assertRoomSettings(t, settings, 900, 1, "500m", 180, 120, &AreaCenterVal{Lat: 34.0, Lng: 135.0})
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
	assertWaitingPlayer(t, msg, "player1", "新しい名前", "red")

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
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#00AAFF"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな","color":"#FF00AA"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	GameHub.UpdateRoomSettings(roomID, 120, 1, "school-yard", 1, 0)

	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.RLock()
	if room.AreaSize != "school-yard" || room.SyncInterval != 1 || room.GracePeriod != 0 {
		room.mu.RUnlock()
		t.Fatalf("設定反映失敗: area_size=%s sync_interval=%d grace_period=%d", room.AreaSize, room.SyncInterval, room.GracePeriod)
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
			if len(msg.OniUsers) != 1 || msg.OniUsers[0] != "player1" {
				t.Fatalf("startのoni_usersが不正です: %+v", msg.OniUsers)
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
	var runner models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player2").First(&runner).Error; err != nil {
		t.Fatalf("逃走者取得失敗: %v", err)
	}
	if runner.Role != 0 || runner.Color != "#FF00AA" {
		t.Fatalf("DBに保存された逃走者状態が不正です: %+v", runner)
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
	} else {
		assertWaitingPlayer(t, msg, "player1", "はるき", "#00AAFF")
	}

	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな","color":"#FF00AA"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	} else {
		if len(msg.Players) != 2 || msg.Players[0].UserID != "player1" || msg.Players[1].UserID != "player2" {
			t.Fatalf("waitingのplayers順序が不正です: %+v", msg.Players)
		}
		assertWaitingPlayer(t, msg, "player1", "はるき", "#00AAFF")
		assertWaitingPlayer(t, msg, "player2", "みな", "#FF00AA")
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	} else {
		assertWaitingPlayer(t, msg, "player1", "はるき", "#00AAFF")
		assertWaitingPlayer(t, msg, "player2", "みな", "#FF00AA")
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
	if len(start1.OniUsers) != 1 || start1.OniUsers[0] != "player2" || len(start2.OniUsers) != 1 || start2.OniUsers[0] != "player2" {
		t.Fatalf("startのoni_usersが不正です: player1=%+v player2=%+v", start1.OniUsers, start2.OniUsers)
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
			wsConn2 := connectToRoom(t, baseURL, roomID)
			defer wsConn2.Close()

			sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#00AAFF"}`)
			if msg := readMessage(t, wsConn); msg.Event != "waiting" {
				t.Fatalf("想定外のイベント: %s", msg.Event)
			}
			sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな","color":"#FF00AA"}`)
			if msg := readMessage(t, wsConn); msg.Event != "waiting" {
				t.Fatalf("想定外のイベント: %s", msg.Event)
			}
			if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
				t.Fatalf("想定外のイベント: %s", msg.Event)
			}

			sendJSON(t, wsConn, tt.startBody)
			if msg := readUntilEvent(t, wsConn, "error"); msg.Message != tt.wantMessage {
				t.Fatalf("エラー通知が不正です: %+v", msg)
			}

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
	wsConn2 := connectToRoom(t, baseURL, roomID)

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	} else {
		assertWaitingPlayer(t, msg, "player1", "はるき", "")
	}
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
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
	_ = wsConn2.Close()
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
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
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

func TestViewerSpecificSyncPayloadAndImmediateFirstSync(t *testing.T) {
	roomID := "viewerSyncRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    120,
		OniCount:     1,
		SyncInterval: 30,
		GracePeriod:  0,
	})
	defer cleanup()

	oniConn := connectToRoom(t, baseURL, roomID)
	defer oniConn.Close()
	runnerConn := connectToRoom(t, baseURL, roomID)
	defer runnerConn.Close()
	otherRunnerConn := connectToRoom(t, baseURL, roomID)
	defer otherRunnerConn.Close()

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#00AAFF"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな","color":"#FF00AA"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, otherRunnerConn, `{"action":"join","user_id":"player3","name":"そうた","color":"#00CC66"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, otherRunnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, oniConn, `{"action":"move","lat":34.7,"lng":135.5}`)
	waitFor(t, func() bool {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
			return false
		}
		return math.Abs(player.Lat-34.7) < 0.000001 && math.Abs(player.Lng-135.5) < 0.000001
	})
	sendJSON(t, runnerConn, `{"action":"move","lat":34.8,"lng":135.6}`)
	waitFor(t, func() bool {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, "player2").First(&player).Error; err != nil {
			return false
		}
		return math.Abs(player.Lat-34.8) < 0.000001 && math.Abs(player.Lng-135.6) < 0.000001
	})
	sendJSON(t, otherRunnerConn, `{"action":"move","lat":34.9,"lng":135.7}`)
	waitFor(t, func() bool {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, "player3").First(&player).Error; err != nil {
			return false
		}
		return math.Abs(player.Lat-34.9) < 0.000001 && math.Abs(player.Lng-135.7) < 0.000001
	})

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	readUntilEvent(t, otherRunnerConn, "start")

	readUntilEvent(t, oniConn, "game_active")
	readUntilEvent(t, runnerConn, "game_active")
	readUntilEvent(t, otherRunnerConn, "game_active")

	oniSync := readUntilEvent(t, oniConn, "sync")
	if len(oniSync.Locations) != 2 {
		t.Fatalf("鬼には未捕獲逃走者2人だけが届く想定です: %+v", oniSync.Locations)
	}
	if _, ok := findLocation(oniSync.Locations, "player1"); ok {
		t.Fatalf("鬼向けsyncに鬼自身が含まれています: %+v", oniSync.Locations)
	}
	runnerLocation, ok := findLocation(oniSync.Locations, "player2")
	if !ok {
		t.Fatalf("鬼向けsyncに未捕獲逃走者player2が含まれていません: %+v", oniSync.Locations)
	}
	assertLocationMeta(t, runnerLocation, roomID, "player2", "みな", 0, false, "#FF00AA")
	assertLocationCoords(t, runnerLocation, 34.8, 135.6)
	otherRunnerLocation, ok := findLocation(oniSync.Locations, "player3")
	if !ok {
		t.Fatalf("鬼向けsyncに未捕獲逃走者player3が含まれていません: %+v", oniSync.Locations)
	}
	assertLocationMeta(t, otherRunnerLocation, roomID, "player3", "そうた", 0, false, "#00CC66")
	assertLocationCoords(t, otherRunnerLocation, 34.9, 135.7)

	runnerSync := readUntilEvent(t, runnerConn, "sync")
	if len(runnerSync.Locations) != 1 {
		t.Fatalf("逃走者には自分の状態だけが届く想定です: %+v", runnerSync.Locations)
	}
	selfLocation, ok := findLocation(runnerSync.Locations, "player2")
	if !ok {
		t.Fatalf("逃走者向けsyncに自分の状態が含まれていません: %+v", runnerSync.Locations)
	}
	assertLocationMeta(t, selfLocation, roomID, "player2", "みな", 0, false, "#FF00AA")
	assertLocationCoords(t, selfLocation, 34.8, 135.6)
	if _, ok := findLocation(runnerSync.Locations, "player1"); ok {
		t.Fatalf("逃走者向けsyncに他プレイヤーが含まれています: %+v", runnerSync.Locations)
	}

	sendJSON(t, runnerConn, `{"action":"capture_response","approved":true}`)
	readUntilEvent(t, oniConn, "captured")
	readUntilEvent(t, runnerConn, "captured")
	readUntilEvent(t, otherRunnerConn, "captured")

	room := GameHub.GetOrCreateRoom(roomID)
	room.SendSyncToAll()

	oniSyncAfterCapture := readUntilEvent(t, oniConn, "sync")
	if len(oniSyncAfterCapture.Locations) != 1 {
		t.Fatalf("捕獲後の鬼向けsyncは未捕獲逃走者1人だけの想定です: %+v", oniSyncAfterCapture.Locations)
	}
	if _, ok := findLocation(oniSyncAfterCapture.Locations, "player2"); ok {
		t.Fatalf("捕獲済み逃走者が鬼向けsyncに含まれています: %+v", oniSyncAfterCapture.Locations)
	}
	remainingRunnerLocation, ok := findLocation(oniSyncAfterCapture.Locations, "player3")
	if !ok {
		t.Fatalf("未捕獲逃走者が鬼向けsyncに含まれていません: %+v", oniSyncAfterCapture.Locations)
	}
	assertLocationMeta(t, remainingRunnerLocation, roomID, "player3", "そうた", 0, false, "#00CC66")
	assertLocationCoords(t, remainingRunnerLocation, 34.9, 135.7)

	caughtRunnerSync := readUntilEvent(t, runnerConn, "sync")
	if len(caughtRunnerSync.Locations) != 1 {
		t.Fatalf("捕獲済み逃走者には自分の状態だけが届く想定です: %+v", caughtRunnerSync.Locations)
	}
	caughtSelfLocation, ok := findLocation(caughtRunnerSync.Locations, "player2")
	if !ok {
		t.Fatalf("捕獲済み逃走者向けsyncに自分の状態が含まれていません: %+v", caughtRunnerSync.Locations)
	}
	assertLocationMeta(t, caughtSelfLocation, roomID, "player2", "みな", 0, true, "#FF00AA")
	assertLocationNoCoords(t, caughtSelfLocation)

	otherRunnerSync := readUntilEvent(t, otherRunnerConn, "sync")
	if len(otherRunnerSync.Locations) != 1 {
		t.Fatalf("未捕獲逃走者には自分の状態だけが届く想定です: %+v", otherRunnerSync.Locations)
	}
	otherSelfLocation, ok := findLocation(otherRunnerSync.Locations, "player3")
	if !ok {
		t.Fatalf("未捕獲逃走者向けsyncに自分の状態が含まれていません: %+v", otherRunnerSync.Locations)
	}
	assertLocationMeta(t, otherSelfLocation, roomID, "player3", "そうた", 0, false, "#00CC66")
	assertLocationCoords(t, otherSelfLocation, 34.9, 135.7)
}

func TestResetAfterResultKeepsConnectedPlayersAndAllowsRestart(t *testing.T) {
	roomID := "resetReplayRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    1,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  0,
	})
	defer cleanup()

	oniConn := connectToRoom(t, baseURL, roomID)
	defer oniConn.Close()
	runnerConn := connectToRoom(t, baseURL, roomID)
	defer runnerConn.Close()
	disconnectedConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, oniConn, "waiting")
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, disconnectedConn, `{"action":"join","user_id":"player3","name":"そうた"}`)
	readUntilEvent(t, oniConn, "waiting")
	readUntilEvent(t, runnerConn, "waiting")
	if msg := readMessage(t, disconnectedConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	readUntilEvent(t, disconnectedConn, "start")
	readUntilEvent(t, oniConn, "game_active")
	readUntilEvent(t, runnerConn, "game_active")
	readUntilEvent(t, disconnectedConn, "game_active")

	if err := disconnectedConn.Close(); err != nil {
		t.Fatalf("切断失敗: %v", err)
	}
	room := GameHub.GetOrCreateRoom(roomID)
	waitFor(t, func() bool {
		_, ok := findClient(room, "player3")
		return !ok
	})

	readUntilEvent(t, oniConn, "result")
	sendJSON(t, oniConn, `{"action":"reset"}`)
	resetWaiting := readUntilEvent(t, oniConn, "waiting")
	if len(resetWaiting.Players) != 2 {
		t.Fatalf("reset後は接続中2人だけがwaitingに戻る想定です: %+v", resetWaiting.Players)
	}
	assertWaitingPlayer(t, resetWaiting, "player1", "はるき", "black")
	assertWaitingPlayer(t, resetWaiting, "player2", "みな", "")
	readUntilEvent(t, runnerConn, "waiting")

	waitFor(t, func() bool {
		var savedRoom models.Room
		if err := db.First(&savedRoom, "id = ?", roomID).Error; err != nil {
			return false
		}
		return savedRoom.Status == 0
	})
	assertDBPlayerExists(t, db, roomID, "player3", false)

	for _, userID := range []string{"player1", "player2"} {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).First(&player).Error; err != nil {
			t.Fatalf("reset後のプレイヤー取得失敗: %v", err)
		}
		if player.Role != 0 || player.IsCaught {
			t.Fatalf("reset後のプレイヤー状態が不正です: %+v", player)
		}
	}

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	if msg := readUntilEvent(t, oniConn, "start"); msg.Role == nil || *msg.Role != 1 {
		t.Fatalf("reset後の再startが不正です: %+v", msg)
	}
}

func TestJoinRejectsNewPlayerDuringActiveAndAllowsExistingReconnect(t *testing.T) {
	roomID := "activeJoinRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    120,
		OniCount:     1,
		SyncInterval: 30,
		GracePeriod:  0,
	})
	defer cleanup()

	oniConn := connectToRoom(t, baseURL, roomID)
	defer oniConn.Close()
	runnerConn := connectToRoom(t, baseURL, roomID)
	defer runnerConn.Close()

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, oniConn, "waiting")
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	readUntilEvent(t, oniConn, "game_active")
	readUntilEvent(t, runnerConn, "game_active")

	newConn := connectToRoom(t, baseURL, roomID)
	defer newConn.Close()
	sendJSON(t, newConn, `{"action":"join","user_id":"player3","name":"そうた"}`)
	assertErrorMessage(t, newConn, "ゲーム中または終了後の新規参加はできません")

	reconnectConn := connectToRoom(t, baseURL, roomID)
	defer reconnectConn.Close()
	sendJSON(t, reconnectConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readUntilEvent(t, reconnectConn, "start"); msg.Role == nil || *msg.Role != 0 {
		t.Fatalf("active中の既存user復帰が不正です: %+v", msg)
	}
	readUntilEvent(t, reconnectConn, "game_active")
	readUntilEvent(t, reconnectConn, "sync")
}

func TestJoinRejectsSixteenthPlayer(t *testing.T) {
	roomID := "maxPlayersRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	conns := make([]*websocket.Conn, 0, maxRoomPlayers)
	for i := 1; i <= maxRoomPlayers; i++ {
		conn := connectToRoom(t, baseURL, roomID)
		defer conn.Close()
		conns = append(conns, conn)
		sendJSON(t, conn, `{"action":"join","user_id":"player`+string(rune('A'+i-1))+`","name":"player"}`)
		if msg := readMessage(t, conn); msg.Event != "waiting" {
			t.Fatalf("%d人目のjoinイベントが不正です: %+v", i, msg)
		}
	}

	var count int64
	if err := db.Model(&models.Player{}).Where("room_id = ?", roomID).Count(&count).Error; err != nil {
		t.Fatalf("プレイヤー件数取得失敗: %v", err)
	}
	if count != maxRoomPlayers {
		t.Fatalf("15人join後のDB件数が不正です: %d", count)
	}

	extraConn := connectToRoom(t, baseURL, roomID)
	defer extraConn.Close()
	sendJSON(t, extraConn, `{"action":"join","user_id":"playerZ","name":"extra"}`)
	assertErrorMessage(t, extraConn, "参加人数は15人までです")
}

func TestStartRejectsTooFewPlayers(t *testing.T) {
	roomID := "tooFewStartRoom"
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

	sendJSON(t, wsConn, `{"action":"start","oni_users":["player1"]}`)
	assertErrorMessage(t, wsConn, "ゲーム開始には2人以上必要です")

	var room models.Room
	if err := db.First(&room, "id = ?", roomID).Error; err != nil {
		t.Fatalf("ルーム取得失敗: %v", err)
	}
	if room.Status != 0 {
		t.Fatalf("不正なstartでDBのroom statusが更新されています: %d", room.Status)
	}
}

func TestStartRejectsInvalidOniSelectionRules(t *testing.T) {
	tests := []struct {
		name        string
		playerCount int
		oniCount    int
		startBody   string
		wantMessage string
	}{
		{
			name:        "duplicateOniUsers",
			playerCount: 3,
			oniCount:    1,
			startBody:   `{"action":"start","oni_users":["player1","player1"]}`,
			wantMessage: "鬼に指定されたユーザーが重複しています",
		},
		{
			name:        "tooManyOniUsers",
			playerCount: 5,
			oniCount:    1,
			startBody:   `{"action":"start","oni_users":["player1","player2","player3","player4"]}`,
			wantMessage: "鬼は1〜3人で指定してください",
		},
		{
			name:        "allPlayersOni",
			playerCount: 2,
			oniCount:    2,
			startBody:   `{"action":"start","oni_users":["player1","player2"]}`,
			wantMessage: "全員を鬼にはできません",
		},
		{
			name:        "oniCountMismatch",
			playerCount: 3,
			oniCount:    2,
			startBody:   `{"action":"start","oni_users":["player1"]}`,
			wantMessage: "設定された鬼の人数と一致しません",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roomID := "invalidOni" + tt.name
			db, baseURL, cleanup := newTestServer(t, models.Room{
				ID:           roomID,
				Status:       0,
				TimeLimit:    900,
				OniCount:     tt.oniCount,
				SyncInterval: 1,
				GracePeriod:  0,
			})
			defer cleanup()

			conns := make([]*websocket.Conn, 0, tt.playerCount)
			for i := 1; i <= tt.playerCount; i++ {
				conn := connectToRoom(t, baseURL, roomID)
				defer conn.Close()
				conns = append(conns, conn)
				userID := "player" + string(rune('0'+i))
				sendJSON(t, conn, `{"action":"join","user_id":"`+userID+`","name":"player"}`)
				if msg := readMessage(t, conn); msg.Event != "waiting" {
					t.Fatalf("%d人目のjoinイベントが不正です: %+v", i, msg)
				}
			}

			sendJSON(t, conns[0], tt.startBody)
			if msg := readUntilEvent(t, conns[0], "error"); msg.Message != tt.wantMessage {
				t.Fatalf("エラー通知が不正です: %+v", msg)
			}

			var room models.Room
			if err := db.First(&room, "id = ?", roomID).Error; err != nil {
				t.Fatalf("ルーム取得失敗: %v", err)
			}
			if room.Status != 0 {
				t.Fatalf("不正なstartでDBのroom statusが更新されています: %d", room.Status)
			}
		})
	}
}

func TestLeaveDeletesPlayerInWaiting(t *testing.T) {
	roomID := "waitingLeaveRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, wsConn1, "waiting")
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn1, `{"action":"leave"}`)
	waiting := readUntilEvent(t, wsConn2, "waiting")
	if _, ok := findWaitingPlayer(waiting.Players, "player1"); ok {
		t.Fatalf("leave済みplayerがwaitingに残っています: %+v", waiting.Players)
	}
	assertDBPlayerExists(t, db, roomID, "player1", false)
}

func TestLeaveKeepsDBPlayerDuringActive(t *testing.T) {
	roomID := "activeLeaveRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    120,
		OniCount:     1,
		SyncInterval: 30,
		GracePeriod:  0,
	})
	defer cleanup()

	oniConn := connectToRoom(t, baseURL, roomID)
	defer oniConn.Close()
	runnerConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, oniConn, "waiting")
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	readUntilEvent(t, oniConn, "game_active")
	readUntilEvent(t, runnerConn, "game_active")

	sendJSON(t, runnerConn, `{"action":"leave"}`)
	assertDBPlayerExists(t, db, roomID, "player2", true)

	room := GameHub.GetOrCreateRoom(roomID)
	waitFor(t, func() bool {
		_, ok := findClient(room, "player2")
		return !ok
	})
}

func TestLeaveDeletesPlayerInResult(t *testing.T) {
	roomID := "resultLeaveRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    120,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  0,
	})
	defer cleanup()

	oniConn := connectToRoom(t, baseURL, roomID)
	defer oniConn.Close()
	runnerConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, oniConn, "waiting")
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	readUntilEvent(t, runnerConn, "game_active")
	sendJSON(t, runnerConn, `{"action":"capture_response","approved":true}`)
	readUntilEvent(t, oniConn, "captured")
	readUntilEvent(t, runnerConn, "captured")
	readUntilEvent(t, oniConn, "result")
	readUntilEvent(t, runnerConn, "result")

	sendJSON(t, runnerConn, `{"action":"leave"}`)
	result := readUntilEvent(t, oniConn, "result")
	assertDBPlayerExists(t, db, roomID, "player2", false)
	if _, ok := findResult(result.Results, "player2"); ok {
		t.Fatalf("leave済みplayerがresultに残っています: %+v", result.Results)
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

	if err := db.Create(&[]models.Player{
		{ID: makePlayerID(roomID, "player1"), RoomID: roomID, UserID: "player1", Name: "はるき", Role: 1},
		{ID: makePlayerID(roomID, "player2"), RoomID: roomID, UserID: "player2", Name: "みな", Role: 0},
	}).Error; err != nil {
		t.Fatalf("既存プレイヤー作成失敗: %v", err)
	}

	wsConn1 := connectToRoom(t, baseURL, roomID)
	defer wsConn1.Close()
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readUntilEvent(t, wsConn1, "start"); msg.Role == nil || *msg.Role != 1 {
		t.Fatalf("active復帰時のstartが不正です: %+v", msg)
	}
	readUntilEvent(t, wsConn1, "game_active")
	readUntilEvent(t, wsConn1, "sync")

	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readUntilEvent(t, wsConn2, "start"); msg.Role == nil || *msg.Role != 0 {
		t.Fatalf("active復帰時のstartが不正です: %+v", msg)
	}
	readUntilEvent(t, wsConn2, "game_active")
	readUntilEvent(t, wsConn2, "sync")

	sendJSON(t, wsConn1, `{"action":"capture_request","target_id":"missing"}`)
	if msg := readUntilEvent(t, wsConn1, "error"); msg.Message != "対象の逃走者が見つからないか、すでに捕まっています" {
		t.Fatalf("エラー通知が不正です: %+v", msg)
	}
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
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, wsConn, "start")
	readUntilEvent(t, wsConn, "game_active")

	msg := readUntilEvent(t, wsConn, "result")
	if len(msg.Survivors) != 1 || msg.Survivors[0] != "player2" {
		t.Fatalf("時間切れ後の生存逃走者が不正です: %+v", msg.Survivors)
	}

	result, ok := findResult(msg.Results, "player1")
	if !ok {
		t.Fatalf("resultにplayer1が含まれていません: %+v", msg.Results)
	}
	if result.Name != "はるき" || result.Role != 1 || result.IsCaught {
		t.Fatalf("player1の結果が不正です: %+v", result)
	}
	runnerResult, ok := findResult(msg.Results, "player2")
	if !ok {
		t.Fatalf("resultにplayer2が含まれていません: %+v", msg.Results)
	}
	if runnerResult.Name != "みな" || runnerResult.Role != 0 || runnerResult.IsCaught {
		t.Fatalf("player2の結果が不正です: %+v", runnerResult)
	}

	waitFor(t, func() bool {
		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			return false
		}
		return room.Status == 2
	})
}

func TestTimeLimitResultIncludesDisconnectedPlayersFromDB(t *testing.T) {
	roomID := "disconnectedResultRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    1,
		OniCount:     1,
		SyncInterval: 1,
		GracePeriod:  0,
	})
	defer cleanup()

	oniConn := connectToRoom(t, baseURL, roomID)
	defer oniConn.Close()
	runnerConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	if msg := readUntilEvent(t, oniConn, "start"); msg.Role == nil || *msg.Role != 1 {
		t.Fatalf("player1は鬼になる想定です: %+v", msg)
	}
	if msg := readUntilEvent(t, runnerConn, "start"); msg.Role == nil || *msg.Role != 0 {
		t.Fatalf("player2は逃走者になる想定です: %+v", msg)
	}

	if err := runnerConn.Close(); err != nil {
		t.Fatalf("切断失敗: %v", err)
	}

	room := GameHub.GetOrCreateRoom(roomID)
	waitFor(t, func() bool {
		_, ok := findClient(room, "player2")
		return !ok
	})

	readUntilEvent(t, oniConn, "game_active")
	msg := readUntilEvent(t, oniConn, "result")

	result, ok := findResult(msg.Results, "player2")
	if !ok {
		t.Fatalf("切断済みプレイヤーがresultに含まれていません: %+v", msg.Results)
	}
	if result.Name != "みな" || result.Role != 0 || result.IsCaught {
		t.Fatalf("切断済みプレイヤーの結果が不正です: %+v", result)
	}
	if len(msg.Survivors) != 1 || msg.Survivors[0] != "player2" {
		t.Fatalf("切断済み逃走者がsurvivorsに含まれていません: %+v", msg.Survivors)
	}

	waitFor(t, func() bool {
		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			return false
		}
		return room.Status == 2
	})
}

func TestAllRunnersCaughtImmediatelyEndsGameWithResultAndAllowsReset(t *testing.T) {
	roomID := "allCaughtRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    20,
		OniCount:     1,
		SyncInterval: 30,
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

	sendJSON(t, wsConn1, `{"action":"move","lat":34.7,"lng":135.5}`)
	waitFor(t, func() bool {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
			return false
		}
		return math.Abs(player.Lat-34.7) < 0.000001 && math.Abs(player.Lng-135.5) < 0.000001
	})
	sendJSON(t, wsConn2, `{"action":"move","lat":34.8,"lng":135.6}`)
	waitFor(t, func() bool {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, "player2").First(&player).Error; err != nil {
			return false
		}
		return math.Abs(player.Lat-34.8) < 0.000001 && math.Abs(player.Lng-135.6) < 0.000001
	})

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
	readUntilEvent(t, wsConn1, "game_active")
	readUntilEvent(t, runnerConn, "game_active")
	readUntilEvent(t, wsConn1, "sync")
	readUntilEvent(t, runnerConn, "sync")
	sendJSON(t, runnerConn, `{"action":"capture_response","approved":true}`)
	if msg := readMessageWithin(t, runnerConn, 500*time.Millisecond); msg.Event != "captured" || msg.TargetID != runnerID || !msg.Approved {
		t.Fatalf("captured通知が不正です: %+v", msg)
	}

	msg := readMessageWithin(t, runnerConn, 500*time.Millisecond)
	if msg.Event != "result" {
		t.Fatalf("captured直後にresultが届く想定です: %+v", msg)
	}
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
	if oniResult, ok := findResult(msg.Results, "player1"); !ok || oniResult.Role != 1 || oniResult.IsCaught {
		t.Fatalf("鬼の結果が不正です: %+v", msg.Results)
	}

	if msg := readMessageWithin(t, wsConn1, 500*time.Millisecond); msg.Event != "captured" || msg.TargetID != runnerID || !msg.Approved {
		t.Fatalf("鬼側captured通知が不正です: %+v", msg)
	}
	if msg := readMessageWithin(t, wsConn1, 500*time.Millisecond); msg.Event != "result" {
		t.Fatalf("鬼側にcaptured直後のresultが届く想定です: %+v", msg)
	}

	waitFor(t, func() bool {
		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			return false
		}
		return room.Status == 2
	})

	sendJSON(t, wsConn1, `{"action":"reset"}`)
	resetWaiting := readUntilEvent(t, wsConn1, "waiting")
	if len(resetWaiting.Players) != 2 {
		t.Fatalf("reset後は接続中2人がwaitingに戻る想定です: %+v", resetWaiting.Players)
	}
	readUntilEvent(t, runnerConn, "waiting")

	waitFor(t, func() bool {
		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			return false
		}
		return room.Status == 0
	})
	for _, userID := range []string{"player1", "player2"} {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).First(&player).Error; err != nil {
			t.Fatalf("reset後のプレイヤー取得失敗: %v", err)
		}
		if player.Role != 0 || player.IsCaught || player.Lat != 0 || player.Lng != 0 {
			t.Fatalf("reset後のプレイヤー状態が不正です: %+v", player)
		}
	}
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
