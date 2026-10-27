package domain

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

var proposalFrom = time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)

func cand(order string, oper int, resource, group string, day, hour int) ProposalCandidate {
	return ProposalCandidate{OrderID: order, OperationID: fmt.Sprintf("%s-%d", order, oper), OperNumber: oper, ResourceID: resource,
		SetupGroup: group, RunMinutes: 60, RouteSetupMinutes: 10, Start: proposalFrom.AddDate(0, 0, day).Add(time.Duration(hour) * time.Hour)}
}

func withKey(c ProposalCandidate, key string) ProposalCandidate {
	c.Attributes = map[string]string{CampaignKeyAttribute: key}
	return c
}

func orderIDs(p ProposedCampaign) []string {
	out := make([]string, 0, len(p.Orders))
	for _, o := range p.Orders {
		out = append(out, o.OrderID)
	}
	return out
}

func TestProposeCampaigns_Rules(t *testing.T) {
	cases := []struct {
		name   string
		cands  []ProposalCandidate
		window int
		want   []string // "key/resource:order,order"
	}{
		{
			name:  "orders sharing a setup group on one resource form a campaign in scheduled order",
			cands: []ProposalCandidate{cand("b", 10, "R1", "WHITE", 2, 8), cand("a", 10, "R1", "WHITE", 1, 8)},
			want:  []string{"WHITE/R1:a,b"},
		},
		{
			name:  "a single order is not a campaign",
			cands: []ProposalCandidate{cand("a", 10, "R1", "WHITE", 1, 8), cand("b", 10, "R1", "BLACK", 1, 9)},
			want:  nil,
		},
		{
			name: "the campaignKey attribute groups orders across setup groups",
			cands: []ProposalCandidate{withKey(cand("a", 10, "R1", "WHITE", 1, 8), "FAMILY-1"),
				withKey(cand("b", 10, "R1", "BLACK", 1, 9), "FAMILY-1"), cand("c", 10, "R1", "WHITE", 1, 10)},
			want: []string{"FAMILY-1/R1:a,b"},
		},
		{
			name:  "the same key on two resources is two groups",
			cands: []ProposalCandidate{cand("a", 10, "R1", "W", 1, 8), cand("b", 10, "R2", "W", 1, 8), cand("c", 10, "R1", "W", 2, 8), cand("d", 10, "R2", "W", 2, 8)},
			want:  []string{"W/R1:a,c", "W/R2:b,d"},
		},
		{
			name: "the window is [from, from+days): the start of day 14 is outside",
			cands: []ProposalCandidate{cand("a", 10, "R1", "W", 0, 0), cand("b", 10, "R1", "W", 13, 23), cand("c", 10, "R1", "W", 14, 0),
				cand("d", 10, "R1", "W", -1, 23)},
			want: []string{"W/R1:a,b"},
		},
		{
			name:   "a custom window widens the horizon",
			cands:  []ProposalCandidate{cand("a", 10, "R1", "W", 0, 0), cand("c", 10, "R1", "W", 20, 0)},
			window: 30,
			want:   []string{"W/R1:a,c"},
		},
		{
			name:   "a window outside 1-90 days falls back to 14",
			cands:  []ProposalCandidate{cand("a", 10, "R1", "W", 0, 0), cand("c", 10, "R1", "W", 20, 0)},
			window: 91,
			want:   nil,
		},
		{
			name: "an order is offered once, with its earliest operation in the window",
			cands: []ProposalCandidate{cand("a", 20, "R2", "W", 3, 0), cand("a", 10, "R1", "W", 1, 0), cand("b", 10, "R1", "W", 2, 0),
				cand("c", 10, "R2", "W", 4, 0)},
			want: []string{"W/R1:a,b"},
		},
		{
			name:  "operations without a setup group or resource are skipped",
			cands: []ProposalCandidate{cand("a", 10, "R1", "", 1, 0), cand("b", 10, "", "W", 1, 0), cand("c", 10, "R1", "W", 1, 0)},
			want:  nil,
		},
		{
			name:  "proposals come back by planned start",
			cands: []ProposalCandidate{cand("a", 10, "R1", "Z", 1, 0), cand("b", 10, "R1", "Z", 5, 0), cand("c", 10, "R1", "A", 3, 0), cand("d", 10, "R1", "A", 4, 0)},
			want:  []string{"Z/R1:a,b", "A/R1:c,d"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			window := tc.window
			if window == 0 {
				window = DefaultProposalWindowDays
			}
			got := ProposeCampaigns(tc.cands, proposalFrom, window)
			var flat []string
			for _, p := range got {
				flat = append(flat, fmt.Sprintf("%s/%s:%s", p.CampaignKey, p.ResourceID, strings.Join(orderIDs(p), ",")))
			}
			if !slices.Equal(flat, tc.want) {
				t.Fatalf("proposals = %v, want %v", flat, tc.want)
			}
		})
	}
}

func TestProposeCampaigns_PlannedStartAndRunMinutes(t *testing.T) {
	a, b := cand("a", 10, "R1", "W", 2, 6), cand("b", 10, "R1", "W", 1, 7)
	b.RunMinutes = 45
	got := ProposeCampaigns([]ProposalCandidate{a, b}, proposalFrom, 14)
	if len(got) != 1 {
		t.Fatalf("proposals = %+v", got)
	}
	if !got[0].PlannedStart.Equal(b.Start) || got[0].RunMinutes != 105 {
		t.Fatalf("planned start %v run minutes %d, want %v and 105", got[0].PlannedStart, got[0].RunMinutes, b.Start)
	}
	if got[0].Orders[0].OperationID != "b-10" || got[0].Orders[0].RouteSetupMinutes != 10 {
		t.Fatalf("first order = %+v", got[0].Orders[0])
	}
}

func TestProposeCampaigns_SplitsAtTheOrderLimitWithoutASingleOrderTail(t *testing.T) {
	for _, n := range []int{MaxProposalOrders, MaxProposalOrders + 1, MaxProposalOrders + 2} {
		cands := make([]ProposalCandidate, 0, n)
		for i := range n {
			c := cand(fmt.Sprintf("o%05d", i), 10, "R1", "W", 0, 0)
			c.Start = c.Start.Add(time.Duration(i) * time.Minute)
			cands = append(cands, c)
		}
		got := ProposeCampaigns(cands, proposalFrom, 14)
		total := 0
		for _, p := range got {
			if len(p.Orders) < 2 || len(p.Orders) > MaxProposalOrders {
				t.Fatalf("n=%d: proposal with %d orders", n, len(p.Orders))
			}
			total += len(p.Orders)
		}
		if total != n {
			t.Fatalf("n=%d: proposals hold %d orders", n, total)
		}
		if n == MaxProposalOrders && len(got) != 1 {
			t.Fatalf("n=%d: %d proposals, want 1", n, len(got))
		}
	}
}
