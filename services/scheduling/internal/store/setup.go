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
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// Error codes of the slice 2 setup entities (README lists them all).
const (
	CodeNetworkDuplicate  = "SCHED-0108"
	CodeNetworkNotFound   = "SCHED-0109"
	CodeMatrixNotFound    = "SCHED-0110"
	CodeCleanoutDuplicate = "SCHED-0111"
	CodeBlockDuplicate    = "SCHED-0112"
	CodeMatrixConflict    = "SCHED-0119"
)

// Network is a resource network with its allowed transitions (FR-23.13).
// Dates are YYYY-MM-DD.
type Network struct {
	ID         string               `json:"id"`
	Code       string               `json:"code"`
	Name       string               `json:"name"`
	ActiveFrom string               `json:"activeFrom"`
	ActiveTo   *string              `json:"activeTo"`
	Edges      []domain.NetworkEdge `json:"edges"`
	Version    int                  `json:"version"`
	CreatedAt  time.Time            `json:"createdAt"`
}

// SetupMatrix is one version of a resource's changeover matrix (FR-23.15).
// Entry costs are in Currency.
type SetupMatrix struct {
	ID         string              `json:"id"`
	ResourceID string              `json:"resourceId"`
	Version    int                 `json:"version"`
	ActiveFrom string              `json:"activeFrom"`
	Currency   string              `json:"currency"`
	Entries    []domain.SetupEntry `json:"entries"`
	CreatedAt  time.Time           `json:"createdAt"`
}

// CleanoutRule is the cleanout definition of a setup group (FR-23.15, BR-23.13).
type CleanoutRule struct {
	ID              string    `json:"id"`
	SetupGroup      string    `json:"setupGroup"`
	DurationMinutes int       `json:"durationMinutes"`
	OperationSetID  string    `json:"operationSetId,omitempty"`
	AfterBatches    *int      `json:"afterBatches"`
	AfterHours      *int      `json:"afterHours"`
	AtCampaignEnd   bool      `json:"atCampaignEnd"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"createdAt"`
}

// Domain converts the stored rule for the planner (hours to minutes).
func (r CleanoutRule) Domain() domain.CleanoutRule {
	out := domain.CleanoutRule{SetupGroup: r.SetupGroup, DurationMinutes: r.DurationMinutes, CleaningSetID: r.OperationSetID, AtCampaignEnd: r.AtCampaignEnd}
	if r.AfterBatches != nil {
		out.AfterBatches = *r.AfterBatches
	}
	if r.AfterHours != nil {
		out.AfterMinutes = *r.AfterHours * 60
	}
	return out
}

// CapacityBlock reserves weekly windows (site-local shifts in the resource's
// time zone) or one dated window for jobs matching Condition (FR-23.17).
type CapacityBlock struct {
	ID         string             `json:"id"`
	ResourceID string             `json:"resourceId"`
	Code       string             `json:"code"`
	Windows    []domain.Shift     `json:"windows,omitempty"`
	StartAt    *time.Time         `json:"startAt,omitempty"`
	EndAt      *time.Time         `json:"endAt,omitempty"`
	Condition  []domain.Predicate `json:"condition"`
	Priority   int                `json:"priority"`
	Active     bool               `json:"active"`
	Version    int                `json:"version"`
	CreatedAt  time.Time          `json:"createdAt"`
}

// SetupStore is the slice 2 setup part of the Store.
type SetupStore interface {
	CreateNetwork(ctx context.Context, in Network, idem Idem) (Network, bool, error)
	ListNetworks(ctx context.Context) ([]Network, error)
	GetNetwork(ctx context.Context, id string) (Network, error)
	CreateSetupMatrix(ctx context.Context, in SetupMatrix, idem Idem) (SetupMatrix, bool, error)
	ListSetupMatrices(ctx context.Context, resourceID string) ([]SetupMatrix, error)
	GetSetupMatrix(ctx context.Context, id string) (SetupMatrix, error)
	CreateCleanoutRule(ctx context.Context, in CleanoutRule, idem Idem) (CleanoutRule, bool, error)
	ListCleanoutRules(ctx context.Context) ([]CleanoutRule, error)
	CreateCapacityBlock(ctx context.Context, in CapacityBlock, idem Idem) (CapacityBlock, bool, error)
	ListCapacityBlocks(ctx context.Context, resourceID string) ([]CapacityBlock, error)
}

