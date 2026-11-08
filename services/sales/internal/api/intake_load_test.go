package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

// Shape of the intake proof's tenant: three legal entities sharing a product
// range, customers in ten groups, a restricted range listed for half the
// groups and MANDATORY supplementary rules on a fifth of the products.
const (
	loadLegalEntities = 3
	loadCustomers     = 300
	loadProducts      = 500
	loadRestricted    = 60  // products 0..59 have ALLOW entries for groups G0..G4
	loadWithSupp      = 100 // products 0..99 add 1 free manual per 10 ordered
	loadPerMinute     = 500
)

// loadAppPool is the application-role pool (RLS enforced) on the test schema,
// sized like dbx.Connect (20 connections), as the service runs it.
func loadAppPool(t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testkit.EnvAppURL)
	if url == "" {
		return admin
	}
	var schema string
	if err := admin.QueryRow(context.Background(), "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 20
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	app, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}

type loadOrder struct {
	le, key    string
	body       []byte
	lines      int
	suppLines  int // MANDATORY supplementary lines the order must get, counted here
	redelivery int // schedule index of the original order for a re-delivery, else -1
	newKey     bool
}

type loadResult struct {
	status  int
	orderID string
	lines   int
	code    string
	tooks   []time.Duration // every attempt's round trip
	retries int
	err     error
}

// loadDraw builds the order mix: 1 line (30%), 2-5 (40%), 6-20 (25%), 21-50
// (5%); UI 50% without an external reference, EDI 30% and WEB 20% with one;
// 30% leave the order type to the legal entity default; quantities 1-200.
// Customers in G0..G4 also order restricted products, the others never do.
func loadDraw(rng *rand.Rand, les, customers, products []string, i int) loadOrder {
	c := rng.Intn(loadCustomers)
	le := les[c%loadLegalEntities]
	n := 1
	switch r := rng.Intn(100); {
	case r < 30:
	case r < 70:
		n = 2 + rng.Intn(4)
	case r < 95:
		n = 6 + rng.Intn(15)
	default:
		n = 21 + rng.Intn(30)
	}
	supp := 0
	lines := make([]string, n)
	for l := range lines {
		p := loadRestricted + rng.Intn(loadProducts-loadRestricted)
		if c%10 < 5 && rng.Intn(4) == 0 {
			p = rng.Intn(loadRestricted)
		}
		qty := 1 + rng.Intn(200)
		if p < loadWithSupp && qty >= 10 {
			supp++
		}
		lines[l] = fmt.Sprintf(`{"productId":%q,"quantity":"%d","unit":"EA","siteId":%q,"warehouseId":%q,"unitPrice":"%d.%02d","discPct":"%d","requestedShipDate":"2027-03-%02d"}`,
			products[p], qty, les[0], les[1], 5+rng.Intn(400), rng.Intn(100), rng.Intn(3)*5, 1+rng.Intn(28))
	}
	channel, ext := "UI", ""
	switch r := rng.Intn(10); {
	case r < 5:
	case r < 8:
		channel, ext = "EDI", fmt.Sprintf(`"externalRef":"PO-%06d",`, i)
	default:
		channel, ext = "WEB", fmt.Sprintf(`"externalRef":"W-%06d",`, i)
	}
	orderType := `"orderType":"ORDER",`
	if rng.Intn(10) < 3 {
		orderType = ""
	}
	body := fmt.Sprintf(`{"legalEntityId":%q,"customerId":%q,"invoiceAccountId":%q,%s"currency":"USD","requestedShipDate":"2027-03-15","channel":%q,%s"lines":[%s]}`,
		le, customers[c], customers[c], orderType, channel, ext, strings.Join(lines, ","))
	return loadOrder{le: le, key: ids.New(), body: []byte(body), lines: n, suppLines: supp, redelivery: -1}
}

func loadPost(client *http.Client, url, path, le, duties, key string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", "intake")
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("X-Passage-Legal-Entity", le)
	if key != "" {
		req.Header.Set(idempotencyKey, key)
	}
	return client.Do(req)
}

