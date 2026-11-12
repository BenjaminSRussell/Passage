package domain

import (
	"errors"
	"math/big"
	"strings"
	"testing"
)

// Reference tags from GS1 TDS: the SGTIN-96 of GTIN 0 0614141 12345 with serial 6789 (company prefix 0614141, item
// reference 112345, i.e. GTIN-14 80614141123458), and the SSCC-96 of SSCC 3 0614141 123456789 (partition 5: 7-digit prefix).
func TestDecodeEPC_GS1ReferenceTags(t *testing.T) {
	sgtin, err := EncodeEPC96(0x30, 3, "0614141", "812345", big.NewInt(6789))
	if err != nil {
		t.Fatal(err)
	}
	if sgtin != "3074257BF7194E4000001A85" {
		t.Fatalf("encoding of the TDS SGTIN example: %s", sgtin)
	}
	got, err := DecodeEPC(sgtin)
	if err != nil || got.Scheme != "SGTIN-96" || got.Filter != 3 || got.CompanyPrefix != "0614141" || got.Key != "80614141123458" || got.Serial != "6789" ||
		got.URN != "urn:epc:id:sgtin:0614141.812345.6789" {
		t.Fatalf("SGTIN-96: %+v %v", got, err)
	}
	sscc, err := EncodeEPC96(0x31, 6, "0614141", "0123456789", nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := DecodeEPC(sscc)
	if err != nil || s.Scheme != "SSCC-96" || !strings.HasPrefix(s.Key, "00614141123456789") || len(s.Key) != 18 || !ValidSSCC(s.Key) || s.URN != "urn:epc:id:sscc:0614141.0123456789" {
		t.Fatalf("SSCC-96: %+v %v", s, err)
	}
}

func TestDecodeEPC_EverySchemeRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		name         string
		header       int
		cp, ref      string
		serial       *big.Int
		wantKey      string
		wantKeyValid func(string) bool
	}{
		{"SGLN", 0x32, "0614141", "12345", big.NewInt(0), "", nil},
		{"GRAI", 0x33, "0614141", "12345", big.NewInt(400), "", nil},
		{"GIAI", 0x34, "0614141", "1234567890123456789012", nil, "", nil},
		{"GSRN", 0x2D, "0614141", "0123456789", nil, "", nil},
		{"GDTI", 0x2C, "0614141", "12345", big.NewInt(2147483647), "", nil},
	} {
		cp := tc.cp
		ref := tc.ref
		// reference width depends on the partition of a 7-digit prefix
		switch tc.header {
		case 0x32, 0x2C:
			ref = "12345"
		case 0x33:
			ref = "12345"
		case 0x34:
			ref = "1234567890123456789012"[:18]
		}
		hex, err := EncodeEPC96(tc.header, 1, cp, ref, tc.serial)
		if err != nil {
			t.Fatalf("%s encode: %v", tc.name, err)
		}
		got, err := DecodeEPC(hex)
		if err != nil || got.CompanyPrefix != cp || got.Key == "" || got.URN == "" {
			t.Fatalf("%s decode of %s: %+v %v", tc.name, hex, got, err)
		}
		if tc.serial != nil && got.Serial != tc.serial.String() {
			t.Fatalf("%s serial %q, want %s", tc.name, got.Serial, tc.serial)
		}
	}
	g, _ := DecodeEPC(mustEPC(t, 0x33, "0614141", "12345", big.NewInt(1)))
	if len(g.Key) != 14 || g.Key[0] != '0' {
		t.Fatalf("GRAI key %q should be a GRAI-14", g.Key)
	}
	l, _ := DecodeEPC(mustEPC(t, 0x32, "0614141", "12345", nil))
	if len(l.Key) != 13 {
		t.Fatalf("SGLN key %q should be a GLN-13", l.Key)
	}
}

func mustEPC(t *testing.T, header int, cp, ref string, serial *big.Int) string {
	t.Helper()
	hex, err := EncodeEPC96(header, 0, cp, ref, serial)
	if err != nil {
		t.Fatal(err)
	}
	return hex
}

func TestDecodeEPC_RefusesWhatItCannotRead(t *testing.T) {
	for name, in := range map[string]string{
		"too short":           "3074257BF7194E4000001A",
		"not hex":             "ZZ74257BF7194E4000001A85",
		"unknown header":      "FF74257BF7194E4000001A85",
		"undefined partition": "3077FFFFFFFFFFFFFFFFFFFF",
		"reference too big":   "301C000000000000000FFFFF",
	} {
		_, err := DecodeEPC(in)
		var de *Error
		if err == nil || !errors.As(err, &de) || de.Code != CodeEPCInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := EncodeEPC96(0x30, 1, "123", "1", nil); err == nil {
		t.Error("a 3-digit company prefix has no partition")
	}
	if _, err := EncodeEPC96(0x30, 1, "0614141", "12", nil); err == nil {
		t.Error("a reference of the wrong width")
	}
	if _, err := EncodeEPC96(0x30, 1, "0614141", "812345", new(big.Int).Lsh(big.NewInt(1), 38)); err == nil {
		t.Error("a serial of 39 bits")
	}
}
