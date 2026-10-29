package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var exportNow = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func dp(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

func eccn5A002(qty, value string) ExportItem {
	return ExportItem{ItemID: "SKU-1", Classification: Classification{Regime: RegimeUSEAR, Code: "5A002"},
		Qty: decimal.RequireFromString(qty), Value: decimal.RequireFromString(value)}
}

func TestRequiresLicense_DecisionTable(t *testing.T) {
	cases := []struct {
		c       Classification
		country string
		want    bool
	}{
		{Classification{Regime: RegimeUSEAR, Code: "5A002"}, "CN", true},
		{Classification{Regime: RegimeUSEAR, Code: "5A002"}, "CA", false},
		{Classification{Regime: RegimeUSEAR, Code: "EAR99"}, "CN", false},
		{Classification{Regime: RegimeEUDualUse, Code: "1A001"}, "FR", false},
		{Classification{Regime: RegimeEUDualUse, Code: "1A001"}, "CH", true},
		{Classification{Regime: RegimeHS, Code: "8471.30"}, "CN", false},
	}
	for _, c := range cases {
		if got := RequiresLicense(DefaultLicenseTable, c.c, c.country); got != c.want {
			t.Errorf("RequiresLicense(%v, %s) = %v, want %v", c.c, c.country, got, c.want)
		}
	}
}

func TestIsEmbargoed_CountryAndRegion(t *testing.T) {
	if !IsEmbargoed("CU", "") || !IsEmbargoed("KP", "") {
		t.Error("comprehensive embargo countries must be embargoed")
	}
	if !IsEmbargoed("UA", "UA-43") {
		t.Error("Crimea must be embargoed by region")
	}
	if IsEmbargoed("UA", "UA-46") || IsEmbargoed("DE", "") {
		t.Error("other regions and countries must not be embargoed")
	}
}

func TestEvaluateExport_TableDriven(t *testing.T) {
	validLicense := License{LicenseID: "LIC-2", Number: "D123", Regime: RegimeUSEAR,
		Classifications: []string{"US_EAR:5A002"}, Destinations: []string{"CN"},
		QtyLimit: dp("100"), UsedQty: decimal.RequireFromString("90"), ExpiresAt: exportNow.AddDate(1, 0, 0)}
	cases := []struct {
		name        string
		in          ExportCheckInput
		wantBlocked bool
		wantCode    string
		wantLicense string
	}{
		{name: "no license needed", in: ExportCheckInput{Destination: "CA", Items: []ExportItem{eccn5A002("1", "10")}, Mode: "BLOCK", Now: exportNow}},
		{name: "license missing blocks with TRD-1003", in: ExportCheckInput{Destination: "CN", Items: []ExportItem{eccn5A002("1", "10")}, Now: exportNow},
			wantBlocked: true, wantCode: CodeLicenseNeeded},
		{name: "valid license within quantity", in: ExportCheckInput{Destination: "CN", Items: []ExportItem{eccn5A002("10", "10")},
			Licenses: []License{validLicense}, Now: exportNow}, wantLicense: "LIC-2"},
		{name: "license quantity exhausted", in: ExportCheckInput{Destination: "CN", Items: []ExportItem{eccn5A002("11", "10")},
			Licenses: []License{validLicense}, Now: exportNow}, wantBlocked: true, wantCode: CodeLicenseNeeded},
		{name: "expired license does not count", in: ExportCheckInput{Destination: "CN", Items: []ExportItem{eccn5A002("1", "1")},
			Licenses: []License{{LicenseID: "LIC-0", Regime: RegimeUSEAR, Classifications: []string{"US_EAR:5A002"},
				Destinations: []string{"CN"}, ExpiresAt: exportNow.AddDate(0, 0, -1)}}, Now: exportNow},
			wantBlocked: true, wantCode: CodeLicenseNeeded},
		{name: "embargo blocks with TRD-1002", in: ExportCheckInput{Destination: "CU", Items: []ExportItem{eccn5A002("1", "1")},
			Licenses: []License{validLicense}, Now: exportNow}, wantBlocked: true, wantCode: CodeEmbargo},
		{name: "officer release with license reference clears embargo", in: ExportCheckInput{Destination: "UA", Region: "UA-43",
			Items: []ExportItem{eccn5A002("1", "1")}, Now: exportNow, OfficerReleaseLicenseRef: "OFAC-GL-7"}, wantLicense: "OFAC-GL-7"},
		{name: "warn mode does not block", in: ExportCheckInput{Destination: "CN", Items: []ExportItem{eccn5A002("1", "1")},
			Mode: "WARN", Now: exportNow}},
		{name: "off mode skips determination", in: ExportCheckInput{Destination: "CN", Items: []ExportItem{eccn5A002("1", "1")},
			Mode: "OFF", Now: exportNow}},
		{name: "unknown mode rejected", in: ExportCheckInput{Destination: "CN", Items: []ExportItem{eccn5A002("1", "1")},
			Mode: "MAYBE", Now: exportNow}, wantCode: CodeExportInput},
		{name: "bad destination rejected", in: ExportCheckInput{Destination: "China", Items: []ExportItem{eccn5A002("1", "1")}, Now: exportNow},
			wantCode: CodeExportInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := EvaluateExport(c.in)
			if c.wantCode != "" && c.wantCode == CodeExportInput {
				if codeOf(err) != CodeExportInput {
					t.Fatalf("want input error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Blocked != c.wantBlocked {
				t.Fatalf("blocked = %v, want %v: %+v", res.Blocked, c.wantBlocked, res)
			}
			if c.wantCode != "" && res.Items[0].Code != c.wantCode {
				t.Fatalf("code = %q, want %q", res.Items[0].Code, c.wantCode)
			}
			if c.wantLicense != "" && res.Items[0].LicenseID != c.wantLicense {
				t.Fatalf("license = %q, want %q", res.Items[0].LicenseID, c.wantLicense)
			}
		})
	}
}

// Property: for a covered item, the license allows the delivery exactly when used plus
// requested stays within the quantity limit.
func TestEvaluateExport_PropertyQuantityLimit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		limit := rapid.IntRange(1, 1000).Draw(t, "limit")
		used := rapid.IntRange(0, limit).Draw(t, "used")
		req := rapid.IntRange(1, 1000).Draw(t, "req")
		lic := License{LicenseID: "L", Regime: RegimeUSEAR, Classifications: []string{"US_EAR:5A002"},
			Destinations: []string{"CN"}, QtyLimit: dp(decimal.NewFromInt(int64(limit)).String()),
			UsedQty: decimal.NewFromInt(int64(used)), ExpiresAt: exportNow.AddDate(1, 0, 0)}
		res, err := EvaluateExport(ExportCheckInput{Destination: "CN", Now: exportNow, Licenses: []License{lic},
			Items: []ExportItem{eccn5A002(decimal.NewFromInt(int64(req)).String(), "1")}})
		if err != nil {
			t.Fatal(err)
		}
		wantAllowed := used+req <= limit
		if res.Items[0].Blocked == wantAllowed {
			t.Fatalf("used %d + req %d vs limit %d: blocked=%v", used, req, limit, res.Items[0].Blocked)
		}
	})
}

