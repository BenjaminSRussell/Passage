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

func svcItem(desc, qty, price string) domain.OutlineNode {
	return domain.OutlineNode{Kind: domain.OutlineItem, Description: desc, Qty: dec(qty), Unit: "h", UnitPrice: dec(price)}
}

// maintenance is the AC-17.17 outline: "Maintenance" with 3 service items worth 650.00.
func maintenance() []domain.OutlineNode {
	return []domain.OutlineNode{{Kind: domain.OutlineLevel, Description: "Maintenance", Children: []domain.OutlineNode{
		svcItem("Inspection", "3", "100"), svcItem("Lubrication", "2", "75"), svcItem("Report", "1", "200")}}}
}

// FR-17.23 and AC-17.17 against Postgres: a PO line takes a specification with a 3-item
// outline and a 2,000.00 limit; the limit check refuses the 200.00 entry that would take the
// unplanned total from 1,900.00 to 2,100.00; the rules of the PO state, the outline and the line
// amount hold; the event rides in the outbox.
func TestServiceSpec_AC17_17(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(75)
	po, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "100", "2027-02-01"), store.Idem{}) // line amount 1,000.00
	if err != nil {
		t.Fatal(err)
	}
	terms := domain.ServiceTerms{ValueLimit: dec("2000"), ExpectedValue: dec("500")}

	if _, err := s.GetServiceSpec(ctx, po.ID, 1); problemCode(err) != store.CodeServiceSpecNotFound {
		t.Fatalf("no specification yet = %v", err)
	}
	if _, err := s.CheckServiceLimit(ctx, po.ID, 1, dec("0"), dec("1")); problemCode(err) != store.CodeServiceSpecNotFound {
		t.Fatalf("check without a specification = %v", err)
	}
	total := dec("1")
	bad := maintenance()
	bad[0].Total = &total
	if _, err := s.PutServiceSpec(ctx, po.ID, 1, po.RowVersion, store.ServiceInput{Terms: terms, Outline: bad}); problemCode(err) != domain.CodeServiceOutline {
		t.Fatalf("wrong stated total = %v", err)
	}
	if _, err := s.PutServiceSpec(ctx, po.ID, 1, po.RowVersion+1, store.ServiceInput{Terms: terms, Outline: maintenance()}); problemCode(err) != store.CodeStaleVersion {
		t.Fatalf("stale If-Match = %v", err)
	}
	if _, err := s.PutServiceSpec(ctx, po.ID, 2, po.RowVersion, store.ServiceInput{Terms: terms}); problemCode(err) != store.CodePOLineNotFound {
		t.Fatalf("missing line = %v", err)
	}
	big := maintenance()
	big[0].Children = append(big[0].Children, svcItem("Overhaul", "1", "400.01"))
	if _, err := s.PutServiceSpec(ctx, po.ID, 1, po.RowVersion, store.ServiceInput{Terms: terms, Outline: big}); problemCode(err) != domain.CodeServicePlanned {
		t.Fatalf("1,050.01 planned on a 1,000.00 line = %v", err)
	}
	if _, err := s.PutServiceSpec(ctx, po.ID, 1, po.RowVersion, store.ServiceInput{Terms: domain.ServiceTerms{ValueLimit: dec("100"), ExpectedValue: dec("200")}}); problemCode(err) != domain.CodeServicePlanned {
		t.Fatalf("expected above the limit = %v", err)
	}
	if _, err := s.PutServiceSpec(ctx, po.ID, 1, po.RowVersion, store.ServiceInput{Terms: terms, ModelID: ids.New()}); problemCode(err) != store.CodeModelServiceNotFound {
		t.Fatalf("unknown model = %v", err)
	}

	put, err := s.PutServiceSpec(ctx, po.ID, 1, po.RowVersion, store.ServiceInput{Terms: terms, Outline: maintenance()})
	if err != nil {
		t.Fatal(err)
	}
	if put.PORowVersion != po.RowVersion+1 || len(put.Outline) != 4 || !put.PlannedTotal.Equal(dec("650")) || !put.ValueLimit.Equal(dec("2000")) ||
		!put.LineAmount.Equal(dec("1000")) || put.Currency != "USD" || put.Outline[0].Kind != domain.OutlineLevel || !put.Outline[0].Amount.Equal(dec("650")) ||
		put.Outline[3].ParentSeq != 1 || !put.Outline[3].Amount.Equal(dec("200")) || put.UpdatedBy == "" {
		t.Fatalf("specification = %+v", put)
	}
	// A rewrite replaces the outline.
	put, err = s.PutServiceSpec(ctx, po.ID, 1, put.PORowVersion, store.ServiceInput{Terms: terms, Outline: []domain.OutlineNode{svcItem("Call-out", "1", "80")}})
	if err != nil || len(put.Outline) != 1 || !put.PlannedTotal.Equal(dec("80")) {
		t.Fatalf("rewrite = %+v (%v)", put, err)
	}
	put, err = s.PutServiceSpec(ctx, po.ID, 1, put.PORowVersion, store.ServiceInput{Terms: terms, Outline: maintenance()})
	if err != nil {
		t.Fatal(err)
	}

	list, err := s.ListServiceSpecs(ctx, po.ID)
	if err != nil || len(list) != 1 || list[0].LineNo != 1 || len(list[0].Outline) != 4 {
		t.Fatalf("list = %+v (%v)", list, err)
	}
	if _, err := s.ListServiceSpecs(ctx, ids.New()); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("list of an unknown PO = %v", err)
	}
	if _, err := s.GetServiceSpec(principalContext(76), po.ID, 1); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("other tenant read = %v", err)
	}

	// AC-17.17: 1,900.00 entered, a further 200.00 is refused and 100.00 fits.
	if chk, err := s.CheckServiceLimit(ctx, po.ID, 1, dec("1900"), dec("200")); err != nil || chk.Allowed || chk.Code != domain.CodeServiceLimit {
		t.Fatalf("200 over 1900 = %+v (%v)", chk, err)
	}
	chk, err := s.CheckServiceLimit(ctx, po.ID, 1, dec("1900"), dec("100"))
	if err != nil || !chk.Allowed || !chk.ValueRemaining.Equal(dec("100")) || chk.Currency != "USD" || chk.LineNo != 1 {
		t.Fatalf("100 over 1900 = %+v (%v)", chk, err)
	}

	// Lowering the line below the planned outline stops the submission while the specification
	// can still be fixed, and the confirmation of an order changed after submit (PRC-SRV-002).
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "50", "2027-02-01"))); err != nil { // 500.00
		t.Fatal(err)
	}
	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	if _, err := s.ActOnPurchaseOrder(ctx, po.ID, cur.RowVersion, domain.POActionSubmit, store.Idem{}); problemCode(err) != domain.CodeServicePlanned {
		t.Fatalf("submit with 650.00 planned on a 500.00 line = %v", err)
	}
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "100", "2027-02-01"))); err != nil {
		t.Fatal(err)
	}
	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	cur = walk(t, ctx, s, cur, domain.POActionSubmit)
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "50", "2027-02-01"))); err != nil {
		t.Fatal(err)
	}
	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	if cur.ApprovalStatus != domain.POStatusDraft {
		t.Fatalf("an edit in review restarts approval, got %s", cur.ApprovalStatus)
	}
	if _, err := s.ActOnPurchaseOrder(ctx, po.ID, cur.RowVersion, domain.POActionSubmit, store.Idem{}); problemCode(err) != domain.CodeServicePlanned {
		t.Fatalf("submit after the edit = %v", err)
	}
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "100", "2027-02-01"))); err != nil {
		t.Fatal(err)
	}
	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	cur = walk(t, ctx, s, cur, domain.POActionSubmit, domain.POActionApprove)
	if _, err := s.PutServiceSpec(ctx, po.ID, 1, cur.RowVersion, store.ServiceInput{Terms: terms}); problemCode(err) != domain.CodeServiceTarget {
		t.Fatalf("edit of an APPROVED PO = %v", err)
	}
	if _, err := s.DeleteServiceSpec(ctx, po.ID, 1, cur.RowVersion); problemCode(err) != domain.CodeServiceTarget {
		t.Fatalf("delete on an APPROVED PO = %v", err)
	}
	// A change of the confirmed PO cannot take the line below the frozen planned outline.
	cur = walk(t, ctx, s, cur, domain.POActionConfirm)
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "50", "2027-02-01"))); problemCode(err) != domain.CodeServicePlanned {
		t.Fatalf("change of a confirmed PO below the planned outline = %v", err)
	}
	// The cancelled PO takes no entry.
	if _, err := s.ActOnPurchaseOrder(ctx, po.ID, cur.RowVersion, domain.POActionCancel, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckServiceLimit(ctx, po.ID, 1, dec("0"), dec("1")); problemCode(err) != domain.CodeServiceTarget {
		t.Fatalf("entry on a cancelled PO = %v", err)
	}

	var changed []purchasingevents.ServiceSpecData
	for _, r := range outbox(t, ctx, pool) {
		if r.Type == purchasingevents.ServiceSpecChanged {
			var d purchasingevents.ServiceSpecData
			if err := remarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
			changed = append(changed, d)
		}
	}
	if len(changed) != 3 || changed[0].OutlineItems != 4 || changed[0].PlannedTotal != "650" || changed[0].ValueLimit != "2000" || changed[0].POID != po.ID || changed[0].Removed {
		t.Fatalf("servicespec.changed events = %+v", changed)
	}
}

