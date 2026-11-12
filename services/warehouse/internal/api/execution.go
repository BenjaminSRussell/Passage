package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/store"
)

// Duties of the persisted routes: the PRD's WMS_SETUP_MAINTAIN,
// WMS_WORK_SUPERVISE, WMS_WORK_EXECUTE and WMS_INQUIRY (§6.31.17), named the
// way every Passage service names its duties.
const (
	dutySetupMaintain = "warehouse.setup.maintain"
	dutyWorkSupervise = "warehouse.work.supervise"
	dutyWorkExecute   = "warehouse.work.execute"
	dutyInquiry       = "warehouse.inquiry.view"
)

const (
	maxBulkLocations = 1000
	maxWorkLines     = 500
	maxKeyLength     = 200
	maxReasonLength  = 200
	maxTextLength    = 60
	maxZoneLength    = 40
	defaultPageSize  = 50
	headerKey        = "Idempotency-Key"
	headerReplayed   = "Idempotent-Replayed"
	maxBody          = 4 << 20
)

// Error codes of the persisted routes (README "Error codes").
const (
	codeValidation       = "COM-0001"
	codeTenant           = "AUTH-0001"
	codeWorkNotFound     = "WMS-WORK-NOT-FOUND"
	codeLineNotFound     = "WMS-LINE-NOT-FOUND"
	codeLocationNotFound = "WMS-LOCATION-NOT-FOUND"
	codeDuplicate        = "WMS-DUPLICATE"
	codeVersionConflict  = "WMS-VERSION-CONFLICT"
	codeNotAssignee      = "WMS-NOT-ASSIGNEE"
	codeIdempotencyReuse = "idempotency_key_reuse"
)

// Store is what the persisted routes need from the database; *store.Store
// implements it.
type Store interface {
	CreateLocations(ctx context.Context, idem store.Idem, warehouseID string, locs []domain.Location) ([]domain.Location, bool, error)
	ListLocations(ctx context.Context, f store.LocationFilter) ([]domain.Location, error)
	CreateDirective(ctx context.Context, idem store.Idem, d domain.Directive) (domain.Directive, bool, error)
	ListDirectives(ctx context.Context, warehouseID, workType, direction string) ([]domain.Directive, error)
	PlanWork(ctx context.Context, idem store.Idem, in store.PlanInput) (store.Planned, bool, error)
	ListWork(ctx context.Context, f store.WorkFilter) ([]domain.Work, error)
	GetWork(ctx context.Context, id string) (domain.Work, error)
	AssignNext(ctx context.Context, idem store.Idem, warehouseID, user string, timeout time.Duration, now time.Time) (store.Assigned, bool, error)
	ConfirmLine(ctx context.Context, idem store.Idem, in store.ConfirmInput) (domain.Work, bool, error)
	ExceptWork(ctx context.Context, idem store.Idem, in store.ExceptionInput) (store.Excepted, bool, error)
	CreateDoor(ctx context.Context, idem store.Idem, d domain.DockDoor) (domain.DockDoor, bool, error)
	ListDoors(ctx context.Context, warehouseID string) ([]domain.DockDoor, error)
	CreateAppointment(ctx context.Context, idem store.Idem, a domain.Appointment) (domain.Appointment, bool, error)
	ListAppointments(ctx context.Context, f store.AppointmentFilter) ([]domain.Appointment, error)
	GetAppointment(ctx context.Context, id string) (domain.Appointment, error)
	RegisterASN(ctx context.Context, idem store.Idem, r domain.Receipt) (domain.Receipt, bool, error)
	ListReceipts(ctx context.Context, f store.ReceiptFilter) ([]domain.ReceiptSummary, error)
	GetReceipt(ctx context.Context, id string) (domain.Receipt, error)
	ReceiveLine(ctx context.Context, idem store.Idem, in store.ReceiveInput) (store.Received, bool, error)
	IngestRFID(ctx context.Context, reads []store.RFIDRead) (store.RFIDIngest, error)
	ListRFIDReads(ctx context.Context, f store.RFIDFilter) ([]store.RFIDReadRow, error)
	CreateAutomationOrder(ctx context.Context, in store.OrderInput) (store.AutomationOrder, error)
	ListAutomationOrders(ctx context.Context, f store.OrderFilter) ([]store.AutomationOrder, error)
	ReportAutomationOrder(ctx context.Context, id, status, reason string, now time.Time) (store.AutomationOrder, error)
	IngestAutomationEvents(ctx context.Context, station string, events []store.AutomationEvent, now time.Time) (store.AutomationIngest, error)
	GetGS1Company(ctx context.Context) (store.GS1Company, error)
	GetPalletLabel(ctx context.Context, sscc string) (store.PalletLabel, error)
	PutGS1Company(ctx context.Context, prefix string, extension int, by string) (store.GS1Company, error)
	ConfirmShipped(ctx context.Context, idem store.Idem, c store.ShipConfirmation) (store.ShipConfirmation, bool, error)
	GetShipConfirmation(ctx context.Context, shipmentID string) (store.ShipConfirmation, error)
}

