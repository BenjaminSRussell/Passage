package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	CodeRunInvalid  = "AP-RUN-002"
	CodeFileInvalid = "AP-FIL-001"

	RuleIBAN = "iban"
	RuleBIC  = "bic"
	RuleABA  = "aba"

	MethodTransfer = "TRANSFER"
	MethodACH      = "ACH"
	MethodCheck    = "CHECK"
	MethodCard     = "CARD"
	MethodWire     = "WIRE"

	FormatPain001     = "PAIN001"
	FormatNacha       = "NACHA"
	FormatCheckPDF    = "CHECK_PDF"
	FormatPositivePay = "POSITIVE_PAY"

	VariantSEPA = "SEPA"
	VariantUS   = "US"
	VariantUK   = "UK"
	VariantCH   = "CH"

	Pain001Default = "001.001.09"
	Pain001Legacy  = "001.001.03"

	CharsetLatinSepa = "latin-sepa"
	CharsetASCII     = "ascii"

	maxOverrideKeys   = 20
	maxUstrdLength    = 140
	maxInitiatingID   = 35
	defaultMinPayment = "1.00"
)

var (
	bicPattern    = regexp.MustCompile(`^[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$`)
	chargeBearers = map[string]bool{"SLEV": true, "SHAR": true, "DEBT": true, "CRED": true}
	charsets      = map[string]bool{CharsetLatinSepa: true, CharsetASCII: true}
	variants      = map[string]bool{VariantSEPA: true, VariantUS: true, VariantUK: true, VariantCH: true}
	methodFormats = map[string][]string{
		MethodTransfer: {FormatPain001},
		MethodACH:      {FormatNacha},
		MethodCheck:    {FormatCheckPDF, FormatPositivePay},
		MethodWire:     {FormatPain001},
		MethodCard:     nil,
	}
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

func ruleError(rule, format string, a ...any) *Error {
	return &Error{Code: CodeRunInvalid, Detail: rule + ": " + fmt.Sprintf(format, a...)}
}

func ValidateIBAN(raw string) error {
	iban := strings.ToUpper(strings.ReplaceAll(raw, " ", ""))
	if len(iban) < 4 {
		return ruleError(RuleIBAN, "too short")
	}
	want, ok := ibanLengths[iban[:2]]
	if !ok {
		return ruleError(RuleIBAN, "unknown country")
	}
	if len(iban) != want {
		return ruleError(RuleIBAN, "length %d does not match country %s", len(iban), iban[:2])
	}
	rearranged := iban[4:] + iban[:4]
	remainder := 0
	for i := 0; i < len(rearranged); i++ {
		c := rearranged[i]
		switch {
		case c >= '0' && c <= '9':
			remainder = (remainder*10 + int(c-'0')) % 97
		case c >= 'A' && c <= 'Z':
			remainder = (remainder*100 + int(c-'A') + 10) % 97
		default:
			return ruleError(RuleIBAN, "invalid character")
		}
	}
	if iban[2] < '0' || iban[2] > '9' || iban[3] < '0' || iban[3] > '9' {
		return ruleError(RuleIBAN, "check digits must be numeric")
	}
	if remainder != 1 {
		return ruleError(RuleIBAN, "checksum mismatch")
	}
	return nil
}

func ValidateBIC(raw string) error {
	if !bicPattern.MatchString(strings.ToUpper(raw)) {
		return ruleError(RuleBIC, "must be 8 or 11 characters in ISO 9362 form")
	}
	return nil
}

func ValidateABA(raw string) error {
	if len(raw) != 9 {
		return ruleError(RuleABA, "must be 9 digits")
	}
	weights := [9]int{3, 7, 1, 3, 7, 1, 3, 7, 1}
	sum := 0
	for i := 0; i < 9; i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return ruleError(RuleABA, "must be 9 digits")
		}
		sum += weights[i] * int(raw[i]-'0')
	}
	if sum%10 != 0 {
		return ruleError(RuleABA, "checksum mismatch")
	}
	return nil
}

type PaymentMethodDTO struct {
	Type       string `json:"type"`
	Format     string `json:"format"`
	MinPayment string `json:"minPayment"`
	MaxPayment string `json:"maxPayment"`
	CutoffTime string `json:"cutoffTime"`
}

type PaymentMethod struct {
	Type       string
	Format     string
	MinPayment decimal.Decimal
	MaxPayment *decimal.Decimal
	CutoffTime string
}

func parseNonNegative(field, raw string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(raw)
	if err != nil || d.IsNegative() {
		return decimal.Zero, invalid("%s must be a non-negative decimal", field)
	}
	return d, nil
}

