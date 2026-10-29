package importer

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

var day = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

type fakeProvider struct {
	errs   []error
	points []provider.RatePoint
	calls  int

	history bool
}

func (f *fakeProvider) Name() string { return "ecb" }

func (f *fakeProvider) SupportsHistory() bool { return f.history }

func (f *fakeProvider) Fetch(context.Context, string, time.Time) ([]provider.RatePoint, error) {
	f.calls++
	if f.calls <= len(f.errs) && f.errs[f.calls-1] != nil {
		return nil, f.errs[f.calls-1]
	}
	return f.points, nil
}

type finish struct {
	kind      string
	runID     string
	attempts  int
	cause     error
	retryable bool
}

type fakeStore struct {
	last        *time.Time
	done        bool
	attempts    int
	finishes    []finish
	posted      []store.RateInput
	attemptErr  error
	postErr     error
	lastErr     error
	degradedErr error
	status      string
}

func (s *fakeStore) outcome() string {
	if len(s.finishes) == 0 {
		return ""
	}
	return s.finishes[len(s.finishes)-1].kind
}

func (s *fakeStore) StartRun(context.Context, string, string, time.Time) (store.Run, bool, error) {
	status := s.status
	if status == "" {
		status = store.RunSucceeded
	}
	return store.Run{ID: "run-1", Status: status, PairsImported: 7, LastSuccessAt: s.last}, s.done, nil
}

func (s *fakeStore) RecordAttempt(_ context.Context, _ string, n int) error {
	s.attempts = n
	return s.attemptErr
}

func (s *fakeStore) PostRates(_ context.Context, in []store.RateInput) ([]store.Posted, error) {
	if s.postErr != nil {
		return nil, s.postErr
	}
	s.posted = in
	out := make([]store.Posted, len(in))
	for n := range out {
		out[n].Unchanged = n == 0
	}
	return out, nil
}

func (s *fakeStore) FinishRunSucceeded(_ context.Context, id string, imported, unchanged int) error {
	s.finishes = append(s.finishes, finish{kind: store.RunSucceeded, runID: id, attempts: imported + unchanged})
	return nil
}

func (s *fakeStore) FinishRunFailed(_ context.Context, id string, attempts int, cause error, retryable bool) error {
	s.finishes = append(s.finishes, finish{"failed", id, attempts, cause, retryable})
	return nil
}

func (s *fakeStore) FinishRunDegraded(_ context.Context, id string, attempts int, cause error, retryable bool) error {
	s.finishes = append(s.finishes, finish{store.RunDegraded, id, attempts, cause, retryable})
	return s.degradedErr
}

func (s *fakeStore) FinishRunAborted(_ context.Context, id string, attempts int, cause error) error {
	s.finishes = append(s.finishes, finish{"aborted", id, attempts, cause, false})
	return nil
}

func (s *fakeStore) LastSuccess(context.Context, string, string) (*time.Time, error) {
	return s.last, s.lastErr
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
	i.now = func() time.Time { return day.Add(time.Hour) }
	return i, &slept
}

