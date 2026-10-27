package domain

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// DemandPriority orders opportunistic cross-dock demand (BR-31.11).
type DemandPriority string

const (
	PrioritySHIPDate    DemandPriority = "SHIP_DATE"
	PriorityOrder       DemandPriority = "ORDER_PRIORITY"
	PriorityFIFORelease DemandPriority = "FIFO_RELEASE"
)

// ValidDemandPriority reports whether p is one of the three priorities.
func ValidDemandPriority(p DemandPriority) bool {
	return p == PrioritySHIPDate || p == PriorityOrder || p == PriorityFIFORelease
}

// QualityAccepted is the only quality status that may be cross-docked (BR-31.11).
const QualityAccepted = "ACCEPTED"

// CodeQualityHold is the catalog code for a supply under quality hold.
const CodeQualityHold = "WMS-0113"

// Supply is a received quantity of one item at a warehouse.
type Supply struct {
	ID         string          `json:"id"`
	ItemID     string          `json:"itemId"`
	Warehouse  string          `json:"warehouse"`
	Qty        decimal.Decimal `json:"qty"`
	ReceivedAt time.Time       `json:"receivedAt"`
	Quality    string          `json:"quality"`
}

// Demand is an outbound line with unpicked quantity. A lower OrderPriority
// number is more urgent.
type Demand struct {
	LineID        string          `json:"lineId"`
	ItemID        string          `json:"itemId"`
	Warehouse     string          `json:"warehouse"`
	ShipDate      time.Time       `json:"shipDate"`
	OrderPriority int             `json:"orderPriority"`
	ReleasedAt    time.Time       `json:"releasedAt"`
	OpenQty       decimal.Decimal `json:"openQty"`
}

// PlannedLink is a planned cross-dock link from an expected supply to a demand
// line, created before arrival by a cross-dock template (FR-31.13).
type PlannedLink struct {
	SupplyID string          `json:"supplyId"`
	LineID   string          `json:"lineId"`
	Qty      decimal.Decimal `json:"qty"`
}

// DefaultSupplyWindow and MaxSupplyWindow are crossdock_supply_window_h from
// §6.31.13 (default 48 h, range 0–336 h). The template stores the window, so a
// zero Window means a zero-hour window, not the default.
const (
	DefaultSupplyWindow = 48 * time.Hour
	MaxSupplyWindow     = 336 * time.Hour
)

// CrossDockOptions are the template settings for one receipt.
type CrossDockOptions struct {
	Priority      DemandPriority
	Window        time.Duration
	Opportunistic bool
	Planned       []PlannedLink
}

// Allocation is quantity of a supply sent to one demand line.
type Allocation struct {
	LineID  string          `json:"lineId"`
	Qty     decimal.Decimal `json:"qty"`
	Planned bool            `json:"planned"`
}

// CrossDockPlan is the outcome of a receipt. Held supply is neither cross-docked
// nor put away until its usage decision; Remainder is the put-away quantity.
type CrossDockPlan struct {
	Allocations []Allocation    `json:"allocations"`
	CrossDocked decimal.Decimal `json:"crossDocked"`
	Remainder   decimal.Decimal `json:"remainder"`
	Held        bool            `json:"held"`
	Warnings    []string        `json:"warnings"`
}

// PlanReceipt allocates a receipt to demand: planned links first, then, when
// opportunistic is on, released demand in window (BR-31.11). Allocation is
// greedy; cross-dock quantity is min(receipt, open demand).
func PlanReceipt(s Supply, demands []Demand, opt CrossDockOptions) (CrossDockPlan, error) {
	if !s.Qty.IsPositive() {
		return CrossDockPlan{}, fmt.Errorf("supply %q must have a positive quantity", s.ID)
	}
	if opt.Priority == "" {
		opt.Priority = PrioritySHIPDate
	}
	if !ValidDemandPriority(opt.Priority) {
		return CrossDockPlan{}, fmt.Errorf("unknown demand priority %q", opt.Priority)
	}
	if opt.Window < 0 || opt.Window > MaxSupplyWindow {
		return CrossDockPlan{}, fmt.Errorf("supply window must be between 0 and %s", MaxSupplyWindow)
	}
	if s.Quality != QualityAccepted {
		return CrossDockPlan{
			Allocations: []Allocation{},
			Held:        true,
			Warnings:    []string{CodeQualityHold + ": supply is under quality hold; cross-dock deferred to usage decision"},
		}, nil
	}

	open := map[string]decimal.Decimal{}
	byLine := map[string]Demand{}
	for _, d := range demands {
		if _, dup := byLine[d.LineID]; dup {
			return CrossDockPlan{}, fmt.Errorf("demand line %q is listed twice", d.LineID)
		}
		byLine[d.LineID] = d
		if d.OpenQty.IsPositive() {
			open[d.LineID] = d.OpenQty
		}
	}

	plan := CrossDockPlan{Allocations: []Allocation{}}
	remaining := s.Qty
	allocate := func(line string, q decimal.Decimal, planned bool) {
		plan.Allocations = append(plan.Allocations, Allocation{LineID: line, Qty: q, Planned: planned})
		plan.CrossDocked = plan.CrossDocked.Add(q)
		remaining = remaining.Sub(q)
		open[line] = open[line].Sub(q)
	}

	for _, link := range opt.Planned {
		if link.SupplyID != s.ID || !link.Qty.IsPositive() || !remaining.IsPositive() {
			continue
		}
		d, ok := byLine[link.LineID]
		if !ok || d.ItemID != s.ItemID || d.Warehouse != s.Warehouse || !open[link.LineID].IsPositive() {
			continue
		}
		if q := decimal.Min(link.Qty, remaining, open[link.LineID]); q.IsPositive() {
			allocate(link.LineID, q, true)
		}
	}

	if opt.Opportunistic && remaining.IsPositive() {
		deadline := s.ReceivedAt.Add(opt.Window)
		var cands []Demand
		for _, d := range demands {
			if d.ItemID != s.ItemID || d.Warehouse != s.Warehouse || d.ShipDate.After(deadline) {
				continue
			}
			if open[d.LineID].IsPositive() {
				cands = append(cands, d)
			}
		}
		slices.SortStableFunc(cands, func(a, b Demand) int {
			if c := priorityCmp(a, b, opt.Priority); c != 0 {
				return c
			}
			if c := a.ShipDate.Compare(b.ShipDate); c != 0 {
				return c
			}
			return strings.Compare(a.LineID, b.LineID)
		})
		for _, d := range cands {
			if !remaining.IsPositive() {
				break
			}
			allocate(d.LineID, decimal.Min(remaining, open[d.LineID]), false)
		}
	}

	plan.Remainder = remaining
	return plan, nil
}

// priorityCmp is the primary ordering key of the demand priority; ship date and
// line id break ties in PlanReceipt.
func priorityCmp(a, b Demand, p DemandPriority) int {
	switch p {
	case PriorityOrder:
		return cmp.Compare(a.OrderPriority, b.OrderPriority)
	case PriorityFIFORelease:
		return a.ReleasedAt.Compare(b.ReleasedAt)
	}
	return 0
}
