package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

const (
	approverA = "0190a3c4-0000-7000-8000-00000000000a"
	approverB = "0190a3c4-0000-7000-8000-00000000000b"
	outsider  = "0190a3c4-0000-7000-8000-00000000000c"
)

// thresholdFixture saves a credit profile and a 25 USD threshold policy with
// the named group for a new customer, and returns a confirmed order of
// 5 x 10 + 5 x 7 = 85 USD for it.
func thresholdFixture(t *testing.T, s *Postgres, ctx context.Context, approvers []string) (ChangePolicy, Order, time.Time) {
	t.Helper()
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	threshold := decimal.RequireFromString("25")
	saved, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: true, ApprovalThreshold: &threshold,
		ThresholdCurrency: "USD", ApproverIDs: approvers}, 0, "manager")
	if err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 10, 7)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	conf, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !conf.Confirmed {
		t.Fatalf("confirm: %v", err)
	}
	return saved, conf.Order, now
}

func unapprovedValue(t *testing.T, pool *pgxpool.Pool, orderID string) decimal.Decimal {
	t.Helper()
	var v decimal.Decimal
	if err := pool.QueryRow(context.Background(), `select unapproved_change_value from sales_order where id = $1`, orderID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestPostgres_ChangeApprovalThresholdAndApprovers covers FR-27.9 slice 27: a
// customer with a 25 USD threshold has a small change applied at once; a
// second small change that together with the first reaches the threshold is
// stored for approval (no splitting a large change into small ones); a named
// approver group limits who may decide; a cancellation counts the whole total.
func TestPostgres_ChangeApprovalThresholdAndApprovers(t *testing.T) {
	s, pool, ctx := migrated(t)
	saved, order, now := thresholdFixture(t, s, ctx, []string{approverB, approverA, approverA})
	if saved.ApprovalThreshold == nil || !saved.ApprovalThreshold.Equal(decimal.NewFromInt(25)) || saved.ThresholdCurrency != "USD" ||
		len(saved.ApproverIDs) != 2 || saved.ApproverIDs[0] != approverA {
		t.Fatalf("saved policy: %+v", saved)
	}
	var data salesevents.ChangePolicyChangedData
	if err := json.Unmarshal(outboxData(t, pool, saved.CustomerID, salesevents.ChangePolicyChanged), &data); err != nil || data.ApprovalThreshold != "25.0000" ||
		data.ThresholdCurrency != "USD" || len(data.ApproverIDs) != 2 || data.ApproverIDs[0] != approverA {
		t.Fatalf("changepolicy.changed data %+v %v", data, err)
	}
	// An invalid policy is refused and stores nothing.
	bad := saved
	zero := decimal.Zero
	bad.ApprovalThreshold = &zero
	if _, err := s.PutChangePolicy(ctx, bad, saved.Version, "manager"); problemCode(t, err) != CodeInvalidChange {
		t.Fatalf("zero threshold: %v", err)
	}
	bad = saved
	bad.ApproverIDs = []string{"mgr-a"}
	if _, err := s.PutChangePolicy(ctx, bad, saved.Version, "manager"); problemCode(t, err) != CodeInvalidChange {
		t.Fatalf("an approver that is not a user id: %v", err)
	}

	// +20 is below the threshold: applied at once, no request stored, and counted.
	small, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("7")}}}, "sales-rep", now)
	if err != nil || small.PendingChange != nil || small.Version != order.Version+1 || !small.NetTotal.Equal(d(105)) {
		t.Fatalf("a change below the threshold applies at once: %+v %v", small.PendingChange, err)
	}
	if v := unapprovedValue(t, pool, order.ID); !v.Equal(d(20)) {
		t.Fatalf("unapproved change value after +20: %s", v)
	}
	// A date-only change moves no money: applied at once, nothing counted.
	dated, err := s.ChangeOrder(ctx, order.ID, small.Version, ChangeRequest{Lines: []LineChange{{LineNo: 2, RequestedShipDate: sp("2027-03-05")}}}, "sales-rep", now)
	if err != nil || dated.PendingChange != nil || dated.Version != small.Version+1 || !unapprovedValue(t, pool, order.ID).Equal(d(20)) {
		t.Fatalf("a date-only change applies at once: %+v %v", dated.PendingChange, err)
	}
	// A cut of only 10 would pass on its own, but with the earlier +20 it moves
	// the order by 30 without approval: stored.
	split, err := s.ChangeOrder(ctx, order.ID, dated.Version, ChangeRequest{Reason: "second half", Lines: []LineChange{{LineNo: 1, Quantity: dp("6")}}}, "sales-rep", now)
	if err != nil || split.PendingChange == nil || split.Version != dated.Version || !split.NetTotal.Equal(d(105)) {
		t.Fatalf("a split change that reaches the threshold waits: %+v %v", split.PendingChange, err)
	}
	req := split.PendingChange

	// Only the named approvers decide; the duty alone is not enough.
	if _, err := s.ApproveChangeRequest(ctx, req.ID, req.Version, "", outsider, now); problemCode(t, err) != CodeNotChangeApprover {
		t.Fatalf("approve by a user outside the group: %v", err)
	}
	if _, err := s.RejectChangeRequest(ctx, req.ID, req.Version, "no", outsider, now); problemCode(t, err) != CodeNotChangeApprover {
		t.Fatalf("reject by a user outside the group: %v", err)
	}
	res, err := s.ApproveChangeRequest(ctx, req.ID, req.Version, "ok", approverA, now)
	if err != nil || res.Request.Status != ChangeApproved || !res.Order.NetTotal.Equal(d(95)) {
		t.Fatalf("approve by a group member: %+v %v", res.Request, err)
	}
	if v := unapprovedValue(t, pool, order.ID); !v.Equal(d(20)) {
		t.Fatalf("an approved change is not counted as unapproved: %s", v)
	}

	// The approve duty without group membership does not skip approval: the
	// PATCH and the cancel of an outsider are stored like a requester's.
	direct, err := s.ChangeOrder(ctx, order.ID, res.Order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("9")}}, Approver: true}, outsider, now)
	if err != nil || direct.PendingChange == nil || direct.Version != res.Order.Version {
		t.Fatalf("an approver outside the group must not change the order directly: %+v %v", direct.PendingChange, err)
	}
	if _, err := s.WithdrawChangeRequest(ctx, direct.PendingChange.ID, direct.PendingChange.Version, "", outsider, now); err != nil {
		t.Fatal(err)
	}
	pending, err := s.CancelOrderAs(ctx, order.ID, res.Order.Version, "duplicate", outsider, true, now)
	if err != nil || pending.PendingChange == nil || pending.PendingChange.Kind != KindCancel {
		t.Fatalf("an approver outside the group must not cancel directly: %v", err)
	}
	// The requester can still withdraw whatever the group is.
	w, err := s.WithdrawChangeRequest(ctx, pending.PendingChange.ID, pending.PendingChange.Version, "", outsider, now)
	if err != nil || w.Status != ChangeWithdrawn {
		t.Fatalf("requester withdraws regardless of the group: %v", err)
	}
	// A group member changes it directly, and that is not counted either.
	member, err := s.ChangeOrder(ctx, order.ID, res.Order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("9")}}, Approver: true}, approverB, now)
	if err != nil || member.PendingChange != nil || !member.NetTotal.Equal(d(125)) || !unapprovedValue(t, pool, order.ID).Equal(d(20)) {
		t.Fatalf("a group member changes directly: %+v %v", member.PendingChange, err)
	}

	// A cancellation moves the whole total (125 >= 25): stored.
	cancel, err := s.CancelOrderAs(ctx, order.ID, member.Version, "duplicate", "sales-rep", false, now)
	if err != nil || cancel.PendingChange == nil || cancel.PendingChange.Kind != KindCancel {
		t.Fatalf("a cancel of an order above the threshold waits: %v", err)
	}
	if _, err := s.WithdrawChangeRequest(ctx, cancel.PendingChange.ID, cancel.PendingChange.Version, "", "sales-rep", now); err != nil {
		t.Fatal(err)
	}

	// A PUT that does not name the threshold or the group (the slice 13 body)
	// keeps them.
	kept, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: saved.LegalEntityID, CustomerID: saved.CustomerID, ApprovalRequired: true,
		KeepThreshold: true, KeepApprovers: true}, saved.Version, "manager")
	if err != nil || kept.ApprovalThreshold == nil || !kept.ApprovalThreshold.Equal(d(25)) || kept.ThresholdCurrency != "USD" || len(kept.ApproverIDs) != 2 {
		t.Fatalf("an old-style PUT must keep threshold and group: %+v %v", kept, err)
	}
	// Raise the threshold above the total plus what was moved (125 + 20) and
	// clear the group: the same cancel now applies at once.
	high := decimal.RequireFromString("1000")
	cleared, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: saved.LegalEntityID, CustomerID: saved.CustomerID, ApprovalRequired: true,
		ApprovalThreshold: &high, ThresholdCurrency: "USD"}, kept.Version, "manager")
	if err != nil || len(cleared.ApproverIDs) != 0 || !cleared.ApprovalThreshold.Equal(high) {
		t.Fatalf("a PUT naming an empty group clears it: %+v %v", cleared, err)
	}
	cancelled, err := s.CancelOrderAs(ctx, order.ID, member.Version, "duplicate", "sales-rep", false, now)
	if err != nil || cancelled.PendingChange != nil || cancelled.Status != "CANCELLED" {
		t.Fatalf("a cancel below the threshold applies at once: %+v %v", cancelled.PendingChange, err)
	}
}

