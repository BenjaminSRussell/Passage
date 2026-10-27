package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// TestAssociation_Precedence is the association specificity rule (BR-33.3).
func TestAssociation_Precedence(t *testing.T) {
	item, group, party, site := ids.New(), ids.New(), ids.New(), ids.New()
	gA := ids.New()
	mk := func(seq int, itemScope, itemRef, partyScope, partyRef, siteScope, siteRef string) Association {
		return Association{ID: ids.New(), Trigger: TriggerPurchaseReceipt, Sequence: seq, TestGroupID: gA, ItemScope: itemScope, ItemRef: itemRef, PartyScope: partyScope, PartyRef: partyRef, SiteScope: siteScope, SiteRef: siteRef}
	}
	all := mk(1, ScopeAll, "", ScopeAll, "", ScopeAll, "")
	itemOnly := mk(2, ScopeItem, item, ScopeAll, "", ScopeAll, "")
	groupParty := mk(3, ScopeItemGroup, group, ScopeParty, party, ScopeAll, "")
	itemSiteParty := mk(4, ScopeItem, item, ScopeParty, party, ScopeSite, site)
	tc := TriggerContext{Trigger: TriggerPurchaseReceipt, ItemID: item, ItemGroupID: group, PartyID: party, SiteID: site}

	cases := []struct {
		name string

		assoc []Association

		want string
	}{
		{"most specific wins", []Association{all, itemOnly, groupParty, itemSiteParty}, itemSiteParty.ID},
		{"item group with party (5) outscores item alone (4)", []Association{groupParty, itemOnly}, groupParty.ID},
		{"all matches when nothing else does", []Association{all}, all.ID},
	}
	for _, c := range cases {
		got, ok := BestAssociation(c.assoc, tc)
		if !ok {
			t.Fatalf("%s: no match", c.name)
		}
		if c.want != "" && got.ID != c.want {
			t.Errorf("%s: got %s want %s", c.name, got.ID, c.want)
		}
	}
	tie := []Association{mk(9, ScopeAll, "", ScopeAll, "", ScopeAll, ""), mk(5, ScopeAll, "", ScopeAll, "", ScopeAll, "")}
	if got, _ := BestAssociation(tie, tc); got.Sequence != 5 {
		t.Errorf("tie: got sequence %d, want 5", got.Sequence)
	}
	if _, ok := BestAssociation([]Association{itemOnly}, TriggerContext{Trigger: TriggerPurchaseReceipt, ItemID: ids.New()}); ok {
		t.Error("a non-matching item must not match")
	}
	if _, ok := BestAssociation([]Association{all}, TriggerContext{Trigger: TriggerProductionRAF}); ok {
		t.Error("a different trigger must not match")
	}
}

func TestSplitDecision(t *testing.T) {
	lot := decimal.RequireFromString("100")
	line := func(d, q string) DecisionLine {
		return DecisionLine{Decision: d, Quantity: decimal.RequireFromString(q), Reason: "reason"}
	}
	s, err := SplitDecision([]DecisionLine{line(DecisionAccept, "90"), line(DecisionReject, "10")}, lot)
	if err != nil || !s.Accept.Equal(decimal.NewFromInt(90)) || !s.Reject.Equal(decimal.NewFromInt(10)) || !s.Deviation.IsZero() {
		t.Fatalf("accept 90 / reject 10: %+v %v", s, err)
	}
	s, err = SplitDecision([]DecisionLine{line(DecisionAcceptWithDeviation, "5"), line(DecisionScrap, "95")}, lot)
	if err != nil || !s.Deviation.Equal(decimal.NewFromInt(5)) || !s.Reject.Equal(decimal.NewFromInt(95)) {
		t.Fatalf("deviation and scrap buckets: %+v %v", s, err)
	}
	_, err = SplitDecision([]DecisionLine{line(DecisionAccept, "90")}, lot)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeQtySplit {
		t.Fatalf("short split: got %v, want %s", err, CodeQtySplit)
	}
	if _, err := SplitDecision([]DecisionLine{line("MAYBE", "100")}, lot); err == nil {
		t.Error("unknown decision must fail")
	}
}

// TestSplitDecision_Property: accept + reject + deviation always equals the lot.
func TestSplitDecision_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lot := rapid.Int64Range(1, 100000).Draw(t, "lot")
		cut := rapid.Int64Range(1, lot).Draw(t, "cut")
		kinds := []string{DecisionAccept, DecisionReject, DecisionAcceptWithDeviation, DecisionScrap}
		kind := kinds[rapid.IntRange(0, len(kinds)-1).Draw(t, "kind")]
		lines := []DecisionLine{{Decision: kind, Quantity: decimal.NewFromInt(cut), Reason: "drawn"}}
		if cut < lot {
			lines = append(lines, DecisionLine{Decision: DecisionReject, Quantity: decimal.NewFromInt(lot - cut)})
		}
		s, err := SplitDecision(lines, decimal.NewFromInt(lot))
		if err != nil {
			t.Fatal(err)
		}
		if !s.Accept.Add(s.Reject).Add(s.Deviation).Equal(decimal.NewFromInt(lot)) {
			t.Fatalf("buckets %+v do not sum to %d", s, lot)
		}
	})
}

