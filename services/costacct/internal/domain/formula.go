package domain

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Formula grammar for template (driver-formula) allocation (FR-38.17):
//
//	expr  := term { ('+' | '-') term }
//	term  := unary { ('*' | '/') unary }
//	unary := '-' unary | primary
//	primary := number | ref | 'min' '(' expr ',' expr ')' | 'max' '(' expr ',' expr ')'
//	         | 'if' '(' cond ',' expr ',' expr ')' | '(' expr ')'
//	cond  := expr ('<' | '<=' | '>' | '>=' | '=' | '!=') expr
//	ref   := 'm.' name   (statistical measure of the receiver in the period)
//	       | 'b.' name   (receiver's journal and manual balance of cost element `name`)
//
// The Unicode operators × ÷ − are accepted as * / -. Nothing else is accepted:
// no other identifiers, no strings, no assignments.

// MaxFormulaLength bounds the source of one formula line.
const MaxFormulaLength = 500

// FormulaError is a syntax error with the byte offset of the offending token.
type FormulaError struct {
	Pos int
	Msg string
}

func (e *FormulaError) Error() string { return fmt.Sprintf("position %d: %s", e.Pos, e.Msg) }

var refName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,39}$`)

type tokKind int

const (
	tokEOF tokKind = iota
	tokNum
	tokIdent
	tokOp
	tokLP
	tokRP
	tokComma
)

type token struct {
	kind tokKind
	text string
	pos  int
}

type nodeKind int

const (
	nodeNum nodeKind = iota
	nodeRef
	nodeNeg
	nodeBin
	nodeMin
	nodeMax
	nodeIf
	nodeCmp
)

type node struct {
	kind nodeKind
	op   string
	num  decimal.Decimal
	ref  string
	args []*node
}

// Formula is a parsed, validated template formula.
type Formula struct {
	src  string
	root *node
	refs []string
}

// Env maps references ("m.x", "b.y") to values for one receiver and period.
// Missing references evaluate to zero.
type Env map[string]decimal.Decimal

// ParseFormula validates src against the grammar and returns the parsed form.
func ParseFormula(src string) (*Formula, error) {
	s := strings.TrimSpace(src)
	if s == "" {
		return nil, &FormulaError{Pos: 0, Msg: "formula is empty"}
	}
	if len(s) > MaxFormulaLength {
		return nil, &FormulaError{Pos: MaxFormulaLength, Msg: "formula exceeds 500 characters"}
	}
	toks, err := lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	root, err := p.expr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEOF {
		return nil, &FormulaError{Pos: t.pos, Msg: "unexpected " + describe(t)}
	}
	seen := map[string]bool{}
	collectRefs(root, seen)
	refs := make([]string, 0, len(seen))
	for r := range seen {
		refs = append(refs, r)
	}
	sort.Strings(refs)
	return &Formula{src: s, root: root, refs: refs}, nil
}

// Source returns the normalised formula text.
func (f *Formula) Source() string { return f.src }

// Refs returns the distinct references the formula reads, sorted.
func (f *Formula) Refs() []string { return append([]string(nil), f.refs...) }

// Eval evaluates the formula. A division by a zero divisor yields 0 for that
// evaluation and reports divByZero (BR-38.12).
func (f *Formula) Eval(env Env) (value decimal.Decimal, divByZero bool) {
	st := &evalState{env: env}
	value = st.num(f.root)
	return value, st.divZero
}

type evalState struct {
	env     Env
	divZero bool
}

func (st *evalState) num(n *node) decimal.Decimal {
	switch n.kind {
	case nodeNum:
		return n.num
	case nodeRef:
		return st.env[n.ref]
	case nodeNeg:
		return st.num(n.args[0]).Neg()
	case nodeBin:
		a, b := st.num(n.args[0]), st.num(n.args[1])
		switch n.op {
		case "+":
			return a.Add(b)
		case "-":
			return a.Sub(b)
		case "*":
			return a.Mul(b)
		default: // "/"
			if b.IsZero() {
				st.divZero = true
				return decimal.Zero
			}
			return a.Div(b)
		}
	case nodeMin:
		return decimal.Min(st.num(n.args[0]), st.num(n.args[1]))
	case nodeMax:
		return decimal.Max(st.num(n.args[0]), st.num(n.args[1]))
	case nodeIf:
		if st.cond(n.args[0]) {
			return st.num(n.args[1])
		}
		return st.num(n.args[2])
	}
	return decimal.Zero
}

func (st *evalState) cond(n *node) bool {
	a, b := st.num(n.args[0]), st.num(n.args[1])
	switch n.op {
	case "<":
		return a.LessThan(b)
	case "<=":
		return a.LessThanOrEqual(b)
	case ">":
		return a.GreaterThan(b)
	case ">=":
		return a.GreaterThanOrEqual(b)
	case "=":
		return a.Equal(b)
	default: // "!="
		return !a.Equal(b)
	}
}

func collectRefs(n *node, seen map[string]bool) {
	if n.kind == nodeRef {
		seen[n.ref] = true
	}
	for _, a := range n.args {
		collectRefs(a, seen)
	}
}

func describe(t token) string {
	switch t.kind {
	case tokEOF:
		return "end of formula"
	case tokNum:
		return "number " + t.text
	case tokIdent:
		return "name " + t.text
	default:
		return "'" + t.text + "'"
	}
}

func lex(src string) ([]token, error) {
	src = strings.NewReplacer("×", "*", "÷", "/", "−", "-").Replace(src)
	var out []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case isDigit(c) || (c == '.' && i+1 < len(src) && isDigit(src[i+1])):
			j := i
			dots := 0
			for j < len(src) && (isDigit(src[j]) || src[j] == '.') {
				if src[j] == '.' {
					dots++
				}
				j++
			}
			if dots > 1 || src[j-1] == '.' {
				return nil, &FormulaError{Pos: i, Msg: "malformed number"}
			}
			out = append(out, token{kind: tokNum, text: src[i:j], pos: i})
			i = j
		case isLetter(c):
			j := i
			for j < len(src) && (isLetter(src[j]) || isDigit(src[j]) || src[j] == '.') {
				j++
			}
			out = append(out, token{kind: tokIdent, text: src[i:j], pos: i})
			i = j
		case c == '(':
			out = append(out, token{kind: tokLP, text: "(", pos: i})
			i++
		case c == ')':
			out = append(out, token{kind: tokRP, text: ")", pos: i})
			i++
		case c == ',':
			out = append(out, token{kind: tokComma, text: ",", pos: i})
			i++
		case c == '+' || c == '-' || c == '*' || c == '/':
			out = append(out, token{kind: tokOp, text: string(c), pos: i})
			i++
		case c == '<' || c == '>' || c == '=' || c == '!':
			op := string(c)
			if i+1 < len(src) && src[i+1] == '=' {
				op += "="
			}
			if c == '!' && op != "!=" {
				return nil, &FormulaError{Pos: i, Msg: "'!' must be followed by '='"}
			}
			out = append(out, token{kind: tokOp, text: op, pos: i})
			i += len(op)
		default:
			return nil, &FormulaError{Pos: i, Msg: fmt.Sprintf("unexpected character %q", c)}
		}
	}
	return append(out, token{kind: tokEOF, pos: len(src)}), nil
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

func (p *parser) isOp(ops ...string) bool {
	t := p.peek()
	if t.kind != tokOp {
		return false
	}
	for _, o := range ops {
		if t.text == o {
			return true
		}
	}
	return false
}

func (p *parser) expect(kind tokKind, what string) error {
	if t := p.peek(); t.kind != kind {
		return &FormulaError{Pos: t.pos, Msg: "expected " + what + ", found " + describe(t)}
	}
	p.next()
	return nil
}

func (p *parser) expr() (*node, error) {
	left, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.isOp("+", "-") {
		op := p.next().text
		right, err := p.term()
		if err != nil {
			return nil, err
		}
		left = &node{kind: nodeBin, op: op, args: []*node{left, right}}
	}
	return left, nil
}

func (p *parser) term() (*node, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.isOp("*", "/") {
		op := p.next().text
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		left = &node{kind: nodeBin, op: op, args: []*node{left, right}}
	}
	return left, nil
}

func (p *parser) unary() (*node, error) {
	if p.isOp("-") {
		p.next()
		arg, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &node{kind: nodeNeg, args: []*node{arg}}, nil
	}
	return p.primary()
}

func (p *parser) primary() (*node, error) {
	t := p.peek()
	switch t.kind {
	case tokNum:
		p.next()
		d, err := decimal.NewFromString(t.text)
		if err != nil {
			return nil, &FormulaError{Pos: t.pos, Msg: "malformed number"}
		}
		return &node{kind: nodeNum, num: d}, nil
	case tokLP:
		p.next()
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(tokRP, "')'"); err != nil {
			return nil, err
		}
		return e, nil
	case tokIdent:
		p.next()
		if p.peek().kind == tokLP {
			return p.call(t)
		}
		return p.ref(t)
	}
	return nil, &FormulaError{Pos: t.pos, Msg: "expected a number, reference or '(', found " + describe(t)}
}

func (p *parser) ref(t token) (*node, error) {
	prefix, name, ok := strings.Cut(t.text, ".")
	if !ok || (prefix != "m" && prefix != "b") || !refName.MatchString(name) {
		return nil, &FormulaError{Pos: t.pos, Msg: "unknown name " + t.text + " (use m.<measure> or b.<element>)"}
	}
	return &node{kind: nodeRef, ref: t.text}, nil
}

func (p *parser) call(t token) (*node, error) {
	p.next() // '('
	switch t.text {
	case "if":
		c, err := p.cond()
		if err != nil {
			return nil, err
		}
		args := []*node{c}
		for k := 0; k < 2; k++ {
			if err := p.expect(tokComma, "','"); err != nil {
				return nil, err
			}
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			args = append(args, e)
		}
		if err := p.expect(tokRP, "')'"); err != nil {
			return nil, err
		}
		return &node{kind: nodeIf, args: args}, nil
	case "min", "max":
		a, err := p.expr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(tokComma, "','"); err != nil {
			return nil, err
		}
		b, err := p.expr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(tokRP, "')'"); err != nil {
			return nil, err
		}
		kind := nodeMin
		if t.text == "max" {
			kind = nodeMax
		}
		return &node{kind: kind, args: []*node{a, b}}, nil
	}
	return nil, &FormulaError{Pos: t.pos, Msg: "unknown function " + t.text + " (use min, max or if)"}
}

func (p *parser) cond() (*node, error) {
	a, err := p.expr()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind != tokOp || !isComparison(t.text) {
		return nil, &FormulaError{Pos: t.pos, Msg: "expected a comparison in if(), found " + describe(t)}
	}
	p.next()
	b, err := p.expr()
	if err != nil {
		return nil, err
	}
	return &node{kind: nodeCmp, op: t.text, args: []*node{a, b}}, nil
}

func isComparison(op string) bool {
	switch op {
	case "<", "<=", ">", ">=", "=", "!=":
		return true
	}
	return false
}

// Message codes of template allocation (PRD 7.38.18).
const (
	CodeFormulaError = "CO-0621" // a formula does not parse or references an unknown measure or element
	CodeMeasureZero  = "CO-0622" // warning: a divisor is zero for a receiver (BR-38.12)
)

// Template errors.
var (
	// ErrNegativeReceiver is returned when a line evaluates below zero and the
	// template does not set allow-negative.
	ErrNegativeReceiver = errors.New("costacct: negative receiver amount")
	// ErrTemplateConservation is returned when the receiver debits and the sender
	// credits do not net to zero. It cannot happen by construction; it guards the invariant.
	ErrTemplateConservation = errors.New("costacct: template totals differ")
)

// TemplateLine is one receiver line of a template with its parsed formula.
type TemplateLine struct {
	ReceiverID string
	ElementID  string
	Formula    *Formula
}

// LineResult is the evaluated amount of one template line.
type LineResult struct {
	ReceiverID string          `json:"receiverCostObjectId"`
	ElementID  string          `json:"secondaryElementId"`
	Amount     decimal.Decimal `json:"amount"`
	DivByZero  bool            `json:"divByZero"`
}

// TemplateResult is the cents-exact outcome of one template evaluation.
type TemplateResult struct {
	Lines    []LineResult    `json:"lines"`
	TotalIn  decimal.Decimal `json:"totalIn"`
	Warnings []string        `json:"warnings"`
}

// EvaluateTemplate evaluates every line for its receiver and rounds each amount
// once to the currency minor unit. The receiver gets +amount on the line's element
// and the sender gets -amount on the same element, so Σ in = Σ out by construction;
// the sum is checked anyway (BR-38.6 style conservation guard).
func EvaluateTemplate(lines []TemplateLine, envs map[string]Env, minor int32, allowNegative bool) (TemplateResult, error) {
	res := TemplateResult{Lines: make([]LineResult, 0, len(lines)), TotalIn: decimal.Zero, Warnings: []string{}}
	senderCredits := decimal.Zero
	for _, ln := range lines {
		value, div := ln.Formula.Eval(envs[ln.ReceiverID])
		amount := value.Round(minor)
		if amount.IsNegative() && !allowNegative {
			return TemplateResult{}, fmt.Errorf("%w: receiver %s evaluates to %s", ErrNegativeReceiver, ln.ReceiverID, amount.StringFixed(minor))
		}
		if div {
			res.Warnings = append(res.Warnings, CodeMeasureZero+": a divisor is zero for receiver "+ln.ReceiverID+"; the division evaluates to 0")
		}
		res.Lines = append(res.Lines, LineResult{ReceiverID: ln.ReceiverID, ElementID: ln.ElementID, Amount: amount, DivByZero: div})
		res.TotalIn = res.TotalIn.Add(amount)
		senderCredits = senderCredits.Add(amount.Neg())
	}
	if !res.TotalIn.Add(senderCredits).IsZero() {
		return TemplateResult{}, ErrTemplateConservation
	}
	return res, nil
}
