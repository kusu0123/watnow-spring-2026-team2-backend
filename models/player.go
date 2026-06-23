package models

import "time"

type Player struct {
	ID          string `gorm:"primaryKey"`
	RoomID      string `gorm:"index:idx_players_room_user,unique"`
	UserID      string `gorm:"index:idx_players_room_user,unique"`
	Name        string
	Role        int     // 0:逃走者, 1:鬼
	IsCaught    bool    `gorm:"default:false"`
	Lat         float64 // 緯度 (Latitude)
	Lng         float64 // 経度 (Longitude)
	HasLocation bool    `gorm:"default:false"`
	Color       string
	PhotoURL    string // Supabase Storageの画像URLを保存するポケット
	CapturedAt  *time.Time
}
