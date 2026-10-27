package testkit

import "testing"

func TestListedIn(t *testing.T) {
	const store = "services/billing/internal/store"
	cases := []struct {
		name string
		list string
		dir  string
		want bool
	}{
		{"exact suffix", store, "/repo/" + store, true},
		{"one of several", "services/a/x " + store, "/repo/" + store, true},
		{"sibling with the same prefix", store, "/repo/" + store + "x", false},
		{"subpackage", store, "/repo/" + store + "/sub", false},
		{"different parent with the same tail", "billing/internal/store", "/repo/mybilling/internal/store", false},
		{"empty list", "", "/repo/" + store, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := listedIn(c.list, c.dir); got != c.want {
				t.Fatalf("listedIn(%q, %q) = %v, want %v", c.list, c.dir, got, c.want)
			}
		})
	}
}
