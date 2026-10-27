package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	assetevents "github.com/BenjaminSRussell/passage/services/assets/internal/events"
)

// Codes raised by adjustments, additions and depreciation runs.
const (
	CodeRunNotFound   = "AST-0019"
	CodeAdditionLimit = "AST-0020"
	// MaxAdditionsPerAsset is the FR-37.14 limit on additions under one asset.
	MaxAdditionsPerAsset = 500

	sourceAddition = "addition"
	sourceRun      = "depreciation_run"
)

// AdjustmentInput is a validated adjustment of one book (POST
// /assets/{id}/transactions).
type AdjustmentInput struct {
	BookID string                `json:"bookId"`
	Type   domain.AdjustmentType `json:"type"`
	Date   Date                  `json:"transactionDate"`
	Amount decimal.Decimal       `json:"amount"`
}

// AdjustResult is the book after an adjustment and the transaction it posted.
// Limited is set when an impairment reversal was capped (AST-0512); Requested
// is then the amount asked for.
type AdjustResult struct {
	Book        Book            `json:"book"`
	Transaction Transaction     `json:"transaction"`
	Limited     bool            `json:"limited"`
	Requested   decimal.Decimal `json:"requested"`
}

// AdditionInput is a validated addition. A financial addition posts an
// AcquisitionAdj of Cost on each selected Open book, or on every Open book when
// BookIDs is empty (AC-37.12).
type AdditionInput struct {
	Type         string          `json:"type"`
	Description  string          `json:"description"`
	Quantity     decimal.Decimal `json:"quantity"`
	SerialNumber *string         `json:"serialNumber"`
	AddedDate    Date            `json:"addedDate"`
	Financial    bool            `json:"financial"`
	Cost         decimal.Decimal `json:"cost"`
	Currency     string          `json:"currency"`
	BookIDs      []string        `json:"bookIds"`
}

// Addition is an improvement, component or accessory recorded under an asset
// with the AcquisitionAdj transactions it posted.
type Addition struct {
	ID           string          `json:"id"`
	AssetID      string          `json:"assetId"`
	Type         string          `json:"type"`
	Description  string          `json:"description"`
	Quantity     decimal.Decimal `json:"quantity"`
	SerialNumber *string         `json:"serialNumber"`
	AddedDate    Date            `json:"addedDate"`
	Financial    bool            `json:"financial"`
	Cost         decimal.Decimal `json:"cost"`
	Currency     *string         `json:"currency"`
	CreatedAt    time.Time       `json:"createdAt"`
	Transactions []Transaction   `json:"transactions"`
}

// RunInput proposes depreciation for one book code of a legal entity and one
// period: "2026-03" for monthly (straight-line) books, "2026" for yearly
// (declining-balance) books.
type RunInput struct {
	LegalEntityID string `json:"legalEntityId"`
	BookCode      string `json:"bookCode"`
	Period        string `json:"period"`
}

// DepreciationRun is one proposal or posting (FR-37.5). Due lines post when the
// run is posted; exception lines stay out and the other books still post.
type DepreciationRun struct {
	ID             string          `json:"id"`
	LegalEntityID  string          `json:"legalEntityId"`
	BookCode       string          `json:"bookCode"`
	Period         string          `json:"period"`
	Currency       *string         `json:"currency"`
	Status         string          `json:"status"`
	AssetCount     int             `json:"assetCount"`
	ExceptionCount int             `json:"exceptionCount"`
	TotalAmount    decimal.Decimal `json:"totalAmount"`
	JournalEntryID *string         `json:"journalEntryId"`
	Version        int             `json:"version"`
	CreatedAt      time.Time       `json:"createdAt"`
	PostedAt       *time.Time      `json:"postedAt"`
	Lines          []RunLine       `json:"lines,omitempty"`
}

// RunLine is one book's part in a run. ScheduledAmount is the original
// schedule's line; Amount differs from it after adjustments (domain.RunAmount).
type RunLine struct {
	AssetBookID     string          `json:"assetBookId"`
	AssetID         string          `json:"assetId"`
	AssetNumber     string          `json:"assetNumber"`
	Status          string          `json:"status"`
	Amount          decimal.Decimal `json:"amount"`
	ScheduledAmount decimal.Decimal `json:"scheduledAmount"`
	ExceptionCode   *string         `json:"exceptionCode"`
	Detail          *string         `json:"detail"`
}

