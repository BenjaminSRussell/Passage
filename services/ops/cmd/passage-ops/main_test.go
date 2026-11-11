package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/failover"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

type fakeAPI struct {
	mu       sync.Mutex
	regions  []domain.Region
	lag      int
	steps    map[string]store.Step
	events   map[string]domain.Plan
	requests []string
	auth     string
	pending  bool
}

func newFakeAPI(lag int) *fakeAPI {
	return &fakeAPI{regions: []domain.Region{{Name: "eu-west", Role: domain.Primary}, {Name: "eu-central", Role: domain.Passive, LagSeconds: lag}},
		steps: map[string]store.Step{}, events: map[string]domain.Plan{}}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	write := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	problem := func(status int, code, detail string) {
		write(status, map[string]any{"code": code, "detail": detail, "status": status})
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/ops/regions":
		write(200, map[string]any{"items": f.regions, "maxLagSeconds": 300})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/ops/failover":
		var body struct {
			To             string `json:"to"`
			AcceptDataLoss bool   `json:"acceptDataLoss"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		key := r.Header.Get("Idempotency-Key")
		if _, ok := f.events[key]; ok {
			write(200, store.Failover{EventID: key, Plan: f.events[key]})
			return
		}
		plan, err := domain.PlanFailover(f.regions, domain.FailoverRequest{To: body.To, AcceptDataLoss: body.AcceptDataLoss, Caller: "a", Approver: "b", MaxLagSeconds: 300})
		if f.pending {
			write(202, map[string]any{"request": store.Request{ID: "req-1", To: body.To, LagSeconds: f.lag}})
			return
		}
		if err != nil {
			problem(422, "OPS-1701", err.Error())
			return
		}
		f.events[key] = plan
		write(201, store.Failover{EventID: key, Plan: plan})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/ops/failover-requests/req-1:approve":
		plan, _ := domain.PlanFailover(f.regions, domain.FailoverRequest{To: "eu-central", AcceptDataLoss: true, Caller: "a", Approver: "b", MaxLagSeconds: 300})
		f.events["evt-approved"] = plan
		write(201, store.Failover{EventID: "evt-approved", Plan: plan})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/ops/failovers/"):
		key := strings.TrimPrefix(r.URL.Path, "/v1/ops/failovers/")
		plan, ok := f.events[key]
		if !ok {
			problem(404, "OPS-1713", "no such failover")
			return
		}
		d := store.FailoverDetail{Failover: store.Failover{EventID: key, Plan: plan}, Steps: []store.Step{}}
		for _, s := range plan.Steps {
			if st, ok := f.steps[key+"/"+s.Action]; ok {
				d.Steps = append(d.Steps, st)
			}
		}
		write(200, d)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/steps/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/ops/failovers/"), "/steps/")
		var st store.Step
		_ = json.NewDecoder(r.Body).Decode(&st)
		st.Action = parts[1]
		f.steps[parts[0]+"/"+parts[1]] = st
		write(200, map[string]any{})
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/lag"):
		var body struct {
			LagSeconds int `json:"lagSeconds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lag = body.LagSeconds
		write(200, map[string]any{})
	case r.URL.Path == "/v1/ops/break-glass" && r.Method == http.MethodPost:
		write(201, store.BreakGlass{ID: "g1", ExpiresAt: time.Now().Add(time.Hour)})
	case r.URL.Path == "/v1/ops/break-glass/g1:review":
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		write(200, store.BreakGlass{ID: "g1", ReviewOutcome: b["outcome"]})
	default:
		problem(404, "COM-0404", "no route "+r.URL.Path)
	}
}

type recorder struct {
	mu   sync.Mutex
	log  []string
	fail string
}

func (r *recorder) hit(action, region string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, action+":"+region)
	if r.fail == action {
		return errors.New("boom")
	}
	return nil
}

type rf struct{ r *recorder }

func (x rf) FenceRegion(_ context.Context, g string) error { return x.r.hit(failover.ActionFence, g) }
func (x rf) Promote(_ context.Context, g string) error     { return x.r.hit(failover.ActionPromote, g) }
func (x rf) StopInto(_ context.Context, g string) error    { return x.r.hit(failover.ActionMirror, g) }
func (x rf) ActivateIn(_ context.Context, g string) error  { return x.r.hit(failover.ActionTemporal, g) }
func (x rf) Flip(_ context.Context, g string) error        { return x.r.hit(failover.ActionDNS, g) }
func (x rf) Verify(_ context.Context, g string) error      { return x.r.hit(failover.ActionChain, g) }

func testDeps(r *recorder) deps {
	return deps{executors: func(func(string) string) (failover.Executors, error) {
		x := rf{r}
		return failover.Executors{Fence: x, Promote: x, Mirror: x, Temporal: x, DNS: x, Chain: x}, nil
	}}
}

func env(srv *httptest.Server) func(string) string {
	return func(k string) string {
		switch k {
		case "OPS_API_URL":
			return srv.URL
		case "OPS_TOKEN":
			return "tok"
		}
		return ""
	}
}

