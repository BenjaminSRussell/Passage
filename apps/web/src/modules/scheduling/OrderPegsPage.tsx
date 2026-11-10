import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { deletePeg, describeProblem, listPegs, type OrderPeg, type Problem } from "./api/client";
import { formatDateTime, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/**
 * Order pegs (FR-23.8): which order consumes which order's output. A move with
 * "Move pegged orders too" pushes the dependents behind their parents.
 */
export function OrderPegsPage() {
  const [filter, setFilter] = useState("");
  const orderId = UUID.test(filter.trim()) ? filter.trim().toLowerCase() : "";
  const state = useLoad((signal) => listPegs(orderId, signal), orderId);
  const [removing, setRemoving] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");

  async function remove(peg: OrderPeg) {
    setRemoving(peg.id);
    setFailure(null);
    setNotice("");
    try {
      await deletePeg(peg.id);
      setNotice(`Order ${shortId(peg.dependentOrderId)} is no longer pegged to ${shortId(peg.parentOrderId)}.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setRemoving(null);
    }
  }

  const columns: DataTableColumn<OrderPeg>[] = [
    { key: "parentOrderId", header: "Parent order", mono: true, render: (p) => <span title={p.parentOrderId}>{shortId(p.parentOrderId)}</span> },
    { key: "dependentOrderId", header: "Dependent order", mono: true, render: (p) => <span title={p.dependentOrderId}>{shortId(p.dependentOrderId)}</span> },
    { key: "createdAt", header: "Pegged", render: (p) => formatDateTime(p.createdAt) },
    {
      key: "actions",
      header: "Actions",
      render: (p) => (
        <Button
          size="sm"
          variant="ghost"
          loading={removing === p.id}
          onClick={() => remove(p)}
          aria-label={`Remove peg of order ${shortId(p.dependentOrderId)} to ${shortId(p.parentOrderId)}`}
        >
          Remove
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Order pegs"
        description="A dependent order uses the parent order's output, so it cannot start before the parent ends. Moves can push dependents along."
        actions={
          <Link className={buttonClassName("primary")} to="/scheduling/order-pegs/new">
            New peg
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="pegs-order" label="Order" optional hint="Paste an order id to see the pegs it is part of.">
          {(control) => <Input {...control} value={filter} spellCheck={false} onChange={(event) => setFilter(event.target.value)} />}
        </FormField>
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="The peg was not removed" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load order pegs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Order pegs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(p) => p.id}
          loading={state.loading}
          empty={{
            title: orderId ? "This order has no pegs" : "No order pegs yet",
            description: "Without pegs a move changes only the moved order and its own later operations.",
          }}
        />
      )}
    </div>
  );
}
