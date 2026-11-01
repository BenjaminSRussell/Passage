package store

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// XrefRow is one legacy-key to Passage-id mapping as the cutover team looks it up.
type XrefRow struct {
	SourceSystem       string    `json:"sourceSystem"`
	Entity             string    `json:"entity"`
	SourceKey          string    `json:"sourceKey"`
	TargetID           string    `json:"targetId"`
	ContentHash        string    `json:"contentHash"`
	MergedIntoTargetID string    `json:"mergedIntoTargetId,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// XrefCursor is the keyset position after the last row of a page.
type XrefCursor struct {
	SourceSystem string `json:"s"`
	Entity       string `json:"e"`
	SourceKey    string `json:"k"`
}

// XrefFilter narrows the cross-reference browse. SourceKey is an exact match, KeyPrefix a literal prefix.
type XrefFilter struct {
	SourceSystem string
	Entity       string
	SourceKey    string
	KeyPrefix    string
	TargetID     string
	After        *XrefCursor
	Limit        int
}

// XrefCount is the number of mapped keys of one entity from one source system.
type XrefCount struct {
	SourceSystem string `json:"sourceSystem"`
	Entity       string `json:"entity"`
	Count        int    `json:"count"`
}

func likePrefix(p string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(p) + "%"
}

// ListXref pages the cross-reference in (source system, entity, source key) order, at most Limit rows (default 50, capped at 201 so a caller can ask for one row more than a page to learn whether another page follows).
// Only the filters that are set become predicates, so a cached generic plan never hides the target-id index behind an "unset or equal" test.
func (s *Postgres) ListXref(ctx context.Context, f XrefFilter) ([]XrefRow, error) {
	p, _ := tenant.FromContext(ctx)
	if f.TargetID != "" && !ids.Valid(f.TargetID) {
		return []XrefRow{}, nil
	}
	args := []any{p.TenantID}
	where := []string{"tenant_id = $1"}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.SourceSystem != "" {
		where = append(where, "source_system = "+arg(f.SourceSystem))
	}
	if f.Entity != "" {
		where = append(where, "source_entity = "+arg(f.Entity))
	}
	if f.SourceKey != "" {
		where = append(where, "source_key = "+arg(f.SourceKey))
	}
	if f.KeyPrefix != "" {
		where = append(where, "source_key like "+arg(likePrefix(f.KeyPrefix)))
	}
	if f.TargetID != "" {
		id := arg(f.TargetID)
		where = append(where, "(target_id = "+id+"::uuid or merged_into_target_id = "+id+"::uuid)")
	}
	if f.After != nil {
		where = append(where, "(source_system, source_entity, source_key) > ("+arg(f.After.SourceSystem)+", "+arg(f.After.Entity)+", "+arg(f.After.SourceKey)+")")
	}
	sql := `select source_system, source_entity, source_key, target_id::text, content_hash,
			coalesce(merged_into_target_id::text, ''), updated_at
		from migration.xref where ` + strings.Join(where, " and ") + `
		order by source_system, source_entity, source_key limit ` + arg(clampLimit(f.Limit))
	out := []XrefRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x XrefRow
			if err := rows.Scan(&x.SourceSystem, &x.Entity, &x.SourceKey, &x.TargetID, &x.ContentHash, &x.MergedIntoTargetID, &x.UpdatedAt); err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// XrefSummary counts mapped keys per source system and entity.
func (s *Postgres) XrefSummary(ctx context.Context) ([]XrefCount, error) {
	p, _ := tenant.FromContext(ctx)
	out := []XrefCount{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_system, source_entity, count(*) from migration.xref
			where tenant_id = $1 group by source_system, source_entity order by source_system, source_entity`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c XrefCount
			if err := rows.Scan(&c.SourceSystem, &c.Entity, &c.Count); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (m *Mem) ListXref(_ context.Context, f XrefFilter) ([]XrefRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []XrefRow{}
	for k, e := range m.xref.Entries {
		switch {
		case f.SourceSystem != "" && k.SourceSystem != f.SourceSystem,
			f.Entity != "" && k.Entity != f.Entity,
			f.SourceKey != "" && k.SourceKey != f.SourceKey,
			f.KeyPrefix != "" && !strings.HasPrefix(k.SourceKey, f.KeyPrefix),
			f.TargetID != "" && e.TargetID != f.TargetID:
			continue
		}
		if f.After != nil && compareXref(k.SourceSystem, k.Entity, k.SourceKey, *f.After) <= 0 {
			continue
		}
		out = append(out, XrefRow{SourceSystem: k.SourceSystem, Entity: k.Entity, SourceKey: k.SourceKey, TargetID: e.TargetID, ContentHash: e.ContentHash})
	}
	slices.SortFunc(out, func(a, b XrefRow) int {
		return compareXref(a.SourceSystem, a.Entity, a.SourceKey, XrefCursor{b.SourceSystem, b.Entity, b.SourceKey})
	})
	return out[:min(len(out), clampLimit(f.Limit))], nil
}

func compareXref(system, entity, key string, c XrefCursor) int {
	if d := strings.Compare(system, c.SourceSystem); d != 0 {
		return d
	}
	if d := strings.Compare(entity, c.Entity); d != 0 {
		return d
	}
	return strings.Compare(key, c.SourceKey)
}

func (m *Mem) XrefSummary(context.Context) ([]XrefCount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := map[[2]string]int{}
	for k := range m.xref.Entries {
		counts[[2]string{k.SourceSystem, k.Entity}]++
	}
	out := []XrefCount{}
	for k, n := range counts {
		out = append(out, XrefCount{SourceSystem: k[0], Entity: k[1], Count: n})
	}
	slices.SortFunc(out, func(a, b XrefCount) int {
		return strings.Compare(a.SourceSystem+"\x00"+a.Entity, b.SourceSystem+"\x00"+b.Entity)
	})
	return out, nil
}
