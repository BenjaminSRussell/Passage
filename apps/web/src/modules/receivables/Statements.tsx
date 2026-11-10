import { useState } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  fetchStatement,
  listBalanceSnapshots,
  runAgingSnapshot,
  type BalanceSnapshot,
  type Problem,
  type SnapshotResult,
  type Statement,
  type StatementLine,
  type StatementSection,
} from "./api/client";
import { compareDecimals, DAY, money, shortId, today, UUID } from "./format";
import { CommandFailure, Detail, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { agingColumns, agingRows } from "./WorkspacePage";

const SOURCE_LABELS: Record<string, string> = {
  INVOICE: "Invoice",
  FEE: "Dunning fee",
  INTEREST: "Interest note",
  DEDUCTION: "Deduction",
  PAYMENT: "Unapplied payment",
  ADJUSTMENT: "Adjustment",
  REBATE: "Rebate",
  CREDIT_NOTE: "Credit note",
};

/** The label of a statement line's source; DEDUCTION and PAYMENT lines carry no due date. */
export function sourceLabel(line: Pick<StatementLine, "sourceType" | "dueDate">): string {
  if (line.sourceType === "DEDUCTION" && line.dueDate !== null) return "Deduction re-bill";
  return SOURCE_LABELS[line.sourceType] ?? line.sourceType;
}

/** The link to a customer's statement, used from the snapshot list and elsewhere. */
export function statementLink(legalEntityId: string, customerId: string, asOf?: string): string {
  const q = new URLSearchParams({ legalEntityId, customer: customerId });
  if (asOf) q.set("asOf", asOf);
  return `/receivables/statements?${q.toString()}`;
}

const lineColumns: DataTableColumn<StatementLine>[] = [
  { key: "documentDate", header: "Date", mono: true },
  { key: "documentNumber", header: "Document", mono: true },
  { key: "sourceType", header: "Type", render: (row) => sourceLabel(row) },
  { key: "dueDate", header: "Due", mono: true, render: (row) => row.dueDate ?? "None" },
  { key: "amount", header: "Amount", mono: true, align: "end" },
  { key: "openAmount", header: "Open", mono: true, align: "end" },
  { key: "runningBalance", header: "Balance", mono: true, align: "end" },
];

function SectionCard({ section, asOf }: { section: StatementSection; asOf: string }) {
  const aging = agingRows({ asOf, totals: section.aging });
  return (
    <Card title={`${section.currency} account`} headingLevel={3}>
      <div className={styles.stack}>
        <dl className={styles.details}>
          <Detail term="Closing balance" mono>{money(section.closingBalance, section.currency)}</Detail>
          <Detail term="Open documents" mono>{section.lines.length}</Detail>
        </dl>
        <DataTable
          label={`${section.currency} statement lines`}
          columns={lineColumns}
          rows={section.lines}
          getRowKey={(row) => `${row.sourceType}|${row.documentNumber}|${row.documentDate}|${row.runningBalance}`}
          empty={{ title: "Nothing open", description: "No document of this currency was open on the statement date." }}
        />
        <DataTable
          label={`${section.currency} aging`}
          columns={agingColumns}
          rows={aging}
          getRowKey={(row) => row.currency}
          empty={{ title: "Nothing overdue to age", description: "Only deductions or unapplied payments are open; they are not aged here." }}
        />
      </div>
    </Card>
  );
}

/** Customer account statement (FR-30.13): what the customer had open at the end of a day, read-only (BR-30.10). */
export function StatementPage() {
  const [params] = useSearchParams();
  const [legalEntityId, setLegalEntityId] = useState(params.get("legalEntityId") ?? "");
  const [customer, setCustomer] = useState(params.get("customer") ?? "");
  const [asOf, setAsOf] = useState(params.get("asOf") ?? today());
  const entity = legalEntityId.trim();
  const cust = customer.trim();
  const entityValid = UUID.test(entity);
  const customerValid = UUID.test(cust);
  const dateValid = DAY.test(asOf);
  const ready = entityValid && customerValid && dateValid;
  const state = useLoad<Statement | null>(
    (signal) => (ready ? fetchStatement(entity, cust, asOf, signal) : Promise.resolve(null)),
    ready ? `${entity}|${cust}|${asOf}` : "",
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Customer statement"
        description="The customer's open documents at the end of the statement date, by currency, with a running balance and an aging footer. Payments, deductions and settlements booked later are added back; nothing changes."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/balance-snapshots">
            Balance snapshots
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="st-entity" label="Legal entity" error={entity !== "" && !entityValid ? "Choose a legal entity." : undefined}>
          {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} invalid={entity !== "" && !entityValid} />}
        </FormField>
        <FormField id="st-customer" label="Customer" error={cust !== "" && !customerValid ? "Choose a customer." : undefined}>
          {(c) => <EntityLookup entity="customer" scope={{ legalEntityId: legalEntityId || undefined }} {...c} value={customer} onChange={(e) => setCustomer(e.target.value)} invalid={cust !== "" && !customerValid} />}
        </FormField>
        <FormField id="st-as-of" label="Statement date" error={dateValid ? undefined : "Enter a date as YYYY-MM-DD."}>
          {(c) => <Input {...c} type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} invalid={!dateValid} />}
        </FormField>
      </div>
      {!ready ? (
        <EmptyState title="Choose a customer" description="A statement is one customer's account in one legal entity. Enter both ids to load it." />
      ) : state.error ? (
        <LoadFailure title="Could not load the statement" problem={state.error} retry={state.reload} />
      ) : state.loading || !state.data ? (
        <DataTable label="Statement lines" columns={lineColumns} rows={[]} getRowKey={(row) => row.documentNumber} loading />
      ) : state.data.sections.length === 0 ? (
        <EmptyState title="Nothing open" description={`The customer had no open document and no unapplied payment at the end of ${state.data.asOf}.`} />
      ) : (
        state.data.sections.map((section) => <SectionCard key={section.currency} section={section} asOf={state.data!.asOf} />)
      )}
      <p className={styles.note}>
        Deductions still being worked are owed and listed on their own line; they are aged apart from invoices. Unapplied payments are credits.
        Monthly PDF statements and their delivery wait for the document service.
      </p>
    </div>
  );
}

