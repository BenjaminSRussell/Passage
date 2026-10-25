package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

func principalContext(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n)})
}

func newStore(t *testing.T) *store.Postgres {
	pool := testkit.DB(t)
	ctx := context.Background()
	drop := func() { _, _ = pool.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool)
}

func sampleRun() store.Run {
	return store.Run{
		SourceSystem: "ax2012", Mode: "rehearsal", Entity: "customer", MappingVersion: 1,
		Mapping: json.RawMessage(`{"entity":"customer","version":1,"fields":[]}`),
		Report: domain.Report{
			Status: domain.StatusPartial, Created: 2, Extracted: 3,
			Rejects: []domain.RowReject{{SourceKey: "C3", Rejects: []domain.Reject{{Field: "name", Code: domain.CodeMapRequired}}}},
			Ledger: []domain.LedgerEntry{
				{SourceKey: "C1", TargetID: "00000000-0000-7000-8000-000000000001", IdempotencyKey: "i1", ContentHash: "h1", Action: domain.ActionCreated},
				{SourceKey: "C2", TargetID: "00000000-0000-7000-8000-000000000002", IdempotencyKey: "i2", ContentHash: "h2", Action: domain.ActionCreated},
			},
		},
	}
}

func TestPostgres_RunRoundTripIdempotencyAndTenantIsolation(t *testing.T) {
	s := newStore(t)
	ctx := principalContext(1)

	saved, err := s.SaveRun(ctx, sampleRun(), "key-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(ctx, saved.ID)
	if err != nil || got.Report.Created != 2 || len(got.Report.Rejects) != 1 || got.Report.Rejects[0].Rejects[0].Code != domain.CodeMapRequired || got.Entity != "customer" {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	replay, err := s.SaveRun(ctx, sampleRun(), "key-1")
	if err != nil || replay.ID != saved.ID {
		t.Fatalf("same key must return the original run, got %+v, %v", replay, err)
	}
	if found, ok, err := s.RunByKey(ctx, "key-1"); err != nil || !ok || found.ID != saved.ID {
		t.Fatalf("RunByKey = %+v, %v, %v", found, ok, err)
	}
	if _, ok, err := s.RunByKey(ctx, "other"); err != nil || ok {
		t.Fatalf("unknown key found = %v, %v", ok, err)
	}
	child := sampleRun()
	child.ParentRunID = saved.ID
	if rerun, err := s.SaveRun(ctx, child, ""); err != nil || rerun.ParentRunID != saved.ID {
		t.Fatalf("child run = %+v, %v", rerun, err)
	}

	var problem *httpx.Problem
	if _, err := s.GetRun(principalContext(2), saved.ID); !errors.As(err, &problem) || problem.Code != store.CodeRunNotFound {
		t.Fatalf("other tenant must get %s, got %v", store.CodeRunNotFound, err)
	}
	if _, ok, _ := s.RunByKey(principalContext(2), "key-1"); ok {
		t.Fatal("other tenant must not see the idempotency key")
	}
	if _, err := s.GetRun(ctx, "not-a-uuid"); !errors.As(err, &problem) || problem.Code != store.CodeRunNotFound {
		t.Fatalf("malformed id must be %s, got %v", store.CodeRunNotFound, err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgres_XrefPersistsTargetIDAndIsolatesTenants(t *testing.T) {
	x := newStore(t).Xref()
	ctx := principalContext(1)
	key := domain.XrefKey{SourceSystem: "ax2012", Entity: "customer", SourceKey: "C1"}
	target := "00000000-0000-7000-8000-000000000001"

	if _, found, err := x.Get(ctx, key); err != nil || found {
		t.Fatalf("empty xref found = %v, %v", found, err)
	}
	if err := x.Put(ctx, key, domain.XrefEntry{TargetID: target}); err != nil {
		t.Fatal(err)
	}
	if err := x.Put(ctx, key, domain.XrefEntry{TargetID: "00000000-0000-7000-8000-0000000000ff", ContentHash: "h"}); err != nil {
		t.Fatal(err)
	}
	got, found, err := x.Get(ctx, key)
	if err != nil || !found || got.TargetID != target || got.ContentHash != "h" {
		t.Fatalf("the first target id must survive an update, got %+v, %v, %v", got, found, err)
	}
	keys, err := x.Keys(ctx, "ax2012", "customer")
	if err != nil || len(keys) != 1 || keys[0] != "C1" {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	if _, found, _ := x.Get(principalContext(2), key); found {
		t.Fatal("tenant 2 must not see tenant 1 xref")
	}
	if keys, _ := x.Keys(principalContext(2), "ax2012", "customer"); len(keys) != 0 {
		t.Fatalf("tenant 2 keys = %v", keys)
	}
}

func TestPostgres_DryRunLedgerWithoutTargetIDsIsSaved(t *testing.T) {
	s := newStore(t)
	ctx := principalContext(1)
	run := sampleRun()
	run.DryRun = true
	run.Report.DryRun = true
	run.Report.Ledger = []domain.LedgerEntry{
		{SourceKey: "C1", IdempotencyKey: "i1", ContentHash: "h1", Action: domain.ActionCreated},
		{SourceKey: "C2", TargetID: "00000000-0000-7000-8000-000000000002", IdempotencyKey: "i2", ContentHash: "h2", Action: domain.ActionUpdated},
	}
	saved, err := s.SaveRun(ctx, run, "dry-1")
	if err != nil || !saved.DryRun {
		t.Fatalf("dry run save = %+v, %v", saved, err)
	}
}

func TestPostgres_XrefBatchRoundTripKeepsFirstTargetID(t *testing.T) {
	x := newStore(t).Xref()
	ctx := principalContext(1)
	first := "00000000-0000-7000-8000-000000000001"
	if err := x.PutMany(ctx, "ax2012", "customer", []domain.XrefItem{
		{SourceKey: "C1", Entry: domain.XrefEntry{TargetID: first}},
		{SourceKey: "C2", Entry: domain.XrefEntry{TargetID: "00000000-0000-7000-8000-000000000002"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.PutMany(ctx, "ax2012", "customer", []domain.XrefItem{{SourceKey: "C1", Entry: domain.XrefEntry{TargetID: "00000000-0000-7000-8000-0000000000ff", ContentHash: "h"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := x.GetMany(ctx, "ax2012", "customer", []string{"C1", "C2", "C9"})
	if err != nil || len(got) != 2 || got["C1"].TargetID != first || got["C1"].ContentHash != "h" {
		t.Fatalf("GetMany = %+v, %v", got, err)
	}
	if other, err := x.GetMany(principalContext(2), "ax2012", "customer", []string{"C1"}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 GetMany = %+v, %v", other, err)
	}
	if err := x.PutMany(ctx, "ax2012", "customer", nil); err != nil {
		t.Fatal(err)
	}
}

func TestPostgres_ConcurrentReservationsAgreeOnTargetIDsAndKeepHashes(t *testing.T) {
	x := newStore(t).Xref()
	ctx := principalContext(1)
	loaded := domain.XrefItem{SourceKey: "C0", Entry: domain.XrefEntry{TargetID: "00000000-0000-7000-8000-0000000000a0", ContentHash: "loaded"}}
	if err := x.PutMany(ctx, "ax2012", "customer", []domain.XrefItem{loaded}); err != nil {
		t.Fatal(err)
	}
	const racers = 8
	results := make([]map[string]string, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mine := fmt.Sprintf("00000000-0000-7000-8000-%012x", i+1)
			results[i], errs[i] = x.Reserve(ctx, "ax2012", "customer", []domain.XrefItem{
				{SourceKey: "C0", Entry: domain.XrefEntry{TargetID: "00000000-0000-7000-8000-0000000000ff"}},
				{SourceKey: "C1", Entry: domain.XrefEntry{TargetID: mine}},
			})
		}()
	}
	wg.Wait()
	for i := 0; i < racers; i++ {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if results[i]["C1"] != results[0]["C1"] || results[i]["C0"] != loaded.Entry.TargetID {
			t.Fatalf("racer %d saw %+v, racer 0 saw %+v", i, results[i], results[0])
		}
	}
	got, err := x.GetMany(ctx, "ax2012", "customer", []string{"C0"})
	if err != nil || got["C0"].ContentHash != "loaded" {
		t.Fatalf("reservation overwrote loaded hash: %+v, %v", got, err)
	}
}

type generatedExtractor struct{ n int }

func (e generatedExtractor) Extract(context.Context, string) ([]domain.Record, error) {
	rows := make([]domain.Record, e.n)
	for i := range rows {
		id := fmt.Sprintf("I%07d", i)
		rows[i] = domain.Record{Key: id, Fields: map[string]any{"id": id}}
	}
	return rows, nil
}

func TestPostgres_FullSizeLoadMeetsTimeBudgetAndRerunsUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("full-size Postgres load skipped under -short")
	}
	const rows = 180_000
	s := newStore(t)
	ctx := principalContext(1)
	mapping, err := domain.ParseMapping([]byte(`{"entity":"item","version":1,"fields":[{"source":"id","target":"sku","type":"string","required":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	loader := domain.NewMemLoader()
	n := 0
	p := &domain.Pipeline{Extractor: generatedExtractor{rows}, Loader: loader, Reader: loader, Xref: s.Xref(), IDGen: func() string {
		n++
		return fmt.Sprintf("00000000-0000-7000-8000-%012d", n)
	}}
	run := domain.RunSpec{SourceSystem: "ax2012", Entity: "item", Mapping: mapping}

	start := time.Now()
	first, err := p.Run(ctx, run)
	if err != nil || first.Status != domain.StatusSucceeded || first.Created != rows {
		t.Fatalf("first run status=%s created=%d err=%v", first.Status, first.Created, err)
	}
	if elapsed := time.Since(start); elapsed >= 30*time.Minute {
		t.Fatalf("load took %s", elapsed)
	}
	second, err := p.Run(ctx, run)
	if err != nil || second.Unchanged != rows || second.Created != 0 || second.Updated != 0 {
		t.Fatalf("rerun unchanged=%d created=%d updated=%d err=%v", second.Unchanged, second.Created, second.Updated, err)
	}
}

func TestMem_FakeMatchesStoreContract(t *testing.T) {
	m := store.NewMem()
	ctx := context.Background()
	saved, _ := m.SaveRun(ctx, sampleRun(), "k")
	if replay, _ := m.SaveRun(ctx, sampleRun(), "k"); replay.ID != saved.ID {
		t.Fatal("replay must return the original run")
	}
	if _, err := m.GetRun(ctx, "missing"); err == nil {
		t.Fatal("unknown run must error")
	}
}

func TestPostgres_RLSHidesOtherTenantsAndRequiresTenantWideScope(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	app := store.New(pool)
	one := principalContext(1)
	if _, err := app.SaveRun(one, sampleRun(), ""); err != nil {
		t.Fatal(err)
	}
	if err := app.Xref().Put(one, domain.XrefKey{SourceSystem: "ax2012", Entity: "customer", SourceKey: "C1"}, domain.XrefEntry{TargetID: "00000000-0000-7000-8000-000000000001"}); err != nil {
		t.Fatal(err)
	}
	count := func(c context.Context, table string) int {
		var n int
		err := dbx.WithTenantTx(c, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(c, "select count(*) from migration."+table).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	scoped := tenant.With(ctx, tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9)})
	for _, table := range []string{"run", "run_ledger", "xref"} {
		if own := count(one, table); own == 0 {
			t.Errorf("%s: tenant 1 must see its rows", table)
		}
		if other := count(principalContext(2), table); other != 0 {
			t.Errorf("%s: tenant 2 sees %d rows of tenant 1", table, other)
		}
		if narrowed := count(scoped, table); narrowed != 0 {
			t.Errorf("%s: a legal-entity-scoped session sees %d tenant-wide rows", table, narrowed)
		}
	}
}
