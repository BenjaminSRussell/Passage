package inbox_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/batch/internal/inbox"
)

const (
	userID   = "00000000-0000-0000-0000-000000000008"
	otherID  = "00000000-0000-0000-0000-000000000007"
	entityID = "00000000-0000-0000-0000-000000000009"
	tenantID = "00000000-0000-0000-0000-000000000001"
)

var base = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type fakeSource struct {
	name, duty string
	items      []inbox.Item
	more       bool
	err        error
	block      bool
	calls      atomic.Int64
	gotEntity  string
	gotUser    string
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Duty() string { return f.duty }
func (f *fakeSource) Fetch(ctx context.Context, p tenant.Principal, le string) (inbox.Fetched, error) {
	f.calls.Add(1)
	f.gotEntity, f.gotUser = le, p.UserID
	if f.block {
		<-ctx.Done()
		return inbox.Fetched{}, ctx.Err()
	}
	return inbox.Fetched{Items: f.items, More: f.more}, f.err
}

func items(source string, offsets ...int) []inbox.Item {
	out := make([]inbox.Item, len(offsets))
	for i, o := range offsets {
		out[i] = inbox.Item{ID: fmt.Sprintf("%s-%d", source, o), Source: source, CreatedAt: base.Add(time.Duration(o) * time.Minute)}
	}
	return out
}

func principal(duties ...string) tenant.Principal {
	p := tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: userID, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return p
}

func ids(page inbox.Page) []string {
	out := make([]string, len(page.Items))
	for i, it := range page.Items {
		out[i] = it.ID
	}
	return out
}

func TestPagesMergeNewestFirstWithoutGapsOrRepeats(t *testing.T) {
	a := &fakeSource{name: "a", duty: "a.duty", items: items("a", 5, 3, 1)}
	b := &fakeSource{name: "b", duty: "b.duty", items: items("b", 5, 4, 2)}
	agg := &inbox.Aggregator{Sources: []inbox.Source{a, b}}
	p := principal("a.duty", "b.duty")

	var seen []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := agg.Page(context.Background(), p, entityID, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, ids(page)...)
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	want := []string{"a-5", "b-5", "b-4", "a-3", "b-2", "a-1"}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("paged order %v, want %v", seen, want)
	}
}

