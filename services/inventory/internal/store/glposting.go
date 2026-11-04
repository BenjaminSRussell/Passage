package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// GL posting codes and limits (#477).
const (
	CodeGLFailed        = "INV-GL-001" // the ledger did not answer, or answered with no code
	CodeGLLedgerForeign = "INV-GL-002" // the ledger is unknown or belongs to another legal entity
	CodeGLLedgerPinned  = "INV-GL-003" // the legal entity is pinned to another ledger
	CodeGLUnbalanced    = "INV-GL-004" // the stored entry cannot be sent (fewer than two lines, unbalanced)

	MaxGLPostingsPerRun = 200
	MaxGLAttempts       = 5

	glSourceType   = "inventory-journal"
	glKeyPrefix    = "inventory-journal:"
	glPosted       = "POSTED"
	glParked       = "PARKED"
	glBackoffSecs  = 60
	glBackoffLimit = 3600
)

// GLBooks books the inventory subledger in the general ledger.
type GLBooks interface {
	PostToGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error)
	RequeueParkedGL(ctx context.Context, legalEntityID string) (int, error)
}

var _ GLBooks = (*Postgres)(nil)

// GLPostRequest names the legal entity swept and the general ledger it books in.
type GLPostRequest struct {
	LegalEntityID string
	LedgerID      string
}

type GLBooked struct {
	JournalID string `json:"journalId"`
	EntryID   string `json:"entryId"`
	Lines     int    `json:"lines"`
}

type GLFailed struct {
	JournalID string `json:"journalId"`
	Code      string `json:"code"`
}

type GLPostResult struct {
	LedgerID string     `json:"ledgerId"`
	Booked   []GLBooked `json:"booked"`
	Failed   []GLFailed `json:"failed"`
	More     bool       `json:"more"`
	Parked   int        `json:"parked"`
}

// glCandidate is a POSTED journal whose stored GL entry no confirmed posting covers.
type glCandidate struct {
	journalID, date string
	entry           domain.JournalEntry
}

// ledgerEntry builds the ledger entry from the journal's stored GL entry alone: one ledger line per stored line and side, in the
// stored order, under the journal's idempotency key.
func (c glCandidate) ledgerEntry(legalEntityID, ledgerID string) ledgerclient.Entry {
	e := ledgerclient.Entry{LegalEntityID: legalEntityID, LedgerID: ledgerID, DocumentID: c.journalID, PostingDate: c.date,
		Currency: c.entry.Currency, IdempotencyKey: glKeyPrefix + c.journalID, SourceType: glSourceType}
	for _, l := range c.entry.Lines {
		if l.Debit.IsPositive() {
			e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Amount: l.Debit})
		}
		if l.Credit.IsPositive() {
			e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Credit: true, Amount: l.Credit})
		}
	}
	return e
}

// checkLedgerOwner refuses (409) a general ledger that is unknown or does not belong to the legal entity.
func checkLedgerOwner(ctx context.Context, ledger ledgerclient.Ledger, legalEntityID, ledgerID string) error {
	owner, err := ledger.LegalEntity(ctx, legalEntityID, ledgerID)
	if err != nil {
		slog.ErrorContext(ctx, "inventory cannot read the ledger", "ledger", ledgerID, "err", err, "correlationId", middleware.GetReqID(ctx))
		var le *ledgerclient.Error
		if errors.As(err, &le) && (le.Status == http.StatusNotFound || le.Status == http.StatusUnprocessableEntity) {
			return problem(http.StatusConflict, CodeGLLedgerForeign, "the ledger is unknown or not readable for this legal entity")
		}
		return problem(http.StatusBadGateway, CodeGLFailed, "the general ledger did not answer the ledger lookup")
	}
	if !strings.EqualFold(owner, legalEntityID) {
		return problem(http.StatusConflict, CodeGLLedgerForeign, "the ledger belongs to another legal entity")
	}
	return nil
}

func (s *Postgres) pinnedLedger(ctx context.Context, tenantID, legalEntityID string) (string, error) {
	var pinned string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select gl_ledger_id::text from inventory_gl_ledger where tenant_id = $1 and legal_entity_id = $2`,
			tenantID, legalEntityID).Scan(&pinned)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return pinned, err
}

// pinLedger pins the legal entity to ledgerID unless it is already pinned, and returns the pinned ledger.
func (s *Postgres) pinLedger(ctx context.Context, p tenant.Principal, legalEntityID, ledgerID string) (string, error) {
	var pinned string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into inventory_gl_ledger (tenant_id, legal_entity_id, gl_ledger_id, pinned_by) values ($1, $2, $3, $4)
			on conflict do nothing`, p.TenantID, legalEntityID, ledgerID, p.UserID); err != nil {
			return scopeProblem(err)
		}
		return tx.QueryRow(ctx, `select gl_ledger_id::text from inventory_gl_ledger where tenant_id = $1 and legal_entity_id = $2`,
			p.TenantID, legalEntityID).Scan(&pinned)
	})
	return pinned, err
}

