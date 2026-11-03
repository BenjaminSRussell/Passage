package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

func leaseReq(le, ledgerID, through string) store.LeaseGLPostRequest {
	return store.LeaseGLPostRequest{LegalEntityID: le, LedgerID: ledgerID, PostThrough: day(through)}
}

func sum(ls []store.LeaseLine, f func(store.LeaseLine) decimal.Decimal) decimal.Decimal {
	t := decimal.Zero
	for _, l := range ls {
		t = t.Add(f(l))
	}
	return t
}

// The worked lease (43,294.77 at 5%, 10,000.00 a year in arrears from 2026-01-01) is booked in two steps; after the last period the liability is
// cleared, the ROU asset is fully depreciated and the ledger holds the interest, payments and depreciation of the schedule to the cent.
func TestLeaseGL_RecognitionAndPeriodsAreBookedInOrderAndClearTheLiability(t *testing.T) {
	s, pool, ctx := setup(t)
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	lease := workedLease(t, s, ctx, le, "L-GL-1")
	other := workedLease(t, s, ctx, ids.New(), "L-GL-OTHER")
	p := store.PlaceholderLeaseProfile

	res, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2026-12-31"))
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 2 || res.Booked[0].Period != 0 || res.Booked[1].Period != 1 || !res.Booked[0].Placeholder || res.More {
		t.Fatalf("first year: %+v %v", res, err)
	}
	first := lease.Schedule[0]
	wantLiab := lease.Liability.Add(first.Interest).Sub(first.Payment).Neg()
	if got := net(t, fake, ledgerID, p.Liability); !got.Equal(wantLiab) || !net(t, fake, ledgerID, p.ROUAsset).Equal(lease.ROUAsset) {
		t.Fatalf("liability %s want %s, ROU %s", got, wantLiab, net(t, fake, ledgerID, p.ROUAsset))
	}
	for _, e := range fake.Entries() {
		if e.SourceType != "asset-lease" || e.DocumentID != lease.ID {
			t.Fatalf("entry of another lease or source: %+v", e)
		}
	}
	if fake.Entries()[0].PostingDate != "2026-01-01" || fake.Entries()[1].PostingDate != "2026-12-31" {
		t.Fatalf("posting dates: %s %s", fake.Entries()[0].PostingDate, fake.Entries()[1].PostingDate)
	}

	if again, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2026-12-31")); err != nil || len(again.Booked) != 0 || len(fake.Entries()) != 2 {
		t.Fatalf("a second request books nothing new: %+v %v", again, err)
	}
	if early, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2027-12-30")); err != nil || len(early.Booked) != 0 {
		t.Fatalf("a period is booked only once it has ended: %+v %v", early, err)
	}

	rest, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2030-12-31"))
	if err != nil || len(rest.Failed) != 0 || len(rest.Booked) != 4 || rest.Booked[0].Period != 2 {
		t.Fatalf("remaining periods: %+v %v", rest, err)
	}
	interest := sum(lease.Schedule, func(l store.LeaseLine) decimal.Decimal { return l.Interest })
	checks := map[string]decimal.Decimal{
		p.Liability:           decimal.Zero,
		p.ROUAsset:            lease.ROUAsset,
		p.ROUAccumulated:      lease.ROUAsset.Neg(),
		p.InterestExpense:     interest,
		p.DepreciationExpense: lease.ROUAsset,
		p.PaymentClearing:     d("50000").Neg(),
	}
	for account, want := range checks {
		if got := net(t, fake, ledgerID, account); !got.Equal(want) {
			t.Errorf("account %s = %s, want %s", account, got, want)
		}
	}
	if !interest.Equal(d("50000").Sub(lease.Liability)) {
		t.Fatalf("interest %s", interest)
	}

	posted, err := s.GetLeasePostings(ctx, lease.ID)
	if err != nil || posted.RecognitionJournalID == nil || len(posted.Periods) != 5 || posted.Periods[0].PeriodEnd != "2026-12-31" || posted.Periods[4].PeriodEnd != "2030-12-31" {
		t.Fatalf("postings: %+v %v", posted, err)
	}
	for _, x := range posted.Periods {
		if x.PostedJournalID == nil {
			t.Fatalf("period %d not booked", x.PeriodNo)
		}
	}
	if o, err := s.GetLeasePostings(ctx, other.ID); err != nil || o.RecognitionJournalID != nil || o.Periods[0].PostedJournalID != nil {
		t.Fatalf("another legal entity's lease is not booked: %+v %v", o, err)
	}
	if _, err := s.GetLeasePostings(ctx, ids.New()); problemCode(err) != store.CodeLeaseNotFound {
		t.Fatalf("unknown lease: %v", err)
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.lease.posted.v1"] != 6 {
		t.Fatalf("outbox = %v (one lease.posted per journal)", types)
	}
	_, err = s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: lease.Version, EffectivePeriod: 3, PaymentAmount: d("12000")}, "mod-after-post")
	if problemCode(err) != store.CodeLeasePeriodPosted {
		t.Fatalf("a booked period cannot be remeasured: %v", err)
	}
}

