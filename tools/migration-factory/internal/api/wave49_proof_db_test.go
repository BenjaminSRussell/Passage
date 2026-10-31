package api

// Wave 49 proof (issue #49) on a synthetic legacy year: an AX 2012 tenant (USMF, DEMF, GBSI) and an
// SAP ECC tenant (1000, 2000, 3000), each with a full year (2026) of invoices, payments, credit
// notes, payroll, accruals, depreciation and project transactions. The extracts and every expected
// value come from testdata/synth/synth_reference.py, an independent Python reference (python3 -I,
// standard library only) that simulates the year, derives the extracts and computes the FR-49.2
// control totals, input hashes and re-keying files from its own reading of the rules; it never runs
// this engine. Regenerate both with `python3 -I testdata/synth/synth_reference.py`.
//
// The test drives the generated cutover runbook through the API as a rehearsal: every pack load
// runs in its runbook step, and the steps a machine performs here (extract, pack loads,
// reconciliation) are timed; the other steps (meetings, master data owned by wave 48, opening user
// access) are recorded at their planned duration, since nothing local can perform them. It then
// rehearses the rollback of the same plan with the first postings after go-live.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

const synthDir = "testdata/synth"

type synthEntity struct {
	Company     string            `json:"company"`
	Currency    string            `json:"currency"`
	Parties     []string          `json:"parties"`
	LateParties []string          `json:"lateParties"`
	OpenItems   []json.RawMessage `json:"openItems"`
	Accounts    []string          `json:"accounts"`
	GLBalances  []json.RawMessage `json:"glBalances"`
	Projects    []json.RawMessage `json:"projects"`
}

type synthDataset struct {
	System        string            `json:"system"`
	CutoffTS      string            `json:"cutoffTs"`
	FreezeTS      string            `json:"freezeTs"`
	T0            string            `json:"t0"`
	Entities      []synthEntity     `json:"entities"`
	RekeyPostings []json.RawMessage `json:"rekeyPostings"`
}

type synthEntityExpected struct {
	OpenItems struct {
		Records     int `json:"records"`
		Quarantined []struct {
			LegacyID string `json:"legacyId"`
			Party    string `json:"party"`
			Rule     string `json:"rule"`
		} `json:"quarantined"`
		Totals                      map[string]string `json:"totals"`
		InputHash                   string            `json:"inputHash"`
		ExceedsDefaultQuarantineMax bool              `json:"exceedsDefaultQuarantineMax"`
	} `json:"openItems"`
	GLBalances struct {
		Records         int               `json:"records"`
		Totals          map[string]string `json:"totals"`
		ControlAccounts map[string]string `json:"controlAccounts"`
		InputHash       string            `json:"inputHash"`
	} `json:"glBalances"`
	ProjectWIP struct {
		Lines  int               `json:"lines"`
		Totals map[string]string `json:"totals"`
	} `json:"projectWip"`
	Stats struct {
		Documents    int `json:"documents"`
		JournalLines int `json:"journalLines"`
	} `json:"stats"`
}

