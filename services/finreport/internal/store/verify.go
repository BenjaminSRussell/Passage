package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// CodeVerificationNotFound is a slice-local code (not in the PRD catalog yet).
const CodeVerificationNotFound = "REPORT-0035"

// MaxVerificationPage is the most verifications one list call returns.
const MaxVerificationPage = 200

// VerifySnapshot is what the read model holds for a ledger, taken in one
// statement: the ledger's high-water mark, the entries waiting ahead of it and
// the net per account in the accounting currency over every period.
// LegalEntityID is the entity that owns the ledger's read-model rows (empty for
// a legacy ledger).
type VerifySnapshot struct {
	LegalEntityID  string
	HWM            int64
	EntriesPending int
	Nets           map[string]decimal.Decimal
}

// VerificationSummary is one stored check without its differing accounts, as
// the list serves it.
type VerificationSummary struct {
	ID               string    `json:"id"`
	LedgerID         string    `json:"ledgerId"`
	LegalEntityID    string    `json:"legalEntityId,omitempty"`
	Status           string    `json:"status"`
	HWMBefore        int64     `json:"hwmBefore"`
	HWMAfter         int64     `json:"hwmAfter"`
	EntriesPending   int       `json:"entriesPending"`
	AccountsCompared int       `json:"accountsCompared"`
	DiffCount        int       `json:"diffCount"`
	RequestedBy      string    `json:"requestedBy"`
	VerifiedAt       time.Time `json:"verifiedAt"`
}

// Verification is one stored check of the read model against the ledger.
// DiffsWithheld is set by the API when the caller may not see ledger balances:
// Diffs is then empty.
type Verification struct {
	VerificationSummary
	Diffs         []VerificationDiff `json:"diffs"`
	DiffsWithheld bool               `json:"diffsWithheld,omitempty"`
	Replayed      bool               `json:"-"`
}

// VerificationDiff is one differing account as stored and served (amounts as
// six-place decimal strings).
type VerificationDiff struct {
	Account    string `json:"account"`
	ReadModel  string `json:"readModel"`
	Ledger     string `json:"ledger"`
	Difference string `json:"difference"`
}

// VerificationSpec is a result to record. RequestHash and IdempotencyKey make
// a retried request return the stored check.
type VerificationSpec struct {
	LedgerID       string
	LegalEntityID  string
	Result         domain.VerifyResult
	RequestedBy    string
	IdempotencyKey string
	RequestHash    string
}

// VerifyReplay returns the stored check for an Idempotency-Key already used, if
// any, so a retry does not read the ledger again. A key used for a different
// request is refused (idempotency_key_reuse).
func (s *Postgres) VerifyReplay(ctx context.Context, key, requestHash string) (Verification, bool, error) {
	var v Verification
	var found bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, verificationSelect+` where idempotency_key=$1`, key)
		var hash string
		var err error
		v, hash, err = scanVerification(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if hash != requestHash {
			return problem(422, CodeIdempotencyReuse, "Idempotency-Key was used for a different request")
		}
		found = true
		v.Replayed = true
		return nil
	})
	return v, found, err
}

// VerifySnapshot reads the ledger's read-model state in one statement (the
// mark, the entries waiting ahead of it and the nets agree with each other,
// because the consumer moves all three in one transaction). A ledger the
// caller sees no read-model rows of is 404 REPORT-0031.
func (s *Postgres) VerifySnapshot(ctx context.Context, ledgerID, requestedEntity string) (VerifySnapshot, error) {
	snap := VerifySnapshot{Nets: map[string]decimal.Decimal{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		entity, err := requireLedgerEntityTx(ctx, tx, ledgerID, requestedEntity)
		if err != nil {
			return err
		}
		snap.LegalEntityID = entity
		rows, err := tx.Query(ctx, `select h.posting_sequence,
				(select count(*) from reporting_entry_pending w where w.ledger_id = p.ledger_id)::integer,
				b.account, b.net::text
			from (select $1::uuid as ledger_id) p
			left join reporting_hwm h on h.ledger_id = p.ledger_id
			left join (select account, sum(net) as net from reporting_balance
				where ledger_id = $1::uuid and currency_type = 'acct' group by account) b on true`, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hwm *int64
			var account, net *string
			if err := rows.Scan(&hwm, &snap.EntriesPending, &account, &net); err != nil {
				return err
			}
			if hwm != nil {
				snap.HWM = *hwm
			}
			if account == nil {
				continue
			}
			n, err := decimal.NewFromString(*net)
			if err != nil {
				return err
			}
			snap.Nets[*account] = n
		}
		return rows.Err()
	})
	return snap, err
}