func TestSourceTheCallerHoldsNoDutyForIsNeverCalled(t *testing.T) {
	allowed := &fakeSource{name: "a", duty: "a.duty", items: items("a", 1)}
	denied := &fakeSource{name: "b", duty: "b.duty", items: items("b", 2)}
	agg := &inbox.Aggregator{Sources: []inbox.Source{allowed, denied}}

	page, err := agg.Page(context.Background(), principal("a.duty"), entityID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if denied.calls.Load() != 0 {
		t.Fatalf("source without the caller's duty was called %d times", denied.calls.Load())
	}
	if got := ids(page); len(got) != 1 || got[0] != "a-1" || len(page.Unavailable) != 0 {
		t.Fatalf("items %v unavailable %v", got, page.Unavailable)
	}
	if allowed.gotEntity != entityID || allowed.gotUser != userID {
		t.Fatalf("source saw entity %q user %q", allowed.gotEntity, allowed.gotUser)
	}
}

func TestSlowFailingAndUnsetSourcesDegradeToAPartialResult(t *testing.T) {
	slow := &fakeSource{name: "slow", duty: "d", block: true}
	broken := &fakeSource{name: "broken", duty: "d", err: errors.New("boom: secret detail")}
	unset := &fakeSource{name: "unset", duty: "d", err: inbox.ErrNotConfigured}
	good := &fakeSource{name: "good", duty: "d", items: items("good", 1), more: true}
	agg := &inbox.Aggregator{Sources: []inbox.Source{slow, broken, unset, good}, SourceTimeout: 50 * time.Millisecond}

	start := time.Now()
	page, err := agg.Page(context.Background(), principal("d"), entityID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the inbox waited %s for a slow source", time.Since(start))
	}
	if got := ids(page); len(got) != 1 || got[0] != "good-1" {
		t.Fatalf("items %v", got)
	}
	want := map[string]string{"slow": "did not answer in time", "broken": "could not be read right now", "unset": "is not set up"}
	if len(page.Unavailable) != len(want) {
		t.Fatalf("unavailable %v", page.Unavailable)
	}
	for _, u := range page.Unavailable {
		if want[u.Source] != u.Reason {
			t.Fatalf("source %s reason %q", u.Source, u.Reason)
		}
	}
	if len(page.Truncated) != 1 || page.Truncated[0] != "good" {
		t.Fatalf("truncated %v", page.Truncated)
	}
	if agg.Failures() != 3 {
		t.Fatalf("failures %d", agg.Failures())
	}
}

type gauge struct {
	fakeSource
	inFlight, peak *atomic.Int64
}

func (g *gauge) Fetch(ctx context.Context, p tenant.Principal, le string) (inbox.Fetched, error) {
	n := g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	for {
		old := g.peak.Load()
		if n <= old || g.peak.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	return inbox.Fetched{}, nil
}

func TestFanOutIsBounded(t *testing.T) {
	var inFlight, peak atomic.Int64
	var sources []inbox.Source
	for i := range 6 {
		sources = append(sources, &gauge{fakeSource: fakeSource{name: fmt.Sprint(i), duty: "d"}, inFlight: &inFlight, peak: &peak})
	}
	agg := &inbox.Aggregator{Sources: sources, Concurrency: 2}
	if _, err := agg.Page(context.Background(), principal("d"), entityID, 10, ""); err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 2 {
		t.Fatalf("%d sources ran at once with a bound of 2", peak.Load())
	}
}

func TestMalformedCursorIsRefused(t *testing.T) {
	agg := &inbox.Aggregator{Sources: []inbox.Source{&fakeSource{name: "a", duty: "d"}}}
	for _, cursor := range []string{"!!!", "bm90LWEtY3Vyc29y"} {
		if _, err := agg.Page(context.Background(), principal("d"), entityID, 10, cursor); !errors.Is(err, inbox.ErrBadCursor) {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
	}
}

func TestHTTPSourcesForwardTheCallersIdentityAndOnlyTheirNarrowDuty(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("batch", priv)
	if err != nil {
		t.Fatal(err)
	}
	type seen struct {
		claims tenant.ServiceClaims
		query  string
	}
	got := map[string]seen{}
	var mu sync.Mutex
	bodies := map[string]string{
		"purchasing": `{"items":[{"id":"r1","number":"REQ-1","nextApprover":"CATEGORY_OWNER","submittedAt":"2026-11-02T10:00:00Z"}],"nextCursor":"x"}`,
		"party": `{"items":[{"id":"c1","field":"credit_limit","requestedValue":"500.00","requestedBy":"` + otherID + `","requestedAt":"2026-11-02T09:00:00Z"},
			{"id":"c2","field":"credit_limit","requestedValue":"1.00","requestedBy":"` + userID + `","requestedAt":"2026-11-02T08:00:00Z"}]}`,
		"ledger": `{"items":[{"id":"b1","journalName":"GJ","description":"Accruals","submittedBy":"` + otherID + `","createdAt":"2026-11-02T07:00:00Z"},
			{"id":"b2","journalName":"GJ","description":"Mine","submittedBy":"` + userID + `","selfApprovalAllowed":false,"createdAt":"2026-11-02T06:00:00Z"},
			{"id":"b3","journalName":"GJ","description":"Mine allowed","submittedBy":"` + userID + `","selfApprovalAllowed":true,"createdAt":"2026-11-02T05:00:00Z"}]}`,
		"payables": `{"items":[{"id":"h1","invoiceNumber":"INV-9","dueDate":"2026-11-30"}]}`,
	}
	servers := map[string]*httptest.Server{}
	for name := range bodies {
		verifier := tenant.NewServiceVerifier(name, map[string]ed25519.PublicKey{"batch": pub})
		servers[name] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
			if err != nil || r.Method != http.MethodGet {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			got[name] = seen{claims: claims, query: r.URL.RawQuery}
			mu.Unlock()
			_, _ = w.Write([]byte(bodies[name]))
		}))
		defer servers[name].Close()
	}
	sources := inbox.NewSources(inbox.Endpoints{
		PurchasingURL: servers["purchasing"].URL, PartyURL: servers["party"].URL,
		LedgerURL: servers["ledger"].URL, PayablesURL: servers["payables"].URL,
	}, signer, time.Second)
	agg := &inbox.Aggregator{Sources: sources}
	p := principal(inbox.DutyRequisitionApprove, inbox.DutyPartyApprove, inbox.DutyJournalApprove, inbox.DutyPaymentHold)

	page, err := agg.Page(context.Background(), p, entityID, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Unavailable) != 0 {
		t.Fatalf("unavailable %v", page.Unavailable)
	}
	if want := "h1 r1 c1 b1 b3"; fmt.Sprint(ids(page)) != "["+want+"]" {
		t.Fatalf("items %v, want %s", ids(page), want)
	}
	if len(page.Truncated) != 1 || page.Truncated[0] != inbox.SourcePurchasing {
		t.Fatalf("truncated %v", page.Truncated)
	}
	wantDuty := map[string]string{"purchasing": inbox.TokenDutyRequisitions, "party": inbox.TokenDutyPartyChanges, "ledger": inbox.TokenDutyJournals, "payables": inbox.TokenDutyHolds}
	for name, s := range got {
		if s.claims.Act != userID || s.claims.Le != entityID || s.claims.Tid != tenantID || s.claims.Aud != name {
			t.Fatalf("%s token claims %+v", name, s.claims)
		}
		if len(s.claims.Duties) != 1 || s.claims.Duties[0] != wantDuty[name] {
			t.Fatalf("%s token duties %v", name, s.claims.Duties)
		}
	}
	if got["ledger"].query != "limit=100&status=in_approval" {
		t.Fatalf("ledger query %q", got["ledger"].query)
	}
	if len(got) != 4 {
		t.Fatalf("only %d sources were called", len(got))
	}
}

func TestHTTPSourceWithoutAnEndpointOrSignerIsNotSetUp(t *testing.T) {
	sources := inbox.NewSources(inbox.Endpoints{}, nil, time.Second)
	agg := &inbox.Aggregator{Sources: sources}
	page, err := agg.Page(context.Background(), principal(inbox.DutyJournalApprove), entityID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Unavailable) != 1 || page.Unavailable[0].Source != inbox.SourceLedger || page.Unavailable[0].Reason != "is not set up" {
		t.Fatalf("unavailable %v", page.Unavailable)
	}
}

func TestHTTPSourceFailureStatusIsReportedNotRetried(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("batch", priv)
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	agg := &inbox.Aggregator{Sources: inbox.NewSources(inbox.Endpoints{LedgerURL: server.URL}, signer, time.Second)}
	page, err := agg.Page(context.Background(), principal(inbox.DutyJournalApprove), entityID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Unavailable) != 1 || hits.Load() != 1 {
		t.Fatalf("unavailable %v after %d calls", page.Unavailable, hits.Load())
	}
}
