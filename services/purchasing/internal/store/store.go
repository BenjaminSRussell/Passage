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
	CodeAgreementNotFound       = "PRC-AGR-NOT-FOUND"
	CodeVersionConflict         = "PRC-VERSION-CONFLICT"
	CodeIdempotencyReuse        = "idempotency_key_reuse"
	eventSource                 = "/passage/procurement/purchasing"
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
	ListPurchaseOrderVersions(ctx context.Context, poID string) ([]PurchaseOrderVersion, error)
	GetPurchaseOrderVersion(ctx context.Context, poID string, no int) (PurchaseOrderVersion, error)
	CreateAgreement(ctx context.Context, in domain.Agreement, idem Idem) (AgreementDetail, error)
	GetAgreement(ctx context.Context, id string) (AgreementDetail, error)
	GetAgreementLine(ctx context.Context, lineID string) (domain.Agreement, error)
	ListAgreements(ctx context.Context, f AgreementFilter, after string, limit int) (Page[AgreementSummary], error)
	ChangeAgreementStatus(ctx context.Context, id string, version int, action string, idem Idem) (AgreementDetail, error)
	ListOpenRequisitionLines(ctx context.Context, f SourcingFilter, after string, limit int) (Page[SourcingLine], error)
	RequisitionLinesByID(ctx context.Context, lineIDs []string) ([]SourcingLine, error)
	ConsolidateRequisitionLines(ctx context.Context, orders []PurchaseOrder, lineIDs []string, idem Idem) ([]PurchaseOrder, error)
	CreateRFQ(ctx context.Context, in domain.RFQ, idem Idem) (domain.RFQ, error)
	GetRFQ(ctx context.Context, id string) (domain.RFQ, error)
	ListRFQs(ctx context.Context, f RFQFilter, after string, limit int) (Page[RFQSummary], error)
	SendRFQ(ctx context.Context, id string, ifMatch int, vendorIDs []string, idem Idem) (domain.RFQ, error)
	ActOnRFQ(ctx context.Context, id string, ifMatch int, action string, idem Idem) (domain.RFQ, error)
	RecordRFQReplies(ctx context.Context, rfqID, vendorID string, replies []domain.RFQReply, idem Idem) (domain.RFQ, error)
	AwardRFQ(ctx context.Context, id string, ifMatch int, awards []domain.Award, orders []AwardedOrder, today domain.Day, idem Idem) (domain.RFQ, []PurchaseOrder, error)
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