func TestAfterResultsAndTransitions(t *testing.T) {
	if s, _ := AfterResults(StatusCreated, false, false); s != StatusInProgress {
		t.Errorf("partial results: got %s", s)
	}
	if s, _ := AfterResults(StatusInProgress, true, false); s != StatusPending {
		t.Errorf("complete results: got %s", s)
	}
	var de *Error
	if _, err := AfterResults(StatusClosed, true, true); !errors.As(err, &de) || de.Code != CodeClosed {
		t.Errorf("results on a closed order must fail with QM-0109, got %v", err)
	}
	if _, err := AfterResults(StatusPending, true, false); !errors.As(err, &de) || de.Code != CodeStatus {
		t.Errorf("first results on a pending order must fail with QM-0115, got %v", err)
	}
	if s, err := AfterResults(StatusPending, true, true); err != nil || s != StatusPending {
		t.Errorf("a retest on a pending order keeps it pending once complete: %s %v", s, err)
	}
	if _, err := AfterResults(StatusCancelled, true, true); !errors.As(err, &de) || de.Code != CodeStatus {
		t.Errorf("results on a cancelled order: %v", err)
	}
	if CanTransition(StatusClosed, StatusOpen) || CanTransition(StatusCancelled, StatusOpen) {
		t.Error("closed and cancelled are final")
	}
	if !CanTransition(StatusPending, StatusClosed) || CanTransition(StatusInProgress, StatusClosed) {
		t.Error("only a pending decision closes")
	}
}

func TestVerdictAndMissingRequired(t *testing.T) {
	req, opt := ids.New(), ids.New()
	group := TestGroup{Tests: []TestLine{{TestID: req, Required: true}, {TestID: opt}}}
	ok := func(test string, sample int, passed bool, retest int) Result {
		return Result{TestID: test, SampleNo: sample, Passed: passed, RetestNo: retest}
	}
	results := []Result{ok(req, 1, true, 0), ok(req, 2, false, 0), ok(req, 2, true, 1), ok(opt, 1, false, 0)}
	if missing := MissingRequired(group, 2, results); len(missing) != 0 {
		t.Errorf("complete required results: missing %v", missing)
	}
	if missing := MissingRequired(group, 3, results); len(missing) != 1 || missing[0] != req {
		t.Errorf("sample 3 absent: missing %v", missing)
	}
	defective, pass := Verdict(group, 2, 0, results)
	if defective != 0 || !pass {
		t.Errorf("latest retest passes sample 2: defective %d pass %v", defective, pass)
	}
	results = append(results, ok(req, 1, false, 1))
	if defective, pass := Verdict(group, 2, 0, results); defective != 1 || pass {
		t.Errorf("failed retest on sample 1: defective %d pass %v", defective, pass)
	}
}

func TestCheckRetests(t *testing.T) {
	test := ids.New()
	existing := []Result{{TestID: test, SampleNo: 1}}
	if f := CheckRetests(existing, []Result{{TestID: test, SampleNo: 1, RetestNo: 1}, {TestID: test, SampleNo: 1, RetestNo: 2}}); len(f) != 0 {
		t.Errorf("retests 1 and 2 follow result 0: %v", f)
	}
	f := CheckRetests(existing, []Result{{TestID: test, SampleNo: 2, RetestNo: 1}, {TestID: test, SampleNo: 1, RetestNo: 3}})
	if f["results[0].retestNo"] == "" || f["results[1].retestNo"] == "" {
		t.Errorf("a retest without its predecessor must be refused: %v", f)
	}
}

func TestSplitDecision_DeviationNeedsReason(t *testing.T) {
	lines := []DecisionLine{{Decision: DecisionAcceptWithDeviation, Quantity: decimal.NewFromInt(10)}}
	var de *Error
	if _, err := SplitDecision(lines, decimal.NewFromInt(10)); !errors.As(err, &de) || de.Code != CodeValidation {
		t.Fatalf("deviation without reason: %v", err)
	}
	lines[0].Reason = "length 0.2 mm over drawing, customer concession C-17"
	s, err := SplitDecision(lines, decimal.NewFromInt(10))
	if err != nil || !s.Deviation.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("deviation with reason: %+v %v", s, err)
	}
}

func TestEvaluate_UsesSampleCountForFractionalSamples(t *testing.T) {
	req := ids.New()
	group := TestGroup{Tests: []TestLine{{TestID: req, Required: true}}}
	// 10 % of a 0.5 kg lot is a 0.5 kg sample: still one physical sample that needs a result.
	o := Order{SampleQty: decimal.RequireFromString("0.5"), AcceptNo: 0}
	if ev := Evaluate(group, o, nil); len(ev.MissingRequired) != 1 {
		t.Fatalf("fractional sample must still need a result: %+v", ev)
	}
	ev := Evaluate(group, o, []Result{{TestID: req, SampleNo: 1, Passed: false}})
	if len(ev.MissingRequired) != 0 || ev.DefectiveSamples != 1 || ev.Passed {
		t.Fatalf("one failing sample with Ac 0: %+v", ev)
	}
}
