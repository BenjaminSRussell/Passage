package domain

import "testing"

func TestDefaultParametersAreValidAndMatchTheBuiltInLimits(t *testing.T) {
	p := DefaultParameters()
	if errs := p.Validate(); len(errs) != 0 {
		t.Fatalf("defaults invalid: %v", errs)
	}
	if p.Limit() != (PropagationLimit{Orders: DefaultPropagationOrders, Levels: DefaultPropagationLevels}) {
		t.Fatalf("limit = %+v", p.Limit())
	}
}

func TestParametersValidate(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*Parameters)
		field string
	}{
		{"mode", func(p *Parameters) { p.DefaultMode = "weekly" }, "defaultMode"},
		{"direction", func(p *Parameters) { p.DefaultDirection = "sideways" }, "defaultDirection"},
		{"snap low", func(p *Parameters) { p.DragSnapMinutes = 0 }, "dragSnapMinutes"},
		{"snap high", func(p *Parameters) { p.DragSnapMinutes = MaxDragSnapMinutes + 1 }, "dragSnapMinutes"},
		{"orders low", func(p *Parameters) { p.PropagationOrders = 0 }, "propagationOrders"},
		{"orders high", func(p *Parameters) { p.PropagationOrders = MaxPropagationOrders + 1 }, "propagationOrders"},
		{"levels low", func(p *Parameters) { p.PropagationLevels = 0 }, "propagationLevels"},
		{"levels high", func(p *Parameters) { p.PropagationLevels = MaxPropagationLevels + 1 }, "propagationLevels"},
	}
	for _, c := range cases {
		p := DefaultParameters()
		c.edit(&p)
		errs := p.Validate()
		if _, ok := errs[c.field]; !ok || len(errs) != 1 {
			t.Errorf("%s: errors = %v, want only %s", c.name, errs, c.field)
		}
	}
	for _, edge := range []Parameters{
		{"operations", "backward", false, 1, 1, 1},
		{"jobs", "forward", true, 7, 4999, 19},
		{"jobs", "forward", true, MaxDragSnapMinutes, MaxPropagationOrders, MaxPropagationLevels},
	} {
		if errs := edge.Validate(); len(errs) != 0 {
			t.Errorf("%+v: %v", edge, errs)
		}
	}
}

func TestPropagationLimitFromParametersCutsTheWalk(t *testing.T) {
	pegs := []Peg{{"a", "b"}, {"b", "c"}, {"c", "d"}}
	p := DefaultParameters()
	p.PropagationLevels = 2
	got, truncated := DependentsOf("a", pegs, p.Limit())
	if len(got) != 2 || !truncated {
		t.Fatalf("walk = %v truncated=%v, want 2 orders and truncated", got, truncated)
	}
	p.PropagationLevels = 5
	p.PropagationOrders = 1
	got, truncated = DependentsOf("a", pegs, p.Limit())
	if len(got) != 1 || !truncated {
		t.Fatalf("walk = %v truncated=%v, want 1 order and truncated", got, truncated)
	}
}
