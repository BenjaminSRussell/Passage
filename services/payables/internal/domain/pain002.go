package domain

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
)

const (
	CodeStatusRejected = "AP-STS-001"
	CodeStatusUnknown  = "AP-STS-002"
	HoldBankDetails    = "BANK_DETAILS"

	maxPain002Bytes = 10 << 20
	statusRejected  = "RJCT"
	reasonBadAcct   = "AC01"
	unknownMessage  = "message"
	unknownPayment  = "payment"
)

var bankStatusTargets = map[string]string{
	"ACCP": PaymentAccepted,
	"ACSP": PaymentAccepted,
	"ACSC": PaymentSettledAtBank,
	"RJCT": PaymentRejected,
}

type KnownPayment struct {
	Status string
	Method string
}

type KnownIDs struct {
	Messages map[string][]string
	Payments map[string]KnownPayment
}

type StatusWarning struct {
	Payment string
	Code    string
	Text    string
}

type StatusOutcome struct {
	EndToEndID      string
	BankStatus      string
	ReasonCode      string
	ReasonText      string
	From            string
	To              string
	Hold            string
	TransitionError *Error
	Warning         *StatusWarning
}

type UnknownRef struct {
	Code string
	Kind string
	ID   string
}

type StatusReport struct {
	MessageID         string
	OriginalMessageID string
	Outcomes          []StatusOutcome
	Unknown           []UnknownRef
}

type xmlStatusDocument struct {
	XMLName xml.Name           `xml:"urn:iso:std:iso:20022:tech:xsd:pain.002.001.10 Document"`
	Report  xmlStatusReportDoc `xml:"CstmrPmtStsRpt"`
}

type xmlStatusReportDoc struct {
	MessageID string               `xml:"GrpHdr>MsgId"`
	Group     xmlOriginalGroup     `xml:"OrgnlGrpInfAndSts"`
	Payments  []xmlOriginalPayment `xml:"OrgnlPmtInfAndSts"`
}

type xmlOriginalGroup struct {
	MessageID string            `xml:"OrgnlMsgId"`
	Status    string            `xml:"GrpSts"`
	Reasons   []xmlStatusReason `xml:"StsRsnInf"`
}

type xmlOriginalPayment struct {
	Status       string            `xml:"PmtInfSts"`
	Reasons      []xmlStatusReason `xml:"StsRsnInf"`
	Transactions []xmlStatusTx     `xml:"TxInfAndSts"`
}

type xmlStatusTx struct {
	EndToEndID string            `xml:"OrgnlEndToEndId"`
	Status     string            `xml:"TxSts"`
	Reasons    []xmlStatusReason `xml:"StsRsnInf"`
}

type xmlStatusReason struct {
	Code string   `xml:"Rsn>Cd"`
	Info []string `xml:"AddtlInf"`
}

type statusReason struct {
	code string
	text string
}

func firstReason(reasons []xmlStatusReason) statusReason {
	if len(reasons) == 0 {
		return statusReason{}
	}
	r := reasons[0]
	return statusReason{code: strings.TrimSpace(r.Code), text: strings.TrimSpace(strings.Join(r.Info, " "))}
}

type statusBuilder struct {
	known    KnownIDs
	outcomes map[string]StatusOutcome
}

func (b *statusBuilder) record(e2e, bankStatus string, reason statusReason) {
	if prior, seen := b.outcomes[e2e]; seen && (prior.BankStatus == statusRejected || bankStatus != statusRejected) {
		return
	}
	known := b.known.Payments[e2e]
	out := StatusOutcome{EndToEndID: e2e, BankStatus: bankStatus, ReasonCode: reason.code, ReasonText: reason.text, To: bankStatusTargets[bankStatus]}
	if out.To == PaymentRejected {
		out.Warning = &StatusWarning{
			Payment: e2e, Code: CodeStatusRejected,
			Text: fmt.Sprintf("Payment %s rejected by bank (%s: %s); items re-opened", e2e, reason.code, reason.text),
		}
		if reason.code == reasonBadAcct {
			out.Hold = HoldBankDetails
		}
	}
	if known.Status != "" && out.To != "" {
		out.From = known.Status
		if err := TransitionPayment(out.From, out.To, known.Method); err != nil {
			out.TransitionError = asDomainError(err)
		}
	}
	b.outcomes[e2e] = out
}

func asDomainError(err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{Code: CodeValidation, Detail: err.Error()}
}

func ParsePain002(content []byte, known KnownIDs) (StatusReport, error) {
	var doc xmlStatusDocument
	if len(content) > maxPain002Bytes || xml.Unmarshal(content, &doc) != nil ||
		doc.Report.MessageID == "" || doc.Report.Group.MessageID == "" {
		return StatusReport{}, invalid("document is not a pain.002.001.10 status report")
	}
	report := doc.Report
	group := report.Group
	groupStatus := strings.TrimSpace(group.Status)
	groupReason := firstReason(group.Reasons)
	b := &statusBuilder{known: known, outcomes: map[string]StatusOutcome{}}
	unknown := map[UnknownRef]bool{}

	if _, ok := known.Messages[group.MessageID]; !ok {
		unknown[UnknownRef{Code: CodeStatusUnknown, Kind: unknownMessage, ID: group.MessageID}] = true
	}
	for _, inf := range report.Payments {
		infStatus := strings.TrimSpace(inf.Status)
		infReason := firstReason(inf.Reasons)
		for _, tx := range inf.Transactions {
			e2e := strings.TrimSpace(tx.EndToEndID)
			if e2e == "" {
				continue
			}
			if _, ok := known.Payments[e2e]; !ok {
				unknown[UnknownRef{Code: CodeStatusUnknown, Kind: unknownPayment, ID: e2e}] = true
				continue
			}
			status, reason := strings.TrimSpace(tx.Status), firstReason(tx.Reasons)
			switch {
			case status != "":
			case infStatus == statusRejected:
				status, reason = statusRejected, infReason
			case groupStatus == statusRejected:
				status, reason = statusRejected, groupReason
			}
			b.record(e2e, status, reason)
		}
	}
	if groupStatus == statusRejected {
		for _, e2e := range known.Messages[group.MessageID] {
			if _, seen := b.outcomes[e2e]; !seen {
				b.record(e2e, statusRejected, groupReason)
			}
		}
	}

	out := StatusReport{MessageID: report.MessageID, OriginalMessageID: group.MessageID}
	for _, o := range b.outcomes {
		out.Outcomes = append(out.Outcomes, o)
	}
	sort.Slice(out.Outcomes, func(i, j int) bool { return out.Outcomes[i].EndToEndID < out.Outcomes[j].EndToEndID })
	for ref := range unknown {
		out.Unknown = append(out.Unknown, ref)
	}
	sort.Slice(out.Unknown, func(i, j int) bool {
		if out.Unknown[i].Kind != out.Unknown[j].Kind {
			return out.Unknown[i].Kind < out.Unknown[j].Kind
		}
		return out.Unknown[i].ID < out.Unknown[j].ID
	})
	return out, nil
}
