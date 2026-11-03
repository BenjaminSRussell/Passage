package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// Problem codes of dimension realignment (FR-11.24).
const (
	CodeRealignmentNotFound = "LEDGER_REALIGNMENT_NOT_FOUND"
	CodeRealignmentState    = "LEDGER_REALIGNMENT_STATE"
	CodeRealignmentEmpty    = "LEDGER_REALIGNMENT_EMPTY"
	// CodeRealignmentEntry refuses reversing one entry of a realignment run on its own.
	CodeRealignmentEntry = "LEDGER_REALIGNMENT_ENTRY"
	// DutyRealign is the duty "Run dimension realignment" (PRD section 8); the policy bundle is a handoff.
	DutyRealign = "ledger.realignment.run"

	maxRealignmentPreview = 1000
)

// RealignmentRun is a restatement of dimension values on posted balances.
type RealignmentRun struct {
	ID                   string                    `json:"id"`
	LedgerID             string                    `json:"ledgerId"`
	Status               string                    `json:"status"`
	Filter               domain.RealignmentFilter  `json:"filter"`
	Mapping              domain.RealignmentMapping `json:"mapping"`
	ReasonCode           *string                   `json:"reasonCode,omitempty"`
	ReasonComment        *string                   `json:"reasonComment,omitempty"`
	CombinationsAffected int                       `json:"combinationsAffected"`
	LineCount            int64                     `json:"lineCount"`
	AmountMoved          string                    `json:"amountMoved"`
	Currency             string                    `json:"currency"`
	Preview              []domain.RealignmentRow   `json:"preview"`
	PreviewTruncated     bool                      `json:"previewTruncated"`
	EntryIDs             []string                  `json:"entryIds"`
	ReversalEntryIDs     []string                  `json:"reversalEntryIds"`
	Version              int                       `json:"version"`
	CreatedBy            string                    `json:"createdBy"`
	CreatedAt            time.Time                 `json:"createdAt"`
	PostedBy             *string                   `json:"postedBy,omitempty"`
	PostedAt             *time.Time                `json:"postedAt,omitempty"`
	ReversedBy           *string                   `json:"reversedBy,omitempty"`
	ReversedAt           *time.Time                `json:"reversedAt,omitempty"`
}

// RealignmentInput is a request to simulate a run.
type RealignmentInput struct {
	LedgerID string
	Filter   domain.RealignmentFilter
	Mapping  domain.RealignmentMapping
	Key      string
	Hash     []byte
	Actor    string
}

// RealignmentAction posts, reverses or cancels a run. A repeat on a run already in the target
// status returns it unchanged. PostingDate is only read by a reversal: when set, every reversing
// entry is dated that day instead of its original date (for a period closed since the post).
type RealignmentAction struct {
	LedgerID, RunID, ReasonCode, ReasonComment, Key, Actor, TraceID string
	Today                                                           time.Time
	PostingDate                                                     *time.Time
	CanPostLedgerSpecific                                           bool
}

const realignmentColumns = `r.id::text, r.ledger_id::text, r.status, r.filter, r.mapping, r.reason_code, r.reason_comment, r.combinations_affected,
	r.line_count, r.amount_moved::text, r.preview, r.entry_ids::text[], r.reversal_entry_ids::text[], r.version, r.created_by, r.created_at,
	r.posted_by, r.posted_at, r.reversed_by, r.reversed_at, l.currency`

