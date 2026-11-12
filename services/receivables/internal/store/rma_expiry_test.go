package store

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/billingclient"
)

func ageCredit(t *testing.T, s *Postgres, ctx context.Context, id string, by time.Duration) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update rma_credit set created_at = created_at - $2::interval where id = $1`, id, by.String())
		return err
	})
	if err != nil {
		t.Fatalf("age credit: %v", err)
	}
}

// An abandoned (parked) RMA credit older than the TTL is cancelled by the sweep and its RMA can be credited again (#557).
func TestExpireRMAReservations_CancelsOnlyOldAbandonedCredits(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "period_closed"}
	_, _ = s.PostRMACredit(ctx, in)
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	if !row.parked {
		t.Fatalf("setup: %+v", row)
	}
	ttl := 72 * time.Hour
	res, err := s.ExpireRMAReservations(ctx, time.Now(), ttl, 10)
	if err != nil || res.Expired != 0 {
		t.Fatalf("a fresh credit must stay: %+v %v", res, err)
	}
	if res, err = s.ExpireRMAReservations(ctx, time.Now(), 0, 10); err != nil || res != (RMAExpiryResult{}) {
		t.Fatalf("ttl 0 disables the pass: %+v %v", res, err)
	}
	ageCredit(t, s, ctx, row.id, 100*time.Hour)
	res, err = s.ExpireRMAReservations(ctx, time.Now(), ttl, 10)
	if err != nil || res.Expired != 1 {
		t.Fatalf("an old parked credit expires: %+v %v", res, err)
	}
	if _, found := storedCredit(t, s, ctx, in.RMAID); found || reservationStatus(t, bill, in) != "RELEASED" {
		t.Fatalf("the credit must be gone and its room released")
	}
	gl.err = nil
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("resend after expiry: %v", err)
	}
	posted, _ := storedCredit(t, s, ctx, in.RMAID)
	ageCredit(t, s, ctx, posted.id, 100*time.Hour)
	if res, err = s.ExpireRMAReservations(ctx, time.Now(), ttl, 10); err != nil || res.Expired != 0 {
		t.Fatalf("a posted credit never expires: %+v %v", res, err)
	}
}

// A post attempt that tries to claim the credit while a cancel is between its checks and its delete is refused: the cancel holds
// the post lease, so the journal cannot be booked for a credit that is about to disappear.
type hookedReserver struct {
	*billingclient.Fake
	onRelease func()
}

func (h hookedReserver) Release(ctx context.Context, legalEntityID, reservationID string) error {
	if h.onRelease != nil {
		h.onRelease()
	}
	return h.Fake.Release(ctx, legalEntityID, reservationID)
}

func TestCancelRMACredit_HoldsThePostLeaseSoAConcurrentPostCannotClaimIt(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable, Code: "down"}
	_, _ = s.PostRMACredit(ctx, in)
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	if row.parked || row.reservationID == "" {
		t.Fatalf("setup: %+v", row)
	}
	// An unparked, never tried credit holding a reservation: the case a sweep or an operator Retry can race.
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `update rma_credit set gl_parked = false, gl_attempts = 0, gl_next_attempt_at = null where id = $1`, row.id)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	var claimErr error
	var claimed bool
	s.reserver = hookedReserver{Fake: bill, onRelease: func() {
		fresh, _ := storedCredit(t, s, ctx, in.RMAID)
		_, claimed, claimErr = s.claimPost(ctx, testkit.TenantID(1), fresh, time.Now().UTC())
	}}
	if _, err := s.CancelRMACredit(ctx, row.id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if claimed || problemCode(claimErr) != CodeIdempotencyBusy {
		t.Fatalf("a post claimed a credit under cancel: claimed %v err %v", claimed, claimErr)
	}
	if _, found := storedCredit(t, s, ctx, in.RMAID); found {
		t.Fatal("the credit must be gone")
	}
}
