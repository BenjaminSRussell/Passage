package api

// AC-49.5 (issue #49 proof 2): one month of legacy documents (`fx-replay-month`, 42k documents) is
// replayed through the ledger's public posting API and the diff shows zero unexplained variances.
//
// The month is generated here, deterministically, rather than vendored: an AX 2012 legal entity's
// March 2026 of customer invoices, vendor invoices, receipts, payments, payroll and accrual journals,
// in the accounting currency. The legacy amounts are kept to six decimals (converted from the
// transaction currency at a six-decimal rate, which legacy stores unrounded), and the ledger rounds
// every line to cents, so each document leaves at most 0.005 per account on the trial balance: the
// per-document rounding BR-49.7 classifies EXPECTED under RND-001. A trial-balance tuple sums
// thousands of documents, so the test also shows that the whole-tuple 0.01 the rule used to apply
// would have left the month UNEXPLAINED.

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// fxReplayMonthDocuments is the size of `fx-replay-month` (9.49.9: one month, 42k documents);
// PASSAGE_REPLAY_DOCUMENTS overrides it.
const fxReplayMonthDocuments = 42000

type fxMonth struct {
	docs    []string
	revenue decimal.Decimal
	lines   int
}

// micro renders an amount in millionths as a decimal string.
func micro(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%06d", sign, v/1_000_000, v%1_000_000)
}

// roundCents rounds a positive amount in millionths to cents (half away from zero), in millionths.
func roundCents(v int64) int64 { return (v + 5_000) / 10_000 * 10_000 }

