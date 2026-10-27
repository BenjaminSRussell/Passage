package domain

import (
	"errors"
	"testing"
)

func TestContainerCheckDigit(t *testing.T) {
	got, err := ContainerCheckDigit("CSQU305438")
	if err != nil || got != 3 {
		t.Fatalf("CSQU305438 check digit = %d, %v; want 3", got, err)
	}
}

func TestValidContainerNumber(t *testing.T) {
	tests := []struct {
		name   string
		number string
		ok     bool
	}{
		{"BR-34.12 example is valid", "CSQU3054383", true},
		{"wrong check digit is rejected with TMS-0124", "CSQU3054384", false},
		{"lowercase input is normalized", "csqu3054383", true},
		{"short number is rejected", "CSQU305438", false},
		{"digit in owner code is rejected", "CSQ13054383", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidContainerNumber(tc.number)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidContainerNumber(%s) = %v, want ok=%v", tc.number, err, tc.ok)
			}
			if err != nil {
				var de *Error
				if !errors.As(err, &de) || de.Code != CodeContainerCheck {
					t.Fatalf("error = %v, want TMS-0124", err)
				}
			}
		})
	}
}

func TestLetterValue_SkipsMultiplesOfEleven(t *testing.T) {
	want := map[byte]int{'A': 10, 'B': 12, 'K': 21, 'L': 23, 'U': 32, 'V': 34, 'Z': 38}
	for c, v := range want {
		if got := letterValue(c); got != v {
			t.Fatalf("letterValue(%c) = %d, want %d", c, got, v)
		}
	}
}
