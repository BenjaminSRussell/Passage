package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

const (
	idempotencyKey   = "Idempotency-Key"
	maxKeyLength     = 255
	defaultListLimit = 100
	maxListLimit     = 200
	periodLayout     = "2006-01"
	// CodeFuturePeriod is REV-0007: a recognition run for a period that has not ended.
	CodeFuturePeriod = "REV-0007"
)

// Store is the persistence the handlers need; *store.Postgres implements it.
type Store interface {
	Ping(ctx context.Context) error
	FindContractByKey(ctx context.Context, key string) (store.Contract, bool, error)
	CreateContract(ctx context.Context, c store.Contract) (store.Contract, error)
	GetContract(ctx context.Context, id string) (store.Contract, error)
	ListContracts(ctx context.Context, f store.ContractFilter) (store.ContractPage, error)
	SatisfyMilestone(ctx context.Context, obligationID string, on time.Time, expectedVersion int) (store.Satisfaction, error)
	RunRecognition(ctx context.Context, legalEntityID string, periodEnd time.Time) (store.Run, bool, error)
	ListRuns(ctx context.Context, legalEntityID string, limit int) ([]store.Run, error)
	RollForward(ctx context.Context, legalEntityID, currency string, start, end time.Time) (domain.RollForward, error)
	ContractRollForward(ctx context.Context, contractID string, start, end time.Time) (store.ContractRoll, error)
}

type contractObligationDTO struct {
	ProductID   string  `json:"productId"`
	SSP         string  `json:"ssp"`
	SSPSource   string  `json:"sspSource"`
	Method      string  `json:"method"`
	TermStart   *string `json:"termStart,omitempty"`
	TermEnd     *string `json:"termEnd,omitempty"`
	SatisfiedOn *string `json:"satisfiedOn,omitempty"`
}

type contractRequest struct {
	LegalEntityID    string                  `json:"legalEntityId"`
	OrderID          string                  `json:"orderId"`
	CustomerID       string                  `json:"customerId"`
	Currency         string                  `json:"currency"`
	TransactionPrice string                  `json:"transactionPrice"`
	ResidualRule     string                  `json:"residualRule,omitempty"`
	RatableBasis     string                  `json:"ratableBasis,omitempty"`
	ContractDate     string                  `json:"contractDate"`
	Obligations      []contractObligationDTO `json:"obligations"`
}

type scheduleOut struct {
	ID           string  `json:"id"`
	Seq          int     `json:"seq"`
	Date         *string `json:"date,omitempty"`
	Amount       string  `json:"amount"`
	Status       string  `json:"status"`
	RecognizedOn *string `json:"recognizedOn,omitempty"`
}

type obligationOut struct {
	ID          string        `json:"id"`
	LineNo      int           `json:"lineNo"`
	ProductID   string        `json:"productId"`
	SSP         string        `json:"ssp"`
	SSPSource   string        `json:"sspSource"`
	Allocated   string        `json:"allocated"`
	Recognized  string        `json:"recognized"`
	Method      string        `json:"method"`
	TermStart   *string       `json:"termStart,omitempty"`
	TermEnd     *string       `json:"termEnd,omitempty"`
	SatisfiedOn *string       `json:"satisfiedOn,omitempty"`
	Schedule    []scheduleOut `json:"schedule"`
}

type warningOut struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type contractSummaryOut struct {
	ID               string `json:"id"`
	LegalEntityID    string `json:"legalEntityId"`
	OrderID          string `json:"orderId"`
	CustomerID       string `json:"customerId"`
	Currency         string `json:"currency"`
	TransactionPrice string `json:"transactionPrice"`
	Recognized       string `json:"recognized"`
	Remaining        string `json:"remaining"`
	ResidualRule     string `json:"residualRule"`
	RatableBasis     string `json:"ratableBasis"`
	ContractDate     string `json:"contractDate"`
	Status           string `json:"status"`
	Version          int    `json:"version"`
}

type contractOut struct {
	contractSummaryOut
	Obligations []obligationOut `json:"obligations"`
	Warnings    []warningOut    `json:"warnings"`
}

type satisfyRequest struct {
	SatisfiedOn string `json:"satisfiedOn"`
}

type satisfactionOut struct {
	ContractID      string `json:"contractId"`
	ContractVersion int    `json:"contractVersion"`
	ObligationID    string `json:"obligationId"`
	ScheduleID      string `json:"scheduleId"`
	Amount          string `json:"amount"`
	RecognizedOn    string `json:"recognizedOn"`
}

type runRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	PeriodEnd     string `json:"periodEnd"`
}

type runOut struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	PeriodEnd     string `json:"periodEnd"`
	LineCount     int    `json:"lineCount"`
	CreatedAt     string `json:"createdAt"`
}

type rollForwardOut struct {
	LegalEntityID string `json:"legalEntityId"`
	ContractID    string `json:"contractId,omitempty"`
	Currency      string `json:"currency"`
	Period        string `json:"period"`
	Start         string `json:"start"`
	End           string `json:"end"`
	Opening       string `json:"opening"`
	Additions     string `json:"additions"`
	Recognized    string `json:"recognized"`
	Adjustments   string `json:"adjustments"`
	Closing       string `json:"closing"`
}

// createContract persists a contract allocated from the transaction price
// (FR-29.7). The Idempotency-Key is required; a replay with the same body
// returns the stored contract (200), a different body under the same key is 422.
func (a *API) createContract(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(idempotencyKey)
	if key == "" || len(key) > maxKeyLength {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "Idempotency-Key header is required (1 to 255 characters)"))
		return
	}
	var req contractRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	c, err := buildContract(req, fields)
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !inScope(r, req.LegalEntityID) {
		httpx.Fail(w, r, outOfScope())
		return
	}
	c.IdempotencyKey = key
	c.RequestHash = requestHash(req)
	existing, found, err := a.store.FindContractByKey(r.Context(), key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if found {
		if existing.RequestHash != c.RequestHash {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "Idempotency-Key was already used with a different request body"))
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		writeContract(w, http.StatusOK, existing)
		return
	}
	created, err := a.store.CreateContract(r.Context(), c)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.contractsCreated.Add(1)
	writeContract(w, http.StatusCreated, created)
}