func TestFailoverRecordsThenExecutesAndReportsEveryStep(t *testing.T) {
	api := newFakeAPI(2)
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"failover", "--to", "eu-central", "--event", "evt-1"}, &out, env(srv), testDeps(rec)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(rec.log) != 6 || rec.log[0] != "fence-old-primary:eu-west" || rec.log[1] != "promote-cnpg:eu-central" {
		t.Fatalf("executors %v", rec.log)
	}
	if len(api.steps) != 6 || api.steps["evt-1/flip-dns"].Status != "DONE" {
		t.Fatalf("reported steps %+v", api.steps)
	}
	if api.auth != "Bearer tok" || !strings.Contains(out.String(), "failover complete") {
		t.Fatalf("auth %q out %s", api.auth, out.String())
	}
}

func TestFailoverStopsOnAFailedStepAndResumes(t *testing.T) {
	api := newFakeAPI(2)
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{fail: failover.ActionMirror}
	var out bytes.Buffer
	err := run(t.Context(), []string{"failover", "--to", "eu-central", "--event", "evt-2"}, &out, env(srv), testDeps(rec))
	if err == nil || !strings.Contains(out.String(), "failover resume --event evt-2") {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
	if api.steps["evt-2/switch-mirrormaker"].Status != "FAILED" || len(rec.log) != 3 {
		t.Fatalf("steps %+v log %v", api.steps, rec.log)
	}
	rec2 := &recorder{}
	out.Reset()
	if err := run(t.Context(), []string{"failover", "resume", "--event", "evt-2"}, &out, env(srv), testDeps(rec2)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(rec2.log) != 4 || rec2.log[0] != "switch-mirrormaker:eu-central" {
		t.Fatalf("resume ran %v (fence and promote were DONE)", rec2.log)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	api := newFakeAPI(2)
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"failover", "--to", "eu-central", "--dry-run"}, &out, env(srv), testDeps(rec)); err != nil {
		t.Fatal(err)
	}
	if len(rec.log) != 0 || len(api.events) != 0 {
		t.Fatalf("a dry run touched something: %v %v", rec.log, api.events)
	}
	for _, want := range []string{"fence-old-primary", "DRY_RUN", "flip DNS to eu-central", "eu-west -> eu-central"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in\n%s", want, out.String())
		}
	}
	// the dry run refuses what the real call would: a lag over the limit without --accept-data-loss
	api2 := newFakeAPI(900)
	srv2 := httptest.NewServer(api2)
	defer srv2.Close()
	if err := run(t.Context(), []string{"failover", "--to", "eu-central", "--dry-run"}, &out, env(srv2), testDeps(rec)); err == nil || !strings.Contains(err.Error(), "OPS-1701") {
		t.Fatalf("err %v", err)
	}
}

func TestDataLossBecomesARequestAndApproveExecutes(t *testing.T) {
	api := newFakeAPI(900)
	api.pending = true
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"failover", "--to", "eu-central", "--accept-data-loss", "--event", "evt-3"}, &out, env(srv), testDeps(rec)); err != nil {
		t.Fatal(err)
	}
	if len(rec.log) != 0 || !strings.Contains(out.String(), "failover approve --request req-1") {
		t.Fatalf("nothing may run before the approval: %v\n%s", rec.log, out.String())
	}
	out.Reset()
	if err := run(t.Context(), []string{"failover", "approve", "--request", "req-1"}, &out, env(srv), testDeps(rec)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(rec.log) != 6 {
		t.Fatalf("approve must execute: %v", rec.log)
	}
}

func TestUsageErrors(t *testing.T) {
	srv := httptest.NewServer(newFakeAPI(0))
	defer srv.Close()
	for _, args := range [][]string{{}, {"nope"}, {"failover"}, {"failover", "approve"}, {"failover", "resume"}, {"lag-report"}, {"break-glass"}} {
		if err := run(t.Context(), args, &bytes.Buffer{}, env(srv), testDeps(&recorder{})); err == nil {
			t.Errorf("%v must fail", args)
		}
	}
	if err := run(t.Context(), []string{"failover", "--to", "x"}, &bytes.Buffer{}, func(string) string { return "" }, testDeps(&recorder{})); err == nil || !strings.Contains(err.Error(), "OPS_API_URL") {
		t.Fatalf("err %v", err)
	}
}

func TestBreakGlassCommands(t *testing.T) {
	srv := httptest.NewServer(newFakeAPI(0))
	defer srv.Close()
	var out bytes.Buffer
	if err := run(t.Context(), []string{"break-glass", "open", "--reason", "REGION_LOSS", "--ticket", "INC-1"}, &out, env(srv), testDeps(&recorder{})); err != nil || !strings.Contains(out.String(), "break-glass window g1 open") {
		t.Fatalf("%v %s", err, out.String())
	}
	out.Reset()
	if err := run(t.Context(), []string{"break-glass", "review", "--id", "g1", "--outcome", "APPROVED"}, &out, env(srv), testDeps(&recorder{})); err != nil || !strings.Contains(out.String(), "reviewed: APPROVED") {
		t.Fatalf("%v %s", err, out.String())
	}
}

