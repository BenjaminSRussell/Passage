package main

import "testing"

func TestParseRMATaxAccounts(t *testing.T) {
	le := "00000000-0000-0000-0000-0000000000a1"
	got, err := parseRMATaxAccounts(`{"` + le + `":{"DE19":"2310","DE7":"2320"}}`)
	if err != nil || got[le]["DE19"] != "2310" || got[le]["DE7"] != "2320" {
		t.Fatalf("valid: %+v %v", got, err)
	}
	if got, err := parseRMATaxAccounts(""); err != nil || len(got) != 0 {
		t.Fatalf("unset: %+v %v", got, err)
	}
	for name, raw := range map[string]string{
		"not json":            `nope`,
		"legal entity not id": `{"x":{"DE19":"2310"}}`,
		"empty account":       `{"` + le + `":{"DE19":""}}`,
		"flat map":            `{"` + le + `":"2310"}`,
	} {
		if _, err := parseRMATaxAccounts(raw); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}
