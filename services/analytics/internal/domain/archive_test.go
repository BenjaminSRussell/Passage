package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReadyToMove_ResidenceBoundary(t *testing.T) {
	cases := []struct {
		name   string
		period string
		months int
		now    string
		want   bool
	}{
		{"one day before boundary", "2024-01", 24, "2026-01-31T23:59:59Z", false},
		{"boundary exactly", "2024-01", 24, "2026-02-01T00:00:00Z", true},
		{"well after", "2024-01", 24, "2030-01-01T00:00:00Z", true},
		{"short residence", "2026-01", 6, "2026-07-31T00:00:00Z", false},
		{"short residence boundary", "2026-01", 6, "2026-08-01T00:00:00Z", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadyToMove(c.period, c.months, mustTime(t, c.now))
			if err != nil || got != c.want {
				t.Fatalf("ReadyToMove = %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

func TestCheckMovable_Exclusions(t *testing.T) {
	now := mustTime(t, "2030-01-01T00:00:00Z")
	base, err := NewArchivePartition("ledger", "journal_line", "2024-01")
	if err != nil {
		t.Fatal(err)
	}
	hold := base
	hold.LegalHold = true
	open := base
	open.OpenPeriod = true
	young := base
	young.Period = "2029-12"
	moved := base
	moved.Status = ArchiveVerified

	cases := []struct {
		name string
		p    ArchivePartition
		want error
	}{
		{"movable", base, nil},
		{"legal hold blocks", hold, ErrLegalHold},
		{"open period blocks", open, ErrOpenPeriod},
		{"not aged", young, ErrNotAged},
		{"already moved", moved, ErrArchiveState},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.CheckMovable(now, DefaultResidenceMonths); !errors.Is(got, c.want) {
				t.Fatalf("CheckMovable = %v; want %v", got, c.want)
			}
		})
	}
}

func TestVerify_BR46_14(t *testing.T) {
	now := mustTime(t, "2030-01-01T00:00:00Z")
	newP := func() ArchivePartition {
		p, _ := NewArchivePartition("ledger", "journal_line", "2024-01")
		return p
	}
	t.Run("match verifies", func(t *testing.T) {
		p := newP()
		if err := p.Verify(1000, 1000, "abc", "abc", now); err != nil {
			t.Fatal(err)
		}
		if p.Status != ArchiveVerified || p.MovedAt == nil || !p.MovedAt.Equal(now) {
			t.Fatalf("status = %s moved %v", p.Status, p.MovedAt)
		}
		if err := p.Verify(1000, 1000, "abc", "abc", now); err == nil || err.Code != CodeStateConflict {
			t.Fatalf("second verify = %v, want %s", err, CodeStateConflict)
		}
		if p.RowsOLTP != 1000 {
			t.Fatalf("a refused verify must not touch the counts: %+v", p)
		}
	})
	t.Run("row mismatch keeps partition with ANA-0110", func(t *testing.T) {
		p := newP()
		err := p.Verify(1000, 999, "abc", "abc", now)
		if err == nil || err.Code != CodeArchiveVerify {
			t.Fatalf("err = %v", err)
		}
		if p.Status != ArchivePending {
			t.Fatalf("status = %s, want PENDING", p.Status)
		}
	})
	t.Run("hash mismatch keeps partition", func(t *testing.T) {
		p := newP()
		err := p.Verify(10, 10, "abc", "abd", now)
		if err == nil || err.Code != CodeArchiveVerify || !strings.Contains(err.Detail, "content hash differs") {
			t.Fatalf("hash mismatch = %v", err)
		}
		if p.Status != ArchivePending {
			t.Fatalf("status = %s", p.Status)
		}
	})
	t.Run("empty hash never verifies", func(t *testing.T) {
		p := newP()
		if err := p.Verify(0, 0, "", "", now); err == nil {
			t.Fatal("empty hashes must not verify")
		}
	})
}

func TestDropRestoreLifecycle(t *testing.T) {
	p, _ := NewArchivePartition("audit", "audit_event", "2024-03")
	if err := p.Drop(); !errors.Is(err, ErrArchiveState) {
		t.Fatalf("drop from PENDING = %v", err)
	}
	if err := p.Verify(5, 5, "h", "h", mustTime(t, "2030-01-01T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	p.LegalHold = true
	if err := p.Drop(); !errors.Is(err, ErrLegalHold) {
		t.Fatalf("drop under hold = %v", err)
	}
	p.LegalHold = false
	if err := p.Drop(); err != nil {
		t.Fatal(err)
	}
	now := mustTime(t, "2031-01-01T00:00:00Z")
	if err := p.Restore(now, 0); !errors.Is(err, ErrRestoreTTL) {
		t.Fatalf("ttl 0 = %v, want ErrRestoreTTL", err)
	}
	if err := p.Restore(now, DefaultRestoreTTLDays); err != nil {
		t.Fatal(err)
	}
	if p.RestoreExpired(now.AddDate(0, 0, 29)) {
		t.Fatal("restore expired too early")
	}
	if !p.RestoreExpired(now.AddDate(0, 0, 30)) {
		t.Fatal("restore must expire at 30 days")
	}
	if err := p.Restore(now.AddDate(0, 0, 1), DefaultRestoreTTLDays); !errors.Is(err, ErrArchiveState) {
		t.Fatalf("restoring a live restore = %v, want ErrArchiveState", err)
	}
	// After the lifetime the partition can be restored again, or expired back to DROPPED.
	again := p
	if err := again.Restore(now.AddDate(0, 0, 31), 5); err != nil || !again.RestoreUntil.Equal(now.AddDate(0, 0, 36)) {
		t.Fatalf("re-restore after expiry = %v until %v", err, again.RestoreUntil)
	}
	if !p.ExpireRestore(now.AddDate(0, 0, 30)) || p.Status != ArchiveDropped || p.RestoreUntil != nil {
		t.Fatalf("expire = %+v", p)
	}
	if p.ExpireRestore(now.AddDate(0, 0, 30)) {
		t.Fatal("a DROPPED partition has nothing to expire")
	}
}

func TestNewArchivePartition_Validation(t *testing.T) {
	if _, err := NewArchivePartition("ledger", "journal_line", "2024-13"); err == nil {
		t.Fatal("month 13 must be rejected")
	}
	if _, err := NewArchivePartition("", "journal_line", "2024-01"); err == nil {
		t.Fatal("empty domain must be rejected")
	}
	if _, err := NewArchivePartition("ledger", "Journal Line", "2024-01"); err == nil || err.Fields["tableName"] == "" {
		t.Fatalf("a table name outside the naming rule must be rejected: %v", err)
	}
}

func TestValidateResidence(t *testing.T) {
	cases := []struct {
		months, statutory int
		ok                bool
	}{
		{24, 0, true},
		{5, 0, false},
		{121, 0, false},
		{24, 36, false},
		{36, 36, true},
	}
	for _, c := range cases {
		if got := ValidateResidence(c.months, c.statutory) == nil; got != c.ok {
			t.Errorf("ValidateResidence(%d,%d) ok=%v want %v", c.months, c.statutory, got, c.ok)
		}
	}
}

func TestContentHash_OrderIndependentAndCountsDuplicates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rows := rapid.SliceOfN(rapid.String(), 0, 50).Draw(t, "rows")
		rev := make([]string, len(rows))
		for i := range rows {
			rev[len(rows)-1-i] = rows[i]
		}
		if ContentHash(rows) != ContentHash(rev) {
			t.Fatal("hash depends on row order")
		}
		if len(rows) > 0 {
			dup := append(append([]string{}, rows...), rows[0])
			if ContentHash(dup) == ContentHash(rows) {
				t.Fatal("hash ignores a duplicated row")
			}
		}
	})
}
