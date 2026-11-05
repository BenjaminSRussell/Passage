package domain

import (
	"errors"
	"strings"
	"testing"
)

func edifactError(t *testing.T, err error) *EdifactError {
	t.Helper()
	var e *EdifactError
	if !errors.As(err, &e) || !errors.Is(err, ErrEdifactSyntax) {
		t.Fatalf("err = %v, want an *EdifactError", err)
	}
	return e
}

func serializeOK(t *testing.T, ic EdifactInterchange) string {
	t.Helper()
	raw, err := SerializeEdifact(ic, false)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDetectSyntax(t *testing.T) {
	for in, want := range map[string]string{
		"ISA*00*": SyntaxX12, "\ufeff\r\n ISA*": SyntaxX12, "UNA:+.? 'UNB": SyntaxEdifact, "\nUNB+UNOC:3": SyntaxEdifact,
		"<xml/>": "", "": "", "GS*PO": "",
	} {
		if got := DetectSyntax([]byte(in)); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func groupedOrders(t *testing.T) EdifactInterchange {
	t.Helper()
	ic, err := ParseEdifact(edifactFixture(t, "orders-inbound.edi"))
	if err != nil {
		t.Fatal(err)
	}
	m1, m2 := ic.Messages[0], ic.Messages[0]
	m1.Group, m2.Group, m2.Ref = "G1", "G1", "2"
	ic.Groups = []EdifactGroup{{ID: "ORDERS", Sender: ic.Sender, Recipient: ic.Recipient, Date: "261105", Time: "0900", Ref: "G1",
		Agency: "UN", Version: "D", Release: "96A"}}
	ic.Messages = []EdifactMessage{m1, m2}
	return ic
}

func TestReadEdifact_FunctionalGroupsRoundTripAndGroupErrors(t *testing.T) {
	raw := serializeOK(t, groupedOrders(t))
	for _, want := range []string{"UNG+ORDERS+4012345000009:14+PASSAGE:ZZZ+261105:0900+G1+UN+D:96A'", "UNE+2+G1'", "UNZ+1+42'"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("%s missing in %s", want, raw)
		}
	}
	back, err := ParseEdifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Groups) != 1 || back.Groups[0].Ref != "G1" || len(back.Messages) != 2 || back.Messages[1].Group != "G1" || back.Messages[1].Ref != "2" {
		t.Fatalf("groups %+v messages %d", back.Groups, len(back.Messages))
	}
	if again := serializeOK(t, back); again != raw {
		t.Fatalf("round trip differs:\n%s\n%s", again, raw)
	}

	cases := map[string]struct {
		raw, code, group string
	}{
		"UNE count":         {strings.Replace(raw, "UNE+2+G1'", "UNE+3+G1'", 1), ContrlErrCount, "G1"},
		"UNE ref":           {strings.Replace(raw, "UNE+2+G1'", "UNE+2+G2'", 1), ContrlErrReferences, "G1"},
		"no UNE":            {strings.Replace(raw, "UNE+2+G1'", "", 1), ContrlErrMissing, "G1"},
		"UNZ counts groups": {strings.Replace(raw, "UNZ+1+42'", "UNZ+2+42'", 1), ContrlErrCount, ""},
		"mixed":             {strings.Replace(raw, "UNE+2+G1'", "UNE+2+G1'UNH+3+ORDERS:D:96A:UN'UNT+2+3'", 1), ContrlErrMixed, ""},
		"UNE outside":       {strings.Replace(edifactFixture(t, "orders-inbound.edi"), "UNZ+1+42'", "UNE+1+X'\nUNZ+1+42'", 1), ContrlErrOutside, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ic, _, err := ReadEdifact(c.raw)
			e := edifactError(t, err)
			if e.Code != c.code || e.Group != c.group || ic.Control != "42" {
				t.Fatalf("%+v control %q", e, ic.Control)
			}
		})
	}

	// A group error is answered with UCI 4 and a UCF 4 carrying the code.
	ic, _, err := ReadEdifact(cases["UNE count"].raw)
	c, err := RefusalCONTRL(ic, edifactError(t, err), "5", fixedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got := serializeOK(t, c); !strings.Contains(got, "UCI+42+4012345000009:14+PASSAGE:ZZZ+4'UCF+G1+4012345000009:14+PASSAGE:ZZZ+4+29'") {
		t.Fatalf("CONTRL %s", got)
	}
}

func TestReadEdifact_MessageWithWrongUNTIsKeptAndRejectedAlone(t *testing.T) {
	good := edifactFixture(t, "orders-inbound.edi")
	for name, c := range map[string]struct {
		from, to, code string
		element        int
	}{
		"UNT count": {"UNT+21+1'", "UNT+20+1'", ContrlErrCount, 1},
		"UNT ref":   {"UNT+21+1'", "UNT+21+9'", ContrlErrReferences, 2},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(good, c.from, c.to, 1)
			ic, broken, err := ReadEdifact(raw)
			if err != nil || len(broken) != 1 || len(ic.Messages) != 1 {
				t.Fatalf("%v %+v", err, broken)
			}
			b := broken[0]
			if b.Code != c.code || b.Message != "1" || b.Segment != 21 || b.Element != c.element {
				t.Fatalf("%+v", b)
			}
			if _, err := ParseEdifact(raw); edifactError(t, err).Message != "1" {
				t.Fatal("ParseEdifact stays strict")
			}
			contrl, err := BuildCONTRL(ic, []EdifactOutcome{{Ref: "1", ErrorCode: b.Code, SegmentPosition: b.Segment, ElementPosition: b.Element, ComponentPosition: b.Component}}, "7", fixedAt)
			if err != nil {
				t.Fatal(err)
			}
			want := "UCI+42+4012345000009:14+PASSAGE:ZZZ+4'UCM+1+ORDERS:D:96A:UN:EAN008+4+" + c.code + "'UCS+21+" + c.code + "'UCD+" + c.code + "+" + string(rune('0'+c.element)) + ":1'"
			if got := serializeOK(t, contrl); !strings.Contains(got, want) {
				t.Fatalf("CONTRL %s\nwant %s", got, want)
			}
		})
	}
}

