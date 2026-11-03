package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// TestPostgres_PartlyShippedExposureIsUndelivered covers BR-27.3: a partly
// delivered order counts only the undelivered part of its lines as open
// exposure, on the profile read and on the profile list, and nothing once a
// line is delivered in full or cancelled.
func TestPostgres_PartlyShippedExposureIsUndelivered(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer, a, b := ids.New(), ids.New(), ids.New(), ids.New()
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalanceAll, OldestItemDaysMax: 30, MaxDunningLevel: 3}}, 0); err != nil {
		t.Fatal(err)
	}
	in := orderOf(le, customer, a, "2027-03-10", 10)
	line2 := in.Lines[0]
	line2.LineNo, line2.ProductID = 2, b
	in.Lines = append(in.Lines, line2)
	o := confirmed(t, s, ctx, in)
	open := func(want string) {
		t.Helper()
		w := decimal.RequireFromString(want)
		if p, err := s.GetCreditProfile(ctx, le, customer); err != nil || !p.Exposure.OpenOrders.Equal(w) {
			t.Fatalf("open orders want %s: %+v %v", want, p.Exposure, err)
		}
		if l, err := s.ListCreditProfiles(ctx, le, 50); err != nil || len(l) != 1 || !l[0].Exposure.OpenOrders.Equal(w) {
			t.Fatalf("list want %s: %+v %v", want, l, err)
		}
	}
	open("200")
	deliver(t, s, ctx, o.ID, "PS-1", 1, 4)
	open("160") // 200 less 4 x 10 delivered
	deliver(t, s, ctx, o.ID, "PS-2", 1, 6)
	open("100") // line 1 delivered in full
	deliver(t, s, ctx, o.ID, "PS-3", 2, 10)
	open("0") // SHIPPED

	// A discounted line counts the line net less the net of the delivered
	// quantity at the same price and discount: 7 x 12.34 less 12.5% is 75.5825,
	// 3 delivered are 32.3925, so 43.19 is open.
	disc := orderOf(le, customer, ids.New(), "2027-03-10", 7)
	disc.Lines[0].UnitPrice, disc.Lines[0].DiscPct = decimal.RequireFromString("12.34"), decimal.RequireFromString("12.5")
	disc.Lines[0].LineNet = decimal.RequireFromString("75.5825")
	o2 := confirmed(t, s, ctx, disc)
	open("75.5825")
	deliver(t, s, ctx, o2.ID, "PS-4", 1, 3)
	open("43.19")
	// Closing the line's remainder leaves nothing open.
	cur, _ := s.GetOrder(ctx, o2.ID)
	if _, err := s.CancelLineRemainder(ctx, o2.ID, 1, cur.Version, "", "rep", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	open("0")
}