func scanRealignment(row pgx.Row) (RealignmentRun, error) {
	var r RealignmentRun
	var filter, mapping, preview []byte
	err := row.Scan(&r.ID, &r.LedgerID, &r.Status, &filter, &mapping, &r.ReasonCode, &r.ReasonComment, &r.CombinationsAffected, &r.LineCount,
		&r.AmountMoved, &preview, &r.EntryIDs, &r.ReversalEntryIDs, &r.Version, &r.CreatedBy, &r.CreatedAt, &r.PostedBy, &r.PostedAt,
		&r.ReversedBy, &r.ReversedAt, &r.Currency)
	if err != nil {
		return r, err
	}
	for _, d := range []struct {
		raw []byte
		to  any
	}{{filter, &r.Filter}, {mapping, &r.Mapping}, {preview, &r.Preview}} {
		if err := json.Unmarshal(d.raw, d.to); err != nil {
			return r, err
		}
	}
	r.PreviewTruncated = r.CombinationsAffected > len(r.Preview)
	if r.EntryIDs == nil {
		r.EntryIDs = []string{}
	}
	if r.ReversalEntryIDs == nil {
		r.ReversalEntryIDs = []string{}
	}
	return r, nil
}

func loadRealignment(ctx context.Context, tx pgx.Tx, tenantID, ledgerID, id string, lock bool) (RealignmentRun, error) {
	q := `select ` + realignmentColumns + ` from realignment_run r join ledger l on l.tenant_id = r.tenant_id and l.id = r.ledger_id
		where r.tenant_id = $1 and r.ledger_id = $2 and r.id = $3`
	if lock {
		q += ` for update of r`
	}
	run, err := scanRealignment(tx.QueryRow(ctx, q, tenantID, ledgerID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return run, httpx.NewProblem(http.StatusNotFound, CodeRealignmentNotFound, "realignment run not found")
	}
	return run, err
}

// realignmentCandidates reads the balance rows a run would move: the filter's accounts and periods,
// combinations holding one of the mapping's old values, and no open-item managed account (a
// reclassification there would open items of its own).
func realignmentCandidates(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, f domain.RealignmentFilter,
	m domain.RealignmentMapping) ([]domain.RealignmentCandidate, error) {
	args := []any{tenantID, ledger.ID, f.PeriodFrom, f.PeriodTo, f.AccountFrom, f.AccountTo}
	attrs := make([]string, 0, len(m))
	for a := range m {
		attrs = append(attrs, a)
	}
	sort.Strings(attrs)
	var match []string
	for _, a := range attrs {
		olds := make([]string, 0, len(m[a]))
		for o := range m[a] {
			olds = append(olds, o)
		}
		sort.Strings(olds)
		args = append(args, a, olds)
		match = append(match, fmt.Sprintf("c.dimensions ->> $%d = any($%d)", len(args)-1, len(args)))
	}
	rows, err := tx.Query(ctx, `select a.id::text, a.account_number, b.period_key, b.dimension_combination_id, c.display_name, c.dimensions,
			b.net::text, b.line_count
		from balance b
		join main_account a on a.tenant_id = b.tenant_id and a.id = b.main_account_id
		join dimension_combination c on c.tenant_id = b.tenant_id and c.id = b.dimension_combination_id
		where b.tenant_id = $1 and b.ledger_id = $2 and b.currency_type = 'acct' and b.period_key between $3 and $4
			and ($5::text = '' or a.account_number >= $5::text) and ($6::text = '' or a.account_number <= $6::text)
			and not exists (select 1 from open_item_setting o where o.tenant_id = b.tenant_id and o.ledger_id = b.ledger_id
				and o.main_account_id = b.main_account_id and o.enabled)
			and (`+strings.Join(match, " or ")+`)
		order by b.period_key, a.account_number, c.display_name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RealignmentCandidate
	for rows.Next() {
		var c domain.RealignmentCandidate
		var net string
		if err := rows.Scan(&c.AccountID, &c.Account, &c.Period, &c.CombinationID, &c.DisplayName, &c.Dimensions, &net, &c.LineCount); err != nil {
			return nil, err
		}
		if c.Net, err = decimal.NewFromString(net); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return withCandidateQuantities(ctx, tx, tenantID, ledger.ID, f, out)
}

// candidateQuantitiesFromProjection reads the net statistical quantities of the filter's accounts and
// periods from the quantity projection.
const candidateQuantitiesFromProjection = `select b.main_account_id::text, b.period_key, b.dimension_combination_id, b.unit, b.net_quantity::text
	from balance_quantity b join main_account a on a.tenant_id = b.tenant_id and a.id = b.main_account_id
	where b.tenant_id = $1 and b.ledger_id = $2 and b.period_key between $3 and $4 and b.net_quantity <> 0
		and ($5::text = '' or a.account_number >= $5::text) and ($6::text = '' or a.account_number <= $6::text)
	order by b.period_key, b.main_account_id, b.dimension_combination_id, b.unit`

// candidateQuantitiesFromJournal reads the same totals from the journal, for a ledger whose
// projection still misses the quantity lines posted before migration 0027 (quantity_rebuild_pending).
// A debit adds and a credit subtracts whether or not the line is a negative posting, as in the
// rebuild (rebuiltQuantityCTE): a negative posting moves the other side's total by a negative amount.
const candidateQuantitiesFromJournal = `select l.main_account_id::text, e.period_key, l.dimension_combination_id, l.unit,
		sum(case when l.debit_credit = 'D' then l.quantity else -l.quantity end)::text
	from journal_line l join journal_entry e on e.tenant_id = l.tenant_id and e.id = l.entry_id
		join main_account a on a.tenant_id = l.tenant_id and a.id = l.main_account_id
	where l.tenant_id = $1 and e.ledger_id = $2 and e.period_key between $3 and $4 and l.quantity is not null
		and ($5::text = '' or a.account_number >= $5::text) and ($6::text = '' or a.account_number <= $6::text)
	group by l.main_account_id, e.period_key, l.dimension_combination_id, l.unit
	having sum(case when l.debit_credit = 'D' then l.quantity else -l.quantity end) <> 0
	order by e.period_key, l.main_account_id, l.dimension_combination_id, l.unit`

// withCandidateQuantities attaches the net statistical quantity per unit (BR-11.2) of each candidate
// balance row, read from the quantity projection with the filter's accounts and periods. Until a
// ledger's first rebuild after migration 0027 the projection may miss older quantity lines, so the
// totals are read from the journal instead: a run must empty the old combination's quantities as it
// empties its amounts.
func withCandidateQuantities(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string, f domain.RealignmentFilter,
	cands []domain.RealignmentCandidate) ([]domain.RealignmentCandidate, error) {
	if len(cands) == 0 {
		return cands, nil
	}
	var pending bool
	if err := tx.QueryRow(ctx, `select quantity_rebuild_pending from ledger where tenant_id = $1 and id = $2`,
		tenantID, ledgerID).Scan(&pending); err != nil {
		return nil, err
	}
	q := candidateQuantitiesFromProjection
	if pending {
		q = candidateQuantitiesFromJournal
	}
	rows, err := tx.Query(ctx, q, tenantID, ledgerID, f.PeriodFrom, f.PeriodTo, f.AccountFrom, f.AccountTo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string][]domain.RealignmentQuantity{}
	for rows.Next() {
		var account, period, unit, net string
		var combo []byte
		if err := rows.Scan(&account, &period, &combo, &unit, &net); err != nil {
			return nil, err
		}
		n, err := decimal.NewFromString(net)
		if err != nil {
			return nil, err
		}
		k := account + "|" + period + "|" + string(combo)
		byKey[k] = append(byKey[k], domain.RealignmentQuantity{Unit: unit, Net: n.String()})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range cands {
		c := &cands[i]
		c.Quantities = byKey[c.AccountID+"|"+c.Period+"|"+string(c.CombinationID)]
	}
	return cands, nil
}

func planFor(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, f domain.RealignmentFilter, m domain.RealignmentMapping) ([]domain.RealignmentRow, error) {
	cands, err := realignmentCandidates(ctx, tx, tenantID, ledger, f, m)
	if err != nil {
		return nil, err
	}
	return domain.PlanRealignment(m, cands, func(account string, dims map[string]string) string {
		_, name := domain.CombinationID(ledger.DimensionAttributes, ledger.ChartCode, account, dims)
		return name
	}), nil
}

func invalidRealignment(problems map[string]string) error {
	var vs []domain.Violation
	for field, msg := range problems {
		vs = append(vs, domain.Violation{Code: domain.CodeRealignmentInvalid, Field: field, Message: msg})
	}
	sortViolations(vs)
	return &domain.RejectedError{Violations: vs}
}

// CreateRealignment simulates a run (FR-11.24): it lists the affected combinations, line counts and
// amounts and stores the run as proposed. Nothing is posted. A repeated key returns the stored run
// (replayed true); the same key with another body is 409 LEDGER_IDEMPOTENCY_CONFLICT.
func (s *Postgres) CreateRealignment(ctx context.Context, in RealignmentInput) (RealignmentRun, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RealignmentRun{}, false, err
	}
	var out RealignmentRun
	var replayed bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, true)
		if err != nil {
			return err
		}
		var existing string
		var stored []byte
		err = tx.QueryRow(ctx, `select id::text, payload_hash from realignment_run where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.Key).Scan(&existing, &stored)
		switch {
		case err == nil:
			if !bytes.Equal(stored, in.Hash) {
				return httpx.NewProblem(http.StatusConflict, "LEDGER_IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used with a different request body")
			}
			replayed = true
			out, err = loadRealignment(ctx, tx, p.TenantID, ledger.ID, existing, false)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if problems := domain.ValidateRealignment(in.Filter, in.Mapping, ledger.DimensionAttributes); len(problems) > 0 {
			return invalidRealignment(problems)
		}
		rows, err := planFor(ctx, tx, p.TenantID, ledger, in.Filter, in.Mapping)
		if err != nil {
			return err
		}
		moved, lines := domain.RealignmentTotals(rows)
		preview := rows
		if len(preview) > maxRealignmentPreview {
			preview = preview[:maxRealignmentPreview]
		}
		if preview == nil {
			preview = []domain.RealignmentRow{}
		}
		filter, _ := json.Marshal(in.Filter)
		mapping, _ := json.Marshal(in.Mapping)
		prev, err := json.Marshal(preview)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err = tx.Exec(ctx, `insert into realignment_run (tenant_id, id, legal_entity_id, ledger_id, filter, mapping, combinations_affected,
				line_count, amount_moved, preview, idempotency_key, payload_hash, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::text::numeric, $10, $11, $12, $13)`,
			p.TenantID, id, ledger.LegalEntityID, ledger.ID, filter, mapping, len(rows), lines,
			moved.StringFixed(int32(money.MinorUnits(ledger.Currency))), prev, in.Key, in.Hash, p.UserID); err != nil {
			return err
		}
		out, err = loadRealignment(ctx, tx, p.TenantID, ledger.ID, id, false)
		return err
	})
	return out, replayed, err
}