func scrape(i *Importer) string {
	rec := httptest.NewRecorder()
	i.metrics.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func downs(n int, err error) []error {
	out := make([]error, n)
	for k := range out {
		out[k] = err
	}
	return out
}

func TestRunSuccessPostsRatesWithProviderSource(t *testing.T) {
	p, s := &fakeProvider{points: points()}, &fakeStore{}
	i, slept := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || res.Degraded || res.Imported != 1 || res.Unchanged != 1 || res.Attempts != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if s.outcome() != store.RunSucceeded || len(*slept) != 0 || len(s.posted) != 2 || s.posted[0].Source != "ecb" || s.posted[0].RateType != "CLOSING" {
		t.Fatalf("finishes=%+v slept=%v posted=%+v", s.finishes, *slept, s.posted)
	}
	want := fmt.Sprintf(`fx_provider_last_success_timestamp_seconds{provider="ecb",rate_type="CLOSING"} %d`, day.Add(time.Hour).Unix())
	if body := scrape(i); !strings.Contains(body, want) || !strings.Contains(body, `fx_provider_degraded{provider="ecb",rate_type="CLOSING"} 0`) {
		t.Fatalf("metrics = %s", body)
	}
}

func TestRunRetriesTransientFailureWithBackoffThenSucceeds(t *testing.T) {
	errs := []error{errors.New("timeout"), fmt.Errorf("ecb: %w", &provider.StatusError{Code: 503})}
	p, s := &fakeProvider{errs: errs, points: points()}, &fakeStore{}
	i, slept := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || res.Degraded || res.Attempts != 3 || p.calls != 3 || s.attempts != 3 || s.outcome() != store.RunSucceeded {
		t.Fatalf("res = %+v err = %v calls=%d finishes=%+v", res, err, p.calls, s.finishes)
	}
	if len(*slept) != 2 || (*slept)[0] != time.Second || (*slept)[1] != 2*time.Second {
		t.Fatalf("backoff = %v", *slept)
	}
}

func TestRunOutageFallsBackToLastKnownRateAndMarksDegraded(t *testing.T) {
	last := day.Add(-24 * time.Hour)
	down := fmt.Errorf("ecb: %w", &provider.StatusError{Code: 503})
	p, s := &fakeProvider{errs: downs(4, down)}, &fakeStore{last: &last}
	i, slept := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || !res.Degraded || res.LastSuccessAt == nil || !res.LastSuccessAt.Equal(last) || res.Attempts != 4 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(s.finishes) != 1 || s.finishes[0].kind != store.RunDegraded || s.finishes[0].runID != "run-1" || s.finishes[0].attempts != 4 || !errors.Is(s.finishes[0].cause, down) || s.posted != nil || p.calls != 4 {
		t.Fatalf("finishes=%+v posted=%v calls=%d", s.finishes, s.posted, p.calls)
	}
	if len(*slept) != 3 || (*slept)[2] != 3*time.Second {
		t.Fatalf("backoff = %v, want capped at MaxBackoff", *slept)
	}
	body := scrape(i)
	for _, want := range []string{
		`fx_provider_degraded{provider="ecb",rate_type="CLOSING"} 1`,
		`fx_provider_degraded_runs_total{provider="ecb",rate_type="CLOSING"} 1`,
		fmt.Sprintf(`fx_provider_last_success_timestamp_seconds{provider="ecb",rate_type="CLOSING"} %d`, last.Unix()),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}

func TestRunRecoveryClearsDegradedMetric(t *testing.T) {
	last := day
	s := &fakeStore{last: &last}
	i, _ := harness(&fakeProvider{errs: downs(4, errors.New("down"))}, s)
	if _, err := i.Run(context.Background(), "CLOSING", day); err != nil {
		t.Fatal(err)
	}
	i.provider = &fakeProvider{points: points()}
	if _, err := i.Run(context.Background(), "CLOSING", day); err != nil {
		t.Fatal(err)
	}
	if body := scrape(i); !strings.Contains(body, `fx_provider_degraded{provider="ecb",rate_type="CLOSING"} 0`) {
		t.Fatalf("metrics = %s", body)
	}
}

func TestRunAlreadySucceededSkipsFetchAndResetsGauge(t *testing.T) {
	last := day
	s := &fakeStore{last: &last}
	i, _ := harness(&fakeProvider{errs: downs(4, errors.New("down"))}, s)
	if _, err := i.Run(context.Background(), "CLOSING", day); err != nil {
		t.Fatal(err)
	}
	s.done = true
	p := &fakeProvider{}
	i.provider = p
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || !res.AlreadyDone || res.Imported != 7 || p.calls != 0 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, p.calls)
	}
	if body := scrape(i); !strings.Contains(body, `fx_provider_degraded{provider="ecb",rate_type="CLOSING"} 0`) {
		t.Fatalf("metrics = %s", body)
	}
}

func TestRunOutageWithoutLastKnownRateIsAClearError(t *testing.T) {
	down := errors.New("down")
	p, s := &fakeProvider{errs: downs(4, down)}, &fakeStore{}
	i, _ := harness(p, s)
	_, err := i.Run(context.Background(), "CLOSING", day)
	if !errors.Is(err, ErrNoLastKnownRate) || !strings.Contains(err.Error(), "down") || s.outcome() != "failed" || !errors.Is(s.finishes[0].cause, ErrNoLastKnownRate) {
		t.Fatalf("err = %v finishes=%+v", err, s.finishes)
	}
	if body := scrape(i); !strings.Contains(body, `fx_provider_degraded{provider="ecb",rate_type="CLOSING"} 1`) {
		t.Fatalf("metrics = %s", body)
	}
}

