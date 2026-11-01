package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/inventory"
)

type holdCalls struct {
	calls []string
	got   []inventory.Call
	fail  error
}

func (h *holdCalls) record(action string, call inventory.Call) error {
	if h.fail != nil {
		return h.fail
	}
	h.calls = append(h.calls, action+" "+call.SourceType+":"+call.SourceRef)
	h.got = append(h.got, call)
	return nil
}

func (h *holdCalls) Place(_ context.Context, c inventory.Call) error   { return h.record("place", c) }
func (h *holdCalls) Release(_ context.Context, c inventory.Call) error { return h.record("release", c) }

func holdOrder(t *testing.T, s *Postgres, ctx context.Context) (domain.Order, domain.TestGroup) {
	t.Helper()
	outcome := ids.New()
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "GRP-HOLD", Tests: []domain.TestLine{
		{TestID: ids.New(), Name: "appearance", Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{outcome}},
	}}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	order := domain.Order{ID: ids.New(), Trigger: domain.TriggerManual, SourceRef: domain.SourceRef{Type: "po_line", ID: ids.New()}, ItemID: ids.New(),
		SiteID: ids.New(), WarehouseID: ids.New(), Dims: map[string]string{"batch": "B1", "note": "ignored"}, LotQty: decimal.NewFromInt(100), SampleQty: decimal.NewFromInt(20),
		UOM: "EA", TestGroupID: group.ID, TestGroupVersion: 1, SamplingMethod: domain.MethodAQL, Severity: domain.SeverityNormal, CodeLetter: "E", AcceptNo: 1, RejectNo: 2}
	created, _, err := s.CreateOrder(ctx, order, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	return created, group
}

func pendingLinks(t *testing.T, s *Postgres, ctx context.Context, state string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `select count(*) from quality_hold_link where state = $1`, state).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestHoldLink_FailedInspectionPlacesTheLotAndTheDecisionReleasesTheAcceptedQuantity(t *testing.T) {
	base, _, ctx := setup(t)
	port := &holdCalls{}
	s := base.WithHolds(port)
	order, group := holdOrder(t, s, ctx)

	passing := []domain.Result{{TestID: group.Tests[0].TestID, SampleNo: 1, OutcomeID: ids.New(), Passed: true, RecordedBy: "u1"}}
	if _, _, err := s.RecordResults(ctx, order.ID, 1, domain.StatusInProgress, passing, "r-0"); err != nil {
		t.Fatal(err)
	}
	if len(port.calls) != 0 {
		t.Fatalf("a passing result held stock: %v", port.calls)
	}
	failing := []domain.Result{{TestID: group.Tests[0].TestID, SampleNo: 2, OutcomeID: ids.New(), Passed: false, RecordedBy: "u1"}}
	if _, _, err := s.RecordResults(ctx, order.ID, 2, domain.StatusPending, failing, "r-1"); err != nil {
		t.Fatal(err)
	}
	want := "place QUALITY_ORDER:" + order.ID
	if len(port.calls) != 1 || port.calls[0] != want {
		t.Fatalf("calls %v, want [%s]", port.calls, want)
	}
	c := port.got[0]
	if !c.Qty.Equal(decimal.NewFromInt(100)) || c.Dims["site"] != order.SiteID || c.Dims["warehouse"] != order.WarehouseID || c.Dims["batch"] != "B1" || len(c.Dims) != 3 ||
		c.ProductID != order.ItemID || c.LegalEntityID != "00000000-0000-0000-0000-0000000000e1" || c.UserID != "user-test" || c.Key != "quality-order:"+order.ID+":place" {
		t.Fatalf("place call %+v", c)
	}

	lines := []domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(90)}, {Decision: domain.DecisionReject, Quantity: decimal.NewFromInt(10)}}
	split, err := domain.SplitDecision(lines, order.LotQty)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PostUsageDecision(ctx, order.ID, 3, domain.Decision{Lines: lines, Split: split, DecidedBy: "u1"}, "d-1"); err != nil {
		t.Fatal(err)
	}
	if len(port.calls) != 2 || port.calls[1] != "release QUALITY_ORDER:"+order.ID || !port.got[1].KeepQty.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("calls %v keep %v, want the lot released down to the 10 rejected", port.calls, port.got[len(port.got)-1].KeepQty)
	}
	if pendingLinks(t, s, ctx, holdDone) != 2 || pendingLinks(t, s, ctx, holdPending) != 0 {
		t.Fatal("both links should be DONE")
	}

	ncrs, err := s.ListNonconformances(ctx, NCRFilter{QualityOrderID: order.ID, Limit: 5})
	if err != nil || len(ncrs) != 1 {
		t.Fatalf("decision NCR: %v %d", err, len(ncrs))
	}
	refuse := func(n domain.Nonconformance) (domain.Nonconformance, error) {
		return n.Refuse("not a defect", time.Now())
	}
	if _, _, err := s.UpdateNonconformance(ctx, ncrs[0].ID, ncrs[0].Version, "", refuse); err != nil {
		t.Fatal(err)
	}
	if len(port.calls) != 3 || port.calls[2] != "release QUALITY_ORDER:"+order.ID || !port.got[2].KeepQty.IsZero() {
		t.Fatalf("a refused NCR releases the lot it held: %v", port.calls)
	}
}

