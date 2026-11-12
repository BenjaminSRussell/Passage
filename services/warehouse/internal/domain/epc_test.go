package domain

import (
	"errors"
	"fmt"
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

// The schemes longer than 96 bits: the serial is 7-bit characters, the tag is padded to a whole 16-bit word.
func TestDecodeEPC_LongSchemesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		header           int
		scheme           string
		cp, ref, serial  string
		hexLen           int
		wantKey, wantURN string
	}{
		{0x36, "SGTIN-198", "0614141", "712345", "32a/b", 52, "70614141123451", "urn:epc:id:sgtin:0614141.712345.32a%2Fb"},
		{0x39, "SGLN-195", "0614141", "12345", "Dock-3", 52, "0614141123452", "urn:epc:id:sgln:0614141.12345.Dock-3"},
		{0x37, "GRAI-170", "0614141", "12345", "ABC123", 44, "00614141123452", "urn:epc:id:grai:0614141.12345.ABC123"},
		{0x3E, "GDTI-174", "0614141", "12345", "PO-77", 44, "0614141123452", "urn:epc:id:gdti:0614141.12345.PO-77"},
		{0x38, "GIAI-202", "0614141", "", "FORK&LIFT-12", 52, "0614141FORK&LIFT-12", "urn:epc:id:giai:0614141.FORK%26LIFT-12"},
	} {
		hex, err := EncodeEPCString(tc.header, 1, tc.cp, tc.ref, tc.serial)
		if err != nil {
			t.Fatalf("%s: %v", tc.scheme, err)
		}
		if len(hex) != tc.hexLen {
			t.Fatalf("%s: %d hex characters, want %d", tc.scheme, len(hex), tc.hexLen)
		}
		got, err := DecodeEPC(hex)
		if err != nil || got.Scheme != tc.scheme || got.Filter != 1 || got.CompanyPrefix != tc.cp || got.Key != tc.wantKey || got.URN != tc.wantURN {
			t.Fatalf("%s: %+v %v", tc.scheme, got, err)
		}
		if tc.scheme != "GIAI-202" && got.Serial != tc.serial {
			t.Fatalf("%s serial %q", tc.scheme, got.Serial)
		}
		// a reader that reports only the scheme's own bits, rounded to a hex digit, is read the same
		trimmed := hex[:(epcSchemes[tc.header].bits+3)/4]
		if again, err := DecodeEPC(trimmed); err != nil || again.URN != got.URN {
			t.Fatalf("%s trimmed: %+v %v", tc.scheme, again, err)
		}
	}
}

// SGTIN-198 is laid out bit by bit as GS1 TDS 14.5.1.2 has it: header 00110110, filter, partition, company prefix and
// item reference as in SGTIN-96, then the serial in 7-bit characters.
func TestDecodeEPC_SGTIN198Layout(t *testing.T) {
	hex, err := EncodeEPCString(0x36, 3, "0614141", "712345", "AB")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := new(big.Int).SetString(hex, 16)
	b := epcBits{v: v, width: 208}
	if b.at(0, 8).Int64() != 0x36 || b.at(8, 3).Int64() != 3 || b.at(11, 3).Int64() != 5 || b.at(14, 24).Int64() != 614141 || b.at(38, 20).Int64() != 712345 {
		t.Fatalf("header fields of %s", hex)
	}
	if b.at(58, 7).Int64() != 'A' || b.at(65, 7).Int64() != 'B' || b.at(72, 7).Int64() != 0 || b.at(72, 136).Sign() != 0 {
		t.Fatalf("serial characters of %s", hex)
	}
	// the same item as SGTIN-96 gives the same GTIN
	short, _ := EncodeEPC96(0x30, 3, "0614141", "712345", big.NewInt(1))
	a, _ := DecodeEPC(short)
	c, _ := DecodeEPC(hex)
	if a.Key != c.Key {
		t.Fatalf("GTIN of SGTIN-96 %s and SGTIN-198 %s", a.Key, c.Key)
	}
}

func TestDecodeEPC_GIDAndGSRNP(t *testing.T) {
	// GID-96: header 0x35, manager 95100000, class 12345, serial 400
	v := new(big.Int).Lsh(big.NewInt(0x35), 88)
	v.Or(v, new(big.Int).Lsh(big.NewInt(95100000), 60))
	v.Or(v, new(big.Int).Lsh(big.NewInt(12345), 36))
	v.Or(v, big.NewInt(400))
	got, err := DecodeEPC(fmt.Sprintf("%024X", v))
	if err != nil || got.Scheme != "GID-96" || got.URN != "urn:epc:id:gid:95100000.12345.400" {
		t.Fatalf("GID-96: %+v %v", got, err)
	}
	gsrn, _ := EncodeEPC96(0x2D, 0, "0614141", "1234567890", nil)
	p, err := DecodeEPC("2E" + gsrn[2:])
	if err != nil || p.Scheme != "GSRNP-96" || !strings.HasPrefix(p.URN, "urn:epc:id:gsrnp:0614141.1234567890") {
		t.Fatalf("GSRNP-96: %+v %v", p, err)
	}
}

func TestDecodeEPC_RefusesBrokenLongTags(t *testing.T) {
	good, err := EncodeEPCString(0x36, 1, "0614141", "712345", "AB")
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]string{
		"96 bits of a 198-bit scheme": good[:24],
		"padding not zero":            good[:51] + "1",
		"too long":                    good + "0000",
		"empty serial":                good[:14] + strings.Repeat("0", 38),
		"character outside the set": func() string {
			h, _ := EncodeEPCString(0x36, 1, "0614141", "712345", "A")
			v, _ := new(big.Int).SetString(h, 16)
			v.Or(v, new(big.Int).Lsh(big.NewInt(0x7F^'A'), 208-65))
			return fmt.Sprintf("%052X", v)
		}(),
		"text after the end": func() string {
			v, _ := new(big.Int).SetString(good, 16)
			v.Or(v, new(big.Int).Lsh(big.NewInt('C'), 208-86))
			return fmt.Sprintf("%052X", v)
		}(),
	} {
		_, err := DecodeEPC(in)
		var de *Error
		if err == nil || !errors.As(err, &de) || de.Code != CodeEPCInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := EncodeEPCString(0x36, 1, "0614141", "712345", "with space"); err == nil {
		t.Error("a space is not in the character set")
	}
	if _, err := EncodeEPCString(0x36, 1, "0614141", "712345", strings.Repeat("A", 21)); err == nil {
		t.Error("21 characters do not fit SGTIN-198")
	}
}
