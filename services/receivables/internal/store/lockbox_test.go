package store

import (
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

func TestImportLockbox_PersistsChecksAndRefusesDuplicate(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(3)
	sha := strings.Repeat("a", 64)
	in := LockboxImport{LegalEntityID: legalEntity, FileSHA256: sha, ReceivedDate: "2027-03-05", AccountCount: 1,
		Checks: []domain.LockboxCheck{
			{Line: 4, AccountNo: "123456789", Currency: "USD", Amount: amt("1250.00"), BankRef: "REF1", CustomerRef: "INV-30001"},
			{Line: 5, AccountNo: "123456789", Currency: "USD", Amount: amt("75.10")},
		}}

	batch, err := s.ImportLockbox(ctx, in)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if batch.PaymentCount != 2 || len(batch.Payments) != 2 {
		t.Fatalf("batch = %+v, want two payments", batch)
	}
	for _, p := range batch.Payments {
		if p.Source != "LOCKBOX" || p.Status != "RECEIVED" || p.CustomerID != nil || p.SourceRef == nil {
			t.Errorf("payment = %+v, want an unidentified LOCKBOX receipt with a source ref", p)
		}
	}
	if batch.Payments[0].SourceRef == nil || *batch.Payments[0].SourceRef != "123456789:REF1" {
		t.Errorf("source ref = %v, want the bank reference keyed by account", batch.Payments[0].SourceRef)
	}
	if len(batch.Payments[0].Remittance) != 1 || batch.Payments[0].Remittance[0].Reference != "INV-30001" {
		t.Errorf("remittance = %+v", batch.Payments[0].Remittance)
	}

	_, err = s.ImportLockbox(ctx, in)
	if got := problemCode(err); got != CodeLockboxDuplicate {
		t.Fatalf("second import code = %q (%v), want %s", got, err, CodeLockboxDuplicate)
	}
	batches, err := s.ListLockboxBatches(ctx, 50)
	if err != nil || len(batches) != 1 || batches[0].PaymentCount != 2 || batches[0].ImportedAt == nil || batches[0].Payments != nil {
		t.Fatalf("batches = %+v (%v), want the one import without its payments", batches, err)
	}
	if got := countOutbox(t, s, ctx, "dev.passage.receivables.payment.received.v1"); got != 2 {
		t.Errorf("payment.received events = %d, want one per check", got)
	}
}

func TestWorklist_RanksOverdueCustomer(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(4)
	item := seedInvoice(t, s, ctx) // due 2027-03-31, 1,000.00 open

	rows, err := s.Worklist(ctx, legalEntity, time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("worklist: %v", err)
	}
	if len(rows) != 1 || rows[0].CustomerID != item.CustomerID {
		t.Fatalf("rows = %+v, want the one seeded customer", rows)
	}
	r := rows[0]
	if !r.Overdue.Equal(amt("1000.00")) || r.OldestDays != 31 || r.RiskScore <= 0 {
		t.Errorf("row = %+v, want 1,000.00 overdue, 31 days, positive score", r)
	}

	current, err := s.Worklist(ctx, legalEntity, time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("worklist: %v", err)
	}
	if len(current) != 1 || !current[0].Overdue.IsZero() || current[0].RiskScore != 0 {
		t.Errorf("before due date: %+v, want no overdue and score 0", current)
	}
}
