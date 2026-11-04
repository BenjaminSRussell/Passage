package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// threeLineQuotation is a quotation of three lines priced 10 a unit, line n
// with quantity n (nets 10, 20, 30).
func threeLineQuotation(le, customer, key string) NewQuotation {
	sixty := 60
	site, wh := ids.New(), ids.New()
	in := NewQuotation{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, Currency: "USD",
		ValidUntil: time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC), ProbabilityPct: &sixty, IdempotencyKey: key, RequestHash: key + "-hash",
		Actor: "sales-rep"}
	for i := 1; i <= 3; i++ {
		in.Lines = append(in.Lines, Line{LineNo: i, ProductID: ids.New(), Quantity: d(int64(i)), Unit: "EA", SiteID: site,
			WarehouseID: wh, UnitPrice: d(10), DiscPct: d(0), LineNet: d(int64(10 * i)), RequestedShipDate: fmt.Sprintf("2027-03-%02d", 3+i)})
	}
	return in
}

// TestPostgres_QuotationConvertsSelectedLines covers AC-27.6: a sent 3-line
// quotation converts lines 1 and 3 into one 2-line order at the quoted prices,
// the quotation becomes CONVERTED, the converted lines name the order, and
// quotation.converted is written in the same transaction. The remaining line
// converts later into a second order; nothing converts twice.
func TestPostgres_QuotationConvertsSelectedLines(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	in := threeLineQuotation(le, customer, "quote-1")
	q, created, err := s.CreateQuotation(ctx, in)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if q.Status != domain.QuoteDraft || q.Version != 1 || !q.NetTotal.Equal(d(60)) || q.QuotationNumber != "SQ-0000001" ||
		q.ValidUntil != "2027-03-31" || q.ProbabilityPct != 60 || q.LineCount != 3 || q.OpenLines != 3 || q.CreatedBy != "sales-rep" {
		t.Fatalf("unexpected draft: %+v", q)
	}
	replay, created, err := s.CreateQuotation(ctx, in)
	if err != nil || created || replay.ID != q.ID {
		t.Fatalf("replay must return the stored quotation: created=%v err=%v", created, err)
	}
	reuse := in
	reuse.RequestHash = "another body"
	if _, _, err := s.CreateQuotation(ctx, reuse); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("key with another body must be refused: %v", err)
	}

	now := time.Date(2027, 3, 2, 9, 0, 0, 0, time.UTC)
	if _, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: q.Version, Actor: "sales-rep"}, now); problemCode(t, err) != CodeQuotationState {
		t.Fatalf("a DRAFT must not convert: %v", err)
	}
	sent, err := s.SendQuotation(ctx, q.ID, q.Version, now)
	if err != nil || sent.Status != domain.QuoteSent || sent.Version != 2 {
		t.Fatalf("send: %v %+v", err, sent)
	}
	if _, err := s.SendQuotation(ctx, q.ID, sent.Version, now); problemCode(t, err) != CodeQuotationState {
		t.Fatalf("a SENT quotation must not be sent again: %v", err)
	}
	if _, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: 1, Actor: "sales-rep"}, now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version must conflict: %v", err)
	}

	res, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: sent.Version, LineNos: []int{3, 1}, Actor: "sales-rep"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Quotation.Status != domain.QuoteConverted || res.Quotation.Version != 3 || res.Quotation.OpenLines != 1 {
		t.Fatalf("quotation after conversion: %+v", res.Quotation)
	}
	o := res.Order
	if len(o.Lines) != 2 || o.Status != domain.OrderDraft || o.Channel != quoteChannel || !o.NetTotal.Equal(d(40)) ||
		o.CustomerID != customer || o.Currency != "USD" || o.RequestedShipDate != "2027-03-04" {
		t.Fatalf("order: %+v", o)
	}
	for i, n := range []int{1, 3} {
		ql, ol := in.Lines[n-1], o.Lines[i]
		if ol.LineNo != i+1 || ol.ProductID != ql.ProductID || !ol.UnitPrice.Equal(ql.UnitPrice) || !ol.Quantity.Equal(ql.Quantity) ||
			!ol.LineNet.Equal(ql.LineNet) || ol.RequestedShipDate != ql.RequestedShipDate {
			t.Fatalf("order line %d = %+v, want quotation line %d %+v", i+1, ol, n, ql)
		}
	}
	for _, l := range res.Quotation.Lines {
		converted := l.LineNo != 2
		if converted != (l.LineStatus == domain.QuoteLineConverted) || converted != (l.ConvertedOrderID != nil && *l.ConvertedOrderID == o.ID) {
			t.Fatalf("quotation line %d status %s order %v", l.LineNo, l.LineStatus, l.ConvertedOrderID)
		}
	}
	var evt salesevents.QuotationConvertedData
	if err := json.Unmarshal(outboxData(t, pool, q.ID, salesevents.QuotationConverted), &evt); err != nil {
		t.Fatal(err)
	}
	if evt.OrderID != o.ID || evt.OrderNumber != o.OrderNumber || evt.QuotationNumber != q.QuotationNumber || evt.CustomerID != customer ||
		!reflect.DeepEqual(evt.Lines, []int{1, 3}) || evt.Version != 3 || evt.Actor != "sales-rep" {
		t.Fatalf("event %+v", evt)
	}

	if _, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: sent.Version, Actor: "sales-rep"}, now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("a retry with the old version must conflict: %v", err)
	}
	if _, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: 3, LineNos: []int{1}, Actor: "sales-rep"}, now); problemCode(t, err) != CodeLineSelection {
		t.Fatalf("a converted line must not convert again: %v", err)
	}
	if _, err := s.LoseQuotation(ctx, q.ID, 3, "lost the rest"); problemCode(t, err) != CodeQuotationState {
		t.Fatalf("a converted quotation must not be lost: %v", err)
	}
	rest, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: 3, Actor: "sales-rep"}, now)
	if err != nil || len(rest.Order.Lines) != 1 || rest.Order.Lines[0].ProductID != in.Lines[1].ProductID || rest.Quotation.OpenLines != 0 {
		t.Fatalf("convert the remaining line: %v %+v", err, rest)
	}
	if _, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: rest.Quotation.Version, Actor: "sales-rep"}, now); problemCode(t, err) != CodeLineSelection {
		t.Fatalf("nothing left to convert: %v", err)
	}
	if n := countWhere(t, pool, `select count(*) from sales_order where channel = 'QUOTE' and customer_id = $1`, customer); n != 2 {
		t.Fatalf("want 2 orders from the quotation, got %d", n)
	}
}

