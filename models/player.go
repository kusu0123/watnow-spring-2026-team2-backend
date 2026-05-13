package models

type Player struct {
	ID       string  `gorm:"primaryKey"`
	RoomID   string  `gorm:"index"` // どの部屋にいるか検索しやすくする
	Name     string
	Role     int     // 0:逃走者, 1:鬼
	IsCaught bool    `gorm:"default:false"`
	Lat      float64 // 緯度 (Latitude)
	Lng      float64 // 経度 (Longitude)
}