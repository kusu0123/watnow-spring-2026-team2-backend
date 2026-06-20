package main

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
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
	if err := db.AutoMigrate(&models.Room{}, &models.Player{}); err != nil {
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
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
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
		"time_limit": 900,
		"oni_count": 1,
		"area_size": "500m",
		"sync_interval": 180,
		"grace_period": 120,
		"area_center": {"lat": 34.0, "lng": 135.0}
	}`)

	settings1 := readHTTPTestUntilEvent(t, wsConn1, "room_settings")
	settings2 := readHTTPTestUntilEvent(t, wsConn2, "room_settings")
	assertHTTPTestRoomSettings(t, settings1, 900, 1, "500m", 180, 120, 34.0, 135.0)
	assertHTTPTestRoomSettings(t, settings2, 900, 1, "500m", 180, 120, 34.0, 135.0)

	var savedRoom models.Room
	if err := db.First(&savedRoom, "id = ?", roomID).Error; err != nil {
		t.Fatalf("ルーム取得失敗: %v", err)
	}
	if !savedRoom.HasAreaCenter || math.Abs(savedRoom.AreaCenterLat-34.0) > 0.000001 || math.Abs(savedRoom.AreaCenterLng-135.0) > 0.000001 {
		t.Fatalf("DBに保存されたarea_centerが不正です: %+v", savedRoom)
	}

	roomState := ws.GameHub.GetOrCreateRoom(roomID)
	stateSettings := roomState.RoomSettingsMessage()
	assertHTTPTestRoomSettings(t, ws.OutgoingMessage{
		Event:        stateSettings.Event,
		TimeLimit:    stateSettings.TimeLimit,
		OniCount:     stateSettings.OniCount,
		AreaSize:     stateSettings.AreaSize,
		SyncInterval: stateSettings.SyncInterval,
		GracePeriod:  stateSettings.GracePeriod,
		AreaCenter:   stateSettings.AreaCenter,
	}, 900, 1, "500m", 180, 120, 34.0, 135.0)
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
	_, server, _, cleanup := newHTTPTestServer(t, models.Room{
		ID:           roomID,
		Status:       0,
		TimeLimit:    900,
		OniCount:     1,
		AreaSize:     "500m",
		SyncInterval: 180,
		GracePeriod:  120,
	})
	defer cleanup()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "oniCountZero",
			body: `{"time_limit":900,"oni_count":0,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "oniCountFour",
			body: `{"time_limit":900,"oni_count":4,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "invalidTimeLimit",
			body: `{"time_limit":300,"oni_count":1,"area_size":"500m","sync_interval":180,"grace_period":120}`,
		},
		{
			name: "invalidSyncInterval",
			body: `{"time_limit":900,"oni_count":1,"area_size":"500m","sync_interval":90,"grace_period":120}`,
		},
		{
			name: "invalidGracePeriod",
			body: `{"time_limit":900,"oni_count":1,"area_size":"500m","sync_interval":180,"grace_period":30}`,
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
