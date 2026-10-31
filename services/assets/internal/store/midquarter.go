package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
)

// CodeMidQuarterCurrency refuses a mid-quarter test over books in more than
// one currency: their costs cannot be added without conversion.
const CodeMidQuarterCurrency = "AST-0029"

// maxReviewBooks caps the books listed for review; ReviewCount is the full count.
const maxReviewBooks = 500

// MidQuarterFilter selects one tax book (by code) of a legal entity and one
// tax year (a calendar year, NOT-IMPLEMENTED.md D1d).
type MidQuarterFilter struct {
	LegalEntityID string
	BookCode      string
	Year          int
}

// MidQuarterBook is a book placed in service in the tested year whose
// convention does not follow the test: a declining-balance book that is not on
// MidQuarter when the test applies, or a MidQuarter book when it does not.
type MidQuarterBook struct {
	AssetID       string          `json:"assetId"`
	AssetNumber   string          `json:"assetNumber"`
	BookID        string          `json:"bookId"`
	Method        string          `json:"method"`
	Convention    string          `json:"convention"`
	InServiceDate Date            `json:"inServiceDate"`
	Quarter       int             `json:"quarter"`
	Cost          decimal.Decimal `json:"cost"`
}

// MidQuarterReport is the BR-37.5 test for one tax book and year: the basis
// per quarter, the Q4 share, whether mid-quarter applies (AST-0511) and the
// books whose convention must be reviewed. Nothing is switched or posted: the
// PRD requires approval before the delta posts.
type MidQuarterReport struct {
	LegalEntityID string  `json:"legalEntityId"`
	BookCode      string  `json:"bookCode"`
	Currency      *string `json:"currency"`
	BookCount     int     `json:"bookCount"`
	domain.MidQuarterResult
	ReviewCount int              `json:"reviewCount"`
	Review      []MidQuarterBook `json:"review"`
}

// MidQuarterTest runs BR-37.5 over every book with the code placed in service
// in the year, at its current cost. Books placed in service and fully disposed
// of in the same year are left out, as IRS Pub. 946 excludes them; so are the
// emptied source books of a transfer or split, whose basis the target books
// carry with the same in-service date.
func (s *Postgres) MidQuarterTest(ctx context.Context, f MidQuarterFilter) (MidQuarterReport, error) {
	p := principal(ctx)
	out := MidQuarterReport{LegalEntityID: f.LegalEntityID, BookCode: f.BookCode, Review: []MidQuarterBook{}}
	var books []MidQuarterBook
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select a.id, a.asset_number, b.id, b.method, b.convention, b.in_service_date, b.cost, b.currency
			from asset_book b join asset a on a.tenant_id = b.tenant_id and a.id = b.asset_id
			where b.tenant_id = $1 and b.legal_entity_id = $2 and b.book_code = $3
			  and b.in_service_date >= make_date($4::int, 1, 1) and b.in_service_date < make_date($4::int + 1, 1, 1)
			  and b.status <> 'Transferred'
			  and not (b.status <> 'Open' and exists (select 1 from asset_transaction t
			      where t.tenant_id = b.tenant_id and t.asset_book_id = b.id and t.type in ('DisposalSale', 'DisposalScrap')
			        and t.transaction_date < make_date($4::int + 1, 1, 1)))
			order by a.asset_number, b.id`, p.TenantID, f.LegalEntityID, f.BookCode, f.Year)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b MidQuarterBook
			var currency string
			if err := rows.Scan(&b.AssetID, &b.AssetNumber, &b.BookID, &b.Method, &b.Convention, &b.InServiceDate, &b.Cost, &currency); err != nil {
				return err
			}
			if out.Currency != nil && *out.Currency != currency {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeMidQuarterCurrency,
					"books "+f.BookCode+" placed in service in the year use more than one currency; the test needs one")
			}
			out.Currency = &currency
			b.Quarter = domain.Quarter(b.InServiceDate.Time)
			books = append(books, b)
		}
		return rows.Err()
	})
	if err != nil {
		return MidQuarterReport{}, err
	}
	additions := make([]domain.YearAddition, len(books))
	for i, b := range books {
		additions[i] = domain.YearAddition{InService: b.InServiceDate.Time, Basis: b.Cost}
	}
	out.BookCount = len(books)
	out.MidQuarterResult = domain.MidQuarterTest(f.Year, additions)
	for _, b := range books {
		midQuarter := b.Convention == string(domain.ConventionMidQuarter)
		annual := b.Method == string(domain.MethodDecliningBalance) || b.Method == string(domain.MethodMACRS)
		if out.Applies && annual && !midQuarter || !out.Applies && midQuarter {
			out.ReviewCount++
			if len(out.Review) < maxReviewBooks {
				out.Review = append(out.Review, b)
			}
		}
	}
	return out, nil
}
