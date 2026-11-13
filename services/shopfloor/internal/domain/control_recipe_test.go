package domain

import (
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

func setpoint(seq int, node string, value float64) Instruction {
	return Instruction{Seq: seq, Category: InstSetpoint, Node: node, Value: f64(value),
		Char: &InstructionCharacteristic{Name: "Temperature", Type: "numeric", Unit: "degC", Min: f64(60), Max: f64(90)}}
}

func recipe(ins ...Instruction) RecipeDoc {
	return RecipeDoc{Procedure: Procedure{Name: "Batch", UnitProcedures: []UnitProcedure{{Name: "Reactor", Operations: []Operation{{Name: "Heat",
		Phases: []Phase{{Name: "Ramp", Instructions: ins}}}}}}}}
}

// AC-25.11: the setpoint of 72.0 on node ns=2;s=Vessel1.TempSP is a valid pcs-opcua recipe, listed with
// the path of its phase.
func TestRecipe_ValidSetpointRecipeListsItsSetpoints(t *testing.T) {
	doc := recipe(Instruction{Seq: 1, Category: InstText, Text: "Check the vessel is empty."}, setpoint(2, "ns=2;s=Vessel1.TempSP", 72))
	if f := doc.FieldErrors(DestPCSOPCUA); len(f) != 0 {
		t.Fatalf("errors = %v", f)
	}
	sp := doc.Setpoints()
	if len(sp) != 1 || sp[0].Node != "ns=2;s=Vessel1.TempSP" || sp[0].Value != 72 || sp[0].PhasePath != "Batch/Reactor/Heat/Ramp" || sp[0].Unit != "degC" {
		t.Fatalf("setpoints = %+v", sp)
	}
}

func TestRecipe_FieldErrorsNameThePath(t *testing.T) {
	bad := func(in Instruction) map[string]string { return recipe(in).FieldErrors(DestPCSOPCUA) }
	p := "recipe.procedure.unitProcedures[0].operations[0].phases[0].instructions[0]"
	cases := map[string]struct {
		in  Instruction
		key string
	}{
		"setpoint out of limits":  {setpoint(1, "n", 95), p + ".value"},
		"setpoint without a node": {setpoint(1, "", 70), p + ".node"},
		"setpoint without value":  {Instruction{Seq: 1, Category: InstSetpoint, Node: "n", Char: &InstructionCharacteristic{Name: "T", Type: "numeric"}}, p + ".value"},
		"text setpoint":           {Instruction{Seq: 1, Category: InstSetpoint, Node: "n", Value: f64(1), Char: &InstructionCharacteristic{Name: "T", Type: "text"}}, p + ".characteristic.type"},
		"unknown category":        {Instruction{Seq: 1, Category: "magic"}, p + ".category"},
		"wrong sequence":          {Instruction{Seq: 3, Category: InstText, Text: "x"}, p + ".seq"},
		"text without text":       {Instruction{Seq: 1, Category: InstText}, p + ".text"},
		"data request no node":    {Instruction{Seq: 1, Category: InstDataRequest, Char: &InstructionCharacteristic{Name: "Level", Type: "numeric"}}, p + ".node"},
		"limits reversed":         {Instruction{Seq: 1, Category: InstInspection, Char: &InstructionCharacteristic{Name: "pH", Type: "numeric", Min: f64(8), Max: f64(7)}}, p + ".characteristic"},
	}
	for name, tc := range cases {
		if got := bad(tc.in); got[tc.key] == "" {
			t.Errorf("%s: errors = %v, want key %s", name, got, tc.key)
		}
	}
	// The kiosk needs no node: it renders the instruction.
	if f := recipe(setpoint(1, "", 70)).FieldErrors(DestKiosk); len(f) != 0 {
		t.Errorf("kiosk setpoint without a node: %v", f)
	}
	if f := (RecipeDoc{}).FieldErrors(DestKiosk); f["recipe.procedure.name"] == "" || f["recipe.procedure.unitProcedures"] == "" {
		t.Errorf("empty recipe: %v", f)
	}
	dup := recipe(Instruction{Seq: 1, Category: InstText, Text: "a"})
	dup.Procedure.UnitProcedures = append(dup.Procedure.UnitProcedures, dup.Procedure.UnitProcedures[0])
	if f := dup.FieldErrors(DestKiosk); !strings.Contains(f["recipe.procedure.unitProcedures[1].name"], "twice") {
		t.Errorf("duplicate unit procedure: %v", f)
	}
}

func TestRecipeTransition_FollowsTheLifecycle(t *testing.T) {
	ok := [][2]string{{RecipeSent, RecipeReceived}, {RecipeReceived, RecipeInExecution}, {RecipeInExecution, RecipeCompleted},
		{RecipeSent, RecipeError}, {RecipeReceived, RecipeError}, {RecipeInExecution, RecipeError}}
	for _, p := range ok {
		if err := RecipeTransition(p[0], p[1]); err != nil {
			t.Errorf("%s to %s: %v", p[0], p[1], err)
		}
	}
	for _, p := range [][2]string{{RecipeSent, RecipeCompleted}, {RecipeSent, RecipeInExecution}, {RecipeReceived, RecipeCompleted},
		{RecipeCompleted, RecipeInExecution}, {RecipeCompleted, RecipeError}, {RecipeError, RecipeReceived}, {RecipeInExecution, RecipeReceived}} {
		if err := RecipeTransition(p[0], p[1]); err == nil {
			t.Errorf("%s to %s allowed", p[0], p[1])
		}
	}
}

// BR-25.11: only an error is resent, five times at most (send count 1 is the first send), and with force
// and a reason once process messages for the order exist.
func TestResendCheck_BR2511(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		sent     int
		messages bool
		force    bool
		reason   string
		want     string
	}{
		{"first resend", RecipeError, 1, false, false, "", ""},
		{"fifth resend", RecipeError, 5, false, false, "", ""},
		{"sixth resend", RecipeError, 6, false, false, "", "limit"},
		{"not in error", RecipeInExecution, 1, false, false, "", "transition"},
		{"messages without force", RecipeError, 2, true, false, "", "force"},
		{"force without a reason", RecipeError, 2, true, true, "  ", "force"},
		{"force with a reason", RecipeError, 2, true, true, "PCS confirmed nothing ran", ""},
		{"force is not needed without messages", RecipeError, 2, false, false, "", ""},
	}
	for _, tc := range cases {
		got, err := ResendCheck(tc.status, tc.sent, tc.messages, tc.force, tc.reason)
		if got != tc.want || (got != "" && err == nil) {
			t.Errorf("%s: kind %q err %v, want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestSignatureMeaningIsCheckedOnSignatureSteps(t *testing.T) {
	doc := func(ins Instruction) RecipeDoc {
		return RecipeDoc{Procedure: Procedure{Name: "P", UnitProcedures: []UnitProcedure{{Name: "U", Operations: []Operation{{Name: "O",
			Phases: []Phase{{Name: "Ph", Instructions: []Instruction{ins}}}}}}}}}
	}
	if f := doc(Instruction{Seq: 1, Category: InstSignature, Text: "Sign the weigh-off", Meaning: "verified"}).FieldErrors(DestKiosk); len(f) != 0 {
		t.Fatalf("verified signature: %v", f)
	}
	if f := doc(Instruction{Seq: 1, Category: InstSignature, Text: "Sign", Meaning: "liked"}).FieldErrors(DestKiosk); f["recipe.procedure.unitProcedures[0].operations[0].phases[0].instructions[0].meaning"] == "" {
		t.Fatalf("unknown meaning: %v", f)
	}
	if f := doc(Instruction{Seq: 1, Category: InstText, Text: "Read", Meaning: "approved"}).FieldErrors(DestKiosk); f["recipe.procedure.unitProcedures[0].operations[0].phases[0].instructions[0].meaning"] == "" {
		t.Fatalf("meaning on a text step: %v", f)
	}
}