// requireResources refuses ids that are not resources of the tenant.
func requireResources(ctx context.Context, tx pgx.Tx, rids []string) error {
	want := distinct(rids)
	var known int
	if err := tx.QueryRow(ctx, `select count(*) from scheduling_resource where id = any($1::uuid[])`, want).Scan(&known); err != nil {
		return err
	}
	if known != len(want) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownResource, "every resource must exist")
	}
	return nil
}

// distinct returns the values of in without repeats, in first-seen order.
func distinct(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// replayRow looks up a stored row by its Idempotency-Key. found is true when
// the same request already created it; a different body is refused.
func replayRow(ctx context.Context, tx pgx.Tx, table string, idem Idem) (id string, found bool, err error) {
	if idem.Key == "" {
		return "", false, nil
	}
	var fp string
	err = tx.QueryRow(ctx, `select id::text, idempotency_fingerprint from `+table+` where idempotency_key = $1`, idem.Key).Scan(&id, &fp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if fp != idem.Fingerprint {
		return "", false, reuseProblem()
	}
	return id, true, nil
}

const networkCols = "id::text, code, name, active_from::text, active_to::text, version, created_at"

func scanNetwork(row pgx.Row) (Network, error) {
	var n Network
	err := row.Scan(&n.ID, &n.Code, &n.Name, &n.ActiveFrom, &n.ActiveTo, &n.Version, &n.CreatedAt)
	n.Edges = []domain.NetworkEdge{}
	return n, err
}

// CreateNetwork stores a network and its edges. Every resource an edge names
// must exist; the domain has already checked the edge rules.
func (s *Postgres) CreateNetwork(ctx context.Context, in Network, idem Idem) (Network, bool, error) {
	var out Network
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayRow(ctx, tx, "scheduling_resource_network", idem)
		if err != nil {
			return err
		}
		if !found {
			var rids []string
			for _, e := range in.Edges {
				rids = append(rids, e.From, e.To)
				if e.SecondaryResourceID != "" {
					rids = append(rids, e.SecondaryResourceID)
				}
			}
			if err := requireResources(ctx, tx, rids); err != nil {
				return err
			}
			id = ids.New()
			if _, err := tx.Exec(ctx, `insert into scheduling_resource_network (tenant_id, id, code, name, active_from, active_to, idempotency_key, idempotency_fingerprint)
				values ($1, $2, $3, $4, $5::date, $6::date, $7, $8)`,
				tenantOf(ctx), id, in.Code, in.Name, in.ActiveFrom, in.ActiveTo, nullable(idem.Key), nullable(idem.Fingerprint)); err != nil {
				return err
			}
			for _, e := range in.Edges {
				if _, err := tx.Exec(ctx, `insert into scheduling_network_edge (tenant_id, network_id, from_resource_id, to_resource_id, transfer_minutes, max_wait_minutes, secondary_resource_id)
					values ($1, $2, $3, $4, $5, $6, $7)`, tenantOf(ctx), id, e.From, e.To, e.TransferMinutes, e.MaxWaitMinutes, nullable(e.SecondaryResourceID)); err != nil {
					return err
				}
			}
			created = true
		}
		nets, err := loadNetworks(ctx, tx, id)
		if err != nil {
			return err
		}
		out = nets[0]
		return nil
	})
	if err != nil {
		return Network{}, false, mapWriteErr(err, CodeNetworkDuplicate, "a network with this code already exists")
	}
	return out, created, nil
}

