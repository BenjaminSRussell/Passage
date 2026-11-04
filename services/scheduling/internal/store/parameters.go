package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

// CodeParametersVersion is returned when the parameters were changed since the
// caller read them.
const CodeParametersVersion = "SCHED-0140"

// Parameters are the tenant's scheduling defaults (PRD 4.23.13). Version is 1
// until the first change, when nothing is stored yet and the built-in defaults
// apply; UpdatedAt is null until then.
type Parameters struct {
	DefaultMode       string     `json:"defaultMode"`
	DefaultDirection  string     `json:"defaultDirection"`
	DefaultFinite     bool       `json:"defaultFinite"`
	DragSnapMinutes   int        `json:"dragSnapMinutes"`
	PropagationOrders int        `json:"propagationOrders"`
	PropagationLevels int        `json:"propagationLevels"`
	Version           int        `json:"version"`
	UpdatedAt         *time.Time `json:"updatedAt"`
}

// Domain converts to the domain value.
func (p Parameters) Domain() domain.Parameters {
	return domain.Parameters{DefaultMode: p.DefaultMode, DefaultDirection: p.DefaultDirection, DefaultFinite: p.DefaultFinite,
		DragSnapMinutes: p.DragSnapMinutes, PropagationOrders: p.PropagationOrders, PropagationLevels: p.PropagationLevels}
}

// ParametersPatch changes the listed fields; nil fields stay as they are.
type ParametersPatch struct {
	DefaultMode       *string
	DefaultDirection  *string
	DefaultFinite     *bool
	DragSnapMinutes   *int
	PropagationOrders *int
	PropagationLevels *int
}

// ParametersStore is the scheduling parameters part of the Store.
type ParametersStore interface {
	GetParameters(ctx context.Context) (Parameters, error)
	UpdateParameters(ctx context.Context, version int, patch ParametersPatch, idem Idem) (Parameters, bool, error)
}

func defaultParameters() Parameters {
	d := domain.DefaultParameters()
	return Parameters{DefaultMode: d.DefaultMode, DefaultDirection: d.DefaultDirection, DefaultFinite: d.DefaultFinite,
		DragSnapMinutes: d.DragSnapMinutes, PropagationOrders: d.PropagationOrders, PropagationLevels: d.PropagationLevels, Version: 1}
}

const parameterCols = `default_mode, default_direction, default_finite, drag_snap_minutes, propagation_orders, propagation_levels, version, updated_at`

func scanParameters(row pgx.Row) (Parameters, error) {
	var p Parameters
	err := row.Scan(&p.DefaultMode, &p.DefaultDirection, &p.DefaultFinite, &p.DragSnapMinutes, &p.PropagationOrders, &p.PropagationLevels, &p.Version, &p.UpdatedAt)
	return p, err
}

// loadParameters reads the tenant's parameters inside tx; without a row it
// returns the built-in defaults.
func loadParameters(ctx context.Context, tx pgx.Tx, lock bool) (Parameters, error) {
	q := `select ` + parameterCols + ` from scheduling_parameters`
	if lock {
		q += ` for update`
	}
	p, err := scanParameters(tx.QueryRow(ctx, q))
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultParameters(), nil
	}
	return p, err
}

// GetParameters returns the tenant's scheduling parameters.
func (s *Postgres) GetParameters(ctx context.Context) (Parameters, error) {
	var out Parameters
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadParameters(ctx, tx, false)
		return err
	})
	return out, err
}

// UpdateParameters changes the parameters under optimistic versioning. The
// first change stores the row. A replay of the same key returns the stored
// answer with replayed=true, even after the version has moved on.
func (s *Postgres) UpdateParameters(ctx context.Context, version int, patch ParametersPatch, idem Idem) (Parameters, bool, error) {
	var out Parameters
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Make sure the row exists, then lock it: a retry that raced the first
		// request waits for it and replays its answer.
		if _, err := tx.Exec(ctx, `insert into scheduling_parameters (tenant_id) values ($1::uuid) on conflict do nothing`, tenantOf(ctx)); err != nil {
			return err
		}
		cur, err := loadParameters(ctx, tx, true)
		if err != nil {
			return err
		}
		if idem.Key != "" {
			found, err := replay(ctx, tx, idem, &out)
			if err != nil || found {
				replayed = found
				return err
			}
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeParametersVersion,
				fmt.Sprintf("scheduling parameters were changed by another user; current version is %d", cur.Version))
		}
		if patch.DefaultMode != nil {
			cur.DefaultMode = *patch.DefaultMode
		}
		if patch.DefaultDirection != nil {
			cur.DefaultDirection = *patch.DefaultDirection
		}
		if patch.DefaultFinite != nil {
			cur.DefaultFinite = *patch.DefaultFinite
		}
		if patch.DragSnapMinutes != nil {
			cur.DragSnapMinutes = *patch.DragSnapMinutes
		}
		if patch.PropagationOrders != nil {
			cur.PropagationOrders = *patch.PropagationOrders
		}
		if patch.PropagationLevels != nil {
			cur.PropagationLevels = *patch.PropagationLevels
		}
		if errs := cur.Domain().Validate(); len(errs) > 0 {
			return httpx.NewProblem(http.StatusBadRequest, "COM-0001", "scheduling parameters failed validation")
		}
		out, err = scanParameters(tx.QueryRow(ctx, `update scheduling_parameters set default_mode = $1, default_direction = $2, default_finite = $3,
			drag_snap_minutes = $4, propagation_orders = $5, propagation_levels = $6, version = version + 1, updated_at = now()
			returning `+parameterCols, cur.DefaultMode, cur.DefaultDirection, cur.DefaultFinite, cur.DragSnapMinutes, cur.PropagationOrders, cur.PropagationLevels))
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "parameters", tenantOf(ctx), schedevents.ParametersUpdated, schedevents.ParametersUpdatedData{
			DefaultMode: out.DefaultMode, DefaultDirection: out.DefaultDirection, DefaultFinite: out.DefaultFinite, DragSnapMinutes: out.DragSnapMinutes,
			PropagationOrders: out.PropagationOrders, PropagationLevels: out.PropagationLevels, Version: out.Version}); err != nil {
			return err
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return Parameters{}, false, mapWriteErr(err, CodeIdempotencyReuse, "a concurrent request used the same Idempotency-Key; retry")
	}
	return out, replayed, nil
}
