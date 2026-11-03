package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Reservation, batch expiry and transaction inquiry routes (FR-16.7, FR-16.13).
const (
	dutyReserve       = "inventory.reservation.write"
	dutyBatchWrite    = "inventory.batch.write"
	reservationsPath  = "/v1/inventory/reservations"
	batchExpiriesPath = "/v1/inventory/batch-expiries"
	transactionsPath  = "/v1/inventory/transactions"
	candidatesPath    = "/v1/inventory/reservation-candidates"
	defaultPageSize   = 100
	maxPageSize       = 500
	maxShelfLifeDays  = 365
	maxTTLHours       = 8760
	maxIdentifierLen  = 60
	dateLayout        = "2006-01-02"
)

// inquirySourceTypes are the origin types the transaction inquiry filters by:
// the service's own documents and the external origins of transactions:apply.
var inquirySourceTypes = append([]string{"INVENT_JOURNAL_LINE", "BLOCKING", "TRANSFER_LINE", "CONSIGNMENT_LINE", "OWNERSHIP_CHANGE", "TRANSFER_IN"},
	store.ExternalSourceTypes...)

func (a *API) registerReservations(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get(transactionsPath, a.listTransactions)
	r.With(authz.RequireDuty(dutyRead)).Get(candidatesPath, a.listReservationCandidates)
	r.With(authz.RequireDuty(dutyReserve)).Post(reservationsPath, a.createReservation)
	r.With(authz.RequireDuty(dutyRead)).Get(reservationsPath, a.listReservations)
	r.With(authz.RequireDuty(dutyReserve)).Post(reservationsPath+":expire", a.expireReservations)
	r.With(authz.RequireDuty(dutyReserve)).Delete(reservationsPath+"/{id}", a.releaseReservation)
	r.With(authz.RequireDuty(dutyBatchWrite)).Put(batchExpiriesPath+"/{productId}/{batchId}", a.putBatchExpiry)
}

type reserveRequest struct {
	LegalEntityID       string            `json:"legalEntityId"`
	ProductID           string            `json:"productId"`
	Qty                 string            `json:"qty"`
	DemandType          string            `json:"demandType"`
	DemandID            string            `json:"demandId"`
	Policy              string            `json:"policy"`
	RequiredDate        string            `json:"requiredDate"`
	ShelfLifeMarginDays int               `json:"shelfLifeMarginDays"`
	AllowPartial        bool              `json:"allowPartial"`
	Dimensions          map[string]string `json:"dimensions"`
	// SegmentType and SegmentRef name the segment of the demand (FR-16.20);
	// SegmentFallback (default true) lets it take unsegmented stock.
	SegmentType     string `json:"segmentType"`
	SegmentRef      string `json:"segmentRef"`
	SegmentFallback *bool  `json:"segmentFallback"`
	// TTLHours makes the reservation lapse after that many hours (0 or absent: it holds until released).
	TTLHours int `json:"ttlHours"`
	// Against is PHYSICAL (default: available stock) or ORDERED (the expected receipt of one dimension set; MANUAL only).
	Against string `json:"against"`
}

type reservationResponse struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	Dimensions    map[string]string `json:"dimensions"`
	Qty           string            `json:"qty"`
	DemandType    string            `json:"demandType"`
	DemandID      string            `json:"demandId"`
	DemandSegment string            `json:"demandSegment,omitempty"`
	Policy        string            `json:"policy"`
	Status        string            `json:"status"`
	Kind          string            `json:"kind"`
	AllocationNo  int               `json:"allocationNo"`
	CreatedAt     time.Time         `json:"createdAt"`
	ReleasedAt    *time.Time        `json:"releasedAt,omitempty"`
	ExpiresAt     *time.Time        `json:"expiresAt,omitempty"`
	ConvertedAt   *time.Time        `json:"convertedAt,omitempty"`
}

type reserveResponse struct {
	Items     []reservationResponse `json:"items"`
	Shortfall string                `json:"shortfall"`
}

