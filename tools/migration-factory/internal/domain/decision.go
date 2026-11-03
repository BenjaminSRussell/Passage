package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Go/no-go decision values (FR-49.6, 9.49.5 "record go/no-go").
const (
	DecisionGo   = "GO"
	DecisionNoGo = "NO_GO"
)

// Checklist item sources. A system item is evaluated from the factory's own data; an attested item
// is confirmed by the people recording the decision with a reference to the evidence.
const (
	ItemSourceSystem   = "system"
	ItemSourceAttested = "attested"
)

// Checklist item statuses. UNCONFIRMED is an attested item nobody has confirmed yet.
const (
	ItemMet         = "MET"
	ItemOpen        = "OPEN"
	ItemUnconfirmed = "UNCONFIRMED"
)

// Codes of the two checklist items the factory evaluates itself.
const (
	ItemPacksReconciled  = "packs_reconciled"
	ItemParallelSignedOf = "parallel_run_signed_off"
)

// ChecklistItem is one line of the phase-exit go/no-go checklist of PRD 9.49.1 (executed at T+24h).
type ChecklistItem struct {
	Code   string
	Title  string
	Source string
}

// Checklist is the go/no-go checklist, in PRD order. Any open item is NO-GO.
var Checklist = []ChecklistItem{
	{ItemPacksReconciled, "All packs of the plan RECONCILED for every entity, 0 MISMATCH", ItemSourceSystem},
	{ItemParallelSignedOf, "Parallel run for the prior month SIGNED_OFF with 0 unexplained", ItemSourceSystem},
	{"opening_tb_signed", "Opening trial balance signed by the customer controller", ItemSourceAttested},
	{"sod_clear", "SoD evaluation shows 0 unmitigated HIGH conflicts for go-live users", ItemSourceAttested},
	{"country_packs", "Country packs installed and one statutory fixture executed per entity", ItemSourceAttested},
	{"load_test", "Load test on the production cluster at 50% of target passed within SLOs", ItemSourceAttested},
	{"dr_game_day", "DR game day passed within 30 days before cutover", ItemSourceAttested},
	{"backup_verified", "Backup verified: PITR drill on production data within 7 days", ItemSourceAttested},
	{"security_clear", "Security: pen-test high/critical = 0, secrets rotated, images signed", ItemSourceAttested},
	{"integrations_smoke", "Integration endpoints (bank, EDI, e-invoicing, carriers) smoke-tested in production", ItemSourceAttested},
	{"uat_signed", "User acceptance sign-off for the 12 core processes", ItemSourceAttested},
	{"support_rota", "Support rota, escalation and rollback decision owner named", ItemSourceAttested},
	{"monitoring_green", "Monitoring dashboards and alerts green for 24 h", ItemSourceAttested},
	{"legacy_freeze_rollback", "Legacy freeze confirmed and rollback plan rehearsed", ItemSourceAttested},
}

// LegacyFreezeOffsetMin is the legacy transaction freeze of the FR-49.6 timeline (T-1d 18:00). The
// production extracts are taken from it on, so only pack runs started at or after it count for the
// checklist; earlier runs are dry runs and rehearsals.
const LegacyFreezeOffsetMin = -6 * MinutesPerHour

// PackFact is the latest run of one pack for one company, as the checklist sees it.
type PackFact struct {
	Pack      Pack
	Company   string
	RunID     string
	Status    RunStatus
	CreatedAt time.Time
}

// ParallelFact is one parallel run of the period the checklist asks for.
type ParallelFact struct {
	ParallelRunID    string
	Period           string
	SourceSystem     string
	State            string
	UnexplainedCount int
}

