package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/api"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/registry"
)

func vendorDefinition() *domain.Entity {
	return &domain.Entity{
		Name:       "Vendor",
		Domain:     "vendor",
		TableGroup: "main",
		Fields: []domain.Field{
			{Name: "code", Type: "string", Length: 20, Required: true},
			{Name: "status", Type: "enum", Enum: "Status"},
		},
		Enums: []domain.Enum{{Name: "Status", Values: []domain.EnumValue{{Name: "active", Code: 1}}}},
	}
}

func server(t *testing.T, s registry.Store) *httptest.Server {
	t.Helper()
	r := httpx.NewRouter()
	api.New(s).Register(r)
	return httptest.NewServer(r)
}

func request(t *testing.T, client *http.Client, method, url, body string, duties ...string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderUser, testkit.TenantID(2))
	req.Header.Set(tenant.HeaderDuties, strings.Join(duties, ","))
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func body(t *testing.T, res *http.Response) []byte {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestExtensionAppearsInEffectiveSchemaAndGeneratedAPIWithoutRestart(t *testing.T) {
	store := registry.NewMemory()
	if _, err := store.Publish(testkit.Ctx(testkit.TenantID(1)), testkit.TenantID(1), vendorDefinition()); err != nil {
		t.Fatal(err)
	}
	srv := server(t, store)
	defer srv.Close()

	extension := `{
	  "name":"customer.vendor-preferences",
	  "entity":"Vendor",
	  "fields":[{"name":"account_manager","type":"string","length":80}],
	  "enum_values":[{"enum":"Status","values":[{"name":"preferred","code":1000}]}]
	}`
	res := request(t, srv.Client(), http.MethodPost, srv.URL+"/v1/metadata/extensions", extension, "metadata.write")
	if data := body(t, res); res.StatusCode != http.StatusCreated {
		t.Fatalf("extension status=%d body=%s", res.StatusCode, data)
	}

	res = request(t, srv.Client(), http.MethodGet, srv.URL+"/api/v1/metadata/entities/Vendor/schema", "", "metadata.read")
	schema := body(t, res)
	if res.StatusCode != http.StatusOK || !bytes.Contains(schema, []byte(`"accountManager"`)) || !bytes.Contains(schema, []byte(`"preferred"`)) {
		t.Fatalf("effective schema status=%d body=%s", res.StatusCode, schema)
	}

	record := `{"code":"V-1","status":"preferred","accountManager":"Ada"}`
	res = request(t, srv.Client(), http.MethodPost, srv.URL+"/v1/data/Vendor", record, "metadata.data.write")
	created := body(t, res)
	if res.StatusCode != http.StatusCreated || !bytes.Contains(created, []byte(`"accountManager":"Ada"`)) {
		t.Fatalf("create status=%d body=%s", res.StatusCode, created)
	}

	res = request(t, srv.Client(), http.MethodGet, srv.URL+"/v1/data/Vendor", "", "metadata.data.read")
	list := body(t, res)
	if res.StatusCode != http.StatusOK || !bytes.Contains(list, []byte(`"status":"preferred"`)) {
		t.Fatalf("list status=%d body=%s", res.StatusCode, list)
	}
}

func TestBreakingPublishFailsLoudly(t *testing.T) {
	store := registry.NewMemory()
	if _, err := store.Publish(testkit.Ctx(testkit.TenantID(1)), testkit.TenantID(1), vendorDefinition()); err != nil {
		t.Fatal(err)
	}
	srv := server(t, store)
	defer srv.Close()
	broken := vendorDefinition()
	broken.Fields = broken.Fields[1:]
	data, _ := json.Marshal(broken)
	res := request(t, srv.Client(), http.MethodPost, srv.URL+"/v1/metadata/entities", string(data), "metadata.write")
	got := body(t, res)
	if res.StatusCode != http.StatusConflict || !bytes.Contains(got, []byte("MET-0201")) {
		t.Fatalf("status=%d body=%s", res.StatusCode, got)
	}
}

func TestGeneratedAPIThreeNodesSustainsThousandReadsPerSecond(t *testing.T) {
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previousLogger)
	store := registry.NewMemory()
	tenantID := testkit.TenantID(1)
	if _, err := store.Publish(testkit.Ctx(tenantID), tenantID, vendorDefinition()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRecord(testkit.Ctx(tenantID), tenantID, "Vendor", map[string]any{"code": "V-1", "status": "active"}); err != nil {
		t.Fatal(err)
	}
	nodes := []*httptest.Server{server(t, store), server(t, store), server(t, store)}
	for _, node := range nodes {
		defer node.Close()
	}

	const requests = 1000
	const workers = 20
	durations := make([]time.Duration, requests)
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < requests; i += workers {
				began := time.Now()
				res := request(t, nodes[i%len(nodes)].Client(), http.MethodGet, nodes[i%len(nodes)].URL+"/v1/data/Vendor", "", "metadata.data.read")
				_ = body(t, res)
				if res.StatusCode != http.StatusOK {
					t.Errorf("request %d status=%d", i, res.StatusCode)
				}
				durations[i] = time.Since(began)
			}
		}(worker)
	}
	wg.Wait()
	elapsed := time.Since(start)
	if rate := float64(requests) / elapsed.Seconds(); rate < 1000 {
		t.Fatalf("throughput %.0f rps < 1000 rps (%s)", rate, elapsed)
	}
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p95 := sorted[(len(sorted)*95/100)-1]
	if p95 >= 50*time.Millisecond {
		t.Fatalf("p95=%s, want <50ms; elapsed=%s", p95, elapsed)
	}
	t.Logf("%s", fmt.Sprintf("three-node generated API: %.0f rps, p95 %s", float64(requests)/elapsed.Seconds(), p95))
}
