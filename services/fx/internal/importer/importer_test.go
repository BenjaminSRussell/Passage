package importer

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

var day = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

type fakeProvider struct {
	errs   []error
	points []provider.RatePoint
	calls  int
}

func (f *fakeProvider) Name() string { return "ecb" }

func (f *fakeProvider) Fetch(context.Context, string, time.Time) ([]provider.RatePoint, error) {
	f.calls++
	if f.calls <= len(f.errs) && f.errs[f.calls-1] != nil {
		return nil, f.errs[f.calls-1]
	}
	return f.points, nil
}

type fakeStore struct {
	last     *time.Time
	done     bool
	status   string
	attempts int
	finished string
	posted   []store.RateInput
	imported int
}

func (s *fakeStore) StartRun(context.Context, string, string, time.Time) (store.Run, bool, error) {
	return store.Run{ID: "run-1", Status: store.RunSucceeded, PairsImported: 7}, s.done, nil
}

func (s *fakeStore) RecordAttempt(_ context.Context, _ string, n int) error {
	s.attempts = n
	return nil
}

func (s *fakeStore) PostRates(_ context.Context, in []store.RateInput) ([]store.Posted, error) {
	s.posted = in
	out := make([]store.Posted, len(in))
	for n := range out {
		out[n].Unchanged = n == 0
	}
	return out, nil
}

func (s *fakeStore) FinishRunSucceeded(context.Context, string, int, int) error {
	s.finished = store.RunSucceeded
	return nil
}

func (s *fakeStore) FinishRunFailed(context.Context, string, int, error) error {
	s.finished = store.RunFailed
	return nil
}

func (s *fakeStore) FinishRunDegraded(context.Context, string, int, error) error {
	s.finished = store.RunDegraded
	return nil
}

func (s *fakeStore) LastSuccess(context.Context, string, string) (*time.Time, error) {
	return s.last, nil
}

func points() []provider.RatePoint {
	return []provider.RatePoint{
		{From: "EUR", To: "USD", ValidFrom: day, Rate: decimal.RequireFromString("1.08")},
		{From: "EUR", To: "GBP", ValidFrom: day, Rate: decimal.RequireFromString("0.85")},
	}
}

func harness(p *fakeProvider, s *fakeStore) (*Importer, *[]time.Duration) {
	var slept []time.Duration
	i := New(p, s, Config{MaxAttempts: 4, BaseBackoff: time.Second, MaxBackoff: 3 * time.Second}, NewMetrics())
	i.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return i, &slept
}

func TestRunSuccessPostsRatesWithProviderSource(t *testing.T) {
	p, s := &fakeProvider{points: points()}, &fakeStore{}
	i, slept := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || res.Degraded || res.Imported != 1 || res.Unchanged != 1 || res.Attempts != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if s.finished != store.RunSucceeded || len(*slept) != 0 || len(s.posted) != 2 || s.posted[0].Source != "ecb" || s.posted[0].RateType != "CLOSING" {
		t.Fatalf("finished=%q slept=%v posted=%+v", s.finished, *slept, s.posted)
	}
}

func TestRunRetriesTransientFailureWithBackoffThenSucceeds(t *testing.T) {
	p, s := &fakeProvider{errs: []error{errors.New("timeout"), errors.New("503")}, points: points()}, &fakeStore{}
	i, slept := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || res.Degraded || res.Attempts != 3 || p.calls != 3 || s.attempts != 3 || s.finished != store.RunSucceeded {
		t.Fatalf("res = %+v err = %v calls=%d finished=%q", res, err, p.calls, s.finished)
	}
	if len(*slept) != 2 || (*slept)[0] != time.Second || (*slept)[1] != 2*time.Second {
		t.Fatalf("backoff = %v", *slept)
	}
}

func TestRunOutageFallsBackToLastKnownRateAndMarksDegraded(t *testing.T) {
	last := day.Add(-24 * time.Hour)
	down := errors.New("ecb: unexpected status 503")
	p, s := &fakeProvider{errs: []error{down, down, down, down}}, &fakeStore{last: &last}
	i, slept := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || !res.Degraded || res.LastSuccessAt == nil || !res.LastSuccessAt.Equal(last) || res.Attempts != 4 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if s.finished != store.RunDegraded || s.posted != nil || p.calls != 4 {
		t.Fatalf("finished=%q posted=%v calls=%d", s.finished, s.posted, p.calls)
	}
	if len(*slept) != 3 || (*slept)[2] != 3*time.Second {
		t.Fatalf("backoff = %v, want capped at MaxBackoff", *slept)
	}
	rec := httptest.NewRecorder()
	i.metrics.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `fx_provider_degraded{provider="ecb",rate_type="CLOSING"} 1`) || !strings.Contains(body, `fx_provider_degraded_runs_total{provider="ecb",rate_type="CLOSING"} 1`) {
		t.Fatalf("metrics = %s", body)
	}
}

func TestRunRecoveryClearsDegradedMetric(t *testing.T) {
	last := day
	down := errors.New("down")
	s := &fakeStore{last: &last}
	i, _ := harness(&fakeProvider{errs: []error{down, down, down, down}}, s)
	if _, err := i.Run(context.Background(), "CLOSING", day); err != nil {
		t.Fatal(err)
	}
	i.provider = &fakeProvider{points: points()}
	if _, err := i.Run(context.Background(), "CLOSING", day); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	i.metrics.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `fx_provider_degraded{provider="ecb",rate_type="CLOSING"} 0`) {
		t.Fatalf("metrics = %s", rec.Body.String())
	}
}

func TestRunOutageWithoutLastKnownRateIsAClearError(t *testing.T) {
	down := errors.New("down")
	p, s := &fakeProvider{errs: []error{down, down, down, down}}, &fakeStore{}
	i, _ := harness(p, s)
	_, err := i.Run(context.Background(), "CLOSING", day)
	if !errors.Is(err, ErrNoLastKnownRate) || !strings.Contains(err.Error(), "down") || s.finished != store.RunFailed {
		t.Fatalf("err = %v finished=%q", err, s.finished)
	}
}

func TestRunDisabledProviderIsNotRetried(t *testing.T) {
	last := day
	p, s := &fakeProvider{errs: []error{provider.ErrDisabled}}, &fakeStore{last: &last}
	i, slept := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || !res.Degraded || p.calls != 1 || len(*slept) != 0 {
		t.Fatalf("res=%+v err=%v calls=%d slept=%v", res, err, p.calls, *slept)
	}
}

func TestRunAlreadySucceededSkipsFetch(t *testing.T) {
	p, s := &fakeProvider{}, &fakeStore{done: true}
	i, _ := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || !res.AlreadyDone || res.Imported != 7 || p.calls != 0 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, p.calls)
	}
}

func TestRunCancelledContextStopsRetriesButStillRecordsOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	last := day
	s := &fakeStore{last: &last}
	p := &fakeProvider{errs: []error{errors.New("down"), errors.New("down"), errors.New("down"), errors.New("down")}}
	i, _ := harness(p, s)
	i.sleep = func(context.Context, time.Duration) error {
		cancel()
		return ctx.Err()
	}
	res, err := i.Run(ctx, "CLOSING", day)
	if err != nil || !res.Degraded || p.calls != 1 || s.finished != store.RunDegraded {
		t.Fatalf("res=%+v err=%v calls=%d finished=%q", res, err, p.calls, s.finished)
	}
}
