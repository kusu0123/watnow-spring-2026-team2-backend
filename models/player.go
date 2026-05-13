package models

type Player struct {
	ID       string `gorm:"primaryKey"`
	RoomID   string `gorm:"index"`
	Name     string
	Role     int    // 0:逃走者, 1:鬼
	IsCaught bool   `gorm:"default:false"`
}