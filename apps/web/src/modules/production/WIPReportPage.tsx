import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { STATUSES, describeProblem, getWIPBalances, type Problem, type ProductionStatus, type WIPQuery, type WIPRow, type WIPTotal } from "./api/client";
import { statusLabel, statusTone } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface WIPReportPageProps {
  /** Opens a production order from its row. */
  onOpenOrder?(orderId: string): void;
}

const columns: DataTableColumn<WIPRow>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "status", header: "Status", render: (row) => <Badge tone={statusTone[row.status]}>{statusLabel[row.status] ?? row.status}</Badge> },
  { key: "consumption", header: "Posted consumption", align: "end", render: (row) => `${row.consumption} ${row.currency}` },
  { key: "rafValue", header: "Received (RAF)", align: "end", render: (row) => `${row.rafValue} ${row.currency}` },
  { key: "settled", header: "Settled at End", align: "end", render: (row) => `${row.settled} ${row.currency}` },
  { key: "wipBalance", header: "WIP balance", align: "end", render: (row) => `${row.wipBalance} ${row.currency}` },
];

interface Filter {
  status: ProductionStatus | "";
  asOf: string;
  siteId: string;
}

interface State {
  rows: WIPRow[];
  totals: WIPTotal[];
  asOf: string;
  nextCursor?: string;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
}

const query = (f: Filter): WIPQuery => ({ status: f.status, asOf: f.asOf || undefined, siteId: f.siteId || undefined });

const initial: State = { rows: [], totals: [], asOf: "", loading: true, loadingMore: false, error: null };

/**
 * WIP balance by order (PRD 4.21.14): what each order has consumed into WIP, what its reports as
 * finished received, what End settled and the balance left, as of a date, with totals per currency.
 */
export function WIPReportPage({ onOpenOrder }: WIPReportPageProps) {
  const [draft, setDraft] = useState<Filter>({ status: "", asOf: "", siteId: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<{ siteId?: string }>({});
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    getWIPBalances(query(filter), controller.signal).then(
      (rep) =>
        token === latest.current &&
        setState({ rows: rep.items, totals: rep.totals, asOf: rep.asOf, nextCursor: rep.nextCursor, loading: false, loadingMore: false, error: null }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    if (draft.siteId !== "" && !uuidPattern.test(draft.siteId)) {
      setErrors({ siteId: "Choose a site, or leave it empty." });
      return;
    }
    setErrors({});
    setFilter({ ...draft });
  };

  const loadMore = async () => {
    if (!state.nextCursor) {
      return;
    }
    setState((s) => ({ ...s, loadingMore: true }));
    try {
      const rep = await getWIPBalances({ ...query(filter), asOf: state.asOf || undefined, cursor: state.nextCursor });
      setState((s) => ({ ...s, rows: [...s.rows, ...rep.items], nextCursor: rep.nextCursor, loadingMore: false }));
    } catch (error) {
      setState((s) => ({ ...s, loadingMore: false, error: describeProblem(error) }));
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="WIP balance by order"
        description="Work in progress from the production subledger: consumption posted into WIP, value received by reports as finished, and what End settled into variances."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the WIP balance">
          <FormField id="wip-status" label="Status">
            {(control) => (
              <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as ProductionStatus | "" })}>
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="wip-as-of" label="As of" optional>
            {(control) => <Input {...control} type="date" value={draft.asOf} onChange={(e) => setDraft({ ...draft, asOf: e.target.value })} />}
          </FormField>
          <FormField id="wip-site" label="Site" optional error={errors.siteId}>
            {(control) => <EntityLookup {...control} entity="site" prefill={false} value={draft.siteId} onChange={(e) => setDraft({ ...draft, siteId: e.target.value })} />}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The WIP balance is not available" live="alert">
          {state.error.status === 403 ? "You need the View production costs duty to see the WIP balance." : <ProblemMessage problem={state.error} />}
        </Alert>
      ) : (
        <>
          {state.totals.map((t) => (
            <StatGroup key={t.currency} label={`WIP totals in ${t.currency}`}>
              <StatTile label="Orders" value={String(t.orders)} />
              <StatTile label="Posted consumption" value={`${t.consumption} ${t.currency}`} />
              <StatTile label="Received (RAF)" value={`${t.rafValue} ${t.currency}`} />
              <StatTile label="Settled at End" value={`${t.settled} ${t.currency}`} />
              <StatTile label="WIP balance" value={`${t.wipBalance} ${t.currency}`} />
            </StatGroup>
          ))}
          <DataTable
            label={state.asOf ? `WIP balance as of ${state.asOf}` : "WIP balance"}
            columns={columns}
            rows={state.rows}
            loading={state.loading}
            getRowKey={(row) => row.orderId}
            onRowOpen={onOpenOrder ? (row) => onOpenOrder(row.orderId) : undefined}
            empty={{
              title: "No work in progress",
              description: "Orders appear once a journal posts material or hours into WIP on or before the date.",
            }}
          />
          {state.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more orders
              </Button>
            </div>
          ) : null}
          <p className={styles.hint}>The WIP account balance from the general ledger isn't part of this reconciliation yet.</p>
        </>
      )}
    </div>
  );
}