// A modification takes effect in its first period: the journal of that period also moves the ROU asset against the liability.
func TestLeaseGL_ARemeasurementIsBookedWithItsPeriodAndTheProfitOrLossExcessGoesToTheGainAccount(t *testing.T) {
	s, _, ctx := setup(t)
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	up := workedLease(t, s, ctx, le, "L-GL-UP")
	mod, err := s.ModifyLease(ctx, up.ID, store.LeaseModificationInput{Version: up.Version, EffectivePeriod: 3, PaymentAmount: d("12000")}, "gl-mod-up")
	if err != nil {
		t.Fatal(err)
	}
	p := store.PlaceholderLeaseProfile
	res, err := s.PostLeasesToGL(ctx, fake, store.LeaseGLPostRequest{LegalEntityID: le, LedgerID: ledgerID, LeaseID: up.ID, PostThrough: day("2030-12-31")})
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 6 {
		t.Fatalf("modified lease: %+v %v", res, err)
	}
	rou := up.ROUAsset.Add(mod.Modification.ROUAdjustment)
	if !net(t, fake, ledgerID, p.Liability).IsZero() || !net(t, fake, ledgerID, p.ROUAsset).Equal(rou) || !net(t, fake, ledgerID, p.ROUAccumulated).Equal(rou.Neg()) {
		t.Fatalf("liability %s ROU %s accumulated %s, want 0, %s, %s", net(t, fake, ledgerID, p.Liability), net(t, fake, ledgerID, p.ROUAsset), net(t, fake, ledgerID, p.ROUAccumulated), rou, rou.Neg())
	}
	if !mod.Modification.Adjustment.Equal(d("5446.49")) {
		t.Fatalf("worked remeasurement %s", mod.Modification.Adjustment)
	}

	// A payment cut to 100.00 in the last period takes the liability below the ROU carrying amount: the excess is a gain.
	down := workedLease(t, s, ctx, le, "L-GL-DOWN")
	cut, err := s.ModifyLease(ctx, down.ID, store.LeaseModificationInput{Version: down.Version, EffectivePeriod: 5, PaymentAmount: d("100")}, "gl-mod-down")
	if err != nil || cut.Modification.ProfitLossExcess.StringFixed(2) != "769.61" {
		t.Fatalf("cut: %+v %v", cut.Modification, err)
	}
	ledger2, fake2 := ids.New(), ledgerclient.NewFake()
	if res, err = s.PostLeasesToGL(ctx, fake2, store.LeaseGLPostRequest{LegalEntityID: le, LedgerID: ledger2, LeaseID: down.ID, PostThrough: day("2030-12-31")}); err != nil || len(res.Failed) != 0 || len(res.Booked) != 6 {
		t.Fatalf("cut lease: %+v %v", res, err)
	}
	if got := net(t, fake2, ledger2, p.RemeasurementGain); !got.Equal(d("-769.61")) {
		t.Fatalf("gain account %s, want a 769.61 credit", got)
	}
	if !net(t, fake2, ledger2, p.Liability).IsZero() || !net(t, fake2, ledger2, p.ROUAsset).Add(net(t, fake2, ledger2, p.ROUAccumulated)).IsZero() {
		t.Fatalf("liability %s, ROU less accumulated %s", net(t, fake2, ledger2, p.Liability), net(t, fake2, ledger2, p.ROUAsset).Add(net(t, fake2, ledger2, p.ROUAccumulated)))
	}
}

