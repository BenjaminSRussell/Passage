package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestParsePack_RejectsUnknown(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"open_items", false},
		{"bank_balances", false},
		{"OPEN_ITEMS", true},
		{"", true},
		{"journal", true},
	}
	for _, c := range cases {
		_, err := ParsePack(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParsePack(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
	if len(Packs) != 9 {
		t.Fatalf("FR-49.1 defines nine packs, got %d", len(Packs))
	}
}

func TestParseSourceSystem(t *testing.T) {
	for _, ok := range []string{"AX2012", "D365FO", "SAPECC", "S4HANA"} {
		if _, err := ParseSourceSystem(ok); err != nil {
			t.Errorf("%s should parse: %v", ok, err)
		}
	}
	if _, err := ParseSourceSystem("ORACLE"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown system should be ErrInvalid, got %v", err)
	}
}

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to RunStatus
		want     bool
	}{
		{StatusExtracted, StatusValidated, true},
		{StatusExtracted, StatusLoaded, false},
		{StatusValidated, StatusLoaded, true},
		{StatusLoaded, StatusReconciled, true},
		{StatusReconciled, StatusLoaded, false},
		{StatusLoaded, StatusRolledBack, true},
		{StatusReconciled, StatusRolledBack, true},
		{StatusFailed, StatusValidated, false},
		{StatusRolledBack, StatusReconciled, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.want {
			t.Errorf("CanTransition(%s,%s)=%v want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestAdvance_BlocksReconciledWithMismatch(t *testing.T) {
	run := &PackRun{Status: StatusLoaded}
	bad, _ := NewControlTotal("open_items.amount", nil, d("10"), d("9"), Tolerance{})
	run.Totals = []ControlTotal{bad}
	if err := run.Advance(StatusReconciled); !errors.Is(err, ErrTransition) {
		t.Fatalf("reconcile with MISMATCH must fail, got %v", err)
	}
	run.Totals = nil
	if err := run.Advance(StatusReconciled); err != nil || run.Status != StatusReconciled {
		t.Fatalf("reconcile with no mismatch should pass: %v", err)
	}
}

func TestNewControlTotal(t *testing.T) {
	cases := []struct {
		name     string
		src, tgt string
		tol      Tolerance
		want     ControlStatus
		wantErr  bool
	}{
		{"exact", "100.00", "100.00", Tolerance{}, StatusMatch, false},
		{"off by a cent, no tolerance", "100.00", "100.01", Tolerance{}, StatusMismatch, false},
		{"within configured tolerance", "100.00", "100.01", Tolerance{Amount: d("0.01"), Reason: "FX step"}, StatusWithinTolerance, false},
		{"tolerance without reason", "1", "1", Tolerance{Amount: d("0.01")}, "", true},
		{"negative tolerance", "1", "1", Tolerance{Amount: d("-1"), Reason: "x"}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewControlTotal("n", nil, d(c.src), d(c.tgt), c.tol)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			if err == nil && got.Status != c.want {
				t.Fatalf("status=%s want %s", got.Status, c.want)
			}
		})
	}
}

func TestHashRecords_OrderIndependent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(t, "n")
		recs := make([]Keyed, n)
		for i := range recs {
			recs[i] = Keyed{LegacyID: fmt.Sprintf("OI-%05d", i), Amount: decimal.New(rapid.Int64Range(-99999, 99999).Draw(t, "amt"), -2)}
		}
		rev := make([]Keyed, n)
		for i := range recs {
			rev[n-1-i] = recs[i]
		}
		if HashRecords(recs) != HashRecords(rev) {
			t.Fatalf("hash depends on order")
		}
	})
}

func TestValidate_Rules(t *testing.T) {
	parties := map[string]struct{}{"V001": {}, "V002": {}}
	base := func(id, party, cur string) Record {
		return Record{LegacyID: id, Party: party, Account: "2100", Currency: cur, Amount: d("10")}
	}
	cases := []struct {
		name     string
		in       []Record
		accepted int
		rule     string
	}{
		{"clean", []Record{base("A", "V001", "USD")}, 1, ""},
		{"vendor missing in master data", []Record{base("A", "V999", "USD")}, 0, RuleReferential},
		{"bad currency", []Record{base("A", "V001", "US$")}, 0, RuleDomain},
		{"duplicate legacy id", []Record{base("A", "V001", "USD"), base("A", "V002", "USD")}, 1, RuleUniqueness},
		{"blank legacy id", []Record{base("", "V001", "USD")}, 0, RuleUniqueness},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acc, q := Validate(c.in, parties)
			if len(acc) != c.accepted {
				t.Fatalf("accepted=%d want %d", len(acc), c.accepted)
			}
			if c.rule == "" {
				if len(q) != 0 {
					t.Fatalf("unexpected quarantine %+v", q)
				}
				return
			}
			if len(q) == 0 || q[0].Rule != c.rule {
				t.Fatalf("quarantine=%+v want rule %s", q, c.rule)
			}
		})
	}
}

