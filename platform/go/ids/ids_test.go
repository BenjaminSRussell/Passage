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
