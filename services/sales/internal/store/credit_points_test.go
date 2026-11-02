package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func creditPointsParams(points ...string) domain.Parameters {
	p := domain.DefaultParameters()
	p.CreditCheckPoints = points
	return p
}

func draftOrder(ctx context.Context, t *testing.T, s *Postgres, le, customer string, day time.Time, net int64, key string) Order {
	t.Helper()
	in := oneLineOrder(le, customer, ids.New(), day)
	in.RequestHash = key
	in.Lines[0].UnitPrice, in.Lines[0].Quantity, in.Lines[0].LineNet = d(net), d(1), d(net)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// TestPostgres_CreditCheckPoints covers FR-27.7: the entry check is advisory
// (stored and evented, no hold), a legal entity without confirm in its points
// confirms without a check, and the release point records a fresh evaluation.
func TestPostgres_CreditCheckPoints(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	day := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5), ToleranceAmount: d(1000), OldestItemDaysMax: 30, MaxDunningLevel: 3},
		Exposure: domain.Exposure{OpenAR: d(8500)}}, 0); err != nil {
		t.Fatal(err)
	}
	count := func(point string) int {
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from outbox where type like '%creditcheck.evaluated%' and payload->'data'->>'point' = $1`, point).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := draftOrder(ctx, t, s, le, customer, day, 3000, ids.New())
	if before.CreditCheck != nil || count("entry") != 0 {
		t.Fatalf("no entry check by default: %s", before.CreditCheck)
	}
	if _, err := s.PutParameters(ctx, SalesParameters{LegalEntityID: le, Parameters: creditPointsParams("entry", "confirm", "release")}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	o := draftOrder(ctx, t, s, le, customer, day, 3000, ids.New())
	if o.CreditCheck == nil || o.Status != domain.OrderDraft || len(o.Holds) != 0 || count("entry") != 1 {
		t.Fatalf("entry check is stored and places no hold: %+v entry events %d", o, count("entry"))
	}
	held, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", day)
	if err != nil || held.Confirmed || count("confirm") != 1 {
		t.Fatalf("confirm must hold: %v confirmed=%v confirm events %d", err, held.Confirmed, count("confirm"))
	}
	queue, err := s.ListHolds(ctx, HoldQueueFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(queue) != 1 {
		t.Fatalf("queue: %v %+v", err, queue)
	}
	rel, err := s.ReleaseHold(principalCtx("credit-manager"), o.ID, queue[0].HoldID, queue[0].OrderVersion, "credit-manager", day.Add(time.Hour))
	if err != nil || !rel.Confirmed || count("release") != 1 {
		t.Fatalf("release confirms and records the release check: %v %+v release events %d", err, rel.Confirmed, count("release"))
	}

	// A quotation converted into an order is entered too.
	q, _, err := s.CreateQuotation(ctx, threeLineQuotation(le, customer, ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	sent, err := s.SendQuotation(ctx, q.ID, q.Version, day)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: sent.Version, Actor: "sales-rep"}, day)
	if err != nil || conv.Order.CreditCheck == nil || count("entry") != 2 {
		t.Fatalf("a converted order is checked at entry: %v %+v entry events %d", err, conv.Order.CreditCheck, count("entry"))
	}

	// The API refuses a set without confirm (domain.Parameters.Validate); the
	// store still honours one, as packing_slip and invoice will be blocking
	// points of their own: without confirm the order confirms unchecked.
	saved, err := s.GetParameters(ctx, le)
	if err != nil {
		t.Fatal(err)
	}
	saved.CreditCheckPoints = []string{"release"}
	if _, err := s.PutParameters(ctx, saved, saved.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	o2 := draftOrder(ctx, t, s, le, customer, day, 3000, ids.New())
	res, err := s.ConfirmOrder(ctx, o2.ID, o2.Version, "sales-rep", day)
	if err != nil || !res.Confirmed || len(res.Evaluation.Rules) != 0 || count("confirm") != 1 {
		t.Fatalf("confirm without the confirm point: %v %+v confirm events %d", err, res, count("confirm"))
	}
}

// TestPostgres_OpenOrdersExposure: BALANCE_ALL counts the customer's other
// CONFIRMED orders in the profile currency, ignoring the stored openOrders
// figure, and the profile reads back the computed figure.
func TestPostgres_OpenOrdersExposure(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	day := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalanceAll, OldestItemDaysMax: 30, MaxDunningLevel: 3},
		Exposure: domain.Exposure{OpenOrders: d(99999)}}, 0); err != nil {
		t.Fatal(err)
	}
	if p, err := s.GetCreditProfile(ctx, le, customer); err != nil || !p.Exposure.OpenOrders.IsZero() {
		t.Fatalf("no confirmed orders, stored figure ignored: %+v %v", p.Exposure, err)
	}
	first := draftOrder(ctx, t, s, le, customer, day, 6000, ids.New())
	if res, err := s.ConfirmOrder(ctx, first.ID, first.Version, "sales-rep", day); err != nil || !res.Confirmed {
		t.Fatalf("first order confirms: %v %+v", err, res.Confirmed)
	}
	if p, err := s.GetCreditProfile(ctx, le, customer); err != nil || !p.Exposure.OpenOrders.Equal(d(6000)) {
		t.Fatalf("open orders 6000: %+v %v", p.Exposure, err)
	}
	if l, err := s.ListCreditProfiles(ctx, le, 50); err != nil || len(l) != 1 || !l[0].Exposure.OpenOrders.Equal(d(6000)) {
		t.Fatalf("the list shows the computed 6000, not the 99999 sent: %+v %v", l, err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `select exposure->>'openOrders' from credit_profile where customer_id = $1`, customer).Scan(&stored); err != nil || stored != "0" {
		t.Fatalf("a PUT value is not stored: %q %v", stored, err)
	}
	// 6000 open plus 5000 is over 10000, with no tolerance: held.
	second := draftOrder(ctx, t, s, le, customer, day, 5000, ids.New())
	res, err := s.ConfirmOrder(ctx, second.ID, second.Version, "sales-rep", day)
	if err != nil || res.Confirmed || res.Evaluation.Outcome != domain.RuleFail {
		t.Fatalf("second order must hold on open orders: %v %+v", err, res)
	}
	// An order is not counted against itself when checked again after it is confirmed.
	third := draftOrder(ctx, t, s, le, customer, day, 3000, ids.New())
	if res, err := s.ConfirmOrder(ctx, third.ID, third.Version, "sales-rep", day); err != nil || !res.Confirmed {
		t.Fatalf("6000 + 3000 fits: %v %+v", err, res.Confirmed)
	}
}

// TestPostgres_OpenOrdersCheckedOneAtATime: the credit check locks the
// customer's profile, so an order confirming while another order of the same
// customer is being confirmed waits for it and counts it, instead of both
// passing on exposures that each miss the other.
func TestPostgres_OpenOrdersCheckedOneAtATime(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	day := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalanceAll, OldestItemDaysMax: 30, MaxDunningLevel: 3}}, 0); err != nil {
		t.Fatal(err)
	}
	first := draftOrder(ctx, t, s, le, customer, day, 6000, ids.New())
	second := draftOrder(ctx, t, s, le, customer, day, 5000, ids.New())

	// Another confirmation holds the profile lock and has confirmed the
	// first order but not committed yet.
	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback(ctx)
	if _, err := other.Exec(ctx, `select 1 from credit_profile where legal_entity_id = $1 and customer_id = $2 for update`, le, customer); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, `update sales_order set status = 'CONFIRMED' where id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		res ConfirmResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := s.ConfirmOrder(ctx, second.ID, second.Version, "sales-rep", day)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		t.Fatalf("the check must wait for the profile lock: %v %+v", o.err, o.res.Confirmed)
	case <-time.After(300 * time.Millisecond):
	}
	if err := other.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	o := <-done
	if o.err != nil || o.res.Confirmed || o.res.Evaluation.Outcome != domain.RuleFail {
		t.Fatalf("6000 confirmed meanwhile plus 5000 is over 10000: %v %+v", o.err, o.res)
	}
}

