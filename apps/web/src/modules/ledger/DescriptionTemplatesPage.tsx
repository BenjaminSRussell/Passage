import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchDescriptionTemplates, type DescriptionTemplate } from "./api/client";
import styles from "./pages.module.css";

/** "%1 = voucher, %2 = document_date", ordered by placeholder. */
export function bindingsLabel(bindings: Record<string, string>): string {
  return Object.keys(bindings)
    .sort()
    .map((k) => `%${k} = ${bindings[k]}`)
    .join(", ");
}

/** Display label of a template's scope. */
export function scopeLabel(t: Pick<DescriptionTemplate, "scope" | "legalEntityId">): string {
  return t.scope === "tenant" ? "All legal entities" : `Legal entity ${t.legalEntityId ?? ""}`;
}

const columns: DataTableColumn<DescriptionTemplate>[] = [
  { key: "sourceType", header: "Source type", mono: true },
  { key: "language", header: "Language", mono: true },
  {
    key: "scope",
    header: "Scope",
    render: (t) => (t.scope === "tenant" ? <Badge tone="neutral">All legal entities</Badge> : <Badge tone="info">{scopeLabel(t)}</Badge>),
  },
  { key: "template", header: "Template" },
  { key: "fieldBindings", header: "Placeholders", render: (t) => bindingsLabel(t.fieldBindings) || "None" },
];

/** Default description templates (FR-11.17): the text a posting gets when it has none. */
export function DescriptionTemplatesPage() {
  const navigate = useNavigate();
  const result = useQuery({ queryKey: ["ledger", "description-templates"], queryFn: () => fetchDescriptionTemplates() });

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Description templates"
        description="Default text for postings that arrive without a description or line text, per source type and language. A legal entity's own template wins over the one for all legal entities."
        actions={
          <Link className={buttonClassName("primary")} to="/ledger/description-templates/new">
            New template
          </Link>
        }
      />
      {result.isError ? (
        <Alert tone="danger" title="Could not load the description templates" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="Description templates"
          columns={columns}
          rows={result.data ?? []}
          getRowKey={(t) => t.id}
          loading={result.isPending}
          empty={{ title: "No description templates yet", description: "Postings without a description stay blank until you add a template for their source type." }}
          onRowOpen={(t) => navigate(`/ledger/description-templates/${encodeURIComponent(t.id)}`)}
        />
      )}
      <p className={styles.note}>Explicit descriptions and line texts are never replaced. Text longer than 255 characters is cut and the posting reports it.</p>
    </div>
  );
}
