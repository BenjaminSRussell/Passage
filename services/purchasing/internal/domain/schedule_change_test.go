package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func schedQty(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// schedFixture is an agreement as of 2027-01-04: line 1 is FIRM (7 days out),
// line 2 TRADEOFF (21 days), line 3 FORECAST (56 days) with 40 already received.
func schedFixture() SchedulingAgreement {
	p := DefaultReleaseProfile()
	return SchedulingAgreement{
		ID: "sa-1", Number: "SA-00000001", Currency: "EUR", Unit: "EA", TargetQty: schedQty("300"),
		ValidFrom: schedDay(2027, time.January, 1), ValidTo: schedPtr(schedDay(2027, time.June, 30)), Profile: p,
		Lines: []ScheduleLine{
			{LineNo: 1, DeliveryDate: schedDay(2027, time.January, 11), Qty: schedQty("100")},
			{LineNo: 2, DeliveryDate: schedDay(2027, time.January, 25), Qty: schedQty("100")},
			{LineNo: 3, DeliveryDate: schedDay(2027, time.March, 1), Qty: schedQty("100"), ReceivedQty: schedQty("40")},
		},
	}
}

// keep returns the change that leaves a fixture line as it is.
func keep(n int) ScheduleChange {
	l := schedFixture().Lines[n-1]
	return ScheduleChange{LineNo: l.LineNo, DeliveryDate: l.DeliveryDate, Qty: l.Qty}
}

func with(c ScheduleChange, qty, reason string) ScheduleChange {
	c.Qty, c.Reason = schedQty(qty), reason
	return c
}

func TestApplySchedule_RulesByZone(t *testing.T) {
	asOf := schedDay(2027, time.January, 4)
	moved := keep(1)
	moved.DeliveryDate = schedDay(2027, time.February, 15)
	cases := []struct {
		name     string
		changes  []ScheduleChange
		wantCode string
		wantWarn int
		wantQty  map[int]string // line number -> qty after the change
		wantRsn  map[int]string
		wantLen  int
	}{
		{name: "firm reduction without reason is PRC-SA-001", changes: []ScheduleChange{with(keep(1), "50", ""), keep(2), keep(3)}, wantCode: CodeFirmZone},
		{
			name:    "firm reduction with reason passes and records it",
			changes: []ScheduleChange{with(keep(1), "50", "CUT"), keep(2), with(keep(3), "100", "IGNORED")},
			wantQty: map[int]string{1: "50", 2: "100", 3: "100"}, wantRsn: map[int]string{1: "CUT", 3: ""}, wantLen: 3,
		},
		{name: "firm increase needs no reason", changes: []ScheduleChange{with(keep(1), "150", ""), keep(2), keep(3)}, wantQty: map[int]string{1: "150"}, wantLen: 3},
		{name: "moving a firm line needs a reason", changes: []ScheduleChange{moved, keep(2), keep(3)}, wantCode: CodeFirmZone},
		{name: "moving a firm line with a reason passes", changes: []ScheduleChange{with(moved, "100", "PUSH_OUT"), keep(2), keep(3)}, wantRsn: map[int]string{1: "PUSH_OUT"}, wantLen: 3},
		{name: "trade-off reduction warns", changes: []ScheduleChange{keep(1), with(keep(2), "80", ""), keep(3)}, wantWarn: 1, wantQty: map[int]string{2: "80"}, wantLen: 3},
		{name: "forecast reduction passes silently", changes: []ScheduleChange{keep(1), keep(2), with(keep(3), "50", "")}, wantQty: map[int]string{3: "50"}, wantLen: 3},
		{name: "reduction below received is PRC-SA-002", changes: []ScheduleChange{keep(1), keep(2), with(keep(3), "30", "")}, wantCode: CodeReceivedAboveRelease},
		{name: "removing a line with receipts is PRC-SA-002", changes: []ScheduleChange{keep(1), keep(2)}, wantCode: CodeReceivedAboveRelease},
		{name: "removing a firm line needs a reason", changes: []ScheduleChange{keep(2), keep(3)}, wantCode: CodeFirmZone},
		{name: "a firm line sent with quantity 0 and a reason is removed", changes: []ScheduleChange{with(keep(1), "0", "CANCELLED"), keep(2), keep(3)}, wantQty: map[int]string{2: "100"}, wantLen: 2},
		{name: "a firm line sent with quantity 0 without a reason is PRC-SA-001", changes: []ScheduleChange{with(keep(1), "0", ""), keep(2), keep(3)}, wantCode: CodeFirmZone},
		{name: "a received line cannot be zeroed", changes: []ScheduleChange{keep(1), keep(2), with(keep(3), "0", "X")}, wantCode: CodeReceivedAboveRelease},
		{name: "a new row with quantity 0 is rejected", changes: []ScheduleChange{keep(1), keep(2), keep(3), {DeliveryDate: schedDay(2027, time.April, 1), Qty: schedQty("0")}}, wantCode: CodeValidation},
		{name: "removing a trade-off line warns and drops the row", changes: []ScheduleChange{keep(1), keep(3)}, wantWarn: 1, wantQty: map[int]string{1: "100", 3: "100"}, wantLen: 2},
		{
			name:    "a new row takes the next line number",
			changes: []ScheduleChange{keep(1), keep(2), keep(3), {DeliveryDate: schedDay(2027, time.April, 1), Qty: schedQty("10")}},
			wantQty: map[int]string{4: "10"}, wantLen: 4,
		},
		{name: "a new row outside validity is rejected", changes: []ScheduleChange{keep(1), keep(2), keep(3), {DeliveryDate: schedDay(2027, time.July, 1), Qty: schedQty("10")}}, wantCode: CodeValidation},
		{name: "unknown line is rejected", changes: []ScheduleChange{{LineNo: 9, DeliveryDate: schedDay(2027, time.January, 11), Qty: schedQty("1")}}, wantCode: CodeValidation},
		{name: "a line named twice is rejected", changes: []ScheduleChange{keep(1), keep(1), keep(2), keep(3)}, wantCode: CodeValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warns, err := ApplySchedule(schedFixture(), tc.changes, asOf)
			if tc.wantCode != "" {
				if code := schedErrCode(err); code != tc.wantCode {
					t.Fatalf("error code = %q (err %v), want %q", code, err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(warns) != tc.wantWarn || len(got) != tc.wantLen {
				t.Fatalf("warnings = %v (want %d), lines = %d (want %d)", warns, tc.wantWarn, len(got), tc.wantLen)
			}
			byNo := map[int]ScheduleLine{}
			for i, l := range got {
				byNo[l.LineNo] = l
				if i > 0 && got[i-1].LineNo >= l.LineNo {
					t.Errorf("lines not sorted by line number: %v", got)
				}
			}
			for no, want := range tc.wantQty {
				if l, ok := byNo[no]; !ok || !l.Qty.Equal(schedQty(want)) {
					t.Errorf("line %d = %+v, want qty %s", no, l, want)
				}
			}
			for no, want := range tc.wantRsn {
				if byNo[no].ReductionReason != want {
					t.Errorf("line %d reason = %q, want %q", no, byNo[no].ReductionReason, want)
				}
			}
		})
	}
}

// A change keeps the receipt history of a row, which the next release reads as
// cumulative received, and keeps the reason of an earlier reduction.
func TestApplySchedule_KeepsReceiptsAndEarlierReason(t *testing.T) {
	sa := schedFixture()
	sa.Lines[0].ReductionReason = "EARLIER"
	got, _, err := ApplySchedule(sa, []ScheduleChange{keep(1), keep(2), with(keep(3), "120", "")}, schedDay(2027, time.January, 4))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got[2].ReceivedQty.Equal(schedQty("40")) || got[0].ReductionReason != "EARLIER" {
		t.Fatalf("lines = %+v", got)
	}
}
