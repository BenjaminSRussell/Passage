package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/issueclaim"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/fulfillment/internal/domain"
)

const (
	CodeCostCurrency   = "FUL-0082"
	CodeSlipGLNotFound = "FUL-0083"
	CodeSlipGLState    = "FUL-0084"

	codeUnavailable   = "COM-0503"
	slipGLSourceType  = "fulfillment-packing-slip"
	slipGLActor       = "system:fulfillment-slip-gl"
	slipGLBatch       = 50
	slipGLLease       = 2 * time.Minute
	slipGLInterval    = 30 * time.Second
	slipGLBackoffBase = time.Minute
	slipGLBackoffMax  = time.Hour
	slipGLMaxError    = 500
	dateLayout        = "2006-01-02"
)

func (s *Postgres) WithLedger(l ledgerclient.Ledger, ledgerByLegalEntity map[string]string) *Postgres {
	s.ledger, s.ledgers = l, ledgerByLegalEntity
	return s
}

// valuedCosts prices the shipment lines still to ship from inventory valuation
// when the claimer can value (issueclaim.Valuer): the request's unit costs are
// then ignored. A line the warehouse holds no valued stock of (empty currency)
// keeps the request's cost, since no valuation exists for it; catch weights
// always come from the request.
func (s *Postgres) valuedCosts(ctx context.Context, sh Shipment, currency string, date time.Time, requested map[string]Cost) (map[string]Cost, error) {
	valuer, ok := s.claims.(issueclaim.Valuer)
	if !ok {
		return requested, nil
	}
	out := make(map[string]Cost, len(requested))
	for _, l := range sh.Lines {
		if !l.Packed.Sub(l.Shipped).IsPositive() {
			continue
		}
		valuedIn, unit, err := valuer.IssueCost(ctx, sh.LegalEntityID, l.ProductID, sh.WarehouseID)
		switch {
		case issueclaim.Refused(err):
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeQualityHold, "an item of this shipment "+issueclaim.Reason(err)+"; ship not allowed")
		case err != nil:
			slog.ErrorContext(ctx, "inventory valuation failed for a packing slip", "shipment", sh.ID, "shipmentLine", l.ID, "err", err, "correlationId", middleware.GetReqID(ctx))
			return nil, httpx.NewProblem(http.StatusServiceUnavailable, codeUnavailable, "inventory valuation failed")
		case valuedIn == "":
			if c, asked := requested[l.ID]; asked {
				out[l.ID] = c
			}
			continue
		case valuedIn != currency:
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCostCurrency,
				fmt.Sprintf("inventory values shipment line %s in %s, not %s", l.ID, valuedIn, currency))
		}
		asked, wasAsked := requested[l.ID]
		if wasAsked && !asked.UnitCost.Equal(unit) {
			slog.WarnContext(ctx, "packing slip unit cost from the request ignored: inventory prices the issue", "shipmentLine", l.ID,
				"requested", asked.UnitCost.String(), "valued", unit.String(), "correlationId", middleware.GetReqID(ctx))
		}
		out[l.ID] = Cost{UnitCost: unit, CostSnapshotID: "inventory:" + l.ProductID + ":" + date.Format(dateLayout), Weight: asked.Weight}
	}
	return out, nil
}

// queueSlipGL records, inside the slip's transaction, that the slip's journal is
// owed to the ledger. A slip with no journal (every line cost nothing) owes none.
func queueSlipGL(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, slip PackingSlip) error {
	if len(slip.Journal) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `insert into packing_slip_gl (tenant_id, slip_id, legal_entity_id) values ($1, $2, $3)`, tenantID, slip.ID, legalEntityID)
	return err
}

// slipEntry is the ledger entry of a slip: its journal line for line, a negative
// amount a credit, so a reversing slip (negated journal) posts the exact mirror
// under its own key.
func slipEntry(slip PackingSlip, legalEntityID, ledgerID string) ledgerclient.Entry {
	purpose := "issue"
	if slip.ReversalOfID != nil {
		purpose = "cancel"
	}
	lines := make([]ledgerclient.Line, 0, len(slip.Journal))
	for _, j := range slip.Journal {
		lines = append(lines, ledgerclient.Line{Account: j.Account, Credit: j.Amount.IsNegative(), Amount: j.Amount.Abs(), Text: slip.Number})
	}
	return ledgerclient.Entry{LegalEntityID: legalEntityID, LedgerID: ledgerID, DocumentID: slip.ID, PostingDate: slip.PostingDate.Format(dateLayout),
		Currency: slip.Currency, IdempotencyKey: ledgerclient.MustPostingKey("fulfillment", "packingslip", slip.ID, purpose), SourceType: slipGLSourceType, Lines: lines}
}

