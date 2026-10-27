package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// maxAgreementLines caps the lines of one purchase agreement, like requisitions.
const maxAgreementLines = 500

// codeTooManyAgreementLines rejects an agreement with more than maxAgreementLines.
const codeTooManyAgreementLines = "PRC-AGR-002"

// purchaseAgreementBody is the create request for a blanket purchase agreement
// (FR-17.5). Status defaults to EFFECTIVE; DRAFT keeps it out of releases until
// it is activated.
type purchaseAgreementBody struct {
	LegalEntityID string                      `json:"legalEntityId"`
	VendorID      string                      `json:"vendorId"`
	Currency      string                      `json:"currency"`
	Status        string                      `json:"status,omitempty"`
	ValidFrom     domain.Day                  `json:"validFrom"`
	ValidTo       *domain.Day                 `json:"validTo,omitempty"`
	Lines         []purchaseAgreementLineBody `json:"lines"`
}

type purchaseAgreementLineBody struct {
	CommitmentType  string `json:"commitmentType"`
	ProductID       string `json:"productId,omitempty"`
	Category        string `json:"category,omitempty"`
	Unit            string `json:"unit,omitempty"`
	CommittedQty    string `json:"committedQty,omitempty"`
	CommittedAmount string `json:"committedAmount,omitempty"`
	UnitPrice       string `json:"unitPrice,omitempty"`
	DiscountPct     string `json:"discountPct,omitempty"`
	MaxEnforced     string `json:"maxEnforced,omitempty"`
	PriceOverride   bool   `json:"priceOverride,omitempty"`
}

// purchaseAgreementDTO is the wire form of an agreement with release tracking.
type purchaseAgreementDTO struct {
	ID            string                     `json:"id"`
	Number        string                     `json:"number"`
	LegalEntityID string                     `json:"legalEntityId"`
	VendorID      string                     `json:"vendorId"`
	Currency      string                     `json:"currency"`
	Status        string                     `json:"status"`
	ValidFrom     domain.Day                 `json:"validFrom"`
	ValidTo       *domain.Day                `json:"validTo,omitempty"`
	Version       int                        `json:"version"`
	Lines         []purchaseAgreementLineDTO `json:"lines"`
	Releases      []store.AgreementRelease   `json:"releases"`
	CreatedAt     time.Time                  `json:"createdAt"`
	UpdatedAt     time.Time                  `json:"updatedAt"`
}

// purchaseAgreementLineDTO is one line with its release position. PctReleased
// and Remaining are measured on the commitment type (quantity for QUANTITY,
// amount otherwise).
type purchaseAgreementLineDTO struct {
	ID              string `json:"id"`
	LineNo          int    `json:"lineNo"`
	CommitmentType  string `json:"commitmentType"`
	ProductID       string `json:"productId,omitempty"`
	Category        string `json:"category,omitempty"`
	Unit            string `json:"unit,omitempty"`
	CommittedQty    string `json:"committedQty"`
	CommittedAmount string `json:"committedAmount"`
	UnitPrice       string `json:"unitPrice"`
	DiscountPct     string `json:"discountPct"`
	MaxEnforced     string `json:"maxEnforced"`
	PriceOverride   bool   `json:"priceOverride"`
	ReleasedQty     string `json:"releasedQty"`
	ReleasedAmount  string `json:"releasedAmount"`
	PctReleased     string `json:"pctReleased"`
	Remaining       string `json:"remaining"`
}

