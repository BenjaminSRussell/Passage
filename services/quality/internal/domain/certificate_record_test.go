package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func certGroup() (TestGroup, string) {
	pass := ids.New()
	return TestGroup{ID: ids.New(), Code: "GRP-STEEL", Version: 1, Tests: []TestLine{
		{TestID: "t-yield", Name: "yield strength", Kind: KindVariable, Required: true, Decimals: 0, Min: ptrDec("355")},
		{TestID: "t-surface", Name: "surface", Kind: KindAttribute, Required: true, PassingOutcomes: []string{pass}},
		{TestID: "t-hard", Name: "hardness", Kind: KindVariable, Required: false, Decimals: 0, Max: ptrDec("200")},
	}}, pass
}

func TestValidateCertificate(t *testing.T) {
	group, pass := certGroup()
	ok := map[string]CertValue{"t-yield": {Value: ptrDec("360")}, "t-surface": {Outcome: pass}}
	if f := ValidateCertificate(CertEN10204_31, "EDI-1", ok, group); len(f) != 0 {
		t.Fatalf("valid certificate: %v", f)
	}
	cases := []struct {
		name, certType, ref string
		values              map[string]CertValue
		field               string
	}{
		{"NONE is not a certificate", CertNone, "", nil, "type"},
		{"unknown type", "MILL", "", nil, "type"},
		{"document reference too long", CertCoA, strings.Repeat("x", 201), nil, "documentRef"},
		{"value for a test outside the group", CertCoA, "", map[string]CertValue{"t-other": {Value: ptrDec("1")}}, "values.t-other"},
		{"outcome on a variable test", CertCoA, "", map[string]CertValue{"t-yield": {Outcome: pass}}, "values.t-yield"},
		{"more than 6 decimals", CertCoA, "", map[string]CertValue{"t-yield": {Value: ptrDec("1.1234567")}}, "values.t-yield"},
		{"value on an attribute test", CertCoA, "", map[string]CertValue{"t-surface": {Value: ptrDec("1")}}, "values.t-surface"},
		{"outcome that is not an id", CertCoA, "", map[string]CertValue{"t-surface": {Outcome: "smooth"}}, "values.t-surface"},
	}
	for _, c := range cases {
		if f := ValidateCertificate(c.certType, c.ref, c.values, group); f[c.field] == "" {
			t.Errorf("%s: want an error on %s, got %v", c.name, c.field, f)
		}
	}
}

func TestEvaluateCertificate(t *testing.T) {
	group, pass := certGroup()
	inSpec, dev := EvaluateCertificate(CertEN10204_31, map[string]CertValue{"t-yield": {Value: ptrDec("354.6")}, "t-surface": {Outcome: pass}}, group)
	if !inSpec || len(dev) != 0 {
		t.Fatalf("354.6 rounds to 355, the minimum (inclusive): %v %v", inSpec, dev)
	}
	inSpec, dev = EvaluateCertificate(CertCoA, map[string]CertValue{"t-yield": {Value: ptrDec("354.4")}, "t-hard": {Value: ptrDec("250")}}, group)
	if inSpec || strings.Join(dev, ",") != "yield strength,surface,hardness" {
		t.Fatalf("low yield, missing surface and an optional value over its limit are deviations: %v %v", inSpec, dev)
	}
	if inSpec, dev := EvaluateCertificate(CertCoC, nil, group); !inSpec || dev == nil || len(dev) != 0 {
		t.Fatalf("a certificate of conformance without values conforms: %v %v", inSpec, dev)
	}
	if inSpec, _ := EvaluateCertificate(CertCoC, map[string]CertValue{"t-yield": {Value: ptrDec("300")}}, group); inSpec {
		t.Fatal("values a COC carries are still checked")
	}
	if inSpec, _ := EvaluateCertificate(CertCoA, nil, group); inSpec {
		t.Fatal("a certificate of analysis without values misses the required tests")
	}
}

