import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listAgreementHistory,
  listAgreementVisits,
  newKey,
  recordAgreementVisit,
  VISIT_KINDS,
  type Agreement,
  type AgreementChange,
  type AgreementVisit,
  type AgreementVisitInput,
  type Problem,
  type VisitKind,
} from "./api/client";
import { dash, DATE, formatDateTime, sentence, shortId, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface VisitValues {
  lineIndex: string;
  kind: VisitKind;
  visitDate: string;
  workOrderId: string;
  note: string;
}

/** Checks that mirror the service (agreements.go recordVisit). */
export function validateVisit(values: VisitValues, lineCount: number): { errors: Record<string, string>; input?: AgreementVisitInput } {
  const errors: Record<string, string> = {};
  const line = Number(values.lineIndex);
  if (!Number.isInteger(line) || line < 0 || line >= lineCount) errors.lineIndex = "Choose one of the agreement's lines.";
  if (!DATE.test(values.visitDate)) errors.visitDate = "Choose the day of the visit.";
  const wo = values.workOrderId.trim();
  if (wo && !UUID.test(wo)) errors.workOrderId = "Enter the work order's UUID, or leave it empty.";
  if (values.note.length > 500) errors.note = "Keep the note to 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { lineIndex: line, kind: values.kind, visitDate: values.visitDate, ...(wo ? { workOrderId: wo } : {}), ...(values.note ? { note: values.note } : {}) } };
}

const visitColumns: DataTableColumn<AgreementVisit>[] = [
  { key: "visitDate", header: "Date", mono: true },
  { key: "lineIndex", header: "Line", align: "end", render: (row) => row.lineIndex + 1 },
  { key: "kind", header: "Kind", render: (row) => sentence(row.kind) },
  { key: "workOrderId", header: "Work order", mono: true, render: (row) => (row.workOrderId ? shortId(row.workOrderId) : dash) },
  { key: "note", header: "Note", render: (row) => row.note || dash },
];

const historyColumns: DataTableColumn<AgreementChange & { id: string }>[] = [
  { key: "at", header: "When", mono: true, render: (row) => formatDateTime(row.at) },
  { key: "change", header: "Change", render: (row) => `${sentence(row.from)} to ${sentence(row.to)}` },
  { key: "reason", header: "Reason", render: (row) => row.reason || dash },
  { key: "actor", header: "By", mono: true, render: (row) => row.actor || dash },
];

/** Visits used per line, counted from the visit list. */
export function visitsUsed(visits: AgreementVisit[], lineIndex: number): number {
  return visits.filter((v) => v.lineIndex === lineIndex).length;
}

/** Included visits consumed and the form to record one (FR-45.3), and the status history. */
export function AgreementVisitsPanel({ agreement, onRecorded }: { agreement: Agreement; onRecorded: () => void }) {
  const visits = useLoad((signal) => listAgreementVisits(agreement.id, signal), `${agreement.id}:${agreement.visitsConsumed}`);
  const history = useLoad((signal) => listAgreementHistory(agreement.id, signal), `${agreement.id}:${agreement.version}`);
  const [values, setValues] = useState<VisitValues>({ lineIndex: "0", kind: "PREVENTIVE", visitDate: today(), workOrderId: "", note: "" });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<VisitValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateVisit(values, agreement.lines.length);
    setErrors(found);
    setFailure(null);
    setNotice("");
    if (!input) return;
    setBusy(true);
    try {
      const recorded = await recordAgreementVisit(agreement.id, input, key.current);
      key.current = newKey();
      setNotice(`Visit recorded; ${recorded.visitsLeft} left on line ${input.lineIndex + 1}.`);
      setValues((v) => ({ ...v, workOrderId: "", note: "" }));
      onRecorded();
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Card title="Visits" headingLevel={3}>
        {visits.error ? (
          <Alert tone="danger" title="Could not load the visits" live="alert" action={<Button size="sm" onClick={visits.reload}>Try again</Button>}>
            {visits.error.message} ({visits.error.code})
          </Alert>
        ) : (
          <>
            <p className={styles.muted}>
              {agreement.lines
                .map((l, i) => `Line ${i + 1}: ${visitsUsed(visits.data ?? [], i)} of ${l.includedVisits} included visits used`)
                .join(". ")}
              {agreement.lines.length > 0 ? "." : ""}
            </p>
            <DataTable
              label="Agreement visits"
              columns={visitColumns}
              rows={visits.data ?? []}
              getRowKey={(row) => row.id}
              loading={visits.loading}
              empty={{ title: "No visits recorded", description: "A visit consumes one included visit of its line." }}
            />
          </>
        )}
      </Card>
      {agreement.status === "ACTIVE" ? (
        <Card title="Record a visit" headingLevel={3}>
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Record a visit">
            <FormField id="visit-line" label="Line" error={errors.lineIndex}>
              {(control) => (
                <Select {...control} value={values.lineIndex} onChange={(e) => update({ lineIndex: e.target.value })} invalid={Boolean(errors.lineIndex)}>
                  {agreement.lines.map((l, i) => (
                    <option key={i} value={String(i)}>
                      {`Line ${i + 1} — ${l.includedVisits} included`}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="visit-kind" label="Kind">
              {(control) => (
                <Select {...control} value={values.kind} onChange={(e) => update({ kind: e.target.value as VisitKind })}>
                  {VISIT_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {sentence(k)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="visit-date" label="Visit date" error={errors.visitDate}>
              {(control) => <Input {...control} type="date" value={values.visitDate} onChange={(e) => update({ visitDate: e.target.value })} invalid={Boolean(errors.visitDate)} />}
            </FormField>
            <FormField id="visit-work-order" label="Work order" hint="A work order of this agreement consumes at most one visit." error={errors.workOrderId} optional>
              {(control) => (
                <Input {...control} className={styles.mono} value={values.workOrderId} onChange={(e) => update({ workOrderId: e.target.value })} invalid={Boolean(errors.workOrderId)} />
              )}
            </FormField>
            <FormField id="visit-note" label="Note" error={errors.note} optional>
              {(control) => <Input {...control} value={values.note} onChange={(e) => update({ note: e.target.value })} invalid={Boolean(errors.note)} />}
            </FormField>
            <div className={styles.formActions}>
              <Alert tone="success" live="status">
                {notice}
              </Alert>
            </div>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not record the visit" live="alert">
                  {failure.message} ({failure.code})
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Record visit
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Card title="Status history" headingLevel={3}>
        {history.error ? (
          <Alert tone="danger" title="Could not load the history" live="alert" action={<Button size="sm" onClick={history.reload}>Try again</Button>}>
            {history.error.message} ({history.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Agreement status history"
            columns={historyColumns}
            rows={(history.data ?? []).map((c, i) => ({ ...c, id: String(i) }))}
            getRowKey={(row) => row.id}
            loading={history.loading}
            empty={{ title: "No status changes yet", description: "Activating, suspending or canceling the agreement is logged here." }}
          />
        )}
      </Card>
    </>
  );
}
