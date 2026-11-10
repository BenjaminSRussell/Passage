import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeError,
  dryRunTemplate,
  getTemplate,
  getTemplateRun,
  listCostElements,
  listCostObjects,
  listMeasures,
  listTemplateRuns,
  listTemplates,
  runTemplate,
  saveTemplate,
  type AllocationTemplate,
  type CostElement,
  type CostObject,
  type DryRunResult,
  type StatisticalMeasure,
  type TemplateLine,
  type TemplateLineResult,
  type TemplateRun,
} from "./api/client";
import { formatAmount, isPeriod, UUID, validateTemplate, type TemplateForm } from "./logic";
import styles from "./pages.module.css";
import { dash, Detail, ErrorList, formatDateTime, LoadError, thisPeriod } from "./parts";
import { useLoad } from "./useLoad";

/** Codes of a legal entity's objects, elements and measures, for pickers and labels. */
function useLedger(le: string) {
  const objects = useLoad((signal) => (le ? listCostObjects({ legalEntityId: le }, signal) : Promise.resolve({ items: [] as CostObject[] })), le);
  const elements = useLoad((signal) => (le ? listCostElements(le, signal) : Promise.resolve({ items: [] as CostElement[] })), le);
  const measures = useLoad((signal) => (le ? listMeasures(le, signal) : Promise.resolve({ items: [] as StatisticalMeasure[] })), le);
  const objectCode = (id: string) => (objects.data?.items ?? []).find((o) => o.id === id)?.code ?? id;
  const elementCode = (id: string) => (elements.data?.items ?? []).find((e) => e.id === id)?.code ?? id;
  return { objects, elements, measures, objectCode, elementCode };
}

const templateColumns: DataTableColumn<AllocationTemplate>[] = [
  { key: "code", header: "Template", mono: true },
  { key: "name", header: "Name" },
  { key: "lines", header: "Lines", align: "end", render: (t) => t.lines.length },
  { key: "currency", header: "Currency", mono: true },
  { key: "version", header: "Version", align: "end" },
  { key: "updatedAt", header: "Saved", render: (t) => formatDateTime(t.updatedAt) },
];

/** Allocation templates (FR-38.17): driver formulas per receiver, such as 25.00 × inspection lots + 0.5% × material. */
export function TemplatesPage() {
  const navigate = useNavigate();
  const [legalEntityId, setLegalEntityId] = useState("");
  const le = UUID.test(legalEntityId.trim()) ? legalEntityId.trim() : "";
  const state = useLoad((signal) => listTemplates(le || undefined, signal), le);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Template allocation"
        description="A template credits a sender, such as quality control, and debits each receiver with its own formula over statistical measures and cost element balances. Runs net to zero."
        actions={
          <Link className={buttonClassName("primary")} to="/costacct/templates/new">
            New template
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="templates-legal-entity" label="Legal entity" optional>
          {(c) => <Input {...c} className={styles.mono} value={legalEntityId} onChange={(e) => setLegalEntityId(e.target.value)} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load allocation templates" error={state.error} onRetry={state.reload} />
      ) : (
        <DataTable
          label="Allocation templates"
          columns={templateColumns}
          rows={state.data?.items ?? []}
          getRowKey={(t) => t.id}
          loading={state.loading && !state.data}
          empty={{ title: "No allocation templates yet", description: "Create a template with one formula line per receiver." }}
          onRowOpen={(t) => navigate(`/costacct/templates/${t.id}`)}
        />
      )}
    </div>
  );
}

type LineDraft = TemplateLine & { key: string };
const emptyLine = (): LineDraft => ({ key: crypto.randomUUID(), receiverCostObjectId: "", secondaryElementId: "", formula: "" });

/** Creates a template, or saves the next version of one (/costacct/templates/:id/edit). */
export function TemplateEditorPage() {
  const { id = "" } = useParams();
  const existing = useLoad((signal) => (id ? getTemplate(id, signal) : Promise.resolve(null)), id);
  if (id && existing.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Edit template" />
        <LoadError title="Could not load this template" error={existing.error} onRetry={existing.reload} />
      </div>
    );
  }
  if (id && !existing.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Edit template" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  return <TemplateEditor key={existing.data?.id ?? "new"} existing={existing.data ?? null} />;
}

