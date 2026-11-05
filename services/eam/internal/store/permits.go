package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// PermitType is a permit category with its approval and validity rules (FR-44.19).
type PermitType struct {
	Code              string `json:"code"`
	Name              string `json:"name"`
	ApproverRole      string `json:"approverRole"`
	MaxValidityHours  int    `json:"maxValidityHours"`
	RequiresIsolation bool   `json:"requiresIsolation"`
	RequiresGasTest   bool   `json:"requiresGasTest"`
}

// IsolationPoint is one energy isolation on an asset (BR-44.11).
type IsolationPoint struct {
	ID               string    `json:"id"`
	AssetID          string    `json:"assetId"`
	EnergyType       string    `json:"energyType"`
	LocationText     string    `json:"location"`
	Method           string    `json:"method"`
	VerificationStep string    `json:"verificationStep"`
	TagCode          string    `json:"tagCode"`
	CreatedAt        time.Time `json:"createdAt"`
}

// Permit authorises work on a work order during a validity window. LocksOpen
// counts the personal locks still applied or verified on it (permit register).
type Permit struct {
	ID             string    `json:"id"`
	Number         string    `json:"number"`
	PermitTypeCode string    `json:"permitTypeCode"`
	WorkOrderID    string    `json:"workOrderId"`
	AssetID        string    `json:"assetId"`
	Status         string    `json:"status"`
	ValidFrom      time.Time `json:"validFrom"`
	ValidTo        time.Time `json:"validTo"`
	RequestedBy    string    `json:"requestedBy"`
	ApprovedBy     *string   `json:"approvedBy,omitempty"`
	IssuedBy       *string   `json:"issuedBy,omitempty"`
	ClosedBy       *string   `json:"closedBy,omitempty"`
	LocksOpen      int       `json:"locksOpen"`
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
}

// LotoLock is one technician's personal lock on one isolation point (BR-44.11).
type LotoLock struct {
	ID               string     `json:"id"`
	PermitID         string     `json:"permitId"`
	IsolationPointID string     `json:"isolationPointId"`
	TechnicianID     string     `json:"technicianId"`
	LockNumber       string     `json:"lockNumber"`
	State            string     `json:"state"`
	AppliedAt        time.Time  `json:"appliedAt"`
	VerifiedAt       *time.Time `json:"verifiedAt,omitempty"`
	RemovedAt        *time.Time `json:"removedAt,omitempty"`
}

// PermitDetail is a permit with the asset's isolation points, the locks on the
// permit, the points still lacking a verified lock and the append-only history.
type PermitDetail struct {
	Permit          Permit           `json:"permit"`
	IsolationPoints []IsolationPoint `json:"isolationPoints"`
	Locks           []LotoLock       `json:"locks"`
	UnlockedPoints  int              `json:"unlockedPoints"`
	History         []StatusChange   `json:"history"`
}

// PermitRequest is the input to request a permit for a work order.
type PermitRequest struct {
	WorkOrderID    string
	PermitTypeCode string
	ValidFrom      time.Time
	ValidTo        time.Time
}

// JobPermits is the set of permit types a job type requires (BR-44.11).
type JobPermits struct {
	JobType         string   `json:"jobType"`
	PermitTypeCodes []string `json:"permitTypeCodes"`
}

// ExpiredPermit is one permit the expiry job suspended (EAM-PMT-004). The work
// order is flagged when it was IN_PROGRESS: the job is running without a valid permit.
type ExpiredPermit struct {
	PermitID            string    `json:"permitId"`
	Number              string    `json:"number"`
	WorkOrderID         string    `json:"workOrderId"`
	ValidTo             time.Time `json:"validTo"`
	WorkOrderInProgress bool      `json:"workOrderInProgress"`
	Code                string    `json:"code"`
}

// ExpirySystemActor is the actor recorded on automatic suspensions.
const ExpirySystemActor = "system:eam-permit-expiry"

// actorOf returns the caller's user id, recorded on permit transitions.
func actorOf(ctx context.Context) string {
	if p, ok := tenant.FromContext(ctx); ok {
		return p.UserID
	}
	return ""
}

