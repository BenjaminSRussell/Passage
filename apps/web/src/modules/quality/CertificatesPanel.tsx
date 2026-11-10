import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listCertificates,
  newKey,
  recordCertificate,
  waiveCertificate,
  type CertificateRecord,
  type CertType,
  type NewCertificate,
  type OrderView,
  type Problem,
  type TestLine,
} from "./api/client";
import { CERT_LABELS, CERT_STATUS_LABELS, dash, formatDateTime, shortId, SIGNED_DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const OTHER = "__other__";
const CERT_TYPES: CertType[] = ["COA", "COC", "EN10204_31"];

export interface CertificateValues {
  type: CertType;
  documentRef: string;
  /** Per test id: the number of a variable test, or the chosen outcome of an attribute test. */
  values: Record<string, string>;
  /** Per attribute test id: the outcome id typed when OTHER is chosen. */
  other: Record<string, string>;
}

/** Checks that mirror the service's ValidateCertificate, and the request the form sends. Empty tests are left out. */
export function validateCertificate(form: CertificateValues, tests: TestLine[]): { errors: Record<string, string>; input?: NewCertificate } {
  const errors: Record<string, string> = {};
  const ref = form.documentRef.trim();
  if (ref.length > 200) errors.documentRef = "Keep the document reference to 200 characters.";
  const values: NewCertificate["values"] = {};
  for (const test of tests) {
    const raw = (form.values[test.testId] ?? "").trim();
    if (raw === "") continue;
    if (test.kind === "VARIABLE") {
      if (!SIGNED_DECIMAL.test(raw)) errors[test.testId] = "Enter a number with up to 6 decimals.";
      else values[test.testId] = { value: raw };
    } else {
      const outcome = raw === OTHER ? (form.other[test.testId] ?? "").trim() : raw;
      if (!UUID.test(outcome)) errors[test.testId] = "Enter the outcome id (a UUID).";
      else values[test.testId] = { outcome };
    }
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { type: form.type, values, ...(ref ? { documentRef: ref } : {}) } };
}

function columns(tests: TestLine[]): DataTableColumn<CertificateRecord>[] {
  const names = new Map(tests.map((t) => [t.testId, t.name || shortId(t.testId)]));
  return [
    { key: "type", header: "Certificate", render: (row) => CERT_LABELS[row.type] ?? row.type },
    { key: "documentRef", header: "Document", mono: true, render: (row) => row.documentRef || dash },
    {
      key: "inSpec",
      header: "Result",
      render: (row) =>
        row.waiver ? <Badge tone="info">Waived</Badge> : <span className={row.inSpec ? styles.pass : styles.fail}>{row.inSpec ? "In specification" : "Out of specification"}</span>,
    },
    {
      key: "deviations",
      header: "Deviations or reason",
      render: (row) => (row.waiver ? (row.reason ?? dash) : row.deviations.length > 0 ? row.deviations.map((d) => names.get(d) ?? d).join(", ") : dash),
    },
    {
      key: "nonconformanceId",
      header: "Nonconformance",
      render: (row) => (row.nonconformanceId ? <Link to={`/quality/nonconformances/${row.nonconformanceId}`}>{`Open NCR ${shortId(row.nonconformanceId)}`}</Link> : dash),
    },
    { key: "recordedBy", header: "Recorded by", render: (row) => row.recordedBy || dash },
    { key: "recordedAt", header: "Recorded", render: (row) => formatDateTime(row.recordedAt) },
  ];
}

/**
 * Incoming vendor certificates of one order (FR-33.15): the requirement and its gate
 * status (BR-33.13), the receipts and waivers, a form to record a receipt against the
 * order's tests, and a waiver with a reason.
 */
export function CertificatesPanel({ order, onChanged }: { order: OrderView; onChanged(): void }) {
  const [refresh, setRefresh] = useState(0);
  const [formShown, setFormShown] = useState(false);
  const state = useLoad((signal) => listCertificates(order.id, signal), `certificates|${order.id}|${refresh}`);
  const status = state.data?.certificateStatus ?? order.certificateStatus;
  const required = order.certificateRequired ?? "NONE";
  const open = order.status !== "CLOSED" && order.status !== "CANCELLED";
  const s = CERT_STATUS_LABELS[status] ?? { label: status, tone: "neutral" as const };

  function changed() {
    setRefresh((n) => n + 1);
    onChanged();
  }

  return (
    <Card title="Incoming certificates" actions={<Badge tone={s.tone}>{s.label}</Badge>}>
      <div className={styles.stack}>
        <p className={styles.note}>
          {required === "NONE"
            ? "This order requires no vendor certificate. A certificate recorded here is checked against the test group but does not gate the decision."
            : `This order requires a ${CERT_LABELS[required].toLowerCase()} before any quantity is accepted.`}
        </p>
        {status === "MISSING" || status === "RECEIVED_NOK" ? (
          <Alert tone="warning" title="Acceptance is blocked">
            {status === "MISSING"
              ? "Record the required certificate, or waive it with a reason, before accepting quantity (QM-0125)."
              : "The latest certificate is out of specification and raised a vendor nonconformance. Record a corrected certificate, waive it, or reject the lot."}
          </Alert>
        ) : null}
        {state.error ? (
          <Alert tone="danger" title="Could not load the certificates" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : (
          <DataTable
            label="Certificates"
            columns={columns(order.testGroup.tests)}
            rows={state.data?.items ?? []}
            getRowKey={(row) => row.id}
            loading={state.loading && !state.data}
            empty={{ title: "No certificates yet", description: "Record the vendor's certificate when it arrives with the delivery, by EDI or through the supplier portal." }}
          />
        )}
        {open && (formShown || (required !== "NONE" && status !== "RECEIVED_OK" && status !== "WAIVED")) ? (
          <RecordCertificateForm order={order} onRecorded={changed} />
        ) : open ? (
          <div className={styles.actions}>
            <Button size="sm" onClick={() => setFormShown(true)}>
              Record a certificate
            </Button>
          </div>
        ) : null}
        {open && required !== "NONE" && status !== "RECEIVED_OK" && status !== "WAIVED" ? <WaiverForm order={order} onWaived={changed} /> : null}
      </div>
    </Card>
  );
}

function RecordCertificateForm({ order, onRecorded }: { order: OrderView; onRecorded(): void }) {
  const tests = order.testGroup.tests;
  const initialType: CertType = order.certificateRequired && order.certificateRequired !== "NONE" ? order.certificateRequired : "COA";
  const [form, setForm] = useState<CertificateValues>({ type: initialType, documentRef: "", values: {}, other: {} });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CertificateValues>) {
    setForm({ ...form, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setNotice(null);
    const checked = validateCertificate(form, tests);
    setErrors(checked.errors);
    if (!checked.input) return;
    setBusy(true);
    try {
      const saved = await recordCertificate(order.id, checked.input, key.current);
      key.current = newKey();
      setForm({ type: form.type, documentRef: "", values: {}, other: {} });
      setNotice(
        saved.inSpec
          ? "The certificate is in specification."
          : `The certificate is out of specification${saved.nonconformanceId ? ` and raised NCR ${shortId(saved.nonconformanceId)}` : ""}.`,
      );
      onRecorded();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Record<string, string> = {};
      for (const [name, msg] of Object.entries(problem.fields)) {
        if (name.startsWith("values.")) fieldErrors[name.slice("values.".length)] = msg;
        else if (name === "documentRef") fieldErrors.documentRef = msg;
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Record a certificate">
      <FormField id="cert-type" label="Certificate type">
        {(control) => (
          <Select {...control} value={form.type} onChange={(event) => update({ type: event.target.value as CertType })}>
            {CERT_TYPES.map((t) => (
              <option key={t} value={t}>
                {CERT_LABELS[t]}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="cert-documentRef" label="Document reference" optional hint="Upload id, EDI attachment or portal document number." error={errors.documentRef}>
        {(control) => <Input {...control} value={form.documentRef} invalid={Boolean(errors.documentRef)} onChange={(event) => update({ documentRef: event.target.value })} />}
      </FormField>
      <fieldset className={styles.valueGroup}>
        <legend className={styles.legend}>
          {form.type === "COC" ? "Values on the certificate (a conformance statement may carry none)" : "Values on the certificate; every required test needs one"}
        </legend>
        {tests.map((test) => {
          const label = `Certificate value: ${test.name || shortId(test.testId)}`;
          const raw = form.values[test.testId] ?? "";
          if (test.kind === "VARIABLE") {
            return (
              <FormField key={test.testId} id={`cert-${test.testId}`} label={label} optional={!test.required} error={errors[test.testId]}>
                {(control) => (
                  <Input
                    {...control}
                    inputMode="decimal"
                    value={raw}
                    invalid={Boolean(errors[test.testId])}
                    onChange={(event) => update({ values: { ...form.values, [test.testId]: event.target.value } })}
                  />
                )}
              </FormField>
            );
          }
          return (
            <div key={test.testId} className={styles.entry}>
              <FormField id={`cert-${test.testId}`} label={label} optional={!test.required} error={errors[test.testId]}>
                {(control) => (
                  <Select {...control} value={raw} invalid={Boolean(errors[test.testId])} onChange={(event) => update({ values: { ...form.values, [test.testId]: event.target.value } })}>
                    <option value="">Not on the certificate</option>
                    {(test.passingOutcomeIds ?? []).map((o) => (
                      <option key={o} value={o}>{`Passing outcome ${shortId(o)}`}</option>
                    ))}
                    <option value={OTHER}>Another outcome (fails)</option>
                  </Select>
                )}
              </FormField>
              {raw === OTHER ? (
                <FormField id={`cert-${test.testId}-other`} label="Certificate outcome id">
                  {(control) => (
                    <Input {...control} value={form.other[test.testId] ?? ""} onChange={(event) => update({ other: { ...form.other, [test.testId]: event.target.value } })} />
                  )}
                </FormField>
              ) : null}
            </div>
          );
        })}
      </fieldset>
      {failure || notice ? (
        <div className={styles.formActions}>
          {failure ? (
            <Alert tone="danger" title="Could not record the certificate" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          ) : null}
          {notice ? (
            <Alert tone="success" live="status">
              {notice}
            </Alert>
          ) : null}
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Record certificate
        </Button>
      </div>
    </form>
  );
}

function WaiverForm({ order, onWaived }: { order: OrderView; onWaived(): void }) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const r = reason.trim();
    if (r === "" || r.length > 500) {
      setError("Give the reason for the waiver, up to 500 characters.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      await waiveCertificate(order.id, r, key.current);
      key.current = newKey();
      setReason("");
      onWaived();
    } catch (failed) {
      setFailure(describeProblem(failed));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Waive the certificate">
      <FormField id="cert-waiver-reason" label="Waiver reason" hint="Needs the usage decision duty. The waiver is recorded with your name." error={error ?? undefined}>
        {(control) => (
          <Textarea
            {...control}
            rows={2}
            value={reason}
            invalid={Boolean(error)}
            onChange={(event) => {
              setReason(event.target.value);
              key.current = newKey();
            }}
          />
        )}
      </FormField>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not waive the certificate" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" loading={busy}>
          {`Waive the ${CERT_LABELS[order.certificateRequired ?? "NONE"].toLowerCase()}`}
        </Button>
      </div>
    </form>
  );
}
