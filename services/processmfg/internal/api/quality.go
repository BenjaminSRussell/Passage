package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
)

// DutyConsumeExpired is "Consume expired batch" (PRD 4.24.17): a picking
// proposal includes expired batches only for a caller holding it (BR-24.10).
const DutyConsumeExpired = "production.batch.consume-expired"

// Bounds of one slice-3 request.
const (
	maxDefinitions = 100
	maxOptions     = 50
	maxReasonLen   = 500
)

// CodeBatchRegistered refuses the caller-supplied picking proposal for a batch
// the batch register holds: its disposition and expiry are on record, so it is
// proposed with batch-picking:propose-stored (not in the PRD catalog).
const CodeBatchRegistered = "PMF-0112"

// registerQuality mounts the slice-3 previews: the attribute check (FR-24.5),
// the picking proposal (FR-24.3, FR-24.5, FR-24.6, BR-24.10) and the rework
// plan (FR-24.18). They store nothing; the picking proposal over the stored
// batch register is pickstored.go.
func (a *API) registerQuality(r chi.Router) {
	r.With(authz.RequireDuty(DutyBatchAttributeWrite)).Post("/v1/production/batch-attributes:check", handle(checkAttributes))
	r.With(authz.RequireDuty(DutyRead)).Post("/v1/production/batch-picking:proposal", a.proposePicking)
	r.With(authz.RequireDuty(DutyBatchOrderWrite)).Post("/v1/production/rework-orders:preview", handle(previewRework))
}

type attributeDefDTO struct {
	Name    string           `json:"name"`
	Kind    string           `json:"kind"`
	Target  *decimal.Decimal `json:"target,omitempty"`
	Min     *decimal.Decimal `json:"min,omitempty"`
	Max     *decimal.Decimal `json:"max,omitempty"`
	Options []string         `json:"options,omitempty"`
}

type attributesRequest struct {
	Definitions []attributeDefDTO   `json:"definitions"`
	Values      map[string]valueDTO `json:"values"`
}

type attributeResultDTO struct {
	Attribute string `json:"attribute"`
	Pass      bool   `json:"pass"`
	Detail    string `json:"detail,omitempty"`
}

type attributesResponse struct {
	Results []attributeResultDTO `json:"results"`
	// Exceptions counts values outside their limits; each becomes an EBR
	// exception (BR-24.15) when the values are recorded.
	Exceptions int `json:"exceptions"`
}

func boundedDecimal(field string, v *decimal.Decimal) error {
	if v != nil && (v.Exponent() < -domain.QtyPrecision || v.Abs().GreaterThanOrEqual(maxQty())) {
		return invalidf("%s must be below 10^12 with at most 6 decimals", field)
	}
	return nil
}

// checkAttributes checks recorded batch attribute values against the item's
// definitions (FR-24.5): wrong types, undefined attributes and enum values that
// are not options are refused; numeric values outside min/max pass through
// flagged, because they are recorded and reviewed as EBR exceptions.
func checkAttributes(in attributesRequest) (attributesResponse, error) {
	if len(in.Definitions) > maxDefinitions || len(in.Values) > maxDefinitions {
		return attributesResponse{}, invalidf("at most %d definitions and %d values", maxDefinitions, maxDefinitions)
	}
	defs := make([]domain.AttributeDef, len(in.Definitions))
	for i, d := range in.Definitions {
		field := fmt.Sprintf("definitions[%d]", i)
		if n := len(d.Name); n < 1 || n > maxAttrName {
			return attributesResponse{}, invalidf("%s.name must be 1 to %d characters", field, maxAttrName)
		}
		if len(d.Options) > maxOptions {
			return attributesResponse{}, invalidf("%s has more than %d options", field, maxOptions)
		}
		for name, v := range map[string]*decimal.Decimal{"target": d.Target, "min": d.Min, "max": d.Max} {
			if err := boundedDecimal(field+"."+name, v); err != nil {
				return attributesResponse{}, err
			}
		}
		defs[i] = domain.AttributeDef{Name: d.Name, Kind: d.Kind, Target: d.Target, Min: d.Min, Max: d.Max, Options: d.Options}
	}
	values := make(map[string]domain.Value, len(in.Values))
	for name, v := range in.Values {
		dv, err := v.toValue("values." + name)
		if err != nil {
			return attributesResponse{}, err
		}
		values[name] = dv
	}
	checks, err := domain.CheckAttributes(defs, values)
	if err != nil {
		return attributesResponse{}, err
	}
	out := attributesResponse{Results: make([]attributeResultDTO, len(checks))}
	for i, c := range checks {
		out.Results[i] = attributeResultDTO{Attribute: c.Name, Pass: c.Pass, Detail: c.Detail}
		if !c.Pass {
			out.Exceptions++
		}
	}
	return out, nil
}

