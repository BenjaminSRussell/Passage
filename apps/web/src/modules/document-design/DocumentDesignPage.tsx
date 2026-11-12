import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Badge, Button, Card, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { EntityName } from "../../shell/lookup/EntityName";
import { ProblemMessage } from "../../shell/errors";
import {
  CHANNEL_LABELS,
  describeProblem,
  DOCUMENT_TYPE_LABELS,
  listOutputRules,
  listTemplates,
  listTextBlocks,
  type DocumentTemplate,
  type OutputRule,
  type Problem,
  type TextBlock,
} from "./api";
import styles from "./DocumentDesignPage.module.css";

const typeLabel = (t: string) => DOCUMENT_TYPE_LABELS[t] ?? t;

function useLoad<T>(load: (signal: AbortSignal) => Promise<T[]>) {
  const [state, setState] = useState<{ data: T[] | null; error: Problem | null }>({ data: null, error: null });
  const loader = useRef(load);
  loader.current = load;
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setState((s) => ({ data: s.data, error: null }));
    loader.current(controller.signal).then(
      (data) => setState({ data, error: null }),
      (failure: unknown) => {
        if (!controller.signal.aborted) setState({ data: null, error: describeProblem(failure) });
      },
    );
    return () => controller.abort();
  }, [attempt]);
  return { ...state, loading: state.data === null && state.error === null, reload: useCallback(() => setAttempt((n) => n + 1), []) };
}

const templateColumns: DataTableColumn<DocumentTemplate>[] = [
  { key: "name", header: "Template" },
  { key: "documentType", header: "Document", render: (t) => typeLabel(t.documentType) },
  { key: "default", header: "Used", render: (t) => (t.default ? <Badge tone="success">Default for this document</Badge> : "When named") },
  { key: "title", header: "Title", render: (t) => t.config.title ?? "Standard" },
  {
    key: "changes",
    header: "Changes",
    render: (t) => {
      const parts = [
        t.config.hideColumns?.length ? `${t.config.hideColumns.length} hidden columns` : "",
        t.config.renameColumns && Object.keys(t.config.renameColumns).length ? `${Object.keys(t.config.renameColumns).length} renamed columns` : "",
        t.config.noteBlocks?.length ? `${t.config.noteBlocks.length} form notes` : "",
        t.config.bodyBlock ? "letter text" : "",
        t.config.footerText ? "footer line" : "",
      ].filter(Boolean);
      return parts.length ? parts.join(", ") : "None";
    },
  },
  { key: "version", header: "Version", align: "end", render: (t) => String(t.version) },
];

const blockColumns: DataTableColumn<TextBlock>[] = [
  { key: "code", header: "Text block", mono: true },
  { key: "language", header: "Language", mono: true },
  { key: "body", header: "Text", render: (b) => (b.body.length > 90 ? `${b.body.slice(0, 90)}…` : b.body) },
  { key: "placeholders", header: "Fills in", render: (b) => (b.placeholders.length ? b.placeholders.join(", ") : "Nothing") },
  { key: "version", header: "Version", align: "end", render: (b) => String(b.version) },
];

const ruleColumns: DataTableColumn<OutputRule>[] = [
  { key: "documentType", header: "Document", render: (r) => typeLabel(r.documentType) },
  {
    key: "scope",
    header: "Applies to",
    render: (r) =>
      r.legalEntityId || r.accountId ? (
        <span>
          {r.legalEntityId ? <EntityName entity="legalEntity" id={r.legalEntityId} fallback="A legal entity" /> : null}
          {r.legalEntityId && r.accountId ? " and " : null}
          {r.accountId ? <EntityName entity="party" id={r.accountId} fallback="An account" /> : null}
        </span>
      ) : (
        "Every legal entity and account"
      ),
  },
  { key: "copyKind", header: "Copy", render: (r) => (r.copyKind === "ORIGINAL" ? "Original" : "Copy") },
  { key: "channel", header: "Goes to", render: (r) => CHANNEL_LABELS[r.channel] ?? r.channel },
  { key: "destination", header: "Destination", render: (r) => (r.destination ? `${r.destination}${r.copies > 1 ? ` (${r.copies} copies)` : ""}` : "None needed") },
  { key: "sequence", header: "Sequence", align: "end", render: (r) => String(r.sequence) },
];

function Section<T>({
  title,
  description,
  label,
  columns,
  state,
  getRowKey,
  empty,
}: {
  title: string;
  description: string;
  label: string;
  columns: DataTableColumn<T>[];
  state: ReturnType<typeof useLoad<T>>;
  getRowKey: (row: T) => string;
  empty: { title: string; description: string };
}) {
  return (
    <Card title={title}>
      <p className={styles.muted}>{description}</p>
      {state.error ? (
        <Alert tone="danger" title={`Could not load ${label.toLowerCase()}`} live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable label={label} columns={columns} rows={state.data ?? []} getRowKey={getRowKey} loading={state.loading} empty={empty} />
      )}
    </Card>
  );
}

/** Document templates, standard text blocks and the output rules of the print service (issue #46). */
export function DocumentDesignPage() {
  const templates = useLoad(listTemplates);
  const blocks = useLoad(listTextBlocks);
  const rules = useLoad(listOutputRules);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Document design"
        description="How purchase orders, invoices, packing slips, remittance advices and letters look, which standard texts they use, and where the original and the copies of each document go."
      />
      <Section
        title="Document templates"
        description="A template changes the title, footer line, column names and visible columns of a document type, and adds form notes or the letter text from the text blocks. The default template of a document type is used when a document names none."
        label="Document templates"
        columns={templateColumns}
        state={templates}
        getRowKey={(t) => t.code}
        empty={{ title: "No templates yet", description: "Documents print with the standard layout until a template is saved." }}
      />
      <Section
        title="Standard text blocks"
        description="Texts with placeholders such as {{dueDate}}, kept per language. A document uses the language it asks for, then the language without its region, then English."
        label="Text blocks"
        columns={blockColumns}
        state={blocks}
        getRowKey={(b) => `${b.code}/${b.language}`}
        empty={{ title: "No text blocks yet", description: "Add a text block to give letters and form notes their wording." }}
      />
      <Section
        title="Output rules"
        description="Where each document goes: a printer queue, an e-mail address, the e-invoice network, the archive or the portal, as the original or as a copy. The most specific rule wins: legal entity and account, then account, then legal entity, then the default."
        label="Output rules"
        columns={ruleColumns}
        state={rules}
        getRowKey={(r) => r.id}
        empty={{ title: "No output rules yet", description: "Without a rule a document goes nowhere on its own; the screen that raised it still offers to download the PDF." }}
      />
    </div>
  );
}
