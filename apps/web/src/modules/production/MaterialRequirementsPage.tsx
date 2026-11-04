import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, Badge, type DataTableColumn } from "../../shell/ui";
import {
  STATUSES,
  describeProblem,
  getMaterialItems,
  getMaterialRequirements,
  type MaterialItemRow,
  type MaterialLineRow,
  type MaterialQuery,
  type Problem,
  type ProductionStatus,
} from "./api/client";
import { dash, statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface MaterialRequirementsPageProps {
  /** Opens a production order from its row. */
  onOpenOrder?(orderId: string): void;
}

type View = "items" | "lines";

interface Filter {
  view: View;
  itemId: string;
  siteId: string;
  orderStatus: ProductionStatus | "";
  dueFrom: string;
  dueTo: string;
  all: boolean;
}

interface FilterErrors {
  itemId?: string;
  siteId?: string;
  dueTo?: string;
}

const itemColumns: DataTableColumn<MaterialItemRow>[] = [
  { key: "itemId", header: "Component", mono: true },
  { key: "orders", header: "Orders", align: "end" },
  { key: "lines", header: "Lines", align: "end" },
  { key: "qtyRequired", header: "Required", align: "end" },
  { key: "qtyIssued", header: "Issued", align: "end" },
  { key: "qtyOutstanding", header: "Outstanding", align: "end" },
  { key: "earliestDue", header: "Earliest due", mono: true, render: (row) => row.earliestDue ?? dash },
];

const lineColumns: DataTableColumn<MaterialLineRow>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "orderStatus", header: "Order status", render: (row) => <Badge tone={statusTone[row.orderStatus]}>{statusLabel[row.orderStatus] ?? row.orderStatus}</Badge> },
  { key: "lineNumber", header: "Line", mono: true },
  { key: "itemId", header: "Component", mono: true },
  { key: "qtyRequired", header: "Required", align: "end" },
  { key: "qtyIssued", header: "Issued", align: "end" },
  { key: "qtyOutstanding", header: "Outstanding", align: "end" },
  { key: "dueDate", header: "Due", mono: true, render: (row) => row.dueDate ?? dash },
];

type Rows = { view: "items"; items: MaterialItemRow[] } | { view: "lines"; items: MaterialLineRow[] };

interface State {
  rows: Rows | null;
  totals: { lines: number; items: number; orders: number } | null;
  nextCursor?: string;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
  /** A failed "load more" keeps the rows already shown. */
  moreError: Problem | null;
}

const initial: State = { rows: null, totals: null, loading: true, loadingMore: false, error: null, moreError: null };

const query = (f: Filter): MaterialQuery => ({
  itemId: f.itemId.trim() || undefined,
  siteId: f.siteId.trim() || undefined,
  orderStatus: f.orderStatus,
  dueFrom: f.dueFrom || undefined,
  dueTo: f.dueTo || undefined,
  all: f.all,
});

/** Checks the filter as the service does: item and site are ids and the due range is in order. */
export function validateMaterialFilter(f: Filter): FilterErrors {
  const errors: FilterErrors = {};
  if (f.itemId.trim() !== "" && !uuidPattern.test(f.itemId.trim())) {
    errors.itemId = "Enter the component's id (a UUID), or leave it empty.";
  }
  if (f.siteId.trim() !== "" && !uuidPattern.test(f.siteId.trim())) {
    errors.siteId = "Enter the site's id (a UUID), or leave it empty.";
  }
  if (f.dueFrom !== "" && f.dueTo !== "" && f.dueFrom > f.dueTo) {
    errors.dueTo = "The end of the due range must not be before its start.";
  }
  return errors;
}

async function fetchPage(f: Filter, cursor: string | undefined, signal?: AbortSignal): Promise<Pick<State, "rows" | "totals" | "nextCursor">> {
  if (f.view === "items") {
    const page = await getMaterialItems({ ...query(f), cursor }, signal);
    return { rows: { view: "items", items: page.items }, totals: null, nextCursor: page.nextCursor };
  }
  const page = await getMaterialRequirements({ ...query(f), cursor }, signal);
  return { rows: { view: "lines", items: page.items }, totals: page.totals, nextCursor: page.nextCursor };
}

/**
 * Material requirements (PRD 4.21.14): the components the open production orders still need to
 * issue, per component or per order line, so a planner sees what to pick or buy first. Stock on
 * hand comes with wave 16; until then this is demand, not shortage.
 */
