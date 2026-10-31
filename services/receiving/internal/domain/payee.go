package domain

import (
	"regexp"
	"strings"
)

const CodeInvalidBank = "AP-BNK-001"

var (
	ibanShape   = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}$`)
	bicShape    = regexp.MustCompile(`^[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$`)
	accountOnly = regexp.MustCompile(`^[0-9]{4,17}$`)
	// ibanLengths is the ISO 13616 registry length per country, the same table payables checks run
	// lines against; an IBAN of an unlisted country or of another length is refused.
	ibanLengths = map[string]int{
		"AD": 24, "AE": 23, "AL": 28, "AT": 20, "AZ": 28, "BA": 20, "BE": 16, "BG": 22, "BH": 22, "BR": 29,
		"BY": 28, "CH": 21, "CR": 22, "CY": 28, "CZ": 24, "DE": 22, "DK": 18, "DO": 28, "EE": 20, "EG": 29,
		"ES": 24, "FI": 18, "FO": 18, "FR": 27, "GB": 22, "GE": 22, "GI": 23, "GL": 18, "GR": 27, "GT": 28,
		"HR": 21, "HU": 28, "IE": 22, "IL": 23, "IQ": 23, "IS": 26, "IT": 27, "JO": 30, "KW": 30, "KZ": 20,
		"LB": 28, "LC": 32, "LI": 21, "LT": 20, "LU": 20, "LV": 21, "MC": 27, "MD": 24, "ME": 22, "MK": 19,
		"MR": 27, "MT": 31, "MU": 30, "NL": 18, "NO": 15, "PK": 24, "PL": 28, "PS": 29, "PT": 25, "QA": 29,
		"RO": 24, "RS": 22, "SA": 24, "SC": 31, "SE": 24, "SI": 19, "SK": 24, "SM": 27, "ST": 25, "SV": 28,
		"TL": 23, "TN": 24, "TR": 26, "UA": 29, "VA": 22, "VG": 24, "XK": 20,
	}
)

type PayeeBank struct {
	IBAN          string `json:"iban,omitempty"`
	BIC           string `json:"bic,omitempty"`
	RoutingNumber string `json:"routingNumber,omitempty"`
	AccountNumber string `json:"accountNumber,omitempty"`
}

func (b PayeeBank) Normalize() (PayeeBank, error) {
	b.IBAN = strings.ToUpper(strings.ReplaceAll(b.IBAN, " ", ""))
	b.BIC = strings.ToUpper(strings.ReplaceAll(b.BIC, " ", ""))
	switch {
	case b.IBAN != "" && (b.RoutingNumber != "" || b.AccountNumber != ""):
		return PayeeBank{}, fail(CodeInvalidBank, "give either an IBAN or a routing and account number, not both")
	case b.IBAN != "":
		if !validIBAN(b.IBAN) {
			return PayeeBank{}, fail(CodeInvalidBank, "the IBAN failed its checksum, country length or format check")
		}
		if b.BIC != "" && !bicShape.MatchString(b.BIC) {
			return PayeeBank{}, fail(CodeInvalidBank, "the BIC must be 8 or 11 characters in ISO 9362 form")
		}
	case b.RoutingNumber != "" || b.AccountNumber != "":
		if !validABA(b.RoutingNumber) {
			return PayeeBank{}, fail(CodeInvalidBank, "the routing number failed its checksum or format check")
		}
		if !accountOnly.MatchString(b.AccountNumber) || b.BIC != "" {
			return PayeeBank{}, fail(CodeInvalidBank, "the account number must be 4 to 17 digits and a BIC goes with an IBAN only")
		}
	default:
		return PayeeBank{}, fail(CodeInvalidBank, "the bank account needs an IBAN or a routing and account number")
	}
	return b, nil
}

func validIBAN(iban string) bool {
	if !ibanShape.MatchString(iban) || len(iban) != ibanLengths[iban[:2]] {
		return false
	}
	remainder := 0
	for _, c := range iban[4:] + iban[:4] {
		if c >= 'A' {
			remainder = (remainder*100 + int(c-'A') + 10) % 97
		} else {
			remainder = (remainder*10 + int(c-'0')) % 97
		}
	}
	return remainder == 1
}

func validABA(routing string) bool {
	if len(routing) != 9 {
		return false
	}
	weights := [9]int{3, 7, 1, 3, 7, 1, 3, 7, 1}
	sum := 0
	for i := 0; i < 9; i++ {
		if routing[i] < '0' || routing[i] > '9' {
			return false
		}
		sum += weights[i] * int(routing[i]-'0')
	}
	return sum%10 == 0
}
