package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
	"github.com/BenjaminSRussell/passage/services/audit/migrations"
)

func phiRead(record, user, purpose string) domain.Read {
	return domain.Read{
		Entity: "hr.worker.benefits", RecordID: record, Fields: []string{"enrollments.optionCode"}, Purpose: purpose,
		UserID: user, Source: "hr", TraceID: "trace-" + record, OccurredAt: time.Now().UTC(),
	}
}

func TestReadLogChainSurvivesPostgresAndStaysPerTenant(t *testing.T) {
	s, ctx := migrated(t)
	for i, rec := range []string{"w-1", "w-2", "w-1"} {
		got, err := s.RecordRead(ctx, phiRead(rec, "user-1", "benefits enquiry"))
		if err != nil {
			t.Fatal(err)
		}
		if got.Seq == 0 || got.Hash == "" || (i == 0 && got.PrevHash != domain.Genesis) {
			t.Fatalf("read %d: %+v", i, got)
		}
	}
	if _, err := s.RecordRead(ctx, phiRead("w-9", "user-2", "audit")); err != nil {
		t.Fatal(err)
	}
	chain, err := s.VerifyReads(ctx)
	if err != nil || !chain.Verified || chain.Entries != 4 || chain.BrokenAt != nil {
		t.Fatalf("chain %+v %v (a nanosecond time must still verify after the microsecond round trip)", chain, err)
	}
	rows, err := s.ListReads(ctx, store.ReadFilter{RecordID: "w-1"})
	if err != nil || len(rows) != 2 || rows[0].Fields[0] != "enrollments.optionCode" || rows[0].UserID != "user-1" || rows[0].SourceService != "hr" {
		t.Fatalf("by record: %+v %v", rows, err)
	}
	if rows, err = s.ListReads(ctx, store.ReadFilter{UserID: "user-2"}); err != nil || len(rows) != 1 || rows[0].RecordID != "w-9" {
		t.Fatalf("by user: %+v %v", rows, err)
	}
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	if rows, err = s.ListReads(ctx, store.ReadFilter{From: &from, To: &to, Limit: 2}); err != nil || len(rows) != 2 {
		t.Fatalf("by time with a limit: %+v %v", rows, err)
	}
	if rows, err = s.ListReads(ctx, store.ReadFilter{AfterSeq: rows[1].Seq}); err != nil || len(rows) != 2 {
		t.Fatalf("after a cursor: %+v %v", rows, err)
	}

	other := tenant.With(context.Background(), principal(2))
	if hidden, err := s.ListReads(other, store.ReadFilter{}); err != nil || len(hidden) != 0 {
		t.Fatalf("tenant 2 saw %v %v", hidden, err)
	}
	first, err := s.RecordRead(other, phiRead("w-1", "user-7", "audit"))
	if err != nil || first.PrevHash != domain.Genesis {
		t.Fatalf("tenant 2 has its own chain from Genesis: %+v %v", first, err)
	}
}

func TestReadLogExportVerifiesBeforeStreaming(t *testing.T) {
	s, ctx := migrated(t)
	for _, rec := range []string{"w-1", "w-2"} {
		if _, err := s.RecordRead(ctx, phiRead(rec, "user-1", "audit")); err != nil {
			t.Fatal(err)
		}
	}
	var verdict store.ReadChain
	var streamed []string
	err := s.ExportReads(ctx, store.ReadFilter{RecordID: "w-2"},
		func(c store.ReadChain) error { verdict = c; return nil },
		func(r store.ReadRecord) error { streamed = append(streamed, r.RecordID); return nil })
	if err != nil || !verdict.Verified || verdict.Entries != 2 || len(streamed) != 1 || streamed[0] != "w-2" {
		t.Fatalf("export: verdict %+v streamed %v err %v", verdict, streamed, err)
	}
}

func TestReadLogConcurrentAppendsDoNotFork(t *testing.T) {
	s, ctx := migrated(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.RecordRead(ctx, phiRead("w-1", "user-1", "audit"))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if chain, err := s.VerifyReads(ctx); err != nil || !chain.Verified || chain.Entries != 8 {
		t.Fatalf("eight concurrent reads must form one chain: %+v %v", chain, err)
	}
}

func TestReadLogIsAppendOnlyAndRowLevelSecured(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var enabled, forced bool
	if err := app.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = 'phi_read' and relnamespace = current_schema()::regnamespace`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("phi_read rls enabled=%v forced=%v err=%v", enabled, forced, err)
	}
	s := store.New(app)
	if _, err := s.RecordRead(ctx, phiRead("w-1", "user-1", "audit")); err != nil {
		t.Fatalf("record as passage_app needs the phi_read sequence: %v", err)
	}
	for _, stmt := range []string{
		`update phi_read set purpose = 'tamper'`,
		`delete from phi_read`,
		`truncate phi_read`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
	// A row written for another tenant is invisible and unwritable through this tenant's session.
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into phi_read (tenant_id, id, entity, record_id, fields, purpose, user_id, trace_id, occurred_at, prev_hash, hash)
			values ($1, gen_random_uuid(), 'e', 'r', '{f}', 'p', 'u', 't', now(), repeat('0', 64), repeat('1', 64))`, testkit.TenantID(2))
		return err
	})
	if err == nil {
		t.Fatal("RLS must refuse a row for another tenant")
	}
}

func TestReadLogTriggerRejectsMutationEvenForTheOwner(t *testing.T) {
	// The owner holds every privilege, so only the trigger stands between it and the rows: the statements must fail
	// with the trigger's exception (raise_exception, P0001), not because the table is missing.
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	read, err := s.RecordRead(ctx, phiRead("w-1", "user-1", "BREAK-GLASS: ER"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewRead(ctx, read.ID, "user-2", domain.ReviewApproved, ""); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`update phi_read set purpose = 'tamper'`, `delete from phi_read`, `truncate phi_read cascade`,
		`update phi_read_review set outcome = 'FLAGGED', note = 'x'`, `delete from phi_read_review`, `truncate phi_read_review`,
	} {
		err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "P0001" {
			t.Errorf("%q = %v; want the append-only trigger's exception (P0001)", stmt, err)
		}
	}
}
