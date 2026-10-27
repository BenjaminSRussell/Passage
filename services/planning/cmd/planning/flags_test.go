package main

import (
	"reflect"
	"testing"
)

func TestFeatureFlags_ParsesKnownNames(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"unset enables none", "", nil},
		{"blank enables none", "   ", nil},
		{"sorted and deduplicated", "planning.mps-scope, planning.dynamic,planning.dynamic",
			[]string{"planning.dynamic", "planning.mps-scope"}},
		{"empty entries are ignored", "planning.dynamic,, ,", []string{"planning.dynamic"}},
		{"all three", "planning.statistical-forecast,planning.mps-scope,planning.dynamic",
			[]string{"planning.dynamic", "planning.mps-scope", "planning.statistical-forecast"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := featureFlags(tc.raw)
			if err != nil {
				t.Fatalf("featureFlags(%q): %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("featureFlags(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestFeatureFlags_RejectsUnknownName(t *testing.T) {
	for _, raw := range []string{"planning.dynamic,planning.dynamc", "PLANNING.DYNAMIC", "dynamic"} {
		if _, err := featureFlags(raw); err == nil {
			t.Errorf("featureFlags(%q): a misspelt flag must stop the service at startup", raw)
		}
	}
}
