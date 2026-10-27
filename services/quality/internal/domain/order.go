package domain

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Quality order statuses (BR-33.4).
const (
	StatusCreated    = "CREATED"
	StatusOpen       = "OPEN"
	StatusInProgress = "RESULTS_IN_PROGRESS"
	StatusPending    = "PENDING_DECISION"
	StatusClosed     = "CLOSED"
	StatusCancelled  = "CANCELLED"
)

var transitions = map[string][]string{
	StatusCreated:    {StatusOpen, StatusCancelled},
	StatusOpen:       {StatusInProgress, StatusCancelled},
	StatusInProgress: {StatusPending},
	StatusPending:    {StatusInProgress, StatusClosed},
}

// CanTransition reports whether BR-33.4 allows from -> to. CLOSED and CANCELLED are final.
func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }

// SourceRef is the polymorphic source line that triggered an order.
type SourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Line string `json:"line,omitempty"`
}

// Order is a quality order. Its hold in the inventory ledger is a handoff, so
// BlockRef stays empty in this slice.
type Order struct {
	ID               string            `json:"id"`
	Trigger          string            `json:"trigger"`
	SourceRef        SourceRef         `json:"sourceRef"`
	ItemID           string            `json:"itemId"`
	SiteID           string            `json:"siteId"`
	WarehouseID      string            `json:"warehouseId"`
	Dims             map[string]string `json:"dims"`
	LotQty           decimal.Decimal   `json:"lotQty"`
	SampleQty        decimal.Decimal   `json:"sampleQty"`
	UOM              string            `json:"uom"`
	TestGroupID      string            `json:"testGroupId"`
	TestGroupVersion int               `json:"testGroupVersion"`
	SamplingMethod   Method            `json:"samplingMethod"`
	Severity         Severity          `json:"severity"`
	CodeLetter       string            `json:"codeLetter"`
	AcceptNo         int               `json:"acceptNo"`
	RejectNo         int               `json:"rejectNo"`
	Status           string            `json:"status"`
	Version          int               `json:"version"`
	CreatedAt        time.Time         `json:"createdAt"`
	ClosedAt         *time.Time        `json:"closedAt,omitempty"`
}

// AfterResults returns the status once a batch of results is recorded. complete
// means every required test has a result for every sample. A pending order takes
// only retests (BR-33.4: PENDING_DECISION -> RESULTS_IN_PROGRESS on a retest
// request), and returns to PENDING_DECISION when the retest batch leaves it complete.
// A closed order is amended only through a nonconformance (QM-0109).
func AfterResults(status string, complete, retestOnly bool) (string, error) {
	switch status {
	case StatusCreated, StatusOpen, StatusInProgress:
	case StatusPending:
		if !retestOnly {
			return "", fail(CodeStatus, "the order is waiting for its usage decision; only retests (retestNo above 0) can be recorded")
		}
	case StatusClosed:
		return "", fail(CodeClosed, "Order is closed; amend through a nonconformance.")
	default:
		return "", fail(CodeStatus, "results cannot be recorded while the order is "+status)
	}
	if complete {
		return StatusPending, nil
	}
	return StatusInProgress, nil
}

// CheckRetests returns field errors for batch results whose retest number does
// not follow an earlier result for the same test and sample: retest n needs
// retest n-1 among the existing results or earlier in the batch.
func CheckRetests(existing, batch []Result) map[string]string {
	type key struct {
		test           string
		sample, retest int
	}
	seen := map[key]bool{}
	for _, r := range existing {
		seen[key{r.TestID, r.SampleNo, r.RetestNo}] = true
	}
	fields := map[string]string{}
	for i, r := range batch {
		if r.RetestNo > 0 && !seen[key{r.TestID, r.SampleNo, r.RetestNo - 1}] {
			fields["results["+strconv.Itoa(i)+"].retestNo"] = "retest " + strconv.Itoa(r.RetestNo) + " needs result " + strconv.Itoa(r.RetestNo-1) + " for the same test and sample first"
		}
		seen[key{r.TestID, r.SampleNo, r.RetestNo}] = true
	}
	return fields
}

// MaxReasonLength bounds a decision line's reason.
const MaxReasonLength = 500

// DecisionLine is one quantity of a usage decision split (FR-33.6).
type DecisionLine struct {
	Decision string          `json:"decision"`
	Quantity decimal.Decimal `json:"quantity"`
	Reason   string          `json:"reason,omitempty"`
}

// Decision values.
const (
	DecisionAccept              = "ACCEPT"
	DecisionReject              = "REJECT"
	DecisionAcceptWithDeviation = "ACCEPT_WITH_DEVIATION"
	DecisionRework              = "REWORK"
	DecisionReturnToVendor      = "RETURN_TO_VENDOR"
	DecisionScrap               = "SCRAP"
)

