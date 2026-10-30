package domain

import (
	"reflect"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestScopeFromPrincipal_OnlyAllScopeDutyIsUnrestricted(t *testing.T) {
	usmf, demf := "00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b"
	allScope := map[string]struct{}{tenant.DutyAllScope: {}}
	tests := []struct {
		name string
		p    tenant.Principal
		want Scope
	}{
		{"no entity and no duty sees nothing", tenant.Principal{}, Scope{}},
		{"all-scope duty with no entity is unrestricted", tenant.Principal{Duties: allScope}, Scope{Unrestricted: true}},
		{"entity beats the duty", tenant.Principal{LegalEntityID: usmf, Duties: allScope}, Scope{LegalEntityIDs: []string{usmf}}},
		{"entity list", tenant.Principal{LegalEntityID: usmf + "," + demf}, Scope{LegalEntityIDs: []string{usmf, demf}}},
		{"star is not an entity", tenant.Principal{LegalEntityID: "*"}, Scope{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScopeFromPrincipal(tc.p); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCompileScope_Table(t *testing.T) {
	usmf, demf := "00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b"
	tests := []struct {
		name       string
		model      string
		scope      Scope
		wantFilter []Filter
		wantReason string
		wantCode   string
	}{
		{"unrestricted adds no filter", "fin_journal_line", Scope{Unrestricted: true}, nil, "", ""},
		{"one entity filters by its id", "fin_journal_line", ScopeFromPrincipal(tenant.Principal{LegalEntityID: usmf}),
			[]Filter{{Member: "fin_journal_line.legal_entity_id", Operator: "equals", Values: []string{usmf}}}, "", ""},
		{"two entities sorted and deduplicated", "sales_line", Scope{LegalEntityIDs: []string{demf, usmf, demf}},
			[]Filter{{Member: "sales_line.legal_entity_id", Operator: "equals", Values: []string{usmf, demf}}}, "", ""},
		{"empty scope is NO_SCOPE with no filter", "fin_journal_line", Scope{}, nil, ReasonNoScope, ""},
		{"unmapped model fails closed", "payroll_line", Scope{Unrestricted: true}, nil, "", CodeUnmapped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CompileScope(tc.model, tc.scope)
			if tc.wantCode != "" {
				if err == nil || err.Code != tc.wantCode || err.Status != 403 {
					t.Fatalf("err = %+v, want %s 403", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if !reflect.DeepEqual(got.Filters, tc.wantFilter) || got.Reason != tc.wantReason {
				t.Fatalf("got %+v, want filters %+v reason %q", got, tc.wantFilter, tc.wantReason)
			}
		})
	}
}

func TestCompileScope_FilterStaysWithinGrant(t *testing.T) {
	ids := []string{"le-a", "le-b", "le-c", "le-d"}
	rapid.Check(t, func(t *rapid.T) {
		picked := rapid.SliceOf(rapid.SampledFrom(ids)).Draw(t, "picked")
		scope := Scope{LegalEntityIDs: picked}
		got, err := CompileScope("fin_journal_line", scope)
		if err != nil {
			t.Fatal(err)
		}
		if len(picked) == 0 {
			if got.Reason != ReasonNoScope || len(got.Filters) != 0 {
				t.Fatalf("empty scope must be NO_SCOPE, got %+v", got)
			}
			return
		}
		want := slices.Compact(slices.Sorted(slices.Values(picked)))
		if !slices.Equal(got.Filters[0].Values, want) {
			t.Fatalf("filter values %v, want %v", got.Filters[0].Values, want)
		}
		if !ScopeWithinGrant(got.Filters, scope) {
			t.Fatal("filter grants more than the scope")
		}
	})
}
