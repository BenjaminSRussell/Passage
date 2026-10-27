package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"
)

// maxFormulaLength and maxFormulaDepth bound a costing-model formula so a
// hostile or mistyped model cannot exhaust the parser (FR-39.13).
const (
	maxFormulaLength = 500
	maxFormulaDepth  = 32
)

// formulaNode is one node of a parsed expression: a number or a characteristic
// reference (leaf), a negation (op 'n') or a binary operator (+ - * /).
type formulaNode struct {
	op   byte
	val  decimal.Decimal
	name string
	l, r *formulaNode
}

// Formula is a parsed arithmetic expression over numbers, characteristic names
// and the operators + - * / with parentheses and unary minus. It is a local
// grammar for costing-model lines; wave 38 owns the shared formula grammar.
type Formula struct {
	src  string
	root *formulaNode
}

// ParseFormula parses src. An empty or malformed formula is an error.
func ParseFormula(src string) (Formula, error) {
	if strings.TrimSpace(src) == "" {
		return Formula{}, errors.New("formula is empty")
	}
	if len(src) > maxFormulaLength {
		return Formula{}, fmt.Errorf("formula exceeds %d characters", maxFormulaLength)
	}
	p := &formulaParser{src: src}
	root, err := p.expr(0)
	if err != nil {
		return Formula{}, err
	}
	p.skip()
	if p.pos != len(p.src) {
		return Formula{}, fmt.Errorf("unexpected %q at position %d", p.src[p.pos], p.pos)
	}
	return Formula{src: src, root: root}, nil
}

// Idents returns the distinct characteristic names the formula reads, sorted.
func (f Formula) Idents() []string {
	seen := map[string]bool{}
	var walk func(n *formulaNode)
	walk = func(n *formulaNode) {
		if n == nil {
			return
		}
		if n.op == 0 && n.name != "" {
			seen[n.name] = true
		}
		walk(n.l)
		walk(n.r)
	}
	walk(f.root)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Eval computes the formula with the given characteristic values. An unknown
// name or a division by zero is an error.
func (f Formula) Eval(vars map[string]decimal.Decimal) (decimal.Decimal, error) {
	if f.root == nil {
		return decimal.Zero, errors.New("formula is not parsed")
	}
	return evalNode(f.root, vars)
}

func evalNode(n *formulaNode, vars map[string]decimal.Decimal) (decimal.Decimal, error) {
	switch n.op {
	case 0:
		if n.name == "" {
			return n.val, nil
		}
		v, ok := vars[n.name]
		if !ok {
			return decimal.Zero, fmt.Errorf("characteristic %q has no value", n.name)
		}
		return v, nil
	case 'n':
		v, err := evalNode(n.l, vars)
		return v.Neg(), err
	}
	l, err := evalNode(n.l, vars)
	if err != nil {
		return decimal.Zero, err
	}
	r, err := evalNode(n.r, vars)
	if err != nil {
		return decimal.Zero, err
	}
	switch n.op {
	case '+':
		return l.Add(r), nil
	case '-':
		return l.Sub(r), nil
	case '*':
		return l.Mul(r), nil
	default:
		if r.IsZero() {
			return decimal.Zero, errors.New("division by zero in formula")
		}
		return l.Div(r), nil
	}
}

// formulaParser is a recursive-descent parser over the source string.
type formulaParser struct {
	src string
	pos int
}

func (p *formulaParser) skip() {
	for p.pos < len(p.src) && unicode.IsSpace(rune(p.src[p.pos])) {
		p.pos++
	}
}

func (p *formulaParser) peek() byte {
	p.skip()
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

// expr := term { ('+' | '-') term }
func (p *formulaParser) expr(depth int) (*formulaNode, error) {
	if depth > maxFormulaDepth {
		return nil, fmt.Errorf("formula nests deeper than %d", maxFormulaDepth)
	}
	left, err := p.term(depth)
	if err != nil {
		return nil, err
	}
	for c := p.peek(); c == '+' || c == '-'; c = p.peek() {
		p.pos++
		right, err := p.term(depth)
		if err != nil {
			return nil, err
		}
		left = &formulaNode{op: c, l: left, r: right}
	}
	return left, nil
}

// term := unary { ('*' | '/') unary }
func (p *formulaParser) term(depth int) (*formulaNode, error) {
	left, err := p.unary(depth)
	if err != nil {
		return nil, err
	}
	for c := p.peek(); c == '*' || c == '/'; c = p.peek() {
		p.pos++
		right, err := p.unary(depth)
		if err != nil {
			return nil, err
		}
		left = &formulaNode{op: c, l: left, r: right}
	}
	return left, nil
}

// unary := '-' unary | primary
func (p *formulaParser) unary(depth int) (*formulaNode, error) {
	if p.peek() == '-' {
		p.pos++
		inner, err := p.unary(depth + 1)
		if err != nil {
			return nil, err
		}
		return &formulaNode{op: 'n', l: inner}, nil
	}
	return p.primary(depth)
}

// primary := number | name | '(' expr ')'
func (p *formulaParser) primary(depth int) (*formulaNode, error) {
	c := p.peek()
	switch {
	case c == '(':
		p.pos++
		inner, err := p.expr(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, fmt.Errorf("missing ')' at position %d", p.pos)
		}
		p.pos++
		return inner, nil
	case c >= '0' && c <= '9' || c == '.':
		start := p.pos
		for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
			p.pos++
		}
		v, err := decimal.NewFromString(p.src[start:p.pos])
		if err != nil {
			return nil, fmt.Errorf("invalid number at position %d", start)
		}
		return &formulaNode{val: v}, nil
	case isNameStart(c):
		start := p.pos
		for p.pos < len(p.src) && isNamePart(p.src[p.pos]) {
			p.pos++
		}
		return &formulaNode{name: p.src[start:p.pos]}, nil
	case c == 0:
		return nil, errors.New("unexpected end of formula")
	default:
		return nil, fmt.Errorf("unexpected %q at position %d", c, p.pos)
	}
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNamePart(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}
