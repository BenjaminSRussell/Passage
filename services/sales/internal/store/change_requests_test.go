package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func TestPostgres_ChangePolicyVersioning(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	got, err := s.GetChangePolicy(ctx, le, customer)
	if err != nil || got.Version != 0 || got.ApprovalRequired || got.UpdatedAt != nil {
		t.Fatalf("default: %+v %v", got, err)
	}
	in := ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: true}
	saved, err := s.PutChangePolicy(ctx, in, 0, "manager")
	if err != nil || saved.Version != 1 || !saved.ApprovalRequired || saved.UpdatedBy != "manager" {
		t.Fatalf("create: %+v %v", saved, err)
	}
	if _, err := s.PutChangePolicy(ctx, in, 0, "manager"); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("second create must conflict: %v", err)
	}
	in.ApprovalRequired = false
	if _, err := s.PutChangePolicy(ctx, in, 7, "manager"); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	off, err := s.PutChangePolicy(ctx, in, 1, "manager")
	if err != nil || off.Version != 2 || off.ApprovalRequired {
		t.Fatalf("replace: %+v %v", off, err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2`, customer, salesevents.ChangePolicyChanged); n != 2 {
		t.Fatalf("changepolicy.changed rows %d", n)
	}
}

// TestPostgres_ChangeNeedsApproval covers FR-27.9 "optional approval per
// customer": a rep's change of a confirmed order is stored, not applied; a
// second one is refused while it waits; a manager approves, which applies the
// change in the same transaction; an approver changes directly; and the
// requester cannot approve their own request.
func TestPostgres_ChangeNeedsApproval(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: true}, 0, "manager"); err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 10, 7)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	conf, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !conf.Confirmed {
		t.Fatalf("confirm: %+v %v", conf.Confirmed, err)
	}
	version := conf.Order.Version
	change := ChangeRequest{Reason: "customer call", Lines: []LineChange{{LineNo: 1, Quantity: dp("8")}}}

	pending, err := s.ChangeOrder(ctx, order.ID, version, change, "sales-rep", now)
	if err != nil || pending.PendingChange == nil || pending.Version != version || !pending.Lines[0].Quantity.Equal(d(5)) {
		t.Fatalf("a rep's change must be stored, not applied: %+v %v", pending.PendingChange, err)
	}
	req := pending.PendingChange
	if req.Status != ChangePending || req.BaseVersion != version || req.RequestedBy != "sales-rep" || req.OrderNumber != order.OrderNumber ||
		len(req.Diff) == 0 || req.Diff[0].Field != "quantity" || req.Change.Lines[0].Quantity == nil || !req.Change.Lines[0].Quantity.Equal(d(8)) {
		t.Fatalf("stored request: %+v", req)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, version, ChangeRequest{Lines: []LineChange{{LineNo: 2, Quantity: dp("2")}}}, "sales-rep", now); problemCode(t, err) != CodeChangePending {
		t.Fatalf("a second change while one waits: %v", err)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, version, ChangeRequest{Lines: []LineChange{{LineNo: 9, Quantity: dp("2")}}}, "sales-rep", now); problemCode(t, err) != CodeInvalidChange {
		t.Fatalf("an invalid change must be refused at submission: %v", err)
	}
	queue, err := s.ListChangeRequests(ctx, ChangeRequestFilter{Status: ChangePending})
	if err != nil || len(queue) != 1 || queue[0].ID != req.ID {
		t.Fatalf("queue: %+v %v", queue, err)
	}

	if _, err := s.ApproveChangeRequest(ctx, req.ID, req.Version, "ok", "sales-rep", now); problemCode(t, err) != CodeChangeSelfApproval {
		t.Fatalf("self approval: %v", err)
	}
	if _, err := s.ApproveChangeRequest(ctx, req.ID, 9, "ok", "manager", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale request version: %v", err)
	}
	res, err := s.ApproveChangeRequest(ctx, req.ID, req.Version, "approved", "manager", now)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if res.Request.Status != ChangeApproved || res.Request.DecidedBy == nil || *res.Request.DecidedBy != "manager" ||
		res.Request.AppliedVersion == nil || *res.Request.AppliedVersion != version+1 ||
		res.Order.Version != version+1 || !res.Order.Lines[0].Quantity.Equal(d(8)) || !res.Order.NetTotal.Equal(d(115)) {
		t.Fatalf("after approval: %+v order v%d total %s", res.Request, res.Order.Version, res.Order.NetTotal)
	}
	versions, err := s.ListOrderVersions(ctx, order.ID)
	if err != nil || versions[0].Version != version+1 || versions[0].Actor != "sales-rep" || versions[0].Reason != "customer call" {
		t.Fatalf("history: %+v %v", versions, err)
	}
	if _, err := s.ApproveChangeRequest(ctx, req.ID, res.Request.Version, "", "manager", now); problemCode(t, err) != CodeChangeDecided {
		t.Fatalf("approve twice: %v", err)
	}
	types := outboxTypes(t, pool, order.ID)
	if types[len(types)-2] != salesevents.OrderChanged {
		t.Fatalf("outbox tail %v", types)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1`, req.ID); n != 2 {
		t.Fatalf("requested and approved events: %d", n)
	}

	direct, err := s.ChangeOrder(ctx, order.ID, res.Order.Version, ChangeRequest{Approver: true, Lines: []LineChange{{LineNo: 2, Quantity: dp("2")}}}, "manager", now)
	if err != nil || direct.PendingChange != nil || direct.Version != res.Order.Version+1 {
		t.Fatalf("an approver changes directly: %+v %v", direct.PendingChange, err)
	}
	if _, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: false}, 1, "manager"); err != nil {
		t.Fatal(err)
	}
	free, err := s.ChangeOrder(ctx, order.ID, direct.Version, ChangeRequest{Lines: []LineChange{{LineNo: 2, Quantity: dp("3")}}}, "sales-rep", now)
	if err != nil || free.PendingChange != nil || free.Version != direct.Version+1 {
		t.Fatalf("approval switched off: %v", err)
	}
}

