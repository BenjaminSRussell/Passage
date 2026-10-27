package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// PatchOp is one RFC 6902 operation. Only add, remove and replace are produced
// and accepted; value is omitted for remove and always present otherwise.
type PatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// MarshalJSON writes value for add and replace even when it is null, and omits it for remove.
func (p PatchOp) MarshalJSON() ([]byte, error) {
	if p.Op == "remove" {
		return json.Marshal(struct {
			Op   string `json:"op"`
			Path string `json:"path"`
		}{p.Op, p.Path})
	}
	return json.Marshal(struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}{p.Op, p.Path, p.Value})
}

// Normalize round-trips v through JSON so Go values compare the way they are
// stored: objects are map[string]any, arrays []any and numbers json.Number.
func Normalize(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Diff returns the RFC 6902 patch that turns a into b (FR-17.8: each version is
// stored as a patch against the previous one). Object keys are visited in sorted
// order and array changes are emitted so that applying them in order is valid.
func Diff(a, b any) ([]PatchOp, error) {
	na, err := Normalize(a)
	if err != nil {
		return nil, err
	}
	nb, err := Normalize(b)
	if err != nil {
		return nil, err
	}
	ops := []PatchOp{}
	diffValue("", na, nb, &ops)
	return ops, nil
}

func diffValue(path string, a, b any, ops *[]PatchOp) {
	if reflect.DeepEqual(a, b) {
		return
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*ops = append(*ops, PatchOp{Op: "replace", Path: path, Value: b})
			return
		}
		for _, k := range sortedKeys(av) {
			child := path + "/" + escapeToken(k)
			if _, ok := bv[k]; !ok {
				*ops = append(*ops, PatchOp{Op: "remove", Path: child})
				continue
			}
			diffValue(child, av[k], bv[k], ops)
		}
		for _, k := range sortedKeys(bv) {
			if _, ok := av[k]; !ok {
				*ops = append(*ops, PatchOp{Op: "add", Path: path + "/" + escapeToken(k), Value: bv[k]})
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*ops = append(*ops, PatchOp{Op: "replace", Path: path, Value: b})
			return
		}
		n := min(len(av), len(bv))
		for i := range n {
			diffValue(path+"/"+strconv.Itoa(i), av[i], bv[i], ops)
		}
		for i := len(av) - 1; i >= n; i-- {
			*ops = append(*ops, PatchOp{Op: "remove", Path: path + "/" + strconv.Itoa(i)})
		}
		for i := n; i < len(bv); i++ {
			*ops = append(*ops, PatchOp{Op: "add", Path: path + "/-", Value: bv[i]})
		}
	default:
		*ops = append(*ops, PatchOp{Op: "replace", Path: path, Value: b})
	}
}

// Apply returns doc with ops applied in order. doc is not modified.
func Apply(doc any, ops []PatchOp) (any, error) {
	cur, err := Normalize(doc)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		v, err := Normalize(op.Value)
		if err != nil {
			return nil, err
		}
		tokens, err := parsePointer(op.Path)
		if err != nil {
			return nil, err
		}
		if cur, err = applyAt(cur, tokens, op.Op, v); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

func applyAt(node any, tokens []string, op string, value any) (any, error) {
	if len(tokens) == 0 {
		if op == "remove" {
			return nil, &Error{Code: CodeValidation, Detail: "cannot remove the document root"}
		}
		return value, nil
	}
	tok := tokens[0]
	switch n := node.(type) {
	case map[string]any:
		if len(tokens) == 1 {
			_, exists := n[tok]
			switch {
			case op == "remove":
				if !exists {
					return nil, patchErr("remove target %q does not exist", tok)
				}
				delete(n, tok)
			case op == "add" || (op == "replace" && exists):
				n[tok] = value
			default:
				return nil, patchErr("replace target %q does not exist", tok)
			}
			return n, nil
		}
		child, ok := n[tok]
		if !ok {
			return nil, patchErr("path segment %q does not exist", tok)
		}
		next, err := applyAt(child, tokens[1:], op, value)
		if err != nil {
			return nil, err
		}
		n[tok] = next
		return n, nil
	case []any:
		if len(tokens) == 1 && tok == "-" && op == "add" {
			return append(n, value), nil
		}
		idx, err := strconv.Atoi(tok)
		if err != nil || idx < 0 {
			return nil, patchErr("array index %q is invalid", tok)
		}
		if len(tokens) == 1 {
			switch {
			case op == "add" && idx <= len(n):
				return append(n[:idx], append([]any{value}, n[idx:]...)...), nil
			case op == "replace" && idx < len(n):
				n[idx] = value
				return n, nil
			case op == "remove" && idx < len(n):
				return append(n[:idx], n[idx+1:]...), nil
			}
			return nil, patchErr("array index %d is out of range for %s", idx, op)
		}
		if idx >= len(n) {
			return nil, patchErr("array index %d is out of range", idx)
		}
		next, err := applyAt(n[idx], tokens[1:], op, value)
		if err != nil {
			return nil, err
		}
		n[idx] = next
		return n, nil
	}
	return nil, patchErr("path descends into a scalar at %q", tok)
}

func patchErr(format string, args ...any) error {
	return &Error{Code: CodeValidation, Detail: "json patch: " + fmt.Sprintf(format, args...)}
}

func parsePointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil, patchErr("pointer %q must start with /", p)
	}
	parts := strings.Split(p[1:], "/")
	for i, t := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(t, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func escapeToken(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
