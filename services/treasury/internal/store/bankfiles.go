package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

// RecordBankFile appends one row of the bank file audit in its own transaction,
// so the row is durable before the caller goes on to sign or send (AC4).
func (p *Postgres) RecordBankFile(ctx context.Context, e bankconnect.AuditEntry) error {
	pr, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, p.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into bank_file_audit
			(tenant_id, id, file_id, legal_entity_id, bank_account_id, event, protocol, file_name, order_type,
			 sha256, size_bytes, key_name, key_version, bank_reference, error, sent_by)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,nullif($13,0),nullif($14,''),nullif($15,''),$16)`,
			pr.TenantID, ids.New(), e.FileID, e.LegalEntityID, e.BankAccountID, e.Event, e.Protocol, e.FileName, e.OrderType,
			e.SHA256, e.SizeBytes, e.KeyName, e.KeyVersion, e.BankReference, e.Error, pr.UserID)
		return err
	})
}
