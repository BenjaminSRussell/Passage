package domain

// ReportStatus is the expense report lifecycle (8.43.7: DRAFT → SUBMITTED →
// APPROVED → POSTED → PAID; SUBMITTED → REJECTED | RETURNED → DRAFT). Slice 4
// creates DRAFT reports and submits them; approval, return and posting arrive
// with the wave-4 workflow and wave-18 AP integration.
type ReportStatus string

const (
	ReportDraft     ReportStatus = "DRAFT"
	ReportSubmitted ReportStatus = "SUBMITTED"
)

// CanAddLine is true while lines may change. A submitted report is frozen.
func CanAddLine(s ReportStatus) bool { return s == ReportDraft }

// CanSubmit is true for a report that has not been submitted yet.
func CanSubmit(s ReportStatus) bool { return s == ReportDraft }

// Blockers returns the results that stop a submit (8.43.5): every BLOCK and every
// unsatisfied REQUIRE_*, in evaluation order.
func Blockers(results []Result) []Result {
	var out []Result
	for _, r := range results {
		if !r.Satisfied {
			out = append(out, r)
		}
	}
	return out
}

// ForLine returns the results that belong to one line, in evaluation order.
func ForLine(results []Result, lineID string) []Result {
	var out []Result
	for _, r := range results {
		if r.LineID == lineID {
			out = append(out, r)
		}
	}
	return out
}