// RunFilter narrows ListRuns. After is the id of the last run of the previous
// page; Limit is 1..200.
type RunFilter struct {
	LegalEntityID string
	After         string
	Limit         int
}

// RunPage is one page of runs, newest first, without lines.
type RunPage struct {
	Items      []DepreciationRun `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

const runColumns = `id, legal_entity_id, book_code, period, currency, status, asset_count, exception_count, total_amount, journal_entry_id,
	version, created_at, posted_at`

func scanRun(row pgx.Row) (DepreciationRun, error) {
	var r DepreciationRun
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.BookCode, &r.Period, &r.Currency, &r.Status, &r.AssetCount, &r.ExceptionCount,
		&r.TotalAmount, &r.JournalEntryID, &r.Version, &r.CreatedAt, &r.PostedAt)
	return r, err
}

// lockOpenAsset locks the asset row and refuses anything but an Open asset
// (BR-37.2: no postings on Sold, Scrapped or Transferred assets).
func lockOpenAsset(ctx context.Context, tx pgx.Tx, tenantID, assetID string, allowNotAcquired bool) error {
	var status string
	err := tx.QueryRow(ctx, `select status from asset where tenant_id = $1 and id = $2 for update`, tenantID, assetID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(CodeAssetNotFound, "no asset with id "+assetID)
	}
	if err != nil {
		return err
	}
	if status == string(domain.StatusOpen) || (allowNotAcquired && status == string(domain.StatusNotYetAcquired)) {
		return nil
	}
	return &domain.Error{Code: domain.CodeAssetState, Detail: "asset is " + status + "; only an Open asset takes postings"}
}

func bumpAsset(ctx context.Context, tx pgx.Tx, tenantID, assetID string) error {
	_, err := tx.Exec(ctx, `update asset set version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, tenantID, assetID)
	return err
}

// applyAdjustment posts one adjustment on a locked Open book, moves the book's
// projections and enqueues asset.adjusted, all in the caller's transaction.
func applyAdjustment(ctx context.Context, tx pgx.Tx, tenantID string, book Book, kind domain.AdjustmentType, date Date, amount decimal.Decimal,
	sourceType string, sourceID *string) (AdjustResult, error) {
	if err := domain.RequireOpenBook(book.Status); err != nil {
		return AdjustResult{}, err
	}
	if date.Before(book.InServiceDate.Time) {
		return AdjustResult{}, &domain.Error{Code: domain.CodeInvalidAdjustment, Detail: "the transaction date is before the in-service date of book " + book.BookCode}
	}
	var impairment decimal.Decimal
	if err := tx.QueryRow(ctx, `select coalesce(sum(impairment_amount), 0) from asset_transaction where tenant_id = $1 and asset_book_id = $2`,
		tenantID, book.ID).Scan(&impairment); err != nil {
		return AdjustResult{}, err
	}
	scheduled := decimal.Zero
	if lines, err := domain.Schedule(book.Profile()); err == nil {
		scheduled = domain.ScheduledThrough(lines, deref(book.LastDepreciatedPeriod))
	}
	adj, err := domain.Adjust(kind, domain.BookBalance{Cost: book.Cost, Accumulated: book.AccumulatedDepreciation, Impairment: impairment,
		Scheduled: scheduled, Currency: book.Currency}, amount)
	if err != nil {
		return AdjustResult{}, err
	}
	t, err := postTransaction(ctx, tx, tenantID, book, txRow{Type: string(kind), Date: date, Cost: adj.CostDelta, Accumulated: adj.AccumulatedDelta,
		Impairment: adj.ImpairmentDelta, SourceType: sourceType, SourceID: sourceID})
	if err != nil {
		return AdjustResult{}, err
	}
	cost := book.Cost.Add(adj.CostDelta)
	acc := book.AccumulatedDepreciation.Add(adj.AccumulatedDelta)
	updated, err := scanBook(tx.QueryRow(ctx, `update asset_book set cost = $3, accumulated_depreciation = $4, nbv = $5
		where tenant_id = $1 and id = $2 returning `+bookColumns, tenantID, book.ID, cost, acc, cost.Sub(acc)))
	if err != nil {
		return AdjustResult{}, err
	}
	if err := enqueue(ctx, tx, "asset", book.AssetID, assetevents.AssetAdjusted, tenantID, assetevents.AssetAdjustedData{
		AssetID: book.AssetID, BookID: book.ID, TransactionID: t.ID, Type: string(kind), TransactionDate: date.String(),
		CostDelta: adj.CostDelta.String(), AccumulatedDelta: adj.AccumulatedDelta.String(), ImpairmentDelta: adj.ImpairmentDelta.String(),
		Currency: book.Currency, SourceType: sourceType, SourceID: sourceID,
	}); err != nil {
		return AdjustResult{}, err
	}
	return AdjustResult{Book: updated, Transaction: t, Limited: adj.Limited, Requested: amount}, nil
}

