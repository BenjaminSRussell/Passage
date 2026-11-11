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

type column struct {
	Name, Type string
	Always     bool
}

type selfRef struct{ Cols, RefCols []string }

type table struct {
	Name        string
	Columns     []column
	Key         []string
	Parents     []string
	SelfRefs    []selfRef
	RLS         bool
	Forced      bool
	LiveOwner   bool
	Partitioned bool
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

func requireConstrainedRole(ctx context.Context, live pgx.Tx) error {
	var user string
	var super, bypass bool
	if err := live.QueryRow(ctx, `select r.rolname, r.rolsuper, r.rolbypassrls from pg_roles r where r.rolname = current_user`).Scan(&user, &super, &bypass); err != nil {
		return err
	}
	if super || bypass {
		return fmt.Errorf("live role %q is a superuser or has BYPASSRLS; connect as the application role so row-level security confines the copy to the tenant", user)
	}
	return nil
}

func discover(ctx context.Context, live pgx.Tx, schema string) ([]table, error) {
	rows, err := live.Query(ctx, `
select c.relname, a.attname, format_type(a.atttypid, a.atttypmod), a.attidentity = 'a',
       c.relrowsecurity, c.relforcerowsecurity, pg_has_role(current_user, c.relowner, 'member'), c.relkind = 'p'
from pg_class c
join pg_namespace n on n.oid = c.relnamespace
join pg_attribute a on a.attrelid = c.oid and a.attnum > 0 and not a.attisdropped and a.attgenerated = ''
where n.nspname = $1 and c.relkind in ('r', 'p') and not c.relispartition
  and exists (select 1 from pg_attribute t where t.attrelid = c.oid and t.attname = 'tenant_id' and not t.attisdropped)
order by c.relname, a.attnum`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]*table{}
	for rows.Next() {
		var rel string
		var col column
		var rls, forced, owner, partitioned bool
		if err := rows.Scan(&rel, &col.Name, &col.Type, &col.Always, &rls, &forced, &owner, &partitioned); err != nil {
			return nil, err
		}
		if byName[rel] == nil {
			byName[rel] = &table{Name: rel, RLS: rls, Forced: forced, LiveOwner: owner, Partitioned: partitioned}
		}
		byName[rel].Columns = append(byName[rel].Columns, col)
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
	if err := loadForeignKeys(ctx, live, schema, byName); err != nil {
		return nil, err
	}
	ordered, err := dependencyOrder(byName)
	if err != nil {
		return nil, err
	}
	for _, t := range ordered {
		if !t.RLS || (t.LiveOwner && !t.Forced) {
			return nil, fmt.Errorf("table %s does not enforce row-level security on the live role (enable and FORCE it); refusing to copy", t.Name)
		}
	}
	return ordered, nil
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

func loadForeignKeys(ctx context.Context, live pgx.Tx, schema string, byName map[string]*table) error {
	rows, err := live.Query(ctx, `
select child.relname, parent.relname, child.oid = parent.oid,
  (select array_agg(a.attname::text order by u.ord) from unnest(k.conkey) with ordinality u(num, ord) join pg_attribute a on a.attrelid = k.conrelid and a.attnum = u.num),
  (select array_agg(a.attname::text order by u.ord) from unnest(k.confkey) with ordinality u(num, ord) join pg_attribute a on a.attrelid = k.confrelid and a.attnum = u.num)
from pg_constraint k
join pg_class child on child.oid = k.conrelid
join pg_class parent on parent.oid = k.confrelid
join pg_namespace n on n.oid = child.relnamespace
where k.contype = 'f' and n.nspname = $1`, schema)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var child, parent string
		var self bool
		var cols, refCols []string
		if err := rows.Scan(&child, &parent, &self, &cols, &refCols); err != nil {
			return err
		}
		switch {
		case byName[child] == nil:
		case self:
			byName[child].SelfRefs = append(byName[child].SelfRefs, selfRef{cols, refCols})
		case byName[parent] != nil:
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

func parentFirst(t table, rows [][]any) ([][]any, error) {
	if len(t.SelfRefs) == 0 {
		return rows, nil
	}
	position := map[string]int{}
	for i, c := range t.Columns {
		position[c.Name] = i
	}
	tuple := func(row []any, names []string) (string, bool) {
		parts := make([]string, len(names))
		for i, n := range names {
			p, ok := position[n]
			if !ok || row[p] == nil {
				return "", false
			}
			parts[i] = fmt.Sprint(row[p])
		}
		return strings.Join(parts, "\x00"), true
	}
	referenced := make([]map[string]int, len(t.SelfRefs))
	for k, ref := range t.SelfRefs {
		referenced[k] = map[string]int{}
		for i, row := range rows {
			if key, ok := tuple(row, ref.RefCols); ok {
				referenced[k][key] = i
			}
		}
	}
	state := make([]int, len(rows))
	ordered := make([][]any, 0, len(rows))
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("table %s: rows reference each other in a cycle through a self-referencing foreign key", t.Name)
		}
		state[i] = 1
		for k, ref := range t.SelfRefs {
			key, ok := tuple(rows[i], ref.Cols)
			if !ok {
				continue
			}
			if j, found := referenced[k][key]; found && j != i {
				if err := visit(j); err != nil {
					return err
				}
			}
		}
		state[i] = 2
		ordered = append(ordered, rows[i])
		return nil
	}
	for i := range rows {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return ordered, nil
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
	override := ""
	for i, c := range t.Columns {
		id := pgx.Identifier{c.Name}.Sanitize()
		names = append(names, id)
		values = append(values, fmt.Sprintf("$%d::text::%s", i+1, c.Type))
		if c.Always {
			override = " overriding system value"
		}
		if !key[c.Name] {
			assign = append(assign, id+" = excluded."+id)
			compareLive = append(compareLive, "t."+id+"::text")
			compareNew = append(compareNew, "excluded."+id+"::text")
		}
	}
	head := fmt.Sprintf("insert into %s as t (%s)%s values (%s) on conflict (%s)", qualified(schema, t.Name), strings.Join(names, ", "), override, strings.Join(values, ", "), strings.Join(conflict, ", "))
	if len(assign) == 0 {
		return head + " do nothing returning true"
	}
	returning := "(xmax = 0)"
	if t.Partitioned {
		returning = "true"
	}
	return fmt.Sprintf("%s do update set %s where t.tenant_id = excluded.tenant_id and (%s) is distinct from (%s) returning %s", head, strings.Join(assign, ", "), strings.Join(compareLive, ", "), strings.Join(compareNew, ", "), returning)
}

func ownRowSQL(schema string, t table) (string, []int) {
	position := map[string]int{}
	for i, c := range t.Columns {
		position[c.Name] = i
	}
	var conds []string
	var at []int
	types := map[string]string{}
	for _, c := range t.Columns {
		types[c.Name] = c.Type
	}
	for i, k := range t.Key {
		conds = append(conds, fmt.Sprintf("%s = $%d::text::%s", pgx.Identifier{k}.Sanitize(), i+1, types[k]))
		at = append(at, position[k])
	}
	conds = append(conds, fmt.Sprintf("tenant_id = $%d::uuid", len(t.Key)+1))
	return fmt.Sprintf("select count(*) from %s where %s", qualified(schema, t.Name), strings.Join(conds, " and ")), at
}

func scratchColumns(ctx context.Context, scratch pgx.Tx, schema string, t table) (table, error) {
	rows, err := scratch.Query(ctx, `
select a.attname
from pg_attribute a
join pg_class c on c.oid = a.attrelid
join pg_namespace n on n.oid = c.relnamespace
where n.nspname = $1 and c.relname = $2 and a.attnum > 0 and not a.attisdropped and a.attgenerated = ''`, schema, t.Name)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return t, err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return t, err
	}
	if len(have) == 0 {
		return table{Name: t.Name}, nil
	}
	shared := t
	shared.Columns = nil
	for _, c := range t.Columns {
		if have[c.Name] {
			shared.Columns = append(shared.Columns, c)
		}
	}
	for _, need := range append([]string{"tenant_id"}, t.Key...) {
		if !have[need] {
			return t, fmt.Errorf("table %s: scratch database has no column %s", t.Name, need)
		}
	}
	return shared, nil
}

func readScratch(ctx context.Context, scratch *pgx.Conn, schema, tenantID string, t table) (table, [][]any, error) {
	tx, err := scratch.Begin(ctx)
	if err != nil {
		return t, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, tenantID); err != nil {
		return t, nil, err
	}
	shared, err := scratchColumns(ctx, tx, schema, t)
	if err != nil || len(shared.Columns) == 0 {
		return shared, nil, err
	}
	rows, err := tx.Query(ctx, selectSQL(schema, shared), tenantID)
	if err != nil {
		return shared, nil, err
	}
	defer rows.Close()
	var batch [][]any
	for rows.Next() {
		args := make([]any, len(shared.Columns))
		ptrs := make([]any, len(args))
		for i := range args {
			ptrs[i] = &args[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return shared, nil, err
		}
		batch = append(batch, args)
	}
	return shared, batch, rows.Err()
}

func copyTable(ctx context.Context, scratch *pgx.Conn, live pgx.Tx, schema, tenantID string, t table, masks *Masks) (TableResult, error) {
	res := TableResult{Table: t.Name}
	var before int
	if err := live.QueryRow(ctx, fmt.Sprintf("select count(*) from %s where tenant_id = $1::uuid", qualified(schema, t.Name)), tenantID).Scan(&before); err != nil {
		return res, err
	}
	shared, batch, err := readScratch(ctx, scratch, schema, tenantID, t)
	if err != nil {
		return res, err
	}
	if masks != nil {
		if batch, err = masks.Apply(shared, batch); err != nil {
			return res, err
		}
	}
	if batch, err = parentFirst(shared, batch); err != nil {
		return res, err
	}
	upsert := upsertSQL(schema, shared)
	ownRow, keyAt := ownRowSQL(schema, shared)
	for _, args := range batch {
		own := make([]any, 0, len(keyAt)+1)
		for _, at := range keyAt {
			own = append(own, args[at])
		}
		existed := 0
		if shared.Partitioned {
			if err := live.QueryRow(ctx, ownRow, append(own, tenantID)...).Scan(&existed); err != nil {
				return res, fmt.Errorf("%s: %w", t.Name, err)
			}
		}
		var inserted bool
		err := live.QueryRow(ctx, upsert, args...).Scan(&inserted)
		if shared.Partitioned {
			inserted = existed == 0
		}
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			var mine int
			if err := live.QueryRow(ctx, ownRow, append(own, tenantID)...).Scan(&mine); err != nil {
				return res, fmt.Errorf("%s: %w", t.Name, err)
			}
			if mine == 0 {
				return res, fmt.Errorf("%s: primary key %v already belongs to another tenant in the live database; aborting", t.Name, own)
			}
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
	return restoreTenant(ctx, scratch, live, schema, tenantID, apply, nil)
}

// CloneTenantMasked copies one tenant's rows from source into target like RestoreTenant, replacing the masked columns
// on the way, so a non-production environment gets realistic data without the personal and commercial values.
func CloneTenantMasked(ctx context.Context, source, target *pgx.Conn, schema, tenantID string, apply bool, masks *Masks) (Result, error) {
	if masks == nil {
		return Result{}, errors.New("a masked clone needs a mask configuration")
	}
	same, err := sameDatabase(ctx, source, target)
	if err != nil {
		return Result{}, err
	}
	if same {
		return Result{}, errors.New("the clone's source and target are the same database (same cluster identity and database name); refusing to mask a tenant in place")
	}
	return restoreTenant(ctx, source, target, schema, tenantID, apply, masks)
}

func restoreTenant(ctx context.Context, scratch, live *pgx.Conn, schema, tenantID string, apply bool, masks *Masks) (Result, error) {
	if !uuidPattern.MatchString(tenantID) {
		return Result{}, fmt.Errorf("tenant %q is not a lower-case uuid", tenantID)
	}
	tx, err := live.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := requireConstrainedRole(ctx, tx); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, tenantID); err != nil {
		return Result{}, err
	}
	tables, err := discover(ctx, tx, schema)
	if err != nil {
		return Result{}, err
	}
	if masks != nil {
		if err := masks.Check(tables); err != nil {
			return Result{}, err
		}
	}
	result := Result{Applied: apply}
	for _, t := range tables {
		r, err := copyTable(ctx, scratch, tx, schema, tenantID, t, masks)
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

// sameDatabase reports whether two connections reach the same database, by what the server says it is rather than by how the URL
// is spelled: the cluster's system identifier and the database name. Two spellings of one URL (another host alias, a proxy, a
// port forward) match; a restored copy (a new cluster identity) and another database of the same cluster do not.
func sameDatabase(ctx context.Context, a, b *pgx.Conn) (bool, error) {
	const q = `select (select system_identifier::text from pg_control_system()), current_database()`
	var idA, dbA, idB, dbB string
	if err := a.QueryRow(ctx, q).Scan(&idA, &dbA); err != nil {
		return false, fmt.Errorf("identify the source database: %w", err)
	}
	if err := b.QueryRow(ctx, q).Scan(&idB, &dbB); err != nil {
		return false, fmt.Errorf("identify the target database: %w", err)
	}
	return idA == idB && dbA == dbB, nil
}
