package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func TestVersionSpec_NormalizeAppliesParameterDefaults(t *testing.T) {
	local := time.Date(2027, 1, 1, 0, 0, 0, 0, time.FixedZone("EST", -5*3600))
	got := VersionSpec{Code: "  STD-2027 ", LegalEntityID: " " + ids.Nil + " ", EffectiveFrom: &local}.Normalize()
	if got.Code != "STD-2027" || got.LegalEntityID != ids.Nil || got.Type != VersionStandard || got.Fallback != FallbackActive {
		t.Fatalf("Normalize = %+v", got)
	}
	if got.SiteIDs == nil || len(got.SiteIDs) != 0 {
		t.Fatalf("site list %v, want empty and non-nil", got.SiteIDs)
	}
	if got.EffectiveFrom.Location() != time.UTC || !got.EffectiveFrom.Equal(local) {
		t.Fatalf("effectiveFrom %v, want the same instant in UTC", got.EffectiveFrom)
	}
}

func TestVersionSpec_Validate(t *testing.T) {
	le, site := ids.New(), ids.New()
	base := VersionSpec{LegalEntityID: le, Code: "S1", Type: VersionStandard, Fallback: FallbackActive}
	tests := []struct {
		name     string
		edit     func(s *VersionSpec)
		fields   string
		wantCode string
	}{
		{"standard with active fallback", func(*VersionSpec) {}, "", ""},
		{"planned with sites", func(s *VersionSpec) { s.Type = VersionPlanned; s.SiteIDs = []string{site} }, "", ""},
		{"simulation may be created", func(s *VersionSpec) { s.Type = VersionSimulation }, "", ""},
		{"code of 30 runes", func(s *VersionSpec) { s.Code = strings.Repeat("ü", 30) }, "", ""},
		{"code of 31 characters", func(s *VersionSpec) { s.Code = strings.Repeat("A", 31) }, "code", ""},
		{"blank code", func(s *VersionSpec) { s.Code = "" }, "code", ""},
		{"legal entity not a uuid", func(s *VersionSpec) { s.LegalEntityID = "LE-01" }, "legalEntityId", ""},
		{"unknown type", func(s *VersionSpec) { s.Type = "Budget" }, "type", ""},
		{"unknown fallback", func(s *VersionSpec) { s.Fallback = "Latest" }, "fallback", ""},
		{"site not a uuid", func(s *VersionSpec) { s.SiteIDs = []string{site, "S-1"} }, "siteIds[1]", ""},
		{"duplicate site in another case", func(s *VersionSpec) { s.SiteIDs = []string{site, strings.ToUpper(site)} }, "siteIds[1]", ""},
		{"specified fallback needs a version", func(s *VersionSpec) { s.Fallback = FallbackSpecified }, "", CodeFallbackVersion},
		{"specified fallback with a version", func(s *VersionSpec) { s.Fallback = FallbackSpecified; s.FallbackVersionID = ids.New() }, "", ""},
		{"fallback version without SpecifiedVersion", func(s *VersionSpec) { s.FallbackVersionID = ids.New() }, "fallbackVersionId", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.edit(&s)
			fields, err := s.Normalize().Validate()
			if got := strings.Join(fieldKeys(fields), ","); got != tc.fields {
				t.Fatalf("fields %q (%v), want %q", got, fields, tc.fields)
			}
			var ce *CodedError
			switch {
			case tc.wantCode != "" && (!errors.As(err, &ce) || ce.Code != tc.wantCode):
				t.Fatalf("err %v, want %s", err, tc.wantCode)
			case tc.wantCode == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}

func TestVersionSpec_TooManySites(t *testing.T) {
	s := VersionSpec{LegalEntityID: ids.New(), Code: "S1"}
	for i := 0; i <= MaxVersionSites; i++ {
		s.SiteIDs = append(s.SiteIDs, ids.New())
	}
	fields, _ := s.Normalize().Validate()
	if _, ok := fields["siteIds"]; !ok {
		t.Fatalf("%d sites accepted: %v", len(s.SiteIDs), fields)
	}
}
