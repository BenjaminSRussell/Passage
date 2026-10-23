package domain

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	entityNamePattern  = `^[A-Z][A-Za-z0-9]{0,47}$`
	fieldNamePattern   = `^[a-z][a-z0-9_]{0,47}$`
	domainPattern      = `^[a-z][a-z0-9]{0,31}$`
	wherePattern       = `^([a-z][a-z0-9_]{0,47}) is (not )?null$`
	numberPattern      = `^-?[0-9]{1,18}(\.[0-9]{1,12})?$`
	maxStringLength    = 4000
	maxPrecision       = 38
	maxScale           = 12
	firstExtensionCode = 1000
)

type Entity struct {
	Name           string     `yaml:"name"`
	Domain         string     `yaml:"domain"`
	TableGroup     string     `yaml:"table_group"`
	PerLegalEntity bool       `yaml:"per_legal_entity"`
	SoftDelete     bool       `yaml:"soft_delete"`
	Breaking       bool       `yaml:"breaking"`
	Audit          bool       `yaml:"audit"`
	TitleFields    []string   `yaml:"title_fields"`
	Fields         []Field    `yaml:"fields"`
	Enums          []Enum     `yaml:"enums"`
	Relations      []Relation `yaml:"relations"`
	Indexes        []Index    `yaml:"indexes"`
	Hooks          []Hook     `yaml:"hooks"`
}

type Field struct {
	Name       string `yaml:"name"`
	Type       string `yaml:"type"`
	Length     int    `yaml:"length"`
	Precision  int    `yaml:"precision"`
	Scale      int    `yaml:"scale"`
	Enum       string `yaml:"enum"`
	Required   bool   `yaml:"required"`
	Unique     bool   `yaml:"unique"`
	Default    string `yaml:"default"`
	Computed   string `yaml:"computed"`
	Visibility string `yaml:"visibility"`
	PII        string `yaml:"pii"`
	Audit      *bool  `yaml:"audit"`
	Label      string `yaml:"label"`
}

type Enum struct {
	Name   string      `yaml:"name"`
	Values []EnumValue `yaml:"values"`
}

type EnumValue struct {
	Name  string `yaml:"name"`
	Code  int    `yaml:"code"`
	Label string `yaml:"label"`
}

type Relation struct {
	FromField string `yaml:"from_field"`
	ToEntity  string `yaml:"to_entity"`
	ToField   string `yaml:"to_field"`
	OnDelete  string `yaml:"on_delete"`
}

type Index struct {
	Fields []string `yaml:"fields"`
	Unique bool     `yaml:"unique"`
	Where  string   `yaml:"where"`
}

type Hook struct {
	Name string `yaml:"name"`
	Expr string `yaml:"expr"`
}

type Violation struct {
	Path string
	Code string
	Msg  string
}

func (v Violation) Error() string {
	return fmt.Sprintf("%s %s: %s", v.Code, v.Path, v.Msg)
}

func Parse(data []byte) (*Entity, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	var e Entity
	if err := dec.Decode(&e); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, Violation{Path: "$", Code: "MET-0001", Msg: "empty definition"}
		}
		code := "MET-0001"
		if strings.Contains(err.Error(), "not found in type") {
			code = "MET-0002"
		}
		return nil, Violation{Path: "$", Code: code, Msg: err.Error()}
	}
	return &e, nil
}

func matches(pattern, s string) bool {
	return regexp.MustCompile(pattern).MatchString(s)
}

func isReserved(name string) bool {
	return slices.Contains([]string{"id", "tenant_id", "legal_entity_id", "ext", "version", "created_at", "created_by", "updated_at", "updated_by", "deleted_at"}, name)
}

func (e *Entity) EnumNames(name string) []string {
	for _, en := range e.Enums {
		if en.Name == name {
			names := make([]string, len(en.Values))
			for i, v := range en.Values {
				names[i] = v.Name
			}
			return names
		}
	}
	return nil
}