// ListRealignments returns the runs of a ledger, newest first, without their previews.
func (s *Postgres) ListRealignments(ctx context.Context, ledgerID string) ([]RealignmentRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []RealignmentRun{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+realignmentColumns+` from realignment_run r join ledger l on l.tenant_id = r.tenant_id and l.id = r.ledger_id
			where r.tenant_id = $1 and r.ledger_id = $2 order by r.created_at desc, r.id limit 200`, p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRealignment(rows)
			if err != nil {
				return err
			}
			r.Preview, r.PreviewTruncated = []domain.RealignmentRow{}, false
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetRealignment returns one run with its preview.
func (s *Postgres) GetRealignment(ctx context.Context, ledgerID, id string) (RealignmentRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RealignmentRun{}, err
	}
	var out RealignmentRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = loadRealignment(ctx, tx, p.TenantID, ledgerID, id, false)
		return err
	})
	return out, err
}

func stateConflict(run RealignmentRun, want string) error {
	return httpx.NewProblem(http.StatusConflict, CodeRealignmentState,
		fmt.Sprintf("the realignment run is %s; only a %s run can take this action", run.Status, want))
}

// asRealignmentPeriodError renames a closed period rejection to LEDGER_REALIGNMENT_PERIOD_CLOSED,
// adding what the caller can do about it.
func asRealignmentPeriodError(err error, hint string) error {
	var rej *domain.RejectedError
	if errors.As(err, &rej) {
		for i, v := range rej.Violations {
			if v.Code == codePeriodClosed {
				rej.Violations[i].Code = domain.CodeRealignmentPeriodClosed
				rej.Violations[i].Message += "; " + hint
			}
		}
	}
	return err
}