type transactionResponse struct {
	ID                 string            `json:"id"`
	LegalEntityID      string            `json:"legalEntityId"`
	OriginID           string            `json:"originId"`
	SourceType         string            `json:"sourceType"`
	SourceID           string            `json:"sourceId"`
	JournalID          string            `json:"journalId,omitempty"`
	Reference          string            `json:"reference,omitempty"`
	ProductID          string            `json:"productId"`
	Dimensions         map[string]string `json:"dimensions"`
	Qty                string            `json:"qty"`
	EnteredQty         string            `json:"enteredQty"`
	EnteredUnit        string            `json:"enteredUnit"`
	CWQty              *int              `json:"cwQty,omitempty"`
	IssueStatus        string            `json:"issueStatus,omitempty"`
	ReceiptStatus      string            `json:"receiptStatus,omitempty"`
	PhysicalDate       string            `json:"physicalDate"`
	FinancialDate      string            `json:"financialDate,omitempty"`
	Currency           string            `json:"currency"`
	CostAmountPhysical string            `json:"costAmountPhysical"`
	CostAmountPosted   *string           `json:"costAmountPosted,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
}

// idempotencyKey reads a required Idempotency-Key into fields when missing or too long.
func idempotencyKey(r *http.Request, fields map[string]string) string {
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || len(key) > maxKeyLength {
		fields[idempotencyHeader] = fmt.Sprintf("is required (1 to %d characters)", maxKeyLength)
	}
	return key
}

// pageLimit reads ?limit= (1..max, default def) into fields when invalid.
func pageLimit(r *http.Request, def, max int, fields map[string]string) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		fields["limit"] = fmt.Sprintf("must be an integer from 1 to %d", max)
		return def
	}
	return n
}

// createReservation reserves a quantity manually (MANUAL, one exact dimension
// set) or by policy (FIFO, FEFO, LIFO over the dimension sets matching the
// dimension filter).
func (a *API) createReservation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req reserveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	qty, err := decimal.NewFromString(req.Qty)
	if err != nil || !qty.IsPositive() || !qty.Equal(qty.Truncate(domain.DefaultJournalRules.MaxScale)) {
		fields["qty"] = "must be a positive decimal number with at most 6 decimals"
	}
	if !slices.Contains(domain.DemandTypes, domain.DemandType(req.DemandType)) {
		fields["demandType"] = "must be MANUAL, SALES_ORDER, PRODUCTION_ORDER or TRANSFER_ORDER"
	}
	req.DemandID = strings.TrimSpace(req.DemandID)
	if n := len(req.DemandID); n == 0 || n > maxIdentifierLen {
		fields["demandId"] = "is required (1 to 60 characters)"
	}
	policy := domain.ReservationPolicy(strings.ToUpper(strings.TrimSpace(req.Policy)))
	if policy == "" {
		policy = domain.PolicyFIFO
	}
	if !slices.Contains(domain.ReservationPolicies, policy) {
		fields["policy"] = "must be MANUAL, FIFO, FEFO or LIFO"
	}
	kind := domain.ReservationKind(strings.ToUpper(strings.TrimSpace(req.Against)))
	switch {
	case kind == "":
		kind = domain.ReserveKindPhysical
	case kind != domain.ReserveKindPhysical && kind != domain.ReserveKindOrdered:
		fields["against"] = "must be PHYSICAL or ORDERED"
	case kind == domain.ReserveKindOrdered && policy != domain.PolicyManual:
		fields["against"] = "ORDERED reserves the expected receipt of one dimension set and needs policy MANUAL"
	}
	if req.TTLHours < 0 || req.TTLHours > maxTTLHours {
		fields["ttlHours"] = "must be between 0 and 8760"
	}
	if req.ShelfLifeMarginDays < 0 || req.ShelfLifeMarginDays > maxShelfLifeDays {
		fields["shelfLifeMarginDays"] = "must be between 0 and 365"
	}
	required := time.Now().UTC().Truncate(24 * time.Hour)
	if req.RequiredDate != "" {
		if required, err = time.Parse(dateLayout, req.RequiredDate); err != nil {
			fields["requiredDate"] = "must be a date (YYYY-MM-DD)"
		}
	}
	dims := domain.Dimensions(req.Dimensions)
	if _, err := dims.Normalize(); err != nil {
		fields["dimensions"] = err.Error()
	} else if policy == domain.PolicyManual && strings.TrimSpace(dims[domain.DimSite]) == "" {
		fields["dimensions"] = "a MANUAL reservation names the dimension set, including its site"
	}
	segment, err := domain.SegmentValue(domain.SegmentType(strings.ToUpper(strings.TrimSpace(req.SegmentType))), req.SegmentRef)
	if err != nil {
		fields["segmentType"] = detail(err)
	}
	fallback := true
	if req.SegmentFallback != nil {
		fallback = *req.SegmentFallback
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	out, err := a.store.Reserve(r.Context(), store.ReserveInput{
		Segment: segment, SegmentFallback: fallback,
		LegalEntityID: req.LegalEntityID, ProductID: req.ProductID, Qty: qty, DemandType: domain.DemandType(req.DemandType),
		DemandID: req.DemandID, Policy: policy, Dims: dims, RequiredDate: required, ShelfLifeMarginDays: req.ShelfLifeMarginDays,
		AllowPartial: req.AllowPartial, IdempotencyKey: key, TTLHours: req.TTLHours, Kind: kind,
	})
	if err != nil {
		a.failed(w, r, "create reservation", err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	resp := reserveResponse{Items: []reservationResponse{}, Shortfall: out.Shortfall.String()}
	for _, res := range out.Reservations {
		resp.Items = append(resp.Items, toReservationResponse(res))
	}
	httpx.JSON(w, status, resp)
}

// listReservations is the reservation inquiry, newest first, with cursor paging.
func (a *API) listReservations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.ReservationFilter{ProductID: q.Get("product"), Status: domain.ReservationStatus(q.Get("status")), DemandID: q.Get("demandId"), Cursor: q.Get("cursor")}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["product"] = "must be a UUID"
	}
	if f.Status != "" && !slices.Contains(domain.ReservationStatuses, f.Status) {
		fields["status"] = "must be ACTIVE, RELEASED or CONSUMED"
	}
	if f.Cursor != "" && !ids.Valid(f.Cursor) {
		fields["cursor"] = "must be a cursor returned by a previous page"
	}
	f.Limit = pageLimit(r, defaultPageSize, maxPageSize, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	limit := f.Limit
	f.Limit++
	found, err := a.store.ListReservations(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list reservations", err)
		return
	}
	body := map[string]any{}
	if len(found) > limit {
		found = found[:limit]
		body["nextCursor"] = found[limit-1].ID
	}
	items := make([]reservationResponse, 0, len(found))
	for _, res := range found {
		items = append(items, toReservationResponse(res))
	}
	body["items"] = items
	httpx.JSON(w, http.StatusOK, body)
}

// releaseReservation releases a reservation. It is idempotent: a reservation
// that is no longer active is returned unchanged.
func (a *API) releaseReservation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeReservationNotFound, "reservation not found"))
		return
	}
	res, err := a.store.ReleaseReservation(r.Context(), id)
	if err != nil {
		a.failed(w, r, "release reservation", err)
		return
	}
	httpx.JSON(w, http.StatusOK, toReservationResponse(res))
}

// expireReservations releases the reservations whose time to live has passed. It is meant
// for a scheduler or an operator; repeating it releases only what is still due.
func (a *API) expireReservations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields := map[string]string{}
	limit := pageLimit(r, 200, maxPageSize, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	out, err := a.store.ExpireReservations(r.Context(), limit)
	if err != nil {
		a.failed(w, r, "expire reservations", err)
		return
	}
	items := make([]reservationResponse, 0, len(out.Released))
	for _, res := range out.Released {
		items = append(items, toReservationResponse(res))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"released": len(items), "more": out.More, "items": items})
}

// putBatchExpiry sets the expiry of a batch, the FEFO input. expiryDate is
// required; null means the batch never expires.
func (a *API) putBatchExpiry(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	productID, batchID := chi.URLParam(r, "productId"), chi.URLParam(r, "batchId")
	fields := map[string]string{}
	if !ids.Valid(productID) {
		fields["productId"] = "must be a UUID"
	}
	if n := len([]rune(batchID)); n == 0 || n > maxIdentifierLen || strings.TrimSpace(batchID) != batchID {
		fields["batchId"] = "is required (1 to 60 characters, no surrounding space)"
	}
	var body struct {
		ExpiryDate json.RawMessage `json:"expiryDate"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var expiry *time.Time
	var raw *string
	if len(body.ExpiryDate) == 0 {
		fields["expiryDate"] = "is required (YYYY-MM-DD or null)"
	} else if err := json.Unmarshal(body.ExpiryDate, &raw); err != nil {
		fields["expiryDate"] = "must be a date (YYYY-MM-DD) or null"
	} else if raw != nil {
		t, err := time.Parse(dateLayout, *raw)
		if err != nil {
			fields["expiryDate"] = "must be a date (YYYY-MM-DD) or null"
		}
		expiry = &t
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	if err := a.store.UpsertBatchExpiry(r.Context(), productID, batchID, expiry); err != nil {
		a.failed(w, r, "set batch expiry", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"productId": productID, "batchId": batchID, "expiryDate": raw})
}

// listTransactions is the transaction inquiry (FR-16.13): filter by product,
// status, origin type and physical date; newest first with cursor paging.
func (a *API) listTransactions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.TransactionFilter{ProductID: q.Get("product"), Status: domain.Status(q.Get("status")), SourceType: q.Get("sourceType"), Cursor: q.Get("cursor")}
	fields := map[string]string{}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["product"] = "must be a UUID"
	}
	if _, _, ok := domain.Lookup(f.Status); f.Status != "" && !ok {
		fields["status"] = "must be an issue or receipt status"
	}
	if f.SourceType != "" && !slices.Contains(inquirySourceTypes, f.SourceType) {
		fields["sourceType"] = "must be one of " + strings.Join(inquirySourceTypes, ", ")
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(dateLayout, v)
			if err != nil {
				fields[name] = "must be a date (YYYY-MM-DD)"
				continue
			}
			*dst = &t
		}
	}
	if f.From != nil && f.To != nil && f.To.Before(*f.From) {
		fields["to"] = "must not be before from"
	}
	if f.Cursor != "" && !ids.Valid(f.Cursor) {
		fields["cursor"] = "must be a cursor returned by a previous page"
	}
	f.Limit = pageLimit(r, defaultPageSize, maxPageSize, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	limit := f.Limit
	f.Limit++ // one extra row tells whether another page exists
	rows, err := a.store.ListTransactions(r.Context(), f)
	if err != nil {
		a.failed(w, r, "transaction inquiry", err)
		return
	}
	body := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		body["nextCursor"] = rows[limit-1].ID
	}
	items := make([]transactionResponse, 0, len(rows))
	for _, t := range rows {
		items = append(items, toTransactionResponse(t))
	}
	body["items"] = items
	httpx.JSON(w, http.StatusOK, body)
}

