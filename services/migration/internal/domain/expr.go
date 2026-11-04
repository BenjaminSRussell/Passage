package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// The restricted expression language of FR-10.3 (`rule: "expr"` on a field, and the `expr` op of rejectWhen).
//
//	literals     12  -3.5  'text'  "text"  true  false  null
//	columns      QTY  address.city  lines.0.sku  `Unit price`   (a source column; in rejectWhen a mapped target)
//	operators    unary - !   * /   + -   < <= > >=   == !=   &&   ||     (decimal arithmetic, never floating point)
//	functions    round(x, scale)  min(a, b, ...)  max(a, b, ...)  abs(x)  concat(a, b, ...)  substr(s, start[, len])
//	             upper(s)  lower(s)  trim(s)  len(s)  isempty(x)  coalesce(a, b, ...)  if(cond, then, else)
//
// An absent (null) column makes arithmetic null and a null result leaves the target absent, so its default, the data
// template value or the required check applies. Null equals only null, orders against nothing (the comparison is false)
// and counts as false in a boolean context (`!`, `&&`, `||`, `if`). There are no loops, assignments or user functions:
// an expression always ends, in time bounded by its length and the size of the row. Every number, whether a literal, a
// column or the result of an operator, stays below 1e30 with at most 40 decimal places, so one value with a huge
// exponent (`1e999999999`) cannot make the arithmetic build a number of a billion digits.
const (
	maxExprLen    = 500
	maxExprNodes  = 200
	maxExprDepth  = 24
	maxExprString = 10000
	divScale      = 12

	maxExprNumText = 80 // characters of text read as a number
	maxExprExp     = 30 // |value| < 1e30
	maxExprScale   = 40 // decimal places
)

var maxExprMagnitude = decimal.New(1, maxExprExp)

// errExprRange marks a number outside the expression limits; a field rule rejects it MIG-MAP-RANGE.
var errExprRange = errors.New("number out of range")

// exprNum parses text as a bounded number; the length cap comes before the parse, which is not linear in the digits.
func exprNum(text string) (decimal.Decimal, bool, error) {
	if len(text) > maxExprNumText {
		return decimal.Zero, false, nil
	}
	d, err := decimal.NewFromString(text)
	if err != nil {
		return decimal.Zero, false, nil
	}
	d, err = boundDec(d)
	return d, true, err
}

// boundDec checks the exponent before any comparison, since comparing rescales to the smaller exponent.
func boundDec(d decimal.Decimal) (decimal.Decimal, error) {
	tooBig := fmt.Errorf("%w: 1e30 or more", errExprRange)
	tooFine := fmt.Errorf("%w: more than %d decimal places", errExprRange, maxExprScale)
	exp := d.Exponent()
	switch {
	case exp > maxExprExp:
		return decimal.Zero, tooBig
	case exp < -2*maxExprScale: // a product of two in-range numbers never goes below this
		return decimal.Zero, tooFine
	case exp < -maxExprScale:
		t := d.Truncate(maxExprScale)
		if !t.Equal(d) {
			return decimal.Zero, tooFine
		}
		d = t
	}
	if d.Abs().GreaterThanOrEqual(maxExprMagnitude) {
		return decimal.Zero, tooBig
	}
	return d, nil
}

type xkind byte

const (
	xNull xkind = iota
	xStr
	xDec
	xBool
)

type xval struct {
	kind xkind
	s    string
	d    decimal.Decimal
	b    bool
}

func (v xval) text() string {
	switch v.kind {
	case xStr:
		return v.s
	case xDec:
		return v.d.String()
	case xBool:
		return strconv.FormatBool(v.b)
	}
	return ""
}

type exprNode interface {
	eval(env func(string) any) (xval, error)
}

// ExprProgram is a parsed expression, safe for concurrent use.
type ExprProgram struct {
	root exprNode
	refs []string
}

// Refs lists the columns the expression reads, in order of first use.
func (p *ExprProgram) Refs() []string { return p.refs }

// Eval evaluates against env, which returns the raw value of a column path (nil when absent).
func (p *ExprProgram) Eval(env func(string) any) (xval, error) { return p.root.eval(env) }

func exprErr(format string, a ...any) error { return fmt.Errorf(format, a...) }

// ---- lexer ----

type xtok struct {
	kind byte // n number, s string, i identifier, q `quoted column`, o operator, e end
	text string
	pos  int
}

