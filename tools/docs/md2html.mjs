// Usage: node tools/docs/md2html.mjs <input.md> <output.html> [title]
// Renders GFM markdown to a self-contained HTML page for headless-Chrome PDF printing.
import { readFileSync, writeFileSync } from "node:fs";
import { marked } from "marked";

const [input, output, title = "Passage PRD"] = process.argv.slice(2);
if (!input || !output) {
  console.error("usage: md2html.mjs <input.md> <output.html> [title]");
  process.exit(2);
}

const css = `
body{font-family:-apple-system,Helvetica,Arial,sans-serif;font-size:10.5pt;line-height:1.4;margin:0;color:#111}
h1{font-size:20pt;border-bottom:2px solid #333;padding-bottom:4px;margin-top:28pt;page-break-before:always}
h1:first-of-type{page-break-before:avoid}
h2{font-size:15pt;margin-top:20pt;border-bottom:1px solid #999}
h3{font-size:12.5pt;margin-top:14pt} h4{font-size:11pt}
table{border-collapse:collapse;width:100%;font-size:8.5pt;margin:8pt 0}
th,td{border:1px solid #bbb;padding:3px 5px;vertical-align:top;text-align:left}
th{background:#eee} tr{page-break-inside:avoid}
code{font-family:Menlo,monospace;font-size:8.5pt;background:#f4f4f4;padding:0 2px}
pre{background:#f4f4f4;padding:6px;font-size:8.5pt;white-space:pre-wrap;border:1px solid #ddd}
@page{size:A4;margin:16mm 14mm}
`;

const md = readFileSync(input, "utf8");
const body = marked.parse(md, { gfm: true }).replaceAll("\\newpage", '<div style="page-break-after:always"></div>');
writeFileSync(output, `<!doctype html><html><head><meta charset="utf-8"><title>${title}</title><style>${css}</style></head><body>${body}</body></html>`);
console.log(`wrote ${output} (${body.length} bytes of body)`);
