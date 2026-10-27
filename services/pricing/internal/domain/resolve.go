package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Stable error codes raised by the engine (message catalog PRC-*).
const (
	CodePriceNotFound = "PRC-0001"
	CodeOverlap       = "PRC-0002"
	CodeNegative      = "PRC-0003"
	CodeValidation    = "COM-0001"
)

// Applied names one condition that changed the price.
type Applied struct {
	Type     ConditionType `json:"type"`
	RecordID string        `json:"recordId"`
	Access   int           `json:"access"`
}

// TraceAccess is one access of an access sequence as evaluated for a condition.
type TraceAccess struct {
	Seq        int      `json:"seq"`
	Access     string   `json:"access"`
	Evaluated  bool     `json:"evaluated"`
	Candidates []string `json:"candidates"`
	Winner     string   `json:"winner,omitempty"`
	Reason     string   `json:"reason"`
}

// TraceStep is the access-by-access trace of one condition type.
type TraceStep struct {
	Condition ConditionType `json:"condition"`
	Accesses  []TraceAccess `json:"accesses"`
	Selected  string        `json:"selected,omitempty"`
}

// LineResult is the priced line. Error is set (and the amounts are zero) when no price exists.
// Exclusive is set when an exclusive winner suppressed the later discount steps for
// this line, including its share of the total discount (BR-26.1 step 8).
type LineResult struct {
	No          int             `json:"lineNo"`
	Unit        string          `json:"unit"`
	Quantity    decimal.Decimal `json:"quantity"`
	UnitPrice   decimal.Decimal `json:"unitPrice"`
	NetUnit     decimal.Decimal `json:"netUnitPrice"`
	LineNet     decimal.Decimal `json:"lineNet"`
	Exclusive   bool            `json:"exclusive,omitempty"`
	Applied     []Applied       `json:"applied"`
	Trace       []TraceStep     `json:"trace"`
	Error       string          `json:"errorCode,omitempty"`
	ErrorDetail string          `json:"errorDetail,omitempty"`
}

// Result is the priced document. Subtotal is the sum of rounded line nets (BR-26.3).
type Result struct {
	Currency      string          `json:"currency"`
	Lines         []LineResult    `json:"lines"`
	Subtotal      decimal.Decimal `json:"subtotal"`
	TotalDiscount decimal.Decimal `json:"totalDiscount"`
	OrderNet      decimal.Decimal `json:"orderNet"`
	Applied       []Applied       `json:"applied"`
	Trace         []TraceStep     `json:"trace"`
}

// Resolve prices every line and then the order. It is a pure function of its input.
// The total discount applies to the lines no exclusive winner protected; protected
// lines keep their net (BR-26.1 step 8).
func Resolve(in Input) Result {
	steps := in.Steps
	if len(steps) == 0 {
		steps = DefaultSteps()
	}
	recs := sortedRecords(in.Records)
	res := Result{Currency: in.Header.Currency, Lines: make([]LineResult, 0, len(in.Lines)), Applied: []Applied{}, Trace: []TraceStep{}}
	sum, base, qty := decimal.Zero, decimal.Zero, decimal.Zero
	for _, line := range in.Lines {
		lr := resolveLine(in.Header, line, recs, steps)
		res.Lines = append(res.Lines, lr)
		if lr.Error != "" {
			continue
		}
		sum = sum.Add(lr.LineNet)
		if !lr.Exclusive {
			base = base.Add(lr.LineNet)
			qty = qty.Add(line.Quantity)
		}
	}
	res.Subtotal, res.OrderNet = sum, sum
	if hasStep(steps, TypeTotalDisc) && base.IsPositive() {
		rec, value, access, step := evaluate(TypeTotalDisc, in.Header, Line{Quantity: qty}, recs, base)
		res.Trace = append(res.Trace, step)
		if rec != nil {
			discounted := roundMinor(value, in.Header.Currency)
			res.TotalDiscount = base.Sub(discounted)
			res.OrderNet = sum.Sub(res.TotalDiscount)
			res.Applied = append(res.Applied, Applied{Type: TypeTotalDisc, RecordID: rec.ID, Access: access})
		}
	}
	return res
}

func resolveLine(h Header, line Line, recs []Record, steps []ConditionType) LineResult {
	lr := LineResult{No: line.No, Unit: line.Unit, Quantity: line.Quantity, Applied: []Applied{}, Trace: []TraceStep{}}
	rec, value, access, step := evaluate(TypePrice, h, line, recs, decimal.Zero)
	lr.Trace = append(lr.Trace, step)
	if rec == nil {
		lr.Error = CodePriceNotFound
		lr.ErrorDetail = fmt.Sprintf("no price for product %s on %s", line.ProductID, dayOf(h.PricingDate))
		return lr
	}
	lr.Applied = append(lr.Applied, Applied{Type: TypePrice, RecordID: rec.ID, Access: access})
	lr.UnitPrice = value
	net, suppressed := value, rec.Exclusive
	for _, t := range steps {
		if t == TypePrice || t == TypeTotalDisc || suppressed {
			continue
		}
		rec, v, access, step := evaluate(t, h, line, recs, net)
		lr.Trace = append(lr.Trace, step)
		if rec == nil {
			continue
		}
		net = v
		suppressed = rec.Exclusive
		lr.Applied = append(lr.Applied, Applied{Type: t, RecordID: rec.ID, Access: access})
	}
	lr.NetUnit = net
	lr.LineNet = roundMinor(line.Quantity.Mul(net), h.Currency)
	lr.Exclusive = suppressed
	return lr
}

