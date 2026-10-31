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
			return PayeeBank{}, fail(CodeInvalidBank, "the IBAN failed its checksum or format check")
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
	if !ibanShape.MatchString(iban) {
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
