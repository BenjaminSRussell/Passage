import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getOrderKpis, type KpiGroupBy, type OrderKpiGroup, type OrderKpis, type Problem } from "./api/client";
import { dash } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

interface Filter {
  groupBy: KpiGroupBy | "";
  siteId: string;
  productId: string;
  from: string;
  to: string;
  asOf: string;
}

interface FilterErrors {
  siteId?: string;
  productId?: string;
  to?: string;
}

/** Checks the filter as the service does: site and product are ids and the period is in order. */
export function validateKpiFilter(f: Filter): FilterErrors {
  const errors: FilterErrors = {};
  if (f.siteId.trim() !== "" && !uuidPattern.test(f.siteId.trim())) {
    errors.siteId = "Enter the site's id (a UUID), or leave it empty.";
  }
  if (f.productId.trim() !== "" && !uuidPattern.test(f.productId.trim())) {
    errors.productId = "Enter the product's id (a UUID), or leave it empty.";
  }
  if (f.from !== "" && f.to !== "" && f.from > f.to) {
    errors.to = "The end of the period must not be before its start.";
  }
  return errors;
}

/** A rate from the service ("66.67") with its percent sign, or a dash when there was nothing to measure. */
export const percent = (value: string | null): string => (value === null ? dash : `${value} %`);

const columnsFor = (groupBy: KpiGroupBy | ""): DataTableColumn<OrderKpiGroup>[] => [
  {
    key: "key",
    header: groupBy === "site" ? "Site" : groupBy === "product" ? "Product" : "Orders",
    mono: true,
    render: (row) => row.key ?? "All orders",
  },
  { key: "ended", header: "Ended", align: "end" },
  { key: "onTimePct", header: "On time", align: "end", render: (row) => percent(row.onTimePct) },
  { key: "avgCycleDays", header: "Cycle days", align: "end", render: (row) => row.avgCycleDays ?? dash },
  { key: "qtyFinished", header: "Finished", align: "end" },
  { key: "qtyError", header: "Error", align: "end" },
  { key: "scrapPct", header: "Scrap", align: "end", render: (row) => percent(row.scrapPct) },
  { key: "open", header: "Open", align: "end" },
  { key: "overdue", header: "Overdue", align: "end" },
];

interface State {
  kpis: OrderKpis | null;
  loading: boolean;
  error: Problem | null;
}

const initial: State = { kpis: null, loading: true, error: null };

/**
 * Production KPIs (4.21.14): how many orders ended in a period, how many on time, how long they took,
 * how much was scrapped, and what is still open or overdue, in total and by site or product.
 */
export function KpiPage() {
  const [draft, setDraft] = useState<Filter>({ groupBy: "", siteId: "", productId: "", from: "", to: "", asOf: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<FilterErrors>({});
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    getOrderKpis(
      {
        groupBy: filter.groupBy,
        siteId: filter.siteId.trim() || undefined,
        productId: filter.productId.trim() || undefined,
        from: filter.from || undefined,
        to: filter.to || undefined,
        asOf: filter.asOf || undefined,
      },
      controller.signal,
    ).then(
      (kpis) => token === latest.current && setState({ kpis, loading: false, error: null }),
      (error) => !controller.signal.aborted && setState({ kpis: null, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const problems = validateKpiFilter(draft);
    setErrors(problems);
    if (Object.keys(problems).length === 0) {
      setFilter({ ...draft });
    }
  };

  const kpis = state.kpis;
  const hasOrders = kpis !== null && kpis.totals.ended + kpis.totals.open > 0;
  const rows = kpis === null ? [] : filter.groupBy !== "" ? kpis.groups : hasOrders ? [kpis.totals] : [];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production KPIs"
        description="Orders ended in a period with on-time share, cycle days and scrap, and the open and overdue orders."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the KPIs">
          <FormField id="kpi-group" label="Group by">
            {(control) => (
              <Select {...control} value={draft.groupBy} onChange={(e) => setDraft({ ...draft, groupBy: e.target.value as KpiGroupBy | "" })}>
                <option value="">No grouping</option>
                <option value="site">Site</option>
                <option value="product">Product</option>
              </Select>
            )}
          </FormField>
          <FormField id="kpi-site" label="Site" optional error={errors.siteId}>
            {(control) => <Input {...control} value={draft.siteId} onChange={(e) => setDraft({ ...draft, siteId: e.target.value })} />}
          </FormField>
          <FormField id="kpi-product" label="Product" optional error={errors.productId}>
            {(control) => <Input {...control} value={draft.productId} onChange={(e) => setDraft({ ...draft, productId: e.target.value })} />}
          </FormField>
          <FormField id="kpi-from" label="Ended from" optional>
            {(control) => <Input {...control} type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />}
          </FormField>
          <FormField id="kpi-to" label="Ended to" optional error={errors.to}>
            {(control) => <Input {...control} type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />}
          </FormField>
          <FormField id="kpi-as-of" label="Overdue as of" optional>
            {(control) => <Input {...control} type="date" value={draft.asOf} onChange={(e) => setDraft({ ...draft, asOf: e.target.value })} />}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The production KPIs are not available" live="alert">
          {state.error.status === 403 ? "You need the View production orders duty to see the KPIs." : <ProblemMessage problem={state.error} />}
        </Alert>
      ) : (
        <>
          {kpis ? (
            <StatGroup label="Order KPIs">
              <StatTile label="Orders ended" value={String(kpis.totals.ended)} />
              <StatTile label="On time" value={percent(kpis.totals.onTimePct)} />
              <StatTile label="Cycle days" value={kpis.totals.avgCycleDays ?? dash} />
              <StatTile label="Scrap" value={percent(kpis.totals.scrapPct)} />
              <StatTile label="Open orders" value={String(kpis.totals.open)} />
              <StatTile label="Overdue" value={String(kpis.totals.overdue)} tone={kpis.totals.overdue > 0 ? "warning" : "neutral"} />
            </StatGroup>
          ) : null}
          <DataTable
            label="KPIs"
            columns={columnsFor(filter.groupBy)}
            rows={rows}
            loading={state.loading}
            getRowKey={(row) => row.key ?? "all"}
            empty={{ title: "No orders", description: "No production order matches these filters: none ended in the period and none is open." }}
          />
          {kpis && kpis.groupsTotal > kpis.groups.length ? (
            <p className={styles.hint}>{`Showing ${kpis.groups.length} of ${kpis.groupsTotal} groups; the tiles cover all of them.`}</p>
          ) : null}
          <p className={styles.hint}>
            On time compares the day an order was last reported as finished with its delivery date. Cycle days run from the first start to that day. Scrap is the error quantity over finished plus error. Open orders are counted as they stand now.
            {kpis?.asOf ? ` Overdue open orders are counted as of ${kpis.asOf}.` : ""}
          </p>
        </>
      )}
    </div>
  );
}
