package domain

// Codes of the batch record review (PRD 4.24.18).
const (
	CodeEBRExceptions  = "PMF-0014"
	CodeEBRNotApproved = "PMF-0015"
)

// Exception dispositions (BR-24.15).
var exceptionDispositions = map[string]bool{"accepted": true, "deviation-raised": true, "rejected": true}

// Exception is one EBR review exception. A disposition needs a signature before
// the record can be approved (invariant of 4.24.4).
type Exception struct {
	ID          string
	Disposition string
	SignatureID string
}

// CheckApprove implements BR-24.15: QA approval is refused while any exception has
// no disposition or no signature (PMF-0014). Unknown dispositions are invalid.
func CheckApprove(exceptions []Exception) error {
	open := 0
	for _, e := range exceptions {
		if e.Disposition != "" && !exceptionDispositions[e.Disposition] {
			return errorf(CodeInvalid, "exception %s has unknown disposition %q", e.ID, e.Disposition)
		}
		if e.Disposition == "" || e.SignatureID == "" {
			open++
		}
	}
	if open > 0 {
		return errorf(CodeEBRExceptions, "batch record cannot be approved: %d exceptions without disposition and signature", open)
	}
	return nil
}

// CheckRelease implements the profile gate (PMF-0015): when the profile requires
// EBR approval for release, a disposition of available needs an approved record.
func CheckRelease(approvalRequired, approved bool, batch string) error {
	if approvalRequired && !approved {
		return errorf(CodeEBRNotApproved, "batch %s cannot be released: batch record not approved", batch)
	}
	return nil
}
