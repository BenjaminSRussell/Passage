package jobs_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/jobs"
)

type memoryPages struct {
	mu       sync.Mutex
	pages    map[int]jobs.Page
	status   string
	errMsg   string
	params   json.RawMessage
	claimErr error
	finished bool
	renewals int
	renewErr error
}

func newMemoryPages() *memoryPages {
	return &memoryPages{pages: map[int]jobs.Page{}, params: explodeParams()}
}

func (m *memoryPages) Claim(context.Context, string, string) (jobs.Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return jobs.Claim{}, m.claimErr
	}
	return jobs.Claim{Params: m.params}, nil
}

func (m *memoryPages) Resume(context.Context, string) (jobs.Resume, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	last := -1
	for no, page := range m.pages {
		if page.Status == jobs.PageOK && no > last {
			last = no
		}
	}
	if last < 0 {
		return jobs.Resume{}, nil
	}
	next := m.pages[last].NextCursor
	return jobs.Resume{NextPage: last + 1, Cursor: next, Done: next == ""}, nil
}

func (m *memoryPages) Record(_ context.Context, _, _ string, page jobs.Page) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.pages[page.No]; !ok || existing.Status != jobs.PageOK {
		m.pages[page.No] = page
	}
	return nil
}

func (m *memoryPages) Renew(context.Context, string, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renewals++
	return m.renewErr
}

func (m *memoryPages) Finish(_ context.Context, _, _, status, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status, m.errMsg, m.finished = status, errMsg, true
	return nil
}

type fakeProduct struct {
	variantsLeft int
	released     []string
	onHand       map[string]bool
	failExplode  int
	failReleased map[string]int
	rejectGroup  bool
	explodeCalls int
	groupCalls   [][]string
}

func (f *fakeProduct) ExplodePage(_ context.Context, _ string, _ map[string][]string, limit int) (jobs.ExplodePage, error) {
	f.explodeCalls++
	if f.failExplode > 0 {
		f.failExplode--
		return jobs.ExplodePage{}, errors.New("product unavailable")
	}
	created := min(limit, f.variantsLeft)
	f.variantsLeft -= created
	return jobs.ExplodePage{Created: created, Remaining: f.variantsLeft}, nil
}

func (f *fakeProduct) ReleasedPage(_ context.Context, after string, limit int) (jobs.ReleasedPage, error) {
	if f.failReleased[after] > 0 {
		f.failReleased[after]--
		return jobs.ReleasedPage{}, errors.New("product unavailable")
	}
	start := sort.SearchStrings(f.released, after)
	if after != "" && start < len(f.released) && f.released[start] == after {
		start++
	}
	end := min(start+limit, len(f.released))
	page := jobs.ReleasedPage{IDs: f.released[start:end]}
	if len(page.IDs) == limit {
		page.Next = page.IDs[limit-1]
	}
	return page, nil
}

func (f *fakeProduct) GroupChange(_ context.Context, releasedIDs []string) ([]jobs.ItemResult, error) {
	f.groupCalls = append(f.groupCalls, releasedIDs)
	if f.rejectGroup {
		return nil, fmt.Errorf("%w: status 422", jobs.ErrRejected)
	}
	out := make([]jobs.ItemResult, len(releasedIDs))
	for i, id := range releasedIDs {
		out[i] = jobs.ItemResult{ID: id, Status: "changed"}
		if f.onHand[id] {
			out[i] = jobs.ItemResult{ID: id, Status: "skipped", Code: "PRD-GRPCHG-ONHAND"}
		}
	}
	return out, nil
}

func releasedIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = testkit.TenantID(i + 1)
	}
	return out
}

func explodeParams() json.RawMessage {
	raw, _ := json.Marshal(jobs.ExplodeParams{MasterID: testkit.TenantID(50)})
	return raw
}

var runID = testkit.TenantID(70)

func TestExplodeProcessesPagesOfAtMost500(t *testing.T) {
	product := &fakeProduct{variantsLeft: 1200}
	pages := newMemoryPages()
	runner := jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 1}

	summary, err := runner.Run(context.Background(), jobs.HandlerProductExplode, runID)
	if err != nil || summary.Pages != 3 || summary.Succeeded != 1200 {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
	if pages.pages[0].Items != 500 || pages.pages[1].Items != 500 || pages.pages[2].Items != 200 {
		t.Fatalf("pages = %+v", pages.pages)
	}
	if pages.status != domain.RunEnded {
		t.Fatalf("run status = %q", pages.status)
	}
}

