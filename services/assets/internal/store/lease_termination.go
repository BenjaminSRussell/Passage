package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	assetevents "github.com/BenjaminSRussell/passage/services/assets/internal/events"
)

// LeaseTerminationInput is a validated early termination (D4c). Version is the lease version the
// caller read (If-Match). AfterPeriod is the last payment period the lease stays in force. Payment
// is what the lessee pays to end the lease (0 or more).
type LeaseTerminationInput struct {
	Version     int             `json:"version"`
	AfterPeriod int             `json:"afterPeriod"`
	Payment     decimal.Decimal `json:"terminationPayment"`
	Reason      string          `json:"reason"`
	// AsOf is today for the rule that the termination takes effect by the end of the period running now
	// (domain.TerminationNotAhead); zero means the current time. It is not part of the idempotent request.
	AsOf time.Time `json:"-"`
}

// LeaseTermination is the recorded derecognition of a terminated lease. GainLoss is positive for a
// gain. JournalID is null until the termination journal is in the general ledger.
type LeaseTermination struct {
	ID                    string          `json:"id"`
	LeaseID               string          `json:"leaseId"`
	AfterPeriod           int             `json:"afterPeriod"`
	TerminationDate       Date            `json:"terminationDate"`
	Reason                string          `json:"reason"`
	Currency              string          `json:"currency"`
	LiabilityDerecognized decimal.Decimal `json:"liabilityDerecognized"`
	ROUGross              decimal.Decimal `json:"rouGross"`
	ROUAccumulated        decimal.Decimal `json:"rouAccumulated"`
	ROUCarrying           decimal.Decimal `json:"rouCarrying"`
	TerminationPayment    decimal.Decimal `json:"terminationPayment"`
	GainLoss              decimal.Decimal `json:"gainLoss"`
	JournalID             *string         `json:"journalId"`
	CreatedBy             string          `json:"createdBy"`
	CreatedAt             time.Time       `json:"createdAt"`
}

// LeaseTerminationResult is the termination and the lease after it (the schedule cut after the last period in force).
type LeaseTerminationResult struct {
	Termination LeaseTermination `json:"termination"`
	Lease       Lease            `json:"lease"`
	Messages    []string         `json:"messages"`
}

const terminationColumns = `id, lease_id, after_period, termination_date, reason, currency, liability_derecognized, rou_gross, rou_accumulated, rou_carrying,
	termination_payment, gain_loss, journal_id::text, created_by, created_at`

func scanTermination(row pgx.Row) (LeaseTermination, error) {
	var t LeaseTermination
	err := row.Scan(&t.ID, &t.LeaseID, &t.AfterPeriod, &t.TerminationDate, &t.Reason, &t.Currency, &t.LiabilityDerecognized, &t.ROUGross, &t.ROUAccumulated,
		&t.ROUCarrying, &t.TerminationPayment, &t.GainLoss, &t.JournalID, &t.CreatedBy, &t.CreatedAt)
	return t, err
}