func TestReadEdifact_InterchangeErrorsKeepTheUNBForTheCONTRL(t *testing.T) {
	good := edifactFixture(t, "orders-inbound.edi")
	for name, c := range map[string]struct {
		raw, code string
	}{
		"UNZ count":      {strings.Replace(good, "UNZ+1+42'", "UNZ+2+42'", 1), ContrlErrCount},
		"UNZ ref":        {strings.Replace(good, "UNZ+1+42'", "UNZ+1+43'", 1), ContrlErrReferences},
		"no terminator":  {strings.TrimSuffix(strings.TrimSpace(good), "'"), ContrlErrMissing},
		"no UNZ":         {strings.Replace(good, "UNZ+1+42'", "", 1), ContrlErrMissing},
		"no UNT":         {strings.Replace(good, "UNT+21+1'\n", "", 1), ContrlErrMissing},
		"outside":        {strings.Replace(good, "UNZ+1+42'", "FTX+AAI'\nUNZ+1+42'", 1), ContrlErrOutside},
		"syntax version": {strings.Replace(good, "UNOC:3", "UNOC:2", 1), ContrlErrSyntaxVersion},
	} {
		t.Run(name, func(t *testing.T) {
			ic, _, err := ReadEdifact(c.raw)
			e := edifactError(t, err)
			if e.Code != c.code || ic.Control != "42" {
				t.Fatalf("%+v control %q", e, ic.Control)
			}
			contrl, err := RefusalCONTRL(ic, e, "8", fixedAt)
			if err != nil {
				t.Fatal(err)
			}
			got := serializeOK(t, contrl)
			if !strings.Contains(got, "UCI+42+4012345000009:14+PASSAGE:ZZZ+4+"+c.code+"'") || !strings.Contains(got, "UNB+UNOC:3+PASSAGE:ZZZ+4012345000009:14+") ||
				!strings.Contains(got, "+8'UNH+1+CONTRL:2:2:UN'") {
				t.Fatalf("CONTRL %s", got)
			}
		})
	}
	// Without a UNB reference there is nothing to answer.
	ic, _, err := ReadEdifact(strings.Replace(good, "261105:0900+42++++1'", "261105:0900'", 1))
	if edifactError(t, err).Code != ContrlErrMissing || ic.Control != "" {
		t.Fatalf("%v %q", err, ic.Control)
	}
	if _, err := RefusalCONTRL(ic, &EdifactError{Code: ContrlErrMissing}, "9", fixedAt); err == nil {
		t.Fatal("a CONTRL without the UNB reference must be refused")
	}
}

