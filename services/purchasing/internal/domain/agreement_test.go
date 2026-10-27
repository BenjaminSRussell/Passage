package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func day(s string) Day {
	t, err := ParseDay(s)
	if err != nil {
		panic(err)
	}
	return t
}

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestAgreementLine_Draw(t *testing.T) {
	cases := []struct {
		name        string
		line        AgreementLine
		draw        Draw
		wantCode    string
		wantOverrun bool
		wantCross   bool
		wantQty     string
		wantAmount  string
	}{
		{
			name:     "AC-17.7 HARD quantity overrun is refused and leaves the line as it was",
			line:     AgreementLine{LineNo: 1, CommitmentType: CommitQuantity, CommittedQty: d("1000"), ReleasedQty: d("950"), ReleasedAmount: d("9500"), MaxEnforced: EnforceHard},
			draw:     Draw{Qty: d("100"), Amount: d("1000")},
			wantCode: CodeAgreementLimit,
			wantQty:  "950", wantAmount: "9500",
		},
		{
			name:    "HARD quantity exactly at the commitment is accepted",
			line:    AgreementLine{LineNo: 1, CommitmentType: CommitQuantity, CommittedQty: d("1000"), ReleasedQty: d("900"), MaxEnforced: EnforceHard},
			draw:    Draw{Qty: d("100"), Amount: d("1000")},
			wantQty: "1000", wantAmount: "1000",
		},
		{
			name:      "threshold fires on the draw that reaches 90 %",
			line:      AgreementLine{LineNo: 5, CommitmentType: CommitQuantity, CommittedQty: d("1000"), ReleasedQty: d("880"), MaxEnforced: EnforceHard},
			draw:      Draw{Qty: d("20"), Amount: d("200")},
			wantCross: true,
			wantQty:   "900", wantAmount: "200",
		},
		{
			name:    "threshold does not fire again once above it",
			line:    AgreementLine{LineNo: 5, CommitmentType: CommitQuantity, CommittedQty: d("1000"), ReleasedQty: d("910"), MaxEnforced: EnforceHard},
			draw:    Draw{Qty: d("10"), Amount: d("100")},
			wantQty: "920", wantAmount: "100",
		},
		{
			name:        "SOFT overrun applies and reports the overrun (BR-17.10)",
			line:        AgreementLine{LineNo: 2, CommitmentType: CommitQuantity, CommittedQty: d("100"), ReleasedQty: d("95"), MaxEnforced: EnforceSoft},
			draw:        Draw{Qty: d("20"), Amount: d("20")},
			wantOverrun: true,
			wantQty:     "115", wantAmount: "20",
		},
		{
			name:    "NONE is unlimited and never reports an overrun",
			line:    AgreementLine{LineNo: 3, CommitmentType: CommitValue, CommittedAmount: d("500"), ReleasedAmount: d("500"), MaxEnforced: EnforceNone},
			draw:    Draw{Qty: d("1"), Amount: d("50")},
			wantQty: "1", wantAmount: "550",
		},
		{
			name:     "TOTAL_VALUE measures the amount, not the quantity",
			line:     AgreementLine{LineNo: 4, CommitmentType: CommitTotalValue, CommittedAmount: d("1000"), ReleasedAmount: d("990"), MaxEnforced: EnforceHard},
			draw:     Draw{Qty: d("1"), Amount: d("10.01")},
			wantCode: CodeAgreementLimit,
			wantQty:  "0", wantAmount: "990",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := tc.line
			res, err := l.Draw(tc.draw, DefaultThresholdPct)
			if got := codeOf(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (err %v)", got, tc.wantCode, err)
			}
			if res.Overrun != tc.wantOverrun || res.ThresholdCrossed != tc.wantCross {
				t.Fatalf("overrun/crossed = %v/%v, want %v/%v", res.Overrun, res.ThresholdCrossed, tc.wantOverrun, tc.wantCross)
			}
			if !l.ReleasedQty.Equal(d(tc.wantQty)) || !l.ReleasedAmount.Equal(d(tc.wantAmount)) {
				t.Fatalf("released = %s / %s, want %s / %s", l.ReleasedQty, l.ReleasedAmount, tc.wantQty, tc.wantAmount)
			}
		})
	}
}