const (
	postPeriodHint    = "exclude the period or reopen it"
	reversePeriodHint = "reopen the period or reverse with a postingDate in an open period"
)

// requireRealignmentReason applies the shared reason code rules to a post or reverse (FR-11.15).
func requireRealignmentReason(ctx context.Context, tx pgx.Tx, tenantID, code, comment string) error {
	var reason *domain.ReasonInfo
	if code != "" {
		var err error
		if reason, err = loadReason(ctx, tx, tenantID, code); err != nil {
			return err
		}
	}
	if v := domain.CheckRealignmentReason(reason, code, comment); len(v) > 0 {
		return &domain.RejectedError{Violations: v}
	}
	return nil
}

func keyHash(key string) []byte {
	h, _ := domain.HashPayload(key)
	return h
}

// realignmentEntryDate is the posting date of a period's reclassification: the period's last day, or
// today while the period is still running.
func realignmentEntryDate(period string, today time.Time) (time.Time, error) {
	end, err := domain.PeriodEnd(period)
	if err != nil {
		return end, err
	}
	start := end.AddDate(0, 0, -(end.Day() - 1))
	if end.After(today) && !today.Before(start) {
		return today, nil
	}
	return end, nil
}

// PostRealignment posts a proposed run (FR-11.24): the current balances are re-read under the ledger
// lock and one reclassification entry per period is posted, flagged as a realignment, with the
// reason code. The run must be proposed; a repeat on a posted run returns it unchanged. Every
// period has to be open (or on hold with duty ledger.period.postclosed), else 422
// LEDGER_REALIGNMENT_PERIOD_CLOSED and nothing is posted.
func (s *Postgres) PostRealignment(ctx context.Context, in RealignmentAction) (RealignmentRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RealignmentRun{}, err
	}
	var out RealignmentRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, true)
		if err != nil {
			return err
		}
		if err := requireLedgerSpecific(ctx, tx, p.TenantID, ledger.ID, in.CanPostLedgerSpecific); err != nil {
			return err
		}
		run, err := loadRealignment(ctx, tx, p.TenantID, ledger.ID, in.RunID, true)
		if err != nil {
			return err
		}
		if run.Status == domain.RealignPosted {
			out = run
			return nil
		}
		if run.Status != domain.RealignProposed {
			return stateConflict(run, domain.RealignProposed)
		}
		if err := requireRealignmentReason(ctx, tx, p.TenantID, in.ReasonCode, in.ReasonComment); err != nil {
			return err
		}
		rows, err := planFor(ctx, tx, p.TenantID, ledger, run.Filter, run.Mapping)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRealignmentEmpty, "no balance matches the filter and mapping any more; nothing to realign")
		}
		byPeriod := map[string][]domain.RealignmentRow{}
		var periods []string
		for _, r := range rows {
			if _, ok := byPeriod[r.Period]; !ok {
				periods = append(periods, r.Period)
			}
			byPeriod[r.Period] = append(byPeriod[r.Period], r)
		}
		sort.Strings(periods)
		var closed []domain.Violation
		dates := map[string]time.Time{}
		for _, period := range periods {
			date, err := realignmentEntryDate(period, in.Today)
			if err != nil {
				return err
			}
			dates[period] = date
			v, err := periodViolations(ctx, ledger, date, "ledger")
			if err != nil {
				return err
			}
			for _, x := range v {
				x.Code = domain.CodeRealignmentPeriodClosed
				x.Message = "period " + period + ": " + x.Message + "; exclude the period or reopen it"
				closed = append(closed, x)
			}
		}
		if len(closed) > 0 {
			return &domain.RejectedError{Violations: closed}
		}
		var entryIDs []string
		for _, period := range periods {
			for part, chunk := range domain.ChunkRealignmentRows(byPeriod[period], realignmentRowsPerEntry) {
				id, err := postRealignmentPeriod(ctx, tx, p.TenantID, ledger, run, period, part, dates[period], chunk, in)
				if err != nil {
					return err
				}
				entryIDs = append(entryIDs, id)
			}
		}
		moved, lines := domain.RealignmentTotals(rows)
		// The preview is replaced by what was posted, which can differ from the simulation when
		// postings arrived in between.
		preview := rows
		if len(preview) > maxRealignmentPreview {
			preview = preview[:maxRealignmentPreview]
		}
		prev, err := json.Marshal(preview)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update realignment_run set status = 'posted', reason_code = $4, reason_comment = nullif($5, ''), entry_ids = $6::text[]::uuid[],
				combinations_affected = $7, line_count = $8, amount_moved = $9::text::numeric, posted_by = $10, posted_at = now(), preview = $11,
				version = version + 1, updated_at = now()
			where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, ledger.ID, run.ID, in.ReasonCode, in.ReasonComment, entryIDs,
			len(rows), lines, moved.StringFixed(int32(money.MinorUnits(ledger.Currency))), in.Actor, prev); err != nil {
			return err
		}
		if err := announceRealignment(ctx, tx, p.TenantID, run, periods, len(rows), entryIDs, false, in); err != nil {
			return err
		}
		out, err = loadRealignment(ctx, tx, p.TenantID, ledger.ID, run.ID, false)
		return err
	})
	return out, asRealignmentPeriodError(idempotencyRace(err), postPeriodHint)
}