// registerPersisted mounts the routes that read and write warehouse state.
// Each one first checks that the tenant is a UUID, as row-level security needs.
func (a *API) registerPersisted(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(requireTenant)
		r.With(authz.RequireDuty(dutySetupMaintain)).Post("/v1/warehouse/locations:bulk", a.createLocations)
		r.With(authz.RequireDuty(dutySetupView)).Get("/v1/warehouse/locations", a.listLocations)
		r.With(authz.RequireDuty(dutySetupMaintain)).Post("/v1/warehouse/location-directives", a.createDirective)
		r.With(authz.RequireDuty(dutySetupView)).Get("/v1/warehouse/location-directives", a.listDirectives)
		r.With(authz.RequireDuty(dutyWorkSupervise)).Post("/v1/warehouse/work", a.createWork)
		r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/work", a.listWork)
		r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/work/{id}", a.getWork)
		a.registerAutomation(r)
		r.With(authz.RequireDuty(dutyWorkExecute)).Post("/v1/warehouse/work:assign", a.assignWork)
		r.With(authz.RequireDuty(dutyWorkExecute)).Post("/v1/warehouse/work/{id}/lines/{n}:confirm", a.confirmLine)
		r.With(authz.RequireDuty(dutyWorkExecute)).Post("/v1/warehouse/work/{id}:exception", a.exceptWork)
		a.registerReceiving(r)
		a.registerShipped(r)
	})
}

// requireTenant refuses a request whose tenant is not a UUID with 401.
func requireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := tenant.FromContext(r.Context()); !ok || !ids.Valid(p.TenantID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, codeTenant, "missing or malformed tenant"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fail maps store and domain errors to problem details. notFound is the code
// of a missing row on this route.
func fail(w http.ResponseWriter, r *http.Request, err error, notFound string) {
	var de *domain.Error
	switch {
	case errors.As(err, &de):
		status := http.StatusUnprocessableEntity
		switch de.Code {
		case domain.CodeLineConfirmed, domain.CodeIllegalTransition, domain.CodeDoorOverlap, domain.CodeLineReceived, domain.CodeGS1Locked, domain.CodeGS1Exhausted:
			status = http.StatusConflict
		}
		httpx.Fail(w, r, httpx.NewProblem(status, de.Code, de.Detail))
	case errors.Is(err, domain.ErrLineNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeLineNotFound, "the work has no such line"))
	case errors.Is(err, domain.ErrPalletNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeLineNotFound, "the receipt has no such line"))
	case errors.Is(err, domain.ErrReceivedQty):
		validationFailed(w, r, map[string]string{"qty": "must be greater than zero"})
	case errors.Is(err, store.ErrGS1NotSet):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeGS1NotSet, "no GS1 company prefix is set"))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, notFound, "not found"))
	case errors.Is(err, store.ErrConflict):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeDuplicate, "a record with the same name, sequence, door code or ASN reference already exists"))
	case errors.Is(err, store.ErrVersionConflict):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeVersionConflict, err.Error()))
	case errors.Is(err, store.ErrIdempotencyReuse):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeIdempotencyReuse, "Idempotency-Key was already used with a different request"))
	case errors.Is(err, domain.ErrNotAssignee):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeNotAssignee, err.Error()))
	case errors.Is(err, domain.ErrReasonRequired):
		validationFailed(w, r, map[string]string{"reason": "is required"})
	default:
		httpx.Fail(w, r, err)
	}
}

