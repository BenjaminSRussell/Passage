// The B2B exchange screens (/api/v1/edi): e-invoices per mandate with submit, the network's answer, the Peppol rule check and the Factur-X
// PDF, and the self-billing dispute worklist. Cedar Ridge Pumps Europe invoices a German distributor under Peppol and Factur-X and a
// Spanish one under Peppol; Bluewater and Summit self-bill their purchases from the US plant.
import { CUSTOMERS, LEGAL_ENTITY_DE, LEGAL_ENTITY_ES } from "../company";
import { reply, route } from "../backend";
import { at, table, uid } from "../store";

const EDI = "/api/v1/edi";
const KIND = 470;

const partners = [
  { id: uid(KIND, 1), name: "Müller Großhandel KG", isaQualifier: "ZZ", isaId: "MUELLER", environment: "PROD", ackSlaMinutes: 60, status: "active", version: 1, createdAt: at(-90) },
  { id: uid(KIND, 2), name: CUSTOMERS[0]!.name, isaQualifier: "ZZ", isaId: "BLUEWATER", environment: "PROD", ackSlaMinutes: 120, status: "active", version: 1, createdAt: at(-60) },
  { id: uid(KIND, 3), name: CUSTOMERS[2]!.name, isaQualifier: "ZZ", isaId: "SUMMIT", environment: "PROD", ackSlaMinutes: 120, status: "active", version: 1, createdAt: at(-45) },
];

route("GET", `${EDI}/partners`, () => ({ items: partners }));

interface Issue {
  code: string;
  detail: string;
  reject: boolean;
}