type claimedSlipGL struct {
	SlipID, LegalEntityID string
	Attempts              int
}

// SlipGLDueTenants lists the tenants with a slip journal due to post, across
// tenants (SECURITY DEFINER fulfillment_slip_gl_due_tenants()).
func (s *Postgres) SlipGLDueTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select t::text from fulfillment_slip_gl_due_tenants() t`)
	if err != nil {
		return nil, fmt.Errorf("fulfillment: due slip journal tenants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (s *Postgres) claimSlipGL(ctx context.Context) ([]claimedSlipGL, error) {
	var out []claimedSlipGL
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select slip_id as claimed_id from packing_slip_gl where status = 'PENDING' and not parked and next_attempt_at <= now()
				and not exists (select 1 from packing_slip s join packing_slip_gl o on o.tenant_id = s.tenant_id and o.slip_id = s.reversal_of_id
					where s.tenant_id = packing_slip_gl.tenant_id and s.id = packing_slip_gl.slip_id and o.status <> 'POSTED')
				order by next_attempt_at, slip_id limit $1 for update skip locked)
			update packing_slip_gl g set attempts = g.attempts + 1, next_attempt_at = now() + make_interval(secs => $2), updated_at = now()
			from claimed where g.slip_id = claimed.claimed_id
			returning g.slip_id::text, g.legal_entity_id::text, g.attempts`, slipGLBatch, slipGLLease.Seconds())
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (claimedSlipGL, error) {
			var c claimedSlipGL
			err := r.Scan(&c.SlipID, &c.LegalEntityID, &c.Attempts)
			return c, err
		})
		return err
	})
	return out, err
}

// PostSlipsToGL posts the slip journals that are due, tenant by tenant, and
// returns how many it posted. It is leased like the sales reporter, so replicas
// never post one slip at once, and a failing slip or tenant does not stop the rest.
func (s *Postgres) PostSlipsToGL(ctx context.Context) (int, error) {
	if s.ledger == nil {
		return 0, nil
	}
	tenants, err := s.SlipGLDueTenants(ctx)
	if err != nil {
		return 0, err
	}
	posted := 0
	var failed []error
	for _, tid := range tenants {
		tctx := SalesReportSystemContext(ctx, tid)
		claimed, err := s.claimSlipGL(tctx)
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
			continue
		}
		for _, c := range claimed {
			if err := s.postSlipGL(ctx, tctx, tid, c); err != nil {
				failed = append(failed, fmt.Errorf("tenant %s slip %s: %w", tid, c.SlipID, err))
				continue
			}
			posted++
		}
	}
	return posted, errors.Join(failed...)
}

