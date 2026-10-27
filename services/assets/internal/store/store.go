// Package store persists the asset register, books, append-only asset
// transactions, depreciation runs, additions, insurance policies, capex
// requests and leases. Every write runs in a tenant transaction, records
// its Idempotency-Key in idempotency_keys and writes its event to the outbox in
// that same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	assetevents "github.com/BenjaminSRussell/passage/services/assets/internal/events"
)

// Stable error codes raised by the store (listed in the README).
const (
	CodeAssetNotFound    = "AST-0001"
	CodeAssetNumberDup   = "AST-0002"
	CodeBookNotFound     = "AST-0003"
	CodeBookDuplicate    = "AST-0004"
	CodeLeaseNotFound    = "AST-0005"
	CodeVersionConflict  = "AST-0006"
	CodeLeaseNumberDup   = "AST-0007"
	CodeIdempotencyReuse = "idempotency_key_reuse"

	uniqueViolation = "23505"
	eventSource     = "/passage/assets/assets"
)

// Store is the persistence contract the API depends on.
type Store interface {
	CreateAsset(ctx context.Context, in Asset, idempotencyKey string) (Asset, error)
	ListAssets(ctx context.Context, f AssetFilter) (AssetPage, error)
	GetAsset(ctx context.Context, id string) (AssetDetail, error)
	AddBook(ctx context.Context, assetID string, in Book, idempotencyKey string) (Book, error)
	GetBook(ctx context.Context, assetID, bookID string) (Book, error)
	ListTransactions(ctx context.Context, assetID string) ([]Transaction, error)
	Dispose(ctx context.Context, assetID string, in DisposalInput, idempotencyKey string) (Disposal, error)
	CreateLease(ctx context.Context, in Lease, idempotencyKey string) (Lease, error)
	ListLeases(ctx context.Context, f LeaseFilter) (LeasePage, error)
	GetLease(ctx context.Context, id string) (Lease, error)

	Adjust(ctx context.Context, assetID string, in AdjustmentInput, idempotencyKey string) (AdjustResult, error)
	AddAddition(ctx context.Context, assetID string, in AdditionInput, idempotencyKey string) (Addition, error)
	ListAdditions(ctx context.Context, assetID string) ([]Addition, error)
	ProposeRun(ctx context.Context, in RunInput, idempotencyKey string) (DepreciationRun, error)
	PostRun(ctx context.Context, runID string, version int) (DepreciationRun, error)
	GetRun(ctx context.Context, runID string) (DepreciationRun, error)
	ListRuns(ctx context.Context, f RunFilter) (RunPage, error)

	UpsertIndexSeries(ctx context.Context, in IndexSeries) (IndexSeries, error)
	ListIndexSeries(ctx context.Context) ([]IndexSeries, error)
	AddInsurancePolicy(ctx context.Context, assetID string, in InsurancePolicy, idempotencyKey string) (InsurancePolicy, error)
	ListInsurancePolicies(ctx context.Context, assetID string) ([]InsurancePolicy, error)
	CreateCapexRequest(ctx context.Context, in CapexRequest, idempotencyKey string) (CapexRequest, error)
	ListCapexRequests(ctx context.Context, f CapexFilter) (CapexPage, error)
	GetCapexRequest(ctx context.Context, id string) (CapexRequest, error)
	SubmitCapexRequest(ctx context.Context, id string, in SubmitInput) (CapexRequest, error)

	Ping(ctx context.Context) error
}

// Postgres implements Store on pgx.
type Postgres struct {
	pool     *pgxpool.Pool
	maxBooks int
}

// New returns a Postgres store over pool with the default book limit.
func New(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool, maxBooks: domain.MaxBooksPerAsset}
}

// WithMaxBooks sets the books-per-asset limit (ASSET_MAX_BOOKS, 1 to 20).
func (s *Postgres) WithMaxBooks(n int) *Postgres {
	if n >= 1 && n <= domain.MaxBooksLimit {
		s.maxBooks = n
	}
	return s
}