// realignmentRowsPerEntry is the most rows (two lines each) one reclassification entry carries; a
// variable so tests can exercise the split without 5,000 combinations.
var realignmentRowsPerEntry = domain.MaxRealignmentRowsPerEntry

// postRealignmentPeriod posts one reclassification entry of a period (part counts the entries of a
// period with more rows than one entry holds).
//
// Pipeline (FR-11.24 against the posting pipeline of FR-11.5): derivation (FR-11.22) and document
// splitting (FR-11.23) do not run, because every line already carries a full combination: the old
// side is the stored combination and the new side is the explicit mapping, which derivation would
// overwrite and inheritance would change. The new side passes account, structure, dimension value
// and validation rule checks (FR-11.21) like any posting; the old side is exempt, as it only
// empties a combination that was valid when it was posted.
func postRealignmentPeriod(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, run RealignmentRun, period string, part int, date time.Time,
	rows []domain.RealignmentRow, in RealignmentAction) (string, error) {
	text := "Dimension realignment " + run.ID[:8]
	// Each entry takes the next posting sequence, so the ledger is read again after the previous one.
	ledger, err := loadLedger(ctx, tx, tenantID, ledger.ID, true)
	if err != nil {
		return "", err
	}
	numbers := make([]string, 0, len(rows))
	for _, r := range rows {
		numbers = append(numbers, r.Account)
	}
	accounts, err := loadAccounts(ctx, tx, tenantID, ledger.ChartCode, numbers)
	if err != nil {
		return "", err
	}
	// The new side is validated like any posting (structure, registered and active dimension values).
	// Each new line is checked against a mirror line so the check balances on its own; the old
	// side is the stored combination, which may use a value that has been deactivated since. Lines
	// come in (old, new) pairs: the amount pair of a row, then one statistical pair per unit.
	var planned []domain.LineInput
	var pairRows []domain.RealignmentRow
	for _, r := range rows {
		ls := domain.RealignmentRowLines(r, ledger.Currency, text)
		planned = append(planned, ls...)
		for range len(ls) / 2 {
			pairRows = append(pairRows, r)
		}
	}
	probe := make([]domain.LineInput, 0, len(planned))
	for i := 1; i < len(planned); i += 2 {
		n := planned[i]
		mirror := n
		mirror.Side = domain.ReversalSide(n.Side)
		probe = append(probe, n, mirror)
	}
	rules, err := withStructures(ctx, tx, tenantID, ledger.ID, ledger.Rules(), probe)
	if err != nil {
		return "", err
	}
	res := domain.ValidateEntry(rules, accounts, date, in.Today, probe)
	outcome, err := realignmentRuleOutcome(ctx, tx, tenantID, ledger, accounts, date, planned)
	if err != nil {
		return "", err
	}
	if violations := append(res.Violations, outcome...); len(violations) > 0 {
		return "", &domain.RejectedError{Violations: violations}
	}
	lines := make([]writeLine, 0, len(planned))
	for i, r := range pairRows {
		old, nw := planned[i*2], res.Lines[i*2]
		acct := accounts[r.Account]
		lines = append(lines, writeLine{id: ids.New(), valid: domain.ValidLine{Account: acct, Dimensions: old.Dimensions, Side: old.Side,
			Amount: old.Amount, Text: text, Quantity: old.Quantity, Unit: old.Unit, CombinationID: r.OldID, DisplayName: r.OldName}})
		nw.Side, nw.Text = planned[i*2+1].Side, text
		lines = append(lines, writeLine{id: ids.New(), valid: nw})
	}
	key := "realign-" + run.ID + "-" + period
	if part > 0 {
		key += fmt.Sprintf("-%d", part)
	}
	entry, err := writeEntry(ctx, tx, tenantID, newEntry{
		ledger: ledger, documentID: ids.New(), postingDate: date, description: text,
		source: ledgerevents.Source{System: "ledger", Type: "realignment", ID: run.ID, URI: "passage://ledger/realignments/" + run.ID},
		key:    key, hash: keyHash(key), actor: in.Actor, traceID: in.TraceID, reasonCode: in.ReasonCode, reasonComment: in.ReasonComment,
		realignmentRunID: run.ID, lines: lines,
	})
	return entry.ID, err
}