// ParallelPeriod is the period whose parallel run the checklist needs: the month before T-0's month (UTC).
func (p Plan) ParallelPeriod() string {
	t := p.T0.UTC()
	return time.Date(t.Year(), t.Month()-1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
}

// Attestation is the confirmation of one attested item, with where the evidence is kept.
type Attestation struct {
	Code      string `json:"code"`
	Confirmed bool   `json:"confirmed"`
	Reference string `json:"reference,omitempty"`
}

// ItemResult is a checklist item as evaluated: its status, why, and the records it rests on.
type ItemResult struct {
	Code       string   `json:"code"`
	Title      string   `json:"title"`
	Source     string   `json:"source"`
	Status     string   `json:"status"`
	Detail     string   `json:"detail,omitempty"`
	References []string `json:"references"`
}

// Decision is a recorded go/no-go decision (cutover_decision). Checklist is the evaluation the
// decision was taken on, kept as evidence.
type Decision struct {
	Seq         int
	Decision    string
	Approvers   []string
	Reason      string
	Checklist   []ItemResult
	DecidedBy   string
	DecidedAt   time.Time
	PlanVersion int
}

// GoApproval is a CutoverManager's approval of a GO in a decision round, waiting for a second,
// different approver (CUT-1605). Round is the seq the next decision takes; a decision recorded in
// between moves the round on and voids the approval.
type GoApproval struct {
	Round        int           `json:"round"`
	Approver     string        `json:"approver"`
	Attestations []Attestation `json:"attestations"`
	PlanVersion  int           `json:"planVersion"`
	ApprovedAt   time.Time     `json:"approvedAt"`
}

// ReadinessFacts is what the checklist is evaluated over.
type ReadinessFacts struct {
	Plan      Plan
	Packs     []PackFact
	Parallel  []ParallelFact
	Decisions []Decision
	// Pending holds the GO approvals of the current round (at most one: a second one records the GO).
	Pending []GoApproval
}

// Round is the seq the next decision takes.
func (f ReadinessFacts) Round() int { return len(f.Decisions) + 1 }

// EvaluateChecklist evaluates every checklist item over the facts and the attestations given. An
// attestation naming an unknown or system item, naming an item twice, or confirming without an
// evidence reference is invalid. Items are returned in checklist order.
func EvaluateChecklist(f ReadinessFacts, att []Attestation) ([]ItemResult, error) {
	byCode := map[string]Attestation{}
	for _, a := range att {
		item, ok := checklistItem(a.Code)
		switch {
		case !ok:
			return nil, fmt.Errorf("%w: %q is not a go/no-go checklist item", ErrInvalid, a.Code)
		case item.Source != ItemSourceAttested:
			return nil, fmt.Errorf("%w: checklist item %q is evaluated from the factory's data and cannot be attested", ErrInvalid, a.Code)
		}
		if _, dup := byCode[a.Code]; dup {
			return nil, fmt.Errorf("%w: checklist item %q is attested twice", ErrInvalid, a.Code)
		}
		a.Reference = strings.TrimSpace(a.Reference)
		if a.Confirmed && a.Reference == "" {
			return nil, fmt.Errorf("%w: CUT-1606 confirming %q needs a reference to the evidence", ErrInvalid, a.Code)
		}
		if len(a.Reference) > 500 {
			return nil, fmt.Errorf("%w: the evidence reference for %q is at most 500 characters", ErrInvalid, a.Code)
		}
		byCode[a.Code] = a
	}
	out := make([]ItemResult, 0, len(Checklist))
	for _, item := range Checklist {
		r := ItemResult{Code: item.Code, Title: item.Title, Source: item.Source, References: []string{}}
		switch item.Code {
		case ItemPacksReconciled:
			r.Status, r.Detail, r.References = packsReconciled(f)
			r.References = nonNil(r.References)
		case ItemParallelSignedOf:
			r.Status, r.Detail, r.References = parallelSignedOff(f.Plan.ParallelPeriod(), f.Parallel)
			r.References = nonNil(r.References)
		default:
			if a, ok := byCode[item.Code]; ok && a.Confirmed {
				r.Status, r.Detail = ItemMet, "Confirmed: "+a.Reference
			} else {
				r.Status, r.Detail = ItemUnconfirmed, "Not confirmed with a reference to its evidence."
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func checklistItem(code string) (ChecklistItem, bool) {
	for _, it := range Checklist {
		if it.Code == code {
			return it, true
		}
	}
	return ChecklistItem{}, false
}

// packsReconciled is met when, for every pack of the plan and every entity of the plan, the latest
// run of that company is RECONCILED and was started at or after the legacy freeze. Runs are matched
// to entities by their source company (the plan's entities are legacy company codes). A pack whose
// load is staged in the factory and not posted to its ledger (NotPosted) stays open even when
// RECONCILED: the plan cannot go live as if that stock were loaded.
func packsReconciled(f ReadinessFacts) (status, detail string, refs []string) {
	latest := map[string]PackFact{}
	for _, pf := range f.Packs {
		latest[string(pf.Pack)+"|"+pf.Company] = pf
	}
	freeze := f.Plan.T0.Add(LegacyFreezeOffsetMin * time.Minute)
	var missing, stale, notReconciled []string
	for _, pk := range f.Plan.Packs {
		for _, e := range f.Plan.Entities {
			pf, ok := latest[string(pk)+"|"+e]
			switch {
			case !ok:
				missing = append(missing, fmt.Sprintf("%s/%s", pk, e))
			case pf.CreatedAt.Before(freeze):
				stale = append(stale, fmt.Sprintf("%s/%s", pk, e))
				refs = append(refs, pf.RunID)
			case pf.Status != StatusReconciled:
				notReconciled = append(notReconciled, fmt.Sprintf("%s/%s is %s", pk, e, pf.Status))
				refs = append(refs, pf.RunID)
			case NotPosted(pk) != "":
				notReconciled = append(notReconciled, fmt.Sprintf("%s/%s is RECONCILED but %s", pk, e, NotPosted(pk)))
				refs = append(refs, pf.RunID)
			default:
				refs = append(refs, pf.RunID)
			}
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	sort.Strings(notReconciled)
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "no run for "+strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		parts = append(parts, "latest run started before the legacy freeze "+freeze.UTC().Format(time.RFC3339)+" for "+strings.Join(stale, ", "))
	}
	if len(notReconciled) > 0 {
		parts = append(parts, strings.Join(notReconciled, "; "))
	}
	if len(parts) > 0 {
		return ItemOpen, strings.Join(parts, "; ") + ".", refs
	}
	return ItemMet, fmt.Sprintf("%d pack/entity runs RECONCILED.", len(refs)), refs
}

// parallelSignedOff is met when the period has at least one parallel run and every parallel run of
// the period (one per legacy system) is SIGNED_OFF, which implies 0 unexplained.
func parallelSignedOff(period string, runs []ParallelFact) (status, detail string, refs []string) {
	if len(runs) == 0 {
		return ItemOpen, fmt.Sprintf("No parallel run exists for %s, the month before T-0.", period), nil
	}
	var open []string
	for _, p := range runs {
		refs = append(refs, p.ParallelRunID)
		if p.State != "SIGNED_OFF" {
			open = append(open, fmt.Sprintf("%s is %s with %d unexplained variances", p.SourceSystem, p.State, p.UnexplainedCount))
		}
	}
	if len(open) > 0 {
		return ItemOpen, fmt.Sprintf("Parallel run for %s: %s.", period, strings.Join(open, "; ")), refs
	}
	return ItemMet, fmt.Sprintf("Parallel run for %s is SIGNED_OFF with 0 unexplained.", period), refs
}

// OpenItems returns the codes of the items not MET.
func OpenItems(items []ItemResult) []string {
	var open []string
	for _, it := range items {
		if it.Status != ItemMet {
			open = append(open, it.Code)
		}
	}
	return open
}

// DecisionInput is a request to record a decision. By is the authenticated user making it; it is
// the only way an approver enters a decision.
type DecisionInput struct {
	Decision     string
	Reason       string
	Attestations []Attestation
	By           string
}

// DecisionStep is what a request does once its rules pass: either it records a decision (Complete)
// with Approvers, or it records By's approval of a GO that waits for a second approver. Attestations
// is the set the checklist is evaluated with.
type DecisionStep struct {
	Complete     bool
	Approvers    []string
	Attestations []Attestation
	Reason       string
}

// PrepareDecision applies the decision rules that do not depend on the checklist. Approvers are
// authenticated users only (CUT-1605): a NO_GO is approved by the user recording it; a GO needs two
// distinct users, so the first CutoverManager's GO is stored as an approval and the GO is recorded
// when a different CutoverManager approves the same attestations. Nothing is decided on a rolled-back
// plan or after a GO. NO_GO needs a reason and is always possible before a GO ("any open item = NO-GO").
func (p *Plan) PrepareDecision(in DecisionInput, f ReadinessFacts) (DecisionStep, error) {
	if p.RolledBack {
		return DecisionStep{}, fmt.Errorf("%w: plan was rolled back; no go/no-go decision can be recorded", ErrTransition)
	}
	for _, d := range f.Decisions {
		if d.Decision == DecisionGo {
			return DecisionStep{}, fmt.Errorf("%w: a GO decision is already recorded for this plan", ErrTransition)
		}
	}
	by := strings.TrimSpace(in.By)
	if by == "" || strings.HasPrefix(by, "service:") {
		return DecisionStep{}, fmt.Errorf("%w: a go/no-go decision must be made by an identified person, not a service", ErrInvalid)
	}
	reason := strings.TrimSpace(in.Reason)
	if len(reason) > 2000 {
		return DecisionStep{}, fmt.Errorf("%w: reason is at most 2000 characters", ErrInvalid)
	}
	switch in.Decision {
	case DecisionNoGo:
		if reason == "" {
			return DecisionStep{}, fmt.Errorf("%w: a NO_GO decision needs a reason", ErrInvalid)
		}
		return DecisionStep{Complete: true, Approvers: []string{by}, Attestations: in.Attestations, Reason: reason}, nil
	case DecisionGo:
		for _, a := range f.Pending {
			if a.Approver == by {
				return DecisionStep{}, fmt.Errorf("%w: CUT-1605 you have already approved this GO; it needs a second approval by a different CutoverManager", ErrTransition)
			}
		}
		if len(f.Pending) == 0 {
			return DecisionStep{Approvers: []string{by}, Attestations: in.Attestations, Reason: reason}, nil
		}
		first := f.Pending[0]
		if len(in.Attestations) > 0 && !SameAttestations(in.Attestations, first.Attestations) {
			return DecisionStep{}, fmt.Errorf("%w: CUT-1605 the second approver approves the attestations %s approved; send none or the same ones, or record a NO_GO to start again", ErrInvalid, first.Approver)
		}
		return DecisionStep{Complete: true, Approvers: []string{first.Approver, by}, Attestations: first.Attestations, Reason: reason}, nil
	default:
		return DecisionStep{}, fmt.Errorf("%w: decision must be GO or NO_GO", ErrInvalid)
	}
}

// RequireAllMet refuses a GO (or a GO approval) while any checklist item is not met (CUT-1607).
func RequireAllMet(items []ItemResult) error {
	if open := OpenItems(items); len(open) > 0 {
		return fmt.Errorf("%w: CUT-1607 GO refused, checklist items not met: %s", ErrInvalid, strings.Join(open, ", "))
	}
	return nil
}

// ConfirmedAttestations keeps the confirmed attestations, trimmed, in checklist order: the set an
// approval stores and a second approver is compared against.
func ConfirmedAttestations(att []Attestation) []Attestation {
	out := []Attestation{}
	for _, item := range Checklist {
		for _, a := range att {
			if a.Code == item.Code && a.Confirmed {
				out = append(out, Attestation{Code: a.Code, Confirmed: true, Reference: strings.TrimSpace(a.Reference)})
			}
		}
	}
	return out
}

// SameAttestations reports whether two attestation sets confirm the same items with the same references.
func SameAttestations(a, b []Attestation) bool {
	x, y := ConfirmedAttestations(a), ConfirmedAttestations(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
