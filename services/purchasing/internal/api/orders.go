package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

type orderBody struct {
	LegalEntityID    string          `json:"legalEntityId"`
	VendorID         string          `json:"vendorId"`
	SiteID           string          `json:"siteId,omitempty"`
	WarehouseID      string          `json:"warehouseId,omitempty"`
	Currency         string          `json:"currency"`
	ChangeManagement *bool           `json:"changeManagement,omitempty"`
	Lines            []orderLineBody `json:"lines"`
	Charges          []chargeBody    `json:"charges,omitempty"`
	// ApplyAutoCharges adds the charges the active auto charges give this vendor and order (FR-17.9).
	ApplyAutoCharges bool `json:"applyAutoCharges,omitempty"`

	// keepCharges are the charges the PO already carries, by charge code id: an edit keeps the
	// calculation, value and sides they were entered with.
	keepCharges map[string]store.PurchaseOrderCharge
	// curSiteID and curWarehouseID are the stored header's on an edit: an existing line that
	// delivered to them follows the header when the edit moves it.
	curSiteID, curWarehouseID string
	// lineNos are the numbers the body's lines have (an edit numbers them before charges name them);
	// nil on create, where line i is number i+1.
	lineNos []int
	// autoApplied are the charge keys auto charges added to the PO before: applyAutoCharges on an
	// edit does not add them again, so a charge the buyer removed stays removed.
	autoApplied map[string]bool
}

type orderLineBody struct {
	// LineNo names the existing line a PATCH changes; omitted for a new line
	// and ignored on create, where lines are numbered in request order.
	LineNo      int    `json:"lineNo,omitempty"`
	ProductID   string `json:"productId,omitempty"`
	Description string `json:"description,omitempty"`
	// Category is the procurement category of the line (FR-17.20); a requisition-backed
	// line takes its requisition line's, and an existing line of a PATCH keeps its own.
	Category string `json:"category,omitempty"`
	// SiteID and WarehouseID are where the line is delivered; omitted, a new line goes where the PO
	// does and an existing line of a PATCH keeps its destination (following the header if it did).
	SiteID            string     `json:"siteId,omitempty"`
	WarehouseID       string     `json:"warehouseId,omitempty"`
	Qty               string     `json:"qty"`
	Unit              string     `json:"unit"`
	UnitPrice         string     `json:"unitPrice,omitempty"`
	PriceUnit         string     `json:"priceUnit,omitempty"`
	DiscountPct       string     `json:"discountPct,omitempty"`
	TaxAmount         string     `json:"taxAmount,omitempty"`
	DeliveryDate      domain.Day `json:"deliveryDate"`
	RequisitionLineID string     `json:"requisitionLineId,omitempty"`
	AgreementLineID   string     `json:"agreementLineId,omitempty"`

	// estimate is the requisition line's estimated unit price, set only by
	// consolidation: it prices the line when neither an agreement nor a trade
	// agreement does (source REQUISITION_ESTIMATE).
	estimate *decimal.Decimal
	// keep is the stored line a PATCH changes: when unitPrice is omitted and
	// the product and unit are unchanged, the line keeps its price and source.
	keep *store.PurchaseOrderLine
}

type chargeBody struct {
	// ChargeCodeID names a charge code (FR-17.9): calc and value then default to the code's and
	// the code's debit and credit sides, vendor and allocation rule are copied to the charge.
	ChargeCodeID string `json:"chargeCodeId,omitempty"`
	Calc         string `json:"calc,omitempty"`
	Value        string `json:"value,omitempty"`
	// LineNo makes it a line charge (FR-17.9): PER_UNIT takes that line's quantity and PERCENT that
	// line's amount after discount. On create it is the line's position (1-based); on an edit the
	// number of an existing line, or of a new one (the numbers after the highest on the order).
	LineNo int `json:"lineNo,omitempty"`
}