func lexExpr(src string) ([]xtok, error) {
	var toks []xtok
	for i := 0; i < len(src); {
		c, _ := utf8.DecodeRuneInString(src[i:])
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			if strings.Count(src[i:j], ".") > 1 {
				return nil, exprErr("bad number %q at %d", src[i:j], i)
			}
			toks = append(toks, xtok{'n', src[i:j], i})
			i = j
		case c == '\'' || c == '"':
			j := i + 1
			var sb strings.Builder
			for ; j < len(src) && rune(src[j]) != c; j++ {
				sb.WriteByte(src[j])
			}
			if j >= len(src) {
				return nil, exprErr("unterminated string at %d", i)
			}
			toks = append(toks, xtok{'s', sb.String(), i})
			i = j + 1
		case c == '`':
			j := strings.IndexByte(src[i+1:], '`')
			if j < 1 {
				return nil, exprErr("unterminated or empty column name at %d", i)
			}
			toks = append(toks, xtok{'q', src[i+1 : i+1+j], i})
			i += j + 2
		case c == '_' || unicode.IsLetter(c):
			j := i
			for j < len(src) {
				r, w := utf8.DecodeRuneInString(src[j:])
				if r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r) {
					j += w
					continue
				}
				break
			}
			if j == i {
				return nil, exprErr("unexpected %q at %d", string(c), i)
			}
			toks = append(toks, xtok{'i', strings.TrimRight(src[i:j], "."), i})
			i = j
		default:
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch two {
			case "<=", ">=", "==", "!=", "&&", "||":
				toks = append(toks, xtok{'o', two, i})
				i += 2
				continue
			}
			if strings.ContainsRune("+-*/<>!(),", c) {
				toks = append(toks, xtok{'o', string(c), i})
				i++
				continue
			}
			return nil, exprErr("unexpected %q at %d", string(c), i)
		}
	}
	return append(toks, xtok{'e', "", len(src)}), nil
}

// ---- parser ----

type exprParser struct {
	toks  []xtok
	i     int
	nodes int
	refs  []string
	seen  map[string]bool
}

// ParseExpr parses and statically checks an expression.
func ParseExpr(src string) (*ExprProgram, error) {
	if strings.TrimSpace(src) == "" {
		return nil, exprErr("expression is empty")
	}
	if len(src) > maxExprLen {
		return nil, exprErr("expression is longer than %d characters", maxExprLen)
	}
	toks, err := lexExpr(src)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks, seen: map[string]bool{}}
	root, err := p.binary(0, exprLevels, 0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != 'e' {
		return nil, exprErr("unexpected %q at %d", t.text, t.pos)
	}
	return &ExprProgram{root: root, refs: p.refs}, nil
}

func (p *exprParser) peek() xtok { return p.toks[p.i] }
func (p *exprParser) next() xtok { t := p.toks[p.i]; p.i++; return t }
func (p *exprParser) isOp(s string) bool {
	t := p.peek()
	return t.kind == 'o' && t.text == s
}
func (p *exprParser) count() error {
	if p.nodes++; p.nodes > maxExprNodes {
		return exprErr("expression has more than %d terms", maxExprNodes)
	}
	return nil
}

type xbin struct {
	op   string
	l, r exprNode
}
type xun struct {
	op string
	x  exprNode
}
type xlit struct{ v xval }
type xref struct{ path string }
type xcall struct {
	fn   string
	args []exprNode
}

var exprLevels = [][]string{{"||"}, {"&&"}, {"==", "!="}, {"<", "<=", ">", ">="}, {"+", "-"}, {"*", "/"}}

func (p *exprParser) binary(depth int, levels [][]string, lvl int) (exprNode, error) {
	if depth > maxExprDepth {
		return nil, exprErr("expression is nested deeper than %d levels", maxExprDepth)
	}
	if lvl == len(levels) {
		return p.parseUnary(depth)
	}
	l, err := p.binary(depth, levels, lvl+1)
	if err != nil {
		return nil, err
	}
	for {
		matched := ""
		for _, op := range levels[lvl] {
			if p.isOp(op) {
				matched = op
			}
		}
		if matched == "" {
			return l, nil
		}
		p.next()
		r, err := p.binary(depth, levels, lvl+1)
		if err != nil {
			return nil, err
		}
		if err := p.count(); err != nil {
			return nil, err
		}
		l = &xbin{matched, l, r}
	}
}

