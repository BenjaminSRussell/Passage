package domain

import (
	"errors"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var gateT0 = time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)

func validCheckIn() GateCheckIn {
	sched := gateT0
	return GateCheckIn{
		SiteID: "site", Direction: DirectionOutbound, CarrierSCAC: "ABCD", TrailerNo: "TR-1001",
		DriverName: "Dana Driver", DriverLicenseID: "D1234567", AppointmentID: "appt", ScheduledAt: &sched,
		ArrivedAt: gateT0.Add(10 * time.Minute), FreeTimeMin: DefaultDetentionFreeTimeMin, ToleranceMin: DefaultGateToleranceMin,
		WalkInAllowed: true,
	}
}

func errCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestNormalizeAndValidateCheckIn(t *testing.T) {
	in := validCheckIn()
	in.Direction, in.CarrierSCAC, in.TrailerNo, in.DriverName = " outbound ", " abcd ", " tr-1001 ", "  Dana  "
	n := NormalizeCheckIn(in)
	if n.Direction != DirectionOutbound || n.CarrierSCAC != "ABCD" || n.TrailerNo != "TR-1001" || n.DriverName != "Dana" {
		t.Fatalf("not normalised: %+v", n)
	}
	tests := []struct {
		name   string
		mutate func(*GateCheckIn)
		field  string
	}{
		{"valid", func(*GateCheckIn) {}, ""},
		{"walk-in without appointment", func(c *GateCheckIn) { c.AppointmentID, c.ScheduledAt = "", nil }, ""},
		{"unknown direction", func(c *GateCheckIn) { c.Direction = "SIDEWAYS" }, "direction"},
		{"SCAC with digits", func(c *GateCheckIn) { c.CarrierSCAC = "AB1" }, "carrierScac"},
		{"SCAC too long", func(c *GateCheckIn) { c.CarrierSCAC = "ABCDE" }, "carrierScac"},
		{"no trailer", func(c *GateCheckIn) { c.TrailerNo = "" }, "trailerNo"},
		{"seal too long", func(c *GateCheckIn) { c.SealNo = "123456789012345678901" }, "sealNo"},
		{"no driver", func(c *GateCheckIn) { c.DriverName = "" }, "driverName"},
		{"no license", func(c *GateCheckIn) { c.DriverLicenseID = "" }, "driverLicenseId"},
		{"appointment without time", func(c *GateCheckIn) { c.ScheduledAt = nil }, "appointmentId"},
		{"time without appointment", func(c *GateCheckIn) { c.AppointmentID = "" }, "appointmentId"},
		{"free time over a day", func(c *GateCheckIn) { c.FreeTimeMin = 1441 }, "freeTimeMin"},
		{"negative tolerance", func(c *GateCheckIn) { c.ToleranceMin = -1 }, "toleranceMin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validCheckIn()
			tc.mutate(&c)
			fields := ValidateCheckIn(c)
			if tc.field == "" {
				if len(fields) != 0 {
					t.Fatalf("unexpected errors %v", fields)
				}
				return
			}
			if _, ok := fields[tc.field]; !ok || len(fields) != 1 {
				t.Fatalf("errors = %v, want only %s", fields, tc.field)
			}
		})
	}
}

func TestOpenVisit_WalkInAndArrival(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*GateCheckIn)
		code    string
		walkIn  bool
		arrival string
		delta   int
	}{
		{"on time inside the 60 min window", func(c *GateCheckIn) {}, "", false, ArrivalOnTime, 10},
		{"exactly at the window edge is on time", func(c *GateCheckIn) { c.ArrivedAt = gateT0.Add(60 * time.Minute) }, "", false, ArrivalOnTime, 60},
		{"late beyond the window", func(c *GateCheckIn) { c.ArrivedAt = gateT0.Add(95 * time.Minute) }, "", false, ArrivalLate, 95},
		{"early beyond the window", func(c *GateCheckIn) { c.ArrivedAt = gateT0.Add(-61 * time.Minute) }, "", false, ArrivalEarly, -61},
		{"walk-in allowed", func(c *GateCheckIn) { c.AppointmentID, c.ScheduledAt = "", nil }, "", true, "", 0},
		{"walk-in refused at the site (TMS-0128)", func(c *GateCheckIn) {
			c.AppointmentID, c.ScheduledAt, c.WalkInAllowed = "", nil, false
		}, CodeNoAppointment, false, "", 0},
		{"appointment admitted where walk-ins are refused", func(c *GateCheckIn) { c.WalkInAllowed = false }, "", false, ArrivalOnTime, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validCheckIn()
			tc.mutate(&c)
			v, err := OpenVisit(c)
			if errCode(err) != tc.code || (tc.code == "" && err != nil) {
				t.Fatalf("err = %v, want code %q", err, tc.code)
			}
			if tc.code != "" {
				return
			}
			if v.Status != VisitInYard || v.WalkIn != tc.walkIn || !v.CheckedInAt.Equal(c.ArrivedAt) {
				t.Fatalf("visit = %+v", v)
			}
			switch {
			case tc.arrival == "" && v.Arrival != nil:
				t.Fatalf("walk-in classified: %+v", v.Arrival)
			case tc.arrival != "" && (v.Arrival == nil || v.Arrival.Status != tc.arrival || v.Arrival.DeltaMin != tc.delta):
				t.Fatalf("arrival = %+v, want %s %d", v.Arrival, tc.arrival, tc.delta)
			case tc.arrival != "" && tc.arrival != ArrivalOnTime && v.Arrival.Code != CodeArrivalOutsideWindow:
				t.Fatalf("EARLY or LATE without TMS-0127: %+v", v.Arrival)
			}
		})
	}
}