func TestRunDoesNotRetryPermanentFailures(t *testing.T) {
	for name, err := range map[string]error{
		"malformed": fmt.Errorf("%w: bad xml", provider.ErrMalformed),
		"404":       fmt.Errorf("ecb: %w", &provider.StatusError{Code: 404}),
		"401":       fmt.Errorf("oxr: %w", &provider.StatusError{Code: 401}),
	} {
		t.Run(name, func(t *testing.T) {
			last := day
			p, s := &fakeProvider{errs: []error{err}}, &fakeStore{last: &last}
			i, slept := harness(p, s)
			res, rerr := i.Run(context.Background(), "CLOSING", day)
			if rerr != nil || !res.Degraded || p.calls != 1 || len(*slept) != 0 || s.finishes[0].retryable {
				t.Fatalf("res=%+v err=%v calls=%d slept=%v", res, rerr, p.calls, *slept)
			}
		})
	}
}

func TestRunDisabledProviderAbortsWithoutDegradedOutcome(t *testing.T) {
	last := day
	p, s := &fakeProvider{errs: []error{provider.ErrDisabled}}, &fakeStore{last: &last}
	i, _ := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if !errors.Is(err, provider.ErrDisabled) || res.Degraded || p.calls != 1 || s.outcome() != "aborted" || !strings.HasPrefix(s.finishes[0].cause.Error(), "aborted: ") {
		t.Fatalf("res=%+v err=%v finishes=%+v", res, err, s.finishes)
	}
	if body := scrape(i); strings.Contains(body, "fx_provider_degraded{") {
		t.Fatalf("metrics = %s", body)
	}
}

func TestRunDeadlineExceededIsAnOutageNotAnAbort(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	last := day
	p, s := &fakeProvider{errs: downs(4, context.DeadlineExceeded)}, &fakeStore{last: &last}
	i, _ := harness(p, s)
	res, err := i.Run(ctx, "CLOSING", day)
	if err != nil || !res.Degraded || s.outcome() != store.RunDegraded || !s.finishes[0].retryable {
		t.Fatalf("res=%+v err=%v finishes=%+v", res, err, s.finishes)
	}
}

func TestRunRetriesRateLimit(t *testing.T) {
	p, s := &fakeProvider{errs: []error{fmt.Errorf("oxr: %w", &provider.StatusError{Code: 429})}, points: points()}, &fakeStore{}
	i, _ := harness(p, s)
	if res, err := i.Run(context.Background(), "CLOSING", day); err != nil || res.Attempts != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestRunCancelledContextAbortsWithoutDegradedOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	last := day
	s := &fakeStore{last: &last}
	p := &fakeProvider{errs: downs(4, errors.New("down"))}
	i, _ := harness(p, s)
	i.sleep = func(context.Context, time.Duration) error {
		cancel()
		return ctx.Err()
	}
	res, err := i.Run(ctx, "CLOSING", day)
	if !errors.Is(err, context.Canceled) || res.Degraded || p.calls != 1 || s.outcome() != "aborted" {
		t.Fatalf("res=%+v err=%v calls=%d finishes=%+v", res, err, p.calls, s.finishes)
	}
	if body := scrape(i); strings.Contains(body, "fx_provider_degraded{") {
		t.Fatalf("metrics changed on cancellation: %s", body)
	}
}

func TestRunRecordAttemptErrorLeavesRunRunningAndMetricsUntouched(t *testing.T) {
	boom := errors.New("db down")
	last := day
	p, s := &fakeProvider{}, &fakeStore{last: &last, attemptErr: boom}
	i, _ := harness(p, s)
	_, err := i.Run(context.Background(), "CLOSING", day)
	if !errors.Is(err, boom) || len(s.finishes) != 0 || p.calls != 0 {
		t.Fatalf("err=%v finishes=%+v calls=%d", err, s.finishes, p.calls)
	}
	if body := scrape(i); strings.Contains(body, "fx_provider_degraded{") {
		t.Fatalf("metrics = %s", body)
	}
}

func TestRunPostRatesErrorAbortsWithoutDegradedOutcome(t *testing.T) {
	boom := errors.New("constraint")
	p, s := &fakeProvider{points: points()}, &fakeStore{postErr: boom}
	i, _ := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if !errors.Is(err, boom) || len(s.finishes) != 1 || s.finishes[0].kind != "aborted" || s.finishes[0].attempts != 1 || !strings.HasPrefix(s.finishes[0].cause.Error(), "aborted: post ecb rates") || res.Degraded {
		t.Fatalf("err=%v finishes=%+v", err, s.finishes)
	}
	if body := scrape(i); strings.Contains(body, "fx_provider_degraded{") {
		t.Fatalf("metrics = %s", body)
	}
}

func TestRunLastSuccessLookupErrorIsReturned(t *testing.T) {
	boom := errors.New("db down")
	s := &fakeStore{lastErr: boom}
	i, _ := harness(&fakeProvider{errs: downs(4, errors.New("down"))}, s)
	if _, err := i.Run(context.Background(), "CLOSING", day); !errors.Is(err, boom) || len(s.finishes) != 0 {
		t.Fatalf("err=%v finishes=%+v", err, s.finishes)
	}
}