// fxReplayMonth generates n balanced legacy journal documents for 2026-03. Every document has one
// line per account, and only one line of a side with more than one line carries a sub-cent part,
// so the lines the ledger rounds to cents still balance (as legacy's own documents do).
func fxReplayMonth(n int) fxMonth {
	r := rand.New(rand.NewPCG(49, 3))
	var m fxMonth
	revenue := int64(0)
	amt := func(lo, hi int64) int64 { return (lo + r.Int64N(hi-lo)) * 10_000 } // whole cents in [lo, hi) cents
	frac := func() int64 { return r.Int64N(10_000) }                           // a sub-cent part, in millionths
	for i := 1; i <= n; i++ {
		day := 2 + (i*27)/(n+1) // March 2 to 28, in posting order
		type ln struct {
			account string
			amount  int64
		}
		var lines []ln
		switch k := r.IntN(100); {
		case k < 34: // customer invoice: AR against revenue and output tax
			net := amt(5_000, 900_000) + frac()
			tax := net / 10_000 * 8 / 100 * 10_000 // 8 % of the net, in whole cents
			revenue += roundCents(net)
			lines = []ln{{"1200", net + tax}, {fmt.Sprintf("40%02d", r.IntN(4)*10), -net}, {"2300", -tax}}
		case k < 58: // vendor invoice: expense and input tax against AP
			net := amt(2_000, 500_000) + frac()
			tax := net / 10_000 * 8 / 100 * 10_000
			lines = []ln{{fmt.Sprintf("6%03d", r.IntN(6)*100), net}, {"1400", tax}, {"2000", -net - tax}}
		case k < 76: // customer receipt
			a := amt(5_000, 900_000) + frac()
			lines = []ln{{"1100", a}, {"1200", -a}}
		case k < 92: // vendor payment
			a := amt(2_000, 500_000) + frac()
			lines = []ln{{"2000", a}, {"1100", -a}}
		case k < 97: // payroll: gross against net pay, withholding and benefits
			gross := amt(200_000, 2_000_000) + frac()
			wh := gross / 10_000 * 22 / 100 * 10_000
			ben := gross / 10_000 * 5 / 100 * 10_000
			lines = []ln{{"7000", gross}, {"2400", -wh}, {"2410", -ben}, {"1100", -(gross - wh - ben)}}
		default: // accrual or FX revaluation journal
			a := amt(100, 300_000) + frac()
			lines = []ln{{"6900", a}, {"2900", -a}}
		}
		var b strings.Builder
		fmt.Fprintf(&b, `{"seq":%d,"documentId":"GJ-202603-%06d","postingDate":"2026-03-%02d","currency":"USD","lines":[`, i, i, day)
		for j, l := range lines {
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"account":%q,"amount":%q}`, l.account, micro(l.amount))
		}
		b.WriteString("]}")
		m.docs = append(m.docs, b.String())
		m.lines += len(lines)
	}
	m.revenue = decimal.New(revenue, -6)
	return m
}

func TestWave49_ReplayMonthHasNoUnexplainedVariances(t *testing.T) {
	h, _, _, fake, _ := ledgerServer(t)
	n := fxReplayMonthDocuments
	if v := os.Getenv("PASSAGE_REPLAY_DOCUMENTS"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1 {
			t.Fatalf("PASSAGE_REPLAY_DOCUMENTS=%q", v)
		}
	}
	month := fxReplayMonth(n)
	body := fmt.Sprintf(`{"period":"2026-03","sourceSystem":"AX2012","legalEntityId":%q,"ledgerId":"L-US","revenue":%q,"documents":[%s]}`,
		le1, month.revenue.StringFixed(2), strings.Join(month.docs, ","))
	if len(body) > parallelBodyLimit {
		t.Fatalf("fx-replay-month body %d bytes is over the %d limit", len(body), parallelBodyLimit)
	}
	rec := do(t, h, request{method: "POST", path: "/api/v1/migration/parallel-runs/replay", tenant: tenantA, duties: allDuties, body: body, user: "migration-engineer", le: le1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("replay: %d %s", rec.Code, truncate(rec.Body.String()))
	}
	run := decode[replayDTO](t, rec)
	if run.Posted != n || len(run.Rejected) != 0 || run.DocumentsReplayed != n || len(fake.Entries()) != n {
		t.Fatalf("replay posted %d of %d (rejected %d, ledger entries %d)", run.Posted, n, len(run.Rejected), len(fake.Entries()))
	}
	if run.UnexplainedCount != 0 || !run.CanSignOff {
		t.Fatalf("AC-49.5: %d unexplained variances (%s): %s", run.UnexplainedCount, run.UnexplainedAmount, run.SignOffBlocker)
	}
	flatWouldFail, docs := 0, 0
	for _, v := range run.Variances {
		if v.Class != "EXPECTED" || v.RuleID != "RND-001" || v.Documents < 1 {
			t.Fatalf("variance %+v", v)
		}
		limit := decimal.New(int64(v.Documents), -2)
		if d := decimal.RequireFromString(v.Delta).Abs(); d.GreaterThan(limit) {
			t.Fatalf("variance %s: %s over %d documents is more than 0.01 each", v.Key, v.Delta, v.Documents)
		} else if d.GreaterThan(decimal.New(1, -2)) {
			flatWouldFail++
		}
		docs = max(docs, v.Documents)
	}
	if len(run.Variances) == 0 || run.ExpectedByRule["RND-001"] != len(run.Variances) {
		t.Fatalf("the month must show per-document rounding variances, all RND-001: %d variances %v", len(run.Variances), run.ExpectedByRule)
	}
	if n >= 1000 && flatWouldFail == 0 {
		t.Fatal("the fixture no longer exercises per-document rounding: no tuple is over a whole-tuple 0.01")
	}
	// FR-49.5 step 6: a reviewer other than the engineer who replayed signs the month off.
	so := do(t, h, request{method: "POST", path: "/api/v1/migration/parallel-runs/" + run.ParallelRunID + "/sign-off", tenant: tenantA, duties: allDuties,
		body: `{}`, user: "controller"})
	if so.Code != http.StatusOK || !strings.Contains(so.Body.String(), `"state":"SIGNED_OFF"`) {
		t.Fatalf("sign-off: %d %s", so.Code, truncate(so.Body.String()))
	}
	t.Logf("fx-replay-month: %d documents, %d lines replayed; %d TB variances, all EXPECTED by RND-001 (largest tuple %d documents; %d would be UNEXPLAINED at a whole-tuple 0.01); unexplained 0",
		n, month.lines, len(run.Variances), docs, flatWouldFail)
}
