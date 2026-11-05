// Package store persists rate cards, freight bills and their decisions, carrier
// invoices, tenders, tracking events, landed-cost voyages, loads, DG checks, gate
// visits with their gate log, goods-in-transit orders, screening holds and route plans. Every query runs inside
// dbx.WithTenantTx so row-level security applies.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
)

// CodeIdempotencyReuse is returned when an Idempotency-Key is replayed with a
// different request (the platform-wide code, as in org and shopfloor).
const CodeIdempotencyReuse = "idempotency_key_reuse"

// ListLimit caps list responses; the newest rows come first.
const ListLimit = 500

const (
	uniqueViolation = "23505"
	eventSource     = "/passage/tms/transport"
)

const rateCardColumns = `id, carrier_id, service_id, basis, currency, to_char(valid_from, 'YYYY-MM-DD'),
	to_char(valid_to, 'YYYY-MM-DD'), minimum_charge::text, dim_divisor, fuel_pct::text, rate_rows, accessorials, request_hash`

const freightBillColumns = `id, load_id, carrier_id, currency, expected_lines, expected_total::text, match_status,
	carrier_invoice_id, variance_total::text, version, created_at, request_hash`

const carrierInvoiceColumns = `id, carrier_id, invoice_no, to_char(invoice_date, 'YYYY-MM-DD'), source, freight_bill_id,
	currency, lines, total::text, match_status, variance_total::text, created_at`

// Idem carries an Idempotency-Key and a fingerprint of the request. An empty
// Key means the command is not idempotent.
type Idem struct{ Key, Fingerprint string }

// RateCardFilter narrows the rate-card list. Empty fields match everything.
type RateCardFilter struct {
	CarrierID string
	ServiceID string
}

// FreightBillFilter narrows the freight-bill list. Empty fields match everything.
type FreightBillFilter struct {
	MatchStatus string
	CarrierID   string
}

// InvoiceFilter narrows the carrier-invoice list. Empty fields match everything.
type InvoiceFilter struct {
	MatchStatus string
	CarrierID   string
}

