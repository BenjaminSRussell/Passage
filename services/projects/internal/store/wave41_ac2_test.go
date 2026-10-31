package store

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// Wave 41 proof, AC2 (PRD AC-41.3): invoice proposals capture exactly the
// billable transactions per billing rule, over a generated database.

// ac2Txn is the test's own record of a generated transaction and the proposal
// the model says it is on ("" when it is in the pool).
type ac2Txn struct {
	id, project, txnType, property, date, wbsCode, proposal string
}

// ac2Milestone is a fixed-price WBS node: a 0-day milestone with an SNET date
// on a weekday after the project start, so its scheduled finish is that date
// (no scheduler output is read to know it).
type ac2Milestone struct {
	code     string
	billable bool
	date     string // "" for an ordinary task
}

var ac2Nodes = []ac2Milestone{
	{"1", true, "2026-09-10"},  // Thu
	{"2", true, "2026-09-30"},  // Wed
	{"3", true, "2026-10-20"},  // Tue
	{"4", false, "2026-09-04"}, // Fri, a milestone that is not billable
	{"5", false, ""},           // ordinary 5-day task
	{"6", true, "2026-11-13"},  // Fri, after every window but the last
}

var ac2AllTypes = []string{"HOUR", "COST", "ITEM", "FEE", "ON_ACCOUNT"}

// ac2Qualifies is BR-41.3 written from the PRD text for one billing rule (a
// project, a window and the requested types), independent of domain.Select,
// domain.Explain and domain.BillableTypes: CHARGEABLE, in the pool, dated in
// [from, to], project IN_PROCESS or FINISHED; a T&M rule bills the requested
// types (all five when none are named); a fixed-price rule bills only
// ON_ACCOUNT lines on billable milestones dated on or before to.
func ac2Qualifies(x ac2Txn, project, ptype, stage, from, to string, requested []string, nodes map[string]ac2Milestone) bool {
	if x.project != project || x.property != "CHARGEABLE" || x.proposal != "" || x.date < from || x.date > to {
		return false
	}
	if stage != "IN_PROCESS" && stage != "FINISHED" {
		return false
	}
	switch ptype {
	case "TIME_MATERIAL":
		return len(requested) == 0 || slices.Contains(requested, x.txnType)
	case "FIXED_PRICE":
		n, ok := nodes[x.wbsCode]
		return x.txnType == "ON_ACCOUNT" && ok && n.billable && n.date != "" && n.date <= to
	}
	return false
}

