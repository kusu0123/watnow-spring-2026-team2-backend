package main

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/watnow/watnow-spring-2026-team2-backend/models"
	"github.com/watnow/watnow-spring-2026-team2-backend/ws"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newHTTPTestServer(t *testing.T, room models.Room) (*gorm.DB, *httptest.Server, string, func()) {
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

	ws.GameHub = &ws.Hub{Rooms: make(map[string]*ws.RoomState)}

	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(setupRouter(db))
	wsBaseURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/rooms/"
	cleanup := func() {
		server.Close()
		_ = sqlDB.Close()
	}

	return db, server, wsBaseURL, cleanup
}

func connectHTTPTestRoom(t *testing.T, baseURL, roomID string) *websocket.Conn {
	t.Helper()

	wsConn, _, err := websocket.DefaultDialer.Dial(baseURL+roomID, nil)
	if err != nil {
		t.Fatalf("接続失敗: %v", err)
	}
	return wsConn
}

func sendHTTPTestWSJSON(t *testing.T, wsConn *websocket.Conn, body string) {
	t.Helper()

	if err := wsConn.WriteMessage(websocket.TextMessage, []byte(body)); err != nil {
		t.Fatalf("送信失敗: %v", err)
	}
}

func readHTTPTestWSMessage(t *testing.T, wsConn *websocket.Conn) ws.OutgoingMessage {
	t.Helper()

	wsConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := wsConn.ReadMessage()
	if err != nil {
		t.Fatalf("受信失敗: %v", err)
	}

	var msg ws.OutgoingMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("JSONパース失敗: %v", err)
	}
	return msg
}

func readHTTPTestUntilEvent(t *testing.T, wsConn *websocket.Conn, event string) ws.OutgoingMessage {
	t.Helper()

	for i := 0; i < 8; i++ {
		msg := readHTTPTestWSMessage(t, wsConn)
		if msg.Event == event {
			return msg
		}
	}

	t.Fatalf("%s イベントを受信できませんでした", event)
	return ws.OutgoingMessage{}
}

func joinHTTPTestClient(t *testing.T, wsConn *websocket.Conn, userID, name string) ws.OutgoingMessage {
	t.Helper()

	sendHTTPTestWSJSON(t, wsConn, `{"action":"join","user_id":"`+userID+`","name":"`+name+`"}`)
	if msg := readHTTPTestUntilEvent(t, wsConn, "waiting"); msg.Event != "waiting" {
		t.Fatalf("想定外のイベント: %+v", msg)
	}
	return readHTTPTestUntilEvent(t, wsConn, "room_settings")
}

func putRoomSettings(t *testing.T, server *httptest.Server, roomID, body string) {
	t.Helper()

	resp := putRoomSettingsRaw(t, server, roomID, body)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUTのステータスが不正です: status=%d body=%s", resp.StatusCode, payload)
	}
}

func putRoomSettingsRaw(t *testing.T, server *httptest.Server, roomID, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPut, server.URL+"/rooms/"+roomID, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("リクエスト作成失敗: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT失敗: %v", err)
	}
	return resp
}

func assertHTTPTestRoomSettings(t *testing.T, msg ws.OutgoingMessage, timeLimit, oniCount int, areaSize string, syncInterval, gracePeriod int, lat, lng float64) {
	t.Helper()

	if msg.Event != "room_settings" {
		t.Fatalf("room_settingsではないイベントを受信しました: %+v", msg)
	}
	if msg.TimeLimit != timeLimit || msg.OniCount != oniCount || msg.AreaSize != areaSize || msg.SyncInterval != syncInterval || msg.GracePeriod != gracePeriod {
		t.Fatalf("room_settingsの設定値が不正です: %+v", msg)
	}
	if msg.AreaCenter == nil {
		t.Fatalf("area_centerが含まれていません: %+v", msg)
	}
	if math.Abs(msg.AreaCenter.Lat-lat) > 0.000001 || math.Abs(msg.AreaCenter.Lng-lng) > 0.000001 {
		t.Fatalf("area_centerが不正です: %+v", msg.AreaCenter)
	}
}

