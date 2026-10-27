package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestContainerLabelZPL(t *testing.T) {
	sscc, err := SSCC(0, "0614141", 12345678)
	if err != nil {
		t.Fatalf("SSCC: %v", err)
	}
	cases := []struct {
		name     string
		sscc     string
		shipment string
		wantErr  error
		contains []string
		absent   []string
	}{
		{
			name: "valid label carries FNC1 and AI 00", sscc: sscc, shipment: "SH-0000001", contains: []string{
				"^XA", "^XZ", "^BCN", ">;>800" + sscc, "(00) " + sscc, "SHIPMENT SH-0000001",
			},
		},
		{name: "bad check digit refused", sscc: sscc[:17] + string(rune('0'+(int(sscc[17]-'0')+1)%10)), wantErr: ErrCheckDigit},
		{name: "short sscc refused", sscc: "00614", wantErr: ErrSSCCFormat},
		{
			name: "control characters cannot inject ZPL", sscc: sscc, shipment: "SH^XZ~DG", contains: []string{"SHIPMENT SH XZ DG"},
			absent: []string{"SH^XZ"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			zpl, err := ContainerLabelZPL(c.sscc, c.shipment, "PALLET")
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ContainerLabelZPL: %v", err)
			}
			for _, s := range c.contains {
				if !strings.Contains(zpl, s) {
					t.Errorf("label missing %q:\n%s", s, zpl)
				}
			}
			for _, s := range c.absent {
				if strings.Contains(zpl, s) {
					t.Errorf("label contains %q:\n%s", s, zpl)
				}
			}
		})
	}
}