// command reads a command's body into v and returns its replay identity: the
// Idempotency-Key header and a hash of method, path, user and body. A missing
// or oversized key is a field error.
func command(r *http.Request, v any, fields map[string]string) (store.Idem, error) {
	key := r.Header.Get(headerKey)
	if key == "" || len(key) > maxKeyLength {
		fields[headerKey] = fmt.Sprintf("is required, at most %d characters", maxKeyLength)
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return store.Idem{}, httpx.NewProblem(http.StatusBadRequest, codeValidation, "request body is too large or unreadable")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return store.Idem{}, httpx.NewProblem(http.StatusBadRequest, codeValidation, "malformed JSON body: "+err.Error())
	}
	p, _ := tenant.FromContext(r.Context())
	h := sha256.New()
	for _, part := range []string{r.Method, r.URL.Path, p.UserID, string(body)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return store.Idem{Key: key, Hash: hex.EncodeToString(h.Sum(nil))}, nil
}

// answer writes a command's result; a replay keeps the first status and says so.
func answer(w http.ResponseWriter, status int, replayed bool, v any) {
	if replayed {
		w.Header().Set(headerReplayed, "true")
	}
	httpx.JSON(w, status, v)
}

// ifMatch reads the optional If-Match header: a work version as "3" or 3.
func ifMatch(r *http.Request, fields map[string]string) int {
	return ifMatchVersion(r, fields, "work")
}

// ifMatchVersion reads the optional If-Match header: the version of the
// command's record (what names it in the error) as "3" or 3.
func ifMatchVersion(r *http.Request, fields map[string]string, what string) int {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		fields["If-Match"] = "must be the " + what + " version, a positive integer"
		return 0
	}
	return n
}

// pageLimit reads ?limit= (1 to store.MaxPageSize, default 50).
func pageLimit(r *http.Request, fields map[string]string) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultPageSize
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > store.MaxPageSize {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", store.MaxPageSize)
	}
	return n
}

// page is a list response; NextCursor is set when another page may follow.
type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func newPage[T any](items []T, limit int, cursor func(T) string) page[T] {
	if items == nil {
		items = []T{}
	}
	p := page[T]{Items: items}
	if len(items) == limit && limit > 0 {
		p.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(cursor(items[len(items)-1])))
	}
	return p
}

func decodeCursor(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	return string(b), err == nil
}

// locationInput is a location to create. The server assigns the id and starts
// usage at zero with no stock: stock arrives through confirmed PUT work.
type locationInput struct {
	Name                  string                `json:"name"`
	Zone                  string                `json:"zone"`
	PickSequence          int                   `json:"pickSequence"`
	Status                domain.LocationStatus `json:"status"`
	Capacity              domain.Capacity       `json:"capacity"`
	FireSection           string                `json:"fireSection"`
	RackGroup             string                `json:"rackGroup"`
	AllowedStorageClasses []string              `json:"allowedStorageClasses"`
}

type locationsRequest struct {
	WarehouseID string          `json:"warehouseId"`
	Locations   []locationInput `json:"locations"`
}

