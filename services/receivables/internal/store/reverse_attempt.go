package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	attemptPending      = "PENDING"
	attemptPosted       = "POSTED"
	attemptCommitted    = "COMMITTED"
	attemptCompensating = "COMPENSATING"
	attemptCompensated  = "COMPENSATED"
	attemptAmbiguous    = "AMBIGUOUS"

	reversalAttemptSettleAfter = 2 * time.Minute
	reversalAttemptBatch       = 50

	legMain = "main"
	legFX   = "fx"

	attemptTable   = `payment_application_reversal_attempt`
	attemptColumns = `application_id::text, legal_entity_id::text, payment_id::text, posting_date::text, generation, state, main_key, main_currency, main_lines,
		coalesce(main_entry_id, ''), fx_key, fx_currency, fx_lines, coalesce(fx_entry_id, ''), probe_legs, txid`
)

type attemptLeg struct {
	name, key, currency, entryID string
	lines                        []journalLine
}

type reversalAttempt struct {
	applicationID, legalEntityID, paymentID, postingDate, state string
	generation                                                  int
	legs                                                        []*attemptLeg
	probe                                                       []string
	txid                                                        int64
}

func (a *reversalAttempt) leg(name string) *attemptLeg {
	for _, l := range a.legs {
		if l.name == name {
			return l
		}
	}
	return nil
}

func (a *reversalAttempt) entryIDs() (main, fx string) {
	main = a.legs[0].entryID
	if l := a.leg(legFX); l != nil {
		fx = l.entryID
	}
	return main, fx
}

func newReversalAttempt(pay Payment, app appReversal, u unapplied, generation int, txid int64) *reversalAttempt {
	positive := func(lines []journalLine) []journalLine {
		return slices.DeleteFunc(slices.Clone(lines), func(l journalLine) bool { return !l.Amount.IsPositive() })
	}
	gen := strconv.Itoa(generation)
	a := &reversalAttempt{applicationID: app.id, legalEntityID: pay.LegalEntityID, paymentID: pay.ID, postingDate: app.postingDate, state: attemptPending,
		generation: generation, txid: txid,
		legs: []*attemptLeg{{name: legMain, key: ledgerclient.MustPostingKey("receivables", "paymentapplication", app.id, "reversal", gen), currency: pay.Currency, lines: positive(u.main)}}}
	if u.fxLines != nil {
		a.legs = append(a.legs, &attemptLeg{name: legFX, key: ledgerclient.MustPostingKey("receivables", "paymentapplication", app.id, "reversal-fx", gen), currency: u.fxCurrency, lines: positive(u.fxLines)})
	}
	return a
}

func scanAttempt(row rowScanner) (*reversalAttempt, error) {
	var a reversalAttempt
	var mainKey, mainCurrency, mainEntry, fxEntry string
	var mainLines, fxLines []byte
	var fxKey, fxCurrency *string
	if err := row.Scan(&a.applicationID, &a.legalEntityID, &a.paymentID, &a.postingDate, &a.generation, &a.state, &mainKey, &mainCurrency, &mainLines,
		&mainEntry, &fxKey, &fxCurrency, &fxLines, &fxEntry, &a.probe, &a.txid); err != nil {
		return nil, err
	}
	main := &attemptLeg{name: legMain, key: mainKey, currency: mainCurrency, entryID: mainEntry}
	if err := json.Unmarshal(mainLines, &main.lines); err != nil {
		return nil, err
	}
	a.legs = []*attemptLeg{main}
	if fxKey != nil && fxCurrency != nil {
		fx := &attemptLeg{name: legFX, key: *fxKey, currency: *fxCurrency, entryID: fxEntry}
		if err := json.Unmarshal(fxLines, &fx.lines); err != nil {
			return nil, err
		}
		a.legs = append(a.legs, fx)
	}
	return &a, nil
}

