package jobs_test

import (
	"context"
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
	runner := jobs.ProductJobs{Client: product, Pages: newMemoryPages(), MaxAttempts: 5}
	if _, err := runner.Run(context.Background(), jobs.HandlerProductGroupChange, runID); !errors.Is(err, jobs.ErrRejected) {
		t.Fatalf("err = %v", err)
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
	if product.explodeCalls != 0 || pages.status != domain.RunError {
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

func TestHTTPProductClientSpeaksProductContract(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get(tenant.HeaderTenant) != testkit.TenantID(1) || r.Header.Get(tenant.HeaderLegalEntity) != testkit.TenantID(9) {
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
	defer server.Close()
	client := jobs.NewHTTPProductClient(server.URL + "/")
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "u", Duties: map[string]struct{}{"product.write": {}}})

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

	_, err = client.ReleasedPage(tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2)}), "", 1)
	if !errors.Is(err, jobs.ErrRejected) {
		t.Fatalf("4xx must be ErrRejected, got %v", err)
	}
}

func TestHTTPProductClientTreats5xxAndBrokenBodyAsRetryable(t *testing.T) {
	status := http.StatusBadGateway
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte("not json"))
		}
	}))
	defer server.Close()
	client := jobs.NewHTTPProductClient(server.URL)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	for _, code := range []int{http.StatusBadGateway, http.StatusOK} {
		status = code
		if _, err := client.ReleasedPage(ctx, "", 1); err == nil || errors.Is(err, jobs.ErrRejected) {
			t.Fatalf("status %d: err = %v; want a retryable error", code, err)
		}
	}
}

func TestHTTPProductClientForwardsOnlyTheDutiesTheCallNeeds(t *testing.T) {
	var duties []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		duties = append(duties, r.Header.Get(tenant.HeaderDuties))
		_, _ = w.Write([]byte(`{"items":[],"created":0,"remaining":0}`))
	}))
	defer server.Close()
	client := jobs.NewHTTPProductClient(server.URL)
	held := map[string]struct{}{"product.read": {}, "product.write": {}, "product.jobs.write": {}, "product.release": {}}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: held})
	if _, err := client.ExplodePage(ctx, testkit.TenantID(50), nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReleasedPage(ctx, "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GroupChange(ctx, []string{testkit.TenantID(1)}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(duties) != "[product.write product.read product.jobs.write]" {
		t.Fatalf("duties = %v", duties)
	}
}

func TestHTTPProductClientRetriesTransientStatusesAndReportsBodyDetail(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"items":[],"next":""}`))
	}))
	defer server.Close()
	client := jobs.NewHTTPProductClient(server.URL)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	if _, err := client.ReleasedPage(ctx, "", 1); err != nil || calls != 3 {
		t.Fatalf("429 must be retried: err=%v calls=%d", err, calls)
	}

	calls = 0
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, strings.Repeat("x", 5000), http.StatusUnprocessableEntity)
	}))
	defer rejecting.Close()
	_, err := jobs.NewHTTPProductClient(rejecting.URL).ReleasedPage(ctx, "", 1)
	if !errors.Is(err, jobs.ErrRejected) || calls != 1 || len(err.Error()) > 700 || !strings.Contains(err.Error(), "xxx") {
		t.Fatalf("422 must be final with bounded detail: err=%v calls=%d", err, calls)
	}
}

func TestHTTPProductClientRefusesRedirects(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	if _, err := jobs.NewHTTPProductClient(server.URL).ReleasedPage(ctx, "", 1); !errors.Is(err, jobs.ErrRejected) || leaked {
		t.Fatalf("redirect followed: err=%v leaked=%v", err, leaked)
	}
}
