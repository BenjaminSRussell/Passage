package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// codeOf returns the stable domain code of err, or "" when err is not a *Error.
func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestAbcThresholdsValidate(t *testing.T) {
	if err := DefaultAbcThresholds().Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	bad := AbcThresholds{A: d("80"), B: d("15"), C: d("4")}
	if got := codeOf(bad.Validate()); got != CodeABCThresholds {
		t.Fatalf("code %q, want %s", got, CodeABCThresholds)
	}
}

func classesOf(ranks []AbcRank) map[string]string {
	out := map[string]string{}
	for _, r := range ranks {
		out[r.ProductID] = r.Class
	}
	return out
}

func TestClassifyABCTable(t *testing.T) {
	th := DefaultAbcThresholds()
	cases := []struct {
		name  string
		items []AbcItem
		want  map[string]string
	}{
		{
			name: "five equal items: the fifth starts exactly at the 80% boundary and stays A",
			items: func() []AbcItem {
				var items []AbcItem
				for i := 0; i < 5; i++ {
					items = append(items, AbcItem{ProductID: fmt.Sprintf("big%02d", i), Value: d("100")})
				}
				return items
			}(),
			want: map[string]string{"big00": "A", "big01": "A", "big02": "A", "big03": "A", "big04": "A"},
		},
		{
			name: "boundary tie goes to higher class",
			items: []AbcItem{
				{ProductID: "a", Value: d("80")},
				{ProductID: "b", Value: d("15")},
				{ProductID: "c", Value: d("5")},
			},
			// b starts at exactly 80% -> A (higher class); c starts at exactly 95% -> B (higher class).
			want: map[string]string{"a": "A", "b": "A", "c": "B"},
		},
		{
			name: "item starting above 95 percent is C",
			items: []AbcItem{
				{ProductID: "a", Value: d("60")},
				{ProductID: "b", Value: d("30")},
				{ProductID: "c", Value: d("6")},
				{ProductID: "d", Value: d("4")},
			},
			// a starts at 0 (A), b at 60 (A), c at 90 (B), d at 96 (C).
			want: map[string]string{"a": "A", "b": "A", "c": "B", "d": "C"},
		},
		{
			name: "zero value goes to zero class",
			items: []AbcItem{
				{ProductID: "a", Value: d("10")},
				{ProductID: "z", Value: d("0")},
			},
			want: map[string]string{"a": "A", "z": "C"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classesOf(ClassifyABC(tc.items, th, ABCC))
			for id, want := range tc.want {
				if got[id] != want {
					t.Fatalf("%s: class %q, want %q (all %v)", id, got[id], want, got)
				}
			}
		})
	}
}

func TestClassifyABCZeroClassN(t *testing.T) {
	got := classesOf(ClassifyABC([]AbcItem{{ProductID: "z", Value: d("0")}}, DefaultAbcThresholds(), ABCN))
	if got["z"] != ABCN {
		t.Fatalf("class %q, want N", got["z"])
	}
}

func TestClassifyABCPropertiesHold(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		values := rapid.SliceOfN(rapid.Int64Range(0, 10000), 1, 200).Draw(rt, "values")
		items := make([]AbcItem, len(values))
		total := decimal.Zero
		for i, v := range values {
			items[i] = AbcItem{ProductID: fmt.Sprintf("p%03d", i), Site: "S1", Value: decimal.NewFromInt(v)}
			total = total.Add(decimal.NewFromInt(v))
		}
		ranks := ClassifyABC(items, DefaultAbcThresholds(), ABCC)
		if len(ranks) != len(items) {
			rt.Fatalf("lost items: %d of %d", len(ranks), len(items))
		}
		order := map[string]int{ABCA: 0, ABCB: 1, ABCC: 2, ABCN: 3}
		sumA := decimal.Zero
		prev := -1
		for _, r := range ranks {
			if order[r.Class] < prev {
				rt.Fatalf("class %s after a lower class: not monotone", r.Class)
			}
			prev = order[r.Class]
			if r.Class == ABCA {
				sumA = sumA.Add(r.Value)
			}
		}
		if total.IsPositive() && sumA.Mul(decimal.NewFromInt(100)).LessThan(total.Mul(decimal.NewFromInt(80))) {
			rt.Fatalf("class A holds %s of %s, below 80%%", sumA, total)
		}
	})
}
