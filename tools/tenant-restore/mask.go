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
//	null    NULL (the column must be nullable)
//
// Only text values are masked. A rule that meets a non-text value stops the clone instead of copying the value, and so does
// a rule on a key column: masking a key would break the references to it.
type Masks struct {
	Salt    string            `json:"salt"`
	Columns map[string]string `json:"columns"`
}

var maskRules = map[string]bool{"hash": true, "email": true, "digits": true, "redact": true, "null": true}

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
			return nil, fmt.Errorf("mask configuration: %q has unknown rule %q (hash, email, digits, redact, null)", k, rule)
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

// Apply masks the rows read for t (columns in t.Columns order) and returns them.
func (m *Masks) Apply(t table, rows [][]any) ([][]any, error) {
	keys := map[string]bool{}
	for _, k := range t.Key {
		keys[k] = true
	}
	type target struct {
		at   int
		rule string
	}
	var targets []target
	for i, c := range t.Columns {
		r, ok := m.rule(t.Name, c.Name)
		if !ok {
			continue
		}
		if keys[c.Name] || c.Name == "tenant_id" {
			if _, explicit := m.Columns[t.Name+"."+c.Name]; explicit {
				return nil, fmt.Errorf("%s.%s is a key column and cannot be masked: the rows that reference it would break", t.Name, c.Name)
			}
			continue // a *.column rule never touches a key
		}
		targets = append(targets, target{i, r})
	}
	for _, row := range rows {
		for _, tg := range targets {
			v := row[tg.at]
			if v == nil {
				continue
			}
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s.%s: the %s rule masks text only, found %T; refusing to copy the value", t.Name, t.Columns[tg.at].Name, tg.rule, v)
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
