package store

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func intakeOrder(le, customer, ref, hash string) NewOrder {
	in := oneLineOrder(le, customer, ids.New(), time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC))
	in.Channel, in.ExternalRef, in.RequestHash = "EDI", &ref, hash
	return in
}

func problemStatus(err error) int {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return 0
}

// TestPostgres_IntakeDedupWindow covers BR-27.10 with intake_dedup_window_days:
// inside the window a repeat of (channel, externalRef) returns the order (same
// body) or is refused with SLS-0007 (other body); once the order is older than
// the window the reference opens a new order, and the reference and the window
// are per legal entity.
func TestPostgres_IntakeDedupWindow(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, other, customer := ids.New(), ids.New(), ids.New()
	first, created, err := s.CreateOrder(ctx, intakeOrder(le, customer, "PO-1", "body-a"))
	if err != nil || !created {
		t.Fatalf("first intake: %v created=%v", err, created)
	}
	again, created, err := s.CreateOrder(ctx, intakeOrder(le, customer, "PO-1", "body-a"))
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("a repeat inside the window returns the order: %v created=%v %s vs %s", err, created, again.ID, first.ID)
	}
	if _, _, err := s.CreateOrder(ctx, intakeOrder(le, customer, "PO-1", "body-b")); problemStatus(err) != http.StatusConflict {
		t.Fatalf("another body inside the window: %v", err)
	}
	// The reference is matched within the legal entity: the same reference for
	// another legal entity opens that legal entity's own order, even for a
	// caller who can see both.
	elsewhere, created, err := s.CreateOrder(ctx, intakeOrder(other, customer, "PO-1", "body-a"))
	if err != nil || !created || elsewhere.ID == first.ID || elsewhere.LegalEntityID != other {
		t.Fatalf("the reference in another legal entity: %v created=%v %s", err, created, elsewhere.LegalEntityID)
	}

	age := func(days int) {
		if _, err := pool.Exec(ctx, `update sales_order set created_at = now() - make_interval(days => $1) where id = $2`, days, first.ID); err != nil {
			t.Fatal(err)
		}
	}
	age(29)
	if r, created, err := s.CreateOrder(ctx, intakeOrder(le, customer, "PO-1", "body-a")); err != nil || created || r.ID != first.ID {
		t.Fatalf("29 days old is inside the default 30-day window: %v created=%v", err, created)
	}
	age(31)
	reopened, created, err := s.CreateOrder(ctx, intakeOrder(le, customer, "PO-1", "body-b"))
	if err != nil || !created || reopened.ID == first.ID {
		t.Fatalf("31 days old is outside the window, so the reference opens a new order (any body): %v created=%v", err, created)
	}
	if r, created, err := s.CreateOrder(ctx, intakeOrder(le, customer, "PO-1", "body-b")); err != nil || created || r.ID != reopened.ID {
		t.Fatalf("the newest order with the reference is the one a repeat finds: %v created=%v", err, created)
	}

	// A longer window keeps the old order matching; a shorter one releases it sooner.
	p := domain.DefaultParameters()
	p.IntakeDedupWindowDays = 90
	if _, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: other, Parameters: p}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	long, _, err := s.CreateOrder(ctx, intakeOrder(other, customer, "PO-9", "body-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update sales_order set created_at = now() - interval '60 days' where id = $1`, long.ID); err != nil {
		t.Fatal(err)
	}
	if r, created, err := s.CreateOrder(ctx, intakeOrder(other, customer, "PO-9", "body-a")); err != nil || created || r.ID != long.ID {
		t.Fatalf("60 days old is inside a 90-day window: %v created=%v", err, created)
	}
	p.IntakeDedupWindowDays = 7
	saved, err := s.GetParameters(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if saved.IntakeDedupWindowDays != 90 {
		t.Fatalf("window stored: %d", saved.IntakeDedupWindowDays)
	}
	if _, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: other, Parameters: p}, saved.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	if r, created, err := s.CreateOrder(ctx, intakeOrder(other, customer, "PO-9", "body-a")); err != nil || !created || r.ID == long.ID {
		t.Fatalf("60 days old is outside a 7-day window: %v created=%v", err, created)
	}
}

// TestPostgres_IntakeDedupConcurrent: concurrent first intakes of one reference
// create one order now that no unique index backs the reference.
func TestPostgres_IntakeDedupConcurrent(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	var wg sync.WaitGroup
	idsSeen := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, err := s.CreateOrder(ctx, intakeOrder(le, customer, "PO-RACE", "same-body"))
			if err != nil {
				t.Error(err)
				return
			}
			idsSeen <- o.ID
		}()
	}
	wg.Wait()
	close(idsSeen)
	distinct := map[string]bool{}
	for id := range idsSeen {
		distinct[id] = true
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from sales_order where external_ref = 'PO-RACE'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if len(distinct) != 1 || n != 1 {
		t.Fatalf("one order for one reference: %d distinct ids, %d rows", len(distinct), n)
	}
}

// TestPostgres_CreditProfilesReviewDue: the review-due list holds the profiles
// whose review date is on or before the day asked, oldest review date first,
// and leaves out profiles without a review date.
func TestPostgres_CreditProfilesReviewDue(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	day := func(v string) *time.Time { t, _ := time.Parse("2006-01-02", v); return &t }
	put := func(customer string, review *time.Time) {
		t.Helper()
		if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
			Limit: d(1000), Currency: "USD", LimitType: domain.LimitBalance, ReviewDate: review}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	late, soon, edge, far, none := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	put(late, day("2027-01-10"))
	put(soon, day("2027-03-01"))
	put(edge, day("2027-03-31"))
	put(far, day("2027-09-01"))
	put(none, nil)
	due, err := s.ListCreditProfilesReviewDue(ctx, le, "2027-03-31", 50)
	if err != nil || len(due) != 3 || due[0].CustomerID != late || due[1].CustomerID != soon || due[2].CustomerID != edge {
		t.Fatalf("due by 2027-03-31: %v %+v", err, due)
	}
	if all, err := s.ListCreditProfiles(ctx, le, 50); err != nil || len(all) != 5 {
		t.Fatalf("unfiltered list still has every profile: %v %d", err, len(all))
	}
	if limited, err := s.ListCreditProfilesReviewDue(ctx, le, "2028-01-01", 2); err != nil || len(limited) != 2 || limited[0].CustomerID != late {
		t.Fatalf("limit applies after the review-date order: %v %+v", err, limited)
	}
}