// loadNetworks returns the network with id, or every network ordered by code
// when id is empty, each with its edges.
func loadNetworks(ctx context.Context, tx pgx.Tx, id string) ([]Network, error) {
	rows, err := tx.Query(ctx, `select `+networkCols+` from scheduling_resource_network where ($1 = '' or id::text = $1) order by code`, id)
	if err != nil {
		return nil, err
	}
	out := []Network{}
	index := map[string]int{}
	for rows.Next() {
		n, err := scanNetwork(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		index[n.ID] = len(out)
		out = append(out, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if id != "" && len(out) == 0 {
		return nil, httpx.NewProblem(http.StatusNotFound, CodeNetworkNotFound, "resource network not found")
	}
	rows, err = tx.Query(ctx, `select network_id::text, from_resource_id::text, to_resource_id::text, transfer_minutes, max_wait_minutes, coalesce(secondary_resource_id::text, '')
		from scheduling_network_edge where ($1 = '' or network_id::text = $1) order by network_id, transfer_minutes, from_resource_id, to_resource_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var nid string
		var e domain.NetworkEdge
		if err := rows.Scan(&nid, &e.From, &e.To, &e.TransferMinutes, &e.MaxWaitMinutes, &e.SecondaryResourceID); err != nil {
			return nil, err
		}
		if i, ok := index[nid]; ok {
			out[i].Edges = append(out[i].Edges, e)
		}
	}
	return out, rows.Err()
}

// ListNetworks returns every network of the tenant with its edges.
func (s *Postgres) ListNetworks(ctx context.Context) ([]Network, error) {
	var out []Network
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadNetworks(ctx, tx, "")
		return err
	})
	return out, err
}

// GetNetwork returns one network with its edges.
func (s *Postgres) GetNetwork(ctx context.Context, id string) (Network, error) {
	var out Network
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		nets, err := loadNetworks(ctx, tx, id)
		if err != nil {
			return err
		}
		out = nets[0]
		return nil
	})
	return out, err
}

// CreateSetupMatrix stores the next version of a resource's setup matrix. The
// resource row is locked so two concurrent creates get versions n and n+1.
func (s *Postgres) CreateSetupMatrix(ctx context.Context, in SetupMatrix, idem Idem) (SetupMatrix, bool, error) {
	var out SetupMatrix
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayRow(ctx, tx, "scheduling_setup_matrix", idem)
		if err != nil {
			return err
		}
		if !found {
			var locked string
			err := tx.QueryRow(ctx, `select id::text from scheduling_resource where id = $1 for update`, in.ResourceID).Scan(&locked)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnknownResource, "the resource must exist")
			}
			if err != nil {
				return err
			}
			id = ids.New()
			if _, err := tx.Exec(ctx, `insert into scheduling_setup_matrix (tenant_id, id, resource_id, version, active_from, currency, idempotency_key, idempotency_fingerprint)
				values ($1, $2, $3, (select coalesce(max(version), 0) + 1 from scheduling_setup_matrix where resource_id = $3), $4::date, $5, $6, $7)`,
				tenantOf(ctx), id, in.ResourceID, in.ActiveFrom, in.Currency, nullable(idem.Key), nullable(idem.Fingerprint)); err != nil {
				return err
			}
			for _, e := range in.Entries {
				if _, err := tx.Exec(ctx, `insert into scheduling_setup_matrix_entry (tenant_id, matrix_id, from_setup_group, to_setup_group, changeover_minutes, changeover_cost, cleaning_operation_set_id)
					values ($1, $2, $3, $4, $5, $6::numeric, $7)`, tenantOf(ctx), id, e.FromGroup, e.ToGroup, e.Minutes, e.Cost.String(), nullable(e.CleaningSetID)); err != nil {
					return err
				}
			}
			created = true
		}
		ms, err := loadMatrices(ctx, tx, id, "")
		if err != nil {
			return err
		}
		out = ms[0]
		return nil
	})
	if err != nil {
		return SetupMatrix{}, false, mapWriteErr(err, CodeMatrixConflict, "a concurrent request created a matrix version for this resource; try again")
	}
	return out, created, nil
}

// loadMatrices returns the matrix with id, or the matrices of resourceID (all
// resources when empty) newest version first, each with its entries.
func loadMatrices(ctx context.Context, tx pgx.Tx, id, resourceID string) ([]SetupMatrix, error) {
	rows, err := tx.Query(ctx, `select id::text, resource_id::text, version, active_from::text, currency::text, created_at from scheduling_setup_matrix
		where ($1 = '' or id::text = $1) and ($2 = '' or resource_id::text = $2) order by resource_id, version desc`, id, resourceID)
	if err != nil {
		return nil, err
	}
	out := []SetupMatrix{}
	index := map[string]int{}
	for rows.Next() {
		var m SetupMatrix
		if err := rows.Scan(&m.ID, &m.ResourceID, &m.Version, &m.ActiveFrom, &m.Currency, &m.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		m.Entries = []domain.SetupEntry{}
		index[m.ID] = len(out)
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if id != "" && len(out) == 0 {
		return nil, httpx.NewProblem(http.StatusNotFound, CodeMatrixNotFound, "setup matrix not found")
	}
	mids := make([]string, 0, len(out))
	for _, m := range out {
		mids = append(mids, m.ID)
	}
	rows, err = tx.Query(ctx, `select matrix_id::text, from_setup_group, to_setup_group, changeover_minutes, changeover_cost::text, coalesce(cleaning_operation_set_id, '')
		from scheduling_setup_matrix_entry where matrix_id = any($1::uuid[]) order by matrix_id, from_setup_group, to_setup_group`, mids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mid, cost string
		var e domain.SetupEntry
		if err := rows.Scan(&mid, &e.FromGroup, &e.ToGroup, &e.Minutes, &cost, &e.CleaningSetID); err != nil {
			return nil, err
		}
		e.Cost = dec(cost)
		out[index[mid]].Entries = append(out[index[mid]].Entries, e)
	}
	return out, rows.Err()
}

// dec parses a numeric selected as text. Database numerics are well formed.
func dec(s string) decimal.Decimal {
	v, _ := decimal.NewFromString(s)
	return v
}

// ListSetupMatrices returns matrices newest version first, optionally for one resource.
func (s *Postgres) ListSetupMatrices(ctx context.Context, resourceID string) ([]SetupMatrix, error) {
	var out []SetupMatrix
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadMatrices(ctx, tx, "", resourceID)
		return err
	})
	return out, err
}

// GetSetupMatrix returns one matrix with its entries.
func (s *Postgres) GetSetupMatrix(ctx context.Context, id string) (SetupMatrix, error) {
	var out SetupMatrix
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ms, err := loadMatrices(ctx, tx, id, "")
		if err != nil {
			return err
		}
		out = ms[0]
		return nil
	})
	return out, err
}

// activeMatrix returns the matrix in force for a resource on a day: the highest
// version whose active_from is on or before it. ok is false when none is.
func activeMatrix(ctx context.Context, tx pgx.Tx, resourceID string, on time.Time) (SetupMatrix, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `select id::text from scheduling_setup_matrix where resource_id = $1 and active_from <= $2::date
		order by version desc limit 1`, resourceID, on.UTC().Format(time.DateOnly)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SetupMatrix{}, false, nil
	}
	if err != nil {
		return SetupMatrix{}, false, err
	}
	ms, err := loadMatrices(ctx, tx, id, "")
	if err != nil {
		return SetupMatrix{}, false, err
	}
	return ms[0], true, nil
}

const cleanoutCols = "id::text, setup_group, duration_minutes, coalesce(operation_set_id, ''), after_batches, after_hours, at_campaign_end, version, created_at"

func scanCleanout(row pgx.Row) (CleanoutRule, error) {
	var r CleanoutRule
	err := row.Scan(&r.ID, &r.SetupGroup, &r.DurationMinutes, &r.OperationSetID, &r.AfterBatches, &r.AfterHours, &r.AtCampaignEnd, &r.Version, &r.CreatedAt)
	return r, err
}

// CreateCleanoutRule stores the cleanout rule of a setup group (one per group).
func (s *Postgres) CreateCleanoutRule(ctx context.Context, in CleanoutRule, idem Idem) (CleanoutRule, bool, error) {
	var out CleanoutRule
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayRow(ctx, tx, "scheduling_cleanout_rule", idem)
		if err != nil {
			return err
		}
		if !found {
			id = ids.New()
			if _, err := tx.Exec(ctx, `insert into scheduling_cleanout_rule (tenant_id, id, setup_group, duration_minutes, operation_set_id, after_batches, after_hours, at_campaign_end, idempotency_key, idempotency_fingerprint)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, tenantOf(ctx), id, in.SetupGroup, in.DurationMinutes, nullable(in.OperationSetID),
				in.AfterBatches, in.AfterHours, in.AtCampaignEnd, nullable(idem.Key), nullable(idem.Fingerprint)); err != nil {
				return err
			}
			created = true
		}
		out, err = scanCleanout(tx.QueryRow(ctx, `select `+cleanoutCols+` from scheduling_cleanout_rule where id = $1`, id))
		return err
	})
	if err != nil {
		return CleanoutRule{}, false, mapWriteErr(err, CodeCleanoutDuplicate, "this setup group already has a cleanout rule")
	}
	return out, created, nil
}

