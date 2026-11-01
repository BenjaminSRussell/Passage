package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Codes for finite-capacity dates (FR-23.10, BR-21.1).
const (
	// CodeFiniteInput refuses windows that are malformed or name an operation the order lacks.
	CodeFiniteInput = "PRODUCTION_FINITE_INPUT"
	// CodeFiniteState refuses finite dates on an order that is not dated, or past Started.
	CodeFiniteState = "PRODUCTION_FINITE_STATE"
	// CodeFiniteLocked refuses to move an operation that already has feedback.
	CodeFiniteLocked = "PRODUCTION_FINITE_LOCKED"
)

// Schedule bases: how the dates of a scheduled order were found.
const (
	BasisInfinite = "infinite"
	BasisFinite   = "finite"
)

// MaxFiniteWindows bounds the operations in one request.
const MaxFiniteWindows = 500

// CheckFiniteStatus allows finite dates on a dated order that has not reported finished: the
// schedule exists from Scheduled and its dates stop moving once output is reported.
func CheckFiniteStatus(s Status) error {
	switch s {
	case Scheduled, Released, Started:
		return nil
	}
	return &Error{Code: CodeFiniteState, Detail: fmt.Sprintf("finite dates apply to a Scheduled, Released or Started order, not %s", s)}
}

// FinitePlan is the schedule finite windows give an order. Compressed names the moved operations
// whose window is shorter than the planned hours of their jobs: the jobs keep those hours (the
// work content) but are dated closer together than the hours say, which a resource efficiency
// above 100% explains and anything else does not, so it is kept and shown, not refused.
type FinitePlan struct {
	Schedule
	Compressed []int
}

// ApplyFiniteWindows takes the windows finite-capacity scheduling gave some operations and
// places the order's jobs inside them. jobs are the order's current jobs in route order;
// windows are the operations to move (the others keep their jobs). Within a window the
// operation's jobs keep their order and take the window in proportion to their hours (all
// equal when the operation has no hours), to the second; the last job ends with the window,
// so queue and transit stretch with the calendar gaps scheduling found. locked names the
// operations that already have feedback, which cannot move. The windows, moved or not, must keep
// route order: an operation may overlap the one before it (BR-23.5) but neither start nor end
// before it does. It returns the jobs, the windows of every operation (moved or not, from ops),
// the order's first start and last end, and the compressed operations.
func ApplyFiniteWindows(ops []Operation, jobs []PlannedJob, windows []OperationWindow, locked map[int]bool) (FinitePlan, error) {
	if len(windows) == 0 || len(windows) > MaxFiniteWindows {
		return FinitePlan{}, &Error{Code: CodeFiniteInput, Detail: fmt.Sprintf("send between 1 and %d operation windows", MaxFiniteWindows)}
	}
	known := map[int]bool{}
	for _, op := range ops {
		known[op.OperNumber] = true
	}
	move := map[int]OperationWindow{}
	for _, w := range windows {
		switch {
		case !known[w.OperNumber]:
			return FinitePlan{}, &Error{Code: CodeFiniteInput, Detail: fmt.Sprintf("the order has no operation %d", w.OperNumber)}
		case w.End.Before(w.Start):
			return FinitePlan{}, &Error{Code: CodeFiniteInput, Detail: fmt.Sprintf("operation %d ends before it starts", w.OperNumber)}
		}
		if _, dup := move[w.OperNumber]; dup {
			return FinitePlan{}, &Error{Code: CodeFiniteInput, Detail: fmt.Sprintf("operation %d appears twice", w.OperNumber)}
		}
		move[w.OperNumber] = OperationWindow{OperNumber: w.OperNumber, Start: w.Start.UTC().Truncate(time.Second), End: w.End.UTC().Truncate(time.Second)}
	}
	var out FinitePlan
	byOp := map[int][]PlannedJob{}
	for _, j := range jobs {
		byOp[j.OperNumber] = append(byOp[j.OperNumber], j)
	}
	for _, op := range ops {
		w, moved := move[op.OperNumber]
		own := byOp[op.OperNumber]
		if !moved {
			if len(own) == 0 {
				continue
			}
			s, e := own[0].Start, own[0].End
			for _, j := range own {
				if j.Start.Before(s) {
					s = j.Start
				}
				if j.End.After(e) {
					e = j.End
				}
			}
			out.Windows = append(out.Windows, OperationWindow{OperNumber: op.OperNumber, Start: s, End: e})
			out.Jobs = append(out.Jobs, own...)
			continue
		}
		if locked[op.OperNumber] {
			return FinitePlan{}, &Error{Code: CodeFiniteLocked, Detail: fmt.Sprintf("operation %d has feedback and cannot move", op.OperNumber)}
		}
		hours := decimal.Zero
		for _, j := range own {
			hours = hours.Add(j.Hours)
		}
		if hoursToDuration(hours) > w.End.Sub(w.Start) {
			out.Compressed = append(out.Compressed, op.OperNumber)
		}
		out.Windows = append(out.Windows, w)
		out.Jobs = append(out.Jobs, spreadJobs(own, w)...)
	}
	for i := 1; i < len(out.Windows); i++ {
		prev, cur := out.Windows[i-1], out.Windows[i]
		if cur.Start.Before(prev.Start) || cur.End.Before(prev.End) {
			return FinitePlan{}, &Error{Code: CodeFiniteInput, Detail: fmt.Sprintf(
				"operation %d (%s to %s) cannot start or end before operation %d (%s to %s): the windows must follow the route",
				cur.OperNumber, cur.Start.Format(time.RFC3339), cur.End.Format(time.RFC3339),
				prev.OperNumber, prev.Start.Format(time.RFC3339), prev.End.Format(time.RFC3339))}
		}
	}
	if len(out.Windows) == 0 {
		return FinitePlan{}, &Error{Code: CodeFiniteState, Detail: "the order has no scheduled operations"}
	}
	out.Start, out.End = out.Windows[0].Start, out.Windows[0].End
	for _, w := range out.Windows {
		if w.Start.Before(out.Start) {
			out.Start = w.Start
		}
		if w.End.After(out.End) {
			out.End = w.End
		}
	}
	return out, nil
}

// spreadJobs lays jobs (in time order) across w in proportion to their hours.
func spreadJobs(jobs []PlannedJob, w OperationWindow) []PlannedJob {
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].Seq < jobs[b].Seq })
	total := decimal.Zero
	for _, j := range jobs {
		total = total.Add(j.Hours)
	}
	span := decimal.NewFromInt(int64(w.End.Sub(w.Start) / time.Second))
	out := make([]PlannedJob, len(jobs))
	cum := decimal.Zero
	cursor := w.Start
	for i, j := range jobs {
		if total.IsZero() {
			cum = cum.Add(decimal.NewFromInt(1))
		} else {
			cum = cum.Add(j.Hours)
		}
		end := w.End
		if i < len(jobs)-1 {
			share := cum.Div(decimal.NewFromInt(int64(len(jobs))))
			if !total.IsZero() {
				share = cum.Div(total)
			}
			end = w.Start.Add(time.Duration(span.Mul(share).Round(0).IntPart()) * time.Second)
		}
		j.Start, j.End = cursor, end
		cursor = end
		out[i] = j
	}
	return out
}
