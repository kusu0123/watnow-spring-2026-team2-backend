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
	db.AutoMigrate(&models.Room{}, &models.Player{})

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

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "無効なデータ形式です"})
			return
		}

		// DBの更新（0値も更新したいので map で Updates する）
		tx := db.Model(&models.Room{}).Where("id = ?", roomID).Updates(map[string]interface{}{
			"time_limit":     input.TimeLimit,
			"oni_count":      input.OniCount,
			"area_size":      input.AreaSize,
			"sync_interval":  input.SyncInterval,
			"grace_period":   input.GracePeriod,
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
