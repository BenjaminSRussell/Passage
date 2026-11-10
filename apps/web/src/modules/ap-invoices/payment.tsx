import { useQueries } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { ApiError } from "../../shell/api/http";
import { fetchOpenItemsForInvoices, OPEN_ITEMS_BY_INVOICE_LIMIT, type OpenItem } from "../../shell/api/openItems";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, Skeleton, type BadgeTone } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import type { Invoice } from "./api/client";

export const OPEN_ITEM_DUTY = "open-item.read";
const PROPOSAL_DUTY = "payment-proposal.read";

export const NO_ACCESS_TEXT = "Payment status is not available to you";

const today = () => new Date().toISOString().slice(0, 10);

export interface OpenItemLookup {
  allowed: boolean;
  pending: boolean;
  forbidden: boolean;
  failed: boolean;
  byInvoice: Map<string, OpenItem>;
  retry: () => void;
}

function chunksByEntity(invoices: readonly Pick<Invoice, "id" | "legalEntityId">[]) {
  const grouped = new Map<string, string[]>();
  for (const { id, legalEntityId } of invoices) grouped.set(legalEntityId, [...(grouped.get(legalEntityId) ?? []), id]);
  return [...grouped].flatMap(([legalEntityId, ids]) =>
    Array.from({ length: Math.ceil(ids.length / OPEN_ITEMS_BY_INVOICE_LIMIT) }, (_, n) => ({ legalEntityId, ids: ids.slice(n * OPEN_ITEMS_BY_INVOICE_LIMIT, (n + 1) * OPEN_ITEMS_BY_INVOICE_LIMIT) })),
  );
}

/** The payables open item behind each posted invoice, read with the caller's own duties. */
export function useOpenItemsForInvoices(invoices: readonly Pick<Invoice, "id" | "legalEntityId">[]): OpenItemLookup {
  const allowed = holdsDuty(usePrincipalDuties(), OPEN_ITEM_DUTY);
  const chunks = chunksByEntity(invoices);
  const results = useQueries({
    queries: chunks.map((c) => ({
      queryKey: ["ap-invoices", "open-items", c.legalEntityId, c.ids.join(",")],
      queryFn: () => fetchOpenItemsForInvoices(c.legalEntityId, c.ids),
      enabled: allowed,
      retry: false,
    })),
  });
  const byInvoice = new Map<string, OpenItem>();
  for (const item of results.flatMap((r) => r.data ?? [])) if (item.sourceInvoiceId) byInvoice.set(item.sourceInvoiceId, item);
  const errors = results.flatMap((r) => (r.error ? [r.error] : []));
  const forbidden = errors.some((e) => e instanceof ApiError && e.status === 403);
  return {
    allowed,
    pending: allowed && results.some((r) => r.isPending),
    forbidden,
    failed: errors.some((e) => !(e instanceof ApiError && e.status === 403)),
    byInvoice,
    retry: () => results.forEach((r) => void r.refetch()),
  };
}

export function dueDateText(invoice: Pick<Invoice, "id" | "status">, lookup: OpenItemLookup): string {
  if (!lookup.allowed || lookup.forbidden) return "Not available to you";
  const item = lookup.byInvoice.get(invoice.id);
  if (item) return item.dueDateSource === "DEFAULTED" ? `${item.dueDate} (no payment terms yet)` : item.dueDate;
  if (lookup.pending) return "Loading…";
  if (lookup.failed) return "Could not load";
  return invoice.status === "POSTED" ? "Not scheduled yet" : "None yet";
}

interface PaymentState {
  label: string;
  tone: BadgeTone;
}

export function paymentState(item: OpenItem, day: string): PaymentState {
  if (item.status === "SETTLED") return { label: "Paid", tone: "success" };
  if (item.status === "REVERSED") return { label: "Payment reversed", tone: "neutral" };
  if (item.paymentHold) return { label: "On hold", tone: "warning" };
  if (item.proposalId) return { label: "In a payment proposal", tone: "info" };
  return item.dueDate <= day ? { label: "Due", tone: "warning" } : { label: "Not due yet", tone: "neutral" };
}

function Row({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

const noPaymentText: Record<Invoice["status"], string> = {
  POSTED: "No payment is scheduled yet. It appears here once accounts payable has picked up the posted invoice.",
  AUDIT_BLOCKED: "Nothing is payable until the invoice is released from audit.",
  AUDIT_REJECTED: "This invoice was rejected after audit, so it will not be paid.",
};

export function PaymentCard({ invoice, lookup }: { invoice: Invoice; lookup: OpenItemLookup }) {
  const duties = usePrincipalDuties();
  const item = lookup.byInvoice.get(invoice.id);
  let body: ReactNode;
  if (!lookup.allowed || lookup.forbidden) {
    body = <p>{NO_ACCESS_TEXT}.</p>;
  } else if (item) {
    const state = paymentState(item, today());
    body = (
      <dl className={styles.details}>
        <Row term="Status">
          <Badge tone={state.tone}>{state.label}</Badge>
        </Row>
        <Row term="Due date">{item.dueDateSource === "DEFAULTED" ? `${item.dueDate} (no payment terms yet)` : item.dueDate}</Row>
        <Row term="Still to pay">{`${item.openAmount} ${item.currency}`}</Row>
        {item.paymentHold ? <Row term="Hold">{item.paymentHoldReason ? `On hold: ${item.paymentHoldReason}` : "On hold, no reason recorded"}</Row> : null}
        {item.proposalId ? (
          <Row term="Payment proposal">
            {holdsDuty(duties, PROPOSAL_DUTY) ? <Link to={`/payables/proposals/${encodeURIComponent(item.proposalId)}`}>View the payment proposal</Link> : "Included in a payment proposal"}
          </Row>
        ) : null}
        <Row term="Open item">
          <Link to="/payables/open-items">See it among the vendor open items</Link>
        </Row>
      </dl>
    );
  } else if (lookup.pending) {
    body = (
      <div aria-busy="true">
        <span className="visually-hidden">Loading the payment status…</span>
        <Skeleton height="control" />
      </div>
    );
  } else if (lookup.failed) {
    body = (
      <Alert tone="warning" title="Could not load the payment status" live="alert" action={<Button size="sm" onClick={lookup.retry}>Try loading the payment status again</Button>}>
        The invoice itself is shown above.
      </Alert>
    );
  } else {
    body = <p>{noPaymentText[invoice.status]}</p>;
  }
  return <Card title="Payment status">{body}</Card>;
}