// Ping checks the database connection.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const (
	assetColumns = `a.id, a.legal_entity_id, a.asset_number, a.name, a.class_code, a.status, a.barcode, a.quantity, a.acquired_date,
		(select count(*) from asset_book b where b.tenant_id = a.tenant_id and b.asset_id = a.id), a.version, a.created_at`
	bookColumns = `id, asset_id, legal_entity_id, book_code, currency, method, factor, switch_to_straight_line, service_life_periods, salvage_value, convention,
		in_service_date, cost, accumulated_depreciation, nbv, status, last_depreciated_period, coalesce(acquisition_cost, cost)`
	txColumns = `t.id, t.asset_book_id, b.book_code, b.currency, t.type, t.transaction_date, t.cost_amount, t.accumulated_amount, t.proceeds_amount, t.gain_loss,
		t.impairment_amount, t.source_type, t.source_id, t.run_id, t.created_at`
	leaseColumns = "id, legal_entity_id, lease_number, commencement_date, term_months, payments_per_year, payment_amount, payment_timing, ibr_rate, currency, liability, rou_asset, status, version, created_at"
)

func principal(ctx context.Context) tenant.Principal {
	p, _ := tenant.FromContext(ctx)
	return p
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// uniqueConstraint returns the violated constraint name, or "" when err is not
// a unique violation.
func uniqueConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return pgErr.ConstraintName
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func notFound(code, detail string) error {
	return httpx.NewProblem(http.StatusNotFound, code, detail)
}

// idempotent runs a command at most once per (tenant, Idempotency-Key). The key
// is claimed first (a concurrent request with the same key waits on the
// primary key and then replays); the response is stored in the same
// transaction as the change. Reusing a key with a different request is 422.
func idempotent[T any](ctx context.Context, tx pgx.Tx, tenantID, key, scope string, request any, run func() (T, error)) (T, error) {
	var zero T
	raw, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	sum := sha256.Sum256(append([]byte(scope+"\n"), raw...))
	hash := hex.EncodeToString(sum[:])
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 0)
		on conflict (tenant_id, key) do nothing`, tenantID, key, hash)
	if err != nil {
		return zero, err
	}
	if tag.RowsAffected() == 0 {
		var stored string
		var body []byte
		if err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&stored, &body); err != nil {
			return zero, err
		}
		if stored != hash {
			return zero, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
		}
		var out T
		if err := json.Unmarshal(body, &out); err != nil {
			return zero, fmt.Errorf("replay %s: %w", key, err)
		}
		return out, nil
	}
	out, err := run()
	if err != nil {
		return zero, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `update idempotency_keys set response_status = $3, response_body = $4 where tenant_id = $1 and key = $2`,
		tenantID, key, http.StatusCreated, body)
	return out, err
}

func scanAsset(row pgx.Row) (Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.LegalEntityID, &a.AssetNumber, &a.Name, &a.ClassCode, &a.Status, &a.Barcode, &a.Quantity, &a.AcquiredDate, &a.BookCount, &a.Version, &a.CreatedAt)
	return a, err
}

func scanBook(row pgx.Row) (Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.AssetID, &b.LegalEntityID, &b.BookCode, &b.Currency, &b.Method, &b.Factor, &b.SwitchToStraightLine,
		&b.ServiceLifePeriods, &b.SalvageValue, &b.Convention, &b.InServiceDate, &b.Cost, &b.AccumulatedDepreciation, &b.NBV, &b.Status,
		&b.LastDepreciatedPeriod, &b.AcquisitionCost)
	return b, err
}

func scanTx(row pgx.Row) (Transaction, error) {
	var t Transaction
	err := row.Scan(&t.ID, &t.AssetBookID, &t.BookCode, &t.Currency, &t.Type, &t.TransactionDate, &t.CostAmount, &t.AccumulatedAmount,
		&t.ProceedsAmount, &t.GainLoss, &t.ImpairmentAmount, &t.SourceType, &t.SourceID, &t.RunID, &t.CreatedAt)
	return t, err
}

func scanLease(row pgx.Row) (Lease, error) {
	var l Lease
	err := row.Scan(&l.ID, &l.LegalEntityID, &l.LeaseNumber, &l.CommencementDate, &l.TermMonths, &l.PaymentsPerYear, &l.PaymentAmount,
		&l.PaymentTiming, &l.IBRRate, &l.Currency, &l.Liability, &l.ROUAsset, &l.Status, &l.Version, &l.CreatedAt)
	return l, err
}

func loadAsset(ctx context.Context, tx pgx.Tx, tenantID, id string) (Asset, error) {
	a, err := scanAsset(tx.QueryRow(ctx, `select `+assetColumns+` from asset a where a.tenant_id = $1 and a.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, notFound(CodeAssetNotFound, "no asset with id "+id)
	}
	return a, err
}