func TestEvaluateExport_LinesShareOneLicense(t *testing.T) {
	lic := License{LicenseID: "LIC-1", Regime: RegimeUSEAR, Classifications: []string{"US_EAR:5A002"}, Destinations: []string{"cn"},
		QtyLimit: dp("100"), ValueLimit: dp("1000.00"), ExpiresAt: exportNow.AddDate(0, 6, 0)}
	first, second := eccn5A002("60", "100.00"), eccn5A002("60", "100.00")
	second.ItemID = "SKU-2"
	res, err := EvaluateExport(ExportCheckInput{Destination: "cn", Items: []ExportItem{first, second}, Licenses: []License{lic}, Now: exportNow})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Blocked || res.Items[0].LicenseID != "LIC-1" {
		t.Fatalf("first line fits the license: %+v", res.Items[0])
	}
	if !res.Items[1].Blocked || res.Items[1].Code != CodeLicenseNeeded || !res.Blocked {
		t.Fatalf("second line would take the license to 120 of 100 and must block: %+v", res)
	}
	// The value limit is drawn the same way.
	byValue := lic
	byValue.QtyLimit = nil
	a, b := eccn5A002("1", "600.00"), eccn5A002("1", "600.00")
	b.ItemID = "SKU-2"
	res, _ = EvaluateExport(ExportCheckInput{Destination: "CN", Items: []ExportItem{a, b}, Licenses: []License{byValue}, Now: exportNow})
	if res.Items[0].Blocked || !res.Items[1].Blocked {
		t.Fatalf("value limit 1000 covers 600 once: %+v", res.Items)
	}
}

func TestEvaluateExport_EmbargoRegionsAndInputChecks(t *testing.T) {
	for _, region := range []string{"UA-43", "UA-40", "ua-14", "UA-09"} {
		res, err := EvaluateExport(ExportCheckInput{Destination: "UA", Region: region, Items: []ExportItem{eccn5A002("1", "1")}, Mode: LicenseCheckOff, Now: exportNow})
		if err != nil || !res.Embargo || !res.Blocked || res.Items[0].Code != CodeEmbargo {
			t.Errorf("%s must be blocked under embargo even with license checks off: %v %+v", region, err, res)
		}
	}
	bad := []ExportCheckInput{
		{Destination: "UA", Region: "RU-MOW", Items: []ExportItem{eccn5A002("1", "1")}},
		{Destination: "C1", Items: []ExportItem{eccn5A002("1", "1")}},
		{Destination: "CN", Items: []ExportItem{{ItemID: "X", Classification: Classification{Regime: "ITAR", Code: "XI"}}}},
		{Destination: "CN", Items: []ExportItem{eccn5A002("1", "1")}, Licenses: []License{{LicenseID: "L", UsedQty: decimal.RequireFromString("-1")}}},
	}
	for i, in := range bad {
		in.Now = exportNow
		if _, err := EvaluateExport(in); codeOf(err) != CodeExportInput {
			t.Errorf("case %d: want %s, got %v", i, CodeExportInput, err)
		}
	}
}
