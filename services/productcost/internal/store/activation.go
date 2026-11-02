package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	costevents "github.com/BenjaminSRussell/passage/services/productcost/internal/events"
)

// Activation is a stored standard cost activation: the revaluation of the
// on-hand positions of one version and the ledger journal that booked it. A
// reversal is another activation that names the one it reverses.
type Activation struct {
	ID                       string                    `json:"id"`
	VersionID                string                    `json:"costingVersionId"`
	LegalEntityID            string                    `json:"legalEntityId"`
	ReversesActivationID     *string                   `json:"reversesActivationId,omitempty"`
	LedgerID                 string                    `json:"ledgerId"`
	PostingDate              string                    `json:"postingDate"`
	Currency                 string                    `json:"currency"`
	ItemCount                int                       `json:"itemCount"`
	Total                    decimal.Decimal           `json:"total"`
	JournalEntryID           string                    `json:"journalEntryId,omitempty"`
	NewStandardForOpenOrders bool                      `json:"newStandardForOpenOrders"`
	Lines                    []domain.RevalPreviewLine `json:"lines"`
	ActivatedBy              string                    `json:"activatedBy"`
	CreatedAt                time.Time                 `json:"createdAt"`
}

// ActivationResult is the version after the activation and the activation.
type ActivationResult struct {
	Version    Version    `json:"version"`
	Activation Activation `json:"activation"`
}

// ActivationInput is one activation or reversal. Fingerprint is what the
// Idempotency-Key hashes (the caller's request, not the quantities read for
// it). Post books the journal and returns its entry id; the store calls it
// once, inside the transaction, so a refused journal leaves the version as it
// was.
type ActivationInput struct {
	VersionID                string
	Expected                 int
	Key                      string
	Fingerprint              any
	ReversesID               string
	LedgerID                 string
	PostingDate              string
	Currency                 string
	NewStandardForOpenOrders bool
	Preview                  domain.RevalPreview
	Post                     func(ctx context.Context, activationID string) (string, error)
}

const activationColumns = `id::text, costing_version_id::text, legal_entity_id::text, reverses_activation_id::text, ledger_id::text,
  to_char(posting_date, 'YYYY-MM-DD'), currency, item_count, total, coalesce(journal_entry_id, ''), new_standard_for_open_orders,
  lines, activated_by, created_at`

func scanActivation(row pgx.Row) (Activation, error) {
	var a Activation
	var lines []byte
	err := row.Scan(&a.ID, &a.VersionID, &a.LegalEntityID, &a.ReversesActivationID, &a.LedgerID, &a.PostingDate, &a.Currency,
		&a.ItemCount, &a.Total, &a.JournalEntryID, &a.NewStandardForOpenOrders, &lines, &a.ActivatedBy, &a.CreatedAt)
	if err != nil {
		return a, err
	}
	return a, json.Unmarshal(lines, &a.Lines)
}

func activationNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeReversalTarget, "activation "+id+" not found")
}