type potencyBandDTO struct {
	Attribute string          `json:"attribute"`
	Min       decimal.Decimal `json:"min"`
	Max       decimal.Decimal `json:"max"`
}

type pickingRequest struct {
	PostingDate    string          `json:"postingDate"`
	Qty            decimal.Decimal `json:"qty"`
	Potency        *potencyBandDTO `json:"potency,omitempty"`
	OverrideReason string          `json:"overrideReason,omitempty"`
	Batches        []batchDTO      `json:"batches"`
}

type pickedDTO struct {
	BatchID         string          `json:"batchId"`
	Qty             decimal.Decimal `json:"qty"`
	Expiry          *string         `json:"expiry,omitempty"`
	ExpiredOverride bool            `json:"expiredOverride"`
}

type pickExcludedDTO struct {
	BatchID string `json:"batchId"`
	Code    string `json:"code"`
	Detail  string `json:"detail"`
}

type pickingResponse struct {
	Picked    []pickedDTO       `json:"picked"`
	Excluded  []pickExcludedDTO `json:"excluded"`
	Shortfall decimal.Decimal   `json:"shortfall"`
	// OverrideHeld says whether the caller holds the consume-expired duty and
	// gave a reason, so expired batches were eligible.
	OverrideHeld bool `json:"overrideHeld"`
}

