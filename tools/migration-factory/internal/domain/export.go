package domain

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Runbook exports (9.49.15): CSV files a cutover manager keeps with the cutover record. Every file has a
// header row, RFC 4180 quoting, times in RFC 3339 UTC and amounts with six decimals. Free text that
// someone typed (a decision reason, a waiver reason, an evidence reference) is neutralised against
// spreadsheet formulas by CSVText.
const (
	ExportRunbook        = "runbook.csv"
	ExportRollbackPlan   = "rollback-plan.csv"
	ExportGoNoGo         = "go-no-go.csv"
	ExportDecisions      = "decisions.csv"
	ExportReconciliation = "reconciliation.csv"
)

// ExportFiles lists the exports of a plan, in the order the runbook screen shows them.
var ExportFiles = []string{ExportRunbook, ExportRollbackPlan, ExportGoNoGo, ExportDecisions, ExportReconciliation}

// Headers of the export files.
var (
	RunbookHeader       = []string{"seq", "planned_start", "offset_min", "step", "owner", "planned_min", "go_no_go", "pack", "status", "started_at", "started_by", "completed_at", "completed_by", "actual_min", "elapsed_min", "tone"}
	RollbackPlanHeader  = []string{"seq", "planned_start", "offset_min", "step", "owner", "planned_min", "decision_point"}
	GoNoGoHeader        = []string{"code", "title", "source", "status", "detail", "references"}
	DecisionsHeader     = []string{"seq", "decision", "decided_by", "decided_at", "approvers", "reason", "plan_version", "open_items", "waivers"}
	ReconciliationHead  = []string{"pack", "company", "run_id", "run_status", "started_at", "total", "key", "source", "target", "delta", "tolerance", "status"}
	reconciliationNoRun = "NO_RUN"
)

// CSVText returns s safe to open in a spreadsheet: a cell that starts with a character a spreadsheet
// reads as a formula (= + - @), a tab or a carriage return is prefixed with an apostrophe.
func CSVText(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func renderCSV(header []string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339Ptr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return rfc3339(*t)
}

// RenderRunbookCSV is the runbook of the plan: every step with its planned start, owner and planned
// duration and what happened to it (who started and completed it, the recorded duration, and elapsed
// against planned with the amber or red tone at now, 9.49.6).
func RenderRunbookCSV(p Plan, now time.Time, amberPct, redPct int) ([]byte, error) {
	rows := make([][]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		elapsed, tone := "", ""
		if e, t, _ := s.Timing(now, amberPct, redPct); t != "" {
			elapsed, tone = strconv.Itoa(e), t
		}
		actual := ""
		if s.Status == StepDone {
			actual = strconv.Itoa(s.ActualDurationMin)
		}
		rows = append(rows, []string{strconv.Itoa(s.Seq), rfc3339(p.T0.Add(time.Duration(s.OffsetMin) * time.Minute)), strconv.Itoa(s.OffsetMin),
			CSVText(s.Name), CSVText(s.Owner), strconv.Itoa(s.PlannedDurationMin), strconv.FormatBool(s.GoNoGo), s.Pack, s.Status,
			rfc3339Ptr(s.StartedAt), CSVText(s.StartedBy), rfc3339Ptr(s.CompletedAt), CSVText(s.CompletedBy), actual, elapsed, tone})
	}
	return renderCSV(RunbookHeader, rows)
}

// RenderRollbackPlanCSV is the generated rollback plan (FR-49.9) with each step's planned start.
func RenderRollbackPlanCSV(p Plan) ([]byte, error) {
	steps := p.RollbackPlan()
	rows := make([][]string, 0, len(steps))
	for _, s := range steps {
		rows = append(rows, []string{strconv.Itoa(s.Seq), rfc3339(p.T0.Add(time.Duration(s.OffsetMin) * time.Minute)), strconv.Itoa(s.OffsetMin),
			CSVText(s.Name), CSVText(s.Owner), strconv.Itoa(s.PlannedDurationMin), strconv.FormatBool(s.DecisionPoint)})
	}
	return renderCSV(RollbackPlanHeader, rows)
}

// RenderChecklistCSV is the go/no-go checklist as evaluated: its status, why and the runs or evidence it rests on.
func RenderChecklistCSV(items []ItemResult) ([]byte, error) {
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, []string{it.Code, it.Title, it.Source, it.Status, CSVText(it.Detail), CSVText(strings.Join(it.References, " "))})
	}
	return renderCSV(GoNoGoHeader, rows)
}

// RenderDecisionsCSV is the decision history, oldest first: who approved, why, the checklist items that
// were open and the waivers that were accepted.
func RenderDecisionsCSV(decisions []Decision) ([]byte, error) {
	rows := make([][]string, 0, len(decisions))
	for _, d := range decisions {
		waivers := make([]string, 0, len(d.Waivers))
		for _, w := range NormalizeWaivers(d.Waivers) {
			waivers = append(waivers, w.Pack+": "+w.Reason)
		}
		rows = append(rows, []string{strconv.Itoa(d.Seq), d.Decision, CSVText(d.DecidedBy), rfc3339(d.DecidedAt), CSVText(strings.Join(d.Approvers, " ")), CSVText(d.Reason),
			strconv.Itoa(d.PlanVersion), strings.Join(OpenItems(d.Checklist), " "), CSVText(strings.Join(waivers, "; "))})
	}
	return renderCSV(DecisionsHeader, rows)
}

// ReconciliationRun is the latest run of a pack and company of the plan with its control totals; Run is nil
// when there is none.
type ReconciliationRun struct {
	Pack    Pack
	Company string
	Run     *PackRun
}

// RenderReconciliationCSV lists, for every pack and entity of the plan, the latest run and each of its
// control totals (source, target, delta, tolerance and status), so the reconciliation can be signed off
// outside the factory. A pack and entity with no run has one row with status NO_RUN.
func RenderReconciliationCSV(runs []ReconciliationRun) ([]byte, error) {
	var rows [][]string
	for _, r := range runs {
		if r.Run == nil {
			rows = append(rows, []string{string(r.Pack), CSVText(r.Company), "", "", "", "", "", "", "", "", "", reconciliationNoRun})
			continue
		}
		run := r.Run
		if len(run.Totals) == 0 {
			rows = append(rows, []string{string(r.Pack), CSVText(r.Company), run.RunID, string(run.Status), rfc3339(run.CreatedAt), "", "", "", "", "", "", ""})
		}
		for _, t := range run.Totals {
			rows = append(rows, []string{string(r.Pack), CSVText(r.Company), run.RunID, string(run.Status), rfc3339(run.CreatedAt), t.Name, CSVText(keyText(t.Key)),
				t.Source.StringFixed(6), t.Target.StringFixed(6), t.Delta.StringFixed(6), t.Tolerance.Amount.StringFixed(6), string(t.Status)})
		}
	}
	return renderCSV(ReconciliationHead, rows)
}

// keyText renders a total's key as sorted name=value pairs.
func keyText(key map[string]string) string {
	names := make([]string, 0, len(key))
	for k := range key {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = fmt.Sprintf("%s=%s", k, key[k])
	}
	return strings.Join(parts, ";")
}