// A closed period stops the lease at the refused journal; the next request books it and the rest. A refusal is final, so the pending
// booking is dropped and the journal is rebuilt from the profile in force.
func TestLeaseGL_ARefusedJournalStopsTheLeaseAndTheNextRequestResumes(t *testing.T) {
	s, _, ctx := setup(t)
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	workedLease(t, s, ctx, le, "L-GL-CLOSED")
	fake.RefuseClosedThrough("2026-06-30")
	res, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2028-12-31"))
	if err != nil || len(res.Booked) != 0 || len(res.Failed) != 1 || res.Failed[0].Code != ledgerclient.CodePeriodClosed || res.Failed[0].Period != 0 || res.Failed[0].LeaseNumber != "L-GL-CLOSED" {
		t.Fatalf("closed: %+v %v", res, err)
	}
	fake.RefuseClosedThrough("")
	// The accountant changes the accounts before the retry: the rebuilt journal uses them.
	if _, _, err := s.SaveLeasePostingProfile(ctx, store.LeasePostingProfile{LegalEntityID: le, ROUAsset: "1750", ROUAccumulated: "1795", Liability: "2450", InterestExpense: "7250",
		DepreciationExpense: "6850", PaymentClearing: "2150", RemeasurementGain: "7150"}, 0, "accountant"); err != nil {
		t.Fatal(err)
	}
	res, err = s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2028-12-31"))
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 4 || res.Booked[0].Placeholder {
		t.Fatalf("resume: %+v %v", res, err)
	}
	if !net(t, fake, ledgerID, "1750").Equal(workedLeaseLiability) || net(t, fake, ledgerID, "1700").Sign() != 0 {
		t.Fatalf("the profile accounts are used: 1750 = %s, 1700 = %s", net(t, fake, ledgerID, "1750"), net(t, fake, ledgerID, "1700"))
	}
}

var workedLeaseLiability = d("43294.77")

// The ledger accepts a journal but the answer is lost: the booking stays pending, the lease cannot be remeasured meanwhile, and the retry resends the
// same journal, which the ledger recognizes by its key, so nothing is booked twice.
func TestLeaseGL_ALostAnswerIsRetriedWithoutBookingTwice(t *testing.T) {
	s, _, ctx := setup(t)
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	lease := workedLease(t, s, ctx, le, "L-GL-LOST")
	fake.LoseAnswers(errors.New("timeout"))
	res, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2026-12-31"))
	if err != nil || len(res.Booked) != 0 || len(res.Failed) != 1 || res.Failed[0].Code != store.CodeGLFailed {
		t.Fatalf("lost answer: %+v %v", res, err)
	}
	if _, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: lease.Version, EffectivePeriod: 2, PaymentAmount: d("12000")}, "mod-in-flight"); problemCode(err) != store.CodeLeasePeriodPosted {
		t.Fatalf("a lease with a booking in flight is not remeasured: %v", err)
	}
	fake.LoseAnswers(nil)
	if res, err = s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2026-12-31")); err != nil || len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if len(fake.Entries()) != 2 {
		t.Fatalf("the ledger holds %d entries, want 2", len(fake.Entries()))
	}
	if _, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: lease.Version, EffectivePeriod: 2, PaymentAmount: d("12000")}, "mod-after-settle"); err != nil {
		t.Fatalf("period 2 is still open for remeasurement once the booking is settled: %v", err)
	}
}

func TestLeaseGL_TheProfileHasPlaceholdersVersionsAndTenantIsolation(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	got, err := s.GetLeasePostingProfile(ctx, le)
	if err != nil || !got.Placeholder || got.LegalEntityID != le || got.ROUAsset != "1700" {
		t.Fatalf("placeholder: %+v %v", got, err)
	}
	in := store.LeasePostingProfile{LegalEntityID: le, ROUAsset: "1750", ROUAccumulated: "1795", Liability: "2450", InterestExpense: "7250", DepreciationExpense: "6850",
		PaymentClearing: "2150", RemeasurementGain: "7150"}
	if _, _, err := s.SaveLeasePostingProfile(ctx, in, 3, "a"); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("replacing a missing profile: %v", err)
	}
	saved, created, err := s.SaveLeasePostingProfile(ctx, in, 0, "a")
	if err != nil || !created || saved.Version != 1 || saved.Placeholder || saved.UpdatedBy != "a" {
		t.Fatalf("create: %+v %v %v", saved, created, err)
	}
	if _, _, err := s.SaveLeasePostingProfile(ctx, in, 0, "a"); problemCode(err) != "precondition_required" {
		t.Fatalf("replace without If-Match: %v", err)
	}
	in.Liability = "2460"
	if _, _, err := s.SaveLeasePostingProfile(ctx, in, 2, "a"); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if saved, created, err = s.SaveLeasePostingProfile(ctx, in, 1, "b"); err != nil || created || saved.Version != 2 || saved.Liability != "2460" {
		t.Fatalf("replace: %+v %v %v", saved, created, err)
	}
	if got, err = s.GetLeasePostingProfile(scoped(2, ""), le); err != nil || !got.Placeholder {
		t.Fatalf("another tenant does not see it: %+v %v", got, err)
	}
}

