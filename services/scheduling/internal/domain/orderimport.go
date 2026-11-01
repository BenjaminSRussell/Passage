package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Limits an imported operation must respect: the same bounds POST /operations
// enforces (1 to 100000 process minutes, setup up to one week).
const (
	MaxOperationMinutes = 100000
	MaxSetupMinutes     = 7 * 24 * 60
)

// Reasons an operation of a production order is left out of the import.
const (
	SkipNoProcessJob     = "no-process-job"
	SkipTooLong          = "too-long"
	SkipSetupTooLong     = "setup-too-long"
	SkipUnknownResource  = "unknown-resource"
	SkipBadHours         = "bad-hours"
	SkipFirmed           = "firmed"
	productionJobProcess = "process"
	productionJobSetup   = "setup"
)

// ProductionJob is one dated job of production's order.scheduled.v1. Only the
// process and setup jobs carry work for a resource; queue and transport time
// is not modelled here.
type ProductionJob struct {
	OperNumber int
	Seq        int
	JobType    string
	ResourceID string
	Hours      string
}

// ProductionOrder is the part of order.scheduled.v1 scheduling reads.
type ProductionOrder struct {
	OrderID      string
	OrderNumber  string
	Start        time.Time
	DeliveryDate *time.Time
	Jobs         []ProductionJob
}

// ImportedOperation is the demand scheduling registers for one route
// operation of a production order.
type ImportedOperation struct {
	OperNumber   int
	Minutes      int
	SetupMinutes int
	// ResourceIDs are the resources of the operation's process jobs in order
	// of first appearance; the first is the preferred one.
	ResourceIDs []string
	ReadyAt     *time.Time
	DueAt       *time.Time
}

// SkippedOperation is an operation that could not be imported and why.
type SkippedOperation struct {
	OperNumber int    `json:"operNumber"`
	Code       string `json:"code"`
	Detail     string `json:"detail"`
}

// DeliveryDue is the due instant of a delivery date: the end of that calendar
// day in loc, that is the next local midnight, so a job that finishes on the
// delivery day (up to midnight) is on time and a DST day keeps its true length.
func DeliveryDue(date time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(date.Year(), date.Month(), date.Day()+1, 0, 0, 0, 0, loc).UTC()
}

// PlanImport turns the dated jobs of a production order into scheduling
// operations (FR-23.10): per route operation, the process hours become the
// operation's minutes (rounded up to a whole minute, at least one), the setup
// hours its route setup minutes, and the resources of its process jobs its
// candidates. The first operation may not start before the order's baseline
// start. DueAt is left empty: the caller sets it from the delivery date with
// DeliveryDue in the time zone of the resource the order finishes on.
// Operations that cannot be expressed are returned as skipped rather than
// failing the order. Both lists are in operation order.
func PlanImport(o ProductionOrder) ([]ImportedOperation, []SkippedOperation) {
	byOper := map[int][]ProductionJob{}
	for _, j := range o.Jobs {
		byOper[j.OperNumber] = append(byOper[j.OperNumber], j)
	}
	numbers := make([]int, 0, len(byOper))
	for n := range byOper {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)

	var ops []ImportedOperation
	var skipped []SkippedOperation
	for i, n := range numbers {
		jobs := byOper[n]
		sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].Seq < jobs[b].Seq })
		op := ImportedOperation{OperNumber: n}
		if i == 0 && !o.Start.IsZero() {
			ready := o.Start.UTC().Truncate(time.Minute)
			op.ReadyAt = &ready
		}
		process, setup := decimal.Zero, decimal.Zero
		hasProcess := false
		seen := map[string]bool{}
		bad := ""
		for _, j := range jobs {
			if j.JobType != productionJobProcess && j.JobType != productionJobSetup {
				continue
			}
			h, err := decimal.NewFromString(j.Hours)
			if err != nil || h.IsNegative() {
				bad = fmt.Sprintf("planned hours %q are not a non-negative decimal", j.Hours)
				break
			}
			if j.JobType == productionJobSetup {
				setup = setup.Add(h)
				continue
			}
			hasProcess = true
			process = process.Add(h)
			if j.ResourceID != "" && !seen[j.ResourceID] {
				seen[j.ResourceID] = true
				op.ResourceIDs = append(op.ResourceIDs, j.ResourceID)
			}
		}
		switch {
		case bad != "":
			skipped = append(skipped, SkippedOperation{OperNumber: n, Code: SkipBadHours, Detail: bad})
			continue
		case !hasProcess || len(op.ResourceIDs) == 0:
			skipped = append(skipped, SkippedOperation{OperNumber: n, Code: SkipNoProcessJob, Detail: "the operation has no process job with a resource"})
			continue
		}
		op.Minutes = ceilMinutes(process)
		if op.Minutes < 1 {
			op.Minutes = 1
		}
		op.SetupMinutes = ceilMinutes(setup)
		if op.Minutes > MaxOperationMinutes {
			skipped = append(skipped, SkippedOperation{OperNumber: n, Code: SkipTooLong, Detail: fmt.Sprintf("%d process minutes exceed the limit of %d", op.Minutes, MaxOperationMinutes)})
			continue
		}
		if op.SetupMinutes > MaxSetupMinutes {
			skipped = append(skipped, SkippedOperation{OperNumber: n, Code: SkipSetupTooLong, Detail: fmt.Sprintf("%d setup minutes exceed the limit of %d", op.SetupMinutes, MaxSetupMinutes)})
			continue
		}
		ops = append(ops, op)
	}
	return ops, skipped
}

// ceilMinutes converts decimal hours to whole minutes, rounding up.
func ceilMinutes(hours decimal.Decimal) int {
	m := hours.Mul(decimal.NewFromInt(60)).Ceil()
	if m.GreaterThan(decimal.NewFromInt(1_000_000_000)) {
		return 1_000_000_000
	}
	return int(m.IntPart())
}

// OperationNumbers lists the distinct operation numbers of the order's jobs.
func (o ProductionOrder) OperationNumbers() []int {
	seen := map[int]bool{}
	var out []int
	for _, j := range o.Jobs {
		if !seen[j.OperNumber] {
			seen[j.OperNumber] = true
			out = append(out, j.OperNumber)
		}
	}
	sort.Ints(out)
	return out
}
