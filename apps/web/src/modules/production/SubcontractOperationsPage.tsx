import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  OPER_STATUSES,
  STATUSES,
  describeProblem,
  getSubcontractOperations,
  type OperStatus,
  type Problem,
  type ProductionStatus,
  type SubcontractQuery,
  type SubcontractReport,
  type SubcontractRow,
} from "./api/client";
import { dash, operStatusLabel, operStatusTone, statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface SubcontractOperationsPageProps {
  /** Opens a production order from its row. */
  onOpenOrder?(orderId: string): void;
}

const columns: DataTableColumn<SubcontractRow>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "orderStatus", header: "Order status", render: (row) => <Badge tone={statusTone[row.orderStatus]}>{statusLabel[row.orderStatus] ?? row.orderStatus}</Badge> },
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "resourceId", header: "Vendor resource", mono: true },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "qtyGood", header: "Reported good", align: "end" },
  { key: "unitPrice", header: "Unit price", align: "end", render: (row) => `${row.unitPrice} ${row.currency}` },
  { key: "amount", header: "Amount", align: "end", render: (row) => `${row.amount} ${row.currency}` },
  { key: "dueDate", header: "Due", mono: true, render: (row) => row.dueDate ?? dash },
  { key: "operStatus", header: "Operation status", render: (row) => <Badge tone={operStatusTone[row.operStatus]}>{operStatusLabel[row.operStatus] ?? row.operStatus}</Badge> },
  {
    key: "poRequestedAt",
    header: "Vendor PO",
    render: (row) =>
      row.poRequestedAt ? (
        <Badge tone="info">
          Requested <time dateTime={row.poRequestedAt}>{row.poRequestedAt.slice(0, 10)}</time>
        </Badge>
      ) : row.poCancelledAt ? (
        <Badge tone="warning">
          Withdrawn <time dateTime={row.poCancelledAt}>{row.poCancelledAt.slice(0, 10)}</time>
        </Badge>
      ) : (
        <Badge tone="neutral">At release</Badge>
      ),
  },
];

interface Filter {
  status: OperStatus | "";
  orderStatus: ProductionStatus | "";
  resourceId: string;
  dueFrom: string;
  dueTo: string;
}

interface FilterErrors {
  resourceId?: string;
  dueTo?: string;
}

interface State {
  report: SubcontractReport;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
}

const emptyReport: SubcontractReport = { items: [], totals: [] };
const initial: State = { report: emptyReport, loading: true, loadingMore: false, error: null };

const query = (f: Filter): SubcontractQuery => ({
  status: f.status,
  orderStatus: f.orderStatus,
  resourceId: f.resourceId.trim() || undefined,
  dueFrom: f.dueFrom || undefined,
  dueTo: f.dueTo || undefined,
});

/** Checks the filter as the service does: the vendor resource is an id and the due range is in order. */
export function validateSubcontractFilter(f: Filter): FilterErrors {
  const errors: FilterErrors = {};
  const resource = f.resourceId.trim();
  if (resource !== "" && !uuidPattern.test(resource)) {
    errors.resourceId = "Enter the vendor resource's id (a UUID), or leave it empty.";
  }
  if (f.dueFrom !== "" && f.dueTo !== "" && f.dueFrom > f.dueTo) {
    errors.dueTo = "The end of the due range must not be before its start.";
  }
  return errors;
}

/**
 * Subcontract operations (PRD 4.21.14, FR-21.10): every operation done by a vendor, with the
 * quantity and price purchasing is asked for when the order is released, and when it was asked.
 */
export function SubcontractOperationsPage({ onOpenOrder }: SubcontractOperationsPageProps) {
  const [draft, setDraft] = useState<Filter>({ status: "", orderStatus: "", resourceId: "", dueFrom: "", dueTo: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<FilterErrors>({});
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    getSubcontractOperations(query(filter), controller.signal).then(
      (report) => token === latest.current && setState({ report, loading: false, loadingMore: false, error: null }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const problems = validateSubcontractFilter(draft);
    setErrors(problems);
    if (Object.keys(problems).length === 0) {
      setFilter({ ...draft });
    }
  };

  const loadMore = async () => {
    const cursor = state.report.nextCursor;
    if (!cursor) {
      return;
    }
    setState((s) => ({ ...s, loadingMore: true }));
    try {
      const next = await getSubcontractOperations({ ...query(filter), cursor });
      setState((s) => ({
        ...s,
        report: { ...s.report, items: [...s.report.items, ...next.items], nextCursor: next.nextCursor },
        loadingMore: false,
      }));
    } catch (error) {
      setState((s) => ({ ...s, loadingMore: false, error: describeProblem(error) }));
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Subcontract operations"
        description="Operations done by a vendor: the quantity and price purchasing is asked to order when the production order is released, and when it was asked."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the subcontract operations">
          <FormField id="sub-status" label="Operation status">
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
          <FormField id="sub-order-status" label="Order status">
            {(control) => (
              <Select
                {...control}
                value={draft.orderStatus}
                onChange={(e) => setDraft({ ...draft, orderStatus: e.target.value as ProductionStatus | "" })}
              >
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="sub-resource" label="Vendor resource" optional error={errors.resourceId}>
            {(control) => <Input {...control} value={draft.resourceId} onChange={(e) => setDraft({ ...draft, resourceId: e.target.value })} />}
          </FormField>
          <FormField id="sub-due-from" label="Due from" optional>
            {(control) => <Input {...control} type="date" value={draft.dueFrom} onChange={(e) => setDraft({ ...draft, dueFrom: e.target.value })} />}
          </FormField>
          <FormField id="sub-due-to" label="Due to" optional error={errors.dueTo}>
            {(control) => <Input {...control} type="date" value={draft.dueTo} onChange={(e) => setDraft({ ...draft, dueTo: e.target.value })} />}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The subcontract operations are not available" live="alert">
          {state.error.status === 403
            ? "You need the View production orders duty to see the subcontract operations."
            : `${state.error.message} (${state.error.code})`}
        </Alert>
      ) : (
        <>
          {state.report.totals.map((t) => (
            <StatGroup key={t.currency} label={`Subcontract totals in ${t.currency}`}>
              <StatTile label="Operations" value={String(t.operations)} />
              <StatTile label="Planned amount" value={`${t.amount} ${t.currency}`} />
            </StatGroup>
          ))}
          <DataTable
            label="Subcontract operations"
            columns={columns}
            rows={state.report.items}
            loading={state.loading}
            getRowKey={(row) => `${row.orderId}:${row.operNumber}`}
            onRowOpen={onOpenOrder ? (row) => onOpenOrder(row.orderId) : undefined}
            empty={{
              title: "No subcontract operations",
              description: "Operations appear here once an order's route has an operation on a vendor resource.",
            }}
          />
          {state.report.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more operations
              </Button>
            </div>
          ) : null}
          <p className={styles.hint}>
            Purchasing (wave 17) creates the vendor PO line from the request; its number and receipt status will show here once it does.
          </p>
        </>
      )}
    </div>
  );
}