func TestHealthzReturnsOK(t *testing.T) {
	_, server, _, cleanup := newHTTPTestServer(t, models.Room{})
	defer cleanup()

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /healthz status mismatch: status=%d body=%s", resp.StatusCode, payload)
	}

	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("GET /healthz JSON decode failed: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("GET /healthz payload mismatch: %+v", payload)
	}
}

func TestHealthzHeadReturnsOK(t *testing.T) {
	_, server, _, cleanup := newHTTPTestServer(t, models.Room{})
	defer cleanup()

	resp, err := http.Head(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("HEAD /healthz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD /healthz status mismatch: status=%d", resp.StatusCode)
	}
}

func TestServerAddrUsesPortEnv(t *testing.T) {
	t.Setenv("PORT", "10000")

	if got := serverAddr(); got != "0.0.0.0:10000" {
		t.Fatalf("serverAddr() = %q, want %q", got, "0.0.0.0:10000")
	}
}

func TestServerAddrDefaultsTo8080(t *testing.T) {
	t.Setenv("PORT", "")

	if got := serverAddr(); got != "0.0.0.0:8080" {
		t.Fatalf("serverAddr() = %q, want %q", got, "0.0.0.0:8080")
	}
}

func TestPutRoomSettingsBroadcastsAndSavesAreaCenter(t *testing.T) {
	roomID := "putSettingsRoom"
	db, server, wsBaseURL, cleanup := newHTTPTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		AreaSize:     "300m",
		SyncInterval: 60,
		GracePeriod:  30,
	})
	defer cleanup()

	wsConn1 := connectHTTPTestRoom(t, wsBaseURL, roomID)
	defer wsConn1.Close()
	wsConn2 := connectHTTPTestRoom(t, wsBaseURL, roomID)
	defer wsConn2.Close()

	initialSettings := joinHTTPTestClient(t, wsConn1, "player1", "はるき")
	if initialSettings.AreaCenter != nil {
		t.Fatalf("初期area_centerはnull想定です: %+v", initialSettings.AreaCenter)
	}
	joinHTTPTestClient(t, wsConn2, "player2", "みな")

	putRoomSettings(t, server, roomID, `{
		"user_id": "player1",
		"time_limit": 900,
		"oni_count": 1,
		"max_players": 8,
		"area_size": "500m",
		"sync_interval": 180,
		"grace_period": 120,
		"mission_enabled": true,
		"area_center": {"lat": 34.0, "lng": 135.0}
	}`)

	settings1 := readHTTPTestUntilEvent(t, wsConn1, "room_settings")
	settings2 := readHTTPTestUntilEvent(t, wsConn2, "room_settings")
	assertHTTPTestRoomSettings(t, settings1, 900, 1, "500m", 180, 120, 34.0, 135.0)
	assertHTTPTestRoomSettings(t, settings2, 900, 1, "500m", 180, 120, 34.0, 135.0)
	if settings1.MaxPlayers != 8 || settings2.MaxPlayers != 8 {
		t.Fatalf("max_playersがbroadcastされていません: settings1=%+v settings2=%+v", settings1, settings2)
	}
	if !settings1.MissionEnabled || !settings2.MissionEnabled {
		t.Fatalf("mission_enabledがbroadcastされていません: settings1=%+v settings2=%+v", settings1, settings2)
	}

	var savedRoom models.Room
	if err := db.First(&savedRoom, "id = ?", roomID).Error; err != nil {
		t.Fatalf("ルーム取得失敗: %v", err)
	}
	if !savedRoom.MissionEnabled {
		t.Fatalf("DBにmission_enabledが保存されていません: %+v", savedRoom)
	}
	if savedRoom.MaxPlayers != 8 {
		t.Fatalf("DBにmax_playersが保存されていません: %+v", savedRoom)
	}
	if !savedRoom.HasAreaCenter || math.Abs(savedRoom.AreaCenterLat-34.0) > 0.000001 || math.Abs(savedRoom.AreaCenterLng-135.0) > 0.000001 {
		t.Fatalf("DBに保存されたarea_centerが不正です: %+v", savedRoom)
	}

	roomState := ws.GameHub.GetOrCreateRoom(roomID)
	stateSettings := roomState.RoomSettingsMessage()
	if !stateSettings.MissionEnabled {
		t.Fatalf("RoomStateにmission_enabledが反映されていません: %+v", stateSettings)
	}
	if stateSettings.MaxPlayers != 8 {
		t.Fatalf("RoomStateにmax_playersが反映されていません: %+v", stateSettings)
	}
	assertHTTPTestRoomSettings(t, ws.OutgoingMessage{
		Event:          stateSettings.Event,
		TimeLimit:      stateSettings.TimeLimit,
		OniCount:       stateSettings.OniCount,
		MaxPlayers:     stateSettings.MaxPlayers,
		AreaSize:       stateSettings.AreaSize,
		SyncInterval:   stateSettings.SyncInterval,
		GracePeriod:    stateSettings.GracePeriod,
		MissionEnabled: stateSettings.MissionEnabled,
		AreaCenter:     stateSettings.AreaCenter,
	}, 900, 1, "500m", 180, 120, 34.0, 135.0)
}

