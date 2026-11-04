package store

import (
	"context"
	"errors"
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

// Codes raised by lease modifications and count sessions.
const (
	CodeCountSessionNotFound = "AST-0026"
	CodeLeasePeriodPosted    = "AST-0027"
)

// expiringSoonDays is the insurance-expiry alert window (FR-37.15).
const expiringSoonDays = 30

// LeaseModificationInput is a validated remeasurement request (FR-37.10,
// BR-37.8). Version is the lease version the caller read (If-Match).
// EffectivePeriod is the first schedule period under the revised terms.
// PaymentAmount is rounded to the lease currency's minor unit. TermMonths is
// the full revised term (0 keeps the term in force). IBRRate is
// the revised rate of a scope change; without a scope change the lease keeps
// its rate.
type LeaseModificationInput struct {
	Version         int              `json:"version"`
	EffectivePeriod int              `json:"effectivePeriod"`
	PaymentAmount   decimal.Decimal  `json:"paymentAmount"`
	TermMonths      int              `json:"termMonths"`
	ScopeChange     bool             `json:"scopeChange"`
	IBRRate         *decimal.Decimal `json:"ibrRate"`
}

// LeaseModification is one recorded remeasurement. LiabilityBefore is the
// carrying liability at the start of the effective period, LiabilityAfter the
// present value of the remaining payments; the ROU moves from ROUBefore to
// ROUAfter and ProfitLossExcess is what went to P&L because the ROU reached 0.
type LeaseModification struct {
	ID               string          `json:"id"`
	LeaseID          string          `json:"leaseId"`
	EffectivePeriod  int             `json:"effectivePeriod"`
	TermMonths       int             `json:"termMonths"`
	PaymentAmount    decimal.Decimal `json:"paymentAmount"`
	IBRRate          decimal.Decimal `json:"ibrRate"`
	ScopeChange      bool            `json:"scopeChange"`
	LiabilityBefore  decimal.Decimal `json:"liabilityBefore"`
	LiabilityAfter   decimal.Decimal `json:"liabilityAfter"`
	Adjustment       decimal.Decimal `json:"adjustment"`
	ROUBefore        decimal.Decimal `json:"rouBefore"`
	ROUAfter         decimal.Decimal `json:"rouAfter"`
	ROUAdjustment    decimal.Decimal `json:"rouAdjustment"`
	ProfitLossExcess decimal.Decimal `json:"profitLossExcess"`
	CreatedAt        time.Time       `json:"createdAt"`
	// Superseded marks a modification that a later one, taking effect in an earlier period, replaced: its schedule and adjustments no longer apply.
	Superseded bool `json:"superseded,omitempty"`
}

// LeaseModificationResult is the recorded modification, the lease after it
// (with the replaced schedule) and the AST-0516 info message.
type LeaseModificationResult struct {
	Modification LeaseModification `json:"modification"`
	Lease        Lease             `json:"lease"`
	Messages     []string          `json:"messages"`
}

const modificationColumns = `id, lease_id, effective_period, term_months, payment_amount, ibr_rate, scope_change, liability_before, liability_after,
	adjustment, rou_before, rou_after, rou_adjustment, profit_loss_excess, created_at`

func scanModification(row pgx.Row) (LeaseModification, error) {
	var m LeaseModification
	err := row.Scan(&m.ID, &m.LeaseID, &m.EffectivePeriod, &m.TermMonths, &m.PaymentAmount, &m.IBRRate, &m.ScopeChange, &m.LiabilityBefore,
		&m.LiabilityAfter, &m.Adjustment, &m.ROUBefore, &m.ROUAfter, &m.ROUAdjustment, &m.ProfitLossExcess, &m.CreatedAt)
	return m, err
}

// loadLease reads a lease with its schedule and modifications.
func loadLease(ctx context.Context, tx pgx.Tx, tenantID, id string, forUpdate bool) (Lease, error) {
	lock := ""
	if forUpdate {
		lock = " for update"
	}
	l, err := scanLease(tx.QueryRow(ctx, `select `+leaseColumns+` from lease where tenant_id = $1 and id = $2`+lock, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, notFound(CodeLeaseNotFound, "no lease with id "+id)
	}
	if err != nil {
		return Lease{}, err
	}
	rows, err := tx.Query(ctx, `select period_no, opening_liability, remeasurement, interest, payment, closing_liability, rou_depreciation
		from lease_schedule_line where tenant_id = $1 and lease_id = $2 order by period_no`, tenantID, id)
	if err != nil {
		return Lease{}, err
	}
	l.Schedule = []LeaseLine{}
	for rows.Next() {
		var line LeaseLine
		if err := rows.Scan(&line.PeriodNo, &line.Opening, &line.Remeasurement, &line.Interest, &line.Payment, &line.Closing, &line.ROUDepreciation); err != nil {
			rows.Close()
			return Lease{}, err
		}
		l.Schedule = append(l.Schedule, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Lease{}, err
	}
	mods, err := tx.Query(ctx, `select `+modificationColumns+` from lease_modification where tenant_id = $1 and lease_id = $2
		order by created_at, id`, tenantID, id)
	if err != nil {
		return Lease{}, err
	}
	defer mods.Close()
	l.Modifications = []LeaseModification{}
	for mods.Next() {
		m, err := scanModification(mods)
		if err != nil {
			return Lease{}, err
		}
		l.Modifications = append(l.Modifications, m)
	}
	if err := mods.Err(); err != nil {
		return Lease{}, err
	}
	markSuperseded(l.Modifications)
	return l, nil
}

// markSuperseded flags every modification (in the order they were recorded) that a later one replaced by taking effect in an earlier
// period: the later one rewrote the schedule from its own period, so the earlier one's adjustments are no longer in it.
func markSuperseded(mods []LeaseModification) {
	for i := range mods {
		for _, later := range mods[i+1:] {
			if later.EffectivePeriod < mods[i].EffectivePeriod {
				mods[i].Superseded = true
				break
			}
		}
	}
}

// insertLeaseLines writes schedule lines of a lease.
func insertLeaseLines(ctx context.Context, tx pgx.Tx, tenantID string, lease Lease, lines []LeaseLine) error {
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `insert into lease_schedule_line (tenant_id, lease_id, legal_entity_id, period_no, opening_liability, remeasurement,
			interest, payment, closing_liability, rou_depreciation) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			tenantID, lease.ID, lease.LegalEntityID, l.PeriodNo, l.Opening, l.Remeasurement, l.Interest, l.Payment, l.Closing, l.ROUDepreciation); err != nil {
			return err
		}
	}
	return nil
}

// ModifyLease remeasures an Active or Modified lease from the effective period
// on (BR-37.8): the schedule lines from that period are replaced, earlier
// lines are kept, the ROU carrying amount moves by the liability adjustment
// (any shortfall below zero goes to P&L), and the lease.remeasured event is
// written to the outbox in the same transaction.
func (s *Postgres) ModifyLease(ctx context.Context, leaseID string, in LeaseModificationInput, idempotencyKey string) (LeaseModificationResult, error) {
	p := principal(ctx)
	var out LeaseModificationResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			LeaseID string
			Input   LeaseModificationInput
		}{leaseID, in}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "lease.modify", request, func() (LeaseModificationResult, error) {
			lease, err := loadLease(ctx, tx, p.TenantID, leaseID, true)
			if err != nil {
				return LeaseModificationResult{}, err
			}
			if lease.Version != in.Version {
				return LeaseModificationResult{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					fmt.Sprintf("the lease changed since you read it (version %d, current %d); reload and try again", in.Version, lease.Version))
			}
			if err := domain.CanModify(lease.Status); err != nil {
				return LeaseModificationResult{}, err
			}
			if in.EffectivePeriod < 1 || in.EffectivePeriod > len(lease.Schedule) {
				return LeaseModificationResult{}, &domain.Error{Code: domain.CodeInvalidLease,
					Detail: fmt.Sprintf("effective period must be 1 to %d, a period of the schedule in force", len(lease.Schedule))}
			}
			var posted bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from lease_schedule_line where tenant_id = $1 and lease_id = $2
				and period_no >= $3 and posted_journal_id is not null)`, p.TenantID, leaseID, in.EffectivePeriod).Scan(&posted); err != nil {
				return LeaseModificationResult{}, err
			}
			if posted {
				return LeaseModificationResult{}, &domain.Error{Code: CodeLeasePeriodPosted,
					Detail: fmt.Sprintf("a period from %d on is already posted; modify from the first unposted period", in.EffectivePeriod)}
			}
			var inFlight bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from asset_pending_booking where tenant_id = $1 and book_code = 'LEASE' and document_id = $2)`,
				p.TenantID, leaseID).Scan(&inFlight); err != nil {
				return LeaseModificationResult{}, err
			}
			if inFlight {
				return LeaseModificationResult{}, &domain.Error{Code: CodeLeasePeriodPosted,
					Detail: "a ledger booking of this lease is awaiting the ledger's answer; post the lease again to settle it, then modify"}
			}
			minor := money.MinorUnits(lease.Currency)
			payment := in.PaymentAmount.Round(minor)
			paid := in.EffectivePeriod - 1
			line := lease.Schedule[paid]
			// The ROU still to depreciate from the effective period on, less the ROU adjustments of modifications that take effect after it:
			// this modification replaces the schedule from its period, so those are superseded and their adjustments are not carried.
			carrying := decimal.Zero
			for _, l := range lease.Schedule[paid:] {
				carrying = carrying.Add(l.ROUDepreciation)
			}
			for _, m := range lease.Modifications {
				if !m.Superseded && m.EffectivePeriod > in.EffectivePeriod {
					carrying = carrying.Sub(m.ROUAdjustment)
				}
			}
			rate, term := lease.IBRRate, lease.TermMonths
			if in.ScopeChange && in.IBRRate != nil {
				rate = *in.IBRRate
			}
			if in.TermMonths > 0 {
				term = in.TermMonths
			}
			current := line.Opening.Add(line.Remeasurement)
			rm, err := domain.Remeasure(domain.LeaseTerms{Payment: payment, PaymentsPerYear: lease.PaymentsPerYear, TermMonths: term,
				Advance: lease.PaymentTiming == "advance", AnnualRate: rate, Currency: lease.Currency, RateMethod: lease.RateMethod},
				paid, line.Opening, current, carrying)
			if err != nil {
				return LeaseModificationResult{}, err
			}
			if _, err := tx.Exec(ctx, `delete from lease_schedule_line where tenant_id = $1 and lease_id = $2 and period_no >= $3`,
				p.TenantID, leaseID, in.EffectivePeriod); err != nil {
				return LeaseModificationResult{}, err
			}
			if err := insertLeaseLines(ctx, tx, p.TenantID, lease, toStoreLines(rm.Lines)); err != nil {
				return LeaseModificationResult{}, err
			}
			if _, err := tx.Exec(ctx, `update lease set status = $3, payment_amount = $4, term_months = $5, ibr_rate = $6, version = version + 1
				where tenant_id = $1 and id = $2`, p.TenantID, leaseID, domain.LeaseModified, payment, term, rate); err != nil {
				return LeaseModificationResult{}, err
			}
			mod, err := scanModification(tx.QueryRow(ctx, `insert into lease_modification (tenant_id, id, lease_id, legal_entity_id, effective_period,
				term_months, payment_amount, ibr_rate, scope_change, liability_before, liability_after, adjustment, rou_before, rou_after, rou_adjustment,
				profit_loss_excess) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) returning `+modificationColumns,
				p.TenantID, ids.New(), leaseID, lease.LegalEntityID, in.EffectivePeriod, term, payment, rate, in.ScopeChange, current,
				rm.NewLiability, rm.Adjustment, carrying, rm.ROUAsset, rm.ROUAdjustment, rm.ProfitLossExcess))
			if err != nil {
				return LeaseModificationResult{}, err
			}
			if err := enqueue(ctx, tx, "lease", leaseID, assetevents.LeaseRemeasured, p.TenantID, assetevents.LeaseRemeasuredData{
				LeaseID: leaseID, ModificationID: mod.ID, EffectivePeriod: mod.EffectivePeriod, TermMonths: term, Rate: rate.String(),
				LiabilityBefore: current.String(), LiabilityAfter: rm.NewLiability.String(), Adjustment: rm.Adjustment.String(),
				ROUAdjustment: rm.ROUAdjustment.String(), ProfitLossExcess: rm.ProfitLossExcess.String(), Currency: lease.Currency,
			}); err != nil {
				return LeaseModificationResult{}, err
			}
			after, err := loadLease(ctx, tx, p.TenantID, leaseID, false)
			if err != nil {
				return LeaseModificationResult{}, err
			}
			msg := fmt.Sprintf("%s: lease remeasured; ROU adjusted by %s %s", domain.CodeLeaseRemeasured, rm.ROUAdjustment.StringFixed(minor), lease.Currency)
			if rm.ProfitLossExcess.IsPositive() {
				msg += fmt.Sprintf("; %s %s recognized in profit or loss", rm.ProfitLossExcess.StringFixed(minor), lease.Currency)
			}
			return LeaseModificationResult{Modification: mod, Lease: after, Messages: []string{msg}}, nil
		})
		return err
	})
	return out, err
}

func toStoreLines(in []domain.LeaseLine) []LeaseLine {
	out := make([]LeaseLine, 0, len(in))
	for _, l := range in {
		out = append(out, LeaseLine{PeriodNo: l.PeriodNo, Opening: l.Opening, Remeasurement: l.Remeasurement, Interest: l.Interest, Payment: l.Payment,
			Closing: l.Closing, ROUDepreciation: l.ROUDepreciation})
	}
	return out
}

// CountSessionInput is a validated physical inventory scan (FR-37.11).
// Barcodes are the scans in order, trimmed; duplicates are allowed.
type CountSessionInput struct {
	LegalEntityID string   `json:"legalEntityId"`
	Location      string   `json:"location"`
	CountedDate   Date     `json:"countedDate"`
	Barcodes      []string `json:"barcodes"`
}

// CountLine is one distinct barcode of a count session. Found and Missing
// lines carry the asset; an Unknown scan has none.
type CountLine struct {
	Barcode     string  `json:"barcode"`
	Outcome     string  `json:"outcome"`
	AssetID     *string `json:"assetId"`
	AssetNumber *string `json:"assetNumber"`
	AssetName   *string `json:"assetName"`
}

// CountSession is a physical inventory session: the scanned barcodes
// reconciled against the Open assets of one legal entity that carry a
// barcode. Lines and Warnings (AST-0525 per unknown barcode) come with a
// single session, not with a list.
type CountSession struct {
	ID             string      `json:"id"`
	LegalEntityID  string      `json:"legalEntityId"`
	Location       string      `json:"location"`
	CountedDate    Date        `json:"countedDate"`
	ExpectedCount  int         `json:"expectedCount"`
	FoundCount     int         `json:"foundCount"`
	MissingCount   int         `json:"missingCount"`
	UnknownCount   int         `json:"unknownCount"`
	DuplicateCount int         `json:"duplicateCount"`
	CreatedAt      time.Time   `json:"createdAt"`
	Lines          []CountLine `json:"lines,omitempty"`
	Warnings       []string    `json:"warnings,omitempty"`
}

// CountFilter narrows ListCountSessions. After is the id of the last session
// of the previous page; Limit is 1..200.
type CountFilter struct {
	LegalEntityID string
	After         string
	Limit         int
}

// CountPage is one page of count sessions, newest first, without lines.
type CountPage struct {
	Items      []CountSession `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

const countColumns = `s.id, s.legal_entity_id, s.location, s.counted_date, s.expected_count, s.found_count, s.missing_count, s.unknown_count,
	s.duplicate_count, s.created_at`

func scanCount(row pgx.Row) (CountSession, error) {
	var c CountSession
	err := row.Scan(&c.ID, &c.LegalEntityID, &c.Location, &c.CountedDate, &c.ExpectedCount, &c.FoundCount, &c.MissingCount, &c.UnknownCount,
		&c.DuplicateCount, &c.CreatedAt)
	return c, err
}

// CreateCountSession reconciles the scans against the Open assets of the legal
// entity that carry a barcode and stores the session with one line per
// distinct barcode. It changes no asset: a counter cannot adjust the register
// (PRD 7.37.17).
func (s *Postgres) CreateCountSession(ctx context.Context, in CountSessionInput, idempotencyKey string) (CountSession, error) {
	p := principal(ctx)
	var out CountSession
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "asset.count-session.create", in, func() (CountSession, error) {
			rows, err := tx.Query(ctx, `select id, barcode from asset where tenant_id = $1 and legal_entity_id = $2 and status = $3
				and barcode is not null order by asset_number, id`, p.TenantID, in.LegalEntityID, string(domain.StatusOpen))
			if err != nil {
				return CountSession{}, err
			}
			expected := []domain.CountAsset{}
			for rows.Next() {
				var a domain.CountAsset
				if err := rows.Scan(&a.AssetID, &a.Barcode); err != nil {
					rows.Close()
					return CountSession{}, err
				}
				expected = append(expected, a)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return CountSession{}, err
			}
			res := domain.ReconcileCount(expected, in.Barcodes)
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into asset_count_session (tenant_id, id, legal_entity_id, location, counted_date, expected_count,
				found_count, missing_count, unknown_count, duplicate_count) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				p.TenantID, id, in.LegalEntityID, in.Location, in.CountedDate, len(expected), len(res.Found), len(res.Missing), len(res.Unknown),
				res.Duplicates); err != nil {
				return CountSession{}, err
			}
			var barcodes, outcomes []string
			var assetIDs []*string
			for _, group := range [][]domain.CountLine{res.Found, res.Missing, res.Unknown} {
				for _, l := range group {
					barcodes, outcomes = append(barcodes, l.Barcode), append(outcomes, l.Outcome)
					var asset *string
					if l.AssetID != "" {
						a := l.AssetID
						asset = &a
					}
					assetIDs = append(assetIDs, asset)
				}
			}
			if len(barcodes) > 0 {
				// One statement for the lines: COPY is not available on tables with row-level security.
				if _, err := tx.Exec(ctx, `insert into asset_count_line (tenant_id, session_id, legal_entity_id, barcode, outcome, asset_id)
					select $1, $2, $3, b, o, a from unnest($4::text[], $5::text[], $6::uuid[]) as l(b, o, a)`,
					p.TenantID, id, in.LegalEntityID, barcodes, outcomes, assetIDs); err != nil {
					return CountSession{}, err
				}
			}
			return loadCount(ctx, tx, p.TenantID, id)
		})
		return err
	})
	return out, err
}

