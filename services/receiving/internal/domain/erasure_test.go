package domain

import (
	"testing"
	"time"
)

func TestPayeeRetentionEnd_KeysOnTheEntityCountryAndFailsClosed(t *testing.T) {
	activity := time.Date(2018, 3, 1, 0, 0, 0, 0, time.UTC)
	if end, ok := PayeeRetentionEnd("gb", activity); !ok || !end.Equal(activity.AddDate(7, 0, 0)) {
		t.Fatalf("GB: %v %v", end, ok)
	}
	for _, country := range []string{"DE", " fr ", "", "X", "GBR"} {
		if _, ok := PayeeRetentionEnd(country, activity); ok {
			t.Errorf("%q must be retained", country)
		}
	}
}

func TestParseLegalEntityCountries(t *testing.T) {
	got, err := ParseLegalEntityCountries("0192F000-0000-7000-8000-000000000001=gb, 0192f000-0000-7000-8000-000000000002=DE,")
	if err != nil || got["0192f000-0000-7000-8000-000000000001"] != "GB" || got["0192f000-0000-7000-8000-000000000002"] != "DE" {
		t.Fatalf("%v %v", got, err)
	}
	if empty, err := ParseLegalEntityCountries(""); err != nil || len(empty) != 0 {
		t.Fatalf("%v %v", empty, err)
	}
	for _, bad := range []string{"nope", "0192f000-0000-7000-8000-000000000001=GBR", "=GB", "0192f000-0000-7000-8000-000000000001"} {
		if _, err := ParseLegalEntityCountries(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