func TestCertificateGate(t *testing.T) {
	order := Order{ID: "qo-1", CertificateRequired: CertEN10204_31, LotQty: decimal.NewFromInt(10)}
	split := func(accept, reject, deviation int64) Split {
		return Split{Accept: decimal.NewFromInt(accept), Reject: decimal.NewFromInt(reject), Deviation: decimal.NewFromInt(deviation)}
	}
	codeOf := func(err error) string {
		if e, ok := err.(*Error); ok {
			return e.Code
		}
		return ""
	}
	if code := codeOf(CertificateGate(order, split(10, 0, 0), nil)); code != CodeCertRequired {
		t.Fatalf("ACCEPT without the certificate (AC-33.12): %q", code)
	}
	if code := codeOf(CertificateGate(order, split(0, 5, 5), nil)); code != CodeCertRequired {
		t.Fatalf("accepting with a deviation also releases stock: %q", code)
	}
	if err := CertificateGate(order, split(0, 10, 0), nil); err != nil {
		t.Fatalf("a full reject releases nothing and is not gated: %v", err)
	}
	nok := []CertRecord{{Type: CertEN10204_31, InSpec: false}}
	if CertificateGate(order, split(10, 0, 0), nok) == nil {
		t.Fatal("a NOK certificate keeps the gate closed")
	}
	if err := CertificateGate(order, split(10, 0, 0), append(nok, CertRecord{Type: CertEN10204_31, InSpec: true})); err != nil {
		t.Fatalf("a later in-specification certificate opens it: %v", err)
	}
	if err := CertificateGate(order, split(10, 0, 0), []CertRecord{{Type: CertEN10204_31, Waiver: true, Reason: "audit"}}); err != nil {
		t.Fatalf("a waiver of the required type opens it: %v", err)
	}
	if CertificateGate(order, split(10, 0, 0), []CertRecord{{Type: CertCoC, Waiver: true}, {Type: CertCoA, InSpec: true}}) == nil {
		t.Fatal("waivers and receipts of other types do not count")
	}
	if err := CertificateGate(Order{CertificateRequired: CertNone}, split(10, 0, 0), nil); err != nil {
		t.Fatalf("no requirement, no gate: %v", err)
	}
}

func TestCheckCertificateOrder(t *testing.T) {
	codeOf := func(err error) string {
		if e, ok := err.(*Error); ok {
			return e.Code
		}
		return ""
	}
	open := Order{Status: StatusPending, CertificateRequired: CertCoA}
	if err := CheckCertificateOrder(open, true); err != nil {
		t.Fatal(err)
	}
	if code := codeOf(CheckCertificateOrder(Order{Status: StatusClosed, CertificateRequired: CertCoA}, false)); code != CodeClosed {
		t.Errorf("closed order: %q", code)
	}
	if code := codeOf(CheckCertificateOrder(Order{Status: StatusCancelled}, false)); code != CodeStatus {
		t.Errorf("cancelled order: %q", code)
	}
	if code := codeOf(CheckCertificateOrder(Order{Status: StatusCreated, CertificateRequired: CertNone}, true)); code != CodeCertNotRequired {
		t.Errorf("waiver without a requirement: %q", code)
	}
	if err := CheckCertificateOrder(Order{Status: StatusCreated, CertificateRequired: CertNone}, false); err != nil {
		t.Errorf("an optional certificate may be recorded on any open order: %v", err)
	}
}

func TestCertificateNCR(t *testing.T) {
	order := Order{ID: "qo-7", ItemID: ids.New(), Dims: map[string]string{"batch": "B-7"}, LotQty: decimal.RequireFromString("40.5"), UOM: "KG"}
	rec := CertRecord{Type: CertEN10204_31, Deviations: []string{"yield strength", "surface"}, RecordedBy: "qa-1"}
	n, err := CertificateNCR(order, rec, time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if n.Source != SourceVendor || n.ProblemType != ProblemCertificateNOK || n.Severity != NCRMajor || n.QualityOrderID != "qo-7" ||
		!n.Qty.Equal(order.LotQty) || n.UOM != "KG" || n.Dims["batch"] != "B-7" || n.Status != NCRNew || n.CreatedBy != "qa-1" {
		t.Fatalf("certificate NCR: %+v", n)
	}
	if !strings.Contains(n.Description, "EN10204_31") || !strings.HasSuffix(n.Description, ": surface, yield strength.") {
		t.Fatalf("description names the type and the deviations: %q", n.Description)
	}
}