func TestValidate_PartitionsEveryRecord(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 40).Draw(t, "n")
		parties := map[string]struct{}{"V1": {}}
		in := make([]Record, n)
		for i := range in {
			p := "V1"
			if rapid.Bool().Draw(t, "ok") {
				p = "VX"
			}
			in[i] = Record{LegacyID: fmt.Sprintf("R%d", i), Party: p, Currency: "EUR", Amount: d("1")}
		}
		acc, q := Validate(in, parties)
		if len(acc)+len(q) != n {
			t.Fatalf("accepted %d + quarantined %d != %d", len(acc), len(q), n)
		}
	})
}

func TestQuarantineShareExceeded(t *testing.T) {
	cases := []struct {
		q, total int
		max      string
		wantErr  bool
	}{
		{0, 100, "1", false},
		{1, 100, "1", false},
		{2, 100, "1", true},
		{0, 0, "1", false},
	}
	for _, c := range cases {
		err := QuarantineShareExceeded(c.q, c.total, d(c.max))
		if (err != nil) != c.wantErr {
			t.Errorf("q=%d total=%d err=%v", c.q, c.total, err)
		}
	}
}

func TestPlanLoad_RerunLoadsNothing(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 50).Draw(t, "n")
		recs := make([]Record, n)
		existing := map[string]struct{}{}
		for i := range recs {
			recs[i] = Record{LegacyID: fmt.Sprintf("OI-%d", i), Amount: d("1")}
			existing[XrefKey(SourceAX2012, "USMF", "open_items", recs[i].LegacyID)] = struct{}{}
		}
		plan := PlanLoad(recs, SourceAX2012, "USMF", "open_items", existing)
		if len(plan.ToLoad) != 0 || plan.AlreadyPresent != n {
			t.Fatalf("re-run loaded %d, present %d of %d", len(plan.ToLoad), plan.AlreadyPresent, n)
		}
	})
}

func TestPlanLoad_KeyIncludesCompanyAndTable(t *testing.T) {
	recs := []Record{{LegacyID: "1"}}
	existing := map[string]struct{}{XrefKey(SourceAX2012, "USMF", "open_items", "1"): {}}
	if got := PlanLoad(recs, SourceAX2012, "DEMF", "open_items", existing); len(got.ToLoad) != 1 {
		t.Fatalf("different company must not match")
	}
	if got := PlanLoad(recs, SourceAX2012, "USMF", "open_orders", existing); len(got.ToLoad) != 1 {
		t.Fatalf("different table must not match")
	}
}

func TestDiffClassifier(t *testing.T) {
	ex := Explanations{}
	if err := ex.Explain("TB", "2000|USD", " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank reason must be rejected")
	}
	if err := ex.Explain("TB", "2000|USD", "policy difference"); err != nil {
		t.Fatal(err)
	}
	legacy := []Line{
		{Area: "TB", Key: "1000|USD", Legacy: d("100.00")},
		{Area: "TB", Key: "1001|USD", Legacy: d("50.00")},
		{Area: "TB", Key: "2000|USD", Legacy: d("10.00")},
		{Area: "AP", Key: "V1", Legacy: d("5.00")},
	}
	passage := []Line{
		{Area: "TB", Key: "1000|USD", Passage: d("100.01")},
		{Area: "TB", Key: "1001|USD", Passage: d("50.00")},
		{Area: "TB", Key: "2000|USD", Passage: d("12.00")},
		{Area: "AP", Key: "V1", Passage: d("5.00")},
	}
	vs := Diff(legacy, passage, DefaultRules(), ex)
	if len(vs) != 2 {
		t.Fatalf("want 2 variances (equal pairs skipped), got %d: %+v", len(vs), vs)
	}
	byKey := map[string]Variance{}
	for _, v := range vs {
		byKey[v.Key] = v
	}
	if v := byKey["1000|USD"]; v.Class != ClassExpected || v.RuleID != RuleRND001 {
		t.Errorf("rounding variance: %+v", v)
	}
	if v := byKey["2000|USD"]; v.Class != ClassExplained || v.Reason != "policy difference" {
		t.Errorf("explained variance: %+v", v)
	}
	s := Summarize(vs)
	if s.UnexplainedCount != 0 || s.ByRule[RuleRND001] != 1 || !s.ExplainedAmount.Equal(d("2")) {
		t.Errorf("summary %+v", s)
	}
}

