import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, Skeleton, buttonClassName, type DataTableColumn } from "../../shell/ui";
import {
  ProcurementApiError,
  changeModelServiceSpec,
  checkServiceLimit,
  createModelServiceSpec,
  deleteServiceSpec,
  describeProblem,
  getModelServiceSpec,
  getPurchaseOrder,
  getServiceSpec,
  listModelServiceSpecs,
  newIdempotencyKey,
  putServiceSpec,
  type ModelServiceSpec,
  type ModelServiceSpecInput,
  type ModelServiceStatus,
  type OutlineKind,
  type OutlineRow,
  type PurchaseOrderLine,
  type ServiceLimitCheck,
  type ServiceSpecInput,
  type ServiceSpecResult,
} from "./api/client";
import { MAX_DEPTH, MAX_NODES, draftFromOutline, newOutlineRow, toOutlineInput, type OutlineDraftRow } from "./outline";
import { Details, LoadError, ShortId, StatusBadge, currencyPattern, dash, decimalPattern, statusLabel, useResource, uuidPattern, type Failure } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const STATUSES: ModelServiceStatus[] = ["ACTIVE", "INACTIVE"];
const hasScale = (v: string) => (v.split(".")[1] ?? "").length <= 6;

const outlineColumns = (currency: string): DataTableColumn<OutlineRow>[] => [
  { key: "seq", header: "No.", align: "end" },
  {
    key: "description",
    header: "Service",
    render: (r) => (
      <span style={{ paddingInlineStart: `${(r.depth - 1) * 1.25}rem`, fontWeight: r.kind === "LEVEL" ? 600 : undefined }}>
        {r.description || <ShortId id={r.productId} />}
      </span>
    ),
  },
  { key: "kind", header: "Kind", render: (r) => (r.kind === "LEVEL" ? "Level" : "Service item") },
  { key: "qty", header: "Quantity", align: "end", render: (r) => (r.kind === "ITEM" ? `${r.qty} ${r.unit ?? ""}` : dash) },
  { key: "unitPrice", header: "Unit price", align: "end", render: (r) => (r.kind === "ITEM" ? r.unitPrice : dash) },
  { key: "amount", header: `Amount (${currency})`, align: "end" },
];

/** The outline as a read-only table, indented by depth. */
export function OutlineTable({ rows, currency }: { rows: OutlineRow[]; currency: string }) {
  return (
    <DataTable
      label="Service outline"
      columns={outlineColumns(currency)}
      rows={rows}
      getRowKey={(r) => String(r.seq)}
      empty={{ title: "No planned services", description: "The line has a limit for unplanned services and no outline." }}
    />
  );
}

/**
 * Edits an outline as a flat list of rows with a depth: a level groups the rows below it that are
 * deeper than it, a service item is a leaf. The service computes amounts and checks the sums.
 */