func TestPutRoomSettingsRejectsNonHostOrMissingUserID(t *testing.T) {
	roomID := "putSettingsAuthRoom"
	db, server, _, cleanup := newHTTPTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		MaxPlayers:   6,
		AreaSize:     "300m",
		SyncInterval: 60,
		GracePeriod:  60,
		HostUserID:   "player1",
	})
	defer cleanup()

	for _, player := range []models.Player{
		{ID: roomID + ":player1", RoomID: roomID, UserID: "player1", Name: "はるき"},
		{ID: roomID + ":player2", RoomID: roomID, UserID: "player2", Name: "みな"},
	} {
		if err := db.Create(&player).Error; err != nil {
			t.Fatalf("テストプレイヤー作成失敗: %v", err)
		}
	}

	validSettings := `"time_limit":900,"oni_count":1,"max_players":6,"area_size":"300m","sync_interval":60,"grace_period":60`
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missingUserID",
			body: `{` + validSettings + `}`,
		},
		{
			name: "guestUserID",
			body: `{"user_id":"player2",` + validSettings + `}`,
		},
		{
			name: "unknownUserID",
			body: `{"user_id":"ghost",` + validSettings + `}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := putRoomSettingsRaw(t, server, roomID, tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				payload, _ := io.ReadAll(resp.Body)
				t.Fatalf("host以外の設定更新が拒否されていません: status=%d body=%s", resp.StatusCode, payload)
			}
		})
	}

	if err := db.Model(&models.Room{}).Where("id = ?", roomID).Update("host_user_id", "ghost").Error; err != nil {
		t.Fatalf("host_user_id更新失敗: %v", err)
	}
	resp := putRoomSettingsRaw(t, server, roomID, `{"user_id":"ghost",`+validSettings+`}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("存在しないhost user_idが拒否されていません: status=%d body=%s", resp.StatusCode, payload)
	}
}