// Adjust posts an acquisition or depreciation adjustment, a revaluation or an
// impairment (write-down or capped reversal) on one Open book (FR-37.9).
func (s *Postgres) Adjust(ctx context.Context, assetID string, in AdjustmentInput, idempotencyKey string) (AdjustResult, error) {
	p := principal(ctx)
	var out AdjustResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			AssetID string
			Input   AdjustmentInput
		}{assetID, in}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "asset.adjust", request, func() (AdjustResult, error) {
			if err := lockOpenAsset(ctx, tx, p.TenantID, assetID, false); err != nil {
				return AdjustResult{}, err
			}
			book, err := scanBook(tx.QueryRow(ctx, `select `+bookColumns+` from asset_book where tenant_id = $1 and asset_id = $2 and id = $3 for update`,
				p.TenantID, assetID, in.BookID))
			if errors.Is(err, pgx.ErrNoRows) {
				return AdjustResult{}, notFound(CodeBookNotFound, "no book "+in.BookID+" on asset "+assetID)
			}
			if err != nil {
				return AdjustResult{}, err
			}
			res, err := applyAdjustment(ctx, tx, p.TenantID, book, in.Type, in.Date, in.Amount, "manual", nil)
			if err != nil {
				return AdjustResult{}, err
			}
			return res, bumpAsset(ctx, tx, p.TenantID, assetID)
		})
		return err
	})
	return out, err
}

