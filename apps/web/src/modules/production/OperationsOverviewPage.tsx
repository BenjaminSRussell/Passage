import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  OPER_STATUSES,
  STATUSES,
  describeProblem,
  getOperationsOverview,
  type OperStatus,
  type OperationOverviewRow,
  type OperationsOverview,
  type OperationsQuery,
  type Problem,
  type ProductionStatus,
} from "./api/client";
import { dash, operStatusLabel, operStatusTone, statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { ResourceName, ResourceSelect } from "../mfg-shared/resources";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface OperationsOverviewPageProps {
  /** Opens a production order from its row. */
  onOpenOrder?(orderId: string): void;
}

const columns: DataTableColumn<OperationOverviewRow>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "orderStatus", header: "Order status", render: (row) => <Badge tone={statusTone[row.orderStatus]}>{statusLabel[row.orderStatus] ?? row.orderStatus}</Badge> },
  { key: "operNumber", header: "Operation", align: "end" },
  {
    key: "resourceId",
    header: "Resource",
    render: (row) =>
      row.subcontract ? (
        <span>
          <EntityName entity="vendor" id={row.resourceId} fallback={row.resourceId} /> (vendor)
        </span>
      ) : (
        <ResourceName id={row.resourceId} />
      ),
  },
  { key: "plannedHours", header: "Planned hours", align: "end" },
  { key: "hoursReported", header: "Reported hours", align: "end" },
  { key: "qtyGood", header: "Good", align: "end" },
  { key: "qtyError", header: "Error", align: "end" },
  { key: "dueDate", header: "Due", mono: true, render: (row) => row.dueDate ?? dash },
  { key: "operStatus", header: "Status", render: (row) => <Badge tone={operStatusTone[row.operStatus]}>{operStatusLabel[row.operStatus] ?? row.operStatus}</Badge> },
];

interface Filter {
  status: OperStatus | "";
  orderStatus: ProductionStatus | "";
  resourceId: string;
  siteId: string;
  dueFrom: string;
  dueTo: string;
}

interface FilterErrors {
  resourceId?: string;
  siteId?: string;
  dueTo?: string;
}

interface State {
  overview: OperationsOverview | null;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
  /** A failed "load more" keeps the rows already shown. */
  moreError: Problem | null;
}

const initial: State = { overview: null, loading: true, loadingMore: false, error: null, moreError: null };

const query = (f: Filter): OperationsQuery => ({
  status: f.status,
  orderStatus: f.orderStatus,
  resourceId: f.resourceId.trim() || undefined,
  siteId: f.siteId.trim() || undefined,
  dueFrom: f.dueFrom || undefined,
  dueTo: f.dueTo || undefined,
});

/** Checks the filter as the service does: the resource and site are ids and the due range is in order. */
export function validateOperationsFilter(f: Filter): FilterErrors {
  const errors: FilterErrors = {};
  const resource = f.resourceId.trim();
  if (resource !== "" && !uuidPattern.test(resource)) {
    errors.resourceId = "Choose a resource, or leave it empty.";
  }
  const site = f.siteId.trim();
  if (site !== "" && !uuidPattern.test(site)) {
    errors.siteId = "Choose a site, or leave it empty.";
  }
  if (f.dueFrom !== "" && f.dueTo !== "" && f.dueFrom > f.dueTo) {
    errors.dueTo = "The end of the due range must not be before its start.";
  }
  return errors;
}

/**
 * Operations overview (PRD 4.21.14): what every order's route asks of each resource and what the
 * route cards reported so far, so the shop floor lead sees open, started and finished work at once.
 */
