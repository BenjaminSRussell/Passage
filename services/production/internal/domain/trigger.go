package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Stable error codes of trigger points (FR-21.17, BR-21.13).
const (
	// CodeTriggerCondition: the condition is not comparisons "<field> <op> <number>" joined by and / or.
	CodeTriggerCondition = "PRODUCTION_TRIGGER_CONDITION_INVALID"
	// CodeTriggerFunctionUnsupported: the function needs an engine this service does not have yet.
	CodeTriggerFunctionUnsupported = "PRODUCTION_TRIGGER_FUNCTION_UNSUPPORTED"
	// CodeTriggerFunctionFailed (PROD-0019, TRIGGER_FUNCTION_FAILED): a due trigger could not
	// perform its function. It is recorded on the firing and never blocks the journal post.
	CodeTriggerFunctionFailed = "PRODUCTION_TRIGGER_FUNCTION_FAILED"
)

// InsertGap is the distance between the reporting operation and the first operation a
// trigger inserts after it (FR-21.17: oper_number = current + 5).
const InsertGap = 5

// MaxTriggerOperations bounds the operations one trigger point inserts or routes.
const MaxTriggerOperations = 50

// MaxConditionLength bounds a condition's text.
const MaxConditionLength = 200

// ReworkOperStep numbers the route of a rework order a trigger creates: 10, 20, 30...
const ReworkOperStep = 10

// TriggerFunction is what a trigger point does when its condition holds (FR-21.17).
type TriggerFunction string

const (
	FnInsertOperations  TriggerFunction = "insert-operations"
	FnCreateReworkOrder TriggerFunction = "create-rework-order"
	FnReleaseNext       TriggerFunction = "release-next"
	FnStartWorkflow     TriggerFunction = "start-workflow"
	FnNotify            TriggerFunction = "notify"
)

// Valid reports whether f is one of the five FR-21.17 functions.
func (f TriggerFunction) Valid() bool {
	switch f {
	case FnInsertOperations, FnCreateReworkOrder, FnReleaseNext, FnStartWorkflow, FnNotify:
		return true
	}
	return false
}

// Performed reports whether this service performs f. release-next needs released jobs
// (scheduling, wave 23) and start-workflow the workflow engine (wave 4).
func (f TriggerFunction) Performed() bool {
	return f == FnInsertOperations || f == FnCreateReworkOrder || f == FnNotify
}

// RoutesOperations reports whether f carries operations: the operations to insert,
// or the route of the rework order.
func (f TriggerFunction) RoutesOperations() bool {
	return f == FnInsertOperations || f == FnCreateReworkOrder
}

// TriggerOperation is one operation a trigger inserts or routes on a rework order: the
// fields of a route operation the trigger defines. Its number is assigned when it fires.
type TriggerOperation struct {
	ResourceID   string          `json:"resourceId"`
	SetupH       decimal.Decimal `json:"setupTimeH"`
	RunHPer      decimal.Decimal `json:"runTimeHPer"`
	ScrapPct     decimal.Decimal `json:"scrapPct"`
	LaborRate    decimal.Decimal `json:"laborRate"`
	OverheadRate decimal.Decimal `json:"overheadRate"`
}

func (t TriggerOperation) operation(n int, processQty decimal.Decimal) Operation {
	return Operation{OperNumber: n, ResourceID: t.ResourceID, SetupH: t.SetupH, RunHPer: t.RunHPer, ScrapPct: t.ScrapPct,
		LaborRate: t.LaborRate, OverheadRate: t.OverheadRate, ProcessQty: processQty, Status: OperOpen}
}

// Feedback is the payload a condition is evaluated against: the hours, good quantity and
// error quantity of a card line, or of the operation's totals when it finishes.
type Feedback struct {
	Hours    decimal.Decimal `json:"hours"`
	QtyGood  decimal.Decimal `json:"qtyGood"`
	QtyError decimal.Decimal `json:"qtyError"`
}

// TriggerEvent is what evaluates a trigger point (FR-21.17): the feedback of a route card or job
// card, or the operation turning finished. The feedback of a finish is the operation's totals.
type TriggerEvent string

const (
	// EventFeedback evaluates the point on each route card or job card line of its operation.
	EventFeedback TriggerEvent = "feedback"
	// EventOperationFinished evaluates it when its operation becomes finished (the :finish
	// command, or a card that finishes it), against the hours and quantities posted on it.
	EventOperationFinished TriggerEvent = "operation-finished"
)

// Valid reports whether e is one of the two events; the empty event is feedback.
func (e TriggerEvent) Valid() bool {
	return e == "" || e == EventFeedback || e == EventOperationFinished
}

// Or returns e, or feedback when it is empty.
func (e TriggerEvent) Or() TriggerEvent {
	if e == "" {
		return EventFeedback
	}
	return e
}

