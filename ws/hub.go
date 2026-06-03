package ws

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
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
	//部屋に誰もいなくなったかチェック
	isEmpty := len(room.Clients) == 0
	room.mu.Unlock()

	//誰もいなければ、Hub(メモリ)から部屋ごと削除する
	if isEmpty {
		h.mu.Lock()
		// ロックを取ってからもう一度確認（処理中に別の人とすれ違いで入室してきたら消さないようにする安全対策）
		if r, exists := h.Rooms[roomID]; exists {
			r.mu.RLock()
			stillEmpty := len(r.Clients) == 0
			r.mu.RUnlock()
			
			if stillEmpty {
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
        for range ticker.C {
            client.mu.Lock()
            if err := client.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
                client.mu.Unlock()
                return // 送信失敗＝切断されているので終了
            }
            client.mu.Unlock()
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

			if len(msg.Name) == 0 || len(msg.Name) > 20 {
				sendError(client, "名前は1文字以上、20文字以下にしてください")
				continue
			}
			
			// カラーコード（例: #FF0000）の簡易チェック：7文字で「#」から始まるか
			if msg.Color != "" && (len(msg.Color) != 7 || msg.Color[0] != '#') {
				sendError(client, "カラーの形式が不正です（例: #FF0000）")
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

			if room.Status != 1 {
                room.mu.RUnlock()
                sendError(client, "ゲーム中ではありません")
                continue
            }

            client.mu.Lock()
            isAttackerOni := client.Role == 1
            attackerName := client.Name
            client.mu.Unlock()

            if !isAttackerOni {
                room.mu.RUnlock()
                sendError(client, "あなたは鬼ではありません")
                continue
            }

			var targetClient *Client

			// ターゲットのClientを探す
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

			if targetClient != nil {
				// 2歩目：ターゲット（逃走者）だけに確認通知を個別送信
				targetClient.mu.Lock()
				_ = targetClient.Conn.WriteJSON(OutgoingMessage{
					Event:        "capture_checking",
					AttackerName: attackerName, // ← ここで上で作った変数を使う！
				})
				targetClient.mu.Unlock()
			} else {
				sendError(client, "対象の逃走者が見つからないか、すでに捕まっています")
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