// listContracts is the revenue contracts inquiry (§5.29.14): headers with the
// revenue recognized and remaining, newest first, filtered by legal entity,
// customer, status and obligation method. A page holds at most `limit` items;
// nextCursor, when present, is passed back as `cursor` for the next page.
func (a *API) listContracts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ContractFilter{LegalEntityID: q.Get("legalEntityId"), CustomerID: q.Get("customerId"), OrderID: q.Get("orderId"), Status: q.Get("status"), Method: q.Get("method")}
	fields := map[string]string{}
	if f.Method != "" && !domain.ValidMethod(f.Method) {
		fields["method"] = "must be POINT_IN_TIME, RATABLE, MILESTONE or USAGE"
	}
	after, ok := decodeCursor(q.Get("cursor"))
	if !ok {
		fields["cursor"] = "must be a nextCursor returned by a previous page"
	}
	f.After = after
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if f.OrderID != "" && !ids.Valid(f.OrderID) {
		fields["orderId"] = "must be a UUID"
	}
	if f.OrderID == "" && allocationOnly(r) {
		fields["orderId"] = "is required for a caller holding only " + dutyAllocationRead
	}
	if f.Status != "" && f.Status != domain.ContractOpen && f.Status != domain.ContractComplete && f.Status != domain.ContractCancelled {
		fields["status"] = "must be OPEN, COMPLETE or CANCELLED"
	}
	limit, ok := parseLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = "must be an integer from 1 to 200"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	f.Limit = limit
	page, err := a.store.ListContracts(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := contractListOut{Items: make([]contractSummaryOut, 0, len(page.Items))}
	for _, c := range page.Items {
		out.Items = append(out.Items, toSummary(c))
	}
	if page.NextAfter != "" {
		out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextAfter))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type contractListOut struct {
	Items      []contractSummaryOut `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

// decodeCursor reads an opaque list cursor (base64url of the last contract id).
// An absent cursor is the first page.
func decodeCursor(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || !ids.Valid(string(b)) {
		return "", false
	}
	return strings.ToLower(string(b)), true
}

func (a *API) getContract(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "contract not found"))
		return
	}
	c, err := a.store.GetContract(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if allocationOnly(r) && !strings.EqualFold(c.OrderID, r.URL.Query().Get("orderId")) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "contract not found"))
		return
	}
	writeContract(w, http.StatusOK, c)
}

func allocationOnly(r *http.Request) bool {
	p, _ := tenant.FromContext(r.Context())
	return !p.HasDuty(dutyView)
}

// satisfyMilestone recognizes a MILESTONE obligation on the given date
// (FR-29.8). A retry with the same date returns the recorded satisfaction
// (Idempotent-Replayed); an If-Match header, when sent, must carry the contract
// version the caller reviewed (412 REV-0103 otherwise).
func (a *API) satisfyMilestone(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "obligation not found"))
		return
	}
	expected, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", `If-Match must be the contract version as an entity tag, for example "1"`))
		return
	}
	var req satisfyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	on, err := time.Parse(dateLayout, req.SatisfiedOn)
	if err != nil {
		writeFields(w, r, map[string]string{"satisfiedOn": "must be a date as YYYY-MM-DD"})
		return
	}
	s, err := a.store.SatisfyMilestone(r.Context(), id, on, expected)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if s.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.milestonesSatisfied.Add(1)
	}
	w.Header().Set("ETag", etag(s.ContractVersion))
	httpx.JSON(w, http.StatusOK, satisfactionOut{
		ContractID: s.ContractID, ContractVersion: s.ContractVersion, ObligationID: s.ObligationID, ScheduleID: s.ScheduleID,
		Amount: fixed(s.Amount, s.Currency), RecognizedOn: s.RecognizedOn.Format(dateLayout),
	})
}

// runRecognition recognizes the due ratable lines of a legal entity through a
// period end (FR-29.8). The key is the period: the first call creates the run
// (201), later calls for the same entity and period end return it (200). A
// period that has not ended yet is refused (REV-0007), so revenue is never
// recognized ahead of time.
func (a *API) runRecognition(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	end, err := time.Parse(dateLayout, req.PeriodEnd)
	if err != nil {
		fields["periodEnd"] = "must be a date as YYYY-MM-DD"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !inScope(r, req.LegalEntityID) {
		httpx.Fail(w, r, outOfScope())
		return
	}
	today := a.now().UTC().Truncate(24 * time.Hour)
	if end.After(today) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeFuturePeriod,
			fmt.Sprintf("the period ending %s has not ended; revenue cannot be recognized ahead of time", req.PeriodEnd)))
		return
	}
	run, created, err := a.store.RunRecognition(r.Context(), req.LegalEntityID, end)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.linesRecognized.Add(int64(run.LineCount))
	} else {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, toRunOut(run))
}

// listRuns lists recognition runs, latest period first.
func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	le := q.Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID"
	}
	limit, ok := parseLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = "must be an integer from 1 to 200"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	runs, err := a.store.ListRuns(r.Context(), le, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]runOut, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunOut(run))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// rollForward reports deferred revenue for a calendar month (FR-29.9): one
// currency of a legal entity (legalEntityId and currency), or with contractId
// the drill-down to one contract, stated in that contract's currency.
func (a *API) rollForward(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	contractID, le, cur, period := q.Get("contractId"), q.Get("legalEntityId"), q.Get("currency"), q.Get("period")
	if contractID != "" {
		if !ids.Valid(contractID) {
			fields["contractId"] = "must be a UUID"
		}
		if le != "" || cur != "" {
			fields["contractId"] = "is used without legalEntityId and currency; the contract fixes both"
		}
	} else {
		if !ids.Valid(le) {
			fields["legalEntityId"] = "must be a UUID"
		}
		if !currencyCode.MatchString(cur) {
			fields["currency"] = "must be an ISO 4217 alpha-3 code"
		}
	}
	start, err := time.Parse(periodLayout, period)
	if err != nil {
		fields["period"] = "must be a month as YYYY-MM"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	end := start.AddDate(0, 1, -1)
	if contractID != "" {
		// Row-level security hides a contract of another tenant or another
		// legal-entity scope, so it answers 404 like an unknown id.
		cr, err := a.store.ContractRollForward(r.Context(), contractID, start, end)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toRollForwardOut(cr.RollForward, cr.LegalEntityID, contractID, cr.Currency, period, start, end))
		return
	}
	if !inScope(r, le) {
		httpx.Fail(w, r, outOfScope())
		return
	}
	rf, err := a.store.RollForward(r.Context(), le, cur, start, end)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRollForwardOut(rf, le, "", cur, period, start, end))
}

func toRollForwardOut(rf domain.RollForward, le, contractID, cur, period string, start, end time.Time) rollForwardOut {
	return rollForwardOut{
		LegalEntityID: le, ContractID: contractID, Currency: cur, Period: period,
		Start: start.Format(dateLayout), End: end.Format(dateLayout),
		Opening: fixed(rf.Opening, cur), Additions: fixed(rf.Additions, cur), Recognized: fixed(rf.Recognized, cur),
		Adjustments: fixed(rf.Adjustments, cur), Closing: fixed(rf.Closing, cur),
	}
}

// buildContract validates the request and allocates it. Field errors go into
// fields; a domain rejection (residual count, method inputs) is returned.
func buildContract(req contractRequest, fields map[string]string) (store.Contract, error) {
	if req.ResidualRule == "" {
		req.ResidualRule = domain.ResidualLargest
	}
	basis := req.RatableBasis
	if basis == "" {
		basis = domain.BasisDaily
	}
	if !domain.ValidRatableBasis(basis) {
		fields["ratableBasis"] = "must be DAILY or MONTHLY"
	}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "orderId": req.OrderID, "customerId": req.CustomerID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if !decimalText.MatchString(req.TransactionPrice) {
		fields["transactionPrice"] = "must be a non-negative decimal with at most 6 fraction digits"
	}
	if !domain.ValidResidualRule(req.ResidualRule) {
		fields["residualRule"] = "must be LARGEST, FIRST or LAST"
	}
	contractDate, err := time.Parse(dateLayout, req.ContractDate)
	if err != nil {
		fields["contractDate"] = "must be a date as YYYY-MM-DD"
	}
	if n := len(req.Obligations); n == 0 || n > maxObligations {
		fields["obligations"] = "must hold 1 to 50 obligations"
	}
	// The allocation identifies obligations by line, so one product may appear
	// on several lines (a bundle with two support terms, for example).
	dtos := make([]obligationDTO, len(req.Obligations))
	satisfied := make([]*time.Time, len(req.Obligations))
	for i, o := range req.Obligations {
		dtos[i] = obligationDTO{ID: strconv.Itoa(i + 1), SSP: o.SSP, SSPSource: o.SSPSource, Method: o.Method, TermStart: o.TermStart, TermEnd: o.TermEnd}
		key := func(name string) string { return fmt.Sprintf("obligations[%d].%s", i, name) }
		if !ids.Valid(o.ProductID) {
			fields[key("productId")] = "must be a UUID"
		}
		switch {
		case o.Method == domain.MethodPointInTime && o.SatisfiedOn == nil:
			fields[key("satisfiedOn")] = "a POINT_IN_TIME obligation needs satisfiedOn, the date control transferred"
		case o.Method != domain.MethodPointInTime && o.SatisfiedOn != nil:
			fields[key("satisfiedOn")] = "only a POINT_IN_TIME obligation takes satisfiedOn; satisfy a milestone with :satisfy"
		case o.SatisfiedOn != nil:
			t, err := time.Parse(dateLayout, *o.SatisfiedOn)
			if err != nil {
				fields[key("satisfiedOn")] = "must be a date as YYYY-MM-DD"
			} else {
				satisfied[i] = &t
			}
		}
	}
	obs, terms, ok := toDomain(dtos, fields)
	if !ok || len(fields) > 0 {
		return store.Contract{}, nil
	}
	if err := domain.CheckSSPSources(obs); err != nil {
		return store.Contract{}, problemFrom(err)
	}
	price := money.Amount{Value: decimal.RequireFromString(req.TransactionPrice), Currency: req.Currency}.Round()
	allocated, err := domain.Allocate(price, obs, req.ResidualRule)
	if err != nil {
		return store.Contract{}, problemFrom(err)
	}
	c := store.Contract{
		ID: ids.New(), LegalEntityID: req.LegalEntityID, OrderID: req.OrderID, CustomerID: req.CustomerID,
		Currency: req.Currency, TransactionPrice: price.Value, ResidualRule: req.ResidualRule, RatableBasis: basis,
		ContractDate: contractDate,
	}
	for i, o := range allocated {
		ob := store.Obligation{
			ID: ids.New(), ContractID: c.ID, LineNo: i + 1, ProductID: req.Obligations[i].ProductID,
			SSP: o.SSP, SSPSource: o.SSPSource, Allocated: o.Allocated, Method: o.Method, SatisfiedOn: satisfied[i],
		}
		if t := terms[i]; t != nil {
			start, end := t[0], t[1]
			ob.TermStart, ob.TermEnd = &start, &end
		}
		lines, err := domain.ScheduleLines(o, req.Currency, basis, ob.TermStart, ob.TermEnd, ob.SatisfiedOn)
		if err != nil {
			return store.Contract{}, problemFrom(err)
		}
		for _, l := range lines {
			sc := store.Schedule{ID: ids.New(), Seq: l.Seq, Date: l.Date, Amount: l.Amount, Status: domain.InitialStatus(o.Method, l.Date)}
			if sc.Status == domain.ScheduleRecognized {
				sc.RecognizedOn = l.Date
			}
			ob.Schedule = append(ob.Schedule, sc)
		}
		c.Obligations = append(c.Obligations, ob)
	}
	return c, nil
}

func requestHash(req contractRequest) string {
	b, _ := json.Marshal(req) // a decoded request always re-encodes
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeContract(w http.ResponseWriter, status int, c store.Contract) {
	w.Header().Set("ETag", etag(c.Version))
	httpx.JSON(w, status, toContractOut(c))
}

func toSummary(c store.Contract) contractSummaryOut {
	return contractSummaryOut{
		ID: c.ID, LegalEntityID: c.LegalEntityID, OrderID: c.OrderID, CustomerID: c.CustomerID, Currency: c.Currency,
		TransactionPrice: fixed(c.TransactionPrice, c.Currency), Recognized: fixed(c.Recognized, c.Currency),
		Remaining: fixed(c.TransactionPrice.Sub(c.Recognized), c.Currency), ResidualRule: c.ResidualRule, RatableBasis: c.RatableBasis,
		ContractDate: c.ContractDate.Format(dateLayout), Status: c.Status, Version: c.Version,
	}
}

func toContractOut(c store.Contract) contractOut {
	out := contractOut{contractSummaryOut: toSummary(c), Obligations: []obligationOut{}, Warnings: []warningOut{}}
	dobs := make([]domain.Obligation, 0, len(c.Obligations))
	products := make([]string, 0, len(c.Obligations))
	for _, o := range c.Obligations {
		item := obligationOut{
			ID: o.ID, LineNo: o.LineNo, ProductID: o.ProductID, SSP: sspText(o.SSP, c.Currency), SSPSource: o.SSPSource,
			Allocated: fixed(o.Allocated, c.Currency), Method: o.Method, TermStart: fmtDate(o.TermStart),
			TermEnd: fmtDate(o.TermEnd), SatisfiedOn: fmtDate(o.SatisfiedOn), Schedule: []scheduleOut{},
		}
		recognized := decimal.Zero
		for _, sc := range o.Schedule {
			if sc.Status == domain.ScheduleRecognized {
				recognized = recognized.Add(sc.Amount)
			}
			item.Schedule = append(item.Schedule, scheduleOut{
				ID: sc.ID, Seq: sc.Seq, Date: fmtDate(sc.Date), Amount: fixed(sc.Amount, c.Currency),
				Status: sc.Status, RecognizedOn: fmtDate(sc.RecognizedOn),
			})
		}
		item.Recognized = fixed(recognized, c.Currency)
		out.Obligations = append(out.Obligations, item)
		dobs = append(dobs, domain.Obligation{SSPSource: o.SSPSource})
		products = append(products, fmt.Sprintf("line %d (product %s)", o.LineNo, o.ProductID))
	}
	for _, wn := range domain.SSPWarnings(dobs, products) {
		out.Warnings = append(out.Warnings, warningOut{Code: wn.Code, Detail: wn.Detail})
	}
	return out
}

func toRunOut(run store.Run) runOut {
	return runOut{ID: run.ID, LegalEntityID: run.LegalEntityID, PeriodEnd: run.PeriodEnd.Format(dateLayout),
		LineCount: run.LineCount, CreatedAt: run.CreatedAt.UTC().Format(time.RFC3339)}
}

func fmtDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(dateLayout)
	return &s
}

func inScope(r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	return tenant.InLegalEntityScope(p, legalEntityID)
}

func outOfScope() error {
	return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity")
}

// parseLimit reads the list limit: absent means the default, otherwise 1 to 200.
func parseLimit(raw string) (int, bool) {
	if raw == "" {
		return defaultListLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxListLimit {
		return 0, false
	}
	return n, true
}

// parseIfMatch reads `"3"`, `W/"3"` or `3`; an absent header returns 0 (no precondition).
func parseIfMatch(raw string) (int, bool) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "W/")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(strings.Trim(raw, `"`))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func etag(version int) string { return strconv.Quote(strconv.Itoa(version)) }
