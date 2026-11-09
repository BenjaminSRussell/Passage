package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type column struct{ Name, Type string }

type table struct {
	Name    string
	Columns []column
	Key     []string
	Parents []string
}

type TableResult struct {
	Table     string
	Source    int
	Inserted  int
	Updated   int
	Unchanged int
	LiveOnly  int
}

type Result struct {
	Applied bool
	Tables  []TableResult
}

func discover(ctx context.Context, live pgx.Tx, schema string) ([]table, error) {
	rows, err := live.Query(ctx, `
select c.relname, a.attname, format_type(a.atttypid, a.atttypmod)
from pg_class c
join pg_namespace n on n.oid = c.relnamespace
join pg_attribute a on a.attrelid = c.oid and a.attnum > 0 and not a.attisdropped and a.attgenerated = ''
where n.nspname = $1 and c.relkind in ('r', 'p')
  and exists (select 1 from pg_attribute t where t.attrelid = c.oid and t.attname = 'tenant_id' and not t.attisdropped)
order by c.relname, a.attnum`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]*table{}
	for rows.Next() {
		var rel, col, typ string
		if err := rows.Scan(&rel, &col, &typ); err != nil {
			return nil, err
		}
		if byName[rel] == nil {
			byName[rel] = &table{Name: rel}
		}
		byName[rel].Columns = append(byName[rel].Columns, column{col, typ})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(byName) == 0 {
		return nil, fmt.Errorf("schema %q has no table with a tenant_id column", schema)
	}
	if err := loadKeys(ctx, live, schema, byName); err != nil {
		return nil, err
	}
	if err := loadParents(ctx, live, schema, byName); err != nil {
		return nil, err
	}
	return dependencyOrder(byName)
}

func loadKeys(ctx context.Context, live pgx.Tx, schema string, byName map[string]*table) error {
	rows, err := live.Query(ctx, `
select c.relname, a.attname
from pg_index i
join pg_class c on c.oid = i.indrelid
join pg_namespace n on n.oid = c.relnamespace
join pg_attribute a on a.attrelid = c.oid and a.attnum = any(i.indkey)
where n.nspname = $1 and i.indisprimary
order by c.relname, array_position(i.indkey::int2[], a.attnum)`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rel, col string
		if err := rows.Scan(&rel, &col); err != nil {
			return err
		}
		if t := byName[rel]; t != nil {
			t.Key = append(t.Key, col)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range byName {
		if len(t.Key) == 0 {
			return fmt.Errorf("table %s has no primary key; rows cannot be matched", t.Name)
		}
	}
	return nil
}

func loadParents(ctx context.Context, live pgx.Tx, schema string, byName map[string]*table) error {
	rows, err := live.Query(ctx, `
select child.relname, parent.relname
from pg_constraint k
join pg_class child on child.oid = k.conrelid
join pg_class parent on parent.oid = k.confrelid
join pg_namespace n on n.oid = child.relnamespace
where k.contype = 'f' and n.nspname = $1 and child.oid <> parent.oid`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return err
		}
		if byName[child] != nil && byName[parent] != nil {
			byName[child].Parents = append(byName[child].Parents, parent)
		}
	}
	return rows.Err()
}

func dependencyOrder(byName map[string]*table) ([]table, error) {
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var ordered []table
	done := map[string]bool{}
	for len(ordered) < len(names) {
		progressed := false
		for _, n := range names {
			t := byName[n]
			if done[n] || !allDone(t.Parents, done) {
				continue
			}
			done[n] = true
			ordered = append(ordered, *t)
			progressed = true
		}
		if !progressed {
			return nil, errors.New("foreign keys between tenant tables form a cycle; copy order cannot be derived")
		}
	}
	return ordered, nil
}

func allDone(parents []string, done map[string]bool) bool {
	for _, p := range parents {
		if !done[p] {
			return false
		}
	}
	return true
}

func qualified(schema, name string) string { return pgx.Identifier{schema, name}.Sanitize() }

func selectSQL(schema string, t table) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = pgx.Identifier{c.Name}.Sanitize() + "::text"
	}
	return fmt.Sprintf("select %s from %s where tenant_id = $1::uuid", strings.Join(cols, ", "), qualified(schema, t.Name))
}

