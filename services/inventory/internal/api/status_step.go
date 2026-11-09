package api

import (
	"fmt"
	"net/http"
	"slices"
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

// Status-step routes (FR-16.2, BR-16.1). transactions:apply is the
// service-to-service command of waves 17–20 (service account duty); the named
// backward commands are user actions on one transaction.
const (
	dutyTransactionApply = "inventory.transaction.apply"
	dutyTransactionStep  = "inventory.transaction.step"
	applyPath            = "/internal/v1/inventory/transactions:apply"
	statusChangesPath    = "/v1/inventory/status-changes"
	maxReferenceLength   = 60
	maxQtyDecimals       = 6
	maxCWQty             = 1_000_000
)

// namedCommandVerbs maps each custom-method verb to its named backward command.
var namedCommandVerbs = map[string]domain.StepCommand{
	":unpick":             domain.CommandUnpick,
	":unregister":         domain.CommandUnregister,
	":cancel-reservation": domain.CommandCancelReservation,
}

// applyRequest is one registration (origin set) or one STEP (transactionId set).
type applyRequest struct {
	// STEP of an existing transaction.
	TransactionID string `json:"transactionId"`
	FromStatus    string `json:"fromStatus"`
	CostAmount    string `json:"costAmount"`
	// Registration of a transaction for an external origin.
	Origin        *originRequest    `json:"origin"`
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	Dimensions    map[string]string `json:"dimensions"`
	Qty           string            `json:"qty"`
	EnteredQty    *string           `json:"enteredQty"`
	EnteredUnit   string            `json:"enteredUnit"`
	Currency      string            `json:"currency"`
	CostPrice     string            `json:"costPrice"`
	CWQty         *int              `json:"cwQty"`
	// IssueRef names the document line an issue registration is booked for
	// (platform/issueclaim: SALES_LINE:<id>, PRODUCTION_ORDER:<order>:<item>);
	// it settles the claims taken for that line and for the picks without a ref.
	IssueRef string `json:"issueRef"`
	// Both.
	ToStatus string `json:"toStatus"`
	Date     string `json:"date"`
}

type originRequest struct {
	SourceType string `json:"sourceType"`
	SourceID   string `json:"sourceId"`
	Reference  string `json:"reference"`
}

type namedStepRequest struct {
	Date string `json:"date"`
}

// statusChangeResponse is one status-change audit row.
type statusChangeResponse struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	TransactionID string    `json:"transactionId"`
	Command       string    `json:"command"`
	FromStatus    string    `json:"fromStatus,omitempty"`
	ToStatus      string    `json:"toStatus"`
	Qty           string    `json:"qty"`
	Date          string    `json:"date"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
}

type transactionDetailResponse struct {
	transactionResponse
	StatusChanges []statusChangeResponse `json:"statusChanges"`
}

// registerStatusSteps mounts the status-step routes. The named backward
// commands are the only way a transaction moves backward (BR-16.1).
func (a *API) registerStatusSteps(r chi.Router) {
	r.With(authz.RequireDuty(dutyTransactionApply)).Post(applyPath, a.applyTransaction)
	for verb, cmd := range namedCommandVerbs {
		r.With(authz.RequireDuty(dutyTransactionStep)).Post(transactionsPath+"/{id}"+verb, a.namedStep(cmd))
	}
	r.With(authz.RequireDuty(dutyRead)).Get(transactionsPath+"/{id}", a.getTransaction)
	r.With(authz.RequireDuty(dutyRead)).Get(statusChangesPath, a.listStatusChanges)
}

// optionalStepDate parses an optional date into fields when malformed.
func optionalStepDate(raw string, fields map[string]string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	d, err := time.Parse(dateLayout, raw)
	if err != nil {
		fields["date"] = "must be a date (YYYY-MM-DD)"
	}
	return d
}

func validStatus(raw string) bool {
	_, _, ok := domain.Lookup(domain.Status(raw))
	return ok
}

// applyTransaction registers a transaction for an external origin or steps an
// existing one forward (POST /internal/v1/inventory/transactions:apply).
func (a *API) applyTransaction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req applyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	key := idempotencyKey(r, fields)
	date := optionalStepDate(req.Date, fields)
	if !validStatus(req.ToStatus) {
		fields["toStatus"] = "must be an issue or receipt status"
	}
	switch {
	case req.Origin != nil && req.TransactionID != "":
		fields["origin"] = "give either origin (register a transaction) or transactionId (step one), not both"
	case req.Origin != nil:
		a.registerTransaction(w, r, req, date, key, fields)
	case req.TransactionID != "":
		a.stepTransaction(w, r, req, date, key, fields)
	default:
		fields["transactionId"] = "is required unless origin registers a new transaction"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
	}
}

// stepTransaction answers the STEP form of transactions:apply. It writes the
// response only when fields is empty; otherwise the caller reports the fields.
func (a *API) stepTransaction(w http.ResponseWriter, r *http.Request, req applyRequest, date time.Time, key string, fields map[string]string) {
	if !ids.Valid(req.TransactionID) {
		fields["transactionId"] = "must be a UUID"
	}
	if !validStatus(req.FromStatus) {
		fields["fromStatus"] = "must be the status the caller read"
	}
	for name, set := range map[string]bool{"legalEntityId": req.LegalEntityID != "", "productId": req.ProductID != "", "qty": req.Qty != "",
		"dimensions": req.Dimensions != nil, "currency": req.Currency != "", "costPrice": req.CostPrice != "", "enteredUnit": req.EnteredUnit != "",
		"cwQty": req.CWQty != nil, "issueRef": req.IssueRef != ""} {
		if set {
			fields[name] = "applies only when origin registers a new transaction"
		}
	}
	var cost *decimal.Decimal
	if req.CostAmount != "" {
		v, err := decimal.NewFromString(req.CostAmount)
		if err != nil || v.Sign() < 0 {
			fields["costAmount"] = "must be a non-negative decimal number"
		}
		cost = &v
	}
	if len(fields) > 0 {
		return
	}
	a.runStep(w, r, store.StatusStepInput{TransactionID: req.TransactionID, Command: domain.CommandStep, From: domain.Status(req.FromStatus),
		To: domain.Status(req.ToStatus), Date: date, CostAmount: cost, IdempotencyKey: key, Negative: a.post.Negative})
}

// registerTransaction answers the registration form of transactions:apply.
func (a *API) registerTransaction(w http.ResponseWriter, r *http.Request, req applyRequest, date time.Time, key string, fields map[string]string) {
	o := req.Origin
	if !slices.Contains(store.ExternalSourceTypes, o.SourceType) {
		fields["origin.sourceType"] = "must be one of " + strings.Join(store.ExternalSourceTypes, ", ")
	}
	if !ids.Valid(o.SourceID) {
		fields["origin.sourceId"] = "must be a UUID"
	}
	if o.Reference = strings.TrimSpace(o.Reference); len(o.Reference) > maxReferenceLength {
		fields["origin.reference"] = "must be at most 60 characters"
	}
	if req.FromStatus != "" || req.CostAmount != "" {
		fields["fromStatus"] = "fromStatus and costAmount apply only to a step of an existing transaction"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	qty, err := decimal.NewFromString(strings.TrimSpace(req.Qty))
	if err != nil || qty.IsZero() {
		fields["qty"] = "must be a non-zero decimal number (positive for receipts, negative for issues)"
	} else if qty.Exponent() < -maxQtyDecimals {
		fields["qty"] = "must have at most 6 decimals"
	}
	entered := qty
	if req.EnteredQty != nil {
		if entered, err = decimal.NewFromString(*req.EnteredQty); err != nil || entered.Sign() != qty.Sign() {
			fields["enteredQty"] = "must be a decimal number with the sign of qty"
		}
	}
	if !unitCode.MatchString(req.EnteredUnit) {
		fields["enteredUnit"] = "is required (1 to 12 characters of A-Z, 0-9, _ or -)"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	cost, err := decimalOr(req.CostPrice, decimal.Zero)
	if err != nil || cost.Sign() < 0 {
		fields["costPrice"] = "must be a non-negative decimal number"
	}
	cwQty := 0
	if req.CWQty != nil {
		if *req.CWQty < 1 || *req.CWQty > maxCWQty {
			fields["cwQty"] = fmt.Sprintf("must be a whole number from 1 to %d", maxCWQty)
		} else {
			cwQty = *req.CWQty
		}
	}
	if len(req.IssueRef) > domain.MaxIssueRef || (req.IssueRef != "" && qty.IsPositive()) {
		fields["issueRef"] = "must be at most 255 characters and applies only to an issue"
	}
	if len(fields) > 0 {
		return
	}
	change, applied, err := a.store.CreateTransaction(r.Context(), store.CreateTransactionInput{LegalEntityID: req.LegalEntityID,
		SourceType: o.SourceType, SourceID: o.SourceID, Reference: o.Reference, ProductID: req.ProductID, Dims: domain.Dimensions(req.Dimensions),
		Qty: qty, EnteredQty: entered, EnteredUnit: req.EnteredUnit, Status: domain.Status(req.ToStatus), Currency: req.Currency,
		CostPrice: cost, CWQty: cwQty, Date: date, IdempotencyKey: key, Negative: a.post.Negative, IssueRef: req.IssueRef})
	writeStatusChange(w, r, a, "register transaction", change, applied, err)
}

// namedStep answers one named backward command on a transaction (Unpick,
// Unregister, CancelReservation). The move follows from the transaction's
// current status; the optional body carries the date of the move.
func (a *API) namedStep(cmd domain.StepCommand) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeTransactionNotFound, "inventory transaction not found"))
			return
		}
		var req namedStepRequest
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		fields := map[string]string{}
		key := idempotencyKey(r, fields)
		date := optionalStepDate(req.Date, fields)
		if len(fields) > 0 {
			fieldProblem(w, r, fields)
			return
		}
		a.runStep(w, r, store.StatusStepInput{TransactionID: id, Command: cmd, Date: date, IdempotencyKey: key, Negative: a.post.Negative})
	}
}

func (a *API) runStep(w http.ResponseWriter, r *http.Request, in store.StatusStepInput) {
	change, applied, err := a.store.ApplyStatusStep(r.Context(), in)
	writeStatusChange(w, r, a, "apply status step", change, applied, err)
}

// writeStatusChange answers 201 for an applied change and 200 with
// Idempotent-Replayed for a replay.
func writeStatusChange(w http.ResponseWriter, r *http.Request, a *API, op string, c store.StatusChange, applied bool, err error) {
	if err != nil {
		a.failed(w, r, op, err)
		return
	}
	status := http.StatusCreated
	if !applied {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, toStatusChangeResponse(c))
}

func toStatusChangeResponse(c store.StatusChange) statusChangeResponse {
	return statusChangeResponse{ID: c.ID, LegalEntityID: c.LegalEntityID, TransactionID: c.TransactionID, Command: string(c.Command),
		FromStatus: string(c.From), ToStatus: string(c.To), Qty: c.Qty.String(), Date: c.Date, CreatedBy: c.CreatedBy, OccurredAt: c.OccurredAt}
}

// getTransaction is the transaction detail: the transaction, its origin and its
// status history, oldest first.
func (a *API) getTransaction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeTransactionNotFound, "inventory transaction not found"))
		return
	}
	t, history, err := a.store.GetTransaction(r.Context(), id)
	if err != nil {
		a.failed(w, r, "get transaction", err)
		return
	}
	out := transactionDetailResponse{transactionResponse: toTransactionResponse(t), StatusChanges: make([]statusChangeResponse, 0, len(history))}
	for _, c := range history {
		out.StatusChanges = append(out.StatusChanges, toStatusChangeResponse(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// listStatusChanges is the status-change inquiry: audit rows newest first,
// filtered by transaction and command, with cursor paging.
func (a *API) listStatusChanges(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.StatusChangeFilter{TransactionID: q.Get("transaction"), Command: domain.StepCommand(q.Get("command")), Cursor: q.Get("cursor")}
	fields := map[string]string{}
	if f.TransactionID != "" && !ids.Valid(f.TransactionID) {
		fields["transaction"] = "must be a UUID"
	}
	known := append([]domain.StepCommand{domain.CommandCreate, domain.CommandStep}, domain.NamedCommands()...)
	if f.Command != "" && !slices.Contains(known, f.Command) {
		fields["command"] = "must be CREATE, STEP, UNPICK, UNREGISTER or CANCEL_RESERVATION"
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
	rows, err := a.store.ListStatusChanges(r.Context(), f)
	if err != nil {
		a.failed(w, r, "status-change inquiry", err)
		return
	}
	body := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		body["nextCursor"] = rows[limit-1].ID
	}
	items := make([]statusChangeResponse, 0, len(rows))
	for _, c := range rows {
		items = append(items, toStatusChangeResponse(c))
	}
	body["items"] = items
	httpx.JSON(w, http.StatusOK, body)
}
