package domain

import "regexp"

var certificateNumberPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,64}$`)

// Certificate is a stored exemption certificate: the engine's Exemption plus who it was issued to.
type Certificate struct {
	Exemption
	PartyRef string `json:"party_ref"`
	Note     string `json:"note"`
}

// ValidateCertificate checks a certificate; knownCodes are the stored tax codes it may exempt.
func ValidateCertificate(c Certificate, knownCodes map[string]bool) error {
	switch {
	case !certificateNumberPattern.MatchString(c.Number):
		return invalid("number", "number is required, at most 64 letters, digits, dots, slashes or dashes")
	case len(c.PartyRef) > 64:
		return invalid("party_ref", "party_ref is at most 64 characters")
	case len(c.Note) > 200:
		return invalid("note", "note is at most 200 characters")
	case len(c.Codes) == 0 || len(c.Codes) > 100:
		return invalid("codes", "name between 1 and 100 tax codes the certificate exempts")
	case c.ValidFrom.IsZero():
		return invalid("valid_from", "valid_from is required")
	case !c.ValidTo.IsZero() && c.ValidTo.Before(c.ValidFrom.Time):
		return invalid("valid_to", "valid_to must not be before valid_from")
	}
	seen := map[string]bool{}
	for _, code := range c.Codes {
		switch {
		case seen[code]:
			return invalid("codes", "tax code %q is listed twice", code)
		case !knownCodes[code]:
			return invalid("codes", "tax code %q does not exist", code)
		}
		seen[code] = true
	}
	return nil
}
