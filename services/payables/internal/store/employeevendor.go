package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// ErrEmployeeVendorTaken means the worker is already mapped to another vendor in the legal entity.
var ErrEmployeeVendorTaken = errors.New("the worker is already mapped to another vendor")

// EmployeeVendor maps an HR worker to the party vendor that pays their expense reimbursements.
type EmployeeVendor struct {
	LegalEntityID string    `json:"legalEntityId"`
	WorkerID      string    `json:"workerId"`
	VendorID      string    `json:"vendorId"`
	RegisteredBy  string    `json:"registeredBy"`
	RegisteredAt  time.Time `json:"registeredAt"`
	// Repointed is the number of OPEN reimbursement items this registration moved to the vendor.
	Repointed int64 `json:"repointedItems"`
}

// RegisterEmployeeVendor records the worker's vendor once. Registering the same vendor again is
// a replay; another vendor is ErrEmployeeVendorTaken. The worker's OPEN reimbursement items still
// waiting for a payee (vendor id = worker id, payee_unverified) move to the vendor in the same
// transaction, so they enter the next proposal.
func (s *Postgres) RegisterEmployeeVendor(ctx context.Context, legalEntityID, workerID, vendorID, by string) (EmployeeVendor, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out EmployeeVendor
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockPayee(ctx, tx, p.TenantID, legalEntityID, workerID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `insert into ap_employee_vendor (tenant_id, legal_entity_id, worker_id, vendor_id, registered_by)
			values ($1, $2::uuid, $3::uuid, $4::uuid, $5) on conflict do nothing
			returning legal_entity_id::text, worker_id::text, vendor_id::text, registered_by, registered_at`,
			p.TenantID, legalEntityID, workerID, vendorID, by).Scan(&out.LegalEntityID, &out.WorkerID, &out.VendorID, &out.RegisteredBy, &out.RegisteredAt)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx, `select legal_entity_id::text, worker_id::text, vendor_id::text, registered_by, registered_at from ap_employee_vendor
				where legal_entity_id = $1::uuid and worker_id = $2::uuid`, legalEntityID, workerID).
				Scan(&out.LegalEntityID, &out.WorkerID, &out.VendorID, &out.RegisteredBy, &out.RegisteredAt); err != nil {
				return err
			}
			if out.VendorID != vendorID {
				return ErrEmployeeVendorTaken
			}
		} else if err != nil {
			return err
		} else {
			created = true
		}
		tag, err := tx.Exec(ctx, `update ap_open_item set vendor_id = $3::uuid, payee_worker_id = $2::uuid, payee_unverified = false, version = version + 1, updated_at = now()
			where legal_entity_id = $1::uuid and vendor_id = $2::uuid and payee_unverified and status = 'OPEN'`, legalEntityID, workerID, vendorID)
		out.Repointed = tag.RowsAffected()
		return err
	})
	return out, created, err
}

// CreateReimbursement creates the open item of an approved expense report for the worker: the payee
// is the vendor the register maps the worker to, or the worker id marked payee_unverified while no
// mapping exists. The lock it shares with RegisterEmployeeVendor means an item created while the
// worker is being registered is either created for the vendor or moved to it.
func (s *Postgres) CreateReimbursement(ctx context.Context, in OpenItem, workerID string) (OpenItem, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out OpenItem
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockPayee(ctx, tx, p.TenantID, in.LegalEntityID, workerID); err != nil {
			return err
		}
		var vendor string
		err := tx.QueryRow(ctx, `select vendor_id::text from ap_employee_vendor where legal_entity_id = $1::uuid and worker_id = $2::uuid`, in.LegalEntityID, workerID).Scan(&vendor)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			in.VendorID, in.PayeeUnverified, in.PayeeWorkerID = workerID, true, ""
		case err != nil:
			return err
		default:
			in.VendorID, in.PayeeUnverified, in.PayeeWorkerID = vendor, false, workerID
		}
		out, created, err = createFromInvoice(ctx, tx, p.TenantID, in.DueDateSource, in)
		return err
	})
	return out, created, err
}

func lockPayee(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, workerID string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('ap_employee_vendor:' || $1 || ':' || $2 || ':' || $3, 0))`, tenantID, legalEntityID, workerID)
	return err
}
