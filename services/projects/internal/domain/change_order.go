package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
)

// Catalog codes for change orders. PROJ-CO-001 and PROJ-CO-002 are §8.41.18;
// the others are service-specific and listed in the service README.
const (
	CodeChangeOrderApplied = "PROJ-CO-001"
	CodeCustomerApproval   = "PROJ-CO-002"
	// CodeChangeOrderState: the lifecycle does not allow the move.
	CodeChangeOrderState = "PROJ-CO-003"
	// CodeSelfApproval: the creator of a change order cannot approve, reject or
	// apply it (§8.41.17 SoD between projects.maintain and projects.change-order.approve).
	CodeSelfApproval = "PROJ-CO-004"
	// CodeNoBaseline: applying needs a current BASELINE plan version to copy.
	CodeNoBaseline = "PROJ-CO-005"
	// CodeApplyImpact: an impact line cannot be applied (a WBS code missing from
	// the current baseline, or a negative contract value, effort or duration).
	CodeApplyImpact = "PROJ-CO-006"
)

// DefaultCustomerApprovalRequired is the §8.41.13 default of the legal-entity
// parameter "Change order customer approval required". The parameter table is
// not built yet (NOT-IMPLEMENTED.md), so the default always applies.
const DefaultCustomerApprovalRequired = true

var (
	// decimalPattern fits numeric(20,6): up to 14 integer and 6 fraction digits.
	decimalPattern     = regexp.MustCompile(`^-?\d{1,14}(\.\d{1,6})?$`)
	effortDeltaPattern = regexp.MustCompile(`^-?\d{1,10}(\.\d{1,2})?$`)
)

// MaxChangeOrderLines caps the impact lines of one change order.
const MaxChangeOrderLines = 500

// ChangeOrderType classifies a change order (FR-41.18).
type ChangeOrderType string

const (
	COScopeChange   ChangeOrderType = "SCOPE_CHANGE"
	COClaimCustomer ChangeOrderType = "CLAIM_CUSTOMER"
	COClaimVendor   ChangeOrderType = "CLAIM_VENDOR"
	COInternal      ChangeOrderType = "INTERNAL"
)

// ChangeOrderStatus is the lifecycle DRAFT → SUBMITTED → APPROVED | REJECTED → APPLIED.
type ChangeOrderStatus string

const (
	COStatusDraft     ChangeOrderStatus = "DRAFT"
	COStatusSubmitted ChangeOrderStatus = "SUBMITTED"
	COStatusApproved  ChangeOrderStatus = "APPROVED"
	COStatusRejected  ChangeOrderStatus = "REJECTED"
	COStatusApplied   ChangeOrderStatus = "APPLIED"
)

// ChangeOrderLine is one impact line. Amounts are decimal strings in the
// project currency and may be negative (a saving or a descoped item).
// WBSNodeID names a node of one of the project's plan versions; the store
// resolves its WBSCode, which is how applying finds the node in the baseline.
type ChangeOrderLine struct {
	LineNo            int     `json:"lineNo"`
	WBSNodeID         *string `json:"wbsNodeId,omitempty"`
	WBSCode           *string `json:"wbsCode,omitempty"`
	CostImpact        string  `json:"costImpact"`
	RevenueImpact     string  `json:"revenueImpact"`
	EffortHoursDelta  string  `json:"effortHoursDelta"`
	ScheduleDaysDelta int     `json:"scheduleDaysDelta"`
}

// ChangeOrder is a scope change or claim against a project.
type ChangeOrder struct {
	ID                       string            `json:"id"`
	Number                   string            `json:"number"`
	ProjectID                string            `json:"projectId"`
	Type                     ChangeOrderType   `json:"type"`
	Title                    string            `json:"title"`
	Description              string            `json:"description"`
	Status                   ChangeOrderStatus `json:"status"`
	CustomerApprovalRef      *string           `json:"customerApprovalRef,omitempty"`
	VendorPartyID            *string           `json:"vendorPartyId,omitempty"`
	CreatedBy                string            `json:"createdBy"`
	DecidedBy                *string           `json:"decidedBy,omitempty"`
	AppliedBy                *string           `json:"appliedBy,omitempty"`
	AppliedBaselineVersionID *string           `json:"appliedBaselineVersionId,omitempty"`
	AppliedAt                *time.Time        `json:"appliedAt,omitempty"`
	CostImpact               string            `json:"costImpact"`
	RevenueImpact            string            `json:"revenueImpact"`
	Lines                    []ChangeOrderLine `json:"lines"`
	Version                  int               `json:"version"`
	CreatedAt                time.Time         `json:"createdAt"`
	UpdatedAt                time.Time         `json:"updatedAt"`
}

