import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import { describeProblem, getRun, newKey, releaseQuarantined, type Problem, type QuarantinedRecord } from "./api/client";
import { canReconcile, dash, formatDateTime, formatKey, money, projectsOf, projectStatus, SOURCE_LABELS, splitList, type ControlTotal } from "./domain";
import styles from "./pages.module.css";
import { TargetLoadPanel } from "./TargetLoadPanel";
import { useLoad } from "./useLoad";

const totalColumns: DataTableColumn<ControlTotal>[] = [
  { key: "name", header: "Control total", mono: true },
  { key: "key", header: "Key", render: (row) => formatKey(row.key) },
  { key: "source", header: "Legacy", align: "end", mono: true, render: (row) => money(row.source) },
  { key: "target", header: "Passage", align: "end", mono: true, render: (row) => money(row.target) },
  { key: "delta", header: "Delta", align: "end", mono: true, render: (row) => money(row.delta) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

interface ProjectRow {
  projectId: string;
  status: "RECONCILED" | "OPENING_MISMATCH";
}

interface Correction {
  party: string;
  account: string;
  /** Onhand dimension and unit, blank to keep the extracted value. */
  site: string;
  warehouse: string;
  location: string;
  batch: string;
  unit: string;
  deliveryDate: string;
  parties: string;
  customers: string;
  vendors: string;
}

const NO_FIX: Correction = { party: "", account: "", site: "", warehouse: "", location: "", batch: "", unit: "", deliveryDate: "", parties: "", customers: "", vendors: "" };

/** Packs whose quarantined records can be corrected and released (FR-49.11). */
const RELEASABLE = ["open_items", "bank_balances", "onhand", "open_orders"];

/** What the master-data list of a release names, by pack. */
const MASTER_LABEL: Record<string, string> = {
  open_items: "Parties added to master data",
  bank_balances: "Bank accounts added to master data",
  onhand: "Items added to master data",
  open_orders: "Items added to master data",
};

/** One pack run: its counts, the control-total board, the quarantine with release, and its projects (9.49.6). */
/** Packs whose loaded rows stay in the factory (domain.NotPosted), with the ledger they are not posted to. */
const STAGED_ONLY: Partial<Record<string, string>> = {
  onhand: "the inventory ledger",
  gl_balances: "the general ledger",
  project_wip: "project operations",
  open_orders: "sales or purchasing",
  bank_balances: "treasury",
};

export function PackRunDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getRun(id, signal), id);
  const run = state.data;
  const [selected, setSelected] = useState<QuarantinedRecord | null>(null);
  const [fix, setFix] = useState<Correction>(NO_FIX);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const releaseKey = useRef(newKey());
  const mismatches = run ? run.controlTotals.filter((t) => t.status === "MISMATCH").length : 0;
  const releasable = Boolean(run && RELEASABLE.includes(run.pack) && run.status === "LOADED");

  const quarantineColumns: DataTableColumn<QuarantinedRecord>[] = [
    { key: "seq", header: "Seq", mono: true },
    { key: "legacyId", header: "Record", mono: true },
    { key: "rule", header: "Rule failed" },
    { key: "reason", header: "Reason" },
    {
      key: "dimensions",
      header: run?.pack === "gl_balances" ? "Account and dimensions" : run?.pack === "bank_balances" ? "Bank account" : run?.pack === "onhand" ? "Item and dimension" : run?.pack === "open_orders" ? "Party, item and order line" : "Party and account",
      render: (row) =>
        (run?.pack === "gl_balances" || run?.pack === "onhand" ? [row.account, row.dimensions] : run?.pack === "open_orders" ? [row.party, row.account, row.dimensions] : [row.ledger === "AP" ? "Payable" : undefined, row.party, row.account])
          .filter(Boolean)
          .join(" / ") || dash,
    },
    // An onhand or open order line's amount is its quantity (the open quantity of an order) in the line's unit, not money in its currency.
    {
      key: "amount",
      header: run?.pack === "onhand" ? "Quantity" : run?.pack === "open_orders" ? "Open quantity" : "Amount",
      align: "end",
      mono: true,
      render: (row) => (run?.pack === "onhand" || run?.pack === "open_orders" ? row.amount : `${money(row.amount)} ${row.currency ?? ""}`.trim()),
    },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        releasable && row.status === "OPEN" ? (
          <Button size="sm" onClick={() => choose(row)} aria-label={`Release record ${row.legacyId}`}>
            Release
          </Button>
        ) : row.status === "RELEASED" && row.releasedBy ? (
          `By ${row.releasedBy}`
        ) : null,
    },
  ];

  const projectColumns: DataTableColumn<ProjectRow>[] = [
    { key: "projectId", header: "Project", mono: true },
    { key: "status", header: "Opening balance", render: (row) => <StatusPill status={row.status === "RECONCILED" ? "Reconciled" : "Opening mismatch"} /> },
  ];

  function choose(record: QuarantinedRecord) {
    setSelected(record);
    setFix({ ...NO_FIX, party: record.party ?? "", account: record.account ?? "" });
    setFailure(null);
    setNotice(null);
    releaseKey.current = newKey();
  }

  async function submitRelease(event: FormEvent) {
    event.preventDefault();
    if (!run || !selected) return;
    setFailure(null);
    setBusy(true);
    try {
      const parties = splitList(fix.parties);
      const changed = (value: string, extracted?: string) => value.trim() !== (extracted ?? "").trim();
      const fields =
        run.pack === "bank_balances"
          ? changed(fix.account, selected.account)
            ? { bankAccount: fix.account.trim() }
            : {}
          : run.pack === "open_orders"
            ? {
                ...(changed(fix.account, selected.account) ? { item: fix.account.trim() } : {}),
                ...(changed(fix.party, selected.party) ? { party: fix.party.trim() } : {}),
                ...(fix.unit.trim() ? { unit: fix.unit.trim() } : {}),
                ...(fix.deliveryDate.trim() ? { deliveryDate: fix.deliveryDate.trim() } : {}),
                ...(splitList(fix.customers).length > 0 ? { customers: splitList(fix.customers) } : {}),
                ...(splitList(fix.vendors).length > 0 ? { vendors: splitList(fix.vendors) } : {}),
              }
            : run.pack === "onhand"
            ? {
                ...(changed(fix.account, selected.account) ? { item: fix.account.trim() } : {}),
                ...(fix.site.trim() ? { site: fix.site.trim() } : {}),
                ...(fix.warehouse.trim() ? { warehouse: fix.warehouse.trim() } : {}),
                ...(fix.location.trim() ? { location: fix.location.trim() } : {}),
                ...(fix.batch.trim() ? { batch: fix.batch.trim() } : {}),
                ...(fix.unit.trim() ? { unit: fix.unit.trim() } : {}),
              }
            : {
                ...(changed(fix.party, selected.party) ? { party: fix.party.trim() } : {}),
                ...(changed(fix.account, selected.account) ? { account: fix.account.trim() } : {}),
              };
      const next = await releaseQuarantined(
        run.runId,
        selected.seq,
        {
          ...fields,
          ...(parties.length > 0 ? { parties } : {}),
          version: run.version,
        },
        releaseKey.current,
      );
      state.set(next);
      setNotice(`Released ${selected.legacyId}. ${next.status === "RECONCILED" ? "Every control total now matches." : `${next.recordsQuarantined} record(s) still quarantined.`}`);
      setSelected(null);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(
        problem.code === "MIG-0006" ? { ...problem, message: "Someone changed this run since you opened it. The run has been reloaded; check it and try again." } : problem,
      );
      if (problem.code === "MIG-0006") state.reload();
      releaseKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const projects: ProjectRow[] = run && run.pack === "project_wip" ? projectsOf(run.controlTotals).map((p) => ({ projectId: p, status: projectStatus(run.controlTotals, p) })) : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={run ? `Pack run ${run.pack}` : "Pack run"}
        description={
          run
            ? canReconcile(run.controlTotals)
              ? "Every control total matches the legacy extract."
              : `${mismatches} control total(s) do not match; the run can't reconcile until they do.`
            : undefined
        }
        meta={run ? <StatusPill status={run.status} /> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/migration-factory">
            All runs
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the pack run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The record was not released" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {notice ? (
        <Alert tone="success" title="Record released" live="status">
          {notice}
        </Alert>
      ) : null}
      {run ? (
        <>
          {run.message ? (
            <Alert tone="info" title="Nothing new to load">
              {run.message}
            </Alert>
          ) : null}
          {STAGED_ONLY[run.pack] ? (
            <Alert tone="warning" title={`Staged, not posted to ${STAGED_ONLY[run.pack]}`}>
              This run holds its rows in the migration factory and reconciles them with the extract. Nothing is posted to {STAGED_ONLY[run.pack]} yet,
              so the cutover go/no-go keeps the packs item open for a plan that includes this pack until both approvers waive it.
            </Alert>
          ) : null}
          {run.mismatchMessages && run.mismatchMessages.length > 0 ? (
            <Alert tone="danger" title={run.pack === "onhand" ? "Inventory account differs from the loaded stock" : "Open items differ from the GL"}>
              <ul className={styles.plainList}>
                {run.mismatchMessages.map((m) => (
                  <li key={m}>{m}</li>
                ))}
              </ul>
            </Alert>
          ) : null}
          <StatGroup label="Records">
            <StatTile label="Records" value={run.recordsIn} />
            <StatTile label="Loaded" value={run.recordsLoaded} />
            <StatTile label="Already present" value={run.alreadyPresent} />
            <StatTile label="Quarantined" value={run.recordsQuarantined} tone={run.recordsQuarantined > 0 ? "warning" : "neutral"} />
          </StatGroup>
          <Card title="Run">
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Source</dt>
                <dd className={styles.value}>
                  {SOURCE_LABELS[run.sourceSystem] ?? run.sourceSystem} {run.sourceCompany}
                </dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Cutoff</dt>
                <dd className={styles.value}>{formatDateTime(run.cutoffTs)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Started</dt>
                <dd className={styles.value}>{formatDateTime(run.createdAt)}</dd>
              </div>
              {run.openItemsRunId ? (
                <div className={styles.entry}>
                  <dt className={styles.term}>Reconciled with open items</dt>
                  <dd className={`${styles.value} ${styles.mono}`}>
                    <Link to={`/migration-factory/runs/${run.openItemsRunId}`}>{run.openItemsRunId}</Link>
                  </dd>
                </div>
              ) : null}
              {run.glRunId ? (
                <div className={styles.entry}>
                  <dt className={styles.term}>Reconciled with GL balances</dt>
                  <dd className={`${styles.value} ${styles.mono}`}>
                    <Link to={`/migration-factory/runs/${run.glRunId}`}>{run.glRunId}</Link>
                  </dd>
                </div>
              ) : null}
              <div className={styles.entry}>
                <dt className={styles.term}>Input hash</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{run.inputHash}</dd>
              </div>
            </dl>
          </Card>
        </>
      ) : null}
      {!state.error ? (
        <DataTable
          label="Control totals"
          columns={totalColumns}
          rows={run?.controlTotals ?? []}
          getRowKey={(row) => `${row.name}|${formatKey(row.key)}`}
          loading={state.loading}
          empty={{ title: "No totals yet", description: "Totals appear once the run has loaded." }}
        />
      ) : null}
      {run?.targetLoad ? <TargetLoadPanel initial={run.targetLoad} onChanged={state.reload} /> : null}
      {projects.length > 0 && run ? (
        <Card title="Projects">
          <p className={styles.note}>Opening lines never post to the GL. A project that does not reconcile can't be invoiced or estimated (BR-49.12).</p>
          <DataTable
            label="Projects"
            columns={projectColumns}
            rows={projects}
            getRowKey={(row) => row.projectId}
            onRowOpen={(row) => navigate(`/migration-factory/runs/${run.runId}/projects/${encodeURIComponent(row.projectId)}`)}
          />
        </Card>
      ) : null}
      {selected ? (
        <Card title={`Release ${selected.legacyId}`}>
          <form className={styles.form} onSubmit={submitRelease} noValidate>
            <p className={`${styles.note} ${styles.wide}`}>
              {selected.rule}: {selected.reason}.{" "}
              {run?.pack === "bank_balances"
                ? "Correct the bank account; the balance and currency come from the legacy extract and change only by re-extract."
                : run?.pack === "open_orders"
                  ? "Correct the item, the customer or vendor, the unit or the delivery date (blank keeps the extracted value); order and line number, quantities, price and currency come from the legacy extract and change only by re-extract."
                  : run?.pack === "onhand"
                  ? "Correct the item, the inventory dimension or the unit (blank keeps the extracted value); quantity, value and serial come from the legacy extract and change only by re-extract."
                  : "Correct the party or control account; amount and currency come from the legacy extract and change only by re-extract."}
            </p>
            {run?.pack === "open_items" ? (
              <>
                <FormField id="release-party" label="Party">
                  {(control) => <Input {...control} value={fix.party} onChange={(event) => setFix({ ...fix, party: event.target.value })} />}
                </FormField>
                <FormField id="release-account" label="Control account">
                  {(control) => <Input {...control} value={fix.account} onChange={(event) => setFix({ ...fix, account: event.target.value })} />}
                </FormField>
              </>
            ) : null}
            {run?.pack === "bank_balances" ? (
              <FormField id="release-bank-account" label="Bank account">
                {(control) => <Input {...control} value={fix.account} onChange={(event) => setFix({ ...fix, account: event.target.value })} />}
              </FormField>
            ) : null}
            {run?.pack === "open_orders" ? (
              <>
                <FormField id="release-item" label="Item">
                  {(control) => <Input {...control} value={fix.account} onChange={(event) => setFix({ ...fix, account: event.target.value })} />}
                </FormField>
                <FormField id="release-party" label={selected.dimensions?.startsWith("PURCHASE/") ? "Vendor" : "Customer"}>
                  {(control) => <Input {...control} value={fix.party} onChange={(event) => setFix({ ...fix, party: event.target.value })} />}
                </FormField>
                <FormField id="release-unit" label="Unit" hint="Blank keeps the extracted value." optional>
                  {(control) => <Input {...control} value={fix.unit} onChange={(event) => setFix({ ...fix, unit: event.target.value })} />}
                </FormField>
                <FormField id="release-delivery-date" label="Delivery date" hint="YYYY-MM-DD. Blank keeps the extracted value." optional>
                  {(control) => <Input {...control} value={fix.deliveryDate} onChange={(event) => setFix({ ...fix, deliveryDate: event.target.value })} />}
                </FormField>
              </>
            ) : null}
            {run?.pack === "onhand" ? (
              <>
                <FormField id="release-item" label="Item">
                  {(control) => <Input {...control} value={fix.account} onChange={(event) => setFix({ ...fix, account: event.target.value })} />}
                </FormField>
                {(
                  [
                    ["site", "Site"],
                    ["warehouse", "Warehouse"],
                    ["location", "Location"],
                    ["batch", "Batch"],
                    ["unit", "Unit"],
                  ] as const
                ).map(([field, label]) => (
                  <FormField key={field} id={`release-${field}`} label={label} hint="Blank keeps the extracted value." optional>
                    {(control) => <Input {...control} value={fix[field]} onChange={(event) => setFix({ ...fix, [field]: event.target.value })} />}
                  </FormField>
                ))}
              </>
            ) : null}
            <div className={styles.wide}>
              <FormField id="release-parties" label={MASTER_LABEL[run?.pack ?? "open_items"] ?? "Added to master data"} hint="Ids created since the run, separated by commas." optional>
                {(control) => <Textarea {...control} mono rows={2} value={fix.parties} onChange={(event) => setFix({ ...fix, parties: event.target.value })} />}
              </FormField>
            </div>
            {run?.pack === "open_orders" ? (
              <>
                <FormField id="release-customers" label="Customers added to master data" hint="Ids created since the run, separated by commas." optional>
                  {(control) => <Textarea {...control} mono rows={2} value={fix.customers} onChange={(event) => setFix({ ...fix, customers: event.target.value })} />}
                </FormField>
                <FormField id="release-vendors" label="Vendors added to master data" hint="Ids created since the run, separated by commas." optional>
                  {(control) => <Textarea {...control} mono rows={2} value={fix.vendors} onChange={(event) => setFix({ ...fix, vendors: event.target.value })} />}
                </FormField>
              </>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Release record
              </Button>
              <Button variant="ghost" onClick={() => setSelected(null)}>
                Cancel
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {run && run.quarantined.length > 0 ? (
        <DataTable label="Quarantined records" columns={quarantineColumns} rows={run.quarantined} getRowKey={(row) => String(row.seq)} />
      ) : null}
    </div>
  );
}
