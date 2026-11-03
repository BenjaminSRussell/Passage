package domain

import "testing"

func TestValidateCatchWeight_AC245(t *testing.T) {
	spec := CatchWeight{Nominal: dec("10"), Min: dec("9.8"), Max: dec("11")}
	if err := ValidateCatchWeight(spec, 10, dec("95")); err == nil || err.(*CatchWeightError).Code != CodeCatchWeight {
		t.Fatalf("95 kg for 10 cases should fail with SLS-0061, got %v", err)
	}
	if err := ValidateCatchWeight(spec, 10, dec("102")); err != nil {
		t.Fatalf("102 kg for 10 cases should pass: %v", err)
	}
}

func TestLineNetFor_CatchWeight(t *testing.T) {
	spec := &CatchWeight{Nominal: dec("10"), Min: dec("9.8"), Max: dec("11")}
	got, err := LineNetFor(dec("10"), dec("2"), dec("0"), spec, nil)
	if err != nil || !got.Equal(dec("200")) {
		t.Fatalf("nominal 10 cases at 2.00: %s %v, want 200", got, err)
	}
	actual := dec("102")
	got, err = LineNetFor(dec("10"), dec("2"), dec("0"), spec, &actual)
	if err != nil || !got.Equal(dec("204")) {
		t.Fatalf("102 kg at 2.00: %s %v, want 204", got, err)
	}
}
