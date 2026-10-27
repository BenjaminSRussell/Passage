import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import { listConditionRecords, type ConditionRecord } from "./api/client";
import { addDays, describeCustomer, describeProduct, today, TYPE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const EXPIRY_WINDOW_DAYS = 30;
const PAGE = 100;

interface Overview {
  active: ConditionRecord[];
  drafts: ConditionRecord[];
}

/** The count, with "+" when the list hit the page size and there may be more. */
function count(rows: ConditionRecord[]): string {
  return rows.length >= PAGE ? `${PAGE}+` : String(rows.length);
}

const expiringColumns: DataTableColumn<ConditionRecord>[] = [
  { key: "type", header: "Type", render: (row) => TYPE_LABELS[row.type] },
  { key: "customerKey", header: "Customer", render: describeCustomer },
  { key: "productKey", header: "Product", mono: true, render: describeProduct },
  { key: "validTo", header: "Valid to", mono: true },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status.toLowerCase()} /> },
];

/** Pricing workspace (PRD §5.26.6): what needs attention in the trade agreements. */
export function WorkspacePage() {
  const navigate = useNavigate();
  const day = today();
  const state = useLoad<Overview>(async (signal) => {
    const [active, drafts] = await Promise.all([
      listConditionRecords({ status: "ACTIVE", limit: PAGE }, signal),
      listConditionRecords({ status: "DRAFT", limit: PAGE }, signal),
    ]);
    return { active, drafts };
  }, day);
  const horizon = addDays(day, EXPIRY_WINDOW_DAYS);
  const expiring = (state.data?.active ?? []).filter((r) => r.validTo >= day && r.validTo <= horizon).sort((a, b) => a.validTo.localeCompare(b.validTo));

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Pricing workspace"
        description="Trade agreements that are live, waiting for activation or about to expire."
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("primary")} to="/pricing/agreements/new">
              New agreement
            </Link>
            <Link className={buttonClassName("secondary")} to="/pricing/simulate">
              Price simulation
            </Link>
          </div>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the trade agreements" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          <StatGroup label="Pricing tiles">
            <StatTile label="Active agreements" value={state.data ? count(state.data.active) : "…"} />
            <StatTile label="Drafts to activate" value={state.data ? count(state.data.drafts) : "…"} tone="warning" />
            <StatTile label={`Expiring in ${EXPIRY_WINDOW_DAYS} days`} value={state.data ? expiring.length : "…"} tone="warning" />
          </StatGroup>
          <Card title="Expiring soon">
            <DataTable
              label="Expiring agreements"
              columns={expiringColumns}
              rows={expiring}
              getRowKey={(row) => row.id}
              loading={state.loading}
              empty={{ title: "Nothing expires in the next 30 days", description: "Active agreements whose validity ends soon appear here." }}
              onRowOpen={(row) => navigate(`/pricing/agreements/${row.id}`)}
            />
          </Card>
        </>
      )}
    </div>
  );
}
