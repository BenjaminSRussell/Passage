package domain

import (
	"errors"
	"testing"
)

func pendingEstimate() SalesEstimate {
	return SalesEstimate{ItemID: "FG-CFG", Status: StatusPending, CalculationType: CalcTypeSalesOrder}
}

func hasDiag(diags []Diagnostic, code string) bool {
	for _, dg := range diags {
		if dg.Code == code {
			return true
		}
	}
	return false
}

func TestRefreshEstimate_FirstConfigurationIsPending(t *testing.T) {
	res, err := RefreshEstimate(nil, "L-1", "cfg-a", pendingEstimate())
	if err != nil {
		t.Fatalf("RefreshEstimate: %v", err)
	}
	if !res.Changed || res.Current.Version != 1 || res.Current.Status != StatusPending {
		t.Errorf("current = %+v changed=%v, want version 1 pending", res.Current, res.Changed)
	}
	if len(res.Diagnostics) != 0 {
		t.Errorf("unexpected diagnostics %+v", res.Diagnostics)
	}
}

func TestRefreshEstimate_SameConfigurationChangesNothing(t *testing.T) {
	first, _ := RefreshEstimate(nil, "L-1", "cfg-a", pendingEstimate())
	again, err := RefreshEstimate(first.Versions, "L-1", "cfg-a", pendingEstimate())
	if err != nil {
		t.Fatalf("RefreshEstimate: %v", err)
	}
	if again.Changed || len(again.Versions) != 1 || again.Current.Version != 1 {
		t.Errorf("changed=%v versions=%d, want no change", again.Changed, len(again.Versions))
	}
}

func TestRefreshEstimate_ConfigurationChangeSupersedes(t *testing.T) {
	first, _ := RefreshEstimate(nil, "L-1", "cfg-a", pendingEstimate())
	next, err := RefreshEstimate(first.Versions, "L-1", "cfg-b", pendingEstimate())
	if err != nil {
		t.Fatalf("RefreshEstimate: %v", err)
	}
	if !next.Changed || next.Current.Version != 2 || next.Current.ConfigHash != "cfg-b" {
		t.Fatalf("current = %+v, want version 2 for cfg-b", next.Current)
	}
	if next.Versions[0].Status != EstimateSuperseded {
		t.Errorf("version 1 status = %s, want Superseded", next.Versions[0].Status)
	}
	if !hasDiag(next.Diagnostics, CodeEstimateSuperseded) {
		t.Errorf("diagnostics %+v, want %s", next.Diagnostics, CodeEstimateSuperseded)
	}
}

func TestRefreshEstimate_ChangeAfterFreezeSupersedesFrozen(t *testing.T) {
	first, _ := RefreshEstimate(nil, "L-1", "cfg-a", pendingEstimate())
	frozen, _, err := FreezeEstimate(first.Versions, "L-1")
	if err != nil {
		t.Fatalf("FreezeEstimate: %v", err)
	}
	next, err := RefreshEstimate(frozen, "L-1", "cfg-b", pendingEstimate())
	if err != nil {
		t.Fatalf("RefreshEstimate: %v", err)
	}
	if next.Versions[0].Status != EstimateSuperseded || next.Current.Version != 2 {
		t.Errorf("frozen version not superseded: %+v", next.Versions)
	}
}

func TestRefreshEstimate_RejectsForeignLineAndEmptyHash(t *testing.T) {
	first, _ := RefreshEstimate(nil, "L-1", "cfg-a", pendingEstimate())
	if _, err := RefreshEstimate(first.Versions, "L-2", "cfg-a", pendingEstimate()); err == nil {
		t.Error("version of another line accepted")
	}
	if _, err := RefreshEstimate(nil, "L-1", "", pendingEstimate()); err == nil {
		t.Error("empty configuration hash accepted")
	}
	if _, err := RefreshEstimate(nil, "", "cfg-a", pendingEstimate()); err == nil {
		t.Error("empty line id accepted")
	}
}

func TestFreezeEstimate_ConfirmsAndIsIdempotent(t *testing.T) {
	first, _ := RefreshEstimate(nil, "L-1", "cfg-a", pendingEstimate())
	once, cur, err := FreezeEstimate(first.Versions, "L-1")
	if err != nil {
		t.Fatalf("FreezeEstimate: %v", err)
	}
	if cur.Status != EstimateFrozen || once[0].Status != EstimateFrozen {
		t.Errorf("version not frozen: %+v", cur)
	}
	twice, cur2, err := FreezeEstimate(once, "L-1")
	if err != nil {
		t.Fatalf("second FreezeEstimate: %v", err)
	}
	if cur2.Status != EstimateFrozen || len(twice) != 1 {
		t.Errorf("second freeze changed the version: %+v", cur2)
	}
}

