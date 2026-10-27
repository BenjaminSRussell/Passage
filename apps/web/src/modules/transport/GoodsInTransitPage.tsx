import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { GIT_STATUSES, listGoodsInTransit, type GITStatus, type GoodsInTransit } from "./api/client";
import { formatDateTime, money, stateTone, statusLabel, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<GoodsInTransit>[] = [
  { key: "voyageReference", header: "Voyage", mono: true },
  { key: "incoterm", header: "Incoterm", mono: true },
  { key: "status", header: "Status", render: (row) => <Badge tone={stateTone(row.status)}>{statusLabel(row.status)}</Badge> },
  { key: "lines", header: "Receipt lines", align: "end", render: (row) => String(row.lines.length) },
  { key: "goodsValue", header: "Goods value", align: "end", render: (row) => money(row.goodsValue, row.currency) },
  { key: "estimatedTotal", header: "Estimated landed cost", align: "end", render: (row) => money(row.estimatedTotal, row.currency) },
  { key: "inTransitWarehouse", header: "In-transit warehouse", mono: true },
  { key: "shippedAt", header: "Shipped", render: (row) => formatDateTime(row.shippedAt) },
];

/** Goods in transit (PRD 6.34.14): receipt lines owned by the buyer from the origin transfer event until they are received. */
export function GoodsInTransitPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const voyageId = UUID.test(params.get("voyageId") ?? "") ? (params.get("voyageId") ?? "") : "";
  const [status, setStatus] = useState<GITStatus | "">("");
  const state = useLoad((signal) => listGoodsInTransit({ status, voyageId }, signal), `git:${status}:${voyageId}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Goods in transit"
        description="Under an origin Incoterm (EXW, FCA, FAS, FOB, CFR, CIF, CPT, CIP) ownership moves when the goods ship. A goods-in-transit order carries those receipt lines and their estimated freight, duty and other costs into the in-transit warehouse."
        actions={
          <Link className={buttonClassName("primary")} to={`/transport/goods-in-transit/new${voyageId ? `?voyageId=${voyageId}` : ""}`}>
            New goods-in-transit order
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="git-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as GITStatus | "")}>
              <option value="">All</option>
              {GIT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {voyageId ? (
          <p className={styles.note}>
            Showing one voyage. <Link to="/transport/goods-in-transit">Show all voyages</Link>
          </p>
        ) : null}
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load goods in transit" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Goods-in-transit orders"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "" ? "No goods-in-transit orders" : `No ${statusLabel(status).toLowerCase()} orders`,
            description: "Open an order from an estimated voyage when its goods ship under an origin Incoterm.",
          }}
          onRowOpen={(row) => navigate(`/transport/goods-in-transit/${row.id}`)}
        />
      )}
    </div>
  );
}
