package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// quoteValidUntil is a one-line 100 USD quotation for the customer at the
// given probability and validity.
func quoteValidUntil(le, customer, key, currency string, day time.Time, pct int) NewQuotation {
	in := threeLineQuotation(le, customer, key)
	in.Lines = in.Lines[:1]
	in.Lines[0].Quantity, in.Lines[0].UnitPrice, in.Lines[0].LineNet = d(10), d(10), d(100)
	in.Currency, in.ValidUntil, in.ProbabilityPct = currency, day, &pct
	return in
}

// TestPostgres_ExpireQuotations: a lapsed DRAFT and SENT quotation become
// EXPIRED with a new version and one quotation.expired each; a valid one, a
// converted one and a repeat of the run are untouched.
func TestPostgres_ExpireQuotations(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	day := func(n int) time.Time { return time.Date(2027, 3, n, 0, 0, 0, 0, time.UTC) }
	lapsedDraft, _, err := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "e1", "USD", day(1), 50))
	if err != nil {
		t.Fatal(err)
	}
	lapsedSent, _, _ := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "e2", "USD", day(2), 50))
	if _, err := s.SendQuotation(ctx, lapsedSent.ID, 1, day(2)); err != nil {
		t.Fatal(err)
	}
	validQuote, _, _ := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "e3", "USD", day(10), 50))
	converted, _, _ := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "e4", "USD", day(2), 50))
	if _, err := s.SendQuotation(ctx, converted.ID, 1, day(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConvertQuotation(ctx, converted.ID, ConvertRequest{Version: 2, Actor: "sales-rep"}, day(2)); err != nil {
		t.Fatal(err)
	}

	// On 2027-03-02 a quotation valid through 03-02 is still valid.
	res, err := s.ExpireQuotations(ctx, "", day(2), "job")
	if err != nil || res.Count != 1 || res.Expired[0].ID != lapsedDraft.ID || res.Expired[0].PreviousStatus != domain.QuoteDraft {
		t.Fatalf("run on day 2: %+v err %v", res, err)
	}
	res, err = s.ExpireQuotations(ctx, "", day(3), "job")
	if err != nil || res.Count != 1 || res.Expired[0].ID != lapsedSent.ID || res.Expired[0].Version != 3 || res.More {
		t.Fatalf("run on day 3: %+v err %v", res, err)
	}
	if res, _ := s.ExpireQuotations(ctx, "", day(3), "job"); res.Count != 0 || len(res.Expired) != 0 {
		t.Fatalf("a repeat must expire nothing: %+v", res)
	}
	if res, _ := s.ExpireQuotations(ctx, ids.New(), day(30), "job"); res.Count != 0 {
		t.Fatalf("another legal entity's filter must match nothing: %+v", res)
	}
	got, _ := s.GetQuotation(ctx, lapsedSent.ID)
	if got.Status != domain.QuoteExpired || got.Version != 3 {
		t.Fatalf("sent quotation: %+v", got)
	}
	if got, _ := s.GetQuotation(ctx, validQuote.ID); got.Status != domain.QuoteDraft || got.Version != 1 {
		t.Fatalf("a valid quotation must not change: %+v", got)
	}
	if got, _ := s.GetQuotation(ctx, converted.ID); got.Status != domain.QuoteConverted {
		t.Fatalf("a converted quotation must not change: %+v", got)
	}
	var evt salesevents.QuotationExpiredData
	if err := json.Unmarshal(outboxData(t, pool, lapsedSent.ID, salesevents.QuotationExpired), &evt); err != nil {
		t.Fatal(err)
	}
	if evt.PreviousStatus != "SENT" || evt.Version != 3 || evt.Actor != "job" || evt.NetTotal != "100.0000" || evt.Currency != "USD" || evt.ValidUntil != "2027-03-02" {
		t.Fatalf("event %+v", evt)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where type = $1`, salesevents.QuotationExpired); n != 2 {
		t.Fatalf("%d expired events, want 2", n)
	}
	// A later send is refused: the quotation is EXPIRED.
	if _, err := s.SendQuotation(ctx, lapsedDraft.ID, 2, day(3)); problemCode(t, err) != CodeQuotationState {
		t.Fatalf("an expired quotation must not be sent: %v", err)
	}
}

// TestPostgres_SalesSummary: the pipeline is per currency and weighted by
// probability, lapsed and expiring quotations are counted apart, and the
// conversion rate is won over closed.
func TestPostgres_SalesSummary(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := func(n int) time.Time { return today.AddDate(0, 0, n) }
	if _, _, err := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "s1", "USD", day(3), 50)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "s2", "USD", day(20), 20)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "s3", "EUR", day(0), 100)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateQuotation(ctx, quoteValidUntil(ids.New(), customer, "s4", "USD", day(3), 100)); err != nil {
		t.Fatal(err)
	}
	// The edges of the 7-day horizon seen from day 2: the last valid day
	// day(8) is inside it, day(9) is outside.
	if _, _, err := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "s8", "USD", day(8), 0)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "s9", "USD", day(9), 0)); err != nil {
		t.Fatal(err)
	}
	// A lapsed one cannot be created (validity must not be past), so it is
	// created valid through day 1 and the summary is taken on day 2.
	if _, _, err := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "s5", "USD", day(1), 50)); err != nil {
		t.Fatal(err)
	}
	lost, _, _ := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "s6", "USD", day(5), 50))
	if _, err := s.LoseQuotation(ctx, lost.ID, 1, "price"); err != nil {
		t.Fatal(err)
	}
	won, _, _ := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "s7", "USD", day(5), 50))
	if _, err := s.SendQuotation(ctx, won.ID, 1, today); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConvertQuotation(ctx, won.ID, ConvertRequest{Version: 2, Actor: "rep"}, today); err != nil {
		t.Fatal(err)
	}

	sum, err := s.Summary(ctx, SummaryFilter{LegalEntityID: le, Today: day(2), WindowDays: 90})
	if err != nil {
		t.Fatal(err)
	}
	// On day 2: s1, s2, s8 and s9 are open and valid; s3 and s5 lapsed; s1
	// and s8 expire within 7 days.
	if sum.QuotationsLapsed != 2 || sum.QuotationsExpiring != 2 {
		t.Fatalf("lapsed %d expiring %d", sum.QuotationsLapsed, sum.QuotationsExpiring)
	}
	if len(sum.Pipeline) != 1 || sum.Pipeline[0].Currency != "USD" || sum.Pipeline[0].OpenCount != 4 ||
		!sum.Pipeline[0].OpenNet.Equal(d(400)) || !sum.Pipeline[0].WeightedNet.Equal(d(70)) {
		t.Fatalf("pipeline %+v", sum.Pipeline)
	}
	// The pipeline filters narrow the pipeline only.
	byProbability, _ := s.Summary(ctx, SummaryFilter{LegalEntityID: le, Today: day(2), WindowDays: 90, MinProbabilityPct: 30})
	if len(byProbability.Pipeline) != 1 || byProbability.Pipeline[0].OpenCount != 1 || !byProbability.Pipeline[0].WeightedNet.Equal(d(50)) ||
		byProbability.QuotationsExpiring != 2 || byProbability.Won != 1 {
		t.Fatalf("minProbabilityPct 30: %+v", byProbability)
	}
	if soon, _ := s.Summary(ctx, SummaryFilter{LegalEntityID: le, Today: day(2), WindowDays: 90, ExpiringWithinDays: 7}); len(soon.Pipeline) != 1 || soon.Pipeline[0].OpenCount != 2 {
		t.Fatalf("expiringWithinDays 7: %+v", soon.Pipeline)
	}
	if sent, _ := s.Summary(ctx, SummaryFilter{LegalEntityID: le, Today: day(2), WindowDays: 90, Status: domain.QuoteSent}); len(sent.Pipeline) != 0 {
		t.Fatalf("no open quotation was sent: %+v", sent.Pipeline)
	}
	if sum.Won != 1 || sum.Lost != 1 || sum.Expired != 0 || sum.ConversionRatePct == nil || sum.ConversionRatePct.String() != "50" {
		t.Fatalf("closed: %+v", sum)
	}
	if _, err := s.ExpireQuotations(ctx, le, day(2), "job"); err != nil {
		t.Fatal(err)
	}
	sum, _ = s.Summary(ctx, SummaryFilter{LegalEntityID: le, Today: day(2), WindowDays: 90})
	if sum.Expired != 2 || sum.QuotationsLapsed != 0 || sum.ConversionRatePct.String() != "25" {
		t.Fatalf("after the expiry run: %+v", sum)
	}
	all, _ := s.Summary(ctx, SummaryFilter{Today: day(2), WindowDays: 90})
	if all.Pipeline[0].OpenCount != 5 {
		t.Fatalf("an empty legal entity covers every legal entity: %+v", all.Pipeline)
	}
	empty, _ := s.Summary(ctx, SummaryFilter{LegalEntityID: ids.New(), Today: day(2), WindowDays: 90})
	if empty.ConversionRatePct != nil || len(empty.Pipeline) != 0 || empty.ActiveHolds != 0 {
		t.Fatalf("empty summary: %+v", empty)
	}
}

// TestPostgres_SummaryTilesAndScope: the hold and backorder tiles count the
// legal entity's ACTIVE holds and open backorder schedule lines, and neither
// the summary nor the expiry run reaches another legal entity or tenant.
func TestPostgres_SummaryTilesAndScope(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer, product := ids.New(), ids.New(), ids.New()
	o := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 45))
	if _, err := s.PromiseLine(ctx, o.ID, o.Version, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "planner", time.Now()); err != nil {
		t.Fatal(err)
	}
	held, _, err := s.CreateOrder(ctx, oneLineOrder(le, customer, product, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyHold(ctx, held.ID, held.Version, HoldRequest{HoldType: domain.HoldFraud, ReasonCode: "CARD-MISMATCH"}, "fraud-analyst"); err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC()
	quote, _, err := s.CreateQuotation(ctx, quoteValidUntil(le, customer, "t1", "USD", today, 50))
	if err != nil {
		t.Fatal(err)
	}
	later := today.AddDate(0, 0, 5)

	for name, c := range map[string]context.Context{
		"other legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")}),
		"other tenant":       tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: testkit.ScopeDuties("")}),
	} {
		sum, err := s.Summary(c, SummaryFilter{Today: later, WindowDays: 90})
		if err != nil || sum.ActiveHolds != 0 || sum.BackorderLines != 0 || sum.QuotationsLapsed != 0 || len(sum.Pipeline) != 0 {
			t.Fatalf("%s: summary %+v %v", name, sum, err)
		}
		if res, err := s.ExpireQuotations(c, "", later, "x"); err != nil || res.Count != 0 {
			t.Fatalf("%s: expiry %+v %v", name, res, err)
		}
	}
	if got, _ := s.GetQuotation(ctx, quote.ID); got.Status != domain.QuoteDraft {
		t.Fatalf("another scope's run must leave the quotation alone: %+v", got)
	}

	sum, err := s.Summary(ctx, SummaryFilter{LegalEntityID: le, Today: later, WindowDays: 90})
	if err != nil || sum.ActiveHolds != 1 || sum.BackorderLines != 1 || sum.QuotationsLapsed != 1 {
		t.Fatalf("own legal entity: %+v %v", sum, err)
	}
	own := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "x", Duties: testkit.ScopeDuties(le)})
	if res, err := s.ExpireQuotations(own, "", later, "x"); err != nil || res.Count != 1 || res.Expired[0].ID != quote.ID {
		t.Fatalf("own legal entity's run: %+v %v", res, err)
	}
}
