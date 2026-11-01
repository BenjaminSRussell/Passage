package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// JobType is the kind of time a ProductionJob covers (PRD 4.21 entities).
type JobType string

const (
	JobQueueBefore JobType = "queue-before"
	JobSetup       JobType = "setup"
	JobProcess     JobType = "process"
	JobQueueAfter  JobType = "queue-after"
	JobTransport   JobType = "transport"
)

// ValidJobType reports whether t is one of the job types.
func ValidJobType(t JobType) bool {
	switch t {
	case JobQueueBefore, JobSetup, JobProcess, JobQueueAfter, JobTransport:
		return true
	}
	return false
}

// SchedulingMethod says how finely `:schedule` cuts an operation: `operations` gives each
// operation one process job (setup and run together), `jobs` a setup job and a process job. Queue and
// transit hours are their own jobs under both.
type SchedulingMethod string

const (
	MethodOperations SchedulingMethod = "operations"
	MethodJobs       SchedulingMethod = "jobs"
)

// Valid reports whether m is a scheduling method.
func (m SchedulingMethod) Valid() bool { return m == MethodOperations || m == MethodJobs }

// Direction is the scheduling direction: forward from a start, backward from a finish.
type Direction string

const (
	Forward  Direction = "forward"
	Backward Direction = "backward"
)

// Valid reports whether d is a direction.
func (d Direction) Valid() bool { return d == Forward || d == Backward }

// CodeScheduleInput refuses a schedule request the order cannot satisfy.
const CodeScheduleInput = "PRODUCTION_SCHEDULE_INPUT"

// PlannedJob is one dated job of an operation. Seq numbers the jobs of the operation from 1.
// ID is empty from PlanSchedule; the store sets it when it writes the job.
type PlannedJob struct {
	ID         string          `json:"id,omitempty"`
	OperNumber int             `json:"operNumber"`
	Seq        int             `json:"seq"`
	Type       JobType         `json:"jobType"`
	ResourceID string          `json:"resourceId"`
	Start      time.Time       `json:"plannedStart"`
	End        time.Time       `json:"plannedEnd"`
	Hours      decimal.Decimal `json:"plannedHours"`
}

// OperationWindow is the span of one scheduled operation, queue and transit included.
type OperationWindow struct {
	OperNumber int
	Start      time.Time
	End        time.Time
}

// Schedule is the result of PlanSchedule: the order's start and finish, the window of each
// operation and the jobs, all in route order.
type Schedule struct {
	Start   time.Time
	End     time.Time
	Windows []OperationWindow
	Jobs    []PlannedJob
}

// StartDate and EndDate are the UTC calendar dates of the first start and the last finish.
func (s Schedule) StartDate() string { return s.Start.UTC().Format(time.DateOnly) }
func (s Schedule) EndDate() string   { return s.End.UTC().Format(time.DateOnly) }

// hoursToDuration converts hours to a duration at one-second resolution.
func hoursToDuration(h decimal.Decimal) time.Duration {
	return time.Duration(h.Mul(decimal.NewFromInt(3600)).Round(0).IntPart()) * time.Second
}

// offsetJob is a job positioned as offsets from the start of the plan.
type offsetJob struct {
	PlannedJob
	s, e time.Duration
}

// MaxPlanHours bounds the hours of one order's plan (about 11 years): a longer route is a
// data error, and its instants would leave the range the database stores.
var MaxPlanHours = decimal.NewFromInt(100000)

// PlanSchedule dates the route with infinite capacity (wave 21; finite capacity and calendars
// are wave 23): the operations run one after the other in route order, on a continuous clock,
// each as queue-before, setup, process (run time × process quantity), queue-after and transit.
// Forward starts the first operation at anchor; backward ends the last one at anchor. Jobs
// of zero hours are left out, except the process job, so every operation has a job. Hours are
// kept to 6 decimals and the instants to the second. An order without operations (a sales
// line order whose route is not resolved yet) takes no time: it starts and ends at anchor.
func PlanSchedule(ops []Operation, method SchedulingMethod, dir Direction, anchor time.Time) (Schedule, error) {
	if !method.Valid() || !dir.Valid() {
		return Schedule{}, &Error{Code: CodeScheduleInput, Detail: fmt.Sprintf("unknown scheduling method %q or direction %q", method, dir)}
	}
	// Plan forward from a zero offset, then shift the whole plan: backward is the same plan
	// ending at the anchor.
	var cursor time.Duration
	type span struct {
		oper       int
		start, end time.Duration
	}
	var jobs []offsetJob
	var spans []span
	total := decimal.Zero
	for _, op := range ops {
		total = total.Add(op.QueueBeforeH).Add(op.SetupH).Add(op.RunHPer.Mul(op.ProcessQty)).Add(op.QueueAfterH).Add(op.TransitH)
	}
	if total.GreaterThan(MaxPlanHours) {
		return Schedule{}, &Error{Code: CodeScheduleInput, Detail: fmt.Sprintf("the route takes %s hours; a plan is limited to %s", total.Round(2), MaxPlanHours)}
	}
	for _, op := range ops {
		opStart := cursor
		seq := 0
		add := func(t JobType, hours decimal.Decimal, always bool) {
			hours = hours.Round(QtyScale)
			d := hoursToDuration(hours)
			if d == 0 && !always {
				return
			}
			seq++
			jobs = append(jobs, offsetJob{PlannedJob{OperNumber: op.OperNumber, Seq: seq, Type: t, ResourceID: op.ResourceID, Hours: hours}, cursor, cursor + d})
			cursor += d
		}
		process := op.SetupH.Add(op.RunHPer.Mul(op.ProcessQty))
		add(JobQueueBefore, op.QueueBeforeH, false)
		if method == MethodJobs {
			add(JobSetup, op.SetupH, false)
			add(JobProcess, op.RunHPer.Mul(op.ProcessQty), true)
		} else {
			add(JobProcess, process, true)
		}
		add(JobQueueAfter, op.QueueAfterH, false)
		add(JobTransport, op.TransitH, false)
		spans = append(spans, span{op.OperNumber, opStart, cursor})
	}
	base := anchor
	if dir == Backward {
		base = anchor.Add(-cursor)
	}
	out := Schedule{Start: base, End: base.Add(cursor)}
	for _, sp := range spans {
		out.Windows = append(out.Windows, OperationWindow{OperNumber: sp.oper, Start: base.Add(sp.start), End: base.Add(sp.end)})
	}
	for _, j := range jobs {
		pj := j.PlannedJob
		pj.Start, pj.End = base.Add(j.s), base.Add(j.e)
		out.Jobs = append(out.Jobs, pj)
	}
	return out, nil
}

// ResolveAnchor picks the instant PlanSchedule anchors on. An explicit instant wins; otherwise
// a forward schedule starts now and a backward one finishes at the end of the delivery date
// (23:59:59 UTC), which a backward schedule therefore requires.
func ResolveAnchor(dir Direction, at *time.Time, deliveryDate *string, now time.Time) (time.Time, error) {
	if at != nil {
		return at.UTC(), nil
	}
	if dir == Forward {
		return now.UTC().Truncate(time.Second), nil
	}
	if deliveryDate == nil {
		return time.Time{}, &Error{Code: CodeScheduleInput, Detail: "a backward schedule needs a delivery date on the order or an explicit at"}
	}
	d, err := time.Parse(time.DateOnly, *deliveryDate)
	if err != nil {
		return time.Time{}, &Error{Code: CodeScheduleInput, Detail: "the delivery date of the order is not a date"}
	}
	return d.Add(24*time.Hour - time.Second), nil
}