func (p *exprParser) parseUnary(depth int) (exprNode, error) {
	if depth > maxExprDepth {
		return nil, exprErr("expression is nested deeper than %d levels", maxExprDepth)
	}
	if p.isOp("-") || p.isOp("!") {
		op := p.next().text
		x, err := p.parseUnary(depth + 1)
		if err != nil {
			return nil, err
		}
		if err := p.count(); err != nil {
			return nil, err
		}
		return &xun{op, x}, nil
	}
	return p.parsePrimary(depth)
}

var exprFuncs = map[string][2]int{
	"round": {2, 2}, "min": {2, 10}, "max": {2, 10}, "abs": {1, 1}, "concat": {2, 20}, "substr": {2, 3},
	"upper": {1, 1}, "lower": {1, 1}, "trim": {1, 1}, "len": {1, 1}, "isempty": {1, 1}, "coalesce": {2, 10}, "if": {3, 3},
}

func (p *exprParser) parsePrimary(depth int) (exprNode, error) {
	t := p.next()
	if err := p.count(); err != nil {
		return nil, err
	}
	switch t.kind {
	case 'n':
		d, ok, err := exprNum(t.text)
		if !ok {
			return nil, exprErr("bad number %q at %d", t.text, t.pos)
		}
		if err != nil {
			return nil, exprErr("number %s at %d: %v", t.text, t.pos, err)
		}
		return &xlit{xval{kind: xDec, d: d}}, nil
	case 'q':
		return p.ref(t.text), nil
	case 's':
		return &xlit{xval{kind: xStr, s: t.text}}, nil
	case 'i':
		switch t.text {
		case "true", "false":
			return &xlit{xval{kind: xBool, b: t.text == "true"}}, nil
		case "null":
			return &xlit{}, nil
		}
		if !p.isOp("(") {
			return p.ref(t.text), nil
		}
		ar, ok := exprFuncs[t.text]
		if !ok {
			return nil, exprErr("unknown function %s at %d", t.text, t.pos)
		}
		p.next()
		var args []exprNode
		if !p.isOp(")") {
			for {
				a, err := p.binary(depth+1, exprLevels, 0)
				if err != nil {
					return nil, err
				}
				args = append(args, a)
				if p.isOp(",") {
					p.next()
					continue
				}
				break
			}
		}
		if !p.isOp(")") {
			return nil, exprErr("expected ) after the arguments of %s at %d", t.text, p.peek().pos)
		}
		p.next()
		if len(args) < ar[0] || len(args) > ar[1] {
			return nil, exprErr("%s takes %d to %d arguments, got %d", t.text, ar[0], ar[1], len(args))
		}
		return &xcall{t.text, args}, nil
	case 'o':
		if t.text == "(" {
			x, err := p.binary(depth+1, exprLevels, 0)
			if err != nil {
				return nil, err
			}
			if !p.isOp(")") {
				return nil, exprErr("expected ) at %d", p.peek().pos)
			}
			p.next()
			return x, nil
		}
	}
	if t.kind == 'e' {
		return nil, exprErr("expression ends early")
	}
	return nil, exprErr("unexpected %q at %d", t.text, t.pos)
}

func (p *exprParser) ref(path string) exprNode {
	if !p.seen[path] {
		p.seen[path] = true
		p.refs = append(p.refs, path)
	}
	return &xref{path}
}

// ---- evaluation ----

func (n *xlit) eval(func(string) any) (xval, error) { return n.v, nil }

func (n *xref) eval(env func(string) any) (xval, error) {
	raw := env(n.path)
	switch x := raw.(type) {
	case nil:
		return xval{}, nil
	case bool:
		return xval{kind: xBool, b: x}, nil
	case int, int64, float64, json.Number:
		text, _, _ := sourceText(x)
		d, ok, err := exprNum(text)
		if !ok && len(text) > maxExprNumText {
			err = fmt.Errorf("%w: 1e30 or more", errExprRange)
		} else if !ok {
			return xval{}, exprErr("%s is not a number", n.path)
		}
		if err != nil {
			return xval{}, fmt.Errorf("%s: %w", n.path, err)
		}
		return xval{kind: xDec, d: d}, nil
	case time.Time:
		return xval{kind: xStr, s: x.Format(dateLayout)}, nil
	}
	text, present, scalar := sourceText(raw)
	if !scalar {
		return xval{}, exprErr("%s is not a scalar", n.path)
	}
	if !present {
		return xval{}, nil
	}
	return xval{kind: xStr, s: text}, nil
}

