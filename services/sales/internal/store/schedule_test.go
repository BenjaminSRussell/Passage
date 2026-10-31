package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func day(s string) time.Time { v, _ := time.Parse(dayLayout, s); return v }

func atpBuckets() []domain.Period {
	return []domain.Period{
		{Date: day("2027-03-01"), Supply: d(40)},
		{Date: day("2027-03-04"), Supply: d(5)},
	}
}

// TestPostgres_ScheduleLinesAndBackorders covers FR-27.6 and AC-27.8: confirm
// writes one schedule line per line, a promise of 45 against ATP 40 on 03-01
// and 5 on 03-04 stores two schedule lines and lists the 5 on 03-04 as
// backorder, a change that keeps quantity and date keeps the split, a change
// of quantity resets it to one line, and cancel cancels it.
func TestPostgres_ScheduleLinesAndBackorders(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, product := ids.New(), ids.New(), ids.New()
	o := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 45))

	lines, err := s.ListScheduleLines(ctx, o.ID)
	if err != nil || len(lines) != 1 || lines[0].Status != domain.SchedConfirmed || !lines[0].Quantity.Equal(d(45)) ||
		lines[0].DeliveryDate == nil || *lines[0].DeliveryDate != "2027-03-01" || lines[0].Source != domain.SchedSourceLine || lines[0].BucketDate != nil {
		t.Fatalf("confirm schedule: %v %+v", err, lines)
	}

	// Refused: stale version, unknown line, DRAFT order.
	if _, err := s.PromiseLine(ctx, o.ID, o.Version-1, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "planner", time.Now()); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.PromiseLine(ctx, o.ID, o.Version, PromiseRequest{LineNo: 9, Periods: atpBuckets()}, "planner", time.Now()); problemCode(t, err) != CodeLineNotFound {
		t.Fatalf("unknown line: %v", err)
	}
	draft, _, err := s.CreateOrder(ctx, orderOf(le, customer, product, "2027-03-01", 5))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromiseLine(ctx, draft.ID, draft.Version, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "planner", time.Now()); problemCode(t, err) != CodeNotPromisable {
		t.Fatalf("draft order: %v", err)
	}
	journalIn := orderOf(le, customer, product, "2027-03-01", 5)
	journalIn.OrderType = domain.OrderTypeJournal
	journal := confirmed(t, s, ctx, journalIn)
	if _, err := s.PromiseLine(ctx, journal.ID, journal.Version, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "planner", time.Now()); problemCode(t, err) != CodeNotPromisable {
		t.Fatalf("JOURNAL order never consumes ATP (BR-27.7): %v", err)
	}

	res, err := s.PromiseLine(ctx, o.ID, o.Version, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "planner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Order.Version != o.Version+1 || len(res.Schedule) != 2 ||
		res.Schedule[0].Status != domain.SchedConfirmed || !res.Schedule[0].Quantity.Equal(d(40)) ||
		res.Schedule[1].Status != domain.SchedBackorder || !res.Schedule[1].Quantity.Equal(d(5)) || *res.Schedule[1].DeliveryDate != "2027-03-04" ||
		res.Schedule[1].Source != domain.SchedSourceATP || res.Schedule[1].BucketDate == nil || *res.Schedule[1].BucketDate != "2027-03-04" {
		t.Fatalf("promise: %+v", res)
	}
	if got := *res.Order.Lines[0].ConfirmedShipDate; got != "2027-03-01" {
		t.Fatalf("confirmed date %s", got)
	}
	var sum decimal.Decimal
	for _, l := range res.Schedule {
		sum = sum.Add(l.Quantity)
	}
	if !sum.Equal(d(45)) {
		t.Fatalf("schedule adds up to %s", sum)
	}
	back, err := s.ListBackorders(ctx, BackorderFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(back) != 1 || !back[0].Quantity.Equal(d(5)) || *back[0].PromisedDate != "2027-03-04" || *back[0].DaysLate != 3 || back[0].OrderNumber != o.OrderNumber {
		t.Fatalf("backorders: %v %+v", err, back)
	}
	if got, _ := s.ListBackorders(ctx, BackorderFilter{LegalEntityID: le, ProductID: ids.New(), Limit: 50}); len(got) != 0 {
		t.Fatalf("other product: %+v", got)
	}
	if got, _ := s.ListBackorders(ctx, BackorderFilter{LegalEntityID: le, PromisedTo: "2027-03-02", Limit: 50}); len(got) != 0 {
		t.Fatalf("promisedTo before the backorder: %+v", got)
	}

	var data salesevents.ATPPromisedData
	if err := json.Unmarshal(outboxData(t, pool, o.ID, salesevents.ATPPromised), &data); err != nil {
		t.Fatal(err)
	}
	if data.LineNo != 1 || data.Complete != true || data.BackorderQty != "5" || len(data.Schedule) != 2 || data.Version != o.Version+1 ||
		data.RequestedShipDate != "2027-03-01" || data.PromisedDate != "2027-03-01" || data.Source != domain.SourceATP ||
		data.Schedule[1].BucketDate != "2027-03-04" || data.SiteID != o.Lines[0].SiteID {
		t.Fatalf("atp.promised %+v", data)
	}
	var ver int
	if err := pool.QueryRow(ctx, `select max(version) from sales_order_version where order_id = $1`, o.ID).Scan(&ver); err != nil || ver != o.Version+1 {
		t.Fatalf("version row: %d %v", ver, err)
	}

	// A price change keeps the split; a quantity change resets to one line.
	price := decimal.NewFromInt(11)
	changed, err := s.ChangeOrder(ctx, o.ID, res.Order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, UnitPrice: &price}}}, "rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if lines, _ = s.ListScheduleLines(ctx, o.ID); len(lines) != 2 {
		t.Fatalf("price change must keep the split: %+v", lines)
	}
	qty := d(50)
	changed, err = s.ChangeOrder(ctx, o.ID, changed.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: &qty}}}, "rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lines, _ = s.ListScheduleLines(ctx, o.ID)
	if len(lines) != 1 || !lines[0].Quantity.Equal(d(50)) || lines[0].Status != domain.SchedConfirmed {
		t.Fatalf("quantity change resets the schedule: %+v", lines)
	}
	if back, _ = s.ListBackorders(ctx, BackorderFilter{LegalEntityID: le, Limit: 50}); len(back) != 0 {
		t.Fatalf("backorder list after reset: %+v", back)
	}
	if lines[0].Source != domain.SchedSourceLine {
		t.Fatalf("a reset schedule copies the line's date: %+v", lines[0])
	}
	if n := countWhere(t, pool, `select count(*) from order_schedule_line where order_id = $1 and status = 'SUPERSEDED'`, o.ID); n != 3 {
		t.Fatalf("superseded lines kept as history: %d", n)
	}

	// Supply for 30 only: 30 confirmed, 20 unpromised and undated; complete-only backorders all.
	res, err = s.PromiseLine(ctx, o.ID, changed.Version, PromiseRequest{LineNo: 1, Periods: []domain.Period{{Date: day("2027-03-01"), Supply: d(30)}}}, "planner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Schedule) != 2 || res.Schedule[1].Status != domain.SchedUnpromised || res.Schedule[1].DeliveryDate != nil || !res.Schedule[1].Quantity.Equal(d(20)) {
		t.Fatalf("unpromised remainder: %+v", res.Schedule)
	}
	back, _ = s.ListBackorders(ctx, BackorderFilter{LegalEntityID: le, Limit: 50})
	if len(back) != 1 || back[0].PromisedDate != nil || back[0].Status != domain.SchedUnpromised {
		t.Fatalf("undated backorder: %+v", back)
	}
	res, err = s.PromiseLine(ctx, o.ID, res.Order.Version, PromiseRequest{LineNo: 1, CompleteOnly: true, Periods: []domain.Period{{Date: day("2027-03-01"), Supply: d(30)}}}, "planner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Order.Lines[0].ConfirmedShipDate != nil || len(res.Schedule) != 1 || res.Schedule[0].Status != domain.SchedUnpromised {
		t.Fatalf("complete-only with no cover: %+v %+v", res.Order.Lines[0], res.Schedule)
	}

	cancelled, err := s.CancelOrder(ctx, o.ID, res.Order.Version, "customer left", "rep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if lines, _ = s.ListScheduleLines(ctx, cancelled.ID); len(lines) != 0 {
		t.Fatalf("cancel clears the schedule: %+v", lines)
	}
	if back, _ = s.ListBackorders(ctx, BackorderFilter{LegalEntityID: le, Limit: 50}); len(back) != 0 {
		t.Fatalf("cancelled order is not a backorder: %+v", back)
	}
	if _, err := s.ListScheduleLines(ctx, ids.New()); problemCode(t, err) != CodeOrderNotFound {
		t.Fatalf("unknown order: %v", err)
	}
}

