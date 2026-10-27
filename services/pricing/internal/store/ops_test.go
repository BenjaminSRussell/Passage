package store_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// BR-26.9 against Postgres: the expiry job moves only the ACTIVE records of the named legal
// entity whose validity ended before asOf, bumps their version, writes conditionrecord.withdrawn
// with reason EXPIRED, and a second run changes nothing.
func TestPostgres_ExpireRecords(t *testing.T) {
	pool := appPool(t)
	ctx := principal(7, "")
	s := store.New(pool)
	activate := func(e store.Entry) store.Entry {
		t.Helper()
		d, err := s.CreateRecord(ctx, e, "")
		if err != nil {
			t.Fatal(err)
		}
		a, err := s.ActivateRecord(ctx, d.ID, d.Version, "")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	ended := activate(entry("2026-01-01", "2026-03-19"))
	lastDay := entry("2026-01-01", "2026-03-20")
	lastDay.CustomerKey = "C2"
	stillValid := activate(lastDay)
	other := entry("2026-01-01", "2026-02-28")
	other.LegalEntityID = otherLegalEntity
	otherEntity := activate(other)
	draft, err := s.CreateRecord(ctx, func() store.Entry { e := entry("2026-01-01", "2026-01-31"); e.CustomerKey = "C3"; return e }(), "")
	if err != nil {
		t.Fatal(err)
	}
	before := len(outboxRows(t, ctx, pool))

	expired, err := s.ExpireRecords(ctx, legalEntity, day("2026-03-20"))
	if err != nil || len(expired) != 1 || expired[0].ID != ended.ID || expired[0].Status != domain.StatusExpired || expired[0].Version != ended.Version+1 {
		t.Fatalf("expired = %+v, %v; want only %s at v%d", expired, err, ended.ID, ended.Version+1)
	}
	for id, want := range map[string]string{stillValid.ID: domain.StatusActive, otherEntity.ID: domain.StatusActive, draft.ID: domain.StatusDraft} {
		if got, err := s.GetRecord(ctx, id); err != nil || got.Status != want {
			t.Fatalf("record %s = %s, %v; want %s", id, got.Status, err, want)
		}
	}
	rows := outboxRows(t, ctx, pool)
	if len(rows) != before+1 {
		t.Fatalf("outbox grew by %d; want one event", len(rows)-before)
	}
	last := rows[len(rows)-1]
	if last.Type != events.ConditionRecordWithdrawn || last.Data["id"] != ended.ID || last.Data["reason"] != store.ExpiryReason || last.Data["version"] != float64(ended.Version+1) {
		t.Fatalf("event = %+v", last)
	}

	if again, err := s.ExpireRecords(ctx, legalEntity, day("2026-03-20")); err != nil || len(again) != 0 {
		t.Fatalf("second run = %+v, %v; want nothing", again, err)
	}
	if len(outboxRows(t, ctx, pool)) != before+1 {
		t.Fatal("a second run must not write events")
	}
	// Another tenant sees nothing to expire.
	if theirs, err := s.ExpireRecords(principal(8, ""), otherLegalEntity, day("2026-12-31")); err != nil || len(theirs) != 0 {
		t.Fatalf("other tenant expired %+v, %v", theirs, err)
	}
}

// FR-26.18 against Postgres: the settlement totals per rep and currency cover invoices dated in
// the period (credit notes net off), leave out other periods and legal entities, and are
// ordered by rep.
func TestPostgres_SettlementTotals(t *testing.T) {
	pool := appPool(t)
	ctx := principal(9, "")
	s := store.New(pool)
	reps := []domain.Share{{RepID: "R1", SharePct: decimal.NewFromInt(60)}, {RepID: "R2", SharePct: decimal.NewFromInt(40)}}
	group := func(le string) store.SalesGroup {
		g, err := s.CreateSalesGroup(ctx, store.SalesGroup{LegalEntityID: le, Name: "SG-" + le[len(le)-1:], Reps: reps}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateCommissionRule(ctx, domain.CommissionRule{SalesGroupID: g.ID, Basis: domain.CommissionRevenue, RatePct: decimal.NewFromInt(5)}, ""); err != nil {
			t.Fatal(err)
		}
		return g
	}
	g, og := group(legalEntity), group(otherLegalEntity)
	post := func(le, groupID, line, reverses, date, net string) {
		t.Helper()
		l := commissionLine(line, reverses, net)
		l.InvoiceDate = day(date)
		if _, _, err := s.PostCommission(ctx, store.CommissionPosting{LegalEntityID: le, SalesGroupID: groupID, Line: l}); err != nil {
			t.Fatal(err)
		}
	}
	post(legalEntity, g.ID, "INV-1", "", "2027-03-01", "10000")     // R1 300, R2 200
	post(legalEntity, g.ID, "INV-2", "", "2027-03-31", "2000")      // R1 60, R2 40
	post(legalEntity, g.ID, "CN-1", "INV-1", "2027-03-15", "-4000") // R1 -120, R2 -80
	post(legalEntity, g.ID, "INV-3", "", "2027-04-01", "1000")      // next period
	post(otherLegalEntity, og.ID, "INV-9", "", "2027-03-10", "1000")

	rows, err := s.SettlementTotals(ctx, legalEntity, day("2027-03-01"), day("2027-03-31"))
	if err != nil || len(rows) != 2 {
		t.Fatalf("totals = %+v, %v; want two reps", rows, err)
	}
	if rows[0].RepID != "R1" || !rows[0].Amount.Equal(decimal.NewFromInt(240)) || rows[0].Transactions != 3 || rows[0].Currency != "USD" ||
		rows[1].RepID != "R2" || !rows[1].Amount.Equal(decimal.NewFromInt(160)) || rows[1].Transactions != 3 {
		t.Fatalf("totals = %+v; want R1 240.00 and R2 160.00 over three transactions", rows)
	}
	if empty, err := s.SettlementTotals(ctx, legalEntity, day("2027-05-01"), day("2027-05-31")); err != nil || len(empty) != 0 {
		t.Fatalf("empty period = %+v, %v", empty, err)
	}
	if theirs, err := s.SettlementTotals(principal(10, ""), legalEntity, day("2027-03-01"), day("2027-03-31")); err != nil || len(theirs) != 0 {
		t.Fatalf("other tenant read %+v, %v", theirs, err)
	}
}

// §5.26.6 against Postgres: the workspace tiles count exactly, per legal entity or across the
// caller's scope, and keep customer and vendor accruals apart.
func TestPostgres_WorkspaceSummary(t *testing.T) {
	pool := appPool(t)
	ctx := principal(11, "")
	s := store.New(pool)
	asOf := day("2027-03-20")
	mk := func(le, cust, from, to string, activate bool) {
		t.Helper()
		e := entry(from, to)
		e.LegalEntityID, e.CustomerKey = le, cust
		d, err := s.CreateRecord(ctx, e, "")
		if err != nil {
			t.Fatal(err)
		}
		if activate {
			if _, err := s.ActivateRecord(ctx, d.ID, d.Version, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk(legalEntity, "C1", "2027-01-01", "2027-04-19", true)      // expires in 30 days
	mk(legalEntity, "C2", "2027-01-01", "2027-04-20", true)      // day 31: not expiring
	mk(legalEntity, "C3", "2027-01-01", "2027-12-31", false)     // draft
	mk(otherLegalEntity, "C4", "2027-01-01", "2027-03-25", true) // expiring, other entity

	if _, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: "J", Lines: []domain.JournalLine{
		{Op: domain.OpCreate, Record: entry("2028-01-01", "2028-12-31").Record}}}, ""); err != nil {
		t.Fatal(err)
	}
	camp := testCampaign("")
	camp.ValidFrom, camp.ValidTo = day("2027-03-01"), day("2027-03-31")
	if _, err := s.CreateCampaign(ctx, camp, ""); err != nil {
		t.Fatal(err)
	}
	custAgreement := agreement(tier("0", "2"))
	custAgreement.ValidFrom, custAgreement.ValidTo = day("2027-01-01"), day("2027-12-31")
	cust, err := s.CreateAgreement(ctx, custAgreement, "")
	if err != nil {
		t.Fatal(err)
	}
	vend, err := s.CreateAgreement(ctx, vendorAgreement(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []store.AccrualInput{
		invoice(cust.ID, "I-1", "2027-03-05", "10000"), // 200.00
		invoice(cust.ID, "I-2", "2027-02-27", "5000"),  // February: not this month
		vendorInvoice(vend.ID, "V-1", "20000"),         // 600.00 on 2027-03-10
	} {
		if _, _, err := s.AccrueInvoiceLine(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateVendorClaim(ctx, store.VendorClaimInput{AgreementID: vend.ID, PeriodKey: "2027-03"}, ""); err != nil {
		t.Fatal(err)
	}

	got, err := s.WorkspaceSummary(ctx, legalEntity, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Month != "2027-03" || got.ActiveRecords != 2 || got.DraftRecords != 1 || got.ExpiringIn30Days != 1 || got.JournalsToPost != 1 ||
		got.LiveCampaigns != 1 || got.ClaimsAwaitingApproval != 1 {
		t.Fatalf("tiles = %+v", got)
	}
	if len(got.AccrualsThisMonth) != 2 ||
		got.AccrualsThisMonth[0].PartyRole != domain.PartyCustomer || !got.AccrualsThisMonth[0].Amount.Equal(decimal.NewFromInt(200)) ||
		got.AccrualsThisMonth[1].PartyRole != domain.PartyVendor || !got.AccrualsThisMonth[1].Amount.Equal(decimal.NewFromInt(600)) {
		t.Fatalf("accruals = %+v; want CUSTOMER 200 and VENDOR 600", got.AccrualsThisMonth)
	}
	all, err := s.WorkspaceSummary(ctx, "", asOf)
	if err != nil || all.ActiveRecords != 3 || all.ExpiringIn30Days != 2 {
		t.Fatalf("every entity = %+v, %v; want 3 active and 2 expiring", all, err)
	}
	scoped, err := s.WorkspaceSummary(principal(11, otherLegalEntity), "", asOf)
	if err != nil || scoped.ActiveRecords != 1 || scoped.DraftRecords != 0 || len(scoped.AccrualsThisMonth) != 0 {
		t.Fatalf("a caller scoped to the other entity = %+v, %v; want only its own record", scoped, err)
	}
	if theirs, err := s.WorkspaceSummary(principal(12, ""), "", asOf); err != nil || theirs.ActiveRecords != 0 || theirs.JournalsToPost != 0 {
		t.Fatalf("other tenant = %+v, %v", theirs, err)
	}
}