func loadBooks(ctx context.Context, tx pgx.Tx, tenantID, assetID string, forUpdate bool) ([]Book, error) {
	sql := `select ` + bookColumns + ` from asset_book where tenant_id = $1 and asset_id = $2 order by book_code`
	if forUpdate {
		sql += ` for update`
	}
	rows, err := tx.Query(ctx, sql, tenantID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	books := []Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		books = append(books, b)
	}
	return books, rows.Err()
}

// CreateAsset adds an asset to the register in status NotYetAcquired.
func (s *Postgres) CreateAsset(ctx context.Context, in Asset, idempotencyKey string) (Asset, error) {
	p := principal(ctx)
	var out Asset
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			LegalEntityID, AssetNumber, Name, ClassCode string
			Barcode                                     *string
			Quantity                                    string
		}{in.LegalEntityID, in.AssetNumber, in.Name, in.ClassCode, in.Barcode, in.Quantity.String()}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "asset.create", request, func() (Asset, error) {
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into asset (tenant_id, id, legal_entity_id, asset_number, name, class_code, barcode, quantity)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`,
				p.TenantID, id, in.LegalEntityID, in.AssetNumber, in.Name, in.ClassCode, in.Barcode, in.Quantity); err != nil {
				return Asset{}, err
			}
			return loadAsset(ctx, tx, p.TenantID, id)
		})
		return err
	})
	switch uniqueConstraint(err) {
	case "":
		return out, err
	case "asset_barcode_unique":
		return Asset{}, httpx.NewProblem(http.StatusConflict, CodeAssetNumberDup, "an asset with this barcode already exists in the legal entity")
	default:
		return Asset{}, httpx.NewProblem(http.StatusConflict, CodeAssetNumberDup, "an asset with this number already exists in the legal entity")
	}
}

// ListAssets returns one page of the register ordered by asset number.
func (s *Postgres) ListAssets(ctx context.Context, f AssetFilter) (AssetPage, error) {
	p := principal(ctx)
	page := AssetPage{Items: []Asset{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+assetColumns+` from asset a
			where a.tenant_id = $1
			  and ($2::text = '' or a.legal_entity_id::text = $2)
			  and ($3::text = '' or a.status = $3)
			  and ($4::text = '' or (a.asset_number, a.id) > (select c.asset_number, c.id from asset c where c.tenant_id = $1 and c.id::text = $4))
			order by a.asset_number, a.id limit $5`,
			p.TenantID, f.LegalEntityID, f.Status, f.After, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, a)
		}
		return rows.Err()
	})
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, err
}

// GetAsset reads an asset with its books.
func (s *Postgres) GetAsset(ctx context.Context, id string) (AssetDetail, error) {
	p := principal(ctx)
	var out AssetDetail
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := loadAsset(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		books, err := loadBooks(ctx, tx, p.TenantID, id, false)
		out = AssetDetail{Asset: a, Books: books}
		return err
	})
	return out, err
}

// GetBook reads one book of an asset.
func (s *Postgres) GetBook(ctx context.Context, assetID, bookID string) (Book, error) {
	p := principal(ctx)
	var b Book
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		b, err = scanBook(tx.QueryRow(ctx, `select `+bookColumns+` from asset_book where tenant_id = $1 and asset_id = $2 and id = $3`, p.TenantID, assetID, bookID))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeBookNotFound, "no book "+bookID+" on asset "+assetID)
		}
		return err
	})
	return b, err
}

// ListTransactions returns every transaction on every book of an asset, oldest first.
func (s *Postgres) ListTransactions(ctx context.Context, assetID string) ([]Transaction, error) {
	p := principal(ctx)
	out := []Transaction{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadAsset(ctx, tx, p.TenantID, assetID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+txColumns+` from asset_transaction t
			join asset_book b on b.tenant_id = t.tenant_id and b.id = t.asset_book_id
			where t.tenant_id = $1 and b.asset_id = $2 order by t.transaction_date, t.created_at, b.book_code`, p.TenantID, assetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTx(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

func insertTransaction(ctx context.Context, tx pgx.Tx, tenantID string, book Book, typ string, date Date, cost, accumulated any, proceeds, gainLoss any) (Transaction, error) {
	return postTransaction(ctx, tx, tenantID, book, txRow{Type: typ, Date: date, Cost: cost, Accumulated: accumulated, Impairment: 0,
		Proceeds: proceeds, GainLoss: gainLoss, SourceType: "manual"})
}

// txRow is one asset_transaction insert. Amount fields take a decimal or 0.
type txRow struct {
	Type                          string
	Date                          Date
	Cost, Accumulated, Impairment any
	Proceeds, GainLoss            any
	SourceType                    string
	SourceID, RunID               *string
}

func postTransaction(ctx context.Context, tx pgx.Tx, tenantID string, book Book, row txRow) (Transaction, error) {
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into asset_transaction (tenant_id, id, asset_book_id, legal_entity_id, type, transaction_date, cost_amount, accumulated_amount,
		impairment_amount, proceeds_amount, gain_loss, source_type, source_id, run_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		tenantID, id, book.ID, book.LegalEntityID, row.Type, row.Date, row.Cost, row.Accumulated, row.Impairment, row.Proceeds, row.GainLoss,
		row.SourceType, row.SourceID, row.RunID); err != nil {
		return Transaction{}, err
	}
	return scanTx(tx.QueryRow(ctx, `select `+txColumns+` from asset_transaction t
		join asset_book b on b.tenant_id = t.tenant_id and b.id = t.asset_book_id where t.tenant_id = $1 and t.id = $2`, tenantID, id))
}

// AddBook attaches a book (at most the configured limit per asset) and
// posts its Acquisition transaction for the cost on the in-service date.
func (s *Postgres) AddBook(ctx context.Context, assetID string, in Book, idempotencyKey string) (Book, error) {
	p := principal(ctx)
	var out Book
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			AssetID string
			Book    Book
		}{assetID, in}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "asset.book.add", request, func() (Book, error) {
			var assetStatus, legalEntityID string
			err := tx.QueryRow(ctx, `select status, legal_entity_id from asset where tenant_id = $1 and id = $2 for update`, p.TenantID, assetID).Scan(&assetStatus, &legalEntityID)
			if errors.Is(err, pgx.ErrNoRows) {
				return Book{}, notFound(CodeAssetNotFound, "no asset with id "+assetID)
			}
			if err != nil {
				return Book{}, err
			}
			next, err := domain.AcquireStatus(domain.AssetStatus(assetStatus))
			if err != nil {
				return Book{}, err
			}
			var count int
			if err := tx.QueryRow(ctx, `select count(*) from asset_book where tenant_id = $1 and asset_id = $2`, p.TenantID, assetID).Scan(&count); err != nil {
				return Book{}, err
			}
			if count >= s.maxBooks {
				return Book{}, &domain.Error{Code: domain.CodeTooManyBooks, Detail: "an asset has at most " + strconv.Itoa(s.maxBooks) + " books"}
			}
			book, err := scanBook(tx.QueryRow(ctx, `insert into asset_book (tenant_id, id, asset_id, legal_entity_id, book_code, currency, method, factor, switch_to_straight_line,
				service_life_periods, salvage_value, convention, in_service_date, cost, acquisition_cost, accumulated_depreciation, nbv, status)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14, 0, $14, 'Open') returning `+bookColumns,
				p.TenantID, ids.New(), assetID, legalEntityID, in.BookCode, in.Currency, in.Method, in.Factor, in.SwitchToStraightLine,
				in.ServiceLifePeriods, in.SalvageValue, in.Convention, in.InServiceDate, in.Cost))
			if uniqueConstraint(err) != "" {
				return Book{}, httpx.NewProblem(http.StatusConflict, CodeBookDuplicate, "the asset already has a book with this code")
			}
			if err != nil {
				return Book{}, err
			}
			if _, err := insertTransaction(ctx, tx, p.TenantID, book, "Acquisition", in.InServiceDate, in.Cost, 0, nil, nil); err != nil {
				return Book{}, err
			}
			if _, err := tx.Exec(ctx, `update asset set status = $3, acquired_date = least(coalesce(acquired_date, $4::date), $4::date), version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, assetID, string(next), in.InServiceDate); err != nil {
				return Book{}, err
			}
			return book, enqueue(ctx, tx, "asset", assetID, assetevents.AssetAcquired, p.TenantID, assetevents.AssetAcquiredData{
				AssetID: assetID, BookIDs: []string{book.ID}, Cost: in.Cost.String(), Currency: in.Currency, SourceType: "manual",
			})
		})
		return err
	})
	return out, err
}

