package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func doc(id, party, category, amount, currency string, date time.Time, texts ...string) AuditDocument {
	return AuditDocument{
		ID: id, PartyID: party, Category: category, Amount: decimal.RequireFromString(amount),
		Currency: currency, Date: date, Texts: texts,
	}
}

func TestKeywordHits(t *testing.T) {
	tests := []struct {
		name     string
		texts    []string
		keywords []string
		want     []string
	}{
		{"whole word, case and punctuation folded", []string{"Bought a Gift-Card"}, []string{"gift card"}, []string{"gift card"}},
		{"substring is not a word match", []string{"giftcards for staff"}, []string{"gift"}, nil},
		{"several keywords sorted", []string{"alcohol and gifts"}, []string{"gifts", "alcohol", "cash"}, []string{"alcohol", "gifts"}},
		{"blank keyword ignored", []string{"cash"}, []string{"  ", "cash"}, []string{"cash"}},
		{"no text", nil, []string{"cash"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := KeywordHits(tc.texts, tc.keywords)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestOnWatchList(t *testing.T) {
	watch := []string{"Jane  Doe", "ACME Staff"}
	if !OnWatchList("jane doe", watch) {
		t.Fatal("normalized name must match")
	}
	if OnWatchList("Jane Does", watch) {
		t.Fatal("different name must not match")
	}
	if OnWatchList("   ", watch) {
		t.Fatal("blank party never matches")
	}
}

func TestThresholdExceeded(t *testing.T) {
	limit := decimal.NewFromInt(100)
	tests := []struct {
		name         string
		amount       string
		currency     string
		limitCurr    string
		wantExceeded bool
		wantScore    int64
	}{
		{"above limit", "150", "USD", "USD", true, 150},
		{"at limit is not above", "100", "USD", "USD", false, 100},
		{"below limit", "40", "USD", "USD", false, 40},
		{"other currency not compared", "500", "EUR", "USD", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, pct := ThresholdExceeded(decimal.RequireFromString(tc.amount), tc.currency, limit, tc.limitCurr)
			if ok != tc.wantExceeded || pct.IntPart() != tc.wantScore {
				t.Fatalf("want (%v, %d), got (%v, %s)", tc.wantExceeded, tc.wantScore, ok, pct)
			}
		})
	}
	if ok, _ := ThresholdExceeded(decimal.NewFromInt(5), "USD", decimal.Zero, "USD"); ok {
		t.Fatal("a zero limit disables the rule")
	}
}

func TestOutsideBusinessCalendar(t *testing.T) {
	holidays := map[string]bool{"2026-12-25": true}
	tests := []struct {
		date string
		want bool
	}{
		{"2026-10-03", true},  // Saturday
		{"2026-10-04", true},  // Sunday
		{"2026-10-05", false}, // Monday
		{"2026-12-25", true},  // listed holiday (Friday)
	}
	for _, tc := range tests {
		d, _ := time.Parse("2006-01-02", tc.date)
		if got := OutsideBusinessCalendar(d, holidays); got != tc.want {
			t.Fatalf("%s: want %v, got %v", tc.date, tc.want, got)
		}
	}
}

func TestSelectSample_Errors(t *testing.T) {
	ids := []string{"a", "b"}
	for name, s := range map[string]SampleSpec{
		"no seed":        {Percent: 50},
		"no selector":    {Seed: "s"},
		"percent over":   {Seed: "s", Percent: 101},
		"negative every": {Seed: "s", EveryK: -2},
	} {
		if _, err := SelectSample(ids, s); err == nil {
			t.Fatalf("%s: want error", name)
		}
	}
}

func TestEvaluateAudit_RuleTypes(t *testing.T) {
	sat := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	docs := []AuditDocument{
		doc("d1", "P1", "MEALS", "250", "USD", mon, "team dinner gift cards"),
		doc("d2", "P2", "MEALS", "40", "USD", sat),
		doc("d3", "EMP-7", "SERVICES", "900", "USD", mon),
		doc("d4", "P3", "SERVICES", "600", "USD", mon),
		doc("d5", "P3", "SERVICES", "600", "USD", time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)),
	}
	tests := []struct {
		name    string
		spec    AuditSpec
		wantIDs []string
	}{
		{"keyword", AuditSpec{Type: AuditRuleKeyword, Keywords: []string{"gift cards"}}, []string{"d1"}},
		{"list search", AuditSpec{Type: AuditRuleListSearch, WatchList: []string{"EMP-7"}}, []string{"d3"}},
		{"threshold per category", AuditSpec{Type: AuditRuleThreshold, Currency: "USD", Limits: map[string]decimal.Decimal{"MEALS": decimal.NewFromInt(200)}}, []string{"d1"}},
		{"weekend and holiday", AuditSpec{Type: AuditRuleWeekendHoliday}, []string{"d2"}},
		{"aggregate per party month", AuditSpec{Type: AuditRuleAggregate, Currency: "USD", Limit: decimal.NewFromInt(1000)}, []string{"d4", "d5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvaluateAudit("POL-1", 1, tc.spec, docs)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, f := range got {
				ids = append(ids, f.DocumentID)
				if f.RuleType != tc.spec.Type || f.Key == "" {
					t.Fatalf("finding missing rule type or key: %+v", f)
				}
			}
			if fmt.Sprint(ids) != fmt.Sprint(tc.wantIDs) {
				t.Fatalf("want %v, got %v", tc.wantIDs, ids)
			}
		})
	}
}

func TestEvaluateAudit_ConfigAndType(t *testing.T) {
	docs := []AuditDocument{doc("d1", "P1", "X", "1", "USD", time.Now())}
	cases := map[string]AuditSpec{
		"keyword without words": {Type: AuditRuleKeyword},
		"list without entries":  {Type: AuditRuleListSearch},
		"sampling without spec": {Type: AuditRuleSampling},
		"aggregate no limit":    {Type: AuditRuleAggregate, Currency: "USD"},
		"threshold no limits":   {Type: AuditRuleThreshold, Currency: "USD"},
		"threshold no currency": {Type: AuditRuleThreshold, Limits: map[string]decimal.Decimal{"X": decimal.NewFromInt(1)}},
		"threshold zero limit":  {Type: AuditRuleThreshold, Currency: "USD", Limits: map[string]decimal.Decimal{"X": decimal.Zero}},
		"holiday not a date":    {Type: AuditRuleWeekendHoliday, Holidays: []string{"25/12/2026"}},
	}
	for name, spec := range cases {
		_, err := EvaluateAudit("POL-1", 1, spec, docs)
		var de *Error
		if !errors.As(err, &de) || de.Code != CodeAuditRuleConfig {
			t.Fatalf("%s: want %s, got %v", name, CodeAuditRuleConfig, err)
		}
	}
	_, err := EvaluateAudit("POL-1", 1, AuditSpec{Type: "NOPE"}, docs)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeAuditRuleType {
		t.Fatalf("unknown type: want %s, got %v", CodeAuditRuleType, err)
	}
}

func TestEvaluateAudit_RerunIsIdempotent(t *testing.T) {
	docs := []AuditDocument{doc("d1", "P1", "X", "1", "USD", time.Now(), "gift")}
	spec := AuditSpec{Type: AuditRuleKeyword, Keywords: []string{"gift"}}
	a, _ := EvaluateAudit("POL-1", 2, spec, docs)
	b, _ := EvaluateAudit("POL-1", 2, spec, docs)
	if len(a) != 1 || a[0].Key != b[0].Key {
		t.Fatalf("rerun must yield the same key: %+v vs %+v", a, b)
	}
	c, _ := EvaluateAudit("POL-1", 3, spec, docs)
	if c[0].Key == a[0].Key {
		t.Fatal("a new policy version must yield a new key")
	}
}

func TestSelectSample_SeededReproducible(t *testing.T) {
	ids := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		ids = append(ids, fmt.Sprintf("doc-%03d", i))
	}
	first, err := SelectSample(ids, SampleSpec{Seed: "run-1", Percent: 10})
	if err != nil {
		t.Fatal(err)
	}
	rev := make([]string, len(ids))
	for i := range ids {
		rev[len(ids)-1-i] = ids[i]
	}
	again, _ := SelectSample(rev, SampleSpec{Seed: "run-1", Percent: 10})
	if len(first) != len(again) {
		t.Fatalf("input order must not matter: %d vs %d", len(first), len(again))
	}
	for id := range first {
		if !again[id] {
			t.Fatalf("%s missing on rerun", id)
		}
	}
	if len(first) == 0 || len(first) == len(ids) {
		t.Fatalf("10%% sample should select a strict subset, got %d", len(first))
	}
}