// loadTermination reads the termination of a lease, nil when it was not terminated.
func loadTermination(ctx context.Context, tx pgx.Tx, tenantID, leaseID string) (*LeaseTermination, error) {
	t, err := scanTermination(tx.QueryRow(ctx, `select `+terminationColumns+` from lease_termination where tenant_id = $1 and lease_id = $2`, tenantID, leaseID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// TerminateLease ends an Active or Modified lease early at the end of period AfterPeriod: the
// schedule after that period is dropped, the liability and the ROU asset carrying amount are
// derecognized with the termination payment and the gain or loss, the lease becomes Terminated
// and lease.terminated is written to the outbox in the same transaction. A period after the
// termination that is already posted to the ledger, or a ledger booking in flight, refuses it
// (AST-0027); so does a period AfterPeriod that has not started by AsOf (AST-0036: ending the lease
// later is a change of term, a modification). The termination journal is booked by the lease posting (POST /v1/leases/gl-postings).
func (s *Postgres) TerminateLease(ctx context.Context, leaseID string, in LeaseTerminationInput, idempotencyKey string) (LeaseTerminationResult, error) {
	p := principal(ctx)
	var out LeaseTerminationResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			LeaseID string
			Input   LeaseTerminationInput
		}{leaseID, in}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "lease.terminate", request, func() (LeaseTerminationResult, error) {
			lease, err := loadLease(ctx, tx, p.TenantID, leaseID, true)
			if err != nil {
				return LeaseTerminationResult{}, err
			}
			if lease.Version != in.Version {
				return LeaseTerminationResult{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					fmt.Sprintf("the lease changed since you read it (version %d, current %d); reload and try again", in.Version, lease.Version))
			}
			if err := domain.CanTerminate(lease.Status); err != nil {
				return LeaseTerminationResult{}, err
			}
			if err := domain.ValidTerminationPeriod(in.AfterPeriod, len(lease.Schedule)); err != nil {
				return LeaseTerminationResult{}, err
			}
			asOf := in.AsOf
			if asOf.IsZero() {
				asOf = time.Now().UTC()
			}
			if err := domain.TerminationNotAhead(lease.CommencementDate.Time, lease.PaymentsPerYear, in.AfterPeriod, asOf); err != nil {
				return LeaseTerminationResult{}, err
			}
			last := lease.Schedule[in.AfterPeriod-1]
			if last.PeriodNo != in.AfterPeriod {
				return LeaseTerminationResult{}, fmt.Errorf("lease %s schedule is not numbered from 1 (line %d is period %d)", leaseID, in.AfterPeriod, last.PeriodNo)
			}
			var posted bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from lease_schedule_line where tenant_id = $1 and lease_id = $2
				and period_no > $3 and posted_journal_id is not null)`, p.TenantID, leaseID, in.AfterPeriod).Scan(&posted); err != nil {
				return LeaseTerminationResult{}, err
			}
			if posted {
				return LeaseTerminationResult{}, &domain.Error{Code: CodeLeasePeriodPosted,
					Detail: fmt.Sprintf("a period after %d is already posted to the ledger; terminate after the last posted period or later", in.AfterPeriod)}
			}
			var inFlight bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from asset_pending_booking where tenant_id = $1 and book_code = 'LEASE' and document_id = $2)`,
				p.TenantID, leaseID).Scan(&inFlight); err != nil {
				return LeaseTerminationResult{}, err
			}
			if inFlight {
				return LeaseTerminationResult{}, &domain.Error{Code: CodeLeasePeriodPosted,
					Detail: "a ledger booking of this lease is awaiting the ledger's answer; post the lease again to settle it, then terminate"}
			}
			accumulated, remaining := decimal.Zero, decimal.Zero
			for _, l := range lease.Schedule {
				if l.PeriodNo <= in.AfterPeriod {
					accumulated = accumulated.Add(l.ROUDepreciation)
				} else {
					remaining = remaining.Add(l.ROUDepreciation)
				}
			}
			// Modifications that take effect after the termination were never booked: their ROU adjustment is not part of what is derecognized.
			for _, m := range lease.Modifications {
				if !m.Superseded && m.EffectivePeriod > in.AfterPeriod {
					remaining = remaining.Sub(m.ROUAdjustment)
				}
			}
			minor := money.MinorUnits(lease.Currency)
			t, err := domain.ComputeTermination(domain.TerminationInput{Currency: lease.Currency, Liability: last.Closing,
				ROUAccumulated: accumulated, ROURemaining: remaining, Payment: in.Payment})
			if err != nil {
				return LeaseTerminationResult{}, err
			}
			date := domain.LeaseTerminationDate(lease.CommencementDate.Time, lease.PaymentsPerYear, in.AfterPeriod)
			if _, err := tx.Exec(ctx, `delete from lease_schedule_line where tenant_id = $1 and lease_id = $2 and period_no > $3`, p.TenantID, leaseID, in.AfterPeriod); err != nil {
				return LeaseTerminationResult{}, err
			}
			rec, err := scanTermination(tx.QueryRow(ctx, `insert into lease_termination (tenant_id, id, lease_id, legal_entity_id, after_period, termination_date, reason, currency,
				liability_derecognized, rou_gross, rou_accumulated, rou_carrying, termination_payment, gain_loss, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) returning `+terminationColumns,
				p.TenantID, ids.New(), leaseID, lease.LegalEntityID, in.AfterPeriod, date, in.Reason, lease.Currency, t.Liability, t.ROUGross, t.ROUAccumulated,
				t.ROUCarrying, t.Payment, t.GainLoss, p.UserID))
			if err != nil {
				return LeaseTerminationResult{}, err
			}
			if _, err := tx.Exec(ctx, `update lease set status = $3, version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, leaseID, domain.LeaseTerminated); err != nil {
				return LeaseTerminationResult{}, err
			}
			if err := enqueue(ctx, tx, "lease", leaseID, assetevents.LeaseTerminated, p.TenantID, assetevents.LeaseTerminatedData{
				LeaseID: leaseID, TerminationID: rec.ID, LegalEntityID: lease.LegalEntityID, AfterPeriod: rec.AfterPeriod, TerminationDate: date.Format(DateLayout),
				LiabilityDerecognized: t.Liability.String(), ROUGross: t.ROUGross.String(), ROUAccumulated: t.ROUAccumulated.String(), ROUCarrying: t.ROUCarrying.String(),
				TerminationPayment: t.Payment.String(), GainLoss: t.GainLoss.String(), Currency: lease.Currency,
			}); err != nil {
				return LeaseTerminationResult{}, err
			}
			after, err := loadLease(ctx, tx, p.TenantID, leaseID, false)
			if err != nil {
				return LeaseTerminationResult{}, err
			}
			word := "gain"
			if t.GainLoss.IsNegative() {
				word = "loss"
			}
			msg := fmt.Sprintf("%s: lease terminated after period %d; liability %s %s and ROU asset %s %s derecognized, %s %s %s",
				domain.CodeLeaseTermination, in.AfterPeriod, t.Liability.StringFixed(minor), lease.Currency, t.ROUCarrying.StringFixed(minor), lease.Currency,
				word, t.GainLoss.Abs().StringFixed(minor), lease.Currency)
			return LeaseTerminationResult{Termination: rec, Lease: after, Messages: []string{msg}}, nil
		})
		return err
	})
	return out, err
}
