package store_test

import (
	"testing"
	"time"
)

// BR-24.14: the third failed re-authentication in a row locks signing for 15
// minutes, the count is per tenant and signer, and a success clears it.
func TestSignerLockout_CountsPerSigner(t *testing.T) {
	s, _ := setup(t)
	ctx := asUser("qa-1")
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= 2; i++ {
		l, err := s.RecordReauthResult(ctx, "qa-1", true, now)
		if err != nil || l.Failures != i || l.Locked(now) {
			t.Fatalf("failure %d: %+v err=%v", i, l, err)
		}
	}
	if l, err := s.RecordReauthResult(ctx, "qa-1", false, now); err != nil || l.Failures != 0 {
		t.Fatalf("success: %+v err=%v", l, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.RecordReauthResult(ctx, "qa-1", true, now); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.SignerLockout(ctx, "qa-1")
	if err != nil || l.CheckAttempt(now.Add(14*time.Minute)) == nil || l.CheckAttempt(now.Add(15*time.Minute)) != nil {
		t.Fatalf("lock %+v err=%v", l, err)
	}
	if other, err := s.SignerLockout(ctxFor(2, ""), "qa-1"); err != nil || other.Failures != 0 || !other.LockedUntil.IsZero() {
		t.Fatalf("another tenant sees the lock: %+v err=%v", other, err)
	}
	if fresh, err := s.SignerLockout(ctx, "qa-2"); err != nil || fresh.Locked(now) {
		t.Fatalf("another signer is locked: %+v err=%v", fresh, err)
	}
}
