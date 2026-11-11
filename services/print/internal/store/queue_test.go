package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
	"github.com/BenjaminSRussell/passage/services/print/migrations"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

func TestPrinterQueue_EditRetireAndFreeRank(t *testing.T) {
	s, _ := migrated(t)
	ctx := tenantCtx(1)
	d1, _, err := s.CreatePrinter(ctx, dock("DOCK-1", 1), "q-1")
	if err != nil {
		t.Fatal(err)
	}
	d2, _, err := s.CreatePrinter(ctx, dock("DOCK-2", 2), "q-2")
	if err != nil {
		t.Fatal(err)
	}

	moved := d1
	moved.Name, moved.QueueCode, moved.FailoverRank = "Moved", "OFFICE", 1
	got, err := s.UpdatePrinter(ctx, moved)
	if err != nil || got.QueueCode != "OFFICE" || got.Name != "Moved" || got.Code != "DOCK-1" {
		t.Fatalf("move: %+v %v", got, err)
	}
	clash := d2
	clash.QueueCode, clash.FailoverRank = "OFFICE", 1
	if _, err := s.UpdatePrinter(ctx, clash); problemCode(err) != domain.CodeRankTaken {
		t.Fatalf("rank clash: %v", err)
	}
	if _, err := s.UpdatePrinter(tenantCtx(2), moved); problemCode(err) != domain.CodePrinterNotFound {
		t.Fatalf("another tenant edited it: %v", err)
	}

	retired, err := s.RetirePrinter(ctx, d1.ID)
	if err != nil || retired.RetiredAt == nil {
		t.Fatalf("retire: %+v %v", retired, err)
	}
	if _, err := s.RetirePrinter(ctx, d1.ID); problemCode(err) != domain.CodePrinterNotFound {
		t.Fatalf("second retire: %v", err)
	}
	if _, err := s.UpdatePrinter(ctx, moved); problemCode(err) != domain.CodePrinterNotFound {
		t.Fatalf("edit retired: %v", err)
	}
	active, _ := s.ListPrinters(ctx, false)
	all, _ := s.ListPrinters(ctx, true)
	if len(active) != 1 || len(all) != 2 {
		t.Fatalf("active %d all %d, want 1 and 2", len(active), len(all))
	}
	if q, _ := s.QueueDevices(ctx, "OFFICE"); len(q) != 0 {
		t.Fatalf("retired printer still in its queue: %+v", q)
	}
	st, _ := s.PrinterStatuses(ctx)
	if len(st) != 1 || st[0].Code != "DOCK-2" {
		t.Fatalf("status lists retired printers: %+v", st)
	}
	// Its rank is free again: a new printer can take OFFICE rank 1.
	if _, _, err := s.CreatePrinter(ctx, domain.Printer{Code: "OFF-1", Name: "Office", DeviceType: domain.DeviceZPL, Dpi: 203, MediaSize: domain.Media4x6In, Host: "10.0.4.30", Port: 9100, QueueCode: "OFFICE", FailoverRank: 1}, "q-3"); err != nil {
		t.Fatalf("reuse rank: %v", err)
	}
}

func TestRoutingRule_UpdateDelete(t *testing.T) {
	s, _ := migrated(t)
	ctx := tenantCtx(1)
	r, _, err := s.CreateRoutingRule(ctx, domain.RoutingRule{DocumentType: "PICK_LIST", QueueCode: "DOCK", Sequence: 1}, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	r.QueueCode, r.Zone, r.Sequence = "OFFICE", "A", 3
	got, err := s.UpdateRoutingRule(ctx, r)
	if err != nil || got.QueueCode != "OFFICE" || got.Zone != "A" || got.Sequence != 3 {
		t.Fatalf("update: %+v %v", got, err)
	}
	if _, err := s.UpdateRoutingRule(tenantCtx(2), r); problemCode(err) != domain.CodeRuleNotFound {
		t.Fatalf("another tenant updated it: %v", err)
	}
	if err := s.DeleteRoutingRule(tenantCtx(2), r.ID); problemCode(err) != domain.CodeRuleNotFound {
		t.Fatalf("another tenant deleted it: %v", err)
	}
	if err := s.DeleteRoutingRule(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoutingRule(ctx, r.ID); problemCode(err) != domain.CodeRuleNotFound {
		t.Fatalf("second delete: %v", err)
	}
}

func TestClaimStaleJobs_ClaimsOnceAndWorksAsAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenantCtx(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	old := queuedJob(t, s, ctx, "stale-a")
	queuedJob(t, s, ctx, "stale-b")
	cutoff := time.Now().Add(time.Minute) // both jobs are older than this
	got, err := s.ClaimStaleJobs(ctx, cutoff, 1)
	if err != nil || len(got) != 1 || got[0].ZPL != "^XA^XZ" || got[0].Status != domain.JobQueued {
		t.Fatalf("claim: %+v %v", got, err)
	}
	if got[0].ID != old.ID {
		t.Fatalf("claimed %s, want the oldest %s", got[0].ID, old.ID)
	}
	// Claiming stamps updated_at, so the same cutoff in the past skips it; the other one is still due.
	again, err := s.ClaimStaleJobs(ctx, time.Now().Add(-time.Second), 5)
	if err != nil || len(again) != 0 {
		t.Fatalf("a claimed job was claimed again: %+v %v", again, err)
	}
	rest, err := s.ClaimStaleJobs(ctx, cutoff, 5)
	if err != nil || len(rest) != 2 {
		t.Fatalf("claim with a later cutoff = %d jobs, %v; want both", len(rest), err)
	}
	if other, _ := s.ClaimStaleJobs(tenantCtx(2), cutoff, 5); len(other) != 0 {
		t.Fatalf("tenant 2 claimed tenant 1's jobs: %+v", other)
	}
	_ = ids.New
}
