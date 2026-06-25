package model

// Listing — объявление на доске
type Listing struct {
	ID                 string   `json:"id"`
	City               string   `json:"city"`
	DependencyType     string   `json:"dependency_type"`
	HelpType           string   `json:"help_type"`
	Urgency            string   `json:"urgency"`
	Languages          []string `json:"languages"`
	WalletAddress      string   `json:"-"`
	PaymentTxID        string   `json:"-"`
	ReconnectionHashes []string `json:"-"`
	VisibleUntil       int64    `json:"visible_until"`
	CreatedAt          int64    `json:"created_at"`
	Status             string   `json:"status"`
	ResponsesCount     int      `json:"responses_count,omitempty"`
	TimeLeft           int64    `json:"time_left,omitempty"`
	IsSample           bool     `json:"is_sample,omitempty"`
}

// Response — отклик психолога
type Response struct {
	ID               string `json:"id"`
	ListingID        string `json:"listing_id"`
	CounselorAddress string `json:"-"`
	CounselorPubkey  string `json:"counselor_pubkey"`
	Status           string `json:"status"`
	CreatedAt        int64  `json:"created_at"`
	CancelledAt      *int64 `json:"cancelled_at,omitempty"`
	CooldownUntil    *int64 `json:"-"`
}

// ResponseWithReputation — отклик + рейтинг для клиента
type ResponseWithReputation struct {
	Response
	Reputation *Reputation `json:"reputation,omitempty"`
}

// Invoice — платёж
type Invoice struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Address      string  `json:"address"`
	AmountUSD    float64 `json:"amount_usd"`
	AmountCrypto string  `json:"amount_crypto,omitempty"`
	Currency     string  `json:"currency"`
	PayerAddress string  `json:"-"`
	TxID         string  `json:"-"`
	Status       string  `json:"status"`
	CreatedAt    int64   `json:"created_at"`
}

// WalletSession — активная сессия кошелька
type WalletSession struct {
	WalletAddress  string  `json:"-"`
	Role           string  `json:"role"`
	BalanceStatus  string  `json:"balance_status"`
	MinRequiredUSD float64 `json:"min_required_usd"`
	LastCheckedAt  *int64  `json:"last_checked_at,omitempty"`
	Verified       bool    `json:"verified"`
	FirstSeen      int64   `json:"first_seen"`
	CreatedAt      int64   `json:"created_at"`
}

// Reputation — рейтинг психолога (агрегат)
type Reputation struct {
	CounselorHash    string `json:"-"`
	Region           string `json:"region"`
	SessionsTotal    int    `json:"sessions_total"`
	SessionsCompleted int   `json:"sessions_completed"`
	SessionsEarlyExit int  `json:"sessions_early_exit"`
	ThumbsUp         int    `json:"thumbs_up"`
	ThumbsDown       int    `json:"thumbs_down"`
	ReturningClients int    `json:"returning_clients"`
	FirstSeen        int64  `json:"first_seen"`
}

// ChatRoom — чат-комната
type ChatRoom struct {
	ID               string `json:"id"`
	ListingID        string `json:"listing_id"`
	ResponseID       string `json:"response_id"`
	ClientAddress    string `json:"-"`
	CounselorAddress string `json:"-"`
	ClientPubkey     string `json:"client_pubkey"`
	CounselorPubkey  string `json:"counselor_pubkey"`
	StartedAt        int64  `json:"started_at"`
	ExpiresAt        int64  `json:"expires_at"`
	ClosedAt         *int64 `json:"closed_at,omitempty"`
	ClosedBy         string `json:"closed_by,omitempty"`
	Status           string `json:"status"`
}

// EncryptedMessage — зашифрованное сообщение (сервер не может прочитать)
type EncryptedMessage struct {
	ID           string `json:"id"`
	RoomID       string `json:"room_id"`
	SenderPubkey string `json:"sender_pubkey"`
	Nonce        string `json:"nonce"`
	Ciphertext   string `json:"ciphertext"`
	CreatedAt    int64  `json:"created_at"`
}

// ReviewToken — анонимный одноразовый токен для оценки
type ReviewToken struct {
	Token        string `json:"token"`
	CounselorHash string `json:"-"`
	IsPaid       bool   `json:"-"`
	Used         bool   `json:"-"`
	CreatedAt    int64  `json:"-"`
	ExpiresAt    int64  `json:"-"`
}

// AbuseReport — входящий abuse report от психолога
type AbuseReport struct {
	CounselorHash string   `json:"-"`
	ClientHash    string   `json:"-"`
	Categories    []string `json:"categories"`
}
