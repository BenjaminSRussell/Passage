package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const naturalKeyMapping = `{"entity":"customer","version":1,"naturalKey":%s,"fields":[
	{"source":"AccountNum","target":"account","type":"string"},
	{"source":"DataAreaId","target":"company","type":"string"},
	{"source":"Name","target":"name","type":"string"}],
	"tests":[{"name":"t","input":{"AccountNum":"C1","DataAreaId":"usmf","Name":"Adatum"},"expect":{"account":"C1","company":"usmf","name":"Adatum"}}]}`

func parseKey(t *testing.T, key string) (domain.Mapping, error) {
	t.Helper()
	return domain.ParseMapping([]byte(strings.Replace(naturalKeyMapping, "%s", key, 1)))
}

func TestParseMapping_NaturalKeyMustBeProducedByRules(t *testing.T) {
	cases := []struct{ name, key, want string }{
		{"one column", `["account"]`, ""},
		{"composite", `["company","account"]`, ""},
		{"absent", `[]`, ""},
		{"unknown column", `["customer_no"]`, "not produced by any field rule"},
		{"source name is not a mapped column", `["AccountNum"]`, "not produced by any field rule"},
		{"repeated", `["account","account"]`, "twice"},
		{"blank", `[""]`, "empty column name"},
		{"too many", `["a","b","c","d","e","f","g","h","i"]`, "at most 8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseKey(t, c.key)
			if c.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeMapInvalid || !strings.Contains(de.Detail, c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestApply_BlankNaturalKeyRejectsTheRow(t *testing.T) {
	m, err := parseKey(t, `["company","account"]`)
	if err != nil {
		t.Fatal(err)
	}
	if out, rejects := m.Apply(map[string]any{"AccountNum": "C1", "DataAreaId": "usmf", "Name": "Adatum"}); len(rejects) != 0 || out["account"] != "C1" {
		t.Fatalf("complete key: %v %v", out, rejects)
	}
	for name, row := range map[string]map[string]any{
		"missing column": {"AccountNum": "C1", "Name": "Adatum"},
		"blank column":   {"AccountNum": "   ", "DataAreaId": "usmf", "Name": "Adatum"},
	} {
		t.Run(name, func(t *testing.T) {
			out, rejects := m.Apply(row)
			if out != nil || len(rejects) != 1 || rejects[0].Code != domain.CodeMapKey {
				t.Fatalf("out=%v rejects=%v", out, rejects)
			}
		})
	}
	// A column that already failed its own rule is reported once, by that rule.
	if _, rejects := m.Apply(map[string]any{"AccountNum": "C1", "DataAreaId": map[string]any{"x": 1}}); len(rejects) != 1 || rejects[0].Code == domain.CodeMapKey {
		t.Fatalf("rejects = %v", rejects)
	}
}

func TestScheduleCronProblemMin(t *testing.T) {
	for _, c := range []struct {
		expr string
		min  int
		want string
	}{
		{"", 60, ""},
		{"*/15 * * * *", 15, ""},
		{"*/15 * * * *", 5, ""}, // below the floor reads as the 15-minute floor
		{"*/15 * * * *", 60, "must leave at least 60 minutes between runs"},
		{"0 * * * *", 60, ""},
		{"0 */2 * * *", 120, ""},
		{"0 */2 * * *", 180, "must leave at least 180 minutes between runs"},
		{"0 3 * * *", domain.MaxRecurringMinIntervalMin, ""},
		{"0 3,4 * * *", domain.MaxRecurringMinIntervalMin, "must leave at least 1440 minutes between runs"},
		{"not cron", 60, "five-field cron"},
	} {
		got := domain.ScheduleCronProblemMin(c.expr, c.min)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%q min %d: %q, want %q", c.expr, c.min, got, c.want)
		}
	}
}

func TestRecurringMinIntervalProblem(t *testing.T) {
	for n, ok := range map[int]bool{14: false, 15: true, 60: true, 1440: true, 1441: false, 0: false, -5: false} {
		if got := domain.RecurringMinIntervalProblem(n); (got == "") != ok {
			t.Errorf("%d: %q", n, got)
		}
	}
}
