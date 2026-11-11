package failover

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

type calls struct {
	mu   sync.Mutex
	log  []string
	fail map[string]error
}

func (c *calls) hit(name, region string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, name+":"+region)
	return c.fail[name]
}

type fakeFence struct{ c *calls }

func (f fakeFence) FenceRegion(_ context.Context, r string) error { return f.c.hit(ActionFence, r) }

type fakePromote struct{ c *calls }

func (f fakePromote) Promote(_ context.Context, r string) error { return f.c.hit(ActionPromote, r) }

type fakeMirror struct{ c *calls }

func (f fakeMirror) StopInto(_ context.Context, r string) error { return f.c.hit(ActionMirror, r) }

type fakeTemporal struct{ c *calls }

func (f fakeTemporal) ActivateIn(_ context.Context, r string) error {
	return f.c.hit(ActionTemporal, r)
}

type fakeDNS struct{ c *calls }

func (f fakeDNS) Flip(_ context.Context, r string) error { return f.c.hit(ActionDNS, r) }

type fakeChain struct{ c *calls }

func (f fakeChain) Verify(_ context.Context, r string) error { return f.c.hit(ActionChain, r) }

func fakes(c *calls) Executors {
	return Executors{fakeFence{c}, fakePromote{c}, fakeMirror{c}, fakeTemporal{c}, fakeDNS{c}, fakeChain{c}}
}

