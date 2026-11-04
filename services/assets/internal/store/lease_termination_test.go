package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// late is the day the tests terminate on: inside period 5 of the worked lease (2026-01-01, yearly), so ending after periods 1 to 4 is not ahead
// of the running period (domain.TerminationNotAhead).
var late = time.Date(2030, 6, 30, 15, 0, 0, 0, time.UTC)

// The worked lease (43,294.77 at 5%, 10,000.00 a year in arrears, 60 months) ends after year 2: the liability then is 27,232.49
// (35,459.51 after year 1, interest 1,772.98 in year 2), the ROU asset has depreciated 2 x 8,658.95 = 17,317.90 of 43,294.77 and its
// carrying amount is 25,976.87; with a 1,000.00 termination payment the gain is 27,232.49 - 25,976.87 - 1,000.00 = 255.62.
func TestTerminateLease_DerecognizesLiabilityAndROUWithGain(t *testing.T) {
	s, pool, ctx := setup(t)
	lease := workedLease(t, s, ctx, ids.New(), "L-TERM-1")
	in := store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: 2, Payment: d("1000"), Reason: "site closed"}
	res, err := s.TerminateLease(ctx, lease.ID, in, "term-1")
	if err != nil {
		t.Fatal(err)
	}
	tm := res.Termination
	if !tm.LiabilityDerecognized.Equal(d("27232.49")) || !tm.ROUAccumulated.Equal(d("17317.90")) || !tm.ROUCarrying.Equal(d("25976.87")) ||
		!tm.ROUGross.Equal(d("43294.77")) || !tm.GainLoss.Equal(d("255.62")) || tm.TerminationDate.String() != "2027-12-31" || tm.JournalID != nil || tm.Reason != "site closed" {
		t.Fatalf("termination %+v", tm)
	}
	if res.Lease.Status != domain.LeaseTerminated || res.Lease.Version != lease.Version+1 || len(res.Lease.Schedule) != 2 || res.Lease.Termination == nil || len(res.Messages) != 1 {
		t.Fatalf("lease after: %+v", res.Lease)
	}
	replay, err := s.TerminateLease(ctx, lease.ID, in, "term-1")
	if err != nil || replay.Termination.ID != tm.ID {
		t.Fatalf("replay with the same key returns the first answer: %+v %v", replay, err)
	}
	if _, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: 3}, "term-1"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other request: %v", err)
	}
	if _, err := s.TerminateLease(ctx, lease.ID, in, "term-again"); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	again := store.LeaseTerminationInput{AsOf: late, Version: res.Lease.Version, AfterPeriod: 1}
	if _, err := s.TerminateLease(ctx, lease.ID, again, "term-twice"); err == nil || err.Error() == "" {
		t.Fatalf("a terminated lease is not terminated again: %v", err)
	}
	if _, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: res.Lease.Version, EffectivePeriod: 1, PaymentAmount: d("1")}, "mod-after-term"); err == nil {
		t.Fatal("a terminated lease cannot be modified")
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.lease.terminated.v1"] != 1 {
		t.Fatalf("outbox = %v", types)
	}
	got, err := s.GetLease(ctx, lease.ID)
	if err != nil || got.Termination == nil || got.Termination.ID != tm.ID {
		t.Fatalf("get lease carries the termination: %+v %v", got.Termination, err)
	}
	if _, err := s.TerminateLease(scoped(2, ""), lease.ID, in, "term-other-tenant"); problemCode(err) != store.CodeLeaseNotFound {
		t.Fatalf("another tenant: %v", err)
	}
}

func TestTerminateLease_Validation(t *testing.T) {
	s, _, ctx := setup(t)
	lease := workedLease(t, s, ctx, ids.New(), "L-TERM-2")
	for name, after := range map[string]int{"zero": 0, "the last period": 5, "beyond": 9} {
		if _, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: after}, "bad-"+name); err == nil {
			t.Errorf("after period %d (%s) accepted", after, name)
		}
	}
	if _, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: 1, Payment: d("-1")}, "bad-payment"); err == nil {
		t.Error("negative payment accepted")
	}
	if _, err := s.TerminateLease(ctx, ids.New(), store.LeaseTerminationInput{AsOf: late, Version: 1, AfterPeriod: 1}, "unknown"); problemCode(err) != store.CodeLeaseNotFound {
		t.Errorf("unknown lease: %v", err)
	}
}