// Store is the persistence port used by the API.
type Store interface {
	CreateRateCard(ctx context.Context, in domain.RateCard, idem Idem) (domain.RateCard, error)
	ListRateCards(ctx context.Context, f RateCardFilter) ([]domain.RateCard, error)
	CreateFreightBill(ctx context.Context, in domain.FreightBill, idem Idem) (domain.FreightBill, error)
	GetFreightBill(ctx context.Context, id string) (domain.FreightBill, error)
	ListFreightBills(ctx context.Context, f FreightBillFilter) ([]domain.FreightBill, error)
	RegisterCarrierInvoice(ctx context.Context, in domain.CarrierInvoice) (domain.CarrierInvoice, *domain.FreightBill, error)
	GetCarrierInvoice(ctx context.Context, id string) (domain.CarrierInvoice, error)
	ListCarrierInvoices(ctx context.Context, f InvoiceFilter) ([]domain.CarrierInvoice, error)
	CreateTender(ctx context.Context, in domain.Tender, idem Idem, now time.Time) (domain.Tender, error)
	GetTender(ctx context.Context, id string) (domain.Tender, error)
	ListTenders(ctx context.Context, f TenderFilter) ([]domain.Tender, error)
	RespondTender(ctx context.Context, id, carrierID string, accept bool, now time.Time) (domain.Tender, error)
	ExpireTenders(ctx context.Context, now time.Time) ([]domain.Tender, error)
	RecordTrackingEvent(ctx context.Context, in domain.TrackingEvent) (domain.TrackingEvent, bool, error)
	ListTrackingEvents(ctx context.Context, f TrackingFilter) ([]domain.TrackingEvent, error)
	CreateVoyage(ctx context.Context, in domain.Voyage, idem Idem) (domain.Voyage, error)
	GetVoyage(ctx context.Context, id string) (domain.Voyage, error)
	ListVoyages(ctx context.Context, f VoyageFilter) ([]VoyageSummary, error)
	AllocateVoyage(ctx context.Context, id string, now time.Time) (domain.Voyage, error)
	BuildLoads(ctx context.Context, loads []domain.Load, idem Idem) ([]domain.Load, error)
	ListLoads(ctx context.Context, f LoadFilter) ([]domain.Load, error)
	GetLoad(ctx context.Context, id string) (domain.Load, error)
	ReleaseLoad(ctx context.Context, in domain.LoadRelease, version int, idem Idem, now time.Time) (domain.LoadRelease, error)
	RecordDGCheck(ctx context.Context, in domain.DGCheck) (domain.DGCheck, error)
	ListDGChecks(ctx context.Context, loadID string) ([]domain.DGCheck, error)
	DecideFreightBill(ctx context.Context, id, action, reason string, version int, idem Idem, now time.Time) (domain.FreightBill, error)
	ListBillDecisions(ctx context.Context, billID string) ([]domain.Decision, error)
	CheckIn(ctx context.Context, in domain.GateVisit, idem Idem) (domain.GateVisit, error)
	RecordSeal(ctx context.Context, id, seal string, now time.Time) (domain.GateVisit, error)
	CheckOut(ctx context.Context, id, declaredSeal string, at time.Time, hourlyRate decimal.Decimal, idem Idem) (domain.GateVisit, error)
	GetGateVisit(ctx context.Context, id string) (domain.GateVisit, []domain.GateLogEntry, error)
	ListGateVisits(ctx context.Context, f GateVisitFilter) ([]domain.GateVisit, error)
	CreateGoodsInTransit(ctx context.Context, voyageID, incoterm, siteID, legalEntityID string, lines []int, idem Idem) (domain.GoodsInTransit, error)
	GetGoodsInTransit(ctx context.Context, id string) (domain.GoodsInTransit, error)
	ListGoodsInTransit(ctx context.Context, f GoodsInTransitFilter) ([]domain.GoodsInTransit, error)
	ShipGoodsInTransit(ctx context.Context, id string, at time.Time) (domain.GoodsInTransit, error)
	RecordScreeningHold(ctx context.Context, loadID string, parties []domain.PartyScreening) (domain.ScreeningHold, error)
	GetScreeningHold(ctx context.Context, id string) (domain.ScreeningHold, error)
	ListScreeningHolds(ctx context.Context, f HoldFilter) ([]domain.ScreeningHold, error)
	ReleaseScreeningHold(ctx context.Context, id, reason string, at time.Time) (domain.ScreeningHold, error)
	FindRoutePlan(ctx context.Context, hash, legalEntityID string) (domain.RoutePlan, bool, error)
	SaveRoutePlan(ctx context.Context, plan domain.RoutePlan) (domain.RoutePlan, bool, error)
	GetRoutePlan(ctx context.Context, id string) (domain.RoutePlan, error)
	ListRoutePlans(ctx context.Context) ([]domain.RoutePlanSummary, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store on a pgx pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a Postgres store.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// CreateRateCard saves an effective-dated card. Cards are serialised per
// carrier service with a transaction advisory lock, so two concurrent saves
// cannot both pass the overlap check (BR-34.10, TMS-0102).
func (s *Postgres) CreateRateCard(ctx context.Context, in domain.RateCard, idem Idem) (domain.RateCard, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RateCard
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() (bool, error) {
			if idem.Key == "" {
				return false, nil
			}
			card, hash, err := scanRateCard(tx.QueryRow(ctx, `select `+rateCardColumns+` from rate_card where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key))
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if hash != idem.Fingerprint {
				return false, reuse()
			}
			out = card
			return true, nil
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('transport.rate_card:' || $1 || ':' || $2 || ':' || $3))`,
			p.TenantID, in.CarrierID, in.ServiceID); err != nil {
			return err
		}
		existing, err := queryRateCards(ctx, tx, `select `+rateCardColumns+` from rate_card where tenant_id = $1 and carrier_id = $2 and service_id = $3`,
			p.TenantID, in.CarrierID, in.ServiceID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if domain.Overlaps(e, in) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeRateCardOverlap,
					"rate card overlaps "+e.ID+" ("+e.ValidFrom.Format(time.DateOnly)+" to "+validToText(e.ValidTo)+")")
			}
		}
		rows, accessorials, err := marshalCard(in)
		if err != nil {
			return err
		}
		out, _, err = scanRateCard(tx.QueryRow(ctx, `insert into rate_card (tenant_id, id, carrier_id, service_id, basis, currency, valid_from, valid_to,
			minimum_charge, dim_divisor, fuel_pct, rate_rows, accessorials, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8::date, $9::numeric, $10, $11::numeric, $12::jsonb, $13::jsonb, $14, $15)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+rateCardColumns,
			p.TenantID, ids.New(), in.CarrierID, in.ServiceID, string(in.Basis), in.Currency, in.ValidFrom.Format(time.DateOnly), dayPtr(in.ValidTo),
			in.MinimumCharge.String(), in.DimDivisor, in.FuelPct.String(), rows, accessorials, nullable(idem.Key), nullable(idem.Fingerprint)))
		if errors.Is(err, pgx.ErrNoRows) {
			// A concurrent request with the same key committed first.
			if done, err := replay(); done || err != nil {
				return err
			}
		}
		return err
	})
	return out, err
}

func (s *Postgres) ListRateCards(ctx context.Context, f RateCardFilter) ([]domain.RateCard, error) {
	p, _ := tenant.FromContext(ctx)
	var cards []domain.RateCard
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		cards, err = queryRateCards(ctx, tx, `select `+rateCardColumns+` from rate_card
			where tenant_id = $1 and ($2::text = '' or carrier_id::text = $2) and ($3::text = '' or service_id::text = $3)
			order by carrier_id, service_id, basis, valid_from`, p.TenantID, f.CarrierID, f.ServiceID)
		return err
	})
	if cards == nil {
		cards = []domain.RateCard{}
	}
	return cards, err
}

// CreateFreightBill stores the expected charges for a load and carrier.
func (s *Postgres) CreateFreightBill(ctx context.Context, in domain.FreightBill, idem Idem) (domain.FreightBill, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.FreightBill
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() (bool, error) {
			if idem.Key == "" {
				return false, nil
			}
			bill, hash, err := scanFreightBill(tx.QueryRow(ctx, `select `+freightBillColumns+` from freight_bill where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key))
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if hash != idem.Fingerprint {
				return false, reuse()
			}
			out = bill
			return true, nil
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		entity, err := parentEntityTx(ctx, tx, parentLoad, p.TenantID, in.LoadID, in.LegalEntityID)
		if err != nil {
			return err
		}
		lines, err := json.Marshal(in.ExpectedLines)
		if err != nil {
			return err
		}
		out, _, err = scanFreightBill(tx.QueryRow(ctx, `insert into freight_bill (tenant_id, id, load_id, carrier_id, currency, expected_lines, expected_total,
			match_status, idempotency_key, request_hash, legal_entity_id)
			values ($1, $2, $3, $4, $5, $6::jsonb, $7::numeric, $8, $9, $10, nullif($11, '')::uuid)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+freightBillColumns,
			p.TenantID, ids.New(), in.LoadID, in.CarrierID, in.Currency, string(lines), domain.SumLines(in.ExpectedLines).String(),
			domain.BillUnmatched, nullable(idem.Key), nullable(idem.Fingerprint), entity))
		if errors.Is(err, pgx.ErrNoRows) {
			if done, err := replay(); done || err != nil {
				return err
			}
		}
		return err
	})
	return out, err
}

func (s *Postgres) GetFreightBill(ctx context.Context, id string) (domain.FreightBill, error) {
	p, _ := tenant.FromContext(ctx)
	var b domain.FreightBill
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		b, _, err = scanFreightBill(tx.QueryRow(ctx, `select `+freightBillColumns+` from freight_bill where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FreightBill{}, NotFound("freight bill")
	}
	return b, err
}

// ListFreightBills returns the newest bills first (freight reconciliation status, §6.34.14).
func (s *Postgres) ListFreightBills(ctx context.Context, f FreightBillFilter) ([]domain.FreightBill, error) {
	p, _ := tenant.FromContext(ctx)
	bills := []domain.FreightBill{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+freightBillColumns+` from freight_bill
			where tenant_id = $1 and ($2::text = '' or match_status = $2) and ($3::text = '' or carrier_id::text = $3)
			order by created_at desc, id limit $4`, p.TenantID, f.MatchStatus, f.CarrierID, ListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, _, err := scanFreightBill(rows)
			if err != nil {
				return err
			}
			bills = append(bills, b)
		}
		return rows.Err()
	})
	return bills, err
}

// RegisterCarrierInvoice stores a carrier invoice and, when it names a freight
// bill, matches it in the same transaction (BR-34.4). The bill row is locked so
// two invoices cannot match the same bill at once, and a bill that already has
// an invoice refuses another (TMS-0111). The outbox row is written in that
// transaction too. Invoices without a bill are stored as UNEXPECTED_INVOICE and
// never auto-approved. Duplicate invoice numbers per carrier are TMS-0108.
func (s *Postgres) RegisterCarrierInvoice(ctx context.Context, in domain.CarrierInvoice) (domain.CarrierInvoice, *domain.FreightBill, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	in.Total = domain.SumLines(in.Lines)
	var saved domain.CarrierInvoice
	var bill *domain.FreightBill
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var result domain.MatchResult
		if in.FreightBillID == nil {
			in.MatchStatus, in.VarianceTotal = domain.MatchUnexpected, nil
		} else {
			b, _, err := scanFreightBill(tx.QueryRow(ctx, `select `+freightBillColumns+` from freight_bill where tenant_id = $1 and id = $2 for update`, p.TenantID, *in.FreightBillID))
			if errors.Is(err, pgx.ErrNoRows) {
				return NotFound("freight bill")
			}
			if err != nil {
				return err
			}
			if b.CarrierInvoiceID != nil {
				return httpx.NewProblem(http.StatusConflict, domain.CodeBillInvoiced, "freight bill already has carrier invoice "+*b.CarrierInvoiceID)
			}
			if fields := domain.InvoiceFits(b, in); len(fields) > 0 {
				problem := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeInvoiceMismatch, "the invoice does not belong to this freight bill")
				problem.Errors = fields
				return problem
			}
			result = domain.MatchBill(b, in)
			variance := result.VarianceTotal
			in.MatchStatus, in.VarianceTotal = result.Status, &variance
		}
		billID := ""
		if in.FreightBillID != nil {
			billID = *in.FreightBillID
		}
		entity, err := ownerOrParentTx(ctx, tx, parentFreightBill, p.TenantID, billID, in.LegalEntityID)
		if err != nil {
			return err
		}
		saved, err = insertInvoice(ctx, tx, p.TenantID, entity, in)
		if err != nil || in.FreightBillID == nil {
			return err
		}
		updated, _, err := scanFreightBill(tx.QueryRow(ctx, `update freight_bill set match_status = $3, carrier_invoice_id = $4, variance_total = $5::numeric,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 returning `+freightBillColumns,
			p.TenantID, *in.FreightBillID, result.Status, saved.ID, result.VarianceTotal.String()))
		if err != nil {
			return err
		}
		bill = &updated
		return enqueueMatched(ctx, tx, p.TenantID, saved.ID, updated.ID, result)
	})
	if err != nil {
		return domain.CarrierInvoice{}, nil, err
	}
	return saved, bill, nil
}