func TestServiceSpec_DeleteAndContractLimit(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(77)
	vendor, product := ids.New(), ids.New()
	created, err := s.CreateAgreement(ctx, blanket(t, vendor, product), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	valueLine := created.Agreement.Lines[1] // TOTAL_VALUE 500.00
	po, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, vendor, "100", "2027-02-01"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	terms := domain.ServiceTerms{ValueLimit: dec("2000"), ContractAgreementLineID: valueLine.ID}
	put, err := s.PutServiceSpec(ctx, po.ID, 1, po.RowVersion, store.ServiceInput{Terms: terms})
	if err != nil || put.ContractRemaining == nil || !put.ContractRemaining.Equal(dec("500")) || len(put.Outline) != 0 {
		t.Fatalf("contract limit = %+v (%v)", put, err)
	}
	// The contract line's remaining 500.00 bounds the unplanned total below the 2,000.00 value limit.
	if chk, err := s.CheckServiceLimit(ctx, po.ID, 1, dec("400"), dec("100.01")); err != nil || chk.Allowed || chk.Code != domain.CodeServiceLimit ||
		!chk.Remaining.Equal(dec("100")) {
		t.Fatalf("500.01 over a 500.00 contract = %+v (%v)", chk, err)
	}
	if chk, err := s.CheckServiceLimit(ctx, po.ID, 1, dec("400"), dec("100")); err != nil || !chk.Allowed || chk.ContractRemaining == nil {
		t.Fatalf("500.00 over a 500.00 contract = %+v (%v)", chk, err)
	}
	// Another vendor's or an unknown contract line cannot back the limit.
	otherPO, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "10", "2027-02-01"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutServiceSpec(ctx, otherPO.ID, 1, otherPO.RowVersion, store.ServiceInput{Terms: terms}); problemCode(err) != domain.CodeServicePlanned {
		t.Fatalf("another vendor's agreement = %v", err)
	}
	if _, err := s.PutServiceSpec(ctx, otherPO.ID, 1, otherPO.RowVersion, store.ServiceInput{Terms: domain.ServiceTerms{ValueLimit: dec("1"), ContractAgreementLineID: ids.New()}}); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("unknown agreement line = %v", err)
	}

	// A REJECTED PO takes a specification again.
	rej := walk(t, ctx, s, otherPO, domain.POActionSubmit, domain.POActionReject)
	if _, err := s.PutServiceSpec(ctx, rej.ID, 1, rej.RowVersion, store.ServiceInput{Terms: domain.ServiceTerms{ValueLimit: dec("1")}}); err != nil {
		t.Fatalf("specification on a REJECTED PO = %v", err)
	}

	v, err := s.DeleteServiceSpec(ctx, po.ID, 1, put.PORowVersion)
	if err != nil || v != put.PORowVersion+1 {
		t.Fatalf("delete = %d (%v)", v, err)
	}
	if _, err := s.DeleteServiceSpec(ctx, po.ID, 1, v); problemCode(err) != store.CodeServiceSpecNotFound {
		t.Fatalf("second delete = %v", err)
	}
	var removed int
	for _, r := range outbox(t, ctx, pool) {
		if r.Type == purchasingevents.ServiceSpecChanged {
			var d purchasingevents.ServiceSpecData
			_ = remarshal(r.Data, &d)
			if d.Removed {
				removed++
			}
		}
	}
	if removed != 1 {
		t.Errorf("removed events = %d", removed)
	}
}

