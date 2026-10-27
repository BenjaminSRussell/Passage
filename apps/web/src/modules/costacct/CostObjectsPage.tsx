import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createCostObject,
  describeError,
  getCostObject,
  listCostElements,
  listCostObjects,
  listEntries,
  type CostEntry,
  type CostObject,
  type CostObjectKind,
} from "./api/client";
import { formatAmount, formatTicks, isPeriod, sumTicks, UUID, validateCodeForm, type CodeForm } from "./logic";
import styles from "./pages.module.css";
import { Detail, ErrorList, kindLabel, LoadError, thisPeriod } from "./parts";
import { SettlementPanel } from "./SettlementPanel";
import { useLoad } from "./useLoad";

export const KINDS: CostObjectKind[] = ["CostCenter", "InternalOrder", "Product", "Project"];

const columns: DataTableColumn<CostObject>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "kind", header: "Kind", render: (o) => kindLabel(o.kind) },
  { key: "legalEntityId", header: "Legal entity", mono: true },
];

/** Cost objects (FR-38.6): cost centers, internal orders, products and projects that carry cost. */
export function CostObjectsPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const [kind, setKind] = useState<CostObjectKind | "">("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listCostObjects({ legalEntityId: le || undefined, kind }, signal), `${le}|${kind}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Cost objects"
        description="Cost centers, internal orders, products and projects. Journal lines mirror into them as primary cost; allocation runs move cost between them."
        actions={
          <Link className={buttonClassName("primary")} to="/costacct/cost-objects/new">
            New cost object
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="objects-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
        <FormField id="objects-kind" label="Kind">
          {(c) => (
            <Select {...c} value={kind} onChange={(e) => setKind(e.target.value as CostObjectKind | "")}>
              <option value="">All</option>
              {KINDS.map((k) => (
                <option key={k} value={k}>
                  {kindLabel(k)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load cost objects" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Cost objects"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(o) => o.id}
          loading={state.loading && !state.data}
          empty={{ title: "No cost objects yet", description: "Create a cost center such as CC-100 to start collecting cost." }}
          onRowOpen={(o) => navigate(`/costacct/cost-objects/${o.id}`)}
        />
      )}
    </div>
  );
}

/** Creates a cost object; codes are unique per legal entity (CST-0003). */
export function CreateCostObjectPage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<CodeForm & { kind: CostObjectKind }>({ legalEntityId: "", code: "", name: "", kind: "CostCenter" });
  const [errors, setErrors] = useState<Partial<Record<keyof CodeForm, string>>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { ...form, legalEntityId: form.legalEntityId.trim(), code: form.code.trim(), name: form.name.trim() };
    const found = validateCodeForm(clean);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const body = JSON.stringify(clean);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const created = await createCostObject(clean, key.current.value);
      navigate(`/costacct/cost-objects/${created.id}`);
    } catch (err) {
      setFailure(describeError(err, "The cost object could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New cost object" description="Internal orders collect cost for a purpose and settle it to cost centers or other objects." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="object-le" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <Input {...c} className={styles.mono} value={form.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => setForm({ ...form, legalEntityId: e.target.value })} />}
          </FormField>
          <FormField id="object-kind" label="Kind">
            {(c) => (
              <Select {...c} value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value as CostObjectKind })}>
                {KINDS.map((k) => (
                  <option key={k} value={k}>
                    {kindLabel(k)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="object-code" label="Code" error={errors.code}>
            {(c) => <Input {...c} className={styles.mono} value={form.code} invalid={Boolean(errors.code)} onChange={(e) => setForm({ ...form, code: e.target.value })} />}
          </FormField>
          <FormField id="object-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={form.name} invalid={Boolean(errors.name)} onChange={(e) => setForm({ ...form, name: e.target.value })} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save cost object
            </Button>
            <Link className={buttonClassName("ghost")} to="/costacct">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <ErrorList errors={[]} failure={failure} />
    </div>
  );
}

/** One cost object and its line items for a period (journal mirrors and allocations). */
export function CostObjectDetailPage() {
  const { id = "" } = useParams();
  const [period, setPeriod] = useState(thisPeriod());
  const object = useLoad((signal) => getCostObject(id, signal), id);
  const validPeriod = isPeriod(period);
  const entries = useLoad(
    (signal) => (validPeriod ? listEntries(id, period, signal) : Promise.resolve({ items: [] as CostEntry[] })),
    `${id}|${validPeriod ? period : ""}`,
  );
  const elements = useLoad((signal) => (object.data ? listCostElements(object.data.legalEntityId, signal) : Promise.resolve({ items: [] })), object.data?.legalEntityId ?? "");
  const elementCode = new Map((elements.data?.items ?? []).map((e) => [e.id, e.code]));

  const entryColumns: DataTableColumn<CostEntry>[] = [
    { key: "costElementId", header: "Cost element", mono: true, render: (e) => elementCode.get(e.costElementId) ?? e.costElementId },
    { key: "origin", header: "Origin" },
    { key: "behavior", header: "Behavior" },
    { key: "amount", header: "Amount", align: "end", mono: true, render: (e) => formatAmount(e.amount) },
    { key: "currency", header: "Currency", mono: true },
    {
      key: "sourceId",
      header: "Source",
      mono: true,
      render: (e) =>
        e.allocationRunId ? (
          <Link to={`/costacct/allocation-runs/${e.allocationRunId}`}>Allocation run</Link>
        ) : e.topDownRunId ? (
          <Link to={`/costacct/top-down-runs/${e.topDownRunId}`}>Top-down run</Link>
        ) : e.settlementId ? (
          "Settlement"
        ) : (
          e.sourceId
        ),
    },
  ];

  if (object.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Cost object" />
        <LoadError title="Could not load this cost object" error={object.error} onRetry={object.reload} />
      </div>
    );
  }
  if (!object.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Cost object" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const o = object.data;
  const rows = entries.data?.items ?? [];
  const byCurrency = new Map<string, string[]>();
  for (const e of rows) byCurrency.set(e.currency, [...(byCurrency.get(e.currency) ?? []), e.amount]);

  return (
    <div className={styles.stack}>
      <PageHeader title={`${o.code} ${o.name}`} description={`${kindLabel(o.kind)} cost object.`} />
      <Card>
        <dl className={styles.details}>
          <Detail term="Code" mono>
            {o.code}
          </Detail>
          <Detail term="Kind">{kindLabel(o.kind)}</Detail>
          <Detail term="Legal entity" mono>
            {o.legalEntityId}
          </Detail>
          {[...byCurrency].map(([cur, amounts]) => (
            <Detail key={cur} term={`Period total (${cur})`} mono>
              {formatAmount(formatTicks(sumTicks(amounts) ?? 0n))}
            </Detail>
          ))}
        </dl>
      </Card>
      <div className={styles.toolbar}>
        <FormField id="entries-period" label="Period" hint="YYYY-MM" error={validPeriod ? undefined : "Enter a period as YYYY-MM."}>
          {(c) => <Input {...c} className={styles.mono} value={period} invalid={!validPeriod} onChange={(e) => setPeriod(e.target.value)} />}
        </FormField>
      </div>
      {entries.error ? (
        <LoadError title="Could not load cost entries" error={entries.error} onRetry={entries.reload} />
      ) : (
        <DataTable
          label="Cost entries"
          columns={entryColumns}
          rows={rows}
          getRowKey={(e) => e.id}
          loading={entries.loading && !entries.data}
          empty={{ title: "No cost entries in this period", description: "Mirrored journal lines and allocation postings for the period appear here." }}
        />
      )}
      {o.kind === "InternalOrder" && validPeriod && entries.data ? <SettlementPanel order={o} period={period} entries={rows} onSettled={entries.reload} /> : null}
    </div>
  );
}