func TestExplodeResumesAfterCrashWithoutReprocessing(t *testing.T) {
	product := &fakeProduct{variantsLeft: 1200, failExplode: 0}
	pages := newMemoryPages()
	crashing := &crashAfter{fakeProduct: product, okCalls: 1}
	first := jobs.ProductJobs{Client: crashing, Pages: pages, MaxAttempts: 1}
	if _, err := first.Run(context.Background(), jobs.HandlerProductExplode, runID); err == nil {
		t.Fatal("first run must fail on the second page")
	}
	if pages.status != domain.RunError || pages.pages[1].Status != jobs.PageFailed || pages.pages[0].Status != jobs.PageOK {
		t.Fatalf("after crash status=%q pages=%+v", pages.status, pages.pages)
	}

	product.explodeCalls = 0
	second := jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 1}
	summary, err := second.Run(context.Background(), jobs.HandlerProductExplode, runID)
	if err != nil || summary.Succeeded != 700 || summary.Pages != 2 || product.explodeCalls != 2 {
		t.Fatalf("resume = %+v, %v, calls=%d", summary, err, product.explodeCalls)
	}
	if pages.pages[1].Status != jobs.PageOK || pages.pages[1].Error != "" || pages.status != domain.RunEnded {
		t.Fatalf("retried page not healed: %+v status=%q", pages.pages[1], pages.status)
	}

	product.explodeCalls = 0
	if summary, err := second.Run(context.Background(), jobs.HandlerProductExplode, runID); err != nil || summary.Pages != 0 || product.explodeCalls != 0 {
		t.Fatalf("finished run must not call product again: %+v, %v, calls=%d", summary, err, product.explodeCalls)
	}
}

type crashAfter struct {
	*fakeProduct
	okCalls int
}

func (c *crashAfter) ExplodePage(ctx context.Context, master string, selected map[string][]string, limit int) (jobs.ExplodePage, error) {
	if c.okCalls == 0 {
		return jobs.ExplodePage{}, errors.New("pod killed")
	}
	c.okCalls--
	return c.fakeProduct.ExplodePage(ctx, master, selected, limit)
}

func TestExplodeRetriesAFailingPageThenRecordsIt(t *testing.T) {
	product := &fakeProduct{variantsLeft: 10, failExplode: 2}
	pages := newMemoryPages()
	retrying := jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 3}
	if summary, err := retrying.Run(context.Background(), jobs.HandlerProductExplode, runID); err != nil || summary.Succeeded != 10 {
		t.Fatalf("retry = %+v, %v", summary, err)
	}

	failing := &fakeProduct{variantsLeft: 10, failExplode: 5}
	pages = newMemoryPages()
	exhausted := jobs.ProductJobs{Client: failing, Pages: pages, MaxAttempts: 2}
	if _, err := exhausted.Run(context.Background(), jobs.HandlerProductExplode, runID); err == nil {
		t.Fatal("exhausted attempts must fail the run")
	}
	if failing.explodeCalls != 2 || pages.pages[0].Status != jobs.PageFailed || pages.pages[0].Error == "" || pages.errMsg == "" {
		t.Fatalf("calls=%d page=%+v runErr=%q", failing.explodeCalls, pages.pages[0], pages.errMsg)
	}
}

func TestExplodeStopsWhenProductMakesNoProgress(t *testing.T) {
	stuck := stuckProduct{}
	runner := jobs.ProductJobs{Client: stuck, Pages: newMemoryPages(), MaxAttempts: 1}
	if _, err := runner.Run(context.Background(), jobs.HandlerProductExplode, runID); err == nil {
		t.Fatal("zero-progress explode must fail instead of looping")
	}
}

type stuckProduct struct{ *fakeProduct }

func (stuckProduct) ExplodePage(context.Context, string, map[string][]string, int) (jobs.ExplodePage, error) {
	return jobs.ExplodePage{Remaining: 5}, nil
}

