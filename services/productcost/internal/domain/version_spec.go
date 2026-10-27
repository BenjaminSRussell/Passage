package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// MaxVersionSites bounds the site scope of one costing version.
const MaxVersionSites = 500

const maxVersionCode = 30

// Codes for stored costing versions. They extend the wave-39 catalog for slice
// 5 (CST-0711, CST-0715 and CST-0716 come from the lifecycle rules).
const (
	CodeVersionDuplicate = "CST-0713"
	CodeVersionConflict  = "CST-0714"
	CodeVersionNotFound  = "CST-0717"
	CodeLegalEntityScope = "CST-0718"
)

// VersionSpec is the caller-supplied content of a new costing version (FR-39.3).
// A new version always starts Open.
type VersionSpec struct {
	LegalEntityID     string     `json:"legalEntityId"`
	Code              string     `json:"code"`
	Type              string     `json:"type"`
	Fallback          string     `json:"fallback"`
	FallbackVersionID string     `json:"fallbackVersionId,omitempty"`
	SiteIDs           []string   `json:"siteIds"`
	EffectiveFrom     *time.Time `json:"effectiveFrom,omitempty"`
}

// Normalize trims the code and applies the parameter defaults: type Standard
// (costing.versionType) and fallback Active (costing.fallback).
func (s VersionSpec) Normalize() VersionSpec {
	s.Code = strings.TrimSpace(s.Code)
	s.LegalEntityID = strings.TrimSpace(s.LegalEntityID)
	s.FallbackVersionID = strings.TrimSpace(s.FallbackVersionID)
	if s.Type == "" {
		s.Type = VersionStandard
	}
	if s.Fallback == "" {
		s.Fallback = FallbackActive
	}
	if s.SiteIDs == nil {
		s.SiteIDs = []string{}
	}
	if s.EffectiveFrom != nil {
		t := s.EffectiveFrom.UTC()
		s.EffectiveFrom = &t
	}
	return s
}

// Validate returns the field messages for shape errors, or, when the shape is
// valid, the lifecycle rule a new Open version breaks (CST-0716 for a
// SpecifiedVersion fallback without a fallback version).
func (s VersionSpec) Validate() (map[string]string, error) {
	fields := map[string]string{}
	if !ids.Valid(s.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	switch {
	case s.Code == "":
		fields["code"] = "is required"
	case utf8.RuneCountInString(s.Code) > maxVersionCode:
		fields["code"] = fmt.Sprintf("must be at most %d characters", maxVersionCode)
	}
	switch s.Type {
	case VersionStandard, VersionPlanned, VersionSimulation, VersionConversion:
	default:
		fields["type"] = "must be Standard, Planned, Simulation or Conversion"
	}
	switch s.Fallback {
	case FallbackNone, FallbackActive, FallbackSpecified:
	default:
		fields["fallback"] = "must be None, Active or SpecifiedVersion"
	}
	if s.FallbackVersionID != "" && !ids.Valid(s.FallbackVersionID) {
		fields["fallbackVersionId"] = "must be a UUID"
	}
	if len(s.SiteIDs) > MaxVersionSites {
		fields["siteIds"] = fmt.Sprintf("must contain at most %d sites", MaxVersionSites)
	}
	seen := make(map[string]bool, len(s.SiteIDs))
	for i, site := range s.SiteIDs {
		switch {
		case !ids.Valid(site):
			fields[fmt.Sprintf("siteIds[%d]", i)] = "must be a UUID"
		case seen[strings.ToLower(site)]:
			fields[fmt.Sprintf("siteIds[%d]", i)] = "must be unique"
		}
		seen[strings.ToLower(site)] = true
	}
	if len(fields) > 0 {
		return fields, nil
	}
	v := Version{Type: s.Type, Status: VersionOpen, Fallback: s.Fallback, FallbackVersionID: s.FallbackVersionID}
	if err := v.Validate(); err != nil {
		if _, ok := err.(*CodedError); ok {
			return nil, err
		}
		// The only uncoded rule left is a fallbackVersionId without SpecifiedVersion.
		return map[string]string{"fallbackVersionId": err.Error()}, nil
	}
	return nil, nil
}
