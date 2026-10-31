package domain_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

func templateMapping(t testing.TB) domain.Mapping {
	return parseMapping(t, fieldDoc("", strings.Join([]string{
		`{"source":"Name","target":"name","type":"string","required":true}`,
		`{"source":"Terms","target":"paymentTerms","type":"string"}`,
		`{"source":"Limit","target":"creditLimit","type":"decimal","default":"100"}`,
		`{"rule":"const","value":"customer","target":"kind","type":"string"}`,
	}, ",")))
}

func mustTemplate(t testing.TB, doc string) domain.DataTemplate {
	tmpl, err := domain.ParseTemplate([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func TestTemplate_FillsAbsentFieldsBeforeRuleDefaultAndSourceWins(t *testing.T) {
	m := templateMapping(t)
	tmpl := mustTemplate(t, `{"name":"EU defaults","entity":"e","version":1,"defaults":{"paymentTerms":"NET30","creditLimit":"500"}}`)
	if err := m.CheckTemplate(tmpl); err != nil {
		t.Fatal(err)
	}
	out, _, rejects, from := m.ApplyTemplate(map[string]any{"Name": "Acme", "Limit": "250"}, &tmpl)
	if len(rejects) != 0 || out["paymentTerms"] != "NET30" || out["creditLimit"] != "250" {
		t.Fatalf("out=%v rejects=%v", out, rejects)
	}
	if len(from) != 1 || from[0] != "paymentTerms" {
		t.Fatalf("template fields = %v", from)
	}
	out, _, _, from = m.ApplyTemplate(map[string]any{"Name": "Beta"}, &tmpl)
	if out["creditLimit"] != "500" || len(from) != 2 {
		t.Fatalf("template must beat the rule default: out=%v from=%v", out, from)
	}
	out, _, _, from = m.ApplyTemplate(map[string]any{"Name": "Gamma"}, nil)
	if out["creditLimit"] != "100" || out["paymentTerms"] != nil || from != nil {
		t.Fatalf("no template keeps rule defaults: out=%v", out)
	}
}

func TestTemplate_ParseTrimsNameAndEntitySoTheEntityMatchesTheMapping(t *testing.T) {
	tmpl := mustTemplate(t, `{"name":"  EU  ","entity":" e ","version":1,"defaults":{"paymentTerms":"NET30"}}`)
	if tmpl.Name != "EU" || tmpl.Entity != "e" {
		t.Fatalf("name=%q entity=%q", tmpl.Name, tmpl.Entity)
	}
	if err := templateMapping(t).CheckTemplate(tmpl); err != nil {
		t.Fatal(err)
	}
}

func TestTemplate_SatisfiesRequiredField(t *testing.T) {
	m := templateMapping(t)
	tmpl := mustTemplate(t, `{"name":"n","entity":"e","version":1,"defaults":{"name":"Unknown customer"}}`)
	out, _, rejects, from := m.ApplyTemplate(map[string]any{"Name": ""}, &tmpl)
	if len(rejects) != 0 || out["name"] != "Unknown customer" || len(from) != 1 {
		t.Fatalf("out=%v rejects=%v from=%v", out, rejects, from)
	}
}

func TestTemplate_ParseAndCheckRejectBadTemplates(t *testing.T) {
	for _, doc := range []string{
		`{`, `{"name":"","entity":"e","version":1,"defaults":{"a":"b"}}`,
		`{"name":"n","entity":"","version":1,"defaults":{"a":"b"}}`,
		`{"name":"n","entity":"e","version":0,"defaults":{"a":"b"}}`,
		`{"name":"n","entity":"e","version":1,"defaults":{}}`,
		`{"name":"n","entity":"e","version":1,"defaults":{"a":"b"},"extra":1}`,
	} {
		var de *domain.Error
		if _, err := domain.ParseTemplate([]byte(doc)); !errors.As(err, &de) || de.Code != domain.CodeTemplateInvalid {
			t.Errorf("%s: err = %v", doc, err)
		}
	}
	m := templateMapping(t)
	for name, doc := range map[string]string{
		"other entity":  `{"name":"n","entity":"vendor","version":1,"defaults":{"paymentTerms":"x"}}`,
		"unknown field": `{"name":"n","entity":"e","version":1,"defaults":{"nope":"x"}}`,
		"const target":  `{"name":"n","entity":"e","version":1,"defaults":{"kind":"x"}}`,
		"bad type":      `{"name":"n","entity":"e","version":1,"defaults":{"creditLimit":"lots"}}`,
	} {
		var de *domain.Error
		if err := m.CheckTemplate(mustTemplate(t, doc)); !errors.As(err, &de) || de.Code != domain.CodeTemplateInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestPipeline_TemplateDefaultsAreInLineageAndRerunIsUnchanged(t *testing.T) {
	m := templateMapping(t)
	tmpl := mustTemplate(t, `{"name":"n","entity":"e","version":1,"defaults":{"paymentTerms":"NET30"}}`)
	rows := []domain.Record{
		{Key: "C1", Fields: map[string]any{"Name": "Acme"}},
		{Key: "C2", Fields: map[string]any{"Name": "Beta", "Terms": "NET60"}},
	}
	p, _, loader := newPipeline(rows)
	rs := domain.RunSpec{SourceSystem: system, Entity: "e", Mapping: m, Template: &tmpl}
	rep, err := p.Run(context.Background(), rs)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Created != 2 || rep.Templated != 1 {
		t.Fatalf("created=%d templated=%d", rep.Created, rep.Templated)
	}
	byKey := map[string][]string{}
	for _, e := range rep.Ledger {
		byKey[e.SourceKey] = e.TemplateFields
	}
	if len(byKey["C1"]) != 1 || byKey["C1"][0] != "paymentTerms" || len(byKey["C2"]) != 0 {
		t.Fatalf("lineage = %v", byKey)
	}
	loaded := 0
	for _, it := range loader.Rows {
		if it.SourceKey == "C1" && it.Fields["paymentTerms"] == "NET30" {
			loaded++
		}
	}
	if loaded != 1 {
		t.Fatal("templated value was not loaded")
	}
	again, err := p.Run(context.Background(), rs)
	if err != nil || again.Unchanged != 2 || again.Created != 0 || again.Updated != 0 {
		t.Fatalf("rerun: %+v err=%v", again, err)
	}
}
