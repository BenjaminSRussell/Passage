package domain

import (
	"strings"
	"testing"
)

func isID(s string) bool { return strings.HasPrefix(s, "le-") }

func TestValidateProject(t *testing.T) {
	copyOK := Project{Name: "USMF to CAMF", Type: ProjectCopy, SourceLegalEntityID: "le-1", TargetLegalEntityID: "le-2", Status: ProjectActive}
	tests := []struct {
		name  string
		edit  func(*Project)
		field string
	}{
		{"valid copy", func(*Project) {}, ""},
		{"valid migrate", func(p *Project) {
			*p = Project{Name: "AX", Type: ProjectMigrate, SourceSystem: "ax2012", Status: ProjectActive, ScheduleCron: "*/15 2-4 * * 1,3"}
		}, ""},
		{"valid config package", func(p *Project) { *p = Project{Name: "Cfg", Type: ProjectConfigPackage, Status: ProjectArchived} }, ""},
		{"blank name", func(p *Project) { p.Name = " " }, "name"},
		{"padded name", func(p *Project) { p.Name = " x" }, "name"},
		{"long name", func(p *Project) { p.Name = strings.Repeat("x", 81) }, "name"},
		{"unknown type", func(p *Project) { p.Type = "sync" }, "projectType"},
		{"copy without source", func(p *Project) { p.SourceLegalEntityID = "" }, "sourceLegalEntityId"},
		{"copy bad target", func(p *Project) { p.TargetLegalEntityID = "x" }, "targetLegalEntityId"},
		{"copy same entity", func(p *Project) { p.TargetLegalEntityID = p.SourceLegalEntityID }, "targetLegalEntityId"},
		{"copy with legacy system", func(p *Project) { p.SourceSystem = "ax2012" }, "sourceSystem"},
		{"migrate without system", func(p *Project) { *p = Project{Name: "AX", Type: ProjectMigrate, Status: ProjectActive} }, "sourceSystem"},
		{"valid migrate with target legal entity and profile", func(p *Project) {
			*p = Project{Name: "AX", Type: ProjectMigrate, SourceSystem: "ax2012", TargetLegalEntityID: "le-2", ConnectionProfileID: "le-9", Status: ProjectActive}
		}, ""},
		{"valid custom source system", func(p *Project) {
			*p = Project{Name: "Acme", Type: ProjectMigrate, SourceSystem: "custom_acme", Status: ProjectActive}
		}, ""},
		{"custom system with uppercase", func(p *Project) {
			*p = Project{Name: "Acme", Type: ProjectMigrate, SourceSystem: "custom_Acme", Status: ProjectActive}
		}, "sourceSystem"},
		{"migrate bad target legal entity", func(p *Project) {
			*p = Project{Name: "AX", Type: ProjectMigrate, SourceSystem: "ax2012", TargetLegalEntityID: "x", Status: ProjectActive}
		}, "targetLegalEntityId"},
		{"copy with profile", func(p *Project) { p.ConnectionProfileID = "le-9" }, "connectionProfileId"},
		{"migrate bad profile id", func(p *Project) {
			*p = Project{Name: "AX", Type: ProjectMigrate, SourceSystem: "ax2012", ConnectionProfileID: "x", Status: ProjectActive}
		}, "connectionProfileId"},
		{"migrate with source legal entity", func(p *Project) {
			*p = Project{Name: "AX", Type: ProjectMigrate, SourceSystem: "ax2012", SourceLegalEntityID: "le-1", Status: ProjectActive}
		}, "sourceLegalEntityId"},
		{"unknown system", func(p *Project) {
			*p = Project{Name: "AX", Type: ProjectMigrate, SourceSystem: "oracle", Status: ProjectActive}
		}, "sourceSystem"},
		{"package with system", func(p *Project) {
			*p = Project{Name: "Cfg", Type: ProjectConfigPackage, SourceSystem: "ax2012", Status: ProjectActive}
		}, "projectType"},
		{"bad status", func(p *Project) { p.Status = "paused" }, "status"},
		{"bad cron", func(p *Project) { p.ScheduleCron = "0 25 * * *" }, "scheduleCron"},
		// FR-10.16: recurring imports run at most every 15 minutes.
		{"cron too frequent", func(p *Project) { p.ScheduleCron = "*/10 * * * *" }, "scheduleCron"},
		{"cron every quarter hour", func(p *Project) { p.ScheduleCron = "*/15 * * * *" }, ""},
		{"80 characters, not bytes", func(p *Project) { p.Name = strings.Repeat("é", 80) }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := copyOK
			tc.edit(&p)
			bad := ValidateProject(p, isID)
			if tc.field == "" && len(bad) != 0 {
				t.Fatalf("unexpected %v", bad)
			}
			if tc.field != "" && bad[tc.field] == "" {
				t.Fatalf("want %s in %v", tc.field, bad)
			}
		})
	}
}