func TestHoldLink_NonconformanceWithoutAnOrderHoldsItsOwnQuantityUntilDisposition(t *testing.T) {
	base, _, ctx := setup(t)
	port := &holdCalls{}
	s := base.WithHolds(port)
	n := newNCR(t)
	n.Dims = map[string]string{"site": "S1", "warehouse": "W1", "batch": "B9", "inventory_status": "BLOCKED"}
	created, _, err := s.CreateNonconformance(ctx, n, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if len(port.calls) != 1 || port.calls[0] != "place NONCONFORMANCE:"+created.ID {
		t.Fatalf("calls %v", port.calls)
	}
	if got := port.got[0]; !got.Qty.Equal(n.Qty) || got.Dims["batch"] != "B9" || got.Dims["inventory_status"] != "" {
		t.Fatalf("place call %+v", got)
	}
	if _, _, err := s.CreateNonconformance(ctx, newNCR(t), "k1"); err != nil || len(port.calls) != 1 {
		t.Fatalf("a replayed create holds nothing more: %v %v", err, port.calls)
	}
	refuse := func(n domain.Nonconformance) (domain.Nonconformance, error) { return n.Refuse("duplicate", time.Now()) }
	if _, _, err := s.UpdateNonconformance(ctx, created.ID, 1, "", refuse); err != nil {
		t.Fatal(err)
	}
	if len(port.calls) != 2 || port.calls[1] != "release NONCONFORMANCE:"+created.ID || !port.got[1].KeepQty.IsZero() {
		t.Fatalf("calls %v", port.calls)
	}
	approve := func(n domain.Nonconformance) (domain.Nonconformance, error) { return n.Approve(time.Now()) }
	other, _, _ := s.CreateNonconformance(ctx, newNCR(t), "")
	if _, _, err := s.UpdateNonconformance(ctx, other.ID, 1, "", approve); err != nil || len(port.calls) != 3 {
		t.Fatalf("approving an NCR keeps its hold: %v %v", err, port.calls)
	}
}

func TestHoldLink_AFailingCallIsRetriedInOrderAndARefusedCallIsNot(t *testing.T) {
	base, _, ctx := setup(t)
	port := &holdCalls{fail: errors.New("inventory down")}
	s := base.WithHolds(port)
	first, _, err := s.CreateNonconformance(ctx, newNCR(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateNonconformance(ctx, newNCR(t), "b")
	if err != nil {
		t.Fatal(err)
	}
	if pendingLinks(t, s, ctx, holdPending) != 2 || len(port.calls) != 0 {
		t.Fatalf("an unreachable inventory leaves both holds queued, calls %v", port.calls)
	}
	port.fail = nil
	s.FlushHolds(ctx)
	if len(port.calls) != 2 || port.calls[0] != "place NONCONFORMANCE:"+first.ID || port.calls[1] != "place NONCONFORMANCE:"+second.ID {
		t.Fatalf("queued holds are sent in order: %v", port.calls)
	}
	port.fail = fmt.Errorf("%w: 422 INV-HLD-001", inventory.ErrRejected)
	third, _, _ := s.CreateNonconformance(ctx, newNCR(t), "c")
	port.fail = nil
	s.FlushHolds(ctx)
	if pendingLinks(t, s, ctx, holdFailed) != 1 || len(port.calls) != 2 {
		t.Fatalf("a refused hold for %s is FAILED and never resent: %v", third.ID, port.calls)
	}
}

func TestHoldLink_NothingIsQueuedWithoutInventoryOrALegalEntity(t *testing.T) {
	s, _, ctx := setup(t)
	if _, _, err := s.CreateNonconformance(ctx, newNCR(t), ""); err != nil {
		t.Fatal(err)
	}
	if pendingLinks(t, s, ctx, holdPending) != 0 {
		t.Fatal("a store without an inventory client queued a hold")
	}
	port := &holdCalls{}
	linked := s.WithHolds(port)
	tenantWide := withoutLegalEntity(ctx)
	if _, _, err := linked.CreateNonconformance(tenantWide, newNCR(t), ""); err != nil || pendingLinks(t, s, ctx, holdPending) != 0 || len(port.calls) != 0 {
		t.Fatalf("a caller with no single legal entity cannot sign a hold: %v %v", err, port.calls)
	}
}

func withoutLegalEntity(ctx context.Context) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.LegalEntityID = ""
	return tenant.With(ctx, p)
}