// A HARD line never ends above its commitment however the draws arrive, and
// every refusal leaves it unchanged; the threshold fires at most once.
func TestAgreementLine_DrawPropertyHardNeverExceeds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		typ := rapid.SampledFrom([]string{CommitQuantity, CommitValue}).Draw(t, "type")
		committed := decimal.NewFromInt(rapid.Int64Range(1, 10_000).Draw(t, "committed"))
		l := AgreementLine{LineNo: 1, CommitmentType: typ, MaxEnforced: EnforceHard, CommittedQty: committed, CommittedAmount: committed}
		crossings := 0
		for i, n := 0, rapid.IntRange(1, 40).Draw(t, "draws"); i < n; i++ {
			v := decimal.NewFromInt(rapid.Int64Range(1, 500).Draw(t, "v"))
			before := l
			res, err := l.Draw(Draw{Qty: v, Amount: v}, DefaultThresholdPct)
			if err != nil && (codeOf(err) != CodeAgreementLimit || !l.ReleasedQty.Equal(before.ReleasedQty) || !l.ReleasedAmount.Equal(before.ReleasedAmount)) {
				t.Fatalf("refusal %v changed the line or used another code", err)
			}
			if res.ThresholdCrossed {
				crossings++
			}
			if c, r := l.Measure(); r.GreaterThan(c) {
				t.Fatalf("released %s exceeds committed %s", r, c)
			}
		}
		if crossings > 1 {
			t.Fatalf("threshold fired %d times", crossings)
		}
	})
}