// TestPostgres_ChangeThresholdCountsEachLine: a rise on one line and an equal
// cut on another leave the total as it was but move 2 x 15 = 30 >= 25, so
// the change waits; a swap of 2 x 10 = 20 applies at once.
func TestPostgres_ChangeThresholdCountsEachLine(t *testing.T) {
	s, pool, ctx := migrated(t)
	_, order, now := thresholdFixture(t, s, ctx, nil)
	small, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, UnitPrice: dp("12")}, {LineNo: 2, UnitPrice: dp("5")}}}, "sales-rep", now)
	if err != nil || small.PendingChange != nil || !small.NetTotal.Equal(d(85)) || !unapprovedValue(t, pool, order.ID).Equal(d(20)) {
		t.Fatalf("a 2 x 10 swap applies at once and counts 20: %+v %v", small.PendingChange, err)
	}
	_, order2, _ := thresholdFixture(t, s, ctx, nil)
	swap, err := s.ChangeOrder(ctx, order2.ID, order2.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, UnitPrice: dp("13")}, {LineNo: 2, UnitPrice: dp("4")}}}, "sales-rep", now)
	if err != nil || swap.PendingChange == nil {
		t.Fatalf("a 2 x 15 swap waits although the total stays: %v", err)
	}
}

// TestPostgres_ChangeThresholdOtherCurrency: an order in another currency than
// the threshold has no rate, so every change of it needs approval.
func TestPostgres_ChangeThresholdOtherCurrency(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(0), ToleranceAmount: d(0), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits}, 0); err != nil {
		t.Fatal(err)
	}
	threshold := decimal.RequireFromString("1000")
	if _, err := s.PutChangePolicy(ctx, ChangePolicy{LegalEntityID: le, CustomerID: customer, ApprovalRequired: true, ApprovalThreshold: &threshold, ThresholdCurrency: "EUR"}, 0, "manager"); err != nil {
		t.Fatal(err)
	}
	order := changeOrderFixture(t, s, ctx, le, customer, 10)
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	conf, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || !conf.Confirmed {
		t.Fatalf("confirm: %v", err)
	}
	got, err := s.ChangeOrder(ctx, order.ID, conf.Order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: dp("6")}}}, "sales-rep", now)
	if err != nil || got.PendingChange == nil {
		t.Fatalf("a USD order against a EUR threshold needs approval: %v", err)
	}
}
