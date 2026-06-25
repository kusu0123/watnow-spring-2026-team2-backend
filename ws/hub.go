package ws

import (
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/watnow/watnow-spring-2026-team2-backend/models"
	"gorm.io/gorm"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 本番環境では適切にCORSの設定をしてください
	},
}

const (
	minStartPlayers        = 2
	minRoomPlayers         = 2
	defaultMaxPlayers      = 6
	maxRoomPlayers         = 15
	minOniUsers            = 1
	maxOniUsers            = 3
	rouletteDecelerationMS = 2500
	rouletteStatusReady    = "ready"
	rouletteStatusSpinning = "spinning"
	rouletteStatusStopped  = "stopped"
)

type Client struct {
	Conn           *websocket.Conn
	PlayerID       string
	UserID         string
	RoomID         string
	Name           string
	Role           int
	IsCaught       bool
	Lat            float64
	Lng            float64
	HasLocation    bool
	Color          string
	PhotoURL       string
	CapturedAt     *time.Time
	LeftExplicitly bool
	mu             sync.Mutex
}

type RouletteState struct {
	SessionID         string
	SpinID            int
	Status            string
	RouletteOrder     []string
	PendingOniUserIDs []string
	StartedAt         *time.Time
	StoppedAt         *time.Time
}

type RoomState struct {
	Status         int
	TimeLimit      int
	OniCount       int // 追加
	MaxPlayers     int
	AreaSize       string
	SyncInterval   int // 追加
	GracePeriod    int // 追加
	MissionEnabled bool
	AreaCenterLat  float64
	AreaCenterLng  float64
	HasAreaCenter  bool
	HostUserID     string
	StartAt        time.Time
	ActiveAt       time.Time
	IsGameActive   bool
	Clients        map[*Client]bool
	IsGMLoopActive bool // 追加：GMの重複起動防止
	LoopID         uint64
	Roulette       RouletteState
	mu             sync.RWMutex
}

type Hub struct {
	Rooms map[string]*RoomState
	mu    sync.RWMutex
}

var GameHub = &Hub{
	Rooms: make(map[string]*RoomState),
}

func makePlayerID(roomID, userID string) string {
	return roomID + ":" + userID
}

func sendError(client *Client, message string) {
	// ▼ サーバー側（ターミナル）にログを出力
	client.mu.Lock()
	log.Printf("[Error] Room: %s | User: %s | Message: %s\n", client.RoomID, client.UserID, message)

	// ユーザー（スマホやPC）にエラーメッセージを送信
	_ = client.Conn.WriteJSON(OutgoingMessage{
		Event:   "error",
		Message: message,
	})
	client.mu.Unlock()
}

func normalizeHexColor(color string) (string, bool) {
	if len(color) != 7 || color[0] != '#' {
		return "", false
	}
	for _, ch := range color[1:] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return "", false
		}
	}
	return strings.ToUpper(color), true
}

func normalizePlayerName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	length := len([]rune(trimmed))
	return trimmed, length >= 1 && length <= 12
}

func isValidPhotoURL(urlStr string) bool {
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return false
	}

	supabaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("SUPABASE_URL")), "/")
	if supabaseURL == "" {
		return false
	}

	baseURL, err := url.Parse(supabaseURL)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" {
		return false
	}

	photoURL, err := url.Parse(urlStr)
	if err != nil || photoURL.Scheme != "https" || photoURL.Host != baseURL.Host {
		return false
	}

	basePath := strings.TrimRight(baseURL.EscapedPath(), "/")
	publicPrefix := basePath + "/storage/v1/object/public/"
	return strings.HasPrefix(photoURL.EscapedPath(), publicPrefix)
}

func (h *Hub) UpdateRoomSettings(roomID string, timeLimit, oniCount int, areaSize string, syncInterval, gracePeriod int) {
	timeLimit, syncInterval, gracePeriod = cleanGameSettings(timeLimit, syncInterval, gracePeriod)

	room := h.GetOrCreateRoom(roomID)
	room.mu.Lock()
	defer room.mu.Unlock()
	if room.OniCount != oniCount {
		clearPendingRouletteLocked(room)
	}
	room.TimeLimit = timeLimit
	room.OniCount = oniCount
	if room.MaxPlayers == 0 {
		room.MaxPlayers = defaultMaxPlayers
	}
	room.AreaSize = areaSize
	room.SyncInterval = syncInterval
	room.GracePeriod = gracePeriod
}

func (h *Hub) UpdateRoomSettingsFromModel(room models.Room) *RoomState {
	timeLimit, syncInterval, gracePeriod := cleanGameSettings(room.TimeLimit, room.SyncInterval, room.GracePeriod)

	roomState := h.GetOrCreateRoom(room.ID)
	roomState.mu.Lock()
	defer roomState.mu.Unlock()
	if roomState.OniCount != room.OniCount {
		clearPendingRouletteLocked(roomState)
	}
	roomState.Status = room.Status
	roomState.TimeLimit = timeLimit
	roomState.OniCount = room.OniCount
	roomState.MaxPlayers = cleanMaxPlayers(room.MaxPlayers)
	roomState.AreaSize = room.AreaSize
	roomState.SyncInterval = syncInterval
	roomState.GracePeriod = gracePeriod
	roomState.MissionEnabled = room.MissionEnabled
	roomState.AreaCenterLat = room.AreaCenterLat
	roomState.AreaCenterLng = room.AreaCenterLng
	roomState.HasAreaCenter = room.HasAreaCenter
	roomState.HostUserID = room.HostUserID
	return roomState
}

func (h *Hub) GetOrCreateRoom(roomID string) *RoomState {
	h.mu.Lock()
	defer h.mu.Unlock()

	room, ok := h.Rooms[roomID]
	if !ok {
		room = &RoomState{
			Status:     0,
			TimeLimit:  900,
			MaxPlayers: defaultMaxPlayers,
			Clients:    make(map[*Client]bool),
		}
		h.Rooms[roomID] = room
	}
	return room
}

func (h *Hub) GetRoom(roomID string) *RoomState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.Rooms[roomID]
}

func (h *Hub) Register(roomID string, client *Client) {
	room := h.GetOrCreateRoom(roomID)

	client.mu.Lock()
	userID := client.UserID
	client.mu.Unlock()

	room.mu.Lock()
	for oldClient := range room.Clients {
		if oldClient == client {
			continue
		}

		oldClient.mu.Lock()
		sameUser := userID != "" && oldClient.UserID == userID
		oldClient.mu.Unlock()

		if sameUser {
			delete(room.Clients, oldClient)
			oldClient.mu.Lock()
			_ = oldClient.Conn.Close()
			oldClient.mu.Unlock()
		}
	}
	room.Clients[client] = true
	room.mu.Unlock()
}

func (h *Hub) removeClientFromRoom(roomID string, client *Client) (*RoomState, int, bool) {
	h.mu.RLock()
	room, ok := h.Rooms[roomID]
	h.mu.RUnlock()
	if !ok {
		return nil, 0, false
	}

	room.mu.Lock()
	status := room.Status
	delete(room.Clients, client)
	isEmpty := len(room.Clients) == 0
	room.mu.Unlock()

	return room, status, isEmpty
}

