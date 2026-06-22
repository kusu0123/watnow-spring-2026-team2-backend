package models

import (
	"time"
)

type CaptureRequest struct {
	ID             string     `gorm:"primaryKey;type:varchar(36)" json:"request_id"` // UUIDを使用
	RoomID         string     `gorm:"index;not null" json:"room_id"`
	AttackerUserID string     `gorm:"not null" json:"attacker_user_id"`
	TargetUserID   string     `gorm:"index;not null" json:"target_user_id"`
	Status         string     `gorm:"not null;default:'pending'" json:"status"` // pending, approved, rejected, expired
	PhotoURL       string     `gorm:"not null" json:"photo_url"`
	CreatedAt      time.Time  `gorm:"autoCreateTime" json:"created_at"`
	ExpiresAt      time.Time  `gorm:"not null" json:"expires_at"`
	RespondedAt    *time.Time `json:"responded_at,omitempty"` // null許容のためポインタ型
}