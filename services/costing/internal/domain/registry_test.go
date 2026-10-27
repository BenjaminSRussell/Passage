package domain

import (
	"errors"
	"testing"
)

func TestCheckCategoryTypes(t *testing.T) {
	origin := []ValuationTypeSpec{{Code: "DOMESTIC", Account: "1300"}, {Code: "IMPORT", Account: "1301"}}
	cases := []struct {
		name  string
		batch bool
		types []ValuationTypeSpec
		ok    bool
	}{
		{"ORIGIN with DOMESTIC and IMPORT", false, origin, true},
		{"single condition type", false, []ValuationTypeSpec{{Code: "NEW", Account: "1300"}}, true},
		{"batch category without types", true, nil, true},
		{"batch category with fixed types", true, origin, false},
		{"split category without types", false, nil, false},
		{"type listed twice", false, []ValuationTypeSpec{{Code: "IMPORT", Account: "1"}, {Code: "IMPORT", Account: "2"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCategoryTypes(tc.batch, tc.types)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeCategoryTypes {
				t.Fatalf("err = %v, want %s", err, CodeCategoryTypes)
			}
		})
	}
}
