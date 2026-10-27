package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func absDay(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

func absDayPtr(t time.Time) *time.Time { return &t }

func requireDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("error = %v, want domain code %s", err, code)
	}
}

// TestCoveredDaysCountsEachDayOnce covers the day arithmetic under PeriodAccrual.
func TestCoveredDaysCountsEachDayOnce(t *testing.T) {
	march := []time.Time{absDay(time.March, 1), absDay(time.March, 31)}
	cases := []struct {
		name  string
		spans []DaySpan
		want  int
	}{
		{"no employment", nil, 0},
		{"hired mid-March, open", []DaySpan{{From: absDay(time.March, 16)}}, 16},
		{"ended on 10 March (inclusive)", []DaySpan{{From: absDay(time.March, 1), To: absDayPtr(absDay(time.March, 10))}}, 10},
		{"span entirely before the period", []DaySpan{{From: absDay(time.January, 1), To: absDayPtr(absDay(time.February, 28))}}, 0},
		{"two overlapping spans count once", []DaySpan{{From: absDay(time.March, 1)}, {From: absDay(time.March, 16)}}, 31},
		{"adjacent spans merge", []DaySpan{
			{From: absDay(time.March, 1), To: absDayPtr(absDay(time.March, 9))},
			{From: absDay(time.March, 10)},
		}, 31},
		{"gap between spans", []DaySpan{
			{From: absDay(time.March, 1), To: absDayPtr(absDay(time.March, 9))},
			{From: absDay(time.March, 12)},
		}, 9 + 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CoveredDays(march[0], march[1], c.spans); got != c.want {
				t.Fatalf("CoveredDays = %d, want %d", got, c.want)
			}
		})
	}
}

// TestAccrualWorkedExampleUS_PTO_15 is BR-42.3 and AC-42.3 end to end in
// memory: hire 2026-03-16, monthly 1.25 prorated by days, cap 22.50, a
// 4.00 request approved in the year, carry-over capped at 5.00, and the
// first 2027 accrual.
func TestAccrualWorkedExampleUS_PTO_15(t *testing.T) {
	rate := decimal.RequireFromString("1.25")
	limit := decimal.RequireFromString("22.50")
	hire := absDay(time.March, 16)
	var l Ledger

	for m := time.March; m <= time.December; m++ {
		from := absDay(m, 1)
		to := from.AddDate(0, 1, -1)
		amount := PeriodAccrual(PeriodInput{Rate: rate, Prorate: true, From: from, To: to, HireDate: hire,
			Employed: []DaySpan{{From: hire}}})
		l, _ = l.Accrue(amount, limit)
		if m == time.March && !amount.Equal(decimal.RequireFromString("0.65")) {
			t.Fatalf("March accrual = %s, want 0.65", amount)
		}
		if m == time.December {
			if !l.Current.Accrued.Equal(decimal.RequireFromString("11.90")) {
				t.Fatalf("accrued through December = %s, want 11.90", l.Current.Accrued)
			}
		}
	}

	l, err := l.Consume(decimal.RequireFromString("4.00"), BalanceFloor(rate, false))
	if err != nil {
		t.Fatalf("consume 4.00: %v", err)
	}
	if !l.Total().Equal(decimal.RequireFromString("7.90")) {
		t.Fatalf("balance at 2026-12-31 = %s, want 7.90", l.Total())
	}

	l, expired := l.YearEnd(decimal.RequireFromString("5.00"))
	if !l.Carry.Accrued.Equal(decimal.RequireFromString("5.00")) || !expired.Equal(decimal.RequireFromString("2.90")) {
		t.Fatalf("year end carry = %s expired = %s; want 5.00 and 2.90", l.Carry.Accrued, expired)
	}

	jan2027 := func(d int) time.Time { return time.Date(2027, time.January, d, 0, 0, 0, 0, time.UTC) }
	jan := PeriodAccrual(PeriodInput{Rate: rate, Prorate: true, From: jan2027(1), To: jan2027(31),
		HireDate: hire, Employed: []DaySpan{{From: hire}}})
	l, _ = l.Accrue(jan, limit)
	if !l.Current.Accrued.Equal(decimal.RequireFromString("1.25")) || !l.Total().Equal(decimal.RequireFromString("6.25")) {
		t.Fatalf("after January: current accrued %s total %s; want 1.25 and 6.25", l.Current.Accrued, l.Total())
	}
}

