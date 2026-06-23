package ws

import (
	"log"
	"sort"
	"time"

	"github.com/watnow/watnow-spring-2026-team2-backend/models"
	"gorm.io/gorm"
)

const writeWait = 10 * time.Second

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

func sendToClient(client *Client, msg interface{}) error {
	client.mu.Lock()
	defer client.mu.Unlock()

	_ = client.Conn.SetWriteDeadline(time.Now().Add(writeWait))
	err := client.Conn.WriteJSON(msg)
	_ = client.Conn.SetWriteDeadline(time.Time{})
	return err
}

func (room *RoomState) Broadcast(msg interface{}) {
	// 1. 部屋のロックを取得して、送信先（クライアント）のリストだけをコピーする
	room.mu.RLock()
	clients := make([]*Client, 0, len(room.Clients))
	for client := range room.Clients {
		clients = append(clients, client)
	}
	room.mu.RUnlock() // ▼ コピーし終わったら、すぐに部屋のロックを解除する！

	// 2. コピーしたリストに対して、順番に送信していく
	for _, client := range clients {
		// 送信に失敗した場合（すでに通信が切れている等）はログだけ残す
		// ※完全に切断されている場合は、先ほど作ったPing/Pong機能が自動で回収してくれます
		if err := sendToClient(client, msg); err != nil {
			log.Printf("[Info] ユーザー %s へのBroadcast送信に失敗しました\n", client.UserID)
		}
	}
}

func (room *RoomState) roomSettingsMessage() RoomSettingsMessage {
	room.mu.RLock()
	defer room.mu.RUnlock()

	timeLimit, syncInterval, gracePeriod := cleanGameSettings(room.TimeLimit, room.SyncInterval, room.GracePeriod)
	var areaCenter *AreaCenterVal
	if room.HasAreaCenter {
		areaCenter = &AreaCenterVal{
			Lat: room.AreaCenterLat,
			Lng: room.AreaCenterLng,
		}
	}

	return RoomSettingsMessage{
		Event:          "room_settings",
		TimeLimit:      timeLimit,
		OniCount:       room.OniCount,
		AreaSize:       room.AreaSize,
		SyncInterval:   syncInterval,
		GracePeriod:    gracePeriod,
		MissionEnabled: room.MissionEnabled,
		AreaCenter:     areaCenter,
	}
}

func (room *RoomState) RoomSettingsMessage() RoomSettingsMessage {
	return room.roomSettingsMessage()
}

