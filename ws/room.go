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
		client.mu.Lock()
		_ = client.Conn.SetWriteDeadline(time.Now().Add(writeWait))
		err := client.Conn.WriteJSON(msg)
		_ = client.Conn.SetWriteDeadline(time.Time{})
		client.mu.Unlock()

		// 送信に失敗した場合（すでに通信が切れている等）はログだけ残す
		// ※完全に切断されている場合は、先ほど作ったPing/Pong機能が自動で回収してくれます
		if err != nil {
			log.Printf("[Info] ユーザー %s へのBroadcast送信に失敗しました\n", client.UserID)
		}
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

func (room *RoomState) waitingPlayers() []WaitingPlayerVal {
	clients := room.clientList()
	players := make([]WaitingPlayerVal, 0, len(clients))

	for _, client := range clients {
		client.mu.Lock()
		player := WaitingPlayerVal{
			UserID: client.UserID,
			Name:   client.Name,
			Color:  client.Color,
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