const permitColumns = `p.id::text, p.number, p.permit_type_code, p.work_order_id::text, p.asset_id::text, p.status,
  p.valid_from, p.valid_to, p.requested_by, p.approved_by, p.issued_by, p.closed_by,
  (select count(*) from loto_lock l where l.tenant_id = p.tenant_id and l.permit_id = p.id and l.state <> 'REMOVED'),
  p.version, p.created_at`

func scanPermit(row pgx.Row) (Permit, error) {
	var p Permit
	err := row.Scan(&p.ID, &p.Number, &p.PermitTypeCode, &p.WorkOrderID, &p.AssetID, &p.Status,
		&p.ValidFrom, &p.ValidTo, &p.RequestedBy, &p.ApprovedBy, &p.IssuedBy, &p.ClosedBy, &p.LocksOpen, &p.Version, &p.CreatedAt)
	return p, err
}

func getPermit(ctx context.Context, tx pgx.Tx, t, id string, lock bool) (Permit, error) {
	q := `select ` + permitColumns + ` from permit p where p.tenant_id = $1 and p.id = $2`
	if lock {
		q += ` for update of p`
	}
	p, err := scanPermit(tx.QueryRow(ctx, q, t, id))
	return p, notFound(err, "permit")
}

// governingPermit returns the permit of a type on a work order that is still
// in force (not CLOSED or CANCELED); a unique index keeps it to one. With share
// the row is held FOR SHARE, so a transition or lock action on the permit (both
// take it FOR UPDATE) waits until the caller's transaction ends.
func governingPermit(ctx context.Context, tx pgx.Tx, t, woID, typeCode string, share bool) (Permit, bool, error) {
	q := `select ` + permitColumns + ` from permit p
	  where p.tenant_id = $1 and p.work_order_id = $2 and p.permit_type_code = $3
	    and p.status not in ('CLOSED', 'CANCELED')`
	if share {
		q += ` for share of p`
	}
	p, err := scanPermit(tx.QueryRow(ctx, q, t, woID, typeCode))
	if errors.Is(err, pgx.ErrNoRows) {
		return Permit{}, false, nil
	}
	return p, err == nil, err
}

const permitTypeColumns = `code, name, approver_role, max_validity_hours, requires_isolation, requires_gas_test`

func scanPermitType(row pgx.Row) (PermitType, error) {
	var pt PermitType
	err := row.Scan(&pt.Code, &pt.Name, &pt.ApproverRole, &pt.MaxValidityHours, &pt.RequiresIsolation, &pt.RequiresGasTest)
	return pt, err
}

func getPermitType(ctx context.Context, tx pgx.Tx, t, code string) (PermitType, error) {
	pt, err := scanPermitType(tx.QueryRow(ctx, `select `+permitTypeColumns+` from permit_type
	  where tenant_id = $1 and code = $2`, t, code))
	return pt, notFound(err, "permit type")
}

// UpsertPermitType creates or replaces a permit type (eam.setup.admin). Repeating
// the same definition is a no-op, so the call is idempotent by content.
func (s *Postgres) UpsertPermitType(ctx context.Context, in PermitType) (PermitType, error) {
	var out PermitType
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		row := tx.QueryRow(ctx, `insert into permit_type (tenant_id, code, name, approver_role, max_validity_hours,
		    requires_isolation, requires_gas_test) values ($1, $2, $3, $4, $5, $6, $7)
		  on conflict (tenant_id, code) do update set name = excluded.name, approver_role = excluded.approver_role,
		    max_validity_hours = excluded.max_validity_hours, requires_isolation = excluded.requires_isolation,
		    requires_gas_test = excluded.requires_gas_test, updated_at = now()
		  returning `+permitTypeColumns, t, in.Code, in.Name, in.ApproverRole, in.MaxValidityHours,
			in.RequiresIsolation, in.RequiresGasTest)
		var err error
		out, err = scanPermitType(row)
		return err
	})
	return out, err
}