// RunSlipGL calls PostSlipsToGL every slipGLInterval until ctx ends.
func (s *Postgres) RunSlipGL(ctx context.Context) {
	t := time.NewTicker(slipGLInterval)
	defer t.Stop()
	for {
		if _, err := s.PostSlipsToGL(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "fulfillment packing slip ledger poster run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Postgres) postSlipGL(ctx, tctx context.Context, tenantID string, c claimedSlipGL) error {
	ledgerID := s.ledgers[c.LegalEntityID]
	if ledgerID == "" {
		err := errors.New("no ledger is configured for the legal entity")
		s.recordSlipGLFailure(ctx, tctx, c, false, err)
		return err
	}
	var slip PackingSlip
	if err := dbx.WithTenantTx(tctx, s.pool, func(tx pgx.Tx) (err error) {
		slip, err = loadSlip(tctx, tx, c.SlipID)
		return err
	}); err != nil {
		s.recordSlipGLFailure(ctx, tctx, c, false, err)
		return err
	}
	pctx := tenant.With(ctx, tenant.Principal{TenantID: tenantID, LegalEntityID: c.LegalEntityID, UserID: slipGLActor})
	entryID, err := s.postEntry(pctx, slipEntry(slip, c.LegalEntityID, ledgerID))
	if err != nil {
		s.recordSlipGLFailure(ctx, tctx, c, ledgerclient.Rejected(err), err)
		return err
	}
	return dbx.WithTenantTx(tctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(tctx, `update packing_slip_gl set status = 'POSTED', journal_entry_id = $3, last_error = null, updated_at = now()
			where slip_id = $1 and attempts = $2 and status = 'PENDING'`, c.SlipID, c.Attempts, entryID)
		return err
	})
}

func (s *Postgres) postEntry(ctx context.Context, e ledgerclient.Entry) (string, error) {
	owner, err := s.ledger.LegalEntity(ctx, e.LegalEntityID, e.LedgerID)
	if err != nil {
		return "", err
	}
	if owner != e.LegalEntityID {
		return "", &ledgerclient.Error{Op: "ledger", Status: http.StatusUnprocessableEntity, Code: "ledger_legal_entity_mismatch"}
	}
	return s.ledger.Post(ctx, e)
}

func (s *Postgres) recordSlipGLFailure(ctx, tctx context.Context, c claimedSlipGL, parked bool, cause error) {
	slog.ErrorContext(ctx, "packing slip journal not posted to the ledger", "slip", c.SlipID, "legalEntity", c.LegalEntityID, "attempts", c.Attempts,
		"parked", parked, "err", cause, "correlationId", middleware.GetReqID(ctx))
	msg := cause.Error()
	if len(msg) > slipGLMaxError {
		msg = msg[:slipGLMaxError]
	}
	next := time.Now().Add(min(slipGLBackoffBase<<min(c.Attempts, 10), slipGLBackoffMax))
	detached := context.WithoutCancel(tctx)
	if err := dbx.WithTenantTx(detached, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(detached, `update packing_slip_gl set next_attempt_at = $3, parked = $4, last_error = $5, updated_at = now()
			where slip_id = $1 and attempts = $2 and status = 'PENDING'`, c.SlipID, c.Attempts, next, parked, msg)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "packing slip journal failure not recorded", "slip", c.SlipID, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

type SlipGL struct {
	SlipID        string    `json:"slipId"`
	LegalEntityID string    `json:"legalEntityId"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
	Parked        bool      `json:"parked"`
	LastError     string    `json:"lastError,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type SlipGLStore interface {
	ReleaseSlipGL(ctx context.Context, slipID string, idem Idem) (SlipGL, error)
}

const slipGLCols = `slip_id::text, legal_entity_id::text, status, attempts, next_attempt_at, parked, coalesce(last_error, ''), updated_at`

func scanSlipGL(row pgx.Row) (g SlipGL, err error) {
	err = row.Scan(&g.SlipID, &g.LegalEntityID, &g.Status, &g.Attempts, &g.NextAttemptAt, &g.Parked, &g.LastError, &g.UpdatedAt)
	return g, err
}

// ReleaseSlipGL puts a parked slip journal back in the poster's queue, due now
// and with its attempts counted from zero. Only a PENDING parked row is released
// (FUL-0084); a slip with no journal row is FUL-0083.
func (s *Postgres) ReleaseSlipGL(ctx context.Context, slipID string, idem Idem) (SlipGL, error) {
	p, _ := tenant.FromContext(ctx)
	var out SlipGL
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (SlipGL, error) {
			g, err := scanSlipGL(tx.QueryRow(ctx, `select `+slipGLCols+` from packing_slip_gl where slip_id = $1 for update`, slipID))
			if errors.Is(err, pgx.ErrNoRows) {
				return g, httpx.NewProblem(http.StatusNotFound, CodeSlipGLNotFound, "no packing slip journal found for this slip")
			}
			if err != nil {
				return g, err
			}
			if g.Status != "PENDING" || !g.Parked {
				return g, httpx.NewProblem(http.StatusConflict, CodeSlipGLState, "only a parked packing slip journal is released")
			}
			return scanSlipGL(tx.QueryRow(ctx, `update packing_slip_gl set parked = false, attempts = 0, next_attempt_at = now(), updated_at = now()
				where slip_id = $1 returning `+slipGLCols, slipID))
		})
		out = res
		return err
	})
	return out, err
}

