import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { fetchRecurringJournalPage, type RecurringJournal } from "./api/client";
import styles from "./pages.module.css";

/** "Last day" for day 0, otherwise "day 15". */
export function dayLabel(dayOfMonth: number): string {
  return dayOfMonth === 0 ? "Last day" : `Day ${dayOfMonth}`;
}

export function RecurringStatusBadge({ status }: { status: RecurringJournal["status"] }) {
  return <Badge tone={status === "active" ? "success" : "neutral"}>{status === "active" ? "Active" : "Paused"}</Badge>;
}

const columns: DataTableColumn<RecurringJournal>[] = [
  { key: "name", header: "Name" },
  { key: "journalName", header: "Journal", mono: true },
  { key: "frequency", header: "Schedule", render: (r) => `${r.frequency[0].toUpperCase()}${r.frequency.slice(1)}, ${dayLabel(r.dayOfMonth).toLowerCase()}` },
  { key: "nextRunDate", header: "Next run", render: (r) => r.nextRunDate ?? (r.status === "paused" ? "Paused" : "Finished") },
  { key: "lastRunDate", header: "Last run", render: (r) => r.lastRunDate ?? "Never" },
  { key: "status", header: "Status", render: (r) => <RecurringStatusBadge status={r.status} /> },
  { key: "lineCount", header: "Lines", align: "end" },
];

/** Recurring journals (FR-11.10): templates with a calendar schedule that generate draft batches. */
export function RecurringJournalsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const result = useInfiniteQuery({
    queryKey: ["ledger", "recurring-journals", status],
    queryFn: ({ pageParam }) => fetchRecurringJournalPage(status, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const rows = result.data?.pages.flatMap((page) => page.items) ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Recurring journals"
        description="Templates of journal lines on a monthly, quarterly or yearly schedule. Each run creates a draft batch that goes through the journal name's approval before it posts."
        actions={
          <Link className={buttonClassName("primary")} to="/ledger/recurring-journals/new">
            New recurring journal
          </Link>
        }
      />
      <Card>
        <div className={styles.form}>
          <FormField id="rj-status" label="Status">
            {(c) => (
              <Select {...c} value={status} onChange={(e) => setParams(e.target.value ? { status: e.target.value } : {})}>
                <option value="">All</option>
                <option value="active">Active</option>
                <option value="paused">Paused</option>
              </Select>
            )}
          </FormField>
        </div>
      </Card>
      {result.isError ? (
        <Alert tone="danger" title="Could not load the recurring journals" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="Recurring journals"
          columns={columns}
          rows={rows}
          getRowKey={(r) => r.id}
          loading={result.isPending}
          empty={
            status
              ? { title: `No ${status} recurring journals`, description: "Pick another status to see more." }
              : { title: "No recurring journals yet", description: "Create one for entries you post every period, such as rent, depreciation or accruals." }
          }
          onRowOpen={(r) => navigate(`/ledger/recurring-journals/${r.id}`)}
        />
      )}
      {result.hasNextPage ? (
        <div className={styles.formActions}>
          <Button onClick={() => void result.fetchNextPage()} loading={result.isFetchingNextPage}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
