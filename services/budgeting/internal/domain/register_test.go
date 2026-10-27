package domain

import (
	"errors"
	"testing"
)

func TestValidateEntry(t *testing.T) {
	tests := []struct {
		name  string
		typ   EntryType
		lines []RegisterLine
		code  string
	}{
		{"original accepted", EntryOriginal, []RegisterLine{{"d1", "FY2026", d("100")}}, ""},
		{"balanced transfer accepted", EntryTransfer, []RegisterLine{{"d1", "FY2026", d("-40")}, {"d2", "FY2026", d("40")}}, ""},
		{"unbalanced transfer is CO-0604", EntryTransfer, []RegisterLine{{"d1", "FY2026", d("-40")}, {"d2", "FY2026", d("30")}}, CodeTransferUnbalanced},
		{"empty entry rejected", EntryRevision, nil, "COM-0001"},
		{"encumbrance type is not a register entry", EntryType("Encumbrance"), []RegisterLine{{"d1", "FY2026", d("1")}}, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEntry(tc.typ, tc.lines)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != tc.code {
				t.Fatalf("error = %v, want code %s", err, tc.code)
			}
		})
	}
}
