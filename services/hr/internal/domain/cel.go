package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"
)

// ErrRuleSyntax reports an eligibility rule that does not parse or does not
// type-check against the facts it is given.
var ErrRuleSyntax = errors.New("rule syntax")

// ErrUnknownIdentifier reports a rule that names a fact the caller did not supply
// (for example age, which needs the date of birth held in the vault).
var ErrUnknownIdentifier = errors.New("unknown identifier")

// Env binds rule identifiers to values. Values are decimal.Decimal, string,
// bool or []any. Numbers are never float (PRD 8.42.1: no float on money paths).
type Env map[string]any

// Rule is a compiled eligibility expression: the CEL subset of BR-42.6, with
// && || ! == != < <= > >= in, list literals, numbers, quoted strings and
// booleans (negative numbers as literals only; no arithmetic). Identifiers may
// contain dots (job.type). Unlike full CEL, ! applies to the whole comparison
// after it (!a in [..] is !(a in [..])), and && / || evaluate left to right and
// stop early, so a missing fact on the unused side is not an error.
type Rule struct {
	src  string
	root node
}

// CompileRule parses an eligibility expression. An empty rule is rejected: a
// plan without a rule would make every worker eligible by accident.
func CompileRule(src string) (Rule, error) {
	if len(src) > MaxRuleLength {
		return Rule{}, fmt.Errorf("%w: longer than %d characters", ErrRuleSyntax, MaxRuleLength)
	}
	toks, err := lexRule(src)
	if err != nil {
		return Rule{}, err
	}
	if len(toks) == 0 {
		return Rule{}, fmt.Errorf("%w: empty expression", ErrRuleSyntax)
	}
	p := &ruleParser{toks: toks}
	root, err := p.parseOr()
	if err != nil {
		return Rule{}, err
	}
	if p.i != len(toks) {
		return Rule{}, fmt.Errorf("%w: unexpected %q at %d", ErrRuleSyntax, toks[p.i].text, toks[p.i].pos)
	}
	return Rule{src: src, root: root}, nil
}

// Source returns the expression as written.
func (r Rule) Source() string { return r.src }

// Eval evaluates the rule. The result must be a boolean. An unknown identifier
// is an error, not false, so a missing fact cannot silently exclude or include
// a worker.
func (r Rule) Eval(env Env) (bool, error) {
	if r.root == nil {
		return false, fmt.Errorf("%w: rule not compiled", ErrRuleSyntax)
	}
	v, err := r.root.eval(env)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%w: rule must evaluate to a boolean", ErrRuleSyntax)
	}
	return b, nil
}

type ruleTok struct {
	kind string // ident, num, str, op, kw, EOF
	text string
	pos  int
}

var ruleOps = []string{"&&", "||", "==", "!=", "<=", ">=", "<", ">", "!", "(", ")", "[", "]", ",", "-"}

func lexRule(src string) ([]ruleTok, error) {
	var out []ruleTok
	rs := []rune(src)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '\'' || c == '"':
			end := i + 1
			for end < len(rs) && rs[end] != c {
				end++
			}
			if end >= len(rs) {
				return nil, fmt.Errorf("%w: unterminated string at %d", ErrRuleSyntax, i)
			}
			out = append(out, ruleTok{kind: "str", text: string(rs[i+1 : end]), pos: i})
			i = end + 1
		case unicode.IsDigit(c):
			end := i
			for end < len(rs) && (unicode.IsDigit(rs[end]) || rs[end] == '.') {
				end++
			}
			out = append(out, ruleTok{kind: "num", text: string(rs[i:end]), pos: i})
			i = end
		case unicode.IsLetter(c) || c == '_':
			end := i
			for end < len(rs) && (unicode.IsLetter(rs[end]) || unicode.IsDigit(rs[end]) || rs[end] == '_' || rs[end] == '.') {
				end++
			}
			word := string(rs[i:end])
			switch word {
			case "in":
				out = append(out, ruleTok{kind: "op", text: "in", pos: i})
			case "true", "false":
				out = append(out, ruleTok{kind: "kw", text: word, pos: i})
			default:
				out = append(out, ruleTok{kind: "ident", text: word, pos: i})
			}
			i = end
		default:
			matched := false
			for _, op := range ruleOps {
				if strings.HasPrefix(string(rs[i:min(i+2, len(rs))]), op) {
					out = append(out, ruleTok{kind: "op", text: op, pos: i})
					i += len(op)
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("%w: unexpected character %q at %d", ErrRuleSyntax, c, i)
			}
		}
	}
	return out, nil
}

type node interface {
	eval(env Env) (any, error)
}

type litNode struct{ v any }

func (n litNode) eval(Env) (any, error) { return n.v, nil }

type identNode struct{ name string }

func (n identNode) eval(env Env) (any, error) {
	v, ok := env[n.name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownIdentifier, n.name)
	}
	return v, nil
}

type notNode struct{ x node }

func (n notNode) eval(env Env) (any, error) {
	v, err := n.x.eval(env)
	if err != nil {
		return nil, err
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("%w: ! needs a boolean", ErrRuleSyntax)
	}
	return !b, nil
}

type listNode struct{ items []node }

