package main

import (
	"net/http"

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
	r := gin.Default()

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

		var input struct {
			TimeLimit    int    `json:"time_limit"`
			OniCount     int    `json:"oni_count"`
			AreaSize     string `json:"area_size"`
			SyncInterval int    `json:"sync_interval"`
			GracePeriod  int    `json:"grace_period"`
		}

		// 1. まずJSONを受け取る（データを変数 input に入れる）
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "無効なデータ形式です"})
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
		if input.SyncInterval < 1 || input.SyncInterval > 60 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "更新頻度は1〜60の間で設定してください"})
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
		
		// DBの更新（0値も更新したいので map で Updates する）
		tx := db.Model(&models.Room{}).Where("id = ?", roomID).Updates(map[string]interface{}{
			"time_limit":    input.TimeLimit,
			"oni_count":     input.OniCount,
			"area_size":     input.AreaSize,
			"sync_interval": input.SyncInterval,
			"grace_period":  input.GracePeriod,
		})
		if tx.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "設定の保存に失敗しました"})
			return
		}
		if tx.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Room not found"})
			return
		}

		// WebSocket側のメモリ(GameHub)に最新の設定を同期させる
		ws.GameHub.UpdateRoomSettings(roomID, input.TimeLimit, input.OniCount, input.SyncInterval, input.GracePeriod)

		c.JSON(http.StatusOK, gin.H{
			"status":  "success",
			"room_id": roomID,
		})
	})

	r.Run(":8080")
}
