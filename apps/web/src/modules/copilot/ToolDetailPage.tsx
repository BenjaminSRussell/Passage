import { Alert, Badge, Button, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { inputFields, listTools, toolDomain, type InputField, type McpTool } from "./api/mcp";
import { approvalLabel, approvalTone } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface ToolDetailPageProps {
  name: string;
  load?: (signal: AbortSignal) => Promise<McpTool[]>;
}

const fieldColumns: DataTableColumn<InputField>[] = [
  { key: "name", header: "Argument", mono: true },
  { key: "type", header: "Type", mono: true },
  { key: "required", header: "Required", render: (f) => (f.required ? "Required" : "Optional") },
  { key: "description", header: "Description" },
];

/** One MCP tool: its duty, approval mode, behaviour hints and arguments (FR-47.1). */
export function ToolDetailPage({ name, load = listTools }: ToolDetailPageProps) {
  const state = useLoad(load, name);
  const tool = state.data?.find((t) => t.name === name);

  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={name} />
        <div className={styles.loading}>
          <span className={styles.visuallyHidden}>Loading tool…</span>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
          <Skeleton height="tile" />
        </div>
      </div>
    );
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title={name} />
        <Alert tone="danger" live="alert" title="We couldn't load this tool" action={<Button onClick={state.reload}>Try again</Button>}>
          {state.error}
        </Alert>
      </div>
    );
  }

  if (!tool) {
    return (
      <div className={styles.stack}>
        <PageHeader title={name} />
        <Card>
          <EmptyState
            icon="lock"
            title="This tool isn't available to you"
            description="It doesn't exist, or you don't hold the duty it requires. Ask your workspace administrator for access."
          />
        </Card>
      </div>
    );
  }

  const approval = tool["x-passage-approval"];
  return (
    <div className={styles.stack}>
      <PageHeader title={tool.name} description={tool.description} meta={<Badge tone={approvalTone(approval)}>{approvalLabel(approval)}</Badge>} />
      <Card title="Governance">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Domain</dt>
            <dd className={styles.value}>{toolDomain(tool.name)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Required duty</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{tool["x-passage-duty"]}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Approval</dt>
            <dd className={styles.value}>{approvalLabel(approval)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Access</dt>
            <dd className={styles.value}>{tool.annotations.readOnlyHint ? "Read only" : "Changes data"}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Safe to retry</dt>
            <dd className={styles.value}>{tool.annotations.idempotentHint ? "Yes" : "No"}</dd>
          </div>
        </dl>
      </Card>
      <Card title="Arguments">
        <DataTable
          label="Arguments"
          columns={fieldColumns}
          rows={inputFields(tool.inputSchema)}
          getRowKey={(f) => f.name}
          empty={{ title: "No arguments", description: "This tool takes no input. Your tenant always comes from your sign-in." }}
        />
      </Card>
    </div>
  );
}
