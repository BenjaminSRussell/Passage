package domain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Entity bindings (integration track S2): which table, endpoint or file of a connection profile feeds which Passage
// entity, with the key and the change cursor extraction needs.
const (
	CursorTimestamp  = "timestamp"
	CursorInt        = "int"
	CursorRowversion = "rowversion"

	BindingActive   = "active"
	BindingDisabled = "disabled"

	MaxKeyFields = 5

	// MaxExtractRows bounds one in-memory extraction; a larger source is split by the cursor (a delta) or by file.
	MaxExtractRows = 2_000_000

	// cursorLayout is the fixed-width form timestamp watermarks take, so two of them compare as text.
	cursorLayout = "2006-01-02T15:04:05.000000000Z"
)

// Paging styles of a rest binding.
var PagingStyles = []string{"none", "page", "offset", "cursor", "link", "odata", "skiptoken"}

var (
	entityRe     = regexp.MustCompile(`^[a-z][a-z0-9_.\-]{0,63}$`)
	identRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$#]{0,127}$`)
	fieldPathRe  = regexp.MustCompile(`^[A-Za-z_@$][A-Za-z0-9_$#@.\-]{0,127}$`)
	paramNameRe  = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_.$\-\[\]]{0,63}$`)
	paramValueRe = regexp.MustCompile(`^[\x20-\x7e]{0,200}$`)
)

// SQLSource reads a table (Schema, Table, optional Columns) or runs one SELECT (Query) whose :watermark parameter
// carries the last watermark.
type SQLSource struct {
	Schema  string   `json:"schema,omitempty"`
	Table   string   `json:"table,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Query   string   `json:"query,omitempty"`
}

// Paging describes how a rest endpoint pages. Style defaults to none.
type Paging struct {
	Style       string `json:"style,omitempty"`
	PageSize    int    `json:"pageSize,omitempty"`
	LimitParam  string `json:"limitParam,omitempty"`
	OffsetParam string `json:"offsetParam,omitempty"`
	PageParam   string `json:"pageParam,omitempty"`
	FirstPage   *int   `json:"firstPage,omitempty"`
	CursorParam string `json:"cursorParam,omitempty"`
	CursorPath  string `json:"cursorPath,omitempty"`
}

// RESTSource reads Path under the profile's base URL; RecordsPath says where the records are in the JSON body.
type RESTSource struct {
	Path              string            `json:"path"`
	RecordsPath       string            `json:"recordsPath,omitempty"`
	Params            map[string]string `json:"params,omitempty"`
	ChangedSinceParam string            `json:"changedSinceParam,omitempty"`
	Paging            Paging            `json:"paging,omitempty"`
}

// FileSource names a file in the profile's drop directory (File) or the newest by name of a pattern.
type FileSource struct {
	File        string `json:"file,omitempty"`
	FilePattern string `json:"filePattern,omitempty"`
	RecordsPath string `json:"recordsPath,omitempty"`
}

// EntityBinding is the validated content of a binding.
type EntityBinding struct {
	Entity      string
	Source      map[string]any
	KeyFields   []string
	CursorField string
	CursorType  string
}

func decodeStrict(src map[string]any, into any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

// ParseSQLSource, ParseRESTSource and ParseFileSource decode a binding's source object strictly.
func ParseSQLSource(src map[string]any) (SQLSource, error) {
	var s SQLSource
	return s, decodeStrict(src, &s)
}

func ParseRESTSource(src map[string]any) (RESTSource, error) {
	var s RESTSource
	return s, decodeStrict(src, &s)
}

func ParseFileSource(src map[string]any) (FileSource, error) {
	var s FileSource
	return s, decodeStrict(src, &s)
}

// ValidateBinding returns a message per invalid field for a binding over a profile of the given kind.
func ValidateBinding(b EntityBinding, kind string) map[string]string {
	bad := map[string]string{}
	if !entityRe.MatchString(b.Entity) {
		bad["entity"] = "must be lowercase letters, digits, _ . or - starting with a letter, at most 64 characters"
	}
	if n := len(b.KeyFields); n < 1 || n > MaxKeyFields {
		bad["keyFields"] = "needs one to five fields"
	}
	for _, k := range b.KeyFields {
		if !fieldPathRe.MatchString(k) {
			bad["keyFields"] = "each key field must be a column or a dotted JSON path"
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(b.KeyFields)))) != len(b.KeyFields) {
		bad["keyFields"] = "must not repeat a field"
	}
	switch {
	case b.CursorField == "" && b.CursorType != "":
		bad["cursorField"] = "is required with cursorType"
	case b.CursorField != "" && !slices.Contains([]string{CursorTimestamp, CursorInt, CursorRowversion}, b.CursorType):
		bad["cursorType"] = "must be timestamp, int or rowversion when cursorField is set"
	case b.CursorField != "" && !fieldPathRe.MatchString(b.CursorField):
		bad["cursorField"] = "must be a column or a dotted JSON path"
	}
	switch kind {
	case ConnSQL:
		validateSQLSource(b, bad)
	case ConnREST:
		validateRESTSource(b, bad)
	case ConnFile:
		validateFileSource(b, bad)
	default:
		bad["connectionProfileId"] = "names a profile of an unknown kind"
	}
	return bad
}

