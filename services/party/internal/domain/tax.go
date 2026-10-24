package domain

import (
	"context"
	"regexp"
	"strings"
)

type TaxType string

const (
	TaxVAT   TaxType = "VAT"
	TaxEIN   TaxType = "EIN"
	TaxGSTIN TaxType = "GSTIN"
)

type Status string

const (
	StatusValid      Status = "valid"
	StatusInvalid    Status = "invalid"
	StatusUnverified Status = "unverified"
	StatusFormatOnly Status = "format_only"
)

type Checker interface {
	Check(ctx context.Context, country, number string) (Status, error)
}

type FormatError struct{ Field, Reason string }

func (e *FormatError) Error() string { return e.Field + ": " + e.Reason }

const gstinAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

var (
	einPattern   = regexp.MustCompile(`^\d{9}$`)
	gstinPattern = regexp.MustCompile(`^\d{2}[A-Z]{5}\d{4}[A-Z][1-9A-Z]Z[0-9A-Z]$`)
	vatPatterns  = map[string]*regexp.Regexp{
		"AT": regexp.MustCompile(`^U\d{8}$`),
		"BE": regexp.MustCompile(`^[01]\d{9}$`),
		"BG": regexp.MustCompile(`^\d{9,10}$`),
		"CY": regexp.MustCompile(`^\d{8}[A-Z]$`),
		"CZ": regexp.MustCompile(`^\d{8,10}$`),
		"DE": regexp.MustCompile(`^\d{9}$`),
		"DK": regexp.MustCompile(`^\d{8}$`),
		"EE": regexp.MustCompile(`^\d{9}$`),
		"EL": regexp.MustCompile(`^\d{9}$`),
		"ES": regexp.MustCompile(`^[A-Z0-9]\d{7}[A-Z0-9]$`),
		"FI": regexp.MustCompile(`^\d{8}$`),
		"FR": regexp.MustCompile(`^[A-Z0-9]{2}\d{9}$`),
		"HR": regexp.MustCompile(`^\d{11}$`),
		"HU": regexp.MustCompile(`^\d{8}$`),
		"IE": regexp.MustCompile(`^\d[A-Z0-9+*]\d{5}[A-Z]{1,2}$`),
		"IT": regexp.MustCompile(`^\d{11}$`),
		"LT": regexp.MustCompile(`^(\d{9}|\d{12})$`),
		"LU": regexp.MustCompile(`^\d{8}$`),
		"LV": regexp.MustCompile(`^\d{11}$`),
		"MT": regexp.MustCompile(`^\d{8}$`),
		"NL": regexp.MustCompile(`^\d{9}B\d{2}$`),
		"PL": regexp.MustCompile(`^\d{10}$`),
		"PT": regexp.MustCompile(`^\d{9}$`),
		"RO": regexp.MustCompile(`^\d{2,10}$`),
		"SE": regexp.MustCompile(`^\d{12}$`),
		"SI": regexp.MustCompile(`^\d{8}$`),
		"SK": regexp.MustCompile(`^\d{10}$`),
	}
)

func NormalizeTaxNumber(number string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", ".", "").Replace(number))
}

func VATCountry(country string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "GR" {
		return "EL"
	}
	return country
}

func ValidateFormat(t TaxType, country, number string) error {
	number = NormalizeTaxNumber(number)
	if number == "" {
		return &FormatError{"number", "is required"}
	}
	switch t {
	case TaxVAT:
		_, err := vatBody(country, number)
		return err
	case TaxEIN:
		if !einPattern.MatchString(number) {
			return &FormatError{"number", "an EIN has 9 digits"}
		}
	case TaxGSTIN:
		if !validGSTIN(number) {
			return &FormatError{"number", "not a valid GSTIN"}
		}
	default:
		return &FormatError{"type", "must be one of VAT, EIN, GSTIN"}
	}
	return nil
}

func Validate(ctx context.Context, c Checker, t TaxType, country, number string) (Status, error) {
	if err := ValidateFormat(t, country, number); err != nil {
		return "", err
	}
	if t != TaxVAT {
		return StatusFormatOnly, nil
	}
	body, err := vatBody(country, NormalizeTaxNumber(number))
	if err != nil {
		return "", err
	}
	status, err := c.Check(ctx, VATCountry(country), body)
	if err != nil {
		return StatusUnverified, nil
	}
	return status, nil
}

func vatBody(country, number string) (string, error) {
	prefix := VATCountry(country)
	pattern, ok := vatPatterns[prefix]
	if !ok {
		return "", &FormatError{"country", "must be an EU member state code"}
	}
	body := strings.TrimPrefix(number, prefix)
	if !pattern.MatchString(body) {
		return "", &FormatError{"number", "does not match the " + prefix + " VAT format"}
	}
	return body, nil
}

func validGSTIN(number string) bool {
	if !gstinPattern.MatchString(number) {
		return false
	}
	sum := 0
	for i, c := range number[:14] {
		product := strings.IndexRune(gstinAlphabet, c) * (1 + i%2)
		sum += product/36 + product%36
	}
	return gstinAlphabet[(36-sum%36)%36] == number[14]
}
