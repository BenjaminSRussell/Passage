package replay

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

func doc(seq int, id, date string, amount string) domain.ReplayDocument {
	a := decimal.RequireFromString(amount)
	return domain.ReplayDocument{Seq: seq, DocumentID: id, PostingDate: date, Currency: "USD",
		Lines: []domain.ReplayLine{{Account: "1100", Amount: a}, {Account: "4000", Amount: a.Neg()}}}
}

func TestRun_PostsInSequenceAndStopsOnTransientFailure(t *testing.T) {
	ctx := testkit.Ctx(testkit.TenantID(1))
	f := ledgerclient.NewFake()
	f.AddLedger("L", "le")
	in := Request{Period: "2026-05", System: domain.SourceSAPECC, LegalEntityID: "le", LedgerID: "L",
		Documents: []domain.ReplayDocument{doc(2, "B", "2026-05-02", "20"), doc(1, "A", "2026-05-02", "10"), doc(3, "C", "2026-05-01", "5")}}
	res, err := Run(ctx, f, in)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Entries()
	if len(got) != 3 || got[0].Entry.DocumentID != "C" || got[1].Entry.DocumentID != "A" || got[2].Entry.DocumentID != "B" {
		t.Fatalf("posting order %v", got)
	}
	if len(res.Input.Passage) != 2 || !res.Input.Passage[0].Passage.Equal(decimal.NewFromInt(35)) || res.Input.DocumentsReplayed != 3 {
		t.Fatalf("passage tb %+v", res.Input.Passage)
	}
	f.FailPosts(errors.New("connection reset"))
	in.Period, in.Documents = "2026-06", []domain.ReplayDocument{doc(1, "D", "2026-06-01", "1")}
	if _, err := Run(ctx, f, in); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("transient failure: %v", err)
	}
	in.LegalEntityID = "other"
	if _, err := Run(context.Background(), f, in); !errors.Is(err, ErrLedgerForeign) {
		t.Fatalf("foreign ledger: %v", err)
	}
}
