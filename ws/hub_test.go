package ws

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/watnow/watnow-spring-2026-team2-backend/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	testSupabaseURL     = "https://test-project.supabase.co"
	testCapturePhotoURL = testSupabaseURL + "/storage/v1/object/public/captures/test.jpg"
)

func setTestSupabaseURL(t *testing.T) {
	t.Helper()
	t.Setenv("SUPABASE_URL", testSupabaseURL)
}

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
	if err := db.AutoMigrate(&models.Room{}, &models.Player{}, &models.CaptureRequest{}); err != nil {
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

func readRawEvent(t *testing.T, wsConn *websocket.Conn, event string) OutgoingMessage {
	t.Helper()

	msg := readRawMessage(t, wsConn)
	if msg.Event != event {
		t.Fatalf("想定外のイベント: got=%s want=%s msg=%+v", msg.Event, event, msg)
	}
	return msg
}

func assertErrorMessage(t *testing.T, wsConn *websocket.Conn, message string) {
	t.Helper()

	msg := readMessage(t, wsConn)
	if msg.Event != "error" || msg.Message != message {
		t.Fatalf("エラー通知が不正です: %+v", msg)
	}
}

func prepareRoulette(t *testing.T, hostConn, guestConn *websocket.Conn) OutgoingMessage {
	t.Helper()

	sendJSON(t, hostConn, `{"action":"prepare_roulette"}`)
	hostReady := readUntilEvent(t, hostConn, "roulette_ready")
	guestReady := readUntilEvent(t, guestConn, "roulette_ready")
	if hostReady.RouletteSessionID == "" || guestReady.RouletteSessionID == "" {
		t.Fatalf("roulette_readyにsession_idがありません: host=%+v guest=%+v", hostReady, guestReady)
	}
	if hostReady.RouletteSessionID != guestReady.RouletteSessionID ||
		strings.Join(hostReady.RouletteOrder, ",") != strings.Join(guestReady.RouletteOrder, ",") {
		t.Fatalf("host/guestのroulette_ready payloadが一致していません: host=%+v guest=%+v", hostReady, guestReady)
	}
	if len(hostReady.SelectedOniUserIDs) != 0 {
		t.Fatalf("prepare_rouletteでは鬼を決めない想定です: %+v", hostReady)
	}
	return hostReady
}

func startRouletteSpin(t *testing.T, hostConn, guestConn *websocket.Conn, sessionID string) OutgoingMessage {
	t.Helper()

	sendJSON(t, hostConn, fmt.Sprintf(`{"action":"roulette_start","roulette_session_id":%q}`, sessionID))
	hostStarted := readUntilEvent(t, hostConn, "roulette_spin_started")
	guestStarted := readUntilEvent(t, guestConn, "roulette_spin_started")
	if hostStarted.RouletteSessionID != sessionID || guestStarted.RouletteSessionID != sessionID {
		t.Fatalf("roulette_spin_startedのsession_idが不正です: host=%+v guest=%+v", hostStarted, guestStarted)
	}
	if hostStarted.SpinID <= 0 || hostStarted.SpinID != guestStarted.SpinID ||
		strings.Join(hostStarted.RouletteOrder, ",") != strings.Join(guestStarted.RouletteOrder, ",") ||
		hostStarted.StartsAt == "" || hostStarted.StartsAt != guestStarted.StartsAt {
		t.Fatalf("host/guestのroulette_spin_started payloadが一致していません: host=%+v guest=%+v", hostStarted, guestStarted)
	}
	if len(hostStarted.SelectedOniUserIDs) != 0 {
		t.Fatalf("roulette_startでは鬼を決めない想定です: %+v", hostStarted)
	}
	return hostStarted
}

func stopRouletteSpin(t *testing.T, hostConn, guestConn *websocket.Conn, sessionID string, spinID int) OutgoingMessage {
	t.Helper()

	sendJSON(t, hostConn, fmt.Sprintf(`{"action":"roulette_stop","roulette_session_id":%q,"spin_id":%d}`, sessionID, spinID))
	hostStopped := readUntilEvent(t, hostConn, "roulette_spin_stopped")
	guestStopped := readUntilEvent(t, guestConn, "roulette_spin_stopped")
	if hostStopped.RouletteSessionID != sessionID || guestStopped.RouletteSessionID != sessionID ||
		hostStopped.SpinID != spinID || guestStopped.SpinID != spinID {
		t.Fatalf("roulette_spin_stoppedのsession/spinが不正です: host=%+v guest=%+v", hostStopped, guestStopped)
	}
	if len(hostStopped.SelectedOniUserIDs) == 0 || strings.Join(hostStopped.SelectedOniUserIDs, ",") != strings.Join(guestStopped.SelectedOniUserIDs, ",") ||
		hostStopped.SelectedOniUserID != hostStopped.SelectedOniUserIDs[0] ||
		guestStopped.SelectedOniUserID != hostStopped.SelectedOniUserIDs[0] ||
		hostStopped.RevealedOniCount != 1 || guestStopped.RevealedOniCount != 1 ||
		hostStopped.StopAt == "" || hostStopped.StopAt != guestStopped.StopAt ||
		hostStopped.DecelerationMS != rouletteDecelerationMS || guestStopped.DecelerationMS != rouletteDecelerationMS {
		t.Fatalf("host/guestのroulette_spin_stopped payloadが一致していません: host=%+v guest=%+v", hostStopped, guestStopped)
	}
	return hostStopped
}

func revealNextOni(t *testing.T, hostConn, guestConn *websocket.Conn, sessionID string, wantIndex int) OutgoingMessage {
	t.Helper()

	sendJSON(t, hostConn, fmt.Sprintf(`{"action":"roulette_reveal_next","roulette_session_id":%q}`, sessionID))
	hostReveal := readUntilEvent(t, hostConn, "roulette_reveal_next")
	guestReveal := readUntilEvent(t, guestConn, "roulette_reveal_next")
	if hostReveal.RouletteSessionID != sessionID || guestReveal.RouletteSessionID != sessionID ||
		hostReveal.RevealIndex == nil || guestReveal.RevealIndex == nil ||
		*hostReveal.RevealIndex != wantIndex || *guestReveal.RevealIndex != wantIndex ||
		hostReveal.SelectedOniUserID == "" || hostReveal.SelectedOniUserID != guestReveal.SelectedOniUserID ||
		hostReveal.RevealedOniCount != wantIndex+1 || guestReveal.RevealedOniCount != wantIndex+1 ||
		strings.Join(hostReveal.SelectedOniUserIDs, ",") != strings.Join(guestReveal.SelectedOniUserIDs, ",") ||
		hostReveal.SelectedOniUserID != hostReveal.SelectedOniUserIDs[wantIndex] {
		t.Fatalf("host/guestのroulette_reveal_next payloadが不正です: host=%+v guest=%+v", hostReveal, guestReveal)
	}
	return hostReveal
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

func setDisconnectGraceForTest(t *testing.T, grace time.Duration) {
	t.Helper()

	old := disconnectGraceNanos.Swap(int64(grace))
	t.Cleanup(func() {
		disconnectGraceNanos.Store(old)
	})
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
		// ↓ 修正箇所：userID と msg.Players の2つを渡す！
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

func TestPlayerColorPaletteMatchesFrontendContract(t *testing.T) {
	want := []string{
		"#E2F0CB",
		"#B5EAD7",
		"#99A98F",
		"#93BF4C",
		"#C7CEEA",
		"#E8AEFF",
		"#B550B5",
		"#5C388B",
		"#FFC8A2",
		"#FF9AA2",
		"#FF5563",
		"#F64574",
		"#81E6D9",
		"#7FB5FF",
		"#226DA2",
		"#1E4370",
	}
	if !reflect.DeepEqual(playerColorPalette, want) {
		t.Fatalf("playerColorPalette mismatch:\n got=%v\nwant=%v", playerColorPalette, want)
	}
}

func TestWebSocketFlow(t *testing.T) {
	setDisconnectGraceForTest(t, 100*time.Millisecond)

	roomID := "testRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#7FB5FF"}`)
	msg := readMessage(t, wsConn)
	if msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	assertWaitingPlayer(t, msg, "player1", "はるき", "#7FB5FF")

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
		t.Fatalf("プレイヤー保存失敗: %v", err)
	}
	if player.ID != makePlayerID(roomID, "player1") || player.Name != "はるき" || player.Color != "#7FB5FF" {
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
	if color != "#7FB5FF" {
		t.Fatalf("メモリに保存されたカラーが不正です: %s", color)
	}

	_ = wsConn.Close()
	waitFor(t, func() bool {
		_, ok := findClient(room, "player1")
		return !ok
	})
	assertDBPlayerExists(t, db, roomID, "player1", true)

	waitFor(t, func() bool {
		var count int64
		if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, "player1").Count(&count).Error; err != nil {
			return false
		}
		return count == 0 && !roomExists(roomID)
	})
}

func TestJoinRejectsPaletteOutsideHexColor(t *testing.T) {
	roomID := "invalidJoinColorRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#123456"}`)
	assertErrorMessage(t, wsConn, "カラーは指定されたパレットから選択してください")
	assertDBPlayerExists(t, db, roomID, "player1", false)
}

func TestJoinReservedBlackGetsAutoAssignedPaletteColor(t *testing.T) {
	roomID := "reservedJoinColorRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#000000"}`)
	msg := readUntilEvent(t, wsConn, "waiting")
	assertWaitingPlayer(t, msg, "player1", "はるき", "#E2F0CB")

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
		t.Fatalf("プレイヤー取得失敗: %v", err)
	}
	if player.Color == "#000000" || !isPlayerPaletteColor(player.Color) {
		t.Fatalf("予約色がプレイヤーカラーとして保存されています: %+v", player)
	}
}

func TestUpdateColorBroadcastsAndPersistsWaiting(t *testing.T) {
	roomID := "updateColorRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	defer wsConn1.Close()
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"はるき","color":"#7FB5FF"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな","color":"#F64574"}`)
	readUntilEvent(t, wsConn1, "waiting")
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn1, `{"action":"update_color","color":"#226da2"}`)
	waiting1 := readUntilEvent(t, wsConn1, "waiting")
	waiting2 := readUntilEvent(t, wsConn2, "waiting")
	assertWaitingPlayer(t, waiting1, "player1", "はるき", "#226DA2")
	assertWaitingPlayer(t, waiting2, "player1", "はるき", "#226DA2")

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
		t.Fatalf("プレイヤー取得失敗: %v", err)
	}
	if player.Color != "#226DA2" {
		t.Fatalf("DBのcolorが更新されていません: %+v", player)
	}
}