// TestPostgres_CancelLineRemainder covers BR-27.5 for one line: the line
// delivered in part is closed at the delivered quantity, the other line is not
// touched, the order stays partly shipped, and a line with nothing left to
// cancel or an unknown line is refused.
func TestPostgres_CancelLineRemainder(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, a, b := ids.New(), ids.New(), ids.New(), ids.New()
	q, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, a, nil, "2027-03-01", "2027-03-31", 100))
	if err != nil {
		t.Fatal(err)
	}
	in := orderOf(le, customer, a, "2027-03-10", 10)
	line2 := in.Lines[0]
	line2.LineNo, line2.ProductID = 2, b
	in.Lines = append(in.Lines, line2)
	o := confirmed(t, s, ctx, in)
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, err := s.CancelLineRemainder(ctx, o.ID, 1, o.Version, "early", "rep", now); problemCode(t, err) != CodeNotCancellable {
		t.Fatalf("a confirmed order has no remainder: %v", err)
	}
	deliver(t, s, ctx, o.ID, "PS-1", 1, 4)
	got, _ := s.GetOrder(ctx, o.ID)
	if _, err := s.CancelLineRemainder(ctx, o.ID, 9, got.Version, "x", "rep", now); problemCode(t, err) != CodeLineNotFound {
		t.Fatalf("unknown line: %v", err)
	}
	// Line 0 is not "every line": it is refused and nothing is closed.
	if _, err := s.CancelLineRemainder(ctx, o.ID, 0, got.Version, "x", "rep", now); problemCode(t, err) != CodeLineNotFound {
		t.Fatalf("line 0: %v", err)
	}
	if _, err := s.CancelLineRemainder(ctx, o.ID, 1, got.Version-1, "stale", "rep", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	res, err := s.CancelLineRemainder(ctx, o.ID, 1, got.Version, "customer takes 4 of line 1", "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	l1, l2 := res.Lines[0], res.Lines[1]
	if res.Status != domain.OrderPartiallyShipped || res.Version != got.Version+1 || !res.NetTotal.Equal(d(140)) ||
		!l1.Quantity.Equal(d(4)) || !l1.LineNet.Equal(d(40)) || l1.LineStatus != domain.LineDelivered ||
		!l2.Quantity.Equal(d(10)) || l2.LineStatus == domain.LineCancelled || l2.LineStatus == domain.LineDelivered {
		t.Fatalf("after: %s v%d total %s lines %+v %+v", res.Status, res.Version, res.NetTotal, l1, l2)
	}
	qa, _ := s.GetAllocationQuota(ctx, q.ID)
	if !qa.ConsumedQty.Equal(d(4)) {
		t.Fatalf("quota of line 1 keeps what was delivered: %s", qa.ConsumedQty)
	}
	var data salesevents.OrderRemainderCancelledData
	if err := json.Unmarshal(outboxData(t, pool, o.ID, salesevents.OrderRemainderCancelled), &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != domain.OrderPartiallyShipped || data.Version != res.Version ||
		len(data.Lines) != 1 || data.Lines[0].LineNo != 1 || data.Lines[0].CancelledQty != "6" || data.Lines[0].KeptQty != "4" {
		t.Fatalf("event: %+v", data)
	}
	if _, err := s.CancelLineRemainder(ctx, o.ID, 1, res.Version, "again", "rep", now); problemCode(t, err) != CodeNotCancellable {
		t.Fatalf("a line closed already: %v", err)
	}
	// Closing line 2 (nothing delivered) cancels it and ends the order SHIPPED.
	end, err := s.CancelLineRemainder(ctx, o.ID, 2, res.Version, "", "rep", now)
	if err != nil || end.Status != domain.OrderShipped || !end.NetTotal.Equal(d(40)) || end.Lines[1].LineStatus != domain.LineCancelled {
		t.Fatalf("closing line 2: %v %+v", err, end)
	}
}

// TestPostgres_DeliveredNotInvoicedIsComputed covers BR-27.3: the delivered,
// not yet invoiced value is computed from each line's delivered less invoiced
// quantity (on the profile read, the list and the credit check), a value sent
// with the profile is ignored, an invoiced or fully invoiced order counts
// nothing, and a partly shipped order keeps its full exposure: the delivered
// part as delivered-not-invoiced, the rest as open orders.
func TestPostgres_DeliveredNotInvoicedIsComputed(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer, a, b := ids.New(), ids.New(), ids.New(), ids.New()
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalanceAll, OldestItemDaysMax: 30, MaxDunningLevel: 3},
		Exposure: domain.Exposure{DeliveredNotInvoiced: d(99999)}}, 0); err != nil {
		t.Fatal(err)
	}
	check := func(dni, open string) {
		t.Helper()
		wd, wo := decimal.RequireFromString(dni), decimal.RequireFromString(open)
		p, err := s.GetCreditProfile(ctx, le, customer)
		if err != nil || !p.Exposure.DeliveredNotInvoiced.Equal(wd) || !p.Exposure.OpenOrders.Equal(wo) {
			t.Fatalf("want dni %s open %s: %+v %v", dni, open, p.Exposure, err)
		}
		l, err := s.ListCreditProfiles(ctx, le, 50)
		if err != nil || len(l) != 1 || !l[0].Exposure.DeliveredNotInvoiced.Equal(wd) || !l[0].Exposure.OpenOrders.Equal(wo) {
			t.Fatalf("list want dni %s open %s: %+v %v", dni, open, l, err)
		}
	}
	in := orderOf(le, customer, a, "2027-03-10", 10)
	line2 := in.Lines[0]
	line2.LineNo, line2.ProductID = 2, b
	in.Lines = append(in.Lines, line2)
	o := confirmed(t, s, ctx, in)
	check("0", "200")
	deliver(t, s, ctx, o.ID, "PS-1", 1, 4)
	check("40", "160") // the whole 200 stays in the exposure
	deliver(t, s, ctx, o.ID, "PS-2", 2, 10)
	check("140", "60")
	invoice := func(ref string, lineNo int, qty int64) {
		t.Helper()
		if _, _, err := s.RecordProgress(ctx, progressIn(o.ID, domain.ProgressInvoiced, ref, "k-"+ref, "h-"+ref, ProgressLineInput{LineNo: lineNo, Quantity: d(qty)}), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	invoice("INV-1", 2, 10)
	check("40", "60") // line 2 is invoiced: it is open AR now
	invoice("INV-2", 1, 3)
	check("10", "60")
	deliver(t, s, ctx, o.ID, "PS-3", 1, 6)
	check("70", "0") // SHIPPED
	invoice("INV-3", 1, 7)
	check("0", "0") // INVOICED

	// A discounted line: 7 x 12.34 less 12.5%, 3 delivered and 1 invoiced.
	disc := orderOf(le, customer, ids.New(), "2027-03-10", 7)
	disc.Lines[0].UnitPrice, disc.Lines[0].DiscPct = decimal.RequireFromString("12.34"), decimal.RequireFromString("12.5")
	disc.Lines[0].LineNet = decimal.RequireFromString("75.5825")
	o2 := confirmed(t, s, ctx, disc)
	deliver(t, s, ctx, o2.ID, "PS-4", 1, 3)
	if _, _, err := s.RecordProgress(ctx, progressIn(o2.ID, domain.ProgressInvoiced, "INV-4", "k-INV-4", "h-INV-4", ProgressLineInput{LineNo: 1, Quantity: d(1)}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	check("21.595", "43.19") // 2 x 12.34 x 0.875

	// The order being checked is left out: its whole net total is the amount
	// under check, so neither its delivered nor its undelivered part is in the
	// exposure.
	principal, _ := tenant.FromContext(ctx)
	tenantID := principal.TenantID
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, _, err := loadProfile(ctx, tx, tenantID, le, customer)
		if err != nil {
			return err
		}
		if err := withOpenOrders(ctx, tx, tenantID, &p, o2.ID); err != nil {
			return err
		}
		if !p.Exposure.DeliveredNotInvoiced.IsZero() || !p.Exposure.OpenOrders.IsZero() {
			t.Fatalf("the order under check counts itself: %+v", p.Exposure)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPostgres_DeliveredNotInvoicedScopeAndCheck: delivered, not invoiced
// goods count only for the profile's customer and legal entity and only on
// ORDER orders; those in another currency are listed per currency; and the
// credit check at confirmation counts them for BALANCE_PACKING_SLIP (and flags
// the uncounted currency) but not for BALANCE.
func TestPostgres_DeliveredNotInvoicedScopeAndCheck(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, otherLE, customer := ids.New(), ids.New(), ids.New()
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
		Limit: d(100000), Currency: "USD", LimitType: domain.LimitBalance, OldestItemDaysMax: 30, MaxDunningLevel: 3}}, 0); err != nil {
		t.Fatal(err)
	}
	// Counted: 3 of 10 delivered at 10 = 30 USD.
	counted := confirmed(t, s, ctx, orderOf(le, customer, ids.New(), "2027-03-10", 10))
	deliver(t, s, ctx, counted.ID, "PS-1", 1, 3)
	// Not counted: the same customer in another legal entity, and a
	// SUBSCRIPTION order (billing invoices it per period).
	elsewhere := confirmed(t, s, ctx, orderOf(otherLE, customer, ids.New(), "2027-03-10", 10))
	deliver(t, s, ctx, elsewhere.ID, "PS-2", 1, 5)
	sub := confirmed(t, s, ctx, orderOf(le, customer, ids.New(), "2027-03-10", 10))
	deliver(t, s, ctx, sub.ID, "PS-3", 1, 4)
	if _, err := pool.Exec(ctx, `update sales_order set order_type = 'SUBSCRIPTION' where id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	// Listed per currency: 1 of 2 delivered on an EUR order = 10 EUR (put
	// straight to CONFIRMED: CR-1 fails a foreign order at confirmation).
	eurIn := oneLineOrder(le, customer, ids.New(), time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC))
	eurIn.Currency = "EUR"
	eur, _, err := s.CreateOrder(ctx, eurIn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update sales_order set status = 'CONFIRMED' where id = $1`, eur.ID); err != nil {
		t.Fatal(err)
	}
	deliver(t, s, ctx, eur.ID, "PS-4", 1, 1)

	p, err := s.GetCreditProfile(ctx, le, customer)
	other := p.Exposure.DeliveredNotInvoicedOtherCurrencies
	if err != nil || !p.Exposure.DeliveredNotInvoiced.Equal(d(30)) || len(other) != 1 || other[0].Currency != "EUR" || !other[0].Amount.Equal(d(10)) {
		t.Fatalf("profile: want 30 USD and 10 EUR: %+v %v", p.Exposure, err)
	}
	listed, err := s.ListCreditProfiles(ctx, le, 50)
	if err != nil || len(listed) != 1 || !listed[0].Exposure.DeliveredNotInvoiced.Equal(d(30)) ||
		len(listed[0].Exposure.DeliveredNotInvoicedOtherCurrencies) != 1 || !listed[0].Exposure.DeliveredNotInvoicedOtherCurrencies[0].Amount.Equal(d(10)) {
		t.Fatalf("list: want 30 USD and 10 EUR: %+v %v", listed, err)
	}

	// A 20 USD order against a 20 USD limit with no tolerance: BALANCE ignores
	// the 30 delivered and passes at 20; BALANCE_PACKING_SLIP adds them,
	// 50 > 20, and fails, naming the EUR it could not count.
	cr1 := func(limitType string, version int) (domain.RuleResult, int) {
		t.Helper()
		put, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: domain.CreditProfile{
			Limit: d(20), Currency: "USD", LimitType: limitType, OldestItemDaysMax: 30, MaxDunningLevel: 3}}, version)
		if err != nil {
			t.Fatal(err)
		}
		o, _, err := s.CreateOrder(ctx, oneLineOrder(le, customer, ids.New(), time.Date(2027, 2, 20, 0, 0, 0, 0, time.UTC)))
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res.Evaluation.Rules {
			if r.ID == "CR-1" {
				return r, put.Version
			}
		}
		t.Fatalf("no CR-1: %+v", res.Evaluation)
		return domain.RuleResult{}, 0
	}
	r, v := cr1(domain.LimitBalance, p.Version)
	if r.Result != domain.RulePass || r.Value != "20" {
		t.Fatalf("BALANCE: %+v", r)
	}
	r, _ = cr1(domain.LimitBalancePackingSlip, v)
	if r.Result != domain.RuleFail || r.Value != "50" || !strings.Contains(r.Note, "10 EUR") {
		t.Fatalf("BALANCE_PACKING_SLIP: %+v", r)
	}
}
