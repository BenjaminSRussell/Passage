package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// CreateLocations inserts the locations of one warehouse in one transaction
// with zero usage and no stock. A name already used in the warehouse refuses
// the whole batch with ErrConflict.
func (s *Store) CreateLocations(ctx context.Context, idem Idem, warehouseID string, locs []domain.Location) ([]domain.Location, bool, error) {
	var (
		out      []domain.Location
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() ([]domain.Location, error) {
			created := make([]domain.Location, 0, len(locs))
			for _, l := range locs {
				c := l.Capacity
				if _, err := tx.Exec(ctx, `insert into location (tenant_id, id, warehouse_id, name, zone, pick_sequence, status,
					max_weight, max_volume, max_lps, max_items, allow_mixed_items, fire_section, rack_group, allowed_storage_classes)
					values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
					tenantID(ctx), l.ID, warehouseID, l.Name, l.Zone, l.PickSequence, string(l.Status),
					c.MaxWeight, c.MaxVolume, c.MaxLPs, c.MaxItems, c.AllowMixedItems, l.FireSection, l.RackGroup,
					classes(l.AllowedStorageClasses)); err != nil {
					return nil, err
				}
				l.Version = 1
				l.Used = domain.Usage{Weight: decimal.Zero, Volume: decimal.Zero}
				l.Stock = nil
				created = append(created, l)
			}
			return created, nil
		})
		return err
	})
	return out, replayed, err
}

func classes(c []string) []string {
	if c == nil {
		return []string{}
	}
	return c
}

// LocationFilter narrows ListLocations. AfterName is the keyset cursor: the
// page starts after that name.
type LocationFilter struct {
	WarehouseID string
	Zone        string
	AfterName   string
	Limit       int
}

// ListLocations returns a page of a warehouse's locations ordered by name,
// each with its stock.
func (s *Store) ListLocations(ctx context.Context, f LocationFilter) ([]domain.Location, error) {
	var out []domain.Location
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = queryLocations(ctx, tx, `where warehouse_id = $1 and ($2::text = '' or zone = $2::text) and name > $3::text
			order by name limit $4`, f.WarehouseID, f.Zone, f.AfterName, f.Limit)
		return err
	})
	return out, err
}

const locationColumns = `id::text, name, zone, pick_sequence, status, max_weight::text, max_volume::text, max_lps, max_items,
	allow_mixed_items, used_weight::text, used_volume::text, used_lps, fire_section, rack_group, allowed_storage_classes, version`

// loadLocations reads every location of a warehouse with its stock: the
// candidate set BR-31.1 evaluates.
func loadLocations(ctx context.Context, tx pgx.Tx, warehouseID string) ([]domain.Location, error) {
	return queryLocations(ctx, tx, `where warehouse_id = $1 order by zone, pick_sequence, name`, warehouseID)
}

// queryLocations runs a location query (tail is the where/order clause) and
// attaches each location's stock.
func queryLocations(ctx context.Context, tx pgx.Tx, tail string, args ...any) ([]domain.Location, error) {
	rows, err := tx.Query(ctx, `select `+locationColumns+` from location `+tail, args...)
	if err != nil {
		return nil, err
	}
	var out []domain.Location
	index := map[string]int{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		index[l.ID] = len(out)
		out = append(out, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	idList := make([]string, 0, len(out))
	for _, l := range out {
		idList = append(idList, l.ID)
	}
	stock, err := tx.Query(ctx, `select location_id::text, item_id, qty::text from location_stock where location_id = any($1::uuid[])`, idList)
	if err != nil {
		return nil, err
	}
	defer stock.Close()
	for stock.Next() {
		var loc, item, qty string
		if err := stock.Scan(&loc, &item, &qty); err != nil {
			return nil, err
		}
		q, err := dec(qty)
		if err != nil {
			return nil, err
		}
		l := &out[index[loc]]
		if l.Stock == nil {
			l.Stock = map[string]decimal.Decimal{}
		}
		l.Stock[item] = q
	}
	return out, stock.Err()
}

// readLocation reads one location with its stock under a row lock, for a
// confirmed movement.
func readLocation(ctx context.Context, tx pgx.Tx, id string) (domain.Location, error) {
	locs, err := queryLocations(ctx, tx, `where id = $1 for update`, id)
	if err != nil {
		return domain.Location{}, err
	}
	if len(locs) == 0 {
		return domain.Location{}, pgx.ErrNoRows
	}
	return locs[0], nil
}

// writeLocation stores a location's usage and its stock of item after a move.
func writeLocation(ctx context.Context, tx pgx.Tx, l domain.Location, item string) error {
	if _, err := tx.Exec(ctx, `update location set used_weight = $2, used_volume = $3, used_lps = $4, version = version + 1,
		updated_at = now() where id = $1`, l.ID, l.Used.Weight, l.Used.Volume, l.Used.LPs); err != nil {
		return err
	}
	if q := l.Stock[item]; q.IsPositive() {
		_, err := tx.Exec(ctx, `insert into location_stock (tenant_id, location_id, item_id, qty) values ($1, $2, $3, $4)
			on conflict (tenant_id, location_id, item_id) do update set qty = excluded.qty`, tenantID(ctx), l.ID, item, q)
		return err
	}
	_, err := tx.Exec(ctx, `delete from location_stock where location_id = $1 and item_id = $2`, l.ID, item)
	return err
}

func scanLocation(row scanner) (domain.Location, error) {
	var (
		l                              domain.Location
		status, usedWeight, usedVolume string
		maxWeight, maxVolume           *string
		maxLPs, maxItems               *int
		allowedClasses                 []string
	)
	if err := row.Scan(&l.ID, &l.Name, &l.Zone, &l.PickSequence, &status, &maxWeight, &maxVolume, &maxLPs, &maxItems,
		&l.Capacity.AllowMixedItems, &usedWeight, &usedVolume, &l.Used.LPs, &l.FireSection, &l.RackGroup, &allowedClasses, &l.Version); err != nil {
		return l, err
	}
	l.Status = domain.LocationStatus(status)
	l.Capacity.MaxLPs, l.Capacity.MaxItems = maxLPs, maxItems
	if len(allowedClasses) > 0 {
		l.AllowedStorageClasses = allowedClasses
	}
	var err error
	if l.Capacity.MaxWeight, err = decPtr(maxWeight); err != nil {
		return l, err
	}
	if l.Capacity.MaxVolume, err = decPtr(maxVolume); err != nil {
		return l, err
	}
	if l.Used.Weight, err = dec(usedWeight); err != nil {
		return l, err
	}
	if l.Used.Volume, err = dec(usedVolume); err != nil {
		return l, err
	}
	return l, nil
}

// CreateDirective stores a location directive. A sequence already used for
// the same warehouse, work type and direction is ErrConflict.
func (s *Store) CreateDirective(ctx context.Context, idem Idem, d domain.Directive) (domain.Directive, bool, error) {
	var (
		out      domain.Directive
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() (domain.Directive, error) {
			query, err := json.Marshal(d.Query)
			if err != nil {
				return d, err
			}
			if d.Query == nil {
				query = []byte("{}")
			}
			lines, err := json.Marshal(d.Bands)
			if err != nil {
				return d, err
			}
			_, err = tx.Exec(ctx, `insert into location_directive (tenant_id, id, warehouse_id, sequence, work_type, direction, query, lines)
				values ($1,$2,$3,$4,$5,$6,$7,$8)`,
				tenantID(ctx), d.ID, d.Warehouse, d.Sequence, d.WorkType, string(d.Direction), query, lines)
			return d, err
		})
		return err
	})
	return out, replayed, err
}

// ListDirectives returns the directives of a warehouse in evaluation order.
// An empty workType or direction matches all.
func (s *Store) ListDirectives(ctx context.Context, warehouseID, workType, direction string) ([]domain.Directive, error) {
	var out []domain.Directive
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadDirectives(ctx, tx, warehouseID, workType, direction)
		return err
	})
	return out, err
}

// loadDirectives reads the directives that apply to a work type, in sequence order.
func loadDirectives(ctx context.Context, tx pgx.Tx, warehouseID, workType, direction string) ([]domain.Directive, error) {
	rows, err := tx.Query(ctx, `select id::text, sequence, work_type, direction, query, lines
		from location_directive
		where warehouse_id = $1 and ($2::text = '' or work_type = $2::text) and ($3::text = '' or direction = $3::text)
		order by work_type, direction, sequence`, warehouseID, workType, direction)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Directive
	for rows.Next() {
		var d domain.Directive
		var dir string
		var query, lines []byte
		if err := rows.Scan(&d.ID, &d.Sequence, &d.WorkType, &dir, &query, &lines); err != nil {
			return nil, err
		}
		d.Warehouse = warehouseID
		d.Direction = domain.Direction(dir)
		if err := json.Unmarshal(query, &d.Query); err != nil {
			return nil, fmt.Errorf("decode directive %s query: %w", d.ID, err)
		}
		if len(d.Query) == 0 {
			d.Query = nil
		}
		if err := json.Unmarshal(lines, &d.Bands); err != nil {
			return nil, fmt.Errorf("decode directive %s lines: %w", d.ID, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
