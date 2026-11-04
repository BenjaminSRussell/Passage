package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func campaignOn(from, to, group string) domain.Campaign {
	f, _ := time.Parse("2006-01-02", from)
	t, _ := time.Parse("2006-01-02", to)
	return domain.Campaign{Name: "Spring", ValidFrom: f, ValidTo: t, TargetCustomerGroup: group, Currency: "USD"}
}

// A revise never changes how a day before today priced: the start and target group of a
// started campaign are fixed, its end moves only within today and later, and a campaign that
// has not started cannot start in the past.
func TestCampaignPastChanges(t *testing.T) {
	today, _ := time.Parse("2006-01-02", "2026-11-03")
	running := campaignOn("2026-10-01", "2026-11-30", "RETAIL")
	ended := campaignOn("2026-09-01", "2026-09-30", "RETAIL")
	future := campaignOn("2026-12-01", "2026-12-31", "RETAIL")
	for _, tc := range []struct {
		name      string
		cur, next domain.Campaign
		want      []string
	}{
		{"running: shorten to today", running, campaignOn("2026-10-01", "2026-11-03", "RETAIL"), nil},
		{"running: end yesterday stops it now", running, campaignOn("2026-10-01", "2026-11-02", "RETAIL"), nil},
		{"running: extend", running, campaignOn("2026-10-01", "2027-01-31", "RETAIL"), nil},
		{"running: end before yesterday drops priced days", running, campaignOn("2026-10-01", "2026-10-15", "RETAIL"), []string{"validTo"}},
		{"running: move the start later", running, campaignOn("2026-10-10", "2026-11-30", "RETAIL"), []string{"validFrom"}},
		{"running: move the start earlier", running, campaignOn("2026-09-01", "2026-11-30", "RETAIL"), []string{"validFrom"}},
		{"running: change the group", running, campaignOn("2026-10-01", "2026-11-30", ""), []string{"targetCustomerGroup"}},
		{"ended: extend over past days", ended, campaignOn("2026-09-01", "2026-12-31", "RETAIL"), []string{"validTo"}},
		{"ended: unchanged validity", ended, campaignOn("2026-09-01", "2026-09-30", "RETAIL"), nil},
		{"future: any start from today", future, campaignOn("2026-11-03", "2026-12-15", ""), nil},
		{"future: start in the past", future, campaignOn("2026-11-01", "2026-12-31", "RETAIL"), []string{"validFrom"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.CampaignPastChanges(tc.cur, tc.next, today)
			if len(got) != len(tc.want) {
				t.Fatalf("fields = %v; want %v", got, tc.want)
			}
			for _, f := range tc.want {
				if got[f] == "" {
					t.Fatalf("fields = %v; want %s", got, f)
				}
			}
		})
	}
}

// Exported CSV cells: text a spreadsheet would run as a formula gets a leading apostrophe;
// numbers, signed ones included, stay numbers.
func TestCSVCell(t *testing.T) {
	for in, want := range map[string]string{
		"P1":                             "P1",
		"":                               "",
		"12.50":                          "12.50",
		"-12.50":                         "-12.50",
		"+5":                             "+5",
		"=HYPERLINK(\"http://x\",\"a\")": "'=HYPERLINK(\"http://x\",\"a\")",
		"+1+cmd|' /C calc'!A0":           "'+1+cmd|' /C calc'!A0",
		"-2+3":                           "'-2+3",
		"-1e3":                           "'-1e3",
		"@SUM(A1)":                       "'@SUM(A1)",
		"\t=1":                           "'\t=1",
		"\r=1":                           "'\r=1",
	} {
		if got := domain.CSVCell(in); got != want {
			t.Errorf("CSVCell(%q) = %q; want %q", in, got, want)
		}
	}
}
