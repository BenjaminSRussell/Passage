package domain

import (
	"errors"
	"testing"
)

func TestMapPrimary(t *testing.T) {
	ranges := []AccountRange{
		{ElementID: "E-PAYROLL", From: "600000", To: "609999"},
		{ElementID: "E-SUPPLIES", From: "610000", To: "619999"},
	}
	cases := []struct {
		account string
		want    string
		err     error
	}{
		{"600000", "E-PAYROLL", nil},
		{"609999", "E-PAYROLL", nil},
		{"610500", "E-SUPPLIES", nil},
		{"700000", "", ErrNoPrimaryElement},
	}
	for _, c := range cases {
		got, err := MapPrimary(ranges, c.account)
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("MapPrimary(%s) = %q, %v; want %q, %v", c.account, got, err, c.want, c.err)
		}
	}
}

func TestAccountRangeOverlapAndValidation(t *testing.T) {
	a := AccountRange{From: "600000", To: "609999"}
	cases := []struct {
		b       AccountRange
		overlap bool
	}{
		{AccountRange{From: "609999", To: "610000"}, true},
		{AccountRange{From: "610000", To: "619999"}, false},
		{AccountRange{From: "500000", To: "600000"}, true},
	}
	for _, c := range cases {
		if got := a.Overlaps(c.b); got != c.overlap {
			t.Errorf("overlap with %v = %v, want %v", c.b, got, c.overlap)
		}
	}
	if err := ValidateRange(AccountRange{From: "7", To: "6"}); err == nil {
		t.Error("reversed range accepted")
	}
}

func TestValidators(t *testing.T) {
	periods := map[string]bool{"2026-09": true, "2026-13": false, "2026-00": false, "26-09": false}
	for p, want := range periods {
		if ValidPeriod(p) != want {
			t.Errorf("ValidPeriod(%q) != %v", p, want)
		}
	}
	kinds := map[string]bool{KindCostCenter: true, KindInternalOrder: true, "Asset": false}
	for k, want := range kinds {
		if ValidObjectKind(k) != want {
			t.Errorf("ValidObjectKind(%q) != %v", k, want)
		}
	}
	if ValidCode("cc-10") || !ValidCode("CC-10") {
		t.Error("ValidCode must require upper case codes")
	}
}
