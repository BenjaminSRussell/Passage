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

func TestMaterialListInstructionIsChecked(t *testing.T) {
	doc := func(in Instruction) RecipeDoc {
		return RecipeDoc{Procedure: Procedure{Name: "B", UnitProcedures: []UnitProcedure{{Name: "U", Operations: []Operation{{Name: "O", Phases: []Phase{{Name: "P", Instructions: []Instruction{in}}}}}}}}}
	}
	good := Instruction{Seq: 1, Category: InstMaterials, Text: "Charge 2 materials", Materials: []InstructionMaterial{
		{ItemID: "0195f0a0-0000-7000-8000-0000000000a1", Qty: "12.5", Unit: "kg", Role: "ingredient"}}}
	if f := doc(good).FieldErrors(DestKiosk); len(f) != 0 {
		t.Fatalf("a good material list: %v", f)
	}
	for name, mutate := range map[string]func(*Instruction){
		"bad item":     func(i *Instruction) { i.Materials[0].ItemID = "pump" },
		"bad quantity": func(i *Instruction) { i.Materials[0].Qty = "lots" },
		"negative":     func(i *Instruction) { i.Materials[0].Qty = "-1" },
		"bad role":     func(i *Instruction) { i.Materials[0].Role = "waste" },
	} {
		in := good
		in.Materials = append([]InstructionMaterial(nil), good.Materials...)
		mutate(&in)
		if f := doc(in).FieldErrors(DestKiosk); len(f) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	text := Instruction{Seq: 1, Category: InstText, Text: "Check", Materials: good.Materials}
	if f := doc(text).FieldErrors(DestKiosk); len(f) == 0 {
		t.Error("materials on a text instruction were accepted")
	}
}