func TestAgreement_CheckRelease(t *testing.T) {
	to := day("2027-06-30")
	a := Agreement{Number: "PA-1", LegalEntityID: "le", VendorID: "v1", Currency: "USD", ValidFrom: day("2027-01-01"), ValidTo: &to, Status: AgreementEffective}
	qty := AgreementLine{LineNo: 1, CommitmentType: CommitQuantity, ProductID: "p1", Unit: "EA"}
	value := AgreementLine{LineNo: 2, CommitmentType: CommitValue, ProductID: "p1"}
	total := AgreementLine{LineNo: 3, CommitmentType: CommitTotalValue}
	ok := Release{LegalEntityID: "le", VendorID: "v1", Currency: "USD", ProductID: "p1", Unit: "EA", DeliveryDate: day("2027-03-01")}
	cases := []struct {
		name   string
		status string
		line   AgreementLine
		change func(*Release)
		want   string
	}{
		{"inside validity", AgreementEffective, qty, func(*Release) {}, ""},
		{"first day", AgreementEffective, qty, func(r *Release) { r.DeliveryDate = day("2027-01-01") }, ""},
		{"last day", AgreementEffective, qty, func(r *Release) { r.DeliveryDate = day("2027-06-30") }, ""},
		{"after validity BR-17.10", AgreementEffective, qty, func(r *Release) { r.DeliveryDate = day("2027-07-01") }, CodeAgreementWindow},
		{"before validity", AgreementEffective, qty, func(r *Release) { r.DeliveryDate = day("2026-12-31") }, CodeAgreementWindow},
		{"other vendor", AgreementEffective, qty, func(r *Release) { r.VendorID = "v2" }, CodeAgreementParty},
		{"other currency", AgreementEffective, qty, func(r *Release) { r.Currency = "EUR" }, CodeAgreementParty},
		{"other legal entity", AgreementEffective, qty, func(r *Release) { r.LegalEntityID = "le2" }, CodeAgreementParty},
		{"draft", AgreementDraft, qty, func(*Release) {}, CodeAgreementInactive},
		{"on hold", AgreementOnHold, qty, func(*Release) {}, CodeAgreementInactive},
		{"closed", AgreementClosed, qty, func(*Release) {}, CodeAgreementInactive},
		{"quantity line, other product", AgreementEffective, qty, func(r *Release) { r.ProductID = "p2" }, CodeAgreementItem},
		{"quantity line, other unit", AgreementEffective, qty, func(r *Release) { r.Unit = "BOX" }, CodeAgreementItem},
		{"value line, other unit is fine", AgreementEffective, value, func(r *Release) { r.Unit = "BOX" }, ""},
		{"value line, other product", AgreementEffective, value, func(r *Release) { r.ProductID = "" }, CodeAgreementItem},
		{"total value takes any line", AgreementEffective, total, func(r *Release) { r.ProductID, r.Unit = "", "HR" }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := a
			x.Status = tc.status
			r := ok
			tc.change(&r)
			if got := codeOf(x.CheckRelease(tc.line, r)); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}
	open := a
	open.ValidTo = nil
	if !open.Covers(day("2099-01-01")) {
		t.Fatal("an open-ended agreement must cover any later date")
	}
}

func TestNextStatus(t *testing.T) {
	cases := []struct {
		from, action, want string
	}{
		{AgreementDraft, AgreementActivate, AgreementEffective},
		{AgreementOnHold, AgreementActivate, AgreementEffective},
		{AgreementEffective, AgreementHold, AgreementOnHold},
		{AgreementEffective, AgreementClose, AgreementClosed},
		{AgreementOnHold, AgreementClose, AgreementClosed},
		{AgreementEffective, AgreementActivate, ""},
		{AgreementDraft, AgreementHold, ""},
		{AgreementDraft, AgreementClose, ""},
		{AgreementClosed, AgreementActivate, ""},
	}
	for _, c := range cases {
		got, err := NextStatus(c.from, c.action)
		if got != c.want || (c.want == "") != (codeOf(err) == CodeAgreementState) {
			t.Errorf("%s + %s = %q, %v; want %q", c.from, c.action, got, err, c.want)
		}
	}
}

func TestPctAndRemaining(t *testing.T) {
	cases := []struct{ used, committed, want string }{
		{"0", "100", "0"},
		{"90", "100", "90"},
		{"1", "3", "33.33"},
		{"2", "3", "66.67"},
		{"0", "0", "0"},
		{"5", "0", "100"},
	}
	for _, c := range cases {
		if got := Pct(d(c.used), d(c.committed)); !got.Equal(d(c.want)) {
			t.Errorf("Pct(%s,%s) = %s, want %s", c.used, c.committed, got, c.want)
		}
	}
	l := AgreementLine{CommitmentType: CommitQuantity, CommittedQty: d("1000"), ReleasedQty: d("950"), CommittedAmount: d("1"), ReleasedAmount: d("0")}
	if !l.PctReleased().Equal(d("95")) || !l.Remaining().Equal(d("50")) {
		t.Fatalf("quantity line pct/remaining = %s/%s, want 95/50", l.PctReleased(), l.Remaining())
	}
	l.CommitmentType = CommitValue
	if !l.Remaining().Equal(d("1")) {
		t.Fatalf("value line remaining = %s, want the amount measure 1", l.Remaining())
	}
}

func TestValidateAgreementLine(t *testing.T) {
	cases := []struct {
		name  string
		line  AgreementLine
		field string
	}{
		{"quantity ok", AgreementLine{CommitmentType: CommitQuantity, ProductID: "p", Unit: "EA", CommittedQty: d("1")}, ""},
		{"quantity without product", AgreementLine{CommitmentType: CommitQuantity, Unit: "EA", CommittedQty: d("1")}, "productId"},
		{"quantity without unit", AgreementLine{CommitmentType: CommitQuantity, ProductID: "p", CommittedQty: d("1")}, "unit"},
		{"quantity zero", AgreementLine{CommitmentType: CommitQuantity, ProductID: "p", Unit: "EA"}, "committedQty"},
		{"value ok", AgreementLine{CommitmentType: CommitValue, ProductID: "p", CommittedAmount: d("10")}, ""},
		{"value without amount", AgreementLine{CommitmentType: CommitValue, ProductID: "p"}, "committedAmount"},
		{"category value ok", AgreementLine{CommitmentType: CommitCategoryValue, Category: "IT", CommittedAmount: d("10")}, ""},
		{"category value without category", AgreementLine{CommitmentType: CommitCategoryValue, CommittedAmount: d("10")}, "category"},
		{"total value ok", AgreementLine{CommitmentType: CommitTotalValue, CommittedAmount: d("10")}, ""},
		{"total value zero", AgreementLine{CommitmentType: CommitTotalValue}, "committedAmount"},
		{"unknown type", AgreementLine{CommitmentType: "X"}, "commitmentType"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if field, msg := ValidateAgreementLine(tc.line); field != tc.field {
				t.Fatalf("field = %q (%s), want %q", field, msg, tc.field)
			}
		})
	}
}