func (room *RoomState) BroadcastRoomSettings() {
	room.Broadcast(room.RoomSettingsMessage())
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

type syncPlayerSnapshot struct {
	PlayerID    string
	UserID      string
	Name        string
	Role        int
	IsCaught    bool
	Lat         float64
	Lng         float64
	HasLocation bool
	Color       string
	PhotoURL    string
}

func snapshotClient(client *Client) syncPlayerSnapshot {
	client.mu.Lock()
	defer client.mu.Unlock()

	playerID := client.PlayerID
	if playerID == "" && client.RoomID != "" && client.UserID != "" {
		playerID = makePlayerID(client.RoomID, client.UserID)
	}

	return syncPlayerSnapshot{
		PlayerID:    playerID,
		UserID:      client.UserID,
		Name:        client.Name,
		Role:        client.Role,
		IsCaught:    client.IsCaught,
		Lat:         client.Lat,
		Lng:         client.Lng,
		HasLocation: client.HasLocation,
		Color:       client.Color,
		PhotoURL:    client.PhotoURL,
	}
}

func (room *RoomState) waitingPlayers() []WaitingPlayerVal {
	clients := room.clientList()
	players := make([]WaitingPlayerVal, 0, len(clients))

	for _, client := range clients {
		client.mu.Lock()
		player := WaitingPlayerVal{
			UserID:   client.UserID,
			Name:     client.Name,
			Color:    client.Color,
			PhotoURL: client.PhotoURL,
		}
		client.mu.Unlock()

		if player.Name != "" {
			players = append(players, player)
		}
	}

	sort.Slice(players, func(i, j int) bool {
		return players[i].UserID < players[j].UserID
	})

	return players
}

func locationForSnapshot(player syncPlayerSnapshot, includeCoords bool) LocationVal {
	location := LocationVal{
		PlayerID: player.PlayerID,
		UserID:   player.UserID,
		Name:     player.Name,
		Role:     player.Role,
		IsCaught: player.IsCaught,
		Color:    player.Color,
		PhotoURL: player.PhotoURL,
	}
	if includeCoords && player.HasLocation {
		lat := player.Lat
		lng := player.Lng
		location.Lat = &lat
		location.Lng = &lng
	}
	return location
}

func (room *RoomState) syncMessageFor(viewer *Client) OutgoingMessage {
	viewerState := snapshotClient(viewer)
	clients := room.clientList()
	locations := make([]LocationVal, 0, len(clients))

	for _, client := range clients {
		player := snapshotClient(client)
		if player.UserID == "" {
			continue
		}

		switch {
		case viewerState.Role == 1:
			if player.Role == 0 && !player.IsCaught && player.HasLocation {
				locations = append(locations, locationForSnapshot(player, true))
			}
		case player.UserID == viewerState.UserID:
			locations = append(locations, locationForSnapshot(player, !player.IsCaught))
		}
	}

	sort.Slice(locations, func(i, j int) bool {
		return locations[i].UserID < locations[j].UserID
	})

	return OutgoingMessage{
		Event:     "sync",
		Locations: locations,
	}
}

func (room *RoomState) SendSyncToAll() {
	clients := room.clientList()

	for _, client := range clients {
		if err := sendToClient(client, room.syncMessageFor(client)); err != nil {
			client.mu.Lock()
			userID := client.UserID
			client.mu.Unlock()
			log.Printf("[Info] ユーザー %s へのsync送信に失敗しました\n", userID)
		}
	}
}

func (room *RoomState) resultMessage(roomID string, db *gorm.DB) (OutgoingMessage, error) {
	var players []models.Player
	if err := db.Where("room_id = ?", roomID).Order("user_id ASC").Find(&players).Error; err != nil {
		return OutgoingMessage{}, err
	}

	return resultMessageFromPlayers(players), nil
}

func resultMessageFromPlayers(players []models.Player) OutgoingMessage {
	results := make([]ResultVal, 0, len(players))
	var survivors []string

	for _, player := range players {
		result := ResultVal{
			UserID:   player.UserID,
			Name:     player.Name,
			Role:     player.Role,
			IsCaught: player.IsCaught,
			PhotoURL: player.PhotoURL,
		}

		if result.Role == 0 && !result.IsCaught {
			survivors = append(survivors, result.UserID)
		}
		results = append(results, result)
	}

	sort.Strings(survivors)

	return OutgoingMessage{
		Event:     "result",
		Survivors: survivors,
		Results:   results,
	}
}

func (room *RoomState) resultMessageFromClients() OutgoingMessage {
	clients := room.clientList()
	players := make([]models.Player, 0, len(clients))
	for _, client := range clients {
		snapshot := snapshotClient(client)
		if snapshot.UserID == "" {
			continue
		}
		players = append(players, models.Player{
			ID:       snapshot.PlayerID,
			UserID:   snapshot.UserID,
			Name:     snapshot.Name,
			Role:     snapshot.Role,
			IsCaught: snapshot.IsCaught,
			PhotoURL: snapshot.PhotoURL,
		})
	}

	sort.Slice(players, func(i, j int) bool {
		return players[i].UserID < players[j].UserID
	})

	return resultMessageFromPlayers(players)
}

func (room *RoomState) resultMessageBestEffort(roomID string, db *gorm.DB) OutgoingMessage {
	resultMessage, err := room.resultMessage(roomID, db)
	if err != nil {
		log.Printf("[Error] Room: %s | リザルト集計に失敗したため接続中プレイヤーから作成します: %v\n", roomID, err)
		return room.resultMessageFromClients()
	}
	return resultMessage
}

func allRunnersCaughtInDB(db *gorm.DB, roomID string) (bool, error) {
	var runnerCount int64
	if err := db.Model(&models.Player{}).Where("room_id = ? AND role = ?", roomID, 0).Count(&runnerCount).Error; err != nil {
		return false, err
	}
	if runnerCount == 0 {
		return false, nil
	}

	var uncaughtCount int64
	if err := db.Model(&models.Player{}).Where("room_id = ? AND role = ? AND is_caught = ?", roomID, 0, false).Count(&uncaughtCount).Error; err != nil {
		return false, err
	}

	return uncaughtCount == 0, nil
}

func (room *RoomState) allConnectedRunnersCaught() bool {
	clients := room.clientList()
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

func (room *RoomState) shouldEnd(now time.Time, roomID string, db *gorm.DB) bool {
	room.mu.RLock()
	if room.Status != 1 || !room.IsGameActive {
		room.mu.RUnlock()
		return false
	}

	if room.TimeLimit <= 0 || !now.Before(room.ActiveAt.Add(time.Duration(room.TimeLimit)*time.Second)) {
		room.mu.RUnlock()
		return true
	}
	room.mu.RUnlock()

	allCaught, err := allRunnersCaughtInDB(db, roomID)
	if err != nil {
		log.Printf("[Error] Room: %s | 終了判定に失敗しました: %v\n", roomID, err)
		return room.allConnectedRunnersCaught()
	}
	return allCaught
}

func (room *RoomState) finish(roomID string, db *gorm.DB) bool {
	resultMessage := room.resultMessageBestEffort(roomID, db)

	room.mu.Lock()
	if room.Status != 1 {
		room.mu.Unlock()
		return false
	}
	room.Status = 2
	room.IsGameActive = false
	room.IsGMLoopActive = false
	room.StartAt = time.Time{}
	room.ActiveAt = time.Time{}
	room.LoopID++
	room.mu.Unlock()

	if err := db.Model(&models.Room{}).Where("id = ?", roomID).Update("status", 2).Error; err != nil {
		log.Printf("[Error] Room: %s | 終了状態の保存に失敗しました: %v\n", roomID, err)
	}
	room.Broadcast(resultMessage)
	return true
}

func runGameLoop(roomID string, room *RoomState, db *gorm.DB, loopID uint64) {
	defer func() {
		room.mu.Lock()
		if room.LoopID == loopID {
			room.IsGMLoopActive = false
		}
		room.mu.Unlock()
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var nextSyncAt time.Time

	for {
		now := time.Now()

		room.mu.RLock()
		status := room.Status
		currentLoopID := room.LoopID
		hasClients := len(room.Clients) > 0
		isGameActive := room.IsGameActive
		startAt := room.StartAt
		syncInterval := room.SyncInterval
		activeAt := room.ActiveAt
		room.mu.RUnlock()

		if status != 1 || currentLoopID != loopID || !hasClients {
			return
		}

		if !isGameActive && !now.Before(startAt) {
			room.mu.Lock()
			if room.Status != 1 || room.LoopID != loopID || len(room.Clients) == 0 {
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
			room.SendSyncToAll()
		}

		if isGameActive {
			if room.shouldEnd(now, roomID, db) {
				room.finish(roomID, db)
				return
			}

			if nextSyncAt.IsZero() {
				nextSyncAt = activeAt.Add(time.Duration(syncInterval) * time.Second)
			}
			if !now.Before(nextSyncAt) {
				room.SendSyncToAll()
				for !now.Before(nextSyncAt) {
					nextSyncAt = nextSyncAt.Add(time.Duration(syncInterval) * time.Second)
				}
			}
		}

		<-ticker.C
	}
}
