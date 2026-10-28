package domain

import (
	"context"
	"regexp"
	"strings"
)

type AddressSuggestion struct {
	Address PostalAddress `json:"address"`
	Reason  string        `json:"reason"`
}

type AddressValidator interface {
	Normalize(ctx context.Context, a PostalAddress) (PostalAddress, error)
	Suggest(ctx context.Context, a PostalAddress) ([]AddressSuggestion, error)
}

type NullAddressValidator struct{}

func (NullAddressValidator) Normalize(_ context.Context, a PostalAddress) (PostalAddress, error) {
	return a, nil
}

func (NullAddressValidator) Suggest(context.Context, PostalAddress) ([]AddressSuggestion, error) {
	return nil, nil
}

type RuleAddressValidator struct{}

type postalLayout struct {
	valid  *regexp.Regexp
	layout func(compact string) string
}

func identity(compact string) string { return compact }

var (
	usZipPlus4  = regexp.MustCompile(`^[0-9]{9}$`)
	postalRules = map[string]postalLayout{
		"US": {regexp.MustCompile(`^[0-9]{5}(-[0-9]{4})?$`), func(c string) string {
			if usZipPlus4.MatchString(c) {
				return c[:5] + "-" + c[5:]
			}
			return c
		}},
		"CA": {regexp.MustCompile(`^[A-Z][0-9][A-Z] [0-9][A-Z][0-9]$`), func(c string) string {
			if len(c) == 6 {
				return c[:3] + " " + c[3:]
			}
			return c
		}},
		"GB": {regexp.MustCompile(`^[A-Z]{1,2}[0-9][A-Z0-9]? [0-9][A-Z]{2}$`), func(c string) string {
			if len(c) >= 5 && len(c) <= 7 {
				return c[:len(c)-3] + " " + c[len(c)-3:]
			}
			return c
		}},
		"DE": {regexp.MustCompile(`^[0-9]{5}$`), identity},
		"FR": {regexp.MustCompile(`^[0-9]{5}$`), identity},
		"IN": {regexp.MustCompile(`^[1-9][0-9]{5}$`), identity},
		"MX": {regexp.MustCompile(`^[0-9]{5}$`), identity},
	}
	countryAliases = map[string]string{"UK": "GB", "USA": "US"}
	leadingZero    = map[string]bool{"US": true, "DE": true, "FR": true, "MX": true}
	digitLookalike = strings.NewReplacer("O", "0", "I", "1")
)

func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }

func compactPostal(s string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "\t", "").Replace(s))
}

func (RuleAddressValidator) Normalize(_ context.Context, a PostalAddress) (PostalAddress, error) {
	for _, field := range []*string{&a.CountryCode, &a.RegionCode, &a.City, &a.PostalCode, &a.Street1, &a.Street2, &a.Street3, &a.Building, &a.POBox} {
		*field = squeeze(*field)
	}
	a.CountryCode, a.RegionCode = strings.ToUpper(a.CountryCode), strings.ToUpper(a.RegionCode)
	if canonical, aliased := countryAliases[a.CountryCode]; aliased {
		a.CountryCode = canonical
	}
	if rule, known := postalRules[a.CountryCode]; known {
		a.PostalCode = rule.layout(compactPostal(a.PostalCode))
	} else {
		a.PostalCode = NormalizePostalCode(a.PostalCode)
	}
	return a, nil
}

func (v RuleAddressValidator) Suggest(ctx context.Context, a PostalAddress) ([]AddressSuggestion, error) {
	a, _ = v.Normalize(ctx, a)
	rule, known := postalRules[a.CountryCode]
	if !known || rule.valid.MatchString(a.PostalCode) {
		return nil, nil
	}
	var suggestions []AddressSuggestion
	try := func(postal, reason string) {
		if rule.valid.MatchString(postal) {
			fixed := a
			fixed.PostalCode = postal
			suggestions = append(suggestions, AddressSuggestion{Address: fixed, Reason: reason})
		}
	}
	if leadingZero[a.CountryCode] {
		try("0"+a.PostalCode, "postal code is missing a leading zero")
	}
	if compact := []byte(compactPostal(a.PostalCode)); a.CountryCode == "CA" && len(compact) == 6 {
		for _, i := range []int{1, 3, 5} {
			compact[i] = digitLookalike.Replace(string(compact[i]))[0]
		}
		try(rule.layout(string(compact)), "letters O and I at digit positions replaced by 0 and 1")
	}
	return suggestions, nil
}
