package main

import (
	"errors"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/watnow/watnow-spring-2026-team2-backend/models"
	"github.com/watnow/watnow-spring-2026-team2-backend/ws"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
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

	if err := db.AutoMigrate(&models.Room{}, &models.Player{}); err != nil {
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
	TimeLimit    int              `json:"time_limit"`
	OniCount     int              `json:"oni_count"`
	AreaSize     string           `json:"area_size"`
	SyncInterval int              `json:"sync_interval"`
	GracePeriod  int              `json:"grace_period"`
	AreaCenter   *areaCenterInput `json:"area_center"`
}

func setupRouter(db *gorm.DB) *gin.Engine {
	r := gin.Default()

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.GET("/ws/rooms/:id", func(c *gin.Context) {
		ws.ServeWs(c, db)
	})

	r.POST("/rooms", func(c *gin.Context) {
		room := models.Room{
			ID:        models.GenerateRoomID(), // models/room.goで作った関数
			Status:    0,
			TimeLimit: 900,
		}

		if err := db.Create(&room).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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

		// 2. その後で、受け取ったデータの中身をチェックする
		if input.TimeLimit < 1 || input.TimeLimit > 3600 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "制限時間は1〜3600の間で設定してください"})
			return
		}
		if input.OniCount < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "鬼の人数は1人以上にしてください"})
			return
		}
		if input.SyncInterval < 1 || input.SyncInterval > 300 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "更新頻度は1〜300の間で設定してください"})
			return
		}
		if input.GracePeriod < 0 || input.GracePeriod > 300 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "猶予時間は0〜300の間で設定してください"})
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
			"area_size":     input.AreaSize,
			"sync_interval": input.SyncInterval,
			"grace_period":  input.GracePeriod,
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
