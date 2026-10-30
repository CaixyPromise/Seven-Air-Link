package model

import "time"

// SharedNote is one workspace-wide board, not a document/Agent task system.
// Revision is the CAS token and SSE snapshot ID; text never appears in audit.
type SharedNote struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Revision  int64      `json:"revision"`
	UpdatedBy string     `json:"updatedBy"`
	UpdatedAt *time.Time `json:"updatedAt"`
}

type NoteWrite struct {
	Text          string `json:"text"`
	BaseRevision  *int64 `json:"baseRevision"`
	CorrelationID string `json:"correlationId,omitempty"`
}

type WebPasswordStatus struct {
	Enabled   bool       `json:"enabled"`
	Revision  int64      `json:"revision"`
	UpdatedAt *time.Time `json:"updatedAt"`
	Algorithm string     `json:"algorithm,omitempty"`
}
