package domain

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"pgregory.net/rapid"
)

func TestDimensions_Canonical(t *testing.T) {
	tests := []struct {
		name string
		in   Dimensions
		want string
	}{
		{"empty", Dimensions{}, ""},
		{"blank values are not set", Dimensions{DimSite: "  ", DimBatch: ""}, ""},
		{"site and batch in canonical order", Dimensions{DimBatch: "B-7", DimSite: "S1"}, "site=2:S1;batch=3:B-7;"},
		{"owner comes last", Dimensions{DimOwner: "V1", DimWarehouse: "W1"}, "warehouse=2:W1;owner=2:V1;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := tt.in.Normalize()
			if err != nil {
				t.Fatal(err)
			}
			if got := n.Canonical(); got != tt.want {
				t.Fatalf("Canonical() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDimensions_Normalize_rejects(t *testing.T) {
	if _, err := (Dimensions{"colour": "RED"}).Normalize(); err == nil {
		t.Fatal("unknown dimension name must be rejected")
	}
	long := make([]byte, maxDimensionValue+1)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := (Dimensions{DimBatch: string(long)}).Normalize(); err == nil {
		t.Fatal("over-long value must be rejected")
	}
}

func TestDimensions_HashIsSHA256OfCanonical(t *testing.T) {
	d, _ := Dimensions{DimSite: "S1", DimBatch: "B-7"}.Normalize()
	want := sha256.Sum256([]byte("site=2:S1;batch=3:B-7;"))
	if !bytes.Equal(d.Hash(), want[:]) {
		t.Fatalf("hash %x does not match SHA-256 of canonical form", d.Hash())
	}
}

func TestDimensions_HashIgnoresBlanksAndDistinguishesValues(t *testing.T) {
	a, _ := Dimensions{DimSite: "S1"}.Normalize()
	b, _ := Dimensions{DimSite: "S1", DimBatch: " "}.Normalize()
	c, _ := Dimensions{DimSite: "S2"}.Normalize()
	d, _ := Dimensions{DimWarehouse: "S1"}.Normalize()
	if !bytes.Equal(a.Hash(), b.Hash()) {
		t.Fatal("blank dimensions must not change the hash")
	}
	if bytes.Equal(a.Hash(), c.Hash()) || bytes.Equal(a.Hash(), d.Hash()) {
		t.Fatal("different values or names must produce different hashes")
	}
}

func TestDimensions_HashIsInjectiveOnSmallSpace(t *testing.T) {
	// Length prefixes stop "ab"+"c" from colliding with "a"+"bc" across dimensions.
	x, _ := Dimensions{DimSite: "ab", DimBatch: "c"}.Normalize()
	y, _ := Dimensions{DimSite: "a", DimBatch: "bc"}.Normalize()
	if bytes.Equal(x.Hash(), y.Hash()) {
		t.Fatal("split-point collision")
	}
}

func TestDimensions_HashDeterministicProperty(t *testing.T) {
	names := DimensionNames()
	rapid.Check(t, func(t *rapid.T) {
		d := Dimensions{}
		for _, n := range names {
			if rapid.Bool().Draw(t, "set") {
				d[n] = rapid.StringMatching(`[A-Z0-9-]{1,12}`).Draw(t, n)
			}
		}
		a, err := d.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := a.Normalize()
		if !bytes.Equal(a.Hash(), b.Hash()) || len(a.Hash()) != 32 {
			t.Fatal("hash must be deterministic and 32 bytes")
		}
	})
}