func (v xval) dec(what string) (decimal.Decimal, error) {
	switch v.kind {
	case xDec:
		return v.d, nil
	case xStr:
		if d, ok, err := exprNum(strings.TrimSpace(v.s)); ok {
			if err != nil {
				return decimal.Zero, fmt.Errorf("%s: %w", what, err)
			}
			return d, nil
		}
	}
	return decimal.Zero, exprErr("%s is not a number", what)
}

func (v xval) truthy() bool { return v.kind == xBool && v.b }

func (n *xun) eval(env func(string) any) (xval, error) {
	x, err := n.x.eval(env)
	if err != nil {
		return xval{}, err
	}
	if n.op == "!" {
		if x.kind != xNull && x.kind != xBool {
			return xval{}, exprErr("! needs a boolean")
		}
		return xval{kind: xBool, b: !x.truthy()}, nil
	}
	if x.kind == xNull {
		return xval{}, nil
	}
	d, err := x.dec("operand of -")
	if err != nil {
		return xval{}, err
	}
	return xval{kind: xDec, d: d.Neg()}, nil
}

func (n *xbin) eval(env func(string) any) (xval, error) {
	l, err := n.l.eval(env)
	if err != nil {
		return xval{}, err
	}
	if n.op == "&&" || n.op == "||" {
		if l.kind != xNull && l.kind != xBool {
			return xval{}, exprErr("%s needs booleans", n.op)
		}
		if n.op == "&&" && !l.truthy() || n.op == "||" && l.truthy() {
			return xval{kind: xBool, b: n.op == "||"}, nil
		}
		r, err := n.r.eval(env)
		if err != nil {
			return xval{}, err
		}
		if r.kind != xNull && r.kind != xBool {
			return xval{}, exprErr("%s needs booleans", n.op)
		}
		return xval{kind: xBool, b: r.truthy()}, nil
	}
	r, err := n.r.eval(env)
	if err != nil {
		return xval{}, err
	}
	switch n.op {
	case "==", "!=", "<", "<=", ">", ">=":
		return compareVals(n.op, l, r)
	}
	if l.kind == xNull || r.kind == xNull {
		return xval{}, nil
	}
	a, err := l.dec("left operand of " + n.op)
	if err != nil {
		return xval{}, err
	}
	b, err := r.dec("right operand of " + n.op)
	if err != nil {
		return xval{}, err
	}
	var d decimal.Decimal
	switch n.op {
	case "+":
		d = a.Add(b)
	case "-":
		d = a.Sub(b)
	case "*":
		d = a.Mul(b)
	case "/":
		if b.IsZero() {
			return xval{}, exprErr("division by zero")
		}
		d = a.DivRound(b, divScale)
	}
	if d, err = boundDec(d); err != nil {
		return xval{}, fmt.Errorf("result of %s: %w", n.op, err)
	}
	return xval{kind: xDec, d: d}, nil
}

// compareVals: a string is read as a number only when the other side is a number, so two codes such as "007" and
// "7" stay different; null equals only null and orders against nothing.
func compareVals(op string, l, r xval) (xval, error) {
	res := func(b bool) (xval, error) { return xval{kind: xBool, b: b}, nil }
	if l.kind == xNull || r.kind == xNull {
		switch op {
		case "==":
			return res(l.kind == r.kind)
		case "!=":
			return res(l.kind != r.kind)
		}
		return res(false)
	}
	var c int
	switch {
	case l.kind == xBool || r.kind == xBool:
		if l.kind != r.kind || op != "==" && op != "!=" {
			return xval{}, exprErr("a boolean compares only with a boolean, using == or !=")
		}
		if l.b != r.b {
			c = 1
		}
	case l.kind == xDec || r.kind == xDec:
		a, err := l.dec("left operand of " + op)
		if err != nil {
			return xval{}, err
		}
		b, err := r.dec("right operand of " + op)
		if err != nil {
			return xval{}, err
		}
		c = a.Cmp(b)
	default:
		c = strings.Compare(l.s, r.s)
	}
	switch op {
	case "==":
		return res(c == 0)
	case "!=":
		return res(c != 0)
	case "<":
		return res(c < 0)
	case "<=":
		return res(c <= 0)
	case ">":
		return res(c > 0)
	}
	return res(c >= 0)
}