func pinnedElsewhere() error {
	return problem(http.StatusConflict, CodeGLLedgerPinned, "the legal entity books its inventory entries in another general ledger")
}

func (s *Postgres) unbookedJournals(ctx context.Context, tenantID, legalEntityID string) ([]glCandidate, int, error) {
	var out []glCandidate
	parked := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select count(*) from inventory_gl_posting where tenant_id = $1 and legal_entity_id = $2 and status = 'PARKED'`,
			tenantID, legalEntityID).Scan(&parked); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select j.id::text, j.posting_date::text, j.gl_entry::text
			from inventory_journal j
			left join inventory_gl_posting g on g.tenant_id = j.tenant_id and g.journal_id = j.id
			where j.tenant_id = $1 and j.legal_entity_id = $2 and j.status = 'POSTED'
				and jsonb_array_length(coalesce(j.gl_entry->'lines', '[]'::jsonb)) > 0
				and (g.journal_id is null or (g.status = 'PENDING' and g.next_attempt_at <= now()))
			order by j.posted_at, j.id limit $3`, tenantID, legalEntityID, MaxGLPostingsPerRun+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c glCandidate
			var raw string
			if err := rows.Scan(&c.journalID, &c.date, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &c.entry); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, parked, err
}

// stage writes the posting row unless the journal has one and returns its status and confirmed entry id.
func (s *Postgres) stage(ctx context.Context, tenantID, legalEntityID, ledgerID, journalID string) (status, entryID string, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into inventory_gl_posting (tenant_id, journal_id, legal_entity_id, gl_ledger_id, idempotency_key)
			values ($1, $2, $3, $4, $5) on conflict do nothing`, tenantID, journalID, legalEntityID, ledgerID, glKeyPrefix+journalID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select status, coalesce(journal_entry_id, '') from inventory_gl_posting where tenant_id = $1 and journal_id = $2`,
			tenantID, journalID).Scan(&status, &entryID)
	})
	return status, entryID, err
}

// markBooked records the entry the ledger confirmed; the ledger's answer wins over a concurrent park.
func (s *Postgres) markBooked(ctx context.Context, tenantID, journalID, entryID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update inventory_gl_posting set status = 'POSTED', journal_entry_id = $3, parked_reason = null, posted_at = now()
			where tenant_id = $1 and journal_id = $2 and status <> 'POSTED'`, tenantID, journalID, entryID)
		return err
	})
}

// recordFailure counts an attempt and backs off (60 s doubling, capped at an hour); park, or the last allowed attempt, parks the row.
func (s *Postgres) recordFailure(ctx context.Context, tenantID, journalID, code string, park bool) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update inventory_gl_posting set attempt_count = attempt_count + 1, last_error = $3,
				next_attempt_at = now() + make_interval(secs => least($6, $5 * power(2, attempt_count))::double precision),
				status = case when $4 or attempt_count + 1 >= $7 then 'PARKED' else status end,
				parked_reason = case when $4 or attempt_count + 1 >= $7 then $3 else parked_reason end
			where tenant_id = $1 and journal_id = $2 and status = 'PENDING'`, tenantID, journalID, code, park, glBackoffSecs, glBackoffLimit, MaxGLAttempts)
		return err
	})
}

// glCode is the code reported for a failed posting: the ledger's own code, this service's problem code, or INV-GL-001.
func glCode(err error) string {
	if code := ledgerclient.CodeOf(err); code != "" {
		return code
	}
	var hp *httpx.Problem
	if errors.As(err, &hp) && hp.Code != "" {
		return hp.Code
	}
	return CodeGLFailed
}