// Dispose records a sale or scrap of a percentage of the asset on every Open
// book (FR-37.8). Each book gets its own transaction with the disposed share
// of cost and accumulated depreciation and its own gain or loss against the
// same proceeds, and its own asset.disposed event. A full disposal closes the
// asset and every book as Sold or Scrapped (BR-37.2).
func (s *Postgres) Dispose(ctx context.Context, assetID string, in DisposalInput, idempotencyKey string) (Disposal, error) {
	p := principal(ctx)
	var out Disposal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			AssetID string
			Input   DisposalInput
		}{assetID, in}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "asset.dispose", request, func() (Disposal, error) {
			var assetStatus string
			var version int
			err := tx.QueryRow(ctx, `select status, version from asset where tenant_id = $1 and id = $2 for update`, p.TenantID, assetID).Scan(&assetStatus, &version)
			if errors.Is(err, pgx.ErrNoRows) {
				return Disposal{}, notFound(CodeAssetNotFound, "no asset with id "+assetID)
			}
			if err != nil {
				return Disposal{}, err
			}
			if version != in.Version {
				return Disposal{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					fmt.Sprintf("the asset changed since you read it (version %d, current %d); reload and try again", in.Version, version))
			}
			nextAsset, err := domain.AfterDisposal(domain.AssetStatus(assetStatus), in.Percentage, in.Kind)
			if err != nil {
				return Disposal{}, err
			}
			books, err := loadBooks(ctx, tx, p.TenantID, assetID, true)
			if err != nil {
				return Disposal{}, err
			}
			txType := "DisposalSale"
			if in.Kind == domain.DisposalScrap {
				txType = "DisposalScrap"
			}
			nextBook := string(nextAsset)
			res := Disposal{Books: []Book{}, Transactions: []Transaction{}}
			for _, book := range books {
				if book.Status != string(domain.StatusOpen) {
					continue
				}
				if in.Date.Before(book.InServiceDate.Time) {
					return Disposal{}, &domain.Error{Code: domain.CodeInvalidDisposal, Detail: "the disposal date is before the in-service date of book " + book.BookCode}
				}
				if err := domain.CheckDisposalDate(in.Date.Time, deref(book.LastDepreciatedPeriod)); err != nil {
					return Disposal{}, err
				}
				d, err := domain.Dispose(book.Cost, book.AccumulatedDepreciation, in.Proceeds, in.Percentage, book.Currency)
				if err != nil {
					return Disposal{}, err
				}
				t, err := insertTransaction(ctx, tx, p.TenantID, book, txType, in.Date, d.DisposedCost.Neg(), d.DisposedAccumulated.Neg(), in.Proceeds, d.GainLoss)
				if err != nil {
					return Disposal{}, err
				}
				// The salvage value leaves with the disposed share, so a later run
				// depreciates the remaining asset down to its own salvage.
				salvage := book.SalvageValue.Sub(book.SalvageValue.Mul(in.Percentage).Div(decimal.NewFromInt(100)).Round(money.MinorUnits(book.Currency)))
				updated, err := scanBook(tx.QueryRow(ctx, `update asset_book set cost = $3, accumulated_depreciation = $4, nbv = $5, status = $6, salvage_value = $7
					where tenant_id = $1 and id = $2 returning `+bookColumns,
					p.TenantID, book.ID, d.RemainingCost, d.RemainingAccumulated, d.RemainingCost.Sub(d.RemainingAccumulated), nextBook, salvage))
				if err != nil {
					return Disposal{}, err
				}
				if err := enqueue(ctx, tx, "asset", assetID, assetevents.AssetDisposed, p.TenantID, assetevents.AssetDisposedData{
					AssetID: assetID, BookID: book.ID, Kind: string(in.Kind), TransactionDate: in.Date.String(), Percentage: in.Percentage.String(),
					Proceeds: in.Proceeds.String(), NBV: d.NBVDisposed.String(), GainLoss: d.GainLoss.String(), Currency: book.Currency,
				}); err != nil {
					return Disposal{}, err
				}
				res.Books = append(res.Books, updated)
				res.Transactions = append(res.Transactions, t)
			}
			if len(res.Books) == 0 {
				return Disposal{}, &domain.Error{Code: domain.CodeAssetState, Detail: "the asset has no Open book to dispose of"}
			}
			if _, err := tx.Exec(ctx, `update asset set status = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
				p.TenantID, assetID, string(nextAsset)); err != nil {
				return Disposal{}, err
			}
			res.Asset, err = loadAsset(ctx, tx, p.TenantID, assetID)
			return res, err
		})
		return err
	})
	return out, err
}

// CreateLease stores the lease, its schedule, and the lease.recognized event.
func (s *Postgres) CreateLease(ctx context.Context, in Lease, idempotencyKey string) (Lease, error) {
	p := principal(ctx)
	var out Lease
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			LegalEntityID, LeaseNumber, CommencementDate, PaymentAmount, PaymentTiming, IBRRate, Currency string
			TermMonths, PaymentsPerYear                                                                   int
		}{in.LegalEntityID, in.LeaseNumber, in.CommencementDate.String(), in.PaymentAmount.String(), in.PaymentTiming, in.IBRRate.String(), in.Currency,
			in.TermMonths, in.PaymentsPerYear}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "lease.create", request, func() (Lease, error) {
			row, err := scanLease(tx.QueryRow(ctx, `insert into lease (tenant_id, id, legal_entity_id, lease_number, commencement_date, term_months, payments_per_year,
				payment_amount, payment_timing, ibr_rate, currency, liability, rou_asset)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) returning `+leaseColumns,
				p.TenantID, ids.New(), in.LegalEntityID, in.LeaseNumber, in.CommencementDate, in.TermMonths, in.PaymentsPerYear, in.PaymentAmount,
				in.PaymentTiming, in.IBRRate, in.Currency, in.Liability, in.ROUAsset))
			if err != nil {
				return Lease{}, err
			}
			for _, l := range in.Schedule {
				if _, err := tx.Exec(ctx, `insert into lease_schedule_line (tenant_id, lease_id, legal_entity_id, period_no, opening_liability, interest, payment, closing_liability, rou_depreciation)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, row.ID, row.LegalEntityID, l.PeriodNo, l.Opening, l.Interest, l.Payment, l.Closing, l.ROUDepreciation); err != nil {
					return Lease{}, err
				}
			}
			row.Schedule = in.Schedule
			return row, enqueue(ctx, tx, "lease", row.ID, assetevents.LeaseRecognized, p.TenantID, assetevents.LeaseRecognizedData{
				LeaseID: row.ID, Liability: row.Liability.String(), ROUAsset: row.ROUAsset.String(), Currency: row.Currency,
				TermMonths: row.TermMonths, Rate: row.IBRRate.String(),
			})
		})
		return err
	})
	if uniqueConstraint(err) != "" {
		return Lease{}, httpx.NewProblem(http.StatusConflict, CodeLeaseNumberDup, "a lease with this number already exists in the legal entity")
	}
	return out, err
}

