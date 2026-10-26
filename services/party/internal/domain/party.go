package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	TypeOrganization = "organization"
	TypePerson       = "person"
	StatusActive     = "active"
	StatusInactive   = "inactive"
	StatusMerged     = "merged"
	MaskedDOB        = "••••"
	MaxNameLength    = 200
	dateLayout       = "2006-01-02"
)

type PartyFields struct {
	LegalName          *string `json:"legalName,omitempty"`
	TradeName          *string `json:"tradeName,omitempty"`
	RegistrationNumber *string `json:"registrationNumber,omitempty"`
	IndustryCode       *string `json:"industryCode,omitempty"`
	GivenName          *string `json:"givenName,omitempty"`
	MiddleName         *string `json:"middleName,omitempty"`
	FamilyName         *string `json:"familyName,omitempty"`
	Prefix             *string `json:"prefix,omitempty"`
	Suffix             *string `json:"suffix,omitempty"`
	DOB                *string `json:"dob,omitempty"`
	Gender             *string `json:"gender,omitempty"`
	LanguageCode       *string `json:"languageCode,omitempty"`
	TimeZone           *string `json:"timeZone,omitempty"`
	OwnerUserID        *string `json:"ownerUserId,omitempty"`
	OwnerTeamID        *string `json:"ownerTeamId,omitempty"`
}

type Party struct {
	ID           string  `json:"id"`
	PartyNumber  string  `json:"partyNumber"`
	PartyType    string  `json:"partyType"`
	Name         string  `json:"name"`
	SearchName   string  `json:"searchName"`
	Status       string  `json:"status"`
	MergedIntoID *string `json:"mergedIntoId,omitempty"`
	PartyFields
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type fieldRef struct {
	name  string
	ptr   **string
	scope string
}

func (f *PartyFields) refs() []fieldRef {
	return []fieldRef{
		{"legalName", &f.LegalName, TypeOrganization}, {"tradeName", &f.TradeName, TypeOrganization},
		{"registrationNumber", &f.RegistrationNumber, TypeOrganization}, {"industryCode", &f.IndustryCode, TypeOrganization},
		{"givenName", &f.GivenName, TypePerson}, {"middleName", &f.MiddleName, TypePerson}, {"familyName", &f.FamilyName, TypePerson},
		{"prefix", &f.Prefix, TypePerson}, {"suffix", &f.Suffix, TypePerson}, {"dob", &f.DOB, TypePerson}, {"gender", &f.Gender, TypePerson},
		{"languageCode", &f.LanguageCode, ""}, {"timeZone", &f.TimeZone, ""}, {"ownerUserId", &f.OwnerUserID, ""}, {"ownerTeamId", &f.OwnerTeamID, ""},
	}
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (f PartyFields) Clean() PartyFields {
	for _, r := range f.refs() {
		if *r.ptr == nil {
			continue
		}
		if t := strings.TrimSpace(**r.ptr); t == "" {
			*r.ptr = nil
		} else {
			*r.ptr = &t
		}
	}
	return f
}

func DisplayName(partyType string, f PartyFields) string {
	if partyType == TypeOrganization {
		return text(f.LegalName)
	}
	parts := make([]string, 0, 2)
	for _, p := range []string{text(f.GivenName), text(f.FamilyName)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}

func ValidateParty(partyType string, f PartyFields, requireNames bool) map[string]string {
	errs := map[string]string{}
	if partyType != TypeOrganization && partyType != TypePerson {
		errs["partyType"] = "must be organization or person"
		return errs
	}
	for _, r := range f.refs() {
		v := *r.ptr
		if v == nil {
			continue
		}
		switch {
		case r.scope != "" && r.scope != partyType:
			errs[r.name] = "not allowed for a " + partyType
		case utf8.RuneCountInString(*v) > MaxNameLength:
			errs[r.name] = fmt.Sprintf("must be at most %d characters", MaxNameLength)
		case (r.name == "ownerUserId" || r.name == "ownerTeamId") && *v != "" && !ids.Valid(*v):
			errs[r.name] = "must be a UUID"
		case r.name == "dob" && *v != "":
			if d, err := time.Parse(dateLayout, *v); err != nil {
				errs[r.name] = "must be YYYY-MM-DD"
			} else if d.After(time.Now().UTC()) {
				errs[r.name] = "must not be in the future"
			}
		}
	}
	if requireNames {
		required := []fieldRef{{"legalName", &f.LegalName, ""}}
		if partyType == TypePerson {
			required = []fieldRef{{"givenName", &f.GivenName, ""}, {"familyName", &f.FamilyName, ""}}
		}
		for _, r := range required {
			if *r.ptr == nil || strings.TrimSpace(**r.ptr) == "" {
				errs[r.name] = "required"
			}
		}
		if _, bad := errs["familyName"]; !bad && partyType == TypePerson && utf8.RuneCountInString(DisplayName(partyType, f)) > MaxNameLength {
			errs["familyName"] = fmt.Sprintf("given and family name together must be at most %d characters", MaxNameLength)
		}
	}
	return errs
}

func ApplyPatch(cur *Party, patch PartyFields) []string {
	changed := []string{}
	curRefs := cur.PartyFields.refs()
	for i, p := range patch.refs() {
		if *p.ptr == nil {
			continue
		}
		next := strings.TrimSpace(**p.ptr)
		if text(*curRefs[i].ptr) == next {
			continue
		}
		if next == "" {
			*curRefs[i].ptr = nil
		} else {
			*curRefs[i].ptr = &next
		}
		changed = append(changed, p.name)
	}
	sort.Strings(changed)
	return changed
}

func CheckTransition(from, to string) bool {
	return from != StatusMerged && (to == StatusActive || to == StatusInactive)
}

func MaskDOB(dob *string, canRead bool) *string {
	if dob == nil || canRead {
		return dob
	}
	masked := MaskedDOB
	return &masked
}

func RequestHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func PublicFields(changed []string) []string {
	public := make([]string, 0, len(changed))
	for _, f := range changed {
		if f != "dob" && f != "gender" {
			public = append(public, f)
		}
	}
	return public
}
