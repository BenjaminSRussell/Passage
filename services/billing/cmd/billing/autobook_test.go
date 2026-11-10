package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

type recordingBooks struct {
	mu    sync.Mutex
	runs  []tenant.Principal
	fails map[string]error
}

func (r *recordingBooks) AutoBook(ctx context.Context, le string) (int, int, error) {
	p, _ := tenant.FromContext(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, p)
	return 1, 0, r.fails[p.TenantID+le]
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestAutoBook_RunsEveryTenantAndEntityUnderAScopedPrincipalAndSurvivesFailures(t *testing.T) {
	tenantA, tenantB, leA, leB := ids.New(), ids.New(), ids.New(), ids.New()
	profiles := map[string]store.GLProfile{leA: {}, leB: {}}
	books := &recordingBooks{fails: map[string]error{tenantA + leA: errors.New("ledger down")}}
	w, err := newAutoBook(env(map[string]string{"BILLING_GL_AUTOBOOK_TENANT_IDS": tenantA + "," + tenantB + "," + tenantA}), profiles, books)
	if err != nil || w == nil {
		t.Fatalf("newAutoBook: %v %v", w, err)
	}
	w.once(context.Background())
	if len(books.runs) != 4 {
		t.Fatalf("%d runs, want tenants x entities = 4 (a failing run must not stop the rest)", len(books.runs))
	}
	for _, p := range books.runs {
		if p.LegalEntityID == "" || p.UserID != autoBookActor || p.TenantID == "" {
			t.Errorf("principal %+v is not scoped to one tenant and legal entity", p)
		}
	}
}

func TestAutoBook_Config(t *testing.T) {
	le := ids.New()
	profiles := map[string]store.GLProfile{le: {}}
	good := ids.New()
	if w, err := newAutoBook(env(nil), profiles, nil); w != nil || err != nil {
		t.Errorf("unset tenants: %v %v, want disabled", w, err)
	}
	cases := map[string]map[string]string{
		"bad tenant":   {"BILLING_GL_AUTOBOOK_TENANT_IDS": "nope"},
		"nil tenant":   {"BILLING_GL_AUTOBOOK_TENANT_IDS": ids.Nil},
		"short period": {"BILLING_GL_AUTOBOOK_TENANT_IDS": good, "BILLING_GL_AUTOBOOK_INTERVAL": "1s"},
		"bad period":   {"BILLING_GL_AUTOBOOK_TENANT_IDS": good, "BILLING_GL_AUTOBOOK_INTERVAL": "soon"},
	}
	for name, e := range cases {
		if _, err := newAutoBook(env(e), profiles, nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := newAutoBook(env(map[string]string{"BILLING_GL_AUTOBOOK_TENANT_IDS": good}), nil, nil); err == nil {
		t.Error("auto-book without GL profiles accepted")
	}
	w, err := newAutoBook(env(map[string]string{"BILLING_GL_AUTOBOOK_TENANT_IDS": good, "BILLING_GL_AUTOBOOK_INTERVAL": "30s"}), profiles, nil)
	if err != nil || w.interval != 30*time.Second {
		t.Errorf("interval: %v %v", w, err)
	}
}

func TestAutoBook_LoopRunsImmediatelyAndStopsWithItsContext(t *testing.T) {
	books := &recordingBooks{}
	w := &autoBook{interval: time.Hour, tenants: []string{ids.New()}, entities: []string{ids.New()}, books: books}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.loop(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		books.mu.Lock()
		n := len(books.runs)
		books.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("loop did not run at start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}
}