// A line that drew a quota period is promised no earlier than that period.
func TestPostgres_PromiseHonoursAllocationPeriod(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, product := ids.New(), ids.New(), ids.New()
	if _, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-03-01", "2027-03-31", 10)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAllocationQuota(ctx, quotaIn(le, product, nil, "2027-04-01", "2027-04-30", 100)); err != nil {
		t.Fatal(err)
	}
	o := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-05", 20))
	if got := *o.Lines[0].ConfirmedShipDate; got != "2027-04-01" {
		t.Fatalf("allocation moved the line to %s", got)
	}
	lines, _ := s.ListScheduleLines(ctx, o.ID)
	if len(lines) != 1 || lines[0].Status != domain.SchedBackorder {
		t.Fatalf("a line held back by quota is a backorder: %+v", lines)
	}
	res, err := s.PromiseLine(ctx, o.ID, o.Version, PromiseRequest{LineNo: 1, Periods: []domain.Period{{Date: day("2027-03-01"), Supply: d(100)}}}, "planner", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := *res.Order.Lines[0].ConfirmedShipDate; got != "2027-04-01" {
		t.Fatalf("promise went before the quota period: %s", got)
	}
	if len(res.Schedule) != 1 || res.Schedule[0].Status != domain.SchedBackorder || *res.Schedule[0].BucketDate != "2027-03-01" {
		t.Fatalf("schedule held back by quota: %+v", res.Schedule)
	}
	var data salesevents.ATPPromisedData
	if err := json.Unmarshal(outboxData(t, pool, o.ID, salesevents.ATPPromised), &data); err != nil {
		t.Fatal(err)
	}
	if data.Source != "ALLOCATION" || data.PromisedDate != "2027-04-01" || data.RequestedShipDate != "2027-03-05" {
		t.Fatalf("atp.promised names the quota as the source: %+v", data)
	}
}

