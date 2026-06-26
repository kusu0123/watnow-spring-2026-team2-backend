package ws

// フロントエンドから送られてくるデータ
type IncomingMessage struct {
	Action            string   `json:"action"`
	UserID            string   `json:"user_id,omitempty"`
	Name              string   `json:"name,omitempty"`
	TargetID          string   `json:"target_id,omitempty"`
	Approved          bool     `json:"approved,omitempty"`
	Lat               float64  `json:"lat,omitempty"`
	Lng               float64  `json:"lng,omitempty"`
	Color             string   `json:"color,omitempty"`
	OniUsers          []string `json:"oni_users,omitempty"`
	PhotoURL          string   `json:"photo_url,omitempty"`
	RequestID         string   `json:"request_id,omitempty"`
	RouletteSessionID string   `json:"roulette_session_id,omitempty"`
	SpinID            int      `json:"spin_id,omitempty"`
}

// フロントエンドへ送信するデータ
type OutgoingMessage struct {
	Event              string             `json:"event"`
	Message            string             `json:"message,omitempty"`
	Players            []WaitingPlayerVal `json:"players,omitempty"`
	HostUserID         string             `json:"host_user_id,omitempty"`
	Role               *int               `json:"role,omitempty"`
	TimeLimit          int                `json:"time_limit,omitempty"`
	OniCount           int                `json:"oni_count,omitempty"`
	MaxPlayers         int                `json:"max_players,omitempty"`
	AreaSize           string             `json:"area_size,omitempty"`
	SyncInterval       int                `json:"sync_interval,omitempty"`
	GracePeriod        int                `json:"grace_period,omitempty"`
	MissionEnabled     bool               `json:"mission_enabled"`
	AreaCenter         *AreaCenterVal     `json:"area_center,omitempty"`
	OniUsers           []string           `json:"oni_users,omitempty"`
	SelectedOniUserIDs []string           `json:"selected_oni_user_ids,omitempty"`
	SelectedOniUserID  string             `json:"selected_oni_user_id,omitempty"`
	RevealIndex        *int               `json:"reveal_index,omitempty"`
	RevealedOniCount   int                `json:"revealed_oni_count,omitempty"`
	RouletteSessionID  string             `json:"roulette_session_id,omitempty"`
	SpinID             int                `json:"spin_id,omitempty"`
	RouletteOrder      []string           `json:"roulette_order,omitempty"`
	StartsAt           string             `json:"starts_at,omitempty"`
	StopAt             string             `json:"stop_at,omitempty"`
	DurationMS         int                `json:"duration_ms,omitempty"`
	DecelerationMS     int                `json:"deceleration_ms,omitempty"`
	TargetID           string             `json:"target_id,omitempty"`
	AttackerName       string             `json:"attacker_name,omitempty"`
	Approved           bool               `json:"approved,omitempty"`
	PhotoURL           string             `json:"photo_url,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
	AttackerID         string             `json:"attacker_id,omitempty"`
	ExpiresAt          string             `json:"expires_at,omitempty"`
	Locations          []LocationVal      `json:"locations,omitempty"`
	Survivors          []string           `json:"survivors,omitempty"`
	Results            []ResultVal        `json:"results,omitempty"`
	ServerNow          string             `json:"server_now,omitempty"`
	GameID             string             `json:"game_id,omitempty"`
	GameStartedAt      string             `json:"game_started_at,omitempty"`
	GraceEndsAt        string             `json:"grace_ends_at,omitempty"`
	GameEndsAt         string             `json:"game_ends_at,omitempty"`
	GamePhase          string             `json:"game_phase,omitempty"`
	RemainingSeconds   *int               `json:"remaining_seconds,omitempty"`
	Winner             string             `json:"winner,omitempty"`
	EndReason          string             `json:"end_reason,omitempty"`
}

type RoomSettingsMessage struct {
	Event          string         `json:"event"`
	TimeLimit      int            `json:"time_limit"`
	OniCount       int            `json:"oni_count"`
	MaxPlayers     int            `json:"max_players"`
	AreaSize       string         `json:"area_size"`
	SyncInterval   int            `json:"sync_interval"`
	GracePeriod    int            `json:"grace_period"`
	MissionEnabled bool           `json:"mission_enabled"`
	AreaCenter     *AreaCenterVal `json:"area_center"`
}

type AreaCenterVal struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// 待機中プレイヤーのまとまり
type WaitingPlayerVal struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Color    string `json:"color"`
	PhotoURL string `json:"photo_url,omitempty"`
	IsHost   bool   `json:"is_host"`
}

// 位置情報のまとまり
type LocationVal struct {
	PlayerID string   `json:"player_id"`
	UserID   string   `json:"user_id"`
	Name     string   `json:"name"`
	Role     int      `json:"role"`
	IsCaught bool     `json:"is_caught"`
	Lat      *float64 `json:"lat,omitempty"`
	Lng      *float64 `json:"lng,omitempty"`
	Color    string   `json:"color"`
	PhotoURL string   `json:"photo_url,omitempty"`
}

// 結果発表用のまとまり
type ResultVal struct {
	UserID          string `json:"user_id"`
	Name            string `json:"name"`
	Role            int    `json:"role"`
	IsCaught        bool   `json:"is_caught"`
	PhotoURL        string `json:"photo_url,omitempty"`
	CapturedAt      string `json:"captured_at,omitempty"`
	SurvivalSeconds *int   `json:"survival_seconds,omitempty"`
}
