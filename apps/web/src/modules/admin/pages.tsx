import { useState } from "react";
import { Link } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  CardGrid,
  DataTable,
  EmptyState,
  FormField,
  PageHeader,
  Textarea,
  type DataTableColumn,
  type IconName,
} from "../../shell/ui";
import styles from "./pages.module.css";

const adminCards = [
  { to: "/admin/audit", title: "Audit", description: "See who changed what, and when." },
  { to: "/admin/sequences", title: "Sequences", description: "Set up gap-free numbering per legal entity." },
  { to: "/admin/alerts", title: "Alerts", description: "Review failures and overdue approvals." },
  { to: "/admin/import", title: "Import / export", description: "Move records in and out as CSV." },
  {
    to: "/admin/documents",
    title: "Documents",
    description: "Check documents in and out, track versions and apply legal holds.",
  },
  { to: "/admin/texts", title: "Standard texts", description: "Reusable text in every language you support." },
];

export function AdminHome() {
  return (
    <>
      <PageHeader
        title="Admin"
        description="Numbering, audit, alerts, imports and documents in one place."
      />
      <CardGrid as="ul" label="Admin pages">
        {adminCards.map((card) => (
          <li key={card.to} className={styles.cell}>
            <Card>
              <h3 className={styles.cardTitle}>
                <Link to={card.to} className={styles.cardLink}>
                  {card.title}
                </Link>
              </h3>
              <p className={styles.cardDescription}>{card.description}</p>
            </Card>
          </li>
        ))}
      </CardGrid>
    </>
  );
}

interface AuditEntry {
  userId: string;
  occurredAt: string;
  oldValue: string;
  newValue: string;
  traceId: string;
  field: string;
}

const auditColumns: DataTableColumn<AuditEntry>[] = [
  { key: "field", header: "Field" },
  { key: "userId", header: "User" },
  {
    key: "occurredAt",
    header: "Time",
    mono: true,
    render: (row) => <time dateTime={row.occurredAt}>{row.occurredAt}</time>,
  },
  { key: "oldValue", header: "Old" },
  { key: "newValue", header: "New" },
  { key: "traceId", header: "Trace", mono: true, render: (row) => <span className={styles.muted}>{row.traceId}</span> },
];

export function AuditViewer({ items = [] }: { items?: AuditEntry[] }) {
  return (
    <>
      <PageHeader title="Audit" description="See who changed what, and when." />
      <DataTable
        label="Audit entries"
        columns={auditColumns}
        rows={items}
        getRowKey={(row) => row.traceId + row.field}
        empty={{
          title: "No audit entries yet",
          description: "Changes to records appear here with who made them and when.",
        }}
      />
    </>
  );
}

function EmptyPage({
  title,
  description,
  icon,
  emptyTitle,
  emptyDescription,
}: {
  title: string;
  description: string;
  icon: IconName;
  emptyTitle: string;
  emptyDescription: string;
}) {
  return (
    <>
      <PageHeader title={title} description={description} />
      <Card>
        <EmptyState icon={icon} title={emptyTitle} description={emptyDescription} />
      </Card>
    </>
  );
}

export function SequencePage() {
  return (
    <EmptyPage
      title="Sequences"
      description="Set up gap-free numbering per legal entity."
      icon="receipt"
      emptyTitle="No sequences set up yet"
      emptyDescription="Sequences number your documents without gaps, per legal entity."
    />
  );
}

export function AlertInbox({ items = [] }: { items?: { message: string }[] }) {
  return (
    <>
      <PageHeader title="Alerts" description="Review failures and overdue approvals." />
      {items.length === 0 ? (
        <Card>
          <EmptyState
            icon="checkCircle"
            title="No alerts right now"
            description="When a batch run fails or an approval is overdue, you'll see it here."
          />
        </Card>
      ) : (
        <ul className={styles.alerts}>
          {items.map((item) => (
            <li key={item.message}>
              <Alert tone="warning">{item.message}</Alert>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

function countColumns(line: string): number {
  let columns = 1;
  let quoted = false;
  for (const char of line) {
    if (char === '"') {
      quoted = !quoted;
    } else if (char === "," && !quoted) {
      columns += 1;
    }
  }
  return columns;
}

export type CsvCheck = { ok: true; rows: number } | { ok: false; message: string };

export function checkCsv(text: string): CsvCheck {
  const lines = text.split(/\r?\n/).filter((line) => line.trim() !== "");
  if (lines.length < 2) {
    return { ok: false, message: "Add a header row and at least one data row." };
  }
  const expected = countColumns(lines[0]);
  for (let index = 1; index < lines.length; index += 1) {
    const found = countColumns(lines[index]);
    if (found !== expected) {
      return { ok: false, message: `Row ${index + 1} has ${found} ${found === 1 ? "column" : "columns"}, expected ${expected}.` };
    }
  }
  return { ok: true, rows: lines.length - 1 };
}

export function ImportExportPage() {
  const [text, setText] = useState("title,body\nHello,World");
  const [result, setResult] = useState<CsvCheck | null>(null);
  return (
    <>
      <PageHeader title="Import / export" description="Move records in and out as CSV." />
      <Card>
        <form
          className={styles.csvForm}
          onSubmit={(event) => {
            event.preventDefault();
            setResult(checkCsv(text));
          }}
        >
          <FormField
            id="csv"
            label="CSV data"
            hint="Paste comma-separated rows. The first row names the columns."
          >
            {(control) => (
              <Textarea
                {...control}
                mono
                rows={8}
                value={text}
                onChange={(e) => {
                  setText(e.target.value);
                  setResult(null);
                }}
              />
            )}
          </FormField>
          <div>
            <Button type="submit" variant="primary">
              Validate
            </Button>
          </div>
        </form>
      </Card>
      <div className={styles.notice}>
        <Alert tone="success" live="status">
          {result?.ok ? `${result.rows} ${result.rows === 1 ? "row" : "rows"} ready to import` : null}
        </Alert>
        <Alert tone="danger" live="alert">
          {result && !result.ok ? result.message : null}
        </Alert>
      </div>
    </>
  );
}

export function DocumentsPage() {
  return (
    <EmptyPage
      title="Documents"
      description="Check documents in and out, track versions and apply legal holds."
      icon="note"
      emptyTitle="No documents yet"
      emptyDescription="Documents you attach to records appear here."
    />
  );
}

export function TextsPage() {
  return (
    <EmptyPage
      title="Standard texts"
      description="Language versions with placeholder tokens, used by templates and notes."
      icon="note"
      emptyTitle="No standard texts yet"
      emptyDescription="Standard texts you create appear here for use in templates and notes."
    />
  );
}
