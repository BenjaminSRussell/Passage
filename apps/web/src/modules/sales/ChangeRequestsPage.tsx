import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { CHANGE_STATUSES, listChangeRequests, type ChangeRequest, type ChangeRequestFilter, type ChangeStatus } from "./api/client";
import { formatDateTime, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const tone: Record<ChangeStatus, BadgeTone> = { PENDING: "warning", APPROVED: "success", REJECTED: "danger", WITHDRAWN: "neutral" };

export function ChangeStatusBadge({ status }: { status: ChangeStatus }) {
  return <Badge tone={tone[status]}>{sentence(status)}</Badge>;
}

/** A short summary of what a change touches: "2 fields on line 1". */
export function diffSummary(request: Pick<ChangeRequest, "diff"> & { kind?: ChangeRequest["kind"] }): string {
  if (request.kind === "CANCEL") return "Cancel the order";
  const n = request.diff.length;
  const lines = new Set(request.diff.filter((d) => d.lineNo > 0).map((d) => d.lineNo));
  const where = lines.size > 0 ? ` on ${lines.size === 1 ? `line ${[...lines][0]}` : `${lines.size} lines`}` : "";
  return `${n} ${n === 1 ? "field" : "fields"}${where}`;
}

const columns: DataTableColumn<ChangeRequest>[] = [
  { key: "orderNumber", header: "Order", mono: true, render: (r) => <Link to={`/sales/orders/${r.orderId}`}>{r.orderNumber}</Link> },
  { key: "changes", header: "Change", render: (r) => <Link to={`/sales/change-requests/${r.id}`}>{diffSummary(r)}</Link> },
  { key: "reason", header: "Reason", render: (r) => r.reason || "—" },
  { key: "requestedBy", header: "Requested", render: (r) => `${formatDateTime(r.requestedAt)} by ${r.requestedBy}` },
  { key: "status", header: "Status", render: (r) => <ChangeStatusBadge status={r.status} /> },
];

/**
 * Change approval queue (FR-27.9, PRD §5.27.14 backorders / change requests):
 * changes to confirmed orders of customers that need approval, oldest pending
 * first. A sales manager opens one to compare the diff and approve or reject.
 */
export function ChangeRequestsPage() {
  const [draft, setDraft] = useState<{ legalEntityId: string; status: ChangeStatus | "" }>({ legalEntityId: "", status: "PENDING" });
  const [filter, setFilter] = useState<ChangeRequestFilter>({ status: "PENDING" });
  const invalid = draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim()) ? "Enter a legal entity UUID." : undefined;
  const state = useLoad((signal) => listChangeRequests(filter, signal), JSON.stringify(filter));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Change requests"
        description="Changes to confirmed orders that wait for a sales manager's approval, oldest first. Nothing changes on the order until a request is approved."
        actions={
          <Link className={buttonClassName("ghost")} to="/sales/change-policy">
            Customer change approval
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (!invalid) setFilter({ legalEntityId: draft.legalEntityId.trim(), status: draft.status });
        }}
      >
        <FormField id="cr-le" label="Legal entity" error={invalid} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid)} />}
        </FormField>
        <FormField id="cr-status" label="Status">
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as ChangeStatus | "" })}>
              <option value="">Any status</option>
              {CHANGE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load the change requests" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Change requests"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(r) => r.id}
          loading={state.loading}
          empty={{ title: "No change requests", description: "A change of a confirmed order by a customer that needs approval appears here until it is decided." }}
        />
      )}
    </div>
  );
}
