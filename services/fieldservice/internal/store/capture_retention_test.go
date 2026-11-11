package store

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/objectstore"
)

// Captures go to the object store when the service has one, an earlier Postgres capture moves over, reads come from the
// store, and the retention job deletes the bytes of finished work orders but keeps the row (#45).
func TestStore_CapturesInTheObjectStoreAndTheRetentionJob(t *testing.T) {
	d := newDispatch(t, 34)
	wo, tech := d.dispatchedWith(t, "OBJ", nil, nil)
	dev := d.device(tech)
	pgPhoto, objPhoto := ids.New(), ids.New()
	put := func(id string, shade uint8) []byte {
		data := pngBytes(t, 32, 24, shade)
		if _, err := d.s.PutCapture(dev, CaptureInput{ID: id, WorkOrderID: wo.ID, TechnicianID: tech.ID, Kind: domain.CapturePhoto, ContentType: "image/png", Width: 32, Height: 24,
			Data: data, CapturedAt: at(10)}); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
		return data
	}
	// Without an object store the bytes stay in Postgres.
	pgData := put(pgPhoto, 10)
	var inPG int
	count := func(where string) int {
		var n int
		if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(d.ctx, `select count(*) from service_wo_capture where tenant_id = $1 and `+where, tenantOf(d.ctx)).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if inPG = count("data is not null and object_key is null"); inPG != 1 {
		t.Fatalf("captures in Postgres: %d", inPG)
	}

	// Turn the object store on: a new capture goes there, with no bytes in the row.
	store := objectstore.NewMemory()
	d.s.WithObjects(store)
	objData := put(objPhoto, 20)
	key := objectstore.Key(tenantOf(d.ctx), wo.ID, objPhoto)
	if !bytes.Equal(store.Objects[key], objData) || count("data is null and object_key is not null") != 1 {
		t.Fatalf("object store holds %d objects; the new capture must be there and not in Postgres", len(store.Objects))
	}
	if _, got, err := d.s.GetCapture(dev, wo.ID, objPhoto); err != nil || !bytes.Equal(got, objData) {
		t.Fatalf("read from the object store: %v", err)
	}
	if _, got, err := d.s.GetCapture(dev, wo.ID, pgPhoto); err != nil || !bytes.Equal(got, pgData) {
		t.Fatalf("an earlier capture stays readable: %v", err)
	}
	// An upload retried after the commit is a duplicate and writes nothing again.
	puts := store.Puts
	if c, err := d.s.PutCapture(dev, CaptureInput{ID: objPhoto, WorkOrderID: wo.ID, TechnicianID: tech.ID, Kind: domain.CapturePhoto, ContentType: "image/png", Width: 32, Height: 24,
		Data: objData, CapturedAt: at(10)}); err != nil || !c.Duplicate || store.Puts != puts {
		t.Fatalf("retried upload %+v %v puts %d", c, err, store.Puts-puts)
	}
	// A store that is down refuses the upload with 503 and stores no row.
	store.FailPut = http.ErrHandlerTimeout
	if _, err := d.s.PutCapture(dev, CaptureInput{ID: ids.New(), WorkOrderID: wo.ID, TechnicianID: tech.ID, Kind: domain.CapturePhoto, ContentType: "image/png", Width: 32, Height: 24,
		Data: pngBytes(t, 32, 24, 30), CapturedAt: at(10)}); problemCode(err) != domain.CodeCaptureStoreDown || problemStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("store down: %v", err)
	}
	store.FailPut = nil
	if count("true") != 2 {
		t.Fatalf("captures: %d, want 2", count("true"))
	}

	// The offload moves the Postgres capture over.
	if n, err := d.s.OffloadCaptures(d.ctx, 10); err != nil || n != 1 || count("data is not null") != 0 || len(store.Objects) != 2 {
		t.Fatalf("offload %d %v, objects %d", n, err, len(store.Objects))
	}
	if _, got, err := d.s.GetCapture(dev, wo.ID, pgPhoto); err != nil || !bytes.Equal(got, pgData) {
		t.Fatalf("offloaded capture: %v", err)
	}
	if n, err := d.s.OffloadCaptures(d.ctx, 10); err != nil || n != 0 {
		t.Fatalf("a second offload moved %d: %v", n, err)
	}

	// The order is still being worked on: nothing is purged however old the retention.
	now := time.Now().UTC().Add(100 * 24 * time.Hour)
	if res, err := d.s.PurgeCaptures(d.ctx, now, 30*24*time.Hour, 10); err != nil || res.Purged != 0 {
		t.Fatalf("purge of an open work order: %+v %v", res, err)
	}
	if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(d.ctx, `update service_work_order set status = 'COMPLETED', completed_at = $3 where tenant_id = $1 and id = $2`, tenantOf(d.ctx), wo.ID, time.Now().UTC())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Completed today: inside the retention window of 30 days, even looking from the future by less than that.
	if res, err := d.s.PurgeCaptures(d.ctx, time.Now().UTC().Add(10*24*time.Hour), 30*24*time.Hour, 10); err != nil || res.Purged != 0 {
		t.Fatalf("purge inside the window: %+v %v", res, err)
	}
	if res, err := d.s.PurgeCaptures(d.ctx, now, 0, 10); err != nil || res != (CaptureRetentionResult{}) {
		t.Fatalf("retention 0 disables the purge: %+v %v", res, err)
	}
	res, err := d.s.PurgeCaptures(d.ctx, now, 30*24*time.Hour, 10)
	if err != nil || res.Purged != 2 || res.Failed != 0 || len(store.Objects) != 0 {
		t.Fatalf("purge %+v %v, objects left %d", res, err, len(store.Objects))
	}
	if _, _, err := d.s.GetCapture(dev, wo.ID, objPhoto); problemCode(err) != domain.CodeCaptureGone || problemStatus(err) != http.StatusGone {
		t.Fatalf("read of a purged capture: %v", err)
	}
	if count("purged_at is not null and data is null and object_key is null") != 2 {
		t.Fatal("the rows must stay, without bytes")
	}
	if res, err := d.s.PurgeCaptures(d.ctx, now, 30*24*time.Hour, 10); err != nil || res.Purged != 0 {
		t.Fatalf("a second purge: %+v %v", res, err)
	}
	if les, err := d.s.CaptureLegalEntities(d.ctx); err != nil || len(les) != 0 {
		t.Fatalf("entities with bytes left: %v %v", les, err)
	}
}
