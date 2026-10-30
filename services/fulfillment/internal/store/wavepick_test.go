package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func TestConfirmWavePick_AllShipmentsOneCommand(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	warehouse, le, customer := ids.New(), ids.New(), ids.New()
	shipments := releaseMany(t, s, ctx, warehouse, le, []int{2, 2, 1}, []string{customer, customer, customer}, func(int) string { return "Z1" })
	waves, err := s.CreateWaves(ctx, store.WaveRequest{WarehouseID: warehouse, GroupBy: domain.GroupCarrier, Limits: domain.WaveLimits{MaxLines: 20, MaxOrders: 50}}, idem("w"))
	if err != nil || len(waves) != 1 {
		t.Fatalf("CreateWaves: %d waves, %v", len(waves), err)
	}
	wave := waves[0]
	short := shipments[0].Lines[0].ID
	other := shipments[1].Lines[0].ID

	// Refusals write nothing: a line outside the wave, a reason on a full pick,
	// an unknown reason, another tenant.
	outside := ids.New()
	if _, err := s.ConfirmWavePick(ctx, wave.ID, []store.PickInput{{ShipmentLineID: outside, Picked: d("1")}}, true, idem("x1")); problemCode(err) != store.CodeLineNotFound {
		t.Fatalf("line outside the wave: %v, want %s", err, store.CodeLineNotFound)
	}
	if _, err := s.ConfirmWavePick(ctx, wave.ID, []store.PickInput{{ShipmentLineID: other, Picked: d("1"), Reason: "DAMAGED"}}, true, idem("x2")); problemCode(err) != store.CodeValidation {
		t.Fatalf("reason on a full pick: %v, want %s", err, store.CodeValidation)
	}
	if _, err := s.ConfirmWavePick(ctx, wave.ID, []store.PickInput{{ShipmentLineID: short, Picked: d("0"), Reason: "LOST"}}, true, idem("x3")); problemCode(err) != store.CodeValidation {
		t.Fatalf("unknown reason: %v, want %s", err, store.CodeValidation)
	}
	if _, err := s.ConfirmWavePick(ctxFor(2, ""), wave.ID, nil, true, idem("x4")); problemCode(err) != store.CodeWaveNotFound {
		t.Fatalf("other tenant: %v, want %s", err, store.CodeWaveNotFound)
	}
	if n := countEvents(t, admin, fulevents.PickConfirmed); n != 0 {
		t.Fatalf("refused confirmations wrote %d events", n)
	}
	for _, sh := range shipments {
		if got := getShipment(t, s, ctx, sh.ID); got.Status != domain.StatusPicking || got.Lines[0].PickConfirmed {
			t.Fatalf("shipment %s changed by a refused confirmation: %s", sh.ShipmentNumber, got.Status)
		}
	}

	detail, err := s.GetWave(ctx, wave.ID)
	if err != nil || len(detail.Lines) != 5 || detail.Lines[0].PickConfirmed {
		t.Fatalf("wave detail lines: %+v, %v", detail.Lines, err)
	}
	key := idem("wp")
	res, err := s.ConfirmWavePick(ctx, wave.ID, []store.PickInput{{ShipmentLineID: short, Picked: d("0"), Reason: "OUT_OF_STOCK"}}, true, key)
	if err != nil {
		t.Fatalf("ConfirmWavePick: %v", err)
	}
	if res.LinesConfirmed != 5 || res.ShortLines != 1 || len(res.Shipments) != 3 {
		t.Fatalf("result %+v, want 5 lines, 1 short, 3 shipments", res)
	}
	if replay, err := s.ConfirmWavePick(ctx, wave.ID, []store.PickInput{{ShipmentLineID: short, Picked: d("0"), Reason: "OUT_OF_STOCK"}}, true, key); err != nil || replay.LinesConfirmed != 5 {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	if n := countEvents(t, admin, fulevents.PickConfirmed); n != 3 {
		t.Fatalf("pick.confirmed events = %d, want 3 (one per shipment, replay adds none)", n)
	}
	// The short line keeps its reason and the backorders report shows it; the
	// shipment with all lines confirmed is PICKED.
	got := getShipment(t, s, ctx, shipments[0].ID)
	if got.Status != domain.StatusPicked || got.Lines[0].ShortPickReason == nil || *got.Lines[0].ShortPickReason != "OUT_OF_STOCK" || !got.Lines[1].Picked.Equal(d("1")) {
		t.Fatalf("shipment 0: status %s line0 %+v line1 %+v", got.Status, got.Lines[0], got.Lines[1])
	}
	rows, err := s.ListBackorders(ctx, store.BackorderFilter{WarehouseID: warehouse, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Reason == nil || *rows[0].Reason != "OUT_OF_STOCK" {
		t.Fatalf("backorders: %+v, %v", rows, err)
	}
	if detail, err = s.GetWave(ctx, wave.ID); err != nil {
		t.Fatal(err)
	}
	confirmed := 0
	for _, l := range detail.Lines {
		if l.PickConfirmed {
			confirmed++
		}
		if l.ShipmentLineID == short && (l.ShortPickReason == nil || *l.ShortPickReason != "OUT_OF_STOCK" || !l.Picked.IsZero()) {
			t.Fatalf("short line in wave detail: %+v", l)
		}
	}
	if confirmed != 5 {
		t.Fatalf("confirmed lines in wave detail = %d, want 5", confirmed)
	}
	// Nothing is left to pick.
	if _, err := s.ConfirmWavePick(ctx, wave.ID, nil, true, idem("again")); problemCode(err) != store.CodeValidation {
		t.Fatalf("second confirmation: %v, want %s", err, store.CodeValidation)
	}
	// A listed line on a shipment that already picked is refused (FUL-0001).
	if _, err := s.ConfirmWavePick(ctx, wave.ID, []store.PickInput{{ShipmentLineID: other, Picked: d("1")}}, false, idem("late")); problemCode(err) != store.CodeStatus {
		t.Fatalf("listed line on a picked shipment: %v, want %s", err, store.CodeStatus)
	}
}

func TestConfirmPick_ShortReasonOnASingleShipment(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 10)
	l := f.shipment.Lines[0]
	if _, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: l.ID, Picked: d("10"), Reason: "DAMAGED"}}, idem("p0")); problemCode(err) != store.CodeValidation {
		t.Fatalf("reason on a full pick: %v, want %s", err, store.CodeValidation)
	}
	sh, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: l.ID, Picked: d("7"), Reason: "DAMAGED"}}, idem("p1"))
	if err != nil || sh.Lines[0].ShortPickReason == nil || *sh.Lines[0].ShortPickReason != "DAMAGED" {
		t.Fatalf("short pick: %+v, %v", sh.Lines, err)
	}
	rows, err := s.ListBackorders(ctx, store.BackorderFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Reason == nil || *rows[0].Reason != "DAMAGED" {
		t.Fatalf("backorders: %+v, %v", rows, err)
	}
}