func TestUpdateColorRejectsInvalidColor(t *testing.T) {
	roomID := "invalidUpdateColorRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#7FB5FF"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, wsConn, `{"action":"update_color","color":"#GGGGGG"}`)
	assertErrorMessage(t, wsConn, "カラーの形式が不正です（例: #FF0000）")
	sendJSON(t, wsConn, `{"action":"update_color","color":"#123456"}`)
	assertErrorMessage(t, wsConn, "カラーは指定されたパレットから選択してください")
	sendJSON(t, wsConn, `{"action":"update_color"}`)
	assertErrorMessage(t, wsConn, "カラーを選択してください")
}

func TestUpdateColorRejectsDuplicateAndReservedBlack(t *testing.T) {
	roomID := "duplicateUpdateColorRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	defer wsConn1.Close()
	wsConn2 := connectToRoom(t, baseURL, roomID)
	defer wsConn2.Close()

	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"はるき","color":"#7FB5FF"}`)
	if msg := readMessage(t, wsConn1); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな","color":"#F64574"}`)
	readUntilEvent(t, wsConn1, "waiting")
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	duplicateJoinConn := connectToRoom(t, baseURL, roomID)
	defer duplicateJoinConn.Close()
	sendJSON(t, duplicateJoinConn, `{"action":"join","user_id":"player3","name":"そうた","color":"#F64574"}`)
	duplicateJoinWaiting := readUntilEvent(t, duplicateJoinConn, "waiting")
	player3, ok := findWaitingPlayer(duplicateJoinWaiting.Players, "player3")
	if !ok {
		t.Fatalf("重複色join後のwaitingにplayer3が含まれていません: %+v", duplicateJoinWaiting.Players)
	}
	if player3.Color == "" || player3.Color == "#F64574" {
		t.Fatalf("重複色joinで安全な色が自動割当されていません: %+v", player3)
	}
	readUntilEvent(t, wsConn1, "waiting")
	readUntilEvent(t, wsConn2, "waiting")

	sendJSON(t, wsConn1, `{"action":"update_color","color":"#F64574"}`)
	assertErrorMessage(t, wsConn1, "このカラーはすでに使われています")
	sendJSON(t, wsConn1, `{"action":"update_color","color":"#000000"}`)
	assertErrorMessage(t, wsConn1, "黒は鬼用のカラーです")
}

func TestUpdateColorReflectsNextActiveSync(t *testing.T) {
	roomID := "activeUpdateColorRoom"
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

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#7FB5FF"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな","color":"#F64574"}`)
	readUntilEvent(t, oniConn, "waiting")
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, runnerConn, `{"action":"move","lat":34.7,"lng":135.5}`)

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	readUntilEvent(t, oniConn, "game_active")
	readUntilEvent(t, runnerConn, "game_active")
	readUntilEvent(t, runnerConn, "sync")

	sendJSON(t, runnerConn, `{"action":"update_color","color":"#226DA2"}`)
	room := GameHub.GetOrCreateRoom(roomID)
	waitFor(t, func() bool {
		client, ok := findClient(room, "player2")
		if !ok {
			return false
		}
		client.mu.Lock()
		color := client.Color
		client.mu.Unlock()
		return color == "#226DA2"
	})
	room.SendSyncToAll()

	syncMsg := readUntilEvent(t, runnerConn, "sync")
	location, ok := findLocation(syncMsg.Locations, "player2")
	if !ok {
		t.Fatalf("syncにplayer2が含まれていません: %+v", syncMsg.Locations)
	}
	assertLocationMeta(t, location, roomID, "player2", "みな", 0, false, "#226DA2")
}

func TestWaitingHostAssignedAndTransferredOnLeave(t *testing.T) {
	roomID := "hostTransferRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	firstWaiting := readUntilEvent(t, hostConn, "waiting")
	if firstWaiting.HostUserID != "player1" {
		t.Fatalf("最初の参加者がhostになっていません: %+v", firstWaiting)
	}
	hostPlayer, ok := findWaitingPlayer(firstWaiting.Players, "player1")
	if !ok || !hostPlayer.IsHost {
		t.Fatalf("players[].is_hostが不正です: %+v", firstWaiting.Players)
	}

	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	waitingForHost := readUntilEvent(t, hostConn, "waiting")
	waitingForGuest := readUntilEvent(t, guestConn, "waiting")
	if waitingForHost.HostUserID != "player1" || waitingForGuest.HostUserID != "player1" {
		t.Fatalf("host_user_idが維持されていません: host=%+v guest=%+v", waitingForHost, waitingForGuest)
	}

	sendJSON(t, hostConn, `{"action":"leave"}`)
	transferred := readUntilEvent(t, guestConn, "waiting")
	if transferred.HostUserID != "player2" {
		t.Fatalf("hostが移譲されていません: %+v", transferred)
	}
	guestPlayer, ok := findWaitingPlayer(transferred.Players, "player2")
	if !ok || !guestPlayer.IsHost {
		t.Fatalf("移譲後のis_hostが不正です: %+v", transferred.Players)
	}

	var room models.Room
	if err := db.First(&room, "id = ?", roomID).Error; err != nil {
		t.Fatalf("ルーム取得失敗: %v", err)
	}
	if room.HostUserID != "player2" {
		t.Fatalf("DBのhost_user_idが移譲されていません: %+v", room)
	}
}

