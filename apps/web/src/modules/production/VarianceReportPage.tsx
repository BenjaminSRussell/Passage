import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getVarianceReport, type Problem, type VarianceQuery, type VarianceReport, type VarianceRow, type VarianceType } from "./api/client";
import { costGroupLabel, dash, varianceLabel } from "./labels";
import styles from "./pages.module.css";

export interface VarianceReportPageProps {
  /** Opens a production order from its row. */
  onOpenOrder?(orderId: string): void;
}

const columns: DataTableColumn<VarianceRow>[] = [
  { key: "orderNumber", header: "Order", mono: true },
  { key: "type", header: "Variance", render: (row) => varianceLabel[row.type] ?? row.type },
  { key: "costGroup", header: "Cost group", render: (row) => costGroupLabel[row.costGroup] ?? row.costGroup },
  { key: "amount", header: "Amount (debit +)", align: "end", render: (row) => `${row.amount} ${row.currency}` },
  { key: "planned", header: "Planned", align: "end", render: (row) => `${row.planned} ${row.currency}` },
  { key: "pctOfPlanned", header: "% of planned", align: "end", render: (row) => (row.pctOfPlanned === null ? dash : `${row.pctOfPlanned}%`) },
  { key: "postedOn", header: "Posted", mono: true },
];

interface Filter {
  varianceType: VarianceType | "";
  from: string;
  to: string;
  itemGroup: string;
}

interface State {
  report: VarianceReport;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
}

const emptyReport: VarianceReport = { items: [], totals: [], net: [] };
const initial: State = { report: emptyReport, loading: true, loadingMore: false, error: null };

const query = (f: Filter): VarianceQuery => ({ varianceType: f.varianceType, from: f.from, to: f.to, itemGroup: f.itemGroup.trim() });

/** Checks the period; the service applies the same rule. */
export function validatePeriod(from: string, to: string): string | undefined {
  return from !== "" && to !== "" && from > to ? "The end of the period must not be before its start." : undefined;
}

/**
 * Variance report (PRD 4.21.14): the variances End posted, by order, type and cost group, with
 * each as a share of the planned cost, the totals by type and the net per currency.
 */
export function VarianceReportPage({ onOpenOrder }: VarianceReportPageProps) {
  const [draft, setDraft] = useState<Filter>({ varianceType: "", from: "", to: "", itemGroup: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [periodError, setPeriodError] = useState<string | undefined>();
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    getVarianceReport(query(filter), controller.signal).then(
      (report) => token === latest.current && setState({ report, loading: false, loadingMore: false, error: null }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const problem = validatePeriod(draft.from, draft.to);
    setPeriodError(problem);
    if (!problem) {
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
      const next = await getVarianceReport({ ...query(filter), cursor });
      setState((s) => ({ ...s, report: { ...s.report, items: [...s.report.items, ...next.items], nextCursor: next.nextCursor }, loadingMore: false }));
    } catch (error) {
      setState((s) => ({ ...s, loadingMore: false, error: describeProblem(error) }));
    }
  };

  const { report } = state;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production variances"
        description="Variances posted when orders end: lot size, quantity, price, substitution and rework. Positive is unfavourable."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the variances">
          <FormField id="var-type" label="Variance">
            {(control) => (
              <Select {...control} value={draft.varianceType} onChange={(e) => setDraft({ ...draft, varianceType: e.target.value as VarianceType | "" })}>
                <option value="">All variances</option>
                {(Object.keys(varianceLabel) as VarianceType[]).map((t) => (
                  <option key={t} value={t}>
                    {varianceLabel[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="var-from" label="Posted from" optional>
            {(control) => <Input {...control} type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />}
          </FormField>
          <FormField id="var-to" label="Posted to" optional error={periodError}>
            {(control) => <Input {...control} type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />}
          </FormField>
          <FormField id="var-item-group" label="Item group" optional>
            {(control) => <Input {...control} value={draft.itemGroup} onChange={(e) => setDraft({ ...draft, itemGroup: e.target.value })} />}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The variances are not available" live="alert">
          {state.error.status === 403 ? "You need the View production costs duty to see variances." : `${state.error.message} (${state.error.code})`}
        </Alert>
      ) : (
        <>
          {report.net.map((n) => (
            <StatGroup key={n.currency} label={`Variance totals in ${n.currency}`}>
              {report.totals
                .filter((t) => t.currency === n.currency)
                .map((t) => (
                  <StatTile key={t.type} label={varianceLabel[t.type] ?? t.type} value={`${t.amount} ${t.currency}`} />
                ))}
              <StatTile label="Net" value={`${n.amount} ${n.currency}`} hint={n.amount.startsWith("-") ? "Favourable" : undefined} />
            </StatGroup>
          ))}
          <DataTable
            label="Posted variances"
            columns={columns}
            rows={report.items}
            loading={state.loading}
            getRowKey={(row) => row.id}
            onRowOpen={onOpenOrder ? (row) => onOpenOrder(row.orderId) : undefined}
            empty={{ title: "No variances", description: "Variances are posted when a production order ends." }}
          />
          {report.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more variances
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
