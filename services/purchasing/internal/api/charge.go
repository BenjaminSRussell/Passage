package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// maxAutoCharges bounds the charges a PO takes from auto charges, so a misconfigured set of auto
// charges cannot bury a document.
const maxAutoCharges = 20

// chargeCodeBody is the POST body of a charge code. PUT takes the same fields; code and
// legalEntityId are ignored there because a code does not change.
type chargeCodeBody struct {
	LegalEntityID   string `json:"legalEntityId"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Calc            string `json:"calc"`
	DefaultValue    string `json:"defaultValue,omitempty"`
	DebitType       string `json:"debitType,omitempty"`
	CreditType      string `json:"creditType,omitempty"`
	DebitAccountID  string `json:"debitAccountId,omitempty"`
	CreditAccountID string `json:"creditAccountId,omitempty"`
	VendorID        string `json:"vendorId,omitempty"`
	AllocationRule  string `json:"allocationRule,omitempty"`
	Status          string `json:"status,omitempty"`
}

// chargeCodeFrom validates a body into a charge code; omitted sides default to the PRD's: debited to the
// item, payable to the vendor, allocated by amount, ACTIVE, default value 0.
func chargeCodeFrom(body chargeCodeBody) (domain.ChargeCode, error) {
	for _, f := range []struct{ name, v string }{{"debitAccountId", body.DebitAccountID},
		{"creditAccountId", body.CreditAccountID}, {"vendorId", body.VendorID}} {
		if err := uuidField(f.name, f.v, false); err != nil {
			return domain.ChargeCode{}, err
		}
	}
	value, err := decimalField("defaultValue", body.DefaultValue, "0")
	if err != nil {
		return domain.ChargeCode{}, err
	}
	c := domain.NormalizeChargeCode(domain.ChargeCode{LegalEntityID: body.LegalEntityID, Code: body.Code, Name: body.Name,
		Calc: body.Calc, DefaultValue: value, DebitType: orDefault(body.DebitType, domain.ChargeSideItem),
		CreditType: orDefault(body.CreditType, domain.ChargeSideVendor), DebitAccountID: body.DebitAccountID,
		CreditAccountID: body.CreditAccountID, VendorID: body.VendorID,
		AllocationRule: orDefault(body.AllocationRule, domain.AllocateAmount), Status: orDefault(body.Status, domain.ChargeActive)})
	if err := c.Validate(); err != nil {
		return domain.ChargeCode{}, err
	}
	return c, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (a *API) createChargeCode(w http.ResponseWriter, r *http.Request) {
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
	var body chargeCodeBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if err := uuidField("legalEntityId", body.LegalEntityID, true); err != nil {
		fail(w, r, err)
		return
	}
	c, err := chargeCodeFrom(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, c.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateChargeCode(r.Context(), c, store.Idem{Key: key, Hash: hashBody("charge-code.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.chargeCodesSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) getChargeCode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetChargeCode(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listChargeCodes(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.ChargeCodeFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status"), Text: strings.TrimSpace(q.Get("q"))}
	if err := uuidField("legalEntityId", f.LegalEntityID, false); err != nil {
		fail(w, r, err)
		return
	}
	if f.Status != "" && !domain.ValidChargeStatus(f.Status) {
		fail(w, r, invalid("status", "must be ACTIVE or INACTIVE"))
		return
	}
	page, err := a.store.ListChargeCodes(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// changeChargeCode handles PUT /charge-codes/{id}: it replaces everything but the code and the legal entity
// under If-Match.
func (a *API) changeChargeCode(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body chargeCodeBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetChargeCode(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	body.LegalEntityID, body.Code = cur.LegalEntityID, cur.Code
	c, err := chargeCodeFrom(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.UpdateChargeCode(r.Context(), id, want, c)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.chargeCodesSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// autoChargeBody is the POST body of an auto charge. PUT takes value, minOrderAmount, currency,
// validFrom, validTo and status; the charge code and the key do not change.
type autoChargeBody struct {
	LegalEntityID  string      `json:"legalEntityId"`
	ChargeCodeID   string      `json:"chargeCodeId"`
	AccountCode    string      `json:"accountCode"`
	AccountID      string      `json:"accountId,omitempty"`
	Value          string      `json:"value,omitempty"`
	MinOrderAmount string      `json:"minOrderAmount,omitempty"`
	Currency       string      `json:"currency,omitempty"`
	ValidFrom      *domain.Day `json:"validFrom,omitempty"`
	ValidTo        *domain.Day `json:"validTo,omitempty"`
	Status         string      `json:"status,omitempty"`
}

func optionalDecimal(field, raw string) (*decimal.Decimal, error) {
	if raw == "" {
		return nil, nil
	}
	d, err := decimalField(field, raw, "")
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// autoChargeFrom validates a body against its charge code; a missing validFrom is today.
func (a *API) autoChargeFrom(body autoChargeBody, code domain.ChargeCode) (domain.AutoCharge, error) {
	if err := uuidField("accountId", body.AccountID, false); err != nil {
		return domain.AutoCharge{}, err
	}
	value, err := optionalDecimal("value", body.Value)
	if err != nil {
		return domain.AutoCharge{}, err
	}
	min, err := optionalDecimal("minOrderAmount", body.MinOrderAmount)
	if err != nil {
		return domain.AutoCharge{}, err
	}
	cur := ""
	if body.Currency != "" {
		if cur, err = currencyField(body.Currency); err != nil {
			return domain.AutoCharge{}, err
		}
	}
	from := a.today()
	if body.ValidFrom != nil {
		from = *body.ValidFrom
	}
	ac := domain.AutoCharge{LegalEntityID: code.LegalEntityID, ChargeCodeID: code.ID, AccountCode: body.AccountCode,
		AccountID: body.AccountID, Value: value, MinOrderAmount: min, Currency: cur, ValidFrom: from, ValidTo: body.ValidTo,
		Status: orDefault(body.Status, domain.ChargeActive)}
	if err := ac.Validate(code); err != nil {
		return domain.AutoCharge{}, err
	}
	return ac, nil
}

func (a *API) createAutoCharge(w http.ResponseWriter, r *http.Request) {
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
	var body autoChargeBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	for _, f := range []struct{ name, v string }{{"legalEntityId", body.LegalEntityID}, {"chargeCodeId", body.ChargeCodeID}} {
		if err := uuidField(f.name, f.v, true); err != nil {
			fail(w, r, err)
			return
		}
	}
	if err := inScope(p, body.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	code, err := a.store.GetChargeCode(r.Context(), body.ChargeCodeID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if code.LegalEntityID != body.LegalEntityID || code.Status != domain.ChargeActive {
		fail(w, r, &domain.Error{Code: domain.CodeChargeInactive, Detail: "charge code " + code.Code + " is inactive or belongs to another legal entity"})
		return
	}
	ac, err := a.autoChargeFrom(body, code)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateAutoCharge(r.Context(), ac, store.Idem{Key: key, Hash: hashBody("auto-charge.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.autoChargesSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) getAutoCharge(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetAutoCharge(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listAutoCharges(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.AutoChargeFilter{LegalEntityID: q.Get("legalEntityId"), ChargeCodeID: q.Get("chargeCodeId"), Status: q.Get("status")}
	for _, u := range []struct{ name, v string }{{"legalEntityId", f.LegalEntityID}, {"chargeCodeId", f.ChargeCodeID}} {
		if err := uuidField(u.name, u.v, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	if f.Status != "" && !domain.ValidChargeStatus(f.Status) {
		fail(w, r, invalid("status", "must be ACTIVE or INACTIVE"))
		return
	}
	page, err := a.store.ListAutoCharges(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// changeAutoCharge handles PUT /auto-charges/{id} under If-Match.
func (a *API) changeAutoCharge(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body autoChargeBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetAutoCharge(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	code, err := a.store.GetChargeCode(r.Context(), cur.ChargeCodeID)
	if err != nil {
		fail(w, r, err)
		return
	}
	body.AccountCode, body.AccountID = cur.AccountCode, cur.AccountID
	ac, err := a.autoChargeFrom(body, code)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.UpdateAutoCharge(r.Context(), id, want, ac)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.autoChargesSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// orderCharges turns the charges of a PO body into priced inputs and the sides each carries (FR-17.9).
// A charge with a chargeCodeId takes its calculation and value from the code unless the body gives a
// value (a calc given must be the code's, PRC-CHG-005) and copies the code's sides; the code must be
// ACTIVE and of the PO's legal entity (PRC-CHG-004), unless the PO already carries that code: an edit
// keeps the calculation, value and sides it was entered with. With applyAutoCharges the active auto
// charges that apply to the vendor, currency and order total add their codes, once each: a code among
// the charges, or one an auto charge added to the PO before (and the buyer may since have removed), is
// not added again. It also returns the codes the auto charges added.
func (a *API) orderCharges(ctx context.Context, body orderBody, inputs []domain.LineInput) ([]domain.ChargeInput, []store.PurchaseOrderCharge, []string, error) {
	charges := make([]domain.ChargeInput, 0, len(body.Charges))
	sides := make([]store.PurchaseOrderCharge, 0, len(body.Charges))
	have := map[string]bool{}
	for i, c := range body.Charges {
		field := "charges[" + strconv.Itoa(i) + "]."
		if err := uuidField(field+"chargeCodeId", c.ChargeCodeID, false); err != nil {
			return nil, nil, nil, err
		}
		in, side, err := a.chargeFromBody(ctx, field, c, body)
		if err != nil {
			return nil, nil, nil, err
		}
		if c.ChargeCodeID != "" {
			have[c.ChargeCodeID] = true
		}
		charges, sides = append(charges, in), append(sides, side)
	}
	if !body.ApplyAutoCharges {
		return charges, sides, nil, nil
	}
	cur := strings.ToUpper(body.Currency)
	// The minimum order amount is compared with the lines after discount, before any charge, in the
	// PO currency; Applies skips an auto charge in another currency.
	doc, err := domain.Price(inputs, nil, money.MinorUnits(cur))
	if err != nil {
		return nil, nil, nil, err
	}
	base := doc.Totals.Subtotal.Sub(doc.Totals.DiscountTotal)
	groups, err := a.memberships.VendorGroups(ctx, body.VendorID)
	if err != nil {
		return nil, nil, nil, err
	}
	today := a.today()
	autos, err := a.store.ActiveAutoCharges(ctx, body.LegalEntityID, body.VendorID, groups, today)
	if err != nil {
		return nil, nil, nil, err
	}
	var added []string
	for _, ac := range autos {
		if have[ac.ChargeCodeID] || body.autoApplied[ac.ChargeCodeID] || !ac.Applies(body.VendorID, groups, cur, base, today) {
			continue
		}
		code, err := a.store.GetChargeCode(ctx, ac.ChargeCodeID)
		if err != nil {
			return nil, nil, nil, err
		}
		if code.Status != domain.ChargeActive {
			continue
		}
		if len(added) == maxAutoCharges {
			return nil, nil, nil, invalid("applyAutoCharges", "adds more than "+strconv.Itoa(maxAutoCharges)+" charges; deactivate auto charges that overlap")
		}
		calc, value := ac.Resolve(code)
		have[code.ID] = true
		added = append(added, code.ID)
		charges = append(charges, domain.ChargeInput{Calc: calc, Value: value})
		side := sidesOf(code)
		side.Auto = true
		sides = append(sides, side)
	}
	return charges, sides, added, nil
}

// sidesOf copies the code's identity and sides into a PO charge.
func sidesOf(code domain.ChargeCode) store.PurchaseOrderCharge {
	return store.PurchaseOrderCharge{ChargeCodeID: code.ID, ChargeCode: code.Code, DebitType: code.DebitType,
		CreditType: code.CreditType, DebitAccountID: code.DebitAccountID, CreditAccountID: code.CreditAccountID,
		VendorID: code.VendorID, AllocationRule: code.AllocationRule}
}

// chargeFromBody prices one entered charge.
func (a *API) chargeFromBody(ctx context.Context, field string, c chargeBody, body orderBody) (domain.ChargeInput, store.PurchaseOrderCharge, error) {
	if c.ChargeCodeID == "" {
		if !domain.ValidChargeCalc(c.Calc) {
			return domain.ChargeInput{}, store.PurchaseOrderCharge{}, invalid(field+"calc", "must be FIXED, PER_UNIT or PERCENT")
		}
		v, err := decimalField(field+"value", c.Value, "")
		if err != nil {
			return domain.ChargeInput{}, store.PurchaseOrderCharge{}, err
		}
		if v.Sign() < 0 {
			return domain.ChargeInput{}, store.PurchaseOrderCharge{}, invalid(field+"value", "must not be negative")
		}
		return domain.ChargeInput{Calc: c.Calc, Value: v}, store.PurchaseOrderCharge{}, nil
	}
	var side store.PurchaseOrderCharge
	var calc string
	var value decimal.Decimal
	if kept, ok := body.keepCharges[c.ChargeCodeID]; ok {
		// The PO already carries this code: keep the calculation, value and sides it was entered
		// with, even if the code has since been changed or deactivated.
		side, calc, value = kept, kept.Calc, kept.Value
	} else {
		code, err := a.store.GetChargeCode(ctx, c.ChargeCodeID)
		if err != nil {
			return domain.ChargeInput{}, store.PurchaseOrderCharge{}, err
		}
		if code.LegalEntityID != body.LegalEntityID || code.Status != domain.ChargeActive {
			return domain.ChargeInput{}, store.PurchaseOrderCharge{}, &domain.Error{Code: domain.CodeChargeInactive,
				Detail: field + "chargeCodeId: charge code " + code.Code + " is inactive or belongs to another legal entity"}
		}
		side, calc, value = sidesOf(code), code.Calc, code.DefaultValue
	}
	if c.Calc != "" && c.Calc != calc {
		return domain.ChargeInput{}, store.PurchaseOrderCharge{}, &domain.Error{Code: domain.CodeChargeCalcMismatch,
			Detail: field + "calc: charge code " + side.ChargeCode + " calculates " + calc + ", not " + c.Calc}
	}
	if c.Value != "" {
		v, err := decimalField(field+"value", c.Value, "")
		if err != nil {
			return domain.ChargeInput{}, store.PurchaseOrderCharge{}, err
		}
		value = v
	}
	if value.Sign() < 0 {
		return domain.ChargeInput{}, store.PurchaseOrderCharge{}, invalid(field+"value", "must not be negative")
	}
	return domain.ChargeInput{Calc: calc, Value: value}, side, nil
}

// copyChargeSides sets the code identity and sides of out's charges from sides, which align by index.
func copyChargeSides(out *store.PurchaseOrder, sides []store.PurchaseOrderCharge) {
	for i := range out.Charges {
		if i >= len(sides) {
			return
		}
		s := sides[i]
		c := &out.Charges[i]
		c.ChargeCodeID, c.ChargeCode, c.DebitType, c.CreditType = s.ChargeCodeID, s.ChargeCode, s.DebitType, s.CreditType
		c.DebitAccountID, c.CreditAccountID, c.VendorID, c.AllocationRule, c.Auto = s.DebitAccountID, s.CreditAccountID, s.VendorID, s.AllocationRule, s.Auto
	}
}

// registerChargeRoutes mounts the FR-17.9 setup routes under /v1/procurement.
func (a *API) registerChargeRoutes(r chi.Router) {
	write := r.With(authz.RequireDuty(dutyPolicyMaintain))
	read := r.With(authz.Require(anyDuty{dutyPolicyMaintain, dutyPurchaseOrderInquire},
		dutyPolicyMaintain+" or "+dutyPurchaseOrderInquire, ""))
	write.Post("/charge-codes", a.createChargeCode)
	read.Get("/charge-codes", a.listChargeCodes)
	read.Get("/charge-codes/{id}", a.getChargeCode)
	write.Put("/charge-codes/{id}", a.changeChargeCode)
	write.Post("/auto-charges", a.createAutoCharge)
	read.Get("/auto-charges", a.listAutoCharges)
	read.Get("/auto-charges/{id}", a.getAutoCharge)
	write.Put("/auto-charges/{id}", a.changeAutoCharge)
}
