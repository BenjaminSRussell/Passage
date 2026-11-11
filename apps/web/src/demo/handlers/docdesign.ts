// Print service docgen routes (/api/v1/print): document templates, text blocks and output rules.
// Fictional Cedar Ridge Pumps data; read-only in the demo.
import type { DocumentTemplate, OutputRule, TextBlock } from "../../modules/document-design/api";
import { CUSTOMERS, LEGAL_ENTITY } from "../company";
import { route } from "../backend";
import { uid } from "../store";

const BASE = "/api/v1/print";
const K_RULE = 390;

const templates: DocumentTemplate[] = [
  { code: "INV-STANDARD", name: "Standard invoice", documentType: "CUSTOMER_INVOICE", default: true, version: 3,
    config: { title: "Tax invoice", footerText: "Cedar Ridge Pumps, Inc. - Registered in Ohio", hideColumns: ["Unit"], noteBlocks: ["PAYMENT-TERMS"] } },
  { code: "PO-STANDARD", name: "Standard purchase order", documentType: "PURCHASE_ORDER", default: true, version: 2,
    config: { renameColumns: { Qty: "Quantity" }, noteBlocks: ["PO-CONDITIONS"] } },
  { code: "STMT-MONTHLY", name: "Monthly statement of account", documentType: "PERIODIC_STATEMENT", default: true, version: 1,
    config: { bodyBlock: "STATEMENT-COVER" } },
  { code: "BALCONF-YEAREND", name: "Year-end balance confirmation", documentType: "BALANCE_CONFIRMATION", default: false, version: 1,
    config: { bodyBlock: "BALANCE-CONFIRMATION", footerText: "Please sign and return one copy." } },
];

const blocks: TextBlock[] = [
  { code: "PAYMENT-TERMS", language: "en", body: "Payment is due within 30 days of the invoice date.", placeholders: [], version: 2 },
  { code: "PAYMENT-TERMS", language: "de", body: "Zahlbar innerhalb von 30 Tagen nach Rechnungsdatum.", placeholders: [], version: 1 },
  { code: "PO-CONDITIONS", language: "en", body: "Goods are accepted subject to our general conditions of purchase.", placeholders: [], version: 1 },
  { code: "STATEMENT-COVER", language: "en", body: "Dear {{counterpartyName}}, this statement lists the open items on your account up to the statement date shown above.", placeholders: ["counterpartyName"], version: 1 },
  { code: "BALANCE-CONFIRMATION", language: "en", body: "As of {{balanceDate}} our records show a balance of {{amount}} on your account. If this differs from yours, tell us within 14 days.", placeholders: ["amount", "balanceDate"], version: 1 },
];

const customer = CUSTOMERS[0]!;
const rules: OutputRule[] = [
  { id: uid(K_RULE, 1), documentType: "CUSTOMER_INVOICE", copyKind: "ORIGINAL", channel: "PRINTER", destination: "OFFICE", copies: 1, sequence: 1 },
  { id: uid(K_RULE, 2), documentType: "CUSTOMER_INVOICE", copyKind: "COPY", channel: "ARCHIVE", copies: 1, sequence: 1 },
  { id: uid(K_RULE, 3), documentType: "CUSTOMER_INVOICE", accountId: customer.id, copyKind: "ORIGINAL", channel: "EMAIL", destination: "payables@bluewater.example", copies: 1, sequence: 1 },
  { id: uid(K_RULE, 4), documentType: "PACKING_SLIP", legalEntityId: LEGAL_ENTITY.id, copyKind: "ORIGINAL", channel: "PRINTER", destination: "DOCK", copies: 2, sequence: 1 },
];

route("GET", `${BASE}/document-templates`, () => ({ items: templates }));
route("GET", `${BASE}/text-blocks`, () => ({ items: blocks }));
route("GET", `${BASE}/output-rules`, () => ({ items: rules }));
