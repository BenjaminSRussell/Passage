package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

func (f *fakeStore) RunQuotas(_ context.Context, _, _ string, _ []string, _, _ time.Time) ([]domain.RunQuota, error) {
	return f.runQuotas, f.runQuotasErr
}

const (
	quotaVendorA = "00000000-0000-0000-0000-00000000c0a1"
	quotaVendorB = "00000000-0000-0000-0000-00000000c0b1"
)

func purchaseQuota(split bool) domain.RunQuota {
	d := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }
	return domain.RunQuota{ArrangementID: "arr-1", Spec: domain.QuotaArrangementSpec{
		ItemID: fgID, SiteID: siteID, ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ValidTo: time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC),
		Split: split, MinSplitQty: decimal.Zero,
		Sources: []domain.QuotaSourceSpec{
			{ID: "src-a", Type: domain.SourceVendor, VendorID: quotaVendorA, Quota: d("50"), Allocated: d("100")},
			{ID: "src-b", Type: domain.SourceVendor, VendorID: quotaVendorB, Quota: d("50")},
		}}}
}

// AC-22.13: the run gives the order to the vendor with the lowest rating.
func TestRun_QuotaArrangementPicksVendorAndStampsTheOrder(t *testing.T) {
	st := &fakeStore{runQuotas: []domain.RunQuota{purchaseQuota(false)}}
	_, req := runVersion(t, st, strings.Replace(runBody, `"production"`, `"purchase"`, 1), "k-quota")
	o := onlyOrder(t, req)
	if o.QuotaSourceID != "src-b" || o.VendorID != quotaVendorB || o.QuotaArrangementID != "arr-1" {
		t.Fatalf("order %+v", o)
	}
}

func TestRun_QuotaSplitCreatesOneOrderPerSource(t *testing.T) {
	st := &fakeStore{runQuotas: []domain.RunQuota{purchaseQuota(true)}}
	_, req := runVersion(t, st, strings.Replace(runBody, `"production"`, `"purchase"`, 1), "k-split")
	if len(req.Result.Orders) < 1 {
		t.Fatal("no orders")
	}
	total := decimal.Zero
	for _, o := range req.Result.Orders {
		if o.QuotaSourceID == "" {
			t.Fatalf("order %+v has no source", o)
		}
		total = total.Add(o.Qty)
	}
	if !total.Equal(decimal.RequireFromString("110")) { // 120 demand + 10 safety - 20 on hand
		t.Fatalf("split orders total %s", total)
	}
}

func TestRun_QuotaIgnoresOtherOrderTypes(t *testing.T) {
	st := &fakeStore{runQuotas: []domain.RunQuota{purchaseQuota(false)}}
	_, req := runVersion(t, st, runBody, "k-prod")
	if o := onlyOrder(t, req); o.QuotaSourceID != "" || o.VendorID != "" {
		t.Fatalf("a vendor source must not take a production order: %+v", o)
	}
}

// A failure reading the arrangements fails the run: nothing is stored, rather
// than a version planned as if the item had no arrangement.
func TestRun_QuotaReadFailureFailsTheRun(t *testing.T) {
	st := &fakeStore{runQuotasErr: errors.New("connection reset")}
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", strings.Replace(runBody, `"production"`, `"purchase"`, 1), "planning.run", true, "k-quota-err")
	if rec.Code != http.StatusInternalServerError || st.last.PlanCode != "" {
		t.Fatalf("status %d, stored %+v: %s", rec.Code, st.last.PlanCode, rec.Body.String())
	}
}
