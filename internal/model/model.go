// Package model defines the shared wire contract. A Device is not a Principal.
package model

import "time"

const Protocol = 1
const Version = "0.1.0-rc.1"

type Device struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	OS          string     `json:"os"`
	Arch        string     `json:"arch"`
	NodeVersion string     `json:"nodeVersion"`
	OwnerID     string     `json:"ownerId"`
	CreatedAt   time.Time  `json:"createdAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	LastSeen    *time.Time `json:"lastSeen,omitempty"`
	Status      string     `json:"status"`
	IP          string     `json:"ip,omitempty"`
	SessionID   string     `json:"sessionId,omitempty"`
}
type Event struct {
	Seq           int64     `json:"seq"`
	Time          time.Time `json:"time"`
	ActorID       string    `json:"actorId"`
	DeviceID      string    `json:"deviceId,omitempty"`
	Action        string    `json:"action"`
	ResourceID    string    `json:"resourceId,omitempty"`
	Result        string    `json:"result"`
	Code          string    `json:"code,omitempty"`
	RequestID     string    `json:"requestId,omitempty"`
	CorrelationID string    `json:"correlationId,omitempty"`
}
type PairingRequest struct {
	TTLSeconds int `json:"ttlSeconds"`
}
type PairingResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type JoinRequest struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	NodeVersion string `json:"nodeVersion"`
	Protocol    int    `json:"protocol"`
}
type JoinResponse struct {
	DeviceID string `json:"deviceId"`
	Token    string `json:"token"`
	Protocol int    `json:"protocol"`
}
type Control struct {
	NetworkProtocol int       `json:"networkProtocol,omitempty"`
	ServiceIDs      []string  `json:"serviceIds,omitempty"`
	TunnelID        string    `json:"tunnelId,omitempty"`
	ServiceID       string    `json:"serviceId,omitempty"`
	Ticket          string    `json:"ticket,omitempty"`
	Target          string    `json:"target,omitempty"`
	Code            string    `json:"code,omitempty"`
	ExpiresAt       time.Time `json:"expiresAt,omitempty"`
	Type            string    `json:"type"`
	Protocol        int       `json:"protocol,omitempty"`
	SessionID       string    `json:"sessionId,omitempty"`
	HeartbeatMS     int64     `json:"heartbeatMs,omitempty"`
	TimeoutMS       int64     `json:"timeoutMs,omitempty"`
	Seq             int64     `json:"seq,omitempty"`
	ServerTime      time.Time `json:"serverTime,omitempty"`
}
type Config struct {
	Version     int    `json:"version"`
	Server      string `json:"server"`
	Fingerprint string `json:"fingerprint"`
	Token       string `json:"token"`
	SubjectID   string `json:"subjectId"`
	Role        string `json:"role"`
}
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
	Retryable bool   `json:"retryable"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }
