package domain

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestCode128Modules_KnownSymbols(t *testing.T) {
	cases := []struct {
		name string
		data string
		want int
	}{
		// start C, FNC1, 10 digit pairs, check: 13 characters x 11 + 13 stop.
		{"GS1 SSCC in subset C", ">;>800340123456789012340", 13*11 + 13},
		// no start code: subset B, 7 characters plus start and check.
		{"plain location in subset B", "A-01-02", 9*11 + 13},
		{"explicit start B", ">:A-01-02", 9*11 + 13},
		// start C, FNC1, pair "10", switch B, "AB", FNC1, switch C, pair "17": 9 + start and check.
		{"code set switches count as characters", ">;>810>6AB>8>517", 10*11 + 13},
		{"an odd digit in subset C takes a character of its own", ">;123", 4*11 + 13},
	}
	for _, c := range cases {
		if got := code128Modules(c.data); got != c.want {
			t.Errorf("%s: code128Modules(%q) = %d, want %d", c.name, c.data, got, c.want)
		}
	}
}

// decodeGS1 strips the start code, the leading FNC1 and the code set switches
// from gs1Code128 output, giving back the element string, and checks that every
// subset C stretch holds only digit pairs and FNC1.
func decodeGS1(t *rapid.T, data string) string {
	var subsetC bool
	switch {
	case strings.HasPrefix(data, startC+fnc1):
		subsetC = true
	case strings.HasPrefix(data, startB+fnc1):
	default:
		t.Fatalf("%q does not open with a start code and FNC1", data)
	}
	data = data[len(startC+fnc1):]
	var out strings.Builder
	for i := 0; i < len(data); {
		switch {
		case strings.HasPrefix(data[i:], switchC):
			subsetC = true
			i += 2
		case strings.HasPrefix(data[i:], switchB):
			subsetC = false
			i += 2
		case strings.HasPrefix(data[i:], fnc1):
			out.WriteString(fnc1)
			i += 2
		case subsetC:
			if i+1 >= len(data) || !isDigit(data[i]) || !isDigit(data[i+1]) {
				t.Fatalf("subset C at %d of %q is not a digit pair", i, data)
			}
			out.WriteString(data[i : i+2])
			i += 2
		default:
			out.WriteByte(data[i])
			i++
		}
	}
	return out.String()
}

// The encoder keeps the data, writes subset C only for digit pairs, and is never
// longer than the same data in subset B alone.
func TestGS1Code128_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		parts := rapid.SliceOfN(rapid.SampledFrom([]string{"0", "1", "7", "9", "A", "z", "-", fnc1}), 1, 60).Draw(t, "parts")
		value := strings.Join(parts, "")
		got := gs1Code128(value)
		if back := decodeGS1(t, got); back != value {
			t.Fatalf("decoded %q from %q, want %q", back, got, value)
		}
		if enc, allB := code128Modules(got), code128Modules(startB+fnc1+value); enc > allB {
			t.Fatalf("%q is %d modules, longer than subset B alone (%d)", got, enc, allB)
		}
	})
}

// The GS1 carton bar code (SSCC, PO and postal code) fits a 4 in label only with
// subset C for its digit runs: in subset B alone it is about 4.4 in wide.
func TestGS1Code128_CartonFitsFourInches(t *testing.T) {
	value := "00340123456789012340" + "400PO-7781" + fnc1 + "42060601"
	w, _, _ := LabelDots(Media4x6In, LayoutDpi)
	if allB := code128Modules(startB+fnc1+value) * defaultModuleWidth; allB <= w {
		t.Fatalf("subset B alone is %d dots, the test no longer shows the need for subset C", allB)
	}
	if got := code128Modules(gs1Code128(value)) * defaultModuleWidth; got+2*quietZoneModules*defaultModuleWidth > w-40 {
		t.Fatalf("carton bar code is %d dots, too wide for the 4 x 6 in layout", got)
	}
}