func TestDiff_OuterJoinMissingSideIsZero(t *testing.T) {
	vs := Diff([]Line{{Area: "INV", Key: "ITEM-1", Legacy: d("3")}}, nil, nil, Explanations{})
	if len(vs) != 1 || vs[0].Class != ClassUnexplained || !vs[0].Delta.Equal(d("-3")) {
		t.Fatalf("missing Passage line must be UNEXPLAINED with delta -3: %+v", vs)
	}
}

func TestDiff_HundredSeededVariances(t *testing.T) {
	var legacy, passage []Line
	ex := Explanations{}
	wantUnexplained := 0
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("K%03d", i)
		legacy = append(legacy, Line{Area: "AR", Key: key, Legacy: d("1000")})
		switch i % 4 {
		case 0: // rounding, EXPECTED by RND-001
			passage = append(passage, Line{Area: "AR", Key: key, Passage: d("1000.01")})
		case 1: // explained
			passage = append(passage, Line{Area: "AR", Key: key, Passage: d("1005")})
			_ = ex.Explain("AR", key, "policy difference")
		case 2: // unexplained
			passage = append(passage, Line{Area: "AR", Key: key, Passage: d("990")})
			wantUnexplained++
		default: // equal, not a variance
			passage = append(passage, Line{Area: "AR", Key: key, Passage: d("1000")})
		}
	}
	s := Summarize(Diff(legacy, passage, DefaultRules(), ex))
	if s.UnexplainedCount != wantUnexplained || s.ByRule[RuleRND001] != 25 {
		t.Fatalf("summary %+v, want unexplained %d and 25 RND-001", s, wantUnexplained)
	}
}

func TestSignOffGate(t *testing.T) {
	rev := d("1000000")
	cases := []struct {
		name string
		s    Summary
		max  string
		err  bool
	}{
		{"clean", Summary{ExplainedAmount: d("0")}, "0.1", false},
		{"unexplained blocks", Summary{UnexplainedCount: 1, UnexplainedAmount: d("0.5")}, "0.1", true},
		{"explained within 0.1%", Summary{ExplainedAmount: d("1000")}, "0.1", false},
		{"explained over 0.1%", Summary{ExplainedAmount: d("1000.01")}, "0.1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := SignOffGate(c.s, rev, d(c.max)); (err != nil) != c.err {
				t.Fatalf("err=%v want error=%v", err, c.err)
			}
		})
	}
}

func TestCheckCutoff(t *testing.T) {
	freeze := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	if err := CheckCutoff(freeze.Add(time.Hour), freeze); !errors.Is(err, ErrInvalid) {
		t.Fatalf("late cutoff must fail MIG-1501, got %v", err)
	}
	if err := CheckCutoff(freeze, freeze); err != nil {
		t.Fatalf("cutoff equal to freeze is allowed: %v", err)
	}
}

func TestOverrunTone(t *testing.T) {
	cases := []struct {
		planned, actual int
		want            string
	}{
		{100, 100, ToneOK},
		{100, 109, ToneOK},
		{100, 110, ToneAmber},
		{100, 124, ToneAmber},
		{100, 125, ToneRed},
		{0, 500, ToneOK},
	}
	for _, c := range cases {
		if got := OverrunTone(c.planned, c.actual, 110, 125); got != c.want {
			t.Errorf("OverrunTone(%d,%d)=%s want %s", c.planned, c.actual, got, c.want)
		}
	}
}

func TestDiff_ExplanationIsScopedToArea(t *testing.T) {
	ex := Explanations{}
	if err := ex.Explain("TB", "K1", "FX rounding step"); err != nil {
		t.Fatal(err)
	}
	legacy := []Line{{Area: "TB", Key: "K1", Legacy: d("100")}, {Area: "AR", Key: "K1", Legacy: d("100")}}
	passage := []Line{{Area: "TB", Key: "K1", Passage: d("105")}, {Area: "AR", Key: "K1", Passage: d("105")}}
	got := map[Area]string{}
	for _, v := range Diff(legacy, passage, DefaultRules(), ex) {
		got[v.Area] = v.Class
	}
	if got["TB"] != ClassExplained || got["AR"] != ClassUnexplained {
		t.Fatalf("a TB reason must not explain the AR line with the same key: %v", got)
	}
}

