package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCertificateStatus(t *testing.T) {
	ok := []CertReceipt{{Type: CertEN10204_31, InSpec: true}}
	nok := []CertReceipt{{Type: CertEN10204_31, InSpec: false}}
	cases := []struct {
		name     string
		required string
		receipts []CertReceipt
		waived   bool
		want     string
	}{
		{"no requirement", CertNone, nil, false, CertNotRequired},
		{"required and missing", CertEN10204_31, nil, false, CertMissing},
		{"wrong type only", CertEN10204_31, []CertReceipt{{Type: CertCoA, InSpec: true}}, false, CertMissing},
		{"received in spec", CertEN10204_31, ok, false, CertReceivedOK},
		{"received out of spec", CertEN10204_31, nok, false, CertReceivedNOK},
		{"latest receipt decides", CertEN10204_31, append(nok, ok...), false, CertReceivedOK},
		{"waiver overrides missing", CertEN10204_31, nil, true, CertWaived},
	}
	for _, c := range cases {
		if got := CertificateStatus(c.required, c.receipts, c.waived); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestCheckCertificateAccept(t *testing.T) {
	// AC-33.12: accept is rejected without the certificate (QM-0125), allowed afterwards.
	err := CheckCertificateAccept(CertEN10204_31, CertMissing)
	if e, isErr := err.(*Error); !isErr || e.Code != CodeCertRequired {
		t.Fatalf("missing certificate: %v", err)
	}
	if err := CheckCertificateAccept(CertEN10204_31, CertReceivedNOK); err == nil {
		t.Error("NOK certificate must block acceptance")
	}
	for _, s := range []string{CertNotRequired, CertReceivedOK, CertWaived} {
		if err := CheckCertificateAccept(CertEN10204_31, s); err != nil {
			t.Errorf("%s must allow acceptance: %v", s, err)
		}
	}
}

func TestValidateCertWaiver(t *testing.T) {
	if err := ValidateCertWaiver("   "); err == nil {
		t.Error("blank reason must fail")
	}
	long := make([]byte, 501)
	for i := range long {
		long[i] = 'x'
	}
	if err := ValidateCertWaiver(string(long)); err == nil {
		t.Error("reason over 500 characters must fail")
	}
	if err := ValidateCertWaiver("supplier audit on file"); err != nil {
		t.Error(err)
	}
}

func TestCertInSpec(t *testing.T) {
	lines := []TestLine{
		{TestID: "t-var", Kind: KindVariable, Required: true, Decimals: 1, Min: ptrDec("1"), Max: ptrDec("2")},
		{TestID: "t-att", Kind: KindAttribute, Required: true, PassingOutcomes: []string{"o-pass"}},
		{TestID: "t-opt", Kind: KindVariable, Required: false, Decimals: 0, Max: ptrDec("1")},
	}
	in := map[string]CertValue{
		"t-var": {Value: ptrDec("1.5")},
		"t-att": {Outcome: "o-pass"},
	}
	if !CertInSpec(lines, in) {
		t.Error("certificate within specification must pass")
	}
	out := map[string]CertValue{"t-var": {Value: ptrDec("2.6")}, "t-att": {Outcome: "o-pass"}}
	if CertInSpec(lines, out) {
		t.Error("value 2.6 rounds to 2.6, above max 2")
	}
	if CertInSpec(lines, map[string]CertValue{"t-var": {Value: ptrDec("1.5")}}) {
		t.Error("a required test without a value must fail")
	}
}

func ptrDec(s string) *decimal.Decimal {
	v := decimal.RequireFromString(s)
	return &v
}

func TestSourceInspectionCover(t *testing.T) {
	accepted := []SourceAcceptance{{POLine: "PO-1", Batch: "B", AcceptedQty: dec("1000")}}
	// AC-33.14: 1,000 accepted for B covers 1,000 of B; batch C gets a normal order.
	b, err := SourceInspectionCover(accepted, "PO-1", "B", dec("1000"))
	if err != nil || !b.Skip || !b.NormalQty.IsZero() {
		t.Errorf("batch B: %+v %v", b, err)
	}
	c, _ := SourceInspectionCover(accepted, "PO-1", "C", dec("1000"))
	if c.Skip || !c.NormalQty.Equal(dec("1000")) {
		t.Errorf("batch C: %+v", c)
	}
	// Excess over the accepted quantity still needs receiving inspection.
	p, _ := SourceInspectionCover(accepted, "PO-1", "B", dec("1200"))
	if p.Skip || !p.CoveredQty.Equal(dec("1000")) || !p.NormalQty.Equal(dec("200")) {
		t.Errorf("excess: %+v", p)
	}
	if _, err := SourceInspectionCover(accepted, "PO-1", "B", dec("0")); err == nil {
		t.Error("zero receipt must fail")
	}
}

func TestSourceInspectionCoverProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		acc := decimal.NewFromInt(rapid.Int64Range(0, 2000).Draw(t, "accepted"))
		recv := decimal.NewFromInt(rapid.Int64Range(1, 2000).Draw(t, "received"))
		cov, err := SourceInspectionCover([]SourceAcceptance{{POLine: "L", Batch: "B", AcceptedQty: acc}}, "L", "B", recv)
		if err != nil {
			t.Fatal(err)
		}
		if !cov.CoveredQty.Add(cov.NormalQty).Equal(recv) {
			t.Fatalf("covered + normal != received: %+v", cov)
		}
		if cov.Skip != acc.GreaterThanOrEqual(recv) {
			t.Fatalf("skip must hold iff accepted >= received: accepted %s received %s skip %v", acc, recv, cov.Skip)
		}
	})
}
