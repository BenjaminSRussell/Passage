// Package store is the Postgres persistence for the purchasing service. Every
// transaction runs through dbx.WithTenantTx so row-level security applies.
package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// Problem codes raised by the store. The README lists them with their HTTP status.
const (
	CodeRequisitionNotFound     = "PRC-REQ-NOT-FOUND"
	CodePurchaseOrderNotFound   = "PRC-PO-NOT-FOUND"
	CodeTradeAgreementNotFound  = "PRC-TA-NOT-FOUND"
	CodeRequisitionState        = "PRC-REQ-STATE"
	CodeRequisitionLineNotFound = "PRC-REQ-LINE-NOT-FOUND"
	CodeRequisitionLegalEntity  = "PRC-REQ-005"
	// CodeRequisitionOnRFQ is the 409 for a requisition line a running RFQ is sourcing (FR-17.3).
	CodeRequisitionOnRFQ  = "PRC-REQ-006"
	CodeAgreementNotFound = "PRC-AGR-NOT-FOUND"
	CodeVersionConflict   = "PRC-VERSION-CONFLICT"
	CodeIdempotencyReuse  = "idempotency_key_reuse"
	eventSource           = "/passage/procurement/purchasing"
)

// Page sizes for the list endpoints.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Store is the persistence surface the API depends on.
type Store interface {
	CreateTradeAgreement(ctx context.Context, row domain.TradeAgreement, idem Idem) (domain.TradeAgreement, error)
	GetTradeAgreement(ctx context.Context, id string) (domain.TradeAgreement, error)
	ListTradeAgreements(ctx context.Context, f TradeAgreementFilter, after string, limit int) (Page[domain.TradeAgreement], error)
	PriceCandidates(ctx context.Context, f PriceFilter) ([]domain.TradeAgreement, error)
	CreateRequisition(ctx context.Context, in Requisition, idem Idem) (Requisition, error)
	GetRequisition(ctx context.Context, id string) (Requisition, error)
	ListRequisitions(ctx context.Context, f RequisitionFilter, after string, limit int) (Page[RequisitionSummary], error)
	SubmitRequisition(ctx context.Context, id string, version int, chain []string, policyVersion string, idem Idem) (Requisition, error)
	CreatePurchaseOrder(ctx context.Context, in PurchaseOrder, idem Idem) (PurchaseOrder, error)
	GetPurchaseOrder(ctx context.Context, id string) (PurchaseOrder, error)
	ListPurchaseOrders(ctx context.Context, f PurchaseOrderFilter, after string, limit int) (Page[PurchaseOrderSummary], error)
	EditPurchaseOrder(ctx context.Context, id string, ifMatch int, next PurchaseOrder) (PurchaseOrder, error)
	ActOnPurchaseOrder(ctx context.Context, id string, ifMatch int, action string, idem Idem) (PurchaseOrder, error)
	RepriceCandidates(ctx context.Context, f RepriceFilter, after string, limit int) (Page[string], error)
	RepricePurchaseOrder(ctx context.Context, id string, ifMatch int, next PurchaseOrder, plan domain.RepricePlan, idem Idem) (RepriceOutcome, error)
	ListPurchaseOrderVersions(ctx context.Context, poID string) ([]PurchaseOrderVersion, error)
	GetPurchaseOrderVersion(ctx context.Context, poID string, no int) (PurchaseOrderVersion, error)
	CreateAgreement(ctx context.Context, in domain.Agreement, idem Idem) (AgreementDetail, error)
	GetAgreement(ctx context.Context, id string) (AgreementDetail, error)
	GetAgreementLine(ctx context.Context, lineID string) (domain.Agreement, error)
	ListAgreements(ctx context.Context, f AgreementFilter, after string, limit int) (Page[AgreementSummary], error)
	ChangeAgreementStatus(ctx context.Context, id string, version int, action string, idem Idem) (AgreementDetail, error)
	ListOpenRequisitionLines(ctx context.Context, f SourcingFilter, after string, limit int) (Page[SourcingLine], error)
	RequisitionLinesByID(ctx context.Context, lineIDs []string) ([]SourcingLine, error)
	ReleaseRequisitions(ctx context.Context, in ReleaseInput, idem Idem) (ReleaseRun, error)
	GetReleaseRun(ctx context.Context, id string) (ReleaseRun, error)
	ListReleaseRuns(ctx context.Context, f ReleaseRunFilter, after string, limit int) (Page[ReleaseRun], error)
	SplitRequisitionLine(ctx context.Context, lineID string, version int, quantities []decimal.Decimal, minor int32, splitBy string, idem Idem) (Requisition, error)
	ListOpenOrderLines(ctx context.Context, f OpenLineFilter, after string, limit int) (Page[OpenOrderLine], error)
	ListOpenOrdersByVendor(ctx context.Context, f InquiryFilter, after string, limit int) (Page[VendorOpenOrders], error)
	ListOverdueLines(ctx context.Context, f InquiryFilter, after string, limit int) (Page[OverdueLine], error)
	ListBackorderedLines(ctx context.Context, f InquiryFilter, after string, limit int) (Page[BackorderedLine], error)
	ListExpiringAgreements(ctx context.Context, f InquiryFilter, after string, limit int) (Page[ExpiringAgreement], error)
	ListNearLimitAgreementLines(ctx context.Context, f InquiryFilter, after string, limit int) (Page[NearLimitLine], error)
	ListChangeOrders(ctx context.Context, f InquiryFilter, after string, limit int) (Page[ChangeOrder], error)
	ListPriceHistory(ctx context.Context, f InquiryFilter, after string, limit int) (Page[PriceHistoryEntry], error)
	ConsolidateRequisitionLines(ctx context.Context, orders []PurchaseOrder, lineIDs []string, idem Idem) ([]PurchaseOrder, error)
	CreateRFQ(ctx context.Context, in domain.RFQ, idem Idem) (domain.RFQ, error)
	GetRFQ(ctx context.Context, id string) (domain.RFQ, error)
	ListRFQs(ctx context.Context, f RFQFilter, after string, limit int) (Page[RFQSummary], error)
	SendRFQ(ctx context.Context, id string, ifMatch int, vendorIDs []string, idem Idem) (domain.RFQ, error)
	ActOnRFQ(ctx context.Context, id string, ifMatch int, action string, idem Idem) (domain.RFQ, error)
	RecordRFQReplies(ctx context.Context, rfqID, vendorID string, replies []domain.RFQReply, idem Idem) (domain.RFQ, error)
	AwardRFQ(ctx context.Context, id string, ifMatch int, awards []domain.Award, orders []AwardedOrder, today domain.Day, idem Idem) (domain.RFQ, []PurchaseOrder, error)
	SetRFQScores(ctx context.Context, id string, ifMatch int, vendorID string, scores []domain.VendorScore, now time.Time) (domain.RFQ, error)
	OpenSealedRFQ(ctx context.Context, id, userID string, at time.Time) (domain.RFQ, error)
	PutInvoicingPlan(ctx context.Context, poID string, lineNo, ifMatch int, in InvoicingPlanInput) (PlanResult, error)
	GetInvoicingPlan(ctx context.Context, poID string, lineNo int) (PlanResult, error)
	ListInvoicingPlans(ctx context.Context, poID string) ([]domain.InvoicingPlan, error)
	DeleteInvoicingPlan(ctx context.Context, poID string, lineNo, ifMatch int) (int, error)
	PutDeliverySchedule(ctx context.Context, poID string, lineNo, ifMatch int, in []domain.ScheduleInput) (ScheduleResult, error)
	GetDeliverySchedule(ctx context.Context, poID string, lineNo int) (ScheduleResult, error)
	ListDeliverySchedules(ctx context.Context, poID string) ([]domain.DeliverySchedule, error)
	DeleteDeliverySchedule(ctx context.Context, poID string, lineNo, ifMatch int) (int, error)
	PutServiceSpec(ctx context.Context, poID string, lineNo, ifMatch int, in ServiceInput) (ServiceResult, error)
	GetServiceSpec(ctx context.Context, poID string, lineNo int) (ServiceResult, error)
	ListServiceSpecs(ctx context.Context, poID string) ([]domain.ServiceSpec, error)
	DeleteServiceSpec(ctx context.Context, poID string, lineNo, ifMatch int) (int, error)
	CheckServiceLimit(ctx context.Context, poID string, lineNo int, unplanned, entry decimal.Decimal) (domain.LimitCheck, error)
	CreateModelServiceSpec(ctx context.Context, in domain.ModelService, nodes []domain.OutlineNode, idem Idem) (domain.ModelService, error)
	UpdateModelServiceSpec(ctx context.Context, id string, ifMatch int, in domain.ModelService, nodes []domain.OutlineNode) (domain.ModelService, error)
	GetModelServiceSpec(ctx context.Context, id string) (domain.ModelService, error)
	ListModelServiceSpecs(ctx context.Context, f ModelServiceFilter, after string, limit int) (Page[domain.ModelService], error)
	ConfirmDeliveryDate(ctx context.Context, poID string, lineNo, seq, ifMatch int, date domain.Day) (ScheduleResult, error)
	MarkPlanRowInvoiced(ctx context.Context, poID string, lineNo, seq int, ref string, amount decimal.Decimal) (PlanRowSettlement, error)
	CreateSchedulingAgreement(ctx context.Context, in domain.SchedulingAgreement, idem Idem) (SchedulingAgreementDetail, error)
	GetSchedulingAgreement(ctx context.Context, id string) (SchedulingAgreementDetail, error)
	ListSchedulingAgreements(ctx context.Context, f SchedulingAgreementFilter, after string, limit int) (Page[SAMonitorRow], error)
	ReplaceSchedule(ctx context.Context, id string, ifMatch int, asOf domain.Day, changes []domain.ScheduleChange) (SchedulingAgreementDetail, []string, error)
	RunSchedulingRelease(ctx context.Context, id, kind string, runDate domain.Day, reset bool, idem Idem) (domain.SARelease, bool, error)
	ListSchedulingReleases(ctx context.Context, agreementID string) ([]domain.SARelease, error)
	GetSchedulingRelease(ctx context.Context, agreementID string, releaseNo int) (domain.SARelease, error)
	GetPolicy(ctx context.Context, legalEntityID string) (domain.Policy, error)
	ListPolicies(ctx context.Context) ([]domain.Policy, error)
	PutPolicy(ctx context.Context, in domain.Policy, ifMatch int) (domain.Policy, error)
	CreateCatalog(ctx context.Context, in domain.Catalog, idem Idem) (domain.Catalog, error)
	GetCatalog(ctx context.Context, id string) (domain.Catalog, error)
	ListCatalogs(ctx context.Context, f CatalogFilter, after string, limit int) (Page[CatalogSummary], error)
	AddCatalogEntries(ctx context.Context, id string, ifMatch int, entries []domain.CatalogEntry, idem Idem) (domain.Catalog, error)
	RemoveCatalogEntry(ctx context.Context, id, entryID string, ifMatch int) (domain.Catalog, error)
	ActOnCatalog(ctx context.Context, id string, ifMatch int, action string, idem Idem) (domain.Catalog, error)
	SearchCatalogEntries(ctx context.Context, f CatalogSearch, after string, limit int) (Page[CatalogHit], error)
	GetCatalogEntry(ctx context.Context, entryID string) (domain.Catalog, domain.CatalogEntry, error)
	ListCatalogEntries(ctx context.Context, id string, afterNo, limit int) (CatalogEntryPage, error)
	StageVendorImport(ctx context.Context, catalogID string, in ImportInput, idem Idem) (domain.CatalogImport, error)
	GetCatalogImport(ctx context.Context, id string) (domain.CatalogImport, error)
	ListCatalogImports(ctx context.Context, f ImportFilter, after string, limit int) (Page[domain.CatalogImport], error)
	ListImportRows(ctx context.Context, importID, status string, afterRow, limit int) (ImportRowPage, error)
	DecideImportRows(ctx context.Context, id string, ifMatch int, d ImportDecisions, idem Idem) (domain.CatalogImport, error)
	ActOnCatalogImport(ctx context.Context, id string, ifMatch int, action string, idem Idem) (domain.CatalogImport, error)
	CreateCategoryVendor(ctx context.Context, in domain.CategoryVendor, idem Idem) (domain.CategoryVendor, error)
	GetCategoryVendor(ctx context.Context, id string) (domain.CategoryVendor, error)
	ListCategoryVendors(ctx context.Context, f CategoryVendorFilter, after string, limit int) (Page[domain.CategoryVendor], error)
	UpdateCategoryVendor(ctx context.Context, id string, ifMatch int, status string, from domain.Day, to *domain.Day, notes string) (domain.CategoryVendor, error)
	CategoryAssignments(ctx context.Context, legalEntityID string, categories []string) ([]domain.CategoryVendor, error)
	CreateChargeCode(ctx context.Context, in domain.ChargeCode, idem Idem) (domain.ChargeCode, error)
	GetChargeCode(ctx context.Context, id string) (domain.ChargeCode, error)
	ListChargeCodes(ctx context.Context, f ChargeCodeFilter, after string, limit int) (Page[domain.ChargeCode], error)
	UpdateChargeCode(ctx context.Context, id string, ifMatch int, in domain.ChargeCode) (domain.ChargeCode, error)
	CreateAutoCharge(ctx context.Context, in domain.AutoCharge, idem Idem) (domain.AutoCharge, error)
	GetAutoCharge(ctx context.Context, id string) (domain.AutoCharge, error)
	ListAutoCharges(ctx context.Context, f AutoChargeFilter, after string, limit int) (Page[domain.AutoCharge], error)
	UpdateAutoCharge(ctx context.Context, id string, ifMatch int, in domain.AutoCharge) (domain.AutoCharge, error)
	ActiveAutoCharges(ctx context.Context, legalEntityID, vendorID string, groups []string, on domain.Day) ([]domain.AutoCharge, error)
	CreateCategoryManager(ctx context.Context, in domain.CategoryManager, idem Idem) (domain.CategoryManager, error)
	ListCategoryManagers(ctx context.Context, f CategoryManagerFilter, after string, limit int) (Page[domain.CategoryManager], error)
	DeleteCategoryManager(ctx context.Context, id string) error
	CreateSigningLimitRequest(ctx context.Context, in domain.SigningLimitRequest, idem Idem) (domain.SigningLimitRequest, error)
	GetSigningLimitRequest(ctx context.Context, id string) (domain.SigningLimitRequest, error)
	ListSigningLimitRequests(ctx context.Context, f SigningRequestFilter, after string, limit int) (Page[domain.SigningLimitRequest], error)
	DecideSigningLimitRequest(ctx context.Context, id string, ifMatch int, action, note string, today domain.Day, idem Idem) (domain.SigningLimitRequest, error)
	GetSigningLimit(ctx context.Context, id string) (domain.SigningLimit, error)
	ListSigningLimits(ctx context.Context, f SigningLimitFilter, after string, limit int) (Page[domain.SigningLimit], error)
	SigningLimitsFor(ctx context.Context, legalEntityID, workerID, documentType string) ([]domain.SigningLimit, error)
	RecertifySigningLimit(ctx context.Context, id string, ifMatch int, today domain.Day, idem Idem) (domain.SigningLimit, error)
	GetInboundChange(ctx context.Context, id string) (InboundChange, error)
	ListInboundChanges(ctx context.Context, f InboundFilter, after string, limit int) (Page[InboundChange], error)
	AcknowledgeInboundChange(ctx context.Context, id, note string, idem Idem) (InboundChange, error)
	RetryInboundChange(ctx context.Context, id, note string, idem Idem) (InboundChange, error)
	Ping(ctx context.Context) error
}

