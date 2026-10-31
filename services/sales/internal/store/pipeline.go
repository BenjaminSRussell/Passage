package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

const (
	// ExpireBatchSize bounds one expiry run; More tells the caller to run again.
	ExpireBatchSize = 500
	// ExpiringSoonDays is the horizon of the "quotes expiring" count: an open
	// quotation whose last valid day is today or one of the next six days.
	ExpiringSoonDays = 7
	// DefaultSummaryWindowDays and MaxSummaryWindowDays bound the period over
	// which the conversion rate is measured.
	DefaultSummaryWindowDays = 90
	MaxSummaryWindowDays     = 365
)

// ExpiredQuotation is one quotation moved to EXPIRED by an expiry run.
type ExpiredQuotation struct {
	ID              string `json:"id"`
	QuotationNumber string `json:"quotationNumber"`
	LegalEntityID   string `json:"legalEntityId"`
	CustomerID      string `json:"customerId"`
	ValidUntil      string `json:"validUntil"`
	PreviousStatus  string `json:"previousStatus"`
	Version         int    `json:"version"`
}

// ExpireResult is the outcome of one expiry run. More is true when the batch
// limit stopped the run and further quotations are still lapsed.
type ExpireResult struct {
	Expired []ExpiredQuotation `json:"expired"`
	Count   int                `json:"count"`
	More    bool               `json:"more"`
}