func upsertSQL(schema string, t table) string {
	var names, values, assign, compareLive, compareNew []string
	key := map[string]bool{}
	for _, k := range t.Key {
		key[k] = true
	}
	conflict := make([]string, len(t.Key))
	for i, k := range t.Key {
		conflict[i] = pgx.Identifier{k}.Sanitize()
	}
	for i, c := range t.Columns {
		id := pgx.Identifier{c.Name}.Sanitize()
		names = append(names, id)
		values = append(values, fmt.Sprintf("$%d::text::%s", i+1, c.Type))
		if !key[c.Name] {
			assign = append(assign, id+" = excluded."+id)
			compareLive = append(compareLive, "t."+id+"::text")
			compareNew = append(compareNew, "excluded."+id+"::text")
		}
	}
	head := fmt.Sprintf("insert into %s as t (%s) values (%s) on conflict (%s)", qualified(schema, t.Name), strings.Join(names, ", "), strings.Join(values, ", "), strings.Join(conflict, ", "))
	if len(assign) == 0 {
		return head + " do nothing returning true"
	}
	return fmt.Sprintf("%s do update set %s where (%s) is distinct from (%s) returning (xmax = 0)", head, strings.Join(assign, ", "), strings.Join(compareLive, ", "), strings.Join(compareNew, ", "))
}

func scanTexts(rows pgx.Rows, width int) ([]any, error) {
	args := make([]any, width)
	ptrs := make([]any, width)
	for i := range args {
		ptrs[i] = &args[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	return args, nil
}

func copyTable(ctx context.Context, scratch *pgx.Conn, live pgx.Tx, schema, tenantID string, t table) (TableResult, error) {
	res := TableResult{Table: t.Name}
	var before int
	if err := live.QueryRow(ctx, fmt.Sprintf("select count(*) from %s where tenant_id = $1::uuid", qualified(schema, t.Name)), tenantID).Scan(&before); err != nil {
		return res, err
	}
	rows, err := scratch.Query(ctx, selectSQL(schema, t), tenantID)
	if err != nil {
		return res, err
	}
	var batch [][]any
	for rows.Next() {
		args, err := scanTexts(rows, len(t.Columns))
		if err != nil {
			rows.Close()
			return res, err
		}
		batch = append(batch, args)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	upsert := upsertSQL(schema, t)
	for _, args := range batch {
		var inserted bool
		err := live.QueryRow(ctx, upsert, args...).Scan(&inserted)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			res.Unchanged++
		case err != nil:
			return res, fmt.Errorf("%s: %w", t.Name, err)
		case inserted:
			res.Inserted++
		default:
			res.Updated++
		}
	}
	res.Source = len(batch)
	res.LiveOnly = before - (res.Source - res.Inserted)
	return res, nil
}

func RestoreTenant(ctx context.Context, scratch, live *pgx.Conn, schema, tenantID string, apply bool) (Result, error) {
	if !uuidPattern.MatchString(tenantID) {
		return Result{}, fmt.Errorf("tenant %q is not a lower-case uuid", tenantID)
	}
	tx, err := live.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, tenantID); err != nil {
		return Result{}, err
	}
	tables, err := discover(ctx, tx, schema)
	if err != nil {
		return Result{}, err
	}
	result := Result{Applied: apply}
	for _, t := range tables {
		r, err := copyTable(ctx, scratch, tx, schema, tenantID, t)
		if err != nil {
			return Result{}, err
		}
		result.Tables = append(result.Tables, r)
	}
	if !apply {
		return result, nil
	}
	return result, tx.Commit(ctx)
}
