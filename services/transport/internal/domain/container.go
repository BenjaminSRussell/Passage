package domain

import "strings"

// letterValue maps an ISO 6346 owner letter to its value (BR-34.12). Values skip
// multiples of 11: A=10, B=12, ..., K=21, L=23, ..., U=32, V=34, ..., Z=38.
func letterValue(c byte) int {
	v := 10
	for l := byte('A'); l < c; l++ {
		v++
		if v%11 == 0 {
			v++
		}
	}
	return v
}

// ContainerCheckDigit computes the ISO 6346 check digit of the first ten
// characters (four owner letters, six serial digits). Each character is
// weighted by 2^position; the check digit is (sum mod 11) mod 10.
func ContainerCheckDigit(first10 string) (int, error) {
	if len(first10) != 10 {
		return 0, &Error{Code: CodeContainerCheck, Detail: "container number must be 11 characters including the check digit"}
	}
	sum := 0
	for i := 0; i < 10; i++ {
		c := first10[i]
		var v int
		switch {
		case i < 4 && c >= 'A' && c <= 'Z':
			v = letterValue(c)
		case i >= 4 && c >= '0' && c <= '9':
			v = int(c - '0')
		default:
			return 0, &Error{Code: CodeContainerCheck, Detail: "owner code must be four letters and serial must be six digits"}
		}
		sum += v * (1 << i)
	}
	return (sum % 11) % 10, nil
}

// ValidContainerNumber accepts an 11-character ISO 6346 number whose last digit
// is the computed check digit. Input is case-insensitive and trimmed.
func ValidContainerNumber(number string) error {
	n := strings.ToUpper(strings.TrimSpace(number))
	if len(n) != 11 {
		return &Error{Code: CodeContainerCheck, Detail: "container number must be 11 characters"}
	}
	want, err := ContainerCheckDigit(n[:10])
	if err != nil {
		return err
	}
	if n[10] < '0' || n[10] > '9' || int(n[10]-'0') != want {
		return &Error{Code: CodeContainerCheck, Detail: "container number fails the ISO 6346 check digit"}
	}
	return nil
}