func TestGroupChangePagesCheckpointsAndSkipsOnHand(t *testing.T) {
	ids := releasedIDs(7)
	product := &fakeProduct{released: ids, onHand: map[string]bool{ids[1]: true, ids[5]: true}}
	pages := newMemoryPages()
	runner := jobs.ProductJobs{Client: product, Pages: pages, PageSize: 3, MaxAttempts: 1}

	summary, err := runner.Run(context.Background(), jobs.HandlerProductGroupChange, runID)
	if err != nil || summary.Pages != 3 || summary.Items != 7 || summary.Succeeded != 5 || summary.Skipped != 2 {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
	if pages.pages[1].Cursor != ids[2] || pages.pages[2].NextCursor != "" {
		t.Fatalf("cursors = %+v", pages.pages)
	}
}

func TestGroupChangeResumesFromLastCheckpoint(t *testing.T) {
	ids := releasedIDs(7)
	product := &fakeProduct{released: ids, failReleased: map[string]int{ids[5]: 99}}
	pages := newMemoryPages()
	runner := jobs.ProductJobs{Client: product, Pages: pages, PageSize: 3, MaxAttempts: 1}
	if _, err := runner.Run(context.Background(), jobs.HandlerProductGroupChange, runID); err == nil {
		t.Fatal("third page must fail")
	}
	if len(product.groupCalls) != 2 || pages.pages[2].Status != jobs.PageFailed {
		t.Fatalf("calls=%d pages=%+v", len(product.groupCalls), pages.pages)
	}

	product.failReleased = nil
	product.groupCalls = nil
	summary, err := runner.Run(context.Background(), jobs.HandlerProductGroupChange, runID)
	if err != nil || summary.Items != 1 || len(product.groupCalls) != 1 || product.groupCalls[0][0] != ids[6] {
		t.Fatalf("resume = %+v, %v, calls=%v", summary, err, product.groupCalls)
	}
}

func TestGroupChangePermanentRejectionIsNotRetried(t *testing.T) {
	product := &fakeProduct{released: releasedIDs(2), rejectGroup: true}
	pages := newMemoryPages()
	runner := jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 5}
	if _, err := runner.Run(context.Background(), jobs.HandlerProductGroupChange, runID); !errors.Is(err, jobs.ErrRejected) {
		t.Fatalf("err = %v", err)
	}
	if pages.status != domain.RunFailed {
		t.Fatalf("a rejected run is terminal, status = %q", pages.status)
	}
	if len(product.groupCalls) != 1 {
		t.Fatalf("a 4xx must not be retried, calls=%d", len(product.groupCalls))
	}
}

func TestRunRejectsInvalidInput(t *testing.T) {
	good := jobs.ProductJobs{Client: &fakeProduct{}, Pages: newMemoryPages(), MaxAttempts: 1}
	cases := map[string]struct {
		runner  jobs.ProductJobs
		handler string
		runID   string
	}{
		"unknown handler":  {good, "metadata.usage", runID},
		"bad run id":       {good, jobs.HandlerProductGroupChange, "run-1"},
		"page over 500":    {jobs.ProductJobs{Client: &fakeProduct{}, Pages: newMemoryPages(), MaxAttempts: 1, PageSize: 501}, jobs.HandlerProductGroupChange, runID},
		"no attempts":      {jobs.ProductJobs{Client: &fakeProduct{}, Pages: newMemoryPages()}, jobs.HandlerProductGroupChange, runID},
		"missing adapters": {jobs.ProductJobs{MaxAttempts: 1}, jobs.HandlerProductGroupChange, runID},
	}
	for name, c := range cases {
		if _, err := c.runner.Run(context.Background(), c.handler, c.runID); !errors.Is(err, jobs.ErrInvalidArgument) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestRunErrorsTheRunWhenPersistedParamsAreInvalid(t *testing.T) {
	pages := newMemoryPages()
	pages.params = json.RawMessage(`{}`)
	product := &fakeProduct{variantsLeft: 5}
	runner := jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 1}
	if _, err := runner.Run(context.Background(), jobs.HandlerProductExplode, runID); !errors.Is(err, jobs.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
	if product.explodeCalls != 0 || pages.status != domain.RunFailed {
		t.Fatalf("calls=%d status=%q", product.explodeCalls, pages.status)
	}
}

func TestRunDoesNothingWhenTheClaimIsRefused(t *testing.T) {
	pages := newMemoryPages()
	pages.claimErr = jobs.ErrRunNotActive
	product := &fakeProduct{variantsLeft: 5, released: releasedIDs(2)}
	runner := jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 1}
	for _, handler := range []string{jobs.HandlerProductExplode, jobs.HandlerProductGroupChange} {
		if _, err := runner.Run(context.Background(), handler, runID); !errors.Is(err, jobs.ErrRunNotActive) {
			t.Fatalf("%s: err = %v", handler, err)
		}
	}
	if product.explodeCalls != 0 || len(product.groupCalls) != 0 || pages.finished {
		t.Fatalf("a refused claim touched product or the run: %+v finished=%v", product, pages.finished)
	}
}

func TestRunFinalizesACancelledRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pages := newMemoryPages()
	runner := jobs.ProductJobs{Client: cancelling{fakeProduct: &fakeProduct{variantsLeft: 1200}, cancel: cancel}, Pages: pages, MaxAttempts: 3}
	if _, err := runner.Run(ctx, jobs.HandlerProductExplode, runID); err == nil {
		t.Fatal("a cancelled run must report an error")
	}
	if !pages.finished || pages.status != domain.RunError || pages.pages[0].Status != jobs.PageFailed {
		t.Fatalf("cancelled run not settled: finished=%v status=%q pages=%+v", pages.finished, pages.status, pages.pages)
	}
}

