package main

import (
	"fmt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"github.com/watnow/watnow-spring-2026-team2-backend/models" // 手順1のモジュール名に合わせる
)

func main() {
	db, err := gorm.Open(sqlite.Open("onigokko.db"), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}

	// テーブル作成
	db.AutoMigrate(&models.Room{}, &models.Player{})

	fmt.Println("Database connection and migration successful!")
}