func ParsePaymentMethod(dto PaymentMethodDTO) (PaymentMethod, error) {
	formats, known := methodFormats[dto.Type]
	if !known {
		return PaymentMethod{}, invalid("type must be one of TRANSFER, ACH, CHECK, CARD, WIRE")
	}
	if dto.Type == MethodCard {
		if dto.Format != "" {
			return PaymentMethod{}, invalid("CARD has no file format until a provider adapter exists")
		}
	} else if !contains(formats, dto.Format) {
		return PaymentMethod{}, invalid("format %q is not valid for type %s", dto.Format, dto.Type)
	}
	minRaw := dto.MinPayment
	if minRaw == "" {
		minRaw = defaultMinPayment
	}
	minPayment, err := parseNonNegative("minPayment", minRaw)
	if err != nil {
		return PaymentMethod{}, err
	}
	method := PaymentMethod{Type: dto.Type, Format: dto.Format, MinPayment: minPayment, CutoffTime: dto.CutoffTime}
	if dto.MaxPayment != "" {
		maxPayment, err := parseNonNegative("maxPayment", dto.MaxPayment)
		if err != nil {
			return PaymentMethod{}, err
		}
		if maxPayment.LessThan(minPayment) {
			return PaymentMethod{}, invalid("maxPayment must not be below minPayment")
		}
		method.MaxPayment = &maxPayment
	}
	if dto.CutoffTime != "" {
		if _, err := time.Parse("15:04", dto.CutoffTime); err != nil {
			return PaymentMethod{}, invalid("cutoffTime must be HH:MM")
		}
	}
	return method, nil
}

func (m PaymentMethod) MediaFormat() (string, error) {
	if m.Format == "" {
		return "", invalid("%s payments have no media format", m.Type)
	}
	return m.Format, nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

type BankProfileDTO struct {
	Variant        string                     `json:"variant"`
	Pain001Version string                     `json:"pain001Version"`
	Overrides      map[string]json.RawMessage `json:"overrides"`
}

type Overrides struct {
	ChargeBearer              string
	BatchBooking              bool
	BICRequired               bool
	StructuredRemittance      bool
	Charset                   string
	MaxUstrdLength            int
	StructuredAddressRequired bool
	InitiatingPartyID         string
}

type BankProfile struct {
	Variant        string
	Pain001Version string
	Overrides      Overrides
}

func defaultOverrides() Overrides {
	return Overrides{ChargeBearer: "SHAR", BatchBooking: true, Charset: CharsetLatinSepa, MaxUstrdLength: maxUstrdLength}
}

func ParseBankProfile(dto BankProfileDTO) (BankProfile, error) {
	if !variants[dto.Variant] {
		return BankProfile{}, invalid("variant must be one of SEPA, US, UK, CH")
	}
	version := dto.Pain001Version
	switch version {
	case "":
		version = Pain001Default
	case Pain001Default:
	case Pain001Legacy:
		return BankProfile{}, invalid("pain001Version %s is not supported in this slice", Pain001Legacy)
	default:
		return BankProfile{}, invalid("pain001Version must be %s", Pain001Default)
	}
	if len(dto.Overrides) > maxOverrideKeys {
		return BankProfile{}, invalid("overrides may hold at most %d keys", maxOverrideKeys)
	}
	o := defaultOverrides()
	for key, raw := range dto.Overrides {
		if err := applyOverride(&o, key, raw); err != nil {
			return BankProfile{}, err
		}
	}
	return BankProfile{Variant: dto.Variant, Pain001Version: version, Overrides: o}, nil
}

func applyOverride(o *Overrides, key string, raw json.RawMessage) error {
	var target any
	switch key {
	case "charge_bearer":
		target = &o.ChargeBearer
	case "batch_booking":
		target = &o.BatchBooking
	case "bic_required":
		target = &o.BICRequired
	case "structured_remittance":
		target = &o.StructuredRemittance
	case "charset":
		target = &o.Charset
	case "max_ustrd_length":
		target = &o.MaxUstrdLength
	case "structured_address_required":
		target = &o.StructuredAddressRequired
	case "initiating_party_id":
		target = &o.InitiatingPartyID
	default:
		return invalid("unknown override key %q", key)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return invalid("override %s has the wrong type", key)
	}
	switch {
	case key == "charge_bearer" && !chargeBearers[o.ChargeBearer]:
		return invalid("override charge_bearer must be SLEV, SHAR, DEBT or CRED")
	case key == "charset" && !charsets[o.Charset]:
		return invalid("override charset must be latin-sepa or ascii")
	case key == "max_ustrd_length" && (o.MaxUstrdLength < 1 || o.MaxUstrdLength > maxUstrdLength):
		return invalid("override max_ustrd_length must be between 1 and %d", maxUstrdLength)
	case key == "initiating_party_id" && len(o.InitiatingPartyID) > maxInitiatingID:
		return invalid("override initiating_party_id must be at most %d characters", maxInitiatingID)
	}
	return nil
}
