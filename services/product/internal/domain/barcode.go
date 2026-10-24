package domain

import (
	"fmt"
	"strings"
)

// EAN-13 encodings: L (odd), G (even), R (right). First digit selects the LG pattern.
var (
	eanL = [10]string{"0001101", "0011001", "0010011", "0111101", "0100011", "0110001", "0101111", "0111011", "0110111", "0001011"}
	eanG = [10]string{"0100111", "0110011", "0011011", "0100001", "0011101", "0111001", "0000101", "0010001", "0001001", "0010111"}
	eanR = [10]string{"1110010", "1100110", "1101100", "1000010", "1011100", "1001110", "1010000", "1000100", "1001000", "1110100"}
	eanP = [10]string{"LLLLLL", "LLGLGG", "LLGGLG", "LLGGGL", "LGLLGG", "LGGLLG", "LGGGLL", "LGLGLG", "LGLGGL", "LGGLGL"}
)

// EncodeEAN13 returns the 95-module bit string for a validated GTIN-13.
func EncodeEAN13(gtin string) (string, error) {
	if len(gtin) != 13 {
		return "", errf(CodeGTINCheckDigit, "EAN-13 requires a 13-digit GTIN")
	}
	if err := ValidateGTIN(gtin); err != nil {
		return "", err
	}
	pattern := eanP[gtin[0]-'0']
	var b strings.Builder
	b.WriteString("101")
	for i := 0; i < 6; i++ {
		d := gtin[i+1] - '0'
		if pattern[i] == 'G' {
			b.WriteString(eanG[d])
		} else {
			b.WriteString(eanL[d])
		}
	}
	b.WriteString("01010")
	for i := 7; i < 13; i++ {
		b.WriteString(eanR[gtin[i]-'0'])
	}
	b.WriteString("101")
	return b.String(), nil
}

// RenderEAN13SVG draws a scannable-width EAN-13 as SVG (FR-8.9).
func RenderEAN13SVG(gtin string) (string, error) {
	bits, err := EncodeEAN13(gtin)
	if err != nil {
		return "", err
	}
	const module = 2
	const height = 80
	width := len(bits)*module + 20
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, width, height+24, width, height+24)
	b.WriteString(`<rect width="100%" height="100%" fill="#fff"/>`)
	x := 10
	for i := 0; i < len(bits); i++ {
		if bits[i] == '1' {
			fmt.Fprintf(&b, `<rect x="%d" y="8" width="%d" height="%d" fill="#000"/>`, x, module, height)
		}
		x += module
	}
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="monospace" font-size="14" text-anchor="middle">%s</text>`, width/2, height+20, gtin)
	b.WriteString(`</svg>`)
	return b.String(), nil
}
