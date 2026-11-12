package store_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
	"github.com/BenjaminSRussell/passage/services/audit/migrations"
)

func problemCode(err error) (int, string) {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status, p.Code
	}
	return 0, ""
}

func TestBreakGlassReadsAreQueuedAndReviewedOnceByAnotherPerson(t *testing.T) {
	s, ctx := migrated(t)
	ordinary, err := s.RecordRead(ctx, phiRead("w-1", "user-1", "benefits enquiry"))
	if err != nil {
		t.Fatal(err)
	}
	glass, err := s.RecordRead(ctx, phiRead("w-2", "user-1", "break-glass: member in ER, HR closed"))
	if err != nil {
		t.Fatal(err)
	}
	// A purpose that only starts with the letters is not the reason code.
	if _, err := s.RecordRead(ctx, phiRead("w-3", "user-1", "BREAK-GLASSES lost")); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListBreakGlass(ctx, store.ReviewPending, 0, 0)
	if err != nil || len(pending) != 1 || pending[0].ID != glass.ID || pending[0].Review != nil {
		t.Fatalf("pending queue: %+v %v", pending, err)
	}

	if _, err := s.ReviewRead(ctx, glass.ID, "user-1", domain.ReviewApproved, ""); err == nil {
		t.Fatal("the reader must not review their own break-glass access")
	} else if status, code := problemCode(err); status != http.StatusConflict || code != domain.CodeReviewRefused {
		t.Fatalf("self review: %d %s %v", status, code, err)
	}
	if _, err := s.ReviewRead(ctx, ordinary.ID, "user-2", domain.ReviewApproved, ""); err == nil {
		t.Fatal("an ordinary read is not reviewed")
	}
	if _, err := s.ReviewRead(ctx, ids.New(), "user-2", domain.ReviewApproved, ""); err == nil {
		t.Fatal("unknown read")
	} else if status, code := problemCode(err); status != http.StatusNotFound || code != domain.CodeReadNotFound {
		t.Fatalf("unknown read: %d %s", status, code)
	}

	got, err := s.ReviewRead(ctx, glass.ID, "user-2", domain.ReviewFlagged, "  no ticket for the ER call  ")
	if err != nil || got.Review == nil || got.Review.Outcome != domain.ReviewFlagged || got.Review.ReviewerID != "user-2" ||
		got.Review.Note != "no ticket for the ER call" || got.Hash != glass.Hash {
		t.Fatalf("review: %+v %v", got, err)
	}
	if _, err := s.ReviewRead(ctx, glass.ID, "user-3", domain.ReviewApproved, ""); err == nil {
		t.Fatal("a read is reviewed once")
	}
	if pending, _ = s.ListBreakGlass(ctx, store.ReviewPending, 0, 0); len(pending) != 0 {
		t.Fatalf("still pending: %+v", pending)
	}
	reviewed, err := s.ListBreakGlass(ctx, store.ReviewReviewed, 10, 0)
	if err != nil || len(reviewed) != 1 || reviewed[0].Review.Outcome != domain.ReviewFlagged {
		t.Fatalf("reviewed: %+v %v", reviewed, err)
	}
	// The review lives beside the chain, so the read log still verifies.
	if chain, err := s.VerifyReads(ctx); err != nil || !chain.Verified || chain.Entries != 3 {
		t.Fatalf("chain after review: %+v %v", chain, err)
	}
	// Another tenant sees neither the read nor its review.
	other := tenant.With(context.Background(), principal(2))
	if all, err := s.ListBreakGlass(other, store.ReviewAll, 0, 0); err != nil || len(all) != 0 {
		t.Fatalf("tenant 2 saw %+v %v", all, err)
	}
	if _, err := s.ReviewRead(other, glass.ID, "user-9", domain.ReviewApproved, ""); err == nil {
		t.Fatal("tenant 2 must not review tenant 1's read")
	}
}

func TestConcurrentReviewsOfOneReadLeaveOne(t *testing.T) {
	s, ctx := migrated(t)
	glass, err := s.RecordRead(ctx, phiRead("w-1", "user-1", "BREAK-GLASS"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ReviewRead(ctx, glass.ID, "user-2", domain.ReviewApproved, "")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		} else if status, code := problemCode(err); status != http.StatusConflict || code != domain.CodeReviewRefused {
			t.Errorf("a losing review must be refused, not fail: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d reviews succeeded; want 1", ok)
	}
}

func TestReviewWorksAsTheAppRoleAndIsRowLevelSecured(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var enabled, forced bool
	if err := app.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = 'phi_read_review' and relnamespace = current_schema()::regnamespace`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("phi_read_review rls enabled=%v forced=%v err=%v", enabled, forced, err)
	}
	s := store.New(app)
	glass, err := s.RecordRead(ctx, phiRead("w-1", "user-1", "BREAK-GLASS: ER"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewRead(ctx, glass.ID, "user-2", domain.ReviewApproved, ""); err != nil {
		t.Fatalf("review as passage_app: %v", err)
	}
	for _, stmt := range []string{`update phi_read_review set outcome = 'FLAGGED'`, `delete from phi_read_review`} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}
