package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/inventory"
)

type holdCalls struct {
	mu    sync.Mutex
	calls []string
	got   []inventory.Call
	fail  error
}

func (h *holdCalls) record(action string, call inventory.Call) error {
	h.mu.Lock()
	defer h.mu.Unlock()
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
	s.FlushHolds(ctx)
	if len(port.calls) != 0 {
		t.Fatalf("a passing result held stock: %v", port.calls)
	}
	failing := []domain.Result{{TestID: group.Tests[0].TestID, SampleNo: 2, OutcomeID: ids.New(), Passed: false, RecordedBy: "u1"}}
	if _, _, err := s.RecordResults(ctx, order.ID, 2, domain.StatusPending, failing, "r-1"); err != nil {
		t.Fatal(err)
	}
	s.FlushHolds(ctx)
	want := "place QUALITY_ORDER:" + order.ID
	if len(port.calls) != 1 || port.calls[0] != want {
		t.Fatalf("calls %v, want [%s]", port.calls, want)
	}
	c := port.got[0]
	if !c.Qty.Equal(decimal.NewFromInt(100)) || !c.WholeLot || c.Dims["site"] != order.SiteID || c.Dims["warehouse"] != order.WarehouseID || c.Dims["batch"] != "B1" || len(c.Dims) != 3 ||
		c.ProductID != order.ItemID || c.LegalEntityID != "00000000-0000-0000-0000-0000000000e1" || c.UserID != "user-test" || c.Key != "quality-order:"+order.ID+":place" {
		t.Fatalf("place call %+v", c)
	}

	lines := []domain.DecisionLine{{Decision: domain.DecisionAccept, Quantity: decimal.NewFromInt(90)}, {Decision: domain.DecisionReject, Quantity: decimal.NewFromInt(10)}}
	split, err := domain.SplitDecision(lines, order.LotQty)
	if err != nil {
		t.Fatal(err)
	}
	manual := newNCR(t)
	manual.QualityOrderID = order.ID
	manualNCR, _, err := s.CreateNonconformance(ctx, manual, "m-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PostUsageDecision(ctx, order.ID, 3, domain.Decision{Lines: lines, Split: split, DecidedBy: "u1"}, "d-1"); err != nil {
		t.Fatal(err)
	}
	s.FlushHolds(ctx)
	ncrs, err := s.ListNonconformances(ctx, NCRFilter{QualityOrderID: order.ID, Limit: 5})
	if err != nil || len(ncrs) != 2 {
		t.Fatalf("decision NCR: %v %d", err, len(ncrs))
	}
	decisionNCR := ncrs[0]
	if decisionNCR.ID == manualNCR.ID {
		decisionNCR = ncrs[1]
	}
	last := port.got[len(port.got)-1]
	if len(port.calls) != 2 || port.calls[1] != "release QUALITY_ORDER:"+order.ID || !last.KeepQty.Equal(decimal.NewFromInt(10)) ||
		last.MoveToType != "NONCONFORMANCE" || last.MoveToRef != decisionNCR.ID {
		t.Fatalf("calls %v call %+v, want the lot released down to the 10 rejected and handed to the decision's NCR", port.calls, last)
	}
	if pendingLinks(t, s, ctx, holdDone) != 2 || pendingLinks(t, s, ctx, holdPending) != 0 {
		t.Fatal("both links should be DONE")
	}
	refuse := func(n domain.Nonconformance) (domain.Nonconformance, error) {
		return n.Refuse("not a defect", time.Now())
	}
	if _, _, err := s.UpdateNonconformance(ctx, manualNCR.ID, manualNCR.Version, "", refuse); err != nil {
		t.Fatal(err)
	}
	s.FlushHolds(ctx)
	if len(port.calls) != 2 {
		t.Fatalf("refusing a nonconformance that placed nothing must not release the order's hold: %v", port.calls)
	}
	tenantWide := withoutLegalEntity(ctx, tenant.DutyAllScope)
	if _, _, err := s.UpdateNonconformance(tenantWide, decisionNCR.ID, decisionNCR.Version, "", refuse); err != nil {
		t.Fatal(err)
	}
	s.FlushHolds(tenantWide)
	last = port.got[len(port.got)-1]
	if len(port.calls) != 3 || port.calls[2] != "release NONCONFORMANCE:"+decisionNCR.ID || !last.KeepQty.IsZero() || last.LegalEntityID != "00000000-0000-0000-0000-0000000000e1" {
		t.Fatalf("the decision's NCR releases only what it holds, in the legal entity that placed it: %v %+v", port.calls, last)
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
	s.FlushHolds(ctx)
	if len(port.calls) != 1 || port.calls[0] != "place NONCONFORMANCE:"+created.ID {
		t.Fatalf("calls %v", port.calls)
	}
	if got := port.got[0]; !got.Qty.Equal(n.Qty) || got.Dims["batch"] != "B9" || got.Dims["inventory_status"] != "" {
		t.Fatalf("place call %+v", got)
	}
	_, _, err = s.CreateNonconformance(ctx, newNCR(t), "k1")
	s.FlushHolds(ctx)
	if err != nil || len(port.calls) != 1 {
		t.Fatalf("a replayed create holds nothing more: %v %v", err, port.calls)
	}
	refuse := func(n domain.Nonconformance) (domain.Nonconformance, error) { return n.Refuse("duplicate", time.Now()) }
	if _, _, err := s.UpdateNonconformance(ctx, created.ID, 1, "", refuse); err != nil {
		t.Fatal(err)
	}
	s.FlushHolds(ctx)
	if len(port.calls) != 2 || port.calls[1] != "release NONCONFORMANCE:"+created.ID || !port.got[1].KeepQty.IsZero() {
		t.Fatalf("calls %v", port.calls)
	}
	approve := func(n domain.Nonconformance) (domain.Nonconformance, error) { return n.Approve(time.Now()) }
	other, _, _ := s.CreateNonconformance(ctx, newNCR(t), "")
	_, _, err = s.UpdateNonconformance(ctx, other.ID, 1, "", approve)
	s.FlushHolds(ctx)
	if err != nil || len(port.calls) != 3 {
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
	s.FlushHolds(ctx)
	if pendingLinks(t, s, ctx, holdPending) != 2 || len(port.calls) != 0 {
		t.Fatalf("an unreachable inventory leaves both holds queued, calls %v", port.calls)
	}
	port.fail = nil
	s.FlushHolds(ctx)
	if len(port.calls) != 2 || port.calls[0] != "place NONCONFORMANCE:"+first.ID || port.calls[1] != "place NONCONFORMANCE:"+second.ID {
		t.Fatalf("queued holds are sent in order: %v", port.calls)
	}
	third, _, _ := s.CreateNonconformance(ctx, newNCR(t), "c")
	port.fail = fmt.Errorf("%w: 422 INV-HLD-001", inventory.ErrRejected)
	s.FlushHolds(ctx)
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

func withoutLegalEntity(ctx context.Context, duties ...string) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.LegalEntityID = ""
	p.Duties = map[string]struct{}{}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(ctx, p)
}

func TestHoldLink_AReleaseThatFindsNoHoldIsFailedAndAMissingSourceIsNotQueued(t *testing.T) {
	base, _, ctx := setup(t)
	port := &holdCalls{}
	s := base.WithHolds(port)
	created, _, err := s.CreateNonconformance(ctx, newNCR(t), "k")
	if err != nil {
		t.Fatal(err)
	}
	s.FlushHolds(ctx)
	port.fail = inventory.ErrNoHold
	refuse := func(n domain.Nonconformance) (domain.Nonconformance, error) { return n.Refuse("x", time.Now()) }
	if _, _, err := s.UpdateNonconformance(ctx, created.ID, 1, "", refuse); err != nil {
		t.Fatal(err)
	}
	s.FlushHolds(ctx)
	if pendingLinks(t, s, ctx, holdFailed) != 1 || pendingLinks(t, s, ctx, holdDone) != 1 {
		t.Fatal("a release inventory found nothing for must be FAILED, not DONE")
	}
}

func TestHoldLink_TheSweeperSendsAKickedTenantOffTheRequestPath(t *testing.T) {
	base, _, ctx := setup(t)
	port := &holdCalls{}
	s := base.WithHolds(port)
	run, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunHoldSweeper(run, nil, time.Hour, time.Minute); close(done) }()
	if _, _, err := s.CreateNonconformance(ctx, newNCR(t), "k"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(port.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	<-done
	if got := port.snapshot(); len(got) != 1 {
		t.Fatalf("calls %v, want the kicked tenant's one placement", got)
	}
}

func (h *holdCalls) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}
