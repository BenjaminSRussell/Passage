// Package api is the HTTP surface of the purchasing service under /v1/procurement.
// Every handler decodes, validates, reads the tenant from context, calls the
// store or domain, and answers with httpx.JSON or httpx.Problem.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// Problem codes raised by the HTTP layer. Domain and store codes are listed in
// the service README.
const (
	codeInvalid        = "COM-0001"
	codeAuth           = "AUTH-0001"
	codeScope          = "AUTH-0003"
	codeIdemRequired   = "PRC-IDEM-001"
	codeIfMatchMissing = "PRC-IFMATCH-001"
	codeIfMatchBad     = "PRC-IFMATCH-002"
	codeNoPrice        = "PRC-PRICE-001"
	codeBadTie         = "PRC-PRICE-002"
	codeTooManyLines   = "PRC-REQ-002"
	codeNoEstimate     = "PRC-REQ-003"
	codeNotRequester   = "PRC-REQ-004"
	codeUnknownAction  = "PRC-ACTION-001"
)

// Duties (PRD 3.17.17) enforced on the routes.
const (
	dutyRequisitionCreate      = "RequisitionCreate"
	dutyPurchaseOrderMaintain  = "PurchaseOrderMaintain"
	dutyPurchaseOrderInquire   = "PurchaseOrderInquire"
	dutyTradeAgreementMaintain = "TradeAgreementMaintain"
	dutyAgreementMaintain      = "AgreementMaintain"
)

// maxRequisitionLines is the FR-17.1 cap on lines per requisition.
const maxRequisitionLines = 500

// readyTimeout bounds the database ping behind /readyz.
const readyTimeout = 2 * time.Second

// Memberships answers which vendor groups a vendor, and which product groups a
// product, belong to. Vendor groups are owned by wave 7 and product groups by
// wave 8; until those services expose memberships, NoMemberships returns none
// and group-level trade agreement rows apply only when a lookup names groups.
type Memberships interface {
	VendorGroups(ctx context.Context, vendorID string) ([]string, error)
	ProductGroups(ctx context.Context, productID string) ([]string, error)
}

// NoMemberships is the in-package stand-in for the wave 7/8 membership lookups.
type NoMemberships struct{}

// VendorGroups implements Memberships.
func (NoMemberships) VendorGroups(context.Context, string) ([]string, error) { return nil, nil }

// ProductGroups implements Memberships.
func (NoMemberships) ProductGroups(context.Context, string) ([]string, error) { return nil, nil }

// API serves the purchasing routes over a store.
type API struct {
	store       store.Store
	policy      domain.ApprovalPolicy
	memberships Memberships
	now         func() time.Time

	requisitionsCreated   atomic.Int64
	requisitionsSubmitted atomic.Int64
	ordersCreated         atomic.Int64
	agreementsCreated     atomic.Int64
	priceLookups          atomic.Int64

	purchaseAgreementsCreated atomic.Int64

	orderEdits      atomic.Int64
	ordersConfirmed atomic.Int64
	ordersCancelled atomic.Int64

	rfqsCreated        atomic.Int64
	rfqsSent           atomic.Int64
	rfqRepliesRecorded atomic.Int64
	rfqsAwarded        atomic.Int64
	rfqVendorsScored   atomic.Int64
	sealedRFQsOpened   atomic.Int64

	invoicingPlansSaved atomic.Int64
}

// New returns the API over s using the default approval policy (FR-17.2) and
// no group memberships.
func New(s store.Store) *API {
	return &API{store: s, policy: domain.DefaultApprovalPolicy(), memberships: NoMemberships{}, now: time.Now}
}

// WithMemberships replaces the group membership source.
func (a *API) WithMemberships(m Memberships) *API {
	a.memberships = m
	return a
}

