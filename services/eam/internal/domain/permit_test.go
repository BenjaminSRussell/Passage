package domain

import (
	"errors"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestCanPermitTransition_TableDriven(t *testing.T) {
	cases := []struct {
		from, to PermitStatus
		want     bool
	}{
		{PermitRequested, PermitApproved, true},
		{PermitRequested, PermitCanceled, true},
		{PermitRequested, PermitIssued, false},
		{PermitApproved, PermitIssued, true},
		{PermitApproved, PermitClosed, false},
		{PermitIssued, PermitSuspended, true},
		{PermitIssued, PermitClosed, true},
		{PermitSuspended, PermitIssued, true},
		{PermitSuspended, PermitClosed, true},
		{PermitClosed, PermitIssued, false},
		{PermitCanceled, PermitApproved, false},
	}
	for _, c := range cases {
		if got := CanPermitTransition(c.from, c.to); got != c.want {
			t.Errorf("%s -> %s: got %v want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestCheckApprover_TableDriven(t *testing.T) {
	cases := []struct {
		requester, approver string
		code                string
	}{
		{"u1", "u1", CodeSelfApproval},
		{"u1", "u2", ""},
	}
	for _, c := range cases {
		err := CheckApprover(c.requester, c.approver)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s/%s: unexpected %v", c.requester, c.approver, err)
			}
			continue
		}
		var ge GateError
		if !errors.As(err, &ge) || ge.Code != c.code {
			t.Errorf("%s/%s: got %v want %s", c.requester, c.approver, err, c.code)
		}
	}
}

func TestValidPermitWindow_TableDriven(t *testing.T) {
	base := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		to   time.Time
		max  int
		ok   bool
	}{
		{"within limit", base.Add(12 * time.Hour), 12, true},
		{"exactly limit", base.Add(12 * time.Hour), 12, true},
		{"over limit", base.Add(13 * time.Hour), 12, false},
		{"empty window", base, 12, false},
		{"inverted", base.Add(-time.Hour), 12, false},
	}
	for _, c := range cases {
		err := ValidPermitWindow(base, c.to, c.max)
		if (err == nil) != c.ok {
			t.Errorf("%s: got %v want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestCheckPermitGate_TableDriven(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)
	iso := "ELECTRICAL_ISOLATION"
	cases := []struct {
		name string
		req  PermitRequirement
		code string
	}{
		{"no permit", PermitRequirement{TypeCode: "HOT_WORK"}, CodePermitMissing},
		{"open locks reported before status", PermitRequirement{TypeCode: iso, RequiresIsolation: true, Found: true,
			Status: PermitApproved, ValidFrom: from, ValidTo: to, Points: 3, UnlockedPoints: 1}, CodeIsolationOpen},
		{"isolation permit on an asset without isolation points fails closed", PermitRequirement{TypeCode: iso,
			RequiresIsolation: true, Found: true, Status: PermitIssued, ValidFrom: from, ValidTo: to}, CodeIsolationOpen},
		{"approved but not issued", PermitRequirement{TypeCode: "HOT_WORK", Found: true,
			Status: PermitApproved, ValidFrom: from, ValidTo: to}, CodePermitMissing},
		{"issued but expired", PermitRequirement{TypeCode: "HOT_WORK", Found: true,
			Status: PermitIssued, ValidFrom: from.Add(-2 * time.Hour), ValidTo: from}, CodePermitMissing},
		{"issued and valid", PermitRequirement{TypeCode: "HOT_WORK", Found: true,
			Status: PermitIssued, ValidFrom: from, ValidTo: to}, ""},
		{"isolation complete and issued", PermitRequirement{TypeCode: iso, RequiresIsolation: true, Found: true,
			Status: PermitIssued, ValidFrom: from, ValidTo: to, Points: 3}, ""},
		{"not yet valid", PermitRequirement{TypeCode: "HOT_WORK", Found: true,
			Status: PermitIssued, ValidFrom: to, ValidTo: to.Add(time.Hour)}, CodePermitMissing},
	}
	for _, c := range cases {
		err := CheckPermitGate(c.req, now)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: unexpected %v", c.name, err)
			}
			continue
		}
		var ge GateError
		if !errors.As(err, &ge) || ge.Code != c.code {
			t.Errorf("%s: got %v want %s", c.name, err, c.code)
		}
	}
}

func TestNextLockState_TableDriven(t *testing.T) {
	cases := []struct {
		cur    LockState
		action LockAction
		want   LockState
		ok     bool
	}{
		{"", LockApply, LockApplied, true},
		{"", LockVerify, "", false},
		{LockApplied, LockApply, "", false},
		{LockApplied, LockVerify, LockVerified, true},
		{LockVerified, LockVerify, LockVerified, true},
		{LockApplied, LockRemove, LockRemoved, true},
		{LockVerified, LockRemove, LockRemoved, true},
		{LockRemoved, LockRemove, LockRemoved, true},
		{LockRemoved, LockVerify, LockRemoved, false},
	}
	for _, c := range cases {
		got, err := NextLockState(c.cur, c.action)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("%q %s: got %q err=%v want %q ok=%v", c.cur, c.action, got, err, c.want, c.ok)
		}
	}
}

func TestLocksOpen_TableDriven(t *testing.T) {
	if err := LocksOpen(0); err != nil {
		t.Errorf("no locks: %v", err)
	}
	var ge GateError
	if err := LocksOpen(1); !errors.As(err, &ge) || ge.Code != CodeLocksOpen {
		t.Errorf("one lock: got %v", err)
	}
}

func TestCheckIssuer_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		holders []string
		ok      bool
	}{
		{"issuer is the only holder", []string{"iss", "iss"}, false},
		{"another technician also holds a lock", []string{"iss", "tech-1"}, true},
		{"issuer holds no lock", []string{"tech-1", "tech-2"}, true},
		{"no locks (non-isolation permit)", nil, true},
	}
	for _, c := range cases {
		err := CheckIssuer("iss", c.holders)
		var ge GateError
		if c.ok && err != nil || !c.ok && (!errors.As(err, &ge) || ge.Code != CodeSelfApproval) {
			t.Errorf("%s: got %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		status PermitStatus
		to     time.Time
		want   bool
	}{
		{PermitIssued, now, true}, // valid_to is exclusive
		{PermitIssued, now.Add(-time.Minute), true},
		{PermitIssued, now.Add(time.Minute), false},
		{PermitApproved, now.Add(-time.Hour), false}, // only issued permits auto-suspend
		{PermitSuspended, now.Add(-time.Hour), false},
	}
	for _, c := range cases {
		if got := Expired(c.status, c.to, now); got != c.want {
			t.Errorf("%s until %v: got %v want %v", c.status, c.to, got, c.want)
		}
	}
}

// Property: a verified lock never reopens a point. Once a lock is REMOVED it stays
// REMOVED under any further action, and VERIFY never moves a lock backwards.
func TestNextLockState_Property(t *testing.T) {
	states := []LockState{"", LockApplied, LockVerified, LockRemoved}
	actions := []LockAction{LockApply, LockVerify, LockRemove}
	rapid.Check(t, func(rt *rapid.T) {
		cur := rapid.SampledFrom(states).Draw(rt, "state")
		act := rapid.SampledFrom(actions).Draw(rt, "action")
		next, err := NextLockState(cur, act)
		if err != nil {
			if next != cur {
				rt.Fatalf("error must leave state unchanged: %q -> %q", cur, next)
			}
			return
		}
		if cur == LockRemoved && next != LockRemoved {
			rt.Fatalf("removed lock reopened by %s", act)
		}
		if act == LockVerify && cur == LockApplied && next != LockVerified {
			rt.Fatalf("verify did not verify an applied lock")
		}
	})
}

// Property: the gate never passes without an issued, currently valid permit.
func TestCheckPermitGate_Property(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	rapid.Check(t, func(rt *rapid.T) {
		req := PermitRequirement{
			TypeCode:          "HOT_WORK",
			RequiresIsolation: rapid.Bool().Draw(rt, "iso"),
			Found:             rapid.Bool().Draw(rt, "found"),
			Status:            rapid.SampledFrom([]PermitStatus{PermitRequested, PermitApproved, PermitIssued, PermitSuspended}).Draw(rt, "status"),
			ValidFrom:         now.Add(time.Duration(rapid.IntRange(-48, 48).Draw(rt, "from")) * time.Hour),
			Points:            rapid.IntRange(0, 3).Draw(rt, "points"),
		}
		req.UnlockedPoints = rapid.IntRange(0, req.Points).Draw(rt, "unlocked")
		req.ValidTo = req.ValidFrom.Add(time.Duration(rapid.IntRange(1, 24).Draw(rt, "span")) * time.Hour)
		if CheckPermitGate(req, now) == nil {
			if !req.Found || req.Status != PermitIssued || !PermitValidAt(req.ValidFrom, req.ValidTo, now) {
				rt.Fatalf("gate passed without an issued valid permit: %+v", req)
			}
			if req.RequiresIsolation && (req.Points == 0 || req.UnlockedPoints > 0) {
				rt.Fatalf("gate passed with open isolation points: %+v", req)
			}
		}
	})
}
