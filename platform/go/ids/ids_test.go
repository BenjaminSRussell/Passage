package ids

import (
	"sort"
	"testing"
	"time"
)

func TestNewIsValidV7AndTimeOrdered(t *testing.T) {
	a := New()
	if !Valid(a) || a[14] != '7' {
		t.Fatalf("expected a UUIDv7, got %s", a)
	}
	time.Sleep(2 * time.Millisecond)
	b := New()
	ids := []string{b, a}
	sort.Strings(ids)
	if ids[0] != a {
		t.Fatalf("ids issued later must sort later: %s before %s", b, a)
	}
	if !Valid(Nil) || Valid("not-a-uuid") {
		t.Fatal("Valid misclassified")
	}
}

func TestCanonicalNormalisesEveryAcceptedForm(t *testing.T) {
	want := "0192a3b4-c5d6-7e8f-9a0b-1c2d3e4f5a6b"
	for _, in := range []string{want, "0192A3B4-C5D6-7E8F-9A0B-1C2D3E4F5A6B", "0192a3b4c5d67e8f9a0b1c2d3e4f5a6b", "{" + want + "}"} {
		if got, ok := Canonical(in); !ok || got != want {
			t.Fatalf("Canonical(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := Canonical("nope"); ok {
		t.Fatal("Canonical accepted a non-UUID")
	}
}
