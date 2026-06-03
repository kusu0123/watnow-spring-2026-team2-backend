package ws

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/watnow/watnow-spring-2026-team2-backend/models"
	"gorm.io/gorm"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 本番環境では適切にCORSの設定をしてください
	},
}

type Client struct {
	Conn     *websocket.Conn
	UserID   string
	RoomID   string
	Name     string
	Role     int
	IsCaught bool
	Lat      float64
	Lng      float64
	Color    string
	mu       sync.Mutex
}

type RoomState struct {
	Status         int
	TimeLimit      int
	OniCount       int // 追加
	SyncInterval   int // 追加
	GracePeriod    int // 追加
	StartAt        time.Time
	ActiveAt       time.Time
	IsGameActive   bool
	Clients        map[*Client]bool
	IsGMLoopActive bool // 追加：GMの重複起動防止
	mu             sync.RWMutex
}

type Hub struct {
	Rooms map[string]*RoomState
	mu    sync.RWMutex
}

var GameHub = &Hub{
	Rooms: make(map[string]*RoomState),
}

type IncomingMessage struct {
	Action   string  `json:"action"`
	UserID   string  `json:"user_id,omitempty"`
	Name     string  `json:"name,omitempty"`
	TargetID string  `json:"target_id,omitempty"`
	Approved bool    `json:"approved,omitempty"` // 追加：逃走者からの「はい(true)/いいえ(false)」
	Lat      float64 `json:"lat,omitempty"`
	Lng      float64 `json:"lng,omitempty"`
	Color    string  `json:"color,omitempty"`
}

type OutgoingMessage struct {
	Event        string        `json:"event"`
	Message      string        `json:"message,omitempty"`
	Players      []string      `json:"players,omitempty"`
	Role         *int          `json:"role,omitempty"`
	TimeLimit    int           `json:"time_limit,omitempty"`
	TargetID     string        `json:"target_id,omitempty"`
	AttackerName string        `json:"attacker_name,omitempty"` // 追加：誰に捕まえられそうか
	Approved     bool          `json:"approved,omitempty"`      // 追加：最終的な判定結果
	Locations    []LocationVal `json:"locations,omitempty"`
	Survivors    []string      `json:"survivors,omitempty"`
	Results      []ResultVal   `json:"results,omitempty"`
}