// realignmentRuleOutcome runs the ledger's active validation rules (FR-11.21) on a reclassification
// entry: header rules over the whole entry (source ledger/realignment), line rules over the new side
// only (the odd lines). Errors refuse the post and name the entry line; warnings are not returned.
func realignmentRuleOutcome(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, accounts map[string]domain.Account,
	date time.Time, planned []domain.LineInput) ([]domain.Violation, error) {
	rules, err := queryValidationRules(ctx, tx, tenantID, ledger.ID, true)
	if err != nil || len(rules) == 0 {
		return nil, err
	}
	var header, line []domain.ValidationRule
	for _, r := range rules {
		if r.Level == domain.RuleLevelHeader {
			header = append(header, r)
		} else {
			line = append(line, r)
		}
	}
	h := domain.RuleHeader{SourceSystem: "ledger", SourceType: "realignment", PostingDate: date}
	errs := domain.EvaluateRules(header, h, accounts, planned).Errors
	newSide := make([]domain.LineInput, 0, len(planned)/2)
	for i := 1; i < len(planned); i += 2 {
		newSide = append(newSide, planned[i])
	}
	for _, v := range domain.EvaluateRules(line, h, accounts, newSide).Errors {
		if v.LineIndex != nil {
			idx := *v.LineIndex*2 + 1
			v.LineIndex = &idx
		}
		errs = append(errs, v)
	}
	return errs, nil
}

