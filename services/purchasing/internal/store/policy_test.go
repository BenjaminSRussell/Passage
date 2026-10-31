package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// asUser is ctx's tenant with another user.
func asUser(n int, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: user, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

func TestPolicy_DefaultsSaveStaleListAndEvent(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	user := ids.New()
	ctx := asUser(1, user)
	got, err := s.GetPolicy(ctx, legalEntity)
	if err != nil || got.Version != 0 || !got.ChangeManagementDefault || got.POFourEyes || got.AllowInvoiceBeforeReceipt {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	saved, err := s.PutPolicy(ctx, domain.Policy{LegalEntityID: legalEntity, ChangeManagementDefault: false, POFourEyes: true}, 0)
	if err != nil || saved.Version != 1 || saved.ChangeManagementDefault || !saved.POFourEyes || saved.UpdatedBy != user {
		t.Fatalf("create = %+v, %v", saved, err)
	}
	if _, err := s.PutPolicy(ctx, domain.Policy{LegalEntityID: legalEntity}, 0); problemCode(err) != store.CodePolicyStale {
		t.Errorf("creating twice: %v", err)
	}
	if _, err := s.PutPolicy(ctx, domain.Policy{LegalEntityID: legalEntity}, 7); problemCode(err) != store.CodePolicyStale {
		t.Errorf("wrong version: %v", err)
	}
	next, err := s.PutPolicy(ctx, domain.Policy{LegalEntityID: legalEntity, ChangeManagementDefault: true, AllowInvoiceBeforeReceipt: true}, 1)
	if err != nil || next.Version != 2 || next.POFourEyes || !next.AllowInvoiceBeforeReceipt {
		t.Fatalf("update = %+v, %v", next, err)
	}
	list, err := s.ListPolicies(ctx)
	if err != nil || len(list) != 1 || list[0].Version != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	var changed int
	for _, e := range outbox(t, ctx, pool) {
		if e.Type == purchasingevents.PolicyChanged {
			changed++
		}
	}
	if changed != 2 {
		t.Errorf("policy.changed events = %d, want 2 (failed saves write none)", changed)
	}
	if other, _ := s.ListPolicies(asUser(2, user)); len(other) != 0 {
		t.Errorf("another tenant sees %d policies", len(other))
	}
}

// PO_FOUR_EYES: with the policy on, neither the creator, the submitter nor the editor of a change can
// approve or confirm; another user can; with it off, the creator can.
func TestPolicy_FourEyesBlocksCreatorSubmitterAndEditor(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	creator, submitter, approver := ids.New(), ids.New(), ids.New()
	cctx, sctx, actx := asUser(1, creator), asUser(1, submitter), asUser(1, approver)
	if _, err := s.PutPolicy(cctx, domain.Policy{LegalEntityID: legalEntity, ChangeManagementDefault: true, POFourEyes: true}, 0); err != nil {
		t.Fatal(err)
	}
	po, err := s.CreatePurchaseOrder(cctx, lifecyclePO(t, ids.New(), "10", "2027-01-05"), store.Idem{})
	if err != nil || po.CreatedBy != creator {
		t.Fatalf("create: %+v, %v", po, err)
	}
	po = walk(t, sctx, s, po, domain.POActionSubmit)
	if po.SubmittedBy != submitter {
		t.Fatalf("submittedBy = %q", po.SubmittedBy)
	}
	act := func(ctx context.Context, action string) (store.PurchaseOrder, error) {
		return s.ActOnPurchaseOrder(ctx, po.ID, po.RowVersion, action, store.Idem{})
	}
	for name, ctx := range map[string]context.Context{"creator": cctx, "submitter": sctx} {
		if _, err := act(ctx, domain.POActionApprove); problemCode(err) != domain.CodeFourEyes {
			t.Errorf("%s approving: %v", name, err)
		}
	}
	po, err = act(actx, domain.POActionApprove)
	if err != nil {
		t.Fatalf("a third user approves: %v", err)
	}
	if _, err := act(cctx, domain.POActionConfirm); problemCode(err) != domain.CodeFourEyes {
		t.Errorf("creator confirming: %v", err)
	}
	if po, err = act(actx, domain.POActionConfirm); err != nil || po.ApprovalStatus != domain.POStatusConfirmed {
		t.Fatalf("a third user confirms: %v", err)
	}

	// A change order: its editor is the fourth user and cannot approve it; the
	// approver of version 1 can.
	editor := ids.New()
	ectx := asUser(1, editor)
	po, err = s.EditPurchaseOrder(ectx, po.ID, po.RowVersion, edited(po, lifecyclePO(t, "", "20", "2027-01-05")))
	if err != nil || po.ApprovalStatus != domain.POStatusInReview || po.ChangedBy != editor {
		t.Fatalf("change order: %s changedBy %q, %v", po.ApprovalStatus, po.ChangedBy, err)
	}
	if _, err := act(ectx, domain.POActionApprove); problemCode(err) != domain.CodeFourEyes {
		t.Errorf("editor approving their change: %v", err)
	}
	if po, err = act(actx, domain.POActionApprove); err != nil {
		t.Fatalf("another user approves the change: %v", err)
	}
	if _, err := act(ectx, domain.POActionConfirm); problemCode(err) != domain.CodeFourEyes {
		t.Errorf("editor confirming their change: %v", err)
	}
	if po, err = act(actx, domain.POActionConfirm); err != nil || po.VersionNo != 2 {
		t.Fatalf("another user confirms version 2: %+v, %v", po, err)
	}

	// A caller with no user id cannot show a second person.
	anon := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	pending, err := s.EditPurchaseOrder(ectx, po.ID, po.RowVersion, edited(po, lifecyclePO(t, "", "40", "2027-01-05")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnPurchaseOrder(anon, pending.ID, pending.RowVersion, domain.POActionApprove, store.Idem{}); problemCode(err) != domain.CodeFourEyes {
		t.Errorf("approve with no user id: %v", err)
	}

	// Policy off: the same user does everything.
	if _, err := s.PutPolicy(cctx, domain.Policy{LegalEntityID: legalEntity, ChangeManagementDefault: true}, 1); err != nil {
		t.Fatal(err)
	}
	solo, err := s.CreatePurchaseOrder(cctx, lifecyclePO(t, ids.New(), "10", "2027-01-05"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if solo = walk(t, cctx, s, solo, domain.POActionSubmit, domain.POActionApprove, domain.POActionConfirm); solo.ApprovalStatus != domain.POStatusConfirmed {
		t.Errorf("without four-eyes: %s", solo.ApprovalStatus)
	}
}

// BR-17.7: a quantity below invoiced is refused until the policy allows it; one
// below received never is.
func TestPolicy_AllowInvoiceBeforeReceiptLowersTheFloor(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := asUser(1, ids.New())
	po := confirmedPO(t, ctx, s, lifecyclePO(t, ids.New(), "100", "2027-01-05"))
	setLineQuantities(t, ctx, pool, po.ID, "20", "60")
	if _, err := s.EditPurchaseOrder(ctx, po.ID, po.RowVersion, edited(po, lifecyclePO(t, "", "50", "2027-01-05"))); problemCode(err) != domain.CodeReceivedFloor {
		t.Fatalf("below invoiced without the policy: %v", err)
	}
	if _, err := s.PutPolicy(ctx, domain.Policy{LegalEntityID: legalEntity, ChangeManagementDefault: true, AllowInvoiceBeforeReceipt: true}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditPurchaseOrder(ctx, po.ID, po.RowVersion, edited(po, lifecyclePO(t, "", "10", "2027-01-05"))); problemCode(err) != domain.CodeReceivedFloor {
		t.Errorf("below received with the policy: %v", err)
	}
	out, err := s.EditPurchaseOrder(ctx, po.ID, po.RowVersion, edited(po, lifecyclePO(t, "", "50", "2027-01-05")))
	if err != nil || !out.Lines[0].Qty.Equal(dec("50")) {
		t.Fatalf("below invoiced with the policy: %+v, %v", out, err)
	}
}

// The policy table is scoped by legal entity: a user of another legal entity
// in the same tenant neither sees nor overwrites it.
func TestPolicy_LegalEntityScope(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(3), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(3), LegalEntityID: entityB, UserID: ids.New()})
	if _, err := s.PutPolicy(ctxA, domain.Policy{LegalEntityID: entityA, POFourEyes: true}, 0); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListPolicies(ctxB); err != nil || len(list) != 0 {
		t.Errorf("entity B lists %+v, %v", list, err)
	}
	if got, err := s.GetPolicy(ctxB, entityA); err != nil || got.Version != 0 || got.POFourEyes {
		t.Errorf("entity B reads entity A's policy: %+v, %v", got, err)
	}
	if _, err := s.PutPolicy(ctxB, domain.Policy{LegalEntityID: entityA}, 0); err == nil {
		t.Error("entity B created a policy row for entity A")
	}
	if got, _ := s.GetPolicy(ctxA, entityA); got.Version != 1 || !got.POFourEyes {
		t.Errorf("entity A's policy changed: %+v", got)
	}
}