type synthExpected struct {
	Entities map[string]synthEntityExpected `json:"entities"`
	Rekey    struct {
		JournalLines  int        `json:"journalLines"`
		DocumentLines int        `json:"documentLines"`
		Excluded      int        `json:"excluded"`
		Journals      [][]string `json:"journals"`
		Documents     [][]string `json:"documents"`
	} `json:"rekey"`
}

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(synthDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// totalKey renders a control total as the reference does: name, then key=value sorted by key.
func totalKey(name string, key map[string]string) string {
	ks := make([]string, 0, len(key))
	for k := range key {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range ks {
		b.WriteString("|" + k + "=" + key[k])
	}
	return b.String()
}

// tally counts the control totals checked across the whole proof.
type tally struct{ checked, match int }

// checkTotals requires that the run reports exactly the expected totals, each with the source
// value the reference computed and MATCH (target equal to source).
func (ty *tally) checkTotals(t *testing.T, label string, got []totalDTO, want map[string]string) {
	t.Helper()
	seen := make(map[string]bool, len(got))
	for _, c := range got {
		k := totalKey(c.Name, c.Key)
		w, ok := want[k]
		switch {
		case !ok:
			t.Errorf("%s: unexpected total %s", label, k)
		case c.Source != w:
			t.Errorf("%s: %s source %s, reference %s", label, k, c.Source, w)
		}
		ty.checked++
		if c.Status == "MATCH" && c.Target == c.Source {
			ty.match++
		} else {
			t.Errorf("%s: %s is %s (source %s, target %s)", label, k, c.Status, c.Source, c.Target)
		}
		seen[k] = true
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("%s: total %s missing", label, k)
		}
	}
}

// subset returns the totals of got whose name has the prefix.
func subset(got []totalDTO, prefix string) []totalDTO {
	var out []totalDTO
	for _, c := range got {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func merged(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// synthRuns are the run ids of one entity, kept across runbook steps.
type synthRuns struct{ gl, openItems, project string }

// rehearsal drives one system's dataset through its generated runbook.
type rehearsal struct {
	t       *testing.T
	h       http.Handler
	admin   *pgxpool.Pool
	ds      synthDataset
	exp     synthExpected
	runs    map[string]*synthRuns
	ty      *tally
	machine time.Duration
	records int
	bodies  map[string]map[string]string // company -> pack -> request body
	reruns  map[string]string            // company -> open_items re-run body (master data refreshed)
}

func (r *rehearsal) post(path, body string, want int) *runDetailDTO {
	r.t.Helper()
	rec := do(r.t, r.h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, user: "migration-engineer", body: body})
	if rec.Code != want {
		r.t.Fatalf("POST %s: %d %s", path, rec.Code, truncate(rec.Body.String()))
	}
	if want != http.StatusCreated && want != http.StatusOK {
		return nil
	}
	out := decode[runDetailDTO](r.t, rec)
	return &out
}

func truncate(s string) string {
	if len(s) > 600 {
		return s[:600] + "..."
	}
	return s
}

// extract reads the legacy extracts and builds the factory requests (the T-0 extract step).
func (r *rehearsal) extract() {
	r.t.Helper()
	readJSON(r.t, strings.ToLower(r.ds.System)+".json", &r.ds)
	r.bodies, r.reruns = map[string]map[string]string{}, map[string]string{}
	for _, e := range r.ds.Entities {
		base := map[string]any{"sourceSystem": r.ds.System, "sourceCompany": e.Company, "cutoffTs": r.ds.CutoffTS, "freezeTs": r.ds.FreezeTS}
		oi := merged2(base, map[string]any{"parties": e.Parties, "records": e.OpenItems})
		gl := merged2(base, map[string]any{"accounts": e.Accounts, "records": e.GLBalances})
		pw := merged2(base, map[string]any{"projects": e.Projects})
		r.bodies[e.Company] = map[string]string{"open_items": mustJSON(r.t, oi), "gl_balances": mustJSON(r.t, gl), "project_wip": mustJSON(r.t, pw)}
		r.reruns[e.Company] = mustJSON(r.t, merged2(oi, map[string]any{"parties": append(append([]string{}, e.Parties...), e.LateParties...)}))
		r.records += len(e.OpenItems) + len(e.GLBalances)
		for _, p := range e.Projects {
			var pr struct{ Lines []json.RawMessage }
			if err := json.Unmarshal(p, &pr); err != nil {
				r.t.Fatal(err)
			}
			r.records += len(pr.Lines)
		}
	}
}

func merged2(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func (r *rehearsal) expected(company string) synthEntityExpected {
	x, ok := r.exp.Entities[company]
	if !ok {
		r.t.Fatalf("no reference for %s %s", r.ds.System, company)
	}
	return x
}

// loadGL loads the gl_balances pack at T+4h. The open items are not loaded yet in runbook order,
// so the BR-49.2 control accounts are reconciled in the reconciliation step.
func (r *rehearsal) loadGL() {
	for _, e := range r.ds.Entities {
		x := r.expected(e.Company)
		run := r.post(glPath, r.bodies[e.Company]["gl_balances"], http.StatusCreated)
		label := r.ds.System + " " + e.Company + " gl_balances"
		if run.Status != "RECONCILED" || run.RecordsLoaded != x.GLBalances.Records || run.RecordsQuarantined != 0 || run.InputHash != x.GLBalances.InputHash {
			r.t.Fatalf("%s: %+v (reference hash %s)", label, run.runDTO, x.GLBalances.InputHash)
		}
		r.ty.checkTotals(r.t, label, run.ControlTotals, x.GLBalances.Totals)
		r.runs[e.Company].gl = run.RunID
	}
}

// loadOpenItems loads open_items, releases the records of vendors onboarded after the master-data
// snapshot with the vendor added, and checks the reconciled run.
func (r *rehearsal) loadOpenItems() {
	for _, e := range r.ds.Entities {
		x := r.expected(e.Company)
		label := r.ds.System + " " + e.Company + " open_items"
		body := r.bodies[e.Company]["open_items"]
		if x.OpenItems.ExceedsDefaultQuarantineMax {
			// MIG-1504 at the default migration.quarantine_max_pct of 1 %: nothing is stored, and the
			// rehearsal operator raises the tenant parameter to 5 % for this entity.
			rec := do(r.t, r.h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: body})
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "MIG-1504") {
				r.t.Fatalf("%s at the default quarantine maximum: %d %s", label, rec.Code, truncate(rec.Body.String()))
			}
			if n := count(r.t, r.admin, `select count(*) from pack_run where pack = 'open_items' and source_system = $1 and source_company = $2`, r.ds.System, e.Company); n != 0 {
				r.t.Fatalf("%s: MIG-1504 stored %d runs", label, n)
			}
			body = strings.Replace(body, `"parties":`, `"quarantineMaxPct":"5","parties":`, 1)
		}
		run := r.post(runsPath, body, http.StatusCreated)
		q := len(x.OpenItems.Quarantined)
		if run.RecordsIn != x.OpenItems.Records || run.RecordsQuarantined != q || run.RecordsLoaded != x.OpenItems.Records-q || run.InputHash != x.OpenItems.InputHash {
			r.t.Fatalf("%s: %+v (reference: %d records, %d quarantined, hash %s)", label, run.runDTO, x.OpenItems.Records, q, x.OpenItems.InputHash)
		}
		for i, want := range x.OpenItems.Quarantined {
			got := run.Quarantined[i]
			if got.LegacyID != want.LegacyID || got.Rule != want.Rule || got.Party != want.Party {
				r.t.Fatalf("%s quarantine %d: %+v, reference %+v", label, i+1, got, want)
			}
		}
		for i, want := range x.OpenItems.Quarantined {
			rel := do(r.t, r.h, request{method: "POST", path: releasePath(run.RunID, i+1), tenant: tenantA, duties: allDuties, user: "data-steward",
				body: mustJSON(r.t, map[string]any{"parties": []string{want.Party}})})
			if rel.Code != http.StatusOK {
				r.t.Fatalf("%s release %d: %d %s", label, i+1, rel.Code, truncate(rel.Body.String()))
			}
		}
		got := decode[runDetailDTO](r.t, do(r.t, r.h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantA, duties: "MigrationView"}))
		if got.Status != "RECONCILED" || got.RecordsLoaded != x.OpenItems.Records || got.RecordsQuarantined != 0 {
			r.t.Fatalf("%s after release: %+v", label, got.runDTO)
		}
		r.ty.checkTotals(r.t, label, got.ControlTotals, x.OpenItems.Totals)
		r.runs[e.Company].openItems = run.RunID
	}
}

func (r *rehearsal) loadProjects() {
	for _, e := range r.ds.Entities {
		x := r.expected(e.Company)
		label := r.ds.System + " " + e.Company + " project_wip"
		run := r.post(projectRunsPath, r.bodies[e.Company]["project_wip"], http.StatusCreated)
		if run.Status != "RECONCILED" || run.RecordsLoaded != x.ProjectWIP.Lines || run.RecordsQuarantined != 0 {
			r.t.Fatalf("%s: %+v", label, run.runDTO)
		}
		r.ty.checkTotals(r.t, label, run.ControlTotals, x.ProjectWIP.Totals)
		r.runs[e.Company].project = run.RunID
	}
}

// reconcile is the T+24h reconciliation: the GL extract runs again naming the open_items run, so
// the open items per control account are compared with the migrated GL balance (BR-49.2); every
// pack re-run loads nothing and reports the same totals (FR-49.10, AC-49.4); and the stored rows
// agree with the extracts.
func (r *rehearsal) reconcile() {
	for _, e := range r.ds.Entities {
		x := r.expected(e.Company)
		runs := r.runs[e.Company]
		label := r.ds.System + " " + e.Company
		body := strings.Replace(r.bodies[e.Company]["gl_balances"], `"accounts":`, `"openItemsRunId":"`+runs.openItems+`","accounts":`, 1)
		gl := r.post(glPath, body, http.StatusCreated)
		if gl.Status != "RECONCILED" || gl.RecordsLoaded != 0 || gl.AlreadyPresent != x.GLBalances.Records || gl.OpenItemsRunID != runs.openItems || len(gl.MismatchMessages) != 0 {
			r.t.Fatalf("%s BR-49.2 gl re-run: %+v %v", label, gl.runDTO, gl.MismatchMessages)
		}
		r.ty.checkTotals(r.t, label+" gl_balances+BR-49.2", gl.ControlTotals, merged(x.GLBalances.Totals, x.GLBalances.ControlAccounts))
		if len(subset(gl.ControlTotals, "gl_balances.control_account")) == 0 {
			r.t.Fatalf("%s: no BR-49.2 control-account totals", label)
		}

		oi := r.post(runsPath, r.reruns[e.Company], http.StatusCreated)
		if oi.Status != "RECONCILED" || oi.RecordsLoaded != 0 || oi.AlreadyPresent != x.OpenItems.Records || oi.InputHash != x.OpenItems.InputHash {
			r.t.Fatalf("%s AC-49.4 open_items re-run: %+v", label, oi.runDTO)
		}
		r.ty.checkTotals(r.t, label+" open_items re-run", oi.ControlTotals, x.OpenItems.Totals)
		pw := r.post(projectRunsPath, r.bodies[e.Company]["project_wip"], http.StatusCreated)
		if pw.Status != "RECONCILED" || pw.RecordsLoaded != 0 || pw.AlreadyPresent != x.ProjectWIP.Lines {
			r.t.Fatalf("%s AC-49.4 project_wip re-run: %+v", label, pw.runDTO)
		}
		r.ty.checkTotals(r.t, label+" project_wip re-run", pw.ControlTotals, x.ProjectWIP.Totals)

		want := x.OpenItems.Records + x.GLBalances.Records + x.ProjectWIP.Lines
		if n := count(r.t, r.admin, `select count(*) from legacy_xref where source_system = $1 and source_company = $2`, r.ds.System, e.Company); n != want {
			r.t.Fatalf("%s: legacy_xref rows %d, want one per migrated record (%d)", label, n, want)
		}
		if n := count(r.t, r.admin, `select count(*) from (select currency from gl_opening_balance where source_system = $1 and source_company = $2
			group by currency having sum(balance) <> 0) unbalanced`, r.ds.System, e.Company); n != 0 {
			r.t.Fatalf("%s: the opening balances do not net to zero in %d currencies", label, n)
		}
		if n := count(r.t, r.admin, `select count(*) from control_total c join pack_run p using (tenant_id, run_id)
			where p.source_system = $1 and p.source_company = $2 and c.status <> 'MATCH'
			  and p.run_id in ($3::uuid, $4::uuid, $5::uuid)`, r.ds.System, e.Company, runs.openItems, runs.project, gl.RunID); n != 0 {
			r.t.Fatalf("%s: %d stored totals are not MATCH", label, n)
		}
	}
}

// machineStep times f, adds it to the machine time and returns whole minutes, at least one.
func (r *rehearsal) machineStep(f func()) int {
	start := time.Now()
	f()
	d := time.Since(start)
	r.machine += d
	return int(math.Max(1, math.Ceil(d.Minutes())))
}

func stepPath(planID string, seq int, action string) string {
	return fmt.Sprintf("/api/v1/cutover/plans/%s/steps/%d/%s", planID, seq, action)
}

// AC1 (issue #49 proof 1, AC-49.1, AC-49.2) for the packs built, AC3 (AC-49.6) and the factory side
// of AC4 (AC-49.7) on the synthetic AX and SAP years.
func TestWave49_SyntheticYearCutoverRehearsal(t *testing.T) {
	h, admin := dbServer(t)
	var exp map[string]synthExpected
	readJSON(t, "expected.json", &exp)
	ty := &tally{}
	for _, system := range []string{"AX2012", "SAPECC"} {
		t.Run(system, func(t *testing.T) {
			r := &rehearsal{t: t, h: h, admin: admin, exp: exp[system], runs: map[string]*synthRuns{}, ty: ty}
			var head synthDataset
			readJSON(t, strings.ToLower(system)+".json", &head)
			entities := make([]string, 0, len(head.Entities))
			for _, e := range head.Entities {
				entities = append(entities, e.Company)
				r.runs[e.Company] = &synthRuns{}
			}
			r.ds.System = system

			gen := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: allDuties, user: "cutover-manager",
				body: mustJSON(t, map[string]any{"t0": head.T0, "packs": []string{"gl_balances", "open_items", "project_wip"}, "entities": entities})})
			if gen.Code != http.StatusCreated {
				t.Fatalf("generate: %d %s", gen.Code, gen.Body)
			}
			plan := decode[planDTO](t, gen)
			openAccess := -1
			for _, s := range plan.Steps {
				if s.Name == "Open user access" {
					openAccess = s.OffsetMin
				}
			}
			machine := map[string]func(){
				"Extract transactional packs": r.extract,
				"Load pack gl_balances":       r.loadGL,
				"Load pack open_items":        r.loadOpenItems,
				"Load pack project_wip":       r.loadProjects,
				"Reconciliation complete":     r.reconcile,
			}
			measured := map[string]bool{}
			for _, s := range plan.Steps {
				if s.OffsetMin > openAccess {
					break // after user access: hypercare, outside the AC-49.6 window
				}
				actual := s.PlannedDurationMin
				if s.OffsetMin >= 0 {
					if rec := do(t, h, request{method: "POST", path: stepPath(plan.PlanID, s.Seq, "start"), tenant: tenantA, duties: allDuties, user: "step-owner", body: `{}`}); rec.Code != http.StatusOK {
						t.Fatalf("start %q: %d %s", s.Name, rec.Code, rec.Body)
					}
					if f, ok := machine[s.Name]; ok {
						actual = r.machineStep(f)
						measured[s.Name] = true
					}
				}
				rec := do(t, h, request{method: "POST", path: stepPath(plan.PlanID, s.Seq, "complete"), tenant: tenantA, duties: allDuties, user: "step-owner",
					body: fmt.Sprintf(`{"actualDurationMin":%d}`, actual)})
				if rec.Code != http.StatusOK {
					t.Fatalf("complete %q: %d %s", s.Name, rec.Code, rec.Body)
				}
			}
			if len(measured) != len(machine) {
				t.Fatalf("the runbook did not reach every machine step: %v", measured)
			}

			// AC-49.6: schedule the stored steps from T-0 in runbook order, each starting at its offset
			// or when the step before it ends, whichever is later.
			stored := decode[planDTO](t, do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID, tenant: tenantA, duties: "CutoverManager"}))
			end, window := 0, -1
			for _, s := range stored.Steps {
				if s.OffsetMin < 0 || s.OffsetMin > openAccess {
					continue
				}
				if s.Status != "DONE" || s.Tone == "red" {
					t.Fatalf("step %q: %s, tone %q (planned %d, actual %d)", s.Name, s.Status, s.Tone, s.PlannedDurationMin, s.ActualDurationMin)
				}
				end = max(end, s.OffsetMin) + s.ActualDurationMin
				if s.Name == "Open user access" {
					window = end
				}
			}
			if window < 0 || window >= 48*60 {
				t.Fatalf("AC-49.6 rehearsal T-0 to open user access %d min, want < 2880", window)
			}
			t.Logf("%s rehearsal: T-0 to open user access %dh%02dm (< 48h); machine steps %.1fs for %d records (%.0f records/min)",
				system, window/60, window%60, r.machine.Seconds(), r.records, float64(r.records)/r.machine.Minutes())

			r.rollback(plan.PlanID)
		})
	}
	t.Logf("control totals: %d of %d MATCH, every source equal to the independent reference", ty.match, ty.checked)
	if ty.checked == 0 || ty.match != ty.checked {
		t.Fatalf("reconciliation %d/%d", ty.match, ty.checked)
	}
}

