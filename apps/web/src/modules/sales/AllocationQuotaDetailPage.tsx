import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { changeAllocationQuota, describeProblem, getAllocationQuota, type AllocationConsumption, type Problem, type Versioned, type AllocationQuota } from "./api/client";
import { DECIMAL, dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

const columns: DataTableColumn<AllocationConsumption>[] = [
  { key: "consumedAt", header: "Drawn", render: (c) => formatDateTime(c.consumedAt) },
  { key: "orderId", header: "Order", mono: true, render: (c) => <Link to={`/sales/orders/${c.orderId}`}>{`${c.orderId.split("-")[0]} line ${c.lineNo}`}</Link> },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "returnedAt", header: "Returned", render: (c) => (c.returnedAt ? formatDateTime(c.returnedAt) : dash) },
];

/** One quota with its draw history and the quantity change form (FR-27.16, BR-27.15). */
export function AllocationQuotaDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAllocationQuota(id, signal), id);
  const [latest, setLatest] = useState<Versioned<AllocationQuota> | null>(null);
  const [qty, setQty] = useState("");
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  const current = latest ?? state.data;
  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Allocation quota" />
        <Alert tone="danger" title="Could not load the quota" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Allocation quota" />
        <span className="visually-hidden">Loading the quota…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: quota, etag } = current;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setSaved(false);
    if (!DECIMAL.test(qty) || /^0+(\.0+)?$/.test(qty)) {
      setError("Enter a quantity above 0 with up to 6 decimals.");
      return;
    }
    setError("");
    setBusy(true);
    try {
      const res = await changeAllocationQuota(quota.id, etag, qty);
      setLatest({ value: { ...res.value, consumptions: quota.consumptions }, etag: res.etag });
      setSaved(true);
      setQty("");
      state.reload();
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  const facts: [string, ReactNode][] = [
    ["Product", <EntityName entity="product" id={quota.productId} />],
    ["Customer", quota.customerId ?? "Every customer"],
    ["Legal entity", <EntityName entity="legalEntity" id={quota.legalEntityId} />],
    ["Period", `${quota.periodStart} to ${quota.periodEnd}`],
    ["Quota", quota.quotaQty],
    ["Consumed", quota.consumedQty],
    ["Remaining", quota.remainingQty],
    ["Set", `${formatDateTime(quota.createdAt)} by ${quota.createdBy}`],
  ];
  const mono = new Set<string>();

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Allocation quota ${quota.periodStart} to ${quota.periodEnd}`}
        actions={
          <Link className={buttonClassName("ghost")} to="/sales/allocation-quotas">
            Back to quotas
          </Link>
        }
      />
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono.has(term) ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Change the quota">
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Change the quota">
          <FormField id="quota-new-qty" label="New quota quantity" hint={`At least ${quota.consumedQty}, the quantity already consumed.`} error={error}>
            {(control) => <Input {...control} inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "SLS-0036" ? "The quota cannot go below what is consumed" : "Could not change the quota"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Change quota
            </Button>
          </div>
        </form>
        <Alert tone="success" live="status">
          {saved ? `The quota is now ${quota.quotaQty}.` : null}
        </Alert>
      </Card>
      <Card title="Draws">
        {quota.consumptions && quota.consumptions.length > 0 ? (
          <DataTable label="Draws" columns={columns} rows={quota.consumptions} getRowKey={(c) => c.id} />
        ) : (
          <EmptyState compact headingLevel={3} title="Nothing drawn yet" description="Confirming an order with this product in the period draws on the quota." />
        )}
      </Card>
    </div>
  );
}
