// Package events holds the HR event type constants and payloads (8.42.5).
package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "hr"

var (
	WorkerCreated     = events.Type(Context, "worker", "created", 1)
	EmploymentChanged = events.Type(Context, "employment", "changed", 1)
	// The PRD name position.assignment.changed has four segments after the
	// namespace; the kernel regex allows three, so it collapses like org's
	// teammembership.changed.
	PositionAssignmentChanged = events.Type(Context, "positionassignment", "changed", 1)
)

// All lists every event type the service publishes (asyncapi.yaml).
func All() []string { return []string{WorkerCreated, EmploymentChanged, PositionAssignmentChanged} }

// ActionHire is the employment.changed action for a new employment. Transfer,
// termination and the other personnel actions arrive with FR-42.5.
const ActionHire = "HIRE"

// WorkerCreatedData carries no P2/P3 values: the vault holds those.
type WorkerCreatedData struct {
	ID              string `json:"id"`
	PersonnelNumber string `json:"personnel_number"`
	WorkerType      string `json:"worker_type"`
	PartyPersonID   string `json:"party_person_id"`
}

// EmploymentChangedData is the 8.0 contract (workerId, employmentId, action,
// effectiveFrom, effectiveTo) in the kernel's snake_case payload style.
type EmploymentChangedData struct {
	ID             string  `json:"id"`
	WorkerID       string  `json:"worker_id"`
	LegalEntityID  string  `json:"legal_entity_id"`
	EmploymentType string  `json:"employment_type"`
	AssignmentKind string  `json:"assignment_kind"`
	Action         string  `json:"action"`
	ValidFrom      string  `json:"valid_from"`
	ValidTo        *string `json:"valid_to"`
}

// PositionAssignmentChangedData carries the 8.42.5 fields; manager_position_id
// is the reports-to position as-of the assignment start, for manager-role
// authorization and workflow routing.
type PositionAssignmentChangedData struct {
	ID                string  `json:"id"`
	WorkerID          string  `json:"worker_id"`
	PositionID        string  `json:"position_id"`
	ManagerPositionID *string `json:"manager_position_id"`
	FTEShare          string  `json:"fte_share"`
	IsPrimary         bool    `json:"is_primary"`
	ValidFrom         string  `json:"valid_from"`
	ValidTo           *string `json:"valid_to"`
}