func TestExecutorsFromEnv(t *testing.T) {
	env := map[string]string{
		"OPS_KUBE_REGIONS":        `{"eu-west":{"server":"https://k8s.west"}}`,
		"OPS_TEMPORAL_NAMESPACES": "a, b",
		"OPS_CHAIN_VERIFY_CMD":    `["python3","baseline.py","{region}"]`,
		"DNS_TTL_S":               "30",
	}
	e, err := executorsFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if tc := e.Temporal.(failover.TemporalCLI); len(tc.Namespaces) != 2 {
		t.Fatalf("namespaces %v", tc.Namespaces)
	}
	if v := e.Chain.(failover.CommandVerifier); len(v.Argv) != 3 {
		t.Fatalf("argv %v", v.Argv)
	}
	if e.DNS.(failover.DNSWebhook).TTL != 30 {
		t.Fatal("ttl")
	}
	if _, err := executorsFromEnv(func(string) string { return "" }); err == nil {
		t.Fatal("no kube regions must fail")
	}
	if _, err := executorsFromEnv(func(k string) string {
		if k == "OPS_KUBE_REGIONS" {
			return "not json"
		}
		return ""
	}); err == nil {
		t.Fatal("bad json must fail")
	}
}

type fakeLag struct {
	secs int
	err  error
}

func (f fakeLag) Lag(context.Context) (int, error) { return f.secs, f.err }
func (fakeLag) Close(context.Context) error        { return nil }

func TestLagReporterReportsAndFailsClosed(t *testing.T) {
	api := newFakeAPI(0)
	srv := httptest.NewServer(api)
	defer srv.Close()
	d := testDeps(&recorder{})
	d.lagSource = func(context.Context, func(string) string) (LagSource, error) { return fakeLag{secs: 12}, nil }
	var out bytes.Buffer
	if err := run(t.Context(), []string{"lag-report", "--region", "eu-central", "--once"}, &out, env(srv), d); err != nil || api.lag != 12 {
		t.Fatalf("err=%v lag=%d", err, api.lag)
	}
	// a failed measurement reports the unknown-lag value, never the last good number or zero
	d.lagSource = func(context.Context, func(string) string) (LagSource, error) {
		return fakeLag{err: errors.New("no standby")}, nil
	}
	if err := run(t.Context(), []string{"lag-report", "--region", "eu-central", "--once", "--unknown-lag-s", "7200"}, &out, env(srv), d); err == nil || api.lag != 7200 {
		t.Fatalf("err=%v lag=%d", err, api.lag)
	}
}

func TestLagReporterLoopStopsOnContext(t *testing.T) {
	api := newFakeAPI(0)
	srv := httptest.NewServer(api)
	defer srv.Close()
	d := testDeps(&recorder{})
	d.lagSource = func(context.Context, func(string) string) (LagSource, error) { return fakeLag{secs: 3}, nil }
	ctx, cancel := context.WithCancel(t.Context())
	n := 0
	d.sleep = func(context.Context, time.Duration) error {
		n++
		if n == 3 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	var out bytes.Buffer
	if err := run(ctx, []string{"lag-report", "--region", "eu-central", "--interval", "1s"}, &out, env(srv), d); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "lag 3 s") != 3 {
		t.Fatalf("out %s", out.String())
	}
}

func TestRoundLag(t *testing.T) {
	for in, want := range map[float64]int{0: 0, 0.2: 1, 12: 12, -5: 0, 1e12: 1 << 30} {
		if got := roundLag(in); got != want {
			t.Errorf("roundLag(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestFenceUnreachableFlag(t *testing.T) {
	api := newFakeAPI(900)
	srv := httptest.NewServer(api)
	defer srv.Close()
	var out bytes.Buffer
	if err := run(t.Context(), []string{"failover", "--to", "eu-central", "--fence-unreachable", "region lost"}, &out, env(srv), testDeps(&recorder{})); err == nil || !strings.Contains(err.Error(), "--accept-data-loss") {
		t.Fatalf("the flag needs --accept-data-loss: %v", err)
	}
	// lag within the limit in the fake so the failover applies at once; the old region's fence fails
	api2 := newFakeAPI(2)
	srv2 := httptest.NewServer(api2)
	defer srv2.Close()
	rec := &recorder{fail: failover.ActionFence}
	out.Reset()
	if err := run(t.Context(), []string{"failover", "--to", "eu-central", "--accept-data-loss", "--fence-unreachable", "region lost, INC-9", "--event", "evt-lost"}, &out, env(srv2), testDeps(rec)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	st := api2.steps["evt-lost/fence-old-primary"]
	if !st.Forced || st.Status != "DONE" || !strings.Contains(st.Detail, "FENCE_UNREACHABLE: region lost, INC-9") || len(rec.log) != 6 {
		t.Fatalf("step %+v log %v", st, rec.log)
	}
	// without the flag the same failure stops at the fence
	rec2 := &recorder{fail: failover.ActionFence}
	if err := run(t.Context(), []string{"failover", "--to", "eu-central", "--event", "evt-x"}, &bytes.Buffer{}, env(srv2), testDeps(rec2)); err == nil || len(rec2.log) != 1 {
		t.Fatalf("err=%v log=%v", err, rec2.log)
	}
}
