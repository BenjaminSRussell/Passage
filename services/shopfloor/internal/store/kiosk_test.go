package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// FR-25.1 and the "Who is clocked in" inquiry (FR-25.14): a badge resolves to its
// active worker with the open clock-in; unknown and deactivated badges are both
// SF-0001; the board lists workers whose last punch is a clock-in, longest present
// first, with only the jobs started since the clock-in and still open.
func TestKiosk_BadgeLoginAndClockedInBoard(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(30)
	ctx := testkit.Ctx(tenantID)
	in := mustWorker(t, s, ctx, "IN-1", nil)
	out := mustWorker(t, s, ctx, "OUT-1", nil)
	early := mustWorker(t, s, ctx, "EARLY-1", nil)
	gone := mustWorker(t, s, ctx, "GONE-1", nil)

	sess, err := s.BadgeLogin(ctx, "IN-1")
	if err != nil || sess.WorkerID != in.ID || sess.Name != in.Name || sess.PartyID != in.PartyID || sess.ClockedInAt != nil {
		t.Fatalf("before any punch = %+v, %v", sess, err)
	}

	open, stopped, closed, before := ids.New(), ids.New(), ids.New(), ids.New()
	bundle := ids.New()
	half := decimal.RequireFromString("0.5")
	closedStop := day(8, 30)
	if _, err := s.CreateRegistrations(ctx, []Registration{
		{ID: ids.New(), WorkerID: in.ID, Kind: "job-start", JobID: &before, StartedAt: day(6, 0)},
		clock(in.ID, "clock-in", day(7, 0)),
		{ID: ids.New(), WorkerID: in.ID, Kind: "job-start", JobID: &open, BundleID: &bundle, StartedAt: day(7, 30)},
		{ID: ids.New(), WorkerID: in.ID, Kind: "job-start", JobID: &stopped, StartedAt: day(7, 30)},
		{ID: ids.New(), WorkerID: in.ID, Kind: "job-stop", JobID: &stopped, StartedAt: day(9, 0)},
		{ID: ids.New(), WorkerID: in.ID, Kind: "job-start", JobID: &closed, StartedAt: day(8, 0), StoppedAt: &closedStop, Hours: &half},
		clock(out.ID, "clock-in", day(7, 0)),
		clock(out.ID, "clock-out", day(12, 0)),
		clock(early.ID, "clock-in", day(6, 0)),
		clock(gone.ID, "clock-in", day(6, 30)),
	}); err != nil {
		t.Fatal(err)
	}
	inTenant(t, admin, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update worker set active = false where id = $1`, gone.ID)
		return err
	})

	sess, err = s.BadgeLogin(ctx, "IN-1")
	if err != nil || sess.ClockedInAt == nil || !sess.ClockedInAt.Equal(day(7, 0)) {
		t.Fatalf("clocked-in badge = %+v, %v", sess, err)
	}
	if sess, err := s.BadgeLogin(ctx, "OUT-1"); err != nil || sess.ClockedInAt != nil {
		t.Fatalf("clocked-out badge = %+v, %v", sess, err)
	}
	for _, badge := range []string{"GONE-1", "NO-SUCH-BADGE", "in-1"} {
		if _, err := s.BadgeLogin(ctx, badge); problemCode(err) != CodeBadgeUnknown {
			t.Fatalf("badge %s: %v, want %s", badge, err, CodeBadgeUnknown)
		}
	}

	board, err := s.ClockedIn(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Longest present first; the deactivated worker is still on the floor.
	if len(board) != 3 || board[0].WorkerID != early.ID || board[1].WorkerID != gone.ID || board[2].WorkerID != in.ID {
		t.Fatalf("board = %+v", board)
	}
	if board[1].Active || len(board[0].Jobs) != 0 {
		t.Fatalf("board rows = %+v", board[:2])
	}
	jobs := board[2].Jobs
	if len(jobs) != 1 || jobs[0].JobID != open || jobs[0].BundleID == nil || *jobs[0].BundleID != bundle || !jobs[0].StartedAt.Equal(day(7, 30)) {
		t.Fatalf("current jobs = %+v", jobs)
	}
	if first, err := s.ClockedIn(ctx, 1); err != nil || len(first) != 1 || first[0].WorkerID != early.ID {
		t.Fatalf("limit 1 = %+v, %v", first, err)
	}

	// Another tenant sees nobody and cannot resolve the badge (RLS).
	other := testkit.Ctx(testkit.TenantID(31))
	if board, err := s.ClockedIn(other, 10); err != nil || len(board) != 0 {
		t.Fatalf("other tenant board = %+v, %v", board, err)
	}
	if _, err := s.BadgeLogin(other, "IN-1"); problemCode(err) != CodeBadgeUnknown {
		t.Fatalf("other tenant badge: %v", err)
	}
}
