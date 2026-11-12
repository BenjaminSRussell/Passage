package store

import (
	"bytes"
	"context"
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

// hookedStore runs a function inside Put or Delete, to stand for another replica acting at that moment.
type hookedStore struct {
	*objectstore.Memory
	afterPut     func(key string)
	beforeDelete func(key string)
}

func (h *hookedStore) Put(ctx context.Context, key, ct string, data []byte) error {
	if err := h.Memory.Put(ctx, key, ct, data); err != nil {
		return err
	}
	if h.afterPut != nil {
		h.afterPut(key)
	}
	return nil
}

func (h *hookedStore) Delete(ctx context.Context, key string) error {
	if h.beforeDelete != nil {
		h.beforeDelete(key)
	}
	return h.Memory.Delete(ctx, key)
}

// Two replicas running the retention job at once (#45): a purge that lands while an offload is uploading wins, and the
// offload deletes the object it wrote; an offload that moved the bytes while a purge was deleting leaves the capture to the
// next pass instead of purging a row whose key changed.
func TestStore_OffloadAndPurgeRaceBetweenReplicas(t *testing.T) {
	d := newDispatch(t, 36)
	wo, tech := d.dispatchedWith(t, "RAC", nil, nil)
	dev := d.device(tech)
	id := ids.New()
	if _, err := d.s.PutCapture(dev, CaptureInput{ID: id, WorkOrderID: wo.ID, TechnicianID: tech.ID, Kind: domain.CapturePhoto, ContentType: "image/png", Width: 32, Height: 24,
		Data: pngBytes(t, 32, 24, 7), CapturedAt: at(10)}); err != nil {
		t.Fatal(err)
	}
	complete := func() {
		if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(d.ctx, `update service_work_order set status = 'COMPLETED', completed_at = now() - interval '400 days' where tenant_id = $1 and id = $2`, tenantOf(d.ctx), wo.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	complete()
	retention := 30 * 24 * time.Hour

	// 1. The purge marks the row purged while the offload's upload is in flight.
	store := &hookedStore{Memory: objectstore.NewMemory()}
	d.s.WithObjects(store)
	store.afterPut = func(string) {
		if _, err := d.s.PurgeCaptures(d.ctx, time.Now().UTC(), retention, 10); err != nil {
			t.Error(err)
		}
	}
	if n, err := d.s.OffloadCaptures(d.ctx, 10); err != nil || n != 0 {
		t.Fatalf("offload %d %v", n, err)
	}
	if len(store.Objects) != 0 {
		t.Fatalf("the offload left the object it wrote for a purged capture: %d objects", len(store.Objects))
	}
	if _, _, err := d.s.GetCapture(dev, wo.ID, id); problemCode(err) != domain.CodeCaptureGone {
		t.Fatalf("read: %v", err)
	}

	// 2. An offload moves the bytes while the purge is deleting: the purge must not mark the row purged.
	id2 := ids.New()
	d.s.WithObjects(nil)
	if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(d.ctx, `update service_work_order set status = 'IN_PROGRESS', completed_at = null where tenant_id = $1 and id = $2`, tenantOf(d.ctx), wo.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.s.PutCapture(dev, CaptureInput{ID: id2, WorkOrderID: wo.ID, TechnicianID: tech.ID, Kind: domain.CapturePhoto, ContentType: "image/png", Width: 32, Height: 24,
		Data: pngBytes(t, 32, 24, 9), CapturedAt: at(11)}); err != nil {
		t.Fatal(err)
	}
	complete()
	store2 := &hookedStore{Memory: objectstore.NewMemory()}
	d.s.WithObjects(store2)
	if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(d.ctx, `update service_wo_capture set object_key = $3, data = null where tenant_id = $1 and id = $2`, tenantOf(d.ctx), id2, objectstore.Key(tenantOf(d.ctx), wo.ID, id2))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	store2.Objects[objectstore.Key(tenantOf(d.ctx), wo.ID, id2)] = []byte("x")
	store2.beforeDelete = func(string) {
		// another replica re-keys the row between this purge's select and update
		if err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(d.ctx, `update service_wo_capture set object_key = 'moved/key' where tenant_id = $1 and id = $2`, tenantOf(d.ctx), id2)
			return err
		}); err != nil {
			t.Error(err)
		}
	}
	res, err := d.s.PurgeCaptures(d.ctx, time.Now().UTC(), retention, 10)
	if err != nil || res.Purged != 0 {
		t.Fatalf("purge of a row whose key changed: %+v %v", res, err)
	}
	if n := func() int {
		var c int
		_ = dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(d.ctx, `select count(*) from service_wo_capture where tenant_id = $1 and id = $2 and purged_at is null and object_key = 'moved/key'`, tenantOf(d.ctx), id2).Scan(&c)
		})
		return c
	}(); n != 1 {
		t.Fatal("the re-keyed capture must stay unpurged")
	}
}
