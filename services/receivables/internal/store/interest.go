package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// InterestRequest selects the calculation for one legal entity (BR-30.5).
type InterestRequest struct {
	LegalEntityID   string          `json:"legalEntityId"`
	CalculationDate string          `json:"calculationDate"`
	AnnualRate      decimal.Decimal `json:"annualRate"`
	Basis           string          `json:"basis"`
	GraceDays       int             `json:"graceDays"`
}

// InterestNote records interest charged on one item for one period.
type InterestNote struct {
	ID              string          `json:"id"`
	LegalEntityID   string          `json:"legalEntityId"`
	CustomerID      string          `json:"customerId"`
	OpenItemID      string          `json:"openItemId"`
	DocumentNumber  string          `json:"documentNumber"`
	InterestItemID  string          `json:"interestItemId"`
	Period          string          `json:"period"`
	CalculationDate string          `json:"calculationDate"`
	Days            int             `json:"days"`
	AnnualRate      decimal.Decimal `json:"annualRate"`
	Basis           string          `json:"basis"`
	Currency        string          `json:"currency"`
	Principal       decimal.Decimal `json:"principal"`
	Amount          decimal.Decimal `json:"amount"`
}

// InterestResult is the calculation, the exclusions, and (on post) the notes written.
type InterestResult struct {
	Candidates []domain.InterestCandidate `json:"candidates"`
	Exclusions []domain.Exclusion         `json:"exclusions"`
	Posted     []InterestNote             `json:"posted"`
}

// CalculateInterest previews interest without writing anything.
func (s *Postgres) CalculateInterest(ctx context.Context, req InterestRequest) (InterestResult, error) {
	var out InterestResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		items, err := loadDunnable(ctx, tx, req.LegalEntityID)
		if err != nil {
			return err
		}
		out = planInterest(parseDay(req.CalculationDate), req, items)
		return nil
	})
	return out, err
}

func planInterest(asOf time.Time, req InterestRequest, items []domain.DunnableItem) InterestResult {
	cands, excl := domain.PlanInterest(asOf, req.GraceDays, req.AnnualRate, req.Basis, items)
	if cands == nil {
		cands = []domain.InterestCandidate{}
	}
	if excl == nil {
		excl = []domain.Exclusion{}
	}
	return InterestResult{Candidates: cands, Exclusions: excl, Posted: []InterestNote{}}
}

// interestHash identifies one post command for Idempotency-Key replay.
func interestHash(req InterestRequest) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"interest", req.LegalEntityID, req.CalculationDate, req.AnnualRate.String(),
		req.Basis, strconv.Itoa(req.GraceDays)}, "|")))
	return hex.EncodeToString(sum[:])
}

// PostInterest writes one INTEREST open item and one interest note per candidate
// (FR-30.8). A transaction-scoped advisory lock per legal entity serialises
// concurrent posts, so the second one sees the first one's notes. An item gets
// at most one note per period (calendar month of the calculation date), and the
// next note charges only the days after the last one. A repeated
// Idempotency-Key replays the stored response.
func (s *Postgres) PostInterest(ctx context.Context, req InterestRequest, key string) (InterestResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out InterestResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('receivables.interest.' || $1 || '.' || $2, 0))`,
			p.TenantID, req.LegalEntityID); err != nil {
			return err
		}
		hash := interestHash(req)
		if ok, err := replayJSON(ctx, tx, p.TenantID, key, hash, &out); err != nil || ok {
			return err
		}
		items, err := loadDunnable(ctx, tx, req.LegalEntityID)
		if err != nil {
			return err
		}
		asOf := parseDay(req.CalculationDate)
		out = planInterest(asOf, req, items)
		period := asOf.Format("2006-01")
		for _, c := range out.Candidates {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from interest_note where open_item_id = $1 and period = $2)`,
				c.OpenItemID, period).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			itemID, noteID := ids.New(), ids.New()
			if _, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
				document_date, due_date, currency, amount, open_amount, status)
				values ($1, $2, $3, $4, 'INTEREST', $5, $6::date, $6::date, $7, $8::numeric, $8::numeric, 'OPEN')`,
				p.TenantID, itemID, req.LegalEntityID, c.CustomerID, "IN-"+period+"-"+c.DocumentNumber, req.CalculationDate,
				c.Currency, c.Amount.String()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into interest_note (tenant_id, id, legal_entity_id, open_item_id, interest_item_id, period,
				calculation_date, days, annual_rate, basis, principal, amount)
				values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9::numeric, $10, $11::numeric, $12::numeric)`,
				p.TenantID, noteID, req.LegalEntityID, c.OpenItemID, itemID, period, req.CalculationDate, c.Days,
				req.AnnualRate.String(), req.Basis, c.Principal.String(), c.Amount.String()); err != nil {
				return err
			}
			if err := enqueueJournal(ctx, tx, p.TenantID, journalSpec{LegalEntityID: req.LegalEntityID, SourceType: JournalInterest, SourceID: noteID,
				SourceKey: "interest:" + noteID, DocumentID: "IN-" + period + "-" + c.DocumentNumber, PostingDate: req.CalculationDate, Currency: c.Currency,
				Lines: []journalLine{debit(domain.AccountReceivable, c.Amount, "Interest"), credit(domain.AccountInterestIncome, c.Amount, "Interest")}}); err != nil {
				return err
			}
			out.Posted = append(out.Posted, InterestNote{ID: noteID, LegalEntityID: req.LegalEntityID, CustomerID: c.CustomerID,
				OpenItemID: c.OpenItemID, DocumentNumber: c.DocumentNumber, InterestItemID: itemID, Period: period,
				CalculationDate: req.CalculationDate, Days: c.Days, AnnualRate: req.AnnualRate, Basis: req.Basis, Currency: c.Currency,
				Principal: c.Principal, Amount: c.Amount})
		}
		return rememberJSON(ctx, tx, p.TenantID, key, hash, out)
	})
	return out, mapWriteErr(err)
}

// ListInterestNotes reads posted interest notes, latest calculation first.
func (s *Postgres) ListInterestNotes(ctx context.Context, customerID string, limit int) ([]InterestNote, error) {
	out := []InterestNote{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select n.id, n.legal_entity_id, oi.customer_id, n.open_item_id, oi.document_number, n.interest_item_id,
			n.period, n.calculation_date::text, n.days, n.annual_rate::text, n.basis, oi.currency, n.principal::text, n.amount::text
			from interest_note n join open_item oi on oi.tenant_id = n.tenant_id and oi.id = n.open_item_id
			where ($1::text = '' or oi.customer_id::text = $1::text)
			order by n.calculation_date desc, oi.document_number, n.id limit $2`, customerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n InterestNote
			var rate, principal, amount string
			if err := rows.Scan(&n.ID, &n.LegalEntityID, &n.CustomerID, &n.OpenItemID, &n.DocumentNumber, &n.InterestItemID, &n.Period,
				&n.CalculationDate, &n.Days, &rate, &n.Basis, &n.Currency, &principal, &amount); err != nil {
				return err
			}
			if n.AnnualRate, err = decimal.NewFromString(rate); err != nil {
				return err
			}
			if n.Principal, err = decimal.NewFromString(principal); err != nil {
				return err
			}
			if n.Amount, err = decimal.NewFromString(amount); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}
