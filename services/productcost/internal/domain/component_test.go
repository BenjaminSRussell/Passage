package domain

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func fieldKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestCostComponent_Validate(t *testing.T) {
	ok := CostComponent{Code: "DM", Name: "Direct material", CostGroupType: GroupDirectMaterial, Behaviour: BehaviourVariable}
	tests := []struct {
		name   string
		edit   func(c *CostComponent)
		fields string
	}{
		{"valid", func(*CostComponent) {}, ""},
		{"hyphenated code", func(c *CostComponent) { c.Code = "IND-MAT" }, ""},
		{"twenty characters", func(c *CostComponent) { c.Code = strings.Repeat("A", 20) }, ""},
		{"lower-case code", func(c *CostComponent) { c.Code = "dm" }, "code"},
		{"leading hyphen", func(c *CostComponent) { c.Code = "-DM" }, "code"},
		{"twenty-one characters", func(c *CostComponent) { c.Code = strings.Repeat("A", 21) }, "code"},
		{"blank name", func(c *CostComponent) { c.Name = "" }, "name"},
		{"name of 121 runes", func(c *CostComponent) { c.Name = strings.Repeat("é", 121) }, "name"},
		{"unknown group type", func(c *CostComponent) { c.CostGroupType = "Labour" }, "costGroupType"},
		{"unknown behaviour", func(c *CostComponent) { c.Behaviour = "Mixed" }, "behaviour"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.edit(&c)
			if got := strings.Join(fieldKeys(c.Validate()), ","); got != tc.fields {
				t.Fatalf("fields %q, want %q", got, tc.fields)
			}
		})
	}
}

func TestCostComponent_NormalizeTrimsAndDefaultsBehaviour(t *testing.T) {
	got := CostComponent{Code: " SUB ", Name: " Subcontract ", CostGroupType: GroupUndefined}.Normalize()
	if got.Code != "SUB" || got.Name != "Subcontract" || got.Behaviour != BehaviourVariable {
		t.Fatalf("Normalize = %+v", got)
	}
	if got := (CostComponent{Behaviour: BehaviourFixed}).Normalize(); got.Behaviour != BehaviourFixed {
		t.Fatalf("Normalize overwrote an explicit behaviour: %+v", got)
	}
}

func TestCostGroup_Validate(t *testing.T) {
	ok := CostGroup{Code: "MACH", Name: "Machining", Type: GroupDirectManufacturing, Behaviour: BehaviourVariable, ComponentID: ids.New()}
	tests := []struct {
		name   string
		edit   func(g *CostGroup)
		fields string
	}{
		{"valid", func(*CostGroup) {}, ""},
		{"missing component", func(g *CostGroup) { g.ComponentID = "" }, "componentId"},
		{"component not a uuid", func(g *CostGroup) { g.ComponentID = "DM" }, "componentId"},
		{"unknown type", func(g *CostGroup) { g.Type = "Overhead" }, "type"},
		{"several faults", func(g *CostGroup) { g.Code = ""; g.Name = ""; g.Behaviour = "x" }, "behaviour,code,name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := ok
			tc.edit(&g)
			if got := strings.Join(fieldKeys(g.Validate()), ","); got != tc.fields {
				t.Fatalf("fields %q, want %q", got, tc.fields)
			}
		})
	}
}

func TestCapacityLimits(t *testing.T) {
	tests := []struct {
		name     string
		check    func(int) error
		max      int
		wantCode string
	}{
		{"components", CheckComponentCapacity, MaxComponents, CodeComponentLimit},
		{"cost groups", CheckGroupCapacity, MaxCostGroups, CodeGroupLimit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.check(tc.max - 1); err != nil {
				t.Fatalf("one below the limit: %v", err)
			}
			for _, n := range []int{tc.max, tc.max + 3} {
				var ce *CodedError
				if err := tc.check(n); !errors.As(err, &ce) || ce.Code != tc.wantCode {
					t.Fatalf("%d existing: %v, want %s", n, err, tc.wantCode)
				}
			}
		})
	}
	if MaxComponents != 20 || MaxCostGroups != 50 {
		t.Fatalf("FR-39.1 limits are 20 components and 50 groups, got %d and %d", MaxComponents, MaxCostGroups)
	}
}
