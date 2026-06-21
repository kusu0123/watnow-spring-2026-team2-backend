package ws

import (
	"encoding/json"
	"math"
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
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB取得失敗: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
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
		_ = sqlDB.Close()
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

func assertLocationNoCoords(t *testing.T, location LocationVal) {
	t.Helper()

	if location.Lat != nil || location.Lng != nil {
		t.Fatalf("syncの座標は省略される想定です: %+v", location)
	}
}

func assertLocationCoords(t *testing.T, location LocationVal, lat, lng float64) {
	t.Helper()

	if location.Lat == nil || location.Lng == nil {
		t.Fatalf("syncの座標が含まれていません: %+v", location)
	}
	if math.Abs(*location.Lat-lat) > 0.000001 || math.Abs(*location.Lng-lng) > 0.000001 {
		t.Fatalf("syncの座標が不正です: got=(%f,%f) want=(%f,%f)", *location.Lat, *location.Lng, lat, lng)
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

func assertDBPlayerCaught(t *testing.T, db *gorm.DB, roomID, userID string, want bool) {
	t.Helper()

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).First(&player).Error; err != nil {
		t.Fatalf("プレイヤー取得失敗: %v", err)
	}
	if player.IsCaught != want {
		t.Fatalf("DB playerのis_caughtが不正です: user_id=%s got=%v want=%v", userID, player.IsCaught, want)
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
	if count != 0 {
		t.Fatalf("waiting中の切断後はプレイヤーが削除される想定ですが、%d件でした", count)
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

	sendJSON(t, wsConn, `{"action":"start","oni_users":["player2"]}`)

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
			if msg.Role == nil || *msg.Role != 0 {
				t.Fatalf("startのroleが不正です: %v", msg.Role)
			}
			if len(msg.OniUsers) != 1 || msg.OniUsers[0] != "player2" {
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
	if player.Role != 0 || player.Color != "#00AAFF" {
		t.Fatalf("DBに保存されたプレイヤー状態が不正です: %+v", player)
	}
	var oni models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player2").First(&oni).Error; err != nil {
		t.Fatalf("鬼取得失敗: %v", err)
	}
	if oni.Role != 1 || oni.Color != "black" {
		t.Fatalf("DBに保存された鬼状態が不正です: %+v", oni)
	}
	client, ok := findClient(room, "player2")
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

func TestViewerSpecificSyncPayloadAndImmediateFirstSync(t *testing.T) {
	roomID := "viewerSyncRoom"
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

	sendJSON(t, runnerConn, `{"action":"move","lat":34.7,"lng":135.5}`)
	sendJSON(t, otherRunnerConn, `{"action":"move","lat":34.8,"lng":135.6}`)

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
	assertLocationCoords(t, runnerLocation, 34.7, 135.5)
	otherRunnerLocation, ok := findLocation(oniSync.Locations, "player3")
	if !ok {
		t.Fatalf("鬼向けsyncに未捕獲逃走者player3が含まれていません: %+v", oniSync.Locations)
	}
	assertLocationMeta(t, otherRunnerLocation, roomID, "player3", "そうた", 0, false, "#00CC66")
	assertLocationCoords(t, otherRunnerLocation, 34.8, 135.6)

	runnerSync := readUntilEvent(t, runnerConn, "sync")
	if len(runnerSync.Locations) != 1 {
		t.Fatalf("逃走者には自分の状態だけが届く想定です: %+v", runnerSync.Locations)
	}
	selfLocation, ok := findLocation(runnerSync.Locations, "player2")
	if !ok {
		t.Fatalf("逃走者向けsyncに自分の状態が含まれていません: %+v", runnerSync.Locations)
	}
	assertLocationMeta(t, selfLocation, roomID, "player2", "みな", 0, false, "#FF00AA")
	assertLocationCoords(t, selfLocation, 34.7, 135.5)
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
	assertLocationCoords(t, remainingRunnerLocation, 34.8, 135.6)

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
}

func TestUnlocatedPlayersDoNotExposeZeroCoordinatesInSync(t *testing.T) {
	roomID := "unlocatedSyncRoom"
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

	oniSync := readUntilEvent(t, oniConn, "sync")
	if len(oniSync.Locations) != 0 {
		t.Fatalf("位置未取得の逃走者は鬼向けsyncに出さない想定です: %+v", oniSync.Locations)
	}

	runnerSync := readUntilEvent(t, runnerConn, "sync")
	if len(runnerSync.Locations) != 1 {
		t.Fatalf("逃走者には座標なしの自分状態だけが届く想定です: %+v", runnerSync.Locations)
	}
	selfLocation, ok := findLocation(runnerSync.Locations, "player2")
	if !ok {
		t.Fatalf("逃走者向けsyncに自分の状態が含まれていません: %+v", runnerSync.Locations)
	}
	assertLocationMeta(t, selfLocation, roomID, "player2", "みな", 0, false, "")
	assertLocationNoCoords(t, selfLocation)
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
	assertWaitingPlayer(t, resetWaiting, "player1", "はるき", "")
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
		if player.Role != 0 || player.IsCaught || player.Color != "" || player.HasLocation {
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

func TestWaitingDisconnectDoesNotLeaveStalePlayerForResult(t *testing.T) {
	roomID := "waitingDisconnectStaleRoom"
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
	staleConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, oniConn, "waiting")
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, staleConn, `{"action":"join","user_id":"player3","name":"そうた"}`)
	readUntilEvent(t, oniConn, "waiting")
	readUntilEvent(t, runnerConn, "waiting")
	if msg := readMessage(t, staleConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	if err := staleConn.Close(); err != nil {
		t.Fatalf("切断失敗: %v", err)
	}
	room := GameHub.GetOrCreateRoom(roomID)
	waitFor(t, func() bool {
		_, ok := findClient(room, "player3")
		return !ok
	})
	assertDBPlayerExists(t, db, roomID, "player3", false)

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	result := readUntilEvent(t, oniConn, "result")

	if _, ok := findResult(result.Results, "player3"); ok {
		t.Fatalf("waiting中に切断したplayerがresultに混ざっています: %+v", result.Results)
	}
	for _, survivor := range result.Survivors {
		if survivor == "player3" {
			t.Fatalf("waiting中に切断したplayerがsurvivorsに混ざっています: %+v", result.Survivors)
		}
	}
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

func TestInvalidCaptureResponseDoesNotCatchPlayer(t *testing.T) {
	roomID := "invalidCaptureResponseRoom"
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

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, oniConn, "waiting")
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, runnerConn, `{"action":"capture_response","approved":true}`)
	assertErrorMessage(t, runnerConn, "捕獲回答はゲーム本編中のみ有効です")
	assertDBPlayerCaught(t, db, roomID, "player2", false)

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	readUntilEvent(t, oniConn, "game_active")
	readUntilEvent(t, runnerConn, "game_active")
	readUntilEvent(t, oniConn, "sync")

	sendJSON(t, oniConn, `{"action":"capture_response","approved":true}`)
	assertErrorMessage(t, oniConn, "捕獲回答の対象が不正です")
	assertDBPlayerCaught(t, db, roomID, "player1", false)
	assertDBPlayerCaught(t, db, roomID, "player2", false)

	spoofConn := connectToRoom(t, baseURL, roomID)
	defer spoofConn.Close()
	sendJSON(t, spoofConn, `{"action":"capture_response","user_id":"player2","approved":true}`)
	assertErrorMessage(t, spoofConn, "先に入室してください")
	assertDBPlayerCaught(t, db, roomID, "player2", false)
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
}
