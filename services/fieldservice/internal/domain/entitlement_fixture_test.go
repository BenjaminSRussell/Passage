package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata"
)

// EntitlementFixture is svc-entitlement-50 (testdata/svc-entitlement-50): the
// cases as written and the answers reference_entitlement.py derived for them
// independently of this package. The store test opens the same cases.
type EntitlementFixture struct {
	Profiles map[string]FixtureProfile `json:"profiles"`
	Cases    []FixtureCase             `json:"cases"`
}

// FixtureProfile is an SLA profile of the fixture; targets are [priority, response, resolution].
type FixtureProfile struct {
	TimeZone string   `json:"timeZone"`
	AllDay   bool     `json:"allDay"`
	Open     int      `json:"open"`
	Close    int      `json:"close"`
	Holidays []string `json:"holidays"`
	Targets  [][3]int `json:"targets"`
}

// FixtureCase is one case with its own agreements.
type FixtureCase struct {
	ID          string             `json:"id"`
	DomainOnly  bool               `json:"domainOnly"`
	Customer    string             `json:"customer"`
	LegalEntity string             `json:"legalEntity"`
	Object      string             `json:"object"`
	Category    string             `json:"category"`
	OpenedAt    time.Time          `json:"openedAt"`
	Priority    int                `json:"priority"`
	Fallback    string             `json:"fallback"`
	Agreements  []FixtureAgreement `json:"agreements"`
}

// FixtureAgreement is an agreement of a fixture case; empty fields take the defaults.
type FixtureAgreement struct {
	ID           string `json:"id"`
	Customer     string `json:"customer"`
	LegalEntity  string `json:"legalEntity"`
	Status       string `json:"status"`
	ValidFrom    string `json:"validFrom"`
	ValidTo      string `json:"validTo"`
	PriorityRank int    `json:"priorityRank"`
	Profile      string `json:"profile"`
	Lines        []struct {
		Object   string `json:"object"`
		Category string `json:"category"`
	} `json:"lines"`
}

// FixtureAnswer is the reference answer for one case.
type FixtureAnswer struct {
	Error            string  `json:"error"`
	Agreement        *string `json:"agreement"`
	Specificity      int     `json:"specificity"`
	Profile          string  `json:"profile"`
	ResponseDue      string  `json:"responseDue"`
	ResponseWarnAt   string  `json:"responseWarnAt"`
	ResolutionDue    string  `json:"resolutionDue"`
	ResolutionWarnAt string  `json:"resolutionWarnAt"`
}

// Defaults of the fixture's case and agreement fields.
func (c FixtureCase) CustomerOrDefault() string    { return orDefault(c.Customer, "C1") }
func (c FixtureCase) LegalEntityOrDefault() string { return orDefault(c.LegalEntity, "LE1") }
func (a FixtureAgreement) CustomerOrDefault() string {
	return orDefault(a.Customer, "C1")
}
func (a FixtureAgreement) LegalEntityOrDefault() string { return orDefault(a.LegalEntity, "LE1") }
func (a FixtureAgreement) StatusOrDefault() string      { return orDefault(a.Status, AgreementActive) }
func (a FixtureAgreement) ValidFromOrDefault() string   { return orDefault(a.ValidFrom, "2026-01-01") }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// LoadEntitlementFixture reads cases.json and expected.json from dir.
func LoadEntitlementFixture(t *testing.T, dir string) (EntitlementFixture, map[string]FixtureAnswer) {
	t.Helper()
	var fx EntitlementFixture
	var want map[string]FixtureAnswer
	for name, into := range map[string]any{"cases.json": &fx, "expected.json": &want} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(fx.Cases) != 50 || len(want) != 50 {
		t.Fatalf("fixture has %d cases and %d answers, want 50 of each", len(fx.Cases), len(want))
	}
	return fx, want
}

