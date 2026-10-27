package store_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// blanket is an EFFECTIVE agreement for 2027 with a HARD QUANTITY line of
// 1,000 EA and a SOFT TOTAL_VALUE line of 500.
func blanket(t *testing.T, vendor, product string) domain.Agreement {
	to := day(t, "2027-12-31")
	return domain.Agreement{LegalEntityID: legalEntity, VendorID: vendor, Currency: "USD", ValidFrom: day(t, "2027-01-01"), ValidTo: &to,
		Status: domain.AgreementEffective, Lines: []domain.AgreementLine{
			{CommitmentType: domain.CommitQuantity, ProductID: product, Unit: "EA", CommittedQty: dec("1000"), UnitPrice: dec("2.5"),
				DiscountPct: decimal.Zero, MaxEnforced: domain.EnforceHard},
			{CommitmentType: domain.CommitTotalValue, CommittedAmount: dec("500"), UnitPrice: decimal.Zero, DiscountPct: decimal.Zero,
				MaxEnforced: domain.EnforceSoft},
		}}
}

// release is a one-line DRAFT PO for qty of product at 2.50, drawing on agreementLine.
func release(t *testing.T, vendor, product, qty, agreementLine, delivery string) store.PurchaseOrder {
	amount := dec(qty).Mul(dec("2.5"))
	return store.PurchaseOrder{LegalEntityID: legalEntity, VendorID: vendor, Currency: "USD", ChangeManagement: true,
		Subtotal: amount, DiscountTotal: decimal.Zero, ChargesTotal: decimal.Zero, TaxTotal: decimal.Zero, Total: amount,
		Lines: []store.PurchaseOrderLine{{LineNo: 1, ProductID: product, Qty: dec(qty), Unit: "EA", UnitPrice: dec("2.5"), PriceUnit: dec("1"),
			DiscountPct: decimal.Zero, GrossAmount: amount, DiscountAmount: decimal.Zero, LineAmount: amount, TaxAmount: decimal.Zero,
			DeliveryDate: day(t, delivery), PriceSource: "AGREEMENT:" + agreementLine, AgreementLineID: agreementLine}}}
}