func TestNonHostCannotStartOrControlRoulette(t *testing.T) {
	roomID := "hostOnlyActionRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    120,
		OniCount:     1,
		SyncInterval: 30,
		GracePeriod:  0,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, hostConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	if msg := readMessage(t, guestConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, guestConn, `{"action":"start_roulette"}`)
	assertErrorMessage(t, guestConn, "ホストのみ実行できます")
	sendJSON(t, guestConn, `{"action":"start","oni_users":["player1"]}`)
	assertErrorMessage(t, guestConn, "ホストのみ実行できます")

	ready := prepareRoulette(t, hostConn, guestConn)
	sendJSON(t, guestConn, fmt.Sprintf(`{"action":"roulette_start","roulette_session_id":%q}`, ready.RouletteSessionID))
	assertErrorMessage(t, guestConn, "ホストのみ実行できます")

	started := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	sendJSON(t, guestConn, fmt.Sprintf(`{"action":"roulette_stop","roulette_session_id":%q,"spin_id":%d}`, ready.RouletteSessionID, started.SpinID))
	assertErrorMessage(t, guestConn, "ホストのみ実行できます")
	stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, started.SpinID)
	sendJSON(t, guestConn, fmt.Sprintf(`{"action":"roulette_reveal_next","roulette_session_id":%q}`, ready.RouletteSessionID))
	assertErrorMessage(t, guestConn, "ホストのみ実行できます")
	sendJSON(t, guestConn, fmt.Sprintf(`{"action":"roulette_reset","roulette_session_id":%q}`, ready.RouletteSessionID))
	assertErrorMessage(t, guestConn, "ホストのみ実行できます")
}

func TestRouletteFlowBroadcastsToRoom(t *testing.T) {
	roomID := "rouletteRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	if msg := readMessage(t, hostConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	if msg := readMessage(t, guestConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	ready := prepareRoulette(t, hostConn, guestConn)
	if strings.Join(ready.RouletteOrder, ",") != "player1,player2" {
		t.Fatalf("roulette_orderが不正です: %+v", ready.RouletteOrder)
	}
	started := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	stopped := stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, started.SpinID)
	if len(stopped.SelectedOniUserIDs) != 1 {
		t.Fatalf("roulette_stopのselected_oni_user_ids数が不正です: %+v", stopped)
	}
}

