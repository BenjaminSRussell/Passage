package domain_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

type fakeProvider struct {
	delay time.Duration
	res   domain.Result
	err   error
}

func (f fakeProvider) Name() string { return "avalara" }

func (f fakeProvider) Calculate(ctx context.Context, _ domain.Document) (domain.Result, error) {
	select {
	case <-time.After(f.delay):
		return f.res, f.err
	case <-ctx.Done():
		return domain.Result{}, ctx.Err()
	}
}

func (fakeProvider) Commit(context.Context, string) error                  { return nil }
func (fakeProvider) Void(context.Context, string) error                    { return nil }
func (fakeProvider) ValidateAddress(context.Context, domain.Address) error { return nil }

func failoverFixture() (domain.Document, domain.Config) {
	return buildDoc("EUR", domain.DirOutput, false, "100.00"), buildConfig(domain.RoundPerDocument, spec{name: "VAT", pct: "19"})
}

func TestFailover_Timeout(t *testing.T) {
	doc, cfg := failoverFixture()
	tests := []struct {
		name    string
		timeout time.Duration
		maxWait time.Duration
	}{
		{"default two second timeout", 0, 2500 * time.Millisecond},
		{"explicit timeout", 50 * time.Millisecond, 500 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			res, notice, err := domain.CalculateWithFailover(context.Background(), fakeProvider{delay: time.Minute}, doc, cfg, tc.timeout)
			if err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed > tc.maxWait {
				t.Errorf("failover took %s, want under %s", elapsed, tc.maxWait)
			}
			if !res.Fallback || res.Engine != domain.EngineInternal || !hasWarning(res, domain.CodeProviderFallback) {
				t.Errorf("fallback not flagged: %+v", res)
			}
			mustEqual(t, "internal tax", res.Lines[0].Taxes[0].TaxAmount, "19.00")
			if notice == nil || notice.Provider != "avalara" || notice.Reason == "" {
				t.Errorf("notice %+v", notice)
			}
		})
	}
}

func TestFailover_Outcomes(t *testing.T) {
	doc, cfg := failoverFixture()
	providerResult := domain.Result{Engine: "avalara"}
	tests := []struct {
		name         string
		provider     fakeProvider
		wantFallback bool
		wantErr      bool
	}{
		{"provider answers", fakeProvider{res: providerResult}, false, false},
		{"provider 5xx", fakeProvider{err: &domain.ProviderError{Status: 503, Err: errors.New("unavailable")}}, true, false},
		{"provider network error", fakeProvider{err: errors.New("connection reset")}, true, false},
		{"provider 4xx is surfaced", fakeProvider{err: &domain.ProviderError{Status: 400, Err: errors.New("bad address")}}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, notice, err := domain.CalculateWithFailover(context.Background(), tc.provider, doc, cfg, time.Second)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if res.Fallback != tc.wantFallback || (notice != nil) != tc.wantFallback {
				t.Errorf("fallback = %v notice = %v, want %v", res.Fallback, notice, tc.wantFallback)
			}
			if !tc.wantFallback && !tc.wantErr && res.Engine != "avalara" {
				t.Errorf("engine %q, want provider result", res.Engine)
			}
		})
	}
}

func TestFailover_CallerCancellationIsNotFallback(t *testing.T) {
	doc, cfg := failoverFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := domain.CalculateWithFailover(ctx, fakeProvider{delay: time.Minute}, doc, cfg, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestFailover_InternalEngineErrorSurfaces(t *testing.T) {
	doc, cfg := failoverFixture()
	doc.TaxDate = domain.Day("2019-01-01")
	_, _, err := domain.CalculateWithFailover(context.Background(), fakeProvider{err: &domain.ProviderError{Status: 500, Err: errors.New("down")}}, doc, cfg, time.Second)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeRateNotFound {
		t.Fatalf("got %v, want %s", err, domain.CodeRateNotFound)
	}
}
