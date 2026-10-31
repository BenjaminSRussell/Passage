package domain_test

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func sampler(t *testing.T, seed string) *domain.Sampler {
	t.Helper()
	s, err := domain.NewSampler([]byte(seed))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSampler_DecisionIsAFunctionOfSeedInvoiceAndRate(t *testing.T) {
	a, b, other := sampler(t, "seed-aaaaaaaaaaaaaaaa"), sampler(t, "seed-aaaaaaaaaaaaaaaa"), sampler(t, "seed-bbbbbbbbbbbbbbbb")
	differs := false
	for i := range 300 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		first := a.Blocks(id, dec("37.5"))
		if first != a.Blocks(id, dec("37.5")) || first != b.Blocks(id, dec("37.5")) {
			t.Fatalf("invoice %s: the decision is not reproducible", id)
		}
		differs = differs || first != other.Blocks(id, dec("37.5"))
	}
	if !differs {
		t.Fatal("a different seed gave identical decisions")
	}
	if a.Blocks("ABCDEF00-0000-4000-8000-000000000001", dec("50")) != a.Blocks("abcdef00-0000-4000-8000-000000000001", dec("50")) {
		t.Fatal("the invoice id case changes the decision")
	}
}

func TestSampler_RateIsMonotonic(t *testing.T) {
	s := sampler(t, "seed-aaaaaaaaaaaaaaaa")
	for i := range 500 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		if s.Blocks(id, dec("10")) && !s.Blocks(id, dec("20")) {
			t.Fatalf("invoice %s blocked at 10%% but not at 20%%", id)
		}
	}
}

func TestSampler_Boundaries(t *testing.T) {
	s := sampler(t, "seed-aaaaaaaaaaaaaaaa")
	for i := range 500 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		if s.Blocks(id, dec("0")) {
			t.Fatal("0% blocked an invoice")
		}
		if !s.Blocks(id, dec("100")) {
			t.Fatal("100% let an invoice through")
		}
	}
	var none *domain.Sampler
	if !none.Blocks("x", dec("0.01")) {
		t.Fatal("a missing sampler with a positive rate must fail closed and block")
	}
	if none.Blocks("x", dec("0")) {
		t.Fatal("a zero rate blocks nothing, sampler or not")
	}
}

func TestSampler_BlocksAboutTheConfiguredShare(t *testing.T) {
	s := sampler(t, "seed-aaaaaaaaaaaaaaaa")
	for _, tc := range []struct {
		rate     string
		min, max int
	}{{"10", 900, 1100}, {"0.50", 20, 80}, {"99.99", 9980, 10000}} {
		blocked := 0
		for i := range 10000 {
			if s.Blocks(fmt.Sprintf("00000000-0000-4000-8000-%012d", i), dec(tc.rate)) {
				blocked++
			}
		}
		if blocked < tc.min || blocked > tc.max {
			t.Errorf("rate %s%% blocked %d of 10000, want %d to %d", tc.rate, blocked, tc.min, tc.max)
		}
	}
}

func TestNewSampler_NeedsASeedOfSomeLength(t *testing.T) {
	if _, err := domain.NewSampler([]byte("short")); err == nil {
		t.Fatal("a 5 byte seed was accepted")
	}
	a, err := domain.NewRandomSampler()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := domain.NewRandomSampler()
	same := true
	for i := range 64 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		same = same && a.Blocks(id, dec("50")) == b.Blocks(id, dec("50"))
	}
	if same {
		t.Fatal("two randomly seeded samplers agree on 64 invoices")
	}
}

func TestValidSampleRate(t *testing.T) {
	for rate, want := range map[string]bool{"0": true, "100": true, "12.34": true, "0.01": true, "100.01": false, "-0.01": false, "12.345": false, "1e2": true} {
		if got := domain.ValidSampleRate(decimal.RequireFromString(rate)); got != want {
			t.Errorf("ValidSampleRate(%s) = %v, want %v", rate, got, want)
		}
	}
}

func TestAuditPolicyWeightIsTotalOverThresholdCappedAtOne(t *testing.T) {
	p := domain.AuditPolicy{RatePct: dec("50"), Threshold: dec("1000"), ThresholdCurrency: "USD", RuleVersion: 1}
	for _, tc := range []struct {
		total, currency, want string
	}{
		{"250", "USD", "0.25"}, {"1000", "USD", "1"}, {"5000", "USD", "1"}, {"0", "USD", "0"}, {"-5", "USD", "0"}, {"10", "EUR", "1"},
	} {
		if got := p.Weight(dec(tc.total), tc.currency); !got.Equal(dec(tc.want)) {
			t.Errorf("weight(%s %s) = %s, want %s", tc.total, tc.currency, got, tc.want)
		}
	}
	if got := (domain.AuditPolicy{RatePct: dec("50")}).Weight(dec("1"), "USD"); !got.Equal(dec("1")) {
		t.Errorf("no threshold must weigh 1, got %s", got)
	}
}

func TestSamplerDecisionDependsOnRuleVersion(t *testing.T) {
	s, _ := domain.NewSampler([]byte("0123456789abcdef"))
	differs := false
	for i := range 200 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		v1 := s.Decide(id, domain.AuditPolicy{RatePct: dec("50"), RuleVersion: 1}, dec("0"), "")
		if v1 != s.Decide(id, domain.AuditPolicy{RatePct: dec("50"), RuleVersion: 1}, dec("0"), "") {
			t.Fatal("the same version must decide the same way")
		}
		differs = differs || v1 != s.Decide(id, domain.AuditPolicy{RatePct: dec("50"), RuleVersion: 2}, dec("0"), "")
	}
	if !differs {
		t.Fatal("a new rule version must re-draw")
	}
}
