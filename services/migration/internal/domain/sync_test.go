package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func TestCronNext(t *testing.T) {
	at := func(s string) time.Time {
		ts, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	for _, tc := range []struct{ expr, after, want string }{
		{"*/15 * * * *", "2026-11-01 10:07", "2026-11-01 10:15"},
		{"*/15 * * * *", "2026-11-01 10:15", "2026-11-01 10:30"}, // strictly after
		{"0 2 * * *", "2026-11-01 02:00", "2026-11-02 02:00"},
		{"30 6 * * 1", "2026-11-01 10:00", "2026-11-02 06:30"}, // 1 Nov 2026 is a Sunday; next Monday
		{"0 0 1 * *", "2026-11-15 00:00", "2026-12-01 00:00"},
		{"0 0 31 * *", "2026-11-15 00:00", "2026-12-31 00:00"},  // November has no 31st
		{"0 12 15 * 1", "2026-11-01 00:00", "2026-11-02 12:00"}, // day of month and weekday both set: either matches
		{"0 3 * * 7", "2026-11-01 04:00", "2026-11-08 03:00"},   // 7 is Sunday
		{"0 0 29 2 *", "2026-11-01 00:00", "2028-02-29 00:00"},
	} {
		got, ok := domain.CronNext(tc.expr, at(tc.after))
		if !ok || !got.Equal(at(tc.want)) {
			t.Errorf("CronNext(%q, %s) = %s %v, want %s", tc.expr, tc.after, got.Format("2006-01-02 15:04"), ok, tc.want)
		}
	}
	for _, bad := range []string{"", "* * * *", "61 * * * *", "@daily", "0 0 30 2 *"} {
		if got, ok := domain.CronNext(bad, at("2026-11-01 00:00")); ok && bad != "0 0 30 2 *" {
			t.Errorf("CronNext(%q) = %s, want invalid", bad, got)
		}
	}
	if _, ok := domain.CronNext("0 0 30 2 *", at("2026-11-01 00:00")); ok {
		t.Error("February 30 never fires")
	}
}

func TestSyncBackoff(t *testing.T) {
	for failures, want := range map[int]time.Duration{0: 0, 1: 0, 2: 0, 3: 15 * time.Minute, 4: 30 * time.Minute, 5: time.Hour, 8: 8 * time.Hour, 9: 16 * time.Hour, 10: 24 * time.Hour, 50: 24 * time.Hour} {
		if got := domain.SyncBackoff(failures); got != want {
			t.Errorf("SyncBackoff(%d) = %s, want %s", failures, got, want)
		}
	}
}

// deltaExtractor answers ExtractSince from a fixed set and records the since it was given.
type deltaExtractor struct {
	rows  []domain.Record
	wm    string
	since []string
}

func (d *deltaExtractor) Extract(ctx context.Context, e string) ([]domain.Record, error) {
	r, _, err := d.ExtractSince(ctx, e, "")
	return r, err
}

func (d *deltaExtractor) ExtractSince(_ context.Context, _, since string) ([]domain.Record, string, error) {
	d.since = append(d.since, since)
	return d.rows, d.wm, nil
}

func custRow(key, name string) domain.Record {
	return domain.Record{Key: key, Fields: map[string]any{"AccountNum": key, "Name": name}}
}

func TestPipeline_DeltaRunPassesTheWatermarkAndSkipsAbsentDetection(t *testing.T) {
	ex := &deltaExtractor{rows: []domain.Record{custRow("C1", "One"), custRow("C2", "Two")}, wm: "2026-11-01T10:00:00.000000Z"}
	p, xref, _ := newPipeline(nil)
	p.Extractor = ex
	rep, err := p.Run(context.Background(), spec(t))
	if err != nil || rep.Watermark != ex.wm || rep.Created != 2 || ex.since[0] != "" {
		t.Fatalf("first run (no watermark = full read): %+v %v since=%v", rep, err, ex.since)
	}
	ex.rows = []domain.Record{custRow("C3", "Three")}
	s := spec(t)
	s.Since = ex.wm
	rep, err = p.Run(context.Background(), s)
	if err != nil || ex.since[1] != ex.wm || rep.Created != 1 {
		t.Fatalf("delta run: %+v %v since=%v", rep, err, ex.since)
	}
	if len(rep.Absent) != 0 {
		t.Fatalf("a delta run must not report every unseen key as absent: %v", rep.Absent)
	}
	if keys, _ := xref.Keys(context.Background(), system, "customer"); len(keys) != 3 {
		t.Fatalf("xref keys = %v", keys)
	}
}

func TestPipeline_PassageSourceOfTruthHoldsChangedRowsAsConflicts(t *testing.T) {
	ex := &deltaExtractor{rows: []domain.Record{custRow("C1", "One"), custRow("C2", "Two")}}
	p, xref, loader := newPipeline(nil)
	p.Extractor = ex
	if _, err := p.Run(context.Background(), spec(t)); err != nil {
		t.Fatal(err)
	}
	callsAfterLoad := loader.Calls
	// Legacy changed C1 (conflict), left C2, and added C3 (new keys still load: legacy has a record Passage lacks).
	ex.rows = []domain.Record{custRow("C1", "One (legacy edit)"), custRow("C2", "Two"), custRow("C3", "Three")}
	s := spec(t)
	s.HoldExisting = true
	rep, err := p.Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Conflicted != 1 || len(rep.Conflicts) != 1 || rep.Conflicts[0].SourceKey != "C1" || rep.Conflicts[0].Fields["name"] != "One (legacy edit)" || rep.Conflicts[0].TargetID == "" {
		t.Fatalf("conflicts = %+v", rep.Conflicts)
	}
	if rep.Created != 1 || rep.Updated != 0 || rep.Unchanged != 1 || rep.Status != domain.StatusPartial {
		t.Fatalf("created=%d updated=%d unchanged=%d status=%s", rep.Created, rep.Updated, rep.Unchanged, rep.Status)
	}
	if len(rep.Rejects) != 1 || rep.Rejects[0].Rejects[0].Code != domain.CodeSourceOfTruth {
		t.Fatalf("a held row is reported on the MIG-TARGET-DRIFT code: %+v", rep.Rejects)
	}
	if loader.Calls != callsAfterLoad+1 || loader.Rows[mustTarget(t, xref, "C1")].Fields["name"] != "One" {
		t.Fatal("Passage's record must not be overwritten")
	}
	// Keeping Passage acknowledges that legacy hash: the same row is quiet next time, a further change is reported again.
	s.Acknowledged = map[string]string{"C1": rep.Conflicts[0].ContentHash}
	rep, _ = p.Run(context.Background(), s)
	if rep.Conflicted != 0 || len(rep.Rejects) != 0 || rep.Status != domain.StatusSucceeded {
		t.Fatalf("acknowledged row reported again: %+v", rep)
	}
	ex.rows = []domain.Record{custRow("C1", "One (edited again)")}
	if rep, _ = p.Run(context.Background(), s); rep.Conflicted != 1 {
		t.Fatalf("a changed legacy row must be reported again: %+v", rep)
	}
	// With legacy as the source of truth the same change is applied.
	s.HoldExisting = false
	if rep, _ = p.Run(context.Background(), s); rep.Updated != 1 || rep.Conflicted != 0 {
		t.Fatalf("legacy source of truth must update: %+v", rep)
	}
}

func mustTarget(t *testing.T, x *domain.MemXref, key string) string {
	t.Helper()
	e, ok, _ := x.Get(context.Background(), domain.XrefKey{SourceSystem: system, Entity: "customer", SourceKey: key})
	if !ok {
		t.Fatalf("no xref for %s", key)
	}
	return e.TargetID
}

func TestDeltaSince(t *testing.T) {
	for _, tc := range []struct{ typ, wm, want string }{
		{domain.CursorTimestamp, "", ""},
		{domain.CursorTimestamp, "2026-11-01T10:00:00.003000000Z", "2026-11-01T09:55:00.003000000Z"},
		{domain.CursorTimestamp, "2026-11-01T10:00:00Z", "2026-11-01T09:55:00.000000000Z"},
		{domain.CursorTimestamp, "not a time", ""}, // a watermark of another cursor type reads everything
		{domain.CursorInt, "200", "199"},
		{domain.CursorInt, "-9223372036854775808", "-9223372036854775808"},
		{domain.CursorInt, "abc", ""},
		{domain.CursorRowversion, "00000000000007d1", "00000000000007d0"},
		{domain.CursorRowversion, "0000000000000100", "00000000000000ff"},
		{domain.CursorRowversion, "0000000000000000", "0000000000000000"},
		{domain.CursorRowversion, "zz", ""},
		{"", "W1", ""},
	} {
		if got := domain.DeltaSince(tc.typ, tc.wm); got != tc.want {
			t.Errorf("DeltaSince(%s, %q) = %q, want %q", tc.typ, tc.wm, got, tc.want)
		}
	}
}

// The extractors return rows strictly after since. A row that shares the watermark's timestamp but was committed
// after the last read, and a row a long transaction committed late with a slightly earlier timestamp, are both read
// by the next delta because it starts SyncLookback before the watermark.
func TestDeltaSince_LateRowsAtOrBeforeTheWatermarkAreReadAgain(t *testing.T) {
	strictlyAfter := func(rows map[string]string, since string) []string {
		var out []string
		for k, c := range rows {
			if since == "" || domain.CompareCursor(domain.CursorTimestamp, c, since) > 0 {
				out = append(out, k)
			}
		}
		return out
	}
	wm := "2026-11-01T10:00:00.000000000Z"
	rows := map[string]string{"C1": "2026-11-01T09:00:00.000000000Z", "C2": wm}
	if got := strictlyAfter(rows, ""); len(got) != 2 {
		t.Fatalf("first read = %v", got)
	}
	rows["C3"] = wm                               // same timestamp, committed after the read
	rows["C4"] = "2026-11-01T09:59:58.000000000Z" // earlier timestamp, committed late
	got := map[string]bool{}
	for _, k := range strictlyAfter(rows, domain.DeltaSince(domain.CursorTimestamp, wm)) {
		got[k] = true
	}
	if !got["C3"] || !got["C4"] || got["C1"] {
		t.Fatalf("delta after %s read %v; want C3 and C4 (and the re-read C2), not C1", wm, got)
	}
	if strictlyAfter(rows, wm) != nil && len(strictlyAfter(rows, wm)) != 0 {
		t.Fatal("control: a strict read from the watermark itself misses both")
	}
}