// AddAddition records an improvement, component or accessory under an asset
// (FR-37.14, at most 500 per asset). A financial addition also posts an
// AcquisitionAdj of its cost on the selected Open books, linked by
// source_type 'addition' (AC-37.12).
func (s *Postgres) AddAddition(ctx context.Context, assetID string, in AdditionInput, idempotencyKey string) (Addition, error) {
	p := principal(ctx)
	var out Addition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			AssetID string
			Input   AdditionInput
		}{assetID, in}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "asset.addition.add", request, func() (Addition, error) {
			if err := lockOpenAsset(ctx, tx, p.TenantID, assetID, !in.Financial); err != nil {
				return Addition{}, err
			}
			var count int
			if err := tx.QueryRow(ctx, `select count(*) from asset_addition where tenant_id = $1 and asset_id = $2`, p.TenantID, assetID).Scan(&count); err != nil {
				return Addition{}, err
			}
			if count >= MaxAdditionsPerAsset {
				return Addition{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeAdditionLimit, "an asset takes at most "+strconv.Itoa(MaxAdditionsPerAsset)+" additions")
			}
			var books []Book
			if in.Financial {
				if books, err = additionBooks(ctx, tx, p.TenantID, assetID, in); err != nil {
					return Addition{}, err
				}
			}
			var currency *string
			if in.Financial {
				currency = &in.Currency
			}
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into asset_addition (tenant_id, id, asset_id, legal_entity_id, type, description, quantity, serial_number,
				added_date, financial, cost_amount, currency)
				select $1, $2, a.id, a.legal_entity_id, $4, $5, $6, $7, $8, $9, $10, $11 from asset a where a.tenant_id = $1 and a.id = $3`,
				p.TenantID, id, assetID, in.Type, in.Description, in.Quantity, in.SerialNumber, in.AddedDate, in.Financial, in.Cost, currency); err != nil {
				return Addition{}, err
			}
			for _, b := range books {
				if _, err := applyAdjustment(ctx, tx, p.TenantID, b, domain.AdjustmentAcquisition, in.AddedDate, in.Cost, sourceAddition, &id); err != nil {
					return Addition{}, err
				}
			}
			if err := bumpAsset(ctx, tx, p.TenantID, assetID); err != nil {
				return Addition{}, err
			}
			additions, err := loadAdditions(ctx, tx, p.TenantID, assetID, id)
			if err != nil {
				return Addition{}, err
			}
			return additions[0], nil
		})
		return err
	})
	return out, err
}

// additionBooks locks the Open books a financial addition posts to: the
// selected ones, or every Open book. Each must be in the addition's currency.
func additionBooks(ctx context.Context, tx pgx.Tx, tenantID, assetID string, in AdditionInput) ([]Book, error) {
	all, err := loadBooks(ctx, tx, tenantID, assetID, true)
	if err != nil {
		return nil, err
	}
	selected := len(in.BookIDs) > 0
	want := map[string]bool{}
	for _, id := range in.BookIDs {
		want[id] = true
	}
	var out []Book
	for _, b := range all {
		if selected && !want[b.ID] {
			continue
		}
		delete(want, b.ID)
		if b.Status != string(domain.StatusOpen) {
			if len(in.BookIDs) > 0 {
				return nil, &domain.Error{Code: domain.CodeAssetState, Detail: "book " + b.BookCode + " is " + b.Status + "; only Open books take additions"}
			}
			continue
		}
		if b.Currency != in.Currency {
			return nil, &domain.Error{Code: domain.CodeInvalidAdjustment, Detail: "book " + b.BookCode + " is in " + b.Currency + ", not " + in.Currency +
				"; select the books in the addition's currency"}
		}
		out = append(out, b)
	}
	for id := range want {
		return nil, notFound(CodeBookNotFound, "no book "+id+" on asset "+assetID)
	}
	if len(out) == 0 {
		return nil, &domain.Error{Code: domain.CodeAssetState, Detail: "the asset has no Open book to post the addition to"}
	}
	return out, nil
}

// ListAdditions returns the additions of an asset, oldest first, each with its transactions.
func (s *Postgres) ListAdditions(ctx context.Context, assetID string) ([]Addition, error) {
	p := principal(ctx)
	var out []Addition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadAsset(ctx, tx, p.TenantID, assetID); err != nil {
			return err
		}
		var err error
		out, err = loadAdditions(ctx, tx, p.TenantID, assetID, "")
		return err
	})
	return out, err
}

// loadAdditions reads the additions of an asset (only one when id is set) with
// the transactions each posted.
func loadAdditions(ctx context.Context, tx pgx.Tx, tenantID, assetID, id string) ([]Addition, error) {
	rows, err := tx.Query(ctx, `select id, asset_id, type, description, quantity, serial_number, added_date, financial, cost_amount, currency, created_at
		from asset_addition where tenant_id = $1 and asset_id = $2 and ($3::text = '' or id::text = $3) order by added_date, created_at, id`,
		tenantID, assetID, id)
	if err != nil {
		return nil, err
	}
	out := []Addition{}
	index := map[string]int{}
	for rows.Next() {
		var a Addition
		if err := rows.Scan(&a.ID, &a.AssetID, &a.Type, &a.Description, &a.Quantity, &a.SerialNumber, &a.AddedDate, &a.Financial, &a.Cost,
			&a.Currency, &a.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		a.Transactions = []Transaction{}
		index[a.ID] = len(out)
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	txRows, err := tx.Query(ctx, `select `+txColumns+` from asset_transaction t
		join asset_book b on b.tenant_id = t.tenant_id and b.id = t.asset_book_id
		where t.tenant_id = $1 and b.asset_id = $2 and t.source_type = $3 order by b.book_code`, tenantID, assetID, sourceAddition)
	if err != nil {
		return nil, err
	}
	defer txRows.Close()
	for txRows.Next() {
		t, err := scanTx(txRows)
		if err != nil {
			return nil, err
		}
		if i, ok := index[deref(t.SourceID)]; ok {
			out[i].Transactions = append(out[i].Transactions, t)
		}
	}
	return out, txRows.Err()
}

func runLockKey(tenantID string, in RunInput) string {
	return "assets.depreciation-run/" + tenantID + "/" + in.LegalEntityID + "/" + in.BookCode + "/" + in.Period
}

// tryRunLock takes the per-(legal entity, book, period) advisory lock for the
// transaction (BR-37.9); a concurrent run gets 409 AST-0504.
func tryRunLock(ctx context.Context, tx pgx.Tx, key string) error {
	var locked bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtextextended($1, 0))`, key).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return httpx.NewProblem(http.StatusConflict, domain.CodeRunLocked, "another depreciation run holds this book and period; wait for it to finish")
	}
	return nil
}

