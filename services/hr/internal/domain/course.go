package domain

import "fmt"

// Course registration statuses (FR-42.14, BR-42.9).
const (
	RegRequested  = "REQUESTED"
	RegConfirmed  = "CONFIRMED"
	RegWaitlisted = "WAITLISTED"
	RegAttended   = "ATTENDED"
	RegNoShow     = "NO_SHOW"
	RegCanceled   = "CANCELED"

	ResultPass = "PASS"
	ResultFail = "FAIL"
)

// Course problem codes. HCM-CRS-001 and HCM-CRS-002 are in the 8.42.18
// catalog; the others are not, and the service README lists them.
const (
	// CodeCourseFull: the session and its waitlist are full (BR-42.9).
	CodeCourseFull = "HCM-CRS-001"
	// CodeCourseApproval is a warning: the course needs manager approval, so
	// the registration waits as REQUESTED (FR-42.14).
	CodeCourseApproval = "HCM-CRS-002"
	// CodeCourseState: the registration or session is not in a state that
	// allows the step.
	CodeCourseState = "HCM-CRS-STATE"
	// CodeCourseOutcome: an attendance line is malformed.
	CodeCourseOutcome = "HCM-CRS-OUTCOME"
	// CodeCourseSelfApproval: an approver cannot approve their own registration.
	CodeCourseSelfApproval = "HCM-CRS-SELF-APPROVAL"

	// DefaultWaitlistCapacity is the waitlist size used when a session does
	// not state one (8.42.13 "Course waitlist default size").
	DefaultWaitlistCapacity = 5
	// MaxWaitlistCapacity is the parameter's upper bound (8.42.13: 0-100).
	MaxWaitlistCapacity = 100
)

// Seat decides the status of a registration that takes a place in a session
// (BR-42.9). Seats are allocated in registration order: CONFIRMED while a seat
// is free, WAITLISTED while the waitlist has room, and ok=false (HCM-CRS-001)
// when both are full.
func Seat(confirmed, waitlisted, capacity, waitlistCap int) (status string, ok bool) {
	if confirmed < capacity {
		return RegConfirmed, true
	}
	if waitlisted < waitlistCap {
		return RegWaitlisted, true
	}
	return "", false
}

// ValidateOutcome checks one attendance line: ATTENDED with an optional PASS
// or FAIL, or NO_SHOW with no result. A score needs a result.
func ValidateOutcome(status, result string, hasScore bool) error {
	switch status {
	case RegAttended:
		if result != "" && result != ResultPass && result != ResultFail {
			return &Error{Code: CodeCourseOutcome, Detail: "the result must be PASS or FAIL"}
		}
		if hasScore && result == "" {
			return &Error{Code: CodeCourseOutcome, Detail: "a score needs a PASS or FAIL result"}
		}
		return nil
	case RegNoShow:
		if result != "" || hasScore {
			return &Error{Code: CodeCourseOutcome, Detail: "a no-show carries no result or score"}
		}
		return nil
	default:
		return &Error{Code: CodeCourseOutcome, Detail: fmt.Sprintf("the outcome must be %s or %s", RegAttended, RegNoShow)}
	}
}

// Recorded is a registration's stored outcome: its status, result and score
// ("" when absent).
type Recorded struct {
	Status string
	Result string
	Score  string
}

// CompletionStep decides whether an attendance line may be written over the
// stored registration. The same outcome again is a no-op (a retried
// completion changes nothing). A CONFIRMED registration takes any outcome; an
// ATTENDED one without a result may get its result later (attendance at the
// session end, the exam result afterwards). Anything else is HCM-CRS-STATE.
func CompletionStep(stored, next Recorded) (noop bool, err error) {
	if stored == next {
		return true, nil
	}
	switch stored.Status {
	case RegConfirmed:
		return false, nil
	case RegAttended:
		if stored.Result == "" && next.Status == RegAttended && next.Result != "" {
			return false, nil
		}
		return false, &Error{Code: CodeCourseState, Detail: "the registration already has a different outcome recorded"}
	case RegNoShow:
		return false, &Error{Code: CodeCourseState, Detail: "the registration already has a different outcome recorded"}
	default:
		return false, &Error{Code: CodeCourseState, Detail: fmt.Sprintf("only a confirmed registration can be completed; this one is %s", stored.Status)}
	}
}

// CheckCancel refuses canceling a registration whose outcome is recorded. A
// canceled registration cancels again as a no-op.
func CheckCancel(status string) (noop bool, err error) {
	switch status {
	case RegCanceled:
		return true, nil
	case RegAttended, RegNoShow:
		return false, &Error{Code: CodeCourseState, Detail: "a registration with a recorded outcome cannot be canceled"}
	default:
		return false, nil
	}
}

// AwardRating is the rating a completed course writes to a skill: the higher
// of the worker's existing rating and the course outcome, so a course never
// lowers a rating (BR-42.9). An existing rating of 0 means not rated yet.
func AwardRating(existing, awarded int) int {
	if awarded > existing {
		return awarded
	}
	return existing
}

// CheckNotSelfApproval refuses an approver approving their own registration.
func CheckNotSelfApproval(actorUserID string, workerUserID *string) error {
	if actorUserID != "" && workerUserID != nil && *workerUserID == actorUserID {
		return &Error{Code: CodeCourseSelfApproval, Detail: "an approver cannot approve their own course registration"}
	}
	return nil
}
