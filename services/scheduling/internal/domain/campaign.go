package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Campaign statuses (FR-23.14). Only proposed and planned campaigns accept
// orders; a campaign is frozen once its first order is started.
const (
	CampaignProposed  = "proposed"
	CampaignPlanned   = "planned"
	CampaignStarted   = "started"
	CampaignCompleted = "completed"
	CampaignCancelled = "cancelled"
)

// Sequence rules a campaign can use (FR-23.14). The CP-SAT circuit rule is
// not built yet (see NOT-IMPLEMENTED.md).
const (
	SequenceNearestNeighbor = "nearest-neighbor"
	SequenceManual          = "manual"
)

// CodeCampaignStarted is SCHED-0011: the order cannot join a frozen campaign.
const CodeCampaignStarted = "SCHED-0011"

// Error makes a Conflict usable as an error value.
func (c *Conflict) Error() string { return c.Code + ": " + c.Detail }

// CampaignLimits caps a campaign. Zero means unlimited. MaxMinutes bounds the
// campaign's contiguous duration on the resource: run, changeover and cleanout
// minutes together.
type CampaignLimits struct {
	MaxOrders  int
	MaxQty     decimal.Decimal
	MaxMinutes int
}

// CampaignOrder is a production order placed in a campaign. Each order is one
// batch for the BR-23.13 batch counter.
type CampaignOrder struct {
	SetupOrder
	Qty decimal.Decimal
}

// CampaignInput is everything PlanCampaign needs. BatchesSinceCleanout and
// MinutesSinceCleanout carry the resource's counters into the campaign.
type CampaignInput struct {
	Status               string
	Rule                 string
	Matrix               SetupMatrix
	Cleanouts            []CleanoutRule
	CurrentGroup         string
	BatchesSinceCleanout int
	MinutesSinceCleanout int
	Limits               CampaignLimits
	Orders               []CampaignOrder
}

// PlannedCleanout is a cleanout order inserted after an order of the campaign.
type PlannedCleanout struct {
	AfterOrderID  string `json:"afterOrderId"`
	SetupGroup    string `json:"setupGroup"`
	Minutes       int    `json:"minutes"`
	Reason        string `json:"reason"`
	CleaningSetID string `json:"cleaningSetId,omitempty"`
}

// CampaignPlan is the contiguous run of a campaign on its resource: the
// sequenced orders, the changeovers between them (the first one is the
// leading setup), and the cleanouts that BR-23.13 demands.
type CampaignPlan struct {
	Sequence          Sequence
	Cleanouts         []PlannedCleanout
	RunMinutes        int
	ChangeoverMinutes int
	CleanoutMinutes   int
	TotalQty          decimal.Decimal
}

// DurationMinutes is the campaign's length on the resource.
func (p CampaignPlan) DurationMinutes() int {
	return p.RunMinutes + p.ChangeoverMinutes + p.CleanoutMinutes
}

// CanJoin reports whether an order may join, leave or resequence a campaign
// with this status.
func CanJoin(status string) *Conflict {
	switch status {
	case CampaignProposed, CampaignPlanned:
		return nil
	case CampaignStarted, CampaignCompleted, CampaignCancelled:
		return &Conflict{Code: CodeCampaignStarted, Detail: fmt.Sprintf("campaign is %s and no longer accepts changes; use the next campaign", status)}
	}
	return &Conflict{Code: CodeCampaignStarted, Detail: "unknown campaign status " + status}
}