// decideBook computes a book's run line for period from its current balances.
func decideBook(b Book, period string) (RunLine, bool) {
	line := RunLine{AssetBookID: b.ID, AssetID: b.AssetID, Amount: decimal.Zero, ScheduledAmount: decimal.Zero}
	exception := func(code, detail string) (RunLine, bool) {
		line.Status, line.ExceptionCode, line.Detail = "Exception", &code, &detail
		return line, true
	}
	lines, err := domain.Schedule(b.RunProfile())
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return exception(de.Code, de.Detail)
		}
		return exception(domain.CodeInvalidDepreciation, err.Error())
	}
	d := domain.DecideRun(lines, deref(b.LastDepreciatedPeriod), period)
	switch d.Outcome {
	case domain.RunDue:
		line.Status = "Due"
		line.ScheduledAmount = d.Line.Amount
		line.Amount = domain.RunAmount(lines, d.Line, b.Cost, b.SalvageValue, b.AccumulatedDepreciation, b.Currency)
		return line, true
	case domain.RunException:
		return exception(d.Code, d.Detail)
	}
	return line, false
}

// ProposeRun decides every Open book with the book code in the legal entity for
// one period and stores the proposal (FR-37.5). Proposing again refreshes a
// run that is still Proposed (new version); a Posted run for the same book and
// period is rejected (AST-0502), and a concurrent request gets AST-0504.
func (s *Postgres) ProposeRun(ctx context.Context, in RunInput, idempotencyKey string) (DepreciationRun, error) {
	p := principal(ctx)
	var out DepreciationRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "depreciation-run.propose", in, func() (DepreciationRun, error) {
			if err := tryRunLock(ctx, tx, runLockKey(p.TenantID, in)); err != nil {
				return DepreciationRun{}, err
			}
			existing, err := scanRun(tx.QueryRow(ctx, `select `+runColumns+` from depreciation_run
				where tenant_id = $1 and legal_entity_id = $2 and book_code = $3 and period = $4 for update`,
				p.TenantID, in.LegalEntityID, in.BookCode, in.Period))
			found := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return DepreciationRun{}, err
			}
			if found && existing.Status == "Posted" {
				return DepreciationRun{}, httpx.NewProblem(http.StatusConflict, domain.CodeAlreadyPosted,
					fmt.Sprintf("depreciation for book %s period %s is already posted", in.BookCode, in.Period))
			}
			rows, err := tx.Query(ctx, `select `+bookColumns+` from asset_book
				where tenant_id = $1 and legal_entity_id = $2 and book_code = $3 and status = 'Open' order by asset_id`,
				p.TenantID, in.LegalEntityID, in.BookCode)
			if err != nil {
				return DepreciationRun{}, err
			}
			var books []Book
			for rows.Next() {
				b, err := scanBook(rows)
				if err != nil {
					rows.Close()
					return DepreciationRun{}, err
				}
				books = append(books, b)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return DepreciationRun{}, err
			}
			var currency *string
			var lines []RunLine
			total, due, exceptions := decimal.Zero, 0, 0
			for _, b := range books {
				if currency == nil {
					currency = &b.Currency
				} else if *currency != b.Currency {
					return DepreciationRun{}, &domain.Error{Code: domain.CodeInvalidDepreciation,
						Detail: "book " + in.BookCode + " has books in " + *currency + " and " + b.Currency + "; a run covers one currency"}
				}
				line, ok := decideBook(b, in.Period)
				if !ok {
					continue
				}
				if line.Status == "Due" {
					due++
					total = total.Add(line.Amount)
				} else {
					exceptions++
				}
				lines = append(lines, line)
			}
			id := ids.New()
			if found {
				id = existing.ID
				if _, err := tx.Exec(ctx, `delete from depreciation_run_line where tenant_id = $1 and run_id = $2`, p.TenantID, id); err != nil {
					return DepreciationRun{}, err
				}
				if _, err := tx.Exec(ctx, `update depreciation_run set currency = $3, asset_count = $4, exception_count = $5, total_amount = $6,
					version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, id, currency, due, exceptions, total); err != nil {
					return DepreciationRun{}, err
				}
			} else if _, err := tx.Exec(ctx, `insert into depreciation_run (tenant_id, id, legal_entity_id, book_code, period, currency, asset_count, exception_count, total_amount)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, id, in.LegalEntityID, in.BookCode, in.Period, currency, due, exceptions, total); err != nil {
				return DepreciationRun{}, err
			}
			for _, l := range lines {
				if _, err := tx.Exec(ctx, `insert into depreciation_run_line (tenant_id, run_id, asset_book_id, asset_id, legal_entity_id, status, amount,
					scheduled_amount, exception_code, detail) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
					p.TenantID, id, l.AssetBookID, l.AssetID, in.LegalEntityID, l.Status, l.Amount, l.ScheduledAmount, l.ExceptionCode, l.Detail); err != nil {
					return DepreciationRun{}, err
				}
			}
			return loadRun(ctx, tx, p.TenantID, id)
		})
		return err
	})
	return out, err
}

// PostRun posts a Proposed run at the version the caller reviewed (If-Match).
// Each due book is re-decided under a row lock: when it still matches the
// proposal a Depreciation transaction dated the period end is posted and the
// book's accumulated depreciation and last depreciated period move; a book that
// changed since the proposal becomes an exception (AST-0018) and the others
// still post. One depreciation.posted event carries the totals.
func (s *Postgres) PostRun(ctx context.Context, runID string, version int) (DepreciationRun, error) {
	p := principal(ctx)
	var out DepreciationRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := scanRun(tx.QueryRow(ctx, `select `+runColumns+` from depreciation_run where tenant_id = $1 and id = $2 for update`, p.TenantID, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeRunNotFound, "no depreciation run with id "+runID)
		}
		if err != nil {
			return err
		}
		if run.Status == "Posted" {
			return httpx.NewProblem(http.StatusConflict, domain.CodeAlreadyPosted,
				fmt.Sprintf("depreciation for book %s period %s is already posted", run.BookCode, run.Period))
		}
		if run.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
				fmt.Sprintf("the run was proposed again since you read it (version %d, current %d); review it and try again", version, run.Version))
		}
		if err := tryRunLock(ctx, tx, runLockKey(p.TenantID, RunInput{LegalEntityID: run.LegalEntityID, BookCode: run.BookCode, Period: run.Period})); err != nil {
			return err
		}
		end, err := domain.PeriodEnd(run.Period)
		if err != nil {
			return err
		}
		date := NewDate(end)
		// Lock order is asset, then book, as in Adjust, AddBook and Dispose, so a
		// concurrent adjustment cannot deadlock with the posting.
		rows, err := tx.Query(ctx, `select asset_id, asset_book_id, amount from depreciation_run_line
			where tenant_id = $1 and run_id = $2 and status = 'Due' order by asset_id, asset_book_id`, p.TenantID, runID)
		if err != nil {
			return err
		}
		type due struct {
			assetID, bookID string
			amount          decimal.Decimal
		}
		var dues []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.assetID, &d.bookID, &d.amount); err != nil {
				rows.Close()
				return err
			}
			dues = append(dues, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		total, posted := decimal.Zero, 0
		touched := map[string]bool{}
		locked := map[string]bool{}
		for _, d := range dues {
			if !locked[d.assetID] {
				locked[d.assetID] = true
				if _, err := tx.Exec(ctx, `select 1 from asset where tenant_id = $1 and id = $2 for update`, p.TenantID, d.assetID); err != nil {
					return err
				}
			}
			book, err := scanBook(tx.QueryRow(ctx, `select `+bookColumns+` from asset_book where tenant_id = $1 and id = $2 for update`, p.TenantID, d.bookID))
			if err != nil {
				return err
			}
			line, ok := decideBook(book, run.Period)
			if book.Status != string(domain.StatusOpen) {
				line, ok = RunLine{Status: "Exception", ExceptionCode: ptr(domain.CodeAssetState), Detail: ptr("book is " + book.Status)}, true
			}
			if !ok || line.Status != "Due" || !line.Amount.Equal(d.amount) {
				code, detail := domain.CodeRunStale, "the book changed since the proposal; propose the run again"
				if ok && line.Status == "Exception" {
					code, detail = deref(line.ExceptionCode), deref(line.Detail)
				}
				if _, err := tx.Exec(ctx, `update depreciation_run_line set status = 'Exception', exception_code = $4, detail = $5
					where tenant_id = $1 and run_id = $2 and asset_book_id = $3`, p.TenantID, runID, d.bookID, code, detail); err != nil {
					return err
				}
				continue
			}
			if d.amount.IsPositive() {
				if _, err := postTransaction(ctx, tx, p.TenantID, book, txRow{Type: "Depreciation", Date: date, Cost: 0, Accumulated: d.amount, Impairment: 0,
					SourceType: sourceRun, SourceID: &runID, RunID: &runID}); err != nil {
					return err
				}
			}
			acc := book.AccumulatedDepreciation.Add(d.amount)
			if _, err := tx.Exec(ctx, `update asset_book set accumulated_depreciation = $3, nbv = $4, last_depreciated_period = $5
				where tenant_id = $1 and id = $2`, p.TenantID, book.ID, acc, book.Cost.Sub(acc), run.Period); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update depreciation_run_line set status = 'Posted' where tenant_id = $1 and run_id = $2 and asset_book_id = $3`,
				p.TenantID, runID, d.bookID); err != nil {
				return err
			}
			if !touched[book.AssetID] {
				touched[book.AssetID] = true
				if err := bumpAsset(ctx, tx, p.TenantID, book.AssetID); err != nil {
					return err
				}
			}
			total = total.Add(d.amount)
			posted++
		}
		if _, err := tx.Exec(ctx, `update depreciation_run set status = 'Posted', posted_at = now(), version = version + 1, asset_count = $3, total_amount = $4,
			exception_count = (select count(*) from depreciation_run_line l where l.tenant_id = $1 and l.run_id = $2 and l.status = 'Exception')
			where tenant_id = $1 and id = $2`, p.TenantID, runID, posted, total); err != nil {
			return err
		}
		if out, err = loadRun(ctx, tx, p.TenantID, runID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "depreciation_run", runID, assetevents.DepreciationPosted, p.TenantID, assetevents.DepreciationPostedData{
			RunID: runID, LegalEntityID: run.LegalEntityID, BookCode: run.BookCode, Period: run.Period, AssetCount: posted,
			TotalAmount: total.String(), Currency: run.Currency, JournalEntryID: nil,
		})
	})
	return out, err
}