type cancelling struct {
	*fakeProduct
	cancel context.CancelFunc
}

func (c cancelling) ExplodePage(ctx context.Context, _ string, _ map[string][]string, _ int) (jobs.ExplodePage, error) {
	c.cancel()
	return jobs.ExplodePage{}, ctx.Err()
}

func TestExplodeCreditsVariantsCreatedByACallThatCrashedBeforeItsCheckpoint(t *testing.T) {
	product := &fakeProduct{variantsLeft: 1200}
	pages := newMemoryPages()
	first := jobs.ProductJobs{Client: &crashAfter{fakeProduct: product, okCalls: 1}, Pages: pages, MaxAttempts: 1}
	if _, err := first.Run(context.Background(), jobs.HandlerProductExplode, runID); err == nil {
		t.Fatal("second page must fail")
	}
	product.variantsLeft -= 500
	second := jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 1}
	summary, err := second.Run(context.Background(), jobs.HandlerProductExplode, runID)
	if err != nil || summary.Succeeded != 700 {
		t.Fatalf("resume = %+v, %v", summary, err)
	}
	if pages.pages[1].Succeeded != 700 {
		t.Fatalf("lost call not credited: %+v", pages.pages)
	}
}

func serviceClient(t *testing.T, h http.Handler) *jobs.HTTPProductClient {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("batch", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("product", map[string]ed25519.PublicKey{"batch": pub})
	server := httptest.NewServer(tenant.MiddlewareWith(verifier)(h))
	t.Cleanup(server.Close)
	return jobs.NewHTTPProductClient(server.URL+"/", signer)
}

func runCtx(tenantN int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantN), LegalEntityID: testkit.TenantID(9), UserID: testkit.TenantID(8), Duties: allProductDuties()})
}

func allProductDuties() map[string]struct{} {
	return map[string]struct{}{"product.variants.explode": {}, "product.released.read": {}, "product.jobs.write": {}}
}

