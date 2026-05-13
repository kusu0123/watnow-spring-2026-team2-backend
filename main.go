package main

import (
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/watnow/watnow-spring-2026-team2-backend/models"
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

	r.Run(":8080")
}