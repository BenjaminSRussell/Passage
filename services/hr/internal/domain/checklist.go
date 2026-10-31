package domain

import "time"

const (
	ChecklistOnboarding  = "ONBOARDING"
	ChecklistOffboarding = "OFFBOARDING"
	ChecklistTransfer    = "TRANSFER"

	RoleManager   = "MANAGER"
	RoleWorker    = "WORKER"
	RoleHRPartner = "HR_PARTNER"
	RoleIT        = "IT"

	ChecklistCompleted = "COMPLETED"

	TaskOpen    = "OPEN"
	TaskDone    = "DONE"
	TaskSkipped = "SKIPPED"

	LoanOut      = "OUT"
	LoanReturned = "RETURNED"
	LoanLost     = "LOST"

	ReturnLoanedItemsCode  = "RETURN_LOANED_ITEMS"
	ReturnLoanedItemsTitle = "Return loaned items"

	CodeLoanedItemsOut = "HCM-LOAN-001"
)

var (
	ChecklistKinds = []string{ChecklistOnboarding, ChecklistOffboarding, ChecklistTransfer}
	OwnerRoles     = []string{RoleManager, RoleWorker, RoleHRPartner, RoleIT}
)

// TaskDueOn is BR-42.12: the action's effective date plus the task's offset in
// days. A negative offset is a pre-boarding task.
func TaskDueOn(effective time.Time, offsetDays int) time.Time {
	return effective.AddDate(0, 0, offsetDays)
}
