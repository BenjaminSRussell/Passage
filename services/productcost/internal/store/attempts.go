package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	costevents "github.com/BenjaminSRussell/passage/services/productcost/internal/events"
)

const (
	OutcomePosted    = "posted"
	OutcomeNotPosted = "not-posted"

	resolveScope = "version.resolve-attempt"
)

// pastReplayWindow is true for an attempt older than the ledger replay window:
// the ledger idempotency retention the attempt was created under (or the
// configured one, if lower) less the safety margin. prepareActivation and
// recordFailedAttempt refuse to resend or clear such an attempt (CST-0730); an
// operator settles it through ResolveActivationAttempt. ttl and margin are the
// placeholders of the configured retention in days and of the margin.
func pastReplayWindow(ttl, margin string) string {
	return `created_at <= now() - make_interval(days => least(ledger_idempotency_ttl_days, ` + ttl + `) - ` + margin + `)`
}

// Attempt is one pending activation attempt: the journal sent, or about to be
// sent, to the ledger and not yet recorded as an activation.
type Attempt struct {
	VersionID            string          `json:"costingVersionId"`
	LegalEntityID        string          `json:"legalEntityId"`
	ActivationID         string          `json:"activationId"`
	ReversesActivationID *string         `json:"reversesActivationId,omitempty"`
	Total                decimal.Decimal `json:"total"`
	Currency             string          `json:"currency"`
	Ambiguous            bool            `json:"ambiguous"`
	NeedsOperator        bool            `json:"needsOperator"`
	CreatedAt            time.Time       `json:"createdAt"`
}

// ListActivationAttempts returns the pending attempts in the caller's scope,
// oldest first, each flagged when it is past the ledger replay window.
func (s *Postgres) ListActivationAttempts(ctx context.Context) ([]Attempt, error) {
	out := []Attempt{}
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		rows, err := tx.Query(ctx, `select costing_version_id::text, legal_entity_id::text, payload, attempted_ambiguous, created_at, `+pastReplayWindow("$2", "$3")+`
			from standard_cost_activation_attempt where tenant_id = $1 order by created_at, attempt_key limit 500`,
			p.TenantID, s.ledgerIdempotencyTTL, domain.LedgerReplaySafetyMarginDays)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Attempt
			var raw []byte
			if err := rows.Scan(&a.VersionID, &a.LegalEntityID, &raw, &a.Ambiguous, &a.CreatedAt, &a.NeedsOperator); err != nil {
				return err
			}
			var pend pendingActivation
			if err := json.Unmarshal(raw, &pend); err != nil {
				return err
			}
			a.ActivationID, a.ReversesActivationID = pend.Activation.ID, pend.Activation.ReversesActivationID
			a.Total, a.Currency = pend.Activation.Total, pend.Activation.Currency
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

type ResolveInput struct {
	VersionID      string
	Expected       int
	ReversesID     string
	Outcome        string
	JournalEntryID string
	Reason         string
	Key            string
	Fingerprint    any
}

type ResolveResult struct {
	Outcome    string      `json:"outcome"`
	Version    Version     `json:"version"`
	Activation *Activation `json:"activation,omitempty"`
}

// ResolveActivationAttempt records an operator's decision on an attempt past the
// ledger replay window. The operator has checked the ledger: "posted" names the entry that
// landed and the activation is recorded around it, as if the post had just
// returned; "not-posted" discards the attempt so the version can be activated
// afresh. An attempt inside the window is refused: a retry of the activation
// resolves it by itself.
func (s *Postgres) ResolveActivationAttempt(ctx context.Context, in ResolveInput) (ResolveResult, bool, error) {
	var out ResolveResult
	var replayed bool
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		var done bool
		if out, done, err = storedReplay[ResolveResult](ctx, tx, p.TenantID, in.Key, resolveScope, in.Fingerprint); err != nil || done {
			replayed = done
			return err
		}
		out, replayed, err = idempotent(ctx, tx, p.TenantID, in.Key, resolveScope, in.Fingerprint, func() (ResolveResult, error) {
			return resolveAttempt(ctx, tx, p, in, s.ledgerIdempotencyTTL)
		})
		return err
	})
	return out, replayed, err
}

func resolveAttempt(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ResolveInput, ttlDays int) (ResolveResult, error) {
	var res ResolveResult
	act := ActivationInput{VersionID: in.VersionID, Expected: in.Expected, ReversesID: in.ReversesID}
	cur, err := lockVersion(ctx, tx, p.TenantID, act)
	if err != nil {
		return res, err
	}
	key := attemptKey(cur.ID, act)
	var raw []byte
	var expired bool
	err = tx.QueryRow(ctx, `select payload, `+pastReplayWindow("$3", "$4")+`
		from standard_cost_activation_attempt where tenant_id = $1 and attempt_key = $2 for update`,
		p.TenantID, key, ttlDays, domain.LedgerReplaySafetyMarginDays).Scan(&raw, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, httpx.NewProblem(http.StatusNotFound, domain.CodeAttemptNotFound, "version "+cur.ID+" has no pending activation attempt")
	}
	if err != nil {
		return res, err
	}
	if !expired {
		return res, httpx.NewProblem(http.StatusConflict, domain.CodeAttemptNotExpired,
			"the attempt is inside the ledger replay window; retry the activation and it settles on its own")
	}
	var pend pendingActivation
	if err := json.Unmarshal(raw, &pend); err != nil {
		return res, err
	}
	data := costevents.ActivationAttemptResolvedData{CostingVersionID: cur.ID, LegalEntityID: cur.LegalEntityID, ActivationID: pend.Activation.ID,
		ReversesActivationID: in.ReversesID, Outcome: in.Outcome, JournalEntryID: in.JournalEntryID, Reason: in.Reason, Actor: p.UserID}
	res.Outcome = in.Outcome
	switch in.Outcome {
	case OutcomePosted:
		if !ids.Valid(in.JournalEntryID) {
			return res, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "journalEntryId must be the UUID of the ledger entry")
		}
		if in.ReversesID == "" {
			// The same four-eyes rule as an activation: the roller does not confirm it.
			if err := refuseRollerActivating(ctx, tx, p, cur); err != nil {
				return res, err
			}
		}
		pend.Activation.JournalEntryID = in.JournalEntryID
		done, err := finalizeActivation(ctx, tx, p, act, preparedActivation{cur: cur, pending: pend, post: true})
		if err != nil {
			return res, err
		}
		res.Version, res.Activation = done.Version, &done.Activation
	case OutcomeNotPosted:
		if _, err := tx.Exec(ctx, `delete from standard_cost_activation_attempt where tenant_id = $1 and attempt_key = $2`, p.TenantID, key); err != nil {
			return res, err
		}
		res.Version = cur
	default:
		return res, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "outcome must be posted or not-posted")
	}
	return res, enqueue(ctx, tx, "activationattempt", pend.Activation.ID, costevents.ActivationAttemptResolved, p, data)
}
