package domain

import (
	"encoding/json"
	"slices"
	"time"
)

// Export job states. A job is QUEUED until the export runner writes the file
// (READY) and DELETED once the file is removed after ExportFileLifetime.
const (
	ExportQueued  = "QUEUED"
	ExportReady   = "READY"
	ExportDeleted = "DELETED"

	// CodeNoScope is ANA-0102: the caller has no legal-entity scope, so there
	// is nothing to export (BR-46.9).
	CodeNoScope = "ANA-0102"
)

// IsExportFormat reports whether f is a file export format.
func IsExportFormat(f string) bool { return slices.Contains(exportFormats, f) }

// Export is one explorer export (export_job, the export log of §9.46.14).
// The plan is the compiled, scope-filtered query the runner executes, so an
// export never reads more than the requester may see.
type Export struct {
	ID            string    `json:"exportId"`
	RequestedBy   string    `json:"requestedBy"`
	QueryHash     string    `json:"queryHash"`
	Plan          Plan      `json:"plan"`
	Format        string    `json:"format"`
	Mode          string    `json:"mode"`
	RowCount      int64     `json:"rowCount"`
	LinkTTLHours  int       `json:"linkTtlHours"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	LinkExpiresAt time.Time `json:"linkExpiresAt"`
	DeleteAt      time.Time `json:"deleteAt"`
	// Reused is true when an identical request inside ExportReuseWindow
	// returned this existing job instead of starting a new one (BR-46.7).
	Reused bool `json:"reused"`
}

// ExportCanonical renders an export request in the stable form that QueryHash
// fingerprints: the requesting user, the format and the compiled plan, which
// carries the caller's scope filters. Including the user keeps one person's
// export from being handed to another (BR-46.7).
func ExportCanonical(userID, format string, plan Plan) string {
	// Plan holds only strings and slices of strings, so Marshal cannot fail.
	raw, _ := json.Marshal(plan)
	return userID + "|" + format + "|" + string(raw)
}