// ValidChangeOrderType reports whether t is one of the four PRD types.
func ValidChangeOrderType(t ChangeOrderType) bool {
	switch t {
	case COScopeChange, COClaimCustomer, COClaimVendor, COInternal:
		return true
	}
	return false
}

// ValidateChangeOrder returns field-level errors for a new change order (empty
// when valid). Amounts are checked as decimals here; the store rounds them to
// the project currency.
func ValidateChangeOrder(co ChangeOrder) map[string]string {
	fields := map[string]string{}
	if !ValidChangeOrderType(co.Type) {
		fields["type"] = "must be SCOPE_CHANGE, CLAIM_CUSTOMER, CLAIM_VENDOR or INTERNAL"
	}
	if n := utf8.RuneCountInString(co.Title); n < 1 || n > 120 {
		fields["title"] = "must be 1 to 120 characters"
	}
	if utf8.RuneCountInString(co.Description) > 2000 {
		fields["description"] = "must be at most 2000 characters"
	}
	if co.CustomerApprovalRef != nil {
		if n := utf8.RuneCountInString(*co.CustomerApprovalRef); n < 1 || n > 200 {
			fields["customerApprovalRef"] = "must be 1 to 200 characters when given"
		}
	}
	switch {
	case co.VendorPartyID != nil && !ids.Valid(*co.VendorPartyID):
		fields["vendorPartyId"] = "must be a UUID"
	case co.Type == COClaimVendor && co.VendorPartyID == nil:
		fields["vendorPartyId"] = "is required for a vendor claim"
	case co.Type != COClaimVendor && co.VendorPartyID != nil:
		fields["vendorPartyId"] = "is only allowed on a vendor claim"
	}
	switch {
	case len(co.Lines) == 0:
		fields["lines"] = "needs at least one impact line"
	case len(co.Lines) > MaxChangeOrderLines:
		fields["lines"] = fmt.Sprintf("must have at most %d impact lines", MaxChangeOrderLines)
	}
	for i, l := range co.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if l.WBSNodeID != nil && !ids.Valid(*l.WBSNodeID) {
			fields[prefix+"wbsNodeId"] = "must be a UUID"
		}
		if !decimalPattern.MatchString(l.CostImpact) {
			fields[prefix+"costImpact"] = "must be a decimal amount such as -1250.00"
		}
		if !decimalPattern.MatchString(l.RevenueImpact) {
			fields[prefix+"revenueImpact"] = "must be a decimal amount such as 50000.00"
		}
		if !effortDeltaPattern.MatchString(l.EffortHoursDelta) {
			fields[prefix+"effortHoursDelta"] = "must be a decimal number of hours with at most 2 fraction digits"
		}
		if l.ScheduleDaysDelta < -maxDurationDays || l.ScheduleDaysDelta > maxDurationDays {
			fields[prefix+"scheduleDaysDelta"] = fmt.Sprintf("must be between -%d and %d working days", maxDurationDays, maxDurationDays)
		}
		changesPlan := l.ScheduleDaysDelta != 0 || (effortDeltaPattern.MatchString(l.EffortHoursDelta) && !zeroDecimal(l.EffortHoursDelta))
		if changesPlan && l.WBSNodeID == nil {
			fields[prefix+"wbsNodeId"] = "is required when the line changes effort or schedule"
		}
	}
	return fields
}

func zeroDecimal(s string) bool {
	d, err := decimal.NewFromString(s)
	return err == nil && d.IsZero()
}

// NormalizeChangeOrder fills defaults in place: an empty effort delta is "0".
func NormalizeChangeOrder(co *ChangeOrder) {
	for i := range co.Lines {
		if co.Lines[i].EffortHoursDelta == "" {
			co.Lines[i].EffortHoursDelta = "0"
		}
	}
}