func TestPutRoomSettingsWithoutAreaCenterPreservesExistingCenter(t *testing.T) {
	roomID := "preserveCenterRoom"
	db, server, wsBaseURL, cleanup := newHTTPTestServer(t, models.Room{
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

	wsConn := connectHTTPTestRoom(t, wsBaseURL, roomID)
	defer wsConn.Close()
	joinSettings := joinHTTPTestClient(t, wsConn, "player1", "はるき")
	assertHTTPTestRoomSettings(t, joinSettings, 900, 1, "500m", 180, 120, 34.0, 135.0)

	putRoomSettings(t, server, roomID, `{
		"user_id": "player1",
		"time_limit": 600,
		"oni_count": 2,
		"area_size": "700m",
		"sync_interval": 60,
		"grace_period": 60
	}`)

	settings := readHTTPTestUntilEvent(t, wsConn, "room_settings")
	assertHTTPTestRoomSettings(t, settings, 600, 2, "700m", 60, 60, 34.0, 135.0)

	var savedRoom models.Room
	if err := db.First(&savedRoom, "id = ?", roomID).Error; err != nil {
		t.Fatalf("ルーム取得失敗: %v", err)
	}
	if !savedRoom.HasAreaCenter || math.Abs(savedRoom.AreaCenterLat-34.0) > 0.000001 || math.Abs(savedRoom.AreaCenterLng-135.0) > 0.000001 {
		t.Fatalf("area_centerが保持されていません: %+v", savedRoom)
	}

	stateSettings := ws.GameHub.GetOrCreateRoom(roomID).RoomSettingsMessage()
	if stateSettings.AreaCenter == nil || math.Abs(stateSettings.AreaCenter.Lat-34.0) > 0.000001 || math.Abs(stateSettings.AreaCenter.Lng-135.0) > 0.000001 {
		t.Fatalf("RoomStateのarea_centerが保持されていません: %+v", stateSettings.AreaCenter)
	}
}

func TestPutRoomSettingsRejectsInvalidStep3Values(t *testing.T) {
	roomID := "invalidSettingsRoom"
	db, server, _, cleanup := newHTTPTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		AreaSize:     "500m",
		SyncInterval: 180,
		GracePeriod:  120,
		HostUserID:   "player1",
	})
	defer cleanup()
	if err := db.Create(&models.Player{
		ID:     roomID + ":player1",
		RoomID: roomID,
		UserID: "player1",
		Name:   "はるき",
	}).Error; err != nil {
		t.Fatalf("host player作成失敗: %v", err)
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "oniCountZero",
			body: `{"user_id":"player1","time_limit":900,"oni_count":0,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "oniCountFour",
			body: `{"user_id":"player1","time_limit":900,"oni_count":4,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "maxPlayersOne",
			body: `{"user_id":"player1","time_limit":900,"oni_count":1,"max_players":1,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "maxPlayersSixteen",
			body: `{"user_id":"player1","time_limit":900,"oni_count":1,"max_players":16,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "maxPlayersEqualsOniCount",
			body: `{"user_id":"player1","time_limit":900,"oni_count":2,"max_players":2,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "invalidTimeLimit",
			body: `{"user_id":"player1","time_limit":300,"oni_count":1,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "invalidSyncInterval",
			body: `{"user_id":"player1","time_limit":900,"oni_count":1,"area_size":"500m","sync_interval":90,"grace_period":120}`,
		},
		{
			name: "invalidGracePeriod",
			body: `{"user_id":"player1","time_limit":900,"oni_count":1,"area_size":"500m","sync_interval":180,"grace_period":30}`,
		},
		{
			name: "invalidMissionEnabledType",
			body: `{"user_id":"player1","time_limit":900,"oni_count":1,"area_size":"500m","sync_interval":180,"grace_period":120,"mission_enabled":"yes"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := putRoomSettingsRaw(t, server, roomID, tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				payload, _ := io.ReadAll(resp.Body)
				t.Fatalf("不正な設定が拒否されていません: status=%d body=%s", resp.StatusCode, payload)
			}
		})
	}
}

func TestPutRoomSettingsRejectsMaxPlayersBelowCurrentPlayers(t *testing.T) {
	roomID := "maxPlayersCurrentRoom"
	db, server, _, cleanup := newHTTPTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		MaxPlayers:   6,
		AreaSize:     "500m",
		SyncInterval: 180,
		GracePeriod:  120,
		HostUserID:   "player1",
	})
	defer cleanup()

	for _, userID := range []string{"player1", "player2", "player3"} {
		if err := db.Create(&models.Player{
			ID:     roomID + ":" + userID,
			RoomID: roomID,
			UserID: userID,
			Name:   userID,
		}).Error; err != nil {
			t.Fatalf("テストプレイヤー作成失敗: %v", err)
		}
	}

	resp := putRoomSettingsRaw(t, server, roomID, `{
		"user_id": "player1",
		"time_limit": 900,
		"oni_count": 1,
		"max_players": 2,
		"area_size": "500m",
		"sync_interval": 180,
		"grace_period": 120
	}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("現在人数未満のmax_playersが拒否されていません: status=%d body=%s", resp.StatusCode, payload)
	}
}

func TestRoulettePendingNotClearedBySettingsUpdateAndSourceOfTruth(t *testing.T) {
	roomID := "integrationRouletteSettingsTestRoom"
	_, server, wsBaseURL, cleanup := newHTTPTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		MaxPlayers:   6,
		AreaSize:     "500m",
		SyncInterval: 180,
		GracePeriod:  120,
	})
	defer cleanup()

	// 1. ws connection 1 (host) & ws connection 2
	wsConn1 := connectHTTPTestRoom(t, wsBaseURL, roomID)
	defer wsConn1.Close()

	_ = joinHTTPTestClient(t, wsConn1, "player1", "はるき")

	wsConn2 := connectHTTPTestRoom(t, wsBaseURL, roomID)
	defer wsConn2.Close()

	_ = joinHTTPTestClient(t, wsConn2, "player2", "みな")

	// 2. prepare_roulette -> roulette_start -> roulette_stop
	sendHTTPTestWSJSON(t, wsConn1, `{"action":"prepare_roulette"}`)
	ready := readHTTPTestUntilEvent(t, wsConn1, "roulette_ready")
	_ = readHTTPTestUntilEvent(t, wsConn2, "roulette_ready")

	sendHTTPTestWSJSON(t, wsConn1, `{"action":"roulette_start","roulette_session_id":"`+ready.RouletteSessionID+`"}`)
	started := readHTTPTestUntilEvent(t, wsConn1, "roulette_spin_started")
	_ = readHTTPTestUntilEvent(t, wsConn2, "roulette_spin_started")

	sendHTTPTestWSJSON(t, wsConn1, `{"action":"roulette_stop","roulette_session_id":"`+ready.RouletteSessionID+`","spin_id":`+strconv.Itoa(started.SpinID)+`}`)
	stopped := readHTTPTestUntilEvent(t, wsConn1, "roulette_spin_stopped")
	_ = readHTTPTestUntilEvent(t, wsConn2, "roulette_spin_stopped")

	if len(stopped.SelectedOniUserIDs) == 0 {
		t.Fatalf("鬼が決定されていません")
	}
	selectedOni := stopped.SelectedOniUserIDs[0]

	// 3. PUT /rooms/:id settings update (change time_limit to 1800, keep oni_count=1)
	putRoomSettings(t, server, roomID, `{
		"user_id": "player1",
		"time_limit": 1800,
		"oni_count": 1,
		"max_players": 6,
		"area_size": "500m",
		"sync_interval": 180,
		"grace_period": 120
	}`)

	// 4. start (with empty oni_users to verify the pending roulette is used)
	sendHTTPTestWSJSON(t, wsConn1, `{"action":"start"}`)
	start1 := readHTTPTestUntilEvent(t, wsConn1, "start")
	start2 := readHTTPTestUntilEvent(t, wsConn2, "start")

	// Verify that the selected oni matches what roulette chose
	if len(start1.OniUsers) != 1 || start1.OniUsers[0] != selectedOni {
		t.Fatalf("開始後の鬼がルーレットで決まった鬼 (%s) と一致しません: %+v", selectedOni, start1.OniUsers)
	}
	if len(start2.OniUsers) != 1 || start2.OniUsers[0] != selectedOni {
		t.Fatalf("開始後の鬼がルーレットで決まった鬼 (%s) と一致しません: %+v", selectedOni, start2.OniUsers)
	}
}
