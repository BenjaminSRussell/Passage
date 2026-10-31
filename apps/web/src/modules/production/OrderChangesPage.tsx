import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { CHANGE_STATUSES, describeProblem, listOrderChanges, type ChangePage, type ChangeQuery, type ChangeStatus, type OrderChange, type Problem } from "./api/client";
import { changeStatusLabel, changeStatusTone, describeDelta } from "./labels";
import styles from "./pages.module.css";

export interface OrderChangesPageProps {
  /** Opens a change record from its row. */
  onOpenChange?(change: OrderChange): void;
}

const columns: DataTableColumn<OrderChange>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "ecoId", header: "ECO", mono: true },
  { key: "status", header: "Status", render: (row) => <Badge tone={changeStatusTone[row.status]}>{changeStatusLabel[row.status]}</Badge> },
  {
    key: "needsReview",
    header: "Review",
    render: (row) => (row.needsReview ? <Badge tone="warning">Edited by hand</Badge> : <Badge tone="neutral">Standard</Badge>),
  },
  { key: "profileMode", header: "Profile", render: (row) => (row.profileMode === "automatic" ? "Automatic" : "Manual") },
  { key: "delta", header: "Changes", render: (row) => describeDelta(row.delta) },
  { key: "changeCost", header: "Change cost", align: "end", render: (row) => `${row.changeCost} ${row.currency}` },
  {
    key: "detectedAt",
    header: "Detected",
    render: (row) => <time dateTime={row.detectedAt}>{row.detectedAt.slice(0, 10)}</time>,
  },
];

interface Filter {
  status: ChangeStatus | "";
  ecoId: string;
  reviewOnly: boolean;
}

interface State {
  page: ChangePage;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
}

const initial: State = { page: { items: [] }, loading: true, loadingMore: false, error: null };

const query = (f: Filter): ChangeQuery => ({
  status: f.status,
  ecoId: f.ecoId.trim() || undefined,
  needsReview: f.reviewOnly ? true : undefined,
});

/**
 * The order change management worklist (PRD 4.21.14, FR-21.16): open orders whose BOM or route an
 * engineering change superseded, with the delta and the change cost, for the planner to apply or dismiss.
 */
export function OrderChangesPage({ onOpenChange }: OrderChangesPageProps) {
  const [draft, setDraft] = useState<Filter>({ status: "pending", ecoId: "", reviewOnly: false });
  const [filter, setFilter] = useState<Filter>(draft);
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    listOrderChanges(query(filter), controller.signal).then(
      (page) => token === latest.current && setState({ page, loading: false, loadingMore: false, error: null }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    setFilter({ ...draft });
  };

  const loadMore = async () => {
    const cursor = state.page.nextCursor;
    if (!cursor) {
      return;
    }
    setState((s) => ({ ...s, loadingMore: true }));
    try {
      const next = await listOrderChanges({ ...query(filter), cursor });
      setState((s) => ({ ...s, page: { items: [...s.page.items, ...next.items], nextCursor: next.nextCursor }, loadingMore: false }));
    } catch (error) {
      setState((s) => ({ ...s, loadingMore: false, error: describeProblem(error) }));
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Order changes"
        description="Open production orders whose BOM or route a newer engineering change superseded. Preview the delta and its change cost, then apply or dismiss."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the order changes">
          <FormField id="chg-status" label="Status">
            {(control) => (
              <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as ChangeStatus | "" })}>
                <option value="">All</option>
                {CHANGE_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {changeStatusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="chg-eco" label="ECO" optional>
            {(control) => <Input {...control} value={draft.ecoId} onChange={(e) => setDraft({ ...draft, ecoId: e.target.value })} />}
          </FormField>
          <FormField id="chg-review" label="Orders">
            {(control) => (
              <Select
                {...control}
                value={draft.reviewOnly ? "review" : "all"}
                onChange={(e) => setDraft({ ...draft, reviewOnly: e.target.value === "review" })}
              >
                <option value="all">All orders</option>
                <option value="review">Edited by hand only</option>
              </Select>
            )}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The order changes are not available" live="alert">
          {state.error.status === 403
            ? "You need the View production orders duty to see the order changes."
            : `${state.error.message} (${state.error.code})`}
        </Alert>
      ) : (
        <>
          <DataTable
            label="Order changes"
            columns={columns}
            rows={state.page.items}
            loading={state.loading}
            getRowKey={(row) => row.id}
            onRowOpen={onOpenChange}
            empty={{
              title: "No order changes",
              description: "A change appears here when an engineering change supersedes the BOM or route of an open order.",
            }}
          />
          {state.page.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more changes
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
