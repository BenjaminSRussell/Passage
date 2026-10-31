package domain_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func rejectDoc(conds string) string {
	return fmt.Sprintf(`{"entity":"cust","version":1,"fields":[{"source":"AccountNum","target":"id","type":"string"},{"source":"CustGroup","target":"group","type":"string"},{"source":"Blocked","target":"blocked","type":"bool","fn":"ax_noyes"}],"rejectWhen":[%s]}`, conds)
}

func TestRejectWhen_EachOpRejectsAMatchingRowAndPassesTheRest(t *testing.T) {
	cases := []struct {
		name, cond string
		row        map[string]any
		want       []string
	}{
		{"empty hits absent target", `{"field":"group","op":"empty","code":"CUST-NO-GROUP"}`, map[string]any{"AccountNum": "1", "CustGroup": "  "}, []string{"CUST-NO-GROUP"}},
		{"empty misses value", `{"field":"group","op":"empty","code":"CUST-NO-GROUP"}`, map[string]any{"AccountNum": "1", "CustGroup": "A"}, nil},
		{"eq hits mapped value", `{"field":"blocked","op":"eq","value":"true","code":"CUST-BLOCKED"}`, map[string]any{"AccountNum": "1", "Blocked": "Yes"}, []string{"CUST-BLOCKED"}},
		{"eq compares mapped not source text", `{"field":"blocked","op":"eq","value":"Yes","code":"CUST-BLOCKED"}`, map[string]any{"AccountNum": "1", "Blocked": "Yes"}, nil},
		{"eq misses absent", `{"field":"group","op":"eq","value":"BLK","code":"CUST-BLOCKED"}`, map[string]any{"AccountNum": "1"}, nil},
		{"ne hits other value", `{"field":"group","op":"ne","value":"A","code":"CUST-NOT-A"}`, map[string]any{"AccountNum": "1", "CustGroup": "B"}, []string{"CUST-NOT-A"}},
		{"ne hits absent", `{"field":"group","op":"ne","value":"A","code":"CUST-NOT-A"}`, map[string]any{"AccountNum": "1"}, []string{"CUST-NOT-A"}},
		{"ne misses equal", `{"field":"group","op":"ne","value":"A","code":"CUST-NOT-A"}`, map[string]any{"AccountNum": "1", "CustGroup": "A"}, nil},
		{"in hits", `{"field":"group","op":"in","values":["X","Y"],"code":"CUST-TEST-GROUP"}`, map[string]any{"AccountNum": "1", "CustGroup": "Y"}, []string{"CUST-TEST-GROUP"}},
		{"in misses unlisted", `{"field":"group","op":"in","values":["X","Y"],"code":"CUST-TEST-GROUP"}`, map[string]any{"AccountNum": "1", "CustGroup": "Z"}, nil},
		{"notIn hits", `{"field":"group","op":"notIn","values":["A","B"],"code":"CUST-BAD-GROUP"}`, map[string]any{"AccountNum": "1", "CustGroup": "Z"}, []string{"CUST-BAD-GROUP"}},
		{"notIn misses listed", `{"field":"group","op":"notIn","values":["A","B"],"code":"CUST-BAD-GROUP"}`, map[string]any{"AccountNum": "1", "CustGroup": "B"}, nil},
		{"match hits", `{"field":"id","op":"match","value":"^TMP-","code":"CUST-TEMP"}`, map[string]any{"AccountNum": "TMP-9"}, []string{"CUST-TEMP"}},
		{"match misses", `{"field":"id","op":"match","value":"^TMP-","code":"CUST-TEMP"}`, map[string]any{"AccountNum": "C-9"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := parseMapping(t, rejectDoc(tc.cond))
			out, rejects := m.Apply(tc.row)
			var got []string
			for _, r := range rejects {
				got = append(got, r.Code)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			if (out == nil) != (len(tc.want) > 0) {
				t.Fatalf("a rejected row has no output and a passed row has output: %v", out)
			}
		})
	}
}

func TestRejectWhen_RejectNamesTheTargetAndOpButNotTheValue(t *testing.T) {
	m := parseMapping(t, rejectDoc(`{"field":"group","op":"in","values":["SECRET-GRP"],"code":"CUST-TEST-GROUP"},{"field":"id","op":"match","value":"^TMP","code":"CUST-TEMP"}`))
	_, rejects := m.Apply(map[string]any{"AccountNum": "TMP-1", "CustGroup": "SECRET-GRP"})
	want := []domain.Reject{{Field: "group", Code: "CUST-TEST-GROUP", Detail: "matched rejectWhen in"}, {Field: "id", Code: "CUST-TEMP", Detail: "matched rejectWhen match"}}
	if !slices.Equal(rejects, want) {
		t.Fatalf("got %+v want %+v", rejects, want)
	}
}

func TestRejectWhen_FieldRejectsWinOverConditions(t *testing.T) {
	m := parseMapping(t, rejectDoc(`{"field":"group","op":"empty","code":"CUST-NO-GROUP"}`))
	_, rejects := m.Apply(map[string]any{"AccountNum": "1", "Blocked": "2"})
	if len(rejects) != 1 || rejects[0].Code != domain.CodeMapCoerce {
		t.Fatalf("conditions run only after every field rule succeeds: %+v", rejects)
	}
}

func TestRejectWhen_ExpectRejectsTestsAssertTheReasonCode(t *testing.T) {
	doc := `{"entity":"cust","version":1,"fields":[{"source":"id","target":"id","type":"string"}],
	"rejectWhen":[{"field":"id","op":"match","value":"^TMP","code":"CUST-TEMP"}],
	"tests":[{"name":"temp","input":{"id":"TMP-1"},"expectRejects":["CUST-TEMP"]},{"name":"ok","input":{"id":"C-1"},"expect":{"id":"C-1"}},{"name":"wrong","input":{"id":"C-2"},"expectRejects":["CUST-TEMP"]}]}`
	m := parseMapping(t, doc)
	f := m.RunTests()
	if len(f) != 1 || f[0].Name != "wrong" {
		t.Fatalf("failures: %+v", f)
	}
}

func TestRejectWhen_InvalidConditionsAreRejectedAtParse(t *testing.T) {
	cases := []struct{ cond, code string }{
		{`{"field":"id","op":"like","value":"x","code":"A-B"}`, domain.CodeMapUnknownFn},
		{`{"field":"id","op":"match","value":"(","code":"A-B"}`, domain.CodeMapInvalid},
		{`{"field":"CustGroup","op":"empty","code":"A-B"}`, domain.CodeMapInvalid},
		{`{"op":"empty","code":"A-B"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"empty","code":"bad code"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"empty","code":"NOHYPHEN"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"empty","code":"MIG-MAP-X"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"empty","code":"A-` + strings.Repeat("B", 63) + `"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"empty","value":"x","code":"A-B"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"eq","code":"A-B"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"in","code":"A-B"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"in","value":"x","values":["y"],"code":"A-B"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"match","value":"` + strings.Repeat("a", 201) + `","code":"A-B"}`, domain.CodeMapInvalid},
		{`{"field":"id","op":"empty","code":"A-B","detail":"x"}`, domain.CodeMapInvalid},
	}
	for _, c := range cases {
		if got := parseCode(rejectDoc(c.cond)); got != c.code {
			t.Errorf("%s: got %q want %q", c.cond, got, c.code)
		}
	}
	many := strings.TrimSuffix(strings.Repeat(`{"field":"id","op":"empty","code":"A-B"},`, 51), ",")
	if got := parseCode(rejectDoc(many)); got != domain.CodeMapInvalid {
		t.Errorf("51 conditions: got %q", got)
	}
}