func TestSelectSample_EveryKSelectsOneInK(t *testing.T) {
	ids := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		ids = append(ids, fmt.Sprintf("d%03d", i))
	}
	got, err := SelectSample(ids, SampleSpec{Seed: "seed", EveryK: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 14 || len(got) > 15 {
		t.Fatalf("every 7th of 100 selects 14 or 15, got %d", len(got))
	}
}

func TestSelectSample_PropertyPercent100SelectsAll(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 50).Draw(t, "n")
		seed := rapid.StringMatching(`[a-z0-9]{1,12}`).Draw(t, "seed")
		ids := make([]string, n)
		for i := range ids {
			ids[i] = fmt.Sprintf("id-%d", i)
		}
		got, err := SelectSample(ids, SampleSpec{Seed: seed, Percent: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != n {
			t.Fatalf("100%% must select all %d documents, got %d", n, len(got))
		}
	})
}

func TestEvaluateAudit_ThresholdEvidence(t *testing.T) {
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	docs := []AuditDocument{
		doc("d1", "P1", "MEALS", "250", "USD", mon),
		doc("d2", "P1", "MEALS", "250", "EUR", mon),   // other currency is not compared
		doc("d3", "P1", "TRAVEL", "9000", "USD", mon), // no limit for the category
	}
	spec := AuditSpec{Type: AuditRuleThreshold, Currency: "USD", Limits: map[string]decimal.Decimal{"MEALS": decimal.NewFromInt(200)}}
	got, err := EvaluateAudit("POL-1", 1, spec, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DocumentID != "d1" || got[0].Score != 100 || got[0].Evidence["pctOfLimit"] != "125" {
		t.Fatalf("want d1 at 125%% of the limit, got %+v", got)
	}
}

// TestEvaluateAudit_DuplicatePlantedFixture is AC-48.13 through the run kernel:
// 1,000 vendor invoices with 12 planted duplicates give 12 findings, a rerun of the
// same policy version yields the same keys, and grouping gives one case per vendor.
func TestEvaluateAudit_DuplicatePlantedFixture(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var docs []AuditDocument
	for i := 0; i < 1000; i++ {
		d := doc(fmt.Sprintf("I%04d", i), fmt.Sprintf("V%03d", i%100), "", fmt.Sprintf("%d.00", 100+i), "USD", base.AddDate(0, 0, i*7))
		d.Number = fmt.Sprintf("INV-%d", 5000+i)
		docs = append(docs, d)
	}
	for i := 0; i < 12; i++ {
		src := docs[i]
		d := doc(fmt.Sprintf("D%04d", i), src.PartyID, "", src.Amount.String(), "USD", src.Date.AddDate(0, 0, 2))
		d.Number = src.Number + "X"
		docs = append(docs, d)
	}
	// A posted credit note repeating an invoice is never a duplicate (BR-48.14).
	credit := doc("C0001", docs[20].PartyID, "", docs[20].Amount.String(), "USD", docs[20].Date)
	credit.Number, credit.PostedCredit = docs[20].Number, true
	docs = append(docs, credit)

	spec := AuditSpec{Type: AuditRuleDuplicate}
	first, err := EvaluateAudit("POL-DUP", 1, spec, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 12 {
		t.Fatalf("want 12 findings, got %d: %+v", len(first), first)
	}
	keys := map[string]bool{}
	var vs []Violation
	for _, f := range first {
		if f.DocumentID[0] != 'D' || f.Evidence["duplicateOf"] == "" {
			t.Fatalf("the planted copy is the finding, with its original as evidence: %+v", f)
		}
		keys[f.Key] = true
		vs = append(vs, Violation{ID: "v-" + f.DocumentID, PolicyID: "POL-DUP", RuleType: f.RuleType, DocumentID: f.DocumentID, PartyID: f.Evidence["vendorId"]})
	}
	rerun, _ := EvaluateAudit("POL-DUP", 1, spec, docs)
	for _, f := range rerun {
		if !keys[f.Key] {
			t.Fatalf("rerun produced a new key %s", f.Key)
		}
	}
	drafts, err := GroupViolations(vs)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 12 {
		t.Fatalf("one case per vendor: want 12, got %d", len(drafts))
	}
}
