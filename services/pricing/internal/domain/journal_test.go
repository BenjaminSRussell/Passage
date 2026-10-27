package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func jRec(id, cust, amount string) Record {
	return Record{ID: id, Version: 1, Status: StatusActive, Type: TypePrice, CustomerKind: CustomerKindCustomer,
		CustomerKey: cust, ProductKind: ProductKindProduct, ProductKey: "P1", Currency: "USD",
		Amount: decimal.RequireFromString(amount), ValidFrom: msDay("2026-01-01"), ValidTo: msDay("2026-12-31")}
}

func jStored(recs ...Record) map[string]Record {
	m := map[string]Record{}
	for _, r := range recs {
		m[r.ID] = r
	}
	return m
}

func jCopy(m map[string]Record) map[string]Record {
	out := make(map[string]Record, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// jApply stores each validated line's after-state, as the store does on post, and records the version it produced.
func jApply(j *Journal, stored map[string]Record) {
	for i := range j.Lines {
		l := &j.Lines[i]
		if l.After == nil {
			continue
		}
		stored[l.RecordID] = *l.After
		l.AfterVersion = l.After.Version
	}
}

func TestValidateJournal(t *testing.T) {
	cases := []struct {
		name      string
		smart     bool
		stored    map[string]Record
		lines     []JournalLine
		wantClean bool
		wantCode  string
		wantAmt   string // expected after amount of line 1 when clean
	}{
		{"create clean record", false, jStored(jRec("r1", "C1", "10")),
			[]JournalLine{{Op: OpCreate, RecordID: "r2", Record: jRec("", "C2", "12")}}, true, "", ""},
		{"create overlaps active record", false, jStored(jRec("r1", "C1", "10")),
			[]JournalLine{{Op: OpCreate, RecordID: "r2", Record: jRec("", "C1", "12")}}, false, CodeOverlap, ""},
		{"two creates overlap inside one journal", false, jStored(),
			[]JournalLine{
				{Op: OpCreate, RecordID: "a", Record: jRec("", "C1", "12")},
				{Op: OpCreate, RecordID: "b", Record: jRec("", "C1", "13")},
			}, false, CodeOverlap, ""},
		{"smart rounding stores 7.99 for 7.41 raised by 5 percent", true, jStored(jRec("r1", "C1", "7.41")),
			[]JournalLine{{Op: OpUpdate, RecordID: "r1", ExpectedVersion: 1, AdjustPct: decimal.NewFromInt(5)}}, true, "", "7.99"},
		{"without smart rounding the raw amount is kept", false, jStored(jRec("r1", "C1", "7.41")),
			[]JournalLine{{Op: OpUpdate, RecordID: "r1", ExpectedVersion: 1, AdjustPct: decimal.NewFromInt(5)}}, true, "", "7.7805"},
		{"stale expected version", false, jStored(jRec("r1", "C1", "7.41")),
			[]JournalLine{{Op: OpUpdate, RecordID: "r1", ExpectedVersion: 2, AdjustPct: decimal.NewFromInt(5)}}, false, "conflict_version", ""},
		{"adjustment below zero is negative", false, jStored(jRec("r1", "C1", "7.41")),
			[]JournalLine{{Op: OpUpdate, RecordID: "r1", ExpectedVersion: 1, AdjustPct: decimal.NewFromInt(-150)}}, false, CodeNegative, ""},
		{"delete frees the key for a create in the same journal", false, jStored(jRec("r1", "C1", "10")),
			[]JournalLine{
				{Op: OpDelete, RecordID: "r1", ExpectedVersion: 1},
				{Op: OpCreate, RecordID: "r2", Record: jRec("", "C1", "11")},
			}, true, "", ""},
		{"unknown record", false, jStored(),
			[]JournalLine{{Op: OpDelete, RecordID: "missing", ExpectedVersion: 1}}, false, CodeValidation, ""},
		{"restore of an active record", false, jStored(jRec("r1", "C1", "10")),
			[]JournalLine{{Op: OpRestore, RecordID: "r1", ExpectedVersion: 1, Record: jRec("r1", "C1", "10")}}, false, CodeValidation, ""},
		{"empty journal", false, jStored(), nil, false, "", ""},
		{"one record on two lines", false, jStored(jRec("r1", "C1", "10")),
			[]JournalLine{
				{No: 1, Op: OpUpdate, RecordID: "r1", ExpectedVersion: 1, AdjustPct: decimal.NewFromInt(5)},
				{No: 2, Op: OpDelete, RecordID: "r1", ExpectedVersion: 1},
			}, false, CodeValidation, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := Journal{LegalEntityID: "le", SmartRound: tc.smart, Lines: tc.lines}
			clean := ValidateJournal(&j, jCopy(tc.stored), DefaultSmartBands())
			if clean != tc.wantClean {
				t.Fatalf("clean = %v, want %v; lines = %+v", clean, tc.wantClean, j.Lines)
			}
			if tc.wantCode != "" {
				found := false
				for _, l := range j.Lines {
					for _, e := range l.Errors {
						if e.Code == tc.wantCode {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("want error %s on a line, got %+v", tc.wantCode, j.Lines)
				}
			}
			if tc.wantAmt != "" {
				if got := j.Lines[0].After.Amount; !got.Equal(decimal.RequireFromString(tc.wantAmt)) {
					t.Fatalf("after amount = %s, want %s", got, tc.wantAmt)
				}
			}
		})
	}
}

func TestValidateJournalNamesTheLine(t *testing.T) {
	j := Journal{LegalEntityID: "le", Lines: []JournalLine{
		{Op: OpCreate, RecordID: "a", Record: jRec("", "C9", "12")},
		{Op: OpCreate, RecordID: "b", Record: jRec("", "C1", "12")},
	}}
	ValidateJournal(&j, jStored(jRec("r1", "C1", "10")), DefaultSmartBands())
	if len(j.Lines[0].Errors) != 0 || len(j.Lines[1].Errors) != 1 {
		t.Fatalf("only line 2 must fail: %+v", j.Lines)
	}
}

// TestJournalReversalRestoresPriorState is the reversal property (AC-26.7): after
// posting and then reversing a valid journal, every touched record is back to its
// prior content, and created records are withdrawn.
func TestJournalReversalRestoresPriorState(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(t, "records")
		stored := map[string]Record{}
		for i := 0; i < n; i++ {
			amt := rapid.Int64Range(1, 100000).Draw(t, "amount")
			id := fmt.Sprintf("r%d", i)
			stored[id] = jRec(id, fmt.Sprintf("c%d", i), decimal.New(amt, -2).String())
		}
		orig := jCopy(stored)
		var lines []JournalLine
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("r%d", i)
			switch rapid.IntRange(0, 2).Draw(t, "op") {
			case 1:
				pct := rapid.IntRange(-50, 200).Draw(t, "pct")
				if pct == 0 {
					pct = 1 // a zero adjustment selects the replace-with-Record path, tested separately
				}
				lines = append(lines, JournalLine{Op: OpUpdate, RecordID: id, ExpectedVersion: 1, AdjustPct: decimal.NewFromInt(int64(pct))})
			case 2:
				lines = append(lines, JournalLine{Op: OpDelete, RecordID: id, ExpectedVersion: 1})
			}
		}
		if rapid.Bool().Draw(t, "create") {
			lines = append(lines, JournalLine{Op: OpCreate, RecordID: "new", Record: jRec("", "cnew", "5")})
		}
		if len(lines) == 0 {
			t.Skip("no operations drawn")
		}
		smart := rapid.Bool().Draw(t, "smart")
		j := Journal{LegalEntityID: "le", SmartRound: smart, Lines: lines}
		state := jCopy(orig)
		if !ValidateJournal(&j, jCopy(state), DefaultSmartBands()) {
			t.Fatalf("journal with disjoint keys must validate: %+v", j.Lines)
		}
		jApply(&j, state)

		rev := Reverse(j)
		if !ValidateJournal(&rev, jCopy(state), DefaultSmartBands()) {
			t.Fatalf("reversal must validate against the posted state: %+v", rev.Lines)
		}
		jApply(&rev, state)

		for id, want := range orig {
			got := state[id]
			if got.Status != StatusActive || !got.Amount.Equal(want.Amount) || got.CustomerKey != want.CustomerKey {
				t.Fatalf("record %s not restored: got %+v want %+v", id, got, want)
			}
		}
		if created, ok := state["new"]; ok && created.Status != StatusWithdrawn {
			t.Fatalf("created record must be withdrawn after reversal, got %s", created.Status)
		}
	})
}

func TestReverseFailsWhenRecordChangedSincePosting(t *testing.T) {
	stored := jStored(jRec("r1", "C1", "10"))
	j := Journal{LegalEntityID: "le", Lines: []JournalLine{{Op: OpUpdate, RecordID: "r1", ExpectedVersion: 1, AdjustPct: decimal.NewFromInt(10)}}}
	if !ValidateJournal(&j, jCopy(stored), DefaultSmartBands()) {
		t.Fatal("setup journal must validate")
	}
	jApply(&j, stored)
	stored["r1"] = func() Record { r := stored["r1"]; r.Version = 3; return r }()
	rev := Reverse(j)
	if ValidateJournal(&rev, jCopy(stored), DefaultSmartBands()) {
		t.Fatal("reversal over a record changed since posting must fail")
	}
}
