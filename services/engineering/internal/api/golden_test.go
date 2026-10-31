package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/golden"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

// AC-9.1 through the HTTP handler: the adversarial fixture exploded for site
// S1 returns the oracle's flat list, at p95 under 500 ms over 100 requests.
func TestExplode_AdversarialDAG_MatchesOracleAndP95Under500ms(t *testing.T) {
	fx, err := golden.Load("../../testdata/bom-dag-7x2000.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := golden.FlatCSV("../../testdata/bom-dag-7x2000.csv")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := fx.BomVersions()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot{}
	for _, v := range versions {
		snapshot[v.ProductID] = append(snapshot[v.ProductID], v)
	}
	r, _ := newRouter(&fakeStore{catalog: snapshot}, approves)
	body := fmt.Sprintf(`{"productId":%q,"siteId":%q,"asOf":%q,"quantity":%q,"levels":%d}`, fx.Root, fx.SiteID, fx.AsOf, fx.Quantity, fx.Levels)

	runs := make([]time.Duration, 100)
	for i := range runs {
		start := time.Now()
		rec := do(r, http.MethodPost, "/v1/engineering/boms:explode", body, allDuties, true)
		runs[i] = time.Since(start)
		if rec.Code != http.StatusOK {
			t.Fatalf("explode = %d %s", rec.Code, rec.Body.String())
		}
		if i > 0 {
			continue
		}
		var got explosionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		flat := make([]domain.FlatLine, len(got.Flat))
		for j, f := range got.Flat {
			flat[j] = domain.FlatLine{ProductID: f.ProductID, Quantity: f.Quantity}
		}
		if diffs := golden.FlatDiff(flat, want); len(diffs) > 0 {
			t.Fatalf("%d flat lines differ from the oracle, first: %v", len(diffs), diffs[0])
		}
	}
	slices.Sort(runs)
	if runs[94] >= 500*time.Millisecond {
		t.Fatalf("explode p95 over 100 requests = %v, want < 500ms", runs[94])
	}
	t.Logf("explode through the handler: p95 %v", runs[94])
}

// AC-9.4 through the handler: the oracle's adversarial lead-time fixtures,
// overlap included, and a negative overlap refused.
func TestLeadTime_AdversarialFixtures(t *testing.T) {
	var fixtures []struct {
		Name          string
		Quantity      string
		HoursPerDay   string
		Operations    json.RawMessage
		ExpectedHours string
		ExpectedDays  int
	}
	raw, err := os.ReadFile("../../testdata/routing-leadtime-adversarial.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(&fakeStore{}, approves)
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			body := fmt.Sprintf(`{"quantity":%q,"hoursPerDay":%q,"operations":%s}`, f.Quantity, f.HoursPerDay, f.Operations)
			rec := do(r, http.MethodPost, "/v1/engineering/routes:lead-time", body, allDuties, true)
			var got struct {
				Hours        decimal.Decimal
				CalendarDays int
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("lead time = %d %s", rec.Code, rec.Body)
			}
			if !got.Hours.Equal(dec(f.ExpectedHours)) || got.CalendarDays != f.ExpectedDays {
				t.Fatalf("got %s h / %d d, want %s h / %d d", got.Hours, got.CalendarDays, f.ExpectedHours, f.ExpectedDays)
			}
		})
	}
	negative := `{"quantity":"10","hoursPerDay":"8","operations":[{"no":10,"setup":"1","run":"1","processQty":"1","overlapQty":"-1"}]}`
	if rec := do(r, http.MethodPost, "/v1/engineering/routes:lead-time", negative, allDuties, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeRouteQty) {
		t.Fatalf("negative overlap = %d %s; want 422 %s", rec.Code, rec.Body, domain.CodeRouteQty)
	}
}
