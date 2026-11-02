package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const sheetTable = "receiving_service_entry_sheet"

func sampleSheet(id, legalEntityID string, amount string) store.ServiceSheet {
	a := decimal.RequireFromString(amount)
	return store.ServiceSheet{
		ID: id, LegalEntityID: legalEntityID, VendorID: vendorID, PurchaseOrderID: poID, Currency: "USD", Total: a, CreatedBy: "alice",
		Lines: []domain.SheetLine{{POLineNo: 1, Description: "Consulting", Unit: "H", Qty: decimal.NewFromInt(1), UnitPrice: a, Amount: a}},
	}
}

func sheetEvent(s store.ServiceSheet) (events.Event, error) {
	return events.New("dev.passage.receiving.serviceentrysheet.decided.v1", "/passage/receiving/receiving", s.ID, testkit.TenantID(1), map[string]string{"decision": s.Status})
}

func TestServiceSheet_CapDecisionSegregationAndIsolation(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	limits := map[int]decimal.Decimal{1: decimal.NewFromInt(100)}
	if _, err := st.CreateSheet(ctx, sampleSheet(payeeOne, db.one.LegalEntityID, "70"), limits); err != nil {
		t.Fatal(err)
	}
	var de *domain.Error
	if _, err := st.CreateSheet(ctx, sampleSheet(payeeOther, db.one.LegalEntityID, "40"), limits); !errors.As(err, &de) || de.Code != domain.CodeSheetOverLine {
		t.Fatalf("over the cap: %v", err)
	}
	if _, err := st.DecideSheet(ctx, payeeOne, "alice", true, "", sheetEvent); !errors.As(err, &de) || de.Code != domain.CodeSheetSelfAccept {
		t.Fatalf("author decided: %v", err)
	}
	rejected, err := st.DecideSheet(ctx, payeeOne, "bob", false, "not performed", sheetEvent)
	if err != nil || rejected.Status != domain.SheetRejected || rejected.RejectReason != "not performed" || rejected.DecidedAt == nil {
		t.Fatalf("reject: %+v %v", rejected, err)
	}
	if _, err := st.DecideSheet(ctx, payeeOne, "bob", true, "", sheetEvent); !errors.As(err, &de) || de.Code != domain.CodeSheetNotPending {
		t.Fatalf("decided twice: %v", err)
	}
	if _, err := st.CreateSheet(ctx, sampleSheet(payeeOther, db.one.LegalEntityID, "100"), limits); err != nil {
		t.Fatalf("a rejected sheet still holds the cap: %v", err)
	}
	got, err := st.GetSheet(ctx, payeeOther)
	if err != nil || got.Status != domain.SheetPending || len(got.Lines) != 1 || got.Lines[0].Amount.String() != "100" {
		t.Fatalf("read %+v %v", got, err)
	}
	var outbox int
	outbox = db.rows(db.one, "outbox")
	if outbox != 1 {
		t.Fatalf("%d outbox rows, want 1 (only the committed decision)", outbox)
	}
	if _, err := st.GetSheet(tenant.With(context.Background(), principal(2)), payeeOther); !errors.Is(err, store.ErrSheetNotFound) {
		t.Fatalf("another tenant read the sheet: %v", err)
	}
	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	if _, err := st.GetSheet(tenant.With(context.Background(), otherEntity), payeeOther); !errors.Is(err, store.ErrSheetNotFound) {
		t.Fatalf("another legal entity read the sheet: %v", err)
	}
	for name, sql := range map[string]string{
		"a delete":                `delete from ` + sheetTable,
		"an update of the lines":  `update ` + sheetTable + ` set lines = '[]'`,
		"an update of the total":  `update ` + sheetTable + ` set total = 0`,
		"an update of the author": `update ` + sheetTable + ` set created_by = 'bob'`,
	} {
		if err := db.run(db.one, sql); err == nil {
			t.Errorf("passage_app must not run %s", name)
		}
	}
}
