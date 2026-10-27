package domain

import "testing"

func TestPartnerProfile_Validate(t *testing.T) {
	base := PartnerProfile{Name: "Demo distributor", ISA: Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"}, Environment: "TEST"}
	tests := []struct {
		name   string
		edit   func(*PartnerProfile)
		field  string
		wantOK bool
	}{
		{"valid", func(*PartnerProfile) {}, "", true},
		{"blank name", func(p *PartnerProfile) { p.Name = "  " }, "name", false},
		{"lower-case qualifier", func(p *PartnerProfile) { p.ISA.Qualifier = "zz" }, "isaQualifier", false},
		{"hyphen in id", func(p *PartnerProfile) { p.ISA.ID = "DIST-1" }, "isaId", false},
		{"id longer than 15", func(p *PartnerProfile) { p.ISA.ID = "ABCDEFGHIJKLMNOP" }, "isaId", false},
		{"unknown environment", func(p *PartnerProfile) { p.Environment = "UAT" }, "environment", false},
		{"sla below range", func(p *PartnerProfile) { p.AckSLAMinutes = 4 }, "ackSlaMinutes", false},
		{"sla above range", func(p *PartnerProfile) { p.AckSLAMinutes = 1441 }, "ackSlaMinutes", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.edit(&p)
			errs := p.Validate()
			if tc.wantOK {
				if len(errs) != 0 {
					t.Fatalf("unexpected field errors: %v", errs)
				}
				return
			}
			if _, ok := errs[tc.field]; !ok {
				t.Fatalf("want error on %s, got %v", tc.field, errs)
			}
		})
	}
}

func TestPartnerProfile_DefaultsAndSenderMatch(t *testing.T) {
	p := PartnerProfile{ISA: Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"}}.WithDefaults()
	if p.AckSLAMinutes != DefaultAckSLAMinutes {
		t.Fatalf("default SLA = %d", p.AckSLAMinutes)
	}
	if !p.MatchesSender(Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"}) {
		t.Fatal("matching sender rejected")
	}
	if p.MatchesSender(Party{Qualifier: "01", ID: "DISTRIBUTOR"}) {
		t.Fatal("a different qualifier must not match (BR-35.1)")
	}
}
