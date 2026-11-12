import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { listOrders, type Order, type OrderStatus } from "./api/client";
import { formatDateTime, ORDER_STATUSES, qty, shortId, STATUS_LABELS, TRIGGER_LABELS, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { SourceDocument } from "../mfg-shared/orders";

export function OrderStatusBadge({ status }: { status: OrderStatus }) {
  const s = STATUS_LABELS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

const columns: DataTableColumn<Order>[] = [
  { key: "id", header: "Order", mono: true, render: (row) => <Link to={`/quality/orders/${row.id}`}>{row.number ?? `Open order ${shortId(row.id)}`}</Link> },
  { key: "trigger", header: "Trigger", render: (row) => TRIGGER_LABELS[row.trigger] ?? row.trigger },
  { key: "itemId", header: "Item", render: (row) => <EntityName entity="product" id={row.itemId} fallback={row.itemId} /> },
  { key: "source", header: "Source", render: (row) => <SourceDocument type={row.sourceRef.type} id={row.sourceRef.id} /> },
  { key: "lotQty", header: "Lot", align: "end", mono: true, render: (row) => `${qty(row.lotQty)} ${row.uom}` },
  { key: "sampleQty", header: "Sample", align: "end", mono: true, render: (row) => qty(row.sampleQty) },
  { key: "acceptNo", header: "Ac / Re", align: "end", render: (row) => `${row.acceptNo} / ${row.rejectNo}` },
  { key: "status", header: "Status", render: (row) => <OrderStatusBadge status={row.status} /> },
  { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
];

/** Quality order workbench (section 6.33.6): the worklist by status and site, newest first. */
export function OrdersPage() {
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") ?? "") as OrderStatus | "";
  const siteId = params.get("siteId") ?? "";
  const [siteDraft, setSiteDraft] = useState(siteId);
  const [siteError, setSiteError] = useState<string | null>(null);
  const state = useLoad((signal) => listOrders({ status, siteId }, signal), `orders|${status}|${siteId}`);

  function setFilter(name: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(name, value);
    else next.delete(name);
    setParams(next);
  }

  function applySite(event: FormEvent) {
    event.preventDefault();
    const value = siteDraft.trim();
    if (value !== "" && !UUID.test(value)) {
      setSiteError("Choose a site, or leave it empty for every site.");
      return;
    }
    setSiteError(null);
    setFilter("siteId", value);
  }

  const filtered = status !== "" || siteId !== "";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Quality orders"
        description="Orders created by quality associations at receipt, production and shipment, and manual orders. Open an order to record results and post its usage decision."
        actions={
          <Link className={buttonClassName("primary")} to="/quality/orders/new">
            New quality order
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="orders-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setFilter("status", event.target.value)}>
              <option value="">All statuses</option>
              {ORDER_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {STATUS_LABELS[s].label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <form className={styles.toolbar} onSubmit={applySite} noValidate>
          <FormField id="orders-site" label="Site" hint="Empty shows every site." error={siteError ?? undefined}>
            {(control) => <EntityLookup {...control} entity="site" prefill={false} value={siteDraft} invalid={Boolean(siteError)} onChange={(event) => setSiteDraft(event.target.value)} />}
          </FormField>
          <Button type="submit">Show orders</Button>
        </form>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load quality orders" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Quality orders"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            filtered
              ? { title: "No quality orders match these filters", description: "Clear a filter to see more orders." }
              : { title: "No quality orders yet", description: "Orders appear here when a receipt, production or shipment matches a quality association, or when you create one." }
          }
        />
      )}
    </div>
  );
}
