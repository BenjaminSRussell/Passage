package store

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

func pngBytes(t *testing.T, w, h int, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.SetGray(x, 0, color.Gray{Y: shade})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// dispatchedWith returns a DISPATCHED work order with a checklist, assigned to a technician with
// a truck, for the Monday 2026-10-26 the fixture plans.
func (d dispatchFixture) dispatchedWith(t *testing.T, code string, lines []domain.WorkLine, checklist []domain.ChecklistItem) (WorkOrder, Technician) {
	t.Helper()
	worker, site, warehouse, location := ids.New(), ids.New(), ids.New(), ids.New()
	tech, _, err := d.s.CreateTechnician(d.ctx, Technician{Code: code, Name: "Tech " + code, WorkerID: &worker, LegalEntityID: d.le, TerritoryIDs: []string{d.territory},
		TimeZone: "America/Chicago", ShiftDays: []int{1, 2, 3, 4, 5}, ShiftStartMinute: 480, ShiftEndMinute: 1020, MaxDailyMinutes: 480, Active: true,
		TruckSiteID: &site, TruckWarehouseID: &warehouse, TruckLocationID: &location}, "key-"+code, "hash-"+code)
	if err != nil {
		t.Fatal(err)
	}
	wo, _, err := d.s.CreateWorkOrder(d.ctx, WorkOrder{CustomerPartyID: d.customer, LegalEntityID: d.le, TerritoryID: d.territory, EstDurationMinutes: 60,
		Currency: "USD", Lines: lines, Address: "12 Mill Road", Checklist: checklist}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(d.ctx, `update service_work_order set created_at = '2026-10-20T00:00:00Z' where tenant_id = $1 and id = $2`, tenantOf(d.ctx), wo.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	wo = d.assign(t, wo, tech, start)
	if _, _, err := d.s.PublishDispatch(d.ctx, PublishInput{LegalEntityID: d.le, TerritoryID: d.territory, Date: "2026-10-26", TimeZone: "America/Chicago"}, start, "", ""); err != nil {
		t.Fatal(err)
	}
	wo, err = d.s.GetWorkOrder(d.ctx, wo.ID)
	if err != nil || wo.Status != "DISPATCHED" {
		t.Fatalf("dispatched order %+v %v", wo, err)
	}
	return wo, tech
}

// FR-45.8: the technician's day, photo and signature captures, the checklist gate and the
// completed event with the truck, against Postgres as passage_app.
func TestStore_TechnicianDayCapturesAndChecklist(t *testing.T) {
	d := newDispatch(t, 31)
	part := domain.WorkLine{ID: ids.New(), Kind: domain.KindPart, Qty: decimal.NewFromInt(3), UnitPrice: decimal.NewFromInt(20), Description: "Impeller",
		ProductID: ids.New(), Unit: "EA"}
	must, optional := ids.New(), ids.New()
	wo, tech := d.dispatchedWith(t, "DAY", []domain.WorkLine{part}, []domain.ChecklistItem{{ID: must, Text: "Isolate power", Required: true}, {ID: optional, Text: "Photograph the nameplate"}})
	dev := d.device(tech)
	other := d.worker(t, "OTH")

	day, err := d.s.TechnicianDay(dev, "me", "2026-10-26", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if day.Technician.ID != tech.ID || day.Technician.Truck == nil || day.Technician.Truck.WarehouseID == "" || len(day.Visits) != 1 || day.Totals.Open != 1 {
		t.Fatalf("day %+v", day)
	}
	v := day.Visits[0]
	if v.ID != wo.ID || v.Sequence != 1 || v.Status != "DISPATCHED" || v.Address != "12 Mill Road" || len(v.Lines) != 1 || v.Lines[0].Description != "Impeller" ||
		len(v.Checklist) != 2 || v.Checklist[0].Done || v.Signature.Signed {
		t.Fatalf("visit %+v", v)
	}
	// The day belongs to the technician: another technician's device, or the next day, sees nothing of it.
	if _, err := d.s.TechnicianDay(d.device(other), tech.ID, "2026-10-26", time.Now()); problemStatus(err) != http.StatusForbidden {
		t.Fatalf("another technician read the day: %v", err)
	}
	if next, err := d.s.TechnicianDay(dev, "me", "2026-10-27", time.Now()); err != nil || len(next.Visits) != 0 {
		t.Fatalf("next day %+v %v", next, err)
	}
	if _, err := d.s.TechnicianDay(d.as(ids.New(), "fieldservice.work-order.execute"), "me", "2026-10-26", time.Now()); problemCode(err) != CodeNoTechnician {
		t.Fatalf("a user with no technician: %v", err)
	}
	if office, err := d.s.TechnicianDay(d.as(ids.New(), "fieldservice.technician.dispatch"), tech.ID, "2026-10-26", time.Now()); err != nil || len(office.Visits) != 1 {
		t.Fatalf("dispatcher view %+v %v", office, err)
	}

	// A photo uploads once however often the device retries it; the id cannot carry other bytes.
	photoID, sigID := ids.New(), ids.New()
	photo := CaptureInput{ID: photoID, WorkOrderID: wo.ID, TechnicianID: tech.ID, Kind: domain.CapturePhoto, ContentType: "image/png", Width: 64, Height: 48,
		Data: pngBytes(t, 64, 48, 10), CapturedAt: at(10)}
	if c, err := d.s.PutCapture(dev, photo); err != nil || c.Duplicate || c.Ref != "capture:"+photoID {
		t.Fatalf("photo %+v %v", c, err)
	}
	if c, err := d.s.PutCapture(dev, photo); err != nil || !c.Duplicate {
		t.Fatalf("retried photo %+v %v", c, err)
	}
	reused := photo
	reused.Data = pngBytes(t, 64, 48, 99)
	if _, err := d.s.PutCapture(dev, reused); problemCode(err) != domain.CodeActionReuse {
		t.Fatalf("an id with other bytes: %v", err)
	}
	if _, err := d.s.PutCapture(d.device(other), CaptureInput{ID: ids.New(), WorkOrderID: wo.ID, TechnicianID: other.ID, Kind: domain.CapturePhoto, ContentType: "image/png",
		Width: 8, Height: 8, Data: pngBytes(t, 8, 8, 1), CapturedAt: at(10)}); problemStatus(err) != http.StatusForbidden {
		t.Fatalf("a capture for someone else's work order: %v", err)
	}
	if _, err := d.s.PutCapture(dev, CaptureInput{ID: sigID, WorkOrderID: wo.ID, TechnicianID: tech.ID, Kind: domain.CaptureSignature, ContentType: "image/png", Width: 300, Height: 100,
		Data: pngBytes(t, 300, 100, 200), CapturedAt: at(60), Latitude: fp(41.88), Longitude: fp(-87.63)}); err != nil {
		t.Fatal(err)
	}
	if c, data, err := d.s.GetCapture(dev, wo.ID, photoID); err != nil || c.WidthPx != 64 || !bytes.Equal(data, photo.Data) {
		t.Fatalf("read back %+v %v", c, err)
	}

	missing := ids.New()
	batch := []ActionInput{
		act(domain.ActTravelStart, 1), act(domain.ActWorkStart, 5),
		{ID: ids.New(), Kind: domain.ActPhoto, DeviceTime: at(12), CaptureIDs: []string{photoID}},
		{ID: ids.New(), Kind: domain.ActPhoto, DeviceTime: at(13), CaptureIDs: []string{missing}},
		{ID: ids.New(), Kind: domain.ActChecklist, DeviceTime: at(20), ItemID: ids.New(), Done: true},
		{ID: ids.New(), Kind: domain.ActChecklist, DeviceTime: at(30), ItemID: optional, Done: true},
		{ID: ids.New(), Kind: domain.ActPartUsed, DeviceTime: at(40), LineID: part.ID, Qty: decimal.NewFromInt(3)},
		{ID: ids.New(), Kind: domain.ActSignature, DeviceTime: at(61), SignedBy: "Pat Customer", SignatureRef: "capture:" + sigID, Latitude: fp(41.88), Longitude: fp(-87.63)},
		{ID: ids.New(), Kind: domain.ActComplete, DeviceTime: at(70)},
	}
	res, err := d.s.RecordActions(dev, wo.ID, tech.ID, batch, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"APPLIED", "APPLIED", "APPLIED", "REJECTED", "REJECTED", "APPLIED", "APPLIED", "APPLIED", "REJECTED"}
	for i, r := range res.Results {
		if r.Outcome != want[i] {
			t.Fatalf("action %d (%s) = %s %s %s, want %s; all %v", i, r.Kind, r.Outcome, r.Code, r.Reason, want[i], outcomes(res))
		}
	}
	if c := res.Results[3].Code; c != domain.CodeCaptureUnknown {
		t.Fatalf("unknown photo code %s", c)
	}
	if c := res.Results[4].Code; c != domain.CodeChecklistItem {
		t.Fatalf("unknown checklist item code %s", c)
	}
	if c := res.Results[8].Code; c != domain.CodeChecklistOpen || res.WorkOrder.Status != "IN_PROGRESS" {
		t.Fatalf("completion with a required item open: %s, status %s", c, res.WorkOrder.Status)
	}
	if res.WorkOrder.SignedLatitude == nil || *res.WorkOrder.SignedLatitude != 41.88 {
		t.Fatalf("signature position %+v", res.WorkOrder.SignedLatitude)
	}

	// The later tick wins; a tick older than the stored one is superseded.
	later := []ActionInput{
		{ID: ids.New(), Kind: domain.ActChecklist, DeviceTime: at(80), ItemID: must, Done: true},
		{ID: ids.New(), Kind: domain.ActChecklist, DeviceTime: at(75), ItemID: must, Done: false},
		act(domain.ActComplete, 90),
	}
	res, err = d.s.RecordActions(dev, wo.ID, tech.ID, later, time.Now())
	if err != nil || res.WorkOrder.Status != "COMPLETED" {
		t.Fatalf("complete %v %v", outcomes(res), err)
	}
	if got := outcomes(res); got[0] != "APPLIED" || got[1] != "APPLIED" || got[2] != "APPLIED" {
		// at(75) is applied first (device order), at(80) then supersedes it by being later
		t.Fatalf("ticks %v", got)
	}

	day, err = d.s.TechnicianDay(dev, "me", "2026-10-26", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v = day.Visits[0]
	if v.Status != "COMPLETED" || day.Totals.Completed != 1 || !v.Checklist[0].Done || !v.Checklist[1].Done || len(v.Photos) != 1 || v.Photos[0].CaptureID != photoID ||
		!v.Signature.Signed || v.Lines[0].ConsumedQty.String() != "3" {
		t.Fatalf("completed visit %+v", v)
	}

	// The completed event addresses the truck and names the consumed part's product (BR-45.7).
	var site, product, unit, consumed string
	if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(d.ctx, `select payload->'data'->'truck'->>'siteId', payload->'data'->'lines'->0->>'productId', payload->'data'->'lines'->0->>'unit',
			payload->'data'->'lines'->0->>'consumedQty' from outbox where type = 'dev.passage.service.workorder.completed.v1'`).Scan(&site, &product, &unit, &consumed)
	}); err != nil {
		t.Fatal(err)
	}
	if site != *tech.TruckSiteID || product != part.ProductID || unit != "EA" || consumed != "3" {
		t.Fatalf("completed event truck %s product %s unit %s consumed %s", site, product, unit, consumed)
	}
}

// A checklist can be replaced until completion and keeps its ticks by item id.
func TestStore_ChecklistReplacement(t *testing.T) {
	d := newDispatch(t, 32)
	item := ids.New()
	wo, tech := d.dispatchedWith(t, "CHK", nil, []domain.ChecklistItem{{ID: item, Text: "Lock out", Required: true}})
	dev := d.device(tech)
	if _, err := d.s.RecordActions(dev, wo.ID, tech.ID, []ActionInput{{ID: ids.New(), Kind: domain.ActChecklist, DeviceTime: at(5), ItemID: item, Done: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A tick made earlier on the device clock than the stored one does not undo it.
	res, err := d.s.RecordActions(dev, wo.ID, tech.ID, []ActionInput{{ID: ids.New(), Kind: domain.ActChecklist, DeviceTime: at(2), ItemID: item, Done: false}}, time.Now())
	if err != nil || res.Superseded != 1 {
		t.Fatalf("older tick %v %v", outcomes(res), err)
	}
	wo, _ = d.s.GetWorkOrder(d.ctx, wo.ID)
	wo, err = d.s.SetChecklist(d.ctx, wo.ID, wo.Version, []domain.ChecklistItem{{ID: item, Text: "Lock out and tag", Required: true}, {ID: ids.New(), Text: "Test run"}})
	if err != nil || len(wo.Checklist) != 2 {
		t.Fatalf("replace %+v %v", wo, err)
	}
	if _, err := d.s.SetChecklist(d.ctx, wo.ID, wo.Version-1, nil); problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale version: %v", err)
	}
	day, err := d.s.TechnicianDay(dev, "me", "2026-10-26", time.Now())
	if err != nil || len(day.Visits[0].Checklist) != 2 || !day.Visits[0].Checklist[0].Done {
		t.Fatalf("tick kept? %+v %v", day, err)
	}
}

func fp(v float64) *float64 { return &v }

// A manual :transition to COMPLETED meets the same required checklist as the device's COMPLETE (#45): it is refused
// while a required item is open and goes through once the technician has ticked it or the checklist is replaced.
func TestStore_ManualCompletionChecksTheRequiredChecklist(t *testing.T) {
	d := newDispatch(t, 33)
	must, optional := ids.New(), ids.New()
	wo, tech := d.dispatchedWith(t, "MAN", nil, []domain.ChecklistItem{{ID: must, Text: "Lock out and tag", Required: true}, {ID: optional, Text: "Photograph the nameplate"}})
	dev := d.device(tech)
	if _, err := d.s.RecordActions(dev, wo.ID, tech.ID, []ActionInput{act(domain.ActTravelStart, 1), act(domain.ActWorkStart, 5)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(d.ctx, `update service_work_order set signature_exempt = true where tenant_id = $1 and id = $2`, tenantOf(d.ctx), wo.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wo, err := d.s.GetWorkOrder(d.ctx, wo.ID)
	if err != nil || wo.Status != "IN_PROGRESS" {
		t.Fatalf("setup %+v %v", wo, err)
	}
	if _, err := d.s.TransitionWorkOrder(d.ctx, wo.ID, wo.Version, domain.WOCompleted); problemCode(err) != domain.CodeChecklistOpen {
		t.Fatalf("manual completion with a required item open: %v", err)
	}
	if got, _ := d.s.GetWorkOrder(d.ctx, wo.ID); got.Status != "IN_PROGRESS" || got.Version != wo.Version {
		t.Fatalf("the refused completion changed the order: %+v", got)
	}
	// The optional item does not count; the technician's tick of the required one does.
	if _, err := d.s.RecordActions(dev, wo.ID, tech.ID, []ActionInput{{ID: ids.New(), Kind: domain.ActChecklist, DeviceTime: at(8), ItemID: must, Done: true}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	done, err := d.s.TransitionWorkOrder(d.ctx, wo.ID, wo.Version, domain.WOCompleted)
	if err != nil || done.Status != "COMPLETED" || done.CompletedAt == nil {
		t.Fatalf("manual completion after the tick: %+v %v", done, err)
	}
}