// TestPostgres_QuotationLoseExpiryAndList covers the LOST and validity rules
// of BR-27.5 and the quotation list filters.
func TestPostgres_QuotationLoseExpiryAndList(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer, other := ids.New(), ids.New(), ids.New()
	lost, _, err := s.CreateQuotation(ctx, threeLineQuotation(le, customer, "quote-lost"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoseQuotation(ctx, lost.ID, 9, "price"); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale lose must conflict: %v", err)
	}
	gone, err := s.LoseQuotation(ctx, lost.ID, lost.Version, "competitor 8% cheaper")
	if err != nil || gone.Status != domain.QuoteLost || gone.LostReason == nil || *gone.LostReason != "competitor 8% cheaper" {
		t.Fatalf("lose: %v %+v", err, gone)
	}
	in := time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)
	if _, err := s.SendQuotation(ctx, lost.ID, gone.Version, in); problemCode(t, err) != CodeQuotationState {
		t.Fatalf("a LOST quotation must not send: %v", err)
	}

	late := time.Date(2027, 4, 1, 9, 0, 0, 0, time.UTC)
	draft, _, err := s.CreateQuotation(ctx, threeLineQuotation(le, other, "quote-exp"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendQuotation(ctx, draft.ID, draft.Version, late); problemCode(t, err) != CodeQuotationExpired {
		t.Fatalf("an expired DRAFT must not send: %v", err)
	}
	sent, err := s.SendQuotation(ctx, draft.ID, draft.Version, time.Date(2027, 3, 31, 23, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("the last valid day still sends: %v", err)
	}
	if _, err := s.ConvertQuotation(ctx, draft.ID, ConvertRequest{Version: sent.Version, Actor: "sales-rep"}, late); problemCode(t, err) != CodeQuotationExpired {
		t.Fatalf("an expired quotation must not convert: %v", err)
	}
	if n := countWhere(t, pool, `select count(*) from sales_order where customer_id = $1`, other); n != 0 {
		t.Fatalf("a refused conversion left %d orders", n)
	}

	all, err := s.ListQuotations(ctx, QuotationFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(all) != 2 || all[0].ID != draft.ID || len(all[0].Lines) != 0 || all[0].LineCount != 3 {
		t.Fatalf("list newest first without lines: %v %+v", err, all)
	}
	byStatus, err := s.ListQuotations(ctx, QuotationFilter{Status: domain.QuoteLost, CustomerID: customer, Limit: 50})
	if err != nil || len(byStatus) != 1 || byStatus[0].ID != lost.ID {
		t.Fatalf("status and customer filter: %v %+v", err, byStatus)
	}
	if none, err := s.ListQuotations(ctx, QuotationFilter{LegalEntityID: ids.New(), Limit: 50}); err != nil || len(none) != 0 {
		t.Fatalf("another legal entity: %v %+v", err, none)
	}
	if _, err := s.GetQuotation(ctx, ids.New()); problemCode(t, err) != CodeQuotationNotFound {
		t.Fatalf("unknown quotation: %v", err)
	}
}

// TestPostgres_QuotationConversionChecksListings shows the conversion is one
// transaction: a line the product listing denies refuses the whole conversion
// and leaves the quotation as it was; the approve duty with a reason converts
// it and logs the override on the order (FR-27.15).
func TestPostgres_QuotationConversionChecksListings(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	in := threeLineQuotation(le, customer, "quote-listing")
	q, _, err := s.CreateQuotation(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 3, 2, 9, 0, 0, 0, time.UTC)
	sent, err := s.SendQuotation(ctx, q.ID, q.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateListing(ctx, ProductListing{LegalEntityID: le, ScopeType: domain.ScopeCustomer, ScopeKey: customer,
		ProductID: in.Lines[1].ProductID, ListingType: domain.ListingDeny, CreatedBy: "listing-admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: sent.Version, Actor: "sales-rep"}, now); problemCode(t, err) != CodeProductNotListed {
		t.Fatalf("a denied line must refuse the conversion: %v", err)
	}
	after, err := s.GetQuotation(ctx, q.ID)
	if err != nil || after.Status != domain.QuoteSent || after.Version != sent.Version || after.OpenLines != 3 {
		t.Fatalf("a refused conversion must change nothing: %v %+v", err, after)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1`, q.ID); n != 0 {
		t.Fatalf("a refused conversion wrote %d events", n)
	}
	res, err := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: sent.Version, Actor: "sales-manager", CanOverrideListing: true,
		ListingOverrideReason: "customer accepted the substitute"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Order.Lines) != 3 || len(res.Order.ListingOverrides) != 1 || res.Order.ListingOverrides[0].LineNo != 2 ||
		res.Order.ListingOverrides[0].Actor != "sales-manager" {
		t.Fatalf("override: %+v", res.Order)
	}
}

// TestPostgres_SupplementaryRules stores, lists, refuses a duplicate of and
// removes supplementary item rules, and evaluates AC-27.10 on stored rules.
func TestPostgres_SupplementaryRules(t *testing.T) {
	s, _, ctx := migrated(t)
	le, parent, manual := ids.New(), ids.New(), ids.New()
	rule := domain.SupplementaryRule{LegalEntityID: le, ParentProductID: parent, SupplementaryProduct: manual, Unit: "EA",
		Requirement: domain.SuppMandatory, BaseQuantity: d(10), QuantityPerBase: d(1), Pricing: domain.SuppFree, UnitPrice: d(0)}
	saved, err := s.CreateSupplementaryRule(ctx, rule, "product-admin")
	if err != nil || saved.ID == "" || saved.CustomerID != nil || !saved.BaseQuantity.Equal(d(10)) || saved.Unit != "EA" {
		t.Fatalf("create rule: %v %+v", err, saved)
	}
	if _, err := s.CreateSupplementaryRule(ctx, rule, "product-admin"); problemCode(t, err) != CodeSupplementaryDuplicate {
		t.Fatalf("a second all-customers rule for the same item must be refused: %v", err)
	}
	c9 := ids.New()
	mine := rule
	mine.CustomerID, mine.QuantityPerBase = &c9, d(2)
	if _, err := s.CreateSupplementaryRule(ctx, mine, "product-admin"); err != nil {
		t.Fatalf("a customer rule beside the all-customers rule: %v", err)
	}
	if _, err := s.CreateSupplementaryRule(ctx, mine, "product-admin"); problemCode(t, err) != CodeSupplementaryDuplicate {
		t.Fatalf("a second rule for the same customer must be refused: %v", err)
	}

	rules, err := s.ListSupplementaryRules(ctx, SupplementaryRuleFilter{LegalEntityID: le, ParentProductID: parent, Limit: 50})
	if err != nil || len(rules) != 2 || rules[0].ID != saved.ID {
		t.Fatalf("list: %v %+v", err, rules)
	}
	items, err := domain.EvaluateSupplementary(rules, parent, ids.New(), d(25))
	if err != nil || len(items) != 1 || !items[0].Quantity.Equal(d(2)) || !items[0].UnitPrice.IsZero() || items[0].ProductID != manual {
		t.Fatalf("AC-27.10 evaluation: %v %+v", err, items)
	}
	items, err = domain.EvaluateSupplementary(rules, parent, c9, d(25))
	if err != nil || len(items) != 1 || !items[0].Quantity.Equal(d(4)) {
		t.Fatalf("C9's own rule: %v %+v", err, items)
	}

	if err := s.DeleteSupplementaryRule(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSupplementaryRule(ctx, saved.ID); problemCode(t, err) != CodeSupplementaryNotFound {
		t.Fatalf("second delete: %v", err)
	}
	if rules, err := s.ListSupplementaryRules(ctx, SupplementaryRuleFilter{LegalEntityID: le, Limit: 50}); err != nil || len(rules) != 1 {
		t.Fatalf("after delete: %v %+v", err, rules)
	}
}

// TestPostgres_HoldQueueListsActiveCreditHolds covers the credit release
// queue: the CREDIT hold of the AC-27.3 flow is listed with the failed rule
// (exposure 11,500 against threshold 11,000) and the customer's limit; the
// type filter works and a released hold leaves the queue.
func TestPostgres_HoldQueueListsActiveCreditHolds(t *testing.T) {
	s, _, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	limits := domain.CreditProfile{Limit: d(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: d(5),
		ToleranceAmount: d(1000), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	if _, err := s.PutCreditProfile(ctx, CreditProfile{LegalEntityID: le, CustomerID: customer, CreditProfile: limits,
		Exposure: domain.Exposure{OpenAR: d(8500)}}, 0); err != nil {
		t.Fatal(err)
	}
	in := oneLineOrder(le, customer, ids.New(), time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	in.Lines[0].UnitPrice, in.Lines[0].Quantity, in.Lines[0].LineNet = d(3000), d(1), d(3000)
	order, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	held, err := s.ConfirmOrder(ctx, order.ID, order.Version, "sales-rep", now)
	if err != nil || held.Confirmed {
		t.Fatalf("confirm must place a CREDIT hold: %v", err)
	}
	credit, err := s.ListHolds(ctx, HoldQueueFilter{LegalEntityID: le, HoldType: domain.HoldCredit, Limit: 50})
	if err != nil || len(credit) != 1 {
		t.Fatalf("credit queue: %v %+v", err, credit)
	}
	it := credit[0]
	if it.OrderID != order.ID || it.OrderNumber != order.OrderNumber || it.OrderVersion != held.Order.Version || !it.Amount.Equal(d(3000)) ||
		it.ReasonCode != "CR-1" || it.CustomerID != customer || it.Currency != "USD" {
		t.Fatalf("queue row %+v", it)
	}
	if it.FailedRule == nil || it.FailedRule.ID != "CR-1" || it.FailedRule.Value != "11500" || it.FailedRule.Threshold != "11000" {
		t.Fatalf("failed rule %+v", it.FailedRule)
	}
	if it.CreditLimit == nil || !it.CreditLimit.Equal(d(10000)) || it.LimitCurrency == nil || *it.LimitCurrency != "USD" {
		t.Fatalf("limit %v %v", it.CreditLimit, it.LimitCurrency)
	}
	if fraud, err := s.ListHolds(ctx, HoldQueueFilter{LegalEntityID: le, HoldType: domain.HoldFraud, Limit: 50}); err != nil || len(fraud) != 0 {
		t.Fatalf("fraud filter must be empty: %v %+v", err, fraud)
	}
	if _, err := s.ReleaseHold(principalCtx("credit-manager"), order.ID, it.HoldID, it.OrderVersion, "credit-manager", now); err != nil {
		t.Fatal(err)
	}
	if after, err := s.ListHolds(ctx, HoldQueueFilter{Limit: 50, LegalEntityID: le}); err != nil || len(after) != 0 {
		t.Fatalf("a released hold must leave the queue: %v %+v", err, after)
	}
}

// TestPostgres_QuotationChangeAndCancel covers the quotation maintenance of
// FR-27.1 and BR-27.5: a DRAFT quotation takes a new line set, validity and
// probability; a SENT one takes only validity and probability; CANCELLED needs
// a reason and ends the quotation; each change writes its event with the
// version.
func TestPostgres_QuotationChangeAndCancel(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	in := threeLineQuotation(le, customer, "quote-change")
	q, _, err := s.CreateQuotation(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 3, 2, 9, 0, 0, 0, time.UTC)
	site, wh := ids.New(), ids.New()
	newLines := []Line{
		{LineNo: 1, ProductID: ids.New(), Quantity: d(5), Unit: "EA", SiteID: site, WarehouseID: wh, UnitPrice: d(8), DiscPct: d(0), LineNet: d(40), RequestedShipDate: "2027-03-20"},
		{LineNo: 2, ProductID: ids.New(), Quantity: d(1), Unit: "EA", SiteID: site, WarehouseID: wh, UnitPrice: d(7), DiscPct: d(0), LineNet: d(7), RequestedShipDate: "2027-03-21"},
	}
	if _, err := s.ChangeQuotation(ctx, q.ID, QuotationChange{Version: 9, Lines: newLines, Actor: "rep"}, now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale change must conflict: %v", err)
	}
	until, prob := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC), 85
	changed, err := s.ChangeQuotation(ctx, q.ID, QuotationChange{Version: q.Version, ValidUntil: &until, ProbabilityPct: &prob, Lines: newLines, Actor: "rep"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Version != 2 || changed.ValidUntil != "2027-04-15" || changed.ProbabilityPct != 85 || changed.LineCount != 2 ||
		!changed.NetTotal.Equal(d(47)) || changed.Lines[0].ProductID != newLines[0].ProductID || changed.Lines[1].LineNo != 2 {
		t.Fatalf("after change: %+v", changed)
	}
	var evt salesevents.QuotationChangedData
	if err := json.Unmarshal(outboxData(t, pool, q.ID, salesevents.QuotationChanged), &evt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evt.Changed, []string{"valid_until", "probability_pct", "lines"}) || evt.Version != 2 || evt.NetTotal != "47" ||
		evt.LineCount != 2 || evt.Actor != "rep" || evt.Status != domain.QuoteDraft || evt.ValidUntil != "2027-04-15" {
		t.Fatalf("event %+v", evt)
	}

	sent, err := s.SendQuotation(ctx, q.ID, changed.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeQuotation(ctx, q.ID, QuotationChange{Version: sent.Version, Lines: newLines, Actor: "rep"}, now); problemCode(t, err) != CodeQuotationState {
		t.Fatalf("a SENT quotation takes no new lines: %v", err)
	}
	later := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	ext, err := s.ChangeQuotation(ctx, q.ID, QuotationChange{Version: sent.Version, ValidUntil: &later, Actor: "rep"}, now)
	if err != nil || ext.ValidUntil != "2027-05-01" || ext.Status != domain.QuoteSent || ext.LineCount != 2 || !ext.NetTotal.Equal(d(47)) {
		t.Fatalf("extend a sent quotation: %v %+v", err, ext)
	}
	if _, err := s.ChangeQuotation(ctx, q.ID, QuotationChange{Version: ext.Version, ProbabilityPct: &prob, Actor: "rep"},
		time.Date(2027, 5, 2, 0, 0, 0, 0, time.UTC)); problemCode(t, err) != CodeQuotationExpired {
		t.Fatalf("a lapsed quotation cannot be changed: %v", err)
	}

	if _, err := s.CancelQuotation(ctx, q.ID, 99, "customer withdrew", "rep"); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale cancel must conflict: %v", err)
	}
	gone, err := s.CancelQuotation(ctx, q.ID, ext.Version, "customer withdrew", "rep")
	if err != nil || gone.Status != domain.QuoteCancelled || gone.CancelReason == nil || *gone.CancelReason != "customer withdrew" || gone.Version != ext.Version+1 {
		t.Fatalf("cancel: %v %+v", err, gone)
	}
	var cancelled salesevents.QuotationCancelledData
	if err := json.Unmarshal(outboxData(t, pool, q.ID, salesevents.QuotationCancelled), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.PreviousStatus != domain.QuoteSent || cancelled.Reason != "customer withdrew" || cancelled.Version != gone.Version ||
		cancelled.NetTotal != "47" || cancelled.Actor != "rep" || cancelled.LegalEntityID != le {
		t.Fatalf("event %+v", cancelled)
	}
	for name, err := range map[string]error{
		"cancel again": func() error { _, e := s.CancelQuotation(ctx, q.ID, gone.Version, "again", "rep"); return e }(),
		"send":         func() error { _, e := s.SendQuotation(ctx, q.ID, gone.Version, now); return e }(),
		"lose":         func() error { _, e := s.LoseQuotation(ctx, q.ID, gone.Version, "x"); return e }(),
		"change": func() error {
			_, e := s.ChangeQuotation(ctx, q.ID, QuotationChange{Version: gone.Version, ProbabilityPct: &prob}, now)
			return e
		}(),
		"convert": func() error {
			_, e := s.ConvertQuotation(ctx, q.ID, ConvertRequest{Version: gone.Version}, now)
			return e
		}(),
	} {
		if problemCode(t, err) != CodeQuotationState {
			t.Errorf("%s on a CANCELLED quotation: %v", name, err)
		}
	}

	// A converted quotation keeps its orders and cannot be cancelled.
	conv, _, err := s.CreateQuotation(ctx, threeLineQuotation(le, customer, "quote-change-2"))
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := s.SendQuotation(ctx, conv.ID, conv.Version, now)
	res, err := s.ConvertQuotation(ctx, conv.ID, ConvertRequest{Version: cs.Version, LineNos: []int{1}, Actor: "rep"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelQuotation(ctx, conv.ID, res.Quotation.Version, "too late", "rep"); problemCode(t, err) != CodeQuotationState {
		t.Fatalf("a converted quotation must not be cancelled: %v", err)
	}
}