// A penalty above the liability less the ROU carrying amount is a loss (a debit on the gain account).
func TestTerminateLease_PenaltyGivesLoss(t *testing.T) {
	s, _, ctx := setup(t)
	lease := workedLease(t, s, ctx, ids.New(), "L-TERM-3")
	res, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: 2, Payment: d("5000")}, "loss")
	if err != nil || !res.Termination.GainLoss.Equal(d("-3744.38")) || res.Termination.Reason != "" {
		t.Fatalf("%+v %v", res.Termination, err)
	}
}

// The termination journal is booked after every period up to the termination, dated on the termination date; afterwards the liability, the ROU asset
// and its accumulated depreciation are all zero in the ledger, and the payment and the gain are the only residue.
func TestTerminateLease_JournalFollowsThePeriodsAndClearsTheBalances(t *testing.T) {
	s, pool, ctx := setup(t)
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	lease := workedLease(t, s, ctx, le, "L-TERM-4")
	p := store.PlaceholderLeaseProfile
	res, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: 2, Payment: d("1000")}, "term-gl")
	if err != nil || res.Termination.JournalID != nil {
		t.Fatal(err)
	}
	// Through the end of year 1 only the recognition and period 1 are booked: the termination waits for period 2.
	first, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2027-06-30"))
	if err != nil || len(first.Booked) != 2 || len(first.Failed) != 0 {
		t.Fatalf("first: %+v %v", first, err)
	}
	if posted, err := s.GetLeasePostings(ctx, lease.ID); err != nil || posted.TerminationJournalID != nil || len(posted.Periods) != 2 {
		t.Fatalf("postings: %+v %v", posted, err)
	}
	rest, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2035-12-31"))
	if err != nil || len(rest.Failed) != 0 || len(rest.Booked) != 2 || rest.Booked[0].Period != 2 || rest.Booked[1].Period != -1 {
		t.Fatalf("rest: %+v %v", rest, err)
	}
	entries := fake.Entries()
	if last := entries[len(entries)-1]; last.PostingDate != "2027-12-31" || last.DocumentID != lease.ID {
		t.Fatalf("termination entry: %+v", last)
	}
	checks := map[string]string{
		p.Liability: "0", p.ROUAsset: "0", p.ROUAccumulated: "0", p.TerminationGainLoss: "-255.62", p.RemeasurementGain: "0", p.PaymentClearing: "-21000",
		p.DepreciationExpense: "17317.90",
	}
	for account, want := range checks {
		if got := net(t, fake, ledgerID, account); !got.Equal(d(want)) {
			t.Errorf("account %s = %s, want %s", account, got, want)
		}
	}
	posted, err := s.GetLeasePostings(ctx, lease.ID)
	if err != nil || posted.TerminationJournalID == nil || len(posted.Periods) != 2 {
		t.Fatalf("postings after: %+v %v", posted, err)
	}
	got, err := s.GetLease(ctx, lease.ID)
	if err != nil || got.Termination == nil || got.Termination.JournalID == nil || *got.Termination.JournalID != *posted.TerminationJournalID {
		t.Fatalf("termination journal link: %+v %v", got.Termination, err)
	}
	if again, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2035-12-31")); err != nil || len(again.Booked) != 0 {
		t.Fatalf("booked once: %+v %v", again, err)
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.lease.posted.v1"] != 4 || types["dev.passage.assets.lease.terminated.v1"] != 1 {
		t.Fatalf("outbox = %v", types)
	}
}

// A period after the termination that is already booked refuses it; terminating after the last booked period works.
func TestTerminateLease_RefusedWhenLaterPeriodsArePosted(t *testing.T) {
	s, _, ctx := setup(t)
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	lease := workedLease(t, s, ctx, le, "L-TERM-5")
	if r, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2029-12-31")); err != nil || len(r.Booked) != 5 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: 2}, "late"); problemCode(err) != store.CodeLeasePeriodPosted {
		t.Fatalf("periods 3 and 4 are posted: %v", err)
	}
	res, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: 4}, "last-posted")
	if err != nil {
		t.Fatal(err)
	}
	// After period 4 the liability is 9,523.82 (one payment of 10,000 at 5% is 9,523.81; the schedule carries the cents of its rounded interest) and the ROU carrying amount is its final year's depreciation.
	if !res.Termination.LiabilityDerecognized.Equal(d("9523.82")) {
		t.Fatalf("liability %s", res.Termination.LiabilityDerecognized)
	}
	if r, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2030-12-31")); err != nil || len(r.Booked) != 1 || r.Booked[0].Period != -1 {
		t.Fatalf("termination journal: %+v %v", r, err)
	}
}

