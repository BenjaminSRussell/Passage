package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Masks decides which columns of a cloned tenant are replaced. The configuration is JSON:
//
//	{"salt": "per-clone secret", "columns": {"party_vendor.name": "hash", "*.email": "email", "*.phone": "digits", "party_contact.note": "null"}}
//
// A key is table.column, or *.column for that column name in every table. Rules:
//
//	hash    text replaced by a salted sha256 prefix of the same length (at most 64): equal inputs stay equal, nothing reads back
//	email   salted hash at example.invalid, so it is a valid-looking address that cannot receive mail
//	digits  every digit replaced by a salted digit, other characters kept (phone and account numbers keep their shape)
//	redact  the fixed text REDACTED
//	null    NULL, for any column type (the column must be nullable)
//	keep    the value is copied as it is: the operator's statement that the column holds nothing personal or commercial
//
// The configuration is an allowlist. Every text, json, jsonb, array or xml column of the cloned tenant's tables that is not a key
// column needs a rule (keep included), and a clone with any column left without one is refused before a row is read, so a
// column added after the configuration was written is never copied unmasked by accident. A key column (primary key, tenant_id)
// can only be kept: a hash, redaction or null on a key would break the rows that reference it, and a rule that matches a key
// column, wildcard or explicit, is an error rather than silently skipped. Name the table's key column explicitly with keep.
//
// The text rules (hash, email, digits, redact) mask text values only; one that meets another type stops the clone instead of
// copying the value.
type Masks struct {
	Salt    string            `json:"salt"`
	Columns map[string]string `json:"columns"`
}

var maskRules = map[string]bool{"hash": true, "email": true, "digits": true, "redact": true, "null": true, "keep": true}

// ParseMasks reads and validates a mask configuration. An empty salt is refused: without it the hashes can be guessed
// from a dictionary of names.
func ParseMasks(raw []byte) (*Masks, error) {
	var m Masks
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("mask configuration: %w", err)
	}
	if len(m.Salt) < 8 {
		return nil, errors.New("mask configuration: salt must be at least 8 characters")
	}
	if len(m.Columns) == 0 {
		return nil, errors.New("mask configuration: columns is empty; a masked clone with no masked column is a plain copy")
	}
	for k, rule := range m.Columns {
		if strings.Count(k, ".") != 1 || strings.HasPrefix(k, ".") || strings.HasSuffix(k, ".") {
			return nil, fmt.Errorf("mask configuration: %q must be table.column or *.column", k)
		}
		if !maskRules[rule] {
			return nil, fmt.Errorf("mask configuration: %q has unknown rule %q (hash, email, digits, redact, null, keep)", k, rule)
		}
	}
	return &m, nil
}

func (m *Masks) rule(table, column string) (string, bool) {
	if r, ok := m.Columns[table+"."+column]; ok {
		return r, true
	}
	r, ok := m.Columns["*."+column]
	return r, ok
}

// textLike reports whether a column of this type can carry free text or structured personal data and so needs a rule.
func textLike(typ string) bool {
	t := strings.ToLower(typ)
	if strings.HasSuffix(t, "]") {
		return true // arrays of anything
	}
	for _, p := range []string{"text", "character", "citext", "json", "xml", "name", "varchar", "bpchar"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

func isKey(t table, col string) bool {
	if col == "tenant_id" {
		return true
	}
	for _, k := range t.Key {
		if k == col {
			return true
		}
	}
	return false
}

// Check refuses a configuration that leaves a column unclassified or puts a rule on a key column. It runs over every table of
// the clone before any row is read.
func (m *Masks) Check(tables []table) error {
	var missing []string
	for _, t := range tables {
		for _, c := range t.Columns {
			rule, ok := m.rule(t.Name, c.Name)
			switch {
			case isKey(t, c.Name):
				if ok && rule != "keep" {
					return fmt.Errorf("%s.%s is a key column and cannot be masked (%s): the rows that reference it would break; use keep", t.Name, c.Name, rule)
				}
			case !ok && textLike(c.Type):
				missing = append(missing, t.Name+"."+c.Name+" ("+c.Type+")")
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the mask configuration has no rule for %d text, json or array column(s); give each a rule (or keep, if it holds nothing personal): %s", len(missing), strings.Join(missing, ", "))
	}
	return nil
}

// Apply masks the rows read for t (columns in t.Columns order) and returns them.
func (m *Masks) Apply(t table, rows [][]any) ([][]any, error) {
	type target struct {
		at   int
		rule string
	}
	var targets []target
	for i, c := range t.Columns {
		r, ok := m.rule(t.Name, c.Name)
		if !ok || r == "keep" {
			continue
		}
		if isKey(t, c.Name) {
			return nil, fmt.Errorf("%s.%s is a key column and cannot be masked (%s): the rows that reference it would break; use keep", t.Name, c.Name, r)
		}
		targets = append(targets, target{i, r})
	}
	for _, row := range rows {
		for _, tg := range targets {
			v := row[tg.at]
			if v == nil {
				continue
			}
			if tg.rule == "null" {
				row[tg.at] = nil
				continue
			}
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s.%s: the %s rule masks text only, found %T; refusing to copy the value (use null or keep)", t.Name, t.Columns[tg.at].Name, tg.rule, v)
			}
			row[tg.at] = m.mask(tg.rule, t.Name+"."+t.Columns[tg.at].Name, s)
		}
	}
	return rows, nil
}

func (m *Masks) digest(scope, value string) []byte {
	h := sha256.Sum256([]byte(m.Salt + "\x00" + scope + "\x00" + value))
	return h[:]
}

func (m *Masks) mask(rule, scope, v string) any {
	switch rule {
	case "keep":
		return v
	case "null":
		return nil
	case "redact":
		return "REDACTED"
	case "hash":
		hexed := hex.EncodeToString(m.digest(scope, v))
		n := len(v)
		if n > len(hexed) {
			n = len(hexed)
		}
		if n == 0 {
			return v
		}
		return hexed[:n]
	case "email":
		return hex.EncodeToString(m.digest("email", v))[:16] + "@example.invalid"
	case "digits":
		d := m.digest(scope, v)
		out := []byte(v)
		j := 0
		for i, ch := range out {
			if ch >= '0' && ch <= '9' {
				out[i] = '0' + d[j%len(d)]%10
				j++
			}
		}
		return string(out)
	}
	return v
}