// CheckOrders validates a campaign's order list against its limits on order
// count and quantity. The duration limit needs the sequence and is checked by
// PlanCampaign.
func CheckOrders(orders []CampaignOrder, lim CampaignLimits) error {
	seen := make(map[string]bool, len(orders))
	total := decimal.Zero
	for _, o := range orders {
		if seen[o.OrderID] {
			return fmt.Errorf("order %s is listed twice", o.OrderID)
		}
		seen[o.OrderID] = true
		if !o.Qty.IsPositive() || o.Minutes < 0 || o.RouteSetupMinutes < 0 {
			return fmt.Errorf("order %s needs a positive quantity and non-negative minutes", o.OrderID)
		}
		total = total.Add(o.Qty)
	}
	switch {
	case lim.MaxOrders > 0 && len(orders) > lim.MaxOrders:
		return fmt.Errorf("campaign holds %d orders, limit is %d", len(orders), lim.MaxOrders)
	case lim.MaxQty.IsPositive() && total.GreaterThan(lim.MaxQty):
		return fmt.Errorf("campaign quantity %s exceeds limit %s", total, lim.MaxQty)
	}
	return nil
}

// PlanCampaign sequences a campaign (FR-23.14) with the setup matrix and
// inserts cleanouts (FR-23.15, BR-23.13): after the order at which the batch or
// run-time counter of its group's rule is reached, and after the last order
// when the rule demands cleaning at campaign end. A changeover that requires a
// cleaning operation set cleans the resource, so it resets the counters. It
// refuses frozen campaigns and campaigns over their limits.
func PlanCampaign(in CampaignInput) (CampaignPlan, error) {
	if c := CanJoin(in.Status); c != nil {
		return CampaignPlan{}, c
	}
	if len(in.Orders) == 0 {
		return CampaignPlan{}, fmt.Errorf("campaign has no orders")
	}
	if err := CheckOrders(in.Orders, in.Limits); err != nil {
		return CampaignPlan{}, err
	}
	plan := CampaignPlan{TotalQty: decimal.Zero, Cleanouts: []PlannedCleanout{}}
	setup := make([]SetupOrder, 0, len(in.Orders))
	for _, o := range in.Orders {
		setup = append(setup, o.SetupOrder)
		plan.RunMinutes += o.Minutes
		plan.TotalQty = plan.TotalQty.Add(o.Qty)
	}
	switch in.Rule {
	case SequenceManual:
		plan.Sequence = SequenceAsGiven(in.Matrix, in.CurrentGroup, setup)
	case SequenceNearestNeighbor, "":
		plan.Sequence = SequenceNearest(in.Matrix, in.CurrentGroup, setup)
	default:
		return CampaignPlan{}, fmt.Errorf("sequence rule %q is not available", in.Rule)
	}
	plan.ChangeoverMinutes = plan.Sequence.TotalMinutes

	rules := make(map[string]CleanoutRule, len(in.Cleanouts))
	for _, r := range in.Cleanouts {
		rules[r.SetupGroup] = r
	}
	cleaning := make(map[string]bool, len(plan.Sequence.Changeovers))
	for _, c := range plan.Sequence.Changeovers {
		if c.CleaningSetID != "" {
			cleaning[c.AfterOrderID] = true
		}
	}
	batches, minutes := in.BatchesSinceCleanout, in.MinutesSinceCleanout
	last := len(plan.Sequence.Orders) - 1
	for i, o := range plan.Sequence.Orders {
		if cleaning[o.OrderID] {
			batches, minutes = 0, 0
		}
		batches++
		minutes += o.Minutes
		r, ok := rules[o.SetupGroup]
		if !ok {
			continue
		}
		if reason := CleanoutDue(r, batches, minutes, i == last); reason != CleanoutNone {
			plan.Cleanouts = append(plan.Cleanouts, PlannedCleanout{AfterOrderID: o.OrderID, SetupGroup: o.SetupGroup,
				Minutes: r.DurationMinutes, Reason: reason, CleaningSetID: r.CleaningSetID})
			plan.CleanoutMinutes += r.DurationMinutes
			batches, minutes = 0, 0
		}
	}
	if in.Limits.MaxMinutes > 0 && plan.DurationMinutes() > in.Limits.MaxMinutes {
		return CampaignPlan{}, fmt.Errorf("campaign runs %d minutes with changeovers and cleanouts, limit is %d", plan.DurationMinutes(), in.Limits.MaxMinutes)
	}
	return plan, nil
}