// ListCleanoutRules returns every cleanout rule ordered by setup group.
func (s *Postgres) ListCleanoutRules(ctx context.Context) ([]CleanoutRule, error) {
	out := []CleanoutRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = cleanoutRules(ctx, tx, nil)
		return err
	})
	return out, err
}

// cleanoutRules returns the rules of the given groups, or all when groups is nil.
func cleanoutRules(ctx context.Context, tx pgx.Tx, groups []string) ([]CleanoutRule, error) {
	rows, err := tx.Query(ctx, `select `+cleanoutCols+` from scheduling_cleanout_rule where ($1::boolean or setup_group = any($2::text[])) order by setup_group`,
		groups == nil, groups)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CleanoutRule{}
	for rows.Next() {
		r, err := scanCleanout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const blockCols = "id::text, resource_id::text, code, windows, start_at, end_at, condition, priority, active, version, created_at"

func scanBlock(row pgx.Row) (CapacityBlock, error) {
	var b CapacityBlock
	var windows, condition []byte
	if err := row.Scan(&b.ID, &b.ResourceID, &b.Code, &windows, &b.StartAt, &b.EndAt, &condition, &b.Priority, &b.Active, &b.Version, &b.CreatedAt); err != nil {
		return CapacityBlock{}, err
	}
	if windows != nil {
		if err := json.Unmarshal(windows, &b.Windows); err != nil {
			return CapacityBlock{}, err
		}
	}
	b.Condition = []domain.Predicate{}
	return b, json.Unmarshal(condition, &b.Condition)
}

// CreateCapacityBlock stores a block on a resource (code unique per resource).
func (s *Postgres) CreateCapacityBlock(ctx context.Context, in CapacityBlock, idem Idem) (CapacityBlock, bool, error) {
	var windows []byte
	if len(in.Windows) > 0 {
		var err error
		if windows, err = json.Marshal(in.Windows); err != nil {
			return CapacityBlock{}, false, err
		}
	}
	condition, err := json.Marshal(in.Condition)
	if err != nil {
		return CapacityBlock{}, false, err
	}
	var out CapacityBlock
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayRow(ctx, tx, "scheduling_capacity_block", idem)
		if err != nil {
			return err
		}
		if !found {
			if err := requireResources(ctx, tx, []string{in.ResourceID}); err != nil {
				return err
			}
			id = ids.New()
			if _, err := tx.Exec(ctx, `insert into scheduling_capacity_block (tenant_id, id, resource_id, code, windows, start_at, end_at, condition, priority, active, idempotency_key, idempotency_fingerprint)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`, tenantOf(ctx), id, in.ResourceID, in.Code, windows, in.StartAt, in.EndAt,
				condition, in.Priority, in.Active, nullable(idem.Key), nullable(idem.Fingerprint)); err != nil {
				return err
			}
			created = true
		}
		out, err = scanBlock(tx.QueryRow(ctx, `select `+blockCols+` from scheduling_capacity_block where id = $1`, id))
		return err
	})
	if err != nil {
		return CapacityBlock{}, false, mapWriteErr(err, CodeBlockDuplicate, "this resource already has a block with that code")
	}
	return out, created, nil
}

// ListCapacityBlocks returns blocks by resource, priority (highest first) and code.
func (s *Postgres) ListCapacityBlocks(ctx context.Context, resourceID string) ([]CapacityBlock, error) {
	out := []CapacityBlock{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+blockCols+` from scheduling_capacity_block where ($1 = '' or resource_id::text = $1)
			order by resource_id, priority desc, code`, resourceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBlock(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