// TestPostgres_OpenOrdersInOtherCurrencies: open orders that cannot be
// converted to the profile currency are listed on the profile and flagged on
// the check rather than dropped silently.
func TestPostgres_OpenOrdersInOtherCurrencies(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	day := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalanceAll, OldestItemDaysMax: 30, MaxDunningLevel: 3}}, 0); err != nil {
		t.Fatal(err)
	}
	in := oneLineOrder(le, customer, ids.New(), day)
	in.Currency = "EUR"
	in.Lines[0].UnitPrice, in.Lines[0].Quantity, in.Lines[0].LineNet = d(4000), d(1), d(4000)
	eur, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Confirmed after review by a credit manager (CR-1 fails on a foreign order).
	if _, err := pool.Exec(ctx, `update sales_order set status = 'CONFIRMED' where id = $1`, eur.ID); err != nil {
		t.Fatal(err)
	}
	want := []domain.CurrencyAmount{{Currency: "EUR", Amount: d(4000)}}
	p, err := s.GetCreditProfile(ctx, le, customer)
	if err != nil || !p.Exposure.OpenOrders.IsZero() || len(p.Exposure.OpenOrdersOtherCurrencies) != 1 ||
		p.Exposure.OpenOrdersOtherCurrencies[0].Currency != "EUR" || !p.Exposure.OpenOrdersOtherCurrencies[0].Amount.Equal(want[0].Amount) {
		t.Fatalf("profile lists the EUR open orders: %+v %v", p.Exposure, err)
	}
	listed, err := s.ListCreditProfiles(ctx, le, 50)
	if err != nil || len(listed) != 1 || len(listed[0].Exposure.OpenOrdersOtherCurrencies) != 1 {
		t.Fatalf("list shows the same: %+v %v", listed, err)
	}
	usd := draftOrder(ctx, t, s, le, customer, day, 1000, ids.New())
	res, err := s.ConfirmOrder(ctx, usd.ID, usd.Version, "sales-rep", day)
	if err != nil || !res.Confirmed || res.Evaluation.Outcome != domain.RuleWarn {
		t.Fatalf("a USD order within the limit confirms with a warning: %v %+v", err, res)
	}
	for _, r := range res.Evaluation.Rules {
		if r.ID == "CR-1" && r.Note == "" {
			t.Fatalf("CR-1 must name the uncounted orders: %+v", r)
		}
	}
}