interface EInvoice {
  id: string;
  legalEntityId: string;
  invoiceId: string;
  number: string;
  mandate: "PEPPOL" | "IT_SDI" | "MX_CFDI" | "FACTURX";
  format: string;
  documentType: "INVOICE" | "CREDIT_NOTE";
  status: "VALID" | "INVALID" | "SUBMITTING" | "SUBMITTED" | "ACCEPTED" | "REJECTED";
  xml?: string;
  issues: Issue[];
  reference?: string;
  responseText?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

const UBL = (number: string, net: string, vat: string, gross: string) =>
  `<?xml version="1.0" encoding="UTF-8"?>\n<Invoice xmlns="urn:oasis:names:specification:ubl:schema:xsd:Invoice-2" xmlns:cac="urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2" xmlns:cbc="urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2">\n  <cbc:CustomizationID>urn:cen.eu:en16931:2017#compliant#urn:fdc:peppol.eu:2017:poacc:billing:3.0</cbc:CustomizationID>\n  <cbc:ID>${number}</cbc:ID>\n  <cbc:DocumentCurrencyCode>EUR</cbc:DocumentCurrencyCode>\n  <cac:TaxTotal><cbc:TaxAmount currencyID="EUR">${vat}</cbc:TaxAmount></cac:TaxTotal>\n  <cac:LegalMonetaryTotal><cbc:TaxExclusiveAmount currencyID="EUR">${net}</cbc:TaxExclusiveAmount><cbc:PayableAmount currencyID="EUR">${gross}</cbc:PayableAmount></cac:LegalMonetaryTotal>\n</Invoice>`;

function invoice(n: number, number: string, mandate: EInvoice["mandate"], status: EInvoice["status"], extra: Partial<EInvoice> = {}): EInvoice {
  return {
    id: uid(KIND + 1, n),
    legalEntityId: LEGAL_ENTITY_DE.id,
    invoiceId: uid(KIND + 2, n),
    number,
    mandate,
    format: mandate === "PEPPOL" ? "UBL21" : mandate === "FACTURX" ? "FACTURX_EN16931" : mandate,
    documentType: "INVOICE",
    status,
    xml: status === "INVALID" ? undefined : UBL(number, "2428.11", "461.34", "2889.45"),
    issues: [],
    version: 1,
    createdAt: at(-3 + n * 0.1),
    updatedAt: at(-2 + n * 0.1),
    ...extra,
  };
}

const einvoices = table<EInvoice>([
  invoice(1, "INV-DE-10412", "PEPPOL", "ACCEPTED", { reference: "MLR-20261108-0412" }),
  invoice(2, "INV-DE-10413", "PEPPOL", "SUBMITTED", { reference: "AP-INV-DE-10413" }),
  invoice(3, "INV-DE-10414", "PEPPOL", "VALID"),
  invoice(4, "INV-DE-10415", "FACTURX", "VALID"),
  invoice(5, "INV-DE-10416", "PEPPOL", "REJECTED", { reference: "AP-INV-DE-10416", responseText: "BR-CO-17: the VAT amount of category S at 19 % is not the taxable amount times the rate." }),
  invoice(6, "INV-DE-10417", "PEPPOL", "INVALID", {
    issues: [{ code: "EINV-0110", detail: "AccountingCustomerParty EndpointID with schemeID is required", reject: true }],
  }),
  invoice(7, "INV-ES-20031", "PEPPOL", "ACCEPTED", { legalEntityId: LEGAL_ENTITY_ES.id, reference: "MLR-20261107-0031" }),
]);

route("GET", `${EDI}/einvoice/invoices`, ({ query }) => {
  const status = query.get("status") ?? "";
  const mandate = query.get("mandate") ?? "";
  const rows = einvoices
    .all()
    .filter((e) => (!status || e.status === status) && (!mandate || e.mandate === mandate))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map(({ xml: _xml, ...rest }) => rest);
  return { items: rows };
});

route("GET", `${EDI}/einvoice/invoices/{id}`, ({ params }) => einvoices.get(params.id) ?? reply.problem(404, "EDI-0005", "e-invoice not found"));

route("POST", `${EDI}/einvoice/invoices/{id}:submit`, ({ params }) => {
  const e = einvoices.get(params.id);
  if (!e) return reply.problem(404, "EDI-0005", "e-invoice not found");
  if (e.status !== "VALID" && e.status !== "REJECTED") return reply.problem(409, "EDI-0030", `the e-invoice is already ${e.status.toLowerCase()}`);
  return einvoices.update(params.id, { status: "SUBMITTED", reference: `AP-${e.number}`, responseText: undefined, version: e.version + 1, updatedAt: at(0) });
});

route("POST", `${EDI}/einvoice/invoices/{id}:response`, ({ params, body }) => {
  const e = einvoices.get(params.id);
  const answer = body as { outcome?: string; reference?: string; reason?: string };
  if (!e) return reply.problem(404, "EDI-0005", "e-invoice not found");
  if (e.status !== "SUBMITTED") return reply.problem(409, "EDI-0030", `the e-invoice is ${e.status.toLowerCase()}, not submitted`);
  if (answer.outcome !== "ACCEPTED" && answer.outcome !== "REJECTED") return reply.problem(400, "COM-0001", "invalid", { outcome: "must be ACCEPTED or REJECTED" });
  return einvoices.update(params.id, {
    status: answer.outcome,
    reference: answer.reference || e.reference,
    responseText: answer.reason || undefined,
    version: e.version + 1,
    updatedAt: at(0),
  });
});

route("POST", `${EDI}/einvoice/invoices/{id}:validate`, ({ params }) => {
  const e = einvoices.get(params.id);
  if (!e) return reply.problem(404, "EDI-0005", "e-invoice not found");
  if (e.mandate !== "PEPPOL" || !e.xml) return reply.problem(409, "EDI-0030", "the Peppol rules apply to a built Peppol e-invoice");
  const broken = e.status === "REJECTED";
  return {
    valid: !broken,
    rules: "peppol-bis-3.0-go-subset",
    issues: broken ? [{ code: "EINV-0114", detail: "BR-CO-17: the VAT of category S at 19% is 461.35, not 461.34", reject: true }] : [],
  };
});

route("GET", `${EDI}/einvoice/invoices/{id}/facturx.pdf`, ({ params }) => {
  const e = einvoices.get(params.id);
  if (!e || e.mandate !== "FACTURX") return reply.problem(409, "EDI-0030", "the Factur-X PDF applies to a built FACTURX e-invoice");
  return reply.text("%PDF-1.7\n% Factur-X demo document\n", "application/pdf", `${e.number}.pdf`);
});

interface Dispute {
  id: string;
  partnerId: string;
  kind: "DIFFERENCE" | "UNMATCHED" | "UNBILLED";
  code: string;
  documentId?: string;
  line?: string;
  shipmentId?: string;
  detail: string;
  status: "OPEN" | "ACCEPTED" | "REJECTED";
  resolution?: string;
  resolvedAt?: string;
  createdAt: string;
}

const disputes = table<Dispute>([
  {
    id: uid(KIND + 3, 1),
    partnerId: partners[1]!.id,
    kind: "DIFFERENCE",
    code: "EDI-0136",
    documentId: uid(KIND + 4, 1),
    line: "2",
    detail: "Self-billed line 2 differs: unitPrice 482.50 vs 480.00.",
    status: "OPEN",
    createdAt: at(-4),
  },
  {
    id: uid(KIND + 3, 2),
    partnerId: partners[1]!.id,
    kind: "UNMATCHED",
    code: "EDI-0137",
    documentId: uid(KIND + 4, 1),
    line: "3",
    detail: "Self-billed line 3 has no matching shipment.",
    status: "OPEN",
    createdAt: at(-4),
  },
  {
    id: uid(KIND + 3, 3),
    partnerId: partners[2]!.id,
    kind: "UNBILLED",
    code: "EDI-0138",
    shipmentId: uid(KIND + 5, 1),
    detail: "Shipment SHP-0041-PS-1 line 1 item SEAL-MK-125 shipped " + at(-40).slice(0, 10) + " was not self-billed within 30 days.",
    status: "OPEN",
    createdAt: at(-9),
  },
  {
    id: uid(KIND + 3, 4),
    partnerId: partners[2]!.id,
    kind: "DIFFERENCE",
    code: "EDI-0136",
    documentId: uid(KIND + 4, 2),
    line: "1",
    detail: "Self-billed line 1 differs: quantity 12 vs 10.",
    status: "ACCEPTED",
    resolution: "Customer received an extra pair; accepted. Their figures taken.",
    resolvedAt: at(-6),
    createdAt: at(-12),
  },
]);

route("GET", `${EDI}/self-billing/disputes`, ({ query }) => {
  const status = query.get("status") ?? "";
  const partner = query.get("partnerId") ?? "";
  return { items: disputes.all().filter((d) => (!status || d.status === status) && (!partner || d.partnerId === partner)).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
});

route("POST", `${EDI}/self-billing/disputes/{id}:resolve`, ({ params, body }) => {
  const d = disputes.get(params.id);
  const decision = (body as { decision?: string; note?: string }).decision;
  if (!d) return reply.problem(404, "EDI-0005", "dispute not found");
  if (d.status !== "OPEN") return reply.problem(409, "EDI-0026", "the dispute was already decided");
  if (decision !== "ACCEPTED" && decision !== "REJECTED") return reply.problem(400, "COM-0001", "invalid", { decision: "must be ACCEPTED or REJECTED" });
  const note = (body as { note?: string }).note ?? "";
  return disputes.update(params.id, { status: decision, resolution: note || undefined, resolvedAt: at(0) });
});

route("POST", `${EDI}/self-billing:sweep`, () => ({ reconciled: 0, unbilled: [] }));