func (p FixtureProfile) calendar(t *testing.T) Calendar {
	t.Helper()
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		t.Fatal(err)
	}
	if p.AllDay {
		return Calendar{Loc: loc, AllDay: true}
	}
	cal, err := MondayToFriday(loc, p.Open, p.Close).WithHolidays(p.Holidays...)
	if err != nil || !cal.Validate() {
		t.Fatalf("calendar %+v: %v", p, err)
	}
	return cal
}

// AC-45.1 (domain): every svc-entitlement-50 case resolves to the reference
// agreement, specificity and SLA profile, and the clocks started from that
// profile land on the reference due and warning instants. The candidates are
// the case customer's agreements in its legal entity, as the store loads them.
func TestResolve_SvcEntitlement50(t *testing.T) {
	fx, want := LoadEntitlementFixture(t, filepath.Join("testdata", "svc-entitlement-50"))
	for _, c := range fx.Cases {
		t.Run(c.ID, func(t *testing.T) {
			exp := want[c.ID]
			var cands []Agreement
			for _, a := range c.Agreements {
				if a.CustomerOrDefault() != c.CustomerOrDefault() || a.LegalEntityOrDefault() != c.LegalEntityOrDefault() {
					continue
				}
				from, err := time.Parse(DateLayout, a.ValidFromOrDefault())
				if err != nil {
					t.Fatal(err)
				}
				agr := Agreement{ID: a.ID, SLAProfileID: a.Profile, Status: a.StatusOrDefault(), ValidFrom: from, PriorityRank: a.PriorityRank}
				if a.ValidTo != "" {
					to, err := time.Parse(DateLayout, a.ValidTo)
					if err != nil {
						t.Fatal(err)
					}
					agr.ValidTo = &to
				}
				for _, l := range a.Lines {
					agr.Lines = append(agr.Lines, AgreementLine{ServiceObjectID: l.Object, CategoryID: l.Category})
				}
				cands = append(cands, agr)
			}
			opened := c.OpenedAt.UTC() // the API stores openedAt in UTC
			ent, ok := Resolve(cands, opened, c.Object, c.Category)
			profile := ent.SLAProfileID
			switch {
			case ok:
				if exp.Agreement == nil || ent.AgreementID != *exp.Agreement || ent.Specificity != exp.Specificity {
					t.Fatalf("resolved %s at %d, want %v at %d", ent.AgreementID, ent.Specificity, exp.Agreement, exp.Specificity)
				}
			case exp.Agreement != nil:
				t.Fatalf("no agreement resolved, want %s", *exp.Agreement)
			case c.Fallback == "":
				if exp.Error != "SVC-ENT-002" {
					t.Fatalf("no match and no fallback, but the reference expects %+v", exp)
				}
				return
			default:
				profile = c.Fallback
			}
			p := fx.Profiles[profile]
			var target Target
			found := false
			for _, tg := range p.Targets {
				if tg[0] == c.Priority {
					target, found = Target{Priority: tg[0], ResponseMinutes: tg[1], ResolutionMinutes: tg[2]}, true
				}
			}
			if profile != exp.Profile {
				t.Fatalf("profile %s, want %s", profile, exp.Profile)
			}
			if !found {
				if exp.Error != "SVC-SLA-003" {
					t.Fatalf("profile %s has no target for priority %d, but the reference expects %+v", profile, c.Priority, exp)
				}
				return
			}
			if exp.Error != "" {
				t.Fatalf("resolved with clocks, but the reference expects %s", exp.Error)
			}
			clocks := StartClocks(opened, p.calendar(t), target)
			for name, pair := range map[string][2]any{
				"responseDue": {clocks.ResponseDue, exp.ResponseDue}, "responseWarnAt": {clocks.ResponseWarnAt, exp.ResponseWarnAt},
				"resolutionDue": {clocks.ResolutionDue, exp.ResolutionDue}, "resolutionWarnAt": {clocks.ResolutionWarnAt, exp.ResolutionWarnAt},
			} {
				if got := pair[0].(time.Time).UTC().Format(time.RFC3339); got != pair[1].(string) {
					t.Errorf("%s = %s, want %s", name, got, pair[1])
				}
			}
		})
	}
}
