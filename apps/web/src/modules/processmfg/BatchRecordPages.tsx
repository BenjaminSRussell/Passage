import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
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
  type BadgeTone,
  type DataTableColumn,
} from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  ATTRIBUTE_KINDS,
  changeBatchDisposition,
  defineAttribute,
  describeProblem,
  DISPOSITIONS,
  getBatch,
  listAttributeDefinitions,
  listBatches,
  newKey,
  recordBatchAttributes,
  registerBatch,
  type AttributeKind,
  type AttributeRole,
  type AttributeValue,
  type Batch,
  type Disposition,
  type DispositionChange,
  type Problem,
  type RecordedValue,
  type StoredAttributeDefinition,
} from "./api/client";
import { GenealogyCard } from "./GenealogyCard";
import { dash, formatDateTime, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Duty that changes a batch's disposition; enforced by the service (AC-24.10). */
export const DISPOSITION_DUTY = "production.batch-disposition.write";

const DISPOSITION_LABELS: Record<Disposition, string> = {
  available: "Available",
  "on-hold": "On hold",
  quarantine: "Quarantine",
  rejected: "Rejected",
};

const DISPOSITION_TONES: Record<Disposition, BadgeTone> = {
  available: "success",
  "on-hold": "warning",
  quarantine: "warning",
  rejected: "danger",
};

const KIND_LABELS: Record<AttributeKind, string> = { numeric: "Numeric", text: "Text", enum: "Enum", date: "Date" };
const ROLE_LABELS: Record<AttributeRole, string> = { generic: "Generic", potency: "Potency" };

const NUMBER = /^-?\d{1,12}(\.\d{1,6})?$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

export function DispositionBadge({ disposition }: { disposition: Disposition }) {
  return <Badge tone={DISPOSITION_TONES[disposition] ?? "neutral"}>{DISPOSITION_LABELS[disposition] ?? disposition}</Badge>;
}

function LoadError({ title, error, reload }: { title: string; error: Problem; reload: () => void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
      {error.message} ({error.code})
    </Alert>
  );
}

function valueText(v: AttributeValue): string {
  return v.num ?? v.text ?? v.date ?? dash;
}

const batchColumns: DataTableColumn<Batch>[] = [
  { key: "batchNumber", header: "Batch", mono: true },
  { key: "itemId", header: "Item", mono: true, render: (b) => shortId(b.itemId) },
  { key: "disposition", header: "Disposition", render: (b) => <DispositionBadge disposition={b.disposition} /> },
  { key: "expiryDate", header: "Expiry", render: (b) => b.expiryDate ?? dash },
  { key: "createdAt", header: "Registered", render: (b) => formatDateTime(b.createdAt) },
];

/** Batches with their disposition, newest first (FR-24.5, AC-24.10). */
export function BatchesPage() {
  const navigate = useNavigate();
  const [disposition, setDisposition] = useState<Disposition | "">("");
  const state = useLoad((signal) => listBatches({ disposition }, signal), `batches|${disposition}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Batches"
        description="Quality records of batches: recorded attributes, expiry and disposition. A batch that is not available is not picked."
        actions={
          <Link className={buttonClassName("primary")} to="/processmfg/batches/new">
            Register batch
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="batch-disposition-filter" label="Disposition">
          {(c) => (
            <Select {...c} value={disposition} onChange={(e) => setDisposition(e.target.value as Disposition | "")}>
              <option value="">All dispositions</option>
              {DISPOSITIONS.map((d) => (
                <option key={d} value={d}>
                  {DISPOSITION_LABELS[d]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load batches" error={state.error} reload={state.reload} />
      ) : (
        <DataTable
          label="Batches"
          columns={batchColumns}
          rows={state.data ?? []}
          getRowKey={(b) => b.id}
          loading={state.loading}
          empty={
            disposition
              ? { title: "No batches with this disposition", description: "Choose another disposition, or all dispositions." }
              : { title: "No batches yet", description: "Register a batch to record its attributes and disposition." }
          }
          onRowOpen={(b) => navigate(`/processmfg/batches/${b.id}`)}
        />
      )}
    </div>
  );
}

/** Register a batch for quality records. */
export function NewBatchPage() {
  const navigate = useNavigate();
  const [itemId, setItemId] = useState("");
  const [batchNumber, setBatchNumber] = useState("");
  const [disposition, setDisposition] = useState<Disposition>("available");
  const [errors, setErrors] = useState<{ itemId?: string; batchNumber?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: typeof errors = {};
    if (!UUID.test(itemId.trim())) found.itemId = "Enter the item id as a UUID.";
    const number = batchNumber.trim();
    if (number === "" || number.length > 40) found.batchNumber = "Enter a batch number of 1 to 40 characters.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const batch = await registerBatch({ itemId: itemId.trim(), batchNumber: number, disposition }, key.current);
      navigate(`/processmfg/batches/${batch.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Register batch" description="Creates the quality record of a batch. Attributes and disposition are recorded on the batch afterwards." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="batch-item" label="Item id" error={errors.itemId}>
            {(c) => (
              <Input {...c} value={itemId} onChange={(e) => { setItemId(e.target.value); key.current = newKey(); }} invalid={Boolean(errors.itemId)} />
            )}
          </FormField>
          <FormField id="batch-number" label="Batch number" error={errors.batchNumber}>
            {(c) => (
              <Input {...c} value={batchNumber} onChange={(e) => { setBatchNumber(e.target.value); key.current = newKey(); }} invalid={Boolean(errors.batchNumber)} />
            )}
          </FormField>
          <FormField id="batch-start-disposition" label="Disposition">
            {(c) => (
              <Select {...c} value={disposition} onChange={(e) => { setDisposition(e.target.value as Disposition); key.current = newKey(); }}>
                {DISPOSITIONS.map((d) => (
                  <option key={d} value={d}>
                    {DISPOSITION_LABELS[d]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not register the batch" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Register batch
            </Button>
            <Link className={buttonClassName("ghost")} to="/processmfg/batches">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

const valueColumns: DataTableColumn<RecordedValue>[] = [
  { key: "name", header: "Attribute", mono: true },
  { key: "value", header: "Value", align: "end", render: (v) => valueText(v) },
  { key: "pass", header: "Result", render: (v) => (v.pass ? <Badge tone="success">Within limits</Badge> : <Badge tone="warning">Outside limits</Badge>) },
  { key: "detail", header: "Detail", render: (v) => v.detail || dash },
  { key: "recordedAt", header: "Recorded", render: (v) => `${formatDateTime(v.recordedAt)} by ${v.recordedBy || "unknown"}` },
];

const changeColumns: DataTableColumn<DispositionChange>[] = [
  { key: "changedAt", header: "When", render: (c) => formatDateTime(c.changedAt) },
  { key: "from", header: "From", render: (c) => DISPOSITION_LABELS[c.from] ?? c.from },
  { key: "to", header: "To", render: (c) => DISPOSITION_LABELS[c.to] ?? c.to },
  { key: "reason", header: "Reason" },
  { key: "changedBy", header: "By" },
  {
    key: "segregationConflict",
    header: "Segregation",
    render: (c) => (c.segregationConflict ? <Badge tone="warning">Same user recorded attributes</Badge> : dash),
  },
];

/** The value the user typed for one definition, turned into the wire shape, or an error. */
function toValue(def: StoredAttributeDefinition, raw: string): { value?: AttributeValue; error?: string } {
  const text = raw.trim();
  if (text === "") return {};
  switch (def.kind) {
    case "numeric":
      return NUMBER.test(text) ? { value: { num: text } } : { error: "Enter a number with at most 6 decimals." };
    case "date":
      return DATE.test(text) ? { value: { date: text } } : { error: "Enter a date as yyyy-mm-dd." };
    default:
      return text.length <= 200 ? { value: { text } } : { error: "At most 200 characters." };
  }
}

function RecordCard({
  batch,
  defs,
  reload,
  onRecorded,
}: {
  batch: Batch;
  defs: StoredAttributeDefinition[];
  reload: () => void;
  onRecorded: (b: Batch, failures: string[]) => void;
}) {
  const [entered, setEntered] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const values: Record<string, AttributeValue> = {};
    for (const def of defs) {
      const r = toValue(def, entered[def.name] ?? "");
      if (r.error) found[def.name] = r.error;
      else if (r.value) values[def.name] = r.value;
    }
    if (Object.keys(found).length === 0 && Object.keys(values).length === 0) found._form = "Enter at least one value.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const result = await recordBatchAttributes(batch.id, { version: batch.version, values }, key.current);
      key.current = newKey();
      setEntered({});
      onRecorded(result.batch, result.checks.filter((c) => !c.pass).map((c) => c.detail || c.name));
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (defs.length === 0) {
    return (
      <Card title="Record attributes">
        <p className={styles.note}>
          This item defines no batch attributes yet. <Link to={`/processmfg/attribute-definitions?itemId=${batch.itemId}`}>Define attributes for the item</Link>.
        </p>
      </Card>
    );
  }
  return (
    <Card title="Record attributes">
      <form className={styles.form} onSubmit={submit} noValidate>
        {defs.map((def) => {
          const limits = def.kind === "numeric" && (def.min || def.max) ? ` (${def.min ?? "…"} to ${def.max ?? "…"})` : "";
          const set = (v: string) => {
            setEntered({ ...entered, [def.name]: v });
            key.current = newKey();
          };
          return (
            <FormField
              key={def.id}
              id={`attr-${def.name}`}
              label={`${def.name}${limits}`}
              hint={def.role === "potency" ? `${KIND_LABELS[def.kind]}, potency` : KIND_LABELS[def.kind]}
              error={errors[def.name]}
            >
              {(c) =>
                def.kind === "enum" ? (
                  <Select {...c} value={entered[def.name] ?? ""} onChange={(e) => set(e.target.value)}>
                    <option value="">Not recorded</option>
                    {def.options.map((o) => (
                      <option key={o} value={o}>
                        {o}
                      </option>
                    ))}
                  </Select>
                ) : (
                  <Input
                    {...c}
                    inputMode={def.kind === "numeric" ? "decimal" : undefined}
                    placeholder={def.kind === "date" ? "yyyy-mm-dd" : undefined}
                    value={entered[def.name] ?? ""}
                    onChange={(e) => set(e.target.value)}
                    invalid={Boolean(errors[def.name])}
                  />
                )
              }
            </FormField>
          );
        })}
        {errors._form ? <p role="alert" className={styles.fieldError}>{errors._form}</p> : null}
        {failure ? (
          <Alert
            tone="danger"
            title="Could not record the attributes"
            live="alert"
            action={failure.code === "PMF-0104" ? <Button size="sm" onClick={reload}>Reload batch</Button> : undefined}
          >
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Record attributes
          </Button>
        </div>
      </form>
    </Card>
  );
}

function DispositionCard({ batch, reload, onChanged }: { batch: Batch; reload: () => void; onChanged: (b: Batch) => void }) {
  const allowed = holdsDuty(usePrincipalDuties(), DISPOSITION_DUTY);
  const [to, setTo] = useState<Disposition>(batch.disposition === "available" ? "on-hold" : "available");
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (!allowed) {
    return (
      <Card title="Change disposition">
        <p className={styles.note}>Changing a batch's disposition needs the batch disposition duty, which is separate from recording attributes. Ask your administrator if you should have it.</p>
      </Card>
    );
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (to === batch.disposition) {
      setError(`The batch is already ${DISPOSITION_LABELS[to].toLowerCase()}.`);
      return;
    }
    if (reason.trim() === "" || reason.length > 500) {
      setError("Give a reason of 1 to 500 characters.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      const updated = await changeBatchDisposition(batch.id, { version: batch.version, disposition: to, reason: reason.trim() }, key.current);
      key.current = newKey();
      setReason("");
      onChanged(updated);
    } catch (e) {
      const problem = describeProblem(e);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }
  return (
    <Card title="Change disposition">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="disposition-to" label="New disposition">
          {(c) => (
            <Select {...c} value={to} onChange={(e) => { setTo(e.target.value as Disposition); key.current = newKey(); }}>
              {DISPOSITIONS.map((d) => (
                <option key={d} value={d}>
                  {DISPOSITION_LABELS[d]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="disposition-reason" label="Reason" error={error ?? undefined}>
          {(c) => <Textarea {...c} value={reason} onChange={(e) => { setReason(e.target.value); key.current = newKey(); }} invalid={Boolean(error)} />}
        </FormField>
        {failure ? (
          <Alert
            tone="danger"
            title="Could not change the disposition"
            live="alert"
            action={failure.code === "PMF-0104" ? <Button size="sm" onClick={reload}>Reload batch</Button> : undefined}
          >
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Change disposition
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** One batch: facts, current attributes, recording, disposition and both histories. */
export function BatchDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getBatch(id, signal), id);
  const itemId = state.data?.itemId ?? "";
  const defs = useLoad((signal) => (itemId ? listAttributeDefinitions(itemId, signal) : Promise.resolve([])), `defs|${itemId}`);
  const [notice, setNotice] = useState<{ tone: "success" | "warning"; text: string } | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Batch" />
        <LoadError title="Could not load the batch" error={state.error} reload={state.reload} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Batch" />
        <span className="visually-hidden">Loading the batch…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const batch = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader title={batch.batchNumber} description={`Item ${batch.itemId}`} meta={<DispositionBadge disposition={batch.disposition} />} />
      <StatGroup label="Batch facts">
        <StatTile label="Disposition" value={DISPOSITION_LABELS[batch.disposition]} />
        <StatTile label="Expiry" value={batch.expiryDate ?? dash} />
        <StatTile label="Recorded attributes" value={String(batch.attributes?.length ?? 0)} />
      </StatGroup>
      {notice ? <Alert tone={notice.tone} title={notice.text} live="status" /> : null}
      <Card title="Current attributes">
        <DataTable
          label="Current batch attributes"
          columns={valueColumns}
          rows={batch.attributes ?? []}
          getRowKey={(v) => v.name}
          empty={{ title: "No attributes recorded", description: "Record the item's attributes below." }}
        />
      </Card>
      {defs.error ? (
        <LoadError title="Could not load the attribute definitions" error={defs.error} reload={defs.reload} />
      ) : defs.loading ? (
        <Skeleton height="tile" />
      ) : (
        <RecordCard
          batch={batch}
          defs={defs.data ?? []}
          reload={state.reload}
          onRecorded={(_updated, failures) => {
            setNotice(
              failures.length > 0
                ? { tone: "warning", text: `Recorded. ${failures.length} value${failures.length === 1 ? " is" : "s are"} outside limits: ${failures.join("; ")}.` }
                : { tone: "success", text: "Attributes recorded." },
            );
            state.reload();
          }}
        />
      )}
      <DispositionCard
        key={`${batch.disposition}|${batch.version}`}
        batch={batch}
        reload={state.reload}
        onChanged={(b) => {
          setNotice({ tone: "success", text: `Disposition is now ${DISPOSITION_LABELS[b.disposition].toLowerCase()}.` });
          state.reload();
        }}
      />
      <Card title="Disposition history">
        <DataTable
          label="Disposition history"
          columns={changeColumns}
          rows={batch.dispositions ?? []}
          getRowKey={(c) => `${c.changedAt}|${c.to}`}
          empty={{ title: "No disposition changes", description: "The batch still has the disposition it was registered with." }}
        />
      </Card>
      <GenealogyCard batchId={batch.id} />
      <Card title="Attribute history">
        <DataTable
          label="Attribute history"
          columns={valueColumns}
          rows={batch.attributeHistory ?? []}
          getRowKey={(v) => `${v.name}|${v.recordedAt}|${valueText(v)}`}
          empty={{ title: "Nothing recorded yet", description: "Every value recorded on the batch is kept here." }}
        />
      </Card>
      <p className={styles.muted}>{`Registered ${formatDateTime(batch.createdAt)} by ${batch.createdBy || "unknown"} · updated ${formatDateTime(batch.updatedAt)} · version ${batch.version}`}</p>
    </div>
  );
}

const definitionColumns: DataTableColumn<StoredAttributeDefinition>[] = [
  { key: "itemId", header: "Item", mono: true, render: (d) => shortId(d.itemId) },
  { key: "name", header: "Attribute", mono: true },
  { key: "kind", header: "Kind", render: (d) => KIND_LABELS[d.kind] },
  { key: "role", header: "Role", render: (d) => ROLE_LABELS[d.role] ?? d.role },
  { key: "limits", header: "Limits", render: (d) => (d.kind === "numeric" ? `${d.min ?? "…"} to ${d.max ?? "…"}${d.target ? `, target ${d.target}` : ""}` : d.kind === "enum" ? d.options.join(", ") : dash) },
];

/** Attribute definitions per item, with a form to add one (FR-24.5). */
export function AttributeDefinitionsPage() {
  const [params] = useSearchParams();
  const initial = params.get("itemId") ?? "";
  const [filter, setFilter] = useState(initial);
  const filterValid = filter.trim() === "" || UUID.test(filter.trim());
  const state = useLoad((signal) => listAttributeDefinitions(filterValid ? filter.trim() : "", signal), `definitions|${filterValid ? filter.trim() : ""}`);
  const [form, setForm] = useState({ itemId: initial, name: "", kind: "numeric" as AttributeKind, role: "generic" as AttributeRole, min: "", max: "", target: "", options: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<string | null>(null);
  const key = useRef(newKey());

  function edit(patch: Partial<typeof form>) {
    setForm({ ...form, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (!UUID.test(form.itemId.trim())) found.itemId = "Enter the item id as a UUID.";
    const name = form.name.trim();
    if (name === "" || name.length > 40) found.name = "Enter a name of 1 to 40 characters.";
    if (form.kind === "numeric") {
      for (const f of ["min", "max", "target"] as const) {
        if (form[f].trim() !== "" && !NUMBER.test(form[f].trim())) found[f] = "Enter a number with at most 6 decimals.";
      }
    }
    const options = form.options.split(",").map((o) => o.trim()).filter(Boolean);
    if (form.kind === "enum" && options.length === 0) found.options = "List the allowed values, separated by commas.";
    setErrors(found);
    setFailure(null);
    setDone(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await defineAttribute(
        {
          itemId: form.itemId.trim(),
          name,
          kind: form.kind,
          ...(form.kind === "numeric" && form.min.trim() ? { min: form.min.trim() } : {}),
          ...(form.kind === "numeric" && form.max.trim() ? { max: form.max.trim() } : {}),
          ...(form.kind === "numeric" && form.target.trim() ? { target: form.target.trim() } : {}),
          ...(form.kind === "numeric" && form.role === "potency" ? { role: "potency" as const } : {}),
          ...(form.kind === "enum" ? { options } : {}),
        },
        key.current,
      );
      key.current = newKey();
      setDone(`Defined ${name}.`);
      setForm({ ...form, name: "", role: "generic", min: "", max: "", target: "", options: "" });
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.code !== "COM-0500") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Batch attribute definitions" description="The attributes an item's batches carry, with limits for numbers and options for enums." />
      <div className={styles.toolbar}>
        <FormField id="definition-filter" label="Item id" hint="Leave empty for every item." error={filterValid ? undefined : "Enter the item id as a UUID."}>
          {(c) => <Input {...c} value={filter} onChange={(e) => setFilter(e.target.value)} invalid={!filterValid} />}
        </FormField>
      </div>
      {state.error ? (
        <LoadError title="Could not load the definitions" error={state.error} reload={state.reload} />
      ) : (
        <DataTable
          label="Batch attribute definitions"
          columns={definitionColumns}
          rows={state.data ?? []}
          getRowKey={(d) => d.id}
          loading={state.loading}
          empty={{ title: "No attribute definitions", description: "Define the first attribute with the form below." }}
        />
      )}
      <Card title="Define an attribute">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="def-item" label="Item id" error={errors.itemId}>
            {(c) => <Input {...c} value={form.itemId} onChange={(e) => edit({ itemId: e.target.value })} invalid={Boolean(errors.itemId)} />}
          </FormField>
          <FormField id="def-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={form.name} onChange={(e) => edit({ name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="def-kind" label="Kind">
            {(c) => (
              <Select {...c} value={form.kind} onChange={(e) => edit({ kind: e.target.value as AttributeKind })}>
                {ATTRIBUTE_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {KIND_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {form.kind === "numeric" ? (
            <>
              <FormField id="def-role" label="Role" hint="One potency attribute per item carries its active-ingredient percentage.">
                {(c) => (
                  <Select {...c} value={form.role} onChange={(e) => edit({ role: e.target.value as AttributeRole })}>
                    <option value="generic">{ROLE_LABELS.generic}</option>
                    <option value="potency">{ROLE_LABELS.potency}</option>
                  </Select>
                )}
              </FormField>
              <FormField id="def-min" label="Minimum" error={errors.min}>
                {(c) => <Input {...c} inputMode="decimal" value={form.min} onChange={(e) => edit({ min: e.target.value })} invalid={Boolean(errors.min)} />}
              </FormField>
              <FormField id="def-max" label="Maximum" error={errors.max}>
                {(c) => <Input {...c} inputMode="decimal" value={form.max} onChange={(e) => edit({ max: e.target.value })} invalid={Boolean(errors.max)} />}
              </FormField>
              <FormField id="def-target" label="Target" error={errors.target}>
                {(c) => <Input {...c} inputMode="decimal" value={form.target} onChange={(e) => edit({ target: e.target.value })} invalid={Boolean(errors.target)} />}
              </FormField>
            </>
          ) : null}
          {form.kind === "enum" ? (
            <FormField id="def-options" label="Options" hint="Separated by commas." error={errors.options}>
              {(c) => <Input {...c} value={form.options} onChange={(e) => edit({ options: e.target.value })} invalid={Boolean(errors.options)} />}
            </FormField>
          ) : null}
          {done ? <Alert tone="success" title={done} live="status" /> : null}
          {failure ? (
            <Alert tone="danger" title="Could not define the attribute" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Define attribute
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
