package domain

import (
	"errors"
	"maps"
	"testing"
)

func domainCode(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestValidateBlockRequest(t *testing.T) {
	base := Blocking{ProductID: "p", Qty: d("10"), Unit: "EA", Target: StockQuarantine, Reason: "QUALITY", Dims: Dimensions{DimSite: "S1", DimWarehouse: "W1"}}
	tests := []struct {
		name string
		mut  func(b *Blocking)
		code string
	}{
		{"valid quarantine", func(*Blocking) {}, ""},
		{"valid blocked", func(b *Blocking) { b.Target = StockBlocked }, ""},
		{"zero qty", func(b *Blocking) { b.Qty = d("0") }, CodeInvalid},
		{"too many decimals", func(b *Blocking) { b.Qty = d("1.0000001") }, CodeInvalid},
		{"unit is required", func(b *Blocking) { b.Unit = "" }, CodeInvalid},
		{"available is not a target", func(b *Blocking) { b.Target = StockAvailable }, CodeInvalid},
		{"unknown reason", func(b *Blocking) { b.Reason = "BORED" }, CodeInvalid},
		{"source already restricted", func(b *Blocking) { b.Dims[DimStatus] = StockBlocked }, CodeInvalid},
		{"site is mandatory", func(b *Blocking) { delete(b.Dims, DimSite) }, CodeDimensionMandatory},
		{"unknown dimension", func(b *Blocking) { b.Dims["shelf"] = "A" }, CodeInvalid},
		{"owner is reserved", func(b *Blocking) { b.Dims[DimOwner] = "V1" }, CodeInvalid},
		{"serial blocks one unit", func(b *Blocking) { b.Dims[DimSerial] = "S-1" }, CodeSerialQty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := base
			b.Dims = maps.Clone(base.Dims)
			tc.mut(&b)
			if got := domainCode(ValidateBlockRequest(b, 6)); got != tc.code {
				t.Fatalf("code %q, want %q", got, tc.code)
			}
		})
	}
}

func TestCheckBlockable(t *testing.T) {
	if err := CheckBlockable(d("10"), d("10")); err != nil {
		t.Fatalf("exact availability rejected: %v", err)
	}
	err := CheckBlockable(d("4"), d("-2"))
	if domainCode(err) != CodeBlockExceeds {
		t.Fatalf("over-blocking: %v, want %s", err, CodeBlockExceeds)
	}
	var de *Error
	if errors.As(err, &de) && de.Detail != "blocked quantity 4 exceeds available 0" {
		t.Fatalf("detail %q should report negative availability as 0", de.Detail)
	}
}

func TestBlockedDimsSetsStatusWithoutTouchingSource(t *testing.T) {
	src := Dimensions{DimSite: "S1", DimBatch: "B7"}
	out := BlockedDims(src, StockQuarantine)
	if out[DimStatus] != StockQuarantine || out[DimSite] != "S1" || out[DimBatch] != "B7" || len(out) != 3 {
		t.Fatalf("unexpected blocked dims %v", out)
	}
	if _, ok := src[DimStatus]; ok {
		t.Fatal("source dimensions were mutated")
	}
	if string(src.Hash()) == string(out.Hash()) {
		t.Fatal("blocked stock must land on a different dimension set")
	}
}

func TestResolveRelease(t *testing.T) {
	tests := []struct {
		name, target, state, disposition string
		want, code                       string
	}{
		{"blocked release", StockBlocked, BlockingActive, DispositionRelease, DispositionRelease, ""},
		{"blocked without disposition releases", StockBlocked, BlockingActive, "", DispositionRelease, ""},
		{"quarantine needs a disposition", StockQuarantine, BlockingActive, "", "", CodeQuarantineDisposition},
		{"quarantine release", StockQuarantine, BlockingActive, DispositionRelease, DispositionRelease, ""},
		{"quarantine scrap", StockQuarantine, BlockingActive, DispositionScrap, DispositionScrap, ""},
		{"blocked scrap", StockBlocked, BlockingActive, DispositionScrap, DispositionScrap, ""},
		{"return not yet", StockBlocked, BlockingActive, DispositionReturn, "", CodeDispositionLater},
		{"unknown disposition", StockBlocked, BlockingActive, "SELL", "", CodeInvalid},
		{"already released", StockBlocked, BlockingReleased, DispositionRelease, "", CodeBlockingState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRelease(Blocking{ID: "b1", Target: tc.target, State: tc.state}, tc.disposition)
			if domainCode(err) != tc.code || got != tc.want {
				t.Fatalf("got %q / %v, want %q / %q", got, err, tc.want, tc.code)
			}
		})
	}
}

func TestValidateReservable(t *testing.T) {
	for _, s := range []string{StockBlocked, StockQuarantine} {
		if err := ValidateReservable(Dimensions{DimStatus: s}); domainCode(err) != CodeBlockedReserve {
			t.Fatalf("%s dims: got %v", s, err)
		}
	}
	if err := ValidateReservable(Dimensions{DimSite: "S1"}); err != nil {
		t.Fatalf("unrestricted dims rejected: %v", err)
	}
}

func TestCheckEscalate(t *testing.T) {
	for _, tc := range []struct{ name, target, state, code string }{
		{"quarantine moves to blocked", StockQuarantine, BlockingActive, ""},
		{"blocked stays", StockBlocked, BlockingActive, CodeBlockingTransition},
		{"released quarantine", StockQuarantine, BlockingReleased, CodeBlockingState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := domainCode(CheckEscalate(Blocking{ID: "b1", Target: tc.target, State: tc.state})); got != tc.code {
				t.Fatalf("got %q, want %q", got, tc.code)
			}
		})
	}
}

func TestCheckExpectedRelease(t *testing.T) {
	for _, tc := range []struct{ expected, code string }{
		{"", ""}, {"2026-09-02", ""}, {"2026-10-01", ""}, {"2026-09-01", CodeInvalid},
	} {
		if got := domainCode(CheckExpectedRelease("2026-09-02", tc.expected)); got != tc.code {
			t.Fatalf("expected %q: got %q, want %q", tc.expected, got, tc.code)
		}
	}
}
