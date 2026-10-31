package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func TestWave_AssignReassignAndCloseLifecycle(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	warehouse, le, customer := ids.New(), ids.New(), ids.New()
	shipments := releaseMany(t, s, ctx, warehouse, le, []int{1, 2, 1}, []string{customer, customer, customer}, func(int) string { return "Z1" })
	waves, err := s.CreateWaves(ctx, store.WaveRequest{WarehouseID: warehouse, GroupBy: domain.GroupCarrier, Limits: domain.WaveLimits{MaxLines: 20, MaxOrders: 50}}, idem("w"))
	if err != nil || len(waves) != 1 {
		t.Fatalf("CreateWaves: %d waves, %v", len(waves), err)
	}
	wave := waves[0]
	if wave.Version != 1 || wave.Picker != nil {
		t.Fatalf("new wave: version %d picker %v", wave.Version, wave.Picker)
	}
	if rows, err := s.ListWaves(ctx, store.WaveFilter{Unassigned: true, Status: "OPEN", Limit: 10}); err != nil || len(rows) != 1 || rows[0].ID != wave.ID {
		t.Fatalf("list unassigned before assign: %d, %v", len(rows), err)
	}

	// First assignment, replay, stale version, same picker, other tenant and scope.
	key := idem("a1")
	got, err := s.AssignWave(ctx, wave.ID, 1, "anna", key)
	if err != nil || got.Picker == nil || *got.Picker != "anna" || got.Version != 2 || got.AssignedAt == nil {
		t.Fatalf("AssignWave: %+v, %v", got.WaveSummary, err)
	}
	if replay, err := s.AssignWave(ctx, wave.ID, 1, "anna", key); err != nil || replay.Version != 2 {
		t.Fatalf("replay: %+v, %v", replay.WaveSummary, err)
	}
	if _, err := s.AssignWave(ctx, wave.ID, 1, "ben", idem("a2")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale version: %v, want %s", err, store.CodeVersion)
	}
	if _, err := s.AssignWave(ctx, wave.ID, 2, "anna", idem("a3")); problemCode(err) != store.CodeValidation {
		t.Fatalf("same picker: %v, want %s", err, store.CodeValidation)
	}
	if _, err := s.AssignWave(ctxFor(2, ""), wave.ID, 2, "ben", idem("a4")); problemCode(err) != store.CodeWaveNotFound {
		t.Fatalf("other tenant: %v, want %s", err, store.CodeWaveNotFound)
	}
	if _, err := s.AssignWave(ctxFor(1, ids.New()), wave.ID, 2, "ben", idem("a5")); problemCode(err) != store.CodeWaveNotFound {
		t.Fatalf("other legal entity: %v, want %s", err, store.CodeWaveNotFound)
	}
	got, err = s.AssignWave(ctx, wave.ID, 2, "ben", idem("a6"))
	if err != nil || *got.Picker != "ben" || got.Version != 3 {
		t.Fatalf("reassign: %+v, %v", got.WaveSummary, err)
	}
	if n := countEvents(t, admin, fulevents.WaveAssigned); n != 2 {
		t.Fatalf("wave.assigned events = %d, want 2 (replay and refusals add none)", n)
	}
	var previous string
	if err := admin.QueryRow(ctx, `select payload->'data'->>'previous_picker' from outbox where type = $1 and payload->'data'->>'picker' = 'ben'`, fulevents.WaveAssigned).Scan(&previous); err != nil || previous != "anna" {
		t.Fatalf("previous_picker = %q, %v", previous, err)
	}

	// Filters.
	if rows, err := s.ListWaves(ctx, store.WaveFilter{Picker: "ben", Status: "OPEN", Limit: 10}); err != nil || len(rows) != 1 {
		t.Fatalf("list by picker: %d, %v", len(rows), err)
	}
	if rows, err := s.ListWaves(ctx, store.WaveFilter{Picker: "anna", Limit: 10}); err != nil || len(rows) != 0 {
		t.Fatalf("list by old picker: %d, %v", len(rows), err)
	}
	if rows, err := s.ListWaves(ctx, store.WaveFilter{Unassigned: true, Limit: 10}); err != nil || len(rows) != 0 {
		t.Fatalf("list unassigned: %d, %v", len(rows), err)
	}

	// Close is refused while lines are left to pick; a cancelled shipment does not count.
	if _, err := s.CloseWave(ctx, wave.ID, 3, idem("c1")); problemCode(err) != store.CodeWaveOpenLines {
		t.Fatalf("close with open lines: %v, want %s", err, store.CodeWaveOpenLines)
	}
	cancelled := getShipment(t, s, ctx, shipments[0].ID)
	if _, err := s.CancelShipment(ctx, cancelled.ID, cancelled.Version, idem("cancel")); err != nil {
		t.Fatalf("CancelShipment: %v", err)
	}
	partly := getShipment(t, s, ctx, shipments[1].ID)
	if _, err := s.ConfirmPick(ctx, partly.ID, partly.Version, []store.PickInput{{ShipmentLineID: partly.Lines[0].ID, Picked: d("0"), Reason: "NOT_FOUND"}}, idem("short")); err != nil {
		t.Fatalf("short pick: %v", err)
	}
	if _, err := s.CloseWave(ctx, wave.ID, 3, idem("c2")); problemCode(err) != store.CodeWaveOpenLines {
		t.Fatalf("close with a line left: %v, want %s", err, store.CodeWaveOpenLines)
	}
	if _, err := s.ConfirmWavePick(ctx, wave.ID, nil, true, idem("rest")); err != nil {
		t.Fatalf("ConfirmWavePick: %v", err)
	}
	if _, err := s.CloseWave(ctx, wave.ID, 2, idem("c3")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale close: %v, want %s", err, store.CodeVersion)
	}
	closeKey := idem("c4")
	closed, err := s.CloseWave(ctx, wave.ID, 3, closeKey)
	if err != nil || closed.Status != "CLOSED" || closed.Version != 4 || closed.ClosedAt == nil {
		t.Fatalf("CloseWave: %+v, %v", closed.WaveSummary, err)
	}
	if replay, err := s.CloseWave(ctx, wave.ID, 3, closeKey); err != nil || replay.Status != "CLOSED" {
		t.Fatalf("replay: %+v, %v", replay.WaveSummary, err)
	}
	if n := countEvents(t, admin, fulevents.WaveClosed); n != 1 {
		t.Fatalf("wave.closed events = %d, want 1", n)
	}
	var short, lines int
	if err := admin.QueryRow(ctx, `select (payload->'data'->>'short_lines')::int, (payload->'data'->>'line_count')::int from outbox where type = $1`, fulevents.WaveClosed).Scan(&short, &lines); err != nil || short != 1 || lines != 4 {
		t.Fatalf("wave.closed short_lines = %d line_count = %d, %v", short, lines, err)
	}
	// A closed wave is final.
	if _, err := s.AssignWave(ctx, wave.ID, 4, "carl", idem("a7")); problemCode(err) != store.CodeStatus {
		t.Fatalf("assign closed: %v, want %s", err, store.CodeStatus)
	}
	if _, err := s.CloseWave(ctx, wave.ID, 4, idem("c5")); problemCode(err) != store.CodeStatus {
		t.Fatalf("close closed: %v, want %s", err, store.CodeStatus)
	}
	// The refusal comes from the wave, not from the shipment's own status.
	var closedPick *httpx.Problem
	if _, err := s.ConfirmWavePick(ctx, wave.ID, []store.PickInput{{ShipmentLineID: partly.Lines[0].ID, Picked: d("1")}}, false, idem("repick")); !errors.As(err, &closedPick) ||
		closedPick.Code != store.CodeStatus || !strings.Contains(closedPick.Detail, "only an OPEN wave takes picks") {
		t.Fatalf("pick on a closed wave: %v, want %s from the wave", err, store.CodeStatus)
	}
	if rows, err := s.ListWaves(ctx, store.WaveFilter{Status: "CLOSED", Limit: 10}); err != nil || len(rows) != 1 {
		t.Fatalf("list closed: %d, %v", len(rows), err)
	}
	if rows, err := s.ListWaves(ctx, store.WaveFilter{Status: "OPEN", Limit: 10}); err != nil || len(rows) != 0 {
		t.Fatalf("list open after close: %d, %v", len(rows), err)
	}
}