func plan(t *testing.T) domain.Plan {
	t.Helper()
	p, err := domain.PlanFailover([]domain.Region{{Name: "eu-west", Role: domain.Primary}, {Name: "eu-central", Role: domain.Passive}},
		domain.FailoverRequest{To: "eu-central", MaxLagSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunExecutesEveryStepInOrderAndReportsEach(t *testing.T) {
	c := &calls{}
	var reported []string
	r := Runner{Exec: fakes(c), Report: func(_ context.Context, res StepResult) error {
		reported = append(reported, res.Action+"="+res.Status)
		return nil
	}}
	res, err := r.Run(t.Context(), plan(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "fence-old-primary:eu-west promote-cnpg:eu-central switch-mirrormaker:eu-central activate-temporal:eu-central flip-dns:eu-central verify-ledger-hash-chain:eu-central"
	if got := strings.Join(c.log, " "); got != want {
		t.Fatalf("order\n got %s\nwant %s", got, want)
	}
	if len(res) != 6 || len(reported) != 6 || res[5].Status != StatusDone {
		t.Fatalf("results %+v reported %v", res, reported)
	}
}

func TestRunStopsAtTheFirstFailureAndResumes(t *testing.T) {
	c := &calls{fail: map[string]error{ActionMirror: errors.New("kube down")}}
	r := Runner{Exec: fakes(c)}
	res, err := r.Run(t.Context(), plan(t), nil)
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("want ErrStepFailed, got %v", err)
	}
	if len(res) != 3 || res[2].Status != StatusFailed || !strings.Contains(res[2].Detail, "kube down") {
		t.Fatalf("results %+v", res)
	}
	if len(c.log) != 3 {
		t.Fatalf("steps after the failure ran: %v", c.log)
	}
	// resume: fence and promote are done, the mirror now works
	c2 := &calls{}
	res, err = Runner{Exec: fakes(c2)}.Run(t.Context(), plan(t), map[string]bool{ActionFence: true, ActionPromote: true})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != StatusSkipped || res[1].Status != StatusSkipped || len(c2.log) != 4 || c2.log[0] != "switch-mirrormaker:eu-central" {
		t.Fatalf("resume: %+v %v", res, c2.log)
	}
}

func TestDryRunCallsNoPortAndReportsNothing(t *testing.T) {
	c := &calls{}
	reported := 0
	r := Runner{Exec: fakes(c), DryRun: true, Report: func(context.Context, StepResult) error { reported++; return nil }}
	res, err := r.Run(t.Context(), plan(t), nil)
	if err != nil || len(c.log) != 0 || reported != 0 {
		t.Fatalf("err=%v calls=%v reported=%d", err, c.log, reported)
	}
	for _, s := range res {
		if s.Status != StatusDryRun || s.Detail == "" {
			t.Fatalf("step %+v", s)
		}
	}
	// a dry run needs no ports at all
	if _, err := (Runner{DryRun: true}).Run(t.Context(), plan(t), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runner{}).Run(t.Context(), plan(t), nil); err == nil {
		t.Fatal("a real run without ports must refuse")
	}
}

func TestReportFailureStopsTheRun(t *testing.T) {
	c := &calls{}
	r := Runner{Exec: fakes(c), Report: func(context.Context, StepResult) error { return errors.New("ops api down") }}
	if _, err := r.Run(t.Context(), plan(t), nil); err == nil || len(c.log) != 1 {
		t.Fatalf("err=%v calls=%v", err, c.log)
	}
}

type kubeFake struct {
	mu       sync.Mutex
	requests []string
	bodies   []string
	gets     int
}

func (k *kubeFake) handler(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	k.requests = append(k.requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type")+" "+r.Header.Get("Authorization"))
	k.bodies = append(k.bodies, string(b))
	switch r.Method {
	case http.MethodGet:
		k.gets++
		phase := "Cluster in healthy state"
		if k.gets < 2 {
			phase = "Waiting for the instances to become active"
		}
		_, _ = w.Write([]byte(`{"spec":{},"status":{"phase":"` + phase + `","currentPrimary":"passage-pg-1"}}`))
	case http.MethodDelete:
		w.WriteHeader(http.StatusNotFound)
	default:
		_, _ = w.Write([]byte(`{}`))
	}
}

func TestCNPGFenceAndPromoteThroughTheKubeAPI(t *testing.T) {
	kf := &kubeFake{}
	srv := httptest.NewServer(http.HandlerFunc(kf.handler))
	defer srv.Close()
	kube := &Kube{Servers: map[string]KubeServer{"eu-west": {Server: srv.URL, Token: "tok"}, "eu-central": {Server: srv.URL, Token: "tok"}}}
	c := CNPG{Kube: kube, Namespace: "passage-data", Cluster: "passage-pg", PollEvery: time.Millisecond, Timeout: time.Second}
	if err := c.FenceRegion(t.Context(), "eu-west"); err != nil {
		t.Fatal(err)
	}
	if err := c.Promote(t.Context(), "eu-central"); err != nil {
		t.Fatal(err)
	}
	if kf.requests[0] != "PATCH /apis/postgresql.cnpg.io/v1/namespaces/passage-data/clusters/passage-pg application/merge-patch+json Bearer tok" {
		t.Fatalf("fence request %q", kf.requests[0])
	}
	var fence map[string]map[string]map[string]any
	if err := json.Unmarshal([]byte(kf.bodies[0]), &fence); err != nil || fence["metadata"]["annotations"]["cnpg.io/fencedInstances"] != `["*"]` {
		t.Fatalf("fence body %s", kf.bodies[0])
	}
	if !strings.Contains(kf.bodies[1], `"replica":null`) || !strings.Contains(kf.bodies[1], `"cnpg.io/fencedInstances":null`) {
		t.Fatalf("promote body %s", kf.bodies[1])
	}
	if kf.gets < 2 {
		t.Fatalf("promote must wait for a healthy primary, polled %d times", kf.gets)
	}
	if err := (CNPG{Kube: kube, Namespace: "n", Cluster: "c"}).FenceRegion(t.Context(), "nowhere"); err == nil {
		t.Fatal("an unconfigured region must fail")
	}
}

func TestPromoteTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"spec":{"replica":{"enabled":true}},"status":{"phase":"Setting up primary"}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := CNPG{Kube: &Kube{Servers: map[string]KubeServer{"r": {Server: srv.URL}}}, Namespace: "n", Cluster: "c", PollEvery: time.Millisecond, Timeout: 20 * time.Millisecond}
	if err := c.Promote(t.Context(), "r"); err == nil || !strings.Contains(err.Error(), "not a healthy primary") {
		t.Fatalf("err %v", err)
	}
}

func TestMirrorMakerDeleteIsIdempotent(t *testing.T) {
	kf := &kubeFake{}
	srv := httptest.NewServer(http.HandlerFunc(kf.handler)) // answers 404 to DELETE
	defer srv.Close()
	m := MirrorMaker{Kube: &Kube{Servers: map[string]KubeServer{"eu-central": {Server: srv.URL}}}, Namespace: "kafka"}
	if err := m.StopInto(t.Context(), "eu-central"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(kf.requests[0], "DELETE /apis/kafka.strimzi.io/v1beta2/namespaces/kafka/kafkamirrormaker2s/passage-mm2") {
		t.Fatalf("request %q", kf.requests[0])
	}
}

func TestKubeErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	}))
	defer srv.Close()
	err := (CNPG{Kube: &Kube{Servers: map[string]KubeServer{"r": {Server: srv.URL}}}, Namespace: "n", Cluster: "c"}).FenceRegion(t.Context(), "r")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err %v", err)
	}
}