// A termination takes effect at the latest at the end of the period running today: ending the lease after a later period is a change of term
// (a modification remeasured now), so it is refused. The boundary is the first day of the period.
func TestTerminateLease_RefusedAheadOfTheRunningPeriod(t *testing.T) {
	s, _, ctx := setup(t)
	lease := workedLease(t, s, ctx, ids.New(), "L-TERM-AHEAD")
	inYear1 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	_, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{Version: lease.Version, AfterPeriod: 2, AsOf: inYear1}, "ahead")
	if problemCode(err) != domain.CodeLeaseTermination {
		t.Fatalf("period 2 starts in 2027: %v", err)
	}
	if got, _ := s.GetLease(ctx, lease.ID); got.Status != domain.LeaseActive || len(got.Schedule) != 5 || got.Termination != nil {
		t.Fatalf("a refused termination changes nothing: %+v", got)
	}
	firstDay := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{Version: lease.Version, AfterPeriod: 2, AsOf: firstDay}, "on-first-day")
	if err != nil || res.Termination.TerminationDate.String() != "2027-12-31" {
		t.Fatalf("on the first day of period 2: %+v %v", res.Termination, err)
	}
	other := workedLease(t, s, ctx, ids.New(), "L-TERM-NOW")
	if _, err := s.TerminateLease(ctx, other.ID, store.LeaseTerminationInput{Version: other.Version, AfterPeriod: 1, AsOf: inYear1}, "running"); err != nil {
		t.Fatalf("the running period: %v", err)
	}
}

// A modified lease: the ROU adjustment of a modification in force at the termination is part of the gross ROU derecognized, one that takes
// effect after it is not (it was never booked). Either way the ledger ends with the liability, the ROU asset and its accumulated depreciation
// at zero and the journal balanced.
func TestTerminateLease_ModifiedLeaseClearsTheLedger(t *testing.T) {
	s, _, ctx := setup(t)
	p := store.PlaceholderLeaseProfile
	for _, c := range []struct {
		name             string
		effective, after int
		modInGross       bool
	}{
		{"modification in force", 2, 3, true},
		{"modification after the termination", 4, 2, false},
	} {
		le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
		lease := workedLease(t, s, ctx, le, fmt.Sprintf("L-TERM-MOD-%d", c.effective))
		mod, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: lease.Version, EffectivePeriod: c.effective, PaymentAmount: d("12000")}, "mod-"+c.name)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		res, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: mod.Lease.Version, AfterPeriod: c.after, Payment: d("750.25")}, "term-"+c.name)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		tm := res.Termination
		gross := lease.ROUAsset
		if c.modInGross {
			gross = gross.Add(mod.Modification.ROUAdjustment)
		}
		if !tm.ROUGross.Equal(gross) || !tm.LiabilityDerecognized.Equal(mod.Lease.Schedule[c.after-1].Closing) {
			t.Fatalf("%s: gross %s want %s, liability %s want %s", c.name, tm.ROUGross, gross, tm.LiabilityDerecognized, mod.Lease.Schedule[c.after-1].Closing)
		}
		booked, err := s.PostLeasesToGL(ctx, fake, leaseReq(le, ledgerID, "2035-12-31"))
		if err != nil || len(booked.Failed) != 0 || len(booked.Booked) != c.after+2 {
			t.Fatalf("%s: %+v %v", c.name, booked, err)
		}
		for _, account := range []string{p.Liability, p.ROUAsset, p.ROUAccumulated} {
			if got := net(t, fake, ledgerID, account); !got.IsZero() {
				t.Errorf("%s: account %s = %s after the termination journal", c.name, account, got)
			}
		}
		if got := net(t, fake, ledgerID, p.TerminationGainLoss); !got.Equal(tm.GainLoss.Neg()) {
			t.Errorf("%s: gain account %s, want %s", c.name, got, tm.GainLoss.Neg())
		}
	}
}

// A termination payment with more decimals than the currency allows is refused, not rounded.
func TestTerminateLease_PaymentPrecision(t *testing.T) {
	s, _, ctx := setup(t)
	lease := workedLease(t, s, ctx, ids.New(), "L-TERM-CENTS")
	if _, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{AsOf: late, Version: lease.Version, AfterPeriod: 1, Payment: d("10.005")}, "cents"); problemCode(err) != domain.CodeLeaseTermination {
		t.Fatalf("10.005 USD: %v", err)
	}
}
