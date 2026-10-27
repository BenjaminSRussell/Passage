import { Badge, type DataTableColumn } from "../../shell/ui";
import type { Entry } from "./api/client";
import { amount, dash, isNonZero } from "./format";

/** Readable names of the consolidation accounts the rules post to. */
export const accountNames: Record<string, string> = {
  IC_REVENUE: "Intercompany revenue",
  IC_COGS: "Intercompany cost of sales",
  IC_PAYABLE: "Intercompany payable",
  IC_RECEIVABLE: "Intercompany receivable",
  IC_DIFFERENCE: "Intercompany difference",
  CTA: "Translation difference (CTA)",
  COGS: "Cost of sales",
  INVENTORY: "Inventory",
  NCI_EQUITY: "Non-controlling interest (equity)",
  NCI_PROFIT: "Profit attributable to NCI",
  SHARE_CAPITAL: "Share capital",
  PRE_ACQ_RE: "Pre-acquisition retained earnings",
  GOODWILL: "Goodwill",
  INVESTMENT: "Investment in subsidiary",
  INVESTMENT_ASSOCIATE: "Investment in associate",
  SHARE_OF_PROFIT_ASSOCIATES: "Share of profit of associates",
  DIVIDEND_INCOME: "Dividend income",
};

const money = (v: string) => (isNonZero(v) ? amount(v) : dash);

/** Consolidation entry columns: rule, readable account, account code, debit and credit. */
export function entryColumns<T extends Entry>(lead: DataTableColumn<T>[] = []): DataTableColumn<T>[] {
  return [
    ...lead,
    { key: "rule", header: "Rule", render: (e) => <Badge tone="neutral">{e.rule}</Badge> },
    { key: "account", header: "Account", render: (e) => accountNames[e.account] ?? e.account },
    { key: "code", header: "Code", mono: true, render: (e) => e.account },
    { key: "debit", header: "Debit", align: "end", render: (e) => money(e.debit) },
    { key: "credit", header: "Credit", align: "end", render: (e) => money(e.credit) },
  ];
}
