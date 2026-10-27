package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// Built-in template codes. Templates are ZPL with {{field}} placeholders and
// helpers {{bc128 f}}, {{sscc f}} and {{dm f}} (FR-32.7).
const (
	TemplateLP       = "LP_LABEL"
	TemplateLocation = "LOCATION_LABEL"
)

// Template returns the ZPL source of a built-in template.
func Template(code string) (string, bool) {
	switch code {
	case TemplateLP:
		return lpLabel, true
	case TemplateLocation:
		return locationLabel, true
	}
	return "", false
}

const lpLabel = "^XA\n^CI28\n^FO40,40^A0N,40,40^FDLP {{lp}}^FS\n^FO40,100^A0N,30,30^FD{{item}}^FS\n" +
	"^FO40,150^A0N,30,30^FDLOT {{lot}} EXP {{expiry}}^FS\n^FO40,220{{sscc sscc}}\n^XZ\n"

const locationLabel = "^XA\n^CI28\n^FO40,40^A0N,60,60^FD{{location}}^FS\n^FO40,130{{bc128 location}}\n^XZ\n"

const placeholderPattern = `\{\{\s*(?:(bc128|sscc|dm)\s+)?([a-zA-Z][a-zA-Z0-9_]*)\s*\}\}`

// Render substitutes data into a ZPL template. Values may not contain ZPL
// control characters (^ and ~) or control runes, so data cannot inject commands.
func Render(tpl string, data map[string]string) (string, error) {
	re := regexp.MustCompile(placeholderPattern)
	var firstErr error
	out := re.ReplaceAllStringFunc(tpl, func(m string) string {
		if firstErr != nil {
			return ""
		}
		parts := re.FindStringSubmatch(m)
		helper, field := parts[1], parts[2]
		value, ok := data[field]
		if !ok {
			firstErr = &Error{CodeInvalid, fmt.Sprintf("field %s is missing", field)}
			return ""
		}
		if err := checkValue(helper, field, value); err != nil {
			firstErr = err
			return ""
		}
		switch helper {
		case "bc128":
			return "^BCN,100,Y,N,N^FD" + value + "^FS"
		case "sscc":
			// GS1-128: start code C (>;), FNC1 (>8), then AI 00 and the 18-digit SSCC.
			return "^BCN,100,Y,N,N^FD>;>800" + value + "^FS"
		case "dm":
			return "^BXN,6,200^FD" + value + "^FS"
		}
		return value
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

func checkValue(helper, field, value string) error {
	if value == "" {
		return &Error{CodeInvalid, fmt.Sprintf("field %s is empty", field)}
	}
	if strings.ContainsAny(value, "^~") {
		return &Error{CodeInvalid, fmt.Sprintf("field %s contains a ZPL control character", field)}
	}
	for _, r := range value {
		if r < 0x20 {
			return &Error{CodeInvalid, fmt.Sprintf("field %s contains a control character", field)}
		}
	}
	if helper == "sscc" {
		if len(value) != 18 || !allDigits(value) || !Mod10Valid(value) {
			return &Error{CodeInvalid, fmt.Sprintf("field %s is not a valid SSCC", field)}
		}
	}
	return nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Mod10Valid checks a GS1 numeric identifier (GTIN, SSCC) whose last digit is
// the check digit: weights 3 and 1 alternate from the digit before the check.
func Mod10Valid(digits string) bool {
	if len(digits) < 2 || !allDigits(digits) {
		return false
	}
	sum := 0
	weight := 3
	for i := len(digits) - 2; i >= 0; i-- {
		sum += int(digits[i]-'0') * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	return (10-sum%10)%10 == int(digits[len(digits)-1]-'0')
}