// WriteSlipGLMetrics writes the process-wide counts of open and parked slip
// journals as Prometheus gauges; a failed read writes nothing rather than a zero.
func (s *Postgres) WriteSlipGLMetrics(w io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var pending, parked int64
	if err := s.pool.QueryRow(ctx, `select pending_rows, parked_rows from fulfillment_slip_gl_counts()`).Scan(&pending, &parked); err != nil {
		slog.ErrorContext(ctx, "packing slip journal gauges not read", "err", err)
		return
	}
	_, _ = fmt.Fprintf(w, "# TYPE depot_fulfillment_slip_gl_pending gauge\ndepot_fulfillment_slip_gl_pending %d\n# TYPE depot_fulfillment_slip_gl_parked gauge\ndepot_fulfillment_slip_gl_parked %d\n", pending, parked)
}

// DNIReconciliation compares the delivered-not-invoiced report with the
// ledger's account 1405 (FR-28.12).
type DNIReconciliation struct {
	LegalEntityID string          `json:"legalEntityId"`
	AsOf          string          `json:"asOf"`
	Currency      string          `json:"currency"`
	ReportNet     decimal.Decimal `json:"reportNet"`
	LedgerNet     decimal.Decimal `json:"ledgerNet"`
	Difference    decimal.Decimal `json:"difference"`
	PendingSlips  int             `json:"pendingSlips"`
	ParkedSlips   int             `json:"parkedSlips"`
	Reconciled    bool            `json:"reconciled"`
}

// ReconcileDNI sums the DNI report of the legal entity in currency and the
// ledger's 1405 balance, both cut at the end of asOf's month (the ledger reads
// whole periods). It is reconciled only when the
// two are equal and no slip journal is still waiting or parked, as the ledger
// would otherwise lack what the report already counts.
func (s *Postgres) ReconcileDNI(ctx context.Context, legalEntityID, currency string, asOf time.Time) (DNIReconciliation, error) {
	ledgerID := s.ledgers[legalEntityID]
	if s.ledger == nil || ledgerID == "" {
		return DNIReconciliation{}, httpx.NewProblem(http.StatusServiceUnavailable, codeUnavailable, "the ledger or the legal entity's ledger is not configured")
	}
	cutoff := time.Date(asOf.Year(), asOf.Month()+1, 0, 0, 0, 0, 0, time.UTC)
	out := DNIReconciliation{LegalEntityID: legalEntityID, AsOf: cutoff.Format(dateLayout), Currency: currency}
	rows, err := s.DeliveredNotInvoiced(ctx, DNIFilter{LegalEntityID: legalEntityID, AsOf: cutoff})
	if err != nil {
		return DNIReconciliation{}, err
	}
	for _, r := range rows {
		if r.Currency == currency {
			out.ReportNet = out.ReportNet.Add(r.Amount)
		}
	}
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) filter (where not parked), count(*) filter (where parked) from packing_slip_gl
			where legal_entity_id = $1 and status = 'PENDING'`, legalEntityID).Scan(&out.PendingSlips, &out.ParkedSlips)
	}); err != nil {
		return DNIReconciliation{}, err
	}
	bal, err := s.ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: legalEntityID, LedgerID: ledgerID, Account: domain.AccountDeliveredNotInvoiced, PeriodTo: cutoff.Format("2006-01")})
	if err != nil {
		slog.ErrorContext(ctx, "ledger 1405 balance not read", "legalEntity", legalEntityID, "err", err, "correlationId", middleware.GetReqID(ctx))
		return DNIReconciliation{}, httpx.NewProblem(http.StatusServiceUnavailable, codeUnavailable, "the ledger balance could not be read; retry later")
	}
	if bal.Currency != "" && bal.Currency != currency {
		return DNIReconciliation{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCostCurrency, "the ledger is kept in "+bal.Currency+", not "+currency)
	}
	out.LedgerNet = bal.Net()
	out.Difference = out.ReportNet.Sub(out.LedgerNet)
	out.Reconciled = out.Difference.IsZero() && out.PendingSlips == 0 && out.ParkedSlips == 0
	return out, nil
}
