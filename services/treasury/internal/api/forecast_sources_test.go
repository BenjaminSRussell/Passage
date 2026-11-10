package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/forecastsrc"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

type fakeSource struct {
	typ  string
	res  forecastsrc.Result
	err  error
	read int
}

func (f *fakeSource) Type() string { return f.typ }
func (f *fakeSource) Contribute(context.Context, string, time.Time, time.Time) (forecastsrc.Result, error) {
	f.read++
	return f.res, f.err
}

func contrib(typ, id, amount, cur string, d time.Time) domain.Contribution {
	return domain.Contribution{SourceType: typ, SourceID: id, ExpectedDate: d, Amount: decimal.RequireFromString(amount), Currency: cur}
}

func TestCashForecast_LiveSourcesReplaceStoredEntriesOfTheirTypeAndReportTheirState(t *testing.T) {
	fs := newFakeStore()
	le := ids.New()
	due := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	fs.entries = []store.ForecastEntry{
		{LegalEntityID: le, SourceType: "AP", SourceID: "old-ap", ExpectedDate: "2026-11-05", Amount: "-999.00", Currency: "USD"},
		{LegalEntityID: le, SourceType: "AR", SourceID: "old-ar", ExpectedDate: "2026-11-05", Amount: "777.00", Currency: "USD"},
		{LegalEntityID: le, SourceType: "Manual", SourceID: "m1", ExpectedDate: "2026-11-05", Amount: "5.00", Currency: "USD"},
	}
	ap := &fakeSource{typ: "AP", res: forecastsrc.Result{Contributions: []domain.Contribution{contrib("AP", "inv1", "-100", "USD", due), contrib("AP", "inv2", "-40", "EUR", due)}}}
	ar := &fakeSource{typ: "AR", err: errors.New("receivables down")}
	r := httpx.NewRouter()
	a := New(fs).WithForecastSources(ap, ar)
	a.Register(r)
	rec := call(r, http.MethodGet, "/v1/treasury/cash-forecast?legalEntityId="+le+"&layout=monthly&asOf=2026-11-01&currency=USD", nil, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("forecast: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Buckets []struct{ Currency, Inflow, Outflow, Net string } `json:"buckets"`
		Sources []sourceStatus                                    `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// AP is live (-100 USD; the stored -999 is ignored), AR failed (its stored 777 stays), Manual 5; the EUR item is filtered out.
	if len(got.Buckets) != 1 || got.Buckets[0].Inflow != "782" || got.Buckets[0].Outflow != "-100" || got.Buckets[0].Net != "682" {
		t.Fatalf("buckets = %+v, want inflow 782 outflow -100 net 682", got.Buckets)
	}
	if len(got.Sources) != 2 || got.Sources[0] != (sourceStatus{Type: "AP", Status: "ok", Count: 1}) || got.Sources[1] != (sourceStatus{Type: "AR", Status: "unavailable"}) {
		t.Fatalf("sources = %+v", got.Sources)
	}
	// live=false reads stored entries only.
	rec = call(r, http.MethodGet, "/v1/treasury/cash-forecast?legalEntityId="+le+"&layout=monthly&asOf=2026-11-01&live=false", nil, "", nil)
	if ap.read != 1 || rec.Code != http.StatusOK {
		t.Fatalf("live=false still read the sources (%d reads) or failed: %d", ap.read, rec.Code)
	}
}
