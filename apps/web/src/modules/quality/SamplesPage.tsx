import { useRef, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listSamples, newKey, registerSample, type NewSample, type OrderView, type Problem, type Sample, type SampleKind, type SampleStatus } from "./api/client";
import { DECIMAL, dash, formatDate, pickFields, qty, SAMPLE_KIND_LABELS, SAMPLE_KINDS, SAMPLE_STATUS_LABELS, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function SampleStatusBadge({ status }: { status: SampleStatus }) {
  const s = SAMPLE_STATUS_LABELS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

function sampleColumns(withOrder: boolean): DataTableColumn<Sample>[] {
  const cols: DataTableColumn<Sample>[] = [
    { key: "id", header: "Sample", mono: true, render: (row) => shortId(row.id) },
    { key: "kind", header: "Kind", render: (row) => SAMPLE_KIND_LABELS[row.kind] ?? row.kind },
    { key: "quantity", header: "Quantity", align: "end", mono: true, render: (row) => `${qty(row.quantity)} ${row.uom}` },
    { key: "storageLocation", header: "Container / location", render: (row) => [row.container, row.storageLocation].filter(Boolean).join(", ") || dash },
    { key: "primaryIds", header: "From primaries", mono: true, render: (row) => (row.primaryIds.length > 0 ? row.primaryIds.map(shortId).join(", ") : dash) },
    { key: "retentionExpiresOn", header: "Retention until", render: (row) => (row.retentionExpiresOn ? formatDate(row.retentionExpiresOn) : dash) },
    { key: "status", header: "Status", render: (row) => <SampleStatusBadge status={row.status} /> },
  ];
  if (withOrder) {
    cols.splice(1, 0, {
      key: "qualityOrderId",
      header: "Quality order",
      mono: true,
      render: (row) => <Link to={`/quality/orders/${row.qualityOrderId}`}>{`Open order ${shortId(row.qualityOrderId)}`}</Link>,
    });
  }
  return cols;
}

/** Physical samples (FR-33.20): every order's samples, filtered to the retention samples due for disposal (AC-33.18). */
export function SamplesPage() {
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") ?? "") as SampleStatus | "";
  const state = useLoad((signal) => listSamples({ status }, signal), `samples|${status}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Physical samples"
        description="Primary, pooled and retention samples drawn for quality orders. A retention sample is due for disposal from its retention expiry: the batch expiry plus the retention years. Register a sample on its quality order."
      />
      <div className={styles.toolbar}>
        <FormField id="samples-status" label="Status">
          {(control) => (
            <Select
              {...control}
              value={status}
              onChange={(event) => {
                const next = new URLSearchParams(params);
                if (event.target.value) next.set("status", event.target.value);
                else next.delete("status");
                setParams(next);
              }}
            >
              <option value="">All samples</option>
              {(Object.keys(SAMPLE_STATUS_LABELS) as SampleStatus[]).map((s) => (
                <option key={s} value={s}>
                  {SAMPLE_STATUS_LABELS[s].label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the samples" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Physical samples"
          columns={sampleColumns(true)}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            status === "DUE_FOR_DISPOSAL"
              ? { title: "No samples are due for disposal", description: "Retention samples appear here on the day their retention expires." }
              : { title: "No physical samples yet", description: "Open a quality order to register the samples drawn from its lot." }
          }
        />
      )}
    </div>
  );
}

export interface SampleValues {
  kind: SampleKind;
  quantity: string;
  container: string;
  storageLocation: string;
  condition: string;
  batchExpiresOn: string;
  retentionYears: string;
  primaryIds: string[];
}

type SampleErrors = Partial<Record<keyof SampleValues, string>>;

/** Checks that mirror the service's ValidateSample, and the request the form sends. */
export function validateSample(orderId: string, values: SampleValues): { errors: SampleErrors; input?: NewSample } {
  const errors: SampleErrors = {};
  const q = values.quantity.trim();
  if (!DECIMAL.test(q) || /^0+(\.0+)?$/.test(q)) errors.quantity = "Enter a quantity above 0, up to 6 decimals.";
  if (values.container.trim().length > 60) errors.container = "Keep the container to 60 characters.";
  if (values.storageLocation.trim().length > 60) errors.storageLocation = "Keep the location to 60 characters.";
  if (values.condition.trim().length > 120) errors.condition = "Keep the condition to 120 characters.";
  const expiry = values.batchExpiresOn.trim();
  if (expiry === "" && values.kind === "RESERVE") errors.batchExpiresOn = "A retention sample needs the batch expiry date.";
  else if (expiry !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(expiry)) errors.batchExpiresOn = "Enter the date as YYYY-MM-DD.";
  const years = values.retentionYears.trim();
  if (years !== "" && (!/^\d{1,2}$/.test(years) || Number(years) > 10)) errors.retentionYears = "Enter 0 to 10 years.";
  if (values.kind === "POOLED" && values.primaryIds.length === 0) errors.primaryIds = "Choose the primary samples this sample is pooled from.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewSample = { qualityOrderId: orderId, kind: values.kind, quantity: q };
  if (values.container.trim()) input.container = values.container.trim();
  if (values.storageLocation.trim()) input.storageLocation = values.storageLocation.trim();
  if (values.condition.trim()) input.condition = values.condition.trim();
  if (expiry) input.batchExpiresOn = expiry;
  if (years !== "") input.retentionYears = Number(years);
  if (values.kind === "POOLED") input.primaryIds = values.primaryIds;
  return { errors, input };
}

const emptySample: SampleValues = {
  kind: "PRIMARY",
  quantity: "",
  container: "",
  storageLocation: "",
  condition: "",
  batchExpiresOn: "",
  retentionYears: "",
  primaryIds: [],
};

/** The physical samples of one order with a form to register the next (FR-33.20). */
export function SamplesPanel({ order }: { order: OrderView }) {
  const [refresh, setRefresh] = useState(0);
  const state = useLoad((signal) => listSamples({ qualityOrderId: order.id }, signal), `order-samples|${order.id}|${refresh}`);
  const [values, setValues] = useState<SampleValues>(emptySample);
  const [errors, setErrors] = useState<SampleErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const primaries = (state.data ?? []).filter((s) => s.kind === "PRIMARY");

  function update(patch: Partial<SampleValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const checked = validateSample(order.id, values);
    setErrors(checked.errors);
    if (!checked.input) return;
    setBusy(true);
    try {
      await registerSample(checked.input, key.current);
      key.current = newKey();
      setValues({ ...emptySample, kind: values.kind });
      setRefresh((n) => n + 1);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = pickFields(problem.fields, ["kind", "quantity", "container", "storageLocation", "condition", "batchExpiresOn", "retentionYears", "primaryIds"] as const);
      if (problem.code === "QM-0181") fieldErrors.primaryIds = "Pool only primary samples of this order.";
      if (problem.code === "QM-0182") fieldErrors.quantity = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: "container" | "storageLocation" | "condition" | "batchExpiresOn" | "retentionYears" | "quantity", label: string, hint?: string, optional = true) => (
    <FormField id={`sample-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          type={id === "batchExpiresOn" ? "date" : "text"}
          inputMode={id === "quantity" ? "decimal" : id === "retentionYears" ? "numeric" : undefined}
          value={values[id]}
          invalid={Boolean(errors[id])}
          onChange={(event) => update({ [id]: event.target.value } as Partial<SampleValues>)}
        />
      )}
    </FormField>
  );

  return (
    <Card title="Physical samples">
      <div className={styles.stack}>
        {state.error ? (
          <Alert tone="danger" title="Could not load the samples" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : (
          <DataTable
            label="Samples of this order"
            columns={sampleColumns(false)}
            rows={state.data ?? []}
            getRowKey={(row) => row.id}
            loading={state.loading && !state.data}
            empty={{ title: "No samples registered", description: "Register each primary sample drawn from the lot, any pooled sample, and the retention sample." }}
          />
        )}
        {order.status !== "CANCELLED" ? (
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Register a sample">
            <FormField id="sample-kind" label="Kind">
              {(control) => (
                <Select {...control} value={values.kind} onChange={(event) => update({ kind: event.target.value as SampleKind, primaryIds: [] })}>
                  {SAMPLE_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {SAMPLE_KIND_LABELS[k]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {text("quantity", `Quantity (${order.uom})`, undefined, false)}
            {text("container", "Container")}
            {text("storageLocation", "Storage location")}
            {text("condition", "Storage condition", "For example 25 °C / 60 % RH.")}
            {text("batchExpiresOn", "Batch expiry", values.kind === "RESERVE" ? "The retention runs from this date." : undefined, values.kind !== "RESERVE")}
            {values.kind === "RESERVE" ? text("retentionYears", "Retention years", "Default 1 year past batch expiry; 0 to 10.") : null}
            {values.kind === "POOLED" ? (
              <fieldset className={styles.valueGroup} aria-describedby={errors.primaryIds ? "sample-primaryIds-error" : undefined}>
                <legend className={styles.legend}>Pooled from</legend>
                {primaries.length === 0 ? <p className={styles.note}>Register the primary samples first.</p> : null}
                {primaries.map((p) => (
                  <label key={p.id} className={styles.check}>
                    <Input
                      type="checkbox"
                      checked={values.primaryIds.includes(p.id)}
                      onChange={(event) =>
                        update({ primaryIds: event.target.checked ? [...values.primaryIds, p.id] : values.primaryIds.filter((id) => id !== p.id) })
                      }
                    />
                    {`Primary ${shortId(p.id)}, ${qty(p.quantity)} ${p.uom}`}
                  </label>
                ))}
                {errors.primaryIds ? (
                  <p id="sample-primaryIds-error" className={styles.fail} role="alert">
                    {errors.primaryIds}
                  </p>
                ) : null}
              </fieldset>
            ) : null}
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not register the sample" live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Register sample
              </Button>
            </div>
          </form>
        ) : null}
      </div>
    </Card>
  );
}
