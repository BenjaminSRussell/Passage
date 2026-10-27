package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestSelectConfirmations_ThresholdAndZeroBalances(t *testing.T) {
	bal := []CustomerBalance{
		{"c1", "USD", decimal.NewFromInt(10000)},
		{"c2", "USD", decimal.RequireFromString("9999.99")},
		{"c3", "USD", decimal.NewFromInt(-12000)},
		{"c4", "USD", decimal.Zero},
	}
	got := SelectConfirmations(bal, decimal.NewFromInt(10000), 0, false, "seed")
	if len(got) != 2 || got[0].CustomerID != "c1" || got[1].CustomerID != "c3" {
		t.Fatalf("threshold selection = %+v, want c1 (at threshold) and c3 (credit balance)", got)
	}
	withZero := SelectConfirmations(bal, decimal.NewFromInt(10000), 0, true, "seed")
	if len(withZero) != 3 || withZero[2].CustomerID != "c4" {
		t.Fatalf("include_zero_balances = %+v, want c1, c3, c4", withZero)
	}
	// A zero threshold still leaves zero balances out unless asked (BR-30.13).
	if all := SelectConfirmations(bal, decimal.Zero, 0, false, "seed"); len(all) != 3 {
		t.Fatalf("zero threshold = %+v, want the three non-zero balances", all)
	}
}

func TestSelectConfirmations_SampleIsReproducibleFromSeed(t *testing.T) {
	var bal []CustomerBalance
	for i := 0; i < 40; i++ {
		bal = append(bal, CustomerBalance{fmt.Sprintf("c%02d", i), "EUR", decimal.NewFromInt(int64(100 + i))})
	}
	a := SelectConfirmations(bal, decimal.Zero, 5, false, "run-1")
	b := SelectConfirmations(bal, decimal.Zero, 5, false, "run-1")
	c := SelectConfirmations(bal, decimal.Zero, 5, false, "run-2")
	if len(a) != 5 || fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("same seed gave different samples: %v vs %v", a, b)
	}
	if fmt.Sprint(a) == fmt.Sprint(c) {
		t.Fatalf("different seeds gave the same 5 of 40: %v", a)
	}
}

func TestSelectConfirmations_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 40).Draw(t, "n")
		sample := rapid.IntRange(0, 45).Draw(t, "sample")
		includeZero := rapid.Bool().Draw(t, "includeZero")
		var bal []CustomerBalance
		qualifying := 0
		threshold := decimal.NewFromInt(10000)
		for i := 0; i < n; i++ {
			v := decimal.NewFromInt(rapid.Int64Range(-20000, 20000).Draw(t, "balance"))
			bal = append(bal, CustomerBalance{fmt.Sprintf("c%03d", i), "USD", v})
			if (v.IsZero() && includeZero) || (!v.IsZero() && v.Abs().GreaterThanOrEqual(threshold)) {
				qualifying++
			}
		}
		got := SelectConfirmations(bal, threshold, sample, includeZero, "seed")
		want := qualifying
		if sample > 0 && sample < qualifying {
			want = sample
		}
		if len(got) != want {
			t.Fatalf("selected %d rows, want %d (qualifying %d, sample %d)", len(got), want, qualifying, sample)
		}
		for i, g := range got {
			if !g.Balance.IsZero() && g.Balance.Abs().LessThan(threshold) {
				t.Fatalf("row below threshold: %+v", g)
			}
			if i > 0 && got[i-1].CustomerID >= g.CustomerID {
				t.Fatalf("result not sorted by customer: %v", got)
			}
		}
	})
}

func TestReplyBalance(t *testing.T) {
	ours := decimal.RequireFromString("12000.00")
	other := decimal.RequireFromString("11500.00")
	same := decimal.RequireFromString("12000")
	tests := []struct {
		name   string
		reply  string
		theirs *decimal.Decimal
		want   string
		code   string
	}{
		{"agreed without a balance records ours", ReplyAgreed, nil, "12000", ""},
		{"agreed with the same balance", ReplyAgreed, &same, "12000", ""},
		{"agreed with another balance is refused", ReplyAgreed, &other, "", CodeConfirmationReply},
		{"disagreed records theirs", ReplyDisagreed, &other, "11500", ""},
		{"disagreed needs a balance", ReplyDisagreed, nil, "", CodeConfirmationReply},
		{"unknown reply", ReplyNone, nil, "", CodeConfirmationReply},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReplyBalance(tc.reply, ours, tc.theirs)
			if tc.code != "" {
				if err == nil || err.Code != tc.code {
					t.Fatalf("err = %v, want %s", err, tc.code)
				}
				return
			}
			if err != nil || !got.Equal(decimal.RequireFromString(tc.want)) {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}
	if d := BalanceDifference(ours, other); !d.Equal(decimal.RequireFromString("-500")) {
		t.Fatalf("difference = %s, want -500", d)
	}
}