func TestRouletteRevealNextBroadcastsPendingOni(t *testing.T) {
	roomID := "rouletteRevealNextRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  2,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()
	thirdConn := connectToRoom(t, baseURL, roomID)
	defer thirdConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")
	sendJSON(t, thirdConn, `{"action":"join","user_id":"player3","name":"そうた"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")
	readUntilEvent(t, thirdConn, "waiting")

	ready := prepareRoulette(t, hostConn, guestConn)
	started := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	stopped := stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, started.SpinID)
	if len(stopped.SelectedOniUserIDs) != 2 {
		t.Fatalf("roulette_stopのselected_oni_user_ids数が不正です: %+v", stopped)
	}

	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.RLock()
	if len(room.Roulette.PendingOniUserIDs) != 2 || room.Roulette.RevealedOniCount != 1 {
		t.Fatalf("roulette_stop後のstateが不正です: %+v", room.Roulette)
	}
	room.mu.RUnlock()

	revealed := revealNextOni(t, hostConn, guestConn, ready.RouletteSessionID, 1)
	if strings.Join(revealed.SelectedOniUserIDs, ",") != strings.Join(stopped.SelectedOniUserIDs, ",") {
		t.Fatalf("reveal_nextで決定済み鬼配列が変わっています: stopped=%+v revealed=%+v", stopped, revealed)
	}

	reconnectConn := connectToRoom(t, baseURL, roomID)
	defer reconnectConn.Close()
	sendJSON(t, reconnectConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readRawEvent(t, reconnectConn, "waiting")
	readRawEvent(t, reconnectConn, "room_settings")
	readRawEvent(t, reconnectConn, "roulette_ready")
	stopSnapshot := readRawEvent(t, reconnectConn, "roulette_spin_stopped")
	if strings.Join(stopSnapshot.SelectedOniUserIDs, ",") != strings.Join(stopped.SelectedOniUserIDs, ",") ||
		stopSnapshot.RevealedOniCount != 2 {
		t.Fatalf("reveal後のroulette snapshotが不正です: %+v", stopSnapshot)
	}

	sendJSON(t, hostConn, fmt.Sprintf(`{"action":"roulette_reveal_next","roulette_session_id":%q}`, ready.RouletteSessionID))
	assertErrorMessage(t, hostConn, "未発表の鬼はいません")
}

func TestRouletteRevealNextRejectsSessionMismatchAndNotStopped(t *testing.T) {
	roomID := "rouletteRevealNextValidationRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  2,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()
	thirdConn := connectToRoom(t, baseURL, roomID)
	defer thirdConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")
	sendJSON(t, thirdConn, `{"action":"join","user_id":"player3","name":"そうた"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")
	readUntilEvent(t, thirdConn, "waiting")

	ready := prepareRoulette(t, hostConn, guestConn)
	sendJSON(t, hostConn, fmt.Sprintf(`{"action":"roulette_reveal_next","roulette_session_id":%q}`, ready.RouletteSessionID))
	assertErrorMessage(t, hostConn, "ルーレットは停止していません")

	started := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, started.SpinID)
	sendJSON(t, hostConn, `{"action":"roulette_reveal_next","roulette_session_id":"wrong-session"}`)
	assertErrorMessage(t, hostConn, "ルーレットセッションが見つかりません")
}

func TestRouletteReadySnapshotSentOnReconnect(t *testing.T) {
	roomID := "rouletteReadyReconnectRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	ready := prepareRoulette(t, hostConn, guestConn)

	reconnectConn := connectToRoom(t, baseURL, roomID)
	defer reconnectConn.Close()
	sendJSON(t, reconnectConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readRawEvent(t, reconnectConn, "waiting")
	readRawEvent(t, reconnectConn, "room_settings")
	snapshot := readRawEvent(t, reconnectConn, "roulette_ready")

	if snapshot.RouletteSessionID != ready.RouletteSessionID || strings.Join(snapshot.RouletteOrder, ",") != strings.Join(ready.RouletteOrder, ",") {
		t.Fatalf("roulette_ready snapshotが既存stateを復元していません: ready=%+v snapshot=%+v", ready, snapshot)
	}
}

func TestRouletteSpinningSnapshotSentOnReconnect(t *testing.T) {
	roomID := "rouletteSpinningReconnectRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	ready := prepareRoulette(t, hostConn, guestConn)
	started := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)

	reconnectConn := connectToRoom(t, baseURL, roomID)
	defer reconnectConn.Close()
	sendJSON(t, reconnectConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readRawEvent(t, reconnectConn, "waiting")
	readRawEvent(t, reconnectConn, "room_settings")
	readySnapshot := readRawEvent(t, reconnectConn, "roulette_ready")
	spinSnapshot := readRawEvent(t, reconnectConn, "roulette_spin_started")

	if readySnapshot.RouletteSessionID != ready.RouletteSessionID || strings.Join(readySnapshot.RouletteOrder, ",") != strings.Join(ready.RouletteOrder, ",") {
		t.Fatalf("roulette_ready snapshotが不正です: ready=%+v snapshot=%+v", ready, readySnapshot)
	}
	if spinSnapshot.RouletteSessionID != started.RouletteSessionID || spinSnapshot.SpinID != started.SpinID ||
		strings.Join(spinSnapshot.RouletteOrder, ",") != strings.Join(started.RouletteOrder, ",") ||
		spinSnapshot.StartsAt != started.StartsAt {
		t.Fatalf("roulette_spin_started snapshotが不正です: started=%+v snapshot=%+v", started, spinSnapshot)
	}
}

func TestRouletteStoppedSnapshotSentOnReconnect(t *testing.T) {
	roomID := "rouletteStoppedReconnectRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	ready := prepareRoulette(t, hostConn, guestConn)
	started := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	stopped := stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, started.SpinID)

	reconnectConn := connectToRoom(t, baseURL, roomID)
	defer reconnectConn.Close()
	sendJSON(t, reconnectConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readRawEvent(t, reconnectConn, "waiting")
	readRawEvent(t, reconnectConn, "room_settings")
	readySnapshot := readRawEvent(t, reconnectConn, "roulette_ready")
	stopSnapshot := readRawEvent(t, reconnectConn, "roulette_spin_stopped")

	if readySnapshot.RouletteSessionID != ready.RouletteSessionID || strings.Join(readySnapshot.RouletteOrder, ",") != strings.Join(ready.RouletteOrder, ",") {
		t.Fatalf("roulette_ready snapshotが不正です: ready=%+v snapshot=%+v", ready, readySnapshot)
	}
	if stopSnapshot.RouletteSessionID != stopped.RouletteSessionID || stopSnapshot.SpinID != stopped.SpinID ||
		strings.Join(stopSnapshot.RouletteOrder, ",") != strings.Join(ready.RouletteOrder, ",") ||
		strings.Join(stopSnapshot.SelectedOniUserIDs, ",") != strings.Join(stopped.SelectedOniUserIDs, ",") ||
		stopSnapshot.RevealedOniCount != stopped.RevealedOniCount ||
		stopSnapshot.StartsAt != started.StartsAt || stopSnapshot.StopAt != stopped.StopAt ||
		stopSnapshot.DecelerationMS != rouletteDecelerationMS {
		t.Fatalf("roulette_spin_stopped snapshotが不正です: stopped=%+v snapshot=%+v", stopped, stopSnapshot)
	}
}

func TestStartRouletteAliasPreparesWithoutSelection(t *testing.T) {
	roomID := "rouletteAliasRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	sendJSON(t, hostConn, `{"action":"start_roulette"}`)
	hostReady := readUntilEvent(t, hostConn, "roulette_ready")
	guestReady := readUntilEvent(t, guestConn, "roulette_ready")
	if hostReady.RouletteSessionID == "" || hostReady.RouletteSessionID != guestReady.RouletteSessionID {
		t.Fatalf("start_roulette aliasのroulette_readyが不正です: host=%+v guest=%+v", hostReady, guestReady)
	}
	if len(hostReady.SelectedOniUserIDs) != 0 || len(guestReady.SelectedOniUserIDs) != 0 {
		t.Fatalf("start_roulette aliasでは鬼を決めない想定です: host=%+v guest=%+v", hostReady, guestReady)
	}
}

func TestStartUsesPendingRouletteSelectionFromStop(t *testing.T) {
	roomID := "roulettePendingRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    120,
		OniCount:     1,
		SyncInterval: 30,
		GracePeriod:  0,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	ready := prepareRoulette(t, hostConn, guestConn)
	started := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	stopped := stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, started.SpinID)
	selected := stopped.SelectedOniUserIDs[0]
	wrongOni := "player1"
	if selected == "player1" {
		wrongOni = "player2"
	}
	sendJSON(t, hostConn, `{"action":"start","oni_users":["`+wrongOni+`"]}`)

	hostStart := readUntilEvent(t, hostConn, "start")
	guestStart := readUntilEvent(t, guestConn, "start")
	if len(hostStart.OniUsers) != 1 || hostStart.OniUsers[0] != selected || len(guestStart.OniUsers) != 1 || guestStart.OniUsers[0] != selected {
		t.Fatalf("startがpending rouletteの鬼を使っていません: selected=%s host=%+v guest=%+v", selected, hostStart, guestStart)
	}
	if selected == "player1" {
		if hostStart.Role == nil || *hostStart.Role != 1 || guestStart.Role == nil || *guestStart.Role != 0 {
			t.Fatalf("pending rouletteのrole反映が不正です: host=%+v guest=%+v", hostStart, guestStart)
		}
	} else {
		if hostStart.Role == nil || *hostStart.Role != 0 || guestStart.Role == nil || *guestStart.Role != 1 {
			t.Fatalf("pending rouletteのrole反映が不正です: host=%+v guest=%+v", hostStart, guestStart)
		}
	}
}

func TestRouletteResetClearsPendingAndAllowsNewStop(t *testing.T) {
	roomID := "rouletteResetRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    120,
		OniCount:     1,
		SyncInterval: 30,
		GracePeriod:  0,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	ready := prepareRoulette(t, hostConn, guestConn)
	firstStarted := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	firstStopped := stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, firstStarted.SpinID)
	if len(firstStopped.SelectedOniUserIDs) != 1 {
		t.Fatalf("初回stopのselected_oni_user_idsが不正です: %+v", firstStopped)
	}

	sendJSON(t, hostConn, fmt.Sprintf(`{"action":"roulette_reset","roulette_session_id":%q}`, ready.RouletteSessionID))
	hostReset := readUntilEvent(t, hostConn, "roulette_reset")
	guestReset := readUntilEvent(t, guestConn, "roulette_reset")
	if hostReset.RouletteSessionID != ready.RouletteSessionID || guestReset.RouletteSessionID != ready.RouletteSessionID {
		t.Fatalf("roulette_resetのpayloadが不正です: host=%+v guest=%+v", hostReset, guestReset)
	}

	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.RLock()
	if len(room.Roulette.PendingOniUserIDs) != 0 || room.Roulette.RevealedOniCount != 0 || room.Roulette.Status != rouletteStatusReady {
		t.Fatalf("roulette_reset後のstateが不正です: %+v", room.Roulette)
	}
	room.mu.RUnlock()

	secondStarted := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	if secondStarted.SpinID <= firstStarted.SpinID {
		t.Fatalf("reset後のspin_idが更新されていません: first=%d second=%d", firstStarted.SpinID, secondStarted.SpinID)
	}
	secondStopped := stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, secondStarted.SpinID)
	if len(secondStopped.SelectedOniUserIDs) != 1 {
		t.Fatalf("reset後stopのselected_oni_user_idsが不正です: %+v", secondStopped)
	}
}

func TestRouletteRejectsPlayersShortage(t *testing.T) {
	roomID := "rouletteShortageRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")

	sendJSON(t, hostConn, `{"action":"prepare_roulette"}`)
	assertErrorMessage(t, hostConn, "ゲーム開始には2人以上必要です")
}

func TestRouletteRejectsAllOniSetting(t *testing.T) {
	roomID := "rouletteAllOniRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  2,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	sendJSON(t, hostConn, `{"action":"prepare_roulette"}`)
	assertErrorMessage(t, hostConn, "全員を鬼にはできません")
}

func TestJoinUsesMaxPlayersLimit(t *testing.T) {
	roomID := "maxPlayersJoinRoom"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:         roomID,
		Status:     0,
		TimeLimit:  900,
		MaxPlayers: 2,
	})
	defer cleanup()

	conn1 := connectToRoom(t, baseURL, roomID)
	defer conn1.Close()
	conn2 := connectToRoom(t, baseURL, roomID)
	defer conn2.Close()
	conn3 := connectToRoom(t, baseURL, roomID)
	defer conn3.Close()

	sendJSON(t, conn1, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, conn1, "waiting")
	sendJSON(t, conn2, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, conn1, "waiting")
	readUntilEvent(t, conn2, "waiting")

	sendJSON(t, conn3, `{"action":"join","user_id":"player3","name":"そうた"}`)
	assertErrorMessage(t, conn3, "参加人数は2人までです")
}