func TestParseQuarantineMaxPct(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{{"", "1", true}, {"0", "0", true}, {" 2.5 ", "2.5", true}, {"10", "10", true}, {"10.01", "", false}, {"-1", "", false}, {"x", "", false}} {
		got, err := ParseQuarantineMaxPct(c.in)
		if (err == nil) != c.ok || (c.ok && !got.Equal(d(c.want))) {
			t.Errorf("ParseQuarantineMaxPct(%q) = %s, %v", c.in, got, err)
		}
	}
}

func TestValidate_BlankAccountIsDomainFailure(t *testing.T) {
	_, q := Validate([]Record{{LegacyID: "1", Party: "V1", Currency: "USD", Account: " ", Amount: d("1")}}, map[string]struct{}{"V1": {}})
	if len(q) != 1 || q[0].Rule != RuleDomain {
		t.Fatalf("blank account must quarantine under DOMAIN: %+v", q)
	}
}

func validatedRun(t *testing.T) *PackRun {
	t.Helper()
	r := &PackRun{SourceCompany: "USMF", Status: StatusExtracted}
	if err := r.Advance(StatusValidated); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReconcileOpenItems(t *testing.T) {
	rec := func(id, acct, cur, amt string) Record {
		return Record{LegacyID: id, Party: "V1", Account: acct, Currency: cur, Amount: d(amt)}
	}
	records := []Record{rec("1", "2100", "USD", "10.00"), rec("2", "2100", "EUR", "5.50"), rec("3", "2200", "USD", "1.25")}
	t.Run("all present reconciles", func(t *testing.T) {
		r := validatedRun(t)
		present := map[string]decimal.Decimal{"1": d("10"), "2": d("5.5"), "3": d("1.25")}
		if err := ReconcileOpenItems(r, records, records, 2, present); err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusReconciled || r.RecordsLoaded != 2 || r.AlreadyPresent != 1 {
			t.Fatalf("run %+v", r)
		}
		// amount per currency (EUR, USD), per (account, currency) (2100/EUR, 2100/USD, 2200/USD), and the count.
		if len(r.Totals) != 6 {
			t.Fatalf("totals %+v", r.Totals)
		}
		if match, mismatch := CountTotals(r.Totals); match != 6 || mismatch != 0 {
			t.Fatalf("match %d mismatch %d", match, mismatch)
		}
		if r.Message() != "" {
			t.Fatalf("a run that loaded records has no MIG-1505 message: %q", r.Message())
		}
	})
	t.Run("held back record mismatches its subtotals", func(t *testing.T) {
		r := validatedRun(t)
		present := map[string]decimal.Decimal{"1": d("10"), "2": d("5.5")}
		if err := ReconcileOpenItems(r, records, records[:2], 2, present); err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusLoaded {
			t.Fatalf("status %s", r.Status)
		}
		var bad []string
		for _, tot := range r.Totals {
			if tot.Status == StatusMismatch {
				bad = append(bad, tot.Name+" "+tot.Key["account"]+" "+tot.Key["currency"])
			}
		}
		want := []string{"open_items.amount  USD", "open_items.amount_by_account 2200 USD", "open_items.count  "}
		if fmt.Sprint(bad) != fmt.Sprint(want) {
			t.Fatalf("mismatches %q, want %q", bad, want)
		}
	})
	t.Run("rerun with nothing new reports MIG-1505", func(t *testing.T) {
		r := validatedRun(t)
		present := map[string]decimal.Decimal{"1": d("10"), "2": d("5.5"), "3": d("1.25")}
		if err := ReconcileOpenItems(r, records, records, 0, present); err != nil {
			t.Fatal(err)
		}
		if r.Message() != AlreadyPresentMessage(3) {
			t.Fatalf("message %q", r.Message())
		}
	})
	t.Run("needs a validated run", func(t *testing.T) {
		r := &PackRun{Status: StatusExtracted}
		if err := ReconcileOpenItems(r, records, records, 0, nil); !errors.Is(err, ErrTransition) {
			t.Fatalf("got %v", err)
		}
	})
}
