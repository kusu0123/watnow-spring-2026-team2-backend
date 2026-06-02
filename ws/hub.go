package ws

import (
	"encoding/json"
	"net/http"
	"sync"
	"time" // 追加（Goroutineのタイマー処理で使います）
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
	mu       sync.Mutex
}

type RoomState struct {
	Status    int
	TimeLimit int
	OniCount       int  // 追加
	SyncInterval   int  // 追加
	GracePeriod    int  // 追加
	Clients        map[*Client]bool
	IsGMLoopActive bool // 追加：GMの重複起動防止
	mu        sync.RWMutex
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
}

type OutgoingMessage struct {
	Event     string        `json:"event"`
	Players   []string      `json:"players,omitempty"`
	Role      *int          `json:"role,omitempty"`
	TimeLimit int           `json:"time_limit,omitempty"`
	TargetID  string        `json:"target_id,omitempty"`
	AttackerName string        `json:"attacker_name,omitempty"` // 追加：誰に捕まえられそうか
	Approved     bool          `json:"approved,omitempty"`      // 追加：最終的な判定結果
	Locations []LocationVal `json:"locations,omitempty"`
}

type LocationVal struct {
	UserID   string  `json:"user_id"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	IsCaught bool    `json:"is_caught"`
}

func (h *Hub) UpdateRoomSettings(roomID string, timeLimit, oniCount, syncInterval, gracePeriod int) {
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
	room.mu.Lock()
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

func ServeWs(c *gin.Context, db *gorm.DB) {
	roomID := c.Param("id")

	// Check if room exists in database
	var room models.Room
	if err := db.First(&room, "id = ?", roomID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
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

	client := &Client{
		Conn:   conn,
		RoomID: roomID,
	}

	GameHub.Register(roomID, client)
	defer GameHub.Unregister(roomID, client)

	for {
		_, messageData, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg IncomingMessage
		if err := json.Unmarshal(messageData, &msg); err != nil {
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
			client.mu.Lock()
			client.Name = msg.Name
			client.mu.Unlock()

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
			if room.Status == 1 || room.IsGMLoopActive {
				room.mu.Unlock()
				continue // 既に始まっている場合は無視
			}
			room.Status = 1
			room.IsGMLoopActive = true

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

			oniAssigned := 0
			for c := range room.Clients {
				c.mu.Lock()
				if oniAssigned < oniCount {
					c.Role = 1 // 鬼
					oniAssigned++
				} else {
					c.Role = 0 // 逃走者
				}
				c.mu.Unlock()
			}
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
				})
				c.mu.Unlock()
			}
			room.mu.RUnlock()

			// ▼▼ GM機能：非同期で時間を管理するゴルーチン ▼▼
			go func(r *RoomState) {
				// ゴルーチン終了時に次回 start を許可
				defer func() {
					r.mu.Lock()
					r.IsGMLoopActive = false
					r.mu.Unlock()
				}()

				r.mu.RLock()
				gracePeriod := r.GracePeriod
				syncInterval := r.SyncInterval
				r.mu.RUnlock()

				if gracePeriod < 0 {
					gracePeriod = 0
				}
				if syncInterval <= 0 {
					syncInterval = 1
				}

				// 1. 逃走猶予時間の待機（分）
				time.Sleep(time.Duration(gracePeriod) * time.Minute)

				// 猶予終了・本編開始を全員に通知
				r.Broadcast(OutgoingMessage{Event: "game_active"})

				// 2. マップ更新の定期実行（分）
				ticker := time.NewTicker(time.Duration(syncInterval) * time.Minute)
				defer ticker.Stop()

				for range ticker.C {

					r.mu.RLock()
					if r.Status != 1 || len(r.Clients) == 0 { // ゲーム停止 or 部屋が空なら終了
						r.mu.RUnlock()
						break
					}
					var locs []LocationVal
					for c := range r.Clients {
						c.mu.Lock()
						locs = append(locs, LocationVal{
							UserID:   c.UserID,
							Lat:      c.Lat,
							Lng:      c.Lng,
							IsCaught: c.IsCaught,
						})
						c.mu.Unlock()
					}
					r.mu.RUnlock()

					// インターバル経過時のみ一斉送信
					r.Broadcast(OutgoingMessage{
						Event:     "sync",
						Locations: locs,
					})
				}
			}(room)

		case "move":
			client.mu.Lock()
			client.Lat = msg.Lat
			client.Lng = msg.Lng
			client.mu.Unlock()
			// ※ Broadcast（一斉送信）は削除し、メモリの更新のみを行う

		// --- 1歩目：鬼からの確保申請 ---
		case "capture_request":
			room := GameHub.GetOrCreateRoom(roomID)
			room.mu.RLock()
			var targetClient *Client
		
			// ターゲットのClientを探す
			for c := range room.Clients {
				if c.UserID == msg.TargetID {
					targetClient = c
					break
				}
			}
			room.mu.RUnlock()

			if targetClient != nil {
				// 2歩目：ターゲット（逃走者）だけに確認通知を個別送信
				targetClient.mu.Lock()
				_ = targetClient.Conn.WriteJSON(OutgoingMessage{
				Event:        "capture_checking",
				AttackerName: client.Name, // 申請した人（鬼）の名前
				})
				targetClient.mu.Unlock()
			}

		// --- 3歩目：逃走者からの回答 ---
		case "capture_response":
			room := GameHub.GetOrCreateRoom(roomID)
		
			if msg.Approved {
				// 4歩目（承認時）：ステータスを確定させて全員に通知
				room.mu.Lock()
				for c := range room.Clients {
					c.mu.Lock()
					// 回答した本人（ターゲット）のステータスを更新
					if c.UserID == client.UserID {
					c.IsCaught = true
				}
				c.mu.Unlock()
			}
			room.mu.Unlock()

			room.Broadcast(OutgoingMessage{
				Event:    "captured",
				TargetID: client.UserID,
				Approved: true,
			})
			} else {
				// 4歩目（拒否時）：不成立だったことを全員（または鬼）に通知
				room.Broadcast(OutgoingMessage{
				Event:    "capture_denied",
				TargetID: client.UserID,
				})
			}
		}
	}
}
