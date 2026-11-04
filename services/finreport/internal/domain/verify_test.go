package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func nets(kv ...any) map[string]decimal.Decimal {
	m := map[string]decimal.Decimal{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = decimal.RequireFromString(kv[i+1].(string))
	}
	return m
}

func TestVerify_MatchedIgnoresMissingZeroAccounts(t *testing.T) {
	res := Verify(nets("1000", "10.5", "2000", "0"), nets("1000", "10.500000", "3000", "0"), 7, 7, 0)
	if res.Status != VerifyMatched || res.DiffCount != 0 || res.AccountsCompared != 3 || len(res.Diffs) != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestVerify_DifferenceIsReadModelMinusLedgerAndMissingSideIsZero(t *testing.T) {
	res := Verify(nets("1000", "10", "4000", "-5"), nets("1000", "12.25", "5000", "3"), 4, 4, 0)
	if res.Status != VerifyDiffers || res.DiffCount != 3 {
		t.Fatalf("got %+v", res)
	}
	want := []AccountDiff{
		{"1000", decimal.RequireFromString("10"), decimal.RequireFromString("12.25"), decimal.RequireFromString("-2.25")},
		{"4000", decimal.RequireFromString("-5"), decimal.Zero, decimal.RequireFromString("-5")},
		{"5000", decimal.Zero, decimal.RequireFromString("3"), decimal.RequireFromString("-3")},
	}
	for i, w := range want {
		g := res.Diffs[i]
		if g.Account != w.Account || !g.ReadModel.Equal(w.ReadModel) || !g.Ledger.Equal(w.Ledger) || !g.Difference.Equal(w.Difference) {
			t.Errorf("diff %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestVerify_MovedMarkIsInconclusiveEvenWhenEqual(t *testing.T) {
	if res := Verify(nets("1", "1"), nets("1", "2"), 3, 4, 0); res.Status != VerifyInconclusive || res.DiffCount != 1 {
		t.Fatalf("got %+v", res)
	}
	if res := Verify(nets("1", "1"), nets("1", "1"), 3, 4, 0); res.Status != VerifyInconclusive {
		t.Fatalf("equal but moved: got %+v", res)
	}
}

func TestVerify_EntriesWaitingAheadOfTheMarkAreInconclusive(t *testing.T) {
	res := Verify(nets("1000", "10"), nets("1000", "12"), 5, 5, 1)
	if res.Status != VerifyInconclusive || res.EntriesPending != 1 || res.DiffCount != 1 {
		t.Fatalf("got %+v", res)
	}
}

func TestVerify_CapsStoredDiffsButCountsAll(t *testing.T) {
	rm := map[string]decimal.Decimal{}
	for i := 0; i < MaxVerifyDiffs+25; i++ {
		rm[string(rune('A'+i/26/26%26))+string(rune('A'+i/26%26))+string(rune('A'+i%26))] = decimal.NewFromInt(1)
	}
	res := Verify(rm, nil, 1, 1, 0)
	if res.DiffCount != MaxVerifyDiffs+25 || len(res.Diffs) != MaxVerifyDiffs {
		t.Fatalf("count %d stored %d", res.DiffCount, len(res.Diffs))
	}
}

func TestVerify_PropertyIdenticalMapsMatchAndOneChangeDiffers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := rapid.MapOfN(rapid.StringMatching(`[0-9]{4}`), rapid.Int64Range(-1e9, 1e9), 1, 40).Draw(t, "nets")
		a, b := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
		for k, v := range m {
			a[k], b[k] = decimal.New(v, -2), decimal.New(v, -2)
		}
		if res := Verify(a, b, 1, 1, 0); res.Status != VerifyMatched {
			t.Fatalf("identical maps: %+v", res)
		}
		for k := range b {
			b[k] = b[k].Add(decimal.New(1, -6))
			break
		}
		if res := Verify(a, b, 1, 1, 0); res.Status != VerifyDiffers || res.DiffCount != 1 {
			t.Fatalf("one changed: %+v", res)
		}
	})
}