func (s *Postgres) openReversalAttempt(ctx context.Context, p tenant.Principal, pay Payment, app appReversal, u unapplied, txid int64) (*reversalAttempt, error) {
	var att *reversalAttempt
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		prev, err := scanAttempt(tx.QueryRow(ctx, `select `+attemptColumns+` from `+attemptTable+`
			where tenant_id = $1 and application_id = $2 order by generation desc limit 1 for update`, p.TenantID, app.id))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch {
		case prev != nil && prev.state == attemptCompensating:
			return httpx.NewProblem(http.StatusServiceUnavailable, CodeReversalLedgerUnavailable, "an earlier attempt of this reversal is being resolved; retry with the same Idempotency-Key")
		case prev != nil && prev.state == attemptCommitted:
			return httpx.NewProblem(http.StatusConflict, CodePaymentState, "the application is already reversed")
		case prev != nil && prev.state != attemptCompensated:
			prev.state, prev.probe, prev.txid = attemptPending, nil, txid
			att = prev
			_, err = tx.Exec(ctx, `update `+attemptTable+` set state = $4, probe_legs = '{}', txid = $5, updated_at = now()
				where tenant_id = $1 and application_id = $2 and generation = $3`, p.TenantID, app.id, prev.generation, attemptPending, txid)
			return err
		}
		generation := 0
		if prev != nil {
			generation = prev.generation + 1
		}
		att = newReversalAttempt(pay, app, u, generation, txid)
		main := att.legs[0]
		mainLines, err := json.Marshal(main.lines)
		if err != nil {
			return err
		}
		var fxKey, fxCurrency *string
		var fxLines []byte
		if fx := att.leg(legFX); fx != nil {
			fxKey, fxCurrency = &fx.key, &fx.currency
			if fxLines, err = json.Marshal(fx.lines); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `insert into `+attemptTable+` (tenant_id, application_id, generation, legal_entity_id, payment_id, posting_date, state, main_key,
				main_currency, main_lines, fx_key, fx_currency, fx_lines, txid)
			values ($1, $2, $3, $4, $5, $6::date, $7, $8, $9, $10, $11, $12, $13, $14)`, p.TenantID, app.id, generation, pay.LegalEntityID, pay.ID, app.postingDate,
			attemptPending, main.key, main.currency, mainLines, fxKey, fxCurrency, fxLines, txid)
		return err
	})
	return att, err
}

func (s *Postgres) postAttemptLegs(ctx context.Context, pay Payment, att *reversalAttempt) ([]string, error) {
	for _, leg := range att.legs {
		ambiguous, err := s.postAttemptLeg(ctx, pay, att, leg)
		if err != nil {
			if ambiguous {
				return []string{leg.name}, err
			}
			return nil, err
		}
	}
	if err := att.record(context.WithoutCancel(ctx), s, attemptPosted); err != nil {
		slog.ErrorContext(ctx, "payment application reversal attempt entry ids not recorded", "application", att.applicationID, "generation", att.generation,
			"err", err, "correlationId", middleware.GetReqID(ctx))
	}
	return nil, nil
}

