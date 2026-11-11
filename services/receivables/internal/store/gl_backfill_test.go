package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

type journalRow struct {
	sourceType, key, date string
	debits, credits       map[string]string
}

func journals(t *testing.T, s *Postgres, ctx context.Context) []journalRow {
	t.Helper()
	var out []journalRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_type, source_key, posting_date::text, lines from gl_journal order by created_at, source_key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r journalRow
			var raw []byte
			if err := rows.Scan(&r.sourceType, &r.key, &r.date, &raw); err != nil {
				return err
			}
			r.debits, r.credits = map[string]string{}, map[string]string{}
			for _, l := range decodeJournalLines(t, raw) {
				if l.Credit {
					r.credits[l.Account] = l.Amount.String()
				} else {
					r.debits[l.Account] = l.Amount.String()
				}
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// 30-2 backfill: a payment recorded and applied before the journals existed gets its receipt and its application journal; the
// payments that already have theirs, and ones that book no cash, get nothing; a repeat queues nothing; and the opening journal
// books the imported open amount against the account named.
func TestBackfill_QueuesWhatPreDatesTheJournalsAndNothingTwice(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &item.CustomerID, PaymentMode: "WIRE", Source: "MANUAL",
		Amount: amt("980.00"), UnappliedAmount: amt("980.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "bf-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("980.00"), CashDiscount: amt("20.00")}}}, "bf-apply"); err != nil {
		t.Fatal(err)
	}
	// Make the payment look like it pre-dates the journals: no journal of its own survives.
	execAdmin(t, s, `delete from gl_journal`)
	// A payment that has its journals, and an advised one that books no cash.
	if _, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", Amount: amt("10.00"), UnappliedAmount: amt("10.00"),
		Currency: "USD", ReceivedDate: "2027-03-09", ValueDate: "2027-03-09"}, "bf-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRemittanceAdviceWith(ctx, legalEntity, []domain.AdviceLine{{PaymentRef: "ADV-1", References: []domain.RemittanceLine{{Reference: "INV-1", Amount: amt("5.00")}}}},
		AdviceOptions{CreateMissing: true, Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	imp, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "INVOICE", "DEBIT", "1000.00", "1000.00"), openingRow("2", "CREDIT_NOTE", "CREDIT", "200.00", "200.00")))
	if err != nil || !imp.Committed {
		t.Fatalf("import: %+v %v", imp, err)
	}
	before := len(journals(t, s, ctx))

	res, err := s.BackfillGLJournals(ctx, BackfillRequest{LegalEntityID: legalEntity})
	if err != nil || res.Receipts != 1 || res.Applications != 1 || res.OpeningItems != 0 || res.More {
		t.Fatalf("backfill = %+v, %v", res, err)
	}
	all := journals(t, s, ctx)
	if len(all) != before+2 {
		t.Fatalf("%d journals after, %d before: want two more", len(all), before)
	}
	var receipt, apply *journalRow
	for i := range all {
		switch all[i].key {
		case "receipt:" + pay.ID:
			receipt = &all[i]
		case "apply-backfill:" + pay.ID:
			apply = &all[i]
		}
	}
	if receipt == nil || receipt.debits["1000"] != "980" || receipt.credits["1205"] != "980" || receipt.date != "2027-03-08" {
		t.Fatalf("receipt journal = %+v", receipt)
	}
	if apply == nil || apply.debits["1205"] != "980" || apply.debits["4100"] != "20" || apply.credits["1200"] != "1000" {
		t.Fatalf("application journal = %+v", apply)
	}
	if again, err := s.BackfillGLJournals(ctx, BackfillRequest{LegalEntityID: legalEntity}); err != nil || again.Receipts != 0 || again.Applications != 0 {
		t.Fatalf("repeat = %+v, %v", again, err)
	}

	// Opening journals: Dr 1200 against the opening account for the net the import set (1000 debit less 200 credit).
	op, err := s.BackfillGLJournals(ctx, BackfillRequest{LegalEntityID: legalEntity, OpeningAccount: "3900", OpeningDate: "2027-01-01"})
	if err != nil || op.OpeningItems != 1 || op.OpeningQueued != 0 {
		t.Fatalf("opening = %+v, %v", op, err)
	}
	var opening *journalRow
	all = journals(t, s, ctx)
	for i := range all {
		if all[i].sourceType == JournalOpening {
			opening = &all[i]
		}
	}
	if opening == nil || opening.key != "opening:SAP-PRD:USD" || opening.debits["1200"] != "800" || opening.credits["3900"] != "800" || opening.date != "2027-01-01" {
		t.Fatalf("opening journal = %+v", opening)
	}
	if again, err := s.BackfillGLJournals(ctx, BackfillRequest{LegalEntityID: legalEntity, OpeningAccount: "3900", OpeningDate: "2027-01-01"}); err != nil || again.OpeningItems != 0 || again.OpeningQueued != 1 {
		t.Fatalf("opening repeat = %+v, %v", again, err)
	}
}

// A net credit import books the other way round.
func TestBackfill_NetCreditOpeningBooksCreditToReceivables(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	if _, err := s.ImportOpeningItems(ctx, openingBatch(openingRow("1", "CREDIT_NOTE", "CREDIT", "300.00", "300.00"), openingRow("2", "INVOICE", "DEBIT", "100.00", "100.00"))); err != nil {
		t.Fatal(err)
	}
	if res, err := s.BackfillGLJournals(ctx, BackfillRequest{LegalEntityID: legalEntity, OpeningAccount: "3900", OpeningDate: "2027-01-01"}); err != nil || res.OpeningItems != 1 {
		t.Fatalf("backfill = %+v, %v", res, err)
	}
	for _, j := range journals(t, s, ctx) {
		if j.sourceType == JournalOpening && (j.debits["3900"] != "200" || j.credits["1200"] != "200") {
			t.Fatalf("net credit opening = %+v", j)
		}
	}
}

func decodeJournalLines(t *testing.T, raw []byte) []journalLine {
	t.Helper()
	var lines []journalLine
	if err := json.Unmarshal(raw, &lines); err != nil {
		t.Fatal(err)
	}
	return lines
}