// book sends one journal's entry: a journal the ledger already confirmed returns its entry id, a parked one is skipped, and a
// pending one is sent under its fixed key, so a retry after a lost answer gets back the entry the ledger already holds.
func (s *Postgres) book(ctx context.Context, ledger ledgerclient.Ledger, tenantID string, in GLPostRequest, c glCandidate) (string, int, error) {
	status, entryID, err := s.stage(ctx, tenantID, in.LegalEntityID, in.LedgerID, c.journalID)
	if err != nil {
		return "", 0, err
	}
	entry := c.ledgerEntry(in.LegalEntityID, in.LedgerID)
	switch status {
	case glPosted:
		return entryID, len(entry.Lines), nil
	case glParked:
		return "", 0, problem(http.StatusConflict, CodeGLFailed, "the posting is parked; requeue it to retry")
	}
	if err := entry.Validate(); err != nil {
		return "", 0, errors.Join(problem(http.StatusUnprocessableEntity, CodeGLUnbalanced, "the stored GL entry cannot be posted: "+err.Error()),
			s.recordFailure(ctx, tenantID, c.journalID, CodeGLUnbalanced, true))
	}
	entryID, err = ledger.Post(ctx, entry)
	if err != nil {
		code := glCode(err)
		// A refusal the ledger repeats on every retry parks at once; a closed period, a conflict or no answer backs off.
		park := ledgerclient.Rejected(err) && code != ledgerclient.CodePeriodClosed
		if rerr := s.recordFailure(ctx, tenantID, c.journalID, code, park); rerr != nil {
			slog.ErrorContext(ctx, "inventory GL failure not recorded", "journal", c.journalID, "err", rerr, "correlationId", middleware.GetReqID(ctx))
		}
		return "", 0, err
	}
	return entryID, len(entry.Lines), s.markBooked(ctx, tenantID, c.journalID, entryID)
}

// PostToGL books, in the legal entity's general ledger, the stored GL entry of every POSTED journal of the legal entity that no
// confirmed posting covers, one ledger entry per journal keyed by the journal id. The ledger must belong to the legal entity; the
// first sweep pins it and later sweeps must name the same one. Only postings that are due are tried, so one that keeps failing
// waits out its backoff and is parked instead of holding back the others; a journal the ledger refuses is reported and the rest
// go on. Journals whose entry is empty (TRANSFER, BOM, vendor-owned counts) have nothing to book and are not swept.
func (s *Postgres) PostToGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return GLPostResult{}, errors.New("no principal in context")
	}
	in.LedgerID = strings.ToLower(in.LedgerID)
	pinned, err := s.pinnedLedger(ctx, p.TenantID, in.LegalEntityID)
	if err != nil {
		return GLPostResult{}, err
	}
	if pinned != "" && pinned != in.LedgerID {
		return GLPostResult{}, pinnedElsewhere()
	}
	if err := checkLedgerOwner(ctx, ledger, in.LegalEntityID, in.LedgerID); err != nil {
		return GLPostResult{}, err
	}
	if pinned == "" {
		if pinned, err = s.pinLedger(ctx, p, in.LegalEntityID, in.LedgerID); err != nil {
			return GLPostResult{}, err
		}
		if pinned != in.LedgerID {
			return GLPostResult{}, pinnedElsewhere()
		}
	}
	candidates, parked, err := s.unbookedJournals(ctx, p.TenantID, in.LegalEntityID)
	if err != nil {
		return GLPostResult{}, err
	}
	res := GLPostResult{LedgerID: pinned, Booked: []GLBooked{}, Failed: []GLFailed{}, Parked: parked}
	if len(candidates) > MaxGLPostingsPerRun {
		res.More, candidates = true, candidates[:MaxGLPostingsPerRun]
	}
	for _, c := range candidates {
		entryID, lines, err := s.book(ctx, ledger, p.TenantID, in, c)
		if err != nil {
			code := glCode(err)
			slog.ErrorContext(ctx, "inventory GL posting failed", "journal", c.journalID, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
			res.Failed = append(res.Failed, GLFailed{JournalID: c.journalID, Code: code})
			continue
		}
		res.Booked = append(res.Booked, GLBooked{JournalID: c.journalID, EntryID: entryID, Lines: lines})
	}
	return res, nil
}

// RequeueParkedGL makes the legal entity's parked postings due again with a fresh attempt count. They are resent under their own
// key, because the ledger may already hold them.
func (s *Postgres) RequeueParkedGL(ctx context.Context, legalEntityID string) (int, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, errors.New("no principal in context")
	}
	var n int64
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update inventory_gl_posting set status = 'PENDING', attempt_count = 0, parked_reason = null, next_attempt_at = now()
			where tenant_id = $1 and legal_entity_id = $2 and status = 'PARKED'`, p.TenantID, legalEntityID)
		n = tag.RowsAffected()
		return err
	})
	return int(n), err
}
