package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
)

const (
	CodeProfileChangePending  = "HCM-ESS-003"
	CodeProfileChangeNotFound = "HCM-ESS-NOT-FOUND"

	fieldHomeAddress        = "worker_pii.home_address"
	fieldChangeHomeAddress  = "hr_profile_change.home_address"
	fieldChangeBankAccount  = "hr_profile_change.bank_account"
	profileChangeColumns    = `c.id::text, c.worker_id::text, w.legal_name, w.personnel_number, c.kind, c.status, c.summary, c.requested_by, c.decided_by, c.decided_at, c.decision_note, c.created_at`
	profileChangeFromClause = ` from hr_profile_change c join worker w on w.tenant_id = c.tenant_id and w.id = c.worker_id`
)

// ProfileChange is one employee change to their own record (ESS). Summary is
// the masked new value: the preferred name itself, the address's city and
// country, or the bank account's last four characters.
type ProfileChange struct {
	ID              string     `json:"id"`
	WorkerID        string     `json:"workerId"`
	WorkerName      string     `json:"workerName"`
	PersonnelNumber string     `json:"personnelNumber"`
	Kind            string     `json:"kind"`
	Status          string     `json:"status"`
	Summary         string     `json:"summary"`
	RequestedBy     string     `json:"requestedBy"`
	DecidedBy       *string    `json:"decidedBy,omitempty"`
	DecidedAt       *time.Time `json:"decidedAt,omitempty"`
	DecisionNote    *string    `json:"decisionNote,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// ProfileChangeInput is a validated, normalized change; only the field of Kind is read.
type ProfileChangeInput struct {
	Kind          string
	PreferredName string
	Address       domain.Address
	BankAccount   string
}

// ProfileChangeFilter narrows a change list. ManagerWorkerID limits it to that
// manager's direct reports and the kinds a manager decides.
type ProfileChangeFilter struct {
	Status          string
	WorkerID        string
	ManagerWorkerID string
}

// ProfileChangePage is one keyset page, newest first.
type ProfileChangePage struct {
	Items     []ProfileChange
	NextAfter string
}

func scanProfileChange(row pgx.Row) (ProfileChange, error) {
	var c ProfileChange
	err := row.Scan(&c.ID, &c.WorkerID, &c.WorkerName, &c.PersonnelNumber, &c.Kind, &c.Status, &c.Summary, &c.RequestedBy, &c.DecidedBy, &c.DecidedAt, &c.DecisionNote, &c.CreatedAt)
	return c, err
}

func getProfileChange(ctx context.Context, tx pgx.Tx, tenantID, id string) (ProfileChange, error) {
	return scanProfileChange(tx.QueryRow(ctx, `select `+profileChangeColumns+profileChangeFromClause+` where c.tenant_id = $1 and c.id = $2`, tenantID, id))
}

// RequestProfileChange records the worker's own change. A preferred name is
// written to the worker at once (APPLIED); an address or bank account is sealed
// on the change row and waits (PENDING). One pending change per kind.
func (s *Postgres) RequestProfileChange(ctx context.Context, workerID string, in ProfileChangeInput) (ProfileChange, error) {
	p := principal(ctx)
	var out ProfileChange
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id := ids.New()
		switch in.Kind {
		case domain.ChangePreferredName:
			tag, err := tx.Exec(ctx, `update worker set preferred_name = nullif($3, ''), updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, workerID, in.PreferredName)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
			}
			summary := in.PreferredName
			if summary == "" {
				summary = "(cleared)"
			}
			if _, err := tx.Exec(ctx, `insert into hr_profile_change (tenant_id, id, worker_id, kind, status, summary, requested_by)
				values ($1, $2, $3, $4, 'APPLIED', $5, $6)`, p.TenantID, id, workerID, in.Kind, summary, p.UserID); err != nil {
				return err
			}
		case domain.ChangeHomeAddress, domain.ChangeBankAccount:
			field, plain, summary := fieldChangeBankAccount, in.BankAccount, domain.MaskBank(in.BankAccount)
			if in.Kind == domain.ChangeHomeAddress {
				raw, err := json.Marshal(in.Address)
				if err != nil {
					return err
				}
				field, plain, summary = fieldChangeHomeAddress, string(raw), in.Address.Summary()
			}
			sealed, err := s.sealer.Seal(p.TenantID, field, plain)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `insert into hr_profile_change (tenant_id, id, worker_id, kind, status, summary, value_sealed, requested_by)
				values ($1, $2, $3, $4, 'PENDING', $5, $6, $7)`, p.TenantID, id, workerID, in.Kind, summary, sealed, p.UserID)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return problem(http.StatusConflict, CodeProfileChangePending, "a change of this kind is already waiting for approval; withdraw it first")
			}
			if err != nil {
				return err
			}
		default:
			return problem(http.StatusBadRequest, "COM-0001", "unknown change kind")
		}
		var err error
		out, err = getProfileChange(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListProfileChanges pages changes newest first.
func (s *Postgres) ListProfileChanges(ctx context.Context, f ProfileChangeFilter, after string, limit int) (ProfileChangePage, error) {
	p := principal(ctx)
	page := ProfileChangePage{Items: []ProfileChange{}}
	if after == "" {
		after = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		query := `select ` + profileChangeColumns + profileChangeFromClause + `
			where c.tenant_id = $1 and ($2 = '' or c.status = $2) and ($3 = '' or c.worker_id::text = $3) and c.id < $4::uuid
			order by c.id desc limit $5`
		args := []any{p.TenantID, f.Status, f.WorkerID, after, limit + 1}
		if f.ManagerWorkerID != "" {
			query = teamCTE + ` select ` + profileChangeColumns + profileChangeFromClause + `
				where c.tenant_id = $1 and ($5 = '' or c.status = $5) and ($6 = '' or c.worker_id::text = $6) and c.id < $7::uuid
					and c.kind = '` + domain.ChangeHomeAddress + `' and c.worker_id in (select worker_id from team where manager_id = $4::uuid)
				order by c.id desc limit $8`
			args = []any{p.TenantID, s.now().UTC(), s.depth, f.ManagerWorkerID, f.Status, f.WorkerID, after, limit + 1}
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanProfileChange(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, c)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].ID
	}
	return page, err
}

// DecideProfileChange approves or rejects a PENDING change. managerWorkerID,
// when set, confines the decision to that manager's direct reports (anyone
// else is 404) and to the kinds a manager decides (HCM-ESS-005). Nobody
// decides a change to their own record (HCM-ESS-006). Approval moves the
// sealed value into the vault with a p3_change audit row; either way the
// sealed copy on the change row is dropped.
func (s *Postgres) DecideProfileChange(ctx context.Context, id, decision, note, managerWorkerID string) (ProfileChange, bool, error) {
	p := principal(ctx)
	var out ProfileChange
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var workerID, kind, status, requestedBy string
		var workerUser *string
		var sealed []byte
		err := tx.QueryRow(ctx, `select c.worker_id::text, c.kind, c.status, c.requested_by, c.value_sealed, w.user_id::text
			from hr_profile_change c join worker w on w.tenant_id = c.tenant_id and w.id = c.worker_id
			where c.tenant_id = $1 and c.id = $2 for update of c`, p.TenantID, id).Scan(&workerID, &kind, &status, &requestedBy, &sealed, &workerUser)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeProfileChangeNotFound, "profile change not found")
		}
		if err != nil {
			return err
		}
		if managerWorkerID != "" {
			found, err := directReportsTx(ctx, tx, p.TenantID, managerWorkerID, []string{workerID}, s.now().UTC(), s.depth)
			if err != nil {
				return err
			}
			if len(found) == 0 {
				return problem(http.StatusNotFound, CodeProfileChangeNotFound, "profile change not found")
			}
			if !domain.ManagerMayDecide(kind) {
				return problem(http.StatusForbidden, domain.CodeProfileChangeHR, "HR decides bank account changes")
			}
		}
		if p.UserID == "" || requestedBy == p.UserID || (workerUser != nil && *workerUser == p.UserID) {
			return problem(http.StatusForbidden, domain.CodeProfileChangeSelf, "you cannot decide a change to your own record")
		}
		next, moved, err := domain.NextChangeStatus(status, decision)
		if err != nil {
			return changeProblem(err)
		}
		if moved {
			if next == domain.ChangeApproved {
				if err := s.applyChangeTx(ctx, tx, workerID, kind, id, sealed); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `update hr_profile_change set status = $3, decided_by = $4, decided_at = now(), decision_note = nullif($5, ''), value_sealed = null
				where tenant_id = $1 and id = $2`, p.TenantID, id, next, p.UserID, note); err != nil {
				return err
			}
			changed = true
		}
		out, err = getProfileChange(ctx, tx, p.TenantID, id)
		return err
	})
	return out, changed, err
}

