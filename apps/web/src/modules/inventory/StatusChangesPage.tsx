import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listStatusChanges, STATUS_COMMANDS, type Page, type Problem, type StatusChange, type StatusCommand } from "./api/client";
import { dash, formatDateTime, sentence, shortId, UUID } from "./format";
import { ProblemAlert } from "./parts";
import styles from "./pages.module.css";
import { commandLabels } from "./TransactionPage";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<StatusChange>[] = [
  { key: "occurredAt", header: "When", mono: true, render: (row) => formatDateTime(row.occurredAt) },
  {
    key: "transactionId",
    header: "Transaction",
    mono: true,
    render: (row) => <Link to={`/inventory/transactions/${row.transactionId}`}>{shortId(row.transactionId)}</Link>,
  },
  { key: "command", header: "Command", render: (row) => commandLabels[row.command] ?? sentence(row.command) },
  { key: "fromStatus", header: "From", render: (row) => (row.fromStatus ? sentence(row.fromStatus) : dash) },
  { key: "toStatus", header: "To", render: (row) => sentence(row.toStatus) },
  { key: "qty", header: "Quantity", align: "end", mono: true },
  { key: "date", header: "Date", mono: true },
];

interface Filters {
  transaction: string;
  command: StatusCommand | "";
}

const noFilters: Filters = { transaction: "", command: "" };

/** Status-change inquiry (FR-16.1, BR-16.1): every registration and status move, newest first. */
export function StatusChangesPage() {
  const [draft, setDraft] = useState<Filters>(noFilters);
  const [filters, setFilters] = useState<Filters>(noFilters);
  const [error, setError] = useState("");
  const key = JSON.stringify(filters);
  const state = useLoad((signal) => listStatusChanges(filters, signal), key);
  const [more, setMore] = useState<{ key: string; page: Page<StatusChange> } | null>(null);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  const extra = more && more.key === key ? more.page : null;
  const rows = [...(state.data?.items ?? []), ...(extra?.items ?? [])];
  const nextCursor = extra ? extra.nextCursor : state.data?.nextCursor;

  function apply(event: FormEvent) {
    event.preventDefault();
    const transaction = draft.transaction.trim();
    const invalid = transaction !== "" && !UUID.test(transaction) ? "Enter the transaction id (a UUID)." : "";
    setError(invalid);
    if (!invalid) setFilters({ ...draft, transaction });
  }

  async function loadMore() {
    if (!nextCursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listStatusChanges({ ...filters, cursor: nextCursor });
      setMore({ key, page: { items: [...(extra?.items ?? []), ...page.items], nextCursor: page.nextCursor } });
    } catch (failure) {
      setMoreError(describeProblem(failure));
    } finally {
      setLoadingMore(false);
    }
  }

  const failure = state.error ?? moreError;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Status changes"
        description="The audit of every transaction registered by another service and every status move, including unpick, unregister and cancelled reservations."
      />
      <Card title="Filter">
        <form onSubmit={apply} noValidate className={styles.form}>
          <FormField id="sc-transaction" label="Transaction" optional error={error || undefined}>
            {(control) => (
              <Input
                {...control}
                value={draft.transaction}
                onChange={(event) => setDraft({ ...draft, transaction: event.target.value })}
                placeholder="Transaction id"
                invalid={Boolean(error)}
              />
            )}
          </FormField>
          <FormField id="sc-command" label="Command" optional>
            {(control) => (
              <Select {...control} value={draft.command} onChange={(event) => setDraft({ ...draft, command: event.target.value as StatusCommand | "" })}>
                <option value="">Any command</option>
                {STATUS_COMMANDS.map((c) => (
                  <option key={c} value={c}>
                    {commandLabels[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filters
            </Button>
            <Button
              onClick={() => {
                setError("");
                setDraft(noFilters);
                setFilters(noFilters);
              }}
            >
              Clear filters
            </Button>
          </div>
        </form>
      </Card>
      {failure ? <ProblemAlert title="Could not load status changes" problem={failure} onRetry={state.reload} /> : null}
      {state.error ? null : (
        <DataTable
          label="Status changes"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: "No status changes match",
            description: "Status changes appear when purchasing, receiving, sales or production register a transaction, or a user unpicks, unregisters or cancels a reservation.",
          }}
        />
      )}
      {nextCursor && !state.loading ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more status changes
          </Button>
        </div>
      ) : null}
    </div>
  );
}