func toTransactionResponse(t store.TransactionRow) transactionResponse {
	item := transactionResponse{ID: t.ID, LegalEntityID: t.LegalEntityID, OriginID: t.OriginID, SourceType: t.SourceType, SourceID: t.SourceID,
		JournalID: t.JournalID, Reference: t.Reference, ProductID: t.ProductID, Dimensions: t.Dims, Qty: t.Qty.String(),
		EnteredQty: t.EnteredQty.String(), EnteredUnit: t.EnteredUnit, IssueStatus: t.IssueStatus, ReceiptStatus: t.ReceiptStatus,
		PhysicalDate: t.PhysicalDate, FinancialDate: t.FinancialDate, Currency: t.Currency, CostAmountPhysical: t.CostAmountPhysical.String(),
		CreatedAt: t.CreatedAt, CWQty: t.CWQty}
	if t.CostAmountPosted != nil {
		v := t.CostAmountPosted.String()
		item.CostAmountPosted = &v
	}
	return item
}

func toReservationResponse(res domain.Reservation) reservationResponse {
	return reservationResponse{ID: res.ID, LegalEntityID: res.LegalEntityID, ProductID: res.ProductID, Dimensions: res.Dims, Qty: res.Qty.String(),
		DemandType: string(res.DemandType), DemandID: res.DemandID, DemandSegment: res.DemandSegment, Policy: string(res.Policy), Status: string(res.Status), Kind: reservationKind(res.Kind),
		AllocationNo: res.AllocationNo, CreatedAt: res.CreatedAt, ReleasedAt: res.ReleasedAt, ExpiresAt: res.ExpiresAt, ConvertedAt: res.ConvertedAt}
}

