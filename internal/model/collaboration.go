package model

import "time"

// Principal is an authenticated participant, never a device or model runtime.
// The member role has the same API rights for humans and bots.
type Principal struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Role      string     `json:"role"`
	OwnerID   string     `json:"ownerId"`
	DeviceID  string     `json:"deviceId,omitempty"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	Listening int        `json:"listening"` // Active inbox subscriptions, NOT an AI execution state.
}
type PrincipalCreate struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	DeviceID   string `json:"deviceId,omitempty"`
	TTLSeconds int64  `json:"ttlSeconds"`
}
type Enrollment struct {
	Principal Principal `json:"principal"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type Claim struct {
	Principal Principal `json:"principal"`
	Token     string    `json:"token"`
}
type Message struct {
	ID              string    `json:"id"`
	Seq             int64     `json:"seq"`
	ClientMessageID string    `json:"clientMessageId"`
	SenderID        string    `json:"senderId"`
	RecipientID     string    `json:"recipientId"` // "shared" is the one workspace-wide channel.
	Kind            string    `json:"kind"`
	Text            string    `json:"text"`
	Attachments     []string  `json:"attachments"`
	ReplyTo         string    `json:"replyTo,omitempty"`
	CorrelationID   string    `json:"correlationId,omitempty"`
	Status          string    `json:"status"` // "accepted" only; never implies read or executed.
	CreatedAt       time.Time `json:"createdAt"`
}
type MessageSend struct {
	ClientMessageID string   `json:"clientMessageId"`
	To              string   `json:"to"`
	Text            string   `json:"text"`
	Attachments     []string `json:"attachments,omitempty"`
	ReplyTo         string   `json:"replyTo,omitempty"`
	CorrelationID   string   `json:"correlationId,omitempty"`
}
type MessagePage struct {
	Messages    []Message `json:"messages"`
	NextCursor  int64     `json:"nextCursor"`
	SnapshotSeq int64     `json:"snapshotSeq"`
	HasMore     bool      `json:"hasMore"`
}
type File struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Size        int64      `json:"size"`
	SHA256      string     `json:"sha256"`
	OwnerID     string     `json:"ownerId"`
	RecipientID string     `json:"recipientId"`
	CreatedAt   time.Time  `json:"createdAt"`
	DeletedAt   *time.Time `json:"deletedAt,omitempty"`
}