func ptr(s string) *string { return &s }

// GetRun reads a run with its lines.
func (s *Postgres) GetRun(ctx context.Context, runID string) (DepreciationRun, error) {
	p := principal(ctx)
	var out DepreciationRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadRun(ctx, tx, p.TenantID, runID)
		return err
	})
	return out, err
}

// ListRuns returns one page of runs, newest first, without lines.
func (s *Postgres) ListRuns(ctx context.Context, f RunFilter) (RunPage, error) {
	p := principal(ctx)
	page := RunPage{Items: []DepreciationRun{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` from depreciation_run r
			where r.tenant_id = $1
			  and ($2::text = '' or r.legal_entity_id::text = $2)
			  and ($3::text = '' or (r.created_at, r.id) < (select c.created_at, c.id from depreciation_run c where c.tenant_id = $1 and c.id::text = $3))
			order by r.created_at desc, r.id desc limit $4`, p.TenantID, f.LegalEntityID, f.After, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, r)
		}
		return rows.Err()
	})
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, err
}

func loadRun(ctx context.Context, tx pgx.Tx, tenantID, id string) (DepreciationRun, error) {
	r, err := scanRun(tx.QueryRow(ctx, `select `+runColumns+` from depreciation_run where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DepreciationRun{}, notFound(CodeRunNotFound, "no depreciation run with id "+id)
	}
	if err != nil {
		return DepreciationRun{}, err
	}
	rows, err := tx.Query(ctx, `select l.asset_book_id, l.asset_id, a.asset_number, l.status, l.amount, l.scheduled_amount, l.exception_code, l.detail
		from depreciation_run_line l join asset a on a.tenant_id = l.tenant_id and a.id = l.asset_id
		where l.tenant_id = $1 and l.run_id = $2 order by a.asset_number, l.asset_book_id`, tenantID, id)
	if err != nil {
		return DepreciationRun{}, err
	}
	defer rows.Close()
	r.Lines = []RunLine{}
	for rows.Next() {
		var l RunLine
		if err := rows.Scan(&l.AssetBookID, &l.AssetID, &l.AssetNumber, &l.Status, &l.Amount, &l.ScheduledAmount, &l.ExceptionCode, &l.Detail); err != nil {
			return DepreciationRun{}, err
		}
		r.Lines = append(r.Lines, l)
	}
	return r, rows.Err()
}