// GetActivation returns one stored activation, or CST-0723 (404).
func (s *Postgres) GetActivation(ctx context.Context, id string) (Activation, error) {
	var out Activation
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		a, err := scanActivation(tx.QueryRow(ctx, `select `+activationColumns+` from standard_cost_activation where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return activationNotFound(id)
		}
		out = a
		return err
	})
	return out, err
}

// ActivateVersion activates a version with its revaluation, or reverses an
// earlier activation, atomically: the version row is locked, the counter must
// equal Expected (CST-0714), no roll of the version may hold its advisory lock
// (CST-0710, BR-39.7), the journal is posted through Post, and the activation,
// the version change and the outbox events commit together. A reversal needs an
// Active version and an activation of it that nothing reversed yet (CST-0723).
func (s *Postgres) ActivateVersion(ctx context.Context, in ActivationInput) (ActivationResult, bool, error) {
	var out ActivationResult
	var replayed bool
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, p.TenantID, in.Key, "version.activate", in.Fingerprint, func() (ActivationResult, error) {
			return activateInTx(ctx, tx, p, in)
		})
		return err
	})
	return out, replayed, err
}

func activateInTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ActivationInput) (ActivationResult, error) {
	var res ActivationResult
	cur, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and id = $2 for update`, p.TenantID, in.VersionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, versionNotFound(in.VersionID)
	}
	if err != nil {
		return res, err
	}
	if cur.Version != in.Expected {
		return res, httpx.NewProblem(http.StatusConflict, domain.CodeVersionConflict, "current version is "+strconv.Itoa(cur.Version))
	}
	var free bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtextextended($1, 0))`, domain.RollLockKey(p.TenantID, in.VersionID)).Scan(&free); err != nil {
		return res, err
	}
	if !free {
		return res, httpx.NewProblem(http.StatusConflict, domain.CodeVersionRolling, "version "+cur.Code+" is being rolled; activation blocked")
	}
	reversing := in.ReversesID != ""
	if reversing {
		if err := checkReversal(ctx, tx, p.TenantID, cur, in.ReversesID); err != nil {
			return res, err
		}
	} else if _, err := domain.Transition(cur.Lifecycle(), domain.ActionActivate); err != nil {
		return res, err
	}
	next, err := scanVersion(tx.QueryRow(ctx, `update costing_version
		set status = case when $3 then status else 'Active' end, version = version + 1,
		    activated_at = case when $3 then activated_at else now() end
		where tenant_id = $1 and id = $2 returning `+versionColumns, p.TenantID, in.VersionID, reversing))
	if err != nil {
		return res, err
	}
	activationID := ids.New()
	journalID := ""
	if !in.Preview.Total.IsZero() {
		if journalID, err = in.Post(ctx, activationID); err != nil {
			return res, err
		}
	}
	lines, err := json.Marshal(in.Preview.Lines)
	if err != nil {
		return res, err
	}
	a := Activation{ID: activationID, VersionID: cur.ID, LegalEntityID: cur.LegalEntityID, ReversesActivationID: nullable(in.ReversesID), LedgerID: in.LedgerID,
		PostingDate: in.PostingDate, Currency: in.Currency, ItemCount: len(in.Preview.Lines), Total: in.Preview.Total, JournalEntryID: journalID,
		NewStandardForOpenOrders: in.NewStandardForOpenOrders, Lines: in.Preview.Lines, ActivatedBy: p.UserID}
	err = tx.QueryRow(ctx, `insert into standard_cost_activation
		(tenant_id, id, legal_entity_id, costing_version_id, reverses_activation_id, ledger_id, posting_date, currency, item_count, total,
		 journal_entry_id, new_standard_for_open_orders, lines, activated_by)
		values ($1, $2, $3, $4, $5::uuid, $6, $7::date, $8, $9, $10, $11, $12, $13, $14) returning created_at`,
		p.TenantID, a.ID, a.LegalEntityID, a.VersionID, a.ReversesActivationID, a.LedgerID, a.PostingDate, a.Currency, a.ItemCount, a.Total,
		nullable(journalID), a.NewStandardForOpenOrders, lines, a.ActivatedBy).Scan(&a.CreatedAt)
	if pgCode(err) == uniqueViolation {
		return res, httpx.NewProblem(http.StatusConflict, domain.CodeReversalTarget, "activation "+in.ReversesID+" was already reversed")
	}
	if err != nil {
		return res, err
	}
	if !reversing {
		if err := enqueue(ctx, tx, "costingversion", next.ID, costevents.VersionTransitioned, p, costevents.VersionTransitionedData{
			ID: next.ID, LegalEntityID: next.LegalEntityID, Code: next.Code, Type: next.Type, Action: domain.ActionActivate,
			FromStatus: cur.Status, ToStatus: next.Status, Version: next.Version, ActivatedAt: next.ActivatedAt, Actor: p.UserID,
		}); err != nil {
			return res, err
		}
	}
	err = enqueue(ctx, tx, "standardcostactivation", a.ID, costevents.StandardCostActivated, p, costevents.StandardCostActivatedData{
		ActivationID: a.ID, CostingVersionID: a.VersionID, LegalEntityID: a.LegalEntityID, ReversesActivationID: in.ReversesID, ItemCount: a.ItemCount,
		RevaluationJournalID: journalID, TotalRevaluation: a.Total.String(), Currency: a.Currency, PostingDate: a.PostingDate,
		NewStandardForOpenOrders: a.NewStandardForOpenOrders, Actor: p.UserID,
	})
	return ActivationResult{Version: next, Activation: a}, err
}

func checkReversal(ctx context.Context, tx pgx.Tx, tenantID string, cur Version, reversesID string) error {
	if cur.Status != domain.VersionActive {
		return &domain.CodedError{Code: domain.CodeReversalTarget, Detail: "only an Active version's activation can be reversed"}
	}
	var one int
	err := tx.QueryRow(ctx, `select 1 from standard_cost_activation where tenant_id = $1 and id = $2 and costing_version_id = $3`, tenantID, reversesID, cur.ID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return &domain.CodedError{Code: domain.CodeReversalTarget, Detail: "activation " + reversesID + " is not an activation of this version"}
	}
	return err
}
