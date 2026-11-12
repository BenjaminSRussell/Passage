package modbilling

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
	"github.com/BenjaminSRussell/passage/services/revrec/migrations"
)

type fakeBilling struct {
	answers []Answer
	keys    []string
	bodies  []map[string]any
	actors  []string
}

func (f *fakeBilling) Send(ctx context.Context, _, key string, body []byte) (Answer, error) {
	p, _ := tenant.FromContext(ctx)
	f.actors = append(f.actors, p.UserID)
	f.keys = append(f.keys, key)
	var b map[string]any
	_ = json.Unmarshal(body, &b)
	f.bodies = append(f.bodies, b)
	a := f.answers[0]
	if len(f.answers) > 1 {
		f.answers = f.answers[1:]
	}
	return a, nil
}

func ratableContract(t *testing.T, le string) store.Contract {
	t.Helper()
	obs := []domain.Obligation{{ID: "sup", SSP: decimal.RequireFromString("1200"), SSPSource: "EXPLICIT", Method: domain.MethodRatable}}
	allocated, err := domain.Allocate(money.Must("1200.00", "USD"), obs, domain.ResidualLargest)
	if err != nil {
		t.Fatal(err)
	}
	start, end := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC)
	c := store.Contract{ID: ids.New(), LegalEntityID: le, OrderID: ids.New(), CustomerID: ids.New(), Currency: "USD",
		TransactionPrice: decimal.RequireFromString("1200.00"), ResidualRule: domain.ResidualLargest, RatableBasis: domain.BasisMonthly,
		ContractDate: start, IdempotencyKey: ids.New(), RequestHash: "hash"}
	ob := store.Obligation{ID: ids.New(), ContractID: c.ID, LineNo: 1, ProductID: ids.New(), SSP: allocated[0].SSP, SSPSource: "EXPLICIT",
		Allocated: allocated[0].Allocated, Method: domain.MethodRatable, TermStart: &start, TermEnd: &end}
	lines, err := domain.ScheduleLines(allocated[0], "USD", domain.BasisMonthly, &start, &end, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		ob.Schedule = append(ob.Schedule, store.Schedule{ID: ids.New(), Seq: l.Seq, Date: l.Date, Amount: l.Amount, Status: domain.ScheduleScheduled})
	}
	c.Obligations = []store.Obligation{ob}
	return c
}

// A modification's price change is sent to billing signed as revrec-billing with its frozen body and key: a 201 bills it, a refusal
// parks it, and a retry puts a parked one back in the queue.
func TestReporter_BillsParksAndRetries(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: testkit.ScopeDuties("")})
	c, err := s.CreateContract(ctx, ratableContract(t, le))
	if err != nil {
		t.Fatal(err)
	}
	_, mod, err := s.ModifyContract(ctx, c.ID, store.ModificationRequest{Type: domain.ModProspective, EffectiveDate: time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC),
		PriceChange: decimal.RequireFromString("120.00")})
	if err != nil || mod.BillingID == "" {
		t.Fatalf("modify: %v (%+v)", err, mod)
	}
	fake := &fakeBilling{answers: []Answer{{Status: http.StatusServiceUnavailable, Detail: "down"}}}
	r := &Reporter{Store: s, Billing: fake}
	if n, err := r.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("first run: %d %v", n, err)
	}
	if fake.keys[0] != store.ModificationBillingKeyPrefix+mod.ID || fake.bodies[0]["priceChange"] != "120.00" || fake.actors[0] != tenant.ServiceActor(PrincipalName) {
		t.Fatalf("sent key %s body %v actor %s", fake.keys[0], fake.bodies[0], fake.actors[0])
	}
	// The outage leaves it PENDING with a back-off: nothing is due now.
	if n, _ := r.RunOnce(context.Background()); n != 0 {
		t.Fatalf("a backed-off request was sent again: %d", n)
	}
	if _, err := admin.Exec(context.Background(), `update modification_billing set next_attempt_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	fake.answers = []Answer{{Status: http.StatusUnprocessableEntity, Detail: "BIL-0140 the order has no posted invoice line"}}
	if n, err := r.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("second run: %d %v", n, err)
	}
	var status, lastError string
	if err := admin.QueryRow(context.Background(), `select status, coalesce(last_error, '') from modification_billing where modification_id = $1`, mod.ID).Scan(&status, &lastError); err != nil || status != store.BillingParked {
		t.Fatalf("after a refusal: %s %s (%v)", status, lastError, err)
	}
	b, err := s.RetryBilling(ctx, mod.ID)
	if err != nil || b.Status != store.BillingPending || b.Attempts != 0 {
		t.Fatalf("retry: %+v (%v)", b, err)
	}
	fake.answers = []Answer{{Status: http.StatusCreated}}
	if n, err := r.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("third run: %d %v", n, err)
	}
	if err := admin.QueryRow(context.Background(), `select status from modification_billing where modification_id = $1`, mod.ID).Scan(&status); err != nil || status != store.BillingBilled {
		t.Fatalf("after 201: %s (%v)", status, err)
	}
}
