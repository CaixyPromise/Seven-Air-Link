package model

import "time"

// Source is observed by the Server. IP is diagnostic, never an identity key.
// A browser client is a registered session endpoint, not proof of a physical device.
type Source struct {
	ClientID  string `json:"clientId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	DeviceID  string `json:"deviceId,omitempty"`
	Kind      string `json:"kind"`
	IP        string `json:"ip,omitempty"`
}

type Client struct {
	ID          string     `json:"id"`
	PrincipalID string     `json:"principalId"`
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	CreatedIP   string     `json:"createdIp,omitempty"`
}

type ClientClaim struct {
	Principal Principal `json:"principal"`
	Client    Client    `json:"client"`
	Token     string    `json:"token"`
}
