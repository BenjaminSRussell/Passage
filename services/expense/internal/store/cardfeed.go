package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

// CodeDuplicateFile refuses a feed file whose hash was already imported (FR-43.13,
// 8.43.18 EXP-CARD-002).
const CodeDuplicateFile = "EXP-CARD-002"

// RowError is one quarantined feed row: its file line (or OFX block) and the reason.
type RowError struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// NewCardRow is one parsed feed row already mapped to its cardholder.
type NewCardRow struct {
	Line      int
	WorkerID  string
	CardLast4 string
	TxnDate   time.Time
	PostDate  time.Time
	Merchant  string
	MCC       string
	Amount    decimal.Decimal
	Currency  string
}

// NewImport is the input to ImportCardFeed.
type NewImport struct {
	LegalEntityID string
	Format        string
	FileHash      string
	Rows          []NewCardRow
	Quarantined   []RowError
}

// Import is a persisted feed import: the file hash, the count imported and the
// quarantined rows.
type Import struct {
	ID            string
	LegalEntityID string
	Format        string
	FileHash      string
	Imported      int
	Quarantined   []RowError
	CreatedAt     time.Time
}

// CardTransaction is one imported card transaction (8.43.4 CardTransaction).
type CardTransaction struct {
	ID            string
	ImportID      string
	LegalEntityID string
	WorkerID      string
	Line          int
	CardLast4     string
	TxnDate       time.Time
	PostDate      time.Time
	Merchant      string
	MCC           string
	Amount        decimal.Decimal
	Currency      string
	Status        string
}

// CardFilter narrows ListCardTransactions. Empty fields do not filter.
type CardFilter struct {
	WorkerID string
	Status   string
}

const cardColumns = `id::text, import_id::text, legal_entity_id::text, worker_id::text, source_line, card_last4,
  txn_date, post_date, merchant, mcc, amount::text, currency::text, status`

// ImportCardFeed stores one feed file: the import with its quarantine and one
// UNASSIGNED card transaction per accepted row, all in one transaction. The file hash
// is unique per tenant, so the same file under a new key is refused with EXP-CARD-002;
// the same key with the same request replays the original import (replayed=true).
func (s *Store) ImportCardFeed(ctx context.Context, key, hash string, in NewImport) (Import, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Import{}, false, err
	}
	if !inScope(p, in.LegalEntityID) {
		return Import{}, false, outOfScope()
	}
	quarantined, err := json.Marshal(nonNilSlice(in.Quarantined))
	if err != nil {
		return Import{}, false, err
	}
	var out Import
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id, stored string
		err := tx.QueryRow(ctx, `select id::text, request_hash from card_import where tenant_id=$1 and create_key=$2`, p.TenantID, key).Scan(&id, &stored)
		if err == nil {
			if stored != hash {
				return reuse()
			}
			replayed = true
			out, err = loadImport(ctx, tx, p.TenantID, id)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id = ids.New()
		_, err = tx.Exec(ctx, `insert into card_import(tenant_id, id, legal_entity_id, format, file_hash, imported, quarantined,
			imported_by, create_key, request_hash) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			p.TenantID, id, in.LegalEntityID, in.Format, in.FileHash, len(in.Rows), quarantined, p.UserID, key, hash)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "card_import_file_hash" {
			return httpx.NewProblem(http.StatusConflict, CodeDuplicateFile, "Feed file "+in.FileHash+" was already imported")
		}
		if err != nil {
			return translate(err, CodeIdempotencyReuse)
		}
		if len(in.Rows) > 0 {
			batch := &pgx.Batch{}
			for _, r := range in.Rows {
				batch.Queue(`insert into card_transaction(tenant_id, id, import_id, legal_entity_id, worker_id, source_line,
					card_last4, txn_date, post_date, merchant, mcc, amount, currency)
					values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::numeric,$13)`,
					p.TenantID, ids.New(), id, in.LegalEntityID, r.WorkerID, r.Line, r.CardLast4, r.TxnDate, r.PostDate,
					r.Merchant, r.MCC, r.Amount.String(), r.Currency)
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return err
			}
		}
		out, err = loadImport(ctx, tx, p.TenantID, id)
		return err
	})
	if err != nil {
		return Import{}, false, err
	}
	return out, replayed, nil
}

// ListCardTransactions returns up to limit card transactions after the cursor (an id),
// in id order; status UNASSIGNED is a worker's unassigned pool.
func (s *Store) ListCardTransactions(ctx context.Context, f CardFilter, after string, limit int) ([]CardTransaction, string, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, "", err
	}
	if after == "" {
		after = ids.Nil
	}
	out := []CardTransaction{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+cardColumns+` from card_transaction
			where tenant_id=$1 and id > $2::uuid and ($3 = '' or worker_id::text = $3) and ($4 = '' or status = $4)
			order by id limit $5`, p.TenantID, after, f.WorkerID, f.Status, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCard(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	out, next := pageOf(out, limit, func(c CardTransaction) string { return c.ID })
	return out, next, nil
}

func loadImport(ctx context.Context, tx pgx.Tx, tenantID, id string) (Import, error) {
	var imp Import
	var quarantined []byte
	err := tx.QueryRow(ctx, `select id::text, legal_entity_id::text, format, file_hash, imported, quarantined, created_at
		from card_import where tenant_id=$1 and id=$2`, tenantID, id).
		Scan(&imp.ID, &imp.LegalEntityID, &imp.Format, &imp.FileHash, &imp.Imported, &quarantined, &imp.CreatedAt)
	if err != nil {
		return Import{}, err
	}
	if err := json.Unmarshal(quarantined, &imp.Quarantined); err != nil {
		return Import{}, err
	}
	return imp, nil
}

func scanCard(r pgx.Row) (CardTransaction, error) {
	var c CardTransaction
	var amount string
	if err := r.Scan(&c.ID, &c.ImportID, &c.LegalEntityID, &c.WorkerID, &c.Line, &c.CardLast4, &c.TxnDate, &c.PostDate,
		&c.Merchant, &c.MCC, &amount, &c.Currency, &c.Status); err != nil {
		return CardTransaction{}, err
	}
	c.TxnDate, c.PostDate = utcDay(c.TxnDate), utcDay(c.PostDate)
	var err error
	c.Amount, err = decimal.NewFromString(amount)
	return c, err
}