func (a *API) createLocations(w http.ResponseWriter, r *http.Request) {
	var req locationsRequest
	fields := map[string]string{}
	idem, err := command(r, &req, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.WarehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	if n := len(req.Locations); n == 0 || n > maxBulkLocations {
		fields["locations"] = fmt.Sprintf("must hold 1 to %d locations", maxBulkLocations)
	}
	locs := make([]domain.Location, 0, len(req.Locations))
	names := map[string]bool{}
	for i, in := range req.Locations {
		path := fmt.Sprintf("locations[%d]", i)
		status := in.Status
		if status == "" {
			status = domain.StatusActive
		}
		l := domain.Location{ID: ids.New(), Name: strings.TrimSpace(in.Name), Zone: strings.TrimSpace(in.Zone), PickSequence: in.PickSequence,
			Status: status, Capacity: in.Capacity, FireSection: in.FireSection, RackGroup: in.RackGroup, AllowedStorageClasses: in.AllowedStorageClasses}
		validateLocation(fields, path, l)
		if len(l.Name) > maxTextLength {
			fields[path+".name"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
		}
		if len(l.Zone) > maxZoneLength {
			fields[path+".zone"] = fmt.Sprintf("must be at most %d characters", maxZoneLength)
		}
		if l.PickSequence < 0 {
			fields[path+".pickSequence"] = "must not be negative"
		}
		if l.Name != "" && names[l.Name] {
			fields[path+".name"] = "is used by another location in this request"
		}
		names[l.Name] = true
		locs = append(locs, l)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.CreateLocations(r.Context(), idem, req.WarehouseID, locs)
	if err != nil {
		fail(w, r, err, codeLocationNotFound)
		return
	}
	answer(w, http.StatusCreated, replayed, page[domain.Location]{Items: out})
}

func (a *API) listLocations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("warehouseId")) {
		fields["warehouseId"] = "must be a UUID"
	}
	limit := pageLimit(r, fields)
	after, ok := decodeCursor(q.Get("cursor"))
	if !ok {
		fields["cursor"] = "is invalid"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.ListLocations(r.Context(), store.LocationFilter{WarehouseID: q.Get("warehouseId"), Zone: q.Get("zone"), AfterName: after, Limit: limit})
	if err != nil {
		fail(w, r, err, codeLocationNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, newPage(out, limit, func(l domain.Location) string { return l.Name }))
}

// directiveInput is a directive to create; the server assigns the id.
type directiveInput struct {
	WarehouseID string           `json:"warehouseId"`
	Sequence    int              `json:"sequence"`
	WorkType    string           `json:"workType"`
	Direction   domain.Direction `json:"direction"`
	Query       domain.Query     `json:"query"`
	Lines       []domain.Band    `json:"lines"`
}

func (a *API) createDirective(w http.ResponseWriter, r *http.Request) {
	var in directiveInput
	fields := map[string]string{}
	idem, err := command(r, &in, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d := domain.Directive{ID: ids.New(), Sequence: in.Sequence, WorkType: in.WorkType, Direction: in.Direction,
		Warehouse: in.WarehouseID, Query: in.Query, Bands: in.Lines}
	validateDirective(fields, "directive", d)
	for k, v := range fields {
		if rest, ok := strings.CutPrefix(k, "directive."); ok {
			delete(fields, k)
			fields[rest] = v
		}
	}
	if in.Sequence < 1 {
		fields["sequence"] = "must be a positive integer"
	}
	if !domain.ValidWorkType(in.WorkType) {
		fields["workType"] = "is not a warehouse work type"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.CreateDirective(r.Context(), idem, d)
	if err != nil {
		fail(w, r, err, codeLocationNotFound)
		return
	}
	answer(w, http.StatusCreated, replayed, out)
}

func (a *API) listDirectives(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("warehouseId")) {
		fields["warehouseId"] = "must be a UUID"
	}
	if dir := q.Get("direction"); dir != "" && !domain.ValidDirection(domain.Direction(dir)) {
		fields["direction"] = "must be PUT or PICK"
	}
	if wt := q.Get("workType"); wt != "" && !domain.ValidWorkType(wt) {
		fields["workType"] = "is not a warehouse work type"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.ListDirectives(r.Context(), q.Get("warehouseId"), q.Get("workType"), q.Get("direction"))
	if err != nil {
		fail(w, r, err, codeLocationNotFound)
		return
	}
	if out == nil {
		out = []domain.Directive{}
	}
	httpx.JSON(w, http.StatusOK, page[domain.Directive]{Items: out})
}

type workLineInput struct {
	ItemID     string            `json:"itemId"`
	Direction  domain.Direction  `json:"direction"`
	Qty        decimal.Decimal   `json:"qty"`
	Weight     decimal.Decimal   `json:"weight"`
	Volume     decimal.Decimal   `json:"volume"`
	LPs        int               `json:"lps"`
	Attributes map[string]string `json:"attributes"`
	// OrderLineID is the sales order line a PICK serves; it becomes the line's
	// issueRef attribute, the ref fulfillment's ship claim and the DELIVERED
	// booking of the line use, so inventory counts the pick and the ship once.
	OrderLineID string `json:"orderLineId"`
}

type workRequest struct {
	WarehouseID string          `json:"warehouseId"`
	WorkType    string          `json:"workType"`
	Priority    int             `json:"priority"`
	Lines       []workLineInput `json:"lines"`
}

func (a *API) createWork(w http.ResponseWriter, r *http.Request) {
	var req workRequest
	fields := map[string]string{}
	idem, err := command(r, &req, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.WarehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	if !domain.ValidWorkType(req.WorkType) {
		fields["workType"] = "is not a warehouse work type"
	}
	if req.Priority != 0 && (req.Priority < 1 || req.Priority > 99) {
		fields["priority"] = "must be 1 (highest) to 99"
	}
	if n := len(req.Lines); n == 0 || n > maxWorkLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxWorkLines)
	}
	reqs := make([]domain.NewLine, 0, len(req.Lines))
	for i, in := range req.Lines {
		p := fmt.Sprintf("lines[%d]", i)
		item := strings.TrimSpace(in.ItemID)
		if item == "" || len(item) > maxTextLength {
			fields[p+".itemId"] = fmt.Sprintf("is required, at most %d characters", maxTextLength)
		}
		if !domain.ValidDirection(in.Direction) {
			fields[p+".direction"] = "must be PUT or PICK"
		}
		if !in.Qty.IsPositive() {
			fields[p+".qty"] = "must be greater than zero"
		}
		if in.Weight.IsNegative() {
			fields[p+".weight"] = "must not be negative"
		}
		if in.Volume.IsNegative() {
			fields[p+".volume"] = "must not be negative"
		}
		if in.LPs < 0 {
			fields[p+".lps"] = "must not be negative"
		}
		attrs := in.Attributes
		if ref := attrs[issueclaim.RefAttribute]; ref != "" && !issueclaim.CanonicalRef(ref) {
			fields[p+".attributes.issueRef"] = "must be SALES_LINE:<order line id> or PRODUCTION_ORDER:<order id>:<item id>, ids in lower case"
		}
		if in.OrderLineID != "" {
			switch {
			case in.Direction != domain.DirectionPick || !issueclaim.CanonicalRef(issueclaim.SalesLineRef(in.OrderLineID)):
				fields[p+".orderLineId"] = "must be a sales order line id, on a PICK line"
			case attrs[issueclaim.RefAttribute] != "":
				fields[p+".orderLineId"] = "and the issueRef attribute name the same thing: give one"
			default:
				attrs = maps.Clone(attrs)
				if attrs == nil {
					attrs = map[string]string{}
				}
				attrs[issueclaim.RefAttribute] = issueclaim.SalesLineRef(in.OrderLineID)
			}
		}
		reqs = append(reqs, domain.NewLine{ItemID: item, Direction: in.Direction, Qty: in.Qty,
			Weight: in.Weight, Volume: in.Volume, LPs: in.LPs, Attributes: attrs})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.PlanWork(r.Context(), idem, store.PlanInput{WarehouseID: req.WarehouseID, WorkType: req.WorkType,
		Priority: req.Priority, Lines: reqs})
	if err != nil {
		fail(w, r, err, codeWorkNotFound)
		return
	}
	if out.Traces == nil {
		out.Traces = []domain.Result{}
	}
	if !replayed {
		a.workCreated.Add(1)
		if out.Work.Status == domain.WorkBlockedNoLocation {
			a.workBlocked.Add(1)
		}
	}
	answer(w, http.StatusCreated, replayed, out)
}

var workStatuses = []domain.WorkStatus{domain.WorkOpen, domain.WorkAssigned, domain.WorkInProgress, domain.WorkCompleted,
	domain.WorkPartiallyComplete, domain.WorkCancelled, domain.WorkBlockedNoLocation}

func encodeWorkKey(w domain.Work) string {
	return fmt.Sprintf("%d/%s/%s", w.Priority, w.CreatedAt.UTC().Format(time.RFC3339Nano), w.ID)
}

func decodeWorkKey(s string) (*store.WorkKey, bool) {
	if s == "" {
		return nil, true
	}
	parts := strings.Split(s, "/")
	if len(parts) != 3 || !ids.Valid(parts[2]) {
		return nil, false
	}
	p, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, false
	}
	at, err := time.Parse(time.RFC3339Nano, parts[1])
	if err != nil {
		return nil, false
	}
	return &store.WorkKey{Priority: p, CreatedAt: at, ID: parts[2]}, true
}

func (a *API) listWork(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("warehouseId")) {
		fields["warehouseId"] = "must be a UUID"
	}
	if s := q.Get("status"); s != "" {
		known := false
		for _, st := range workStatuses {
			known = known || string(st) == s
		}
		if !known {
			fields["status"] = "is not a work status"
		}
	}
	limit := pageLimit(r, fields)
	raw, ok := decodeCursor(q.Get("cursor"))
	after, valid := decodeWorkKey(raw)
	if !ok || !valid {
		fields["cursor"] = "is invalid"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.st.ListWork(r.Context(), store.WorkFilter{WarehouseID: q.Get("warehouseId"), Status: q.Get("status"), After: after, Limit: limit})
	if err != nil {
		fail(w, r, err, codeWorkNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, newPage(out, limit, encodeWorkKey))
}

func (a *API) getWork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeWorkNotFound, "not found"))
		return
	}
	out, err := a.st.GetWork(r.Context(), id)
	if err != nil {
		fail(w, r, err, codeWorkNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type assignRequest struct {
	WarehouseID string `json:"warehouseId"`
}

// user returns the calling user, or records a field error when the gateway
// sent none: work is assigned to and executed by a named user.
func user(r *http.Request, fields map[string]string) string {
	p, _ := tenant.FromContext(r.Context())
	if strings.TrimSpace(p.UserID) == "" {
		fields[tenant.HeaderUser] = "the calling user is required"
	}
	return p.UserID
}

// assignWork gives the caller the next work of the warehouse; work is null
// when nothing is assignable.
func (a *API) assignWork(w http.ResponseWriter, r *http.Request) {
	var req assignRequest
	fields := map[string]string{}
	idem, err := command(r, &req, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.WarehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	who := user(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.AssignNext(r.Context(), idem, req.WarehouseID, who, a.lockTimeout, time.Now())
	if err != nil {
		fail(w, r, err, codeWorkNotFound)
		return
	}
	if !replayed && out.Work != nil {
		a.assignments.Add(1)
	}
	answer(w, http.StatusOK, replayed, out)
}

type confirmRequest struct {
	Qty decimal.Decimal `json:"qty"`
}

// workTarget validates the {id} path parameter of a work command.
func workTarget(r *http.Request, fields map[string]string) string {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	return id
}

// confirmLine confirms line n at its open quantity (ConfirmWorkLine).
func (a *API) confirmLine(w http.ResponseWriter, r *http.Request) {
	var req confirmRequest
	fields := map[string]string{}
	idem, err := command(r, &req, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	id := workTarget(r, fields)
	n, convErr := strconv.Atoi(chi.URLParam(r, "n"))
	if convErr != nil || n < 1 {
		fields["n"] = "must be a positive line number"
	}
	if !req.Qty.IsPositive() {
		fields["qty"] = "must be greater than zero"
	}
	version := ifMatch(r, fields)
	who := user(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.ConfirmLine(r.Context(), idem, store.ConfirmInput{WorkID: id, LineNo: n, Qty: req.Qty, User: who,
		IfMatch: version, Now: time.Now()})
	if err != nil {
		fail(w, r, err, codeWorkNotFound)
		return
	}
	answer(w, http.StatusOK, replayed, out)
}

type exceptionRequest struct {
	Action       string          `json:"action"`
	LineNo       int             `json:"lineNo"`
	ConfirmedQty decimal.Decimal `json:"confirmedQty"`
	Reason       string          `json:"reason"`
}

// exceptWork runs SHORT_PICK or CANCEL on a work (FR-31.7, BR-31.3).
func (a *API) exceptWork(w http.ResponseWriter, r *http.Request) {
	var req exceptionRequest
	fields := map[string]string{}
	idem, err := command(r, &req, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	id := workTarget(r, fields)
	switch req.Action {
	case store.ActionShortPick:
		if req.LineNo < 1 {
			fields["lineNo"] = "must be a positive line number"
		}
		if req.ConfirmedQty.IsNegative() {
			fields["confirmedQty"] = "must not be negative"
		}
	case store.ActionCancel:
	default:
		fields["action"] = "must be SHORT_PICK or CANCEL"
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len(reason) > maxReasonLength {
		fields["reason"] = fmt.Sprintf("is required, at most %d characters", maxReasonLength)
	}
	version := ifMatch(r, fields)
	who := user(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	out, replayed, err := a.st.ExceptWork(r.Context(), idem, store.ExceptionInput{WorkID: id, Action: req.Action, LineNo: req.LineNo,
		Qty: req.ConfirmedQty, Reason: reason, User: who, Supervise: p.HasDuty(dutyWorkSupervise), IfMatch: version, Now: time.Now()})
	if err != nil {
		fail(w, r, err, codeWorkNotFound)
		return
	}
	answer(w, http.StatusOK, replayed, out)
}