// MaxConditionClauses bounds the comparisons of one condition.
const MaxConditionClauses = 5

// Comparison is one "<field> <op> <number>" of a condition.
type Comparison struct {
	Field string
	Op    string
	Value decimal.Decimal
}

// Condition is a feedback predicate: comparisons "<field> <op> <number>" joined by and / or,
// for example "qty_error > 0 and hours >= 2". and binds tighter than or, so a condition is
// an or of groups of and-ed comparisons. The fields are qty_error, qty_good, qty_total
// (good + error), error_pct (error as a percentage of the total, 0 without a total) and
// hours; the operators > >= < <= == !=.
type Condition struct {
	Groups [][]Comparison
}

var conditionFields = map[string]func(Feedback) decimal.Decimal{
	"qty_error": func(f Feedback) decimal.Decimal { return f.QtyError },
	"qty_good":  func(f Feedback) decimal.Decimal { return f.QtyGood },
	"qty_total": func(f Feedback) decimal.Decimal { return f.QtyGood.Add(f.QtyError) },
	"error_pct": func(f Feedback) decimal.Decimal {
		total := f.QtyGood.Add(f.QtyError)
		if !total.IsPositive() {
			return decimal.Zero
		}
		return f.QtyError.Mul(hundred).Div(total)
	},
	"hours": func(f Feedback) decimal.Decimal { return f.Hours },
}

// conditionNumber is the value of a comparison: a plain decimal, its length bounded by the
// condition's. Exponents ("1e-2000000000") are refused: comparing or printing such a value
// would allocate a number with billions of digits. Every stored condition is canonical
// (Condition.String never writes an exponent), so this refuses nothing already stored.
var conditionNumber = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

const conditionHint = "the condition must be '<field> <op> <number>' comparisons joined by and / or, with field qty_error, qty_good, qty_total, error_pct or hours"

// ParseCondition reads a condition such as "qty_error > 0" or "error_pct >= 5 and hours > 2".
func ParseCondition(s string) (Condition, error) {
	bad := func(detail string) (Condition, error) {
		return Condition{}, &Error{Code: CodeTriggerCondition, Detail: detail}
	}
	parts := strings.Fields(s)
	if len(s) > MaxConditionLength || len(parts) == 0 || (len(parts)+1)%4 != 0 {
		return bad(conditionHint)
	}
	var c Condition
	group := []Comparison{}
	clauses := 0
	for i := 0; i < len(parts); i += 4 {
		if conditionFields[parts[i]] == nil {
			return bad(conditionHint)
		}
		switch parts[i+1] {
		case ">", ">=", "<", "<=", "==", "!=":
		default:
			return bad("the operator must be one of > >= < <= == !=")
		}
		if !conditionNumber.MatchString(parts[i+2]) {
			return bad("the condition value must be a plain decimal number such as 2 or 0.5")
		}
		v, err := decimal.NewFromString(parts[i+2])
		if err != nil {
			return bad("the condition value must be a plain decimal number such as 2 or 0.5")
		}
		group = append(group, Comparison{Field: parts[i], Op: parts[i+1], Value: v})
		clauses++
		if i+3 >= len(parts) {
			break
		}
		switch parts[i+3] {
		case "and":
		case "or":
			c.Groups, group = append(c.Groups, group), []Comparison{}
		default:
			return bad("comparisons are joined with and or or")
		}
	}
	if clauses > MaxConditionClauses {
		return bad(fmt.Sprintf("at most %d comparisons", MaxConditionClauses))
	}
	c.Groups = append(c.Groups, group)
	return c, nil
}

// String is the condition in its canonical form.
func (c Condition) String() string {
	var out []string
	for gi, g := range c.Groups {
		if gi > 0 {
			out = append(out, "or")
		}
		for i, cmp := range g {
			if i > 0 {
				out = append(out, "and")
			}
			out = append(out, cmp.Field, cmp.Op, cmp.Value.String())
		}
	}
	return strings.Join(out, " ")
}