func (s *Postgres) postAttemptLeg(ctx context.Context, pay Payment, att *reversalAttempt, leg *attemptLeg) (bool, error) {
	if leg.entryID != "" {
		return false, nil
	}
	ledgerID := s.journalLedger(pay.LegalEntityID)
	if s.ledger == nil || ledgerID == "" {
		slog.ErrorContext(ctx, "payment application reversal not posted: ledger not configured", "application", att.applicationID, "correlationId", middleware.GetReqID(ctx))
		return false, httpx.NewProblem(http.StatusServiceUnavailable, CodeReversalLedgerUnavailable, "the ledger is not available; retry with the same Idempotency-Key")
	}
	suffix := ""
	if leg.name == legFX {
		suffix = "-fx"
	}
	started := time.Now()
	id, err := s.postLedgerEntry(ctx, ledgerEntry{legalEntityID: pay.LegalEntityID, ledgerID: ledgerID, documentID: "PAY-" + pay.ID + "-APP-REVERSAL" + suffix,
		postingDate: att.postingDate, currency: leg.currency, key: leg.key, sourceType: "receivables-payment_application_reversal"}, leg.lines)
	if err != nil {
		ambiguous := !ledgerclient.Rejected(err)
		slog.ErrorContext(ctx, "payment application reversal not posted", "application", att.applicationID, "leg", leg.name, "ledgerKey", leg.key,
			"durationMs", time.Since(started).Milliseconds(), "ambiguous", ambiguous, "err", err, "correlationId", middleware.GetReqID(ctx))
		if ledgerclient.CodeOf(err) == ledgerclient.CodePeriodClosed {
			return false, httpx.NewProblem(http.StatusConflict, CodeReversalPeriodClosed, "the posting period of the application is closed; the reversal is not re-dated")
		}
		return ambiguous, httpx.NewProblem(http.StatusServiceUnavailable, CodeReversalLedgerUnavailable, "the ledger refused or could not be reached; retry with the same Idempotency-Key")
	}
	leg.entryID = id
	slog.InfoContext(ctx, "payment application reversal posted", "application", att.applicationID, "leg", leg.name, "ledgerKey", leg.key, "entry", id,
		"durationMs", time.Since(started).Milliseconds(), "correlationId", middleware.GetReqID(ctx))
	return false, nil
}

func (a *reversalAttempt) write(ctx context.Context, tx pgx.Tx, tenantID, state string) error {
	main, fx := a.entryIDs()
	_, err := tx.Exec(ctx, `update `+attemptTable+` set state = $4, main_entry_id = nullif($5, ''), fx_entry_id = nullif($6, ''), probe_legs = $7, updated_at = now()
		where tenant_id = $1 and application_id = $2 and generation = $3 and state not in ('COMMITTED', 'COMPENSATED')`,
		tenantID, a.applicationID, a.generation, state, main, fx, append([]string{}, a.probe...))
	return err
}

func (a *reversalAttempt) markCommitted(ctx context.Context, tx pgx.Tx) error {
	p, _ := tenant.FromContext(ctx)
	return a.write(ctx, tx, p.TenantID, attemptCommitted)
}

func (a *reversalAttempt) record(ctx context.Context, s *Postgres, state string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error { return a.write(ctx, tx, p.TenantID, state) })
}

