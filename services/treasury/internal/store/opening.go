package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Opening bank balances (#49, H-S10-1).
const (
	CodeOpeningInvalid     = "TRS-0601"
	CodeOpeningReference   = "TRS-0602"
	CodeOpeningKeyConflict = "TRS-0603"
	CodeOpeningAccountHeld = "TRS-0604"
)

// OpeningItem is one account's opening book balance in minor units of its currency.
type OpeningItem struct {
	Reference     string `json:"reference"`
	BankAccountID string `json:"bankAccountId"`
	Currency      string `json:"currency"`
	BalanceMinor  int64  `json:"balanceMinor"`
}

// OpeningImport is a validated batch.
type OpeningImport struct {
	LegalEntityID string
	BatchKey      string
	AsOf          string
	Items         []OpeningItem
}

// OpeningBatch is a stored batch with its total per currency.
type OpeningBatch struct {
	LegalEntityID string           `json:"legalEntityId"`
	BatchKey      string           `json:"batchKey"`
	AsOf          string           `json:"asOf"`
	ItemCount     int              `json:"itemCount"`
	TotalsMinor   map[string]int64 `json:"totalsMinor"`
}

func (in OpeningImport) hash() string {
	rows := make([]string, len(in.Items))
	for i, it := range in.Items {
		rows[i] = fmt.Sprintf("%s\x1f%s\x1f%s\x1f%d", it.Reference, strings.ToLower(it.BankAccountID), it.Currency, it.BalanceMinor)
	}
	sort.Strings(rows)
	h := sha256.New()
	for _, p := range append([]string{strings.ToLower(in.LegalEntityID), in.AsOf}, rows...) {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rowProblem(code, detail string, errs map[string]string) error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, code, detail)
	p.Errors = errs
	return p
}

// ImportOpeningBalances stores a batch all or nothing. The same key with the same content answers the stored
// batch with replayed true; the same key with other content is TRS-0603. An account must exist in the batch's legal
// entity, in the item's currency (TRS-0602 names the rows), and may be opened once (TRS-0604).
func (s *Postgres) ImportOpeningBalances(ctx context.Context, in OpeningImport) (OpeningBatch, bool, error) {
	p, _ := tenant.FromContext(ctx)
	hash := in.hash()
	var out OpeningBatch
	var replayed bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('opening|' || $1 || '|' || $2, 0))`, p.TenantID, in.BatchKey); err != nil {
			return err
		}
		var held string
		var totals []byte
		err := tx.QueryRow(ctx, `select content_hash, totals_minor from bank_opening_batch where tenant_id = $1 and batch_key = $2`, p.TenantID, in.BatchKey).Scan(&held, &totals)
		switch {
		case err == nil:
			if held != hash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOpeningKeyConflict, "the batch key was already used with other content")
			}
			replayed = true
			var err error
			out, err = readOpeningBatch(ctx, tx, p.TenantID, in.BatchKey)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		accountIDs := make([]string, len(in.Items))
		for i, it := range in.Items {
			accountIDs[i] = it.BankAccountID
		}
		type acct struct{ entity, currency string }
		accounts := map[string]acct{}
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, currency from bank_account where tenant_id = $1 and id::text = any($2)`, p.TenantID, accountIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var a acct
			if err := rows.Scan(&id, &a.entity, &a.currency); err != nil {
				rows.Close()
				return err
			}
			accounts[id] = a
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		bad := map[string]string{}
		for i, it := range in.Items {
			a, ok := accounts[strings.ToLower(it.BankAccountID)]
			switch {
			case !ok || a.entity != strings.ToLower(in.LegalEntityID):
				bad[fmt.Sprintf("items[%d].bankAccountId", i+1)] = "is not a bank account of this legal entity"
			case a.currency != it.Currency:
				bad[fmt.Sprintf("items[%d].currency", i+1)] = "differs from the bank account's currency " + a.currency
			}
		}
		if len(bad) > 0 {
			return rowProblem(CodeOpeningReference, "one or more rows name a bank account that cannot take the opening balance", bad)
		}
		var heldAcct, heldRef string
		err = tx.QueryRow(ctx, `select bank_account_id::text, reference from bank_opening_balance where tenant_id = $1 and (bank_account_id::text = any($2) or (legal_entity_id = $3 and reference = any($4))) limit 1`,
			p.TenantID, accountIDs, in.LegalEntityID, references(in.Items)).Scan(&heldAcct, &heldRef)
		switch {
		case err == nil:
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOpeningAccountHeld,
				fmt.Sprintf("bank account %s already has an opening balance (reference %s); it is opened once", heldAcct, heldRef))
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		sums := map[string]int64{}
		for _, it := range in.Items {
			sums[it.Currency] += it.BalanceMinor
		}
		sumJSON, err := json.Marshal(sums)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into bank_opening_batch (tenant_id, legal_entity_id, batch_key, content_hash, as_of, item_count, totals_minor) values ($1, $2, $3, $4, $5, $6, $7)`,
			p.TenantID, in.LegalEntityID, in.BatchKey, hash, in.AsOf, len(in.Items), sumJSON); err != nil {
			return scopeError(err)
		}
		b := &pgx.Batch{}
		for _, it := range in.Items {
			b.Queue(`insert into bank_opening_balance (tenant_id, id, legal_entity_id, batch_key, reference, bank_account_id, currency, balance_minor, as_of)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, ids.New(), in.LegalEntityID, in.BatchKey, it.Reference, it.BankAccountID, it.Currency, it.BalanceMinor, in.AsOf)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return scopeError(err)
		}
		out = OpeningBatch{LegalEntityID: in.LegalEntityID, BatchKey: in.BatchKey, AsOf: in.AsOf, ItemCount: len(in.Items), TotalsMinor: sums}
		return nil
	})
	return out, replayed, err
}

func references(items []OpeningItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Reference
	}
	return out
}

func scopeError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// A concurrent batch opened the same account (or reference) between the check and the insert.
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOpeningAccountHeld, "a bank account in this batch already has an opening balance; it is opened once")
	}
	if err != nil && strings.Contains(err.Error(), "row-level security") {
		return httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope")
	}
	return err
}

func readOpeningBatch(ctx context.Context, tx pgx.Tx, tenantID, key string) (OpeningBatch, error) {
	var b OpeningBatch
	var totals []byte
	if err := tx.QueryRow(ctx, `select legal_entity_id::text, batch_key, as_of::text, item_count, totals_minor from bank_opening_batch where tenant_id = $1 and batch_key = $2`,
		tenantID, key).Scan(&b.LegalEntityID, &b.BatchKey, &b.AsOf, &b.ItemCount, &totals); err != nil {
		return b, err
	}
	return b, json.Unmarshal(totals, &b.TotalsMinor)
}

// ListOpeningBatches returns the legal entity's batches after the key, in key order.
func (s *Postgres) ListOpeningBatches(ctx context.Context, legalEntityID, afterKey string, limit int) ([]OpeningBatch, error) {
	p, _ := tenant.FromContext(ctx)
	out := []OpeningBatch{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select batch_key from bank_opening_batch where tenant_id = $1 and legal_entity_id = $2 and batch_key > $3 order by batch_key limit $4`,
			p.TenantID, legalEntityID, afterKey, limit)
		if err != nil {
			return err
		}
		var keys []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, k := range keys {
			b, err := readOpeningBatch(ctx, tx, p.TenantID, k)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return nil
	})
	return out, err
}
