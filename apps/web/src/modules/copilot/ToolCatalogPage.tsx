import { useMemo, useState } from "react";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { APPROVAL_MODES, listTools, toolDomain, type ApprovalMode, type McpTool } from "./api/mcp";
import { approvalLabel, approvalTone } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface ToolCatalogPageProps {
  /** Injected for tests; defaults to the MCP gateway's tools/list. */
  load?: (signal: AbortSignal) => Promise<McpTool[]>;
  /** Opens one tool. The route wrapper in module.ts navigates to its detail page. */
  onOpen?: (name: string) => void;
}

const columns: DataTableColumn<McpTool>[] = [
  { key: "name", header: "Tool", mono: true },
  { key: "domain", header: "Domain", render: (t) => toolDomain(t.name) },
  { key: "description", header: "Description" },
  { key: "duty", header: "Required duty", mono: true, render: (t) => t["x-passage-duty"] },
  {
    key: "approval",
    header: "Approval",
    render: (t) => <Badge tone={approvalTone(t["x-passage-approval"])}>{approvalLabel(t["x-passage-approval"])}</Badge>,
  },
  { key: "access", header: "Access", render: (t) => (t.annotations.readOnlyHint ? "Read" : "Write") },
];

/**
 * Tool catalog inquiry (PRD 9.47.14): the MCP tools the signed-in user may call,
 * filtered by domain, duty and approval mode. The gateway lists only tools whose
 * duty the user holds (AC-47.1), so this page never shows a tool you cannot use.
 */
export function ToolCatalogPage({ load = listTools, onOpen }: ToolCatalogPageProps) {
  const state = useLoad(load, "catalog");
  const [search, setSearch] = useState("");
  const [approval, setApproval] = useState<ApprovalMode | "">("");

  const rows = useMemo(() => {
    const q = search.trim().toLowerCase();
    return (state.data ?? []).filter(
      (t) =>
        (approval === "" || t["x-passage-approval"] === approval) &&
        (q === "" || t.name.toLowerCase().includes(q) || t["x-passage-duty"].toLowerCase().includes(q)),
    );
  }, [state.data, search, approval]);

  const filtered = search.trim() !== "" || approval !== "";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Tool catalog"
        description="The Passage tools the copilot and agents can call on your behalf. You only see tools whose duty you hold; every call runs with your permissions."
      />
      <div className={styles.toolbar}>
        <FormField id="tool-search" label="Tool or duty">
          {(a11y) => <Input {...a11y} type="search" value={search} onChange={(e) => setSearch(e.target.value)} />}
        </FormField>
        <FormField id="tool-approval" label="Approval mode">
          {(a11y) => (
            <Select {...a11y} value={approval} onChange={(e) => setApproval(e.target.value as ApprovalMode | "")}>
              <option value="">All</option>
              {APPROVAL_MODES.map((m) => (
                <option key={m} value={m}>
                  {approvalLabel(m)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <Alert
        tone="danger"
        live="alert"
        title={state.error ? "We couldn't load the tool catalog" : undefined}
        action={state.error ? <Button onClick={state.reload}>Try again</Button> : undefined}
      >
        {state.error}
      </Alert>
      {state.error ? null : (
        <DataTable
          label="Tools"
          columns={columns}
          rows={rows}
          getRowKey={(t) => t.name}
          loading={state.loading}
          empty={
            filtered
              ? { title: "No tools match", description: "Try another name or duty, or clear the approval filter." }
              : { title: "No tools available to you", description: "Tools appear here once you hold the duty they require. Ask your workspace administrator for access." }
          }
          {...(onOpen ? { onRowOpen: (t: McpTool) => onOpen(t.name) } : {})}
        />
      )}
    </div>
  );
}
