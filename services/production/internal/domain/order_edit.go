package domain

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/shopspring/decimal"
)

// EditOp is the kind of an order-specific BOM or route edit (FR-21.18).
type EditOp string

const (
	EditAdd    EditOp = "add"
	EditRemove EditOp = "remove"
	EditChange EditOp = "change"
)

// Valid reports whether o is add, remove or change.
func (o EditOp) Valid() bool { return o == EditAdd || o == EditRemove || o == EditChange }

// EditTarget is what an edit acts on: a BOM line or a route operation.
type EditTarget string

const (
	TargetLine      EditTarget = "line"
	TargetOperation EditTarget = "operation"
)

// Stable error codes of the order-specific BOM and route edits (FR-21.18).
const (
	// CodeEditNotAllowed: the order's status does not allow BOM or route edits (Started or later).
	CodeEditNotAllowed = "PRODUCTION_ORDER_EDIT_NOT_ALLOWED"
	// CodeEditTargetMissing: the line or operation to change or remove is not on the order.
	CodeEditTargetMissing = "PRODUCTION_ORDER_EDIT_TARGET_NOT_FOUND"
	// CodeEditTargetExists: the line or operation to add is already on the order.
	CodeEditTargetExists = "PRODUCTION_ORDER_EDIT_TARGET_EXISTS"
	// CodeEditTargetInUse: journals or feedback reference the line or operation, so it
	// cannot be removed or re-pointed to another item or resource.
	CodeEditTargetInUse = "PRODUCTION_ORDER_EDIT_TARGET_IN_USE"
)

// LineEdit is one edit of a BOM line. add reads every field (ItemID and QtyPer are
// required; series 1, scrap 0, unit cost 0, manual flushing by default); change sets
// the fields it carries and keeps the others; remove reads LineNo only.
type LineEdit struct {
	Op         EditOp           `json:"op"`
	LineNo     string           `json:"lineNo"`
	ItemID     *string          `json:"itemId,omitempty"`
	QtyPer     *decimal.Decimal `json:"qtyPer,omitempty"`
	SeriesSize *decimal.Decimal `json:"seriesSize,omitempty"`
	ScrapPct   *decimal.Decimal `json:"scrapPct,omitempty"`
	RoundUp    *bool            `json:"roundUp,omitempty"`
	UnitCost   *decimal.Decimal `json:"unitCost,omitempty"`
	Flushing   *Flushing        `json:"flushing,omitempty"`
}

// OperationEdit is one edit of a route operation. add reads every field
// (ResourceID is required, the others default to 0 and not subcontracted);
// change sets the fields it carries; remove reads OperNumber only.
type OperationEdit struct {
	Op               EditOp           `json:"op"`
	OperNumber       int              `json:"operNumber"`
	ResourceID       *string          `json:"resourceId,omitempty"`
	SetupH           *decimal.Decimal `json:"setupTimeH,omitempty"`
	RunHPer          *decimal.Decimal `json:"runTimeHPer,omitempty"`
	ScrapPct         *decimal.Decimal `json:"scrapPct,omitempty"`
	LaborRate        *decimal.Decimal `json:"laborRate,omitempty"`
	OverheadRate     *decimal.Decimal `json:"overheadRate,omitempty"`
	Subcontract      *bool            `json:"subcontract,omitempty"`
	SubcontractPrice *decimal.Decimal `json:"subcontractPrice,omitempty"`
}

// OrderEdits is one request of edits: the line edits are applied first, in order,
// then the operation edits.
type OrderEdits struct {
	Lines      []LineEdit      `json:"lines"`
	Operations []OperationEdit `json:"operations"`
}

// Count is the number of edits in the request.
func (e OrderEdits) Count() int { return len(e.Lines) + len(e.Operations) }

// InUse names the lines and operations that journals reference (posted, reversed
// or draft). Consumption and route feedback are read from the lines and operations.
type InUse struct {
	Lines      map[string]bool
	Operations map[int]bool
}

// EditAudit is the audit diff of one applied edit: the line or operation before
// and after it (nil before an add and after a remove).
type EditAudit struct {
	Target     EditTarget
	Op         EditOp
	Key        string
	LineBefore *OrderLine
	LineAfter  *OrderLine
	OperBefore *Operation
	OperAfter  *Operation
}

