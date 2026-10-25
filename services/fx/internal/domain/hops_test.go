package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestResolveHops(t *testing.T) {
	b := testBook(t)
	type hop struct {
		from, to, rate string
		validFrom      time.Time
	}
	cases := []struct {
		name, typ, from, to, ref string
		on                       string
		path                     Path
		want                     []hop
	}{
		{"direct", "SPOT", "EUR", "USD", "", "2026-03-15", PathDirect, []hop{{"EUR", "USD", "1.085", date("2026-03-01")}}},
		{"inverse", "SPOT", "USD", "EUR", "", "2026-03-15", PathInverse, []hop{{"USD", "EUR", decimal.NewFromInt(1).Div(dec("1.085")).String(), date("2026-03-01")}}},
		{"triangulated", "SPOT", "GBP", "CHF", "EUR", "2026-03-15", PathTriangulated, []hop{{"GBP", "EUR", "1.17", date("2026-03-01")}, {"EUR", "CHF", "0.95", date("2026-03-01")}}},
		{"same currency", "SPOT", "EUR", "EUR", "", "2026-03-15", PathDirect, []hop{{"EUR", "EUR", "1", date("2026-03-15")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := b.Resolve(tc.typ, tc.from, tc.to, date(tc.on), tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if res.Path != tc.path || len(res.Hops) != len(tc.want) || len(res.Factors) != len(tc.want) {
				t.Fatalf("got path %s hops %d factors %d", res.Path, len(res.Hops), len(res.Factors))
			}
			for i, w := range tc.want {
				h := res.Hops[i]
				if h.From != w.from || h.To != w.to || !h.Rate.Equal(dec(w.rate)) || !h.ValidFrom.Equal(w.validFrom) || !res.Factors[i].Equal(h.Rate) {
					t.Fatalf("hop %d = %+v, want %+v", i, h, w)
				}
			}
		})
	}
}