// Page is one page of a list. Next is the id to pass as the next cursor, or
// empty on the last page. Lists are ordered newest first by UUIDv7 id.
type Page[T any] struct {
	Items []T
	Next  string
}

// pageOf trims a result fetched with limit+1 rows to limit and sets Next.
func pageOf[T any](items []T, limit int, id func(T) string) Page[T] {
	if len(items) <= limit {
		return Page[T]{Items: items}
	}
	items = items[:limit]
	return Page[T]{Items: items, Next: id(items[limit-1])}
}

// clampLimit applies the default and maximum page size.
func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultPageSize
	}
	return min(limit, MaxPageSize)
}

// Idem is an Idempotency-Key claim: the key and a hash of the canonical request.
// An empty Key disables replay protection.
type Idem struct {
	Key  string
	Hash string
}

// Postgres implements Store on a pgx pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a Postgres store.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// claimKey records the idempotency key for resourceID. It returns the id of the
// resource created by an earlier request with the same key and body (replay =
// true), or claims the key for resourceID (replay = false). A key reused with a
// different body is 422 idempotency_key_reuse.
func claimKey(ctx context.Context, tx pgx.Tx, idem Idem, resourceID string) (existing string, replay bool, err error) {
	if idem.Key == "" {
		return "", false, nil
	}
	p, _ := tenant.FromContext(ctx)
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, resource_id) values ($1, $2, $3, $4)
		on conflict (tenant_id, key) do nothing`, p.TenantID, idem.Key, idem.Hash, resourceID)
	if err != nil {
		return "", false, err
	}
	if tag.RowsAffected() == 1 {
		return "", false, nil
	}
	var hash, id string
	if err := tx.QueryRow(ctx, `select request_hash, resource_id from idempotency_keys where tenant_id = $1 and key = $2`,
		p.TenantID, idem.Key).Scan(&hash, &id); err != nil {
		return "", false, err
	}
	if hash != idem.Hash {
		return "", false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return id, true, nil
}

// enqueue writes an event to the outbox inside tx.
func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, data any) error {
	p, _ := tenant.FromContext(ctx)
	evt, err := events.New(eventType, eventSource, aggregateID, p.TenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// notFound maps pgx.ErrNoRows to a 404 problem with the given code.
func notFound(err error, code, detail string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, code, detail)
	}
	return err
}

func day(t time.Time) domain.Day { return domain.Day{Time: t} }

// withTx is dbx.WithTenantTx under the service's pool.
func (s *Postgres) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return dbx.WithTenantTx(ctx, s.pool, fn)
}