func toPurchaseAgreementDTO(d store.AgreementDetail) purchaseAgreementDTO {
	a := d.Agreement
	dto := purchaseAgreementDTO{
		ID: a.ID, Number: a.Number, LegalEntityID: a.LegalEntityID, VendorID: a.VendorID, Currency: a.Currency, Status: a.Status,
		ValidFrom: a.ValidFrom, ValidTo: a.ValidTo, Version: a.Version, Lines: make([]purchaseAgreementLineDTO, 0, len(a.Lines)),
		Releases: d.Releases, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if dto.Releases == nil {
		dto.Releases = []store.AgreementRelease{}
	}
	for _, l := range a.Lines {
		dto.Lines = append(dto.Lines, purchaseAgreementLineDTO{
			ID: l.ID, LineNo: l.LineNo, CommitmentType: l.CommitmentType, ProductID: l.ProductID, Category: l.Category, Unit: l.Unit,
			CommittedQty: l.CommittedQty.String(), CommittedAmount: l.CommittedAmount.String(), UnitPrice: l.UnitPrice.String(),
			DiscountPct: l.DiscountPct.String(), MaxEnforced: l.MaxEnforced, PriceOverride: l.PriceOverride,
			ReleasedQty: l.ReleasedQty.String(), ReleasedAmount: l.ReleasedAmount.String(),
			PctReleased: l.PctReleased().String(), Remaining: l.Remaining().String(),
		})
	}
	return dto
}

func (a *API) createPurchaseAgreement(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body purchaseAgreementBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	agr, err := buildPurchaseAgreement(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, agr.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateAgreement(r.Context(), agr, store.Idem{Key: key, Hash: hashBody("purchase-agreement.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.purchaseAgreementsCreated.Add(1)
	w.Header().Set("ETag", etag(out.Agreement.Version))
	httpx.JSON(w, http.StatusCreated, toPurchaseAgreementDTO(out))
}

func (a *API) getPurchaseAgreement(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetAgreement(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Agreement.Version))
	httpx.JSON(w, http.StatusOK, toPurchaseAgreementDTO(out))
}

func validAgreementStatus(s string) bool {
	switch s {
	case domain.AgreementDraft, domain.AgreementEffective, domain.AgreementOnHold, domain.AgreementClosed:
		return true
	}
	return false
}

func (a *API) listPurchaseAgreements(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.AgreementFilter{Status: q.Get("status"), VendorID: q.Get("vendorId")}
	if f.Status != "" && !validAgreementStatus(f.Status) {
		fail(w, r, invalid("status", "must be DRAFT, EFFECTIVE, ON_HOLD or CLOSED"))
		return
	}
	if err := uuidField("vendorId", f.VendorID, false); err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListAgreements(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// purchaseAgreementAction handles POST /agreements/{id}:activate, :hold and
// :close with If-Match (version) and an Idempotency-Key.
func (a *API) purchaseAgreementAction(w http.ResponseWriter, r *http.Request) {
	if _, err := principal(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	switch action {
	case domain.AgreementActivate, domain.AgreementHold, domain.AgreementClose:
	default:
		found = false
	}
	if !found || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown agreement action"))
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	idem := store.Idem{Key: key, Hash: hashBody("purchase-agreement."+action, map[string]any{"id": id, "version": version})}
	out, err := a.store.ChangeAgreementStatus(r.Context(), id, version, action, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Agreement.Version))
	httpx.JSON(w, http.StatusOK, toPurchaseAgreementDTO(out))
}

// buildPurchaseAgreement validates a create request and returns the domain
// agreement. Lines are numbered in request order by the store.
func buildPurchaseAgreement(body purchaseAgreementBody) (domain.Agreement, error) {
	if err := uuidField("legalEntityId", body.LegalEntityID, true); err != nil {
		return domain.Agreement{}, err
	}
	if err := uuidField("vendorId", body.VendorID, true); err != nil {
		return domain.Agreement{}, err
	}
	cur, err := currencyField(body.Currency)
	if err != nil {
		return domain.Agreement{}, err
	}
	if body.ValidFrom.IsZero() {
		return domain.Agreement{}, invalid("validFrom", "is required")
	}
	if body.ValidTo != nil && body.ValidTo.Before(body.ValidFrom.Time) {
		return domain.Agreement{}, invalid("validTo", "must not be before validFrom")
	}
	status := body.Status
	switch status {
	case "":
		status = domain.AgreementEffective
	case domain.AgreementDraft, domain.AgreementEffective:
	default:
		return domain.Agreement{}, invalid("status", "must be DRAFT or EFFECTIVE")
	}
	if len(body.Lines) == 0 {
		return domain.Agreement{}, invalid("lines", "needs at least one line")
	}
	if len(body.Lines) > maxAgreementLines {
		return domain.Agreement{}, httpx.NewProblem(http.StatusUnprocessableEntity, codeTooManyAgreementLines,
			"agreement has more than "+strconv.Itoa(maxAgreementLines)+" lines")
	}
	out := domain.Agreement{LegalEntityID: body.LegalEntityID, VendorID: body.VendorID, Currency: cur,
		ValidFrom: body.ValidFrom, ValidTo: body.ValidTo, Status: status}
	for i, l := range body.Lines {
		line, err := buildPurchaseAgreementLine(i, l)
		if err != nil {
			return domain.Agreement{}, err
		}
		out.Lines = append(out.Lines, line)
	}
	return out, nil
}

func buildPurchaseAgreementLine(i int, l purchaseAgreementLineBody) (domain.AgreementLine, error) {
	field := func(n string) string { return "lines[" + strconv.Itoa(i) + "]." + n }
	if err := uuidField(field("productId"), l.ProductID, false); err != nil {
		return domain.AgreementLine{}, err
	}
	line := domain.AgreementLine{
		LineNo: i + 1, CommitmentType: l.CommitmentType, ProductID: l.ProductID,
		Category: strings.TrimSpace(l.Category), Unit: strings.TrimSpace(l.Unit), PriceOverride: l.PriceOverride,
		MaxEnforced: l.MaxEnforced,
	}
	for _, f := range []struct {
		name, raw string
		dst       *decimal.Decimal
	}{
		{"committedQty", l.CommittedQty, &line.CommittedQty}, {"committedAmount", l.CommittedAmount, &line.CommittedAmount},
		{"unitPrice", l.UnitPrice, &line.UnitPrice}, {"discountPct", l.DiscountPct, &line.DiscountPct},
	} {
		v, err := decimalField(field(f.name), strings.TrimSpace(f.raw), "0")
		if err != nil {
			return domain.AgreementLine{}, err
		}
		if v.IsNegative() {
			return domain.AgreementLine{}, invalid(field(f.name), "must not be negative")
		}
		*f.dst = v
	}
	if line.DiscountPct.GreaterThan(decimal.NewFromInt(100)) {
		return domain.AgreementLine{}, invalid(field("discountPct"), "must be from 0 to 100")
	}
	switch line.MaxEnforced {
	case "":
		line.MaxEnforced = domain.EnforceHard
	case domain.EnforceHard, domain.EnforceSoft, domain.EnforceNone:
	default:
		return domain.AgreementLine{}, invalid(field("maxEnforced"), "must be HARD, SOFT or NONE")
	}
	if name, msg := domain.ValidateAgreementLine(line); name != "" {
		return domain.AgreementLine{}, invalid(field(name), msg)
	}
	if line.PriceOverride {
		// FR-17.5: the agreement price replaces the trade agreement price of a
		// product, so only product commitments can carry one.
		if line.CommitmentType != domain.CommitQuantity && line.CommitmentType != domain.CommitValue {
			return domain.AgreementLine{}, invalid(field("priceOverride"), "needs a QUANTITY or VALUE line for a product")
		}
		if !line.UnitPrice.IsPositive() {
			return domain.AgreementLine{}, invalid(field("unitPrice"), "must be positive when priceOverride is set")
		}
	}
	return line, nil
}
