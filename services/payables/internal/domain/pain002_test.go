package domain_test

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	pain002FixtureDir = "../../testdata/pain002"
	sepa50Message     = "SEPA50-F1"
)

func sepa50Known(t tester, statuses map[string]string) (domain.KnownIDs, []string) {
	t.Helper()
	in := loadFixture(t, "sepa-50")
	known := domain.KnownIDs{Messages: map[string][]string{}, Payments: map[string]domain.KnownPayment{}}
	ids := make([]string, len(in.Payments))
	for i, p := range in.Payments {
		status, set := statuses[p.Number]
		if !set {
			status = domain.PaymentSent
		}
		known.Payments[p.Number] = domain.KnownPayment{Status: status, Method: p.Method.Type}
		ids[i] = p.Number
	}
	known.Messages[sepa50Message] = ids
	return known, ids
}

func readPain002(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(pain002FixtureDir, name+".xml"))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func parsePain002(t *testing.T, content []byte, known domain.KnownIDs) domain.StatusReport {
	t.Helper()
	report, err := domain.ParsePain002(content, known)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

type wantOutcome struct {
	id, bank, to, from, code, text string
	hold, illegal                  bool
}

func requireOutcomes(t *testing.T, report domain.StatusReport, want []wantOutcome) {
	t.Helper()
	if len(report.Outcomes) != len(want) {
		t.Fatalf("got %d outcomes, want %d: %+v", len(report.Outcomes), len(want), report.Outcomes)
	}
	if !sort.SliceIsSorted(report.Outcomes, func(i, j int) bool { return report.Outcomes[i].EndToEndID < report.Outcomes[j].EndToEndID }) {
		t.Fatal("outcomes are not ordered by end-to-end id")
	}
	for i, w := range want {
		got := report.Outcomes[i]
		if got.EndToEndID != w.id || got.BankStatus != w.bank || got.To != w.to || got.From != w.from || got.ReasonCode != w.code || got.ReasonText != w.text {
			t.Fatalf("outcome %d = %+v, want %+v", i, got, w)
		}
		wantHold := ""
		if w.hold {
			wantHold = domain.HoldBankDetails
		}
		if got.Hold != wantHold {
			t.Fatalf("%s hold = %q, want %q", w.id, got.Hold, wantHold)
		}
		if w.illegal != (got.TransitionError != nil) || (w.illegal && got.TransitionError.Code != domain.CodeValidation) {
			t.Fatalf("%s transition error = %v, want illegal %v", w.id, got.TransitionError, w.illegal)
		}
		rejected := w.to == domain.PaymentRejected
		if rejected != (got.Warning != nil) {
			t.Fatalf("%s warning = %+v, rejected %v", w.id, got.Warning, rejected)
		}
		if rejected {
			wantText := "Payment " + w.id + " rejected by bank (" + w.code + ": " + w.text + "); items re-opened"
			if got.Warning.Payment != w.id || got.Warning.Code != domain.CodeStatusRejected || got.Warning.Text != wantText {
				t.Fatalf("%s warning = %+v, want text %q", w.id, got.Warning, wantText)
			}
		}
	}
}

func TestParsePain002_Fixtures(t *testing.T) {
	statuses := map[string]string{
		"SEPA-0002": "",
		"SEPA-0004": domain.PaymentAccepted,
		"SEPA-0007": domain.PaymentSettledAtBank,
		"SEPA-0008": domain.PaymentAccepted,
	}
	cases := []struct {
		file    string
		want    []wantOutcome
		unknown []domain.UnknownRef
	}{
		{"accp", []wantOutcome{
			{id: "SEPA-0001", bank: "ACCP", to: domain.PaymentAccepted, from: domain.PaymentSent},
			{id: "SEPA-0002", bank: "ACCP", to: domain.PaymentAccepted},
		}, nil},
		{"acsp", []wantOutcome{{id: "SEPA-0003", bank: "ACSP", to: domain.PaymentAccepted, from: domain.PaymentSent}}, nil},
		{"acsc", []wantOutcome{{id: "SEPA-0004", bank: "ACSC", to: domain.PaymentSettledAtBank, from: domain.PaymentAccepted}}, nil},
		{"pdng", []wantOutcome{{id: "SEPA-0005", bank: "PDNG"}}, nil},
		{"rjct-ac01", []wantOutcome{{id: "SEPA-0006", bank: "RJCT", to: domain.PaymentRejected, from: domain.PaymentSent, code: "AC01", text: "Incorrect account number", hold: true}}, nil},
		{"rjct-ac04", []wantOutcome{{id: "SEPA-0007", bank: "RJCT", to: domain.PaymentRejected, from: domain.PaymentSettledAtBank, code: "AC04", text: "Closed account number", illegal: true}}, nil},
		{"rjct-am04", []wantOutcome{{id: "SEPA-0008", bank: "RJCT", to: domain.PaymentRejected, from: domain.PaymentAccepted, code: "AM04", text: "Insufficient funds"}}, nil},
		{"pmtinf-rjct", []wantOutcome{
			{id: "SEPA-0011", bank: "RJCT", to: domain.PaymentRejected, from: domain.PaymentSent, code: "AG01", text: "Transaction forbidden"},
			{id: "SEPA-0012", bank: "RJCT", to: domain.PaymentRejected, from: domain.PaymentSent, code: "AG01", text: "Transaction forbidden"},
			{id: "SEPA-0013", bank: "ACCP", to: domain.PaymentAccepted, from: domain.PaymentSent},
		}, nil},
		{"mixed-unknown", []wantOutcome{
			{id: "SEPA-0020", bank: "ACCP", to: domain.PaymentAccepted, from: domain.PaymentSent},
			{id: "SEPA-0021", bank: "RJCT", to: domain.PaymentRejected, from: domain.PaymentSent, code: "AC01", text: "Incorrect account number", hold: true},
			{id: "SEPA-0022", bank: "ACSC", to: domain.PaymentSettledAtBank, from: domain.PaymentSent, illegal: true},
		}, []domain.UnknownRef{{Code: domain.CodeStatusUnknown, Kind: "payment", ID: "UNKNOWN-0001"}}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			known, _ := sepa50Known(t, statuses)
			report := parsePain002(t, readPain002(t, c.file), known)
			requireOutcomes(t, report, c.want)
			if report.OriginalMessageID != sepa50Message || !strings.HasPrefix(report.MessageID, "STS-") {
				t.Fatalf("message ids = %q / %q", report.MessageID, report.OriginalMessageID)
			}
			if len(report.Unknown) != len(c.unknown) {
				t.Fatalf("unknown = %+v, want %+v", report.Unknown, c.unknown)
			}
			for i, u := range c.unknown {
				if report.Unknown[i] != u {
					t.Fatalf("unknown[%d] = %+v, want %+v", i, report.Unknown[i], u)
				}
			}
		})
	}
}

