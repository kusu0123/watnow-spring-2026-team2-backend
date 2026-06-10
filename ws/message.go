package ws

// フロントエンドから送られてくるデータ
type IncomingMessage struct {
	Action   string   `json:"action"`
	UserID   string   `json:"user_id,omitempty"`
	Name     string   `json:"name,omitempty"`
	TargetID string   `json:"target_id,omitempty"`
	Approved bool     `json:"approved,omitempty"`
	Lat      float64  `json:"lat,omitempty"`
	Lng      float64  `json:"lng,omitempty"`
	Color    string   `json:"color,omitempty"`
	OniUsers []string `json:"oni_users,omitempty"`
}

// フロントエンドへ送信するデータ
type OutgoingMessage struct {
	Event        string             `json:"event"`
	Message      string             `json:"message,omitempty"`
	Players      []WaitingPlayerVal `json:"players,omitempty"`
	Role         *int               `json:"role,omitempty"`
	TimeLimit    int                `json:"time_limit,omitempty"`
	TargetID     string             `json:"target_id,omitempty"`
	AttackerName string             `json:"attacker_name,omitempty"`
	Approved     bool               `json:"approved,omitempty"`
	Locations    []LocationVal      `json:"locations,omitempty"`
	Survivors    []string           `json:"survivors,omitempty"`
	Results      []ResultVal        `json:"results,omitempty"`
}

// 待機中プレイヤーのまとまり
type WaitingPlayerVal struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Color  string `json:"color"`
}

// 位置情報のまとまり
type LocationVal struct {
	UserID   string  `json:"user_id"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	IsCaught bool    `json:"is_caught"`
	Color    string  `json:"color"`
}

// 結果発表用のまとまり
type ResultVal struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Role     int    `json:"role"`
	IsCaught bool   `json:"is_caught"`
}