func TestCronMinInterval(t *testing.T) {
	for expr, want := range map[string]int{
		"0 2 * * *":          1440,
		"*/15 * * * *":       15,
		"0,30 9-17 * * 1-5":  30,
		"0 0 1,15 * 0":       1440,
		"30 1-5/2 * 1-6 1-5": 120,
		"0 0 * * 7":          1440,
		"50 23 * * *":        1440,
		"0,45 0,23 * * *":    15,
		"5,55 0 * * *":       50,
		"0-59/20 */6 * * *":  20,
		"0 0,12 * * *":       720,
		"* * * * *":          1,
		"*/5 * * * *":        5,
		"0,10 * * * *":       10,
		"59 * * * *":         60,
		"0  2  *  *  *":      1440,
		"07 02 * * *":        1440,
		"0 0 31 12 *":        1440,
		"*/59 * * * *":       1,
		"0 */23 * * *":       60,
		"10,20,40 3 * * *":   10,
	} {
		if got, ok := CronMinInterval(expr); !ok || got != want {
			t.Errorf("%q: got %d %v, want %d", expr, got, ok, want)
		}
	}
	for _, bad := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "5-1 * * * *",
		"*/0 * * * *", "5/2 * * * *", "a * * * *", "@daily", "1- * * * *", "+5 * * * *", "-5 * * * *", "0 0 * JAN *", "0 0 * * MON", "005 * * * *",
		"*/60 * * * *", "0," + strings.Repeat("1,", 60) + "2 * * * *"} {
		if _, ok := CronMinInterval(bad); ok {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestValidateRewriteRules(t *testing.T) {
	// The longer prefix first is fine: USMF-01 takes rule 1 and USMF-02 falls through to rule 2.
	if bad := ValidateRewriteRules([]KeyRewrite{{KeyField, "USMF-01", "CAMF-01"}, {KeyField, "USMF", "CAMF"}, {"dataAreaId", "USMF", "CAMF"}, {KeyField, "USRT", "CART"}}); len(bad) != 0 {
		t.Fatalf("valid rules rejected: %v", bad)
	}
	if bad := ValidateRewriteRules(nil); len(bad) != 0 {
		t.Fatalf("an empty rule set clears the rules: %v", bad)
	}
	for name, rules := range map[string][]KeyRewrite{
		"rules[0].field": {{"", "A", "B"}},
		"rules[0].from":  {{KeyField, "", "B"}},
		"rules[0].to":    {{KeyField, "A", "A"}},
		"rules[1].from":  {{KeyField, "A", "B"}, {KeyField, "A", "C"}},
		// US- matches every USMF- key first, so the second rule could never apply.
		"rules[2].from": {{KeyField, "US-", "CA-"}, {"dataAreaId", "USMF", "CAMF"}, {KeyField, "US-MF", "CA-MF"}},
	} {
		if bad := ValidateRewriteRules(rules); bad[name] == "" {
			t.Errorf("want %s in %v", name, bad)
		}
	}
	if bad := ValidateRewriteRules(make([]KeyRewrite, 201)); bad["rules"] == "" {
		t.Errorf("201 rules should be refused: %v", bad)
	}
}
