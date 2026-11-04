package store

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

// TestPostgres_RejectCreditHold covers the credit manager's other decision in
// the release queue (PRD §5.27.6): the CREDIT hold is REJECTED with a comment,
// the unconfirmed order is cancelled in the same transaction and its other
// ACTIVE holds are cleared, and both events are queued together.
func TestPostgres_RejectCreditHold(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	le, customer, product, site := ids.New(), ids.New(), ids.New(), ids.New()
	const day = "2027-03-04"
	limits := domain.CreditProfile{Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5), ToleranceAmount: d(1000), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits, Exposure: domain.Exposure{OpenAR: d(8500)}}, 0); err != nil {
		t.Fatal(err)
	}
	line := Line{LineNo: 1, ProductID: product, Quantity: d(1), Unit: "EA", SiteID: site, WarehouseID: ids.New(), UnitPrice: d(3000), DiscPct: d(0), LineNet: d(3000), RequestedShipDate: day}
	in := NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD", RequestedShipDate: day, Channel: "UI", RequestHash: "h1", Lines: []Line{line}}
	order, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	held, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || held.Confirmed || len(held.Order.Holds) != 1 {
		t.Fatalf("confirm over limit: %v %+v", err, held.Order.Holds)
	}
	// A hand-applied hold on the same order is cleared by the rejection.
	manual, err := s.ApplyHold(principalCtx("fraud-analyst"), order.ID, held.Order.Version, HoldRequest{HoldType: domain.HoldManual, ReasonCode: "CHECK_ADDRESS"}, "fraud-analyst")
	if err != nil {
		t.Fatal(err)
	}
	creditID := held.Order.Holds[0].ID
	manager := principalCtx("credit-manager")
	// The queue tells the screen the order still awaits confirmation, so it
	// offers the rejection.
	before, err := s.ListHolds(manager, HoldQueueFilter{LegalEntityID: le, HoldType: domain.HoldCredit, Limit: 10})
	if err != nil || len(before) != 1 || before[0].HoldID != creditID || before[0].OrderStatus != held.Order.Status || !domain.CanConfirm(before[0].OrderStatus) {
		t.Fatalf("queue before the rejection: %v %+v", err, before)
	}

	if _, err := s.RejectHold(ctx, order.ID, creditID, manual.Version, "too risky", "sales-rep", now); problemCode(t, err) != "AUTH-0003" {
		t.Fatalf("the user who applied the hold must not reject it: %v", err)
	}
	if _, err := s.RejectHold(manager, order.ID, creditID, manual.Version-1, "too risky", "credit-manager", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	var manualID string
	for _, h := range manual.Holds {
		if h.HoldType == domain.HoldManual {
			manualID = h.ID
		}
	}
	if _, err := s.RejectHold(manager, order.ID, manualID, manual.Version, "no", "credit-manager", now); problemCode(t, err) != CodeHoldNotRejectable {
		t.Fatalf("only CREDIT holds can be rejected: %v", err)
	}
	if _, err := s.RejectHold(manager, order.ID, ids.New(), manual.Version, "no", "credit-manager", now); problemCode(t, err) != CodeHoldNotFound {
		t.Fatalf("unknown hold: %v", err)
	}

	got, err := s.RejectHold(manager, order.ID, creditID, manual.Version, "exposure too high", "credit-manager", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "CANCELLED" || got.Version != manual.Version+1 || got.Lines[0].LineStatus != domain.LineCancelled {
		t.Fatalf("order %s v%d line %s", got.Status, got.Version, got.Lines[0].LineStatus)
	}
	byID := map[string]Hold{}
	for _, h := range got.Holds {
		byID[h.ID] = h
	}
	rej := byID[creditID]
	if rej.Status != domain.HoldRejected || rej.RejectedBy == nil || *rej.RejectedBy != "credit-manager" || rej.RejectionComment == nil || *rej.RejectionComment != "exposure too high" || rej.RejectedAt == nil {
		t.Fatalf("rejected hold %+v", rej)
	}
	if byID[manualID].Status != "CLEARED" {
		t.Fatalf("the manual hold must be cleared: %+v", byID[manualID])
	}
	if _, err := s.RejectHold(manager, order.ID, creditID, got.Version, "again", "credit-manager", now); problemCode(t, err) != CodeHoldNotActive {
		t.Fatalf("second rejection: %v", err)
	}
	queue, err := s.ListHolds(manager, HoldQueueFilter{LegalEntityID: le, Limit: 10})
	if err != nil || len(queue) != 0 {
		t.Fatalf("the rejected order must leave the queue: %v %+v", err, queue)
	}
	versions, err := s.ListOrderVersions(manager, order.ID)
	if err != nil || len(versions) == 0 || !strings.Contains(versions[0].Reason, "exposure too high") {
		t.Fatalf("version reason: %v %+v", err, versions)
	}

	types := outboxTypes(t, pool, order.ID)
	sort.Strings(types)
	want := []string{salesevents.CreditCheckEvaluated, salesevents.OrderCancelled, salesevents.OrderHoldApplied, salesevents.OrderHoldApplied, salesevents.OrderHoldRejected}
	sort.Strings(want)
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("outbox %v want %v", types, want)
	}
	var data salesevents.OrderHoldRejectedData
	if err := json.Unmarshal(outboxData(t, pool, order.ID, salesevents.OrderHoldRejected), &data); err != nil {
		t.Fatal(err)
	}
	if data.HoldID != creditID || data.Comment != "exposure too high" || data.Actor != "credit-manager" || data.Version != got.Version || data.HoldType != domain.HoldCredit {
		t.Fatalf("orderhold.rejected payload %+v", data)
	}

	// A rejected order cannot be confirmed.
	if _, err := s.ConfirmOrder(ctx, order.ID, got.Version, "sales-rep", now); problemCode(t, err) != CodeNotConfirmable {
		t.Fatalf("confirm after rejection: %v", err)
	}
}

// TestRejectionReason keeps the cancellation reason within the 500 characters
// order.cancelled.v1 allows, whatever the comment's length or script.
func TestRejectionReason(t *testing.T) {
	if got := rejectionReason("over the limit"); got != "Credit hold rejected: over the limit" {
		t.Fatalf("short comment: %q", got)
	}
	long := rejectionReason(strings.Repeat("é", 500))
	if n := len([]rune(long)); n != changeReasonMaxSize || !strings.HasPrefix(long, "Credit hold rejected: é") {
		t.Fatalf("long comment: %d runes", n)
	}
}
