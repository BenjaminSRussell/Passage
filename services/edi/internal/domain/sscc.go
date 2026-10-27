package domain

import "fmt"

// SSCCCheckDigit returns the GS1 mod-10 check digit for the 17 digits that
// precede it. Weights alternate 3 and 1 starting with 3 at the leftmost digit,
// which is the rule that gives the digit to the left of the check position
// weight 3 (GS1 General Specifications, SSCC-18).
func SSCCCheckDigit(seventeen string) (byte, error) {
	if !isDigits(seventeen, 17) {
		return 0, fmt.Errorf("sscc: %q is not 17 digits", seventeen)
	}
	sum := 0
	for i := 0; i < 17; i++ {
		d := int(seventeen[i] - '0')
		if i%2 == 0 {
			sum += 3 * d
		} else {
			sum += d
		}
	}
	return byte('0' + (10-sum%10)%10), nil
}

// ValidSSCC reports whether s is 18 digits whose last digit is the GS1 check
// digit of the first 17.
func ValidSSCC(s string) bool {
	if !isDigits(s, 18) {
		return false
	}
	check, err := SSCCCheckDigit(s[:17])
	return err == nil && check == s[17]
}
