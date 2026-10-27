package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func sampleInterchange() Interchange {
	return Interchange{
		Delims:   Delims{Element: '*', Component: '>', Segment: '~'},
		Sender:   Party{Qualifier: "ZZ", ID: "SENDER"},
		Receiver: Party{Qualifier: "ZZ", ID: "RECEIVER"},
		Date:     "260101", Time: "1200", Version: "00401", Control: "000000905", Usage: "T",
		Groups: []Group{{
			FunctionalID: "PO", Sender: "SENDER", Receiver: "RECEIVER", Date: "20260101", Time: "1200",
			Control: "42", Version: "004010",
			Transactions: []Transaction{{SetID: "850", Control: "0001", Body: []Segment{
				{ID: "BEG", Elements: []string{"00", "SA", "PO-1001", "", "20260101"}},
				{ID: "PO1", Elements: []string{"1", "10", "EA", "12.50", "PE", "VP", "ABC-1"}},
				{ID: "CTT", Elements: []string{"1"}},
			}}},
		}},
	}
}

func TestParse_RoundTripsSample(t *testing.T) {
	raw, err := Serialize(sampleInterchange())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "ISA*00*") || len(raw[:106]) != 106 {
		t.Fatalf("ISA header is not 106 characters: %q", raw[:min(len(raw), 120)])
	}
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(got, sampleInterchange()) {
		t.Fatalf("round trip changed the interchange:\n got %+v\nwant %+v", got, sampleInterchange())
	}
}

func TestParse_RejectsBrokenEnvelopes(t *testing.T) {
	good, err := Serialize(sampleInterchange())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  string
	}{
		{"not an ISA", "GS*PO~"},
		{"truncated before IEA", good[:strings.Index(good, "IEA")]},
		{"SE01 miscounts the segments", strings.Replace(good, "SE*5*0001", "SE*6*0001", 1)},
		{"SE02 differs from ST02", strings.Replace(good, "SE*5*0001", "SE*5*0002", 1)},
		{"GE02 differs from GS06", strings.Replace(good, "GE*1*42", "GE*1*43", 1)},
		{"IEA02 differs from ISA13", strings.Replace(good, "IEA*1*000000905", "IEA*1*000000906", 1)},
		{"IEA01 miscounts the groups", strings.Replace(good, "IEA*1*000000905", "IEA*2*000000905", 1)},
		{"content after IEA", good + "XX*1~"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.raw); !errors.Is(err, ErrSyntax) {
				t.Fatalf("want ErrSyntax, got %v", err)
			}
		})
	}
}

func TestParse_Version005010CarriesTheRepetitionSeparator(t *testing.T) {
	ic := sampleInterchange()
	ic.Version = "00501"
	ic.Delims.Repetition = '^'
	ic.Groups[0].Version = "005010"
	raw, err := Serialize(ic)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw[:106], "*^*00501*") {
		t.Fatalf("ISA11 must hold the repetition separator for 00501: %q", raw[:106])
	}
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, ic) {
		t.Fatalf("005010 round trip changed the interchange:\n got %+v\nwant %+v", got, ic)
	}
	ic.Groups[0].Transactions[0].Body[0].Elements[2] = "PO^1"
	if _, err := Serialize(ic); !errors.Is(err, ErrSyntax) {
		t.Fatalf("a value holding the repetition separator must be refused, got %v", err)
	}
}

func TestParse_RejectsUnsupportedVersion(t *testing.T) {
	raw, err := Serialize(sampleInterchange())
	if err != nil {
		t.Fatal(err)
	}
	raw = strings.Replace(raw, "*U*00401*", "*U*00305*", 1)
	if _, err := Parse(raw); !errors.Is(err, ErrSyntax) {
		t.Fatalf("ISA12 00305 must be refused, got %v", err)
	}
}