// Holds evaluates the condition against a feedback: some group of comparisons all hold.
func (c Condition) Holds(f Feedback) bool {
	for _, g := range c.Groups {
		all := true
		for _, cmp := range g {
			if !cmp.holds(f) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func (c Comparison) holds(f Feedback) bool {
	cmp := conditionFields[c.Field](f).Cmp(c.Value)
	switch c.Op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "==":
		return cmp == 0
	default:
		return cmp != 0
	}
}

// TriggerPoint is a rule on one operation of an order (FR-21.17, PRD 4.21.4).
type TriggerPoint struct {
	ID         string             `json:"id"`
	OrderID    string             `json:"orderId"`
	OperNumber int                `json:"operNumber"`
	Sequence   int                `json:"sequence"`
	Condition  string             `json:"condition"`
	Event      TriggerEvent       `json:"event"`
	Function   TriggerFunction    `json:"function"`
	Operations []TriggerOperation `json:"operations"`
	Repeatable bool               `json:"repeatable"`
	FiredCount int                `json:"firedCount"`
}

// CheckTriggerPoint validates a definition before it is stored: the condition parses, the
// function is one this service performs, and insert-operations and create-rework-order
// carry valid operations while notify carries none.
func CheckTriggerPoint(tp TriggerPoint) error {
	if _, err := ParseCondition(tp.Condition); err != nil {
		return err
	}
	if !tp.Event.Valid() {
		return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("unknown trigger event %q: use feedback or operation-finished", tp.Event)}
	}
	if !tp.Function.Valid() {
		return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("unknown trigger function %q", tp.Function)}
	}
	if !tp.Function.Performed() {
		return &Error{Code: CodeTriggerFunctionUnsupported, Detail: fmt.Sprintf(
			"trigger function %s is not available yet: release-next needs scheduled jobs (wave 23), start-workflow the workflow engine (wave 4)", tp.Function)}
	}
	switch {
	case tp.Function.RoutesOperations() && len(tp.Operations) == 0:
		return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("%s needs at least one operation", tp.Function)}
	case !tp.Function.RoutesOperations() && len(tp.Operations) > 0:
		return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("%s takes no operations", tp.Function)}
	case len(tp.Operations) > MaxTriggerOperations:
		return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("at most %d operations", MaxTriggerOperations)}
	}
	for i, o := range tp.Operations {
		if err := checkOperation(o.operation(i+1, decimal.Zero)); err != nil {
			return err
		}
	}
	return nil
}

// DueTriggers returns the trigger points of operation operNumber on the event whose condition
// holds for the feedback, in ascending sequence (BR-21.13). A trigger that is not repeatable and has
// fired is skipped: it fires at most once per operation (FR-21.17).
func DueTriggers(tps []TriggerPoint, operNumber int, event TriggerEvent, f Feedback) ([]TriggerPoint, error) {
	var due []TriggerPoint
	for _, tp := range tps {
		if tp.OperNumber != operNumber || tp.Event.Or() != event || (!tp.Repeatable && tp.FiredCount > 0) {
			continue
		}
		c, err := ParseCondition(tp.Condition)
		if err != nil {
			return nil, err
		}
		if c.Holds(f) {
			due = append(due, tp)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].Sequence < due[j].Sequence })
	return due, nil
}

// InsertAfter numbers the operations a trigger inserts after operation current of the
// route ops (FR-21.17): current + 5, + 6, ... When those numbers would reach the next
// operation they are renumbered into the gap right after current (current + 1, + 2, ...);
// operations already on the route keep their numbers, because journals and costs refer to
// them. Without room the function fails. Each inserted operation processes the reported
// error quantity when there is one (the units to rework), otherwise the reporting
// operation's process quantity; the route's other operations are not re-planned.
func InsertAfter(ops []Operation, current int, inserted []TriggerOperation, f Feedback) ([]Operation, error) {
	at := indexOfOperation(ops, current)
	if at < 0 {
		return nil, &Error{Code: CodeTriggerFunctionFailed, Detail: fmt.Sprintf("operation %d is not on the order", current)}
	}
	if len(inserted) == 0 {
		return nil, &Error{Code: CodeTriggerFunctionFailed, Detail: "the trigger has no operations to insert"}
	}
	next := 0
	for _, op := range ops {
		if op.OperNumber > current && (next == 0 || op.OperNumber < next) {
			next = op.OperNumber
		}
	}
	n := len(inserted)
	first := current + InsertGap
	if next != 0 && first+n-1 >= next {
		first = current + 1
		if first+n-1 >= next {
			return nil, &Error{Code: CodeTriggerFunctionFailed, Detail: fmt.Sprintf(
				"no room for %d operations between operation %d and operation %d", n, current, next)}
		}
	}
	process := ops[at].ProcessQty
	if f.QtyError.IsPositive() {
		process = f.QtyError
	}
	out := make([]Operation, n)
	for i, t := range inserted {
		out[i] = t.operation(first+i, process)
	}
	return out, nil
}

// ReworkRoute is the route of a rework order a trigger creates (FR-21.17, FR-21.20): the
// trigger's operations numbered 10, 20, ... and planned for the reported error quantity.
func ReworkRoute(ops []TriggerOperation, f Feedback) ([]Operation, error) {
	if !f.QtyError.IsPositive() {
		return nil, &Error{Code: CodeTriggerFunctionFailed, Detail: "the feedback reports no error quantity to rework"}
	}
	if len(ops) == 0 {
		return nil, &Error{Code: CodeTriggerFunctionFailed, Detail: "the trigger has no rework operations"}
	}
	route := make([]Operation, len(ops))
	for i, t := range ops {
		route[i] = t.operation((i+1)*ReworkOperStep, decimal.Zero)
	}
	return PlanOperations(route, f.QtyError)
}