func TestHTTPProductClientSpeaksProductContract(t *testing.T) {
	var seen []string
	client := serviceClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		p, _ := tenant.FromContext(r.Context())
		if p.TenantID != testkit.TenantID(1) || p.LegalEntityID != testkit.TenantID(9) || p.UserID != testkit.TenantID(8) || p.Service != "batch" {
			http.Error(w, "scope", http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == "/v1/product/products/"+testkit.TenantID(50)+"/variants:explode":
			var body struct {
				Limit    int
				Selected map[string][]string
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Limit != 500 || body.Selected["size"][0] != "S" {
				http.Error(w, "body", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"created":500,"skipped":3,"remaining":20,"variants":[]}`))
		case r.URL.Path == "/v1/product/released-products":
			_, _ = w.Write([]byte(`{"items":[{"id":"` + testkit.TenantID(1) + `"},{"id":"` + testkit.TenantID(2) + `"}],"next":"` + testkit.TenantID(2) + `"}`))
		case r.URL.Path == "/v1/product/jobs/group-change":
			var body struct {
				Items []struct{ ReleasedProductID string }
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Items) != 2 || body.Items[0].ReleasedProductID != testkit.TenantID(1) {
				http.Error(w, "body", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"a","status":"changed"},{"id":"b","status":"skipped","code":"PRD-GRPCHG-ONHAND"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	ctx := runCtx(1)

	explode, err := client.ExplodePage(ctx, testkit.TenantID(50), map[string][]string{"size": {"S"}}, 500)
	if err != nil || explode.Created != 500 || explode.Remaining != 20 {
		t.Fatalf("explode = %+v, %v", explode, err)
	}
	released, err := client.ReleasedPage(ctx, testkit.TenantID(1), 2)
	if err != nil || len(released.IDs) != 2 || released.Next != testkit.TenantID(2) {
		t.Fatalf("released = %+v, %v", released, err)
	}
	results, err := client.GroupChange(ctx, released.IDs)
	if err != nil || len(results) != 2 || results[1].Code != "PRD-GRPCHG-ONHAND" {
		t.Fatalf("group change = %+v, %v", results, err)
	}
	if seen[1] != "GET /v1/product/released-products?after="+testkit.TenantID(1)+"&limit=2" {
		t.Fatalf("released query = %q", seen[1])
	}

	_, err = client.ReleasedPage(runCtx(2), "", 1)
	if !errors.Is(err, jobs.ErrRejected) {
		t.Fatalf("4xx must be ErrRejected, got %v", err)
	}
}

func TestHTTPProductClientSendsEachCallATokenWithOnlyThatCallsDutyAndNoUserHeaders(t *testing.T) {
	var got []tenant.Principal
	var leaked []string
	client := serviceClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = append(got, p)
		leaked = append(leaked, r.Header.Get(tenant.HeaderDuties), r.Header.Get(tenant.HeaderTenant))
		_, _ = w.Write([]byte(`{"items":[],"created":0,"remaining":0}`))
	}))
	ctx := runCtx(1)
	if _, err := client.ExplodePage(ctx, testkit.TenantID(50), nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReleasedPage(ctx, "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GroupChange(ctx, []string{testkit.TenantID(1)}); err != nil {
		t.Fatal(err)
	}
	want := []string{"product.variants.explode", "product.released.read", "product.jobs.write"}
	if len(got) != 3 {
		t.Fatalf("calls = %d", len(got))
	}
	for i, p := range got {
		if len(p.Duties) != 1 || !p.HasDuty(want[i]) || p.Service != "batch" || p.UserID != testkit.TenantID(8) || p.LegalEntityID != testkit.TenantID(9) {
			t.Fatalf("call %d principal = %+v, want only %s", i, p, want[i])
		}
	}
	for _, header := range leaked {
		if header != "" {
			t.Fatalf("user identity headers were forwarded: %q", leaked)
		}
	}
}

func TestHTTPProductClientRefusesToCallWithoutAValidServicePrincipal(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("batch", priv)
	multi := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9) + "," + testkit.TenantID(10), UserID: testkit.TenantID(8), Duties: allProductDuties()})
	noUser := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), Duties: allProductDuties()})
	snapshot := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: testkit.TenantID(8), Duties: map[string]struct{}{"product.jobs.write": {}}})
	cases := map[string]struct {
		client *jobs.HTTPProductClient
		ctx    context.Context
	}{
		"no signer":                      {jobs.NewHTTPProductClient(server.URL, nil), runCtx(1)},
		"no principal":                   {jobs.NewHTTPProductClient(server.URL, signer), context.Background()},
		"several entities":               {jobs.NewHTTPProductClient(server.URL, signer), multi},
		"duty not in the run's snapshot": {jobs.NewHTTPProductClient(server.URL, signer), snapshot},
		"no acting user":                 {jobs.NewHTTPProductClient(server.URL, signer), noUser},
		"wrong service":                  {jobs.NewHTTPProductClient(server.URL, mustSigner(t, "receiving")), runCtx(1)},
	}
	for name, c := range cases {
		if _, err := c.client.ReleasedPage(c.ctx, "", 1); !errors.Is(err, jobs.ErrRejected) {
			t.Errorf("%s: err = %v; want ErrRejected", name, err)
		}
	}
	if hits != 0 {
		t.Fatalf("product was called %d times without a valid service token", hits)
	}
}

func mustSigner(t *testing.T, name string) *tenant.ServiceSigner {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner(name, priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestHTTPProductClientTreats5xxAndBrokenBodyAsRetryable(t *testing.T) {
	status := http.StatusBadGateway
	client := serviceClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte("not json"))
		}
	}))
	for _, code := range []int{http.StatusBadGateway, http.StatusOK, http.StatusUnauthorized} {
		status = code
		if _, err := client.ReleasedPage(runCtx(1), "", 1); err == nil || errors.Is(err, jobs.ErrRejected) {
			t.Fatalf("status %d: err = %v; want a retryable error", code, err)
		}
	}
}

func TestHTTPProductClientRetriesTransientStatusesAndReportsBodyDetail(t *testing.T) {
	var calls int
	client := serviceClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"items":[],"next":""}`))
	}))
	if _, err := client.ReleasedPage(runCtx(1), "", 1); err != nil || calls != 3 {
		t.Fatalf("429 must be retried with a fresh token each time: err=%v calls=%d", err, calls)
	}

	calls = 0
	rejecting := serviceClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, strings.Repeat("x", 5000), http.StatusUnprocessableEntity)
	}))
	_, err := rejecting.ReleasedPage(runCtx(1), "", 1)
	if !errors.Is(err, jobs.ErrRejected) || calls != 1 || len(err.Error()) > 700 || !strings.Contains(err.Error(), "xxx") {
		t.Fatalf("422 must be final with bounded detail: err=%v calls=%d", err, calls)
	}
}