// evaluate runs one condition type through the access sequence (BR-26.1 steps 2-6).
// It returns the best winner (lowest value; equal values keep the earlier access),
// the value it produces on current, the access sequence number and the trace.
func evaluate(t ConditionType, h Header, line Line, recs []Record, current decimal.Decimal) (*Record, decimal.Decimal, int, TraceStep) {
	step := TraceStep{Condition: t, Accesses: []TraceAccess{}}
	var best *Record
	var bestValue decimal.Decimal
	bestAccess := 0
	stopped := false
	for _, acc := range DefaultAccesses() {
		ta := TraceAccess{Seq: acc.Seq, Access: acc.Name(), Candidates: []string{}}
		if stopped {
			ta.Reason = "not evaluated: an earlier access produced the winner"
			step.Accesses = append(step.Accesses, ta)
			continue
		}
		ta.Evaluated = true
		cands := candidates(t, h, line, recs, acc, current)
		for _, c := range cands {
			ta.Candidates = append(ta.Candidates, c.ID)
		}
		if len(cands) == 0 {
			ta.Reason = "no candidate at this access"
			step.Accesses = append(step.Accesses, ta)
			continue
		}
		w := cands[0]
		v := apply(t, current, w)
		ta.Winner = w.ID
		if best == nil || v.LessThan(bestValue) {
			best, bestValue, bestAccess = w, v, acc.Seq
		}
		if w.SearchNext {
			ta.Reason = "winner has search_next: continue to the next access"
		} else {
			ta.Reason = "winner at this access: search stops"
			stopped = true
		}
		step.Accesses = append(step.Accesses, ta)
	}
	if best != nil {
		step.Selected = best.ID
	}
	return best, bestValue, bestAccess, step
}

// candidates returns the matching ACTIVE records for one access, best first:
// greatest from_quantity, then lowest value, then id (deterministic).
func candidates(t ConditionType, h Header, line Line, recs []Record, acc Access, current decimal.Decimal) []*Record {
	var out []*Record
	for i := range recs {
		r := &recs[i]
		if r.Type != t || r.Status != StatusActive || r.Currency != h.Currency || !inValidity(*r, h.PricingDate) {
			continue
		}
		// TOTAL_DISC is an order-level condition: the order has no single unit, so the
		// record's unit is not compared (its from_quantity applies to the summed quantity).
		if t != TypeTotalDisc && r.Unit != "" && r.Unit != line.Unit {
			continue
		}
		if r.FromQuantity.GreaterThan(line.Quantity) {
			continue
		}
		if matchesAccess(*r, acc, h, line) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if c := a.FromQuantity.Cmp(b.FromQuantity); c != 0 {
			return c > 0
		}
		if c := apply(t, current, a).Cmp(apply(t, current, b)); c != 0 {
			return c < 0
		}
		return a.ID < b.ID
	})
	return out
}

func matchesAccess(r Record, acc Access, h Header, line Line) bool {
	if r.CustomerKind != acc.Customer || r.ProductKind != acc.Product {
		return false
	}
	switch r.CustomerKind {
	case CustomerKindCustomer:
		if r.CustomerKey != h.CustomerID {
			return false
		}
	case CustomerKindGroup:
		if r.CustomerKey != h.CustomerGroupID {
			return false
		}
	}
	switch r.ProductKind {
	case ProductKindProduct:
		return r.ProductKey == line.ProductID
	case ProductKindGroup:
		return r.ProductKey == line.ProductGroupID
	case ProductKindVariant:
		return r.ProductKey == line.ProductID && r.Variant == line.Variant
	}
	return true
}

// apply computes the value a record produces on current, rounded to 6 decimals (BR-26.3).
// PRICE: amount x (1-p1)(1-p2). Discounts: (current - amount) x (1-p1)(1-p2), floored at zero (BR-26.10).
func apply(t ConditionType, current decimal.Decimal, r *Record) decimal.Decimal {
	f := pctFactor(r.DiscPct1, r.DiscPct2)
	if t == TypePrice {
		return r.Amount.Mul(f).RoundBank(6)
	}
	base := current.Sub(r.Amount)
	if base.IsNegative() {
		base = decimal.Zero
	}
	return base.Mul(f).RoundBank(6)
}

func pctFactor(p1, p2 decimal.Decimal) decimal.Decimal {
	hundred := decimal.NewFromInt(100)
	return hundred.Sub(p1).Mul(hundred.Sub(p2)).Shift(-4)
}