export function OutlineEditor({ idPrefix, rows, onChange, errors }: { idPrefix: string; rows: OutlineDraftRow[]; onChange: (rows: OutlineDraftRow[]) => void; errors: Errors }) {
  const set = (i: number, patch: Partial<OutlineDraftRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className={styles.form}>
      {rows.length === 0 ? (
        <EmptyState icon="receipt" title="No planned services" description="Add levels and service items, or leave the outline empty and rely on the unplanned limit." compact />
      ) : null}
      {rows.map((r, i) => (
        <fieldset key={r.key} className={styles.lineCard} aria-label={`Outline row ${i + 1}`}>
          <div className={styles.lineHead}>
            <p className={styles.lineTitle}>
              Row {i + 1}: {r.kind === "LEVEL" ? "level" : "service item"}
            </p>
            <Button size="sm" variant="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
              Remove row {i + 1}
            </Button>
          </div>
          <div className={styles.grid}>
            <FormField id={`${idPrefix}-${i}-kind`} label="Kind" error={errors[`outline[${i}].kind`]}>
              {(c) => (
                <Select {...c} value={r.kind} onChange={(e) => set(i, { kind: e.target.value as OutlineKind })}>
                  <option value="ITEM">Service item</option>
                  <option value="LEVEL">Level</option>
                </Select>
              )}
            </FormField>
            <FormField id={`${idPrefix}-${i}-depth`} label="Nesting level" error={errors[`outline[${i}].depth`]}>
              {(c) => (
                <Select {...c} value={String(r.depth)} onChange={(e) => set(i, { depth: Number(e.target.value) })}>
                  {Array.from({ length: MAX_DEPTH }, (_, d) => d + 1).map((d) => (
                    <option key={d} value={d}>
                      {d === 1 ? "1 (top level)" : String(d)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id={`${idPrefix}-${i}-description`} label="Description" error={errors[`outline[${i}].description`]}>
              {(c) => <Input {...c} value={r.description} onChange={(e) => set(i, { description: e.target.value })} />}
            </FormField>
            {r.kind === "ITEM" ? (
              <>
                <FormField id={`${idPrefix}-${i}-product`} label="Service product id" optional error={errors[`outline[${i}].productId`]}>
                  {(c) => <Input {...c} value={r.productId} onChange={(e) => set(i, { productId: e.target.value })} />}
                </FormField>
                <FormField id={`${idPrefix}-${i}-qty`} label="Quantity" error={errors[`outline[${i}].qty`]}>
                  {(c) => <Input {...c} inputMode="decimal" value={r.qty} onChange={(e) => set(i, { qty: e.target.value })} />}
                </FormField>
                <FormField id={`${idPrefix}-${i}-unit`} label="Unit" error={errors[`outline[${i}].unit`]}>
                  {(c) => <Input {...c} value={r.unit} maxLength={20} onChange={(e) => set(i, { unit: e.target.value })} />}
                </FormField>
                <FormField id={`${idPrefix}-${i}-price`} label="Unit price" error={errors[`outline[${i}].unitPrice`]}>
                  {(c) => <Input {...c} inputMode="decimal" value={r.unitPrice} onChange={(e) => set(i, { unitPrice: e.target.value })} />}
                </FormField>
              </>
            ) : null}
          </div>
        </fieldset>
      ))}
      <div className={styles.actions}>
        <Button size="sm" disabled={rows.length >= MAX_NODES} onClick={() => onChange([...rows, newOutlineRow({ depth: rows.length > 0 ? rows[rows.length - 1].depth : 1 })])}>
          Add row
        </Button>
      </div>
      {errors.outline ? (
        <p className={styles.muted} role="alert">
          {errors.outline}
        </p>
      ) : null}
    </div>
  );
}

// --- The specification of one PO line (FR-17.23) ---

export interface SpecForm {
  valueLimit: string;
  expectedValue: string;
  contractAgreementLineId: string;
  modelId: string;
}

export const blankSpec: SpecForm = { valueLimit: "0", expectedValue: "0", contractAgreementLineId: "", modelId: "" };

export function validateSpec(f: SpecForm): Errors {
  const e: Errors = {};
  if (!decimalPattern.test(f.valueLimit.trim()) || !hasScale(f.valueLimit.trim())) e.valueLimit = "Enter the limit as a decimal; 0 allows no unplanned services.";
  if (!decimalPattern.test(f.expectedValue.trim()) || !hasScale(f.expectedValue.trim())) e.expectedValue = "Enter a decimal value.";
  else if (!e.valueLimit && Number(f.expectedValue) > Number(f.valueLimit)) e.expectedValue = "The expected value cannot be above the limit.";
  if (f.contractAgreementLineId.trim() !== "" && !uuidPattern.test(f.contractAgreementLineId.trim())) e.contractAgreementLineId = "Enter a UUID or leave blank.";
  return e;
}

/** Builds the PUT body from the form and the outline rows (or the model whose outline is copied). */
export function toSpecInput(f: SpecForm, rows: OutlineDraftRow[]): { input?: ServiceSpecInput; errors: Errors } {
  const errors = validateSpec(f);
  const built = f.modelId === "" ? toOutlineInput(rows) : { errors: {} as Errors, outline: undefined };
  Object.assign(errors, built.errors);
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      valueLimit: f.valueLimit.trim(),
      expectedValue: f.expectedValue.trim(),
      contractAgreementLineId: f.contractAgreementLineId.trim() || undefined,
      modelId: f.modelId || undefined,
      outline: f.modelId === "" ? built.outline : undefined,
    },
  };
}

async function loadSpec(poId: string, lineNo: number, signal: AbortSignal) {
  try {
    return (await getServiceSpec(poId, lineNo, signal)).body;
  } catch (error) {
    if (error instanceof ProcurementApiError && error.problem.code === "PRC-SRV-NOT-FOUND") return null;
    throw error;
  }
}

/**
 * The service specification of one PO line: the outline of planned services, the limit for
 * unplanned services, the optional contract limit and a model to copy the outline from. It can
 * be changed while the order is a draft or was rejected. A check shows whether an entry of
 * unplanned services would fit, the way receiving asks before it accepts a service entry sheet.
 */
export function ServiceSpecPage() {
  const { id = "", lineNo: rawLineNo = "" } = useParams();
  const lineNo = Number(rawLineNo);
  const po = useResource((signal) => getPurchaseOrder(id, signal), [id]);
  const loaded = useResource((signal) => loadSpec(id, lineNo, signal), [id, lineNo]);
  const models = useResource(async (signal) => (await listModelServiceSpecs({ status: "ACTIVE" }, { limit: 100, signal })).items, []);
  const [spec, setSpec] = useState<ServiceSpecResult | null | undefined>(undefined);
  const [rowVersion, setRowVersion] = useState<number | null>(null);
  const [form, setForm] = useState<SpecForm>(blankSpec);
  const [rows, setRows] = useState<OutlineDraftRow[]>([]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [unplanned, setUnplanned] = useState("0");
  const [entry, setEntry] = useState("");
  const [check, setCheck] = useState<{ result?: ServiceLimitCheck; failure?: Failure } | null>(null);

  useEffect(() => setRowVersion(null), [id, lineNo]);
  useEffect(() => {
    if (loaded.data) {
      show(loaded.data);
    } else if (loaded.data === null && !loaded.loading && !loaded.error) {
      setSpec(null);
    }
  }, [loaded.data, loaded.loading, loaded.error]);
  useEffect(() => {
    const v = po.data?.body.rowVersion;
    if (v !== undefined) setRowVersion((current) => current ?? v);
  }, [po.data]);

  function show(next: ServiceSpecResult) {
    setSpec(next);
    setRowVersion(next.poRowVersion);
    setForm({ valueLimit: next.valueLimit, expectedValue: next.expectedValue, contractAgreementLineId: next.contractAgreementLineId ?? "", modelId: "" });
    setRows(draftFromOutline(next.outline));
  }

  const order = po.data?.body ?? null;
  const line: PurchaseOrderLine | undefined = order?.lines.find((l) => l.lineNo === lineNo);

  function fail(error: unknown) {
    const problem = describeProblem(error);
    setFailure(problem);
    if (problem.errors) setErrors(problem.errors);
  }

  async function onSave(event: FormEvent) {
    event.preventDefault();
    const built = toSpecInput(form, rows);
    setErrors(built.errors);
    setFailure(null);
    setNotice(null);
    if (!built.input || rowVersion === null) return;
    setBusy("save");
    try {
      const { body } = await putServiceSpec(id, lineNo, rowVersion, built.input);
      show(body);
      setCheck(null);
      setNotice(`Service specification saved: planned services ${body.plannedTotal} ${body.currency}, unplanned limit ${body.valueLimit} ${body.currency}.`);
    } catch (error) {
      fail(error);
    } finally {
      setBusy(null);
    }
  }

  async function onDelete() {
    if (rowVersion === null || !spec) return;
    setBusy("delete");
    setFailure(null);
    setNotice(null);
    try {
      setRowVersion(await deleteServiceSpec(id, lineNo, rowVersion));
      setSpec(null);
      setForm(blankSpec);
      setRows([]);
      setCheck(null);
      setConfirmDelete(false);
      setNotice("Service specification removed. The line is a plain line again.");
    } catch (error) {
      fail(error);
    } finally {
      setBusy(null);
    }
  }

  async function onCheck(event: FormEvent) {
    event.preventDefault();
    if (!decimalPattern.test(entry.trim()) || !decimalPattern.test(unplanned.trim())) {
      setCheck({ failure: { code: "COM-0001", message: "Enter the unplanned amount already entered and the amount of the entry as decimals." } });
      return;
    }
    setBusy("check");
    try {
      const { body } = await checkServiceLimit(id, lineNo, unplanned.trim(), entry.trim());
      setCheck({ result: body });
    } catch (error) {
      setCheck({ failure: describeProblem(error) });
    } finally {
      setBusy(null);
    }
  }

  const title = order ? `Service line: ${order.number} line ${lineNo}` : "Service line";
  const back = (
    <Link className={buttonClassName("secondary")} to={`/purchasing/purchase-orders/${id}`}>
      Back to the purchase order
    </Link>
  );
  if (po.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Service line" />
        {po.error.code === "PRC-PO-NOT-FOUND" ? (
          <Card>
            <EmptyState icon="receipt" title="Purchase order not found" description="It may belong to another legal entity, or the link is wrong." />
          </Card>
        ) : (
          <LoadError what="the purchase order" error={po.error} onRetry={po.reload} />
        )}
      </div>
    );
  }
  if (!order || spec === undefined) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Service line" />
        {loaded.error ? <LoadError what="the service specification" error={loaded.error} onRetry={loaded.reload} /> : null}
        <p className={styles.muted} role="status">
          Loading service line…
        </p>
      </div>
    );
  }
  if (!line) {
    return (
      <div className={styles.stack}>
        <PageHeader title={title} actions={back} />
        <Card>
          <EmptyState icon="receipt" title={`Line ${rawLineNo} not found`} description={`${order.number} has no line with that number.`} />
        </Card>
      </div>
    );
  }

  const editable = (order.approvalStatus === "DRAFT" || order.approvalStatus === "REJECTED") && line.status !== "CANCELLED";
  const stale = failure?.code === "PRC-PO-009";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description="A service line is paid for by service entry sheets. Plan the services as an outline, set the limit for unplanned work, and optionally bound it by the remaining value of an agreement line."
        actions={back}
      />
      <Alert tone="danger" live="alert">
        {failure ? (stale ? <ProblemMessage problem={failure} message="Someone else changed this order. Reload the page and try again." /> : <ProblemMessage problem={failure} />) : null}
      </Alert>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Line">
        <Details
          items={[
            { label: "Item", value: line.description || <ShortId id={line.productId} /> },
            { label: "Line amount", value: `${line.lineAmount} ${order.currency}`, mono: true },
            { label: "Line status", value: <StatusBadge status={line.status} /> },
            { label: "Order status", value: <StatusBadge status={order.approvalStatus} /> },
          ]}
        />
      </Card>
      {spec ? (
        <>
          <Card
            title="Specification"
            actions={
              editable ? (
                <Button size="sm" variant="ghost" onClick={() => setConfirmDelete(true)}>
                  Remove specification
                </Button>
              ) : undefined
            }
          >
            {confirmDelete ? (
              <Alert
                tone="warning"
                title="Remove this service specification?"
                action={
                  <div className={styles.actions}>
                    <Button size="sm" variant="danger" loading={busy === "delete"} onClick={() => void onDelete()}>
                      Yes, remove it
                    </Button>
                    <Button size="sm" variant="ghost" disabled={busy === "delete"} onClick={() => setConfirmDelete(false)}>
                      Keep it
                    </Button>
                  </div>
                }
              >
                The line becomes a plain line again and the outline is lost.
              </Alert>
            ) : null}
            <Details
              items={[
                { label: "Planned services", value: `${spec.plannedTotal} ${spec.currency}`, mono: true },
                { label: "Unplanned limit", value: `${spec.valueLimit} ${spec.currency}`, mono: true },
                { label: "Expected unplanned", value: `${spec.expectedValue} ${spec.currency}`, mono: true },
                { label: "Contract line", value: spec.contractAgreementLineId ? <ShortId id={spec.contractAgreementLineId} /> : dash },
                { label: "Contract remaining", value: spec.contractRemaining !== undefined ? `${spec.contractRemaining} ${spec.currency}` : dash, mono: true },
                { label: "Copied from model", value: spec.modelId ? <ShortId id={spec.modelId} /> : dash },
              ]}
            />
          </Card>
          <Card title="Planned services">
            <OutlineTable rows={spec.outline} currency={spec.currency} />
          </Card>
          <Card title="Check an entry of unplanned services">
            <form className={styles.form} onSubmit={onCheck} noValidate>
              <div className={styles.grid}>
                <FormField id="svc-unplanned" label="Unplanned already entered" hint="Accepted entry sheets, unplanned part">
                  {(c) => <Input {...c} inputMode="decimal" value={unplanned} onChange={(e) => setUnplanned(e.target.value)} />}
                </FormField>
                <FormField id="svc-entry" label="This entry">
                  {(c) => <Input {...c} inputMode="decimal" value={entry} onChange={(e) => setEntry(e.target.value)} />}
                </FormField>
              </div>
              <div className={styles.actions}>
                <Button type="submit" loading={busy === "check"}>
                  Check entry
                </Button>
              </div>
              {check?.result?.allowed ? (
                <Alert tone="success" live="status">
                  The entry fits: {check.result.valueRemaining} of the {check.result.valueLimit} limit was still free
                  {check.result.contractRemaining !== undefined ? `, and ${check.result.contractRemaining} remain on the contract line` : ""}.
                </Alert>
              ) : null}
              {check?.result && !check.result.allowed ? (
                <Alert tone="danger" live="alert">
                  {check.result.detail} ({check.result.code}). {check.result.remaining} {check.result.currency} can still be entered.
                </Alert>
              ) : null}
              {check?.failure ? (
                <Alert tone="danger" live="alert">
                  <ProblemMessage problem={check.failure} />
                </Alert>
              ) : null}
            </form>
          </Card>
        </>
      ) : (
        <Card>
          <EmptyState
            icon="receipt"
            title="Not a service line yet"
            description={editable ? "Save a specification below to make this a service line." : "A specification can be added while the order is a draft or was rejected."}
            compact
          />
        </Card>
      )}
      {!editable ? (
        <Alert tone="info" title="The specification is fixed">
          {line.status === "CANCELLED"
            ? "A cancelled line takes no service specification."
            : "The specification of an order that is in review, approved, confirmed, finalized or cancelled changes through the order's change management, not here."}
        </Alert>
      ) : (
        <form className={styles.form} onSubmit={onSave} noValidate>
          <Card title={spec ? "Replace the specification" : "Set up the specification"}>
            <div className={styles.grid}>
              <FormField id="svc-limit" label={`Unplanned limit (${order.currency})`} hint="0 allows no unplanned services" error={errors.valueLimit}>
                {(c) => <Input {...c} inputMode="decimal" value={form.valueLimit} onChange={(e) => setForm({ ...form, valueLimit: e.target.value })} />}
              </FormField>
              <FormField id="svc-expected" label={`Expected unplanned (${order.currency})`} error={errors.expectedValue}>
                {(c) => <Input {...c} inputMode="decimal" value={form.expectedValue} onChange={(e) => setForm({ ...form, expectedValue: e.target.value })} />}
              </FormField>
              <FormField id="svc-contract" label="Contract agreement line id" optional hint="An agreement line of the same vendor whose remaining value also bounds unplanned work" error={errors.contractAgreementLineId}>
                {(c) => <Input {...c} value={form.contractAgreementLineId} onChange={(e) => setForm({ ...form, contractAgreementLineId: e.target.value })} />}
              </FormField>
              <FormField id="svc-model" label="Outline from" hint="A model copies its outline into this line" error={errors.modelId}>
                {(c) => (
                  <Select {...c} value={form.modelId} onChange={(e) => setForm({ ...form, modelId: e.target.value })}>
                    <option value="">The rows below</option>
                    {(models.data ?? [])
                      .filter((m) => m.currency === order.currency && m.legalEntityId === order.legalEntityId)
                      .map((m) => (
                        <option key={m.id} value={m.id}>
                          Model {m.code}: {m.name}
                        </option>
                      ))}
                  </Select>
                )}
              </FormField>
            </div>
            {models.error ? <LoadError what="the model service specifications" error={models.error} onRetry={models.reload} /> : null}
          </Card>
          {form.modelId === "" ? (
            <Card title="Outline">
              <OutlineEditor idPrefix="svc" rows={rows} onChange={setRows} errors={errors} />
            </Card>
          ) : (
            <Alert tone="info" title="The outline is copied from the model">
              The line keeps its own copy; changing the model later does not move it.
            </Alert>
          )}
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy === "save"}>
              {spec ? "Replace specification" : "Save specification"}
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}

// --- Model service specifications ---

const modelColumns: DataTableColumn<ModelServiceSpec>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "currency", header: "Currency", mono: true },
  { key: "plannedTotal", header: "Planned total", align: "end" },
  { key: "status", header: "Status", render: (m) => <StatusBadge status={m.status} /> },
];

/** Model service specifications with status and text filters and load-more paging. */
export function ModelServiceSpecsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ModelServiceStatus | "">("");
  const [q, setQ] = useState("");
  const [cursor, setCursor] = useState<string | undefined>();
  const [rows, setRows] = useState<ModelServiceSpec[]>([]);
  const page = useResource(
    async (signal) => {
      const result = await listModelServiceSpecs({ status, q }, { cursor, signal });
      setRows((prev) => (cursor ? [...prev, ...result.items] : result.items));
      return result;
    },
    [status, q, cursor],
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Model service specifications"
        description="Reusable outlines of planned services, for example a maintenance visit, that a buyer copies into a service line instead of typing it again."
        actions={
          <Link className={buttonClassName("primary")} to="/purchasing/model-service-specs/new">
            New model
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <div className={styles.filter}>
          <FormField id="ms-status-filter" label="Status">
            {(control) => (
              <Select
                {...control}
                value={status}
                onChange={(e) => {
                  setCursor(undefined);
                  setStatus(e.target.value as ModelServiceStatus | "");
                }}
              >
                <option value="">All statuses</option>
                {STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {statusLabel(s)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
        <div className={styles.filter}>
          <FormField id="ms-text-filter" label="Code or name" optional>
            {(control) => (
              <Input
                {...control}
                value={q}
                onChange={(e) => {
                  setCursor(undefined);
                  setQ(e.target.value);
                }}
              />
            )}
          </FormField>
        </div>
      </div>
      {page.error ? <LoadError what="model service specifications" error={page.error} onRetry={page.reload} /> : null}
      <DataTable
        label="Model service specifications"
        columns={modelColumns}
        rows={page.error ? [] : rows}
        getRowKey={(m) => m.id}
        loading={page.loading && rows.length === 0}
        empty={{ title: "No model service specifications yet", description: "Create one for a service you buy often, such as a maintenance visit or an inspection." }}
        onRowOpen={(m) => navigate(`/purchasing/model-service-specs/${m.id}`)}
      />
      {page.data?.nextCursor ? (
        <div className={styles.actions}>
          <Button loading={page.loading} onClick={() => setCursor(page.data?.nextCursor)}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

interface ModelForm {
  legalEntityId: string;
  code: string;
  name: string;
  currency: string;
  status: ModelServiceStatus;
}

const blankModel: ModelForm = { legalEntityId: "", code: "", name: "", currency: "USD", status: "ACTIVE" };

export function validateModel(f: ModelForm, creating: boolean): Errors {
  const e: Errors = {};
  if (creating) {
    if (!uuidPattern.test(f.legalEntityId.trim())) e.legalEntityId = "Enter the legal entity id (a UUID).";
    if (!/^[A-Z0-9_-]{1,20}$/.test(f.code.trim().toUpperCase())) e.code = "Use 1 to 20 letters, digits, _ or -.";
  }
  if (f.name.trim() === "") e.name = "Enter the name.";
  if (!currencyPattern.test(f.currency.trim())) e.currency = "Enter a three-letter currency code in capitals.";
  return e;
}

function toModelInput(f: ModelForm, rows: OutlineDraftRow[], creating: boolean): { input?: ModelServiceSpecInput; errors: Errors } {
  const built = toOutlineInput(rows);
  const errors = { ...validateModel(f, creating), ...built.errors };
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      ...(creating ? { legalEntityId: f.legalEntityId.trim(), code: f.code.trim().toUpperCase() } : {}),
      name: f.name.trim(),
      currency: f.currency.trim(),
      status: f.status,
      outline: built.outline,
    },
  };
}

function ModelFields({ id, form, set, errors, creating }: { id: string; form: ModelForm; set: (patch: Partial<ModelForm>) => void; errors: Errors; creating: boolean }) {
  return (
    <div className={styles.grid}>
      {creating ? (
        <>
          <FormField id={`${id}-legal-entity`} label="Legal entity id" error={errors.legalEntityId}>
            {(c) => <Input {...c} value={form.legalEntityId} onChange={(e) => set({ legalEntityId: e.target.value })} />}
          </FormField>
          <FormField id={`${id}-code`} label="Code" hint="Upper case, for example MAINT-VISIT" error={errors.code}>
            {(c) => <Input {...c} value={form.code} maxLength={20} onChange={(e) => set({ code: e.target.value.toUpperCase() })} />}
          </FormField>
        </>
      ) : null}
      <FormField id={`${id}-name`} label="Name" error={errors.name}>
        {(c) => <Input {...c} value={form.name} onChange={(e) => set({ name: e.target.value })} />}
      </FormField>
      <FormField id={`${id}-currency`} label="Currency" hint="Prices are in this currency; a line takes the model only on an order in it" error={errors.currency}>
        {(c) => <Input {...c} value={form.currency} maxLength={3} onChange={(e) => set({ currency: e.target.value.toUpperCase() })} />}
      </FormField>
      {creating ? null : (
        <FormField id={`${id}-status`} label="Status" hint="An inactive model cannot be copied into a line">
          {(c) => (
            <Select {...c} value={form.status} onChange={(e) => set({ status: e.target.value as ModelServiceStatus })}>
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {statusLabel(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      )}
    </div>
  );
}

/** Create form for a model service specification. */
export function ModelServiceSpecCreatePage() {
  const navigate = useNavigate();
  const [form, setForm] = useState<ModelForm>(blankModel);
  const [rows, setRows] = useState<OutlineDraftRow[]>([newOutlineRow()]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useMemo(() => newIdempotencyKey(), [form, rows]);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    const built = toModelInput(form, rows, true);
    setErrors(built.errors);
    setFailure(null);
    if (!built.input) return;
    setBusy(true);
    try {
      const { body } = await createModelServiceSpec(built.input, key);
      navigate(`/purchasing/model-service-specs/${body.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      if (problem.errors) setErrors(problem.errors);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New model service specification" description="A code is unique within its legal entity and cannot be renamed; deactivate a model instead of deleting it." />
      <form className={styles.form} onSubmit={onSubmit} noValidate>
        <Card title="Model">
          <ModelFields id="ms" form={form} set={(patch) => setForm((f) => ({ ...f, ...patch }))} errors={errors} creating />
        </Card>
        <Card title="Outline">
          <OutlineEditor idPrefix="msr" rows={rows} onChange={setRows} errors={errors} />
        </Card>
        <Alert tone="danger" live="alert">
          {failure ? <ProblemMessage problem={failure} /> : null}
        </Alert>
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save model
          </Button>
          <Link className={buttonClassName("ghost")} to="/purchasing/model-service-specs">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}

const formOf = (m: ModelServiceSpec): ModelForm => ({ legalEntityId: m.legalEntityId, code: m.code, name: m.name, currency: m.currency, status: m.status });

/** One model: read it, change it under its version and see the outline it copies into a line. */
export function ModelServiceSpecDetailPage() {
  const { id = "" } = useParams();
  const resource = useResource(async (signal) => (await getModelServiceSpec(id, signal)).body, [id]);
  const [saved, setSaved] = useState<ModelServiceSpec | null>(null);
  const [draft, setDraft] = useState<ModelForm>(blankModel);
  const [rows, setRows] = useState<OutlineDraftRow[]>([]);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function show(m: ModelServiceSpec) {
    setSaved(m);
    setDraft(formOf(m));
    setRows(draftFromOutline(m.outline));
  }
  useEffect(() => {
    if (resource.data) show(resource.data);
  }, [resource.data]);

  function reload() {
    setFailure(null);
    setNotice(null);
    resource.reload();
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!saved) return;
    const built = toModelInput(draft, rows, false);
    setErrors(built.errors);
    setFailure(null);
    setNotice(null);
    if (!built.input) return;
    setBusy(true);
    try {
      const { body } = await changeModelServiceSpec(saved.id, built.input, saved.version);
      show(body);
      setNotice(`Model saved as version ${body.version}. Lines that already copied it keep their own outline.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "PRC-SRV-007";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={saved ? `Model ${saved.code}` : "Model service specification"}
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/model-service-specs">
            All models
          </Link>
        }
      />
      {resource.error ? <LoadError what="the model service specification" error={resource.error} onRetry={resource.reload} /> : null}
      {resource.loading && !saved ? <Skeleton /> : null}
      {saved ? (
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <Card>
            <Details
              items={[
                { label: "Code", value: saved.code, mono: true },
                { label: "Legal entity", value: saved.legalEntityId, mono: true },
                { label: "Planned total", value: `${saved.plannedTotal} ${saved.currency}`, mono: true },
                { label: "Status", value: <StatusBadge status={saved.status} /> },
                { label: "Version", value: String(saved.version), mono: true },
              ]}
            />
          </Card>
          <Card title="Outline now">
            <OutlineTable rows={saved.outline} currency={saved.currency} />
          </Card>
          <Card title="Change">
            <ModelFields id="msd" form={draft} set={(patch) => setDraft((d) => ({ ...d, ...patch }))} errors={errors} creating={false} />
          </Card>
          <Card title="Change the outline">
            <OutlineEditor idPrefix="msdr" rows={rows} onChange={setRows} errors={errors} />
          </Card>
          <Alert
            tone="danger"
            live="alert"
            action={
              stale ? (
                <Button type="button" variant="secondary" onClick={reload}>
                  Reload model
                </Button>
              ) : undefined
            }
          >
            {failure
              ? stale
                ? <ProblemMessage problem={failure} message="Someone else changed this model since you opened it. Reload to see their change, then save again." />
                : <ProblemMessage problem={failure} />
              : null}
          </Alert>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save changes
            </Button>
          </div>
        </form>
      ) : null}
    </div>
  );
}
