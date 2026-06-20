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
	PlayerID string
	UserID   string
	RoomID   string
	Name     string
	Role     int
	IsCaught bool
	Lat      float64
	Lng      float64
	Color    string
	PhotoURL string
	mu       sync.Mutex
}

type RoomState struct {
	Status         int
	TimeLimit      int
	OniCount       int // 追加
	AreaSize       string
	SyncInterval   int // 追加
	GracePeriod    int // 追加
	AreaCenterLat  float64
	AreaCenterLng  float64
	HasAreaCenter  bool
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

func (h *Hub) UpdateRoomSettings(roomID string, timeLimit, oniCount int, areaSize string, syncInterval, gracePeriod int) {
	timeLimit, syncInterval, gracePeriod = cleanGameSettings(timeLimit, syncInterval, gracePeriod)

	room := h.GetOrCreateRoom(roomID)
	room.mu.Lock()
	defer room.mu.Unlock()
	room.TimeLimit = timeLimit
	room.OniCount = oniCount
	room.AreaSize = areaSize
	room.SyncInterval = syncInterval
	room.GracePeriod = gracePeriod
}

func (h *Hub) UpdateRoomSettingsFromModel(room models.Room) *RoomState {
	timeLimit, syncInterval, gracePeriod := cleanGameSettings(room.TimeLimit, room.SyncInterval, room.GracePeriod)

	roomState := h.GetOrCreateRoom(room.ID)
	roomState.mu.Lock()
	defer roomState.mu.Unlock()
	roomState.Status = room.Status
	roomState.TimeLimit = timeLimit
	roomState.OniCount = room.OniCount
	roomState.AreaSize = room.AreaSize
	roomState.SyncInterval = syncInterval
	roomState.GracePeriod = gracePeriod
	roomState.AreaCenterLat = room.AreaCenterLat
	roomState.AreaCenterLng = room.AreaCenterLng
	roomState.HasAreaCenter = room.HasAreaCenter
	return roomState
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
	room.mu.Unlock()

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
			}
			r.mu.Unlock()

			if stillEmpty {
				if shouldFinishRoom && db != nil {
					if err := db.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 2).Error; err != nil {
						log.Printf("[Error] Room: %s | 空部屋の終了状態保存に失敗しました: %v\n", roomID, err)
						h.mu.Unlock()
						return
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
	shouldSyncRoom := roomState.Status == 0 && !roomState.IsGMLoopActive
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
			client.PlayerID = player.ID
			client.UserID = player.UserID
			client.Name = player.Name
			client.Role = player.Role
			client.IsCaught = player.IsCaught
			client.Lat = player.Lat
			client.Lng = player.Lng
			client.Color = player.Color
			client.PhotoURL = player.PhotoURL
			client.mu.Unlock()

			GameHub.Register(roomID, client)

			room.Broadcast(OutgoingMessage{
				Event:   "waiting",
				Players: room.waitingPlayers(),
			})
			if err := sendToClient(client, room.roomSettingsMessage()); err != nil {
				log.Printf("[Info] Room: %s | User: %s | room_settings送信に失敗しました: %v\n", roomID, player.UserID, err)
			}

		case "start":
			room.mu.Lock()
			if room.Status != 0 || room.IsGMLoopActive {
				room.mu.Unlock()
				sendError(client, "ゲームはすでに開始しています")
				continue
			}
			if len(msg.OniUsers) == 0 {
				room.mu.Unlock()
				sendError(client, "鬼に指定するユーザーを1人以上選択してください")
				continue
			}

			selectedOniUsers := append([]string(nil), msg.OniUsers...)
			oniUsers := make(map[string]bool, len(msg.OniUsers))
			validStart := true
			errorMessage := ""
			for _, userID := range msg.OniUsers {
				if userID == "" {
					validStart = false
					errorMessage = "鬼に指定されたユーザーIDが不正です"
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
			for userID := range oniUsers {
				if !joinedUsers[userID] {
					validStart = false
					errorMessage = "鬼に指定されたユーザーが参加していません"
					break
				}
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
				}
				for _, state := range playerStates {
					state.Client.mu.Lock()
					state.Client.Role = state.PreviousRole
					state.Client.Color = state.PreviousColor
					state.Client.mu.Unlock()
				}
				room.mu.Unlock()
			}

			if err := db.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 1).Error; err != nil {
				rollbackStart()
				sendError(client, "ゲーム開始状態の保存に失敗しました")
				continue
			}

			saveFailed := false
			for _, state := range playerStates {
				updates := map[string]interface{}{"role": state.Role}
				if state.Role == 1 {
					updates["color"] = state.Color
				}
				if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, state.UserID).Updates(updates).Error; err != nil {
					saveFailed = true
					break
				}
			}
			if saveFailed {
				rollbackStart()
				if err := db.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 0).Error; err != nil {
					log.Printf("[Error] Room: %s | ゲーム開始状態の巻き戻しに失敗しました: %v\n", roomID, err)
				}
				for _, state := range playerStates {
					if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, state.UserID).Updates(map[string]interface{}{
						"role":  state.PreviousRole,
						"color": state.PreviousColor,
					}).Error; err != nil {
						log.Printf("[Error] Room: %s | User: %s | プレイヤー状態の巻き戻しに失敗しました: %v\n", roomID, state.UserID, err)
					}
				}
				sendError(client, "プレイヤー情報の保存に失敗しました")
				continue
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
					OniUsers:  selectedOniUsers,
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
					PhotoURL:     msg.PhotoURL, // ← ★追加：鬼から届いたURLをそのまま逃走者に渡す
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
