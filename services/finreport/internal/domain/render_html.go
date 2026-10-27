package domain

import (
	"bytes"
	"html/template"
)

// RenderHTML writes a statement as one self-contained HTML document (FR-15.6,
// PRD §2.15.15): the header block, the grid and the "as of" footer, with the
// same content as RenderCSV. It needs no network: styles are inline and there
// are no scripts. Every label goes through html/template, so definition text
// cannot inject markup. Equal inputs give equal bytes.
func RenderHTML(def Definition, cells []Cell, st Stamp) ([]byte, error) {
	lay := buildLayout(def, cells, st)
	data := htmlStatement{Title: lay.title, Columns: lay.columns}
	for _, h := range lay.header {
		data.Header = append(data.Header, htmlTerm{Term: h[0], Value: h[1]})
	}
	for _, r := range lay.rows {
		row := htmlRow{Code: r.code, Label: r.label, Indent: r.indent, Strong: r.strong}
		for _, v := range r.values {
			row.Values = append(row.Values, v.text)
		}
		data.Rows = append(data.Rows, row)
	}
	for _, f := range lay.footer {
		value := f[1]
		if len(f) == 3 { // as of sequence: ledger and sequence
			value = f[1] + ": " + f[2]
		}
		data.Footer = append(data.Footer, htmlTerm{Term: f[0], Value: value})
	}
	var buf bytes.Buffer
	if err := htmlTemplate.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type htmlStatement struct {
	Title   string
	Header  []htmlTerm
	Columns []string
	Rows    []htmlRow
	Footer  []htmlTerm
}

type htmlTerm struct{ Term, Value string }

type htmlRow struct {
	Code   string
	Label  string
	Indent int
	Strong bool
	Values []string
}

var htmlTemplate = template.Must(template.New("statement").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body { font-family: system-ui, sans-serif; margin: 2rem; color: #1a1a1a; }
dl { display: grid; grid-template-columns: max-content 1fr; gap: 0.25rem 1rem; }
dt { font-weight: 600; }
dd { margin: 0; font-family: ui-monospace, monospace; }
table { border-collapse: collapse; margin: 1.5rem 0; }
th, td { padding: 0.25rem 0.75rem; border-bottom: 1px solid #ddd; text-align: left; }
td.num { text-align: right; font-variant-numeric: tabular-nums; font-family: ui-monospace, monospace; }
tr.strong td, tr.strong th { font-weight: 600; }
</style>
</head>
<body>
<h1>{{.Title}}</h1>
<dl>
{{- range .Header}}
<dt>{{.Term}}</dt><dd>{{.Value}}</dd>
{{- end}}
</dl>
<table>
<thead>
<tr><th scope="col">Row</th><th scope="col">Label</th>{{range .Columns}}<th scope="col">{{.}}</th>{{end}}</tr>
</thead>
<tbody>
{{- range .Rows}}
<tr{{if .Strong}} class="strong"{{end}}><th scope="row">{{.Code}}</th><td style="padding-left: {{.Indent}}.75rem">{{.Label}}</td>
{{- range .Values}}<td class="num">{{.}}</td>{{end}}</tr>
{{- end}}
</tbody>
</table>
<footer>
<dl>
{{- range .Footer}}
<dt>{{.Term}}</dt><dd>{{.Value}}</dd>
{{- end}}
</dl>
</footer>
</body>
</html>
`))
