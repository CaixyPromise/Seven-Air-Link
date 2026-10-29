package model

import "time"

// Service keeps principal authorization distinct from optional device delegation.
// M2 has one human Principal (the Owner). Bot/member creation belongs to M3.
type Service struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Protocol          string    `json:"protocol"`
	Target            string    `json:"target"`
	OwnerPrincipalID  string    `json:"ownerPrincipalId"`
	OwnerDeviceID     string    `json:"ownerDeviceId"`
	AllowDevices      []string  `json:"allowDevices"`
	AllowPrincipals   []string  `json:"allowPrincipals"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	Disabled          bool      `json:"disabled"`
	Revision          int64     `json:"revision"`
	Status            string    `json:"status"`
	ActiveConnections int       `json:"activeConnections"`
}
type ServiceCreate struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Protocol        string   `json:"protocol"`
	Target          string   `json:"target"`
	AllowDevices    []string `json:"allowDevices"`
	AllowPrincipals []string `json:"allowPrincipals"`
}
type Grants struct {
	AllowDevices    []string `json:"allowDevices"`
	AllowPrincipals []string `json:"allowPrincipals"`
}
type Forward struct {
	ID              string    `json:"id"`
	ServiceID       string    `json:"serviceId"`
	ActorID         string    `json:"actorId"`
	ActorRole       string    `json:"actorRole"`
	CredentialStamp string    `json:"-"`
	Listen          string    `json:"listen"`
	ExpiresAt       time.Time `json:"expiresAt"`
	StartedAt       time.Time `json:"startedAt"`
}
type TunnelRequest struct {
	ForwardID string `json:"forwardId"`
}
type TunnelTicket struct {
	TunnelID  string    `json:"tunnelId"`
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expiresAt"`
}