type LocationVal struct {
	UserID   string  `json:"user_id"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	IsCaught bool    `json:"is_caught"`
	Color    string  `json:"color"`
}

type ResultVal struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Role     int    `json:"role"`
	IsCaught bool   `json:"is_caught"`
}

func makePlayerID(roomID, userID string) string {
	return roomID + ":" + userID
}

func cleanGameSettings(timeLimit, syncInterval, gracePeriod int) (int, int, int) {
	if timeLimit < 0 {
		timeLimit = 0
	}
	if syncInterval <= 0 {
		syncInterval = 1
	}
	if gracePeriod < 0 {
		gracePeriod = 0
	}
	return timeLimit, syncInterval, gracePeriod
}

func sendError(client *Client, message string) {
	client.mu.Lock()
	_ = client.Conn.WriteJSON(OutgoingMessage{
		Event:   "error",
		Message: message,
	})
	client.mu.Unlock()
}

func (h *Hub) UpdateRoomSettings(roomID string, timeLimit, oniCount, syncInterval, gracePeriod int) {
	timeLimit, syncInterval, gracePeriod = cleanGameSettings(timeLimit, syncInterval, gracePeriod)

	room := h.GetOrCreateRoom(roomID)
	room.mu.Lock()
	defer room.mu.Unlock()
	room.TimeLimit = timeLimit
	room.OniCount = oniCount
	room.SyncInterval = syncInterval
	room.GracePeriod = gracePeriod
}

func (h *Hub) GetOrCreateRoom(roomID string) *RoomState {
	h.mu.Lock()
	defer h.mu.Unlock()

	room, ok := h.Rooms[roomID]
	if !ok {
		room = &RoomState{
			Status:    0,
			TimeLimit: 900,
			Clients:   make(map[*Client]bool),
		}
		h.Rooms[roomID] = room
	}
	return room
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

func (h *Hub) Unregister(roomID string, client *Client) {
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
	room.mu.Unlock()
}

func (room *RoomState) Broadcast(msg interface{}) {
	room.mu.RLock()
	defer room.mu.RUnlock()
	for client := range room.Clients {
		client.mu.Lock()
		_ = client.Conn.WriteJSON(msg)
		client.mu.Unlock()
	}
}

func (room *RoomState) clientList() []*Client {
	room.mu.RLock()
	defer room.mu.RUnlock()

	clients := make([]*Client, 0, len(room.Clients))
	for client := range room.Clients {
		clients = append(clients, client)
	}
	return clients
}

func (room *RoomState) locations() []LocationVal {
	clients := room.clientList()
	locations := make([]LocationVal, 0, len(clients))

	for _, client := range clients {
		client.mu.Lock()
		locations = append(locations, LocationVal{
			UserID:   client.UserID,
			Lat:      client.Lat,
			Lng:      client.Lng,
			IsCaught: client.IsCaught,
			Color:    client.Color,
		})
		client.mu.Unlock()
	}

	sort.Slice(locations, func(i, j int) bool {
		return locations[i].UserID < locations[j].UserID
	})

	return locations
}

func (room *RoomState) resultMessage() OutgoingMessage {
	clients := room.clientList()
	results := make([]ResultVal, 0, len(clients))
	var survivors []string

	for _, client := range clients {
		client.mu.Lock()
		result := ResultVal{
			UserID:   client.UserID,
			Name:     client.Name,
			Role:     client.Role,
			IsCaught: client.IsCaught,
		}
		client.mu.Unlock()

		if result.Role == 0 && !result.IsCaught {
			survivors = append(survivors, result.Name)
		}
		results = append(results, result)
	}

	sort.Strings(survivors)
	sort.Slice(results, func(i, j int) bool {
		return results[i].UserID < results[j].UserID
	})

	return OutgoingMessage{
		Event:     "result",
		Survivors: survivors,
		Results:   results,
	}
}

func (room *RoomState) shouldEnd(now time.Time) bool {
	room.mu.RLock()
	if room.Status != 1 || !room.IsGameActive {
		room.mu.RUnlock()
		return false
	}

	if room.TimeLimit <= 0 || !now.Before(room.ActiveAt.Add(time.Duration(room.TimeLimit)*time.Second)) {
		room.mu.RUnlock()
		return true
	}

	clients := make([]*Client, 0, len(room.Clients))
	for client := range room.Clients {
		clients = append(clients, client)
	}
	room.mu.RUnlock()

	hasRunner := false
	allCaught := true
	for _, client := range clients {
		client.mu.Lock()
		role := client.Role
		isCaught := client.IsCaught
		client.mu.Unlock()

		if role == 0 {
			hasRunner = true
			if !isCaught {
				allCaught = false
			}
		}
	}

	return hasRunner && allCaught
}

func (room *RoomState) finish(roomID string, db *gorm.DB) bool {
	room.mu.Lock()
	if room.Status != 1 {
		room.mu.Unlock()
		return false
	}
	room.Status = 2
	room.mu.Unlock()

	_ = db.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 2).Error
	room.Broadcast(room.resultMessage())
	return true
}

func runGameLoop(roomID string, room *RoomState, db *gorm.DB) {
	defer func() {
		room.mu.Lock()
		room.IsGMLoopActive = false
		room.mu.Unlock()
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var nextSyncAt time.Time

	for {
		now := time.Now()

		room.mu.RLock()
		status := room.Status
		hasClients := len(room.Clients) > 0
		isGameActive := room.IsGameActive
		startAt := room.StartAt
		syncInterval := room.SyncInterval
		activeAt := room.ActiveAt
		room.mu.RUnlock()

		if status != 1 || !hasClients {
			return
		}

		if !isGameActive && !now.Before(startAt) {
			room.mu.Lock()
			if room.Status != 1 || len(room.Clients) == 0 {
				room.mu.Unlock()
				return
			}
			if !room.IsGameActive {
				room.IsGameActive = true
				room.ActiveAt = now
				activeAt = now
				isGameActive = true
			}
			syncInterval = room.SyncInterval
			room.mu.Unlock()

			nextSyncAt = activeAt.Add(time.Duration(syncInterval) * time.Second)
			room.Broadcast(OutgoingMessage{Event: "game_active"})
		}

		if isGameActive {
			if room.shouldEnd(now) {
				room.finish(roomID, db)
				return
			}

			if nextSyncAt.IsZero() {
				nextSyncAt = activeAt.Add(time.Duration(syncInterval) * time.Second)
			}
			if !now.Before(nextSyncAt) {
				room.Broadcast(OutgoingMessage{
					Event:     "sync",
					Locations: room.locations(),
				})
				for !now.Before(nextSyncAt) {
					nextSyncAt = nextSyncAt.Add(time.Duration(syncInterval) * time.Second)
				}
			}
		}

		<-ticker.C
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
	roomState.mu.Lock()
	if roomState.Status == 0 && !roomState.IsGMLoopActive {
		timeLimit, syncInterval, gracePeriod := cleanGameSettings(room.TimeLimit, room.SyncInterval, room.GracePeriod)
		roomState.Status = room.Status
		roomState.TimeLimit = timeLimit
		roomState.OniCount = room.OniCount
		roomState.SyncInterval = syncInterval
		roomState.GracePeriod = gracePeriod
	}
	roomState.mu.Unlock()

	client := &Client{
		Conn:   conn,
		RoomID: roomID,
	}

	defer GameHub.Unregister(roomID, client)

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

			var player models.Player
			err := db.Where("room_id = ? AND user_id = ?", roomID, msg.UserID).First(&player).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				player = models.Player{
					ID:     makePlayerID(roomID, msg.UserID),
					RoomID: roomID,
					UserID: msg.UserID,
					Name:   msg.Name,
					Color:  msg.Color,
				}
				if err := db.Create(&player).Error; err != nil {
					sendError(client, "プレイヤー情報の保存に失敗しました")
					continue
				}
			} else if err != nil {
				sendError(client, "プレイヤー情報の保存に失敗しました")
				continue
			} else {
				updates := map[string]interface{}{}
				if msg.Name != "" && msg.Name != player.Name {
					player.Name = msg.Name
					updates["name"] = msg.Name
				}
				if msg.Color != "" && msg.Color != player.Color {
					player.Color = msg.Color
					updates["color"] = msg.Color
				}
				if len(updates) > 0 {
					if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, msg.UserID).Updates(updates).Error; err != nil {
						sendError(client, "プレイヤー情報の保存に失敗しました")
						continue
					}
				}
			}

			client.mu.Lock()
			client.UserID = player.UserID
			client.Name = player.Name
			client.Role = player.Role
			client.IsCaught = player.IsCaught
			client.Lat = player.Lat
			client.Lng = player.Lng
			client.Color = player.Color
			client.mu.Unlock()

			GameHub.Register(roomID, client)

			room.mu.RLock()
			var names []string
			for c := range room.Clients {
				c.mu.Lock()
				if c.Name != "" {
					names = append(names, c.Name)
				}
				c.mu.Unlock()
			}
			room.mu.RUnlock()

			room.Broadcast(OutgoingMessage{
				Event:   "waiting",
				Players: names,
			})

		case "start":
			room.mu.Lock()
			if room.Status != 0 || room.IsGMLoopActive {
				room.mu.Unlock()
				sendError(client, "ゲームはすでに開始しています")
				continue
			}
			room.TimeLimit, room.SyncInterval, room.GracePeriod = cleanGameSettings(room.TimeLimit, room.SyncInterval, room.GracePeriod)
			room.Status = 1
			room.IsGMLoopActive = true
			room.IsGameActive = false
			room.StartAt = time.Now().Add(time.Duration(room.GracePeriod) * time.Second)
			room.ActiveAt = time.Time{}

			// 鬼の人数（oni_count）を正規化して役割を割り当て
			playerCount := len(room.Clients)
			oniCount := room.OniCount
			if playerCount == 0 {
				oniCount = 0
			} else {
				if oniCount < 1 {
					oniCount = 1
				}
				if oniCount > playerCount {
					oniCount = playerCount
				}
			}

			type roleSave struct {
				UserID string
				Role   int
			}

			var roleSaves []roleSave
			oniAssigned := 0
			for c := range room.Clients {
				c.mu.Lock()
				if oniAssigned < oniCount {
					c.Role = 1 // 鬼
					oniAssigned++
				} else {
					c.Role = 0 // 逃走者
				}
				if c.UserID != "" {
					roleSaves = append(roleSaves, roleSave{
						UserID: c.UserID,
						Role:   c.Role,
					})
				}
				c.mu.Unlock()
			}
			room.mu.Unlock()

			for _, roleSave := range roleSaves {
				if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, roleSave.UserID).Update("role", roleSave.Role).Error; err != nil {
					continue
				}
			}

			// 開始通知（フロントエンド側はこの通知で猶予時間のカウントダウンUIを出す）
			room.mu.RLock()
			for c := range room.Clients {
				c.mu.Lock()
				role := c.Role
				_ = c.Conn.WriteJSON(OutgoingMessage{
					Event:     "start",
					Role:      &role,
					TimeLimit: room.TimeLimit,
				})
				c.mu.Unlock()
			}
			room.mu.RUnlock()

			go runGameLoop(roomID, room, db)

		case "move":
			client.mu.Lock()
			if client.UserID == "" {
				client.mu.Unlock()
				sendError(client, "先に入室してください")
				continue
			}
			client.Lat = msg.Lat
			client.Lng = msg.Lng
			userID := client.UserID
			client.mu.Unlock()
			if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, userID).Updates(map[string]interface{}{
				"lat": msg.Lat,
				"lng": msg.Lng,
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

			room := GameHub.GetOrCreateRoom(roomID)
			room.mu.RLock()
			var targetClient *Client

			// ターゲットのClientを探す
			for c := range room.Clients {
				c.mu.Lock()
				isTarget := c.UserID == msg.TargetID
				c.mu.Unlock()
				if isTarget {
					targetClient = c
					break
				}
			}
			room.mu.RUnlock()

			if targetClient != nil {
				// 2歩目：ターゲット（逃走者）だけに確認通知を個別送信
				client.mu.Lock()
				attackerName := client.Name
				client.mu.Unlock()

				targetClient.mu.Lock()
				_ = targetClient.Conn.WriteJSON(OutgoingMessage{
					Event:        "capture_checking",
					AttackerName: attackerName, // 申請した人（鬼）の名前
				})
				targetClient.mu.Unlock()
			} else {
				sendError(client, "捕まえる相手が見つかりません")
			}

		// --- 3歩目：逃走者からの回答 ---
		case "capture_response":
			room := GameHub.GetOrCreateRoom(roomID)

			if msg.Approved {
				// 4歩目（承認時）：ステータスを確定させて全員に通知
				client.mu.Lock()
				client.IsCaught = true
				targetID := client.UserID
				client.mu.Unlock()

				if targetID != "" {
					if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, targetID).Update("is_caught", true).Error; err != nil {
						sendError(client, "プレイヤー情報の保存に失敗しました")
						continue
					}
				}

				room.Broadcast(OutgoingMessage{
					Event:    "captured",
					TargetID: targetID,
					Approved: true,
				})
			} else {
				// 4歩目（拒否時）：不成立だったことを全員（または鬼）に通知
				client.mu.Lock()
				targetID := client.UserID
				client.mu.Unlock()

				room.Broadcast(OutgoingMessage{
					Event:    "capture_denied",
					TargetID: targetID,
				})
			}
		default:
			sendError(client, "対応していない操作です")
		}
	}
}