func TestPeriodAccrualRules(t *testing.T) {
	rate := decimal.RequireFromString("1.25")
	march := []time.Time{absDay(time.March, 1), absDay(time.March, 31)}
	cases := []struct {
		name string
		in   PeriodInput
		want string
	}{
		{"not employed in the period", PeriodInput{Rate: rate, Prorate: true, HireDate: absDay(time.June, 1),
			Employed: []DaySpan{{From: absDay(time.June, 1)}}}, "0"},
		{"no proration pays the full rate for a partial month", PeriodInput{Rate: rate, Prorate: false, HireDate: absDay(time.March, 16),
			Employed: []DaySpan{{From: absDay(time.March, 16)}}}, "1.25"},
		{"minimum service not yet reached", PeriodInput{Rate: rate, Prorate: true, HireDate: absDay(time.March, 16), MinServiceDays: 30,
			Employed: []DaySpan{{From: absDay(time.March, 16)}}}, "0"},
		{"minimum service reached at period end", PeriodInput{Rate: rate, Prorate: true, HireDate: absDay(time.January, 1), MinServiceDays: 30,
			Employed: []DaySpan{{From: absDay(time.January, 1)}}}, "1.25"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.From, c.in.To = march[0], march[1]
			if got := PeriodAccrual(c.in); !got.Equal(decimal.RequireFromString(c.want)) {
				t.Fatalf("PeriodAccrual = %s, want %s", got, c.want)
			}
		})
	}
}

func TestBalanceFloor(t *testing.T) {
	rate := decimal.RequireFromString("1.25")
	if got := BalanceFloor(rate, false); !got.IsZero() {
		t.Fatalf("no negatives: floor = %s, want 0", got)
	}
	if got := BalanceFloor(rate, true); !got.Equal(decimal.RequireFromString("-2.50")) {
		t.Fatalf("negatives allowed: floor = %s, want -2.50", got)
	}
}

// TestConsumeRefusesBelowFloor is AC-42.4: a 2.00 balance cannot cover 3.00
// when negatives are forbidden, and the refusal is HCM-ABS-002.
func TestConsumeRefusesBelowFloor(t *testing.T) {
	l := Ledger{Current: Bucket{Accrued: decimal.RequireFromString("2.00")}}
	_, err := l.Consume(decimal.RequireFromString("3.00"), BalanceFloor(decimal.RequireFromString("1.00"), false))
	requireDomainCode(t, err, "HCM-ABS-002")
}

func TestCheckOverlap(t *testing.T) {
	req := func(id, status string, from, to time.Time) RequestSpan {
		return RequestSpan{ID: id, Status: status, From: from, To: to}
	}
	from, to := absDay(time.March, 10), absDay(time.March, 12)
	cases := []struct {
		name     string
		existing []RequestSpan
		code     string
		warn     int
	}{
		{"nothing existing", nil, "", 0},
		{"touching the last day is an overlap", []RequestSpan{req("a", RequestApproved, absDay(time.March, 12), absDay(time.March, 14))}, "HCM-ABS-001", 0},
		{"day before is clear", []RequestSpan{req("a", RequestApproved, absDay(time.March, 1), absDay(time.March, 9))}, "", 0},
		{"submitted overlap warns only", []RequestSpan{req("b", RequestSubmitted, absDay(time.March, 11), absDay(time.March, 11))}, "", 1},
		{"rejected overlap is ignored", []RequestSpan{req("c", RequestRejected, from, to)}, "", 0},
		{"canceled overlap is ignored", []RequestSpan{req("d", RequestCanceled, from, to)}, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			warnings, err := CheckOverlap("", from, to, c.existing)
			if c.code == "" {
				if err != nil || len(warnings) != c.warn {
					t.Fatalf("err=%v warnings=%d, want none and %d", err, len(warnings), c.warn)
				}
				return
			}
			requireDomainCode(t, err, c.code)
		})
	}
}

