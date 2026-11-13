package domain

import "testing"

func TestReadbackTolerance_ByNodeType(t *testing.T) {
	for _, tc := range []struct {
		typ           string
		written, back float64
		match         bool
	}{
		{"Double", 12.5, 12.5009, true},
		{"Double", 12.5, 12.502, false},
		{"", 12.5, 12.5009, true},
		{"Float", 1234567.89, float64(float32(1234567.89)), true}, // 1234567.875: the rounding of a 32-bit float
		{"Double", 1234567.89, float64(float32(1234567.89)), false},
		{"Float", 12.5, 12.6, false},
		{"Int32", 12, 12, true},
		{"Int32", 12, 12.0009, false},
		{"Boolean", 1, 0, false},
	} {
		if got := ReadbackMatches(tc.written, tc.back, ReadbackTolerance(tc.typ, tc.written)); got != tc.match {
			t.Errorf("%s %v read back %v: match %v, want %v", tc.typ, tc.written, tc.back, got, tc.match)
		}
	}
	if !KnownNodeType("UInt16") || KnownNodeType("String") {
		t.Fatal("known node types")
	}
}
