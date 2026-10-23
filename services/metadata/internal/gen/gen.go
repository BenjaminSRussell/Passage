package gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

// Output is the three artifacts generated for one entity.
type Output struct {
	Migration string
	OpenAPI   string
	Client    string
}

// Files maps artifact file names to contents for the entity.
func (o Output) Files(name string) map[string]string {
	return map[string]string{
		name + ".sql":          o.Migration,
		name + ".openapi.json": o.OpenAPI,
		name + ".client.ts":    o.Client,
	}
}

// Generate validates the entity and renders its migration, OpenAPI 3.1 document and TypeScript client.
func Generate(e *domain.Entity) (Output, error) {
	if vs := domain.Validate(e); len(vs) > 0 {
		errs := make([]error, len(vs))
		for i, v := range vs {
			errs[i] = v
		}
		return Output{}, fmt.Errorf("entity %q: %w", e.Name, errors.Join(errs...))
	}
	doc, err := openAPI(e)
	if err != nil {
		return Output{}, fmt.Errorf("entity %q: %w", e.Name, err)
	}
	return Output{Migration: migrationSQL(e), OpenAPI: doc, Client: tsClient(e)}, nil
}

// LoadDir parses every *.yaml entity definition in dir, in file-name order.
func LoadDir(dir string) ([]*domain.Entity, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("no entity definitions in %q", dir)
	}
	slices.Sort(paths)
	var out []*domain.Entity
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		e, err := domain.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func ident(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func snake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func camel(name string) string {
	parts := strings.Split(name, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func plural(word string) string {
	switch {
	case strings.HasSuffix(word, "s"), strings.HasSuffix(word, "x"):
		return word + "es"
	case strings.HasSuffix(word, "y") && !strings.ContainsAny(word[len(word)-2:len(word)-1], "aeiou"):
		return word[:len(word)-1] + "ies"
	}
	return word + "s"
}

func tableName(entity string) string { return plural(snake(entity)) }

func collectionPath(e *domain.Entity) string {
	return "/v1/" + e.Domain + "/" + plural(strings.ReplaceAll(snake(e.Name), "_", "-"))
}

type prop struct {
	Col, Wire, SQL, TS string
	Schema             map[string]any
}

func props(e *domain.Entity, f domain.Field) []prop {
	p := prop{Col: f.Name, Wire: camel(f.Name), TS: "string", Schema: map[string]any{"type": "string"}}
	switch f.Type {
	case "string":
		p.SQL, p.Schema["maxLength"] = fmt.Sprintf("varchar(%d)", f.Length), f.Length
	case "text":
		p.SQL = "text"
	case "int32", "int64":
		p.SQL, p.TS, p.Schema = map[string]string{"int32": "integer", "int64": "bigint"}[f.Type], "number", map[string]any{"type": "integer", "format": f.Type}
	case "decimal":
		p.SQL, p.Schema["format"] = fmt.Sprintf("numeric(%d,%d)", f.Precision, f.Scale), "decimal"
	case "money":
		p.SQL, p.Schema["format"] = "numeric(20,6)", "decimal"
	case "bool":
		p.SQL, p.TS, p.Schema = "boolean", "boolean", map[string]any{"type": "boolean"}
	case "date", "time":
		p.SQL, p.Schema["format"] = f.Type, f.Type
	case "datetime":
		p.SQL, p.Schema["format"] = "timestamptz", "date-time"
	case "uuid":
		p.SQL, p.Schema["format"] = "uuid", "uuid"
	case "enum":
		names := e.EnumNames(f.Enum)
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = `"` + n + `"`
		}
		p.SQL, p.TS, p.Schema["enum"] = "text", strings.Join(quoted, " | "), names
	case "json":
		p.SQL, p.TS, p.Schema = "jsonb", "unknown", map[string]any{}
	case "bytes":
		p.SQL, p.Schema["format"] = "bytea", "byte"
	}
	if f.PII != "" && f.PII != "none" {
		p.Schema["x-passage-pii"] = f.PII
	}
	if f.Type != "money" {
		return []prop{p}
	}
	currency := prop{Col: f.Name + "_currency", Wire: p.Wire + "Currency", SQL: "char(3)", TS: "string", Schema: map[string]any{"type": "string", "pattern": "^[A-Z]{3}$"}}
	return []prop{p, currency}
}