// RoundLines rounds every line's cost and revenue impact half-even to the
// currency's minor unit (BR-41.5) and renumbers the lines from 1.
func RoundLines(lines []ChangeOrderLine, currency string) ([]ChangeOrderLine, error) {
	out := make([]ChangeOrderLine, len(lines))
	scale := money.MinorUnits(currency)
	for i, l := range lines {
		c, err := decimal.NewFromString(l.CostImpact)
		if err != nil {
			return nil, fmt.Errorf("%w: cost impact %q", ErrInvalidInput, l.CostImpact)
		}
		r, err := decimal.NewFromString(l.RevenueImpact)
		if err != nil {
			return nil, fmt.Errorf("%w: revenue impact %q", ErrInvalidInput, l.RevenueImpact)
		}
		e, err := decimal.NewFromString(l.EffortHoursDelta)
		if err != nil {
			return nil, fmt.Errorf("%w: effort delta %q", ErrInvalidInput, l.EffortHoursDelta)
		}
		l.LineNo = i + 1
		l.CostImpact = c.RoundBank(scale).StringFixed(scale)
		l.RevenueImpact = r.RoundBank(scale).StringFixed(scale)
		l.EffortHoursDelta = e.StringFixed(2)
		out[i] = l
	}
	return out, nil
}

// Totals returns Σ cost impact and Σ revenue impact of lines in the currency's
// minor unit.
func Totals(lines []ChangeOrderLine, currency string) (cost, revenue string, err error) {
	c, r, err := sums(lines)
	if err != nil {
		return "", "", err
	}
	scale := money.MinorUnits(currency)
	return c.StringFixed(scale), r.StringFixed(scale), nil
}

func sums(lines []ChangeOrderLine) (cost, revenue decimal.Decimal, err error) {
	for _, l := range lines {
		c, err := decimal.NewFromString(l.CostImpact)
		if err != nil {
			return cost, revenue, fmt.Errorf("%w: cost impact %q", ErrInvalidInput, l.CostImpact)
		}
		r, err := decimal.NewFromString(l.RevenueImpact)
		if err != nil {
			return cost, revenue, fmt.Errorf("%w: revenue impact %q", ErrInvalidInput, l.RevenueImpact)
		}
		cost, revenue = cost.Add(c), revenue.Add(r)
	}
	return cost, revenue, nil
}

// coMoves lists the allowed lifecycle moves (FR-41.18).
var coMoves = map[ChangeOrderStatus][]ChangeOrderStatus{
	COStatusDraft:     {COStatusSubmitted},
	COStatusSubmitted: {COStatusApproved, COStatusRejected},
	COStatusApproved:  {COStatusApplied},
}

// CheckChangeOrderMove returns PROJ-CO-001 for any move out of APPLIED (an
// applied change order is immutable, BR-41.13) and PROJ-CO-003 for any other
// move the lifecycle does not allow.
func CheckChangeOrderMove(co ChangeOrder, to ChangeOrderStatus) error {
	if co.Status == COStatusApplied {
		return rule(CodeChangeOrderApplied, "Change order %s is already APPLIED", co.Number)
	}
	for _, next := range coMoves[co.Status] {
		if next == to {
			return nil
		}
	}
	return rule(CodeChangeOrderState, "Change order %s cannot move from %s to %s", co.Number, co.Status, to)
}

// CheckDecision applies the rules of a move by actor: the lifecycle; the
// creator may not approve, reject or apply their own change order (§8.41.17);
// and a customer claim needs customer approval evidence before it is approved
// or applied when the parameter is on (BR-41.13, PROJ-CO-002).
func CheckDecision(co ChangeOrder, to ChangeOrderStatus, actor string, customerApprovalRequired bool) error {
	if err := CheckChangeOrderMove(co, to); err != nil {
		return err
	}
	if to == COStatusSubmitted {
		return nil
	}
	if actor == "" || actor == co.CreatedBy {
		return rule(CodeSelfApproval, "Change order %s must be decided by someone other than its creator", co.Number)
	}
	if to != COStatusRejected && co.Type == COClaimCustomer && customerApprovalRequired &&
		(co.CustomerApprovalRef == nil || *co.CustomerApprovalRef == "") {
		return rule(CodeCustomerApproval, "Customer approval reference required for a customer claim")
	}
	return nil
}

