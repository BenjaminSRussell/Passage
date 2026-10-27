package domain

import (
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// Value is the result of an expression: a decimal, or blank. Blank marks a
// cell with nothing to show (division by zero, an absent prior-year period,
// a header row). Blank propagates through operators (FR-15.2).
type Value struct {
	V     decimal.Decimal
	Blank bool
}

// Blank is the blank value.
var Blank = Value{Blank: true}

var identPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// ValidCode reports whether s may name a row or column in an expression.
func ValidCode(s string) bool { return identPattern.MatchString(s) }

// Expr is a parsed calculation expression over row or column codes.
type Expr struct {
	root *node
}

type node struct {
	kind byte // 'n' number, 'r' reference, 'u' unary minus, or one of + - * /
	num  decimal.Decimal
	ref  string
	l, r *node
}

type token struct {
	kind byte // 'n' number, 'i' identifier, 'o' operator or parenthesis, 'e' end
	text string
}

// ParseExpr parses an expression such as "(R3-R4)/R5*100". The grammar is
// expr = term {('+'|'-') term}; term = unary {('*'|'/') unary};
// unary = '-' unary | primary; primary = number | identifier | '(' expr ')'.
func ParseExpr(src string) (*Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, src: src}
	root, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != 'e' {
		return nil, p.fail("unexpected " + p.peek().text)
	}
	return &Expr{root: root}, nil
}

// RawPrefix marks a row calculation that works on stored net amounts (debit
// minus credit) instead of displayed, sign-presented values (BR-15.3).
const RawPrefix = "raw:"

// ParseRowExpr parses a calculation row expression, which may start with the
// raw: prefix. raw reports whether the prefix was present.
func ParseRowExpr(src string) (raw bool, expr *Expr, err error) {
	trimmed := strings.TrimSpace(src)
	if strings.HasPrefix(trimmed, RawPrefix) {
		raw, trimmed = true, strings.TrimPrefix(trimmed, RawPrefix)
	}
	expr, err = ParseExpr(trimmed)
	return raw, expr, err
}

// Refs lists the distinct references in first-use order.
func (e *Expr) Refs() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.kind == 'r' && !seen[n.ref] {
			seen[n.ref] = true
			out = append(out, n.ref)
		}
		walk(n.l)
		walk(n.r)
	}
	walk(e.root)
	return out
}

// Eval computes the expression. lookup supplies the value of each reference.
func (e *Expr) Eval(lookup func(ref string) (Value, error)) (Value, error) {
	return e.root.eval(lookup)
}

func (n *node) eval(lookup func(string) (Value, error)) (Value, error) {
	switch n.kind {
	case 'n':
		return Value{V: n.num}, nil
	case 'r':
		return lookup(n.ref)
	case 'u':
		v, err := n.l.eval(lookup)
		if err != nil || v.Blank {
			return v, err
		}
		return Value{V: v.V.Neg()}, nil
	}
	l, err := n.l.eval(lookup)
	if err != nil {
		return Value{}, err
	}
	r, err := n.r.eval(lookup)
	if err != nil {
		return Value{}, err
	}
	if l.Blank || r.Blank {
		return Blank, nil
	}
	switch n.kind {
	case '+':
		return Value{V: l.V.Add(r.V)}, nil
	case '-':
		return Value{V: l.V.Sub(r.V)}, nil
	case '*':
		return Value{V: l.V.Mul(r.V)}, nil
	default: // '/'
		if r.V.IsZero() {
			return Blank, nil
		}
		return Value{V: l.V.Div(r.V)}, nil
	}
}

func lex(src string) ([]token, error) {
	var toks []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c >= '0' && c <= '9' || c == '.':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			toks = append(toks, token{'n', src[i:j]})
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(src) && (src[j] == '_' || src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] >= '0' && src[j] <= '9') {
				j++
			}
			toks = append(toks, token{'i', src[i:j]})
			i = j
		case strings.ContainsRune("+-*/()", rune(c)):
			toks = append(toks, token{'o', string(c)})
			i++
		default:
			return nil, errf(CodeExpression, "unexpected character %q in %q", c, src)
		}
	}
	return append(toks, token{kind: 'e'}), nil
}

type parser struct {
	toks []token
	pos  int
	src  string
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != 'e' {
		p.pos++
	}
	return t
}

func (p *parser) fail(detail string) error {
	return errf(CodeExpression, "%s in %q", detail, p.src)
}

func (p *parser) expr() (*node, error) {
	left, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == 'o' && (p.peek().text == "+" || p.peek().text == "-") {
		op := p.next().text[0]
		right, err := p.term()
		if err != nil {
			return nil, err
		}
		left = &node{kind: op, l: left, r: right}
	}
	return left, nil
}

func (p *parser) term() (*node, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == 'o' && (p.peek().text == "*" || p.peek().text == "/") {
		op := p.next().text[0]
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		left = &node{kind: op, l: left, r: right}
	}
	return left, nil
}

func (p *parser) unary() (*node, error) {
	if p.peek().kind == 'o' && p.peek().text == "-" {
		p.next()
		inner, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &node{kind: 'u', l: inner}, nil
	}
	return p.primary()
}

func (p *parser) primary() (*node, error) {
	t := p.next()
	switch {
	case t.kind == 'n':
		d, err := decimal.NewFromString(t.text)
		if err != nil {
			return nil, p.fail("malformed number " + t.text)
		}
		return &node{kind: 'n', num: d}, nil
	case t.kind == 'i':
		return &node{kind: 'r', ref: t.text}, nil
	case t.kind == 'o' && t.text == "(":
		inner, err := p.expr()
		if err != nil {
			return nil, err
		}
		if closing := p.next(); closing.kind != 'o' || closing.text != ")" {
			return nil, p.fail("missing )")
		}
		return inner, nil
	case t.kind == 'e':
		return nil, p.fail("expression ends early")
	default:
		return nil, p.fail("unexpected " + t.text)
	}
}
