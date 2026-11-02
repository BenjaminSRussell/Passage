package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func freightCode() domain.ChargeCode {
	return domain.ChargeCode{LegalEntityID: legalEntity, Code: "FREIGHT", Name: "Freight", Calc: domain.CalcFixed, DefaultValue: dec("40"),
		DebitType: domain.ChargeSideItem, CreditType: domain.ChargeSideLedger, CreditAccountID: ids.New(), AllocationRule: domain.AllocateQty,
		Status: domain.ChargeActive}
}

func TestChargeCode_CreateChangeListAndEvents(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	user := ids.New()
	ctx := asUser(1, user)
	in := freightCode()
	made, err := s.CreateChargeCode(ctx, in, store.Idem{Key: "cc1", Hash: "h1"})
	if err != nil || made.Version != 1 || made.CreatedBy != user || made.CreditAccountID != in.CreditAccountID || made.DebitAccountID != "" ||
		!made.DefaultValue.Equal(dec("40")) || made.AllocationRule != domain.AllocateQty {
		t.Fatalf("create = %+v, %v", made, err)
	}
	if replay, err := s.CreateChargeCode(ctx, in, store.Idem{Key: "cc1", Hash: "h1"}); err != nil || replay.ID != made.ID {
		t.Fatalf("a replayed key returns the first row: %+v, %v", replay, err)
	}
	if _, err := s.CreateChargeCode(ctx, in, store.Idem{Key: "cc1", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Errorf("same key, other body: %v", err)
	}
	if _, err := s.CreateChargeCode(ctx, in, store.Idem{Key: "cc2", Hash: "h2"}); problemCode(err) != store.CodeChargeCodeExists {
		t.Errorf("the same code twice in a legal entity: %v", err)
	}
	handling := in
	handling.Code, handling.Name, handling.Calc, handling.DefaultValue = "HANDLING", "Handling fee", domain.CalcPercent, dec("2")
	handling.DebitType, handling.DebitAccountID, handling.CreditType, handling.CreditAccountID = domain.ChargeSideLedger, ids.New(), domain.ChargeSideVendor, ""
	other, err := s.CreateChargeCode(ctx, handling, store.Idem{Key: "cc2", Hash: "h2"})
	if err != nil {
		t.Fatalf("another code and the key of the failed create: %v", err)
	}

	next := made
	next.Name, next.Calc, next.DefaultValue, next.Status = "Inbound freight", domain.CalcPerUnit, dec("1.25"), domain.ChargeInactive
	next.Code = "IGNORED"
	if _, err := s.UpdateChargeCode(ctx, made.ID, 7, next); problemCode(err) != store.CodeChargeCodeStale {
		t.Errorf("stale If-Match: %v", err)
	}
	upd, err := s.UpdateChargeCode(ctx, made.ID, 1, next)
	if err != nil || upd.Version != 2 || upd.Name != "Inbound freight" || upd.Calc != domain.CalcPerUnit || upd.Status != domain.ChargeInactive ||
		upd.Code != "FREIGHT" || !upd.DefaultValue.Equal(dec("1.25")) {
		t.Fatalf("update = %+v, %v; the code string must not change", upd, err)
	}
	if _, err := s.GetChargeCode(ctx, ids.New()); problemCode(err) != store.CodeChargeCodeNotFound {
		t.Errorf("unknown id: %v", err)
	}

	page, err := s.ListChargeCodes(ctx, store.ChargeCodeFilter{LegalEntityID: legalEntity}, "", 1)
	if err != nil || len(page.Items) != 1 || page.Next == "" || page.Items[0].ID != other.ID {
		t.Fatalf("first page newest first = %+v, %v", page, err)
	}
	if rest, _ := s.ListChargeCodes(ctx, store.ChargeCodeFilter{}, page.Next, 5); len(rest.Items) != 1 || rest.Items[0].ID != made.ID {
		t.Errorf("second page = %+v", rest.Items)
	}
	if active, _ := s.ListChargeCodes(ctx, store.ChargeCodeFilter{Status: domain.ChargeActive}, "", 5); len(active.Items) != 1 || active.Items[0].ID != other.ID {
		t.Errorf("status filter: %+v", active.Items)
	}
	if found, _ := s.ListChargeCodes(ctx, store.ChargeCodeFilter{Text: "inbound"}, "", 5); len(found.Items) != 1 || found.Items[0].ID != made.ID {
		t.Errorf("text filter on the name: %+v", found.Items)
	}

	counts := map[string]int{}
	for _, e := range outbox(t, ctx, pool) {
		counts[e.Type]++
	}
	if counts[purchasingevents.ChargeCodeCreated] != 2 || counts[purchasingevents.ChargeCodeChanged] != 1 {
		t.Errorf("events = %v; want 2 created and 1 changed", counts)
	}
	if seen, _ := s.ListChargeCodes(asUser(2, user), store.ChargeCodeFilter{}, "", 5); len(seen.Items) != 0 {
		t.Errorf("another tenant sees %d charge codes", len(seen.Items))
	}
}

func TestAutoCharge_CreateChangeListAndActive(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	user := ids.New()
	ctx := asUser(1, user)
	code, err := s.CreateChargeCode(ctx, freightCode(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendor := ids.New()
	min := dec("500")
	in := domain.AutoCharge{LegalEntityID: legalEntity, ChargeCodeID: code.ID, AccountCode: domain.AutoChargeVendor, AccountID: vendor,
		MinOrderAmount: &min, Currency: "USD", ValidFrom: dayOf(t, "2026-03-01"), Status: domain.ChargeActive}
	made, err := s.CreateAutoCharge(ctx, in, store.Idem{Key: "ac1", Hash: "h1"})
	if err != nil || made.Version != 1 || made.Value != nil || made.MinOrderAmount == nil || !made.MinOrderAmount.Equal(min) || made.ValidTo != nil || made.AccountID != vendor {
		t.Fatalf("create = %+v, %v", made, err)
	}
	if replay, err := s.CreateAutoCharge(ctx, in, store.Idem{Key: "ac1", Hash: "h1"}); err != nil || replay.ID != made.ID {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	if _, err := s.CreateAutoCharge(ctx, domain.AutoCharge{LegalEntityID: legalEntity, ChargeCodeID: ids.New(), AccountCode: domain.AutoChargeAll,
		Currency: "USD", ValidFrom: dayOf(t, "2026-03-01"), Status: domain.ChargeActive}, store.Idem{}); err == nil {
		t.Error("an auto charge for a charge code that does not exist must be refused by the foreign key")
	}
	all, err := s.CreateAutoCharge(ctx, domain.AutoCharge{LegalEntityID: legalEntity, ChargeCodeID: code.ID, AccountCode: domain.AutoChargeAll,
		Currency: "USD", ValidFrom: dayOf(t, "2026-03-01"), Status: domain.ChargeActive}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}

	value, to := dec("55.5"), dayOf(t, "2026-12-31")
	upd := made
	upd.Value, upd.MinOrderAmount, upd.ValidTo, upd.Status = &value, nil, &to, domain.ChargeActive
	if _, err := s.UpdateAutoCharge(ctx, made.ID, 9, upd); problemCode(err) != store.CodeAutoChargeStale {
		t.Errorf("stale If-Match: %v", err)
	}
	got, err := s.UpdateAutoCharge(ctx, made.ID, 1, upd)
	if err != nil || got.Version != 2 || got.Value == nil || !got.Value.Equal(value) || got.MinOrderAmount != nil || got.ValidTo == nil || got.ValidTo.String() != "2026-12-31" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if _, err := s.GetAutoCharge(ctx, ids.New()); problemCode(err) != store.CodeAutoChargeNotFound {
		t.Errorf("unknown id: %v", err)
	}
	off := all
	off.Status = domain.ChargeInactive
	if _, err := s.UpdateAutoCharge(ctx, all.ID, 1, off); err != nil {
		t.Fatal(err)
	}

	group := ids.New()
	byGroup, err := s.CreateAutoCharge(ctx, domain.AutoCharge{LegalEntityID: legalEntity, ChargeCodeID: code.ID, AccountCode: domain.AutoChargeGroup,
		AccountID: group, Currency: "USD", ValidFrom: dayOf(t, "2026-03-01"), Status: domain.ChargeActive}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAutoCharge(ctx, domain.AutoCharge{LegalEntityID: legalEntity, ChargeCodeID: code.ID, AccountCode: domain.AutoChargeVendor,
		AccountID: ids.New(), Currency: "USD", ValidFrom: dayOf(t, "2026-03-01"), Status: domain.ChargeActive}, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	// The vendor's own row and its group's; not the deactivated ALL row, another vendor's, or another group's.
	active, err := s.ActiveAutoCharges(ctx, legalEntity, vendor, []string{group}, dayOf(t, "2026-06-01"))
	if err != nil || len(active) != 2 || active[0].ID != made.ID || active[1].ID != byGroup.ID {
		t.Fatalf("active = %+v, %v; want the vendor's and the group's rows, oldest first", active, err)
	}
	if active, _ := s.ActiveAutoCharges(ctx, legalEntity, vendor, nil, dayOf(t, "2026-06-01")); len(active) != 1 || active[0].ID != made.ID {
		t.Errorf("without groups = %+v; want the vendor's row only", active)
	}
	if active, _ := s.ActiveAutoCharges(ctx, legalEntity, vendor, []string{group}, dayOf(t, "2027-01-01")); len(active) != 1 || active[0].ID != byGroup.ID {
		t.Errorf("after the vendor row's window = %+v; want the open-ended group row only", active)
	}
	if active, _ := s.ActiveAutoCharges(ctx, legalEntity, vendor, []string{group}, dayOf(t, "2026-02-28")); len(active) != 0 {
		t.Errorf("before every window = %+v", active)
	}

	// The calculation of a code that auto charges use is fixed: their values were checked against it.
	cur, _ := s.GetChargeCode(ctx, code.ID)
	pct := cur
	pct.Calc = domain.CalcPercent
	if _, err := s.UpdateChargeCode(ctx, code.ID, cur.Version, pct); problemCode(err) != store.CodeChargeCalcLocked {
		t.Errorf("calc change on a code with auto charges: %v, want %s", err, store.CodeChargeCalcLocked)
	}
	renamed := cur
	renamed.Name = "Freight in"
	if _, err := s.UpdateChargeCode(ctx, code.ID, cur.Version, renamed); err != nil {
		t.Errorf("a change that keeps the calculation: %v", err)
	}
	if page, _ := s.ListAutoCharges(ctx, store.AutoChargeFilter{ChargeCodeID: code.ID}, "", 5); len(page.Items) != 4 || page.Items[3].ID != made.ID {
		t.Errorf("list newest first = %+v", page.Items)
	}
	if page, _ := s.ListAutoCharges(ctx, store.AutoChargeFilter{Status: domain.ChargeInactive}, "", 5); len(page.Items) != 1 || page.Items[0].ID != all.ID {
		t.Errorf("status filter = %+v", page.Items)
	}
	counts := map[string]int{}
	for _, e := range outbox(t, ctx, pool) {
		counts[e.Type]++
	}
	if counts[purchasingevents.AutoChargeCreated] != 4 || counts[purchasingevents.AutoChargeChanged] != 2 {
		t.Errorf("events = %v; want 4 created and 2 changed", counts)
	}
}

func TestPurchaseOrder_ChargeSidesAreStoredAndSurviveAnEdit(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := asUser(1, ids.New())
	code, err := s.CreateChargeCode(ctx, freightCode(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendor := ids.New()
	in := lifecyclePO(t, vendor, "10", "2026-04-01")
	in.Charges = []store.PurchaseOrderCharge{
		{Seq: 1, Calc: domain.CalcFixed, Value: dec("40"), Amount: dec("40"), ChargeCodeID: code.ID, ChargeCode: code.Code,
			DebitType: code.DebitType, CreditType: code.CreditType, CreditAccountID: code.CreditAccountID, AllocationRule: code.AllocationRule, Auto: true},
		{Seq: 2, Calc: domain.CalcFixed, Value: dec("5"), Amount: dec("5")},
	}
	in.ChargesTotal, in.Total = dec("45"), dec("145")
	in.AutoChargeCodes = []string{code.ID}
	po, err := s.CreatePurchaseOrder(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	check := func(label string, p store.PurchaseOrder) {
		t.Helper()
		if len(p.Charges) != 2 {
			t.Fatalf("%s: charges = %+v", label, p.Charges)
		}
		c, plain := p.Charges[0], p.Charges[1]
		if c.ChargeCodeID != code.ID || c.ChargeCode != "FREIGHT" || c.CreditType != domain.ChargeSideLedger || c.CreditAccountID != code.CreditAccountID ||
			c.AllocationRule != domain.AllocateQty || !c.Auto {
			t.Errorf("%s: coded charge = %+v", label, c)
		}
		if plain.ChargeCodeID != "" || plain.DebitType != "ITEM" || plain.CreditType != "VENDOR" || plain.AllocationRule != "AMOUNT" || plain.Auto {
			t.Errorf("%s: a charge without a code takes the defaults, got %+v", label, plain)
		}
	}
	check("create", po)
	got, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("read", got)

	// The code changing later does not move the PO, and an edit that keeps the charges keeps their sides.
	changed := code
	changed.CreditType, changed.CreditAccountID, changed.AllocationRule = domain.ChargeSideVendor, "", domain.AllocateLines
	if _, err := s.UpdateChargeCode(ctx, code.ID, 1, changed); err != nil {
		t.Fatal(err)
	}
	next := got
	next.Lines = append([]store.PurchaseOrderLine{}, got.Lines...)
	edit, err := s.EditPurchaseOrder(ctx, po.ID, got.RowVersion, edited(got, next))
	if err != nil {
		t.Fatal(err)
	}
	check("edit", edit)
	var zero decimal.Decimal
	if edit.ChargesTotal.Equal(zero) {
		t.Error("chargesTotal lost in the edit")
	}
	if len(got.AutoChargeCodes) != 1 || got.AutoChargeCodes[0] != code.ID || len(edit.AutoChargeCodes) != 1 {
		t.Errorf("auto charge codes = %v after create, %v after an edit that added none; want [%s]", got.AutoChargeCodes, edit.AutoChargeCodes, code.ID)
	}

	// An edit that drops the auto charge keeps its code in the set; an edit that adds another code grows it.
	later := ids.New()
	drop := edit
	drop.Lines = append([]store.PurchaseOrderLine{}, edit.Lines...)
	drop.Charges = edit.Charges[1:]
	drop.Charges[0].Seq = 1
	drop.ChargesTotal, drop.Total = dec("5"), dec("105")
	drop.AutoChargeCodes = []string{later, code.ID}
	after, err := s.EditPurchaseOrder(ctx, po.ID, edit.RowVersion, edited(edit, drop))
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Charges) != 1 || len(after.AutoChargeCodes) != 2 || after.AutoChargeCodes[0] != code.ID || after.AutoChargeCodes[1] != later {
		t.Errorf("after dropping the auto charge: charges %+v, codes %v; want one charge and [%s %s]", after.Charges, after.AutoChargeCodes, code.ID, later)
	}
}

func TestChargeCode_LegalEntityScope(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(93), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(93), LegalEntityID: entityB, UserID: ids.New()})
	in := freightCode()
	in.LegalEntityID = entityA
	code, err := s.CreateChargeCode(ctxA, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	ac, err := s.CreateAutoCharge(ctxA, domain.AutoCharge{LegalEntityID: entityA, ChargeCodeID: code.ID, AccountCode: domain.AutoChargeAll,
		Currency: "USD", ValidFrom: dayOf(t, "2026-03-01"), Status: domain.ChargeActive}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChargeCode(ctxB, code.ID); problemCode(err) != store.CodeChargeCodeNotFound {
		t.Errorf("legal entity B read A's charge code: %v", err)
	}
	if _, err := s.GetAutoCharge(ctxB, ac.ID); problemCode(err) != store.CodeAutoChargeNotFound {
		t.Errorf("legal entity B read A's auto charge: %v", err)
	}
	if page, _ := s.ListChargeCodes(ctxB, store.ChargeCodeFilter{}, "", 5); len(page.Items) != 0 {
		t.Errorf("legal entity B lists %d charge codes", len(page.Items))
	}
	if active, _ := s.ActiveAutoCharges(ctxB, entityA, ids.New(), nil, dayOf(t, "2026-06-01")); len(active) != 0 {
		t.Errorf("legal entity B sees %d of A's auto charges", len(active))
	}
	other := freightCode()
	other.LegalEntityID = entityA
	other.Code = "HANDLING"
	if _, err := s.CreateChargeCode(ctxB, other, store.Idem{}); err == nil {
		t.Error("legal entity B wrote a charge code of A")
	}
}
