package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

const loanColumns = `id, legal_entity_id, role, counterparty_name, principal::text, currency, start_date::text, maturity_date::text,
	schedule_type, fixed_rate::text, day_count, period_months, status, version, created_at`

// Loan is a bank or intercompany loan contract with its generated schedule (FR-36.19).
type Loan struct {
	ID               string                `json:"id"`
	LegalEntityID    string                `json:"legalEntityId"`
	Role             string                `json:"role"`
	CounterpartyName string                `json:"counterpartyName"`
	Principal        string                `json:"principal"`
	Currency         string                `json:"currency"`
	StartDate        string                `json:"startDate"`
	MaturityDate     string                `json:"maturityDate"`
	ScheduleType     string                `json:"scheduleType"`
	FixedRate        string                `json:"fixedRate"`
	DayCount         string                `json:"dayCount"`
	PeriodMonths     int                   `json:"periodMonths"`
	Status           string                `json:"status"`
	Version          int                   `json:"version"`
	CreatedAt        time.Time             `json:"createdAt"`
	Schedule         []domain.ScheduleLine `json:"schedule,omitempty"`
}

// NewLoan is a validated loan create request with its computed schedule.
type NewLoan struct {
	LegalEntityID    string
	Role             string
	CounterpartyName string
	Principal        string
	Currency         string
	StartDate        string
	MaturityDate     string
	ScheduleType     string
	FixedRate        string
	DayCount         string
	PeriodMonths     int
	Schedule         []domain.ScheduleLine
}

func scanLoan(row pgx.Row) (Loan, error) {
	var l Loan
	err := row.Scan(&l.ID, &l.LegalEntityID, &l.Role, &l.CounterpartyName, &l.Principal, &l.Currency, &l.StartDate, &l.MaturityDate,
		&l.ScheduleType, &l.FixedRate, &l.DayCount, &l.PeriodMonths, &l.Status, &l.Version, &l.CreatedAt)
	return l, err
}

// getLoanTx loads a loan and its schedule inside an open transaction.
func getLoanTx(ctx context.Context, tx pgx.Tx, tenantID, id string) (Loan, error) {
	l, err := scanLoan(tx.QueryRow(ctx, `select `+loanColumns+` from treasury_loan where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return l, notFound(CodeLoanNotFound, "loan contract not found")
	}
	if err != nil {
		return l, err
	}
	rows, err := tx.Query(ctx, `select period_no, period_start::text, period_end::text, days, opening_principal::text, interest::text,
		principal_repayment::text, closing_principal::text from treasury_loan_schedule_line
		where tenant_id = $1 and loan_id = $2 order by period_no`, tenantID, id)
	if err != nil {
		return l, err
	}
	defer rows.Close()
	l.Schedule = []domain.ScheduleLine{}
	for rows.Next() {
		var line domain.ScheduleLine
		var opening, interest, repayment, closing string
		if err := rows.Scan(&line.PeriodNo, &line.PeriodStart, &line.PeriodEnd, &line.Days, &opening, &interest, &repayment, &closing); err != nil {
			return l, err
		}
		for _, p := range []struct {
			raw string
			dst *decimal.Decimal
		}{{opening, &line.OpeningPrincipal}, {interest, &line.Interest}, {repayment, &line.PrincipalRepayment}, {closing, &line.ClosingPrincipal}} {
			v, err := decimal.NewFromString(p.raw)
			if err != nil {
				return l, err
			}
			*p.dst = v
		}
		l.Schedule = append(l.Schedule, line)
	}
	return l, rows.Err()
}

// CreateLoan stores the contract and its schedule in one transaction; a
// repeated Idempotency-Key replays the stored loan.
func (s *Postgres) CreateLoan(ctx context.Context, in NewLoan, key string) (Loan, error) {
	p, _ := tenant.FromContext(ctx)
	reqHash, err := requestHash("create-loan", in)
	if err != nil {
		return Loan{}, err
	}
	var out Loan
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, reqHash, func() (Loan, error) {
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into treasury_loan (tenant_id, id, legal_entity_id, role, counterparty_name, principal, currency,
				start_date, maturity_date, schedule_type, fixed_rate, day_count, period_months)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				p.TenantID, id, in.LegalEntityID, in.Role, in.CounterpartyName, in.Principal, in.Currency, in.StartDate, in.MaturityDate,
				in.ScheduleType, in.FixedRate, in.DayCount, in.PeriodMonths); err != nil {
				return Loan{}, err
			}
			batch := &pgx.Batch{}
			for _, line := range in.Schedule {
				batch.Queue(`insert into treasury_loan_schedule_line (tenant_id, loan_id, legal_entity_id, period_no, period_start,
					period_end, days, opening_principal, interest, principal_repayment, closing_principal)
					values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
					p.TenantID, id, in.LegalEntityID, line.PeriodNo, line.PeriodStart, line.PeriodEnd, line.Days,
					line.OpeningPrincipal.String(), line.Interest.String(), line.PrincipalRepayment.String(), line.ClosingPrincipal.String())
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return Loan{}, err
			}
			return getLoanTx(ctx, tx, p.TenantID, id)
		})
		return err
	})
	return out, err
}

// ListLoans returns the loan contracts of one legal entity, newest first (without schedules).
func (s *Postgres) ListLoans(ctx context.Context, legalEntityID string) ([]Loan, error) {
	p, _ := tenant.FromContext(ctx)
	items := []Loan{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+loanColumns+` from treasury_loan where tenant_id = $1 and legal_entity_id = $2
			order by start_date desc, id`, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLoan(rows)
			if err != nil {
				return err
			}
			items = append(items, l)
		}
		return rows.Err()
	})
	return items, err
}

// GetLoan returns one loan contract with its schedule.
func (s *Postgres) GetLoan(ctx context.Context, id string) (Loan, error) {
	p, _ := tenant.FromContext(ctx)
	var out Loan
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = getLoanTx(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}
