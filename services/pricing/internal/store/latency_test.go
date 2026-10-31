package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/api"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// Catalog shape for the latency proof: one legal entity's USD price list.
const (
	latProducts  = 2000
	latPGroups   = 40
	latCustomers = 500
	latCGroups   = 25
)

// latencyCatalog is a representative price list of 18,083 ACTIVE records: base prices with
// three quantity breaks for every product, customer-group and customer-specific prices that
// overlap them (some with search_next), group and customer line discounts, multiline
// discounts per product group and total discounts. Every product has a base price, so every
// line of every request prices.
func latencyCatalog() []domain.Record {
	var out []domain.Record
	add := func(t domain.ConditionType, ck domain.CustomerKind, ckey string, pk domain.ProductKind, pkey, from, amount, pct string, searchNext bool) {
		out = append(out, domain.Record{Type: t, CustomerKind: ck, CustomerKey: ckey, ProductKind: pk, ProductKey: pkey,
			Currency: "USD", Unit: "EA", FromQuantity: decimal.RequireFromString(from), Amount: decimal.RequireFromString(amount),
			DiscPct1: decimal.RequireFromString(pct), ValidFrom: day("2026-01-01"), ValidTo: day("2026-12-31"), SearchNext: searchNext})
	}
	for p := 0; p < latProducts; p++ {
		prod := fmt.Sprintf("P%04d", p)
		base := 10 + p%90
		add(domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, prod, "0", fmt.Sprintf("%d.00", base), "0", false)
		add(domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, prod, "10", fmt.Sprintf("%d.50", base-1), "0", false)
		add(domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindProduct, prod, "100", fmt.Sprintf("%d.25", base-2), "0", false)
	}
	for g := 0; g < latCGroups; g++ {
		for p := g % 10; p < latProducts; p += 10 {
			add(domain.TypePrice, domain.CustomerKindGroup, fmt.Sprintf("CG%02d", g), domain.ProductKindProduct, fmt.Sprintf("P%04d", p),
				"0", fmt.Sprintf("%d.75", 9+p%90), "0", p%3 == 0)
		}
		for pg := 0; pg < latPGroups; pg++ {
			add(domain.TypeLineDisc, domain.CustomerKindGroup, fmt.Sprintf("CG%02d", g), domain.ProductKindGroup, fmt.Sprintf("PG%02d", pg),
				"0", "0", fmt.Sprint(1+(g+pg)%8), false)
		}
	}
	for c := 0; c < latCustomers; c++ {
		cust := fmt.Sprintf("C%03d", c)
		for k := 0; k < 10; k++ {
			p := (c*37 + k*211) % latProducts
			add(domain.TypePrice, domain.CustomerKindCustomer, cust, domain.ProductKindProduct, fmt.Sprintf("P%04d", p),
				"0", fmt.Sprintf("%d.10", 8+p%90), "0", k%4 == 0)
		}
		add(domain.TypeLineDisc, domain.CustomerKindCustomer, cust, domain.ProductKindAll, "", "0", "0", "2", false)
		add(domain.TypeTotalDisc, domain.CustomerKindCustomer, cust, domain.ProductKindAll, "", "0", "0", "1.5", true)
	}
	for pg := 0; pg < latPGroups; pg++ {
		add(domain.TypeMultiline, domain.CustomerKindAll, "", domain.ProductKindGroup, fmt.Sprintf("PG%02d", pg), "50", "0", "3", false)
		add(domain.TypeMultiline, domain.CustomerKindAll, "", domain.ProductKindGroup, fmt.Sprintf("PG%02d", pg), "200", "0", "5", false)
	}
	for _, br := range []string{"0", "100", "500"} {
		add(domain.TypeTotalDisc, domain.CustomerKindAll, "", domain.ProductKindAll, "", br, "0", "1", false)
	}
	return out
}