// Split is the accept / reject / deviation quantities of a decision.
type Split struct {
	Accept    decimal.Decimal `json:"acceptQty"`
	Reject    decimal.Decimal `json:"rejectQty"`
	Deviation decimal.Decimal `json:"deviationQty"`
}

// Decision is a posted usage decision.
type Decision struct {
	ID        string         `json:"id"`
	OrderID   string         `json:"orderId"`
	Lines     []DecisionLine `json:"lines"`
	Split     Split          `json:"split"`
	DecidedBy string         `json:"decidedBy"`
	DecidedAt time.Time      `json:"decidedAt"`
	// NonconformanceID is the NCR raised for the rejected quantity (FR-33.6).
	NonconformanceID string `json:"nonconformanceId,omitempty"`
}

// SplitDecision checks a decision's lines and sums them into buckets. Accept
// goes to accept, ACCEPT_WITH_DEVIATION to deviation, the rest to reject. The
// total must equal the lot quantity (QM-0104 otherwise).
func SplitDecision(lines []DecisionLine, lot decimal.Decimal) (Split, error) {
	if len(lines) == 0 {
		return Split{}, invalid("at least one decision line is required")
	}
	var s Split
	s.Accept, s.Reject, s.Deviation = decimal.Zero, decimal.Zero, decimal.Zero
	total := decimal.Zero
	for _, l := range lines {
		if !validDecision(l.Decision) {
			return Split{}, invalid("unknown decision " + l.Decision)
		}
		if !l.Quantity.IsPositive() {
			return Split{}, invalid("decision quantities must be positive")
		}
		if len(l.Reason) > MaxReasonLength {
			return Split{}, invalid("a decision reason is at most 500 characters")
		}
		if l.Decision == DecisionAcceptWithDeviation && strings.TrimSpace(l.Reason) == "" {
			return Split{}, invalid("ACCEPT_WITH_DEVIATION needs a deviation reason")
		}
		total = total.Add(l.Quantity)
		switch l.Decision {
		case DecisionAccept:
			s.Accept = s.Accept.Add(l.Quantity)
		case DecisionAcceptWithDeviation:
			s.Deviation = s.Deviation.Add(l.Quantity)
		default:
			s.Reject = s.Reject.Add(l.Quantity)
		}
	}
	if !total.Equal(lot) {
		return Split{}, fail(CodeQtySplit, "Decision quantities "+total.String()+" do not equal lot quantity "+lot.String()+".")
	}
	return s, nil
}

func validDecision(d string) bool {
	switch d {
	case DecisionAccept, DecisionReject, DecisionAcceptWithDeviation, DecisionRework, DecisionReturnToVendor, DecisionScrap:
		return true
	}
	return false
}

// Evaluation is the computed pass/fail of an order from its results (FR-33.4,
// BR-33.6). Passed is meaningful only once MissingRequired is empty.
type Evaluation struct {
	DefectiveSamples int      `json:"defectiveSamples"`
	Passed           bool     `json:"passed"`
	MissingRequired  []string `json:"missingRequired"`
}

// Evaluate computes the order verdict and the required tests still lacking a result.
func Evaluate(group TestGroup, o Order, results []Result) Evaluation {
	n := SampleCount(o.SampleQty)
	defective, pass := Verdict(group, n, o.AcceptNo, results)
	return Evaluation{DefectiveSamples: defective, Passed: pass, MissingRequired: MissingRequired(group, n, results)}
}

// Verdict counts defective samples (a sample is defective when a required test
// fails on its latest result) and reports whether the lot is within Ac.
func Verdict(group TestGroup, sampleQty, acceptNo int, results []Result) (defective int, pass bool) {
	eff := effectiveResults(results)
	for s := 1; s <= sampleQty; s++ {
		for _, l := range group.Tests {
			if !l.Required {
				continue
			}
			if r, ok := eff[resultKey{l.TestID, s}]; ok && !r.Passed {
				defective++
				break
			}
		}
	}
	return defective, defective <= acceptNo
}

// MissingRequired lists the required tests that lack a result for some sample.
// A decision needs every entry gone (QM-0105).
func MissingRequired(group TestGroup, sampleQty int, results []Result) []string {
	eff := effectiveResults(results)
	missing := []string{}
	for _, l := range group.Tests {
		if !l.Required {
			continue
		}
		for s := 1; s <= sampleQty; s++ {
			if _, ok := eff[resultKey{l.TestID, s}]; !ok {
				missing = append(missing, l.TestID)
				break
			}
		}
	}
	return missing
}

type resultKey struct {
	test   string
	sample int
}

// effectiveResults keeps the latest retest per (test, sample).
func effectiveResults(results []Result) map[resultKey]Result {
	out := map[resultKey]Result{}
	for _, r := range results {
		k := resultKey{r.TestID, r.SampleNo}
		if cur, ok := out[k]; !ok || r.RetestNo > cur.RetestNo {
			out[k] = r
		}
	}
	return out
}
