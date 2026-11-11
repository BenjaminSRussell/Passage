import { STATUS_LABEL } from "./actions";
import type { DayVisit } from "./types";

/** What the customer is shown at the end of the visit: the work done, the parts used and who signed. */
export interface CustomerSummary {
  heading: string;
  rows: { label: string; value: string }[];
  parts: { text: string }[];
  checklist: { text: string; done: boolean }[];
  note: string;
  signedBy: string | null;
}

export function customerSummary(v: DayVisit, customerName: string): CustomerSummary {
  const rows = [
    { label: "Customer", value: customerName },
    { label: "Job", value: v.number },
    { label: "Status", value: STATUS_LABEL[v.status] ?? v.status },
  ];
  if (v.address) rows.push({ label: "Address", value: v.address });
  if (v.serviceObjectLabel) rows.push({ label: "Equipment", value: v.serviceObjectLabel });
  const parts = v.lines
    .filter((l) => l.kind === "PART" && Number(l.consumedQty) > 0)
    .map((l) => ({ text: `${l.consumedQty}${l.unit ? ` ${l.unit}` : ""} ${l.description ?? "part"}` }));
  return {
    heading: `Service visit ${v.number}`,
    rows,
    parts,
    checklist: v.checklist.map((c) => ({ text: c.text, done: c.done })),
    note: v.technicianNote,
    signedBy: v.signature.signed ? (v.signature.signedBy ?? "The customer") : null,
  };
}