func TestFreezeEstimate_FailsWithoutUsableEstimate(t *testing.T) {
	errorEstimate := SalesEstimate{ItemID: "FG-CFG", Status: StatusError}
	withError, _ := RefreshEstimate(nil, "L-1", "cfg-a", errorEstimate)
	cases := []struct {
		name     string
		prior    []EstimateVersion
		lineID   string
		wantCode string
	}{
		{"no versions", nil, "L-1", CodeNoEstimate},
		{"current estimate errored", withError.Versions, "L-1", CodeNoEstimate},
		{"empty line id", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := FreezeEstimate(tc.prior, tc.lineID)
			if err == nil {
				t.Fatal("FreezeEstimate succeeded")
			}
			if tc.wantCode == "" {
				return
			}
			var ce *CodedError
			if !errors.As(err, &ce) || ce.Code != tc.wantCode {
				t.Fatalf("err = %v, want code %s", err, tc.wantCode)
			}
		})
	}
}

func TestFreezeEstimate_FrozenThenRefreshedFreezesNewVersion(t *testing.T) {
	first, _ := RefreshEstimate(nil, "L-1", "cfg-a", pendingEstimate())
	frozen, _, _ := FreezeEstimate(first.Versions, "L-1")
	next, _ := RefreshEstimate(frozen, "L-1", "cfg-b", pendingEstimate())
	versions, cur, err := FreezeEstimate(next.Versions, "L-1")
	if err != nil {
		t.Fatalf("FreezeEstimate: %v", err)
	}
	if cur.Version != 2 || cur.Status != EstimateFrozen {
		t.Errorf("current = %+v, want frozen version 2", cur)
	}
	if versions[0].Status != EstimateSuperseded {
		t.Errorf("version 1 = %s, want Superseded", versions[0].Status)
	}
}

func TestRefreshEstimate_RejectsForgedHistory(t *testing.T) {
	cases := []struct {
		name  string
		prior []EstimateVersion
	}{
		{"duplicate version number", []EstimateVersion{
			{LineID: "L-1", Version: 1, ConfigHash: "a", Status: StatusPending},
			{LineID: "L-1", Version: 1, ConfigHash: "b", Status: EstimateSuperseded}}},
		{"version zero", []EstimateVersion{{LineID: "L-1", Version: 0, ConfigHash: "a", Status: StatusPending}}},
		{"unknown status", []EstimateVersion{{LineID: "L-1", Version: 1, ConfigHash: "a", Status: "Approved"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RefreshEstimate(tc.prior, "L-1", "c", pendingEstimate()); err == nil {
				t.Error("RefreshEstimate accepted the history")
			}
			if _, _, err := FreezeEstimate(tc.prior, "L-1"); err == nil {
				t.Error("FreezeEstimate accepted the history")
			}
		})
	}
}

func TestRefreshEstimate_DoesNotMutateTheCallersVersions(t *testing.T) {
	prior := []EstimateVersion{{LineID: "L-1", Version: 1, ConfigHash: "a", Status: EstimateFrozen, Estimate: pendingEstimate()}}
	if _, err := RefreshEstimate(prior, "L-1", "b", pendingEstimate()); err != nil {
		t.Fatal(err)
	}
	if prior[0].Status != EstimateFrozen {
		t.Errorf("caller's version 1 became %s", prior[0].Status)
	}
}

func TestSalesEstimate_AC39_13_MarginThenFreezeOnConfirmation(t *testing.T) {
	cat := Catalog{
		"RM-FRAME": {ID: "RM-FRAME", Kind: KindPurchased, Price: ptr("1500.00")},
		"CFG-LINE": {ID: "CFG-LINE", Kind: KindManufactured, CostingLot: d("1"),
			BOM:        []BOMLine{{ChildID: "RM-FRAME", Qty: d("1")}},
			Operations: []Operation{{RunHours: d("6"), Rate: d("60")}}},
	}
	est, err := SalesOrderEstimate(cat, "CFG-LINE", d("1"), d("2400.00"), MoneyScale, RollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := RefreshEstimate(nil, "SO-1/10", "cfg-1", est)
	if err != nil {
		t.Fatal(err)
	}
	_, confirmed, err := FreezeEstimate(res.Versions, "SO-1/10")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != EstimateFrozen || confirmed.Estimate.Cost.StringFixed(2) != "1860.00" ||
		confirmed.Estimate.Margin.StringFixed(2) != "540.00" || confirmed.Estimate.MarginPct.StringFixed(2) != "22.50" {
		t.Errorf("confirmed = %+v, want frozen cost 1860.00 margin 540.00 (22.50%%)", confirmed)
	}
}
