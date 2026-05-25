package ws

import (
	"encoding/json"
	"net/http"
	"sync"

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
	Clients   map[*Client]bool
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
	Lat      float64 `json:"lat,omitempty"`
	Lng      float64 `json:"lng,omitempty"`
}

type OutgoingMessage struct {
	Event     string        `json:"event"`
	Players   []string      `json:"players,omitempty"`
	Role      *int          `json:"role,omitempty"`
	TimeLimit int           `json:"time_limit,omitempty"`
	TargetID  string        `json:"target_id,omitempty"`
	Locations []LocationVal `json:"locations,omitempty"`
}

type LocationVal struct {
	UserID   string  `json:"user_id"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	IsCaught bool    `json:"is_caught"`
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
			room.Status = 1
			isFirst := true
			for c := range room.Clients {
				c.mu.Lock()
				if isFirst {
					c.Role = 1
					isFirst = false
				} else {
					c.Role = 0
				}
				c.mu.Unlock()
			}
			room.mu.Unlock()

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

		case "move":
			client.mu.Lock()
			client.Lat = msg.Lat
			client.Lng = msg.Lng
			client.mu.Unlock()

			room.mu.RLock()
			var locs []LocationVal
			for c := range room.Clients {
				c.mu.Lock()
				locs = append(locs, LocationVal{
					UserID:   c.UserID,
					Lat:      c.Lat,
					Lng:      c.Lng,
					IsCaught: c.IsCaught,
				})
				c.mu.Unlock()
			}
			room.mu.RUnlock()

			room.Broadcast(OutgoingMessage{
				Event:     "sync",
				Locations: locs,
			})

		case "capture":
			room.mu.Lock()
			for c := range room.Clients {
				c.mu.Lock()
				if c.UserID == msg.TargetID {
					c.IsCaught = true
				}
				c.mu.Unlock()
			}
			room.mu.Unlock()

			room.Broadcast(OutgoingMessage{
				Event:    "captured",
				TargetID: msg.TargetID,
			})
		}
	}
}
