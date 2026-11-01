package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// Cartons go on a pallet with its own SSCC, tare and returnable flag; the
// pallet and its cartons ship on one slip, the pallet raises the customer's
// returnable balance and the ASN names the pallet as parent. A carton on a pallet
// cannot be unpacked, and dissolving the pallet frees the cartons again.
func statusByID(sh store.Shipment) map[string]string {
	out := map[string]string{}
	for _, c := range sh.Containers {
		out[c.ID] = c.Status
	}
	return out
}

func TestNestContainers(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "EUR-PALLET", TareKg: d("25"), Returnable: true})
	f := released(t, s, ctx, 10, 5)
	l1, l2 := f.shipment.Lines[0], f.shipment.Lines[1]
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: l1.ID, Picked: d("10")}, {ShipmentLineID: l2.ID, Picked: d("5")}}, idem("p1"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.PackContainer(ctx, f.shipment.ID, picked.Version, "BOX", []store.PackInput{{ShipmentLineID: l1.ID, Qty: d("10")}}, idem("k1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.PackContainer(ctx, f.shipment.ID, a.Shipment.Version, "BOX", []store.PackInput{{ShipmentLineID: l2.ID, Qty: d("5")}}, idem("k2"))
	if err != nil {
		t.Fatal(err)
	}
	ver := b.Shipment.Version
	ids2 := []string{a.Container.ID, b.Container.ID}

	// Refusals write nothing: stale version, unknown carton, other tenant or legal entity, no cartons.
	if _, err := s.NestContainers(ctx, f.shipment.ID, ver+2, "EUR-PALLET", ids2, idem("n-stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale: %v", err)
	}
	if _, err := s.NestContainers(ctx, f.shipment.ID, ver, "EUR-PALLET", []string{ids.New()}, idem("n-unknown")); problemCode(err) != store.CodeContainerNotFound {
		t.Fatalf("unknown carton: %v", err)
	}
	if _, err := s.NestContainers(ctxFor(2, ""), f.shipment.ID, ver, "EUR-PALLET", ids2, idem("n-tenant")); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := s.NestContainers(ctxFor(1, ids.New()), f.shipment.ID, ver, "EUR-PALLET", ids2, idem("n-entity")); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	if _, err := s.NestContainers(ctx, f.shipment.ID, ver, "EUR-PALLET", nil, idem("n-none")); problemCode(err) != store.CodeNesting {
		t.Fatalf("no cartons: %v", err)
	}
	if n := countEvents(t, admin, fulevents.ContainerNested); n != 0 {
		t.Fatalf("refused nesting wrote %d events", n)
	}

	key := idem("n1")
	pal, err := s.NestContainers(ctx, f.shipment.ID, ver, "EUR-PALLET", ids2, key)
	if err != nil {
		t.Fatal(err)
	}
	if pal.Shipment.Version != ver+1 || pal.Shipment.Status != domain.StatusPacked || len(pal.Shipment.Containers) != 3 {
		t.Fatalf("after nest: version %d status %s containers %d", pal.Shipment.Version, pal.Shipment.Status, len(pal.Shipment.Containers))
	}
	if !pal.Container.TareKg.Equal(d("25")) || !pal.Container.Returnable || len(pal.Container.ChildContainerIDs) != 2 || len(pal.Container.Lines) != 0 {
		t.Fatalf("pallet = %+v", pal.Container)
	}
	if replay, err := s.NestContainers(ctx, f.shipment.ID, ver, "EUR-PALLET", ids2, key); err != nil || replay.Container.ID != pal.Container.ID {
		t.Fatalf("replayed nest: %v", err)
	}
	got := getShipment(t, s, ctx, f.shipment.ID)
	byID := map[string]store.Container{}
	for _, c := range got.Containers {
		byID[c.ID] = c
	}
	if len(byID) != 3 || byID[a.Container.ID].ParentContainerID == nil || *byID[a.Container.ID].ParentContainerID != pal.Container.ID || len(byID[pal.Container.ID].ChildContainerIDs) != 2 {
		t.Fatalf("loaded containers = %+v", got.Containers)
	}
	if !got.Lines[0].Packed.Equal(d("10")) || !got.Lines[1].Packed.Equal(d("5")) {
		t.Fatalf("nesting changed packed quantities: %+v", got.Lines)
	}
	if n := countEvents(t, admin, fulevents.ContainerNested); n != 1 {
		t.Fatalf("container.nested events = %d, want 1", n)
	}

	// Rules: a carton on a pallet is not nested again or unpacked; a pallet does not nest or unpack.
	if _, err := s.NestContainers(ctx, f.shipment.ID, pal.Shipment.Version, "EUR-PALLET", []string{a.Container.ID}, idem("n-again")); problemCode(err) != store.CodeNesting {
		t.Fatalf("carton already on a pallet: %v", err)
	}
	if _, err := s.NestContainers(ctx, f.shipment.ID, pal.Shipment.Version, "EUR-PALLET", []string{pal.Container.ID}, idem("n-pallet")); problemCode(err) != store.CodeNesting {
		t.Fatalf("pallet on a pallet: %v", err)
	}
	if _, err := s.UnpackContainer(ctx, f.shipment.ID, pal.Shipment.Version, a.Container.ID, idem("u-carton")); problemCode(err) != store.CodeNesting {
		t.Fatalf("unpack carton on a pallet: %v", err)
	}
	if _, err := s.UnpackContainer(ctx, f.shipment.ID, pal.Shipment.Version, pal.Container.ID, idem("u-pallet")); problemCode(err) != store.CodeNesting {
		t.Fatalf("unpack pallet: %v", err)
	}
	if _, err := s.UnnestContainer(ctxFor(1, ids.New()), f.shipment.ID, pal.Shipment.Version, pal.Container.ID, idem("un-entity")); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("unnest from another legal entity: %v", err)
	}
	if _, err := s.UnnestContainer(ctx, f.shipment.ID, pal.Shipment.Version, a.Container.ID, idem("un-carton")); problemCode(err) != store.CodeNesting {
		t.Fatalf("unnest a carton: %v", err)
	}
	if _, err := s.CancelShipment(ctx, f.shipment.ID, pal.Shipment.Version, idem("c")); problemCode(err) != store.CodeInvalidMove {
		t.Fatalf("cancel with a pallet: %v", err)
	}

	// Dissolve the pallet: the cartons are free, the pallet SSCC stays issued.
	un, err := s.UnnestContainer(ctx, f.shipment.ID, pal.Shipment.Version, pal.Container.ID, idem("un1"))
	if err != nil || len(un.Containers) != 2 || un.Containers[0].ParentContainerID != nil {
		t.Fatalf("unnest: %v containers %+v", err, un.Containers)
	}
	if n := countEvents(t, admin, fulevents.ContainerUnnested); n != 1 {
		t.Fatalf("container.unnested events = %d, want 1", n)
	}
	if _, err := s.UnpackContainer(ctx, f.shipment.ID, un.Version, a.Container.ID, idem("u-free")); err != nil {
		t.Fatalf("unpack a freed carton: %v", err)
	}
	re, err := s.PackContainer(ctx, f.shipment.ID, un.Version+1, "BOX", []store.PackInput{{ShipmentLineID: l1.ID, Qty: d("10")}}, idem("k3"))
	if err != nil {
		t.Fatal(err)
	}
	pal2, err := s.NestContainers(ctx, f.shipment.ID, re.Shipment.Version, "EUR-PALLET", []string{re.Container.ID, b.Container.ID}, idem("n2"))
	if err != nil || pal2.Container.SSCC == pal.Container.SSCC {
		t.Fatalf("second pallet: %v sscc %s old %s", err, pal2.Container.SSCC, pal.Container.SSCC)
	}

	// The pallet and both cartons ship on one slip, the pallet counts as one returnable.
	slip, err := s.PostPackingSlip(ctx, f.shipment.ID, pal2.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "2"), EmitASN: true}, idem("post"))
	if err != nil {
		t.Fatal(err)
	}
	st := statusByID(getShipment(t, s, ctx, f.shipment.ID))
	if st[pal2.Container.ID] != "SHIPPED" || st[re.Container.ID] != "SHIPPED" || st[b.Container.ID] != "SHIPPED" {
		t.Fatalf("statuses after post = %v", st)
	}
	items, err := s.ListReturnableBalances(ctx, f.shipment.CustomerID, 10)
	if err != nil || len(items) != 1 || items[0].ContainerType != "EUR-PALLET" || !items[0].Balance.Equal(d("1")) {
		t.Fatalf("balances = %+v, %v", items, err)
	}
	var parents int
	if err := admin.QueryRow(t.Context(), `select count(*) from outbox o, jsonb_array_elements(o.payload->'data'->'containers') c
		where o.type = $1 and c->>'parent_sscc' = $2`, fulevents.ASNIssued, pal2.Container.SSCC).Scan(&parents); err != nil || parents != 2 {
		t.Fatalf("ASN cartons with the pallet as parent = %d, %v; want 2", parents, err)
	}
	if _, err := s.NestContainers(ctx, f.shipment.ID, pal2.Shipment.Version+1, "EUR-PALLET", []string{b.Container.ID}, idem("n-shipped")); err == nil {
		t.Fatal("nesting a shipped carton was accepted")
	}
	// Cancelling the slip puts pallet and cartons back and takes the balance off.
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel")); err != nil {
		t.Fatalf("cancel slip: %v", err)
	}
	st = statusByID(getShipment(t, s, ctx, f.shipment.ID))
	if st[pal2.Container.ID] != "CLOSED" || st[b.Container.ID] != "CLOSED" {
		t.Fatalf("statuses after cancel = %v", st)
	}
}