func validateSQLSource(b EntityBinding, bad map[string]string) {
	s, err := ParseSQLSource(b.Source)
	if err != nil {
		bad["source"] = "for a sql profile may hold schema, table, columns or query only: " + err.Error()
		return
	}
	for _, f := range append(slices.Clone(b.KeyFields), b.CursorField) {
		if f != "" && !identRe.MatchString(f) {
			bad["source"] = "key and cursor fields of a sql binding must be plain column names"
		}
	}
	switch {
	case s.Query != "" && (s.Table != "" || s.Schema != "" || len(s.Columns) > 0):
		bad["source"] = "give a table or a query, not both"
	case s.Query != "":
		if err := ValidateSelect(s.Query); err != nil {
			bad["source.query"] = err.Error()
		}
	case s.Table == "":
		bad["source"] = "needs a table or a query"
	default:
		if !identRe.MatchString(s.Table) || (s.Schema != "" && !identRe.MatchString(s.Schema)) {
			bad["source.table"] = "schema and table must be plain names (letters, digits, _ $ #)"
		}
		if len(s.Columns) > 0 {
			need := append(slices.Clone(b.KeyFields), b.CursorField)
			for _, c := range s.Columns {
				if !identRe.MatchString(c) {
					bad["source.columns"] = "must be plain column names"
				}
			}
			for _, n := range need {
				if n != "" && !slices.Contains(s.Columns, n) {
					bad["source.columns"] = "must include the key fields and the cursor field (" + n + ")"
				}
			}
		}
		if len(s.Columns) > 200 {
			bad["source.columns"] = "at most 200 columns"
		}
	}
}

func validateRESTSource(b EntityBinding, bad map[string]string) {
	s, err := ParseRESTSource(b.Source)
	if err != nil {
		bad["source"] = "for a rest profile may hold path, recordsPath, params, changedSinceParam and paging only: " + err.Error()
		return
	}
	if !strings.HasPrefix(s.Path, "/") || strings.ContainsAny(s.Path, "?#\\") || strings.Contains(s.Path, "..") || strings.Contains(s.Path, "//") || len(s.Path) > 255 {
		bad["source.path"] = "must be an absolute path without query, fragment or .. (for example /api/customers)"
	}
	if s.RecordsPath != "" && !fieldPathRe.MatchString(s.RecordsPath) {
		bad["source.recordsPath"] = "must be a dotted path such as data.items"
	}
	if len(s.Params) > 20 {
		bad["source.params"] = "at most 20 parameters"
	}
	for k, v := range s.Params {
		if !paramNameRe.MatchString(k) || !paramValueRe.MatchString(v) || secretKey.MatchString(k) {
			bad["source.params"] = "names and values must be plain text, and secrets belong in the connection profile"
		}
	}
	if s.ChangedSinceParam != "" && !paramNameRe.MatchString(s.ChangedSinceParam) {
		bad["source.changedSinceParam"] = "must be a query parameter name"
	}
	p := s.Paging
	if p.Style != "" && !slices.Contains(PagingStyles, p.Style) {
		bad["source.paging.style"] = "must be one of " + strings.Join(PagingStyles, ", ")
	}
	if p.PageSize < 0 || p.PageSize > MaxBatch {
		bad["source.paging.pageSize"] = "must be 1 to 1000"
	}
	for _, v := range []string{p.LimitParam, p.OffsetParam, p.PageParam, p.CursorParam} {
		if v != "" && !paramNameRe.MatchString(v) {
			bad["source.paging"] = "parameter names must be plain names"
		}
	}
	if p.CursorPath != "" && !fieldPathRe.MatchString(p.CursorPath) {
		bad["source.paging.cursorPath"] = "must be a dotted path"
	}
	if p.Style == "cursor" && p.CursorPath == "" {
		bad["source.paging.cursorPath"] = "is required for cursor paging (where the next cursor is in the response)"
	}
	if p.FirstPage != nil && (*p.FirstPage < 0 || *p.FirstPage > 1) {
		bad["source.paging.firstPage"] = "must be 0 or 1"
	}
}

