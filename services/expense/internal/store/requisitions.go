package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
)

// Problem codes raised by the requisition store (listed in the service README).
const (
	CodeRequisitionNotFound = "EXP-TR-003"
	CodeRequisitionState    = "EXP-TR-004"
	CodeRequisitionVersion  = "EXP-TR-005"
)

// Destination is one place a requisition travels to.
type Destination struct {
	Country string `json:"country"`
	City    string `json:"city"`
}

// Estimate is one estimated cost line of a requisition by expense category, in the
// requisition currency.
type Estimate struct {
	Category string          `json:"category"`
	Amount   decimal.Decimal `json:"amount"`
}

// Requisition is a persisted pre-trip travel requisition (FR-43.16).
type Requisition struct {
	ID            string
	LegalEntityID string
	WorkerID      string
	Purpose       string
	Currency      string
	Status        string
	TripFrom      time.Time
	TripTo        time.Time
	Destinations  []Destination
	Estimates     []Estimate
	Estimated     decimal.Decimal
	RequestedBy   string
	DecidedBy     string
	Version       int
	CreatedAt     time.Time
	SubmittedAt   *time.Time
	DecidedAt     *time.Time
}

// NewRequisition is the input to CreateRequisition.
type NewRequisition struct {
	LegalEntityID string
	WorkerID      string
	Purpose       string
	Currency      string
	TripFrom      time.Time
	TripTo        time.Time
	Destinations  []Destination
	Estimates     []Estimate
}

// RequisitionFilter narrows ListRequisitions. Empty fields do not filter.
type RequisitionFilter struct {
	WorkerID string
	Status   string
}

const requisitionColumns = `id::text, legal_entity_id::text, worker_id::text, purpose, currency::text, status,
  trip_from, trip_to, destinations, estimate_lines, estimated_total::text, requested_by, coalesce(decided_by, ''),
  version, created_at, submitted_at, decided_at`

// CreateRequisition inserts a DRAFT requisition whose estimated total is the sum of
// its estimate lines. A replayed key with the same request hash returns the original
// with replayed=true; the same key with another body is EXP-IDEM-001.
func (s *Store) CreateRequisition(ctx context.Context, key, hash string, in NewRequisition) (Requisition, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Requisition{}, false, err
	}
	if !inScope(p, in.LegalEntityID) {
		return Requisition{}, false, outOfScope()
	}
	dests, err := json.Marshal(nonNilSlice(in.Destinations))
	if err != nil {
		return Requisition{}, false, err
	}
	ests, err := json.Marshal(nonNilSlice(in.Estimates))
	if err != nil {
		return Requisition{}, false, err
	}
	total := decimal.Zero
	for _, e := range in.Estimates {
		total = total.Add(e.Amount)
	}
	var out Requisition
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `insert into travel_requisition(tenant_id, id, legal_entity_id, worker_id, purpose, currency,
			trip_from, trip_to, destinations, estimate_lines, estimated_total, requested_by, create_key, request_hash)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12,$13,$14)
			on conflict (tenant_id, create_key) do nothing returning id::text`,
			p.TenantID, ids.New(), in.LegalEntityID, in.WorkerID, in.Purpose, in.Currency, in.TripFrom, in.TripTo,
			dests, ests, total.String(), p.UserID, key, hash).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			var stored string
			if err := tx.QueryRow(ctx, `select id::text, request_hash from travel_requisition where tenant_id=$1 and create_key=$2`,
				p.TenantID, key).Scan(&id, &stored); err != nil {
				return notFoundRequisition(err)
			}
			if stored != hash {
				return reuse()
			}
			replayed = true
		} else if err != nil {
			return translate(err, CodeIdempotencyReuse)
		}
		out, err = loadRequisition(ctx, tx, p.TenantID, id)
		return err
	})
	if err != nil {
		return Requisition{}, false, err
	}
	return out, replayed, nil
}

