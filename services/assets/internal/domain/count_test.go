package domain

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

func TestReconcileCount(t *testing.T) {
	expected := []CountAsset{{"a1", "BC-1"}, {"a2", "BC-2"}, {"a3", "BC-3"}}
	tests := []struct {
		name                             string
		scanned                          []string
		found, missing, unknown, repeats []string
		dups                             int
	}{
		{name: "all found", scanned: []string{"BC-3", "BC-1", "BC-2"}, found: []string{"BC-1", "BC-2", "BC-3"}},
		{name: "one missing", scanned: []string{"BC-1", "BC-3"}, found: []string{"BC-1", "BC-3"}, missing: []string{"BC-2"}},
		{name: "unknown barcode", scanned: []string{"XX-9", "BC-1"}, found: []string{"BC-1"}, missing: []string{"BC-2", "BC-3"}, unknown: []string{"XX-9"}},
		{name: "duplicate scans count once", scanned: []string{"BC-1", "BC-1", "XX-9", "XX-9"}, found: []string{"BC-1"}, missing: []string{"BC-2", "BC-3"}, unknown: []string{"XX-9"}, dups: 2},
		{name: "nothing scanned", missing: []string{"BC-1", "BC-2", "BC-3"}},
	}
	codes := func(lines []CountLine) []string {
		out := []string{}
		for _, l := range lines {
			out = append(out, l.Barcode)
		}
		return out
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ReconcileCount(expected, tc.scanned)
			for label, pair := range map[string][2][]string{"found": {codes(res.Found), tc.found}, "missing": {codes(res.Missing), tc.missing},
				"unknown": {codes(res.Unknown), tc.unknown}} {
				if fmt.Sprint(pair[0]) != fmt.Sprint(append([]string{}, pair[1]...)) {
					t.Errorf("%s = %v, want %v", label, pair[0], pair[1])
				}
			}
			if res.Duplicates != tc.dups {
				t.Errorf("duplicates = %d, want %d", res.Duplicates, tc.dups)
			}
		})
	}
	res := ReconcileCount(expected, []string{"BC-2", "ZZ"})
	if res.Found[0].AssetID != "a2" || res.Found[0].Outcome != CountFound || res.Missing[0].AssetID != "a1" || res.Unknown[0].AssetID != "" || res.Unknown[0].Outcome != CountUnknown {
		t.Fatalf("lines = %+v", res)
	}
}

func TestReconcileCount_PropertyOutcomesPartitionRegisterAndScans(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(t, "register")
		expected := make([]CountAsset, n)
		for i := range expected {
			expected[i] = CountAsset{AssetID: fmt.Sprintf("a%d", i), Barcode: fmt.Sprintf("BC-%d", i)}
		}
		scanned := rapid.SliceOfN(rapid.StringMatching(`BC-[0-9]{1,2}|XX-[0-9]`), 0, 60).Draw(t, "scanned")
		res := ReconcileCount(expected, scanned)
		if len(res.Found)+len(res.Missing) != n {
			t.Fatalf("found %d + missing %d != register %d", len(res.Found), len(res.Missing), n)
		}
		if len(res.Found)+len(res.Unknown)+res.Duplicates != len(scanned) {
			t.Fatalf("found %d + unknown %d + duplicates %d != scans %d", len(res.Found), len(res.Unknown), res.Duplicates, len(scanned))
		}
	})
}