// ApplyResult is the effect of applying a change order (BR-41.13): the deltas,
// the new contract value and the WBS of the new BASELINE.
type ApplyResult struct {
	ContractValueDelta string    `json:"contractValueDelta"`
	BudgetDelta        string    `json:"budgetDelta"`
	ContractValue      string    `json:"contractValue"`
	EffortHoursDelta   string    `json:"effortHoursDelta"`
	ScheduleDaysDelta  int       `json:"scheduleDaysDelta"`
	Nodes              []WBSNode `json:"-"`
}

// ApplyChangeOrder computes what applying co does (BR-41.13): contract value +=
// Σ revenue impact, budget delta = Σ cost impact, and the nodes of the new
// baseline are the current baseline's nodes with each line's effort and
// duration delta added to the node with the line's WBS code. baseline is not
// modified. It refuses a change order that is not APPROVED (PROJ-CO-001 when
// APPLIED, PROJ-CO-003 otherwise) and a line it cannot apply (PROJ-CO-006).
// The caller reschedules the returned nodes.
func ApplyChangeOrder(co ChangeOrder, contractValue, currency string, baseline []WBSNode) (ApplyResult, error) {
	if err := CheckChangeOrderMove(co, COStatusApplied); err != nil {
		return ApplyResult{}, err
	}
	contract, err := decimal.NewFromString(contractValue)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("%w: contract value %q", ErrInvalidInput, contractValue)
	}
	cost, revenue, err := sums(co.Lines)
	if err != nil {
		return ApplyResult{}, err
	}
	scale := money.MinorUnits(currency)
	newContract := contract.Add(revenue).RoundBank(scale)
	if newContract.IsNegative() {
		return ApplyResult{}, rule(CodeApplyImpact, "Change order %s would make the contract value negative (%s)", co.Number, newContract.StringFixed(scale))
	}
	nodes := make([]WBSNode, len(baseline))
	copy(nodes, baseline)
	at := make(map[string]int, len(nodes))
	summary := make(map[string]bool, len(nodes))
	for i, n := range nodes {
		at[n.WBSCode] = i
		if n.ParentID != nil {
			summary[*n.ParentID] = true
		}
	}
	effort := decimal.Zero
	schedule := 0
	for _, l := range co.Lines {
		e, err := decimal.NewFromString(l.EffortHoursDelta)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("%w: effort delta %q", ErrInvalidInput, l.EffortHoursDelta)
		}
		effort = effort.Add(e)
		schedule += l.ScheduleDaysDelta
		if l.WBSCode == nil {
			continue
		}
		i, ok := at[*l.WBSCode]
		if !ok {
			return ApplyResult{}, rule(CodeApplyImpact, "WBS %s of line %d is not in the current baseline", *l.WBSCode, l.LineNo)
		}
		n := nodes[i]
		if !e.IsZero() {
			cur := decimal.Zero
			if n.EffortHours != nil {
				if cur, err = decimal.NewFromString(*n.EffortHours); err != nil {
					return ApplyResult{}, fmt.Errorf("%w: effort %q", ErrInvalidInput, *n.EffortHours)
				}
			}
			next := cur.Add(e)
			if next.IsNegative() {
				return ApplyResult{}, rule(CodeApplyImpact, "Line %d would make the effort of WBS %s negative", l.LineNo, n.WBSCode)
			}
			s := next.StringFixed(2)
			n.EffortHours = &s
		}
		if l.ScheduleDaysDelta != 0 {
			if summary[n.ID] {
				return ApplyResult{}, rule(CodeApplyImpact, "Line %d changes the duration of summary WBS %s; its dates roll up from its tasks", l.LineNo, n.WBSCode)
			}
			d := n.DurationDays + l.ScheduleDaysDelta
			if d < 0 || d > maxDurationDays {
				return ApplyResult{}, rule(CodeApplyImpact, "Line %d would make the duration of WBS %s %s working days", l.LineNo, n.WBSCode, strconv.Itoa(d))
			}
			n.DurationDays = d
		}
		nodes[i] = n
	}
	return ApplyResult{
		ContractValueDelta: revenue.StringFixed(scale),
		BudgetDelta:        cost.StringFixed(scale),
		ContractValue:      newContract.StringFixed(scale),
		EffortHoursDelta:   effort.StringFixed(2),
		ScheduleDaysDelta:  schedule,
		Nodes:              nodes,
	}, nil
}