func TestJoinNameValidationTrimsAndCountsRunes(t *testing.T) {
	roomID := "nameValidationRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"  あいうえおかきくけこさし  "}`)
	waiting := readUntilEvent(t, wsConn, "waiting")
	assertWaitingPlayer(t, waiting, "player1", "あいうえおかきくけこさし", playerColorPalette[0])

	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player1").First(&player).Error; err != nil {
		t.Fatalf("プレイヤー取得失敗: %v", err)
	}
	if player.Name != "あいうえおかきくけこさし" || player.Color != playerColorPalette[0] {
		t.Fatalf("trim後の名前が保存されていません: %+v", player)
	}

	invalidConn := connectToRoom(t, baseURL, roomID)
	defer invalidConn.Close()
	sendJSON(t, invalidConn, `{"action":"join","user_id":"player2","name":"あいうえおかきくけこさしす"}`)
	assertErrorMessage(t, invalidConn, "名前は1文字以上、12文字以下にしてください")
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
		AreaSize:      "100",
		SyncInterval:  180,
		GracePeriod:   120,
		AreaCenterLat: 34.0,
		AreaCenterLng: 135.0,
		HasAreaCenter: true,
	})
	defer cleanup()

	wsConn := connectToRoom(t, baseURL, roomID)
	defer wsConn.Close()

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#7FB5FF"}`)
	if msg := readRawMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	settings := readRawMessage(t, wsConn)
	assertRoomSettings(t, settings, 900, 1, "100", 180, 120, &AreaCenterVal{Lat: 34.0, Lng: 135.0})
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

	sendJSON(t, wsConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#1E4370"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"みな","color":"#F64574"}`)
	if msg := readMessage(t, wsConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, wsConn2); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	GameHub.UpdateRoomSettings(roomID, 120, 1, "100", 1, 0)

	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.RLock()
	if room.AreaSize != "100" || room.SyncInterval != 1 || room.GracePeriod != 0 {
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
	if player.Role != 0 || player.Color != "#1E4370" {
		t.Fatalf("DBに保存されたプレイヤー状態が不正です: %+v", player)
	}
	var oni models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player2").First(&oni).Error; err != nil {
		t.Fatalf("鬼取得失敗: %v", err)
	}
	if oni.Role != 1 || oni.Color != "#F64574" {
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
	setTestSupabaseURL(t)

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

	sendJSON(t, oniConn, `{"action":"join","user_id":"player1","name":"はるき","color":"#1E4370"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, runnerConn, `{"action":"join","user_id":"player2","name":"みな","color":"#F64574"}`)
	if msg := readMessage(t, oniConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}
	if msg := readMessage(t, runnerConn); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %s", msg.Event)
	}

	sendJSON(t, otherRunnerConn, `{"action":"join","user_id":"player3","name":"そうた","color":"#226DA2"}`)
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
	assertLocationMeta(t, runnerLocation, roomID, "player2", "みな", 0, false, "#F64574")
	assertLocationCoords(t, runnerLocation, 34.7, 135.5)
	otherRunnerLocation, ok := findLocation(oniSync.Locations, "player3")
	if !ok {
		t.Fatalf("鬼向けsyncに未捕獲逃走者player3が含まれていません: %+v", oniSync.Locations)
	}
	assertLocationMeta(t, otherRunnerLocation, roomID, "player3", "そうた", 0, false, "#226DA2")
	assertLocationCoords(t, otherRunnerLocation, 34.8, 135.6)

	runnerSync := readUntilEvent(t, runnerConn, "sync")
	if len(runnerSync.Locations) != 1 {
		t.Fatalf("逃走者には自分の状態だけが届く想定です: %+v", runnerSync.Locations)
	}
	selfLocation, ok := findLocation(runnerSync.Locations, "player2")
	if !ok {
		t.Fatalf("逃走者向けsyncに自分の状態が含まれていません: %+v", runnerSync.Locations)
	}
	assertLocationMeta(t, selfLocation, roomID, "player2", "みな", 0, false, "#F64574")
	assertLocationCoords(t, selfLocation, 34.7, 135.5)
	if _, ok := findLocation(runnerSync.Locations, "player1"); ok {
		t.Fatalf("逃走者向けsyncに他プレイヤーが含まれています: %+v", runnerSync.Locations)
	}

	// ★修正：鬼が捕獲申請を行う
	sendJSON(t, oniConn, fmt.Sprintf(`{"action":"capture_request","target_id":"player2","photo_url":%q}`, testCapturePhotoURL))

	// ★修正：逃走者が通知を受け取り、RequestIDを抜き取る
	checkMsg := readUntilEvent(t, runnerConn, "capture_checking")
	reqID := checkMsg.RequestID

	// ★修正：抜き取ったIDを使って承認する
	sendJSON(t, runnerConn, `{"action":"capture_response","approved":true,"request_id":"`+reqID+`"}`)

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
	assertLocationMeta(t, remainingRunnerLocation, roomID, "player3", "そうた", 0, false, "#226DA2")
	assertLocationCoords(t, remainingRunnerLocation, 34.8, 135.6)

	caughtRunnerSync := readUntilEvent(t, runnerConn, "sync")
	if len(caughtRunnerSync.Locations) != 1 {
		t.Fatalf("捕獲済み逃走者には自分の状態だけが届く想定です: %+v", caughtRunnerSync.Locations)
	}
	caughtSelfLocation, ok := findLocation(caughtRunnerSync.Locations, "player2")
	if !ok {
		t.Fatalf("捕獲済み逃走者向けsyncに自分の状態が含まれていません: %+v", caughtRunnerSync.Locations)
	}
	assertLocationMeta(t, caughtSelfLocation, roomID, "player2", "みな", 0, true, "#F64574")
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
	assertLocationMeta(t, selfLocation, roomID, "player2", "みな", 0, false, playerColorPalette[1])
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

	ready := prepareRoulette(t, oniConn, runnerConn)
	started := startRouletteSpin(t, oniConn, runnerConn, ready.RouletteSessionID)
	stopRouletteSpin(t, oniConn, runnerConn, ready.RouletteSessionID, started.SpinID)
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
	assertWaitingPlayer(t, resetWaiting, "player1", "はるき", playerColorPalette[0])
	assertWaitingPlayer(t, resetWaiting, "player2", "みな", playerColorPalette[1])
	readUntilEvent(t, runnerConn, "waiting")

	waitFor(t, func() bool {
		var savedRoom models.Room
		if err := db.First(&savedRoom, "id = ?", roomID).Error; err != nil {
			return false
		}
		return savedRoom.Status == 0
	})
	assertDBPlayerExists(t, db, roomID, "player3", false)
	room.mu.RLock()
	if room.Roulette.SessionID != "" || len(room.Roulette.PendingOniUserIDs) != 0 || room.Roulette.RevealedOniCount != 0 {
		t.Fatalf("reset後にroulette stateが残っています: %+v", room.Roulette)
	}
	room.mu.RUnlock()

	expectedColors := map[string]string{
		"player1": playerColorPalette[0],
		"player2": playerColorPalette[1],
	}
	for _, userID := range []string{"player1", "player2"} {
		var player models.Player
		if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).First(&player).Error; err != nil {
			t.Fatalf("reset後のプレイヤー取得失敗: %v", err)
		}
		if player.Role != 0 || player.IsCaught || player.Color != expectedColors[userID] || player.HasLocation {
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
	setDisconnectGraceForTest(t, 30*time.Millisecond)

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
	assertDBPlayerExists(t, db, roomID, "player3", true)
	waitFor(t, func() bool {
		var count int64
		if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, "player3").Count(&count).Error; err != nil {
			return false
		}
		return count == 0
	})

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

func TestWaitingDisconnectReconnectRestoresSamePlayerWithinGrace(t *testing.T) {
	setDisconnectGraceForTest(t, 150*time.Millisecond)

	roomID := "waitingReconnectGraceRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	if err := guestConn.Close(); err != nil {
		t.Fatalf("切断失敗: %v", err)
	}
	room := GameHub.GetOrCreateRoom(roomID)
	waitFor(t, func() bool {
		_, ok := findClient(room, "player2")
		return !ok
	})
	assertDBPlayerExists(t, db, roomID, "player2", true)

	reconnectConn := connectToRoom(t, baseURL, roomID)
	defer reconnectConn.Close()
	sendJSON(t, reconnectConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readRawEvent(t, reconnectConn, "waiting")
	readRawEvent(t, reconnectConn, "room_settings")

	time.Sleep(200 * time.Millisecond)
	var count int64
	if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, "player2").Count(&count).Error; err != nil {
		t.Fatalf("プレイヤー件数取得失敗: %v", err)
	}
	if count != 1 {
		t.Fatalf("grace内reconnect後のDB player件数が不正です: got=%d want=1", count)
	}
	var player models.Player
	if err := db.Where("room_id = ? AND user_id = ?", roomID, "player2").First(&player).Error; err != nil {
		t.Fatalf("reconnect後のプレイヤー取得失敗: %v", err)
	}
	if player.ID != makePlayerID(roomID, "player2") {
		t.Fatalf("reconnect後に同じplayer IDが維持されていません: %+v", player)
	}
}

func TestHostDisconnectTransfersHostOnlyAfterGrace(t *testing.T) {
	setDisconnectGraceForTest(t, 80*time.Millisecond)

	roomID := "hostDisconnectGraceRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	guestConn := connectToRoom(t, baseURL, roomID)
	defer guestConn.Close()

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	if err := hostConn.Close(); err != nil {
		t.Fatalf("切断失敗: %v", err)
	}
	room := GameHub.GetOrCreateRoom(roomID)
	waitFor(t, func() bool {
		_, ok := findClient(room, "player1")
		return !ok
	})

	var savedRoom models.Room
	if err := db.First(&savedRoom, "id = ?", roomID).Error; err != nil {
		t.Fatalf("ルーム取得失敗: %v", err)
	}
	if savedRoom.HostUserID != "player1" || room.hostUserID() != "player1" {
		t.Fatalf("grace中にhostが移譲されています: db=%s memory=%s", savedRoom.HostUserID, room.hostUserID())
	}

	waitFor(t, func() bool {
		if err := db.First(&savedRoom, "id = ?", roomID).Error; err != nil {
			return false
		}
		var count int64
		if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, "player1").Count(&count).Error; err != nil {
			return false
		}
		return count == 0 && savedRoom.HostUserID == "player2" && room.hostUserID() == "player2"
	})
}

func TestRouletteDisconnectDoesNotClearPendingImmediately(t *testing.T) {
	setDisconnectGraceForTest(t, 200*time.Millisecond)

	roomID := "rouletteDisconnectGraceRoom"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    120,
		OniCount:     1,
		SyncInterval: 30,
		GracePeriod:  0,
	})
	defer cleanup()

	hostConn := connectToRoom(t, baseURL, roomID)
	defer hostConn.Close()
	guestConn := connectToRoom(t, baseURL, roomID)

	sendJSON(t, hostConn, `{"action":"join","user_id":"player1","name":"はるき"}`)
	readUntilEvent(t, hostConn, "waiting")
	sendJSON(t, guestConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readUntilEvent(t, hostConn, "waiting")
	readUntilEvent(t, guestConn, "waiting")

	ready := prepareRoulette(t, hostConn, guestConn)
	started := startRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID)
	stopped := stopRouletteSpin(t, hostConn, guestConn, ready.RouletteSessionID, started.SpinID)

	if err := guestConn.Close(); err != nil {
		t.Fatalf("切断失敗: %v", err)
	}
	room := GameHub.GetOrCreateRoom(roomID)
	waitFor(t, func() bool {
		_, ok := findClient(room, "player2")
		return !ok
	})
	assertDBPlayerExists(t, db, roomID, "player2", true)
	room.mu.RLock()
	pending := copyStrings(room.Roulette.PendingOniUserIDs)
	status := room.Roulette.Status
	sessionID := room.Roulette.SessionID
	spinID := room.Roulette.SpinID
	stateText := fmt.Sprintf("%+v", room.Roulette)
	room.mu.RUnlock()
	if status != rouletteStatusStopped || sessionID != ready.RouletteSessionID || spinID != started.SpinID ||
		strings.Join(pending, ",") != strings.Join(stopped.SelectedOniUserIDs, ",") {
		t.Fatalf("非明示disconnectでroulette pendingが即clearされています: pending=%+v state=%s", pending, stateText)
	}

	reconnectConn := connectToRoom(t, baseURL, roomID)
	sendJSON(t, reconnectConn, `{"action":"join","user_id":"player2","name":"みな"}`)
	readRawEvent(t, reconnectConn, "waiting")
	readRawEvent(t, reconnectConn, "room_settings")
	readRawEvent(t, reconnectConn, "roulette_ready")
	readRawEvent(t, reconnectConn, "roulette_spin_stopped")
	sendJSON(t, reconnectConn, `{"action":"leave"}`)
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

	// 存在しないリクエストIDで承認しようとする（不正なレスポンス）
	sendJSON(t, runnerConn, `{"action":"capture_response","approved":true,"request_id":"invalid-dummy-id"}`)
	assertErrorMessage(t, runnerConn, "捕獲回答はゲーム本編中のみ有効です")
	assertDBPlayerCaught(t, db, roomID, "player2", false)

	sendJSON(t, oniConn, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, oniConn, "start")
	readUntilEvent(t, runnerConn, "start")
	readUntilEvent(t, oniConn, "game_active")
	readUntilEvent(t, runnerConn, "game_active")
	readUntilEvent(t, oniConn, "sync")

	// 鬼が不正に承認リクエストを送る
	sendJSON(t, oniConn, `{"action":"capture_response","approved":true,"request_id":"invalid-dummy-id"}`)
	assertErrorMessage(t, oniConn, "捕獲回答の権限がありません")
	assertDBPlayerCaught(t, db, roomID, "player1", false)
	assertDBPlayerCaught(t, db, roomID, "player2", false)

	spoofConn := connectToRoom(t, baseURL, roomID)
	defer spoofConn.Close()
	sendJSON(t, spoofConn, `{"action":"capture_response","user_id":"player2","approved":true,"request_id":"invalid-dummy-id"}`)
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
	if result.SurvivalSeconds != nil {
		t.Fatalf("鬼のsurvival_secondsは省略する想定です: %+v", result)
	}
	runnerResult, ok := findResult(msg.Results, "player2")
	if !ok {
		t.Fatalf("resultにplayer2が含まれていません: %+v", msg.Results)
	}
	if runnerResult.Name != "みな" || runnerResult.Role != 0 || runnerResult.IsCaught {
		t.Fatalf("player2の結果が不正です: %+v", runnerResult)
	}
	if runnerResult.SurvivalSeconds == nil || *runnerResult.SurvivalSeconds < 0 {
		t.Fatalf("逃げ切りrunnerのsurvival_secondsが不正です: %+v", runnerResult)
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
	setTestSupabaseURL(t)

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

	// ★追加：鬼が捕獲申請を行う
	sendJSON(t, wsConn1, fmt.Sprintf(`{"action":"capture_request","target_id":"player2","photo_url":%q}`, testCapturePhotoURL))

	// ★追加：逃走者が通知を受け取り、RequestIDを抜き取る
	checkMsg := readUntilEvent(t, runnerConn, "capture_checking")
	reqID := checkMsg.RequestID
	if reqID == "" {
		t.Fatalf("capture_checking に RequestID が含まれていません")
	}

	// 逃走者が抜き取ったIDを使って承認する
	sendJSON(t, runnerConn, `{"action":"capture_response","approved":true,"request_id":"`+reqID+`"}`)

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
	if result.PhotoURL != testCapturePhotoURL {
		t.Fatalf("逃走者のphoto_urlがresultに反映されていません: %+v", result)
	}
	if result.CapturedAt == "" {
		t.Fatalf("捕獲済みrunnerにcaptured_atが含まれていません: %+v", result)
	}
	if result.SurvivalSeconds == nil || *result.SurvivalSeconds < 0 {
		t.Fatalf("捕獲済みrunnerのsurvival_secondsが不正です: %+v", result)
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

func TestUpdateRoomSettingsPreservesRoulettePending(t *testing.T) {
	roomID := "testRoom_settings_roulette"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"Player 1","color":"#7FB5FF"}`)
	_ = readUntilEvent(t, wsConn1, "waiting")

	wsConn2 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"Player 2","color":"#F64574"}`)
	_ = readUntilEvent(t, wsConn2, "waiting")

	// ルーレットを準備して開始、停止
	ready := prepareRoulette(t, wsConn1, wsConn2)
	started := startRouletteSpin(t, wsConn1, wsConn2, ready.RouletteSessionID)
	_ = stopRouletteSpin(t, wsConn1, wsConn2, ready.RouletteSessionID, started.SpinID)

	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.RLock()
	pendingOni := room.Roulette.PendingOniUserIDs
	oniCountBefore := room.OniCount
	room.mu.RUnlock()

	if len(pendingOni) == 0 {
		t.Fatalf("ルーレット停止後のPendingOniが空です")
	}

	// 別の設定（時間制限など、OniCount以外）を更新
	GameHub.UpdateRoomSettings(roomID, 1800, oniCountBefore, "300", 60, 60)

	room.mu.RLock()
	pendingOniAfter := room.Roulette.PendingOniUserIDs
	room.mu.RUnlock()

	if len(pendingOniAfter) == 0 {
		t.Fatalf("OniCount以外の設定更新でPendingOniが消えてしまいました (P0バグ)")
	}

	// OniCountを変更して設定更新
	GameHub.UpdateRoomSettings(roomID, 1800, oniCountBefore+1, "300", 60, 60)

	room.mu.RLock()
	pendingOniAfterChange := room.Roulette.PendingOniUserIDs
	room.mu.RUnlock()

	if len(pendingOniAfterChange) != 0 {
		t.Fatalf("OniCount変更時にもPendingOniがクリアされていません")
	}
}

func TestResetRestrictedToHost(t *testing.T) {
	roomID := "testRoom_reset_host"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"Player 1","color":"#7FB5FF"}`)
	_ = readUntilEvent(t, wsConn1, "waiting")

	wsConn2 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn2, `{"action":"join","user_id":"guest","name":"Guest Player","color":"#F64574"}`)
	_ = readUntilEvent(t, wsConn2, "waiting")

	// ゲーム開始 (ダミーで開始)
	ready := prepareRoulette(t, wsConn1, wsConn2)
	started := startRouletteSpin(t, wsConn1, wsConn2, ready.RouletteSessionID)
	_ = stopRouletteSpin(t, wsConn1, wsConn2, ready.RouletteSessionID, started.SpinID)

	sendJSON(t, wsConn1, `{"action":"start","oni_users":["player1"]}`)
	_ = readUntilEvent(t, wsConn1, "start")

	// ゲームを終了状態 (Status=2) に強制移行する
	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.Lock()
	room.Status = 2
	room.mu.Unlock()
	db.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 2)

	// ゲスト (wsConn2) から reset を送信
	sendJSON(t, wsConn2, `{"action":"reset"}`)
	err2 := readUntilEvent(t, wsConn2, "error")
	if !strings.Contains(err2.Message, "ホストのみ実行できます") {
		t.Fatalf("ゲストからのリセットが拒否されませんでした: %s", err2.Message)
	}

	// ホスト (wsConn1) から reset を送信
	sendJSON(t, wsConn1, `{"action":"reset"}`)
	resetWaiting := readUntilEvent(t, wsConn1, "waiting")
	if len(resetWaiting.Players) != 2 {
		t.Fatalf("ホストからのリセットが失敗しました: %+v", resetWaiting)
	}
}

