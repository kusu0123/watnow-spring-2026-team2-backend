package models

import (
	"math/rand"
	"time"
)

type Room struct {
	ID           string    `gorm:"primaryKey" json:"room_id"`
	Status       int       `json:"status"` // 0:待機中, 1:進行中, 2:終了
	TimeLimit    int       `json:"time_limit"`
	OniCount     int       `json:"oni_count"`     // 追加：鬼の人数
	AreaSize     string    `json:"area_size"`     // 追加：プレイエリアの広さ
	SyncInterval int       `json:"sync_interval"` // 追加：位置情報の公開頻度（秒）
	GracePeriod  int       `json:"grace_period"`  // 追加：逃走猶予時間（秒）
	CreatedAt    time.Time `json:"created_at"`
	Players      []Player  `gorm:"foreignKey:RoomID" json:"players,omitempty"`
}

// 4桁のランダムな英数字を生成する関数
func GenerateRoomID() string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	// 実行するたびに異なる乱数が出るように現在時刻をシード値に使う
	seed := rand.NewSource(time.Now().UnixNano())
	random := rand.New(seed)

	result := make([]byte, 4)
	for i := range result {
		result[i] = charset[random.Intn(len(charset))]
	}
	return string(result)
}