func (s *Postgres) GetCarrierInvoice(ctx context.Context, id string) (domain.CarrierInvoice, error) {
	p, _ := tenant.FromContext(ctx)
	var inv domain.CarrierInvoice
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		inv, err = scanInvoice(tx.QueryRow(ctx, `select `+carrierInvoiceColumns+` from carrier_invoice where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CarrierInvoice{}, NotFound("carrier invoice")
	}
	return inv, err
}

// ListCarrierInvoices returns the newest invoices first; filter by
// UNEXPECTED_INVOICE for the exception queue.
func (s *Postgres) ListCarrierInvoices(ctx context.Context, f InvoiceFilter) ([]domain.CarrierInvoice, error) {
	p, _ := tenant.FromContext(ctx)
	invoices := []domain.CarrierInvoice{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+carrierInvoiceColumns+` from carrier_invoice
			where tenant_id = $1 and ($2::text = '' or match_status = $2) and ($3::text = '' or carrier_id::text = $3)
			order by created_at desc, id limit $4`, p.TenantID, f.MatchStatus, f.CarrierID, ListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			inv, err := scanInvoice(rows)
			if err != nil {
				return err
			}
			invoices = append(invoices, inv)
		}
		return rows.Err()
	})
	return invoices, err
}

func insertInvoice(ctx context.Context, tx pgx.Tx, tenantID, entity string, in domain.CarrierInvoice) (domain.CarrierInvoice, error) {
	lines, err := json.Marshal(in.Lines)
	if err != nil {
		return domain.CarrierInvoice{}, err
	}
	saved, err := scanInvoice(tx.QueryRow(ctx, `insert into carrier_invoice (tenant_id, id, carrier_id, invoice_no, invoice_date, source, freight_bill_id,
		currency, lines, total, match_status, variance_total, legal_entity_id)
		values ($1, $2, $3, $4, $5::date, $6, $7, $8, $9::jsonb, $10::numeric, $11, $12::numeric, nullif($13, '')::uuid)
		returning `+carrierInvoiceColumns,
		tenantID, in.ID, in.CarrierID, in.InvoiceNo, in.InvoiceDate, in.Source, in.FreightBillID, in.Currency, string(lines),
		in.Total.String(), in.MatchStatus, varianceString(in.VarianceTotal), entity))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.CarrierInvoice{}, httpx.NewProblem(http.StatusConflict, domain.CodeDuplicateInvoice,
			"carrier invoice "+in.InvoiceNo+" is already registered for this carrier")
	}
	return saved, err
}

func enqueueMatched(ctx context.Context, tx pgx.Tx, tenantID, invoiceID, billID string, res domain.MatchResult) error {
	data := tmsevents.FreightBillMatchedPayload{
		FreightBillID:    billID,
		CarrierInvoiceID: invoiceID,
		MatchStatus:      res.Status,
		Variance:         res.VarianceTotal.StringFixed(2),
		Lines:            make([]tmsevents.MatchLine, 0, len(res.Lines)),
	}
	for _, l := range res.Lines {
		data.Lines = append(data.Lines, tmsevents.MatchLine{ChargeCode: l.ChargeCode, Expected: l.Expected.StringFixed(2),
			Invoiced: l.Invoiced.StringFixed(2), Variance: l.Variance.StringFixed(2), WithinTolerance: l.Within})
	}
	evt, err := events.New(tmsevents.FreightBillMatched, eventSource, billID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "freightbill", billID, evt)
}

func queryRateCards(ctx context.Context, q queryer, sql string, args ...any) ([]domain.RateCard, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RateCard
	for rows.Next() {
		c, _, err := scanRateCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanRateCard(row pgx.Row) (domain.RateCard, string, error) {
	var c domain.RateCard
	var basis, validFrom, minimum, fuel string
	var validTo, hash *string
	var rows, accessorials []byte
	if err := row.Scan(&c.ID, &c.CarrierID, &c.ServiceID, &basis, &c.Currency, &validFrom, &validTo, &minimum, &c.DimDivisor, &fuel, &rows, &accessorials, &hash); err != nil {
		return domain.RateCard{}, "", err
	}
	c.Basis = domain.Basis(basis)
	c.ValidFrom = parseDay(validFrom)
	if validTo != nil {
		t := parseDay(*validTo)
		c.ValidTo = &t
	}
	c.MinimumCharge = decimal.RequireFromString(minimum)
	c.FuelPct = decimal.RequireFromString(fuel)
	if err := json.Unmarshal(rows, &c.Rows); err != nil {
		return domain.RateCard{}, "", err
	}
	if err := json.Unmarshal(accessorials, &c.Accessorials); err != nil {
		return domain.RateCard{}, "", err
	}
	return c, deref(hash), nil
}

func scanFreightBill(row pgx.Row) (domain.FreightBill, string, error) {
	var b domain.FreightBill
	var lines []byte
	var total string
	var variance, invoiceID, hash *string
	if err := row.Scan(&b.ID, &b.LoadID, &b.CarrierID, &b.Currency, &lines, &total, &b.MatchStatus, &invoiceID, &variance,
		&b.Version, &b.CreatedAt, &hash); err != nil {
		return domain.FreightBill{}, "", err
	}
	if err := json.Unmarshal(lines, &b.ExpectedLines); err != nil {
		return domain.FreightBill{}, "", err
	}
	b.ExpectedTotal = decimal.RequireFromString(total)
	b.CarrierInvoiceID = invoiceID
	b.VarianceTotal = decimalPtr(variance)
	return b, deref(hash), nil
}

func scanInvoice(row pgx.Row) (domain.CarrierInvoice, error) {
	var inv domain.CarrierInvoice
	var lines []byte
	var total string
	var variance *string
	if err := row.Scan(&inv.ID, &inv.CarrierID, &inv.InvoiceNo, &inv.InvoiceDate, &inv.Source, &inv.FreightBillID, &inv.Currency,
		&lines, &total, &inv.MatchStatus, &variance, &inv.CreatedAt); err != nil {
		return domain.CarrierInvoice{}, err
	}
	if err := json.Unmarshal(lines, &inv.Lines); err != nil {
		return domain.CarrierInvoice{}, err
	}
	inv.Total = decimal.RequireFromString(total)
	inv.VarianceTotal = decimalPtr(variance)
	return inv, nil
}

func marshalCard(c domain.RateCard) (rows, accessorials string, err error) {
	if c.Rows == nil {
		c.Rows = []domain.RateRow{}
	}
	if c.Accessorials == nil {
		c.Accessorials = []domain.Accessorial{}
	}
	r, err := json.Marshal(c.Rows)
	if err != nil {
		return "", "", err
	}
	a, err := json.Marshal(c.Accessorials)
	if err != nil {
		return "", "", err
	}
	return string(r), string(a), nil
}

// NotFound is the TMS-0100 problem for an unknown or invisible record.
func NotFound(what string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeNotFound, what+" not found")
}

func reuse() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
}

func parseDay(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

func dayPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

func validToText(t *time.Time) string {
	if t == nil {
		return "open"
	}
	return t.Format(time.DateOnly)
}

func decimalPtr(s *string) *decimal.Decimal {
	if s == nil {
		return nil
	}
	d := decimal.RequireFromString(*s)
	return &d
}

func varianceString(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
