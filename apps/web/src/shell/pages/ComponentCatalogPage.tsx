import { useState, type ReactNode } from "react";
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  standardStatuses,
  type BadgeTone,
} from "../ui";
import styles from "./catalog.module.css";

/**
 * The living component catalog at /design-system. Every shared component is rendered here from the real code, next
 * to the rule for using it, so a change to a component shows up in the catalog without a second copy to update.
 * Pages use these components; a page that builds its own version of one of them is a bug to fix.
 */

interface Entry {
  id: string;
  name: string;
  rule: string;
  example: ReactNode;
}

const TONES: BadgeTone[] = ["neutral", "info", "success", "warning", "danger"];

function ConfirmExample() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="danger" onClick={() => setOpen(true)}>
        Cancel requisition
      </Button>
      {open ? (
        <ConfirmDialog
          title="Cancel this requisition?"
          description="A cancelled requisition cannot be reopened."
          confirmLabel="Cancel requisition"
          cancelLabel="Keep it"
          onConfirm={() => setOpen(false)}
          onCancel={() => setOpen(false)}
        />
      ) : null}
    </>
  );
}

export const catalogEntries: Entry[] = [
  {
    id: "page-header",
    name: "Page header",
    rule: "One per page, below the top bar title. Title states what the page is, the description says what it is for, and the single primary action goes on the right.",
    example: <PageHeader title="Requisitions" description="Requests to buy goods and services." actions={<Button variant="primary">New requisition</Button>} />,
  },
  {
    id: "button",
    name: "Button",
    rule: "One primary button per view. Secondary for other actions, ghost for low-emphasis ones, danger only for actions that destroy or cancel something and always behind a confirmation dialog.",
    example: (
      <div className={styles.catalogRow}>
        <Button variant="primary">Save</Button>
        <Button>Secondary</Button>
        <Button variant="ghost">Ghost</Button>
        <Button variant="danger">Delete</Button>
        <Button loading>Saving</Button>
      </div>
    ),
  },
  {
    id: "form-field",
    name: "Form field",
    rule: "Every control sits in a FormField: a visible label, an optional hint, and an error that names the problem and how to fix it. Mark optional fields as Optional, not required ones with an asterisk.",
    example: (
      <div className={styles.catalogRow}>
        <FormField id="cat-name" label="Name" hint="As it appears on invoices.">
          {(control) => <Input {...control} defaultValue="Cedar Ridge Pumps" />}
        </FormField>
        <FormField id="cat-status" label="Status" optional>
          {(control) => (
            <Select {...control} defaultValue="">
              <option value="">All statuses</option>
              <option value="draft">Draft</option>
            </Select>
          )}
        </FormField>
        <FormField id="cat-reason" label="Reason" error="Enter a reason, for example a wrong quantity.">
          {(control) => <Textarea {...control} defaultValue="" />}
        </FormField>
      </div>
    ),
  },
  {
    id: "data-table",
    name: "Table",
    rule: "Lists of records use DataTable (or ListPage for filtered lists). It has a label, sortable headers with aria-sort, a loading skeleton and an empty state. Numbers align to the end.",
    example: (
      <DataTable<{ number: string; status: string; total: string }>
        label="Example orders"
        columns={[
          { key: "number", header: "Number", mono: true },
          { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
          { key: "total", header: "Total", align: "end" },
        ]}
        rows={[
          { number: "PO-1001", status: "approved", total: "1,200.00" },
          { number: "PO-1002", status: "on_hold", total: "640.50" },
        ]}
        getRowKey={(row) => row.number}
      />
    ),
  },
  {
    id: "status",
    name: "Status badge",
    rule: "Statuses use StatusPill, which takes the tone and label from the status standard below. Never choose a colour in a page, and never show a status by colour alone: the label is always text.",
    example: (
      <div className={styles.catalogRow}>
        {TONES.map((tone) => (
          <Badge key={tone} tone={tone}>
            {tone}
          </Badge>
        ))}
      </div>
    ),
  },
  {
    id: "alert",
    name: "Alert",
    rule: "Alerts explain a result or a problem on the page. Use live=\"alert\" only for errors that appear after the user acts; the icon and the title carry the tone as well as the colour.",
    example: (
      <div className={styles.catalogStack}>
        <Alert tone="info" title="Prices were updated">The estimates now use the trade agreement price.</Alert>
        <Alert tone="danger" title="Could not save">The vendor is blocked for purchasing.</Alert>
      </div>
    ),
  },
  {
    id: "dialog",
    name: "Dialog and confirmation",
    rule: "Destructive actions ask first with ConfirmDialog: the title is a question, the confirm button repeats the action, Escape and Keep it cancel. Focus moves into the dialog and returns to the button that opened it.",
    example: <ConfirmExample />,
  },
  {
    id: "empty-state",
    name: "Empty state",
    rule: "A list with nothing to show says why and what to do next. Never leave a blank table.",
    example: <EmptyState icon="inbox" title="No requisitions yet" description="Requests you create appear here." />,
  },
  {
    id: "skeleton",
    name: "Loading skeleton",
    rule: "Loading is shown with skeletons shaped like the content, not a bare Loading text. Skeletons are hidden from screen readers; the page announces the loaded state.",
    example: (
      <div className={styles.catalogStack}>
        <Skeleton height="tile" />
        <Skeleton width="3/4" />
      </div>
    ),
  },
  {
    id: "stat-tile",
    name: "Stat tile",
    rule: "A few headline counts at the top of a workspace. A tone only shows when the count is above zero.",
    example: (
      <StatGroup label="Example counts">
        <StatTile label="Waiting for approval" value={3} tone="warning" />
        <StatTile label="Overdue" value={0} tone="danger" />
      </StatGroup>
    ),
  },
  {
    id: "card",
    name: "Card",
    rule: "Groups related content under a heading. Do not nest cards.",
    example: <Card title="Details">Cards hold a heading and its content.</Card>,
  },
];

export function ComponentCatalogPage() {
  const statuses = standardStatuses();
  return (
    <>
      <PageHeader
        title="Component catalog"
        description="The shared components, rendered from the real code, with the rule for using each one."
      />
      <nav aria-label="Components" className={styles.catalogNav}>
        <ul>
          {catalogEntries.map((entry) => (
            <li key={entry.id}>
              <a href={`#${entry.id}`}>{entry.name}</a>
            </li>
          ))}
          <li>
            <a href="#status-standard">Status standard</a>
          </li>
        </ul>
      </nav>
      {catalogEntries.map((entry) => (
        <section key={entry.id} id={entry.id} className={styles.catalogSection}>
          <Card title={entry.name} headingLevel={2}>
            <p>{entry.rule}</p>
            <div className={styles.catalogExample}>{entry.example}</div>
          </Card>
        </section>
      ))}
      <section id="status-standard" className={styles.catalogSection}>
        <Card title="Status standard" headingLevel={2}>
          <p>
            Neutral means nothing to do, info means in progress, success means done or in force, warning means a person should look, and danger means it failed or was refused. Add new statuses to
            shell/ui/status.ts, not to a page.
          </p>
          <ul className={styles.catalogStatuses}>
            {statuses.map((s) => (
              <li key={s.status}>
                <StatusPill status={s.status} />
              </li>
            ))}
          </ul>
        </Card>
      </section>
    </>
  );
}