func TestCaptureRequestValidatesPhotoURL(t *testing.T) {
	setTestSupabaseURL(t)

	roomID := "testRoom_capture_url"
	_, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"Player 1","color":"#7FB5FF"}`)
	_ = readUntilEvent(t, wsConn1, "waiting")

	wsConn2 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player2","name":"Player 2","color":"#F64574"}`)
	_ = readUntilEvent(t, wsConn2, "waiting")

	// ゲーム開始
	ready := prepareRoulette(t, wsConn1, wsConn2)
	started := startRouletteSpin(t, wsConn1, wsConn2, ready.RouletteSessionID)
	stopped := stopRouletteSpin(t, wsConn1, wsConn2, ready.RouletteSessionID, started.SpinID)

	oniUserID := stopped.SelectedOniUserIDs[0]
	var oniConn, runnerConn *websocket.Conn
	var runnerID string
	if oniUserID == "player1" {
		oniConn = wsConn1
		runnerConn = wsConn2
		runnerID = "player2"
	} else {
		oniConn = wsConn2
		runnerConn = wsConn1
		runnerID = "player1"
	}

	sendJSON(t, wsConn1, `{"action":"start","oni_users":[]}`)
	_ = readUntilEvent(t, wsConn1, "start")

	// ゲームをアクティブに
	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.Lock()
	room.IsGameActive = true
	room.mu.Unlock()

	// 無効なURLを送信
	sendJSON(t, oniConn, fmt.Sprintf(`{"action":"capture_request","target_id":%q,"photo_url":"http://malicious.com/hack.jpg"}`, runnerID))
	err1 := readUntilEvent(t, oniConn, "error")
	if !strings.Contains(err1.Message, "無効な写真URLです") {
		t.Fatalf("無効なURLでのキャプチャがエラーになりませんでした: %s", err1.Message)
	}

	// local URIを送信
	sendJSON(t, oniConn, fmt.Sprintf(`{"action":"capture_request","target_id":%q,"photo_url":"file:///tmp/capture.jpg"}`, runnerID))
	errLocal := readUntilEvent(t, oniConn, "error")
	if !strings.Contains(errLocal.Message, "無効な写真URLです") {
		t.Fatalf("local URIでのキャプチャがエラーになりませんでした: %s", errLocal.Message)
	}

	// 空のURLを送信
	sendJSON(t, oniConn, fmt.Sprintf(`{"action":"capture_request","target_id":%q,"photo_url":""}`, runnerID))
	err2 := readUntilEvent(t, oniConn, "error")
	if !strings.Contains(err2.Message, "証拠写真のURLがありません") {
		t.Fatalf("空のURLでのキャプチャがエラーになりませんでした: %s", err2.Message)
	}

	// fake URLを送信
	sendJSON(t, oniConn, fmt.Sprintf(`{"action":"capture_request","target_id":%q,"photo_url":"https://example.com/test.jpg"}`, runnerID))
	err3 := readUntilEvent(t, oniConn, "error")
	if !strings.Contains(err3.Message, "無効な写真URLです") {
		t.Fatalf("example.com URLでのキャプチャがエラーになりませんでした: %s", err3.Message)
	}

	// 有効なSupabase public URLを送信
	sendJSON(t, oniConn, fmt.Sprintf(`{"action":"capture_request","target_id":%q,"photo_url":%q}`, runnerID, testCapturePhotoURL))
	// エラーではなく、ターゲットへ capture_checking が飛ぶはず
	_ = readUntilEvent(t, runnerConn, "capture_checking")
}