func (s *Postgres) failReversalAttempt(ctx context.Context, att *reversalAttempt, probe []string, commitPhase bool) {
	detached := context.WithoutCancel(ctx)
	att.probe = probe
	if !commitPhase && len(probe) == 0 {
		err := s.compensateAttempt(detached, att)
		if err == nil {
			return
		}
		slog.ErrorContext(ctx, "payment application reversal compensation not recorded; the sweeper retries it", "application", att.applicationID,
			"generation", att.generation, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
	if err := att.record(detached, s, attemptAmbiguous); err != nil {
		slog.ErrorContext(ctx, "payment application reversal attempt not marked ambiguous; the sweeper treats it as unresolved once it is stale", "application", att.applicationID,
			"generation", att.generation, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

func (s *Postgres) compensateAttempt(ctx context.Context, att *reversalAttempt) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, leg := range att.legs {
			if leg.entryID == "" {
				continue
			}
			flipped := make([]journalLine, len(leg.lines))
			for i, l := range leg.lines {
				l.Credit = !l.Credit
				flipped[i] = l
			}
			suffix := ""
			if leg.name == legFX {
				suffix = "-fx"
			}
			if err := insertJournal(ctx, tx, p.TenantID, journalSpec{LegalEntityID: att.legalEntityID, SourceType: JournalReversalCompensation, SourceID: att.paymentID,
				SourceKey:  "reversal-compensation:" + att.applicationID + ":" + leg.name + ":" + strconv.Itoa(att.generation),
				DocumentID: "PAY-" + att.paymentID + "-APP-REVERSAL-COMPENSATION" + suffix, PostingDate: att.postingDate, Currency: leg.currency, Lines: flipped}, flipped); err != nil {
				return err
			}
		}
		att.probe = nil
		return att.write(ctx, tx, p.TenantID, attemptCompensated)
	})
}

func (s *Postgres) resolveReversalAttempts(ctx context.Context, minAge time.Duration) (int, error) {
	p, _ := tenant.FromContext(ctx)
	type claim struct {
		att   *reversalAttempt
		prior string
	}
	var claims []claim
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+attemptColumns+` from `+attemptTable+` where tenant_id = $1 and state in ('PENDING', 'POSTED', 'AMBIGUOUS', 'COMPENSATING')
			and updated_at <= now() - make_interval(secs => $2) order by updated_at limit $3 for update skip locked`, p.TenantID, minAge.Seconds(), reversalAttemptBatch)
		if err != nil {
			return err
		}
		for rows.Next() {
			att, err := scanAttempt(rows)
			if err != nil {
				rows.Close()
				return err
			}
			claims = append(claims, claim{att: att, prior: att.state})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range claims {
			if c.prior == attemptPending {
				for _, l := range c.att.legs {
					if l.entryID == "" {
						c.att.probe = append(c.att.probe, l.name)
					}
				}
			}
			if err := c.att.write(ctx, tx, p.TenantID, attemptCompensating); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	resolved := 0
	for _, c := range claims {
		if s.resolveAttempt(ctx, c.att) {
			resolved++
		}
	}
	return resolved, nil
}

func (s *Postgres) resolveAttempt(ctx context.Context, att *reversalAttempt) bool {
	p, _ := tenant.FromContext(ctx)
	unresolved := func(reason string, err error) bool {
		slog.ErrorContext(ctx, "payment application reversal attempt still unresolved", "application", att.applicationID, "generation", att.generation, "reason", reason,
			"err", err, "correlationId", middleware.GetReqID(ctx))
		if rerr := att.record(ctx, s, attemptAmbiguous); rerr != nil {
			slog.ErrorContext(ctx, "payment application reversal attempt not released", "application", att.applicationID, "generation", att.generation, "err", rerr)
		}
		return false
	}
	var status *string
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select txid_status($1)`, att.txid).Scan(&status)
	}); err != nil {
		return unresolved("transaction status unreadable", err)
	}
	switch {
	case status != nil && *status == "committed":
		if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			att.probe = nil
			return att.write(ctx, tx, p.TenantID, attemptCommitted)
		}); err != nil {
			return unresolved("commit not recorded", err)
		}
		slog.WarnContext(ctx, "payment application reversal attempt had committed; nothing compensated", "application", att.applicationID, "generation", att.generation)
		return true
	case status != nil && *status == "aborted":
	default:
		return unresolved("transaction outcome not yet known", nil)
	}
	pay := Payment{ID: att.paymentID, LegalEntityID: att.legalEntityID}
	if s.ledger == nil || s.journalLedger(att.legalEntityID) == "" {
		return unresolved("ledger not configured", nil)
	}
	for _, name := range att.probe {
		leg := att.leg(name)
		if leg == nil || leg.entryID != "" {
			continue
		}
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalLedgerBudget)
		ambiguous, err := s.postAttemptLeg(probeCtx, pay, att, leg)
		cancel()
		if err != nil && ambiguous {
			return unresolved("ledger leg "+name+" outcome still unknown", err)
		}
	}
	if err := s.compensateAttempt(ctx, att); err != nil {
		return unresolved("compensation not recorded", err)
	}
	slog.WarnContext(ctx, "payment application reversal attempt rolled back; its ledger legs compensated", "application", att.applicationID, "generation", att.generation)
	return true
}