// EditResult is the order's lines and operations after the edits, the operations
// re-planned for the order quantity, and one audit row per edit.
type EditResult struct {
	Lines      []OrderLine
	Operations []Operation
	Audit      []EditAudit
}

// CheckOrderEditable applies the FR-21.18 status rule: the BOM and route can be
// edited after explosion and before Started. A Released order also needs the
// duty "Maintain order BOM after release", which the caller checks.
func CheckOrderEditable(s Status) error {
	switch s {
	case Created, Estimated, Scheduled, Released:
		return nil
	}
	return &Error{Code: CodeEditNotAllowed, Detail: fmt.Sprintf("the BOM and route of a %s order cannot be edited; edits are allowed until the order is started", s)}
}

// ApplyOrderEdits applies the edits to copies of lines and ops for an order of
// orderQty units (FR-21.18). Required quantities follow BR-21.2 and process
// quantities BR-21.8. A line or operation that journals reference, that has
// consumption or route feedback, is never removed or moved to another item or
// resource, and a requirement never drops below what was consumed. The order
// keeps at least one operation. The inputs are not modified.
func ApplyOrderEdits(lines []OrderLine, ops []Operation, orderQty decimal.Decimal, edits OrderEdits, inUse InUse) (EditResult, error) {
	if edits.Count() == 0 {
		return EditResult{}, invalidEdit("at least one line or operation edit is required")
	}
	outLines := append([]OrderLine(nil), lines...)
	outOps := append([]Operation(nil), ops...)
	audit := make([]EditAudit, 0, edits.Count())
	for _, e := range edits.Lines {
		a, err := applyLineEdit(&outLines, orderQty, e, inUse.Lines)
		if err != nil {
			return EditResult{}, err
		}
		audit = append(audit, a)
	}
	for _, e := range edits.Operations {
		a, err := applyOperationEdit(&outOps, e, inUse.Operations)
		if err != nil {
			return EditResult{}, err
		}
		audit = append(audit, a)
	}
	if len(outOps) == 0 {
		return EditResult{}, &Error{Code: CodeInvalidInput, Detail: "an order keeps at least one operation"}
	}
	sort.SliceStable(outOps, func(i, j int) bool { return outOps[i].OperNumber < outOps[j].OperNumber })
	planned, err := PlanOperations(outOps, orderQty)
	if err != nil {
		return EditResult{}, err
	}
	// The audit's after-image of an operation carries its re-planned process quantity.
	for i := range audit {
		if audit[i].OperAfter != nil {
			if at := indexOfOperation(planned, audit[i].OperAfter.OperNumber); at >= 0 {
				op := planned[at]
				audit[i].OperAfter = &op
			}
		}
	}
	return EditResult{Lines: outLines, Operations: planned, Audit: audit}, nil
}

func applyLineEdit(lines *[]OrderLine, orderQty decimal.Decimal, e LineEdit, journaled map[string]bool) (EditAudit, error) {
	a := EditAudit{Target: TargetLine, Op: e.Op, Key: e.LineNo}
	if e.LineNo == "" {
		return a, invalidEdit("lineNo is required")
	}
	at := indexOfLine(*lines, e.LineNo)
	switch e.Op {
	case EditAdd:
		if at >= 0 {
			return a, &Error{Code: CodeEditTargetExists, Detail: fmt.Sprintf("line %s is already on the order", e.LineNo)}
		}
		if e.ItemID == nil || *e.ItemID == "" || e.QtyPer == nil {
			return a, invalidEdit(fmt.Sprintf("line %s: itemId and qtyPer are required to add a line", e.LineNo))
		}
		l := OrderLine{LineNo: e.LineNo, ItemID: *e.ItemID, QtyPer: *e.QtyPer, SeriesSize: one, ScrapPct: decimal.Zero,
			UnitCost: decimal.Zero, Flushing: FlushManual, QtyConsumed: decimal.Zero}
		setLineFields(&l, e)
		if err := planLine(&l, orderQty); err != nil {
			return a, err
		}
		*lines = append(*lines, l)
		a.LineAfter = &l
	case EditRemove:
		if at < 0 {
			return a, missingLine(e.LineNo)
		}
		before := (*lines)[at]
		if lineInUse(before, journaled) {
			return a, &Error{Code: CodeEditTargetInUse, Detail: fmt.Sprintf("line %s has consumption or journals and cannot be removed", e.LineNo)}
		}
		*lines = append((*lines)[:at:at], (*lines)[at+1:]...)
		a.LineBefore = &before
	case EditChange:
		if at < 0 {
			return a, missingLine(e.LineNo)
		}
		if e.ItemID == nil && e.QtyPer == nil && e.SeriesSize == nil && e.ScrapPct == nil && e.RoundUp == nil && e.UnitCost == nil && e.Flushing == nil {
			return a, invalidEdit(fmt.Sprintf("line %s: a change sets at least one field", e.LineNo))
		}
		before := (*lines)[at]
		l := before
		if e.ItemID != nil && *e.ItemID != before.ItemID && lineInUse(before, journaled) {
			return a, &Error{Code: CodeEditTargetInUse, Detail: fmt.Sprintf("line %s has consumption or journals; add a new line for another item", e.LineNo)}
		}
		setLineFields(&l, e)
		if err := planLine(&l, orderQty); err != nil {
			return a, err
		}
		if l.QtyRequired.LessThan(l.QtyConsumed) {
			return a, &Error{Code: CodeEditTargetInUse, Detail: fmt.Sprintf("line %s: the requirement %s would be below the consumed %s", e.LineNo, l.QtyRequired, l.QtyConsumed)}
		}
		(*lines)[at] = l
		a.LineBefore, a.LineAfter = &before, &l
	default:
		return a, invalidEdit(fmt.Sprintf("line %s: op must be add, remove or change", e.LineNo))
	}
	return a, nil
}

