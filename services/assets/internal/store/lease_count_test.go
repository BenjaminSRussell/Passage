package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// workedLease recognizes the PRD FR-37.10 lease: USD 10,000 a year in arrears
// for 5 years at 5% (liability 43,294.77).
func workedLease(t *testing.T, s *store.Postgres, ctx context.Context, le, number string) store.Lease {
	t.Helper()
	terms := domain.LeaseTerms{Payment: d("10000"), PaymentsPerYear: 1, TermMonths: 60, AnnualRate: d("0.05"), Currency: "USD"}
	sched, err := domain.ComputeLease(terms)
	if err != nil {
		t.Fatal(err)
	}
	lines := []store.LeaseLine{}
	for _, l := range sched.Lines {
		lines = append(lines, store.LeaseLine{PeriodNo: l.PeriodNo, Opening: l.Opening, Interest: l.Interest, Payment: l.Payment, Closing: l.Closing,
			ROUDepreciation: l.ROUDepreciation})
	}
	lease, err := s.CreateLease(ctx, store.Lease{LegalEntityID: le, LeaseNumber: number, CommencementDate: day("2026-01-01"), TermMonths: 60,
		PaymentsPerYear: 1, PaymentAmount: d("10000"), PaymentTiming: "arrears", IBRRate: d("0.05"), Currency: "USD",
		Liability: sched.Liability, ROUAsset: sched.ROUAsset, Schedule: lines}, "lease-"+number)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

// checkLeaseInvariants asserts the PRD 7.37.4 schedule invariants on a stored
// lease: closing(n) = opening(n+1), final closing 0.00, and Σ ROU depreciation
// = recognized ROU + Σ ROU adjustments of the modifications.
func checkLeaseInvariants(t *testing.T, l store.Lease) {
	t.Helper()
	dep := decimal.Zero
	for i, line := range l.Schedule {
		if line.PeriodNo != i+1 {
			t.Fatalf("period %d at position %d", line.PeriodNo, i)
		}
		dep = dep.Add(line.ROUDepreciation)
		if i+1 < len(l.Schedule) && !line.Closing.Equal(l.Schedule[i+1].Opening) {
			t.Fatalf("period %d closing %s != next opening %s", line.PeriodNo, line.Closing, l.Schedule[i+1].Opening)
		}
	}
	if last := l.Schedule[len(l.Schedule)-1]; !last.Closing.IsZero() {
		t.Fatalf("final closing = %s", last.Closing)
	}
	rou := l.ROUAsset
	for _, m := range l.Modifications {
		rou = rou.Add(m.ROUAdjustment)
	}
	if !dep.Equal(rou) {
		t.Fatalf("Σ ROU depreciation %s != ROU %s + adjustments = %s", dep, l.ROUAsset, rou)
	}
}

// TestStore_LeaseModificationBR37_8 remeasures the worked example after Y2:
// the schedule from Y3 is replaced, the ROU moves by the liability adjustment,
// the lease.remeasured event is written, the call is idempotent and stale
// versions are refused.
func TestStore_LeaseModificationBR37_8(t *testing.T) {
	s, pool, ctx := setup(t)
	lease := workedLease(t, s, ctx, ids.New(), "L-MOD-1")
	in := store.LeaseModificationInput{Version: lease.Version, EffectivePeriod: 3, PaymentAmount: d("12000.004")}
	res, err := s.ModifyLease(ctx, lease.ID, in, "mod-1")
	if err != nil {
		t.Fatal(err)
	}
	m := res.Modification
	if m.LiabilityBefore.StringFixed(2) != "27232.49" || m.LiabilityAfter.StringFixed(2) != "32678.98" || m.Adjustment.StringFixed(2) != "5446.49" {
		t.Fatalf("modification = %+v", m)
	}
	if m.ROUBefore.StringFixed(2) != "25976.87" || m.ROUAfter.StringFixed(2) != "31423.36" || !m.ROUAdjustment.Equal(m.Adjustment) || !m.ProfitLossExcess.IsZero() {
		t.Fatalf("ROU %s -> %s (%s), excess %s", m.ROUBefore, m.ROUAfter, m.ROUAdjustment, m.ProfitLossExcess)
	}
	if !m.PaymentAmount.Equal(d("12000")) || !m.IBRRate.Equal(d("0.05")) || m.TermMonths != 60 || m.ScopeChange {
		t.Fatalf("terms = payment %s rate %s term %d", m.PaymentAmount, m.IBRRate, m.TermMonths)
	}
	after := res.Lease
	if after.Status != "Modified" || after.Version != 2 || !after.PaymentAmount.Equal(d("12000")) || !after.Liability.Equal(lease.Liability) {
		t.Fatalf("lease after = status %s version %d payment %s liability %s", after.Status, after.Version, after.PaymentAmount, after.Liability)
	}
	if len(after.Schedule) != 5 || !after.Schedule[1].Interest.Equal(lease.Schedule[1].Interest) || after.Schedule[2].Remeasurement.StringFixed(2) != "5446.49" ||
		after.Schedule[2].Interest.StringFixed(2) != "1633.95" || len(after.Modifications) != 1 {
		t.Fatalf("schedule after = %+v", after.Schedule)
	}
	checkLeaseInvariants(t, after)
	if len(res.Messages) != 1 || !strings.HasPrefix(res.Messages[0], domain.CodeLeaseRemeasured+": lease remeasured; ROU adjusted by 5446.49 USD") {
		t.Fatalf("messages = %v", res.Messages)
	}

	replay, err := s.ModifyLease(ctx, lease.ID, in, "mod-1")
	if err != nil || replay.Modification.ID != m.ID || replay.Lease.Version != 2 {
		t.Fatalf("replay = %+v, %v", replay.Modification, err)
	}
	if _, err := s.ModifyLease(ctx, lease.ID, in, "mod-2"); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: err = %v", err)
	}
	other := in
	other.PaymentAmount = d("1")
	if _, err := s.ModifyLease(ctx, lease.ID, other, "mod-1"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: err = %v", err)
	}

	// A scope change extends the term to 7 years at a revised 6% from Y4.
	rate := d("0.06")
	ext, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: 2, EffectivePeriod: 4, PaymentAmount: d("12000"), TermMonths: 84,
		ScopeChange: true, IBRRate: &rate}, "mod-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(ext.Lease.Schedule) != 7 || ext.Lease.TermMonths != 84 || !ext.Lease.IBRRate.Equal(rate) || len(ext.Lease.Modifications) != 2 {
		t.Fatalf("extended lease = %d lines, term %d, rate %s", len(ext.Lease.Schedule), ext.Lease.TermMonths, ext.Lease.IBRRate)
	}
	checkLeaseInvariants(t, ext.Lease)

	// A payment cut below the ROU carrying amount sends the excess to P&L.
	cut, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: 3, EffectivePeriod: 5, PaymentAmount: d("100")}, "mod-4")
	if err != nil {
		t.Fatal(err)
	}
	if !cut.Modification.ROUAfter.IsZero() || !cut.Modification.ProfitLossExcess.IsPositive() ||
		!cut.Modification.ROUAdjustment.Equal(cut.Modification.ROUBefore.Neg()) || !strings.Contains(cut.Messages[0], "recognized in profit or loss") {
		t.Fatalf("cut = %+v, %v", cut.Modification, cut.Messages)
	}
	checkLeaseInvariants(t, cut.Lease)

	cases := map[string]struct {
		in   store.LeaseModificationInput
		code string
	}{
		"period beyond the schedule": {store.LeaseModificationInput{Version: 4, EffectivePeriod: 8, PaymentAmount: d("100")}, domain.CodeInvalidLease},
		"term shorter than paid":     {store.LeaseModificationInput{Version: 4, EffectivePeriod: 5, PaymentAmount: d("100"), TermMonths: 48, ScopeChange: true, IBRRate: &rate}, domain.CodeInvalidLease},
		"revised rate out of range":  {store.LeaseModificationInput{Version: 4, EffectivePeriod: 5, PaymentAmount: d("100"), ScopeChange: true, IBRRate: ptr(d("0.7"))}, domain.CodeIBRRange},
	}
	for name, tc := range cases {
		if _, err := s.ModifyLease(ctx, lease.ID, tc.in, "bad-"+name); problemCode(err) != tc.code {
			t.Errorf("%s: err = %v, want %s", name, err, tc.code)
		}
	}
	if _, err := s.ModifyLease(ctx, ids.New(), in, "mod-unknown"); problemCode(err) != store.CodeLeaseNotFound {
		t.Fatalf("unknown lease: err = %v", err)
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.lease.remeasured.v1"] != 3 {
		t.Fatalf("outbox = %v (three remeasurements, the replay adds none)", types)
	}
}

