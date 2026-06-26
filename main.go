package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/watnow/watnow-spring-2026-team2-backend/models"
	"github.com/watnow/watnow-spring-2026-team2-backend/ws"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Warning: Error loading .env file (It's OK if running in production where env vars are set)")
	}

	db, err := gorm.Open(sqlite.Open("onigokko.db"), &gorm.Config{})
	if err != nil {
		panic("DB接続失敗")
	}

	sqlDB, err := db.DB()
	if err != nil {
		panic("データベースの取得に失敗しました")
	}

	// SQLiteは同時書き込みに弱いため、最大接続数を1にしてロックエラーを防ぐ
	sqlDB.SetMaxOpenConns(1)

	// サーバー終了時に、確実にデータベースの接続を閉じて後片付けする
	defer sqlDB.Close()

	if err := db.AutoMigrate(&models.Room{}, &models.Player{}, &models.CaptureRequest{}); err != nil {
		panic("DBマイグレーション失敗: " + err.Error())
	}
	r := setupRouter(db)

	if err := r.Run(serverAddr()); err != nil {
		panic("サーバー起動失敗: " + err.Error())
	}
}

func serverAddr() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return "0.0.0.0:" + port
}

type areaCenterInput struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type roomSettingsInput struct {
	UserID         *string          `json:"user_id"`
	TimeLimit      int              `json:"time_limit"`
	OniCount       int              `json:"oni_count"`
	MaxPlayers     *int             `json:"max_players"`
	AreaSize       string           `json:"area_size"`
	SyncInterval   int              `json:"sync_interval"`
	GracePeriod    int              `json:"grace_period"`
	MissionEnabled *bool            `json:"mission_enabled"`
	AreaCenter     *areaCenterInput `json:"area_center"`
}

const (
	defaultMaxPlayers  = 6
	minMaxPlayers      = 2
	absoluteMaxPlayers = 15
)

func isAllowedInt(value int, allowedValues ...int) bool {
	for _, allowed := range allowedValues {
		if value == allowed {
			return true
		}
	}
	return false
}

func setupRouter(db *gorm.DB) *gin.Engine {
	r := gin.Default()

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.HEAD("/healthz", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	r.GET("/ws/rooms/:id", func(c *gin.Context) {
		ws.ServeWs(c, db)
	})

	r.POST("/rooms", func(c *gin.Context) {
		var room models.Room
		var err error
		maxRetries := 5

		for i := 0; i < maxRetries; i++ {
			room = models.Room{
				ID:         models.GenerateRoomID(),
				Status:     0,
				TimeLimit:  900,
				MaxPlayers: defaultMaxPlayers,
			}
			err = db.Create(&room).Error
			if err == nil {
				break
			}
			if !strings.Contains(err.Error(), "UNIQUE") {
				break
			}
		}

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "ルームの作成に失敗しました: " + err.Error()})
			return
		}

		c.JSON(http.StatusCreated, room)
	})

	r.PUT("/rooms/:id", func(c *gin.Context) {
		roomID := c.Param("id")

		var input roomSettingsInput

		// 1. まずJSONを受け取る（データを変数 input に入れる）
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "無効なデータ形式です"})
			return
		}

		var room models.Room
		if err := db.First(&room, "id = ?", roomID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Room not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "ルーム取得に失敗しました"})
			return
		}

		if input.UserID == nil || strings.TrimSpace(*input.UserID) == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "ホストのみ設定を変更できます"})
			return
		}
		settingsUserID := strings.TrimSpace(*input.UserID)
		// Room作成直後はhost_user_idが未確定なため、最初のjoinでhostが確定するまで設定更新を拒否する。
		if room.HostUserID == "" || room.HostUserID != settingsUserID {
			c.JSON(http.StatusForbidden, gin.H{"error": "ホストのみ設定を変更できます"})
			return
		}
		var hostPlayerCount int64
		if err := db.Model(&models.Player{}).Where("room_id = ? AND user_id = ?", roomID, settingsUserID).Count(&hostPlayerCount).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "プレイヤー情報の取得に失敗しました"})
			return
		}
		if hostPlayerCount == 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "ホストのみ設定を変更できます"})
			return
		}

		// 2. その後で、受け取ったデータの中身をチェックする
		if !isAllowedInt(input.TimeLimit, 600, 900, 1800) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "制限時間は10分、15分、30分から選んでください"})
			return
		}
		if input.OniCount < 1 || input.OniCount > 3 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "鬼の人数は1〜3人で設定してください"})
			return
		}
		maxPlayers := room.MaxPlayers
		if maxPlayers < minMaxPlayers || maxPlayers > absoluteMaxPlayers {
			maxPlayers = defaultMaxPlayers
		}
		if input.MaxPlayers != nil {
			maxPlayers = *input.MaxPlayers
		}
		if maxPlayers < minMaxPlayers || maxPlayers > absoluteMaxPlayers {
			c.JSON(http.StatusBadRequest, gin.H{"error": "最大参加人数は2〜15人で設定してください"})
			return
		}
		if maxPlayers <= input.OniCount {
			c.JSON(http.StatusBadRequest, gin.H{"error": "最大参加人数は鬼の人数より多くしてください"})
			return
		}
		var currentPlayers int64
		if err := db.Model(&models.Player{}).Where("room_id = ?", roomID).Count(&currentPlayers).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "プレイヤー情報の取得に失敗しました"})
			return
		}
		if maxPlayers < int(currentPlayers) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "現在の参加人数より少ない最大参加人数にはできません"})
			return
		}
		if !isAllowedInt(input.SyncInterval, 60, 180, 300) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "更新頻度は1分、3分、5分から選んでください"})
			return
		}
		if !isAllowedInt(input.GracePeriod, 60, 120, 180) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "猶予時間は1分、2分、3分から選んでください"})
			return
		}
		if len(input.AreaSize) > 50 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "エリアの文字数が長すぎます"})
			return
		}
		if input.AreaCenter != nil {
			if input.AreaCenter.Lat < -90 || input.AreaCenter.Lat > 90 || input.AreaCenter.Lng < -180 || input.AreaCenter.Lng > 180 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "中心地点の座標が不正です"})
				return
			}
		}

		// DBの更新（0値も更新したいので map で Updates する）
		updates := map[string]interface{}{
			"time_limit":    input.TimeLimit,
			"oni_count":     input.OniCount,
			"max_players":   maxPlayers,
			"area_size":     input.AreaSize,
			"sync_interval": input.SyncInterval,
			"grace_period":  input.GracePeriod,
		}
		if input.MissionEnabled != nil {
			updates["mission_enabled"] = *input.MissionEnabled
		}
		if input.AreaCenter != nil {
			updates["area_center_lat"] = input.AreaCenter.Lat
			updates["area_center_lng"] = input.AreaCenter.Lng
			updates["has_area_center"] = true
		}

		if err := db.Model(&models.Room{}).Where("id = ?", roomID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "設定の保存に失敗しました"})
			return
		}
		var updatedRoom models.Room
		if err := db.First(&updatedRoom, "id = ?", roomID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "設定の保存に失敗しました"})
			return
		}

		// WebSocket側のメモリ(GameHub)に最新の設定を同期させる
		roomState := ws.GameHub.UpdateRoomSettingsFromModel(updatedRoom)
		roomState.BroadcastRoomSettings()

		c.JSON(http.StatusOK, gin.H{
			"status":  "success",
			"room_id": roomID,
		})
	})

	return r
}
