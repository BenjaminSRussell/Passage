package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// BR-25.1 and BR-25.7 through the database: a bundle's closed jobs are stored
// with their shares and the recorded clock offset, read back by bundle, a resend
// is a no-op, and a later batch adding a closed job to the bundle is SF-0113.
func TestBundle_StoredOnceWithSharesAndClockOffset(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(21))
	worker := mustWorker(t, s, ctx, "B-21", nil).ID
	bundle := ids.New()
	start, stop := day(9, 0), day(11, 0)
	offset := int64(420)
	job := func(qty, hours string) Registration {
		j := ids.New()
		h := decimal.RequireFromString(hours)
		return Registration{ID: ids.New(), WorkerID: worker, Kind: "job-stop", JobID: &j, BundleID: &bundle, StartedAt: start,
			StoppedAt: &stop, Hours: &h, QtyGood: decimal.RequireFromString(qty), Offline: true, ClockOffsetSeconds: &offset}
	}
	first, second := job("30", "1.50"), job("10", "0.50")
	if _, err := s.CreateRegistrations(ctx, []Registration{first, second}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListBundleRegistrations(ctx, bundle, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("bundle rows = %+v", got)
	}
	sum := decimal.Zero
	for _, r := range got {
		if r.ClockOffsetSeconds == nil || *r.ClockOffsetSeconds != offset || !r.Offline {
			t.Fatalf("row %+v lost its clock offset", r)
		}
		sum = sum.Add(*r.Hours)
	}
	if sum.StringFixed(2) != "2.00" {
		t.Fatalf("bundle hours sum to %s, want the 2.00 worked once", sum)
	}

	// A resend of part of the batch is a replay, not a second bundle.
	replay, err := s.CreateRegistrations(ctx, []Registration{second})
	if err != nil || replay[0].Status != StatusDuplicate {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	// A new closed job on the same bundle would count the bundle's time again.
	if _, err := s.CreateRegistrations(ctx, []Registration{job("5", "2.00")}); problemCode(err) != domain.CodeBundleInvalid {
		t.Fatalf("late bundle member: err = %v, want %s", err, domain.CodeBundleInvalid)
	}
	// An open job registration on the bundle carries no hours and is accepted.
	open := job("0", "0")
	open.StoppedAt, open.Hours = nil, nil
	if _, err := s.CreateRegistrations(ctx, []Registration{open}); err != nil {
		t.Fatalf("open bundle member: %v", err)
	}

	other, err := s.ListBundleRegistrations(testkit.Ctx(testkit.TenantID(22)), bundle, 100)
	if err != nil || len(other) != 0 {
		t.Fatalf("another tenant read the bundle: %+v %v", other, err)
	}
}