// pickRest leaves a cancelled shipment of the wave alone, never re-picks a line
// another command already confirmed (here a single-shipment short pick), and
// the events carry the wave id and the short reason. Another legal entity does
// not see the wave.
func TestConfirmWavePick_PickRestSkipsCancelledAndConfirmed(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	warehouse, le, customer := ids.New(), ids.New(), ids.New()
	shipments := releaseMany(t, s, ctx, warehouse, le, []int{1, 2, 1}, []string{customer, customer, customer}, func(int) string { return "Z1" })
	waves, err := s.CreateWaves(ctx, store.WaveRequest{WarehouseID: warehouse, GroupBy: domain.GroupCarrier, Limits: domain.WaveLimits{MaxLines: 20, MaxOrders: 50}}, idem("w"))
	if err != nil || len(waves) != 1 {
		t.Fatalf("CreateWaves: %d waves, %v", len(waves), err)
	}
	wave := waves[0]
	if _, err := s.ConfirmWavePick(ctxFor(1, ids.New()), wave.ID, nil, true, idem("le")); problemCode(err) != store.CodeWaveNotFound {
		t.Fatalf("other legal entity: %v, want %s", err, store.CodeWaveNotFound)
	}
	cancelled := getShipment(t, s, ctx, shipments[0].ID)
	if _, err := s.CancelShipment(ctx, cancelled.ID, cancelled.Version, idem("cancel")); err != nil {
		t.Fatalf("CancelShipment: %v", err)
	}
	partly := getShipment(t, s, ctx, shipments[1].ID)
	single := partly.Lines[0]
	if _, err := s.ConfirmPick(ctx, partly.ID, partly.Version, []store.PickInput{{ShipmentLineID: single.ID, Picked: d("0"), Reason: "NOT_FOUND"}}, idem("single")); err != nil {
		t.Fatalf("single-shipment short pick: %v", err)
	}
	// A listed line on the cancelled shipment is refused as a whole.
	if _, err := s.ConfirmWavePick(ctx, wave.ID, []store.PickInput{{ShipmentLineID: cancelled.Lines[0].ID, Picked: d("1")}}, true, idem("c")); problemCode(err) != store.CodeStatus {
		t.Fatalf("listed line on a cancelled shipment: %v, want %s", err, store.CodeStatus)
	}
	res, err := s.ConfirmWavePick(ctx, wave.ID, nil, true, idem("rest"))
	if err != nil {
		t.Fatalf("ConfirmWavePick: %v", err)
	}
	if res.LinesConfirmed != 2 || res.ShortLines != 0 || len(res.Shipments) != 2 {
		t.Fatalf("result %+v, want 2 lines on 2 shipments, none short", res)
	}
	got := getShipment(t, s, ctx, partly.ID)
	if got.Status != domain.StatusPicked || !got.Lines[0].Picked.IsZero() || got.Lines[0].ShortPickReason == nil || *got.Lines[0].ShortPickReason != "NOT_FOUND" || !got.Lines[1].Picked.Equal(d("1")) {
		t.Fatalf("pickRest changed the confirmed short line: %+v", got.Lines)
	}
	if got := getShipment(t, s, ctx, cancelled.ID); got.Status != domain.StatusCancelled || got.Lines[0].PickConfirmed {
		t.Fatalf("cancelled shipment touched: %s %+v", got.Status, got.Lines)
	}
	var withWave, withReason int
	if err := admin.QueryRow(context.Background(), `select count(*) filter (where payload->'data'->>'wave_id' = $2),
			count(*) filter (where payload->'data'->'lines'->0->>'short_reason' = 'NOT_FOUND')
		from outbox where type = $1`, fulevents.PickConfirmed, wave.ID).Scan(&withWave, &withReason); err != nil {
		t.Fatal(err)
	}
	if withWave != 2 || withReason != 1 {
		t.Fatalf("pick.confirmed with wave_id = %d (want 2), with short_reason = %d (want 1)", withWave, withReason)
	}
}
