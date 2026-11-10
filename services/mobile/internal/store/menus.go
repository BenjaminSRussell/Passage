package store

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Menu is a server-driven device menu for one role (a duty the user must
// hold, empty for everyone) and one site (a warehouse, empty for all). The
// most specific matching menu of a key wins.
type Menu struct {
	MenuID      string   `json:"menuId"`
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	WarehouseID string   `json:"warehouseId,omitempty"`
	FlowCodes   []string `json:"flowCodes"`
	Version     int      `json:"version"`
}

const menuColumns = `menu_id::text, key, name, role, coalesce(warehouse_id::text, ''), flow_codes, version`

func scanMenu(row scanner) (Menu, error) {
	var m Menu
	if err := row.Scan(&m.MenuID, &m.Key, &m.Name, &m.Role, &m.WarehouseID, &m.FlowCodes, &m.Version); err != nil {
		return Menu{}, err
	}
	if m.FlowCodes == nil {
		m.FlowCodes = []string{}
	}
	return m, nil
}

// UpsertMenu stores a menu for its key, role and site, bumping its version
// when it already exists.
func (s *Store) UpsertMenu(ctx context.Context, idem Idem, m Menu) (Menu, bool, error) {
	var out Menu
	var replayed bool
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		out, replayed, err = once(ctx, tx, idem, 200, func() (Menu, error) {
			var wh any
			if m.WarehouseID != "" {
				wh = m.WarehouseID
			}
			if m.FlowCodes == nil {
				m.FlowCodes = []string{}
			}
			return scanMenu(tx.QueryRow(ctx, `insert into mobile_menu (tenant_id, menu_id, key, name, role, warehouse_id, flow_codes)
				values ($1, $2, $3, $4, $5, $6::uuid, $7)
				on conflict (tenant_id, key, role, coalesce(warehouse_id, '00000000-0000-0000-0000-000000000000'::uuid))
				do update set name = excluded.name, flow_codes = excluded.flow_codes, version = mobile_menu.version + 1, updated_at = now()
				returning `+menuColumns, tenantID(ctx), ids.New(), m.Key, m.Name, m.Role, wh, m.FlowCodes))
		})
		return err
	})
	return out, replayed, err
}

// ResolveMenu picks the menu of a key for the caller at a site: its role must
// be a duty the caller holds and its site the caller's, and a role-specific
// menu outranks a site-specific one, which outranks the general menu.
func (s *Store) ResolveMenu(ctx context.Context, key, warehouseID string) (Menu, error) {
	p, _ := tenant.FromContext(ctx)
	var candidates []Menu
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+menuColumns+` from mobile_menu where tenant_id = $1 and (key = $2 or menu_id::text = $2)
			and (warehouse_id is null or warehouse_id::text = $3::text)`, tenantID(ctx), key, warehouseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMenu(rows)
			if err != nil {
				return err
			}
			candidates = append(candidates, m)
		}
		return rows.Err()
	})
	if err != nil {
		return Menu{}, err
	}
	candidates = slices.DeleteFunc(candidates, func(m Menu) bool { return m.Role != "" && !p.HasDuty(m.Role) })
	if len(candidates) == 0 {
		return Menu{}, ErrNotFound
	}
	score := func(m Menu) int {
		n := 0
		if m.Role != "" {
			n += 2
		}
		if m.WarehouseID != "" {
			n++
		}
		return n
	}
	slices.SortStableFunc(candidates, func(a, b Menu) int { return score(b) - score(a) })
	return candidates[0], nil
}

func (s *Store) ListMenus(ctx context.Context, limit int) ([]Menu, error) {
	var out []Menu
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+menuColumns+` from mobile_menu where tenant_id = $1 order by key, role, warehouse_id nulls first limit $2`, tenantID(ctx), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMenu(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}