func setLineFields(l *OrderLine, e LineEdit) {
	if e.ItemID != nil {
		l.ItemID = *e.ItemID
	}
	if e.QtyPer != nil {
		l.QtyPer = *e.QtyPer
	}
	if e.SeriesSize != nil {
		l.SeriesSize = *e.SeriesSize
	}
	if e.ScrapPct != nil {
		l.ScrapPct = *e.ScrapPct
	}
	if e.RoundUp != nil {
		l.RoundUp = *e.RoundUp
	}
	if e.UnitCost != nil {
		l.UnitCost = *e.UnitCost
	}
	if e.Flushing != nil {
		l.Flushing = *e.Flushing
	}
}

// planLine checks a line's values and recomputes its requirement (BR-21.2).
func planLine(l *OrderLine, orderQty decimal.Decimal) error {
	switch {
	case l.ItemID == "":
		return invalidEdit(fmt.Sprintf("line %s: itemId is required", l.LineNo))
	case l.QtyPer.IsNegative() || l.UnitCost.IsNegative():
		return invalidEdit(fmt.Sprintf("line %s: qtyPer and unitCost must not be negative", l.LineNo))
	case !l.SeriesSize.IsPositive():
		return invalidEdit(fmt.Sprintf("line %s: seriesSize must be positive", l.LineNo))
	case l.ScrapPct.IsNegative() || !l.ScrapPct.LessThan(hundred):
		return invalidEdit(fmt.Sprintf("line %s: scrap must be from 0 up to but excluding 100", l.LineNo))
	case !l.Flushing.Valid():
		return invalidEdit(fmt.Sprintf("line %s: flushing must be manual, start, finish or available-at-location", l.LineNo))
	}
	req, err := RequiredQty(l.QtyPer, orderQty, l.SeriesSize, l.ScrapPct, l.RoundUp)
	if err != nil {
		return err
	}
	l.QtyRequired = req
	return nil
}