// AC-34.12: an outbound load sealed with 778812 cannot check out when the
// driver declares 778813.
func TestCheckOut_AC3412SealMismatchBlocks(t *testing.T) {
	v, err := OpenVisit(validCheckIn())
	if err != nil {
		t.Fatal(err)
	}
	v, err = v.RecordSeal(" 778812 ")
	if err != nil || v.SealNo != "778812" {
		t.Fatalf("seal = %q, %v", v.SealNo, err)
	}
	blocked, err := v.CheckOut("778813", gateT0.Add(3*time.Hour), dec("75"))
	if errCode(err) != CodeSealMismatch {
		t.Fatalf("err = %v, want TMS-0129", err)
	}
	if blocked.Status != VisitInYard || blocked.CheckedOutAt != nil {
		t.Fatalf("blocked check-out changed the visit: %+v", blocked)
	}
	out, err := v.CheckOut("778812", gateT0.Add(3*time.Hour), dec("75"))
	if err != nil || out.Status != VisitCheckedOut {
		t.Fatalf("matching seal: %+v, %v", out, err)
	}
}

func TestCheckOut_SealRules(t *testing.T) {
	tests := []struct {
		name      string
		direction string
		recorded  string
		declared  string
		code      string
	}{
		{"outbound without a confirmed load", DirectionOutbound, "", "778812", CodeSealMismatch},
		{"outbound with no seal declared", DirectionOutbound, "778812", "", CodeSealMismatch},
		{"seal compare ignores case and space", DirectionOutbound, "ab-778812", " AB-778812 ", ""},
		{"inbound sealed trailer must match", DirectionInbound, "555", "556", CodeSealMismatch},
		{"inbound unsealed trailer leaves without a check", DirectionInbound, "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := validCheckIn()
			c.Direction, c.SealNo = tc.direction, tc.recorded
			v, err := OpenVisit(c)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.CheckOut(tc.declared, gateT0.Add(time.Hour), dec("0"))
			if errCode(err) != tc.code || (tc.code == "" && err != nil) {
				t.Fatalf("err = %v, want %q", err, tc.code)
			}
		})
	}
}

// AC-34.11 through the visit: in at 08:00 with 2 h free time, out at 11:20
// gives 80 detention minutes, billed as 2 h at the carrier's rate (TMS-0126).
func TestCheckOut_AC3411Detention(t *testing.T) {
	c := validCheckIn()
	c.Direction, c.ArrivedAt = DirectionInbound, gateT0
	v, _ := OpenVisit(c)
	out, err := v.CheckOut("", time.Date(2026, 3, 2, 11, 20, 0, 0, time.UTC), dec("85.00"))
	if err != nil {
		t.Fatal(err)
	}
	d := out.Detention
	if d == nil || d.DwellMin != 200 || d.DetentionMin != 80 || d.Hours != 2 || !d.Charge.Equal(dec("170.00")) || d.Code != CodeDetention {
		t.Fatalf("detention = %+v", d)
	}
	if !out.CheckedOutAt.Equal(time.Date(2026, 3, 2, 11, 20, 0, 0, time.UTC)) {
		t.Fatalf("checked out at %v", out.CheckedOutAt)
	}
	within, _ := v.CheckOut("", gateT0.Add(2*time.Hour), dec("85.00"))
	if within.Detention.DetentionMin != 0 || !within.Detention.Charge.IsZero() || within.Detention.Code != "" {
		t.Fatalf("no detention inside free time: %+v", within.Detention)
	}
}

func TestCheckOut_StateAndTime(t *testing.T) {
	c := validCheckIn()
	c.Direction = DirectionInbound
	v, _ := OpenVisit(c)
	if _, err := v.CheckOut("", c.ArrivedAt.Add(-time.Minute), dec("0")); !errors.Is(err, ErrCheckOutBeforeCheckIn) {
		t.Fatalf("err = %v, want ErrCheckOutBeforeCheckIn", err)
	}
	out, err := v.CheckOut("", c.ArrivedAt, dec("0"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.CheckOut("", c.ArrivedAt.Add(time.Hour), dec("0")); errCode(err) != CodeVisitState {
		t.Fatalf("second check-out err = %v, want TMS-0118", err)
	}
	if _, err := out.RecordSeal("1"); errCode(err) != CodeVisitState {
		t.Fatalf("seal after check-out err = %v, want TMS-0118", err)
	}
}

// Property: a matching seal always checks out, any other seal never does, and
// detention never exceeds the dwell.
func TestCheckOut_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seal := rapid.StringMatching(`[0-9]{6}`).Draw(t, "seal")
		other := rapid.StringMatching(`[0-9]{6}`).Draw(t, "other")
		dwell := rapid.IntRange(0, 3000).Draw(t, "dwell")
		c := validCheckIn()
		c.SealNo = seal
		v, err := OpenVisit(c)
		if err != nil {
			t.Fatal(err)
		}
		at := c.ArrivedAt.Add(time.Duration(dwell) * time.Minute)
		out, err := v.CheckOut(seal, at, dec("50"))
		if err != nil || out.Detention.DwellMin != dwell || out.Detention.DetentionMin > dwell {
			t.Fatalf("matching seal: %+v, %v", out.Detention, err)
		}
		if other != seal {
			if _, err := v.CheckOut(other, at, dec("50")); errCode(err) != CodeSealMismatch {
				t.Fatalf("seal %s against %s: %v", other, seal, err)
			}
		}
	})
}