// Register mounts the routes under /v1/procurement, each behind its duty, and
// a /readyz that pings the database.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.Route("/v1/procurement", func(r chi.Router) {
		create := r.With(authz.RequireDuty(dutyRequisitionCreate))
		create.Post("/requisitions", a.createRequisition)
		create.Get("/requisitions", a.listRequisitions)
		create.Get("/requisitions/{id}", a.getRequisition)
		// Custom methods (":submit") arrive as one path segment; see requisitionAction.
		create.Post("/requisitions/{id}", a.requisitionAction)
		maintain := r.With(authz.RequireDuty(dutyPurchaseOrderMaintain))
		maintain.Post("/purchase-orders", a.createOrder)
		// The consolidation queue and :consolidate (FR-17.3).
		maintain.Get("/requisition-lines", a.listOpenRequisitionLines)
		maintain.Post("/requisitions:consolidate", a.consolidateRequisitions)
		r.With(authz.RequireDuty(dutyPurchaseOrderInquire)).Get("/purchase-orders", a.listOrders)
		inquire := r.With(authz.RequireDuty(dutyPurchaseOrderInquire))
		inquire.Get("/purchase-orders/{id}", a.getOrder)
		// PO lifecycle (FR-17.7, FR-17.8): PATCH edits or creates a change
		// version; custom methods (":submit", ":approve", ":reject", ":confirm",
		// ":cancel") arrive as one path segment and check their own duty in
		// orderAction, since approve, reject and confirm need PurchaseOrderConfirm.
		maintain.Patch("/purchase-orders/{id}", a.editOrder)
		r.Post("/purchase-orders/{id}", a.orderAction)
		inquire.Get("/purchase-orders/{id}/versions", a.orderVersions)
		inquire.Get("/purchase-orders/{id}/versions/{n}/diff", a.orderVersionDiff)
		// Invoicing plans per PO line (FR-17.21); a line amount change
		// reproportions them inside the PO edit (BR-17.17).
		inquire.Get("/purchase-orders/{id}/invoicing-plans", a.listInvoicingPlans)
		inquire.Get("/purchase-orders/{id}/lines/{lineNo}/invoicing-plan", a.getInvoicingPlan)
		maintain.Put("/purchase-orders/{id}/lines/{lineNo}/invoicing-plan", a.putInvoicingPlan)
		maintain.Delete("/purchase-orders/{id}/lines/{lineNo}/invoicing-plan", a.deleteInvoicingPlan)
		r.With(authz.RequireDuty(dutyPurchaseOrderMaintain)).Get("/pricing/lookup", a.lookupPrice)
		ta := r.With(authz.RequireDuty(dutyTradeAgreementMaintain))
		ta.Post("/trade-agreements", a.createAgreement)
		ta.Get("/trade-agreements", a.listAgreements)
		ta.Get("/trade-agreements/{id}", a.getAgreement)
		// Purchase agreements (FR-17.5); custom methods (":activate", ":hold",
		// ":close") arrive as one path segment, see purchaseAgreementAction.
		pa := r.With(authz.RequireDuty(dutyAgreementMaintain))
		pa.Post("/agreements", a.createPurchaseAgreement)
		pa.Get("/agreements", a.listPurchaseAgreements)
		pa.Get("/agreements/{id}", a.getPurchaseAgreement)
		pa.Post("/agreements/{id}", a.purchaseAgreementAction)
		// RFQ cycle (FR-17.4). Custom methods (":send", ":close", ":cancel",
		// ":award") arrive as one path segment and check their own duty in
		// rfqAction, since :award needs RFQAward.
		rfqMaintain := r.With(authz.RequireDuty(dutyRFQMaintain))
		rfqRead := r.With(authz.Require(anyDuty{dutyRFQMaintain, dutyRFQAward}, dutyRFQMaintain+" or "+dutyRFQAward, ""))
		rfqMaintain.Post("/rfqs", a.createRFQ)
		rfqRead.Get("/rfqs", a.listRFQs)
		rfqRead.Get("/rfqs/{id}", a.getRFQ)
		r.Post("/rfqs/{id}", a.rfqAction)
		rfqMaintain.Post("/rfqs/{id}/replies", a.recordRFQReplies)
		rfqRead.Get("/rfqs/{id}/compare", a.compareRFQ)
		// Buyer scoring of a vendor's reply on the RFQ's criteria (FR-17.19).
		r.With(authz.RequireDuty(dutyRFQAward)).Put("/rfqs/{id}/scores/{vendorId}", a.scoreRFQVendor)
	})
}