func applyOperationEdit(ops *[]Operation, e OperationEdit, journaled map[int]bool) (EditAudit, error) {
	a := EditAudit{Target: TargetOperation, Op: e.Op, Key: strconv.Itoa(e.OperNumber)}
	if e.OperNumber < 1 {
		return a, invalidEdit("operNumber must be a positive whole number")
	}
	at := indexOfOperation(*ops, e.OperNumber)
	switch e.Op {
	case EditAdd:
		if at >= 0 {
			return a, &Error{Code: CodeEditTargetExists, Detail: fmt.Sprintf("operation %d is already on the order", e.OperNumber)}
		}
		if e.ResourceID == nil || *e.ResourceID == "" {
			return a, invalidEdit(fmt.Sprintf("operation %d: resourceId is required to add an operation", e.OperNumber))
		}
		op := Operation{OperNumber: e.OperNumber, Status: OperOpen}
		setOperationFields(&op, e)
		if err := checkOperation(op); err != nil {
			return a, err
		}
		*ops = append(*ops, op)
		a.OperAfter = &op
	case EditRemove:
		if at < 0 {
			return a, missingOperation(e.OperNumber)
		}
		before := (*ops)[at]
		if operationInUse(before, journaled) {
			return a, &Error{Code: CodeEditTargetInUse, Detail: fmt.Sprintf("operation %d has route feedback or journals and cannot be removed", e.OperNumber)}
		}
		*ops = append((*ops)[:at:at], (*ops)[at+1:]...)
		a.OperBefore = &before
	case EditChange:
		if at < 0 {
			return a, missingOperation(e.OperNumber)
		}
		if e.ResourceID == nil && e.SetupH == nil && e.RunHPer == nil && e.ScrapPct == nil && e.LaborRate == nil &&
			e.OverheadRate == nil && e.Subcontract == nil && e.SubcontractPrice == nil {
			return a, invalidEdit(fmt.Sprintf("operation %d: a change sets at least one field", e.OperNumber))
		}
		before := (*ops)[at]
		if e.ResourceID != nil && *e.ResourceID != before.ResourceID && operationInUse(before, journaled) {
			return a, &Error{Code: CodeEditTargetInUse, Detail: fmt.Sprintf("operation %d has route feedback or journals; add a new operation for another resource", e.OperNumber)}
		}
		op := before
		setOperationFields(&op, e)
		if err := checkOperation(op); err != nil {
			return a, err
		}
		(*ops)[at] = op
		a.OperBefore, a.OperAfter = &before, &op
	default:
		return a, invalidEdit(fmt.Sprintf("operation %d: op must be add, remove or change", e.OperNumber))
	}
	return a, nil
}

func setOperationFields(op *Operation, e OperationEdit) {
	if e.ResourceID != nil {
		op.ResourceID = *e.ResourceID
	}
	for _, f := range []struct {
		from *decimal.Decimal
		to   *decimal.Decimal
	}{{e.SetupH, &op.SetupH}, {e.RunHPer, &op.RunHPer}, {e.ScrapPct, &op.ScrapPct}, {e.LaborRate, &op.LaborRate},
		{e.OverheadRate, &op.OverheadRate}, {e.SubcontractPrice, &op.SubcontractPrice}} {
		if f.from != nil {
			*f.to = *f.from
		}
	}
	if e.Subcontract != nil {
		op.Subcontract = *e.Subcontract
	}
}

func checkOperation(op Operation) error {
	if op.ResourceID == "" {
		return invalidEdit(fmt.Sprintf("operation %d: resourceId is required", op.OperNumber))
	}
	for _, v := range []decimal.Decimal{op.SetupH, op.RunHPer, op.ScrapPct, op.LaborRate, op.OverheadRate, op.SubcontractPrice} {
		if v.IsNegative() {
			return invalidEdit(fmt.Sprintf("operation %d: times, scrap, rates and price must not be negative", op.OperNumber))
		}
	}
	if !op.ScrapPct.LessThan(hundred) {
		return invalidEdit(fmt.Sprintf("operation %d: scrap must be from 0 up to but excluding 100", op.OperNumber))
	}
	return nil
}

func lineInUse(l OrderLine, journaled map[string]bool) bool {
	return !l.QtyConsumed.IsZero() || journaled[l.LineNo]
}

func operationInUse(op Operation, journaled map[int]bool) bool {
	return journaled[op.OperNumber] || !op.HoursReported.IsZero() || !op.QtyGood.IsZero() || !op.QtyError.IsZero() ||
		(op.Status != "" && op.Status != OperOpen)
}

func indexOfLine(lines []OrderLine, lineNo string) int {
	for i, l := range lines {
		if l.LineNo == lineNo {
			return i
		}
	}
	return -1
}

func indexOfOperation(ops []Operation, n int) int {
	for i, op := range ops {
		if op.OperNumber == n {
			return i
		}
	}
	return -1
}

func invalidEdit(detail string) error { return &Error{Code: CodeInvalidInput, Detail: detail} }

func missingLine(lineNo string) error {
	return &Error{Code: CodeEditTargetMissing, Detail: fmt.Sprintf("line %s is not on the order", lineNo)}
}

func missingOperation(n int) error {
	return &Error{Code: CodeEditTargetMissing, Detail: fmt.Sprintf("operation %d is not on the order", n)}
}