// ListCountSessions returns one page of sessions, newest first, without lines.
func (s *Postgres) ListCountSessions(ctx context.Context, f CountFilter) (CountPage, error) {
	p := principal(ctx)
	page := CountPage{Items: []CountSession{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+countColumns+` from asset_count_session s
			where s.tenant_id = $1
			  and ($2::text = '' or s.legal_entity_id::text = $2)
			  and ($3::text = '' or (s.created_at, s.id) < (select c.created_at, c.id from asset_count_session c where c.tenant_id = $1 and c.id::text = $3))
			order by s.created_at desc, s.id desc limit $4`, p.TenantID, f.LegalEntityID, f.After, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCount(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, c)
		}
		return rows.Err()
	})
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, err
}

// GetCountSession reads a session with its lines (Missing, then Unknown, then
// Found, each by barcode) and an AST-0525 warning per unknown barcode.
func (s *Postgres) GetCountSession(ctx context.Context, id string) (CountSession, error) {
	p := principal(ctx)
	var out CountSession
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadCount(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

func loadCount(ctx context.Context, tx pgx.Tx, tenantID, id string) (CountSession, error) {
	c, err := scanCount(tx.QueryRow(ctx, `select `+countColumns+` from asset_count_session s where s.tenant_id = $1 and s.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return CountSession{}, notFound(CodeCountSessionNotFound, "no count session with id "+id)
	}
	if err != nil {
		return CountSession{}, err
	}
	rows, err := tx.Query(ctx, `select l.barcode, l.outcome, l.asset_id, a.asset_number, a.name from asset_count_line l
		left join asset a on a.tenant_id = l.tenant_id and a.id = l.asset_id
		where l.tenant_id = $1 and l.session_id = $2
		order by case l.outcome when 'Missing' then 0 when 'Unknown' then 1 else 2 end, l.barcode`, tenantID, id)
	if err != nil {
		return CountSession{}, err
	}
	defer rows.Close()
	c.Lines, c.Warnings = []CountLine{}, []string{}
	for rows.Next() {
		var l CountLine
		if err := rows.Scan(&l.Barcode, &l.Outcome, &l.AssetID, &l.AssetNumber, &l.AssetName); err != nil {
			return CountSession{}, err
		}
		if l.Outcome == domain.CountUnknown {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s: barcode %s is not in the register", domain.CodeCountUnknown, l.Barcode))
		}
		c.Lines = append(c.Lines, l)
	}
	return c, rows.Err()
}

// InsuranceReportFilter narrows the insurance report (7.37.14). AsOf picks
// the policies in force on that date; ExpiringWithinDays, when set, keeps
// those whose cover ends within that many days of AsOf. After is the id of the
// last policy of the previous page; Limit is 1..200.
type InsuranceReportFilter struct {
	LegalEntityID      string
	AsOf               Date
	InsuranceType      string
	ExpiringWithinDays *int
	After              string
	Limit              int
}

// BookValue is the net book value of one Open book of an asset.
type BookValue struct {
	BookCode string          `json:"bookCode"`
	Currency string          `json:"currency"`
	NBV      decimal.Decimal `json:"nbv"`
}

// InsuranceReportRow is a policy in force with its asset, the NBV of every
// Open book, and whether cover ends within 30 days of the report date
// (insurance-expiry alert window). UnderInsured and Warnings follow the
// policy (AST-0531, AST-0532).
type InsuranceReportRow struct {
	InsurancePolicy
	AssetNumber  string      `json:"assetNumber"`
	AssetName    string      `json:"assetName"`
	AssetStatus  string      `json:"assetStatus"`
	Books        []BookValue `json:"books"`
	ExpiringSoon bool        `json:"expiringSoon"`
}

// InsuranceReport is one page of the report, ordered by asset number and
// policy number.
type InsuranceReport struct {
	AsOf       Date                 `json:"asOf"`
	Items      []InsuranceReportRow `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

// InsuranceReport lists the policies of a legal entity in force on the report
// date with insured value, indexed replacement value, NBV per book and the
// under-insured flag (FR-37.15, 7.37.14 insurance report).
func (s *Postgres) InsuranceReport(ctx context.Context, f InsuranceReportFilter) (InsuranceReport, error) {
	p := principal(ctx)
	out := InsuranceReport{AsOf: f.AsOf, Items: []InsuranceReportRow{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select p.id, p.asset_id, p.legal_entity_id, p.insurer_party_id, p.policy_number, p.insurance_type, p.coverage_from,
			p.coverage_to, p.premium_amount, p.currency, p.basis, p.base_value, p.sum_insured, p.index_series_code, p.index_base_year, p.valuation_year,
			p.index_year_used, p.indexed_value, p.created_at, a.asset_number, a.name, a.status
			from asset_insurance_policy p join asset a on a.tenant_id = p.tenant_id and a.id = p.asset_id
			where p.tenant_id = $1 and p.legal_entity_id = $2 and p.coverage_from <= $3 and p.coverage_to >= $3
			  and ($4::text = '' or p.insurance_type = $4)
			  and ($5::int is null or p.coverage_to <= $3::date + $5::int)
			  and ($6::text = '' or (a.asset_number, p.policy_number, p.id) > (select ca.asset_number, cp.policy_number, cp.id
			      from asset_insurance_policy cp join asset ca on ca.tenant_id = cp.tenant_id and ca.id = cp.asset_id
			      where cp.tenant_id = $1 and cp.id::text = $6))
			order by a.asset_number, p.policy_number, p.id limit $7`,
			p.TenantID, f.LegalEntityID, f.AsOf, f.InsuranceType, f.ExpiringWithinDays, f.After, f.Limit+1)
		if err != nil {
			return err
		}
		assetIDs := []string{}
		for rows.Next() {
			var r InsuranceReportRow
			pol := &r.InsurancePolicy
			if err := rows.Scan(&pol.ID, &pol.AssetID, &pol.LegalEntityID, &pol.InsurerPartyID, &pol.PolicyNumber, &pol.InsuranceType, &pol.CoverageFrom,
				&pol.CoverageTo, &pol.Premium, &pol.Currency, &pol.Basis, &pol.BaseValue, &pol.SumInsured, &pol.IndexSeriesCode, &pol.IndexBaseYear,
				&pol.ValuationYear, &pol.IndexYearUsed, &pol.IndexedValue, &pol.CreatedAt, &r.AssetNumber, &r.AssetName, &r.AssetStatus); err != nil {
				rows.Close()
				return err
			}
			pol.decorate()
			r.Books = []BookValue{}
			r.ExpiringSoon = !pol.CoverageTo.After(f.AsOf.AddDate(0, 0, expiringSoonDays))
			out.Items = append(out.Items, r)
			assetIDs = append(assetIDs, pol.AssetID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > f.Limit {
			out.Items = out.Items[:f.Limit]
			out.NextCursor = out.Items[f.Limit-1].ID
		}
		if len(assetIDs) == 0 {
			return nil
		}
		books, err := tx.Query(ctx, `select asset_id, book_code, currency, nbv from asset_book
			where tenant_id = $1 and asset_id = any($2::uuid[]) and status = $3 order by book_code`, p.TenantID, assetIDs, string(domain.StatusOpen))
		if err != nil {
			return err
		}
		defer books.Close()
		byAsset := map[string][]BookValue{}
		for books.Next() {
			var assetID string
			var b BookValue
			if err := books.Scan(&assetID, &b.BookCode, &b.Currency, &b.NBV); err != nil {
				return err
			}
			byAsset[assetID] = append(byAsset[assetID], b)
		}
		if err := books.Err(); err != nil {
			return err
		}
		for i := range out.Items {
			if b, ok := byAsset[out.Items[i].AssetID]; ok {
				out.Items[i].Books = b
			}
		}
		return nil
	})
	return out, err
}