// seedViaJournal activates records through one CREATE journal (create, validate, post).
func seedViaJournal(t *testing.T, ctx context.Context, s *store.Postgres, recs []domain.Record) {
	t.Helper()
	lines := make([]domain.JournalLine, len(recs))
	for i, r := range recs {
		lines[i] = domain.JournalLine{Op: domain.OpCreate, Record: r}
	}
	j, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: "Seed price list", Lines: lines}, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.ValidateJournal(ctx, j.ID); err != nil || v.Status != domain.JournalValidated {
		t.Fatalf("validate seed journal = %s with %d errors, %v", v.Status, v.ErrorCount, err)
	}
	if _, err := s.PostJournal(ctx, j.ID, ""); err != nil {
		t.Fatalf("post seed journal: %v", err)
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	i := int(float64(len(sorted))*p+0.999999) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

// latencyRun sends callers x perCaller resolves of 10 random lines each, from callers
// concurrent goroutines, and returns every request's wall time (HTTP round trip), sorted.
// Customers are random, or caller c is customer firstCust+c when firstCust >= 0.
func latencyRun(t *testing.T, srv *httptest.Server, callers, perCaller int, seed int64, firstCust int) []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var all []time.Duration
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for c := 0; c < callers; c++ {
		wg.Add(1)
		go func(c int, seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			client := srv.Client()
			local := make([]time.Duration, 0, perCaller)
			for i := 0; i < perCaller; i++ {
				cust := rng.Intn(latCustomers)
				if firstCust >= 0 {
					cust = firstCust + c
				}
				var lines []string
				for l := 0; l < 10; l++ {
					p := rng.Intn(latProducts)
					lines = append(lines, fmt.Sprintf(`{"productId":"P%04d","productGroupId":"PG%02d","unit":"EA","quantity":"%d"}`,
						p, p%latPGroups, 1+rng.Intn(150)))
				}
				body := fmt.Sprintf(`{"legalEntityId":"%s","customerId":"C%03d","customerGroupId":"CG%02d","currency":"USD","pricingDate":"2026-06-01","lines":[%s]}`,
					legalEntity, cust, cust%latCGroups, strings.Join(lines, ","))
				req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/pricing/resolve?explain=true", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
				req.Header.Set("X-Passage-Duties", "pricing.view")
				req.Header.Set("X-Passage-Legal-Entity", legalEntity)
				start := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					errs <- err
					return
				}
				var out struct {
					Lines []struct {
						ErrorCode string `json:"errorCode"`
					} `json:"lines"`
				}
				err = json.NewDecoder(resp.Body).Decode(&out)
				resp.Body.Close()
				local = append(local, time.Since(start))
				if err != nil || resp.StatusCode != http.StatusOK || len(out.Lines) != 10 {
					errs <- fmt.Errorf("resolve = %d, %d lines, %v", resp.StatusCode, len(out.Lines), err)
					return
				}
				for _, l := range out.Lines {
					if l.ErrorCode != "" {
						errs <- fmt.Errorf("a line did not price: %s in %s", l.ErrorCode, body)
						return
					}
				}
			}
			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
		}(c, seed*1000+int64(c))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all
}

// TestPostgres_ResolveP95Under20ms (Wave 26 AC3, FR-26.14, AC-26.9): 50 concurrent callers
// resolve 10-line orders over HTTP (with explain) against an 18,083-record price list in
// Postgres, through the service's record snapshot cache at its default TTL (60 s, as
// cmd/pricing wires it). The asserted run is the steady state: the snapshot of each of the
// 25 customer groups has been loaded once before the clock starts, as it is for all but one
// resolve per group per minute in service. p95 must stay under 20 ms and p99 under 60 ms.
// Logged, not asserted: the cold start (50 callers on an empty cache, every snapshot load
// counted) and the same load without the cache (PRICING_CACHE_TTL=0, every resolve reads
// the whole price list from Postgres).
func TestPostgres_ResolveP95Under20ms(t *testing.T) {
	if testing.Short() {
		t.Skip("latency proof seeds 18k records")
	}
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	catalog := latencyCatalog()
	seedViaJournal(t, ctx, s, catalog)
	report := func(name string, d []time.Duration) (time.Duration, time.Duration) {
		p95, p99 := percentile(d, 0.95), percentile(d, 0.99)
		t.Logf("%s: %d records, %d 10-line resolves: p50 %s, p95 %s, p99 %s, max %s",
			name, len(catalog), len(d), percentile(d, 0.50), p95, p99, d[len(d)-1])
		return p95, p99
	}
	server := func(ttl time.Duration) *httptest.Server {
		r := httpx.NewRouter()
		api.New(store.NewCache(s, ttl), api.Config{}).Register(r)
		srv := httptest.NewServer(r)
		t.Cleanup(srv.Close)
		return srv
	}

	cached := server(60 * time.Second)
	report("cold start, cache 60s, 50 callers (logged)", latencyRun(t, cached, 50, 4, 1, -1))
	// Load any group snapshot the cold start did not reach: customers C000..C024 are in
	// groups CG00..CG24.
	latencyRun(t, cached, latCGroups, 1, 2, 0)
	p95, p99 := report("steady state, cache 60s, 50 callers (asserted)", latencyRun(t, cached, 50, 100, 3, -1))
	if p95 >= 20*time.Millisecond || p99 >= 60*time.Millisecond {
		t.Errorf("steady state p95 %s / p99 %s; want p95 < 20ms and p99 < 60ms (FR-26.14)", p95, p99)
	}
	report("no cache, 50 callers (logged)", latencyRun(t, server(0), 50, 4, 4, -1))
}