// Conflict reports the first ACTIVE record that candidate c would overlap: same key,
// currency, unit and from_quantity with intersecting validity (FR-26.2, BR-26.6).
func Conflict(c Record, existing []Record) (Record, bool) {
	for _, r := range existing {
		if r.ID != c.ID && r.Status == StatusActive && sameKey(c, r) && validityOverlaps(c, r) {
			return r, true
		}
	}
	return Record{}, false
}

func sameKey(a, b Record) bool {
	return a.Type == b.Type && a.CustomerKind == b.CustomerKind && a.CustomerKey == b.CustomerKey &&
		a.ProductKind == b.ProductKind && a.ProductKey == b.ProductKey && a.Variant == b.Variant &&
		a.Currency == b.Currency && a.Unit == b.Unit && a.FromQuantity.Equal(b.FromQuantity)
}

// validityOverlaps reports whether two inclusive day ranges intersect.
func validityOverlaps(a, b Record) bool {
	return dayOf(a.ValidFrom) <= dayOf(b.ValidTo) && dayOf(b.ValidFrom) <= dayOf(a.ValidTo)
}

func inValidity(r Record, date time.Time) bool {
	d := dayOf(date)
	return dayOf(r.ValidFrom) <= d && d <= dayOf(r.ValidTo)
}

// ExplainTrace returns the canonical JSON of the trace and its content address
// (FR-26.4). Identical results always yield identical bytes and reference.
func ExplainTrace(r Result) ([]byte, string, error) {
	body, err := json.Marshal(struct {
		Lines []LineResult `json:"lines"`
		Trace []TraceStep  `json:"trace"`
	}{r.Lines, r.Trace})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sortedRecords(in []Record) []Record {
	out := append([]Record(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func hasStep(steps []ConditionType, t ConditionType) bool {
	for _, s := range steps {
		if s == t {
			return true
		}
	}
	return false
}

func roundMinor(d decimal.Decimal, currency string) decimal.Decimal {
	return d.RoundBank(money.MinorUnits(currency))
}

// dayOf renders the calendar day of t (validity dates are days, not instants).
func dayOf(t time.Time) string { return t.Format("2006-01-02") }

// Error is a validation failure with a stable code and field-level detail.
type Error struct {
	Code   string
	Detail string
	Fields map[string]string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// ValidateRecord checks a record before it is stored (FR-26.2, BR-26.10).
func ValidateRecord(r Record) *Error {
	fields := map[string]string{}
	if !ValidType(r.Type) {
		fields["type"] = "must be PRICE, LINE_DISC or TOTAL_DISC"
	}
	if !isCurrencyCode(r.Currency) {
		fields["currency"] = "must be an ISO 4217 code in capitals"
	}
	switch r.CustomerKind {
	case CustomerKindCustomer, CustomerKindGroup:
		if r.CustomerKey == "" {
			fields["customerKey"] = "is required for this customer kind"
		}
	case CustomerKindAll:
	default:
		fields["customerKind"] = "must be customer, customer_group or all"
	}
	switch r.ProductKind {
	case ProductKindProduct, ProductKindGroup:
		if r.ProductKey == "" {
			fields["productKey"] = "is required for this product kind"
		}
	case ProductKindVariant:
		if r.ProductKey == "" || r.Variant == "" {
			fields["productKey"] = "product and variant are required"
		}
	case ProductKindAll:
	default:
		fields["productKind"] = "must be product, product_group, variant or all"
	}
	// TOTAL_DISC prices the order, which has no product: a product-keyed total
	// discount could never apply, so it is refused instead of stored silently.
	if r.Type == TypeTotalDisc && r.ProductKind != ProductKindAll {
		fields["productKind"] = "must be all for TOTAL_DISC (an order-level discount)"
	}
	if r.Amount.IsNegative() {
		fields["amount"] = "must not be negative"
	}
	if r.FromQuantity.IsNegative() {
		fields["fromQuantity"] = "must not be negative"
	}
	for name, p := range map[string]decimal.Decimal{"discPct1": r.DiscPct1, "discPct2": r.DiscPct2} {
		if p.IsNegative() || p.GreaterThan(decimal.NewFromInt(100)) {
			fields[name] = "must be from 0 to 100"
		}
	}
	if r.ValidFrom.IsZero() || r.ValidTo.IsZero() || dayOf(r.ValidFrom) > dayOf(r.ValidTo) {
		fields["validity"] = "validFrom must be on or before validTo"
	}
	if len(fields) == 0 {
		return nil
	}
	code := CodeValidation
	if _, ok := fields["amount"]; ok {
		code = CodeNegative
	}
	return &Error{Code: code, Detail: "condition record is invalid", Fields: fields}
}

// ValidType reports whether t is a condition type this slice supports.
func ValidType(t ConditionType) bool {
	return t == TypePrice || t == TypeLineDisc || t == TypeTotalDisc
}

func isCurrencyCode(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, ch := range c {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}
