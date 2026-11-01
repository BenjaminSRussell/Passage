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

func signingRequest(worker string) domain.SigningLimitRequest {
	return domain.SigningLimitRequest{LegalEntityID: legalEntity, WorkerID: worker, DocumentType: domain.DocPurchaseOrder, Currency: "USD",
		SpendingLimit: decimal.RequireFromString("1000"), ApprovalLimit: decimal.RequireFromString("5000.5"), Reason: "new buyer"}
}

func TestSigningLimit_RequestApproveRecertifyAndEvents(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	requester, approver, worker := ids.New(), ids.New(), ids.New()
	asRequester, asApprover, asWorker := asUser(1, requester), asUser(1, approver), asUser(1, worker)
	today := dayOf(t, "2026-03-01")

	req, err := s.CreateSigningLimitRequest(asRequester, signingRequest(worker), store.Idem{Key: "q1", Hash: "h1"})
	if err != nil || req.Version != 1 || req.Status != domain.SigningPending || req.RequestedBy != requester || !req.ApprovalLimit.Equal(decimal.RequireFromString("5000.5")) {
		t.Fatalf("create = %+v, %v", req, err)
	}
	if again, err := s.CreateSigningLimitRequest(asRequester, signingRequest(worker), store.Idem{Key: "q1", Hash: "h1"}); err != nil || again.ID != req.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.CreateSigningLimitRequest(asRequester, signingRequest(worker), store.Idem{Key: "q1", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Errorf("same key, other body: %v", err)
	}

	// The requester and the worker may not decide it.
	for name, ctx := range map[string]context.Context{"requester": asRequester, "worker": asWorker} {
		if _, err := s.DecideSigningLimitRequest(ctx, req.ID, 1, store.SigningApprove, "", today, store.Idem{Key: "x-" + name, Hash: "h"}); problemCode(err) != domain.CodeSigningSelfDecision {
			t.Errorf("%s decides: %v", name, err)
		}
	}
	if _, err := s.DecideSigningLimitRequest(asApprover, req.ID, 9, store.SigningApprove, "", today, store.Idem{Key: "d0", Hash: "h"}); problemCode(err) != store.CodeSigningStale {
		t.Errorf("stale If-Match: %v", err)
	}
	if got, _ := s.SigningLimitsFor(asApprover, legalEntity, worker, domain.DocPurchaseOrder); len(got) != 0 {
		t.Fatalf("a refused decision wrote a limit: %+v", got)
	}

	done, err := s.DecideSigningLimitRequest(asApprover, req.ID, 1, store.SigningApprove, "ok", today, store.Idem{Key: "d1", Hash: "h"})
	if err != nil || done.Status != domain.SigningApproved || done.Version != 2 || done.DecidedBy != approver || done.SigningLimitID == "" || done.DecisionNote != "ok" {
		t.Fatalf("approve = %+v, %v", done, err)
	}
	if replay, err := s.DecideSigningLimitRequest(asApprover, req.ID, 1, store.SigningApprove, "ok", today, store.Idem{Key: "d1", Hash: "h"}); err != nil || replay.Version != 2 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.DecideSigningLimitRequest(asApprover, req.ID, 2, store.SigningReject, "", today, store.Idem{Key: "d2", Hash: "h2"}); problemCode(err) != store.CodeSigningRequestDecided {
		t.Errorf("deciding twice: %v", err)
	}

	limits, err := s.SigningLimitsFor(asApprover, legalEntity, worker, domain.DocPurchaseOrder)
	if err != nil || len(limits) != 1 {
		t.Fatalf("limits = %+v, %v", limits, err)
	}
	l := limits[0]
	if l.ID != done.SigningLimitID || l.Version != 1 || !l.SpendingLimit.Equal(decimal.RequireFromString("1000")) || l.RecertifyBy.String() != "2027-03-01" ||
		l.LastRecertifiedOn == nil || l.LastRecertifiedOn.String() != "2026-03-01" {
		t.Fatalf("limit = %+v", l)
	}
	if other, _ := s.SigningLimitsFor(asApprover, legalEntity, worker, domain.DocRequisition); len(other) != 0 {
		t.Errorf("another document type: %+v", other)
	}

	// A second approved request for the same worker, type and currency changes the same limit.
	second := signingRequest(worker)
	second.ApprovalLimit = decimal.RequireFromString("9000")
	req2, err := s.CreateSigningLimitRequest(asRequester, second, store.Idem{Key: "q2", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideSigningLimitRequest(asApprover, req2.ID, 1, store.SigningApprove, "", dayOf(t, "2026-06-01"), store.Idem{Key: "d3", Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	changed, _ := s.GetSigningLimit(asApprover, l.ID)
	if changed.Version != 2 || !changed.ApprovalLimit.Equal(decimal.RequireFromString("9000")) || changed.RecertifyBy.String() != "2027-06-01" {
		t.Fatalf("changed limit = %+v", changed)
	}

	// A rejected request writes no limit.
	third := signingRequest(ids.New())
	req3, _ := s.CreateSigningLimitRequest(asRequester, third, store.Idem{Key: "q3", Hash: "h"})
	rej, err := s.DecideSigningLimitRequest(asApprover, req3.ID, 1, store.SigningReject, "no", today, store.Idem{Key: "d4", Hash: "h"})
	if err != nil || rej.Status != domain.SigningRejected || rej.SigningLimitID != "" {
		t.Fatalf("reject = %+v, %v", rej, err)
	}

	// Recertify: stale, own limit, then the date moves 12 months from today.
	if _, err := s.RecertifySigningLimit(asApprover, l.ID, 1, dayOf(t, "2027-04-01"), store.Idem{Key: "r0", Hash: "h"}); problemCode(err) != store.CodeSigningStale {
		t.Errorf("stale recertify: %v", err)
	}
	if _, err := s.RecertifySigningLimit(asWorker, l.ID, 2, dayOf(t, "2027-04-01"), store.Idem{Key: "r1", Hash: "h"}); problemCode(err) != domain.CodeSigningSelfDecision {
		t.Errorf("own limit: %v", err)
	}
	re, err := s.RecertifySigningLimit(asApprover, l.ID, 2, dayOf(t, "2027-04-01"), store.Idem{Key: "r2", Hash: "h"})
	if err != nil || re.Version != 3 || re.RecertifyBy.String() != "2028-04-01" || re.UpdatedBy != approver {
		t.Fatalf("recertify = %+v, %v", re, err)
	}
	if _, err := s.GetSigningLimit(asApprover, ids.New()); problemCode(err) != store.CodeSigningLimitNotFound {
		t.Errorf("unknown limit: %v", err)
	}

	// Lists.
	page, err := s.ListSigningLimits(asApprover, store.SigningLimitFilter{WorkerID: worker, Today: dayOf(t, "2028-05-01")}, "", 5)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("limits list = %+v, %v", page, err)
	}
	if exp, _ := s.ListSigningLimits(asApprover, store.SigningLimitFilter{Expired: true, Today: dayOf(t, "2028-05-01")}, "", 5); len(exp.Items) != 1 {
		t.Errorf("expired on 2028-05-01: %+v", exp.Items)
	}
	if exp, _ := s.ListSigningLimits(asApprover, store.SigningLimitFilter{Expired: true, Today: dayOf(t, "2028-04-01")}, "", 5); len(exp.Items) != 0 {
		t.Errorf("not yet expired: %+v", exp.Items)
	}
	reqs, err := s.ListSigningLimitRequests(asApprover, store.SigningRequestFilter{Status: domain.SigningPending}, "", 5)
	if err != nil || len(reqs.Items) != 0 {
		t.Errorf("pending requests = %+v, %v", reqs.Items, err)
	}
	all, _ := s.ListSigningLimitRequests(asApprover, store.SigningRequestFilter{}, "", 2)
	if len(all.Items) != 2 || all.Next == "" || all.Items[0].ID != req3.ID {
		t.Errorf("first page newest first = %+v", all)
	}

	counts := map[string]int{}
	for _, e := range outbox(t, asApprover, pool) {
		counts[e.Type]++
	}
	if counts[purchasingevents.SigningLimitChanged] != 3 {
		t.Errorf("events = %v; want 3 signinglimit.changed (two approvals, one recertification)", counts)
	}
	if other, _ := s.ListSigningLimits(asUser(2, approver), store.SigningLimitFilter{Today: today}, "", 5); len(other.Items) != 0 {
		t.Errorf("another tenant sees %d limits", len(other.Items))
	}
}

func TestSigningLimit_LegalEntityScopeHidesOtherEntities(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(93), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(93), LegalEntityID: entityB, UserID: ids.New()})
	in := signingRequest(ids.New())
	in.LegalEntityID = entityA
	req, err := s.CreateSigningLimitRequest(ctxA, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSigningLimitRequest(ctxB, req.ID); problemCode(err) != store.CodeSigningRequestNotFound {
		t.Errorf("entity B reads entity A's request: %v", err)
	}
	if _, err := s.CreateSigningLimitRequest(ctxB, in, store.Idem{}); err == nil {
		t.Error("entity B wrote a request for entity A")
	}
}