// proposePicking proposes the batches a line consumes (FR-24.3, FR-24.5,
// FR-24.6) from batches the caller describes: available batches only
// (on-hold, quarantine and rejected are excluded with PMF-0016, AC-24.10),
// expiry first, potency inside the line's band (PMF-0005), and expired
// batches excluded with PMF-0003 (AC-24.4) unless the caller holds "Consume
// expired batch" and gives a reason of at least ten characters (BR-24.10).
// Batches picked on the override are flagged, because each override is an EBR
// exception (BR-24.15). It is a what-if preview for batches that are not in
// the batch register: a registered batch is refused with PMF-0112, so its
// stored hold or expiry cannot be bypassed by restating it here.
func (a *API) proposePicking(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var in pickingRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	batches, err := toBatches(in.Batches)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := a.refuseRegistered(r.Context(), batches); err != nil {
		fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	out, err := pickBatches(in, batches, p.HasDuty(DutyConsumeExpired))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// refuseRegistered fails with PMF-0112 when any batch id is a batch of the
// register in the caller's scope.
func (a *API) refuseRegistered(ctx context.Context, batches []domain.Batch) error {
	uuids := make([]string, 0, len(batches))
	for _, b := range batches {
		if id, ok := ids.Canonical(b.ID); ok {
			uuids = append(uuids, id)
		}
	}
	if len(uuids) == 0 {
		return nil
	}
	found, err := a.store.RegisteredBatches(ctx, uuids)
	if err != nil || len(found) == 0 {
		return err
	}
	return httpx.NewProblem(http.StatusConflict, CodeBatchRegistered, fmt.Sprintf(
		"batch %s is in the batch register; propose it with batch-picking:propose-stored so its stored disposition, expiry and attributes apply", found[0]))
}

// pickBatches applies the picking rules to batches already converted, whether
// the caller passed them in or they were read from the batch register.
func pickBatches(in pickingRequest, batches []domain.Batch, dutyHeld bool) (pickingResponse, error) {
	posting, err := parseDate("postingDate", in.PostingDate)
	if err != nil {
		return pickingResponse{}, err
	}
	if !validQty(in.Qty) {
		return pickingResponse{}, invalidf("qty must be a positive quantity below 10^12 with at most 6 decimals")
	}
	if len(in.OverrideReason) > maxReasonLen {
		return pickingResponse{}, invalidf("overrideReason must be at most %d characters", maxReasonLen)
	}
	line := domain.PickLine{PostingDate: posting}
	if b := in.Potency; b != nil {
		if n := len(b.Attribute); n < 1 || n > maxAttrName {
			return pickingResponse{}, invalidf("potency.attribute must be 1 to %d characters", maxAttrName)
		}
		for name, v := range map[string]decimal.Decimal{"min": b.Min, "max": b.Max} {
			if err := boundedDecimal("potency."+name, &v); err != nil {
				return pickingResponse{}, err
			}
		}
		if b.Min.GreaterThan(b.Max) {
			return pickingResponse{}, invalidf("potency.min must not exceed potency.max")
		}
		line.Potency = &domain.PotencyBand{Attribute: b.Attribute, Min: b.Min, Max: b.Max}
	}
	// The override needs both the duty and a stated reason; a reason without
	// the duty changes nothing, and expired batches stay excluded (PMF-0003).
	ov := domain.ExpiryOverride{DutyHeld: dutyHeld && in.OverrideReason != "", Reason: in.OverrideReason}
	prop, err := domain.ProposePicking(batches, line, in.Qty, ov)
	if err != nil {
		return pickingResponse{}, err
	}
	out := pickingResponse{Picked: make([]pickedDTO, len(prop.Picked)), Excluded: make([]pickExcludedDTO, len(prop.Excluded)),
		Shortfall: prop.Shortfall, OverrideHeld: ov.DutyHeld}
	for i, pk := range prop.Picked {
		out.Picked[i] = pickedDTO{BatchID: pk.BatchID, Qty: pk.Qty, ExpiredOverride: pk.ExpiredOverride}
		if pk.Expiry != nil {
			s := pk.Expiry.Format(dateLayout)
			out.Picked[i].Expiry = &s
		}
	}
	for i, e := range prop.Excluded {
		out.Excluded[i] = pickExcludedDTO{BatchID: e.BatchID, Code: e.Code, Detail: e.Detail}
	}
	return out, nil
}

type reworkParamsDTO struct {
	KeepBatchNumber     *bool  `json:"keepBatchNumber,omitempty"`
	RequireQualityOrder *bool  `json:"requireQualityOrder,omitempty"`
	OutputDisposition   string `json:"outputDisposition,omitempty"`
	Capitalize          bool   `json:"capitalize,omitempty"`
}

type reworkRequest struct {
	Batch     batchDTO        `json:"batch"`
	ReworkQty decimal.Decimal `json:"reworkQty"`
	OutputQty decimal.Decimal `json:"outputQty"`
	Params    reworkParamsDTO `json:"params"`
}

type reworkResponse struct {
	SourceBatchID        string          `json:"sourceBatchId"`
	ReworkQty            decimal.Decimal `json:"reworkQty"`
	OutputBatchID        *string         `json:"outputBatchId"`
	OutputQty            decimal.Decimal `json:"outputQty"`
	OutputDisposition    string          `json:"outputDisposition"`
	QualityOrderRequired bool            `json:"qualityOrderRequired"`
	VarianceType         string          `json:"varianceType"`
	Parents              []string        `json:"parents"`
}

// previewRework plans a rework batch order (FR-24.18, AC-24.18): the batch or
// part of it as the first ingredient, the output batch (kept number by
// default, null for a new number at report as finished), the output
// disposition (quarantine by default), the quality order flag, the variance
// type of the rework cost and the genealogy parent. Omitted parameters take
// the FR-24.18 defaults.
func previewRework(in reworkRequest) (reworkResponse, error) {
	src, err := in.Batch.toBatch("batch")
	if err != nil {
		return reworkResponse{}, err
	}
	for name, v := range map[string]decimal.Decimal{"reworkQty": in.ReworkQty, "outputQty": in.OutputQty} {
		if !validQty(v) {
			return reworkResponse{}, invalidf("%s must be a positive quantity below 10^12 with at most 6 decimals", name)
		}
	}
	p := domain.DefaultReworkParams()
	if in.Params.KeepBatchNumber != nil {
		p.KeepBatchNumber = *in.Params.KeepBatchNumber
	}
	if in.Params.RequireQualityOrder != nil {
		p.RequireQualityOrder = *in.Params.RequireQualityOrder
	}
	if in.Params.OutputDisposition != "" {
		p.OutputDisposition = in.Params.OutputDisposition
	}
	p.Capitalize = in.Params.Capitalize
	plan, err := domain.PlanRework(src, in.ReworkQty, in.OutputQty, p)
	if err != nil {
		return reworkResponse{}, err
	}
	out := reworkResponse{SourceBatchID: plan.SourceBatchID, ReworkQty: plan.InputQty, OutputQty: plan.OutputQty,
		OutputDisposition: plan.OutputDisposition, QualityOrderRequired: plan.QualityOrderRequired,
		VarianceType: plan.VarianceType, Parents: plan.Parents}
	if plan.OutputBatchID != "" {
		id := plan.OutputBatchID
		out.OutputBatchID = &id
	}
	return out, nil
}
