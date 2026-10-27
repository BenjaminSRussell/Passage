package domain

import (
	"strings"
)

// zplField strips ZPL control characters so a value cannot inject commands
// into the label program.
func zplField(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '^', '~', '\n', '\r':
			return ' '
		}
		return r
	}, s)
}

// ContainerLabelZPL renders the GS1-128 container label as a ZPL program. The
// barcode encodes the SSCC as application identifier 00 after an FNC1 (ZPL ">8")
// in code set C (">;"). The SSCC is validated first (BR-28.5), so a label is
// never printed with a bad check digit.
func ContainerLabelZPL(sscc, shipmentNumber, containerType string) (string, error) {
	if err := ValidSSCC(sscc); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("^XA\n^PW812\n^LL406\n")
	b.WriteString("^FO40,30^A0N,30,30^FDSHIPMENT " + zplField(shipmentNumber) + "^FS\n")
	b.WriteString("^FO40,75^A0N,26,26^FDTYPE " + zplField(containerType) + "^FS\n")
	b.WriteString("^FO40,120^BY3^BCN,120,N,N,N^FD>;>800" + sscc + "^FS\n")
	b.WriteString("^FO40,290^A0N,28,28^FD(00) " + sscc + "^FS\n")
	b.WriteString("^XZ\n")
	return b.String(), nil
}
