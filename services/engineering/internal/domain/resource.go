package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

const (
	TypeMachine  = "machine"
	TypeTool     = "tool"
	TypeLabor    = "labor"
	TypeVendor   = "vendor"
	TypeLocation = "location"

	ReqResource   = "resource"
	ReqGroup      = "group"
	ReqCapability = "capability"
	ReqType       = "type"
)

type ResourceCapability struct {
	CapabilityID  string
	Level         int
	EffectiveFrom time.Time
	EffectiveTo   time.Time
}

type Resource struct {
	ID              string
	Code            string
	Name            string
	Type            string
	SiteID          string
	GroupID         string
	CalendarID      string
	EfficiencyPct   decimal.Decimal
	CapacityPerDayH decimal.Decimal
	VendorPartyID   string
	Exclusive       bool
	Capabilities    []ResourceCapability
	Version         int
}

type ResourceGroup struct {
	ID         string
	Code       string
	Name       string
	SiteID     string
	CalendarID string
}

type Capability struct {
	ID   string
	Code string
	Name string
}

type Requirement struct {
	Kind     string
	ID       string
	MinLevel int
	Type     string
}

func ValidateResource(r Resource) error {
	if r.Type == TypeVendor && r.VendorPartyID == "" {
		return errf(CodeResVendor, "resource of type vendor requires vendorPartyId")
	}
	if r.Type != TypeVendor && r.VendorPartyID != "" {
		return errf(CodeResVendor, "resource of type %s must not carry vendorPartyId", r.Type)
	}
	return nil
}

func EffectiveCalendar(r Resource, g ResourceGroup) string {
	if r.CalendarID != "" {
		return r.CalendarID
	}
	return g.CalendarID
}

func (c ResourceCapability) effectiveOn(asOf time.Time) bool {
	return !asOf.Before(c.EffectiveFrom) && (c.EffectiveTo.IsZero() || !asOf.After(c.EffectiveTo))
}

func (r Resource) hasCapability(id string, minLevel int, asOf time.Time) bool {
	for _, c := range r.Capabilities {
		if c.CapabilityID == id && c.Level >= minLevel && c.effectiveOn(asOf) {
			return true
		}
	}
	return false
}

func Satisfies(req Requirement, resources []Resource, groups []ResourceGroup, asOf time.Time) []Resource {
	groupKnown := false
	for _, g := range groups {
		groupKnown = groupKnown || g.ID == req.ID
	}
	minLevel := max(req.MinLevel, 1)
	var matched []Resource
	for _, r := range resources {
		var ok bool
		switch req.Kind {
		case ReqResource:
			ok = r.ID == req.ID
		case ReqGroup:
			ok = groupKnown && r.GroupID == req.ID
		case ReqCapability:
			ok = r.hasCapability(req.ID, minLevel, asOf)
		case ReqType:
			ok = r.Type == req.Type
		}
		if ok {
			matched = append(matched, r)
		}
	}
	return matched
}
