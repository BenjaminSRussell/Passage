import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listFreeGoodsRules, type FreeGoodsRule } from "./api/client";
import { describeOffer, describeProduct, describeWho, FREE_GOODS_STATES, freeGoodsState } from "./freeGoodsFormat";
import { today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function columns(day: string): DataTableColumn<FreeGoodsRule>[] {
  return [
    { key: "productId", header: "Product", mono: true, render: describeProduct },
    { key: "customerKey", header: "Customers", render: describeWho },
    { key: "freeQuantity", header: "Offer", render: describeOffer },
    { key: "validFrom", header: "Valid from", mono: true },
    { key: "validTo", header: "Valid to", mono: true },
    {
      key: "withdrawn",
      header: "Status",
      render: (row) => {
        const s = FREE_GOODS_STATES[freeGoodsState(row, day)];
        return <Badge tone={s.tone}>{s.label}</Badge>;
      },
    },
  ];
}

/** Free goods rules (FR-26.22): free units per quantity ordered, proposed as zero-priced lines on a priced order. */
export function FreeGoodsPage() {
  const navigate = useNavigate();
  const day = today();
  const state = useLoad((signal) => listFreeGoodsRules({}, signal), "all");

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Free goods"
        description="A rule gives free units for every quantity ordered of a product or of every product in a product group. Pricing proposes them as zero-priced lines that sales adds to the order."
        actions={
          <Link className={buttonClassName("primary")} to="/pricing/free-goods/new">
            New free goods rule
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the free goods rules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Free goods rules"
          columns={columns(day)}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No free goods rules", description: "Create a rule, for example 1 free within every 10 ordered." }}
          onRowOpen={(row) => navigate(`/pricing/free-goods/${row.id}`)}
        />
      )}
      {state.data && state.data.length >= 100 ? <p className={styles.note}>Showing the first 100 rules.</p> : null}
    </div>
  );
}
