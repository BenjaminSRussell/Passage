import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listColumnSets, listColumnTemplates, listRowSets, listTrees, type ColumnSet, type RowSet, type Tree } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const rowSetColumns: DataTableColumn<RowSet>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "rows", header: "Rows", mono: true, align: "end", render: (s) => String(s.rows.length) },
  { key: "updatedBy", header: "Last edited by", mono: true, render: (s) => s.updatedBy ?? "—" },
];

const columnSetColumns: DataTableColumn<ColumnSet>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "columns", header: "Columns", mono: true, align: "end", render: (s) => String(s.columns.length) },
  { key: "periods", header: "Periods", mono: true, render: (s) => s.columns.map((c) => c.periodExpr).join(" ") },
];

const treeColumns: DataTableColumn<Tree>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "attribute", header: "Dimension", mono: true },
  { key: "units", header: "Units", mono: true, align: "end", render: (t) => String(t.units.length) },
  { key: "updatedBy", header: "Last edited by", mono: true, render: (t) => t.updatedBy ?? "—" },
];

/** Reporting trees (FR-15.4): reporting units with roll-up that a statement run can be limited to. */
export function TreesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listTrees(signal), "trees");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Reporting trees"
        description="Reporting units, such as profit centers grouped by region. Run a statement for one unit and its roll-up."
        actions={
          <Link className={buttonClassName("primary")} to="/finreport/trees/new">
            New tree
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the trees" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Reporting trees"
          columns={treeColumns}
          rows={state.data ?? []}
          getRowKey={(t) => t.code}
          loading={state.loading}
          empty={{ title: "No reporting trees yet", description: "Create a tree to run statements per profit center, segment or other dimension value." }}
          onRowOpen={(t) => navigate(`/finreport/trees/${encodeURIComponent(t.code)}`)}
        />
      )}
    </div>
  );
}

/** Row sets (FR-15.2): named lists of rows that report definitions start from. */
export function RowSetsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listRowSets(signal), "row-sets");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Row sets"
        description="Reusable lists of statement rows. A report definition can start from one, so the same layout is kept in one place."
        actions={
          <Link className={buttonClassName("primary")} to="/finreport/row-sets/new">
            New row set
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the row sets" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Row sets"
          columns={rowSetColumns}
          rows={state.data ?? []}
          getRowKey={(s) => s.code}
          loading={state.loading}
          empty={{ title: "No row sets yet", description: "Create a row set for the rows several statements share, such as an income statement layout." }}
          onRowOpen={(s) => navigate(`/finreport/row-sets/${encodeURIComponent(s.code)}`)}
        />
      )}
    </div>
  );
}

/** Column sets (FR-15.3) and the comparative templates the service ships. */
export function ColumnSetsPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listColumnSets(signal), "column-sets");
  const templates = useLoad((signal) => listColumnTemplates(signal), "column-templates");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Column sets"
        description="Reusable lists of statement columns, such as the period against the same period a year earlier with its variance."
        actions={
          <Link className={buttonClassName("primary")} to="/finreport/column-sets/new">
            New column set
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the column sets" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Column sets"
          columns={columnSetColumns}
          rows={state.data ?? []}
          getRowKey={(s) => s.code}
          loading={state.loading}
          empty={{ title: "No column sets yet", description: "Create one, or start from a comparative template below." }}
          onRowOpen={(s) => navigate(`/finreport/column-sets/${encodeURIComponent(s.code)}`)}
        />
      )}
      <Card title="Comparative templates">
        <p className={styles.note}>Shipped with the service. Copy one into a column set of your own to change it.</p>
        {templates.error ? (
          <Alert tone="danger" title="Could not load the templates" live="alert" action={<Button size="sm" onClick={templates.reload}>Try again</Button>}>
            {templates.error.message} ({templates.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Comparative templates"
            columns={[
              ...columnSetColumns,
              {
                key: "use",
                header: "Use",
                render: (s) => (
                  <Link to={`/finreport/column-sets/new?template=${encodeURIComponent(s.code)}`} aria-label={`Copy template ${s.code}`}>
                    Copy
                  </Link>
                ),
              },
            ]}
            rows={templates.data ?? []}
            getRowKey={(s) => s.code}
            loading={templates.loading}
            empty={{ title: "No templates", description: "The service ships none." }}
          />
        )}
        <p className={styles.note}>
          <Badge tone="info">Template</Badge> variance columns subtract the comparative from the current column; the percent divides by the comparative as shown, is blank when it is zero and changes sign when the comparative is below zero.
        </p>
      </Card>
    </div>
  );
}
