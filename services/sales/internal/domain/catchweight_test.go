package domain

import "testing"

func TestValidateCatchWeight_AC245(t *testing.T) {
	spec := CatchWeight{Nominal: dec("10"), Min: dec("9.8"), Max: dec("11")}
	if err := ValidateCatchWeight(spec, dec("10"), dec("95")); err == nil || err.(*CatchWeightError).Code != CodeCatchWeight {
		t.Fatalf("95 kg for 10 cases should fail with SLS-0061, got %v", err)
	}
	if err := ValidateCatchWeight(spec, dec("10"), dec("102")); err != nil {
		t.Fatalf("102 kg for 10 cases should pass: %v", err)
	}
	if err := ValidateCatchWeight(spec, dec("5"), dec("51")); err != nil {
		t.Fatalf("51 kg for the 5 cases a report delivers should pass: %v", err)
	}
	if err := ValidateCatchWeight(spec, dec("5"), dec("102")); err == nil || err.(*CatchWeightError).Code != CodeCatchWeight {
		t.Fatalf("102 kg for 5 cases is outside 49-55: %v", err)
	}
	for _, cases := range []string{"2.5", "0"} {
		if err := ValidateCatchWeight(spec, dec(cases), dec("25")); err == nil || err.(*CatchWeightError).Code != CodeCatchWeightInput {
			t.Fatalf("%s cases should fail with SLS-0062, got %v", cases, err)
		}
	}
}

func TestCatchWeightSpecValidate(t *testing.T) {
	for name, spec := range map[string]CatchWeight{
		"zero":         {},
		"min over max": {Nominal: dec("10"), Min: dec("12"), Max: dec("11")},
		"nominal low":  {Nominal: dec("9"), Min: dec("9.8"), Max: dec("11")},
		"seven places": {Nominal: dec("10"), Min: dec("9.8000001"), Max: dec("11")},
		"oversized":    {Nominal: dec("10"), Min: dec("9.8"), Max: dec("100000000000000")},
	} {
		if err := spec.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if err := (CatchWeight{Nominal: dec("10"), Min: dec("9.8"), Max: dec("11")}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLineNetFor_CatchWeight(t *testing.T) {
	spec := &CatchWeight{Nominal: dec("10"), Min: dec("9.8"), Max: dec("11")}
	got, err := LineNetFor(dec("10"), dec("0"), dec("2"), dec("0"), spec, nil)
	if err != nil || !got.Equal(dec("200")) {
		t.Fatalf("nominal 10 cases at 2.00: %s %v, want 200", got, err)
	}
	actual := dec("51")
	got, err = LineNetFor(dec("10"), dec("5"), dec("2"), dec("0"), spec, &actual)
	if err != nil || !got.Equal(dec("202")) {
		t.Fatalf("5 delivered at 51 kg and 5 at nominal: %s %v, want 202", got, err)
	}
	actual = dec("102")
	got, err = LineNetFor(dec("10"), dec("10"), dec("2"), dec("0"), spec, &actual)
	if err != nil || !got.Equal(dec("204")) {
		t.Fatalf("102 kg delivered at 2.00: %s %v, want 204", got, err)
	}
}