// Metrics serves Prometheus text counters for the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, c := range []struct {
			name, help string
			value      int64
		}{
			{"purchasing_requisitions_created_total", "Requisitions created; idempotent replays are counted.", a.requisitionsCreated.Load()},
			{"purchasing_requisitions_submitted_total", "Requisitions submitted for approval.", a.requisitionsSubmitted.Load()},
			{"purchasing_purchase_orders_created_total", "Purchase orders created.", a.ordersCreated.Load()},
			{"purchasing_trade_agreements_created_total", "Trade agreement rows created.", a.agreementsCreated.Load()},
			{"purchasing_price_lookups_total", "Trade agreement price lookups answered.", a.priceLookups.Load()},
			{"purchasing_purchase_agreements_created_total", "Purchase agreements created.", a.purchaseAgreementsCreated.Load()},
			{"purchasing_purchase_order_edits_total", "Purchase order edits accepted.", a.orderEdits.Load()},
			{"purchasing_purchase_orders_confirmed_total", "Purchase order versions confirmed.", a.ordersConfirmed.Load()},
			{"purchasing_purchase_orders_cancelled_total", "Purchase orders cancelled.", a.ordersCancelled.Load()},
			{"purchasing_rfqs_created_total", "RFQs created; idempotent replays are counted.", a.rfqsCreated.Load()},
			{"purchasing_rfqs_sent_total", "RFQs sent to vendors.", a.rfqsSent.Load()},
			{"purchasing_rfq_replies_recorded_total", "RFQ reply lines recorded.", a.rfqRepliesRecorded.Load()},
			{"purchasing_rfqs_awarded_total", "RFQ awards accepted.", a.rfqsAwarded.Load()},
			{"purchasing_rfq_vendors_scored_total", "RFQ vendor scorings recorded.", a.rfqVendorsScored.Load()},
			{"purchasing_sealed_rfqs_opened_total", "Sealed RFQs whose replies were opened after the deadline.", a.sealedRFQsOpened.Load()},
			{"purchasing_invoicing_plans_saved_total", "Invoicing plans written by PUT.", a.invoicingPlansSaved.Load()},
		} {
			fmt.Fprintf(w, "# HELP %[1]s %[2]s\n# TYPE %[1]s counter\n%[1]s %[3]d\n", c.name, c.help, c.value)
		}
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err)
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// today is the current UTC calendar day.
func (a *API) today() domain.Day {
	t := a.now().UTC()
	return domain.Day{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)}
}

// principal returns the caller, which must carry a tenant.
func principal(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok || !ids.Valid(p.TenantID) {
		return tenant.Principal{}, httpx.NewProblem(http.StatusUnauthorized, codeAuth, "a tenant is required")
	}
	return p, nil
}

// caller returns the acting user, which must be a UUID.
func caller(ctx context.Context) (string, error) {
	p, err := principal(ctx)
	if err != nil {
		return "", err
	}
	if !ids.Valid(p.UserID) {
		return "", httpx.NewProblem(http.StatusUnauthorized, codeAuth, "a user is required")
	}
	return p.UserID, nil
}

// fail writes err as a problem. Domain rule errors become 422 with their PRC
// code, except PRC-RFQ-004 (sealed replies before the deadline), which is 403
// as AC-17.14 requires.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusUnprocessableEntity
		if de.Code == domain.CodeRFQSealed {
			status = http.StatusForbidden
		}
		err = httpx.NewProblem(status, de.Code, de.Detail)
	}
	httpx.Fail(w, r, err)
}