// ListPermitTypes returns every permit type of the tenant.
func (s *Postgres) ListPermitTypes(ctx context.Context) ([]PermitType, error) {
	out := []PermitType{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select `+permitTypeColumns+` from permit_type where tenant_id = $1 order by code`, t)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			pt, err := scanPermitType(rows)
			if err != nil {
				return err
			}
			out = append(out, pt)
		}
		return rows.Err()
	})
	return out, err
}

// SetJobPermits replaces the permit types a job type requires (BR-44.11). Every
// code must name an existing permit type (404 otherwise).
func (s *Postgres) SetJobPermits(ctx context.Context, jobType string, codes []string) (JobPermits, error) {
	out := JobPermits{JobType: jobType, PermitTypeCodes: []string{}}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		for _, c := range codes {
			if _, err := getPermitType(ctx, tx, t, c); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `delete from job_permit_requirement where tenant_id = $1 and job_type = $2`, t, jobType); err != nil {
			return err
		}
		for _, c := range codes {
			if _, err := tx.Exec(ctx, `insert into job_permit_requirement (tenant_id, job_type, permit_type_code)
			  values ($1, $2, $3) on conflict do nothing`, t, jobType, c); err != nil {
				return err
			}
		}
		var err error
		out.PermitTypeCodes, err = jobPermitCodes(ctx, tx, t, jobType)
		return err
	})
	return out, err
}

// GetJobPermits returns the permit types a job type requires (empty when none).
func (s *Postgres) GetJobPermits(ctx context.Context, jobType string) (JobPermits, error) {
	out := JobPermits{JobType: jobType}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		var err error
		out.PermitTypeCodes, err = jobPermitCodes(ctx, tx, t, jobType)
		return err
	})
	return out, err
}

func jobPermitCodes(ctx context.Context, tx pgx.Tx, t, jobType string) ([]string, error) {
	types, err := requiredPermitTypes(ctx, tx, t, jobType)
	if err != nil {
		return nil, err
	}
	codes := make([]string, 0, len(types))
	for _, pt := range types {
		codes = append(codes, pt.Code)
	}
	return codes, nil
}

// requiredPermitTypes lists the permit types a job type needs, with their rules.
func requiredPermitTypes(ctx context.Context, tx pgx.Tx, t, jobType string) ([]PermitType, error) {
	rows, err := tx.Query(ctx, `select pt.code, pt.name, pt.approver_role, pt.max_validity_hours,
	    pt.requires_isolation, pt.requires_gas_test from job_permit_requirement r
	  join permit_type pt on pt.tenant_id = r.tenant_id and pt.code = r.permit_type_code
	  where r.tenant_id = $1 and r.job_type = $2 order by pt.code`, t, jobType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PermitType
	for rows.Next() {
		pt, err := scanPermitType(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pt)
	}
	return out, rows.Err()
}

// checkPermitGate enforces BR-44.11 before a work order enters IN_PROGRESS:
// every permit type its job type requires needs an ISSUED permit valid now,
// and an isolation permit needs every isolation point locked and verified.
func checkPermitGate(ctx context.Context, tx pgx.Tx, t string, wo WorkOrder, now time.Time) error {
	if wo.Type == domain.TypeRound {
		return roundPermitGate(ctx, tx, t, wo.ID)
	}
	if wo.JobType == "" {
		return nil
	}
	types, err := requiredPermitTypes(ctx, tx, t, wo.JobType)
	if err != nil {
		return err
	}
	for _, pt := range types {
		req := domain.PermitRequirement{TypeCode: pt.Code, RequiresIsolation: pt.RequiresIsolation}
		// Hold the permit so it cannot be suspended or lose a lock while the
		// work order starts on it (permit checks fail closed, 8.44.19).
		p, found, err := governingPermit(ctx, tx, t, wo.ID, pt.Code, true)
		if err != nil {
			return err
		}
		if found {
			req.Found = true
			req.Status = domain.PermitStatus(p.Status)
			req.ValidFrom, req.ValidTo = p.ValidFrom, p.ValidTo
			if pt.RequiresIsolation {
				if req.Points, req.UnlockedPoints, err = isolationState(ctx, tx, t, wo.AssetID, p.ID); err != nil {
					return err
				}
			}
		}
		if err := domain.CheckPermitGate(req, now); err != nil {
			return gateProblem(err)
		}
	}
	return nil
}

// roundPermitGate fails closed for ROUND orders: a round carries one job per
// stop but no permit per stop, so a round cannot start while any stop's job
// type requires a permit to work (EAM-PMT-001). Rounds refuse such job types
// when defined; this catches a requirement added to the job type later.
func roundPermitGate(ctx context.Context, tx pgx.Tx, t, woID string) error {
	stops, err := workOrderStops(ctx, tx, t, woID)
	if err != nil {
		return err
	}
	for _, st := range stops {
		types, err := requiredPermitTypes(ctx, tx, t, st.JobType)
		if err != nil {
			return err
		}
		if len(types) > 0 {
			return problem(422, domain.CodePermitMissing, fmt.Sprintf(
				"round stop %d job type %s requires an ISSUED %s permit, which a round cannot carry; plan the job as its own work order",
				st.Sequence, st.JobType, types[0].Code))
		}
	}
	return nil
}

// gateProblem maps a domain rule violation to a 422 problem with its code.
func gateProblem(err error) error {
	var ge domain.GateError
	if errors.As(err, &ge) {
		return problem(422, ge.Code, ge.Detail)
	}
	return err
}

// isolationState counts the asset's isolation points and those without a
// verified lock on the permit.
func isolationState(ctx context.Context, tx pgx.Tx, t, assetID, permitID string) (points, unlocked int, err error) {
	err = tx.QueryRow(ctx, `select count(*), count(*) filter (where not exists (select 1 from loto_lock l
	    where l.tenant_id = ip.tenant_id and l.isolation_point_id = ip.id and l.permit_id = $3 and l.state = 'VERIFIED'))
	  from isolation_point ip where ip.tenant_id = $1 and ip.asset_id = $2`, t, assetID, permitID).Scan(&points, &unlocked)
	return points, unlocked, err
}

// CreateIsolationPoint adds an energy isolation point to an asset (eam.asset.maintain).
// A tag code is unique per asset (EAM-DUP-001).
func (s *Postgres) CreateIsolationPoint(ctx context.Context, assetID string, in IsolationPoint, key string) (IsolationPoint, error) {
	var out IsolationPoint
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "isolation_point", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getIsolationPoint(ctx, tx, t, id)
			if gerr == nil && out.AssetID != assetID {
				return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another asset")
			}
			return gerr
		}
		if _, err := getAsset(ctx, tx, t, assetID, false); err != nil {
			return err
		}
		dup, err := taken(ctx, tx, "isolation_point", "tenant_id = $1 and asset_id = $2 and tag_code = $3", t, assetID, in.TagCode)
		if err != nil {
			return err
		}
		if dup {
			return problem(409, CodeDuplicate, "tag code "+in.TagCode+" is already used on this asset")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into isolation_point (tenant_id, id, asset_id, energy_type, location_text,
		    method, verification_step, tag_code, idempotency_key) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			t, id, assetID, in.EnergyType, in.LocationText, in.Method, in.VerificationStep, in.TagCode, optStr(key)); err != nil {
			return err
		}
		out, err = getIsolationPoint(ctx, tx, t, id)
		return err
	})
	return out, err
}

const isolationColumns = `id::text, asset_id::text, energy_type, location_text, method, verification_step, tag_code, created_at`

func scanIsolationPoint(row pgx.Row) (IsolationPoint, error) {
	var ip IsolationPoint
	err := row.Scan(&ip.ID, &ip.AssetID, &ip.EnergyType, &ip.LocationText, &ip.Method, &ip.VerificationStep, &ip.TagCode, &ip.CreatedAt)
	return ip, err
}

func getIsolationPoint(ctx context.Context, tx pgx.Tx, t, id string) (IsolationPoint, error) {
	ip, err := scanIsolationPoint(tx.QueryRow(ctx, `select `+isolationColumns+` from isolation_point
	  where tenant_id = $1 and id = $2`, t, id))
	return ip, notFound(err, "isolation point")
}

func isolationPoints(ctx context.Context, tx pgx.Tx, t, assetID string) ([]IsolationPoint, error) {
	out := []IsolationPoint{}
	rows, err := tx.Query(ctx, `select `+isolationColumns+` from isolation_point
	  where tenant_id = $1 and asset_id = $2 order by tag_code`, t, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		ip, err := scanIsolationPoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ip)
	}
	return out, rows.Err()
}

// ListIsolationPoints returns the isolation points of an asset (404 for an unknown asset).
func (s *Postgres) ListIsolationPoints(ctx context.Context, assetID string) ([]IsolationPoint, error) {
	var out []IsolationPoint
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if _, err := getAsset(ctx, tx, t, assetID, false); err != nil {
			return err
		}
		var err error
		out, err = isolationPoints(ctx, tx, t, assetID)
		return err
	})
	return out, err
}

// RequestPermit opens a REQUESTED permit for an open work order (eam.work.plan).
// The validity window must fit the permit type's maximum (BR-44.11), and only
// one permit of a type may be in force on a work order at a time.
func (s *Postgres) RequestPermit(ctx context.Context, in PermitRequest, key string) (Permit, error) {
	var out Permit
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "permit", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getPermit(ctx, tx, t, id, false)
			return gerr
		}
		pt, err := getPermitType(ctx, tx, t, in.PermitTypeCode)
		if err != nil {
			return err
		}
		wo, err := getWorkOrder(ctx, tx, t, in.WorkOrderID, true)
		if err != nil {
			return err
		}
		if !domain.Status(wo.Status).Open() {
			return problem(422, domain.CodePermitState, "work order is "+wo.Status+"; permits need an open work order")
		}
		if err := domain.ValidPermitWindow(in.ValidFrom, in.ValidTo, pt.MaxValidityHours); err != nil {
			return problem(422, CodeValidation, err.Error())
		}
		if cur, found, err := governingPermit(ctx, tx, t, wo.ID, pt.Code, false); err != nil {
			return err
		} else if found {
			return problem(409, CodeDuplicate, fmt.Sprintf("permit %s of type %s is still %s on this work order", cur.Number, pt.Code, cur.Status))
		}
		id := ids.New()
		actor := actorOf(ctx)
		now := s.now()
		if _, err := tx.Exec(ctx, `insert into permit (tenant_id, id, number, permit_type_code, work_order_id, asset_id,
		    status, valid_from, valid_to, requested_by, idempotency_key, created_at, updated_at)
		  values ($1, $2, 'EAM-PMT-' || nextval('permit_number_seq'), $3, $4, $5, 'REQUESTED', $6, $7, $8, $9, $10, $10)`,
			t, id, pt.Code, in.WorkOrderID, wo.AssetID, in.ValidFrom, in.ValidTo, actor, optStr(key), now); err != nil {
			return err
		}
		if err := recordPermitTransition(ctx, tx, t, id, wo.ID, "", domain.PermitRequested, actor, now, ""); err != nil {
			return err
		}
		out, err = getPermit(ctx, tx, t, id, false)
		return err
	})
	return out, err
}

// recordPermitTransition appends the audit row and the status event (same tx).
func recordPermitTransition(ctx context.Context, tx pgx.Tx, t, permitID, woID, from string, to domain.PermitStatus,
	actor string, now time.Time, key string) error {
	if _, err := tx.Exec(ctx, `insert into permit_transition (tenant_id, id, permit_id, from_status, to_status, actor, at, idempotency_key)
	  values ($1, $2, $3, $4, $5, $6, $7, $8)`, t, ids.New(), permitID, from, string(to), actor, now, optStr(key)); err != nil {
		return err
	}
	return enqueue(ctx, tx, t, "permit", permitID, eamevents.PermitStatus, eamevents.PermitStatusData{
		PermitID: permitID, WorkOrderID: woID, From: from, To: string(to), At: now.Format(time.RFC3339Nano), Actor: actor})
}

// lockHolders lists the technicians holding a verified lock on the permit.
func lockHolders(ctx context.Context, tx pgx.Tx, t, permitID string) ([]string, error) {
	rows, err := tx.Query(ctx, `select distinct technician_id from loto_lock
	  where tenant_id = $1 and permit_id = $2 and state = 'VERIFIED' order by technician_id`, t, permitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// TransitionPermit moves a permit through its lifecycle (BR-44.11) guarded by
// the caller's version. Approval needs an approver other than the requester;
// issue needs the permit still valid and, for an isolation permit, every
// isolation point locked and verified by someone other than the issuer alone;
// close and cancel need every lock removed, and close needs the work order
// not IN_PROGRESS. A repeated key returns the permit as it is now.
func (s *Postgres) TransitionPermit(ctx context.Context, id string, to domain.PermitStatus, version int, key string) (Permit, error) {
	var out Permit
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur, err := getPermit(ctx, tx, t, id, true)
		if err != nil {
			return err
		}
		if key != "" {
			var permitID string
			err := tx.QueryRow(ctx, `select permit_id::text from permit_transition where tenant_id = $1 and idempotency_key = $2`,
				t, key).Scan(&permitID)
			if err == nil {
				if permitID != id {
					return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another permit")
				}
				out = cur
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if cur.Version != version {
			return problem(412, CodeVersion, "permit version changed")
		}
		from := domain.PermitStatus(cur.Status)
		if !domain.CanPermitTransition(from, to) {
			return problem(422, domain.CodePermitState, "transition "+cur.Status+" to "+string(to)+" not allowed")
		}
		now := s.now()
		actor := actorOf(ctx)
		var approvedBy, issuedBy, closedBy *string
		switch to {
		case domain.PermitApproved:
			if err := domain.CheckApprover(cur.RequestedBy, actor); err != nil {
				return gateProblem(err)
			}
			approvedBy = &actor
		case domain.PermitIssued:
			if !now.Before(cur.ValidTo) {
				return problem(422, domain.CodePermitExpired, "permit "+cur.Number+" validity has ended; request a new permit")
			}
			pt, err := getPermitType(ctx, tx, t, cur.PermitTypeCode)
			if err != nil {
				return err
			}
			if pt.RequiresIsolation {
				points, unlocked, err := isolationState(ctx, tx, t, cur.AssetID, cur.ID)
				if err != nil {
					return err
				}
				if err := domain.CheckIsolation(points, unlocked); err != nil {
					return gateProblem(err)
				}
				holders, err := lockHolders(ctx, tx, t, cur.ID)
				if err != nil {
					return err
				}
				if err := domain.CheckIssuer(actor, holders); err != nil {
					return gateProblem(err)
				}
			}
			issuedBy = &actor
		case domain.PermitClosed, domain.PermitCanceled:
			if err := domain.LocksOpen(cur.LocksOpen); err != nil {
				return gateProblem(err)
			}
			if to == domain.PermitClosed {
				wo, err := getWorkOrder(ctx, tx, t, cur.WorkOrderID, false)
				if err != nil {
					return err
				}
				if wo.Status == string(domain.StatusInProgress) {
					return problem(422, domain.CodeWorkInProgress,
						"work order is IN_PROGRESS; complete it or put it on hold before closing the permit")
				}
			}
			closedBy = &actor
		}
		if _, err := tx.Exec(ctx, `update permit set status = $3, version = version + 1, updated_at = $8,
		    approved_by = coalesce($4, approved_by), issued_by = coalesce($5, issued_by), closed_by = coalesce($6, closed_by)
		  where tenant_id = $1 and id = $2 and version = $7`,
			t, id, string(to), approvedBy, issuedBy, closedBy, version, now); err != nil {
			return err
		}
		if err := recordPermitTransition(ctx, tx, t, id, cur.WorkOrderID, string(from), to, actor, now, key); err != nil {
			return err
		}
		out, err = getPermit(ctx, tx, t, id, false)
		return err
	})
	return out, err
}

// ExpirePermits is the eam-permit-expiry job (8.44.20): every ISSUED permit
// past valid_to moves to SUSPENDED (BR-44.11), with its audit row and event in
// the same transaction, and the result flags work orders still IN_PROGRESS
// (EAM-PMT-004). Running it again finds nothing more to do.
func (s *Postgres) ExpirePermits(ctx context.Context) ([]ExpiredPermit, error) {
	out := []ExpiredPermit{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		now := s.now()
		rows, err := tx.Query(ctx, `select p.id::text, p.number, p.work_order_id::text, p.valid_to, w.status = 'IN_PROGRESS'
		  from permit p join work_order w on w.tenant_id = p.tenant_id and w.id = p.work_order_id
		  where p.tenant_id = $1 and p.status = 'ISSUED' and p.valid_to <= $2
		  order by p.valid_to, p.id for update of p`, t, now)
		if err != nil {
			return err
		}
		var due []ExpiredPermit
		for rows.Next() {
			var e ExpiredPermit
			if err := rows.Scan(&e.PermitID, &e.Number, &e.WorkOrderID, &e.ValidTo, &e.WorkOrderInProgress); err != nil {
				rows.Close()
				return err
			}
			e.Code = domain.CodePermitExpired
			due = append(due, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range due {
			if _, err := tx.Exec(ctx, `update permit set status = 'SUSPENDED', version = version + 1, updated_at = $3
			  where tenant_id = $1 and id = $2`, t, e.PermitID, now); err != nil {
				return err
			}
			if err := recordPermitTransition(ctx, tx, t, e.PermitID, e.WorkOrderID, string(domain.PermitIssued),
				domain.PermitSuspended, ExpirySystemActor, now, ""); err != nil {
				return err
			}
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

const lockColumns = `id::text, permit_id::text, isolation_point_id::text, technician_id, lock_number, state,
  applied_at, verified_at, removed_at`

func scanLock(row pgx.Row) (LotoLock, error) {
	var l LotoLock
	err := row.Scan(&l.ID, &l.PermitID, &l.IsolationPointID, &l.TechnicianID, &l.LockNumber, &l.State,
		&l.AppliedAt, &l.VerifiedAt, &l.RemovedAt)
	return l, err
}

func getLock(ctx context.Context, tx pgx.Tx, t, id string) (LotoLock, error) {
	l, err := scanLock(tx.QueryRow(ctx, `select `+lockColumns+` from loto_lock where tenant_id = $1 and id = $2`, t, id))
	return l, notFound(err, "lock")
}

// RecordLock applies, verifies (the zero-energy check) or removes the
// technician's personal lock on an isolation point of the permit's asset
// (BR-44.11). Each action is logged append-only with its key; a repeated key
// returns the lock as it is now, and verifying a verified lock is a no-op.
func (s *Postgres) RecordLock(ctx context.Context, permitID string, action domain.LockAction,
	pointID, technicianID, key string) (LotoLock, error) {
	var out LotoLock
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		permit, err := getPermit(ctx, tx, t, permitID, true)
		if err != nil {
			return err
		}
		if key != "" {
			var lockID string
			err := tx.QueryRow(ctx, `select lock_id::text from loto_lock_action where tenant_id = $1 and idempotency_key = $2`,
				t, key).Scan(&lockID)
			if err == nil {
				l, err := getLock(ctx, tx, t, lockID)
				if err != nil {
					return err
				}
				if l.PermitID != permitID {
					return problem(422, CodeKeyReuse, "Idempotency-Key was already used for another permit")
				}
				out = l
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if !domain.PermitStatus(permit.Status).Lockable() {
			return problem(422, domain.CodePermitState, "locks need an APPROVED, ISSUED or SUSPENDED permit; this one is "+permit.Status)
		}
		ip, err := getIsolationPoint(ctx, tx, t, pointID)
		if err != nil {
			return err
		}
		if ip.AssetID != permit.AssetID {
			return problem(422, CodeValidation, "isolation point belongs to another asset")
		}
		open, err := scanLock(tx.QueryRow(ctx, `select `+lockColumns+` from loto_lock
		  where tenant_id = $1 and permit_id = $2 and isolation_point_id = $3 and technician_id = $4
		    and state <> 'REMOVED' for update`, t, permitID, pointID, technicianID))
		current := domain.LockState(open.State)
		if errors.Is(err, pgx.ErrNoRows) {
			current = ""
		} else if err != nil {
			return err
		}
		if action == domain.LockApply && current != "" {
			return problem(422, domain.CodeLockHeld, "you already hold lock "+open.LockNumber+" on isolation point "+ip.TagCode)
		}
		if action != domain.LockApply && current == "" {
			return problem(422, domain.CodePermitState, "you hold no open lock on isolation point "+ip.TagCode)
		}
		next, err := domain.NextLockState(current, action)
		if err != nil {
			return gateProblem(err)
		}
		now := s.now()
		lockID := open.ID
		switch {
		case action == domain.LockApply:
			lockID = ids.New()
			if _, err := tx.Exec(ctx, `insert into loto_lock (tenant_id, id, permit_id, isolation_point_id, technician_id,
			    lock_number, state, applied_at)
			  values ($1, $2, $3, $4, $5, 'LOCK-' || nextval('lock_number_seq'), 'APPLIED', $6)`,
				t, lockID, permitID, pointID, technicianID, now); err != nil {
				return err
			}
		case next != current:
			var verified, removed *time.Time
			if next == domain.LockVerified {
				verified = &now
			} else {
				removed = &now
			}
			if _, err := tx.Exec(ctx, `update loto_lock set state = $3,
			    verified_at = coalesce($4, verified_at), removed_at = coalesce($5, removed_at)
			  where tenant_id = $1 and id = $2`, t, lockID, string(next), verified, removed); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `insert into loto_lock_action (tenant_id, id, lock_id, action, actor, at, idempotency_key)
		  values ($1, $2, $3, $4, $5, $6, $7)`, t, ids.New(), lockID, string(action), technicianID, now, optStr(key)); err != nil {
			return err
		}
		out, err = getLock(ctx, tx, t, lockID)
		return err
	})
	return out, err
}

// GetPermit returns a permit with its isolation points, locks and history.
func (s *Postgres) GetPermit(ctx context.Context, id string) (PermitDetail, error) {
	var out PermitDetail
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		p, err := getPermit(ctx, tx, t, id, false)
		if err != nil {
			return err
		}
		out.Permit = p
		if out.IsolationPoints, err = isolationPoints(ctx, tx, t, p.AssetID); err != nil {
			return err
		}
		out.Locks = []LotoLock{}
		lrows, err := tx.Query(ctx, `select `+lockColumns+` from loto_lock
		  where tenant_id = $1 and permit_id = $2 order by applied_at, id`, t, p.ID)
		if err != nil {
			return err
		}
		for lrows.Next() {
			l, err := scanLock(lrows)
			if err != nil {
				lrows.Close()
				return err
			}
			out.Locks = append(out.Locks, l)
		}
		lrows.Close()
		if err := lrows.Err(); err != nil {
			return err
		}
		out.History = []StatusChange{}
		hrows, err := tx.Query(ctx, `select from_status, to_status, at, actor from permit_transition
		  where tenant_id = $1 and permit_id = $2 order by at, id`, t, p.ID)
		if err != nil {
			return err
		}
		for hrows.Next() {
			var c StatusChange
			var actor string
			if err := hrows.Scan(&c.From, &c.To, &c.At, &actor); err != nil {
				hrows.Close()
				return err
			}
			c.Actor = optStr(actor)
			out.History = append(out.History, c)
		}
		hrows.Close()
		if err := hrows.Err(); err != nil {
			return err
		}
		_, out.UnlockedPoints, err = isolationState(ctx, tx, t, p.AssetID, p.ID)
		return err
	})
	return out, err
}

// ListPermits is the permit register (8.44.12): newest first, filtered by
// status, asset or work order, in cursor pages.
func (s *Postgres) ListPermits(ctx context.Context, opt ListOptions) (Page[Permit], error) {
	var out Page[Permit]
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select `+permitColumns+` from permit p
		  where p.tenant_id = $1 and ($2::text is null or p.status = $2) and ($3::uuid is null or p.asset_id = $3)
		    and ($4::uuid is null or p.work_order_id = $4) and ($5::uuid is null or p.id < $5)
		  order by p.id desc limit $6`,
			t, optStr(opt.Status), optStr(opt.AssetID), optStr(opt.WorkOrderID), optStr(opt.Cursor), opt.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		var items []Permit
		for rows.Next() {
			p, err := scanPermit(rows)
			if err != nil {
				return err
			}
			items = append(items, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = pageOf(items, opt.Limit, func(p Permit) string { return p.ID })
		return nil
	})
	return out, err
}
