package domain

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/shopspring/decimal"
)

// The expected values in testdata/payroll-us-200.csv and payroll-retro-25.json
// come from testdata/gen_payroll_fixtures.py, an exact-fraction reference that
// derives the 2025 standard schedules from the Rev. Proc. 2024-40 brackets and
// standard deductions and computes Worksheet 1A without this package.

func readUS200(t *testing.T) []map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "payroll-us-200.csv"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]string, 0, len(rows)-1)
	for _, r := range rows[1:] {
		m := map[string]string{}
		for i, h := range rows[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return out
}

func fixtureW4(filing string, legacy bool, allowances int, step2 bool, s3, s4a, s4b, s4c string) W4 {
	return W4{FilingStatus: filing, Legacy: legacy, Allowances: allowances, MultipleJobs: step2,
		DependentsCredit: dec(s3), OtherIncome: dec(s4a), Deductions: dec(s4b), ExtraWithholding: dec(s4c)}
}

// fixtureInput builds a calculation input the way a run would: REGULAR and
// OVERTIME earnings, a 401(k) and a Section 125 pre-tax deduction.
func fixtureInput(periods int, w W4, state string, earnings map[string]string, k401, s125, ytd string) Input {
	in := Input{RunType: RunRegular, Periods: periods, W4: w, State: state,
		YTD: YTD{FICAWages: dec(ytd), FUTAWages: dec(ytd)}}
	for _, code := range []string{"REGULAR", "OVERTIME", "SHIFT_DIFF"} {
		if a, ok := earnings[code]; ok && !dec(a).IsZero() {
			in.Earnings = append(in.Earnings, Earning{Code: code, Amount: dec(a)})
		}
	}
	if !dec(k401).IsZero() {
		in.PreTax = append(in.PreTax, Deduction{Code: "401K", Amount: dec(k401)})
	}
	if !dec(s125).IsZero() {
		in.PreTax = append(in.PreTax, Deduction{Code: "MED125", Amount: dec(s125), ReducesFICA: true})
	}
	return in
}

// TestPayrollUS200_GrossToNetMatchesPub15T is AC1 / AC-43.3: 200 US scenarios
// (every filing status, both Worksheet 1A schedules, pre-2020 and 2020+ W-4s,
// Steps 3 and 4(a)-(c), all eight Pub 15-T pay frequencies, row-boundary
// straddles, the Social Security base and additional Medicare crossings, and
// 401(k) / Section 125 deductions) must match the reference to the cent.
func TestPayrollUS200_GrossToNetMatchesPub15T(t *testing.T) {
	rows := readUS200(t)
	if len(rows) != 200 {
		t.Fatalf("fixture has %d scenarios, want 200", len(rows))
	}
	rs := US2025()
	matched := 0
	for _, r := range rows {
		periods, _ := strconv.Atoi(r["periods"])
		allowances, _ := strconv.Atoi(r["allowances"])
		state := r["state"]
		w := fixtureW4(r["filing"], r["legacy"] == "true", allowances, r["step2"] == "true",
			r["step3"], r["step4a"], r["step4b"], r["step4c"])
		in := fixtureInput(periods, w, state, map[string]string{"REGULAR": r["regular"], "OVERTIME": r["overtime"]},
			r["pretax_401k"], r["pretax_s125"], r["ytd_fica"])
		in.YTD.FUTAWages = dec(r["ytd_futa"])
		st, err := Calculate(rs, in)
		if err != nil {
			t.Errorf("%s: %v", r["id"], err)
			continue
		}
		got := lineAmounts(st)
		var bracket *BracketRef
		for _, l := range st.Lines {
			if l.Code == "FED_WH" {
				bracket = l.Bracket
			}
		}
		stateWH := decimal.Zero
		if state != "" {
			stateWH = got["STATE_"+state+"_WH"]
		}
		checks := []struct {
			name string
			got  decimal.Decimal
			want string
		}{
			{"federal taxable wages", st.FedTaxableWages, r["exp_fed_wages"]},
			{"FICA wages", st.FICAWages, r["exp_fica_wages"]},
			{"FED_WH", got["FED_WH"], r["exp_fed_wh"]},
			{"SS_EE", got["SS_EE"], r["exp_ss"]},
			{"MED_EE", got["MED_EE"], r["exp_medicare"]},
			{"state WH", stateWH, r["exp_state"]},
			{"net", st.Net, r["exp_net"]},
			{"FUTA", got["FUTA"], r["exp_futa"]},
			{"MED_ER", got["MED_ER"], r["exp_medicare_er"]},
			{"employer cost", st.EmployerCost, r["exp_employer_cost"]},
		}
		ok := true
		for _, c := range checks {
			if !c.got.Equal(dec(c.want)) {
				ok = false
				t.Errorf("%s (%s): %s = %s, want %s", r["id"], r["group"], c.name, c.got.StringFixed(2), c.want)
			}
		}
		// The explain path: the statement records the adjusted annual wage and schedule.
		if bracket == nil || !bracket.AdjustedWage.Equal(dec(r["exp_adjusted_annual"])) || bracket.Schedule != r["exp_schedule"] {
			ok = false
			t.Errorf("%s: bracket %+v, want adjusted %s on %s", r["id"], bracket, r["exp_adjusted_annual"], r["exp_schedule"])
		}
		if ok {
			matched++
		}
	}
	t.Logf("payroll-us-200: %d/%d scenarios match Pub 15-T to 0.01", matched, len(rows))
}

type retroCase struct {
	Name    string `json:"name"`
	Periods int    `json:"periods"`
	State   string `json:"state"`
	W4      struct {
		Filing     string `json:"filing"`
		Legacy     bool   `json:"legacy"`
		Allowances int    `json:"allowances"`
		Step2      bool   `json:"step2"`
		Step3      string `json:"step3"`
		Step4a     string `json:"step4a"`
		Step4b     string `json:"step4b"`
		Step4c     string `json:"step4c"`
	} `json:"w4"`
	PreTax401k string `json:"pretax401k"`
	PreTaxS125 string `json:"pretaxS125"`
	YTDWages   string `json:"ytdWages"`
	Affected   []struct {
		Prior        map[string]string `json:"prior"`
		Recalculated map[string]string `json:"recalculated"`
	} `json:"affected"`
	Current  map[string]string `json:"current"`
	Expected struct {
		Retro    map[string]string `json:"retro"`
		Gross    string            `json:"gross"`
		FedWages string            `json:"fedWages"`
		FedWH    string            `json:"fedWH"`
		SS       string            `json:"ss"`
		Medicare string            `json:"medicare"`
		State    string            `json:"state"`
		Net      string            `json:"net"`
	} `json:"expected"`
}

// TestPayrollRetro25_RecalculationMatchesExpectedDeltas is AC2 / AC-43.4 over
// the payroll-retro-25 fixture: each affected finalized period is recalculated
// under the rule set it was bound to (US-FED-2025.1), RetroDelta books one
// RETRO_<code> line per code whose summed delta is non-zero (rate increases and
// decreases, hours corrections, a code only in the recalculation, deltas that
// net to zero), and the current run taxes the new gross, including retro that
// crosses the Social Security base or the additional Medicare threshold.
func TestPayrollRetro25_RecalculationMatchesExpectedDeltas(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "payroll-retro-25.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []retroCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 25 {
		t.Fatalf("fixture has %d cases, want 25", len(cases))
	}
	rs := US2025()
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			w := fixtureW4(c.W4.Filing, c.W4.Legacy, c.W4.Allowances, c.W4.Step2, c.W4.Step3, c.W4.Step4a, c.W4.Step4b, c.W4.Step4c)
			var periods []RetroPeriod
			for _, p := range c.Affected {
				prior, err := Calculate(rs, fixtureInput(c.Periods, w, c.State, p.Prior, c.PreTax401k, c.PreTaxS125, "0"))
				if err != nil {
					t.Fatal(err)
				}
				recalc, err := Calculate(rs, fixtureInput(c.Periods, w, c.State, p.Recalculated, c.PreTax401k, c.PreTaxS125, "0"))
				if err != nil {
					t.Fatal(err)
				}
				periods = append(periods, RetroPeriod{Prior: prior.Lines, Recalculated: recalc.Lines})
			}
			retro := RetroDelta(periods...)
			if len(retro) != len(c.Expected.Retro) {
				t.Fatalf("retro lines %+v, want %v", retro, c.Expected.Retro)
			}
			for _, l := range retro {
				want, ok := c.Expected.Retro[l.Code]
				if !ok || l.Kind != KindEarning || !l.Amount.Equal(dec(want)) || l.RuleVersion != rs.Code {
					t.Errorf("retro line %s %s = %s (%s), want %q", l.Kind, l.Code, l.Amount, l.RuleVersion, want)
				}
			}

			in := fixtureInput(c.Periods, w, c.State, c.Current, c.PreTax401k, c.PreTaxS125, c.YTDWages)
			in.Earnings = append(in.Earnings, RetroEarnings(retro)...)
			st, err := Calculate(rs, in)
			if err != nil {
				t.Fatal(err)
			}
			got := lineAmounts(st)
			stateWH := decimal.Zero
			if c.State != "" {
				stateWH = got["STATE_"+c.State+"_WH"]
			}
			for _, chk := range []struct {
				name string
				got  decimal.Decimal
				want string
			}{
				{"gross", st.Gross, c.Expected.Gross},
				{"federal taxable wages", st.FedTaxableWages, c.Expected.FedWages},
				{"FED_WH", got["FED_WH"], c.Expected.FedWH},
				{"SS_EE", got["SS_EE"], c.Expected.SS},
				{"MED_EE", got["MED_EE"], c.Expected.Medicare},
				{"state WH", stateWH, c.Expected.State},
				{"net", st.Net, c.Expected.Net},
			} {
				if !chk.got.Equal(dec(chk.want)) {
					t.Errorf("current period %s = %s, want %s", chk.name, chk.got.StringFixed(2), chk.want)
				}
			}
		})
	}
}

// TestPayrollFixturesMatchReference regenerates both fixtures with the Python
// reference (python3 -I) and requires the committed files to be identical, so
// an edited expected value cannot pass unnoticed.
func TestPayrollFixturesMatchReference(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	out, err := exec.Command(py, "-I", filepath.Join("testdata", "gen_payroll_fixtures.py"), dir).CombinedOutput()
	if err != nil {
		t.Fatalf("reference failed: %v\n%s", err, out)
	}
	for _, name := range []string{"payroll-us-200.csv", "payroll-retro-25.json"} {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("testdata/%s differs from the reference output", name)
		}
	}
}