func TestParsePain002_GroupRejectionRejectsEveryKnownPayment(t *testing.T) {
	known, ids := sepa50Known(t, nil)
	report := parsePain002(t, readPain002(t, "grp-rjct"), known)
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	want := make([]wantOutcome, len(sorted))
	for i, id := range sorted {
		want[i] = wantOutcome{id: id, bank: "RJCT", to: domain.PaymentRejected, from: domain.PaymentSent, code: "FF01", text: "File rejected by the bank"}
	}
	requireOutcomes(t, report, want)
}

func TestParsePain002_GroupRejectionKeepsTransactionOwnStatus(t *testing.T) {
	known, ids := sepa50Known(t, nil)
	content := strings.Replace(string(readPain002(t, "accp")), "</OrgnlMsgNmId>", "</OrgnlMsgNmId><GrpSts>RJCT</GrpSts>", 1)
	report := parsePain002(t, []byte(content), known)
	if len(report.Outcomes) != len(ids) {
		t.Fatalf("got %d outcomes, want %d", len(report.Outcomes), len(ids))
	}
	for _, o := range report.Outcomes {
		wantAccepted := o.EndToEndID == "SEPA-0001" || o.EndToEndID == "SEPA-0002"
		if wantAccepted != (o.To == domain.PaymentAccepted) || wantAccepted == (o.Warning != nil) {
			t.Fatalf("outcome %+v", o)
		}
	}
}

