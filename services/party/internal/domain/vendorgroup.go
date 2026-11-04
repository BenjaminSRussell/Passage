package domain

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const AggregateVendorGroup = "vendor-group"

// VendorGroupCodePattern is the short code of a vendor group: it is posted as a
// ledger dimension value (at most 20 characters), so it is at most 10.
var vendorGroupCodePattern = regexp.MustCompile(`^[A-Z0-9_-]{1,10}$`)

type VendorGroup struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type NewVendorGroup struct {
	// ID is optional: it registers a code for a vendor group id vendor roles
	// already carry (loaded before groups existed); empty assigns a new id.
	ID   string `json:"id,omitempty"`
	Code string `json:"code"`
	Name string `json:"name"`
}

func (g NewVendorGroup) Clean() NewVendorGroup {
	g.Code = strings.ToUpper(strings.TrimSpace(g.Code))
	g.Name = strings.TrimSpace(g.Name)
	if id, err := uuid.Parse(strings.TrimSpace(g.ID)); err == nil {
		g.ID = id.String()
	}
	return g
}

func (g NewVendorGroup) Validate() map[string]string {
	errs := map[string]string{}
	if _, err := uuid.Parse(g.ID); g.ID != "" && err != nil {
		errs["id"] = "must be a UUID when given"
	}
	if !vendorGroupCodePattern.MatchString(g.Code) {
		errs["code"] = "is required: 1 to 10 characters of A-Z, 0-9, dash or underscore"
	}
	if n := len([]rune(g.Name)); n < 1 || n > 100 {
		errs["name"] = "is required, at most 100 characters"
	}
	return errs
}