// TestPostgres_RejectAndStaleChangeRequest covers a rejection (comment kept,
// order untouched, a new request possible), and an approval refused because
// the order moved since the request was made.
func TestPostgres_RejectAndStaleChangeRequest(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: true}, 0, "manager"); err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 10)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	conf, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !conf.Confirmed {
		t.Fatal(err)
	}
	v := conf.Order.Version
	first, err := s.ChangeOrder(ctx, order.ID, v, ChangeRequest{Lines: []LineChange{{LineNo: 1, UnitPrice: dp("12")}}}, "sales-rep", now)
	if err != nil || first.PendingChange == nil {
		t.Fatalf("submit: %v", err)
	}
	rejected, err := s.RejectChangeRequest(ctx, first.PendingChange.ID, first.PendingChange.Version, "price is fixed", "manager", now)
	if err != nil || rejected.Status != ChangeRejected || rejected.DecisionComment != "price is fixed" || rejected.AppliedVersion != nil {
		t.Fatalf("reject: %+v %v", rejected, err)
	}
	if got, _ := s.GetOrder(ctx, order.ID); got.Version != v || !got.Lines[0].UnitPrice.Equal(d(10)) {
		t.Fatalf("a rejection must leave the order: v%d", got.Version)
	}
	if _, err := s.RejectChangeRequest(ctx, first.PendingChange.ID, rejected.Version, "again", "manager", now); problemCode(t, err) != CodeChangeDecided {
		t.Fatalf("reject twice: %v", err)
	}

	second, err := s.ChangeOrder(ctx, order.ID, v, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("9")}}}, "sales-rep", now)
	if err != nil || second.PendingChange == nil {
		t.Fatalf("a new request after a rejection: %v", err)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, v, ChangeRequest{Approver: true, Lines: []LineChange{{LineNo: 1, Quantity: dp("6")}}}, "boss", now); err != nil {
		t.Fatalf("approver direct change: %v", err)
	}
	if _, err := s.ApproveChangeRequest(ctx, second.PendingChange.ID, second.PendingChange.Version, "", "manager", now); problemCode(t, err) != CodeChangeStale {
		t.Fatalf("an order that moved: %v", err)
	}
	if got, err := s.GetChangeRequest(ctx, second.PendingChange.ID); err != nil || got.Status != ChangePending {
		t.Fatalf("a refused approval leaves the request PENDING: %+v %v", got, err)
	}
	if _, err := s.GetChangeRequest(ctx, ids.New()); problemCode(t, err) != CodeChangeNotFound {
		t.Fatalf("unknown request: %v", err)
	}
	history, err := s.ListChangeRequests(ctx, ChangeRequestFilter{OrderID: order.ID})
	if err != nil || len(history) != 2 {
		t.Fatalf("order history: %d %v", len(history), err)
	}
}