func TestParsePain002_UnknownMessageStillMapsKnownPayments(t *testing.T) {
	known, _ := sepa50Known(t, nil)
	content := strings.Replace(string(readPain002(t, "accp")), sepa50Message, "OTHER-MSG", 1)
	report := parsePain002(t, []byte(content), known)
	if len(report.Outcomes) != 2 || report.Outcomes[0].To != domain.PaymentAccepted {
		t.Fatalf("outcomes = %+v", report.Outcomes)
	}
	if want := (domain.UnknownRef{Code: domain.CodeStatusUnknown, Kind: "message", ID: "OTHER-MSG"}); len(report.Unknown) != 1 || report.Unknown[0] != want {
		t.Fatalf("unknown = %+v", report.Unknown)
	}
}

func TestParsePain002_EmptyKnownIDsMapsNothing(t *testing.T) {
	report := parsePain002(t, readPain002(t, "mixed-unknown"), domain.KnownIDs{})
	if len(report.Outcomes) != 0 || len(report.Unknown) != 5 {
		t.Fatalf("report = %+v", report)
	}
	for _, u := range report.Unknown {
		if u.Code != domain.CodeStatusUnknown {
			t.Fatalf("unknown = %+v", u)
		}
	}
}

func TestParsePain002_OtherIsoStatusesAreRecordedWithoutTransition(t *testing.T) {
	known, _ := sepa50Known(t, nil)
	for _, status := range []string{"RCVD", "ACTC", "ACWC", "PART", "XXXX"} {
		content := strings.ReplaceAll(string(readPain002(t, "accp")), "<TxSts>ACCP</TxSts>", "<TxSts>"+status+"</TxSts>")
		report := parsePain002(t, []byte(content), known)
		for _, o := range report.Outcomes {
			if o.BankStatus != status || o.To != "" || o.From != "" || o.Warning != nil || o.Hold != "" || o.TransitionError != nil {
				t.Fatalf("%s outcome = %+v", status, o)
			}
		}
		if len(report.Outcomes) != 2 {
			t.Fatalf("%s outcomes = %+v", status, report.Outcomes)
		}
	}
}

func TestParsePain002_RejectionWithoutReasonStillWarnsWithoutHold(t *testing.T) {
	known, _ := sepa50Known(t, nil)
	content := string(readPain002(t, "rjct-ac01"))
	start, end := strings.Index(content, "<StsRsnInf>"), strings.Index(content, "</StsRsnInf>")+len("</StsRsnInf>")
	report := parsePain002(t, []byte(content[:start]+content[end:]), known)
	if o := report.Outcomes[0]; o.To != domain.PaymentRejected || o.Warning == nil || o.Hold != "" || o.ReasonCode != "" {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestParsePain002_RejectsInvalidDocuments(t *testing.T) {
	known, _ := sepa50Known(t, nil)
	pain001 := baseInput(t)
	file, err := domain.GeneratePain001(pain001)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(readPain002(t, "accp"))
	cases := []struct {
		name    string
		content []byte
	}{
		{"not xml", []byte("<not-xml")},
		{"truncated", []byte(valid[:len(valid)/2])},
		{"empty", nil},
		{"pain.001 document", file.Content},
		{"wrong namespace", []byte(strings.Replace(valid, "pain.002.001.10", "pain.002.001.09", 1))},
		{"wrong root", []byte(strings.ReplaceAll(valid, "Document", "Envelope"))},
		{"missing message id", []byte(strings.Replace(valid, "<MsgId>STS-ACCP-1</MsgId>", "", 1))},
		{"oversized", append([]byte(valid), bytes.Repeat([]byte(" "), 10<<20)...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report, err := domain.ParsePain002(c.content, known)
			requireCode(t, err, domain.CodeValidation, "pain.002")
			if len(report.Outcomes) != 0 || len(report.Unknown) != 0 {
				t.Fatalf("report returned alongside an error: %+v", report)
			}
		})
	}
}

func TestValidateXSD_Pain002(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(pain002FixtureDir, "*.xml"))
	if err != nil || len(files) != 10 {
		t.Fatalf("pain.002 fixtures = %v (%v)", files, err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			content, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			validateXSD(t, pain002Schema, content)
		})
	}
}

type generatedReason struct {
	Code string `xml:"Rsn>Cd"`
}

type generatedTx struct {
	E2E    string           `xml:"OrgnlEndToEndId"`
	Status string           `xml:"TxSts,omitempty"`
	Reason *generatedReason `xml:"StsRsnInf,omitempty"`
}

