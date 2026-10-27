package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func pairing() Partner {
	p := activePartner()
	p.CustomerAccount, p.VendorAccount = "IC-B", "IC-A"
	return p
}

func TestValidatePairing_Table(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Partner)
		want string // detail fragment; "" is valid
	}{
		{"valid", func(*Partner) {}, ""},
		{"missing customer account", func(p *Partner) { p.CustomerAccount = " " }, "customer account is required"},
		{"missing vendor account", func(p *Partner) { p.VendorAccount = "" }, "vendor account is required"},
		{"customer account too long", func(p *Partner) { p.CustomerAccount = strings.Repeat("C", 65) }, "at most 64"},
		{"entity code too long", func(p *Partner) { p.SellerEntity = strings.Repeat("A", 65) }, "at most 64"},
		{"same entity both sides", func(p *Partner) { p.BuyerEntity = p.SellerEntity }, "different entities"},
		{"unknown pricing", func(p *Partner) { p.Pricing = "Guess" }, "unknown pricing policy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := pairing()
			c.edit(&p)
			err := ValidatePairing(p)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeInvalid || !strings.Contains(de.Detail, c.want) {
				t.Fatalf("err = %v, want %s containing %q", err, CodeInvalid, c.want)
			}
		})
	}
}

func TestPartnerPatch_Apply(t *testing.T) {
	inactive, costPlus := false, PriceCostPlus
	markup, pct, days := lpDec("10"), lpDec("1"), 3
	same := "IC-B"
	cases := []struct {
		name    string
		patch   PartnerPatch
		changed []string
		check   func(Partner) bool
	}{
		{"empty patch changes nothing", PartnerPatch{}, nil, func(p Partner) bool { return reflect.DeepEqual(p, pairing()) }},
		{"same value is no change", PartnerPatch{CustomerAccount: &same, Active: ptr(true), MarkupPct: ptr(lpDec("0"))}, nil,
			func(p Partner) bool { return p.CustomerAccount == "IC-B" }},
		{"deactivate", PartnerPatch{Active: &inactive}, []string{"active"}, func(p Partner) bool { return !p.Active }},
		{"pricing and markup", PartnerPatch{Pricing: &costPlus, MarkupPct: &markup}, []string{"pricing", "markupPct"},
			func(p Partner) bool { return p.Pricing == PriceCostPlus && p.MarkupPct.Equal(markup) }},
		{"either tolerance field reports tolerance once", PartnerPatch{TolerancePricePct: &pct, ToleranceDateDays: &days}, []string{"tolerance"},
			func(p Partner) bool { return p.Tolerance.PricePct.Equal(pct) && p.Tolerance.DateDays == 3 }},
		{"actions", PartnerPatch{Actions: &ActionPolicy{AutoCreate: true, AutoConfirm: true}}, []string{"actions"},
			func(p Partner) bool { return p.Actions.AutoConfirm }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed := c.patch.Apply(pairing())
			if !reflect.DeepEqual(changed, c.changed) {
				t.Fatalf("changed = %v, want %v", changed, c.changed)
			}
			if !c.check(got) {
				t.Fatalf("patched pairing = %+v", got)
			}
			if got.SellerEntity != "A" || got.BuyerEntity != "B" {
				t.Fatalf("patch moved the direction: %s -> %s", got.SellerEntity, got.BuyerEntity)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