func TestSerialize_RefusesValuesThatContainDelimiters(t *testing.T) {
	ic := sampleInterchange()
	ic.Groups[0].Transactions[0].Body[0].Elements[2] = "PO*1"
	if _, err := Serialize(ic); !errors.Is(err, ErrSyntax) {
		t.Fatalf("want ErrSyntax for a value containing the element separator, got %v", err)
	}
}

func TestSerialize_RefusesFieldsOutsideTheirWidth(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Interchange)
	}{
		{"sender id longer than 15", func(ic *Interchange) { ic.Sender.ID = strings.Repeat("A", 16) }},
		{"control longer than 9", func(ic *Interchange) { ic.Control = "1234567890" }},
		{"usage not T or P", func(ic *Interchange) { ic.Usage = "X" }},
		{"non-distinct delimiters", func(ic *Interchange) { ic.Delims.Component = '*' }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ic := sampleInterchange()
			tc.edit(&ic)
			if _, err := Serialize(ic); !errors.Is(err, ErrSyntax) {
				t.Fatalf("want ErrSyntax, got %v", err)
			}
		})
	}
}

// genInterchange draws interchanges whose values never contain a delimiter and
// whose segment ids cannot collide with envelope segments (body ids start with X).
func genInterchange(t *rapid.T) Interchange {
	alnum := func(pattern string) string { return rapid.StringMatching(pattern).Draw(t, pattern) }
	ic := Interchange{
		Delims:   Delims{Element: '*', Component: '>', Segment: '~'},
		Sender:   Party{Qualifier: alnum(`[A-Z0-9]{2}`), ID: alnum(`[A-Z0-9]{1,15}`)},
		Receiver: Party{Qualifier: alnum(`[A-Z0-9]{2}`), ID: alnum(`[A-Z0-9]{1,15}`)},
		Date:     alnum(`[0-9]{6}`), Time: alnum(`[0-9]{4}`), Version: "00401",
		Control: alnum(`[0-9]{9}`), Usage: rapid.SampledFrom([]string{"T", "P"}).Draw(t, "usage"),
	}
	for g, gn := 0, rapid.IntRange(1, 2).Draw(t, "groups"); g < gn; g++ {
		grp := Group{FunctionalID: alnum(`[A-Z]{2}`), Sender: alnum(`[A-Z0-9]{1,15}`), Receiver: alnum(`[A-Z0-9]{1,15}`),
			Date: alnum(`[0-9]{8}`), Time: alnum(`[0-9]{4}`), Control: alnum(`[0-9]{1,9}`), Version: "004010"}
		for x, xn := 0, rapid.IntRange(0, 2).Draw(t, "transactions"); x < xn; x++ {
			tx := Transaction{SetID: alnum(`[0-9]{3}`), Control: alnum(`[0-9]{4}`)}
			for s, sn := 0, rapid.IntRange(0, 3).Draw(t, "segments"); s < sn; s++ {
				seg := Segment{ID: "X" + alnum(`[A-Z]{1,2}`), Elements: make([]string, rapid.IntRange(0, 4).Draw(t, "elements"))}
				for e := range seg.Elements {
					seg.Elements[e] = alnum(`[A-Z0-9 .]{0,8}`)
				}
				tx.Body = append(tx.Body, seg)
			}
			grp.Transactions = append(grp.Transactions, tx)
		}
		ic.Groups = append(ic.Groups, grp)
	}
	return ic
}

func TestParse_PropertyRoundTripIsByteEqualAfterNormalization(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ic := genInterchange(t)
		first, err := Serialize(ic)
		if err != nil {
			t.Fatalf("serialize generated interchange: %v", err)
		}
		parsed, err := Parse(first)
		if err != nil {
			t.Fatalf("parse serialized interchange: %v\n%s", err, first)
		}
		second, err := Serialize(parsed)
		if err != nil {
			t.Fatalf("re-serialize: %v", err)
		}
		if first != second {
			t.Fatalf("round trip is not byte-equal:\n%s\n%s", first, second)
		}
	})
}