// rollback rehearses FR-49.9 on the rehearsal plan: the postings since T-0 come back as journal and
// document files exactly as the reference lays them out, every journal balancing.
func (r *rehearsal) rollback(planID string) {
	t := r.t
	x := r.exp.Rekey
	body := mustJSON(t, map[string]any{"approvers": []string{"Controller", "CIO"}, "postings": r.ds.RekeyPostings})
	rec := do(t, r.h, request{method: "POST", path: "/api/v1/cutover/plans/" + planID + "/rollback", tenant: tenantA, duties: "CutoverManager", user: "cutover-manager", body: body})
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, truncate(rec.Body.String()))
	}
	plan := decode[planDTO](t, rec)
	if !plan.RolledBack || plan.Rekeying == nil || plan.Rekeying.JournalLines != x.JournalLines || plan.Rekeying.DocumentLines != x.DocumentLines || plan.Rekeying.Excluded != x.Excluded {
		t.Fatalf("rollback summary %+v, reference %d/%d/%d", plan.Rekeying, x.JournalLines, x.DocumentLines, x.Excluded)
	}
	for _, f := range []struct {
		name string
		want [][]string
	}{{"journals.csv", x.Journals}, {"documents.csv", x.Documents}} {
		file := do(t, r.h, request{method: "GET", path: "/api/v1/cutover/plans/" + planID + "/rekeying/" + f.name, tenant: tenantA, duties: "CutoverManager"})
		if file.Code != http.StatusOK {
			t.Fatalf("%s: %d", f.name, file.Code)
		}
		rows, err := csv.NewReader(bytes.NewReader(file.Body.Bytes())).ReadAll()
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if len(rows) != len(f.want)+1 {
			t.Fatalf("%s: %d rows, reference %d", f.name, len(rows)-1, len(f.want))
		}
		for i, want := range f.want {
			if got := rows[i+1]; strings.Join(got, "\x1f") != strings.Join(want, "\x1f") {
				t.Fatalf("%s row %d: %q, reference %q", f.name, i+1, got, want)
			}
		}
		if f.name == "journals.csv" {
			net := map[string]decimal.Decimal{}
			for _, row := range rows[1:] {
				k := row[0] + "|" + row[1] + "|" + row[6]
				if row[7] != "" {
					net[k] = net[k].Add(decimal.RequireFromString(row[7]))
				}
				if row[8] != "" {
					net[k] = net[k].Sub(decimal.RequireFromString(row[8]))
				}
			}
			for k, v := range net {
				if !v.IsZero() {
					t.Fatalf("journal %s does not balance: %s", k, v)
				}
			}
		}
	}
	var payload []byte
	if err := r.admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.cutover.plan.rolledback.v1' and aggregate_id = $1`, planID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data struct {
			Journal  int `json:"rekey_journal_lines"`
			Document int `json:"rekey_document_lines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.Data.Journal != x.JournalLines || evt.Data.Document != x.DocumentLines {
		t.Fatalf("rolledback event for the coexistence adapters: %s (%v)", payload, err)
	}
}