func validateFileSource(b EntityBinding, bad map[string]string) {
	s, err := ParseFileSource(b.Source)
	if err != nil {
		bad["source"] = "for a file profile may hold file, filePattern and recordsPath only: " + err.Error()
		return
	}
	switch {
	case (s.File == "") == (s.FilePattern == ""):
		bad["source"] = "give either file or filePattern"
	case s.File != "" && (!globRe.MatchString(s.File) || strings.ContainsAny(s.File, "*?")):
		bad["source.file"] = "must be a plain file name (letters, digits, . _ -)"
	case s.FilePattern != "" && !globRe.MatchString(s.FilePattern):
		bad["source.filePattern"] = "must be a file name pattern (letters, digits, . _ - * ?)"
	}
	if s.RecordsPath != "" && !fieldPathRe.MatchString(s.RecordsPath) {
		bad["source.recordsPath"] = "must be a dotted path"
	}
}

// SourceKey joins the key fields' text of a row with "|". A missing or empty key field is an error: a row without
// its key cannot be tied to the cross-reference.
func SourceKey(row map[string]any, keyFields []string) (string, error) {
	parts := make([]string, len(keyFields))
	for i, f := range keyFields {
		text, present, scalar := sourceText(lookup(row, f))
		if !scalar || !present {
			return "", fmt.Errorf("key field %s is missing or not a scalar", f)
		}
		parts[i] = text
	}
	return strings.Join(parts, "|"), nil
}

// DeltaExtractor extracts what changed after a watermark. since "" extracts everything. The returned watermark is the
// largest cursor value read (since when nothing was read) and goes back in as the next since.
type DeltaExtractor interface {
	Extractor
	ExtractSince(ctx context.Context, entity, since string) ([]Record, string, error)
}

// NormalizeCursor renders a cursor value in the form watermarks use: timestamps in fixed-width UTC RFC 3339, integers
// in decimal, row versions as lowercase hex.
func NormalizeCursor(typ string, v any) (string, bool) {
	switch typ {
	case CursorTimestamp:
		switch x := v.(type) {
		case time.Time:
			return x.UTC().Format(cursorLayout), true
		case string:
			x = strings.TrimSpace(x)
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", dateLayout} {
				if t, err := time.Parse(layout, x); err == nil {
					return t.UTC().Format(cursorLayout), true
				}
			}
		}
	case CursorInt:
		switch x := v.(type) {
		case int:
			return strconv.Itoa(x), true
		case int64:
			return strconv.FormatInt(x, 10), true
		case float64:
			if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
				return strconv.FormatInt(int64(x), 10), true
			}
		case json.Number:
			if n, err := x.Int64(); err == nil {
				return strconv.FormatInt(n, 10), true
			}
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
				return strconv.FormatInt(n, 10), true
			}
		}
	case CursorRowversion:
		switch x := v.(type) {
		case []byte:
			return hex.EncodeToString(x), true
		case string:
			x = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(x), "0x"))
			if _, err := hex.DecodeString(x); err == nil && x != "" {
				return x, true
			}
		}
	}
	return "", false
}

// CompareCursor orders two normalized cursor values of one type.
func CompareCursor(typ, a, b string) int {
	if typ == CursorInt {
		x, _ := strconv.ParseInt(a, 10, 64)
		y, _ := strconv.ParseInt(b, 10, 64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	return strings.Compare(a, b)
}

// MaxCursor returns the larger of two normalized cursors; "" is smaller than any value.
func MaxCursor(typ, a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case CompareCursor(typ, a, b) >= 0:
		return a
	}
	return b
}

// SameOrigin reports whether next is an absolute URL on the same scheme, host and port as base.
func SameOrigin(base, next string) bool {
	b, err1 := url.Parse(base)
	n, err2 := url.Parse(next)
	return err1 == nil && err2 == nil && n.User == nil && b.Scheme == n.Scheme && strings.EqualFold(b.Host, n.Host)
}

// FormatTimestamp renders a time the way timestamp watermarks and timestamp source values are rendered.
func FormatTimestamp(t time.Time) string { return t.UTC().Format(cursorLayout) }