func TestNextRequestStatus(t *testing.T) {
	cases := []struct {
		status, decision, next string
		changed                bool
		code                   string
	}{
		{RequestSubmitted, DecisionApprove, RequestApproved, true, ""},
		{RequestSubmitted, DecisionReject, RequestRejected, true, ""},
		{RequestApproved, DecisionApprove, RequestApproved, false, ""},
		{RequestRejected, DecisionReject, RequestRejected, false, ""},
		{RequestApproved, DecisionReject, "", false, "HCM-ABS-STATE"},
		{RequestRejected, DecisionApprove, "", false, "HCM-ABS-STATE"},
		{RequestCanceled, DecisionApprove, "", false, "HCM-ABS-STATE"},
	}
	for _, c := range cases {
		next, changed, err := NextRequestStatus(c.status, c.decision)
		if c.code != "" {
			requireDomainCode(t, err, c.code)
			continue
		}
		if err != nil || next != c.next || changed != c.changed {
			t.Errorf("%s + %s = (%s, %v, %v), want (%s, %v)", c.status, c.decision, next, changed, err, c.next, c.changed)
		}
	}
}

func TestCheckNotSelf(t *testing.T) {
	me := "user-1"
	if err := CheckNotSelf("user-1", &me); err == nil {
		t.Fatal("approver deciding own request was allowed")
	}
	if err := CheckNotSelf("user-2", &me); err != nil {
		t.Fatalf("other approver refused: %v", err)
	}
	if err := CheckNotSelf("", nil); err != nil {
		t.Fatalf("anonymous with no requester refused: %v", err)
	}
}

// TestPropCoveredDaysBounded: the covered count stays within the period, and a
// duplicated span list counts the same days as the list once.
func TestPropCoveredDaysBounded(t *testing.T) {
	base := absDay(time.January, 1)
	rapid.Check(t, func(t *rapid.T) {
		from := base.AddDate(0, 0, rapid.IntRange(0, 60).Draw(t, "from"))
		to := from.AddDate(0, 0, rapid.IntRange(0, 60).Draw(t, "len"))
		var spans []DaySpan
		for i, n := 0, rapid.IntRange(0, 5).Draw(t, "spans"); i < n; i++ {
			start := base.AddDate(0, 0, rapid.IntRange(0, 120).Draw(t, "start"))
			s := DaySpan{From: start}
			if rapid.Bool().Draw(t, "closed") {
				end := start.AddDate(0, 0, rapid.IntRange(0, 60).Draw(t, "span"))
				s.To = &end
			}
			spans = append(spans, s)
		}
		got := CoveredDays(from, to, spans)
		if got < 0 || got > InclusiveDays(from, to) {
			t.Fatalf("CoveredDays = %d outside [0, %d]", got, InclusiveDays(from, to))
		}
		if dup := CoveredDays(from, to, append(append([]DaySpan{}, spans...), spans...)); dup != got {
			t.Fatalf("duplicated spans changed the count: %d vs %d", dup, got)
		}
	})
}

// TestPropOverlapIsSymmetric: whether two inclusive day ranges overlap does not
// depend on which one is the candidate.
func TestPropOverlapIsSymmetric(t *testing.T) {
	base := absDay(time.January, 1)
	rapid.Check(t, func(t *rapid.T) {
		a0 := base.AddDate(0, 0, rapid.IntRange(0, 60).Draw(t, "a0"))
		a1 := a0.AddDate(0, 0, rapid.IntRange(0, 20).Draw(t, "a1"))
		b0 := base.AddDate(0, 0, rapid.IntRange(0, 60).Draw(t, "b0"))
		b1 := b0.AddDate(0, 0, rapid.IntRange(0, 20).Draw(t, "b1"))
		_, errAB := CheckOverlap("a", a0, a1, []RequestSpan{{ID: "b", Status: RequestApproved, From: b0, To: b1}})
		_, errBA := CheckOverlap("b", b0, b1, []RequestSpan{{ID: "a", Status: RequestApproved, From: a0, To: a1}})
		if (errAB == nil) != (errBA == nil) {
			t.Fatalf("overlap not symmetric for [%s,%s] and [%s,%s]", a0, a1, b0, b1)
		}
	})
}