func (n *xcall) eval(env func(string) any) (xval, error) {
	if n.fn == "if" {
		c, err := n.args[0].eval(env)
		if err != nil {
			return xval{}, err
		}
		if c.kind != xNull && c.kind != xBool {
			return xval{}, exprErr("if needs a boolean condition")
		}
		if c.truthy() {
			return n.args[1].eval(env)
		}
		return n.args[2].eval(env)
	}
	args := make([]xval, len(n.args))
	for i, a := range n.args {
		v, err := a.eval(env)
		if err != nil {
			return xval{}, err
		}
		args[i] = v
	}
	switch n.fn {
	case "coalesce":
		for _, a := range args {
			if a.kind != xNull && !(a.kind == xStr && a.s == "") {
				return a, nil
			}
		}
		return xval{}, nil
	case "isempty":
		return xval{kind: xBool, b: args[0].kind == xNull || args[0].kind == xStr && args[0].s == ""}, nil
	case "len":
		return xval{kind: xDec, d: decimal.NewFromInt(int64(utf8.RuneCountInString(args[0].text())))}, nil
	case "concat":
		var sb strings.Builder
		for _, a := range args {
			t := a.text()
			if sb.Len()+len(t) > maxExprString {
				return xval{}, errTooLong()
			}
			sb.WriteString(t)
		}
		return capString(sb.String())
	case "upper", "lower", "trim":
		if args[0].kind == xNull {
			return xval{}, nil
		}
		s := args[0].text()
		if n.fn != "trim" && len(s) > maxExprString {
			return xval{}, errTooLong()
		}
		switch n.fn {
		case "upper":
			s = strings.ToUpper(s)
		case "lower":
			s = strings.ToLower(s)
		default:
			s = strings.TrimSpace(s)
		}
		return capString(s)
	case "substr":
		return substrVal(args)
	case "abs", "round":
		if args[0].kind == xNull {
			return xval{}, nil
		}
		d, err := args[0].dec("argument of " + n.fn)
		if err != nil {
			return xval{}, err
		}
		if n.fn == "abs" {
			return xval{kind: xDec, d: d.Abs()}, nil
		}
		sc, err := args[1].dec("scale of round")
		if err != nil || !sc.IsInteger() || sc.IsNegative() || sc.GreaterThan(decimal.NewFromInt(maxDecimalScale)) {
			return xval{}, exprErr("round scale must be a whole number from 0 to %d", maxDecimalScale)
		}
		return xval{kind: xDec, d: d.RoundBank(int32(sc.IntPart()))}, nil
	}
	return minMax(n.fn, args)
}

func errTooLong() error { return exprErr("a text value is longer than %d characters", maxExprString) }

// capString bounds text that an expression builds (concat, upper, lower, trim); a column read as is or cut by substr is
// never longer than the row it came from.
func capString(s string) (xval, error) {
	if len(s) > maxExprString {
		return xval{}, errTooLong()
	}
	return xval{kind: xStr, s: s}, nil
}

// substrVal is 1-based like SQL: substr('ABCDE', 2, 3) is 'BCD'; a length past the end is cut.
func substrVal(args []xval) (xval, error) {
	if args[0].kind == xNull {
		return xval{}, nil
	}
	st, err := args[1].dec("start of substr")
	if err != nil || !st.IsInteger() || st.LessThan(decimal.NewFromInt(1)) {
		return xval{}, exprErr("substr start must be a whole number from 1")
	}
	s := args[0].text()
	// Clamp in decimal before converting: IntPart of a value past int64 is undefined.
	limit := decimal.NewFromInt(int64(len(s)) + 1)
	from := int(decimal.Min(st, limit).IntPart()) - 1
	count := len(s)
	if len(args) == 3 {
		ln, err := args[2].dec("length of substr")
		if err != nil || !ln.IsInteger() || ln.IsNegative() {
			return xval{}, exprErr("substr length must be a whole number, 0 or more")
		}
		count = int(decimal.Min(ln, limit).IntPart())
	}
	// Walk runes only as far as needed instead of converting the whole text.
	start, end, r := len(s), len(s), 0
	for i := range s {
		if r == from {
			start = i
		}
		if r == from+count {
			end = i
			break
		}
		r++
	}
	return xval{kind: xStr, s: s[start:end]}, nil
}

func minMax(fn string, args []xval) (xval, error) {
	var best xval
	for _, a := range args {
		if a.kind == xNull {
			continue
		}
		d, err := a.dec("argument of " + fn)
		if err != nil {
			return xval{}, err
		}
		if best.kind == xNull || fn == "min" && d.LessThan(best.d) || fn == "max" && d.GreaterThan(best.d) {
			best = xval{kind: xDec, d: d}
		}
	}
	return best, nil
}
