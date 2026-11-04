package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// exemptLease stores a quarterly lease of 12 months under an election: no liability, no ROU asset, one payment line per quarter.
func exemptLease(t *testing.T, s *store.Postgres, ctx context.Context, le, number, election string, underlying *string) store.Lease {
	t.Helper()
	terms := domain.LeaseTerms{Payment: d("750"), PaymentsPerYear: 4, TermMonths: 12, AnnualRate: d("0.05"), Currency: "USD"}
	sched, err := domain.ComputeExemptLease(terms)
	if err != nil {
		t.Fatal(err)
	}
	lines := []store.LeaseLine{}
	for _, l := range sched.Lines {
		lines = append(lines, store.LeaseLine{PeriodNo: l.PeriodNo, Opening: l.Opening, Interest: l.Interest, Payment: l.Payment, Closing: l.Closing})
	}
	in := store.Lease{LegalEntityID: le, LeaseNumber: number, CommencementDate: day("2026-01-01"), TermMonths: 12, PaymentsPerYear: 4, PaymentAmount: d("750"),
		PaymentTiming: "advance", IBRRate: d("0.05"), Currency: "USD", Election: election, Schedule: lines}
	if underlying != nil {
		v := d(*underlying)
		in.UnderlyingAssetValue, in.UnderlyingAssetCurrency = &v, "EUR"
	}
	lease, err := s.CreateLease(ctx, in, "lease-"+number)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

// A lease under an election is stored without a liability or ROU asset, announced with its election, never recognized in the ledger, and each
// payment period is expensed against the payment clearing account; it can be neither remeasured nor terminated.
func TestLeaseElection_NothingIsRecognizedAndEachPaymentIsExpensed(t *testing.T) {
	s, pool, ctx := setup(t)
	le, ledgerID, fake := ids.New(), ids.New(), ledgerclient.NewFake()
	lease := exemptLease(t, s, ctx, le, "L-ST-1", domain.ElectionShortTerm, nil)
	value := "4200"
	low := exemptLease(t, s, ctx, le, "L-LV-1", domain.ElectionLowValue, &value)
	got, err := s.GetLease(ctx, low.ID)
	if err != nil || got.Election != domain.ElectionLowValue || got.UnderlyingAssetValue == nil || !got.UnderlyingAssetValue.Equal(d("4200")) || got.UnderlyingAssetCurrency != "EUR" || !got.Liability.IsZero() || !got.ROUAsset.IsZero() {
		t.Fatalf("low value lease: %+v %v", got, err)
	}
	if lease.Election != domain.ElectionShortTerm || lease.UnderlyingAssetValue != nil || lease.UnderlyingAssetCurrency != "" || lease.PurchaseOption || len(lease.Schedule) != 4 {
		t.Fatalf("short-term lease: %+v", lease)
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.lease.recognized.v1"] != 2 {
		t.Fatalf("outbox = %v", types)
	}

	p := store.PlaceholderLeaseProfile
	res, err := s.PostLeasesToGL(ctx, fake, store.LeaseGLPostRequest{LegalEntityID: le, LedgerID: ledgerID, LeaseID: lease.ID, PostThrough: day("2026-06-30")})
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 2 || res.Booked[0].Period != 1 || res.Booked[1].Period != 2 {
		t.Fatalf("first half year: %+v %v", res, err)
	}
	for _, e := range fake.Entries() {
		if len(e.Lines) != 2 {
			t.Fatalf("a payment period is one expense line and one clearing line: %+v", e)
		}
	}
	rest, err := s.PostLeasesToGL(ctx, fake, store.LeaseGLPostRequest{LegalEntityID: le, LedgerID: ledgerID, PostThrough: day("2026-12-31")})
	if err != nil || len(rest.Failed) != 0 || len(rest.Booked) != 6 {
		t.Fatalf("rest, both leases: %+v %v", rest, err)
	}
	if got := net(t, fake, ledgerID, p.ExemptLeaseExpense); !got.Equal(d("6000")) {
		t.Fatalf("expense %s, want 2 leases x 4 x 750", got)
	}
	for _, account := range []string{p.ROUAsset, p.ROUAccumulated, p.Liability, p.InterestExpense, p.DepreciationExpense, p.RemeasurementGain} {
		if got := net(t, fake, ledgerID, account); !got.IsZero() {
			t.Errorf("account %s = %s on a lease under an election", account, got)
		}
	}
	if got := net(t, fake, ledgerID, p.PaymentClearing); !got.Equal(d("-6000")) {
		t.Fatalf("clearing %s", got)
	}
	if posted, err := s.GetLeasePostings(ctx, lease.ID); err != nil || posted.RecognitionJournalID != nil || len(posted.Periods) != 4 || posted.Periods[3].PostedJournalID == nil {
		t.Fatalf("postings: %+v %v", posted, err)
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.lease.posted.v1"] != 8 {
		t.Fatalf("outbox = %v (one lease.posted per period)", types)
	}

	if _, err := s.ModifyLease(ctx, lease.ID, store.LeaseModificationInput{Version: lease.Version, EffectivePeriod: 3, PaymentAmount: d("800")}, "mod-exempt"); problemCode(err) != domain.CodeLeaseElection {
		t.Fatalf("modify: %v", err)
	}
	if _, err := s.TerminateLease(ctx, lease.ID, store.LeaseTerminationInput{Version: lease.Version, AfterPeriod: 2, AsOf: late}, "term-exempt"); problemCode(err) != domain.CodeLeaseElection {
		t.Fatalf("terminate: %v", err)
	}
}

// The database refuses an election that was not validated: a short-term lease over 12 months or with a purchase option, a low-value lease
// without its value or without the currency of that value, a recognized balance on a lease under an election.
func TestLeaseElection_DatabaseConstraints(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	base := store.Lease{LegalEntityID: le, CommencementDate: day("2026-01-01"), TermMonths: 24, PaymentsPerYear: 12, PaymentAmount: d("100"), PaymentTiming: "arrears",
		IBRRate: d("0.05"), Currency: "USD", Election: domain.ElectionShortTerm}
	long := base
	long.LeaseNumber = "L-BAD-1"
	if _, err := s.CreateLease(ctx, long, "bad-1"); err == nil {
		t.Fatal("a 24-month short-term lease was stored")
	}
	noValue := base
	noValue.LeaseNumber, noValue.TermMonths, noValue.Election = "L-BAD-2", 12, domain.ElectionLowValue
	if _, err := s.CreateLease(ctx, noValue, "bad-2"); err == nil {
		t.Fatal("a low-value lease without a value was stored")
	}
	option := base
	option.LeaseNumber, option.TermMonths, option.PurchaseOption = "L-BAD-4", 12, true
	if _, err := s.CreateLease(ctx, option, "bad-4"); err == nil {
		t.Fatal("a short-term lease with a purchase option was stored")
	}
	noCurrency := noValue
	v := d("900")
	noCurrency.LeaseNumber, noCurrency.UnderlyingAssetValue = "L-BAD-5", &v
	if _, err := s.CreateLease(ctx, noCurrency, "bad-5"); err == nil {
		t.Fatal("a low-value lease without the currency of its value was stored")
	}
	recognized := base
	recognized.LeaseNumber, recognized.TermMonths, recognized.Liability, recognized.ROUAsset = "L-BAD-3", 12, d("1000"), d("1000")
	if _, err := s.CreateLease(ctx, recognized, "bad-3"); err == nil {
		t.Fatal("a lease under an election with a liability was stored")
	}
}

// The termination gain or loss goes to its own account; a profile saved without the two new accounts keeps the earlier ones.
func TestLeaseElection_ProfileAccounts(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	got, err := s.GetLeasePostingProfile(ctx, le)
	if err != nil || got.TerminationGainLoss != "7130" || got.ExemptLeaseExpense != "6830" {
		t.Fatalf("placeholder: %+v %v", got, err)
	}
	in := store.LeasePostingProfile{LegalEntityID: le, ROUAsset: "1750", ROUAccumulated: "1795", Liability: "2450", InterestExpense: "7250", DepreciationExpense: "6850",
		PaymentClearing: "2150", RemeasurementGain: "7150"}
	saved, _, err := s.SaveLeasePostingProfile(ctx, in, 0, "a")
	if err != nil || saved.TerminationGainLoss != "7150" || saved.ExemptLeaseExpense != "6850" {
		t.Fatalf("defaults: %+v %v", saved, err)
	}
	in.TerminationGainLoss, in.ExemptLeaseExpense = "7160", "6860"
	if saved, _, err = s.SaveLeasePostingProfile(ctx, in, 1, "a"); err != nil || saved.TerminationGainLoss != "7160" || saved.ExemptLeaseExpense != "6860" {
		t.Fatalf("explicit: %+v %v", saved, err)
	}
}
