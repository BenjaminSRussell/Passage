package domain

import (
	"errors"
	"testing"
	"time"
)

// TestTender_AC343 is AC-34.3: carrier 1 ignores (times out), carrier 2 declines,
// carrier 3 receives the tender and accepts. Ten-minute timeouts on a test clock.
func TestTender_AC343(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	tender, err := NewTender("load-1", []string{"car-1", "car-2", "car-3"}, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tender.Send(t0)
	tender.Tick(t0.Add(10 * time.Minute))
	if len(tender.Attempts) != 2 || tender.Attempts[1].CarrierID != "car-2" {
		t.Fatalf("after timeout attempts = %+v", tender.Attempts)
	}
	if err := tender.Respond("car-2", false, t0.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(tender.Attempts) != 3 || tender.Attempts[2].CarrierID != "car-3" {
		t.Fatalf("after decline attempts = %+v", tender.Attempts)
	}
	if err := tender.Respond("car-3", true, t0.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if tender.Status != TenderAccepted || tender.AcceptedCarrier != "car-3" {
		t.Fatalf("status = %s carrier = %s", tender.Status, tender.AcceptedCarrier)
	}
	if tender.Attempts[0].Response != ResponseExpired || tender.Attempts[1].Response != ResponseDeclined {
		t.Fatalf("earlier responses = %+v", tender.Attempts)
	}
}

func TestTender_LateAcceptanceRejected(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	tender, _ := NewTender("load-1", []string{"car-1", "car-2"}, 10*time.Minute)
	tender.Send(t0)
	err := tender.Respond("car-1", true, t0.Add(11*time.Minute))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeTenderExpired {
		t.Fatalf("late acceptance error = %v, want TMS-0105", err)
	}
	if tender.Status != TenderPending || tender.AcceptedCarrier != "" {
		t.Fatalf("late acceptance changed the tender: %+v", tender)
	}
}

func TestTender_ExhaustedFailsWithPlannerTask(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	tender, _ := NewTender("load-1", []string{"car-1", "car-2"}, 10*time.Minute)
	tender.Send(t0)
	tender.Tick(t0.Add(time.Hour))
	if tender.Status != TenderFailed || !tender.PlannerTask {
		t.Fatalf("status = %s planner = %v", tender.Status, tender.PlannerTask)
	}
	if len(tender.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(tender.Attempts))
	}
}

func TestTender_LateTickWalksEveryDeadline(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	tender, _ := NewTender("load-1", []string{"car-1", "car-2", "car-3"}, 10*time.Minute)
	tender.Send(t0)
	tender.Tick(t0.Add(25 * time.Minute))
	if len(tender.Attempts) != 3 || tender.Attempts[2].SentAt != t0.Add(20*time.Minute) {
		t.Fatalf("attempts = %+v", tender.Attempts)
	}
}

func TestNewTender_Validation(t *testing.T) {
	if _, err := NewTender("load-1", nil, time.Minute); err == nil {
		t.Fatal("empty route guide must be rejected")
	}
	if _, err := NewTender("load-1", []string{"car-1"}, 0); err == nil {
		t.Fatal("zero timeout must be rejected")
	}
}

// TestTender_TickAtReportsEachExpiryOnce: a late sweep expires every missed
// deadline in order, and a second sweep reports nothing new.
func TestTender_TickAtReportsEachExpiryOnce(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	tender, _ := NewTender("load-1", []string{"car-1", "car-2"}, 10*time.Minute)
	tender.Send(t0)
	if tender.Overdue(t0.Add(9*time.Minute)) || !tender.Overdue(t0.Add(10*time.Minute)) {
		t.Fatal("Overdue must turn true at the deadline")
	}
	expired := tender.TickAt(t0.Add(time.Hour))
	if len(expired) != 2 || expired[0].CarrierID != "car-1" || expired[1].CarrierID != "car-2" {
		t.Fatalf("expired = %+v, want car-1 then car-2", expired)
	}
	if tender.Status != TenderFailed || !tender.PlannerTask || tender.Overdue(t0.Add(time.Hour)) {
		t.Fatalf("tender = %+v, want TENDER_FAILED with a planner task", tender)
	}
	if again := tender.TickAt(t0.Add(2 * time.Hour)); len(again) != 0 {
		t.Fatalf("second sweep reported %+v", again)
	}
}

// TestTender_RespondAt: expiries that happen before a response are reported,
// also when the late response itself is rejected (TMS-0105).
func TestTender_RespondAt(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	tender, _ := NewTender("load-1", []string{"car-1", "car-2", "car-3"}, 10*time.Minute)
	tender.Send(t0)
	expired, err := tender.RespondAt("car-1", true, t0.Add(11*time.Minute))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeTenderExpired {
		t.Fatalf("err = %v, want TMS-0105", err)
	}
	if len(expired) != 1 || expired[0].CarrierID != "car-1" {
		t.Fatalf("expired = %+v, want car-1", expired)
	}
	expired, err = tender.RespondAt("car-3", true, t0.Add(25*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].CarrierID != "car-2" || tender.AcceptedCarrier != "car-3" {
		t.Fatalf("expired = %+v carrier = %s", expired, tender.AcceptedCarrier)
	}
	if !tender.Responded("car-3", true) || tender.Responded("car-3", false) || tender.Responded("car-1", true) {
		t.Fatal("Responded must match only the carrier's last response")
	}
	if _, ok := tender.Pending(); ok {
		t.Fatal("an accepted tender has no pending attempt")
	}
}

func TestTender_RespondFromAnotherCarrierIsNotPending(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	tender, _ := NewTender("load-1", []string{"car-1", "car-2"}, 10*time.Minute)
	tender.Send(t0)
	if _, err := tender.RespondAt("car-2", true, t0.Add(time.Minute)); !errors.Is(err, ErrNotPending) {
		t.Fatalf("err = %v, want ErrNotPending", err)
	}
	if a, ok := tender.Pending(); !ok || a.CarrierID != "car-1" {
		t.Fatalf("pending = %+v", a)
	}
}
