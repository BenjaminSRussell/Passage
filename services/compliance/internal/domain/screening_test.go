package domain

import (
	"testing"

	"pgregory.net/rapid"
)

var sdnEntry = ListEntry{ListCode: "OFAC_SDN", EntryID: "SDN-1001", Version: 3,
	Name: "Ivan Petrov Trading Company", Aliases: []string{"Petrov Trading"}, Country: "RU"}

func TestNormalize_TransliteratesAndCollapses(t *testing.T) {
	cases := map[string]string{
		"  Müller-Straße  GmbH ": "muller strasse gmbh",
		"Ørsted & Søn, A/S":      "orsted son a s",
		"José  Núñez":            "jose nunez",
		"ACME, Inc.":             "acme inc",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokenSetRatio_WordOrderAndExtraTokens(t *testing.T) {
	if got := TokenSetRatio("petrov ivan", "ivan petrov"); got != 1 {
		t.Errorf("reordered tokens should match exactly, got %v", got)
	}
	if got := TokenSetRatio("ivan petrov", "ivan petrov trading company inc"); got != 1 {
		t.Errorf("subset of tokens should match exactly, got %v", got)
	}
	if got := TokenSetRatio("acme", "zenith"); got > 0.5 {
		t.Errorf("unrelated names should score low, got %v", got)
	}
}

func TestScreen_TableDriven(t *testing.T) {
	cases := []struct {
		name      string
		subject   Subject
		threshold float64
		wantHits  int
		wantCode  string
	}{
		{name: "exact name hits", subject: Subject{Type: "PARTY", ID: "P1", Name: "Ivan Petrov Trading Company"}, wantHits: 1},
		{name: "alias hits", subject: Subject{Type: "PARTY", ID: "P2", Name: "Petrov Trading"}, wantHits: 1},
		{name: "country mismatch lowers score below default", subject: Subject{Type: "PARTY", ID: "P3",
			Name: "Ivan Petrov Trading Company", Country: "DE"}, threshold: 0.95, wantHits: 0},
		{name: "unrelated name clears", subject: Subject{Type: "PARTY", ID: "P4", Name: "Northwind Traders"}, wantHits: 0},
		{name: "empty name rejected", subject: Subject{Type: "PARTY", ID: "P5"}, wantCode: CodeScreenInput},
		{name: "bad type rejected", subject: Subject{Type: "CONTACT", ID: "P6", Name: "X"}, wantCode: CodeScreenInput},
		{name: "threshold out of range", subject: Subject{Type: "DOCUMENT", ID: "D1", Name: "X"}, threshold: 0.5, wantCode: CodeScreenInput},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits, err := Screen(ScreenInput{Subject: c.subject, Entries: []ListEntry{sdnEntry}, Threshold: c.threshold})
			if c.wantCode != "" {
				if codeOf(err) != c.wantCode {
					t.Fatalf("want %s, got %v", c.wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(hits) != c.wantHits {
				t.Fatalf("want %d hits, got %+v", c.wantHits, hits)
			}
			for _, h := range hits {
				if h.State != HitOpen {
					t.Fatalf("hit should be OPEN and above threshold: %+v", h)
				}
			}
		})
	}
}

func TestScreen_ClearedEntryIsNotReflagged(t *testing.T) {
	subj := Subject{Type: "PARTY", ID: "P1", Name: "Ivan Petrov Trading Company", Country: "RU"}
	first, err := Screen(ScreenInput{Subject: subj, Entries: []ListEntry{sdnEntry}})
	if err != nil || len(first) != 1 {
		t.Fatalf("expected one hit, got %v %v", first, err)
	}
	c := Clearance{SubjectID: "P1", ListCode: "OFAC_SDN", EntryID: "SDN-1001", EntryVersion: 3, SubjectFingerprint: Fingerprint(subj), Reason: "different DOB"}
	again, _ := Screen(ScreenInput{Subject: subj, Entries: []ListEntry{sdnEntry}, Clearances: []Clearance{c}})
	if len(again) != 0 {
		t.Fatalf("cleared pair must not be re-flagged, got %+v", again)
	}
	// A changed name, a changed list entry version or a changed party all re-open the hit.
	renamed := subj
	renamed.Name = "Ivan Petrov Trading Co"
	changedEntry := sdnEntry
	changedEntry.Version = 4
	for name, tc := range map[string]struct {
		s Subject
		e ListEntry
	}{"renamed party": {renamed, sdnEntry}, "new list version": {subj, changedEntry}} {
		hits, _ := Screen(ScreenInput{Subject: tc.s, Entries: []ListEntry{tc.e}, Clearances: []Clearance{c}})
		if len(hits) != 1 {
			t.Errorf("%s: clearance should lapse, got %+v", name, hits)
		}
	}
}

func TestDecideHit_RulesAndStates(t *testing.T) {
	st, err := DecideHit(HitOpen, DecisionClearFalsePositive, "different date of birth", "u-officer")
	if err != nil || st != HitFalsePositive {
		t.Fatalf("clear: %v %s", err, st)
	}
	st, err = DecideHit(HitOpen, DecisionConfirm, "exact match", "u-officer")
	if err != nil || st != HitConfirmed {
		t.Fatalf("confirm: %v %s", err, st)
	}
	if _, err := DecideHit(HitOpen, DecisionClearFalsePositive, "  ", "u-officer"); codeOf(err) != CodeDecisionReason {
		t.Errorf("blank reason must be refused, got %v", err)
	}
	if _, err := DecideHit(HitFalsePositive, DecisionConfirm, "late", "u-officer"); codeOf(err) != CodeHitState {
		t.Errorf("decision on a decided hit must be refused, got %v", err)
	}
}

func TestReleaseBlocked_OnlyFalsePositivesRelease(t *testing.T) {
	cases := []struct {
		states []HitState
		want   bool
	}{
		{nil, false},
		{[]HitState{HitFalsePositive, HitFalsePositive}, false},
		{[]HitState{HitFalsePositive, HitOpen}, true},
		{[]HitState{HitConfirmed}, true},
	}
	for _, c := range cases {
		if got := ReleaseBlocked(c.states); got != c.want {
			t.Errorf("ReleaseBlocked(%v) = %v, want %v", c.states, got, c.want)
		}
	}
}

// Property: a name planted into the list (any word order) is always found, and an
// unrelated long random name is never reported as an exact match.
func TestScreen_PropertyPlantedNameAlwaysHits(t *testing.T) {
	word := rapid.StringMatching(`[a-z]{4,9}`)
	rapid.Check(t, func(t *rapid.T) {
		a, b := word.Draw(t, "a"), word.Draw(t, "b")
		entry := ListEntry{ListCode: "UN_SC", EntryID: "UN-1", Version: 1, Name: a + " " + b}
		subject := Subject{Type: "PARTY", ID: "P", Name: b + " " + a}
		hits, err := Screen(ScreenInput{Subject: subject, Entries: []ListEntry{entry}})
		if err != nil || len(hits) != 1 {
			t.Fatalf("planted reordered name must hit: %v %+v", err, hits)
		}
	})
}

func TestScreen_EmptyNamesNeverMatchEverything(t *testing.T) {
	// A punctuation-only subject name normalizes to nothing and is refused rather
	// than scoring 1.0 against every entry.
	if _, err := Screen(ScreenInput{Subject: Subject{Type: SubjectParty, ID: "P1", Name: "-- !!"}, Entries: []ListEntry{sdnEntry}}); codeOf(err) != CodeScreenInput {
		t.Fatalf("punctuation-only name: want %s, got %v", CodeScreenInput, err)
	}
	// An empty alias on a list entry must not turn into a match for any subject.
	withBlankAlias := sdnEntry
	withBlankAlias.Aliases = []string{"", " . "}
	hits, err := Screen(ScreenInput{Subject: Subject{Type: SubjectParty, ID: "P2", Name: "Northwind Traders"}, Entries: []ListEntry{withBlankAlias}})
	if err != nil || len(hits) != 0 {
		t.Fatalf("blank alias matched an unrelated party: %v %+v", err, hits)
	}
	if got := TokenSetRatio("", "ivan petrov"); got != 0 {
		t.Fatalf("TokenSetRatio with an empty side = %v, want 0", got)
	}
}

func TestScreen_RefusesSubjectWithoutIDAndEntryWithoutName(t *testing.T) {
	if _, err := Screen(ScreenInput{Subject: Subject{Type: SubjectParty, Name: "Ivan Petrov"}, Entries: []ListEntry{sdnEntry}}); codeOf(err) != CodeScreenInput {
		t.Errorf("subject without id: got %v", err)
	}
	nameless := ListEntry{ListCode: "UN_SC", EntryID: "UN-9", Version: 1}
	if _, err := Screen(ScreenInput{Subject: Subject{Type: SubjectParty, ID: "P", Name: "Ivan Petrov"}, Entries: []ListEntry{nameless}}); codeOf(err) != CodeScreenInput {
		t.Errorf("entry without name: got %v", err)
	}
}

func TestScreen_CyrillicNameMeetsRomanizedEntry(t *testing.T) {
	entry := ListEntry{ListCode: "UK_OFSI", EntryID: "RUS-0042", Version: 1, Name: "Ivan Petrov"}
	hits, err := Screen(ScreenInput{Subject: Subject{Type: SubjectParty, ID: "P", Name: "Иван Петров"}, Entries: []ListEntry{entry}})
	if err != nil || len(hits) != 1 || hits[0].Score != 1 {
		t.Fatalf("Cyrillic spelling should match the romanized entry exactly: %v %+v", err, hits)
	}
}

func TestScore_AddressAndCountryShapeTheScore(t *testing.T) {
	entry := ListEntry{ListCode: "OFAC_SDN", EntryID: "1", Version: 1, Name: "Ivan Petrov", Address: "12 Tverskaya Street Moscow", Country: "RU"}
	same, fields := Score(Subject{Name: "Ivan Petrov", Address: "12 Tverskaya Street, Moscow", Country: "ru"}, entry)
	if same != 1 || len(fields) != 3 {
		t.Fatalf("same name, address and country: score %v fields %v", same, fields)
	}
	elsewhere, _ := Score(Subject{Name: "Ivan Petrov", Country: "DE"}, entry)
	if elsewhere != 0.9 {
		t.Fatalf("a country mismatch scales the score by 0.9, got %v", elsewhere)
	}
}