func loadPercentile(sorted []time.Duration, p float64) time.Duration {
	i := int(float64(len(sorted))*p+0.999999) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

// TestPostgres_OrderIntake500PerMinuteP99Under500ms is the Wave 27 proof of
// "Order API handles 500 orders/min with p99 < 500 ms" (AC-27.5's rate):
// POST /v1/sales/orders through the service router, the application role
// (row-level security on) and a 20-connection pool, as cmd/sales wires it,
// against local Postgres. Arrivals are open loop, one new order every 120 ms
// whatever the latency, so a slow response cannot slow the offered load. On top
// of the 500 new orders, 5% of arrivals re-deliver an earlier order (same key,
// or for EDI/WEB a new key with the same channel and external reference), as
// at-least-once intake does; a 409 SLS-0007 (the original still in flight) is
// retried after 100 ms. Asserted: every new order 201 with its lines and the
// MANDATORY supplementary lines counted independently here, every re-delivery
// 200 with the original order, exactly one stored order per new order, no 5xx,
// and p99 < 500 ms over every request. PASSAGE_SALES_INTAKE_MINUTES (default 1)
// runs the same rate for longer (10 is AC-27.5's 5,000-order soak).
func TestPostgres_OrderIntake500PerMinuteP99Under500ms(t *testing.T) {
	if testing.Short() {
		t.Skip("load proof runs for a minute")
	}
	admin := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	minutes := 1
	if v := os.Getenv("PASSAGE_SALES_INTAKE_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("PASSAGE_SALES_INTAKE_MINUTES=%q", v)
		}
		minutes = n
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil))) // request logs are still formatted, not printed
	t.Cleanup(func() { slog.SetDefault(prev) })

	les, customers, products := make([]string, loadLegalEntities), make([]string, loadCustomers), make([]string, loadProducts)
	for _, s := range [][]string{les, customers, products} {
		for i := range s {
			s[i] = ids.New()
		}
	}
	// Customer c is in group G(c mod 10), as the party service holds it; the
	// party lookup is an in-process fake, so the budget is sales' own path.
	groups := mapGroups{}
	for c, id := range customers {
		groups[id] = fmt.Sprintf("G%d", c%10)
	}

	router := httpx.NewRouter()
	New(store.New(loadAppPool(t, admin))).WithCustomerGroups(groups).Register(router)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 256}, Timeout: 30 * time.Second}
	manual := ids.New()
	seed := func(path, le, body string) {
		resp, err := loadPost(client, srv.URL, path, le, "sales.listing.maintain", "", []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("seed %s = %d %s", path, resp.StatusCode, msg)
		}
	}
	for _, le := range les {
		for p := 0; p < loadRestricted; p++ {
			for g := 0; g < 5; g++ {
				seed(productListings, le, fmt.Sprintf(`{"legalEntityId":%q,"scopeType":"GROUP","scopeKey":"G%d","productId":%q,"listingType":"ALLOW"}`, le, g, products[p]))
			}
		}
		for p := 0; p < loadWithSupp; p++ {
			seed(suppRules, le, fmt.Sprintf(`{"legalEntityId":%q,"parentProductId":%q,"supplementaryProductId":%q,"unit":"EA","requirement":"MANDATORY","baseQuantity":"10","quantityPerBase":"1","pricing":"FREE"}`, le, products[p], manual))
		}
	}

	rng := rand.New(rand.NewSource(27))
	total := loadPerMinute * minutes
	var schedule []loadOrder
	for i := 0; i < total; i++ {
		schedule = append(schedule, loadDraw(rng, les, customers, products, i))
		if i > 0 && rng.Intn(20) == 0 { // 5%: re-deliver an earlier order
			o := rng.Intn(len(schedule))
			for schedule[o].redelivery >= 0 {
				o = rng.Intn(len(schedule))
			}
			r := schedule[o]
			r.redelivery = o
			if bytes.Contains(r.body, []byte(`"externalRef"`)) && rng.Intn(2) == 0 {
				r.key, r.newKey = ids.New(), true // BR-27.10: same (channel, externalRef), new key
			}
			schedule = append(schedule, r)
		}
	}

	results := make([]loadResult, len(schedule))
	interval := time.Minute / loadPerMinute
	var wg sync.WaitGroup
	began := time.Now()
	next := began
	for i := range schedule {
		if schedule[i].redelivery < 0 {
			time.Sleep(time.Until(next))
			next = next.Add(interval)
		}
		wg.Add(1)
		go func(i int, o loadOrder) {
			defer wg.Done()
			res := &results[i]
			for {
				start := time.Now()
				resp, err := loadPost(client, srv.URL, orders, o.le, "sales.order.write,sales.channel.intake", o.key, o.body)
				took := time.Since(start)
				if err != nil {
					res.err = err
					return
				}
				var out struct {
					ID    string            `json:"id"`
					Lines []json.RawMessage `json:"lines"`
					Code  string            `json:"code"`
				}
				err = json.NewDecoder(resp.Body).Decode(&out)
				resp.Body.Close()
				res.tooks = append(res.tooks, took)
				res.status, res.orderID, res.lines, res.code, res.err = resp.StatusCode, out.ID, len(out.Lines), out.Code, err
				if resp.StatusCode == http.StatusConflict && out.Code == store.CodeDuplicateIntake && res.retries < 5 {
					res.retries++
					time.Sleep(100 * time.Millisecond)
					continue
				}
				return
			}
		}(i, schedule[i])
	}
	offered := time.Since(began)
	wg.Wait()

	var all []time.Duration
	retried, created, replayed := 0, 0, 0
	for i, o := range schedule {
		r := results[i]
		all = append(all, r.tooks...)
		retried += r.retries
		if r.err != nil || r.status >= 500 {
			t.Fatalf("request %d: status %d %s %v", i, r.status, r.code, r.err)
		}
		if o.redelivery < 0 {
			if r.status != http.StatusCreated || r.lines != o.lines+o.suppLines {
				t.Fatalf("order %d = %d %s with %d lines; want 201 with %d + %d supplementary", i, r.status, r.code, r.lines, o.lines, o.suppLines)
			}
			created++
			continue
		}
		orig := results[o.redelivery]
		if r.status != http.StatusOK || r.orderID != orig.orderID {
			t.Fatalf("re-delivery %d (new key %v) = %d %s order %s; want 200 with the original %s", i, o.newKey, r.status, r.code, r.orderID, orig.orderID)
		}
		replayed++
	}
	var stored, suppStored int
	if err := admin.QueryRow(ctx, `select count(*) from sales_order`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `select count(*) from sales_order_line where supplementary_of is not null`).Scan(&suppStored); err != nil {
		t.Fatal(err)
	}
	wantSupp := 0
	for _, o := range schedule {
		if o.redelivery < 0 {
			wantSupp += o.suppLines
		}
	}
	if stored != total || created != total || suppStored != wantSupp {
		t.Fatalf("stored %d orders (%d supplementary lines) for %d new orders; want exactly one each and %d supplementary lines", stored, suppStored, total, wantSupp)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	p99 := loadPercentile(all, 0.99)
	t.Logf("%d new orders + %d re-deliveries offered in %s (%.0f new orders/min), %d SLS-0007 retries; latency over %d requests: p50 %s, p95 %s, p99 %s, max %s",
		total, replayed, offered.Round(time.Millisecond), float64(total)/offered.Minutes(), retried, len(all),
		loadPercentile(all, 0.50), loadPercentile(all, 0.95), p99, all[len(all)-1])
	if p99 >= 500*time.Millisecond {
		t.Errorf("p99 %s; want < 500 ms at %d orders/min", p99, loadPerMinute)
	}
}

// mapGroups is a read-only customer -> group table standing in for party.
type mapGroups map[string]string

func (m mapGroups) CustomerGroup(_ context.Context, _, customerID string) (string, error) {
	return m[customerID], nil
}
