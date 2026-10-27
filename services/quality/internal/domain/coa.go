package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// SpecDefault names the specification a CoA prints today: the limits of the
// order's test group. Customer and country specification sets (FR-33.21,
// BR-33.17) are a later slice.
const SpecDefault = "DEFAULT"

// maxCoAReason bounds the amendment reason of a CoA revision.
const maxCoAReason = 500

// CoARequest asks for a certificate of analysis of a closed quality order
// (FR-33.9). ShipmentID and CustomerID are optional: a CoA without a shipment is
// the batch CoA; each shipment has its own revision chain. Reason is required for
// every revision after the first (BR-33.10).
type CoARequest struct {
	QualityOrderID string `json:"qualityOrderId"`
	ShipmentID     string `json:"shipmentId,omitempty"`
	CustomerID     string `json:"customerId,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// ValidateCoARequest returns field errors for a CoA request, empty when valid.
func ValidateCoARequest(req CoARequest) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(req.QualityOrderID) {
		fields["qualityOrderId"] = "must be a UUID"
	}
	if req.ShipmentID != "" && !ids.Valid(req.ShipmentID) {
		fields["shipmentId"] = "must be a UUID when given"
	}
	if req.CustomerID != "" && !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID when given"
	}
	if len(strings.TrimSpace(req.Reason)) > maxCoAReason {
		fields["reason"] = "must be 500 characters or fewer"
	}
	return fields
}

// CoALine is one printed result: the test with its specification (limits of the
// test group) and the result that counted for the decision.
type CoALine struct {
	TestID       string           `json:"testId"`
	TestName     string           `json:"testName"`
	Kind         string           `json:"kind"`
	Required     bool             `json:"required"`
	Decimals     int              `json:"decimals"`
	Min          *decimal.Decimal `json:"min,omitempty"`
	Max          *decimal.Decimal `json:"max,omitempty"`
	SampleNo     int              `json:"sampleNo"`
	RetestNo     int              `json:"retestNo"`
	Value        *decimal.Decimal `json:"value,omitempty"`
	OutcomeID    string           `json:"outcomeId,omitempty"`
	Passed       bool             `json:"passed"`
	InstrumentID string           `json:"instrumentId,omitempty"`
	RecordedBy   string           `json:"recordedBy"`
	RecordedAt   time.Time        `json:"recordedAt"`
}

// CoA is one immutable revision of a certificate of analysis (FR-33.9, BR-33.10).
// The signature block is the usage-decision user and time; IssuedBy is who
// generated the document. A revision after the first supersedes the one before
// it in the same (order, shipment) chain and records why.
type CoA struct {
	ID               string            `json:"id"`
	QualityOrderID   string            `json:"qualityOrderId"`
	ShipmentID       string            `json:"shipmentId,omitempty"`
	CustomerID       string            `json:"customerId,omitempty"`
	ItemID           string            `json:"itemId"`
	Batch            string            `json:"batch"`
	Dims             map[string]string `json:"dims"`
	UOM              string            `json:"uom"`
	LotQty           decimal.Decimal   `json:"lotQty"`
	AcceptQty        decimal.Decimal   `json:"acceptQty"`
	DeviationQty     decimal.Decimal   `json:"deviationQty"`
	Deviations       []string          `json:"deviations"`
	TestGroupID      string            `json:"testGroupId"`
	TestGroupCode    string            `json:"testGroupCode"`
	TestGroupVersion int               `json:"testGroupVersion"`
	SpecificationSet string            `json:"specificationSet"`
	Revision         int               `json:"revision"`
	Supersedes       string            `json:"supersedes,omitempty"`
	Reason           string            `json:"reason,omitempty"`
	Lines            []CoALine         `json:"lines"`
	DecisionID       string            `json:"decisionId"`
	SignedBy         string            `json:"signedBy"`
	SignedAt         time.Time         `json:"signedAt"`
	IssuedBy         string            `json:"issuedBy"`
	IssuedAt         time.Time         `json:"issuedAt"`
}

// BuildCoA assembles the next CoA revision. The order must be closed by a usage
// decision that released quantity (accept or accept with deviation). Only the
// results the decision was taken on are printed (BR-33.10): those recorded up to
// the decision, the latest retest of each test and sample, in test group order
// then sample. prev is the latest revision of the same chain, nil for the first;
// every later revision needs a reason.
func BuildCoA(o Order, g TestGroup, d *Decision, results []Result, prev *CoA, req CoARequest, issuer string, now time.Time) (CoA, error) {
	if o.Status != StatusClosed || d == nil {
		return CoA{}, fail(CodeCoANotReleasable, "a certificate of analysis needs a quality order closed by its usage decision, not "+o.Status)
	}
	released := d.Split.Accept.Add(d.Split.Deviation)
	if !released.IsPositive() {
		return CoA{}, fail(CodeCoANotReleasable, "the usage decision released no quantity, so no certificate of analysis can be issued")
	}
	reason := strings.TrimSpace(req.Reason)
	if prev != nil && reason == "" {
		return CoA{}, fail(CodeCoAReasonRequired, "a certificate of analysis already exists; give the reason for the new revision")
	}
	order := map[string]int{}
	for i, l := range g.Tests {
		order[l.TestID] = i
	}
	decided := []Result{}
	for _, r := range results {
		if _, ok := order[r.TestID]; ok && (d.DecidedAt.IsZero() || !r.RecordedAt.After(d.DecidedAt)) {
			decided = append(decided, r)
		}
	}
	lines := make([]CoALine, 0, len(decided))
	for _, r := range effectiveResults(decided) {
		l := g.Tests[order[r.TestID]]
		lines = append(lines, CoALine{TestID: l.TestID, TestName: l.Name, Kind: l.Kind, Required: l.Required, Decimals: l.Decimals,
			Min: l.Min, Max: l.Max, SampleNo: r.SampleNo, RetestNo: r.RetestNo, Value: r.Value, OutcomeID: r.OutcomeID,
			Passed: r.Passed, InstrumentID: r.InstrumentID, RecordedBy: r.RecordedBy, RecordedAt: r.RecordedAt})
	}
	sort.Slice(lines, func(i, j int) bool {
		if a, b := order[lines[i].TestID], order[lines[j].TestID]; a != b {
			return a < b
		}
		return lines[i].SampleNo < lines[j].SampleNo
	})
	deviations := []string{}
	for _, l := range d.Lines {
		if l.Decision == DecisionAcceptWithDeviation && strings.TrimSpace(l.Reason) != "" {
			deviations = append(deviations, strings.TrimSpace(l.Reason))
		}
	}
	dims := map[string]string{}
	for k, v := range o.Dims {
		dims[k] = v
	}
	doc := CoA{QualityOrderID: o.ID, ShipmentID: req.ShipmentID, CustomerID: req.CustomerID, ItemID: o.ItemID, Batch: o.Dims["batch"],
		Dims: dims, UOM: o.UOM, LotQty: o.LotQty, AcceptQty: d.Split.Accept, DeviationQty: d.Split.Deviation, Deviations: deviations,
		TestGroupID: g.ID, TestGroupCode: g.Code, TestGroupVersion: g.Version, SpecificationSet: SpecDefault, Revision: 1,
		Reason: reason, Lines: lines, DecisionID: d.ID, SignedBy: d.DecidedBy, SignedAt: d.DecidedAt.UTC(), IssuedBy: issuer, IssuedAt: now.UTC()}
	if prev != nil {
		doc.Revision, doc.Supersedes = prev.Revision+1, prev.ID
	}
	return doc, nil
}