// lineNumbers returns the number of each body line.
func (b orderBody) lineNumbers() []int {
	if b.lineNos != nil {
		return b.lineNos
	}
	out := make([]int, len(b.Lines))
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func (a *API) createOrder(w http.ResponseWriter, r *http.Request) {
	if _, err := principal(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body orderBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	p, _ := principal(r.Context())
	if err := inScope(p, body.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	hash := hashBody("purchase-order.create", body)
	if err := a.orderCategories(r.Context(), &body); err != nil {
		fail(w, r, err)
		return
	}
	po, err := a.buildOrder(r.Context(), body)
	if err != nil {
		fail(w, r, err)
		return
	}
	// FR-17.20: every line with a category, against the PO vendor.
	warnings, err := a.checkOrderCategoryAccess(r.Context(), po, func(int) bool { return true })
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreatePurchaseOrder(r.Context(), po, store.Idem{Key: key, Hash: hash})
	if err != nil {
		fail(w, r, err)
		return
	}
	out.Warnings = append(out.Warnings, warnings...)
	a.ordersCreated.Add(1)
	w.Header().Set("ETag", etag(out.RowVersion))
	httpx.JSON(w, http.StatusCreated, out)
}

// buildOrder validates the body, fills missing unit prices from trade
// agreements (recording the source on the line), and prices the document.
// Totals come from domain.Price, so header totals satisfy the identity by construction.
func (a *API) buildOrder(ctx context.Context, body orderBody) (store.PurchaseOrder, error) {
	for _, f := range []struct {
		name, value string
		required    bool
	}{
		{"legalEntityId", body.LegalEntityID, true}, {"vendorId", body.VendorID, true},
		{"siteId", body.SiteID, false}, {"warehouseId", body.WarehouseID, false},
	} {
		if err := uuidField(f.name, f.value, f.required); err != nil {
			return store.PurchaseOrder{}, err
		}
	}
	cur, err := currencyField(body.Currency)
	if err != nil {
		return store.PurchaseOrder{}, err
	}
	if len(body.Lines) == 0 {
		return store.PurchaseOrder{}, invalid("lines", "needs at least one line")
	}
	// BR-17.3: the price date is the line creation date (the delivery-date
	// switch is a policy setting listed in NOT-IMPLEMENTED.md).
	orderDay := a.today()
	out := store.PurchaseOrder{
		LegalEntityID:    body.LegalEntityID,
		VendorID:         body.VendorID,
		SiteID:           body.SiteID,
		WarehouseID:      body.WarehouseID,
		Currency:         cur,
		ChangeManagement: true,
	}
	if body.ChangeManagement != nil {
		out.ChangeManagement = *body.ChangeManagement
	} else {
		// FR-17.13: the legal entity's policy sets the default.
		pol, err := a.store.GetPolicy(ctx, body.LegalEntityID)
		if err != nil {
			return store.PurchaseOrder{}, err
		}
		out.ChangeManagement = pol.ChangeManagementDefault
	}
	inputs := make([]domain.LineInput, 0, len(body.Lines))
	for i, l := range body.Lines {
		line, in, err := a.buildOrderLine(ctx, i, l, body.VendorID, cur, orderDay)
		if err != nil {
			return store.PurchaseOrder{}, err
		}
		line.SiteID, line.WarehouseID = lineDestination(body, l)
		out.Lines = append(out.Lines, line)
		inputs = append(inputs, in)
	}
	charges, sides, autoCodes, err := a.orderCharges(ctx, body, inputs)
	if err != nil {
		return store.PurchaseOrder{}, err
	}
	if err := priceDocument(&out, inputs, charges); err != nil {
		return store.PurchaseOrder{}, err
	}
	copyChargeSides(&out, sides)
	out.AutoChargeCodes = autoCodes
	return out, nil
}

// lineDestination is the site and warehouse a line is delivered to: its own, else for an existing
// line of an edit its stored destination (the new header's if it followed the old header), else
// the PO header's.
func lineDestination(body orderBody, l orderLineBody) (site, warehouse string) {
	site, warehouse = l.SiteID, l.WarehouseID
	if k := l.keep; k != nil {
		if site == "" && k.SiteID != body.curSiteID {
			site = k.SiteID
		}
		if warehouse == "" && k.WarehouseID != body.curWarehouseID {
			warehouse = k.WarehouseID
		}
	}
	if site == "" {
		site = body.SiteID
	}
	if warehouse == "" {
		warehouse = body.WarehouseID
	}
	return site, warehouse
}

// priceDocument fills line amounts, charge amounts and header totals of out from
// the domain pricing function (BR-17.5, FR-17.11). out.Lines must align with inputs.
func priceDocument(out *store.PurchaseOrder, inputs []domain.LineInput, charges []domain.ChargeInput) error {
	doc, err := domain.Price(inputs, charges, money.MinorUnits(out.Currency))
	if err != nil {
		return err
	}
	for i := range out.Lines {
		la := doc.Lines[i]
		out.Lines[i].GrossAmount = la.Gross
		out.Lines[i].DiscountAmount = la.Discount
		out.Lines[i].LineAmount = la.Amount
		out.Lines[i].TaxAmount = la.Tax
	}
	out.Charges = out.Charges[:0]
	for i, c := range charges {
		out.Charges = append(out.Charges, store.PurchaseOrderCharge{Seq: i + 1, Calc: c.Calc, Value: c.Value, Amount: doc.Charges[i]})
	}
	out.Subtotal = doc.Totals.Subtotal
	out.DiscountTotal = doc.Totals.DiscountTotal
	out.ChargesTotal = doc.Totals.ChargesTotal
	out.TaxTotal = doc.Totals.TaxTotal
	out.Total = doc.Totals.Total
	return nil
}

// buildOrderLine validates one line and resolves its price. A supplied unit
// price is MANUAL; otherwise the trade agreement winner for the vendor, product,
// quantity and order date sets the price and records TRADE_AGREEMENT:<id>.
func (a *API) buildOrderLine(ctx context.Context, idx int, l orderLineBody, vendor, cur string, orderDay domain.Day) (store.PurchaseOrderLine, domain.LineInput, error) {
	field := func(name string) string { return "lines[" + strconv.Itoa(idx) + "]." + name }
	if err := uuidField(field("productId"), l.ProductID, false); err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	if err := uuidField(field("siteId"), l.SiteID, false); err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	if err := uuidField(field("warehouseId"), l.WarehouseID, false); err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	if err := uuidField(field("requisitionLineId"), l.RequisitionLineID, false); err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	if err := uuidField(field("agreementLineId"), l.AgreementLineID, false); err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	if l.ProductID == "" && l.Description == "" {
		return store.PurchaseOrderLine{}, domain.LineInput{}, invalid(field("productId"), "or description is required")
	}
	if strings.TrimSpace(l.Unit) == "" {
		return store.PurchaseOrderLine{}, domain.LineInput{}, invalid(field("unit"), "is required")
	}
	if l.DeliveryDate.IsZero() {
		return store.PurchaseOrderLine{}, domain.LineInput{}, invalid(field("deliveryDate"), "is required")
	}
	qty, err := decimalField(field("qty"), l.Qty, "")
	if err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	if qty.Sign() <= 0 {
		return store.PurchaseOrderLine{}, domain.LineInput{}, invalid(field("qty"), "must be positive")
	}
	discount, err := decimalField(field("discountPct"), l.DiscountPct, "0")
	if err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	tax, err := decimalField(field("taxAmount"), l.TaxAmount, "0")
	if err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	priceUnit, err := decimalField(field("priceUnit"), l.PriceUnit, "1")
	if err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	if priceUnit.Sign() <= 0 {
		return store.PurchaseOrderLine{}, domain.LineInput{}, invalid(field("priceUnit"), "must be positive")
	}
	if discount.Sign() < 0 || discount.GreaterThan(decimal.NewFromInt(100)) {
		return store.PurchaseOrderLine{}, domain.LineInput{}, invalid(field("discountPct"), "must be from 0 to 100")
	}
	if tax.Sign() < 0 {
		return store.PurchaseOrderLine{}, domain.LineInput{}, invalid(field("taxAmount"), "must not be negative")
	}

	price, source, err := a.linePrice(ctx, l, vendor, cur, qty, orderDay, field)
	if err != nil {
		return store.PurchaseOrderLine{}, domain.LineInput{}, err
	}
	if source.unitPrice != nil {
		price = *source.unitPrice
	}
	if source.priceUnit != nil {
		priceUnit = *source.priceUnit
	}
	if source.discountPct != nil && strings.TrimSpace(l.DiscountPct) == "" {
		discount = *source.discountPct
	}
	in := domain.LineInput{Qty: qty, UnitPrice: price, PriceUnit: priceUnit, DiscountPct: discount, Tax: tax}
	line := store.PurchaseOrderLine{
		LineNo:            idx + 1,
		ProductID:         l.ProductID,
		Description:       l.Description,
		Category:          strings.TrimSpace(l.Category),
		Qty:               qty,
		Unit:              strings.TrimSpace(l.Unit),
		UnitPrice:         price,
		PriceUnit:         priceUnit,
		DiscountPct:       discount,
		DeliveryDate:      l.DeliveryDate,
		PriceSource:       source.label,
		RequisitionLineID: l.RequisitionLineID,
		AgreementLineID:   l.AgreementLineID,
	}
	return line, in, nil
}

// lineSource is the price a line took and where it came from.
type lineSource struct {
	label       string
	unitPrice   *decimal.Decimal
	priceUnit   *decimal.Decimal
	discountPct *decimal.Decimal
}

// linePrice returns, in order: the explicit price (MANUAL); on an edit, the
// stored price of an unchanged line with its source; the price of the
// referenced agreement line when it overrides trade agreements (FR-17.5,
// AGREEMENT:<line id>, with its discount unless the line gives one); the trade
// agreement winner (TRADE_AGREEMENT:<id>); or, for consolidated lines, the
// requisition estimate (REQUISITION_ESTIMATE).
func (a *API) linePrice(ctx context.Context, l orderLineBody, vendor, cur string, qty decimal.Decimal, date domain.Day, field func(string) string) (decimal.Decimal, lineSource, error) {
	if l.UnitPrice != "" {
		p, err := decimalField(field("unitPrice"), l.UnitPrice, "")
		if err != nil {
			return decimal.Zero, lineSource{}, err
		}
		if p.Sign() < 0 {
			return decimal.Zero, lineSource{}, invalid(field("unitPrice"), "must not be negative")
		}
		return p, lineSource{label: "MANUAL"}, nil
	}
	if k := l.keep; k != nil && k.ProductID == l.ProductID && k.Unit == strings.TrimSpace(l.Unit) {
		price, unit, disc := k.UnitPrice, k.PriceUnit, k.DiscountPct
		return price, lineSource{label: k.PriceSource, unitPrice: &price, priceUnit: &unit, discountPct: &disc}, nil
	}
	if l.AgreementLineID != "" {
		agr, err := a.store.GetAgreementLine(ctx, l.AgreementLineID)
		if err != nil {
			return decimal.Zero, lineSource{}, err
		}
		if al := agr.Lines[0]; al.PriceOverride {
			price, unit, disc := al.UnitPrice, decimal.NewFromInt(1), al.DiscountPct
			return price, lineSource{label: "AGREEMENT:" + al.ID, unitPrice: &price, priceUnit: &unit, discountPct: &disc}, nil
		}
	}
	estimate := func() (decimal.Decimal, lineSource, error) {
		p := *l.estimate
		return p, lineSource{label: "REQUISITION_ESTIMATE", unitPrice: &p}, nil
	}
	if l.ProductID == "" {
		if l.estimate != nil {
			return estimate()
		}
		return decimal.Zero, lineSource{}, invalid(field("unitPrice"), "is required when the line has no product")
	}
	res, err := a.resolve(ctx, domain.Query{
		Relation:  domain.RelationPrice,
		VendorID:  vendor,
		ProductID: l.ProductID,
		Qty:       qty,
		Currency:  cur,
		Unit:      strings.TrimSpace(l.Unit),
		Date:      date,
	}, nil, nil)
	if err != nil {
		return decimal.Zero, lineSource{}, err
	}
	if res.Winner == nil && l.estimate != nil {
		return estimate()
	}
	if res.Winner == nil {
		return decimal.Zero, lineSource{}, httpx.NewProblem(http.StatusUnprocessableEntity, codeNoPrice,
			"no trade agreement price for "+l.ProductID+"; enter a unit price")
	}
	price, unit := res.Winner.Price, res.Winner.PriceUnit
	return price, lineSource{label: "TRADE_AGREEMENT:" + res.Winner.ID, unitPrice: &price, priceUnit: &unit}, nil
}

// resolve fills the vendor's and product's group memberships (plus any groups
// the caller names), loads the candidate rows and applies FR-17.6.
func (a *API) resolve(ctx context.Context, q domain.Query, vendorGroups, productGroups []string) (domain.Resolution, error) {
	vg, err := a.memberships.VendorGroups(ctx, q.VendorID)
	if err != nil {
		return domain.Resolution{}, err
	}
	pg, err := a.memberships.ProductGroups(ctx, q.ProductID)
	if err != nil {
		return domain.Resolution{}, err
	}
	q.VendorGroupIDs = append(vg, vendorGroups...)
	q.ProductGroupIDs = append(pg, productGroups...)
	rows, err := a.store.PriceCandidates(ctx, store.PriceFilter{Relation: q.Relation, Currency: q.Currency, Unit: q.Unit, Date: q.Date,
		VendorID: q.VendorID, VendorGroupIDs: q.VendorGroupIDs, ProductID: q.ProductID, ProductGroupIDs: q.ProductGroupIDs})
	if err != nil {
		return domain.Resolution{}, err
	}
	return domain.Resolve(rows, q), nil
}

func validApprovalStatus(s string) bool {
	switch s {
	case "DRAFT", "IN_REVIEW", "APPROVED", "REJECTED", "CONFIRMED", "FINALIZED", "CANCELLED":
		return true
	}
	return false
}

func (a *API) listOrders(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.PurchaseOrderFilter{ApprovalStatus: q.Get("approvalStatus"), VendorID: q.Get("vendorId")}
	if f.ApprovalStatus != "" && !validApprovalStatus(f.ApprovalStatus) {
		fail(w, r, invalid("approvalStatus", "must be DRAFT, IN_REVIEW, APPROVED, REJECTED, CONFIRMED, FINALIZED or CANCELLED"))
		return
	}
	if err := uuidField("vendorId", f.VendorID, false); err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListPurchaseOrders(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetPurchaseOrder(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.RowVersion))
	httpx.JSON(w, http.StatusOK, out)
}

// agreementBody is a trade agreement row (FR-17.6) in the create request.
type agreementBody struct {
	Relation    string      `json:"relation"`
	AccountCode string      `json:"accountCode"`
	AccountID   string      `json:"accountId,omitempty"`
	ItemCode    string      `json:"itemCode"`
	ItemID      string      `json:"itemId,omitempty"`
	Currency    string      `json:"currency"`
	Unit        string      `json:"unit"`
	FromQty     string      `json:"fromQty,omitempty"`
	ToQty       string      `json:"toQty,omitempty"`
	Price       string      `json:"price"`
	PriceUnit   string      `json:"priceUnit,omitempty"`
	ValidFrom   domain.Day  `json:"validFrom"`
	ValidTo     *domain.Day `json:"validTo,omitempty"`
	Consignment bool        `json:"consignment,omitempty"`
}

// agreementDTO is the wire form of a trade agreement row.
type agreementDTO struct {
	ID          string      `json:"id"`
	Relation    string      `json:"relation"`
	AccountCode string      `json:"accountCode"`
	AccountID   string      `json:"accountId,omitempty"`
	ItemCode    string      `json:"itemCode"`
	ItemID      string      `json:"itemId,omitempty"`
	Currency    string      `json:"currency"`
	Unit        string      `json:"unit"`
	FromQty     string      `json:"fromQty"`
	ToQty       *string     `json:"toQty,omitempty"`
	Price       string      `json:"price"`
	PriceUnit   string      `json:"priceUnit"`
	ValidFrom   domain.Day  `json:"validFrom"`
	ValidTo     *domain.Day `json:"validTo,omitempty"`
	Consignment bool        `json:"consignment"`
	Status      string      `json:"status"`
}

func toAgreementDTO(t domain.TradeAgreement) agreementDTO {
	dto := agreementDTO{
		ID: t.ID, Relation: t.Relation, AccountCode: t.AccountCode, AccountID: t.AccountID,
		ItemCode: t.ItemCode, ItemID: t.ItemID, Currency: t.Currency, Unit: t.Unit,
		FromQty: t.FromQty.String(), Price: t.Price.String(), PriceUnit: t.PriceUnit.String(),
		ValidFrom: t.ValidFrom, ValidTo: t.ValidTo, Consignment: t.Consignment, Status: t.Status,
	}
	if t.ToQty != nil {
		s := t.ToQty.String()
		dto.ToQty = &s
	}
	return dto
}

func (a *API) createAgreement(w http.ResponseWriter, r *http.Request) {
	if _, err := principal(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body agreementBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	row, err := buildAgreement(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateTradeAgreement(r.Context(), row, store.Idem{Key: key, Hash: hashBody("trade-agreement.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.agreementsCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, toAgreementDTO(out))
}

func (a *API) getAgreement(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetTradeAgreement(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAgreementDTO(out))
}

func (a *API) listAgreements(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.TradeAgreementFilter{AccountID: q.Get("accountId"), ItemID: q.Get("itemId"), Status: q.Get("status")}
	for _, c := range []struct{ name, value string }{{"accountId", f.AccountID}, {"itemId", f.ItemID}} {
		if err := uuidField(c.name, c.value, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	if f.Status != "" && f.Status != "DRAFT" && f.Status != domain.StatusActive && f.Status != "EXPIRED" {
		fail(w, r, invalid("status", "must be DRAFT, ACTIVE or EXPIRED"))
		return
	}
	page, err := a.store.ListTradeAgreements(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	dtos := make([]agreementDTO, 0, len(page.Items))
	for _, t := range page.Items {
		dtos = append(dtos, toAgreementDTO(t))
	}
	writePage(w, store.Page[agreementDTO]{Items: dtos, Next: page.Next})
}

// buildAgreement validates a trade agreement row. Only PRICE rows are accepted in
// this slice; account and item codes need an id exactly when they name a level.
func buildAgreement(b agreementBody) (domain.TradeAgreement, error) {
	if b.Relation != domain.RelationPrice {
		return domain.TradeAgreement{}, invalid("relation", "must be PRICE in this release")
	}
	if err := levelFields("account", b.AccountCode, b.AccountID, domain.AccountVendor, domain.AccountGroup, domain.AccountAll); err != nil {
		return domain.TradeAgreement{}, err
	}
	if err := levelFields("item", b.ItemCode, b.ItemID, domain.ItemProduct, domain.ItemGroup, domain.ItemAll); err != nil {
		return domain.TradeAgreement{}, err
	}
	cur, err := currencyField(b.Currency)
	if err != nil {
		return domain.TradeAgreement{}, err
	}
	if strings.TrimSpace(b.Unit) == "" {
		return domain.TradeAgreement{}, invalid("unit", "is required")
	}
	from, err := decimalField("fromQty", b.FromQty, "0")
	if err != nil {
		return domain.TradeAgreement{}, err
	}
	if from.Sign() < 0 {
		return domain.TradeAgreement{}, invalid("fromQty", "must not be negative")
	}
	var to *decimal.Decimal
	if b.ToQty != "" {
		t, err := decimalField("toQty", b.ToQty, "")
		if err != nil {
			return domain.TradeAgreement{}, err
		}
		if t.LessThanOrEqual(from) {
			return domain.TradeAgreement{}, invalid("toQty", "must be greater than fromQty")
		}
		to = &t
	}
	price, err := decimalField("price", b.Price, "")
	if err != nil {
		return domain.TradeAgreement{}, err
	}
	if price.Sign() < 0 {
		return domain.TradeAgreement{}, invalid("price", "must not be negative")
	}
	priceUnit, err := decimalField("priceUnit", b.PriceUnit, "1")
	if err != nil {
		return domain.TradeAgreement{}, err
	}
	if priceUnit.Sign() <= 0 {
		return domain.TradeAgreement{}, invalid("priceUnit", "must be positive")
	}
	if b.ValidFrom.IsZero() {
		return domain.TradeAgreement{}, invalid("validFrom", "is required")
	}
	if b.ValidTo != nil && b.ValidTo.Before(b.ValidFrom.Time) {
		return domain.TradeAgreement{}, invalid("validTo", "must not be before validFrom")
	}
	return domain.TradeAgreement{
		Relation: b.Relation, AccountCode: b.AccountCode, AccountID: b.AccountID,
		ItemCode: b.ItemCode, ItemID: b.ItemID, Currency: cur, Unit: strings.TrimSpace(b.Unit),
		FromQty: from, ToQty: to, Price: price, PriceUnit: priceUnit,
		ValidFrom: b.ValidFrom, ValidTo: b.ValidTo, Consignment: b.Consignment,
		Status: domain.StatusActive,
	}, nil
}

// uuidList validates a repeated query parameter of UUIDs (each value may also
// be a comma-separated list).
func uuidList(field string, raw []string) ([]string, error) {
	var out []string
	for _, v := range raw {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id == "" {
				continue
			}
			if !ids.Valid(id) {
				return nil, invalid(field, "must be UUIDs")
			}
			out = append(out, id)
		}
	}
	return out, nil
}

// levelFields checks a code/id pair: VENDOR/GROUP/PRODUCT need an id, ALL must not have one.
func levelFields(name, code, id, specific, group, all string) error {
	switch code {
	case specific, group:
		return uuidField(name+"Id", id, true)
	case all:
		if id != "" {
			return invalid(name+"Id", "must be empty when "+name+"Code is ALL")
		}
		return nil
	}
	return invalid(name+"Code", "must be "+specific+", "+group+" or "+all)
}

// priceResponse is the pricing lookup answer: the winner and the explain list.
type priceResponse struct {
	Winner     *agreementDTO      `json:"winner"`
	Candidates []domain.Candidate `json:"candidates"`
}

func (a *API) lookupPrice(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, f := range []struct{ name, value string }{{"vendor", q.Get("vendor")}, {"product", q.Get("product")}} {
		if err := uuidField(f.name, f.value, true); err != nil {
			fail(w, r, err)
			return
		}
	}
	vendorGroups, err := uuidList("vendorGroup", q["vendorGroup"])
	if err != nil {
		fail(w, r, err)
		return
	}
	productGroups, err := uuidList("productGroup", q["productGroup"])
	if err != nil {
		fail(w, r, err)
		return
	}
	qty, err := decimalField("qty", q.Get("qty"), "")
	if err != nil {
		fail(w, r, err)
		return
	}
	if qty.Sign() <= 0 {
		fail(w, r, invalid("qty", "must be positive"))
		return
	}
	date, err := domain.ParseDay(q.Get("date"))
	if err != nil {
		fail(w, r, invalid("date", "must be YYYY-MM-DD"))
		return
	}
	cur, err := currencyField(q.Get("currency"))
	if err != nil {
		fail(w, r, err)
		return
	}
	unit := strings.TrimSpace(q.Get("unit"))
	if unit == "" {
		fail(w, r, invalid("unit", "is required"))
		return
	}
	tie := q.Get("tie")
	if !domain.ValidTie(tie) {
		fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeBadTie, "tie must be LATEST_FROM_DATE or LOWEST_PRICE"))
		return
	}
	res, err := a.resolve(r.Context(), domain.Query{
		Relation:  domain.RelationPrice,
		VendorID:  q.Get("vendor"),
		ProductID: q.Get("product"),
		Qty:       qty,
		Currency:  cur,
		Unit:      unit,
		Date:      date,
		Tie:       tie,
	}, vendorGroups, productGroups)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.priceLookups.Add(1)
	resp := priceResponse{Candidates: res.Candidates}
	if res.Winner != nil {
		dto := toAgreementDTO(*res.Winner)
		resp.Winner = &dto
	}
	httpx.JSON(w, http.StatusOK, resp)
}
