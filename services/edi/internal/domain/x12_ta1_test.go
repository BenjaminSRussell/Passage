package domain

import (
	"strings"
	"testing"
	"time"
)

const ta1Sample = "ISA*00*          *00*          *ZZ*DISTRIBUTOR    *ZZ*PASSAGE        *261105*0900*U*00401*000000042*0*T*>~" +
	"GS*PO*DISTRIBUTOR*PASSAGE*20261105*0900*7*X*004010~ST*850*0001~BEG*00*SA*PO1**20261105~SE*3*0001~GE*1*7~IEA*1*000000042~"

func TestBuildTA1_RoundTripsAndIsNeverAGroup(t *testing.T) {
	ic, err := Parse(ta1Sample)
	if err != nil {
		t.Fatal(err)
	}
	ta1, err := BuildTA1(ic, TA1Rejected, TA1Duplicate, "000000005", time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Serialize(ta1)
	if err != nil {
		t.Fatal(err)
	}
	want := "ISA*00*          *00*          *ZZ*PASSAGE        *ZZ*DISTRIBUTOR    *261105*1030*U*00401*000000005*0*T*>~TA1*000000042*261105*0900*R*025~IEA*0*000000005~"
	if raw != want {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}
	back, err := Parse(raw)
	if err != nil || len(back.Groups) != 0 || len(back.TA1) != 1 || back.TA1[0] != (InterchangeAck{Control: "000000042", Date: "261105", Time: "0900", Code: "R", Note: "025"}) {
		t.Fatalf("%+v %v", back, err)
	}
	if !IsTA1Interchange(raw) || IsTA1Interchange(ta1Sample) {
		t.Fatal("IsTA1Interchange")
	}
	// A TA1 before a 997 group is read with the group.
	mixed := strings.Replace(ta1Sample, "*>~GS", "*>~TA1*000000009*261104*1200*A*000~GS", 1)
	if ic, err := Parse(mixed); err != nil || len(ic.TA1) != 1 || ic.TA1[0].Code != TA1Accepted || len(ic.Groups) != 1 {
		t.Fatalf("mixed: %+v %v", ic, err)
	}
	for _, bad := range []string{"TA1*42*261104*1200*A*000~", "TA1*000000009*261104*1200*X*000~", "TA1*000000009*261104*1200*A*~"} {
		if _, err := Parse(strings.Replace(ta1Sample, "*>~GS", "*>~"+bad+"GS", 1)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := BuildTA1(ic, "X", "025", "1", time.Now()); err == nil {
		t.Fatal("TA104 X")
	}
	ic.Control = "42"
	if _, err := BuildTA1(ic, TA1Rejected, "025", "1", time.Now()); err == nil {
		t.Fatal("ISA13 not nine digits")
	}
}

func TestTA1Cause_InterchangeLevelErrorsOnly(t *testing.T) {
	for name, c := range map[string]struct {
		raw  string
		note string
		ok   bool
	}{
		"clean":                 {ta1Sample, "", false},
		"no IEA":                {strings.TrimSuffix(ta1Sample, "IEA*1*000000042~"), TA1PrematureEnd, true},
		"IEA02":                 {strings.Replace(ta1Sample, "IEA*1*000000042", "IEA*1*000000043", 1), TA1ControlMismatch, true},
		"IEA01":                 {strings.Replace(ta1Sample, "IEA*1*", "IEA*2*", 1), TA1GroupCount, true},
		"no group":              {ta1Sample[:106] + "IEA*0*000000042~", TA1InvalidContent, true},
		"after IEA":             {ta1Sample + "ST*850*0002~", TA1InvalidContent, true},
		"a group error (997)":   {strings.Replace(ta1Sample, "SE*3*0001", "SE*4*0001", 1), "", false},
		"a TA1 is not answered": {ta1Sample[:106] + "TA1*000000009*261104*1200*R*025~IEA*1*000000042~", "", false},
		"unreadable ISA":        {"ISA*00*short~", "", false},
	} {
		ic, note, ok := TA1Cause(c.raw)
		if ok != c.ok || note != c.note || (ok && ic.Control != "000000042") {
			t.Errorf("%s: %q %v %+v", name, note, ok, ic)
		}
	}
}
