package domain

import "testing"

func endDoc() RecipeDoc {
	return RecipeDoc{Procedure: Procedure{Name: "Batch", UnitProcedures: []UnitProcedure{{Name: "Reactor", Operations: []Operation{
		{Name: "Heat", Phases: []Phase{{Name: "Ramp"}, {Name: "Hold"}}}, {Name: "Cool", Phases: []Phase{{Name: "Drop"}}}}}}}}
}

func TestRecipeEnd(t *testing.T) {
	status := func(v string, extra ...Characteristic) []Characteristic {
		return append([]Characteristic{{Name: StatusCharacteristic, Type: "text", Value: v}}, extra...)
	}
	for name, tc := range map[string]struct {
		category, path string
		values         []Characteristic
		to, text       string
		ends           bool
	}{
		"completed":           {"order-status", "", status(" Completed "), RecipeCompleted, "", true},
		"aborted with reason": {"order-status", "", status("aborted", Characteristic{Name: StatusMessageCharacteristic, Type: "text", Value: "high pressure"}), RecipeError, "high pressure", true},
		"failed without one":  {"order-status", "", status("failed"), RecipeError, "the process control system reported the batch as failed", true},
		"running":             {"order-status", "", status("running"), "", "", false},
		"no status":           {"order-status", "", nil, "", "", false},
		"last phase ends":     {"phase-end", "Batch/Reactor/Cool/Drop", nil, RecipeCompleted, "", true},
		"another phase ends":  {"phase-end", "Batch/Reactor/Heat/Hold", nil, "", "", false},
		"phase start":         {"phase-start", "Batch/Reactor/Cool/Drop", nil, "", "", false},
		"deviation":           {"deviation", "Batch/Reactor/Cool/Drop", status("completed"), "", "", false},
	} {
		to, text, ends := RecipeEnd(tc.category, tc.path, tc.values, endDoc())
		if to != tc.to || text != tc.text || ends != tc.ends {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", name, to, text, ends, tc.to, tc.text, tc.ends)
		}
	}
	if got := (RecipeDoc{}).PhasePaths(); len(got) != 0 {
		t.Fatalf("an empty recipe has phases: %v", got)
	}
	if _, _, ends := RecipeEnd("phase-end", "x", nil, RecipeDoc{}); ends {
		t.Fatal("a recipe with no phases cannot end by a phase")
	}
}
