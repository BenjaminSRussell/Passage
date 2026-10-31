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
	// AbsenceApproved is written in the same transaction as the balance
	// consumption it reports (FR-42.7). Time, projects and payroll consume it.
	AbsenceApproved = events.Type(Context, "absence", "approved", 1)
	// BenefitEnrollmentChanged is written in the same transaction as an
	// enrollment starts or ends (8.42.5); payroll deductions consume it. The
	// PRD name benefit.enrollment.changed collapses to one entity segment like
	// positionassignment.
	BenefitEnrollmentChanged = events.Type(Context, "benefitenrollment", "changed", 1)
	// WorkerSkillChanged is written in the same transaction as a worker's
	// rating on a skill changes (FR-42.10); wave-45 dispatch keeps its
	// technician skill index from it.
	WorkerSkillChanged = events.Type(Context, "workerskill", "changed", 1)
	// CertificationIssued is written in the same transaction as a certificate
	// is recorded (BR-42.8). Wave-45 dispatch stops matching on the expiry day;
	// reminders (FR-42.20) read expires_on.
	CertificationIssued = events.Type(Context, "certification", "issued", 1)
	// CourseCompleted is written in the same transaction as a registration is
	// recorded ATTENDED with PASS and its skills and certificate are written
	// (8.42.5, BR-42.9); wave-45 dispatch reads the skills, notifications the
	// certificate.
	CourseCompleted = events.Type(Context, "course", "completed", 1)
	// IncidentChanged is written when an incident is reported or moves to
	// UNDER_REVIEW or CLOSED (FR-42.16). It carries ids, codes and counts only:
	// no worker name and none of the sealed medical details.
	IncidentChanged = events.Type(Context, "incident", "changed", 1)
	// LoanedItemChanged is written when an item is issued, returned or lost
	// (FR-42.17); wave-44 assets and the offboarding checklist consume it.
	LoanedItemChanged = events.Type(Context, "loaneditem", "changed", 1)
	// ChecklistChanged is written when a checklist is started or completed
	// (FR-42.19); notifications and MSS consume it.
	ChecklistChanged = events.Type(Context, "checklist", "changed", 1)
)

// All lists every event type the service publishes (asyncapi.yaml).
func All() []string {
	return []string{WorkerCreated, EmploymentChanged, PositionAssignmentChanged, AbsenceApproved, BenefitEnrollmentChanged,
		WorkerSkillChanged, CertificationIssued, CourseCompleted, IncidentChanged, LoanedItemChanged, ChecklistChanged}
}

type IncidentChangedData struct {
	ID             string `json:"id"`
	CaseNo         int    `json:"case_no"`
	WorkerID       string `json:"worker_id"`
	LegalEntityID  string `json:"legal_entity_id"`
	SiteID         string `json:"site_id"`
	IncidentType   string `json:"incident_type"`
	Status         string `json:"status"`
	Outcome        string `json:"outcome"`
	OSHARecordable bool   `json:"osha_recordable"`
	OccurredAt     string `json:"occurred_at"`
}

type LoanedItemChangedData struct {
	ID       string  `json:"id"`
	WorkerID string  `json:"worker_id"`
	LoanType string  `json:"loan_type"`
	AssetID  *string `json:"asset_id"`
	Status   string  `json:"status"`
	IssuedOn string  `json:"issued_on"`
	DueOn    *string `json:"due_on"`
	ClosedOn *string `json:"closed_on"`
}

type ChecklistChangedData struct {
	ID            string `json:"id"`
	WorkerID      string `json:"worker_id"`
	Kind          string `json:"kind"`
	EffectiveDate string `json:"effective_date"`
	Status        string `json:"status"`
	TaskCount     int    `json:"task_count"`
}

// ActionHire is the employment.changed action for a new employment. Transfer,
// termination and the other personnel actions arrive with FR-42.5.
const ActionHire = "HIRE"

// AbsenceApprovedData is the 8.42.5 contract (workerId, planId, from, to,
// hours) in the kernel's snake_case payload style. The PRD's hours becomes
// quantity plus unit: a DAYS plan cannot be turned into hours until the wave-8
// work calendars exist. Dates are inclusive YYYY-MM-DD; no P2/P3 values.
type AbsenceApprovedData struct {
	ID       string `json:"id"`
	WorkerID string `json:"worker_id"`
	PlanID   string `json:"plan_id"`
	Unit     string `json:"unit"`
	DateFrom string `json:"date_from"`
	DateTo   string `json:"date_to"`
	Quantity string `json:"quantity"`
}

// BenefitEnrollmentChangedData is the 8.42.5 contract (workerId, planId,
// option, validity, employeeRate) plus what payroll needs to take the
// deduction: the plan's currency, pre-tax flag and deduction code. Action is
// START or END; an END carries valid_to and end_reason. Dependents are never
// included.
type BenefitEnrollmentChangedData struct {
	ID                   string  `json:"id"`
	WorkerID             string  `json:"worker_id"`
	PlanID               string  `json:"plan_id"`
	PlanCode             string  `json:"plan_code"`
	OptionCode           string  `json:"option_code"`
	Action               string  `json:"action"`
	ValidFrom            string  `json:"valid_from"`
	ValidTo              *string `json:"valid_to"`
	EndReason            *string `json:"end_reason"`
	EmployeeRate         string  `json:"employee_rate"`
	EmployerRate         string  `json:"employer_rate"`
	Currency             string  `json:"currency"`
	RateBasis            string  `json:"rate_basis"`
	PreTax               bool    `json:"pre_tax"`
	PayrollDeductionCode *string `json:"payroll_deduction_code"`
}

// Benefit enrollment actions.
const (
	BenefitStart = "START"
	BenefitEnd   = "END"
)

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

// WorkerSkillChangedData carries the skill code and the new rating; a rating
// recorded again unchanged publishes nothing. previous_rating is null when
// the worker was not rated on the skill before.
type WorkerSkillChangedData struct {
	WorkerID       string `json:"worker_id"`
	SkillID        string `json:"skill_id"`
	SkillCode      string `json:"skill_code"`
	Rating         int    `json:"rating"`
	PreviousRating *int   `json:"previous_rating"`
	AssessedOn     string `json:"assessed_on"`
}

// CertificationIssuedData carries the certificate code and its validity
// [issued_on, expires_on); a null expires_on never expires. No holder names.
type CertificationIssuedData struct {
	ID        string  `json:"id"`
	WorkerID  string  `json:"worker_id"`
	Code      string  `json:"code"`
	IssuedOn  string  `json:"issued_on"`
	ExpiresOn *string `json:"expires_on"`
}

// CourseSkillAward is one skill outcome of a completed course: the rating the
// worker holds after the completion, never lower than before (BR-42.9).
type CourseSkillAward struct {
	SkillID   string `json:"skill_id"`
	SkillCode string `json:"skill_code"`
	Rating    int    `json:"rating"`
}

// CourseCompletedData is the 8.42.5 contract (workerId, courseId, sessionId,
// skills[], certificationId) in the kernel's snake_case payload style, plus
// the registration id. certification_id is null when the course issues none.
type CourseCompletedData struct {
	RegistrationID  string             `json:"registration_id"`
	WorkerID        string             `json:"worker_id"`
	CourseID        string             `json:"course_id"`
	SessionID       string             `json:"session_id"`
	CompletedOn     string             `json:"completed_on"`
	Skills          []CourseSkillAward `json:"skills"`
	CertificationID *string            `json:"certification_id"`
}
