package models

import "time"

type Room struct {
	ID        string    `gorm:"primaryKey"`
	Status    int       // 0:待機中, 1:進行中, 2:終了
	TimeLimit int
	CreatedAt time.Time
	// Player構造体は別ファイル(player.go)にありますが、
	// 同じmodelsパッケージなのでそのまま使えます。
	Players   []Player  `gorm:"foreignKey:RoomID"`
}