package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestRecordVersionHash_CanonicalAndSensitive(t *testing.T) {
	a := map[string]any{"lot": "L-100", "qty": "12.000", "status": "APPROVED"}
	b := map[string]any{"status": "APPROVED", "qty": "12.000", "lot": "L-100"}
	ha, err := RecordVersionHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := RecordVersionHash(b)
	if ha != hb {
		t.Fatalf("key order must not change the hash: %s vs %s", ha, hb)
	}
	if len(ha) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(ha))
	}
	a["qty"] = "13.000"
	hc, _ := RecordVersionHash(a)
	if hc == ha {
		t.Fatal("a changed field must change the hash")
	}
}

func TestVerifySigningHash_ChangedRecordFails(t *testing.T) {
	rec := map[string]string{"lot": "L-100", "status": "QA_HOLD"}
	h, _ := RecordVersionHash(rec)
	if err := VerifySigningHash(h, rec); err != nil {
		t.Fatalf("unchanged record must verify: %v", err)
	}
	if err := VerifySigningHash(strings.ToUpper(h), rec); err != nil {
		t.Fatalf("hex case must not matter: %v", err)
	}
	rec["status"] = "RELEASED"
	if got := codeOfErr(VerifySigningHash(h, rec)); got != CodeESignHashMismatch {
		t.Fatalf("want %s, got %q", CodeESignHashMismatch, got)
	}
}

func TestRequiredComponents(t *testing.T) {
	signed := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		last  *time.Time
		now   time.Time
		reuse int
		want  int
	}{
		{"first signing of a session", nil, signed, 15, SignComponentsFull},
		{"second within 15 min", &signed, signed.Add(14 * time.Minute), 15, SignComponentsReuse},
		{"exactly 15 min is still reuse", &signed, signed.Add(15 * time.Minute), 15, SignComponentsReuse},
		{"after 15 min both components", &signed, signed.Add(16 * time.Minute), 15, SignComponentsFull},
		{"reuse disabled", &signed, signed.Add(time.Minute), 0, SignComponentsFull},
		{"clock skew backwards is not reuse", &signed, signed.Add(-time.Minute), 15, SignComponentsFull},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiredComponents(tc.last, tc.now, tc.reuse); got != tc.want {
				t.Fatalf("want %d, got %d", tc.want, got)
			}
		})
	}
}

func TestLockout_ThreeFailuresLockThirtyMinutes(t *testing.T) {
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	var l Lockout
	for i := 0; i < 2; i++ {
		l.Fail(start, DefaultLockoutAttempts, DefaultLockoutMinutes)
		if err := l.Check(start); err != nil {
			t.Fatalf("locked after %d failures: %v", i+1, err)
		}
	}
	l.Fail(start, DefaultLockoutAttempts, DefaultLockoutMinutes)
	// AC-48.12: the fourth attempt within 30 minutes is refused with ESG-0603.
	if got := codeOfErr(l.Check(start.Add(10 * time.Minute))); got != CodeESignLocked {
		t.Fatalf("fourth attempt at +10 min: want %s, got %q", CodeESignLocked, got)
	}
	var de *Error
	if err := l.Check(start); !errors.As(err, &de) || !strings.Contains(de.Detail, "30 min") {
		t.Fatalf("right after the lock the message names 30 min: %v", err)
	}
	if err := l.Check(start.Add(30 * time.Minute)); err != nil {
		t.Fatalf("lock must expire at 30 min: %v", err)
	}
}

func TestLockout_SucceedClearsFailures(t *testing.T) {
	start := time.Now().UTC()
	var l Lockout
	l.Fail(start, 3, 30)
	l.Fail(start, 3, 30)
	if err := l.Succeed(start); err != nil {
		t.Fatal(err)
	}
	l.Fail(start, 3, 30)
	if err := l.Check(start); err != nil {
		t.Fatalf("a success must reset the count; one new failure must not lock: %v", err)
	}
}

func TestValidMeaning(t *testing.T) {
	for _, m := range []Meaning{MeaningAuthored, MeaningReviewed, MeaningApproved, MeaningReleased, MeaningVerified} {
		if !ValidMeaning(m) {
			t.Fatalf("%s should be valid", m)
		}
	}
	if ValidMeaning("DELETED") {
		t.Fatal("unknown meaning must be rejected")
	}
}

func TestLockout_PropertyNeverUnlocksEarly(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
		attempts := rapid.IntRange(1, 10).Draw(t, "attempts")
		lockMin := rapid.IntRange(5, 1440).Draw(t, "lockMin")
		var l Lockout
		for i := 0; i < attempts; i++ {
			l.Fail(start, DefaultLockoutAttempts, lockMin)
		}
		offset := rapid.IntRange(0, lockMin*2).Draw(t, "offsetMin")
		at := start.Add(time.Duration(offset) * time.Minute)
		locked := l.Check(at) != nil
		shouldLock := attempts >= DefaultLockoutAttempts && offset < lockMin
		if locked != shouldLock {
			t.Fatalf("attempts=%d lock=%dm offset=%dm: locked=%v want %v", attempts, lockMin, offset, locked, shouldLock)
		}
	})
}