const snapshotColumns: DataTableColumn<BalanceSnapshot>[] = [
  { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
  { key: "currency", header: "Currency", mono: true },
  { key: "riskScore", header: "Risk score", mono: true, align: "end" },
  { key: "totalOpen", header: "Balance", mono: true, align: "end", render: (row) => money(row.totalOpen, row.currency) },
  { key: "overdue", header: "Overdue", mono: true, align: "end", render: (row) => money(row.overdue, row.currency) },
  { key: "oldestItemDays", header: "Oldest (days)", mono: true, align: "end" },
  { key: "dunningLevel", header: "Dunning level", mono: true, align: "end" },
];

/** Stored customer balances from the aging snapshot job (§5.30.16), with a manual run for a missed night. */
export function BalanceSnapshotsPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const [asOf, setAsOf] = useState("");
  const [result, setResult] = useState<SnapshotResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [runs, setRuns] = useState(0);
  const entity = legalEntityId.trim();
  const entityValid = UUID.test(entity);
  const dateValid = asOf === "" || DAY.test(asOf);
  const ready = entityValid && dateValid;
  const state = useLoad<BalanceSnapshot[] | null>(
    (signal) => (ready ? listBalanceSnapshots({ legalEntityId: entity, asOf }, signal) : Promise.resolve(null)),
    ready ? `${entity}|${asOf}|${runs}` : "",
  );
  const rows = state.data ?? [];
  const shownDate = rows[0]?.asOf ?? (asOf || "the latest date");

  async function run() {
    setFailure(null);
    setResult(null);
    setBusy(true);
    try {
      setResult(await runAgingSnapshot(entity, asOf || undefined));
      setRuns((n) => n + 1);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Balance snapshots"
        description="Each customer's balance per currency as stored by the nightly aging and risk score job, riskiest first. A balance that moved since the last snapshot is announced to sales credit and the portal."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/worklist">
            Collections worklist
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="bs-entity-snap" label="Legal entity" error={entity !== "" && !entityValid ? "Choose a legal entity." : undefined}>
          {(c) => <EntityLookup entity="legalEntity" {...c} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} invalid={entity !== "" && !entityValid} />}
        </FormField>
        <FormField id="bs-as-of" label="Key date" optional hint="Empty shows the latest snapshot." error={dateValid ? undefined : "Enter a date as YYYY-MM-DD."}>
          {(c) => <Input {...c} type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} invalid={!dateValid} />}
        </FormField>
        <Button variant="primary" onClick={run} loading={busy} disabled={!ready}>
          Run snapshot now
        </Button>
      </div>
      {result ? (
        <Alert tone="success" title="Snapshot stored" live="status">
          {`${result.rows} balance(s) stored for ${result.asOf}; ${result.changed} change(s) announced.`}
        </Alert>
      ) : null}
      {failure ? <CommandFailure title={failure.code === "AUTH-0003" ? "You may not run the snapshot" : "Could not run the snapshot"} problem={failure} /> : null}
      {!ready ? (
        <EmptyState title="Choose a legal entity" description="Snapshots are stored per legal entity. Enter its id to see them." />
      ) : state.error ? (
        <LoadFailure title="Could not load the balance snapshots" problem={state.error} retry={state.reload} />
      ) : (
        <>
          {rows.length > 0 ? (
            <StatGroup label="Snapshot totals">
              <StatTile label="Customers" value={String(new Set(rows.map((r) => r.customerId)).size)} />
              <StatTile label="With overdue balances" value={String(rows.filter((r) => compareDecimals(r.overdue, "0") > 0).length)} tone="danger" />
              <StatTile label="At dunning level 3 or 4" value={String(rows.filter((r) => r.dunningLevel >= 3).length)} tone="warning" />
            </StatGroup>
          ) : null}
          <DataTable
            label={`Balance snapshots for ${shownDate}`}
            columns={snapshotColumns}
            rows={rows}
            getRowKey={(row) => `${row.customerId}|${row.currency}`}
            loading={state.loading}
            empty={{ title: "No snapshot", description: "No snapshot is stored for this legal entity and date. Run it now, or wait for the 01:30 job." }}
            onRowOpen={(row) => navigate(statementLink(row.legalEntityId, row.customerId))}
          />
        </>
      )}
    </div>
  );
}
