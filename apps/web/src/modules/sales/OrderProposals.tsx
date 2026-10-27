import { useState } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, type DataTableColumn } from "../../shell/ui";
import { describeProblem, evaluateProposals, respondToProposal, type Problem, type Proposal, type ProposalReason } from "./api/client";
import { sentence, UUID } from "./format";
import type { LineValues } from "./lines";
import styles from "./pages.module.css";

export interface OrderProposalsProps {
  legalEntityId: string;
  customerId: string;
  lines: LineValues[];
  /** Adds a line for the accepted proposal, next to the line that ordered it. */
  onAccept(proposal: Proposal, orderedLine: LineValues): void;
}

export const reasonBadge = (r: ProposalReason) => <Badge tone={r === "UP_SELL" ? "info" : "neutral"}>{sentence(r)}</Badge>;

/** The products of the lines that are entered well enough to propose for, without repeats. */
export function committedProducts(lines: LineValues[]): string[] {
  return [...new Set(lines.map((l) => l.productId.trim()).filter((id) => UUID.test(id)))];
}

/**
 * Product proposals at line commit (FR-27.21): asks the service which products
 * to offer for the entered lines. "Add line" adds a line for the product and
 * records the acceptance; "Not now" records the decline. Nothing is added to
 * the order unless the user accepts.
 */
export function OrderProposals({ legalEntityId, customerId, lines, onAccept }: OrderProposalsProps) {
  const [proposals, setProposals] = useState<Proposal[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [answering, setAnswering] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notRecorded, setNotRecorded] = useState<Problem | null>(null);
  const products = committedProducts(lines);
  const legalEntity = legalEntityId.trim();
  const customer = UUID.test(customerId.trim()) ? customerId.trim() : undefined;
  const ready = UUID.test(legalEntity) && products.length > 0;

  async function check() {
    setBusy(true);
    setFailure(null);
    setNotRecorded(null);
    try {
      setProposals(await evaluateProposals({ legalEntityId: legalEntity, ...(customer ? { customerId: customer } : {}), productIds: products }));
    } catch (error) {
      setProposals(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  async function answer(p: Proposal, accepted: boolean) {
    setAnswering(p.ruleId);
    setNotRecorded(null);
    if (accepted) {
      const ordered = lines.find((l) => l.productId.trim() === p.orderedProductId);
      if (ordered) onAccept(p, ordered);
    }
    setProposals((list) => (list ?? []).filter((x) => x.ruleId !== p.ruleId));
    try {
      await respondToProposal({ legalEntityId: legalEntity, ruleId: p.ruleId, ...(customer ? { customerId: customer } : {}), accepted });
    } catch (error) {
      setNotRecorded(describeProblem(error));
    } finally {
      setAnswering(null);
    }
  }

  const columns: DataTableColumn<Proposal>[] = [
    { key: "proposedProductId", header: "Proposed product", mono: true },
    { key: "orderedProductId", header: "For ordered product", mono: true },
    { key: "reason", header: "Reason", render: (p) => reasonBadge(p.reason) },
    {
      key: "actions",
      header: "Action",
      render: (p) => (
        <div className={styles.actions}>
          <Button size="sm" variant="primary" disabled={answering !== null} onClick={() => answer(p, true)} aria-label={`Add a line for ${p.proposedProductId}`}>
            Add line
          </Button>
          <Button size="sm" variant="ghost" disabled={answering !== null} onClick={() => answer(p, false)} aria-label={`Do not add ${p.proposedProductId}`}>
            Not now
          </Button>
        </div>
      ),
    },
  ];

  return (
    <Card title="Product proposals">
      <div className={styles.stack}>
        <p className={styles.muted}>
          Cross-sell, up-sell and accessory products for the lines entered. A proposal adds nothing unless you add its line.
        </p>
        <div>
          <Button onClick={check} loading={busy} disabled={!ready}>
            Show proposals
          </Button>
        </div>
        {!ready ? <p className={styles.muted}>Enter the legal entity and at least one product to see proposals.</p> : null}
        {failure ? (
          <Alert tone="danger" title="Could not load proposals" live="alert" action={<Button size="sm" onClick={check}>Try again</Button>}>
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {notRecorded ? (
          <Alert tone="warning" title="Your answer was not recorded" live="status">
            The order is not affected. {notRecorded.message} ({notRecorded.code})
          </Alert>
        ) : null}
        {proposals === null ? null : proposals.length === 0 ? (
          <EmptyState compact headingLevel={3} title="No proposals" description="No rule proposes a product that is not already on the order, or every proposal has been answered." />
        ) : (
          <DataTable label="Proposed products" columns={columns} rows={proposals} getRowKey={(p) => p.ruleId} />
        )}
      </div>
    </Card>
  );
}