// ExpireQuotations moves DRAFT and SENT quotations whose validity ended before
// day today to EXPIRED (FR-27.1, BR-27.5), oldest validity first, up to
// ExpireBatchSize. Each quotation is locked, its version bumped and
// quotation.expired written in the same transaction. A partly converted
// quotation is CONVERTED and is never touched. Running it again expires
// nothing that is already EXPIRED, so a retry is safe. An empty legalEntityID
// covers every legal entity in the caller's scope.
func (s *Postgres) ExpireQuotations(ctx context.Context, legalEntityID string, today time.Time, actor string) (ExpireResult, error) {
	p, _ := tenant.FromContext(ctx)
	out := ExpireResult{Expired: []ExpiredQuotation{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		type lapsed struct {
			ExpiredQuotation
			probability int
			net, ccy    string
		}
		rows, err := tx.Query(ctx, `select id::text, quotation_number, legal_entity_id::text, customer_id::text, valid_until::text, status,
				version, probability_pct, net_total::text, currency::text
			from sales_quotation
			where tenant_id = $1 and status in ('DRAFT', 'SENT') and valid_until < $2::date and ($3 = '' or legal_entity_id::text = $3)
			order by valid_until, id limit $4 for update`, p.TenantID, domain.DayOf(today).Format(dayLayout), legalEntityID, ExpireBatchSize+1)
		if err != nil {
			return err
		}
		var batch []lapsed
		for rows.Next() {
			var l lapsed
			if err := rows.Scan(&l.ID, &l.QuotationNumber, &l.LegalEntityID, &l.CustomerID, &l.ValidUntil, &l.PreviousStatus,
				&l.Version, &l.probability, &l.net, &l.ccy); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) > ExpireBatchSize {
			out.More = true
			batch = batch[:ExpireBatchSize]
		}
		for _, l := range batch {
			if _, err := tx.Exec(ctx, `update sales_quotation set status = 'EXPIRED', version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, l.ID); err != nil {
				return err
			}
			l.Version++
			if err := enqueue(ctx, tx, "quotation", l.ID, salesevents.QuotationExpired, p.TenantID, salesevents.QuotationExpiredData{
				QuotationID: l.ID, QuotationNumber: l.QuotationNumber, LegalEntityID: l.LegalEntityID, CustomerID: l.CustomerID,
				ValidUntil: l.ValidUntil, PreviousStatus: l.PreviousStatus, ProbabilityPct: l.probability, NetTotal: l.net,
				Currency: l.ccy, Actor: actor, Version: l.Version}); err != nil {
				return err
			}
			out.Expired = append(out.Expired, l.ExpiredQuotation)
		}
		out.Count = len(out.Expired)
		return nil
	})
	return out, err
}

// SummaryFilter selects the legal entity (empty: every one in the caller's
// scope), the day the counts are taken on and the days over which the
// conversion rate is measured. Status, MinProbabilityPct and
// ExpiringWithinDays are the pipeline KPI filters (PRD 5.27.14: status,
// expiry window, probability); they narrow the pipeline only, never the tiles
// or the conversion rate. Status is DRAFT, SENT or empty (both);
// ExpiringWithinDays 0 means any validity.
type SummaryFilter struct {
	LegalEntityID      string
	Today              time.Time
	WindowDays         int
	Status             string
	MinProbabilityPct  int
	ExpiringWithinDays int
}

// PipelineCurrency is the open quotation pipeline in one currency. Amounts of
// different currencies are never added together.
type PipelineCurrency struct {
	Currency    string          `json:"currency"`
	OpenCount   int             `json:"openCount"`
	OpenNet     decimal.Decimal `json:"openNet"`
	WeightedNet decimal.Decimal `json:"weightedNet"`
}

// Summary feeds the sales workspace: the tiles (active holds, backorder
// lines, quotations about to expire) and the quotation pipeline KPIs
// (PRD 5.27.14).
type Summary struct {
	ActiveHolds    int `json:"activeHolds"`
	BackorderLines int `json:"backorderLines"`
	// QuotationsExpiring counts open quotations whose last valid day falls in
	// the ExpiringSoonDays days starting today; QuotationsLapsed counts open
	// quotations already past their validity that no expiry run has closed.
	QuotationsExpiring int                `json:"quotationsExpiring"`
	QuotationsLapsed   int                `json:"quotationsLapsed"`
	Pipeline           []PipelineCurrency `json:"pipeline"`
	WindowDays         int                `json:"windowDays"`
	Won                int                `json:"won"`
	Lost               int                `json:"lost"`
	Expired            int                `json:"expired"`
	// ConversionRatePct is won / (won + lost + expired) over the window, in
	// percent at one place; nil when nothing closed in the window.
	ConversionRatePct *decimal.Decimal `json:"conversionRatePct,omitempty"`
}

// Summary reads the workspace counts in one transaction. Won, lost and expired
// are the quotations that reached that status in the window (by last update).
func (s *Postgres) Summary(ctx context.Context, f SummaryFilter) (Summary, error) {
	p, _ := tenant.FromContext(ctx)
	today := domain.DayOf(f.Today).Format(dayLayout)
	out := Summary{Pipeline: []PipelineCurrency{}, WindowDays: f.WindowDays}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select count(*) from order_hold where tenant_id = $1 and status = $2 and ($3 = '' or legal_entity_id::text = $3)`,
			p.TenantID, domain.HoldActive, f.LegalEntityID).Scan(&out.ActiveHolds); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) from order_schedule_line sl
			join sales_order_line l on l.tenant_id = sl.tenant_id and l.id = sl.line_id
			join sales_order o on o.tenant_id = sl.tenant_id and o.id = sl.order_id
			where sl.tenant_id = $1 and sl.status in ('BACKORDER', 'UNPROMISED') and o.status = 'CONFIRMED' and l.line_status <> 'CANCELLED'
				and ($2 = '' or sl.legal_entity_id::text = $2)`, p.TenantID, f.LegalEntityID).Scan(&out.BackorderLines); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select
				count(*) filter (where valid_until >= $3::date and valid_until < $3::date + $4::int),
				count(*) filter (where valid_until < $3::date)
			from sales_quotation where tenant_id = $1 and status in ('DRAFT', 'SENT') and ($2 = '' or legal_entity_id::text = $2)`,
			p.TenantID, f.LegalEntityID, today, ExpiringSoonDays).Scan(&out.QuotationsExpiring, &out.QuotationsLapsed); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select currency::text, count(*), coalesce(sum(net_total), 0)::text,
				coalesce(sum(net_total * probability_pct / 100), 0)::text
			from sales_quotation
			where tenant_id = $1 and status in ('DRAFT', 'SENT') and valid_until >= $3::date and ($2 = '' or legal_entity_id::text = $2)
				and ($4 = '' or status = $4) and probability_pct >= $5 and ($6 = 0 or valid_until < $3::date + $6::int)
			group by currency order by currency`, p.TenantID, f.LegalEntityID, today, f.Status, f.MinProbabilityPct, f.ExpiringWithinDays)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c PipelineCurrency
			var open, weighted string
			if err := rows.Scan(&c.Currency, &c.OpenCount, &open, &weighted); err != nil {
				rows.Close()
				return err
			}
			c.OpenNet = decimal.RequireFromString(open)
			c.WeightedNet = decimal.RequireFromString(weighted).Round(4)
			out.Pipeline = append(out.Pipeline, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select
				count(*) filter (where status = 'CONVERTED'), count(*) filter (where status = 'LOST'), count(*) filter (where status = 'EXPIRED')
			from sales_quotation
			where tenant_id = $1 and status in ('CONVERTED', 'LOST', 'EXPIRED') and updated_at >= $3::date - $4::int and ($2 = '' or legal_entity_id::text = $2)`,
			p.TenantID, f.LegalEntityID, today, f.WindowDays).Scan(&out.Won, &out.Lost, &out.Expired)
	})
	if err != nil {
		return Summary{}, err
	}
	if closed := out.Won + out.Lost + out.Expired; closed > 0 {
		rate := decimal.NewFromInt(int64(out.Won)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(closed))).Round(1)
		out.ConversionRatePct = &rate
	}
	return out, nil
}
