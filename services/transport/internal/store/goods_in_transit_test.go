package store_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

// TestGoodsInTransit_AC349 is AC-34.9's goods-in-transit half against Postgres:
// FOB orders for the voyage's receipt lines carry the estimated freight and duty
// per line, and when they ship the events move ownership with estimates that
// sum to the voyage cost total to the cent. A receipt line moves once.
func TestGoodsInTransit_AC349(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	site := ids.New()
	voyage, err := s.CreateVoyage(ctx, newVoyage(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateGoodsInTransit(ctx, voyage.ID, "FOB", site, []int{1}, store.Idem{Key: "git-1", Fingerprint: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != domain.GITPending || first.VoyageReference != "MAEU-2611E" || first.InTransitWarehouse != "TRANSIT-"+site ||
		len(first.Lines) != 1 || first.Lines[0].Line != 1 || first.Version != 1 || first.Code != "" {
		t.Fatalf("first = %+v", first)
	}
	if replay, err := s.CreateGoodsInTransit(ctx, voyage.ID, "FOB", site, []int{1}, store.Idem{Key: "git-1", Fingerprint: "a"}); err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.CreateGoodsInTransit(ctx, voyage.ID, "FOB", site, []int{2}, store.Idem{Key: "git-1", Fingerprint: "b"}); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("reuse = %v", err)
	}
	if _, err := s.CreateGoodsInTransit(ctx, voyage.ID, "FOB", site, []int{1, 2}, store.Idem{}); problemCode(t, err) != domain.CodeGITState {
		t.Fatalf("line 1 twice = %v, want TMS-0119", err)
	}
	if _, err := s.CreateGoodsInTransit(ctx, voyage.ID, "FOB", site, []int{7}, store.Idem{}); problemCode(t, err) != domain.CodeGITState {
		t.Fatalf("line not on voyage = %v, want TMS-0119", err)
	}
	if _, err := s.CreateGoodsInTransit(ctx, voyage.ID, "DDP", site, nil, store.Idem{}); problemCode(t, err) != domain.CodeNoGoodsInTransit {
		t.Fatalf("DDP = %v, want TMS-0120", err)
	}
	if _, err := s.CreateGoodsInTransit(ctx, ids.New(), "FOB", site, nil, store.Idem{}); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("unknown voyage = %v", err)
	}
	rest, err := s.CreateGoodsInTransit(ctx, voyage.ID, "FOB", site, []int{3, 2}, store.Idem{})
	if err != nil || len(rest.Lines) != 2 || rest.Lines[0].Line != 2 {
		t.Fatalf("rest = %+v, %v", rest, err)
	}

	shippedAt := time.Date(2026, 6, 1, 2, 0, 0, 0, time.UTC)
	for _, g := range []domain.GoodsInTransit{first, rest} {
		shipped, err := s.ShipGoodsInTransit(ctx, g.ID, shippedAt)
		if err != nil {
			t.Fatal(err)
		}
		if shipped.Status != domain.GITInTransit || !shipped.ShippedAt.Equal(shippedAt) || shipped.Code != domain.CodeOwnershipMoved || shipped.Version != 2 {
			t.Fatalf("shipped = %+v", shipped)
		}
	}
	// A retried ship returns the order unchanged and publishes nothing more.
	if again, err := s.ShipGoodsInTransit(ctx, first.ID, shippedAt.Add(time.Hour)); err != nil || again.Version != 2 || !again.ShippedAt.Equal(shippedAt) {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	evts := eventsOf(t, admin, tmsevents.GoodsInTransitShipped)
	if len(evts) != 2 {
		t.Fatalf("shipped events = %d, want 2", len(evts))
	}
	sum, freight, duty := decimal.Zero, decimal.Zero, decimal.Zero
	for _, e := range evts {
		var p tmsevents.GoodsInTransitShippedPayload
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if p.Incoterm != "FOB" || p.TransferEvent != domain.TransferOriginShipment || p.InTransitWarehouse != "TRANSIT-"+site || e.Subject != p.OrderID {
			t.Fatalf("payload = %+v", p)
		}
		lines := decimal.Zero
		for _, l := range p.Lines {
			lines = lines.Add(dec(l.Estimated))
			for _, c := range l.Costs {
				switch c.CostType {
				case "FREIGHT":
					freight = freight.Add(dec(c.Amount))
				case "DUTY":
					duty = duty.Add(dec(c.Amount))
				}
			}
		}
		if !lines.Equal(dec(p.EstimatedTotal)) {
			t.Fatalf("order %s: lines sum %s, estimatedTotal %s", p.OrderID, lines, p.EstimatedTotal)
		}
		sum = sum.Add(dec(p.EstimatedTotal))
	}
	if !sum.Equal(voyage.CostTotal()) || !freight.Equal(dec("1000")) || !duty.Equal(dec("45.55")) {
		t.Fatalf("shipped estimates = %s (freight %s, duty %s), voyage costs %s", sum, freight, duty, voyage.CostTotal())
	}

	list, err := s.ListGoodsInTransit(ctx, store.GoodsInTransitFilter{VoyageID: voyage.ID, Status: domain.GITInTransit})
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if pending, _ := s.ListGoodsInTransit(ctx, store.GoodsInTransitFilter{Status: domain.GITPending}); len(pending) != 0 {
		t.Fatalf("pending = %+v", pending)
	}
	got, err := s.GetGoodsInTransit(ctx, rest.ID)
	if err != nil || len(got.Lines) != 2 || len(got.Lines[0].Costs) != 2 || !got.EstimatedTotal.Equal(rest.EstimatedTotal) {
		t.Fatalf("get = %+v, %v", got, err)
	}
}

func TestGoodsInTransit_PostedVoyageTenantAndConcurrency(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	site := ids.New()
	posted, err := s.CreateVoyage(ctx, newVoyage(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllocateVoyage(ctx, posted.ID, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGoodsInTransit(ctx, posted.ID, "FOB", site, nil, store.Idem{}); problemCode(t, err) != domain.CodeGITState {
		t.Fatalf("posted voyage = %v, want TMS-0119", err)
	}

	open, err := s.CreateVoyage(ctx, newVoyage(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	other := tenantCtx(2)
	if _, err := s.CreateGoodsInTransit(other, open.ID, "FOB", site, nil, store.Idem{}); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 create = %v", err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.CreateGoodsInTransit(ctx, open.ID, "CIF", site, nil, store.Idem{})
		}(i)
	}
	wg.Wait()
	created := 0
	var id string
	for _, err := range errs {
		if err == nil {
			created++
			continue
		}
		if problemCode(t, err) != domain.CodeGITState {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("%d concurrent orders for the same lines, want 1", created)
	}
	list, _ := s.ListGoodsInTransit(ctx, store.GoodsInTransitFilter{VoyageID: open.ID})
	if len(list) != 1 {
		t.Fatalf("orders = %+v", list)
	}
	id = list[0].ID
	if _, err := s.GetGoodsInTransit(other, id); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 get = %v", err)
	}
	if _, err := s.ShipGoodsInTransit(other, id, t0); problemCode(t, err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 ship = %v", err)
	}
	if l, _ := s.ListGoodsInTransit(other, store.GoodsInTransitFilter{}); len(l) != 0 {
		t.Fatalf("tenant 2 list = %+v", l)
	}
}
