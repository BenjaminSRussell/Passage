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
	// StepNo, AccountKey and Statistical come from the procedure step that applied the
	// condition (FR-26.1); all are empty under the reference order. A statistical step is
	// computed and reported with its Amount (the change it would make to the net: negative
	// for a discount), but does not change the net.
	StepNo      int              `json:"stepNo,omitempty"`
	AccountKey  string           `json:"accountKey,omitempty"`
	Statistical bool             `json:"statistical,omitempty"`
	Amount      *decimal.Decimal `json:"amount,omitempty"`
}

// ProcedureRef names the procedure version a result was priced with (FR-26.4). It is part
// of the explain content address, so the same request under another version never shares a reference.
type ProcedureRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
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
	// StepNo is the procedure step (0 under the reference order). Reason says why a step
	// that found a record did not apply: an exclusion group chose another member (FR-26.8).
	StepNo int    `json:"stepNo,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Basis is set on the TOTAL_DISC step when it applied to the GROSS line amounts
	// (total_discount_basis); absent under AFTER_LINE_DISC, so the explain content address of a
	// document priced after line discounts is the one it had before the basis existed.
	Basis string `json:"basis,omitempty"`
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
	// Surcharge is the small-order surcharge added to the order net (FR-26.6); zero when none applied.
	Surcharge decimal.Decimal `json:"surcharge"`
	OrderNet  decimal.Decimal `json:"orderNet"`
	Applied   []Applied       `json:"applied"`
	Trace     []TraceStep     `json:"trace"`
	// Procedure is the procedure version that drove the resolution; nil under the reference order.
	Procedure *ProcedureRef `json:"procedure,omitempty"`
	// PricingDate is the day the document was priced on (BR-26.2), YYYY-MM-DD.
	PricingDate string `json:"pricingDate"`
}

// Resolve prices every line and then the order. It is a pure function of its input.
// The total discount applies to the lines no exclusive winner protected; protected
// lines keep their net (BR-26.1 step 8). Under a procedure (Input.Procedure) the steps run
// in step order with their exclusion groups and statistical flags (FR-26.1, FR-26.8);
// without one they run in the reference order.
func Resolve(in Input) Result {
	plan := in.plan()
	recs := in.Set.index()
	if in.Set == nil {
		recs = bucket(in.Records)
	}
	groups := groupQuantities(in.Lines)
	res := Result{Currency: in.Header.Currency, Lines: make([]LineResult, 0, len(in.Lines)), Applied: []Applied{}, Trace: []TraceStep{},
		PricingDate: dayOf(in.Header.PricingDate)}
	if in.Procedure != nil && len(in.Procedure.Steps) > 0 {
		res.Procedure = &ProcedureRef{ID: in.Procedure.ID, Name: in.Procedure.Name, Version: in.Procedure.Version}
	}
	sum, base, gross, qty, allQty := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	for _, line := range in.Lines {
		lr := resolveLine(in.Header, line, recs, plan, groups[groupKey(line)])
		res.Lines = append(res.Lines, lr)
		if lr.Error != "" {
			continue
		}
		sum = sum.Add(lr.LineNet)
		allQty = allQty.Add(line.Quantity)
		if !lr.Exclusive {
			base = base.Add(lr.LineNet)
			gross = gross.Add(roundMinor(lr.Quantity.Mul(lr.UnitPrice), in.Header.Currency))
			qty = qty.Add(line.Quantity)
		}
	}
	res.Subtotal, res.OrderNet = sum, sum
	// Order-level steps run in step order, each type once. The total discount applies to the
	// summed nets of the unprotected lines; the small-order surcharge (FR-26.6) is a separate
	// condition, reported on its own (Surcharge) so the caller can post it to its revenue
	// account, and added to OrderNet unless the step is statistical.
	seen := map[ConditionType]bool{}
	for _, st := range plan {
		t := st.ConditionType
		if !orderLevel(t) || seen[t] {
			continue
		}
		seen[t] = true
		switch t {
		case TypeTotalDisc:
			if !base.IsPositive() {
				continue
			}
			// total_discount_basis GROSS applies the discount to the line amounts at the PRICE step's
			// unit price; the discount is then capped at the nets it can come off.
			discBase := base
			if in.TotalDiscountBasis == TotalBasisGross {
				discBase = gross
			}
			rec, value, access, step := evaluate(t, in.Header, Line{Quantity: qty}, recs, discBase)
			step.StepNo = st.StepNo
			if in.TotalDiscountBasis == TotalBasisGross {
				step.Basis = TotalBasisGross
			}
			res.Trace = append(res.Trace, step)
			if rec == nil {
				continue
			}
			discounted := roundMinor(value, in.Header.Currency)
			if cut := discBase.Sub(discounted); cut.GreaterThan(base) {
				discounted = discBase.Sub(base)
			}
			ap := Applied{Type: t, RecordID: rec.ID, Access: access, StepNo: st.StepNo, AccountKey: st.AccountKey}
			if st.Statistical {
				change := discounted.Sub(discBase)
				ap.Statistical, ap.Amount = true, &change
			} else {
				res.TotalDiscount = discBase.Sub(discounted)
				res.OrderNet = res.OrderNet.Sub(res.TotalDiscount)
			}
			res.Applied = append(res.Applied, ap)
		case TypeSurcharge:
			if !in.SmallOrderThreshold.IsPositive() || !sum.IsPositive() || !sum.LessThan(in.SmallOrderThreshold) {
				continue
			}
			rec, value, access, step := evaluate(t, in.Header, Line{Quantity: allQty}, recs, res.OrderNet)
			step.StepNo = st.StepNo
			res.Trace = append(res.Trace, step)
			if rec == nil {
				continue
			}
			surcharged := roundMinor(value, in.Header.Currency)
			ap := Applied{Type: t, RecordID: rec.ID, Access: access, StepNo: st.StepNo, AccountKey: st.AccountKey}
			change := surcharged.Sub(res.OrderNet)
			if st.Statistical {
				ap.Statistical, ap.Amount = true, &change
			} else {
				res.Surcharge = change
				res.OrderNet = surcharged
			}
			res.Applied = append(res.Applied, ap)
		}
	}
	return res
}

// plan is the ordered steps a resolve runs: the procedure's, or the reference order (Steps,
// default DefaultSteps) as steps with no number, group or flag.
func (in Input) plan() []ProcedureStep {
	if in.Procedure != nil && len(in.Procedure.Steps) > 0 {
		return in.Procedure.Steps
	}
	types := in.Steps
	if len(types) == 0 {
		types = DefaultSteps()
	}
	out := make([]ProcedureStep, len(types))
	for i, t := range types {
		out[i] = ProcedureStep{ConditionType: t}
	}
	return out
}

// orderLevel reports whether t prices the whole order rather than one line.
func orderLevel(t ConditionType) bool { return t == TypeTotalDisc || t == TypeSurcharge }

// groupKey identifies one multiline group: lines sharing product group and unit (BR-26.4).
func groupKey(l Line) string { return l.ProductGroupID + "\x00" + l.Unit }

// groupQuantities sums quantities per multiline group over the whole document.
// Lines without a product group are their own group and are not summed.
func groupQuantities(lines []Line) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	for _, l := range lines {
		if l.ProductGroupID == "" {
			continue
		}
		k := groupKey(l)
		out[k] = out[k].Add(l.Quantity)
	}
	return out
}

// SmallOrderThreshold is the reference small-order threshold per currency (FR-26.6): 250 USD, 200 EUR.
// Other currencies have none (zero).
func SmallOrderThreshold(currency string) decimal.Decimal {
	switch currency {
	case "USD":
		return decimal.NewFromInt(250)
	case "EUR":
		return decimal.NewFromInt(200)
	}
	return decimal.Zero
}

// lineEvaluable reports whether the engine prices a step of type t on one line. The other
// types a procedure may sequence (FREIGHT, REBATE_ACCRUAL, ROUNDING, FREE_GOODS) have no
// records yet and change no price here.
func lineEvaluable(t ConditionType) bool { return t == TypeLineDisc || t == TypeMultiline }

// stepCandidate is one member of an exclusion group as evaluated on a line.
type stepCandidate struct {
	st     ProcedureStep
	rec    *Record
	value  decimal.Decimal
	access int
	trace  TraceStep
}

func resolveLine(h Header, line Line, recs index, plan []ProcedureStep, groupQty decimal.Decimal) LineResult {
	lr := LineResult{No: line.No, Unit: line.Unit, Quantity: line.ChargedQuantity(), Applied: []Applied{}, Trace: []TraceStep{}}
	rec, value, access, step := evaluate(TypePrice, h, line, recs, decimal.Zero)
	var priceStep ProcedureStep
	for _, st := range plan {
		if st.ConditionType == TypePrice {
			priceStep = st
			break
		}
	}
	step.StepNo = priceStep.StepNo
	lr.Trace = append(lr.Trace, step)
	if rec == nil {
		lr.Error = CodePriceNotFound
		lr.ErrorDetail = fmt.Sprintf("no price for product %s on %s", line.ProductID, dayOf(h.PricingDate))
		return lr
	}
	// The PRICE step's account key names the revenue account the caller posts the price to.
	lr.Applied = append(lr.Applied, Applied{Type: TypePrice, RecordID: rec.ID, Access: access, StepNo: priceStep.StepNo, AccountKey: priceStep.AccountKey})
	lr.UnitPrice = value
	net, suppressed := value, rec.Exclusive
	try := func(st ProcedureStep, from decimal.Decimal) stepCandidate {
		l := line
		if st.ConditionType == TypeMultiline && line.ProductGroupID != "" {
			// Multiline records match on the summed group quantity, not the line quantity.
			l.Quantity = groupQty
		}
		r, v, a, tr := evaluate(st.ConditionType, h, l, recs, from)
		tr.StepNo = st.StepNo
		return stepCandidate{st: st, rec: r, value: v, access: a, trace: tr}
	}
	// Each line-level condition type prices once (ValidateSteps refuses a repeated type; a
	// procedure stored before that rule must not apply the same record twice).
	handled, seen := map[string]bool{}, map[ConditionType]bool{}
	for i, st := range plan {
		if !lineEvaluable(st.ConditionType) || suppressed || seen[st.ConditionType] {
			continue
		}
		if st.ExclusionGroup == "" || st.Statistical {
			seen[st.ConditionType] = true
		}
		if st.Statistical {
			// A statistical step is computed from the running net and reported; it changes nothing.
			c := try(st, net)
			lr.Trace = append(lr.Trace, c.trace)
			if c.rec != nil {
				change := roundMinor(line.ChargedQuantity().Mul(c.value.Sub(net)), h.Currency)
				lr.Applied = append(lr.Applied, Applied{Type: st.ConditionType, RecordID: c.rec.ID, Access: c.access,
					StepNo: st.StepNo, AccountKey: st.AccountKey, Statistical: true, Amount: &change})
			}
			continue
		}
		var chosen stepCandidate
		if st.ExclusionGroup == "" {
			chosen = try(st, net)
			lr.Trace = append(lr.Trace, chosen.trace)
		} else {
			if handled[st.ExclusionGroup] {
				continue
			}
			handled[st.ExclusionGroup] = true
			chosen = chooseFromGroup(plan[i:], st.ExclusionGroup, net, try, &lr, seen)
		}
		if chosen.rec == nil {
			continue
		}
		net = chosen.value
		suppressed = chosen.rec.Exclusive
		lr.Applied = append(lr.Applied, Applied{Type: chosen.st.ConditionType, RecordID: chosen.rec.ID, Access: chosen.access,
			StepNo: chosen.st.StepNo, AccountKey: chosen.st.AccountKey})
	}
	lr.NetUnit = net
	lr.LineNet = roundMinor(line.ChargedQuantity().Mul(net), h.Currency)
	lr.Exclusive = suppressed
	return lr
}

// chooseFromGroup evaluates every member of an exclusion group from the same running net
// and keeps one (FR-26.8): BEST the member with the lowest resulting net (the lower step on
// a tie), FIRST the member with the lowest step number that found a record. The others
// stay in the trace with the reason they did not apply. rest starts at the first member.
func chooseFromGroup(rest []ProcedureStep, group string, net decimal.Decimal, try func(ProcedureStep, decimal.Decimal) stepCandidate, lr *LineResult, seen map[ConditionType]bool) stepCandidate {
	var members []stepCandidate
	for _, st := range rest {
		if st.ExclusionGroup == group && lineEvaluable(st.ConditionType) && !st.Statistical && !seen[st.ConditionType] {
			seen[st.ConditionType] = true
			members = append(members, try(st, net))
		}
	}
	mode := rest[0].ExclusionMode
	win := -1
	for i, m := range members {
		switch {
		case m.rec == nil:
		case win < 0:
			win = i
		case mode == ExclusionBest && m.value.LessThan(members[win].value):
			win = i
		}
	}
	for i := range members {
		tr := members[i].trace
		switch {
		case members[i].rec == nil:
		case i == win:
			tr.Reason = fmt.Sprintf("selected: %s of exclusion group %s", mode, group)
		default:
			tr.Reason = fmt.Sprintf("excluded: %s of exclusion group %s kept step %d", mode, group, members[win].st.StepNo)
		}
		lr.Trace = append(lr.Trace, tr)
	}
	if win < 0 {
		return stepCandidate{}
	}
	return members[win]
}

// evaluate runs one condition type through the access sequence (BR-26.1 steps 2-6).
// It returns the best winner (lowest value; equal values keep the earlier access),
// the value it produces on current, the access sequence number and the trace.
func evaluate(t ConditionType, h Header, line Line, recs index, current decimal.Decimal) (*Record, decimal.Decimal, int, TraceStep) {
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
// greatest from_quantity, then most dimensions matched (a campaign record before a
// plain one, BR-26.1 step 4), then lowest value, then id (deterministic).
func candidates(t ConditionType, h Header, line Line, recs index, acc Access, current decimal.Decimal) []*Record {
	var out []*Record
	day := dayNum(h.PricingDate)
	for _, r := range recs[accessKey(t, acc, h, line)] {
		if r.Status != StatusActive || r.Currency != h.Currency || dayNum(r.ValidFrom) > day || day > dayNum(r.ValidTo) {
			continue
		}
		// TOTAL_DISC and SURCHARGE are order-level conditions: the order has no single unit,
		// so the record's unit is not compared (its from_quantity applies to the summed quantity).
		if !orderLevel(t) && r.Unit != "" && r.Unit != line.Unit {
			continue
		}
		if r.FromQuantity.GreaterThan(line.Quantity) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if c := a.FromQuantity.Cmp(b.FromQuantity); c != 0 {
			return c > 0
		}
		if (a.CampaignID != "") != (b.CampaignID != "") {
			return a.CampaignID != ""
		}
		if c := apply(t, current, a).Cmp(apply(t, current, b)); c != 0 {
			return c < 0
		}
		return a.ID < b.ID
	})
	return out
}

// RecordSet is a read-only set of condition records bucketed by access key, built once
// and shared by any number of concurrent resolves (the record snapshot cache of FR-26.14:
// a resolve reads only the buckets its lines' accesses name, so its cost does not grow
// with the price list). Resolve never changes it.
type RecordSet struct {
	recs []Record
	idx  index
}

// NewRecordSet copies recs and buckets the copy, so later changes to recs do not reach it.
func NewRecordSet(recs []Record) *RecordSet {
	own := append([]Record(nil), recs...)
	return &RecordSet{recs: own, idx: bucket(own)}
}

// Records returns a copy of the set's records in the order they were given.
func (s *RecordSet) Records() []Record {
	return append([]Record(nil), s.recs...)
}

func (s *RecordSet) index() index {
	if s == nil {
		return nil
	}
	return s.idx
}

// index holds records bucketed by condition type and access key, each bucket in id order.
type index map[indexKey][]*Record

// indexKey is one access bucket. A key field is set only when its kind compares it: an
// "all" kind ignores the key, and only the variant kind compares the variant.
type indexKey struct {
	typ         ConditionType
	customer    CustomerKind
	customerKey string
	product     ProductKind
	productKey  string
	variant     string
}

// bucket indexes recs by access key. It reads recs and never changes them; status,
// currency and validity are checked per candidate, so one index serves any header.
func bucket(recs []Record) index {
	idx := index{}
	for i := range recs {
		r := &recs[i]
		k := indexKey{typ: r.Type, customer: r.CustomerKind, product: r.ProductKind}
		if r.CustomerKind == CustomerKindCustomer || r.CustomerKind == CustomerKindGroup {
			k.customerKey = r.CustomerKey
		}
		switch r.ProductKind {
		case ProductKindProduct, ProductKindGroup:
			k.productKey = r.ProductKey
		case ProductKindVariant:
			k.productKey, k.variant = r.ProductKey, r.Variant
		}
		idx[k] = append(idx[k], r)
	}
	for _, b := range idx {
		sort.SliceStable(b, func(i, j int) bool { return b[i].ID < b[j].ID })
	}
	return idx
}

// accessKey is the bucket access acc reads for line: the header's customer or customer
// group and the line's product, product group or product and variant (BR-26.1 step 3).
func accessKey(t ConditionType, acc Access, h Header, line Line) indexKey {
	k := indexKey{typ: t, customer: acc.Customer, product: acc.Product}
	switch acc.Customer {
	case CustomerKindCustomer:
		k.customerKey = h.CustomerID
	case CustomerKindGroup:
		k.customerKey = h.CustomerGroupID
	}
	switch acc.Product {
	case ProductKindProduct:
		k.productKey = line.ProductID
	case ProductKindGroup:
		k.productKey = line.ProductGroupID
	case ProductKindVariant:
		k.productKey, k.variant = line.ProductID, line.Variant
	}
	return k
}

// dayNum is the calendar day of t as yyyymmdd, ordered like the days themselves.
func dayNum(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

// apply computes the value a record produces on current, rounded to 6 decimals (BR-26.3).
// PRICE: amount x (1-p1)(1-p2). Discounts: (current - amount) x (1-p1)(1-p2), floored at zero (BR-26.10).
func apply(t ConditionType, current decimal.Decimal, r *Record) decimal.Decimal {
	f := pctFactor(r.DiscPct1, r.DiscPct2)
	if t == TypePrice {
		return r.Amount.Mul(f).RoundBank(6)
	}
	if t == TypeSurcharge {
		// Surcharge: current + fixed amount + DiscPct1 percent of current (FR-26.6).
		return current.Add(r.Amount).Add(current.Mul(r.DiscPct1).Shift(-2)).RoundBank(6)
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
// currency, unit, from_quantity and campaign with intersecting validity (FR-26.2, BR-26.6).
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
		a.Currency == b.Currency && a.Unit == b.Unit && a.FromQuantity.Equal(b.FromQuantity) && a.CampaignID == b.CampaignID
}

// validityOverlaps reports whether two inclusive day ranges intersect.
func validityOverlaps(a, b Record) bool {
	return dayOf(a.ValidFrom) <= dayOf(b.ValidTo) && dayOf(b.ValidFrom) <= dayOf(a.ValidTo)
}

// ExplainTrace returns the canonical JSON of the trace and its content address
// (FR-26.4). Identical results always yield identical bytes and reference.
func ExplainTrace(r Result) ([]byte, string, error) {
	body, err := json.Marshal(struct {
		Procedure *ProcedureRef `json:"procedure,omitempty"`
		Lines     []LineResult  `json:"lines"`
		Trace     []TraceStep   `json:"trace"`
	}{r.Procedure, r.Lines, r.Trace})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, "sha256:" + hex.EncodeToString(sum[:]), nil
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
		fields["type"] = "must be PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC or SURCHARGE"
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
	// TOTAL_DISC and SURCHARGE price the order, which has no product: a product-keyed
	// order condition could never apply, so it is refused instead of stored silently.
	if orderLevel(r.Type) && r.ProductKind != ProductKindAll {
		fields["productKind"] = "must be all for " + string(r.Type) + " (an order-level condition)"
	}
	// MULTILINE_DISC sums the quantity of lines sharing a product group (BR-26.4), so it is
	// keyed by product group or applies to all products.
	if r.Type == TypeMultiline && r.ProductKind != ProductKindGroup && r.ProductKind != ProductKindAll {
		fields["productKind"] = "must be product_group or all for MULTILINE_DISC"
	}
	// A surcharge adds a fixed amount and one percentage; a second percentage has no meaning.
	if r.Type == TypeSurcharge && !r.DiscPct2.IsZero() {
		fields["discPct2"] = "must be 0 for SURCHARGE"
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

// ValidType reports whether t is a condition type this service supports.
func ValidType(t ConditionType) bool {
	switch t {
	case TypePrice, TypeLineDisc, TypeTotalDisc, TypeMultiline, TypeSurcharge:
		return true
	}
	return false
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