export function MaterialRequirementsPage({ onOpenOrder }: MaterialRequirementsPageProps) {
  const [draft, setDraft] = useState<Filter>({ view: "items", itemId: "", siteId: "", orderStatus: "", dueFrom: "", dueTo: "", all: false });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<FilterErrors>({});
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    fetchPage(filter, undefined, controller.signal).then(
      (page) => token === latest.current && setState({ ...initial, ...page, loading: false }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const problems = validateMaterialFilter(draft);
    setErrors(problems);
    if (Object.keys(problems).length === 0) {
      setFilter({ ...draft });
    }
  };

  const loadMore = async () => {
    const cursor = state.nextCursor;
    if (!cursor) {
      return;
    }
    const token = latest.current;
    setState((s) => ({ ...s, loadingMore: true, moreError: null }));
    try {
      const next = await fetchPage(filter, cursor);
      if (token !== latest.current) {
        return;
      }
      setState((s) => ({
        ...s,
        rows: (s.rows ? { view: next.rows!.view, items: [...s.rows.items, ...next.rows!.items] } : next.rows) as Rows,
        nextCursor: next.nextCursor,
        loadingMore: false,
      }));
    } catch (error) {
      if (token === latest.current) {
        setState((s) => ({ ...s, loadingMore: false, moreError: describeProblem(error) }));
      }
    }
  };

  const empty = {
    title: "No open material requirements",
    description: "Components appear here while open production orders still have material to issue.",
  };
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Material requirements"
        description="The components open production orders still need to issue, per component or per order line, with what posted picking lists already issued."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the material requirements">
          <FormField id="mat-view" label="Show">
            {(control) => (
              <Select {...control} value={draft.view} onChange={(e) => setDraft({ ...draft, view: e.target.value as View })}>
                <option value="items">Per component</option>
                <option value="lines">Per order line</option>
              </Select>
            )}
          </FormField>
          <FormField id="mat-item" label="Component" optional error={errors.itemId}>
            {(control) => <Input {...control} value={draft.itemId} onChange={(e) => setDraft({ ...draft, itemId: e.target.value })} />}
          </FormField>
          <FormField id="mat-site" label="Site" optional error={errors.siteId}>
            {(control) => <Input {...control} value={draft.siteId} onChange={(e) => setDraft({ ...draft, siteId: e.target.value })} />}
          </FormField>
          <FormField id="mat-order-status" label="Order status">
            {(control) => (
              <Select {...control} value={draft.orderStatus} onChange={(e) => setDraft({ ...draft, orderStatus: e.target.value as ProductionStatus | "" })}>
                <option value="">Open orders</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="mat-due-from" label="Due from" optional>
            {(control) => <Input {...control} type="date" value={draft.dueFrom} onChange={(e) => setDraft({ ...draft, dueFrom: e.target.value })} />}
          </FormField>
          <FormField id="mat-due-to" label="Due to" optional error={errors.dueTo}>
            {(control) => <Input {...control} type="date" value={draft.dueTo} onChange={(e) => setDraft({ ...draft, dueTo: e.target.value })} />}
          </FormField>
          <FormField id="mat-all" label="Lines">
            {(control) => (
              <Select {...control} value={draft.all ? "all" : "outstanding"} onChange={(e) => setDraft({ ...draft, all: e.target.value === "all" })}>
                <option value="outstanding">Still to issue</option>
                <option value="all">Including fully issued</option>
              </Select>
            )}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The material requirements are not available" live="alert">
          {state.error.status === 403
            ? "You need the View production orders duty to see the material requirements."
            : `${state.error.message} (${state.error.code})`}
        </Alert>
      ) : (
        <>
          {state.totals ? (
            <StatGroup label="Material totals">
              <StatTile label="Lines" value={String(state.totals.lines)} />
              <StatTile label="Components" value={String(state.totals.items)} />
              <StatTile label="Orders" value={String(state.totals.orders)} />
            </StatGroup>
          ) : null}
          {filter.view === "items" ? (
            <DataTable
              label="Components"
              columns={itemColumns}
              rows={state.rows?.view === "items" ? state.rows.items : []}
              loading={state.loading}
              getRowKey={(row) => row.itemId}
              empty={empty}
            />
          ) : (
            <DataTable
              label="Order lines"
              columns={lineColumns}
              rows={state.rows?.view === "lines" ? state.rows.items : []}
              loading={state.loading}
              getRowKey={(row) => `${row.orderId}:${row.lineNumber}`}
              onRowOpen={onOpenOrder ? (row) => onOpenOrder(row.orderId) : undefined}
              empty={empty}
            />
          )}
          {state.moreError ? (
            <Alert tone="danger" title="More material requirements could not be loaded" live="alert">
              {`${state.moreError.message} (${state.moreError.code})`}
            </Alert>
          ) : null}
          {state.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more
              </Button>
            </div>
          ) : null}
          <p className={styles.hint}>Outstanding is required minus issued (posted picking lists, backflush included, net of reversals). Stock on hand is not compared yet (wave 16).</p>
        </>
      )}
    </div>
  );
}