func TestRunFinishDegradedErrorIsReturned(t *testing.T) {
	boom := errors.New("tx failed")
	last := day
	s := &fakeStore{last: &last, degradedErr: boom}
	i, _ := harness(&fakeProvider{errs: downs(4, errors.New("down"))}, s)
	if _, err := i.Run(context.Background(), "CLOSING", day); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	if body := scrape(i); strings.Contains(body, "fx_provider_degraded{") {
		t.Fatalf("metrics = %s", body)
	}
}

func TestNewReplacesNonPositiveConfigWithDefaults(t *testing.T) {
	p := &fakeProvider{errs: downs(10, errors.New("down"))}
	s := &fakeStore{}
	i := New(p, s, Config{}, nil)
	i.sleep = func(context.Context, time.Duration) error { return nil }
	if i.cfg != DefaultConfig {
		t.Fatalf("cfg = %+v", i.cfg)
	}
	if _, err := i.Run(context.Background(), "CLOSING", day); !errors.Is(err, ErrNoLastKnownRate) || p.calls != DefaultConfig.MaxAttempts {
		t.Fatalf("err=%v calls=%d", err, p.calls)
	}
}

func TestSleepContextReturnsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
	if err := sleepContext(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsAggregateWorstStateAcrossTenantsAndEscapeLabels(t *testing.T) {
	m := NewMetrics()
	a := tenant.With(context.Background(), tenant.Principal{TenantID: "t1"})
	b := tenant.With(context.Background(), tenant.Principal{TenantID: "t2"})
	older, newer := day, day.Add(time.Hour)
	m.record(a, "ecb", "CLOSING", true, &older)
	m.record(b, "ecb", "CLOSING", false, &newer)
	m.record(b, "ecb", "CLOSING", false, nil)
	m.record(a, "we\"ird\\", "A\nB", false, nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`fx_provider_degraded{provider="ecb",rate_type="CLOSING"} 1`,
		`fx_provider_degraded_runs_total{provider="ecb",rate_type="CLOSING"} 1`,
		fmt.Sprintf(`fx_provider_last_success_timestamp_seconds{provider="ecb",rate_type="CLOSING"} %d`, older.Unix()),
		`{provider="we\"ird\\",rate_type="A\nB"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
}

func TestRunReportsInProgressWhenAnotherRunHoldsTheDay(t *testing.T) {
	s := &fakeStore{done: true, status: store.RunRunning}
	p := &fakeProvider{points: points()}
	i, _ := harness(p, s)
	res, err := i.Run(context.Background(), "CLOSING", day)
	if err != nil || !res.InProgress || res.AlreadyDone || p.calls != 0 || len(s.finishes) != 0 {
		t.Fatalf("res=%+v err=%v calls=%d finishes=%v", res, err, p.calls, s.finishes)
	}
}

func TestRunFailsWhenHistoryProviderReturnsAnotherDate(t *testing.T) {
	stale := points()
	stale[1].ValidFrom = day.AddDate(0, 0, -1)
	s := &fakeStore{}
	i, _ := harness(&fakeProvider{points: stale, history: true}, s)
	_, err := i.Run(context.Background(), "CLOSING", day.Add(5*time.Hour))
	if !errors.Is(err, ErrDateMismatch) || s.outcome() != "failed" || len(s.posted) != 0 {
		t.Fatalf("err=%v outcome=%q posted=%d", err, s.outcome(), len(s.posted))
	}
}

func TestRunAcceptsMatchingDateForHistoryProvider(t *testing.T) {
	s := &fakeStore{}
	i, _ := harness(&fakeProvider{points: points(), history: true}, s)
	if _, err := i.Run(context.Background(), "CLOSING", day.Add(5*time.Hour)); err != nil || s.outcome() != store.RunSucceeded {
		t.Fatalf("err=%v outcome=%q", err, s.outcome())
	}
}

func TestRunIgnoresCubeDateForProvidersWithoutHistory(t *testing.T) {
	stale := points()
	stale[0].ValidFrom = day.AddDate(0, 0, -3)
	s := &fakeStore{}
	i, _ := harness(&fakeProvider{points: stale}, s)
	if _, err := i.Run(context.Background(), "CLOSING", day); err != nil || s.outcome() != store.RunSucceeded {
		t.Fatalf("err=%v outcome=%q", err, s.outcome())
	}
}
