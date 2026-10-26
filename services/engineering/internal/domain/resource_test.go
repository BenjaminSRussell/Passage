package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

func ids(rs []domain.Resource) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestSatisfies(t *testing.T) {
	asOf := day("2026-06-01")
	weld := func(level int, from, to string) []domain.ResourceCapability {
		c := domain.ResourceCapability{CapabilityID: "weld", Level: level, EffectiveFrom: day(from)}
		if to != "" {
			c.EffectiveTo = day(to)
		}
		return []domain.ResourceCapability{c}
	}
	resources := []domain.Resource{
		{ID: "r1", Type: domain.TypeMachine, GroupID: "g1", Capabilities: weld(3, "2026-01-01", "")},
		{ID: "r2", Type: domain.TypeMachine, GroupID: "g1", Capabilities: weld(1, "2026-01-01", "")},
		{ID: "r3", Type: domain.TypeLabor, GroupID: "g2", Capabilities: weld(3, "2026-07-01", "")},
		{ID: "r4", Type: domain.TypeLabor, GroupID: "g2", Capabilities: weld(3, "2025-01-01", "2026-05-31")},
		{ID: "r5", Type: domain.TypeTool},
		{ID: "r6", Type: domain.TypeLabor, Capabilities: weld(2, "2026-06-01", "2026-06-01")},
	}
	groups := []domain.ResourceGroup{{ID: "g1"}, {ID: "g2"}}
	tests := []struct {
		name string
		req  domain.Requirement
		want []string
	}{
		{"resource", domain.Requirement{Kind: domain.ReqResource, ID: "r5"}, []string{"r5"}},
		{"resource unknown", domain.Requirement{Kind: domain.ReqResource, ID: "nope"}, nil},
		{"group", domain.Requirement{Kind: domain.ReqGroup, ID: "g1"}, []string{"r1", "r2"}},
		{"group unknown", domain.Requirement{Kind: domain.ReqGroup, ID: "g9"}, nil},
		{"type", domain.Requirement{Kind: domain.ReqType, Type: domain.TypeLabor}, []string{"r3", "r4", "r6"}},
		{"capability any level", domain.Requirement{Kind: domain.ReqCapability, ID: "weld"}, []string{"r1", "r2", "r6"}},
		{"capability minimum level excludes lower", domain.Requirement{Kind: domain.ReqCapability, ID: "weld", MinLevel: 2}, []string{"r1", "r6"}},
		{"capability minimum level 3", domain.Requirement{Kind: domain.ReqCapability, ID: "weld", MinLevel: 3}, []string{"r1"}},
		{"capability unknown", domain.Requirement{Kind: domain.ReqCapability, ID: "paint"}, nil},
		{"unknown kind", domain.Requirement{Kind: "other", ID: "r1"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(domain.Satisfies(tc.req, resources, groups, asOf))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
	if got := domain.Satisfies(domain.Requirement{Kind: domain.ReqType, Type: domain.TypeTool}, nil, groups, asOf); len(got) != 0 {
		t.Fatalf("empty resources returned %v", got)
	}
	if got := domain.Satisfies(domain.Requirement{Kind: domain.ReqCapability, ID: "weld"}, resources, groups, time.Time{}); len(got) != 0 {
		t.Fatalf("zero asOf returned %v", ids(got))
	}
}

func TestValidateResource_Vendor(t *testing.T) {
	tests := []struct {
		name string
		r    domain.Resource
		ok   bool
	}{
		{"vendor with party", domain.Resource{Type: domain.TypeVendor, VendorPartyID: "p1"}, true},
		{"vendor without party", domain.Resource{Type: domain.TypeVendor}, false},
		{"machine with party", domain.Resource{Type: domain.TypeMachine, VendorPartyID: "p1"}, false},
		{"machine without party", domain.Resource{Type: domain.TypeMachine}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateResource(tc.r)
			if tc.ok && err != nil {
				t.Fatal(err)
			}
			if !tc.ok && !domain.HasCode(err, domain.CodeResVendor) {
				t.Fatalf("err = %v, want %s", err, domain.CodeResVendor)
			}
		})
	}
}

func TestEffectiveCalendar(t *testing.T) {
	group := domain.ResourceGroup{CalendarID: "cal-group"}
	if got := domain.EffectiveCalendar(domain.Resource{CalendarID: "cal-own"}, group); got != "cal-own" {
		t.Fatalf("own calendar = %q", got)
	}
	if got := domain.EffectiveCalendar(domain.Resource{}, group); got != "cal-group" {
		t.Fatalf("inherited calendar = %q", got)
	}
}
