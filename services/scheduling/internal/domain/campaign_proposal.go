package domain

import (
	"slices"
	"strings"
	"time"
)

// Campaign proposal window (FR-23.14, site parameter "Campaign proposal window
// days", default 14, range 1-90). Site parameters are not built yet, so the
// window is chosen per inquiry.
const (
	DefaultProposalWindowDays = 14
	MaxProposalWindowDays     = 90
)

// CampaignKeyAttribute is the operation attribute that carries an order's
// campaign key (a formula family, for example). An operation without it is
// keyed by its setup group, the other key FR-23.14 names.
const CampaignKeyAttribute = "campaignKey"

// MaxProposalOrders caps one proposed campaign at the campaign order limit; a
// longer run of orders sharing a key is proposed as consecutive campaigns.
const MaxProposalOrders = 1000

// ProposalCandidate is one scheduled operation the campaign-proposal rules
// consider: an order not in any campaign, placed on a resource, with the setup
// group a campaign order needs. Start is the start of its scheduled job.
type ProposalCandidate struct {
	OrderID           string            `json:"orderId"`
	OperationID       string            `json:"operationId"`
	OperNumber        int               `json:"operNumber"`
	ResourceID        string            `json:"resourceId"`
	SetupGroup        string            `json:"setupGroup"`
	Attributes        map[string]string `json:"-"`
	RunMinutes        int               `json:"runMinutes"`
	RouteSetupMinutes int               `json:"routeSetupMinutes"`
	Start             time.Time         `json:"start"`
	DueAt             *time.Time        `json:"dueAt,omitempty"`
	Firmed            bool              `json:"firmed"`
}

// CampaignKey is the key the candidate is grouped by: the campaignKey
// attribute when set, otherwise the setup group.
func (c ProposalCandidate) CampaignKey() string {
	if k := strings.TrimSpace(c.Attributes[CampaignKeyAttribute]); k != "" {
		return k
	}
	return c.SetupGroup
}

// ProposedCampaign is a campaign the proposal rules suggest: orders sharing a
// campaign key whose jobs on one resource start inside the window, in the
// order they are scheduled. PlannedStart is the earliest of their starts.
type ProposedCampaign struct {
	CampaignKey  string              `json:"campaignKey"`
	ResourceID   string              `json:"resourceId"`
	PlannedStart time.Time           `json:"plannedStart"`
	RunMinutes   int                 `json:"runMinutes"`
	Orders       []ProposalCandidate `json:"orders"`
}

// ProposeCampaigns groups planned and firmed orders that share a campaign key
// into campaign proposals (FR-23.14). Only jobs starting in
// [from, from+windowDays) count; a window outside 1-90 days uses the default.
// An order is offered once, with its earliest operation in the window (lowest
// operation number on a tie), so it never lands in two proposals. A group
// needs at least two orders to be worth a campaign, and a group longer than
// MaxProposalOrders is split into consecutive proposals. Proposals come back
// by planned start, then key, then resource, so the same input always gives
// the same answer.
func ProposeCampaigns(cands []ProposalCandidate, from time.Time, windowDays int) []ProposedCampaign {
	if windowDays < 1 || windowDays > MaxProposalWindowDays {
		windowDays = DefaultProposalWindowDays
	}
	end := from.AddDate(0, 0, windowDays)
	first := map[string]ProposalCandidate{}
	for _, c := range cands {
		if c.OrderID == "" || c.ResourceID == "" || c.SetupGroup == "" || c.CampaignKey() == "" {
			continue
		}
		if c.Start.Before(from) || !c.Start.Before(end) {
			continue
		}
		if prev, ok := first[c.OrderID]; ok && !earlierCandidate(c, prev) {
			continue
		}
		first[c.OrderID] = c
	}
	type groupKey struct{ key, resource string }
	groups := map[groupKey][]ProposalCandidate{}
	for _, c := range first {
		k := groupKey{c.CampaignKey(), c.ResourceID}
		groups[k] = append(groups[k], c)
	}
	out := []ProposedCampaign{}
	for k, members := range groups {
		slices.SortFunc(members, func(a, b ProposalCandidate) int {
			if c := a.Start.Compare(b.Start); c != 0 {
				return c
			}
			return strings.Compare(a.OrderID, b.OrderID)
		})
		for len(members) >= 2 {
			n := min(len(members), MaxProposalOrders)
			if rest := len(members) - n; rest == 1 {
				// Never leave a single order behind as a one-order remainder.
				n--
			}
			chunk := members[:n]
			p := ProposedCampaign{CampaignKey: k.key, ResourceID: k.resource, PlannedStart: chunk[0].Start, Orders: slices.Clone(chunk)}
			for _, m := range chunk {
				p.RunMinutes += m.RunMinutes
			}
			out = append(out, p)
			members = members[n:]
		}
	}
	slices.SortFunc(out, func(a, b ProposedCampaign) int {
		if c := a.PlannedStart.Compare(b.PlannedStart); c != 0 {
			return c
		}
		if c := strings.Compare(a.CampaignKey, b.CampaignKey); c != 0 {
			return c
		}
		return strings.Compare(a.ResourceID, b.ResourceID)
	})
	return out
}

func earlierCandidate(a, b ProposalCandidate) bool {
	if c := a.Start.Compare(b.Start); c != 0 {
		return c < 0
	}
	return a.OperNumber < b.OperNumber
}