func TestReconnectionRaceCondition(t *testing.T) {
	roomID := "reconnect_race_room"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
	})
	defer cleanup()

	// 1. クライアント1で接続して入室
	wsConn1 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"Player 1","color":"#7FB5FF"}`)
	_ = readUntilEvent(t, wsConn1, "waiting")

	// DBにプレイヤーが存在することを確認
	assertDBPlayerExists(t, db, roomID, "player1", true)

	// 2. 同じ userID で新しいコネクション (wsConn2) から接続して入室
	wsConn2 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn2, `{"action":"join","user_id":"player1","name":"Player 1","color":"#7FB5FF"}`)
	_ = readUntilEvent(t, wsConn2, "waiting")

	// 古いコネクション (wsConn1) が切断されるのを待つ
	time.Sleep(100 * time.Millisecond)

	// 3. 再接続したコネクションが正常に生きている & DBから削除されていないことを確認
	assertDBPlayerExists(t, db, roomID, "player1", true)

	// wsConn2 が生きており、何らかのメッセージが送れる・受信できることを確認
	sendJSON(t, wsConn2, `{"action":"update_color","color":"#F64574"}`)
	msg := readUntilEvent(t, wsConn2, "waiting")
	player, ok := findWaitingPlayer(msg.Players, "player1")
	if !ok || player.Color != "#F64574" {
		t.Fatalf("再接続後のカラー更新が反映されていません: got=%+v", msg.Players)
	}
}

func TestLeaveTransfersHostInResultState(t *testing.T) {
	roomID := "testRoom_leave_host_result"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:        roomID,
		Status:    0,
		TimeLimit: 900,
		OniCount:  1,
	})
	defer cleanup()

	wsConn1 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn1, `{"action":"join","user_id":"player1","name":"Player 1","color":"#7FB5FF"}`)
	_ = readUntilEvent(t, wsConn1, "waiting")

	wsConn2 := connectToRoom(t, baseURL, roomID)
	sendJSON(t, wsConn2, `{"action":"join","user_id":"guest","name":"Guest Player","color":"#F64574"}`)
	_ = readUntilEvent(t, wsConn2, "waiting")

	// ゲーム開始 (ダミーで開始)
	ready := prepareRoulette(t, wsConn1, wsConn2)
	started := startRouletteSpin(t, wsConn1, wsConn2, ready.RouletteSessionID)
	_ = stopRouletteSpin(t, wsConn1, wsConn2, ready.RouletteSessionID, started.SpinID)

	sendJSON(t, wsConn1, `{"action":"start","oni_users":["player1"]}`)
	_ = readUntilEvent(t, wsConn1, "start")

	// ゲームを終了状態 (Status=2) に強制移行する
	room := GameHub.GetOrCreateRoom(roomID)
	room.mu.Lock()
	room.Status = 2
	room.mu.Unlock()
	db.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 2)

	// ホストである player1 が leave を送信
	sendJSON(t, wsConn1, `{"action":"leave"}`)

	// guest が result イベントを受け取るはず (leave後のリザルト更新)
	_ = readUntilEvent(t, wsConn2, "result")

	// DBとメモリで guest がホストに移譲されているか確認
	var savedRoom models.Room
	if err := db.First(&savedRoom, "id = ?", roomID).Error; err != nil {
		t.Fatalf("ルーム取得失敗: %v", err)
	}
	if savedRoom.HostUserID != "guest" {
		t.Fatalf("DBのホストが移譲されていません: got=%s, want=guest", savedRoom.HostUserID)
	}

	if room.HostUserID != "guest" {
		t.Fatalf("メモリのホストが移譲されていません: got=%s, want=guest", room.HostUserID)
	}

	// 新しいホストである guest から reset を送信し、正常に待機画面に戻ることを確認
	sendJSON(t, wsConn2, `{"action":"reset"}`)
	resetWaiting := readUntilEvent(t, wsConn2, "waiting")
	if len(resetWaiting.Players) != 1 || resetWaiting.Players[0].UserID != "guest" {
		t.Fatalf("新ホストからのリセットが失敗しました: %+v", resetWaiting)
	}
}

func TestCaptureResponseChecksRoomID(t *testing.T) {
	setTestSupabaseURL(t)

	// Room A のセットアップ
	roomA := "room_A"
	db, baseURL, cleanup := newTestServer(t, models.Room{
		ID:           roomA,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		SyncInterval: 60,
		GracePeriod:  0,
	})
	defer cleanup()

	connA1 := connectToRoom(t, baseURL, roomA)
	defer connA1.Close()
	connA2 := connectToRoom(t, baseURL, roomA)
	defer connA2.Close()

	sendJSON(t, connA1, `{"action":"join","user_id":"player1","name":"Player 1"}`)
	readUntilEvent(t, connA1, "waiting")
	sendJSON(t, connA2, `{"action":"join","user_id":"player2","name":"Player 2"}`)
	readUntilEvent(t, connA1, "waiting")
	readUntilEvent(t, connA2, "waiting")

	sendJSON(t, connA1, `{"action":"start","oni_users":["player1"]}`)
	readUntilEvent(t, connA1, "start")
	readUntilEvent(t, connA2, "start")
	readUntilEvent(t, connA2, "game_active")

	// player1 (鬼) から player2 (逃走者) へのキャプチャ申請
	sendJSON(t, connA1, fmt.Sprintf(`{"action":"capture_request","target_id":"player2","photo_url":%q}`, testCapturePhotoURL))
	checking := readUntilEvent(t, connA2, "capture_checking")
	reqID := checking.RequestID
	if reqID == "" {
		t.Fatal("RequestIDが取得できませんでした")
	}

	// Room B のセットアップ
	roomB := "room_B"
	// Room B をDBに追加
	if err := db.Create(&models.Room{
		ID:           roomB,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		SyncInterval: 60,
		GracePeriod:  0,
	}).Error; err != nil {
		t.Fatalf("Room B 作成失敗: %v", err)
	}

	connB1 := connectToRoom(t, baseURL, roomB)
	defer connB1.Close()
	connB2 := connectToRoom(t, baseURL, roomB)
	defer connB2.Close()

	sendJSON(t, connB1, `{"action":"join","user_id":"player3","name":"Player 3"}`)
	readUntilEvent(t, connB1, "waiting")
	sendJSON(t, connB2, `{"action":"join","user_id":"player4","name":"Player 4"}`)
	readUntilEvent(t, connB1, "waiting")
	readUntilEvent(t, connB2, "waiting")

	sendJSON(t, connB1, `{"action":"start","oni_users":["player3"]}`)
	readUntilEvent(t, connB1, "start")
	readUntilEvent(t, connB2, "start")
	readUntilEvent(t, connB2, "game_active")

	// player4 (Room B の逃走者) が Room A の reqID に対して承認を試みる
	sendJSON(t, connB2, fmt.Sprintf(`{"action":"capture_response","approved":true,"request_id":%q}`, reqID))
	errB2 := readUntilEvent(t, connB2, "error")
	if !strings.Contains(errB2.Message, "この部屋の捕獲申請ではありません") {
		t.Fatalf("別ルームのキャプチャレスポンスが拒否されませんでした: %s", errB2.Message)
	}

	// DB上のリクエストのステータスが "pending" のままであることを確認
	var req models.CaptureRequest
	if err := db.First(&req, "id = ?", reqID).Error; err != nil {
		t.Fatalf("リクエスト取得失敗: %v", err)
	}
	if req.Status != "pending" {
		t.Fatalf("リクエストステータスが更新されてしまいました: %s", req.Status)
	}

	// 各プレイヤーが捕まっていないことを確認
	assertDBPlayerCaught(t, db, roomA, "player2", false)
	assertDBPlayerCaught(t, db, roomB, "player4", false)
}