func (n listNode) eval(env Env) (any, error) {
	out := make([]any, 0, len(n.items))
	for _, it := range n.items {
		v, err := it.eval(env)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

type binNode struct {
	op   string
	l, r node
}

func (n binNode) eval(env Env) (any, error) {
	lv, err := n.l.eval(env)
	if err != nil {
		return nil, err
	}
	// && and || short-circuit, so an unknown fact on the unused side is not read.
	if n.op == "&&" || n.op == "||" {
		lb, ok := lv.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: %s needs booleans", ErrRuleSyntax, n.op)
		}
		if (n.op == "&&" && !lb) || (n.op == "||" && lb) {
			return lb, nil
		}
		rv, err := n.r.eval(env)
		if err != nil {
			return nil, err
		}
		rb, ok := rv.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: %s needs booleans", ErrRuleSyntax, n.op)
		}
		return rb, nil
	}
	rv, err := n.r.eval(env)
	if err != nil {
		return nil, err
	}
	if n.op == "in" {
		list, ok := rv.([]any)
		if !ok {
			return nil, fmt.Errorf("%w: in needs a list on the right", ErrRuleSyntax)
		}
		for _, item := range list {
			eq, err := valuesEqual(lv, item)
			if err != nil {
				return nil, err
			}
			if eq {
				return true, nil
			}
		}
		return false, nil
	}
	if n.op == "==" || n.op == "!=" {
		eq, err := valuesEqual(lv, rv)
		if err != nil {
			return nil, err
		}
		return eq == (n.op == "=="), nil
	}
	c, err := valuesOrder(lv, rv)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case "<":
		return c < 0, nil
	case "<=":
		return c <= 0, nil
	case ">":
		return c > 0, nil
	case ">=":
		return c >= 0, nil
	}
	return nil, fmt.Errorf("%w: operator %s", ErrRuleSyntax, n.op)
}

func valuesEqual(a, b any) (bool, error) {
	switch x := a.(type) {
	case decimal.Decimal:
		if y, ok := b.(decimal.Decimal); ok {
			return x.Equal(y), nil
		}
	case string:
		if y, ok := b.(string); ok {
			return x == y, nil
		}
	case bool:
		if y, ok := b.(bool); ok {
			return x == y, nil
		}
	}
	return false, fmt.Errorf("%w: cannot compare %T with %T", ErrRuleSyntax, a, b)
}

func valuesOrder(a, b any) (int, error) {
	switch x := a.(type) {
	case decimal.Decimal:
		if y, ok := b.(decimal.Decimal); ok {
			return x.Cmp(y), nil
		}
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), nil
		}
	}
	return 0, fmt.Errorf("%w: cannot order %T with %T", ErrRuleSyntax, a, b)
}

// maxRuleDepth bounds nesting (parentheses, lists, negation) so a hostile rule
// cannot exhaust the stack.
const maxRuleDepth = 32

type ruleParser struct {
	toks  []ruleTok
	i     int
	depth int
}

func (p *ruleParser) peek() ruleTok {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ruleTok{kind: "EOF", pos: -1}
}

func (p *ruleParser) is(kind, text string) bool {
	t := p.peek()
	return t.kind == kind && t.text == text
}

func (p *ruleParser) expect(text string) error {
	if !p.is("op", text) {
		t := p.peek()
		return fmt.Errorf("%w: expected %q at %d", ErrRuleSyntax, text, t.pos)
	}
	p.i++
	return nil
}

func (p *ruleParser) parseOr() (node, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxRuleDepth {
		return nil, fmt.Errorf("%w: nested more than %d levels", ErrRuleSyntax, maxRuleDepth)
	}
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.is("op", "||") {
		p.i++
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = binNode{op: "||", l: l, r: r}
	}
	return l, nil
}

func (p *ruleParser) parseAnd() (node, error) {
	l, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.is("op", "&&") {
		p.i++
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l = binNode{op: "&&", l: l, r: r}
	}
	return l, nil
}

func (p *ruleParser) parseNot() (node, error) {
	if p.is("op", "!") {
		p.i++
		p.depth++
		defer func() { p.depth-- }()
		if p.depth > maxRuleDepth {
			return nil, fmt.Errorf("%w: nested more than %d levels", ErrRuleSyntax, maxRuleDepth)
		}
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return notNode{x: x}, nil
	}
	return p.parseCmp()
}

func (p *ruleParser) parseCmp() (node, error) {
	l, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind != "op" {
		return l, nil
	}
	switch t.text {
	case "==", "!=", "<", "<=", ">", ">=", "in":
		p.i++
		r, err := p.parsePrimary()
		if err != nil {
			return nil, err
		}
		return binNode{op: t.text, l: l, r: r}, nil
	}
	return l, nil
}

func (p *ruleParser) parsePrimary() (node, error) {
	t := p.peek()
	switch t.kind {
	case "num":
		p.i++
		d, err := decimal.NewFromString(t.text)
		if err != nil {
			return nil, fmt.Errorf("%w: bad number %q at %d", ErrRuleSyntax, t.text, t.pos)
		}
		return litNode{v: d}, nil
	case "str":
		p.i++
		return litNode{v: t.text}, nil
	case "kw":
		p.i++
		return litNode{v: t.text == "true"}, nil
	case "ident":
		p.i++
		return identNode{name: t.text}, nil
	case "op":
		switch t.text {
		case "-":
			// A minus sign is only a negative number literal; there is no arithmetic.
			if p.i+1 < len(p.toks) && p.toks[p.i+1].kind == "num" {
				p.i++
				num, err := p.parsePrimary()
				if err != nil {
					return nil, err
				}
				return litNode{v: num.(litNode).v.(decimal.Decimal).Neg()}, nil
			}
		case "(":
			p.i++
			x, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			return x, nil
		case "[":
			p.i++
			var items []node
			for !p.is("op", "]") {
				if len(items) > 0 {
					if err := p.expect(","); err != nil {
						return nil, err
					}
				}
				x, err := p.parseOr()
				if err != nil {
					return nil, err
				}
				items = append(items, x)
			}
			p.i++
			return listNode{items: items}, nil
		}
	}
	return nil, fmt.Errorf("%w: unexpected %q at %d", ErrRuleSyntax, t.text, t.pos)
}