func TestHTTPProductClientRefusesRedirects(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer target.Close()
	client := serviceClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	if _, err := client.ReleasedPage(runCtx(1), "", 1); !errors.Is(err, jobs.ErrRejected) || leaked {
		t.Fatalf("redirect followed: err=%v leaked=%v", err, leaked)
	}
}

func TestRunRenewsTheLeaseBeforeEveryAttemptAndStopsWhenItIsLost(t *testing.T) {
	product := &fakeProduct{variantsLeft: 10, failExplode: 2}
	pages := newMemoryPages()
	if _, err := (jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 3}).Run(context.Background(), jobs.HandlerProductExplode, runID); err != nil {
		t.Fatal(err)
	}
	if pages.renewals != 3 || product.explodeCalls != 3 {
		t.Fatalf("renewals=%d calls=%d; want one renewal per attempt", pages.renewals, product.explodeCalls)
	}

	lost := newMemoryPages()
	lost.renewErr = jobs.ErrLostLease
	stolen := &fakeProduct{variantsLeft: 10}
	if _, err := (jobs.ProductJobs{Client: stolen, Pages: lost, MaxAttempts: 3}).Run(context.Background(), jobs.HandlerProductExplode, runID); !errors.Is(err, jobs.ErrLostLease) {
		t.Fatalf("err = %v", err)
	}
	if stolen.explodeCalls != 0 || lost.finished {
		t.Fatalf("a worker that lost its lease called product or finished the run: calls=%d finished=%v", stolen.explodeCalls, lost.finished)
	}
}

func TestRunReturnsWithoutFinishingWhenTheRunStoppedBeingActive(t *testing.T) {
	pages := newMemoryPages()
	pages.renewErr = jobs.ErrRunNotActive
	if _, err := (jobs.ProductJobs{Client: &fakeProduct{released: releasedIDs(2)}, Pages: pages, MaxAttempts: 1}).Run(context.Background(), jobs.HandlerProductGroupChange, runID); !errors.Is(err, jobs.ErrRunNotActive) {
		t.Fatalf("err = %v", err)
	}
	if pages.finished {
		t.Fatal("a canceled run must not be finished by the worker")
	}
}
