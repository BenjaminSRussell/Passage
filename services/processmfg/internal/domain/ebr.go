package domain

// Codes of the batch record review (PRD 4.24.18).
const (
	CodeEBRExceptions  = "PMF-0014"
	CodeEBRNotApproved = "PMF-0015"
	// CodeEBRSegregation refuses an approval by someone who reviewed the same
	// record or created its batch order (PRD 4.24.17 segregation of duties). It
	// is a service code; the PRD catalog has none for this rule.
	CodeEBRSegregation = "PMF-0124"
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

// ValidExceptionDisposition reports whether s is a disposition an exception can
// be given (BR-24.15).
func ValidExceptionDisposition(s string) bool { return exceptionDispositions[s] }

// ValidSeverity reports whether s is an exception severity.
func ValidSeverity(s string) bool {
	switch s {
	case "minor", "major", "critical":
		return true
	}
	return false
}

// ExceptionDescription is the text of the exception raised for a recorded value
// outside its limits (BR-24.15): the attribute and the limit check's detail,
// cut to the 500 characters an exception holds.
func ExceptionDescription(attribute, detail string) string {
	s := attribute + " outside its limits"
	if detail != "" {
		s += ": " + detail
	}
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500])
	}
	return s
}

// CheckApproverIndependent implements the segregation of duties of PRD 4.24.17:
// *Approve batch records* conflicts with *Review batch records* on the same
// record and with *Maintain batch orders* on the same order. The approver must
// therefore be named and must not be any of the reviewers (who raised or decided
// an exception of the record) nor the creator of its batch order (PMF-0124).
func CheckApproverIndependent(approver string, reviewers []string, orderCreator string) error {
	if approver == "" {
		return errorf(CodeEBRSegregation, "the approver must be identified to approve a batch record")
	}
	for _, r := range reviewers {
		if r == approver {
			return errorf(CodeEBRSegregation, "%s reviewed this batch record and cannot also approve it; another QA approver approves", approver)
		}
	}
	if orderCreator != "" && orderCreator == approver {
		return errorf(CodeEBRSegregation, "%s created the batch order of this record and cannot approve its batch record; another QA approver approves", approver)
	}
	return nil
}