func announceRealignment(ctx context.Context, tx pgx.Tx, tenantID string, run RealignmentRun, periods []string, combinations int,
	entryIDs []string, reversal bool, in RealignmentAction) error {
	return enqueueFor(ctx, tx, "realignment_run", run.ID, ledgerevents.RealignmentCompleted, tenantID, ledgerevents.RealignmentCompletedData{
		RunID: run.ID, LedgerID: run.LedgerID, PeriodFrom: periods[0], PeriodTo: periods[len(periods)-1],
		CombinationsAffected: combinations, EntryIDs: entryIDs, Reversal: reversal, Actor: in.Actor})
}

// ReverseRealignment reverses a posted run as a unit: every reclassification entry is reversed with
// the given reason code in one transaction, so the combination balances return to their values
// before the run. A repeat on a reversed run returns it unchanged.
func (s *Postgres) ReverseRealignment(ctx context.Context, in RealignmentAction) (RealignmentRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RealignmentRun{}, err
	}
	var out RealignmentRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, true)
		if err != nil {
			return err
		}
		run, err := loadRealignment(ctx, tx, p.TenantID, ledger.ID, in.RunID, true)
		if err != nil {
			return err
		}
		if run.Status == domain.RealignReversed {
			out = run
			return nil
		}
		if run.Status != domain.RealignPosted {
			return stateConflict(run, domain.RealignPosted)
		}
		if err := requireRealignmentReason(ctx, tx, p.TenantID, in.ReasonCode, in.ReasonComment); err != nil {
			return err
		}
		var reversals []string
		for i, entryID := range run.EntryIDs {
			key := fmt.Sprintf("realign-reverse-%s-%d", run.ID, i)
			res, err := reverseEntryTx(ctx, tx, p.TenantID, ReverseInput{EntryID: entryID, Description: "Reversal of dimension realignment " + run.ID[:8],
				ReasonCode: in.ReasonCode, ReasonComment: in.ReasonComment, PostingDate: in.PostingDate, Key: key, Hash: keyHash(key), Actor: in.Actor,
				TraceID: in.TraceID, Today: in.Today, CanPostLedgerSpecific: in.CanPostLedgerSpecific, realignmentRunID: run.ID})
			if err != nil {
				return err
			}
			reversals = append(reversals, res.Entries[0].ID)
		}
		if _, err := tx.Exec(ctx, `update realignment_run set status = 'reversed', reversal_entry_ids = $4::text[]::uuid[], reversed_by = $5,
				reversed_at = now(), version = version + 1, updated_at = now()
			where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, ledger.ID, run.ID, reversals, in.Actor); err != nil {
			return err
		}
		if err := announceRealignment(ctx, tx, p.TenantID, run, []string{run.Filter.PeriodFrom, run.Filter.PeriodTo}, run.CombinationsAffected, reversals, true, in); err != nil {
			return err
		}
		out, err = loadRealignment(ctx, tx, p.TenantID, ledger.ID, run.ID, false)
		return err
	})
	return out, asRealignmentPeriodError(idempotencyRace(err), reversePeriodHint)
}

// CancelRealignment drops a proposed run; nothing was posted by it.
func (s *Postgres) CancelRealignment(ctx context.Context, in RealignmentAction) (RealignmentRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RealignmentRun{}, err
	}
	var out RealignmentRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := loadRealignment(ctx, tx, p.TenantID, in.LedgerID, in.RunID, true)
		if err != nil {
			return err
		}
		if run.Status == domain.RealignCancelled {
			out = run
			return nil
		}
		if run.Status != domain.RealignProposed {
			return stateConflict(run, domain.RealignProposed)
		}
		if _, err := tx.Exec(ctx, `update realignment_run set status = 'cancelled', version = version + 1, updated_at = now()
			where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, in.LedgerID, run.ID); err != nil {
			return err
		}
		out, err = loadRealignment(ctx, tx, p.TenantID, in.LedgerID, run.ID, false)
		return err
	})
	return out, err
}