// TestAC2_ProposalsCaptureExactlyTheBillableTransactions generates N
// transactions (10,000 by default, PASSAGE_PROJECTS_AC2_TXNS to change, seed
// PASSAGE_PROJECTS_AC2_SEED, default 41) over two time-and-material projects
// and a fixed-price project, through CreateTransaction: every type, both line
// properties, dates on both sides of every window, ON_ACCOUNT lines on billable,
// non-billable, ordinary and missing WBS nodes. It then runs eleven billing
// rules with overlapping windows and type sets, interleaved with line
// removals, a cancel, an ON_HOLD project and a FINISHED one, and after each
// build compares the proposal's lines with the model's predicate: no line
// outside it (no false positive), no qualifying line left out (no false
// negative), and no transaction ever on two live proposals.
func TestAC2_ProposalsCaptureExactlyTheBillableTransactions(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(1)
	n := envInt(t, "PASSAGE_PROJECTS_AC2_TXNS", 10000)
	seed := envInt(t, "PASSAGE_PROJECTS_AC2_SEED", 41)
	rng := rand.New(rand.NewPCG(uint64(seed), 4103))

	ptypes := map[string]string{"AC2-TM1": "TIME_MATERIAL", "AC2-TM2": "TIME_MATERIAL", "AC2-FP": "FIXED_PRICE"}
	stages := map[string]string{}
	versions := map[string]int{}
	for id, pt := range ptypes {
		p := sampleProject(id)
		p.ProjectType, p.StartDate = domain.ProjectType(pt), "2026-08-03"
		if _, _, err := s.CreateProject(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TransitionProject(ctx, id, 1, domain.StageInProcess); err != nil {
			t.Fatal(err)
		}
		stages[id], versions[id] = "IN_PROCESS", 2
	}
	categories := map[string]string{"HOUR": "LABOR", "COST": "TRAVEL", "ITEM": "PARTS", "FEE": "FEES", "ON_ACCOUNT": "MILESTONE"}
	for typ, id := range categories {
		if _, _, err := s.UpsertCategory(ctx, domain.Category{ID: id, Name: id, TxnType: domain.TxnType(typ)}, 0); err != nil {
			t.Fatal(err)
		}
	}

	// Fixed-price WBS: milestones with known dates, scheduled so the stored
	// early finish exists; node ids by code for the transactions.
	nodes := map[string]ac2Milestone{}
	var wbs []domain.WBSNodeInput
	for _, m := range ac2Nodes {
		nodes[m.code] = m
		in := domain.WBSNodeInput{WBSCode: m.code, Name: "Node " + m.code, IsBillableMilestone: m.billable, DurationDays: 5}
		if m.date != "" {
			date := m.date
			in.DurationDays, in.ConstraintType, in.ConstraintDate = 0, domain.ConstraintSNET, &date
		}
		wbs = append(wbs, in)
	}
	domain.NormalizeWBS(wbs)
	working, _, err := s.CreatePlanVersion(ctx, "AC2-FP", NewPlan{Kind: domain.KindWorking}, "ac2-w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceWBS(ctx, "AC2-FP", working.ID, 1, wbs, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SchedulePlan(ctx, "AC2-FP", working.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := s.LoadWBS(ctx, "AC2-FP", working.ID)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := map[string]string{}
	for _, nd := range stored.Nodes {
		nodeID[nd.WBSCode] = nd.ID
		if m := nodes[nd.WBSCode]; m.date != "" && (nd.EarlyFinish == nil || *nd.EarlyFinish != m.date) {
			t.Fatalf("milestone %s scheduled %v, want its SNET date %s", nd.WBSCode, nd.EarlyFinish, m.date)
		}
	}

	// Generate the transactions.
	worker, item := ids.New(), ids.New()
	firstDay := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	txns := make([]*ac2Txn, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		x := &ac2Txn{date: firstDay.AddDate(0, 0, rng.IntN(93)).Format(time.DateOnly), property: "CHARGEABLE"}
		switch r := rng.IntN(100); {
		case r < 60:
			x.project = "AC2-TM1"
		case r < 75:
			x.project = "AC2-TM2"
		default:
			x.project = "AC2-FP"
		}
		if rng.IntN(4) == 0 {
			x.property = "NON_CHARGEABLE"
		}
		x.txnType = ac2AllTypes[rng.IntN(len(ac2AllTypes))]
		if x.project == "AC2-FP" && rng.IntN(2) == 0 {
			x.txnType = "ON_ACCOUNT" // half the fixed-price lines are milestone billings
		}
		in := domain.Transaction{ProjectID: x.project, TxnType: domain.TxnType(x.txnType), CategoryID: categories[x.txnType],
			LineProperty: x.property, TxnDate: x.date, Quantity: strconv.Itoa(1 + rng.IntN(8)), CostPrice: "40", SalesPrice: "95.50",
			Currency: "USD", SourceDocType: domain.SourceManual}
		switch x.txnType {
		case "HOUR":
			in.WorkerID = &worker
		case "ITEM":
			in.ItemID = &item
		}
		if x.project == "AC2-FP" {
			if k := rng.IntN(len(ac2Nodes) + 1); k < len(ac2Nodes) { // the extra draw leaves the line without a node
				x.wbsCode = ac2Nodes[k].code
				id := nodeID[x.wbsCode]
				in.WBSNodeID = &id
			}
		}
		if err := domain.PriceLine(&in); err != nil {
			t.Fatal(err)
		}
		out, created, err := s.CreateTransaction(ctx, in, fmt.Sprintf("ac2-%d", i))
		if err != nil || !created {
			t.Fatalf("transaction %d: created=%v %v", i, created, err)
		}
		x.id = out.ID
		txns = append(txns, x)
	}
	t.Logf("generated %d transactions in %s (seed %d)", n, time.Since(start).Round(time.Millisecond), seed)

	byID := map[string]*ac2Txn{}
	for _, x := range txns {
		byID[x.id] = x
	}
	type rule struct {
		project, from, to string
		types             []string
	}
	totalSelected := 0
	build := func(step int, r rule) string {
		t.Helper()
		var want []string
		for _, x := range txns {
			if ac2Qualifies(*x, r.project, ptypes[r.project], stages[r.project], r.from, r.to, r.types, nodes) {
				want = append(want, x.id)
			}
		}
		req := domain.ProposalRequest{ProjectID: r.project, From: r.from, To: r.to}
		for _, typ := range r.types {
			req.TxnTypes = append(req.TxnTypes, domain.TxnType(typ))
		}
		pr, _, err := s.CreateProposal(ctx, req, fmt.Sprintf("ac2-rule-%d", step))
		if len(want) == 0 {
			if ruleCode(err) != domain.CodeProposalEmpty {
				t.Fatalf("rule %d %+v: nothing qualifies, build = %v", step, r, err)
			}
			t.Logf("rule %d %s %s..%s %v: 0 qualify, PROJ-PROP-002", step, r.project, r.from, r.to, r.types)
			return ""
		}
		if err != nil {
			t.Fatalf("rule %d %+v: %v", step, r, err)
		}
		got := map[string]bool{}
		for _, l := range pr.Lines {
			x, ok := byID[l.TransactionID]
			switch {
			case !ok:
				t.Fatalf("rule %d: line for unknown transaction %s", step, l.TransactionID)
			case got[l.TransactionID]:
				t.Fatalf("rule %d: transaction %s twice on one proposal", step, l.TransactionID)
			case !ac2Qualifies(*x, r.project, ptypes[r.project], stages[r.project], r.from, r.to, r.types, nodes):
				t.Fatalf("rule %d: false positive %+v", step, *x)
			}
			got[l.TransactionID] = true
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("rule %d: false negative, qualifying %+v not captured", step, *byID[id])
			}
		}
		for _, id := range want {
			byID[id].proposal = pr.ID
		}
		totalSelected += len(want)
		t.Logf("rule %d %s %s..%s %v: %d captured = %d qualifying", step, r.project, r.from, r.to, r.types, len(pr.Lines), len(want))
		return pr.ID
	}
	release := func(proposalID, txnID string) {
		for _, x := range txns {
			if x.proposal == proposalID && (txnID == "" || x.id == txnID) {
				x.proposal = ""
			}
		}
	}
	transition := func(project string, to domain.Stage) {
		t.Helper()
		if _, err := s.TransitionProject(ctx, project, versions[project], to); err != nil {
			t.Fatalf("%s to %s: %v", project, to, err)
		}
		stages[project] = string(to)
		versions[project]++
	}

	p1 := build(1, rule{"AC2-TM1", "2026-09-01", "2026-09-30", []string{"HOUR", "COST"}})
	p2 := build(2, rule{"AC2-TM1", "2026-09-15", "2026-10-15", nil})
	// Remove three lines from the second proposal and cancel the first: those
	// transactions return to the pool.
	gp2, err := s.GetProposal(ctx, p2)
	if err != nil {
		t.Fatal(err)
	}
	version := gp2.Version
	for _, l := range gp2.Lines[:3] {
		if _, err := s.RemoveProposalLine(ctx, p2, version, l.TransactionID); err != nil {
			t.Fatal(err)
		}
		release(p2, l.TransactionID)
		version++
	}
	if _, err := s.DecideProposal(ctx, p1, 1, domain.ProposalCanceled); err != nil {
		t.Fatal(err)
	}
	release(p1, "")
	build(3, rule{"AC2-TM1", "2026-08-15", "2026-11-15", []string{"FEE", "ITEM", "ON_ACCOUNT"}})
	build(4, rule{"AC2-TM1", "2026-08-15", "2026-11-15", nil})
	build(5, rule{"AC2-TM1", "2026-08-15", "2026-11-15", nil}) // nothing left: PROJ-PROP-002
	build(6, rule{"AC2-FP", "2026-08-15", "2026-09-30", nil})  // milestones 1 and 2 due, 2 exactly on the end date
	build(7, rule{"AC2-FP", "2026-08-15", "2026-10-19", nil})  // later-dated lines on 1 and 2; milestone 3 due a day after
	build(8, rule{"AC2-FP", "2026-08-15", "2026-11-15", []string{"ON_ACCOUNT"}})
	transition("AC2-TM2", domain.StageOnHold)
	build(9, rule{"AC2-TM2", "2026-08-15", "2026-11-15", nil}) // ON_HOLD: PROJ-PROP-002
	transition("AC2-TM2", domain.StageInProcess)
	build(10, rule{"AC2-TM2", "2026-09-01", "2026-10-31", []string{"HOUR", "FEE"}})
	// FINISHED is billable (BR-41.3): cancel every open TM1 proposal, finish
	// the project, and one build captures every chargeable TM1 line again.
	for _, pr := range openAC2Proposals(t, s, "AC2-TM1") {
		if _, err := s.DecideProposal(ctx, pr.ID, pr.Version, domain.ProposalCanceled); err != nil {
			t.Fatal(err)
		}
		release(pr.ID, "")
	}
	transition("AC2-TM1", domain.StageFinished)
	build(11, rule{"AC2-TM1", "2026-08-15", "2026-11-15", nil})

	// Every transaction is on at most one live proposal, as the model says.
	live := map[string]string{}
	for _, project := range []string{"AC2-TM1", "AC2-TM2", "AC2-FP"} {
		for _, pr := range openAC2Proposals(t, s, project) {
			full, err := s.GetProposal(ctx, pr.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range full.Lines {
				if other, dup := live[l.TransactionID]; dup {
					t.Fatalf("transaction %s on live proposals %s and %s", l.TransactionID, other, pr.ID)
				}
				live[l.TransactionID] = pr.ID
			}
		}
	}
	chargeable := 0
	for _, x := range txns {
		if live[x.id] != x.proposal {
			t.Fatalf("transaction %s: database proposal %q, model %q", x.id, live[x.id], x.proposal)
		}
		if x.property == "CHARGEABLE" {
			chargeable++
		}
	}
	t.Logf("%d transactions, %d chargeable; %d selections over 11 rules; %d on live proposals at the end",
		n, chargeable, totalSelected, len(live))
}

// openAC2Proposals returns the DRAFT and APPROVED proposals of a project.
func openAC2Proposals(t *testing.T, s *Postgres, project string) []domain.InvoiceProposal {
	t.Helper()
	var out []domain.InvoiceProposal
	for _, st := range []domain.ProposalStatus{domain.ProposalDraft, domain.ProposalApproved} {
		after := ""
		for {
			page, err := s.ListProposals(tenantCtx(1), ProposalFilter{ProjectID: project, Status: st, AfterID: after})
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, page.Items...)
			if page.NextID == "" {
				break
			}
			after = page.NextID
		}
	}
	return out
}

func envInt(t *testing.T, name string, def int) int {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("%s=%q is not a positive integer", name, v)
	}
	return n
}