function TemplateEditor({ existing }: { existing: AllocationTemplate | null }) {
  const navigate = useNavigate();
  const [form, setForm] = useState<TemplateForm & { allowNegative: boolean }>(
    existing
      ? { legalEntityId: existing.legalEntityId, code: existing.code, name: existing.name, currency: existing.currency, senderCostObjectId: existing.senderCostObjectId, allowNegative: existing.allowNegative }
      : { legalEntityId: "", code: "", name: "", currency: "USD", senderCostObjectId: "", allowNegative: false },
  );
  const [lines, setLines] = useState<LineDraft[]>(existing ? existing.lines.map((l) => ({ ...l, key: crypto.randomUUID() })) : [emptyLine()]);
  const [errors, setErrors] = useState<string[]>([]);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef({ body: "", value: crypto.randomUUID() });
  const le = UUID.test(form.legalEntityId.trim()) ? form.legalEntityId.trim() : "";
  const { objects, elements, measures } = useLedger(le);
  const allObjects = objects.data?.items ?? [];
  const secondaries = (elements.data?.items ?? []).filter((e) => e.kind === "Secondary");

  function update(i: number, patch: Partial<TemplateLine>) {
    setLines((cur) => cur.map((l, k) => (k === i ? { ...l, ...patch } : l)));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = { ...form, legalEntityId: form.legalEntityId.trim(), code: form.code.trim(), name: form.name.trim() };
    const cleanLines = lines.map(({ receiverCostObjectId, secondaryElementId, formula }) => ({ receiverCostObjectId, secondaryElementId, formula: formula.trim() }));
    const known = {
      measures: (measures.data?.items ?? []).map((m) => m.code),
      elements: (elements.data?.items ?? []).map((el) => el.code),
    };
    const found = validateTemplate(clean, cleanLines, known);
    setErrors(found);
    setFailure(null);
    if (found.length > 0) return;
    const input = { ...clean, lines: cleanLines, expectedVersion: existing?.version ?? 0 };
    const body = JSON.stringify(input);
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy(true);
    try {
      const saved = await saveTemplate(input, key.current.value);
      navigate(`/costacct/templates/${saved.id}`);
    } catch (err) {
      setFailure(describeError(err, "The template could not be saved."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={existing ? `Edit ${existing.code}` : "New allocation template"}
        description="Formulas use numbers, + − × ÷, min(a, b), max(a, b), if(condition, a, b), m.<measure> for a statistical measure of the receiver and b.<element> for the receiver's primary cost on a cost element."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="template-le" label="Legal entity">
            {(c) => <Input {...c} className={styles.mono} disabled={Boolean(existing)} value={form.legalEntityId} onChange={(e) => setForm({ ...form, legalEntityId: e.target.value })} />}
          </FormField>
          <FormField id="template-code" label="Code">
            {(c) => <Input {...c} className={styles.mono} disabled={Boolean(existing)} value={form.code} onChange={(e) => setForm({ ...form, code: e.target.value.toUpperCase() })} />}
          </FormField>
          <FormField id="template-name" label="Name">
            {(c) => <Input {...c} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />}
          </FormField>
          <FormField id="template-currency" label="Currency">
            {(c) => <Input {...c} className={styles.mono} value={form.currency} onChange={(e) => setForm({ ...form, currency: e.target.value.toUpperCase() })} />}
          </FormField>
          <FormField id="template-sender" label="Sender" hint="The cost object credited with every line's amount.">
            {(c) => (
              <Select {...c} value={form.senderCostObjectId} onChange={(e) => setForm({ ...form, senderCostObjectId: e.target.value })}>
                <option value="">{le ? "Choose the sender" : "Enter a legal entity first"}</option>
                {allObjects.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.code} {o.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            <label className={styles.check}>
              <Input type="checkbox" aria-label="Allow negative amounts" checked={form.allowNegative} onChange={(e) => setForm({ ...form, allowNegative: e.target.checked })} />
              Allow negative amounts
            </label>
            <p className={styles.note}>Without it, a line that evaluates below zero fails the run.</p>
          </div>
          {lines.map((line, i) => (
            <fieldset key={line.key} className={styles.lineSet}>
              <legend className={styles.legend}>Line {i + 1}</legend>
              <FormField id={`template-receiver-${i}`} label="Receiver">
                {(c) => (
                  <Select {...c} value={line.receiverCostObjectId} onChange={(e) => update(i, { receiverCostObjectId: e.target.value })}>
                    <option value="">Choose a receiver</option>
                    {allObjects
                      .filter((o) => o.id !== form.senderCostObjectId)
                      .map((o) => (
                        <option key={o.id} value={o.id}>
                          {o.code} {o.name}
                        </option>
                      ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`template-element-${i}`} label="Secondary element">
                {(c) => (
                  <Select {...c} value={line.secondaryElementId} onChange={(e) => update(i, { secondaryElementId: e.target.value })}>
                    <option value="">Choose an element</option>
                    {secondaries.map((el) => (
                      <option key={el.id} value={el.id}>
                        {el.code} {el.name}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id={`template-formula-${i}`} label="Formula" hint="For example 25.00 × m.inspectionLots + 0.005 × b.MATERIAL">
                {(c) => <Textarea {...c} rows={2} className={styles.mono} value={line.formula} onChange={(e) => update(i, { formula: e.target.value })} />}
              </FormField>
              {lines.length > 1 ? (
                <Button variant="ghost" onClick={() => setLines((cur) => cur.filter((_, k) => k !== i))}>
                  Remove line {i + 1}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            <Button onClick={() => setLines((cur) => [...cur, emptyLine()])}>Add line</Button>
            <Button type="submit" variant="primary" loading={busy}>
              {existing ? `Save version ${existing.version + 1}` : "Save template"}
            </Button>
            <Link className={buttonClassName("ghost")} to={existing ? `/costacct/templates/${existing.id}` : "/costacct/templates"}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      <ErrorList errors={errors} failure={failure} />
    </div>
  );
}

/** One template: its lines, a dry run for a period, the run itself and the run history. */
export function TemplateDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getTemplate(id, signal), id);
  const runs = useLoad((signal) => listTemplateRuns({ templateId: id }, signal), id);
  const le = state.data?.legalEntityId ?? "";
  const { objectCode, elementCode } = useLedger(le);
  const [period, setPeriod] = useState(thisPeriod());
  const [dry, setDry] = useState<DryRunResult | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState<"dry" | "run" | null>(null);
  const key = useRef({ body: "", value: crypto.randomUUID() });

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Allocation template" />
        <LoadError title="Could not load this template" error={state.error} onRetry={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Allocation template" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const t = state.data;

  async function preview() {
    setFailure(null);
    setDry(null);
    if (!isPeriod(period)) {
      setFailure("Enter the period as YYYY-MM.");
      return;
    }
    setBusy("dry");
    try {
      setDry(await dryRunTemplate(t.id, period));
    } catch (err) {
      setFailure(describeError(err, "The dry run could not be completed."));
    } finally {
      setBusy(null);
    }
  }

  async function run() {
    setFailure(null);
    if (!isPeriod(period)) {
      setFailure("Enter the period as YYYY-MM.");
      return;
    }
    const body = `${t.id}|${t.version}|${period}`;
    if (key.current.body !== body) key.current = { body, value: crypto.randomUUID() };
    setBusy("run");
    try {
      const done = await runTemplate(t.id, period, key.current.value);
      navigate(`/costacct/template-runs/${done.id}`);
    } catch (err) {
      setFailure(describeError(err, "The template run could not be completed."));
    } finally {
      setBusy(null);
    }
  }

  const lineColumns: DataTableColumn<TemplateLine & { n: number }>[] = [
    { key: "n", header: "Line", align: "end" },
    { key: "receiverCostObjectId", header: "Receiver", mono: true, render: (l) => objectCode(l.receiverCostObjectId) },
    { key: "secondaryElementId", header: "Element", mono: true, render: (l) => elementCode(l.secondaryElementId) },
    { key: "formula", header: "Formula", mono: true },
  ];
  const resultColumns = resultColumnsFor(objectCode, elementCode, t.currency);
  const runColumns: DataTableColumn<TemplateRun>[] = [
    { key: "period", header: "Period", mono: true },
    { key: "templateVersion", header: "Version", align: "end" },
    { key: "total", header: `Total (${t.currency})`, align: "end", mono: true, render: (r) => formatAmount(r.total) },
    { key: "warnings", header: "Warnings", align: "end", render: (r) => r.warnings.length },
    { key: "createdAt", header: "Run at", render: (r) => formatDateTime(r.createdAt) },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Template ${t.code}`}
        description={t.name}
        meta={<Badge tone="neutral">Version {t.version}</Badge>}
        actions={
          <Link className={buttonClassName("secondary")} to={`/costacct/templates/${t.id}/edit`}>
            Edit template
          </Link>
        }
      />
      <Card>
        <dl className={styles.details}>
          <Detail term="Sender" mono>
            <Link to={`/costacct/cost-objects/${t.senderCostObjectId}`}>{objectCode(t.senderCostObjectId)}</Link>
          </Detail>
          <Detail term="Currency" mono>
            {t.currency}
          </Detail>
          <Detail term="Negative amounts">{t.allowNegative ? "Allowed" : "Fail the run"}</Detail>
          <Detail term="Legal entity" mono>
            {t.legalEntityId}
          </Detail>
          <Detail term="Saved">{formatDateTime(t.updatedAt)}</Detail>
        </dl>
      </Card>
      <DataTable label="Template lines" columns={lineColumns} rows={t.lines.map((l, i) => ({ ...l, n: i + 1 }))} getRowKey={(l) => String(l.n)} />

      <Card>
        <div className={styles.stack}>
          <h3 className={styles.sectionTitle}>Run the template</h3>
          <div className={styles.toolbar}>
            <FormField id="template-period" label="Period" hint="YYYY-MM">
              {(c) => <Input {...c} className={styles.mono} value={period} onChange={(e) => setPeriod(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button onClick={preview} loading={busy === "dry"}>
              Dry run
            </Button>
            <Button variant="primary" onClick={run} loading={busy === "run"}>
              Run for {isPeriod(period) ? period : "the period"}
            </Button>
          </div>
          <Alert tone="danger" live="alert">
            {failure}
          </Alert>
          {dry ? (
            <>
              <StatGroup label="Dry run totals">
                <StatTile label={`Receivers get (${t.currency})`} value={formatAmount(dry.totalIn)} />
                <StatTile label="Sender credited" value={formatAmount(dry.totalIn)} />
                <StatTile label="Warnings" value={dry.warnings.length} tone="warning" />
              </StatGroup>
              {dry.warnings.length > 0 ? (
                <Alert tone="warning" title="Zero divisors">
                  <ul className={styles.errorList}>
                    {dry.warnings.map((w) => (
                      <li key={w}>{w}</li>
                    ))}
                  </ul>
                </Alert>
              ) : null}
              <DataTable label="Dry run" columns={resultColumns} rows={dry.lines} getRowKey={(l, i) => `${l.receiverCostObjectId}-${i}`} />
            </>
          ) : null}
        </div>
      </Card>

      <h3 className={styles.sectionTitle}>Runs</h3>
      {runs.error ? (
        <LoadError title="Could not load the runs" error={runs.error} onRetry={runs.reload} />
      ) : (
        <DataTable
          label="Template runs"
          columns={runColumns}
          rows={runs.data?.items ?? []}
          getRowKey={(r) => r.id}
          loading={runs.loading && !runs.data}
          empty={{ title: "Not run yet", description: "Dry-run a period to check the amounts, then run it." }}
          onRowOpen={(r) => navigate(`/costacct/template-runs/${r.id}`)}
        />
      )}
    </div>
  );
}

function resultColumnsFor(objectCode: (id: string) => string, elementCode: (id: string) => string, currency: string): DataTableColumn<TemplateLineResult>[] {
  return [
    { key: "receiverCostObjectId", header: "Receiver", mono: true, render: (l) => objectCode(l.receiverCostObjectId) },
    { key: "secondaryElementId", header: "Element", mono: true, render: (l) => elementCode(l.secondaryElementId) },
    { key: "amount", header: `Amount (${currency})`, align: "end", mono: true, render: (l) => formatAmount(l.amount) },
    { key: "divByZero", header: "Zero divisor", render: (l) => (l.divByZero ? <Badge tone="warning">CO-0622</Badge> : dash) },
  ];
}

/** One completed template run: what each receiver got and what the sender was credited. */
export function TemplateRunDetailPage() {
  const { id = "" } = useParams();
  const run = useLoad((signal) => getTemplateRun(id, signal), id);
  const { objectCode, elementCode } = useLedger(run.data?.legalEntityId ?? "");
  if (run.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Template run" />
        <LoadError title="Could not load this template run" error={run.error} onRetry={run.reload} />
      </div>
    );
  }
  if (!run.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Template run" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const r = run.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Template run ${r.period}`}
        description="Each receiver was debited its line amount and the sender credited the same on the line's secondary element, so the run nets to zero."
        meta={<Badge tone="success">Completed</Badge>}
      />
      <StatGroup label="Run totals">
        <StatTile label={`Receivers (${r.currency})`} value={formatAmount(r.total)} />
        <StatTile label="Sender credited" value={formatAmount(r.total)} />
        <StatTile label="Lines" value={r.lines.length} />
        <StatTile label="Warnings" value={r.warnings.length} tone="warning" />
      </StatGroup>
      <Card>
        <dl className={styles.details}>
          <Detail term="Template" mono>
            <Link to={`/costacct/templates/${r.templateId}`}>Version {r.templateVersion}</Link>
          </Detail>
          <Detail term="Sender" mono>
            <Link to={`/costacct/cost-objects/${r.senderCostObjectId}`}>{objectCode(r.senderCostObjectId)}</Link>
          </Detail>
          <Detail term="Run at">{formatDateTime(r.createdAt)}</Detail>
        </dl>
      </Card>
      {r.warnings.length > 0 ? (
        <Alert tone="warning" title="Zero divisors">
          <ul className={styles.errorList}>
            {r.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      <DataTable label="Run lines" columns={resultColumnsFor(objectCode, elementCode, r.currency)} rows={r.lines} getRowKey={(l, i) => `${l.receiverCostObjectId}-${i}`} />
    </div>
  );
}
