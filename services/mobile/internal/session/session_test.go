package session

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

func view(id, work string, seen time.Time) View {
	return View{ID: id, DeviceID: "d-" + id, UserID: "u-" + id, CurrentWorkID: work, LastSeenAt: seen, Status: Active}
}

func TestStateAtBoundary(t *testing.T) {
	idle := Idle(30)
	s := view("a", "w1", t0)
	if got := StateAt(s, t0.Add(30*time.Minute), idle); got != Active {
		t.Errorf("exactly at the window: %s", got)
	}
	if got := StateAt(s, t0.Add(31*time.Minute), idle); got != Stale {
		t.Errorf("31 minutes: %s", got)
	}
	s.Status = Ended
	if got := StateAt(s, t0, idle); got != Ended {
		t.Errorf("ended session: %s", got)
	}
}

func TestAC328StaleSessionWorkIsAssignable(t *testing.T) {
	idle := Idle(30)
	sessions := []View{view("a", "w1", t0)}
	if Assignable(WorkAssigned, "w1", sessions, t0.Add(10*time.Minute), idle) {
		t.Error("work held by an active session must not be assignable")
	}
	if !Assignable(WorkAssigned, "w1", sessions, t0.Add(31*time.Minute), idle) {
		t.Error("work held by a session silent for 31 minutes must be assignable")
	}
	if !Assignable(WorkOpen, "w2", sessions, t0, idle) {
		t.Error("open work with no holder is assignable")
	}
	for _, status := range []WorkStatus{"COMPLETED", "CANCELLED", WorkInProgress} {
		if Assignable(status, "w9", nil, t0, idle) {
			t.Errorf("%s work must not be assignable", status)
		}
	}
}

func TestResumeRelocksOnlyFreeWork(t *testing.T) {
	idle := Idle(30)
	now := t0.Add(40 * time.Minute)
	stale := view("a", "w1", t0)
	if ok, why := Resumed(stale, "w1", WorkAssigned, []View{stale}, now, idle); !ok {
		t.Errorf("free work must re-lock: %s", why)
	}
	taker := view("b", "w1", now.Add(-time.Minute))
	if ok, _ := Resumed(stale, "w1", WorkAssigned, []View{stale, taker}, now, idle); ok {
		t.Error("work taken by another active session must not re-lock")
	}
	if ok, why := Resumed(stale, "w1", "COMPLETED", []View{stale}, now, idle); ok || why != "work is COMPLETED" {
		t.Errorf("completed work: %v %s", ok, why)
	}
	if ok, _ := Resumed(stale, "", WorkOpen, nil, now, idle); ok {
		t.Error("no work held")
	}
}

func TestIdleClamps(t *testing.T) {
	for in, want := range map[int]time.Duration{0: 30 * time.Minute, 4: 30 * time.Minute, 5: 5 * time.Minute, 240: 240 * time.Minute, 241: 30 * time.Minute} {
		if got := Idle(in); got != want {
			t.Errorf("Idle(%d)=%s want %s", in, got, want)
		}
	}
}

func TestSkewBlocksBeyondFiveMinutes(t *testing.T) {
	if SkewBlocked(Skew(t0, t0.Add(-300*time.Second))) {
		t.Error("300 s is allowed")
	}
	if !SkewBlocked(Skew(t0, t0.Add(-301*time.Second))) || !SkewBlocked(Skew(t0, t0.Add(301*time.Second))) {
		t.Error("beyond 300 s either way must block")
	}
}
