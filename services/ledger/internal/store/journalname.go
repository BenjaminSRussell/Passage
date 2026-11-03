package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// JournalName is the setup of a kind of manual journal (FR-11.9, AX
// LedgerJournalName): whether its batches need approval, whether the submitter
// may approve, whether reversing their entries needs a reason (FR-11.15),
// whether lines may hit reconciliation accounts (BR-11.8), and the lines limit.
// Names are deactivated, never deleted, because batches reference them.
type JournalName struct {
	Code                   string `json:"code"`
	Name                   string `json:"name"`
	RequiresApproval       bool   `json:"requiresApproval"`
	SelfApprovalAllowed    bool   `json:"selfApprovalAllowed"`
	ReversalReasonRequired bool   `json:"reversalReasonRequired"`
	AllowReconAccounts     bool   `json:"allowReconAccounts"`
	LinesLimit             int    `json:"linesLimit"`
	// PostingRestriction is who may post the journal name's batches: "none" (any holder of the
	// posting duty) or "own_journals_only" (only the batch's creator). DescriptionLanguage is where
	// default descriptions take their language from: "legal_entity" or "user" (FR-11.17).
	PostingRestriction  string    `json:"postingRestriction"`
	DescriptionLanguage string    `json:"descriptionLanguage"`
	Active              bool      `json:"active"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

const journalNameColumns = `code, name, requires_approval, self_approval_allowed, reversal_reason_required,
	allow_recon_accounts, lines_limit, active, updated_at, posting_restriction, description_language`

func scanJournalName(row pgx.Row, extra ...any) (JournalName, error) {
	var n JournalName
	err := row.Scan(append([]any{&n.Code, &n.Name, &n.RequiresApproval, &n.SelfApprovalAllowed, &n.ReversalReasonRequired,
		&n.AllowReconAccounts, &n.LinesLimit, &n.Active, &n.UpdatedAt, &n.PostingRestriction, &n.DescriptionLanguage}, extra...)...)
	return n, err
}

// ListJournalNames returns every journal name of the tenant ordered by code.
func (s *Postgres) ListJournalNames(ctx context.Context) ([]JournalName, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []JournalName{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+journalNameColumns+` from journal_name where tenant_id = $1 order by code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanJournalName(rows)
			if err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

// GetJournalName returns one journal name or 404 LEDGER_JOURNAL_NAME_NOT_FOUND.
func (s *Postgres) GetJournalName(ctx context.Context, code string) (JournalName, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return JournalName{}, err
	}
	var out JournalName
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := scanJournalName(tx.QueryRow(ctx, `select `+journalNameColumns+` from journal_name where tenant_id = $1 and code = $2`,
			p.TenantID, code))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "LEDGER_JOURNAL_NAME_NOT_FOUND", "journal name not found")
		}
		out = n
		return err
	})
	return out, err
}

// PutJournalName creates or replaces a journal name. created reports whether
// the name is new, so the API can answer 201 or 200. A change applies to the
// next action on each batch: submit reads the workflow flag, approve the
// self-approval flag, post the reconciliation override.
func (s *Postgres) PutJournalName(ctx context.Context, code string, in JournalName) (JournalName, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return JournalName{}, false, err
	}
	if in.PostingRestriction == "" {
		in.PostingRestriction = domain.PostingNone
	}
	if in.DescriptionLanguage == "" {
		in.DescriptionLanguage = domain.DescriptionLanguageLE
	}
	var out JournalName
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `insert into journal_name (tenant_id, code, name, requires_approval, self_approval_allowed,
				reversal_reason_required, allow_recon_accounts, lines_limit, active, posting_restriction, description_language)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			on conflict (tenant_id, code) do update set name = excluded.name, requires_approval = excluded.requires_approval,
				self_approval_allowed = excluded.self_approval_allowed, reversal_reason_required = excluded.reversal_reason_required,
				allow_recon_accounts = excluded.allow_recon_accounts, lines_limit = excluded.lines_limit,
				active = excluded.active, posting_restriction = excluded.posting_restriction,
				description_language = excluded.description_language, updated_at = now()
			returning `+journalNameColumns+`, (xmax = 0)`, p.TenantID, code, in.Name, in.RequiresApproval,
			in.SelfApprovalAllowed, in.ReversalReasonRequired, in.AllowReconAccounts, in.LinesLimit, in.Active,
			in.PostingRestriction, in.DescriptionLanguage)
		var scanErr error
		out, scanErr = scanJournalName(row, &created)
		return scanErr
	})
	if err != nil {
		return JournalName{}, false, err
	}
	return out, created, nil
}

// loadJournalNameTx reads a journal name for a batch action, or nil when the
// code is unknown.
func loadJournalNameTx(ctx context.Context, tx pgx.Tx, tenantID, code string) (*JournalName, error) {
	n, err := scanJournalName(tx.QueryRow(ctx, `select `+journalNameColumns+` from journal_name where tenant_id = $1 and code = $2`,
		tenantID, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// journalNameRequiresReason reports whether the entry was posted from a batch
// whose journal name sets reversal_reason_required (FR-11.15).
func journalNameRequiresReason(ctx context.Context, tx pgx.Tx, tenantID, entryID string) (bool, error) {
	var required bool
	err := tx.QueryRow(ctx, `select n.reversal_reason_required
		from journal_batch b join journal_name n on n.tenant_id = b.tenant_id and n.code = b.journal_name
		where b.tenant_id = $1 and b.posted_entry_id = $2`, tenantID, entryID).Scan(&required)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return required, err
}
