// Print service document rendering (/api/v1/print/documents:render) for the demo. Answers with a small
// one-page PDF that lists the document's text, so "Download PDF" works on the order, invoice and packing slip pages.
// The real service lays the document out and prints Unicode; this stand-in prints plain lines in ASCII.
import type { DocumentParty, DocumentRequest } from "../../shell/documents/pdfDocument";
import { DemoResponse, route } from "../backend";
import { CUSTOMERS, VENDORS } from "../company";

// The addresses of the demo's customers and vendors (GET /api/v1/org/addresses?ownerType=party), which the documents print.
route("GET", "/api/v1/org/addresses", ({ query }) => {
  const party = query.get("ownerType") === "party" ? [...CUSTOMERS, ...VENDORS].find((p) => p.id === query.get("ownerId")) : undefined;
  if (!party) return { items: [] };
  const formatted = `${party.city}, ${party.state}\nUS`;
  return {
    items: [{ id: `addr-${party.id}`, versionId: `addr-${party.id}-1`, ownerType: "party", ownerId: party.id, countryCode: "US", regionCode: party.state, city: party.city, street1: "", geoStatus: "unverified", formatted, purposes: [{ purpose: "business", primary: true }], validFrom: "2026-01-01", createdAt: "2026-01-01T00:00:00Z" }],
  };
});

const escape = (s: string) => s.replace(/[\\()]/g, (c) => `\\${c}`).replace(/[^ -~]/g, "?");

function pdf(lines: string[]): Uint8Array {
  const content = lines.map((l, i) => `BT /F1 ${i === 0 ? 14 : 10} Tf 50 ${790 - i * 14} Td (${escape(l)}) Tj ET`).join("\n");
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    `<< /Length ${content.length} >>\nstream\n${content}\nendstream`,
  ];
  let out = "%PDF-1.4\n";
  const offsets: number[] = [];
  objects.forEach((o, i) => {
    offsets.push(out.length);
    out += `${i + 1} 0 obj\n${o}\nendobj\n`;
  });
  const xref = out.length;
  out += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n${offsets.map((o) => `${String(o).padStart(10, "0")} 00000 n \n`).join("")}`;
  out += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return new TextEncoder().encode(out);
}

const partyLines = (p: DocumentParty, fallbackLabel: string) => [`${p.label ?? fallbackLabel}: ${p.name}`, ...(p.address ?? []).map((a) => `    ${a}`)];

route("POST", "/api/v1/print/documents:render", ({ body }) => {
  const d = body as DocumentRequest;
  const title = { PURCHASE_ORDER: "Purchase order", CUSTOMER_INVOICE: "Invoice", PACKING_SLIP: "Packing slip" }[d.documentType];
  const lines = [
    `${d.legalEntity.name} - ${title} ${d.number}`,
    ...(d.legalEntity.address ?? []),
    ...(d.counterparty ? partyLines(d.counterparty, "To") : []),
    ...(d.additionalParties ?? []).flatMap((p) => partyLines(p, "Party")),
    ...(d.fields ?? []).map((f) => `${f.label}: ${f.value}`),
    d.columns.map((c) => c.header).join(" | "),
    ...d.lines.map((l) => l.join(" | ")),
    ...(d.totals ?? []).map((t) => `${t.label}: ${t.value}`),
  ];
  return new DemoResponse(200, pdf(lines), { "Content-Type": "application/pdf" });
});
