package gen

import (
	"fmt"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func sqlDefault(f domain.Field) string {
	switch {
	case f.Default == "now()":
		return "now()"
	case f.Default == "current_user()":
		return "current_user"
	case f.Type == "string" || f.Type == "text" || f.Type == "enum":
		return sqlString(f.Default)
	}
	return f.Default
}

func migrationSQL(e *domain.Entity) string {
	table := tableName(e.Name)
	lines := []string{ident("tenant_id") + " uuid not null", ident("id") + " uuid not null"}
	if e.PerLegalEntity {
		lines = append(lines, ident("legal_entity_id")+" uuid not null")
	}
	var constraints []string
	for _, f := range e.Fields {
		for i, p := range props(e, f) {
			line := ident(p.Col) + " " + p.SQL
			if f.Required {
				line += " not null"
			}
			if f.Default != "" && i == 0 {
				line += " default " + sqlDefault(f)
			}
			lines = append(lines, line)
		}
		if f.Unique {
			constraints = append(constraints, fmt.Sprintf("constraint %s unique (%s, %s)", ident("uq_"+table+"_"+f.Name), ident("tenant_id"), ident(f.Name)))
		}
		if f.Type == "enum" {
			values := make([]string, 0)
			for _, n := range e.EnumNames(f.Enum) {
				values = append(values, sqlString(n))
			}
			constraints = append(constraints, fmt.Sprintf("constraint %s check (%s in (%s))", ident("ck_"+table+"_"+f.Name), ident(f.Name), strings.Join(values, ", ")))
		}
	}
	lines = append(lines,
		"ext jsonb not null default '{}'",
		"version integer not null default 1",
		"created_at timestamptz not null default now()",
		"created_by uuid not null",
		"updated_at timestamptz not null default now()",
		"updated_by uuid not null",
	)
	if e.SoftDelete {
		lines = append(lines, "deleted_at timestamptz")
	}
	lines = append(lines, "primary key (tenant_id, id)")
	for _, r := range e.Relations {
		action := strings.ReplaceAll(r.OnDelete, "_", " ")
		switch r.OnDelete {
		case "":
			action = "restrict"
		case "set_null":
			action += " (" + ident(r.FromField) + ")"
		}
		constraints = append(constraints, fmt.Sprintf("foreign key (%s, %s) references %s (%s, %s) on delete %s",
			ident("tenant_id"), ident(r.FromField), ident(tableName(r.ToEntity)), ident("tenant_id"), ident(r.ToField), action))
	}
	var b strings.Builder
	b.WriteString("create table " + ident(table) + " (\n  " + strings.Join(append(lines, constraints...), ",\n  ") + "\n);\n")
	for i, ix := range e.Indexes {
		cols := []string{ident("tenant_id")}
		for _, c := range ix.Fields {
			cols = append(cols, ident(c))
		}
		unique := ""
		if ix.Unique {
			unique = "unique "
		}
		where := ""
		if ix.Where != "" {
			where = " where " + ix.Where
		}
		fmt.Fprintf(&b, "create %sindex %s on %s (%s)%s;\n", unique, ident(fmt.Sprintf("ix_%s_%02d", table, i+1)), ident(table), strings.Join(cols, ", "), where)
	}
	b.WriteString(dbx.RLSPolicy(table) + "\n")
	if e.PerLegalEntity {
		b.WriteString(dbx.LegalEntityScopePolicy(table) + "\n")
	}
	return b.String()
}