// Schedule lines and the backorder list are invisible to a caller scoped to
// another legal entity and to another tenant; the caller's own entity sees them.
func TestPostgres_ScheduleLegalEntityScope(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer, product := ids.New(), ids.New(), ids.New()
	o := confirmed(t, s, ctx, orderOf(le, customer, product, "2027-03-01", 45))
	if _, err := s.PromiseLine(ctx, o.ID, o.Version, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "planner", time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]context.Context{
		"other legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")}),
		"other tenant":       tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: testkit.ScopeDuties("")}),
	} {
		if _, err := s.ListScheduleLines(c, o.ID); problemCode(t, err) != CodeOrderNotFound {
			t.Fatalf("%s: schedule lines %v", name, err)
		}
		if back, err := s.ListBackorders(c, BackorderFilter{Limit: 50}); err != nil || len(back) != 0 {
			t.Fatalf("%s: backorders %v %+v", name, err, back)
		}
		if _, err := s.PromiseLine(c, o.ID, o.Version+1, PromiseRequest{LineNo: 1, Periods: atpBuckets()}, "x", time.Now()); problemCode(t, err) != CodeOrderNotFound {
			t.Fatalf("%s: promise %v", name, err)
		}
	}
	own := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "x", Duties: testkit.ScopeDuties(le)})
	if back, err := s.ListBackorders(own, BackorderFilter{Limit: 50}); err != nil || len(back) != 1 || back[0].ScheduleLineID == "" {
		t.Fatalf("own legal entity: %v %+v", err, back)
	}
}