type generatedInf struct {
	Status string        `xml:"PmtInfSts,omitempty"`
	Txs    []generatedTx `xml:"TxInfAndSts"`
}

type generatedStatusDoc struct {
	XMLName     xml.Name       `xml:"urn:iso:std:iso:20022:tech:xsd:pain.002.001.10 Document"`
	MsgID       string         `xml:"CstmrPmtStsRpt>GrpHdr>MsgId"`
	OrgnlMsgID  string         `xml:"CstmrPmtStsRpt>OrgnlGrpInfAndSts>OrgnlMsgId"`
	GroupStatus string         `xml:"CstmrPmtStsRpt>OrgnlGrpInfAndSts>GrpSts,omitempty"`
	Infs        []generatedInf `xml:"CstmrPmtStsRpt>OrgnlPmtInfAndSts"`
}

func TestParsePain002_Property(t *testing.T) {
	known, ids := sepa50Known(t, map[string]string{})
	statuses := []string{"", "ACCP", "ACSP", "ACSC", "PDNG", "RJCT"}
	rapid.Check(t, func(rt *rapid.T) {
		inDoc := rapid.SliceOfNDistinct(rapid.SampledFrom(ids), 0, 12, func(s string) string { return s }).Draw(rt, "known")
		strangers := rapid.SliceOfNDistinct(rapid.StringMatching(`UNK-[0-9]{3}`), 0, 4, func(s string) string { return s }).Draw(rt, "unknown")
		groupStatus := rapid.SampledFrom([]string{"", "ACCP", "RJCT"}).Draw(rt, "group")
		infStatus := rapid.SampledFrom([]string{"", "ACCP", "RJCT"}).Draw(rt, "inf")
		inf := generatedInf{Status: infStatus}
		wantRejected := map[string]bool{}
		for _, id := range append(append([]string(nil), inDoc...), strangers...) {
			tx := generatedTx{E2E: id, Status: rapid.SampledFrom(statuses).Draw(rt, "status")}
			if tx.Status == "RJCT" {
				tx.Reason = &generatedReason{Code: rapid.SampledFrom([]string{"AC01", "AC04", "AM04"}).Draw(rt, "reason")}
			}
			inf.Txs = append(inf.Txs, tx)
			if strings.HasPrefix(id, "UNK-") {
				continue
			}
			wantRejected[id] = tx.Status == "RJCT" || (tx.Status == "" && (infStatus == "RJCT" || groupStatus == "RJCT"))
		}
		wantIDs := inDoc
		if groupStatus == "RJCT" {
			wantIDs = ids
			for _, id := range ids {
				if _, listed := wantRejected[id]; !listed {
					wantRejected[id] = true
				}
			}
		}
		raw, err := xml.Marshal(generatedStatusDoc{MsgID: "STS-1", OrgnlMsgID: sepa50Message, GroupStatus: groupStatus, Infs: []generatedInf{inf}})
		if err != nil {
			rt.Fatal(err)
		}
		report, err := domain.ParsePain002(raw, known)
		if err != nil {
			rt.Fatal(err)
		}
		if len(report.Outcomes) != len(wantIDs) {
			rt.Fatalf("got %d outcomes for %d known ids", len(report.Outcomes), len(wantIDs))
		}
		seen := map[string]bool{}
		for _, o := range report.Outcomes {
			if seen[o.EndToEndID] || strings.HasPrefix(o.EndToEndID, "UNK-") {
				rt.Fatalf("duplicate or unknown outcome %+v", o)
			}
			seen[o.EndToEndID] = true
			rejected := o.To == domain.PaymentRejected
			if rejected != wantRejected[o.EndToEndID] || rejected != (o.Warning != nil && o.Warning.Code == domain.CodeStatusRejected) {
				rt.Fatalf("outcome %+v, want rejected %v", o, wantRejected[o.EndToEndID])
			}
			if o.Hold != "" && !(rejected && o.ReasonCode == "AC01") {
				rt.Fatalf("hold on %+v", o)
			}
		}
		if len(report.Unknown) != len(strangers) {
			rt.Fatalf("unknown = %+v, want %v", report.Unknown, strangers)
		}
	})
}