func TestTemporalCLIUpdatesEveryNamespace(t *testing.T) {
	var got []string
	tc := TemporalCLI{Address: "temporal:7233", Namespaces: []string{"passage", "passage-ops"}, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append(got, name+" "+strings.Join(args, " "))
		return nil, nil
	}}
	if err := tc.ActivateIn(t.Context(), "eu-central"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "temporal operator namespace update --namespace passage --active-cluster eu-central --yes --address temporal:7233" {
		t.Fatalf("commands %v", got)
	}
	failing := TemporalCLI{Namespaces: []string{"x"}, Run: func(context.Context, string, ...string) ([]byte, error) { return []byte("boom"), errors.New("exit 1") }}
	if err := failing.ActivateIn(t.Context(), "r"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	if err := (TemporalCLI{Run: ExecRunner}).ActivateIn(t.Context(), "r"); err == nil {
		t.Fatal("no namespaces configured must fail")
	}
}

func TestDNSWebhook(t *testing.T) {
	var body map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
	}))
	defer srv.Close()
	d := DNSWebhook{URL: srv.URL, Token: "s3", Record: "app.passage.example", TTL: 60}
	if err := d.Flip(t.Context(), "eu-central"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer s3" || body["target"] != "eu-central" || body["record"] != "app.passage.example" || body["ttlSeconds"] != float64(60) || body["weight"] != float64(100) {
		t.Fatalf("auth %q body %v", auth, body)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer bad.Close()
	if err := (DNSWebhook{URL: bad.URL}).Flip(t.Context(), "r"); err == nil {
		t.Fatal("a 502 must fail")
	}
	if err := (DNSWebhook{}).Flip(t.Context(), "r"); err == nil {
		t.Fatal("no URL must fail")
	}
}

func TestCommandVerifier(t *testing.T) {
	var got []string
	v := CommandVerifier{Argv: []string{"python3", "baseline.py", "verify", "--region", "{region}"}, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return nil, nil
	}}
	if err := v.Verify(t.Context(), "eu-central"); err != nil || strings.Join(got, " ") != "python3 baseline.py verify --region eu-central" {
		t.Fatalf("err=%v got=%v", err, got)
	}
	broken := CommandVerifier{Argv: []string{"x"}, Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("break at journal 7"), errors.New("exit 2")
	}}
	if err := broken.Verify(t.Context(), "r"); err == nil || !strings.Contains(err.Error(), "OPS-1704") || !strings.Contains(err.Error(), "journal 7") {
		t.Fatalf("err %v", err)
	}
	if err := (CommandVerifier{}).Verify(t.Context(), "r"); err == nil {
		t.Fatal("no command must fail")
	}
}