func TestPurchaseAgreement_CreateListAndLifecycle(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(21)
	vendor, product := ids.New(), ids.New()
	draft := blanket(t, vendor, product)
	draft.Status = domain.AgreementDraft
	created, err := s.CreateAgreement(ctx, draft, store.Idem{Key: "pa", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	a := created.Agreement
	if a.Number == "" || a.Status != domain.AgreementDraft || a.Version != 1 || len(a.Lines) != 2 || a.Lines[1].LineNo != 2 || a.Lines[0].ID == "" ||
		a.ValidTo == nil || a.ValidTo.String() != "2027-12-31" || len(created.Releases) != 0 {
		t.Fatalf("created %+v", created)
	}
	if replay, err := s.CreateAgreement(ctx, draft, store.Idem{Key: "pa", Hash: "h"}); err != nil || replay.Agreement.ID != a.ID {
		t.Fatalf("replay = %v, %v", replay.Agreement.ID, err)
	}

	if _, err := s.ChangeAgreementStatus(ctx, a.ID, 9, domain.AgreementActivate, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version = %v", err)
	}
	if _, err := s.ChangeAgreementStatus(ctx, a.ID, 1, domain.AgreementHold, store.Idem{}); problemCode(err) != domain.CodeAgreementState {
		t.Fatalf("hold a draft = %v", err)
	}
	active, err := s.ChangeAgreementStatus(ctx, a.ID, 1, domain.AgreementActivate, store.Idem{Key: "act", Hash: "x"})
	if err != nil || active.Agreement.Status != domain.AgreementEffective || active.Agreement.Version != 2 {
		t.Fatalf("activate = %+v, %v", active.Agreement, err)
	}
	if again, err := s.ChangeAgreementStatus(ctx, a.ID, 1, domain.AgreementActivate, store.Idem{Key: "act", Hash: "x"}); err != nil || again.Agreement.Version != 2 {
		t.Fatalf("replayed activate = %+v, %v; want the current agreement", again.Agreement, err)
	}

	page, err := s.ListAgreements(ctx, store.AgreementFilter{Status: domain.AgreementEffective, VendorID: vendor}, "", 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].LineCount != 2 || !page.Items[0].MaxPctReleased.IsZero() {
		t.Fatalf("list = %+v, %v", page, err)
	}
	if _, err := s.GetAgreement(principalContext(22), a.ID); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("other tenant read = %v", err)
	}
}

// AC-17.7 and BR-17.10 against Postgres: releases draw the line down under a
// lock, a HARD overrun refuses the whole PO and writes nothing, the threshold
// event is written once with the PO, and the agreement lists its releases.
func TestPurchaseAgreement_ReleasesDrawDownAndEnforceLimits(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(23)
	vendor, product := ids.New(), ids.New()
	created, err := s.CreateAgreement(ctx, blanket(t, vendor, product), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	qtyLine := created.Agreement.Lines[0].ID

	po, err := s.CreatePurchaseOrder(ctx, release(t, vendor, product, "900", qtyLine, "2027-03-01"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if len(po.Warnings) != 1 || po.Warnings[0].Code != domain.CodeAgreementThreshold || po.Lines[0].AgreementLineID != qtyLine {
		t.Fatalf("PO = %+v; want the agreement line kept and one PRC-AGR-004 warning at 90 %%", po)
	}
	events := outbox(t, ctx, pool)
	if len(events) != 1 || events[0].Type != purchasingevents.AgreementThreshold || events[0].Data["pct"] != "90" || events[0].Data["line"] != float64(1) ||
		events[0].Data["expiry"] != "2027-12-31" {
		t.Fatalf("outbox = %+v", events)
	}

	// AC-17.7: 900 + 150 > 1,000 on a HARD line.
	if _, err := s.CreatePurchaseOrder(ctx, release(t, vendor, product, "150", qtyLine, "2027-03-01"), store.Idem{}); problemCode(err) != domain.CodeAgreementLimit {
		t.Fatalf("HARD overrun = %v", err)
	}
	for _, tc := range []struct {
		name string
		po   store.PurchaseOrder
		code string
	}{
		{"delivery after validity", release(t, vendor, product, "1", qtyLine, "2028-01-01"), domain.CodeAgreementWindow},
		{"another vendor", release(t, ids.New(), product, "1", qtyLine, "2027-03-01"), domain.CodeAgreementParty},
		{"another product", release(t, vendor, ids.New(), "1", qtyLine, "2027-03-01"), domain.CodeAgreementItem},
		{"unknown agreement line", release(t, vendor, product, "1", ids.New(), "2027-03-01"), store.CodeAgreementNotFound},
	} {
		if _, err := s.CreatePurchaseOrder(ctx, tc.po, store.Idem{}); problemCode(err) != tc.code {
			t.Fatalf("%s = %v, want %s", tc.name, err, tc.code)
		}
	}
	// The SOFT value line accepts an overrun and says so.
	soft, err := s.CreatePurchaseOrder(ctx, release(t, vendor, ids.New(), "240", created.Agreement.Lines[1].ID, "2027-03-01"), store.Idem{})
	if err != nil || len(soft.Warnings) != 1 {
		t.Fatalf("SOFT overrun = %+v, %v", soft.Warnings, err)
	}

	detail, err := s.GetAgreement(ctx, created.Agreement.ID)
	if err != nil {
		t.Fatal(err)
	}
	l0, l1 := detail.Agreement.Lines[0], detail.Agreement.Lines[1]
	if !l0.ReleasedQty.Equal(dec("900")) || !l0.ReleasedAmount.Equal(dec("2250")) || !l1.ReleasedAmount.Equal(dec("600")) {
		t.Fatalf("released = %s/%s and %s; refused POs must not draw", l0.ReleasedQty, l0.ReleasedAmount, l1.ReleasedAmount)
	}
	if len(detail.Releases) != 2 || detail.Releases[0].PurchaseOrderID != po.ID || !detail.Releases[0].Qty.Equal(dec("900")) {
		t.Fatalf("releases = %+v", detail.Releases)
	}
	if page, _ := s.ListPurchaseOrders(ctx, store.PurchaseOrderFilter{VendorID: vendor}, "", 0); len(page.Items) != 2 {
		t.Fatalf("POs written = %d, want only the two accepted", len(page.Items))
	}
	if page, _ := s.ListAgreements(ctx, store.AgreementFilter{}, "", 0); !page.Items[0].MaxPctReleased.Equal(dec("120")) {
		t.Fatalf("max pct = %s, want the SOFT line's 120", page.Items[0].MaxPctReleased)
	}

	held, err := s.ChangeAgreementStatus(ctx, created.Agreement.ID, 1, domain.AgreementHold, store.Idem{})
	if err != nil || held.Agreement.Status != domain.AgreementOnHold {
		t.Fatalf("hold = %v", err)
	}
	if _, err := s.CreatePurchaseOrder(ctx, release(t, vendor, product, "1", qtyLine, "2027-03-01"), store.Idem{}); problemCode(err) != domain.CodeAgreementInactive {
		t.Fatalf("release on hold = %v", err)
	}
}

func TestPurchaseAgreement_DatabaseRefusesHardOverrun(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(24)
	created, err := s.CreateAgreement(ctx, blanket(t, ids.New(), ids.New()), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update purchase_agreement_line set released_qty = 1001 where id = $1`, created.Agreement.Lines[0].ID)
		return err
	})
	if err == nil {
		t.Fatal("the HARD check constraint must refuse released above committed")
	}
}