func roomStatus(room *RoomState) int {
	room.mu.RLock()
	defer room.mu.RUnlock()
	return room.Status
}

func roomPlayerCount(db *gorm.DB, roomID string) (int64, error) {
	var count int64
	if err := db.Model(&models.Player{}).Where("room_id = ?", roomID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func isReservedPlayerColor(color string) bool {
	return strings.EqualFold(color, "#000000") || strings.EqualFold(color, "black")
}

var (
	errNoAvailablePlayerColor = errors.New("利用可能なカラーがありません")
	playerColorPalette        = []string{
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
)

func isPlayerPaletteColor(color string) bool {
	normalizedColor, ok := normalizeHexColor(color)
	if !ok {
		return false
	}
	for _, paletteColor := range playerColorPalette {
		if normalizedColor == paletteColor {
			return true
		}
	}
	return false
}

func colorUsedByOtherPlayer(db *gorm.DB, roomID, userID, color string) (bool, error) {
	var players []models.Player
	if err := db.Where("room_id = ?", roomID).Find(&players).Error; err != nil {
		return false, err
	}
	for _, player := range players {
		if player.UserID != userID && strings.EqualFold(player.Color, color) {
			return true, nil
		}
	}
	return false, nil
}

func usedPlayerColors(db *gorm.DB, roomID, userID string) (map[string]bool, error) {
	var players []models.Player
	if err := db.Where("room_id = ?", roomID).Find(&players).Error; err != nil {
		return nil, err
	}

	used := make(map[string]bool, len(players))
	for _, player := range players {
		if player.UserID == userID {
			continue
		}
		normalizedColor, ok := normalizeHexColor(player.Color)
		if ok {
			used[normalizedColor] = true
		}
	}
	return used, nil
}

func firstAvailablePlayerColor(db *gorm.DB, roomID, userID string) (string, error) {
	used, err := usedPlayerColors(db, roomID, userID)
	if err != nil {
		return "", err
	}
	for _, color := range playerColorPalette {
		if !used[color] {
			return color, nil
		}
	}
	return "", errNoAvailablePlayerColor
}

func safeJoinColor(db *gorm.DB, roomID, userID, requestedColor string) (string, error) {
	if requestedColor == "" || isReservedPlayerColor(requestedColor) {
		return firstAvailablePlayerColor(db, roomID, userID)
	}
	normalizedColor, ok := normalizeHexColor(requestedColor)
	if !ok || !isPlayerPaletteColor(normalizedColor) {
		return firstAvailablePlayerColor(db, roomID, userID)
	}

	used, err := colorUsedByOtherPlayer(db, roomID, userID, normalizedColor)
	if err != nil {
		return "", err
	}
	if used {
		return firstAvailablePlayerColor(db, roomID, userID)
	}
	return normalizedColor, nil
}

func clearPendingRouletteLocked(room *RoomState) {
	room.Roulette = RouletteState{}
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func timePtr(t time.Time) *time.Time {
	utc := t.UTC()
	return &utc
}

func rouletteReadyMessage(sessionID string, order []string) OutgoingMessage {
	return OutgoingMessage{
		Event:             "roulette_ready",
		RouletteSessionID: sessionID,
		RouletteOrder:     copyStrings(order),
	}
}

func rouletteSpinStartedMessage(sessionID string, spinID int, order []string, startsAt time.Time) OutgoingMessage {
	if startsAt.IsZero() {
		startsAt = time.Now().UTC()
	}
	return OutgoingMessage{
		Event:             "roulette_spin_started",
		RouletteSessionID: sessionID,
		SpinID:            spinID,
		RouletteOrder:     copyStrings(order),
		StartsAt:          startsAt.UTC().Format(time.RFC3339Nano),
	}
}

func rouletteSpinStoppedMessage(sessionID string, spinID int, selectedOniUserIDs []string, stopAt time.Time) OutgoingMessage {
	if stopAt.IsZero() {
		stopAt = time.Now().UTC()
	}
	return OutgoingMessage{
		Event:              "roulette_spin_stopped",
		RouletteSessionID:  sessionID,
		SpinID:             spinID,
		SelectedOniUserIDs: copyStrings(selectedOniUserIDs),
		StopAt:             stopAt.UTC().Format(time.RFC3339Nano),
		DecelerationMS:     rouletteDecelerationMS,
	}
}

func rouletteResetMessage(sessionID string) OutgoingMessage {
	return OutgoingMessage{
		Event:             "roulette_reset",
		RouletteSessionID: sessionID,
	}
}

func connectedUserIDsLocked(room *RoomState) []string {
	userIDs := make([]string, 0, len(room.Clients))
	for c := range room.Clients {
		c.mu.Lock()
		userID := c.UserID
		c.mu.Unlock()
		if userID != "" {
			userIDs = append(userIDs, userID)
		}
	}
	sort.Strings(userIDs)
	return userIDs
}

func selectOniUserIDs(userIDs []string, oniCount int) []string {
	candidates := copyStrings(userIDs)
	rand.New(rand.NewSource(time.Now().UnixNano())).Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	selected := candidates[:oniCount]
	sort.Strings(selected)
	return copyStrings(selected)
}

func validateRoulettePlayersLocked(room *RoomState, userIDs []string) string {
	playerCount := len(userIDs)
	if playerCount < minStartPlayers {
		return "ゲーム開始には2人以上必要です"
	}
	if room.OniCount < minOniUsers || room.OniCount > maxOniUsers {
		return "鬼は1〜3人で指定してください"
	}
	if room.OniCount >= playerCount {
		return "全員を鬼にはできません"
	}
	return ""
}

func joinedClientIdentity(client *Client) (string, string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.UserID, client.PlayerID
}

func syncRoomHost(room *RoomState, hostUserID string) {
	room.mu.Lock()
	room.HostUserID = hostUserID
	room.mu.Unlock()
}

func ensureRoomHost(db *gorm.DB, roomID, fallbackUserID string) (string, error) {
	var room models.Room
	if err := db.First(&room, "id = ?", roomID).Error; err != nil {
		return "", err
	}
	if room.HostUserID != "" || fallbackUserID == "" {
		return room.HostUserID, nil
	}
	if err := db.Model(&models.Room{}).Where("id = ?", roomID).Update("host_user_id", fallbackUserID).Error; err != nil {
		return "", err
	}
	return fallbackUserID, nil
}

func transferRoomHostIfNeeded(db *gorm.DB, roomID, leavingUserID string) (string, error) {
	var room models.Room
	if err := db.First(&room, "id = ?", roomID).Error; err != nil {
		return "", err
	}
	if room.HostUserID == "" || room.HostUserID != leavingUserID {
		return room.HostUserID, nil
	}

	var nextPlayer models.Player
	err := db.Where("room_id = ?", roomID).Order("user_id ASC").First(&nextPlayer).Error
	newHostUserID := ""
	if err == nil {
		newHostUserID = nextPlayer.UserID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	if err := db.Model(&models.Room{}).Where("id = ?", roomID).Update("host_user_id", newHostUserID).Error; err != nil {
		return "", err
	}
	return newHostUserID, nil
}

func (room *RoomState) isHost(userID string) bool {
	room.mu.RLock()
	defer room.mu.RUnlock()
	if room.HostUserID == "" {
		return false
	}
	return room.HostUserID == userID
}

func oniUsersForRoom(db *gorm.DB, roomID string) ([]string, error) {
	var players []models.Player
	if err := db.Where("room_id = ? AND role = ?", roomID, 1).Order("user_id ASC").Find(&players).Error; err != nil {
		return nil, err
	}

	oniUsers := make([]string, 0, len(players))
	for _, player := range players {
		oniUsers = append(oniUsers, player.UserID)
	}
	return oniUsers, nil
}

func sendRoomSettingsToClient(roomID string, client *Client, room *RoomState, userID string) {
	if err := sendToClient(client, room.RoomSettingsMessage()); err != nil {
		log.Printf("[Info] Room: %s | User: %s | room_settings送信に失敗しました: %v\n", roomID, userID, err)
	}
}

func startGameLoopIfNeeded(roomID string, room *RoomState, db *gorm.DB) {
	startLoop := false
	var loopID uint64
	room.mu.Lock()
	if room.Status == 1 && !room.IsGMLoopActive {
		room.LoopID++
		loopID = room.LoopID
		room.IsGMLoopActive = true
		startLoop = true
	}
	room.mu.Unlock()

	if startLoop {
		go runGameLoop(roomID, room, db, loopID)
	}
}

func sendActiveStateToClient(roomID string, client *Client, room *RoomState, db *gorm.DB) {
	oniUsers, err := oniUsersForRoom(db, roomID)
	if err != nil {
		log.Printf("[Error] Room: %s | 鬼一覧の取得に失敗しました: %v\n", roomID, err)
		oniUsers = nil
	}

	client.mu.Lock()
	role := client.Role
	userID := client.UserID
	client.mu.Unlock()

	room.mu.RLock()
	timeLimit := room.TimeLimit
	isGameActive := room.IsGameActive
	room.mu.RUnlock()

	if err := sendToClient(client, OutgoingMessage{
		Event:     "start",
		Role:      &role,
		TimeLimit: timeLimit,
		OniUsers:  oniUsers,
	}); err != nil {
		log.Printf("[Info] Room: %s | User: %s | start再送に失敗しました: %v\n", roomID, userID, err)
		return
	}

	if isGameActive {
		if err := sendToClient(client, OutgoingMessage{Event: "game_active"}); err != nil {
			log.Printf("[Info] Room: %s | User: %s | game_active再送に失敗しました: %v\n", roomID, userID, err)
			return
		}
		if err := sendToClient(client, room.syncMessageFor(client)); err != nil {
			log.Printf("[Info] Room: %s | User: %s | sync再送に失敗しました: %v\n", roomID, userID, err)
		}
	}
}

func resetRoomForReplay(roomID string, room *RoomState, db *gorm.DB) (map[string]string, error) {
	clients := room.clientList()
	connectedUserIDs := make([]string, 0, len(clients))
	for _, client := range clients {
		client.mu.Lock()
		userID := client.UserID
		client.mu.Unlock()
		if userID != "" {
			connectedUserIDs = append(connectedUserIDs, userID)
		}
	}

	preservedColors := make(map[string]string, len(connectedUserIDs))
	return preservedColors, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 0).Error; err != nil {
			return err
		}
		if len(connectedUserIDs) > 0 {
			var players []models.Player
			if err := tx.Where("room_id = ? AND user_id IN ?", roomID, connectedUserIDs).Find(&players).Error; err != nil {
				return err
			}
			for _, player := range players {
				preservedColors[player.UserID] = player.Color
			}
			if err := tx.Model(&models.Player{}).Where("room_id = ? AND user_id IN ?", roomID, connectedUserIDs).Updates(map[string]interface{}{
				"role":         0,
				"is_caught":    false,
				"lat":          0,
				"lng":          0,
				"has_location": false,
				"photo_url":    "",
				"captured_at":  nil,
			}).Error; err != nil {
				return err
			}
			if err := tx.Where("room_id = ? AND user_id NOT IN ?", roomID, connectedUserIDs).Delete(&models.Player{}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Where("room_id = ?", roomID).Delete(&models.Player{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (h *Hub) Unregister(roomID string, client *Client, db *gorm.DB) {
	h.mu.Lock()
	room, ok := h.Rooms[roomID]
	h.mu.Unlock()

	if !ok {
		return
	}

	room.mu.Lock()
	if _, ok := room.Clients[client]; ok {
		delete(room.Clients, client)
		client.mu.Lock()
		_ = client.Conn.Close()
		client.mu.Unlock()
	}
	//部屋に誰もいなくなったかチェック
	isEmpty := len(room.Clients) == 0
	status := room.Status
	room.mu.Unlock()

	client.mu.Lock()
	leftExplicitly := client.LeftExplicitly
	userID := client.UserID
	client.mu.Unlock()

	hasActiveConnection := false
	if userID != "" {
		room.mu.Lock()
		for c := range room.Clients {
			if c != client {
				c.mu.Lock()
				sameUser := c.UserID == userID
				c.mu.Unlock()
				if sameUser {
					hasActiveConnection = true
					break
				}
			}
		}
		room.mu.Unlock()
	}

	if !hasActiveConnection && !leftExplicitly && (status == 0 || status == 2) && userID != "" && db != nil {
		if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).Delete(&models.Player{}).Error; err != nil {
			log.Printf("[Error] Room: %s | User: %s | waiting切断後のプレイヤー削除に失敗しました: %v\n", roomID, userID, err)
		} else {
			newHostUserID, err := transferRoomHostIfNeeded(db, roomID, userID)
			if err != nil {
				log.Printf("[Error] Room: %s | User: %s | waiting切断後のhost移譲に失敗しました: %v\n", roomID, userID, err)
			} else {
				syncRoomHost(room, newHostUserID)
			}
			room.mu.Lock()
			clearPendingRouletteLocked(room)
			room.mu.Unlock()
			if !isEmpty {
				room.Broadcast(room.waitingMessage())
			}
		}
	}

	if leftExplicitly && status == 1 {
		return
	}

	//誰もいなければ、Hub(メモリ)から部屋ごと削除する
	if isEmpty {
		h.mu.Lock()
		// ロックを取ってからもう一度確認（処理中に別の人とすれ違いで入室してきたら消さないようにする安全対策）
		if r, exists := h.Rooms[roomID]; exists {
			r.mu.Lock()
			stillEmpty := len(r.Clients) == 0
			shouldFinishRoom := stillEmpty && r.Status == 1
			if shouldFinishRoom {
				r.Status = 2
				r.IsGameActive = false
				r.IsGMLoopActive = false
				r.LoopID++
			}
			r.mu.Unlock()

			if stillEmpty {
				if shouldFinishRoom && db != nil {
					if err := db.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 2).Error; err != nil {
						log.Printf("[Error] Room: %s | 空部屋の終了状態保存に失敗しました: %v\n", roomID, err)
						h.mu.Unlock()
						return
					}
					if err := db.Model(&models.CaptureRequest{}).Where("room_id = ? AND status = ?", roomID, "pending").Update("status", "canceled").Error; err != nil {
						log.Printf("[Error] Room: %s | 空部屋の未解決キャプチャキャンセルに失敗しました: %v\n", roomID, err)
					}
				}
				delete(h.Rooms, roomID)
				log.Printf("[Info] Room: %s | メモリから削除されました（退出完了）\n", roomID)
			}
		}
		h.mu.Unlock()
	}
}

func ServeWs(c *gin.Context, db *gorm.DB) {
	roomID := c.Param("id")

	// Check if room exists in database
	var room models.Room
	if err := db.First(&room, "id = ?", roomID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(c.Writer, "Room not found", http.StatusNotFound)
			return
		}
		http.Error(c.Writer, "Database error", http.StatusInternalServerError)
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	roomState := GameHub.GetOrCreateRoom(roomID)
	roomState.mu.RLock()
	shouldSyncRoom := roomState.Status == 0 && roomState.HostUserID == "" && !roomState.IsGMLoopActive
	roomState.mu.RUnlock()

	if shouldSyncRoom {
		GameHub.UpdateRoomSettingsFromModel(room)
	}

	client := &Client{
		Conn:   conn,
		RoomID: roomID,
	}

	defer GameHub.Unregister(roomID, client, db)

	done := make(chan struct{})
	defer close(done)

	pongWait := 60 * time.Second
	pingPeriod := (pongWait * 9) / 10

	// クライアントからPongが返ってきたら、タイムアウト時間を延長する
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				client.mu.Lock()
				_ = client.Conn.SetWriteDeadline(time.Now().Add(writeWait))
				err := client.Conn.WriteMessage(websocket.PingMessage, nil)
				_ = client.Conn.SetWriteDeadline(time.Time{})
				client.mu.Unlock()
				if err != nil {
					client.mu.Lock()
					_ = client.Conn.Close()
					client.mu.Unlock()
					return // 送信失敗＝切断されているので終了
				}
			}
		}
	}()

	for {
		_, messageData, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg IncomingMessage
		if err := json.Unmarshal(messageData, &msg); err != nil {
			sendError(client, "送信データの形式が正しくありません")
			continue
		}

		client.mu.Lock()
		if client.UserID == "" && msg.UserID != "" {
			client.UserID = msg.UserID
		}
		client.mu.Unlock()

		room := GameHub.GetOrCreateRoom(roomID)

		switch msg.Action {
		case "join":
			if msg.UserID == "" {
				sendError(client, "ユーザーIDがありません")
				continue
			}

			normalizedName, ok := normalizePlayerName(msg.Name)
			if !ok {
				sendError(client, "名前は1文字以上、12文字以下にしてください")
				continue
			}
			msg.Name = normalizedName

			if msg.Color != "" {
				msg.Color = strings.TrimSpace(msg.Color)
				if strings.EqualFold(msg.Color, "black") {
					msg.Color = "#000000"
				} else {
					normalizedColor, ok := normalizeHexColor(msg.Color)
					if !ok {
						sendError(client, "カラーの形式が不正です（例: #FF0000）")
						continue
					}
					if !isReservedPlayerColor(normalizedColor) && !isPlayerPaletteColor(normalizedColor) {
						sendError(client, "カラーは指定されたパレットから選択してください")
						continue
					}
					msg.Color = normalizedColor
				}
			}

			status := roomStatus(room)

			var player models.Player
			err := db.Where("room_id = ? AND user_id = ?", roomID, msg.UserID).First(&player).Error
			playerExists := err == nil
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if status != 0 {
					sendError(client, "ゲーム中または終了後の新規参加はできません")
					continue
				}
				count, err := roomPlayerCount(db, roomID)
				if err != nil {
					sendError(client, "プレイヤー情報の取得に失敗しました")
					continue
				}
				maxPlayers := room.maxPlayers()
				if count >= int64(maxPlayers) {
					sendError(client, "参加人数は"+strconv.Itoa(maxPlayers)+"人までです")
					continue
				}

				joinColor, err := safeJoinColor(db, roomID, msg.UserID, msg.Color)
				if err != nil {
					if errors.Is(err, errNoAvailablePlayerColor) {
						sendError(client, err.Error())
					} else {
						sendError(client, "プレイヤーカラーの確認に失敗しました")
					}
					continue
				}

				player = models.Player{
					ID:     makePlayerID(roomID, msg.UserID),
					RoomID: roomID,
					UserID: msg.UserID,
					Name:   msg.Name,
					Color:  joinColor,
				}
				if err := db.Create(&player).Error; err != nil {
					sendError(client, "プレイヤー情報の保存に失敗しました")
					continue
				}
				room.mu.Lock()
				clearPendingRouletteLocked(room)
				room.mu.Unlock()
			} else if err != nil {
				sendError(client, "プレイヤー情報の保存に失敗しました")
				continue
			} else if status == 0 {
				updates := map[string]interface{}{}
				if msg.Name != "" && msg.Name != player.Name {
					player.Name = msg.Name
					updates["name"] = msg.Name
				}
				joinColor := player.Color
				if msg.Color != "" {
					joinColor = msg.Color
				}
				joinColor, err := safeJoinColor(db, roomID, msg.UserID, joinColor)
				if err != nil {
					if errors.Is(err, errNoAvailablePlayerColor) {
						sendError(client, err.Error())
					} else {
						sendError(client, "プレイヤーカラーの確認に失敗しました")
					}
					continue
				}
				if joinColor != player.Color {
					player.Color = joinColor
					updates["color"] = joinColor
				}
				if len(updates) > 0 {
					if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, msg.UserID).Updates(updates).Error; err != nil {
						sendError(client, "プレイヤー情報の保存に失敗しました")
						continue
					}
				}
			}

			client.mu.Lock()
			client.PlayerID = player.ID
			client.UserID = player.UserID
			client.Name = player.Name
			client.Role = player.Role
			client.IsCaught = player.IsCaught
			client.Lat = player.Lat
			client.Lng = player.Lng
			client.HasLocation = player.HasLocation
			client.Color = player.Color
			if status == 1 && player.Role == 1 {
				client.Color = "black"
			}
			client.PhotoURL = player.PhotoURL
			client.CapturedAt = player.CapturedAt
			client.mu.Unlock()

			GameHub.Register(roomID, client)

			switch status {
			case 0:
				hostUserID, err := ensureRoomHost(db, roomID, player.UserID)
				if err != nil {
					sendError(client, "ホスト情報の保存に失敗しました")
					continue
				}
				syncRoomHost(room, hostUserID)
				room.Broadcast(room.waitingMessage())
				sendRoomSettingsToClient(roomID, client, room, player.UserID)
			case 1:
				sendRoomSettingsToClient(roomID, client, room, player.UserID)
				sendActiveStateToClient(roomID, client, room, db)
				startGameLoopIfNeeded(roomID, room, db)
			case 2:
				sendRoomSettingsToClient(roomID, client, room, player.UserID)
				resultMessage, err := room.resultMessage(roomID, db)
				if err != nil {
					log.Printf("[Error] Room: %s | リザルト再送に失敗しました: %v\n", roomID, err)
					continue
				}
				if err := sendToClient(client, resultMessage); err != nil {
					log.Printf("[Info] Room: %s | User: %s | result再送に失敗しました: %v\n", roomID, player.UserID, err)
				}
			default:
				if playerExists {
					sendRoomSettingsToClient(roomID, client, room, player.UserID)
				}
			}

		case "update_color":
			if msg.Color == "" {
				sendError(client, "カラーを選択してください")
				continue
			}
			msg.Color = strings.TrimSpace(msg.Color)
			if isReservedPlayerColor(msg.Color) {
				sendError(client, "黒は鬼用のカラーです")
				continue
			}
			normalizedColor, ok := normalizeHexColor(msg.Color)
			if !ok {
				sendError(client, "カラーの形式が不正です（例: #FF0000）")
				continue
			}
			if !isPlayerPaletteColor(normalizedColor) {
				sendError(client, "カラーは指定されたパレットから選択してください")
				continue
			}

			client.mu.Lock()
			userID := client.UserID
			playerID := client.PlayerID
			role := client.Role
			client.mu.Unlock()
			if userID == "" || playerID == "" {
				sendError(client, "先に入室してください")
				continue
			}
			if role == 1 {
				sendError(client, "鬼のカラーは変更できません")
				continue
			}

			var player models.Player
			if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).First(&player).Error; err != nil {
				sendError(client, "プレイヤー情報の取得に失敗しました")
				continue
			}

			if player.Role == 1 {
				sendError(client, "鬼のカラーは変更できません")
				continue
			}
			if !strings.EqualFold(player.Color, normalizedColor) {
				used, err := colorUsedByOtherPlayer(db, roomID, userID, normalizedColor)
				if err != nil {
					sendError(client, "プレイヤーカラーの確認に失敗しました")
					continue
				}
				if used {
					sendError(client, "このカラーはすでに使われています")
					continue
				}
			}
			if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, userID).Update("color", normalizedColor).Error; err != nil {
				sendError(client, "プレイヤーカラーの保存に失敗しました")
				continue
			}

			for _, c := range room.clientList() {
				c.mu.Lock()
				if c.UserID == userID {
					c.Color = normalizedColor
				}
				c.mu.Unlock()
			}

			if roomStatus(room) == 0 {
				room.Broadcast(room.waitingMessage())
			}

		case "prepare_roulette", "start_roulette":
			userID, playerID := joinedClientIdentity(client)
			if userID == "" || playerID == "" {
				sendError(client, "先に入室してください")
				continue
			}
			if !room.isHost(userID) {
				sendError(client, "ホストのみ実行できます")
				continue
			}
			room.mu.Lock()
			if room.Status != 0 {
				room.mu.Unlock()
				sendError(client, "ルーレット開始は待機中のみ実行できます")
				continue
			}

			rouletteOrder := connectedUserIDsLocked(room)
			if errorMessage := validateRoulettePlayersLocked(room, rouletteOrder); errorMessage != "" {
				room.mu.Unlock()
				sendError(client, errorMessage)
				continue
			}

			sessionID := uuid.New().String()
			room.Roulette = RouletteState{
				SessionID:     sessionID,
				Status:        rouletteStatusReady,
				RouletteOrder: copyStrings(rouletteOrder),
			}
			rouletteMessage := rouletteReadyMessage(sessionID, rouletteOrder)
			room.mu.Unlock()

			room.Broadcast(rouletteMessage)

		case "roulette_start":
			userID, playerID := joinedClientIdentity(client)
			if userID == "" || playerID == "" {
				sendError(client, "先に入室してください")
				continue
			}
			if !room.isHost(userID) {
				sendError(client, "ホストのみ実行できます")
				continue
			}
			if msg.RouletteSessionID == "" {
				sendError(client, "ルーレットセッションIDがありません")
				continue
			}

			room.mu.Lock()
			if room.Status != 0 {
				room.mu.Unlock()
				sendError(client, "ルーレット開始は待機中のみ実行できます")
				continue
			}
			if room.Roulette.SessionID == "" || room.Roulette.SessionID != msg.RouletteSessionID {
				room.mu.Unlock()
				sendError(client, "ルーレットセッションが見つかりません")
				continue
			}
			if room.Roulette.Status == rouletteStatusSpinning {
				room.mu.Unlock()
				sendError(client, "ルーレットはすでに回転中です")
				continue
			}
			if room.Roulette.Status != rouletteStatusReady {
				room.mu.Unlock()
				sendError(client, "ルーレットをリセットしてください")
				continue
			}
			if errorMessage := validateRoulettePlayersLocked(room, room.Roulette.RouletteOrder); errorMessage != "" {
				room.mu.Unlock()
				sendError(client, errorMessage)
				continue
			}

			room.Roulette.SpinID++
			startsAt := time.Now().UTC()
			room.Roulette.Status = rouletteStatusSpinning
			room.Roulette.PendingOniUserIDs = nil
			room.Roulette.StartedAt = timePtr(startsAt)
			room.Roulette.StoppedAt = nil
			rouletteMessage := rouletteSpinStartedMessage(room.Roulette.SessionID, room.Roulette.SpinID, room.Roulette.RouletteOrder, startsAt)
			room.mu.Unlock()

			room.Broadcast(rouletteMessage)

		case "roulette_stop":
			userID, playerID := joinedClientIdentity(client)
			if userID == "" || playerID == "" {
				sendError(client, "先に入室してください")
				continue
			}
			if !room.isHost(userID) {
				sendError(client, "ホストのみ実行できます")
				continue
			}
			if msg.RouletteSessionID == "" {
				sendError(client, "ルーレットセッションIDがありません")
				continue
			}
			if msg.SpinID <= 0 {
				sendError(client, "spin_idがありません")
				continue
			}

			room.mu.Lock()
			if room.Status != 0 {
				room.mu.Unlock()
				sendError(client, "ルーレット停止は待機中のみ実行できます")
				continue
			}
			if room.Roulette.SessionID == "" || room.Roulette.SessionID != msg.RouletteSessionID {
				room.mu.Unlock()
				sendError(client, "ルーレットセッションが見つかりません")
				continue
			}
			if room.Roulette.SpinID != msg.SpinID {
				room.mu.Unlock()
				sendError(client, "spin_idが一致しません")
				continue
			}
			if room.Roulette.Status != rouletteStatusSpinning {
				room.mu.Unlock()
				sendError(client, "ルーレットは開始されていません")
				continue
			}
			if errorMessage := validateRoulettePlayersLocked(room, room.Roulette.RouletteOrder); errorMessage != "" {
				room.mu.Unlock()
				sendError(client, errorMessage)
				continue
			}

			selectedOniUserIDs := selectOniUserIDs(room.Roulette.RouletteOrder, room.OniCount)
			stoppedAt := time.Now().UTC()
			room.Roulette.Status = rouletteStatusStopped
			room.Roulette.PendingOniUserIDs = copyStrings(selectedOniUserIDs)
			room.Roulette.StoppedAt = timePtr(stoppedAt)
			rouletteMessage := rouletteSpinStoppedMessage(room.Roulette.SessionID, room.Roulette.SpinID, selectedOniUserIDs, stoppedAt)
			room.mu.Unlock()

			room.Broadcast(rouletteMessage)

		case "roulette_reset":
			userID, playerID := joinedClientIdentity(client)
			if userID == "" || playerID == "" {
				sendError(client, "先に入室してください")
				continue
			}
			if !room.isHost(userID) {
				sendError(client, "ホストのみ実行できます")
				continue
			}
			if msg.RouletteSessionID == "" {
				sendError(client, "ルーレットセッションIDがありません")
				continue
			}

			room.mu.Lock()
			if room.Status != 0 {
				room.mu.Unlock()
				sendError(client, "ルーレットリセットは待機中のみ実行できます")
				continue
			}
			if room.Roulette.SessionID == "" || room.Roulette.SessionID != msg.RouletteSessionID {
				room.mu.Unlock()
				sendError(client, "ルーレットセッションが見つかりません")
				continue
			}
			room.Roulette.Status = rouletteStatusReady
			room.Roulette.PendingOniUserIDs = nil
			room.Roulette.StartedAt = nil
			room.Roulette.StoppedAt = nil
			rouletteMessage := rouletteResetMessage(room.Roulette.SessionID)
			room.mu.Unlock()

			room.Broadcast(rouletteMessage)

		case "start":
			client.mu.Lock()
			startUserID := client.UserID
			startPlayerID := client.PlayerID
			client.mu.Unlock()
			if startUserID == "" || startPlayerID == "" {
				sendError(client, "先に入室してください")
				continue
			}
			if !room.isHost(startUserID) {
				sendError(client, "ホストのみ実行できます")
				continue
			}

			room.mu.Lock()
			if room.Status != 0 || room.IsGMLoopActive {
				room.mu.Unlock()
				sendError(client, "ゲームはすでに開始しています")
				continue
			}

			selectedOniUsers := copyStrings(room.Roulette.PendingOniUserIDs)
			if len(selectedOniUsers) == 0 {
				selectedOniUsers = copyStrings(msg.OniUsers)
			}
			if len(selectedOniUsers) == 0 {
				room.mu.Unlock()
				sendError(client, "鬼に指定するユーザーを1人以上選択してください")
				continue
			}
			if len(selectedOniUsers) > maxOniUsers {
				room.mu.Unlock()
				sendError(client, "鬼は1〜3人で指定してください")
				continue
			}

			oniUsers := make(map[string]bool, len(selectedOniUsers))
			validStart := true
			errorMessage := ""
			for _, userID := range selectedOniUsers {
				if userID == "" {
					validStart = false
					errorMessage = "鬼に指定されたユーザーIDが不正です"
					break
				}
				if oniUsers[userID] {
					validStart = false
					errorMessage = "鬼に指定されたユーザーが重複しています"
					break
				}
				oniUsers[userID] = true
			}
			if !validStart {
				room.mu.Unlock()
				sendError(client, errorMessage)
				continue
			}

			joinedUsers := make(map[string]bool, len(room.Clients))
			for c := range room.Clients {
				c.mu.Lock()
				if c.UserID != "" {
					joinedUsers[c.UserID] = true
				}
				c.mu.Unlock()
			}
			playerCount := len(joinedUsers)
			if playerCount < minStartPlayers {
				room.mu.Unlock()
				sendError(client, "ゲーム開始には2人以上必要です")
				continue
			}
			for userID := range oniUsers {
				if !joinedUsers[userID] {
					validStart = false
					errorMessage = "鬼に指定されたユーザーが参加していません"
					break
				}
			}
			if validStart && len(oniUsers) >= playerCount {
				validStart = false
				errorMessage = "全員を鬼にはできません"
			}
			if validStart && room.OniCount >= minOniUsers && room.OniCount <= maxOniUsers && len(oniUsers) != room.OniCount {
				validStart = false
				errorMessage = "設定された鬼の人数と一致しません"
			}
			if !validStart {
				room.mu.Unlock()
				sendError(client, errorMessage)
				continue
			}

			room.TimeLimit, room.SyncInterval, room.GracePeriod = cleanGameSettings(room.TimeLimit, room.SyncInterval, room.GracePeriod)
			room.Status = 1
			room.IsGMLoopActive = true
			room.IsGameActive = false
			room.StartAt = time.Now().Add(time.Duration(room.GracePeriod) * time.Second)
			room.ActiveAt = time.Time{}
			room.LoopID++
			loopID := room.LoopID

			type playerStartState struct {
				Client        *Client
				UserID        string
				Role          int
				Color         string
				PreviousRole  int
				PreviousColor string
			}

			var playerStates []playerStartState
			for c := range room.Clients {
				c.mu.Lock()
				if c.UserID != "" {
					previousRole := c.Role
					previousColor := c.Color
					c.Role = 0
					if oniUsers[c.UserID] {
						c.Role = 1
						c.Color = "black"
					}
					playerStates = append(playerStates, playerStartState{
						Client:        c,
						UserID:        c.UserID,
						Role:          c.Role,
						Color:         c.Color,
						PreviousRole:  previousRole,
						PreviousColor: previousColor,
					})
				}
				c.mu.Unlock()
			}
			room.mu.Unlock()

			rollbackStart := func() {
				room.mu.Lock()
				if room.Status == 1 {
					room.Status = 0
					room.IsGMLoopActive = false
					room.IsGameActive = false
					room.StartAt = time.Time{}
					room.ActiveAt = time.Time{}
					room.LoopID++
				}
				for _, state := range playerStates {
					state.Client.mu.Lock()
					state.Client.Role = state.PreviousRole
					state.Client.Color = state.PreviousColor
					state.Client.mu.Unlock()
				}
				room.mu.Unlock()
			}

			txErr := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 1).Error; err != nil {
					return err
				}
				for _, state := range playerStates {
					updates := map[string]interface{}{"role": state.Role}
					if err := tx.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, state.UserID).Updates(updates).Error; err != nil {
						return err
					}
				}
				return nil
			})

			if txErr != nil {
				rollbackStart()
				log.Printf("[Error] Room: %s | ゲーム開始状態の保存（トランザクション）に失敗しました: %v\n", roomID, txErr)
				sendError(client, "ゲーム開始状態の保存に失敗しました")
				continue
			}

			room.mu.Lock()
			clearPendingRouletteLocked(room)
			room.mu.Unlock()

			// 開始通知（フロントエンド側はこの通知で猶予時間のカウントダウンUIを出す）
			room.mu.RLock()
			for c := range room.Clients {
				c.mu.Lock()
				role := c.Role
				_ = c.Conn.WriteJSON(OutgoingMessage{
					Event:     "start",
					Role:      &role,
					TimeLimit: room.TimeLimit,
					OniUsers:  selectedOniUsers,
				})
				c.mu.Unlock()
			}
			room.mu.RUnlock()

			go runGameLoop(roomID, room, db, loopID)

		case "reset":
			client.mu.Lock()
			userID := client.UserID
			client.mu.Unlock()
			if userID == "" {
				sendError(client, "先に入室してください")
				continue
			}
			if !room.isHost(userID) {
				sendError(client, "ホストのみ実行できます")
				continue
			}
			if roomStatus(room) != 2 {
				sendError(client, "リセットはリザルト後に実行してください")
				continue
			}

			preservedColors, err := resetRoomForReplay(roomID, room, db)
			if err != nil {
				sendError(client, "ルームのリセットに失敗しました")
				continue
			}

			room.mu.Lock()
			room.Status = 0
			room.IsGameActive = false
			room.IsGMLoopActive = false
			room.StartAt = time.Time{}
			room.ActiveAt = time.Time{}
			room.LoopID++
			clearPendingRouletteLocked(room)
			room.mu.Unlock()

			for _, c := range room.clientList() {
				c.mu.Lock()
				c.Role = 0
				c.IsCaught = false
				c.Lat = 0
				c.Lng = 0
				c.HasLocation = false
				c.Color = preservedColors[c.UserID]
				c.PhotoURL = ""
				c.CapturedAt = nil
				c.mu.Unlock()
			}
			var requests []models.CaptureRequest
			if err := db.Where("room_id = ?", roomID).Find(&requests).Error; err == nil {
				for _, req := range requests {
					go deleteSupabasePhoto(req.PhotoURL)
				}
				// 用済みの申請履歴レコードをDBからも削除する
				db.Where("room_id = ?", roomID).Delete(&models.CaptureRequest{})
			}

			room.Broadcast(room.waitingMessage())

		case "leave":
			client.mu.Lock()
			userID := client.UserID
			client.LeftExplicitly = true
			client.mu.Unlock()

			status := roomStatus(room)
			if (status == 0 || status == 2) && userID != "" {
				if err := db.Where("room_id = ? AND user_id = ?", roomID, userID).Delete(&models.Player{}).Error; err != nil {
					client.mu.Lock()
					client.LeftExplicitly = false
					client.mu.Unlock()
					sendError(client, "プレイヤー情報の削除に失敗しました")
					continue
				}
				if status == 0 || status == 2 {
					newHostUserID, err := transferRoomHostIfNeeded(db, roomID, userID)
					if err != nil {
						client.mu.Lock()
						client.LeftExplicitly = false
						client.mu.Unlock()
						sendError(client, "ホスト移譲に失敗しました")
						continue
					}
					syncRoomHost(room, newHostUserID)
					if status == 0 {
						room.mu.Lock()
						clearPendingRouletteLocked(room)
						room.mu.Unlock()
					}
				}
			}

			removedRoom, leaveStatus, _ := GameHub.removeClientFromRoom(roomID, client)
			client.mu.Lock()
			_ = client.Conn.Close()
			client.mu.Unlock()

			if removedRoom != nil {
				switch leaveStatus {
				case 0:
					removedRoom.Broadcast(removedRoom.waitingMessage())
				case 2:
					resultMessage, err := removedRoom.resultMessage(roomID, db)
					if err != nil {
						log.Printf("[Error] Room: %s | leave後のリザルト更新に失敗しました: %v\n", roomID, err)
					} else {
						removedRoom.Broadcast(resultMessage)
					}
				}
			}
			return

		case "move":
			if msg.Lat < -90 || msg.Lat > 90 || msg.Lng < -180 || msg.Lng > 180 {
				// 異常な座標の場合は保存せずに無視する。
				// 必要であればここで client.Conn.WriteJSON() を使ってエラーを返しても良いですが、
				// moveは高頻度で飛んでくるため、サーバー側で静かにドロップ（破棄）するのが一般的です。
				continue
			}
			client.mu.Lock()
			if client.UserID == "" {
				client.mu.Unlock()
				sendError(client, "先に入室してください")
				continue
			}
			client.Lat = msg.Lat
			client.Lng = msg.Lng
			client.HasLocation = true
			userID := client.UserID
			client.mu.Unlock()
			if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, userID).Updates(map[string]interface{}{
				"lat":          msg.Lat,
				"lng":          msg.Lng,
				"has_location": true,
			}).Error; err != nil {
				sendError(client, "プレイヤー情報の保存に失敗しました")
				continue
			}
			// ※ Broadcast（一斉送信）は削除し、メモリの更新のみを行う

		// --- 1歩目：鬼からの確保申請 ---
		case "capture_request":
			if msg.TargetID == "" {
				sendError(client, "捕まえる相手が見つかりません")
				continue
			}
			if msg.PhotoURL == "" {
				sendError(client, "証拠写真のURLがありません") // 仕様書通り必須化
				continue
			}
			if !isValidPhotoURL(msg.PhotoURL) {
				sendError(client, "無効な写真URLです")
				continue
			}

			room := GameHub.GetOrCreateRoom(roomID)
			room.mu.RLock()

			if room.Status != 1 || !room.IsGameActive {
				room.mu.RUnlock()
				sendError(client, "ゲーム本編中ではありません")
				continue
			}

			client.mu.Lock()
			isAttackerOni := client.Role == 1
			attackerName := client.Name
			attackerID := client.UserID
			client.mu.Unlock()

			if !isAttackerOni {
				room.mu.RUnlock()
				sendError(client, "あなたは鬼ではありません")
				continue
			}
			if attackerID == msg.TargetID {
				room.mu.RUnlock()
				sendError(client, "自分自身は捕まえられません")
				continue
			}

			var targetClient *Client
			for c := range room.Clients {
				c.mu.Lock()
				isTarget := c.UserID == msg.TargetID && c.Role == 0 && !c.IsCaught
				c.mu.Unlock()
				if isTarget {
					targetClient = c
					break
				}
			}
			room.mu.RUnlock()

			if targetClient == nil {
				sendError(client, "対象の逃走者が見つからないか、すでに捕まっています")
				continue
			}

			// 同一ターゲットへのPending確認
			var existingPending int64
			if err := db.Model(&models.CaptureRequest{}).Where("room_id = ? AND target_user_id = ? AND status = ?", roomID, msg.TargetID, "pending").Count(&existingPending).Error; err != nil {
				sendError(client, "データベースエラーが発生しました")
				continue
			}
			if existingPending > 0 {
				sendError(client, "この逃走者には既に捕獲申請中です")
				continue
			}

			// request_id (UUID v4) の生成とDB保存
			requestID := uuid.New().String()
			now := time.Now()
			expiresAt := now.Add(30 * time.Second)

			captureReq := models.CaptureRequest{
				ID:             requestID,
				RoomID:         roomID,
				AttackerUserID: attackerID,
				TargetUserID:   msg.TargetID,
				Status:         "pending",
				PhotoURL:       msg.PhotoURL,
				CreatedAt:      now,
				ExpiresAt:      expiresAt,
			}

			if err := db.Create(&captureReq).Error; err != nil {
				sendError(client, "捕獲申請の保存に失敗しました")
				continue
			}

			// 2歩目：ターゲット（逃走者）だけに確認通知を個別送信
			targetClient.mu.Lock()
			_ = targetClient.Conn.WriteJSON(OutgoingMessage{
				Event:        "capture_checking",
				RequestID:    requestID,
				AttackerID:   attackerID,
				AttackerName: attackerName,
				TargetID:     msg.TargetID,
				PhotoURL:     msg.PhotoURL,
				ExpiresAt:    expiresAt.Format(time.RFC3339),
			})
			targetClient.mu.Unlock()

			// 30秒ExpireのGoroutine起動
			go func(reqID, rID, tID, aID, pURL string) {
				time.Sleep(30 * time.Second)

				// 排他制御：30秒後にまだpendingならexpiredに更新する
				result := db.Model(&models.CaptureRequest{}).
					Where("id = ? AND status = ?", reqID, "pending").
					Update("status", "expired")

				if result.Error == nil && result.RowsAffected > 0 {
					// Expiredに更新成功（誰も回答しなかった）ため、鬼と逃走者に通知
					expMsg := OutgoingMessage{
						Event:      "capture_expired",
						RequestID:  reqID,
						TargetID:   tID,
						AttackerID: aID,
					}
					// 対象の2人だけに送る
					r := GameHub.GetRoom(rID)
					if r == nil {
						return
					}
					for _, c := range r.clientList() {
						c.mu.Lock()
						uid := c.UserID
						c.mu.Unlock()
						if uid == tID || uid == aID {
							_ = sendToClient(c, expMsg)
						}
					}
					go deleteSupabasePhoto(pURL)
				}
			}(requestID, roomID, msg.TargetID, attackerID, msg.PhotoURL)

		// --- 3歩目：逃走者からの回答 ---
		case "capture_response":
			if msg.RequestID == "" {
				sendError(client, "リクエストIDがありません")
				continue
			}

			room := GameHub.GetOrCreateRoom(roomID)
			room.mu.RLock()
			canRespond := room.Status == 1 && room.IsGameActive
			room.mu.RUnlock()
			if !canRespond {
				sendError(client, "捕獲回答はゲーム本編中のみ有効です")
				continue
			}

			client.mu.Lock()
			targetID := client.UserID
			joined := client.PlayerID != "" && client.UserID != ""
			isRunner := client.Role == 0
			isAlreadyCaught := client.IsCaught
			client.mu.Unlock()

			if !joined {
				sendError(client, "先に入室してください")
				continue
			}
			if !isRunner || isAlreadyCaught {
				sendError(client, "捕獲回答の権限がありません")
				continue
			}

			// DBから対象のリクエストを取得
			var req models.CaptureRequest
			if err := db.Where("id = ?", msg.RequestID).First(&req).Error; err != nil {
				sendError(client, "存在しない捕獲申請です")
				continue
			}

			if req.RoomID != roomID {
				sendError(client, "この部屋の捕獲申請ではありません")
				continue
			}

			if req.Status != "pending" {
				sendError(client, "この捕獲申請はすでに処理済みか、期限切れです")
				continue
			}
			if req.TargetUserID != targetID {
				sendError(client, "自分宛ての捕獲申請にしか回答できません")
				continue
			}
			if time.Now().After(req.ExpiresAt) {
				sendError(client, "この捕獲申請は期限切れです")
				continue
			}

			now := time.Now()
			newStatus := "rejected"
			if msg.Approved {
				newStatus = "approved"
			}

			// 排他制御：pending状態のときだけ更新（他で処理されていないことを担保）
			result := db.Model(&models.CaptureRequest{}).
				Where("id = ? AND status = ?", msg.RequestID, "pending").
				Updates(map[string]interface{}{
					"status":       newStatus,
					"responded_at": now,
				})

			if result.Error != nil || result.RowsAffected == 0 {
				sendError(client, "回答の保存に失敗しました（すでに処理されている可能性があります）")
				continue
			}

			if msg.Approved {
				// 承認された場合のみ、Playerを確保状態にする
				pResult := db.Model(&models.Player{}).
					Where("room_id = ? AND user_id = ? AND role = ? AND is_caught = ?", roomID, targetID, 0, false).
					Updates(map[string]interface{}{
						"is_caught":   true,
						"photo_url":   req.PhotoURL,
						"captured_at": now,
					})

				if pResult.Error != nil || pResult.RowsAffected == 0 {
					sendError(client, "プレイヤー状態の更新に失敗しました")
					continue
				}

				client.mu.Lock()
				client.IsCaught = true
				client.PhotoURL = req.PhotoURL
				client.CapturedAt = &now
				client.mu.Unlock()

				room.Broadcast(OutgoingMessage{
					Event:      "captured",
					RequestID:  msg.RequestID,
					TargetID:   targetID,
					AttackerID: req.AttackerUserID,
					Approved:   true,
					PhotoURL:   req.PhotoURL,
				})

				if roomStatus(room) == 1 {
					allCaught, err := allRunnersCaughtInDB(db, roomID)
					if err != nil {
						log.Printf("[Error] Room: %s | 捕獲後の終了判定に失敗しました: %v\n", roomID, err)
						allCaught = room.allConnectedRunnersCaught()
					}
					if allCaught {
						room.finish(roomID, db)
					}
				}
			} else {
				// 拒否された場合は、鬼と逃走者のみに通知
				deniedMsg := OutgoingMessage{
					Event:      "capture_denied",
					RequestID:  msg.RequestID,
					TargetID:   targetID,
					AttackerID: req.AttackerUserID,
					Approved:   false,
				}
				for _, c := range room.clientList() {
					c.mu.Lock()
					uid := c.UserID
					c.mu.Unlock()
					if uid == targetID || uid == req.AttackerUserID {
						_ = sendToClient(c, deniedMsg)
					}
				}
				go deleteSupabasePhoto(req.PhotoURL)
			}

		default:
			sendError(client, "対応していない操作です")
		}
	}
}

func deleteSupabasePhoto(photoURL string) {
	if photoURL == "" {
		return
	}

	// 環境変数からURLとキーを取得
	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseKey := os.Getenv("SUPABASE_KEY")

	if supabaseURL == "" || supabaseKey == "" {
		log.Println("[Storage] エラー: SUPABASE_URL または SUPABASE_KEY が環境変数に設定されていません")
		return
	}

	// URLからバケット名とファイル名を抽出する
	prefix := supabaseURL + "/storage/v1/object/public/"
	if !strings.HasPrefix(photoURL, prefix) {
		return
	}
	objectPath := strings.TrimPrefix(photoURL, prefix)

	// DELETE用のAPIエンドポイント
	deleteAPI := supabaseURL + "/storage/v1/object/" + objectPath

	req, err := http.NewRequest("DELETE", deleteAPI, nil)
	if err != nil {
		log.Printf("[Storage] 削除リクエスト作成エラー: %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+supabaseKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Storage] 画像削除エラー: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("[Storage] 画像を削除しました: %s\n", objectPath)
	} else {
		log.Printf("[Storage] 画像削除失敗: ステータスコード %d\n", resp.StatusCode)
	}
}
