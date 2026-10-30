package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func ts(h, m int) time.Time { return time.Date(2026, 10, 20, h, m, 0, 0, time.UTC) }

func code(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestPlanCorrection(t *testing.T) {
	stop := ts(15, 0)
	cases := []struct {
		name string
		reg  StoredRegistration
		req  CorrectionRequest
		want string
		hrs  string
	}{
		{"clock-in moves", StoredRegistration{Kind: KindClockIn, StartedAt: ts(7, 0)}, CorrectionRequest{StartedAt: ptr(ts(6, 30)), Reason: "forgot"}, "", ""},
		{"clock-in takes no stop", StoredRegistration{Kind: KindClockIn, StartedAt: ts(7, 0)}, CorrectionRequest{StoppedAt: &stop, Reason: "x"}, CodeRegistrationNotCorrectable, ""},
		{"job-stop recomputes hours", StoredRegistration{Kind: "job-stop", StartedAt: ts(8, 0), StoppedAt: &stop}, CorrectionRequest{StartedAt: ptr(ts(9, 0)), Reason: "x"}, "", "6"},
		{"open job closes", StoredRegistration{Kind: "job-start", StartedAt: ts(8, 0)}, CorrectionRequest{StoppedAt: &stop, Reason: "x"}, "", "7"},
		{"bundled is refused", StoredRegistration{Kind: "job-stop", Bundled: true, StartedAt: ts(8, 0), StoppedAt: &stop}, CorrectionRequest{StartedAt: ptr(ts(9, 0)), Reason: "x"}, CodeRegistrationNotCorrectable, ""},
		{"reason required", StoredRegistration{Kind: KindClockIn, StartedAt: ts(7, 0)}, CorrectionRequest{StartedAt: ptr(ts(6, 0))}, CodeCorrectionInvalid, ""},
		{"stop before start", StoredRegistration{Kind: "job-start", StartedAt: ts(8, 0)}, CorrectionRequest{StoppedAt: ptr(ts(7, 0)), Reason: "x"}, CodeCorrectionInvalid, ""},
		{"over 24 hours", StoredRegistration{Kind: "job-start", StartedAt: ts(8, 0)}, CorrectionRequest{StoppedAt: ptr(ts(8, 0).Add(25 * time.Hour)), Reason: "x"}, CodeCorrectionInvalid, ""},
		{"no change", StoredRegistration{Kind: KindClockIn, StartedAt: ts(7, 0)}, CorrectionRequest{StartedAt: ptr(ts(7, 0)), Reason: "x"}, CodeCorrectionInvalid, ""},
		{"unconfirmed auto clock-out confirmed with its own time", StoredRegistration{Kind: KindClockOut, StartedAt: ts(22, 0), Unconfirmed: true}, CorrectionRequest{StartedAt: ptr(ts(22, 0)), Reason: "x"}, "", ""},
		{"confirmed auto clock-out needs a change", StoredRegistration{Kind: KindClockOut, StartedAt: ts(22, 0)}, CorrectionRequest{StartedAt: ptr(ts(22, 0)), Reason: "x"}, CodeCorrectionInvalid, ""},
		{"confirming still needs a reason", StoredRegistration{Kind: KindClockOut, StartedAt: ts(22, 0), Unconfirmed: true}, CorrectionRequest{StartedAt: ptr(ts(22, 0))}, CodeCorrectionInvalid, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PlanCorrection(c.reg, c.req)
			if code(err) != c.want {
				t.Fatalf("code %q (err %v), want %q", code(err), err, c.want)
			}
			if c.hrs != "" && (got.Hours == nil || !got.Hours.Equal(decimal.RequireFromString(c.hrs))) {
				t.Fatalf("hours %v, want %s", got.Hours, c.hrs)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func line(typ, h, m, a string) PayLine {
	return PayLine{Type: typ, Hours: decimal.RequireFromString(h), Multiplier: decimal.RequireFromString(m), Amount: decimal.RequireFromString(a)}
}

func eff(id, typ, h, m, a string) EffectiveLine {
	l := line(typ, h, m, a)
	return EffectiveLine{ID: id, Type: l.Type, Hours: l.Hours, Multiplier: l.Multiplier, Amount: l.Amount}
}

// AC-25.8: a day approved at 8.00 base + 2.00 overtime is edited to 9.00 hours worked.
func TestPlanRevision_AC25_8(t *testing.T) {
	effective := []EffectiveLine{eff("a", "base", "8.00", "1", "160"), eff("b", "overtime-1.5", "2.00", "1.5", "60")}
	rev := PlanRevision(effective, []PayLine{line("base", "8.00", "1", "160"), line("overtime-1.5", "1.00", "1.5", "30")})
	if len(rev.Reverse) != 1 || rev.Reverse[0].ID != "b" {
		t.Fatalf("reverse %+v, want only the overtime line", rev.Reverse)
	}
	if len(rev.Add) != 1 || rev.Add[0].Type != "overtime-1.5" || !rev.Add[0].Hours.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("add %+v", rev.Add)
	}
	if rev.Empty() {
		t.Fatal("revision should not be empty")
	}
}

func TestPlanRevision_SameLinesAreEmptyAndGoneDaysReverseAll(t *testing.T) {
	effective := []EffectiveLine{eff("a", "base", "8.00", "1", "160")}
	if rev := PlanRevision(effective, []PayLine{line("base", "8.0", "1.0", "160.00")}); !rev.Empty() {
		t.Fatalf("equal decimals with different scale must match: %+v", rev)
	}
	rev := PlanRevision(effective, nil)
	if len(rev.Reverse) != 1 || len(rev.Add) != 0 {
		t.Fatalf("a day without punches reverses its lines: %+v", rev)
	}
}