// invalid is a field-level validation problem.
func invalid(field, msg string) error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, codeInvalid, field+" "+msg)
	p.Errors = map[string]string{field: msg}
	return p
}

// uuidField checks an optional or required UUID field.
func uuidField(field, raw string, required bool) error {
	if raw == "" {
		if required {
			return invalid(field, "is required")
		}
		return nil
	}
	if !ids.Valid(raw) {
		return invalid(field, "must be a UUID")
	}
	return nil
}

// currencyField checks an ISO 4217 style three-letter upper-case code.
func currencyField(raw string) (string, error) {
	if len(raw) != 3 {
		return "", invalid("currency", "must be a three-letter ISO 4217 code")
	}
	for _, c := range raw {
		if c < 'A' || c > 'Z' {
			return "", invalid("currency", "must be a three-letter ISO 4217 code")
		}
	}
	return raw, nil
}

// decimalField parses a decimal string, using def when raw is empty.
func decimalField(field, raw, def string) (decimal.Decimal, error) {
	if raw == "" {
		raw = def
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, invalid(field, "must be a decimal number")
	}
	return d, nil
}

// idempotencyKey reads the required Idempotency-Key header (convention: creating
// POSTs and custom methods carry one).
func idempotencyKey(r *http.Request) (string, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", httpx.NewProblem(http.StatusBadRequest, codeIdemRequired, "Idempotency-Key header is required")
	}
	if len(key) > 255 {
		return "", invalid("Idempotency-Key", "must be at most 255 characters")
	}
	return key, nil
}

// hashBody fingerprints an operation and its canonical request so reusing a key
// with another body, or on another endpoint, is caught as idempotency_key_reuse.
func hashBody(operation string, v any) string {
	b, _ := json.Marshal(v) // these DTOs always marshal
	sum := sha256.Sum256(append([]byte(operation+"\n"), b...))
	return hex.EncodeToString(sum[:])
}

// inScope rejects a write for a legal entity outside the caller's scope
// (passage.legal_entity_scopes: empty or "*" means every entity).
func inScope(p tenant.Principal, legalEntityID string) error {
	if p.LegalEntityID == "" || p.LegalEntityID == "*" {
		return nil
	}
	for _, scope := range strings.Split(p.LegalEntityID, ",") {
		if strings.TrimSpace(scope) == legalEntityID {
			return nil
		}
	}
	return httpx.NewProblem(http.StatusForbidden, codeScope, "legal entity "+legalEntityID+" is outside the caller's scope")
}

// listParams reads limit and cursor. The cursor is the id of the last row of
// the previous page.
func listParams(r *http.Request) (limit int, after string, err error) {
	q := r.URL.Query()
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > store.MaxPageSize {
			return 0, "", invalid("limit", fmt.Sprintf("must be an integer from 1 to %d", store.MaxPageSize))
		}
	}
	after = q.Get("cursor")
	if after != "" && !ids.Valid(after) {
		return 0, "", invalid("cursor", "is invalid")
	}
	return limit, after, nil
}

// pageBody is the list response: items and the cursor of the next page.
type pageBody[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func writePage[T any](w http.ResponseWriter, p store.Page[T]) {
	httpx.JSON(w, http.StatusOK, pageBody[T]{Items: p.Items, NextCursor: p.Next})
}

// pathID reads and validates the {id} path parameter.
func pathID(r *http.Request) (string, error) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		return "", invalid("id", "must be a UUID")
	}
	return id, nil
}

// etag renders a version as a quoted entity tag.
func etag(v int) string { return fmt.Sprintf(`"%d"`, v) }

// ifMatch reads the version the client read; PRC-IFMATCH-001 when absent.
func ifMatch(r *http.Request) (int, error) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, codeIfMatchMissing, "If-Match header with the current version is required")
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, httpx.NewProblem(http.StatusBadRequest, codeIfMatchBad, "If-Match must be a version number")
	}
	return v, nil
}
