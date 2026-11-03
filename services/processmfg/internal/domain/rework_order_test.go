package domain

import "testing"

func TestCheckReworkFormula(t *testing.T) {
	if err := CheckReworkFormula("item-a", "item-a"); err != nil {
		t.Fatalf("same item: %v", err)
	}
	if err := CheckReworkFormula("item-a", "item-b"); err == nil || err.(*Error).Code != CodeInvalid {
		t.Fatalf("other item: %v", err)
	}
}

func TestCheckReworkOutputNumber(t *testing.T) {
	for _, tc := range []struct {
		keep   bool
		number string
		ok     bool
	}{{true, "", true}, {true, "L-2", false}, {false, "L-2", true}, {false, "", false}} {
		err := CheckReworkOutputNumber(tc.keep, tc.number)
		if (err == nil) != tc.ok {
			t.Errorf("keep=%v number=%q: %v", tc.keep, tc.number, err)
		}
	}
}
