package domain

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	CodeRegionRequired = "ORG-ADDR-REGION-REQUIRED"
	CodePostalFormat   = "ORG-ADDR-POSTAL-FORMAT"
)

var AddressPurposes = []string{"business", "delivery", "invoice", "remit_to", "service", "home", "other"}

type PostalAddress struct {
	CountryCode, RegionCode, City, PostalCode  string
	Street1, Street2, Street3, Building, POBox string
}

type CountryRule struct {
	RegionRequired bool
	PostalRegex    string
}

type Geocode struct {
	Latitude, Longitude decimal.Decimal
	Confidence          float64
	Found               bool
}

type Geocoder interface {
	Geocode(ctx context.Context, a PostalAddress) (Geocode, error)
}

type NullGeocoder struct{}

func (NullGeocoder) Geocode(context.Context, PostalAddress) (Geocode, error) { return Geocode{}, nil }

func ValidPurpose(p string) bool { return slices.Contains(AddressPurposes, p) }

func NormalizePostalCode(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func CheckCountryRules(a PostalAddress, rule CountryRule) map[string]string {
	problems := map[string]string{}
	if rule.RegionRequired && a.RegionCode == "" {
		problems["regionCode"] = CodeRegionRequired
	}
	if rule.PostalRegex == "" {
		return problems
	}
	re, err := regexp.Compile(rule.PostalRegex)
	if postal := NormalizePostalCode(a.PostalCode); err == nil && (postal == "" || !re.MatchString(postal)) {
		problems["postalCode"] = CodePostalFormat
	}
	return problems
}

func join(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), " ")
}

func usCity(a PostalAddress) []string {
	return []string{join(a.City+",", a.RegionCode, a.PostalCode)}
}

func postalFirst(a PostalAddress) []string { return []string{join(a.PostalCode, a.City)} }

func genericLines(a PostalAddress) []string { return append(postalFirst(a), a.RegionCode) }

var addressFormats = map[string]func(PostalAddress) []string{
	"US": usCity,
	"CA": usCity,
	"GB": func(a PostalAddress) []string { return []string{a.City, a.PostalCode} },
	"DE": postalFirst,
	"FR": postalFirst,
	"IN": func(a PostalAddress) []string { return []string{join(a.City, a.PostalCode), a.RegionCode} },
	"MX": func(a PostalAddress) []string { return []string{join(a.PostalCode, a.City+",", a.RegionCode)} },
}

func Format(a PostalAddress) string {
	a.PostalCode = NormalizePostalCode(a.PostalCode)
	render, ok := addressFormats[a.CountryCode]
	if !ok {
		render = genericLines
	}
	lines := append([]string{a.Street1, a.Street2, a.Street3, a.Building, a.POBox}, render(a)...)
	lines = append(lines, a.CountryCode)
	return strings.Join(slices.DeleteFunc(lines, func(l string) bool { return l == "" }), "\n")
}
