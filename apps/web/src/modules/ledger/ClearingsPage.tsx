import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { fetchClearingPage, type ClearingDocument } from "./api/client";
import { LedgerPicker, ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

export function ClearingStatusBadge({ status }: { status: ClearingDocument["status"] }) {
  return <Badge tone={status === "cleared" ? "success" : "neutral"}>{status === "cleared" ? "Cleared" : "Reset"}</Badge>;
}

const columns: DataTableColumn<ClearingDocument>[] = [
  { key: "clearingDate", header: "Clearing date", mono: true },
  { key: "account", header: "Account", mono: true },
  { key: "items", header: "Items", align: "end", render: (d) => d.itemIds.length },
  { key: "differenceAmount", header: "Written off", align: "end", mono: true },
  { key: "toleranceGroup", header: "Tolerance group", mono: true },
  { key: "residual", header: "Residual item", render: (d) => (d.residualItemId ? "Yes" : "") },
  { key: "status", header: "Status", render: (d) => <ClearingStatusBadge status={d.status} /> },
  { key: "createdBy", header: "Cleared by" },
];

/** Clearing documents of a ledger (FR-11.19), newest first. */
export function ClearingsPage() {
  const navigate = useNavigate();
  const [ledgerId] = useLedgerParam();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const result = useInfiniteQuery({
    queryKey: ["ledger", "clearings", ledgerId, status],
    queryFn: ({ pageParam }) => fetchClearingPage(ledgerId, status, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
    enabled: ledgerId !== "",
  });
  const rows = result.data?.pages.flatMap((page) => page.items) ?? [];
  const query = ledgerQuery(ledgerId);

  return (
    <div className={styles.stack}>
      <PageHeader title="Clearing documents" description="Each document groups the open items one clearing cleared. A document is reset as a whole, which opens its items again." />
      <LedgerPicker id="cl-ledger" label="Choose a ledger" ledgerId={ledgerId} onChange={(id) => setParams(id ? (status ? { ledger: id, status } : { ledger: id }) : {})} />
      {ledgerId === "" ? (
        <p className={styles.note}>Choose a ledger to see its clearing documents.</p>
      ) : (
        <>
          <Card>
            <div className={styles.form}>
              <FormField id="cl-status" label="Status">
                {(c) => (
                  <Select {...c} value={status} onChange={(e) => setParams({ ledger: ledgerId, ...(e.target.value ? { status: e.target.value } : {}) })}>
                    <option value="">All</option>
                    <option value="cleared">Cleared</option>
                    <option value="reset">Reset</option>
                  </Select>
                )}
              </FormField>
            </div>
          </Card>
          {result.isError ? (
            <Alert tone="danger" title="Could not load the clearing documents" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
              {errorText(result.error)}
            </Alert>
          ) : (
            <DataTable
              label="Clearing documents"
              columns={columns}
              rows={rows}
              getRowKey={(d) => d.id}
              loading={result.isPending}
              empty={
                status
                  ? { title: `No ${status} documents`, description: "Pick another status to see more." }
                  : { title: "Nothing cleared yet", description: "Select open items on the Open items page and clear them." }
              }
              onRowOpen={(d) => navigate(`/ledger/clearings/${d.id}${query}`)}
            />
          )}
          {result.hasNextPage ? (
            <div className={styles.formActions}>
              <Button onClick={() => void result.fetchNextPage()} loading={result.isFetchingNextPage}>
                Load more
              </Button>
            </div>
          ) : null}
          <p className={styles.note}>
            <Link to={`/ledger/open-items${query}`}>Open items</Link>
          </p>
        </>
      )}
    </div>
  );
}
