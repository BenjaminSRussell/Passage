package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func records(n int) []domain.Record {
	out := make([]domain.Record, n)
	for i := range out {
		out[i] = domain.Record{Key: string(rune('a'+i%26)) + string(rune('a'+i/26)), Fields: map[string]any{"v": int64(i), "s": "x"}}
	}
	return out
}

func failedChecks(r domain.ReconcileReport) map[string]bool {
	out := map[string]bool{}
	for _, c := range r.Checks {
		if !c.OK {
			out[c.Name] = true
		}
	}
	return out
}

func TestReconcile_MatchingSetsPassIgnoringOrder(t *testing.T) {
	src := records(200)
	tgt := append([]domain.Record(nil), src...)
	for i, j := 0, len(tgt)-1; i < j; i, j = i+1, j-1 {
		tgt[i], tgt[j] = tgt[j], tgt[i]
	}
	if rep := domain.Reconcile(src, tgt, 50, 7); !rep.OK() || len(rep.Checks) != 3 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestReconcile_DetectsFailures(t *testing.T) {
	src := records(100)
	altered := records(100)
	altered[10].Fields = map[string]any{"v": int64(10), "s": "CHANGED"}
	cases := []struct {
		name   string
		target []domain.Record
		sample int
		failed []string
	}{
		{"count mismatch", src[:99], 100, []string{domain.CheckRowCount, domain.CheckHashTotal, domain.CheckSampleCompare}},
		{"altered field fully sampled", altered, 100, []string{domain.CheckHashTotal, domain.CheckSampleCompare}},
		{"altered field outside sample still fails hash total", altered, 0, []string{domain.CheckHashTotal}},
		{"empty target", []domain.Record{}, 100, []string{domain.CheckRowCount, domain.CheckHashTotal, domain.CheckSampleCompare}},
		{"nil target", nil, 100, []string{domain.CheckRowCount, domain.CheckHashTotal, domain.CheckSampleCompare}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := domain.Reconcile(src, c.target, c.sample, 3)
			if rep.OK() {
				t.Fatal("report must not be OK")
			}
			got := failedChecks(rep)
			if len(got) != len(c.failed) {
				t.Fatalf("failed = %v, want %v", got, c.failed)
			}
			for _, name := range c.failed {
				if !got[name] {
					t.Fatalf("check %s should fail, failed = %v", name, got)
				}
			}
		})
	}
}

func TestReconcile_NilSourceWithRowsInTargetFails(t *testing.T) {
	if domain.Reconcile(nil, records(3), 10, 1).OK() {
		t.Fatal("extra target rows must fail")
	}
}

func TestReconcile_TwoEmptySetsPass(t *testing.T) {
	if !domain.Reconcile(nil, nil, 10, 1).OK() {
		t.Fatal("empty reconciles to empty")
	}
}

func TestReconcile_SampleIsDeterministicForSeed(t *testing.T) {
	src := records(500)
	tgt := records(500)
	tgt[123].Fields = map[string]any{"v": int64(-1)}
	a := domain.Reconcile(src, tgt, 40, 9)
	b := domain.Reconcile(src, tgt, 40, 9)
	if a.Checks[2] != b.Checks[2] {
		t.Fatalf("sample differs between identical calls: %+v vs %+v", a.Checks[2], b.Checks[2])
	}
}
