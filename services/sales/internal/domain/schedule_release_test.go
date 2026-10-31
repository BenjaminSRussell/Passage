package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func s(v string) *string { return &v }

func TestOpenReleaseLines_AC2713(t *testing.T) {
	res, err := OpenReleaseLines(decimal.NewFromInt(1200), []ReleaseBucket{{Date: "2027-04-15", CumRequired: decimal.NewFromInt(1500)}})
	if err != nil || res.Mismatch || !res.OpenQuantity.Equal(decimal.NewFromInt(300)) || len(res.Lines) != 1 || !res.Lines[0].Quantity.Equal(decimal.NewFromInt(300)) {
		t.Fatalf("%v %+v", err, res)
	}
	res, err = OpenReleaseLines(decimal.NewFromInt(2000), []ReleaseBucket{{Date: "2027-04-15", CumRequired: decimal.NewFromInt(1500)}})
	if err != nil || len(res.Lines) != 0 || !res.OpenQuantity.IsZero() {
		t.Fatalf("over-shipped: %v %+v", err, res)
	}
	if _, err := OpenReleaseLines(decimal.NewFromInt(-1), nil); err == nil {
		t.Fatal("negative shipped must be refused")
	}
}

func TestCheckShippedUpdate(t *testing.T) {
	n := decimal.NewFromInt
	cases := []struct {
		name       string
		prev, next int64
		pr, nr     *string
		ok         bool
	}{
		{"increase", 100, 150, nil, nil, true},
		{"same", 100, 100, nil, nil, true},
		{"decrease", 100, 50, nil, nil, false},
		{"decrease with same reset", 100, 50, s("2027-01-01"), s("2027-01-01"), false},
		{"first reset may lower", 100, 0, nil, s("2027-01-01"), true},
		{"later reset may lower", 100, 0, s("2026-01-01"), s("2027-01-01"), true},
		{"reset moves back", 100, 100, s("2027-01-01"), s("2026-01-01"), false},
		{"negative", 100, -1, nil, nil, false},
	}
	for _, c := range cases {
		err := CheckShippedUpdate(n(c.prev), n(c.next), c.pr, c.nr)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v ok=%v", c.name, err, c.ok)
		}
	}
}
