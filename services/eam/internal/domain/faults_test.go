package domain

import (
	"errors"
	"testing"
)

func validCatalog() FaultCatalog {
	return FaultCatalog{
		Symptoms:     []FaultCode{{"NOISE", "Abnormal noise"}, {"LEAK", "Leak"}},
		Areas:        []FaultCode{{"BEARING", "Bearing"}},
		Types:        []FaultCode{{"WEAR", "Wear"}},
		Causes:       []FaultCode{{"FATIGUE", "Fatigue"}},
		Remedies:     []FaultCode{{"REPLACE", "Replace"}},
		Combinations: []FaultCombination{{"NOISE", "BEARING", "WEAR"}},
	}
}

func TestCheckCatalog(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*FaultCatalog)
		ok     bool
	}{
		{"valid", func(*FaultCatalog) {}, true},
		{"empty causes", func(c *FaultCatalog) { c.Causes = nil }, false},
		{"lowercase code", func(c *FaultCatalog) { c.Symptoms[0].Code = "noise" }, false},
		{"blank text", func(c *FaultCatalog) { c.Remedies[0].Text = " " }, false},
		{"duplicate code", func(c *FaultCatalog) { c.Symptoms = append(c.Symptoms, FaultCode{"NOISE", "Again"}) }, false},
		{"no combinations", func(c *FaultCatalog) { c.Combinations = nil }, false},
		{"combination unknown area", func(c *FaultCatalog) { c.Combinations[0].Area = "GEAR" }, false},
		{"duplicate combination", func(c *FaultCatalog) { c.Combinations = append(c.Combinations, c.Combinations[0]) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCatalog()
			tc.mutate(&c)
			err := CheckCatalog(c)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrFaultCatalog) {
				t.Fatalf("err = %v, want ErrFaultCatalog", err)
			}
		})
	}
}

func TestCombinationAllowed(t *testing.T) {
	allowed := []FaultCombination{{"NOISE", "BEARING", "WEAR"}}
	if !CombinationAllowed(allowed, FaultCombination{"NOISE", "BEARING", "WEAR"}) {
		t.Fatal("listed combination should be allowed")
	}
	// AC-44.15: a combination not allowed for the asset type is rejected.
	if CombinationAllowed(allowed, FaultCombination{"LEAK", "BEARING", "WEAR"}) {
		t.Fatal("unlisted combination should not be allowed")
	}
	if CombinationAllowed(nil, FaultCombination{"NOISE", "BEARING", "WEAR"}) {
		t.Fatal("empty catalog allows nothing")
	}
}

func TestFaultRequired(t *testing.T) {
	cases := map[string]bool{
		"CORRECTIVE": true, "EMERGENCY": true,
		"PREVENTIVE": false, "INSPECTION": false, "CAPITAL": false, "ROUND": false, "REFURBISHMENT": false,
	}
	for wt, want := range cases {
		if got := FaultRequired(wt); got != want {
			t.Errorf("FaultRequired(%s) = %v, want %v", wt, got, want)
		}
	}
}

func TestCheckFault_AC4415(t *testing.T) {
	c := validCatalog()
	ok := Fault{Symptom: "NOISE", Area: "BEARING", Type: "WEAR", Cause: "FATIGUE", Remedy: "REPLACE"}
	if err := CheckFault(c, ok); err != nil {
		t.Fatalf("allowed fault refused: %v", err)
	}
	for name, f := range map[string]Fault{
		"combination not allowed": {Symptom: "LEAK", Area: "BEARING", Type: "WEAR", Cause: "FATIGUE", Remedy: "REPLACE"},
		"unknown cause":           {Symptom: "NOISE", Area: "BEARING", Type: "WEAR", Cause: "MISUSE", Remedy: "REPLACE"},
		"unknown remedy":          {Symptom: "NOISE", Area: "BEARING", Type: "WEAR", Cause: "FATIGUE", Remedy: "PAINT"},
		"area used as a cause":    {Symptom: "NOISE", Area: "BEARING", Type: "WEAR", Cause: "BEARING", Remedy: "REPLACE"},
	} {
		if err := CheckFault(c, f); !errors.Is(err, ErrFaultNotAllowed) {
			t.Errorf("%s: err = %v, want ErrFaultNotAllowed", name, err)
		}
	}
}

func TestCheckCatalog_SizeLimit(t *testing.T) {
	c := validCatalog()
	for i := 0; len(c.Causes) <= MaxFaultCodes; i++ {
		c.Causes = append(c.Causes, FaultCode{Code: "C" + string(rune('A'+i%26)) + string(rune('A'+i/26)), Text: "x"})
	}
	if err := CheckCatalog(c); !errors.Is(err, ErrFaultCatalog) {
		t.Fatalf("oversized catalog: err = %v", err)
	}
}