// TestPostgres_ChangeRequestLegalEntityScope checks that a change request and
// a customer's change policy are invisible to a caller scoped to another legal
// entity and to another tenant, who can neither decide the request nor see
// the switch, and that the request events name the legal entity.
func TestPostgres_ChangeRequestLegalEntityScope(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: true}, 0, "manager"); err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 10)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	conf, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !conf.Confirmed {
		t.Fatal(err)
	}
	sub, err := s.ChangeOrder(ctx, order.ID, conf.Order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("9")}}}, "sales-rep", now)
	if err != nil || sub.PendingChange == nil {
		t.Fatalf("submit: %v", err)
	}
	req := sub.PendingChange
	var data salesevents.OrderChangeRequestedData
	if err := json.Unmarshal(outboxData(t, pool, req.ID, salesevents.OrderChangeRequested), &data); err != nil || data.LegalEntityID != le || data.OrderID != order.ID {
		t.Fatalf("orderchange.requested data %+v %v", data, err)
	}

	for name, c := range map[string]context.Context{
		"other legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")}),
		"other tenant":       tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: testkit.ScopeDuties("")}),
	} {
		if _, err := s.GetChangeRequest(c, req.ID); problemCode(t, err) != CodeChangeNotFound {
			t.Fatalf("%s: get %v", name, err)
		}
		if list, err := s.ListChangeRequests(c, ChangeRequestFilter{Status: ChangePending}); err != nil || len(list) != 0 {
			t.Fatalf("%s: list %v %+v", name, err, list)
		}
		if _, err := s.ApproveChangeRequest(c, req.ID, req.Version, "", "x", now); problemCode(t, err) != CodeChangeNotFound {
			t.Fatalf("%s: approve %v", name, err)
		}
		if _, err := s.RejectChangeRequest(c, req.ID, req.Version, "no", "x", now); problemCode(t, err) != CodeChangeNotFound {
			t.Fatalf("%s: reject %v", name, err)
		}
		if pol, err := s.GetChangePolicy(c, le, customer); err != nil || pol.Version != 0 || pol.ApprovalRequired {
			t.Fatalf("%s: policy %+v %v", name, pol, err)
		}
	}
	if got, err := s.GetChangeRequest(ctx, req.ID); err != nil || got.Status != ChangePending || got.Version != req.Version {
		t.Fatalf("the request is untouched: %+v %v", got, err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "manager", Duties: testkit.ScopeDuties(le)})
	res, err := s.ApproveChangeRequest(scoped, req.ID, req.Version, "", "manager", now)
	if err != nil || res.Request.Status != ChangeApproved {
		t.Fatalf("a manager scoped to the legal entity approves: %v", err)
	}
	var decided salesevents.OrderChangeDecidedData
	if err := json.Unmarshal(outboxData(t, pool, req.ID, salesevents.OrderChangeApproved), &decided); err != nil || decided.LegalEntityID != le || decided.AppliedVersion == nil {
		t.Fatalf("orderchange.approved data %+v %v", decided, err)
	}
}