export function OperationsOverviewPage({ onOpenOrder }: OperationsOverviewPageProps) {
  const [draft, setDraft] = useState<Filter>({ status: "", orderStatus: "", resourceId: "", siteId: "", dueFrom: "", dueTo: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<FilterErrors>({});
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    getOperationsOverview(query(filter), controller.signal).then(
      (overview) => token === latest.current && setState({ ...initial, overview, loading: false }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const problems = validateOperationsFilter(draft);
    setErrors(problems);
    if (Object.keys(problems).length === 0) {
      setFilter({ ...draft });
    }
  };

  const loadMore = async () => {
    const cursor = state.overview?.nextCursor;
    if (!cursor) {
      return;
    }
    const token = latest.current;
    setState((s) => ({ ...s, loadingMore: true, moreError: null }));
    try {
      const next = await getOperationsOverview({ ...query(filter), cursor });
      if (token !== latest.current) {
        return;
      }
      setState((s) => ({
        ...s,
        overview: s.overview ? { ...s.overview, items: [...s.overview.items, ...next.items], nextCursor: next.nextCursor } : next,
        loadingMore: false,
      }));
    } catch (error) {
      if (token === latest.current) {
        setState((s) => ({ ...s, loadingMore: false, moreError: describeProblem(error) }));
      }
    }
  };

  const totals = state.overview?.totals;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Operations overview"
        description="Every operation of the production orders with its resource, planned and reported hours, good and error quantities and feedback status."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the operations">
          <FormField id="ops-status" label="Operation status">
            {(control) => (
              <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as OperStatus | "" })}>
                <option value="">All</option>
                {OPER_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {operStatusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ops-order-status" label="Order status">
            {(control) => (
              <Select {...control} value={draft.orderStatus} onChange={(e) => setDraft({ ...draft, orderStatus: e.target.value as ProductionStatus | "" })}>
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ops-resource" label="Resource" optional error={errors.resourceId}>
            {(control) => <ResourceSelect {...control} emptyLabel="All resources" value={draft.resourceId} onChange={(v) => setDraft({ ...draft, resourceId: v })} />}
          </FormField>
          <FormField id="ops-site" label="Site" optional error={errors.siteId}>
            {(control) => <EntityLookup {...control} entity="site" prefill={false} value={draft.siteId} onChange={(e) => setDraft({ ...draft, siteId: e.target.value })} />}
          </FormField>
          <FormField id="ops-due-from" label="Due from" optional>
            {(control) => <Input {...control} type="date" value={draft.dueFrom} onChange={(e) => setDraft({ ...draft, dueFrom: e.target.value })} />}
          </FormField>
          <FormField id="ops-due-to" label="Due to" optional error={errors.dueTo}>
            {(control) => <Input {...control} type="date" value={draft.dueTo} onChange={(e) => setDraft({ ...draft, dueTo: e.target.value })} />}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The operations overview is not available" live="alert">
          {state.error.status === 403
            ? "You need the View production orders duty to see the operations overview."
            : <ProblemMessage problem={state.error} />}
        </Alert>
      ) : (
        <>
          {totals ? (
            <StatGroup label="Operation totals">
              <StatTile label="Operations" value={String(totals.operations)} />
              <StatTile label="Open" value={String(totals.open)} />
              <StatTile label="Started" value={String(totals.started)} />
              <StatTile label="Finished" value={String(totals.finished)} />
              <StatTile label="Planned hours" value={totals.plannedHours} />
              <StatTile label="Reported hours" value={totals.hoursReported} />
            </StatGroup>
          ) : null}
          <DataTable
            label="Operations"
            columns={columns}
            rows={state.overview?.items ?? []}
            loading={state.loading}
            getRowKey={(row) => `${row.orderId}:${row.operNumber}`}
            onRowOpen={onOpenOrder ? (row) => onOpenOrder(row.orderId) : undefined}
            empty={{
              title: "No operations",
              description: "Operations appear here once a production order with a route is created.",
            }}
          />
          {state.moreError ? (
            <Alert tone="danger" title="More operations could not be loaded" live="alert">
              {<ProblemMessage problem={state.moreError} />}
            </Alert>
          ) : null}
          {state.overview?.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more operations
              </Button>
            </div>
          ) : null}
          <p className={styles.hint}>Due is the order's delivery date until operations carry their own scheduled dates.</p>
        </>
      )}
    </div>
  );
}