// detail is the message of a domain rule violation without its code.
func detail(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Detail
	}
	return err.Error()
}

type candidateResponse struct {
	Dimensions map[string]string `json:"dimensions"`
	BatchID    string            `json:"batchId,omitempty"`
	Serial     string            `json:"serial,omitempty"`
	Available  string            `json:"available"`
	Status     string            `json:"status"`
	Segment    string            `json:"segment,omitempty"`
	ExpiryDate string            `json:"expiryDate,omitempty"`
	Eligible   bool              `json:"eligible"`
	Reason     string            `json:"reason,omitempty"`
}

// candidateDimensions are the dimensions the candidate inquiry filters by.
var candidateDimensions = []string{domain.DimSite, domain.DimWarehouse, domain.DimLocation, domain.DimBatch, domain.DimSerial,
	domain.DimConfig, domain.DimSize, domain.DimColor, domain.DimStyle}

// listReservationCandidates lists the batches and serials a reservation could take, in the
// pick order of the policy, with the expiry date and the reason a row is not eligible (FR-16.7).
func (a *API) listReservationCandidates(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	in := store.CandidateQuery{LegalEntityID: q.Get("legalEntityId"), ProductID: q.Get("productId"), SegmentFallback: true}
	if !ids.Valid(in.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(in.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	in.Policy = domain.ReservationPolicy(strings.ToUpper(strings.TrimSpace(q.Get("policy"))))
	if in.Policy == "" {
		in.Policy = domain.PolicyFIFO
	}
	if !slices.Contains(domain.ReservationPolicies, in.Policy) {
		fields["policy"] = "must be MANUAL, FIFO, FEFO or LIFO"
	}
	if raw := q.Get("shelfLifeMarginDays"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > maxShelfLifeDays {
			fields["shelfLifeMarginDays"] = "must be between 0 and 365"
		}
		in.ShelfLifeMarginDays = n
	}
	in.RequiredDate = time.Now().UTC().Truncate(24 * time.Hour)
	if raw := q.Get("requiredDate"); raw != "" {
		d, err := time.Parse(dateLayout, raw)
		if err != nil {
			fields["requiredDate"] = "must be a date (YYYY-MM-DD)"
		}
		in.RequiredDate = d
	}
	if raw := q.Get("segmentFallback"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			fields["segmentFallback"] = "must be true or false"
		}
		in.SegmentFallback = b
	}
	segment, err := domain.SegmentValue(domain.SegmentType(strings.ToUpper(strings.TrimSpace(q.Get("segmentType")))), q.Get("segmentRef"))
	if err != nil {
		fields["segmentType"] = detail(err)
	}
	in.Segment = segment
	in.Dims = domain.Dimensions{}
	for _, d := range candidateDimensions {
		if v := strings.TrimSpace(q.Get(d)); v != "" {
			in.Dims[d] = v
		}
	}
	if _, err := in.Dims.Normalize(); err != nil {
		fields["dimensions"] = err.Error()
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	found, err := a.store.ReservationCandidates(r.Context(), in)
	if err != nil {
		a.failed(w, r, "list reservation candidates", err)
		return
	}
	items := make([]candidateResponse, 0, len(found))
	for _, c := range found {
		item := candidateResponse{Dimensions: c.Dims, BatchID: c.BatchID, Serial: c.Dims[domain.DimSerial], Available: c.Available.String(),
			Status: c.Status, Segment: c.Segment, Eligible: c.Eligible, Reason: c.Reason}
		if c.Expiry != nil {
			item.ExpiryDate = c.Expiry.Format(dateLayout)
		}
		items = append(items, item)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// reservationKind reads an unset kind as PHYSICAL.
func reservationKind(k domain.ReservationKind) string {
	if k == "" {
		return string(domain.ReserveKindPhysical)
	}
	return string(k)
}
