package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Stable error codes of trigger points (FR-21.17, BR-21.13).
const (
	// CodeTriggerCondition: the condition is not "<field> <op> <number>".
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

// Feedback is what a route card reports on one operation, the payload a condition is
// evaluated against: the hours, good quantity and error quantity of the journal line.
type Feedback struct {
	Hours    decimal.Decimal `json:"hours"`
	QtyGood  decimal.Decimal `json:"qtyGood"`
	QtyError decimal.Decimal `json:"qtyError"`
}

// Condition is a feedback predicate "<field> <op> <number>", for example "qty_error > 0".
// The fields are qty_error, qty_good and hours; the operators > >= < <= == !=.
type Condition struct {
	Field string
	Op    string
	Value decimal.Decimal
}

var conditionFields = map[string]func(Feedback) decimal.Decimal{
	"qty_error": func(f Feedback) decimal.Decimal { return f.QtyError },
	"qty_good":  func(f Feedback) decimal.Decimal { return f.QtyGood },
	"hours":     func(f Feedback) decimal.Decimal { return f.Hours },
}

// ParseCondition reads a condition such as "qty_error > 0".
func ParseCondition(s string) (Condition, error) {
	parts := strings.Fields(s)
	if len(s) > MaxConditionLength || len(parts) != 3 || conditionFields[parts[0]] == nil {
		return Condition{}, &Error{Code: CodeTriggerCondition,
			Detail: "the condition must be '<field> <op> <number>' with field qty_error, qty_good or hours"}
	}
	switch parts[1] {
	case ">", ">=", "<", "<=", "==", "!=":
	default:
		return Condition{}, &Error{Code: CodeTriggerCondition, Detail: "the operator must be one of > >= < <= == !="}
	}
	v, err := decimal.NewFromString(parts[2])
	if err != nil {
		return Condition{}, &Error{Code: CodeTriggerCondition, Detail: "the condition value must be a number"}
	}
	return Condition{Field: parts[0], Op: parts[1], Value: v}, nil
}

// String is the condition in its canonical form.
func (c Condition) String() string { return c.Field + " " + c.Op + " " + c.Value.String() }

// Holds evaluates the condition against a feedback.
func (c Condition) Holds(f Feedback) bool {
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

// DueTriggers returns the trigger points of operation operNumber whose condition holds for
// the feedback, in ascending sequence (BR-21.13). A trigger that is not repeatable and has
// fired is skipped: it fires at most once per operation (FR-21.17).
func DueTriggers(tps []TriggerPoint, operNumber int, f Feedback) ([]TriggerPoint, error) {
	var due []TriggerPoint
	for _, tp := range tps {
		if tp.OperNumber != operNumber || (!tp.Repeatable && tp.FiredCount > 0) {
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
