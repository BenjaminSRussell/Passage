import { useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getJournalInquiry,
  type JournalInquiry,
  type JournalInquiryQuery,
  type JournalInquiryRow,
  type ListedJournalType,
  type Problem,
} from "./api/client";
import { amount, dash, journalTypeLabel } from "./labels";
import styles from "./pages.module.css";

export interface JournalInquiryPageProps {
  /** Opens a production order from its row. */
  onOpenOrder?(orderId: string): void;
}

const types = Object.keys(journalTypeLabel) as ListedJournalType[];

const columns: DataTableColumn<JournalInquiryRow>[] = [
  { key: "journalNumber", header: "Journal", mono: true },
  { key: "type", header: "Type", render: (row) => journalTypeLabel[row.type] ?? row.type },
  { key: "orderNumber", header: "Order", mono: true },
  { key: "postingDate", header: "Posting date", mono: true },
  {
    key: "posted",
    header: "Status",
    render: (row) =>
      row.reversalOf ? <Badge tone="warning">Reversal</Badge> : row.posted ? <Badge tone="success">Posted</Badge> : <Badge tone="neutral">Draft</Badge>,
  },
  { key: "lines", header: "Lines", align: "end" },
  // A draft is valued when it posts, so its values are not shown yet.
  { key: "consumed", header: "Consumed", align: "end", render: (row) => (row.posted ? `${amount(row.consumed)} ${row.currency}` : dash) },
  { key: "received", header: "Received", align: "end", render: (row) => (row.posted ? `${amount(row.received)} ${row.currency}` : dash) },
  { key: "journalEntryId", header: "Journal entry", mono: true, render: (row) => row.journalEntryId ?? dash },
];

interface Filter {
  type: ListedJournalType | "";
  posted: "true" | "false" | "";
  orderNumber: string;
  from: string;
  to: string;
}

interface State {
  inquiry: JournalInquiry | null;
  loading: boolean;
  loadingMore: boolean;
  error: Problem | null;
  /** A failed "load more" keeps the rows already shown. */
  moreError: Problem | null;
}

const initial: State = { inquiry: null, loading: true, loadingMore: false, error: null, moreError: null };

const query = (f: Filter): JournalInquiryQuery => ({
  type: f.type,
  posted: f.posted,
  orderNumber: f.orderNumber.trim() || undefined,
  from: f.from || undefined,
  to: f.to || undefined,
});

/** Checks the posting date range as the service does. */
export function validateJournalFilter(f: Filter): { to?: string } {
  return f.from !== "" && f.to !== "" && f.from > f.to ? { to: "The end of the date range must not be before its start." } : {};
}

/**
 * Production journals across orders (PRD 4.21.14): picking lists, route cards and reports as
 * finished with what each consumed and received, so a cost accountant can find a posting
 * without opening orders one by one.
 */
export function JournalInquiryPage({ onOpenOrder }: JournalInquiryPageProps) {
  const [draft, setDraft] = useState<Filter>({ type: "", posted: "", orderNumber: "", from: "", to: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<{ to?: string }>({});
  const [state, setState] = useState<State>(initial);
  const latest = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    const token = ++latest.current;
    setState(initial);
    getJournalInquiry(query(filter), controller.signal).then(
      (inquiry) => token === latest.current && setState({ ...initial, inquiry, loading: false }),
      (error) => !controller.signal.aborted && setState({ ...initial, loading: false, error: describeProblem(error) }),
    );
    return () => controller.abort();
  }, [filter]);

  const apply = (event: FormEvent) => {
    event.preventDefault();
    const problems = validateJournalFilter(draft);
    setErrors(problems);
    if (Object.keys(problems).length === 0) {
      setFilter({ ...draft });
    }
  };

  const loadMore = async () => {
    const cursor = state.inquiry?.nextCursor;
    if (!cursor) {
      return;
    }
    const token = latest.current;
    setState((s) => ({ ...s, loadingMore: true, moreError: null }));
    try {
      const next = await getJournalInquiry({ ...query(filter), cursor });
      if (token !== latest.current) {
        return;
      }
      setState((s) => ({
        ...s,
        inquiry: s.inquiry ? { ...s.inquiry, items: [...s.inquiry.items, ...next.items], nextCursor: next.nextCursor } : next,
        loadingMore: false,
      }));
    } catch (error) {
      if (token === latest.current) {
        setState((s) => ({ ...s, loadingMore: false, moreError: describeProblem(error) }));
      }
    }
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Production journals"
        description="Picking lists, route cards and reports as finished across all production orders, with the value each consumed and received."
      />
      <Card>
        <form className={styles.commandForm} onSubmit={apply} noValidate aria-label="Filter the production journals">
          <FormField id="jrn-type" label="Journal type">
            {(control) => (
              <Select {...control} value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value as ListedJournalType | "" })}>
                <option value="">All types</option>
                {types.map((t) => (
                  <option key={t} value={t}>
                    {journalTypeLabel[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="jrn-posted" label="Posted">
            {(control) => (
              <Select {...control} value={draft.posted} onChange={(e) => setDraft({ ...draft, posted: e.target.value as Filter["posted"] })}>
                <option value="">Posted and drafts</option>
                <option value="true">Posted</option>
                <option value="false">Drafts</option>
              </Select>
            )}
          </FormField>
          <FormField id="jrn-order" label="Order number" optional>
            {(control) => <Input {...control} value={draft.orderNumber} onChange={(e) => setDraft({ ...draft, orderNumber: e.target.value })} />}
          </FormField>
          <FormField id="jrn-from" label="Posting date from" optional>
            {(control) => <Input {...control} type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />}
          </FormField>
          <FormField id="jrn-to" label="Posting date to" optional error={errors.to}>
            {(control) => <Input {...control} type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />}
          </FormField>
          <Button type="submit" variant="primary" loading={state.loading}>
            Show
          </Button>
        </form>
      </Card>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="The production journals are not available" live="alert">
          {state.error.status === 403
            ? "You need the View production orders duty to see the production journals."
            : `${state.error.message} (${state.error.code})`}
        </Alert>
      ) : (
        <>
          {state.inquiry ? (
            <StatGroup label="Journal count">
              <StatTile label="Journals" value={String(state.inquiry.total)} />
            </StatGroup>
          ) : null}
          <DataTable
            label="Production journals"
            columns={columns}
            rows={state.inquiry?.items ?? []}
            loading={state.loading}
            getRowKey={(row) => row.id}
            onRowOpen={onOpenOrder ? (row) => onOpenOrder(row.orderId) : undefined}
            empty={{
              title: "No production journals",
              description: "Journals appear here once picking lists, route cards or reports as finished are created on an order.",
            }}
          />
          {state.moreError ? (
            <Alert tone="danger" title="More journals could not be loaded" live="alert">
              {`${state.moreError.message} (${state.moreError.code})`}
            </Alert>
          ) : null}
          {state.inquiry?.nextCursor && !state.loading ? (
            <div className={styles.more}>
              <Button onClick={loadMore} loading={state.loadingMore}>
                Load more journals
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