func (s *Postgres) applyChangeTx(ctx context.Context, tx pgx.Tx, workerID, kind, changeID string, sealed []byte) error {
	p := principal(ctx)
	from, to, column, auditField := fieldChangeBankAccount, fieldBankAccount, "bank_account_sealed", "bank_account"
	if kind == domain.ChangeHomeAddress {
		from, to, column, auditField = fieldChangeHomeAddress, fieldHomeAddress, "home_address_sealed", "home_address"
	}
	plain, err := s.sealer.Open(p.TenantID, from, sealed)
	if err != nil {
		return err
	}
	resealed, err := s.sealer.Seal(p.TenantID, to, plain)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `update worker_pii set `+column+` = $3 where tenant_id = $1 and worker_id = $2`, p.TenantID, workerID, resealed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	return audit(ctx, tx, p, "p3_change", workerID, []string{auditField}, "self-service profile change "+changeID+" approved", "")
}

// CancelProfileChange withdraws the worker's own PENDING change. A change of
// another worker is 404; one already withdrawn is returned unchanged.
func (s *Postgres) CancelProfileChange(ctx context.Context, id, workerID string) (ProfileChange, error) {
	p := principal(ctx)
	var out ProfileChange
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `select status from hr_profile_change where tenant_id = $1 and id = $2 and worker_id = $3 for update`, p.TenantID, id, workerID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeProfileChangeNotFound, "profile change not found")
		}
		if err != nil {
			return err
		}
		next, moved, err := domain.NextChangeStatus(status, "CANCEL")
		if err != nil {
			return changeProblem(err)
		}
		if moved {
			if _, err := tx.Exec(ctx, `update hr_profile_change set status = $3, value_sealed = null where tenant_id = $1 and id = $2`, p.TenantID, id, next); err != nil {
				return err
			}
		}
		out, err = getProfileChange(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

func changeProblem(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusConflict
	if de.Code == "COM-0001" {
		status = http.StatusBadRequest
	}
	return problem(status, de.Code, de.Detail)
}
