import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { INTAKE_STATUSES, describeProblem, listPlannedIntake, type IntakePage, type IntakeStatus, type PlannedIntake, type Problem } from "./api/client";
import { intakeStatusLabel, intakeStatusTone } from "./labels";
import styles from "./pages.module.css";

export interface PlannedIntakePageProps {
  /** Opens a planned order from its row. */
  onOpen?(row: PlannedIntake): void;
}

const columns: DataTableColumn<PlannedIntake>[] = [
  { key: "status", header: "Status", render: (row) => <Badge tone={intakeStatusTone[row.status]}>{intakeStatusLabel[row.status]}</Badge> },
  { key: "productId", header: "Product", mono: true },
  { key: "quantity", header: "Quantity", align: "end" },
  { key: "deliveryDate", header: "Delivery", render: (row) => <time dateTime={row.deliveryDate}>{row.deliveryDate}</time> },
  { key: "siteId", header: "Site", mono: true },
  { key: "warehouseId", header: "Warehouse", mono: true, render: (row) => row.warehouseId ?? "Site scope" },
  { key: "receivedAt", header: "Firmed", render: (row) => <time dateTime={row.receivedAt}>{row.receivedAt.slice(0, 10)}</time> },
];

interface State {
  page: IntakePage;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
  /** A failed "load more": the rows already shown stay. */
  moreError: Problem | null;
}

const initial: State = { page: { items: [] }, loading: true, loadingMore: false, error: null, moreError: null };

/**
 * The planned order intake (FR-22.10): production planned orders planning firmed, waiting for a planner to
 * convert them into production orders (or dismiss them). Warehouse-scope orders carry their warehouse.
 */
export function PlannedIntakePage({ onOpen }: PlannedIntakePageProps) {
  const [draft, setDraft] = useState<IntakeStatus | "">("open");
  const [status, setStatus] = useState<IntakeStatus | "">("open");
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    listPlannedIntake({ status }, controller.signal).then(
      (page) => token === latest.current && setState({ ...initial, page, loading: false }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [status]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    setStatus(draft);
  };

  const loadMore = async () => {
    const cursor = state.page.nextCursor;
    if (!cursor) {
      return;
    }
    const token = latest.current;
    setState((s) => ({ ...s, loadingMore: true, moreError: null }));
    try {
      const next = await listPlannedIntake({ status, cursor });
      if (token === latest.current) {
        setState((s) => ({ ...s, page: { items: [...s.page.items, ...next.items], nextCursor: next.nextCursor }, loadingMore: false }));
      }
    } catch (error) {
      if (token === latest.current) {
        setState((s) => ({ ...s, loadingMore: false, moreError: describeProblem(error) }));
      }
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Planned orders"
        description="Production orders planning firmed. Open one to enter its BOM and route and create the production order, or dismiss it."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the planned orders">
          <FormField id="pi-status" label="Status">
            {(control) => (
              <Select {...control} value={draft} onChange={(e) => setDraft(e.target.value as IntakeStatus | "")}>
                <option value="">All</option>
                {INTAKE_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {intakeStatusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The planned orders are not available" live="alert">
          {state.error.status === 403
            ? "You need the View production orders duty to see the planned orders."
            : `${state.error.message} (${state.error.code})`}
        </Alert>
      ) : (
        <>
          <DataTable
            label="Planned orders"
            columns={columns}
            rows={state.page.items}
            loading={state.loading}
            getRowKey={(row) => row.id}
            onRowOpen={onOpen}
            empty={{
              title: "No planned orders",
              description: "A planned order appears here when planning firms a production order.",
            }}
          />
          {state.moreError ? (
            <Alert tone="danger" title="More planned orders could not be loaded" live="alert">
              {`${state.moreError.message} (${state.moreError.code})`}
            </Alert>
          ) : null}
          {state.page.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more planned orders
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
