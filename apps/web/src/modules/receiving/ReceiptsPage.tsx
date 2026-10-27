import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { fetchReceipts, RECEIPT_STATES, type Receipt, type ReceiptState } from "./api/client";
import { StateBadge } from "./parts";
import styles from "./pages.module.css";

const columns: DataTableColumn<Receipt>[] = [
  { key: "receiptNumber", header: "Receipt", mono: true },
  { key: "packingSlip", header: "Packing slip" },
  { key: "vendorId", header: "Vendor", mono: true },
  { key: "receiptDate", header: "Receipt date" },
  { key: "currency", header: "Currency" },
  { key: "state", header: "State", render: (r) => <StateBadge state={r.state} /> },
];

/** Receipts newest first, filtered by state, paged by the service's cursor. */
export function ReceiptsPage() {
  const navigate = useNavigate();
  const [state, setState] = useState<ReceiptState | "">("");
  const result = useInfiniteQuery({
    queryKey: ["receiving", "receipts", state],
    queryFn: ({ pageParam }) => fetchReceipts({ state: state || undefined, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const rows = result.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Receipts"
        description="Deliveries from arrival at the dock to posting. Open a receipt to register what was received, post it to the ledger, correct it or cancel it."
        actions={
          <Link className={buttonClassName("primary")} to="/receiving/receipts/new">
            Record arrival
          </Link>
        }
      />
      <div className={styles.filters}>
        <FormField id="receipt-state" label="State" optional>
          {(c) => (
            <Select {...c} value={state} onChange={(e) => setState(e.target.value as ReceiptState | "")}>
              <option value="">All states</option>
              {RECEIPT_STATES.map((s) => (
                <option key={s} value={s}>
                  {s.charAt(0) + s.slice(1).toLowerCase()}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {result.isError ? (
        <Alert tone="danger" title="Could not load the receipts" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="Receipts"
          columns={columns}
          rows={rows}
          getRowKey={(r) => r.id}
          loading={result.isPending}
          empty={{ title: "No receipts yet", description: "Record an arrival when a delivery reaches the dock." }}
          onRowOpen={(r) => navigate(`/receiving/receipts/${encodeURIComponent(r.id)}`)}
        />
      )}
      {result.hasNextPage ? (
        <div>
          <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
            Load more receipts
          </Button>
        </div>
      ) : null}
    </div>
  );
}