func TestModelServiceSpec_CreateChangeAndApply(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(78)
	in := domain.ModelService{LegalEntityID: legalEntity, Code: "MAINT", Name: "Maintenance", Currency: "USD", Status: domain.ModelActive}
	m, err := s.CreateModelServiceSpec(ctx, in, maintenance(), store.Idem{Key: "m1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || len(m.Outline) != 4 || !m.PlannedTotal.Equal(dec("650")) || m.CreatedBy == "" {
		t.Fatalf("model = %+v", m)
	}
	if replay, err := s.CreateModelServiceSpec(ctx, in, maintenance(), store.Idem{Key: "m1", Hash: "h"}); err != nil || replay.ID != m.ID {
		t.Fatalf("replay = %v (%v)", replay.ID, err)
	}
	if _, err := s.CreateModelServiceSpec(ctx, in, maintenance(), store.Idem{}); problemCode(err) != store.CodeModelServiceExists {
		t.Fatalf("duplicate code = %v", err)
	}
	if _, err := s.UpdateModelServiceSpec(ctx, m.ID, 9, m, maintenance()); problemCode(err) != store.CodeModelServiceStale {
		t.Fatalf("stale version = %v", err)
	}

	// Applying the model to a line copies its outline.
	po, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "100", "2027-02-01"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	terms := domain.ServiceTerms{ValueLimit: dec("1000")}
	put, err := s.PutServiceSpec(ctx, po.ID, 1, po.RowVersion, store.ServiceInput{Terms: terms, ModelID: m.ID})
	if err != nil || put.ModelID != m.ID || len(put.Outline) != 4 || !put.PlannedTotal.Equal(dec("650")) {
		t.Fatalf("apply = %+v (%v)", put, err)
	}

	// Changing the model does not move the line, and an inactive or other-currency model is refused.
	m.Name, m.Status = "Maintenance v2", domain.ModelInactive
	upd, err := s.UpdateModelServiceSpec(ctx, m.ID, 1, m, []domain.OutlineNode{svcItem("Call-out", "1", "80")})
	if err != nil || upd.Version != 2 || len(upd.Outline) != 1 || upd.Status != domain.ModelInactive || upd.Code != "MAINT" {
		t.Fatalf("update = %+v (%v)", upd, err)
	}
	got, _ := s.GetPurchaseOrder(ctx, po.ID)
	if again, err := s.GetServiceSpec(ctx, po.ID, 1); err != nil || len(again.Outline) != 4 {
		t.Fatalf("the line keeps its copy: %+v (%v)", again, err)
	}
	if _, err := s.PutServiceSpec(ctx, po.ID, 1, got.RowVersion, store.ServiceInput{Terms: terms, ModelID: m.ID}); problemCode(err) != domain.CodeServiceModel {
		t.Fatalf("inactive model = %v", err)
	}
	eur := domain.ModelService{LegalEntityID: legalEntity, Code: "EUR1", Name: "Euro", Currency: "EUR", Status: domain.ModelActive}
	em, err := s.CreateModelServiceSpec(ctx, eur, maintenance(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutServiceSpec(ctx, po.ID, 1, got.RowVersion, store.ServiceInput{Terms: terms, ModelID: em.ID}); problemCode(err) != domain.CodeServiceModel {
		t.Fatalf("model in another currency = %v", err)
	}

	page, err := s.ListModelServiceSpecs(ctx, store.ModelServiceFilter{LegalEntityID: legalEntity, Text: "maint"}, "", 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].Code != "MAINT" || len(page.Items[0].Outline) != 0 {
		t.Fatalf("list = %+v (%v)", page, err)
	}
	if page, _ := s.ListModelServiceSpecs(ctx, store.ModelServiceFilter{Status: domain.ModelActive}, "", 0); len(page.Items) != 1 || page.Items[0].Code != "EUR1" {
		t.Fatalf("active list = %+v", page)
	}
	if _, err := s.GetModelServiceSpec(principalContext(79), m.ID); problemCode(err) != store.CodeModelServiceNotFound {
		t.Fatalf("other tenant read = %v", err)
	}

	types := map[string]int{}
	for _, r := range outbox(t, ctx, pool) {
		types[r.Type]++
	}
	if types[purchasingevents.ModelServiceSpecCreated] != 2 || types[purchasingevents.ModelServiceSpecChanged] != 1 {
		t.Errorf("events = %v", types)
	}
}

// The service tables are isolated by tenant and, through the PO or the model, by legal entity
// scope: a caller of another tenant or another legal entity finds neither the specification, the
// limit check nor the model.
func TestServiceSpec_TenantAndLegalEntityIsolation(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(93), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(93), LegalEntityID: entityB, UserID: ids.New()})
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(94), UserID: ids.New(), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})

	in := lifecyclePO(t, ids.New(), "100", "2027-02-01")
	in.LegalEntityID = entityA
	po, err := s.CreatePurchaseOrder(ctxA, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutServiceSpec(ctxA, po.ID, 1, po.RowVersion, store.ServiceInput{Terms: domain.ServiceTerms{ValueLimit: dec("100")}, Outline: maintenance()}); err != nil {
		t.Fatal(err)
	}
	m, err := s.CreateModelServiceSpec(ctxA, domain.ModelService{LegalEntityID: entityA, Code: "ISO", Name: "Isolated", Currency: "USD", Status: domain.ModelActive}, maintenance(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{"other legal entity": ctxB, "other tenant": other} {
		if _, err := s.GetServiceSpec(ctx, po.ID, 1); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Errorf("%s: read of the specification = %v", name, err)
		}
		if _, err := s.ListServiceSpecs(ctx, po.ID); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Errorf("%s: list of the specifications = %v", name, err)
		}
		if _, err := s.CheckServiceLimit(ctx, po.ID, 1, decimal.Zero, dec("1")); problemCode(err) != store.CodePurchaseOrderNotFound {
			t.Errorf("%s: limit check = %v", name, err)
		}
		if _, err := s.GetModelServiceSpec(ctx, m.ID); problemCode(err) != store.CodeModelServiceNotFound {
			t.Errorf("%s: read of the model = %v", name, err)
		}
		if page, err := s.ListModelServiceSpecs(ctx, store.ModelServiceFilter{}, "", 0); err != nil || len(page.Items) != 0 {
			t.Errorf("%s: model list = %+v (%v)", name, page.Items, err)
		}
	}
	// Writes are refused too: another legal entity cannot change the model.
	if _, err := s.UpdateModelServiceSpec(ctxB, m.ID, m.Version, m, maintenance()); problemCode(err) != store.CodeModelServiceNotFound {
		t.Errorf("other legal entity changing the model = %v", err)
	}
	if got, err := s.GetServiceSpec(ctxA, po.ID, 1); err != nil || len(got.Outline) != 4 {
		t.Fatalf("owner read = %+v (%v)", got, err)
	}
}