// A modification dated before an earlier, unbooked one replaces the schedule from its own period, so the earlier one no longer applies: its
// period books no remeasurement, the later one starts from the ROU carrying amount without the superseded adjustment, and the ledger clears
// the liability and the ROU asset to the cent.
func TestLeaseGL_AModificationDatedBeforeAnEarlierOneSupersedesIt(t *testing.T) {
	s, _, ctx := setup(t)
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	lease := workedLease(t, s, ctx, le, "L-GL-RETRO")
	later, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: lease.Version, EffectivePeriod: 5, PaymentAmount: d("12000")}, "retro-5")
	if err != nil {
		t.Fatal(err)
	}
	earlier, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: later.Lease.Version, EffectivePeriod: 3, PaymentAmount: d("11000")}, "retro-3")
	if err != nil {
		t.Fatal(err)
	}
	// The ROU carrying amount at the start of period 3 is the recognized ROU less two periods of depreciation; the superseded adjustment of period 5 is not part of it.
	wantCarrying := lease.ROUAsset.Sub(lease.Schedule[0].ROUDepreciation).Sub(lease.Schedule[1].ROUDepreciation)
	if !earlier.Modification.ROUBefore.Equal(wantCarrying) {
		t.Fatalf("ROU before the period-3 modification %s, want %s", earlier.Modification.ROUBefore, wantCarrying)
	}
	mods := earlier.Lease.Modifications
	if len(mods) != 2 || !mods[0].Superseded || mods[1].Superseded {
		t.Fatalf("the period-5 modification is superseded, the period-3 one is not: %+v", mods)
	}
	res, err := s.PostLeasesToGL(ctx, fake, store.LeaseGLPostRequest{LegalEntityID: le, LedgerID: ledgerID, LeaseID: lease.ID, PostThrough: day("2030-12-31")})
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 6 {
		t.Fatalf("retroactive modification: %+v %v", res, err)
	}
	p := store.PlaceholderLeaseProfile
	rou := lease.ROUAsset.Add(earlier.Modification.ROUAdjustment)
	if !net(t, fake, ledgerID, p.Liability).IsZero() || !net(t, fake, ledgerID, p.ROUAsset).Equal(rou) || !net(t, fake, ledgerID, p.ROUAccumulated).Equal(rou.Neg()) {
		t.Fatalf("liability %s ROU %s accumulated %s, want 0, %s, %s", net(t, fake, ledgerID, p.Liability), net(t, fake, ledgerID, p.ROUAsset),
			net(t, fake, ledgerID, p.ROUAccumulated), rou, rou.Neg())
	}
}

// overtakingLedger runs another posting request while the ledger is answering the first journal of a request, so that request resends the
// same pending bookings and confirms them first.
type overtakingLedger struct {
	*ledgerclient.Fake
	overtake func()
}

func (l *overtakingLedger) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	if f := l.overtake; f != nil {
		l.overtake = nil
		f()
	}
	return l.Fake.Post(ctx, e)
}

// Two requests that send the same journals (a double click) both report them booked; the ledger holds each once and one lease.posted is written per journal.
func TestLeaseGL_ARequestOvertakenByAnotherReportsTheSameEntriesBooked(t *testing.T) {
	s, pool, ctx := setup(t)
	le, ledgerID := ids.New(), ids.New()
	workedLease(t, s, ctx, le, "L-GL-TWICE")
	ledger := &overtakingLedger{Fake: ledgerclient.NewFake()}
	var second store.LeaseGLPostResult
	var secondErr error
	ledger.overtake = func() { second, secondErr = s.PostLeasesToGL(ctx, ledger, leaseReq(le, ledgerID, "2026-12-31")) }
	first, err := s.PostLeasesToGL(ctx, ledger, leaseReq(le, ledgerID, "2026-12-31"))
	if err != nil || secondErr != nil || len(first.Failed) != 0 || len(second.Failed) != 0 || len(first.Booked) != 2 || len(second.Booked) != 2 {
		t.Fatalf("first %+v %v, second %+v %v", first, err, second, secondErr)
	}
	for i := range first.Booked {
		if first.Booked[i].EntryID != second.Booked[i].EntryID {
			t.Fatalf("journal %d booked as %s and %s", i, first.Booked[i].EntryID, second.Booked[i].EntryID)
		}
	}
	if n := len(ledger.Entries()); n != 2 {
		t.Fatalf("the ledger holds %d entries, want 2", n)
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.lease.posted.v1"] != 2 {
		t.Fatalf("outbox = %v (one lease.posted per journal)", types)
	}
}