// TestCheckOverlapSkipsTheRequestItself: an approval rechecks its own request
// against the others, so the request's own row never blocks it, while another
// approved request on the same days still does (BR-42.5 at approval time).
func TestCheckOverlapSkipsTheRequestItself(t *testing.T) {
	from, to := absDay(time.May, 4), absDay(time.May, 5)
	own := RequestSpan{ID: "self", Status: RequestSubmitted, From: from, To: to}
	if w, err := CheckOverlap("self", from, to, []RequestSpan{own}); err != nil || len(w) != 0 {
		t.Fatalf("own request: warnings=%v err=%v, want none", w, err)
	}
	other := RequestSpan{ID: "other", Status: RequestApproved, From: to, To: to}
	_, err := CheckOverlap("self", from, to, []RequestSpan{own, other})
	requireDomainCode(t, err, CodeOverlapApproved)
}

func TestCanCancel(t *testing.T) {
	if changed, err := CanCancel(RequestSubmitted); err != nil || !changed {
		t.Fatalf("submitted: changed=%v err=%v", changed, err)
	}
	if changed, err := CanCancel(RequestCanceled); err != nil || changed {
		t.Fatalf("replayed cancel must be a no-op: changed=%v err=%v", changed, err)
	}
	for _, s := range []string{RequestApproved, RequestRejected} {
		_, err := CanCancel(s)
		requireDomainCode(t, err, CodeRequestState)
	}
}