// ListLeases returns one page of leases ordered by lease number, without schedules.
func (s *Postgres) ListLeases(ctx context.Context, f LeaseFilter) (LeasePage, error) {
	p := principal(ctx)
	page := LeasePage{Items: []Lease{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+leaseColumns+` from lease l
			where l.tenant_id = $1
			  and ($2::text = '' or l.legal_entity_id::text = $2)
			  and ($3::text = '' or (l.lease_number, l.id) > (select c.lease_number, c.id from lease c where c.tenant_id = $1 and c.id::text = $3))
			order by l.lease_number, l.id limit $4`,
			p.TenantID, f.LegalEntityID, f.After, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLease(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, l)
		}
		return rows.Err()
	})
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, err
}

// GetLease reads a lease with its schedule.
func (s *Postgres) GetLease(ctx context.Context, id string) (Lease, error) {
	p := principal(ctx)
	var l Lease
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		l, err = scanLease(tx.QueryRow(ctx, `select `+leaseColumns+` from lease where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeLeaseNotFound, "no lease with id "+id)
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select period_no, opening_liability, interest, payment, closing_liability, rou_depreciation
			from lease_schedule_line where tenant_id = $1 and lease_id = $2 order by period_no`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		l.Schedule = []LeaseLine{}
		for rows.Next() {
			var line LeaseLine
			if err := rows.Scan(&line.PeriodNo, &line.Opening, &line.Interest, &line.Payment, &line.Closing, &line.ROUDepreciation); err != nil {
				return err
			}
			l.Schedule = append(l.Schedule, line)
		}
		return rows.Err()
	})
	return l, err
}