// Mark returns the ledger's read-model high-water mark (0 before the first
// entry) and the number of its entries waiting ahead of the mark, in one statement.
func (s *Postgres) Mark(ctx context.Context, ledgerID string) (hwm int64, pending int, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select coalesce((select posting_sequence from reporting_hwm where ledger_id = $1::uuid), 0),
			(select count(*) from reporting_entry_pending where ledger_id = $1::uuid)::integer`, ledgerID).Scan(&hwm, &pending)
	})
	return hwm, pending, err
}

// RecordVerification stores a check and writes balance.verified.v1 to the
// outbox in the same transaction. A repeated Idempotency-Key with the same
// request returns the stored check; a different request is refused. Two
// concurrent requests with one key store one check: the loser of the insert
// returns the winner's check and writes no event.
func (s *Postgres) RecordVerification(ctx context.Context, spec VerificationSpec) (Verification, error) {
	var out Verification
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		diffs := make([]VerificationDiff, 0, len(spec.Result.Diffs))
		for _, d := range spec.Result.Diffs {
			diffs = append(diffs, VerificationDiff{Account: d.Account, ReadModel: d.ReadModel.StringFixed(6),
				Ledger: d.Ledger.StringFixed(6), Difference: d.Difference.StringFixed(6)})
		}
		body, err := json.Marshal(diffs)
		if err != nil {
			return err
		}
		out = Verification{VerificationSummary: VerificationSummary{ID: ids.New(), LedgerID: spec.LedgerID, LegalEntityID: spec.LegalEntityID,
			Status: spec.Result.Status, HWMBefore: spec.Result.HWMBefore, HWMAfter: spec.Result.HWMAfter, EntriesPending: spec.Result.EntriesPending,
			AccountsCompared: spec.Result.AccountsCompared, DiffCount: spec.Result.DiffCount, RequestedBy: spec.RequestedBy}, Diffs: diffs}
		err = tx.QueryRow(ctx, `insert into balance_verification(tenant_id, id, ledger_id, legal_entity_id, status, hwm_before, hwm_after,
			entries_pending, accounts_compared, diff_count, diffs, requested_by, request_hash, idempotency_key)
			values($1,$2,$3,nullif($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			on conflict (tenant_id, idempotency_key) do nothing returning verified_at`,
			tenantID(ctx), out.ID, spec.LedgerID, spec.LegalEntityID, out.Status, out.HWMBefore, out.HWMAfter, out.EntriesPending,
			out.AccountsCompared, out.DiffCount, body, spec.RequestedBy, spec.RequestHash, spec.IdempotencyKey).Scan(&out.VerifiedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			var hash string
			out, hash, err = scanVerification(tx.QueryRow(ctx, verificationSelect+` where idempotency_key=$1`, spec.IdempotencyKey))
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// No row visible: the key holds a check of a legal entity outside the caller's scope.
			if err != nil || hash != spec.RequestHash {
				return problem(422, CodeIdempotencyReuse, "Idempotency-Key was used for a different request")
			}
			out.Replayed = true
			return nil
		}
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "balance_verification", out.ID, events.BalanceVerified, tenantID(ctx), events.BalanceVerifiedData{
			VerificationID: out.ID, LedgerID: spec.LedgerID, LegalEntityID: spec.LegalEntityID, Status: out.Status,
			HWMBefore: out.HWMBefore, HWMAfter: out.HWMAfter, EntriesPending: out.EntriesPending,
			AccountsCompared: out.AccountsCompared, DiffCount: out.DiffCount, Actor: spec.RequestedBy,
		})
	})
	return out, err
}

// ListVerifications returns the checks of a ledger (all ledgers when ledgerID
// is empty), newest first, at most limit, without their differing accounts.
func (s *Postgres) ListVerifications(ctx context.Context, ledgerID string, limit int) ([]VerificationSummary, error) {
	out := []VerificationSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+summaryColumns+` from balance_verification
			where ($1 = '' or ledger_id::text = $1) order by verified_at desc, id desc limit $2`, ledgerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanSummary(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// GetVerification returns one stored check.
func (s *Postgres) GetVerification(ctx context.Context, id string) (Verification, error) {
	var v Verification
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		v, _, err = scanVerification(tx.QueryRow(ctx, verificationSelect+` where id=$1::uuid`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeVerificationNotFound, fmt.Sprintf("balance verification %s does not exist", id))
		}
		return err
	})
	return v, err
}

const summaryColumns = `id::text, ledger_id::text, coalesce(legal_entity_id::text,''), status, hwm_before, hwm_after, entries_pending,
	accounts_compared, diff_count, requested_by, verified_at`

const verificationSelect = `select ` + summaryColumns + `, diffs, request_hash from balance_verification`

func summaryFields(v *VerificationSummary) []any {
	return []any{&v.ID, &v.LedgerID, &v.LegalEntityID, &v.Status, &v.HWMBefore, &v.HWMAfter, &v.EntriesPending,
		&v.AccountsCompared, &v.DiffCount, &v.RequestedBy, &v.VerifiedAt}
}

func scanSummary(r rowScanner) (VerificationSummary, error) {
	var v VerificationSummary
	return v, r.Scan(summaryFields(&v)...)
}

func scanVerification(r rowScanner) (Verification, string, error) {
	var v Verification
	var diffs []byte
	var hash string
	if err := r.Scan(append(summaryFields(&v.VerificationSummary), &diffs, &hash)...); err != nil {
		return v, "", err
	}
	if err := json.Unmarshal(diffs, &v.Diffs); err != nil {
		return v, "", err
	}
	if v.Diffs == nil {
		v.Diffs = []VerificationDiff{}
	}
	return v, hash, nil
}