func ptr[T any](v T) *T { return &v }

// TestStore_LeaseRateMethodAndPostedGuard stores the effective-rate option
// and refuses to remeasure a period that is already posted to the ledger.
func TestStore_LeaseRateMethodAndPostedGuard(t *testing.T) {
	s, pool, ctx := setup(t)
	lease := workedLease(t, s, ctx, ids.New(), "L-RATE-1")
	if lease.RateMethod != domain.RateNominal {
		t.Fatalf("default rate method = %q", lease.RateMethod)
	}
	terms := domain.LeaseTerms{Payment: d("1000"), PaymentsPerYear: 12, TermMonths: 12, AnnualRate: d("0.05"), Currency: "USD", RateMethod: domain.RateEffective}
	sched, err := domain.ComputeLease(terms)
	if err != nil {
		t.Fatal(err)
	}
	eff, err := s.CreateLease(ctx, store.Lease{LegalEntityID: lease.LegalEntityID, LeaseNumber: "L-RATE-2", CommencementDate: day("2026-01-01"),
		TermMonths: 12, PaymentsPerYear: 12, PaymentAmount: d("1000"), PaymentTiming: "arrears", IBRRate: d("0.05"), RateMethod: domain.RateEffective,
		Currency: "USD", Liability: sched.Liability, ROUAsset: sched.ROUAsset}, "lease-eff")
	if err != nil || eff.RateMethod != domain.RateEffective {
		t.Fatalf("effective lease = %+v, %v", eff, err)
	}
	// Stand in for the lease-schedule-post job (handoff): period 3 is posted.
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update lease_schedule_line set posted_journal_id = $1 where lease_id = $2 and period_no = 3`, ids.New(), lease.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: 1, EffectivePeriod: 3, PaymentAmount: d("9000")}, "mod-posted"); problemCode(err) != store.CodeLeasePeriodPosted {
		t.Fatalf("modifying a posted period: err = %v", err)
	}
	if _, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: 1, EffectivePeriod: 4, PaymentAmount: d("9000")}, "mod-after-posted"); err != nil {
		t.Fatalf("modifying after the posted period: %v", err)
	}
}

// TestStore_CountSessionFR37_11 reconciles a scan against the Open, barcoded
// assets of one legal entity: found, missing, unknown (AST-0525) and repeated
// scans; other entities and disposed assets are not expected.
func TestStore_CountSessionFR37_11(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	open := func(number, barcode string) store.Asset {
		a, err := s.CreateAsset(ctx, store.Asset{LegalEntityID: le, AssetNumber: number, Name: "Asset " + number, ClassCode: "IT", Barcode: &barcode, Quantity: d("1")}, "a-"+number)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddBook(ctx, a.ID, book("IFRS", "1000"), "b-"+number); err != nil {
			t.Fatal(err)
		}
		return a
	}
	laptop, printer := open("FA-C1", "BC-1"), open("FA-C2", "BC-2")
	sold := open("FA-C3", "BC-3")
	current, err := s.GetAsset(ctx, sold.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispose(ctx, sold.ID, store.DisposalInput{Version: current.Version, Kind: domain.DisposalScrap, Percentage: d("100"), Date: day("2026-03-01")}, "scrap-c3"); err != nil {
		t.Fatal(err)
	}
	notAcquired := "BC-4"
	if _, err := s.CreateAsset(ctx, store.Asset{LegalEntityID: le, AssetNumber: "FA-C4", Name: "Not yet acquired", ClassCode: "IT", Barcode: &notAcquired, Quantity: d("1")}, "a-c4"); err != nil {
		t.Fatal(err)
	}
	elsewhere := "BC-9"
	if _, err := s.CreateAsset(ctx, store.Asset{LegalEntityID: ids.New(), AssetNumber: "FA-C9", Name: "Other entity", ClassCode: "IT", Barcode: &elsewhere, Quantity: d("1")}, "a-c9"); err != nil {
		t.Fatal(err)
	}

	in := store.CountSessionInput{LegalEntityID: le, Location: "Warehouse 1", CountedDate: day("2026-04-10"), Barcodes: []string{"BC-1", "BC-9", "BC-1", "XX-1"}}
	session, err := s.CreateCountSession(ctx, in, "count-1")
	if err != nil {
		t.Fatal(err)
	}
	if session.ExpectedCount != 2 || session.FoundCount != 1 || session.MissingCount != 1 || session.UnknownCount != 2 || session.DuplicateCount != 1 ||
		session.CountedDate.String() != "2026-04-10" {
		t.Fatalf("session = %+v (expected: the two Open barcoded assets of the entity)", session)
	}
	if len(session.Lines) != 4 || session.Lines[0].Outcome != "Missing" || *session.Lines[0].AssetID != printer.ID || *session.Lines[0].AssetNumber != "FA-C2" ||
		session.Lines[1].Outcome != "Unknown" || session.Lines[1].Barcode != "BC-9" || session.Lines[1].AssetID != nil ||
		session.Lines[3].Outcome != "Found" || *session.Lines[3].AssetID != laptop.ID {
		t.Fatalf("lines = %+v", session.Lines)
	}
	if len(session.Warnings) != 2 || !strings.HasPrefix(session.Warnings[0], domain.CodeCountUnknown+": barcode BC-9") {
		t.Fatalf("warnings = %v", session.Warnings)
	}
	if replay, err := s.CreateCountSession(ctx, in, "count-1"); err != nil || replay.ID != session.ID {
		t.Fatalf("replay = %s, %v", replay.ID, err)
	}
	empty, err := s.CreateCountSession(ctx, store.CountSessionInput{LegalEntityID: le, Location: "Office", CountedDate: day("2026-04-11"), Barcodes: []string{}}, "count-2")
	if err != nil || empty.MissingCount != 2 || empty.FoundCount != 0 || len(empty.Lines) != 2 {
		t.Fatalf("empty scan = %+v, %v", empty, err)
	}
	if a, err := s.GetAsset(ctx, printer.ID); err != nil || a.Status != "Open" {
		t.Fatalf("a missing asset stays Open in the register: %+v, %v", a.Asset, err)
	}

	page, err := s.ListCountSessions(ctx, store.CountFilter{LegalEntityID: le, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != empty.ID || page.NextCursor == "" || page.Items[0].Lines != nil {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := s.ListCountSessions(ctx, store.CountFilter{LegalEntityID: le, After: page.NextCursor, Limit: 1})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != session.ID || next.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", next, err)
	}
	if got, err := s.GetCountSession(ctx, session.ID); err != nil || len(got.Lines) != 4 || len(got.Warnings) != 2 {
		t.Fatalf("read back = %+v, %v", got, err)
	}
	if _, err := s.GetCountSession(ctx, ids.New()); problemCode(err) != store.CodeCountSessionNotFound {
		t.Fatalf("unknown session: err = %v", err)
	}
}

// TestStore_InsuranceReportAC37_9 lists the policies in force with the AC-37.9
// indexed value, the under-insured flag, NBV per Open book and the filters.
func TestStore_InsuranceReportAC37_9(t *testing.T) {
	s, _, ctx := setup(t)
	if _, err := s.UpsertIndexSeries(ctx, store.IndexSeries{Code: "US-MACH", Country: "US", ClassCode: "MACHINE",
		Values: []store.IndexPoint{{Year: 2024, Value: d("112.0")}, {Year: 2026, Value: d("118.5")}}}); err != nil {
		t.Fatal(err)
	}
	le := ids.New()
	press := newAsset(t, s, ctx, le, "FA-R1")
	if _, err := s.AddBook(ctx, press.ID, book("IFRS", "100000"), "r1-ifrs"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBook(ctx, press.ID, book("TAX", "90000"), "r1-tax"); err != nil {
		t.Fatal(err)
	}
	van := newAsset(t, s, ctx, le, "FA-R2")
	other := newAsset(t, s, ctx, ids.New(), "FA-R9")
	code, base, y2026 := "US-MACH", 2024, 2026
	policy := func(number, typ, from, to, sum string) store.InsurancePolicy {
		return store.InsurancePolicy{InsurerPartyID: ids.New(), PolicyNumber: number, InsuranceType: typ, CoverageFrom: day(from), CoverageTo: day(to),
			Currency: "USD", Basis: "Replacement", BaseValue: d("100000"), SumInsured: d(sum), IndexSeriesCode: &code, IndexBaseYear: &base, ValuationYear: &y2026}
	}
	add := func(asset store.Asset, p store.InsurancePolicy) {
		if _, err := s.AddInsurancePolicy(ctx, asset.ID, p, "rp-"+asset.AssetNumber+p.PolicyNumber); err != nil {
			t.Fatal(err)
		}
	}
	add(press, policy("P-UNDER", "Property", "2026-01-01", "2026-12-31", "95223.20"))
	add(press, policy("P-OK", "MachineryBreakdown", "2026-01-01", "2026-05-15", "95223.21"))
	add(press, policy("P-EXPIRED", "Property", "2025-01-01", "2025-12-31", "1"))
	add(van, policy("P-VAN", "Fleet", "2026-04-01", "2027-03-31", "200000"))
	add(other, policy("P-OTHER", "Property", "2026-01-01", "2026-12-31", "1"))

	report, err := s.InsuranceReport(ctx, store.InsuranceReportFilter{LegalEntityID: le, AsOf: day("2026-04-20"), Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	numbers := []string{}
	for _, r := range report.Items {
		numbers = append(numbers, r.AssetNumber+"/"+r.PolicyNumber)
	}
	if strings.Join(numbers, ",") != "FA-R1/P-OK,FA-R1/P-UNDER,FA-R2/P-VAN" {
		t.Fatalf("rows = %v (in force on the date, this entity only)", numbers)
	}
	ok, under := report.Items[0], report.Items[1]
	if ok.UnderInsured || !under.UnderInsured || under.IndexedValue.StringFixed(2) != "105803.57" || !strings.HasPrefix(under.Warnings[0], domain.CodeUnderInsured) {
		t.Fatalf("under-insured flags: P-OK %v, P-UNDER %v %s", ok.UnderInsured, under.UnderInsured, under.IndexedValue)
	}
	if len(under.Books) != 2 || under.Books[0].BookCode != "IFRS" || !under.Books[0].NBV.Equal(d("100000")) || under.Books[1].BookCode != "TAX" ||
		len(report.Items[2].Books) != 0 || report.Items[2].AssetStatus != "NotYetAcquired" {
		t.Fatalf("books = %+v / %+v", under.Books, report.Items[2])
	}
	if !ok.ExpiringSoon || under.ExpiringSoon {
		t.Fatalf("expiring soon: P-OK (ends 2026-05-15) %v, P-UNDER %v", ok.ExpiringSoon, under.ExpiringSoon)
	}

	within := 30
	soon, err := s.InsuranceReport(ctx, store.InsuranceReportFilter{LegalEntityID: le, AsOf: day("2026-04-20"), ExpiringWithinDays: &within, Limit: 50})
	if err != nil || len(soon.Items) != 1 || soon.Items[0].PolicyNumber != "P-OK" {
		t.Fatalf("expiring within 30 days = %+v, %v", soon.Items, err)
	}
	fleet, err := s.InsuranceReport(ctx, store.InsuranceReportFilter{LegalEntityID: le, AsOf: day("2026-04-20"), InsuranceType: "Fleet", Limit: 50})
	if err != nil || len(fleet.Items) != 1 || fleet.Items[0].PolicyNumber != "P-VAN" {
		t.Fatalf("fleet = %+v, %v", fleet.Items, err)
	}
	first, err := s.InsuranceReport(ctx, store.InsuranceReportFilter{LegalEntityID: le, AsOf: day("2026-04-20"), Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor != first.Items[1].ID {
		t.Fatalf("first page = %d rows, cursor %q, %v", len(first.Items), first.NextCursor, err)
	}
	rest, err := s.InsuranceReport(ctx, store.InsuranceReportFilter{LegalEntityID: le, AsOf: day("2026-04-20"), After: first.NextCursor, Limit: 2})
	if err != nil || len(rest.Items) != 1 || rest.Items[0].PolicyNumber != "P-VAN" || rest.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", rest.Items, err)
	}
}