func Validate(e *Entity) []Violation {
	var out []Violation
	add := func(path, code, format string, args ...any) {
		out = append(out, Violation{Path: path, Code: code, Msg: fmt.Sprintf(format, args...)})
	}
	if !matches(entityNamePattern, e.Name) {
		add("name", "MET-0003", "entity name must match %s", entityNamePattern)
	}
	if !matches(domainPattern, e.Domain) {
		add("domain", "MET-0003", "domain must match %s", domainPattern)
	}
	if !slices.Contains([]string{"main", "group", "transaction", "worksheet_header", "worksheet_line", "parameter", "reference"}, e.TableGroup) {
		add("table_group", "MET-0004", "unknown table_group %q", e.TableGroup)
	}
	if len(e.Fields) == 0 {
		add("fields", "MET-0004", "at least one field is required")
	}
	enums := map[string]bool{}
	for i, en := range e.Enums {
		p := fmt.Sprintf("enums[%d]", i)
		if !matches(entityNamePattern, en.Name) {
			add(p+".name", "MET-0003", "enum name must match %s", entityNamePattern)
		}
		if enums[en.Name] || len(en.Values) == 0 {
			add(p, "MET-0007", "enum %q must be unique and have values", en.Name)
		}
		enums[en.Name] = true
		for j, v := range en.Values {
			if !matches(fieldNamePattern, v.Name) {
				add(fmt.Sprintf("%s.values[%d].name", p, j), "MET-0003", "enum value must match %s", fieldNamePattern)
			}
			if v.Code < 0 || v.Code >= firstExtensionCode {
				add(fmt.Sprintf("%s.values[%d].code", p, j), "MET-0007", "sys enum code must be in 0..%d", firstExtensionCode-1)
			}
		}
	}
	fields := map[string]Field{}
	for i, f := range e.Fields {
		p := fmt.Sprintf("fields[%d]", i)
		if !matches(fieldNamePattern, f.Name) {
			add(p+".name", "MET-0003", "field name must match %s", fieldNamePattern)
		} else if _, dup := fields[f.Name]; dup || isReserved(f.Name) {
			add(p+".name", "MET-0009", "field name %q is duplicate or reserved", f.Name)
		}
		fields[f.Name] = f
		if !slices.Contains([]string{"", "always", "advanced", "hidden"}, f.Visibility) || !slices.Contains([]string{"", "none", "personal", "sensitive"}, f.PII) {
			add(p, "MET-0004", "visibility or pii value not allowed")
		}
		switch f.Type {
		case "string":
			if f.Length < 1 || f.Length > maxStringLength {
				add(p+".length", "MET-0006", "string length must be 1..%d", maxStringLength)
			}
		case "decimal":
			if f.Precision < 1 || f.Precision > maxPrecision || f.Scale < 0 || f.Scale > maxScale || f.Scale > f.Precision {
				add(p+".precision", "MET-0006", "decimal needs precision 1..%d and scale 0..%d within precision", maxPrecision, maxScale)
			}
		case "enum":
			if !enums[f.Enum] {
				add(p+".enum", "MET-0007", "enum %q is not declared", f.Enum)
			}
		case "text", "int32", "int64", "money", "bool", "date", "datetime", "time", "uuid", "json", "bytes":
		default:
			add(p+".type", "MET-0004", "unknown type %q", f.Type)
		}
		if msg := defaultProblem(e, f); msg != "" {
			add(p+".default", "MET-0005", "%s", msg)
		}
	}
	for i, f := range e.Fields {
		if _, clash := fields[f.Name+"_currency"]; f.Type == "money" && clash {
			add(fmt.Sprintf("fields[%d].name", i), "MET-0009", "money field %q generates column %q that another field declares", f.Name, f.Name+"_currency")
		}
	}
	for i, t := range e.TitleFields {
		if f, ok := fields[t]; !ok || f.Type != "string" {
			add(fmt.Sprintf("title_fields[%d]", i), "MET-0008", "title field %q must be a declared string field", t)
		}
	}
	for i, r := range e.Relations {
		p := fmt.Sprintf("relations[%d]", i)
		f, ok := fields[r.FromField]
		if !ok || f.Type != "uuid" {
			add(p+".from_field", "MET-0009", "from_field %q must be a declared uuid field", r.FromField)
		}
		if !matches(entityNamePattern, r.ToEntity) || !matches(fieldNamePattern, r.ToField) {
			add(p, "MET-0003", "to_entity and to_field must be valid identifiers")
		}
		if !slices.Contains([]string{"", "restrict", "cascade", "set_null"}, r.OnDelete) || (r.OnDelete == "set_null" && f.Required) {
			add(p+".on_delete", "MET-0009", "on_delete must be restrict, cascade or set_null on a nullable field")
		}
	}
	for i, ix := range e.Indexes {
		p := fmt.Sprintf("indexes[%d]", i)
		if len(ix.Fields) == 0 {
			add(p+".fields", "MET-0009", "index needs fields")
		}
		for _, name := range ix.Fields {
			if _, ok := fields[name]; !ok {
				add(p+".fields", "MET-0009", "unknown field %q", name)
			}
		}
		if ix.Where != "" {
			if m := regexp.MustCompile(wherePattern).FindStringSubmatch(ix.Where); m == nil || fields[m[1]].Name == "" {
				add(p+".where", "MET-0009", "where must be '<field> is [not] null' on a declared field")
			}
		}
	}
	for i, h := range e.Hooks {
		if !slices.Contains([]string{"initValue", "validateField", "validateWrite", "validateDelete", "afterWrite", "afterDelete"}, h.Name) || strings.TrimSpace(h.Expr) == "" {
			add(fmt.Sprintf("hooks[%d]", i), "MET-0004", "hook needs a known name and an expression")
		}
	}
	return out
}

func defaultProblem(e *Entity, f Field) string {
	switch {
	case f.Default == "":
		return ""
	case f.Default == "now()":
		if f.Type != "date" && f.Type != "datetime" {
			return "now() requires a date or datetime field"
		}
		return ""
	case f.Default == "current_user()":
		if f.Type != "string" {
			return "current_user() requires a string field"
		}
		return ""
	}
	switch f.Type {
	case "int32", "int64", "decimal", "money":
		if !matches(numberPattern, f.Default) {
			return "default must be a plain number"
		}
	case "bool":
		if f.Default != "true" && f.Default != "false" {
			return "default must be true or false"
		}
	case "enum":
		if !slices.Contains(e.EnumNames(f.Enum), f.Default) {
			return "default must be a value of the enum"
		}
	case "string", "text":
	default:
		return "literal default is not supported for this type"
	}
	return ""
}
