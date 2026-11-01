package ledgerclient_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
)

func TestFake_PostIsIdempotentPerKeyAndRejectsAChangedPayload(t *testing.T) {
	f := ledgerclient.NewFake()
	first, err := f.Post(principalCtx(), entry())
	again, err2 := f.Post(principalCtx(), entry())
	if err != nil || err2 != nil || first != again || len(f.Entries()) != 1 {
		t.Fatalf("first %q again %q errs %v %v entries %d", first, again, err, err2, len(f.Entries()))
	}
	changed := entry()
	changed.Lines[0].Amount, changed.Lines[1].Amount = decimal.NewFromInt(7), decimal.NewFromInt(7)
	if _, err := f.Post(principalCtx(), changed); ledgerclient.CodeOf(err) != ledgerclient.CodeIdempotencyConflict {
		t.Fatalf("err %v", err)
	}
}

func TestFake_RefusesAnUnbalancedEntry(t *testing.T) {
	bad := entry()
	bad.Lines[0].Amount = decimal.NewFromInt(1)
	if _, err := ledgerclient.NewFake().Post(principalCtx(), bad); !errors.Is(err, ledgerclient.ErrUnbalanced) {
		t.Fatalf("err %v", err)
	}
}

func TestFake_ReverseFlipsTheSidesOnceAndNetsTheBalanceToZero(t *testing.T) {
	f := ledgerclient.NewFake()
	id, _ := f.Post(principalCtx(), entry())
	q := ledgerclient.BalanceQuery{LedgerID: ledgerID, Account: "1400"}
	if b, _ := f.Balance(principalCtx(), q); !b.Net().Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("before %+v", b)
	}
	r := ledgerclient.Reversal{LegalEntityID: entityID, EntryID: id, IdempotencyKey: "rev-1"}
	rev, err := f.Reverse(principalCtx(), r)
	replay, err2 := f.Reverse(principalCtx(), r)
	if err != nil || err2 != nil || rev != replay {
		t.Fatalf("rev %q replay %q errs %v %v", rev, replay, err, err2)
	}
	if b, _ := f.Balance(principalCtx(), q); !b.Net().IsZero() {
		t.Fatalf("after %+v", b)
	}
	other := r
	other.IdempotencyKey = "rev-2"
	if _, err := f.Reverse(principalCtx(), other); ledgerclient.CodeOf(err) != ledgerclient.CodeAlreadyReversed {
		t.Fatalf("second reversal: %v", err)
	}
	if _, err := f.Reverse(principalCtx(), ledgerclient.Reversal{EntryID: "missing", IdempotencyKey: "x"}); ledgerclient.CodeOf(err) != ledgerclient.CodeEntryNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestFake_BalanceHonoursLedgerAndPeriod(t *testing.T) {
	f := ledgerclient.NewFake()
	_, _ = f.Post(principalCtx(), entry())
	late := entry()
	late.IdempotencyKey, late.PostingDate = "key-2", "2026-12-01"
	_, _ = f.Post(principalCtx(), late)
	if b, _ := f.Balance(principalCtx(), ledgerclient.BalanceQuery{LedgerID: ledgerID, Account: "1400", PeriodTo: "2026-10"}); !b.Net().Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("period %+v", b)
	}
	if b, _ := f.Balance(principalCtx(), ledgerclient.BalanceQuery{LedgerID: "other", Account: "1400"}); !b.Net().IsZero() {
		t.Fatalf("ledger %+v", b)
	}
}

func TestFake_FailPostsInjectsTheLedgerAnswer(t *testing.T) {
	f := ledgerclient.NewFake()
	f.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	if _, err := f.Post(principalCtx(), entry()); ledgerclient.CodeOf(err) != ledgerclient.CodePeriodClosed || len(f.Entries()) != 0 {
		t.Fatalf("err %v", err)
	}
	f.FailPosts(nil)
	if _, err := f.Post(principalCtx(), entry()); err != nil {
		t.Fatal(err)
	}
}