func TestReadEdifact_CustomServiceCharactersAndCONTRLInThem(t *testing.T) {
	ic, err := ParseEdifact(edifactFixture(t, "orders-inbound.edi"))
	if err != nil {
		t.Fatal(err)
	}
	ic.Delims = EdifactDelims{Component: '>', Element: '*', Decimal: ',', Release: '!', Repetition: ' ', Segment: '~'}
	raw := serializeOK(t, ic)
	if !strings.HasPrefix(raw, "UNA>*,! ~UNB*UNOC>3*") || !strings.Contains(raw, "Händler GmbH + Co. KG") {
		t.Fatalf("custom %s", raw)
	}
	back, err := ParseEdifact(raw)
	if err != nil {
		t.Fatal(err)
	}
	o, issues := MapORDERS(back.Messages[0])
	if len(issues) != 0 || o.BillToName != "Händler GmbH + Co. KG" || o.Lines[0].Description != "Schraube M8:20" {
		t.Fatalf("%+v %+v", o, issues)
	}
	c, err := BuildCONTRL(back, []EdifactOutcome{{Ref: "1", Accepted: true}}, "3", fixedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got := serializeOK(t, c); !strings.HasPrefix(got, "UNA>*,! ~UNB*UNOC>3*PASSAGE>ZZZ*4012345000009>14*") || !strings.Contains(got, "UCI*42*4012345000009>14*PASSAGE>ZZZ*7~") {
		t.Fatalf("CONTRL %s", got)
	}
}

func TestBuildCONTRL_GroupsGetUCFAndRejectedMessagesUCSAndUCD(t *testing.T) {
	ic := groupedOrders(t)
	g2 := ic.Groups[0]
	g2.Ref = "G2"
	m := ic.Messages[0]
	m.Group, m.Ref = "G2", "1" // the same UNH reference as in G1
	ic.Groups = append(ic.Groups, g2)
	ic.Messages = append(ic.Messages, m)
	c, err := BuildCONTRL(ic, []EdifactOutcome{
		{Group: "G1", Ref: "1", Accepted: true}, {Group: "G1", Ref: "2", Accepted: true},
		{Group: "G2", Ref: "1", ErrorCode: ContrlErrInvalidValue, SegmentPosition: 2, ElementPosition: 1, ComponentPosition: 1},
	}, "4", fixedAt)
	if err != nil {
		t.Fatal(err)
	}
	got := serializeOK(t, c)
	want := "UCI+42+4012345000009:14+PASSAGE:ZZZ+7'UCF+G2+4012345000009:14+PASSAGE:ZZZ+4'UCM+1+ORDERS:D:96A:UN:EAN008+4+12'UCS+2+12'UCD+12+1:1'UNT+"
	if !strings.Contains(got, want) || strings.Contains(got, "UCF+G1") {
		t.Fatalf("CONTRL %s\nwant %s", got, want)
	}
	back, err := ParseEdifact(got)
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := ParseCONTRL(back.Messages[0]); err != nil || ack.Status(3) != StatusAcceptedWithErrors {
		t.Fatalf("%+v %v", ack, err)
	}
	if _, err := BuildCONTRL(ic, []EdifactOutcome{{Group: "G1", Ref: "1", Accepted: true}, {Group: "G1", Ref: "2", Accepted: true}, {Group: "G1", Ref: "3", Accepted: true}}, "5", fixedAt); err == nil {
		t.Fatal("an outcome per message, keyed by group and reference, is required")
	}
}

func TestMapORDERS_IssuesArePlacedForTheCONTRL(t *testing.T) {
	ic, err := ParseEdifact(edifactFixture(t, "orders-inbound.edi"))
	if err != nil {
		t.Fatal(err)
	}
	m := ic.Messages[0]
	var segs []EdifactSegment
	for _, s := range m.Segments {
		switch {
		case s.Tag == "BGM":
			s = Seg("BGM", "999", "PO-EU-881", "9")
		case s.Tag == "CNT":
			s = Seg("CNT", "2:3")
		case s.Tag == "QTY" && len(segs) > 12: // line 2's QTY (position 17) goes
			continue
		}
		segs = append(segs, s)
	}
	m.Segments = segs
	_, issues := MapORDERS(m)
	want := map[ContrlPlace]bool{
		{ContrlErrInvalidValue, 2, 1, 1}: true, // BGM 1001
		{ContrlErrMissing, 15, 0, 0}:     true, // line 2 (LIN at 15) has no QTY 21
		{ContrlErrCount, 19, 1, 2}:       true, // CNT, one segment earlier now
	}
	for _, is := range issues {
		delete(want, is.Contrl)
	}
	if len(want) != 0 {
		t.Fatalf("missing places %+v in %+v", want, issues)
	}
}

func refusalSample(t *testing.T) string {
	t.Helper()
	ic := Interchange{
		Delims: Delims{Element: '*', Component: '>', Segment: '~'}, Sender: Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"},
		Receiver: Party{Qualifier: "ZZ", ID: "PASSAGE"}, Date: "261105", Time: "0900", Version: "00401", Control: "000000077", Usage: "T",
		Groups: []Group{{FunctionalID: "PO", Sender: "DISTRIBUTOR", Receiver: "PASSAGE", Date: "20261105", Time: "0900", Control: "77", Version: "004010",
			Transactions: []Transaction{
				{SetID: "850", Control: "0001", Body: []Segment{{ID: "BEG", Elements: []string{"00", "SA", "PO1"}}}},
				{SetID: "850", Control: "0002", Body: []Segment{{ID: "BEG", Elements: []string{"00", "SA", "PO2"}}}},
			}}},
	}
	raw, err := Serialize(ic)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReadRefusedX12_TrailerErrorsBecome997Codes(t *testing.T) {
	good := refusalSample(t)
	ack := func(raw string) string {
		t.Helper()
		ic, groups, ok := ReadRefusedX12(raw)
		if !ok || len(groups) != 1 {
			t.Fatalf("not readable: %v %+v", ok, groups)
		}
		out, err := Reject997(ic, groups[0], "000000005", "5", fixedAt)
		if err != nil {
			t.Fatal(err)
		}
		text, err := Serialize(out)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "*ZZ*PASSAGE        *ZZ*DISTRIBUTOR    *") || !strings.Contains(text, "GS*FA*PASSAGE*DISTRIBUTOR*") {
			t.Fatalf("997 not back to the sender: %s", text)
		}
		return text
	}
	for name, c := range map[string]struct{ raw, want string }{
		"SE count":   {strings.Replace(good, "SE*3*0001~", "SE*9*0001~", 1), "AK1*PO*77~AK2*850*0001~AK5*R*4~AK2*850*0002~AK5*R~AK9*R*2*2*0~"},
		"SE control": {strings.Replace(good, "SE*3*0002~", "SE*3*0009~", 1), "AK2*850*0001~AK5*R~AK2*850*0002~AK5*R*3~AK9*R*2*2*0~"},
		"no SE":      {strings.Replace(good, "SE*3*0001~", "", 1), "AK2*850*0001~AK5*R*2~"},
		"GE count":   {strings.Replace(good, "GE*2*77~", "GE*3*77~", 1), "AK9*R*2*2*0*5~"},
		"GE control": {strings.Replace(good, "GE*2*77~", "GE*2*78~", 1), "AK9*R*2*2*0*4~"},
		"no GE":      {strings.Replace(good, "GE*2*77~", "", 1), "AK9*R*2*2*0*3~"},
		"IEA count":  {strings.Replace(good, "IEA*1*", "IEA*2*", 1), "AK2*850*0001~AK5*R~AK2*850*0002~AK5*R~AK9*R*2*2*0~"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(c.raw); err == nil {
				t.Fatal("the sample must not parse")
			}
			if got := ack(c.raw); !strings.Contains(got, c.want) {
				t.Fatalf("997 %s\nwant %s", got, c.want)
			}
		})
	}
	// A parsed interchange refused for its sender: every transaction R.
	ic, err := Parse(good)
	if err != nil {
		t.Fatal(err)
	}
	groups := RefusedGroups(ic)
	out, _ := Reject997(ic, groups[0], "000000006", "6", fixedAt)
	if text, _ := Serialize(out); !strings.Contains(text, "AK1*PO*77~AK2*850*0001~AK5*R~AK2*850*0002~AK5*R~AK9*R*2*2*0~") {
		t.Fatalf("997 %s", text)
	}
	// Nothing to answer: no ISA, or only 997 groups.
	if _, _, ok := ReadRefusedX12("GS*PO*A*B~"); ok {
		t.Fatal("no ISA")
	}
	fa := strings.Replace(good, "GS*PO*", "GS*FA*", 1)
	if _, _, ok := ReadRefusedX12(strings.Replace(fa, "GE*2*77~", "GE*3*77~", 1)); ok {
		t.Fatal("997 groups are never answered")
	}
}
