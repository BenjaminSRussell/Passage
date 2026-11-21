package domain

import (
	"net/mail"
	"net/url"
	"regexp"
	"strings"
)

const (
	KindEmail = "email"
	KindPhone = "phone"
	KindURL   = "url"

	maxEmailLen = 254
	maxURLLen   = 2048
)

var (
	phoneFormatting = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "")
	phonePattern    = regexp.MustCompile(`^\+?[0-9]{7,15}$`)
)

type ElectronicAddress struct {
	ID            string `json:"id"`
	PartyID       string `json:"partyId"`
	LegalEntityID string `json:"legalEntityId"`
	Kind          string `json:"kind"`
	Value         string `json:"value"`
}

type NewElectronicAddress struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

func (a NewElectronicAddress) Clean() NewElectronicAddress {
	a.Kind = strings.ToLower(strings.TrimSpace(a.Kind))
	a.Value = strings.TrimSpace(a.Value)
	switch a.Kind {
	case KindEmail:
		a.Value = strings.ToLower(a.Value)
	case KindPhone:
		a.Value = phoneFormatting.Replace(a.Value)
	}
	return a
}

func (a NewElectronicAddress) Validate() map[string]string {
	errs := map[string]string{}
	switch a.Kind {
	case KindEmail:
		if parsed, err := mail.ParseAddress(a.Value); err != nil || parsed.Address != a.Value || len(a.Value) > maxEmailLen {
			errs["value"] = "must be an email address of at most 254 characters"
		}
	case KindPhone:
		if !phonePattern.MatchString(a.Value) {
			errs["value"] = "must be 7 to 15 digits with an optional leading +"
		}
	case KindURL:
		if parsed, err := url.Parse(a.Value); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(a.Value) > maxURLLen {
			errs["value"] = "must be an http or https URL with a host"
		}
	default:
		errs["kind"] = "must be email, phone or url"
	}
	return errs
}

func (a NewElectronicAddress) Normalized() string {
	if a.Kind == KindURL {
		return strings.ToLower(strings.TrimRight(a.Value, "/"))
	}
	return a.Value
}