func TestPlanYearStart(t *testing.T) {
	cases := []struct {
		mmdd string
		on   time.Time
		want time.Time
	}{
		{"01-01", absDay(time.March, 16), absDay(time.January, 1)},
		{"01-01", absDay(time.January, 1), absDay(time.January, 1)},
		{"04-15", absDay(time.April, 14), time.Date(2025, time.April, 15, 0, 0, 0, 0, time.UTC)},
		{"04-15", absDay(time.April, 15), absDay(time.April, 15)},
		{"12-31", absDay(time.December, 30), time.Date(2025, time.December, 31, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := PlanYearStart(c.mmdd, c.on); !got.Equal(c.want) {
			t.Errorf("PlanYearStart(%s, %s) = %s, want %s", c.mmdd, c.on.Format("2006-01-02"), got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}
	for _, bad := range []string{"02-29", "04-31", "13-01", "1-01", "01-1", ""} {
		if _, _, ok := ParsePlanYearStart(bad); ok {
			t.Errorf("ParsePlanYearStart(%q) accepted", bad)
		}
	}
}

func TestCheckAccrualPeriod(t *testing.T) {
	cases := []struct {
		name, freq string
		from, to   time.Time
		ok         bool
	}{
		{"whole month", FrequencyMonthly, absDay(time.February, 1), absDay(time.February, 28), true},
		{"month cut short", FrequencyMonthly, absDay(time.February, 1), absDay(time.February, 27), false},
		{"month not starting on the 1st", FrequencyMonthly, absDay(time.February, 2), absDay(time.March, 1), false},
		{"two weeks", FrequencyBiweekly, absDay(time.March, 2), absDay(time.March, 15), true},
		{"fifteen days", FrequencyBiweekly, absDay(time.March, 2), absDay(time.March, 16), false},
		{"plan year", FrequencyAnnual, absDay(time.January, 1), absDay(time.December, 31), true},
		{"calendar year on an April plan", FrequencyAnnual, absDay(time.April, 1), time.Date(2027, time.March, 31, 0, 0, 0, 0, time.UTC), false},
		{"hours worked needs time", FrequencyPerHourWorked, absDay(time.March, 1), absDay(time.March, 31), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pys := "01-01"
			if c.name == "calendar year on an April plan" {
				pys = "04-15"
			}
			err := CheckAccrualPeriod(c.freq, pys, c.from, c.to)
			if c.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.ok {
				requireDomainCode(t, err, CodePeriod)
			}
		})
	}
}

// TestCloseYearUSPTO15 is the AC-42.3 year end: 7.90 left at 2026-12-31 with
// carry-over max 5.00 carries 5.00 and expires 2.90; the closed 2026 ledger
// then shows the 5.00 it handed on, and the carry-over expires in full if
// nothing uses it by the expiry date.
func TestCloseYearUSPTO15(t *testing.T) {
	d := decimal.RequireFromString
	year := Ledger{Current: Bucket{Accrued: d("11.90"), Used: d("4.00")}}
	closed, carried, expired := CloseYear(year, d("5.00"))
	if !carried.Equal(d("5.00")) || !expired.Equal(d("2.90")) {
		t.Fatalf("carried %s expired %s, want 5.00 and 2.90", carried, expired)
	}
	if !closed.Total().Equal(d("5.00")) || !closed.Current.Expired.Equal(d("2.90")) || !closed.Current.Accrued.Equal(d("11.90")) {
		t.Fatalf("closed ledger = %+v, want history kept and 2.90 expired", closed)
	}

	next := Ledger{Carry: Bucket{Accrued: carried}}
	next, _ = next.Accrue(d("1.25"), d("22.50"))
	if !next.Total().Equal(d("6.25")) {
		t.Fatalf("2027-01-31 total = %s, want 6.25", next.Total())
	}
	next, err := next.Consume(d("2.00"), decimal.Zero)
	if err != nil || !next.Carry.Used.Equal(d("2.00")) {
		t.Fatalf("consumption must take CARRY_OVER first: %+v err=%v", next, err)
	}
	after, gone := ExpireCarryOver(next)
	if !gone.Equal(d("3.00")) || !after.Carry.Balance().IsZero() || !after.Total().Equal(d("1.25")) {
		t.Fatalf("expiry: gone %s ledger %+v, want 3.00 gone and 1.25 left", gone, after)
	}
}

// TestCloseYearTakesExpiryFromCarryOverFirst: when last year's carry-over is
// still on the ledger at year end, the expired part comes out of it first.
func TestCloseYearTakesExpiryFromCarryOverFirst(t *testing.T) {
	d := decimal.RequireFromString
	l := Ledger{Carry: Bucket{Accrued: d("3.00")}, Current: Bucket{Accrued: d("6.00")}}
	closed, carried, expired := CloseYear(l, d("5.00"))
	if !carried.Equal(d("5.00")) || !expired.Equal(d("4.00")) {
		t.Fatalf("carried %s expired %s", carried, expired)
	}
	if !closed.Carry.Expired.Equal(d("3.00")) || !closed.Current.Expired.Equal(d("1.00")) {
		t.Fatalf("expiry split = carry %s current %s, want 3.00 and 1.00", closed.Carry.Expired, closed.Current.Expired)
	}
	neg := Ledger{Current: Bucket{Accrued: d("1.00"), Used: d("2.50")}}
	closed, carried, expired = CloseYear(neg, d("5.00"))
	if !carried.Equal(d("-1.50")) || !expired.IsZero() || !closed.Total().Equal(d("-1.50")) {
		t.Fatalf("negative year end: carried %s expired %s", carried, expired)
	}
	if _, gone := ExpireCarryOver(Ledger{Carry: Bucket{Accrued: d("-1.50")}}); !gone.IsZero() {
		t.Fatalf("a negative carry-over must not expire, got %s", gone)
	}
}

// TestPropCloseYearConserves: whatever the ledger, the year end splits the
// total exactly into carried and expired, never carries more than the cap, and
// leaves the closed ledger's balance equal to what was carried.
func TestPropCloseYearConserves(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		amt := func(label string) decimal.Decimal {
			return decimal.New(int64(rapid.IntRange(0, 4000).Draw(t, label)), -2)
		}
		l := Ledger{
			Carry:   Bucket{Accrued: amt("ca"), Used: amt("cu"), Expired: amt("ce")},
			Current: Bucket{Accrued: amt("na"), Used: amt("nu"), Adjusted: amt("nj")},
		}
		limit := amt("max")
		closed, carried, expired := CloseYear(l, limit)
		if !carried.Add(expired).Equal(l.Total()) {
			t.Fatalf("carried %s + expired %s != total %s", carried, expired, l.Total())
		}
		if carried.GreaterThan(limit) || expired.IsNegative() {
			t.Fatalf("carried %s over cap %s or negative expiry %s", carried, limit, expired)
		}
		if !closed.Total().Equal(carried) {
			t.Fatalf("closed total %s != carried %s", closed.Total(), carried)
		}
	})
}

func TestMaxRequestQuantity(t *testing.T) {
	from, to := absDay(time.May, 4), absDay(time.May, 6)
	if got := MaxRequestQuantity("DAYS", from, to); !got.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("days = %s, want 3", got)
	}
	if got := MaxRequestQuantity("HOURS", from, from); !got.Equal(decimal.NewFromInt(24)) {
		t.Fatalf("hours = %s, want 24", got)
	}
}