// GetRequisition loads one requisition.
func (s *Store) GetRequisition(ctx context.Context, id string) (Requisition, error) {
	p, err := principal(ctx)
	if err != nil {
		return Requisition{}, err
	}
	var out Requisition
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadRequisition(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListRequisitions returns up to limit requisitions after the cursor (an id), in id
// order. The next cursor is empty on the last page.
func (s *Store) ListRequisitions(ctx context.Context, f RequisitionFilter, after string, limit int) ([]Requisition, string, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, "", err
	}
	if after == "" {
		after = ids.Nil
	}
	out := []Requisition{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+requisitionColumns+` from travel_requisition
			where tenant_id=$1 and id > $2::uuid and ($3 = '' or worker_id::text = $3) and ($4 = '' or status = $4)
			order by id limit $5`, p.TenantID, after, f.WorkerID, f.Status, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRequisition(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	out, next := pageOf(out, limit, func(r Requisition) string { return r.ID })
	return out, next, nil
}

// SubmitRequisition moves a DRAFT requisition to SUBMITTED for approval.
func (s *Store) SubmitRequisition(ctx context.Context, id string, c Command) (Requisition, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return Requisition{}, false, err
	}
	return s.moveRequisition(ctx, p.TenantID, id, c, domain.RequisitionSubmitted, "")
}

// DecideRequisition approves or rejects a SUBMITTED requisition (MssApprove). The user
// who created the requisition may not decide it (EXP-SOD-001).
func (s *Store) DecideRequisition(ctx context.Context, id string, approve bool, c Command) (Requisition, bool, error) {
	p, err := decider(ctx)
	if err != nil {
		return Requisition{}, false, err
	}
	target := domain.RequisitionRejected
	if approve {
		target = domain.RequisitionApproved
	}
	return s.moveRequisition(ctx, p.TenantID, id, c, target, p.UserID)
}

// moveRequisition applies one status edge under a row lock. deciderID is set for an
// approver decision and is compared with the requester.
func (s *Store) moveRequisition(ctx context.Context, tenantID, id string, c Command, target domain.RequisitionStatus, deciderID string) (Requisition, bool, error) {
	var out Requisition
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status, requestedBy string
		var version int
		if err := tx.QueryRow(ctx, `select status, version, requested_by from travel_requisition where tenant_id=$1 and id=$2 for update`,
			tenantID, id).Scan(&status, &version, &requestedBy); err != nil {
			return notFoundRequisition(err)
		}
		found, err := priorCommand(ctx, tx, tenantID, c)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = loadRequisition(ctx, tx, tenantID, id)
			return err
		}
		if err := checkVersion(c, version, CodeRequisitionVersion, "requisition"); err != nil {
			return err
		}
		if !domain.CanTransitionRequisition(domain.RequisitionStatus(status), target) {
			return requisitionState(status, target)
		}
		if deciderID != "" && deciderID == requestedBy {
			return ownDecision("requisition")
		}
		if _, err := tx.Exec(ctx, `update travel_requisition set status=$3, version=version+1, updated_at=now(),
			submitted_at = case when $3 = 'SUBMITTED' then now() else submitted_at end,
			decided_at = case when $3 in ('APPROVED', 'REJECTED') then now() else decided_at end,
			decided_by = case when $3 in ('APPROVED', 'REJECTED') then nullif($4, '') else decided_by end
			where tenant_id=$1 and id=$2`, tenantID, id, string(target), deciderID); err != nil {
			return err
		}
		if err := remember(ctx, tx, tenantID, c.Key, c.Hash, http.StatusOK, map[string]string{"requisitionId": id}); err != nil {
			return err
		}
		out, err = loadRequisition(ctx, tx, tenantID, id)
		return err
	})
	if err != nil {
		return Requisition{}, false, err
	}
	return out, replayed, nil
}

func loadRequisition(ctx context.Context, tx pgx.Tx, tenantID, id string) (Requisition, error) {
	r, err := scanRequisition(tx.QueryRow(ctx, `select `+requisitionColumns+` from travel_requisition where tenant_id=$1 and id=$2`, tenantID, id))
	if err != nil {
		return Requisition{}, notFoundRequisition(err)
	}
	return r, nil
}

func scanRequisition(r pgx.Row) (Requisition, error) {
	var q Requisition
	var dests, ests []byte
	var total string
	if err := r.Scan(&q.ID, &q.LegalEntityID, &q.WorkerID, &q.Purpose, &q.Currency, &q.Status,
		&q.TripFrom, &q.TripTo, &dests, &ests, &total, &q.RequestedBy, &q.DecidedBy,
		&q.Version, &q.CreatedAt, &q.SubmittedAt, &q.DecidedAt); err != nil {
		return Requisition{}, err
	}
	q.TripFrom, q.TripTo = utcDay(q.TripFrom), utcDay(q.TripTo)
	if err := json.Unmarshal(dests, &q.Destinations); err != nil {
		return Requisition{}, err
	}
	if err := json.Unmarshal(ests, &q.Estimates); err != nil {
		return Requisition{}, err
	}
	var err error
	q.Estimated, err = decimal.NewFromString(total)
	return q, err
}

func notFoundRequisition(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewProblem(http.StatusNotFound, CodeRequisitionNotFound, "travel requisition not found")
	}
	return err
}

func requisitionState(status string, target domain.RequisitionStatus) error {
	return httpx.NewProblem(http.StatusConflict, CodeRequisitionState,
		"requisition is "+status+" and cannot move to "+string(target))
}

func utcDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
