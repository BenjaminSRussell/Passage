package domain_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/services/numbering/internal/domain"
)

func def(mode string) domain.Definition {
	return domain.Definition{
		Code: "INV", Name: "Invoices", Format: "INV-{######}",
		LegalEntityID: "00000000-0000-0000-0000-000000000009", Mode: mode, StartFrom: 1,
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		format string
		n      int64
		want   string
		err    bool
	}{
		{"INV-{######}", 1, "INV-000001", false},
		{"INV-{######}", 42, "INV-000042", false},
		{"{0000}", 12, "0012", false},
		{"A-{###}-B", 7, "A-007-B", false},
		{"INV-{######}", 0, "", true},
		{"INV-{#}", 10, "", true},
		{"no-token", 1, "", true},
		{"{##} and {##}", 1, "", true},
	}
	for _, tc := range tests {
		got, err := domain.FormatValue(tc.format, tc.n)
		if tc.err {
			if err == nil {
				t.Fatalf("FormatValue(%q,%d) = %q, want error", tc.format, tc.n, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("FormatValue(%q,%d) = %q, %v; want %q", tc.format, tc.n, got, err, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := def(domain.ModeGapFree)
	if fields := domain.Validate(ok); len(fields) != 0 {
		t.Fatalf("valid definition rejected: %v", fields)
	}
	bad := ok
	bad.Code, bad.Name, bad.Format, bad.Mode, bad.StartFrom, bad.LegalEntityID = "1inv", "", "x", "other", 0, ""
	fields := domain.Validate(bad)
	for _, key := range []string{"code", "name", "format", "mode", "startFrom", "legalEntityId"} {
		if fields[key] == "" {
			t.Errorf("expected field error for %s, got %v", key, fields)
		}
	}
}

func TestGapFreeTenThousandConcurrentNoGapsOrDuplicates(t *testing.T) {
	const n = 10_000
	alloc := domain.NewMemoryAllocator()
	d := def(domain.ModeGapFree)
	values := make([]int64, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			got, err := alloc.Allocate("tenant-1", d)
			if err != nil {
				t.Errorf("allocate %d: %v", i, err)
				return
			}
			values[i] = got.Value
			want := fmt.Sprintf("INV-%06d", got.Value)
			if got.Formatted != want {
				t.Errorf("formatted %q, want %q", got.Formatted, want)
			}
		}(i)
	}
	wg.Wait()
	seen := make([]bool, n+1)
	for _, v := range values {
		if v < 1 || v > n {
			t.Fatalf("value %d outside 1..%d", v, n)
		}
		if seen[v] {
			t.Fatalf("duplicate %d", v)
		}
		seen[v] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Fatalf("gap at %d", i)
		}
	}
}

func TestAllocatorRejectsUnknownMode(t *testing.T) {
	_, err := domain.NewMemoryAllocator().Allocate("t", def("other"))
	if err == nil {
		t.Fatal("expected invalid definition")
	}
}
